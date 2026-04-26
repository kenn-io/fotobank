# Fotobank Observability Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Wire structured logging, Prometheus-compatible metrics, deep readiness checks, and a loopback admin HTTP listener into fotobank, per the design at `docs/superpowers/specs/2026-04-25-fotobank-observability-design.md`.

**Architecture:** New `internal/obs` package owns three concerns — `NewLogger` (slog with JSON/text auto-detect), `Metrics` (a wrapper around a private `*metrics.Set` from VictoriaMetrics with typed accessors), and `AdminListener` (a separate `http.Server` mounted on a loopback or unix bind, hosting `/metrics`, `/readyz`, optional pprof). `cli/server.go` constructs both early, calls `slog.SetDefault(logger.With("component","legacy"))` as a transition guard, threads logger + metrics into every existing `Config` that already takes a logger, and runs the admin listener on its own goroutine outside `bgWG` so shutdown is deadlock-free.

**Tech Stack:** Go 1.26, `log/slog` (stdlib), `github.com/VictoriaMetrics/metrics`, `golang.org/x/term` (for `term.IsTerminal`), existing testify/require + huma + net/http.

**Decision pins from the spec (these are NOT to be relitigated):**
- Admin listener bind must be loopback (127.0.0.1, ::1) or `unix:` — config validation rejects anything else.
- Middleware order: `metrics → recovery → identity → display-cache → handler`.
- Shutdown order: flip `obs.Ready` → shut down main API → `bgWG.Wait()` → shut down admin → wait on `adminDone`.
- Histograms use `*metrics.PrometheusHistogram` with explicit `le` buckets (NOT the default vmrange `Histogram`).
- `obs.Metrics.WritePrometheus` writes the private set THEN appends `metrics.WriteProcessMetrics` for `go_*`/`process_*`.
- `slog.SetDefault(baseLogger.With("component","legacy"))` so legacy direct slog.* callsites carry a `component` field.
- Backup last-success is **pushed** by the worker via `m.SetBackupLastSuccess(unixSec)`; not a `MetricSources` closure.
- HTTP request IDs are set as `X-Request-ID` response header BEFORE identity resolution so 401/403 logs carry `req_id`.
- `dur` (Duration) → `dur_ms` (integer ms) and `req` → `req_id` field renames in `httpapi/middleware.go` and `backup/worker.go`.

---

## Task 1: Add VictoriaMetrics/metrics dependency

**Files:**
- Modify: `go.mod`, `go.sum`

- [ ] **Step 1: Add the dep**

```bash
go get github.com/VictoriaMetrics/metrics@latest
```

- [ ] **Step 2: Verify it pulled cleanly**

```bash
go list -m github.com/VictoriaMetrics/metrics
```

Expected: prints `github.com/VictoriaMetrics/metrics vX.Y.Z` with a real version.

- [ ] **Step 3: Verify the API surface we need exists**

```bash
go doc github.com/VictoriaMetrics/metrics NewSet
go doc github.com/VictoriaMetrics/metrics WriteProcessMetrics
go doc github.com/VictoriaMetrics/metrics 'NewPrometheusHistogram*'
```

Expected: at least `NewSet()`, `Set` methods (`NewCounter`, `NewGauge`, `WritePrometheus`), `WriteProcessMetrics(io.Writer)`, and either `NewPrometheusHistogram(name)` (auto buckets) or `NewPrometheusHistogramExt(name, buckets)` (explicit buckets) on either `*Set` or the package.

If the explicit-bucket API is named differently, write down the actual symbol — Task 4 will use it. **Do not silently substitute auto-bucket histograms.**

- [ ] **Step 4: Tidy and verify build**

```bash
go mod tidy
go build ./...
```

Expected: clean.

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum
git commit -m "Add VictoriaMetrics/metrics dependency for observability sub-spec"
```

---

## Task 2: Config schema for [observability]

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

- [ ] **Step 1: Write failing tests for the new schema**

Append to `internal/config/config_test.go`:

```go
func TestObservabilityDefaults(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "c.toml")
	require.NoError(t, os.WriteFile(p, []byte(`
[nas]
root = "/tmp/nas"
`), 0o600))
	cfg, err := config.Load(p)
	require.NoError(t, err)
	require.True(t, cfg.Observability.AdminEnabled)
	require.Equal(t, "127.0.0.1:9090", cfg.Observability.AdminListen)
	require.False(t, cfg.Observability.PprofEnabled)
	require.Equal(t, "auto", cfg.Observability.Logging.Format)
	require.Equal(t, "info", cfg.Observability.Logging.Level)
	require.False(t, cfg.Observability.Logging.AddSource)
}

func TestObservabilityRejectsNonLoopbackAdmin(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "c.toml")
	require.NoError(t, os.WriteFile(p, []byte(`
[nas]
root = "/tmp/nas"
[observability]
admin_enabled = true
admin_listen = "0.0.0.0:9090"
`), 0o600))
	_, err := config.Load(p)
	require.ErrorIs(t, err, errs.ErrBadConfiguration)
	require.Contains(t, err.Error(), "loopback")
}

func TestObservabilityAcceptsLoopbackAndUnix(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:9090", "[::1]:0", "unix:/tmp/fb.sock"} {
		tmp := t.TempDir()
		p := filepath.Join(tmp, "c.toml")
		require.NoError(t, os.WriteFile(p, []byte(`
[nas]
root = "/tmp/nas"
[observability]
admin_listen = "`+addr+`"
`), 0o600))
		_, err := config.Load(p)
		require.NoError(t, err, "addr=%s must be accepted", addr)
	}
}

func TestObservabilityDisabledSkipsAdminListenValidation(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "c.toml")
	require.NoError(t, os.WriteFile(p, []byte(`
[nas]
root = "/tmp/nas"
[observability]
admin_enabled = false
admin_listen = "192.168.1.5:9090"
`), 0o600))
	cfg, err := config.Load(p)
	require.NoError(t, err)
	require.False(t, cfg.Observability.AdminEnabled)
}

func TestObservabilityRejectsBadFormatAndLevel(t *testing.T) {
	for _, body := range []string{
		`[observability.logging]
format = "xml"`,
		`[observability.logging]
level = "verbose"`,
	} {
		tmp := t.TempDir()
		p := filepath.Join(tmp, "c.toml")
		require.NoError(t, os.WriteFile(p, []byte(`
[nas]
root = "/tmp/nas"
`+body), 0o600))
		_, err := config.Load(p)
		require.ErrorIs(t, err, errs.ErrBadConfiguration, "body=%q must reject", body)
	}
}
```

- [ ] **Step 2: Run the tests, expect compile failure**

```bash
go test ./internal/config/ -run TestObservability -count=1
```

Expected: `cfg.Observability` undefined.

- [ ] **Step 3: Add the schema in `internal/config/config.go`**

Add to the `Config` struct field list:

```go
type Config struct {
	// ... existing fields ...
	Observability Observability `toml:"observability"`
}
```

Add new types beneath the existing per-table types:

```go
type Observability struct {
	AdminEnabled bool                `toml:"admin_enabled"`
	AdminListen  string              `toml:"admin_listen"`
	PprofEnabled bool                `toml:"pprof_enabled"`
	Logging      ObservabilityLogging `toml:"logging"`
}

type ObservabilityLogging struct {
	Format    string `toml:"format"`
	Level     string `toml:"level"`
	AddSource bool   `toml:"add_source"`
}
```

- [ ] **Step 4: Add defaults in `applyDefaults`**

Use `meta.IsDefined("observability", "admin_enabled")` to detect explicit opt-out, matching the existing `Backup.Enabled` pattern. The earlier zero-value-vs-else-block approach was a footgun: an operator writing `[observability]\npprof_enabled = true` would silently disable the admin listener. Append to the function body (after the existing `Backup` defaults block):

```go
	// Observability defaults: admin listener on by default at loopback
	// 9090; auto-format logging at info; pprof off; add_source off.
	// Use meta.IsDefined so an operator who writes [observability] for
	// other fields (e.g. pprof_enabled) still gets AdminEnabled=true
	// unless they explicitly set admin_enabled=false.
	if !meta.IsDefined("observability", "admin_enabled") {
		c.Observability.AdminEnabled = true
	}
	if c.Observability.AdminListen == "" {
		c.Observability.AdminListen = "127.0.0.1:9090"
	}
	if c.Observability.Logging.Format == "" {
		c.Observability.Logging.Format = "auto"
	}
	if c.Observability.Logging.Level == "" {
		c.Observability.Logging.Level = "info"
	}
```

- [ ] **Step 5: Add validation in `Validate`**

Append to `(c *Config) Validate()`:

```go
	if c.Observability.AdminEnabled {
		if !isLoopbackOrUnixListen(c.Observability.AdminListen) {
			return fmt.Errorf("%w: observability.admin_listen must be loopback (127.0.0.1, ::1) or unix:; got %q",
				errs.ErrBadConfiguration, c.Observability.AdminListen)
		}
	}
	switch c.Observability.Logging.Format {
	case "auto", "json", "text":
	default:
		return fmt.Errorf("%w: observability.logging.format=%q (must be auto|json|text)",
			errs.ErrBadConfiguration, c.Observability.Logging.Format)
	}
	switch c.Observability.Logging.Level {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("%w: observability.logging.level=%q (must be debug|info|warn|error)",
			errs.ErrBadConfiguration, c.Observability.Logging.Level)
	}
```

Add the helper at the bottom of the file:

```go
// isLoopbackOrUnixListen reports whether addr is a loopback TCP bind or
// a unix-socket path. The admin listener carries unauthenticated
// /metrics and optionally pprof, so non-loopback binds are rejected at
// validation time as defense in depth. We accept only literal loopback
// IPs (127.0.0.1, ::1, expanded forms) and unix: paths — `localhost`
// is rejected because /etc/hosts mappings can vary and could resolve
// to a non-loopback address in unusual environments.
func isLoopbackOrUnixListen(addr string) bool {
	if strings.HasPrefix(addr, "unix:") {
		return true
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return true
	}
	return false
}
```

Add `"net"` and `"strings"` to imports if not already present.

- [ ] **Step 6: Run tests, expect pass**

```bash
go test ./internal/config/ -run TestObservability -count=1
```

Expected: PASS for all five.

- [ ] **Step 7: Run the full config test suite**

```bash
go test ./internal/config/ -count=1
```

Expected: all green.

- [ ] **Step 8: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "Add [observability] config schema with loopback validation"
```

---

## Task 3: obs.NewLogger with TTY-detection seam

**Files:**
- Create: `internal/obs/logger.go`
- Create: `internal/obs/logger_test.go`

- [ ] **Step 1: Write the failing tests**

```go
package obs

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewLogger_BufferAutoIsJSON(t *testing.T) {
	var buf bytes.Buffer
	lg := NewLogger(LoggerConfig{Format: "auto", Level: "info"}, &buf)
	lg.Info("hello", "k", "v")
	out := buf.String()
	require.True(t, strings.HasPrefix(strings.TrimSpace(out), "{"),
		"auto on a non-TTY writer must produce JSON; got %q", out)
}

func TestNewLogger_TerminalAutoIsText(t *testing.T) {
	prev := isTerminal
	isTerminal = func(uintptr) bool { return true }
	t.Cleanup(func() { isTerminal = prev })

	var ttyish ttyWriter
	lg := NewLogger(LoggerConfig{Format: "auto", Level: "info"}, &ttyish)
	lg.Info("hello", "k", "v")
	out := ttyish.buf.String()
	require.False(t, strings.HasPrefix(strings.TrimSpace(out), "{"),
		"auto on a TTY-shaped writer must produce text; got %q", out)
	require.Contains(t, out, "hello")
	require.Contains(t, out, "k=v")
}

func TestNewLogger_ForcedJSON(t *testing.T) {
	var buf bytes.Buffer
	lg := NewLogger(LoggerConfig{Format: "json", Level: "info"}, &buf)
	lg.Info("x")
	require.True(t, strings.HasPrefix(strings.TrimSpace(buf.String()), "{"))
}

func TestNewLogger_ForcedText(t *testing.T) {
	var buf bytes.Buffer
	lg := NewLogger(LoggerConfig{Format: "text", Level: "info"}, &buf)
	lg.Info("x")
	require.False(t, strings.HasPrefix(strings.TrimSpace(buf.String()), "{"))
}

func TestNewLogger_LevelDebugVisible(t *testing.T) {
	var buf bytes.Buffer
	lg := NewLogger(LoggerConfig{Format: "json", Level: "debug"}, &buf)
	lg.Debug("dbg")
	require.Contains(t, buf.String(), `"msg":"dbg"`)
}

func TestNewLogger_LevelInfoSuppressesDebug(t *testing.T) {
	var buf bytes.Buffer
	lg := NewLogger(LoggerConfig{Format: "json", Level: "info"}, &buf)
	lg.Debug("dbg")
	require.Empty(t, buf.String())
}

func TestNewLogger_EnvOverridesLevel(t *testing.T) {
	t.Setenv("FOTOBANK_LOG_LEVEL", "debug")
	var buf bytes.Buffer
	lg := NewLogger(LoggerConfig{Format: "json", Level: "info"}, &buf)
	lg.Debug("dbg")
	require.Contains(t, buf.String(), `"msg":"dbg"`)
}

func TestNewLogger_BogusEnvLevelFallsBackSilently(t *testing.T) {
	t.Setenv("FOTOBANK_LOG_LEVEL", "verbose")
	var buf bytes.Buffer
	lg := NewLogger(LoggerConfig{Format: "json", Level: "warn"}, &buf)
	// Bogus env keeps the configured warn level: info should be silent.
	lg.Info("info")
	require.Empty(t, buf.String())
	lg.Warn("warn")
	require.Contains(t, buf.String(), `"msg":"warn"`)
}

func TestNewLogger_AddSource(t *testing.T) {
	var buf bytes.Buffer
	lg := NewLogger(LoggerConfig{Format: "json", Level: "info", AddSource: true}, &buf)
	lg.Info("here")
	require.Contains(t, buf.String(), `"source"`)
}

// ttyWriter is an io.Writer that exposes Fd() so isTTYWriter type-
// asserts successfully. The stub `isTerminal` controls the answer.
type ttyWriter struct {
	buf bytes.Buffer
}

func (t *ttyWriter) Write(p []byte) (int, error) { return t.buf.Write(p) }
func (t *ttyWriter) Fd() uintptr                  { return 1 }

// Ensure slog uses our handler, not the default.
var _ slog.Handler = (*slog.JSONHandler)(nil)
```

