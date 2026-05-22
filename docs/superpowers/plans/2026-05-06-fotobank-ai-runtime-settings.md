# Fotobank AI Runtime Settings Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add server-global runtime AI settings with DB overrides, admin-only APIs, endpoint diagnostics, hot-applied worker snapshots, and an admin AI settings UI.

**Architecture:** TOML remains the bootstrap/default config, while a new `app_settings` table stores allowlisted JSON overrides. A validated `EffectiveAIConfigProvider` publishes atomic snapshots; admin Apply/reset writes DB state, handles embed generation creation, commits, then reloads the provider. Workers claim only jobs whose `ai_jobs.fingerprint` matches the current claim fingerprint, so endpoint/model/key changes do not silently process old queued work under new config.

**Tech Stack:** Go 1.x, SQLite/sqlx-style `database/sql`, huma/v2, cobra server wiring, Svelte 5, openapi-fetch, Vitest, Playwright, `make test`, `make api-generate`.

**Branching:** Execute directly on `master`. Do not create a feature branch or worktree for this repo.

---

## File Structure

Backend persistence/config:

- Modify `internal/db/migrations/000001_initial_schema.up.sql`: add `app_settings`.
- Modify `internal/db/migrations/000001_initial_schema.down.sql`: drop `app_settings`.
- Modify `internal/db/migrations_test.go`: assert `app_settings` exists in fresh migrated DB.
- Modify `internal/config/config.go`: add `[admin].principals` config and defaulting/validation.
- Modify `internal/config/config.example.toml`: document `[admin]`.
- Modify `internal/config/config_test.go`: stub default, header empty allowlist, explicit allowlist, TOML parsing.

Backend app settings and runtime config:

- Create `internal/appsettings/repo.go`: DB-only global key/value repo.
- Create `internal/appsettings/repo_test.go`.
- Create `internal/service/appsettings/keys.go`: allowlist, section membership, typed codecs.
- Create `internal/service/appsettings/service.go`: admin service for effective GET, section Apply, per-key reset, section reset.
- Create `internal/service/appsettings/service_test.go`.
- Create `internal/ai/runtime/provider.go`: effective AI config provider.
- Create `internal/ai/runtime/fingerprint.go`: claim/result fingerprint derivation.
- Create `internal/ai/runtime/provider_test.go`.
- Modify `internal/ai/embedding/generations.go`: add tx-aware `FindOrCreateBuildingTx`.
- Modify `internal/ai/embedding/generations_test.go`.

Backend probes/admin HTTP:

- Create `internal/ai/probe/probe.go`: common result/classification types.
- Create `internal/ai/probe/vision.go`.
- Create `internal/ai/probe/embed.go`.
- Create `internal/ai/probe/probe_test.go`.
- Create `internal/httpapi/admin_settings.go`: admin settings routes.
- Create `internal/httpapi/admin_auth.go`: `requireAdmin`.
- Create `internal/httpapi/admin_settings_test.go`.
- Modify `internal/httpapi/api.go`: add deps and register admin settings routes.
- Modify `internal/httpapi/me.go` and tests if the SPA needs `features.admin_settings` for link visibility.

Worker hot-apply:

- Modify `internal/ai/jobs/queue.go`: add fingerprint-filtered claim and task/fingerprint supersession helpers.
- Modify `internal/ai/jobs/queue_test.go`.
- Modify `internal/ai/gapscanner/scanner.go`: enqueue claim fingerprints while still checking result fingerprints.
- Modify `internal/ai/gapscanner/scanner_test.go`.
- Modify `internal/ai/worker/worker.go`: read snapshot per loop, claim by claim fingerprint, use snapshot result fingerprint/gateway.
- Modify `internal/ai/worker/worker_test.go`.
- Modify `internal/ai/embedding/worker.go`: read snapshot per loop, claim by claim fingerprint, use snapshot embed result fingerprint/generation.
- Modify `internal/ai/embedding/worker_test.go`.
- Modify `internal/ai/embedding/activator.go`: read provider snapshot for current embed config where needed.
- Modify `internal/cli/server.go`: wire provider, admin service, probes, worker constructors, and reload paths.
- Modify `internal/cli/server_test.go`.

Backup:

