# brokerexec Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship `internal/brokerexec.Registrar`, the first non-stub `broker.BrokerClient` — it shells out to a configurable broker CLI to publish and revoke scopes, replacing `broker.NoopBroker` when `[broker].mode = "exec"`.

**Architecture:** Three-tier package layout. `internal/brokerexec` owns the registrar (`os/exec` + `encoding/json`), depending only on `internal/broker` for the interface and `internal/share` for the scope type. `internal/cli` adds a small mode dispatcher that returns `broker.NoopBroker` for `stub` and a `*brokerexec.Registrar` for `exec`. Wire contract: JSON-on-stdin, sysexits-style exit codes (0 ok, 65 permanent, 75 transient, anything else transient), 30 s default per-call timeout, env merged onto `os.Environ()` by sorted-key overlay. Same-package tests swap an unexported `runFunc` seam; end-to-end tests re-exec the test binary itself as the broker CLI via a shared `internal/testutil/brokerhelper` package.

**Tech Stack:** Go 1.26, `modernc.org/sqlite`, `slog`, `BurntSushi/toml`, `testify/require`. No new external dependencies.

**Spec:** `docs/superpowers/specs/2026-04-24-fotobank-brokerexec-design.md`. Read this first for the wire contract and invariants.

**Local conventions to follow:**
- Errors: wrap with `fmt.Errorf("doing X: %w", err)`. Sentinels live in `internal/errs/` and `internal/broker/`.
- Tests: `testify/require` only. Table-driven where it pays.
- Commits: imperative subject ≤72 chars; commit directly to master per `CLAUDE.md`. No feature branches.
- Run `make lint` and `go test ./...` before each commit; fix every warning.

---

## File structure

| Path | Status | Responsibility |
|------|--------|----------------|
| `internal/brokerexec/exec.go` | new | `Registrar`, `Config`, `New`, env parsing, `runFunc` type, `realRun`, `PublishScope`, `RevokeScope`, `execOnce` orchestrator |
| `internal/brokerexec/payload.go` | new | JSON payload types and builders (`publishRequest`, `revokeRequest`, `newPublishRequest`, `newRevokeRequest`) |
| `internal/brokerexec/buffer.go` | new | `prefixBuffer` — sliding-window byte buffer that retains the last N bytes |
| `internal/brokerexec/classify.go` | new | `classifyExit` and `tailForError` (exit code → sentinel mapping; stderr normalization) |
| `internal/brokerexec/exec_test.go` | new | Layer A unit tests (fake `runFunc`) |
| `internal/brokerexec/exec_e2e_test.go` | new | Layer B end-to-end tests (real `os/exec` against the test binary as helper) |
| `internal/testutil/brokerhelper/helper.go` | new | Shared helper logic; `IsHelper()` + `Run()` reusable from any test binary's `TestMain` |
| `internal/cli/broker.go` | new | `newBrokerClient(cfg config.Broker, logger *slog.Logger)` mode dispatcher |
| `internal/cli/broker_test.go` | new | Unit tests for the dispatcher |
| `internal/cli/e2e_brokerexec_test.go` | new | Layer C single integration test (server + worker + DB) |
| `internal/config/config.go` | modify | Replace `BrokerExec.RegisterScopeArgs`; add `PublishScopeArgs`, `RevokeScopeArgs`, `CallTimeout`, `Env`; add default + validation arm |
| `internal/config/config.example.toml` | modify | Replace the placeholder `[broker.exec]` block with the new schema |
| `internal/config/config_test.go` | modify | Update `TestExplicitTOMLValuesWinOverDefaults`; add 4 new tests |
| `internal/cli/server.go` | modify | Hoist `logger`, replace hard-coded `broker.NoopBroker{}` with `newBrokerClient(...)`, propagate boot-fatal errors |
| `docs/superpowers/specs/2026-04-22-fotobank-go-core-phase1-design.md` | modify | Update the broker config snippet to the new schema |
| `docs/superpowers/plans/2026-04-22-fotobank-go-core-plan-a-foundation.md` | modify | Same |

---

## Task 1: Migrate config schema

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config.example.toml`
- Modify: `internal/config/config_test.go`

- [ ] **Step 1: Write the four failing config tests**

Append to `internal/config/config_test.go`:

```go
func TestBrokerExecRequiresCommand(t *testing.T) {
    tmp := t.TempDir()
    p := filepath.Join(tmp, "c.toml")
    require.NoError(t, os.WriteFile(p, []byte(`
[nas]
root = "/tmp/nas"
[broker]
mode = "exec"
`), 0o600))
    _, err := config.Load(p)
    require.Error(t, err)
    require.ErrorIs(t, err, errs.ErrBadConfiguration)
}

func TestBrokerExecDefaultsCallTimeout(t *testing.T) {
    tmp := t.TempDir()
    p := filepath.Join(tmp, "c.toml")
    require.NoError(t, os.WriteFile(p, []byte(`
[nas]
root = "/tmp/nas"
[broker]
mode = "exec"
[broker.exec]
command = "/usr/local/bin/fb-broker"
`), 0o600))
    cfg, err := config.Load(p)
    require.NoError(t, err)
    require.Equal(t, 30*time.Second, cfg.Broker.Exec.CallTimeout)
}

func TestBrokerExecAcceptsArgsless(t *testing.T) {
    tmp := t.TempDir()
    p := filepath.Join(tmp, "c.toml")
    require.NoError(t, os.WriteFile(p, []byte(`
[nas]
root = "/tmp/nas"
[broker]
mode = "exec"
[broker.exec]
command = "/usr/local/bin/fb-broker"
`), 0o600))
    cfg, err := config.Load(p)
    require.NoError(t, err)
    require.Empty(t, cfg.Broker.Exec.PublishScopeArgs)
    require.Empty(t, cfg.Broker.Exec.RevokeScopeArgs)
}

func TestBrokerExecRejectsNegativeTimeout(t *testing.T) {
    tmp := t.TempDir()
    p := filepath.Join(tmp, "c.toml")
    require.NoError(t, os.WriteFile(p, []byte(`
[nas]
root = "/tmp/nas"
[broker]
mode = "exec"
[broker.exec]
command = "/usr/local/bin/fb-broker"
call_timeout = "-1s"
`), 0o600))
    _, err := config.Load(p)
    require.Error(t, err)
    require.ErrorIs(t, err, errs.ErrBadConfiguration)
}
```

- [ ] **Step 2: Run the new tests and confirm they fail**

Run: `go test ./internal/config -run 'TestBrokerExec' -v`
Expected: all four tests fail (missing struct fields and/or missing validation).

- [ ] **Step 3: Update the `BrokerExec` struct in `internal/config/config.go`**

Replace the existing `BrokerExec` definition (currently around line 133-136):

```go
type BrokerExec struct {
    Command          string        `toml:"command"`
    PublishScopeArgs []string      `toml:"publish_scope_args"`
    RevokeScopeArgs  []string      `toml:"revoke_scope_args"`
    CallTimeout      time.Duration `toml:"call_timeout"`
    Env              []string      `toml:"env"`
}
```

The `RegisterScopeArgs` field is removed (zero consumers; no back-compat shim).

- [ ] **Step 4: Add the call-timeout default in `applyDefaults`**

In `internal/config/config.go`, inside `applyDefaults`, just after the existing `if c.Broker.Mode == "" { c.Broker.Mode = "stub" }` block, append:

```go
if c.Broker.Exec.CallTimeout == 0 {
    c.Broker.Exec.CallTimeout = 30 * time.Second
}
```

- [ ] **Step 5: Split the broker validation arm in `Validate`**

Replace the existing block:

```go
switch c.Broker.Mode {
case "stub", "exec":
default:
    return fmt.Errorf("%w: [broker].mode=%q (must be stub|exec)",
        errs.ErrBadConfiguration, c.Broker.Mode)
}
```

with:

```go
switch c.Broker.Mode {
case "stub":
    // nothing extra
case "exec":
    if strings.TrimSpace(c.Broker.Exec.Command) == "" {
        return fmt.Errorf("%w: [broker.exec].command is required when mode=exec",
            errs.ErrBadConfiguration)
    }
    // call_timeout: 0 is allowed because applyDefaults has already
    // run by this point and replaced 0 with 30s. Negative values
    // are rejected so a future refactor that moves Validate ahead
    // of applyDefaults doesn't silently accept them.
    if c.Broker.Exec.CallTimeout < 0 {
        return fmt.Errorf("%w: [broker.exec].call_timeout must be >= 0",
            errs.ErrBadConfiguration)
    }
default:
    return fmt.Errorf("%w: [broker].mode=%q (must be stub|exec)",
        errs.ErrBadConfiguration, c.Broker.Mode)
}
```

- [ ] **Step 6: Update `TestExplicitTOMLValuesWinOverDefaults` fixture**

In `internal/config/config_test.go`, find the TOML fixture inside `TestExplicitTOMLValuesWinOverDefaults` (around line 56-82) and add an exec command line so the now-strict `mode=exec` validation passes. Replace:

```go
[broker]
mode = "exec"
```

with:

```go
[broker]
mode = "exec"
[broker.exec]
command = "/bin/true"
```

- [ ] **Step 7: Replace the `[broker.exec]` block in `internal/config/config.example.toml`**

Find the existing `[broker]` and `[broker.exec]` block (around lines 60-67) and replace the `[broker.exec]` portion with:

```toml
[broker.exec]
# Required when mode = "exec". Path to the broker CLI executable.
# command = "/usr/local/bin/fb-broker"
#
# Optional. Static argv passed to the broker CLI; the per-call JSON
# payload (with operation = "publish" | "revoke") flows on stdin.
# publish_scope_args = ["scope", "publish"]
# revoke_scope_args  = ["scope", "revoke"]
#
# Optional. Per-call timeout, default 30s. Per-call timeouts surface
# as broker.ErrBrokerTransient, not context.DeadlineExceeded, so the
# share worker counts attempts and eventually flips to failed.
# call_timeout = "30s"
#
# Optional. Non-secret env entries merged onto os.Environ() by key
# (configured values win over inherited). Secrets should come from
# the service manager via inherited environment, not from TOML.
# env = ["FB_BROKER_ENV=prod"]
```

Leave the `[broker]` mode comment untouched.

- [ ] **Step 8: Run all config tests and the full suite**

Run: `go test ./internal/config -v`
Expected: all green, including the four new tests and the updated `TestExplicitTOMLValuesWinOverDefaults`.

Run: `go test ./... -short`
Expected: everything green; no other package depended on `BrokerExec.RegisterScopeArgs`.

- [ ] **Step 9: Lint and commit**

Run: `make lint`
Expected: clean.

```bash
git add internal/config/config.go internal/config/config.example.toml internal/config/config_test.go
git commit -m "Migrate broker.exec config schema to publish/revoke + call_timeout + env"
```

---

## Task 2: Add `prefixBuffer` (sliding-window stderr sink)

**Files:**
- Create: `internal/brokerexec/buffer.go`
- Create: `internal/brokerexec/buffer_test.go`

- [ ] **Step 1: Create `internal/brokerexec/` and write the failing test**

Create `internal/brokerexec/buffer_test.go`:

```go
package brokerexec

