package db_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
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
// scratch table, writes (nullable_ts = NULL) and (nullable_ts = utcNow),
// then reads them back into *time.Time pointers and compares.
func TestOpen_RoundTripNullableTime(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	rw := d.WriteDB()

	_, err := rw.Exec(`CREATE TABLE t_time_test (
		id INTEGER PRIMARY KEY,
		ts TIMESTAMP
	)`)
	r.NoError(err)

	now := time.Now().UTC().Truncate(time.Microsecond)
	_, err = rw.Exec(`INSERT INTO t_time_test (id, ts) VALUES (1, ?), (2, NULL)`, now)
	r.NoError(err)

	var got1 *time.Time
	r.NoError(rw.QueryRow(`SELECT ts FROM t_time_test WHERE id=1`).Scan(&got1))
	r.NotNil(got1)
	r.True(got1.Equal(now), "want %v got %v", now, *got1)

	var got2 *time.Time
	r.NoError(rw.QueryRow(`SELECT ts FROM t_time_test WHERE id=2`).Scan(&got2))
	r.Nil(got2, "NULL TIMESTAMP must Scan into nil *time.Time")
}

// TestOpen_RoundTripTZ asserts that a TIMESTAMP column written as a
// non-UTC time round-trips with the same Unix instant. (Fotobank
// stores everything as UTC; the test exists to assert the driver
// doesn't drop sub-second precision or offset its understanding of
// the wall-clock value.)
func TestOpen_RoundTripTZ(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	rw := d.WriteDB()

	_, err := rw.Exec(`CREATE TABLE t_tz (id INTEGER PRIMARY KEY, ts TIMESTAMP)`)
	r.NoError(err)

	// Fixed-offset PDT-equivalent zone. forbidigo bans tz construction
	// in production paths but explicitly carves out tests; this is the
	// test-only use the lint message refers to. The assertion below
	// only cares about the Unix instant round-trip.
	loc := time.FixedZone("PDT", -7*60*60) //nolint:forbidigo // test-only TZ round-trip; see comment above
	want := time.Date(2026, 5, 1, 9, 30, 0, 0, loc)
	_, err = rw.Exec(`INSERT INTO t_tz (id, ts) VALUES (1, ?)`, want)
	r.NoError(err)

	var got time.Time
	r.NoError(rw.QueryRow(`SELECT ts FROM t_tz WHERE id=1`).Scan(&got))
	r.True(got.Equal(want), "Unix instant must round-trip")
}

// TestOpen_ConcurrentWriters opens two raw *sql.DB handles (no
// MaxOpenConns(1) pinning) against the same file to model the
// production import-worker / AI-worker concurrent-write contention.
// Asserts (a) no insert errors despite genuine SQLite-level lock
// contention, (b) all writes succeed, (c) at least one Exec was
// observed taking > 1ms, proving busy_timeout was actually exercised
// rather than serialized away by the connection pool.
//
// db.Open is deliberately NOT used here because it pins the RW pool
// to MaxOpenConns(1), which would queue writers at the Go database/sql
// layer and never give SQLite a chance to return SQLITE_BUSY. Two raw
// handles with default MaxOpenConns let multiple connections hit the
// file simultaneously, which is what busy_timeout is designed to handle.
func TestOpen_ConcurrentWriters(t *testing.T) {
	if testing.Short() {
		t.Skip("stress test; not under -short")
	}
	r := require.New(t)

	// RegisterSqliteVec is idempotent; call it before sql.Open to be
	// safe even though the sqlite3 driver name is registered already
	// via internal/db/sqlitevec.go's blank import.
	db.RegisterSqliteVec()

	path := filepath.Join(t.TempDir(), "stress.sqlite")
	dsn := path + "?_busy_timeout=5000&_fk=1"

	// Two distinct *sql.DB handles, each with the default (unlimited)
	// MaxOpenConns. This mirrors production where independent workers
	// each hold their own *sql.DB against the same file.
	h1, err := sql.Open("sqlite3", dsn)
	r.NoError(err)
	defer h1.Close()
	h2, err := sql.Open("sqlite3", dsn)
	r.NoError(err)
	defer h2.Close()

	// Set WAL mode and create the scratch table once via h1 before any
	// concurrent writers start. WAL is per-file, not per-handle, so
	// h2 inherits it.
	_, err = h1.Exec(`PRAGMA journal_mode=WAL`)
	r.NoError(err)
	_, err = h1.Exec(`CREATE TABLE stress (id INTEGER PRIMARY KEY, v TEXT)`)
	r.NoError(err)

	const writersPerHandle = 4
	const perWriter = 100
	const totalWriters = 2 * writersPerHandle

	var (
		wg      sync.WaitGroup
		maxExec atomic.Int64 // nanoseconds; tracks the slowest single Exec
		errCh   = make(chan error, totalWriters*perWriter)
	)

	insert := func(handle *sql.DB, label string) {
		defer wg.Done()
		for i := range perWriter {
			start := time.Now()
			_, err := handle.Exec(`INSERT INTO stress (v) VALUES (?)`,
				fmt.Sprintf("%s-%d", label, i))
			elapsed := time.Since(start).Nanoseconds()
			// Record max via CAS loop so concurrent writers don't
			// stomp each other's measurements.
			for {
				prev := maxExec.Load()
				if elapsed <= prev || maxExec.CompareAndSwap(prev, elapsed) {
					break
				}
			}
			if err != nil {
				errCh <- err
				return
			}
		}
	}

	wg.Add(totalWriters)
	for w := range writersPerHandle {
		go insert(h1, fmt.Sprintf("h1-w%d", w))
		go insert(h2, fmt.Sprintf("h2-w%d", w))
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		r.NoError(err, "concurrent insert")
	}

	var total int
	r.NoError(h1.QueryRow(`SELECT COUNT(*) FROM stress`).Scan(&total))
	r.Equal(totalWriters*perWriter, total)

	// Diagnostic: with two handles racing for the write lock, at
	// least one Exec must have observed contention and waited >1ms
	// for busy_timeout to retry. If this fires under 1ms the test
	// isn't actually exercising what it claims.
	maxObserved := time.Duration(maxExec.Load())
	t.Logf("max single Exec under contention: %v", maxObserved)
	r.Greater(maxObserved, time.Millisecond,
		"expected at least one Exec >1ms (busy_timeout retry); got %v — test is not exercising real contention",
		maxObserved)
}
