# fotobank web F2.2 — RAW + JPEG pairing

> F2.0 Viewer Foundation → F2.1 GPS Metadata → **F2.2 RAW + JPEG Pairing** (this doc) →
> F2.3 Albums + Sharing → F2.4 Hidden Privacy → F2.5 Lightbox Viewer.
> F2.2 is independent of F2.3 and F2.4 and may run in parallel with them. F2.4's
> hide cascade is the only forward dependency on this work.

**Date:** 2026-04-28
**Status:** Design (pending plan-writing approval)

---

## §1 Goal

Photographers commonly import directories containing both `IMG_1234.JPG` and a same-basename RAW (`.DNG`, `.RAF`, `.CR2`, `.ARW`, or `.NEF`). Today, fotobank stores them as two unrelated photo rows. F2.2 treats them as **one image, two files**:

- The JPEG is a normal media row visible in the library grid.
- The RAW is a hidden **sidecar**: stored on NAS, addressable via direct URL, but suppressed from the default library, sessions, and album views.
- The JPEG's detail page lists the sidecar(s) and offers per-file downloads via `/api/v1/media/{id}/original`.
- A direct visit to `/media/<raw-id>` renders a dedicated file-detail / download page with a banner-link back to the primary.

New imports compute pairs at the end of each `ImportDirectory` run by examining both the just-imported batch and any existing rows that share the same `(owner, directory)` keys, so a JPEG imported today pairs with a RAW imported last week (and vice-versa). A `fotobank pair backfill` CLI runs the same pairing function across the whole library for verification or to recompute pairs after a future logic change. Pre-F2.2 dev rows have empty `import_source_path` and are not pairable; the expected operator action is to wipe the dev DB and re-import.

The scope is deliberately narrow: hide-with-affordance, no stack abstraction, no manual pair / unpair, no library badging. The data model leaves room for richer features later.

---

## §2 Non-goals

- **Stack abstraction.** No equal-members group, no swap-primary, no expand-stack. A pair is one primary plus zero-or-more sidecars; the relationship is one-directional.
- **Manual pair / unpair.** Auto-detection only.
- **Library grid badge for "has RAW".** List DTOs carry no pair data. Until a user reports they want at-a-glance awareness, the grid stays clean.
- **`fotobank pair detach` CLI.** Future. v1 ships only `pair backfill`.
- **Re-pairing on rename or move.** Pairs derive from `import_source_path`; if a user reorganizes NAS and re-imports, pair membership recomputes from the new layout.
- **GPS or timestamp divergence reconciliation.** If JPEG and RAW EXIF disagree, each row keeps its own values. No merge logic.
- **Search / FTS over sidecars.** When search ships, sidecars are excluded by default. Out of F2.2.
- **Additional RAW formats.** F2.2 ships against the existing five (ARW, RAF, DNG, CR2, NEF). Adding ORF, RW2, CR3, etc. is a one-line change in `internal/ingest/discover.go` for a future plan.
- **Thumbnail rendering on the sidecar direct page.** The thumb pipeline still runs on sidecars — preserving the "every photo row has a thumb" invariant — but the direct page intentionally doesn't render one. The page is file-detail / download, not a second photo viewer.
- **Web delete UI.** Deferred per the broader roadmap. F2.2 captures the future delete contract (§8.7) but doesn't ship a delete UI.

---

## §3 Architecture

Three subsystems cooperate:

1. **Import pipeline** (`internal/ingest`). Workers continue to insert media rows with `thumb_status='pending'` per the existing flow. After the worker `WaitGroup` barrier in `ImportDirectory`, a single-threaded **pairing pass** computes the set of `(owner, directory)` keys touched by the just-imported batch, queries existing media rows in those directories (so a JPEG imported today pairs with a RAW imported last week, and vice-versa), computes pair relationships from `import_source_path`, and writes `paired_with_id` updates. The pass returns before `ImportDirectory` does.

2. **Pair backfill CLI** (`internal/cli/pair.go`). `fotobank pair backfill` runs the same idempotent pairing function across the entire library (or a `--since` / `--owner` slice). Mirrors the F2.1 `gps backfill` command structure exactly.

3. **HTTP and frontend.** `MediaService.List` defaults to filtering out sidecars at the SQL layer; `GET /api/v1/media/{id}` for a JPEG primary embeds the sidecar list; `GET /api/v1/media/{id}` for a sidecar returns the row with a `paired_with` summary populated. Frontend `MediaDetail` renders a `Files` row on primaries and a banner-plus-download layout on direct sidecar visits.