- [ ] **Step 2: Run the tests, expect compile failure**

```bash
go test ./internal/obs/ -count=1
```

Expected: package not found / NewLogger undefined.

- [ ] **Step 3: Implement `internal/obs/logger.go`**

```go
// Package obs is fotobank's observability layer: the canonical slog
// logger, a private metrics registry, and the admin HTTP listener that
// hosts /metrics, /readyz, and (optionally) pprof. See
// docs/superpowers/specs/2026-04-25-fotobank-observability-design.md.
package obs

import (
	"io"
	"log/slog"
	"os"

	"golang.org/x/term"
)

// LoggerConfig is the obs-package mirror of config.ObservabilityLogging.
// Kept independent from internal/config so tests in this package don't
// import the heavier config layer.
type LoggerConfig struct {
	Format    string // "auto" | "json" | "text"
	Level     string // "debug" | "info" | "warn" | "error"
	AddSource bool
}

// NewLogger constructs the canonical *slog.Logger. Format "auto" picks
// JSON when stderr is not a TTY, text otherwise; explicit "json"/"text"
// overrides. FOTOBANK_LOG_LEVEL overrides the configured level when
// set to a recognized value; bogus env values are silently ignored
// (the configured level wins). Tests pass a bytes.Buffer-shaped writer
// to deterministically get JSON.
func NewLogger(cfg LoggerConfig, stderr io.Writer) *slog.Logger {
	level := parseLevel(cfg.Level)
	if env := os.Getenv("FOTOBANK_LOG_LEVEL"); env != "" {
		if l, ok := tryParseLevel(env); ok {
			level = l
		}
	}
	format := cfg.Format
	if format == "auto" {
		if isTTYWriter(stderr) {
			format = "text"
		} else {
			format = "json"
		}
	}
	opts := &slog.HandlerOptions{Level: level, AddSource: cfg.AddSource}
	if format == "json" {
		return slog.New(slog.NewJSONHandler(stderr, opts))
	}
	return slog.New(slog.NewTextHandler(stderr, opts))
}

// parseLevel converts a config-validated level string. Validation
// already ensures the value is one of the four; an unknown value here
// would be a programmer error and we conservatively return Info.
func parseLevel(s string) slog.Level {
	if l, ok := tryParseLevel(s); ok {
		return l
	}
	return slog.LevelInfo
}

func tryParseLevel(s string) (slog.Level, bool) {
	switch s {
	case "debug":
		return slog.LevelDebug, true
	case "info":
		return slog.LevelInfo, true
	case "warn":
		return slog.LevelWarn, true
	case "error":
		return slog.LevelError, true
	}
	return 0, false
}

// isTerminal is the test seam over term.IsTerminal. Tests stub it to
// force terminal/non-terminal decisions independent of the actual
// stdout/stderr the test harness inherits.
var isTerminal = func(fd uintptr) bool { return term.IsTerminal(int(fd)) }

func isTTYWriter(w io.Writer) bool {
	f, ok := w.(interface{ Fd() uintptr })
	return ok && isTerminal(f.Fd())
}
```

- [ ] **Step 4: Pull in golang.org/x/term**

```bash
go get golang.org/x/term@latest
go mod tidy
```

- [ ] **Step 5: Run tests, expect pass**

```bash
go test ./internal/obs/ -count=1
```

Expected: all 9 tests pass.

- [ ] **Step 6: Commit**

```bash
git add internal/obs/logger.go internal/obs/logger_test.go go.mod go.sum
git commit -m "Add obs.NewLogger with TTY-detection seam and env level override"
```

---

## Task 4: obs.Metrics — typed accessors and WritePrometheus

**Files:**
- Create: `internal/obs/metrics.go`
- Create: `internal/obs/metrics_test.go`

- [ ] **Step 1: Write failing tests**

