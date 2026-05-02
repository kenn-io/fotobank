# Fotobank Single-Player Polish — Design Spec

**Status:** Draft
**Date:** 2026-05-02
**Scope:** Three single-player gaps before SD-card dogfooding — `/map` view (with lightbox integration), sharing-UI flag-gate, and a README refresh.

This spec resolves the gaps called out in the post-search-sprint review against `2026-04-22-fotobank-vision.md` and the product principles in `~/.claude/projects/-Users-wesm-code-fotobank/memory/project_product_principles.md`. Watched-folder import (vision Phase 2) and Lightroom-coexistence verification (manual QA, fits in dogfooding) are explicitly out of scope.

---

## 1. Goal

Close the visible v1 product gaps before the maintainer dogfoods on a real photo library:

1. **Map view.** "Map-based visualization of geotagged photos" is listed in the product principles as a v1 core workflow. EXIF GPS extraction (`internal/geo/`) and storage (`media.latitude`, `media.longitude`, `media.location_label`) are already in place; only the UI is missing.
2. **Sharing UI flag-gate.** The sharing surface (UI components, `/shares` route, "Share" CTAs) is functional only against the stub broker. For single-player QA it adds noise. Hide it by default; keep all backend share APIs and CLI verbs unchanged.
3. **README refresh.** Five specific drifts (TOML vs YAML, CGO posture, `-tags sqlite_fts5`, status section, command list).

## 2. Scope

**In scope:**
- New `/map` SPA route with split-pane layout (map left, virtualized photo grid right), Leaflet 1.9 + OSM tiles.
- New backend endpoint `GET /api/v1/media/geo` returning the full owner-scoped geotagged-media DTO list.
- `/map` joins `library | sessions | album | hidden | search` as a lightbox source (`from=map`, snapshot, `LightboxSource` variant, `Lightbox.svelte` and `LightboxActions.svelte` source-switch branches).
- New `LightboxMapPin` component inside `LightboxMetadata` for any photo with GPS.
- New `[ui].sharing_enabled` TOML config (default `false`) plumbed via `httpapi.Deps.SharingEnabled` to `/api/v1/me.features.sharing_enabled`, consumed by a new frontend `appConfig` store.
- Frontend gating of: Sidebar Shares entry, MediaActions Share, AlbumDetail Share Album, ShareDrawer/ShareModal mounting, `/shares` route redirect, copy adjustment for album-delete-blocked-by-CLI-share.
- README surgical fixes (five locations).
- Test coverage (unit + Playwright e2e) for all of the above.

**Out of scope:**
- Watched-folder import (vision Phase 2 — deferred with sharing).
- Lightroom-coexistence verification (manual QA, fits in dogfooding step after this sprint).
- Map ↔ search-store filter integration (the search filter chips do not drive the map in v1; the map shows all geotagged photos for the owner).
- Map cluster-filter URL persistence / reconstruction across lightbox round-trips (cluster IDs are not stable across zoom; v1 trade is `z/c` URL state only).
- Per-photo GPS redaction for shared originals (vision §16.7 — future).
- Direct-reload reconstruction for `/media/:id?from=map` — falls back to `DirectMediaDetail`.
- Tile-server failover, self-hosted tile mirror, or non-OSM tile providers (the `tiles.ts` indirection is the seam, not the feature).

## 3. Map view

### 3.1 Page UX

**Route:** `GET /map`. Existing `AppHeader` and `Sidebar` retained; a new "Map" entry appears in `Sidebar` between "Sessions" and "Albums".

**Layout breakpoint** is computed against the available main-area width (viewport minus the fixed 220px sidebar), not raw viewport width:

- `min-width: 1024px` (main-area ≈ 804px or wider) → **split view**, 60/40 map/grid.
- below that → **tabs**, two tabs `Map | Photos`, default `Map`. Tab state optionally encoded as `?tab=photos` on mobile.

The map container uses an explicit stable height — `100% of available pane` in split view, `100vh - header - tabs` in mobile. `map.invalidateSize()` runs after: route mount, tab switch, hidden-lock-strip mount/unmount, and any pane resize observed via `ResizeObserver`.

**Map pane:**

