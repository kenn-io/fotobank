package cli

import (
	"bufio"
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"go.kenn.io/fotobank/internal/ai"
	"go.kenn.io/fotobank/internal/client"
	"go.kenn.io/fotobank/internal/config"
	"go.kenn.io/fotobank/internal/httpapi"
)

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
	h, err := client.AIHealth(ctx, c.ConfigPath, c.Version)
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
		asJSON   bool
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
			c, err := ensureAIDaemon(cmd.Context(), cfgPath)
			if err != nil {
				return err
			}
			total := 0
			results := make(map[string]httpapi.AIEnqueuedResult)
			var taskErr error
			for _, t := range tasks {
				result, err := client.BackfillAI(cmd.Context(), c.ConfigPath, c.Version, httpapi.AIBackfillRequest{Task: string(t), Force: force})
				n := result.Enqueued
				if err != nil {
					taskErr = fmt.Errorf("backfill %s: %w", t, err)
					break
				}
				results[string(t)] = result
				if !asJSON {
					fmt.Fprintf(cmd.OutOrStdout(), "%s: enqueued %d\n", t, n)
				}
				total += n
			}
			if asJSON && len(results) > 0 {
				return errors.Join(taskErr, jsonv2.MarshalWrite(cmd.OutOrStdout(), results))
			}
			if taskErr != nil {
				return taskErr
			}
			fmt.Fprintf(cmd.OutOrStdout(), "total: %d\n", total)
			return nil
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", "", "path to config file (defaults to DefaultConfigPath)")
	cmd.Flags().StringSliceVar(&taskList, "task", nil, "tag,caption,embed")
	cmd.Flags().BoolVar(&force, "force", false, "include media that already have an active result")
	cmd.Flags().BoolVar(&asJSON, "json", false, "write completed task counts as JSON")
	return cmd
}