The schema change is small: one new column for the pair FK, one new column for pairing input, one self-reference CHECK, two owner-consistency triggers, one column-semantics flip, one provenance index. No new tables. No migration file — the schema change is squashed into `000001_initial_schema.up.sql` (pre-deploy, same approach as F2.1 Task 2).

Sharing (Plan E) needs one targeted update so a recipient who has scope access to a JPEG can also fetch the RAW. The change is internal to `share.Repo.CoverMediaByScopes`; the entry point `share.ScopeResolver.CheckMediaAccess` keeps its signature.

---

## §4 Data model

### §4.1 New columns on `media`

```sql
-- Root-relative original path captured at import time, e.g.
-- "2024-Paris/IMG_1234.JPG". Becomes the substrate for pair detection;
-- persisted so the backfill CLI can recompute pairs without revisiting
-- the source filesystem (which may have moved or been re-imported).
import_source_path TEXT NOT NULL DEFAULT '',

-- Foreign key to the JPEG primary. NULL on primaries and on standalone
-- (RAW-only or JPEG-only) rows. ON DELETE SET NULL is the referential-
-- integrity floor; service-layer delete refuses to delete a primary
-- with sidecars (§8.7).
paired_with_id UUID NULL REFERENCES media(id) ON DELETE SET NULL,
```

Plus a table-level CHECK to forbid self-reference:

```sql
CHECK (paired_with_id IS NULL OR paired_with_id <> id),
```

`paired_with_id` is declared `UUID` to match `media.id` at `internal/db/migrations/000001_initial_schema.up.sql:24`. SQLite stores both with TEXT affinity, but the explicit declaration matches the project convention.

### §4.1a Owner-consistency triggers on `paired_with_id`

The FK plus the CHECK guarantees referential integrity and no-self-reference, but does not by itself ensure that a sidecar's `paired_with_id` points at a primary owned by the same `(owner_hub, owner_user_id)`. Three SQLite triggers — two mirroring the existing `album_media_owner_consistency_*` pair at `000001_initial_schema.up.sql:105-129`, plus an inverse trigger on the primary side — enforce this at the DB level:

```sql
CREATE TRIGGER media_paired_with_owner_consistency_insert
BEFORE INSERT ON media
FOR EACH ROW
WHEN NEW.paired_with_id IS NOT NULL
BEGIN
    SELECT CASE
        WHEN (SELECT owner_hub FROM media WHERE id = NEW.paired_with_id)
                 != NEW.owner_hub
          OR (SELECT owner_user_id FROM media WHERE id = NEW.paired_with_id)
                 != NEW.owner_user_id
        THEN RAISE(ABORT, 'sidecar and primary must share owner')
    END;
END;

CREATE TRIGGER media_paired_with_owner_consistency_update
BEFORE UPDATE OF paired_with_id, owner_hub, owner_user_id ON media
FOR EACH ROW
WHEN NEW.paired_with_id IS NOT NULL
BEGIN
    SELECT CASE
        WHEN (SELECT owner_hub FROM media WHERE id = NEW.paired_with_id)
                 != NEW.owner_hub
          OR (SELECT owner_user_id FROM media WHERE id = NEW.paired_with_id)
                 != NEW.owner_user_id
        THEN RAISE(ABORT, 'sidecar and primary must share owner')
    END;
END;

CREATE TRIGGER media_paired_with_owner_consistency_primary_update
BEFORE UPDATE OF owner_hub, owner_user_id ON media
FOR EACH ROW
WHEN (NEW.owner_hub != OLD.owner_hub OR NEW.owner_user_id != OLD.owner_user_id)
     AND EXISTS (SELECT 1 FROM media WHERE paired_with_id = NEW.id)
BEGIN
    SELECT RAISE(ABORT, 'cannot change primary owner while sidecars reference it');
END;
```

The `WHEN NEW.paired_with_id IS NOT NULL` guard on the first two triggers skips the lookup on primaries and standalones. The update trigger is keyed on the pair-FK column AND the owner columns so a hypothetical future owner-rename path can't slip a cross-owner pair through. The third trigger closes a corner case the first two miss: updating `owner_hub`/`owner_user_id` on a *primary* row that has sidecars referencing it would otherwise leave the sidecars pointing at a primary with a different owner. It rejects any such update outright; fotobank has no current code path that re-owns a primary, so the strict reject is acceptable. The down file's existing `DROP TABLE IF EXISTS media` (`000001_initial_schema.down.sql:12`) drops these table-attached triggers automatically — no down-file edit needed.

