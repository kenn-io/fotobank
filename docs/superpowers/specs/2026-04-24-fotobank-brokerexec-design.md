# exec.BrokerRegistrar Design

**Status:** Draft (2026-04-24)
**Owner:** wesm
**Replaces:** placeholder `[broker.exec].register_scope_args` field in `internal/config/config.go` (no consumers).

## Goal

Ship the first non-stub implementation of `broker.BrokerClient` so fotobank can register and revoke scopes against an external broker. The implementation shells out to a configurable broker CLI, communicating via JSON on stdin and sysexits-compatible exit codes. Vision §6.3 reserves the name `exec.BrokerRegistrar`; this design fits inside the already-merged 2-method `BrokerClient` interface and the existing outbox worker.

## Background

The current production default is `broker.NoopBroker`, which returns `nil` for every call. The share-worker outbox (`internal/shareworker/worker.go`) is fully wired but every published scope flips straight to `broker_status='active'` with no remote effect. To make sharing actually visible to a grantee through the broker, fotobank needs a real `BrokerClient` whose calls have side effects.

The vision spec (§6.3) sketches a 3-method `BrokerRegistrar` interface (`RegisterScope`, `CreateGrant`, `RevokeGrant`). The merged code collapses register+grant into one `PublishScope` call. The fine-grained progress columns survive — `share.Repo.MarkPublished` writes both `broker_registered_at` and `broker_granted_at` with `COALESCE` — so the registrar implementation can choose to make one or two CLI calls under the hood without changing the worker contract.

## Non-goals

- A native broker protocol implementation (gRPC, HTTP). Future work; out of scope.
- Probe-then-create idempotency layering. Vision spec calls non-idempotent broker CLIs a configuration error; v1 trusts the CLI and adds probe support later if a real CLI needs it.
- Reading anything from broker stdout. v1 classifies success/failure purely by exit code.
- Stat or `--version`-probe the broker CLI at server startup.
- Generic env-var interpolation in TOML. Operators export secrets through their service manager; fotobank inherits via `os.Environ()`.

## Architecture

### Package layout

New sibling package `internal/brokerexec/` keeps `os/exec` + `encoding/json` out of the small interface package `internal/broker/`. Existing layout stays:

- `internal/broker/` — interface, sentinels, `NoopBroker`. No new imports.
- `internal/brokerexec/` — `Registrar`, `Config`, `New`. Imports `internal/broker` for the interface and sentinels, `internal/share` for `Scope`.
- `internal/cli/` — adds a small `newBrokerClient` dispatcher (mode → impl); imports both packages above.
- `internal/shareworker/` — unchanged.

### Public surface

```go
// internal/brokerexec/exec.go
package brokerexec

type Registrar struct {
    command     string
    publishArgs []string
    revokeArgs  []string
    callTimeout time.Duration
    envOverride map[string]string  // parsed at New, overlayed onto os.Environ() per call
    logger      *slog.Logger
    runCmd      runFunc             // unexported test seam; defaults to realRun
}

type Config struct {
    Command          string
    PublishScopeArgs []string        // may be empty if broker discriminates via payload
    RevokeScopeArgs  []string        // may be empty for same reason
    CallTimeout      time.Duration   // 0 → 30s
    Env              []string        // KEY=VALUE pairs merged on top of os.Environ()
    Logger           *slog.Logger    // nil → discard
}

func New(cfg Config) (*Registrar, error)
func (r *Registrar) PublishScope(ctx context.Context, s share.Scope) error
func (r *Registrar) RevokeScope(ctx context.Context, uuid string) error

var _ broker.BrokerClient = (*Registrar)(nil)
```

`New` requires `Command` non-empty, validates each `Env` entry as `KEY=VALUE` with non-empty key, and stores parsed overrides as `map[string]string`. Publish/revoke args may both be empty; the broker CLI is expected to discriminate via the JSON payload's `operation` field if needed.

The interface invariant — that publish and revoke are distinguishable on the broker side — is the operator's responsibility. Fotobank guarantees only that a `PublishScope` call always sends `"operation":"publish"` and `RevokeScope` always sends `"operation":"revoke"`.

## Wire contract

### Invocation

For each `PublishScope(ctx, scope)` or `RevokeScope(ctx, uuid)` call:

