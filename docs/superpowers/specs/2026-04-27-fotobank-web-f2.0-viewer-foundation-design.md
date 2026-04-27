# F2.0 — Viewer Foundation Design

> Sub-plan of the F2 viewer-suite milestone. F2 ships in five sub-plans:
> **F2.0 Viewer Foundation** (this doc) → F2.1 GPS Metadata → F2.2 Albums + Sharing →
> F2.3 Hidden Privacy → F2.4 Lightbox Viewer. Each sub-plan has its own spec and
> implementation plan. F2.0 is the prerequisite infrastructure layer; it ships
> reload-safe SPA routing, an incremental MediaStore, a shared MediaCell, and the
> backend thumb tiers (`preview=2560`, `large=4096`) the lightbox needs to render
> at fit and 1:1 quality.

## 1. Goal

Land the viewer-foundation infrastructure so subsequent F2 sub-plans (and especially
F2.4 lightbox) can target a coherent shell:

- **Reload-safe SPA navigation.** Pasted links (`/media/:id`, future `/albums/:id`,
  `/hidden`, `/search`) survive a hard reload because the embed handler already
  serves `index.html` for any non-API, non-asset path; F2.0 wires the matching
  client-side router on top.
- **MediaStore that scales.** The F1 `merge()` is O(N²) over paginated scrolls.
  F2.0 keeps a long-lived `byMonth` Map plus a `byId` index, dirties only the
  months that changed, and reuses month object refs for unaffected chunks so
  the keyed each-block doesn't churn.
- **One MediaStore, not three.** Today Library and Sessions each instantiate
  their own store. F2.0 hoists a single shared instance to App.svelte; SPA nav
  preserves it.
- **Shared MediaCell.** Library, Sessions, and (in F2.4) the lightbox-strip all
  render the same anchor + img + selection-aware cell. F2.0 extracts it as a
  presentational component.
- **Thumb tiers the lightbox needs.** The current pipeline ships
  `grid=256`/`preview=1024`/`lightbox=2048`. F2.0 changes `preview` to **2560**,
  adds `large=4096`, and removes `lightbox`. The worker emits all configured
  sizes per claim already (via `thumb.AllSizes`); F2.0 is mostly vocabulary.
- **Operator-driven regenerate, scoped by owner.** The existing
  `fotobank thumbs regenerate` subcommand already bumps `thumb_version`
  and flips rows to `pending` for the configured stub principal. F2.0
  adds owner-scope flags (`--owner <hub>:<user>`, `--all-owners`) that
  let an operator regenerate for a specific principal or every
  principal in the database, without touching `identity.mode`.

## 2. Scope

**In scope**

- Custom client-side router (singleton class, `$state` for current match).
- SPA fallback verification (the embed handler already does it; F2.0 locks
  this with an explicit Playwright reload test).
- `frontend/src/routes/NotFound.svelte` — fallback view for unknown paths.
- `frontend/src/routes/MediaDetail.svelte` — stub that shows the matched id;
  F2.4 replaces it with the lightbox.
- `frontend/src/lib/grid/MediaCell.svelte` — presentational shared cell.
- `MediaStore` `merge()` rewritten with `byMonth` + `byId` indices, in-place
  mutation, dirty-only re-sort, ref-stable month reuse.
- MediaStore hoisted to App.svelte (or shared module singleton), consumed by
  Library and Sessions.
- `internal/thumb/sizes.go` — `preview=2560`, `large=4096`, drop `lightbox`.
- `internal/cli/thumbs.go` — extend the existing `regenerate` subcommand
  with `--owner <hub>:<user>` and `--all-owners` scope flags. Existing
  content selectors (`--all`, `--id`, `--type`, `--status`, `--since`)
  are unchanged.

**Not in scope (later F2.x)**

- Lightbox UI, zoom/pan, info panel, source ladder, video element, action
  cluster wiring → F2.4.
- GPS columns, EXIF GPS extraction, reverse geocoder → F2.1.
- Albums and sharing → F2.2.
- Hidden privacy gate → F2.3.
- AI panel content (tags/captions) → separate AI backend slice; not gated by
  any F2 sub-plan.
- Mobile bottom-sheet info panel → F2.4 mobile pass.

## 3. Architecture

Two parallel tracks executed under one plan. They share no code; they're
batched together because both are prerequisites for the lightbox and neither
is large enough to warrant its own spec.