import (
    "bytes"
    "testing"

    "github.com/stretchr/testify/require"
)

func TestPrefixBufferKeepsLastNBytes(t *testing.T) {
    cases := []struct {
        name   string
        cap    int
        writes [][]byte
        want   []byte
    }{
        {"under cap", 10, [][]byte{[]byte("abc")}, []byte("abc")},
        {"exact cap", 4, [][]byte{[]byte("abcd")}, []byte("abcd")},
        {"single write over cap", 4, [][]byte{[]byte("abcdef")}, []byte("cdef")},
        {"multiple writes overflow", 4, [][]byte{[]byte("ab"), []byte("cdef")}, []byte("cdef")},
        {"many small writes", 3, [][]byte{
            []byte("a"), []byte("b"), []byte("c"), []byte("d"), []byte("e"),
        }, []byte("cde")},
        {"single write equal to cap multiple of input", 4, [][]byte{
            bytes.Repeat([]byte("X"), 12),
        }, bytes.Repeat([]byte("X"), 4)},
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            buf := newPrefixBuffer(tc.cap)
            for _, w := range tc.writes {
                n, err := buf.Write(w)
                require.NoError(t, err)
                require.Equal(t, len(w), n)
            }
            require.Equal(t, tc.want, buf.Bytes())
        })
    }
}
```

- [ ] **Step 2: Run the test and confirm compilation failure**

Run: `go test ./internal/brokerexec -run TestPrefixBuffer -v`
Expected: build fails — `newPrefixBuffer` and `prefixBuffer` are undefined.

- [ ] **Step 3: Create `internal/brokerexec/buffer.go`**

```go
// Package brokerexec implements broker.BrokerClient by shelling out
// to a configurable broker CLI. See the package godoc on Registrar
// for the wire contract.
package brokerexec

import "io"

// prefixBuffer is a fixed-cap byte sink that retains the LAST cap
// bytes written to it. Used to bound stderr capture without
// allocating O(input) memory on a misbehaving CLI.
type prefixBuffer struct {
    cap int
    buf []byte
}

func newPrefixBuffer(cap int) *prefixBuffer {
    return &prefixBuffer{cap: cap}
}

// Write appends b, dropping any bytes that fall outside the cap-sized
// trailing window.
func (p *prefixBuffer) Write(b []byte) (int, error) {
    n := len(b)
    if n >= p.cap {
        // Input alone exceeds cap; keep only the tail.
        p.buf = append(p.buf[:0], b[n-p.cap:]...)
        return n, nil
    }
    p.buf = append(p.buf, b...)
    if len(p.buf) > p.cap {
        // Slide the window forward: copy the trailing cap bytes to
        // the head of buf and truncate.
        copy(p.buf, p.buf[len(p.buf)-p.cap:])
        p.buf = p.buf[:p.cap]
    }
    return n, nil
}

// Bytes returns the retained tail. The returned slice aliases the
// internal buffer; callers must not mutate it after subsequent Write
// calls.
func (p *prefixBuffer) Bytes() []byte {
    return p.buf
}

var _ io.Writer = (*prefixBuffer)(nil)
```

- [ ] **Step 4: Run the test and confirm it passes**

Run: `go test ./internal/brokerexec -run TestPrefixBuffer -v`
Expected: PASS for all six sub-cases.

- [ ] **Step 5: Lint and commit**

Run: `make lint`
Expected: clean.

```bash
git add internal/brokerexec/buffer.go internal/brokerexec/buffer_test.go
git commit -m "Add prefixBuffer for bounded stderr capture in brokerexec"
```

---

## Task 3: Add JSON payload types and builders

**Files:**
- Create: `internal/brokerexec/payload.go`
- Create: `internal/brokerexec/payload_test.go`

- [ ] **Step 1: Write the failing payload tests**

Create `internal/brokerexec/payload_test.go`:

```go
package brokerexec

import (
    "encoding/json"
    "testing"
    "time"

    "github.com/stretchr/testify/require"

    "github.com/wesm/fotobank/internal/owners"
    "github.com/wesm/fotobank/internal/share"
)

func TestNewPublishRequestShape(t *testing.T) {
    expires := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
    s := share.Scope{
        UUID:          "scope-uuid",
        Owner:         owners.Principal{Hub: "h", UserID: "alice"},
        Grantee:       owners.Principal{Hub: "h", UserID: "bob"},
        AllowDownload: true,
        Label:         "Trip",
        ExpiresAt:     &expires,
    }
    req := newPublishRequest(s)
    blob, err := json.Marshal(req)
    require.NoError(t, err)

    var got map[string]any
    require.NoError(t, json.Unmarshal(blob, &got))
    require.Equal(t, float64(1), got["schema_version"])
    require.Equal(t, "publish", got["operation"])

    scope := got["scope"].(map[string]any)
    require.Equal(t, "scope-uuid", scope["uuid"])
    require.Equal(t, true, scope["allow_download"])
    require.Equal(t, "Trip", scope["label"])
    require.Equal(t, "2026-05-01T00:00:00Z", scope["expires_at"])

    owner := scope["owner"].(map[string]any)
    require.Equal(t, "h", owner["hub"])
    require.Equal(t, "alice", owner["user_id"])
    grantee := scope["grantee"].(map[string]any)
    require.Equal(t, "bob", grantee["user_id"])

    // Membership must NOT be sent.
    for _, k := range []string{"target_type", "album_id", "media_ids"} {
        _, has := scope[k]
        require.Falsef(t, has, "scope must not contain %q", k)
    }
}

func TestNewPublishRequestOmitsExpiresAtWhenNil(t *testing.T) {
    s := share.Scope{
        UUID:    "x",
        Owner:   owners.Principal{Hub: "h", UserID: "a"},
        Grantee: owners.Principal{Hub: "h", UserID: "b"},
    }
    blob, err := json.Marshal(newPublishRequest(s))
    require.NoError(t, err)

    var got map[string]any
    require.NoError(t, json.Unmarshal(blob, &got))
    scope := got["scope"].(map[string]any)
    _, has := scope["expires_at"]
    require.False(t, has, "expires_at must be omitted when nil")
}

func TestNewRevokeRequestShape(t *testing.T) {
    blob, err := json.Marshal(newRevokeRequest("abc"))
    require.NoError(t, err)

    var got map[string]any
    require.NoError(t, json.Unmarshal(blob, &got))
    require.Equal(t, float64(1), got["schema_version"])
    require.Equal(t, "revoke", got["operation"])
    require.Equal(t, "abc", got["uuid"])

    _, has := got["scope"]
    require.False(t, has, "revoke payload must not include a scope object")
}
```

- [ ] **Step 2: Run the tests and confirm compilation failure**

Run: `go test ./internal/brokerexec -run 'TestNew(Publish|Revoke)Request' -v`
Expected: build fails — `newPublishRequest` and `newRevokeRequest` undefined.

- [ ] **Step 3: Create `internal/brokerexec/payload.go`**

```go
package brokerexec