- Modify `internal/backup/snapshot_test.go`: prove `app_settings` rows are included and secrets are not stored.

Frontend:

- Create `frontend/src/lib/adminSettings/types.ts`.
- Create `frontend/src/lib/adminSettings/client.ts`.
- Create `frontend/src/lib/adminSettings/adminSettingsStore.svelte.ts`.
- Create `frontend/src/lib/adminSettings/adminSettingsStore.test.ts`.
- Create `frontend/src/lib/adminSettings/AdminSettingsSection.svelte`.
- Create `frontend/src/lib/adminSettings/AdminSettingsSection.test.ts`.
- Create `frontend/src/routes/AdminSettingsAI.svelte`.
- Create `frontend/src/routes/AdminSettingsAI.test.ts`.
- Modify `frontend/src/routes/SettingsAI.svelte`: render admin-only “Configure AI…” link.
- Modify `frontend/src/routes/SettingsAI.test.ts`.
- Modify `frontend/src/lib/router/router.svelte.ts`: parse `/admin/settings/ai`.
- Modify `frontend/src/lib/router/router.test.ts`.
- Modify `frontend/src/App.svelte`: route guard/render admin page.

E2E/openapi:

- Modify `cmd/e2e-server/main.go`: probe modes via `X-Probe-Mode`.
- Create `frontend/tests/e2e/admin-settings.spec.ts`.
- Modify `openapi.json` and `frontend/src/lib/api/generated/schema.ts` via `make api-generate`.

---

## Task 1: Schema And Admin Config

**Files:**
- Modify `internal/db/migrations/000001_initial_schema.up.sql`
- Modify `internal/db/migrations/000001_initial_schema.down.sql`
- Modify `internal/db/migrations_test.go`
- Modify `internal/config/config.go`
- Modify `internal/config/config.example.toml`
- Modify `internal/config/config_test.go`

- [ ] **Step 1: Add failing migration test**

Add a test in `internal/db/migrations_test.go`:

```go
func TestSchema_AppSettingsExists(t *testing.T) {
    d := testutil.OpenTestDB(t)
    var name string
    err := d.ReadDB().QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='app_settings'`).Scan(&name)
    require.NoError(t, err)
    require.Equal(t, "app_settings", name)
}
```

- [ ] **Step 2: Run the failing test**

Run: `go test -tags sqlite_fts5 ./internal/db -run TestSchema_AppSettingsExists -count=1`

Expected: FAIL because `app_settings` does not exist.

- [ ] **Step 3: Add schema**

Add to `000001_initial_schema.up.sql` near `user_settings`:

```sql
CREATE TABLE app_settings (
    key                 TEXT      PRIMARY KEY,
    value               TEXT      NOT NULL,
    updated_at          TIMESTAMP NOT NULL,
    updated_by_hub      TEXT,
    updated_by_user_id  TEXT
);
```

Add `DROP TABLE IF EXISTS app_settings;` to `000001_initial_schema.down.sql`.

- [ ] **Step 4: Run DB tests**

Run: `go test -tags sqlite_fts5 ./internal/db -count=1`

Expected: PASS.

- [ ] **Step 5: Add admin config tests**

In `internal/config/config_test.go`, add tests for:

- `[admin].principals` TOML parsing.
- absent `[admin]` + `identity.mode="stub"` defaults to stub principal.
- absent `[admin]` + `identity.mode="header"` leaves allowlist empty.
- malformed admin principal fails validation.

- [ ] **Step 6: Implement admin config**

In `internal/config/config.go`, add:

```go
type Config struct {
    // existing fields...
    Admin Admin `toml:"admin"`
}

type Admin struct {
    Principals []AdminPrincipal `toml:"principals"`
}

