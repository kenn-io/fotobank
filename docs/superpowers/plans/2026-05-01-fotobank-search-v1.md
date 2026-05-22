# Fotobank Search v1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` to implement this plan task-by-task. **Every subagent dispatched for this plan uses the Opus model** — pass `model: "opus"` to the Agent tool on every dispatch (the `model` parameter overrides the agent definition's default). Steps use checkbox (`- [ ]`) syntax for tracking. **After every 5th task, invoke `/roborev-fix` on the current branch as a cleanup checkpoint.** Do not start the next 5-task chunk until the cleanup checkpoint completes.
>
> **Prerequisite:** the pre-step driver migration plan (`docs/superpowers/plans/2026-05-01-fotobank-search-pre-driver-migration.md`) must be complete and its verification checklist green. The mattn driver, `sqlite-vec` extension registration, and post-migration backup/restore must all be in. Do not start Task 1 of this plan otherwise.

**Goal:** Add hybrid (semantic + lexical) search to fotobank exposed via the ⌘K-bound search bar, with image-side multimodal embeddings, an FTS5 lexical index, RRF fusion via a composed sqlite-vec SQL query, structured filter chips (date, tag, location, media type), three sort modes, an indexing-status pill, and an opt-in Diagnostics mode — owner-only in v1.

**Architecture:** Three new backend packages on top of the existing AI substrate (`internal/ai/embedding/`, `internal/search/index/`, `internal/search/hybrid/`) plus a service-layer wrapper (`internal/service/search/`) and a transport file (`internal/httpapi/search.go`). The AI write side extends to a third task, `embed`, sharing `ai_jobs`, the ack gate, the fingerprint pattern, the gap-scanner orchestration, and the health/SSE surface with tag/caption — but with its own worker because batching, request payload, and image preprocessing differ. The search read side is its own subsystem: a `Backend` interface mirroring msgvault's sqlite-vec backend, an `Engine` that adapts msgvault's RRF + filter + engine logic to the photo schema, and a service layer enforcing owner scoping + hidden-gate semantics on every call. New SPA route `/search` consumes the existing virtualized grid.

**Tech Stack:** Go (`database/sql`, `mattn/go-sqlite3` + `sqlite-vec` [post-migration], huma/v2, `golang.org/x/sync/semaphore`, `github.com/VictoriaMetrics/metrics`); Svelte 5 + TypeScript on the frontend with the existing virtualized grid + intersection-observer pagination.

**Reference docs:**
- Spec: `docs/superpowers/specs/2026-05-01-fotobank-search-design.md`
- Pre-step: `docs/superpowers/plans/2026-05-01-fotobank-search-pre-driver-migration.md`
- AI tag/caption plan and surface: `docs/superpowers/plans/2026-04-30-fotobank-ai-tag-caption.md` and `internal/ai/{jobs,worker,gapscanner,gateway,results,failures,skipped,ack,prompts,imginput}/`
- Master vision (identity scope): `docs/superpowers/specs/2026-04-22-fotobank-vision.md`
- Msgvault operational blueprint (referenced only conceptually; no path dependencies): `internal/vector/{backend,embed,sqlitevec,hybrid}` and `internal/query/sqlite_text.go`. The fotobank engine adapts the same RRF + filter CTE pattern.

---

## File structure

Backend (new):

| File | Responsibility |
|---|---|
| `internal/db/migrations/000001_initial_schema.up.sql` | (modify) widen `ai_jobs/ai_results/ai_failures/ai_skipped` task constraints, add `media.lens_model`, add `embedding_generations`, `media_embedding_ids`, `media_fts`, FTS cleanup trigger, `media_owner_type_idx` |
| `internal/db/migrations/000001_initial_schema.down.sql` | (modify) keep the wholesale-teardown shape; sync any explicit `DROP` lines if needed |
| `internal/ai/types.go` | (modify) add `TaskEmbed` and extend `Task.Valid()` |
| `internal/ai/config.go` | (modify) add `EmbedConfig`, validate, defaults |
| `internal/search/config.go` | (new) `[search]` config block — `KPerSignal`, `RRFK`, `RetainRetiredDays`, `ActivationThresholdPct` |
| `internal/ai/imginput/resolver.go` | (modify) add `ResolvePreviewJPEG`; existing `ResolveAndEncode` deprecates to a thin wrapper or is removed once callers migrate |
| `internal/ai/imginput/encode/encode.go` | (new) `EncodeChat(jpg) ([]byte, error)`, `EncodeEmbed(jpg, edge int) ([]byte, error)` |
| `internal/ai/imginput/encode.go` | (existing) chat encoder body moves to `encode.EncodeChat`; this file thin-wraps for back-compat or is replaced |
| `internal/ai/embedding/client.go` | (new) `Client` interface + OpenAI-compat impl (image data URL or text input) |
| `internal/ai/embedding/probe.go` | (new) boot-time modality + dimension probe |
| `internal/ai/embedding/worker.go` | (new) embed worker (claim → resolve preview → batch encode → call → write) |
| `internal/ai/embedding/activator.go` | (new) periodic generation-activation tick |
| `internal/ai/embedding/generations.go` | (new) `Generations` repo: find/create/list/promote/retire |
| `internal/ai/embedding/mapping.go` | (new) `media_embedding_ids` repo: write/replace/delete vec mappings, recount counter |
| `internal/ai/embedding/compactor.go` | (new) retired-generation compaction sweep |
| `internal/ai/gapscanner/scanner.go` | (modify) add `ScanEmbed` predicate (separate query path from tag/caption) |
| `internal/search/index/backend.go` | (new) `Backend` interface; sqlite-vec impl |
| `internal/search/index/fts.go` | (new) `RefreshMediaFTS(tx, mediaID)` helper |
| `internal/search/hybrid/filter.go` | (new) structured-filter resolution → CTE fragments |
| `internal/search/hybrid/rrf.go` | (new) RRF math + `KPerSignal` widening |
| `internal/search/hybrid/query.go` | (new) FTS5 MATCH expression construction (escaping + trailing prefix) |
| `internal/search/hybrid/engine.go` | (new) `Engine.Search` — branching on (q, sort, signal availability) |
| `internal/search/hybrid/cursor.go` | (new) opaque base64 cursor encode/decode + req-hash check |
| `internal/service/search/service.go` | (new) auth-scoped wrapper enforcing owner scoping + hidden-gate |
| `internal/service/search/autocomplete.go` | (new) tag + location autocomplete with hidden-aware counts |
| `internal/service/search/completeness.go` | (new) embedding-completeness fraction under the request's hidden predicate |
| `internal/httpapi/search.go` | (new) huma routes for `/search`, `/search/autocomplete/tags`, `/search/autocomplete/locations` |
| `internal/httpapi/events.go` | (modify) add `EventNameAIEmbedCompleted`, `EventNameAIEmbedFailed`, `EventNameAIEmbedGenerationCreated`, `EventNameAIEmbedGenerationActivated`, `EventNameAIEmbedGenerationRetired` |
| `internal/httpapi/ai_events.go` | (modify) emit helpers for the new event names |
| `internal/httpapi/ai.go` | (modify) extend `aiHealthOutput` with embed-task block + embedding-generations block |
| `internal/cli/ai.go` | (modify) extend `backfill --task=embed`, `retry-failed --task=embed`, add `list-generations`, `promote-generation`, `compact-retired-generations` |
| `internal/ingest/...` | (modify) auto-enqueue embed alongside tag/caption on import |
| `internal/thumb/worker.go` (or thumb-version-bump caller) | (modify) on thumb-version bump, invalidate embed mappings across non-retired generations and re-enqueue |
| `internal/cli/server.go` | (modify) wire embed worker, activator tick, compaction tick, gap-scan tick |
| `internal/obs/metrics.go` | (modify) `task="embed"` labels, embed/search-specific gauges/histograms/counters |

Frontend (new):

| File | Responsibility |
|---|---|
| `frontend/src/lib/api/generated/schema.ts` | (regenerated) `/search` + autocomplete route types |
| `frontend/src/lib/search/types.ts` | (new) `SearchFilters`, `SearchResult`, `SearchSort` |
| `frontend/src/lib/search/searchStore.svelte.ts` | (new) reactive search store with cursor pagination and SSE invalidation |
| `frontend/src/lib/search/client.ts` | (new) typed wrappers for `/search` and the two autocomplete endpoints |
| `frontend/src/lib/router/router.svelte.ts` | (modify) add `/search` route + query-param parsing |
| `frontend/src/lib/components/AppHeader.svelte` | (modify) debounce + ⌘K integration; replace + push history rules |
| `frontend/src/routes/Search.svelte` | (new) the search page; consumes the virtualized grid |
| `frontend/src/lib/search/SearchFiltersPopover.svelte` | (new) date / tag / location / media-type filter controls |
| `frontend/src/lib/search/SearchFilterChips.svelte` | (new) chips below the popover, removable |
| `frontend/src/lib/search/SearchSortSegment.svelte` | (new) Relevance / Newest / Oldest |
| `frontend/src/lib/search/IndexingStatusPill.svelte` | (new) "X / Y indexed" pill |
| `frontend/src/lib/search/IndexingStatusBanner.svelte` | (new) under-80%, no-active-generation, query-embedding-failed banner |
| `frontend/src/lib/search/DiagnosticsBadge.svelte` | (new) hover RRF score on result cells; lightbox per-signal row |
| `frontend/src/lib/lightbox/components/LightboxMetadata.svelte` | (modify) optional Search relevance row when `explain=true` |
| `frontend/src/routes/Search.test.ts`, `frontend/src/lib/search/*.test.ts` | (new) component + store unit tests |
| `cmd/e2e-server/main.go` | (modify) seed FTS + a fixed-fingerprint embedding generation; mock the embedding endpoint |
| `frontend/tests/e2e/search.spec.ts` | (new) E2E: filter chips, sort, pagination, pill/banner |

---

## Conventions and warnings for the executor

