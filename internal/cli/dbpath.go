package cli

import "go.kenn.io/fotobank/internal/config"

// resolveDBPath returns the canonical SQLite path the server and CLI
// agree on. FOTOBANK_DB_PATH wins; otherwise default to
// {flash}/fotobank.sqlite. Discovery uses the same shared resolver.
//
// Existing symlinks are resolved before deriving the lock path. For a new
// database, the deepest existing ancestor is resolved and missing components
// are appended beneath it.
func resolveDBPath(cfg *config.Config) (string, error) {
	return config.CatalogSelection(cfg)
}

// lockPathFor returns the canonical lock-file path for a given dbPath.
// All database users use this so the lock is consistent
// regardless of FOTOBANK_DB_PATH overrides.
func lockPathFor(dbPath string) string {
	return dbPath + ".lock"
}
