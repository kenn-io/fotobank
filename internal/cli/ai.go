package cli

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"go.kenn.io/fotobank/internal/ai"
	"go.kenn.io/fotobank/internal/ai/ack"
	"go.kenn.io/fotobank/internal/ai/embedding"
	"go.kenn.io/fotobank/internal/ai/failures"
	"go.kenn.io/fotobank/internal/ai/gapscanner"
	"go.kenn.io/fotobank/internal/ai/jobs"
	"go.kenn.io/fotobank/internal/ai/results"
	airuntime "go.kenn.io/fotobank/internal/ai/runtime"
	"go.kenn.io/fotobank/internal/ai/skipped"
	appsettingsstore "go.kenn.io/fotobank/internal/appsettings"
	"go.kenn.io/fotobank/internal/client"
	"go.kenn.io/fotobank/internal/config"
	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/httpapi"
	"go.kenn.io/fotobank/internal/owners"
	aiservice "go.kenn.io/fotobank/internal/service/ai"
)

// aiCtx serves the AI commands still awaiting migration to the daemon:
// backfill, retry, and embedding-generation administration.
//
// The embed-task surface needs direct handles to the gap scanner,
// failures repo, ack store, generations registry, and the rw/ro pools
// (for the activator-eligible-count query and the dry-run candidate
// SELECT). They live here rather than inside Service because Service
// is the auth boundary for tag/caption — embed has its own gap
// predicate and its own admin-only generation lifecycle.
type aiCtx struct {
	svc      *aiservice.Service
	caller   owners.Principal
	cfg      *config.Config
	close    func()
	rw       *sql.DB
	ro       *sql.DB
	gap      *gapscanner.Scanner
	failures *failures.Repo
	ack      *ack.Store
	gens     *embedding.Generations
	provider *airuntime.Provider
}

// loadAICtx loads the CLI's configuration, opens the DB (respecting
// FOTOBANK_DB_PATH), and constructs the AI Service plus the caller
// Principal. Enforces stub-mode identity like `albums` because we need
// a single well-defined caller for operator tooling.
func loadAICtx(cfgPath string) (*aiCtx, error) {
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
			"fotobank ai requires identity.mode = stub (got %q)",
			cfg.Identity.Mode)
	}
	d, err := openDatabase(cfg)
	if err != nil {
		return nil, err
	}
	appSettingsRepo := appsettingsstore.NewRepo(d.WriteDB(), d.ReadDB())
	provider, err := airuntime.NewProvider(context.Background(), airuntime.Source{
		FilePath: path,
		Repo:     appSettingsRepo,
	})
	if err != nil {
		_ = d.Close()
		return nil, fmt.Errorf("load effective ai config: %w", err)
	}
	snap := provider.Effective()
	cfg.AI = snap.Config
	rw, ro := d.WriteDB(), d.ReadDB()
	q := jobs.NewQueue(rw, ro)
	resR := results.NewRepo(rw, ro)
	failR := failures.NewRepo(rw, ro)
	skipR := skipped.NewRepo(rw, ro)
	ackS := ack.New(rw, ro)
	gs := gapscanner.New(ro, q, resR, skipR)
	gens := embedding.NewGenerations(rw, ro)
	return &aiCtx{
		svc: aiservice.New(aiservice.Deps{
			Queue:    q,
			Results:  resR,
			Failures: failR,
			Skipped:  skipR,
			Ack:      ackS,
			Gap:      gs,
			ConfigFingerprints: aiservice.ConfigFingerprints{
				Tag:     snap.Result.Tag,
				Caption: snap.Result.Caption,
				Embed:   snap.Result.Embed,
			},
			Runtime: provider,
		}),
		caller:   owners.Principal{Hub: cfg.Identity.Stub.Hub, UserID: cfg.Identity.Stub.UserID},
		cfg:      cfg,
		close:    func() { _ = d.Close() },
		rw:       rw,
		ro:       ro,
		gap:      gs,
		failures: failR,
		ack:      ackS,
		gens:     gens,
		provider: provider,
	}, nil
}