- **Schema policy.** Pre-alpha — every schema change goes in-place into `000001_initial_schema.{up,down}.sql`. The down file is a wholesale teardown; new tables and columns are silently absorbed. Keep up and down in sync — every new `CREATE TABLE` gets a matching `DROP TABLE` in the down file at the right teardown order (FK-dependent rows drop before parents).
- **Service-first.** New transport never bypasses the service layer; the service layer is where owner scoping and hidden-gate semantics live. CLI subcommands that mutate DB state must go through service, never repo. Tag/caption read paths through `internal/service/ai/` — model the new `internal/service/search/` after them.
- **Hidden-gate symmetry.** Every search/autocomplete read path takes `include_hidden bool` and an unlock-claim-bearing context. The numerator and denominator of `embedding_completeness` must be evaluated under the same hidden predicate as the rest of the request — never let the pill leak hidden-only progress to a locked session.
- **Fingerprint discipline.** Every `ai_jobs` row carries the fingerprint it was enqueued under. The active fingerprint at boot derives from server config; the embed worker resolves the matching `embedding_generations` row by `fingerprint_hash`. Never write a vec row to a generation whose fingerprint disagrees with the job's.
- **`vec_id` allocation.** Per-generation `MAX(vec_id)+1` (or 1 when empty), inside the same write transaction as the mapping insert. SQLite serializes writes through the single RW connection, so MAX+1 is safe. Never allocate `vec_id` outside the write transaction.
- **`embedded_count` honesty.** The cached counter ticks only on net-new mappings (DELETE-RETURNING returned no row); replacements are zero-delta. The activator never trusts the cached counter — it always recounts assertively against the same predicate as the eligible query so numerator and denominator agree.
- **vec_table_name safe-eval.** `"media_embeddings_g" + strconv.FormatInt(id, 10)`. Never user-supplied. Never derived from input strings. The only place a vec-table-name string is built is one helper inside `internal/ai/embedding/generations.go`.
- **FTS maintenance is application-owned.** Triggers fire in arbitrary order and racing the AI promotion transaction is fragile. `RefreshMediaFTS(tx, mediaID)` is the single entry point; every code path that mutates the six-column corpus calls it inside the same transaction. The only schema-side trigger is the cleanup-on-`DELETE FROM media`.
- **Filter resolution is owner-scoped.** `filter.Resolve(caller, in)` always asserts ownership on every chip. Tag chips resolve to `tag_key`; location chips to exact `location_label`. Substring matching lives in autocomplete only, never in the committed filter.
- **Cursor `req_hash`.** The hash covers normalized `q`, sorted-key serialized filters, **effective sort** (after the `q == "" && sort == "relevance" → "newest"` coercion), `include_hidden`, and the engine mode (`hybrid` / `bm25_only` / `filter_only`). Mismatches return HTTP 400.
- **`/v1/embeddings` query-time degradation.** Embedding-endpoint failure during query embed must not 500 the whole `/search` request. Degrade to BM25-only with `semantic_unavailable=true`, `semantic_unavailable_reason="query_embedding_failed"`. Log once per request with the response code/body.
- **Effective-sort coercion.** The only case where `effective_sort` differs from request `sort` is `q == "" && sort == "relevance" → "newest"`. Semantic fallback (no active generation, query embed failed) does **not** change `effective_sort` — it surfaces through `semantic_unavailable` + `semantic_unavailable_reason`.
- **Trailing prefix.** Last incomplete token of `q` (when `q` does not end in whitespace) gets `*`. Bounded — no other prefix expansion in the query.
- **LIKE escaping.** Autocomplete prefix/substring is run through a small `%`/`_`/`\` doubling escaper, then `LIKE ? ESCAPE '\'`. Same pattern as the existing media filter substring matcher; reuse the helper if one exists.
- **Tests.** `testutil.OpenTestDB(t)` is the canonical entry. `vec_version()` is available in tests post-migration. Use `require.ErrorIs` for sentinel checks — never `==`.
- **Errors.** New sentinels live in `internal/errs/errs.go` only when no existing one fits. Most search-side errors map cleanly to `ErrNotFound`/`ErrInvalidArgument`/`ErrPermissionDenied`. The HTTP translator stays as-is unless a new mapping is genuinely needed (it isn't — the existing `Translate` covers everything search returns).
- **Frontend.** Bun + Vite + Svelte 5 runes. Tests via Vitest + `@testing-library/svelte`. Use `router.navigate(..., { replace: true })` for in-search-typing; push a real history entry only on Enter or route entry. ⌘K already focuses the AppHeader input today — extend, don't replace.
- **Commits.** Conventional, ≤72 char subject, scoped (`feat(search):`, `feat(ai/embed):`, `feat(httpapi/search):`, `test(search/hybrid):`, `chore(metrics):`). One logical change per commit. Don't bypass pre-commit hooks. After every 5th task, run `/roborev-fix` and commit any cleanup it surfaces.
- **Pre-commit hooks.** `make api-generate` runs whenever `go.mod`/`go.sum`/`internal/httpapi/*.go`/`internal/db/*.go`/`cmd/fotobank-openapi/*.go` change — every HTTP route addition triggers an OpenAPI regeneration and a TypeScript schema regeneration. Don't hand-edit the generated TS — re-run the hook.

---

## Section A — Schema additions

### Task A1 — Widen AI task CHECK constraints to include 'embed'

**Files:**
- Modify: `internal/db/migrations/000001_initial_schema.up.sql`
- Modify: `internal/db/migrations/000001_initial_schema.down.sql` (no change required if the down file is wholesale; verify)
- Modify: `internal/ai/types.go` — add `TaskEmbed` const and extend `Task.Valid()`
- Modify: `internal/ai/types_test.go` — extend the `Valid()` test table
- Modify: `internal/ai/gapscanner/scanner.go` — extend the task-iterating helpers (if any switch on `Task`)
- Modify: `internal/cli/ai.go` — accept `embed` in `--task` argument validation

**Step 1: Test — Task validation accepts embed.**

```go
// internal/ai/types_test.go (extend the existing TestTask_Valid)
{name: "embed", task: ai.TaskEmbed, want: true},
```

Run: `go test ./internal/ai/ -run "TestTask_Valid" -count=1` → fails (constant `TaskEmbed` undefined).

**Step 2: Add the constant + extend Valid.**

```go
// internal/ai/types.go
const TaskEmbed Task = "embed"

func (t Task) Valid() bool { return t == TaskTag || t == TaskCaption || t == TaskEmbed }
```

**Step 3: Test — task constraint at the schema level rejects unknowns and accepts 'embed'.**

```go
// internal/db/db_test.go (append)
func TestSchema_AIJobsAcceptsEmbedTask(t *testing.T) {
	d := testutil.OpenTestDB(t)
	owner := seedOwner(t, d)
	mediaID := seedMedia(t, d, owner)

	// Accepts 'embed'.
	_, err := d.WriteDB().Exec(
		`INSERT INTO ai_jobs(id, media_id, task, fingerprint, status, attempts, enqueued_at)
		 VALUES (?, ?, 'embed', 'fp', 'pending', 0, datetime('now'))`,
		uuid.NewString(), mediaID,
	)
	require.NoError(t, err)

	// Rejects 'classify'.
	_, err = d.WriteDB().Exec(
		`INSERT INTO ai_jobs(id, media_id, task, fingerprint, status, attempts, enqueued_at)
		 VALUES (?, ?, 'classify', 'fp', 'pending', 0, datetime('now'))`,
		uuid.NewString(), mediaID,
	)
	require.Error(t, err)
}
```

(`seedOwner`/`seedMedia` lift from existing test patterns; if the package doesn't have helpers yet, inline the inserts. Do not introduce new shared helpers in this task.)

**Step 4: Update the four CHECK constraints.**

In `internal/db/migrations/000001_initial_schema.up.sql`, replace `CHECK (task IN ('tag','caption'))` with `CHECK (task IN ('tag','caption','embed'))` on:
- `ai_results.task`
- `ai_jobs.task`
- `ai_failures.task`
- `ai_skipped.task`

**Step 5: Verify.**

Run: `go test ./internal/ai/ ./internal/db/ -count=1`

Expected: PASS.

**Step 6: Update CLI argument validator.**

In `internal/cli/ai.go`, locate the `--task` flag's validator (any `switch task { case "tag", "caption": ...; default: ...err... }`). Extend to accept `"embed"`.

Run: `go test ./internal/cli/ -count=1 -run "TestAI"` → PASS.

**Step 7: Commit.**

```bash
git add internal/db/migrations/000001_initial_schema.up.sql internal/ai/types.go internal/ai/types_test.go internal/db/db_test.go internal/cli/ai.go
git commit -m "feat(ai): accept embed task across schema, types, and CLI"
```

---

### Task A2 — Add `media.lens_model` and `media_owner_type_idx`

**Files:**
- Modify: `internal/db/migrations/000001_initial_schema.up.sql`
- Modify: `internal/db/migrations/000001_initial_schema.down.sql` (no change if wholesale; verify)
- Modify: `internal/media/repo.go` — extend `Media` struct with `LensModel *string`; SELECT/INSERT/UPDATE column lists
- Modify: `internal/media/repo_test.go` — round-trip test for `LensModel`
- Modify: `internal/exifread/read.go` — surface `LensModel` from EXIF (`LensModel` IFD tag)
- Modify: `internal/exifread/read_test.go` — fixture with a known LensModel value
- Modify: `internal/ingest/...` (importer) — pass `LensModel` from EXIF to media insert

**Step 1: Add the column.**

Inside `CREATE TABLE media (...)` add:

```sql
lens_model        TEXT,
```

Place it next to `make`, `model`, `focal_length` in the column list. Add the matching `media_owner_type_idx` after the existing media indexes:

```sql
CREATE INDEX media_owner_type_idx
    ON media(owner_hub, owner_user_id, media_type)
    WHERE hidden_at IS NULL;
```

**Step 2: Test — media row round-trips lens_model.**

```go
// internal/media/repo_test.go (append)
func TestRepo_LensModelRoundTrip(t *testing.T) {
	d := testutil.OpenTestDB(t)
	r := media.NewRepo(d.WriteDB(), d.ReadDB())
	owner := seedOwner(t, d)

	lens := "EF 50mm f/1.8 STM"
	id, err := r.Insert(ctx, media.NewMedia{
		Owner: owner,
		// ... required fields ...
		LensModel: &lens,
	})
	require.NoError(t, err)

	got, err := r.GetByID(ctx, id)
	require.NoError(t, err)
	require.NotNil(t, got.LensModel)
	require.Equal(t, lens, *got.LensModel)
}
```

Run: fails — `media.NewMedia.LensModel` and `Media.LensModel` undefined.

**Step 3: Extend the Media struct + INSERT/SELECT.**

In `internal/media/repo.go`:
- Add `LensModel *string` to the `Media` struct (next to `FocalLength *string`).
- Add `LensModel *string` to the `NewMedia` insert input.
- Update the `INSERT INTO media (...)` column list and `?` placeholders to include `lens_model`.
- Update every `SELECT` that hydrates a `Media` to include `lens_model`.

Capture the SELECT lists by `grep -n 'SELECT' internal/media/repo.go` to make sure none are missed.

**Step 4: EXIF + importer wiring.**

In `internal/exifread/read.go`, if the reader returns a struct with named fields, add `LensModel string`. The IFD tag is `LensModel` (0xA434 in the Exif Photo IFD); `go-exif` and the existing pure-Go reader both expose it.

In `internal/ingest/...`, locate the call site that constructs `media.NewMedia{...}` from EXIF and add `LensModel: &exif.LensModel` (or `nil` when empty).

**Step 5: Run.**

Run: `go test ./internal/media/ ./internal/exifread/ ./internal/ingest/ -count=1`

Expected: PASS.

**Step 6: Reconcile backfill for existing rows.**

Add a one-time pass to `internal/reconcile/...` (existing reconcile subcommand) that re-reads EXIF on visited rows and writes `lens_model` if currently NULL. Implement as an additive UPDATE inside the existing reconcile transaction; do **not** add a separate migration.

Test the backfill:

```go
// internal/reconcile/reconcile_test.go (append)
func TestReconcile_BackfillLensModelWhenNull(t *testing.T) { /* … */ }
```

**Step 7: Commit.**

```bash
git add internal/db/migrations/000001_initial_schema.up.sql internal/media/ internal/exifread/ internal/ingest/ internal/reconcile/
git commit -m "feat(media): add lens_model column with EXIF + reconcile wiring"
```

---

### Task A3 — Add `embedding_generations` and `media_embedding_ids`

**Files:**
- Modify: `internal/db/migrations/000001_initial_schema.up.sql`
- Modify: `internal/db/migrations/000001_initial_schema.down.sql` — no change if wholesale; verify

**Step 1: Append the two tables and partial unique indexes.**

```sql
-- ============================================================
-- Search v1 — embedding generations and per-media vec mapping.
-- See docs/superpowers/specs/2026-05-01-fotobank-search-design.md §5.3-§5.4.
-- ============================================================

CREATE TABLE embedding_generations (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    fingerprint      TEXT    NOT NULL UNIQUE,
    fingerprint_hash TEXT    NOT NULL UNIQUE,
    model_id         TEXT    NOT NULL,
    input_profile    TEXT    NOT NULL,
    vec_table_name   TEXT    NOT NULL UNIQUE,
    dimension        INTEGER NOT NULL,
    state            TEXT    NOT NULL CHECK(state IN ('building','active','retired')),
    embedded_count   INTEGER NOT NULL DEFAULT 0,
    threshold_pct    INTEGER NOT NULL DEFAULT 95,
    created_at       TIMESTAMP NOT NULL,
    activated_at     TIMESTAMP,
    retired_at       TIMESTAMP
);

CREATE UNIQUE INDEX embedding_generations_one_active
    ON embedding_generations(state) WHERE state = 'active';
CREATE UNIQUE INDEX embedding_generations_one_building
    ON embedding_generations(state) WHERE state = 'building';

CREATE TABLE media_embedding_ids (
    generation_id INTEGER NOT NULL REFERENCES embedding_generations(id) ON DELETE CASCADE,
    media_id      UUID    NOT NULL REFERENCES media(id) ON DELETE CASCADE,
    vec_id        INTEGER NOT NULL,
    PRIMARY KEY (generation_id, media_id),
    UNIQUE (generation_id, vec_id)
);
CREATE INDEX media_embedding_ids_media_idx
    ON media_embedding_ids(media_id);
```

**Step 2: Test — partial unique indexes enforce one-active and one-building.**

```go
// internal/db/db_test.go (append)
func TestSchema_EmbeddingGenerationsOneActiveOneBuilding(t *testing.T) {
	d := testutil.OpenTestDB(t)

	mustInsertGen := func(state string) {
		_, err := d.WriteDB().Exec(
			`INSERT INTO embedding_generations
			 (fingerprint, fingerprint_hash, model_id, input_profile, vec_table_name,
			  dimension, state, created_at)
			 VALUES (?, ?, 'm', 'p', ?, 768, ?, datetime('now'))`,
			"fp-"+state, "h-"+state, "media_embeddings_g_"+state, state,
		)
		require.NoError(t, err)
	}

	mustInsertGen("active")
	// Second active must fail.
	_, err := d.WriteDB().Exec(
		`INSERT INTO embedding_generations
		 (fingerprint, fingerprint_hash, model_id, input_profile, vec_table_name,
		  dimension, state, created_at)
		 VALUES ('fp2','h2','m','p','t2',768,'active', datetime('now'))`,
	)
	require.Error(t, err, "must reject two active generations")

	mustInsertGen("building")
	// Second building must fail.
	_, err = d.WriteDB().Exec(
		`INSERT INTO embedding_generations
		 (fingerprint, fingerprint_hash, model_id, input_profile, vec_table_name,
		  dimension, state, created_at)
		 VALUES ('fp3','h3','m','p','t3',768,'building', datetime('now'))`,
	)
	require.Error(t, err, "must reject two building generations")
}
```

Run → expect FAIL initially (constraint enforced after migration applied; the test runs against a fresh migrated DB so it should PASS once the schema is in). If FAIL, the migration didn't include the partial unique indexes — re-check the SQL.

**Step 3: Test — `media_embedding_ids.UNIQUE (generation_id, vec_id)` enforced.**

```go
func TestSchema_MediaEmbeddingIDsUniqueVecID(t *testing.T) {
	d := testutil.OpenTestDB(t)
	owner := seedOwner(t, d)
	m1 := seedMedia(t, d, owner)
	m2 := seedMedia(t, d, owner)

	// Insert a generation row.
	res, err := d.WriteDB().Exec(
		`INSERT INTO embedding_generations
		 (fingerprint, fingerprint_hash, model_id, input_profile, vec_table_name,
		  dimension, state, created_at)
		 VALUES ('fp','h','m','p','media_embeddings_g1',768,'building', datetime('now'))`,
	)
	require.NoError(t, err)
	gid, _ := res.LastInsertId()

	require.NoError(t, insertMapping(d, gid, m1, 1))
	// Second mapping with the same vec_id must fail.
	require.Error(t, insertMapping(d, gid, m2, 1))
}
```

**Step 4: Run.**

Run: `go test ./internal/db/ -count=1 -run "TestSchema_"`

Expected: PASS.

**Step 5: Commit.**

```bash
git add internal/db/migrations/000001_initial_schema.up.sql internal/db/db_test.go
git commit -m "feat(search): embedding_generations + media_embedding_ids tables"
```

---

### Task A4 — Add `media_fts` (FTS5) + cleanup trigger

**Files:**
- Modify: `internal/db/migrations/000001_initial_schema.up.sql`

**Step 1: Append the virtual table and trigger.**

```sql
-- ============================================================
-- Search v1 — FTS5 lexical index over the §7 corpus.
-- ============================================================

CREATE VIRTUAL TABLE media_fts USING fts5(
    media_id UNINDEXED,
    caption_text,
    tag_label,
    filename,
    camera,
    lens,
    location_label,
    tokenize = 'porter unicode61 remove_diacritics 2'
);

CREATE TRIGGER media_fts_cleanup_after_delete
AFTER DELETE ON media
FOR EACH ROW
BEGIN
    DELETE FROM media_fts WHERE media_id = OLD.id;
END;
```

Note: FTS5 virtual tables don't accept secondary indexes, so there is **no** `CREATE INDEX media_fts(media_id)`.

**Step 2: Test — the table exists and accepts inserts; cleanup trigger fires.**

```go
// internal/db/db_test.go (append)
func TestSchema_MediaFTSPresentAndDeletable(t *testing.T) {
	d := testutil.OpenTestDB(t)
	owner := seedOwner(t, d)
	mid := seedMedia(t, d, owner)

	_, err := d.WriteDB().Exec(
		`INSERT INTO media_fts (media_id, caption_text, tag_label, filename, camera, lens, location_label)
		 VALUES (?, 'small dog on a beach', 'dog beach', 'IMG_0001.jpg', 'Canon EOS R5', '', 'Paris, France')`,
		mid,
	)
	require.NoError(t, err)

	// MATCH works.
	var got int
	require.NoError(t, d.ReadDB().QueryRow(
		`SELECT COUNT(*) FROM media_fts WHERE media_fts MATCH ?`, "dog AND beach",
	).Scan(&got))
	require.Equal(t, 1, got)

	// Delete cascade via media trigger.
	_, err = d.WriteDB().Exec(`DELETE FROM media WHERE id = ?`, mid)
	require.NoError(t, err)

	require.NoError(t, d.ReadDB().QueryRow(
		`SELECT COUNT(*) FROM media_fts WHERE media_id = ?`, mid,
	).Scan(&got))
	require.Equal(t, 0, got, "media_fts row must be cleaned up by trigger")
}
```

**Step 3: Run.**

Run: `go test ./internal/db/ -count=1 -run "TestSchema_MediaFTS"`

Expected: PASS.

**Step 4: Commit.**

```bash
git add internal/db/migrations/000001_initial_schema.up.sql internal/db/db_test.go
git commit -m "feat(search): media_fts FTS5 index and cleanup trigger"
```

---

### Task A5 — Sanity test the full schema migrates and key tables interact

**Files:**
- Modify: `internal/db/db_test.go` (append)

**Step 1: Test — a single transaction can write a media row, an embedding generation, a vec mapping (without the vec0 table yet — that lands in F2), and an FTS row, then read them all back.**

```go
func TestSchema_FullMigrationSmoke(t *testing.T) {
	d := testutil.OpenTestDB(t)
	owner := seedOwner(t, d)
	mid := seedMedia(t, d, owner)

	tx, err := d.WriteDB().Begin()
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()

	res, err := tx.Exec(
		`INSERT INTO embedding_generations
		 (fingerprint, fingerprint_hash, model_id, input_profile, vec_table_name,
		  dimension, state, created_at)
		 VALUES (?, ?, ?, ?, ?, 768, 'building', datetime('now'))`,
		"siglip|jpeg-384", "h", "siglip2", "jpeg-384", "media_embeddings_g1",
	)
	require.NoError(t, err)
	gid, _ := res.LastInsertId()

	_, err = tx.Exec(
		`INSERT INTO media_embedding_ids (generation_id, media_id, vec_id) VALUES (?, ?, 1)`,
		gid, mid,
	)
	require.NoError(t, err)

	_, err = tx.Exec(
		`INSERT INTO media_fts (media_id, caption_text, tag_label, filename, camera, lens, location_label)
		 VALUES (?, '', '', '', '', '', '')`, mid,
	)
	require.NoError(t, err)

	require.NoError(t, tx.Commit())
}
```

**Step 2: Run.**

Run: `go test ./internal/db/ -count=1 -run "TestSchema_FullMigrationSmoke"`

Expected: PASS.

**Step 3: Commit.**

```bash
git add internal/db/db_test.go
git commit -m "test(db): full-schema smoke for search-v1 additions"
```

---

> **Cleanup checkpoint 1.** Run `/roborev-fix` on the current branch. Address any findings before starting Task B1. Commit any cleanup as its own commit(s); do not amend.

---

## Section B — Configuration

### Task B1 — Add `[ai.embed]` and `[search]` config blocks

**Files:**
- Modify: `internal/ai/config.go` — add `EmbedConfig`; defaults; validate
- Create: `internal/search/config.go` — `[search]` block with defaults
- Modify: `internal/ai/config_test.go`
- Create: `internal/search/config_test.go`
- Modify: `internal/config/...` (the top-level loader) — wire the two new blocks into the config struct

**Step 1: Test — `[ai.embed]` validation rejects empty model when enabled.**

```go
// internal/ai/config_test.go (append)
func TestConfig_EmbedRequiresModelWhenEnabled(t *testing.T) {
	c := &ai.Config{Enabled: true, Vision: ai.VisionConfig{Endpoint: "http://x"},
		Embed: ai.EmbedConfig{Enabled: true /* Model: "" */}}
	c.ApplyDefaults()
	require.ErrorContains(t, c.Validate(), "ai.embed.model")
}

func TestConfig_EmbedDefaultsApply(t *testing.T) {
	c := &ai.Config{Embed: ai.EmbedConfig{Enabled: true, Model: "siglip2", Endpoint: "http://x", Dimension: 768}}
	c.ApplyDefaults()
	require.Equal(t, 384, c.Embed.InputEdge)
	require.Equal(t, 32, c.Embed.BatchSize)
	require.Equal(t, 1, c.Embed.MaxRetries)
	require.Equal(t, 10*time.Second, c.Embed.Timeout)
}
```

**Step 2: Add `EmbedConfig` to `internal/ai/config.go`.**

```go
type EmbedConfig struct {
	Enabled           bool          `toml:"enabled"`
	Model             string        `toml:"model"`
	Endpoint          string        `toml:"endpoint"`
	APIKeyEnv         string        `toml:"api_key_env"`
	Dimension         int           `toml:"dimension"`
	InputEdge         int           `toml:"input_edge"`
	WorkerConcurrency int           `toml:"worker_concurrency"`
	BatchSize         int           `toml:"batch_size"`
	MaxRetries        int           `toml:"max_retries"`
	Timeout           time.Duration `toml:"timeout"`
}

func (e EmbedConfig) APIKey() string {
	if e.APIKeyEnv == "" { return "" }
	return os.Getenv(e.APIKeyEnv)
}
```

In `Config`:
```go
type Config struct {
	Enabled bool         `toml:"enabled"`
	Vision  VisionConfig `toml:"vision"`
	Tag     TaskConfig   `toml:"tag"`
	Caption TaskConfig   `toml:"caption"`
	Embed   EmbedConfig  `toml:"embed"`
}
```

`ApplyDefaults`:
```go
if c.Embed.InputEdge <= 0 { c.Embed.InputEdge = 384 }
if c.Embed.BatchSize <= 0 { c.Embed.BatchSize = 32 }
if c.Embed.WorkerConcurrency < 1 { c.Embed.WorkerConcurrency = 1 }
if c.Embed.MaxRetries < 0 { c.Embed.MaxRetries = 1 }
if c.Embed.Timeout <= 0 { c.Embed.Timeout = 10 * time.Second }
```

`Validate`:
```go
if c.Embed.Enabled {
	if c.Embed.Model == "" { return fmt.Errorf("ai.embed.model: required when ai.embed.enabled=true") }
	if c.Embed.Endpoint == "" { return fmt.Errorf("ai.embed.endpoint: required when ai.embed.enabled=true") }
	if u, err := url.Parse(c.Embed.Endpoint); err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("ai.embed.endpoint: must be http(s) URL with host (got %q)", c.Embed.Endpoint)
	}
	if c.Embed.Dimension <= 0 { return fmt.Errorf("ai.embed.dimension: must be > 0 when enabled") }
}
```

**Step 3: Create `internal/search/config.go`.**

```go
// Package search owns the [search] config block.
package search

type Config struct {
	KPerSignal           int `toml:"k_per_signal"`
	RRFK                 int `toml:"rrf_k"`
	RetainRetiredDays    int `toml:"retain_retired_days"`
	ActivationThresholdPct int `toml:"activation_threshold"`
}

func (c *Config) ApplyDefaults() {
	if c.KPerSignal <= 0 { c.KPerSignal = 200 }
	if c.RRFK <= 0 { c.RRFK = 60 }
	if c.RetainRetiredDays <= 0 { c.RetainRetiredDays = 30 }
	if c.ActivationThresholdPct <= 0 { c.ActivationThresholdPct = 95 }
}
```

**Step 4: Test the search config defaults.**

```go
// internal/search/config_test.go
func TestConfig_DefaultsApply(t *testing.T) {
	c := &search.Config{}
	c.ApplyDefaults()
	require.Equal(t, 200, c.KPerSignal)
	require.Equal(t, 60, c.RRFK)
	require.Equal(t, 30, c.RetainRetiredDays)
	require.Equal(t, 95, c.ActivationThresholdPct)
}
```

**Step 5: Wire into the top-level loader.**

In `internal/config/...`, add fields to the top-level Config struct: `Search search.Config` (TOML key `search`). Call `ApplyDefaults` on both `Embed` (already inside `ai.Config.ApplyDefaults`) and `Search` from the loader's `ApplyDefaults`.

**Step 6: Run.**

Run: `go test ./internal/ai/ ./internal/search/ ./internal/config/ -count=1`

Expected: PASS.

**Step 7: Commit.**

```bash
git add internal/ai/config.go internal/ai/config_test.go internal/search/config.go internal/search/config_test.go internal/config/
git commit -m "feat(search): [ai.embed] and [search] config blocks"
```

---

## Section C — Image input refactor

### Task C1 — Split `Resolver.ResolveAndEncode` into `ResolvePreviewJPEG`

**Files:**
- Modify: `internal/ai/imginput/resolver.go`
- Modify: `internal/ai/imginput/resolver_test.go`

**Step 1: Test — new method returns the raw preview bytes + status without re-encoding.**

```go
// internal/ai/imginput/resolver_test.go (append)
func TestResolver_ResolvePreviewJPEG_ReturnsRawBytes(t *testing.T) {
	store, mediaID := /* fixture */
	r := imginput.NewResolver(d.ReadDB(), store)

	jpg, status, err := r.ResolvePreviewJPEG(ctx, mediaID)
	require.NoError(t, err)
	require.Equal(t, "ready", status)
	// Bytes match the on-disk preview verbatim — no re-encode.
	expected, _ := io.ReadAll(must(store.ReadRange(ctx, owner, key, 0, -1)))
	require.True(t, bytes.Equal(expected, jpg))
}
```

**Step 2: Add the method.**

```go
// internal/ai/imginput/resolver.go (append; do NOT delete ResolveAndEncode yet)
func (r *Resolver) ResolvePreviewJPEG(ctx context.Context, mediaID string) ([]byte, string, error) {
	var (
		hub, userID, status string
		version             int
	)
	row := r.ro.QueryRowContext(ctx,
		`SELECT owner_hub, owner_user_id, thumb_status, thumb_version FROM media WHERE id=?`, mediaID)
	switch err := row.Scan(&hub, &userID, &status, &version); {
	case errors.Is(err, sql.ErrNoRows):
		return nil, "", fmt.Errorf("media %s not found", mediaID)
	case err != nil:
		return nil, "", fmt.Errorf("read media %s: %w", mediaID, err)
	}
	if status != "ready" {
		return nil, status, nil
	}
	owner := owners.Principal{Hub: hub, UserID: userID}
	key := thumb.ThumbKey(mediaID, version, thumb.SizePreview)
	rc, err := r.store.ReadRange(ctx, owner, key, 0, -1)
	if err != nil { return nil, status, fmt.Errorf("read preview %s: %w", key, err) }
	defer func() { _ = rc.Close() }()
	jpg, err := io.ReadAll(rc)
	if err != nil { return nil, status, fmt.Errorf("read preview %s: %w", key, err) }
	return jpg, status, nil
}
```

**Step 3: Run.** Test passes.

**Step 4: Commit.**

```bash
git add internal/ai/imginput/resolver.go internal/ai/imginput/resolver_test.go
git commit -m "feat(ai/imginput): add ResolvePreviewJPEG primitive"
```

---

### Task C2 — Move chat encoder to `encode.EncodeChat`; add `EncodeEmbed`; migrate callers

**Files:**
- Create: `internal/ai/imginput/encode/encode.go`
- Create: `internal/ai/imginput/encode/encode_test.go`
- Modify: `internal/ai/imginput/encode.go` (existing chat encoder body) — remove or thin-wrap
- Modify: every caller of the old `imginput.Encode` (chat workers) — switch to `encode.EncodeChat`
- Modify: `internal/ai/imginput/resolver.go` — `ResolveAndEncode` becomes a thin wrapper around `ResolvePreviewJPEG` + `encode.EncodeChat`, OR delete it once all callers migrate

**Step 1: Test — `EncodeChat` matches the existing encoder output bit-for-bit on a fixture.**

```go
// internal/ai/imginput/encode/encode_test.go
func TestEncodeChat_MatchesExisting(t *testing.T) {
	src := mustReadTestJPEG(t, "testdata/sample.jpg")
	got, err := encode.EncodeChat(src)
	require.NoError(t, err)
	want, _ := imginput.Encode(src) // existing entrypoint
	require.True(t, bytes.Equal(want, got), "byte-for-byte equivalence")
}

func TestEncodeEmbed_RespectsEdge(t *testing.T) {
	src := mustReadTestJPEG(t, "testdata/sample.jpg")
	got, err := encode.EncodeEmbed(src, 384)
	require.NoError(t, err)
	w, h := decodeJPEGDims(t, got)
	require.True(t, w == 384 || h == 384, "longest edge must be 384 (got %dx%d)", w, h)
}
```

**Step 2: Implement.**

```go
// internal/ai/imginput/encode/encode.go
package encode

import (
	"bytes"
	"image/jpeg"
	"github.com/wesm/fotobank/internal/ai/imginput" // existing helpers if any
)

// EncodeChat re-encodes srcJPEG to the chat-task input profile:
// 1024-edge resize, q85 JPEG, EXIF metadata stripped.
func EncodeChat(srcJPEG []byte) ([]byte, error) {
	return encodeJPEG(srcJPEG, 1024, 85)
}

// EncodeEmbed re-encodes srcJPEG to the embed-task input profile:
// caller-specified edge, q85 JPEG, EXIF metadata stripped.
func EncodeEmbed(srcJPEG []byte, edge int) ([]byte, error) {
	if edge <= 0 { return nil, fmt.Errorf("encode embed: edge must be > 0") }
	return encodeJPEG(srcJPEG, edge, 85)
}

// encodeJPEG resizes the longest edge of src to edge px, q-encodes
// the result as a JPEG, and strips metadata. The function is the
// existing pure-Go pipeline lifted from imginput.Encode.
func encodeJPEG(src []byte, edge, quality int) ([]byte, error) { /* … */ }
```

(Lift the resize + encode body from the existing `internal/ai/imginput/encode.go::Encode`. The resize math is the same; quality is the same.)

**Step 3: Migrate chat-task workers.**

Find every call to the old `imginput.Encode` or `imginput.ResolveAndEncode`:

```bash
grep -rn "imginput.Encode\b\|imginput\.ResolveAndEncode" internal/
```

Each chat-task call site changes from:

```go
out, status, err := r.ResolveAndEncode(ctx, mid)
```

to:

```go
jpg, status, err := r.ResolvePreviewJPEG(ctx, mid)
if err != nil || status != "ready" { return ..., status, err }
out, err := encode.EncodeChat(jpg)
```

**Step 4: Remove the old wrapper.**

Once no callers reference `imginput.Encode` or `imginput.ResolveAndEncode` anymore, delete those entrypoints from `internal/ai/imginput/`. Update tests accordingly. Run `go vet ./...` to confirm no unused imports remain.

**Step 5: Run.**

Run: `go test ./internal/ai/... ./internal/cli/... -count=1`

Expected: PASS.

**Step 6: Commit.**

```bash
git add internal/ai/imginput/
git commit -m "refactor(ai/imginput): split chat encoder into encode.EncodeChat"
```

---

## Section D — Embedding gateway client

### Task D1 — `Client` interface + OpenAI-compat impl

**Files:**
- Create: `internal/ai/embedding/client.go`
- Create: `internal/ai/embedding/client_test.go`

**Step 1: Test — single-batch image request.**

```go
// internal/ai/embedding/client_test.go
func TestClient_BatchImagesReturnsVectorsByIndex(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/embeddings", r.URL.Path)
		var body struct { Input []string; Model string }
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Len(t, body.Input, 2)
		require.Equal(t, "siglip2", body.Model)
		// Reply with two distinct 768-dim vectors keyed by index.
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"embedding":`+vec(768, 0.1)+`,"index":0},{"embedding":`+vec(768, 0.2)+`,"index":1}],"model":"siglip2"}`)
	}))
	defer srv.Close()

	c := embedding.NewClient(embedding.Config{
		Endpoint: srv.URL, Model: "siglip2", Dimension: 768, Timeout: 5 * time.Second,
	})

	out, err := c.EmbedImages(ctx, [][]byte{[]byte("a-bytes"), []byte("b-bytes")})
	require.NoError(t, err)
	require.Len(t, out, 2)
	require.Len(t, out[0], 768)
	require.NotEqual(t, out[0], out[1])
}

func TestClient_RejectsDimensionMismatchAsMalformed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"data":[{"embedding":[0.1,0.2,0.3],"index":0}],"model":"siglip2"}`)
	}))
	defer srv.Close()

	c := embedding.NewClient(embedding.Config{Endpoint: srv.URL, Model: "siglip2", Dimension: 768, Timeout: 5 * time.Second})
	_, err := c.EmbedImages(ctx, [][]byte{[]byte("x")})
	require.ErrorIs(t, err, embedding.ErrMalformed)
}

func TestClient_4xxIsPermanent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"bad model"}`, http.StatusBadRequest)
	}))
	defer srv.Close()
	c := embedding.NewClient(embedding.Config{Endpoint: srv.URL, Model: "x", Dimension: 768, Timeout: 5 * time.Second})
	_, err := c.EmbedImages(ctx, [][]byte{[]byte("x")})
	require.ErrorIs(t, err, embedding.ErrProvider4xx)
}

func TestClient_5xxRetriedAndEventuallyTransient(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := embedding.NewClient(embedding.Config{Endpoint: srv.URL, Model: "x", Dimension: 768, Timeout: 5 * time.Second, MaxRetries: 1})
	_, err := c.EmbedImages(ctx, [][]byte{[]byte("x")})
	require.ErrorIs(t, err, embedding.ErrTransient)
	require.GreaterOrEqual(t, hits, 2, "must retry once on 5xx")
}
```

**Step 2: Implement.**

```go
// internal/ai/embedding/client.go
package embedding

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

var (
	ErrTransient    = errors.New("embedding: transient")
	ErrProvider4xx  = errors.New("embedding: provider 4xx")
	ErrMalformed    = errors.New("embedding: malformed response")
)

type Config struct {
	Endpoint   string
	APIKey     string
	Model      string
	Dimension  int
	Timeout    time.Duration
	MaxRetries int
}

type Client struct {
	cfg  Config
	http *http.Client
}

func NewClient(cfg Config) *Client {
	return &Client{cfg: cfg, http: &http.Client{Timeout: cfg.Timeout}}
}

// EmbedImages sends images as data URLs and returns one float32 vector
// per input, in input order. Validates each returned vector has the
// configured Dimension; mismatched dimensions fail the batch with
// ErrMalformed (re-running won't help — config drift).
func (c *Client) EmbedImages(ctx context.Context, jpegs [][]byte) ([][]float32, error) {
	inputs := make([]string, len(jpegs))
	for i, b := range jpegs {
		inputs[i] = "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(b)
	}
	return c.callOnce(ctx, inputs)
}

// EmbedTexts is the query-time counterpart. The configured embed model
// must produce shared image-text embeddings for hybrid ranking to work.
func (c *Client) EmbedTexts(ctx context.Context, texts []string) ([][]float32, error) {
	return c.callOnce(ctx, texts)
}

func (c *Client) callOnce(ctx context.Context, input []string) ([][]float32, error) {
	body, _ := json.Marshal(map[string]any{"input": input, "model": c.cfg.Model})

	var lastErr error
	for attempt := 0; attempt <= c.cfg.MaxRetries; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.Endpoint+"/embeddings", bytes.NewReader(body))
		if err != nil { return nil, fmt.Errorf("build request: %w", err) }
		req.Header.Set("Content-Type", "application/json")
		if c.cfg.APIKey != "" { req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey) }

		resp, err := c.http.Do(req)
		if err != nil { lastErr = fmt.Errorf("%w: %v", ErrTransient, err); continue }

		out, classification, perr := parse(resp, c.cfg.Dimension)
		_ = resp.Body.Close()
		switch classification {
		case classOK:
			return out, nil
		case class4xx:
			return nil, fmt.Errorf("%w: %v", ErrProvider4xx, perr)
		case classMalformed:
			return nil, fmt.Errorf("%w: %v", ErrMalformed, perr)
		case classTransient:
			lastErr = fmt.Errorf("%w: %v", ErrTransient, perr)
			continue
		}
	}
	if lastErr == nil { lastErr = fmt.Errorf("%w: exhausted retries", ErrTransient) }
	return nil, lastErr
}

// parse classifies the HTTP response and decodes embeddings on success.
// Returns one of {classOK, class4xx, classMalformed, classTransient} plus an error
// describing the failure (nil on classOK).
func parse(resp *http.Response, dim int) ([][]float32, classification, error) { /* … */ }
```

(Helper `parse` checks the status code, decodes the OpenAI-shaped envelope, and validates dimension. 4xx → permanent. 429+5xx → transient. JSON decode/dim mismatch → malformed.)

**Step 3: Run.**

Run: `go test ./internal/ai/embedding/ -run "TestClient_" -count=1`

Expected: PASS.

**Step 4: Commit.**

```bash
git add internal/ai/embedding/client.go internal/ai/embedding/client_test.go
git commit -m "feat(ai/embedding): OpenAI-compat embeddings client"
```

---

### Task D2 — Boot-time modality + dimension probe

**Files:**
- Create: `internal/ai/embedding/probe.go`
- Create: `internal/ai/embedding/probe_test.go`

**Step 1: Test — probe rejects bad endpoints, accepts good ones.**

```go
func TestProbe_PassesWhenBothModalitiesReturnExpectedDim(t *testing.T) {
	srv := /* httptest.Server replying with 768-dim vectors for both image+text */
	require.NoError(t, embedding.Probe(ctx, embedding.Config{Endpoint: srv.URL, Model: "m", Dimension: 768, Timeout: 5*time.Second}))
}

