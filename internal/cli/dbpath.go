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
func resolveDBPath(cfg *config.Config) string {
	if v := os.Getenv("FOTOBANK_DB_PATH"); v != "" {
		return v
	}
	return filepath.Join(cfg.Flash.Root, "fotobank.sqlite")
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