import (
    "time"

    "github.com/wesm/fotobank/internal/owners"
    "github.com/wesm/fotobank/internal/share"
)

// schemaVersion is bumped on incompatible changes to the wire JSON.
// Kept as a single int so a broker CLI can fail fast on mismatch.
const schemaVersion = 1

type publishRequest struct {
    SchemaVersion int          `json:"schema_version"`
    Operation     string       `json:"operation"`
    Scope         publishScope `json:"scope"`
}

type publishScope struct {
    UUID          string           `json:"uuid"`
    Owner         publishPrincipal `json:"owner"`
    Grantee       publishPrincipal `json:"grantee"`
    AllowDownload bool             `json:"allow_download"`
    ExpiresAt     *time.Time       `json:"expires_at,omitempty"`
    Label         string           `json:"label"`
}

type publishPrincipal struct {
    Hub    string `json:"hub"`
    UserID string `json:"user_id"`
}

type revokeRequest struct {
    SchemaVersion int    `json:"schema_version"`
    Operation     string `json:"operation"`
    UUID          string `json:"uuid"`
}

func newPublishRequest(s share.Scope) publishRequest {
    return publishRequest{
        SchemaVersion: schemaVersion,
        Operation:     "publish",
        Scope: publishScope{
            UUID:          s.UUID,
            Owner:         toPublishPrincipal(s.Owner),
            Grantee:       toPublishPrincipal(s.Grantee),
            AllowDownload: s.AllowDownload,
            ExpiresAt:     s.ExpiresAt,
            Label:         s.Label,
        },
    }
}

func toPublishPrincipal(p owners.Principal) publishPrincipal {
    return publishPrincipal{Hub: p.Hub, UserID: p.UserID}
}

func newRevokeRequest(uuid string) revokeRequest {
    return revokeRequest{
        SchemaVersion: schemaVersion,
        Operation:     "revoke",
        UUID:          uuid,
    }
}
```

- [ ] **Step 4: Run the tests and confirm they pass**

Run: `go test ./internal/brokerexec -run 'TestNew(Publish|Revoke)Request' -v`
Expected: PASS.

- [ ] **Step 5: Lint and commit**

Run: `make lint`
Expected: clean.

```bash
git add internal/brokerexec/payload.go internal/brokerexec/payload_test.go
git commit -m "Add publish/revoke JSON payload types for brokerexec"
```

---

## Task 4: Add `Registrar` foundation (`Config`, `New`, env merge)

**Files:**
- Create: `internal/brokerexec/exec.go` (initial — only foundation; PublishScope/RevokeScope come in Task 6)
- Create: `internal/brokerexec/exec_test.go` (initial — only foundation tests)

- [ ] **Step 1: Write failing tests for `New`, env parsing, and env overlay**

Create `internal/brokerexec/exec_test.go`:

```go
package brokerexec

import (
    "log/slog"
    "testing"
    "time"

    "github.com/stretchr/testify/require"

    "github.com/wesm/fotobank/internal/errs"
)

func TestNewRequiresCommand(t *testing.T) {
    cases := []string{"", "   ", "\t"}
    for _, cmd := range cases {
        t.Run(cmd, func(t *testing.T) {
            _, err := New(Config{Command: cmd})
            require.ErrorIs(t, err, errs.ErrBadConfiguration)
        })
    }
}

func TestNewRejectsBadEnv(t *testing.T) {
    cases := []string{"=value", "NO_EQUALS", "="}
    for _, e := range cases {
        t.Run(e, func(t *testing.T) {
            _, err := New(Config{
                Command: "/bin/true",
                Env:     []string{e},
            })
            require.ErrorIs(t, err, errs.ErrBadConfiguration)
        })
    }
}

func TestNewDefaultsCallTimeout(t *testing.T) {
    r, err := New(Config{Command: "/bin/true"})
    require.NoError(t, err)
    require.Equal(t, 30*time.Second, r.callTimeout)
}

func TestNewKeepsExplicitCallTimeout(t *testing.T) {
    r, err := New(Config{
        Command:     "/bin/true",
        CallTimeout: 5 * time.Second,
    })
    require.NoError(t, err)
    require.Equal(t, 5*time.Second, r.callTimeout)
}

func TestNewDefaultsLogger(t *testing.T) {
    r, err := New(Config{Command: "/bin/true"})
    require.NoError(t, err)
    require.NotNil(t, r.logger, "logger must default to a non-nil discard logger")
}

func TestNewKeepsExplicitLogger(t *testing.T) {
    custom := slog.Default()
    r, err := New(Config{Command: "/bin/true", Logger: custom})
    require.NoError(t, err)
    require.Same(t, custom, r.logger)
}

func TestBuildEnvOverlay(t *testing.T) {
    t.Setenv("FB_BROKEREXEC_TEST_BASE", "base")
    t.Setenv("FB_BROKEREXEC_TEST_OVERLAY", "from_env")

    r, err := New(Config{
        Command: "/bin/true",
        Env: []string{
            "FB_BROKEREXEC_TEST_OVERLAY=from_config",
            "FB_BROKEREXEC_TEST_NEW=created",
        },
    })
    require.NoError(t, err)

    env := r.buildEnv()
    require.Contains(t, env, "FB_BROKEREXEC_TEST_BASE=base",
        "inherited base value preserved")
    require.Contains(t, env, "FB_BROKEREXEC_TEST_OVERLAY=from_config",
        "configured override wins over inherited")
    require.NotContains(t, env, "FB_BROKEREXEC_TEST_OVERLAY=from_env",
        "inherited overlay must be replaced, not duplicated")
    require.Contains(t, env, "FB_BROKEREXEC_TEST_NEW=created",
        "configured-only key is appended")
}

func TestBuildEnvNoOverridesReturnsBase(t *testing.T) {
    t.Setenv("FB_BROKEREXEC_TEST_NOENV", "keep")
    r, err := New(Config{Command: "/bin/true"})
    require.NoError(t, err)
    env := r.buildEnv()
    require.Contains(t, env, "FB_BROKEREXEC_TEST_NOENV=keep")
}
```

- [ ] **Step 2: Run the tests and confirm compilation failure**

Run: `go test ./internal/brokerexec -run 'TestNew|TestBuildEnv' -v`
Expected: build fails — `Config`, `New`, `Registrar.callTimeout`, `Registrar.logger`, `buildEnv` all undefined.

- [ ] **Step 3: Create `internal/brokerexec/exec.go` with foundation only**

```go
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

// runFunc is the unexported test seam. realRun (defined in Task 6)
// is the production default; same-package tests pass a fake.
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
        // runCmd left nil here; PublishScope/RevokeScope set it to
        // realRun in Task 6. Tests overwrite it directly.
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
```

- [ ] **Step 4: Run the foundation tests and confirm they pass**

Run: `go test ./internal/brokerexec -run 'TestNew|TestBuildEnv' -v`
Expected: PASS for all eight tests.

- [ ] **Step 5: Lint and commit**

Run: `make lint`
Expected: clean.

```bash
git add internal/brokerexec/exec.go internal/brokerexec/exec_test.go
git commit -m "Add brokerexec.Registrar foundation: Config, New, env merge"
```

---

## Task 5: Add `classifyExit` and `tailForError`

**Files:**
- Create: `internal/brokerexec/classify.go`
- Create: `internal/brokerexec/classify_test.go`

- [ ] **Step 1: Write failing classification + normalization tests**

Create `internal/brokerexec/classify_test.go`:

```go
package brokerexec

import (
    "errors"
    "strings"
    "testing"

    "github.com/stretchr/testify/require"

    "github.com/wesm/fotobank/internal/broker"
)

func TestClassifyExitPermanent(t *testing.T) {
    err := classifyExit("publish", 65, "scope already exists", nil)
    require.ErrorIs(t, err, broker.ErrBrokerPermanent)
    require.NotErrorIs(t, err, broker.ErrBrokerTransient)
    require.Contains(t, err.Error(), "scope already exists")
    require.Contains(t, err.Error(), "publish")
}

func TestClassifyExitTransient(t *testing.T) {
    err := classifyExit("publish", 75, "upstream busy", nil)
    require.ErrorIs(t, err, broker.ErrBrokerTransient)
    require.NotErrorIs(t, err, broker.ErrBrokerPermanent)
    require.Contains(t, err.Error(), "upstream busy")
}

func TestClassifyExitUnknownIsTransient(t *testing.T) {
    err := classifyExit("publish", 1, "boom", nil)
    require.ErrorIs(t, err, broker.ErrBrokerTransient)
    require.Contains(t, err.Error(), "exit=1")
    require.Contains(t, err.Error(), "boom")
}