func TestProbe_FailsOnDimensionMismatch(t *testing.T) {
	srv := /* server returning 256-dim vectors */
	err := embedding.Probe(ctx, embedding.Config{Endpoint: srv.URL, Model: "m", Dimension: 768, Timeout: 5*time.Second})
	require.Error(t, err)
	require.Contains(t, err.Error(), "dimension")
}

func TestProbe_FailsOnImageRejected(t *testing.T) {
	srv := /* server that 400s on data URL inputs but accepts text */
	err := embedding.Probe(ctx, embedding.Config{Endpoint: srv.URL, Model: "m", Dimension: 768, Timeout: 5*time.Second})
	require.Error(t, err)
	require.Contains(t, err.Error(), "image")
}
```

**Step 2: Implement.**

```go
// internal/ai/embedding/probe.go
package embedding

// Probe sends one image data URL and one short text string to the
// configured embeddings endpoint and asserts that both responses come
// back with a vector at cfg.Dimension. Alignment between the two
// modalities (whether the vectors live in a meaningfully shared
// latent space) is a model contract documented by the operator's
// chosen model card; Probe cannot verify alignment.
func Probe(ctx context.Context, cfg Config) error {
	c := NewClient(cfg)
	tinyJPEG := embeddedTinyJPEG // 1x1 white pixel; ~125 bytes
	imgs, err := c.EmbedImages(ctx, [][]byte{tinyJPEG})
	if err != nil { return fmt.Errorf("embed probe (image): %w", err) }
	if len(imgs) != 1 || len(imgs[0]) != cfg.Dimension {
		return fmt.Errorf("embed probe (image): expected 1 vector of dimension %d, got %d vectors of dim %d",
			cfg.Dimension, len(imgs), dimOf(imgs))
	}
	texts, err := c.EmbedTexts(ctx, []string{"a small dog on a beach"})
	if err != nil { return fmt.Errorf("embed probe (text): %w", err) }
	if len(texts) != 1 || len(texts[0]) != cfg.Dimension {
		return fmt.Errorf("embed probe (text): expected 1 vector of dimension %d", cfg.Dimension)
	}
	return nil
}
```

`embeddedTinyJPEG` is a 1x1 white pixel JPEG hardcoded as a `[]byte` constant (~80–125 bytes after `base64.StdEncoding`). Generate once with `convert -size 1x1 xc:white tiny.jpg` and paste the raw bytes as a Go byte slice literal.

**Step 3: Wire the probe into the server bootstrap.**

In `internal/cli/server.go`, before starting the AI workers, call:

```go
if cfg.AI.Embed.Enabled {
	if err := embedding.Probe(ctx, embedding.Config{
		Endpoint: cfg.AI.Embed.Endpoint, APIKey: cfg.AI.Embed.APIKey(),
		Model: cfg.AI.Embed.Model, Dimension: cfg.AI.Embed.Dimension,
		Timeout: cfg.AI.Embed.Timeout, MaxRetries: 0,
	}); err != nil {
		return fmt.Errorf("[ai.embed] probe failed: %w", err)
	}
}
```

The probe runs synchronously at boot. A failure aborts startup with a clear message — boot proceeds only when both modalities respond at the configured dimension.

**Step 4: Run.**

Run: `go test ./internal/ai/embedding/ -count=1`

Expected: PASS.

**Step 5: Commit.**

```bash
git add internal/ai/embedding/probe.go internal/ai/embedding/probe_test.go internal/cli/server.go
git commit -m "feat(ai/embedding): boot-time modality and dimension probe"
```

---

> **Cleanup checkpoint 2.** Run `/roborev-fix` on the current branch. Address any findings before starting Task E1. Commit any cleanup as its own commit(s); do not amend.

---

## Section E — Generations and mapping repositories

### Task E1 — `embedding_generations` repo (find/create/list/promote/retire)

**Files:**
- Create: `internal/ai/embedding/generations.go`
- Create: `internal/ai/embedding/generations_test.go`

**Step 1: Tests.**

```go
// internal/ai/embedding/generations_test.go
func TestGenerations_FindOrCreateBuilding_CreatesOnce(t *testing.T) {
	d := testutil.OpenTestDB(t)
	g := embedding.NewGenerations(d.WriteDB(), d.ReadDB())
	fp := ai.Fingerprint{ModelID: "siglip2", PromptVersion: "", InputProfile: "jpeg-384-q85-metadata-stripped-embed-v1"}

	row1, err := g.FindOrCreateBuilding(ctx, fp, 768)
	require.NoError(t, err)
	require.Equal(t, "building", row1.State)
	require.Equal(t, fmt.Sprintf("media_embeddings_g%d", row1.ID), row1.VecTableName)

	row2, err := g.FindOrCreateBuilding(ctx, fp, 768)
	require.NoError(t, err)
	require.Equal(t, row1.ID, row2.ID, "second call must return the same row")
}

func TestGenerations_FindActive_ReturnsNoneInitially(t *testing.T) {
	d := testutil.OpenTestDB(t)
	g := embedding.NewGenerations(d.WriteDB(), d.ReadDB())
	got, err := g.FindActive(ctx)
	require.NoError(t, err)
	require.Nil(t, got, "no active generation initially")
}

func TestGenerations_PromoteRetiresPriorActive(t *testing.T) {
	d := testutil.OpenTestDB(t)
	g := embedding.NewGenerations(d.WriteDB(), d.ReadDB())

	a, err := g.FindOrCreateBuilding(ctx, ai.Fingerprint{ModelID: "v1", InputProfile: "p1"}, 768)
	require.NoError(t, err)
	require.NoError(t, g.Promote(ctx, a.ID))

	b, err := g.FindOrCreateBuilding(ctx, ai.Fingerprint{ModelID: "v2", InputProfile: "p2"}, 768)
	require.NoError(t, err)
	require.NoError(t, g.Promote(ctx, b.ID))

	active, err := g.FindActive(ctx)
	require.NoError(t, err)
	require.Equal(t, b.ID, active.ID)

	// a must now be retired.
	rows, err := g.List(ctx, "retired")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, a.ID, rows[0].ID)
}
```

**Step 2: Implement.**

```go
// internal/ai/embedding/generations.go
package embedding

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/wesm/fotobank/internal/ai"
)

type Row struct {
	ID             int64
	Fingerprint    string
	FingerprintHash string
	ModelID        string
	InputProfile   string
	VecTableName   string
	Dimension      int
	State          string // "building" | "active" | "retired"
	EmbeddedCount  int
	ThresholdPct   int
	CreatedAt      time.Time
	ActivatedAt    *time.Time
	RetiredAt      *time.Time
}

type Generations struct {
	rw, ro *sql.DB
}

func NewGenerations(rw, ro *sql.DB) *Generations { return &Generations{rw: rw, ro: ro} }

// FindOrCreateBuilding returns the building generation matching fp.
// If none exists, inserts one and creates the per-generation vec0
// virtual table. Idempotent under the embedding_generations_one_building
// partial unique index — concurrent callers race on INSERT, the loser
// reads the winner's row back.
func (g *Generations) FindOrCreateBuilding(ctx context.Context, fp ai.Fingerprint, dim int) (Row, error) {
	hash := fingerprintHash(fp)

	// Fast path: an existing building/active row for this fingerprint.
	if row, err := g.findByHash(ctx, hash); err == nil && row.ID != 0 {
		return row, nil
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Row{}, err
	}

	tx, err := g.rw.BeginTx(ctx, nil)
	if err != nil { return Row{}, fmt.Errorf("begin: %w", err) }
	defer func() { _ = tx.Rollback() }()

	// Re-check inside the tx.
	row, err := findByHashTx(ctx, tx, hash)
	if err == nil && row.ID != 0 { return row, tx.Commit() }
	if err != nil && !errors.Is(err, sql.ErrNoRows) { return Row{}, err }

	// Insert. The vec_table_name is derived from the new id, so we
	// allocate the row first and patch the name in a second statement.
	res, err := tx.ExecContext(ctx,
		`INSERT INTO embedding_generations
		 (fingerprint, fingerprint_hash, model_id, input_profile, vec_table_name,
		  dimension, state, created_at)
		 VALUES (?, ?, ?, ?, '', ?, 'building', ?)`,
		fp.String(), hash, fp.ModelID, fp.InputProfile, dim, time.Now().UTC(),
	)
	if err != nil { return Row{}, fmt.Errorf("insert generation: %w", err) }
	id, _ := res.LastInsertId()
	tableName := "media_embeddings_g" + strconv.FormatInt(id, 10)
	if _, err := tx.ExecContext(ctx, `UPDATE embedding_generations SET vec_table_name=? WHERE id=?`, tableName, id); err != nil {
		return Row{}, fmt.Errorf("set vec_table_name: %w", err)
	}

	// Create the vec0 virtual table.
	createSQL := fmt.Sprintf(
		`CREATE VIRTUAL TABLE %s USING vec0(vec_id INTEGER PRIMARY KEY, embedding FLOAT[%d])`,
		tableName, dim,
	)
	if _, err := tx.ExecContext(ctx, createSQL); err != nil {
		return Row{}, fmt.Errorf("create vec table %s: %w", tableName, err)
	}

	if err := tx.Commit(); err != nil { return Row{}, fmt.Errorf("commit: %w", err) }
	return findByHash(ctx, g.ro, hash)
}

// Promote retires any current active generation and promotes id to active.
func (g *Generations) Promote(ctx context.Context, id int64) error {
	tx, err := g.rw.BeginTx(ctx, nil)
	if err != nil { return err }
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx,
		`UPDATE embedding_generations SET state='retired', retired_at=? WHERE state='active'`, now); err != nil {
		return fmt.Errorf("retire prior active: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE embedding_generations SET state='active', activated_at=? WHERE id=?`, now, id); err != nil {
		return fmt.Errorf("promote: %w", err)
	}
	return tx.Commit()
}

// Retire transitions id to retired. The vec table stays in place; the
// compactor drops it after retain_retired_days.
func (g *Generations) Retire(ctx context.Context, id int64) error { /* … */ }

// FindActive / FindBuildingByFingerprint / List / SetEmbeddedCount / IncEmbeddedCount …
```

`fingerprintHash` is `hex.EncodeToString(sha256.Sum256([]byte(fp.String())))`.

**Step 3: Run.**

Run: `go test ./internal/ai/embedding/ -count=1 -run "TestGenerations_"`

Expected: PASS.

**Step 4: Commit.**

```bash
git add internal/ai/embedding/generations.go internal/ai/embedding/generations_test.go
git commit -m "feat(ai/embedding): generations registry"
```

---

### Task E2 — `media_embedding_ids` mapping repo + delete-then-insert helpers

**Files:**
- Create: `internal/ai/embedding/mapping.go`
- Create: `internal/ai/embedding/mapping_test.go`

**Step 1: Tests.**

```go
func TestMapping_WriteVectorIsNetNew(t *testing.T) {
	d := testutil.OpenTestDB(t)
	owner := seedOwner(t, d)
	mid := seedMedia(t, d, owner)
	g := mustCreateBuildingGen(t, d, 768)

	m := embedding.NewMapping(d.WriteDB())
	delta, err := m.WriteVector(ctx, g, mid, mockVec(768))
	require.NoError(t, err)
	require.Equal(t, 1, delta, "first write is net-new")

	// Replacement: zero-delta.
	delta2, err := m.WriteVector(ctx, g, mid, mockVec(768))
	require.NoError(t, err)
	require.Equal(t, 0, delta2)
}

func TestMapping_DeleteForGenerationMedia(t *testing.T) {
	// after WriteVector, calling DeleteForGenerationMedia removes both
	// the mapping row and the vec0 row, returns delta=-1.
}
```

**Step 2: Implement.**

```go
// internal/ai/embedding/mapping.go
package embedding

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

type Mapping struct { rw *sql.DB }
func NewMapping(rw *sql.DB) *Mapping { return &Mapping{rw: rw} }

// WriteVector writes vec for (gen, mediaID) using the §5.4 delete-then-
// insert pattern. Returns +1 on net-new, 0 on replacement. The caller
// is responsible for incrementing embedded_count by the returned delta
// inside the same higher-level transaction (the worker batches mapping
// writes + ai_jobs status updates into one tx).
//
// Must be called inside a write transaction held by the caller. The
// helper does NOT begin its own transaction — under the per-batch
// worker write the mapping writes share a tx with the ai_jobs status
// updates. Pass tx via the *sql.Tx variant below; the *sql.DB variant
// here is for tests that don't span multiple writes.
func (m *Mapping) WriteVector(ctx context.Context, gen Row, mediaID string, vec []float32) (int, error) {
	tx, err := m.rw.BeginTx(ctx, nil)
	if err != nil { return 0, err }
	defer func() { _ = tx.Rollback() }()
	delta, err := WriteVectorTx(ctx, tx, gen, mediaID, vec)
	if err != nil { return 0, err }
	if err := tx.Commit(); err != nil { return 0, fmt.Errorf("commit: %w", err) }
	return delta, nil
}

// WriteVectorTx is the transaction-bound variant.
func WriteVectorTx(ctx context.Context, tx *sql.Tx, gen Row, mediaID string, vec []float32) (int, error) {
	// Step 1: take + drop any prior mapping; capture vec_id if present.
	var priorVecID sql.NullInt64
	row := tx.QueryRowContext(ctx,
		`DELETE FROM media_embedding_ids WHERE generation_id=? AND media_id=? RETURNING vec_id`,
		gen.ID, mediaID,
	)
	switch err := row.Scan(&priorVecID); {
	case errors.Is(err, sql.ErrNoRows):
		priorVecID = sql.NullInt64{}
	case err != nil:
		return 0, fmt.Errorf("drop prior mapping: %w", err)
	}

	// Step 2: drop the prior vec0 row if there was one.
	if priorVecID.Valid {
		if _, err := tx.ExecContext(ctx,
			fmt.Sprintf(`DELETE FROM %s WHERE vec_id = ?`, gen.VecTableName),
			priorVecID.Int64,
		); err != nil {
			return 0, fmt.Errorf("drop prior vec row: %w", err)
		}
	}

	// Step 3: allocate the new vec_id.
	var nextVecID int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(vec_id), 0) + 1 FROM media_embedding_ids WHERE generation_id=?`,
		gen.ID,
	).Scan(&nextVecID); err != nil {
		return 0, fmt.Errorf("allocate vec_id: %w", err)
	}

	// Step 4: insert mapping + vec row.
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO media_embedding_ids (generation_id, media_id, vec_id) VALUES (?, ?, ?)`,
		gen.ID, mediaID, nextVecID,
	); err != nil { return 0, fmt.Errorf("insert mapping: %w", err) }

	if _, err := tx.ExecContext(ctx,
		fmt.Sprintf(`INSERT INTO %s (vec_id, embedding) VALUES (?, vec_f32(?))`, gen.VecTableName),
		nextVecID, vecToBlob(vec),
	); err != nil { return 0, fmt.Errorf("insert vec row: %w", err) }

	if priorVecID.Valid { return 0, nil }
	return 1, nil
}

// DeleteForGenerationMedia removes the mapping + vec row for (gen, mediaID).
// Returns -1 if a row was deleted, 0 otherwise (idempotent no-op).
func DeleteForGenerationMediaTx(ctx context.Context, tx *sql.Tx, gen Row, mediaID string) (int, error) { /* … */ }
```

`vecToBlob` packs `[]float32` little-endian into a `[]byte` for `vec_f32(?)`. Mirror msgvault's `internal/vector/sqlitevec` packing.

**Step 3: Run.**

Run: `go test ./internal/ai/embedding/ -count=1 -run "TestMapping_"`

Expected: PASS.

**Step 4: Commit.**

```bash
git add internal/ai/embedding/mapping.go internal/ai/embedding/mapping_test.go
git commit -m "feat(ai/embedding): media_embedding_ids delete-then-insert"
```

---

## Section F — Embedding worker

### Task F1 — Worker core (claim → resolve → batch encode → call → write)

**Files:**
- Create: `internal/ai/embedding/worker.go`
- Create: `internal/ai/embedding/worker_test.go`

**Step 1: Tests — happy path one batch end-to-end.**

```go
// internal/ai/embedding/worker_test.go
func TestWorker_ProcessesBatchEndToEnd(t *testing.T) {
	d := testutil.OpenTestDB(t)
	owner := seedOwner(t, d)
	mids := []string{seedMedia(t, d, owner), seedMedia(t, d, owner), seedMedia(t, d, owner)}
	for _, m := range mids { mustWriteThumbReady(t, d, m) }

	mockClient := &fakeEmbedClient{vectors: dim768N(3)}
	q := jobs.NewQueue(d.WriteDB(), d.ReadDB())
	gens := embedding.NewGenerations(d.WriteDB(), d.ReadDB())
	mapping := embedding.NewMapping(d.WriteDB())
	resolver := imginput.NewResolver(d.ReadDB(), fakeStore{ready: mockJPEG()})
	w := embedding.NewWorker(embedding.WorkerDeps{
		Q: q, Gens: gens, Mapping: mapping, Client: mockClient,
		Resolver: resolver, Cfg: ai.EmbedConfig{
			Model: "siglip2", InputProfile: "jpeg-384-q85-metadata-stripped-embed-v1",
			Dimension: 768, InputEdge: 384, BatchSize: 32, MaxRetries: 1,
		},
	})

	fp := ai.Fingerprint{ModelID: "siglip2", InputProfile: "jpeg-384-q85-metadata-stripped-embed-v1"}
	for _, m := range mids { require.NoError(t, q.Enqueue(ctx, m, ai.TaskEmbed, fp)) }

	require.NoError(t, w.RunOnce(ctx))

	// All three media have a mapping under the building generation.
	for _, m := range mids { require.True(t, mappingExists(t, d, m)) }
	// embedded_count == 3 on the building gen.
	row, _ := gens.FindBuildingByFingerprint(ctx, fp)
	require.Equal(t, 3, row.EmbeddedCount)
}

// Replacement (re-embed an already-mapped media in the same gen) is
// zero-delta.
func TestWorker_ReplacementIsZeroDelta(t *testing.T) { /* … */ }

// thumb_status='no_preview' results in ai_skipped + done job.
func TestWorker_NoPreviewSkips(t *testing.T) { /* … */ }

// thumb_status='pending' marks job blocked.
func TestWorker_PendingThumbBlocks(t *testing.T) { /* … */ }

// Partial failure (some response indices missing): re-runs failed
// indices as singles.
func TestWorker_PartialFailureRerunsSingles(t *testing.T) { /* … */ }
```

**Step 2: Implement.**

```go
// internal/ai/embedding/worker.go
package embedding

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/wesm/fotobank/internal/ai"
	"github.com/wesm/fotobank/internal/ai/imginput"
	"github.com/wesm/fotobank/internal/ai/imginput/encode"
	"github.com/wesm/fotobank/internal/ai/jobs"
)

type WorkerDeps struct {
	Q        *jobs.Queue
	Gens     *Generations
	Mapping  *Mapping
	Client   ClientIface
	Resolver imginput.Resolver
	Cfg      ai.EmbedConfig
	Events   EventEmitter
}

type Worker struct{ d WorkerDeps }

func NewWorker(d WorkerDeps) *Worker { return &Worker{d: d} }

// RunOnce performs one claim/process/finalize cycle. Returns when no
// more pending embed jobs are claimable.
func (w *Worker) RunOnce(ctx context.Context) error {
	for {
		batch, err := w.d.Q.ClaimBatch(ctx, ai.TaskEmbed, w.d.Cfg.BatchSize)
		if err != nil { return fmt.Errorf("claim batch: %w", err) }
		if len(batch) == 0 { return nil }
		if err := w.process(ctx, batch); err != nil { return err }
	}
}

func (w *Worker) process(ctx context.Context, batch []jobs.Claim) error {
	// 1) Resolve preview JPEGs in parallel; classify thumb status.
	type prepared struct {
		claim jobs.Claim
		jpeg  []byte
		status string
		err   error
	}
	out := make([]prepared, len(batch))
	var wg sync.WaitGroup
	for i, c := range batch {
		i, c := i, c
		wg.Add(1)
		go func() {
			defer wg.Done()
			jpg, status, err := w.d.Resolver.ResolvePreviewJPEG(ctx, c.MediaID)
			out[i] = prepared{claim: c, jpeg: jpg, status: status, err: err}
		}()
	}
	wg.Wait()

	// 2) Skip / block / error per the §6.4 step-2 table.
	type encoded struct {
		idx   int
		claim jobs.Claim
		body  []byte
	}
	var ready []encoded
	for i, p := range out {
		switch {
		case p.err != nil:
			if err := w.d.Q.MarkFailed(ctx, p.claim.JobID, ai.ErrKindMissingAIInput, p.err.Error()); err != nil { return err }
		case p.status == "no_preview":
			if err := w.d.Skipped.Record(ctx, p.claim.MediaID, ai.TaskEmbed, "no_preview"); err != nil { return err }
			if err := w.d.Q.MarkDone(ctx, p.claim.JobID); err != nil { return err }
		case p.status == "pending" || p.status == "working" || p.status == "failed":
			if err := w.d.Q.MarkBlocked(ctx, p.claim.JobID, ai.ErrKindThumbBlocked, "thumb_blocked"); err != nil { return err }
		case p.status == "ready":
			body, err := encode.EncodeEmbed(p.jpeg, w.d.Cfg.InputEdge)
			if err != nil {
				if err := w.d.Q.MarkFailed(ctx, p.claim.JobID, ai.ErrKindMalformed, err.Error()); err != nil { return err }
				continue
			}
			ready = append(ready, encoded{idx: i, claim: p.claim, body: body})
		}
	}
	if len(ready) == 0 { return nil }

	// 3) Resolve target generation by fingerprint.
	fp := w.fingerprintFor(ready[0].claim) // all claims in a batch share fp; defensive
	gen, err := w.d.Gens.FindOrCreateBuilding(ctx, fp, w.d.Cfg.Dimension)
	if err != nil { return fmt.Errorf("resolve generation: %w", err) }

	// 4) Issue one batched /v1/embeddings call.
	jpegs := make([][]byte, len(ready))
	for i, e := range ready { jpegs[i] = e.body }
	vectors, err := w.d.Client.EmbedImages(ctx, jpegs)
	if err != nil {
		// Full-batch failure: mark every job in the batch failed.
		kind := classifyEmbedErr(err)
		for _, e := range ready {
			_ = w.d.Q.MarkFailed(ctx, e.claim.JobID, kind, err.Error())
		}
		return nil
	}

	// 5) On partial failure (vectors len != len(ready)): re-run failed
	//    indices as singles to attribute failures to specific media.
	if len(vectors) != len(ready) { return w.processSingles(ctx, gen, ready) }

	// 6) Write all mappings + ai_jobs status updates in one tx.
	totalDelta := 0
	tx, err := w.d.RWTxBegin(ctx)
	if err != nil { return err }
	defer func() { _ = tx.Rollback() }()
	for i, e := range ready {
		delta, err := WriteVectorTx(ctx, tx, gen, e.claim.MediaID, vectors[i])
		if err != nil { return fmt.Errorf("write vec for %s: %w", e.claim.MediaID, err) }
		totalDelta += delta
		if err := w.markDoneTx(ctx, tx, e.claim.JobID); err != nil { return err }
	}
	if totalDelta > 0 {
		if _, err := tx.ExecContext(ctx,
			`UPDATE embedding_generations SET embedded_count = embedded_count + ? WHERE id = ?`,
			totalDelta, gen.ID,
		); err != nil { return fmt.Errorf("inc embedded_count: %w", err) }
	}
	if err := tx.Commit(); err != nil { return fmt.Errorf("commit: %w", err) }

	// 7) Emit per-job completion events.
	for _, e := range ready {
		w.d.Events.EmitAIEmbedCompleted(e.claim.MediaID, fp.String())
	}
	return nil
}
```

(Helper signatures — `processSingles`, `fingerprintFor`, `classifyEmbedErr`, `markDoneTx`, `RWTxBegin`, `EventEmitter` — are concrete utilities the executor implements. The shape mirrors the existing `internal/ai/worker/worker.go`.)

**Step 3: Run.**

Run: `go test ./internal/ai/embedding/ -count=1 -run "TestWorker_"`

Expected: PASS.

**Step 4: Commit.**

```bash
git add internal/ai/embedding/worker.go internal/ai/embedding/worker_test.go
git commit -m "feat(ai/embedding): batched embedding worker"
```

---

### Task F2 — Generation targeting under concurrent worker contention

**Files:**
- Modify: `internal/ai/embedding/generations.go` (only if E1 didn't already cover the contention test)
- Modify: `internal/ai/embedding/generations_test.go` (append)

**Step 1: Test.**

```go
// Concurrent FindOrCreateBuilding for the same fingerprint must
// produce one generation row, with both callers returning the same id.
func TestGenerations_FindOrCreateBuilding_ConcurrentSafety(t *testing.T) {
	d := testutil.OpenTestDB(t)
	g := embedding.NewGenerations(d.WriteDB(), d.ReadDB())
	fp := ai.Fingerprint{ModelID: "siglip2", InputProfile: "p"}

	const N = 8
	ids := make([]int64, N)
	errs := make([]error, N)
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		i := i
		go func() {
			defer wg.Done()
			row, err := g.FindOrCreateBuilding(ctx, fp, 768)
			ids[i], errs[i] = row.ID, err
		}()
	}
	wg.Wait()
	for _, e := range errs { require.NoError(t, e) }
	for i := 1; i < N; i++ { require.Equal(t, ids[0], ids[i]) }
}
```

**Step 2: Implement / verify.**

The implementation in E1 already takes a transaction and re-checks the row inside it. Run the test — if it fails, the re-check is missing. Likely the issue is a race between `findByHash` (read pool) and the insert (write pool). The fix is to do **all** of the work — including the find — inside the write tx. Adjust if needed.

**Step 3: Run.**

Run: `go test ./internal/ai/embedding/ -count=1 -run "TestGenerations_FindOrCreateBuilding_ConcurrentSafety"`

Expected: PASS.

**Step 4: Commit.**

```bash
git add internal/ai/embedding/
git commit -m "test(ai/embedding): concurrent FindOrCreateBuilding"
```

---

### Task F3 — Embed worker `Run` loop + housekeeping promotion of blocked rows

**Files:**
- Modify: `internal/ai/embedding/worker.go` — add `Run(ctx) error` long-running loop with cancellation
- Modify: `internal/ai/embedding/worker_test.go` — table test for the loop's tick-on-empty-queue + cancel
- Modify: `internal/ai/jobs/queue.go` — extend `PromoteThumbReadyBlocked` (existing helper) or its caller to iterate `embed` alongside `tag` and `caption`

**Step 1: Test — `Run` returns on ctx cancel and processes a queued job before exit.**

```go
func TestWorker_RunDrainsQueueAndExitsOnCancel(t *testing.T) {
	d := testutil.OpenTestDB(t)
	w, q, _ := /* setup */
	mid := /* seed media + thumb-ready */
	require.NoError(t, q.Enqueue(ctx, mid, ai.TaskEmbed, fp))

	ctx2, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx2) }()
	require.Eventually(t, func() bool { return mappingExists(t, d, mid) }, 5*time.Second, 25*time.Millisecond)
	cancel()
	require.NoError(t, <-done)
}
```

**Step 2: Implement.**

```go
func (w *Worker) Run(ctx context.Context) error {
	t := time.NewTicker(w.d.Cfg.IdlePoll)
	defer t.Stop()
	for {
		if err := w.RunOnce(ctx); err != nil {
			if errors.Is(err, context.Canceled) { return nil }
			return fmt.Errorf("RunOnce: %w", err)
		}
		select {
		case <-ctx.Done(): return nil
		case <-t.C:
		}
	}
}
```

**Step 3: Extend the housekeeping promotion call site.**

Find `PromoteThumbReadyBlocked` (in `internal/ai/jobs/queue.go` or its caller). The existing call is per-task. Locate its caller (likely in `internal/cli/server.go`'s housekeeping tick) and extend the iteration:

```go
for _, task := range []ai.Task{ai.TaskTag, ai.TaskCaption, ai.TaskEmbed} {
	if _, err := q.PromoteThumbReadyBlocked(ctx, task); err != nil {
		log.Warn(ctx, "promote thumb-ready blocked", "task", task, "err", err)
	}
}
```

**Step 4: Run.**

Run: `go test ./internal/ai/embedding/ ./internal/ai/jobs/ -count=1`

Expected: PASS.

**Step 5: Commit.**

```bash
git add internal/ai/embedding/worker.go internal/ai/embedding/worker_test.go internal/ai/jobs/queue.go internal/cli/server.go
git commit -m "feat(ai/embedding): Run loop and housekeeping promotion"
```

---

> **Cleanup checkpoint 3.** Run `/roborev-fix` on the current branch. Address any findings before starting Task G1. Commit any cleanup as its own commit(s); do not amend.

---

## Section G — Embed gap scanner

### Task G1 — Embed gap-fill predicate (separate from tag/caption)

**Files:**
- Modify: `internal/ai/gapscanner/scanner.go` — add `ScanEmbed` method using the §6.6 predicate
- Modify: `internal/ai/gapscanner/scanner_test.go` — coverage for the cases in §6.6

**Step 1: Tests covering the §6.6 predicate.**

```go
// internal/ai/gapscanner/scanner_test.go (append)
func TestScanEmbed_EnqueuesNewMedia(t *testing.T) {
	d := testutil.OpenTestDB(t)
	owner := seedOwner(t, d)
	mid := seedMedia(t, d, owner)
	mustWriteThumbReady(t, d, mid)

	g := mustCreateBuildingGen(t, d, 768)
	s := /* construct scanner with embed deps */

	n, err := s.ScanEmbed(ctx, gapscanner.EmbedScanRequest{
		Owner: owner, Generation: g, Fingerprint: fpFromGen(g),
		Limit: 100,
	})
	require.NoError(t, err)
	require.Equal(t, 1, n)
}

