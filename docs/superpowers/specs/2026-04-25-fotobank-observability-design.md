# Fotobank Observability — Design Spec

**Status:** Draft (v0.1)
**Date:** 2026-04-25
**Scope:** Phase-2 observability sub-spec. Establishes structured logging,
Prometheus-compatible metrics, deep readiness checks, and an admin HTTP
listener. Tracing is deferred (field names reserved, no instrumentation).

---

## 1. Goals

Bring fotobank from "stderr text logs and a single liveness endpoint" to a
state where a homelab operator running Prometheus + Grafana on the same box
can:

1. See request rate, latency, and error rate per route from a `/metrics`
   scrape.
2. See worker health (snapshot duration, queue depths, retry rates) without
   grepping logs.
3. Get a deep readiness signal that distinguishes "process is up" from
   "DB is reachable, snapshot dir is writable, NAS root is mounted."
4. Read JSON logs into Loki / `jq` and correlate request lines by
   `req_id`, principal, route, and duration.
5. Continue running without any of the above wired (defaults are sane,
   admin listener can be turned off, no metric exposure means no scrape).

The audience is a single operator on one host. Multi-instance and external
trace collectors are out of scope.

## 2. Non-goals

- OpenTelemetry tracing (spans, exporters, propagation). Field names
  `trace_id` and `span_id` are reserved so a future iteration is one
  config flip away, but no instrumentation is added in v1.
- Per-owner metric labels. The cardinality cost is unbounded as the user
  base grows; logs cover the per-owner audit need.
- Per-route SQL timing, flash cache hit/miss, NAS byte volumes. Add when
  there's a specific bottleneck to debug; don't pay speculatively.
- Log sampling. Volume at homelab traffic shape is negligible.
- PII redaction. Logs are operator-only by threat model; storage keys
  and route templates are non-PII.
- Authenticated `/metrics`. Loopback / unix-socket binding is the
  isolation primitive.
- Push-based metrics (statsd, OTLP). Pull-based Prometheus matches the
  homelab dashboard setup.
- `req_id` propagation into worker job tables. Scope UUID, media ID, and
  snapshot path are the durable per-job correlation keys; no schema
  columns added.

## 3. Architecture

A new package `internal/obs` owns three concerns:

1. **`obs.NewLogger(cfg LoggerConfig, stderr io.Writer) *slog.Logger`** —
   constructs the canonical `*slog.Logger`. Format auto-selected based on
   stderr being a TTY; level read from config with `FOTOBANK_LOG_LEVEL`
   env override.
2. **`obs.Metrics`** — thin wrapper around `github.com/VictoriaMetrics/metrics`
   that owns a private `*metrics.Set`. Workers and HTTP handlers receive
   it through their existing `Config` structs (alongside `*slog.Logger`).
   The default global registry of the upstream library is never used.
3. **`obs.AdminListener(cfg, m, ready) *http.Server`** — second
   `http.Server` mounting `/metrics`, `/readyz`, optional `/debug/pprof/*`.
   No identity middleware on this listener.

`internal/cli/server.go` constructs the logger and metrics registry early,
calls `slog.SetDefault(logger)` as a transition guard, threads both into
every `Config` that already takes a logger, and binds the admin listener
as a separate `http.Server` alongside the main one. The admin listener
is **not** part of `bgWG`; it has its own shutdown handled after
`bgWG.Wait()` (see §8 for the full sequence).

### Component diagram

```
                        FOTOBANK SERVER (single binary)
                        ─────────────────────────────────

cli/server.go:
  obs.NewLogger ─────────► *slog.Logger (the only logger constructed)
       │
       └── slog.SetDefault(...)         (transition guard for legacy callsites)
       │
       └── obs.NewMetrics ───────────► obs.Metrics (private metrics.Set)
                                          │
                                          ├── injected into thumb.Config
                                          ├── injected into shareworker.Config
                                          ├── injected into backup.Config
                                          └── injected into httpapi.Deps

  obs.AdminListener ─────► http.Server on cfg.Observability.AdminListen
                              ├── GET /metrics       (m.WritePrometheus)
                              ├── GET /readyz        (deep checks)
                              └── /debug/pprof/*     (when pprof_enabled)

  Main http.Server on cfg.HTTP.ListenAddress:
                              ├── /api/v1/*          (huma + custom handlers)
                              └── (existing /api/v1/healthz unchanged)
```

### Middleware order on the main listener

