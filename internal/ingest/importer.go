package ingest

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/text/unicode/norm"

	"github.com/wesm/fotobank/internal/errs"
	"github.com/wesm/fotobank/internal/exifread"
	"github.com/wesm/fotobank/internal/media"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/storage"
)

// maxPhotoSeqAttempts bounds how many times we retry a timestamped
// photo path with a bumped sequence suffix before giving up. In
// practice this limit is never reached under correct usage.
const maxPhotoSeqAttempts = 16

// PlaceResolver returns a coarse human-readable label for a coordinate.
// Production callers pass a *geo.NaturalEarth; tests pass a stub or
// nil. When nil, the importer still extracts and stores
// latitude/longitude/gps_at from EXIF and leaves LocationLabel empty.
type PlaceResolver interface {
	Resolve(lat, lon float64) (label string, ok bool)
}

// Options controls an import run.
type Options struct {
	Owner             owners.Principal
	ConcurrentWorkers int
}

// Result summarises an import run.
type Result struct {
	Imported       int
	Duplicates     int
	PathCollisions int
	Failures       []error
}

// Importer wires discovery to extraction, storage, and the media repo.
type Importer struct {
	store  storage.Store
	repo   *media.Repo
	places PlaceResolver
	now    func() time.Time
	ai     AIEnqueuer
}

// NewImporter constructs an Importer with the default UTC wall clock.
// places may be nil — when nil, ingest still extracts and stores
// latitude/longitude/gps_at from EXIF and leaves LocationLabel empty.
// Production callers (the `fotobank import` and `fotobank gps backfill`
// CLIs) MUST pass a real *geo.NaturalEarth.
func NewImporter(store storage.Store, repo *media.Repo, places PlaceResolver) *Importer {
	return &Importer{
		store:  store,
		repo:   repo,
		places: places,
		now:    func() time.Time { return time.Now().UTC() },
		ai:     NoopAIEnqueuer{},
	}
}

// SetAIEnqueuer swaps in a production AIEnqueuer. Server boot calls this
// after the AI subsystem is initialized; CLIs that don't run the AI
// pipeline leave the default NoopAIEnqueuer in place.
func (imp *Importer) SetAIEnqueuer(e AIEnqueuer) { imp.ai = e }

// candidateOutcome is what a worker reports per candidate. id is set
// only when imported is true; the post-barrier pairing pass collects
// these to compute (owner, dir) keys touched by this batch.
type candidateOutcome struct {
	imported      bool
	duplicate     bool
	pathCollision bool
	id            string
	err           error
}

// ImportDirectory walks root, imports every supported candidate, and
// returns a summary. Callers must hold the import file lock before
// invoking this.
func (imp *Importer) ImportDirectory(ctx context.Context, root string, opts Options) (Result, error) {
	// Reject empty root explicitly: filepath.Abs("") silently
	// substitutes the process CWD, which would let a buggy caller
	// import the working directory. Pre-Task 8, Discover rejected
	// "" outright; the filepath.Abs hop introduced here would lose
	// that contract without this guard.
	if root == "" {
		return Result{}, fmt.Errorf("import root is empty")
	}
	// Resolve root once so buildMediaRow can derive a stable
	// root-relative ImportSourcePath from candidate paths (which
	// Discover already resolved against the same absolute root).
	sourceRoot, err := filepath.Abs(root)
	if err != nil {
		return Result{}, fmt.Errorf("resolve import root: %w", err)
	}
	var candidates []Candidate
	if err := Discover(sourceRoot, func(c Candidate) error {
		candidates = append(candidates, c)
		return nil
	}); err != nil {
		return Result{}, fmt.Errorf("discover: %w", err)
	}
	if len(candidates) == 0 {
		return Result{}, nil
	}

	workers := max(opts.ConcurrentWorkers, 1)
	jobs := make(chan Candidate, len(candidates))
	results := make(chan candidateOutcome, len(candidates))

	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for c := range jobs {
				if err := ctx.Err(); err != nil {
					results <- candidateOutcome{err: err}
					continue
				}
				results <- imp.processCandidate(ctx, c, opts.Owner, sourceRoot)
			}
		})
	}
	for _, c := range candidates {
		jobs <- c
	}
	close(jobs)
	wg.Wait()
	close(results)

	var res Result
	var importedIDs []string
	for out := range results {
		switch {
		case out.imported:
			res.Imported++
			if out.id != "" {
				importedIDs = append(importedIDs, out.id)
			}
		case out.duplicate:
			res.Duplicates++
		case out.pathCollision:
			res.PathCollisions++
		}
		if out.err != nil {
			res.Failures = append(res.Failures, out.err)
		}
	}

	// F2.2 post-barrier pairing pass. Pair-pass failures are
	// non-fatal: the rows are already in. We surface the error in
	// res.Failures so operators see it without aborting the import.
	if err := imp.runPairingPass(ctx, opts.Owner, importedIDs); err != nil {
		res.Failures = append(res.Failures, fmt.Errorf("pair pass: %w", err))
	}
	return res, nil
}

