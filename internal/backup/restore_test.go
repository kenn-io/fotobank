package backup

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/gofrs/flock"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func makeBaselineDB(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	require.NoError(t, err)
	defer db.Close()
	// Mirror what db.Open would do in production: enable WAL, run a
	// schema_migrations bootstrap so subsequent db.Open's idempotent
	// migrations land cleanly.
	_, err = db.Exec("PRAGMA journal_mode=WAL")
	require.NoError(t, err)
	_, err = db.Exec("CREATE TABLE schema_migrations (version BIGINT NOT NULL PRIMARY KEY, dirty BOOLEAN NOT NULL)")
	require.NoError(t, err)
	_, err = db.Exec("CREATE TABLE example (k TEXT PRIMARY KEY, v TEXT)")
	require.NoError(t, err)
}

func putRow(t *testing.T, dbPath, k, v string) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer db.Close()
	_, err = db.Exec("INSERT OR REPLACE INTO example(k, v) VALUES (?, ?)", k, v)
	require.NoError(t, err)
}

func readRow(t *testing.T, dbPath, k string) string {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer db.Close()
	var v string
	require.NoError(t, db.QueryRow("SELECT v FROM example WHERE k = ?", k).Scan(&v))
	return v
}

func TestRestoreReplacesDB(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "live.sqlite")
	snapPath := filepath.Join(tmp, "snap.sqlite")
	lockPath := dbPath + ".lock"

	makeBaselineDB(t, dbPath)
	putRow(t, dbPath, "live", "before-restore")

	// Take a snapshot WHILE the db is still in initial state, then mutate.
	makeBaselineDB(t, snapPath)
	putRow(t, snapPath, "live", "from-snapshot")

	res, err := Restore(context.Background(), snapPath, dbPath, lockPath)
	r.NoError(err)
	r.Equal(snapPath, res.SnapshotPath)
	r.Equal(dbPath, res.DBPath)
	r.NotEmpty(res.PreRestoreSuffix)
	r.NotEmpty(res.MovedAside)
	// dbPath now contains snapshot's content
	r.Equal("from-snapshot", readRow(t, dbPath, "live"))
	// Pre-restore copy still exists
	preDB := dbPath + res.PreRestoreSuffix
	r.FileExists(preDB)
}

func TestRestoreRefusesIfLockHeld(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "live.sqlite")
	snapPath := filepath.Join(tmp, "snap.sqlite")
	lockPath := dbPath + ".lock"

	makeBaselineDB(t, dbPath)
	makeBaselineDB(t, snapPath)

	holder := flock.New(lockPath)
	ok, err := holder.TryLock()
	r.NoError(err)
	r.True(ok)
	t.Cleanup(func() { _ = holder.Unlock() })

	_, err = Restore(context.Background(), snapPath, dbPath, lockPath)
	r.ErrorIs(err, ErrServerHoldsLock)
}

func TestRestoreAllowsMissingDB(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "live.sqlite") // does not exist
	snapPath := filepath.Join(tmp, "snap.sqlite")
	lockPath := dbPath + ".lock"

	makeBaselineDB(t, snapPath)
	putRow(t, snapPath, "live", "fresh-from-snap")

	res, err := Restore(context.Background(), snapPath, dbPath, lockPath)
	r.NoError(err)
	r.Empty(res.MovedAside, "no files to move aside on flash-loss recovery")
	r.Equal("fresh-from-snap", readRow(t, dbPath, "live"))
}

func TestRestoreRejectsCorruptedSnapshot(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "live.sqlite")
	snapPath := filepath.Join(tmp, "snap.sqlite")
	lockPath := dbPath + ".lock"

	makeBaselineDB(t, dbPath)
	putRow(t, dbPath, "live", "still-live")
	r.NoError(os.WriteFile(snapPath, []byte("not-a-sqlite-file"), 0o600))

	_, err := Restore(context.Background(), snapPath, dbPath, lockPath)
	r.Error(err)
	// Original DB unchanged
	r.Equal("still-live", readRow(t, dbPath, "live"))
}

