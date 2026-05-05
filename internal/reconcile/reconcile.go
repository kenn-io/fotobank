// Package reconcile walks NAS and the media table to surface drift:
// bytes without rows (orphans), rows without bytes (missing), bytes
// whose on-disk size disagrees with the DB, and stale temp files left
// behind by a crashed writer.
package reconcile

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wesm/fotobank/internal/exifread"
	"github.com/wesm/fotobank/internal/media"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/search/index"
	"github.com/wesm/fotobank/internal/storage"
)

// defaultTempGrace is the minimum age a ".tmp-…" file must reach before
// reconcile considers it stale. Callers override via Options.TempGrace.
const defaultTempGrace = time.Hour

// tmpMarker is the substring used by storage.NASOnly to tag in-progress
// writes; any file whose basename contains this marker is a temp file
// rather than an orphan.
const tmpMarker = ".tmp-"

// Report lists the drift between NAS bytes and the media table.
type Report struct {
	Orphans []Orphan `json:"orphans"`
	// Missing carries the slim ReconcileRow projection (id, path, size,
	// type, lens_model) — sufficient for CLI display and Delete by id.
	// Reconcile never reads other columns from these rows, so paying
	// the full Media scan (30 columns, ~22 sql.Null* boxes per row)
	// for the Missing path was pure overhead.
	Missing      []media.ReconcileRow `json:"missing"`
	SizeMismatch []SizeMismatch       `json:"size_mismatch"`
	StaleTemps   []string             `json:"stale_temps"`
	// DeletedRows is the number of Missing rows removed when
	// Options.CommitDeletes is true.
	DeletedRows int `json:"deleted_rows"`
	// DeletedTemps is the number of StaleTemps files removed when
	// Options.CommitTemps is true.
	DeletedTemps int `json:"deleted_temps"`
	// LensModelBackfilled is the number of photo rows whose
	// lens_model column was filled in from EXIF during this pass.
	// Always zero for video rows or rows whose EXIF lacks LensModel.
	LensModelBackfilled int `json:"lens_model_backfilled"`
}