### 3.1 Frontend track

```
App.svelte
  ├── router (singleton)              ← reads location, owns navigate()
  ├── ThemeStore (existing)
  ├── EventsStore (existing)
  ├── MediaStore (hoisted, NEW)       ← single instance for Library + Sessions
  └── snippet main {
        switch on router.current.route {
          case "library":  <Library {mediaStore} />
          case "sessions": <Sessions {mediaStore} />
          case "media":    <MediaDetail id={router.current.id} />  // F2.4 replaces with Lightbox
          case "settings": <Settings />
          default:         <NotFound />
        }
      }
```

Library and Sessions stop calling `new MediaStore(api)`. They accept a
`{ mediaStore }: { mediaStore: MediaStore }` prop. Tests construct their own
store and pass it in.

### 3.2 Backend track

```
internal/thumb/sizes.go          ← grid=256, preview=2560, large=4096
internal/thumb/worker.go         ← already loops over AllSizes(); just inherits
internal/thumb/raw.go            ← single decode → resize per AllSizes(); large
                                   is just another resize from the existing path
internal/cli/thumbs.go           ← extend existing `regenerate` with --owner/--all-owners
```

The schema policy is pre-prod squash (per `CLAUDE.md`): if a column or
constraint changes, edit `000001_initial_schema.{up,down}.sql` directly. F2.0
does NOT change schema — sizes are a Go-side vocabulary; thumb_version /
thumb_status columns already exist.

## 4. Components & interfaces

### 4.1 `frontend/src/lib/router/router.svelte.ts`

```ts
export type RouteMatch =
  | { route: "library" }
  | { route: "sessions" }
  | { route: "settings" }
  | { route: "media"; id: string }
  | { route: "notfound"; path: string };

export class RouterStore {
  current = $state<RouteMatch>(this.match(window.location.pathname));

  navigate(path: string) {
    history.pushState({}, "", path);
    this.syncFromLocation();
  }

  syncFromLocation() {
    this.current = this.match(window.location.pathname);
  }

  // Pathname-only match. Search and hash are ignored — /media/abc?x=1
  // matches /media/:id. Unknown pathname (with or without query) falls
  // through to NotFound.
  private match(pathname: string): RouteMatch { /* anchored regex table */ }
}

export const router = new RouterStore();
```

App.svelte's existing popstate effect calls `router.syncFromLocation()`.
Anchor click handlers in MediaCell + Sidebar + back links call
`router.navigate(href); e.preventDefault()`.

### 4.2 `frontend/src/routes/NotFound.svelte`

Renders a minimal "page not found" surface with a `<a href="/library">` back
link (which itself goes through `router.navigate`). No props. ~15 lines.

### 4.3 `frontend/src/routes/MediaDetail.svelte`

F2.0 stub that exists only so `/media/:id` is reload-safe and the
client-side router has a real component to mount on the matching route.
Accepts `{ id }: { id: string }` and renders a placeholder "Media {id}"
heading plus a back link to `/library`. ~15 lines. F2.4 replaces this file
with the lightbox shell.

### 4.4 `frontend/src/lib/grid/MediaCell.svelte`

**Presentational only.** No selection-store reads, no orderedIds, no range
logic, no navigation policy.

```svelte
<script lang="ts">
  import type { MediaLite } from "./monthChunkLayout";
  let { media, selected, onCellClick }: {
    media: MediaLite;
    selected: boolean;
    onCellClick: (e: MouseEvent) => void;
  } = $props();
  let imgError = $state(false);
</script>

<a
  href={`/media/${media.id}`}
  aria-label={`Photo ${media.id}`}
  class:selected
  onclick={onCellClick}
>
  {#if !imgError && media.thumbUrl}
    <img
      src={media.thumbUrl}
      alt=""
      loading="lazy"
      decoding="async"
      style="width:100%;height:100%;object-fit:cover"
      onerror={() => (imgError = true)}
    />
  {:else}
    <div class="placeholder" aria-hidden="true"></div>
  {/if}
</a>

<style>
  .placeholder {
    width: 100%; height: 100%;
    background: var(--bg-elevated);
    border-radius: 2px;
  }
  /* selected outline mirrors today's VirtualGrid styling */
</style>
```

