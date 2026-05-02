package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/wesm/fotobank/internal/ai"
	"github.com/wesm/fotobank/internal/ai/embedding"
	"github.com/wesm/fotobank/internal/ai/imginput"
	"github.com/wesm/fotobank/internal/ai/jobs"
	aiprompts "github.com/wesm/fotobank/internal/ai/prompts"
	"github.com/wesm/fotobank/internal/ai/skipped"
	"github.com/wesm/fotobank/internal/config"
	"github.com/wesm/fotobank/internal/db"
	"github.com/wesm/fotobank/internal/errs"
	"github.com/wesm/fotobank/internal/geo"
	"github.com/wesm/fotobank/internal/ingest"
	"github.com/wesm/fotobank/internal/media"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/service"
)

// newImportCmd wires the `fotobank import` subcommand. It takes a single
// positional source directory and runs the ingest importer against the
// currently configured NAS store and media repo.
func newImportCmd() *cobra.Command {
	var (
		cfgPath string
		workers int
		wait    time.Duration
	)
	cmd := &cobra.Command{
		Use:   "import <source-dir>",
		Short: "Import media from a source directory into fotobank",
		Args:  usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runImport(cmd.Context(), importOpts{
				cfgPath: cfgPath,
				source:  args[0],
				workers: workers,
				wait:    wait,
				stdout:  cmd.OutOrStdout(),
				stderr:  cmd.ErrOrStderr(),
			})
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", "", "path to config file (defaults to DefaultConfigPath)")
	cmd.Flags().IntVar(&workers, "workers", 0, "import worker count (0 uses [imports].concurrent_workers)")
	cmd.Flags().DurationVar(&wait, "wait", 0, "max wait for the import lock before failing (0 = fail fast)")
	return cmd
}

type importOpts struct {
	cfgPath string
	source  string
	workers int
	wait    time.Duration
	stdout  io.Writer
	stderr  io.Writer
}

// runImport loads the config, opens the DB, resolves the owner from the
// stub-identity config, acquires the import file lock, and drives
// ingest.Importer.ImportDirectory. A non-empty Result.Failures causes a
// non-nil return so the CLI exits with code 1.
func runImport(ctx context.Context, opts importOpts) error {
	path := opts.cfgPath
	if path == "" {
		path = config.DefaultConfigPath()
	}
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	if cfg.Identity.Mode != "stub" {
		return fmt.Errorf("fotobank import requires identity.mode = stub (got %q)", cfg.Identity.Mode)
	}

	dbPath := os.Getenv("FOTOBANK_DB_PATH")
	if dbPath == "" {
		dbPath = filepath.Join(cfg.Flash.Root, "fotobank.sqlite")
	}
	d, err := db.Open(dbPath)
	if err != nil {
		return err
	}
	defer d.Close()

	owner := owners.Principal{
		Hub:    cfg.Identity.Stub.Hub,
		UserID: cfg.Identity.Stub.UserID,
	}
	storageKey := cfg.Identity.Stub.StorageKey
	if storageKey == "" {
		storageKey = cfg.Identity.Stub.UserID
	}
	ownerSvc := service.NewOwnerService(owners.NewRepo(d.WriteDB(), d.ReadDB()))
	if err := ownerSvc.Ensure(ctx, owner, storageKey); err != nil {
		return err
	}

	keys, err := loadStorageKeys(ctx, ownerSvc)
	if err != nil {
		return err
	}
	storeLayer, _ := buildStorageLayer(cfg, keys)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())

	lockPath := cfg.Imports.FileLockPath
	if lockPath == "" {
		lockPath = filepath.Join(cfg.NAS.Root, ".fotobank", "import.lock")
	}
	unlock, err := ingest.Acquire(ctx, lockPath, opts.wait)
	if err != nil {
		if errors.Is(err, errs.ErrConcurrentImport) {
			return fmt.Errorf("another import is in progress (lock: %s)", lockPath)
		}
		return err
	}
	defer unlock()

	workers := opts.workers
	if workers <= 0 {
		workers = cfg.Imports.ConcurrentWorkers
	}

	places, err := geo.NewNaturalEarth()
	if err != nil {
		return fmt.Errorf("load geo gazetteer: %w", err)
	}
	imp := ingest.NewImporter(storeLayer, repo, places)

	// Wire the production AIEnqueuer so an offline import auto-enqueues
	// for AI processing. The fingerprints capture the active (model,
	// prompt, profile) triple at boot; if [ai].enabled is false the
	// worker pool is dormant but enqueued rows will be processed once
	// the operator flips the flag.
	aiQueue := jobs.NewQueue(d.WriteDB(), d.ReadDB())
	aiSkippedRepo := skipped.NewRepo(d.WriteDB(), d.ReadDB())
	tagPrompt := aiprompts.Tag()
	captionPrompt := aiprompts.Caption()
	tagFP := ai.Fingerprint{
		ModelID:       cfg.AI.Tag.Model,
		PromptVersion: tagPrompt.Version,
		InputProfile:  imginput.ProfileV1,
	}
	captionFP := ai.Fingerprint{
		ModelID:       cfg.AI.Caption.Model,
		PromptVersion: captionPrompt.Version,
		InputProfile:  imginput.ProfileV1,
	}
	enq := ingest.NewRealAIEnqueuer(
		tagFP, captionFP,
		aiQueue.Enqueue,
		aiSkippedRepo.Record,
	)
	// Wire embed-task enqueueing only when the operator has explicitly
	// flipped cfg.AI.Embed.Enabled. Defer config defaults via
	// embedding.Fingerprint so an InputEdge omitted in config still
	// produces the canonical 384-edge fingerprint after Validate.
	if cfg.AI.Embed.Enabled {
		enq.WithEmbed(embedding.Fingerprint(cfg.AI.Embed))
	}
	imp.SetAIEnqueuer(enq)

	res, err := imp.ImportDirectory(ctx, opts.source, ingest.Options{
		Owner:             owner,
		ConcurrentWorkers: workers,
	})
	if err != nil {
		return err
	}

	fmt.Fprintf(opts.stdout, "imported=%d\tduplicates=%d\tpath_collisions=%d\tfailures=%d\n",
		res.Imported, res.Duplicates, res.PathCollisions, len(res.Failures))

	if len(res.Failures) > 0 {
		for _, f := range res.Failures {
			fmt.Fprintln(opts.stderr, f)
		}
		return fmt.Errorf("import completed with %d failure(s)", len(res.Failures))
	}
	return nil
}
