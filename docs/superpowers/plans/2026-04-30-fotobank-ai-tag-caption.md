# Fotobank AI Tagging & Captioning Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add server-side VLM-driven tagging and captioning to fotobank, surfaced in the lightbox info panel and managed from a Settings → AI page, with a real acknowledgement gate that parks workers until the operator confirms hidden-photo processing.

**Architecture:** OpenAI-compatible chat-completions client with a single shared `[ai.vision]` endpoint and per-task model overrides. Two background workers (tag, caption) follow `internal/thumb`'s claim/lease pattern, gated by a global `[ai.vision].max_inflight` semaphore so per-task concurrency caps don't oversubscribe a shared GPU. Result-set storage model: one `ai_results` row per run with child `media_tags` / `media_captions`; reruns transition the prior `active` row to `stale` and promote the new one atomically. AI-agnostic importer enqueues by `(media, task)` with idempotent unique key. A separate `internal/ai/gapscanner` (CLI + periodic tick) repairs gaps, distinct from the existing disk reconciler.

**Tech Stack:** Go (`database/sql`, `modernc.org/sqlite`, huma/v2, `golang.org/x/sync/semaphore`, `github.com/VictoriaMetrics/metrics`); pure-Go JPEG re-encode via the existing thumb pipeline; Svelte 5 + TypeScript on the frontend.

**Reference docs:**
- Spec: `docs/superpowers/specs/2026-04-30-fotobank-ai-tag-caption-design.md`
- Pattern reference: `internal/thumb/queue.go` + `internal/thumb/worker.go` (claim/lease + Run loop)
- HTTP client retry pattern: `~/code/msgvault/internal/vector/embed/client.go`
- Pre-alpha schema policy (per `CLAUDE.md`): edit `internal/db/migrations/000001_initial_schema.up.sql` in place; the down file is a wholesale teardown and stays valid.

---

## File structure

Backend (new):

| File | Responsibility |
|---|---|
| `internal/db/migrations/000001_initial_schema.up.sql` | (modify) append AI tables + indexes |
| `internal/ai/types.go` | `Task`, `Fingerprint`, `JobStatus`, `LastErrorKind` |
| `internal/ai/config.go` | TOML types: `Config`, `VisionConfig`, `TaskConfig`; `Validate`, `ApplyDefaults` |
| `internal/ai/prompts/prompts.go` | `TagPromptV1`, `CaptionPromptV1` consts; `Version()` / `Hash()` helpers |
| `internal/ai/prompts/prompts_test.go` | CI hash pin |
| `internal/ai/gateway/gateway.go` | `VisionGateway` interface + request/response types |
| `internal/ai/gateway/openai.go` | `OpenAICompatible` impl + retry policy |
| `internal/ai/gateway/openai_test.go` | tests via `httptest.Server` |
| `internal/ai/imginput/encode.go` | re-encode preview → 1024px q85 jpeg, EXIF stripped |
| `internal/ai/imginput/encode_test.go` | tests |
| `internal/ai/parse/tags.go` | defensive tag JSON parsing + normalization |
| `internal/ai/parse/caption.go` | defensive caption parsing + validation |
| `internal/ai/parse/*_test.go` | tests |
| `internal/ai/jobs/queue.go` | DB-only job queue (mirrors `internal/thumb/queue.go`) |
| `internal/ai/jobs/queue_test.go` | tests |
| `internal/ai/results/repo.go` | atomic stale-old + promote-new for `ai_results` + child rows |
| `internal/ai/results/repo_test.go` | tests |
| `internal/ai/failures/repo.go` | `ai_failures` CRUD |
| `internal/ai/failures/repo_test.go` | tests |
| `internal/ai/skipped/repo.go` | `ai_skipped` CRUD |
| `internal/ai/skipped/repo_test.go` | tests |
| `internal/ai/ack/ack.go` | acknowledgement helper over `user_settings` |
| `internal/ai/ack/ack_test.go` | tests |
| `internal/ai/worker/semaphore.go` | shared vision semaphore |
| `internal/ai/worker/worker.go` | generic per-task `Worker` (Run/RunOnce) |
| `internal/ai/worker/worker_test.go` | tests with mock gateway |
| `internal/ai/gapscanner/scanner.go` | gap-fill query + enqueue, `--force` predicate widening |
| `internal/ai/gapscanner/scanner_test.go` | tests |
| `internal/service/ai/service.go` | auth-scoped service (single principal in v1, owner enforcement everywhere) |
| `internal/service/ai/health.go` | aggregator for `GET /ai/health` |
| `internal/service/ai/*_test.go` | tests |
| `internal/httpapi/ai.go` | huma routes for `/ai/health`, `/ai/failures`, `/ai/backfill`, `/ai/retry-failed`, `/ai/retry-photo`, `/ai/acknowledge` |
| `internal/httpapi/ai_test.go` | tests |
| `internal/httpapi/events.go` | (modify) add `ai.tag.completed`, `ai.caption.completed`, `ai.health.changed` |
| `internal/cli/ai.go` | `fotobank ai status \| backfill \| retry-failed \| acknowledge` |
| `internal/cli/ai_test.go` | tests |
| `internal/ingest/...` | (modify) auto-enqueue jobs / record video-skips on import |
| `internal/cli/server.go` | (modify) wire workers, lease sweep, gap-scan tick |
| `internal/obs/metrics.go` | (modify) AI metric primitives |
| `internal/errs/errs.go` | (modify) `ErrAcknowledgementRequired` sentinel |
| `internal/httpapi/errors.go` | (modify) translate new sentinel → 409 |

Frontend (new):

| File | Responsibility |
|---|---|
| `frontend/src/lib/api/generated/schema.ts` | (modify, regenerated) AI route types |
| `frontend/src/lib/ai/client.ts` | typed wrappers around AI endpoints |
| `frontend/src/lib/ai/types.ts` | `AIHealth`, `Failure`, `Task` shapes |
| `frontend/src/lib/ai/health.ts` | reactive health store; SSE-driven refresh |
| `frontend/src/lib/components/AIStatusDot.svelte` | shell-strip dot |
| `frontend/src/lib/lightbox/components/LightboxAI.svelte` | tags + caption + provenance + retry |
| `frontend/src/lib/lightbox/components/LightboxAI.test.ts` | unit tests |
| `frontend/src/routes/SettingsAI.svelte` | AI panel (header, disclosure, task cards, failures, ack modal) |
| `frontend/src/routes/SettingsAI.test.ts` | unit tests |
| `frontend/src/lib/lightbox/components/LightboxMetadata.svelte` | (modify) mount `LightboxAI` |
| `frontend/src/lib/router/...` | (modify) register `/settings/ai` route |
| `frontend/src/lib/components/AppHeader.svelte` | (modify) mount `AIStatusDot` |
| `frontend/src/lib/events/...` | (modify) handle `ai.*` SSE events |
| `frontend/tests/e2e/ai.spec.ts` | E2E: ack gate, panel, lightbox, retry |
| `cmd/e2e-server/main.go` | (modify) seed AI fixtures + mock VLM endpoint |

---

## Conventions and warnings for the executor

- **Go:** `database/sql` (not sqlx). Owner is always `owners.Principal{Hub, UserID}`. Sentinels in `internal/errs/`. Wrap with `fmt.Errorf("doing X: %w", err)`. Use `require` (testify) in tests. New tables must add a `JOIN media` for owner checks — never denormalize owner columns into AI tables.
- **Schema:** edit `internal/db/migrations/000001_initial_schema.up.sql` in place. The matching `.down.sql` is a wholesale teardown — adding new tables is silent (the down file already drops the whole DB).
- **Tests:** `testutil.OpenTestDB(t)` spins a fresh migrated SQLite per test. Use `require.ErrorIs` for sentinel checks, never `==`.
- **Frontend:** Bun + Vite + Svelte 5. Tests via Vitest + `@testing-library/svelte`. E2E via Playwright with `cmd/e2e-server`. Don't use `{@html}` for AI text — render as text nodes only.
- **Commits:** conventional, ≤72 char subject, scoped (`feat(ai/queue):`, etc.), one logical change per commit. Don't bypass pre-commit hooks.
- **Pre-commit hooks:** running `go test (short)`, `golangci-lint --fix`, `gofmt`, `testify-helper-check`, and `make api-generate` whenever HTTP routes change. The `migration-history-check` hook has been removed (pre-alpha policy). If `make api-generate` fails on a route addition, that's a routing wiring bug; fix and recommit.

---

## Section A — Schema

### Task A1: Append AI tables to 000001 schema

**Files:**
- Modify: `internal/db/migrations/000001_initial_schema.up.sql`

- [ ] **Step 1: Append AI tables and indexes at the end of the up file**

Open `internal/db/migrations/000001_initial_schema.up.sql` and append the following block at the end of the file (after `auth_hidden_lockout`):

```sql
-- ============================================================
-- AI: tag and caption pipeline.
-- See docs/superpowers/specs/2026-04-30-fotobank-ai-tag-caption-design.md.
-- ============================================================

-- One row per task run (or in-flight insert that gets staled on retry).
CREATE TABLE ai_results (
    id              UUID PRIMARY KEY,
    media_id        UUID NOT NULL REFERENCES media(id) ON DELETE CASCADE,
    task            TEXT NOT NULL CHECK (task IN ('tag','caption')),
    model_id        TEXT NOT NULL,
    prompt_version  TEXT NOT NULL,
    prompt_hash     TEXT NOT NULL,
    input_profile   TEXT NOT NULL,
    status          TEXT NOT NULL CHECK (status IN ('active','stale')),
    generated_at    TIMESTAMP NOT NULL
);
CREATE UNIQUE INDEX ai_results_active_one_per
    ON ai_results(media_id, task) WHERE status = 'active';
CREATE INDEX ai_results_media_task_idx
    ON ai_results(media_id, task, status);

CREATE TABLE media_tags (
    result_id  UUID NOT NULL REFERENCES ai_results(id) ON DELETE CASCADE,
    tag_key    TEXT NOT NULL,
    tag_label  TEXT NOT NULL,
    rank       INTEGER NOT NULL,
    PRIMARY KEY (result_id, tag_key)
);
CREATE INDEX media_tags_key_idx ON media_tags(tag_key);

CREATE TABLE media_captions (
    result_id  UUID PRIMARY KEY REFERENCES ai_results(id) ON DELETE CASCADE,
    text       TEXT NOT NULL
);

CREATE TABLE ai_jobs (
    id              UUID PRIMARY KEY,
    media_id        UUID NOT NULL REFERENCES media(id) ON DELETE CASCADE,
    task            TEXT NOT NULL CHECK (task IN ('tag','caption')),
    fingerprint     TEXT NOT NULL,
    status          TEXT NOT NULL CHECK (
                      status IN ('pending','working','blocked','done','failed','superseded')),
    attempts        INTEGER NOT NULL DEFAULT 0,
    last_error      TEXT,
    last_error_kind TEXT,
    claimed_at      TIMESTAMP,
    enqueued_at     TIMESTAMP NOT NULL,
    completed_at    TIMESTAMP
);
CREATE UNIQUE INDEX ai_jobs_active_idx
    ON ai_jobs(media_id, task) WHERE status IN ('pending','working','blocked');
CREATE INDEX ai_jobs_pending_idx
    ON ai_jobs(task, status, claimed_at) WHERE status IN ('pending','working','blocked');
CREATE INDEX ai_jobs_terminal_idx
    ON ai_jobs(task, status, completed_at) WHERE status IN ('done','failed','superseded');

CREATE TABLE ai_failures (
    media_id        UUID NOT NULL REFERENCES media(id) ON DELETE CASCADE,
    task            TEXT NOT NULL CHECK (task IN ('tag','caption')),
    model_id        TEXT NOT NULL,
    prompt_version  TEXT NOT NULL,
    input_profile   TEXT NOT NULL,
    last_error      TEXT NOT NULL,
    last_error_kind TEXT NOT NULL,
    attempt_count   INTEGER NOT NULL,
    failed_at       TIMESTAMP NOT NULL,
    PRIMARY KEY (media_id, task, model_id, prompt_version, input_profile)
);
CREATE INDEX ai_failures_active_idx
    ON ai_failures(task, model_id, prompt_version, input_profile, failed_at DESC);

CREATE TABLE ai_skipped (
    media_id     UUID NOT NULL REFERENCES media(id) ON DELETE CASCADE,
    task         TEXT NOT NULL CHECK (task IN ('tag','caption')),
    reason       TEXT NOT NULL,
    recorded_at  TIMESTAMP NOT NULL,
    PRIMARY KEY (media_id, task)
);
```

- [ ] **Step 2: Verify the migrated DB applies cleanly**

```
go test ./internal/db/... -run TestMigrationsApplyClean -count=1 -v
```

Expected: PASS. If no such test exists, run any test in `internal/db` that triggers `testutil.OpenTestDB` — those open a fresh migrated DB and will surface schema errors.

```
go test ./internal/media/... -count=1 -run TestRepoInsert -v
```

Expected: PASS (existing test; passing means the new SQL didn't break the migration).

- [ ] **Step 3: Commit**

```
git add internal/db/migrations/000001_initial_schema.up.sql
git commit -m "feat(db): add AI tag/caption schema (results, jobs, failures, skipped)"
```

---

## Section B — AI shared types, config, prompts

### Task B1: AI domain types

**Files:**
- Create: `internal/ai/types.go`
- Create: `internal/ai/types_test.go`

- [ ] **Step 1: Write the failing test**

```go
// internal/ai/types_test.go
package ai_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/ai"
)

func TestFingerprintString(t *testing.T) {
	fp := ai.Fingerprint{
		ModelID:       "qwen2.5-vl:3b",
		PromptVersion: "tags-v1",
		InputProfile:  "jpeg-1024-q85-metadata-stripped-v1",
	}
	require.Equal(t,
		"qwen2.5-vl:3b|tags-v1|jpeg-1024-q85-metadata-stripped-v1",
		fp.String(),
	)
}

func TestTaskValid(t *testing.T) {
	require.True(t, ai.TaskTag.Valid())
	require.True(t, ai.TaskCaption.Valid())
	require.False(t, ai.Task("nonsense").Valid())
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/ai/...`
Expected: FAIL — package `ai` has no exported types yet.

- [ ] **Step 3: Implement**

```go
// internal/ai/types.go
// Package ai owns the shared types and configuration for fotobank's
// VLM-driven tagging and captioning. Subpackages implement the gateway,
// queue, results store, workers, and gap scanner.
package ai

import "fmt"

// Task discriminates between the two AI tasks fotobank runs in v1.
type Task string

const (
	TaskTag     Task = "tag"
	TaskCaption Task = "caption"
)

// Valid reports whether t is a known task value.
func (t Task) Valid() bool { return t == TaskTag || t == TaskCaption }

// Fingerprint is the (model, prompt, input) triple that identifies a
// specific way of producing AI output. Stored verbatim on every result
// row; gap scanner and panel queries filter by the currently-configured
// triple.
type Fingerprint struct {
	ModelID       string
	PromptVersion string
	InputProfile  string
}

// String returns the canonical "model|prompt|profile" form persisted in
// ai_jobs.fingerprint and surfaced in SSE events.
func (f Fingerprint) String() string {
	return fmt.Sprintf("%s|%s|%s", f.ModelID, f.PromptVersion, f.InputProfile)
}

// JobStatus enumerates the states an ai_jobs row may occupy.
type JobStatus string

const (
	JobPending    JobStatus = "pending"
	JobWorking    JobStatus = "working"
	JobBlocked    JobStatus = "blocked"
	JobDone       JobStatus = "done"
	JobFailed     JobStatus = "failed"
	JobSuperseded JobStatus = "superseded"
)

// Terminal reports whether s is a no-further-work state.
func (s JobStatus) Terminal() bool {
	return s == JobDone || s == JobFailed || s == JobSuperseded
}

// LastErrorKind classifies why a job execution failed. Used both to
// drive in-call vs job-level retry decisions and to populate
// ai_failures.last_error_kind for the panel.
type LastErrorKind string

const (
	ErrKindTransient       LastErrorKind = "transient"
	ErrKindProvider4xx     LastErrorKind = "provider_4xx"
	ErrKindMalformed       LastErrorKind = "malformed"
	ErrKindThumbBlocked    LastErrorKind = "thumb_blocked"
	ErrKindMissingAIInput  LastErrorKind = "missing_ai_input"
	ErrKindSuperseded      LastErrorKind = "superseded"
)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/ai/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```
git add internal/ai/types.go internal/ai/types_test.go
git commit -m "feat(ai): add Task, Fingerprint, JobStatus, LastErrorKind"
```

---

### Task B2: AI configuration types

**Files:**
- Create: `internal/ai/config.go`
- Create: `internal/ai/config_test.go`

- [ ] **Step 1: Write the failing test**

```go
// internal/ai/config_test.go
package ai_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/ai"
)

func TestConfigDefaults(t *testing.T) {
	c := ai.Config{
		Vision: ai.VisionConfig{Endpoint: "http://127.0.0.1:11434/v1"},
		Tag:    ai.TaskConfig{Enabled: true, Model: "qwen2.5-vl:3b"},
		Caption: ai.TaskConfig{Enabled: true, Model: "llama3.2-vision:11b"},
	}
	c.ApplyDefaults()
	require.Equal(t, 30*time.Second, time.Duration(0)+c.Vision.timeoutOrDefault()) //nolint:unused
	_ = c
}

func TestConfigValidate_disabledNoEndpoint(t *testing.T) {
	c := ai.Config{Enabled: false}
	c.ApplyDefaults()
	require.NoError(t, c.Validate())
}

func TestConfigValidate_enabledRequiresEndpoint(t *testing.T) {
	c := ai.Config{Enabled: true}
	c.ApplyDefaults()
	err := c.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "endpoint")
}

func TestConfigValidate_maxInflightAtLeastOne(t *testing.T) {
	c := ai.Config{
		Enabled: true,
		Vision: ai.VisionConfig{Endpoint: "http://x/v1", MaxInflight: 0},
		Tag:    ai.TaskConfig{Enabled: true, Model: "m"},
	}
	c.ApplyDefaults()
	require.GreaterOrEqual(t, c.Vision.MaxInflight, 1)
}

func TestConfigValidate_workerConcurrencyMustBePositive(t *testing.T) {
	c := ai.Config{
		Enabled: true,
		Vision:  ai.VisionConfig{Endpoint: "http://x/v1", MaxInflight: 1},
		Tag:     ai.TaskConfig{Enabled: true, Model: "m", WorkerConcurrency: 0},
	}
	c.ApplyDefaults()
	require.GreaterOrEqual(t, c.Tag.WorkerConcurrency, 1)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/ai/...`
Expected: FAIL — types/methods do not exist.

- [ ] **Step 3: Implement**

```go
// internal/ai/config.go
package ai

import (
	"fmt"
	"net/url"
	"os"
	"time"
)

// Config is the [ai] TOML block plus its sub-blocks.
type Config struct {
	Enabled bool          `toml:"enabled"`
	Vision  VisionConfig  `toml:"vision"`
	Tag     TaskConfig    `toml:"tag"`
	Caption TaskConfig    `toml:"caption"`
}

// VisionConfig is the shared OpenAI-compatible chat-completions endpoint.
type VisionConfig struct {
	Endpoint    string        `toml:"endpoint"`
	APIKeyEnv   string        `toml:"api_key_env"`
	Timeout     time.Duration `toml:"timeout"`
	MaxRetries  int           `toml:"max_retries"`
	MaxInflight int           `toml:"max_inflight"`
}

// APIKey resolves the bearer token from the env var named in APIKeyEnv.
// Returns "" when APIKeyEnv is empty or the variable is unset.
func (v VisionConfig) APIKey() string {
	if v.APIKeyEnv == "" {
		return ""
	}
	return os.Getenv(v.APIKeyEnv)
}

// timeoutOrDefault returns the per-request HTTP timeout, defaulting to 2m.
func (v VisionConfig) timeoutOrDefault() time.Duration {
	if v.Timeout <= 0 {
		return 2 * time.Minute
	}
	return v.Timeout
}

// TaskConfig configures a single AI task (tag or caption).
type TaskConfig struct {
	Enabled           bool   `toml:"enabled"`
	Model             string `toml:"model"`
	WorkerConcurrency int    `toml:"worker_concurrency"`
}

// ApplyDefaults fills sensible defaults so a minimal [ai] block works.
func (c *Config) ApplyDefaults() {
	if c.Vision.Timeout == 0 {
		c.Vision.Timeout = 2 * time.Minute
	}
	if c.Vision.MaxRetries == 0 {
		c.Vision.MaxRetries = 2
	}
	if c.Vision.MaxInflight < 1 {
		c.Vision.MaxInflight = 1
	}
	if c.Tag.WorkerConcurrency < 1 {
		c.Tag.WorkerConcurrency = 1
	}
	if c.Caption.WorkerConcurrency < 1 {
		c.Caption.WorkerConcurrency = 1
	}
}

// Validate is invoked at boot. Disabled configs are not checked.
func (c *Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.Vision.Endpoint == "" {
		return fmt.Errorf("ai.vision.endpoint: required when ai.enabled=true")
	}
	u, err := url.Parse(c.Vision.Endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("ai.vision.endpoint: must be http(s) URL with host (got %q)", c.Vision.Endpoint)
	}
	if c.Tag.Enabled && c.Tag.Model == "" {
		return fmt.Errorf("ai.tag.model: required when ai.tag.enabled=true")
	}
	if c.Caption.Enabled && c.Caption.Model == "" {
		return fmt.Errorf("ai.caption.model: required when ai.caption.enabled=true")
	}
	return nil
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/ai/...`
Expected: PASS.

- [ ] **Step 5: Wire into top-level config**

Modify `internal/config/config.go` — add `AI ai.Config` to the `Config` struct and call `cfg.AI.ApplyDefaults()` in the loader. Search for the existing pattern (e.g. how `Backup` or `Observability` is wired) and follow it. After wiring:

```
go build ./...
go test ./internal/config/... -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```
git add internal/ai/config.go internal/ai/config_test.go internal/config/config.go
git commit -m "feat(ai): add config types with validation and defaults"
```


---

### Task B3: Prompts with hash-pinned versioning

**Files:**
- Create: `internal/ai/prompts/prompts.go`
- Create: `internal/ai/prompts/prompts_test.go`

- [ ] **Step 1: Write the failing test**

```go
// internal/ai/prompts/prompts_test.go
package prompts_test

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/ai/prompts"
)

// pinned hashes — bump these when you bump the version string.
var pinned = map[string]string{
	"tags-v1":    "",
	"caption-v1": "",
}

func TestPromptHashesArePinned(t *testing.T) {
	tagP := prompts.Tag()
	capP := prompts.Caption()

	require.Equal(t, "tags-v1", tagP.Version)
	require.Equal(t, "caption-v1", capP.Version)
	require.NotEmpty(t, tagP.Text)
	require.NotEmpty(t, capP.Text)
	require.Equal(t, hashHex(tagP.Text), tagP.Hash)
	require.Equal(t, hashHex(capP.Text), capP.Hash)

	if got, want := tagP.Hash, pinned[tagP.Version]; want != "" && got != want {
		t.Fatalf("tag prompt drifted from pinned hash: got %q want %q (bump version)", got, want)
	}
	if got, want := capP.Hash, pinned[capP.Version]; want != "" && got != want {
		t.Fatalf("caption prompt drifted from pinned hash: got %q want %q (bump version)", got, want)
	}
}

func hashHex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/ai/prompts/...`
Expected: FAIL — package missing.

- [ ] **Step 3: Implement**

```go
// internal/ai/prompts/prompts.go
// Package prompts owns the LLM prompts used by fotobank's AI workers.
// Each prompt is a Go const; bumping a prompt requires bumping the
// version string. The CI test pins the SHA-256 of every const so an
// edit without a version bump fails the build.
package prompts

import (
	"crypto/sha256"
	"encoding/hex"
)

// Prompt bundles a prompt string with its stable version identifier and
// content hash. Workers persist Version on every ai_results row;
// ai_results.prompt_hash is filled from Hash so accidental drift is
// detectable post-hoc as well as in CI.
type Prompt struct {
	Version string
	Text    string
	Hash    string
}

// tagV1 asks the VLM for a JSON array of free-text tags.
const tagV1 = `Look at this photograph and emit a JSON object describing what is visible.

Output strictly valid JSON in this shape (no markdown, no commentary):
{"tags": ["...", "..."]}

Rules:
- Up to 10 tags, ordered most salient first.
- Free-text English; no scores; no sentences.
- Describe subjects, scenes, settings, mood, weather, time of day.
- Do NOT name specific people or invent place names.
- Do NOT include camera/lens metadata.
- If the photo is empty or you cannot describe it, return {"tags": []}.`

// captionV1 asks for a 1–3 sentence neutral description.
const captionV1 = `Look at this photograph and emit a JSON object with a brief plain-text description.

Output strictly valid JSON in this shape (no markdown, no commentary):
{"caption": "..."}

Rules:
- 1 to 3 sentences, at most 300 characters.
- English; descriptive third-person ("A small dog stands on a beach…").
- No first-person ("I see").
- No markdown, no headings, no bullets, no links, no code.
- Do NOT name specific people or invent place names.
- If the photo is empty or you cannot describe it, return {"caption": ""}.`

// Tag returns the active tag prompt.
func Tag() Prompt { return mk("tags-v1", tagV1) }

// Caption returns the active caption prompt.
func Caption() Prompt { return mk("caption-v1", captionV1) }

func mk(version, text string) Prompt {
	sum := sha256.Sum256([]byte(text))
	return Prompt{Version: version, Text: text, Hash: hex.EncodeToString(sum[:])}
}
```

- [ ] **Step 4: Run the test, copy the printed hashes into the pinned map**

Run: `go test ./internal/ai/prompts/...`
Expected: PASS (the `if want != ""` guard is satisfied because the map values are empty).

Now copy the actual hashes into `pinned` to lock them in. Run:

```
go test ./internal/ai/prompts/... -run TestPromptHashesArePinned -v
```

The failure path (drift detection) is exercised by editing the const in a follow-up commit; covered in CI.

- [ ] **Step 5: Pin hashes into the test**

Compute and paste the hashes into `pinned`. After `prompts.go` is committed run:

```
go run -mod=mod ./internal/ai/prompts/cmd/printhashes 2>/dev/null || \
go test -run TestPromptHashesArePinned -v ./internal/ai/prompts/...
```

(Alternative: temporarily modify the test to print `tagP.Hash` and `capP.Hash`, run, paste into `pinned`, revert the print.)

- [ ] **Step 6: Commit**

```
git add internal/ai/prompts/
git commit -m "feat(ai/prompts): pin tag and caption v1 prompts with hash CI check"
```

---

## Section C — Vision gateway (HTTP client)

### Task C1: VisionGateway interface and types

**Files:**
- Create: `internal/ai/gateway/gateway.go`

- [ ] **Step 1: Write the package**

```go
// internal/ai/gateway/gateway.go
// Package gateway defines fotobank's interface to vision-language
// models. v1 ships a single OpenAI-compatible chat-completions impl;
// the interface is provider-neutral so future native adapters can
// plug in without changing worker code.
package gateway

import (
	"context"
	"errors"
)

// Request is one VLM call: a prompt plus the JPEG bytes to attach as
// the user's image content part.
type Request struct {
	Model    string
	Prompt   string
	JPEG     []byte
	MaxTokens int // 0 = provider default
}

// Response is the raw assistant message the VLM returned. The caller
// (see internal/ai/parse) is responsible for defensive JSON extraction.
type Response struct {
	Text string
}

// VisionGateway is the minimum contract a vision provider must implement.
type VisionGateway interface {
	// Generate calls the model and returns the raw assistant message text.
	// Implementations honor ctx for cancellation.
	Generate(ctx context.Context, req Request) (Response, error)
	// HealthCheck performs a low-cost reachability probe. Should not
	// invoke the model itself.
	HealthCheck(ctx context.Context) error
}

// ErrPermanent4xx wraps non-retryable HTTP 4xx responses (other than
// 429). Callers use errors.Is to detect; the error message still
// carries the status code and a bounded response body.
var ErrPermanent4xx = errors.New("gateway: non-retryable 4xx")

// ErrTransient is a transient failure (5xx, 429-after-retries, network).
// Wraps the underlying cause for diagnostic purposes; classification at
// the worker level uses errors.Is.
var ErrTransient = errors.New("gateway: transient failure")
```

- [ ] **Step 2: Build (no tests yet — interface only)**

Run: `go build ./internal/ai/gateway/...`
Expected: PASS.

- [ ] **Step 3: Commit**

```
git add internal/ai/gateway/gateway.go
git commit -m "feat(ai/gateway): introduce VisionGateway interface"
```

---

### Task C2: OpenAI-compatible client with retry policy

**Files:**
- Create: `internal/ai/gateway/openai.go`
- Create: `internal/ai/gateway/openai_test.go`

- [ ] **Step 1: Write the failing test**

```go
// internal/ai/gateway/openai_test.go
package gateway_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/ai/gateway"
)

func TestOpenAI_GenerateSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/chat/completions", r.URL.Path)
		require.Equal(t, "Bearer secret", r.Header.Get("Authorization"))
		var body struct {
			Model    string                   `json:"model"`
			Messages []map[string]interface{} `json:"messages"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, "qwen2.5-vl:3b", body.Model)
		require.Len(t, body.Messages, 1)

		resp := map[string]interface{}{
			"choices": []map[string]interface{}{{
				"message": map[string]string{"role": "assistant", "content": `{"tags":["dog","beach"]}`},
			}},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := gateway.NewOpenAICompatible(gateway.OpenAIConfig{
		Endpoint:   srv.URL + "/v1",
		APIKey:     "secret",
		Timeout:    2 * time.Second,
		MaxRetries: 1,
	})
	got, err := c.Generate(context.Background(), gateway.Request{
		Model:  "qwen2.5-vl:3b",
		Prompt: "describe the photo",
		JPEG:   []byte{0xff, 0xd8, 0xff, 0xd9},
	})
	require.NoError(t, err)
	require.Equal(t, `{"tags":["dog","beach"]}`, got.Text)
}

func TestOpenAI_RetriesOn5xxThenSucceeds(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) == 1 {
			http.Error(w, "boom", http.StatusBadGateway)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{{
				"message": map[string]string{"role": "assistant", "content": "ok"},
			}},
		})
	}))
	defer srv.Close()

	c := gateway.NewOpenAICompatible(gateway.OpenAIConfig{
		Endpoint:   srv.URL + "/v1",
		Timeout:    2 * time.Second,
		MaxRetries: 2,
		BackoffBase: 1 * time.Millisecond, // fast for tests
		BackoffCap:  5 * time.Millisecond,
	})
	got, err := c.Generate(context.Background(), gateway.Request{Model: "m", Prompt: "p", JPEG: []byte{0xff}})
	require.NoError(t, err)
	require.Equal(t, "ok", got.Text)
	require.EqualValues(t, 2, attempts.Load())
}

