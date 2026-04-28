// Package cli implements the top-level fotobank command via cobra.
// Subcommands live in sibling files (version.go, config.go, server.go,
// owners.go) and attach to the root returned by newRootCmd. Callers
// invoke Run/RunContext, which construct a fresh command tree per
// invocation so tests can drive the CLI without global state.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
)

// usageError signals that the user supplied invalid arguments. Commands
// return it so Execute can map it to exit code 2 (usage error) rather
// than 1 (runtime error).
type usageError struct{ err error }

func (u usageError) Error() string { return u.err.Error() }
func (u usageError) Unwrap() error { return u.err }

func newUsageError(format string, args ...any) error {
	return usageError{err: fmt.Errorf(format, args...)}
}

// Run dispatches a fotobank CLI invocation with a background context.
// Returns 0 on success, 1 on runtime errors, 2 on usage errors.
func Run(args []string, stdout, stderr io.Writer) int {
	return RunContext(context.Background(), args, stdout, stderr)
}

// RunContext dispatches a fotobank CLI invocation, threading ctx through
// to long-lived subcommands (the server subcommand observes it to
// trigger graceful shutdown when the caller cancels).
func RunContext(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	root := newRootCmd()
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.SetContext(ctx)

	if err := root.Execute(); err != nil {
		if isUsageError(err) {
			fmt.Fprintln(stderr, formatUsageError(err))
			return 2
		}
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

// formatUsageError prefixes usage-style errors with "usage: " so the
// message reads naturally and so tests that look for that substring
// can distinguish usage failures from runtime failures.
func formatUsageError(err error) string {
	msg := err.Error()
	if strings.HasPrefix(msg, "usage:") {
		return msg
	}
	return "usage: " + msg
}

// errFlagParse is the sentinel we wrap pflag errors with so RunContext
// can map them to exit code 2.
var errFlagParse = errors.New("flag parse error")

// isUsageError reports whether err is a usage-style error that should
// map to exit code 2 rather than 1. Covers our own usageError, wrapped
// pflag errors, and cobra's "unknown command"/"unknown flag" messages.
// Cobra's positional-arg validators are routed through usageArgs (see
// args.go) so they return usageError directly instead of relying on
// fragile substring matching.
func isUsageError(err error) bool {
	var u usageError
	if errors.As(err, &u) {
		return true
	}
	if errors.Is(err, errFlagParse) {
		return true
	}
	msg := err.Error()
	return strings.HasPrefix(msg, "unknown command") ||
		strings.HasPrefix(msg, "unknown flag") ||
		strings.HasPrefix(msg, "unknown shorthand flag") ||
		strings.HasPrefix(msg, "required flag(s)")
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "fotobank",
		Short: "Personal photo archive with multi-owner scoped sharing",
		// Silence auto-printing of usage/errors; RunContext owns the
		// stderr output so exit-code mapping stays deterministic.
		SilenceUsage:  true,
		SilenceErrors: true,
		// Called when the user runs `fotobank` with no subcommand.
		RunE: func(cmd *cobra.Command, _ []string) error {
			_ = cmd.Usage()
			return newUsageError("a subcommand is required")
		},
	}
	root.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		_ = cmd.Usage()
		return fmt.Errorf("%w: %w", errFlagParse, err)
	})
	root.AddCommand(newVersionCmd())
	root.AddCommand(newConfigCmd())
	root.AddCommand(newServerCmd())
	root.AddCommand(newOwnersCmd())
	root.AddCommand(newImportCmd())
	root.AddCommand(newReconcileCmd())
	root.AddCommand(newThumbsCmd())
	root.AddCommand(newGPSCmd())
	root.AddCommand(newPairCmd())
	root.AddCommand(newAlbumsCmd())
	root.AddCommand(newSharesCmd())
	root.AddCommand(newBackupCmd())
	return root
}
