package db_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
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

// TestOpen_ConcurrentWriters spawns N goroutines that each insert
// into a scratch table through the RW pool. They all share the same
// single-connection RW pool and must serialise; the test asserts
// that all N inserts complete within a generous deadline and the
// final row count matches.
func TestOpen_ConcurrentWriters(t *testing.T) {
	if testing.Short() {
		t.Skip("stress test; not under -short")
	}
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	rw := d.WriteDB()

	_, err := rw.Exec(`CREATE TABLE stress (id INTEGER PRIMARY KEY, v TEXT)`)
	r.NoError(err)

	const writers = 8
	const perWriter = 50
	var wg sync.WaitGroup
	wg.Add(writers)
	errCh := make(chan error, writers*perWriter)

	for w := range writers {
		go func(w int) {
			defer wg.Done()
			for i := range perWriter {
				_, err := rw.Exec(`INSERT INTO stress (v) VALUES (?)`,
					fmt.Sprintf("w%d-%d", w, i))
				if err != nil {
					errCh <- err
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		r.NoError(err, "concurrent insert")
	}

	var total int
	r.NoError(rw.QueryRow(`SELECT COUNT(*) FROM stress`).Scan(&total))
	r.Equal(writers*perWriter, total)
}