func TestScanEmbed_SkipsMediaWithExistingMappingInGen(t *testing.T) { /* … */ }
func TestScanEmbed_SkipsMediaWithSkippedRowForEmbed(t *testing.T) { /* … */ }
func TestScanEmbed_SkipsMediaPastFailureBudget(t *testing.T) { /* … */ }
func TestScanEmbed_SkipsMediaWithInflightJob(t *testing.T) { /* … */ }
func TestScanEmbed_HiddenMediaExcludedWithoutAck(t *testing.T) { /* … */ }
func TestScanEmbed_HiddenMediaIncludedWithAck(t *testing.T) { /* … */ }
```

**Step 2: Implement `ScanEmbed`.**

```go
// internal/ai/gapscanner/scanner.go (append)

type EmbedScanRequest struct {
	Owner             owners.Principal
	Generation        embedding.Row
	Fingerprint       ai.Fingerprint
	AckAllowsHidden   bool
	RetryBudget       int      // matches existing tag/caption retry budget
	Limit             int      // 0 = unbounded
	MediaIDs          []string // 0 = all visible eligible
}

// ScanEmbed walks media for the request's generation and enqueues
// embed jobs against the §6.6 predicate. Returns enqueued count.
func (s *Scanner) ScanEmbed(ctx context.Context, req EmbedScanRequest) (int, error) {
	const q = `
SELECT m.id FROM media m
 WHERE m.owner_hub = ? AND m.owner_user_id = ?
   AND m.thumb_status = 'ready'
   AND (m.hidden_at IS NULL OR ?)
   AND NOT EXISTS (
     SELECT 1 FROM media_embedding_ids x
      WHERE x.generation_id = ? AND x.media_id = m.id
   )
   AND NOT EXISTS (
     SELECT 1 FROM ai_skipped sk
      WHERE sk.media_id = m.id AND sk.task = 'embed'
   )
   AND NOT EXISTS (
     SELECT 1 FROM ai_failures f
      WHERE f.media_id = m.id AND f.task = 'embed'
        AND f.model_id = ? AND f.prompt_version = '' AND f.input_profile = ?
        AND f.attempt_count >= ?
   )
   AND NOT EXISTS (
     SELECT 1 FROM ai_jobs j
      WHERE j.media_id = m.id AND j.task = 'embed'
        AND j.status IN ('pending','working','blocked')
   )
 ORDER BY m.id
 LIMIT ?`

	args := []any{
		req.Owner.Hub, req.Owner.UserID,
		req.AckAllowsHidden,
		req.Generation.ID,
		req.Fingerprint.ModelID, req.Fingerprint.InputProfile,
		req.RetryBudget,
		limitOrUnbounded(req.Limit),
	}
	rows, err := s.ro.QueryContext(ctx, q, args...)
	if err != nil { return 0, fmt.Errorf("scan embed: %w", err) }
	defer rows.Close()

	var mids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil { return 0, err }
		mids = append(mids, id)
	}
	if err := rows.Err(); err != nil { return 0, err }

	enqueued := 0
	for _, m := range mids {
		if err := s.q.Enqueue(ctx, m, ai.TaskEmbed, req.Fingerprint); err != nil {
			return enqueued, fmt.Errorf("enqueue %s: %w", m, err)
		}
		enqueued++
	}
	return enqueued, nil
}
```

`limitOrUnbounded(0)` returns a large int (e.g. `1<<31 - 1`); non-zero limit returns the limit unchanged.

**Step 3: Run.**

Run: `go test ./internal/ai/gapscanner/ -count=1 -run "TestScanEmbed_"`

Expected: PASS.

**Step 4: Commit.**

```bash
git add internal/ai/gapscanner/
git commit -m "feat(ai/gapscanner): embed gap-fill predicate"
```

---

## Section H — Activation coordinator

### Task H1 — Activator tick (eligible/embedded counts + threshold check)

**Files:**
- Create: `internal/ai/embedding/activator.go`
- Create: `internal/ai/embedding/activator_test.go`

**Step 1: Test — activator no-ops when threshold not yet met; promotes when it is.**

```go
func TestActivator_PromotesWhenThresholdMet(t *testing.T) {
	d := testutil.OpenTestDB(t)
	owner := seedOwner(t, d)
	for i := 0; i < 20; i++ {
		m := seedMedia(t, d, owner)
		mustWriteThumbReady(t, d, m)
	}
	g := mustCreateBuildingGen(t, d, 768)
	// Seed mappings for 19 of 20 media (95%).
	mustWriteMappings(t, d, g, ownerVisibleMids(t, d, owner)[:19])

	act := embedding.NewActivator(/* deps */)
	require.NoError(t, act.Tick(ctx))

	// Generation is now active.
	gens := embedding.NewGenerations(d.WriteDB(), d.ReadDB())
	a, _ := gens.FindActive(ctx)
	require.NotNil(t, a)
	require.Equal(t, g.ID, a.ID)
}

func TestActivator_NoOpsBelowThreshold(t *testing.T) { /* 18 of 20 → 90% < 95% */ }
func TestActivator_PausedOnAckRequired(t *testing.T) {
	// principal in 'acknowledgement_required' state — Tick must not promote
	// even at 100% embedded.
}
func TestActivator_AssertiveCount_IgnoresOrphanedMappings(t *testing.T) {
	// Insert a mapping whose media has been DELETEd; the assertive
	// embedded-count must exclude it. Confirms the activator does not
	// trust the cached embedded_count.
}
```

**Step 2: Implement.**

```go
// internal/ai/embedding/activator.go
package embedding

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/wesm/fotobank/internal/ai/ack"
	"github.com/wesm/fotobank/internal/owners"
)

type Activator struct {
	rw, ro       *sql.DB
	gens         *Generations
	ack          *ack.Repo
	cfg          ActivatorCfg
	events       EventEmitter
}

type ActivatorCfg struct {
	Principal       owners.Principal
	ThresholdPct    int
	Tick            time.Duration
}

// Tick is invoked periodically (default 30s). One pass:
//   1. If the ack gate requires acknowledgement, no-op (paused).
//   2. Find building generation; if none, no-op.
//   3. Compute eligible_count (visible-media count under hidden predicate).
//   4. Compute embedded_count (assertive COUNT(*) joining mapping back to eligible media).
//   5. If embedded/eligible >= threshold_pct/100, promote in one tx.
func (a *Activator) Tick(ctx context.Context) error {
	required, err := a.ack.IsRequired(ctx, a.cfg.Principal)
	if err != nil { return fmt.Errorf("ack: %w", err) }
	if required { return nil } // paused

	building, err := a.gens.FindBuilding(ctx)
	if err != nil { return fmt.Errorf("find building: %w", err) }
	if building == nil { return nil }

	const eligibleSQL = `
SELECT COUNT(*) FROM media m
 WHERE m.owner_hub = ? AND m.owner_user_id = ?
   AND m.thumb_status = 'ready'
   AND (m.hidden_at IS NULL OR ?)
   AND NOT EXISTS (
     SELECT 1 FROM ai_skipped sk WHERE sk.media_id = m.id AND sk.task = 'embed'
   )`

	const embeddedSQL = `
SELECT COUNT(*) FROM media_embedding_ids x
 JOIN media m ON m.id = x.media_id
 WHERE x.generation_id = ?
   AND m.owner_hub = ? AND m.owner_user_id = ?
   AND m.thumb_status = 'ready'
   AND (m.hidden_at IS NULL OR ?)
   AND NOT EXISTS (
     SELECT 1 FROM ai_skipped sk WHERE sk.media_id = m.id AND sk.task = 'embed'
   )`

	ackAllowsHidden := false // single-principal v1; ack-allows-hidden derives from the principal's ack state

	var eligible, embedded int
	if err := a.ro.QueryRowContext(ctx, eligibleSQL,
		a.cfg.Principal.Hub, a.cfg.Principal.UserID, ackAllowsHidden,
	).Scan(&eligible); err != nil { return fmt.Errorf("eligible: %w", err) }

	if err := a.ro.QueryRowContext(ctx, embeddedSQL,
		building.ID, a.cfg.Principal.Hub, a.cfg.Principal.UserID, ackAllowsHidden,
	).Scan(&embedded); err != nil { return fmt.Errorf("embedded: %w", err) }

	if eligible == 0 { return nil }
	pct := embedded * 100 / eligible
	if pct < a.cfg.ThresholdPct { return nil }

	if err := a.gens.Promote(ctx, building.ID); err != nil { return fmt.Errorf("promote: %w", err) }
	a.events.EmitAIEmbedGenerationActivated(building.ID, building.Fingerprint)
	return nil
}
```

**Step 3: Run.**

Run: `go test ./internal/ai/embedding/ -run "TestActivator_" -count=1`

Expected: PASS.

**Step 4: Commit.**

```bash
git add internal/ai/embedding/activator.go internal/ai/embedding/activator_test.go
git commit -m "feat(ai/embedding): activation coordinator"
```

---

### Task H2 — Activator `Run` loop and ack-paused health surface

**Files:**
- Modify: `internal/ai/embedding/activator.go` — add `Run(ctx)` long-running loop
- Modify: `internal/ai/embedding/activator_test.go` — coverage of the loop tick + cancel
- Modify: `internal/service/ai/health.go` — extend the existing aiHealthOutput payload's embed block with `paused_reason: "acknowledgement_required"` when the activator is paused

**Step 1: Test — `Run` ticks at the configured interval and returns on ctx cancel.**

```go
func TestActivator_RunTicksAndCancels(t *testing.T) {
	a := embedding.NewActivator(/* … */ embedding.ActivatorCfg{ Tick: 50*time.Millisecond, ThresholdPct: 95 })
	ctx2, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx2) }()
	time.Sleep(200 * time.Millisecond) // a few ticks
	cancel()
	require.NoError(t, <-done)
}
```

**Step 2: Implement.**

```go
func (a *Activator) Run(ctx context.Context) error {
	t := time.NewTicker(a.cfg.Tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done(): return nil
		case <-t.C:
			if err := a.Tick(ctx); err != nil { /* log; do not exit */ }
		}
	}
}
```

**Step 3: Surface the paused reason in the health payload.**

In `internal/service/ai/health.go`, the existing `aiHealthOutput` (or whatever struct backs `GET /api/v1/ai/health`) gains an `EmbedTaskPart` block plus an `EmbeddingGenerations` block. Initially populate the `EmbedTaskPart` with the same fields as `TagTaskPart` / `CaptionTaskPart` (queue depth, last-completed-at, last-failure summary). Add a `PausedReason string` field on the embed block; when the principal is in `acknowledgement_required`, set it to `"acknowledgement_required"`.

Add an `EmbeddingGenerations` summary block:

```go
type EmbeddingGenerationSummary struct {
	ID            int64     `json:"id"`
	Fingerprint   string    `json:"fingerprint"`
	State         string    `json:"state"` // building | active | retired
	EmbeddedCount int       `json:"embedded_count"`
	EligibleCount int       `json:"eligible_count"`
	CreatedAt     time.Time `json:"created_at"`
	ActivatedAt   *time.Time `json:"activated_at,omitempty"`
	RetiredAt     *time.Time `json:"retired_at,omitempty"`
}
```

Aggregator queries `embedding_generations` and computes `eligible_count` per building/active row using the activator's eligible SQL.

**Step 4: Run.**

Run: `go test ./internal/ai/embedding/ ./internal/service/ai/ ./internal/httpapi/ -count=1`

Expected: PASS.

**Step 5: Run `make api-generate`.**

Run: `make api-generate`

Expected: `openapi.json` updates with the new health payload fields; `frontend/src/lib/api/generated/schema.ts` updates accordingly.

**Step 6: Commit.**

```bash
git add internal/ai/embedding/ internal/service/ai/ internal/httpapi/ openapi.json frontend/src/lib/api/generated/schema.ts
git commit -m "feat(ai/embedding): activator Run loop and health-payload extension"
```

---

## Section I — Auto-enqueue triggers

### Task I1 — Importer enqueues embed alongside tag and caption

**Files:**
- Modify: `internal/ingest/...` (the importer's per-row finalize block that enqueues AI jobs today)
- Modify: `internal/ingest/..._test.go` — coverage that on import, an embed job lands in `ai_jobs`

**Step 1: Test.**

```go
func TestImporter_EnqueuesEmbedJobAlongsideTagCaption(t *testing.T) {
	d := testutil.OpenTestDB(t)
	imp := /* construct importer with a building generation present */

	mid := imp.ImportFile(ctx, "/path/to/photo.jpg")

	for _, task := range []string{"tag", "caption", "embed"} {
		var n int
		require.NoError(t, d.ReadDB().QueryRow(
			`SELECT COUNT(*) FROM ai_jobs WHERE media_id = ? AND task = ?`, mid, task,
		).Scan(&n))
		require.Equal(t, 1, n, "import must enqueue %s", task)
	}
}
```

**Step 2: Add the third enqueue.**

In the importer's finalize block (search for the existing `q.Enqueue(ctx, mediaID, ai.TaskTag, ...)` / `ai.TaskCaption`), add the embed enqueue:

```go
if cfg.AI.Embed.Enabled {
	if err := q.Enqueue(ctx, mediaID, ai.TaskEmbed, embedFingerprint(cfg.AI.Embed)); err != nil {
		return fmt.Errorf("enqueue embed: %w", err)
	}
}
```

`embedFingerprint(cfg)` is a helper:

```go
// internal/ai/embedding/fingerprint.go (or inline if a single call site)
func Fingerprint(cfg ai.EmbedConfig) ai.Fingerprint {
	return ai.Fingerprint{
		ModelID:       cfg.Model,
		PromptVersion: "", // embed has no prompt
		InputProfile:  fmt.Sprintf("jpeg-%d-q85-metadata-stripped-embed-v1", cfg.InputEdge),
	}
}
```

**Step 3: Run.**

Run: `go test ./internal/ingest/ -count=1 -run "TestImporter_EnqueuesEmbed"`

Expected: PASS.

**Step 4: Commit.**

```bash
git add internal/ingest/ internal/ai/embedding/
git commit -m "feat(ingest): auto-enqueue embed jobs on import"
```

---

### Task I2 — Thumb-regen invalidates embed mappings across non-retired generations

**Files:**
- Modify: `internal/thumb/worker.go` — on thumb-version bump, call a new helper `embedding.OnThumbRegen(ctx, tx, mediaID)` inside the same write transaction that bumps `thumb_version`
- Create: `internal/ai/embedding/on_thumb_regen.go`
- Create: `internal/ai/embedding/on_thumb_regen_test.go`

**Step 1: Test.**

```go
func TestOnThumbRegen_InvalidatesAcrossNonRetiredGens(t *testing.T) {
	d := testutil.OpenTestDB(t)
	owner := seedOwner(t, d)
	mid := seedMedia(t, d, owner)
	mustWriteThumbReady(t, d, mid)

	a := mustCreateActiveGen(t, d, 768) // build then promote
	b := mustCreateBuildingGen(t, d, 768)
	mustWriteMapping(t, d, a, mid, mockVec(768))
	mustWriteMapping(t, d, b, mid, mockVec(768))

	require.NoError(t, withTx(d, func(tx *sql.Tx) error {
		return embedding.OnThumbRegen(ctx, tx, mid)
	}))

	// Mappings gone for both gens.
	require.False(t, mappingExistsInGen(t, d, a, mid))
	require.False(t, mappingExistsInGen(t, d, b, mid))

	// embedded_count decremented on both gens.
	gens := embedding.NewGenerations(d.WriteDB(), d.ReadDB())
	rowA := must(gens.GetByID(ctx, a.ID))
	require.Equal(t, 0, rowA.EmbeddedCount)
	rowB := must(gens.GetByID(ctx, b.ID))
	require.Equal(t, 0, rowB.EmbeddedCount)

	// A pending embed job for mid was enqueued (against active fingerprint).
	requireEmbedJobPending(t, d, mid)
}

func TestOnThumbRegen_LeavesRetiredGensAlone(t *testing.T) { /* … */ }
```

**Step 2: Implement.**

```go
// internal/ai/embedding/on_thumb_regen.go
package embedding

// OnThumbRegen drops every (gen, media) mapping in non-retired generations,
// drops the corresponding vec0 row, decrements each affected gen's
// cached embedded_count, and enqueues a fresh embed job against the
// active or building fingerprint (whichever is present) so the worker
// re-embeds the media.
//
// Must be called inside the same write transaction that bumps
// thumb_version on the media row.
func OnThumbRegen(ctx context.Context, tx *sql.Tx, mediaID string) error {
	rows, err := tx.QueryContext(ctx,
		`SELECT id, vec_table_name FROM embedding_generations WHERE state IN ('active','building')`)
	if err != nil { return fmt.Errorf("list non-retired generations: %w", err) }
	defer rows.Close()

	type genTarget struct { id int64; table string }
	var targets []genTarget
	for rows.Next() {
		var t genTarget
		if err := rows.Scan(&t.id, &t.table); err != nil { return err }
		targets = append(targets, t)
	}
	if err := rows.Err(); err != nil { return err }

	for _, tgt := range targets {
		var prior sql.NullInt64
		row := tx.QueryRowContext(ctx,
			`DELETE FROM media_embedding_ids WHERE generation_id=? AND media_id=? RETURNING vec_id`,
			tgt.id, mediaID,
		)
		if err := row.Scan(&prior); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("drop mapping for gen %d: %w", tgt.id, err)
		}
		if !prior.Valid { continue }

		if _, err := tx.ExecContext(ctx,
			fmt.Sprintf(`DELETE FROM %s WHERE vec_id = ?`, tgt.table), prior.Int64,
		); err != nil { return fmt.Errorf("drop vec row gen %d: %w", tgt.id, err) }

		if _, err := tx.ExecContext(ctx,
			`UPDATE embedding_generations SET embedded_count = embedded_count - 1 WHERE id = ?`,
			tgt.id,
		); err != nil { return fmt.Errorf("dec embedded_count gen %d: %w", tgt.id, err) }
	}
	return nil
}
```

The re-enqueue happens through the existing housekeeping promotion: the `embed` gap scanner pass picks up media without a mapping in the active/building generation on its next tick. **Do not** enqueue a job inside this transaction — the gap scanner handles that and the spec keeps that boundary clean.

**Step 3: Wire into the thumb worker.**

In `internal/thumb/worker.go`, find the success path that bumps `thumb_version` and updates `thumb_status='ready'`. Inside the same write transaction, call `embedding.OnThumbRegen(ctx, tx, mediaID)` immediately after the version bump.

**Step 4: Run.**

Run: `go test ./internal/ai/embedding/ ./internal/thumb/ -count=1`

Expected: PASS.

**Step 5: Commit.**

```bash
git add internal/ai/embedding/on_thumb_regen.go internal/ai/embedding/on_thumb_regen_test.go internal/thumb/
git commit -m "feat(ai/embedding): invalidate vec mappings on thumb regen"
```

---

> **Cleanup checkpoint 4.** Run `/roborev-fix` on the current branch. Address any findings before starting Task J1. Commit any cleanup as its own commit(s); do not amend.

---

## Section J — FTS5 maintenance

### Task J1 — `RefreshMediaFTS` helper

**Files:**
- Create: `internal/search/index/fts.go`
- Create: `internal/search/index/fts_test.go`

**Step 1: Test — refresh reads the current state and writes one row.**

```go
// internal/search/index/fts_test.go
func TestRefreshMediaFTS_BuildsRowFromMediaCaptionTags(t *testing.T) {
	d := testutil.OpenTestDB(t)
	owner := seedOwner(t, d)
	mid := seedMediaWithDetails(t, d, owner, media.NewMedia{
		OriginalFilename: ptr("IMG_0001.jpg"),
		Make: ptr("Canon"), Model: ptr("EOS R5"), LensModel: ptr("RF24-105mm F4 L IS USM"),
		LocationLabel: ptr("Paris, France"),
	})
	mustWriteActiveCaption(t, d, mid, "small dog on a beach")
	mustWriteActiveTags(t, d, mid, []string{"dog", "beach"})

	require.NoError(t, withTx(d, func(tx *sql.Tx) error {
		return index.RefreshMediaFTS(ctx, tx, mid)
	}))

	row := readFTSRow(t, d, mid)
	require.Equal(t, "small dog on a beach", row.CaptionText)
	require.Equal(t, "dog beach", row.TagLabel)
	require.Equal(t, "IMG_0001.jpg", row.Filename)
	require.Equal(t, "Canon EOS R5", row.Camera)
	require.Equal(t, "RF24-105mm F4 L IS USM", row.Lens)
	require.Equal(t, "Paris, France", row.LocationLabel)
}

