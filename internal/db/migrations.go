package db

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strconv"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	migratedb "github.com/golang-migrate/migrate/v4/database"
	migratesqlite "github.com/golang-migrate/migrate/v4/database/sqlite3"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

const migrationTableName = "schema_migrations"

//go:embed migrations/*.sql
var migrationFiles embed.FS

// runMigrations applies all embedded up-migrations to rw. Safe to run
// against a fresh file or a previously-migrated one. Returns an error
// if the database is in a dirty migration state or the DB schema is
// newer than this binary's embedded migration set.
func runMigrations(rw *sql.DB) error {
	sourceDriver, err := iofs.New(migrationFiles, "migrations")
	if err != nil {
		return fmt.Errorf("load embedded migrations: %w", err)
	}

	databaseDriver, err := migratesqlite.WithInstance(rw, &migratesqlite.Config{
		MigrationsTable: migrationTableName,
	})
	if err != nil {
		return fmt.Errorf("open migration driver: %w", err)
	}

	version, dirty, err := databaseDriver.Version()
	if err != nil {
		return fmt.Errorf("read migration version: %w", err)
	}
	if dirty {
		return fmt.Errorf("database is in a dirty migration state")
	}

	latest, err := latestMigrationVersion()
	if err != nil {
		return fmt.Errorf("read embedded migration versions: %w", err)
	}
	if version != migratedb.NilVersion && version > latest {
		return fmt.Errorf(
			"schema version %d is newer than this binary (expects %d); upgrade fotobank",
			version, latest,
		)
	}

	m, err := migrate.NewWithInstance("iofs", sourceDriver, "sqlite3", databaseDriver)
	if err != nil {
		return fmt.Errorf("create migrator: %w", err)
	}
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}

// latestMigrationVersion returns the highest migration version found in
// the embedded migrations directory. Returns (0, nil) when empty.
func latestMigrationVersion() (int, error) {
	files, err := fs.Glob(migrationFiles, "migrations/*.up.sql")
	if err != nil {
		return 0, err
	}
	latest := migratedb.NilVersion
	for _, file := range files {
		name := path.Base(file)
		prefix := strings.TrimSuffix(name, ".up.sql")
		versionText, _, found := strings.Cut(prefix, "_")
		if !found {
			return 0, fmt.Errorf("parse migration version from %q", name)
		}
		v, err := strconv.Atoi(versionText)
		if err != nil {
			return 0, fmt.Errorf("parse migration version from %q: %w", name, err)
		}
		if v > latest {
			latest = v
		}
	}
	if latest == migratedb.NilVersion {
		return 0, nil
	}
	return latest, nil
}
