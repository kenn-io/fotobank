# fotobank AI — Tagging & Captioning Design Spec

> **Status:** Brainstormed and locked 2026-04-30. Pending implementation plan.
>
> **Reference:** `~/code/msgvault/internal/vector` for the OpenAI-compatible HTTP client + config shape; `internal/thumb/` for the claim/lease worker pattern.

## 1. Goal

Add server-side AI tagging and captioning to fotobank, surfaced in the lightbox info panel and managed from a Settings → AI panel. Vision tasks only — no embeddings, no semantic search, no FTS5. The gateway, queue, worker, registry, and panel infrastructure laid down here are the substrate that a later embeddings-and-search spec will sit on top of.

## 2. Product principles

1. **Disabled by default.** Personal photos must never leave the box unless the operator explicitly enables AI and acknowledges what that implies.
2. **Sanitized image input only.** Workers send a downscaled, metadata-stripped JPEG re-encoded from the existing `preview` thumb tier. The original file never enters the AI gateway path.
3. **Provenance on every row.** Every result carries `(model_id, prompt_version, input_profile, generated_at)`. A future operator should be able to ask "what produced this caption?" and get a complete answer.
4. **Owner-only outputs.** Tags and captions never appear in grantee views or shared scopes in v1. Sharing exposure is a separate later spec.
5. **Hidden is processed, with explicit consent.** AI is opt-in for the operator's own library, hidden included, but enabling AI requires acknowledging that hidden image bytes go to the configured endpoint.
6. **No fake precision.** VLM tag confidences are not calibrated; we store rank, not score. AI output is presented as inspectable, not authoritative.
7. **One protocol.** OpenAI-compatible chat completions only. Anthropic-direct, Gemini-native, and provider-specific SDKs are out of scope; users who want them route through OpenRouter / LiteLLM.

## 3. Architecture overview

- **Single shared vision endpoint.** One `[ai.vision]` config block (URL, optional API key) feeds both task workers. Per-task model name overrides let `tag` run on a small fast VLM and `caption` on a larger one against the same Ollama / vLLM / hosted endpoint.
- **Two workers, three pause gates.** A tag worker and a caption worker each run a claim/lease loop modeled on `internal/thumb/worker.go`. Workers stay parked when (a) `[ai].enabled = false`, (b) hidden-processing acknowledgement is missing, or (c) the global `[ai.vision].max_inflight` semaphore is full. The semaphore is shared between workers — without it, `tag.worker_concurrency=1` and `caption.worker_concurrency=1` still permit two simultaneous calls oversubscribing the same backing GPU.
- **Result-set storage model.** Each successful run writes one `ai_results` row plus child rows in `media_tags` or `media_captions`. The `(media_id, task) WHERE status='active'` partial unique index enforces "at most one active result per task per photo." Reruns are atomic: the prior active row is staled and the new run promoted in one transaction.
- **Separate AI gap scanner.** Existing `internal/reconcile` keeps its disk-vs-DB job. AI gap-filling is a distinct subsystem — `internal/ai/gapscanner` — that walks media rows and enqueues missing work for the active fingerprint per task.
- **AI is hidden-aware at the disclosure layer, not at the queue layer.** The queue happily processes hidden photos; the AI panel makes the operator confront what that means before any worker claims a job.

## 4. Configuration

```toml
[ai]
enabled = false                                      # master switch — workers parked while false

[ai.vision]
endpoint     = "http://127.0.0.1:11434/v1"           # OpenAI-compatible /v1 base
api_key_env  = ""                                    # name of env var holding the bearer token; "" = no auth
timeout      = "2m"                                  # per-request HTTP timeout (VLMs are slow)
max_retries  = 2                                     # in-call retries for 429/5xx/network — does NOT bump job attempts
max_inflight = 1                                     # global semaphore across all task workers (shared GPU protection)

[ai.tag]
enabled            = true
model              = "qwen2.5-vl:3b"
worker_concurrency = 1                               # task-local cap; bounded above by ai.vision.max_inflight

[ai.caption]
enabled            = true
model              = "llama3.2-vision:11b"
worker_concurrency = 1
```

**Resolution rules:**

- `[ai].enabled = false` → all AI subsystems silent; panel shows "AI disabled in `config.toml`."
- `[ai].enabled = true` AND no acknowledgement on file → `paused_reason = "acknowledgement_required"`. Workers do not claim; endpoint health checks may run.
- Per-task `enabled = false` parks just that worker; the other continues.
- API keys live in `config.toml` via env-var name only. The SPA never sees keys; the panel shows host + model + reachable status.
- `worker_concurrency` is hard-bounded above by `ai.vision.max_inflight`. A misconfiguration where per-task caps sum higher than `max_inflight` is allowed — the semaphore enforces the global cap regardless.

