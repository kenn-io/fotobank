package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"go.kenn.io/fotobank/internal/config"
)

// resolveDBPath returns the canonical SQLite path the server and CLI
// agree on. FOTOBANK_DB_PATH wins; otherwise default to
// {flash}/fotobank.sqlite. Mirrors the existing logic in runServer
// so that backup commands (and any future shared logic) cannot drift.
//
// Existing symlinks are resolved before deriving the lock path. For a new
// database, the deepest existing ancestor is resolved and missing components
// are appended beneath it.
func resolveDBPath(cfg *config.Config) (string, error) {
	var p string
	if v := os.Getenv("FOTOBANK_DB_PATH"); v != "" {
		p = v
	} else {
		p = filepath.Join(cfg.Flash.Root, "fotobank.sqlite")
	}
	return canonicalDBPath(p)
}

// canonicalDBPath resolves existing symlinks before a database or its lock is
// opened. Missing final components are retained beneath the resolved ancestor.
func canonicalDBPath(p string) (string, error) {
	target := p
	if !filepath.IsAbs(target) {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("get working directory: %w", err)
		}
		target = cwd + string(os.PathSeparator) + target
	}
	current := strings.TrimRight(target, string(os.PathSeparator))
	if current == "" {
		current = string(os.PathSeparator)
	}
	var missing []string
	for {
		_, err := os.Lstat(current)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("inspect database path: %w", err)
		}
		parent, component := rawDBPathParent(current)
		if parent == current {
			return "", fmt.Errorf("find existing database path ancestor: %w", err)
		}
		if component == "." || component == ".." {
			return "", fmt.Errorf("database path traverses %q after a missing component", component)
		}
		missing = append(missing, component)
		current = parent
	}
	resolved, err := filepath.EvalSymlinks(current)
	if err != nil {
		return "", fmt.Errorf("resolve database path: %w", err)
	}
	for _, m := range slices.Backward(missing) {
		resolved = filepath.Join(resolved, m)
	}
	return resolved, nil
}

// rawDBPathParent splits one path component without cleaning the path first.
// Cleaning would give ".." lexical semantics before the operating system has
// resolved a preceding symlink.
func rawDBPathParent(value string) (string, string) {
	volume := filepath.VolumeName(value)
	remainder := value[len(volume):]
	remainder = strings.TrimRight(remainder, string(os.PathSeparator))
	index := strings.LastIndex(remainder, string(os.PathSeparator))
	if index < 0 {
		return value, ""
	}
	component := remainder[index+1:]
	parentRemainder := strings.TrimRight(remainder[:index], string(os.PathSeparator))
	if parentRemainder == "" {
		parentRemainder = string(os.PathSeparator)
	}
	return volume + parentRemainder, component
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
