package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/wesm/fotobank/internal/config"
	"github.com/wesm/fotobank/internal/db"
	"github.com/wesm/fotobank/internal/exifread"
	"github.com/wesm/fotobank/internal/geo"
	"github.com/wesm/fotobank/internal/media"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/service"
	"github.com/wesm/fotobank/internal/storage"
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
	cfgPath    string
	since      string     // raw flag string
	sinceTime  *time.Time // parsed in validate; nil when --since unset
	mode       string     // "full", "fill-missing", "relabel"
	parsedMode media.GPSBackfillMode
	owner      string
	allOwners  bool
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
// Videos are skipped in all modes (per spec §5.5).
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
	return cmd
}

// validateGPSBackfillOpts parses --since, validates --mode, and rejects
// the --owner / --all-owners combination. Runs before the DB is opened
// so a misuse fails fast without creating a SQLite file.
func validateGPSBackfillOpts(opts *gpsBackfillOpts) error {
	if opts.owner != "" && opts.allOwners {
		return newUsageError("--owner and --all-owners are mutually exclusive")
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
	case "full":
		opts.parsedMode = media.GPSBackfillModeFull
	case "fill-missing":
		opts.parsedMode = media.GPSBackfillModeFillMissing
	case "relabel":
		opts.parsedMode = media.GPSBackfillModeRelabel
	default:
		return newUsageError("--mode must be one of full|fill-missing|relabel (got %q)", opts.mode)
	}
	return nil
}

// runGPSBackfill orchestrates the full backfill: validate flags, load
// config, open the gazetteer + DB, then iterate the configured
// principals invoking backfillForPrincipal for each.
func runGPSBackfill(ctx context.Context, opts *gpsBackfillOpts, stdout, stderr io.Writer) error {
	if err := validateGPSBackfillOpts(opts); err != nil {
		return err
	}
	requireStub := opts.owner == "" && !opts.allOwners
	cfg, err := loadGPSConfig(opts.cfgPath, requireStub)
	if err != nil {
		return err
	}

	places, err := geo.NewNaturalEarth()
	if err != nil {
		return fmt.Errorf("load gazetteer: %w", err)
	}

	d, err := openDB(cfg)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()

	ownerSvc := service.NewOwnerService(owners.NewRepo(d.WriteDB(), d.ReadDB()))
	keys, err := loadStorageKeys(ctx, ownerSvc)
	if err != nil {
		return err
	}
	storeLayer, _ := buildStorageLayer(cfg, keys)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	svc := service.NewMediaService(repo, storeLayer)

	principals, err := selectPrincipals(ctx, d, cfg, opts)
	if err != nil {
		return err
	}

	tally := backfillTally{}
	for _, p := range principals {
		if err := backfillForPrincipal(ctx, p, opts, repo, svc, storeLayer, places, stderr, &tally); err != nil {
			return err
		}
	}
	fmt.Fprintln(stdout, tally.summary())
	return nil
}

// loadGPSConfig mirrors loadThumbsConfig: requireStub enforces
// identity.mode=stub when no admin scope flag is set.
func loadGPSConfig(cfgPath string, requireStub bool) (*config.Config, error) {
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
			"fotobank gps backfill requires identity.mode = stub (got %q)",
			cfg.Identity.Mode)
	}
	return cfg, nil
}

// selectPrincipals resolves the principals to back fill: an explicit
// --owner, every owner in the DB for --all-owners, or the stub
// principal from the config when neither is set.
func selectPrincipals(ctx context.Context, d *db.DB, cfg *config.Config, opts *gpsBackfillOpts) ([]owners.Principal, error) {
	switch {
	case opts.owner != "":
		hub, user, ok := strings.Cut(opts.owner, ":")
		if !ok || hub == "" || user == "" {
			return nil, newUsageError("--owner must be <hub>:<user> (got %q)", opts.owner)
		}
		return []owners.Principal{{Hub: hub, UserID: user}}, nil
	case opts.allOwners:
		ownersRepo := owners.NewRepo(d.WriteDB(), d.ReadDB())
		list, err := ownersRepo.List(ctx)
		if err != nil {
			return nil, fmt.Errorf("list owners: %w", err)
		}
		out := make([]owners.Principal, 0, len(list))
		for _, o := range list {
			out = append(out, o.Principal)
		}
		return out, nil
	default:
		return []owners.Principal{
			{Hub: cfg.Identity.Stub.Hub, UserID: cfg.Identity.Stub.UserID},
		}, nil
	}
}

const backfillBatch = 500

type backfillTally struct {
	processed int
	updated   int
	unchanged int
	failed    int
}