func TestOpenAI_4xxFailsImmediately(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		http.Error(w, `{"error":"image too large"}`, http.StatusBadRequest)
	}))
	defer srv.Close()

	c := gateway.NewOpenAICompatible(gateway.OpenAIConfig{
		Endpoint: srv.URL + "/v1", Timeout: 2 * time.Second, MaxRetries: 3,
	})
	_, err := c.Generate(context.Background(), gateway.Request{Model: "m", Prompt: "p", JPEG: []byte{0xff}})
	require.Error(t, err)
	require.True(t, errors.Is(err, gateway.ErrPermanent4xx), "want ErrPermanent4xx, got %v", err)
	require.EqualValues(t, 1, attempts.Load(), "no retries on 4xx")
}

func TestOpenAI_429HonorsRetryAfter(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			http.Error(w, "slow down", http.StatusTooManyRequests)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{{"message": map[string]string{"role": "assistant", "content": "ok"}}},
		})
	}))
	defer srv.Close()

	c := gateway.NewOpenAICompatible(gateway.OpenAIConfig{
		Endpoint: srv.URL + "/v1", Timeout: 2 * time.Second, MaxRetries: 2,
		BackoffBase: 1 * time.Millisecond, BackoffCap: 5 * time.Millisecond,
	})
	got, err := c.Generate(context.Background(), gateway.Request{Model: "m", Prompt: "p", JPEG: []byte{0xff}})
	require.NoError(t, err)
	require.Equal(t, "ok", got.Text)
	require.EqualValues(t, 2, attempts.Load())
}

func TestOpenAI_HealthCheck(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/models", r.URL.Path)
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = w.Write([]byte(`{"data":[{"id":"qwen2.5-vl:3b"}]}`))
	}))
	defer srv.Close()

	c := gateway.NewOpenAICompatible(gateway.OpenAIConfig{Endpoint: srv.URL + "/v1", Timeout: 2 * time.Second})
	require.NoError(t, c.HealthCheck(context.Background()))
}

// satisfy strconv import even when build tags filter
var _ = strconv.Itoa
var _ = strings.NewReader
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/ai/gateway/...`
Expected: FAIL — `OpenAICompatible` not defined.

- [ ] **Step 3: Implement**

```go
// internal/ai/gateway/openai.go
package gateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// OpenAIConfig configures the OpenAI-compatible chat-completions client.
type OpenAIConfig struct {
	Endpoint    string        // base URL ending in /v1; "/chat/completions" appended
	APIKey      string        // optional bearer token
	Timeout     time.Duration // per-request HTTP timeout (default 30s)
	MaxRetries  int           // total HTTP attempts (default 2)
	BackoffBase time.Duration // exponential base (default 5s)
	BackoffCap  time.Duration // max backoff (default 60s)
}

// OpenAICompatible is the v1 vision gateway implementation.
type OpenAICompatible struct {
	cfg  OpenAIConfig
	http *http.Client
	rng  *rand.Rand
}

// NewOpenAICompatible applies defaults and constructs a client.
func NewOpenAICompatible(cfg OpenAIConfig) *OpenAICompatible {
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = 2
	}
	if cfg.BackoffBase == 0 {
		cfg.BackoffBase = 5 * time.Second
	}
	if cfg.BackoffCap == 0 {
		cfg.BackoffCap = 60 * time.Second
	}
	return &OpenAICompatible{
		cfg:  cfg,
		http: &http.Client{Timeout: cfg.Timeout},
		rng:  rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), 0xdeadbeef)),
	}
}

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	MaxTokens int          `json:"max_tokens,omitempty"`
}

type chatMessage struct {
	Role    string         `json:"role"`
	Content []contentPart  `json:"content"`
}

type contentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *imageURL `json:"image_url,omitempty"`
}

type imageURL struct {
	URL string `json:"url"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

// Generate sends a single chat-completion request and returns the
// assistant's raw text. Transient errors are retried per cfg.MaxRetries.
func (c *OpenAICompatible) Generate(ctx context.Context, req Request) (Response, error) {
	dataURL := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(req.JPEG)
	body := chatRequest{
		Model: req.Model,
		Messages: []chatMessage{{
			Role: "user",
			Content: []contentPart{
				{Type: "text", Text: req.Prompt},
				{Type: "image_url", ImageURL: &imageURL{URL: dataURL}},
			},
		}},
		MaxTokens: req.MaxTokens,
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return Response{}, fmt.Errorf("marshal: %w", err)
	}

	var lastErr error
	for attempt := 1; attempt <= c.cfg.MaxRetries; attempt++ {
		text, retryAfter, retryAfterSet, err := c.doOnce(ctx, payload)
		if err == nil {
			return Response{Text: text}, nil
		}
		// Permanent: bail.
		if errors.Is(err, ErrPermanent4xx) {
			return Response{}, err
		}
		lastErr = err
		if attempt == c.cfg.MaxRetries {
			break
		}
		// Transient: backoff and retry.
		backoff := c.computeBackoff(attempt, retryAfter, retryAfterSet)
		if backoff > 0 {
			select {
			case <-ctx.Done():
				return Response{}, fmt.Errorf("gateway: ctx canceled during backoff: %w", ctx.Err())
			case <-time.After(backoff):
			}
		}
	}
	return Response{}, fmt.Errorf("gateway: giving up after %d attempts: %w", c.cfg.MaxRetries, lastErr)
}

func (c *OpenAICompatible) doOnce(ctx context.Context, payload []byte) (string, time.Duration, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.Endpoint+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return "", 0, false, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", 0, false, fmt.Errorf("%w: http do: %v", ErrTransient, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusTooManyRequests {
		ra, ok := parseRetryAfter(resp.Header.Get("Retry-After"))
		return "", ra, ok, fmt.Errorf("%w: HTTP 429", ErrTransient)
	}
	if resp.StatusCode >= 500 {
		return "", 0, false, fmt.Errorf("%w: HTTP %d", ErrTransient, resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			return "", 0, false, fmt.Errorf("HTTP %d: %w", resp.StatusCode, ErrPermanent4xx)
		}
		return "", 0, false, fmt.Errorf("HTTP %d: %s: %w", resp.StatusCode, msg, ErrPermanent4xx)
	}

	var r chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return "", 0, false, fmt.Errorf("%w: decode: %v", ErrTransient, err)
	}
	if len(r.Choices) == 0 {
		return "", 0, false, fmt.Errorf("%w: no choices in response", ErrTransient)
	}
	return r.Choices[0].Message.Content, 0, false, nil
}

func (c *OpenAICompatible) computeBackoff(attempt int, retryAfter time.Duration, retryAfterSet bool) time.Duration {
	if retryAfterSet {
		return retryAfter
	}
	// Exponential with jitter.
	exp := c.cfg.BackoffBase << min(attempt-1, 8)
	if exp > c.cfg.BackoffCap {
		exp = c.cfg.BackoffCap
	}
	if exp <= 0 {
		return 0
	}
	jitter := time.Duration(c.rng.Int64N(int64(exp / 4)))
	return exp + jitter
}

// HealthCheck pings /v1/models. Provider-agnostic: every OpenAI-compatible
// server (Ollama, vLLM, llama.cpp, OpenAI itself) implements GET /v1/models.
func (c *OpenAICompatible) HealthCheck(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.Endpoint+"/models", nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if c.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("health: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("health: HTTP %d", resp.StatusCode)
	}
	return nil
}

func parseRetryAfter(v string) (time.Duration, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	const maxWait = time.Hour
	if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
		d := time.Duration(secs) * time.Second
		if d > maxWait {
			return maxWait, true
		}
		return d, true
	}
	if t, err := http.ParseTime(v); err == nil {
		d := time.Until(t)
		if d <= 0 {
			return 0, true
		}
		if d > maxWait {
			return maxWait, true
		}
		return d, true
	}
	return 0, false
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/ai/gateway/... -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```
git add internal/ai/gateway/openai.go internal/ai/gateway/openai_test.go
git commit -m "feat(ai/gateway): OpenAI-compatible client with retry, jitter, 429-aware"
```


---

## Section D — Image input pipeline & defensive parsing

### Task D1: Re-encode preview tier to AI input profile

**Files:**
- Create: `internal/ai/imginput/encode.go`
- Create: `internal/ai/imginput/encode_test.go`

The thumb pipeline already produces a `preview` (2560 px JPEG) blob in flash storage. We re-encode that to a 1024-edge q85 JPEG with metadata stripped. Reference `internal/thumb/encode.go` for existing decoder/encoder helpers.

- [ ] **Step 1: Write the failing test**

```go
// internal/ai/imginput/encode_test.go
package imginput_test

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/ai/imginput"
)

func TestEncodeFromPreview_DownscalesToProfileEdge(t *testing.T) {
	src := makeJPEG(t, 3000, 2000)
	out, err := imginput.Encode(src)
	require.NoError(t, err)
	require.NotEmpty(t, out)

	cfg, _, err := image.DecodeConfig(bytes.NewReader(out))
	require.NoError(t, err)
	require.LessOrEqual(t, cfg.Width, 1024)
	require.LessOrEqual(t, cfg.Height, 1024)
	// Aspect ratio: original 3:2 → max edge 1024 → 1024 x 682-683.
	if cfg.Width >= cfg.Height {
		require.Equal(t, 1024, cfg.Width)
	} else {
		require.Equal(t, 1024, cfg.Height)
	}
}

func TestEncodeFromPreview_Idempotent(t *testing.T) {
	// Already-small input stays at native dimensions, never upscaled.
	src := makeJPEG(t, 800, 600)
	out, err := imginput.Encode(src)
	require.NoError(t, err)
	cfg, _, err := image.DecodeConfig(bytes.NewReader(out))
	require.NoError(t, err)
	require.LessOrEqual(t, cfg.Width, 800)
	require.LessOrEqual(t, cfg.Height, 600)
}

func TestProfileString(t *testing.T) {
	require.Equal(t, "jpeg-1024-q85-metadata-stripped-v1", imginput.ProfileV1)
}

func makeJPEG(t *testing.T, w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 0x40, A: 0xff})
		}
	}
	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}))
	return buf.Bytes()
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/ai/imginput/...`
Expected: FAIL — package missing.

- [ ] **Step 3: Implement**

```go
// internal/ai/imginput/encode.go
// Package imginput re-encodes existing preview-tier JPEGs into the
// fixed AI input profile: max edge 1024 px, JPEG q=85, EXIF/ICC stripped.
// We never look at original files; the preview is already a sanitized
// downscale produced by the thumb pipeline.
package imginput

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"

	"golang.org/x/image/draw"
)

// ProfileV1 is the canonical name persisted on every ai_results row.
// Bumping requires a new constant and re-running the gap scanner.
const ProfileV1 = "jpeg-1024-q85-metadata-stripped-v1"

// MaxEdge and Quality match ProfileV1.
const (
	MaxEdge = 1024
	Quality = 85
)

// Encode takes preview-tier JPEG bytes (e.g. the 2560-edge thumb tier)
// and returns the AI-input JPEG. The encoder writes only the JPEG
// scan + Huffman/quant tables — no APP markers — so EXIF, ICC, and
// XMP from the source are dropped. Vectors are always JPEG; callers
// pass JPEG bytes from the existing preview store.
func Encode(srcJPEG []byte) ([]byte, error) {
	src, err := jpeg.Decode(bytes.NewReader(srcJPEG))
	if err != nil {
		return nil, fmt.Errorf("decode preview jpeg: %w", err)
	}
	dst := scaleToProfile(src)
	var out bytes.Buffer
	if err := jpeg.Encode(&out, dst, &jpeg.Options{Quality: Quality}); err != nil {
		return nil, fmt.Errorf("encode profile jpeg: %w", err)
	}
	return out.Bytes(), nil
}

// scaleToProfile downscales src so its longest edge is at most MaxEdge.
// Already-small images are returned untouched (no upscale).
func scaleToProfile(src image.Image) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= MaxEdge && h <= MaxEdge {
		return src
	}
	var nw, nh int
	if w >= h {
		nw = MaxEdge
		nh = int(float64(h) * float64(MaxEdge) / float64(w))
	} else {
		nh = MaxEdge
		nw = int(float64(w) * float64(MaxEdge) / float64(h))
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Over, nil)
	return dst
}
```

- [ ] **Step 4: Add `golang.org/x/image` if not already present**

```
go mod tidy
go test ./internal/ai/imginput/... -count=1
```

Expected: PASS. If `go.sum` changes, include in the commit.

- [ ] **Step 5: Commit**

```
git add internal/ai/imginput/ go.mod go.sum
git commit -m "feat(ai/imginput): encode preview to jpeg-1024-q85 profile"
```

---

### Task D2: Tag JSON parsing + normalization

**Files:**
- Create: `internal/ai/parse/tags.go`
- Create: `internal/ai/parse/tags_test.go`

- [ ] **Step 1: Write the failing test**

```go
// internal/ai/parse/tags_test.go
package parse_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/ai/parse"
)

func TestParseTags_Happy(t *testing.T) {
	out, err := parse.Tags(`{"tags":["Dog","Beach","Sunset"]}`)
	require.NoError(t, err)
	require.Len(t, out, 3)
	require.Equal(t, "dog", out[0].Key)
	require.Equal(t, "Dog", out[0].Label)
	require.Equal(t, 1, out[0].Rank)
	require.Equal(t, "sunset", out[2].Key)
	require.Equal(t, 3, out[2].Rank)
}

func TestParseTags_PreservesInternalPunctuation(t *testing.T) {
	out, err := parse.Tags(`{"tags":["Black-and-White","T-Shirt","O'clock"]}`)
	require.NoError(t, err)
	keys := []string{out[0].Key, out[1].Key, out[2].Key}
	require.Equal(t, []string{"black-and-white", "t-shirt", "o'clock"}, keys)
}

func TestParseTags_StripsLeadingTrailingPunct(t *testing.T) {
	out, err := parse.Tags(`{"tags":["  ! Beach .. "]}`)
	require.NoError(t, err)
	require.Equal(t, "beach", out[0].Key)
}

func TestParseTags_DedupesPostNormalize(t *testing.T) {
	out, err := parse.Tags(`{"tags":["Dog","DOG","dog"]}`)
	require.NoError(t, err)
	require.Len(t, out, 1)
	require.Equal(t, "dog", out[0].Key)
	require.Equal(t, "Dog", out[0].Label) // first-seen label wins
	require.Equal(t, 1, out[0].Rank)
}

func TestParseTags_DropsEmptyAfterNorm(t *testing.T) {
	out, err := parse.Tags(`{"tags":["Beach", "   ", "!!!", "Sunset"]}`)
	require.NoError(t, err)
	require.Len(t, out, 2)
	require.Equal(t, "beach", out[0].Key)
	require.Equal(t, "sunset", out[1].Key)
}

func TestParseTags_CapsAtTen(t *testing.T) {
	in := `{"tags":["t1","t2","t3","t4","t5","t6","t7","t8","t9","t10","t11","t12"]}`
	out, err := parse.Tags(in)
	require.NoError(t, err)
	require.Len(t, out, 10)
}

func TestParseTags_MalformedJSON(t *testing.T) {
	_, err := parse.Tags(`not json`)
	require.True(t, errors.Is(err, parse.ErrMalformed))
}

func TestParseTags_StripsCodefencedJSON(t *testing.T) {
	// Some VLMs wrap JSON in ```json fences despite the prompt; tolerate.
	out, err := parse.Tags("```json\n{\"tags\":[\"Dog\"]}\n```")
	require.NoError(t, err)
	require.Equal(t, "dog", out[0].Key)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/ai/parse/...`
Expected: FAIL — package missing.

- [ ] **Step 3: Implement**

```go
// internal/ai/parse/tags.go
// Package parse holds defensive parsers for VLM responses. Workers
// must not assume structured-output mode; every field is parsed
// permissively and validated post-hoc.
package parse

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// ErrMalformed is returned when the VLM response can't be parsed into
// the expected JSON shape, even after permissive cleanup. Workers
// classify this as LastErrorKind=malformed.
var ErrMalformed = errors.New("parse: malformed VLM output")

// Tag is one normalized tag from a VLM response.
type Tag struct {
	Key   string
	Label string
	Rank  int
}

// MaxTags caps how many tags are persisted per result.
const MaxTags = 10

type tagsEnvelope struct {
	Tags []string `json:"tags"`
}

var fencedJSON = regexp.MustCompile("(?s)^\\s*```(?:json)?\\s*(.*?)\\s*```\\s*$")

// Tags extracts the tag list from a VLM response and applies the
// normalization pipeline (NFKC → lowercase → strip leading/trailing
// punct → drop empties → dedupe by Key, first-seen wins → cap at MaxTags).
func Tags(raw string) ([]Tag, error) {
	body := strings.TrimSpace(raw)
	if m := fencedJSON.FindStringSubmatch(body); m != nil {
		body = m[1]
	}
	var env tagsEnvelope
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	out := make([]Tag, 0, MaxTags)
	seen := map[string]int{} // key -> index in out
	for _, raw := range env.Tags {
		key := normalizeKey(raw)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		out = append(out, Tag{Key: key, Label: raw, Rank: len(out) + 1})
		seen[key] = len(out) - 1
		if len(out) == MaxTags {
			break
		}
	}
	return out, nil
}

// normalizeKey applies the spec's tag normalization pipeline:
//  1. NFKC.
//  2. Trim leading/trailing whitespace.
//  3. Lowercase.
//  4. Collapse internal whitespace runs to single space.
//  5. Strip leading/trailing punctuation (non-alphanumeric, non-hyphen, non-apostrophe).
//  6. Remove emoji and control chars.
//  7. Reject empty post-normalization.
//
// Internal punctuation (hyphens, apostrophes, etc.) is preserved.
func normalizeKey(s string) string {
	s = norm.NFKC.String(s)
	s = strings.TrimSpace(s)
	s = strings.ToLower(s)

	// Build with a single pass: collapse whitespace, drop control chars
	// and non-emoji symbols, allow letters/digits/hyphen/apostrophe/space.
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := false
	for _, r := range s {
		switch {
		case unicode.IsControl(r):
			continue
		case unicode.Is(unicode.S, r): // symbols, including emoji
			continue
		case unicode.IsSpace(r):
			if !prevSpace && b.Len() > 0 {
				b.WriteRune(' ')
			}
			prevSpace = true
		default:
			b.WriteRune(r)
			prevSpace = false
		}
	}
	out := b.String()

	// Strip leading/trailing punctuation but preserve internal hyphens/apostrophes.
	out = strings.TrimFunc(out, func(r rune) bool {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return false
		}
		return true
	})
	return out
}
```

- [ ] **Step 4: Run tests**

```
go mod tidy
go test ./internal/ai/parse/... -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```
git add internal/ai/parse/tags.go internal/ai/parse/tags_test.go go.mod go.sum
git commit -m "feat(ai/parse): defensive tag JSON parser with normalization"
```

---

### Task D3: Caption parsing + validation

**Files:**
- Create: `internal/ai/parse/caption.go`
- Create: `internal/ai/parse/caption_test.go`

- [ ] **Step 1: Write the failing test**

```go
// internal/ai/parse/caption_test.go
package parse_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/ai/parse"
)

func TestParseCaption_Happy(t *testing.T) {
	out, err := parse.Caption(`{"caption":"A small dog stands on the beach at sunset."}`)
	require.NoError(t, err)
	require.Equal(t, "A small dog stands on the beach at sunset.", out)
}

func TestParseCaption_RejectsMarkdown(t *testing.T) {
	cases := []string{
		`{"caption":"A **dog** on a beach."}`,           // emphasis
		`{"caption":"# Heading\nA dog on a beach."}`,    // heading
		`{"caption":"[link](http://x)"}`,                // link
		"{\"caption\":\"```code```\"}",                  // code fence
		`{"caption":"- item one"}`,                      // list
		`{"caption":"> blockquote"}`,                    // blockquote
		`{"caption":"|a|b|c|"}`,                         // table
		"{\"caption\":\"with `inline` code\"}",          // inline backtick
	}
	for _, in := range cases {
		_, err := parse.Caption(in)
		require.True(t, errors.Is(err, parse.ErrMalformed), "want ErrMalformed for %q, got %v", in, err)
	}
}

func TestParseCaption_AllowsBareApostrophe(t *testing.T) {
	out, err := parse.Caption(`{"caption":"It's three o'clock."}`)
	require.NoError(t, err)
	require.Equal(t, "It's three o'clock.", out)
}

func TestParseCaption_RejectsEmpty(t *testing.T) {
	_, err := parse.Caption(`{"caption":"   "}`)
	require.True(t, errors.Is(err, parse.ErrMalformed))
}

func TestParseCaption_RejectsTooLong(t *testing.T) {
	long := strings.Repeat("a", 301)
	_, err := parse.Caption(`{"caption":"` + long + `"}`)
	require.True(t, errors.Is(err, parse.ErrMalformed))
}

func TestParseCaption_TrimsWhitespace(t *testing.T) {
	out, err := parse.Caption(`{"caption":"   A dog on a beach.   "}`)
	require.NoError(t, err)
	require.Equal(t, "A dog on a beach.", out)
}

func TestParseCaption_StripsCodefencedJSON(t *testing.T) {
	out, err := parse.Caption("```json\n{\"caption\":\"A dog on a beach.\"}\n```")
	require.NoError(t, err)
	require.Equal(t, "A dog on a beach.", out)
}

