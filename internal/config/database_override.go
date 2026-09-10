package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// DatabaseOverride identifies the environment's catalog choice without touching
// storage. An empty value means the configured default. Preserve components
// rather than cleaning symlink-plus-.. paths; aliases may require an explicit
// restart, but cannot silently select a different catalog.
func DatabaseOverride() (string, error) {
	path, err := absoluteConfiguredPath(filepath.FromSlash(os.Getenv("FOTOBANK_DB_PATH")))
	if err != nil {
		return "", fmt.Errorf("resolve database override working directory: %w", err)
	}
	return path, nil
}