1. Build `cmdCtx, cancel := context.WithTimeout(ctx, r.callTimeout)`
2. `cmd := exec.CommandContext(cmdCtx, r.command, args...)` where `args` is `r.publishArgs` or `r.revokeArgs`
3. `cmd.Env = r.buildEnv()` — `os.Environ()` overlayed by configured overrides in sorted-key order (deterministic for tests; see Env merge semantics below)
4. Marshal payload to a `bytes.Buffer` via `json.NewEncoder` (gives a trailing newline → NDJSON-friendly)
5. `cmd.Stdin = bytes.NewReader(buf.Bytes())`
6. `cmd.Stdout = io.Discard`
7. `cmd.Stderr = newPrefixBuffer(4096)` — sliding window keeps the **last** 4 KiB
8. Call the unexported `runCmd` (defaults to `cmd.Run()`)
9. Map the result via the rules below

### Stdin payloads

**Publish:**

```json
{
  "schema_version": 1,
  "operation": "publish",
  "scope": {
    "uuid": "9a3f...",
    "owner":   { "hub": "h.example", "user_id": "alice" },
    "grantee": { "hub": "h.example", "user_id": "bob"   },
    "allow_download": true,
    "expires_at": "2026-05-01T00:00:00Z",
    "label": "Trip — May"
  }
}
```

`expires_at` is omitted when the scope has no expiry.

**Revoke:**

```json
{
  "schema_version": 1,
  "operation": "revoke",
  "uuid": "9a3f..."
}
```

`schema_version` and `operation` are belt-and-suspenders: even if `publish_scope_args` and `revoke_scope_args` are configured identically, the broker CLI can dispatch on the payload. Fotobank bumps `schema_version` on incompatible field changes.

**Membership stays out.** `target_type`, `album_id`, and `media_ids` are deliberately not sent. Per vision §6.3, the broker is membership-ignorant; fotobank enforces what's inside the scope. This keeps the broker contract minimal and the registrar usable for both `album_live` and `media_set` targets without divergence.

### Exit code → error mapping

Exit codes are sysexits-compatible.

| Code | Meaning | Returned error |
|------|---------|----------------|
| 0 | Success | `nil` |
| 65 | Permanent — `EX_DATAERR`, broker rejected the scope | wraps `broker.ErrBrokerPermanent` |
| 75 | Transient — `EX_TEMPFAIL`, retry later | wraps `broker.ErrBrokerTransient` |
| Any other (incl. 127 / signal kills) | Default transient | wraps `broker.ErrBrokerTransient`, includes `exit=N` |

Error message format: `brokerexec <op>: <stderr-tail>: <sentinel>` (e.g., `brokerexec publish: scope already exists: broker: permanent error`).

These codes are fotobank brokerexec-specific; documented in the package godoc and the example TOML so broker authors have one place to look.

### Stderr capture & normalization

A bounded `prefixBuffer{cap: 4096}` collects stderr as a sliding window — keeps the *last* 4 KiB on overflow, not the first. On error, the last ≤256 bytes are extracted and normalized:

```go
func tailForError(b []byte) string {
    if len(b) > 256 {
        b = b[len(b)-256:]
    }
    s := string(b)
    s = strings.Map(func(r rune) rune {
        if r == '\t' { return ' ' }
        if r < 0x20 || r == 0x7f { return -1 }   // strip control bytes
        return r
    }, s)
    s = strings.Join(strings.Fields(s), " ")     // collapse whitespace
    return s
}
```

The full 4 KiB is also passed to `r.logger.Error("brokerexec failed", "stderr", full, ...)` for diagnostics; only the normalized tail flows into `err.Error()` → `scopes.broker_last_error` (visible in `fotobank shares list --status=failed` and `GET /api/v1/shares/{uuid}`).

### Context mapping

After `cmd.Run()`, distinguish three cases in this order:

```go
err := cmd.Run()

// 1. Parent (worker) ctx cancelled → propagate as-is. The worker's
// isCtxErr matches, recordFailure is skipped, and the drain aborts.
if pctx.Err() != nil {
    return fmt.Errorf("brokerexec %s: %w", op, pctx.Err())
}

// 2. Per-call timeout triggered (parent ctx still healthy) →
// classify as transient. NOT propagated as DeadlineExceeded, because
// the worker's isCtxErr would suppress recordFailure → broker_attempts
// never increments → MaxBrokerAttempts never trips → silent stall.
if cmdCtx.Err() == context.DeadlineExceeded {
    return fmt.Errorf("brokerexec %s: timed out after %s: %w",
        op, r.callTimeout, broker.ErrBrokerTransient)
}

// 3. Otherwise classify exit code from err / *exec.ExitError.
```

This split is load-bearing: the worker's `isCtxErr` (worker.go:158) treats `Canceled` and `DeadlineExceeded` identically, both aborting the drain *and* skipping `recordFailure`. That's right for shutdown; wrong for a per-call timeout.

## Config schema

```toml
[broker]
mode = "exec"

[broker.exec]
command            = "/usr/local/bin/fb-broker"
publish_scope_args = ["scope", "publish"]
revoke_scope_args  = ["scope", "revoke"]
call_timeout       = "30s"                    # optional; default 30s
env                = ["FB_BROKER_ENV=prod"]   # optional; merged on os.Environ()
```

### Go struct

```go
// internal/config/config.go
type BrokerExec struct {
    Command          string        `toml:"command"`
    PublishScopeArgs []string      `toml:"publish_scope_args"`
    RevokeScopeArgs  []string      `toml:"revoke_scope_args"`
    CallTimeout      time.Duration `toml:"call_timeout"`
    Env              []string      `toml:"env"`
}
```

The placeholder `RegisterScopeArgs` field (and its `register_scope_args` TOML key) is removed in the same change. No back-compat shim — the field has zero consumers.

### Defaults

In `applyDefaults`: `if c.Broker.Exec.CallTimeout == 0 { c.Broker.Exec.CallTimeout = 30 * time.Second }`.

### Validation

In `Config.Validate`'s `case "exec"` arm:

```go
case "exec":
    if strings.TrimSpace(c.Broker.Exec.Command) == "" {
        return fmt.Errorf("%w: [broker.exec].command is required when mode=exec",
            errs.ErrBadConfiguration)
    }
    // call_timeout: 0 is allowed because applyDefaults has already run
    // by this point and replaced 0 with 30s. Negative values are rejected
    // so a future refactor that moves Validate ahead of applyDefaults
    // doesn't silently accept them.
    if c.Broker.Exec.CallTimeout < 0 {
        return fmt.Errorf("%w: [broker.exec].call_timeout must be ≥ 0",
            errs.ErrBadConfiguration)
    }
```

`PublishScopeArgs` / `RevokeScopeArgs` are optional. The `Env` slice format check (`KEY=VALUE` with non-empty key) lives in `brokerexec.New`, not in `config.Validate`, to avoid a `config → brokerexec` import or a duplicated parser.

### Secrets policy

The `env` array is for **non-secret** overrides like `FB_BROKER_ENV=prod`. Secrets (tokens, keys) should be exported by the service manager (systemd, launchd) so they reach fotobank via `os.Environ()` inheritance. Fotobank does not interpolate `$VAR` in TOML — strings are taken verbatim and passed to the child literally. Documented in the package godoc and example TOML.

### Env merge semantics

Implemented in `brokerexec.New` (parser) and `Registrar.buildEnv` (per-call composer):

```go
// brokerexec/exec.go
func parseEnv(entries []string) (map[string]string, error) {
    out := make(map[string]string, len(entries))
    for _, e := range entries {
        k, v, ok := strings.Cut(e, "=")
        if !ok || k == "" {
            return nil, fmt.Errorf("brokerexec: env entry %q must be KEY=VALUE with non-empty key", e)
        }
        out[k] = v
    }
    return out, nil
}

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
    sort.Strings(keys)                        // deterministic for tests
    for _, k := range keys {
        kv := k + "=" + r.envOverride[k]
        if i, ok := seen[k]; ok {
            base[i] = kv                      // overlay in place
        } else {
            base = append(base, kv)           // new keys append in sorted order
        }
    }
    return base
}
```

Configured keys overlay inherited ones in place (preserves relative order); new keys append in sorted order. No duplicate-key footgun, no map-iteration-order leakage into tests.

## Server wiring