// runPairingPass runs the F2.2 post-barrier pairing pass. It computes
// the (owner, dir) keys touched by the just-imported batch, fetches
// the existing rows in those dirs (so a JPEG imported today pairs
// with a RAW imported last week and vice versa), runs Compute over
// the union, and applies PairUpdate writes via UpdatePairedWithID.
func (imp *Importer) runPairingPass(
	ctx context.Context,
	owner owners.Principal,
	importedIDs []string,
) error {
	if len(importedIDs) == 0 {
		return nil
	}
	imported, err := imp.repo.GetByIDs(ctx, importedIDs)
	if err != nil {
		return fmt.Errorf("get imported rows: %w", err)
	}
	// Directory keys are NFC-normalized so a JPEG with NFC path text
	// pairs with an existing RAW stored as NFD. ListByOwnerDirectories
	// applies the same normalization on the row side (see repo.go).
	dirSet := make(map[string]struct{})
	for _, m := range imported {
		if m.ImportSourcePath == "" {
			continue
		}
		dirSet[norm.NFC.String(filepath.Dir(m.ImportSourcePath))] = struct{}{}
	}
	if len(dirSet) == 0 {
		return nil
	}
	dirs := make([]string, 0, len(dirSet))
	for d := range dirSet {
		dirs = append(dirs, d)
	}
	existing, err := imp.repo.ListByOwnerDirectories(ctx, owner, dirs)
	if err != nil {
		return fmt.Errorf("list rows in touched dirs: %w", err)
	}
	// Combine, deduping by id (an imported row may also be returned
	// by ListByOwnerDirectories).
	seen := make(map[string]struct{}, len(imported)+len(existing))
	candidates := make([]PairCandidate, 0, len(imported)+len(existing))
	add := func(m media.Media) {
		if _, ok := seen[m.ID]; ok {
			return
		}
		seen[m.ID] = struct{}{}
		candidates = append(candidates, PairCandidate{
			ID:                m.ID,
			Owner:             m.Owner,
			ImportSourcePath:  m.ImportSourcePath,
			Class:             PairClassFromMime(m.MimeType),
			MimeType:          m.MimeType,
			CurrentPairedWith: m.PairedWithID,
		})
	}
	for _, m := range imported {
		add(m)
	}
	for _, m := range existing {
		add(m)
	}
	updates := Compute(candidates)
	// Compute is idempotent and commutative, so apply every update we
	// can and aggregate failures rather than aborting on the first one.
	var pairErrs []error
	for _, u := range updates {
		if err := imp.repo.UpdatePairedWithID(ctx, u.ID, u.PairedWithID); err != nil {
			pairErrs = append(pairErrs, fmt.Errorf("apply pair update for %s: %w", u.ID, err))
		}
	}
	if len(pairErrs) > 0 {
		return errors.Join(pairErrs...)
	}
	return nil
}

// processCandidate runs the full per-file pipeline: checksum, dedup
// lookup, extract, write, insert. It returns a candidateOutcome that
// the caller accumulates into Result. sourceRoot is the absolute
// import root, used by buildMediaRow to derive a stable
// root-relative ImportSourcePath.
func (imp *Importer) processCandidate(ctx context.Context, c Candidate, owner owners.Principal, sourceRoot string) candidateOutcome {
	info, err := os.Stat(c.Path)
	if err != nil {
		return candidateOutcome{err: fmt.Errorf("stat %s: %w", c.Path, err)}
	}
	checksum, err := Checksum(c.Path)
	if err != nil {
		return candidateOutcome{err: fmt.Errorf("checksum %s: %w", c.Path, err)}
	}
	if _, err := imp.repo.GetByOwnerChecksum(ctx, owner, checksum); err == nil {
		return candidateOutcome{duplicate: true}
	} else if !errors.Is(err, errs.ErrNotFound) {
		return candidateOutcome{err: fmt.Errorf("dedup lookup %s: %w", c.Path, err)}
	}

	meta := extractMetadata(c)
	switch c.Type {
	case media.TypeVideo:
		return imp.processVideo(ctx, c, owner, checksum, info.Size(), meta, sourceRoot)
	default:
		return imp.processPhoto(ctx, c, owner, checksum, info.Size(), meta, sourceRoot)
	}
}

