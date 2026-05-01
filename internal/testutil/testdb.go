// Package testutil holds small helpers shared across tests.
package testutil

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wesm/fotobank/internal/db"
)

// OpenTestDB returns an opened DB backed by a fresh file in t.TempDir().
// Automatically closed at test teardown.
//
// Registers the sqlite-vec auto-extension before the first sql.Open
// in the test process. The registration is sync.Once-guarded inside
// internal/db, so calling OpenTestDB N times is safe.
func OpenTestDB(t *testing.T) *db.DB {
	t.Helper()
	db.RegisterSqliteVec()
	d, err := db.Open(filepath.Join(t.TempDir(), "fotobank.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// OpenTestDBAt opens (and migrates) a sqlite DB at path. Unlike
// OpenTestDB, it does NOT register a t.Cleanup closer because CLI
// tests routinely close + reopen across the subprocess boundary;
// the caller manages the lifetime explicitly. If the test does not
// Close the returned DB, the SQLite WAL file is left behind in the
// supplied directory. CLI tests that close + reopen explicitly are
// the intended caller.
//
// Same sqlite-vec registration guarantee as OpenTestDB; callers in
// CLI tests must not register the extension themselves.
func OpenTestDBAt(t *testing.T, path string) *db.DB {
	t.Helper()
	db.RegisterSqliteVec()
	d, err := db.Open(path)
	require.NoError(t, err)
	return d
}