```go
package obs

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewMetrics_BuildInfoEmitted(t *testing.T) {
	m := NewMetrics(MetricSources{}, BuildInfo{
		Version: "v1.2.3", Commit: "abc1234", BuildDate: "2026-04-25",
	})
	var buf bytes.Buffer
	m.WritePrometheus(&buf)
	out := buf.String()
	require.Contains(t, out, `fotobank_build_info{`)
	require.Contains(t, out, `version="v1.2.3"`)
	require.Contains(t, out, `commit="abc1234"`)
	require.Contains(t, out, `build_date="2026-04-25"`)
}

func TestNewMetrics_HTTPCounterIncrements(t *testing.T) {
	m := NewTestMetrics()
	m.HTTPRequests("GET", "/api/v1/healthz", "2xx").Inc()
	m.HTTPRequests("GET", "/api/v1/healthz", "2xx").Inc()
	m.HTTPRequests("POST", "/api/v1/albums", "4xx").Inc()
	require.EqualValues(t, 2, m.HTTPRequests("GET", "/api/v1/healthz", "2xx").Get())
	require.EqualValues(t, 1, m.HTTPRequests("POST", "/api/v1/albums", "4xx").Get())
}

func TestNewMetrics_HTTPDurationObserved(t *testing.T) {
	m := NewTestMetrics()
	m.HTTPRequestDuration("GET", "/api/v1/healthz").Update(0.012)
	m.HTTPRequestDuration("GET", "/api/v1/healthz").Update(0.007)

	var buf bytes.Buffer
	m.WritePrometheus(&buf)
	out := buf.String()
	require.Contains(t, out, `fotobank_http_request_duration_seconds_count{method="GET",route="/api/v1/healthz"} 2`)
	// Explicit le-bucketed series confirm we got a Prometheus histogram,
	// not the default vmrange.
	require.Contains(t, out, `fotobank_http_request_duration_seconds_bucket{`)
	require.Contains(t, out, `le="0.025"`)
}

func TestNewMetrics_WorkerAccessors(t *testing.T) {
	m := NewTestMetrics()
	m.ThumbJobs("ok").Inc()
	m.ThumbJobs("failed").Inc()
	m.ThumbLeasesSwept().Add(3)
	m.SharePublishes("retry").Inc()
	m.ShareRevokes("ok").Inc()
	m.BackupSnapshots("ok").Inc()
	m.BackupRetentionDeleted().Add(5)

	require.EqualValues(t, 1, m.ThumbJobs("ok").Get())
	require.EqualValues(t, 1, m.ThumbJobs("failed").Get())
	require.EqualValues(t, 3, m.ThumbLeasesSwept().Get())
	require.EqualValues(t, 1, m.SharePublishes("retry").Get())
	require.EqualValues(t, 1, m.ShareRevokes("ok").Get())
	require.EqualValues(t, 1, m.BackupSnapshots("ok").Get())
	require.EqualValues(t, 5, m.BackupRetentionDeleted().Get())
}

func TestNewMetrics_BackupLastSuccessPushed(t *testing.T) {
	m := NewTestMetrics()
	// Default: never succeeded. seconds_since_last_success == -1.
	var buf bytes.Buffer
	m.WritePrometheus(&buf)
	require.Contains(t, buf.String(), `fotobank_backup_seconds_since_last_success -1`)
	require.Contains(t, buf.String(), `fotobank_backup_last_success_unix 0`)

	// After push, seconds_since_last_success becomes a non-negative number.
	m.SetBackupLastSuccess(1_700_000_000)
	buf.Reset()
	m.WritePrometheus(&buf)
	out := buf.String()
	require.Contains(t, out, `fotobank_backup_last_success_unix 1.7e+09`)
	require.NotContains(t, out, `fotobank_backup_seconds_since_last_success -1`)
}

func TestNewMetrics_ProcessMetricsAppended(t *testing.T) {
	m := NewTestMetrics()
	var buf bytes.Buffer
	m.WritePrometheus(&buf)
	out := buf.String()
	// Standard runtime metrics appended via metrics.WriteProcessMetrics.
	require.Contains(t, out, "go_goroutines")
	require.True(t, strings.Contains(out, "go_memstats") || strings.Contains(out, "process_"),
		"expected at least one go_memstats_* or process_* series; got %q", out)
}
```

- [ ] **Step 2: Run tests, expect compile failure**

```bash
go test ./internal/obs/ -run TestNewMetrics -count=1
```

Expected: NewMetrics undefined.

- [ ] **Step 3: Implement `internal/obs/metrics.go`**

```go
package obs

import (
	"io"
	"sync/atomic"
	"time"

	"github.com/VictoriaMetrics/metrics"
)

// BuildInfo carries the build labels emitted by fotobank_build_info.
// Filled from internal/version at boot.
type BuildInfo struct {
	Version   string
	Commit    string
	BuildDate string
}

// MetricSources are pull-side closures invoked at scrape time to fill
// derived gauges. nil entries are tolerated and resolve to zero.
type MetricSources struct {
	ThumbQueueDepth  func(state string) int64
	SharePendingByOp func(op string) int64
}

// Metrics owns a private *metrics.Set, never the upstream global. All
// counters/histograms/gauges fotobank emits live on this set; the
// process metrics from metrics.WriteProcessMetrics are appended at
// scrape time but not registered.
type Metrics struct {
	set *metrics.Set

	// Backup last-success unix seconds — pushed by backup.Worker on
	// each successful Snapshot. Stored atomically so the scrape closure
	// can read consistently.
	lastBackupUnix atomic.Int64
}

// httpDurationBuckets and workerDurationBuckets are the Prometheus le
// buckets pinned by the spec. HTTP traffic skews fast (5ms..10s);
// worker traffic skews slower (50ms..60s).
var (
	httpDurationBuckets   = []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10}
	workerDurationBuckets = []float64{.05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60}
)

// NewMetrics constructs the production metrics registry. BuildInfo is
// emitted as fotobank_build_info{version,commit,build_date}=1; pull
// sources fill derived gauges.
func NewMetrics(src MetricSources, build BuildInfo) *Metrics {
	m := &Metrics{set: metrics.NewSet()}

	// Build info: a constant 1 with build labels. Use a gauge with
	// closure returning 1.0 for trivial registration.
	m.set.NewGauge(`fotobank_build_info{version="`+escapeLabel(build.Version)+
		`",commit="`+escapeLabel(build.Commit)+
		`",build_date="`+escapeLabel(build.BuildDate)+`"}`,
		func() float64 { return 1 })

	// Backup pushed gauge + derived seconds-since gauge.
	m.set.NewGauge("fotobank_backup_last_success_unix", func() float64 {
		return float64(m.lastBackupUnix.Load())
	})
	m.set.NewGauge("fotobank_backup_seconds_since_last_success", func() float64 {
		last := m.lastBackupUnix.Load()
		if last == 0 {
			return -1
		}
		return float64(time.Now().Unix() - last)
	})

	// Pull-source gauges. Nil-safe — closures return 0.
	for _, state := range []string{"pending", "working", "failed", "no_preview"} {
		state := state
		m.set.NewGauge(`fotobank_thumb_queue_depth{state="`+state+`"}`, func() float64 {
			if src.ThumbQueueDepth == nil {
				return 0
			}
			return float64(src.ThumbQueueDepth(state))
		})
	}
	for _, op := range []string{"publish", "revoke"} {
		op := op
		m.set.NewGauge(`fotobank_share_pending_total{op="`+op+`"}`, func() float64 {
			if src.SharePendingByOp == nil {
				return 0
			}
			return float64(src.SharePendingByOp(op))
		})
	}
	return m
}

// NewTestMetrics builds a Metrics with empty sources and zero build
// info. Tests use this to assert on counter/histogram values without
// needing a fixed registry order or the upstream global.
func NewTestMetrics() *Metrics {
	return NewMetrics(MetricSources{}, BuildInfo{})
}

// SetBackupLastSuccess records the unix-seconds time of the last
// successful backup snapshot. Called from backup.Worker after each
// successful Snapshot. Concurrent-safe.
func (m *Metrics) SetBackupLastSuccess(unix int64) {
	m.lastBackupUnix.Store(unix)
}

// HTTPRequests returns the counter for a (method, route, status_class)
// triple. Counters are auto-created on first call and cached by the
// upstream Set under their full name+labels signature.
func (m *Metrics) HTTPRequests(method, route, statusClass string) *metrics.Counter {
	return m.set.GetOrCreateCounter(`fotobank_http_requests_total{method="` +
		escapeLabel(method) + `",route="` + escapeLabel(route) +
		`",status_class="` + escapeLabel(statusClass) + `"}`)
}

// HTTPRequestDuration returns the explicit-le-bucketed histogram for a
// (method, route) pair.
func (m *Metrics) HTTPRequestDuration(method, route string) *metrics.PrometheusHistogram {
	return m.getOrCreatePrometheusHistogram(
		`fotobank_http_request_duration_seconds`,
		map[string]string{"method": method, "route": route},
		httpDurationBuckets,
	)
}

func (m *Metrics) ThumbJobs(result string) *metrics.Counter {
	return m.set.GetOrCreateCounter(`fotobank_thumb_jobs_total{result="` + escapeLabel(result) + `"}`)
}

func (m *Metrics) ThumbJobDuration(result string) *metrics.PrometheusHistogram {
	return m.getOrCreatePrometheusHistogram(
		`fotobank_thumb_job_duration_seconds`,
		map[string]string{"result": result},
		workerDurationBuckets,
	)
}

func (m *Metrics) ThumbLeasesSwept() *metrics.Counter {
	return m.set.GetOrCreateCounter(`fotobank_thumb_leases_swept_total`)
}

func (m *Metrics) SharePublishes(result string) *metrics.Counter {
	return m.set.GetOrCreateCounter(`fotobank_share_publishes_total{result="` + escapeLabel(result) + `"}`)
}

func (m *Metrics) SharePublishDuration(result string) *metrics.PrometheusHistogram {
	return m.getOrCreatePrometheusHistogram(
		`fotobank_share_publish_duration_seconds`,
		map[string]string{"result": result},
		workerDurationBuckets,
	)
}

func (m *Metrics) ShareRevokes(result string) *metrics.Counter {
	return m.set.GetOrCreateCounter(`fotobank_share_revokes_total{result="` + escapeLabel(result) + `"}`)
}

func (m *Metrics) ShareRevokeDuration(result string) *metrics.PrometheusHistogram {
	return m.getOrCreatePrometheusHistogram(
		`fotobank_share_revoke_duration_seconds`,
		map[string]string{"result": result},
		workerDurationBuckets,
	)
}

func (m *Metrics) BackupSnapshots(result string) *metrics.Counter {
	return m.set.GetOrCreateCounter(`fotobank_backup_snapshots_total{result="` + escapeLabel(result) + `"}`)
}

func (m *Metrics) BackupSnapshotDuration(result string) *metrics.PrometheusHistogram {
	return m.getOrCreatePrometheusHistogram(
		`fotobank_backup_snapshot_duration_seconds`,
		map[string]string{"result": result},
		workerDurationBuckets,
	)
}

func (m *Metrics) BackupRetentionSweeps(result string) *metrics.Counter {
	return m.set.GetOrCreateCounter(`fotobank_backup_retention_sweeps_total{result="` + escapeLabel(result) + `"}`)
}

func (m *Metrics) BackupRetentionDeleted() *metrics.Counter {
	return m.set.GetOrCreateCounter(`fotobank_backup_retention_deleted_total`)
}

// WritePrometheus writes the private set's Prometheus exposition,
// followed by stdlib runtime/process metrics from the upstream helper.
// metrics.WriteProcessMetrics reads runtime state directly and does
// not depend on the upstream global registry, so calling it from a
// private-set context is safe.
func (m *Metrics) WritePrometheus(w io.Writer) {
	m.set.WritePrometheus(w)
	metrics.WriteProcessMetrics(w)
}
```

Add a small helper file for label escaping and histogram registration:

`internal/obs/metrics_helpers.go`:

```go
package obs

import (
	"sort"
	"strings"

	"github.com/VictoriaMetrics/metrics"
)

// escapeLabel applies the minimum Prometheus label-value escaping
// required by the upstream metrics library: backslash and double-quote
// must be \-escaped; newline becomes \n. We never expect newlines in
// label values, but we escape defensively.
func escapeLabel(s string) string {
	if !strings.ContainsAny(s, `"\`+"\n") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 2)
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// getOrCreatePrometheusHistogram registers (or returns the cached)
// Prometheus-buckets histogram for name+labels with the given le
// buckets. The upstream library keys instruments by their full
// "name{labels}" string, so we build it deterministically (labels
// sorted) and delegate to the set's GetOrCreatePrometheusHistogramExt
// helper.
//
// If the upstream library exposes a different symbol for explicit-
// bucket histograms (e.g. NewPrometheusHistogramExt with a different
// signature, or a Set-method form), adapt this call accordingly.
// Verify against `go doc` output from Task 1 step 3.
func (m *Metrics) getOrCreatePrometheusHistogram(
	name string,
	labels map[string]string,
	buckets []float64,
) *metrics.PrometheusHistogram {
	full := name
	if len(labels) > 0 {
		full = name + "{" + buildLabelString(labels) + "}"
	}
	return m.set.GetOrCreatePrometheusHistogramExt(full, buckets)
}

func buildLabelString(labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(k)
		b.WriteString(`="`)
		b.WriteString(escapeLabel(labels[k]))
		b.WriteByte('"')
	}
	return b.String()
}
```

Note on `GetOrCreatePrometheusHistogramExt`: this is the assumed symbol per the spec. Task 1 step 3 verified the actual API; if the upstream library uses a different name (e.g. `(s *Set).NewPrometheusHistogramExt(name, buckets)` returning a freshly-registered histogram, or a package-level `metrics.NewPrometheusHistogramExt(name, buckets)` writing to the global registry), adjust this helper to register-once-then-cache locally. Fallback strategy if no `Get-or-create` form exists:

```go
type Metrics struct {
	set *metrics.Set
	mu  sync.Mutex
	histos map[string]*metrics.PrometheusHistogram
}

func (m *Metrics) getOrCreatePrometheusHistogram(...) *metrics.PrometheusHistogram {
	m.mu.Lock()
	defer m.mu.Unlock()
	if h, ok := m.histos[full]; ok {
		return h
	}
	h := metrics.NewPrometheusHistogramExt(m.set, full, buckets) // adapt signature
	m.histos[full] = h
	return h
}
```

The implementer chooses the cleanest variant against the actual upstream API.

- [ ] **Step 4: Run tests, expect pass**

```bash
go test ./internal/obs/ -run TestNewMetrics -count=1
```

Expected: all 6 tests pass. If any fail with "undefined symbol", check the upstream API and adapt per Task 1 step 3 + the fallback above.

- [ ] **Step 5: Run full obs test suite**

```bash
go test ./internal/obs/ -count=1
```

Expected: all green.

- [ ] **Step 6: Commit**

```bash
git add internal/obs/metrics.go internal/obs/metrics_helpers.go internal/obs/metrics_test.go
git commit -m "Add obs.Metrics with private set, typed accessors, process metrics"
```

---

## Task 5: obs.Ready and ReadyzHandler

**Files:**
- Create: `internal/obs/ready.go`
- Create: `internal/obs/ready_test.go`

- [ ] **Step 1: Write failing tests**

```go
package obs

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestReady_FlipsOnShutdown(t *testing.T) {
	r := require.New(t)
	ready := NewReady()
	checks := []ReadyCheck{{
		Name: "always_ok",
		Fn:   func(context.Context) error { return nil },
	}}
	h := NewReadyzHandler(ready, checks, ReadyzConfig{
		DeadlineTotal: 50 * time.Millisecond,
		CacheTTL:      0, // no cache for this test
	})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
	r.Equal(200, rec.Code)

	ready.Store(false)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
	r.Equal(503, rec.Code)
}

func TestReadyz_BoundedDeadline(t *testing.T) {
	r := require.New(t)
	ready := NewReady()
	checks := []ReadyCheck{{
		Name: "slow",
		Fn: func(ctx context.Context) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(200 * time.Millisecond):
				return nil
			}
		},
	}}
	h := NewReadyzHandler(ready, checks, ReadyzConfig{
		DeadlineTotal: 50 * time.Millisecond,
		CacheTTL:      0,
	})

	start := time.Now()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
	elapsed := time.Since(start)
	r.Less(elapsed, 100*time.Millisecond,
		"deadline 50ms must surface fast; took %s", elapsed)
	r.Equal(503, rec.Code)
	r.Contains(rec.Body.String(), `"slow"`)
	r.Contains(rec.Body.String(), `"fail"`)
}

func TestReadyz_CachesSuccess(t *testing.T) {
	r := require.New(t)
	ready := NewReady()
	var calls atomic.Int64
	checks := []ReadyCheck{{
		Name: "counts",
		Fn: func(context.Context) error {
			calls.Add(1)
			return nil
		},
	}}
	h := NewReadyzHandler(ready, checks, ReadyzConfig{
		DeadlineTotal: time.Second,
		CacheTTL:      time.Hour,
	})
	for i := 0; i < 5; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
		r.Equal(200, rec.Code)
	}
	r.EqualValues(1, calls.Load(), "cache must collapse 5 probes to 1 check pass")
}

func TestReadyz_ShutdownBypassesCache(t *testing.T) {
	r := require.New(t)
	ready := NewReady()
	checks := []ReadyCheck{{
		Name: "ok", Fn: func(context.Context) error { return nil },
	}}
	h := NewReadyzHandler(ready, checks, ReadyzConfig{
		DeadlineTotal: time.Second,
		CacheTTL:      time.Hour,
	})
	// Prime the cache with a 200.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
	r.Equal(200, rec.Code)

	// Flip ready false; cache is still warm but must be bypassed.
	ready.Store(false)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
	r.Equal(503, rec.Code)
	r.Contains(rec.Body.String(), `"shutting_down"`)
}

func TestReadyz_SingleflightCollapsesConcurrentMisses(t *testing.T) {
	r := require.New(t)
	ready := NewReady()
	var calls atomic.Int64
	gate := make(chan struct{})
	checks := []ReadyCheck{{
		Name: "blocking",
		Fn: func(context.Context) error {
			<-gate
			calls.Add(1)
			return nil
		},
	}}
	h := NewReadyzHandler(ready, checks, ReadyzConfig{
		DeadlineTotal: time.Second,
		CacheTTL:      0, // every probe misses the cache
	})

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
		}()
	}
	// Give the goroutines time to all enter the singleflight wait.
	time.Sleep(20 * time.Millisecond)
	close(gate)
	wg.Wait()
	r.EqualValues(1, calls.Load(),
		"singleflight must collapse 10 concurrent probes into 1 check pass")
}

