package db_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/wesm/fotobank/internal/db"
	"github.com/wesm/fotobank/internal/testutil"
)

func TestOpenEnablesWALAndReturnsBothPools(t *testing.T) {
	r := require.New(t)
	d, err := db.Open(filepath.Join(t.TempDir(), "test.sqlite"))
	r.NoError(err)
	defer d.Close()

	var mode string
	row := d.ReadDB().QueryRow("PRAGMA journal_mode")
	r.NoError(row.Scan(&mode))
	r.Equal("wal", mode)

	r.NotNil(d.WriteDB())
	r.NotNil(d.ReadDB())
}

func TestTxCommitsOnNilError(t *testing.T) {
	r := require.New(t)
	d, err := db.Open(filepath.Join(t.TempDir(), "c.sqlite"))
	r.NoError(err)
	defer d.Close()

	r.NoError(d.Tx(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`CREATE TABLE t (x INTEGER)`)
		return err
	}))

	var n int
	r.NoError(d.ReadDB().QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE name='t'`,
	).Scan(&n))
	r.Equal(1, n)
}

func TestReadDBRejectsWrites(t *testing.T) {
	// Regression: the RO pool must open with mode=ro so an accidental
	// Exec through ReadDB() fails rather than mutating the database.
	r := require.New(t)
	d, err := db.Open(filepath.Join(t.TempDir(), "ro.sqlite"))
	r.NoError(err)
	defer d.Close()

	_, err = d.WriteDB().Exec(`CREATE TABLE t (x INTEGER)`)
	r.NoError(err)

	_, err = d.ReadDB().Exec(`INSERT INTO t VALUES (1)`)
	r.Error(err)
}

func TestTxRollsBackOnError(t *testing.T) {
	r := require.New(t)
	d, err := db.Open(filepath.Join(t.TempDir(), "r.sqlite"))
	r.NoError(err)
	defer d.Close()

	_, err = d.WriteDB().Exec(`CREATE TABLE t (x INTEGER)`)
	r.NoError(err)

	boom := errors.New("boom")
	r.ErrorIs(d.Tx(context.Background(), func(tx *sql.Tx) error {
		_, _ = tx.Exec(`INSERT INTO t VALUES (1)`)
		return boom
	}), boom)

	var n int
	r.NoError(d.ReadDB().QueryRow(`SELECT COUNT(*) FROM t`).Scan(&n))
	r.Equal(0, n)
}

// TestSchema_AIJobsAcceptsEmbedTask asserts the ai_jobs.task CHECK
// constraint admits 'embed' (the v1 search/embedding task) and still
// rejects unknown values like 'classify'. Inserts are inlined per
// pre-alpha policy: no shared seed helpers in db_test.go yet.
func TestSchema_AIJobsAcceptsEmbedTask(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)

	rw := d.WriteDB()
	_, err := rw.Exec(
		`INSERT INTO owners VALUES('h1','u1','k1','u1',datetime('now'))`,
	)
	r.NoError(err)

	mediaID := uuid.NewString()
	_, err = rw.Exec(
		`INSERT INTO media (id,owner_hub,owner_user_id,media_type,mime_type,path,imported_at,size,checksum,thumb_status,thumb_version,thumb_updated_at)
		 VALUES (?, 'h1','u1','photo','image/jpeg','a.jpg',datetime('now'),1,'cs','pending',1,datetime('now'))`,
		mediaID,
	)
	r.NoError(err)

	// Accepts 'embed'.
	_, err = rw.Exec(
		`INSERT INTO ai_jobs(id, media_id, task, fingerprint, status, attempts, enqueued_at)
		 VALUES (?, ?, 'embed', 'fp', 'pending', 0, datetime('now'))`,
		uuid.NewString(), mediaID,
	)
	r.NoError(err)

	// Rejects 'classify'.
	_, err = rw.Exec(
		`INSERT INTO ai_jobs(id, media_id, task, fingerprint, status, attempts, enqueued_at)
		 VALUES (?, ?, 'classify', 'fp', 'pending', 0, datetime('now'))`,
		uuid.NewString(), mediaID,
	)
	r.Error(err)
}

// TestSchema_EmbeddingGenerationsOneActiveOneBuilding asserts the
// partial unique indexes on embedding_generations.state enforce the
// at-most-one-active and at-most-one-building invariants from the
// search v1 design (§5.3). Inserts are inlined to match A1's style;
// no shared seed helpers in db_test.go yet.
func TestSchema_EmbeddingGenerationsOneActiveOneBuilding(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	rw := d.WriteDB()

	mustInsertGen := func(state string) {
		_, err := rw.Exec(
			`INSERT INTO embedding_generations
			 (fingerprint, fingerprint_hash, model_id, input_profile, vec_table_name,
			  dimension, state, created_at)
			 VALUES (?, ?, 'm', 'p', ?, 768, ?, datetime('now'))`,
			"fp-"+state, "h-"+state, "media_embeddings_g_"+state, state,
		)
		r.NoError(err)
	}

	mustInsertGen("active")
	// Second active must fail.
	_, err := rw.Exec(
		`INSERT INTO embedding_generations
		 (fingerprint, fingerprint_hash, model_id, input_profile, vec_table_name,
		  dimension, state, created_at)
		 VALUES ('fp2','h2','m','p','t2',768,'active', datetime('now'))`,
	)
	r.Error(err, "must reject two active generations")

	mustInsertGen("building")
	// Second building must fail.
	_, err = rw.Exec(
		`INSERT INTO embedding_generations
		 (fingerprint, fingerprint_hash, model_id, input_profile, vec_table_name,
		  dimension, state, created_at)
		 VALUES ('fp3','h3','m','p','t3',768,'building', datetime('now'))`,
	)
	r.Error(err, "must reject two building generations")
}

// TestSchema_MediaEmbeddingIDsUniqueVecID asserts the
// UNIQUE (generation_id, vec_id) constraint on media_embedding_ids
// rejects two media rows mapping to the same vec_id within a
// generation (search v1 design §5.4).
func TestSchema_MediaEmbeddingIDsUniqueVecID(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	rw := d.WriteDB()

	_, err := rw.Exec(
		`INSERT INTO owners VALUES('h1','u1','k1','u1',datetime('now'))`,
	)
	r.NoError(err)

	m1 := uuid.NewString()
	m2 := uuid.NewString()
	for _, mid := range []string{m1, m2} {
		_, err = rw.Exec(
			`INSERT INTO media (id,owner_hub,owner_user_id,media_type,mime_type,path,imported_at,size,checksum,thumb_status,thumb_version,thumb_updated_at)
			 VALUES (?, 'h1','u1','photo','image/jpeg',?,datetime('now'),1,?,'pending',1,datetime('now'))`,
			mid, "p-"+mid, "cs-"+mid,
		)
		r.NoError(err)
	}

	// Insert a generation row.
	res, err := rw.Exec(
		`INSERT INTO embedding_generations
		 (fingerprint, fingerprint_hash, model_id, input_profile, vec_table_name,
		  dimension, state, created_at)
		 VALUES ('fp','h','m','p','media_embeddings_g1',768,'building', datetime('now'))`,
	)
	r.NoError(err)
	gid, err := res.LastInsertId()
	r.NoError(err)

	insertMapping := func(generationID int64, mediaID string, vecID int64) error {
		_, execErr := rw.Exec(
			`INSERT INTO media_embedding_ids (generation_id, media_id, vec_id)
			 VALUES (?, ?, ?)`,
			generationID, mediaID, vecID,
		)
		return execErr
	}

	r.NoError(insertMapping(gid, m1, 1))
	// Second mapping with the same vec_id must fail.
	r.Error(insertMapping(gid, m2, 1))
}

func TestScopesBackoffMigration(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	var name string
	err := d.ReadDB().QueryRowContext(context.Background(),
		`SELECT name FROM pragma_table_info('scopes') WHERE name = 'broker_next_attempt_at'`,
	).Scan(&name)
	r.NoError(err)
	r.Equal("broker_next_attempt_at", name)

	var idx string
	err = d.ReadDB().QueryRowContext(context.Background(),
		`SELECT name FROM sqlite_master WHERE type='index' AND name='scopes_broker_ready_idx'`,
	).Scan(&idx)
	r.NoError(err)
	r.Equal("scopes_broker_ready_idx", idx)
}

// TestOpen_RoundTripNullableTime asserts that *time.Time scans round-trip
// nil and non-nil values cleanly under mattn. The test creates a tiny
// scratch table, writes (nullable_ts = NULL) and (nullable_ts = utcNow)
// through the WRITE pool, then reads them back through the READ pool
// into *time.Time pointers and compares.
//
// Reading via ReadDB() (not WriteDB()) is deliberate: the two pools
// open with different DSNs (read-only with mode=ro), and the prior
// test exercised only the writer pool. This version covers both.
func TestOpen_RoundTripNullableTime(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	rw := d.WriteDB()
	ro := d.ReadDB()

	_, err := rw.Exec(`CREATE TABLE t_time_test (
		id INTEGER PRIMARY KEY,
		ts TIMESTAMP
	)`)
	r.NoError(err)

	now := time.Now().UTC().Truncate(time.Microsecond)
	_, err = rw.Exec(`INSERT INTO t_time_test (id, ts) VALUES (1, ?), (2, NULL)`, now)
	r.NoError(err)

	var got1 *time.Time
	r.NoError(ro.QueryRow(`SELECT ts FROM t_time_test WHERE id=1`).Scan(&got1))
	r.NotNil(got1)
	r.True(got1.Equal(now), "want %v got %v", now, *got1)

	var got2 *time.Time
	r.NoError(ro.QueryRow(`SELECT ts FROM t_time_test WHERE id=2`).Scan(&got2))
	r.Nil(got2, "NULL TIMESTAMP must Scan into nil *time.Time")
}

// TestOpen_RoundTripTZ asserts that a TIMESTAMP column written as a
// non-UTC time round-trips with the same Unix instant AND preserves
// sub-second precision (mattn stores at microsecond resolution via
// the .999999999 layout). Fotobank stores everything as UTC; the test
// exists to assert the driver doesn't drop the offset or sub-second
// component during write+read.
//
// Two distinct assertions:
//
//   - time.Time.Equal: the Unix instant survives the round-trip (this
//     would still pass if the driver rewrote a non-UTC value as its
//     UTC equivalent).
//   - Zone offset equality: the FixedZone offset survives literally,
//     so a regression that strips the offset (e.g. mattn returning UTC
//     for a value written in a non-UTC zone) is caught even though the
//     instant would still match.
//
// Read happens via ReadDB() (mode=ro pool) so both pools are
// exercised by the time-scan regression suite.
func TestOpen_RoundTripTZ(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	rw := d.WriteDB()
	ro := d.ReadDB()

	_, err := rw.Exec(`CREATE TABLE t_tz (id INTEGER PRIMARY KEY, ts TIMESTAMP)`)
	r.NoError(err)

	// Fixed-offset PDT-equivalent zone. forbidigo bans tz construction
	// in production paths but explicitly carves out tests; this is the
	// test-only use the lint message refers to. The assertions below
	// cover both the Unix instant and the zone offset round-trip.
	loc := time.FixedZone("PDT", -7*60*60) //nolint:forbidigo // test-only TZ round-trip; see comment above
	// 123.456ms — within mattn's microsecond storage resolution; the
	// trailing zeros below the microsecond boundary keep the value
	// representable so a successful round-trip really does mean
	// sub-second precision survived (not just that we lucked into
	// 0ns and the assertion was vacuous).
	want := time.Date(2026, 5, 1, 9, 30, 0, 123456000, loc)
	_, err = rw.Exec(`INSERT INTO t_tz (id, ts) VALUES (1, ?)`, want)
	r.NoError(err)

	var got time.Time
	r.NoError(ro.QueryRow(`SELECT ts FROM t_tz WHERE id=1`).Scan(&got))
	r.True(got.Equal(want), "Unix instant + sub-second must round-trip; want %v got %v", want, got)

	// Zone offset must round-trip literally. time.Time.Equal returns
	// true for the same instant regardless of zone, so without this
	// assertion a regression that returned UTC ("offset 0") for a
	// non-UTC write would still pass the Equal check.
	_, gotOffset := got.Zone()
	_, wantOffset := want.Zone()
	r.Equal(wantOffset, gotOffset, "TZ offset must round-trip; want %d got %d", wantOffset, gotOffset)
}

// TestOpen_ConcurrentWriters proves busy_timeout is actually engaged
// by manufacturing a SQLITE_BUSY contention window and asserting (a)
// the second writer is BLOCKED while h1 holds the lock — i.e. it
// hasn't completed yet — and (b) the second writer completes
// successfully once h1 releases the lock.
//
// The probe-based design gives a strong-but-not-perfect test of the
// property "busy_timeout makes the second writer wait rather than
// fail immediately." It is more reliable than any elapsed-time
// heuristic (which is what previous iterations attempted; all
// failed code review for various flake modes). The remaining
// caveat: SQLite does not expose a portable way to observe a
// blocked writer from outside, so the test relies on a generous
// probe window (200ms) being enough for h2 to reach ExecContext
// before the probe runs. On a runner so loaded that scheduling a
// goroutine takes >200ms, the test could pass even with a broken
// busy_timeout (h2 would simply not have run yet by the time we
// COMMIT and proceed). That scenario is implausible on any modern
// CI runner; for full determinism we would need SQLite-internal
// lock observation that mattn does not surface.
//
//   - Two raw *sql.DB handles open the same file, each carrying the
//     production DSN (_busy_timeout=5000&_fk=1). Two distinct handles
//     are required: db.Open pins the RW pool to MaxOpenConns(1), which
//     queues writers inside Go's database/sql layer and never gives
//     SQLite a chance to return SQLITE_BUSY. Two raw handles with
//     default MaxOpenConns let two real connections hit the file
//     simultaneously, which is what busy_timeout is designed to handle.
//   - h1 starts a BEGIN IMMEDIATE transaction on a pinned *sql.Conn.
//     BEGIN IMMEDIATE acquires the file's RESERVED write lock up front,
//     so any other connection that tries to write must wait or return
//     SQLITE_BUSY. The pinned conn is required because BEGIN/COMMIT
//     must run on the same connection.
//   - h2 issues an INSERT in a goroutine. With busy_timeout=5000
//     active, h2's Exec must block until h1's COMMIT releases the lock.
//   - The test waits a probe window (200ms) and then asserts h2 has
//     NOT yet completed. If busy_timeout is broken (e.g. 0 / no retry
//     logic), h2 would have errored immediately with SQLITE_BUSY and
//     resCh would be non-empty by now.
//   - Only after confirming h2 is still blocked, the test commits h1's
//     transaction and waits for h2 to finish. h2 must succeed (no
//     error) — proving the retry path released cleanly.
//
// What this test specifically catches: a regression where h2 fails
// IMMEDIATELY with SQLITE_BUSY (busy_timeout=0, missing retry path,
// driver swap to a no-retry driver). What it does NOT catch: a
// scenario where h2's goroutine doesn't reach ExecContext within the
// 200ms probe window. On a sufficiently overloaded runner h2 could
// still be unscheduled (or sitting between `close(started)` and
// `ExecContext`) when we probe — in which case `resCh` would be empty
// for the wrong reason and the test would proceed to COMMIT, after
// which h2 would succeed regardless of whether busy_timeout works.
// 200ms is enormous for goroutine scheduling on any modern system, so
// this miss is implausible in practice, but acknowledging it honestly:
// the probe is a strong-flake-reduction heuristic, not theoretical
// proof. The probe-based design abandons the elapsed-time arms race
// that produced false negatives from scheduler stalls (Job 65) and
// false positives from stalls being counted as elapsed time (Jobs 73
// and 75); each previous iteration plugged one race only to expose
// another.
//
// Bound by ctx (2s); skipped under -short.
func TestOpen_ConcurrentWriters(t *testing.T) {
	if testing.Short() {
		t.Skip("contention test; not under -short")
	}
	r := require.New(t)

	db.RegisterSqliteVec()

	path := filepath.Join(t.TempDir(), "stress.sqlite")
	dsn := path + "?_busy_timeout=5000&_fk=1"

	h1, err := sql.Open("sqlite3", dsn)
	r.NoError(err)
	defer h1.Close()
	h2, err := sql.Open("sqlite3", dsn)
	r.NoError(err)
	defer h2.Close()

	// WAL once via h1 before any contention starts.
	_, err = h1.Exec(`PRAGMA journal_mode=WAL`)
	r.NoError(err)
	_, err = h1.Exec(`CREATE TABLE stress (id INTEGER PRIMARY KEY, v TEXT)`)
	r.NoError(err)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// h1 holds the write lock via BEGIN IMMEDIATE on a pinned conn.
	holdConn, err := h1.Conn(ctx)
	r.NoError(err)
	defer holdConn.Close()
	_, err = holdConn.ExecContext(ctx, `BEGIN IMMEDIATE`)
	r.NoError(err)

	// h2's Exec runs in a goroutine. The goroutine signals via the
	// `started` channel just before invoking ExecContext.
	resCh := make(chan error, 1)
	started := make(chan struct{})
	go func() {
		close(started)
		_, err := h2.ExecContext(ctx, `INSERT INTO stress (v) VALUES (?)`, "from-h2")
		resCh <- err
	}()
	<-started

	// Wait long enough for h2 to (a) be scheduled, (b) call ExecContext,
	// and (c) attempt the write — at which point busy_timeout must keep
	// it blocked. 200ms is huge on any realistic runner; even a 100ms
	// scheduler stall would leave 100ms of margin for h2 to enter SQLite.
	const probeWindow = 200 * time.Millisecond
	time.Sleep(probeWindow)

	// h2 must NOT have completed yet — it should be blocked on the
	// write lock that h1 still holds. If resCh is non-empty here,
	// busy_timeout did not engage (the writer either errored
	// immediately or somehow got around the lock).
	select {
	case err := <-resCh:
		r.FailNowf("h2 completed before COMMIT — busy_timeout failed",
			"got err=%v after %v probe window with h1 still holding the lock",
			err, probeWindow)
	default:
		// Good: h2 is blocked on the lock. busy_timeout is engaged.
	}

	// Release h1's lock. h2's INSERT should now unblock and succeed.
	_, err = holdConn.ExecContext(ctx, `COMMIT`)
	r.NoError(err)

	select {
	case err := <-resCh:
		r.NoError(err, "h2 INSERT must succeed once h1 releases the write lock")
	case <-ctx.Done():
		r.FailNowf("h2 INSERT did not complete after COMMIT", "%v", ctx.Err())
	}

	// Exactly one row from h2.
	var total int
	r.NoError(h1.QueryRowContext(ctx, `SELECT COUNT(*) FROM stress`).Scan(&total))
	r.Equal(1, total)
}
