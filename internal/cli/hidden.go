package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"go.kenn.io/fotobank/internal/auth/hidden"
	"go.kenn.io/fotobank/internal/config"
	"go.kenn.io/fotobank/internal/db"
	"go.kenn.io/fotobank/internal/media"
	"go.kenn.io/fotobank/internal/owners"
)

// hiddenCtx bundles dependencies for hidden subcommands.
type hiddenCtx struct {
	svc    *hidden.Service
	caller owners.Principal
	close  func()
}

// loadHiddenCtx loads config, opens DB, and constructs a hidden.Service.
// For stub mode, caller is the configured stub principal.
// For non-stub mode, a nil principal is returned (caller must supply --owner).
func loadHiddenCtx(cfgPath string) (*hiddenCtx, *config.Config, error) {
	path := cfgPath
	if path == "" {
		path = config.DefaultConfigPath()
	}
	cfg, err := config.Load(path)
	if err != nil {
		return nil, nil, err
	}
	dbPath := os.Getenv("FOTOBANK_DB_PATH")
	if dbPath == "" {
		dbPath = filepath.Join(cfg.Flash.Root, "fotobank.sqlite")
	}
	d, err := db.Open(dbPath)
	if err != nil {
		return nil, nil, err
	}
	mediaRepo := media.NewRepo(d.WriteDB(), d.ReadDB())
	hiddenRepo := hidden.NewRepo(d.WriteDB(), d.ReadDB())
	svc := hidden.NewService(hiddenRepo, mediaRepo)

	var caller owners.Principal
	if cfg.Identity.Mode == "stub" {
		caller = owners.Principal{
			Hub:    cfg.Identity.Stub.Hub,
			UserID: cfg.Identity.Stub.UserID,
		}
	}

	return &hiddenCtx{
		svc:    svc,
		caller: caller,
		close:  func() { _ = d.Close() },
	}, cfg, nil
}

// stdinReader wraps an io.Reader with a single bufio.Scanner so that
// multiple readPasscode / readLine calls on the same command share one
// scan position. Constructed once per command and threaded through the
// run functions.
type stdinReader struct {
	scanner    *bufio.Scanner
	isTerminal bool
}

// newStdinReader constructs a stdinReader from cmd's stdin. When the
// effective stdin is the real os.Stdin and it is a TTY, isTerminal is
// set so callers can use term.ReadPassword for masked input.
func newStdinReader(cmd *cobra.Command) *stdinReader {
	in := cmd.InOrStdin()
	isTTY := in == os.Stdin && term.IsTerminal(int(syscall.Stdin))
	return &stdinReader{
		scanner:    bufio.NewScanner(in),
		isTerminal: isTTY,
	}
}

// readPasscode reads one passcode line, masking terminal input if possible.
func (r *stdinReader) readPasscode(cmd *cobra.Command, prompt string) (string, error) {
	if r.isTerminal {
		fmt.Fprint(cmd.OutOrStdout(), prompt)
		b, err := term.ReadPassword(int(syscall.Stdin))
		fmt.Fprintln(cmd.OutOrStdout()) // newline after masked input
		if err != nil {
			return "", fmt.Errorf("read passcode: %w", err)
		}
		return string(b), nil
	}
	// Non-terminal path: plain line read (tests, piped input).
	if !r.scanner.Scan() {
		if err := r.scanner.Err(); err != nil {
			return "", fmt.Errorf("read passcode: %w", err)
		}
		return "", fmt.Errorf("read passcode: unexpected EOF")
	}
	return strings.TrimRight(r.scanner.Text(), "\r"), nil
}

// readLine reads one plain line (used for confirmation prompts).
func (r *stdinReader) readLine() (string, error) {
	if !r.scanner.Scan() {
		if err := r.scanner.Err(); err != nil {
			return "", err
		}
		return "", fmt.Errorf("unexpected EOF reading confirmation")
	}
	return strings.TrimRight(r.scanner.Text(), "\r"), nil
}

// newHiddenCmd returns the `fotobank hidden` command group.
func newHiddenCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hidden",
		Short: "Manage the hidden-privacy passcode for the stub principal",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			_ = cmd.Usage()
			return newUsageError("a subcommand is required")
		},
	}
	cmd.AddCommand(newHiddenSetupCmd())
	cmd.AddCommand(newHiddenChangeCmd())
	cmd.AddCommand(newHiddenDisableCmd())
	return cmd
}

// newHiddenSetupCmd returns the `fotobank hidden setup` subcommand.
func newHiddenSetupCmd() *cobra.Command {
	var cfgPath string
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Set up a new hidden-privacy passcode",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runHiddenSetup(cmd, cfgPath)
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", "", "path to config file")
	return cmd
}

func runHiddenSetup(cmd *cobra.Command, cfgPath string) error {
	hctx, cfg, err := loadHiddenCtx(cfgPath)
	if err != nil {
		return err
	}
	defer hctx.close()
	if cfg.Identity.Mode != "stub" {
		return fmt.Errorf(
			"hidden setup requires stub identity mode; got %q — use 'admin reset-hidden-passcode' instead",
			cfg.Identity.Mode)
	}

	sr := newStdinReader(cmd)
	passcode, err := sr.readPasscode(cmd, "New passcode: ")
	if err != nil {
		return err
	}
	confirm, err := sr.readPasscode(cmd, "Confirm passcode: ")
	if err != nil {
		return err
	}
	if passcode != confirm {
		return fmt.Errorf("passcodes do not match")
	}

	if err := hctx.svc.Setup(cmd.Context(), hctx.caller, passcode); err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), "passcode set")
	return nil
}

