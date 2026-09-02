package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	airuntime "go.kenn.io/fotobank/internal/ai/runtime"
	appsettingsstore "go.kenn.io/fotobank/internal/appsettings"
	"go.kenn.io/fotobank/internal/config"
	"go.kenn.io/fotobank/internal/content"
	"go.kenn.io/fotobank/internal/geo"
	"go.kenn.io/fotobank/internal/ingest"
	"go.kenn.io/fotobank/internal/media"
	"go.kenn.io/fotobank/internal/owners"
)

func newContentCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "content",
		Short: "Inspect and recover authoritative content",
	}
	cmd.AddCommand(newContentRecoverCmd())
	return cmd
}

func newContentRecoverCmd() *cobra.Command {
	var (
		asJSON bool
		wait   time.Duration
	)
	cmd := &cobra.Command{
		Use:   "recover",
		Short: "Finish interrupted imports and report unmatched content",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfgPath, _ := cmd.Flags().GetString("config")
			return runContentRecovery(cmd.Context(), contentRecoveryOpts{
				configPath: cfgPath,
				wait:       wait,
				asJSON:     asJSON,
				stdout:     cmd.OutOrStdout(),
			})
		},
	}
	cmd.Flags().String("config", "", "path to config file")
	cmd.Flags().DurationVar(&wait, "wait", 0, "max wait for the import lock before failing (0 = fail fast)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON to stdout")
	return cmd
}

type contentRecoveryOpts struct {
	configPath string
	wait       time.Duration
	asJSON     bool
	stdout     io.Writer
}

type ownerRecoveryReport struct {
	Owner       owners.Principal `json:"owner"`
	Adopted     int              `json:"adopted"`
	Finalized   int              `json:"finalized"`
	Pending     int              `json:"pending"`
	Conflicts   int              `json:"conflicts"`
	OrphanPaths []string         `json:"orphan_paths"`
}

func runContentRecovery(ctx context.Context, opts contentRecoveryOpts) error {
	configPath := opts.configPath
	if configPath == "" {
		configPath = config.DefaultConfigPath()
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	dbPath, err := resolveDBPath(cfg)
	if err != nil {
		return err
	}
	d, err := openDatabasePath(dbPath)
	if err != nil {
		return err
	}
	defer d.Close()

	lockPath := cfg.Imports.FileLockPath
	if lockPath == "" {
		lockPath = filepath.Join(cfg.Flash.Root, ".fotobank", "import.lock")
	}
	unlock, err := ingest.Acquire(ctx, lockPath, opts.wait)
	if err != nil {
		return err
	}
	defer unlock()

	contentStore, err := content.Open(ctx, content.Config{Root: cfg.Docbank.Root})
	if err != nil {
		return fmt.Errorf("open Docbank vault: %w", err)
	}
	defer contentStore.Close()

	ownerRepo := owners.NewRepo(d.WriteDB(), d.ReadDB())
	registeredOwners, err := ownerRepo.List(ctx)
	if err != nil {
		return err
	}
	aiProvider, err := airuntime.NewProvider(ctx, airuntime.Source{
		FilePath: configPath,
		Repo:     appsettingsstore.NewRepo(d.WriteDB(), d.ReadDB()),
	})
	if err != nil {
		return fmt.Errorf("load effective ai config: %w", err)
	}
	enqueuer := newIngestAIEnqueuer(d.DB, aiProvider.Effective())
	assets := media.NewAssetRepo(d.WriteDB(), d.ReadDB())
	mediaRepo := media.NewRepo(d.WriteDB(), d.ReadDB())
	places, err := geo.NewNaturalEarth()
	if err != nil {
		return fmt.Errorf("load geo gazetteer: %w", err)
	}
	reports := make([]ownerRecoveryReport, 0, len(registeredOwners))
	for _, registeredOwner := range registeredOwners {
		recoverer := ingest.NewImporter(
			contentStore,
			assets,
			mediaRepo,
			registeredOwner.StorageKey,
			places,
		)
		recoverer.SetAIEnqueuer(enqueuer)
		result, err := recoverer.RecoverOwner(ctx, registeredOwner.Principal)
		if err != nil {
			return fmt.Errorf("recover owner %s: %w", registeredOwner.Principal, err)
		}
		report := ownerRecoveryReport{
			Owner: registeredOwner.Principal, Adopted: result.Adopted,
			Finalized: result.Finalized, Pending: result.Pending,
			Conflicts:   result.Conflicts,
			OrphanPaths: make([]string, 0, len(result.Orphans)),
		}
		for _, orphan := range result.Orphans {
			report.OrphanPaths = append(report.OrphanPaths, orphan.VirtualPath)
		}
		reports = append(reports, report)
	}
	if opts.asJSON {
		return json.NewEncoder(opts.stdout).Encode(reports)
	}
	for _, report := range reports {
		fmt.Fprintf(opts.stdout,
			"owner=%s adopted=%d finalized=%d pending=%d conflicts=%d orphans=%d\n",
			report.Owner, report.Adopted, report.Finalized, report.Pending,
			report.Conflicts, len(report.OrphanPaths))
		for _, orphanPath := range report.OrphanPaths {
			fmt.Fprintf(opts.stdout, "  unmatched: %s\n", orphanPath)
		}
	}
	return nil
}