```
metrics → recovery → identity → display-cache → handler
```

Rationale:
- **Metrics outermost** — counts identity rejections (401/403) as 4xx, and
  panics (recovered to 500) as 5xx. Both are real signals.
- **Recovery inside metrics** — converts panic to 500 *before* metrics
  records the response. A panic outside metrics would leak as
  status_class=2xx (default) or skip recording entirely.
- **Identity → display-cache → handler** — unchanged from current order.

### Shutdown order

On `SIGINT`/`SIGTERM` or `runServer` exit:

1. Flip `obs.Ready` to false. Next `/readyz` returns 503 immediately,
   bypassing the cache.
2. Shut down the main API listener; drain workers via `bgWG.Wait()`.
3. Shut down the admin listener last.

This sequence lets a Prometheus scrape during graceful shutdown observe
the readiness transition rather than connection-refused — operators get
a clean signal that the process is intentionally going down.

## 4. Configuration

### TOML schema

```toml
[observability]
admin_enabled = true                    # disable to skip admin listener
admin_listen = "127.0.0.1:9090"         # required when admin_enabled
pprof_enabled = false

[observability.logging]
format = "auto"                         # auto | json | text
level = "info"                          # debug | info | warn | error
add_source = false
```

### Defaults (applied in `applyDefaults`)

| Field | Default |
|---|---|
| `admin_enabled` | `true` |
| `admin_listen` | `"127.0.0.1:9090"` |
| `pprof_enabled` | `false` |
| `logging.format` | `"auto"` |
| `logging.level` | `"info"` |
| `logging.add_source` | `false` |

A config file with no `[observability]` block boots cleanly with the
defaults.

### Validation rules (`config.Validate`)

- `admin_enabled = true` requires `admin_listen` to be **loopback-only**:
  - IPv4 host must be `127.0.0.1`
  - IPv6 host must be `::1`
  - `unix:<path>` is accepted
  - Anything else (`0.0.0.0`, `192.168.x.x`, public IPs, hostnames) is
    rejected with `errs.ErrBadConfiguration`. Defense in depth: the
    metrics endpoint is unauthenticated, so the bind must be unreachable
    from anywhere but the local host.
- `logging.format` must be one of `auto | json | text`.
- `logging.level` must be one of `debug | info | warn | error`.
- `pprof_enabled = true` does not require additional checks; the
  loopback rule already covers exposure risk.

### Env overrides

- `FOTOBANK_LOG_LEVEL=debug` overrides `logging.level` without a config
  edit. Honored inside `obs.NewLogger`. Invalid values fall back to
  config-declared level with a warn log at startup.

## 5. Logger (`obs.NewLogger`)

### Signature and TTY detection

```go
type LoggerConfig struct {
    Format    string // "auto" | "json" | "text"
    Level     string // "debug" | "info" | "warn" | "error"
    AddSource bool
}

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

// Test seam — overridden by tests to force terminal/non-terminal.
var isTerminal = func(fd uintptr) bool {
    return term.IsTerminal(int(fd))
}

func isTTYWriter(w io.Writer) bool {
    f, ok := w.(interface{ Fd() uintptr })
    return ok && isTerminal(f.Fd())
}
```

A `bytes.Buffer` lacks `Fd()`, so the type assertion fails and `auto`
deterministically resolves to JSON in tests.

### Log contract

**Mandatory fields by scope:**

| Scope | Always present |
|---|---|
| All records emitted via `obs`-constructed loggers | `time`, `level`, `msg`, `component` |
| Request-scoped (added by middleware) | `req_id`, `principal_hub`, `principal_user_id`, `principal` |

**Reserved field names** (callers must not collide):

- `err` — `err.Error()`
- `dur_ms` — integer milliseconds (matches metric label `method+route` for
  cross-tool joining)
- `status` — HTTP status code (integer)
- `route` — route template, matches metric label
- `method` — HTTP method
- `trace_id`, `span_id` — reserved for future OTel; unused in v1

**`component` field — constrained but tolerant.** Documented intended
values: `httpapi | thumb | share | backup | broker | cli | db | obs |
boot`. Not runtime-enforced; deliberate code may introduce new values.
Dashboards filtering on `component` will see records emitted via
obs-constructed loggers.

**Domain correlation in workers:**

| Worker | Correlation field |
|---|---|
| share | `scope_uuid` |
| thumb | `media_id` |
| backup | `path` |