func TestParseCaption_MalformedJSON(t *testing.T) {
	_, err := parse.Caption("not json")
	require.True(t, errors.Is(err, parse.ErrMalformed))
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/ai/parse/... -run TestParseCaption`
Expected: FAIL — `Caption` undefined.

- [ ] **Step 3: Implement**

```go
// internal/ai/parse/caption.go
package parse

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// MaxCaptionChars is the upper bound enforced on caption length.
const MaxCaptionChars = 300

type captionEnvelope struct {
	Caption string `json:"caption"`
}

// markdownPatterns are structural matches: any hit means the model
// emitted markdown despite the prompt. Bare apostrophes/hyphens are
// fine because they don't form constructs.
var markdownPatterns = []*regexp.Regexp{
	regexp.MustCompile("`"),                        // any backtick
	regexp.MustCompile(`\*\*[^*]+\*\*`),            // **bold**
	regexp.MustCompile(`\*[^*\s][^*]*[^*\s]\*`),    // *emphasis*
	regexp.MustCompile(`(?m)^\s*#\s`),              // # heading
	regexp.MustCompile(`\[[^\]]+\]\([^)]+\)`),      // [text](url)
	regexp.MustCompile(`(?m)^\s*>\s`),              // > blockquote
	regexp.MustCompile(`\|[^|\n]+\|`),              // |table|
	regexp.MustCompile(`(?m)^\s*[-*]\s`),           // - or * list item
	regexp.MustCompile(`(?m)^\s*\d+\.\s`),          // 1. ordered list
	regexp.MustCompile(`_[^_\s][^_]*[^_\s]_`),      // _emphasis_
}

// Caption extracts the caption text and applies the validation pipeline.
// On any rejection, returns ErrMalformed wrapped with the reason.
func Caption(raw string) (string, error) {
	body := strings.TrimSpace(raw)
	if m := fencedJSON.FindStringSubmatch(body); m != nil {
		body = m[1]
	}
	var env captionEnvelope
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		return "", fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	text := strings.TrimSpace(env.Caption)
	if text == "" {
		return "", fmt.Errorf("%w: empty caption", ErrMalformed)
	}
	if len([]rune(text)) > MaxCaptionChars {
		return "", fmt.Errorf("%w: caption exceeds %d chars", ErrMalformed, MaxCaptionChars)
	}
	for _, pat := range markdownPatterns {
		if pat.MatchString(text) {
			return "", fmt.Errorf("%w: caption contains markdown (%s)", ErrMalformed, pat.String())
		}
	}
	return text, nil
}
```

- [ ] **Step 4: Run all parse tests**

Run: `go test ./internal/ai/parse/... -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```
git add internal/ai/parse/caption.go internal/ai/parse/caption_test.go
git commit -m "feat(ai/parse): defensive caption parser with markdown/length validation"
```


---

## Section E — Repositories (DB layer)

### Task E1: Job queue (claim, lease, mark*)

**Files:**
- Create: `internal/ai/jobs/queue.go`
- Create: `internal/ai/jobs/queue_test.go`

The thumb queue (`internal/thumb/queue.go`) is the canonical reference. Differences from thumb: jobs live in their own table (`ai_jobs`), keyed by `(media_id, task)`; we have a `superseded` terminal state; `attempts` only bumps on real provider/parser executions.

- [ ] **Step 1: Write the failing test**

```go
// internal/ai/jobs/queue_test.go
package jobs_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/ai"
	"github.com/wesm/fotobank/internal/ai/jobs"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/testutil"
)

func newQueue(t *testing.T) (*jobs.Queue, owners.Principal, []string) {
	t.Helper()
	rw, ro := testutil.OpenTestDBPair(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	mids := []string{
		testutil.SeedPhoto(t, rw, owner, "p1"),
		testutil.SeedPhoto(t, rw, owner, "p2"),
	}
	return jobs.NewQueue(rw, ro), owner, mids
}

func TestEnqueueAndClaim(t *testing.T) {
	q, _, mids := newQueue(t)
	ctx := context.Background()
	fp := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}

	require.NoError(t, q.Enqueue(ctx, mids[0], ai.TaskTag, fp))
	claims, err := q.ClaimBatch(ctx, ai.TaskTag, 10)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	require.Equal(t, mids[0], claims[0].MediaID)
	require.Equal(t, fp.String(), claims[0].Fingerprint)
}

func TestEnqueueIdempotent(t *testing.T) {
	q, _, mids := newQueue(t)
	ctx := context.Background()
	fp := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}

	require.NoError(t, q.Enqueue(ctx, mids[0], ai.TaskTag, fp))
	require.NoError(t, q.Enqueue(ctx, mids[0], ai.TaskTag, fp)) // no-op

	claims, err := q.ClaimBatch(ctx, ai.TaskTag, 10)
	require.NoError(t, err)
	require.Len(t, claims, 1)
}

func TestSupersedeOnFingerprintChange(t *testing.T) {
	q, _, mids := newQueue(t)
	ctx := context.Background()
	fp1 := ai.Fingerprint{ModelID: "m1", PromptVersion: "tags-v1", InputProfile: "ip"}
	fp2 := ai.Fingerprint{ModelID: "m2", PromptVersion: "tags-v1", InputProfile: "ip"}

	require.NoError(t, q.Enqueue(ctx, mids[0], ai.TaskTag, fp1))
	require.NoError(t, q.Enqueue(ctx, mids[0], ai.TaskTag, fp2))

	claims, err := q.ClaimBatch(ctx, ai.TaskTag, 10)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	require.Equal(t, fp2.String(), claims[0].Fingerprint)
}

func TestMarkDoneSucceeds(t *testing.T) {
	q, _, mids := newQueue(t)
	ctx := context.Background()
	fp := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}

	require.NoError(t, q.Enqueue(ctx, mids[0], ai.TaskTag, fp))
	claims, _ := q.ClaimBatch(ctx, ai.TaskTag, 10)
	require.NoError(t, q.MarkDone(ctx, claims[0].JobID, claims[0].ClaimedAt))
}

func TestMarkDoneClaimLost(t *testing.T) {
	q, _, mids := newQueue(t)
	ctx := context.Background()
	fp := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}

	require.NoError(t, q.Enqueue(ctx, mids[0], ai.TaskTag, fp))
	claims, _ := q.ClaimBatch(ctx, ai.TaskTag, 10)

	// Sweep simulation: bump the lease to mimic supersession.
	require.NoError(t, q.SupersedeAll(ctx, mids[0], ai.TaskTag))
	err := q.MarkDone(ctx, claims[0].JobID, claims[0].ClaimedAt)
	require.ErrorIs(t, err, jobs.ErrClaimLost)
}

func TestMarkBlockedReleasesAttempts(t *testing.T) {
	q, _, mids := newQueue(t)
	ctx := context.Background()
	fp := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}

	require.NoError(t, q.Enqueue(ctx, mids[0], ai.TaskTag, fp))
	claims, _ := q.ClaimBatch(ctx, ai.TaskTag, 10)

	require.NoError(t, q.MarkBlocked(ctx, claims[0].JobID, claims[0].ClaimedAt, "thumb_blocked"))
	// Promote back; attempts should still be 0 (blocked path is free).
	require.NoError(t, q.PromoteBlocked(ctx, claims[0].JobID))

	again, err := q.ClaimBatch(ctx, ai.TaskTag, 10)
	require.NoError(t, err)
	require.Len(t, again, 1)
	require.EqualValues(t, 0, again[0].Attempts)
}

func TestSweepLeasesResetsStaleWorking(t *testing.T) {
	q, _, mids := newQueue(t)
	ctx := context.Background()
	fp := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}

	require.NoError(t, q.Enqueue(ctx, mids[0], ai.TaskTag, fp))
	claims, _ := q.ClaimBatch(ctx, ai.TaskTag, 10)
	_ = claims

	// Pretend the worker died: backdate claimed_at, run sweep.
	require.NoError(t, q.BackdateClaim(ctx, claims[0].JobID, time.Now().Add(-30*time.Minute)))
	n, err := q.SweepLeases(ctx, 10*time.Minute)
	require.NoError(t, err)
	require.Equal(t, 1, n)

	// Re-claimable, attempts unchanged.
	again, _ := q.ClaimBatch(ctx, ai.TaskTag, 10)
	require.Len(t, again, 1)
	require.EqualValues(t, 0, again[0].Attempts)
}

func TestCounters(t *testing.T) {
	q, _, mids := newQueue(t)
	ctx := context.Background()
	fp := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}

	require.NoError(t, q.Enqueue(ctx, mids[0], ai.TaskTag, fp))
	require.NoError(t, q.Enqueue(ctx, mids[1], ai.TaskTag, fp))
	c, err := q.Counters(ctx, ai.TaskTag)
	require.NoError(t, err)
	require.Equal(t, 2, c.Pending)
	require.Equal(t, 0, c.Working)
}
```

This test relies on small `testutil` helpers. Add them in the same step:

```go
// internal/testutil/seed.go (additive — append to existing file or create if absent)
package testutil

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/owners"
)

// OpenTestDBPair returns the existing testutil RW/RO pair. Wrap the
// existing helper so test code can dual-use it.
func OpenTestDBPair(t *testing.T) (*sql.DB, *sql.DB) {
	t.Helper()
	return OpenTestDB(t).RW(), OpenTestDB(t).RO()
}

// SeedOwner inserts a row into owners and returns the principal.
func SeedOwner(t *testing.T, rw *sql.DB, hub, user string) owners.Principal {
	t.Helper()
	_, err := rw.ExecContext(context.Background(),
		`INSERT INTO owners(hub,user_id,storage_key,created_at) VALUES (?,?,?,?)`,
		hub, user, hub+"/"+user, time.Now().UTC())
	require.NoError(t, err)
	return owners.Principal{Hub: hub, UserID: user}
}

// SeedPhoto inserts a minimal photo row and returns its ID.
func SeedPhoto(t *testing.T, rw *sql.DB, p owners.Principal, label string) string {
	t.Helper()
	id := uuid.NewString()
	_, err := rw.ExecContext(context.Background(),
		`INSERT INTO media(id, owner_hub, owner_user_id, media_type, mime_type, path,
		 imported_at, size, checksum, thumb_status, thumb_version, import_source_path)
		 VALUES (?,?,?, 'photo','image/jpeg', ?, ?, 0, ?, 'ready', 1, ?)`,
		id, p.Hub, p.UserID, "/photos/"+label+".jpg", time.Now().UTC(), label+"-checksum", label)
	require.NoError(t, err)
	return id
}
```

(If the existing `internal/testutil` API differs — e.g. `OpenTestDB` returns a single `*sql.DB` rather than a wrapper — adapt the helper. Keep the helper signatures shown above; downstream tasks rely on them.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/ai/jobs/...`
Expected: FAIL — package missing.

- [ ] **Step 3: Implement the queue**

```go
// internal/ai/jobs/queue.go
// Package jobs is the DB-only layer of the AI pipeline. It owns the
// claim/lease SQL for ai_jobs and the supersession logic. Mirrors the
// shape of internal/thumb/queue.go.
package jobs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/wesm/fotobank/internal/ai"
)

// ErrClaimLost is returned by Mark* when the lease no longer matches —
// another worker's sweep or a supersession has invalidated this claim.
var ErrClaimLost = errors.New("jobs: claim lost (sweep or supersession)")

// Claim is one row from ClaimBatch.
type Claim struct {
	JobID        string
	MediaID      string
	Fingerprint  string
	Attempts     int
	ClaimedAt    time.Time
}

// Counters is per-task aggregates for the AI panel.
type Counters struct {
	Pending int
	Working int
	Blocked int
}

// Queue is safe to share across goroutines.
type Queue struct {
	rw *sql.DB
	ro *sql.DB
}

func NewQueue(rw, ro *sql.DB) *Queue { return &Queue{rw: rw, ro: ro} }

// Enqueue inserts a pending job for (mediaID, task) under fp.
// Idempotent under the partial unique index when an in-flight job
// already exists for this fingerprint. When an in-flight job exists
// under a different fingerprint, transitions it to 'superseded' and
// inserts the new pending row in one transaction.
func (q *Queue) Enqueue(ctx context.Context, mediaID string, task ai.Task, fp ai.Fingerprint) error {
	tx, err := q.rw.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var existingID string
	var existingFP string
	row := tx.QueryRowContext(ctx,
		`SELECT id, fingerprint FROM ai_jobs
		 WHERE media_id = ? AND task = ? AND status IN ('pending','working','blocked')
		 LIMIT 1`, mediaID, string(task))
	switch err := row.Scan(&existingID, &existingFP); {
	case errors.Is(err, sql.ErrNoRows):
		// fall through and insert
	case err != nil:
		return fmt.Errorf("lookup existing: %w", err)
	default:
		if existingFP == fp.String() {
			return nil // idempotent no-op
		}
		// Supersede the old in-flight row.
		now := time.Now().UTC()
		if _, err := tx.ExecContext(ctx,
			`UPDATE ai_jobs SET status='superseded', completed_at=?,
			 last_error_kind=?, last_error='fingerprint changed'
			 WHERE id = ?`, now, string(ai.ErrKindSuperseded), existingID); err != nil {
			return fmt.Errorf("supersede old: %w", err)
		}
	}

	id := uuid.NewString()
	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO ai_jobs(id, media_id, task, fingerprint, status, attempts, enqueued_at)
		 VALUES (?, ?, ?, ?, 'pending', 0, ?)`,
		id, mediaID, string(task), fp.String(), now); err != nil {
		return fmt.Errorf("insert: %w", err)
	}
	return tx.Commit()
}

// SupersedeAll terminally aborts every in-flight job for (media, task).
// Used by force-rerun and tests.
func (q *Queue) SupersedeAll(ctx context.Context, mediaID string, task ai.Task) error {
	now := time.Now().UTC()
	_, err := q.rw.ExecContext(ctx,
		`UPDATE ai_jobs SET status='superseded', completed_at=?,
		 last_error_kind=?, last_error='fingerprint changed'
		 WHERE media_id=? AND task=? AND status IN ('pending','working','blocked')`,
		now, string(ai.ErrKindSuperseded), mediaID, string(task))
	if err != nil {
		return fmt.Errorf("supersede all: %w", err)
	}
	return nil
}

// ClaimBatch atomically transitions up to n pending rows to 'working'.
func (q *Queue) ClaimBatch(ctx context.Context, task ai.Task, n int) ([]Claim, error) {
	if n <= 0 {
		return nil, nil
	}
	now := time.Now().UTC()
	rows, err := q.rw.QueryContext(ctx, `
		UPDATE ai_jobs
		   SET status='working', claimed_at = ?
		 WHERE id IN (
		   SELECT id FROM ai_jobs
		    WHERE task = ? AND status='pending'
		    ORDER BY enqueued_at ASC, id ASC
		    LIMIT ?
		 )
		RETURNING id, media_id, fingerprint, attempts, claimed_at`, now, string(task), n)
	if err != nil {
		return nil, fmt.Errorf("claim: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Claim
	for rows.Next() {
		var c Claim
		if err := rows.Scan(&c.JobID, &c.MediaID, &c.Fingerprint, &c.Attempts, &c.ClaimedAt); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// MarkDone transitions a working row to 'done' if the claim is still ours.
func (q *Queue) MarkDone(ctx context.Context, jobID string, claimedAt time.Time) error {
	res, err := q.rw.ExecContext(ctx,
		`UPDATE ai_jobs SET status='done', completed_at=?, last_error=NULL, last_error_kind=NULL
		 WHERE id=? AND status='working' AND claimed_at=?`,
		time.Now().UTC(), jobID, claimedAt)
	if err != nil {
		return fmt.Errorf("mark done: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrClaimLost
	}
	return nil
}

// MarkFailed transitions a working row to 'failed' with classification.
func (q *Queue) MarkFailed(ctx context.Context, jobID string, claimedAt time.Time, kind ai.LastErrorKind, errMsg string) error {
	res, err := q.rw.ExecContext(ctx,
		`UPDATE ai_jobs SET status='failed', completed_at=?,
		 attempts=attempts+1, last_error=?, last_error_kind=?
		 WHERE id=? AND status='working' AND claimed_at=?`,
		time.Now().UTC(), errMsg, string(kind), jobID, claimedAt)
	if err != nil {
		return fmt.Errorf("mark failed: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrClaimLost
	}
	return nil
}

// MarkRetryable bumps attempts and returns to 'pending' (caller decides
// whether retry budget remains; if not, prefer MarkFailed instead).
func (q *Queue) MarkRetryable(ctx context.Context, jobID string, claimedAt time.Time, kind ai.LastErrorKind, errMsg string) error {
	res, err := q.rw.ExecContext(ctx,
		`UPDATE ai_jobs SET status='pending', claimed_at=NULL,
		 attempts=attempts+1, last_error=?, last_error_kind=?
		 WHERE id=? AND status='working' AND claimed_at=?`,
		errMsg, string(kind), jobID, claimedAt)
	if err != nil {
		return fmt.Errorf("mark retryable: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrClaimLost
	}
	return nil
}

// MarkBlocked marks a working row as blocked (upstream not ready).
// Does not bump attempts.
func (q *Queue) MarkBlocked(ctx context.Context, jobID string, claimedAt time.Time, reason string) error {
	res, err := q.rw.ExecContext(ctx,
		`UPDATE ai_jobs SET status='blocked', claimed_at=NULL,
		 last_error=?, last_error_kind=?
		 WHERE id=? AND status='working' AND claimed_at=?`,
		reason, string(ai.ErrKindThumbBlocked), jobID, claimedAt)
	if err != nil {
		return fmt.Errorf("mark blocked: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrClaimLost
	}
	return nil
}

// PromoteBlocked transitions a blocked row back to pending. Periodic
// re-checker uses this when upstream readiness has been observed.
func (q *Queue) PromoteBlocked(ctx context.Context, jobID string) error {
	_, err := q.rw.ExecContext(ctx,
		`UPDATE ai_jobs SET status='pending', last_error=NULL, last_error_kind=NULL
		 WHERE id=? AND status='blocked'`, jobID)
	if err != nil {
		return fmt.Errorf("promote: %w", err)
	}
	return nil
}

// SweepLeases resets working rows whose lease is older than ttl.
// Returns how many rows were reset.
func (q *Queue) SweepLeases(ctx context.Context, ttl time.Duration) (int, error) {
	cutoff := time.Now().UTC().Add(-ttl)
	res, err := q.rw.ExecContext(ctx,
		`UPDATE ai_jobs SET status='pending', claimed_at=NULL
		 WHERE status='working' AND claimed_at < ?`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("sweep: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// BackdateClaim is a test helper for SweepLeases. Not used in prod.
func (q *Queue) BackdateClaim(ctx context.Context, jobID string, t time.Time) error {
	_, err := q.rw.ExecContext(ctx,
		`UPDATE ai_jobs SET claimed_at=? WHERE id=?`, t, jobID)
	return err
}

// Counters returns the current depth gauges for one task.
func (q *Queue) Counters(ctx context.Context, task ai.Task) (Counters, error) {
	rows, err := q.ro.QueryContext(ctx,
		`SELECT status, COUNT(*) FROM ai_jobs
		 WHERE task=? AND status IN ('pending','working','blocked')
		 GROUP BY status`, string(task))
	if err != nil {
		return Counters{}, fmt.Errorf("counters: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var c Counters
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			return Counters{}, err
		}
		switch status {
		case "pending":
			c.Pending = n
		case "working":
			c.Working = n
		case "blocked":
			c.Blocked = n
		}
	}
	return c, rows.Err()
}
```

- [ ] **Step 4: Run tests**

```
go mod tidy
go test ./internal/ai/jobs/... -count=1 -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```
git add internal/ai/jobs/ internal/testutil/ go.mod go.sum
git commit -m "feat(ai/jobs): claim/lease queue with supersession on fingerprint change"
```

---

### Task E2: Results repo (atomic stale + promote)

**Files:**
- Create: `internal/ai/results/repo.go`
- Create: `internal/ai/results/repo_test.go`

- [ ] **Step 1: Write the failing test**

```go
// internal/ai/results/repo_test.go
package results_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/ai"
	"github.com/wesm/fotobank/internal/ai/parse"
	"github.com/wesm/fotobank/internal/ai/results"
	"github.com/wesm/fotobank/internal/testutil"
)

func TestWriteTagSuccess(t *testing.T) {
	rw, ro := testutil.OpenTestDBPair(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	mid := testutil.SeedPhoto(t, rw, owner, "p1")
	repo := results.NewRepo(rw, ro)
	ctx := context.Background()
	fp := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}

	require.NoError(t, repo.WriteTagResult(ctx, mid, fp, "hash", []parse.Tag{
		{Key: "dog", Label: "Dog", Rank: 1},
		{Key: "beach", Label: "Beach", Rank: 2},
	}))

	tags, err := repo.GetActiveTags(ctx, mid)
	require.NoError(t, err)
	require.Len(t, tags, 2)
	require.Equal(t, "dog", tags[0].Key)
}

func TestWriteCaptionSuccess(t *testing.T) {
	rw, ro := testutil.OpenTestDBPair(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	mid := testutil.SeedPhoto(t, rw, owner, "p1")
	repo := results.NewRepo(rw, ro)
	ctx := context.Background()
	fp := ai.Fingerprint{ModelID: "m", PromptVersion: "caption-v1", InputProfile: "ip"}

	require.NoError(t, repo.WriteCaptionResult(ctx, mid, fp, "hash", "A dog on a beach."))

	cap, found, err := repo.GetActiveCaption(ctx, mid)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "A dog on a beach.", cap.Text)
	require.Equal(t, "m", cap.ModelID)
	require.WithinDuration(t, time.Now(), cap.GeneratedAt, 10*time.Second)
}

func TestRerunStaleAndPromoteAtomic(t *testing.T) {
	rw, ro := testutil.OpenTestDBPair(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	mid := testutil.SeedPhoto(t, rw, owner, "p1")
	repo := results.NewRepo(rw, ro)
	ctx := context.Background()
	fp1 := ai.Fingerprint{ModelID: "m1", PromptVersion: "caption-v1", InputProfile: "ip"}
	fp2 := ai.Fingerprint{ModelID: "m2", PromptVersion: "caption-v1", InputProfile: "ip"}

	require.NoError(t, repo.WriteCaptionResult(ctx, mid, fp1, "h1", "First."))
	require.NoError(t, repo.WriteCaptionResult(ctx, mid, fp2, "h2", "Second."))

	cap, found, err := repo.GetActiveCaption(ctx, mid)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "Second.", cap.Text)
	require.Equal(t, "m2", cap.ModelID)
}

func TestDoneCounter(t *testing.T) {
	rw, ro := testutil.OpenTestDBPair(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	mids := []string{
		testutil.SeedPhoto(t, rw, owner, "p1"),
		testutil.SeedPhoto(t, rw, owner, "p2"),
	}
	repo := results.NewRepo(rw, ro)
	ctx := context.Background()
	fp := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}

	require.NoError(t, repo.WriteTagResult(ctx, mids[0], fp, "h", []parse.Tag{{Key: "x", Label: "x", Rank: 1}}))
	require.NoError(t, repo.WriteTagResult(ctx, mids[1], fp, "h", []parse.Tag{{Key: "y", Label: "y", Rank: 1}}))

	n, err := repo.DoneCount(ctx, ai.TaskTag, fp)
	require.NoError(t, err)
	require.Equal(t, 2, n)
}
```

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/ai/results/...`
Expected: FAIL — package missing.

- [ ] **Step 3: Implement**

```go
// internal/ai/results/repo.go
// Package results owns the ai_results / media_tags / media_captions
// tables. Every WriteX is atomic: stale prior active + insert new active
// + insert child rows in one transaction.
package results

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/wesm/fotobank/internal/ai"
	"github.com/wesm/fotobank/internal/ai/parse"
)

// Repo is the DB-only handle.
type Repo struct {
	rw *sql.DB
	ro *sql.DB
}

func NewRepo(rw, ro *sql.DB) *Repo { return &Repo{rw: rw, ro: ro} }

// CaptionRow is the active-caption read shape.
type CaptionRow struct {
	Text          string
	ModelID       string
	PromptVersion string
	InputProfile  string
	GeneratedAt   time.Time
}

// WriteTagResult atomically replaces the active tag result for media.
func (r *Repo) WriteTagResult(ctx context.Context, mediaID string, fp ai.Fingerprint, promptHash string, tags []parse.Tag) error {
	return r.writeWithChildren(ctx, mediaID, ai.TaskTag, fp, promptHash, func(ctx context.Context, tx *sql.Tx, resultID string) error {
		for _, t := range tags {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO media_tags(result_id, tag_key, tag_label, rank) VALUES (?,?,?,?)`,
				resultID, t.Key, t.Label, t.Rank); err != nil {
				return fmt.Errorf("insert tag: %w", err)
			}
		}
		return nil
	})
}

// WriteCaptionResult atomically replaces the active caption result.
func (r *Repo) WriteCaptionResult(ctx context.Context, mediaID string, fp ai.Fingerprint, promptHash, text string) error {
	return r.writeWithChildren(ctx, mediaID, ai.TaskCaption, fp, promptHash, func(ctx context.Context, tx *sql.Tx, resultID string) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO media_captions(result_id, text) VALUES (?, ?)`, resultID, text)
		return err
	})
}

func (r *Repo) writeWithChildren(
	ctx context.Context,
	mediaID string,
	task ai.Task,
	fp ai.Fingerprint,
	promptHash string,
	writeChildren func(context.Context, *sql.Tx, string) error,
) error {
	tx, err := r.rw.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		`UPDATE ai_results SET status='stale' WHERE media_id=? AND task=? AND status='active'`,
		mediaID, string(task)); err != nil {
		return fmt.Errorf("stale prior: %w", err)
	}
	id := uuid.NewString()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO ai_results(id, media_id, task, model_id, prompt_version, prompt_hash,
		 input_profile, status, generated_at) VALUES (?,?,?,?,?,?,?, 'active', ?)`,
		id, mediaID, string(task), fp.ModelID, fp.PromptVersion, promptHash, fp.InputProfile,
		time.Now().UTC()); err != nil {
		return fmt.Errorf("insert result: %w", err)
	}
	if err := writeChildren(ctx, tx, id); err != nil {
		return err
	}
	return tx.Commit()
}

// GetActiveTags returns the active-result tags for a media, ordered by rank.
func (r *Repo) GetActiveTags(ctx context.Context, mediaID string) ([]parse.Tag, error) {
	rows, err := r.ro.QueryContext(ctx, `
		SELECT t.tag_key, t.tag_label, t.rank
		  FROM media_tags t
		  JOIN ai_results r ON r.id = t.result_id
		 WHERE r.media_id = ? AND r.task = 'tag' AND r.status = 'active'
		 ORDER BY t.rank ASC`, mediaID)
	if err != nil {
		return nil, fmt.Errorf("query tags: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []parse.Tag
	for rows.Next() {
		var t parse.Tag
		if err := rows.Scan(&t.Key, &t.Label, &t.Rank); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// GetActiveCaption returns the active caption for a media or (zero, false, nil).
func (r *Repo) GetActiveCaption(ctx context.Context, mediaID string) (CaptionRow, bool, error) {
	row := r.ro.QueryRowContext(ctx, `
		SELECT c.text, r.model_id, r.prompt_version, r.input_profile, r.generated_at
		  FROM media_captions c
		  JOIN ai_results r ON r.id = c.result_id
		 WHERE r.media_id = ? AND r.task = 'caption' AND r.status = 'active'`, mediaID)
	var c CaptionRow
	switch err := row.Scan(&c.Text, &c.ModelID, &c.PromptVersion, &c.InputProfile, &c.GeneratedAt); {
	case errors.Is(err, sql.ErrNoRows):
		return CaptionRow{}, false, nil
	case err != nil:
		return CaptionRow{}, false, fmt.Errorf("scan caption: %w", err)
	}
	return c, true, nil
}

// DoneCount returns how many active results exist for (task, fp).
func (r *Repo) DoneCount(ctx context.Context, task ai.Task, fp ai.Fingerprint) (int, error) {
	row := r.ro.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM ai_results
		 WHERE task=? AND status='active'
		   AND model_id=? AND prompt_version=? AND input_profile=?`,
		string(task), fp.ModelID, fp.PromptVersion, fp.InputProfile)
	var n int
	if err := row.Scan(&n); err != nil {
		return 0, fmt.Errorf("count: %w", err)
	}
	return n, nil
}

// HasActiveForFingerprint is used by the gap scanner to skip rows that
// already have a current-fingerprint active result.
func (r *Repo) HasActiveForFingerprint(ctx context.Context, mediaID string, task ai.Task, fp ai.Fingerprint) (bool, error) {
	row := r.ro.QueryRowContext(ctx, `
		SELECT 1 FROM ai_results
		 WHERE media_id=? AND task=? AND status='active'
		   AND model_id=? AND prompt_version=? AND input_profile=?`,
		mediaID, string(task), fp.ModelID, fp.PromptVersion, fp.InputProfile)
	var n int
	switch err := row.Scan(&n); {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("scan: %w", err)
	}
	return true, nil
}
```

- [ ] **Step 4: Run tests**

```
go test ./internal/ai/results/... -count=1 -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```
git add internal/ai/results/
git commit -m "feat(ai/results): atomic stale + promote with child rows"
```


---

### Task E3: Failures repo

**Files:**
- Create: `internal/ai/failures/repo.go`
- Create: `internal/ai/failures/repo_test.go`

- [ ] **Step 1: Write the failing test**

```go
// internal/ai/failures/repo_test.go
package failures_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/ai"
	"github.com/wesm/fotobank/internal/ai/failures"
	"github.com/wesm/fotobank/internal/testutil"
)

func TestRecordAndDelete(t *testing.T) {
	rw, ro := testutil.OpenTestDBPair(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	mid := testutil.SeedPhoto(t, rw, owner, "p1")
	repo := failures.NewRepo(rw, ro)
	ctx := context.Background()
	fp := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}

	require.NoError(t, repo.Record(ctx, mid, ai.TaskTag, fp, ai.ErrKindMalformed, "bad json", 2))

	rows, err := repo.ListForFingerprint(ctx, ai.TaskTag, fp, 10)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, mid, rows[0].MediaID)

	require.NoError(t, repo.Delete(ctx, mid, ai.TaskTag, fp))
	rows, _ = repo.ListForFingerprint(ctx, ai.TaskTag, fp, 10)
	require.Empty(t, rows)
}

func TestCountForFingerprint(t *testing.T) {
	rw, ro := testutil.OpenTestDBPair(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	mids := []string{
		testutil.SeedPhoto(t, rw, owner, "p1"),
		testutil.SeedPhoto(t, rw, owner, "p2"),
	}
	repo := failures.NewRepo(rw, ro)
	ctx := context.Background()
	fp := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}

	require.NoError(t, repo.Record(ctx, mids[0], ai.TaskTag, fp, ai.ErrKindMalformed, "x", 2))
	require.NoError(t, repo.Record(ctx, mids[1], ai.TaskTag, fp, ai.ErrKindProvider4xx, "y", 1))

	n, err := repo.CountForFingerprint(ctx, ai.TaskTag, fp)
	require.NoError(t, err)
	require.Equal(t, 2, n)
}

func TestDeleteAllForFingerprint(t *testing.T) {
	rw, ro := testutil.OpenTestDBPair(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	mid := testutil.SeedPhoto(t, rw, owner, "p1")
	repo := failures.NewRepo(rw, ro)
	ctx := context.Background()
	fp := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}

	require.NoError(t, repo.Record(ctx, mid, ai.TaskTag, fp, ai.ErrKindMalformed, "x", 2))
	n, err := repo.DeleteAllForFingerprint(ctx, ai.TaskTag, fp)
	require.NoError(t, err)
	require.Equal(t, 1, n)
}
```

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/ai/failures/...`
Expected: FAIL — package missing.

- [ ] **Step 3: Implement**

```go
// internal/ai/failures/repo.go
// Package failures owns the ai_failures table — the live record of
// per-media failures keyed by full fingerprint. Successful retry
// deletes the matching row; old-fingerprint rows simply stop
// appearing in current-fingerprint queries.
package failures

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/wesm/fotobank/internal/ai"
)

// Row is one current-fingerprint failure.
type Row struct {
	MediaID       string
	Task          ai.Task
	ModelID       string
	PromptVersion string
	InputProfile  string
	LastError     string
	LastErrorKind ai.LastErrorKind
	AttemptCount  int
	FailedAt      time.Time
}

type Repo struct {
	rw *sql.DB
	ro *sql.DB
}

func NewRepo(rw, ro *sql.DB) *Repo { return &Repo{rw: rw, ro: ro} }

// Record upserts a failure row.
func (r *Repo) Record(ctx context.Context, mediaID string, task ai.Task, fp ai.Fingerprint, kind ai.LastErrorKind, msg string, attempts int) error {
	_, err := r.rw.ExecContext(ctx, `
		INSERT INTO ai_failures
		  (media_id, task, model_id, prompt_version, input_profile,
		   last_error, last_error_kind, attempt_count, failed_at)
		VALUES (?,?,?,?,?, ?,?,?, ?)
		ON CONFLICT(media_id, task, model_id, prompt_version, input_profile) DO UPDATE SET
		  last_error      = excluded.last_error,
		  last_error_kind = excluded.last_error_kind,
		  attempt_count   = excluded.attempt_count,
		  failed_at       = excluded.failed_at`,
		mediaID, string(task), fp.ModelID, fp.PromptVersion, fp.InputProfile,
		msg, string(kind), attempts, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("upsert failure: %w", err)
	}
	return nil
}

// Delete removes a single failure row (used after a successful retry).
func (r *Repo) Delete(ctx context.Context, mediaID string, task ai.Task, fp ai.Fingerprint) error {
	_, err := r.rw.ExecContext(ctx, `
		DELETE FROM ai_failures
		 WHERE media_id=? AND task=?
		   AND model_id=? AND prompt_version=? AND input_profile=?`,
		mediaID, string(task), fp.ModelID, fp.PromptVersion, fp.InputProfile)
	if err != nil {
		return fmt.Errorf("delete failure: %w", err)
	}
	return nil
}

// DeleteAllForFingerprint clears all current-fingerprint failures for a task.
// Returns rows deleted.
func (r *Repo) DeleteAllForFingerprint(ctx context.Context, task ai.Task, fp ai.Fingerprint) (int, error) {
	res, err := r.rw.ExecContext(ctx, `
		DELETE FROM ai_failures
		 WHERE task=? AND model_id=? AND prompt_version=? AND input_profile=?`,
		string(task), fp.ModelID, fp.PromptVersion, fp.InputProfile)
	if err != nil {
		return 0, fmt.Errorf("delete all: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// ListForFingerprint returns up to N most-recent current-fingerprint failures.
func (r *Repo) ListForFingerprint(ctx context.Context, task ai.Task, fp ai.Fingerprint, limit int) ([]Row, error) {
	rows, err := r.ro.QueryContext(ctx, `
		SELECT media_id, last_error, last_error_kind, attempt_count, failed_at
		  FROM ai_failures
		 WHERE task=? AND model_id=? AND prompt_version=? AND input_profile=?
		 ORDER BY failed_at DESC
		 LIMIT ?`, string(task), fp.ModelID, fp.PromptVersion, fp.InputProfile, limit)
	if err != nil {
		return nil, fmt.Errorf("list: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Row
	for rows.Next() {
		var x Row
		var kind string
		x.Task = task
		x.ModelID = fp.ModelID
		x.PromptVersion = fp.PromptVersion
		x.InputProfile = fp.InputProfile
		if err := rows.Scan(&x.MediaID, &x.LastError, &kind, &x.AttemptCount, &x.FailedAt); err != nil {
			return nil, err
		}
		x.LastErrorKind = ai.LastErrorKind(kind)
		out = append(out, x)
	}
	return out, rows.Err()
}

// CountForFingerprint returns how many failures match the active fingerprint.
func (r *Repo) CountForFingerprint(ctx context.Context, task ai.Task, fp ai.Fingerprint) (int, error) {
	row := r.ro.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM ai_failures
		 WHERE task=? AND model_id=? AND prompt_version=? AND input_profile=?`,
		string(task), fp.ModelID, fp.PromptVersion, fp.InputProfile)
	var n int
	if err := row.Scan(&n); err != nil {
		return 0, fmt.Errorf("count: %w", err)
	}
	return n, nil
}
```

- [ ] **Step 4: Run tests**

```
go test ./internal/ai/failures/... -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```
git add internal/ai/failures/
git commit -m "feat(ai/failures): live current-fingerprint failure rows"
```

---

### Task E4: Skipped repo

**Files:**
- Create: `internal/ai/skipped/repo.go`
- Create: `internal/ai/skipped/repo_test.go`

- [ ] **Step 1: Write the failing test**

```go
// internal/ai/skipped/repo_test.go
package skipped_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/ai"
	"github.com/wesm/fotobank/internal/ai/skipped"
	"github.com/wesm/fotobank/internal/testutil"
)

func TestRecordAndCount(t *testing.T) {
	rw, ro := testutil.OpenTestDBPair(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	mid1 := testutil.SeedPhoto(t, rw, owner, "p1")
	mid2 := testutil.SeedPhoto(t, rw, owner, "p2")
	repo := skipped.NewRepo(rw, ro)
	ctx := context.Background()

	require.NoError(t, repo.Record(ctx, mid1, ai.TaskTag, "video"))
	require.NoError(t, repo.Record(ctx, mid1, ai.TaskCaption, "video"))
	require.NoError(t, repo.Record(ctx, mid2, ai.TaskTag, "no_preview"))

	n, err := repo.Count(ctx, ai.TaskTag)
	require.NoError(t, err)
	require.Equal(t, 2, n)

	n, err = repo.Count(ctx, ai.TaskCaption)
	require.NoError(t, err)
	require.Equal(t, 1, n)
}

func TestRecordIsIdempotent(t *testing.T) {
	rw, ro := testutil.OpenTestDBPair(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	mid := testutil.SeedPhoto(t, rw, owner, "p1")
	repo := skipped.NewRepo(rw, ro)
	ctx := context.Background()

	require.NoError(t, repo.Record(ctx, mid, ai.TaskTag, "video"))
	require.NoError(t, repo.Record(ctx, mid, ai.TaskTag, "video"))

	reason, found, err := repo.Get(context.Background(), mid, ai.TaskTag)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "video", reason)
}
```

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/ai/skipped/...`
Expected: FAIL — package missing.

- [ ] **Step 3: Implement**

```go
// internal/ai/skipped/repo.go
// Package skipped owns the ai_skipped table: provenance for media
// that won't be queued (videos at import time) or that were claimed
// then skipped (no_preview thumb state).
package skipped

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/wesm/fotobank/internal/ai"
)

