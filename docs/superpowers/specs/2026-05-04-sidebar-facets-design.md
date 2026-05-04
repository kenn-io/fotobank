# Sidebar Facets Design

**Date:** 2026-05-04
**Status:** Spec draft
**Scope:** v1 — Camera, Lens, Tags (AI), Has-GPS, Media-type facets in a sidebar `FILTERS` group, with chip-strip refinement above the photo grid. Deferred to v2: /hidden + /albums/:id, ISO/aperture/focal-length range filters, persistent saved filters.

## Goal

Sub-library navigation by EXIF + AI tags. Click `Sony A7R IV` in the sidebar, the library narrows. Add `tag: dog`, narrows further. Active filters render as removable chips above the grid. Counts in the sidebar reflect what's reachable given the *other* active filters (Lightroom semantics).

## Architecture (one paragraph)

URL is the single source of truth for active filters. Sidebar `FILTERS` group and chip strip both derive from `router.current` and write back via `router.navigate(...)`. A new `GET /api/v1/facets` endpoint returns counts per facet for the current filter context, computing five "exclude-self" aggregations server-side so each dropdown shows reachable alternatives. Three Go filter paths (`Repo.List` for /library, `Repo.ListGeo` for /map, `hybrid.Resolve` for /search and /facets) each grow the same fields independently — refactor to a shared helper deferred until pain emerges.

## UI shape

### Sidebar — single `FILTERS` group

Appended after `MANAGE` in `Sidebar.svelte`:

```
FILTERS                    Clear
  CAMERAS  ▾    1247
    [search 🔍]              ← shown when items > 8
    ☐ Sony A7R IV       845
    ☐ iPhone 15 Pro     302
    ☐ Canon EOS R5      100
    …

  LENSES   ▾    873
    …

  TAGS     ▾   2104
    …

  PLACES   ▸             ← collapsed; hidden entirely on /map

  MEDIA TYPE  ▾  2814
    ☐ Photo           2645
    ☐ Video            169
```

Behavior:

- Each facet sub-section is independently collapsible. Collapsed/expanded state persists in `localStorage`.
- Collapsed headers show an active-count badge when filters are set (`CAMERAS ▸ 2`) so collapsed filters never become invisible state.
- Multi-select within a group with **OR** semantics. Multi-group AND. (Media Type is single-select-or-none in practice — selecting both photo + video is equivalent to no filter — but it follows the same multi-select component for consistency.)
- Counts are *contextual* and *exclude the facet's own selection* — when Sony is selected, the Camera dropdown still shows `Canon (200)` so swapping is one click. (Lightroom rule.)
- Item search appears at >8 items. Substring/`includes` match (matches agentsview's mental model and is better for lens names with useful tokens in the middle). Media Type's two-row body never gets a search input.
- `Clear` link next to the `FILTERS` group label appears when any filter is active; clears all facets at once.
- The Places sub-section is hidden entirely on `/map` (the route's contract is geotagged-media; HasGPS is implicitly forced true).

### Chip strip — above the photo grid

Mounted in `/library`, `/search`, `/map`:

```
Filters:  [Sony A7R IV ×]  [tag: Dog ×]  [Has GPS ×]              Clear all
```

- One chip per active filter *value* (multi-selected `Sony` + `Canon` = two chips).
- Click `×` removes that one filter; sidebar checkbox unticks in sync.
- `Clear all` appears when ≥2 chips active.
- Tag chips display the canonical `tag_label`, not the `tag_key`.

### Empty state

When filters yield zero photos, the grid shows:

> No photos match these filters.
> [Clear all]

(rather than just a blank grid).

## Backend

### Filter input extension

Three structs gain the same fields:

```go
// internal/media/repo.go — ListFilter (used by Repo.List → /library)
// internal/media/repo.go — ListGeoFilter (used by Repo.ListGeo → /map)
// internal/search/hybrid/filter.go — Input (used by hybrid.Resolve → /search and /facets)

Cameras     []string   // each value is "make + ' ' + model"
Lenses      []string   // each value is lens_model verbatim
AnyTagKeys  []string   // OR-composed, single EXISTS with tag_key IN (?, ?...)
HasGPS      *bool      // tri-state: nil=any, true=geotagged, false=no-GPS
```

Notes:

- `MediaType` already exists on `ListFilter` and `hybrid.Input`; **add** to `ListGeoFilter`.
- `TagKeys` (AND-composed) stays on `hybrid.Input` for the existing typed-chip resolver path on /search. The sidebar facet uses the new `AnyTagKeys` field. The two are independent — a /search request can carry both.
- `HasGPS` is **not** added to `ListGeoFilter`. /map's contract is geotagged-only; the HasGPS facet is hidden in the sidebar UI when route is /map.

### Predicate shapes

```sql
-- Cameras (AnyOf)
(make || ' ' || model) IN (?, ?, ...)

-- Lenses (AnyOf)
lens_model IN (?, ?, ...)

-- AnyTagKeys (OR-composed; single EXISTS)
EXISTS (
  SELECT 1 FROM media_tags mt
  JOIN ai_results r ON mt.result_id = r.id
  WHERE r.media_id = m.id
    AND r.task = 'tag' AND r.status = 'active'
    AND mt.tag_key IN (?, ?, ...)
)

-- HasGPS=true
latitude IS NOT NULL AND longitude IS NOT NULL

-- HasGPS=false
(latitude IS NULL OR longitude IS NULL)
```

### Indexes

Add to `internal/db/migrations/000001_initial_schema.up.sql` (and matching `.down.sql`):

```sql
CREATE INDEX media_owner_camera_visible_idx
  ON media(owner_hub, owner_user_id, (make || ' ' || model))
  WHERE hidden_at IS NULL AND paired_with_id IS NULL
    AND make IS NOT NULL AND model IS NOT NULL;

CREATE INDEX media_owner_lens_visible_idx
  ON media(owner_hub, owner_user_id, lens_model)
  WHERE hidden_at IS NULL AND paired_with_id IS NULL
    AND lens_model IS NOT NULL;
```

`media_tags(tag_key)` already covered by the existing `media_tags_key_idx`. Latitude/longitude is sparse enough that `IS NOT NULL` predicates plan well without an index — verify via `EXPLAIN QUERY PLAN` during implementation; add a partial GPS index if needed.

### `GET /api/v1/facets`

New huma route in `internal/httpapi/facets.go`. Accepts the union of v1-relevant filter params from /library/search/map (subset depending on what the calling route accepts).

**Filter params honored** (full FilterCTE context, so sidebar counts on a route match the visible-result context for chip-shaped state):

- New facet params: `camera`, `lens`, `facet_tag` (→ `AnyTagKeys`), `has_gps`, `media_type`.
- /search-only existing params: `date_after`, `date_before`, `tag` (→ AND-on-labels `TagKeys`), `location`, `include_hidden`.

**Filter param NOT honored — `q` (the FTS lexical query) on /search.** `q` drives BM25/ANN scoring in the hybrid engine, not the FilterCTE. Plumbing it into /facets would turn /facets into "search-with-aggregation" and add latency for every dropdown open. **For v1**, sidebar counts on `/search?q=mountain` reflect "photos matching the FilterCTE", not "photos matching q AND the FilterCTE". The chip strip remains exact for the chips themselves; the slight overstatement only affects the in-dropdown counts. Revisit in v2 if the discrepancy is noticed.

Response:

```json
{
  "cameras":     [{"value": "Sony A7R IV", "count": 845}, ...],
  "lenses":      [{"value": "FE 24-70mm F2.8 GM", "count": 412}, ...],
  "tags":        [{"key": "dog", "label": "Dog", "count": 234}, ...],
  "places":      {"with_gps": 2103, "without_gps": 567},
  "media_types": [{"value": "photo", "count": 2645}, {"value": "video", "count": 169}]
}
```

- Top **200 per facet** by count desc, then value asc as tiebreaker (deterministic ordering for cache stability). Media Types is always two rows max so the limit is moot there.
- Each facet's count is computed against a FilterCTE built **without that facet's own selection** but with all other facets applied (the exclude-self rule).
- Tags include both `tag_key` (canonical, used as URL param value) and `tag_label` (display).
- `places.with_gps` / `without_gps` honor the other non-facet-self filters.

### Aggregation SQL shape

`hybrid.Resolve` projects only `(id, timestamp, imported_at)` from the `filter` CTE — aggregations needing EXIF columns must JOIN `media` back by id, and tag aggregations must JOIN through `ai_results` + `media_tags`. The five queries (one per facet, each built from a FilterCTE that *omits the facet's own selection*) look like:

```sql
-- Cameras
WITH filter AS ( /* hybrid.Resolve output without Cameras applied */ )
SELECT (m.make || ' ' || m.model) AS value, COUNT(*) AS count
FROM filter f
JOIN media m ON m.id = f.id
WHERE m.make IS NOT NULL AND m.model IS NOT NULL
GROUP BY value
ORDER BY count DESC, value ASC
LIMIT 200;

-- Lenses
WITH filter AS ( /* without Lenses applied */ )
SELECT m.lens_model AS value, COUNT(*) AS count
FROM filter f
JOIN media m ON m.id = f.id
WHERE m.lens_model IS NOT NULL
GROUP BY value
ORDER BY count DESC, value ASC
LIMIT 200;

-- Tags
WITH filter AS ( /* without AnyTagKeys applied */ )
SELECT mt.tag_key AS value, MAX(mt.tag_label) AS label, COUNT(DISTINCT f.id) AS count
FROM filter f
JOIN ai_results r ON r.media_id = f.id
                 AND r.task = 'tag' AND r.status = 'active'
JOIN media_tags mt ON mt.result_id = r.id
GROUP BY mt.tag_key
ORDER BY count DESC, value ASC
LIMIT 200;

-- Places
WITH filter AS ( /* without HasGPS applied */ )
SELECT
  COUNT(*) FILTER (WHERE m.latitude IS NOT NULL AND m.longitude IS NOT NULL) AS with_gps,
  COUNT(*) FILTER (WHERE m.latitude IS NULL OR m.longitude IS NULL) AS without_gps
FROM filter f
JOIN media m ON m.id = f.id;

-- Media Types
WITH filter AS ( /* without MediaType applied */ )
SELECT m.media_type AS value, COUNT(*) AS count
FROM filter f
JOIN media m ON m.id = f.id
GROUP BY value
ORDER BY count DESC;
```

Tags uses `COUNT(DISTINCT f.id)` because one media row can carry multiple tags (the JOIN otherwise inflates the count). `MAX(mt.tag_label)` is a deterministic-but-arbitrary pick when multiple labels share a key — in practice each `tag_key` has one canonical label, but the MAX guards the invariant.

### Service layer

New `FacetService` at `internal/service/facets/` — owner-scoped, takes `ActiveFilters`, returns the response struct above. Sits next to existing `internal/service/search/`. The five sub-aggregations build separate `hybrid.Input` values (each missing one facet's selection), call `hybrid.Resolve` for each, and run the SQL above for that facet.

## Frontend

### URL params (multi-value via repeat)

```
?camera=Sony+A7R+IV&camera=iPhone+15+Pro
?lens=FE+24-70mm+F2.8+GM
?facet_tag=dog&facet_tag=cat            ← OR (binds to AnyTagKeys)
?has_gps=1                                ← 1 / 0 / absent
?media_type=photo
```

`facet_tag` is a distinct param from existing `?tag=` (which drives the typed-chip AND-on-labels path on /search). Both can coexist on /search.

### Router shape extensions

`router.svelte.ts` `RouteMatch` discriminated union:

```ts
| { route: "library";
    camera?: string[]; lens?: string[]; facet_tag?: string[];
    has_gps?: boolean; media_type?: "photo" | "video"; }
| { route: "map";
    z?: number; c?: [number, number]; focus?: string; tab?: "map" | "photos";
    camera?: string[]; lens?: string[]; facet_tag?: string[]; media_type?: "photo" | "video"; }
    // No has_gps on /map (forced true)
| { route: "search";
    q?: string; sort?: SearchSort;
    date_after?: string; date_before?: string;
    tag?: string[];                         // existing — AND on labels
    location?: string; media_type?: "photo" | "video"; include_hidden?: boolean;
    camera?: string[]; lens?: string[]; facet_tag?: string[]; has_gps?: boolean; }
```

Each route's `build()` callback parses the new params from `URLSearchParams`. Multi-value via `URLSearchParams.getAll()`.

### Active-filters helper

`frontend/src/lib/filters/activeFilters.ts`:

```ts
export type ActiveFilters = {
  cameras: string[];
  lenses: string[];
  tagKeys: string[];           // sourced from facet_tag (NOT tag)
  hasGps: boolean | null;      // null = any
  mediaType: "photo" | "video" | null;
};

export function fromRoute(r: RouteMatch): ActiveFilters;
export function withToggled(f: ActiveFilters, group: FacetGroup, value: string): ActiveFilters;

// Merge new filter values into existing URL params, preserving non-filter keys.
// (q, sort, date_after on /search; z, c, focus, tab on /map.)
export function withFilters(current: URLSearchParams, f: ActiveFilters): URLSearchParams;
```

`withFilters` strips only the known filter param keys (`camera`, `lens`, `facet_tag`, `has_gps`, `media_type`) before re-applying — never clears unrelated state.

### Components

**Ported from agentsview (re-themed to fotobank tokens — `--surface`, `--ink`, `--amber`, `--border`, etc.):**

- `frontend/src/lib/filters/FacetList.svelte` — search input + scrollable checkbox list. Adapted from agentsview's `FilterDropdown.svelte`, but **inline** (not a popover). Props: `items: {value, label, count, selected}[]`, `onToggle(value)`, `searchPlaceholder`.
- `frontend/src/lib/filters/FilterChipStrip.svelte` — chip row pattern from agentsview's `SessionActiveFilters.svelte`.

**New fotobank components:**

- `frontend/src/lib/filters/FilterSidebar.svelte` — the FILTERS group containing four `FacetSection`s. Mounted from `Sidebar.svelte`. Reads `ActiveFilters` from route, writes via navigate. Receives `facetsResponse` from the store.
- `frontend/src/lib/filters/FacetSection.svelte` — collapsible header (chevron + label + total count + active-count badge when collapsed) wrapping a `FacetList`. Manages its own collapsed state via `localStorage` key.
- `frontend/src/lib/filters/facetsStore.svelte.ts` — fetches `/api/v1/facets`, caches by full `(route + ActiveFilters)` key. Single response per fetch (the backend handles exclude-self internally). Debounced 100ms on filter changes.

**Existing components touched:**

- `Sidebar.svelte` — append `FilterSidebar` after `MANAGE`. Hide the Places sub-section when route is /map (passed via prop).
- `Library.svelte` — mount `FilterChipStrip` above the grid; pass `ActiveFilters` to `mediaStore` fetch calls. On filter-key change, trigger the store's reset path (see below).
- `Search.svelte` — same; preserve existing q/sort UI. searchStore already resets on q change; extend the reset trigger to include the new facet keys.
- `Map.svelte` — same; preserve existing z/c/focus/tab state. The existing geo store is one-shot per page-load, so on filter-key change it must be re-fetched (no pagination to reset).
- `mediaStore.svelte.ts` — accept filter params and implement the **filter-change reset protocol**:
  1. Compute a stable `filterKey` from `ActiveFilters` (deterministic JSON or sorted-tuple hash).
  2. When `filterKey` changes:
     - Increment an internal `fetchToken` (e.g. `Symbol()` or monotonic int).
     - Cancel/abort any in-flight fetch (or tag responses with the token at issue time and discard responses whose token ≠ current).
     - Clear the months map and any per-month index caches.
     - Reset pagination cursor to the start.
     - Set `loading=true`, then issue the new fetch with the current params.
  3. Late responses tagged with stale tokens are dropped silently — they never write to the months map.

  Without this protocol, /library can interleave responses from a prior filter with the new filter, producing a grid that mixes old + new results.

## Routes covered

| Route        | v1 | Notes |
|--------------|----|----|
| /library     | ✅ | Today has zero filter params; this is the biggest extension. |
| /search      | ✅ | Add `camera`/`lens`/`facet_tag`/`has_gps` alongside existing `tag`. |
| /map         | ✅ | Add `camera`/`lens`/`facet_tag`/`media_type`. HasGPS forced true; Places sub-facet hidden in sidebar. |
| /hidden      | ❌ v2 | Uses separate `Repo.ListHidden` data path (#4) — out of v1 scope. |
| /albums/:id  | ❌ v2 | Album-scoped data path; not yet through the shared filter machinery. |

## Performance

- Backend: each /facets call runs **5 aggregation queries** — 4 GROUP BY (Cameras, Lenses, Tags, Media Types) + 1 with two conditional counts (Places, with_gps / without_gps). With proper indexes and partial WHERE clauses, each is sub-100ms on a 100k library.
- Client: 100ms debounce on filter changes prevents redundant fetches when the user multi-clicks. Per-context cache (full `route + ActiveFilters` key) means navigating back to a previous filter combination is instant.
- Backend LRU cache: deferred. The 100ms debounce + partial indexes should be enough; revisit if measurements say otherwise.

## Out of scope (v2 candidates)

- /hidden + /albums/:id facet support (extends `Repo.ListHidden` and album-scoped repo path).
- ISO / aperture / focal-length range sliders.
- Saved/named filter presets ("My A7R IV portraits 2024").
- Year/month sub-facet (the YearScrubber already covers this dimension).
- MIME-type facet (sub-granular relative to media_type photo/video).
- Location-label facet (already on /search via `?location=`).

## Testing strategy

- Backend unit tests: each filter struct's `Apply*` helper given various combinations; FilterCTE output verified against expected SQL (existing `filter_test.go` pattern); /facets handler returns expected counts on a seeded fixture (camera A: 5, camera B: 3, with mixed tags).
- Backend integration: spin up `testutil.OpenTestDB`, seed 50 mixed-EXIF rows, hit /facets with a few filter combos, assert exclude-self counts.
- Frontend unit: `activeFilters.ts` round-trips (URL → ActiveFilters → URL); `withFilters` preserves non-filter keys; `FacetList` emits correct toggle events.
- Playwright e2e: add to existing /library spec — set up fixtures with 3 cameras + 2 lenses + tags, exercise sidebar checkboxes, assert chip strip + URL state + result counts.

## Open questions / decisions to lock during planning

- **`tag_key` vs `facet_tag` URL param name.** Spec uses `facet_tag`; if the existing `?tag=` is renamed/deprecated in a separate cleanup, we'd switch the sidebar to plain `?tag=`. For v1 we treat them as separate.
- **Facet ordering tiebreaker.** Spec says count desc, value asc. Confirm during plan.
- **Maximum facet items returned.** Spec says 200. If a library has >200 lenses, the dropdown's substring search still works because the search runs server-side... actually it doesn't — the spec returns top-200 only. **Decision:** for v1, top 200 client-side substring search. If a 201st lens is the user's target, they fall back to /search's typed query. Revisit if anyone hits this in practice.