func newAIRetryFailedCmd() *cobra.Command {
	var (
		cfgPath  string
		taskList []string
		asJSON   bool
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
			c, err := ensureAIDaemon(cmd.Context(), cfgPath)
			if err != nil {
				return err
			}
			results := make(map[string]httpapi.AIEnqueuedResult)
			var taskErr error
			for _, t := range tasks {
				result, err := client.RetryFailedAI(cmd.Context(), c.ConfigPath, c.Version, httpapi.AIRetryFailedRequest{Task: string(t)})
				n := result.Enqueued
				if err != nil {
					taskErr = fmt.Errorf("retry %s: %w", t, err)
					break
				}
				results[string(t)] = result
				if !asJSON {
					fmt.Fprintf(cmd.OutOrStdout(), "%s: re-enqueued %d\n", t, n)
				}
			}
			if asJSON && len(results) > 0 {
				return errors.Join(taskErr, jsonv2.MarshalWrite(cmd.OutOrStdout(), results))
			}
			return taskErr
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", "", "path to config file (defaults to DefaultConfigPath)")
	cmd.Flags().StringSliceVar(&taskList, "task", nil, "tag,caption,embed")
	cmd.Flags().BoolVar(&asJSON, "json", false, "write completed task counts as JSON")
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
			c, err := ensureAIDaemon(cmd.Context(), cfgPath)
			if err != nil {
				return err
			}
			if err := client.AcknowledgeAI(cmd.Context(), c.ConfigPath, c.Version, httpapi.AIAcknowledgeRequest{Kind: "hidden_processing"}); err != nil {
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

func runAIListGenerations(ctx context.Context, cfgPath, state string, stdout io.Writer) error {
	c, err := ensureAIDaemon(ctx, cfgPath)
	if err != nil {
		return err
	}
	result, err := client.ListGenerations(ctx, c.ConfigPath, c.Version, state)
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(result.Items)
}

func newAIPromoteGenerationCmd() *cobra.Command {
	var (
		cfgPath string
		yes     bool
		asJSON  bool
	)
	cmd := &cobra.Command{
		Use:   "promote-generation [generation-id]",
		Short: "Admin override: promote a retired generation back to active",
		Args:  usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil || id <= 0 {
				return newUsageError("generation-id must be a positive integer")
			}
			return runAIPromoteGeneration(cmd, cfgPath, id, yes, asJSON)
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", "", "path to config file (defaults to DefaultConfigPath)")
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the interactive confirm prompt")
	cmd.Flags().BoolVar(&asJSON, "json", false, "write the promoted generation as JSON")
	return cmd
}

func runAIPromoteGeneration(cmd *cobra.Command, cfgPath string, id int64, yes, asJSON bool) error {
	ctx := cmd.Context()
	c, err := ensureAIDaemon(ctx, cfgPath)
	if err != nil {
		return err
	}
	details, err := client.GetGeneration(ctx, c.ConfigPath, c.Version, id)
	if err != nil {
		return err
	}
	row := details.Generation
	if row.State != "retired" {
		return fmt.Errorf("promote-generation only valid on retired rows; generation %d is %s", id, row.State)
	}
	if row.RetiredAt != nil && details.RetainRetiredDays > 0 {
		age := time.Since(*row.RetiredAt)
		if age > time.Duration(details.RetainRetiredDays)*24*time.Hour {
			fmt.Fprintf(cmd.ErrOrStderr(), "WARNING: generation %d has been retired for %s (>retain_retired_days=%d); the compactor may drop it before promotion\n", id, age.Round(time.Hour), details.RetainRetiredDays)
		}
	}
	if !yes {
		prompt := cmd.OutOrStdout()
		if asJSON {
			prompt = cmd.ErrOrStderr()
		}
		fmt.Fprintf(prompt, "Promote generation %d (state=%s, fingerprint=%s) to active? [y/N]: ", row.ID, row.State, row.Fingerprint)
		answer, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
		if err != nil && err != io.EOF {
			return err
		}
		answer = strings.ToLower(strings.TrimSpace(answer))
		if answer != "y" && answer != "yes" {
			return fmt.Errorf("aborted")
		}
	}
	result, err := client.PromoteGeneration(ctx, c.ConfigPath, c.Version, id)
	if err != nil {
		return err
	}
	if asJSON {
		return jsonv2.MarshalWrite(cmd.OutOrStdout(), result)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "promoted generation %d (fingerprint=%s)\n", result.ID, result.Fingerprint)
	return nil
}

func newAICompactRetiredGenerationsCmd() *cobra.Command {
	var (
		cfgPath string
		dryRun  bool
		asJSON  bool
	)
	cmd := &cobra.Command{
		Use:   "compact-retired-generations",
		Short: "Drop retired generations older than [search] retain_retired_days",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runAICompactRetiredGenerations(cmd.Context(), cfgPath, dryRun, asJSON, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", "", "path to config file (defaults to DefaultConfigPath)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "list candidates without dropping them")
	cmd.Flags().BoolVar(&asJSON, "json", false, "write compaction results as JSON")
	return cmd
}

func runAICompactRetiredGenerations(ctx context.Context, cfgPath string, dryRun, asJSON bool, stdout io.Writer) error {
	c, err := ensureAIDaemon(ctx, cfgPath)
	if err != nil {
		return err
	}
	result, err := client.CompactGenerations(ctx, c.ConfigPath, c.Version, dryRun)
	if err != nil {
		return err
	}
	if asJSON {
		if result.Error != "" {
			err = fmt.Errorf("compact: %s", result.Error)
		}
		return errors.Join(err, jsonv2.MarshalWrite(stdout, result))
	}
	if dryRun {
		if result.Error != "" {
			return fmt.Errorf("compact: %s", result.Error)
		}
		for _, candidate := range result.Candidates {
			fmt.Fprintf(stdout, "candidate generation_id=%d vec_table=%s retired_for=%s\n", candidate.ID, candidate.VecTableName, time.Since(candidate.RetiredAt).Round(time.Hour))
		}
		fmt.Fprintf(stdout, "%d candidate(s) for compaction\n", len(result.Candidates))
	} else {
		fmt.Fprintf(stdout, "%d retired generations dropped\n", result.Dropped)
	}
	if result.Error != "" {
		return fmt.Errorf("compact: %s", result.Error)
	}
	return nil
}
