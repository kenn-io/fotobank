package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// CatalogSelection resolves the normal-mode catalog, including its existing
// filesystem aliases. Recovery must not call this source-dependent operation.
func CatalogSelection(cfg *Config) (string, error) {
	selected := os.Getenv("FOTOBANK_DB_PATH")
	if selected == "" {
		selected = cfg.ConfiguredFlashRoot() + string(os.PathSeparator) + "fotobank.sqlite"
	}
	return ResolveDatabasePath(selected)
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
