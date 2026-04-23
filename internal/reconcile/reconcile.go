// Package reconcile walks NAS and the media table to surface drift:
// bytes without rows (orphans), rows without bytes (missing), bytes
// whose on-disk size disagrees with the DB, and stale temp files left
// behind by a crashed writer.
package reconcile

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wesm/fotobank/internal/media"
	"github.com/wesm/fotobank/internal/owners"
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
	Orphans      []Orphan
	Missing      []media.Media
	SizeMismatch []SizeMismatch
	StaleTemps   []string
	// DeletedRows is the number of Missing rows removed when
	// Options.CommitDeletes is true.
	DeletedRows int
	// DeletedTemps is the number of StaleTemps files removed when
	// Options.CommitTemps is true.
	DeletedTemps int
}

// Orphan identifies bytes under the owner's NAS root that no media row
// claims.
type Orphan struct {
	// Path is the storage-key-relative POSIX path (e.g., "2024/a.jpg").
	Path string
	Size int64
}

// SizeMismatch identifies a media row whose recorded size disagrees with
// the file currently on disk.
type SizeMismatch struct {
	MediaID    string
	DBSize     int64
	OnDiskSize int64
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

	dbRows, err := mediaRepo.ListAll(ctx, opts.Owner)
	if err != nil {
		return Report{}, fmt.Errorf("list media: %w", err)
	}
	dbMap := make(map[string]media.Media, len(dbRows))
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
	return rep, nil
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