func TestClassifyExitProcessNeverStarted(t *testing.T) {
    runErr := errors.New(`exec: "fb-broker": executable file not found in $PATH`)
    err := classifyExit("publish", -1, "", runErr)
    require.ErrorIs(t, err, broker.ErrBrokerTransient)
    require.Contains(t, err.Error(), "executable file not found")
}

func TestTailForErrorTruncates(t *testing.T) {
    in := strings.Repeat("X", 300)
    got := tailForError([]byte(in))
    require.Equal(t, 256, len(got))
    require.Equal(t, strings.Repeat("X", 256), got)
}

func TestTailForErrorNormalizes(t *testing.T) {
    cases := []struct {
        name string
        in   []byte
        want string
    }{
        {"strips control bytes", []byte("hello\x00world"), "helloworld"},
        {"replaces tab with space", []byte("a\tb"), "a b"},
        {"collapses whitespace runs", []byte("a   \n\n b"), "a b"},
        {"strips DEL byte", []byte("foo\x7fbar"), "foobar"},
        {"keeps printable ascii", []byte("hello world!"), "hello world!"},
        {"mixed", []byte("hello\x00\nworld\x07\t!"), "hello world !"},
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            require.Equal(t, tc.want, tailForError(tc.in))
        })
    }
}
```

- [ ] **Step 2: Run the tests and confirm compilation failure**

Run: `go test ./internal/brokerexec -run 'TestClassifyExit|TestTailForError' -v`
Expected: build fails — `classifyExit` and `tailForError` undefined.

- [ ] **Step 3: Create `internal/brokerexec/classify.go`**

```go
package brokerexec

import (
    "fmt"
    "strings"

    "github.com/wesm/fotobank/internal/broker"
)

// classifyExit maps a (exitCode, runErr) pair plus a normalized
// stderr tail into a wrapped broker sentinel:
//
//   - 0 should never reach here; callers gate success out before
//     calling classifyExit. We treat it as transient defensively.
//   - 65 -> ErrBrokerPermanent (sysexits EX_DATAERR; broker
//     rejected the scope).
//   - 75 -> ErrBrokerTransient (sysexits EX_TEMPFAIL).
//   - Any other exit code, including -1 (process never started),
//     is treated as transient. The share worker's MaxBrokerAttempts
//     cap (10) provides a backstop against runaway retries on a
//     persistently broken CLI.
//
// runErr is included verbatim for diagnostic value when the process
// never started (e.g. "executable file not found in $PATH").
func classifyExit(op string, exitCode int, tail string, runErr error) error {
    switch exitCode {
    case 65:
        return fmt.Errorf("brokerexec %s: %s: %w",
            op, tail, broker.ErrBrokerPermanent)
    case 75:
        return fmt.Errorf("brokerexec %s: %s: %w",
            op, tail, broker.ErrBrokerTransient)
    default:
        if runErr != nil {
            return fmt.Errorf("brokerexec %s: %s: exit=%d run-err=%v: %w",
                op, tail, exitCode, runErr, broker.ErrBrokerTransient)
        }
        return fmt.Errorf("brokerexec %s: %s: exit=%d: %w",
            op, tail, exitCode, broker.ErrBrokerTransient)
    }
}

// tailForError returns at most 256 bytes from the END of b, with
// control bytes stripped, tabs replaced by spaces, and whitespace
// runs collapsed. Used to fold stderr into the wrapped error
// message that ends up in scopes.broker_last_error.
func tailForError(b []byte) string {
    if len(b) > 256 {
        b = b[len(b)-256:]
    }
    s := strings.Map(func(r rune) rune {
        if r == '\t' {
            return ' '
        }
        if r < 0x20 || r == 0x7f {
            return -1
        }
        return r
    }, string(b))
    return strings.Join(strings.Fields(s), " ")
}
```

- [ ] **Step 4: Run the tests and confirm they pass**

Run: `go test ./internal/brokerexec -run 'TestClassifyExit|TestTailForError' -v`
Expected: PASS.

- [ ] **Step 5: Lint and commit**

Run: `make lint`
Expected: clean.

```bash
git add internal/brokerexec/classify.go internal/brokerexec/classify_test.go
git commit -m "Add exit code classification and stderr tail normalization"
```

---

## Task 6: Add `PublishScope`, `RevokeScope`, and `realRun`

**Files:**
- Modify: `internal/brokerexec/exec.go` — append `PublishScope`, `RevokeScope`, `execOnce`, `realRun`
- Modify: `internal/brokerexec/exec_test.go` — append Layer A unit tests

- [ ] **Step 1: Append the Layer A unit tests**

Append to `internal/brokerexec/exec_test.go` (after the existing tests):

```go
import (
    "bytes"
    "context"
    "encoding/json"
    "errors"
    "io"
    "strings"

    "github.com/wesm/fotobank/internal/broker"
    "github.com/wesm/fotobank/internal/owners"
    "github.com/wesm/fotobank/internal/share"
)

// fakeResult drives the unexported runFunc seam.
//
// stdoutBytes / stderrBytes are written to the supplied writers
// before return. exit is the returned exit code; runErr is the
// returned error. waitForCtx makes the fake block on ctx.Done()
// and return ctx.Err() — used to test per-call timeout behaviour
// without a real process.
//
// captureStdin / captureEnv / captureArgs collect inputs for assertion.
type fakeResult struct {
    stdoutBytes []byte
    stderrBytes []byte
    exit        int
    runErr      error
    waitForCtx  bool

    captureStdin *bytes.Buffer
    captureEnv   *[]string
    captureArgs  *[]string
}

func newFakeRun(r *fakeResult) runFunc {
    return func(ctx context.Context, command string, args, env []string,
        stdin io.Reader, stdout, stderr io.Writer) (int, error) {
        if r.captureStdin != nil {
            _, _ = io.Copy(r.captureStdin, stdin)
        } else {
            _, _ = io.Copy(io.Discard, stdin)
        }
        if r.captureEnv != nil {
            *r.captureEnv = append([]string(nil), env...)
        }
        if r.captureArgs != nil {
            *r.captureArgs = append([]string(nil), args...)
        }
        if r.waitForCtx {
            <-ctx.Done()
            return 0, ctx.Err()
        }
        if len(r.stdoutBytes) > 0 {
            _, _ = stdout.Write(r.stdoutBytes)
        }
        if len(r.stderrBytes) > 0 {
            _, _ = stderr.Write(r.stderrBytes)
        }
        return r.exit, r.runErr
    }
}

// newTestRegistrar builds a Registrar with the provided Config (Command
// defaults to /bin/true) and installs the fake runner.
func newTestRegistrar(t *testing.T, cfg Config, fr *fakeResult) *Registrar {
    t.Helper()
    if cfg.Command == "" {
        cfg.Command = "/bin/true"
    }
    r, err := New(cfg)
    require.NoError(t, err)
    r.runCmd = newFakeRun(fr)
    return r
}

func sampleScope() share.Scope {
    return share.Scope{
        UUID:          "test-uuid",
        Owner:         owners.Principal{Hub: "h", UserID: "alice"},
        Grantee:       owners.Principal{Hub: "h", UserID: "bob"},
        AllowDownload: true,
        Label:         "Trip",
    }
}

func TestPublishScopeSuccess(t *testing.T) {
    var stdin bytes.Buffer
    var args []string
    fr := &fakeResult{exit: 0, captureStdin: &stdin, captureArgs: &args}
    r := newTestRegistrar(t, Config{
        PublishScopeArgs: []string{"scope", "publish"},
    }, fr)

    require.NoError(t, r.PublishScope(context.Background(), sampleScope()))
    require.Equal(t, []string{"scope", "publish"}, args)

    var got map[string]any
    require.NoError(t, json.Unmarshal(stdin.Bytes(), &got))
    require.Equal(t, float64(1), got["schema_version"])
    require.Equal(t, "publish", got["operation"])
    scope := got["scope"].(map[string]any)
    require.Equal(t, "test-uuid", scope["uuid"])
    require.Equal(t, true, scope["allow_download"])
}

func TestPublishScopeExitPermanent(t *testing.T) {
    fr := &fakeResult{exit: 65, stderrBytes: []byte("scope rejected")}
    r := newTestRegistrar(t, Config{}, fr)
    err := r.PublishScope(context.Background(), sampleScope())
    require.ErrorIs(t, err, broker.ErrBrokerPermanent)
    require.Contains(t, err.Error(), "scope rejected")
}

func TestPublishScopeExitTransient(t *testing.T) {
    fr := &fakeResult{exit: 75}
    r := newTestRegistrar(t, Config{}, fr)
    err := r.PublishScope(context.Background(), sampleScope())
    require.ErrorIs(t, err, broker.ErrBrokerTransient)
    require.NotErrorIs(t, err, broker.ErrBrokerPermanent)
}