The service-layer pairing pass already restricts candidates to one owner per `(owner, directory)` group, so these triggers will never RAISE in normal operation. They're a defence-in-depth floor, mirroring how `album_media_owner_consistency_*` exists even though `AlbumService.AddMedia` already does the owner check.

### §4.2 `original_filename` semantics flip

Pre-F2.2, `internal/ingest/importer.go::buildMediaRow` (line ~304) writes `OriginalFilename: c.Path` — the absolute filesystem path of the import candidate. F2.2 changes this to:

```go
OriginalFilename: filepath.Base(c.Path),
```

The column then actually means what its name says: the basename of the source file (e.g. `"IMG_1234.JPG"`). The change is needed because the pairing logic and the new Files dl row both use `original_filename` for display.

Audited callers — none rely on the absolute-path semantics:
- `internal/reconcile/...` does not use `OriginalFilename` for path arithmetic.
- `internal/httpapi/media.go::toMediaDTO` passes the value through verbatim. JSON consumers see the new shape after F2.2.
- `frontend/src/lib/media/mediaStore.svelte.ts::toMedia` already expects a basename for display.
- Tests in `internal/media/repo_test.go` and `internal/cli/import_test.go` need fixture updates — captured as a step in the implementation plan.

### §4.3 Indexes

```sql
CREATE INDEX media_owner_import_source_path_idx
    ON media(owner_hub, owner_user_id, import_source_path);
CREATE INDEX media_paired_with_id_idx
    ON media(paired_with_id) WHERE paired_with_id IS NOT NULL;
```

`media_owner_import_source_path_idx` is used by the post-import pairing pass (§5.3) to fetch existing rows for the `(owner, directory)` groups touched by the just-imported batch — that's how a JPEG imported today pairs with a RAW imported last week without scanning the entire `media` table. Also a useful debug / provenance lookup. The CLI backfill (§5.4) uses an owner-scoped scan but benefits from the same index on large libraries.

`media_paired_with_id_idx` is the partial index that backs `Repo.GetSidecars` (called from the primary detail DTO embed path, §6.4). Without it, every primary detail fetch would full-scan `media` to find sidecars referencing the row.

### §4.4 Down file

`000001_initial_schema.down.sql` is unchanged. It already drops `media`, `albums`, `album_media`, `scopes`, `scope_media`, etc. in reverse-creation order, which subsumes any column-level changes. Editing the up file in place — and leaving the down file as a full prior-state teardown — is the established pattern (F2.0 / F2.1 followed it for their schema additions).

### §4.5 No data migration

