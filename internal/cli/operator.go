package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"go.kenn.io/fotobank/internal/config"
	"go.kenn.io/fotobank/internal/owners"
)

// localOperatorConfig locates the server without opening SQLite or Docbank.
func localOperatorConfig(configPath string) (string, owners.Principal, error) {
	if configPath == "" {
		configPath = config.DefaultConfigPath()
	}
	cfg, err := config.LoadUnchecked(configPath)
	if err != nil {
		return "", owners.Principal{}, err
	}
	if cfg.Identity.Mode != "stub" {
		return "", owners.Principal{}, fmt.Errorf("local operator commands require identity.mode = stub")
	}
	dbPath, err := resolveDBPath(cfg)
	return dbPath, owners.Principal{Hub: cfg.Identity.Stub.Hub, UserID: cfg.Identity.Stub.UserID}, err
}

// localOperatorPath leaves symlink-sensitive parent components for the server
// to resolve, while making ordinary relative paths independent of its cwd.
func localOperatorPath(value string) (string, error) {
	if filepath.IsAbs(value) {
		return value, nil
	}
	if filepath.VolumeName(value) != "" || (os.PathSeparator == '\\' && len(value) > 0 && os.IsPathSeparator(value[0])) {
		return "", fmt.Errorf("operator path must be a fully qualified path or relative to the working directory")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return cwd + string(os.PathSeparator) + value, nil
}
