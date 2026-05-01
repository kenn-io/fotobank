package db_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

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
	// test-only use the lint message refers to. The assertion below
	// only cares about the Unix instant round-trip.
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
}

// TestOpen_ConcurrentWriters proves busy_timeout is actually engaged
// by manufacturing a deterministic SQLITE_BUSY contention window and
// asserting the second writer waits for the first to release the
// write lock rather than failing immediately.
//
// Contention model:
//
//   - Two raw *sql.DB handles open the same file, each carrying the
//     production DSN (_busy_timeout=5000&_fk=1). Two distinct handles
//     are required: a single *sql.DB with MaxOpenConns>1 still gives
//     two real SQLite connections, but the simpler two-handle setup
//     mirrors how independent workers run in production.
//   - h1 starts a BEGIN IMMEDIATE transaction. BEGIN IMMEDIATE acquires
//     the file's RESERVED write lock up front, so any other connection
//     that tries to write must wait or return SQLITE_BUSY.
//   - h2 then issues an INSERT. With busy_timeout=5000 active, h2's
//     Exec must block until h1's COMMIT releases the lock.
//   - After ~100ms the test commits h1. h2's INSERT then succeeds.
//   - The wall-clock duration of h2's Exec must be >= the hold window
//     (100ms minus a small slack for clock granularity), proving h2
//     actually waited rather than racing through a brief lock-free
//     window. If busy_timeout were 0 / broken, h2's Exec would error
//     immediately with "database is locked" instead of waiting.
//
// db.Open is deliberately NOT used here because it pins the RW pool
// to MaxOpenConns(1), which would queue writers inside the Go
// database/sql layer and never give SQLite a chance to return
// SQLITE_BUSY. Two raw handles with default MaxOpenConns let two real
// connections hit the file simultaneously, which is what busy_timeout
// is designed to handle.
//
// Bound by ctx (2s); skipped under -short.
func TestOpen_ConcurrentWriters(t *testing.T) {
	if testing.Short() {
		t.Skip("contention test; not under -short")
	}
	r := require.New(t)

	// RegisterSqliteVec is idempotent; call it before sql.Open to be
	// safe even though the sqlite3 driver name is registered already
	// via internal/db/sqlitevec.go's blank import.
	db.RegisterSqliteVec()

	path := filepath.Join(t.TempDir(), "stress.sqlite")
	dsn := path + "?_busy_timeout=5000&_fk=1"

	h1, err := sql.Open("sqlite3", dsn)
	r.NoError(err)
	defer h1.Close()
	h2, err := sql.Open("sqlite3", dsn)
	r.NoError(err)
	defer h2.Close()

	// Set WAL once via h1 before any contention starts. WAL is
	// per-file, not per-handle.
	_, err = h1.Exec(`PRAGMA journal_mode=WAL`)
	r.NoError(err)
	_, err = h1.Exec(`CREATE TABLE stress (id INTEGER PRIMARY KEY, v TEXT)`)
	r.NoError(err)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// h1 holds the write lock via BEGIN IMMEDIATE on a single dedicated
	// connection. A bare *sql.DB.Exec("BEGIN IMMEDIATE") would not work
	// reliably because the next call could land on a different pooled
	// conn that doesn't hold the lock; pinning to a single Conn fixes
	// that.
	holdConn, err := h1.Conn(ctx)
	r.NoError(err)
	defer holdConn.Close()
	_, err = holdConn.ExecContext(ctx, `BEGIN IMMEDIATE`)
	r.NoError(err)

	const holdWindow = 100 * time.Millisecond

	// h2's Exec runs in a goroutine. It must block until holdConn
	// commits or the ctx fires.
	type result struct {
		err     error
		elapsed time.Duration
	}
	resCh := make(chan result, 1)
	go func() {
		start := time.Now()
		_, err := h2.ExecContext(ctx, `INSERT INTO stress (v) VALUES (?)`, "from-h2")
		resCh <- result{err: err, elapsed: time.Since(start)}
	}()

	// Hold the write lock for a known window so h2 must wait that
	// long. time.Sleep is fine here because we own the test goroutine
	// and the lock holder.
	time.Sleep(holdWindow)
	_, err = holdConn.ExecContext(ctx, `COMMIT`)
	r.NoError(err)

	// h2 should now complete. ctx is the hard cap (2s).
	var got result
	select {
	case got = <-resCh:
	case <-ctx.Done():
		r.FailNow("h2 INSERT did not complete before ctx deadline", "%v", ctx.Err())
	}
	r.NoError(got.err, "h2 INSERT must succeed once h1 releases the write lock")

	// The elapsed time MUST be at least the hold window (within a
	// small slack for clock granularity). If busy_timeout were broken
	// or 0, h2 would have errored immediately well below holdWindow.
	const slack = 5 * time.Millisecond
	r.GreaterOrEqual(got.elapsed, holdWindow-slack,
		"h2 Exec elapsed %v < holdWindow %v (slack %v): busy_timeout did not retry",
		got.elapsed, holdWindow, slack)
	t.Logf("h2 Exec waited %v for the write lock", got.elapsed)

	// Exactly one row from h2.
	var total int
	r.NoError(h1.QueryRowContext(ctx, `SELECT COUNT(*) FROM stress`).Scan(&total))
	r.Equal(1, total)
}