type AdminPrincipal struct {
    Hub    string `toml:"hub"`
    UserID string `toml:"user_id"`
}
```

Default after identity defaults are applied:

```go
if len(c.Admin.Principals) == 0 && c.Identity.Mode == "stub" {
    c.Admin.Principals = []AdminPrincipal{{
        Hub: c.Identity.Stub.Hub, UserID: c.Identity.Stub.UserID,
    }}
}
```

Validate non-empty `hub` and `user_id` when a principal is configured.

- [ ] **Step 7: Document config**

Add to `internal/config/config.example.toml`:

```toml
[admin]
# In stub mode this defaults to the configured stub principal when omitted.
# In header mode, set this explicitly or admin routes deny everyone.
principals = [
  { hub = "dev-local", user_id = "owner" },
]
```

- [ ] **Step 8: Verify and commit**

Run: `go test -tags sqlite_fts5 ./internal/config ./internal/db -count=1`

Expected: PASS.

Commit:

```bash
git add internal/db/migrations internal/db/migrations_test.go internal/config
git commit -m "feat(config): add global app settings schema"
```

---

## Task 2: App Settings Repo And Effective Provider

**Files:**
- Create `internal/appsettings/repo.go`
- Create `internal/appsettings/repo_test.go`
- Create `internal/service/appsettings/keys.go`
- Create `internal/service/appsettings/service.go`
- Create `internal/service/appsettings/service_test.go`
- Create `internal/ai/runtime/provider.go`
- Create `internal/ai/runtime/fingerprint.go`
- Create `internal/ai/runtime/provider_test.go`

- [ ] **Step 1: Write repo tests**

Test `Upsert`, `Get`, `List`, `Delete`, and audit metadata. Use `testutil.OpenTestDB(t)`.

Run: `go test -tags sqlite_fts5 ./internal/appsettings -count=1`

Expected: FAIL because package does not exist.

- [ ] **Step 2: Implement repo**

`internal/appsettings/repo.go` should be DB-only:

```go
type Row struct {
    Key       string
    Value     string
    UpdatedAt time.Time
    UpdatedByHub *string
    UpdatedByUserID *string
}

type Repo struct { rw, ro *sql.DB }
func NewRepo(rw, ro *sql.DB) *Repo
func (r *Repo) Upsert(ctx context.Context, key, value string, by *owners.Principal) error
func (r *Repo) Get(ctx context.Context, key string) (Row, bool, error)
func (r *Repo) List(ctx context.Context) ([]Row, error)
func (r *Repo) Delete(ctx context.Context, key string) error
func (r *Repo) DeleteMany(ctx context.Context, keys []string) error
```

- [ ] **Step 3: Verify repo**

Run: `go test -tags sqlite_fts5 ./internal/appsettings -count=1`

Expected: PASS.

- [ ] **Step 4: Write key registry tests**

In `internal/service/appsettings/service_test.go`, test:

- all 13 allowlisted keys exist
- section membership for `master`, `vision`, `tag`, `caption`, `embed`
- non-allowlisted key returns `ErrKeyNotEditable`
- JSON type validation catches wrong types
- empty `api_key_env` is allowed and reports `required=false`

- [ ] **Step 5: Implement key registry and service DTOs**

In `internal/service/appsettings/keys.go`, define constants for the 13 keys and section helpers:

```go
type Section string
const (
    SectionMaster Section = "master"
    SectionVision Section = "vision"
    SectionTag Section = "tag"
    SectionCaption Section = "caption"
    SectionEmbed Section = "embed"
)
```

Expose `KeysForSection`, `SectionForKey`, `Editable(key string) bool`, JSON decode helpers, and `ErrKeyNotEditable`.

- [ ] **Step 6: Write provider tests**

In `internal/ai/runtime/provider_test.go`, cover:

- file defaults only
- DB overrides win
- invalid DB override keeps previous snapshot live
- reload mutex serialization with concurrent reload calls
- changing `ai.embed.model` changes embed claim/result fingerprint
- changing `ai.vision.endpoint` changes tag and caption claim fingerprints

- [ ] **Step 7: Implement provider and fingerprints**

`provider.go`:

```go
type Source struct {
    FilePath string
    Repo interface { List(context.Context) ([]appsettings.Row, error) }
}

type Provider struct {
    mu sync.Mutex
    snapshot atomic.Pointer[Snapshot]
    source Source
}

type Snapshot struct {
    Config ai.Config
    FileDefault ai.Config
    Overrides map[string]appsettings.Row
    Claim ClaimFingerprints
    Result ResultFingerprints
}