## 5. Database schema

Schema lands in a **new numbered migration pair** — `internal/db/migrations/000004_ai_tag_caption.{up,down}.sql`. The pre-commit hook prohibits edits to migrations already on `main`, and the pre-prod squash-into-`000001` policy from the web-frontend design has already been superseded in practice by `000002_album_indexes` and `000003_scopes_backoff` landing as separate numbered files. AI follows that newer convention.

ID storage matches the existing schema: `media.id` is `UUID PRIMARY KEY`, `media.owner_*` and album / scope foreign keys all use `UUID` and `TEXT`. AI tables follow the same conventions — no new `BLOB` primary-key shape introduced.

```sql
-- One row per successful AI task run (or in-flight insert that gets staled on retry).
CREATE TABLE ai_results (
    id              UUID PRIMARY KEY,
    media_id        UUID NOT NULL REFERENCES media(id) ON DELETE CASCADE,
    task            TEXT NOT NULL,                                       -- 'tag' | 'caption'
    model_id        TEXT NOT NULL,                                       -- e.g. 'qwen2.5-vl:3b'
    prompt_version  TEXT NOT NULL,                                       -- e.g. 'tags-v1'
    prompt_hash     TEXT NOT NULL,                                       -- sha256 hex of the prompt const
    input_profile   TEXT NOT NULL,                                       -- e.g. 'jpeg-1024-q85-metadata-stripped-v1'
    status          TEXT NOT NULL CHECK (status IN ('active','stale')),
    generated_at    TIMESTAMP NOT NULL
);
CREATE UNIQUE INDEX ai_results_active_one_per
    ON ai_results(media_id, task) WHERE status = 'active';
CREATE INDEX ai_results_media_task_idx
    ON ai_results(media_id, task, status);

-- Tag rows belong to a result; one row per tag, ranked.
CREATE TABLE media_tags (
    result_id  UUID NOT NULL REFERENCES ai_results(id) ON DELETE CASCADE,
    tag_key    TEXT NOT NULL,                                            -- normalized for matching
    tag_label  TEXT NOT NULL,                                            -- model's exact output for display
    rank       INTEGER NOT NULL,                                         -- 1-based emission order
    PRIMARY KEY (result_id, tag_key)
);
CREATE INDEX media_tags_key_idx ON media_tags(tag_key);

-- Caption is one row per result.
CREATE TABLE media_captions (
    result_id  UUID PRIMARY KEY REFERENCES ai_results(id) ON DELETE CASCADE,
    text       TEXT NOT NULL
);

-- Job queue. Mirrors thumb's claim/lease pattern, but task-keyed in its own table
-- because there are multiple AI task types per photo (thumb has one).
CREATE TABLE ai_jobs (
    id            UUID PRIMARY KEY,
    media_id      UUID NOT NULL REFERENCES media(id) ON DELETE CASCADE,
    task          TEXT NOT NULL,
    fingerprint   TEXT NOT NULL,                                         -- '{model}|{prompt_version}|{input_profile}' at enqueue
    status        TEXT NOT NULL CHECK (
                    status IN ('pending','working','blocked','done','failed','superseded')),
    attempts      INTEGER NOT NULL DEFAULT 0,                            -- counts executions reaching provider/parser boundary
    last_error    TEXT,
    last_error_kind TEXT,                                                -- 'provider_4xx' | 'malformed' | 'transient' | 'thumb_blocked' | 'superseded' | ...
    claimed_at    TIMESTAMP,                                             -- lease anchor for crash recovery
    enqueued_at   TIMESTAMP NOT NULL,
    completed_at  TIMESTAMP                                              -- non-NULL for terminal status
);
-- Idempotency for importer / gap-scanner enqueues: at most one in-flight job per (media, task).
-- A new-fingerprint enqueue against an in-flight old-fingerprint job is handled by §6.5
-- (supersession) — the enqueue path transitions the old row to terminal `superseded` first,
-- then inserts the new pending row, so the partial unique index is honored without ambiguity.
CREATE UNIQUE INDEX ai_jobs_active_idx
    ON ai_jobs(media_id, task) WHERE status IN ('pending','working','blocked');
-- Claim ordering and panel counters.
CREATE INDEX ai_jobs_pending_idx
    ON ai_jobs(task, status, claimed_at) WHERE status IN ('pending','working','blocked');
CREATE INDEX ai_jobs_terminal_idx
    ON ai_jobs(task, status, completed_at) WHERE status IN ('done','failed','superseded');

-- Current unresolved failures, keyed by full fingerprint.
-- Successful retry deletes the matching row; old-fingerprint rows remain
-- until naturally stale-cleaned, but are ignored by current-fingerprint queries.
CREATE TABLE ai_failures (
    media_id        UUID NOT NULL REFERENCES media(id) ON DELETE CASCADE,
    task            TEXT NOT NULL,
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

-- Provenance for media that won't be queued (videos) or were skipped after claim
-- (no_preview thumb state, etc.). Keyed by (media, task) so reason updates in place.
CREATE TABLE ai_skipped (
    media_id     UUID NOT NULL REFERENCES media(id) ON DELETE CASCADE,
    task         TEXT NOT NULL,
    reason       TEXT NOT NULL,                                          -- 'video' | 'no_preview' | ...
    recorded_at  TIMESTAMP NOT NULL,
    PRIMARY KEY (media_id, task)
);
```

