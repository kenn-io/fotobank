package cli

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"go.kenn.io/fotobank/internal/backup"
	"go.kenn.io/fotobank/internal/config"
	"go.kenn.io/fotobank/internal/content"
)

func newBackupInitCmd() *cobra.Command {
	var repositoryPath string
	var asJSON bool
	cmd := &cobra.Command{
		Use: "init", Short: "Initialize an empty repository for complete recovery archives",
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if repositoryPath == "" {
				return errors.New("--repo is required")
			}
			repository, err := content.InitBackupRepository(repositoryPath)
			if err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
					ID   string `json:"id"`
					Root string `json:"root"`
				}{repository.ID(), repository.Root()})
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "archive repository initialized: %s\n", repository.Root())
			return err
		},
	}
	cmd.Flags().StringVar(&repositoryPath, "repo", "", "new or empty archive repository directory (required)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON to stdout")
	return cmd
}

func newBackupCreateCmd() *cobra.Command {
	var repositoryPath, tag string
	var asJSON bool
	cmd := &cobra.Command{
		Use: "create", Short: "Back up the catalog and Docbank media together",
		Long: "Create a complete recovery archive in an initialized repository. Stop the Fotobank server first so this command can own the embedded vault. Includes hidden media and all owners. Configuration, credentials, caches, and writable checkout files are not captured.",
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) (retErr error) {
			if tag == backup.ScheduledTag {
				return errors.New("this tag is reserved for scheduled recovery points")
			}
			if repositoryPath == "" {
				return errors.New("--repo is required; initialize it with backup init first")
			}
			repository, err := content.OpenBackupRepository(repositoryPath)
			if err != nil {
				return err
			}
			cfgPath, _ := cmd.Flags().GetString("config")
			if cfgPath == "" {
				cfgPath = config.DefaultConfigPath()
			}
			cfg, err := config.LoadUnchecked(cfgPath)
			if err != nil {
				return err
			}
			if err := cfg.ValidateWithOptions(config.ValidationOptions{AllowUnavailableNAS: true}); err != nil {
				return err
			}
			if err := content.InspectVault(cfg.Docbank.Root); err != nil {
				return fmt.Errorf("inspect existing archive source: %w", err)
			}
			databasePath, err := resolveDBPath(cfg)
			if err != nil {
				return err
			}
			lifetime, err := acquireDatabaseLifetime(databasePath)
			if err != nil {
				return err
			}
			defer func() { retErr = errors.Join(retErr, lifetime.Close()) }()
			vault, err := content.Open(cmd.Context(), contentAdapterConfig(cfg, true))
			if err != nil {
				return fmt.Errorf("open archive source (stop the server first): %w", err)
			}
			defer func() { retErr = errors.Join(retErr, vault.Close()) }()
			snapshot, err := backup.CreateArchive(cmd.Context(), lifetime.path, vault, repository, tag)
			if err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(snapshot)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "archive created: %s (%d content bytes)\n", snapshot.ID, snapshot.BlobBytes)
			return err
		},
	}
	cmd.Flags().StringVar(&repositoryPath, "repo", "", "initialized archive repository directory (required)")
	cmd.Flags().StringVar(&tag, "tag", "", "label for this recovery point")
	cmd.Flags().String("config", "", "path to config file")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON to stdout")
	return cmd
}

func newBackupVerifyCmd() *cobra.Command {
	var repositoryPath string
	var asJSON, all bool
	cmd := &cobra.Command{
		Use: "verify [snapshot-id]", Short: "Verify archive metadata, catalog bytes, and media bytes",
		Args: usageArgs(cobra.MaximumNArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			if repositoryPath == "" {
				return errors.New("--repo is required")
			}
			if all && len(args) != 0 {
				return errors.New("--all and a snapshot ID are mutually exclusive")
			}
			repository, err := content.OpenBackupRepository(repositoryPath)
			if err != nil {
				return err
			}
			options := content.BackupVerifyOptions{All: all}
			if len(args) != 0 {
				options.SnapshotID = args[0]
			}
			report, err := repository.Verify(cmd.Context(), options)
			if err != nil {
				return err
			}
			if asJSON {
				if err := json.NewEncoder(cmd.OutOrStdout()).Encode(report); err != nil {
					return err
				}
			} else {
				for _, problem := range report.Problems {
					fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\n", problem.SnapshotID, problem.Detail)
				}
			}
			if len(report.Problems) != 0 {
				return fmt.Errorf("archive verification found %d problems", len(report.Problems))
			}
			if !asJSON {
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "verified %d recovery points (%d bytes read)\n", len(report.Snapshots), report.BytesRead)
			}
			return err
		},
	}
	cmd.Flags().StringVar(&repositoryPath, "repo", "", "archive repository directory (required)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON to stdout")
	cmd.Flags().BoolVar(&all, "all", false, "verify all recovery points (default: latest)")
	return cmd
}

func listArchives(cmd *cobra.Command, repositoryPath string, asJSON bool) error {
	repository, err := content.OpenBackupRepository(repositoryPath)
	if err != nil {
		return err
	}
	snapshots, err := repository.Snapshots()
	if err != nil {
		return err
	}
	if asJSON {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(snapshots)
	}
	for _, snapshot := range snapshots {
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s  %s  %d bytes  %s\n", snapshot.ID, snapshot.CreatedAt, snapshot.BlobBytes, snapshot.Tag); err != nil {
			return err
		}
	}
	return nil
}

func restoreArchive(cmd *cobra.Command, repositoryPath, snapshotID, target string, asJSON bool) error {
	cfgPath, _ := cmd.Flags().GetString("config")
	if cfgPath == "" {
		cfgPath = config.DefaultConfigPath()
	}
	cfg, err := config.LoadUnchecked(cfgPath)
	if err != nil {
		return err
	}
	// Preserve configured aliases as well as validated destinations. Source
	// directories may be missing after a storage loss; none are opened here.
	configuredVault := cfg.Docbank.Root
	if err := cfg.ValidateWithOptions(config.ValidationOptions{AllowUnavailableStorage: true}); err != nil {
		return err
	}
	protected := []string{configuredVault, cfg.Docbank.Root, cfg.ConfiguredNASRoot(), cfg.NAS.Root,
		cfg.ConfiguredFlashRoot(), cfg.Flash.Root, cfg.Backup.Repository}
	target, protected, err = config.ArchiveRestorePaths(target, configuredDBPath(cfg), protected)
	if err != nil {
		return err
	}
	repository, err := content.OpenBackupRepository(repositoryPath)
	if err != nil {
		return err
	}
	report, err := backup.RestoreArchive(cmd.Context(), repository, snapshotID, target, protected)
	if err != nil {
		return err
	}
	if asJSON {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(report)
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "archive restored and verified: %s\nvault: %s\ncatalog: %s\nverified content references: %d\n", report.SnapshotID, report.VaultRoot, report.CatalogPath, report.ReferencesVerified)
	return err
}
