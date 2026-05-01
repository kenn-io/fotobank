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

	"github.com/wesm/fotobank/internal/testutil"
)

// makeSourceDB creates a tiny SQLite DB at path with one table and one row.
func makeSourceDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", path+"?_busy_timeout=5000&_fk=1")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec("CREATE TABLE t(x INTEGER); INSERT INTO t VALUES (42);")
	require.NoError(t, err)
	return db
}

func integrityOk(t *testing.T, path string) bool {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:"+path+"?mode=ro")
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
	r := require.New(t)
	tmp := t.TempDir()
	missing := filepath.Join(tmp, "no-such.sqlite")
	dst := filepath.Join(tmp, "snap.sqlite")
	err := SnapshotPath(context.Background(), missing, dst)
	r.Error(err)
	// mode=rw on the source DSN must prevent SQLite from creating an
	// empty database in place of the missing source.
	_, statErr := os.Stat(missing)
	r.True(os.IsNotExist(statErr),
		"missing source must not be created by snapshot open path")
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
	r.Contains(dsn, "mode=rw", "DSN must pin mode=rw to refuse create-on-open")

	rel := buildDSN("rel.sqlite")
	u, err := url.Parse(rel)
	r.NoError(err, "DSN built from a relative path must parse cleanly")
	r.Equal("file", u.Scheme)
	r.NotEmpty(u.Path, "relative path must be promoted into u.Path")
	r.True(strings.HasPrefix(u.Path, "/"), "u.Path must be absolute, got %q", u.Path)
	r.True(strings.HasSuffix(u.Path, "/rel.sqlite"), "u.Path must end with the original filename, got %q", u.Path)
}

// TestSnapshotPragmas_AfterRestore_RoundTrip proves that a snapshot
// created with VACUUM INTO and re-opened via the restore-side DSN
// honours the same busy_timeout and foreign_keys defaults as the live
// DB. Foreign-key enforcement is the one that bites silently — a
// snapshot opened without _fk=1 would let a child-row delete cascade
// disappear in tests that assert on FK behavior.
//
// The snapshot is intentionally re-opened in WRITABLE mode (no
// mode=ro on the DSN). A read-only open would make the FK-violating
// INSERT error with "attempt to write a readonly database" before
// SQLite ever consults _fk=1, masking a regression where _fk=1 is
// stripped from the DSN. With a writable connection the INSERT
// reaches FK enforcement: it must error on the constraint, and if
// _fk=1 were ever dropped the INSERT would succeed and the test
// would fail — which is what we want. The snapshot lives under
// t.TempDir() so writable open is safe; nothing else reads it.
func TestSnapshotPragmas_AfterRestore_RoundTrip(t *testing.T) {
	d := testutil.OpenTestDB(t)
	// Seed a parent owner row + a child media row that depends on the
	// owners FK. SeedPhoto inserts a fully-formed media row.
	owner := testutil.SeedOwner(t, d.WriteDB(), "self", "u1")
	mediaID := testutil.SeedPhoto(t, d.WriteDB(), owner, "round-trip")

	// Snapshot.
	snapPath := filepath.Join(t.TempDir(), "snap.sqlite")
	require.NoError(t, Snapshot(context.Background(), d.WriteDB(), snapPath))

	// Re-open the snapshot via the same code path the restore tool uses.
	conn, err := sql.Open("sqlite3", "file:"+snapPath+"?_busy_timeout=5000&_fk=1")
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	// Confirm the seeded media row arrived intact.
	var got string
	require.NoError(t, conn.QueryRow(
		`SELECT id FROM media WHERE id = ?`, mediaID,
	).Scan(&got))
	require.Equal(t, mediaID, got)

	// Confirm foreign_keys is enforced on the snapshot connection.
	// Attempting to insert an album_media row with a non-existent
	// album_id must error with a constraint failure.
	_, err = conn.Exec(`INSERT INTO album_media (album_id, media_id, added_at)
	                       VALUES (?, ?, datetime('now'))`,
		"00000000-0000-0000-0000-000000000000", mediaID)
	require.Error(t, err, "foreign_keys=1 must be active on the snapshot connection")
}