Today the share worker is constructed with a hard-coded `broker.NoopBroker{}` at `internal/cli/server.go:227`. The change is local: hoist the logger, dispatch on mode, and pass the resulting client.

```go
// internal/cli/server.go (replaces the inline logger at line 228)
logger := slog.New(slog.NewTextHandler(opts.stderr, nil))

brokerClient, err := newBrokerClient(cfg.Broker, logger)
if err != nil {
    return fmt.Errorf("broker init: %w", err)
}

shareCfg := shareworker.Config{
    Repo:   sharesRepo,
    Broker: brokerClient,
    Logger: logger,
}
// existing FOTOBANK_TEST_SHARE_WORKER_TICK block unchanged
```

```go
// internal/cli/broker.go (new ~25-line helper file)
func newBrokerClient(cfg config.Broker, logger *slog.Logger) (broker.BrokerClient, error) {
    switch cfg.Mode {
    case "stub":
        return broker.NoopBroker{}, nil
    case "exec":
        return brokerexec.New(brokerexec.Config{
            Command:          cfg.Exec.Command,
            PublishScopeArgs: cfg.Exec.PublishScopeArgs,
            RevokeScopeArgs:  cfg.Exec.RevokeScopeArgs,
            CallTimeout:      cfg.Exec.CallTimeout,
            Env:              cfg.Exec.Env,
            Logger:           logger,
        })
    default:
        return nil, fmt.Errorf("unknown broker mode %q", cfg.Mode)
    }
}
```

`config.Validate` already rejects unknown modes; the `default` arm is defense-in-depth.

### Logger sharing

Same `slog.Logger` instance reaches `shareCfg.Logger` and `brokerexec.Config.Logger`. Registrar `Error("brokerexec failed", ...)` and worker `Error("shareworker tick failed", ...)` interleave cleanly, both to `opts.stderr`.

### Lifecycle

`Registrar` holds no resources between calls — every `PublishScope` / `RevokeScope` spawns a fresh child, drains stderr to a bounded buffer, and returns. No `Close()`, no goroutines, no long-lived state. When the worker exits on ctx cancel, the registrar is GC'd along with it.

### Failure-class table (operator-facing docs)

| Failure | When detected | Outcome | Where the operator sees it |
|---------|---------------|---------|----------------------------|
| Malformed `[broker.exec].env` (`"=v"`, `"NO_EQ"`) | `brokerexec.New` at boot | **Boot-fatal.** | stderr at startup |
| Missing `[broker.exec].command` | `config.Validate` at boot | **Boot-fatal.** | stderr at startup |
| Negative `call_timeout` | `config.Validate` at boot | **Boot-fatal.** | stderr at startup |
| `command` not executable / not found | First `PublishScope`/`RevokeScope` call | **Transient.** Retried up to `MaxBrokerAttempts=10`, then `MarkFailed` with `exec: ... not found` in `broker_last_error`. | `fotobank shares list --status=failed`, `GET /api/v1/shares/{uuid}` |
| Broker CLI returns 65 | Per-call | **Permanent.** Immediate `MarkFailed`. | Same |
| Broker CLI returns 75 / any other non-zero | Per-call | **Transient.** Retried up to `MaxBrokerAttempts`. | Same |
| Per-call timeout (default 30s) | Per-call | **Transient** — returned as `ErrBrokerTransient`, *not* as `DeadlineExceeded`. | Same |

## Worker integration

Zero changes to `internal/shareworker`. The worker already calls `BrokerClient.PublishScope(ctx, scope)` and `RevokeScope(ctx, uuid)` (worker.go:111, 126); the progress columns are written by `share.Repo.MarkPublished` (idempotent via `COALESCE`); transient/permanent classification flows through `errors.Is(err, broker.ErrBrokerPermanent)` (worker.go:141).

The registrar's per-call exec composes with the worker's per-row state machine without touching shareworker.

## Testing strategy

Three layers. Most assertions live at layer A; B verifies the contract holds against real `os/exec`; C is one end-to-end test that proves wiring + worker + DB compose.

### Layer A — unit tests with the runner seam (`brokerexec/exec_test.go`)

Same-package tests swap the unexported `runFunc`; no process spawned.