The matching `000004_ai_tag_caption.down.sql` drops the new tables and indexes in reverse dependency order: `ai_skipped`, `ai_failures` (and its index), `ai_jobs` (and its indexes), `media_captions`, `media_tags` (and its index), `ai_results` (and its indexes).

**Acknowledgement storage.** Hidden-processing acknowledgement is stored in the existing `user_settings` table (per the web-frontend design) under key `ai.hidden_processing_acknowledged_at`, value the ISO-8601 timestamp of acknowledgement. Per-principal; no new table. `user_settings` already exists, so no migration touch needed for it.

**Owner consistency.** Owner identity flows from `media.owner_*` — `ai_results`, `ai_jobs`, `ai_failures`, `ai_skipped` do not denormalize owner columns. Every owner check joins through `media`.

## 6. Job lifecycle

### 6.1 States

- `pending` — waiting for a worker to claim.
- `working` — claimed (`claimed_at` set; lease ~10 min). Crash-recovery sweep resets stale `working` rows to `pending` without bumping `attempts`.
- `blocked` — upstream not ready (thumb pipeline pending/working/failed). Periodic recheck promotes back to `pending`. `attempts` does not change while `blocked`.
- `done` — terminal success. `ai_results` row written, child rows written, prior active staled. Retained for counters; pruned by age (default 30 d) in a maintenance pass.
- `failed` — terminal permanent failure. Retained (default 90 d). Each terminal `failed` writes/refreshes the matching `ai_failures` row.
- `superseded` — terminal abandonment. Set by the enqueue path when a new-fingerprint job arrives for a `(media, task)` that has an old-fingerprint job in flight (§6.5). Retained for counters; pruned alongside `done` (default 30 d). Does not write an `ai_failures` row.

`skipped` is **not** a job state. Skips are recorded in `ai_skipped` either at importer time (videos) or by a worker that claimed a job and discovered an `no_preview` thumb state (after which the worker deletes the job).

### 6.2 Two-layer retries

- **HTTP-client retries (cheap, in-call).** Configured by `[ai.vision].max_retries` (default 2). Triggers on 429/5xx/network; honors `Retry-After`; exponential backoff base 5 s, cap 60 s, **with jitter**. Does not bump `ai_jobs.attempts`.
- **Job-level attempts (expensive, full re-execution).** `ai_jobs.attempts` increments only when an execution reaches the provider/parser boundary. Default cap **2** (initial + 1 retry).

### 6.3 Failure classification per execution

| Outcome | Disposition | `last_error_kind` |
|---|---|---|
| HTTP 5xx / network / timeout | Already retried by client; if still failing → transient → retry job once | `transient` |
| HTTP 429 (after client retries) | Same as 5xx | `transient` |
| HTTP 4xx (other than 429) | Permanent → terminal `failed` immediately | `provider_4xx` |
| JSON parse / schema fail | First occurrence: retry job once. Second: terminal `failed`. | `malformed` |
| Caption validation reject (markdown / empty / over-length) | Same as parse fail | `malformed` |
| Worker context cancel / shutdown | Release to `pending`; do not bump `attempts` | — |
| Thumb upstream pending / working | Mark `blocked`; do not bump `attempts` | `thumb_blocked` |
| Thumb upstream failed | Mark `blocked`; same recheck loop | `thumb_blocked` |
| Thumb upstream `ready` but blob missing | Retry up to 2 times, then terminal `failed` | `missing_ai_input` |

### 6.4 Crash recovery

A periodic lease-expiry sweep (every minute) resets `working` rows whose `claimed_at` is older than 10 min back to `pending`. The worker that died gets its work redone without bumping `attempts`. Importer crashes between media insert and job enqueue are repaired by the gap scanner — no atomic-with-media-insert requirement.