func TestRefreshMediaFTS_ReplacesPriorRow(t *testing.T) { /* refresh twice, second overrides first */ }

func TestRefreshMediaFTS_EmptyOnUnsetMediaCaptionTags(t *testing.T) {
	// Media row with no caption / no tags / no lens / etc → all corpus
	// columns are empty strings, FTS row still exists (so a future
	// caption refresh has something to delete).
}
```

**Step 2: Implement.**

```go
// internal/search/index/fts.go
package index

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// RefreshMediaFTS reads the current state of media + active caption +
// active tags for mediaID and replaces (delete-then-insert) the
// matching row in media_fts. Must be called inside the caller's write
// transaction so the FTS row reflects the same snapshot as any peer
// writes (e.g. AI promotion to active) in the tx.
func RefreshMediaFTS(ctx context.Context, tx *sql.Tx, mediaID string) error {
	const mediaQ = `
SELECT
  COALESCE(original_filename, ''),
  COALESCE(make, '') || CASE WHEN make IS NOT NULL AND model IS NOT NULL THEN ' ' ELSE '' END
                     || COALESCE(model, ''),
  COALESCE(lens_model, ''),
  COALESCE(location_label, '')
FROM media WHERE id = ?`

	var filename, camera, lens, locationLabel string
	if err := tx.QueryRowContext(ctx, mediaQ, mediaID).Scan(&filename, &camera, &lens, &locationLabel); err != nil {
		if errors.Is(err, sql.ErrNoRows) { return fmt.Errorf("media %s not found", mediaID) }
		return fmt.Errorf("read media: %w", err)
	}

	const captionQ = `
SELECT COALESCE(mc.text, '')
FROM ai_results r
LEFT JOIN media_captions mc ON mc.result_id = r.id
WHERE r.media_id = ? AND r.task = 'caption' AND r.status = 'active'`

	var captionText string
	if err := tx.QueryRowContext(ctx, captionQ, mediaID).Scan(&captionText); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read caption: %w", err)
	}

	const tagsQ = `
SELECT mt.tag_label
FROM ai_results r
JOIN media_tags mt ON mt.result_id = r.id
WHERE r.media_id = ? AND r.task = 'tag' AND r.status = 'active'
ORDER BY mt.rank`

	rows, err := tx.QueryContext(ctx, tagsQ, mediaID)
	if err != nil { return fmt.Errorf("read tags: %w", err) }
	defer rows.Close()
	var labels []string
	for rows.Next() {
		var label string
		if err := rows.Scan(&label); err != nil { return err }
		labels = append(labels, label)
	}
	if err := rows.Err(); err != nil { return err }
	tagLabel := strings.Join(labels, " ")

	if _, err := tx.ExecContext(ctx, `DELETE FROM media_fts WHERE media_id = ?`, mediaID); err != nil {
		return fmt.Errorf("delete fts row: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO media_fts (media_id, caption_text, tag_label, filename, camera, lens, location_label)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		mediaID, captionText, tagLabel, filename, camera, lens, locationLabel,
	); err != nil { return fmt.Errorf("insert fts row: %w", err) }

	return nil
}
```

**Step 3: Run.**

Run: `go test ./internal/search/index/ -count=1 -run "TestRefreshMediaFTS"`

Expected: PASS.

**Step 4: Commit.**

```bash
git add internal/search/index/fts.go internal/search/index/fts_test.go
git commit -m "feat(search/index): RefreshMediaFTS application-owned helper"
```

---

### Task J2 — Wire `RefreshMediaFTS` into importer, AI promotion, reconcile, media-update

**Files:**
- Modify: `internal/ingest/...` — call `index.RefreshMediaFTS(ctx, tx, mid)` after media row insert
- Modify: `internal/ai/results/repo.go` — call `RefreshMediaFTS` as the final write inside the promotion transaction (the `Promote` method that transitions an `ai_results` row to `active`)
- Modify: `internal/reconcile/reconcile.go` — call `RefreshMediaFTS` after every media-touching update
- Modify: `internal/media/repo.go` — call `RefreshMediaFTS` from `UpdateLocationLabel` and any other update path that touches `make`, `model`, `lens_model`, `original_filename`, `location_label`

**Step 1: Tests — every wiring point exercised end-to-end.**

```go
// internal/ingest/import_test.go (append)
func TestImporter_PopulatesFTSImmediately(t *testing.T) {
	d := testutil.OpenTestDB(t)
	imp := /* … */
	mid := imp.ImportFile(ctx, "/path/to/photo.jpg")

	row := readFTSRow(t, d, mid)
	require.Equal(t, "photo.jpg", row.Filename)
	require.Empty(t, row.CaptionText, "caption is empty until AI runs")
}

// internal/ai/results/repo_test.go (append)
func TestResults_PromoteRefreshesFTS(t *testing.T) {
	d := testutil.OpenTestDB(t)
	owner := seedOwner(t, d)
	mid := seedMedia(t, d, owner)
	mustWriteFTS(t, d, mid) // empty caption

	rr := results.NewRepo(d.WriteDB(), d.ReadDB())
	require.NoError(t, rr.PromoteCaption(ctx, mid, ai.Fingerprint{ModelID: "m"}, "small dog on a beach"))

	row := readFTSRow(t, d, mid)
	require.Equal(t, "small dog on a beach", row.CaptionText)
}

// Similarly for tags, reconcile-driven media field changes,
// UpdateLocationLabel, etc.
```

**Step 2: Wire each call site.**

Importer: at the end of the per-row finalize tx (the same tx that inserts into `media`), call `index.RefreshMediaFTS(ctx, tx, mediaID)`.

AI results repo: in `Promote*` (caption + tag both), at the end of the existing promotion tx (after the active row is set), call `index.RefreshMediaFTS(ctx, tx, mediaID)`.

Reconcile: for any code path that updates a media field included in the FTS corpus, call refresh inside the same tx.

Media repo: `UpdateLocationLabel`, `UpdateCameraInfo` (if such a path exists), and any future updaters of corpus columns — call refresh.

**Step 3: Run.**

Run: `go test ./internal/ingest/ ./internal/ai/results/ ./internal/reconcile/ ./internal/media/ -count=1`

Expected: PASS.

**Step 4: Commit.**

```bash
git add internal/ingest/ internal/ai/results/ internal/reconcile/ internal/media/
git commit -m "feat(search): wire RefreshMediaFTS at every corpus-touching write"
```

---

## Section K — Retired-generation compaction

### Task K1 — Compaction sweep + CLI hook

**Files:**
- Create: `internal/ai/embedding/compactor.go`
- Create: `internal/ai/embedding/compactor_test.go`

**Step 1: Test.**

```go
func TestCompactor_DropsRetiredOlderThanWindow(t *testing.T) {
	d := testutil.OpenTestDB(t)
	g := mustCreateRetiredGen(t, d, 768, time.Now().Add(-31*24*time.Hour))
	c := embedding.NewCompactor(d.WriteDB(), 30*24*time.Hour)

	dropped, err := c.SweepOnce(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, dropped)

	// Generation row gone.
	gens := embedding.NewGenerations(d.WriteDB(), d.ReadDB())
	row, _ := gens.GetByID(ctx, g.ID)
	require.Nil(t, row)
	// Vec table dropped (CREATE TABLE on a different name to avoid
	// conflicts; assert the original is gone).
	requireTableDoesNotExist(t, d, g.VecTableName)
}

func TestCompactor_LeavesRecentRetiredAlone(t *testing.T) {
	// Retired 5 days ago — must remain.
}
```

**Step 2: Implement.**

```go
// internal/ai/embedding/compactor.go
package embedding

type Compactor struct {
	rw     *sql.DB
	window time.Duration
}

func NewCompactor(rw *sql.DB, retainRetired time.Duration) *Compactor {
	return &Compactor{rw: rw, window: retainRetired}
}

// SweepOnce drops every retired generation whose retired_at is older
// than now - window. Returns the count of generations dropped.
func (c *Compactor) SweepOnce(ctx context.Context) (int, error) {
	cutoff := time.Now().UTC().Add(-c.window)

	rows, err := c.rw.QueryContext(ctx,
		`SELECT id, vec_table_name FROM embedding_generations
		 WHERE state='retired' AND retired_at IS NOT NULL AND retired_at < ?`, cutoff)
	if err != nil { return 0, fmt.Errorf("list candidates: %w", err) }
	defer rows.Close()

	type cand struct { id int64; table string }
	var cs []cand
	for rows.Next() {
		var c cand
		if err := rows.Scan(&c.id, &c.table); err != nil { return 0, err }
		cs = append(cs, c)
	}
	if err := rows.Err(); err != nil { return 0, err }

	dropped := 0
	for _, x := range cs {
		tx, err := c.rw.BeginTx(ctx, nil)
		if err != nil { return dropped, err }
		ok := false
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`DROP TABLE IF EXISTS %s`, x.table)); err != nil {
			_ = tx.Rollback()
			return dropped, fmt.Errorf("drop %s: %w", x.table, err)
		}
		// FK cascade from embedding_generations → media_embedding_ids
		// drops the mapping rows automatically.
		if _, err := tx.ExecContext(ctx, `DELETE FROM embedding_generations WHERE id = ?`, x.id); err != nil {
			_ = tx.Rollback()
			return dropped, fmt.Errorf("delete generation %d: %w", x.id, err)
		}
		if err := tx.Commit(); err != nil { return dropped, err }
		ok = true
		_ = ok
		dropped++
	}
	return dropped, nil
}
```

**Step 3: Wire into housekeeping tick.**

In `internal/cli/server.go`, schedule the compactor sweep alongside the activator tick (default once per day; configurable via `[search] retain_retired_days`).

**Step 4: Run.**

Run: `go test ./internal/ai/embedding/ -count=1 -run "TestCompactor_"`

Expected: PASS.

**Step 5: Commit.**

```bash
git add internal/ai/embedding/compactor.go internal/ai/embedding/compactor_test.go internal/cli/server.go
git commit -m "feat(ai/embedding): retired-generation compaction"
```

---

## Section L — Search backend

### Task L1 — `Backend` interface + sqlite-vec composed FusedSearch

**Files:**
- Create: `internal/search/index/backend.go`
- Create: `internal/search/index/sqlitevec.go`
- Create: `internal/search/index/sqlitevec_test.go`

**Step 1: Tests — composed FusedSearch returns RRF-ordered hits over a fixture.**

Seed three media with known caption + tag content, embed three known vectors against a building→active generation, and assert the order of returned hits for a query that should rank one specific media first.

```go
// internal/search/index/sqlitevec_test.go
func TestSQLiteVec_FusedSearch_HappyPath(t *testing.T) {
	d := testutil.OpenTestDB(t)
	owner := seedOwner(t, d)
	mids := []string{seedMedia(t, d, owner), seedMedia(t, d, owner), seedMedia(t, d, owner)}
	for i, m := range mids {
		mustWriteThumbReady(t, d, m)
		mustWriteFTSCorpus(t, d, m, ftsRow{
			Caption: []string{"dog beach", "cat tree", "small dog on a beach"}[i],
			Tags: []string{"dog beach", "cat tree", "small dog beach"}[i],
		})
	}
	gen := mustCreateActiveGenWithVectors(t, d, 768, map[string][]float32{
		mids[0]: vecForText("dog beach"),
		mids[1]: vecForText("cat tree"),
		mids[2]: vecForText("small dog on a beach"),
	})

	b := index.NewSQLiteVecBackend(d.ReadDB(), gen)
	hits, err := b.FusedSearch(ctx, index.SearchInput{
		Query:       "small dog on a beach",
		QueryVector: vecForText("small dog on a beach"),
		Owner:       owner,
		KPerSignal:  200,
		RRFK:        60,
		Limit:       10,
	})
	require.NoError(t, err)
	require.NotEmpty(t, hits)
	require.Equal(t, mids[2], hits[0].MediaID, "best hit must be the 'small dog on a beach' row")
}
```

**Step 2: Implement.**

```go
// internal/search/index/backend.go
package index

import (
	"context"
	"database/sql"

	"github.com/wesm/fotobank/internal/owners"
)

// Backend is the read-side abstraction. The hybrid Engine builds a
// SearchInput, calls Backend.FusedSearch, and returns its hits to
// the service layer. Implementations: sqlite-vec for production;
// stubs for tests that don't want to spin a real vec table.
type Backend interface {
	FusedSearch(ctx context.Context, in SearchInput) ([]Hit, error)
	BM25Only(ctx context.Context, in SearchInput) ([]Hit, error)
	FilterOnly(ctx context.Context, in SearchInput) ([]Hit, error)
}

type SearchInput struct {
	Query        string             // tokenized + escaped FTS5 MATCH expression (built by hybrid.query.go)
	QueryVector  []float32          // empty when no semantic signal
	Owner        owners.Principal
	IncludeHidden bool
	Filter       FilterCTE          // see hybrid.filter.go
	Sort         Sort               // relevance | newest | oldest
	KPerSignal   int
	RRFK         int
	Limit        int
	Cursor       *Cursor            // nil for first page
}

type Hit struct {
	MediaID         string
	MediaType       string
	Timestamp       *time.Time
	ImportedAt      time.Time
	Width           *int
	Height          *int
	ThumbVersion    int
	Score           float64           // RRF or BM25 depending on engine mode
	ScoreComponents *ScoreComponents  // populated when explain=true
}

type ScoreComponents struct {
	RRF        *float64
	BM25       *float64
	Vector     *float64
	RankBM25   *int
	RankVector *int
}

// internal/search/index/sqlitevec.go
package index

type SQLiteVecBackend struct {
	ro  *sql.DB
	gen embedding.Row // active generation; can be empty for bm25-only
}

func NewSQLiteVecBackend(ro *sql.DB, gen embedding.Row) *SQLiteVecBackend {
	return &SQLiteVecBackend{ro: ro, gen: gen}
}

// FusedSearch is the composed BM25 + ANN + filter intersection +
// RRF fusion in one SQL statement. Adapts msgvault's pattern.
//
// Skeleton (concrete column lists and CTE names live in the
// implementation):
//
//   WITH
//     filter AS (SELECT id, timestamp, imported_at FROM media m WHERE <filter conjuncts>),
//     bm25 AS (
//       SELECT mf.media_id AS id, bm25(media_fts) AS score,
//              ROW_NUMBER() OVER (ORDER BY bm25(media_fts)) AS rank_bm25
//       FROM media_fts mf JOIN filter f ON f.id = mf.media_id
//       WHERE media_fts MATCH ?
//       ORDER BY score
//       LIMIT ?
//     ),
//     ann AS (
//       SELECT x.media_id AS id, distance AS score,
//              ROW_NUMBER() OVER (ORDER BY distance) AS rank_vector
//       FROM <vec_table_name> v
//       JOIN media_embedding_ids x ON x.generation_id = ? AND x.vec_id = v.vec_id
//       JOIN filter f ON f.id = x.media_id
//       WHERE embedding MATCH vec_f32(?) AND k = ?
//     ),
//     fused AS (
//       SELECT COALESCE(b.id, a.id) AS id,
//              ((1.0/(? + b.rank_bm25)) + (1.0/(? + a.rank_vector))) AS rrf,
//              b.rank_bm25, a.rank_vector,
//              b.score AS bm25, a.score AS vec
//       FROM bm25 b FULL OUTER JOIN ann a ON a.id = b.id
//     )
//   SELECT m.id, m.media_type, m.timestamp, m.imported_at, m.width, m.height,
//          m.thumb_version, fused.rrf, fused.bm25, fused.vec,
//          fused.rank_bm25, fused.rank_vector
//   FROM fused JOIN media m ON m.id = fused.id
//   ORDER BY fused.rrf DESC, m.id LIMIT ?;
//
// SQLite doesn't have FULL OUTER JOIN; emulate with two
// LEFT JOINs UNION'd. The vec_table_name segment is interpolated
// via fmt.Sprintf("media_embeddings_g%d", b.gen.ID) — never user input.
func (b *SQLiteVecBackend) FusedSearch(ctx context.Context, in SearchInput) ([]Hit, error) {
	// Build the SQL with filter CTE bind args from in.Filter.
	// ...
}
```

(The concrete SQL is interpolated by `fmt.Sprintf` for the `vec_table_name` piece; everything else is parameterized via `?`. The composed statement is the single hot path; cursor pagination on RRF is handled by the engine, not the backend.)

**Step 3: Run.**

Run: `go test ./internal/search/index/ -count=1 -run "TestSQLiteVec_"`

Expected: PASS.

**Step 4: Commit.**

```bash
git add internal/search/index/backend.go internal/search/index/sqlitevec.go internal/search/index/sqlitevec_test.go
git commit -m "feat(search/index): sqlite-vec composed FusedSearch"
```

---

## Section M — Hybrid engine

### Task M1 — Filter resolution → CTE fragments

**Files:**
- Create: `internal/search/hybrid/filter.go`
- Create: `internal/search/hybrid/filter_test.go`

**Step 1: Tests.**

```go
func TestFilter_DateRangeHalfOpen(t *testing.T) {
	cte, args := hybrid.Resolve(hybrid.Input{
		Owner:      owner,
		DateAfter:  ptr(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)),
		DateBefore: ptr(time.Date(2025, 7, 1, 0, 0, 0, 0, time.UTC)),
	})
	require.Contains(t, cte, "m.timestamp >= ?")
	require.Contains(t, cte, "m.timestamp < ?")
	require.Equal(t, []any{owner.Hub, owner.UserID, /* date_after, date_before */}, args)
}

func TestFilter_TagsAreANDComposed(t *testing.T) { /* multiple EXISTS clauses */ }
func TestFilter_LocationExactMatch(t *testing.T) { /* m.location_label = ? */ }
func TestFilter_MediaTypeFilter(t *testing.T) { /* m.media_type = ? */ }
func TestFilter_HiddenExclusionByDefault(t *testing.T) { /* m.hidden_at IS NULL */ }
func TestFilter_HiddenIncludedRequiresUnlockClaim(t *testing.T) { /* IncludeHidden=true */ }
```

**Step 2: Implement.**

```go
// internal/search/hybrid/filter.go
package hybrid

import (
	"fmt"
	"strings"
	"time"

	"github.com/wesm/fotobank/internal/owners"
)

type Input struct {
	Owner          owners.Principal
	DateAfter      *time.Time
	DateBefore     *time.Time
	TagKeys        []string
	LocationLabel  *string
	MediaType      *string
	IncludeHidden  bool
}

// Resolve returns the body of a `filter` CTE (without the leading
// "WITH filter AS (...)") and the bind arguments. The CTE projects
// (id, timestamp, imported_at). The hybrid engine wraps it.
func Resolve(in Input) (cte string, args []any) {
	var conds []string
	conds = append(conds, "m.owner_hub = ?", "m.owner_user_id = ?")
	args = append(args, in.Owner.Hub, in.Owner.UserID)

	if in.DateAfter != nil { conds = append(conds, "m.timestamp >= ?"); args = append(args, *in.DateAfter) }
	if in.DateBefore != nil { conds = append(conds, "m.timestamp < ?"); args = append(args, *in.DateBefore) }

	for _, key := range in.TagKeys {
		conds = append(conds,
			`EXISTS (SELECT 1 FROM media_tags mt
			          JOIN ai_results r ON mt.result_id = r.id
			         WHERE r.media_id = m.id AND r.task = 'tag' AND r.status = 'active'
			           AND mt.tag_key = ?)`,
		)
		args = append(args, key)
	}

	if in.LocationLabel != nil { conds = append(conds, "m.location_label = ?"); args = append(args, *in.LocationLabel) }
	if in.MediaType != nil { conds = append(conds, "m.media_type = ?"); args = append(args, *in.MediaType) }

	if !in.IncludeHidden { conds = append(conds, "m.hidden_at IS NULL") }

	cte = fmt.Sprintf(
		`SELECT m.id, m.timestamp, m.imported_at FROM media m WHERE %s`,
		strings.Join(conds, " AND "),
	)
	return cte, args
}
```

**Step 3: Run.**

Run: `go test ./internal/search/hybrid/ -count=1 -run "TestFilter_"`

Expected: PASS.

**Step 4: Commit.**

```bash
git add internal/search/hybrid/filter.go internal/search/hybrid/filter_test.go
git commit -m "feat(search/hybrid): structured filter resolution"
```

---

> **Cleanup checkpoint 5.** Run `/roborev-fix` on the current branch. Address any findings before starting Task M2. Commit any cleanup as its own commit(s); do not amend.

---

### Task M2 — FTS5 MATCH expression construction (escape + trailing prefix)

**Files:**
- Create: `internal/search/hybrid/query.go`
- Create: `internal/search/hybrid/query_test.go`

**Step 1: Tests.**

```go
// internal/search/hybrid/query_test.go
func TestBuildMatchExpr(t *testing.T) {
	tests := []struct {
		name    string
		q       string
		want    string
		wantOk  bool
	}{
		{"single complete token (trailing space)", "small ", `"small"`, true},
		{"complete + trailing prefix", "small dog", `"small" AND "dog"*`, true},
		{"escape FTS5 chars", `dog "cat"`, `"dog" AND "cat"*`, true},
		{"drop short tokens", "a small dog on", `"small" AND "dog" AND "on"*`, true},
		{"all tokens drop → empty", "!! ?", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := hybrid.BuildMatchExpr(tt.q)
			require.Equal(t, tt.wantOk, ok)
			require.Equal(t, tt.want, got)
		})
	}
}
```

**Step 2: Implement.**

```go
// internal/search/hybrid/query.go
package hybrid

import (
	"strings"
	"unicode"
)

// BuildMatchExpr converts user-typed q into an FTS5 MATCH expression:
//
//   1. Tokenize by whitespace.
//   2. Strip FTS5-meaningful chars from each token (`"`, `*`, `(`, `)`, `:`, `^`).
//   3. Drop tokens shorter than 2 chars.
//   4. Wrap each token in double quotes (phrase tokens — prevents prefix
//      interpretation by FTS5).
//   5. Apply trailing prefix to the LAST token when q does not end in
//      whitespace.
//   6. Join with " AND ".
//
// Returns (expr, true) on success, ("", false) when every token was
// dropped and the query collapses to empty.
func BuildMatchExpr(q string) (string, bool) {
	endsInSpace := q == "" || unicode.IsSpace(rune(q[len(q)-1]))
	tokens := tokenize(q)
	if len(tokens) == 0 { return "", false }
	pieces := make([]string, len(tokens))
	for i, tok := range tokens {
		quoted := `"` + tok + `"`
		if i == len(tokens)-1 && !endsInSpace {
			quoted = quoted + "*"
		}
		pieces[i] = quoted
	}
	return strings.Join(pieces, " AND "), true
}

// tokenize splits q by whitespace, strips FTS5-meaningful chars from
// each token, and drops tokens shorter than 2 chars.
func tokenize(q string) []string {
	var out []string
	for _, raw := range strings.Fields(q) {
		clean := stripFTS5Specials(raw)
		if len(clean) < 2 { continue }
		out = append(out, clean)
	}
	return out
}

// stripFTS5Specials removes the FTS5-meaningful characters from s.
// Done as a strip rather than escape: FTS5 column-prefix syntax is
// `column:term`, but our terms never carry a colon meaningfully; users
// who type a colon are not addressing FTS5 columns. Same for the
// `^` near-operator and the parens.
func stripFTS5Specials(s string) string {
	r := strings.NewReplacer(`"`, "", `*`, "", `(`, "", `)`, "", `:`, "", `^`, "")
	return r.Replace(s)
}
```

**Step 3: Run.**

Run: `go test ./internal/search/hybrid/ -count=1 -run "TestBuildMatchExpr"`

Expected: PASS.

**Step 4: Commit.**

```bash
git add internal/search/hybrid/query.go internal/search/hybrid/query_test.go
git commit -m "feat(search/hybrid): FTS5 MATCH expression construction"
```

---

### Task M3 — Engine.Search routing + cursor encode/decode

**Files:**
- Create: `internal/search/hybrid/engine.go`
- Create: `internal/search/hybrid/engine_test.go`
- Create: `internal/search/hybrid/cursor.go`
- Create: `internal/search/hybrid/cursor_test.go`
- Create: `internal/search/hybrid/rrf.go` — small helper if not folded into engine.go

**Step 1: Cursor tests.**

```go
// internal/search/hybrid/cursor_test.go
func TestCursor_RoundTrip(t *testing.T) {
	c := hybrid.Cursor{ReqHash: "abc", K1: 0.0156, K2: 1737562800, ID: "uuid-1"}
	enc := hybrid.EncodeCursor(c)
	dec, err := hybrid.DecodeCursor(enc)
	require.NoError(t, err)
	require.Equal(t, c, dec)
}