func NewProvider(ctx context.Context, source Source) (*Provider, error)
func (p *Provider) Effective() Snapshot
func (p *Provider) Reload(ctx context.Context) error
```

`fingerprint.go` derives both claim and result fingerprints. Claim fingerprints may be stable string hashes over a JSON struct; result fingerprints use existing `ai.Fingerprint` / `embedding.Fingerprint`.

- [ ] **Step 8: Verify provider/service**

Run: `go test -tags sqlite_fts5 ./internal/appsettings ./internal/service/appsettings ./internal/ai/runtime -count=1`

Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/appsettings internal/service/appsettings internal/ai/runtime
git commit -m "feat(ai): add runtime settings provider"
```

---

## Task 3: Embedding Generation Transaction Helper

**Files:**
- Modify `internal/ai/embedding/generations.go`
- Modify `internal/ai/embedding/generations_test.go`

- [ ] **Step 1: Write failing tx helper test**

Add a test proving `FindOrCreateBuildingTx` inserts the row and vec table inside the caller transaction, and rollback removes both.

Run: `go test -tags sqlite_fts5 ./internal/ai/embedding -run TestGenerations_FindOrCreateBuildingTx -count=1`

Expected: FAIL.

- [ ] **Step 2: Implement tx helper**

Refactor existing insert logic so:

```go
func (g *Generations) FindOrCreateBuildingTx(ctx context.Context, tx *sql.Tx, fp ai.Fingerprint, dim int) (Row, error)
```

uses the caller tx for find/insert/update/create virtual table. Keep `FindOrCreateBuilding` as a wrapper that opens/commits its own tx and emits lifecycle events after commit.

- [ ] **Step 3: Verify**

Run: `go test -tags sqlite_fts5 ./internal/ai/embedding -count=1`

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/ai/embedding/generations.go internal/ai/embedding/generations_test.go
git commit -m "feat(ai): support transactional embed generations"
```

---

## Task 4: Admin Service Apply/Reset Semantics

**Files:**
- Modify `internal/service/appsettings/service.go`
- Modify `internal/service/appsettings/service_test.go`
- Modify `internal/ai/runtime/provider.go`

- [ ] **Step 1: Write Apply tests**

Cover:

- `GET` model returns all 13 effective and file-default keys
- override metadata with nullable `updated_by`
- `api_key_env_status` for empty and set env var
- section Apply upserts in one tx
- per-key reset deletes one override and recomputes effective
- section reset deletes all section overrides
- embed Apply calls `FindOrCreateBuildingTx` and returns integer generation id
- post-commit reload failure returns `ErrReloadFailed` and keeps previous provider snapshot

- [ ] **Step 2: Implement service**

Use a service constructor shaped like:

```go
type Service struct {
    repo *appsettings.Repo
    provider *runtime.Provider
    gens *embedding.Generations
    db *db.DB // or *sql.DB writer plus reader, following existing patterns
}
```

Expose:

```go
func (s *Service) Effective(ctx context.Context) (EffectiveResponse, error)
func (s *Service) ApplySection(ctx context.Context, caller owners.Principal, section Section, values map[string]any) (ApplyResponse, error)
func (s *Service) ResetKey(ctx context.Context, caller owners.Principal, key string) (ApplyResponse, error)
func (s *Service) ResetSection(ctx context.Context, caller owners.Principal, section Section) (ApplyResponse, error)
```

Return typed sentinel errors for validation, key-not-editable, and reload-failed so HTTP mapping can produce 400/409/500.

- [ ] **Step 3: Verify**

Run: `go test -tags sqlite_fts5 ./internal/service/appsettings ./internal/ai/runtime -count=1`

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/service/appsettings internal/ai/runtime
git commit -m "feat(ai): apply runtime settings sections"
```

---

## Task 5: Synthetic Probe Package

**Files:**
- Create `internal/ai/probe/probe.go`
- Create `internal/ai/probe/vision.go`
- Create `internal/ai/probe/embed.go`
- Create `internal/ai/probe/probe_test.go`

- [ ] **Step 1: Write classification tests**

Use `httptest.Server` fixtures for:

- `ok`
- `auth_failed`
- `unreachable`
- `timeout`
- `model_mismatch`
- `dimension_mismatch`
- `malformed_response`
- `provider_error`
- vision 200 with missing/different model echo returns `ok=true` with warning
- empty `api_key_env` sends no auth header
- non-empty unset `api_key_env` returns `auth_failed` without making HTTP request