func TestPublishScopeExitUnknown(t *testing.T) {
    fr := &fakeResult{exit: 1, stderrBytes: []byte("boom")}
    r := newTestRegistrar(t, Config{}, fr)
    err := r.PublishScope(context.Background(), sampleScope())
    require.ErrorIs(t, err, broker.ErrBrokerTransient)
    require.Contains(t, err.Error(), "exit=1")
}

func TestPublishScopeStderrTruncated(t *testing.T) {
    big := bytes.Repeat([]byte("X"), 8192) // 8 KiB
    fr := &fakeResult{exit: 65, stderrBytes: big}

    var captured bytes.Buffer
    handler := slog.NewTextHandler(&captured, nil)
    r := newTestRegistrar(t, Config{Logger: slog.New(handler)}, fr)

    err := r.PublishScope(context.Background(), sampleScope())
    require.ErrorIs(t, err, broker.ErrBrokerPermanent)

    // Wrapped error tail folds in only the last 256 bytes.
    parts := strings.Split(err.Error(), ": ")
    require.GreaterOrEqual(t, len(parts), 3)
    // The middle part holds the normalized tail; assert it is at
    // most 256 chars.
    foundTail := false
    for _, p := range parts {
        if strings.HasPrefix(p, "XXXX") {
            require.LessOrEqual(t, len(p), 256)
            foundTail = true
        }
    }
    require.True(t, foundTail, "expected tail to surface in error")

    // Logged stderr is bounded to the prefixBuffer cap (4 KiB), not
    // the full 8 KiB the fake wrote.
    logged := captured.String()
    require.Contains(t, logged, "stderr=")
    // The logged stderr value shouldn't exceed ~4 KiB once decoded
    // from the slog text format. Guard with a generous bound.
    require.Less(t, len(logged), 6000,
        "logged stderr must not contain the full 8 KiB payload")
}

func TestPublishScopeStderrNormalized(t *testing.T) {
    fr := &fakeResult{exit: 65, stderrBytes: []byte("hello\x00\nworld\x07\t!")}
    r := newTestRegistrar(t, Config{}, fr)
    err := r.PublishScope(context.Background(), sampleScope())
    require.Contains(t, err.Error(), "hello world !")
}

func TestPublishScopeOmitsExpiresAt(t *testing.T) {
    var stdin bytes.Buffer
    fr := &fakeResult{exit: 0, captureStdin: &stdin}
    r := newTestRegistrar(t, Config{}, fr)

    s := sampleScope()
    s.ExpiresAt = nil
    require.NoError(t, r.PublishScope(context.Background(), s))

    var got map[string]any
    require.NoError(t, json.Unmarshal(stdin.Bytes(), &got))
    scope := got["scope"].(map[string]any)
    _, has := scope["expires_at"]
    require.False(t, has)
}

func TestPublishScopeRedactsMembership(t *testing.T) {
    var stdin bytes.Buffer
    fr := &fakeResult{exit: 0, captureStdin: &stdin}
    r := newTestRegistrar(t, Config{}, fr)

    albumID := "album-1"
    s := sampleScope()
    s.TargetType = share.TargetMediaSet
    s.TargetAlbumID = &albumID
    require.NoError(t, r.PublishScope(context.Background(), s))

    var got map[string]any
    require.NoError(t, json.Unmarshal(stdin.Bytes(), &got))
    scope := got["scope"].(map[string]any)
    for _, k := range []string{"target_type", "album_id", "media_ids"} {
        _, has := scope[k]
        require.Falsef(t, has, "scope payload must not include %q", k)
    }
}

func TestRevokeScopePayload(t *testing.T) {
    var stdin bytes.Buffer
    var args []string
    fr := &fakeResult{exit: 0, captureStdin: &stdin, captureArgs: &args}
    r := newTestRegistrar(t, Config{
        PublishScopeArgs: []string{"scope", "publish"},
        RevokeScopeArgs:  []string{"scope", "revoke"},
    }, fr)

    require.NoError(t, r.RevokeScope(context.Background(), "abc"))
    require.Equal(t, []string{"scope", "revoke"}, args)

    var got map[string]any
    require.NoError(t, json.Unmarshal(stdin.Bytes(), &got))
    require.Equal(t, float64(1), got["schema_version"])
    require.Equal(t, "revoke", got["operation"])
    require.Equal(t, "abc", got["uuid"])
    _, has := got["scope"]
    require.False(t, has, "revoke payload must not contain a scope object")
}

func TestParentCtxCancellation(t *testing.T) {
    fr := &fakeResult{runErr: context.Canceled, exit: -1}
    r := newTestRegistrar(t, Config{}, fr)

    ctx, cancel := context.WithCancel(context.Background())
    cancel()

    err := r.PublishScope(ctx, sampleScope())
    require.ErrorIs(t, err, context.Canceled)
    require.NotErrorIs(t, err, broker.ErrBrokerTransient,
        "ctx cancellation must NOT classify as transient — worker isCtxErr must match")
}

func TestPerCallTimeoutClassifiedTransient(t *testing.T) {
    fr := &fakeResult{waitForCtx: true}
    r := newTestRegistrar(t, Config{
        CallTimeout: 10 * time.Millisecond,
    }, fr)
    err := r.PublishScope(context.Background(), sampleScope())
    require.ErrorIs(t, err, broker.ErrBrokerTransient)
    require.NotErrorIs(t, err, context.DeadlineExceeded,
        "per-call timeout must not propagate as DeadlineExceeded")
    require.NotErrorIs(t, err, context.Canceled)
}

