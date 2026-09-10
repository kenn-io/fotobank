package cli

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"uuid"

	"github.com/spf13/cobra"

	"go.kenn.io/fotobank/internal/client"
	"go.kenn.io/fotobank/internal/config"
	"go.kenn.io/fotobank/internal/httpapi"
	"go.kenn.io/fotobank/internal/owners"
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
	asJSON    bool
	parsedIDs []uuid.UUID
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
	cmd.Flags().BoolVar(&opts.asJSON, "json", false, "emit per-owner queued counts as JSON")
	return cmd
}

// validateSelectors returns a usage error when no selector flag is set.
// It also parses --since up front so a malformed timestamp errors before
// any DB file is created.
func validateSelectors(opts *regenerateOpts) error {
	switch opts.kind {
	case "", "photo", "video":
	default:
		return newUsageError("--type must be photo or video")
	}
	switch opts.status {
	case "", "pending", "working", "ready", "failed", "no_preview":
	default:
		return newUsageError("invalid --status")
	}
	for _, raw := range opts.ids {
		id, err := uuid.Parse(raw)
		if err != nil {
			return newUsageError("--id must be a UUID: %v", err)
		}
		opts.parsedIDs = append(opts.parsedIDs, id)
	}
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

// runThumbsRegenerate validates before automatic startup and queues through HTTP.
func runThumbsRegenerate(ctx context.Context, opts regenerateOpts, stdout, _ io.Writer) error {
	if err := validateSelectors(&opts); err != nil {
		return err
	}
	if err := validateScope(opts); err != nil {
		return err
	}
	lifecycle, err := daemonLifecycle(opts.cfgPath, "")
	if err != nil {
		return err
	}
	cfg, err := config.LoadUnchecked(lifecycle.ConfigPath)
	if err != nil {
		return err
	}
	if opts.owner == "" && !opts.allOwners && cfg.Identity.Mode != "stub" {
		return newUsageError("fotobank thumbs regenerate requires identity.mode = stub without --owner or --all-owners")
	}
	if _, err := lifecycle.Ensure(ctx); err != nil {
		return err
	}
	result, err := client.RegenerateThumbs(ctx, lifecycle.ConfigPath, lifecycle.Version, httpapi.RegenerateThumbsRequest{
		All: opts.all, IDs: opts.parsedIDs, Type: opts.kind, Status: opts.status, Since: opts.sinceTime,
		Owner: opts.owner, AllOwners: opts.allOwners,
	})
	if err != nil {
		return err
	}
	if result.Error != "" {
		err = errors.New(result.Error)
	}
	if opts.asJSON {
		return errors.Join(err, json.MarshalWrite(stdout, result))
	}
	if len(result.Items) == 0 && err == nil {
		fmt.Fprintln(stdout, "no owners found")
	}
	for _, item := range result.Items {
		fmt.Fprintf(stdout, "%d rows enqueued for %s:%s.\n", item.Enqueued, item.Hub, item.UserID)
	}
	return err
}