func TestCursor_RejectsHashMismatch(t *testing.T) {
	c := hybrid.Cursor{ReqHash: "old"}
	enc := hybrid.EncodeCursor(c)
	_, err := hybrid.DecodeCursorAndCheck(enc, "new")
	require.Error(t, err)
}

func TestCursor_ReqHashUsesEffectiveSort(t *testing.T) {
	// Two requests that differ only in raw sort but both coerce to
	// "newest" must produce the same hash.
	h1 := hybrid.NormalizedHash(hybrid.NormalizedReq{Q: "", Sort: "newest", IncludeHidden: false, EngineMode: "filter_only"})
	h2 := hybrid.NormalizedHash(hybrid.NormalizedReq{Q: "", Sort: "newest", IncludeHidden: false, EngineMode: "filter_only"})
	require.Equal(t, h1, h2)
}
```

**Step 2: Cursor implementation.**

```go
// internal/search/hybrid/cursor.go
package hybrid

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

type Cursor struct {
	ReqHash string  `json:"req_hash"`
	K1      float64 `json:"k1"`
	K2      int64   `json:"k2"`
	ID      string  `json:"id"`
}

func EncodeCursor(c Cursor) string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}

func DecodeCursor(s string) (Cursor, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil { return Cursor{}, fmt.Errorf("cursor base64: %w", err) }
	var c Cursor
	if err := json.Unmarshal(b, &c); err != nil { return Cursor{}, fmt.Errorf("cursor json: %w", err) }
	return c, nil
}

func DecodeCursorAndCheck(s, expectedHash string) (Cursor, error) {
	c, err := DecodeCursor(s)
	if err != nil { return Cursor{}, err }
	if c.ReqHash != expectedHash {
		return Cursor{}, fmt.Errorf("cursor request-hash mismatch")
	}
	return c, nil
}

type NormalizedReq struct {
	Q             string
	Sort          string // effective sort
	IncludeHidden bool
	EngineMode    string // hybrid | bm25_only | filter_only
	Filter        map[string]string // sorted key=value pairs
}

func NormalizedHash(r NormalizedReq) string {
	keys := make([]string, 0, len(r.Filter))
	for k := range r.Filter { keys = append(keys, k) }
	sort.Strings(keys)
	var fb []byte
	for _, k := range keys {
		fb = append(fb, k...)
		fb = append(fb, '=')
		fb = append(fb, r.Filter[k]...)
		fb = append(fb, ';')
	}
	h := sha256.New()
	_, _ = h.Write([]byte(r.Q))
	_, _ = h.Write([]byte{'|'})
	_, _ = h.Write([]byte(r.Sort))
	_, _ = h.Write([]byte{'|'})
	if r.IncludeHidden { _, _ = h.Write([]byte("h")) } else { _, _ = h.Write([]byte("v")) }
	_, _ = h.Write([]byte{'|'})
	_, _ = h.Write([]byte(r.EngineMode))
	_, _ = h.Write([]byte{'|'})
	_, _ = h.Write(fb)
	return hex.EncodeToString(h.Sum(nil))
}
```

**Step 3: Engine tests.**

```go
// internal/search/hybrid/engine_test.go
func TestEngine_FilterOnlyForEmptyQ(t *testing.T) {
	// q == "" — engine must call BackendBM25? no — calls FilterOnly with date sort.
}

func TestEngine_RelevanceWithoutQCoercedToNewest(t *testing.T) {
	// q == "" && sort == "relevance" → effective_sort == "newest", no semantic_unavailable change.
}

func TestEngine_BM25OnlyWhenNoActiveGen(t *testing.T) {
	// Active generation absent → FusedSearch never called; BM25Only is.
	// SemanticUnavailable=true, reason="no_active_generation".
}

func TestEngine_DegradesOnQueryEmbeddingFailure(t *testing.T) {
	// Active generation present, but EmbedTexts returns ErrTransient.
	// Engine falls back to BM25Only with reason="query_embedding_failed".
}

func TestEngine_DateSortHybridCandidateSelection(t *testing.T) {
	// q != "", sort = "newest", active gen present → FusedSearch with
	// KPerSignal*2 candidates, then sort by (timestamp NULLS LAST,
	// imported_at, id) for the page.
}
```

**Step 4: Engine implementation.**

```go
// internal/search/hybrid/engine.go
package hybrid

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/wesm/fotobank/internal/ai/embedding"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/search"
	"github.com/wesm/fotobank/internal/search/index"
)

type Engine struct {
	backend    index.Backend
	embedClient embedding.ClientIface
	gens       *embedding.Generations
	cfg        search.Config
}

func NewEngine(b index.Backend, c embedding.ClientIface, g *embedding.Generations, cfg search.Config) *Engine {
	return &Engine{backend: b, embedClient: c, gens: g, cfg: cfg}
}

type Request struct {
	Owner         owners.Principal
	Query         string
	Sort          string // raw — engine coerces to effective
	Filter        Input  // from filter.go
	IncludeHidden bool
	Limit         int
	Cursor        string // empty for first page
	Explain       bool
}

type Response struct {
	Hits                       []index.Hit
	NextCursor                 string
	HasMore                    bool
	Total                      *int
	EffectiveSort              string
	EmbeddingCompleteness      float64
	SemanticUnavailable        bool
	SemanticUnavailableReason  string
}

func (e *Engine) Search(ctx context.Context, req Request) (Response, error) {
	// Step 1: coerce relevance-without-query.
	effSort := req.Sort
	if req.Query == "" && req.Sort == "relevance" { effSort = "newest" }

	active, err := e.gens.FindActive(ctx)
	if err != nil { return Response{}, fmt.Errorf("find active generation: %w", err) }

	var (
		semanticAvailable = active != nil
		semanticReason    string
	)

	// Step 2: branch.
	switch {
	case req.Query == "":
		// filter-only browse
		filterCTE, filterArgs := Resolve(req.Filter.WithOwner(req.Owner).WithHidden(req.IncludeHidden))
		hits, err := e.backend.FilterOnly(ctx, index.SearchInput{
			Filter: index.FilterCTE{SQL: filterCTE, Args: filterArgs},
			Sort:   index.SortFromString(effSort),
			Limit:  req.Limit,
			Cursor: parseCursor(req.Cursor),
		})
		if err != nil { return Response{}, err }
		return e.buildResponse(ctx, req, effSort, hits, semanticAvailable, semanticReason)

	case effSort == "relevance":
		matchExpr, ok := BuildMatchExpr(req.Query)
		if !ok {
			// Fall through to filter-only.
			return e.Search(ctx, Request{Owner: req.Owner, Query: "", Sort: effSort, Filter: req.Filter, IncludeHidden: req.IncludeHidden, Limit: req.Limit, Cursor: req.Cursor, Explain: req.Explain})
		}
		var queryVec []float32
		if semanticAvailable {
			vecs, err := e.embedClient.EmbedTexts(ctx, []string{req.Query})
			if err == nil && len(vecs) == 1 {
				queryVec = vecs[0]
			} else {
				semanticAvailable = false
				semanticReason = "query_embedding_failed"
				log.Warn(ctx, "query embed failed", "err", err)
			}
		} else {
			semanticReason = "no_active_generation"
		}

		filterCTE, filterArgs := Resolve(req.Filter.WithOwner(req.Owner).WithHidden(req.IncludeHidden))
		input := index.SearchInput{
			Query:        matchExpr,
			QueryVector:  queryVec,
			Filter:       index.FilterCTE{SQL: filterCTE, Args: filterArgs},
			KPerSignal:   e.cfg.KPerSignal,
			RRFK:         e.cfg.RRFK,
			Sort:         index.SortRelevance,
			Limit:        req.Limit,
			Cursor:       parseCursor(req.Cursor),
		}
		var hits []index.Hit
		if semanticAvailable {
			hits, err = e.backend.FusedSearch(ctx, input)
		} else {
			hits, err = e.backend.BM25Only(ctx, input)
		}
		if err != nil { return Response{}, err }
		return e.buildResponse(ctx, req, effSort, hits, semanticAvailable, semanticReason)

	default: // q != "" && effSort in (newest, oldest)
		// Date-sorted candidate selection.
		// Same code as relevance, but the backend candidate query
		// returns KPerSignal*2 then re-sorts by (timestamp NULLS LAST, imported_at, id).
		// ...
	}
}

func parseCursor(s string) *Cursor { /* … */ }
```

**Step 5: Run.**

Run: `go test ./internal/search/hybrid/ -count=1`

Expected: PASS.

**Step 6: Commit.**

```bash
git add internal/search/hybrid/engine.go internal/search/hybrid/cursor.go internal/search/hybrid/cursor_test.go internal/search/hybrid/engine_test.go internal/search/hybrid/rrf.go
git commit -m "feat(search/hybrid): engine routing + cursor encoding"
```

---

## Section N — Service layer

### Task N1 — `service/search` auth-scoped wrapper

**Files:**
- Create: `internal/service/search/service.go`
- Create: `internal/service/search/service_test.go`

**Step 1: Tests.**

```go
func TestService_RejectsIncludeHiddenWithoutUnlockClaim(t *testing.T) {
	svc := /* construct service */
	_, err := svc.Search(ctx, owner, search.Request{IncludeHidden: true /* no unlock claim */})
	require.ErrorIs(t, err, errs.ErrPermissionDenied)
}

func TestService_OwnerScopingIsAlwaysEnforced(t *testing.T) {
	// Even when caller passes IncludeHidden=false, owner_hub/user_id is
	// pinned by service to caller's principal, never honored from request.
}

func TestService_ResolvesTagChipsToTagKey(t *testing.T) {
	// User-supplied chip {tag_label: "Dog"} resolves to tag_key="dog"
	// via the autocomplete-style lookup before the engine sees it.
}

func TestService_PassesThroughExplainOnlyWhenSettingsAllow(t *testing.T) {
	// Even if request.Explain=true, the diagnostics flag is honored
	// only when user_settings has the AI Inspection toggle on.
}
```

**Step 2: Implement.**

```go
// internal/service/search/service.go
package search

import (
	"context"

	"github.com/wesm/fotobank/internal/auth/hidden"
	"github.com/wesm/fotobank/internal/errs"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/search/hybrid"
)

type Service struct {
	engine        *hybrid.Engine
	usersettings  UserSettingsRepo
	tagResolver   TagResolver
	hiddenChecker hidden.Checker
}

type Request struct {
	Query         string
	Sort          string
	DateAfter, DateBefore *time.Time
	TagLabels     []string // resolved to tag_key inside Search
	LocationLabel *string
	MediaType     *string
	IncludeHidden bool
	Limit         int
	Cursor        string
	Explain       bool
	UnlockClaim   *hidden.Claim
}

func (s *Service) Search(ctx context.Context, caller owners.Principal, req Request) (hybrid.Response, error) {
	// Hidden gate.
	if req.IncludeHidden {
		if req.UnlockClaim == nil || !s.hiddenChecker.Valid(req.UnlockClaim, caller) {
			return hybrid.Response{}, errs.ErrPermissionDenied
		}
	}

	tagKeys, err := s.tagResolver.LabelsToKeys(ctx, caller, req.TagLabels)
	if err != nil { return hybrid.Response{}, fmt.Errorf("resolve tags: %w", err) }

	// Diagnostics gate.
	explain := req.Explain
	if explain {
		on, err := s.usersettings.AIInspectionEnabled(ctx, caller)
		if err != nil { return hybrid.Response{}, fmt.Errorf("read settings: %w", err) }
		if !on { explain = false }
	}

	hreq := hybrid.Request{
		Owner: caller, Query: req.Query, Sort: req.Sort,
		Filter: hybrid.Input{
			DateAfter: req.DateAfter, DateBefore: req.DateBefore,
			TagKeys: tagKeys, LocationLabel: req.LocationLabel, MediaType: req.MediaType,
		},
		IncludeHidden: req.IncludeHidden,
		Limit:         req.Limit,
		Cursor:        req.Cursor,
		Explain:       explain,
	}

	return s.engine.Search(ctx, hreq)
}
```

**Step 3: Run.**

Run: `go test ./internal/service/search/ -count=1`

Expected: PASS.

**Step 4: Commit.**

```bash
git add internal/service/search/service.go internal/service/search/service_test.go
git commit -m "feat(service/search): auth-scoped search wrapper"
```

---

### Task N2 — Embedding-completeness under the request's hidden predicate

**Files:**
- Create: `internal/service/search/completeness.go`
- Create: `internal/service/search/completeness_test.go`

**Step 1: Tests.**

```go
func TestCompleteness_VisibleOnlyMatchesDenominator(t *testing.T) {
	d := testutil.OpenTestDB(t)
	owner := seedOwner(t, d)
	for i := 0; i < 10; i++ {
		mid := seedMedia(t, d, owner)
		mustWriteThumbReady(t, d, mid)
		// 7 visible, 3 hidden.
		if i >= 7 { mustHide(t, d, mid) }
	}
	gen := mustCreateActiveGenWithMappings(t, d, 768, /* 6 of 7 visible mapped, 0 hidden */)

	svc := /* … */
	got, err := svc.EmbeddingCompleteness(ctx, owner, false /* includeHidden */)
	require.NoError(t, err)
	require.InDelta(t, 6.0/7.0, got, 0.001, "visible-only count: 6/7")
}

func TestCompleteness_IncludeHiddenWithUnlockSeesAll(t *testing.T) {
	// Same fixture; includeHidden=true → 6/10 (no hidden are mapped in this fixture).
}

func TestCompleteness_NoActiveGenReturnsZero(t *testing.T) {
	// When no active generation exists, completeness is 0.
}
```

**Step 2: Implement.**

```go
// internal/service/search/completeness.go
package search

func (s *Service) EmbeddingCompleteness(ctx context.Context, caller owners.Principal, includeHidden bool) (float64, error) {
	active, err := s.gens.FindActive(ctx)
	if err != nil { return 0, err }
	if active == nil { return 0, nil }

	const eligibleSQL = `
SELECT COUNT(*) FROM media m
 WHERE m.owner_hub = ? AND m.owner_user_id = ?
   AND m.thumb_status = 'ready'
   AND (m.hidden_at IS NULL OR ?)
   AND NOT EXISTS (
     SELECT 1 FROM ai_skipped sk WHERE sk.media_id = m.id AND sk.task = 'embed'
   )`
	const embeddedSQL = `
SELECT COUNT(*) FROM media_embedding_ids x
 JOIN media m ON m.id = x.media_id
 WHERE x.generation_id = ?
   AND m.owner_hub = ? AND m.owner_user_id = ?
   AND m.thumb_status = 'ready'
   AND (m.hidden_at IS NULL OR ?)
   AND NOT EXISTS (
     SELECT 1 FROM ai_skipped sk WHERE sk.media_id = m.id AND sk.task = 'embed'
   )`

	var eligible, embedded int
	if err := s.ro.QueryRowContext(ctx, eligibleSQL, caller.Hub, caller.UserID, includeHidden).Scan(&eligible); err != nil {
		return 0, fmt.Errorf("eligible: %w", err)
	}
	if err := s.ro.QueryRowContext(ctx, embeddedSQL, active.ID, caller.Hub, caller.UserID, includeHidden).Scan(&embedded); err != nil {
		return 0, fmt.Errorf("embedded: %w", err)
	}
	if eligible == 0 { return 0, nil }
	return float64(embedded) / float64(eligible), nil
}
```

**Step 3: Run.**

Run: `go test ./internal/service/search/ -count=1`

Expected: PASS.

**Step 4: Commit.**

```bash
git add internal/service/search/completeness.go internal/service/search/completeness_test.go
git commit -m "feat(service/search): hidden-aware embedding completeness"
```

---

## Section O — HTTP routes

### Task O1 — `GET /api/v1/search`

**Files:**
- Create: `internal/httpapi/search.go`
- Create: `internal/httpapi/search_test.go`

**Step 1: Tests.**

```go
// internal/httpapi/search_test.go
func TestRoute_Search_HappyPathHybrid(t *testing.T) {
	srv := /* httptest backed by a service with a fixture */
	resp, err := srv.GET("/api/v1/search?q=small+dog+on+a+beach&sort=relevance")
	require.NoError(t, err)
	require.Equal(t, 200, resp.StatusCode)

	var body apiSearchResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.NotEmpty(t, body.Results)
	require.Equal(t, "relevance", body.EffectiveSort)
	require.False(t, body.SemanticUnavailable)
}

func TestRoute_Search_EffectiveSortCoercion(t *testing.T) {
	resp, _ := srv.GET("/api/v1/search?q=&sort=relevance")
	var body apiSearchResponse
	json.NewDecoder(resp.Body).Decode(&body)
	require.Equal(t, "newest", body.EffectiveSort)
}

func TestRoute_Search_EmbeddingFailureDegradesAndDoesNotError(t *testing.T) {
	// embed client returns ErrTransient → response is 200 with
	// semantic_unavailable=true, reason="query_embedding_failed".
}

func TestRoute_Search_IncludeHiddenWithoutUnlockReturns403(t *testing.T) { /* … */ }

func TestRoute_Search_CursorRoundTrip(t *testing.T) {
	// First request returns next_cursor; second request with that cursor
	// returns the next page; cursor with mismatched req hash returns 400.
}
```

**Step 2: Implement.**

```go
// internal/httpapi/search.go
package httpapi

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/wesm/fotobank/internal/errs"
	searchsvc "github.com/wesm/fotobank/internal/service/search"
)

func registerSearchRoutes(api huma.API, svc *searchsvc.Service) {
	if svc == nil { return }
	registerSearch(api, svc)
	registerSearchAutocompleteTags(api, svc)
	registerSearchAutocompleteLocations(api, svc)
}

type searchInput struct {
	Q             string `query:"q" doc:"free-text query"`
	Sort          string `query:"sort" enum:"relevance,newest,oldest" default:"relevance"`
	DateAfter     string `query:"date_after,omitempty"`
	DateBefore    string `query:"date_before,omitempty"`
	Tag           []string `query:"tag,omitempty"`
	Location      string `query:"location,omitempty"`
	MediaType     string `query:"media_type,omitempty" enum:"photo,video"`
	Limit         int    `query:"limit" default:"60"`
	Cursor        string `query:"cursor,omitempty"`
	IncludeHidden bool   `query:"include_hidden,omitempty"`
	Explain       bool   `query:"explain,omitempty"`
}

type searchOutput struct {
	Body searchBody
}

type searchBody struct {
	Results                  []searchResultDTO `json:"results"`
	NextCursor               *string           `json:"next_cursor"`
	HasMore                  bool              `json:"has_more"`
	Total                    *int              `json:"total,omitempty"`
	EffectiveSort            string            `json:"effective_sort"`
	EmbeddingCompleteness    float64           `json:"embedding_completeness"`
	SemanticUnavailable      bool              `json:"semantic_unavailable"`
	SemanticUnavailableReason string           `json:"semantic_unavailable_reason"`
}

type searchResultDTO struct {
	MediaID         string                  `json:"media_id"`
	MediaType       string                  `json:"media_type"`
	Timestamp       *time.Time              `json:"timestamp"`
	ImportedAt      time.Time               `json:"imported_at"`
	Width           *int                    `json:"width"`
	Height          *int                    `json:"height"`
	ThumbVersion    int                     `json:"thumb_version"`
	Score           float64                 `json:"score,omitempty"`
	ScoreComponents *scoreComponentsDTO     `json:"score_components,omitempty"`
}

type scoreComponentsDTO struct {
	RRF        float64  `json:"rrf"`
	BM25       *float64 `json:"bm25"`
	Vector     *float64 `json:"vector"`
	RankBM25   *int     `json:"rank_bm25"`
	RankVector *int     `json:"rank_vector"`
}

func registerSearch(api huma.API, svc *searchsvc.Service) {
	huma.Register(api, huma.Operation{
		OperationID: "search",
		Method:      http.MethodGet,
		Path:        "/api/v1/search",
		Summary:     "Hybrid (semantic + lexical) search across the caller's library",
	}, func(ctx context.Context, in *searchInput) (*searchOutput, error) {
		id, ok := IdentityFromContext(ctx)
		if !ok { return nil, huma.Error401Unauthorized(errs.ErrIdentityMissing.Error()) }
		req := /* translate searchInput to searchsvc.Request */
		resp, err := svc.Search(ctx, id.Principal.OwnersPrincipal(), req)
		if err != nil { return nil, Translate(err) }

		complete, err := svc.EmbeddingCompleteness(ctx, id.Principal.OwnersPrincipal(), in.IncludeHidden)
		if err != nil { return nil, Translate(err) }

		body := searchBody{
			Results: toSearchResultDTOs(resp.Hits, in.Explain),
			EffectiveSort: resp.EffectiveSort,
			EmbeddingCompleteness: complete,
			SemanticUnavailable: resp.SemanticUnavailable,
			SemanticUnavailableReason: resp.SemanticUnavailableReason,
		}
		if resp.NextCursor != "" { body.NextCursor = &resp.NextCursor }
		body.HasMore = resp.HasMore
		body.Total = resp.Total
		return &searchOutput{Body: body}, nil
	})
}
```

**Step 3: Wire into the route registrar.**

In `internal/httpapi/api.go` (or wherever `registerAIRoutes` is called from), add `registerSearchRoutes(api, deps.Search)`. Pass `deps.Search` from the server bootstrap.

**Step 4: Run.**

Run: `go test ./internal/httpapi/ -count=1 -run "TestRoute_Search"`
Run: `make api-generate`

Expected: PASS; `openapi.json` and `frontend/src/lib/api/generated/schema.ts` update.

**Step 5: Commit.**

```bash
git add internal/httpapi/search.go internal/httpapi/search_test.go internal/httpapi/api.go openapi.json frontend/src/lib/api/generated/schema.ts
git commit -m "feat(httpapi): GET /api/v1/search route"
```

---

> **Cleanup checkpoint 6.** Run `/roborev-fix` on the current branch. Address any findings before starting Task O2. Commit any cleanup as its own commit(s); do not amend.

---

### Task O2 — Autocomplete endpoints (tag + location)

**Files:**
- Modify: `internal/httpapi/search.go` — register the two autocomplete handlers
- Create: `internal/service/search/autocomplete.go`
- Create: `internal/service/search/autocomplete_test.go`
- Modify: `internal/httpapi/search_test.go` — coverage for both autocomplete routes

**Step 1: Tests.**

```go
func TestAutocompleteTags_PrefixMatchOwnerScopedHiddenAware(t *testing.T) {
	d := testutil.OpenTestDB(t)
	owner := seedOwner(t, d)
	other := seedOwner(t, d)
	mid := seedMedia(t, d, owner)
	otherMid := seedMedia(t, d, other)
	mustWriteActiveTags(t, d, mid, []string{"dog", "doggo", "doglike"})
	mustWriteActiveTags(t, d, otherMid, []string{"dog"}) // other-owner; must not appear

	svc := /* … */
	got, err := svc.AutocompleteTags(ctx, owner, "dog", 10, false)
	require.NoError(t, err)
	keys := pluck(got, func(t search.TagSuggestion) string { return t.Key })
	require.ElementsMatch(t, []string{"dog", "doggo", "doglike"}, keys)
}

func TestAutocompleteTags_HiddenWithoutUnlockExcluded(t *testing.T) {
	// Tag attached only to a hidden photo; without unlock, must not appear.
}

