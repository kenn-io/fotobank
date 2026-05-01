// Package db owns the SQLite connection pools and transactional
// helpers. Writes serialise through a single RW pool (MaxOpenConns=1)
// to avoid busy-retry storms; reads fan out through a larger RO pool.
package db

import (
	"context"
	"database/sql"
	"fmt"
)

// DB holds separate read-write and read-only connections to the SQLite
// database file.
type DB struct {
	rw *sql.DB
	ro *sql.DB
}

// Open opens (or creates) a SQLite database at path, enables WAL mode,
// and returns a DB. Callers should defer Close.
//
// The RW pool writes through a 5-second busy timeout with foreign-key
// enforcement. The RO pool opens the same file with SQLite's
// mode=ro URI parameter so reader connections cannot mutate data, even
// if a caller forgets and invokes Exec against ReadDB().
func Open(path string) (*DB, error) {
	RegisterSqliteVec()
	rwDSN := path + "?_busy_timeout=5000&_fk=1"
	rw, err := sql.Open("sqlite3", rwDSN)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	rw.SetMaxOpenConns(1)

	// Initialise the file through the RW pool before opening the RO
	// pool: SQLite's mode=ro refuses to create a missing database.
	if _, err := rw.Exec("PRAGMA journal_mode=WAL"); err != nil {
		_ = rw.Close()
		return nil, fmt.Errorf("enable WAL: %w", err)
	}
	if err := runMigrations(rw); err != nil {
		_ = rw.Close()
		return nil, err
	}

	roDSN := "file:" + path + "?_busy_timeout=5000&_fk=1&mode=ro"
	ro, err := sql.Open("sqlite3", roDSN)
	if err != nil {
		_ = rw.Close()
		return nil, fmt.Errorf("open db ro: %w", err)
	}
	ro.SetMaxOpenConns(4)

	return &DB{rw: rw, ro: ro}, nil
}

// Close closes both pools. Returns the rw close error (ro close errors
// are discarded).
func (d *DB) Close() error {
	_ = d.ro.Close()
	return d.rw.Close()
}

// ReadDB returns the read-only pool (parallel reads).
func (d *DB) ReadDB() *sql.DB { return d.ro }

// WriteDB returns the read-write pool (single writer, serialises
// writes).
func (d *DB) WriteDB() *sql.DB { return d.rw }

// Tx runs fn inside a transaction on the RW pool. Commits if fn
// returns nil; rolls back on any error.
func (d *DB) Tx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := d.rw.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}