// newAICmd wires the `fotobank ai` command group. Running it without a
// subcommand prints usage and returns a usage error.
func newAICmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ai",
		Short: "AI tagging and captioning operations",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			_ = cmd.Usage()
			return newUsageError("a subcommand is required")
		},
	}
	cmd.AddCommand(newAIStatusCmd())
	cmd.AddCommand(newAIBackfillCmd())
	cmd.AddCommand(newAIRetryFailedCmd())
	cmd.AddCommand(newAIAcknowledgeCmd())
	cmd.AddCommand(newAIListGenerationsCmd())
	cmd.AddCommand(newAIPromoteGenerationCmd())
	cmd.AddCommand(newAICompactRetiredGenerationsCmd())
	return cmd
}

func ensureAIDaemon(ctx context.Context, cfgPath string) (client.Lifecycle, error) {
	lifecycle, err := daemonLifecycle(cfgPath, "")
	if err != nil {
		return lifecycle, err
	}
	cfg, err := config.LoadUnchecked(lifecycle.ConfigPath)
	if err != nil {
		return lifecycle, err
	}
	if cfg.Identity.Mode != "stub" {
		return lifecycle, fmt.Errorf("fotobank ai requires identity.mode = stub (got %q)", cfg.Identity.Mode)
	}
	_, err = lifecycle.Ensure(ctx)
	return lifecycle, err
}

func newAIStatusCmd() *cobra.Command {
	var cfgPath string
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show AI health and queue counters as JSON",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runAIStatus(cmd.Context(), cfgPath, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", "", "path to config file (defaults to DefaultConfigPath)")
	return cmd
}

func runAIStatus(ctx context.Context, cfgPath string, stdout io.Writer) error {
	c, err := ensureAIDaemon(ctx, cfgPath)
	if err != nil {
		return err
	}
	h, err := client.AIHealth(ctx, c.DBPath, c.Version)
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(h)
}