- Leaflet 1.9 + `leaflet.markercluster`.
- Tile URL and attribution string live in `frontend/src/lib/map/tiles.ts`; the page imports `tileUrl()` and `attribution()`. Default tile URL: `https://tile.openstreetmap.org/{z}/{x}/{y}.png`. Attribution string: `'© OpenStreetMap contributors'`. The OSMF tile-usage policy recommends not hardcoding the URL so that providers can move; this module is that indirection.
- Initial view priority: (1) `?focus=<id>` → zoom 14 centered on that photo's coords (§3.3); (2) else `?z=<zoom>&c=<lat,lon>` if both are present → use those; (3) else auto-fit to the bounding box of the `/api/v1/media/geo` response (`map.fitBounds(bounds, {animate: false})` on first paint, animated on subsequent fits).
- Subsequent in-page interactions remember zoom and center as `?z=<zoom>&c=<lat>,<lon>`, debounced 300ms, written via `replaceState` so the back button does not replay every pan tick.
- Cluster style: numbered count cluster (red bg, white text) at zoom-out levels; plain circular markers (12px, owner accent color) when zoomed in past the breakpoint.
- Selected cluster highlight: orange ring on the cluster element.

**Cluster click and pan/zoom semantics:**

- **Click cluster** → `cluster.zoomToBounds()` (Leaflet's built-in fit-cluster-bounds animation); right-grid filters to that cluster's media IDs; clear-filter chip appears at the grid header.
- **Click marker** → opens lightbox at that photo (§3.2).
- **Click empty map area** → clears any cluster filter; grid reverts to "photos in current viewport."
- **Pan/zoom (without click)** → grid auto-updates to the bbox-visible IDs, debounced 200ms. Any cluster filter is silently cleared on pan; the clear-filter chip is the affordance, no toast.

**Grid pane:**

- Reuses the existing `VirtualGrid` + `MediaCell`. Density is owned by a separate key `density.map` so changing map-grid density does not change Library density.
- Header strip: photo count (`<n> photos`) + clear-filter chip when a cluster is selected.
- Click photo → opens lightbox with `from=map` (§3.2).

**Empty states:**

- No geotagged photos for this owner: full-pane message: "No geotagged photos in your library yet. Photos with GPS metadata will appear here as you import."
- No photos in current viewport: in-grid message: "No photos in view." subtitle: "Pan or zoom to find your photos."
- Initial fetch in flight: centered skeleton "Loading your photo locations…".
- `/api/v1/media/geo` failure: error toast + Retry button; suppress empty-library text in this state.
- Tile-server failure: rely on Leaflet's default broken-tile gray for individual tiles. We do not surface user-visible errors for individual tile failures (noisy, not actionable).

**Hidden inclusion:**

- `/api/v1/media/geo` returns visible-only by default.
- When the request carries a valid hidden-unlock claim AND the user toggles "Include hidden" in the map header, the SPA refetches with `?include_hidden=true`.
- The toggle defaults to off on every navigation to `/map`. It is not URL-persisted. An ambient unlock claim does not silently change map contents.
- Toggle is only visible when `hiddenStore.unlocked` is true.

### 3.2 Lightbox integration

**`LightboxSource` extension** (`frontend/src/lib/lightbox/lightboxSession.svelte.ts`):

```ts
export type LightboxSource =
  | { kind: "library" }
  | { kind: "sessions" }
  | { kind: "album"; albumId: string }
  | { kind: "hidden" }
  | { kind: "search" }
  | { kind: "map" };          // new
```

No fields on the variant — reconstruction state lives in `returnHref`.

**Snapshot built on photo click in the map's grid pane:**

```
source             = { kind: "map" }
includeHidden      = true iff the geo response that produced this set
                     was fetched with include_hidden=true
navIds             = orderedGridIds when selection.ids.size <= 1
                       OR  !selection.ids.has(clickedId)
                     orderedGridIds.filter(id => selection.ids.has(id))
                       when selection.ids.size > 1
                            && selection.ids.has(clickedId)
selected           = selection.ids.size > 1 && selection.ids.has(clickedId)
returnHref         = `/map?z=<current zoom>&c=<lat,lon>`
                       — cluster filter NOT encoded (see §3.2 trade)
returnFocusMediaId = clicked id
scrollY            = grid pane scrollY (the grid is the scrollable
                     container in split view, not the page)
```

`includeHidden` rides the snapshot so the lightbox can decide whether opening a hidden row from `from=map` is a leak (it is not, when the geo response was fetched explicitly with hidden inclusion).

**Cluster filter is not preserved on lightbox return.** Cluster identity is computed by `leaflet.markercluster` from the current zoom level; encoding cluster bounds and re-applying them is reconstruction code with edge cases. v1 trade: when the lightbox closes, the URL navigates back to `/map?z&c`, the viewport reloads the same set of photos, and `scrollY + returnFocusMediaId` puts the user back on the photo they came from in the grid. If real-use feedback shows this grates, revisit.

**Source-switch branches:**

- `Lightbox.svelte::fromMatchesSession`: add `case "map": return from === "map";`.
- `Lightbox.svelte::hiddenCrossContext`: relax to `isHidden && from !== "hidden" && !(from === "map" && session?.includeHidden === true)`. The map page sets `session.includeHidden` per the snapshot rule above. `LightboxActions.svelte` already shows "Unhide" based on `media.hidden_at != null`, not on `source.kind === "hidden"`.
- `LightboxActions.svelte` source switch: add `case "map":` returning the same verbs as `case "library":` (download, hide/unhide, delete, add-to-album, share-when-enabled). No map-specific verbs in v1.
- `LightboxNavButtons`, `LightboxToolbar`, `LightboxImage`, `LightboxVideo`, `LightboxMetadata` (other than the new pin block) stay unchanged.

**Direct reload (`/media/:id?from=map`)** falls back to `DirectMediaDetail`. We do not encode reconstruction state on the media URL in v1.

### 3.3 Lightbox map pin

New component `frontend/src/lib/components/lightbox/LightboxMapPin.svelte`. Rendered inside `LightboxMetadata` only when `media.latitude !== null && media.longitude !== null` (truthiness check would drop coordinates of `0` — equator and prime meridian).

- ~160×100px static Leaflet preview. `interactive: false`, `dragging: false`, `scrollWheelZoom: false`, no zoom controls, no clusters.
- One circular marker at `(latitude, longitude)`; map auto-fitted to a small radius (default zoom 14).
- `media.location_label` ("Big Sur, CA") rendered as a single line below the preview, truncated with ellipsis if long.
- Wrapped in `<a href="/map?z=14&c=<lat,lon>&focus=<media_id>">` with `handleInternalLinkClick` so cmd-click / open-in-new-tab work. SPA navigation otherwise via `router.navigate`.
- Same `tiles.ts` indirection as the main map page.

**`?focus=<media_id>` handling on `/map`:**

1. Fetch `/api/v1/media/geo` as usual.
2. Look up the focused ID's `(latitude, longitude)` from the response.
3. Center map at that coord with zoom = 14 (constant — overridable if `?z=` is also supplied).
4. Highlight the focused marker (orange ring) for the rest of the session, until any other interaction.
5. Grid pane filters to viewport-visible IDs (which contain the focused photo at z=14).

**Hidden-photo focus retry.** If the first geo response (no `include_hidden`) does not contain the focus id AND `hiddenStore.unlocked === true`, the page retries the geo fetch once with `include_hidden=true` and toggles the "Include hidden" UI state on for that session. This makes the explicit user action "click the map pin on this hidden photo" work without making `include_hidden` ambient.

If the focused id is missing from both responses, fall back to library-bounds auto-fit and emit a one-line toast: "Photo not found on map."

### 3.4 Backend: `GET /api/v1/media/geo`

**Transport** (`internal/httpapi/media.go`):

```go
type geoListInput struct {
    IncludeHidden bool `query:"include_hidden"`
}

type geoListOutput struct {
    Body struct {
        Items []mediaDTO `json:"items"`
    }
}
```

`mediaDTO` is the existing huma response struct used by `/api/v1/media/{id}` so the frontend `toMedia()` parses it without a new mapping. Field name `items` matches `/api/v1/media` and `/api/v1/hidden/media`.

**Hidden-claim validation lives in the handler**, mirroring `media_thumb` and `media_original`:

```go
if in.IncludeHidden {
    claim, ok := hidden.UnlockClaimFromContext(ctx)
    if !ok {
        return nil, huma.Error403Forbidden("unlock cookie required")
    }
    if claim.Principal != identity.Principal {
        return nil, huma.Error403Forbidden("unlock cookie required")
    }
}
```

403, not 401 (401 is reserved for missing identity). No new sentinel; the route returns the huma 403 directly.

**Service** (`internal/service/media.go`):

```go
func (s *MediaService) ListGeo(
    ctx context.Context,
    caller owners.Principal,
    includeHidden bool,
) ([]media.Media, error)
```

Always scoped to `caller`'s rows. `includeHidden` is passed through to the repo. The service does not validate unlock claims — that is the handler's job.

**Repo** (`internal/media/repo.go`):

```go
func (r *Repo) ListGeo(ctx context.Context, f ListGeoFilter) ([]Media, error)
```

`ListGeoFilter`: `Owner`, `IncludeHidden`. Two query shapes (no `(? OR …)` toggle):

```sql
-- IncludeHidden=false (default)
SELECT <mediaSelect>
FROM media
WHERE owner_hub = ? AND owner_user_id = ?
  AND latitude IS NOT NULL AND longitude IS NOT NULL
  AND paired_with_id IS NULL
  AND hidden_at IS NULL
ORDER BY timestamp DESC NULLS LAST, imported_at DESC, id DESC

-- IncludeHidden=true
SELECT <mediaSelect>
FROM media
WHERE owner_hub = ? AND owner_user_id = ?
  AND latitude IS NOT NULL AND longitude IS NOT NULL
  AND paired_with_id IS NULL
ORDER BY timestamp DESC NULLS LAST, imported_at DESC, id DESC
```

Uses the existing partial `media_owner_geo_idx ON media(owner_hub, owner_user_id, latitude, longitude) WHERE latitude IS NOT NULL AND longitude IS NOT NULL`. **No `LIMIT`** in v1; personal libraries are bounded. Bbox pagination is a v2 path if a deployment outgrows it.

## 4. Sharing UI flag-gate

### 4.1 Config

`internal/config/config.go`:

```go
type UI struct {
    SharingEnabled bool `toml:"sharing_enabled"`  // default: false
}

type Config struct {
    // ... existing fields ...
    UI UI `toml:"ui"`
}
```

`internal/config/config.example.toml` gains:

```toml
[ui]
sharing_enabled = false   # Hide the in-app sharing UI. Backend share APIs
                          # and the `fotobank shares ...` CLI work either way.
```

Default `false`. No validation needed for a bool.

### 4.2 Backend wiring

`internal/httpapi/api.go::Deps`:

```go
type Deps struct {
    // ... existing fields ...
    AIEnabled       bool
    SharingEnabled  bool   // new
}
```

`internal/cli/server.go` populates `Deps.SharingEnabled = cfg.UI.SharingEnabled` at server-start, the same way `AIEnabled` is wired. `httpapi` does not import `internal/config`.

`internal/httpapi/me.go`:

```go
type meOutput struct {
    Body struct {
        Principal struct {
            Hub    string `json:"hub"`
            UserID string `json:"user_id"`
            Handle string `json:"handle,omitempty"`
        } `json:"principal"`
        Scopes   []string `json:"scopes"`
        Features struct {
            SharingEnabled bool `json:"sharing_enabled"`
        } `json:"features"`
    }
}
```

The handler reads `deps.SharingEnabled` (passed via closure or registration helper, same shape as `registerAIRoutes(api, ..., deps.AIEnabled)`).

### 4.3 Frontend wiring

New store `frontend/src/lib/app/appConfig.svelte.ts`:

```ts
class AppConfigStore {
  private loaded = $state(false);
  private _sharingEnabled = $state(false);

  get sharingEnabled() { return this._sharingEnabled; }
  get ready() { return this.loaded; }

  async load(): Promise<void> {
    const resp = await fetch("/api/v1/me");
    if (!resp.ok) return;
    const body = await resp.json();
    this._sharingEnabled = body?.features?.sharing_enabled === true;
    this.loaded = true;
  }
}

export const appConfig = new AppConfigStore();
```

`App.svelte` calls `appConfig.load()` on mount; downstream components read `appConfig.sharingEnabled` reactively. The store does not block route mounting — components default to "hidden" until `appConfig.ready` flips, so a bootstrap race renders fewer items, never more.

**Gated surfaces (when `sharing_enabled === false`):**

| Surface | Behavior |
|---|---|
| `frontend/src/lib/components/Sidebar.svelte` "Shares" entry | not rendered |
| `frontend/src/lib/components/MediaActions.svelte` Share button | not rendered |
| `frontend/src/routes/AlbumDetail.svelte` "Share album" button | not rendered |
| `ShareDrawer`, `ShareModal`, `ShareStatePill`, `SharesList` | not mounted (the parent components conditionally `<ShareModal />`) |
| `frontend/src/routes/SharesPage.svelte` (`/shares`) | route gate redirects to `/` when `appConfig.ready && !sharingEnabled` |
| `frontend/src/routes/AlbumDetail.svelte` delete-blocked-by-share copy | "This album has active CLI shares. Run `fotobank shares list --album <id>` and revoke them first." (no UUID assumed in copy; surfaces the album id from context) |

**Backend stays unchanged.** All `/api/v1/shares*`, `/api/v1/shared/*` HTTP routes, the `internal/share/` / `internal/shareworker/` / `internal/broker/` / `internal/brokerexec/` packages, and the `fotobank shares` CLI subcommands behave as today regardless of `sharing_enabled`.

## 5. README refresh

Surgical fix, not a rewrite. Five changes:

| Location | Today | Replace with |
|---|---|---|
| "## Build" intro | "Requires Go 1.26+. Pure Go, no CGO." | "Requires Go 1.26+ and a C compiler. CGO is on for `mattn/go-sqlite3` + `sqlite-vec`. `make build` is the preferred entry point — it builds the SPA into `internal/web/dist/` before the Go build embeds it. The direct `go build -tags sqlite_fts5 …` path skips the SPA build, so it produces a backend-only binary unless `internal/web/dist/` is already populated." |
| `make build` mention | unchanged | also mention `make build-release`, `make install`, `make dev`. |
| Direct-build example | `go build -o bin/fotobank ./cmd/fotobank` | `go build -tags sqlite_fts5 -o bin/fotobank ./cmd/fotobank` |
| "## Configuration" — config format | YAML example (`config.yaml`) | TOML example. Show path precedence: `--config` → `$FOTOBANK_CONFIG` → `$XDG_CONFIG_HOME/fotobank/config.toml` → `$HOME/.config/fotobank/config.toml` → `./config.toml`. Point at `internal/config/config.example.toml` as the canonical example. |
| "## Status" | "Albums are in design. Sharing is deferred." | "Albums, hidden, sessions, AI tag/caption, search, and sharing CLI/API are in. The owner sharing UI is hidden by default behind `[ui].sharing_enabled`." |
| "## Commands" | `server / import / reconcile / thumbs regenerate` | add one-liners for `albums`, `hidden`, `ai`, `gps`, `pair`, `backup`, `shares`, `owners`. |

Out of scope for this README pass: tile-server / map-page operator notes, architecture deep-dive (already in `CLAUDE.md`), sub-spec / plan link bestiary.

## 6. Verification

### 6.1 Unit and integration tests

**Backend (Go):**

- `internal/media/repo_test.go::TestListGeo_*`
  - owner-scoped (cross-owner row not returned)
  - sidecars excluded (`paired_with_id NOT NULL` row not returned)
  - hidden excluded when `IncludeHidden=false`
  - hidden included when `IncludeHidden=true`
  - no-GPS row (lat null OR lon null) excluded — partial-pair rows seeded via direct SQL, since `Repo.Insert` rejects partial pairs
  - ordering exactly: `timestamp DESC NULLS LAST, imported_at DESC, id DESC` (not "matches List", because List supports both directions)
  - empty result on no-rows returns `[]`, not error
- `internal/service/media_test.go::TestListGeo_*`
  - owner scoping via service wrapper
  - `IncludeHidden` propagation to repo
  - **(no unlock-claim assertions — that gate lives in the handler)**
- `internal/httpapi/media_test.go::TestGeoRoute_*`
  - 200 body shape: `items: []` for no-rows
  - `?include_hidden=true` without unlock claim → 403 "unlock cookie required"
  - `?include_hidden=true` with mismatched-principal claim → 403
  - `?include_hidden=true` with valid claim → 200 includes hidden rows
  - **route-conflict regression:** `GET /api/v1/media/geo` resolves to the geo handler, not `GET /api/v1/media/{id}` with `id="geo"`
- `internal/config/config_test.go::TestUIConfig_*`
  - default `sharing_enabled=false`
  - explicit `true` parses
- `internal/httpapi/me_test.go::TestMeFeatures_*`
  - `features.sharing_enabled` reflects `Deps.SharingEnabled`

**Frontend (Vitest):**

- `frontend/src/lib/app/appConfig.test.ts` — store fetches once, exposes `sharingEnabled`.
- `frontend/src/lib/components/Sidebar.test.ts` — Shares entry hidden when `!sharingEnabled`.
- `frontend/src/lib/components/MediaActions.test.ts` — Share button hidden.
- `frontend/src/routes/AlbumDetail.test.ts` — Share Album button hidden; delete-blocked copy renders without UUID assumption.
- `frontend/src/lib/map/tiles.test.ts` — `tileUrl()` returns the OSM URL pattern; `attribution()` returns the OSM attribution string.
- `frontend/src/lib/components/lightbox/LightboxMapPin.test.ts` — renders only when both lat and lon are non-null (regression for coordinate `0`); `<a href="/map?focus=…">` SPA-navigates on click; cmd-click is intercepted by `handleInternalLinkClick`.
- `frontend/src/routes/Map.test.ts` — auto-fit on initial load, marker click opens lightbox snapshot with `from=map` and the expected `navIds`/`returnHref`, cluster click filters grid, "Include hidden" toggle is present iff `hiddenStore.unlocked`, focus retry triggers a second geo fetch with `include_hidden=true` when the first response misses the focus id and the user is unlocked.

### 6.2 Playwright e2e

`frontend/tests/e2e/map.spec.ts` (new):

- Map page loads, attribution element present in DOM, marker layer mounted.
- Click cluster → grid filters; clear chip dismisses filter.
- Click photo → lightbox opens with map-source snapshot; close returns to `/map?z&c` at the same URL state.
- Lightbox map pin → navigates to `/map?focus=<id>`; focused marker has the highlighted class.
- Mobile viewport (≤ 1024px main-area width): tabs render; tab switch from Map → Photos triggers a `map.invalidateSize()` on switch back.
- Hidden toggle absent when locked; present when unlocked; toggling on refetches geo and adds hidden photos to the grid.
- **Tile availability is not asserted.** OSM tile fetches are best-effort over the public network; tests assert SPA structure, route state, attribution presence, and grid/lightbox behavior. Tile rendering is a manual check.

`frontend/tests/e2e/sharing-disabled.spec.ts` (new):

- `cmd/e2e-server/main.go` accepts a `--sharing-enabled` flag (default `true` in the existing suite, `false` for this suite).
- Sidebar has no Shares entry.
- MediaActions has no Share button.
- AlbumDetail has no Share Album button.
- Lightbox actions have no Share verb.
- Direct nav to `/shares` redirects to `/`.
- Album with a CLI-created share: delete attempt surfaces the new copy referencing `fotobank shares list --album <id>`.

### 6.3 Manual QA (gate to SD-card dogfooding)

- `make build` produces a binary; binary opens fresh sandbox config and self-bootstraps.
- `fotobank server` boots; `/api/v1/healthz` returns 200; `/api/v1/me` returns `features.sharing_enabled=false`.
- SPA loads at `http://127.0.0.1:8090`; sidebar has no Shares entry.
- Import 5 GPS-tagged sample photos via `fotobank import`. `fotobank reconcile` reports zero drift.
- Open `/map`: pins appear, click a pin → lightbox; close → back to map at same z/c.
- Open lightbox from library on a GPS-tagged photo, GPS pin appears with location label, click pin → `/map?focus=<id>` highlights the marker.

## 7. Open questions (resolve during planning, not blocking spec)

1. **Marker color.** Spec says "owner accent color" for individual markers and red for clusters. We do not have an existing accent color token in the SPA. Pick during plan-writing — likely fall back to the existing `--color-accent` CSS var if present, otherwise plain `#2563eb`.
2. **`density.map` initial value.** Default to whatever `density.library` defaults to, or pick a denser default since the grid pane is narrower. Probably "match library default" for v1.
3. **Tab transition animation on mobile.** Hard-cut vs cross-fade. Default: hard cut to keep the bundle light.

## 8. Non-goals reaffirmed

- No bbox-paginated `/api/v1/media/geo`. Whole library, no `LIMIT`. v2 if needed.
- No client-side tile cache layer. Browser cache + Leaflet defaults are enough.
- No address-based geocoding. `location_label` is whatever the import pipeline already wrote; we display it but do not produce it here.
- No map-side tag/caption rendering. The grid handles those when a photo is opened.
- No share-UI removal — flag, not delete. The code stays so Phase 2 broker integration can re-enable it.

---

*End of design spec.*
