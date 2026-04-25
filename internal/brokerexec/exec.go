package brokerexec

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/wesm/fotobank/internal/errs"
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
//
// PublishScope and RevokeScope are added in a follow-up change; this
// file currently lands the foundation only.
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
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
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
