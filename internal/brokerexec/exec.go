package brokerexec

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"go.kenn.io/fotobank/internal/broker"
	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/share"
)

const defaultCallTimeout = 30 * time.Second

// Registrar implements broker.BrokerClient by shelling out to a
// configurable broker CLI. The registrar is concurrency-safe: it
// holds no per-call mutable state outside the call's own stack.
//
// Wire contract:
//
//   - Each call invokes Config.Command with the configured publish or
//     revoke args plus a JSON object on stdin (see payload.go for the
//     shape). schema_version starts at 1.
//   - Exit code 0 = success; 65 = permanent (broker.ErrBrokerPermanent);
//     75 = transient (broker.ErrBrokerTransient); any other non-zero
//     code is treated as transient.
//   - Per-call timeout (Config.CallTimeout, default 30s) is enforced
//     by context.WithTimeout. Timeouts surface as
//     broker.ErrBrokerTransient, not context.DeadlineExceeded.
//   - Caller-context cancellation is propagated as-is so the share
//     worker's isCtxErr suppression matches.
//   - Env entries are validated as KEY=VALUE in New; per call the env
//     is os.Environ() overlayed by configured keys in sorted order
//     (deterministic for tests).
type Registrar struct {
	command          string
	publishScopeArgs []string
	revokeScopeArgs  []string
	callTimeout      time.Duration
	envOverride      map[string]string
	logger           *slog.Logger
	runCmd           runFunc
}

// Config is the constructor input for New. CallTimeout=0 picks the
// 30s default. Logger=nil picks a discard logger. Env entries must
// each be KEY=VALUE with non-empty key; New rejects malformed input.
type Config struct {
	Command          string
	PublishScopeArgs []string
	RevokeScopeArgs  []string
	CallTimeout      time.Duration
	Env              []string
	Logger           *slog.Logger
}

// runFunc is the unexported test seam. The production default
// (realRun) and its consumers (PublishScope, RevokeScope, execOnce)
// land in a follow-up change; same-package tests that need to inject
// behaviour assign a fake to Registrar.runCmd directly.
type runFunc func(
	ctx context.Context,
	command string,
	args []string,
	env []string,
	stdin io.Reader,
	stdout io.Writer,
	stderr io.Writer,
) (exitCode int, err error)

// New validates cfg and returns a Registrar ready to call. Returns
// errs.ErrBadConfiguration for Command-missing or malformed Env.
func New(cfg Config) (*Registrar, error) {
	if strings.TrimSpace(cfg.Command) == "" {
		return nil, fmt.Errorf("%w: brokerexec: command is required",
			errs.ErrBadConfiguration)
	}
	env, err := parseEnv(cfg.Env)
	if err != nil {
		return nil, err
	}
	timeout := cfg.CallTimeout
	if timeout == 0 {
		timeout = defaultCallTimeout
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Registrar{
		command:          cfg.Command,
		publishScopeArgs: append([]string(nil), cfg.PublishScopeArgs...),
		revokeScopeArgs:  append([]string(nil), cfg.RevokeScopeArgs...),
		callTimeout:      timeout,
		envOverride:      env,
		logger:           logger,
	}, nil
}

// parseEnv validates KEY=VALUE entries and returns them as a map.
// Empty keys ("=value") and entries without "=" are rejected with
// errs.ErrBadConfiguration.
func parseEnv(entries []string) (map[string]string, error) {
	out := make(map[string]string, len(entries))
	for _, e := range entries {
		k, v, ok := strings.Cut(e, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf(
				"%w: brokerexec: env entry %q must be KEY=VALUE with non-empty key",
				errs.ErrBadConfiguration, e)
		}
		out[k] = v
	}
	return out, nil
}

// buildEnv returns os.Environ() overlayed with envOverride in
// sorted-key order so test output is deterministic.
func (r *Registrar) buildEnv() []string {
	base := os.Environ()
	if len(r.envOverride) == 0 {
		return base
	}
	seen := make(map[string]int, len(base))
	for i, e := range base {
		if k, _, ok := strings.Cut(e, "="); ok {
			seen[k] = i
		}
	}
	keys := make([]string, 0, len(r.envOverride))
	for k := range r.envOverride {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		kv := k + "=" + r.envOverride[k]
		if i, ok := seen[k]; ok {
			base[i] = kv
		} else {
			base = append(base, kv)
		}
	}
	return base
}

// PublishScope satisfies broker.BrokerClient. See package godoc for
// the wire contract.
func (r *Registrar) PublishScope(ctx context.Context, s share.Scope) error {
	return r.execOnce(ctx, "publish", r.publishScopeArgs, newPublishRequest(s))
}

// RevokeScope satisfies broker.BrokerClient. Revocation is keyed by
// UUID only; the broker is membership-ignorant.
func (r *Registrar) RevokeScope(ctx context.Context, uuid string) error {
	return r.execOnce(ctx, "revoke", r.revokeScopeArgs, newRevokeRequest(uuid))
}

// execOnce orchestrates one shell-out: marshal payload, build env,
// run the broker CLI, capture stderr, classify result.
func (r *Registrar) execOnce(ctx context.Context, op string,
	args []string, payload any) error {

	cmdCtx, cancel := context.WithTimeout(ctx, r.callTimeout)
	defer cancel()

	var stdinBuf bytes.Buffer
	if err := json.MarshalWrite(&stdinBuf, payload); err != nil {
		return fmt.Errorf("brokerexec %s: marshal payload: %w", op, err)
	}

	stderrBuf := newPrefixBuffer(4096)
	runner := r.runCmd
	if runner == nil {
		runner = realRun
	}
	exitCode, runErr := runner(
		cmdCtx, r.command, args, r.buildEnv(),
		bytes.NewReader(stdinBuf.Bytes()),
		io.Discard,
		stderrBuf,
	)

	// 1. Caller (worker) ctx cancelled — propagate as-is so the
	// worker's isCtxErr matches and the drain aborts cleanly.
	if ctx.Err() != nil {
		return fmt.Errorf("brokerexec %s: %w", op, ctx.Err())
	}
	// 2. Per-call timeout fired (caller ctx still healthy) —
	// classify as transient. Propagating DeadlineExceeded would
	// suppress recordFailure in the share worker, leaving the row
	// permanently stuck.
	if errors.Is(cmdCtx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("brokerexec %s: timed out after %s: %w",
			op, r.callTimeout, broker.ErrBrokerTransient)
	}
	// 3. Success.
	if exitCode == 0 && runErr == nil {
		return nil
	}
	// 4. Failure — log full stderr and classify by exit code.
	full := stderrBuf.Bytes()
	r.logger.Error("brokerexec failed",
		"op", op, "exit", exitCode, "stderr", string(full))
	return classifyExit(op, exitCode, tailForError(full), runErr)
}

// realRun is the default runFunc: builds an exec.Cmd from the
// supplied parameters, runs it, returns the exit code (or -1 if the
// process never started). All io wiring (env, stdin, stdout,
// stderr) is set on the Cmd; nothing else.
func realRun(ctx context.Context, command string, args, env []string,
	stdin io.Reader, stdout, stderr io.Writer) (int, error) {

	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Env = env
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	if cmd.ProcessState != nil {
		return cmd.ProcessState.ExitCode(), err
	}
	return -1, err
}

var _ broker.BrokerClient = (*Registrar)(nil)