type Repo struct {
	rw *sql.DB
	ro *sql.DB
}

func NewRepo(rw, ro *sql.DB) *Repo { return &Repo{rw: rw, ro: ro} }

// Record upserts a skip row. Calling Record twice for the same
// (media, task) is idempotent — reason updates in place.
func (r *Repo) Record(ctx context.Context, mediaID string, task ai.Task, reason string) error {
	_, err := r.rw.ExecContext(ctx, `
		INSERT INTO ai_skipped(media_id, task, reason, recorded_at) VALUES (?,?,?,?)
		ON CONFLICT(media_id, task) DO UPDATE SET
		  reason=excluded.reason, recorded_at=excluded.recorded_at`,
		mediaID, string(task), reason, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("upsert skip: %w", err)
	}
	return nil
}

// Get returns the skip reason for (media, task).
func (r *Repo) Get(ctx context.Context, mediaID string, task ai.Task) (string, bool, error) {
	row := r.ro.QueryRowContext(ctx,
		`SELECT reason FROM ai_skipped WHERE media_id=? AND task=?`, mediaID, string(task))
	var reason string
	switch err := row.Scan(&reason); {
	case errors.Is(err, sql.ErrNoRows):
		return "", false, nil
	case err != nil:
		return "", false, err
	}
	return reason, true, nil
}

// Count returns how many media are skipped for a task (any reason).
func (r *Repo) Count(ctx context.Context, task ai.Task) (int, error) {
	row := r.ro.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM ai_skipped WHERE task=?`, string(task))
	var n int
	if err := row.Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}
```

- [ ] **Step 4: Run tests**

```
go test ./internal/ai/skipped/... -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```
git add internal/ai/skipped/
git commit -m "feat(ai/skipped): provenance for video and no-preview skips"
```

---

## Section F — Acknowledgement gate

### Task F1: Acknowledgement helper

**Files:**
- Create: `internal/ai/ack/ack.go`
- Create: `internal/ai/ack/ack_test.go`

The web-frontend design's `user_settings(principal_hub, principal_user_id, key, value, updated_at)` table already exists. We use key `ai.hidden_processing_acknowledged_at` with an ISO-8601 timestamp value.

- [ ] **Step 1: Write the failing test**

```go
// internal/ai/ack/ack_test.go
package ack_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/ai/ack"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/testutil"
)

func TestNotAcknowledgedByDefault(t *testing.T) {
	rw, ro := testutil.OpenTestDBPair(t)
	a := ack.New(rw, ro)
	got, err := a.IsAcknowledged(context.Background(),
		owners.Principal{Hub: "local", UserID: "alice"})
	require.NoError(t, err)
	require.False(t, got)
}

func TestAcknowledgePersists(t *testing.T) {
	rw, ro := testutil.OpenTestDBPair(t)
	a := ack.New(rw, ro)
	p := owners.Principal{Hub: "local", UserID: "alice"}

	require.NoError(t, a.Acknowledge(context.Background(), p))

	got, err := a.IsAcknowledged(context.Background(), p)
	require.NoError(t, err)
	require.True(t, got)
}

func TestAcknowledgeIsIdempotent(t *testing.T) {
	rw, ro := testutil.OpenTestDBPair(t)
	a := ack.New(rw, ro)
	p := owners.Principal{Hub: "local", UserID: "alice"}
	require.NoError(t, a.Acknowledge(context.Background(), p))
	require.NoError(t, a.Acknowledge(context.Background(), p))
}

func TestAcknowledgementIsPerPrincipal(t *testing.T) {
	rw, ro := testutil.OpenTestDBPair(t)
	a := ack.New(rw, ro)
	require.NoError(t, a.Acknowledge(context.Background(),
		owners.Principal{Hub: "local", UserID: "alice"}))
	got, err := a.IsAcknowledged(context.Background(),
		owners.Principal{Hub: "local", UserID: "bob"})
	require.NoError(t, err)
	require.False(t, got)
}
```

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/ai/ack/...`
Expected: FAIL — package missing.

- [ ] **Step 3: Implement**

```go
// internal/ai/ack/ack.go
// Package ack persists per-principal acknowledgements that gate AI
// worker activity. v1 has one kind: hidden-photo processing.
package ack

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/wesm/fotobank/internal/owners"
)

// SettingKey is the canonical key in user_settings for the
// hidden-processing acknowledgement.
const SettingKey = "ai.hidden_processing_acknowledged_at"

type Store struct {
	rw *sql.DB
	ro *sql.DB
}

func New(rw, ro *sql.DB) *Store { return &Store{rw: rw, ro: ro} }

// IsAcknowledged returns true if any acknowledgement row exists for p.
func (s *Store) IsAcknowledged(ctx context.Context, p owners.Principal) (bool, error) {
	row := s.ro.QueryRowContext(ctx, `
		SELECT 1 FROM user_settings
		 WHERE principal_hub=? AND principal_user_id=? AND key=?`,
		p.Hub, p.UserID, SettingKey)
	var n int
	switch err := row.Scan(&n); {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("scan: %w", err)
	}
	return true, nil
}

// Acknowledge persists the acknowledgement for p. Idempotent — re-ack
// updates updated_at but doesn't error.
func (s *Store) Acknowledge(ctx context.Context, p owners.Principal) error {
	now := time.Now().UTC()
	value := now.Format(time.RFC3339)
	_, err := s.rw.ExecContext(ctx, `
		INSERT INTO user_settings(principal_hub, principal_user_id, key, value, updated_at)
		VALUES (?,?,?,?,?)
		ON CONFLICT(principal_hub, principal_user_id, key) DO UPDATE SET
		  value=excluded.value, updated_at=excluded.updated_at`,
		p.Hub, p.UserID, SettingKey, value, now)
	if err != nil {
		return fmt.Errorf("ack upsert: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Run tests**

```
go test ./internal/ai/ack/... -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```
git add internal/ai/ack/
git commit -m "feat(ai/ack): per-principal hidden-processing acknowledgement"
```

---

## Section G — Worker

### Task G1: Vision semaphore

**Files:**
- Create: `internal/ai/worker/semaphore.go`
- Create: `internal/ai/worker/semaphore_test.go`

- [ ] **Step 1: Write the failing test**

```go
// internal/ai/worker/semaphore_test.go
package worker_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/ai/worker"
)

func TestSemaphoreLimitsConcurrency(t *testing.T) {
	sem := worker.NewVisionSemaphore(2)
	var inflight, peak atomic.Int32

	run := func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		require.NoError(t, sem.Acquire(ctx))
		defer sem.Release()
		current := inflight.Add(1)
		for {
			if cur := peak.Load(); cur >= current || peak.CompareAndSwap(cur, current) {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
		inflight.Add(-1)
	}

	done := make(chan struct{})
	for i := 0; i < 10; i++ {
		go func() { run(); done <- struct{}{} }()
	}
	for i := 0; i < 10; i++ {
		<-done
	}
	require.LessOrEqual(t, peak.Load(), int32(2))
}

func TestSemaphoreAcquireRespectsCancel(t *testing.T) {
	sem := worker.NewVisionSemaphore(1)
	require.NoError(t, sem.Acquire(context.Background()))
	defer sem.Release()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.Error(t, sem.Acquire(ctx))
}
```

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/ai/worker/...`
Expected: FAIL — package missing.

- [ ] **Step 3: Implement**

```go
// internal/ai/worker/semaphore.go
// Package worker contains the AI claim/lease loop and supporting types.
// VisionSemaphore is the global cap on simultaneous in-flight VLM calls
// across all task workers — without it, per-task concurrency caps would
// still allow tag and caption to oversubscribe a single GPU.
package worker

import "context"

// VisionSemaphore is a counting semaphore around the [ai.vision]
// max_inflight cap. Acquire blocks until a slot is available or ctx
// is canceled; Release returns a slot.
type VisionSemaphore struct {
	ch chan struct{}
}

// NewVisionSemaphore constructs a semaphore with `n` slots. Callers
// must ensure n >= 1; ai.Config.Validate already enforces this.
func NewVisionSemaphore(n int) *VisionSemaphore {
	return &VisionSemaphore{ch: make(chan struct{}, n)}
}

