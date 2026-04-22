// Package db owns the SQLite connection pools and transactional
// helpers. Writes serialise through a single RW pool (MaxOpenConns=1)
// to avoid busy-retry storms; reads fan out through a larger RO pool.
package db

import (
	"context"
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
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
// The DSN enables a 5-second busy timeout and foreign-key enforcement
// per connection.
func Open(path string) (*DB, error) {
	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	rw, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	rw.SetMaxOpenConns(1)

	ro, err := sql.Open("sqlite", dsn)
	if err != nil {
		_ = rw.Close()
		return nil, fmt.Errorf("open db ro: %w", err)
	}
	ro.SetMaxOpenConns(4)

	d := &DB{rw: rw, ro: ro}
	if err := d.init(); err != nil {
		_ = d.Close()
		return nil, err
	}
	return d, nil
}

func (d *DB) init() error {
	if _, err := d.rw.Exec("PRAGMA journal_mode=WAL"); err != nil {
		return fmt.Errorf("enable WAL: %w", err)
	}
	if err := runMigrations(d.rw); err != nil {
		return err
	}
	return nil
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
