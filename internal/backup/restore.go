package backup

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/gofrs/flock"

	"go.kenn.io/fotobank/internal/db"
)

// RestoreResult describes a successful restore. PreRestoreSuffix is the
// common suffix appended to the moved-aside files (".pre-restore.{ns-
// timestamp}.{rand}") so the operator can identify them in stderr
// output. The trailing 4-byte hex random ensures back-to-back restores
// can never collide on filename even at sub-nanosecond clock resolution.
// MovedAside lists each file that was actually moved aside; missing
// sidecars (flash-loss recovery) are absent.
type RestoreResult struct {
	SnapshotPath     string
	DBPath           string
	PreRestoreSuffix string
	MovedAside       []string
}

// Restore replaces dbPath with the contents of snapshotPath, holding a
// non-blocking exclusive flock on lockPath for the duration to prevent races
// with a live server or checkout creator. On success, the previous DB and its
// sidecars are preserved at "{path}.pre-restore.{ns-timestamp}" — the operator
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
	//    The suffix combines a UTC nanosecond timestamp (operator-readable)
	//    with 4 random hex bytes (collision-proof). Without the random
	//    tail, two restores at the same wall-clock nanosecond could let
	//    os.Rename's POSIX overwrite-semantics silently destroy the
	//    earlier preserved DB.
	rnd := make([]byte, 4)
	if _, err := rand.Read(rnd); err != nil {
		return RestoreResult{}, fmt.Errorf("generate suffix: %w", err)
	}
	suffix := ".pre-restore." +
		time.Now().UTC().Format("20060102T150405.000000000Z") + "." +
		hex.EncodeToString(rnd)
	var movedAside []string
	var success bool
	var openedDB *db.DB
	// copyComplete flips to true only after copyFile has installed the
	// snapshot at dbPath. Until that happens, dbPath either still holds
	// the original (move-aside not run, or run but failed before any
	// rename) or is empty (originals safely moved aside). In either
	// case the rollback must NOT delete dbPath/-wal/-shm, because doing
	// so could destroy an unmoved-aside original sidecar (see jobs
	// 15858, 15860, 15861 — partial move-aside failure scenarios).
	var copyComplete bool

	defer func() {
		if success {
			return
		}
		var rollbackErrs []error
		if openedDB != nil {
			rollbackErrs = append(rollbackErrs, openedDB.Close())
		}
		// Always try to remove the transient .incoming temp file in
		// case copyFile crashed mid-flight and left it stranded;
		// the next Restore would otherwise fail at O_EXCL.
		if rmErr := os.Remove(dbPath + ".incoming"); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
			rollbackErrs = append(rollbackErrs, fmt.Errorf("rollback remove %s: %w", dbPath+".incoming", rmErr))
		}
		// Only delete dbPath / -wal / -shm if we actually installed
		// them. Before copyComplete, those paths either already hold
		// the original (move-aside failed before completion) or are
		// empty; in neither case is there an installed file to clean
		// up, and deleting blindly would clobber an original sidecar.
		if copyComplete {
			for _, p := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
				if rmErr := os.Remove(p); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
					rollbackErrs = append(rollbackErrs, fmt.Errorf("rollback remove %s: %w", p, rmErr))
				}
			}
		}
		// Restore each moved-aside file, in reverse order.
		for _, movedTo := range slices.Backward(movedAside) {
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
	copyComplete = true
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
//
// Rejects zero-byte files (which mode=rw is willing to open as a fresh
// empty DB) and SQLite files with an empty schema, both of which would
// otherwise pass integrity_check but produce a useless restore that
// silently overwrites the live DB with an empty one.
func ValidateSnapshot(ctx context.Context, path string) error {
	// The snapshot DB may carry vec0 schema objects; register the
	// sqlite-vec extension before opening so SQLite can resolve
	// vec0 virtual tables when invoked from CLI tools that have not
	// yet driven the live DB through db.Open.
	db.RegisterSqliteVec()
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	if info.Size() == 0 {
		return fmt.Errorf("%s is zero bytes (not a SQLite database)", path)
	}
	dsn := buildDSN(path)
	d, err := sql.Open("sqlite3", dsn)
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
	// Reject DBs that lack the fotobank schema marker. golang-migrate
	// creates schema_migrations as the very first table on any opened
	// DB, so its presence is a strong signal the file came from a
	// fotobank instance and not from an unrelated SQLite app or a
	// freshly-initialized empty DB. A bare empty DB has zero rows in
	// sqlite_master; an unrelated SQLite DB with a dummy table also
	// fails this check unless it happens to be using golang-migrate
	// with the same convention.
	var hasMigrations int
	const q = `SELECT count(*) FROM sqlite_master
	           WHERE type='table' AND name='schema_migrations'`
	if err := d.QueryRowContext(ctx, q).Scan(&hasMigrations); err != nil {
		return fmt.Errorf("check schema_migrations: %w", err)
	}
	if hasMigrations == 0 {
		return fmt.Errorf("%s lacks schema_migrations table (not a fotobank snapshot)", path)
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
