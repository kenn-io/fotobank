package db

import (
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
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

// MigrationsFingerprint returns a stable hex hash of every embedded
// migration file (name + content), so consumers can include schema
// shape in cache keys. Renaming a file or editing one byte of SQL
// changes the result. Used by testutil/scalecache so an in-place edit
// to 000001_initial_schema.up.sql busts cached fixtures even when the
// media-insert SQL is unchanged.
func MigrationsFingerprint() string {
	files, err := fs.Glob(migrationFiles, "migrations/*.sql")
	if err != nil {
		// embed.FS Glob over a literal pattern doesn't return errors
		// for missing files; an error here means the embed itself is
		// broken, in which case the binary is unusable.
		panic(fmt.Errorf("scan migrations for fingerprint: %w", err))
	}
	sort.Strings(files)
	h := sha256.New()
	for _, f := range files {
		h.Write([]byte(f))
		h.Write([]byte{0})
		b, err := migrationFiles.ReadFile(f)
		if err != nil {
			panic(fmt.Errorf("read embedded migration %q: %w", f, err))
		}
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil))
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
