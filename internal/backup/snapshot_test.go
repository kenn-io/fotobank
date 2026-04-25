package backup

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// makeSourceDB creates a tiny SQLite DB at path with one table and one row.
func makeSourceDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec("CREATE TABLE t(x INTEGER); INSERT INTO t VALUES (42);")
	require.NoError(t, err)
	return db
}

func integrityOk(t *testing.T, path string) bool {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	require.NoError(t, err)
	defer db.Close()
	var s string
	require.NoError(t, db.QueryRow("PRAGMA integrity_check").Scan(&s))
	return s == "ok"
}

func TestSnapshotWritesIntegralCopy(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src.sqlite")
	db := makeSourceDB(t, src)
	dst := filepath.Join(tmp, "snap.sqlite")

	r.NoError(Snapshot(context.Background(), db, dst))
	r.FileExists(dst)
	r.True(integrityOk(t, dst), "destination must pass integrity_check")

	// .partial gone; source unchanged
	_, err := os.Stat(dst + ".partial")
	r.True(os.IsNotExist(err))
}

func TestSnapshotRefusesExistingDestination(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src.sqlite")
	db := makeSourceDB(t, src)
	dst := filepath.Join(tmp, "snap.sqlite")
	r.NoError(os.WriteFile(dst, []byte("preexisting"), 0o600))

	err := Snapshot(context.Background(), db, dst)
	r.Error(err, "must not silently replace an existing destination file")
	// preexisting content still on disk
	b, _ := os.ReadFile(dst)
	r.Equal("preexisting", string(b))
	// Cleanup path: the .partial sibling must be removed when the link fails.
	_, perr := os.Stat(dst + ".partial")
	r.True(os.IsNotExist(perr), ".partial must be removed after link failure")
}

func TestSnapshotCallsSyncDirOnParent(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src.sqlite")
	db := makeSourceDB(t, src)
	dst := filepath.Join(tmp, "snap.sqlite")

	calls := []string{}
	setSyncDir(t, func(p string) error {
		calls = append(calls, p)
		return nil
	})

	r.NoError(Snapshot(context.Background(), db, dst))
	r.Contains(calls, tmp, "syncDir must be called on the destination's parent dir")
}

func TestSnapshotPathOpensWritableDSN(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src.sqlite")
	makeSourceDB(t, src) // initialize file
	dst := filepath.Join(tmp, "snap.sqlite")

	r.NoError(SnapshotPath(context.Background(), src, dst))
	r.True(integrityOk(t, dst))
}

func TestSnapshotPathErrorsOnMissingSource(t *testing.T) {
	tmp := t.TempDir()
	missing := filepath.Join(tmp, "no-such.sqlite")
	dst := filepath.Join(tmp, "snap.sqlite")
	err := SnapshotPath(context.Background(), missing, dst)
	require.Error(t, err)
}

func TestSnapshotSurfaceErrorFromSyncDir(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src.sqlite")
	db := makeSourceDB(t, src)
	dst := filepath.Join(tmp, "snap.sqlite")

	want := errors.New("synthetic")
	setSyncDir(t, func(string) error { return want })

	err := Snapshot(context.Background(), db, dst)
	r.ErrorIs(err, want)
}

func TestBuildDSNEscapesReserved(t *testing.T) {
	r := require.New(t)

	dsn := buildDSN("/tmp/foo bar?x#y.sqlite")
	r.Contains(dsn, "%20", "space must be percent-escaped")
	r.Contains(dsn, "%3F", "'?' must be percent-escaped")
	r.Contains(dsn, "%23", "'#' must be percent-escaped")

	rel := buildDSN("rel.sqlite")
	u, err := url.Parse(rel)
	r.NoError(err, "DSN built from a relative path must parse cleanly")
	r.Equal("file", u.Scheme)
	r.NotEmpty(u.Path, "relative path must be promoted into u.Path")
	r.True(strings.HasPrefix(u.Path, "/"), "u.Path must be absolute, got %q", u.Path)
	r.True(strings.HasSuffix(u.Path, "/rel.sqlite"), "u.Path must end with the original filename, got %q", u.Path)
}
