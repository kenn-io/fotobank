package cli

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"go.kenn.io/fotobank/internal/client"
	"go.kenn.io/fotobank/internal/config"
	"go.kenn.io/fotobank/internal/httpapi"
	"go.kenn.io/fotobank/internal/version"
)

// newGPSCmd wires the `fotobank gps` command group. Running `fotobank
// gps` without a subcommand prints usage and returns a usage error.
func newGPSCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "gps",
		Short: "Manage GPS metadata",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			_ = cmd.Usage()
			return newUsageError("a subcommand is required")
		},
	}
	cmd.AddCommand(newGPSBackfillCmd())
	return cmd
}

type gpsBackfillOpts struct {
	cfgPath   string
	since     string     // raw flag string
	sinceTime *time.Time // parsed in validate; nil when --since unset
	mode      string     // "full", "fill-missing", "relabel"
	asJSON    bool
	owner     string
	allOwners bool
}

// newGPSBackfillCmd wires the `fotobank gps backfill` subcommand. It
// re-extracts EXIF GPS or re-resolves location labels for existing
// media rows. Three modes:
//   - full: re-extract EXIF and resolve labels for every photo row
//     (clears GPS if the file no longer has GPS in EXIF).
//   - fill-missing: only touch rows where both lat and lon are NULL.
//   - relabel: re-resolve labels for rows that already have coords;
//     never re-reads EXIF.
//
// Videos are skipped in all modes because video GPS extraction is unsupported.
func newGPSBackfillCmd() *cobra.Command {
	var opts gpsBackfillOpts
	cmd := &cobra.Command{
		Use:   "backfill",
		Short: "Extract or refresh GPS metadata for existing media rows",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runGPSBackfill(cmd.Context(), &opts, cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	cmd.Flags().StringVar(&opts.cfgPath, "config", "",
		"path to config file (defaults to DefaultConfigPath)")
	cmd.Flags().StringVar(&opts.since, "since", "",
		"only rows imported within this duration (e.g. 24h, 168h, 30m)")
	cmd.Flags().StringVar(&opts.mode, "mode", "full",
		"full | fill-missing | relabel (default full)")
	cmd.Flags().StringVar(&opts.owner, "owner", "",
		"admin: backfill for a single principal in <hub>:<user> form")
	cmd.Flags().BoolVar(&opts.allOwners, "all-owners", false,
		"admin: backfill for every registered principal")
	cmd.Flags().BoolVar(&opts.asJSON, "json", false, "emit counts and per-photo failures as JSON")
	return cmd
}

// validateGPSBackfillOpts parses --since, validates --mode, and rejects
// the --owner / --all-owners combination before automatic daemon startup.
func validateGPSBackfillOpts(opts *gpsBackfillOpts) error {
	if opts.owner != "" && opts.allOwners {
		return newUsageError("--owner and --all-owners are mutually exclusive")
	}
	if opts.owner != "" {
		if _, err := parseOwner(opts.owner); err != nil {
			return err
		}
	}
	if opts.since != "" {
		d, err := time.ParseDuration(opts.since)
		if err != nil {
			return newUsageError("invalid --since %q: %v", opts.since, err)
		}
		if d <= 0 {
			return newUsageError("--since must be positive (got %s)", opts.since)
		}
		t := time.Now().UTC().Add(-d)
		opts.sinceTime = &t
	}
	switch opts.mode {
	case "full", "fill-missing", "relabel":
	default:
		return newUsageError("--mode must be one of full|fill-missing|relabel")
	}
	return nil
}

// runGPSBackfill validates before automatic startup, then calls the host API.
func runGPSBackfill(ctx context.Context, opts *gpsBackfillOpts, stdout, stderr io.Writer) error {
	result := httpapi.GPSBackfillResult{}
	err := validateGPSBackfillOpts(opts)
	if err == nil {
		var lifecycle client.Lifecycle
		lifecycle, err = daemonLifecycle(opts.cfgPath, "")
		if err == nil {
			var cfg *config.Config
			cfg, err = config.LoadUnchecked(lifecycle.ConfigPath)
			if err == nil && opts.owner == "" && !opts.allOwners && cfg.Identity.Mode != "stub" {
				err = newUsageError("fotobank gps backfill requires identity.mode = stub without --owner or --all-owners")
			}
		}
		if err == nil {
			_, err = lifecycle.Ensure(ctx)
		}
		if err == nil {
			result, err = client.BackfillGPS(ctx, lifecycle.DBPath, version.Short, httpapi.GPSBackfillRequest{
				Mode: opts.mode, Since: opts.sinceTime, Owner: opts.owner, AllOwners: opts.allOwners,
			})
		}
	}
	if err != nil {
		result.Error = err.Error()
	}
	if opts.asJSON {
		return errors.Join(err, json.MarshalWrite(stdout, result))
	}
	fmt.Fprintf(stdout, "gps backfill: processed=%d updated=%d unchanged=%d failed=%d\n", result.Processed, result.Updated, result.Unchanged, result.Failed)
	for _, failure := range result.Failures {
		fmt.Fprintf(stderr, "gps backfill: owner %s row %s: %s\n", failure.Owner, failure.ID, failure.Error)
	}
	return err
}