```go
type runFunc func(ctx context.Context, cmd string, args, env []string,
                  stdin io.Reader, stdout, stderr io.Writer) (exitCode int, err error)
```

Cases:

| Test | Setup | Asserts |
|------|-------|---------|
| `TestPublishScopeSuccess` | exit 0 | returns nil; stdin JSON has `schema_version`, `operation:"publish"`, all scope fields |
| `TestPublishScopeExitPermanent` | exit 65, stderr "scope rejected" | `errors.Is(err, broker.ErrBrokerPermanent)`; tail in error |
| `TestPublishScopeExitTransient` | exit 75 | `errors.Is(err, broker.ErrBrokerTransient)` |
| `TestPublishScopeExitUnknown` | exit 1 | `errors.Is(err, broker.ErrBrokerTransient)`; error message contains `exit=1` |
| `TestPublishScopeStderrTruncated` | stderr 8 KiB | wrapped error tail ≤256 bytes; full 8 KiB visible to logger (assert via captured slog handler) |
| `TestPublishScopeStderrNormalized` | stderr `"hello\x00\nworld\x07\t!"` | error tail is `"hello world !"` |
| `TestPublishScopeOmitsExpiresAt` | scope `ExpiresAt == nil` | stdin JSON has no `expires_at` key |
| `TestPublishScopeRedactsMembership` | scope `TargetType=media_set, MediaIDs=[…]` | stdin JSON has no `target_type` / `album_id` / `media_ids` |
| `TestRevokeScopePayload` | uuid `"abc"` | stdin JSON is exactly `{"schema_version":1,"operation":"revoke","uuid":"abc"}`; uses `revokeArgs` not `publishArgs` |
| `TestParentCtxCancellation` | runFunc returns `context.Canceled`; pctx cancelled before call | wrapped err satisfies `errors.Is(err, context.Canceled)` so worker `isCtxErr` matches |
| `TestPerCallTimeoutClassifiedTransient` | runFunc blocks past `callTimeout=10ms` | `errors.Is(err, broker.ErrBrokerTransient)` AND **not** `errors.Is(err, context.DeadlineExceeded)` |
| `TestEnvOverlay` | base `FB_X=base`, override `FB_X=override`, override `FB_NEW=v` | `cmd.Env` has `FB_X=override` (in place), `FB_NEW=v` appended after sort |
| `TestEnvParseRejectsEmptyKey` | `New` with `Env: ["=value"]` | error wrapping `errs.ErrBadConfiguration`; mentions `KEY=VALUE` |
| `TestEnvParseRejectsMissingEq` | `New` with `Env: ["NO_EQUALS"]` | same |

### Layer B — end-to-end against a real helper binary (`brokerexec/exec_e2e_test.go`)

Re-exec the test binary as the broker CLI via the `TestHelperProcess` pattern:

```go
func TestMain(m *testing.M) {
    if os.Getenv("BROKEREXEC_TEST_HELPER") == "1" {
        runHelper()
        return
    }
    os.Exit(m.Run())
}

func runHelper() {
    payload, _ := io.ReadAll(os.Stdin)
    if want := os.Getenv("BROKEREXEC_TEST_EXPECT_OPERATION"); want != "" {
        var got struct{ Operation string `json:"operation"` }
        _ = json.Unmarshal(payload, &got)
        if got.Operation != want {
            fmt.Fprintf(os.Stderr, "operation mismatch: got=%q want=%q", got.Operation, want)
            os.Exit(65)
        }
    }
    if msg := os.Getenv("BROKEREXEC_TEST_STDERR"); msg != "" {
        fmt.Fprint(os.Stderr, msg)
    }
    if d := os.Getenv("BROKEREXEC_TEST_SLEEP"); d != "" {
        if dur, err := time.ParseDuration(d); err == nil {
            time.Sleep(dur)
        }
    }
    code, _ := strconv.Atoi(os.Getenv("BROKEREXEC_TEST_EXIT"))
    os.Exit(code)
}
```

Cases:

| Test | Helper env | Asserts |
|------|------------|---------|
| `TestE2EPublishSuccess` | `EXIT=0, EXPECT_OPERATION=publish` | `PublishScope` returns nil; payload reaches helper through stdin |
| `TestE2EPermanentExit` | `EXIT=65, STDERR="scope already exists"` | `errors.Is(err, broker.ErrBrokerPermanent)`; error contains "scope already exists" |
| `TestE2ETimeoutKillsChild` | `SLEEP=5s, EXIT=0`; `CallTimeout=50ms` | within ~100ms, returns `ErrBrokerTransient` (not `DeadlineExceeded`); child gone |
| `TestE2EParentCtxCancelKillsChild` | `SLEEP=5s`; cancel pctx after 50ms | returns `context.Canceled`-wrapping err; child gone |
| `TestE2EEnvReachesChild` | helper echoes `FB_BROKER_ENV` to stderr; `Env: ["FB_BROKER_ENV=prod"]`, `EXIT=65` | error tail contains `prod` |

Skipped under `-short`:

```go
if testing.Short() { t.Skip("e2e exec tests skipped under -short") }
```

### Layer C — single integration test (`internal/cli/e2e_brokerexec_test.go`)

Boots a real fotobank server with `mode = "exec"` and the test helper binary as `command`. Exercises the full path:

1. Owner POSTs `/api/v1/shares` → row inserted with `broker_status='pending'`.
2. Test calls `shareW.RunOnce(ctx)` directly (skips the 15s tick).
3. Assert: `broker_status='active'`, `broker_registered_at` and `broker_granted_at` set.
4. Owner DELETEs the share → row goes to `broker_status='revoking'`.
5. Another `RunOnce` → row reaches `broker_status='revoked_remote'`, `broker_revoked_at` set.

Proves wiring + worker + registrar + DB state machine all compose. No analogous test added to `internal/shareworker` — its existing tests use a fake `BrokerClient` and cover the worker state machine on its own.

### Config tests (`internal/config/config_test.go`)

- `TestBrokerExecRequiresCommand` — `mode=exec`, no `command` → `errs.ErrBadConfiguration`.
- `TestBrokerExecDefaultsCallTimeout` — `mode=exec`, `command` set, no `call_timeout` → loaded value is 30s.
- `TestBrokerExecAcceptsArgsless` — `mode=exec`, `command` set, no publish/revoke args → loads OK.
- `TestBrokerExecRejectsNegativeTimeout` — `call_timeout="-1s"` → `errs.ErrBadConfiguration`.
- `TestExplicitTOMLValuesWinOverDefaults` — existing test; add `[broker.exec]\ncommand = "/bin/true"` to its fixture so `mode=exec` validates.

## Coordinated changes (single commit/PR)

1. `internal/config/config.go` — rename `BrokerExec.RegisterScopeArgs` → split `PublishScopeArgs`/`RevokeScopeArgs`; add `CallTimeout`, `Env`; add default + validation arm.
2. `internal/config/config.example.toml` — drop `register_scope_args` line; add `command`, `publish_scope_args`, `revoke_scope_args`, `call_timeout`, `env` with the safe `FB_BROKER_ENV=prod` example.
3. `docs/superpowers/specs/2026-04-22-fotobank-go-core-phase1-design.md` — update the broker config snippet.
4. `docs/superpowers/plans/2026-04-22-fotobank-go-core-plan-a-foundation.md` — update the broker config snippet.
5. `internal/config/config_test.go::TestExplicitTOMLValuesWinOverDefaults` — add `command = "/bin/true"` to its TOML fixture.
6. New tests as enumerated in §Config tests.
7. New package `internal/brokerexec/` with `exec.go`, `exec_test.go`, `exec_e2e_test.go`.
8. `internal/cli/broker.go` — new dispatcher.
9. `internal/cli/server.go` — hoist logger, replace `broker.NoopBroker{}` with `newBrokerClient` call, wire boot-fatal error path.
10. `internal/cli/e2e_brokerexec_test.go` — single integration test.

## Open questions

None at the time of writing. Decisions:
- Wire shape: JSON on stdin, static args for verb selection. (User-confirmed.)
- Error classification: sysexits 65/75 + transient default. (User-confirmed.)
- Per-call timeout: 30s, operator-tunable. (User-confirmed.)
- Env merge: overlay-by-key, deterministic. (User-confirmed.)
- Per-call timeout NOT propagated as `DeadlineExceeded`: classified as transient so attempts increment. (User-confirmed.)