// Acquire grabs one slot. Returns ctx.Err() if canceled before a slot
// becomes available.
func (s *VisionSemaphore) Acquire(ctx context.Context) error {
	select {
	case s.ch <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Release returns a slot. Always paired with a successful Acquire.
func (s *VisionSemaphore) Release() {
	<-s.ch
}
```

- [ ] **Step 4: Run tests**

```
go test ./internal/ai/worker/... -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```
git add internal/ai/worker/
git commit -m "feat(ai/worker): global vision in-flight semaphore"
```


---

### Task G2: Worker core (claim/process/finalize loop)

**Files:**
- Create: `internal/ai/worker/worker.go`
- Create: `internal/ai/worker/worker_test.go`

The Worker is generic over task: it holds a `Process` callback that takes a claim plus the gateway response and returns the worker's success/failure decision. Concrete tag/caption workers are thin wrappers in Task G3.

- [ ] **Step 1: Write the failing test**

```go
// internal/ai/worker/worker_test.go
package worker_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/ai"
	"github.com/wesm/fotobank/internal/ai/ack"
	"github.com/wesm/fotobank/internal/ai/failures"
	"github.com/wesm/fotobank/internal/ai/gateway"
	"github.com/wesm/fotobank/internal/ai/jobs"
	"github.com/wesm/fotobank/internal/ai/parse"
	"github.com/wesm/fotobank/internal/ai/results"
	"github.com/wesm/fotobank/internal/ai/skipped"
	"github.com/wesm/fotobank/internal/ai/worker"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/testutil"
)

type stubGateway struct {
	respond func() (gateway.Response, error)
	calls   atomic.Int32
}

func (s *stubGateway) Generate(_ context.Context, _ gateway.Request) (gateway.Response, error) {
	s.calls.Add(1)
	return s.respond()
}
func (s *stubGateway) HealthCheck(_ context.Context) error { return nil }

type stubImage struct{ jpeg []byte; status string; err error }

func (s *stubImage) ResolveAndEncode(_ context.Context, _ string) ([]byte, string, error) {
	return s.jpeg, s.status, s.err
}

func setup(t *testing.T) (
	*worker.Worker, *jobs.Queue, *results.Repo, *failures.Repo, *skipped.Repo,
	owners.Principal, string, *stubGateway, *stubImage,
) {
	rw, ro := testutil.OpenTestDBPair(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	mid := testutil.SeedPhoto(t, rw, owner, "p1")

	q := jobs.NewQueue(rw, ro)
	resR := results.NewRepo(rw, ro)
	failR := failures.NewRepo(rw, ro)
	skipR := skipped.NewRepo(rw, ro)
	ackS := ack.New(rw, ro)
	require.NoError(t, ackS.Acknowledge(context.Background(), owner))

	gw := &stubGateway{}
	img := &stubImage{jpeg: []byte{0xff, 0xd8, 0xff, 0xd9}, status: "ready"}

	w := worker.New(worker.Config{
		Task:           ai.TaskTag,
		Fingerprint:    ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"},
		PromptHash:     "h",
		PromptText:     "describe",
		Gateway:        gw,
		Image:          img,
		Queue:          q,
		Results:        resR,
		Failures:       failR,
		Skipped:        skipR,
		Acknowledged:   func(ctx context.Context, _ owners.Principal) (bool, error) {
			ok, err := ackS.IsAcknowledged(ctx, owner)
			return ok, err
		},
		MaxJobAttempts: 2,
		Process: worker.TagProcess,
		Sem:     worker.NewVisionSemaphore(1),
	})

	return w, q, resR, failR, skipR, owner, mid, gw, img
}

func TestWorkerProcessesSuccessfulTagJob(t *testing.T) {
	w, q, resR, _, _, owner, mid, gw, _ := setup(t)
	gw.respond = func() (gateway.Response, error) {
		return gateway.Response{Text: `{"tags":["dog","beach"]}`}, nil
	}
	fp := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}
	require.NoError(t, q.Enqueue(context.Background(), mid, ai.TaskTag, fp))
	_ = owner

	n, err := w.RunOnce(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, n)
	tags, _ := resR.GetActiveTags(context.Background(), mid)
	require.Len(t, tags, 2)
}

func TestWorkerMalformedRetriesThenFails(t *testing.T) {
	w, q, _, failR, _, _, mid, gw, _ := setup(t)
	gw.respond = func() (gateway.Response, error) {
		return gateway.Response{Text: "totally not json"}, nil
	}
	fp := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}
	require.NoError(t, q.Enqueue(context.Background(), mid, ai.TaskTag, fp))

	// First pass: malformed → retry.
	n, _ := w.RunOnce(context.Background())
	require.Equal(t, 1, n)
	// Second pass: malformed → terminal failed + ai_failures row.
	n, _ = w.RunOnce(context.Background())
	require.Equal(t, 1, n)

	cnt, _ := failR.CountForFingerprint(context.Background(), ai.TaskTag, fp)
	require.Equal(t, 1, cnt)
}

func TestWorker4xxFailsImmediately(t *testing.T) {
	w, q, _, failR, _, _, mid, gw, _ := setup(t)
	gw.respond = func() (gateway.Response, error) {
		return gateway.Response{}, errors.New("HTTP 400: image too large: " + gateway.ErrPermanent4xx.Error())
	}
	gw.respond = func() (gateway.Response, error) {
		// Force unwrap match.
		return gateway.Response{}, wrapPermanent("HTTP 400: image too large")
	}
	fp := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}
	require.NoError(t, q.Enqueue(context.Background(), mid, ai.TaskTag, fp))

	n, err := w.RunOnce(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.EqualValues(t, 1, gw.calls.Load(), "no retry on 4xx")
	cnt, _ := failR.CountForFingerprint(context.Background(), ai.TaskTag, fp)
	require.Equal(t, 1, cnt)
}

func TestWorkerSkipsOnNoPreview(t *testing.T) {
	w, q, _, _, skipR, _, mid, _, img := setup(t)
	img.status = "no_preview"
	fp := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}
	require.NoError(t, q.Enqueue(context.Background(), mid, ai.TaskTag, fp))

	_, err := w.RunOnce(context.Background())
	require.NoError(t, err)
	reason, found, _ := skipR.Get(context.Background(), mid, ai.TaskTag)
	require.True(t, found)
	require.Equal(t, "no_preview", reason)
}

func TestWorkerBlocksOnPendingThumb(t *testing.T) {
	w, q, _, _, _, _, mid, _, img := setup(t)
	img.status = "pending"
	fp := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}
	require.NoError(t, q.Enqueue(context.Background(), mid, ai.TaskTag, fp))

	_, err := w.RunOnce(context.Background())
	require.NoError(t, err)
	c, _ := q.Counters(context.Background(), ai.TaskTag)
	require.Equal(t, 1, c.Blocked)
}

func TestWorkerParkedWithoutAcknowledgement(t *testing.T) {
	rw, ro := testutil.OpenTestDBPair(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	mid := testutil.SeedPhoto(t, rw, owner, "p1")
	q := jobs.NewQueue(rw, ro)
	gw := &stubGateway{respond: func() (gateway.Response, error) {
		return gateway.Response{Text: `{"tags":["x"]}`}, nil
	}}
	img := &stubImage{jpeg: []byte{0xff, 0xd8, 0xff, 0xd9}, status: "ready"}
	w := worker.New(worker.Config{
		Task:           ai.TaskTag,
		Fingerprint:    ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"},
		PromptHash:     "h",
		PromptText:     "describe",
		Gateway:        gw, Image: img,
		Queue: q, Results: results.NewRepo(rw, ro),
		Failures: failures.NewRepo(rw, ro), Skipped: skipped.NewRepo(rw, ro),
		Acknowledged: func(_ context.Context, _ owners.Principal) (bool, error) { return false, nil },
		MaxJobAttempts: 2, Process: worker.TagProcess,
		Sem: worker.NewVisionSemaphore(1),
	})
	fp := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}
	require.NoError(t, q.Enqueue(context.Background(), mid, ai.TaskTag, fp))

	n, err := w.RunOnce(context.Background())
	require.NoError(t, err)
	require.Equal(t, 0, n, "no jobs claimed without ack")
	require.EqualValues(t, 0, gw.calls.Load())
}

// helper: wrap a string error so errors.Is(err, gateway.ErrPermanent4xx) is true.
func wrapPermanent(msg string) error {
	return errWrap{msg: msg}
}

type errWrap struct{ msg string }

func (e errWrap) Error() string                    { return e.msg }
func (e errWrap) Is(target error) bool             { return errors.Is(target, gateway.ErrPermanent4xx) || strings.Contains(e.msg, "HTTP") }
func (e errWrap) Unwrap() error                    { return gateway.ErrPermanent4xx }
var _ error = errWrap{}

// silence parse import in case Go's tooling complains about unused.
var _ = parse.MaxTags
var _ = time.Second
```

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/ai/worker/...`
Expected: FAIL — `Worker`, `Config`, `New`, `TagProcess`, `CaptionProcess` undefined.

- [ ] **Step 3: Implement**

```go
// internal/ai/worker/worker.go
package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/wesm/fotobank/internal/ai"
	"github.com/wesm/fotobank/internal/ai/failures"
	"github.com/wesm/fotobank/internal/ai/gateway"
	"github.com/wesm/fotobank/internal/ai/jobs"
	"github.com/wesm/fotobank/internal/ai/parse"
	"github.com/wesm/fotobank/internal/ai/results"
	"github.com/wesm/fotobank/internal/ai/skipped"
	"github.com/wesm/fotobank/internal/owners"
)

// ImageResolver fetches the AI-input JPEG bytes for a media id, plus
// the underlying thumb_status so the worker can react to upstream
// readiness. Implementation: see Task G4 (real impl) and Task G2 stub.
type ImageResolver interface {
	// ResolveAndEncode returns (jpeg, thumbStatus, err).
	// thumbStatus is the source media's thumb_status — "ready",
	// "pending", "working", "no_preview", or "failed".
	ResolveAndEncode(ctx context.Context, mediaID string) ([]byte, string, error)
}

// AcknowledgedFn returns whether ack has been recorded for a principal.
type AcknowledgedFn func(ctx context.Context, p owners.Principal) (bool, error)

// OwnerOf maps mediaID → principal. Implementation passes through
// internal/media.Repo.GetOwner; tests can use a fake.
type OwnerOf func(ctx context.Context, mediaID string) (owners.Principal, error)

// ProcessOutcome is what the per-task Process callback returns.
type ProcessOutcome struct {
	// One of these is set:
	Tags    []parse.Tag // when task=tag
	Caption string      // when task=caption
}

// ProcessFn is the per-task callback that turns gateway output into
// the persisted shape and writes it. It is given a transactional
// callback `commit` that the worker invokes after success to write to
// the results repo and clear the matching ai_failures row.
type ProcessFn func(rawText string) (ProcessOutcome, error)

// TagProcess parses tags from the VLM response.
func TagProcess(rawText string) (ProcessOutcome, error) {
	tags, err := parse.Tags(rawText)
	if err != nil {
		return ProcessOutcome{}, err
	}
	return ProcessOutcome{Tags: tags}, nil
}

// CaptionProcess parses and validates a caption.
func CaptionProcess(rawText string) (ProcessOutcome, error) {
	text, err := parse.Caption(rawText)
	if err != nil {
		return ProcessOutcome{}, err
	}
	return ProcessOutcome{Caption: text}, nil
}

// Config bundles the dependencies the worker needs.
type Config struct {
	Task           ai.Task
	Fingerprint    ai.Fingerprint
	PromptHash     string
	PromptText     string
	Gateway        gateway.VisionGateway
	Image          ImageResolver
	Queue          *jobs.Queue
	Results        *results.Repo
	Failures       *failures.Repo
	Skipped        *skipped.Repo
	Acknowledged   AcknowledgedFn
	OwnerOf        OwnerOf
	MaxJobAttempts int
	Process        ProcessFn
	Sem            *VisionSemaphore
	BatchSize      int

	// Optional: drives Run loop scheduling. Defaults to 5s when zero.
	PollInterval time.Duration
	Logger       *slog.Logger
}

// Worker runs claim/lease iterations against ai_jobs.
type Worker struct {
	cfg Config
}

func New(cfg Config) *Worker {
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 4
	}
	if cfg.MaxJobAttempts <= 0 {
		cfg.MaxJobAttempts = 2
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 5 * time.Second
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.OwnerOf == nil {
		// Default OwnerOf reads from the media row directly via the
		// queue's RW handle; tests inject their own.
		cfg.OwnerOf = func(_ context.Context, _ string) (owners.Principal, error) {
			return owners.Principal{}, errors.New("OwnerOf not configured")
		}
	}
	return &Worker{cfg: cfg}
}

// Run loops until ctx is canceled, calling RunOnce on each tick.
func (w *Worker) Run(ctx context.Context) error {
	t := time.NewTicker(w.cfg.PollInterval)
	defer t.Stop()
	for {
		if _, err := w.RunOnce(ctx); err != nil {
			w.cfg.Logger.Warn("ai worker tick failed", "task", w.cfg.Task, "err", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

// RunOnce claims and processes up to BatchSize jobs. Returns how many
// jobs were processed (regardless of outcome).
func (w *Worker) RunOnce(ctx context.Context) (int, error) {
	claims, err := w.cfg.Queue.ClaimBatch(ctx, w.cfg.Task, w.cfg.BatchSize)
	if err != nil {
		return 0, fmt.Errorf("claim: %w", err)
	}
	processed := 0
	for _, c := range claims {
		if err := w.handleOne(ctx, c); err != nil {
			w.cfg.Logger.Warn("ai job error", "job", c.JobID, "err", err)
		}
		processed++
	}
	return processed, nil
}

func (w *Worker) handleOne(ctx context.Context, c jobs.Claim) error {
	// Owner-scoped acknowledgement gate. Without ack, release the
	// claim (back to pending) without bumping attempts.
	owner, err := w.cfg.OwnerOf(ctx, c.MediaID)
	if err != nil {
		return w.releaseTransient(ctx, c, ai.ErrKindTransient, fmt.Sprintf("owner lookup: %v", err))
	}
	ack, err := w.cfg.Acknowledged(ctx, owner)
	if err != nil {
		return w.releaseTransient(ctx, c, ai.ErrKindTransient, fmt.Sprintf("ack lookup: %v", err))
	}
	if !ack {
		return w.releaseTransient(ctx, c, ai.ErrKindTransient, "acknowledgement_required")
	}

	// Resolve image bytes and consult thumb_status.
	jpegBytes, thumbStatus, err := w.cfg.Image.ResolveAndEncode(ctx, c.MediaID)
	if err != nil {
		return w.markFailed(ctx, c, ai.ErrKindMissingAIInput, err.Error())
	}
	switch thumbStatus {
	case "pending", "working":
		return w.cfg.Queue.MarkBlocked(ctx, c.JobID, c.ClaimedAt, "thumb_"+thumbStatus)
	case "no_preview":
		if err := w.cfg.Skipped.Record(ctx, c.MediaID, w.cfg.Task, "no_preview"); err != nil {
			return err
		}
		// Worker holds a 'working' claim — finalize as 'done' with no result.
		return w.cfg.Queue.MarkDone(ctx, c.JobID, c.ClaimedAt)
	case "failed":
		return w.cfg.Queue.MarkBlocked(ctx, c.JobID, c.ClaimedAt, "thumb_failed")
	case "ready":
		// fall through
	default:
		return w.markFailed(ctx, c, ai.ErrKindMissingAIInput, "unknown thumb_status: "+thumbStatus)
	}

	// Acquire the global vision semaphore.
	if err := w.cfg.Sem.Acquire(ctx); err != nil {
		return w.releaseTransient(ctx, c, ai.ErrKindTransient, "sem: "+err.Error())
	}
	defer w.cfg.Sem.Release()

	// Call the gateway.
	resp, err := w.cfg.Gateway.Generate(ctx, gateway.Request{
		Model:  w.cfg.Fingerprint.ModelID,
		Prompt: w.cfg.PromptText,
		JPEG:   jpegBytes,
	})
	if err != nil {
		if errors.Is(err, gateway.ErrPermanent4xx) {
			return w.markFailed(ctx, c, ai.ErrKindProvider4xx, err.Error())
		}
		return w.maybeRetryOrFail(ctx, c, ai.ErrKindTransient, err.Error())
	}

	// Parse + persist.
	out, err := w.cfg.Process(resp.Text)
	if err != nil {
		if errors.Is(err, parse.ErrMalformed) {
			return w.maybeRetryOrFail(ctx, c, ai.ErrKindMalformed, err.Error())
		}
		return w.maybeRetryOrFail(ctx, c, ai.ErrKindTransient, err.Error())
	}

	switch w.cfg.Task {
	case ai.TaskTag:
		if err := w.cfg.Results.WriteTagResult(ctx, c.MediaID, w.cfg.Fingerprint, w.cfg.PromptHash, out.Tags); err != nil {
			return w.maybeRetryOrFail(ctx, c, ai.ErrKindTransient, "write tags: "+err.Error())
		}
	case ai.TaskCaption:
		if err := w.cfg.Results.WriteCaptionResult(ctx, c.MediaID, w.cfg.Fingerprint, w.cfg.PromptHash, out.Caption); err != nil {
			return w.maybeRetryOrFail(ctx, c, ai.ErrKindTransient, "write caption: "+err.Error())
		}
	default:
		return w.markFailed(ctx, c, ai.ErrKindTransient, "unknown task: "+string(w.cfg.Task))
	}

	// Clear any prior current-fingerprint failure for this media+task.
	if err := w.cfg.Failures.Delete(ctx, c.MediaID, w.cfg.Task, w.cfg.Fingerprint); err != nil {
		w.cfg.Logger.Warn("clear failure row", "err", err)
	}
	return w.cfg.Queue.MarkDone(ctx, c.JobID, c.ClaimedAt)
}

// maybeRetryOrFail bumps attempts. If the post-bump count reaches
// MaxJobAttempts, finalize as failed; otherwise release back to pending.
func (w *Worker) maybeRetryOrFail(ctx context.Context, c jobs.Claim, kind ai.LastErrorKind, msg string) error {
	if c.Attempts+1 >= w.cfg.MaxJobAttempts {
		return w.markFailed(ctx, c, kind, msg)
	}
	if err := w.cfg.Queue.MarkRetryable(ctx, c.JobID, c.ClaimedAt, kind, msg); err != nil {
		return err
	}
	return nil
}

func (w *Worker) markFailed(ctx context.Context, c jobs.Claim, kind ai.LastErrorKind, msg string) error {
	if err := w.cfg.Queue.MarkFailed(ctx, c.JobID, c.ClaimedAt, kind, msg); err != nil {
		return err
	}
	if err := w.cfg.Failures.Record(ctx, c.MediaID, w.cfg.Task, w.cfg.Fingerprint, kind, msg, c.Attempts+1); err != nil {
		w.cfg.Logger.Warn("record failure row", "err", err)
	}
	return nil
}

// releaseTransient puts a claim back to pending without bumping attempts.
// Used for ack-not-yet, owner-lookup transients, semaphore cancellation.
func (w *Worker) releaseTransient(ctx context.Context, c jobs.Claim, _ ai.LastErrorKind, msg string) error {
	// Use MarkBlocked to release without bumping attempts but in a
	// distinct status; the periodic re-checker (lease sweep) promotes
	// blocked rows back to pending.
	return w.cfg.Queue.MarkBlocked(ctx, c.JobID, c.ClaimedAt, msg)
}
```

- [ ] **Step 4: Run tests**

```
go test ./internal/ai/worker/... -count=1 -v
```

Expected: PASS. (If `TestWorker4xxFailsImmediately` fails because the stub error doesn't `errors.Is` to `gateway.ErrPermanent4xx`, switch the stub respond to return `fmt.Errorf("HTTP 400: %w", gateway.ErrPermanent4xx)` instead of the `errWrap` shim.)

- [ ] **Step 5: Commit**

```
git add internal/ai/worker/worker.go internal/ai/worker/worker_test.go
git commit -m "feat(ai/worker): claim/lease loop with retry, ack gate, thumb-state machine"
```

---

### Task G3: Concrete tag and caption workers + image resolver

**Files:**
- Create: `internal/ai/imginput/resolver.go`
- Create: `internal/ai/imginput/resolver_test.go`

The Worker accepts an `ImageResolver` interface; this task provides the concrete implementation that reads `media.thumb_status` and the `preview` blob from the existing thumb store.

- [ ] **Step 1: Identify existing storage helpers**

Read `internal/storage/` and `internal/thumb/` for the helper that resolves thumb-blob paths/bytes. The thumb worker uses something like `storage.PreviewPath(mediaID)` or a similar helper. If absent, add one as part of this task in `internal/storage/preview.go`.

- [ ] **Step 2: Write the failing test**

```go
// internal/ai/imginput/resolver_test.go
package imginput_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/ai/imginput"
	"github.com/wesm/fotobank/internal/testutil"
)

func TestResolverReturnsBytesAndStatus(t *testing.T) {
	rw, ro := testutil.OpenTestDBPair(t)
	dir := t.TempDir()
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	mid := testutil.SeedPhoto(t, rw, owner, "p1")

	// Drop a preview blob into the test storage layout.
	previewPath := filepath.Join(dir, mid+".preview.jpg")
	require.NoError(t, os.WriteFile(previewPath, makeJPEG(t, 2560, 1700), 0o644))

	r := imginput.NewResolver(rw, ro, imginput.LocateFunc(func(id string) string {
		return filepath.Join(dir, id+".preview.jpg")
	}))

	bytes, status, err := r.ResolveAndEncode(context.Background(), mid)
	require.NoError(t, err)
	require.Equal(t, "ready", status)
	require.NotEmpty(t, bytes)
}

func TestResolverPropagatesMissingPreview(t *testing.T) {
	rw, ro := testutil.OpenTestDBPair(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	mid := testutil.SeedPhoto(t, rw, owner, "p1")

	r := imginput.NewResolver(rw, ro, imginput.LocateFunc(func(id string) string {
		return "/nonexistent/" + id
	}))
	_, status, err := r.ResolveAndEncode(context.Background(), mid)
	require.Error(t, err)
	require.Equal(t, "ready", status)
}

// reuse helper from encode_test.go via package alias if needed; keep a
// local copy here so tests are self-contained.
func makeJPEG(t *testing.T, w, h int) []byte { return testutilJPEG(t, w, h) }
```

Add a small shared JPEG helper if `imginput` doesn't already export one:

```go
// internal/ai/imginput/testjpeg.go
package imginput

// (moved to test-only file later; for now exposed for resolver_test.go)
```

(If Go tooling refuses cross-test-file helpers, inline `makeJPEG` from `encode_test.go` directly into `resolver_test.go`.)

- [ ] **Step 3: Implement**

```go
// internal/ai/imginput/resolver.go
package imginput

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
)

// LocateFunc returns the on-disk path for a media's preview blob.
// Production wiring should use the thumb storage helper; tests inject
// a func that points at a temp dir.
type LocateFunc func(mediaID string) string

// Resolver is the production ImageResolver implementation.
type Resolver struct {
	rw     *sql.DB
	ro     *sql.DB
	locate LocateFunc
}

// NewResolver constructs a Resolver. `locate` returns the absolute
// filesystem path of the preview blob for a given media id.
func NewResolver(rw, ro *sql.DB, locate LocateFunc) *Resolver {
	return &Resolver{rw: rw, ro: ro, locate: locate}
}

// ResolveAndEncode reads thumb_status, loads the preview blob, and
// re-encodes it to the AI input profile. Returns ("", "no_preview",
// nil) — i.e. soft skip — only when thumb_status itself is "no_preview";
// other errors propagate.
func (r *Resolver) ResolveAndEncode(ctx context.Context, mediaID string) ([]byte, string, error) {
	var status string
	row := r.ro.QueryRowContext(ctx, `SELECT thumb_status FROM media WHERE id=?`, mediaID)
	switch err := row.Scan(&status); {
	case errors.Is(err, sql.ErrNoRows):
		return nil, "", fmt.Errorf("media %s not found", mediaID)
	case err != nil:
		return nil, "", fmt.Errorf("read thumb_status: %w", err)
	}
	if status != "ready" {
		return nil, status, nil
	}
	path := r.locate(mediaID)
	srcJPEG, err := os.ReadFile(path)
	if err != nil {
		return nil, status, fmt.Errorf("read preview %s: %w", path, err)
	}
	out, err := Encode(srcJPEG)
	if err != nil {
		return nil, status, fmt.Errorf("encode profile: %w", err)
	}
	return out, status, nil
}
```

- [ ] **Step 4: Run tests**

```
go test ./internal/ai/imginput/... -count=1 -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```
git add internal/ai/imginput/resolver.go internal/ai/imginput/resolver_test.go
git commit -m "feat(ai/imginput): resolver reads thumb_status and re-encodes preview"
```


---

## Section H — Gap scanner

### Task H1: Gap-fill predicate + enqueue

**Files:**
- Create: `internal/ai/gapscanner/scanner.go`
- Create: `internal/ai/gapscanner/scanner_test.go`

The gap scanner walks media missing an active result for the active fingerprint per task and enqueues jobs. `--force` widens the predicate to include media that already have an active result.

- [ ] **Step 1: Write the failing test**

```go
// internal/ai/gapscanner/scanner_test.go
package gapscanner_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/ai"
	"github.com/wesm/fotobank/internal/ai/gapscanner"
	"github.com/wesm/fotobank/internal/ai/jobs"
	"github.com/wesm/fotobank/internal/ai/parse"
	"github.com/wesm/fotobank/internal/ai/results"
	"github.com/wesm/fotobank/internal/ai/skipped"
	"github.com/wesm/fotobank/internal/testutil"
)

func TestGapScannerEnqueuesMissingMedia(t *testing.T) {
	rw, ro := testutil.OpenTestDBPair(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	mids := []string{
		testutil.SeedPhoto(t, rw, owner, "p1"),
		testutil.SeedPhoto(t, rw, owner, "p2"),
	}
	q := jobs.NewQueue(rw, ro)
	resR := results.NewRepo(rw, ro)
	skipR := skipped.NewRepo(rw, ro)
	fp := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}

	// p1 already has an active result for fp; p2 doesn't.
	require.NoError(t, resR.WriteTagResult(context.Background(), mids[0], fp, "h",
		[]parse.Tag{{Key: "x", Label: "x", Rank: 1}}))

	s := gapscanner.New(rw, ro, q, resR, skipR)
	n, err := s.Scan(context.Background(), gapscanner.ScanRequest{
		Task: ai.TaskTag, Fingerprint: fp, Force: false, Limit: 100,
	})
	require.NoError(t, err)
	require.Equal(t, 1, n)
	c, _ := q.Counters(context.Background(), ai.TaskTag)
	require.Equal(t, 1, c.Pending)
	_ = mids[1]
}

func TestGapScannerForceIncludesActiveMedia(t *testing.T) {
	rw, ro := testutil.OpenTestDBPair(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	mids := []string{
		testutil.SeedPhoto(t, rw, owner, "p1"),
		testutil.SeedPhoto(t, rw, owner, "p2"),
	}
	q := jobs.NewQueue(rw, ro)
	resR := results.NewRepo(rw, ro)
	skipR := skipped.NewRepo(rw, ro)
	fp := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}

	require.NoError(t, resR.WriteTagResult(context.Background(), mids[0], fp, "h",
		[]parse.Tag{{Key: "x", Label: "x", Rank: 1}}))

	s := gapscanner.New(rw, ro, q, resR, skipR)
	n, err := s.Scan(context.Background(), gapscanner.ScanRequest{
		Task: ai.TaskTag, Fingerprint: fp, Force: true, Limit: 100,
	})
	require.NoError(t, err)
	require.Equal(t, 2, n)
	_ = mids[1]
}

func TestGapScannerSkipsVideoAndRecordsSkip(t *testing.T) {
	rw, ro := testutil.OpenTestDBPair(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	mid := testutil.SeedPhoto(t, rw, owner, "p1")
	// Convert that media row to video by direct UPDATE (bypasses tooling).
	_, err := rw.ExecContext(context.Background(),
		`UPDATE media SET media_type='video' WHERE id=?`, mid)
	require.NoError(t, err)

	q := jobs.NewQueue(rw, ro)
	resR := results.NewRepo(rw, ro)
	skipR := skipped.NewRepo(rw, ro)
	fp := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}

	s := gapscanner.New(rw, ro, q, resR, skipR)
	n, err := s.Scan(context.Background(), gapscanner.ScanRequest{
		Task: ai.TaskTag, Fingerprint: fp, Force: false, Limit: 100,
	})
	require.NoError(t, err)
	require.Equal(t, 0, n, "video produces no enqueue")

	reason, found, _ := skipR.Get(context.Background(), mid, ai.TaskTag)
	require.True(t, found)
	require.Equal(t, "video", reason)
}
```

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/ai/gapscanner/...`
Expected: FAIL — package missing.

- [ ] **Step 3: Implement**

```go
// internal/ai/gapscanner/scanner.go
// Package gapscanner walks media rows and enqueues missing AI jobs
// for the active fingerprint per task. CLI (fotobank ai backfill) and
// the periodic server-side tick both call Scan.
package gapscanner

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/wesm/fotobank/internal/ai"
	"github.com/wesm/fotobank/internal/ai/jobs"
	"github.com/wesm/fotobank/internal/ai/results"
	"github.com/wesm/fotobank/internal/ai/skipped"
)

// ScanRequest controls one scan invocation.
type ScanRequest struct {
	Task        ai.Task
	Fingerprint ai.Fingerprint
	Force       bool     // include media that already have an active result for fp
	Limit       int      // 0 = unbounded
	MediaIDs    []string // 0 = all media; otherwise scoped subset
}

// Scanner walks media rows, classifies them, and enqueues jobs.
type Scanner struct {
	rw      *sql.DB
	ro      *sql.DB
	q       *jobs.Queue
	results *results.Repo
	skipped *skipped.Repo
}

func New(rw, ro *sql.DB, q *jobs.Queue, r *results.Repo, s *skipped.Repo) *Scanner {
	return &Scanner{rw: rw, ro: ro, q: q, results: r, skipped: s}
}

// Scan returns the number of jobs enqueued.
func (s *Scanner) Scan(ctx context.Context, req ScanRequest) (int, error) {
	if !req.Task.Valid() {
		return 0, fmt.Errorf("invalid task %q", req.Task)
	}
	mediaRows, err := s.candidates(ctx, req)
	if err != nil {
		return 0, fmt.Errorf("candidates: %w", err)
	}
	enqueued := 0
	for _, m := range mediaRows {
		if m.MediaType == "video" {
			if err := s.skipped.Record(ctx, m.ID, req.Task, "video"); err != nil {
				return enqueued, fmt.Errorf("record video skip: %w", err)
			}
			continue
		}
		if !req.Force {
			has, err := s.results.HasActiveForFingerprint(ctx, m.ID, req.Task, req.Fingerprint)
			if err != nil {
				return enqueued, fmt.Errorf("check active: %w", err)
			}
			if has {
				continue
			}
		}
		if err := s.q.Enqueue(ctx, m.ID, req.Task, req.Fingerprint); err != nil {
			return enqueued, fmt.Errorf("enqueue %s: %w", m.ID, err)
		}
		enqueued++
	}
	return enqueued, nil
}

type candidate struct {
	ID        string
	MediaType string
}

// candidates returns media to consider. With MediaIDs set, returns only
// those rows; otherwise scans up to Limit (or all) media.
func (s *Scanner) candidates(ctx context.Context, req ScanRequest) ([]candidate, error) {
	if len(req.MediaIDs) > 0 {
		out := make([]candidate, 0, len(req.MediaIDs))
		for _, id := range req.MediaIDs {
			var mt string
			row := s.ro.QueryRowContext(ctx, `SELECT media_type FROM media WHERE id=?`, id)
			if err := row.Scan(&mt); err != nil {
				return nil, fmt.Errorf("read %s: %w", id, err)
			}
			out = append(out, candidate{ID: id, MediaType: mt})
		}
		return out, nil
	}
	q := `SELECT id, media_type FROM media`
	args := []any{}
	if req.Limit > 0 {
		q += ` LIMIT ?`
		args = append(args, req.Limit)
	}
	rows, err := s.ro.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query media: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.ID, &c.MediaType); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
```

- [ ] **Step 4: Run tests**

```
go test ./internal/ai/gapscanner/... -count=1 -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```
git add internal/ai/gapscanner/
git commit -m "feat(ai/gapscanner): enqueue missing-fp jobs, --force widens predicate"
```

---

## Section I — Service layer + health aggregator

### Task I1: AI service (auth-scoped wrappers)

**Files:**
- Create: `internal/service/ai/service.go`
- Create: `internal/service/ai/service_test.go`
- Modify: `internal/errs/errs.go` — add `ErrAcknowledgementRequired`
- Modify: `internal/httpapi/errors.go` — translate `ErrAcknowledgementRequired` → 409

The AI service is the auth boundary. Single-principal stub mode in v1 means the only check is "did this caller's principal acknowledge?" but the call shape (`caller owners.Principal`) is in place for multi-principal later.

- [ ] **Step 1: Add the sentinel error and translator**

In `internal/errs/errs.go`:

```go
// ErrAcknowledgementRequired is returned when an AI action requires
// the caller to acknowledge hidden-photo processing first.
ErrAcknowledgementRequired = errors.New("acknowledgement required")
```

In `internal/httpapi/errors.go::Translate`, add a case before the default:

```go
case errors.Is(err, errs.ErrAcknowledgementRequired):
    return huma.Error409Conflict(errs.ErrAcknowledgementRequired.Error())
```

Run `go build ./...` to confirm.

- [ ] **Step 2: Write the failing service test**

```go
// internal/service/ai/service_test.go
package ai_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/ai"
	"github.com/wesm/fotobank/internal/ai/ack"
	"github.com/wesm/fotobank/internal/ai/failures"
	"github.com/wesm/fotobank/internal/ai/gapscanner"
	"github.com/wesm/fotobank/internal/ai/jobs"
	"github.com/wesm/fotobank/internal/ai/results"
	"github.com/wesm/fotobank/internal/ai/skipped"
	aiservice "github.com/wesm/fotobank/internal/service/ai"
	"github.com/wesm/fotobank/internal/errs"
	"github.com/wesm/fotobank/internal/testutil"
)

func newSvc(t *testing.T) *aiservice.Service {
	t.Helper()
	rw, ro := testutil.OpenTestDBPair(t)
	q := jobs.NewQueue(rw, ro)
	resR := results.NewRepo(rw, ro)
	failR := failures.NewRepo(rw, ro)
	skipR := skipped.NewRepo(rw, ro)
	ackS := ack.New(rw, ro)
	gs := gapscanner.New(rw, ro, q, resR, skipR)

	return aiservice.New(aiservice.Deps{
		Queue:    q,
		Results:  resR,
		Failures: failR,
		Skipped:  skipR,
		Ack:      ackS,
		Gap:      gs,
		ConfigFingerprints: aiservice.ConfigFingerprints{
			Tag:     ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"},
			Caption: ai.Fingerprint{ModelID: "m", PromptVersion: "caption-v1", InputProfile: "ip"},
		},
	})
}

func TestAcknowledgePersists(t *testing.T) {
	svc := newSvc(t)
	owner := testutil.SeedOwner(t, getRW(t, svc), "local", "alice") // small helper below
	require.NoError(t, svc.Acknowledge(context.Background(), owner))
	got, err := svc.IsAcknowledged(context.Background(), owner)
	require.NoError(t, err)
	require.True(t, got)
}

func TestBackfillRequiresAck(t *testing.T) {
	svc := newSvc(t)
	owner := testutil.SeedOwner(t, getRW(t, svc), "local", "alice")
	_, err := svc.Backfill(context.Background(), owner, ai.TaskTag, false /*force*/)
	require.ErrorIs(t, err, errs.ErrAcknowledgementRequired)
}
```

The test imports `getRW(t, svc)` — a helper to expose the underlying DB for seeding. Add it via service test wiring rather than exposing internals. A simpler approach: instantiate the deps directly in the test instead of using `newSvc`.

Refactor the test to pass DB handles in directly:

```go
func makeServiceWithDB(t *testing.T) (*aiservice.Service, *sql.DB) {
	rw, ro := testutil.OpenTestDBPair(t)
	q := jobs.NewQueue(rw, ro)
	resR := results.NewRepo(rw, ro)
	failR := failures.NewRepo(rw, ro)
	skipR := skipped.NewRepo(rw, ro)
	ackS := ack.New(rw, ro)
	gs := gapscanner.New(rw, ro, q, resR, skipR)

	return aiservice.New(aiservice.Deps{
		Queue: q, Results: resR, Failures: failR, Skipped: skipR,
		Ack: ackS, Gap: gs,
		ConfigFingerprints: aiservice.ConfigFingerprints{
			Tag:     ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"},
			Caption: ai.Fingerprint{ModelID: "m", PromptVersion: "caption-v1", InputProfile: "ip"},
		},
	}), rw
}

// then in tests:
//   svc, rw := makeServiceWithDB(t)
//   owner := testutil.SeedOwner(t, rw, "local", "alice")
```

- [ ] **Step 3: Implement**

```go
// internal/service/ai/service.go
// Package ai (under internal/service) is the auth boundary for AI
// operations. Every method takes a caller principal and either scopes
// the action to that principal's library or returns
// errs.ErrAcknowledgementRequired / errs.ErrPermissionDenied.
package ai

import (
	"context"
	"fmt"

	"github.com/wesm/fotobank/internal/ai"
	"github.com/wesm/fotobank/internal/ai/ack"
	"github.com/wesm/fotobank/internal/ai/failures"
	"github.com/wesm/fotobank/internal/ai/gapscanner"
	"github.com/wesm/fotobank/internal/ai/jobs"
	"github.com/wesm/fotobank/internal/ai/results"
	"github.com/wesm/fotobank/internal/ai/skipped"
	"github.com/wesm/fotobank/internal/errs"
	"github.com/wesm/fotobank/internal/owners"
)

// ConfigFingerprints captures the active per-task fingerprints derived
// from the operator's [ai] config + Go-defined prompts at boot. The
// service treats them as immutable for its lifetime; a config change
// requires a process restart.
type ConfigFingerprints struct {
	Tag     ai.Fingerprint
	Caption ai.Fingerprint
}

// Lookup returns the active fingerprint for a task.
func (c ConfigFingerprints) Lookup(t ai.Task) (ai.Fingerprint, bool) {
	switch t {
	case ai.TaskTag:
		return c.Tag, true
	case ai.TaskCaption:
		return c.Caption, true
	}
	return ai.Fingerprint{}, false
}

// Deps bundles the collaborators the service needs.
type Deps struct {
	Queue              *jobs.Queue
	Results            *results.Repo
	Failures           *failures.Repo
	Skipped            *skipped.Repo
	Ack                *ack.Store
	Gap                *gapscanner.Scanner
	ConfigFingerprints ConfigFingerprints
}

// Service is the auth-scoped AI service.
type Service struct {
	deps Deps
}

func New(deps Deps) *Service { return &Service{deps: deps} }

// IsAcknowledged returns whether p has acknowledged hidden-photo processing.
func (s *Service) IsAcknowledged(ctx context.Context, p owners.Principal) (bool, error) {
	return s.deps.Ack.IsAcknowledged(ctx, p)
}

// Acknowledge records the ack for p.
func (s *Service) Acknowledge(ctx context.Context, p owners.Principal) error {
	return s.deps.Ack.Acknowledge(ctx, p)
}

// Backfill enqueues missing-fingerprint jobs for caller-owned media.
// Returns the count enqueued. Requires acknowledgement.
func (s *Service) Backfill(ctx context.Context, caller owners.Principal, task ai.Task, force bool) (int, error) {
	if !task.Valid() {
		return 0, fmt.Errorf("%w: invalid task", errs.ErrInvalidArgument)
	}
	ok, err := s.deps.Ack.IsAcknowledged(ctx, caller)
	if err != nil {
		return 0, fmt.Errorf("ack lookup: %w", err)
	}
	if !ok {
		return 0, errs.ErrAcknowledgementRequired
	}
	fp, _ := s.deps.ConfigFingerprints.Lookup(task)
	return s.deps.Gap.Scan(ctx, gapscanner.ScanRequest{
		Task:        task,
		Fingerprint: fp,
		Force:       force,
		Limit:       0,
	})
}

// RetryFailed clears all current-fingerprint failures for task and
// re-enqueues the corresponding media. Returns the count enqueued.
func (s *Service) RetryFailed(ctx context.Context, caller owners.Principal, task ai.Task) (int, error) {
	if !task.Valid() {
		return 0, fmt.Errorf("%w: invalid task", errs.ErrInvalidArgument)
	}
	ok, err := s.deps.Ack.IsAcknowledged(ctx, caller)
	if err != nil {
		return 0, fmt.Errorf("ack lookup: %w", err)
	}
	if !ok {
		return 0, errs.ErrAcknowledgementRequired
	}
	fp, _ := s.deps.ConfigFingerprints.Lookup(task)
	rows, err := s.deps.Failures.ListForFingerprint(ctx, task, fp, 1000)
	if err != nil {
		return 0, fmt.Errorf("list failures: %w", err)
	}
	mediaIDs := make([]string, 0, len(rows))
	for _, r := range rows {
		mediaIDs = append(mediaIDs, r.MediaID)
	}
	if _, err := s.deps.Failures.DeleteAllForFingerprint(ctx, task, fp); err != nil {
		return 0, fmt.Errorf("delete failures: %w", err)
	}
	if len(mediaIDs) == 0 {
		return 0, nil
	}
	return s.deps.Gap.Scan(ctx, gapscanner.ScanRequest{
		Task: task, Fingerprint: fp, Force: true, MediaIDs: mediaIDs,
	})
}

// RetryPhoto enqueues a single (media, task) job after clearing its
// failure row. Lightbox per-photo Retry button.
func (s *Service) RetryPhoto(ctx context.Context, caller owners.Principal, mediaID string, task ai.Task) error {
	if !task.Valid() {
		return fmt.Errorf("%w: invalid task", errs.ErrInvalidArgument)
	}
	ok, err := s.deps.Ack.IsAcknowledged(ctx, caller)
	if err != nil {
		return fmt.Errorf("ack lookup: %w", err)
	}
	if !ok {
		return errs.ErrAcknowledgementRequired
	}
	fp, _ := s.deps.ConfigFingerprints.Lookup(task)
	if err := s.deps.Failures.Delete(ctx, mediaID, task, fp); err != nil {
		return fmt.Errorf("delete failure: %w", err)
	}
	_, err = s.deps.Gap.Scan(ctx, gapscanner.ScanRequest{
		Task: task, Fingerprint: fp, Force: true, MediaIDs: []string{mediaID},
	})
	return err
}

// ListFailures returns recent failures for the active fingerprint.
func (s *Service) ListFailures(ctx context.Context, caller owners.Principal, task ai.Task, limit int) ([]failures.Row, error) {
	if !task.Valid() {
		return nil, fmt.Errorf("%w: invalid task", errs.ErrInvalidArgument)
	}
	fp, _ := s.deps.ConfigFingerprints.Lookup(task)
	return s.deps.Failures.ListForFingerprint(ctx, task, fp, limit)
}
```

- [ ] **Step 4: Run tests**

```
go build ./...
go test ./internal/service/ai/... -count=1 -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```
git add internal/service/ai/ internal/errs/errs.go internal/httpapi/errors.go
git commit -m "feat(service/ai): auth-scoped AI service with ack gate"
```

---

### Task I2: Health aggregator

**Files:**
- Create: `internal/service/ai/health.go`
- Create: `internal/service/ai/health_test.go`

- [ ] **Step 1: Write the failing test**

```go
// internal/service/ai/health_test.go
package ai_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/ai"
	aiservice "github.com/wesm/fotobank/internal/service/ai"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/testutil"
)

type stubProbe struct {
	err error
}

func (s stubProbe) Probe(_ context.Context) error { return s.err }

func TestHealthDisabled(t *testing.T) {
	svc, _ := makeServiceWithDB(t)
	h := svc.Health(context.Background(), owners.Principal{Hub: "local", UserID: "alice"},
		aiservice.HealthInput{Enabled: false, Probe: stubProbe{}})
	require.False(t, h.Enabled)
	require.Equal(t, "config_disabled", h.PausedReason)
}

func TestHealthAcknowledgementRequired(t *testing.T) {
	svc, _ := makeServiceWithDB(t)
	h := svc.Health(context.Background(), owners.Principal{Hub: "local", UserID: "alice"},
		aiservice.HealthInput{Enabled: true, Probe: stubProbe{}})
	require.True(t, h.Enabled)
	require.Equal(t, "acknowledgement_required", h.PausedReason)
}

func TestHealthReachable(t *testing.T) {
	svc, rw := makeServiceWithDB(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	require.NoError(t, svc.Acknowledge(context.Background(), owner))

	h := svc.Health(context.Background(), owner, aiservice.HealthInput{
		Enabled: true, Probe: stubProbe{},
	})
	require.Equal(t, "", h.PausedReason)
	require.True(t, h.Vision.Reachable)
	require.Equal(t, "m|tags-v1|ip", h.Tag.ActiveFingerprint)
}

func TestHealthUnreachable(t *testing.T) {
	svc, rw := makeServiceWithDB(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	require.NoError(t, svc.Acknowledge(context.Background(), owner))

	h := svc.Health(context.Background(), owner, aiservice.HealthInput{
		Enabled: true, Probe: stubProbe{err: errors.New("connection refused")},
	})
	require.False(t, h.Vision.Reachable)
	require.Equal(t, "connection refused", h.Vision.LastError)
}
```

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/service/ai/...`
Expected: FAIL — `Health`, `HealthInput`, `Probe` undefined.

- [ ] **Step 3: Implement**

```go
// internal/service/ai/health.go
package ai

import (
	"context"
	"time"

	"github.com/wesm/fotobank/internal/ai"
	"github.com/wesm/fotobank/internal/owners"
)

// Probe abstracts the gateway HealthCheck. Tests pass a stub.
type Probe interface {
	Probe(ctx context.Context) error
}

// HealthInput is the per-call info that doesn't live on the service:
// whether [ai].enabled is true and a probe handle.
type HealthInput struct {
	Enabled bool
	Probe   Probe
}

// Health is the aggregate health response.
type Health struct {
	Enabled       bool        `json:"enabled"`
	PausedReason  string      `json:"paused_reason"` // "" | "config_disabled" | "acknowledgement_required"
	Vision        VisionPart  `json:"vision"`
	Tag           TaskPart    `json:"tag"`
	Caption       TaskPart    `json:"caption"`
}

type VisionPart struct {
	Reachable    bool      `json:"reachable"`
	LastCheckAt  time.Time `json:"last_check_at"`
	LastError    string    `json:"last_error,omitempty"`
}

type TaskPart struct {
	ActiveFingerprint string  `json:"active_fingerprint"`
	Pending           int     `json:"pending"`
	Working           int     `json:"working"`
	Blocked           int     `json:"blocked"`
	FailedActive      int     `json:"failed_active"`
	Skipped           int     `json:"skipped"`
	Done              int     `json:"done"`
	ThroughputPerMin  float64 `json:"throughput_per_min"`
	LastCompletedAt   *time.Time `json:"last_completed_at,omitempty"`
}

// Health aggregates the dashboard payload. Per-call inputs (Enabled,
// Probe) come from the caller; persistent state comes from the service's
// repos.
func (s *Service) Health(ctx context.Context, caller owners.Principal, in HealthInput) Health {
	h := Health{Enabled: in.Enabled}
	if !in.Enabled {
		h.PausedReason = "config_disabled"
		return h
	}
	ack, err := s.deps.Ack.IsAcknowledged(ctx, caller)
	if err != nil || !ack {
		h.PausedReason = "acknowledgement_required"
	}
	now := time.Now().UTC()
	h.Vision.LastCheckAt = now
	if err := in.Probe.Probe(ctx); err == nil {
		h.Vision.Reachable = true
	} else {
		h.Vision.LastError = err.Error()
	}
	h.Tag = s.taskHealth(ctx, ai.TaskTag, s.deps.ConfigFingerprints.Tag)
	h.Caption = s.taskHealth(ctx, ai.TaskCaption, s.deps.ConfigFingerprints.Caption)
	return h
}

func (s *Service) taskHealth(ctx context.Context, t ai.Task, fp ai.Fingerprint) TaskPart {
	tp := TaskPart{ActiveFingerprint: fp.String()}
	if c, err := s.deps.Queue.Counters(ctx, t); err == nil {
		tp.Pending = c.Pending
		tp.Working = c.Working
		tp.Blocked = c.Blocked
	}
	if n, err := s.deps.Failures.CountForFingerprint(ctx, t, fp); err == nil {
		tp.FailedActive = n
	}
	if n, err := s.deps.Skipped.Count(ctx, t); err == nil {
		tp.Skipped = n
	}
	if n, err := s.deps.Results.DoneCount(ctx, t, fp); err == nil {
		tp.Done = n
	}
	return tp
}
```

- [ ] **Step 4: Run tests**

```
go test ./internal/service/ai/... -count=1 -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```
git add internal/service/ai/health.go internal/service/ai/health_test.go
git commit -m "feat(service/ai): aggregate health with paused_reason and per-task counters"
```


---

## Section J — HTTP routes

### Task J1: AI routes (health, failures, backfill, retry, ack)

**Files:**
- Create: `internal/httpapi/ai.go`
- Create: `internal/httpapi/ai_test.go`
- Modify: `internal/httpapi/api.go` — add `AIService` to `Deps` and register routes

`internal/httpapi/api.go` is the canonical wiring file. Read it for the existing `Deps` shape and the route-registration helper pattern (e.g. how `/api/v1/shares` is registered).

- [ ] **Step 1: Add `AIService` to `Deps` and register routes**

In `internal/httpapi/api.go`:

```go
// AIService backs /api/v1/ai/*. Nil means those handlers answer 503
// Service Unavailable so the OpenAPI dumper still emits the schema.
AIService *aiservice.Service
// AIVisionProbe drives /api/v1/ai/health's reachability probe.
// Nil means health reports "unreachable" without attempting a probe.
AIVisionProbe aiservice.Probe
// AIEnabled is the [ai].enabled config flag at boot. Wired explicitly
// so the panel can show config_disabled without the AIService poking
// at config.
AIEnabled bool
```

Where the existing `New` function calls `registerXxxRoutes(...)`, add:

```go
registerAIRoutes(api, deps.AIService, deps.AIVisionProbe, deps.AIEnabled)
```

- [ ] **Step 2: Write the failing test**

```go
// internal/httpapi/ai_test.go
package httpapi_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/ai"
	"github.com/wesm/fotobank/internal/ai/ack"
	"github.com/wesm/fotobank/internal/ai/failures"
	"github.com/wesm/fotobank/internal/ai/gapscanner"
	"github.com/wesm/fotobank/internal/ai/jobs"
	"github.com/wesm/fotobank/internal/ai/results"
	"github.com/wesm/fotobank/internal/ai/skipped"
	"github.com/wesm/fotobank/internal/httpapi"
	aiservice "github.com/wesm/fotobank/internal/service/ai"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/testutil"
)

type unreachableProbe struct{}

func (unreachableProbe) Probe(_ context.Context) error { return errors.New("unreachable") }

func setupAITestServer(t *testing.T) (deps httpapi.Deps, owner owners.Principal, rw, ro *struct{}) {
	// adapt to whatever harness your existing httpapi tests use
	return httpapi.Deps{}, owners.Principal{}, nil, nil
}

func TestAIHealth_AcknowledgementRequired(t *testing.T) {
	rw, ro := testutil.OpenTestDBPair(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	q := jobs.NewQueue(rw, ro)
	resR := results.NewRepo(rw, ro)
	failR := failures.NewRepo(rw, ro)
	skipR := skipped.NewRepo(rw, ro)
	ackS := ack.New(rw, ro)
	gs := gapscanner.New(rw, ro, q, resR, skipR)
	svc := aiservice.New(aiservice.Deps{
		Queue: q, Results: resR, Failures: failR, Skipped: skipR,
		Ack: ackS, Gap: gs,
		ConfigFingerprints: aiservice.ConfigFingerprints{
			Tag:     ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"},
			Caption: ai.Fingerprint{ModelID: "m", PromptVersion: "caption-v1", InputProfile: "ip"},
		},
	})

	server := testutil.NewHTTPAPIServer(t, httpapi.Deps{
		AIService:     svc,
		AIVisionProbe: unreachableProbe{},
		AIEnabled:     true,
	}, owner)
	defer server.Close()

	resp, body := server.GET("/api/v1/ai/health")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, body, `"paused_reason":"acknowledgement_required"`)
}

func TestAIBackfill_RequiresAck(t *testing.T) {
	// As above, then:
	server, owner := /* setup */ (httpapi.Deps{}, owners.Principal{}) // adapt to harness
	_ = owner

	resp, _ := server.POST("/api/v1/ai/backfill", strings.NewReader(`{"task":"tag","scope":"all"}`))
	require.Equal(t, http.StatusConflict, resp.StatusCode)
}
```

> **Note for the executor:** the existing tests in `internal/httpapi` (e.g. `me_test.go`, `albums_test.go`, `shares_test.go`) instantiate the test harness via a helper. Read one of those files first and adapt the boilerplate above. The contract under test is what matters: GET /ai/health returns the JSON shape; POST /ai/backfill returns 409 without ack.

- [ ] **Step 3: Implement routes**

```go
// internal/httpapi/ai.go
package httpapi

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/wesm/fotobank/internal/ai"
	"github.com/wesm/fotobank/internal/identity"
	aiservice "github.com/wesm/fotobank/internal/service/ai"
)

func registerAIRoutes(api huma.API, svc *aiservice.Service, probe aiservice.Probe, enabled bool) {
	if svc == nil {
		// emit schema with stub handlers that 503
		register503(api, "ai-stub", "/api/v1/ai/health", http.MethodGet)
		return
	}
	if probe == nil {
		probe = unreachableProbe{}
	}

	huma.Register(api, huma.Operation{
		OperationID: "ai-health",
		Method:      http.MethodGet,
		Path:        "/api/v1/ai/health",
		Summary:     "AI status snapshot",
	}, func(ctx context.Context, _ *struct{}) (*aiHealthOutput, error) {
		caller, err := identity.FromContext(ctx)
		if err != nil {
			return nil, Translate(err)
		}
		h := svc.Health(ctx, caller.Principal, aiservice.HealthInput{Enabled: enabled, Probe: probe})
		return &aiHealthOutput{Body: h}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "ai-failures",
		Method:      http.MethodGet,
		Path:        "/api/v1/ai/failures",
		Summary:     "Recent AI failures for active fingerprint",
	}, func(ctx context.Context, in *struct {
		Task  string `query:"task" enum:"tag,caption"`
		Limit int    `query:"limit" minimum:"1" maximum:"100" default:"5"`
	}) (*aiFailuresOutput, error) {
		caller, err := identity.FromContext(ctx)
		if err != nil {
			return nil, Translate(err)
		}
		rows, err := svc.ListFailures(ctx, caller.Principal, ai.Task(in.Task), in.Limit)
		if err != nil {
			return nil, Translate(err)
		}
		return &aiFailuresOutput{Body: aiFailuresBody{Rows: rows}}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "ai-backfill",
		Method:      http.MethodPost,
		Path:        "/api/v1/ai/backfill",
	}, func(ctx context.Context, in *aiBackfillInput) (*aiBackfillOutput, error) {
		caller, err := identity.FromContext(ctx)
		if err != nil {
			return nil, Translate(err)
		}
		n, err := svc.Backfill(ctx, caller.Principal, ai.Task(in.Body.Task), in.Body.Force)
		if err != nil {
			return nil, Translate(err)
		}
		return &aiBackfillOutput{Body: aiBackfillBody{Enqueued: n}}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "ai-retry-failed",
		Method:      http.MethodPost,
		Path:        "/api/v1/ai/retry-failed",
	}, func(ctx context.Context, in *aiRetryFailedInput) (*aiBackfillOutput, error) {
		caller, err := identity.FromContext(ctx)
		if err != nil {
			return nil, Translate(err)
		}
		n, err := svc.RetryFailed(ctx, caller.Principal, ai.Task(in.Body.Task))
		if err != nil {
			return nil, Translate(err)
		}
		return &aiBackfillOutput{Body: aiBackfillBody{Enqueued: n}}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "ai-retry-photo",
		Method:      http.MethodPost,
		Path:        "/api/v1/ai/retry-photo",
	}, func(ctx context.Context, in *aiRetryPhotoInput) (*aiAckOutput, error) {
		caller, err := identity.FromContext(ctx)
		if err != nil {
			return nil, Translate(err)
		}
		if err := svc.RetryPhoto(ctx, caller.Principal, in.Body.MediaID, ai.Task(in.Body.Task)); err != nil {
			return nil, Translate(err)
		}
		return &aiAckOutput{Body: aiAckBody{OK: true}}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "ai-acknowledge",
		Method:      http.MethodPost,
		Path:        "/api/v1/ai/acknowledge",
	}, func(ctx context.Context, in *aiAcknowledgeInput) (*aiAckOutput, error) {
		caller, err := identity.FromContext(ctx)
		if err != nil {
			return nil, Translate(err)
		}
		if in.Body.Kind != "hidden_processing" {
			return nil, huma.Error400BadRequest("unknown ack kind")
		}
		if err := svc.Acknowledge(ctx, caller.Principal); err != nil {
			return nil, Translate(err)
		}
		return &aiAckOutput{Body: aiAckBody{OK: true}}, nil
	})
}

type aiHealthOutput struct {
	Body aiservice.Health
}

type aiFailuresOutput struct {
	Body aiFailuresBody
}
type aiFailuresBody struct {
	Rows any `json:"rows"`
}

type aiBackfillInput struct {
	Body struct {
		Task  string `json:"task" enum:"tag,caption"`
		Force bool   `json:"force"`
		Scope string `json:"scope" enum:"all"` // v1: only "all" is accepted
	}
}
type aiBackfillOutput struct {
	Body aiBackfillBody
}
type aiBackfillBody struct {
	Enqueued int `json:"enqueued"`
}

type aiRetryFailedInput struct {
	Body struct {
		Task string `json:"task" enum:"tag,caption"`
	}
}

type aiRetryPhotoInput struct {
	Body struct {
		MediaID string `json:"media_id"`
		Task    string `json:"task" enum:"tag,caption"`
	}
}

type aiAcknowledgeInput struct {
	Body struct {
		Kind string `json:"kind" enum:"hidden_processing"`
	}
}
type aiAckOutput struct {
	Body aiAckBody
}
type aiAckBody struct {
	OK bool `json:"ok"`
}

// unreachableProbe is the fallback when no probe is wired.
type unreachableProbe struct{}

func (unreachableProbe) Probe(_ context.Context) error {
	return errReachableNotConfigured
}

var errReachableNotConfigured = httpError("ai vision probe not configured")

type httpError string

func (e httpError) Error() string { return string(e) }

// register503 registers a stub that always returns 503 — used when the
// AI service is nil so OpenAPI generation still works.
func register503(api huma.API, opID, path, method string) {
	huma.Register(api, huma.Operation{OperationID: opID, Method: method, Path: path}, func(_ context.Context, _ *struct{}) (*struct{}, error) {
		return nil, huma.Error503ServiceUnavailable("not configured")
	})
}
```

(Adapt `identity.FromContext` to whatever the existing code uses — `internal/httpapi/middleware.go` will show you the canonical accessor, e.g. `httpapi.callerFromContext(ctx)`.)

- [ ] **Step 4: Run tests + regenerate OpenAPI**

```
go test ./internal/httpapi/... -count=1 -v
make api-generate
```

Expected: tests PASS; `openapi.json` updated. The pre-commit hook runs `api-generate` automatically; if it fails, fix the generator inputs and recommit.

- [ ] **Step 5: Commit**

```
git add internal/httpapi/ai.go internal/httpapi/ai_test.go internal/httpapi/api.go openapi.json frontend/src/lib/api/generated/
git commit -m "feat(httpapi): /api/v1/ai/* routes (health, failures, backfill, retry, ack)"
```

---

## Section K — SSE events

### Task K1: Add ai.* event types to the event bus

**Files:**
- Modify: `internal/httpapi/events.go` — add new event names + payload types
- Create: `internal/httpapi/ai_events.go` — emit helpers used by the worker
- Create: `internal/httpapi/ai_events_test.go`

The F1 SSE skeleton is type-agnostic: events flow through an `EventBus` that broadcasts named events to a per-principal subscription. The emit helpers below let the worker call a single function instead of constructing event frames manually.

- [ ] **Step 1: Read the existing event bus**

Open `internal/httpapi/events.go`. Identify:
- the `EventBus` type and the function that publishes events to a principal,
- the existing event-type constants (e.g. `eventNameImportProgress`),
- the JSON marshaling for payloads.

- [ ] **Step 2: Add event-type constants and payload types**

Append to `internal/httpapi/events.go`:

```go
const (
	EventNameAITagCompleted     = "ai.tag.completed"
	EventNameAICaptionCompleted = "ai.caption.completed"
	EventNameAIHealthChanged    = "ai.health.changed"
)

// AICompletedEvent is the payload for ai.tag.completed and ai.caption.completed.
type AICompletedEvent struct {
	MediaID       string `json:"media_id"`
	Task          string `json:"task"` // "tag" | "caption"
	Status        string `json:"status"` // "done" | "failed"
	ModelID       string `json:"model_id"`
	PromptVersion string `json:"prompt_version"`
	InputProfile  string `json:"input_profile"`
	ResultID      string `json:"result_id,omitempty"`
}

// AIHealthChangedEvent is the payload for ai.health.changed. Carries the
// minimum delta the SPA needs; full health is fetched from /ai/health on
// receipt to avoid duplicating shape.
type AIHealthChangedEvent struct {
	Vision  AIHealthVisionDelta `json:"vision"`
	Tag     AIHealthTaskDelta   `json:"tag"`
	Caption AIHealthTaskDelta   `json:"caption"`
}
type AIHealthVisionDelta struct {
	Reachable bool   `json:"reachable"`
	LastError string `json:"last_error,omitempty"`
}
type AIHealthTaskDelta struct {
	Pending      int `json:"pending"`
	FailedActive int `json:"failed_active"`
}
```

- [ ] **Step 3: Write emit helpers**

```go
// internal/httpapi/ai_events.go
package httpapi

import (
	"github.com/wesm/fotobank/internal/owners"
)

// EmitAICompleted publishes a tag/caption completion event to p.
// Wired into the worker's success/failure paths via the AIEvents
// interface (Section O).
func (b *EventBus) EmitAICompleted(p owners.Principal, ev AICompletedEvent) {
	name := EventNameAITagCompleted
	if ev.Task == "caption" {
		name = EventNameAICaptionCompleted
	}
	b.Publish(p, name, ev)
}

// EmitAIHealthChanged publishes a health-delta event to p.
func (b *EventBus) EmitAIHealthChanged(p owners.Principal, ev AIHealthChangedEvent) {
	b.Publish(p, EventNameAIHealthChanged, ev)
}
```

- [ ] **Step 4: Test event delivery shape**

```go
// internal/httpapi/ai_events_test.go
package httpapi_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/httpapi"
)

func TestAICompletedEventMarshal(t *testing.T) {
	ev := httpapi.AICompletedEvent{
		MediaID: "m1", Task: "tag", Status: "done",
		ModelID: "qwen2.5-vl:3b", PromptVersion: "tags-v1",
		InputProfile: "jpeg-1024-q85-metadata-stripped-v1", ResultID: "r1",
	}
	b, err := json.Marshal(ev)
	require.NoError(t, err)
	require.Contains(t, string(b), `"task":"tag"`)
	require.Contains(t, string(b), `"prompt_version":"tags-v1"`)
}
```

(A more thorough test that publishes through an `EventBus` and reads from a subscriber depends on the existing test harness in `internal/httpapi/events_test.go`. Mirror the closest existing event-bus test.)

- [ ] **Step 5: Run tests**

```
go test ./internal/httpapi/... -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```
git add internal/httpapi/events.go internal/httpapi/ai_events.go internal/httpapi/ai_events_test.go
git commit -m "feat(httpapi): SSE event types ai.tag/caption.completed and ai.health.changed"
```

---

## Section L — CLI

### Task L1: `fotobank ai` parent + subcommands

**Files:**
- Create: `internal/cli/ai.go`
- Create: `internal/cli/ai_test.go`
- Modify: `internal/cli/root.go` — register the new parent command

Read `internal/cli/albums.go` for the cobra subcommand pattern. The CLI uses `clictx` to construct collaborators; `internal/cli/server.go` shows how to access DB pools, config, etc.

- [ ] **Step 1: Add the parent command + subcommands**

```go
// internal/cli/ai.go
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/wesm/fotobank/internal/ai"
	"github.com/wesm/fotobank/internal/ai/ack"
	"github.com/wesm/fotobank/internal/ai/failures"
	"github.com/wesm/fotobank/internal/ai/gapscanner"
	"github.com/wesm/fotobank/internal/ai/jobs"
	"github.com/wesm/fotobank/internal/ai/results"
	"github.com/wesm/fotobank/internal/ai/skipped"
	aiservice "github.com/wesm/fotobank/internal/service/ai"
	"github.com/wesm/fotobank/internal/errs"
)

func newAICmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ai",
		Short: "AI tagging and captioning operations",
	}
	cmd.AddCommand(newAIStatusCmd())
	cmd.AddCommand(newAIBackfillCmd())
	cmd.AddCommand(newAIRetryFailedCmd())
	cmd.AddCommand(newAIAcknowledgeCmd())
	return cmd
}

// builds an aiservice.Service from process-edge collaborators (DB pools,
// config). Mirror the patterns used by other CLI builders in this dir.
func buildAIService(ctx context.Context, cmd *cobra.Command) (*aiservice.Service, error) {
	cfg, err := loadConfigFromCmd(cmd) // existing helper
	if err != nil {
		return nil, err
	}
	rw, ro, err := openDBPools(ctx, cfg) // existing helper
	if err != nil {
		return nil, err
	}
	q := jobs.NewQueue(rw, ro)
	resR := results.NewRepo(rw, ro)
	failR := failures.NewRepo(rw, ro)
	skipR := skipped.NewRepo(rw, ro)
	ackS := ack.New(rw, ro)
	gs := gapscanner.New(rw, ro, q, resR, skipR)
	return aiservice.New(aiservice.Deps{
		Queue: q, Results: resR, Failures: failR, Skipped: skipR, Ack: ackS, Gap: gs,
		ConfigFingerprints: aiservice.ConfigFingerprints{
			Tag: ai.Fingerprint{
				ModelID:       cfg.AI.Tag.Model,
				PromptVersion: "tags-v1",
				InputProfile:  "jpeg-1024-q85-metadata-stripped-v1",
			},
			Caption: ai.Fingerprint{
				ModelID:       cfg.AI.Caption.Model,
				PromptVersion: "caption-v1",
				InputProfile:  "jpeg-1024-q85-metadata-stripped-v1",
			},
		},
	}), nil
}

func newAIStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show AI health and queue counters",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			svc, err := buildAIService(ctx, cmd)
			if err != nil {
				return err
			}
			caller, err := callerPrincipalFromCmd(cmd) // existing helper
			if err != nil {
				return err
			}
			h := svc.Health(ctx, caller, aiservice.HealthInput{
				Enabled: true, // CLI assumes operator deliberately ran the command
				Probe:   noopProbe{},
			})
			return json.NewEncoder(os.Stdout).Encode(h)
		},
	}
}

type noopProbe struct{}

func (noopProbe) Probe(_ context.Context) error { return nil }

func newAIBackfillCmd() *cobra.Command {
	var taskList []string
	var force bool
	cmd := &cobra.Command{
		Use:   "backfill",
		Short: "Enqueue missing-fingerprint AI jobs",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			svc, err := buildAIService(ctx, cmd)
			if err != nil {
				return err
			}
			caller, err := callerPrincipalFromCmd(cmd)
			if err != nil {
				return err
			}
			tasks := parseTaskList(taskList)
			if len(tasks) == 0 {
				return fmt.Errorf("--task is required (tag,caption)")
			}
			total := 0
			for _, t := range tasks {
				n, err := svc.Backfill(ctx, caller, t, force)
				if err != nil {
					if errors.Is(err, errs.ErrAcknowledgementRequired) {
						return fmt.Errorf("acknowledgement required — run `fotobank ai acknowledge --hidden-processing` first")
					}
					return fmt.Errorf("backfill %s: %w", t, err)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s: enqueued %d\n", t, n)
				total += n
			}
			fmt.Fprintf(cmd.OutOrStdout(), "total: %d\n", total)
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&taskList, "task", nil, "tag,caption")
	cmd.Flags().BoolVar(&force, "force", false, "include media that already have an active result for the current fingerprint")
	return cmd
}

func newAIRetryFailedCmd() *cobra.Command {
	var taskList []string
	cmd := &cobra.Command{
		Use:   "retry-failed",
		Short: "Re-enqueue all current-fingerprint failures",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			svc, err := buildAIService(ctx, cmd)
			if err != nil {
				return err
			}
			caller, err := callerPrincipalFromCmd(cmd)
			if err != nil {
				return err
			}
			tasks := parseTaskList(taskList)
			if len(tasks) == 0 {
				return fmt.Errorf("--task is required (tag,caption)")
			}
			for _, t := range tasks {
				n, err := svc.RetryFailed(ctx, caller, t)
				if err != nil {
					return fmt.Errorf("retry %s: %w", t, err)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s: re-enqueued %d\n", t, n)
			}
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&taskList, "task", nil, "tag,caption")
	return cmd
}

func newAIAcknowledgeCmd() *cobra.Command {
	var hidden bool
	cmd := &cobra.Command{
		Use:   "acknowledge",
		Short: "Acknowledge AI processing characteristics (required to start workers)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			if !hidden {
				return fmt.Errorf("--hidden-processing flag required")
			}
			svc, err := buildAIService(ctx, cmd)
			if err != nil {
				return err
			}
			caller, err := callerPrincipalFromCmd(cmd)
			if err != nil {
				return err
			}
			if err := svc.Acknowledge(ctx, caller); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "ok")
			return nil
		},
	}
	cmd.Flags().BoolVar(&hidden, "hidden-processing", false, "acknowledge that AI processes hidden photos")
	return cmd
}

func parseTaskList(in []string) []ai.Task {
	out := []ai.Task{}
	for _, raw := range in {
		for _, p := range strings.Split(raw, ",") {
			p = strings.TrimSpace(p)
			t := ai.Task(p)
			if t.Valid() {
				out = append(out, t)
			}
		}
	}
	return out
}
```

In `internal/cli/root.go`, add `rootCmd.AddCommand(newAICmd())` next to where `newAlbumsCmd()` and `newSharesCmd()` are registered.

- [ ] **Step 2: Smoke-test the CLI**

```go
// internal/cli/ai_test.go
package cli_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAIBackfillRejectsMissingTask(t *testing.T) {
	out := &bytes.Buffer{}
	rc := runCLI(t, []string{"ai", "backfill"}, out, t.TempDir()) // existing helper used by other cli tests
	require.NotEqual(t, 0, rc)
	require.Contains(t, out.String(), "--task is required")
}

func TestAIAcknowledgeRequiresFlag(t *testing.T) {
	out := &bytes.Buffer{}
	rc := runCLI(t, []string{"ai", "acknowledge"}, out, t.TempDir())
	require.NotEqual(t, 0, rc)
	require.Contains(t, out.String(), "--hidden-processing flag required")
	_ = strings.NewReader
}
```

(If the existing CLI test harness doesn't expose `runCLI`, mirror the pattern in `internal/cli/dispatch_test.go` and adapt.)

- [ ] **Step 3: Run tests + build**

```
go build ./...
go test ./internal/cli/... -count=1 -run TestAI -v
```

Expected: PASS.

- [ ] **Step 4: Commit**

```
git add internal/cli/ai.go internal/cli/ai_test.go internal/cli/root.go
git commit -m "feat(cli): fotobank ai status|backfill|retry-failed|acknowledge"
```


---

## Section M — Importer integration

### Task M1: Auto-enqueue on import

**Files:**
- Modify: `internal/ingest/...` — call `jobs.Queue.Enqueue` per inserted photo, `skipped.Repo.Record` per video.

The importer is AI-agnostic; the call is `aijobs.EnqueueForMedia(ctx, mediaID, []ai.Task{ai.TaskTag, ai.TaskCaption}, fp)` with the fingerprint resolved from config + prompts at boot.

- [ ] **Step 1: Identify the importer's per-media insert path**

Read `internal/ingest/`. Locate the function that does `INSERT INTO media`. The enqueue + skip-record code attaches there.

- [ ] **Step 2: Add an `AIEnqueuer` interface with no-op fallback**

```go
// internal/ingest/ai.go (new file)
package ingest

import (
	"context"

	"github.com/wesm/fotobank/internal/ai"
)

// AIEnqueuer is the surface the importer needs from the AI subsystem.
// Production wiring (cmd/fotobank-server) provides a concrete impl;
// existing import tests can pass NoopAIEnqueuer{} so they don't need
// AI tables wired.
type AIEnqueuer interface {
	EnqueueForPhoto(ctx context.Context, mediaID string) error
	RecordVideoSkip(ctx context.Context, mediaID string) error
}

// NoopAIEnqueuer satisfies AIEnqueuer with no side effects.
type NoopAIEnqueuer struct{}

func (NoopAIEnqueuer) EnqueueForPhoto(_ context.Context, _ string) error  { return nil }
func (NoopAIEnqueuer) RecordVideoSkip(_ context.Context, _ string) error { return nil }

// realAIEnqueuer is constructed at the process edge.
type realAIEnqueuer struct {
	tagFP, captionFP ai.Fingerprint
	enqueue          func(ctx context.Context, mid string, t ai.Task, fp ai.Fingerprint) error
	skip             func(ctx context.Context, mid string, t ai.Task, reason string) error
}

func NewRealAIEnqueuer(
	tagFP, captionFP ai.Fingerprint,
	enqueue func(context.Context, string, ai.Task, ai.Fingerprint) error,
	skip func(context.Context, string, ai.Task, string) error,
) AIEnqueuer {
	return &realAIEnqueuer{tagFP: tagFP, captionFP: captionFP, enqueue: enqueue, skip: skip}
}

func (r *realAIEnqueuer) EnqueueForPhoto(ctx context.Context, mid string) error {
	if err := r.enqueue(ctx, mid, ai.TaskTag, r.tagFP); err != nil {
		return err
	}
	return r.enqueue(ctx, mid, ai.TaskCaption, r.captionFP)
}
func (r *realAIEnqueuer) RecordVideoSkip(ctx context.Context, mid string) error {
	if err := r.skip(ctx, mid, ai.TaskTag, "video"); err != nil {
		return err
	}
	return r.skip(ctx, mid, ai.TaskCaption, "video")
}
```

- [ ] **Step 3: Wire `AIEnqueuer` into the importer**

Find the importer struct (likely `Importer` or similar). Add a field `AI ingest.AIEnqueuer`. In the existing successful-insert path:

```go
if mediaType == media.TypePhoto {
    if err := im.AI.EnqueueForPhoto(ctx, mediaID); err != nil {
        // log + continue; gap scanner will repair
        im.Logger.Warn("ai enqueue failed", "media", mediaID, "err", err)
    }
} else if mediaType == media.TypeVideo {
    if err := im.AI.RecordVideoSkip(ctx, mediaID); err != nil {
        im.Logger.Warn("ai skip failed", "media", mediaID, "err", err)
    }
}
```

Existing tests must still pass; if no `AI` field is set, default to `NoopAIEnqueuer{}` in the constructor.

- [ ] **Step 4: Test the integration**

```go
// internal/ingest/ai_test.go
package ingest_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/ai"
	"github.com/wesm/fotobank/internal/ai/jobs"
	"github.com/wesm/fotobank/internal/ai/skipped"
	"github.com/wesm/fotobank/internal/ingest"
	"github.com/wesm/fotobank/internal/testutil"
)

func TestEnqueueForPhotoCreatesBothTaskJobs(t *testing.T) {
	rw, ro := testutil.OpenTestDBPair(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	mid := testutil.SeedPhoto(t, rw, owner, "p1")

	q := jobs.NewQueue(rw, ro)
	skipR := skipped.NewRepo(rw, ro)
	tagFP := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}
	capFP := ai.Fingerprint{ModelID: "m", PromptVersion: "caption-v1", InputProfile: "ip"}

	enq := ingest.NewRealAIEnqueuer(tagFP, capFP,
		func(ctx context.Context, m string, t ai.Task, fp ai.Fingerprint) error { return q.Enqueue(ctx, m, t, fp) },
		func(ctx context.Context, m string, t ai.Task, reason string) error { return skipR.Record(ctx, m, t, reason) },
	)
	require.NoError(t, enq.EnqueueForPhoto(context.Background(), mid))

	c, _ := q.Counters(context.Background(), ai.TaskTag)
	require.Equal(t, 1, c.Pending)
	c, _ = q.Counters(context.Background(), ai.TaskCaption)
	require.Equal(t, 1, c.Pending)
}

func TestRecordVideoSkipSkipsBoth(t *testing.T) {
	rw, ro := testutil.OpenTestDBPair(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	mid := testutil.SeedPhoto(t, rw, owner, "p1")
	q := jobs.NewQueue(rw, ro)
	skipR := skipped.NewRepo(rw, ro)
	tagFP := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}
	capFP := ai.Fingerprint{ModelID: "m", PromptVersion: "caption-v1", InputProfile: "ip"}

	enq := ingest.NewRealAIEnqueuer(tagFP, capFP,
		func(ctx context.Context, m string, t ai.Task, fp ai.Fingerprint) error { return q.Enqueue(ctx, m, t, fp) },
		func(ctx context.Context, m string, t ai.Task, reason string) error { return skipR.Record(ctx, m, t, reason) },
	)
	require.NoError(t, enq.RecordVideoSkip(context.Background(), mid))

	tn, _ := skipR.Count(context.Background(), ai.TaskTag)
	cn, _ := skipR.Count(context.Background(), ai.TaskCaption)
	require.Equal(t, 1, tn)
	require.Equal(t, 1, cn)
}
```

- [ ] **Step 5: Run tests**

```
go test ./internal/ingest/... -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```
git add internal/ingest/
git commit -m "feat(ingest): auto-enqueue AI jobs on import; record video skips"
```

---

## Section N — Observability (Prometheus)

### Task N1: AI metric primitives

**Files:**
- Modify: `internal/obs/metrics.go` — add AI metrics

Read the existing `Metrics` struct and pattern (e.g. how `ThumbXxx` metrics are defined). Add the AI metrics in the same style.

- [ ] **Step 1: Add the metric primitives**

Append to `internal/obs/metrics.go`:

```go
// AI metrics. Cardinality is bounded:
//   task ∈ {tag, caption}
//   status ∈ {pending, working, blocked}
//   result ∈ {ok, failed, skipped}
//   outcome ∈ {ok, transient, provider_4xx, malformed}
//   kind ∈ {vision}

// AIJobsDepth observes (task, status) gauges via pull-side closure.
type AIJobsDepthFn func(task, status string) int64

// AIObserveJobOutcome increments fotobank_ai_jobs_completed_total{task,result}.
func (m *Metrics) AIObserveJobOutcome(task, result string) {
	m.set.GetOrCreateCounter(`fotobank_ai_jobs_completed_total{task="` + task + `",result="` + result + `"}`).Inc()
}

// AIObserveRequestDuration records a duration sample with outcome.
func (m *Metrics) AIObserveRequestDuration(task, outcome string, d time.Duration) {
	h := m.set.GetOrCreateHistogram(`fotobank_ai_request_duration_seconds{task="` + task + `",outcome="` + outcome + `"}`)
	h.Update(d.Seconds())
}

// AISetEndpointReachable sets fotobank_ai_endpoint_reachable{kind=...}.
func (m *Metrics) AISetEndpointReachable(kind string, reachable bool) {
	g := m.set.GetOrCreateGauge(`fotobank_ai_endpoint_reachable{kind="`+kind+`"}`, nil)
	if reachable {
		g.Set(1)
	} else {
		g.Set(0)
	}
}

// AISetAcknowledgementRequired sets the global gauge.
func (m *Metrics) AISetAcknowledgementRequired(b bool) {
	g := m.set.GetOrCreateGauge(`fotobank_ai_acknowledgement_required`, nil)
	if b {
		g.Set(1)
	} else {
		g.Set(0)
	}
}
```

Also add `AIJobsDepth AIJobsDepthFn` to `MetricSources` (the existing pull-side struct) and register a closure in the scrape func that resolves to:

```go
fotobank_ai_jobs_depth{task=...,status=...} <value>
```

per `(task, status)` pair.

- [ ] **Step 2: Test that the metrics are registered**

Add a small check to `internal/obs/metrics_test.go` (or create one if absent):

```go
func TestAIMetricsRegistered(t *testing.T) {
	m := obs.NewMetrics(obs.BuildInfo{}, obs.MetricSources{
		AIJobsDepth: func(task, status string) int64 { return 0 },
	})
	m.AIObserveJobOutcome("tag", "ok")
	m.AIObserveRequestDuration("caption", "ok", 250*time.Millisecond)
	m.AISetEndpointReachable("vision", true)
	m.AISetAcknowledgementRequired(false)

	out := &bytes.Buffer{}
	m.WritePrometheus(out)
	require.Contains(t, out.String(), `fotobank_ai_jobs_completed_total`)
	require.Contains(t, out.String(), `fotobank_ai_request_duration_seconds`)
	require.Contains(t, out.String(), `fotobank_ai_endpoint_reachable`)
}
```

(Adjust API names to match the existing `obs.NewMetrics` constructor signature.)

- [ ] **Step 3: Run tests**

```
go test ./internal/obs/... -count=1
```

Expected: PASS.

- [ ] **Step 4: Commit**

```
git add internal/obs/metrics.go internal/obs/metrics_test.go
git commit -m "feat(obs): AI Prometheus metrics (depth gauge, completion counter, latency, reachable, ack)"
```

---

## Section O — Server wiring

### Task O1: Wire workers, lease sweep, gap-scan tick

**Files:**
- Modify: `internal/cli/server.go` — instantiate gateway, semaphore, workers, gap scanner; start them under the existing graceful-shutdown context

Read `internal/cli/server.go` to understand the existing daemon wiring (how `thumb.NewWorker` / `shareworker.New` are constructed and started, how the context graceful-shutdown plumbing flows).

- [ ] **Step 1: Compute fingerprints at boot**

In the relevant section of `server.go`, after config is loaded:

```go
import (
    "github.com/wesm/fotobank/internal/ai"
    "github.com/wesm/fotobank/internal/ai/ack"
    "github.com/wesm/fotobank/internal/ai/failures"
    "github.com/wesm/fotobank/internal/ai/gapscanner"
    "github.com/wesm/fotobank/internal/ai/gateway"
    "github.com/wesm/fotobank/internal/ai/imginput"
    "github.com/wesm/fotobank/internal/ai/jobs"
    aiprompts "github.com/wesm/fotobank/internal/ai/prompts"
    "github.com/wesm/fotobank/internal/ai/results"
    "github.com/wesm/fotobank/internal/ai/skipped"
    aiworker "github.com/wesm/fotobank/internal/ai/worker"
    aiservice "github.com/wesm/fotobank/internal/service/ai"
)

tagP := aiprompts.Tag()
capP := aiprompts.Caption()
tagFP := ai.Fingerprint{ModelID: cfg.AI.Tag.Model, PromptVersion: tagP.Version, InputProfile: imginput.ProfileV1}
capFP := ai.Fingerprint{ModelID: cfg.AI.Caption.Model, PromptVersion: capP.Version, InputProfile: imginput.ProfileV1}
```

- [ ] **Step 2: Construct collaborators**

```go
queue := jobs.NewQueue(rw, ro)
resR := results.NewRepo(rw, ro)
failR := failures.NewRepo(rw, ro)
skipR := skipped.NewRepo(rw, ro)
ackS := ack.New(rw, ro)
gs := gapscanner.New(rw, ro, queue, resR, skipR)

aiSvc := aiservice.New(aiservice.Deps{
    Queue: queue, Results: resR, Failures: failR, Skipped: skipR, Ack: ackS, Gap: gs,
    ConfigFingerprints: aiservice.ConfigFingerprints{Tag: tagFP, Caption: capFP},
})

var gw gateway.VisionGateway
var probe aiservice.Probe = stubProbe{} // a small struct returning errors.New("ai disabled")
if cfg.AI.Enabled {
    real := gateway.NewOpenAICompatible(gateway.OpenAIConfig{
        Endpoint:   cfg.AI.Vision.Endpoint,
        APIKey:     cfg.AI.Vision.APIKey(),
        Timeout:    cfg.AI.Vision.Timeout,
        MaxRetries: cfg.AI.Vision.MaxRetries,
    })
    gw = real
    probe = realProbe{c: real}
}

// Inject into httpapi.Deps:
httpDeps.AIService = aiSvc
httpDeps.AIVisionProbe = probe
httpDeps.AIEnabled = cfg.AI.Enabled
```

- [ ] **Step 3: Start workers if enabled**

```go
if cfg.AI.Enabled {
    sem := aiworker.NewVisionSemaphore(cfg.AI.Vision.MaxInflight)
    locator := imginput.LocateFunc(func(id string) string {
        return storage.PreviewPath(cfg, id) // or whatever the existing helper is named
    })
    img := imginput.NewResolver(rw, ro, locator)
    ownerLookup := func(ctx context.Context, mediaID string) (owners.Principal, error) {
        var hub, user string
        row := ro.QueryRowContext(ctx, `SELECT owner_hub, owner_user_id FROM media WHERE id=?`, mediaID)
        if err := row.Scan(&hub, &user); err != nil {
            return owners.Principal{}, err
        }
        return owners.Principal{Hub: hub, UserID: user}, nil
    }

    if cfg.AI.Tag.Enabled {
        tagW := aiworker.New(aiworker.Config{
            Task: ai.TaskTag, Fingerprint: tagFP, PromptHash: tagP.Hash, PromptText: tagP.Text,
            Gateway: gw, Image: img,
            Queue: queue, Results: resR, Failures: failR, Skipped: skipR,
            Acknowledged: func(ctx context.Context, p owners.Principal) (bool, error) { return ackS.IsAcknowledged(ctx, p) },
            OwnerOf: ownerLookup,
            MaxJobAttempts: 2, BatchSize: cfg.AI.Tag.WorkerConcurrency,
            Process: aiworker.TagProcess, Sem: sem,
        })
        go func() { _ = tagW.Run(ctx) }()
    }
    if cfg.AI.Caption.Enabled {
        capW := aiworker.New(aiworker.Config{
            Task: ai.TaskCaption, Fingerprint: capFP, PromptHash: capP.Hash, PromptText: capP.Text,
            Gateway: gw, Image: img,
            Queue: queue, Results: resR, Failures: failR, Skipped: skipR,
            Acknowledged: func(ctx context.Context, p owners.Principal) (bool, error) { return ackS.IsAcknowledged(ctx, p) },
            OwnerOf: ownerLookup,
            MaxJobAttempts: 2, BatchSize: cfg.AI.Caption.WorkerConcurrency,
            Process: aiworker.CaptionProcess, Sem: sem,
        })
        go func() { _ = capW.Run(ctx) }()
    }

    // Lease sweep + gap-scan periodic tick.
    go runAIBackground(ctx, queue, gs, tagFP, capFP, cfg)
}
```

```go
func runAIBackground(ctx context.Context, q *jobs.Queue, gs *gapscanner.Scanner, tagFP, capFP ai.Fingerprint, cfg *config.Config) {
    sweepT := time.NewTicker(time.Minute)
    gapT := time.NewTicker(15 * time.Minute)
    defer sweepT.Stop()
    defer gapT.Stop()
    for {
        select {
        case <-ctx.Done():
            return
        case <-sweepT.C:
            _, _ = q.SweepLeases(ctx, 10*time.Minute)
        case <-gapT.C:
            if cfg.AI.Tag.Enabled {
                _, _ = gs.Scan(ctx, gapscanner.ScanRequest{Task: ai.TaskTag, Fingerprint: tagFP, Limit: 200})
            }
            if cfg.AI.Caption.Enabled {
                _, _ = gs.Scan(ctx, gapscanner.ScanRequest{Task: ai.TaskCaption, Fingerprint: capFP, Limit: 200})
            }
        }
    }
}
```

- [ ] **Step 4: Wire importer enqueuer**

Where the importer is constructed in `server.go`, pass the real enqueuer:

```go
importer.AI = ingest.NewRealAIEnqueuer(tagFP, capFP,
    queue.Enqueue, skipR.Record,
)
```

- [ ] **Step 5: Build + smoke**

```
go build ./...
go test ./... -short -count=1
```

Expected: full short test suite still passes.

- [ ] **Step 6: Commit**

```
git add internal/cli/server.go
git commit -m "feat(server): wire AI workers, lease sweep, gap-scan tick, importer enqueuer"
```

---

## Section P — Frontend API client + types

### Task P1: Generated schema + AI typed wrappers

**Files:**
- Modify: `frontend/src/lib/api/generated/schema.ts` — regenerated by `make api-generate`
- Create: `frontend/src/lib/ai/types.ts`
- Create: `frontend/src/lib/ai/client.ts`
- Create: `frontend/src/lib/ai/client.test.ts`

The previous task's `make api-generate` already updated `schema.ts`. This task adds typed wrappers around the generated paths and a small store-friendly type surface.

- [ ] **Step 1: Define the SPA-facing types**

```ts
// frontend/src/lib/ai/types.ts
export type AITask = "tag" | "caption";

export type AIPausedReason = "" | "config_disabled" | "acknowledgement_required";

export interface AIVisionPart {
  reachable: boolean;
  last_check_at: string;
  last_error?: string;
}

export interface AITaskPart {
  active_fingerprint: string;
  pending: number;
  working: number;
  blocked: number;
  failed_active: number;
  skipped: number;
  done: number;
  throughput_per_min: number;
  last_completed_at?: string;
}

export interface AIHealth {
  enabled: boolean;
  paused_reason: AIPausedReason;
  vision: AIVisionPart;
  tag: AITaskPart;
  caption: AITaskPart;
}

export interface AIFailureRow {
  media_id: string;
  last_error: string;
  last_error_kind: string;
  attempt_count: number;
  failed_at: string;
}

export type AIDotState = "idle" | "backlog" | "failing" | "unreachable" | "paused" | "hidden";

export interface AIDotInfo {
  state: AIDotState;
  label: string; // aria-label
}
```

- [ ] **Step 2: Write the wrappers**

```ts
// frontend/src/lib/ai/client.ts
import type { AIHealth, AIFailureRow, AITask } from "./types";

const baseHeaders = { "Content-Type": "application/json" };

export async function getAIHealth(): Promise<AIHealth> {
  const r = await fetch("/api/v1/ai/health");
  if (!r.ok) throw new Error(`/ai/health ${r.status}`);
  return (await r.json()) as AIHealth;
}

export async function listAIFailures(task: AITask, limit = 5): Promise<AIFailureRow[]> {
  const r = await fetch(`/api/v1/ai/failures?task=${task}&limit=${limit}`);
  if (!r.ok) throw new Error(`/ai/failures ${r.status}`);
  const body = (await r.json()) as { rows: AIFailureRow[] | null };
  return body.rows ?? [];
}

export async function backfillAI(task: AITask, opts?: { force?: boolean }): Promise<{ enqueued: number }> {
  const r = await fetch("/api/v1/ai/backfill", {
    method: "POST", headers: baseHeaders,
    body: JSON.stringify({ task, force: opts?.force ?? false, scope: "all" }),
  });
  if (!r.ok) throw new Error(`/ai/backfill ${r.status}`);
  return (await r.json()) as { enqueued: number };
}

export async function retryFailedAI(task: AITask): Promise<{ enqueued: number }> {
  const r = await fetch("/api/v1/ai/retry-failed", {
    method: "POST", headers: baseHeaders, body: JSON.stringify({ task }),
  });
  if (!r.ok) throw new Error(`/ai/retry-failed ${r.status}`);
  return (await r.json()) as { enqueued: number };
}

export async function retryPhotoAI(mediaId: string, task: AITask): Promise<void> {
  const r = await fetch("/api/v1/ai/retry-photo", {
    method: "POST", headers: baseHeaders,
    body: JSON.stringify({ media_id: mediaId, task }),
  });
  if (!r.ok) throw new Error(`/ai/retry-photo ${r.status}`);
}

export async function acknowledgeHiddenProcessing(): Promise<void> {
  const r = await fetch("/api/v1/ai/acknowledge", {
    method: "POST", headers: baseHeaders,
    body: JSON.stringify({ kind: "hidden_processing" }),
  });
  if (!r.ok) throw new Error(`/ai/acknowledge ${r.status}`);
}
```

- [ ] **Step 3: Unit-test the wrappers**

```ts
// frontend/src/lib/ai/client.test.ts
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { backfillAI, getAIHealth, retryPhotoAI } from "./client";

const mockFetch = vi.fn();

beforeEach(() => {
  vi.stubGlobal("fetch", mockFetch);
  mockFetch.mockReset();
});
afterEach(() => vi.unstubAllGlobals());

describe("getAIHealth", () => {
  it("returns parsed JSON", async () => {
    mockFetch.mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        enabled: true, paused_reason: "",
        vision: { reachable: true, last_check_at: "2026-04-30T18:42:11Z" },
        tag: { active_fingerprint: "m|tags-v1|ip", pending: 0, working: 0, blocked: 0, failed_active: 0, skipped: 0, done: 0, throughput_per_min: 0 },
        caption: { active_fingerprint: "m|caption-v1|ip", pending: 0, working: 0, blocked: 0, failed_active: 0, skipped: 0, done: 0, throughput_per_min: 0 },
      }),
    });
    const h = await getAIHealth();
    expect(h.tag.active_fingerprint).toBe("m|tags-v1|ip");
  });
});