The repo is pre-deploy; no users have rows. The schema change is squashed into the existing `000001_initial_schema` pair, same as F2.0 / F2.1 schema work. The implementation plan begins with the `FOTOBANK_MIGRATION_BASE_REF` env-var pre-flight (mirrors F2.1 Task 2's pattern) so the prek migration-history-check hook permits the in-place edit.

Pre-F2.2 dev or test rows have empty `import_source_path` and are not pairable. The pairing pass and the CLI both skip rows with empty `import_source_path` (also documented in §11). The expected operator action is to wipe the dev DB and re-import the source directories so every row gets a non-empty `import_source_path`.

---

## §5 Pairing rule and detection

### §5.1 Pairing rule

A row pairs with another row iff:

- Both rows belong to the same `owner_hub` + `owner_user_id`.
- They share the same **directory** when computed from `import_source_path` via `filepath.Dir`.
- Their **basename stems** (basename minus the final extension) match case-insensitively.
- They have **complementary extension classes**:
  - JPEG class = `{.jpg, .jpeg}` — primaries.
  - RAW class = `{.dng, .raf, .cr2, .arw, .nef}` — sidecars.

Same-class matches (two JPEGs in a directory with the same basename) do not pair.

### §5.2 Multi-sibling resolution

| Scenario | Outcome |
|---|---|
| One JPEG, N RAW siblings | All N RAWs become sidecars of the JPEG. Detail page lists them all. |
| One RAW, N JPEG siblings (N ≥ 2) | Ambiguous. RAW stays unpaired. Pairing pass logs a `WARN` event with the RAW's id and the candidate JPEG ids. |
| One JPEG, zero RAW | JPEG is a standalone primary (`paired_with_id IS NULL`). |
| One RAW, zero JPEG | RAW is a standalone primary in the library (`paired_with_id IS NULL`). Visible like any other photo. |

The library is honest about RAW-only rows: a user whose collection contains DNGs without JPEG counterparts sees them in the grid.

### §5.3 Detection — post-import barrier pass

`internal/ingest/importer.go::ImportDirectory` currently fans out rows through `cfg.ConcurrentWorkers` goroutines. Each worker `Insert`s its row with `thumb_status='pending'`. The thumb worker (server-side) claims pending rows independently — F2.2 changes nothing about that path.

After the worker `sync.WaitGroup` returns, a single goroutine runs the pairing pass and writes `paired_with_id` updates before `ImportDirectory` returns. The pass MUST consider both the new batch and existing rows so a JPEG imported today pairs with a RAW imported last week (and vice-versa):

1. Compute the distinct set of `(owner, dir(import_source_path))` keys touched by the just-imported batch.
2. For each key, fetch the existing media rows in that `(owner, directory)` slice via `media_owner_import_source_path_idx`. The candidate set per key is the union of existing rows in that directory plus just-imported rows in the same key.
3. Within each combined group, separate JPEG-class from RAW-class rows by extension.
4. For each JPEG-class row, find RAW-class rows with the same case-insensitive stem; set their `paired_with_id` to the JPEG's id via `UPDATE media SET paired_with_id = ? WHERE id = ?`.
5. For each RAW-class row in an ambiguous group (multiple JPEG-class siblings with the same stem), force `paired_with_id = NULL` and log a `WARN` event with the RAW's id and the candidate JPEG ids. This explicitly covers the case where the RAW was previously paired but a freshly-imported sibling JPEG just made the directory ambiguous.
6. The pass touches `paired_with_id` only for rows in the candidate set. Rows in directories untouched by this batch are not re-evaluated.

The pass holds no DB lock beyond per-statement transactions. Two parallel `ImportDirectory` invocations are blocked by the import file lock (`internal/ingest::Acquire`); the lock encloses both the worker phase and the pairing phase.

Rows with empty `import_source_path` are skipped — they're either pre-F2.2 dev rows (re-import to recover) or a test-only injection path that bypasses ingest.

### §5.4 Detection — backfill CLI

`fotobank pair backfill` (see §11 for full flag set) runs the same pairing function over either the whole library for the caller, all owners, or a single named owner.

The function is **idempotent**: running it twice on an unchanged DB is a no-op. New imports between runs may produce different results (more pairs discovered), but a re-run on an unchanged DB produces the same `paired_with_id` map.

`--mode=full` is the only mode in v1. It is documented as **idempotent recompute-only**: every covered row's `paired_with_id` is recomputed from scratch and overwritten (with NULL if no pair, or with the discovered primary's id). No destructive arms.

The backfill CLI takes the same import file lock to serialize against in-flight imports.

### §5.5 Idempotency contract

The pairing function MUST satisfy:

- `pair(rows) == pair(pair(rows))` — running twice on the same row set yields the same result. (idempotency)
- Reordering input rows does not change the output. (commutativity)

A monotonicity claim ("adding a row can only ADD pairings") would conflict with §5.2: adding a second JPEG with the same stem to a `(JPEG, RAW)` directory turns the RAW from paired to ambiguous-unpaired. The conservative ambiguity rule wins; a user disambiguates by renaming or pruning the duplicate JPEG.

Tests in the implementation plan exercise both kept properties plus the JPEG-arrival-flips-RAW-from-paired-to-unpaired transition.

---

## §6 HTTP and DTO surface

### §6.1 ListFilter

```go
type ListFilter struct {
    // ... existing fields ...
    IncludeSidecars bool // false by default at the service layer.
}
```

`Repo.List` honors the flag:
- `false` (default): adds `AND paired_with_id IS NULL` to the SQL predicate.
- `true`: returns all rows matching the rest of the filter.

`MediaService.List` clamps `IncludeSidecars = false` before delegating to the repo unless the caller explicitly passes `true`. No HTTP route surfaces the `true` value in F2.2; the flag exists for internal callers (the post-import pairing pass, the backfill CLI, reconcile).

### §6.2 mediaDTO additions

```go
type mediaDTO struct {
    // ... existing fields ...

    // Set on a sidecar row when fetched directly. The bare ID supports
    // SPA routing; the summary saves a round trip on the sidecar
    // detail page.
    PairedWithID *string         `json:"paired_with_id,omitempty"`
    PairedWith   *pairSummaryDTO `json:"paired_with,omitempty"`

    // Set on a primary row when fetched via the detail endpoint. List
    // endpoint never populates this (avoids recursion at the SQL
    // layer). Sorted by original_filename ascending.
    Sidecars []mediaDTO `json:"sidecars,omitempty"`
}

type pairSummaryDTO struct {
    ID               string `json:"id"`
    OriginalFilename string `json:"original_filename"`
}
```