**Levels — guidance, not policy:**

- `DEBUG` — protocol/state traces, off by default
- `INFO` — request completion, worker tick success, lifecycle (start/stop)
- `WARN` — recoverable failures (retry scheduled, sweep error after
  successful snapshot, stale-warning), config quirks
- `ERROR` — request 5xx, non-retryable worker failure, restore failure,
  lifetime-lock contention at boot

### Per-request logger via context

```go
// httpapi/middleware.go
reqLogger := baseLogger.With(
    "component",          "httpapi",
    "req_id",             reqID,
    "principal_hub",      id.Principal.Hub,
    "principal_user_id",  id.Principal.UserID,
    "principal",          id.Principal.OwnersPrincipal().String(),
)
ctx = httpapi.WithLogger(ctx, reqLogger)
```

Handlers fetch via `httpapi.LoggerFromContext(ctx) *slog.Logger`. Direct
`slog.Info`/`slog.Error` callsites continue working via
`slog.SetDefault(baseLogger)` but won't carry `req_id` until migrated.
Migration is opportunistic, not gated by v1.

### Migration scope (v1, small)

Two files, three field renames:

- `internal/httpapi/middleware.go:52,68` — `dur` → `dur_ms`
  (`.Milliseconds()`), `req` → `req_id`.
- `internal/backup/worker.go:94,112` — `duration_ms` → `dur_ms` for
  cross-component consistency.

All other direct `slog.*` callsites (`internal/httpapi/originals.go`,
`media_original.go`, `shared_bytes.go`, `thumbs.go`, etc.) keep working
via the default logger and migrate opportunistically as those files are
touched.

## 6. Metrics

### Library and registry

`github.com/VictoriaMetrics/metrics` — single dependency, idiomatic Go,
Prometheus-compatible exposition.

`obs.Metrics` owns a private `*metrics.Set` constructed at boot. The
upstream global registry is never read or written. Tests construct
fresh instances per `t.TempDir`.

```go
type Metrics struct {
    set *metrics.Set
    // typed accessors below
}

func NewMetrics(src MetricSources) *Metrics
func NewTestMetrics() *Metrics                 // private set, never exposed

func (m *Metrics) HTTPRequests(method, route, statusClass string) *metrics.Counter
func (m *Metrics) HTTPRequestDuration(method, route string) *metrics.Histogram
func (m *Metrics) ThumbJobs(result string) *metrics.Counter
// … etc.

func (m *Metrics) WritePrometheus(w io.Writer) // for /metrics handler
```

### Metric set (v1)

#### HTTP (2)

```
fotobank_http_requests_total{method, route, status_class}
fotobank_http_request_duration_seconds{method, route}
```

- `method` ∈ `{GET, POST, PUT, PATCH, DELETE, HEAD, OPTIONS, OTHER}`
- `route` is the registered ServeMux pattern with `{x}` → `:x`, or
  `unmatched` for 404s
- `status_class` ∈ `{2xx, 3xx, 4xx, 5xx}`
- Histogram buckets (seconds): `[.005, .01, .025, .05, .1, .25, .5, 1,
  2.5, 5, 10]`

Status code is omitted from the histogram label set; the counter exposes
status_class for error-rate queries, and combining the two on dashboards
gives latency-by-class without exploding histogram cardinality.

#### Thumb worker (3)

```
fotobank_thumb_jobs_total{result}                  result: ok | failed | no_preview
fotobank_thumb_job_duration_seconds{result}        histogram with result
fotobank_thumb_leases_swept_total                  counter
```

`result` matches the thumb domain's terminal states (mapped from
`thumb_status`). `fotobank_thumb_leases_swept_total` is incremented by
total rows reset by `MarkClaimsLost`, surfacing stale-lease churn that
indicates worker death or hangs.

#### Share worker (4)

```
fotobank_share_publishes_total{result}             result: ok | retry | terminal_fail
fotobank_share_publish_duration_seconds{result}
fotobank_share_revokes_total{result}               result: ok | retry | terminal_fail
fotobank_share_revoke_duration_seconds{result}
```

`retry` covers transient broker failures that schedule another attempt;
`terminal_fail` covers max-retries-exhausted.

#### Backup worker (4)

```
fotobank_backup_snapshots_total{result}            result: ok | failed
fotobank_backup_snapshot_duration_seconds{result}
fotobank_backup_retention_sweeps_total{result}     result: ok | failed
fotobank_backup_retention_deleted_total            counter
```

