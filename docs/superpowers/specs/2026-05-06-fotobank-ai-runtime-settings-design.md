# fotobank AI Runtime Settings Design

> **Status:** Brainstormed 2026-05-06. Awaiting user review before implementation planning.

## 1. Goal

Add server-global, runtime-editable AI infrastructure settings so an admin can configure AI tagging, captioning, and embedding workflows from the web UI without writing raw secrets to the database and without restarting the server for ordinary model or endpoint changes.

The feature covers AI config persistence, effective-config resolution, hot-apply semantics, admin APIs, endpoint diagnostics, and the admin UI surface. It deliberately does not cover broader AI backlog visibility improvements; those are tracked separately.

## 2. Principles

1. **TOML remains the bootstrap source.** `config.toml` supplies defaults and recovery state. UI edits are DB overrides layered on top.
2. **Runtime AI config is global.** Endpoint, model, dimension, and worker-facing AI infrastructure settings are server-wide operator settings, not per-owner preferences.
3. **Per-owner preferences stay separate.** `user_settings` remains for caller-scoped UI preferences such as `ai.inspection`; it is not overloaded with global infrastructure config.
4. **No raw API keys in SQLite.** The persisted value is the environment variable name, not the secret. Raw bearer tokens never enter `app_settings`, backups, or debug DB copies.
5. **Workers observe validated snapshots.** Workers never read partially-applied config and never publish invalid config to themselves.
6. **Embedding generations keep their existing lifecycle.** Config controls fingerprints; `embedding_generations` remains the source of generation state and promotion lifecycle.

## 3. Persistence

Add a new server-global key/value table:

```sql
CREATE TABLE app_settings (
    key                 TEXT      PRIMARY KEY,
    value               TEXT      NOT NULL, -- JSON-encoded
    updated_at          TIMESTAMP NOT NULL,
    updated_by_hub      TEXT,
    updated_by_user_id  TEXT
);
```

Pre-alpha migration policy applies: edit `internal/db/migrations/000001_initial_schema.up.sql` and `internal/db/migrations/000001_initial_schema.down.sql` in place, keeping both directions in sync.

Values are JSON-encoded so each key preserves its native type (`true`, `512`, `"model-name"`). Keys are literal TOML paths.

The v1 allowlist is:

- `ai.enabled`
- `ai.vision.endpoint`
- `ai.vision.api_key_env`
- `ai.tag.enabled`
- `ai.tag.model`
- `ai.caption.enabled`
- `ai.caption.model`
- `ai.embed.enabled`
- `ai.embed.endpoint`
- `ai.embed.api_key_env`
- `ai.embed.model`
- `ai.embed.dimension`
- `ai.embed.input_edge`

Tuning knobs stay file-only in v1: worker concurrency, `max_inflight`, timeouts, retries, `batch_size`, and `idle_poll`. Admin API attempts to write or reset a non-allowlisted key return 400.

`app_settings` rows are included in backups because they are recovery state. Tests must prove only env var names are stored for `api_key_env` keys, not raw key-shaped values.

## 4. Effective Config Provider

Introduce an `EffectiveAIConfigProvider` that owns the live AI config snapshot. It loads TOML defaults, overlays allowlisted `app_settings`, validates the merged AI config, and publishes it through an atomic snapshot.

`Reload()` runs under a `sync.Mutex`. Atomic store is safe for publication, but file-read plus DB-read must be serialized so concurrent Apply requests cannot interleave reloads. The provider never publishes invalid config. On boot, an invalid file+DB merge fails startup loudly. At runtime, a reload failure keeps the previous snapshot live and returns an error to the caller.

Workers and background components read `provider.Effective()` at claim or tick boundaries. The refactor surface includes tag worker, caption worker, embed worker, embed activator, and gap scanner.

The provider exposes config, not generation handles. `FindOrCreateBuilding` runs from the admin Apply/reset path, and the activator continues polling `embedding_generations` from the DB on its tick.

## 5. Fingerprint Contract

Workers take an effective config snapshot before each claim and only claim jobs matching the current settings fingerprint. Old in-flight work may finish under the old fingerprint; provenance remains correct and later backfill replaces it. Old pending or blocked jobs are not re-claimed after a fingerprint change.

Generation-defining embed keys are:

- `ai.embed.model`
- `ai.embed.dimension`
- `ai.embed.input_edge`

Changing or resetting any of these creates or finds a new `embedding_generations` building row. The old active generation remains active until the existing activator promotes the new one.

## 6. Admin Authorization

Add a TOML-backed admin allowlist. In stub mode, if `[admin]` is absent, default admin to the configured stub principal. In header mode, require explicit `[admin].principals`; otherwise admin routes deny everyone and log a boot warning.

