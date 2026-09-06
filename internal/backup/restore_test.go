package backup

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func makeBaselineDB(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite3", path+"?_busy_timeout=5000&_fk=1")
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

// A zero-byte file would otherwise be silently opened as a fresh empty
// SQLite DB by mode=rw with create-on-open; the size guard surfaces it
// as a clear error before any move-aside runs.
func TestValidateSnapshotRejectsZeroByteFile(t *testing.T) {
	tmp := t.TempDir()
	snap := filepath.Join(tmp, "empty.sqlite")
	require.NoError(t, os.WriteFile(snap, nil, 0o600))
	err := ValidateSnapshot(context.Background(), snap)
	require.Error(t, err)
	require.Contains(t, err.Error(), "zero bytes")
}

// A valid SQLite file with no tables (integrity_check=ok, sqlite_master
// empty) would pass the original integrity check but silently destroy
// the live DB on restore.
func TestValidateSnapshotRejectsEmptySchema(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	snap := filepath.Join(tmp, "empty-schema.sqlite")
	d, err := sql.Open("sqlite3", snap+"?_busy_timeout=5000")
	r.NoError(err)
	// Force the DB file to materialize without creating any tables.
	_, err = d.ExecContext(context.Background(), "PRAGMA user_version=0")
	r.NoError(err)
	r.NoError(d.Close())
	err = ValidateSnapshot(context.Background(), snap)
	r.Error(err)
	r.Contains(err.Error(), "schema_migrations")
}

// An unrelated SQLite database with a dummy table is a real
// operational hazard — somebody points the restore CLI at the wrong
// file and silently destroys the live fotobank DB. ValidateSnapshot
// must reject anything that lacks the schema_migrations marker.
func TestValidateSnapshotRejectsUnrelatedSQLite(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	snap := filepath.Join(tmp, "unrelated.sqlite")
	d, err := sql.Open("sqlite3", snap+"?_busy_timeout=5000")
	r.NoError(err)
	_, err = d.ExecContext(context.Background(),
		"CREATE TABLE notes (id INTEGER PRIMARY KEY, body TEXT)")
	r.NoError(err)
	r.NoError(d.Close())
	err = ValidateSnapshot(context.Background(), snap)
	r.Error(err)
	r.Contains(err.Error(), "schema_migrations")
}