// Orphan identifies bytes under the owner's NAS root that no media row
// claims.
type Orphan struct {
	// Path is the storage-key-relative POSIX path (e.g., "2024/a.jpg").
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// SizeMismatch identifies a media row whose recorded size disagrees with
// the file currently on disk.
type SizeMismatch struct {
	MediaID    string `json:"media_id"`
	DBSize     int64  `json:"db_size"`
	OnDiskSize int64  `json:"on_disk_size"`
}

// Options drives a Reconcile run.
type Options struct {
	Owner      owners.Principal
	StorageKey string // the on-disk subdirectory name for Owner
	NASRoot    string
	// CommitDeletes removes phantom media rows (those in Missing).
	CommitDeletes bool
	// CommitTemps removes stale temp files (those in StaleTemps).
	CommitTemps bool
	// TempGrace is the minimum age of a ".tmp-…" file before it is
	// considered stale. Zero uses defaultTempGrace.
	TempGrace time.Duration
}

// skipDirs are directory names we never walk into — they hold state
// that lives outside the media/NAS contract (lock files, thumbnails).
var skipDirs = map[string]struct{}{
	".fotobank": {},
	".thumbs":   {},
}

// skipFiles are filenames we ignore at any depth.
var skipFiles = map[string]struct{}{
	".DS_Store": {},
}

// Reconcile walks the owner's NAS subtree, loads every media row for the
// owner, and returns a Report describing drift. When CommitDeletes is
// true, Missing rows are removed from the DB; when CommitTemps is true,
// StaleTemps files are removed from disk. The report always reflects the
// state observed at walk time, even after commit actions run.
func Reconcile(ctx context.Context, mediaRepo *media.Repo, opts Options) (Report, error) {
	if opts.NASRoot == "" {
		return Report{}, fmt.Errorf("reconcile: NASRoot is empty")
	}
	if err := storage.ValidateStorageKey(opts.StorageKey); err != nil {
		return Report{}, fmt.Errorf("reconcile: %w", err)
	}
	grace := opts.TempGrace
	if grace <= 0 {
		grace = defaultTempGrace
	}
	now := time.Now()

	ownerRoot := filepath.Join(opts.NASRoot, opts.StorageKey)
	diskMap, staleTemps, err := walkOwnerRoot(ownerRoot, now, grace)
	if err != nil {
		return Report{}, err
	}

	dbRows, err := mediaRepo.ListAllForReconcile(ctx, opts.Owner)
	if err != nil {
		return Report{}, fmt.Errorf("list media: %w", err)
	}
	dbMap := make(map[string]media.ReconcileRow, len(dbRows))
	for _, m := range dbRows {
		dbMap[m.Path] = m
	}

	rep := Report{StaleTemps: staleTemps}

	for path, size := range diskMap {
		m, ok := dbMap[path]
		if !ok {
			rep.Orphans = append(rep.Orphans, Orphan{Path: path, Size: size})
			continue
		}
		if m.Size != size {
			rep.SizeMismatch = append(rep.SizeMismatch, SizeMismatch{
				MediaID:    m.ID,
				DBSize:     m.Size,
				OnDiskSize: size,
			})
		}
	}

	for _, m := range dbRows {
		if _, ok := diskMap[m.Path]; !ok {
			rep.Missing = append(rep.Missing, m)
		}
	}

	if opts.CommitDeletes {
		for _, m := range rep.Missing {
			if err := mediaRepo.Delete(ctx, m.ID); err != nil {
				// Best-effort: keep going so a single bad row does not
				// block cleanup of the rest.
				continue
			}
			rep.DeletedRows++
		}
	}
	if opts.CommitTemps {
		for _, p := range rep.StaleTemps {
			if err := os.Remove(p); err != nil {
				continue
			}
			rep.DeletedTemps++
		}
	}
	rep.LensModelBackfilled = backfillLensModel(ctx, mediaRepo, ownerRoot, dbRows, diskMap)
	return rep, nil
}

// backfillLensModel re-reads EXIF for every photo row whose lens_model
// is currently empty AND whose on-disk file exists, writing lens_model
// when EXIF surfaces a non-empty value. The pass is additive: rows
// whose lens_model is already populated, rows missing on disk, video
// rows, and rows whose EXIF lacks LensModel are all left untouched.
//
// Per-row failures (open, parse, UPDATE) are swallowed so a single bad
// file does not abort the reconcile run; the count returned reflects
// only successful UPDATEs. The backfill runs in every Reconcile pass —
// this is safe because the UPDATE statement is gated on
// `lens_model IS NULL`, so no work is done after the first successful
// fill for a given row.
//
// Each successful UPDATE bundles a media_fts refresh in the same write
// transaction so search reads always see the lens column the row
// carries (lens_model is part of the FTS corpus). A mid-tx failure
// rolls both writes back and the row stays uncounted.
func backfillLensModel(
	ctx context.Context,
	mediaRepo *media.Repo,
	ownerRoot string,
	dbRows []media.ReconcileRow,
	diskMap map[string]int64,
) int {
	updated := 0
	for _, m := range dbRows {
		if m.Type != media.TypePhoto {
			continue
		}
		if m.LensModel != "" {
			continue
		}
		if _, ok := diskMap[m.Path]; !ok {
			continue
		}
		full := filepath.Join(ownerRoot, filepath.FromSlash(m.Path))
		meta, err := exifread.ExtractPhoto(full)
		if err != nil || meta.LensModel == "" {
			continue
		}
		var ok bool
		err = mediaRepo.WithWriteTx(ctx, func(tx *sql.Tx) error {
			var innerErr error
			ok, innerErr = mediaRepo.UpdateLensModelIfNullTx(ctx, tx, m.ID, meta.LensModel)
			if innerErr != nil {
				return innerErr
			}
			if !ok {
				// Nothing changed (id unknown or column already set);
				// skip the FTS refresh to keep the no-op cheap.
				return nil
			}
			return index.RefreshMediaFTS(ctx, tx, m.ID)
		})
		if err != nil || !ok {
			continue
		}
		updated++
	}
	return updated
}

// walkOwnerRoot walks ownerRoot and returns the map of storage-key-relative
// paths to sizes plus the absolute paths of stale temp files.
func walkOwnerRoot(ownerRoot string, now time.Time, grace time.Duration) (map[string]int64, []string, error) {
	diskMap := make(map[string]int64)
	var staleTemps []string

	err := filepath.WalkDir(ownerRoot, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) && p == ownerRoot {
				// Owner root missing is valid — owner has no bytes yet.
				return fs.SkipAll
			}
			return walkErr
		}
		name := d.Name()
		if d.IsDir() {
			if p == ownerRoot {
				return nil
			}
			if _, skip := skipDirs[name]; skip {
				return fs.SkipDir
			}
			return nil
		}
		if _, skip := skipFiles[name]; skip {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return fmt.Errorf("stat %s: %w", p, err)
		}
		if strings.Contains(name, tmpMarker) {
			if info.ModTime().Before(now.Add(-grace)) {
				staleTemps = append(staleTemps, p)
			}
			return nil
		}
		rel, err := relPath(ownerRoot, p)
		if err != nil {
			return err
		}
		diskMap[rel] = info.Size()
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("walk %s: %w", ownerRoot, err)
	}
	return diskMap, staleTemps, nil
}

// relPath returns the forward-slash path of p relative to ownerRoot.
func relPath(ownerRoot, p string) (string, error) {
	rel, err := filepath.Rel(ownerRoot, p)
	if err != nil {
		return "", fmt.Errorf("rel path: %w", err)
	}
	return filepath.ToSlash(rel), nil
}
