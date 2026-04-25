package backup

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/flock"

	"github.com/wesm/fotobank/internal/db"
)

// RestoreResult describes a successful restore. PreRestoreSuffix is the
// common suffix appended to the moved-aside files (".pre-restore.{ns-
// timestamp}") so the operator can identify them in stderr output.
// MovedAside lists each file that was actually moved aside; missing
// sidecars (flash-loss recovery) are absent.
type RestoreResult struct {
	SnapshotPath     string
	DBPath           string
	PreRestoreSuffix string
	MovedAside       []string
}

// Restore replaces dbPath with the contents of snapshotPath, holding a
// non-blocking flock on lockPath for the duration to prevent races with
// a live server. On success, the previous DB and its sidecars are
// preserved at "{path}.pre-restore.{ns-timestamp}" — the operator
// deletes them when satisfied.
//
// Failure paths roll back: any post-move-aside failure restores the
// pre-restore trio so the operator's existing DB lands cleanly. Rollback
// errors are joined to the original via errors.Join — the original is
// the lead, with rollback errors as added context.
//
// A missing dbPath / dbPath-wal / dbPath-shm is tolerated (flash-loss
// recovery has nothing to move aside).
//
// Returns are named so the rollback-on-failure defer can wrap retErr
// with errors.Join.
func Restore(ctx context.Context, snapshotPath, dbPath, lockPath string) (res RestoreResult, retErr error) {
	// 1. Validate the snapshot file is a real SQLite DB.
	if err := ValidateSnapshot(ctx, snapshotPath); err != nil {
		return RestoreResult{}, fmt.Errorf("validate snapshot: %w", err)
	}

	// 2. Acquire the lifetime lock. Non-blocking; refuse if held.
	l := flock.New(lockPath)
	ok, err := l.TryLock()
	if err != nil {
		return RestoreResult{}, fmt.Errorf("flock: %w", err)
	}
	if !ok {
		return RestoreResult{}, fmt.Errorf("%w: %s", ErrServerHoldsLock, dbPath)
	}
	defer func() { _ = l.Unlock() }()

	// 3. Arm rollback BEFORE any move-aside. This guards against partial
	//    move-aside failure (first rename succeeds, second fails).
	suffix := ".pre-restore." + time.Now().UTC().Format("20060102T150405.000000000Z")
	var movedAside []string
	var success bool
	var openedDB *db.DB

	defer func() {
		if success {
			return
		}
		var rollbackErrs []error
		if openedDB != nil {
			rollbackErrs = append(rollbackErrs, openedDB.Close())
		}
		// Remove any freshly-installed files; ignore not-exist.
		// dbPath + ".incoming" is included to defend against a process
		// crash inside copyFile that strands the temp file — without
		// the rollback covering it, the next Restore would fail at
		// O_EXCL with a misleading "create incoming" error.
		for _, p := range []string{dbPath, dbPath + "-wal", dbPath + "-shm", dbPath + ".incoming"} {
			if rmErr := os.Remove(p); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
				rollbackErrs = append(rollbackErrs, fmt.Errorf("rollback remove %s: %w", p, rmErr))
			}
		}
		// Restore each moved-aside file, in reverse order.
		for i := len(movedAside) - 1; i >= 0; i-- {
			movedTo := movedAside[i]
			origPath := strings.TrimSuffix(movedTo, suffix)
			if rmErr := os.Rename(movedTo, origPath); rmErr != nil {
				rollbackErrs = append(rollbackErrs, fmt.Errorf("rollback rename %s -> %s: %w", movedTo, origPath, rmErr))
			}
		}
		// errors.Join wraps the original error with rollback context.
		// retErr is the named return, so this mutation IS observed by
		// the caller — that's why named returns are required here.
		if len(rollbackErrs) > 0 {
			retErr = errors.Join(append([]error{retErr}, rollbackErrs...)...)
		}
	}()

	// 4. Move-aside.
	for _, p := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		movedTo := p + suffix
		if rmErr := os.Rename(p, movedTo); rmErr != nil {
			if errors.Is(rmErr, os.ErrNotExist) {
				continue
			}
			return RestoreResult{}, fmt.Errorf("move aside %s: %w", p, rmErr)
		}
		movedAside = append(movedAside, movedTo)
	}

	// 5. Copy snapshot in atomically.
	if err := copyFile(snapshotPath, dbPath); err != nil {
		return RestoreResult{}, fmt.Errorf("copy snapshot: %w", err)
	}
	if err := syncDir(filepath.Dir(dbPath)); err != nil {
		return RestoreResult{}, fmt.Errorf("fsync db dir: %w", err)
	}

	// 6. Forward-port the schema. db.Open enables WAL and runs migrations
	//    idempotently. Close immediately on success so the server can
	//    open the DB freshly.
	openedDB, err = db.Open(dbPath)
	if err != nil {
		return RestoreResult{}, fmt.Errorf("open restored db: %w", err)
	}
	if err := openedDB.Close(); err != nil {
		openedDB = nil // already closed; don't double-close in rollback
		return RestoreResult{}, fmt.Errorf("close restored db: %w", err)
	}
	openedDB = nil

	// 7. Disarm rollback.
	success = true
	return RestoreResult{
		SnapshotPath:     snapshotPath,
		DBPath:           dbPath,
		PreRestoreSuffix: suffix,
		MovedAside:       append([]string(nil), movedAside...),
	}, nil
}

// ValidateSnapshot opens path with the same DSN as snapshot creation
// (mode=rw, no create) and runs PRAGMA integrity_check to confirm it's
// a real, intact SQLite database. Used by Restore as a precondition
// and by the CLI `backup restore --dry-run` so an operator finds out
// about a bad snapshot before any move-aside runs. Reads only — the
// rw mode is just to share buildDSN; integrity_check does not write.
func ValidateSnapshot(ctx context.Context, path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	dsn := buildDSN(path)
	d, err := sql.Open("sqlite", dsn)
	if err != nil {
		return fmt.Errorf("open snapshot: %w", err)
	}
	defer d.Close()
	var s string
	if err := d.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&s); err != nil {
		return fmt.Errorf("integrity_check: %w", err)
	}
	if s != "ok" {
		return fmt.Errorf("integrity_check returned %q", s)
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	defer in.Close()
	tmp := dst + ".incoming"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create incoming: %w", err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("copy: %w", err)
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("fsync incoming: %w", err)
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close incoming: %w", err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename incoming: %w", err)
	}
	return nil
}
