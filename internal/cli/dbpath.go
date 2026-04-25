package cli

import (
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/wesm/fotobank/internal/config"
)

// resolveDBPath returns the absolute SQLite path the server and CLI
// agree on. FOTOBANK_DB_PATH wins; otherwise default to
// {flash}/fotobank.sqlite. Mirrors the existing logic in runServer
// so that backup commands (and any future shared logic) cannot drift.
//
// The path is canonicalized via filepath.Abs so that two equivalent
// spellings (e.g. one relative, one absolute) produce the same lock
// file under lockPathFor — without this, the lifetime fence can be
// bypassed by spelling the same DB two different ways.
func resolveDBPath(cfg *config.Config) string {
	var p string
	if v := os.Getenv("FOTOBANK_DB_PATH"); v != "" {
		p = v
	} else {
		p = filepath.Join(cfg.Flash.Root, "fotobank.sqlite")
	}
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	return p
}

// lockPathFor returns the canonical lock-file path for a given dbPath.
// Both the server and backup.Restore use this so the lock is consistent
// regardless of FOTOBANK_DB_PATH overrides.
func lockPathFor(dbPath string) string {
	return dbPath + ".lock"
}

// loadConfigFromCmd reads the --config flag from cmd, falling back to
// config.DefaultConfigPath() when unset, and returns the parsed config.
// Subcommands that need both the config and the DB path go through this
// helper so the precedence rules stay in one place.
func loadConfigFromCmd(cmd *cobra.Command) (*config.Config, error) {
	cfgPath, _ := cmd.Flags().GetString("config")
	if cfgPath == "" {
		cfgPath = config.DefaultConfigPath()
	}
	return config.Load(cfgPath)
}