Caller (VirtualGrid / Sessions) computes `selected={selection.ids.has(id)}` and
provides `onCellClick={(e) => handleCellClick(e, id)}`. Click policy stays in
the caller: button !== 0 ignored; shift → `selection.range(id, orderedIds)`;
ctrl/meta → `selection.toggle(id)`; otherwise `e.preventDefault(); router.navigate(...)`.

### 4.5 `frontend/src/lib/media/mediaStore.svelte.ts`

New private state:

```ts
private byMonth = new Map<string, Map<string, Media>>();
private byId = new Map<string, string>();        // id → current monthKey
```

`merge(items)` algorithm:

```
dirty = new Set<string>()
for each item:
  newKey = monthKey(item.taken)
  oldKey = byId.get(item.id)
  if oldKey && oldKey !== newKey:           // relocation
    byMonth.get(oldKey).delete(item.id)
    if byMonth.get(oldKey).size === 0:
      byMonth.delete(oldKey)
    dirty.add(oldKey)
  let inner = byMonth.get(newKey) ?? (set + new Map)
  inner.set(item.id, item)                  // replace OR insert
  byId.set(item.id, newKey)
  dirty.add(newKey)                         // any replacement / insert dirties

// Reactive snapshot: clean months reuse existing object refs so the keyed
// each-block doesn't re-render unaffected chunks.
prevByKey = new Map(this.months.map(m => [m.key, m]))
this.months = [...byMonth.keys()]
  .sort(desc)
  .map(k => dirty.has(k)
    ? { key: k, items: sortedItems(byMonth.get(k)) }
    : prevByKey.get(k)!)
```

Replacement semantics: a row whose timestamp/aspect/thumbUrl/thumb_version
changes (but stays in the same monthKey) replaces in `inner` and dirties the
month — even though the bucket is the same — because the row's identity
fields changed.

### 4.6 MediaStore hoist

`App.svelte`:

```svelte
<script lang="ts">
  import { MediaStore } from "./lib/media/mediaStore.svelte";
  import { router } from "./lib/router/router.svelte";
  import { api } from "./lib/api/client";

  const mediaStore = new MediaStore(api);
  mediaStore.loadInitial();
</script>
...
{#if router.current.route === "library"}
  <Library {mediaStore} />
{:else if router.current.route === "sessions"}
  <Sessions {mediaStore} />
{:else if router.current.route === "media"}
  <MediaDetail id={router.current.id} />
{/if}
```

Library and Sessions stop owning the lifecycle:

```svelte
<!-- Library.svelte -->
let { mediaStore }: { mediaStore: MediaStore } = $props();
```

The store survives nav. `Library.svelte` test files construct a MediaStore
explicitly and pass it in; nothing else changes.

### 4.7 `internal/thumb/sizes.go`

```go
const (
    SizeGrid    Size = "grid"
    SizePreview Size = "preview"   // 2560 (was 1024)
    SizeLarge   Size = "large"     // 4096 (new)
)

func (s Size) MaxEdge() int {
    switch s {
    case SizeGrid:    return 256
    case SizePreview: return 2560
    case SizeLarge:   return 4096
    }
    return 0
}

func ParseSize(s string) (Size, error) {
    switch s {
    case "grid":    return SizeGrid, nil
    case "preview": return SizePreview, nil
    case "large":   return SizeLarge, nil
    }
    return "", ErrUnknownSize
}

func AllSizes() []Size { return []Size{SizeGrid, SizePreview, SizeLarge} }
```

`SizeLightbox` is removed entirely. `?size=lightbox` HTTP requests now return
400. F1 frontend never used `lightbox`; the F2.4 lightbox uses `preview` (fit)
and `large` (1:1).

### 4.8 `internal/thumb/worker.go` and `raw.go`

The worker already iterates `AllSizes()` per claim and emits one keyed file
per size. F2.0 verification: confirm there's no hard-coded 2048 path. The RAW
decode path produces a single decoded image; resizing it to the new max-edge
values is identical to the existing flow — no new branch. Tests assert grid +
preview + large all emitted on a successful claim.

### 4.9 `internal/cli/thumbs.go` — `regenerate` subcommand

The existing subcommand already accepts `--all`, `--id`, `--type`,
`--status`, `--since` as **content selectors** and validates that at
least one is set (`internal/cli/thumbs.go:104`). F2.0 keeps that
contract verbatim and adds two **owner scope** flags. Owner scope and
content selection are independent dimensions:

- **Owner scope** (mutually exclusive; default = configured stub principal):
  - (no owner flag) — caller is the configured stub principal.
    Requires `identity.mode=stub` (existing precondition,
    `internal/cli/thumbs.go:86`).
  - `--owner <hub>:<user>` — that single principal. Bypasses the
    `identity.mode=stub` check (admin maintenance).
  - `--all-owners` — every principal returned by `owners.Repo.List(ctx)`.
    Bypasses the `identity.mode=stub` check (admin maintenance).
- **Content selectors** (existing; at least one required):
  - `--all`, `--id` (repeatable), `--type photo|video`,
    `--status pending|working|ready|failed|no_preview`,
    `--since RFC3339`.

For each owner in scope, F2.0 builds the same `EnqueueFilter` the F1
code already constructs (`internal/cli/thumbs.go:115`), substituting
the owner. `--all-owners` iterates owners and calls `Queue.Enqueue`
once per owner, summing the row counts.

Behavior matrix (illustrative; not exhaustive):

| Invocation | Identity requirement | Effect |
|---|---|---|
| `fotobank thumbs regenerate` | usage error | At least one content selector required (existing message). |
| `fotobank thumbs regenerate --all` | `identity.mode=stub` | All rows for the configured stub principal (existing). |
| `fotobank thumbs regenerate --status failed` | `identity.mode=stub` | Failed rows for the configured stub principal (existing). |
| `fotobank thumbs regenerate --owner local:alice` | none (admin maintenance) | usage error: a content selector is still required. |
| `fotobank thumbs regenerate --owner local:alice --all` | none (admin maintenance) | All rows for `local:alice`. |
| `fotobank thumbs regenerate --owner local:alice --status failed` | none (admin maintenance) | Failed rows for `local:alice`. |
| `fotobank thumbs regenerate --all-owners` | usage error | A content selector is required. |
| `fotobank thumbs regenerate --all-owners --all` | none (admin maintenance) | All rows for every principal in `owners.Repo.List(ctx)`. |
| `fotobank thumbs regenerate --owner foo --all` | usage error | Malformed owner; missing `:` separator. |
| `fotobank thumbs regenerate --owner foo:bar --all` | none | Valid format; `Enqueue` matches 0 rows; CLI prints `queued 0 rows for foo:bar`. Not an error. |
| `fotobank thumbs regenerate --owner X --all-owners --all` | usage error | `--owner` and `--all-owners` are mutually exclusive. |

`Enqueue` already does `thumb_version = thumb_version + 1,
thumb_status = 'pending'` in one statement
(`internal/thumb/queue.go:244`), so the upgrade flow is:

1. Operator runs `regenerate` with a content selector.
2. `Enqueue` immediately bumps `thumb_version` on matched rows.
3. Frontend `?v=N` URLs now mismatch → `404 Cache-Control: no-store`.
4. Worker drains pending rows in the background; new size files keyed by `v=N+1`.
5. `MarkReady` flips `thumb_status='ready'` (does not bump version).

Output: one `queued <n> rows for <hub>:<user>` line per principal
processed (existing single-principal output extended over the loop).

## 5. Data flow

### 5.1 Cold load `/media/abc`

```
browser → GET /media/abc
embed handler → not /api/, not /assets/, not real file → ServeFile index.html (200)
SPA boots → App.svelte → router.match(window.location.pathname) → { route: "media", id: "abc" }
MediaDetail (F2.0 stub, see §4.3) renders id={mediaId}
mediaStore.loadInitial() runs in parallel (App.svelte mounted it)
```

### 5.2 SPA nav `/library` → `/media/abc`

```
user clicks <a href="/media/abc"> in MediaCell
caller's onCellClick: e.preventDefault(); router.navigate("/media/abc")
router.navigate → history.pushState; this.current = match("/media/abc")
App.svelte re-renders main column with MediaDetail
mediaStore is preserved (App.svelte never unmounts it)
selection store is preserved
EventsStore EventSource is preserved
```

### 5.3 `/media/abc?return=library` (with query)

Router strips search and hash before matching. `?return=library` is
ignored by the matcher; future routes that need query params (e.g.
`/search?q=...`) will read `window.location.search` directly.

### 5.4 MediaStore.merge after `/api/v1/media/:id` PUT