### 6.5 Supersession on fingerprint change

The partial unique index `WHERE status IN ('pending','working','blocked')` enforces "at most one in-flight job per `(media, task)`." When a new-fingerprint enqueue (importer, gap scanner, or `--force` backfill) targets a `(media, task)` that already has an in-flight job under a different fingerprint, the enqueue path runs in a single transaction:

1. Transition the old row to terminal `superseded` (`completed_at = now()`, `last_error_kind = 'superseded'`, `last_error = 'fingerprint changed'`).
2. Insert the new `pending` row with the current fingerprint.

Workers handle the race where their claim is superseded mid-call via the existing claim-token pattern (mirrors `internal/thumb`'s `ErrClaimLost`): the result-write SQL filters on `WHERE id = ? AND claimed_at = ? AND status = 'working'`. If the row is now `superseded`, zero rows match; the worker logs and discards its provider response cleanly. Cost of a wasted provider call after a fingerprint flip is accepted as a rare edge case, not engineered around.

`superseded` jobs do not increment `attempts`, do not write `ai_failures`, and are filtered out of all "current-fingerprint" panel queries. They exist purely so the unique index stays honest.

## 7. Triggering, gap scanning, backfill

### 7.1 Auto-enqueue on import

The importer enqueues one `ai_jobs` row per task per media at import time, **only when** the photo's media type is image. Videos and other non-photo types write `ai_skipped(reason='video')` rows instead. The importer is AI-provider-agnostic: it calls `aijobs.EnqueueForMedia(ctx, mediaID, []TaskType{Tag, Caption})` and the queue layer fills in the active fingerprint. Enqueue is idempotent via the partial unique index — a re-import of the same media does not duplicate jobs.

### 7.2 AI gap scanner

A separate subsystem in `internal/ai/gapscanner`:

- **One-shot CLI:** `fotobank ai backfill --task=tag,caption [--media-id=ID … | --all]` walks media missing an active result for the active fingerprint and bulk-enqueues. Pre-confirms with an estimate (`47,381 photos · ~8 h at 1 photo/sec`) before writing.
- **Periodic server-side tick** (every 15 min by default; configurable later): same query as the CLI, but bounded to a small batch per tick. Repairs importer crash windows, model swaps mid-run, and any drift the importer missed.

The gap scanner is **not** the disk reconciler; the disk reconciler stays scoped to disk-vs-DB drift.

### 7.3 Hidden photos

Processed alongside everything else. Outputs are owner-only and only surface in the unlocked Hidden context. The `ai backfill --all` and the periodic gap scanner do not exclude hidden by default. The disclosure surfaces (§13) are how the operator confronts this before workers run.

### 7.4 Model / prompt swap behavior

Bumping `model`, `prompt_version`, or `input_profile` does **not** auto-trigger re-run. It changes the active fingerprint; existing rows under the old fingerprint remain `active` until the gap scanner / explicit backfill encounters them missing for the new fingerprint and enqueues new jobs. On success of those new jobs, the old `active` row is staled and the new is promoted in one transaction (per §3 result-set invariant).

Force-rerun under the same fingerprint is supported via `fotobank ai backfill --force --task=…`. `--force` does **not** delete existing `active` results upfront — that would leave the photo with no AI output if the rerun fails. Instead, `--force` widens the gap-scanner predicate to include media that already have an active result for the current fingerprint, enqueueing jobs for them anyway. On success, the prior `active` is staled and the new is promoted atomically (same path as §3). On failure, the prior `active` stays — same as any other run.

## 8. Input profile

What the worker actually sends to the VLM:

- **Format:** JPEG.
- **Max long edge:** 1024 px.
- **Quality:** 85.
- **Color:** strip ICC profile during re-encode. **Not** color-managed sRGB conversion in v1 — Go's pure-Go JPEG path strips the profile but does not convert AdobeRGB / Display-P3 to sRGB. The provenance string is honest about this:

  ```
  jpeg-1024-q85-metadata-stripped-v1
  ```

  If we later add real ICC handling, the new profile becomes `jpeg-1024-q85-srgb-v2`.
- **EXIF / metadata:** stripped.
- **Source:** re-encode from the `preview` thumb tier (2560 px JPEG) on each job claim. No persistent AI-input blob — bytes are reproducible from `(media_id, thumb_version, input_profile)`.
- **Dependency state machine** (worker consults `media.thumb_status` at claim time):
  - `thumb_status ∈ {pending, working}` → mark `blocked`, recheck periodically.
  - `thumb_status = no_preview` → write `ai_skipped(reason='no_preview')`, delete the job.
  - `thumb_status = failed` → mark `blocked`; worker retries when thumb regenerates.
  - `thumb_status = ready` but the `preview` (and `large` fallback) blob is missing → retry up to 2 times, then terminal `failed:missing_ai_input` (storage drift signal).
- **Wire format:** OpenAI chat completions content part `{"type": "image_url", "image_url": {"url": "data:image/jpeg;base64,..."}}`.

## 9. Tag schema

### 9.1 Output contract

- **Open vocabulary, free-text.** Prompt asks for `{"tags": ["...", "..."]}`. Structured-output / JSON mode is **not** assumed — the parser is defensive.
- **Top-K = 10 emitted.** No score filter (VLM tag confidences are not calibrated; storing them invites fake precision).
- **Rank only.** Emission order is the rank signal; first tag = `rank = 1`. No `score REAL` column.
- **No `candidate` status in v1.** Status is just `active` / `stale`. If a future provider gives calibrated scores worth trusting, a `candidate` status is added then.

### 9.2 Normalization

`tag_key` is the matching key; `tag_label` is the model's exact output. The normalization pipeline:

1. Trim leading/trailing whitespace.
2. NFKC Unicode normalize.
3. Lowercase.
4. Collapse internal whitespace runs to a single space.
5. Strip leading/trailing punctuation only (not internal).
6. Remove emoji and control chars.
7. Reject empty results post-normalization.
8. Dedupe by `tag_key` within a single result; the lower-rank duplicate wins.

**Internal punctuation is preserved** — `black-and-white`, `t-shirt`, `o'clock` keep their hyphens / apostrophes. Over-normalizing into `black and white` produces worse keys.

### 9.3 Display

- All active tags shown as cosmetic chips in the lightbox info panel, in rank order.
- Visible cap: 12 chips, then `+N more` expander.
- Chips are **not** clickable in v1 — search-route navigation lights them up later. No "search coming" toast on click.

## 10. Caption schema

### 10.1 Output contract

- **Single text field**, 1–3 sentences, ≤300 characters, English.
- **Prompt-enforced style:** descriptive third-person ("A small dog on a beach at sunset"), no first-person ("I see"), no hallucinated names of people or places, no markdown.
- **No structured output**, no alt-text variant, no length knob, no language knob.

### 10.2 Validation

After parse, before persist:

- Trim whitespace.
- Reject if empty.
- Reject if markdown formatting is present: paired `*…*` / `_…_` / `**…**` emphasis, headings (line starting with `#` followed by space), code spans (any backtick) or fenced blocks (` ``` `), links (`[…](…)`), blockquote (line starting with `>`), table pipes (`|…|`), or list-leading `- ` / `* ` / `1. `. Bare characters that don't form a markdown construct (e.g. `o'clock`, an apostrophe) are fine.
- Reject if length > 300 chars.
- On reject: retry once (job-level), then terminal `failed:malformed`. **Never silently truncate.**

### 10.3 Display

- Caption text + small provenance line `<model_id> · <generated_at>`.
- `<model_id>` is a real link to the AI panel; the rest is non-interactive.
- On per-photo failure under the active fingerprint, the section reads `"Caption failed · Retry"` inline (in place of the missing caption). The Retry button enqueues a single `ai_jobs` row and clears the matching `ai_failures` row on success.

## 11. Prompt versioning

- Prompts live as Go const strings in `internal/ai/prompts/` — `tags-v1`, `caption-v1`.
- `prompt_version` stored on every `ai_results` row is the stable version identifier (`"tags-v1"`), not the Go const name.
- `prompt_hash` is the SHA-256 hex of the const string. It's stored on every `ai_results` row alongside `prompt_version`. A CI test asserts `sha256(promptConst) == ExpectedHash[promptVersion]`. Editing the const without bumping the version fails CI — accidental drift is caught.
- Bumping the version (e.g. `caption-v1` → `caption-v2`) does not auto-rerun. It changes the active fingerprint; gap scanner / explicit backfill drives re-execution.

## 12. Lightbox surfacing

The lightbox info panel's AI section, in order:

```
AI TAGS
[dog] [beach] [sunset] [golden hour] [silhouette] [small dog]    (cosmetic chips)

CAPTION
A small dog stands on the beach as the sun sets, casting a long
silhouette on the wet sand.
qwen2.5-vl:3b · Apr 28 3:42 pm                                   (model_id is a link → AI panel)
```

- Section is rendered only when at least one active result exists for the photo, OR when an `ai_failures` row exists for the active fingerprint, OR when `[ai].enabled = true` AND no result yet (shows "Tags · pending" / "Caption · pending" placeholders).
- For failures, the section shows `"Tags failed · Retry"` / `"Caption failed · Retry"` in place of the missing content. Retry button is per-photo, per-task.
- For `ai_skipped(reason='video')` photos, the section is omitted — videos aren't a normal AI surface in v1.
- For `ai_skipped(reason='no_preview')` photos, the section shows `"AI not available — preview unavailable"` (low-key gray text, no retry button).

## 13. AI panel (Settings → AI)

### 13.1 Layout

Single page, stacked sections — no tabs. Top-down:

1. **Header strip.** Title "AI"; status line `<color-dot> <reachable status> · <model> @ <host>`; right-aligned indicator `Enabled via config.toml` (read-only — not an interactive toggle).
2. **Hidden-processing disclosure banner** (persistent, full-width, warning-colored). Copy:
   > **Hidden photos are processed.** Your `<N>` hidden photos are tagged and captioned the same as the rest. Outputs stay owner-only and are only visible in the unlocked Hidden context — but the image bytes are sent to the configured AI endpoint.
3. **Tag task card.** Header `Tag · <model_id> · <prompt_version> · <input_profile>` plus `Backfill all` button; counter row (`Done / Pending / Working / Skipped / Failed`); throughput + last-completed line. `Failed` is a link → retry-all action.
4. **Caption task card.** Same shape as tag.
5. **Recent failures** (combined across tasks). Up to 5 rows of `<filename> — <error_kind>: <truncated_message> · <relative_time>`. If more than 5 exist, a `View all (N) →` drill-in (drill-in is deferred but the affordance is reserved).

### 13.2 Acknowledgement gate

When `[ai].enabled = true` AND no `ai.hidden_processing_acknowledged_at` row exists for the operator's principal:

- The header status line reads `Paused · Acknowledgement required` with a yellow dot.
- The disclosure banner is replaced with a **modal-style block** at the top of the panel:
  > **Before AI starts processing your library**
  >
  > Hidden photos will be tagged and captioned alongside everything else. Outputs stay owner-only and are only visible inside the unlocked Hidden context, but the image bytes are sent to `<host>`.
  >
  > [ ] I understand hidden photos are processed and image bytes go to the configured endpoint.
  >
  > `[Acknowledge and start workers]`
- The acknowledge action POSTs `/api/v1/ai/acknowledge { kind: "hidden_processing" }`, which writes the `user_settings` row.
- **Workers stay parked** until the row exists. Endpoint health checks may still run (so the operator sees reachability before acknowledging), but no `ai_jobs` rows are claimed.
- After acknowledgement, the panel reverts to its normal layout and the persistent banner remains (compact, non-blocking).

### 13.3 API endpoints

| Endpoint | Body / params | Purpose |
|---|---|---|
| `GET /api/v1/ai/health` | — | aggregated dashboard fetch (see §14) |
| `GET /api/v1/ai/failures` | `?task=tag&limit=5` | recent failures list |
| `POST /api/v1/ai/backfill` | `{ task, scope: "all" \| { media_ids: [...] } }` | bulk enqueue against active fingerprint |
| `POST /api/v1/ai/retry-failed` | `{ task }` | clear `ai_failures` rows + re-enqueue jobs (idempotent) |
| `POST /api/v1/ai/retry-photo` | `{ media_id, task }` | lightbox per-photo Retry button |
| `POST /api/v1/ai/acknowledge` | `{ kind: "hidden_processing" }` | persist acknowledgement; releases worker pause |

All routes go through `internal/service/ai`, which enforces caller-principal scoping. The CLI (§16) hits the same internal service, **not** the HTTP server.

## 14. Health endpoint shape

`GET /api/v1/ai/health` returns:

```jsonc
{
  "enabled": true,
  "paused_reason": "",                             // "" | "config_disabled" | "acknowledgement_required"
  "vision": {
    "reachable": true,
    "last_check_at": "2026-04-30T18:42:11Z",
    "last_error": null
  },
  "tag": {
    "active_fingerprint": "qwen2.5-vl:3b|tags-v1|jpeg-1024-q85-metadata-stripped-v1",
    "pending": 316,
    "working": 1,
    "blocked": 0,
    "failed_active": 3,
    "skipped": 1247,
    "done": 42108,
    "throughput_per_min": 18.4,
    "last_completed_at": "2026-04-30T18:42:08Z"
  },
  "caption": { /* same shape as tag */ }
}
```

The shell-strip dot state is derived from this payload, evaluated top-down (first match wins). Per the web-frontend design's a11y rule (color + icon + `aria-label`, never color alone), each state pairs a color with a distinct icon and label so colors aren't load-bearing on their own:

- `paused_reason == "config_disabled"` → dot **hidden entirely** (AI is off; nothing to surface).
- `paused_reason == "acknowledgement_required"` → `paused` — yellow, pause-icon, label `"AI paused — acknowledgement required"`.
- `vision.reachable == false` → `unreachable` — red, plug-disconnected icon, label `"AI endpoint unreachable"`.
- `tag.failed_active + caption.failed_active > threshold` (default 10) → `failing` — orange, alert icon, label `"AI failures need attention"`.
- `tag.pending + caption.pending > threshold` (default 1000) → `backlog` — yellow, queue icon, label `"AI backlog"`.
- Otherwise → `idle` — green, check icon, label `"AI healthy"`.

Note that `paused` and `backlog` share a yellow color but are distinguished by icon and `aria-label`, satisfying the never-color-alone rule. The threshold defaults are not yet exposed in config — added if real use shows the need.

## 15. SSE events

Three new event types added to the existing `/api/v1/events` stream (per the web-frontend design's SSE contract). Each is filtered to the caller's principal server-side.

```
event: ai.tag.completed
data: {"media_id":"…","task":"tag","status":"done","model_id":"qwen2.5-vl:3b","prompt_version":"tags-v1","input_profile":"jpeg-1024-q85-metadata-stripped-v1","result_id":"…"}

event: ai.caption.completed
data: {"media_id":"…","task":"caption","status":"done","model_id":"…","prompt_version":"caption-v1","input_profile":"…","result_id":"…"}

event: ai.health.changed
data: {"vision":{"reachable":false,"last_error":"connection refused"},"tag":{"pending":318,"failed_active":4},"caption":{"pending":3935,"failed_active":12}}
```

The completion payloads include the full active-fingerprint triple (`model_id` + `prompt_version` + `input_profile`) so a client can ignore stale events after a config or prompt change without re-fetching.

`status` on completion events is one of `done` | `failed`. Skips do not emit completion events; the panel surfaces skip counts via `/ai/health`.

## 16. CLI surface

```
fotobank ai status                       # pretty-print of /ai/health, hits internal service
fotobank ai backfill --task=tag,caption [--media-id=ID …  | --all] [--force]
fotobank ai retry-failed --task=tag,caption
fotobank ai acknowledge --hidden-processing
```

- All commands use the in-process `service.AI` directly. They do **not** require the HTTP listener to be up. Endpoint reachability is still live-checked when the command needs it.
- `--force` on `backfill` deletes existing `active` `ai_results` rows for the targeted media+task before enqueueing — the only path that re-runs under the same fingerprint.
- No per-photo retry CLI. That's a lightbox affordance.

## 17. Observability (Prometheus)

Mirrors the existing `internal/obs` shape used by thumb / share workers.

| Metric | Type | Labels | Notes |
|---|---|---|---|
| `fotobank_ai_jobs_depth` | gauge | `task`, `status` | depth per (task, status ∈ {pending, working, blocked}). One time series per `(task, status)` pair. |
| `fotobank_ai_jobs_completed_total` | counter | `task`, `result` | result ∈ {ok, failed, skipped}. Increments on terminal transitions. |
| `fotobank_ai_request_duration_seconds` | histogram | `task`, `outcome` | outcome ∈ {ok, transient, provider_4xx, malformed}. Per provider call. |
| `fotobank_ai_endpoint_reachable` | gauge | `kind` | kind="vision". 1 if last health check succeeded, 0 otherwise. |
| `fotobank_ai_acknowledgement_required` | gauge | — | 1 if any worker is parked on acknowledgement, 0 otherwise. **No `principal` label** — identity does not belong in metric labels. |

## 18. Identity & multi-principal forward-compat

- Jobs and results do not denormalize owner columns. Owner is always `JOIN media`.
- The acknowledgement is per-principal in `user_settings`. A worker checks the acknowledgement scoped to the job's owner before claiming. v1 stub mode means one principal, but the check is in place.
- All AI endpoints go through identity middleware; `service.AI` enforces that callers can only see / act on media they own.
- The shell-strip dot is per-caller (a future viewer-only context would see its principal's view of the AI surface — currently no AI work to show, gracefully empty).

## 19. Privacy & sharing rules

- **No grantee exposure of AI tags or captions in v1.** Shared scopes (album-live, media-set) project the existing media fields; tag and caption joins are owner-only. Adding shared exposure is a separate later spec — including the question of whether grantees see model_ids and timestamps too. Out of scope here.
- **Hidden-aware:** AI tags and captions are filtered by the same hidden-context rules as their parent media. Outputs for hidden photos surface only in the unlocked Hidden lightbox; the AI panel counters include hidden in totals (no break-down by hidden state).
- **Coords / EXIF on the wire:** never. The input profile strips metadata before encode.
- **Acknowledgement scope:** `kind = "hidden_processing"`. Future kinds (e.g. `"hosted_endpoint_data_sharing"`) compose by adding new rows; revoking would clear the row and re-park workers.

## 20. Worker mechanics

- Per-task workers in `internal/ai/worker/{tag,caption}.go`, each running `Run(ctx)` ticks + `RunOnce(ctx)` claim-batch loops modeled on `internal/thumb/worker.go`.
- Each worker invokes the gateway through `internal/ai/gateway.VisionGateway` — an interface around the OpenAI-compatible HTTP client. The interface is provider-neutral so a future native-Anthropic adapter could implement it without changing worker code, but only `OpenAICompatible` ships in v1.
- Workers respect three pause gates (in order): (a) `[ai].enabled`, (b) per-task `enabled`, (c) hidden-processing acknowledgement for the job's owner.
- The global `[ai.vision].max_inflight` semaphore is acquired **before** claim and released on terminal disposition (`done` / `failed` / `blocked`) or when the claim batch returned no rows. A worker waiting on a full semaphore does **not** hold any DB claim — `working` count remains an honest measure of in-flight provider calls, not of workers parked on the semaphore.
- Config validation rejects `ai.vision.max_inflight = 0` (would deadlock all AI work) and any `worker_concurrency` value < 1.
- Crash recovery: lease sweep every minute resets `working` rows older than 10 min back to `pending`.

## 21. Verification gaps for the plan

The implementation plan should verify these before assuming:

- `internal/thumb/queue.go` exposes a query / API the AI worker can use to read `thumb_status` per media at claim time without re-implementing the lookup. If not, plan adds a thin `thumb.StatusFor(mediaID)` accessor — owner-scoped where applicable.
- The existing pure-Go JPEG encoder used by the thumb pipeline (`internal/thumb/encode.go`) supports re-encoding from a decoded preview at q=85 with metadata stripping. Plan should verify rather than assume; otherwise, add an explicit AI-input encode path.
- `internal/web/embed.go` and the SPA build pipeline can absorb the new AI panel and lightbox AI section without route changes (existing `/settings/ai` slot reserved by the F1 plan).
- The SSE bus' per-principal filter handles the new event types without broadcaster changes (the F1 SSE skeleton is type-agnostic; verify).
- `obs.Metrics` exposes the metric primitives needed for the new gauges/counters/histograms; if not, the plan adds them.

## 22. Out-of-scope (named v2 candidates)

These are deliberately deferred — naming them here so future planners do not relitigate:

- **Image embeddings** (CLIP-family or hosted multimodal embeddings).
- **Semantic search** / hybrid RRF / query-time fusion.
- **FTS5 lexical index** over `caption_text` and `tag_label`.
- **Search route + filter chips + tag autocomplete API.**
- **Vector storage backend** (sqlite-vec / external vector DB).
- **Shared / grantee exposure of AI tags or captions.**
- **AI image editing** (auto-enhance, magic eraser).
- **Multi-modal prompts** (drop-photo + text query).
- **Face detection / People tab.**
- **Memories / For You / smart album rules.**
- **Video frame extraction for VLM** — videos remain `ai_skipped(reason='video')` in v1.
- **Structured caption variants** (alt-text vs long-description split).
- **Per-user prompt customization.**
- **Per-key cost caps for hosted endpoints** — operators rate-limit at provider side in v1.
- **Native Anthropic / Gemini SDK adapters** — OpenAI-compat only; Anthropic via OpenRouter / LiteLLM.
- **"Skip hidden photos" AI toggle** — disabling AI altogether is the right escape hatch.
- **Score-based tag promotion (`candidate` status, score floors).** Revisited if a future provider gives calibrated scores.

## 23. Open spikes (resolve before plan-writing)

None blocking. The "verification gaps" in §21 are pre-plan checks, not spikes.

---

**Glossary**

- **Active fingerprint** — `(model_id, prompt_version, input_profile)` triple identifying a specific way of producing AI output. Stored verbatim on every result; gap scanner and panel queries filter by the currently-configured triple.
- **Result** — `ai_results` row representing one task run's output. At most one `active` per `(media_id, task)`; reruns transition the prior to `stale` atomically.
- **Acknowledgement gate** — the worker pause condition that holds until the operator has confirmed they understand hidden photos are processed. Stored as a per-principal `user_settings` row.
