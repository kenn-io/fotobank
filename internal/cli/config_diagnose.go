package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"go.kenn.io/fotobank/internal/config"
	"go.kenn.io/fotobank/internal/content"
	"go.kenn.io/fotobank/internal/errs"
)

const sqliteHeader = "SQLite format 3\x00"

type configDiagnostic struct {
	name   string
	status string
	detail string
	action string
}

func newConfigDiagnoseCmd() *cobra.Command {
	var cfgPath string
	cmd := &cobra.Command{
		Use:   "diagnose",
		Short: "Check configuration and required storage without changing them",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			path := cfgPath
			if path == "" {
				path = config.DefaultConfigPath()
			}
			checks := diagnoseConfig(path)
			failed := false
			for _, check := range checks {
				fmt.Fprintf(cmd.OutOrStdout(), "%-20s %-8s %s\n", check.name, check.status, check.detail)
				if check.action != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "  action: %s\n", check.action)
				}
				failed = failed || check.status == "error"
			}
			if failed {
				return fmt.Errorf("%w: one or more configuration diagnostics failed", errs.ErrBadConfiguration)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", "", "path to config file (defaults to DefaultConfigPath)")
	return cmd
}

func diagnoseConfig(path string) []configDiagnostic {
	cfg, err := config.LoadUnchecked(path)
	if err != nil {
		return []configDiagnostic{{
			name: "configuration", status: "error", detail: err.Error(),
			action: "fix the named setting in " + path + " and run this command again",
		}}
	}
	if cfg == nil {
		return []configDiagnostic{{
			name: "configuration", status: "error", detail: "configuration loader returned no result",
			action: "check that " + path + " is a readable TOML configuration file",
		}}
	}
	if err := cfg.ValidateWithOptions(config.ValidationOptions{AllowUnavailableStorage: true}); err != nil {
		return []configDiagnostic{{
			name: "configuration", status: "error", detail: err.Error(),
			action: "fix the named setting in " + path + " and run this command again",
		}}
	}

	checks := []configDiagnostic{{
		name: "configuration", status: "ok", detail: path,
	}}

	dbPath, err := resolveDBPath(cfg)
	if err != nil {
		checks = append(checks, configDiagnostic{
			name: "sqlite", status: "error", detail: err.Error(),
			action: "fix [flash].root or FOTOBANK_DB_PATH",
		})
	} else if err := inspectSQLiteFile(dbPath); err != nil {
		checks = append(checks, configDiagnostic{
			name: "sqlite", status: "error", detail: fmt.Sprintf("%s: %v", dbPath, err),
			action: "make the database readable, or run fotobank serve once to initialize a new database",
		})
	} else {
		checks = append(checks, configDiagnostic{
			name: "sqlite", status: "ok", detail: dbPath,
		})
	}

	if err := content.InspectVault(cfg.Docbank.Root); err != nil {
		checks = append(checks, configDiagnostic{
			name: "docbank", status: "error", detail: fmt.Sprintf("%s: %v", cfg.Docbank.Root, err),
			action: "make [docbank].root available and initialize the vault with fotobank serve",
		})
	} else {
		checks = append(checks, configDiagnostic{
			name: "docbank", status: "ok", detail: cfg.Docbank.Root,
		})
	}

	nasErr := inspectDirectory(cfg.NAS.Root)
	if nasErr != nil {
		checks = append(checks, configDiagnostic{
			name: "nas artifacts", status: "error", detail: fmt.Sprintf("%s: %v", cfg.NAS.Root, nasErr),
			action: "mount or create [nas].root before importing or serving artifacts",
		})
	} else {
		checks = append(checks, configDiagnostic{
			name: "nas artifacts", status: "ok", detail: cfg.NAS.Root + " is available",
		})
	}

	checks = append(checks, configDiagnostic{
		name: "checkout boundaries", status: "ok",
		detail: "[docbank].root, [nas].root, and [flash].root passed configured boundary checks",
	})
	checks = append(checks, configDiagnostic{
		name: "identity", status: "ok", detail: "mode=" + cfg.Identity.Mode,
	})

	if !cfg.Backup.Enabled {
		checks = append(checks, configDiagnostic{
			name: "backups", status: "disabled", detail: "[backup].enabled=false",
		})
		return checks
	}
	backupDir := backupDirFor(cfg)
	if cfg.Backup.Dir == "" && nasErr != nil {
		checks = append(checks, configDiagnostic{
			name: "backups", status: "error",
			detail: fmt.Sprintf("%s: [nas].root is unavailable: %v", backupDir, nasErr),
			action: "mount [nas].root before Fotobank creates or writes the default backup directory",
		})
		return checks
	}
	status, detail, err := inspectBackupDestination(backupDir)
	if err != nil {
		checks = append(checks, configDiagnostic{
			name: "backups", status: "error", detail: fmt.Sprintf("%s: %v", backupDir, err),
			action: "make [backup].dir, or its existing parent, available to the Fotobank service account",
		})
	} else {
		checks = append(checks, configDiagnostic{
			name: "backups", status: status, detail: detail,
		})
	}
	return checks
}

func inspectSQLiteFile(path string) (retErr error) {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("not a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, file.Close()) }()
	return inspectSQLiteReader(file)
}

func inspectSQLiteReader(reader io.Reader) error {
	header := make([]byte, len(sqliteHeader))
	if _, err := io.ReadFull(reader, header); err != nil {
		return fmt.Errorf("read SQLite header: %w", err)
	}
	if string(header) != sqliteHeader {
		return errors.New("not a SQLite database")
	}
	return nil
}

func inspectDirectory(path string) (retErr error) {
	root, err := os.OpenRoot(path)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, root.Close()) }()
	info, err := root.Stat(".")
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("not a directory")
	}
	return nil
}

func inspectBackupDestination(path string) (string, string, error) {
	ancestor := path
	for {
		info, err := os.Lstat(ancestor)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				info, err = os.Stat(ancestor)
				if err != nil {
					return "", "", fmt.Errorf("resolve %s: %w", ancestor, err)
				}
			}
			if !info.IsDir() {
				if ancestor == path {
					return "", "", errors.New("destination is not a directory")
				}
				return "", "", fmt.Errorf("existing parent %s is not a directory", ancestor)
			}
			if ancestor == path {
				return "ok", path + " is available", nil
			}
			return "ready", fmt.Sprintf("%s will be created beneath %s", path, ancestor), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", "", err
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return "", "", fmt.Errorf("no existing parent: %w", os.ErrNotExist)
		}
		ancestor = parent
	}
}