Embedded sidecar DTOs in the `Sidecars` slice never recurse: their own `Sidecars` field is omitted (the population code only sets it on the top-level row). Their `PairedWithID` and `PairedWith` ARE populated and point back to the top-level row.

### §6.3 List endpoint

`GET /api/v1/media` returns the existing list shape. Sidecars filtered out at the SQL layer (§6.1). List DTOs carry no `Sidecars`, no `PairedWithID`, no `PairedWith`. The wire shape for callers that don't care about pairing is unchanged.

### §6.4 Detail endpoint — primary

`GET /api/v1/media/{id}` for a primary embeds the sidecar list:

```json
{
  "id": "abc-123",
  "original_filename": "IMG_1234.JPG",
  "...": "...",
  "sidecars": [
    {
      "id": "def-456",
      "original_filename": "IMG_1234.DNG",
      "paired_with_id": "abc-123",
      "paired_with": {"id": "abc-123", "original_filename": "IMG_1234.JPG"},
      "...": "..."
    }
  ]
}
```

The handler issues one extra query per detail call: `SELECT … FROM media WHERE paired_with_id = ? ORDER BY original_filename ASC`. Each row materializes via `toMediaDTO`, with `PairedWithID` and `PairedWith` populated from the primary's identity.

### §6.5 Detail endpoint — sidecar

`GET /api/v1/media/{id}` for a sidecar returns the row with `paired_with` summary:

```json
{
  "id": "def-456",
  "original_filename": "IMG_1234.DNG",
  "...": "...",
  "paired_with_id": "abc-123",
  "paired_with": {"id": "abc-123", "original_filename": "IMG_1234.JPG"}
}
```

The summary saves a follow-up GET when the frontend renders the banner-link. The handler issues one extra single-row query when the row's `paired_with_id` is non-null: `SELECT id, original_filename FROM media WHERE id = ?`.

### §6.6 Bytes endpoint

`GET /api/v1/media/{id}/original` is unchanged. A sidecar's RAW bytes are fetchable via the same handler since it streams the row's `path` regardless of pair status. Owner auth is enforced via `MediaService.OpenOriginal` per the existing layering.

`GET /api/v1/media/{id}/thumb` is also unchanged. Sidecars have thumbs (extracted from RAW headers via `thumb.ExtractPreview`); they're just not surfaced in the default UI.

### §6.7 OpenAPI regeneration

`make api-generate` runs as part of the prek hook on commit. The implementation plan includes one explicit `api-generate` step after the DTO changes so the regenerated `openapi.json` and `frontend/src/lib/api/generated/schema.ts` land in the same commit as the Go changes.

---

## §7 Frontend UX

### §7.1 Library grid

No change. Sidecars are filtered server-side; the grid sees primaries and standalone-RAW rows only.

### §7.2 MediaDetail — primary

The existing `<dl>` (introduced in F2.1) gains a `Files` row, listed alongside `Captured` and `Location`:

```
Captured     2024-06-15 14:30 UTC
Location     Paris, Île-de-France, France
Files        IMG_1234.JPG (download)
             IMG_1234.DNG (download)
```

Each filename is a download link to `/api/v1/media/{id}/original`. The primary appears first; sidecars follow, sorted by `original_filename`. The mime type is rendered next to the filename when extension alone is ambiguous (rare).

The Files row renders only when the row has at least one sidecar. Standalone JPEG primaries with no sidecars render no Files row — the existing photo + Location layout suffices.

### §7.3 MediaDetail — sidecar (direct access)

Reached by navigating to `/media/<raw-id>` directly. Layout:

```
[← Back to Library]

  RAW sidecar for IMG_1234.JPG  →  (link to /media/<jpeg-id>)

  IMG_1234.DNG                                23.4 MB
  Captured  2024-06-15 14:30 UTC
  Location  Paris, Île-de-France, France

  [ Download IMG_1234.DNG ]
```

No photo or thumb is rendered. The banner uses the primary's `original_filename` (from the embedded `paired_with` summary), and the link is a SPA route via `handleInternalLinkClick`.

The info dl shows what's known about the sidecar: filename, size, captured time, location. Camera/lens metadata renders here too if EXIF was extracted. GPS comes from the sidecar's own row, which the F2.1 `gps backfill` populates per §8.3.

### §7.4 Routing

No new route. Both `/media/<jpeg-id>` and `/media/<raw-id>` go through the existing `MediaDetail.svelte` route. The component branches on `media.paired_with_id`:

- `media.paired_with_id == null` → render the primary layout (photo + dl + Files row if sidecars).
- `media.paired_with_id != null` → render the sidecar layout (banner + dl + download button).

### §7.5 Frontend type extension

`Media` in `frontend/src/lib/media/mediaStore.svelte.ts` gains:

```ts
type Media = {
  // ... existing ...
  paired_with_id?: string;
  paired_with?: { id: string; original_filename: string };
  sidecars?: Media[];
};
```

The `mediaStore.merge` identity-fields-covered guard adds the three new field names to the `Exclude<keyof Media, …>` literal. The `unchanged` predicate compares the new fields:

- `paired_with_id` and `paired_with?.id` — strict equality.
- `sidecars` — compare by length and id-list equality (cheap, sufficient for dirty-tracking; deep equality is overkill since sidecars are themselves mediaDTOs that the store will merge separately if it ever needs to).

`toMedia` populates the new fields from the raw API shape, mirroring the F2.1 GPS pattern. With `tsconfig.json` `exactOptionalPropertyTypes: true`, optional fields are omitted from the literal when source data is missing; do not assign `undefined` explicitly.

---

## §8 Operations cascade

### §8.1 Albums

Albums reference primaries only. `album_media.media_id` is always a primary's id. When the user adds a JPEG to an album, the sidecar does not auto-join. The album view, when it later wants to surface RAW downloads, walks each member's `Sidecars` via the detail endpoint.

`AlbumService.AddMedia` (`internal/service/album_service.go:178`) MUST reject media IDs whose row has a non-NULL `paired_with_id`. The per-ID pre-flight ownership loop at `internal/service/album_service.go:194-206` gains a sidecar check immediately after the existing owner check: if `m.PairedWithID != nil`, return `errs.ErrInvalidArgument` wrapped with a message that names the sidecar id and the directive `"albums reference primaries only"`. The HTTP layer maps `errs.ErrInvalidArgument` to 400 per `internal/httpapi/errors.go::Translate`, which is the right surface for "you sent the wrong kind of id". The Album AddMedia DTO documentation gains one line noting the new rejection class.

### §8.2 Sharing (Plan E)

A share scope on a primary transitively grants access to its sidecars. A recipient hitting `/api/v1/shared/media/<raw-id>/original` succeeds iff the RAW's primary is in their accessible scope.

The implementation hangs off three named functions:

- **`share.ScopeResolver.CheckMediaAccess`** (`internal/share/resolver.go`) is the auth entry point. Signature unchanged. F2.2 changes the implementation only via its delegate.

- **`share.Repo.CoverMediaByScopes`** (`internal/share/repo.go:581`) is the SQL coverage query F2.2 amends. The new predicate accepts a row whose `paired_with_id` resolves to a covered primary, in addition to a row whose `id` is directly covered:

  ```sql
  -- existing coverage clause: media.id IN (resolved set)
  -- F2.2 adds:
  OR media.paired_with_id IN (resolved_primaries)
  ```

  Primary-only scopes continue to behave identically; the OR clause adds coverage for sidecars without changing existing semantics.

- **`SharedReadService.GetMedia`, `OpenOriginal`, `OpenThumb`** (`internal/service/shared_read_service.go`) all funnel through `CheckMediaAccess` and inherit the fix automatically.

- **`share.Repo.ListSharedMediaIDs`** keeps listing primaries only — sidecars are downloadable attachments, not shared-grid rows. The shared-grid view (recipient-side) shows the same images the owner-side library shows.

The scope's `allow_download` flag continues to gate whether the recipient can fetch any bytes (JPEG or RAW). F2.2 does not introduce a per-format download flag.

`ShareService.Create` (`internal/service/share_service.go:54`) MUST reject sidecar IDs in the `media_set` per-ID pre-flight ownership loop at `internal/service/share_service.go:107-115`. The check sits immediately after the existing `m.Owner != caller` check at line 112-114: if `m.PairedWithID != nil`, return `errs.ErrInvalidArgument` with the same `"shares reference primaries only"` directive used by the album rule (§8.1). Without this rejection, a sidecar-only media-set scope could exist whose every member is hidden by `ListSharedMediaIDs`, leaving the recipient with an empty grid and a working `/original` URL — a confusing surface. Rejecting at create time keeps the recipient experience coherent.

### §8.3 GPS backfill (F2.1)

`fotobank gps backfill` continues to operate on **all photo rows including sidecars**. RAW EXIF is often a better source of capture metadata than JPEG; the sidecar detail page surfaces it via the info dl. F2.2 makes no change to `media.Repo.ListGPSBackfillCandidates`.