// newHiddenChangeCmd returns the `fotobank hidden change` subcommand.
func newHiddenChangeCmd() *cobra.Command {
	var cfgPath string
	cmd := &cobra.Command{
		Use:   "change",
		Short: "Change the hidden-privacy passcode",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runHiddenChange(cmd, cfgPath)
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", "", "path to config file")
	return cmd
}

func runHiddenChange(cmd *cobra.Command, cfgPath string) error {
	hctx, cfg, err := loadHiddenCtx(cfgPath)
	if err != nil {
		return err
	}
	defer hctx.close()
	if cfg.Identity.Mode != "stub" {
		return fmt.Errorf(
			"hidden change requires stub identity mode; got %q — use 'admin reset-hidden-passcode' instead",
			cfg.Identity.Mode)
	}

	sr := newStdinReader(cmd)
	current, err := sr.readPasscode(cmd, "Current passcode: ")
	if err != nil {
		return err
	}
	newPass, err := sr.readPasscode(cmd, "New passcode: ")
	if err != nil {
		return err
	}
	confirm, err := sr.readPasscode(cmd, "Confirm new passcode: ")
	if err != nil {
		return err
	}
	if newPass != confirm {
		return fmt.Errorf("passcodes do not match")
	}

	if err := hctx.svc.Change(cmd.Context(), hctx.caller, current, newPass); err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), "passcode changed")
	return nil
}

// newHiddenDisableCmd returns the `fotobank hidden disable` subcommand.
func newHiddenDisableCmd() *cobra.Command {
	var cfgPath string
	cmd := &cobra.Command{
		Use:   "disable",
		Short: "Disable hidden-privacy (clears hidden flags and removes passcode)",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runHiddenDisable(cmd, cfgPath)
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", "", "path to config file")
	return cmd
}

func runHiddenDisable(cmd *cobra.Command, cfgPath string) error {
	hctx, cfg, err := loadHiddenCtx(cfgPath)
	if err != nil {
		return err
	}
	defer hctx.close()
	if cfg.Identity.Mode != "stub" {
		return fmt.Errorf(
			"hidden disable requires stub identity mode; got %q — use 'admin reset-hidden-passcode' instead",
			cfg.Identity.Mode)
	}

	sr := newStdinReader(cmd)
	passcode, err := sr.readPasscode(cmd, "Current passcode: ")
	if err != nil {
		return err
	}

	fmt.Fprint(cmd.OutOrStdout(), "This will clear all hidden flags and remove your passcode. Type 'yes' to confirm: ")
	confirm, err := sr.readLine()
	if err != nil {
		return err
	}
	if confirm != "yes" {
		return fmt.Errorf("aborted")
	}

	if err := hctx.svc.Disable(cmd.Context(), hctx.caller, passcode); err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), "hidden-privacy disabled")
	return nil
}

// newAdminCmd returns the `fotobank admin` command group.
func newAdminCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "admin",
		Short: "Administrative operations (operator-only)",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			_ = cmd.Usage()
			return newUsageError("a subcommand is required")
		},
	}
	cmd.AddCommand(newAdminResetHiddenPasscodeCmd())
	return cmd
}

// newAdminResetHiddenPasscodeCmd returns `fotobank admin reset-hidden-passcode`.
func newAdminResetHiddenPasscodeCmd() *cobra.Command {
	var (
		cfgPath string
		owner   string
		confirm bool
	)
	cmd := &cobra.Command{
		Use:   "reset-hidden-passcode",
		Short: "Delete the hidden-privacy credential and revoke sessions (preserves hidden_at)",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runAdminResetHiddenPasscode(cmd, cfgPath, owner, confirm)
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", "", "path to config file")
	cmd.Flags().StringVar(&owner, "owner", "", "principal in hub:user form (required in non-stub mode)")
	cmd.Flags().BoolVar(&confirm, "confirm", false, "required — confirms the destructive reset")
	_ = cmd.MarkFlagRequired("confirm")
	return cmd
}

func runAdminResetHiddenPasscode(cmd *cobra.Command, cfgPath, ownerRaw string, confirm bool) error {
	// MarkFlagRequired only checks that the flag was provided; it does not
	// verify the value. Reject --confirm=false explicitly so a scripted
	// mistake cannot accidentally trigger the reset.
	if !confirm {
		return newUsageError("--confirm=true is required to perform a destructive reset")
	}

	hctx, cfg, err := loadHiddenCtx(cfgPath)
	if err != nil {
		return err
	}
	defer hctx.close()

	var principal owners.Principal
	if ownerRaw != "" {
		p, err := parseHubUser(ownerRaw)
		if err != nil {
			return newUsageError("%s", err.Error())
		}
		principal = p
	} else if cfg.Identity.Mode == "stub" {
		principal = hctx.caller
	} else {
		return fmt.Errorf("non-stub identity mode requires --owner hub:user")
	}

	if err := hctx.svc.AdminReset(cmd.Context(), principal); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "reset hidden passcode for %s\n", principal)
	return nil
}

// RunWithInput is like RunContext but accepts an explicit io.Reader for stdin.
// It is used by tests to inject passcode input without a real terminal.
func RunWithInput(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	root := newRootCmd()
	root.SetArgs(args)
	root.SetIn(stdin)
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