// processPhoto handles the photo branch: write-insert-retry with a
// bumped seq on either NAS path collision or DB phantom row.
func (imp *Importer) processPhoto(ctx context.Context, c Candidate, owner owners.Principal, checksum string, size int64, meta exifread.Metadata, sourceRoot string) candidateOutcome {
	for seq := range maxPhotoSeqAttempts {
		key := resolvePhotoPath(c.Path, meta.Timestamp, seq)
		landed, err := imp.streamToStore(ctx, owner, key, c.Path)
		if errors.Is(err, storage.ErrPathOccupied) {
			continue
		}
		if err != nil {
			return candidateOutcome{err: fmt.Errorf("write %s: %w", c.Path, err)}
		}

		m := buildMediaRow(c, owner, landed, checksum, size, meta, imp.now(), imp.places, sourceRoot)
		switch err := imp.repo.Insert(ctx, m); {
		case err == nil:
			if aiErr := imp.ai.EnqueueForPhoto(ctx, m.ID); aiErr != nil {
				slog.Default().Warn("ai enqueue for photo failed",
					"media_id", m.ID, "err", aiErr)
			}
			return candidateOutcome{imported: true, id: m.ID}
		case errors.Is(err, media.ErrDuplicateChecksum):
			// A concurrent worker imported the same bytes first.
			_ = imp.store.Delete(ctx, owner, landed)
			return candidateOutcome{duplicate: true}
		case errors.Is(err, media.ErrDuplicatePath):
			// Phantom row: DB claims this path but we just wrote fresh
			// bytes at it. Delete our bytes and retry at seq+1.
			_ = imp.store.Delete(ctx, owner, landed)
			continue
		default:
			_ = imp.store.Delete(ctx, owner, landed)
			return candidateOutcome{err: fmt.Errorf("insert %s: %w", c.Path, err)}
		}
	}
	return candidateOutcome{pathCollision: true, err: fmt.Errorf("path collision for %s", c.Path)}
}

// processVideo handles the video branch: content-addressed path,
// orphan adoption when NAS bytes already match, pathCollision when they
// don't.
func (imp *Importer) processVideo(ctx context.Context, c Candidate, owner owners.Principal, checksum string, size int64, meta exifread.Metadata, sourceRoot string) candidateOutcome {
	key := resolveVideoPath(c.Path, checksum)
	landed, writeErr := imp.streamToStore(ctx, owner, key, c.Path)
	switch {
	case writeErr == nil:
		// Fresh write: insert normally.
	case errors.Is(writeErr, storage.ErrPathOccupied):
		// NAS has bytes at this path. Adopt if their checksum matches
		// (orphan); otherwise report a path collision.
		adopted, err := imp.tryAdoptVideoOrphan(ctx, owner, key, checksum)
		if err != nil {
			return candidateOutcome{err: fmt.Errorf("adopt orphan %s: %w", c.Path, err)}
		}
		if !adopted {
			return candidateOutcome{pathCollision: true, err: fmt.Errorf("path collision for %s", c.Path)}
		}
		landed = key
	default:
		return candidateOutcome{err: fmt.Errorf("write %s: %w", c.Path, writeErr)}
	}

	m := buildMediaRow(c, owner, landed, checksum, size, meta, imp.now(), imp.places, sourceRoot)
	switch err := imp.repo.Insert(ctx, m); {
	case err == nil:
		if aiErr := imp.ai.RecordVideoSkip(ctx, m.ID); aiErr != nil {
			slog.Default().Warn("ai video skip record failed",
				"media_id", m.ID, "err", aiErr)
		}
		return candidateOutcome{imported: true, id: m.ID}
	case errors.Is(err, errs.ErrAlreadyExists):
		// Video paths are content-addressed (movies/{md5}.ext). Any row
		// that collides on checksum OR path references these same bytes,
		// so the insert race winner — not this worker — owns them.
		// Never delete: tryAdoptVideoOrphan already verified the bytes
		// match our checksum before we reached the insert, and for a
		// fresh write our bytes are equivalent to any other worker's.
		return candidateOutcome{duplicate: true}
	default:
		if writeErr == nil {
			_ = imp.store.Delete(ctx, owner, landed)
		}
		return candidateOutcome{err: fmt.Errorf("insert %s: %w", c.Path, err)}
	}
}

