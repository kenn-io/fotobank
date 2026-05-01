package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/wesm/fotobank/internal/ai"
	"github.com/wesm/fotobank/internal/ai/ack"
	"github.com/wesm/fotobank/internal/ai/failures"
	"github.com/wesm/fotobank/internal/ai/gapscanner"
	"github.com/wesm/fotobank/internal/ai/jobs"
	"github.com/wesm/fotobank/internal/ai/prompts"
	"github.com/wesm/fotobank/internal/ai/results"
	"github.com/wesm/fotobank/internal/ai/skipped"
	"github.com/wesm/fotobank/internal/config"
	"github.com/wesm/fotobank/internal/db"
	"github.com/wesm/fotobank/internal/errs"
	"github.com/wesm/fotobank/internal/owners"
	aiservice "github.com/wesm/fotobank/internal/service/ai"
)

// aiCtx bundles the dependencies every `fotobank ai` subcommand needs:
// the auth-scoped service, the stub caller principal, the loaded
// config (so `status` can honor [ai].enabled rather than hardcoding
// true), and a teardown callback that closes the underlying DB.
type aiCtx struct {
	svc    *aiservice.Service
	caller owners.Principal
	cfg    *config.Config
	close  func()
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
	dbPath := os.Getenv("FOTOBANK_DB_PATH")
	if dbPath == "" {
		dbPath = filepath.Join(cfg.Flash.Root, "fotobank.sqlite")
	}
	d, err := db.Open(dbPath)
	if err != nil {
		return nil, err
	}
	rw, ro := d.WriteDB(), d.ReadDB()
	q := jobs.NewQueue(rw, ro)
	resR := results.NewRepo(rw, ro)
	failR := failures.NewRepo(rw, ro)
	skipR := skipped.NewRepo(rw, ro)
	ackS := ack.New(rw, ro)
	gs := gapscanner.New(ro, q, resR, skipR)
	return &aiCtx{
		svc: aiservice.New(aiservice.Deps{
			Queue:    q,
			Results:  resR,
			Failures: failR,
			Skipped:  skipR,
			Ack:      ackS,
			Gap:      gs,
			ConfigFingerprints: aiservice.ConfigFingerprints{
				Tag: ai.Fingerprint{
					ModelID:       cfg.AI.Tag.Model,
					PromptVersion: prompts.Tag().Version,
					InputProfile:  "jpeg-1024-q85-metadata-stripped-v1",
				},
				Caption: ai.Fingerprint{
					ModelID:       cfg.AI.Caption.Model,
					PromptVersion: prompts.Caption().Version,
					InputProfile:  "jpeg-1024-q85-metadata-stripped-v1",
				},
			},
		}),
		caller: owners.Principal{Hub: cfg.Identity.Stub.Hub, UserID: cfg.Identity.Stub.UserID},
		cfg:    cfg,
		close:  func() { _ = d.Close() },
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
	return cmd
}

// unavailableProbe reports the gateway as unreachable with a short
// rationale. The CLI does not own a long-lived gateway client, so
// `fotobank ai status` cannot run a real reachability check —
// reporting "unavailable" is the honest answer rather than claiming
// reachable=true.
type unavailableProbe struct{}

func (unavailableProbe) Probe(_ context.Context) error {
	return errors.New("not probed by CLI; check the running server's /api/v1/ai/health")
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
	c, err := loadAICtx(cfgPath)
	if err != nil {
		return err
	}
	defer c.close()
	h := c.svc.Health(ctx, c.caller, aiservice.HealthInput{
		Enabled: c.cfg.AI.Enabled,
		Probe:   unavailableProbe{},
	})
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
				n, err := c.svc.Backfill(cmd.Context(), c.caller, t, force)
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
				n, err := c.svc.RetryFailed(cmd.Context(), c.caller, t)
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
			c, err := loadAICtx(cfgPath)
			if err != nil {
				return err
			}
			defer c.close()
			if err := c.svc.Acknowledge(cmd.Context(), c.caller); err != nil {
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