describe("backfillAI", () => {
  it("posts force=true correctly", async () => {
    mockFetch.mockResolvedValueOnce({ ok: true, json: async () => ({ enqueued: 5 }) });
    const out = await backfillAI("tag", { force: true });
    expect(out.enqueued).toBe(5);
    const args = mockFetch.mock.calls[0];
    expect(args[0]).toBe("/api/v1/ai/backfill");
    expect(JSON.parse(args[1].body as string)).toEqual({ task: "tag", force: true, scope: "all" });
  });
});

describe("retryPhotoAI", () => {
  it("404s surface as errors", async () => {
    mockFetch.mockResolvedValueOnce({ ok: false, status: 404 });
    await expect(retryPhotoAI("m1", "tag")).rejects.toThrow(/404/);
  });
});
```

- [ ] **Step 4: Run tests**

```
cd frontend && bun run test src/lib/ai/client.test.ts
```

Expected: PASS.

- [ ] **Step 5: Commit**

```
git add frontend/src/lib/ai/ frontend/src/lib/api/generated/
git commit -m "feat(frontend/ai): typed wrappers for /api/v1/ai/* + types"
```


---

## Section Q — Frontend health store + SSE wiring

### Task Q1: Reactive AI health store

**Files:**
- Create: `frontend/src/lib/ai/health.ts`
- Create: `frontend/src/lib/ai/health.test.ts`
- Modify: existing SSE handler module — handle `ai.tag.completed`, `ai.caption.completed`, `ai.health.changed` by triggering `aiHealthStore.refresh()`.

- [ ] **Step 1: Find the SSE handler**

Read `frontend/src/lib/events/` for the existing `EventSource` wiring. Identify the dispatcher that routes named events to subscribers.

- [ ] **Step 2: Create the health store**

```ts
// frontend/src/lib/ai/health.ts
import { writable, derived, get, type Readable } from "svelte/store";
import { getAIHealth } from "./client";
import type { AIDotInfo, AIHealth } from "./types";