// tryAdoptVideoOrphan inspects the existing NAS bytes at key and returns
// true iff their checksum matches the expected value. The caller uses
// this result to decide between orphan adoption (insert) and
// pathCollision (report and fail).
func (imp *Importer) tryAdoptVideoOrphan(ctx context.Context, owner owners.Principal, key, expected string) (bool, error) {
	if _, err := imp.store.Stat(ctx, owner, key); err != nil {
		return false, fmt.Errorf("stat orphan: %w", err)
	}
	rc, err := imp.store.ReadRange(ctx, owner, key, 0, -1)
	if err != nil {
		return false, fmt.Errorf("open orphan: %w", err)
	}
	defer rc.Close()
	h := md5.New()
	if _, err := io.Copy(h, rc); err != nil {
		return false, fmt.Errorf("checksum orphan: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)) == expected, nil
}

// extractMetadata calls the photo or video extractor and swallows any
// extraction error — the master spec says a file with unreadable
// metadata still imports with Timestamp=nil.
func extractMetadata(c Candidate) exifread.Metadata {
	switch c.Type {
	case media.TypePhoto:
		m, err := exifread.ExtractPhoto(c.Path)
		if err != nil {
			return exifread.Metadata{}
		}
		return m
	case media.TypeVideo:
		m, err := exifread.ExtractVideo(c.Path, c.MimeType)
		if err != nil {
			return exifread.Metadata{}
		}
		return m
	}
	return exifread.Metadata{}
}

// streamToStore opens src and hands it to the Store.Write no-clobber
// finalize. The file is closed before this function returns.
func (imp *Importer) streamToStore(ctx context.Context, owner owners.Principal, key, src string) (string, error) {
	f, err := os.Open(src)
	if err != nil {
		return "", fmt.Errorf("open source: %w", err)
	}
	defer f.Close()
	return imp.store.Write(ctx, owner, key, f)
}

// buildMediaRow assembles the media row. Nullable metadata fields are
// only populated when we actually have a value. sourceRoot is used to
// derive a stable root-relative ImportSourcePath; when c.Path is not
// under sourceRoot we fall back to the absolute path so the F2.2
// pairing pass can still group rows by directory.
func buildMediaRow(c Candidate, owner owners.Principal, key, checksum string, size int64, meta exifread.Metadata, importedAt time.Time, places PlaceResolver, sourceRoot string) media.Media {
	rel, err := filepath.Rel(sourceRoot, c.Path)
	if err != nil || strings.HasPrefix(rel, "..") {
		rel = c.Path
	}
	rel = filepath.ToSlash(rel)
	m := media.Media{
		ID:               uuid.NewString(),
		Owner:            owner,
		Type:             c.Type,
		MimeType:         c.MimeType,
		Path:             key,
		OriginalFilename: filepath.Base(c.Path),
		ImportedAt:       importedAt,
		Timestamp:        meta.Timestamp,
		Size:             size,
		Checksum:         checksum,
		Make:             meta.Make,
		Model:            meta.Model,
		FocalLength:      meta.FocalLength,
		Shutter:          meta.ShutterSpeed,
		ImportSourcePath: rel,
		ThumbStatus:      "pending",
	}
	if meta.Width > 0 {
		w := meta.Width
		m.Width = &w
	}
	if meta.Height > 0 {
		h := meta.Height
		m.Height = &h
	}
	if meta.ISO > 0 {
		iso := meta.ISO
		m.ISO = &iso
	}
	if meta.Aperture > 0 {
		a := meta.Aperture
		m.Aperture = &a
	}
	if meta.DurationMs > 0 {
		d := meta.DurationMs
		m.DurationMs = &d
	}
	if meta.Latitude != nil && meta.Longitude != nil {
		m.Latitude = meta.Latitude
		m.Longitude = meta.Longitude
		m.GPSAt = meta.GPSAt
		if places != nil {
			if label, ok := places.Resolve(*meta.Latitude, *meta.Longitude); ok {
				m.LocationLabel = label
			}
		}
	}
	return m
}

// resolvePhotoPath returns {YYYY}/{YYYYMMDD_HHMMSS_SEQ}.ext for a photo
// with a known timestamp, or unknown_date/{basename_SEQ}.ext otherwise.
// seq starts at 0; the caller bumps on ErrPathOccupied.
func resolvePhotoPath(sourcePath string, ts *time.Time, seq int) string {
	ext := filepath.Ext(sourcePath)
	if ts != nil {
		year := ts.UTC().Format("2006")
		base := ts.UTC().Format("20060102_150405")
		return path.Join(year, fmt.Sprintf("%s_%d%s", base, seq, ext))
	}
	name := strings.TrimSuffix(filepath.Base(sourcePath), ext)
	return path.Join("unknown_date", fmt.Sprintf("%s_%d%s", name, seq, ext))
}

// resolveVideoPath returns movies/{md5}.{ext}.
func resolveVideoPath(sourcePath, checksum string) string {
	ext := filepath.Ext(sourcePath)
	return path.Join("movies", checksum+ext)
}
