package backup

import (
	"context"
	"database/sql"
	"fmt"
	"go.kenn.io/fotobank/internal/db"
	"os"
)

// ValidateSnapshot checks SQLite integrity and the catalog schema marker.
func ValidateSnapshot(ctx context.Context, path string) error {
	// The snapshot DB may carry vec0 schema objects; register the
	// sqlite-vec extension before opening so SQLite can resolve
	// vec0 virtual tables when invoked from CLI tools that have not
	// yet driven the live DB through db.Open.
	db.RegisterSqliteVec()
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	if info.Size() == 0 {
		return fmt.Errorf("%s is zero bytes (not a SQLite database)", path)
	}
	dsn := buildDSN(path)
	d, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return fmt.Errorf("open snapshot: %w", err)
	}
	defer d.Close()
	var s string
	if err := d.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&s); err != nil {
		return fmt.Errorf("integrity_check: %w", err)
	}
	if s != "ok" {
		return fmt.Errorf("integrity_check returned %q", s)
	}
	// Reject DBs that lack the fotobank schema marker. golang-migrate
	// creates schema_migrations as the very first table on any opened
	// DB, so its presence is a strong signal the file came from a
	// fotobank instance and not from an unrelated SQLite app or a
	// freshly-initialized empty DB. A bare empty DB has zero rows in
	// sqlite_master; an unrelated SQLite DB with a dummy table also
	// fails this check unless it happens to be using golang-migrate
	// with the same convention.
	var hasMigrations int
	const q = `SELECT count(*) FROM sqlite_master
	           WHERE type='table' AND name='schema_migrations'`
	if err := d.QueryRowContext(ctx, q).Scan(&hasMigrations); err != nil {
		return fmt.Errorf("check schema_migrations: %w", err)
	}
	if hasMigrations == 0 {
		return fmt.Errorf("%s lacks schema_migrations table (not a fotobank snapshot)", path)
	}
	return nil
}