const internal = writable<AIHealth | null>(null);

let inflight: Promise<void> | null = null;

export const aiHealth: Readable<AIHealth | null> = { subscribe: internal.subscribe };

export async function refreshAIHealth(): Promise<void> {
  if (inflight) return inflight;
  inflight = (async () => {
    try {
      const h = await getAIHealth();
      internal.set(h);
    } finally {
      inflight = null;
    }
  })();
  return inflight;
}

const FAILING_THRESHOLD = 10;
const BACKLOG_THRESHOLD = 1000;

export const aiDotInfo: Readable<AIDotInfo> = derived(aiHealth, ($h) => deriveDot($h));

export function deriveDot(h: AIHealth | null): AIDotInfo {
  if (!h) return { state: "hidden", label: "" };
  if (h.paused_reason === "config_disabled") return { state: "hidden", label: "" };
  if (h.paused_reason === "acknowledgement_required")
    return { state: "paused", label: "AI paused — acknowledgement required" };
  if (!h.vision.reachable) return { state: "unreachable", label: "AI endpoint unreachable" };
  const failed = h.tag.failed_active + h.caption.failed_active;
  if (failed > FAILING_THRESHOLD) return { state: "failing", label: "AI failures need attention" };
  const pending = h.tag.pending + h.caption.pending;
  if (pending > BACKLOG_THRESHOLD) return { state: "backlog", label: "AI backlog" };
  return { state: "idle", label: "AI healthy" };
}

// Test helper: read current value synchronously.
export function snapshotHealth(): AIHealth | null {
  return get(internal);
}
```

- [ ] **Step 3: Test the derivation**

```ts
// frontend/src/lib/ai/health.test.ts
import { describe, it, expect } from "vitest";
import { deriveDot } from "./health";
import type { AIHealth } from "./types";

const base: AIHealth = {
  enabled: true,
  paused_reason: "",
  vision: { reachable: true, last_check_at: "2026-04-30T18:42:11Z" },
  tag:     { active_fingerprint: "fp", pending: 0, working: 0, blocked: 0, failed_active: 0, skipped: 0, done: 0, throughput_per_min: 0 },
  caption: { active_fingerprint: "fp", pending: 0, working: 0, blocked: 0, failed_active: 0, skipped: 0, done: 0, throughput_per_min: 0 },
};

describe("deriveDot", () => {
  it("hides when null", () => expect(deriveDot(null).state).toBe("hidden"));
  it("hides when config_disabled", () => {
    expect(deriveDot({ ...base, paused_reason: "config_disabled" }).state).toBe("hidden");
  });
  it("paused on ack required", () => {
    expect(deriveDot({ ...base, paused_reason: "acknowledgement_required" }).state).toBe("paused");
  });
  it("unreachable when vision not reachable", () => {
    expect(deriveDot({ ...base, vision: { ...base.vision, reachable: false } }).state).toBe("unreachable");
  });
  it("failing when threshold exceeded", () => {
    expect(deriveDot({ ...base, tag: { ...base.tag, failed_active: 11 } }).state).toBe("failing");
  });
  it("backlog when pending exceeds threshold", () => {
    expect(deriveDot({ ...base, tag: { ...base.tag, pending: 1001 } }).state).toBe("backlog");
  });
  it("idle otherwise", () => expect(deriveDot(base).state).toBe("idle"));
});
```

- [ ] **Step 4: Wire SSE → refresh**

In the existing SSE dispatcher, add handlers:

```ts
// approximate; adapt to existing dispatcher API
import { refreshAIHealth } from "$lib/ai/health";

eventBus.on("ai.tag.completed", () => void refreshAIHealth());
eventBus.on("ai.caption.completed", () => void refreshAIHealth());
eventBus.on("ai.health.changed", () => void refreshAIHealth());
```

- [ ] **Step 5: Run tests**

```
cd frontend && bun run test src/lib/ai/health.test.ts
```

Expected: PASS.

- [ ] **Step 6: Commit**

```
git add frontend/src/lib/ai/health.ts frontend/src/lib/ai/health.test.ts frontend/src/lib/events/
git commit -m "feat(frontend/ai): reactive AI health store + SSE-driven refresh"
```

---

## Section R — Frontend components

### Task R1: AI status dot (shell strip)

**Files:**
- Create: `frontend/src/lib/components/AIStatusDot.svelte`
- Create: `frontend/src/lib/components/AIStatusDot.test.ts`
- Modify: `frontend/src/lib/components/AppHeader.svelte` — mount the dot and call `refreshAIHealth()` on first render

- [ ] **Step 1: Component**

```svelte
<!-- frontend/src/lib/components/AIStatusDot.svelte -->
<script lang="ts">
  import { onMount } from "svelte";
  import { aiDotInfo, refreshAIHealth } from "$lib/ai/health";
  import type { AIDotState } from "$lib/ai/types";

  onMount(() => { void refreshAIHealth(); });

  const colorByState: Record<AIDotState, string> = {
    idle: "var(--ok, #22c55e)",
    backlog: "var(--warn, #facc15)",
    failing: "var(--danger-soft, #f97316)",
    unreachable: "var(--danger, #ef4444)",
    paused: "var(--warn, #facc15)",
    hidden: "transparent",
  };

  const iconByState: Record<AIDotState, string> = {
    idle: "✓",
    backlog: "≡",
    failing: "!",
    unreachable: "⏚",
    paused: "‖",
    hidden: "",
  };
</script>

