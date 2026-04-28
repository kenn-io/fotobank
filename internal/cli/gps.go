package cli

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/wesm/fotobank/internal/config"
	"github.com/wesm/fotobank/internal/db"
	"github.com/wesm/fotobank/internal/errs"
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

// parseBackfillMode maps the raw --mode flag string to the typed
// media.GPSBackfillMode constant. Returns a usage error for any value
// outside the closed set so a typo fails fast at flag-parse time.
func parseBackfillMode(s string) (media.GPSBackfillMode, error) {
	switch s {
	case "full":
		return media.GPSBackfillModeFull, nil
	case "fill-missing":
		return media.GPSBackfillModeFillMissing, nil
	case "relabel":
		return media.GPSBackfillModeRelabel, nil
	default:
		return 0, newUsageError(
			"--mode must be one of full|fill-missing|relabel (got %q)", s)
	}
}

// validateGPSBackfillOpts parses --since, validates --mode, and rejects
// the --owner / --all-owners combination. Runs before the DB is opened
// so a misuse fails fast without creating a SQLite file.
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
	mode, err := parseBackfillMode(opts.mode)
	if err != nil {
		return err
	}
	opts.parsedMode = mode
	return nil
}

// runGPSBackfill orchestrates the full backfill: validate flags, load
// config, open the gazetteer + DB, then iterate the configured
// principals invoking backfiller.runFor for each.
func runGPSBackfill(ctx context.Context, opts *gpsBackfillOpts, stdout, stderr io.Writer) error {
	if err := validateGPSBackfillOpts(opts); err != nil {
		return err
	}
	requireStub := opts.owner == "" && !opts.allOwners
	cfg, err := loadGPSConfig(opts.cfgPath, requireStub)
	if err != nil {
		return err
	}
	d, err := openDB(cfg)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()

	b, err := newBackfiller(ctx, d, cfg, opts, stderr)
	if err != nil {
		return err
	}
	principals, err := selectPrincipals(ctx, d, cfg, opts)
	if err != nil {
		return err
	}
	for _, p := range principals {
		if err := b.runFor(ctx, p); err != nil {
			return err
		}
	}
	fmt.Fprintln(stdout, b.tally.summary())
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
// principal from the config when neither is set. --owner has already
// been parse-validated in validateGPSBackfillOpts; the parseOwner call
// here cannot fail in practice but we propagate the error to keep the
// call site honest.
func selectPrincipals(
	ctx context.Context,
	d *db.DB,
	cfg *config.Config,
	opts *gpsBackfillOpts,
) ([]owners.Principal, error) {
	switch {
	case opts.owner != "":
		p, err := parseOwner(opts.owner)
		if err != nil {
			return nil, err
		}
		return []owners.Principal{p}, nil
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

var backfillBatch = 500

// SetBackfillBatchForTest swaps backfillBatch for the duration of a
// test and returns the previous value. Tests must restore it via the
// returned value in a t.Cleanup. Used only by tests; not part of the
// public CLI surface.
func SetBackfillBatchForTest(n int) int {
	prev := backfillBatch
	backfillBatch = n
	return prev
}

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

// backfiller bundles the cross-cutting deps a single backfill run uses
// across all principals. Constructed once in runGPSBackfill from the
// already-opened DB and reused per principal — keeps mode-specific
// helpers under the 5-positional-param house limit.
type backfiller struct {
	svc    *service.MediaService
	repo   *media.Repo
	store  storage.Store
	places *geo.NaturalEarth
	mode   media.GPSBackfillMode
	since  *time.Time
	tally  *backfillTally
	stderr io.Writer
}

// newBackfiller wires the service / repo / store / gazetteer onto a
// single struct. selectPrincipals stays at the runGPSBackfill layer so
// this constructor doesn't need the principal list. Loading the
// gazetteer here keeps runGPSBackfill below the cyclomatic limit.
func newBackfiller(
	ctx context.Context,
	d *db.DB,
	cfg *config.Config,
	opts *gpsBackfillOpts,
	stderr io.Writer,
) (*backfiller, error) {
	places, err := geo.NewNaturalEarth()
	if err != nil {
		return nil, fmt.Errorf("load gazetteer: %w", err)
	}
	ownerSvc := service.NewOwnerService(owners.NewRepo(d.WriteDB(), d.ReadDB()))
	keys, err := loadStorageKeys(ctx, ownerSvc)
	if err != nil {
		return nil, err
	}
	storeLayer, _ := buildStorageLayer(cfg, keys)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	svc := service.NewMediaService(repo, storeLayer)
	return &backfiller{
		svc:    svc,
		repo:   repo,
		store:  storeLayer,
		places: places,
		mode:   opts.parsedMode,
		since:  opts.sinceTime,
		tally:  &backfillTally{},
		stderr: stderr,
	}, nil
}

// runFor pages through the candidate set for owner using keyset
// pagination. Always advance the cursor past the last seen ID. For
// Full and Relabel this is required because rows stay in the candidate
// set after being processed; for FillMissing it's required because
// rows that fail to gain GPS (no EXIF segment, IO error) also stay
// candidates and would otherwise loop the fetched page indefinitely.
func (b *backfiller) runFor(ctx context.Context, owner owners.Principal) error {
	afterID := ""
	for {
		page, err := b.repo.ListGPSBackfillCandidates(
			ctx, owner, b.mode, b.since, afterID, backfillBatch)
		if err != nil {
			return fmt.Errorf("list gps candidates: %w", err)
		}
		if len(page) == 0 {
			return nil
		}
		for _, row := range page {
			if err := backfillOne(ctx, b, owner, row); err != nil {
				// Per-row failures are tallied but don't abort the run.
				fmt.Fprintf(b.stderr, "gps backfill: row %s: %v\n", row.ID, err)
				b.tally.failed++
			}
			b.tally.processed++
			if b.tally.processed%100 == 0 {
				fmt.Fprintln(b.stderr, b.tally.summary())
			}
		}
		afterID = page[len(page)-1].ID
		if len(page) < backfillBatch {
			return nil
		}
	}
}

// backfillOne dispatches to the mode-specific helper. The default arm
// is unreachable in practice — parseBackfillMode rejects unknown values
// upstream — but the wrapped sentinel keeps the contract explicit.
func backfillOne(
	ctx context.Context,
	b *backfiller,
	owner owners.Principal,
	row media.Media,
) error {
	switch b.mode {
	case media.GPSBackfillModeRelabel:
		return relabelOne(ctx, b, owner, row)
	case media.GPSBackfillModeFull, media.GPSBackfillModeFillMissing:
		return reextractOne(ctx, b, owner, row)
	default:
		return fmt.Errorf("%w: gps backfill mode %d", errs.ErrInvalidArgument, b.mode)
	}
}

// relabelOne re-resolves the gazetteer label for a row that already has
// coords, without touching EXIF. Rows missing either coordinate are
// counted as unchanged: the relabel candidate query already filters
// them out, but the guard keeps this helper safe in isolation.
func relabelOne(ctx context.Context, b *backfiller, owner owners.Principal, row media.Media) error {
	if row.Latitude == nil || row.Longitude == nil {
		b.tally.unchanged++
		return nil
	}
	label := ""
	if l, ok := b.places.Resolve(*row.Latitude, *row.Longitude); ok {
		label = l
	}
	if label == row.LocationLabel {
		b.tally.unchanged++
		return nil
	}
	if err := b.svc.UpdateGPS(
		ctx, owner, row.ID, row.Latitude, row.Longitude, row.GPSAt, label,
	); err != nil {
		return fmt.Errorf("update gps for row %s: %w", row.ID, err)
	}
	b.tally.updated++
	return nil
}

// reextractOne handles the Full and FillMissing modes: re-read NAS
// bytes through exifread and reconcile the result with the row. Full is
// authoritative — when EXIF has no GPS it clears any existing coords;
// FillMissing leaves rows alone when EXIF has no GPS.
func reextractOne(
	ctx context.Context,
	b *backfiller,
	owner owners.Principal,
	row media.Media,
) error {
	rc, err := b.store.ReadRange(ctx, owner, row.Path, 0, -1)
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
	if meta.Latitude == nil || meta.Longitude == nil {
		return reextractMissing(ctx, b, owner, row)
	}
	label := ""
	if l, ok := b.places.Resolve(*meta.Latitude, *meta.Longitude); ok {
		label = l
	}
	if err := b.svc.UpdateGPS(
		ctx, owner, row.ID, meta.Latitude, meta.Longitude, meta.GPSAt, label,
	); err != nil {
		return fmt.Errorf("update gps for row %s: %w", row.ID, err)
	}
	b.tally.updated++
	return nil
}

// reextractMissing is the EXIF-has-no-GPS branch of reextractOne. Full
// clears any existing coords; FillMissing leaves the row alone. Pulled
// out to keep reextractOne under the cyclomatic limit.
func reextractMissing(
	ctx context.Context,
	b *backfiller,
	owner owners.Principal,
	row media.Media,
) error {
	if b.mode == media.GPSBackfillModeFillMissing {
		b.tally.unchanged++
		return nil
	}
	// Full is authoritative: clear if EXIF has no GPS.
	if row.Latitude == nil && row.Longitude == nil &&
		row.GPSAt == nil && row.LocationLabel == "" {
		b.tally.unchanged++
		return nil
	}
	if err := b.svc.UpdateGPS(ctx, owner, row.ID, nil, nil, nil, ""); err != nil {
		return fmt.Errorf("update gps for row %s: %w", row.ID, err)
	}
	b.tally.updated++
	return nil
}