#### Gauges (4)

```
fotobank_thumb_queue_depth{state}                  state: pending | working | failed | no_preview
fotobank_share_pending_total{op}                   op: publish | revoke
fotobank_backup_last_success_unix                  set by backup.Worker on each Snapshot success
fotobank_backup_seconds_since_last_success         derived: now - last_success_unix; -1 if never
```

`fotobank_thumb_queue_depth{state}` mirrors the four non-terminal /
terminal-failure states from `thumb_status`. `ready` is excluded — it's
not a queue state.

DB-backed gauges (queue depth, share pending) are registered via
VictoriaMetrics' `NewGauge(name, fn)` so the SELECT runs at scrape time
(typically every 15s). Closures live in `obs.Metrics`; data sources are
injected at construction:

```go
type MetricSources struct {
    ThumbQueueDepth   func(state string) int64           // nil-safe; nil returns 0
    SharePendingByOp  func(op string) int64              // nil-safe; nil returns 0
}
```

A nil source returns the zero value. Production wires real repo methods;
tests pass deterministic closures.

`fotobank_backup_last_success_unix` is **not** in `MetricSources`. It is
a settable gauge owned by `obs.Metrics`, written directly by
`backup.Worker` after each successful Snapshot via
`m.SetBackupLastSuccess(time.Now().Unix())`. The default value (0)
serves as the "never succeeded" sentinel.
`fotobank_backup_seconds_since_last_success` is derived in the metrics
package via closure: `if last == 0 { return -1 } else { return now -
last }`.

#### Build / runtime

```
fotobank_build_info{version, commit, build_date}   gauge always 1
go_*                                                via metrics.WriteProcessMetrics
```

Reads from `internal/version` (already populated via ldflags). Standard
Go runtime metrics emitted via the upstream library's helper.

### Cardinality budget

| Group | Series at idle | Bound under load |
|---|---|---|
| HTTP counters | 0 | ~30 (8 methods × small route set × 4 classes; sparse in practice) |
| HTTP duration | 0 | ~10 (per active method+route) |
| Worker counters/durations | 11 | 11 |
| Gauges | 7 | 7 |
| Build/runtime | ~10 | ~10 |
| **Total** | **~28** | **~70** |

Comfortable for a single-Prometheus homelab.

### What's deliberately NOT in v1

Listed in §2; recap:

- Per-route SQL timing
- Flash cache hit/miss
- NAS read/write byte volumes
- Per-owner labels
- EXIF parse durations

## 7. Endpoints

### Main listener (`cfg.HTTP.ListenAddress`)

| Path | Auth | Notes |
|---|---|---|
| `/api/v1/healthz` | none | Existing huma route. Cheap liveness; returns `{"status":"ok"}`. Unchanged. |
| `/api/v1/*` (other) | identity | Wrapped by `metrics → recovery → identity → display-cache → handler`. |

`X-Request-ID` response header is set on **every** response, including
401/403 from identity rejection. The header value is read from
`cfg.Identity.Header.RequestIDHeader` if present in the inbound request,
else a fresh UUID. Header is set before identity resolution so rejection
logs can be correlated.

### Admin listener (`cfg.Observability.AdminListen`)

| Path | Notes |
|---|---|
| `/metrics` | Prometheus exposition via `m.WritePrometheus(w)`. |
| `/readyz` | Deep checks (DB ping + snapshot dir + NAS root); 5s cache; 2s deadline; returns 503 on shutdown. |
| `/debug/pprof/*` | Mounted only when `pprof_enabled = true`; otherwise 404. |

No identity middleware on this listener. Loopback-bind enforces isolation.

### `/readyz` semantics

```go
type ReadyzConfig struct {
    DeadlineTotal time.Duration   // default 2s; tests use 20–50ms
    CacheTTL      time.Duration   // default 5s; tests use 0 or 50ms
}
```

Sequence on each probe:

1. If `obs.Ready` is false → return 503 immediately, bypass cache. This
   makes shutdown observable to scrapers regardless of cache freshness.
2. If cached result is fresh (within `CacheTTL`) → return cached status.
3. Run all checks under a single context with `DeadlineTotal` budget:
   - DB ping (`db.PingContext`)
   - When `cfg.Backup.Enabled`: snapshot dir writable (touch a sentinel
     file with `O_EXCL`, remove on success)
   - NAS root: `os.Stat` the configured root (no write)