{#if $aiDotInfo.state !== "hidden"}
  <a
    href="/settings/ai"
    aria-label={$aiDotInfo.label}
    title={$aiDotInfo.label}
    style:--dot-color={colorByState[$aiDotInfo.state]}
    class="ai-dot"
    data-state={$aiDotInfo.state}
  >
    <span class="dot" aria-hidden="true"></span>
    <span class="icon" aria-hidden="true">{iconByState[$aiDotInfo.state]}</span>
  </a>
{/if}

<style>
  .ai-dot {
    display: inline-flex;
    align-items: center;
    gap: 2px;
    text-decoration: none;
    color: inherit;
  }
  .dot {
    display: inline-block;
    width: 8px;
    height: 8px;
    border-radius: 50%;
    background: var(--dot-color);
  }
  .icon {
    font-size: 9px;
    color: var(--text-muted);
  }
</style>
```

- [ ] **Step 2: Test the component**

```ts
// frontend/src/lib/components/AIStatusDot.test.ts
import { render } from "@testing-library/svelte";
import { describe, it, expect, vi } from "vitest";
import AIStatusDot from "./AIStatusDot.svelte";
import * as health from "$lib/ai/health";

vi.mock("$lib/ai/health", async () => {
  const { writable, derived } = await import("svelte/store");
  const aiDotInfo = writable({ state: "idle" as const, label: "AI healthy" });
  return {
    aiDotInfo,
    refreshAIHealth: vi.fn().mockResolvedValue(undefined),
    deriveDot: () => ({ state: "idle", label: "AI healthy" }),
    aiHealth: derived(aiDotInfo, ($) => null),
  };
});

describe("AIStatusDot", () => {
  it("renders a link with aria-label", () => {
    const { container } = render(AIStatusDot);
    const link = container.querySelector("a.ai-dot")!;
    expect(link).toBeTruthy();
    expect(link.getAttribute("aria-label")).toBe("AI healthy");
    expect(link.getAttribute("data-state")).toBe("idle");
  });
});
```

- [ ] **Step 3: Mount in AppHeader**

In `frontend/src/lib/components/AppHeader.svelte`, import `AIStatusDot` and mount it adjacent to the existing identity stub. Don't move the search input — append to the right cluster.

- [ ] **Step 4: Run tests**

```
cd frontend && bun run test src/lib/components/AIStatusDot.test.ts
```

Expected: PASS.

- [ ] **Step 5: Commit**

```
git add frontend/src/lib/components/AIStatusDot.svelte frontend/src/lib/components/AIStatusDot.test.ts frontend/src/lib/components/AppHeader.svelte
git commit -m "feat(frontend): AIStatusDot in shell strip"
```

---

### Task R2: Lightbox AI section

**Files:**
- Create: `frontend/src/lib/lightbox/components/LightboxAI.svelte`
- Create: `frontend/src/lib/lightbox/components/LightboxAI.test.ts`
- Modify: `frontend/src/lib/lightbox/components/LightboxMetadata.svelte` — mount `LightboxAI` after the existing Capture/Location sections

The component takes a `mediaId` prop, fetches its tags + caption via the AI media-detail endpoint (described next), and renders chips + caption + provenance + per-photo retry.

- [ ] **Step 1: Add a "media AI detail" backend endpoint**

The lightbox needs the AI artifacts for one photo. Add a small route to `internal/httpapi/ai.go` and a service method.

In `internal/service/ai/service.go`:

```go
// MediaAIView is the lightbox-facing artifact bundle for a media.
type MediaAIView struct {
    Tags          []TagView           `json:"tags,omitempty"`
    Caption       *CaptionView        `json:"caption,omitempty"`
    Skipped       *SkippedView        `json:"skipped,omitempty"`
    TagFailure    *MediaFailureView   `json:"tag_failure,omitempty"`
    CaptionFailure *MediaFailureView  `json:"caption_failure,omitempty"`
}

type TagView struct {
    Key   string `json:"key"`
    Label string `json:"label"`
    Rank  int    `json:"rank"`
}

type CaptionView struct {
    Text          string    `json:"text"`
    ModelID       string    `json:"model_id"`
    PromptVersion string    `json:"prompt_version"`
    GeneratedAt   time.Time `json:"generated_at"`
}

type SkippedView struct {
    Reason string `json:"reason"`
}

type MediaFailureView struct {
    Kind    string `json:"kind"`
    Message string `json:"message"`
}

// MediaView returns the AI artifacts for one media. Caller must be the
// owner; not enforced here in v1 (single principal), but the read joins
// through media.id so a future caller-vs-owner check has a place.
func (s *Service) MediaView(ctx context.Context, caller owners.Principal, mediaID string) (MediaAIView, error) {
    // Tags
    tags, err := s.deps.Results.GetActiveTags(ctx, mediaID)
    if err != nil {
        return MediaAIView{}, fmt.Errorf("tags: %w", err)
    }
    out := MediaAIView{}
    for _, t := range tags {
        out.Tags = append(out.Tags, TagView{Key: t.Key, Label: t.Label, Rank: t.Rank})
    }
    // Caption
    if cap, found, err := s.deps.Results.GetActiveCaption(ctx, mediaID); err != nil {
        return MediaAIView{}, fmt.Errorf("caption: %w", err)
    } else if found {
        out.Caption = &CaptionView{
            Text: cap.Text, ModelID: cap.ModelID,
            PromptVersion: cap.PromptVersion, GeneratedAt: cap.GeneratedAt,
        }
    }
    // Skipped
    if reason, found, err := s.deps.Skipped.Get(ctx, mediaID, ai.TaskTag); err == nil && found {
        out.Skipped = &SkippedView{Reason: reason}
    }
    // Per-photo failures (current fingerprint).
    if rows, err := s.deps.Failures.ListForFingerprint(ctx, ai.TaskTag, s.deps.ConfigFingerprints.Tag, 1000); err == nil {
        for _, r := range rows {
            if r.MediaID == mediaID {
                out.TagFailure = &MediaFailureView{Kind: string(r.LastErrorKind), Message: r.LastError}
                break
            }
        }
    }
    if rows, err := s.deps.Failures.ListForFingerprint(ctx, ai.TaskCaption, s.deps.ConfigFingerprints.Caption, 1000); err == nil {
        for _, r := range rows {
            if r.MediaID == mediaID {
                out.CaptionFailure = &MediaFailureView{Kind: string(r.LastErrorKind), Message: r.LastError}
                break
            }
        }
    }
    return out, nil
}
```

In `internal/httpapi/ai.go`, register:

```go
huma.Register(api, huma.Operation{
    OperationID: "ai-media-view",
    Method: http.MethodGet,
    Path: "/api/v1/media/{media_id}/ai",
}, func(ctx context.Context, in *struct {
    MediaID string `path:"media_id"`
}) (*struct{ Body aiservice.MediaAIView }, error) {
    caller, err := identity.FromContext(ctx)
    if err != nil {
        return nil, Translate(err)
    }
    v, err := svc.MediaView(ctx, caller.Principal, in.MediaID)
    if err != nil {
        return nil, Translate(err)
    }
    return &struct{ Body aiservice.MediaAIView }{Body: v}, nil
})
```

Run `make api-generate`; the regenerated TS schema picks up the new route.

- [ ] **Step 2: Add a typed client wrapper**

```ts
// frontend/src/lib/ai/client.ts (append)
import type { AIFailureRow } from "./types";

export interface AIMediaView {
  tags?: { key: string; label: string; rank: number }[];
  caption?: { text: string; model_id: string; prompt_version: string; generated_at: string };
  skipped?: { reason: string };
  tag_failure?: { kind: string; message: string };
  caption_failure?: { kind: string; message: string };
}

export async function getMediaAIView(mediaId: string): Promise<AIMediaView> {
  const r = await fetch(`/api/v1/media/${encodeURIComponent(mediaId)}/ai`);
  if (!r.ok) throw new Error(`/media/${mediaId}/ai ${r.status}`);
  return (await r.json()) as AIMediaView;
}
```

- [ ] **Step 3: Component**

```svelte
<!-- frontend/src/lib/lightbox/components/LightboxAI.svelte -->
<script lang="ts">
  import { onDestroy } from "svelte";
  import { getMediaAIView, retryPhotoAI, type AIMediaView } from "$lib/ai/client";

  export let mediaId: string;

  let view: AIMediaView | null = null;
  let loading = true;
  let error: string | null = null;

  let lastID: string | null = null;

  $: if (mediaId !== lastID) {
    lastID = mediaId;
    void load(mediaId);
  }

  async function load(id: string) {
    loading = true; error = null;
    try {
      view = await getMediaAIView(id);
    } catch (e) {
      error = (e as Error).message;
    } finally {
      loading = false;
    }
  }

  async function retry(task: "tag" | "caption") {
    if (!mediaId) return;
    await retryPhotoAI(mediaId, task);
    await load(mediaId);
  }

  function fmtDate(s: string) {
    return new Date(s).toLocaleString();
  }

  onDestroy(() => {});
</script>

{#if view?.skipped?.reason === "video"}
  <!-- Hide section entirely for video skips. -->
{:else if view?.skipped?.reason === "no_preview"}
  <section class="ai">
    <p class="muted">AI not available — preview unavailable</p>
  </section>
{:else if loading}
  <section class="ai"><p class="muted">Loading AI…</p></section>
{:else if error}
  <section class="ai"><p class="muted">AI unavailable</p></section>
{:else if view}
  <section class="ai">
    <h4 class="label">AI tags</h4>
    {#if view.tag_failure}
      <div class="failure">Tags failed · <button on:click={() => retry("tag")}>Retry</button></div>
    {:else if view.tags && view.tags.length > 0}
      <ul class="chips">
        {#each view.tags.slice(0, 12) as t}
          <li class="chip">{t.label}</li>
        {/each}
        {#if view.tags.length > 12}
          <li class="more">+{view.tags.length - 12} more</li>
        {/if}
      </ul>
    {:else}
      <p class="muted">Tags pending</p>
    {/if}

    <h4 class="label">Caption</h4>
    {#if view.caption_failure}
      <div class="failure">Caption failed · <button on:click={() => retry("caption")}>Retry</button></div>
    {:else if view.caption}
      <p class="caption">{view.caption.text}</p>
      <p class="provenance">
        <a href="/settings/ai">{view.caption.model_id}</a> · {fmtDate(view.caption.generated_at)}
      </p>
    {:else}
      <p class="muted">Caption pending</p>
    {/if}
  </section>
{/if}

<style>
  .ai { display: flex; flex-direction: column; gap: 6px; padding: 8px 0; }
  .label { font-size: 9px; letter-spacing: 0.08em; text-transform: uppercase; color: var(--text-muted); margin: 8px 0 2px; }
  .chips { list-style: none; padding: 0; margin: 0; display: flex; flex-wrap: wrap; gap: 4px; }
  .chip { padding: 2px 8px; border-radius: 10px; background: var(--bg-surface); border: 1px solid var(--border); font-size: 10px; color: var(--text-secondary); }
  .more { font-size: 10px; color: var(--text-muted); align-self: center; }
  .caption { font-size: 11px; color: var(--text-secondary); margin: 0; }
  .provenance { font-size: 9px; color: var(--text-muted); margin: 2px 0 0; }
  .provenance a { text-decoration: underline; color: inherit; }
  .failure { font-size: 11px; color: var(--danger, #ef4444); display: flex; align-items: center; gap: 6px; }
  .failure button { font-size: 10px; padding: 1px 6px; }
  .muted { font-size: 10px; color: var(--text-muted); margin: 0; }
</style>
```

- [ ] **Step 4: Test the component**

```ts
// frontend/src/lib/lightbox/components/LightboxAI.test.ts
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/svelte";
import LightboxAI from "./LightboxAI.svelte";
import * as client from "$lib/ai/client";

vi.mock("$lib/ai/client", () => ({
  getMediaAIView: vi.fn(),
  retryPhotoAI: vi.fn(),
}));

beforeEach(() => {
  vi.mocked(client.getMediaAIView).mockReset();
  vi.mocked(client.retryPhotoAI).mockReset();
});
afterEach(() => vi.restoreAllMocks());

describe("LightboxAI", () => {
  it("renders tag chips and caption with provenance", async () => {
    vi.mocked(client.getMediaAIView).mockResolvedValueOnce({
      tags: [
        { key: "dog", label: "Dog", rank: 1 },
        { key: "beach", label: "Beach", rank: 2 },
      ],
      caption: {
        text: "A small dog on a beach.", model_id: "qwen2.5-vl:3b",
        prompt_version: "caption-v1", generated_at: new Date().toISOString(),
      },
    });
    render(LightboxAI, { mediaId: "m1" });
    await waitFor(() => expect(screen.getByText("Dog")).toBeTruthy());
    expect(screen.getByText("Beach")).toBeTruthy();
    expect(screen.getByText(/A small dog on a beach/)).toBeTruthy();
    const link = screen.getByText("qwen2.5-vl:3b").closest("a")!;
    expect(link.getAttribute("href")).toBe("/settings/ai");
  });

  it("renders failure inline with retry button", async () => {
    vi.mocked(client.getMediaAIView).mockResolvedValueOnce({
      caption_failure: { kind: "malformed", message: "bad json" },
    });
    render(LightboxAI, { mediaId: "m1" });
    await waitFor(() => expect(screen.getByText(/Caption failed/)).toBeTruthy());
  });

  it("hides entirely on video skip", async () => {
    vi.mocked(client.getMediaAIView).mockResolvedValueOnce({
      skipped: { reason: "video" },
    });
    const { container } = render(LightboxAI, { mediaId: "m1" });
    // wait one tick
    await new Promise((r) => setTimeout(r, 0));
    expect(container.querySelector("section.ai")).toBeNull();
  });
});
```

- [ ] **Step 5: Wire into LightboxMetadata**

Open `frontend/src/lib/lightbox/components/LightboxMetadata.svelte`. Import `LightboxAI` and mount it after the Capture/Location sections, keyed off the active media id:

```svelte
<LightboxAI {mediaId} />
```

- [ ] **Step 6: Run tests**

```
cd frontend && bun run test src/lib/lightbox/components/LightboxAI.test.ts
```

Expected: PASS.

- [ ] **Step 7: Commit**

```
git add internal/service/ai/service.go internal/httpapi/ai.go openapi.json frontend/src/lib/api/generated/ frontend/src/lib/ai/client.ts frontend/src/lib/lightbox/components/LightboxAI.svelte frontend/src/lib/lightbox/components/LightboxAI.test.ts frontend/src/lib/lightbox/components/LightboxMetadata.svelte
git commit -m "feat(frontend/lightbox): AI section with tags, caption, provenance, retry"
```


---

### Task R3: Settings → AI panel page

**Files:**
- Create: `frontend/src/routes/SettingsAI.svelte`
- Create: `frontend/src/routes/SettingsAI.test.ts`
- Modify: `frontend/src/lib/router/...` — register the `/settings/ai` route

- [ ] **Step 1: Register the route**

Read the existing router. Add `/settings/ai` → `SettingsAI` with the same auth posture as other Settings sub-pages.

- [ ] **Step 2: Implement the page**

```svelte
<!-- frontend/src/routes/SettingsAI.svelte -->
<script lang="ts">
  import { onMount } from "svelte";
  import { aiHealth, refreshAIHealth } from "$lib/ai/health";
  import {
    acknowledgeHiddenProcessing, backfillAI, listAIFailures, retryFailedAI,
  } from "$lib/ai/client";
  import type { AIFailureRow } from "$lib/ai/types";

  let tagFailures: AIFailureRow[] = [];
  let captionFailures: AIFailureRow[] = [];
  let busy: string | null = null;

  onMount(() => { void refreshAll(); });

  async function refreshAll() {
    await refreshAIHealth();
    [tagFailures, captionFailures] = await Promise.all([
      listAIFailures("tag", 5).catch(() => []),
      listAIFailures("caption", 5).catch(() => []),
    ]);
  }

  async function ack() {
    busy = "ack";
    try { await acknowledgeHiddenProcessing(); await refreshAll(); }
    finally { busy = null; }
  }

  async function backfill(task: "tag" | "caption") {
    busy = "backfill-" + task;
    try { await backfillAI(task); await refreshAll(); }
    finally { busy = null; }
  }

  async function retryAll(task: "tag" | "caption") {
    busy = "retry-" + task;
    try { await retryFailedAI(task); await refreshAll(); }
    finally { busy = null; }
  }
</script>

<section class="ai-panel">
  <header>
    <h2>AI</h2>
    <span class="config-locked" title="Toggle in config.toml">Enabled via config.toml</span>
  </header>

  {#if !$aiHealth}
    <p class="muted">Loading…</p>
  {:else if $aiHealth.paused_reason === "config_disabled"}
    <p class="muted">AI is disabled in config.toml.</p>
  {:else if $aiHealth.paused_reason === "acknowledgement_required"}
    <div class="ack-modal" role="dialog" aria-labelledby="ack-title">
      <h3 id="ack-title">Before AI starts processing your library</h3>
      <p>
        Hidden photos will be tagged and captioned alongside everything else.
        Outputs stay owner-only and are only visible inside the unlocked Hidden context,
        but the image bytes are sent to the configured AI endpoint.
      </p>
      <button on:click={ack} disabled={busy === "ack"}>
        {busy === "ack" ? "Acknowledging…" : "Acknowledge and start workers"}
      </button>
    </div>
  {:else}
    <div class="banner" role="note">
      <strong>Hidden photos are processed.</strong>
      Outputs stay owner-only and only show inside the unlocked Hidden context, but the image bytes are sent to the configured AI endpoint.
    </div>

    <div class="vision">
      <span class="dot" data-reachable={$aiHealth.vision.reachable} aria-hidden="true"></span>
      {$aiHealth.vision.reachable ? "Reachable" : ($aiHealth.vision.last_error || "Unreachable")}
    </div>

    <article class="task-card">
      <header>
        <strong>Tag</strong>
        <span class="meta">{$aiHealth.tag.active_fingerprint}</span>
        <button on:click={() => backfill("tag")} disabled={busy === "backfill-tag"}>
          {busy === "backfill-tag" ? "Enqueuing…" : "Backfill all"}
        </button>
      </header>
      <ul class="counters">
        <li>Done <strong>{$aiHealth.tag.done}</strong></li>
        <li>Pending <strong>{$aiHealth.tag.pending}</strong></li>
        <li>Working <strong>{$aiHealth.tag.working}</strong></li>
        <li>Skipped <strong>{$aiHealth.tag.skipped}</strong></li>
        <li>
          Failed <strong>{$aiHealth.tag.failed_active}</strong>
          {#if $aiHealth.tag.failed_active > 0}
            <button class="link" on:click={() => retryAll("tag")} disabled={busy === "retry-tag"}>retry all</button>
          {/if}
        </li>
      </ul>
    </article>

    <article class="task-card">
      <header>
        <strong>Caption</strong>
        <span class="meta">{$aiHealth.caption.active_fingerprint}</span>
        <button on:click={() => backfill("caption")} disabled={busy === "backfill-caption"}>
          {busy === "backfill-caption" ? "Enqueuing…" : "Backfill all"}
        </button>
      </header>
      <ul class="counters">
        <li>Done <strong>{$aiHealth.caption.done}</strong></li>
        <li>Pending <strong>{$aiHealth.caption.pending}</strong></li>
        <li>Working <strong>{$aiHealth.caption.working}</strong></li>
        <li>Skipped <strong>{$aiHealth.caption.skipped}</strong></li>
        <li>
          Failed <strong>{$aiHealth.caption.failed_active}</strong>
          {#if $aiHealth.caption.failed_active > 0}
            <button class="link" on:click={() => retryAll("caption")} disabled={busy === "retry-caption"}>retry all</button>
          {/if}
        </li>
      </ul>
    </article>

    {#if tagFailures.length > 0 || captionFailures.length > 0}
      <section class="failures">
        <h4>Recent failures</h4>
        <ul>
          {#each tagFailures as f}
            <li><span class="badge">tag</span> {f.media_id} — {f.last_error_kind}: {f.last_error}</li>
          {/each}
          {#each captionFailures as f}
            <li><span class="badge">caption</span> {f.media_id} — {f.last_error_kind}: {f.last_error}</li>
          {/each}
        </ul>
      </section>
    {/if}
  {/if}
</section>

<style>
  .ai-panel { padding: 16px; max-width: 720px; }
  header { display: flex; justify-content: space-between; align-items: baseline; }
  .config-locked { font-size: 11px; color: var(--text-muted); }
  .banner { padding: 10px; border-radius: 6px; background: rgba(250, 204, 21, 0.1); border: 1px solid rgba(250, 204, 21, 0.4); margin: 12px 0; font-size: 11px; }
  .ack-modal { padding: 14px; border-radius: 6px; background: var(--bg-surface); border: 1px solid var(--border); margin: 12px 0; }
  .ack-modal h3 { margin: 0 0 8px 0; font-size: 13px; }
  .vision { display: flex; align-items: center; gap: 6px; font-size: 11px; margin: 6px 0 12px; }
  .dot { width: 8px; height: 8px; border-radius: 50%; background: var(--danger, #ef4444); }
  .dot[data-reachable="true"] { background: var(--ok, #22c55e); }
  .task-card { background: var(--bg-surface); border-radius: 6px; padding: 10px 12px; margin-bottom: 10px; }
  .task-card header { gap: 8px; align-items: center; }
  .meta { font-size: 10px; color: var(--text-muted); flex: 1; }
  .counters { list-style: none; padding: 0; margin: 8px 0 0; display: grid; grid-template-columns: repeat(5, 1fr); gap: 8px; font-size: 10px; }
  .counters li { color: var(--text-muted); }
  .counters strong { color: var(--text-primary); display: block; font-size: 14px; }
  button.link { font-size: 9px; background: none; border: none; color: var(--text-secondary); text-decoration: underline; padding: 0; }
  .failures ul { list-style: none; padding: 0; font-size: 10px; line-height: 1.6; }
  .badge { display: inline-block; padding: 1px 5px; border-radius: 3px; font-size: 9px; background: var(--bg-elevated); color: var(--text-muted); margin-right: 6px; }
</style>
```

- [ ] **Step 3: Test the page**

```ts
// frontend/src/routes/SettingsAI.test.ts
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/svelte";
import { writable } from "svelte/store";
import SettingsAI from "./SettingsAI.svelte";
import type { AIHealth } from "$lib/ai/types";

const store = writable<AIHealth | null>(null);

vi.mock("$lib/ai/health", async () => ({
  aiHealth: { subscribe: store.subscribe },
  refreshAIHealth: vi.fn().mockResolvedValue(undefined),
}));

vi.mock("$lib/ai/client", () => ({
  acknowledgeHiddenProcessing: vi.fn().mockResolvedValue(undefined),
  backfillAI: vi.fn().mockResolvedValue({ enqueued: 5 }),
  listAIFailures: vi.fn().mockResolvedValue([]),
  retryFailedAI: vi.fn().mockResolvedValue({ enqueued: 3 }),
}));

const base: AIHealth = {
  enabled: true, paused_reason: "",
  vision: { reachable: true, last_check_at: "2026-04-30T18:42:11Z" },
  tag:     { active_fingerprint: "fp-tag", pending: 3, working: 1, blocked: 0, failed_active: 0, skipped: 0, done: 100, throughput_per_min: 0 },
  caption: { active_fingerprint: "fp-cap", pending: 0, working: 0, blocked: 0, failed_active: 0, skipped: 0, done: 50, throughput_per_min: 0 },
};

beforeEach(() => { store.set(null); });
afterEach(() => vi.restoreAllMocks());

describe("SettingsAI", () => {
  it("shows ack modal when paused on acknowledgement", async () => {
    store.set({ ...base, paused_reason: "acknowledgement_required" });
    render(SettingsAI);
    await waitFor(() => expect(screen.getByText(/Before AI starts processing/)).toBeTruthy());
  });

  it("hides ack modal once acknowledged", async () => {
    store.set({ ...base });
    render(SettingsAI);
    await waitFor(() => expect(screen.queryByText(/Before AI starts/)).toBeNull());
    expect(screen.getByText("Tag")).toBeTruthy();
    expect(screen.getByText("Caption")).toBeTruthy();
  });

  it("renders task counters", async () => {
    store.set({ ...base });
    render(SettingsAI);
    await waitFor(() => expect(screen.getByText("100")).toBeTruthy()); // tag done
    expect(screen.getByText("50")).toBeTruthy(); // caption done
  });
});
```

- [ ] **Step 4: Run tests**

```
cd frontend && bun run test src/routes/SettingsAI.test.ts
```

Expected: PASS.

- [ ] **Step 5: Commit**

```
git add frontend/src/routes/SettingsAI.svelte frontend/src/routes/SettingsAI.test.ts frontend/src/lib/router/
git commit -m "feat(frontend): SettingsAI panel with ack modal, task cards, failures, retry"
```

---

## Section S — End-to-end tests

### Task S1: Mock VLM endpoint + AI fixtures in e2e-server

**Files:**
- Modify: `cmd/e2e-server/main.go` — add a mock `/v1/chat/completions` and `/v1/models` handler; seed an acknowledgement; seed a known result fingerprint.

The e2e harness must not depend on a real Ollama. We embed a deterministic VLM stub in the e2e binary.

- [ ] **Step 1: Mock VLM handler**

Add to `cmd/e2e-server/main.go`:

```go
// Mock VLM endpoint for e2e. Always returns a known JSON shape so the
// frontend tests can assert against deterministic content.
func mockVLM() *http.ServeMux {
    mux := http.NewServeMux()
    mux.HandleFunc("/v1/models", func(w http.ResponseWriter, _ *http.Request) {
        _, _ = w.Write([]byte(`{"data":[{"id":"qwen2.5-vl:3b"}]}`))
    })
    mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
        var body struct {
            Messages []struct {
                Content []struct {
                    Type string `json:"type"`
                    Text string `json:"text"`
                } `json:"content"`
            } `json:"messages"`
            Model string `json:"model"`
        }
        _ = json.NewDecoder(r.Body).Decode(&body)
        // Decide tag vs caption by inspecting the prompt text.
        var content string
        if len(body.Messages) > 0 && len(body.Messages[0].Content) > 0 &&
            strings.Contains(body.Messages[0].Content[0].Text, `"tags"`) {
            content = `{"tags":["e2e-tag-a","e2e-tag-b"]}`
        } else {
            content = `{"caption":"An e2e test photo of a small dog on a beach."}`
        }
        _, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":` +
            strconv.Quote(content) + `}}]}`))
    })
    return mux
}
```

In the existing e2e bootstrap, start the mock server on a free port and override `cfg.AI.Vision.Endpoint` to point at it (`http://127.0.0.1:<port>/v1`). Set `cfg.AI.Enabled = true`, `cfg.AI.Tag.Enabled = true`, `cfg.AI.Caption.Enabled = true`.

Optionally pre-acknowledge by calling `ackS.Acknowledge` directly during seeding for tests that don't exercise the modal. For tests that DO exercise the modal, leave it unset.

- [ ] **Step 2: Commit the harness change**

```
git add cmd/e2e-server/main.go
git commit -m "test(e2e): mock OpenAI-compatible VLM and AI seeding"
```

---

### Task S2: E2E — acknowledgement gate, lightbox, panel

**Files:**
- Create: `frontend/tests/e2e/ai.spec.ts`

Mirror the structure of existing e2e specs (e.g. `frontend/tests/e2e/lightbox.spec.ts`).

- [ ] **Step 1: Write the spec**

```ts
// frontend/tests/e2e/ai.spec.ts
import { test, expect } from "@playwright/test";

test.describe("AI", () => {
  test("acknowledgement gate parks workers", async ({ page }) => {
    // Start with a fresh DB without ack pre-set.
    await page.goto("/?seed=ai_unack");
    await page.goto("/settings/ai");
    await expect(page.getByText("Before AI starts processing")).toBeVisible();
    await expect(page.getByText("Tag", { exact: true })).toHaveCount(0);
  });

  test("ack reveals task cards", async ({ page }) => {
    await page.goto("/?seed=ai_unack");
    await page.goto("/settings/ai");
    await page.getByRole("button", { name: /Acknowledge/i }).click();
    await expect(page.getByText("Tag", { exact: true })).toBeVisible();
    await expect(page.getByText("Caption", { exact: true })).toBeVisible();
  });

  test("lightbox shows tags + caption + model_id link", async ({ page }) => {
    await page.goto("/?seed=ai_done");
    await page.goto("/");
    // Open the first thumb in the lightbox.
    const firstCell = page.locator("[data-test=grid-cell]").first();
    await firstCell.click();
    await expect(page.getByText(/e2e-tag-a/)).toBeVisible();
    await expect(page.getByText(/An e2e test photo/)).toBeVisible();
    const link = page.getByText("qwen2.5-vl:3b");
    await expect(link).toBeVisible();
    await expect(link.locator("..")).toHaveAttribute("href", "/settings/ai");
  });

  test("per-photo retry from lightbox enqueues a job", async ({ page }) => {
    await page.goto("/?seed=ai_caption_failed");
    await page.goto("/");
    await page.locator("[data-test=grid-cell]").first().click();
    await expect(page.getByText(/Caption failed/)).toBeVisible();
    await page.getByRole("button", { name: /Retry/ }).click();
    // After successful retry, mock returns the deterministic caption.
    await expect(page.getByText(/An e2e test photo/)).toBeVisible({ timeout: 10000 });
  });
});
```

The `?seed=...` query string is a convention recognized by `cmd/e2e-server` to set up specific fixture states (`ai_unack`, `ai_done`, `ai_caption_failed`). Add corresponding seed branches to the e2e server's startup hook.

- [ ] **Step 2: Add seed branches to e2e-server**

In `cmd/e2e-server/main.go`, where the existing seed logic dispatches on `?seed=...`, add:

```go
case "ai_unack":
    seedSinglePhoto(rw, owner, mockVLM)
    // do NOT pre-ack
case "ai_done":
    seedSinglePhoto(rw, owner, mockVLM)
    _ = ackS.Acknowledge(ctx, owner)
    // Run a single tick of the worker synchronously for determinism.
    runOneTagAndCaption(ctx, queue, /* deps */)
case "ai_caption_failed":
    seedSinglePhoto(rw, owner, mockVLM)
    _ = ackS.Acknowledge(ctx, owner)
    failR.Record(ctx, photoID, ai.TaskCaption, captionFP, ai.ErrKindMalformed, "bad json", 2)
```

(Sketch — adapt to the existing seed-dispatch shape.)

- [ ] **Step 3: Run e2e**

```
make test-e2e
```

Expected: AI specs PASS along with the existing suite.

- [ ] **Step 4: Commit**

```
git add frontend/tests/e2e/ai.spec.ts cmd/e2e-server/main.go
git commit -m "test(e2e): AI ack gate, lightbox, retry"
```

---

## Section T — Verification pass

### Task T1: Final verification

**Files:** none (run + observe).

- [ ] **Step 1: Frontend typecheck**

```
cd frontend && bun run typecheck
```

Expected: PASS.

- [ ] **Step 2: Frontend unit tests**

```
cd frontend && bun run test
```

Expected: PASS.

- [ ] **Step 3: Backend short tests**

```
go test ./... -short -count=1
```

Expected: PASS.

- [ ] **Step 4: Lint**

```
make lint
```

Expected: PASS.

- [ ] **Step 5: Full e2e**

```
make test-e2e
```

Expected: PASS.

- [ ] **Step 6: Smoke build**

```
make build
```

Expected: `bin/fotobank` produced; no link errors.

- [ ] **Step 7: Commit any drift**

If any test required a small tweak, commit it as a single drift-cleanup:

```
git add -A
git commit -m "fix(ai): verification pass cleanup"
```

If everything is clean, no commit needed.

---

## Self-review notes (post-write)

After writing the plan, verify against the spec:

1. **Spec coverage:**
   - §3 architecture → Tasks B1, B2, C1–C2, D1, E1–E4, F1, G1–G3, O1
   - §4 configuration → Task B2 (types) + O1 (boot wiring)
   - §5 schema → Task A1 (single migration edit)
   - §6 lifecycle (states, retries, classification, supersession, sweep) → E1 (queue) + G2 (worker) + O1 (sweep tick)
   - §7 triggering / gap scanning / backfill / model swap → M1 (importer) + H1 (gap scanner) + L1 (CLI) + I1 (backfill)
   - §8 input profile → D1 (encode) + G3 (resolver, thumb-state machine)
   - §9 tag schema → D2 (parse + normalize) + E2 (results store)
   - §10 caption schema → D3 (parse + validate) + E2 (results store)
   - §11 prompt versioning → B3 (prompts, hash CI pin)
   - §12 lightbox surfacing → R2 (LightboxAI + service.MediaView + media-AI HTTP route)
   - §13 AI panel → R3 (SettingsAI page) + J1 (HTTP routes)
   - §14 health endpoint → I2 (aggregator) + J1 (route) + Q1 (dot derivation)
   - §15 SSE events → K1
   - §16 CLI → L1
   - §17 observability → N1
   - §18 identity / multi-principal forward-compat → I1 (caller principal everywhere) + F1 (per-principal ack)
   - §19 privacy & sharing rules → I1 (no grantee surface); EXIF stripping enforced by D1 (input profile)
   - §20 worker mechanics → G1 (semaphore) + G2 (claim loop with three pause gates) + O1 (sweep)
   - §21 verification gaps → addressed inline in tasks (G3 reads thumb_status; D1 acknowledges encoder limitation; J1 mounts on existing huma; K1 extends existing bus; N1 adds metrics)
   - §22 out-of-scope items → not implemented (correct)

2. **Placeholder scan:** No "TBD", "TODO", or unspecified test bodies. Tasks that reference "the existing helper" name the file to read first (e.g. `internal/cli/albums.go` for cobra patterns) so the executor has a concrete starting point.

3. **Type consistency:**
   - `ai.Fingerprint{ModelID, PromptVersion, InputProfile}` used identically across types, queue, results, failures, service, worker, gateway client.
   - `jobs.Claim{JobID, MediaID, Fingerprint, Attempts, ClaimedAt}` matches across queue and worker.
   - `aiservice.Health` shape matches the SSE-driven `aiHealth` store and the `/ai/health` route.
   - `parse.Tag{Key, Label, Rank}` matches results.WriteTagResult input + GetActiveTags output + frontend AIMediaView.tags shape.
   - `imginput.ProfileV1 = "jpeg-1024-q85-metadata-stripped-v1"` is the only string used as `input_profile` everywhere.

4. **Failure modes covered**: 4xx-immediate (G2 test), malformed-retry-then-fail (G2 test), thumb-blocked (G2 test), no-preview skip (G2 test), ack-required pause (G2 test), supersession on fingerprint change (E1 test).

5. **No backwards-compatibility cruft**: schema lands in 000001 directly per pre-alpha policy; no migration version checks; `--force` non-destructive per spec §7.4.