If perf becomes an issue at scale (hundreds of thousands of paired rows), a future flag `--exclude-sidecars` can be added. Out of F2.2.

### §8.4 Hidden privacy (F2.4)

When F2.4 ships, the hide flag on a primary cascades to its sidecars: every hide-aware query treats `paired_with_id IN (hidden_primaries)` as hidden. F2.2 does not ship hide; the cascade is a forward dependency.

### §8.5 Reconcile

No special handling. Each row is verified against disk independently. If a JPEG primary's file vanishes but the row stays, that's an orphan-bytes case the existing reconciler handles. The pair relationship doesn't affect reconcile logic.

### §8.6 Thumb pipeline

Sidecars continue to get queued for thumbs via the existing `thumb_status='pending'` flow. Cost is small; preserves the "every photo row has a thumb" invariant; future-proofs any UI that wants a sidecar thumb.

One required alignment: `internal/thumb/worker.go::isRAWMime` (lines 348-357) currently knows ARW, RAF, DNG, and CR2 but not NEF. `internal/ingest/discover.go:71` already classifies NEF as `image/x-nikon-nef` and TypePhoto, so without an `isRAWMime` update a NEF sidecar would be enqueued, then fail at the worker because the worker would try to decode it as a regular image rather than via `thumb.ExtractPreview`. F2.2 adds `"image/x-nikon-nef"` to the `isRAWMime` switch and adds a happy-path test in `internal/thumb/raw_test.go` that exercises a NEF fixture (or a synthetic NEF-MIME row pointed at an embedded-JPEG TIFF, mirroring the existing ARW pattern). Without that alignment, §1's claim that all five RAW formats preserve the invariant would be false.

### §8.7 Delete (future contract)

The existing `MediaService` does not yet expose `Delete` over HTTP (deferred per the broader roadmap). When delete ships, F2.2's contract is:

- `MediaService.Delete(primaryID)` returns a new sentinel `errs.ErrPairedSidecarsExist` when the primary has at least one sidecar.
- The error message names the count, e.g. `"This photo has 2 RAW sidecars. Delete the sidecars first or detach them."`
- The HTTP error translator (`internal/httpapi/errors.go::Translate`) maps the sentinel to `409 Conflict`.

The schema's `ON DELETE SET NULL` is the referential-integrity floor only — it protects sidecar rows from FK violation if a primary is somehow deleted via a path that bypasses the service. The service-layer block is the user-facing rule.

F2.2 ships the sentinel and the HTTP mapping. F2.2 does NOT ship a delete UI or a `fotobank pair detach` CLI; both are deferred. The contract is captured so future delete flows inherit it.

---

## §9 Risks

1. **`original_filename` semantics flip.** `buildMediaRow` currently writes `Candidate.Path` (absolute) here. Flipping to `filepath.Base` is safe per the audited callers, but the implementation plan must include one grep pass at change time to confirm no caller has appeared since this spec was written. Tests in `internal/media/repo_test.go` and `internal/cli/import_test.go` need fixture updates.

2. **Post-import pairing pass adds latency.** The pass is single-threaded and runs after the worker barrier and before `ImportDirectory` returns. Two cost contributors: (a) one indexed query per touched `(owner, directory)` key to fetch existing rows, (b) per-bucket O(N²) extension/stem comparisons in memory. (a) is bounded by the number of distinct directories in the batch and is cheap per query. (b) is fine for typical batch sizes (hundreds to low thousands per directory) but quadratic in pathological cases; the implementation must group by directory first (O(N) bucket) so comparisons happen within each bucket only. Tests should include a 10k-row synthetic batch (one directory) to catch a regression.

3. **Plan E sharing cross-subsystem change.** `share.Repo.CoverMediaByScopes` is owner-side code that recipients depend on. The SQL extension to honor sidecar coverage must NOT regress existing behavior on primary-only scopes. Tests must exercise: (a) recipient with scope on a JPEG can fetch sidecar bytes, (b) recipient with no scope cannot fetch sidecar bytes, (c) `ListSharedMediaIDs` continues to return primaries only.

4. **Idempotency of the pair pass.** The contract in §5.5 must be tested against:
   - Run pair → run pair → assert no DB writes on the second run. (idempotency)
   - Reorder input slice, run pair → assert same output. (commutativity)
   - Pre-existing `(JPEG, RAW)` pair → import a second JPEG with the same stem → run pair → assert RAW's `paired_with_id` is now NULL and a `WARN` was logged. (the explicitly non-monotonic transition from §5.5)
   - Two separate imports landing JPEG and RAW in the same directory in either order → assert the late-arriving file pairs with the existing one. (bidirectional discovery)