4. Cache result; return 200 if all pass, 503 with per-check failure
   detail otherwise.

The cached value is the full result (status + per-check map); the cache
holds a single value (no per-key dimension since `/readyz` takes no
parameters). On cache miss or expiry, the next probe pays the check
cost; concurrent probes share the result via a single-flight pattern
(the second-arriving goroutine waits for the first's result rather than
running checks twice).

### Response shapes

```
GET /readyz   200
{
  "status": "ok",
  "checks": [
    {"name": "db_ping",        "status": "ok"},
    {"name": "snapshot_dir",   "status": "ok"},
    {"name": "nas_root",       "status": "ok"}
  ]
}

GET /readyz   503
{
  "status": "fail",
  "checks": [
    {"name": "db_ping",        "status": "ok"},
    {"name": "snapshot_dir",   "status": "fail",
     "err": "open /nas/.fotobank/snapshots/.readyz-probe: read-only file system"},
    {"name": "nas_root",       "status": "ok"}
  ]
}
```

`/metrics` uses Prometheus exposition format (text/plain;version=0.0.4).

## 8. Wiring changes

### `internal/cli/server.go`

Changes:

1. Construct logger and metrics early — before any worker or http.Server.
2. `slog.SetDefault(logger)` for legacy callsite coverage.
3. Pass logger and metrics into `httpapi.New(httpapi.Deps{...})`,
   `thumb.NewWorker`, `shareworker.New`, `backup.NewWorker`.
4. Bind admin listener (when `cfg.Observability.AdminEnabled`) under
   `bgWG`. Admin listener uses its own `http.Server` instance with the
   same shutdown timeout as the main API.
5. On graceful shutdown:
   1. `obs.Ready.Store(false)`
   2. Shut down main API; `bgWG.Wait()` for workers
   3. Shut down admin listener last

### `internal/httpapi/middleware.go`

Changes:

1. `WithMiddleware(idp, baseLogger, metrics)` — accept the new dependencies.
2. Generate `reqID` and set `X-Request-ID` response header **before**
   calling `idp.FromRequest`. Identity errors then log a record with
   `req_id` populated.
3. Build per-request logger via `baseLogger.With(...)` and attach to ctx
   via new `httpapi.WithLogger`/`LoggerFromContext`.
4. Increment `metrics.HTTPRequests(method, route, statusClass)` and
   record into `metrics.HTTPRequestDuration(method, route)` after the
   handler returns (or recovery converts a panic to 500).
5. Rename log fields: `dur` → `dur_ms`, `req` → `req_id`.

A new file `internal/httpapi/recovery.go` adds the recovery middleware
(panic → 500) so the metrics outermost / recovery inside ordering is
explicit and tested.

### Worker `Config` structs

Each worker's `Config` gains a `Metrics *obs.Metrics` field. Workers
record into typed accessors (`m.BackupSnapshots("ok").Inc()` etc.) on
each tick. `m == nil` is rejected at `NewWorker` — production callers
must wire metrics, tests use `obs.NewTestMetrics()`.

### `internal/backup/worker.go`

Changes:

1. On successful snapshot: `m.BackupLastSuccessUnix.Set(time.Now().Unix())`.
2. Record snapshot duration with `result` label (`ok` or `failed`).
3. Rename log field `duration_ms` → `dur_ms`.

## 9. Testing

### Unit tests in `internal/obs/`

| Test | What it covers |
|---|---|
| `TestNewLogger_BufferAutoIsJSON` | format=auto + bytes.Buffer → JSON output |
| `TestNewLogger_TerminalAutoIsText` | format=auto + stubbed `isTerminal` true → text output |
| `TestNewLogger_LevelEnvOverride` | `FOTOBANK_LOG_LEVEL=debug` overrides config |
| `TestNewLogger_InvalidLevelEnvIgnored` | bogus env value falls back to config level + warn |
| `TestNewMetrics_DerivedGauges` | scrape exposition via `WritePrometheus`, parse, assert closure values |
| `TestNewMetrics_NilSourcesSafe` | nil closures return sentinels, not panics |
| `TestReady_FlipsOnShutdown` | flip `obs.Ready` to false; `/readyz` returns 503 even when cache is stale-fresh |
| `TestReadyzCacheBoundedLatency` | 50ms deadline + 100ms-sleep check → 503 within ~60ms |
| `TestReadyzSingleflight` | concurrent probes share one check pass |
| `TestAdminListenValidation` | accepted: `127.0.0.1:9090`, `[::1]:0`, `unix:/tmp/x`. Rejected: `0.0.0.0:9090`, `192.168.1.5:9090`, `example.com:9090`. |

