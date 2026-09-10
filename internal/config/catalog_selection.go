package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// CatalogSelection identifies the selected catalog without accessing source
// storage. Include the default flash path and the working directory for relative
// paths. Preserve path components until the server resolves filesystem aliases.
func CatalogSelection(cfg *Config) (string, error) {
	selected := os.Getenv("FOTOBANK_DB_PATH")
	if selected == "" {
		selected = cfg.ConfiguredFlashRoot() + string(os.PathSeparator) + "fotobank.sqlite"
	}
	path, err := absoluteConfiguredPath(filepath.FromSlash(selected))
	if err != nil {
		return "", fmt.Errorf("resolve catalog selection working directory: %w", err)
	}
	return path, nil
}