5. **Filesystem normalization edge case.** `import_source_path` is bytewise UTF-8 from the OS. Case-fold for stem comparison uses `strings.ToLower`; directory comparison is bytewise-equal. macOS may surface NFD-normalized filenames whereas Linux typically NFC; mixed-source collections could mis-match. Mitigation: the implementation normalizes paths with `unicode/norm.NFC` before comparison. Test fixture covers an NFD-normalized directory entry.

6. **Pre-deploy migration via `FOTOBANK_MIGRATION_BASE_REF`.** Editing `000001_initial_schema.up.sql` in place requires the env-var escape hatch (mirrors F2.1 Task 2). The implementation plan records the pre-flight step and the env var setting, so the prek `migration-history-check` hook permits the in-place edit.

---

## §10 Dependencies

- **F1 / F2.0 / F2.1 shipped** — Plan A through F2.1 are on master.
- **One new direct Go dependency: `golang.org/x/text`** for `unicode/norm` (NFC normalization in §9.5). `go.mod` does not currently require `x/text` directly; the implementation plan adds it via `go get golang.org/x/text` and a `go mod tidy` step. This is the only new dependency F2.2 adds. No new frontend npm deps.
- **F2.4 (Hidden Privacy)** is downstream of F2.2 for the cascade behavior described in §8.4.
- **F2.3 (Albums + Sharing)** is independent; can ship in parallel.

---

## §11 CLI surface

```
fotobank pair backfill [flags]

Recompute paired_with_id for media rows by re-scanning import_source_path
groupings. Idempotent — running twice on an unchanged DB is a no-op.

Flags:
      --config string         Path to config file (default: DefaultConfigPath)
      --since duration        Filter scope to rows imported within this duration
                              (e.g. 24h, 168h, 30m). The recent rows determine the
                              set of (owner, directory) keys touched; pair state
                              is then recomputed against ALL rows in those
                              directories with non-empty import_source_path,
                              including older ones, so an older JPEG paired with
                              a recent RAW (or vice-versa) is discovered. Parsed
                              before the DB opens so a malformed value fails fast
                              (exit 2).
      --mode string           Pair backfill mode (default "full"; only "full"
                              in v1)
      --owner string          Admin: backfill for hub:user (bypasses stub-mode
                              requirement)
      --all-owners            Admin: backfill across all registered owners
                              (bypasses stub-mode requirement)

Output (stdout, single line):
  pair backfill: processed=N paired=N unchanged=N ambiguous_unpaired=N

Exit codes:
  0   success (output emitted, even if 0 rows touched).
  1   runtime failure (DB open, lock acquisition, query failure).
  2   usage failure (bad flag, malformed --since, --owner + --all-owners,
      stub mode required and not satisfied).
```

`--mode=full` is the only mode in v1. Documented as **idempotent recompute-only**: every covered row's `paired_with_id` is recomputed from scratch; existing values are overwritten with NULL if no pair, or with the discovered primary's id. No destructive arms. The flag is included for symmetry with `fotobank gps backfill --mode=full|fill-missing|relabel` and to leave room for a future `--mode=verify` (read-only check) without re-shaping the surface.

Rows with empty `import_source_path` (pre-F2.2 dev/test rows or test-only injection paths) are skipped. The line counter that prints to stdout reports them as `unchanged`.

---

## §12 Spec coverage cross-check

| Section | Implementing scope |
|---|---|
| §3 Architecture | Single ingest barrier-pass + new CLI + small HTTP/frontend surface. |
| §4 Data model | One migration edit (in place): two columns, one CHECK, two owner-consistency triggers, one index, one column-semantics flip. |
| §5 Pairing rule + detection | One pure-Go function + tests for idempotency and ambiguity. |
| §6 HTTP / DTO | Three DTO field additions, two extra queries on the detail handler. |
| §7 Frontend UX | One `Files` row, one sidecar layout branch, three new fields on the `Media` type. |
| §8 Operations cascade | Albums + Shares sidecar-rejection contracts, Plan E SQL extension, NEF added to thumb.isRAWMime; everything else is no-change. |
| §9 Risks | Captured for the implementation plan. |
| §10 Dependencies | One new direct Go dep: `golang.org/x/text` (for `unicode/norm`). |
| §11 CLI | One subcommand mirroring the F2.1 `gps backfill` shape. |