Admin routes use a single `requireAdmin` middleware:

- 401 when no authenticated principal exists.
- 403 when the caller is authenticated but not on the allowlist.
- 403 responses do not include role or allowlist details.

## 7. Admin API

Routes live under `/api/v1/admin/settings`.

- `GET /api/v1/admin/settings`
- `PUT /api/v1/admin/settings/sections/{vision|tag|caption|embed}`
- `DELETE /api/v1/admin/settings/keys/{key}`
- `DELETE /api/v1/admin/settings/sections/{section}`
- `POST /api/v1/admin/settings/test/{vision|embed}`

### GET Response

`effective` and `file_default` always include all 13 allowlisted keys. `overrides` includes only keys with DB overrides.

```json
{
  "effective": {
    "ai.embed.model": "qwen2-vl-7b"
  },
  "file_default": {
    "ai.embed.model": "qwen2-vl-32b"
  },
  "overrides": {
    "ai.embed.model": {
      "value": "qwen2-vl-7b",
      "updated_at": "2026-05-04T16:23:00Z",
      "updated_by": { "hub": "dev-local", "user_id": "owner" }
    }
  },
  "api_key_env_status": {
    "ai.vision.api_key_env": { "name": "FOTOBANK_VLM_KEY", "is_set": true },
    "ai.embed.api_key_env": { "name": "FOTOBANK_EMBED_KEY", "is_set": false }
  },
  "current_embed_generation": {
    "id": 42,
    "state": "active",
    "model": "clip",
    "dimension": 768
  }
}
```

The server resolves `api_key_env_status` from the process environment without returning secret values.

### Apply And Reset

Section Apply is section-level, not per-field immediate save. The request validates the full section against the current effective config plus pending section values. Apply validates local shape only: URL format, required model values when enabled, positive dimensions, and type correctness. It does not require a successful live endpoint probe.

For non-embed sections, Apply upserts the section keys in one DB transaction. For embed, Apply runs in one DB transaction that includes both `app_settings` writes and `Generations.FindOrCreateBuildingTx(tx, ...)` when a generation-defining key changes. This requires adding a tx-aware generations helper and makes the returned `generation_id` authoritative.

Reset is Apply with overrides deleted and affected keys snapshotted to their effective-without-override values. Per-key reset and section reset share the same validation, embed generation, commit, and reload path. There is no global reset in v1.

Ordering:

1. Validate request.
2. Open DB transaction.
3. Upsert or delete `app_settings` rows.
4. For embed generation changes, find or create the building generation inside the same transaction.
5. Commit.
6. Call `provider.Reload()`.

If reload fails after commit, the previous snapshot stays live, the route returns 500, and persisted overrides remain for a future reload.

Concurrent Applies serialize through SQLite's write pool and the provider mutex. The final published snapshot reflects the last-committed state.

PUT and DELETE responses return the affected section's new effective values. Embed responses also include `generation_id` when apply/reset creates or finds a generation.

## 8. Probe Diagnostics

Endpoint tests live in a new `internal/ai/probe` package. The probe path is deliberately disjoint from the production provider: it builds ephemeral configs from request bodies and performs one synthetic request. It does not update the provider, acquire the production semaphore, emit metrics or events, write jobs, or write failures.

Probe routes are POST because they test pending unsaved form values. Request bodies carry `api_key_env`, never the raw API key. The server resolves `os.Getenv(api_key_env)`. If a non-empty env var name is unset, the result is `auth_failed` with detail like `env var $NAME is not set in the server process`; the probe does not send an empty bearer token and accidentally test anonymous access.

The probe uses a fixed 15s timeout so a UI button cannot hang for a production timeout such as 2 minutes.

### Vision Probe

Send one OpenAI-compatible chat completion request:

- single user message
- one tiny embedded JPEG, reusing the existing small test JPEG shape
- prompt: `Reply with the single word 'ok'.`
- `max_tokens = 8`
- `temperature = 0`

The multimodal payload exercises the same broad path as production tag/caption workers and avoids servers that reject text-only chat on a vision endpoint.

### Embed Probe

Send `POST /v1/embeddings` with one text input: `"test"`. The probe validates `len(data[0].embedding) == configured_dimension`. The text input keeps diagnostics portable; this probe checks endpoint shape, auth, model routing, and dimension, not image-embedding quality.

### Probe Result

```json
{
  "ok": true,
  "classification": "ok",
  "latency_ms": 412,
  "detail": "endpoint responded",
  "model_echoed": "qwen2-vl-7b",
  "warnings": []
}
```

Classifications:

- `ok`
- `auth_failed`
- `unreachable`
- `timeout`
- `model_mismatch`
- `dimension_mismatch`
- `malformed_response`
- `provider_error`

