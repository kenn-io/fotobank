package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/text/unicode/norm"

	"github.com/wesm/fotobank/internal/config"
	"github.com/wesm/fotobank/internal/db"
	"github.com/wesm/fotobank/internal/errs"
	"github.com/wesm/fotobank/internal/ingest"
	"github.com/wesm/fotobank/internal/media"
	"github.com/wesm/fotobank/internal/owners"
)

// newPairCmd wires the `fotobank pair` command group. Running `fotobank
// pair` without a subcommand prints usage and returns a usage error.
func newPairCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pair",
		Short: "Manage RAW + JPEG sidecar pairing",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			_ = cmd.Usage()
			return newUsageError("a subcommand is required")
		},
	}
	cmd.AddCommand(newPairBackfillCmd())
	return cmd
}

type pairBackfillOpts struct {
	cfgPath   string
	since     string     // raw flag string
	sinceTime *time.Time // parsed in validate; nil when --since unset
	mode      string     // currently only "full"
	owner     string
	allOwners bool
}

// newPairBackfillCmd wires the `fotobank pair backfill` subcommand. It
// re-runs the F2.2 RAW + JPEG pairing pass over rows scoped to the
// recent (--since) directories, recomputing against ALL non-empty
// import_source_path rows in those dirs so an older JPEG paired with a
// recent RAW (or vice versa) is discovered.
//
// Today only --mode=full is supported; the flag exists so future modes
// (e.g. fill-missing) drop in without a CLI break.
func newPairBackfillCmd() *cobra.Command {
	var opts pairBackfillOpts
	cmd := &cobra.Command{
		Use:   "backfill",
		Short: "Recompute RAW + JPEG pairings for existing media rows",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runPairBackfill(cmd.Context(), &opts, cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	cmd.Flags().StringVar(&opts.cfgPath, "config", "",
		"path to config file (defaults to DefaultConfigPath)")
	cmd.Flags().StringVar(&opts.since, "since", "",
		"only consider directories with rows imported within this duration (e.g. 24h, 168h, 30m)")
	cmd.Flags().StringVar(&opts.mode, "mode", "full",
		"full (default; only supported value today)")
	cmd.Flags().StringVar(&opts.owner, "owner", "",
		"admin: backfill for a single principal in <hub>:<user> form")
	cmd.Flags().BoolVar(&opts.allOwners, "all-owners", false,
		"admin: backfill for every registered principal")
	return cmd
}

// validatePairBackfillOpts parses --since, validates --mode, and rejects
// the --owner / --all-owners combination. Runs before the DB is opened
// so a misuse fails fast without creating a SQLite file.
func validatePairBackfillOpts(opts *pairBackfillOpts) error {
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
	if opts.mode != "full" {
		return newUsageError("--mode must be full (got %q)", opts.mode)
	}
	return nil
}

// pairTally counts the outcomes of a pair backfill run for the summary
// line printed at the end.
type pairTally struct {
	processed         int
	paired            int
	unchanged         int
	ambiguousUnpaired int
}

func (t pairTally) summary() string {
	return fmt.Sprintf(
		"pair backfill: processed=%d paired=%d unchanged=%d ambiguous_unpaired=%d",
		t.processed, t.paired, t.unchanged, t.ambiguousUnpaired,
	)
}

// runPairBackfill orchestrates the full backfill: validate flags, load
// config, open the DB, acquire the import file lock to serialize against
// in-flight imports, then iterate the configured principals.
func runPairBackfill(ctx context.Context, opts *pairBackfillOpts, stdout, _ io.Writer) error {
	if err := validatePairBackfillOpts(opts); err != nil {
		return err
	}
	requireStub := opts.owner == "" && !opts.allOwners
	cfg, err := loadPairConfig(opts.cfgPath, requireStub)
	if err != nil {
		return err
	}
	d, err := openDB(cfg)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()

	lockPath := cfg.Imports.FileLockPath
	if lockPath == "" {
		lockPath = filepath.Join(cfg.NAS.Root, ".fotobank", "import.lock")
	}
	unlock, err := ingest.Acquire(ctx, lockPath, 0)
	if err != nil {
		if errors.Is(err, errs.ErrConcurrentImport) {
			return fmt.Errorf("another import is in progress (lock: %s)", lockPath)
		}
		return err
	}
	defer unlock()

	principals, err := selectPairPrincipals(ctx, d, cfg, opts)
	if err != nil {
		return err
	}
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	tally := &pairTally{}
	for _, p := range principals {
		if err := pairBackfillFor(ctx, repo, p, opts, tally); err != nil {
			return err
		}
	}
	fmt.Fprintln(stdout, tally.summary())
	return nil
}

// loadPairConfig mirrors loadGPSConfig: requireStub enforces
// identity.mode=stub when no admin scope flag is set.
func loadPairConfig(cfgPath string, requireStub bool) (*config.Config, error) {
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
			"fotobank pair backfill requires identity.mode = stub (got %q)",
			cfg.Identity.Mode)
	}
	return cfg, nil
}