func TestAutocompleteTags_LIKEEscapesPercentAndUnderscore(t *testing.T) {
	// User types "100_%" (literal underscore + literal percent) — must
	// not match every tag.
	got, err := svc.AutocompleteTags(ctx, owner, `100_%`, 10, false)
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestAutocompleteLocations_SubstringMatch(t *testing.T) {
	mustWriteLocation(t, d, "Paris, France")
	mustWriteLocation(t, d, "Paris, Texas, USA")
	mustWriteLocation(t, d, "Berlin, Germany")
	got, _ := svc.AutocompleteLocations(ctx, owner, "Paris", 10, false)
	require.Len(t, got, 2)
}

func TestRoute_SearchAutocomplete(t *testing.T) {
	// HTTP-level test; both endpoints return 200 with structured JSON.
}
```

**Step 2: Service implementation.**

```go
// internal/service/search/autocomplete.go
package search

type TagSuggestion struct { Key, Label string; Count int }
type LocationSuggestion struct { Label string; Count int }

func (s *Service) AutocompleteTags(ctx context.Context, caller owners.Principal, prefix string, limit int, includeHidden bool) ([]TagSuggestion, error) {
	if includeHidden && !s.unlockClaimValid(ctx, caller) { return nil, errs.ErrPermissionDenied }
	esc := likeEscape(prefix) + "%"
	const q = `
SELECT mt.tag_key, mt.tag_label, COUNT(*) AS cnt
FROM media_tags mt
JOIN ai_results r ON mt.result_id = r.id
JOIN media m ON m.id = r.media_id
WHERE r.task = 'tag' AND r.status = 'active'
  AND m.owner_hub = ? AND m.owner_user_id = ?
  AND (m.hidden_at IS NULL OR ?)
  AND mt.tag_label LIKE ? ESCAPE '\'
GROUP BY mt.tag_key
ORDER BY cnt DESC
LIMIT ?`
	rows, err := s.ro.QueryContext(ctx, q, caller.Hub, caller.UserID, includeHidden, esc, limit)
	if err != nil { return nil, fmt.Errorf("autocomplete tags: %w", err) }
	defer rows.Close()
	var out []TagSuggestion
	for rows.Next() {
		var t TagSuggestion
		if err := rows.Scan(&t.Key, &t.Label, &t.Count); err != nil { return nil, err }
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Service) AutocompleteLocations(ctx context.Context, caller owners.Principal, substring string, limit int, includeHidden bool) ([]LocationSuggestion, error) {
	if includeHidden && !s.unlockClaimValid(ctx, caller) { return nil, errs.ErrPermissionDenied }
	esc := "%" + likeEscape(substring) + "%"
	const q = `
SELECT m.location_label, COUNT(*) AS cnt
FROM media m
WHERE m.owner_hub = ? AND m.owner_user_id = ?
  AND (m.hidden_at IS NULL OR ?)
  AND m.location_label IS NOT NULL
  AND m.location_label LIKE ? ESCAPE '\'
GROUP BY m.location_label
ORDER BY cnt DESC
LIMIT ?`
	rows, err := s.ro.QueryContext(ctx, q, caller.Hub, caller.UserID, includeHidden, esc, limit)
	if err != nil { return nil, fmt.Errorf("autocomplete locations: %w", err) }
	defer rows.Close()
	var out []LocationSuggestion
	for rows.Next() {
		var l LocationSuggestion
		if err := rows.Scan(&l.Label, &l.Count); err != nil { return nil, err }
		out = append(out, l)
	}
	return out, rows.Err()
}

// likeEscape doubles % and _ and prefixes them with backslash; LIKE
// pattern uses ESCAPE '\'.
func likeEscape(s string) string {
	b := strings.Builder{}
	for _, r := range s {
		switch r {
		case '%', '_', '\\':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}
```

**Step 3: HTTP routes.**

```go
// internal/httpapi/search.go (append to registerSearchRoutes)
func registerSearchAutocompleteTags(api huma.API, svc *searchsvc.Service) {
	huma.Register(api, huma.Operation{
		OperationID: "search-autocomplete-tags",
		Method:      http.MethodGet,
		Path:        "/api/v1/search/autocomplete/tags",
		Summary:     "Autocomplete tags by prefix; owner-scoped, hidden-aware",
	}, func(ctx context.Context, in *autocompleteTagsInput) (*autocompleteTagsOutput, error) {
		id, ok := IdentityFromContext(ctx)
		if !ok { return nil, huma.Error401Unauthorized(errs.ErrIdentityMissing.Error()) }
		out, err := svc.AutocompleteTags(ctx, id.Principal.OwnersPrincipal(), in.Q, in.Limit, in.IncludeHidden)
		if err != nil { return nil, Translate(err) }
		return &autocompleteTagsOutput{Body: autocompleteTagsBody{Tags: toTagDTOs(out)}}, nil
	})
}
// (locations symmetric)
```

**Step 4: Run.**

Run: `go test ./internal/service/search/ ./internal/httpapi/ -count=1`
Run: `make api-generate`

Expected: PASS; OpenAPI updates.

**Step 5: Commit.**

```bash
git add internal/service/search/autocomplete.go internal/service/search/autocomplete_test.go internal/httpapi/search.go internal/httpapi/search_test.go openapi.json frontend/src/lib/api/generated/schema.ts
git commit -m "feat(httpapi): autocomplete endpoints for tags and locations"
```

---

## Section P — SSE events for embed task

### Task P1 — Add `ai.embed.*` event names + emit helpers

**Files:**
- Modify: `internal/httpapi/events.go` — add the five event-name constants
- Modify: `internal/httpapi/ai_events.go` — add emit helpers (`EmitAIEmbedCompleted`, `EmitAIEmbedFailed`, `EmitAIEmbedGenerationCreated`, `EmitAIEmbedGenerationActivated`, `EmitAIEmbedGenerationRetired`)
- Modify: `internal/ai/embedding/worker.go`, `activator.go`, `generations.go` — call the emit helpers at the right success/failure/state-transition points
- Modify: `internal/httpapi/ai_events_test.go` — coverage that each emit helper writes the expected SSE event name + payload

**Step 1: Add event-name constants.**

```go
// internal/httpapi/events.go (extend the existing const block)
const (
	EventNameAITagCompleted               = "ai.tag.completed"
	EventNameAICaptionCompleted           = "ai.caption.completed"
	EventNameAIHealthChanged              = "ai.health.changed"
	EventNameAIEmbedCompleted             = "ai.embed.completed"
	EventNameAIEmbedFailed                = "ai.embed.failed"
	EventNameAIEmbedGenerationCreated     = "ai.embed.generation_created"
	EventNameAIEmbedGenerationActivated   = "ai.embed.generation_activated"
	EventNameAIEmbedGenerationRetired     = "ai.embed.generation_retired"
)
```

**Step 2: Add emit helpers.**

```go
// internal/httpapi/ai_events.go (append)
type AIEmbedCompletedEvent struct {
	MediaID     string `json:"media_id"`
	Fingerprint string `json:"fingerprint"`
}
type AIEmbedFailedEvent struct {
	MediaID     string `json:"media_id"`
	Fingerprint string `json:"fingerprint"`
	ErrorKind   string `json:"error_kind"`
}
type AIEmbedGenerationEvent struct {
	ID          int64  `json:"id"`
	Fingerprint string `json:"fingerprint"`
}

func (b *EventBus) EmitAIEmbedCompleted(p owners.Principal, ev AIEmbedCompletedEvent) { /* json.Marshal, PublishAutoID */ }
func (b *EventBus) EmitAIEmbedFailed(p owners.Principal, ev AIEmbedFailedEvent) { /* … */ }
func (b *EventBus) EmitAIEmbedGenerationCreated(p owners.Principal, ev AIEmbedGenerationEvent) { /* … */ }
func (b *EventBus) EmitAIEmbedGenerationActivated(p owners.Principal, ev AIEmbedGenerationEvent) { /* … */ }
func (b *EventBus) EmitAIEmbedGenerationRetired(p owners.Principal, ev AIEmbedGenerationEvent) { /* … */ }
```

**Step 3: Wire emits into the worker, generations repo, and activator.**

- `worker.go` per-job success → `EmitAIEmbedCompleted`. Per-job failure (after retry budget) → `EmitAIEmbedFailed`.
- `generations.go::FindOrCreateBuilding` (when it does the INSERT) → `EmitAIEmbedGenerationCreated`. The emit fires on the principal whose ownership scope drives the search; in single-principal v1 that's the configured stub principal. Capture the principal as a constructor field or per-call argument.
- `activator.go::Tick` → `EmitAIEmbedGenerationActivated` after successful promotion.
- `generations.go::Promote` → `EmitAIEmbedGenerationRetired` for the previously-active generation.

**Step 4: Run.**

Run: `go test ./internal/ai/embedding/ ./internal/httpapi/ -count=1`

Expected: PASS.

**Step 5: Commit.**

```bash
git add internal/httpapi/events.go internal/httpapi/ai_events.go internal/httpapi/ai_events_test.go internal/ai/embedding/
git commit -m "feat(httpapi): ai.embed.* SSE events"
```

---

## Section Q — CLI extensions

### Task Q1 — Extend `fotobank ai` with embed-task and generation-lifecycle subcommands

**Files:**
- Modify: `internal/cli/ai.go`
- Modify: `internal/cli/ai_test.go`

**Step 1: Tests.**

```go
func TestCLIAI_BackfillEmbed(t *testing.T) {
	// fotobank ai backfill --task=embed → calls scanner.ScanEmbed →
	// returns "<n> embed jobs enqueued"
}

func TestCLIAI_RetryFailedEmbed(t *testing.T) { /* … */ }

func TestCLIAI_ListGenerations(t *testing.T) {
	// fotobank ai list-generations [--state active|building|retired] →
	// JSON output with id, fingerprint, model_id, input_profile, state,
	// dimension, embedded_count, eligible_count, created_at, activated_at, retired_at.
}

func TestCLIAI_PromoteGeneration_AdminOverride(t *testing.T) {
	// Retire current active, promote a retired generation back.
}

func TestCLIAI_CompactRetiredGenerationsDryRun(t *testing.T) {
	// Output lists candidates without dropping anything.
}
```

**Step 2: Add subcommands.**

In `internal/cli/ai.go`, locate the existing `aiCmd` parent (cobra command). Add:

```go
var listGenerationsCmd = &cobra.Command{
	Use:   "list-generations",
	Short: "List embedding generations",
	RunE: func(cmd *cobra.Command, args []string) error {
		state, _ := cmd.Flags().GetString("state")
		gens, _ := wireGenerations(cmd) // helper that wires DB + repo
		rows, err := gens.List(cmd.Context(), state)
		if err != nil { return err }
		// also compute eligible_count per row
		json.NewEncoder(cmd.OutOrStdout()).Encode(toJSON(rows))
		return nil
	},
}

var promoteGenerationCmd = &cobra.Command{
	Use:   "promote-generation [generation-id]",
	Short: "Admin override: promote a retired generation back to active",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// warn if older than retain_retired_days
		// confirm-flag-or-tty-prompt before proceeding
		id, err := strconv.ParseInt(args[0], 10, 64)
		if err != nil { return fmt.Errorf("generation-id: %w", err) }
		gens, _ := wireGenerations(cmd)
		return gens.Promote(cmd.Context(), id)
	},
}

var compactRetiredGenerationsCmd = &cobra.Command{
	Use:   "compact-retired-generations",
	Short: "Drop retired generations older than [search] retain_retired_days",
	RunE: func(cmd *cobra.Command, args []string) error {
		dry, _ := cmd.Flags().GetBool("dry-run")
		comp, window := wireCompactor(cmd)
		if dry {
			return printCandidates(cmd.OutOrStdout(), comp, window)
		}
		dropped, err := comp.SweepOnce(cmd.Context())
		if err != nil { return err }
		fmt.Fprintf(cmd.OutOrStdout(), "%d retired generations dropped\n", dropped)
		return nil
	},
}
```

Extend `backfill` and `retry-failed` subcommands' `--task` validators to accept `"embed"`. Wire the embed gap-scan path through `gapscanner.ScanEmbed` for `--task=embed`.

**Step 3: Run.**

Run: `go test ./internal/cli/ -count=1 -run "TestCLIAI_"`

Expected: PASS.

**Step 4: Commit.**

```bash
git add internal/cli/ai.go internal/cli/ai_test.go
git commit -m "feat(cli/ai): embed-task subcommands + generation lifecycle"
```

---

## Section R — Metrics

### Task R1 — Embed metric labels + new search metrics

**Files:**
- Modify: `internal/obs/metrics.go`
- Modify: `internal/obs/metrics_test.go`

**Step 1: Tests.**

```go
func TestMetrics_AIJobsDepthEmbedLabel(t *testing.T) {
	obs.AIJobsDepth.WithLabelValues("embed", "pending").Set(7)
	require.Equal(t, 7.0, gather(obs.AIJobsDepth, "embed", "pending"))
}

func TestMetrics_SearchRequestsTotal(t *testing.T) {
	obs.SearchRequestsTotal.WithLabelValues("hybrid", "relevance").Inc()
	require.Equal(t, 1.0, gather(obs.SearchRequestsTotal, "hybrid", "relevance"))
}
```

**Step 2: Add metrics.**

Existing AI metrics extend with `task="embed"` (no new declarations needed; the label set already accepts arbitrary string values). New metrics:

```go
// internal/obs/metrics.go (append)
var (
	AIEmbedBatchSize = metrics.NewHistogram("fotobank_ai_embed_batch_size")
	AIEmbeddingGenerations = metrics.NewLabelGauge("fotobank_ai_embedding_generations", "state")
	AIEmbeddingCount = metrics.NewLabelGauge("fotobank_ai_embedding_count", "state")
	SearchRequestsTotal = metrics.NewLabelCounter("fotobank_search_requests_total", "mode", "sort")
	SearchLatencySeconds = metrics.NewLabelHistogram("fotobank_search_latency_seconds", "mode")
	SearchPoolSaturatedTotal = metrics.NewCounter("fotobank_search_pool_saturated_total")
)
```

(Wrap whichever metric primitives the existing observability layer exposes — the spec calls out `github.com/VictoriaMetrics/metrics`. If the existing helpers use a thin abstraction, follow that; the names above are illustrative.)

**Step 3: Wire emits.**

- Worker batch send → `AIEmbedBatchSize.Update(float64(len(batch)))`.
- Activator promotion → recompute and `AIEmbeddingGenerations.Set` for each state.
- Search route handler → `SearchRequestsTotal.Inc("hybrid"|"bm25_only"|"filter_only", effective_sort)` and `SearchLatencySeconds.Observe(elapsed)`.
- `KPerSignal` cap hit (engine side) → `SearchPoolSaturatedTotal.Inc()`.

**Step 4: Run.**

Run: `go test ./internal/obs/ -count=1`

Expected: PASS.

**Step 5: Commit.**

```bash
git add internal/obs/metrics.go internal/obs/metrics_test.go internal/ai/embedding/ internal/httpapi/search.go
git commit -m "feat(obs): embed task labels + search-specific metrics"
```

---

## Section S — Server wiring

### Task S1 — Wire embed worker, activator, compactor, gap-scan tick into `cmd fotobank server`

**Files:**
- Modify: `internal/cli/server.go`
- Modify: `internal/cli/server_test.go`

**Step 1: Test — server boot starts the embed worker, activator, compactor, gap-scan tick exactly when `[ai.embed].enabled=true`.**

```go
func TestServer_StartsEmbedSubsystemWhenEnabled(t *testing.T) {
	// Construct a minimal server with [ai.embed].enabled=true; assert
	// that after a few hundred ms, the worker has run at least one
	// RunOnce, the activator has ticked, and the compactor has been
	// scheduled. Use a fake clock or polling assertion.
}

func TestServer_LeavesEmbedSubsystemDormantWhenDisabled(t *testing.T) { /* … */ }
```

**Step 2: Wire.**

In `internal/cli/server.go`, locate the existing block that constructs the AI tag and caption workers. After it, behind `if cfg.AI.Embed.Enabled`:

```go
// Probe — already added in D2.
embedClient := embedding.NewClient(/* … */)
gens := embedding.NewGenerations(d.WriteDB(), d.ReadDB())
mapping := embedding.NewMapping(d.WriteDB())
embedW := embedding.NewWorker(embedding.WorkerDeps{
	Q: q, Gens: gens, Mapping: mapping, Client: embedClient,
	Resolver: resolver, Cfg: cfg.AI.Embed, Events: events,
})
go func() { _ = embedW.Run(ctx) }()

activator := embedding.NewActivator(/* … */)
go func() { _ = activator.Run(ctx) }()

compactor := embedding.NewCompactor(d.WriteDB(), time.Duration(cfg.Search.RetainRetiredDays)*24*time.Hour)
go runEvery(ctx, 24*time.Hour, func(ctx context.Context) { _, _ = compactor.SweepOnce(ctx) })

// Embed gap-scan tick alongside the existing tag/caption tick.
go runEvery(ctx, 1*time.Minute, func(ctx context.Context) {
	gen, _ := gens.FindActiveOrBuilding(ctx)
	if gen == nil { return }
	scanner.ScanEmbed(ctx, gapscanner.EmbedScanRequest{
		Owner: cfg.Identity.StubPrincipal(),
		Generation: *gen,
		Fingerprint: embedding.Fingerprint(cfg.AI.Embed),
		AckAllowsHidden: false, // single-principal v1
		RetryBudget: cfg.AI.Embed.MaxRetries + 1,
		Limit: 1000,
	})
})

// Search subsystem.
backend := index.NewSQLiteVecBackend(d.ReadDB(), /* will be re-resolved per-request via gens.FindActive */)
engine := hybrid.NewEngine(backend, embedClient, gens, cfg.Search)
searchSvc := searchsvc.New(/* … */)
deps.Search = searchSvc
```

(`Backend.NewSQLiteVecBackend` may need to be active-generation-aware per request — see L1's design. The exact surface of `Engine` re-resolving the active generation per request is an implementation detail; the engine + backend split must support it.)

**Step 3: Run.**

Run: `go test ./internal/cli/ -count=1 -run "TestServer_"`

Expected: PASS.

**Step 4: Commit.**

```bash
git add internal/cli/server.go internal/cli/server_test.go
git commit -m "feat(cli/server): wire embed subsystem and search service"
```

---

> **Cleanup checkpoint 7.** Run `/roborev-fix` on the current branch. Address any findings before starting Task T1. Commit any cleanup as its own commit(s); do not amend.

---

## Section T — Frontend search route

### Task T1 — Search types + reactive search store

**Files:**
- Create: `frontend/src/lib/search/types.ts`
- Create: `frontend/src/lib/search/searchStore.svelte.ts`
- Create: `frontend/src/lib/search/client.ts`
- Create: `frontend/src/lib/search/searchStore.test.ts`

**Step 1: Tests — store transitions on query change, on cursor pagination, on SSE invalidation.**

```ts
// frontend/src/lib/search/searchStore.test.ts
test("setQuery resets cursor and re-fetches", async () => {
  const store = createSearchStore({ client: mockClient(...) });
  await store.setQuery("dogs");
  expect(store.results.length).toBeGreaterThan(0);
  expect(store.cursor).toBe(null); // first page
  await store.fetchNextPage();
  expect(store.results.length).toBeGreaterThan(initialLen);
});

test("SSE ai.embed.generation_activated invalidates requestHash", async () => {
  const store = createSearchStore({ client: mockClient(...) });
  await store.setQuery("dogs");
  const beforeHash = store.requestHash;
  store.onGenerationActivated();
  expect(store.requestHash).toBe(null); // cleared, next interaction restarts
});

test("cursor mismatch (server returns 400) restarts pagination from null", async () => { /* … */ });
```

**Step 2: Types.**

```ts
// frontend/src/lib/search/types.ts
export type SearchSort = "relevance" | "newest" | "oldest";

export interface SearchFilters {
  dateAfter?: string;
  dateBefore?: string;
  tags: { tag_key: string; tag_label: string }[];
  location?: { location_label: string };
  mediaType?: "photo" | "video";
}

export interface SearchResult {
  media_id: string;
  media_type: "photo" | "video";
  timestamp: string | null;
  imported_at: string;
  width: number | null;
  height: number | null;
  thumb_version: number;
  score?: number;
  score_components?: {
    rrf: number;
    bm25: number | null;
    vector: number | null;
    rank_bm25: number | null;
    rank_vector: number | null;
  };
}

export interface SearchResponse {
  results: SearchResult[];
  next_cursor: string | null;
  has_more: boolean;
  total?: number;
  effective_sort: SearchSort;
  embedding_completeness: number;
  semantic_unavailable: boolean;
  semantic_unavailable_reason: "" | "no_active_generation" | "query_embedding_failed";
}
```

**Step 3: Store.**

```ts
// frontend/src/lib/search/searchStore.svelte.ts
import type { SearchFilters, SearchResponse, SearchResult, SearchSort } from "./types";
import { searchClient } from "./client";

export interface SearchStore {
  // bound fields
  query: string;
  filters: SearchFilters;
  sort: SearchSort;
  // server-derived
  results: SearchResult[];
  cursor: string | null;
  hasMore: boolean;
  total: number | null;
  embeddingCompleteness: number;
  semanticUnavailable: boolean;
  semanticUnavailableReason: SearchResponse["semantic_unavailable_reason"];
  effectiveSort: SearchSort;
  requestHash: string | null;
  // mutations
  setQuery(q: string): Promise<void>;
  setFilters(f: SearchFilters): Promise<void>;
  setSort(s: SearchSort): Promise<void>;
  fetchNextPage(): Promise<void>;
  onGenerationActivated(): void;
}

export function createSearchStore(): SearchStore {
  let query = $state("");
  let filters = $state<SearchFilters>({ tags: [] });
  let sort = $state<SearchSort>("relevance");
  let results = $state<SearchResult[]>([]);
  let cursor = $state<string | null>(null);
  let hasMore = $state(false);
  let total = $state<number | null>(null);
  let embeddingCompleteness = $state(0);
  let semanticUnavailable = $state(false);
  let semanticUnavailableReason = $state<SearchResponse["semantic_unavailable_reason"]>("");
  let effectiveSort = $state<SearchSort>("relevance");
  let requestHash = $state<string | null>(null);

  let inflight: AbortController | null = null;

  async function fetchPage(restart: boolean): Promise<void> {
    inflight?.abort();
    const ctl = new AbortController();
    inflight = ctl;
    try {
      const resp = await searchClient.search({
        q: query, sort, ...filtersToQuery(filters),
        cursor: restart ? null : cursor,
      }, ctl.signal);
      if (restart) results = resp.results;
      else results = [...results, ...resp.results];
      cursor = resp.next_cursor;
      hasMore = resp.has_more;
      total = resp.total ?? null;
      embeddingCompleteness = resp.embedding_completeness;
      semanticUnavailable = resp.semantic_unavailable;
      semanticUnavailableReason = resp.semantic_unavailable_reason;
      effectiveSort = resp.effective_sort;
      requestHash = parseRequestHash(resp.next_cursor);
    } catch (e) {
      if ((e as Error).name === "AbortError") return;
      // 400 cursor mismatch → restart from null
      if (isCursorMismatch(e)) { cursor = null; await fetchPage(true); return; }
      throw e;
    }
  }

  return {
    get query() { return query; }, set query(v) { query = v; },
    get filters() { return filters; }, set filters(v) { filters = v; },
    get sort() { return sort; }, set sort(v) { sort = v; },
    get results() { return results; },
    get cursor() { return cursor; },
    get hasMore() { return hasMore; },
    get total() { return total; },
    get embeddingCompleteness() { return embeddingCompleteness; },
    get semanticUnavailable() { return semanticUnavailable; },
    get semanticUnavailableReason() { return semanticUnavailableReason; },
    get effectiveSort() { return effectiveSort; },
    get requestHash() { return requestHash; },

    async setQuery(q: string) { query = q; await fetchPage(true); },
    async setFilters(f: SearchFilters) { filters = f; await fetchPage(true); },
    async setSort(s: SearchSort) { sort = s; await fetchPage(true); },
    async fetchNextPage() { if (!hasMore || !cursor) return; await fetchPage(false); },
    onGenerationActivated() { requestHash = null; /* next user interaction restarts */ },
  };
}
```

**Step 4: Client.**

```ts
// frontend/src/lib/search/client.ts
import { client } from "$lib/api/client";

export const searchClient = {
  async search(params: SearchQuery, signal?: AbortSignal) { /* GET /api/v1/search */ },
  async autocompleteTags(prefix: string) { /* GET /api/v1/search/autocomplete/tags */ },
  async autocompleteLocations(substring: string) { /* GET /api/v1/search/autocomplete/locations */ },
};
```

**Step 5: Run.**

Run: `cd frontend && bun run test src/lib/search/searchStore.test.ts`

Expected: PASS.

**Step 6: Commit.**

```bash
git add frontend/src/lib/search/
git commit -m "feat(frontend/search): types, store, client"
```

---

### Task T2 — `/search` route + `Search.svelte` page

**Files:**
- Modify: `frontend/src/lib/router/router.svelte.ts` — add `{ route: "search"; q?: string; ... }` and pattern + query-param parsing
- Modify: `frontend/src/lib/router/router_test.ts` — coverage for the new pattern
- Create: `frontend/src/routes/Search.svelte`
- Create: `frontend/src/routes/Search.test.ts`

**Step 1: Add the router match.**

```ts
// frontend/src/lib/router/router.svelte.ts (RouteMatch union)
| { route: "search"; q?: string; sort?: SearchSort;
    date_after?: string; date_before?: string;
    tag?: string[]; location?: string; media_type?: "photo" | "video";
    include_hidden?: boolean }

// PATTERNS array (insert near top):
{ re: /^\/search\/?$/, build: () => {
  const sp = new URLSearchParams(window.location.search);
  const tags = sp.getAll("tag");
  return {
    route: "search" as const,
    ...(sp.get("q") ? { q: sp.get("q")! } : {}),
    ...(sp.get("sort") ? { sort: sp.get("sort") as SearchSort } : {}),
    ...(sp.get("date_after") ? { date_after: sp.get("date_after")! } : {}),
    ...(sp.get("date_before") ? { date_before: sp.get("date_before")! } : {}),
    ...(tags.length ? { tag: tags } : {}),
    ...(sp.get("location") ? { location: sp.get("location")! } : {}),
    ...(sp.get("media_type") ? { media_type: sp.get("media_type") as "photo"|"video" } : {}),
    ...(sp.get("include_hidden") === "true" ? { include_hidden: true } : {}),
  };
} }
```

**Step 2: `Search.svelte` page.**

```svelte
<!-- frontend/src/routes/Search.svelte -->
<script lang="ts">
  import { onMount } from "svelte";
  import { createSearchStore } from "$lib/search/searchStore.svelte";
  import SearchFiltersPopover from "$lib/search/SearchFiltersPopover.svelte";
  import SearchFilterChips from "$lib/search/SearchFilterChips.svelte";
  import SearchSortSegment from "$lib/search/SearchSortSegment.svelte";
  import IndexingStatusPill from "$lib/search/IndexingStatusPill.svelte";
  import IndexingStatusBanner from "$lib/search/IndexingStatusBanner.svelte";
  import VirtualizedGrid from "$lib/grid/VirtualizedGrid.svelte"; // reuse existing
  import { router } from "$lib/router/router.svelte";

  const store = createSearchStore();

  onMount(() => {
    const m = router.match();
    if (m.route !== "search") return;
    store.query = m.q ?? "";
    store.sort = m.sort ?? "relevance";
    store.filters = filtersFromMatch(m);
    void store.setQuery(store.query);
  });

  function onScrollNearBottom() { void store.fetchNextPage(); }
</script>

<div class="search-page">
  <div class="search-toolbar">
    <SearchFiltersPopover bind:filters={store.filters} on:change={() => store.setFilters(store.filters)} />
    <SearchSortSegment bind:sort={store.sort} on:change={() => store.setSort(store.sort)} />
    <IndexingStatusPill completeness={store.embeddingCompleteness} />
  </div>
  <SearchFilterChips bind:filters={store.filters} on:change={() => store.setFilters(store.filters)} />
  <IndexingStatusBanner
    completeness={store.embeddingCompleteness}
    semanticUnavailable={store.semanticUnavailable}
    reason={store.semanticUnavailableReason}
    hasQuery={store.query !== ""} />
  <VirtualizedGrid items={store.results} on:nearbottom={onScrollNearBottom} />
</div>
```

**Step 3: Tests.**

```ts
// frontend/src/routes/Search.test.ts
test("renders empty state when no query", () => { /* … */ });
test("triggers fetchNextPage on near-bottom event", () => { /* … */ });
```

**Step 4: Run.**

Run: `cd frontend && bun run test src/routes/Search.test.ts src/lib/router/router_test.ts`
Run: `cd frontend && bun run typecheck`

Expected: PASS.

**Step 5: Commit.**

```bash
git add frontend/src/lib/router/router.svelte.ts frontend/src/lib/router/router_test.ts frontend/src/routes/Search.svelte frontend/src/routes/Search.test.ts
git commit -m "feat(frontend/search): /search route and page shell"
```

---

### Task T3 — AppHeader debounced search input integration

**Files:**
- Modify: `frontend/src/lib/components/AppHeader.svelte`
- Modify: `frontend/src/lib/components/AppHeader.test.ts`

**Step 1: Tests.**

```ts
test("debounces input by ~300ms", async () => {
  const onChange = vi.fn();
  /* mount AppHeader with mocked router */
  type("d");
  await wait(50); type("o");
  await wait(50); type("g");
  expect(onChange).not.toHaveBeenCalled(); // no debounce yet
  await wait(310);
  expect(onChange).toHaveBeenCalledTimes(1);
  expect(onChange).toHaveBeenCalledWith("dog");
});

test("Enter flushes immediately and pushes a real history entry", async () => { /* … */ });

test("typing while on /search uses replace-state navigation", async () => { /* … */ });

test("⌘K focuses the input", async () => { /* … */ });
```

**Step 2: Wire.**

In `AppHeader.svelte`'s search input, add:

```svelte
<script lang="ts">
  import { router } from "$lib/router/router.svelte";

  let value = $state("");
  let timer: number | undefined;
  const DEBOUNCE_MS = 300;

  function onInput(ev: Event) {
    value = (ev.target as HTMLInputElement).value;
    clearTimeout(timer);
    timer = window.setTimeout(commit, DEBOUNCE_MS);
  }

  function onKeyDown(ev: KeyboardEvent) {
    if (ev.key === "Enter") {
      ev.preventDefault();
      clearTimeout(timer);
      commit({ pushHistory: true });
    }
  }

  function commit({ pushHistory = false } = {}) {
    const params = new URLSearchParams(window.location.search);
    if (value) params.set("q", value); else params.delete("q");
    const target = `/search?${params.toString()}`;
    if (window.location.pathname === "/search" && !pushHistory) {
      router.navigate(target, { replace: true });
    } else {
      router.navigate(target);
    }
  }

  // ⌘K already focuses this input today via the existing handler; leave that wiring alone.
</script>
<input type="search" value={value} oninput={onInput} onkeydown={onKeyDown} placeholder="Search" />
```

**Step 3: Run.**

Run: `cd frontend && bun run test src/lib/components/AppHeader.test.ts`

Expected: PASS.

**Step 4: Commit.**

```bash
git add frontend/src/lib/components/AppHeader.svelte frontend/src/lib/components/AppHeader.test.ts
git commit -m "feat(frontend/header): debounced search input with replace-history"
```

---

## Section U — Filters and sort

### Task U1 — `SearchFiltersPopover` + tag/location autocomplete

**Files:**
- Create: `frontend/src/lib/search/SearchFiltersPopover.svelte`
- Create: `frontend/src/lib/search/SearchFiltersPopover.test.ts`
- Create: `frontend/src/lib/search/SearchFilterChips.svelte`
- Create: `frontend/src/lib/search/SearchFilterChips.test.ts`

**Step 1: Tests — popover controls commit chips.**

```ts
test("date range chips render half-open semantics", async () => { /* … */ });
test("tag autocomplete commits a chip carrying tag_key", async () => { /* … */ });
test("location autocomplete commits a chip carrying location_label", async () => { /* … */ });
test("media-type segmented control sets photo|video|null", async () => { /* … */ });
test("clicking a chip reopens the popover with values pre-filled", async () => { /* … */ });
test("removing a chip dispatches change with the chip absent", async () => { /* … */ });
```

**Step 2: Implement.**

```svelte
<!-- frontend/src/lib/search/SearchFiltersPopover.svelte -->
<script lang="ts">
  import type { SearchFilters } from "./types";
  import { searchClient } from "./client";

  let { filters = $bindable() }: { filters: SearchFilters } = $props();

  // Date range
  let dateAfter = $state(filters.dateAfter ?? "");
  let dateBefore = $state(filters.dateBefore ?? "");

  // Tag autocomplete
  let tagInput = $state("");
  let tagSuggestions = $state<{tag_key: string; tag_label: string; count: number}[]>([]);
  $effect(() => {
    if (tagInput.length < 1) { tagSuggestions = []; return; }
    void searchClient.autocompleteTags(tagInput).then(s => tagSuggestions = s.tags);
  });

  // Location autocomplete (substring)
  let locationInput = $state("");
  let locationSuggestions = $state<{location_label: string; count: number}[]>([]);
  $effect(() => {
    if (locationInput.length < 1) { locationSuggestions = []; return; }
    void searchClient.autocompleteLocations(locationInput).then(s => locationSuggestions = s.locations);
  });

  function addTag(s: typeof tagSuggestions[0]) {
    filters = { ...filters, tags: [...filters.tags, { tag_key: s.tag_key, tag_label: s.tag_label }] };
    tagInput = "";
  }
  function setLocation(s: typeof locationSuggestions[0]) {
    filters = { ...filters, location: { location_label: s.location_label } };
    locationInput = "";
  }
  // Date and media-type setters similar.
</script>

<div class="filters-popover">
  <!-- date range, tag input + suggestion list, location input + suggestion list, media-type segment -->
</div>
```

```svelte
<!-- SearchFilterChips.svelte: removable chips below the popover -->
```

**Step 3: Run.**

Run: `cd frontend && bun run test src/lib/search/SearchFiltersPopover.test.ts src/lib/search/SearchFilterChips.test.ts`

Expected: PASS.

**Step 4: Commit.**

```bash
git add frontend/src/lib/search/SearchFiltersPopover.svelte frontend/src/lib/search/SearchFiltersPopover.test.ts frontend/src/lib/search/SearchFilterChips.svelte frontend/src/lib/search/SearchFilterChips.test.ts
git commit -m "feat(frontend/search): filter popover with autocomplete + chips"
```

---

### Task U2 — Sort segment + URL param sync

**Files:**
- Create: `frontend/src/lib/search/SearchSortSegment.svelte`
- Create: `frontend/src/lib/search/SearchSortSegment.test.ts`
- Modify: `frontend/src/routes/Search.svelte` — sync URL params on store mutations (filters, sort, query)

**Step 1: Tests.**

```ts
test("default sort is Relevance when q != ''", () => { /* … */ });
test("default sort is Newest when q == ''", () => { /* … */ });
test("changing sort triggers store.setSort", async () => { /* … */ });
```

**Step 2: Implement.**

```svelte
<!-- frontend/src/lib/search/SearchSortSegment.svelte -->
<script lang="ts">
  import type { SearchSort } from "./types";
  let { sort = $bindable() }: { sort: SearchSort } = $props();
</script>
<div class="segmented" role="radiogroup" aria-label="Sort">
  <button class:selected={sort === "relevance"} onclick={() => sort = "relevance"}>Relevance</button>
  <button class:selected={sort === "newest"} onclick={() => sort = "newest"}>Newest</button>
  <button class:selected={sort === "oldest"} onclick={() => sort = "oldest"}>Oldest</button>
</div>
```

**Step 3: URL param sync.**

In `Search.svelte`, on every store mutation, update the URL params via `router.navigate(..., { replace: true })` so the URL is the source of truth:

```ts
$effect(() => {
  const params = new URLSearchParams();
  if (store.query) params.set("q", store.query);
  if (store.sort !== "relevance") params.set("sort", store.sort);
  if (store.filters.dateAfter) params.set("date_after", store.filters.dateAfter);
  if (store.filters.dateBefore) params.set("date_before", store.filters.dateBefore);
  for (const t of store.filters.tags) params.append("tag", t.tag_key);
  if (store.filters.location) params.set("location", store.filters.location.location_label);
  if (store.filters.mediaType) params.set("media_type", store.filters.mediaType);
  router.navigate(`/search?${params.toString()}`, { replace: true });
});
```

**Step 4: Run.**

Run: `cd frontend && bun run test src/lib/search/SearchSortSegment.test.ts && bun run typecheck`

Expected: PASS.

**Step 5: Commit.**

```bash
git add frontend/src/lib/search/SearchSortSegment.svelte frontend/src/lib/search/SearchSortSegment.test.ts frontend/src/routes/Search.svelte
git commit -m "feat(frontend/search): sort segment and URL param sync"
```

---

> **Cleanup checkpoint 8.** Run `/roborev-fix` on the current branch. Address any findings before starting Task V1. Commit any cleanup as its own commit(s); do not amend.

---

## Section V — Indexing status surfaces

### Task V1 — Pill + banner components

**Files:**
- Create: `frontend/src/lib/search/IndexingStatusPill.svelte`
- Create: `frontend/src/lib/search/IndexingStatusPill.test.ts`
- Create: `frontend/src/lib/search/IndexingStatusBanner.svelte`
- Create: `frontend/src/lib/search/IndexingStatusBanner.test.ts`

**Step 1: Tests.**

```ts
// IndexingStatusPill.test.ts
test("hides when completeness == 1", () => { /* … */ });
test("renders 'X / Y indexed' when completeness < 1", () => {
  const { getByText } = render(Pill, { completeness: 0.97, embedded: 2879, eligible: 2981 });
  expect(getByText(/2,879 \/ 2,981 indexed/)).toBeTruthy();
});

// IndexingStatusBanner.test.ts
test("under-80% banner shows when q != '' && completeness < 0.80 && !semanticUnavailable", () => { /* … */ });
test("no_active_generation banner is one-time-dismissable per session", () => { /* … */ });
test("query_embedding_failed banner auto-dismisses on next successful query", () => { /* … */ });
```

**Step 2: Implement.**

```svelte
<!-- IndexingStatusPill.svelte -->
<script lang="ts">
  let { completeness, embedded, eligible }: { completeness: number; embedded?: number; eligible?: number } = $props();
  const visible = $derived(completeness < 1.0);
  const label = $derived(
    embedded != null && eligible != null
      ? `${embedded.toLocaleString()} / ${eligible.toLocaleString()} indexed`
      : `${Math.round(completeness * 100)}% indexed`,
  );
</script>
{#if visible}<span class="pill">{label}</span>{/if}
```

```svelte
<!-- IndexingStatusBanner.svelte -->
<script lang="ts">
  let { completeness, semanticUnavailable, reason, hasQuery }: {
    completeness: number;
    semanticUnavailable: boolean;
    reason: "" | "no_active_generation" | "query_embedding_failed";
    hasQuery: boolean;
  } = $props();

  let dismissed = $state(false);

  const showUnderEighty = $derived(hasQuery && completeness < 0.80 && !semanticUnavailable);
  const showNoGen = $derived(reason === "no_active_generation" && !dismissed);
  const showQueryEmbedFailed = $derived(reason === "query_embedding_failed");
</script>
{#if showUnderEighty}
  <div class="banner">Search is still indexing — semantic ranking covers {Math.round(completeness*100)}% of your library so far. Lexical results below.</div>
{:else if showNoGen}
  <div class="banner">Semantic search is not yet available — your library is still being indexed for the first time. <button onclick={() => dismissed = true}>Dismiss</button></div>
{:else if showQueryEmbedFailed}
  <div class="banner">Semantic ranking is temporarily unavailable. Showing lexical results.</div>
{/if}
```

**Step 3: Subscribe the search store to SSE invalidation.**

In `Search.svelte` (or a higher-level events bus that already exists), add a handler for `EventNameAIEmbedGenerationActivated`:

```ts
import { eventsBus } from "$lib/events";

onMount(() => {
  const off = eventsBus.on("ai.embed.generation_activated", () => {
    store.onGenerationActivated();
  });
  return () => off();
});
```

**Step 4: Run.**

Run: `cd frontend && bun run test src/lib/search/IndexingStatus*.test.ts`

Expected: PASS.

**Step 5: Commit.**

```bash
git add frontend/src/lib/search/IndexingStatusPill.svelte frontend/src/lib/search/IndexingStatusPill.test.ts frontend/src/lib/search/IndexingStatusBanner.svelte frontend/src/lib/search/IndexingStatusBanner.test.ts frontend/src/routes/Search.svelte
git commit -m "feat(frontend/search): indexing-status pill + banner"
```

---

### Task V2 — Diagnostics mode

**Files:**
- Modify: `frontend/src/routes/SettingsAI.svelte` — add the "AI Inspection" toggle, persisted via `user_settings`
- Create: `frontend/src/lib/search/DiagnosticsBadge.svelte`
- Modify: `frontend/src/lib/lightbox/components/LightboxMetadata.svelte` — add a "Search relevance" info row when `explain=true`
- Modify: `internal/service/usersettings/...` and the user-settings HTTP route — add a key `ai.inspection` (default false)

**Step 1: Tests.**

```ts
test("toggle in SettingsAI persists user_settings ai.inspection=true", async () => { /* … */ });

test("DiagnosticsBadge hover renders RRF score", async () => {
  const { container } = render(DiagnosticsBadge, { score: 0.0156, components: { rrf: 0.0156, bm25: 8.42, vector: 0.81, rank_bm25: 3, rank_vector: 7 } });
  expect(getByText(container, /0\.0156/)).toBeTruthy();
});

test("LightboxMetadata renders Search relevance row when result has score_components", async () => { /* … */ });
```

**Step 2: Add the user-settings key.**

In `internal/service/usersettings/...`, add a typed accessor for `ai.inspection` (boolean). Update the HTTP user-settings route's allowed keys.

**Step 3: Frontend toggle.**

```svelte
<!-- SettingsAI.svelte (extend) -->
<label>
  <input type="checkbox" checked={settings.aiInspection}
    onchange={(e) => updateSetting("ai.inspection", e.currentTarget.checked)} />
  AI Inspection — show per-signal scores in search results
</label>
```

**Step 4: `DiagnosticsBadge.svelte`.**

```svelte
<script lang="ts">
  let { score, components }: { score: number; components: SearchResult["score_components"] } = $props();
</script>
<span class="diag-badge" title={`bm25=${components?.bm25 ?? "—"} vec=${components?.vector ?? "—"} rank=${components?.rank_bm25}/${components?.rank_vector}`}>
  {score.toFixed(4)}
</span>
```

Mount inside the result-cell template (the existing virtualized grid's cell renderer; behind `if explain`).

**Step 5: Lightbox row.**

In `LightboxMetadata.svelte`, when the active media has `score_components` (passed in from the search route's selection), render:

```
Search relevance
  RRF        0.0156
  BM25       8.42  (rank 3)
  Vector     0.81  (rank 7)
```

**Step 6: Wire through the request.**

Search store reads the `ai.inspection` setting on init. When true, every `searchClient.search` call sets `explain=true`; the result cells receive `score_components` and the badge renders. When false, the request omits `explain` and badges are hidden.

**Step 7: Run.**

Run: `cd frontend && bun run test src/lib/search/DiagnosticsBadge.test.ts src/routes/SettingsAI.test.ts && bun run typecheck`
Run: `make api-generate` if user-settings HTTP keys changed.

Expected: PASS.

**Step 8: Commit.**

```bash
git add frontend/src/routes/SettingsAI.svelte frontend/src/lib/search/DiagnosticsBadge.svelte frontend/src/lib/lightbox/components/LightboxMetadata.svelte internal/service/usersettings/ internal/httpapi/ openapi.json frontend/src/lib/api/generated/schema.ts
git commit -m "feat(search): diagnostics mode — settings toggle, badge, lightbox row"
```

---

## Section W — End-to-end tests

### Task W1 — `cmd/e2e-server` seed + Playwright smoke

**Files:**
- Modify: `cmd/e2e-server/main.go` — seed:
  - 30 visible photos, 5 hidden photos
  - An `ai_results` set with active captions/tags for each
  - A building generation with mappings for 28 of 30 visible photos (≈93% completeness)
  - A mock `/v1/embeddings` endpoint that returns deterministic vectors keyed by input string
  - On the 31st request, fail with 503 to exercise the query-embedding-failure path
- Create: `frontend/tests/e2e/search.spec.ts`

**Step 1: Seed.**

In `cmd/e2e-server/main.go`, after the existing media + AI fixtures:

```go
// Seed an active embedding generation with mappings for 28 of 30 visible photos.
gens := embedding.NewGenerations(d.WriteDB(), d.ReadDB())
gen, _ := gens.FindOrCreateBuilding(ctx, ai.Fingerprint{ModelID: "siglip2", InputProfile: "jpeg-384-q85-metadata-stripped-embed-v1"}, 768)
mapping := embedding.NewMapping(d.WriteDB())
for i, mid := range visibleMediaIDs[:28] {
	_, _ = mapping.WriteVector(ctx, gen, mid, deterministicVec(i, 768))
}
_ = gens.Promote(ctx, gen.ID)

// FTS rows.
for _, mid := range allMediaIDs {
	withTx(d, func(tx *sql.Tx) error { return index.RefreshMediaFTS(ctx, tx, mid) })
}

// Mock /v1/embeddings.
mux.HandleFunc("/v1/embeddings", func(w http.ResponseWriter, r *http.Request) {
	// Fail every Nth request to exercise the degrade path; otherwise
	// return deterministic vectors keyed on the input string.
})
```

**Step 2: Playwright spec.**

```ts
// frontend/tests/e2e/search.spec.ts
test("⌘K opens search input and types a query", async ({ page }) => {
  await page.goto("/library");
  await page.keyboard.press("Meta+K");
  await page.keyboard.type("dog");
  await expect(page).toHaveURL(/\/search\?q=dog/);
  await expect(page.locator("[data-test=result-cell]")).toHaveCountAtLeast(1);
});

test("filter chip narrows results", async ({ page }) => {
  await page.goto("/search?q=dog");
  // open popover, type tag, select suggestion
  // expect result count to drop
});

test("indexing-status pill renders 28/30", async ({ page }) => { /* … */ });

test("under-80% banner appears when completeness < 0.80", async ({ page }) => { /* … */ });

test("query_embedding_failed banner appears when /v1/embeddings 503s", async ({ page }) => { /* … */ });

test("scrolling near bottom triggers next page", async ({ page }) => { /* … */ });

test("AI Inspection toggle reveals score badges in result cells", async ({ page }) => { /* … */ });
```

**Step 3: Run.**

Run: `make test-e2e`

Expected: PASS. The Playwright suite is slower; budget ~3–5 min.

**Step 4: Commit.**

```bash
git add cmd/e2e-server/main.go frontend/tests/e2e/search.spec.ts
git commit -m "test(e2e): search suite — chips, sort, pagination, status, diagnostics"
```

---

## Section X — Verification pass

### Task X1 — Final verification

**Files:** none modified by default; this task is an executable checklist. Any regression caught is fixed in its own commit.

**Step 1: Backend.**

Run all in parallel where possible:
- `go test ./... -shuffle=on` → PASS, no flakes on a clean re-run.
- `make build-release` → non-empty binary.
- `make lint` → PASS.
- `make nilaway` → PASS.
- `make api-generate` → no unstaged diff (regen idempotent).

**Step 2: Frontend.**

- `cd frontend && bun run typecheck` → PASS.
- `cd frontend && bun run test` → PASS.
- `make test-e2e` → PASS.

**Step 3: Smoke pass against a real fixture set.**

Same shape as the migration plan's Task 12 smoke pass, but with `[ai.embed].enabled=true`:

```bash
fotobank import --config config.yaml /path/to/fixtures
fotobank reconcile --config config.yaml
fotobank thumbs run --config config.yaml --once
fotobank ai backfill --config config.yaml --task=embed
# then start server with [ai.embed].enabled=true
fotobank server --config config.yaml &

# probe the search route
curl -fsS "http://127.0.0.1:8080/api/v1/search?q=dog&sort=relevance" | jq .

# probe autocomplete
curl -fsS "http://127.0.0.1:8080/api/v1/search/autocomplete/tags?q=dog" | jq .
curl -fsS "http://127.0.0.1:8080/api/v1/search/autocomplete/locations?q=Pa" | jq .

# probe health
curl -fsS "http://127.0.0.1:8080/api/v1/ai/health" | jq .
```

Expected:
- Embed worker reports queue draining in `/ai/health`.
- `/api/v1/search` returns hits with `embedding_completeness` rising over time.
- Autocomplete endpoints return owner-scoped, hidden-aware suggestions.
- Once embedded count reaches 95% of eligible, the building generation promotes; `embedding_completeness` jumps to (some value ≤1.0); subsequent searches use it.
- Thumb regen on a single photo invalidates that photo's mapping in the active generation; `/api/v1/search` shows the photo absent from semantic ranking briefly, then re-indexed.

**Step 4: SPA smoke.**

- Open `/library` in the browser.
- Press ⌘K, type "small dog" — expect navigation to `/search?q=small+dog`.
- Verify the indexing-status pill renders.
- Add a tag chip and a date-range chip from the popover.
- Switch sort to Newest; results re-order.
- Toggle the "AI Inspection" setting; result cells gain hover badges.
- Open one result in the lightbox — the metadata panel shows the "Search relevance" row.
- Close + open the SPA — URL params are restored.

**Step 5: Verification commit.**

If any regressions surfaced, fix them inline and create one commit per logical fix. The verification pass itself does not commit.

**Step 6: Final cleanup checkpoint.**

> **Final cleanup checkpoint.** Run `/roborev-fix` on the current branch. Address any findings before declaring search v1 done. Commit any cleanup as its own commit(s); do not amend.

---

## Verification checklist (must all be true before search v1 is declared done)

- [ ] `go test ./... -shuffle=on` passes cleanly twice in a row.
- [ ] `make build-release` succeeds; binary is non-empty.
- [ ] `make lint` and `make nilaway` both pass.
- [ ] `make api-generate` is idempotent (no diff after regen).
- [ ] `cd frontend && bun run typecheck && bun run test` passes.
- [ ] `make test-e2e` passes.
- [ ] All schema additions land in `000001_initial_schema.up.sql` (no new numbered migrations).
- [ ] No HTTP search/autocomplete path returns hidden photos or hidden-only counts to a session without unlock.
- [ ] No vec table name is built from user-supplied input — `vec_table_name` is always `"media_embeddings_g" + strconv.FormatInt(id, 10)`.
- [ ] `embedded_count` is honest — replacements zero-delta; the activator never trusts the cached value.
- [ ] Promotion is one transaction; ack-required pauses the activator without leaving stray state.
- [ ] Cursor `req_hash` covers the **effective** sort, not the raw sort.
- [ ] Query-embedding failure degrades to BM25-only and does **not** 500.
- [ ] The pill, banner, and diagnostics surfaces all read from the same hidden-aware service-layer call as the result rows.
- [ ] The AppHeader debounce, replace-vs-push history rules, and Enter behavior all match the spec §8.2.
- [ ] SSE `ai.embed.generation_activated` invalidates the search store's `requestHash` and the next user interaction restarts pagination.

When all the above are true, search v1 is done. Out-of-scope items (per spec §14) are explicitly named for future planners and not relitigated here.