When multiple conditions could apply, classification precedence is:

`unreachable -> timeout -> auth_failed -> provider_error -> malformed_response -> dimension_mismatch -> model_mismatch -> ok`

Vision model echo rule:

- A 200 response with no model field, or a model field that differs from the configured value, is `ok: true` with a warning.
- A clear 400/404 response whose body indicates model-not-found or unknown-model is `ok: false`, `classification: "model_mismatch"`.
- The dividing line is whether the server executed the inference request or refused it.

## 9. UI Surface

The admin page is `/admin/settings/ai`, linked from the existing `/settings/ai` page only when the caller is admin. There is no admin sidebar entry in v1.

The page uses four stacked sections:

- Vision
- Tag
- Caption
- Embed

All sections share an `AdminSettingsSection` component with a section header, status badge, dirty-state handling, field slot, Test, Apply, per-key reset, section reset, and per-section Discard.

Section badge states:

- `clean`: effective values match file defaults and no override exists.
- `overridden`: one or more DB overrides exist and are valid/applied.
- `dirty`: form edits are unsaved.
- `error`: last Apply failed validation or reload.

Discard is per-section and resets only that section's form to current effective values. One section's edits do not interact with another section's edits.

Test runs against pending unsaved form values. Test is disabled while the section form is structurally invalid, such as an empty endpoint URL. Apply is section-level and independent from Test; Apply does not require a green Test result.

Embed Apply requires confirmation when `ai.embed.model`, `ai.embed.dimension`, or `ai.embed.input_edge` changes. The confirmation modal shows:

- current generation id, model, dimension, state, and age
- proposed model, dimension, and input edge
- eligible row count
- estimated rebuild time band based on current embed throughput
- copy stating the old active generation stays active until the new generation promotes

Cancel closes the modal without applying. Confirm sends the Apply request.

## 10. Test Strategy

Unit tests own classification taxonomy completeness: every probe classification is hit by at least one HTTP-fixture-driven probe test. Playwright covers wiring: admin reaches the form, edits propagate, confirmation modal appears, and `generation_id` surfaces in the response. The browser is not the test harness for response classification correctness.

Backend unit and integration tests:

- `internal/ai/probe`: each classification (`ok`, `auth_failed`, `unreachable`, `timeout`, `model_mismatch`, `dimension_mismatch`, `malformed_response`, `provider_error`) against `httptest.Server` fixtures.
- Provider: reload validation, invalid merge keeps previous snapshot, mutex-serialized reload behavior, no invalid snapshot publication.
- Provider fingerprint: changing a generation-defining key such as `ai.embed.model` produces a snapshot fingerprint different from the prior snapshot.
- Workers: one claim-boundary test each for tag, caption, and embed proving workers only claim jobs matching the current snapshot fingerprint and skip previous-fingerprint jobs.
- App settings service: allowlist enforcement, section apply/reset, per-key reset, audit metadata, JSON type validation, env-var status, and embed `FindOrCreateBuildingTx`.
- Admin HTTP: 401 unauthenticated, 403 non-admin with no role hints, GET, PUT, DELETE key, DELETE section, and POST probe routes.
- Backup: write an `app_settings` override, run `Snapshot`, open the snapshot read-only, confirm the row is present, and confirm `api_key_env` rows contain env var names only, not raw key-shaped secret strings.

E2E:

- Extend `cmd/e2e-server` AI mocks rather than adding another process.
- Add mock probe modes driven by an HTTP header such as `X-Probe-Mode`, with values `ok`, `auth_failed`, `timeout`, `wrong_dim`, `wrong_model`, `malformed`, and `provider_error`. Existing AI Playwright tests do not set the header and continue using current mock behavior.
- Playwright covers admin happy path, embed wrong-dimension probe failure, embed apply confirmation, section reset, and non-admin behavior.
- Non-admin behavior includes both direct HTTP and SPA route coverage:
  - `GET /api/v1/admin/settings` as non-admin returns 403 with no role hints.
  - navigating to `/admin/settings/ai` as non-admin redirects to `/settings/ai` or `/`, and the `/settings/ai` configure link is absent.
- Embed confirmation modal e2e asserts current generation id, proposed model and dimension, and old-generation-stays-active copy. Cancel does not apply. Confirm proceeds and surfaces the generation id.

## 11. Out Of Scope

- Raw API key storage in SQLite.
- Per-owner AI model or endpoint settings.
- Runtime editing of tuning knobs such as concurrency, timeouts, retries, batch size, or idle poll.
- Page-level reset-all.
- Replacing the existing embedding generation activator.
- Broader AI processing visibility improvements; those are tracked separately.