func (t backfillTally) summary() string {
	return fmt.Sprintf("gps backfill: processed=%d updated=%d unchanged=%d failed=%d",
		t.processed, t.updated, t.unchanged, t.failed)
}

// backfillForPrincipal pages through the candidate set for owner using
// keyset pagination. FillMissing rows leave the candidate set after
// being updated, so the cursor restarts at "" each iteration; Full and
// Relabel rows stay in the set, so the cursor must advance past the
// last seen ID to make progress.
func backfillForPrincipal(
	ctx context.Context,
	owner owners.Principal,
	opts *gpsBackfillOpts,
	repo *media.Repo,
	svc *service.MediaService,
	store storage.Store,
	places *geo.NaturalEarth,
	stderr io.Writer,
	tally *backfillTally,
) error {
	afterID := ""
	for {
		page, err := repo.ListGPSBackfillCandidates(ctx, owner, opts.parsedMode, opts.sinceTime, afterID, backfillBatch)
		if err != nil {
			return fmt.Errorf("list gps candidates: %w", err)
		}
		if len(page) == 0 {
			return nil
		}
		for _, row := range page {
			if err := backfillOne(ctx, owner, row, opts.parsedMode, svc, store, places, tally); err != nil {
				// Per-row failures are tallied but don't abort the run.
				fmt.Fprintf(stderr, "gps backfill: row %s: %v\n", row.ID, err)
				tally.failed++
			}
			tally.processed++
			if tally.processed%100 == 0 {
				fmt.Fprintln(stderr, tally.summary())
			}
		}
		if opts.parsedMode == media.GPSBackfillModeFillMissing {
			afterID = ""
		} else {
			afterID = page[len(page)-1].ID
		}
		if len(page) < backfillBatch {
			return nil
		}
	}
}

// backfillOne handles a single row according to mode. Relabel re-runs
// the gazetteer against existing coords; Full and FillMissing re-read
// the NAS bytes through exifread. Full is authoritative — when EXIF has
// no GPS it clears any existing coords; FillMissing leaves rows alone
// when EXIF has no GPS.
func backfillOne(
	ctx context.Context,
	owner owners.Principal,
	row media.Media,
	mode media.GPSBackfillMode,
	svc *service.MediaService,
	store storage.Store,
	places *geo.NaturalEarth,
	tally *backfillTally,
) error {
	switch mode {
	case media.GPSBackfillModeRelabel:
		if row.Latitude == nil || row.Longitude == nil {
			tally.unchanged++
			return nil
		}
		label := ""
		if l, ok := places.Resolve(*row.Latitude, *row.Longitude); ok {
			label = l
		}
		if label == row.LocationLabel {
			tally.unchanged++
			return nil
		}
		if err := svc.UpdateGPS(ctx, owner, row.ID, row.Latitude, row.Longitude, row.GPSAt, label); err != nil {
			return err
		}
		tally.updated++
		return nil

	case media.GPSBackfillModeFull, media.GPSBackfillModeFillMissing:
		rc, err := store.ReadRange(ctx, owner, row.Path, 0, -1)
		if err != nil {
			return fmt.Errorf("read NAS bytes: %w", err)
		}
		defer func() { _ = rc.Close() }()
		meta, err := exifread.ExtractPhotoFromReader(rc)
		if err != nil {
			return fmt.Errorf("extract exif: %w", err)
		}
		// ExtractPhotoFromReader returns Metadata{} (no error) when the
		// file simply has no EXIF segment. Such rows naturally fall
		// through hasGPS=false below — no separate "skipped" counter.

		hasGPS := meta.Latitude != nil && meta.Longitude != nil
		if !hasGPS {
			if mode == media.GPSBackfillModeFillMissing {
				tally.unchanged++
				return nil
			}
			// full is authoritative: clear if EXIF has no GPS.
			if row.Latitude == nil && row.Longitude == nil && row.GPSAt == nil && row.LocationLabel == "" {
				tally.unchanged++
				return nil
			}
			if err := svc.UpdateGPS(ctx, owner, row.ID, nil, nil, nil, ""); err != nil {
				return err
			}
			tally.updated++
			return nil
		}

		label := ""
		if l, ok := places.Resolve(*meta.Latitude, *meta.Longitude); ok {
			label = l
		}
		if err := svc.UpdateGPS(ctx, owner, row.ID, meta.Latitude, meta.Longitude, meta.GPSAt, label); err != nil {
			return err
		}
		tally.updated++
		return nil

	default:
		return fmt.Errorf("unknown mode: %d", mode)
	}
}
