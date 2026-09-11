package cli

import (
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"go.kenn.io/fotobank/internal/config"
	"go.kenn.io/fotobank/internal/content"
	"go.kenn.io/fotobank/internal/errs"
)

const sqliteHeader = "SQLite format 3\x00"

type configDiagnostic struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
	Action string `json:"action,omitempty"`
}

func newConfigDiagnoseCmd() *cobra.Command {
	var cfgPath string
	var asJSON bool
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
				if !asJSON {
					fmt.Fprintf(cmd.OutOrStdout(), "%-20s %-8s %s\n", check.Name, check.Status, check.Detail)
					if check.Action != "" {
						fmt.Fprintf(cmd.OutOrStdout(), "  action: %s\n", check.Action)
					}
				}
				failed = failed || check.Status == "error"
			}
			if asJSON {
				if err := json.MarshalWrite(cmd.OutOrStdout(), checks); err != nil {
					return fmt.Errorf("write configuration diagnostics: %w", err)
				}
			}
			if failed {
				return fmt.Errorf("%w: one or more configuration diagnostics failed", errs.ErrBadConfiguration)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", "", "path to config file (defaults to DefaultConfigPath)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit diagnostic checks as JSON")
	return cmd
}

func diagnoseConfig(path string) []configDiagnostic {
	cfg, err := config.LoadUnchecked(path)
	if err != nil {
		return []configDiagnostic{{
			Name: "configuration", Status: "error", Detail: err.Error(),
			Action: "fix the named setting in " + path + " and run this command again",
		}}
	}
	if cfg == nil {
		return []configDiagnostic{{
			Name: "configuration", Status: "error", Detail: "configuration loader returned no result",
			Action: "check that " + path + " is a readable TOML configuration file",
		}}
	}
	if err := cfg.ValidateWithOptions(config.ValidationOptions{AllowUnavailableStorage: true}); err != nil {
		return []configDiagnostic{{
			Name: "configuration", Status: "error", Detail: err.Error(),
			Action: "fix the named setting in " + path + " and run this command again",
		}}
	}

	checks := []configDiagnostic{{
		Name: "configuration", Status: "ok", Detail: path,
	}}

	dbPath, err := resolveDBPath(cfg)
	if err != nil {
		checks = append(checks, configDiagnostic{
			Name: "sqlite", Status: "error", Detail: err.Error(),
			Action: "fix [flash].root or FOTOBANK_DB_PATH",
		})
	} else if err := inspectSQLiteFile(dbPath); err != nil {
		checks = append(checks, configDiagnostic{
			Name: "sqlite", Status: "error", Detail: fmt.Sprintf("%s: %v", dbPath, err),
			Action: "make the database readable, or run fotobank serve once to initialize a new database",
		})
	} else {
		checks = append(checks, configDiagnostic{
			Name: "sqlite", Status: "ok", Detail: dbPath,
		})
	}

	if err := content.InspectVault(cfg.Docbank.Root); err != nil {
		checks = append(checks, configDiagnostic{
			Name: "docbank", Status: "error", Detail: fmt.Sprintf("%s: %v", cfg.Docbank.Root, err),
			Action: "make [docbank].root available and initialize the vault with fotobank serve",
		})
	} else {
		checks = append(checks, configDiagnostic{
			Name: "docbank", Status: "ok", Detail: cfg.Docbank.Root,
		})
	}

	nasErr := inspectDirectory(cfg.NAS.Root)
	if nasErr != nil {
		checks = append(checks, configDiagnostic{
			Name: "nas artifacts", Status: "error", Detail: fmt.Sprintf("%s: %v", cfg.NAS.Root, nasErr),
			Action: "mount or create [nas].root before importing or serving artifacts",
		})
	} else {
		checks = append(checks, configDiagnostic{
			Name: "nas artifacts", Status: "ok", Detail: cfg.NAS.Root + " is available",
		})
	}

	checks = append(checks, configDiagnostic{
		Name: "checkout boundaries", Status: "ok",
		Detail: "[docbank].root, [nas].root, and [flash].root passed configured boundary checks",
	})
	checks = append(checks, configDiagnostic{
		Name: "identity", Status: "ok", Detail: "mode=" + cfg.Identity.Mode,
	})

	if !cfg.Backup.Enabled {
		checks = append(checks, configDiagnostic{
			Name: "backups", Status: "disabled", Detail: "[backup].enabled=false",
		})
		return checks
	}
	if _, err := content.OpenBackupRepository(cfg.Backup.Repository); err != nil {
		checks = append(checks, configDiagnostic{
			Name: "backups", Status: "error", Detail: fmt.Sprintf("%s: %v", cfg.Backup.Repository, err),
			Action: "make [backup].repository available; initialize a new repository with fotobank backup init --repo PATH",
		})
	} else {
		checks = append(checks, configDiagnostic{Name: "backups", Status: "ok", Detail: cfg.Backup.Repository + " is an initialized archive repository"})
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