func TestReadyz_PerCheckErrorsAggregated(t *testing.T) {
	r := require.New(t)
	ready := NewReady()
	checks := []ReadyCheck{
		{Name: "ok", Fn: func(context.Context) error { return nil }},
		{Name: "broken", Fn: func(context.Context) error { return errors.New("dial: refused") }},
	}
	h := NewReadyzHandler(ready, checks, ReadyzConfig{
		DeadlineTotal: time.Second,
		CacheTTL:      0,
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
	r.Equal(503, rec.Code)
	r.Contains(rec.Body.String(), `"ok"`)
	r.Contains(rec.Body.String(), `"broken"`)
	r.Contains(rec.Body.String(), `dial: refused`)
}
```

- [ ] **Step 2: Run tests, expect compile failure**

```bash
go test ./internal/obs/ -run TestReady -count=1
```

Expected: NewReady / NewReadyzHandler undefined.

- [ ] **Step 3: Implement `internal/obs/ready.go`**

```go
package obs

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// Ready is the boolean readiness gate flipped by graceful shutdown.
// Once false, every /readyz probe returns 503 immediately, bypassing
// the deep-check cache so scrapers see the transition before the main
// listener stops accepting requests.
type Ready struct {
	v atomic.Bool
}

func NewReady() *Ready {
	r := &Ready{}
	r.v.Store(true)
	return r
}

func (r *Ready) Store(ok bool) { r.v.Store(ok) }
func (r *Ready) Load() bool    { return r.v.Load() }

// ReadyCheck is a named probe. Fn must return an error to signal
// failure and respect ctx for cancellation.
type ReadyCheck struct {
	Name string
	Fn   func(ctx context.Context) error
}

// ReadyzConfig tunes the deadline and cache TTL. Production uses 2s/5s;
// unit tests use 20-50ms / 0.
type ReadyzConfig struct {
	DeadlineTotal time.Duration
	CacheTTL      time.Duration
}

type readyzResult struct {
	StatusOK bool
	At       time.Time
	Body     []byte
}

type readyzHandler struct {
	ready  *Ready
	checks []ReadyCheck
	cfg    ReadyzConfig

	mu      sync.Mutex
	cache   *readyzResult
	inflight chan struct{} // nil except while a single check pass is running
	pending []chan readyzResult
}

// NewReadyzHandler builds a /readyz HTTP handler. The cfg deadline
// caps the total time the check pass may take; the cache TTL deduplicates
// successive probes. Concurrent cache-miss probes share a single check
// pass via singleflight.
func NewReadyzHandler(ready *Ready, checks []ReadyCheck, cfg ReadyzConfig) http.Handler {
	return &readyzHandler{ready: ready, checks: checks, cfg: cfg}
}

func (h *readyzHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !h.ready.Load() {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":"shutting_down","checks":[]}`))
		return
	}
	res := h.runOrCache(r.Context())
	w.Header().Set("Content-Type", "application/json")
	if res.StatusOK {
		w.WriteHeader(http.StatusOK)
	} else {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	_, _ = w.Write(res.Body)
}

// runOrCache returns a fresh result, either from cache or by running
// the check pass once across concurrent probes.
func (h *readyzHandler) runOrCache(ctx context.Context) readyzResult {
	h.mu.Lock()
	if h.cache != nil && time.Since(h.cache.At) < h.cfg.CacheTTL {
		c := *h.cache
		h.mu.Unlock()
		return c
	}
	if h.inflight != nil {
		// Another goroutine is running the check pass; wait for its
		// result by parking on a fresh channel.
		ch := make(chan readyzResult, 1)
		h.pending = append(h.pending, ch)
		h.mu.Unlock()
		return <-ch
	}
	// We're the leader for this pass.
	h.inflight = make(chan struct{})
	leader := h.inflight
	h.mu.Unlock()

	res := h.runChecks(ctx)

	h.mu.Lock()
	h.cache = &res
	pending := h.pending
	h.pending = nil
	h.inflight = nil
	close(leader)
	h.mu.Unlock()

	for _, ch := range pending {
		ch <- res
	}
	return res
}

func (h *readyzHandler) runChecks(parent context.Context) readyzResult {
	ctx, cancel := context.WithTimeout(parent, h.cfg.DeadlineTotal)
	defer cancel()

	type checkOut struct {
		Name   string `json:"name"`
		Status string `json:"status"`
		Err    string `json:"err,omitempty"`
	}
	out := make([]checkOut, 0, len(h.checks))
	allOK := true
	for _, c := range h.checks {
		err := c.Fn(ctx)
		entry := checkOut{Name: c.Name, Status: "ok"}
		if err != nil {
			allOK = false
			entry.Status = "fail"
			entry.Err = err.Error()
		}
		out = append(out, entry)
	}
	body := struct {
		Status string     `json:"status"`
		Checks []checkOut `json:"checks"`
	}{
		Status: "ok",
		Checks: out,
	}
	if !allOK {
		body.Status = "fail"
	}
	bodyBytes, _ := json.Marshal(body)
	return readyzResult{StatusOK: allOK, At: time.Now(), Body: bodyBytes}
}
```

- [ ] **Step 4: Run tests, expect pass**

```bash
go test ./internal/obs/ -run TestReady -count=1
```

Expected: all 6 tests pass.

- [ ] **Step 5: Run with -race**

```bash
go test ./internal/obs/ -run TestReady -count=1 -race
```

Expected: clean (singleflight is the trickiest concurrent path; the race detector verifies the mutex discipline).

- [ ] **Step 6: Commit**

```bash
git add internal/obs/ready.go internal/obs/ready_test.go
git commit -m "Add obs.Ready + /readyz handler with deadline, cache, singleflight"
```

---

## Task 6: obs.AdminListener

**Files:**
- Create: `internal/obs/admin.go`
- Create: `internal/obs/admin_test.go`

- [ ] **Step 1: Write failing tests**

```go
package obs

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAdminMux_MetricsRoute(t *testing.T) {
	r := require.New(t)
	m := NewTestMetrics()
	m.HTTPRequests("GET", "/x", "2xx").Inc()

	mux := NewAdminMux(AdminConfig{
		Metrics: m,
		Ready:   NewReady(),
		Checks:  nil,
		ReadyzCfg: ReadyzConfig{DeadlineTotal: time.Second, CacheTTL: 0},
		PprofEnabled: false,
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/metrics")
	r.NoError(err)
	defer resp.Body.Close()
	r.Equal(200, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	r.Contains(string(body), "fotobank_http_requests_total")
}

func TestAdminMux_ReadyzRoute(t *testing.T) {
	r := require.New(t)
	mux := NewAdminMux(AdminConfig{
		Metrics:   NewTestMetrics(),
		Ready:     NewReady(),
		Checks:    nil,
		ReadyzCfg: ReadyzConfig{DeadlineTotal: time.Second, CacheTTL: 0},
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/readyz")
	r.NoError(err)
	defer resp.Body.Close()
	r.Equal(200, resp.StatusCode)
}

func TestAdminMux_PprofGated(t *testing.T) {
	r := require.New(t)
	muxOff := NewAdminMux(AdminConfig{
		Metrics:      NewTestMetrics(),
		Ready:        NewReady(),
		ReadyzCfg:    ReadyzConfig{DeadlineTotal: time.Second, CacheTTL: 0},
		PprofEnabled: false,
	})
	srvOff := httptest.NewServer(muxOff)
	defer srvOff.Close()
	resp, err := http.Get(srvOff.URL + "/debug/pprof/")
	r.NoError(err)
	defer resp.Body.Close()
	r.Equal(404, resp.StatusCode)

	muxOn := NewAdminMux(AdminConfig{
		Metrics:      NewTestMetrics(),
		Ready:        NewReady(),
		ReadyzCfg:    ReadyzConfig{DeadlineTotal: time.Second, CacheTTL: 0},
		PprofEnabled: true,
	})
	srvOn := httptest.NewServer(muxOn)
	defer srvOn.Close()
	resp, err = http.Get(srvOn.URL + "/debug/pprof/")
	r.NoError(err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	r.Equal(200, resp.StatusCode)
	r.True(strings.Contains(string(body), "/debug/pprof/") ||
		strings.Contains(string(body), "Profile Descriptions"),
		"pprof index page expected; got %q", string(body))
}
```

- [ ] **Step 2: Run tests, expect compile failure**

```bash
go test ./internal/obs/ -run TestAdminMux -count=1
```

Expected: `NewAdminMux` undefined.

- [ ] **Step 3: Implement `internal/obs/admin.go`**

```go
package obs

import (
	"net/http"
	"net/http/pprof"
)

// AdminConfig assembles the admin listener.
type AdminConfig struct {
	Metrics      *Metrics
	Ready        *Ready
	Checks       []ReadyCheck
	ReadyzCfg    ReadyzConfig
	PprofEnabled bool
}

// NewAdminMux returns the http.Handler that mounts /metrics, /readyz,
// and (when enabled) /debug/pprof/*. No identity middleware; the
// listener it serves on must be loopback or unix-bound.
func NewAdminMux(cfg AdminConfig) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		cfg.Metrics.WritePrometheus(w)
	})
	mux.Handle("/readyz", NewReadyzHandler(cfg.Ready, cfg.Checks, cfg.ReadyzCfg))

	if cfg.PprofEnabled {
		mux.HandleFunc("/debug/pprof/", pprof.Index)
		mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	}
	return mux
}
```

- [ ] **Step 4: Run tests, expect pass**

```bash
go test ./internal/obs/ -run TestAdminMux -count=1
```

Expected: 3 tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/obs/admin.go internal/obs/admin_test.go
git commit -m "Add obs.NewAdminMux: /metrics, /readyz, optional pprof"
```

---

## Task 7: httpapi recovery middleware

**Files:**
- Create: `internal/httpapi/recovery.go`
- Create: `internal/httpapi/recovery_test.go`

- [ ] **Step 1: Write failing tests**

```go
package httpapi

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRecovery_PanicConvertedTo500(t *testing.T) {
	r := require.New(t)
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, nil))

	h := WithRecovery(logger)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))
	r.Equal(500, rec.Code)
	r.Contains(logBuf.String(), `"level":"ERROR"`)
	r.Contains(logBuf.String(), "boom")
}

func TestRecovery_NoPanicPassesThrough(t *testing.T) {
	r := require.New(t)
	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	h := WithRecovery(logger)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(204)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))
	r.Equal(204, rec.Code)
}
```

- [ ] **Step 2: Run tests, expect compile failure**

```bash
go test ./internal/httpapi/ -run TestRecovery -count=1
```

Expected: `WithRecovery` undefined.

- [ ] **Step 3: Implement `internal/httpapi/recovery.go`**

```go
package httpapi

import (
	"log/slog"
	"net/http"
	"runtime/debug"
)

// WithRecovery converts handler panics to HTTP 500 responses and logs
// the panic value plus stack trace at ERROR. Sits inside the metrics
// middleware so the recorded status_class is 5xx.
func WithRecovery(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				rec := recover()
				if rec == nil {
					return
				}
				logger.Error("handler panic",
					"err", rec, "stack", string(debug.Stack()),
					"method", r.Method, "path", r.URL.Path)
				http.Error(w, http.StatusText(http.StatusInternalServerError),
					http.StatusInternalServerError)
			}()
			next.ServeHTTP(w, r)
		})
	}
}
```

- [ ] **Step 4: Run tests, expect pass**

```bash
go test ./internal/httpapi/ -run TestRecovery -count=1
```

Expected: 2 tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/httpapi/recovery.go internal/httpapi/recovery_test.go
git commit -m "Add httpapi.WithRecovery middleware (panic -> 500 + log)"
```

---

## Task 8: httpapi log_context helpers

**Files:**
- Create: `internal/httpapi/log_context.go`
- Modify: `internal/httpapi/middleware.go` (add ctxKey, use new helpers in the next task)

- [ ] **Step 1: Write failing test**

Append to `internal/httpapi/middleware_test.go`:

```go
func TestLoggerFromContext_FallbackToDefault(t *testing.T) {
	// No logger in context -> returns slog.Default(); never returns nil.
	lg := LoggerFromContext(context.Background())
	require.NotNil(t, lg)
}

func TestLoggerFromContext_ReturnsAttached(t *testing.T) {
	r := require.New(t)
	want := slog.New(slog.NewJSONHandler(io.Discard, nil)).With("k", "v")
	ctx := WithLogger(context.Background(), want)
	got := LoggerFromContext(ctx)
	r.Equal(want, got)
}
```

(Add `"context"`, `"io"`, `"log/slog"` to imports if needed.)

- [ ] **Step 2: Run tests, expect compile failure**

```bash
go test ./internal/httpapi/ -run TestLoggerFromContext -count=1
```

Expected: `WithLogger`, `LoggerFromContext` undefined.

- [ ] **Step 3: Implement `internal/httpapi/log_context.go`**

```go
package httpapi

import (
	"context"
	"log/slog"
)

type loggerCtxKey struct{}

// WithLogger attaches the per-request logger to ctx. Middleware does
// this once after building a logger with req_id + principal_* fields;
// handlers fetch via LoggerFromContext.
func WithLogger(ctx context.Context, lg *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerCtxKey{}, lg)
}

// LoggerFromContext returns the per-request logger if attached, else
// slog.Default(). Never returns nil.
func LoggerFromContext(ctx context.Context) *slog.Logger {
	if lg, ok := ctx.Value(loggerCtxKey{}).(*slog.Logger); ok && lg != nil {
		return lg
	}
	return slog.Default()
}
```

- [ ] **Step 4: Run tests, expect pass**

```bash
go test ./internal/httpapi/ -run TestLoggerFromContext -count=1
```

Expected: 2 tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/httpapi/log_context.go internal/httpapi/middleware_test.go
git commit -m "Add httpapi.WithLogger / LoggerFromContext"
```

---

## Task 9: httpapi WithMiddleware refactor

This task is the central wiring change. The existing `WithMiddleware(idp identity.Provider)` becomes `WithMiddleware(WithMiddlewareDeps)` which carries the logger + metrics. It also generates `req_id` BEFORE identity resolution so 401/403 logs carry it, sets `X-Request-ID` response header, runs the metrics + recovery wrap, builds a per-request logger and attaches it to ctx, and renames `dur` → `dur_ms` and `req` → `req_id`.

**Files:**
- Modify: `internal/httpapi/middleware.go`
- Modify: `internal/httpapi/middleware_test.go`
- Modify: `internal/httpapi/api.go` (httpapi.Deps gains Logger, Metrics)

- [ ] **Step 1: Write failing tests**

Append to `internal/httpapi/middleware_test.go`:

```go
// failingProvider is a local test adapter — there is no
// identity.ProviderFunc in the production package; tests construct a
// tiny struct that implements identity.Provider.
type failingProvider struct{ err error }

func (f failingProvider) FromRequest(context.Context, *http.Request) (identity.Identity, error) {
	return identity.Identity{}, f.err
}

func TestRequestIDHeaderSetBeforeIdentity(t *testing.T) {
	r := require.New(t)
	failing := failingProvider{err: errs.ErrIdentityMissing}
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, nil))
	m := obs.NewTestMetrics()

	mw := WithMiddleware(WithMiddlewareDeps{
		Provider: failing,
		Logger:   logger,
		Metrics:  m,
	})
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("identity-rejected request must not reach handler")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))
	r.Equal(401, rec.Code)
	r.NotEmpty(rec.Header().Get("X-Request-ID"),
		"X-Request-ID must be set before identity resolution")
	r.Contains(logBuf.String(), `"req_id"`)
	// Identity-rejected request still increments the 4xx counter.
	r.EqualValues(1, m.HTTPRequests("GET", "unmatched", "4xx").Get(),
		"metrics must record 4xx for identity rejection")
}

func TestPanicRecoveryRecordsAs5xx(t *testing.T) {
	r := require.New(t)
	idp := identity.NewStub(owners.Principal{Hub: "h", UserID: "u"}, "")
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	m := obs.NewTestMetrics()

	mux := http.NewServeMux()
	mux.Handle("GET /panic", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("expected by test")
	}))
	mw := WithMiddleware(WithMiddlewareDeps{
		Provider: idp,
		Logger:   logger,
		Metrics:  m,
	})
	srv := httptest.NewServer(mw(mux))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/panic")
	r.NoError(err)
	defer resp.Body.Close()
	r.Equal(500, resp.StatusCode)
	r.EqualValues(1, m.HTTPRequests("GET", "/panic", "5xx").Get(),
		"panic must be recorded as 5xx after recovery")
}

func TestSuccessfulRequestRecordedAs2xxAndCarriesDurMs(t *testing.T) {
	r := require.New(t)
	idp := identity.NewStub(owners.Principal{Hub: "h", UserID: "u"}, "")
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, nil))
	m := obs.NewTestMetrics()

	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/healthz", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}))
	mw := WithMiddleware(WithMiddlewareDeps{
		Provider: idp,
		Logger:   logger,
		Metrics:  m,
	})
	srv := httptest.NewServer(mw(mux))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/healthz")
	r.NoError(err)
	defer resp.Body.Close()
	r.Equal(200, resp.StatusCode)
	r.EqualValues(1, m.HTTPRequests("GET", "/api/v1/healthz", "2xx").Get())

	out := logBuf.String()
	r.Contains(out, `"dur_ms"`, "log must use dur_ms field name")
	r.Contains(out, `"req_id"`, "log must use req_id field name")
	r.NotContains(out, `"dur":`, "legacy dur field must be gone")
	r.NotContains(out, `"req":`, "legacy req field must be gone")
	r.Contains(out, `"component":"httpapi"`)
	r.Contains(out, `"principal_hub":"h"`)
	r.Contains(out, `"principal_user_id":"u"`)
}
```

(Add imports to the test file as needed: `"bytes"`, `"io"`, `"log/slog"`, `"net/http"`, `"net/http/httptest"`, `"github.com/wesm/fotobank/internal/errs"`, `"github.com/wesm/fotobank/internal/identity"`, `"github.com/wesm/fotobank/internal/obs"`, `"github.com/wesm/fotobank/internal/owners"`.)

- [ ] **Step 2: Run tests, expect compile failure**

```bash
go test ./internal/httpapi/ -run "TestRequestIDHeaderSetBeforeIdentity|TestPanicRecoveryRecordsAs5xx|TestSuccessfulRequestRecorded" -count=1
```

Expected: `WithMiddlewareDeps` undefined / signature mismatch.

- [ ] **Step 3: Replace `WithMiddleware` in `internal/httpapi/middleware.go`**

**Important architectural note (do not skip):** the metrics + final-access-log layer cannot read `r.Pattern` and per-request logger fields directly from the outer `r` after the inner chain returns. Two reasons:

1. `r.WithContext(ctx)` creates a fresh `*Request` — pattern set on the inner request never propagates back to the outer one.
2. `ServeMux` sets `r.Pattern` on a fresh `*Request` it constructs internally just before calling the route handler — control returns to the outer middleware via the original `r` that has no `Pattern` set.

Fix: store a pointer to a small mutable `requestObs` struct in the context. Inner layers (identity, the route-pattern capture handler) update its fields. The outer metrics+log layer reads from it after the chain returns.

For pattern capture: every handler registered on the main mux must be wrapped with `WrapMuxHandler` (defined below), which copies `r.Pattern` from the request that ServeMux passes to the handler into the shared `requestObs`. T13's `cli/server.go` is responsible for applying `WrapMuxHandler` at registration time.

Replace the existing `func WithMiddleware(idp identity.Provider) ...` body and signature with:

```go
type WithMiddlewareDeps struct {
	Provider identity.Provider
	Logger   *slog.Logger
	Metrics  *obs.Metrics
	// RequestIDHeader is the inbound header name to read for an
	// upstream-supplied request ID. Empty means generate a fresh UUID.
	RequestIDHeader string
}

// requestObs is per-request mutable state shared across the middleware
// chain via context. The outer metrics+log layer allocates one per
// request; inner layers (identity, the route-capture handler wrapper)
// mutate fields on it; the outer layer reads them after the chain
// returns. Stored as a pointer so the value the outer layer reads is
// the same one inner layers wrote to.
type requestObs struct {
	routeTemplate string       // "unmatched" until WrapMuxHandler runs
	logger        *slog.Logger // augmented by identityWrap with principal fields
}

type ctxKeyObsType struct{}

var ctxKeyObs ctxKeyObsType

func obsFromContext(ctx context.Context) *requestObs {
	o, _ := ctx.Value(ctxKeyObs).(*requestObs)
	return o
}

// WrapMuxHandler is the per-route wrapper that captures r.Pattern into
// the shared per-request state. ServeMux sets Pattern on the request
// it dispatches to the handler — this wrapper is THE handler from
// ServeMux's perspective, so it sees the populated Pattern. Without
// this wrapper, the outer metrics layer records every request as
// route="unmatched". Apply at registration time via T13's wiring.
func WrapMuxHandler(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if o := obsFromContext(r.Context()); o != nil {
			o.routeTemplate = normalizeRouteTemplate(r.Pattern)
		}
		h.ServeHTTP(w, r)
	})
}

// WithMiddleware composes the main-listener middleware stack:
//
//	metrics → recovery → identity → display-cache (when configured) → handler
//
// Recovery is inside metrics so a recovered 5xx is recorded; metrics
// is outside identity so 401/403 still count as 4xx. The X-Request-ID
// response header is set BEFORE identity resolution so identity-
// rejection logs carry req_id.
//
// Per-request mutable state (route template + augmented logger) lives
// in a *requestObs allocated by metricsWrap and stored in ctx. Inner
// layers mutate its fields; the outer layer reads them after the
// inner chain returns. This indirection is necessary because Go's
// r.WithContext clones the Request, so outer-layer reads of inner
// mutations on r (such as r.Pattern set by ServeMux) would otherwise
// be lost.
func WithMiddleware(deps WithMiddlewareDeps) func(http.Handler) http.Handler {
	idp := deps.Provider
	baseLogger := deps.Logger
	if baseLogger == nil {
		baseLogger = slog.Default()
	}
	m := deps.Metrics

	identityWrap := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, err := idp.FromRequest(r.Context(), r)
			if err != nil {
				status := http.StatusInternalServerError
				switch {
				case errors.Is(err, errs.ErrIdentityMissing):
					status = http.StatusUnauthorized
				case errors.Is(err, errs.ErrDirectAccessBlocked):
					status = http.StatusForbidden
				}
				http.Error(w, http.StatusText(status), status)
				if o := obsFromContext(r.Context()); o != nil && o.logger != nil {
					o.logger.Warn("request rejected",
						"method", r.Method, "path", r.URL.Path,
						"status", status, "err", err)
				}
				return
			}
			ctx := context.WithValue(r.Context(), ctxKeyIdentity, id)
			// Augment the per-request logger with principal fields by
			// updating the SHARED state, not by attaching a new logger
			// to a forked context (the outer layer wouldn't see it).
			if o := obsFromContext(ctx); o != nil {
				o.logger = o.logger.With(
					"principal_hub", id.Principal.Hub,
					"principal_user_id", id.Principal.UserID,
					"principal", id.Principal.OwnersPrincipal().String(),
				)
				ctx = WithLogger(ctx, o.logger)
			}
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}

	recoveryWrap := WithRecovery(baseLogger.With("component", "httpapi"))

	metricsWrap := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			// req_id BEFORE identity resolution.
			reqID := ""
			if deps.RequestIDHeader != "" {
				reqID = r.Header.Get(deps.RequestIDHeader)
			}
			if reqID == "" {
				reqID = uuid.NewString()
			}
			w.Header().Set("X-Request-ID", reqID)

			obs := &requestObs{
				routeTemplate: "unmatched",
				logger:        baseLogger.With("component", "httpapi", "req_id", reqID),
			}
			ctx := context.WithValue(r.Context(), ctxKeyObs, obs)
			ctx = context.WithValue(ctx, ctxKeyRequestID, reqID)
			ctx = WithLogger(ctx, obs.logger)

			rw := &statusCapture{ResponseWriter: w, code: http.StatusOK}
			next.ServeHTTP(rw, r.WithContext(ctx))

			method := normalizeMethod(r.Method)
			route := obs.routeTemplate // populated by WrapMuxHandler if matched
			class := statusClass(rw.code)

			if m != nil {
				m.HTTPRequests(method, route, class).Inc()
				m.HTTPRequestDuration(method, route).
					Update(time.Since(start).Seconds())
			}

			// Final access log uses the augmented logger (now with
			// principal_* fields if identity resolved).
			obs.logger.Info("request",
				"method", method, "route", route, "status", rw.code,
				"dur_ms", time.Since(start).Milliseconds())
		})
	}
	return func(next http.Handler) http.Handler {
		return metricsWrap(recoveryWrap(identityWrap(next)))
	}
}

// normalizeMethod collapses uncommon HTTP methods to "OTHER" so the
// metric label cardinality stays bounded.
func normalizeMethod(m string) string {
	switch m {
	case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS":
		return m
	default:
		return "OTHER"
	}
}

// normalizeRouteTemplate normalizes a registered ServeMux pattern for
// use as a metric label. Returns "unmatched" when pat is empty (the
// route didn't match any registered handler). Strips method prefix and
// host prefix, then rewrites "{name}" path-segment placeholders to
// ":name" so the metric label is stable across renames.
//
// Called by WrapMuxHandler with r.Pattern from the request that
// ServeMux dispatched to a registered handler. Outer middleware never
// calls this directly because the outer Request has no Pattern set
// (ServeMux mutates Pattern on a freshly-cloned Request before
// invoking the handler — see the architectural note above).
func normalizeRouteTemplate(pat string) string {
	if pat == "" {
		return "unmatched"
	}
	// Strip optional leading "METHOD " prefix.
	if i := strings.IndexByte(pat, ' '); i >= 0 {
		pat = pat[i+1:]
	}
	if pat == "" {
		return "unmatched"
	}
	// Strip optional host prefix like "example.com" — we never use
	// host-scoped patterns, but a defensive split keeps the label
	// stable if one is added later.
	if i := strings.Index(pat, "/"); i > 0 {
		pat = pat[i:]
	}
	// {x} → :x
	var b strings.Builder
	b.Grow(len(pat))
	for i := 0; i < len(pat); {
		if pat[i] == '{' {
			j := strings.IndexByte(pat[i:], '}')
			if j > 0 {
				name := pat[i+1 : i+j]
				// Strip optional "..." suffix used for wildcards.
				name = strings.TrimSuffix(name, "...")
				b.WriteByte(':')
				b.WriteString(name)
				i += j + 1
				continue
			}
		}
		b.WriteByte(pat[i])
		i++
	}
	return b.String()
}

func statusClass(status int) string {
	switch {
	case status >= 200 && status < 300:
		return "2xx"
	case status >= 300 && status < 400:
		return "3xx"
	case status >= 400 && status < 500:
		return "4xx"
	default:
		return "5xx"
	}
}
```

Make sure the imports include `"strings"`, `"time"`, `"github.com/wesm/fotobank/internal/obs"`, and remove the `"sync"` import if no longer needed (display-cache may still need it; check the file).

- [ ] **Step 4: Update `internal/httpapi/api.go` Deps to carry Logger and Metrics**

In `httpapi.Deps`, add:

```go
type Deps struct {
	// ... existing fields ...
	Logger  *slog.Logger
	Metrics *obs.Metrics
}
```

Update `httpapi.New` to forward both into `WithMiddleware`.

- [ ] **Step 5: Run the new middleware tests**

```bash
go test ./internal/httpapi/ -run "TestRequestIDHeaderSetBeforeIdentity|TestPanicRecoveryRecordsAs5xx|TestSuccessfulRequestRecorded" -count=1
```

Expected: 3 tests pass.

- [ ] **Step 6: Run the full httpapi test suite — expect breakage from existing tests that build a server**

```bash
go test ./internal/httpapi/ -count=1
```

Expected: existing api_test, middleware_test cases that called `httpapi.New(Deps{...})` or `WithMiddleware(idp)` may fail to compile until they pass `Logger`/`Metrics`. Update them to inject `obs.NewTestMetrics()` and a discard logger.

Common shape:

```go
deps := httpapi.Deps{
	IdentityProvider: idp,
	OwnerService:     ownerSvc,
	// ... other existing deps ...
	Logger:  slog.New(slog.NewJSONHandler(io.Discard, nil)),
	Metrics: obs.NewTestMetrics(),
}
```

For middleware-only tests that previously called `httpapi.WithMiddleware(idp)`, replace with:

```go
mw := httpapi.WithMiddleware(httpapi.WithMiddlewareDeps{
	Provider: idp,
	Logger:   slog.New(slog.NewJSONHandler(io.Discard, nil)),
	Metrics:  obs.NewTestMetrics(),
})
```

- [ ] **Step 7: Re-run the full httpapi test suite**

```bash
go test ./internal/httpapi/ -count=1
```

Expected: all green.

- [ ] **Step 8: Commit**

```bash
git add internal/httpapi/middleware.go internal/httpapi/api.go internal/httpapi/middleware_test.go
git commit -m "httpapi.WithMiddleware: metrics+recovery+identity, X-Request-ID, dur_ms"
```

---

## Task 10: backup.Worker integration

**Files:**
- Modify: `internal/backup/worker.go`
- Modify: `internal/backup/worker_test.go`

- [ ] **Step 1: Write failing test**

Append to `internal/backup/worker_test.go`:

```go
func TestWorkerEmitsMetricsAndPushesLastSuccess(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src.sqlite")
	makeSourceDB(t, src)
	db, err := sql.Open("sqlite", src+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	r.NoError(err)
	t.Cleanup(func() { _ = db.Close() })

	m := obs.NewTestMetrics()
	w := NewWorker(Config{
		DB:       db,
		Dir:      filepath.Join(tmp, "snaps"),
		Interval: 30 * time.Millisecond,
		Policy:   Policy{Keep15Min: 4, KeepHourly: 24, KeepDaily: 7},
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Metrics:  m,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	require.Eventually(t, func() bool {
		return m.BackupSnapshots("ok").Get() >= 1
	}, 2*time.Second, 10*time.Millisecond)
	cancel()
	r.NoError(<-done)

	r.Greater(m.BackupSnapshots("ok").Get(), uint64(0))
	// last_success_unix gauge must have been pushed.
	var buf bytes.Buffer
	m.WritePrometheus(&buf)
	out := buf.String()
	r.NotContains(out, "fotobank_backup_last_success_unix 0",
		"last_success_unix must have a real timestamp after a successful tick")
	r.NotContains(out, "fotobank_backup_seconds_since_last_success -1",
		"seconds_since_last_success must be derived now that last_success is set")
}

func TestWorkerEmitsFailedSnapshotMetric(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	// Use a missing source DB — Snapshot will fail.
	src := filepath.Join(tmp, "missing.sqlite")
	db, err := sql.Open("sqlite", "file:"+src+"?mode=rw")
	r.NoError(err)
	t.Cleanup(func() { _ = db.Close() })

	m := obs.NewTestMetrics()
	w := NewWorker(Config{
		DB:       db,
		Dir:      filepath.Join(tmp, "snaps"),
		Interval: 30 * time.Millisecond,
		Policy:   Policy{Keep15Min: 4, KeepHourly: 24, KeepDaily: 7},
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Metrics:  m,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	require.Eventually(t, func() bool {
		return m.BackupSnapshots("failed").Get() >= 1
	}, 2*time.Second, 10*time.Millisecond)
	cancel()
	r.NoError(<-done)
}

// TestWorkerLogsCarryComponent is the field-contract test the spec
// pins for each worker: when the caller wires a logger derived via
// .With("component", "backup") (as cli/server.go does), the worker's
// log lines carry that component attribute. We construct the same
// wrapper here so the assertion is robust against future log
// additions in the worker.
func TestWorkerLogsCarryComponent(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src.sqlite")
	makeSourceDB(t, src)
	db, err := sql.Open("sqlite", src+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	r.NoError(err)
	t.Cleanup(func() { _ = db.Close() })

	// syncBuf (existing helper at the top of worker_test.go) wraps
	// bytes.Buffer with a mutex; required because the worker goroutine
	// writes via slog while require.Eventually's polling goroutine
	// reads via String(). Plain bytes.Buffer here triggers -race.
	var logBuf syncBuf
	base := slog.New(slog.NewJSONHandler(&logBuf, nil))
	w := NewWorker(Config{
		DB:       db,
		Dir:      filepath.Join(tmp, "snaps"),
		Interval: 30 * time.Millisecond,
		Policy:   Policy{Keep15Min: 4, KeepHourly: 24, KeepDaily: 7},
		Logger:   base.With("component", "backup"),
		Metrics:  obs.NewTestMetrics(),
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	require.Eventually(t, func() bool {
		return strings.Contains(logBuf.String(), `"msg":"backup snapshot ok"`)
	}, 2*time.Second, 10*time.Millisecond)
	cancel()
	r.NoError(<-done)
	// Per-line scan: every emitted line must carry component=backup.
	// A single Contains check would let a stray non-component line
	// slip through.
	for line := range strings.SplitSeq(strings.TrimSpace(logBuf.String()), "\n") {
		r.Contains(line, `"component":"backup"`,
			"every backup-worker log line must carry component=backup")
	}
}
```

- [ ] **Step 2: Run test, expect compile failure**

```bash
go test ./internal/backup/ -run TestWorkerEmitsMetricsAndPushesLastSuccess -count=1
```

Expected: `Config.Metrics` undefined.

- [ ] **Step 3: Update `internal/backup/worker.go`**

Add `Metrics *obs.Metrics` to `Config` (with import `"github.com/wesm/fotobank/internal/obs"`). In `tick`:

```go
func (w *Worker) tick(ctx context.Context) {
	now := time.Now()
	dst := filepath.Join(w.cfg.Dir, now.UTC().Format(StampLayout)+SnapshotExt)
	start := now
	if err := Snapshot(ctx, w.cfg.DB, dst); err != nil {
		w.cfg.Logger.Error("backup snapshot failed",
			"err", err, "dst", dst,
			"dur_ms", time.Since(start).Milliseconds())
		if w.cfg.Metrics != nil {
			w.cfg.Metrics.BackupSnapshots("failed").Inc()
			w.cfg.Metrics.BackupSnapshotDuration("failed").
				Update(time.Since(start).Seconds())
		}
		return
	}
	w.lastSuccessAt = time.Now()
	if w.cfg.Metrics != nil {
		w.cfg.Metrics.BackupSnapshots("ok").Inc()
		w.cfg.Metrics.BackupSnapshotDuration("ok").
			Update(time.Since(start).Seconds())
		w.cfg.Metrics.SetBackupLastSuccess(w.lastSuccessAt.Unix())
	}
	var size int64
	if info, err := os.Stat(dst); err == nil {
		size = info.Size()
	}

	res, sweepErr := Sweep(w.cfg.Dir, w.cfg.Policy, time.Now(), w.cfg.Logger)
	if sweepErr != nil {
		w.cfg.Logger.Warn("backup retention sweep failed",
			"err", sweepErr, "dir", w.cfg.Dir)
		if w.cfg.Metrics != nil {
			w.cfg.Metrics.BackupRetentionSweeps("failed").Inc()
		}
	} else if w.cfg.Metrics != nil {
		w.cfg.Metrics.BackupRetentionSweeps("ok").Inc()
		w.cfg.Metrics.BackupRetentionDeleted().Add(res.Deleted)
	}

	attrs := []any{
		"path", dst,
		"size_bytes", size,
		"dur_ms", time.Since(start).Milliseconds(),
	}
	if sweepErr == nil {
		attrs = append(attrs,
			"kept_15min", res.Kept15Min,
			"kept_hourly", res.KeptHourly,
			"kept_daily", res.KeptDaily,
			"deleted", res.Deleted)
	}
	w.cfg.Logger.Info("backup snapshot ok", attrs...)
}
```

Note the field rename: `duration_ms` → `dur_ms` in both the success and failure log lines.

`BackupRetentionDeleted().Add(res.Deleted)` requires `res.Deleted` to be the integer count; the metric counter accepts `uint64` — cast or change the helper signature accordingly. Adjust based on the actual `Counter.Add` signature in your VictoriaMetrics version.

- [ ] **Step 4: Run the new tests, expect pass**

```bash
go test ./internal/backup/ -run "TestWorkerEmitsMetricsAndPushesLastSuccess|TestWorkerEmitsFailedSnapshotMetric" -count=1
```

Expected: 2 tests pass.

- [ ] **Step 5: Run the full backup suite**

```bash
go test ./internal/backup/ -count=1
```

Expected: all green.

- [ ] **Step 6: Commit**

```bash
git add internal/backup/worker.go internal/backup/worker_test.go
git commit -m "backup.Worker: emit obs metrics, push last_success, dur_ms field"
```

---

## Task 11: thumb.Worker integration

**Files:**
- Modify: `internal/thumb/worker.go`
- Modify: `internal/thumb/worker_test.go`
- Add to `internal/thumb/queue.go` (or wherever): a `DepthByState(state string) int64` method on Queue if not already there (used as a `MetricSources.ThumbQueueDepth` source in Task 14).

- [ ] **Step 1: Inspect thumb worker for the right places to instrument**

```bash
grep -n "Mark\|claim\|SweepLeases" internal/thumb/worker.go
```

The worker has terminal Mark calls (`MarkReady`, `MarkFailed`, `MarkNoPreview`) and a periodic `SweepLeases` call. Each terminal mark is a job result; each sweep returns a count.

- [ ] **Step 2: Write failing test**

Append to `internal/thumb/worker_test.go`. Reuse the existing `newWorkerFixture`, `seedPhotoRow`, `seedVideoRow`, and `waitForStatus` helpers (see `internal/thumb/worker_test.go:37-157`). The test exercises ok + no_preview + a forced stale-lease sweep:

```go
func TestWorkerEmitsResultMetricsAndLeaseSweep(t *testing.T) {
	r := require.New(t)
	fx := newWorkerFixture(t)

	photoID := seedPhotoRow(t, fx, "2024/a-"+uuid.NewString()+".jpg")
	videoID := seedVideoRow(t, fx)

	// Insert a third row pre-claimed with a stale lease so SweepLeases
	// has a row to bump back to pending and increment the swept counter.
	staleID := uuid.NewString()
	staleM := media.Media{
		ID: staleID, Owner: fx.owner, Type: media.TypePhoto,
		MimeType: "image/jpeg", Path: "2024/stale-" + staleID + ".jpg",
		OriginalFilename: "stale.jpg",
		ImportedAt:       time.Now().UTC().Truncate(time.Second),
		Size:             1, Checksum: uuid.NewString(),
		ThumbStatus:      "working",
	}
	r.NoError(fx.repo.Insert(context.Background(), staleM))
	_, err := fx.rw.ExecContext(context.Background(),
		`UPDATE media SET thumb_lease_at = ?, thumb_lease_owner = ?
		 WHERE id = ?`,
		time.Now().Add(-time.Hour).UTC(), "expired-worker", staleID)
	r.NoError(err)

	m := obs.NewTestMetrics()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := thumb.NewWorker(fx.queue, fx.store, thumb.Config{
		WorkerConcurrency: 1,
		PollInterval:      20 * time.Millisecond,
		LeaseTimeout:      time.Minute, // anything > 1h-old stale lease
		Metrics:           m,
	})
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	waitForStatus(t, fx.rw, photoID, "ready")
	waitForStatus(t, fx.rw, videoID, "no_preview")

	require.Eventually(t, func() bool {
		return m.ThumbLeasesSwept().Get() >= 1
	}, 3*time.Second, 20*time.Millisecond,
		"stale-lease sweep counter must increment within the worker's tick")

	cancel()
	<-done

	r.GreaterOrEqual(m.ThumbJobs("ok").Get(), uint64(1))
	r.GreaterOrEqual(m.ThumbJobs("no_preview").Get(), uint64(1))
	r.GreaterOrEqual(m.ThumbLeasesSwept().Get(), uint64(1))
}
```

Add `"github.com/wesm/fotobank/internal/obs"` to the imports if not already present. The test verifies the three accessor surfaces (ok, no_preview, leases_swept) — `failed` is exercised separately by the existing failure-path tests (`TestWorkerStaleWriteDoesNotCorruptReclaim`, etc.), which only need the same `Metrics` plumbing to assert against.

Also append the field-contract test the spec pins (mirrors `TestWorkerLogsCarryComponent` in backup):

```go
func TestThumbWorkerLogsCarryComponent(t *testing.T) {
	r := require.New(t)
	fx := newWorkerFixture(t)
	id := seedPhotoRow(t, fx, "2024/c-"+uuid.NewString()+".jpg")

	var logBuf bytes.Buffer
	base := slog.New(slog.NewJSONHandler(&logBuf, nil))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := thumb.NewWorker(fx.queue, fx.store, thumb.Config{
		WorkerConcurrency: 1,
		PollInterval:      20 * time.Millisecond,
		LeaseTimeout:      time.Minute,
		Logger:            base.With("component", "thumb"),
		Metrics:           obs.NewTestMetrics(),
	})
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	waitForStatus(t, fx.rw, id, "ready")
	cancel()
	<-done
	// Per-line scan: every emitted line must carry component=thumb.
	for line := range strings.SplitSeq(strings.TrimSpace(logBuf.String()), "\n") {
		r.Contains(line, `"component":"thumb"`,
			"every thumb-worker log line must carry component=thumb")
	}
}
```

- [ ] **Step 3: Update `internal/thumb/worker.go`**

Add `Metrics *obs.Metrics` to `thumb.Config`. In each terminal-mark code path, increment the matching counter and observe the duration histogram. After each SweepLeases call, increment by the returned count.

```go
// At the top of processOne or wherever the per-job timer starts:
start := time.Now()

// On success:
if w.cfg.Metrics != nil {
    w.cfg.Metrics.ThumbJobs("ok").Inc()
    w.cfg.Metrics.ThumbJobDuration("ok").Update(time.Since(start).Seconds())
}

// On no-preview:
if w.cfg.Metrics != nil {
    w.cfg.Metrics.ThumbJobs("no_preview").Inc()
    w.cfg.Metrics.ThumbJobDuration("no_preview").Update(time.Since(start).Seconds())
}

// On failed:
if w.cfg.Metrics != nil {
    w.cfg.Metrics.ThumbJobs("failed").Inc()
    w.cfg.Metrics.ThumbJobDuration("failed").Update(time.Since(start).Seconds())
}

// After SweepLeases (queue.SweepLeases returns int, and Counter.Add takes int):
n, err := w.queue.SweepLeases(ctx, w.cfg.LeaseTimeout)
if err == nil && n > 0 && w.cfg.Metrics != nil {
    w.cfg.Metrics.ThumbLeasesSwept().Add(n)
}
```

The exact location of these calls is determined by the existing structure of `worker.go`; place them at the same site that already calls `Mark*` so the metric and the state transition are atomic at the code level.

- [ ] **Step 4: Run thumb tests**

```bash
go test ./internal/thumb/ -count=1
```

Expected: all green, including the new metric test.

- [ ] **Step 5: Commit**

```bash
git add internal/thumb/worker.go internal/thumb/worker_test.go
git commit -m "thumb.Worker: emit obs metrics on terminal marks and sweep"
```

---

## Task 12: shareworker.Worker integration

**Files:**
- Modify: `internal/shareworker/worker.go`
- Modify: `internal/shareworker/worker_test.go`

- [ ] **Step 1: Inspect the share worker's success/retry/terminal paths**

```bash
grep -n "MarkPublished\|MarkAttemptFailed\|MarkFailed\|recordFailure" internal/shareworker/worker.go
```

The success path calls `MarkPublished` (or `MarkRevoked`); retry path calls `MarkAttemptFailed`; terminal path calls `MarkFailed`.

- [ ] **Step 2: Write failing test**

Append to `internal/shareworker/worker_test.go`. Reuse `newWorkerFixture` (lines 30-48 of the existing test) but extend it to thread `obs.NewTestMetrics()` through `shareworker.Config.Metrics`. The test drives one scope through ok, one through retry (transient), and one through terminal_fail (permanent), each in its own fixture so the brokertest.Fake's scripted-error queue is independent:

```go
func TestWorkerEmitsPublishResultMetrics(t *testing.T) {
	r := require.New(t)

	// ok: clean publish, no scripted error.
	fxOK := newWorkerFixture(t)
	mOK := obs.NewTestMetrics()
	fxOK.w = shareworker.New(shareworker.Config{
		Repo:    fxOK.repo,
		Broker:  fxOK.fake,
		Now:     func() time.Time { return fxOK.now },
		Rand:    rand.New(rand.NewSource(1)),
		Metrics: mOK,
	})
	_ = fxOK.insertPending(t)
	_, err := fxOK.w.RunOnce(context.Background())
	r.NoError(err)
	r.EqualValues(1, mOK.SharePublishes("ok").Get())

	// retry: transient error scripted.
	fxRetry := newWorkerFixture(t)
	mRetry := obs.NewTestMetrics()
	fxRetry.w = shareworker.New(shareworker.Config{
		Repo:    fxRetry.repo,
		Broker:  fxRetry.fake,
		Now:     func() time.Time { return fxRetry.now },
		Rand:    rand.New(rand.NewSource(1)),
		Metrics: mRetry,
	})
	idR := fxRetry.insertPending(t)
	fxRetry.fake.QueuePublishError(idR, broker.ErrBrokerTransient)
	_, err = fxRetry.w.RunOnce(context.Background())
	r.NoError(err)
	r.EqualValues(1, mRetry.SharePublishes("retry").Get())
	r.EqualValues(0, mRetry.SharePublishes("terminal_fail").Get(),
		"transient under MaxBrokerAttempts must classify as retry, not terminal_fail")

	// terminal_fail (permanent): scripted permanent error.
	fxPerm := newWorkerFixture(t)
	mPerm := obs.NewTestMetrics()
	fxPerm.w = shareworker.New(shareworker.Config{
		Repo:    fxPerm.repo,
		Broker:  fxPerm.fake,
		Now:     func() time.Time { return fxPerm.now },
		Rand:    rand.New(rand.NewSource(1)),
		Metrics: mPerm,
	})
	idP := fxPerm.insertPending(t)
	fxPerm.fake.QueuePublishError(idP, broker.ErrBrokerPermanent)
	_, err = fxPerm.w.RunOnce(context.Background())
	r.NoError(err)
	r.EqualValues(1, mPerm.SharePublishes("terminal_fail").Get())

	// terminal_fail (max-attempts): transient at the boundary.
	fxMax := newWorkerFixture(t)
	mMax := obs.NewTestMetrics()
	fxMax.w = shareworker.New(shareworker.Config{
		Repo:    fxMax.repo,
		Broker:  fxMax.fake,
		Now:     func() time.Time { return fxMax.now },
		Rand:    rand.New(rand.NewSource(1)),
		Metrics: mMax,
	})
	idM := fxMax.insertPending(t)
	_, err = fxMax.db.ExecContext(context.Background(),
		`UPDATE scopes SET broker_attempts = ?, broker_next_attempt_at = NULL WHERE uuid = ?`,
		share.MaxBrokerAttempts-1, idM)
	r.NoError(err)
	fxMax.fake.QueuePublishError(idM, broker.ErrBrokerTransient)
	_, err = fxMax.w.RunOnce(context.Background())
	r.NoError(err)
	r.EqualValues(1, mMax.SharePublishes("terminal_fail").Get(),
		"transient at MaxBrokerAttempts must classify as terminal_fail")
}
```

Add imports for `"github.com/wesm/fotobank/internal/broker"`, `"github.com/wesm/fotobank/internal/obs"`, `"github.com/wesm/fotobank/internal/share"`, `"math/rand"` if not already present.

Also append the field-contract test the spec pins:

```go
func TestShareWorkerLogsCarryComponent(t *testing.T) {
	r := require.New(t)
	fx := newWorkerFixture(t)

	var logBuf bytes.Buffer
	base := slog.New(slog.NewJSONHandler(&logBuf, nil))
	fx.w = shareworker.New(shareworker.Config{
		Repo:    fx.repo,
		Broker:  fx.fake,
		Now:     func() time.Time { return fx.now },
		Rand:    rand.New(rand.NewSource(1)),
		Logger:  base.With("component", "share"),
		Metrics: obs.NewTestMetrics(),
	})
	_ = fx.insertPending(t)
	_, err := fx.w.RunOnce(context.Background())
	r.NoError(err)
	// Per-line scan: every emitted line must carry component=share.
	for line := range strings.SplitSeq(strings.TrimSpace(logBuf.String()), "\n") {
		r.Contains(line, `"component":"share"`,
			"every share-worker log line must carry component=share")
	}
}
```

`shareworker.Config.Logger` already exists (see `internal/shareworker/worker.go:29-30`). The component-contract assertion presumes the worker emits at least one log per `RunOnce`; the worker is currently silent on the happy path, so add a `Logger.Info("scope publish ok", "uuid", s.UUID)` call at the publish-success site (and a matching `"scope revoke ok"` at the revoke-success site) so the field contract has something to attach to. These two info logs replace the implicit "this happened" knowledge that previously lived only in metrics counters.

- [ ] **Step 3: Update `internal/shareworker/worker.go`**

Add `Metrics *obs.Metrics` to `shareworker.Config`. Wrap each terminal/retry/ok path with a metric increment and a duration observation:

```go
// In each Process* method:
start := time.Now()
err := /* PublishScope or RevokeScope */
if w.cfg.Metrics == nil {
    return /* unchanged */
}

// Then after recordFailure / MarkPublished:
switch {
case err == nil:
    w.cfg.Metrics.SharePublishes("ok").Inc()
    w.cfg.Metrics.SharePublishDuration("ok").Update(time.Since(start).Seconds())
case errors.Is(err, broker.ErrBrokerPermanent) || s.BrokerAttempts+1 >= share.MaxBrokerAttempts:
    w.cfg.Metrics.SharePublishes("terminal_fail").Inc()
    w.cfg.Metrics.SharePublishDuration("terminal_fail").Update(time.Since(start).Seconds())
default:
    w.cfg.Metrics.SharePublishes("retry").Inc()
    w.cfg.Metrics.SharePublishDuration("retry").Update(time.Since(start).Seconds())
}
```

Mirror the same logic in `processRevoke` against `ShareRevokes` / `ShareRevokeDuration`.

The classification logic must mirror what `recordFailure` does (so `terminal_fail` matches when `MarkFailed` would be called) — extract a shared helper if cleaner.

- [ ] **Step 4: Run share worker tests**

```bash
go test ./internal/shareworker/ -count=1
```

Expected: all green.

- [ ] **Step 5: Commit**

```bash
git add internal/shareworker/worker.go internal/shareworker/worker_test.go
git commit -m "shareworker.Worker: emit obs metrics on publish/revoke results"
```

---

## Task 13: cli/server.go wiring

**Files:**
- Modify: `internal/cli/server.go`
- Modify: `internal/cli/server_test.go` (existing tests will need to construct logger/metrics)

This is the big integration task. server.go now:
- Builds logger + metrics early
- Calls slog.SetDefault with `component=legacy`
- Builds MetricSources from repo methods
- Threads metrics into every worker Config
- Builds and runs the admin listener on its own goroutine outside bgWG
- Handles shutdown sequence: ready false → main API → bgWG.Wait → admin → adminDone

- [ ] **Step 1: Update `runServer` logger construction**

Replace:

```go
logger := slog.New(slog.NewTextHandler(opts.stderr, nil))
```

with:

```go
logger := obs.NewLogger(obs.LoggerConfig{
	Format:    cfg.Observability.Logging.Format,
	Level:     cfg.Observability.Logging.Level,
	AddSource: cfg.Observability.Logging.AddSource,
}, opts.stderr)
slog.SetDefault(logger.With("component", "legacy"))
```

Place this near the top of `runServer`, after `cfg, err := config.Load(...)` and before any worker construction or DB open.

- [ ] **Step 2: Build metrics**

After the `db.Open` call (so the queues/repos exist):

```go
metricsObj := obs.NewMetrics(obs.MetricSources{
	ThumbQueueDepth:  thumbQueue.DepthByState,
	SharePendingByOp: sharesRepo.CountPendingByOp,
}, obs.BuildInfo{
	Version:   version.Version,
	Commit:    version.Commit,
	BuildDate: version.BuildDate,
})
```

If `thumbQueue.DepthByState` or `sharesRepo.CountPendingByOp` don't exist, add minimal implementations: each returns `int64` for a state/op string by running a `SELECT count(*) FROM ...` against the read DB. Skipping is acceptable for v1 if either source is wired with a `nil` closure (the gauge then reports 0).

- [ ] **Step 3: Thread metrics + logger into existing worker Configs**

In each construction:

```go
thumbWorker := thumb.NewWorker(thumbQueue, storeLayer, thumb.Config{
	WorkerConcurrency: cfg.Thumbs.WorkerConcurrency,
	PollInterval:      cfg.Thumbs.PollInterval,
	LeaseTimeout:      cfg.Thumbs.LeaseTimeout,
	Logger:            logger.With("component", "thumb"),
	Metrics:           metricsObj,
})

shareCfg := shareworker.Config{
	Repo:    sharesRepo,
	Broker:  brokerClient,
	Logger:  logger.With("component", "share"),
	Metrics: metricsObj,
}

bw := backup.NewWorker(backup.Config{
	DB:       d.WriteDB(),
	Dir:      backupDir,
	Interval: interval,
	Policy:   backup.Policy{...},
	Logger:   logger.With("component", "backup"),
	Metrics:  metricsObj,
})
```

Update `httpapi.New(httpapi.Deps{...})` to pass `Logger: logger`, `Metrics: metricsObj`.

- [ ] **Step 4: Build the admin listener**

After all workers are constructed but before `srv.Serve(ln)`:

```go
ready := obs.NewReady()

var adminSrv *http.Server
adminDone := make(chan error, 1)
if cfg.Observability.AdminEnabled {
	adminMux := obs.NewAdminMux(obs.AdminConfig{
		Metrics: metricsObj,
		Ready:   ready,
		Checks: []obs.ReadyCheck{
			{
				Name: "db_ping",
				Fn: func(ctx context.Context) error {
					return d.WriteDB().PingContext(ctx)
				},
			},
			// snapshot_dir check, gated on cfg.Backup.Enabled:
			// when backups are off, the dir need not exist or be writable.
			obsBackupCheck(cfg, backupDir),
			{
				Name: "nas_root",
				Fn: func(_ context.Context) error {
					_, err := os.Stat(cfg.NAS.Root)
					return err
				},
			},
		},
		ReadyzCfg: obs.ReadyzConfig{
			DeadlineTotal: 2 * time.Second,
			CacheTTL:      5 * time.Second,
		},
		PprofEnabled: cfg.Observability.PprofEnabled,
	})
	adminLn, err := bindListener(cfg.Observability.AdminListen)
	if err != nil {
		return fmt.Errorf("bind admin listener: %w", err)
	}
	adminSrv = &http.Server{Handler: adminMux}
	go func() {
		if err := adminSrv.Serve(adminLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
			adminDone <- err
			return
		}
		adminDone <- nil
	}()
} else {
	close(adminDone) // unblock final wait
}
```

Add a small helper:

```go
func obsBackupCheck(cfg *config.Config, dir string) obs.ReadyCheck {
	if !cfg.Backup.Enabled {
		return obs.ReadyCheck{
			Name: "snapshot_dir",
			Fn:   func(context.Context) error { return nil }, // no-op
		}
	}
	return obs.ReadyCheck{
		Name: "snapshot_dir",
		Fn: func(_ context.Context) error {
			probe := filepath.Join(dir, ".readyz-probe")
			f, err := os.OpenFile(probe, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if err != nil {
				return err
			}
			_ = f.Close()
			return os.Remove(probe)
		},
	}
}
```

- [ ] **Step 5: Update shutdown sequence**

Replace the existing shutdown block with:

```go
case <-sigCtx.Done():
	// 1. Flip readiness false so /readyz returns 503.
	ready.Store(false)
	// 2. Shut down main API listener.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		_ = srv.Close()
		<-serveErr
		bgWG.Wait()
		if adminSrv != nil {
			_ = adminSrv.Shutdown(context.Background())
			<-adminDone
		}
		return err
	}
	<-serveErr
	// 3. Drain workers.
	bgWG.Wait()
	// 4. Shut down admin listener LAST.
	if adminSrv != nil {
		_ = adminSrv.Shutdown(context.Background())
		<-adminDone
	}
	return nil
```

Mirror the same admin-shutdown tail in the `serveErr` (Serve-failed) branch above.

- [ ] **Step 6: Run cli tests**

```bash
go test ./internal/cli/ -count=1
```

Expected: all green. Existing server_test.go likely needs no changes since the admin listener is opt-in by config and the test config has no `[observability]` block (so defaults apply, admin is on, but the test never scrapes it). If port conflicts arise (multiple tests racing for `127.0.0.1:9090`), fix by setting `[observability]\nadmin_listen = "127.0.0.1:0"` in the test configs to bind a free port.

- [ ] **Step 7: Run full test suite**

```bash
go test ./... -count=1 -timeout=300s
```

Expected: all green.

- [ ] **Step 8: Commit**

```bash
git add internal/cli/server.go internal/cli/server_test.go
git commit -m "Wire obs.Logger, obs.Metrics, obs.AdminListener into runServer"
```

---

## Task 14: e2e tests

**Files:**
- Create: `internal/cli/e2e_observability_test.go`

- [ ] **Step 1: Write the e2e tests**

```go
package cli_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/cli"
)

// writeObsConfig produces a TOML config with a free-port admin listener
// so concurrent e2e tests don't fight for 127.0.0.1:9090.
func writeObsConfig(t *testing.T, tmp string) (cfgPath, adminAddrSink string) {
	t.Helper()
	r := require.New(t)
	nasRoot := filepath.Join(tmp, "nas")
	flashRoot := filepath.Join(tmp, "flash")
	r.NoError(os.MkdirAll(nasRoot, 0o700))
	r.NoError(os.MkdirAll(flashRoot, 0o700))
	cfgPath = filepath.Join(tmp, "fotobank.toml")
	r.NoError(os.WriteFile(cfgPath, []byte(`
[nas]
root = "`+nasRoot+`"
[flash]
root = "`+flashRoot+`"
[identity]
mode = "stub"
[identity.stub]
hub = "local"
user_id = "alice"
handle = "Alice"
storage_key = "alice-sk"
[http]
listen_address = "127.0.0.1:0"
[imports]
file_lock_path = "`+filepath.Join(tmp, "import.lock")+`"
[observability]
admin_listen = "127.0.0.1:0"
`), 0o600))
	adminAddrSink = filepath.Join(tmp, "admin-addr")
	t.Setenv("FOTOBANK_TEST_ADMIN_ADDR_SINK", adminAddrSink)
	return cfgPath, adminAddrSink
}

func TestE2EObservabilityAdminScrape(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e tests skipped under -short")
	}
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath, adminSink := writeObsConfig(t, tmp)
	mainSink := filepath.Join(tmp, "main-addr")
	t.Setenv("FOTOBANK_TEST_LISTEN_ADDR_SINK", mainSink)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan int, 1)
	go func() {
		var so, se bytes.Buffer
		done <- cli.RunContext(ctx, []string{"server", "--config", cfgPath}, &so, &se)
	}()

	mainAddr := waitForSink(t, mainSink)
	r.NotEmpty(mainAddr)
	adminAddr := waitForSink(t, adminSink)
	r.NotEmpty(adminAddr, "admin listener must publish its bound address")

	// Issue a few requests through the main listener.
	for i := 0; i < 3; i++ {
		resp, err := http.Get("http://" + mainAddr + "/api/v1/healthz")
		r.NoError(err)
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}

	// Scrape /metrics on admin and assert series presence.
	resp, err := http.Get("http://" + adminAddr + "/metrics")
	r.NoError(err)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	r.Equal(200, resp.StatusCode)
	out := string(body)
	r.Contains(out, "fotobank_build_info")
	r.Contains(out, "go_goroutines")
	r.Contains(out, `fotobank_http_requests_total`)
	r.Contains(out, `route="/api/v1/healthz"`)
	r.Contains(out, `status_class="2xx"`)

	// /readyz should return 200.
	resp, err = http.Get("http://" + adminAddr + "/readyz")
	r.NoError(err)
	resp.Body.Close()
	r.Equal(200, resp.StatusCode)

	cancel()
	<-done
}

func TestE2EObservabilityShutdownReadyz(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e tests skipped under -short")
	}
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath, adminSink := writeObsConfig(t, tmp)
	mainSink := filepath.Join(tmp, "main-addr")
	t.Setenv("FOTOBANK_TEST_LISTEN_ADDR_SINK", mainSink)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan int, 1)
	go func() {
		var so, se bytes.Buffer
		done <- cli.RunContext(ctx, []string{"server", "--config", cfgPath}, &so, &se)
	}()
	r.NotEmpty(waitForSink(t, mainSink))
	adminAddr := waitForSink(t, adminSink)
	r.NotEmpty(adminAddr)

	// Initial /readyz is 200.
	resp, err := http.Get("http://" + adminAddr + "/readyz")
	r.NoError(err)
	resp.Body.Close()
	r.Equal(200, resp.StatusCode)

	// Cancel and observe: /readyz must either return 503, or the
	// admin listener must close. Both prove the readiness flip
	// happened. We must NOT see a sustained 200.
	cancel()
	deadline := time.Now().Add(2 * time.Second)
	saw503OrClosed := false
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://" + adminAddr + "/readyz")
		if err != nil {
			saw503OrClosed = true
			break
		}
		resp.Body.Close()
		if resp.StatusCode == 503 {
			saw503OrClosed = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	r.True(saw503OrClosed, "expected /readyz to flip to 503 or refuse before listener closed")
	<-done
}

func TestE2EObservabilityRejectsNonLoopbackAdmin(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e tests skipped under -short")
	}
	r := require.New(t)
	tmp := t.TempDir()
	nasRoot := filepath.Join(tmp, "nas")
	flashRoot := filepath.Join(tmp, "flash")
	r.NoError(os.MkdirAll(nasRoot, 0o700))
	r.NoError(os.MkdirAll(flashRoot, 0o700))
	cfgPath := filepath.Join(tmp, "fotobank.toml")
	r.NoError(os.WriteFile(cfgPath, []byte(`
[nas]
root = "`+nasRoot+`"
[flash]
root = "`+flashRoot+`"
[identity]
mode = "stub"
[identity.stub]
hub = "local"
user_id = "alice"
handle = "Alice"
storage_key = "alice-sk"
[http]
listen_address = "127.0.0.1:0"
[imports]
file_lock_path = "`+filepath.Join(tmp, "import.lock")+`"
[observability]
admin_listen = "0.0.0.0:9090"
`), 0o600))
	var so, se bytes.Buffer
	code := cli.RunContext(context.Background(),
		[]string{"server", "--config", cfgPath}, &so, &se)
	r.NotEqual(0, code, "non-loopback admin_listen must be rejected at validation")
	r.Contains(strings.ToLower(se.String()+so.String()), "loopback")
}
```

The test config sets `admin_listen = "127.0.0.1:0"` to get a free port. To make the bound address discoverable, `runServer` needs to write the admin-listener's bound address to a file when `FOTOBANK_TEST_ADMIN_ADDR_SINK` is set — mirror the existing main-listener behavior (`FOTOBANK_TEST_LISTEN_ADDR_SINK`).

- [ ] **Step 2: Add the admin-addr sink to `runServer`**

In `internal/cli/server.go`, after `adminLn, err := bindListener(...)` succeeds:

```go
if sink := os.Getenv("FOTOBANK_TEST_ADMIN_ADDR_SINK"); sink != "" {
	if werr := os.WriteFile(sink, []byte(adminLn.Addr().String()), 0o600); werr != nil {
		fmt.Fprintln(opts.stderr, "admin sink write failed:", werr)
	}
}
```

- [ ] **Step 3: Run the e2e tests**

```bash
go test ./internal/cli/ -run TestE2EObservability -v -count=1 -timeout=60s
```

Expected: all 3 pass.

- [ ] **Step 4: Run full e2e suite**

```bash
go test ./internal/cli/ -count=1 -timeout=120s
```

Expected: all green.

- [ ] **Step 5: Commit**

```bash
git add internal/cli/e2e_observability_test.go internal/cli/server.go
git commit -m "Add Layer C e2e tests for admin listener + shutdown readyz + loopback validation"
```

---

## Task 15: Final verification + lint sweep

- [ ] **Step 1: Full lint**

```bash
make lint
```

Expected: 0 issues.

- [ ] **Step 2: Full test suite**

```bash
go test ./... -count=1 -timeout=300s
```

Expected: all green.

- [ ] **Step 3: Race detector pass on the obs package**

```bash
go test -race ./internal/obs/... ./internal/httpapi/... -count=1
```

Expected: clean — `obs.Ready` and the readyz singleflight are the most race-prone surfaces.

- [ ] **Step 4: Manual smoke test**

```bash
make build
mkdir -p /tmp/fb-obs-smoke-nas /tmp/fb-obs-smoke-flash
cat > /tmp/fb-obs-smoke.toml <<'EOF'
[nas]
root = "/tmp/fb-obs-smoke-nas"
[flash]
root = "/tmp/fb-obs-smoke-flash"
[identity]
mode = "stub"
[identity.stub]
hub = "local"
user_id = "alice"
handle = "Alice"
storage_key = "alice-sk"
[http]
listen_address = "127.0.0.1:0"
[imports]
file_lock_path = "/tmp/fb-obs-smoke.lock"
[observability]
admin_listen = "127.0.0.1:9090"
EOF

FOTOBANK_CONFIG=/tmp/fb-obs-smoke.toml bin/fotobank server &
SERVER_PID=$!
sleep 1
curl -s http://127.0.0.1:9090/metrics | head -30
curl -s http://127.0.0.1:9090/readyz
echo
kill $SERVER_PID
```

Expected: `/metrics` prints Prometheus exposition; `/readyz` prints `{"status":"ok",...}`; the server shuts down cleanly.

- [ ] **Step 5: Update CLAUDE.md plans roster**

Edit `CLAUDE.md` to mark observability as done in the Plans section (alongside Plan A/B/C/Backup).

```bash
# After editing CLAUDE.md
git add CLAUDE.md
git commit -m "Mark observability sub-spec as done in CLAUDE.md plans roster"
```

- [ ] **Step 6: Final code review**

Dispatch the superpowers:code-reviewer agent to review the entire observability implementation against the spec at `docs/superpowers/specs/2026-04-25-fotobank-observability-design.md`. Address any HIGH or MEDIUM findings before declaring the work done.

---

*End of observability implementation plan.*
