package db_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

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