func newAIBackfillCmd() *cobra.Command {
	var (
		cfgPath  string
		taskList []string
		force    bool
	)
	cmd := &cobra.Command{
		Use:   "backfill",
		Short: "Enqueue missing-fingerprint AI jobs",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			tasks, err := parseTaskList(taskList)
			if err != nil {
				return err
			}
			if len(tasks) == 0 {
				return newUsageError("--task is required (tag,caption,embed)")
			}
			c, err := loadAICtx(cfgPath)
			if err != nil {
				return err
			}
			defer c.close()
			total := 0
			for _, t := range tasks {
				var n int
				switch t {
				case ai.TaskEmbed:
					n, err = backfillEmbed(cmd.Context(), c, force)
				default:
					n, err = c.svc.Backfill(cmd.Context(), c.caller, t, force)
				}
				if err != nil {
					if errors.Is(err, errs.ErrAcknowledgementRequired) {
						return fmt.Errorf("acknowledgement required — run `fotobank ai acknowledge --hidden-processing` first")
					}
					return fmt.Errorf("backfill %s: %w", t, err)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s: enqueued %d\n", t, n)
				total += n
			}
			fmt.Fprintf(cmd.OutOrStdout(), "total: %d\n", total)
			return nil
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", "", "path to config file (defaults to DefaultConfigPath)")
	cmd.Flags().StringSliceVar(&taskList, "task", nil, "tag,caption,embed")
	cmd.Flags().BoolVar(&force, "force", false, "include media that already have an active result")
	return cmd
}

// backfillEmbed routes the embed task through gapscanner.ScanEmbed
// rather than Service.Backfill — embed has its own gap-fill predicate
// (joins on media_embedding_ids by generation_id, see G1) that
// Service.Backfill's tag/caption-shaped Scan path doesn't speak. The
// CLI is responsible for resolving the building generation up front
// so a fresh deployment auto-creates one on first backfill, matching
// the worker's own behaviour.
//
// Acknowledgement is enforced at the CLI layer because the embed
// surface uses neither Service.Backfill (which checks ack via the
// ack.Store) nor the activator's ack-paused path. Without this gate,
// `backfill --task=embed` would silently bypass the hidden-photo
// processing acknowledgement that other AI surfaces require.
//
// Force is intentionally ignored on the embed path: the embed
// predicate is per-generation, not per-fingerprint, so "include media
// that already have an active result" doesn't translate. A future
// `--force` semantics for embed (e.g. clear ai_skipped to retry
// videos) is a Section R/S concern.
func backfillEmbed(ctx context.Context, c *aiCtx, _ bool) (int, error) {
	if !c.cfg.AI.Embed.Enabled {
		return 0, fmt.Errorf("ai.embed.enabled is false; configure [ai.embed] before running embed backfill")
	}
	acked, err := c.ack.IsAcknowledged(ctx, c.caller)
	if err != nil {
		return 0, fmt.Errorf("ack lookup: %w", err)
	}
	if !acked {
		return 0, errs.ErrAcknowledgementRequired
	}
	snap := c.provider.Effective()
	fp := snap.Result.Embed
	gen, err := c.gens.FindOrCreateBuilding(ctx, fp, c.cfg.AI.Embed.Dimension)
	if err != nil {
		return 0, fmt.Errorf("resolve building generation: %w", err)
	}
	return c.gap.ScanEmbed(ctx, gapscanner.EmbedScanRequest{
		Owner:             c.caller,
		Generation:        gen,
		ClaimFingerprint:  snap.Claim.Embed,
		ResultFingerprint: fp,
		AckAllowsHidden:   acked,
		RetryBudget:       c.cfg.AI.Embed.MaxRetries,
		Limit:             0,
	})
}

func newAIRetryFailedCmd() *cobra.Command {
	var (
		cfgPath  string
		taskList []string
	)
	cmd := &cobra.Command{
		Use:   "retry-failed",
		Short: "Re-enqueue all current-fingerprint failures",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			tasks, err := parseTaskList(taskList)
			if err != nil {
				return err
			}
			if len(tasks) == 0 {
				return newUsageError("--task is required (tag,caption,embed)")
			}
			c, err := loadAICtx(cfgPath)
			if err != nil {
				return err
			}
			defer c.close()
			for _, t := range tasks {
				var n int
				switch t {
				case ai.TaskEmbed:
					n, err = retryFailedEmbed(cmd.Context(), c)
				default:
					n, err = c.svc.RetryFailed(cmd.Context(), c.caller, t)
				}
				if err != nil {
					return fmt.Errorf("retry %s: %w", t, err)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s: re-enqueued %d\n", t, n)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", "", "path to config file (defaults to DefaultConfigPath)")
	cmd.Flags().StringSliceVar(&taskList, "task", nil, "tag,caption,embed")
	return cmd
}

// retryFailedEmbed clears the embed-task failures for caller-owned
// media and re-enqueues them through gapscanner.ScanEmbed.
//
// Mirrors Service.RetryFailed's batched cutoff loop: capture a
// snapshot timestamp at entry so newly-recorded failures (e.g. a
// concurrent worker re-failing a freshly enqueued retry) don't
// spiral the loop indefinitely. ScanEmbed is invoked per chunk with
// the targeted media IDs so the embed predicate's failure-budget
// gate is bypassed only for the explicitly-retried set.
func retryFailedEmbed(ctx context.Context, c *aiCtx) (int, error) {
	if !c.cfg.AI.Embed.Enabled {
		return 0, fmt.Errorf("ai.embed.enabled is false; configure [ai.embed] before running embed retry-failed")
	}
	acked, err := c.ack.IsAcknowledged(ctx, c.caller)
	if err != nil {
		return 0, fmt.Errorf("ack lookup: %w", err)
	}
	if !acked {
		return 0, errs.ErrAcknowledgementRequired
	}
	snap := c.provider.Effective()
	fp := snap.Result.Embed
	gen, err := c.gens.FindOrCreateBuilding(ctx, fp, c.cfg.AI.Embed.Dimension)
	if err != nil {
		return 0, fmt.Errorf("resolve building generation: %w", err)
	}
	const batchSize = 500
	cutoff := time.Now().UTC()
	total := 0
	for {
		rows, err := c.failures.ListForFingerprintByOwner(
			ctx, ai.TaskEmbed, fp, c.caller.Hub, c.caller.UserID, cutoff, batchSize,
		)
		if err != nil {
			return total, fmt.Errorf("list failures: %w", err)
		}
		if len(rows) == 0 {
			return total, nil
		}
		mediaIDs := make([]string, 0, len(rows))
		for _, r := range rows {
			mediaIDs = append(mediaIDs, r.MediaID)
		}
		if _, err := c.failures.DeleteByMediaIDs(ctx, ai.TaskEmbed, fp, mediaIDs); err != nil {
			return total, fmt.Errorf("delete failures: %w", err)
		}
		n, err := c.gap.ScanEmbed(ctx, gapscanner.EmbedScanRequest{
			Owner:             c.caller,
			Generation:        gen,
			ClaimFingerprint:  snap.Claim.Embed,
			ResultFingerprint: fp,
			AckAllowsHidden:   acked,
			RetryBudget:       c.cfg.AI.Embed.MaxRetries,
			MediaIDs:          mediaIDs,
		})
		total += n
		if err != nil {
			return total, fmt.Errorf("scan embed: %w", err)
		}
		if len(rows) < batchSize {
			return total, nil
		}
	}
}

func newAIAcknowledgeCmd() *cobra.Command {
	var (
		cfgPath string
		hidden  bool
	)
	cmd := &cobra.Command{
		Use:   "acknowledge",
		Short: "Acknowledge AI processing characteristics (required to start workers)",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !hidden {
				return newUsageError("--hidden-processing flag required")
			}
			c, err := ensureAIDaemon(cmd.Context(), cfgPath)
			if err != nil {
				return err
			}
			if err := client.AcknowledgeAI(cmd.Context(), c.DBPath, c.Version, httpapi.AIAcknowledgeRequest{Kind: "hidden_processing"}); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "ok")
			return nil
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", "", "path to config file (defaults to DefaultConfigPath)")
	cmd.Flags().BoolVar(&hidden, "hidden-processing", false, "acknowledge that AI processes hidden photos")
	return cmd
}

// parseTaskList accepts repeated --task values and comma-separated lists
// (cobra's StringSliceVar already splits on commas, but we accept either
// form defensively) and returns the unique, valid ai.Task values. It
// returns a usage error when an unknown task is supplied so a typo like
// `--task tag,bogus` fails fast instead of silently running only `tag`.
// Empty tokens are tolerated so --task=tag, doesn't error.
func parseTaskList(in []string) ([]ai.Task, error) {
	out := []ai.Task{}
	seen := map[ai.Task]bool{}
	for _, raw := range in {
		for p := range strings.SplitSeq(raw, ",") {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			t := ai.Task(p)
			if !t.Valid() {
				return nil, newUsageError("unknown task %q (expected tag, caption, or embed)", p)
			}
			if seen[t] {
				continue
			}
			seen[t] = true
			out = append(out, t)
		}
	}
	return out, nil
}

// listGenerationOutputRow is the JSON shape emitted by
// `fotobank ai list-generations`. Mirrors embedding.Row's stable
// fields plus the activator's eligible_count, expressed as a small
// flat struct so a future schema change in embedding.Row doesn't
// silently widen the CLI surface.
type listGenerationOutputRow struct {
	ID            int64      `json:"id"`
	Fingerprint   string     `json:"fingerprint"`
	ModelID       string     `json:"model_id"`
	InputProfile  string     `json:"input_profile"`
	State         string     `json:"state"`
	Dimension     int        `json:"dimension"`
	EmbeddedCount int        `json:"embedded_count"`
	EligibleCount int        `json:"eligible_count"`
	CreatedAt     time.Time  `json:"created_at"`
	ActivatedAt   *time.Time `json:"activated_at,omitempty"`
	RetiredAt     *time.Time `json:"retired_at,omitempty"`
}

// allGenerationStates is the set returned when --state is empty —
// used by listGenerations to walk every state and concat the results
// in (building, active, retired) order. The order mirrors the
// natural lifecycle progression so a panel that walks the slice
// renders the canonical timeline.
var allGenerationStates = []string{"building", "active", "retired"}

// validGenerationStates is the allowed set of --state values;
// validateGenerationState rejects anything else with a usage error so
// a typo like --state=actiev surfaces immediately.
var validGenerationStates = map[string]struct{}{
	"":         {}, // empty = all states
	"building": {},
	"active":   {},
	"retired":  {},
}

func newAIListGenerationsCmd() *cobra.Command {
	var (
		cfgPath string
		state   string
	)
	cmd := &cobra.Command{
		Use:   "list-generations",
		Short: "List embedding generations as JSON",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if _, ok := validGenerationStates[state]; !ok {
				return newUsageError("--state %q (expected active, building, retired, or unset)", state)
			}
			return runAIListGenerations(cmd.Context(), cfgPath, state, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", "", "path to config file (defaults to DefaultConfigPath)")
	cmd.Flags().StringVar(&state, "state", "", "filter by state (active, building, retired); empty = all")
	return cmd
}

// runAIListGenerations encodes one JSON array containing every
// generation matching state (or all states when state is empty),
// each row carrying the activator's eligible_count once for the
// caller. Per-row eligibility is principal-scoped not
// generation-scoped, so we compute it once and apply uniformly.
func runAIListGenerations(ctx context.Context, cfgPath, state string, stdout io.Writer) error {
	c, err := loadAICtx(cfgPath)
	if err != nil {
		return err
	}
	defer c.close()

	states := []string{state}
	if state == "" {
		states = allGenerationStates
	}
	rows := []embedding.Row{}
	for _, s := range states {
		batch, err := c.gens.List(ctx, s)
		if err != nil {
			return fmt.Errorf("list generations (state=%s): %w", s, err)
		}
		rows = append(rows, batch...)
	}

	eligible, err := eligibleEmbedCount(ctx, c.ro, c.caller)
	if err != nil {
		return fmt.Errorf("eligible count: %w", err)
	}

	out := make([]listGenerationOutputRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, listGenerationOutputRow{
			ID:            r.ID,
			Fingerprint:   r.Fingerprint,
			ModelID:       r.ModelID,
			InputProfile:  r.InputProfile,
			State:         r.State,
			Dimension:     r.Dimension,
			EmbeddedCount: r.EmbeddedCount,
			EligibleCount: eligible,
			CreatedAt:     r.CreatedAt,
			ActivatedAt:   r.ActivatedAt,
			RetiredAt:     r.RetiredAt,
		})
	}
	return json.NewEncoder(stdout).Encode(out)
}

// eligibleEmbedCount mirrors the activator's eligibleCount predicate
// (see internal/ai/embedding/activator.go::eligibleCount) so the
// list-generations output is consistent with the activation decision.
// ackAllowsHidden=false: hidden media never count toward the
// eligibility budget at the CLI surface, matching the activator
// default.
func eligibleEmbedCount(ctx context.Context, ro *sql.DB, caller owners.Principal) (int, error) {
	var n int
	err := ro.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM assets m
		 WHERE m.state = 'ready' AND m.owner_hub = ? AND m.owner_user_id = ?
		   AND m.thumb_status = 'ready'
		   AND m.hidden_at IS NULL
		   AND NOT EXISTS (
		     SELECT 1 FROM ai_skipped sk
		      WHERE sk.media_id = m.id AND sk.task = 'embed'
		   )`,
		caller.Hub, caller.UserID,
	).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("scan: %w", err)
	}
	return n, nil
}

func newAIPromoteGenerationCmd() *cobra.Command {
	var (
		cfgPath string
		yes     bool
	)
	cmd := &cobra.Command{
		Use:   "promote-generation [generation-id]",
		Short: "Admin override: promote a retired generation back to active",
		Args:  usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return newUsageError("generation-id %q: %v", args[0], err)
			}
			return runAIPromoteGeneration(cmd, cfgPath, id, yes)
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", "", "path to config file (defaults to DefaultConfigPath)")
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the interactive confirm prompt")
	return cmd
}

// runAIPromoteGeneration looks up id, validates it is in the retired
// state, warns when promoting a row older than retain_retired_days
// (the row may already be a compactor candidate), prompts for
// confirmation unless --yes was passed, then flips the row to active
// via Generations.Promote. Promote retires any current active row in
// the same tx so the partial-unique embedding_generations_one_active
// never observes two active rows.
//
// promote-generation is the admin-override surface — its only sensible
// target is a row that has already been retired but the operator wants
// to bring back. Building rows belong to the activator (which uses
// PromoteFromBuilding to defend against admin retire races), and the
// already-active row is the no-op case. Rejecting both up front keeps
// the surface honest and surfaces operator typos as clean errors.
func runAIPromoteGeneration(cmd *cobra.Command, cfgPath string, id int64, yes bool) error {
	ctx := cmd.Context()
	c, err := loadAICtx(cfgPath)
	if err != nil {
		return err
	}
	defer c.close()

	row, err := c.gens.GetByID(ctx, id)
	if err != nil {
		return fmt.Errorf("lookup generation %d: %w", id, err)
	}
	if row.State != "retired" {
		return fmt.Errorf(
			"promote-generation only valid on retired rows; generation %d is %s",
			id, row.State)
	}
	stderr := cmd.ErrOrStderr()
	if row.RetiredAt != nil {
		retainDays := c.cfg.Search.RetainRetiredDays
		if retainDays > 0 {
			window := time.Duration(retainDays) * 24 * time.Hour
			age := time.Since(*row.RetiredAt)
			if age > window {
				fmt.Fprintf(stderr,
					"WARNING: generation %d has been retired for %s (>retain_retired_days=%d); "+
						"the compactor may drop it before the next sweep stabilises\n",
					id, age.Round(time.Hour), retainDays)
			}
		}
	}
	stdout := cmd.OutOrStdout()
	if !yes {
		fmt.Fprintf(stdout, "Promote generation %d (state=%s, fingerprint=%s) to active? [y/N]: ",
			row.ID, row.State, row.Fingerprint)
		reader := bufio.NewReader(cmd.InOrStdin())
		ans, _ := reader.ReadString('\n')
		ans = strings.ToLower(strings.TrimSpace(ans))
		if ans != "y" && ans != "yes" {
			return fmt.Errorf("aborted")
		}
	}
	if err := c.gens.Promote(ctx, id); err != nil {
		return fmt.Errorf("promote: %w", err)
	}
	fmt.Fprintf(stdout, "promoted generation %d (fingerprint=%s)\n", id, row.Fingerprint)
	return nil
}

func newAICompactRetiredGenerationsCmd() *cobra.Command {
	var (
		cfgPath string
		dryRun  bool
	)
	cmd := &cobra.Command{
		Use:   "compact-retired-generations",
		Short: "Drop retired generations older than [search] retain_retired_days",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runAICompactRetiredGenerations(cmd.Context(), cfgPath, dryRun, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", "", "path to config file (defaults to DefaultConfigPath)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "list candidates without dropping them")
	return cmd
}

// runAICompactRetiredGenerations is the CLI hook for the K1
// compactor. With --dry-run, it queries the same predicate the
// compactor uses (state='retired' AND retired_at < cutoff) and prints
// each candidate's id and age — a safe inspection mode for an admin
// before they execute the destructive sweep. Without --dry-run, it
// runs SweepOnce and reports the count of generations dropped.
//
// retain_retired_days = 0 disables the time gate and would drop
// every retired row immediately; the dry-run path makes that
// behaviour visible up front, so an operator can correct the config
// before the destructive run.
func runAICompactRetiredGenerations(ctx context.Context, cfgPath string, dryRun bool, stdout io.Writer) error {
	c, err := loadAICtx(cfgPath)
	if err != nil {
		return err
	}
	defer c.close()

	window := time.Duration(c.cfg.Search.RetainRetiredDays) * 24 * time.Hour
	if dryRun {
		return printCompactCandidates(ctx, c.ro, window, stdout)
	}
	comp := embedding.NewCompactor(c.rw, window)
	dropped, err := comp.SweepOnce(ctx)
	if err != nil {
		return fmt.Errorf("compact: %w", err)
	}
	fmt.Fprintf(stdout, "%d retired generations dropped\n", dropped)
	return nil
}

// printCompactCandidates mirrors the compactor's phase-1 SELECT and
// emits one human-readable line per candidate: id, vec_table_name,
// retired-for age. The dry-run output stops at this point — no DROP
// TABLE, no DELETE — so the admin can audit the candidate set
// before committing.
func printCompactCandidates(ctx context.Context, ro *sql.DB, window time.Duration, stdout io.Writer) error {
	cutoff := time.Now().UTC().Add(-window)
	rows, err := ro.QueryContext(ctx, `
		SELECT id, vec_table_name, retired_at FROM embedding_generations
		 WHERE state = 'retired' AND retired_at < ?
		 ORDER BY id ASC`, cutoff)
	if err != nil {
		return fmt.Errorf("select retired generations: %w", err)
	}
	defer func() { _ = rows.Close() }()

	count := 0
	for rows.Next() {
		var (
			id           int64
			vecTableName string
			retiredAt    time.Time
		)
		if err := rows.Scan(&id, &vecTableName, &retiredAt); err != nil {
			return fmt.Errorf("scan: %w", err)
		}
		age := time.Since(retiredAt).Round(time.Hour)
		fmt.Fprintf(stdout, "candidate generation_id=%d vec_table=%s retired_for=%s\n",
			id, vecTableName, age)
		count++
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("rows: %w", err)
	}
	fmt.Fprintf(stdout, "%d candidate(s) for compaction\n", count)
	return nil
}