- [ ] **Step 2: Implement result types**

```go
type Classification string
type Result struct {
    OK bool `json:"ok"`
    Classification Classification `json:"classification"`
    LatencyMS int64 `json:"latency_ms"`
    Detail string `json:"detail"`
    ModelEchoed *string `json:"model_echoed,omitempty"`
    Warnings []string `json:"warnings"`
}
```

- [ ] **Step 3: Implement probes**

Vision sends a chat-completions request with one tiny JPEG data URL, prompt `Reply with the single word 'ok'.`, `max_tokens=8`, `temperature=0`, fixed 15s timeout.

Embed sends `POST /v1/embeddings` with input `"test"` and checks `len(data[0].embedding) == configured dimension`.

- [ ] **Step 4: Verify**

Run: `go test -tags sqlite_fts5 ./internal/ai/probe -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/ai/probe
git commit -m "feat(ai): add admin endpoint probes"
```

---

## Task 6: Admin HTTP Routes

**Files:**
- Create `internal/httpapi/admin_auth.go`
- Create `internal/httpapi/admin_settings.go`
- Create `internal/httpapi/admin_settings_test.go`
- Modify `internal/httpapi/api.go`
- Modify `internal/httpapi/me.go`
- Modify `internal/httpapi/me_test.go`

- [ ] **Step 1: Write route tests**

Cover:

- unauthenticated admin route returns 401
- authenticated non-admin returns 403 with no role hints
- `GET /api/v1/admin/settings`
- `PUT /api/v1/admin/settings/sections/embed`
- `DELETE /api/v1/admin/settings/keys/{key}`
- `DELETE /api/v1/admin/settings/sections/{section}`
- `POST /api/v1/admin/settings/test/vision`
- validation failure body
- non-editable key returns 409
- reload failure returns 500
- probe emits one structured log line without secrets

- [ ] **Step 2: Implement auth and routes**

Add Deps:

```go
AdminSettings *appsettingssvc.Service
AdminPrincipals []owners.Principal
AdminProbeLogger *slog.Logger
```

Register routes only when `AdminSettings != nil`; tests can pass nil for unrelated API surfaces.

- [ ] **Step 3: Surface admin capability to SPA**

Extend `/api/v1/me` feature payload with `admin_settings_enabled` or similar boolean. It should be true only when the current caller is admin. Update `frontend` types after API generation in a later task.

- [ ] **Step 4: Regenerate API**

Run: `make api-generate`

Expected: `openapi.json` and `frontend/src/lib/api/generated/schema.ts` update.

- [ ] **Step 5: Verify**

