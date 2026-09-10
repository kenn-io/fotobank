package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// CatalogSelection resolves the normal-mode catalog, including its existing
// filesystem aliases. Recovery must not call this source-dependent operation.
func CatalogSelection(cfg *Config) (string, error) {
	selected, err := ConfiguredDatabasePath(cfg)
	if err != nil {
		return "", err
	}
	return ResolveDatabasePath(selected)
}

// ConfiguredDatabasePath preserves the source selection and working directory
// without resolving symlinks or requiring source storage to be available.
func ConfiguredDatabasePath(cfg *Config) (string, error) {
	selected := os.Getenv("FOTOBANK_DB_PATH")
	if selected == "" {
		selected = cfg.ConfiguredFlashRoot() + string(os.PathSeparator) + "fotobank.sqlite"
	}
	return absoluteConfiguredPath(filepath.FromSlash(selected))
}

// ResolveDatabasePath resolves aliases before deriving database and lock paths.
// Missing final components are allowed, but dangling symlinks are not.
func ResolveDatabasePath(selected string) (string, error) {
	path, err := canonicalConfigPath(filepath.FromSlash(selected))
	if err != nil {
		return "", fmt.Errorf("resolve database path: %w", err)
	}
	return path, nil
}