```
client PUT /api/v1/media/abc with a new timestamp succeeds
client refetches → receives the updated media row (with `timestamp` field)
                 → mediaStore.merge([row])
byId.get("abc") = "2026-04"; new monthKey = "2025-12"
byMonth["2026-04"].delete("abc"); dirty += "2026-04"
if byMonth["2026-04"].size === 0: prune
byMonth["2025-12"].set("abc", row); dirty += "2025-12"
months snapshot rebuilt; "2026-04" and "2025-12" rebuilt; everything else
  reuses existing object refs — VirtualGrid's keyed each-block doesn't
  re-render unaffected chunks
```

### 5.5 Operator regenerate

```
operator runs: fotobank thumbs regenerate --all-owners --all
CLI: owners := owners.Repo.List(ctx)
for o in owners: Queue.Enqueue(EnqueueFilter{Owner: o, All: true})
each Enqueue: UPDATE media SET thumb_version = thumb_version + 1, thumb_status = 'pending' WHERE owner = ?
the running fotobank server's thumb worker drains pending rows; emits 3 sizes per claim
frontend: cached <img src=...?v=N> 404s in any open tab; MediaCell falls back to placeholder
the open tab keeps its placeholder until the operator reloads it (full
  re-fetch hits v=N+1)
```

F2.0 does NOT add an automatic refresh path. The SSE bus is populated
by the import pipeline, not by thumb regeneration, so the running
client never learns the row's `thumb_version` changed. Open tabs
holding cached `?v=N` URLs remain on placeholders until reload. This
is documented as a runbook caveat in §8.

## 6. Error handling

**Frontend**

- Router: unknown pathname → `NotFound` route. Never throws; never crashes the app.
- `navigate("/garbage path")` → still pushState; matcher resolves to `notfound`.
- `MediaStore.merge`: invalid rows already filtered by F1's `toMedia` (no
  change). Replacement of an existing id with new fields → dirties month.
  Cross-month timestamp change → relocates and dirties both months.
- `MediaCell` img `onerror` → flips `imgError = true`; renders neutral
  placeholder div. The cached F1 `class="placeholder"` background is reused.
- MediaStore hoisted to App.svelte: if a route mounts before `loadInitial`
  resolves, it sees `months: []` and renders the existing F1 empty-state UI.
  No new race guards.

**Backend**

- `regenerate` with no content selector → usage error, exit code 2
  (existing behavior preserved).
- `regenerate --owner local:alice` (no content selector) → usage error.
- `regenerate --all-owners` (no content selector) → usage error.
- `regenerate --owner foo --all` (missing `:`) → usage error:
  `owner must be hub:user`.
- `regenerate --owner foo:bar --all` for an owner whose row set is
  empty → 0 rows queued; exit 0.
- `regenerate --all-owners --all` against an empty `owners` table →
  `no owners found`; exit 0.
- `regenerate --owner ... --all-owners ...` → usage error
  (mutually exclusive scope flags).
- `regenerate --all` (or any selector) without `--owner`/`--all-owners`
  outside stub mode → existing usage error: `requires identity.mode=stub`
  (`internal/cli/thumbs.go:86`).
- Worker decode failures on regenerated rows → existing `failed` /
  `no_preview` transitions cover; F2.0 doesn't change failure paths.

## 7. Testing

**Frontend (vitest)**

- `router.test.ts` (new): `match("/")`, `/library`, `/sessions`, `/settings`,
  `/media/abc`, `/media/abc?x=1` (matches; query stripped), `/media/abc/extra`
  (NotFound), `/foo` (NotFound), `/foo?bar=baz` (NotFound). `navigate()`
  updates `current`. `syncFromLocation()` re-reads `window.location`.
- `mediaStore.test.ts`: keep all 9 existing tests; add 3:
  1. Replacement same month with changed thumb_version dirties month and
     updates fields.
  2. Replacement crossing months relocates and dirties both buckets.
  3. Re-merging identical inputs leaves all unaffected month object refs
     stable (snapshot identity check).
- `mediaCell.test.ts` (new): props produce expected DOM (anchor href, img
  src, class:selected, aria-label); `onerror` swaps to placeholder div.

**Frontend (Playwright extensions)**