func TestRestoreRollbackOnSyncDirFailure(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "live.sqlite")
	snapPath := filepath.Join(tmp, "snap.sqlite")
	lockPath := dbPath + ".lock"

	makeBaselineDB(t, dbPath)
	putRow(t, dbPath, "live", "still-live")
	makeBaselineDB(t, snapPath)
	putRow(t, snapPath, "live", "would-have-been")

	// Inject a syncDir failure AFTER move-aside + copy succeed.
	want := errors.New("synthetic-syncdir")
	setSyncDir(t, func(string) error { return want })

	_, err := Restore(context.Background(), snapPath, dbPath, lockPath)
	r.Error(err)
	r.ErrorIs(err, want, "original error must surface")
	// Original DB content restored from rollback
	r.Equal("still-live", readRow(t, dbPath, "live"))
}

// These two test helpers cover the lock contract from the design and
// help reviewers verify the fix in T9 (server-side lock) interacts
// correctly with restore.
func TestRestoreRetriesAfterLockReleased(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "live.sqlite")
	snapPath := filepath.Join(tmp, "snap.sqlite")
	lockPath := dbPath + ".lock"

	makeBaselineDB(t, dbPath)
	makeBaselineDB(t, snapPath)
	putRow(t, snapPath, "live", "after-stop")

	holder := flock.New(lockPath)
	ok, err := holder.TryLock()
	r.NoError(err)
	r.True(ok)

	// First attempt: blocked.
	_, err = Restore(context.Background(), snapPath, dbPath, lockPath)
	r.ErrorIs(err, ErrServerHoldsLock)

	// Release: simulating server stop.
	r.NoError(holder.Unlock())

	// Retry now succeeds.
	res, err := Restore(context.Background(), snapPath, dbPath, lockPath)
	r.NoError(err)
	r.NotEmpty(res.MovedAside)
	r.Equal("after-stop", readRow(t, dbPath, "live"))
}

// TestRestoreTwiceProducesDistinctPreRestoreFiles guards against the
// pre-restore suffix colliding when two restores happen in rapid
// succession. The suffix uses nanosecond resolution; back-to-back calls
// on the same wall clock must still produce distinct suffixes so the
// second restore does not overwrite the first pre-restore set.
func TestRestoreTwiceProducesDistinctPreRestoreFiles(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "live.sqlite")
	snap1 := filepath.Join(tmp, "snap1.sqlite")
	snap2 := filepath.Join(tmp, "snap2.sqlite")
	lockPath := dbPath + ".lock"

	makeBaselineDB(t, dbPath)
	putRow(t, dbPath, "live", "v0")

	makeBaselineDB(t, snap1)
	putRow(t, snap1, "live", "v1")
	res1, err := Restore(context.Background(), snap1, dbPath, lockPath)
	r.NoError(err)
	r.Equal("v1", readRow(t, dbPath, "live"))

	// Mutate live DB to a known second value before the second snapshot,
	// so the second pre-restore set captures distinct content.
	putRow(t, dbPath, "live", "v1.5")

	makeBaselineDB(t, snap2)
	putRow(t, snap2, "live", "v2")
	res2, err := Restore(context.Background(), snap2, dbPath, lockPath)
	r.NoError(err)
	r.Equal("v2", readRow(t, dbPath, "live"))

	r.NotEqual(res1.PreRestoreSuffix, res2.PreRestoreSuffix,
		"back-to-back restores must produce distinct pre-restore suffixes")
	r.FileExists(dbPath+res1.PreRestoreSuffix,
		"first pre-restore set must survive a subsequent restore")
	r.FileExists(dbPath+res2.PreRestoreSuffix,
		"second pre-restore set must exist after the second restore")
}

func TestValidateSnapshotMissingFile(t *testing.T) {
	tmp := t.TempDir()
	err := ValidateSnapshot(context.Background(), filepath.Join(tmp, "no-such.sqlite"))
	require.Error(t, err)
}

func TestValidateSnapshotRejectsDirectory(t *testing.T) {
	tmp := t.TempDir()
	dir := filepath.Join(tmp, "snap-dir")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	err := ValidateSnapshot(context.Background(), dir)
	require.Error(t, err)
	require.Contains(t, err.Error(), "regular file")
}

func TestValidateSnapshotAcceptsValidSqlite(t *testing.T) {
	tmp := t.TempDir()
	snap := filepath.Join(tmp, "snap.sqlite")
	makeBaselineDB(t, snap)
	require.NoError(t, ValidateSnapshot(context.Background(), snap))
}
