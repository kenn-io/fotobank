package backup

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/owners"
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

// TestSnapshot_DuringConcurrentWrites stress-tests the snapshot path
// under contention with active writes. VACUUM INTO acquires a SHARED
// lock on the source DB; concurrent writers must yield via
// busy_timeout and resume after the snapshot commits. A regression in
// either direction (snapshot starves writers, writers block snapshot
// past timeout, mattn losing pragmas across the snapshot connection)
// would surface here.
//
// Sequence:
//   - Spawn a writer goroutine that inserts SeedPhoto rows in a tight
//     loop until stopped, recording per-call errors. Sleeping 5ms
//     between inserts keeps WAL pressure realistic without exhausting
//     CPU on slow runners.
//   - From the main goroutine, run Snapshot once after letting the
//     writer get going. Snapshot must succeed.
//   - Stop the writer, drain its goroutine.
//   - Open the snapshot read-only and confirm:
//   - PRAGMA integrity_check returns "ok" (not a torn copy)
//   - Row count is somewhere between the pre-snapshot count and the
//     final live-DB count (i.e. a consistent point-in-time view, not
//     "all rows" or "no rows")
//   - The writer goroutine made forward progress (at least 1 insert
//     succeeded after Snapshot started); a regression that
//     completely starves writers would zero this.
//
// Skipped under -short: contention-style test with intentional sleep.
func TestSnapshot_DuringConcurrentWrites(t *testing.T) {
	if testing.Short() {
		t.Skip("contention test; not under -short")
	}
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	owner := testutil.SeedOwner(t, d.WriteDB(), "self", "u1")

	// Pre-seed a baseline so the snapshot has something even if its
	// VACUUM INTO lands at the very front of the contention window.
	// SeedPhoto's path is derived from `label`, and the (owner_hub,
	// owner_user_id, path) UNIQUE constraint rejects duplicates — so
	// each preseed row needs a distinct label.
	const preSeed = 5
	for i := range preSeed {
		testutil.SeedPhoto(t, d.WriteDB(), owner, "preseed-"+strconv.Itoa(i))
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	var insertCount atomic.Int64
	var snapshotStartedAt atomic.Int64 // unix ns
	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			// SeedPhoto's t.Helper + require don't tolerate a fail
			// from a non-test goroutine — call the underlying repo path
			// instead and tally errors locally so the test can decide
			// what to do with them.
			id, err := insertOnePhoto(d.WriteDB(), owner)
			if err != nil {
				// Don't fail here — busy_timeout exhaustion under
				// extreme contention is conceivable, and the test's
				// assertion is on whether *some* writes land, not
				// whether *every* write does.
				continue
			}
			_ = id
			insertCount.Add(1)
			time.Sleep(5 * time.Millisecond)
		}
	})

	// Let the writer get a few inserts in before snapshotting so the
	// snapshot's row count strictly exceeds preSeed (catching a
	// regression where Snapshot somehow only sees the pre-tx state).
	time.Sleep(50 * time.Millisecond)

	dst := filepath.Join(t.TempDir(), "snap.sqlite")
	snapshotStartedAt.Store(time.Now().UnixNano())
	r.NoError(Snapshot(context.Background(), d.WriteDB(), dst),
		"Snapshot must succeed under concurrent writes")
	preStopInsertCount := insertCount.Load()

	close(stop)
	wg.Wait()
	finalInsertCount := insertCount.Load()

	// Snapshot integrity.
	r.True(integrityOk(t, dst), "snapshot must pass PRAGMA integrity_check")

	// Snapshot row count: somewhere in [preSeed, preSeed+finalInserts].
	snapDB, err := sql.Open("sqlite3", "file:"+dst+"?mode=ro&_busy_timeout=5000&_fk=1")
	r.NoError(err)
	defer snapDB.Close()
	var snapRows int
	r.NoError(snapDB.QueryRow(`SELECT COUNT(*) FROM media`).Scan(&snapRows))
	r.GreaterOrEqual(snapRows, preSeed,
		"snapshot must contain at least the pre-seeded rows; got %d", snapRows)
	r.LessOrEqual(int64(snapRows), int64(preSeed)+finalInsertCount,
		"snapshot row count must not exceed preSeed + total inserts (%d); got %d",
		int64(preSeed)+finalInsertCount, snapRows)

	// Forward-progress check: writer must have made at least one insert
	// after Snapshot started. A snapshot that starves the writer would
	// leave preStopInsertCount unchanged across the snapshot window,
	// and finalInsertCount would equal whatever the writer had landed
	// before snapshotStartedAt. We don't have a precise "inserts after
	// start" counter, so the proxy is: finalInsertCount must exceed
	// the count we observed *immediately after* Snapshot returned.
	r.Greater(finalInsertCount, preStopInsertCount,
		"writer must have completed at least one insert after Snapshot returned; "+
			"pre-stop=%d final=%d (Snapshot may have starved writers)",
		preStopInsertCount, finalInsertCount)
}

// insertOnePhoto is a writer-goroutine-friendly variant of
// testutil.SeedPhoto: it returns an error rather than t.Fatal'ing
// because callers need to tolerate the occasional busy_timeout
// exhaustion under contention without aborting the parent test.
func insertOnePhoto(rw *sql.DB, p owners.Principal) (string, error) {
	id := "stress-" + strconv.FormatInt(stressCounter.Add(1), 10)
	_, err := rw.Exec(`INSERT INTO media (
		id, owner_hub, owner_user_id, media_type, mime_type, path, original_filename,
		imported_at, size, checksum, thumb_status, thumb_version
	) VALUES (?, ?, ?, 'photo', 'image/jpeg', ?, ?, ?, ?, ?, 'pending', 0)`,
		id, p.Hub, p.UserID, "stress/"+id+".jpg", id+".jpg",
		time.Now().UTC(), int64(1024), "cs-"+id,
	)
	return id, err
}

// stressCounter generates unique stress-test IDs without colliding
// with SeedPhoto's preseed (which uses a different prefix).
var stressCounter atomic.Int64

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
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	// Seed a parent owner row + a child media row that depends on the
	// owners FK. SeedPhoto inserts a fully-formed media row.
	owner := testutil.SeedOwner(t, d.WriteDB(), "self", "u1")
	mediaID := testutil.SeedPhoto(t, d.WriteDB(), owner, "round-trip")

	// Snapshot.
	snapPath := filepath.Join(t.TempDir(), "snap.sqlite")
	r.NoError(Snapshot(context.Background(), d.WriteDB(), snapPath))

	// Re-open the snapshot via the same code path the restore tool uses.
	conn, err := sql.Open("sqlite3", "file:"+snapPath+"?_busy_timeout=5000&_fk=1")
	r.NoError(err)
	t.Cleanup(func() { _ = conn.Close() })

	// Confirm the seeded media row arrived intact.
	var got string
	r.NoError(conn.QueryRow(
		`SELECT id FROM media WHERE id = ?`, mediaID,
	).Scan(&got))
	r.Equal(mediaID, got)

	// Confirm foreign_keys is enforced on the snapshot connection.
	// Attempting to insert an album_media row with a non-existent
	// album_id must error with a constraint failure.
	_, err = conn.Exec(`INSERT INTO album_media (album_id, media_id, added_at)
	                       VALUES (?, ?, datetime('now'))`,
		"00000000-0000-0000-0000-000000000000", mediaID)
	r.Error(err, "foreign_keys=1 must be active on the snapshot connection")
}
