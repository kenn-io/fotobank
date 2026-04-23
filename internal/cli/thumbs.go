package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/wesm/fotobank/internal/config"
	"github.com/wesm/fotobank/internal/db"
	"github.com/wesm/fotobank/internal/media"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/thumb"
)

// newThumbsCmd wires the `fotobank thumbs` command group. It has no
// runtime behavior of its own — running `fotobank thumbs` without a
// subcommand prints usage and returns a usage error.
func newThumbsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "thumbs",
		Short: "Manage thumbnail generation",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			_ = cmd.Usage()
			return newUsageError("a subcommand is required")
		},
	}
	cmd.AddCommand(newThumbsRegenerateCmd())
	return cmd
}

type regenerateOpts struct {
	cfgPath string
	all     bool
	ids     []string
	kind    string
	status  string
	since   string
}

// newThumbsRegenerateCmd wires the `fotobank thumbs regenerate` subcommand.
// It bumps thumb_version and resets thumb_status to 'pending' for every
// row matching the selector flags so the background worker re-processes
// them.
func newThumbsRegenerateCmd() *cobra.Command {
	var opts regenerateOpts
	cmd := &cobra.Command{
		Use:   "regenerate",
		Short: "Enqueue media rows for thumbnail regeneration",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runThumbsRegenerate(cmd.Context(), opts, cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	cmd.Flags().StringVar(&opts.cfgPath, "config", "",
		"path to config file (defaults to DefaultConfigPath)")
	cmd.Flags().BoolVar(&opts.all, "all", false,
		"regenerate every row for the caller")
	cmd.Flags().StringSliceVar(&opts.ids, "id", nil,
		"regenerate specific media IDs (repeatable)")
	cmd.Flags().StringVar(&opts.kind, "type", "",
		"filter: 'photo' or 'video'")
	cmd.Flags().StringVar(&opts.status, "status", "",
		"filter by current thumb_status (pending/working/ready/failed/no_preview)")
	cmd.Flags().StringVar(&opts.since, "since", "",
		"filter: imported_at >= RFC3339 date")
	return cmd
}

// loadThumbsConfig loads and validates the config for the thumbs command,
// enforcing the stub identity mode requirement.
func loadThumbsConfig(cfgPath string) (*config.Config, error) {
	path := cfgPath
	if path == "" {
		path = config.DefaultConfigPath()
	}
	cfg, err := config.Load(path)
	if err != nil {
		return nil, err
	}
	if cfg.Identity.Mode != "stub" {
		return nil, fmt.Errorf(
			"fotobank thumbs regenerate requires identity.mode = stub (got %q)",
			cfg.Identity.Mode)
	}
	return cfg, nil
}

// openDB opens the SQLite database for the thumbs command, preferring
// FOTOBANK_DB_PATH over the path derived from cfg.Flash.Root.
func openDB(cfg *config.Config) (*db.DB, error) {
	dbPath := os.Getenv("FOTOBANK_DB_PATH")
	if dbPath == "" {
		dbPath = filepath.Join(cfg.Flash.Root, "fotobank.sqlite")
	}
	return db.Open(dbPath)
}

// validateSelectors returns a usage error when no selector flag is set.
func validateSelectors(opts regenerateOpts) error {
	if !opts.all && len(opts.ids) == 0 && opts.kind == "" && opts.status == "" && opts.since == "" {
		return newUsageError(
			"at least one of --all, --id, --type, --status, --since is required")
	}
	return nil
}

// buildFilter constructs a thumb.EnqueueFilter from cfg and opts, including
// RFC3339 parsing for --since.
func buildFilter(cfg *config.Config, opts regenerateOpts) (thumb.EnqueueFilter, error) {
	filter := thumb.EnqueueFilter{
		Owner: owners.Principal{Hub: cfg.Identity.Stub.Hub, UserID: cfg.Identity.Stub.UserID},
		All:   opts.all,
		IDs:   opts.ids,
	}
	if opts.kind != "" {
		filter.MediaType = media.Type(opts.kind)
	}
	if opts.status != "" {
		filter.Status = opts.status
	}
	if opts.since != "" {
		ts, err := time.Parse(time.RFC3339, opts.since)
		if err != nil {
			return thumb.EnqueueFilter{}, newUsageError("--since must be RFC3339: %v", err)
		}
		filter.Since = &ts
	}
	return filter, nil
}

// runThumbsRegenerate loads the config, opens the DB, resolves the stub
// owner, and drives thumb.Queue.Enqueue. All selector validation —
// including RFC3339 parsing of --since — runs before opening the
// database so a misuse fails fast without touching the filesystem.
func runThumbsRegenerate(ctx context.Context, opts regenerateOpts, stdout, _ io.Writer) error {
	if err := validateSelectors(opts); err != nil {
		return err
	}
	cfg, err := loadThumbsConfig(opts.cfgPath)
	if err != nil {
		return err
	}
	filter, err := buildFilter(cfg, opts)
	if err != nil {
		return err
	}
	d, err := openDB(cfg)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()

	q := thumb.NewQueue(d.WriteDB(), d.ReadDB())
	n, err := q.Enqueue(ctx, filter)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%d rows enqueued for regeneration.\n", n)
	return nil
}