- Reload `/media/abc` → 200 + SPA shell + matched route.
- Reload `/foo-not-a-route` → 200 + SPA shell + NotFound.
- Reload `/api/v1/healthz` → API JSON response, NOT the SPA shell.
- SPA nav `/library → /sessions → back`: Library's initial fetch
  (`limit=200&offset=0`) is observed once; Sessions does not re-issue that
  initial fetch; `back` does not re-issue it. Pagination sentinels are
  permitted to fire and don't count.

**Backend (Go)**

- `internal/thumb/sizes_test.go`: extend `TestParseSizeValid` for `large`;
  remove `lightbox` cases; assert `ParseSize("lightbox")` returns
  `ErrUnknownSize`.
- `internal/thumb/worker_test.go` (or per-claim emission test): assert
  `grid` + `preview` + `large` all emitted on a successful claim. Uses real
  decode + storage (existing test harness).
- `internal/cli/thumbs_test.go`: keep all existing selector tests
  (`--all`, `--id`, `--type`, `--status`, `--since`, RFC3339 parse
  errors). Add:
  - `regenerate --owner local:alice --all` against a fixture seeded with
    rows for `local:alice` and rows for a different owner — asserts only
    `local:alice` rows transition to `pending` and bump `thumb_version`.
  - `regenerate --all-owners --all` against `testutil.OpenTestDB(t)` seeded
    with 3 owners + N rows each — asserts every row transitions to
    `pending` and `thumb_version` increments by exactly 1 per row. Does
    NOT spin up a worker (worker test's responsibility).
  - `regenerate --owner local:alice --status failed` — confirms owner
    scope and content selector compose; only failed rows for that owner
    are enqueued.
  - Usage-error tests: `regenerate --owner local:alice` (no content
    selector), `regenerate --all-owners` (no content selector),
    `regenerate --owner foo --all` (malformed), `regenerate --all`
    outside stub mode (existing), `regenerate --owner X --all-owners --all`
    (mutually exclusive).
- `internal/httpapi/media_thumb_test.go`: `?size=lightbox` → 400; add
  `?size=large&v=N` happy path.

## 8. Risk

- **MediaStore hoist is a behavior change.** Library and Sessions today own
  store lifecycle; F2.0 removes that. Mitigation: explicit task with the
  no-refetch Playwright assertion. The plan calls out the new component
  contract.
- **Regenerate cache-bust window.** Once `Enqueue` bumps `thumb_version`,
  cached `<img src=...?v=N>` URLs in any open tab return 404 and fall back
  to MediaCell's placeholder. The hoisted MediaStore keeps those cached
  rows in `byMonth` indefinitely — F2.0 has no SSE event or polling path
  that swaps `?v=N` for `?v=N+1`. Open tabs stay on placeholders until the
  user reloads the page (or until a future media-list refetch from a
  different code path lands). Runbook: regenerate during low-use hours
  and tell active operators to reload tabs after the worker drains.
- **`large=4096` wall-clock.** Decoding RAW + resizing to 4096 is ~1s/row on
  Apple Silicon. Per-row total time roughly doubles vs grid+preview. Not a
  correctness risk; flag in the runbook.
- **Anchored regex routes.** The matcher must use `^...$` for each pattern so
  `/media/abc/extra` doesn't match `/media/:id`. Tests above lock this.
- **Removed `SizeLightbox`.** Any external bookmarked URL using
  `?size=lightbox` starts returning 400. F1 frontend never used it; the only
  consumer would be a future external tool. Plan accepts the clean cut.

## 9. Dependencies on later sub-plans

- **F2.1 GPS Metadata** is independent; can run in parallel.
- **F2.2 Albums + Sharing** is independent; can run in parallel.
- **F2.3 Hidden Privacy** is independent; can run in parallel.
- **F2.4 Lightbox Viewer** depends on F2.0 (router for sub-route, MediaStore
  for prev/next perf, MediaCell for the lightbox-strip), F2.1 (info panel
  location), F2.2 (action cluster Add/Share), F2.3 (action cluster Hide), and
  optionally an AI backend slice (info panel AI sections).

## 10. Out-of-scope reminders

- Lazy/code-split route imports — defer to when MapLibre or another heavy
  view lands (F2.1 GPS map, possibly).
- Auto-detect-on-boot regenerate — explicitly rejected; `regenerate` is an
  operator action.
- AI panel content / embeddings / search index — separate AI backend slice;
  not gated by any F2 sub-plan.
- Mobile bottom-sheet info panel — F2.4.