// selectPairPrincipals resolves the principals to back fill: an explicit
// --owner, every owner in the DB for --all-owners, or the stub
// principal from the config when neither is set.
func selectPairPrincipals(
	ctx context.Context,
	d *db.DB,
	cfg *config.Config,
	opts *pairBackfillOpts,
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

// pairBackfillFor runs a single owner's pairing pass. It lists every
// row for owner with a non-empty import_source_path (IncludeSidecars=true
// is required because the very rows we want to fix are sidecars), narrows
// to the directories whose rows pass the --since filter, and re-runs
// ingest.Compute over the union. The resulting PairUpdate slice is
// applied via UpdatePairedWithID and tallied.
func pairBackfillFor(
	ctx context.Context,
	repo *media.Repo,
	owner owners.Principal,
	opts *pairBackfillOpts,
	tally *pairTally,
) error {
	rows, err := listAllForOwner(ctx, repo, owner)
	if err != nil {
		return err
	}

	candidates, processed := buildPairCandidates(rows, opts.sinceTime)
	tally.processed += processed
	updates := ingest.Compute(candidates)
	for _, u := range updates {
		if err := repo.UpdatePairedWithID(ctx, u.ID, u.PairedWithID); err != nil {
			return fmt.Errorf("apply pair update for %s: %w", u.ID, err)
		}
		switch {
		case u.PairedWithID != nil:
			tally.paired++
		default:
			tally.ambiguousUnpaired++
		}
	}
	tally.unchanged += processed - len(updates)
	return nil
}

// listAllForOwner pages through every row for owner via repo.List.
// media.Repo.ListAll exists but is internal-shaped (defaultListLimit
// constant); we replicate its loop here so this command does not depend
// on a private constant. IncludeSidecars=true is required: the F2.2
// pairing pass must see sidecars to recompute their paired_with_id.
func listAllForOwner(
	ctx context.Context,
	repo *media.Repo,
	owner owners.Principal,
) ([]media.Media, error) {
	const pageSize = 1000
	var out []media.Media
	offset := 0
	for {
		page, err := repo.List(ctx, media.ListFilter{
			Owner:           owner,
			Limit:           pageSize,
			Offset:          offset,
			IncludeSidecars: true,
		})
		if err != nil {
			return nil, fmt.Errorf("list media for %s:%s: %w",
				owner.Hub, owner.UserID, err)
		}
		out = append(out, page...)
		if len(page) < pageSize {
			return out, nil
		}
		offset += pageSize
	}
}

// buildPairCandidates narrows rows to the candidate set Compute should
// see. Rows with empty import_source_path are skipped (Compute would
// skip them anyway, but pre-filtering keeps the candidate slice small
// and the processed tally honest). When --since is set, we first build
// the set of NFC-normalized directories that contain at least one row
// imported on/after sinceTime, then admit every row whose dir lives in
// that set — so an older JPEG sitting in a directory that received a
// recent RAW (and vice versa) is still considered.
func buildPairCandidates(rows []media.Media, sinceTime *time.Time) ([]ingest.PairCandidate, int) {
	dirSet := pairBackfillDirSet(rows, sinceTime)
	candidates := make([]ingest.PairCandidate, 0, len(rows))
	processed := 0
	for _, m := range rows {
		if m.ImportSourcePath == "" {
			continue
		}
		dir := norm.NFC.String(filepath.Dir(m.ImportSourcePath))
		if dirSet != nil {
			if _, ok := dirSet[dir]; !ok {
				continue
			}
		}
		processed++
		candidates = append(candidates, ingest.PairCandidate{
			ID:                m.ID,
			ImportSourcePath:  m.ImportSourcePath,
			Class:             ingest.PairClassFromMime(m.MimeType),
			MimeType:          m.MimeType,
			CurrentPairedWith: m.PairedWithID,
		})
	}
	return candidates, processed
}

// pairBackfillDirSet returns the NFC-normalized set of directories the
// caller should consider, or nil for "all directories" (no --since).
// The set membership rule: a directory is included if it contains at
// least one row imported at or after sinceTime. Rows with empty
// import_source_path do not contribute a directory.
func pairBackfillDirSet(rows []media.Media, sinceTime *time.Time) map[string]struct{} {
	if sinceTime == nil {
		return nil
	}
	out := make(map[string]struct{})
	for _, m := range rows {
		if m.ImportSourcePath == "" {
			continue
		}
		if m.ImportedAt.Before(*sinceTime) {
			continue
		}
		out[norm.NFC.String(filepath.Dir(m.ImportSourcePath))] = struct{}{}
	}
	return out
}
