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
func OpenTestDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "fotobank.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// OpenTestDBAt opens (and migrates) a sqlite DB at path. Unlike
// OpenTestDB, it does NOT register a t.Cleanup closer because CLI
// tests routinely close + reopen across the subprocess boundary;
// the caller manages the lifetime explicitly.
func OpenTestDBAt(t *testing.T, path string) *db.DB {
	t.Helper()
	d, err := db.Open(path)
	require.NoError(t, err)
	return d
}
