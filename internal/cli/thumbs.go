package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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
	cfgPath   string
	all       bool
	ids       []string
	kind      string
	status    string
	since     string     // raw RFC3339 string from the flag
	sinceTime *time.Time // parsed during validateSelectors; nil when --since is empty
	owner     string     // "<hub>:<user>"; empty = use stub principal
	allOwners bool       // iterate every principal in owners table
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
	cmd.Flags().StringVar(&opts.owner, "owner", "",
		"admin: regenerate for a single principal in <hub>:<user> form")
	cmd.Flags().BoolVar(&opts.allOwners, "all-owners", false,
		"admin: regenerate for every registered principal")
	return cmd
}

// loadThumbsConfig loads and validates the config for the thumbs command.
// requireStub enforces identity.mode = "stub"; admin scope flags
// (--owner / --all-owners) bypass that check.
func loadThumbsConfig(cfgPath string, requireStub bool) (*config.Config, error) {
	path := cfgPath
	if path == "" {
		path = config.DefaultConfigPath()
	}
	cfg, err := config.Load(path)
	if err != nil {
		return nil, err
	}
	if requireStub && cfg.Identity.Mode != "stub" {
		return nil, newUsageError(
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
// It also parses --since up front so a malformed timestamp errors before
// any DB file is created.
func validateSelectors(opts *regenerateOpts) error {
	if !opts.all && len(opts.ids) == 0 && opts.kind == "" && opts.status == "" && opts.since == "" {
		return newUsageError(
			"at least one of --all, --id, --type, --status, --since is required")
	}
	if opts.since != "" {
		ts, err := time.Parse(time.RFC3339, opts.since)
		if err != nil {
			return newUsageError("--since must be RFC3339: %v", err)
		}
		opts.sinceTime = &ts
	}
	return nil
}

// validateScope checks the owner-scope flags are consistent: --owner and
// --all-owners are mutually exclusive, and --owner must parse as
// <hub>:<user>.
func validateScope(opts regenerateOpts) error {
	if opts.owner != "" && opts.allOwners {
		return newUsageError(
			"--owner and --all-owners are mutually exclusive")
	}
	if opts.owner != "" {
		if _, err := parseOwner(opts.owner); err != nil {
			return err
		}
	}
	return nil
}

// parseOwner parses an "<hub>:<user>" string into a Principal.
func parseOwner(s string) (owners.Principal, error) {
	hub, user, ok := strings.Cut(s, ":")
	if !ok || hub == "" || user == "" {
		return owners.Principal{}, newUsageError(
			"owner must be hub:user (got %q)", s)
	}
	return owners.Principal{Hub: hub, UserID: user}, nil
}

// buildFilter constructs a thumb.EnqueueFilter from a Principal and opts.
// --since parsing happened in validateSelectors, so this function is
// infallible.
func buildFilter(p owners.Principal, opts regenerateOpts) thumb.EnqueueFilter {
	filter := thumb.EnqueueFilter{
		Owner: p,
		All:   opts.all,
		IDs:   opts.ids,
	}
	if opts.kind != "" {
		filter.MediaType = media.Type(opts.kind)
	}
	if opts.status != "" {
		filter.Status = opts.status
	}
	if opts.sinceTime != nil {
		filter.Since = opts.sinceTime
	}
	return filter
}

// resolveOwners returns the principals to iterate over: every owner in
// the DB for --all-owners, the parsed --owner principal, or the stub
// principal from the config.
func resolveOwners(ctx context.Context, d *db.DB, cfg *config.Config, opts regenerateOpts) ([]owners.Principal, error) {
	if opts.allOwners {
		repo := owners.NewRepo(d.WriteDB(), d.ReadDB())
		list, err := repo.List(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]owners.Principal, 0, len(list))
		for _, o := range list {
			out = append(out, o.Principal)
		}
		return out, nil
	}
	if opts.owner != "" {
		p, err := parseOwner(opts.owner)
		if err != nil {
			return nil, err
		}
		return []owners.Principal{p}, nil
	}
	return []owners.Principal{
		{Hub: cfg.Identity.Stub.Hub, UserID: cfg.Identity.Stub.UserID},
	}, nil
}

// runThumbsRegenerate validates selectors and scope, opens the DB,
// resolves the principals to enqueue for, and drives thumb.Queue.Enqueue
// once per principal. All selector validation — including RFC3339
// parsing of --since — runs before opening the database so a misuse
// fails fast without touching the filesystem.
func runThumbsRegenerate(ctx context.Context, opts regenerateOpts, stdout, _ io.Writer) error {
	if err := validateSelectors(&opts); err != nil {
		return err
	}
	if err := validateScope(opts); err != nil {
		return err
	}
	requireStub := opts.owner == "" && !opts.allOwners
	cfg, err := loadThumbsConfig(opts.cfgPath, requireStub)
	if err != nil {
		return err
	}
	d, err := openDB(cfg)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()

	scope, err := resolveOwners(ctx, d, cfg, opts)
	if err != nil {
		return err
	}
	if len(scope) == 0 {
		fmt.Fprintln(stdout, "no owners found")
		return nil
	}

	q := thumb.NewQueue(d.WriteDB(), d.ReadDB())
	for _, p := range scope {
		filter := buildFilter(p, opts)
		n, err := q.Enqueue(ctx, filter)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%d rows enqueued for %s:%s.\n", n, p.Hub, p.UserID)
	}
	return nil
}