All tests pass `*metrics.Set` instances they construct themselves; no
calls to `metrics.GetOrCreateCounter` or any other global accessor.

### Integration tests in `internal/cli/`

| Test | What it covers |
|---|---|
| `TestE2EObservabilityAdminListener` | boot with admin_enabled, scrape `/metrics`, parse, assert at least `fotobank_build_info` and one `go_*` series |
| `TestE2EObservabilityShutdownReadyz` | boot, GET `/readyz` → 200; cancel; poll `/readyz`, accept 503 *or* connection-refused/EOF (loose; deterministic version is the unit test) |
| `TestE2EHTTPMetricsIncrement` | boot, hit `/api/v1/healthz` 3×, scrape `/metrics`, assert `fotobank_http_requests_total{method="GET",route="/api/v1/healthz",status_class="2xx"} >= 3` |
| `TestE2EAdminLoopbackOnlyValidation` | config with `admin_listen = "0.0.0.0:9090"` rejects at `Validate`, server startup exits with `errs.ErrBadConfiguration` |

The shutdown test is intentionally lenient (503 OR EOF, not strict 503
followed by EOF) because a fast scrape after cancel can race the
listener close. The deterministic invariant lives in the unit test.

### Middleware unit tests in `internal/httpapi/`

| Test | What it covers |
|---|---|
| `TestPanicRecoveryRecordsAs5xx` | wire `metrics → recovery → identity → handler` around an `httptest.NewRecorder` with a handler that panics; assert response is 500 *and* the metrics counter increments with `status_class="5xx"`. Pure unit test — no real server, no production-only panic route. |
| `TestMiddlewareOrderIdentityRejectsCount4xx` | inject identity error; assert metrics counter records `status_class="4xx"` (proves metrics-outside-identity ordering). |

### Worker-instrumentation tests

For each worker (thumb / share / backup) one new test that:

1. Builds the worker with `obs.NewTestMetrics()`.
2. Drives a tick covering both success and failure paths.
3. Reads the counter via the typed accessor (`m.ThumbJobs("ok").Get()`)
   or scrapes once and parses for multi-counter assertions.
4. Asserts a duration histogram observed at least one sample with the
   expected `result` label (sample-count probe via the typed handle).

Existing worker tests continue to pass `io.Discard` or a
`bytes.Buffer`-backed logger; metrics is additive, not replacing
existing test scaffolding.

### Field-contract tests

| Test | What it covers |
|---|---|
| `TestRequestLoggerCarriesContextFields` | drive a request through `WithMiddleware`; capture stderr; parse JSON line; assert `req_id`, `principal_hub`, `principal_user_id`, `component=httpapi`, integer `dur_ms` |
| `TestRejectedRequestStillCarriesReqID` | inject identity error; assert log line carries `req_id` (header set before identity resolution) |
| `TestWorkerLogsCarryComponent` | per worker, drive one tick with buffer-backed logger; assert `component=<expected>` |

### Coverage NOT planned

- Load testing of `/metrics` scrape latency (single-Prometheus,
  one query / 15s)
- Round-tripping through a real Prometheus instance (we own exposition
  format)
- Fuzz testing of route normalization (mechanical: split first space;
  strip method; replace `{x}` with `:x`)

## 10. Open items / future hooks

- **Tracing.** Add OTel SDK + exporter when there's a use case beyond
  per-process debugging. The reserved `trace_id` / `span_id` log fields
  and the existing `req_id` header position fotobank to participate in
  a wider trace context without further log-shape changes.
- **Per-route SQL timing.** Add when a specific bottleneck demands it;
  keep it scoped to that surface, not blanket repo-method timing.
- **Authenticated `/metrics`.** If fotobank ever exposes the admin
  listener over a non-loopback bind, wire identity middleware on the
  admin listener and add a config option for the operator principal.
  Out of scope for v1.
- **Log shipping.** The operator chooses (journald → Loki agent, file
  tail → Vector, stderr → Docker log driver). Fotobank emits to stderr
  and stops there; downstream pipelines are a deployment concern.
- **Alerting rules.** Operator-curated based on dashboard usage; no
  fotobank-shipped Prometheus rule files in v1.

---

*End of observability design spec.*