func TestRegistrarSatisfiesBrokerClient(t *testing.T) {
    var _ broker.BrokerClient = (*Registrar)(nil)
}
```

Then **replace** the import block at the top of `exec_test.go` (the one Task 4 created) with the union set both halves need:

```go
import (
    "bytes"
    "context"
    "encoding/json"
    "io"
    "log/slog"
    "strings"
    "testing"
    "time"

    "github.com/stretchr/testify/require"

    "github.com/wesm/fotobank/internal/broker"
    "github.com/wesm/fotobank/internal/errs"
    "github.com/wesm/fotobank/internal/owners"
    "github.com/wesm/fotobank/internal/share"
)
```

(Go test files use one import block. The Task 4 block had `log/slog`, `testing`, `time`, `require`, `errs`; this superset adds `bytes`, `context`, `encoding/json`, `io`, `strings`, plus `broker`, `owners`, `share`. Drop the second `import (...)` block from the appended hunk if your editor inserts it.)

- [ ] **Step 2: Run the new tests and confirm they fail**

Run: `go test ./internal/brokerexec -run 'TestPublishScope|TestRevokeScope|TestParentCtxCancellation|TestPerCallTimeout|TestRegistrarSatisfies' -v`
Expected: build fails — `PublishScope`, `RevokeScope`, and `realRun` undefined.

- [ ] **Step 3: Append the orchestration code to `internal/brokerexec/exec.go`**

Add the `os/exec`, `bytes`, `encoding/json`, `errors` imports to the existing import block. Then append at the bottom of `exec.go`:

```go
import (
    "bytes"
    "context"
    "encoding/json"
    "errors"
    "fmt"
    "io"
    "log/slog"
    "os"
    "os/exec"
    "sort"
    "strings"
    "time"

    "github.com/wesm/fotobank/internal/broker"
    "github.com/wesm/fotobank/internal/errs"
    "github.com/wesm/fotobank/internal/share"
)
```

```go
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
    if err := json.NewEncoder(&stdinBuf).Encode(payload); err != nil {
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
```

Note: this changes `New` so that `r.runCmd` is left nil and `execOnce` falls back to `realRun`. This keeps the test seam injection trivial (assign `r.runCmd = newFakeRun(...)`) without requiring `New` to know about `realRun`.

- [ ] **Step 4: Run all brokerexec tests and confirm green**

Run: `go test ./internal/brokerexec -v`
Expected: PASS for every test in the package — Task 2, 3, 4, 5, and the new Layer A tests from Task 6.

- [ ] **Step 5: Run the full suite for regressions**

Run: `go test ./...`
Expected: all green.

- [ ] **Step 6: Lint and commit**

Run: `make lint`
Expected: clean.

```bash
git add internal/brokerexec/exec.go internal/brokerexec/exec_test.go
git commit -m "Add PublishScope, RevokeScope, and realRun to brokerexec.Registrar"
```

---

## Task 7: Add `internal/testutil/brokerhelper`

**Files:**
- Create: `internal/testutil/brokerhelper/helper.go`

- [ ] **Step 1: Create the package**

```go
// Package brokerhelper supplies a re-execable broker-CLI fake for
// tests. Test binaries opt in by checking IsHelper() in TestMain;
// when set, the binary calls Run() (which exits) instead of running
// its own test suite. Each test binary that uses this becomes its
// own broker CLI when invoked with BROKEREXEC_TEST_HELPER=1.
//
// Behaviour is driven entirely by env vars so callers don't have to
// recompile to vary the simulated broker:
//
//   BROKEREXEC_TEST_HELPER=1            // opt in
//   BROKEREXEC_TEST_EXPECT_OPERATION    // assert payload.operation; exit 65 on mismatch
//   BROKEREXEC_TEST_ECHO_ENV            // os.Getenv($value) -> stderr
//   BROKEREXEC_TEST_STDERR              // literal -> stderr
//   BROKEREXEC_TEST_SLEEP               // time.Duration sleep before exit
//   BROKEREXEC_TEST_EXIT                // exit code (default 0)
package brokerhelper

import (
    "encoding/json"
    "fmt"
    "io"
    "os"
    "strconv"
    "time"
)

// EnvVar is the env-var name that switches a test binary into helper mode.
const EnvVar = "BROKEREXEC_TEST_HELPER"

// IsHelper reports whether the current process should run as the helper.
func IsHelper() bool { return os.Getenv(EnvVar) == "1" }

// Run reads stdin, optionally validates the operation discriminator,
// optionally echoes a configured env var to stderr, optionally writes
// a literal stderr message, optionally sleeps, and exits with the
// configured exit code. Never returns.
func Run() {
    payload, _ := io.ReadAll(os.Stdin)
    if want := os.Getenv("BROKEREXEC_TEST_EXPECT_OPERATION"); want != "" {
        var got struct {
            Operation string `json:"operation"`
        }
        _ = json.Unmarshal(payload, &got)
        if got.Operation != want {
            fmt.Fprintf(os.Stderr, "operation mismatch: got=%q want=%q",
                got.Operation, want)
            os.Exit(65)
        }
    }
    if k := os.Getenv("BROKEREXEC_TEST_ECHO_ENV"); k != "" {
        fmt.Fprint(os.Stderr, os.Getenv(k))
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

- [ ] **Step 2: Verify it compiles standalone**

Run: `go build ./internal/testutil/brokerhelper`
Expected: no output, exit 0.

- [ ] **Step 3: Run go vet**

Run: `go vet ./internal/testutil/brokerhelper`
Expected: clean.

- [ ] **Step 4: Lint and commit**

Run: `make lint`
Expected: clean.

```bash
git add internal/testutil/brokerhelper/helper.go
git commit -m "Add brokerhelper for re-executable test broker CLI fake"
```

---

## Task 8: Layer B end-to-end tests against a real `os/exec`

**Files:**
- Create: `internal/brokerexec/exec_e2e_test.go`

- [ ] **Step 1: Write the e2e test file**

```go
package brokerexec_test

import (
    "context"
    "os"
    "testing"
    "time"

    "github.com/stretchr/testify/require"

    "github.com/wesm/fotobank/internal/broker"
    "github.com/wesm/fotobank/internal/brokerexec"
    "github.com/wesm/fotobank/internal/owners"
    "github.com/wesm/fotobank/internal/share"
    "github.com/wesm/fotobank/internal/testutil/brokerhelper"
)

func TestMain(m *testing.M) {
    if brokerhelper.IsHelper() {
        brokerhelper.Run()
        return
    }
    os.Exit(m.Run())
}

// helperCommand returns the path of the running test binary, which
// becomes the broker CLI when invoked with brokerhelper.EnvVar=1.
func helperCommand(t *testing.T) string {
    t.Helper()
    self, err := os.Executable()
    require.NoError(t, err)
    return self
}

func sampleScope() share.Scope {
    return share.Scope{
        UUID:    "e2e-uuid",
        Owner:   owners.Principal{Hub: "h", UserID: "alice"},
        Grantee: owners.Principal{Hub: "h", UserID: "bob"},
    }
}

func TestE2EPublishSuccess(t *testing.T) {
    if testing.Short() {
        t.Skip("e2e exec tests skipped under -short")
    }
    r, err := brokerexec.New(brokerexec.Config{
        Command: helperCommand(t),
        Env: []string{
            brokerhelper.EnvVar + "=1",
            "BROKEREXEC_TEST_EXIT=0",
            "BROKEREXEC_TEST_EXPECT_OPERATION=publish",
        },
    })
    require.NoError(t, err)
    require.NoError(t, r.PublishScope(context.Background(), sampleScope()))
}

func TestE2EPermanentExit(t *testing.T) {
    if testing.Short() {
        t.Skip("e2e exec tests skipped under -short")
    }
    r, err := brokerexec.New(brokerexec.Config{
        Command: helperCommand(t),
        Env: []string{
            brokerhelper.EnvVar + "=1",
            "BROKEREXEC_TEST_EXIT=65",
            "BROKEREXEC_TEST_STDERR=scope already exists",
        },
    })
    require.NoError(t, err)

    err = r.PublishScope(context.Background(), sampleScope())
    require.ErrorIs(t, err, broker.ErrBrokerPermanent)
    require.Contains(t, err.Error(), "scope already exists")
}

func TestE2ETimeoutKillsChild(t *testing.T) {
    if testing.Short() {
        t.Skip("e2e exec tests skipped under -short")
    }
    r, err := brokerexec.New(brokerexec.Config{
        Command:     helperCommand(t),
        CallTimeout: 50 * time.Millisecond,
        Env: []string{
            brokerhelper.EnvVar + "=1",
            "BROKEREXEC_TEST_SLEEP=5s",
            "BROKEREXEC_TEST_EXIT=0",
        },
    })
    require.NoError(t, err)

    start := time.Now()
    err = r.PublishScope(context.Background(), sampleScope())
    elapsed := time.Since(start)

    require.ErrorIs(t, err, broker.ErrBrokerTransient)
    require.NotErrorIs(t, err, context.DeadlineExceeded,
        "per-call timeout must not propagate as DeadlineExceeded")
    require.Less(t, elapsed, 1*time.Second,
        "child must be killed promptly when call timeout fires")
}

func TestE2EParentCtxCancelKillsChild(t *testing.T) {
    if testing.Short() {
        t.Skip("e2e exec tests skipped under -short")
    }
    r, err := brokerexec.New(brokerexec.Config{
        Command: helperCommand(t),
        Env: []string{
            brokerhelper.EnvVar + "=1",
            "BROKEREXEC_TEST_SLEEP=5s",
            "BROKEREXEC_TEST_EXIT=0",
        },
    })
    require.NoError(t, err)

    ctx, cancel := context.WithCancel(context.Background())
    go func() {
        time.Sleep(50 * time.Millisecond)
        cancel()
    }()
    start := time.Now()
    err = r.PublishScope(ctx, sampleScope())
    elapsed := time.Since(start)

    require.ErrorIs(t, err, context.Canceled)
    require.Less(t, elapsed, 1*time.Second,
        "child must be killed promptly when caller ctx is cancelled")
}

func TestE2EEnvReachesChild(t *testing.T) {
    if testing.Short() {
        t.Skip("e2e exec tests skipped under -short")
    }
    r, err := brokerexec.New(brokerexec.Config{
        Command: helperCommand(t),
        Env: []string{
            brokerhelper.EnvVar + "=1",
            "BROKEREXEC_TEST_EXIT=65",
            "BROKEREXEC_TEST_ECHO_ENV=FB_BROKER_ENV",
            "FB_BROKER_ENV=prod",
        },
    })
    require.NoError(t, err)

    err = r.PublishScope(context.Background(), sampleScope())
    require.ErrorIs(t, err, broker.ErrBrokerPermanent)
    require.Contains(t, err.Error(), "prod",
        "configured env must be passed to the child process")
}
```

- [ ] **Step 2: Run the e2e tests**

Run: `go test ./internal/brokerexec -run TestE2E -v`
Expected: PASS — all five tests. Each typically completes in under 200 ms.

- [ ] **Step 3: Verify `-short` skips them**

Run: `go test ./internal/brokerexec -short -run TestE2E -v`
Expected: SKIP for each test, no failures.

- [ ] **Step 4: Run the full brokerexec package**

Run: `go test ./internal/brokerexec -v`
Expected: every Layer A test still passes alongside Layer B; nothing flaky.

- [ ] **Step 5: Lint and commit**

Run: `make lint`
Expected: clean.

```bash
git add internal/brokerexec/exec_e2e_test.go
git commit -m "Add Layer B e2e tests for brokerexec via re-executed test binary"
```

---

## Task 9: Add `cli/broker.go` mode dispatcher

**Files:**
- Create: `internal/cli/broker.go`
- Create: `internal/cli/broker_test.go`

- [ ] **Step 1: Write the failing dispatcher tests**

Create `internal/cli/broker_test.go`:

```go
package cli

import (
    "log/slog"
    "testing"

    "github.com/stretchr/testify/require"

    "github.com/wesm/fotobank/internal/broker"
    "github.com/wesm/fotobank/internal/brokerexec"
    "github.com/wesm/fotobank/internal/config"
)

func TestNewBrokerClientStub(t *testing.T) {
    c, err := newBrokerClient(config.Broker{Mode: "stub"}, slog.Default())
    require.NoError(t, err)
    _, ok := c.(broker.NoopBroker)
    require.True(t, ok, "mode=stub must return NoopBroker, got %T", c)
}

func TestNewBrokerClientExec(t *testing.T) {
    c, err := newBrokerClient(config.Broker{
        Mode: "exec",
        Exec: config.BrokerExec{Command: "/bin/true"},
    }, slog.Default())
    require.NoError(t, err)
    _, ok := c.(*brokerexec.Registrar)
    require.True(t, ok, "mode=exec must return *brokerexec.Registrar, got %T", c)
}

func TestNewBrokerClientExecPropagatesConfigError(t *testing.T) {
    _, err := newBrokerClient(config.Broker{
        Mode: "exec",
        Exec: config.BrokerExec{Command: ""}, // missing
    }, slog.Default())
    require.Error(t, err, "exec with empty command must surface error")
}

func TestNewBrokerClientUnknown(t *testing.T) {
    _, err := newBrokerClient(config.Broker{Mode: "weird"}, slog.Default())
    require.Error(t, err)
    require.Contains(t, err.Error(), `unknown broker mode "weird"`)
}
```

- [ ] **Step 2: Run the tests and confirm compilation failure**

Run: `go test ./internal/cli -run TestNewBrokerClient -v`
Expected: build fails — `newBrokerClient` is undefined.

- [ ] **Step 3: Create `internal/cli/broker.go`**

```go
package cli

import (
    "fmt"
    "log/slog"

    "github.com/wesm/fotobank/internal/broker"
    "github.com/wesm/fotobank/internal/brokerexec"
    "github.com/wesm/fotobank/internal/config"
)

// newBrokerClient returns the broker.BrokerClient implementation
// matching cfg.Mode. Used by runServer at boot. config.Validate
// already rejects unknown modes; the default arm here is
// defence-in-depth.
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

- [ ] **Step 4: Run the tests and confirm they pass**

Run: `go test ./internal/cli -run TestNewBrokerClient -v`
Expected: PASS for all four.

- [ ] **Step 5: Lint and commit**

Run: `make lint`
Expected: clean.

```bash
git add internal/cli/broker.go internal/cli/broker_test.go
git commit -m "Add cli.newBrokerClient mode dispatcher"
```

---

## Task 10: Wire `cli/server.go` to the dispatcher

**Files:**
- Modify: `internal/cli/server.go`

- [ ] **Step 1: Locate the existing `shareCfg` block**

The block is around `internal/cli/server.go:225-229`:

```go
shareCfg := shareworker.Config{
    Repo:   sharesRepo,
    Broker: broker.NoopBroker{},
    Logger: slog.New(slog.NewTextHandler(opts.stderr, nil)),
}
```

- [ ] **Step 2: Replace it with the dispatcher call**

Replace the block above with:

```go
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
```

If `fmt` isn't already imported in `server.go`, add it. If `broker` is no longer referenced (the only use was `broker.NoopBroker{}`), remove that import — `goimports` will handle this if `make lint` runs `goimports`.

- [ ] **Step 3: Build the server binary**

Run: `make build`
Expected: builds cleanly to `bin/fotobank`.

- [ ] **Step 4: Run the cli package tests**

Run: `go test ./internal/cli -v`
Expected: all green, including existing `TestSharedE2EHeaderMode` (which uses `mode = "stub"` so the dispatcher returns `NoopBroker` — no behavioural change).

- [ ] **Step 5: Run the full suite**

Run: `go test ./...`
Expected: all green.

- [ ] **Step 6: Lint and commit**

Run: `make lint`
Expected: clean.

```bash
git add internal/cli/server.go
git commit -m "Wire cli/server.go to brokerexec dispatcher"
```

---

## Task 11: Layer C single integration test

**Files:**
- Create: `internal/cli/e2e_brokerexec_test.go`

- [ ] **Step 1: Write the integration test**

```go
package cli_test

import (
    "bytes"
    "context"
    "database/sql"
    "encoding/json"
    "fmt"
    "net/http"
    "os"
    "path/filepath"
    "testing"
    "time"

    "github.com/stretchr/testify/require"

    _ "modernc.org/sqlite"

    "github.com/wesm/fotobank/internal/cli"
    "github.com/wesm/fotobank/internal/testutil/brokerhelper"
)

func TestMain(m *testing.M) {
    if brokerhelper.IsHelper() {
        brokerhelper.Run()
        return
    }
    os.Exit(m.Run())
}

// TestE2EBrokerExecPublishesAndRevokes boots a real fotobank server
// with mode = "exec" and the test binary itself as the broker CLI.
// It POSTs a share, watches for broker_status='active' (the worker
// has run PublishScope through the helper), then DELETEs the share
// and watches for 'revoked_remote'.
//
// The Layer A and B tests in internal/brokerexec already cover wire
// and exec details; Layer C only proves wiring + worker + DB
// compose under the actual server boot path.
func TestE2EBrokerExecPublishesAndRevokes(t *testing.T) {
    if testing.Short() {
        t.Skip("e2e tests skipped under -short")
    }
    r := require.New(t)

    tmp := t.TempDir()
    nasRoot := filepath.Join(tmp, "nas")
    flashRoot := filepath.Join(tmp, "flash")
    r.NoError(os.MkdirAll(nasRoot, 0o755))
    r.NoError(os.MkdirAll(flashRoot, 0o755))

    self, err := os.Executable()
    r.NoError(err)

    cfgPath := filepath.Join(tmp, "fotobank.toml")
    r.NoError(os.WriteFile(cfgPath, fmt.Appendf(nil, `
[nas]
root = %q
[flash]
root = %q
[http]
listen_address = "127.0.0.1:0"
[imports]
file_lock_path = %q
[broker]
mode = "exec"
[broker.exec]
command = %q
publish_scope_args = []
revoke_scope_args  = []
call_timeout = "5s"
env = [
  "%s=1",
  "BROKEREXEC_TEST_EXIT=0",
]
`, nasRoot, flashRoot, filepath.Join(tmp, "import.lock"),
        self, brokerhelper.EnvVar), 0o600))

    t.Setenv("FOTOBANK_CONFIG", cfgPath)
    t.Setenv("FOTOBANK_TEST_SHARE_WORKER_TICK", "50ms")

    sink := filepath.Join(tmp, "addr")
    t.Setenv("FOTOBANK_TEST_LISTEN_ADDR_SINK", sink)

    ctx, cancel := context.WithCancel(context.Background())
    t.Cleanup(cancel)

    done := make(chan int, 1)
    go func() {
        var so, se bytes.Buffer
        done <- cli.RunContext(ctx, []string{"server"}, &so, &se)
    }()

    addr := waitForSink(t, sink)
    r.NotEmpty(addr)
    base := "http://" + addr
    client := &http.Client{Timeout: 5 * time.Second}

    // Seed a media row so we have something to share. (album_live
    // would need album+media setup; media_set is shorter — use that.)
    mediaID := importOneMedia(t, client, base)

    // Create a share targeting media_set with the seeded media.
    body, err := json.Marshal(map[string]any{
        "grantee":     map[string]string{"hub": "h", "user_id": "bob"},
        "target_type": "media_set",
        "media_ids":   []string{mediaID},
    })
    r.NoError(err)

    resp, err := client.Post(base+"/api/v1/shares",
        "application/json", bytes.NewReader(body))
    r.NoError(err)
    r.Equal(http.StatusCreated, resp.StatusCode)
    var scope struct{ UUID string `json:"uuid"` }
    r.NoError(json.NewDecoder(resp.Body).Decode(&scope))
    r.NoError(resp.Body.Close())
    r.NotEmpty(scope.UUID)

    dbPath := filepath.Join(nasRoot, "fotobank.db")

    // Phase 1: worker should reach broker_status='active' within
    // a few ticks (50ms each). The helper exits 0 on every call.
    require.Eventually(t, func() bool {
        return readScopeStatus(t, dbPath, scope.UUID) == "active"
    }, 5*time.Second, 25*time.Millisecond,
        "broker_status did not reach 'active' — worker or registrar broken")

    // Assert the progress timestamps are populated.
    requireProgressSet(t, dbPath, scope.UUID,
        "broker_registered_at", "broker_granted_at")

    // Phase 2: revoke and watch for 'revoked_remote'.
    req, err := http.NewRequest(http.MethodDelete,
        base+"/api/v1/shares/"+scope.UUID, nil)
    r.NoError(err)
    resp, err = client.Do(req)
    r.NoError(err)
    r.NoError(resp.Body.Close())
    r.Equal(http.StatusNoContent, resp.StatusCode)

    require.Eventually(t, func() bool {
        return readScopeStatus(t, dbPath, scope.UUID) == "revoked_remote"
    }, 5*time.Second, 25*time.Millisecond,
        "broker_status did not reach 'revoked_remote'")

    requireProgressSet(t, dbPath, scope.UUID, "broker_revoked_at")

    // Tidy shutdown.
    cancel()
    select {
    case ec := <-done:
        r.Equal(0, ec)
    case <-time.After(5 * time.Second):
        r.Fail("server did not shut down")
    }
}

// readScopeStatus opens a fresh sql.DB to read scopes.broker_status.
// Opening per call is fine for the 50ms poll cadence; SQLite readers
// don't block writers in WAL mode.
func readScopeStatus(t *testing.T, dbPath, uuid string) string {
    t.Helper()
    db, err := sql.Open("sqlite", dbPath)
    require.NoError(t, err)
    defer db.Close()

    var status string
    err = db.QueryRow(
        "SELECT broker_status FROM scopes WHERE uuid = ?", uuid,
    ).Scan(&status)
    if err == sql.ErrNoRows {
        return ""
    }
    require.NoError(t, err)
    return status
}

// requireProgressSet asserts every named timestamp column on the
// scopes row is non-NULL.
func requireProgressSet(t *testing.T, dbPath, uuid string, cols ...string) {
    t.Helper()
    db, err := sql.Open("sqlite", dbPath)
    require.NoError(t, err)
    defer db.Close()

    for _, c := range cols {
        var ts sql.NullString
        err := db.QueryRow(
            fmt.Sprintf("SELECT %s FROM scopes WHERE uuid = ?", c), uuid,
        ).Scan(&ts)
        require.NoError(t, err)
        require.True(t, ts.Valid, "%s must be non-NULL after broker call", c)
    }
}

// importOneMedia POSTs a tiny JPEG fixture so the share has a real
// media to target. Returns the imported media ID.
//
// Implementation: use the existing /api/v1/media list after running
// the import. For minimal coupling, use the same fixture path that
// e2e_shared_test.go uses (../../testdata/...).
func importOneMedia(t *testing.T, client *http.Client, base string) string {
    t.Helper()
    // Mirror the exact pattern e2e_shared_test.go uses for seeding
    // media. If that helper is exported elsewhere, call it; otherwise
    // inline the import POST + media list GET here.
    //
    // Step 1: import via the existing CLI runImport equivalent.
    // The simplest path is to reuse the test fixture import that
    // e2e_shared_test.go already established. If a shared helper
    // doesn't exist, copy the import logic from that file.

    resp, err := client.Get(base + "/api/v1/media")
    require.NoError(t, err)
    var mediaList struct {
        Items []struct{ ID string `json:"id"` } `json:"items"`
    }
    require.NoError(t, json.NewDecoder(resp.Body).Decode(&mediaList))
    require.NoError(t, resp.Body.Close())

    if len(mediaList.Items) > 0 {
        return mediaList.Items[0].ID
    }
    require.Fail(t, "no media available — seed fixture before running this test")
    return ""
}
```

> **Implementer note on `importOneMedia`:** the existing `TestSharedE2EHeaderMode` in `internal/cli/e2e_shared_test.go` already imports a fixture into `nasRoot`. Read that file to see the exact import-fixture sequence (`runImport` or the equivalent invocation) and mirror it before issuing the `GET /api/v1/media`. The placeholder above documents the contract; do not commit until the test actually seeds media.

> **Implementer note on `waitForSink`:** this helper already exists in `internal/cli/` test files (used by `TestSharedE2EHeaderMode`). Reuse it; do not redeclare. If it's defined in `e2e_shared_test.go`, both files share package `cli_test` and the helper is already in scope.

- [ ] **Step 2: Inspect `internal/cli/e2e_shared_test.go` for the import sequence and `waitForSink` declaration**

Run: `grep -n "waitForSink\|runImport\|importMediaForTest" internal/cli/e2e_shared_test.go`
Expected: locate both helpers and the exact fixture-import sequence.

- [ ] **Step 3: Replace the `importOneMedia` placeholder with the real import sequence**

Mirror the lines from `e2e_shared_test.go` that drive the import (CLI invocation or HTTP POST), then return the resulting media ID.

- [ ] **Step 4: Run the integration test**

Run: `go test ./internal/cli -run TestE2EBrokerExecPublishesAndRevokes -v`
Expected: PASS in under 5 seconds.

- [ ] **Step 5: Verify it skips under `-short`**

Run: `go test ./internal/cli -short -run TestE2EBrokerExecPublishesAndRevokes -v`
Expected: SKIP, no failures.

- [ ] **Step 6: Run the full suite**

Run: `go test ./...`
Expected: all green.

- [ ] **Step 7: Lint and commit**

Run: `make lint`
Expected: clean.

```bash
git add internal/cli/e2e_brokerexec_test.go
git commit -m "Add Layer C integration test for brokerexec publish/revoke flow"
```

---

## Task 12: Update legacy planning docs

**Files:**
- Modify: `docs/superpowers/specs/2026-04-22-fotobank-go-core-phase1-design.md`
- Modify: `docs/superpowers/plans/2026-04-22-fotobank-go-core-plan-a-foundation.md`

These are historical planning documents from Plan A. They reference the old `register_scope_args` schema. Update so future readers don't see contradictory advice; the implementation is the source of truth, but stale docs mislead.

- [ ] **Step 1: Update the broker config snippet in the phase-1 spec**

In `docs/superpowers/specs/2026-04-22-fotobank-go-core-phase1-design.md`, find the line containing `register_scope_args` (around line 357 per the earlier grep) and replace its surrounding `[broker.exec]` block with the same shape as `internal/config/config.example.toml`'s new block:

```toml
[broker.exec]
# command = "/usr/local/bin/<broker-cli>"
# publish_scope_args = ["scope", "publish"]
# revoke_scope_args  = ["scope", "revoke"]
# call_timeout = "30s"
# env = ["FB_BROKER_ENV=prod"]
```

Keep any surrounding prose intact.

- [ ] **Step 2: Update the same snippet in plan A**

In `docs/superpowers/plans/2026-04-22-fotobank-go-core-plan-a-foundation.md`, find the `RegisterScopeArgs` field reference (around line 789) and the corresponding TOML block; update both to match the new schema:

```go
type BrokerExec struct {
    Command          string        `toml:"command"`
    PublishScopeArgs []string      `toml:"publish_scope_args"`
    RevokeScopeArgs  []string      `toml:"revoke_scope_args"`
    CallTimeout      time.Duration `toml:"call_timeout"`
    Env              []string      `toml:"env"`
}
```

- [ ] **Step 3: Verify nothing else in the repo references the old field**

Run: `grep -rn "RegisterScopeArgs\|register_scope_args" .`
Expected: zero matches (the actual rename has already removed all live references; this catches stragglers in docs).

- [ ] **Step 4: Commit**

```bash
git add docs/superpowers/specs/2026-04-22-fotobank-go-core-phase1-design.md \
        docs/superpowers/plans/2026-04-22-fotobank-go-core-plan-a-foundation.md
git commit -m "Update legacy plan and spec docs to new broker.exec schema"
```

---

## Final verification

- [ ] **Run the full lint and test suite once more.**

Run: `make lint && go test ./...`
Expected: zero warnings, all green.

- [ ] **Smoke test the binary boots in stub mode (no broker CLI required).**

Run: `make build`
Run: `bin/fotobank server --help`
Expected: subcommand help prints; no crash.

- [ ] **Verify a brokerexec config validates.**

Create a temporary `/tmp/fotobank-smoke.toml`:

```toml
[nas]
root = "/tmp/fb-smoke-nas"
[broker]
mode = "exec"
[broker.exec]
command = "/bin/true"
```

Run: `FOTOBANK_CONFIG=/tmp/fotobank-smoke.toml bin/fotobank server --help`
Expected: subcommand help prints (validation passes, server doesn't actually start because we're hitting `--help`).

If the help path doesn't trigger config validation, instead start the server briefly with a fast TICK env var and SIGINT it; the goal is just to confirm `[broker.exec]` validates.

---

## Notes on test ordering and parallelism

- The Layer A unit tests use `t.Setenv` only inside `TestBuildEnvOverlay` and `TestBuildEnvNoOverridesReturnsBase`. Per Go's testing rules, a test that calls `t.Setenv` cannot also call `t.Parallel()` — none of these do, so we're fine.
- The Layer B e2e tests each call `os.Executable()` and spawn the test binary as a child. Don't `t.Parallel()` them inside one package — the child reads env vars set by `Config.Env`, not by the parent's process env, so concurrent tests don't collide. Still, sequential is simpler.
- The Layer C integration test uses `FOTOBANK_TEST_LISTEN_ADDR_SINK`, `FOTOBANK_TEST_SHARE_WORKER_TICK`, and `FOTOBANK_CONFIG`, all set via `t.Setenv`. Don't `t.Parallel()`; existing `TestSharedE2EHeaderMode` doesn't either.