Run: `go test -tags sqlite_fts5 ./internal/httpapi -run 'Admin|Me' -count=1`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/httpapi openapi.json frontend/src/lib/api/generated/schema.ts
git commit -m "feat(httpapi): add admin AI settings routes"
```

---

## Task 7: Queue, Gap Scanner, And Worker Claim Fingerprints

**Files:**
- Modify `internal/ai/jobs/queue.go`
- Modify `internal/ai/jobs/queue_test.go`
- Modify `internal/ai/gapscanner/scanner.go`
- Modify `internal/ai/gapscanner/scanner_test.go`
- Modify `internal/ai/worker/worker.go`
- Modify `internal/ai/worker/worker_test.go`
- Modify `internal/ai/embedding/worker.go`
- Modify `internal/ai/embedding/worker_test.go`

- [ ] **Step 1: Add queue tests**

Tests:

- `ClaimBatchForFingerprint` claims only matching pending rows.
- superseding an old claim fingerprint terminally supersedes pending/blocked/working jobs for `(media, task)`.
- old fingerprint rows are not claimed after a new fingerprint exists.

- [ ] **Step 2: Implement queue methods**

Add:

```go
func (q *Queue) ClaimBatchForFingerprint(ctx context.Context, task ai.Task, fp string, n int) ([]Claim, error)
func (q *Queue) SupersedeForFingerprintChange(ctx context.Context, task ai.Task, mediaIDs []string, newClaimFP string) error
```

Keep existing `ClaimBatch` if CLI/tests still need it, but production workers must move to the fingerprint-aware path.

- [ ] **Step 3: Update gap scanner tests**

Test that:

- scanner receives both `ClaimFingerprint string` and result `ai.Fingerprint`
- result skip checks still use result fingerprint
- enqueued `ai_jobs.fingerprint` is claim fingerprint
- vision endpoint changes supersede tag/caption old jobs on backfill

- [ ] **Step 4: Update gap scanner implementation**

Extend scan request:

```go
type ScanRequest struct {
    Task ai.Task
    ClaimFingerprint string
    ResultFingerprint ai.Fingerprint
    // existing fields...
}
```

Use result fingerprint for `ai_results` / `ai_failures` checks and claim fingerprint for `Queue.Enqueue`.

- [ ] **Step 5: Update tag/caption worker tests**

Construct a fake provider that returns snapshot A then snapshot B. Seed one old-fingerprint and one current-fingerprint job. Assert the worker claims only current and writes results with the snapshot result fingerprint.

- [ ] **Step 6: Refactor tag/caption worker**

Change worker config to accept provider/snapshot access rather than static `Fingerprint`, `Gateway`, and model. At the top of `RunOnce`, read snapshot; if `ai.enabled` or task enabled is false, return without claiming. Claim with current claim fingerprint. Build gateway/client from snapshot or use a small runtime gateway factory.

- [ ] **Step 7: Update embed worker tests**

Assert embed worker claims only current embed claim fingerprint and uses snapshot result fingerprint/generation for `FindOrCreateBuilding`.

- [ ] **Step 8: Refactor embed worker**

Stop parsing `ai_jobs.fingerprint` as an `ai.Fingerprint`. The job fingerprint is queue-safety identity. Use the snapshot's embed result fingerprint to find/create generation, call the configured endpoint/model, and record failures/mappings.

- [ ] **Step 9: Verify**

Run:

```bash
go test -tags sqlite_fts5 ./internal/ai/jobs ./internal/ai/gapscanner ./internal/ai/worker ./internal/ai/embedding -count=1
```

Expected: PASS.

- [ ] **Step 10: Commit**

```bash
git add internal/ai/jobs internal/ai/gapscanner internal/ai/worker internal/ai/embedding
git commit -m "feat(ai): claim jobs by runtime fingerprint"
```

---

## Task 8: Server Wiring And Health

**Files:**
- Modify `internal/cli/server.go`
- Modify `internal/cli/server_test.go`
- Modify `internal/cli/ai.go`
- Modify `internal/service/ai/service.go`
- Modify `internal/service/ai/health.go`
- Modify `internal/service/ai/service_test.go`
- Modify `internal/service/ai/health_test.go`

- [ ] **Step 1: Write wiring tests**

In `internal/cli/server_test.go`, add tests that boot a server with:

- stub admin default
- header mode absent admin allowlist
- DB override for `ai.enabled=false` reflected in `/api/v1/ai/health`
- applying `ai.enabled=false` stops new claims after reload

- [ ] **Step 2: Wire provider**

In `runServer`, construct app settings repo/service and runtime provider after DB open and config load. Replace boot-captured `cfg.AI` uses in AI service/worker construction with provider snapshot usage where hot apply is required.

- [ ] **Step 3: Wire admin routes**

Pass `AdminSettings`, parsed `AdminPrincipals`, and logger to `httpapi.New`.

- [ ] **Step 4: Update AI CLI**

CLI commands still load TOML plus DB overrides when they mutate or inspect AI state. Use the same provider/overlay logic in `loadAICtx` so `fotobank ai backfill` targets the effective runtime config.

- [ ] **Step 5: Update health service**

Health should read current effective config from provider so `/api/v1/ai/health` reflects runtime overrides. Preserve ack behavior and existing counters.

- [ ] **Step 6: Verify**

Run:

```bash
go test -tags sqlite_fts5 ./internal/cli ./internal/service/ai -run 'AI|Server|Health' -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/cli internal/service/ai
git commit -m "feat(ai): wire runtime settings into server"
```

---

## Task 9: Backup Boundary Test

**Files:**
- Modify `internal/backup/snapshot_test.go`

- [ ] **Step 1: Write failing backup test**

Add a test that writes:

- `ai.vision.api_key_env` override with value `"FOTOBANK_VLM_KEY"`
- a fake raw key in process memory only, never DB

Run snapshot, open snapshot DB read-only, assert row exists and the stored value is the JSON string env var name only.

- [ ] **Step 2: Verify**

Run: `go test -tags sqlite_fts5 ./internal/backup -run AppSettings -count=1`

Expected: PASS after Task 1 schema exists and no raw secrets are stored.

- [ ] **Step 3: Commit**

```bash
git add internal/backup/snapshot_test.go
git commit -m "test(backup): preserve app settings without secrets"
```

---

## Task 10: Frontend Client, Store, And Routing

**Files:**
- Create `frontend/src/lib/adminSettings/types.ts`
- Create `frontend/src/lib/adminSettings/client.ts`
- Create `frontend/src/lib/adminSettings/adminSettingsStore.svelte.ts`
- Create `frontend/src/lib/adminSettings/adminSettingsStore.test.ts`
- Modify `frontend/src/lib/router/router.svelte.ts`
- Modify `frontend/src/lib/router/router.test.ts`
- Modify `frontend/src/App.svelte`

- [ ] **Step 1: Write store tests**

Tests:

- loads effective response
- tracks per-section dirty state
- resets one key
- resets a section
- runs probe with pending values
- maps validation error to section `error`
- embed apply response preserves numeric `generation_id`

Run: `cd frontend && bun test src/lib/adminSettings/adminSettingsStore.test.ts`

Expected: FAIL.

- [ ] **Step 2: Implement types/client/store**

Use generated OpenAPI client methods where available. If generated path typing is awkward, wrap `api.GET`/`api.PUT`/`api.DELETE`/`api.POST` in focused functions:

```ts
export async function getAdminSettings(client = api): Promise<AdminSettingsResponse>
export async function applySection(section: Section, values: SectionValues, client = api): Promise<ApplyResponse>
export async function resetKey(key: EditableKey, client = api): Promise<ApplyResponse>
export async function resetSection(section: Section, client = api): Promise<ApplyResponse>
export async function testProbe(kind: "vision" | "embed", values: ProbeRequest, client = api): Promise<ProbeResult>
```

- [ ] **Step 3: Add route parser test**

Add `/admin/settings/ai` to `RouteMatch` and parser tests.

- [ ] **Step 4: Wire App route**

Import `AdminSettingsAI` later, or temporarily render a placeholder until Task 11. Add SPA guard so non-admins redirect to `/settings/ai` or `/`.

- [ ] **Step 5: Verify**

Run:

```bash
cd frontend
bun test src/lib/adminSettings/adminSettingsStore.test.ts src/lib/router/router.test.ts
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add frontend/src/lib/adminSettings frontend/src/lib/router frontend/src/App.svelte
git commit -m "feat(frontend): add admin settings store"
```

---

## Task 11: Admin AI Settings UI

**Files:**
- Create `frontend/src/lib/adminSettings/AdminSettingsSection.svelte`
- Create `frontend/src/lib/adminSettings/AdminSettingsSection.test.ts`
- Create `frontend/src/routes/AdminSettingsAI.svelte`
- Create `frontend/src/routes/AdminSettingsAI.test.ts`
- Modify `frontend/src/routes/SettingsAI.svelte`
- Modify `frontend/src/routes/SettingsAI.test.ts`
- Modify `frontend/src/App.svelte`

- [ ] **Step 1: Write component tests**

For `AdminSettingsSection`:

- badge states `clean`, `overridden`, `dirty`, `error`
- Apply disabled when invalid
- Test disabled when invalid
- Discard resets section-local edits
- per-key reset calls handler
- section reset calls handler

- [ ] **Step 2: Implement shared section component**

Keep controls dense and utilitarian. Use existing CSS variables. Do not nest cards. Stable dimensions for buttons and status badges.

- [ ] **Step 3: Write route tests**

For `AdminSettingsAI.svelte`:

- renders master toggle
- renders Vision/Tag/Caption/Embed stacked sections
- probe uses pending unsaved values
- embed generation-defining change opens confirmation modal
- modal shows current generation id, proposed model/dimension/input edge, eligible count, estimate, and old-generation-stays-active copy
- Cancel does not apply
- Confirm applies and displays returned generation id

- [ ] **Step 4: Implement route**

Use `AdminSettingsStore`. Master toggle has its own Apply/reset. Embed confirmation only triggers for `ai.embed.model`, `ai.embed.dimension`, or `ai.embed.input_edge`, not for `ai.enabled`.

- [ ] **Step 5: Add discovery link**

In `SettingsAI.svelte`, render “Configure AI…” in the header only when `/me` feature says the caller is admin. The link goes to `/admin/settings/ai`. Non-admins do not see it.

- [ ] **Step 6: Verify frontend unit tests**

Run:

```bash
cd frontend
bun test src/lib/adminSettings/AdminSettingsSection.test.ts src/routes/AdminSettingsAI.test.ts src/routes/SettingsAI.test.ts
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add frontend/src/lib/adminSettings frontend/src/routes frontend/src/App.svelte
git commit -m "feat(frontend): add admin AI settings page"
```

---

## Task 12: E2E Mocks And Browser Coverage

**Files:**
- Modify `cmd/e2e-server/main.go`
- Create `frontend/tests/e2e/admin-settings.spec.ts`
- Modify `frontend/playwright-e2e.config.ts` only if route setup needs env changes

- [ ] **Step 1: Extend e2e mocks**

Add `X-Probe-Mode` handling to the existing mock VLM and embed handlers:

- `ok`
- `auth_failed`
- `timeout`
- `wrong_dim`
- `wrong_model`
- `malformed`
- `provider_error`

Existing tests do not set the header and keep current behavior.

- [ ] **Step 2: Write Playwright tests**

In `admin-settings.spec.ts`:

- admin reaches `/admin/settings/ai`
- editing embed model/dimension shows confirmation modal
- modal contains current generation id, proposed model/dimension, and old-generation-stays-active copy
- Cancel does not apply
- Confirm applies and surfaces `generation_id`
- wrong-dimension probe shows failure
- section reset restores file defaults
- direct non-admin API request gets 403 with no role hints
- non-admin SPA navigation redirects and `/settings/ai` link is absent

- [ ] **Step 3: Run focused e2e**

Run:

```bash
cd frontend
bunx playwright test tests/e2e/admin-settings.spec.ts --config playwright-e2e.config.ts
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add cmd/e2e-server/main.go frontend/tests/e2e/admin-settings.spec.ts frontend/playwright-e2e.config.ts
git commit -m "test(e2e): cover admin AI settings"
```

---

## Task 13: Full Verification And API Regeneration Sweep

**Files:**
- `openapi.json`
- `frontend/src/lib/api/generated/schema.ts`
- Any files changed by hooks

- [ ] **Step 1: Regenerate API**

Run: `make api-generate`

Expected: `openapi.json` and generated frontend schema match the new admin API.

- [ ] **Step 2: Go tests**

Run: `make test-short`

Expected: PASS.

- [ ] **Step 3: Frontend tests**

Run:

```bash
cd frontend
bun test
```

Expected: PASS.

- [ ] **Step 4: Focused e2e**

Run:

```bash
cd frontend
bunx playwright test tests/e2e/admin-settings.spec.ts tests/e2e/ai.spec.ts --config playwright-e2e.config.ts
```

Expected: PASS.

- [ ] **Step 5: Lint**

Run: `make lint`

Expected: PASS. If hooks modify files, inspect and commit those changes.

- [ ] **Step 6: Final commit**

If any generated or lint changes remain:

```bash
git add openapi.json frontend/src/lib/api/generated/schema.ts
git commit -m "chore: refresh generated API for AI settings"
```

---

## Implementation Notes

- Use `require.ErrorIs` for sentinel checks.
- Do not store raw API key values. `api_key_env` is the only persisted secret-adjacent value.
- Run `make api-generate` after admin HTTP routes are added.
- Pre-alpha schema policy means editing `000001_initial_schema.{up,down}.sql` in place.
- Keep old in-flight jobs uncanceled. Hot apply only affects future claims.
- Claim fingerprints are queue-safety identities and may include endpoint/key fields. Result/generation fingerprints remain provenance identities and must not include secrets.
- Commit after every task. Do not amend commits.
