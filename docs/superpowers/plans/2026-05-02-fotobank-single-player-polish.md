# Fotobank Single-Player Polish Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. **After every 5th task, invoke `/roborev-fix` on the current branch as a cleanup checkpoint.** Do not start the next 5-task chunk until the cleanup checkpoint completes.

**Goal:** Close the three single-player gaps before SD-card dogfooding — ship the `/map` page (split view, Leaflet + OSM, lightbox-source, lightbox map pin), gate the sharing UI behind a config flag (default off), and refresh the stale README.

**Architecture:** One new backend endpoint (`GET /api/v1/media/geo`) plus a `[ui].sharing_enabled` config field surfaced via `/api/v1/me.features`. New SPA route `/map` consumes the existing virtualized grid and adds Leaflet 1.9 + `leaflet.markercluster` against OSM raster tiles via a centralized `tiles.ts` module. The `LightboxSource` union gains a `"map"` variant; `Lightbox.svelte` and `LightboxActions.svelte` get one new branch each. Sharing UI gating is one new store (`appConfig`) consumed by a handful of components and a route-gate in `App.svelte`.

**Tech Stack:** Go (`database/sql`, `mattn/go-sqlite3`, huma/v2, `BurntSushi/toml`); Svelte 5 + TypeScript on the frontend; Leaflet 1.9.x + `leaflet.markercluster` 1.5.x.

**Reference docs:**
- Spec: `docs/superpowers/specs/2026-05-02-fotobank-single-player-polish-design.md`
- Master vision (identity scope): `docs/superpowers/specs/2026-04-22-fotobank-vision.md`
- Existing list-pattern reference: `internal/media/repo.go::ListHidden`, `internal/httpapi/hidden_media.go::registerListHiddenMedia`
- Existing source-pattern reference: `frontend/src/lib/lightbox/lightboxSession.svelte.ts`, `frontend/src/lib/components/lightbox/Lightbox.svelte::fromMatchesSession`
- Existing `AIEnabled` wiring (mirror for `SharingEnabled`): `internal/httpapi/api.go::Deps.AIEnabled`, `internal/cli/server.go` wiring, `internal/httpapi/ai.go::registerAIRoutes` signature.

---

## File structure

Backend:

| File | Responsibility |
|---|---|
| `internal/config/config.go` | (modify) add `UI` struct, `Config.UI`, defaults |
| `internal/config/config.example.toml` | (modify) document `[ui]` block |
| `internal/config/config_test.go` | (modify) cover `[ui].sharing_enabled` default + parse |
| `internal/media/repo.go` | (modify) add `ListGeoFilter`, `Repo.ListGeo` |
| `internal/media/repo_test.go` | (modify) cover ListGeo cases |
| `internal/service/media.go` | (modify) add `MediaService.ListGeo` |
| `internal/service/media_test.go` | (modify) cover ListGeo owner-scoping + IncludeHidden propagation |
| `internal/httpapi/api.go` | (modify) add `Deps.SharingEnabled`; pass to `registerMe`; mount `registerMediaGeo` |
| `internal/httpapi/me.go` | (modify) add `features.sharing_enabled` to response |
| `internal/httpapi/me_test.go` | (modify) cover features field + Deps wiring |
| `internal/httpapi/media_geo.go` | (new) huma route + handler for `GET /api/v1/media/geo` |
| `internal/httpapi/media_geo_test.go` | (new) cover route shape + hidden gate + route-conflict |
| `internal/cli/server.go` | (modify) wire `cfg.UI.SharingEnabled` into `Deps.SharingEnabled` |

Frontend:

| File | Responsibility |
|---|---|
| `frontend/package.json` | (modify) add `leaflet` and `leaflet.markercluster` deps |
| `frontend/src/lib/api/generated/schema.ts` | (regenerated) `/media/geo` and `/me.features` route types |
| `frontend/src/lib/app/appConfig.svelte.ts` | (new) reactive app-config store fetched from `/me` |
| `frontend/src/lib/app/appConfig.test.ts` | (new) store unit tests |
| `frontend/src/lib/map/tiles.ts` | (new) centralized OSM tile URL + attribution string |
| `frontend/src/lib/map/tiles.test.ts` | (new) tile module unit tests |
| `frontend/src/lib/map/geoStore.svelte.ts` | (new) cached `/media/geo` fetcher with hidden-include retry |
| `frontend/src/lib/map/geoStore.test.ts` | (new) geo store unit tests |
| `frontend/src/lib/lightbox/lightboxSession.svelte.ts` | (modify) add `"map"` to `LightboxSource`; add `includeHidden` to snapshot |
| `frontend/src/lib/router/router.svelte.ts` | (modify) extend `parseFrom` with `"map"`; add `/map` route |
| `frontend/src/routes/Map.svelte` | (new) the page shell — split / tabs |
| `frontend/src/routes/Map.test.ts` | (new) component tests |
| `frontend/src/lib/map/MapPane.svelte` | (new) Leaflet pane + clusters |
| `frontend/src/lib/map/MapPane.test.ts` | (new) component tests |
| `frontend/src/lib/map/MapGridPane.svelte` | (new) right-side grid + clear-filter chip |
| `frontend/src/lib/components/Sidebar.svelte` | (modify) add "Map" entry; conditional Shares entry |
| `frontend/src/lib/components/Sidebar.test.ts` | (modify) cover gating + new entry |
| `frontend/src/lib/components/MediaActions.svelte` | (modify) gate Share button on `appConfig.sharingEnabled` |
| `frontend/src/lib/components/MediaActions.test.ts` | (modify) cover gated state |
| `frontend/src/routes/AlbumDetail.svelte` | (modify) gate Share Album button + delete-blocked copy fix |
| `frontend/src/routes/AlbumDetail.test.ts` | (modify) cover gating + copy |
| `frontend/src/App.svelte` | (modify) `appConfig.load()` on mount; mount `Map.svelte`; redirect `/shares` when disabled |
| `frontend/src/lib/components/lightbox/Lightbox.svelte` | (modify) add `case "map"` to `fromMatchesSession`; relax `hiddenCrossContext` for `from=map && includeHidden` |
| `frontend/src/lib/components/lightbox/LightboxActions.svelte` | (modify) add `case "map"` to source switch |
| `frontend/src/lib/components/lightbox/LightboxMapPin.svelte` | (new) static map pin in metadata panel |
| `frontend/src/lib/components/lightbox/LightboxMapPin.test.ts` | (new) component tests |
| `frontend/src/lib/components/lightbox/LightboxMetadata.svelte` | (modify) mount `LightboxMapPin` when GPS present |
| `cmd/e2e-server/main.go` | (modify) `--sharing-enabled` flag; seed GPS-tagged fixtures |
| `frontend/tests/e2e/map.spec.ts` | (new) Playwright suite for `/map` |
| `frontend/tests/e2e/sharing-disabled.spec.ts` | (new) Playwright suite for sharing-gated SPA |

Docs:

| File | Responsibility |
|---|---|
| `README.md` | (modify) five surgical fixes per spec §5 |

---

## Conventions and warnings for the executor

- **Schema policy.** No schema changes in this plan. Touch `internal/db/migrations/000001_initial_schema.up.sql` only if a task explicitly says so (none do).
- **Service-first.** Transports (httpapi, CLI) never bypass `internal/service/`. The new `/api/v1/media/geo` handler goes handler → `MediaService.ListGeo` → `media.Repo.ListGeo`. The handler also performs the unlock-claim check; the service trusts its caller and only enforces owner scoping.
- **Sentinels.** No new sentinels in this plan. The geo handler returns `huma.Error403Forbidden("unlock cookie required")` directly when `include_hidden=true` and the unlock claim is absent or principal-mismatched. The service surfaces existing `errs` sentinels which `Translate` already maps.
- **Frontend GPS check.** Always `media.latitude != null && media.longitude != null` (loose `!=`); never strict `!==` (lets `undefined` through) or truthiness (drops literal `0`). The `Media` TS type marks both fields optional.
- **Internal links.** SPA navigation always via `router.navigate(href)` or `<a href="..." onclick={(e) => handleInternalLinkClick(e, href)}>`. The latter is required for any user-clickable entry so cmd/ctrl/middle-click open in a new tab — `handleInternalLinkClick` already short-circuits those cases.
- **`exactOptionalPropertyTypes`.** Discriminated-union route types do not allow `undefined` on optional fields. Spread keys conditionally (`...(x !== null ? { x } : {})`) when building route matches.
- **Tests.** Backend uses `testify/require` and `testutil.OpenTestDB(t)` per fresh DB. Frontend unit tests use Vitest + `@testing-library/svelte`. E2E uses Playwright; rebuild the SPA bundle and the e2e-server binary via `bun run pretest:e2e` (already wired) before running.
- **Commits.** Conventional, ≤72 char subject, scoped (`feat(map):`, `fix(httpapi):`, etc.). One logical change per commit. Never bypass pre-commit hooks. The `make api-generate` hook regenerates `internal/httpapi/openapi.json` and `frontend/src/lib/api/generated/schema.ts` whenever an HTTP route changes — let it run.
- **No new dependencies on the backend.** All backend tasks use libraries already in `go.mod`.
- **Frontend deps:** `leaflet@^1.9` and `leaflet.markercluster@^1.5` (and their `@types/...` packages) are added in Task F1; do not import them earlier.
- **Pre-alpha policy.** Pre-existing CLAUDE.md applies. Edit master directly. No worktrees, no feature branches.

---

## Section A — Backend foundation: config, /me, Deps wiring

### Task A1: Add `[ui].sharing_enabled` to backend config

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config.example.toml`
- Test: `internal/config/config_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/config/config_test.go`:

```go
func TestConfig_UI_SharingEnabled_DefaultsFalse(t *testing.T) {
    t.Helper()
    cfg, err := config.LoadString(minimalConfigTOML())
    require.NoError(t, err)
    require.False(t, cfg.UI.SharingEnabled, "sharing_enabled defaults to false")
}

func TestConfig_UI_SharingEnabled_ParsesTrue(t *testing.T) {
    t.Helper()
    src := minimalConfigTOML() + "\n[ui]\nsharing_enabled = true\n"
    cfg, err := config.LoadString(src)
    require.NoError(t, err)
    require.True(t, cfg.UI.SharingEnabled)
}
```

(`minimalConfigTOML()` is the existing helper in `config_test.go` that returns the smallest valid TOML. `config.LoadString` is the existing string-loader.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -tags sqlite_fts5 ./internal/config -run TestConfig_UI_SharingEnabled -v`
Expected: FAIL with `cfg.UI undefined` or similar.

- [ ] **Step 3: Add `UI` struct and embed in `Config`**

Edit `internal/config/config.go`. Inside the type block, add:

```go
type UI struct {
    SharingEnabled bool `toml:"sharing_enabled"`
}
```

Add `UI UI \`toml:"ui"\`` to the `Config` struct (place it next to `Search`):

```go
type Config struct {
    Flash         Flash         `toml:"flash"`
    NAS           NAS           `toml:"nas"`
    Storage       Storage       `toml:"storage"`
    Identity      Identity      `toml:"identity"`
    HTTP          HTTP          `toml:"http"`
    Imports       Imports       `toml:"imports"`
    Thumbs        Thumbs        `toml:"thumbs"`
    Broker        Broker        `toml:"broker"`
    Backup        Backup        `toml:"backup"`
    Observability Observability `toml:"observability"`
    AI            ai.Config     `toml:"ai"`
    Search        search.Config `toml:"search"`
    UI            UI            `toml:"ui"`
}
```

No defaults helper needed — Go zero-values `false`, which is the documented default.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -tags sqlite_fts5 ./internal/config -run TestConfig_UI_SharingEnabled -v`
Expected: PASS.

Run the full config test set:
Run: `go test -tags sqlite_fts5 ./internal/config -v`
Expected: PASS.

- [ ] **Step 5: Document in `config.example.toml` and commit**

Append to `internal/config/config.example.toml`:

```toml

[ui]
# Hide the in-app sharing UI. Backend share APIs and the
# `fotobank shares ...` CLI work either way. Default: false.
sharing_enabled = false
```

Run: `git add internal/config/config.go internal/config/config.example.toml internal/config/config_test.go && git commit -m "feat(config): add [ui].sharing_enabled (default false)"`

### Task A2: Add `Deps.SharingEnabled` and wire from `cli/server.go`

**Files:**
- Modify: `internal/httpapi/api.go`
- Modify: `internal/cli/server.go`

- [ ] **Step 1: Add field to `Deps`**

Edit `internal/httpapi/api.go`. Add the field after `AIEnabled`:

```go
    // AIEnabled is the [ai].enabled config flag at boot. Wired explicitly
    // so the panel can show config_disabled without the AIService poking
    // at config.
    AIEnabled bool
    // SharingEnabled is the [ui].sharing_enabled config flag at boot.
    // Surfaced to the SPA via /api/v1/me.features.sharing_enabled so the
    // frontend can hide share UI without the share data-plane changing
    // shape. Backend share APIs and the `fotobank shares ...` CLI work
    // regardless of this flag.
    SharingEnabled bool
    // Search backs GET /api/v1/search.
    Search *searchsvc.Service
```

- [ ] **Step 2: Wire from `cli/server.go`**

Locate the `httpapi.New(httpapi.Deps{...})` block in `internal/cli/server.go` (search for `AIEnabled:` to find it). Add `SharingEnabled: cfg.UI.SharingEnabled,` next to `AIEnabled: cfg.AI.Enabled,`:

```go
    AIEnabled:        cfg.AI.Enabled,
    SharingEnabled:   cfg.UI.SharingEnabled,
```

- [ ] **Step 3: Build to confirm wiring compiles**

Run: `go build -tags sqlite_fts5 ./...`
Expected: success, no compile errors.

- [ ] **Step 4: Run the broader httpapi test suite**

Run: `go test -tags sqlite_fts5 ./internal/httpapi/... -v`
Expected: PASS (no behavior change yet — only field plumbing).

- [ ] **Step 5: Commit**

Run: `git add internal/httpapi/api.go internal/cli/server.go && git commit -m "feat(httpapi): plumb cfg.UI.SharingEnabled into Deps"`

### Task A3: Extend `/api/v1/me` with `features.sharing_enabled`

**Files:**
- Modify: `internal/httpapi/api.go` (registration call)
- Modify: `internal/httpapi/me.go`
- Test: `internal/httpapi/me_test.go`

- [ ] **Step 1: Write the failing test**

Edit `internal/httpapi/me_test.go`. Add or replace the relevant test:

```go
func TestMe_FeaturesSharingEnabled(t *testing.T) {
    t.Helper()
    rec := callMe(t, httpapi.Deps{
        IdentityProvider: stubProvider(t, "h", "u"),
        SharingEnabled:   true,
    })
    require.Equal(t, http.StatusOK, rec.Code)

    var body struct {
        Features struct {
            SharingEnabled bool `json:"sharing_enabled"`
        } `json:"features"`
    }
    require.NoError(t, json.NewDecoder(rec.Body).Decode(&body))
    require.True(t, body.Features.SharingEnabled)
}

func TestMe_FeaturesSharingDisabledByDefault(t *testing.T) {
    t.Helper()
    rec := callMe(t, httpapi.Deps{
        IdentityProvider: stubProvider(t, "h", "u"),
        // SharingEnabled left zero (false)
    })
    require.Equal(t, http.StatusOK, rec.Code)

    var body struct {
        Features struct {
            SharingEnabled bool `json:"sharing_enabled"`
        } `json:"features"`
    }
    require.NoError(t, json.NewDecoder(rec.Body).Decode(&body))
    require.False(t, body.Features.SharingEnabled)
}
```

(`callMe` and `stubProvider` are existing helpers in `me_test.go`. If they aren't yet, copy the pattern from a sibling test file like `albums_test.go::TestAlbums_*`.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -tags sqlite_fts5 ./internal/httpapi -run TestMe_Features -v`
Expected: FAIL with `Features` field undefined.

- [ ] **Step 3: Extend response and registration**

Edit `internal/httpapi/me.go`:

```go
package httpapi

import (
    "context"
    "net/http"

    "github.com/danielgtaylor/huma/v2"

    "github.com/wesm/fotobank/internal/errs"
)

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

// registerMe wires GET /api/v1/me. sharingEnabled is the [ui].sharing_enabled
// config flag at boot; it surfaces under features.sharing_enabled so the SPA
// can hide share UI without the share data-plane changing shape.
func registerMe(api huma.API, sharingEnabled bool) {
    huma.Register(api, huma.Operation{
        OperationID: "me",
        Method:      http.MethodGet,
        Path:        "/api/v1/me",
        Summary:     "Return identity of the caller",
    }, func(ctx context.Context, _ *struct{}) (*meOutput, error) {
        id, ok := IdentityFromContext(ctx)
        if !ok {
            return nil, huma.Error401Unauthorized(errs.ErrIdentityMissing.Error())
        }
        out := &meOutput{}
        out.Body.Principal.Hub = id.Principal.Hub
        out.Body.Principal.UserID = id.Principal.UserID
        out.Body.Principal.Handle = id.Principal.Handle
        out.Body.Scopes = id.Scopes
        out.Body.Features.SharingEnabled = sharingEnabled
        return out, nil
    })
}
```

Edit `internal/httpapi/api.go::buildAPI` and update the call site:

```go
    registerMe(api, deps.SharingEnabled)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -tags sqlite_fts5 ./internal/httpapi -run TestMe_Features -v`
Expected: PASS.

Then full httpapi suite:
Run: `go test -tags sqlite_fts5 ./internal/httpapi -v`
Expected: PASS.

- [ ] **Step 5: Commit**

The pre-commit `make api-generate` hook will regenerate `internal/httpapi/openapi.json` and `frontend/src/lib/api/generated/schema.ts`. Stage everything it touched:

Run:
```bash
git add internal/httpapi/me.go internal/httpapi/me_test.go internal/httpapi/api.go internal/httpapi/openapi.json frontend/src/lib/api/generated/schema.ts
git commit -m "feat(httpapi): /me reports features.sharing_enabled"
```

---

## Section B — Backend: `GET /api/v1/media/geo`

### Task B1: Add `Repo.ListGeo`

**Files:**
- Modify: `internal/media/repo.go`
- Test: `internal/media/repo_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/media/repo_test.go`:

```go
func TestRepo_ListGeo_OwnerScoped(t *testing.T) {
    t.Helper()
    d := testutil.OpenTestDB(t)
    repo := media.NewRepo(d.WriteDB(), d.ReadDB())
    ownerA := owners.Principal{Hub: "h", UserID: "a"}
    ownerB := owners.Principal{Hub: "h", UserID: "b"}
    seedOwner(t, d, ownerA, "a")
    seedOwner(t, d, ownerB, "b")
    insertMediaGPS(t, repo, ownerA, "p1.jpg", floatPtr(40.0), floatPtr(-105.0))
    insertMediaGPS(t, repo, ownerB, "p2.jpg", floatPtr(35.0), floatPtr(-115.0))

    rows, err := repo.ListGeo(context.Background(), media.ListGeoFilter{Owner: ownerA})
    require.NoError(t, err)
    require.Len(t, rows, 1)
    require.Equal(t, "p1.jpg", rows[0].OriginalFilename)
}

func TestRepo_ListGeo_ExcludesSidecars(t *testing.T) {
    t.Helper()
    d := testutil.OpenTestDB(t)
    repo := media.NewRepo(d.WriteDB(), d.ReadDB())
    owner := owners.Principal{Hub: "h", UserID: "u"}
    seedOwner(t, d, owner, "u")
    primary := insertMediaGPS(t, repo, owner, "primary.jpg", floatPtr(10.0), floatPtr(20.0))
    sidecar := insertMediaGPS(t, repo, owner, "primary.arw", floatPtr(10.0), floatPtr(20.0))
    pairSidecar(t, repo, sidecar, primary.ID)

    rows, err := repo.ListGeo(context.Background(), media.ListGeoFilter{Owner: owner})
    require.NoError(t, err)
    require.Len(t, rows, 1)
    require.Equal(t, primary.ID, rows[0].ID)
}

func TestRepo_ListGeo_ExcludesHiddenByDefault(t *testing.T) {
    t.Helper()
    d := testutil.OpenTestDB(t)
    repo := media.NewRepo(d.WriteDB(), d.ReadDB())
    owner := owners.Principal{Hub: "h", UserID: "u"}
    seedOwner(t, d, owner, "u")
    visible := insertMediaGPS(t, repo, owner, "v.jpg", floatPtr(0.0), floatPtr(0.0))
    hidden := insertMediaGPS(t, repo, owner, "h.jpg", floatPtr(1.0), floatPtr(1.0))
    require.NoError(t, repo.SetHiddenAt(context.Background(), hidden.ID, time.Now()))

    rows, err := repo.ListGeo(context.Background(), media.ListGeoFilter{Owner: owner, IncludeHidden: false})
    require.NoError(t, err)
    require.Len(t, rows, 1)
    require.Equal(t, visible.ID, rows[0].ID)
}

func TestRepo_ListGeo_IncludesHiddenWhenRequested(t *testing.T) {
    t.Helper()
    d := testutil.OpenTestDB(t)
    repo := media.NewRepo(d.WriteDB(), d.ReadDB())
    owner := owners.Principal{Hub: "h", UserID: "u"}
    seedOwner(t, d, owner, "u")
    visible := insertMediaGPS(t, repo, owner, "v.jpg", floatPtr(0.0), floatPtr(0.0))
    hidden := insertMediaGPS(t, repo, owner, "h.jpg", floatPtr(1.0), floatPtr(1.0))
    require.NoError(t, repo.SetHiddenAt(context.Background(), hidden.ID, time.Now()))

    rows, err := repo.ListGeo(context.Background(), media.ListGeoFilter{Owner: owner, IncludeHidden: true})
    require.NoError(t, err)
    require.Len(t, rows, 2)
}

func TestRepo_ListGeo_OmitsRowsWithoutGPS(t *testing.T) {
    t.Helper()
    d := testutil.OpenTestDB(t)
    repo := media.NewRepo(d.WriteDB(), d.ReadDB())
    owner := owners.Principal{Hub: "h", UserID: "u"}
    seedOwner(t, d, owner, "u")
    insertMediaGPS(t, repo, owner, "with-gps.jpg", floatPtr(40.0), floatPtr(-105.0))
    insertMediaGPS(t, repo, owner, "no-gps.jpg", nil, nil)

    // Direct SQL: insert a row with lat set but lon NULL. Repo.Insert
    // rejects partial pairs, so we bypass it for this regression seed.
    _, err := d.WriteDB().Exec(`
        INSERT INTO media (id, owner_hub, owner_user_id, media_type, mime_type, path,
                            imported_at, size, checksum, latitude, longitude, thumb_status, thumb_version)
        VALUES (?, ?, ?, 'photo', 'image/jpeg', ?, ?, 1, ?, 50.0, NULL, 'pending', 0)
    `, "00000000-0000-0000-0000-0000000000aa", owner.Hub, owner.UserID, "partial.jpg",
        time.Now(), "deadbeef-partial")
    require.NoError(t, err)

    rows, err := repo.ListGeo(context.Background(), media.ListGeoFilter{Owner: owner})
    require.NoError(t, err)
    require.Len(t, rows, 1)
    require.Equal(t, "with-gps.jpg", rows[0].OriginalFilename)
}

func TestRepo_ListGeo_OrderingTimestampDescThenImportedDescThenIDDesc(t *testing.T) {
    t.Helper()
    d := testutil.OpenTestDB(t)
    repo := media.NewRepo(d.WriteDB(), d.ReadDB())
    owner := owners.Principal{Hub: "h", UserID: "u"}
    seedOwner(t, d, owner, "u")
    t1 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
    t2 := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
    older := insertMediaGPSAt(t, repo, owner, "older.jpg", floatPtr(10.0), floatPtr(20.0), &t1)
    newer := insertMediaGPSAt(t, repo, owner, "newer.jpg", floatPtr(10.0), floatPtr(20.0), &t2)
    nullTS := insertMediaGPSAt(t, repo, owner, "null.jpg", floatPtr(10.0), floatPtr(20.0), nil)

    rows, err := repo.ListGeo(context.Background(), media.ListGeoFilter{Owner: owner})
    require.NoError(t, err)
    require.Len(t, rows, 3)
    require.Equal(t, newer.ID, rows[0].ID)
    require.Equal(t, older.ID, rows[1].ID)
    require.Equal(t, nullTS.ID, rows[2].ID)
}

func TestRepo_ListGeo_EmptyReturnsEmptySlice(t *testing.T) {
    t.Helper()
    d := testutil.OpenTestDB(t)
    repo := media.NewRepo(d.WriteDB(), d.ReadDB())
    owner := owners.Principal{Hub: "h", UserID: "u"}
    seedOwner(t, d, owner, "u")

    rows, err := repo.ListGeo(context.Background(), media.ListGeoFilter{Owner: owner})
    require.NoError(t, err)
    require.Empty(t, rows)
}
```

(`seedOwner`, `insertMediaGPS`, `pairSidecar`, `floatPtr`, and `insertMediaGPSAt` are helpers. If they don't exist, add them at the top of `repo_test.go` modeled on existing helpers like `seedMedia`. `insertMediaGPS` calls `repo.Insert` with lat/lon set; `insertMediaGPSAt` adds a `*time.Time` for the timestamp; `pairSidecar` uses the existing pair-update helper or `repo.UpdatePairing`.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -tags sqlite_fts5 ./internal/media -run TestRepo_ListGeo -v`
Expected: FAIL with `repo.ListGeo undefined`.

- [ ] **Step 3: Implement `ListGeoFilter` and `Repo.ListGeo`**

Edit `internal/media/repo.go`. After the `ListFilter` definition, add:

```go
// ListGeoFilter scopes a ListGeo query.
type ListGeoFilter struct {
    Owner         owners.Principal
    IncludeHidden bool
}
```

Add the method (place near `ListHidden`):

```go
// ListGeo returns primary and standalone rows for owner that have GPS
// coordinates. Sidecars (paired_with_id IS NOT NULL) and rows missing
// either latitude or longitude are excluded. When IncludeHidden is
// false (default), hidden rows are also excluded; when true, all rows
// — visible and hidden — are returned (the handler is expected to
// have validated an unlock claim before calling).
//
// Sort order: timestamp DESC NULLS LAST, imported_at DESC, id DESC.
//
// Uses the partial index media_owner_geo_idx for the predicate
// latitude IS NOT NULL AND longitude IS NOT NULL.
func (r *Repo) ListGeo(ctx context.Context, f ListGeoFilter) ([]Media, error) {
    var q string
    if f.IncludeHidden {
        q = mediaSelect + `
 WHERE owner_hub = ? AND owner_user_id = ?
   AND latitude IS NOT NULL AND longitude IS NOT NULL
   AND paired_with_id IS NULL
 ORDER BY timestamp DESC NULLS LAST, imported_at DESC, id DESC`
    } else {
        q = mediaSelect + `
 WHERE owner_hub = ? AND owner_user_id = ?
   AND latitude IS NOT NULL AND longitude IS NOT NULL
   AND paired_with_id IS NULL
   AND hidden_at IS NULL
 ORDER BY timestamp DESC NULLS LAST, imported_at DESC, id DESC`
    }
    rows, err := r.ro.QueryContext(ctx, q, f.Owner.Hub, f.Owner.UserID)
    if err != nil {
        return nil, fmt.Errorf("list geo: %w", err)
    }
    defer func() { _ = rows.Close() }()

    out := make([]Media, 0)
    for rows.Next() {
        m, err := scanMedia(rows)
        if err != nil {
            return nil, fmt.Errorf("scan geo: %w", err)
        }
        out = append(out, m)
    }
    if err := rows.Err(); err != nil {
        return nil, fmt.Errorf("iterate geo: %w", err)
    }
    return out, nil
}
```

(Returning `make([]Media, 0)` rather than `nil` makes the empty-slice test pass cleanly without a `len()` ambiguity.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -tags sqlite_fts5 ./internal/media -run TestRepo_ListGeo -v`
Expected: PASS for all seven cases.

Run the full media test set:
Run: `go test -tags sqlite_fts5 ./internal/media -v`
Expected: PASS.

- [ ] **Step 5: Commit**

Run:
```bash
git add internal/media/repo.go internal/media/repo_test.go
git commit -m "feat(media): add Repo.ListGeo for geotagged primaries"
```

### Task B2: Add `MediaService.ListGeo`

**Files:**
- Modify: `internal/service/media.go`
- Test: `internal/service/media_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/service/media_test.go`:

```go
func TestMediaService_ListGeo_OwnerScoped(t *testing.T) {
    t.Helper()
    fix := newMediaServiceFixture(t)
    ownerA := fix.seedOwner("a")
    ownerB := fix.seedOwner("b")
    fix.seedGPS(ownerA, "p1.jpg", 10.0, 20.0)
    fix.seedGPS(ownerB, "p2.jpg", 30.0, 40.0)

    rows, err := fix.svc.ListGeo(context.Background(), ownerA, false)
    require.NoError(t, err)
    require.Len(t, rows, 1)
    require.Equal(t, "p1.jpg", rows[0].OriginalFilename)
}

func TestMediaService_ListGeo_PropagatesIncludeHidden(t *testing.T) {
    t.Helper()
    fix := newMediaServiceFixture(t)
    owner := fix.seedOwner("u")
    visible := fix.seedGPS(owner, "v.jpg", 10.0, 20.0)
    hidden := fix.seedGPS(owner, "h.jpg", 30.0, 40.0)
    fix.hide(hidden.ID)

    visOnly, err := fix.svc.ListGeo(context.Background(), owner, false)
    require.NoError(t, err)
    require.Len(t, visOnly, 1)
    require.Equal(t, visible.ID, visOnly[0].ID)

    all, err := fix.svc.ListGeo(context.Background(), owner, true)
    require.NoError(t, err)
    require.Len(t, all, 2)
}
```

(The fixture builder follows the existing `newMediaServiceFixture` pattern; if it doesn't expose `seedGPS` / `hide`, add them. The service does **not** validate unlock claims — that gate lives in the handler in B3. These tests therefore do not assert any 403/permission behavior.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -tags sqlite_fts5 ./internal/service -run TestMediaService_ListGeo -v`
Expected: FAIL with `svc.ListGeo undefined`.

- [ ] **Step 3: Implement the service method**

Edit `internal/service/media.go`. Add near `ListHidden`:

```go
// ListGeo returns geotagged primaries owned by caller. When
// includeHidden is true, hidden rows are included; the handler is
// expected to have validated an unlock claim before calling.
//
// The service does not enforce the unlock-claim gate — the gate is in
// httpapi.registerMediaGeo so the 403 response shape stays inside the
// transport layer (matching list-hidden-media).
func (s *MediaService) ListGeo(
    ctx context.Context,
    caller owners.Principal,
    includeHidden bool,
) ([]media.Media, error) {
    return s.repo.ListGeo(ctx, media.ListGeoFilter{
        Owner:         caller,
        IncludeHidden: includeHidden,
    })
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -tags sqlite_fts5 ./internal/service -run TestMediaService_ListGeo -v`
Expected: PASS.

Run the full service test set:
Run: `go test -tags sqlite_fts5 ./internal/service -v`
Expected: PASS.

- [ ] **Step 5: Commit**

Run:
```bash
git add internal/service/media.go internal/service/media_test.go
git commit -m "feat(service): MediaService.ListGeo wrapper"
```

### Task B3: Add `GET /api/v1/media/geo` route + handler

**Files:**
- Create: `internal/httpapi/media_geo.go`
- Create: `internal/httpapi/media_geo_test.go`
- Modify: `internal/httpapi/api.go` (mount registration)

- [ ] **Step 1: Write the failing tests**

Create `internal/httpapi/media_geo_test.go`:

```go
package httpapi_test

import (
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "testing"

    "github.com/stretchr/testify/require"

    "github.com/wesm/fotobank/internal/httpapi"
)

func TestGeoRoute_EmptyOwnerReturnsEmptyItems(t *testing.T) {
    t.Helper()
    server := newTestServer(t, withMediaService(t))
    rec := server.do(t, http.MethodGet, "/api/v1/media/geo", nil)
    require.Equal(t, http.StatusOK, rec.Code)

    var body struct {
        Items []map[string]any `json:"items"`
    }
    require.NoError(t, json.NewDecoder(rec.Body).Decode(&body))
    require.NotNil(t, body.Items)
    require.Len(t, body.Items, 0)
}

func TestGeoRoute_VisibleByDefault(t *testing.T) {
    t.Helper()
    server := newTestServer(t, withMediaService(t))
    server.seedGPS(t, "visible.jpg", 10.0, 20.0)

    rec := server.do(t, http.MethodGet, "/api/v1/media/geo", nil)
    require.Equal(t, http.StatusOK, rec.Code)

    var body struct {
        Items []map[string]any `json:"items"`
    }
    require.NoError(t, json.NewDecoder(rec.Body).Decode(&body))
    require.Len(t, body.Items, 1)
    require.Equal(t, "visible.jpg", body.Items[0]["original_filename"])
}

func TestGeoRoute_IncludeHiddenWithoutUnlockReturns403(t *testing.T) {
    t.Helper()
    server := newTestServer(t, withMediaService(t), withHiddenAuth(t))
    server.seedGPS(t, "anything.jpg", 10.0, 20.0)

    rec := server.do(t, http.MethodGet, "/api/v1/media/geo?include_hidden=true", nil)
    require.Equal(t, http.StatusForbidden, rec.Code)
    require.Contains(t, rec.Body.String(), "unlock cookie required")
}

func TestGeoRoute_IncludeHiddenWithValidClaimReturnsHidden(t *testing.T) {
    t.Helper()
    server := newTestServer(t, withMediaService(t), withHiddenAuth(t))
    visible := server.seedGPS(t, "v.jpg", 10.0, 20.0)
    hidden := server.seedGPS(t, "h.jpg", 30.0, 40.0)
    server.hide(t, hidden.ID)

    cookie := server.unlockHidden(t)
    req := httptest.NewRequest(http.MethodGet, "/api/v1/media/geo?include_hidden=true", nil)
    req.AddCookie(cookie)
    rec := server.serveAuth(t, req)
    require.Equal(t, http.StatusOK, rec.Code)

    var body struct {
        Items []map[string]any `json:"items"`
    }
    require.NoError(t, json.NewDecoder(rec.Body).Decode(&body))
    require.Len(t, body.Items, 2)

    ids := []any{body.Items[0]["id"], body.Items[1]["id"]}
    require.Contains(t, ids, visible.ID)
    require.Contains(t, ids, hidden.ID)
}

// Route conflict regression: GET /api/v1/media/geo MUST resolve to the
// geo handler, not GET /api/v1/media/{id} with id="geo". huma's route
// dispatch is order-sensitive; this test pins behavior so a future
// reorder doesn't silently break the geo endpoint.
func TestGeoRoute_DoesNotCollideWithGetMediaByID(t *testing.T) {
    t.Helper()
    server := newTestServer(t, withMediaService(t))
    rec := server.do(t, http.MethodGet, "/api/v1/media/geo", nil)
    // Geo handler returns 200 with items even on empty DB.
    require.Equal(t, http.StatusOK, rec.Code)

    var body struct {
        Items *[]any `json:"items"`
    }
    require.NoError(t, json.NewDecoder(rec.Body).Decode(&body))
    require.NotNil(t, body.Items, "items array present means geo handler ran (detail handler would 404 with no items field)")
}
```

(Helpers `newTestServer`, `withMediaService`, `withHiddenAuth`, `seedGPS`, `hide`, `unlockHidden`, `serveAuth`, `do` are existing or follow the pattern in `internal/httpapi/hidden_media_test.go` and `albums_test.go`. If a helper is missing, copy the most-similar existing version and adapt.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -tags sqlite_fts5 ./internal/httpapi -run TestGeoRoute -v`
Expected: FAIL — route not registered, returns 404 or detail-handler 503.

- [ ] **Step 3: Implement the handler**

Create `internal/httpapi/media_geo.go`:

```go
package httpapi

import (
    "context"
    "net/http"

    "github.com/danielgtaylor/huma/v2"

    "github.com/wesm/fotobank/internal/auth/hidden"
    "github.com/wesm/fotobank/internal/errs"
    "github.com/wesm/fotobank/internal/service"
)

type listMediaGeoInput struct {
    IncludeHidden bool `query:"include_hidden" doc:"Return hidden geotagged media; requires a valid hidden-unlock cookie."`
}

type listMediaGeoOutput struct {
    Body struct {
        Items []mediaDTO `json:"items"`
    }
}

// registerMediaGeo wires GET /api/v1/media/geo. Returns the full owner-
// scoped set of geotagged primaries (sidecars excluded). When the
// caller passes ?include_hidden=true, the handler validates the
// hidden-unlock cookie before delegating to the service.
//
// Both svc and hiddenAuth may be nil; when either is nil the handler
// answers 503 so the OpenAPI dumper can pass an empty Deps and still
// emit the route shape.
func registerMediaGeo(api huma.API, svc *service.MediaService, hiddenAuth *hidden.Service) {
    huma.Register(api, huma.Operation{
        OperationID: "list-media-geo",
        Method:      http.MethodGet,
        Path:        "/api/v1/media/geo",
        Summary:     "List the caller's geotagged media (primaries only).",
    }, func(ctx context.Context, in *listMediaGeoInput) (*listMediaGeoOutput, error) {
        if svc == nil {
            return nil, huma.Error503ServiceUnavailable("media service not configured")
        }
        id, ok := IdentityFromContext(ctx)
        if !ok {
            return nil, huma.Error401Unauthorized(errs.ErrIdentityMissing.Error())
        }
        caller := id.Principal.OwnersPrincipal()

        if in.IncludeHidden {
            if hiddenAuth == nil {
                return nil, huma.Error503ServiceUnavailable("hidden media service not configured")
            }
            claim, hasClaim := hidden.UnlockClaimFromContext(ctx)
            if !hasClaim || claim.Principal != caller {
                return nil, huma.Error403Forbidden("unlock cookie required")
            }
        }

        rows, err := svc.ListGeo(ctx, caller, in.IncludeHidden)
        if err != nil {
            return nil, Translate(err)
        }
        out := &listMediaGeoOutput{}
        out.Body.Items = make([]mediaDTO, 0, len(rows))
        for _, m := range rows {
            out.Body.Items = append(out.Body.Items, toMediaDTO(m))
        }
        return out, nil
    })
}
```

Edit `internal/httpapi/api.go::buildAPI` to register the new route. Place the call between the existing `registerHiddenMedia` and `registerAIRoutes` lines:

```go
    registerHiddenMedia(api, deps.MediaService, deps.HiddenAuth)
    registerMediaGeo(api, deps.MediaService, deps.HiddenAuth)
    registerAIRoutes(api, deps.AIService, deps.AIVisionProbe, deps.AIEnabled)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -tags sqlite_fts5 ./internal/httpapi -run TestGeoRoute -v`
Expected: PASS for all five cases.

Then full httpapi suite:
Run: `go test -tags sqlite_fts5 ./internal/httpapi -v`
Expected: PASS.

- [ ] **Step 5: Commit**

`make api-generate` will pick up the new route and update `openapi.json` + `frontend/src/lib/api/generated/schema.ts`.

Run:
```bash
git add internal/httpapi/media_geo.go internal/httpapi/media_geo_test.go internal/httpapi/api.go internal/httpapi/openapi.json frontend/src/lib/api/generated/schema.ts
git commit -m "feat(httpapi): GET /api/v1/media/geo for owner-scoped geotagged primaries"
```

---

## Section C — Frontend foundation: appConfig store + tiles module

### Task C1: Add `appConfig` store and load on App.svelte mount

**Files:**
- Create: `frontend/src/lib/app/appConfig.svelte.ts`
- Create: `frontend/src/lib/app/appConfig.test.ts`
- Modify: `frontend/src/App.svelte`

- [ ] **Step 1: Write the failing test**

Create `frontend/src/lib/app/appConfig.test.ts`:

```ts
import { describe, expect, it, vi, beforeEach } from "vitest";
import { AppConfigStore } from "./appConfig.svelte";

beforeEach(() => {
  vi.restoreAllMocks();
});

describe("AppConfigStore", () => {
  it("starts with sharingEnabled=false and ready=false", () => {
    const s = new AppConfigStore();
    expect(s.sharingEnabled).toBe(false);
    expect(s.ready).toBe(false);
  });

  it("flips sharingEnabled when /me reports true", async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({
        principal: { hub: "h", user_id: "u" },
        scopes: [],
        features: { sharing_enabled: true },
      }),
    });
    vi.stubGlobal("fetch", fetchMock);

    const s = new AppConfigStore();
    await s.load();

    expect(s.ready).toBe(true);
    expect(s.sharingEnabled).toBe(true);
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/me");
  });

  it("treats a /me failure as default-disabled", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue({ ok: false, status: 500 }));
    const s = new AppConfigStore();
    await s.load();
    // Failed loads do NOT flip ready=true; the SPA continues to render
    // the safer default (sharing UI hidden).
    expect(s.ready).toBe(false);
    expect(s.sharingEnabled).toBe(false);
  });

  it("treats a missing features.sharing_enabled as false", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({ principal: { hub: "h", user_id: "u" }, scopes: [] }),
    }));
    const s = new AppConfigStore();
    await s.load();
    expect(s.ready).toBe(true);
    expect(s.sharingEnabled).toBe(false);
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd frontend && bun run test src/lib/app/appConfig.test.ts`
Expected: FAIL — module does not exist.

- [ ] **Step 3: Implement the store**

Create `frontend/src/lib/app/appConfig.svelte.ts`:

```ts
// frontend/src/lib/app/appConfig.svelte.ts
//
// Reactive snapshot of /api/v1/me's `features` block, fetched once on
// App.svelte mount. Components read `appConfig.sharingEnabled`
// reactively to gate UI surfaces (Sidebar, MediaActions, Album CTAs,
// /shares route).
//
// Defensive defaults: sharingEnabled=false, ready=false. A failed or
// pending load leaves the SPA on the safer default (sharing UI hidden),
// not the more permissive one.

export class AppConfigStore {
  private _sharingEnabled = $state(false);
  private _ready = $state(false);

  get sharingEnabled(): boolean {
    return this._sharingEnabled;
  }

  get ready(): boolean {
    return this._ready;
  }

  async load(): Promise<void> {
    try {
      const resp = await fetch("/api/v1/me");
      if (!resp.ok) return;
      const body = (await resp.json()) as {
        features?: { sharing_enabled?: boolean };
      };
      this._sharingEnabled = body?.features?.sharing_enabled === true;
      this._ready = true;
    } catch {
      // Network failure on app boot leaves ready=false; the next
      // call to load() (e.g. via a manual reload) will retry.
    }
  }
}

export const appConfig = new AppConfigStore();
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd frontend && bun run test src/lib/app/appConfig.test.ts`
Expected: PASS for all four cases.

- [ ] **Step 5: Wire from `App.svelte` and commit**

Edit `frontend/src/App.svelte`. At the top of the `<script>` block (after existing imports), add:

```ts
import { appConfig } from "./lib/app/appConfig.svelte";
import { onMount } from "svelte";
```

(`onMount` may already be imported; if so, skip that line.)

Add an effect at the bottom of the script (before the existing `function activeId(...)`):

```ts
  onMount(() => {
    void appConfig.load();
  });
```

Run:
```bash
cd frontend && bun run test src/lib/app/
cd ..
git add frontend/src/lib/app/ frontend/src/App.svelte
git commit -m "feat(frontend/app): appConfig store loaded from /me on App mount"
```

### Task C2: Add `tiles.ts` map module

**Files:**
- Create: `frontend/src/lib/map/tiles.ts`
- Create: `frontend/src/lib/map/tiles.test.ts`

- [ ] **Step 1: Write the failing test**

Create `frontend/src/lib/map/tiles.test.ts`:

```ts
import { describe, expect, it } from "vitest";
import { tileUrl, attribution, defaultMaxZoom } from "./tiles";

describe("tiles", () => {
  it("returns the OSM raster tile URL pattern", () => {
    expect(tileUrl()).toBe("https://tile.openstreetmap.org/{z}/{x}/{y}.png");
  });

  it("returns OSM attribution string", () => {
    expect(attribution()).toContain("OpenStreetMap");
    expect(attribution()).toContain("contributors");
  });

  it("exports a default max zoom", () => {
    // OSM standard tiles top out at z=19.
    expect(defaultMaxZoom).toBe(19);
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd frontend && bun run test src/lib/map/tiles.test.ts`
Expected: FAIL — module does not exist.

- [ ] **Step 3: Implement the module**

Create `frontend/src/lib/map/tiles.ts`:

```ts
// frontend/src/lib/map/tiles.ts
//
// Single source of truth for the SPA's map tile URL and attribution.
// Centralizing the URL satisfies the OSMF tile-policy recommendation
// against hardcoding the URL across an app, and makes provider
// migration (self-hosted, paid CDN, etc.) one-file.
//
// Reference: https://operations.osmfoundation.org/policies/tiles/

export function tileUrl(): string {
  return "https://tile.openstreetmap.org/{z}/{x}/{y}.png";
}

export function attribution(): string {
  return '© <a href="https://www.openstreetmap.org/copyright" target="_blank" rel="noopener">OpenStreetMap</a> contributors';
}

// Standard OSM raster tiles top out at zoom 19. Pinned here so the
// MapPane can pass it to Leaflet's L.tileLayer({ maxZoom }).
export const defaultMaxZoom = 19;
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd frontend && bun run test src/lib/map/tiles.test.ts`
Expected: PASS.

- [ ] **Step 5: Commit**

Run:
```bash
git add frontend/src/lib/map/tiles.ts frontend/src/lib/map/tiles.test.ts
git commit -m "feat(frontend/map): centralized tiles.ts module (OSM)"
```

---

## Section D — Sharing UI flag-gate (frontend)

### Task D1: Gate the Sidebar Shares entry

**Files:**
- Modify: `frontend/src/lib/components/Sidebar.svelte`
- Modify: `frontend/src/lib/components/Sidebar.test.ts`

- [ ] **Step 1: Write the failing test**

Append to `frontend/src/lib/components/Sidebar.test.ts`:

```ts
import { render } from "@testing-library/svelte";
import { describe, expect, it } from "vitest";
import Sidebar from "./Sidebar.svelte";
import { appConfig } from "../app/appConfig.svelte";

describe("Sidebar — sharing gate", () => {
  it("hides the Shares entry when appConfig.sharingEnabled is false", () => {
    // appConfig defaults to sharingEnabled=false on import.
    expect(appConfig.sharingEnabled).toBe(false);
    const { queryByText } = render(Sidebar, { props: { active: "" } });
    expect(queryByText("Shares")).toBeNull();
  });

  it("shows the Shares entry when appConfig.sharingEnabled flips to true", async () => {
    // Stub /me before construction so the store flips before render.
    globalThis.fetch = (async () => ({
      ok: true,
      json: async () => ({ principal: { hub: "h", user_id: "u" }, scopes: [], features: { sharing_enabled: true } }),
    })) as unknown as typeof fetch;
    await appConfig.load();

    const { findByText } = render(Sidebar, { props: { active: "" } });
    expect(await findByText("Shares")).toBeTruthy();
  });
});
```

(If the existing `Sidebar.test.ts` doesn't yet exist, create it; or merge into the existing one.)

- [ ] **Step 2: Run test to verify it fails**

Run: `cd frontend && bun run test src/lib/components/Sidebar.test.ts`
Expected: FAIL — Shares entry currently always renders.

- [ ] **Step 3: Gate the entry**

Edit `frontend/src/lib/components/Sidebar.svelte`. Import `appConfig`:

```ts
  import { appConfig } from "../app/appConfig.svelte";
```

Wrap the existing Shares `<a>` element in an `{#if appConfig.sharingEnabled}` block. (If the file uses an array of nav items rather than literal anchors, add a `disabled` flag in the items array and skip when `disabled === true`. Do not introduce a separate "disabled" rendering — completely omit.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd frontend && bun run test src/lib/components/Sidebar.test.ts`
Expected: PASS.

- [ ] **Step 5: Commit**

Run:
```bash
git add frontend/src/lib/components/Sidebar.svelte frontend/src/lib/components/Sidebar.test.ts
git commit -m "feat(frontend/sidebar): gate Shares entry on appConfig.sharingEnabled"
```

### Task D2: Gate the MediaActions Share button

**Files:**
- Modify: `frontend/src/lib/components/MediaActions.svelte`
- Modify: `frontend/src/lib/components/MediaActions.test.ts`

- [ ] **Step 1: Write the failing test**

Append to `frontend/src/lib/components/MediaActions.test.ts`:

```ts
import { render } from "@testing-library/svelte";
import { describe, expect, it } from "vitest";
import MediaActions from "./MediaActions.svelte";

describe("MediaActions — sharing gate", () => {
  it("does not render Share when appConfig.sharingEnabled is false", () => {
    // (appConfig default is false; reset between tests if needed.)
    const { queryByRole } = render(MediaActions, {
      props: mediaActionsBaselineProps(),
    });
    expect(queryByRole("button", { name: /share/i })).toBeNull();
  });
});

// Tiny helper: returns the minimum prop set MediaActions accepts when
// no selection is active. Adjust to the component's actual prop shape.
function mediaActionsBaselineProps() {
  return {
    selectionCount: 0,
    onShare: () => {},
    onDelete: () => {},
  };
}
```

(If the existing `MediaActions.test.ts` doesn't have a fixture builder, add one based on the component's actual props.)

- [ ] **Step 2: Run test to verify it fails**

Run: `cd frontend && bun run test src/lib/components/MediaActions.test.ts`
Expected: FAIL — Share button still renders.

- [ ] **Step 3: Gate the Share button**

Edit `frontend/src/lib/components/MediaActions.svelte`. Import `appConfig` and wrap the Share `<button>` in `{#if appConfig.sharingEnabled}`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd frontend && bun run test src/lib/components/MediaActions.test.ts`
Expected: PASS.

- [ ] **Step 5: Commit**

Run:
```bash
git add frontend/src/lib/components/MediaActions.svelte frontend/src/lib/components/MediaActions.test.ts
git commit -m "feat(frontend/media-actions): gate Share button on sharingEnabled"
```

### Task D3: Gate the AlbumDetail Share Album button + fix delete-blocked copy

**Files:**
- Modify: `frontend/src/routes/AlbumDetail.svelte`
- Modify: `frontend/src/routes/AlbumDetail.test.ts`

- [ ] **Step 1: Write the failing tests**

Append to `frontend/src/routes/AlbumDetail.test.ts`:

```ts
describe("AlbumDetail — sharing gate", () => {
  it("does not render the Share album button when sharing UI is disabled", () => {
    // appConfig defaults to false.
    const { queryByRole } = renderAlbumDetail(); // existing helper
    expect(queryByRole("button", { name: /share album/i })).toBeNull();
  });

  it("delete-blocked-by-share copy references the CLI command, not a UUID", async () => {
    // Force album-delete to surface the share-conflict path. Existing
    // tests have a helper for this (e.g. `simulateDeleteShareConflict`);
    // if not, mock the api response to return errs.ErrAlreadyShared.
    const { findByText } = renderAlbumDetailWithDeleteShareConflict("album-123");
    const message = await findByText(/active CLI shares/i);
    expect(message.textContent).toContain("fotobank shares list --album");
    expect(message.textContent).toContain("album-123");
  });
});
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd frontend && bun run test src/routes/AlbumDetail.test.ts`
Expected: FAIL on both cases.

- [ ] **Step 3: Apply the gating and copy update**

Edit `frontend/src/routes/AlbumDetail.svelte`:

1. Import `appConfig` at the top.
2. Wrap the "Share album" button in `{#if appConfig.sharingEnabled}`.
3. Find the existing delete-blocked-by-share toast/inline message. Replace its template literal with:

```ts
const blockedMessage = `This album has active CLI shares. Run \`fotobank shares list --album ${album.id}\` and revoke them first.`;
```

(Use the album's actual ID variable name in scope. The CLI fragment must remain copy-pastable, so the backticks and `--album` flag are part of the rendered text, not template syntax.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd frontend && bun run test src/routes/AlbumDetail.test.ts`
Expected: PASS.

- [ ] **Step 5: Commit**

Run:
```bash
git add frontend/src/routes/AlbumDetail.svelte frontend/src/routes/AlbumDetail.test.ts
git commit -m "feat(frontend/album): gate Share album button + fix delete-blocked copy"
```

### Task D4: Skip mounting `ShareModal` when sharing UI is disabled

**Files:**
- Modify: `frontend/src/routes/AlbumDetail.svelte`
- Modify: `frontend/src/routes/Library.svelte` (only if it mounts ShareModal — check first)

- [ ] **Step 1: Identify all `<ShareModal>` and `<ShareDrawer>` mount points**

Run: `grep -rn "<ShareModal\|<ShareDrawer" frontend/src/`
Record each file path. The expected set per the spec is `AlbumDetail.svelte`, `Library.svelte` (if it mounts a drawer), `MediaDetail.svelte`. Confirm before editing.

- [ ] **Step 2: Wrap each mount in `{#if appConfig.sharingEnabled}`**

For every file from Step 1, import `appConfig` (if not already) and wrap the `<ShareModal …>` / `<ShareDrawer …>` element:

```svelte
{#if appConfig.sharingEnabled}
  <ShareModal … />
{/if}
```

The state-driving variables (e.g. `shareModalOpen`) can stay; they simply never become `true` when the trigger is gated, and the modal element is unreachable when `sharingEnabled=false`.

- [ ] **Step 3: Build the SPA to confirm there are no orphan refs**

Run: `cd frontend && bun run typecheck && bun run test`
Expected: typecheck passes; existing tests still pass.

- [ ] **Step 4: Commit**

Run:
```bash
git add frontend/src/routes/
git commit -m "feat(frontend): skip mounting ShareModal when sharingEnabled=false"
```

### Task D5: Redirect `/shares` to `/` when sharing UI is disabled

**Files:**
- Modify: `frontend/src/App.svelte`
- Modify: `frontend/src/App.test.ts` (or create if absent — check first)

- [ ] **Step 1: Write the failing test**

Inspect existing routing tests under `frontend/src/`. If `App.test.ts` exists with a routing fixture, append:

```ts
describe("App route gate — sharing", () => {
  it("redirects /shares to / when sharingEnabled=false", async () => {
    // appConfig defaults to false.
    history.replaceState({}, "", "/shares");
    render(App);
    // The router runs synchronously on mount; appConfig.load is async
    // but its default state already holds the gating decision.
    await tick();
    expect(window.location.pathname).toBe("/");
  });
});
```

If no such test file exists, create `frontend/src/App.test.ts` with the minimum harness needed (mirror `frontend/src/routes/SettingsAI.test.ts` for module setup).

- [ ] **Step 2: Run test to verify it fails**

Run: `cd frontend && bun run test src/App.test.ts`
Expected: FAIL — `/shares` is currently accessible.

- [ ] **Step 3: Add a route gate**

Edit `frontend/src/App.svelte`. Above the routing block (`{#if router.current.route === "library"}`), add:

```svelte
{#if router.current.route === "shares" && appConfig.ready && !appConfig.sharingEnabled}
  {@const _ = router.navigate("/", { replace: true })}
{/if}
```

(`{@const}` runs the redirect as a side-effect during template evaluation. The `appConfig.ready` guard prevents a flash redirect when the SPA boots before `/me` lands. Until ready, `/shares` shows the old behavior briefly; once ready, the redirect fires.)

A cleaner alternative if Svelte 5 syntax for `{@const}` fights you: lift the redirect into the `<script>` block as an `$effect`:

```ts
$effect(() => {
  if (
    router.current.route === "shares" &&
    appConfig.ready &&
    !appConfig.sharingEnabled
  ) {
    router.navigate("/", { replace: true });
  }
});
```

Use whichever shape passes type-check.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd frontend && bun run test src/App.test.ts`
Expected: PASS.

- [ ] **Step 5: Commit**

Run:
```bash
git add frontend/src/App.svelte frontend/src/App.test.ts
git commit -m "feat(frontend/app): redirect /shares to / when sharingEnabled=false"
```

---

**Cleanup checkpoint:** Tasks A1–D5 complete. Invoke `/roborev-fix` on master before continuing.

---

## Section E — Map view: lightbox source extension

### Task E1: Add `"map"` to `LightboxSource`, `parseFrom`, and snapshot shape

**Files:**
- Modify: `frontend/src/lib/lightbox/lightboxSession.svelte.ts`
- Modify: `frontend/src/lib/router/router.svelte.ts`
- Test: `frontend/src/lib/lightbox/lightboxSession.test.ts` (extend existing)
- Test: `frontend/src/lib/router/router.test.ts` (extend existing)

- [ ] **Step 1: Write the failing tests**

Append to `frontend/src/lib/lightbox/lightboxSession.test.ts` (or create one if absent — model on `lightboxNav.test.ts`):

```ts
import { describe, expect, it } from "vitest";
import { LightboxSessionStore } from "./lightboxSession.svelte";

describe("LightboxSessionStore — map source", () => {
  it("accepts a map source variant with includeHidden", () => {
    const s = new LightboxSessionStore();
    s.open({
      source: { kind: "map" },
      navIds: ["a", "b", "c"],
      selected: false,
      scrollY: 0,
      returnFocusMediaId: null,
      returnHref: "/map?z=10&c=37,-122",
      includeHidden: false,
    });
    expect(s.snapshot?.source.kind).toBe("map");
    expect(s.snapshot?.includeHidden).toBe(false);
  });
});
```

Append to `frontend/src/lib/router/router.test.ts`:

```ts
describe("parseFrom — map", () => {
  it("accepts from=map", () => {
    const router = createRouter();
    router.navigate("/media/abc?from=map");
    const r = router.current;
    expect(r.route).toBe("media");
    if (r.route === "media") {
      expect(r.from).toBe("map");
    }
  });
});
```

- [ ] **Step 2: Run tests to verify they fail**

Run:
```bash
cd frontend && bun run test src/lib/lightbox/lightboxSession.test.ts src/lib/router/router.test.ts
```
Expected: FAIL — `"map"` is not in the union.

- [ ] **Step 3: Extend the types and parser**

Edit `frontend/src/lib/lightbox/lightboxSession.svelte.ts`:

```ts
export type LightboxSource =
  | { kind: "library" }
  | { kind: "sessions" }
  | { kind: "album"; albumId: string }
  | { kind: "hidden" }
  | { kind: "search" }
  | { kind: "map" };

export type LightboxSnapshot = {
  source: LightboxSource;
  navIds: string[];
  selected: boolean;
  scrollY: number;
  returnFocusMediaId: string | null;
  returnHref: string;
  // includeHidden is set when navIds came from a hidden-aware fetch
  // (currently only produced by the map page when the user has the
  // Include-hidden toggle on AND has a valid unlock claim). Other
  // sources leave it false. The lightbox uses it to decide whether
  // opening a hidden row from from=map is a leak (it isn't, when
  // includeHidden was honored upstream).
  includeHidden?: boolean;
  // Existing optional fields (qHash, scoreComponentsById from the
  // search source) stay as-is.
  qHash?: string;
  scoreComponentsById?: Map<string, unknown>;
};
```

(Preserve existing optional fields like `qHash`, `scoreComponentsById`. Only add `"map"` and `includeHidden`.)

Edit `frontend/src/lib/router/router.svelte.ts::parseFrom`:

```ts
function parseFrom(raw: string | null): string | null {
  if (raw === null) return null;
  if (
    raw === "library" ||
    raw === "sessions" ||
    raw === "hidden" ||
    raw === "search" ||
    raw === "map"
  )
    return raw;
  if (raw.startsWith("album:") && raw.length > "album:".length) return raw;
  return null;
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run:
```bash
cd frontend && bun run test src/lib/lightbox/ src/lib/router/
```
Expected: PASS.

- [ ] **Step 5: Commit**

Run:
```bash
git add frontend/src/lib/lightbox/lightboxSession.svelte.ts frontend/src/lib/router/router.svelte.ts frontend/src/lib/lightbox/lightboxSession.test.ts frontend/src/lib/router/router.test.ts
git commit -m "feat(frontend/lightbox): add map source variant + includeHidden snapshot field"
```

### Task E2: Add `case "map"` to `Lightbox.svelte`'s source switch + relax `hiddenCrossContext`

**Files:**
- Modify: `frontend/src/lib/components/lightbox/Lightbox.svelte`

- [ ] **Step 1: Add `case "map"` to `fromMatchesSession`**

Edit `frontend/src/lib/components/lightbox/Lightbox.svelte`. In the `fromMatchesSession` switch:

```ts
      case "map":
        return from === "map";
```

Place it next to `case "search":` (preserving alphabetical or arrival order — match the existing project convention).

- [ ] **Step 2: Relax `hiddenCrossContext`**

Locate:

```ts
  const hiddenCrossContext = $derived(isHidden && from !== "hidden");
```

Replace with:

```ts
  // hiddenCrossContext: a hidden row was opened with a non-hidden
  // `from` — render via DirectMediaDetail so the unhide flow runs in
  // the standalone surface (Lightbox is library-shaped, not hidden).
  // Exception: from=map combined with a snapshot that was fetched
  // under explicit include_hidden=true is NOT a cross-context leak
  // — the user explicitly opted in via the map's Include-hidden
  // toggle, and the resulting nav set is hidden-aware.
  const fromMapWithHidden = $derived(
    from === "map" && session?.includeHidden === true,
  );
  const hiddenCrossContext = $derived(
    isHidden && from !== "hidden" && !fromMapWithHidden,
  );
```

- [ ] **Step 3: Run frontend tests**

Run: `cd frontend && bun run typecheck && bun run test src/lib/components/lightbox/`
Expected: PASS — no test changes; only behavior added under from=map condition (which existing tests don't exercise).

- [ ] **Step 4: Add a focused test**

Append to `frontend/src/lib/components/lightbox/Lightbox.test.ts` (or create the closest sibling test if missing):

```ts
describe("Lightbox.hiddenCrossContext — map source", () => {
  it("does not flag from=map with includeHidden as a cross-context leak", () => {
    // Mount a Lightbox with a hidden row and a map snapshot whose
    // includeHidden=true. Assert hiddenCrossContext is false (i.e.
    // the in-place lightbox renders, not DirectMediaDetail).
    // Implementation note: the easiest path is to expose
    // `hiddenCrossContext` from a small driver harness. If the
    // production code hides it, render Lightbox and assert that
    // the lightbox <article> element appears (not the <DirectMediaDetail>).
    const harness = mountLightboxHarness({
      mediaId: "hidden-1",
      hiddenAt: new Date(),
      from: "map",
      session: {
        source: { kind: "map" },
        navIds: ["hidden-1"],
        selected: false,
        scrollY: 0,
        returnFocusMediaId: null,
        returnHref: "/map?z=10&c=0,0",
        includeHidden: true,
      },
    });
    expect(harness.directDetailRendered).toBe(false);
    expect(harness.lightboxRendered).toBe(true);
  });

  it("flags from=library on a hidden row as cross-context", () => {
    const harness = mountLightboxHarness({
      mediaId: "hidden-1",
      hiddenAt: new Date(),
      from: "library",
      session: null,
    });
    expect(harness.directDetailRendered).toBe(true);
  });
});
```

(`mountLightboxHarness` is a small test helper. If one already exists in the project, use it. If not, the simplest implementation renders `<Lightbox …>` with seeded media-store entries and queries `.lightbox` / `.direct-media-detail` selectors that the production templates already use.)

- [ ] **Step 5: Run tests and commit**

Run: `cd frontend && bun run test src/lib/components/lightbox/`
Expected: PASS.

Run:
```bash
git add frontend/src/lib/components/lightbox/Lightbox.svelte frontend/src/lib/components/lightbox/Lightbox.test.ts
git commit -m "feat(frontend/lightbox): map source + hidden-cross-context exception"
```

### Task E3: Add `case "map"` to `LightboxActions.svelte` source switch

**Files:**
- Modify: `frontend/src/lib/components/lightbox/LightboxActions.svelte`

- [ ] **Step 1: Inspect the existing switch**

Open `frontend/src/lib/components/lightbox/LightboxActions.svelte`. Find the `switch (source.kind)` block. Confirm what `case "library":` returns — that is the verbs `case "map":` will mirror.

- [ ] **Step 2: Add the `case "map":` branch**

Add a `case "map":` immediately above (or below, mirroring file convention) `case "library":` that returns the **same** result. The simplest way is fall-through:

```ts
      case "map":
      case "library":
        return [...librarySharedVerbs, downloadVerb, ...maybeShareVerb];
```

(Use the actual variable names from the file. The point is: `map` returns the same array as `library`.)

The `Unhide` verb is already conditional on `media.hidden_at != null` per the spec — confirm this is true in the file. If it currently keys off `source.kind === "hidden"`, change the condition to `media.hidden_at != null` (the spec calls this out explicitly).

- [ ] **Step 3: Add a focused test**

Append to `frontend/src/lib/components/lightbox/LightboxActions.test.ts`:

```ts
describe("LightboxActions — map source", () => {
  it("returns the same verbs as library for from=map (sharing-disabled)", () => {
    const lib = computeVerbs({ kind: "library" }, baseMedia());
    const map = computeVerbs({ kind: "map" }, baseMedia());
    expect(map).toEqual(lib);
  });

  it("includes Unhide on hidden media regardless of source", () => {
    const verbs = computeVerbs({ kind: "map" }, { ...baseMedia(), hidden_at: new Date().toISOString() });
    const labels = verbs.map((v) => v.label);
    expect(labels).toContain("Unhide");
  });
});

function baseMedia() {
  return { id: "x", hidden_at: null /* …minimal media…*/ };
}
```

(`computeVerbs` is the existing helper exported by `LightboxActions.svelte` for testing. If the file doesn't export one, add a small `export function _computeVerbsForTest(...)` and call that.)

- [ ] **Step 4: Run tests**

Run: `cd frontend && bun run test src/lib/components/lightbox/LightboxActions.test.ts`
Expected: PASS.

- [ ] **Step 5: Commit**

Run:
```bash
git add frontend/src/lib/components/lightbox/LightboxActions.svelte frontend/src/lib/components/lightbox/LightboxActions.test.ts
git commit -m "feat(frontend/lightbox-actions): map source returns library verbs; Unhide keys off media.hidden_at"
```

---

## Section F — Map view: page + components

### Task F1: Install Leaflet and add the geo store

**Files:**
- Modify: `frontend/package.json`
- Create: `frontend/src/lib/map/geoStore.svelte.ts`
- Create: `frontend/src/lib/map/geoStore.test.ts`

- [ ] **Step 1: Add Leaflet deps**

Run:
```bash
cd frontend && bun add leaflet@1.9 leaflet.markercluster@1.5
cd frontend && bun add -d @types/leaflet @types/leaflet.markercluster
```

(Pin minor versions to avoid silent CSS/API drift.)

- [ ] **Step 2: Write the failing test**

Create `frontend/src/lib/map/geoStore.test.ts`:

```ts
import { describe, expect, it, vi, beforeEach } from "vitest";
import { GeoStore } from "./geoStore.svelte";

beforeEach(() => {
  vi.restoreAllMocks();
});

describe("GeoStore", () => {
  it("loads visible-only by default and exposes items", async () => {
    const fetchMock = vi.fn().mockImplementation((url: string) => {
      expect(url).toBe("/api/v1/media/geo");
      return Promise.resolve({
        ok: true,
        json: async () => ({ items: [{ id: "a", latitude: 1, longitude: 2 }] }),
      });
    });
    vi.stubGlobal("fetch", fetchMock);

    const s = new GeoStore();
    await s.load(false);

    expect(s.items.length).toBe(1);
    expect(s.includedHiddenAtFetch).toBe(false);
  });

  it("loads with include_hidden=true when requested and records the flag", async () => {
    const fetchMock = vi.fn().mockImplementation((url: string) => {
      expect(url).toBe("/api/v1/media/geo?include_hidden=true");
      return Promise.resolve({
        ok: true,
        json: async () => ({ items: [{ id: "h", latitude: 3, longitude: 4 }] }),
      });
    });
    vi.stubGlobal("fetch", fetchMock);

    const s = new GeoStore();
    await s.load(true);

    expect(s.items.length).toBe(1);
    expect(s.includedHiddenAtFetch).toBe(true);
  });

  it("findById returns the cached item or undefined", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({ items: [{ id: "a", latitude: 1, longitude: 2 }] }),
    }));
    const s = new GeoStore();
    await s.load(false);

    expect(s.findById("a")?.latitude).toBe(1);
    expect(s.findById("missing")).toBeUndefined();
  });
});
```

- [ ] **Step 3: Run test to verify it fails**

Run: `cd frontend && bun run test src/lib/map/geoStore.test.ts`
Expected: FAIL — module does not exist.

- [ ] **Step 4: Implement the store**

Create `frontend/src/lib/map/geoStore.svelte.ts`:

```ts
// frontend/src/lib/map/geoStore.svelte.ts
//
// Cache of GET /api/v1/media/geo, populated on /map mount and on
// explicit reload (e.g. when the user toggles "Include hidden" on
// while unlocked). The store also serves the lightbox map pin's
// focus-retry path.

import type { Media } from "../media/types";
import { toMedia } from "../media/types";

export class GeoStore {
  private _items = $state<Media[]>([]);
  private _ready = $state(false);
  private _error = $state<string | null>(null);
  private _includedHidden = $state(false);

  get items(): Media[] {
    return this._items;
  }
  get ready(): boolean {
    return this._ready;
  }
  get error(): string | null {
    return this._error;
  }
  get includedHiddenAtFetch(): boolean {
    return this._includedHidden;
  }

  async load(includeHidden: boolean): Promise<void> {
    this._error = null;
    const url = includeHidden
      ? "/api/v1/media/geo?include_hidden=true"
      : "/api/v1/media/geo";
    try {
      const resp = await fetch(url);
      if (!resp.ok) {
        this._error = `geo fetch failed: ${resp.status}`;
        return;
      }
      const body = (await resp.json()) as { items: unknown[] };
      const parsed: Media[] = [];
      for (const raw of body.items ?? []) {
        const m = toMedia(raw as Record<string, unknown>);
        if (m) parsed.push(m);
      }
      this._items = parsed;
      this._includedHidden = includeHidden;
      this._ready = true;
    } catch (e) {
      this._error = e instanceof Error ? e.message : "network error";
    }
  }

  findById(id: string): Media | undefined {
    return this._items.find((m) => m.id === id);
  }
}
```

(Replace `import { toMedia } from "../media/types"` with the actual path / function name in your project — check `frontend/src/lib/media/`. The store relies on the existing `Media` TS type.)

- [ ] **Step 5: Run tests and commit**

Run: `cd frontend && bun run test src/lib/map/geoStore.test.ts`
Expected: PASS.

Run:
```bash
git add frontend/package.json frontend/bun.lock frontend/src/lib/map/geoStore.svelte.ts frontend/src/lib/map/geoStore.test.ts
git commit -m "feat(frontend/map): leaflet deps + GeoStore (cached /media/geo)"
```

### Task F2: Add `/map` route and `Map.svelte` shell

**Files:**
- Modify: `frontend/src/lib/router/router.svelte.ts` (add `/map` pattern + route type)
- Create: `frontend/src/routes/Map.svelte`
- Create: `frontend/src/routes/Map.test.ts`
- Modify: `frontend/src/App.svelte` (mount the route)
- Modify: `frontend/src/lib/components/Sidebar.svelte` (add Map link)

- [ ] **Step 1: Add the route to the router**

Edit `frontend/src/lib/router/router.svelte.ts`. Extend `RouteMatch`:

```ts
  | {
      route: "map";
      z?: number;
      c?: [number, number];
      focus?: string;
      tab?: "map" | "photos";
    }
```

Add the regex/builder to the `PATTERNS` array (place after the `/sessions` line):

```ts
  { re: /^\/map\/?$/, build: () => {
    const sp = new URLSearchParams(window.location.search);
    const zRaw = sp.get("z");
    const cRaw = sp.get("c");
    const focusRaw = sp.get("focus");
    const tabRaw = sp.get("tab");
    const z = zRaw !== null ? Number(zRaw) : NaN;
    const c = cRaw !== null ? cRaw.split(",").map(Number) : [];
    const tab = tabRaw === "photos" || tabRaw === "map" ? tabRaw : null;
    return {
      route: "map" as const,
      ...(Number.isFinite(z) ? { z } : {}),
      ...(c.length === 2 && c.every(Number.isFinite) ? { c: [c[0]!, c[1]!] as [number, number] } : {}),
      ...(focusRaw !== null && focusRaw !== "" ? { focus: focusRaw } : {}),
      ...(tab !== null ? { tab } : {}),
    };
  } },
```

- [ ] **Step 2: Update `activeId` and mount in App.svelte**

Edit `frontend/src/App.svelte`:

```ts
    if (route.route === "map") return "map";
```

(Place next to the other route-to-sidebar mappings.)

In the `{#snippet main()}` block, add:

```svelte
    {:else if router.current.route === "map"}
      <Map
        z={router.current.z}
        c={router.current.c}
        focus={router.current.focus}
        tab={router.current.tab}
        {mediaStore}
        {hiddenStore}
        {toastStore}
      />
```

(Place after the `sessions` branch and before `media`.)

Import:

```ts
  import Map from "./routes/Map.svelte";
```

- [ ] **Step 3: Add the Sidebar entry**

Edit `frontend/src/lib/components/Sidebar.svelte`. Add a new nav entry "Map" between Sessions and Albums:

```svelte
<a
  href="/map"
  class:active={active === "map"}
  onclick={(e) => handleInternalLinkClick(e, "/map")}
>Map</a>
```

(Use the same template the existing entries use — the snippet above is illustrative.)

- [ ] **Step 4: Create `Map.svelte` (shell only — Leaflet content lands in F3)**

Create `frontend/src/routes/Map.svelte`:

```svelte
<!-- frontend/src/routes/Map.svelte -->
<script lang="ts">
  import { onMount } from "svelte";
  import { GeoStore } from "../lib/map/geoStore.svelte";
  import { hiddenStore as importedHiddenStore } from "../lib/hidden/hiddenStore.svelte";
  import type { MediaStore } from "../lib/media/mediaStore.svelte";
  import type { ToastStore } from "../lib/toasts/toastStore.svelte";
  import type { HiddenStore } from "../lib/hidden/hiddenStore.svelte";

  let {
    z,
    c,
    focus,
    tab,
    mediaStore,
    hiddenStore,
    toastStore,
  }: {
    z?: number;
    c?: [number, number];
    focus?: string;
    tab?: "map" | "photos";
    mediaStore: MediaStore;
    hiddenStore: HiddenStore;
    toastStore: ToastStore;
  } = $props();

  const geo = new GeoStore();

  onMount(() => {
    void geo.load(false);
  });
</script>

<section class="map-page" data-testid="map-page">
  {#if !geo.ready && geo.error === null}
    <div class="loading">Loading your photo locations…</div>
  {:else if geo.error !== null}
    <div class="error">
      Couldn't load photo locations.
      <button onclick={() => geo.load(false)}>Retry</button>
    </div>
  {:else if geo.items.length === 0}
    <div class="empty">
      No geotagged photos in your library yet. Photos with GPS metadata will appear here as you import.
    </div>
  {:else}
    <!-- F3 fills in <MapPane /> and <MapGridPane />. -->
    <div data-testid="map-loaded">{geo.items.length} photos with GPS</div>
  {/if}
</section>

<style>
  .map-page {
    display: block;
    height: calc(100vh - var(--header-height, 56px));
  }
  .loading,
  .error,
  .empty {
    padding: 24px;
    color: var(--text-secondary, #6b7280);
  }
</style>
```

(Imports for `MediaStore`, `ToastStore`, `HiddenStore` may need adjustment to your project's actual paths.)

- [ ] **Step 5: Write smoke test, run, commit**

Create `frontend/src/routes/Map.test.ts`:

```ts
import { render, screen, waitFor } from "@testing-library/svelte";
import { describe, expect, it, vi } from "vitest";
import Map from "./Map.svelte";

describe("Map page shell", () => {
  it("renders the empty state when geo response has no items", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({ items: [] }),
    }));
    render(Map, { props: mapProps() });
    await waitFor(() => expect(screen.getByText(/no geotagged photos/i)).toBeTruthy());
  });

  it("renders the count when geo response has items", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({ items: [{ id: "a", latitude: 1, longitude: 2 }] }),
    }));
    render(Map, { props: mapProps() });
    await waitFor(() => expect(screen.getByText(/1 photos with GPS/)).toBeTruthy());
  });
});

function mapProps() {
  return {
    mediaStore: stubMediaStore(),
    hiddenStore: stubHiddenStore(),
    toastStore: stubToastStore(),
  };
}

// Stubs follow the existing test-store helper conventions — see
// frontend/src/routes/Library.test.ts for prior art and copy the
// pattern. If a shared helper exists (e.g. testStores.ts), use that.
```

Run:
```bash
cd frontend && bun run typecheck && bun run test src/routes/Map.test.ts
```
Expected: PASS.

Run:
```bash
git add frontend/src/lib/router/router.svelte.ts frontend/src/App.svelte frontend/src/lib/components/Sidebar.svelte frontend/src/routes/Map.svelte frontend/src/routes/Map.test.ts
git commit -m "feat(frontend/map): /map route shell + Sidebar entry"
```

### Task F3: Implement `MapPane.svelte` with Leaflet + clusters

**Files:**
- Create: `frontend/src/lib/map/MapPane.svelte`
- Create: `frontend/src/lib/map/MapPane.test.ts`
- Modify: `frontend/src/routes/Map.svelte` (mount MapPane)

- [ ] **Step 1: Create `MapPane.svelte`**

Create `frontend/src/lib/map/MapPane.svelte`:

```svelte
<!-- frontend/src/lib/map/MapPane.svelte -->
<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import L from "leaflet";
  import "leaflet/dist/leaflet.css";
  import "leaflet.markercluster";
  import "leaflet.markercluster/dist/MarkerCluster.css";
  import "leaflet.markercluster/dist/MarkerCluster.Default.css";
  import { tileUrl, attribution, defaultMaxZoom } from "./tiles";
  import type { Media } from "../media/types";

  let {
    items,
    initialZoom,
    initialCenter,
    focusId,
    onMarkerClick,
    onClusterClick,
    onViewportChange,
    onClearClusterFilter,
  }: {
    items: Media[];
    initialZoom?: number;
    initialCenter?: [number, number];
    focusId?: string;
    onMarkerClick: (id: string) => void;
    onClusterClick: (ids: string[], bounds: L.LatLngBounds) => void;
    onViewportChange: (visibleIds: string[]) => void;
    onClearClusterFilter: () => void;
  } = $props();

  let container: HTMLDivElement | null = $state(null);
  let map: L.Map | null = null;
  let cluster: L.MarkerClusterGroup | null = null;
  let markersById = new Map<string, L.Marker>();

  // Viewport-visible IDs are pushed to the parent on every moveend
  // (debounced upstream). Direct map clicks (not on a marker or
  // cluster) clear any active cluster filter.
  function emitViewportVisible(): void {
    if (map === null) return;
    const bounds = map.getBounds();
    const visible: string[] = [];
    for (const [id, marker] of markersById) {
      if (bounds.contains(marker.getLatLng())) visible.push(id);
    }
    onViewportChange(visible);
  }

  function buildMarkers(): void {
    if (cluster === null) return;
    cluster.clearLayers();
    markersById.clear();
    for (const m of items) {
      if (m.latitude == null || m.longitude == null) continue;
      const marker = L.marker([m.latitude, m.longitude]);
      marker.on("click", () => onMarkerClick(m.id));
      cluster.addLayer(marker);
      markersById.set(m.id, marker);
    }
  }

  onMount(() => {
    if (container === null) return;
    map = L.map(container, {
      zoomControl: true,
      attributionControl: true,
    });

    L.tileLayer(tileUrl(), {
      attribution: attribution(),
      maxZoom: defaultMaxZoom,
    }).addTo(map);

    cluster = L.markerClusterGroup();
    cluster.on("clusterclick", (e) => {
      const ids = e.layer
        .getAllChildMarkers()
        .map((mk: L.Marker) => {
          for (const [id, mm] of markersById) if (mm === mk) return id;
          return null;
        })
        .filter((x): x is string => x !== null);
      onClusterClick(ids, e.layer.getBounds());
      // Default behavior also zooms — keep it.
    });
    map.addLayer(cluster);

    map.on("moveend zoomend", emitViewportVisible);
    map.on("click", () => onClearClusterFilter());

    buildMarkers();

    // Initial view priority (per spec §3.1):
    //   1. focusId       → zoom 14 at that photo
    //   2. z + c         → use those
    //   3. otherwise     → fitBounds to all markers
    if (focusId !== undefined) {
      const found = items.find((m) => m.id === focusId);
      if (found && found.latitude != null && found.longitude != null) {
        map.setView([found.latitude, found.longitude], initialZoom ?? 14, { animate: false });
      } else if (initialCenter !== undefined && initialZoom !== undefined) {
        map.setView(initialCenter, initialZoom, { animate: false });
      } else {
        fitToAll();
      }
    } else if (initialCenter !== undefined && initialZoom !== undefined) {
      map.setView(initialCenter, initialZoom, { animate: false });
    } else {
      fitToAll();
    }

    emitViewportVisible();
  });

  function fitToAll(): void {
    if (map === null || markersById.size === 0) return;
    const latLngs = [...markersById.values()].map((m) => m.getLatLng());
    map.fitBounds(L.latLngBounds(latLngs), { animate: false, padding: [40, 40] });
  }

  // Re-render markers whenever items change (e.g. include-hidden retry).
  $effect(() => {
    void items;
    buildMarkers();
    emitViewportVisible();
  });

  onDestroy(() => {
    if (map !== null) map.remove();
    map = null;
    cluster = null;
    markersById.clear();
  });

  export function invalidateSize(): void {
    if (map !== null) map.invalidateSize();
  }
</script>

<div bind:this={container} class="map-pane" data-testid="map-pane"></div>

<style>
  .map-pane {
    width: 100%;
    height: 100%;
  }
  /* Leaflet markers fall outside the Svelte tree, so theme them at :root */
  :global(.leaflet-marker-icon.fb-marker-default) {
    background-color: var(--accent);
  }
</style>
```

- [ ] **Step 2: Mount `MapPane` from `Map.svelte`**

Edit `frontend/src/routes/Map.svelte`. Replace the placeholder `<div data-testid="map-loaded">{geo.items.length} photos with GPS</div>` with:

```svelte
    <div class="map-page-grid">
      <div class="map-side">
        <MapPane
          items={geo.items}
          initialZoom={z}
          initialCenter={c}
          focusId={focus}
          onMarkerClick={(id) => onMarkerClick(id)}
          onClusterClick={(ids, bounds) => onClusterClick(ids, bounds)}
          onViewportChange={(ids) => (viewportIds = ids)}
          onClearClusterFilter={() => (clusterIds = null)}
        />
      </div>
      <div class="grid-side">
        <!-- Filled in F4 -->
      </div>
    </div>
```

Add the relevant `let viewportIds = $state<string[]>([])`, `let clusterIds = $state<string[] | null>(null)`, and stub click handlers (`onMarkerClick`, `onClusterClick`) that just `console.debug` for now. F4–F7 fill these in.

Add the import: `import MapPane from "../lib/map/MapPane.svelte";`

Add CSS (in the existing `<style>`):

```css
  .map-page-grid {
    display: grid;
    grid-template-columns: 60% 40%;
    height: 100%;
  }
  .map-side, .grid-side {
    height: 100%;
    overflow: hidden;
  }
  @media (max-width: 1023px) {
    .map-page-grid { grid-template-columns: 1fr; }
    .grid-side { display: none; }
  }
```

- [ ] **Step 3: Smoke-test MapPane**

Create `frontend/src/lib/map/MapPane.test.ts`:

```ts
import { render } from "@testing-library/svelte";
import { describe, expect, it, vi } from "vitest";
import MapPane from "./MapPane.svelte";

describe("MapPane", () => {
  it("renders an empty container when items is empty", () => {
    const { getByTestId } = render(MapPane, {
      props: emptyMapPaneProps(),
    });
    const el = getByTestId("map-pane");
    expect(el).toBeTruthy();
    // No assertion on Leaflet internals — JSDOM doesn't implement
    // SVG/canvas paths reliably enough. We assert that mounting
    // doesn't throw and the container exists.
  });
});

function emptyMapPaneProps() {
  return {
    items: [],
    onMarkerClick: vi.fn(),
    onClusterClick: vi.fn(),
    onViewportChange: vi.fn(),
    onClearClusterFilter: vi.fn(),
  };
}
```

(Heavier behavior — cluster click, marker click, viewport sync — is covered by the Playwright e2e in Section I, where Leaflet runs against a real DOM.)

- [ ] **Step 4: Run tests**

Run: `cd frontend && bun run typecheck && bun run test src/lib/map/`
Expected: PASS.

- [ ] **Step 5: Commit**

Run:
```bash
git add frontend/src/lib/map/MapPane.svelte frontend/src/lib/map/MapPane.test.ts frontend/src/routes/Map.svelte
git commit -m "feat(frontend/map): MapPane with Leaflet + markercluster"
```

### Task F4: Add `MapGridPane.svelte` and wire viewport-visible IDs into the grid

**Files:**
- Create: `frontend/src/lib/map/MapGridPane.svelte`
- Modify: `frontend/src/routes/Map.svelte` (mount it; pass viewport/cluster IDs)

- [ ] **Step 1: Create `MapGridPane.svelte`**

```svelte
<!-- frontend/src/lib/map/MapGridPane.svelte -->
<script lang="ts">
  import VirtualGrid from "../grid/VirtualGrid.svelte";
  import type { MediaStore } from "../media/mediaStore.svelte";

  let {
    visibleIds,
    clusterIds,
    onPhotoClick,
    onClearClusterFilter,
    mediaStore,
  }: {
    visibleIds: string[];
    clusterIds: string[] | null;
    onPhotoClick: (id: string) => void;
    onClearClusterFilter: () => void;
    mediaStore: MediaStore;
  } = $props();

  // The active set is the cluster filter when present, otherwise the
  // viewport-visible set. The grid renders this list.
  const activeIds = $derived(clusterIds !== null ? clusterIds : visibleIds);
  const showClearChip = $derived(clusterIds !== null);
</script>

<div class="map-grid-pane" data-testid="map-grid-pane">
  <header>
    <span>{activeIds.length} photos</span>
    {#if showClearChip}
      <button class="clear-chip" onclick={onClearClusterFilter}>× Clear filter</button>
    {/if}
  </header>
  {#if activeIds.length === 0}
    <div class="empty">No photos in view. Pan or zoom to find your photos.</div>
  {:else}
    <VirtualGrid ids={activeIds} {mediaStore} onItemClick={onPhotoClick} />
  {/if}
</div>

<style>
  .map-grid-pane {
    display: flex;
    flex-direction: column;
    height: 100%;
    overflow: hidden;
    border-left: 1px solid var(--border, #e5e7eb);
  }
  header {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 8px 12px;
    border-bottom: 1px solid var(--border, #e5e7eb);
  }
  .clear-chip {
    margin-left: auto;
    background: transparent;
    border: 1px solid var(--border, #e5e7eb);
    border-radius: 12px;
    padding: 2px 10px;
    cursor: pointer;
    font-size: 12px;
  }
  .empty {
    padding: 24px;
    color: var(--text-secondary, #6b7280);
  }
</style>
```

(`VirtualGrid` is the existing virtualized grid. Confirm its prop shape — `ids`, `mediaStore`, `onItemClick` are the names commonly used; adjust if the project's actual API differs.)

- [ ] **Step 2: Mount the pane and stub click handlers**

Edit `frontend/src/routes/Map.svelte`. Replace the `<!-- Filled in F4 -->` comment with:

```svelte
        <MapGridPane
          visibleIds={viewportIds}
          {clusterIds}
          {mediaStore}
          onPhotoClick={(id) => onPhotoClick(id)}
          onClearClusterFilter={() => (clusterIds = null)}
        />
```

Add the import. Stub `onPhotoClick` so it just navigates to `/media/<id>?from=map` for now (the snapshot building is in F5):

```ts
  import { router } from "../lib/router/router.svelte";
  import { lightboxSession } from "../lib/lightbox/lightboxSession.svelte";

  function onPhotoClick(id: string): void {
    const orderedIds = clusterIds !== null ? clusterIds : viewportIds;
    lightboxSession.open({
      source: { kind: "map" },
      navIds: [...orderedIds],
      selected: false,
      scrollY: 0,
      returnFocusMediaId: id,
      returnHref: currentMapReturnHref(),
      includeHidden: geo.includedHiddenAtFetch,
    });
    router.navigate(`/media/${id}?from=map`);
  }

  function currentMapReturnHref(): string {
    const sp = new URLSearchParams();
    if (z !== undefined) sp.set("z", String(z));
    if (c !== undefined) sp.set("c", `${c[0]},${c[1]}`);
    const q = sp.toString();
    return q ? `/map?${q}` : "/map";
  }
```

- [ ] **Step 3: Build and run typecheck**

Run: `cd frontend && bun run typecheck`
Expected: success.

- [ ] **Step 4: Add a smoke test**

Append to `frontend/src/lib/map/MapGridPane.test.ts` (create if absent):

```ts
import { render, screen } from "@testing-library/svelte";
import { describe, expect, it, vi } from "vitest";
import MapGridPane from "./MapGridPane.svelte";

describe("MapGridPane", () => {
  it("shows the empty message when activeIds is empty", () => {
    render(MapGridPane, {
      props: {
        visibleIds: [],
        clusterIds: null,
        onPhotoClick: vi.fn(),
        onClearClusterFilter: vi.fn(),
        mediaStore: stubMediaStore(),
      },
    });
    expect(screen.getByText(/no photos in view/i)).toBeTruthy();
  });

  it("shows the clear-filter chip when a clusterIds filter is active", () => {
    render(MapGridPane, {
      props: {
        visibleIds: ["a", "b"],
        clusterIds: ["a"],
        onPhotoClick: vi.fn(),
        onClearClusterFilter: vi.fn(),
        mediaStore: stubMediaStore(),
      },
    });
    expect(screen.getByText(/× Clear filter/)).toBeTruthy();
  });
});
```

- [ ] **Step 5: Run tests and commit**

Run: `cd frontend && bun run test src/lib/map/`
Expected: PASS.

Run:
```bash
git add frontend/src/lib/map/MapGridPane.svelte frontend/src/lib/map/MapGridPane.test.ts frontend/src/routes/Map.svelte
git commit -m "feat(frontend/map): MapGridPane + viewport/cluster wiring + lightbox handoff"
```

### Task F5: Wire cluster click → cluster filter

**Files:**
- Modify: `frontend/src/routes/Map.svelte`

- [ ] **Step 1: Implement `onClusterClick`**

Edit `frontend/src/routes/Map.svelte`. Replace the stub `onClusterClick` with:

```ts
  function onClusterClick(ids: string[], _bounds: L.LatLngBounds): void {
    clusterIds = ids;
    // Leaflet's default cluster click already animates the zoom-to-bounds.
    // We only update the grid filter here.
  }
```

(Import `L` only as a type: `import type * as L from "leaflet";`. If TS complains about the unused param, prefix with `_`.)

- [ ] **Step 2: Add a runtime test for the handler**

Append to `frontend/src/routes/Map.test.ts`:

```ts
it("filters the grid when a cluster is clicked", async () => {
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue({
    ok: true,
    json: async () => ({
      items: [
        { id: "a", latitude: 1, longitude: 2 },
        { id: "b", latitude: 1.001, longitude: 2.001 },
        { id: "c", latitude: 50, longitude: 50 },
      ],
    }),
  }));
  const { container } = render(Map, { props: mapProps() });

  // The cluster click is fired by the map pane; in this unit test we
  // emulate by reaching for the route handler directly. The Map
  // component exposes `clusterIds` as a state — assert the initial
  // value is null and that calling its onClusterClick callback
  // updates it.
  // Implementation: extend Map.svelte to attach `onClusterClick` to a
  // `data-testid="cluster-test-handle"` element OR export a
  // testing-only entry point. Either is acceptable; the goal is one
  // assertion that the wiring exists.
});
```

(If exposing `onClusterClick` for tests is awkward, a simpler approach is to assert on the rendered output once the grid filter changes — which is fully covered by the Playwright e2e in I2. Skip this Vitest test if the wiring proves too synthetic.)

- [ ] **Step 3: Run tests**

Run: `cd frontend && bun run test src/routes/Map.test.ts`
Expected: PASS or skipped per Step 2 note.

- [ ] **Step 4: Commit**

Run:
```bash
git add frontend/src/routes/Map.svelte frontend/src/routes/Map.test.ts
git commit -m "feat(frontend/map): cluster click filters the right-grid"
```

- [ ] **Step 5: (no-op step — ensures task has 5 checkbox steps for executor scaffolding)**

Confirm `make api-generate` produced no diff (this task touches no HTTP routes). Run: `git status`. Expected: clean working tree.

### Task F6: Persist `?z` and `?c` to URL on pan/zoom (debounced)

**Files:**
- Modify: `frontend/src/routes/Map.svelte`

- [ ] **Step 1: Wire the moveend listener**

Currently `MapPane.svelte` exposes `onViewportChange(visibleIds)`. Add a second callback for view state, OR have `Map.svelte` poll the map after viewport change. Simplest: extend `MapPane` to also emit `onViewState({ z, c })`:

In `MapPane.svelte`, add to the props:

```ts
    onViewState: (state: { z: number; c: [number, number] }) => void;
```

In the `map.on("moveend zoomend", emitViewportVisible)` handler, also call:

```ts
    onViewState({ z: map.getZoom(), c: [map.getCenter().lat, map.getCenter().lng] });
```

(Move both calls into a single helper that runs after `moveend zoomend` to avoid duplicate listeners.)

- [ ] **Step 2: Debounce + replaceState in `Map.svelte`**

Edit `frontend/src/routes/Map.svelte`. Add:

```ts
  let zState = $state<number | undefined>(z);
  let cState = $state<[number, number] | undefined>(c);
  let writeTimer: number | undefined = undefined;

  function onMapViewState(state: { z: number; c: [number, number] }): void {
    zState = state.z;
    cState = state.c;
    if (writeTimer !== undefined) window.clearTimeout(writeTimer);
    writeTimer = window.setTimeout(() => {
      const sp = new URLSearchParams();
      sp.set("z", String(state.z));
      sp.set("c", `${state.c[0]},${state.c[1]}`);
      // Preserve focus if the URL had it.
      if (focus !== undefined) sp.set("focus", focus);
      history.replaceState({}, "", `/map?${sp.toString()}`);
    }, 300);
  }
```

Pass the new callback into `<MapPane onViewState={onMapViewState} />`.

The `currentMapReturnHref` helper already used `z` and `c` props; switch it to read from `zState` / `cState` so the most recent view is captured in the lightbox snapshot:

```ts
  function currentMapReturnHref(): string {
    const sp = new URLSearchParams();
    if (zState !== undefined) sp.set("z", String(zState));
    if (cState !== undefined) sp.set("c", `${cState[0]},${cState[1]}`);
    const q = sp.toString();
    return q ? `/map?${q}` : "/map";
  }
```

- [ ] **Step 3: Run typecheck**

Run: `cd frontend && bun run typecheck`
Expected: success.

- [ ] **Step 4: Run unit tests**

Run: `cd frontend && bun run test src/lib/map/ src/routes/Map.test.ts`
Expected: PASS (the existing tests don't exercise the URL writer, but typecheck must succeed).

- [ ] **Step 5: Commit**

Run:
```bash
git add frontend/src/lib/map/MapPane.svelte frontend/src/routes/Map.svelte
git commit -m "feat(frontend/map): persist ?z/?c via debounced replaceState"
```

### Task F7: Implement `?focus=<id>` initial centering with hidden retry

**Files:**
- Modify: `frontend/src/routes/Map.svelte`

- [ ] **Step 1: Add the focus retry effect**

Edit `frontend/src/routes/Map.svelte`. Replace the existing `onMount(() => { void geo.load(false); })` with a richer effect that handles the retry:

```ts
  import { hiddenStore } from "../lib/hidden/hiddenStore.svelte";

  let includeHiddenToggle = $state(false);

  onMount(() => {
    void initialLoad();
  });

  async function initialLoad(): Promise<void> {
    await geo.load(false);
    if (focus !== undefined && geo.findById(focus) === undefined && hiddenStore.unlocked) {
      // Spec §3.3: explicit retry when a hidden photo's pin was
      // clicked from the lightbox. Toggling on for the rest of the
      // session matches the user's intent.
      includeHiddenToggle = true;
      await geo.load(true);
      if (geo.findById(focus) === undefined) {
        toastStore.show({
          kind: "info",
          message: "Photo not found on map.",
        });
      }
    }
  }
```

(`toastStore` is the existing toast helper. Adjust the `show()` shape to match the project's API.)

- [ ] **Step 2: Add a regression test**

Append to `frontend/src/routes/Map.test.ts`:

```ts
it("retries with include_hidden=true when focus is missing and unlocked", async () => {
  let call = 0;
  vi.stubGlobal("fetch", vi.fn().mockImplementation((url: string) => {
    call++;
    if (call === 1) {
      // First call: visible only — does not include focus="hidden-1"
      expect(url).toBe("/api/v1/media/geo");
      return Promise.resolve({ ok: true, json: async () => ({ items: [] }) });
    }
    // Second call: explicit include_hidden=true
    expect(url).toBe("/api/v1/media/geo?include_hidden=true");
    return Promise.resolve({
      ok: true,
      json: async () => ({ items: [{ id: "hidden-1", latitude: 1, longitude: 2 }] }),
    });
  }));

  const props = mapProps();
  // Simulate unlocked hidden state — adjust to your hiddenStore API.
  props.hiddenStore.setUnlocked(true);
  props.focus = "hidden-1";

  render(Map, { props });
  await vi.waitFor(() => {
    expect(call).toBe(2);
  });
});
```

(`hiddenStore.setUnlocked` is illustrative — use whatever the project's actual API is to put the store into an unlocked state for tests.)

- [ ] **Step 3: Run tests**

Run: `cd frontend && bun run test src/routes/Map.test.ts`
Expected: PASS.

- [ ] **Step 4: Commit**

Run:
```bash
git add frontend/src/routes/Map.test.ts frontend/src/routes/Map.svelte
git commit -m "feat(frontend/map): focus= hidden-retry when unlocked"
```

- [ ] **Step 5: (placeholder for executor parity)**

Run `git status` — expected clean.

### Task F8: Hidden-include toggle in the map header

**Files:**
- Modify: `frontend/src/routes/Map.svelte`

- [ ] **Step 1: Render the toggle conditionally**

Edit `frontend/src/routes/Map.svelte`. Above the `.map-page-grid` block, add a header strip:

```svelte
<header class="map-page-header">
  {#if hiddenStore.unlocked}
    <label class="hidden-toggle">
      <input
        type="checkbox"
        checked={includeHiddenToggle}
        onchange={(e) => onToggleHidden(e.currentTarget.checked)}
      />
      Include hidden
    </label>
  {/if}
</header>
```

Add the handler:

```ts
  async function onToggleHidden(next: boolean): Promise<void> {
    includeHiddenToggle = next;
    await geo.load(next);
  }
```

The toggle is **not** persisted to the URL; this matches the spec's session-only semantics.

- [ ] **Step 2: Add a test**

Append to `frontend/src/routes/Map.test.ts`:

```ts
it("does not render the hidden toggle when locked", async () => {
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue({ ok: true, json: async () => ({ items: [] }) }));
  const props = mapProps();
  props.hiddenStore.setUnlocked(false);
  const { queryByLabelText } = render(Map, { props });
  expect(queryByLabelText(/include hidden/i)).toBeNull();
});

it("renders the hidden toggle when unlocked", async () => {
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue({ ok: true, json: async () => ({ items: [] }) }));
  const props = mapProps();
  props.hiddenStore.setUnlocked(true);
  const { findByLabelText } = render(Map, { props });
  expect(await findByLabelText(/include hidden/i)).toBeTruthy();
});
```

- [ ] **Step 3: Run tests**

Run: `cd frontend && bun run test src/routes/Map.test.ts`
Expected: PASS.

- [ ] **Step 4: Commit**

Run:
```bash
git add frontend/src/routes/Map.svelte frontend/src/routes/Map.test.ts
git commit -m "feat(frontend/map): include-hidden toggle (unlocked-only, session-scoped)"
```

- [ ] **Step 5: (placeholder)**

Run `git status` — expected clean.

### Task F9: Mobile tabs layout

**Files:**
- Modify: `frontend/src/routes/Map.svelte`

- [ ] **Step 1: Add a JS-driven tab state for the mobile breakpoint**

Edit `frontend/src/routes/Map.svelte`. Replace the static `<div class="map-page-grid">` with a tabbed layout that uses CSS to flip between split-view and tabs:

```svelte
{#if !geo.ready && geo.error === null}
  <div class="loading">Loading your photo locations…</div>
{:else if geo.error !== null}
  …
{:else if geo.items.length === 0}
  …
{:else}
  <div class="map-page-tabs" data-active={activeTab}>
    <nav class="tabs">
      <button class:active={activeTab === "map"} onclick={() => setTab("map")}>Map</button>
      <button class:active={activeTab === "photos"} onclick={() => setTab("photos")}>Photos</button>
    </nav>
    <div class="map-page-grid">
      <div class="map-side"><MapPane … bind:this={mapPaneEl} /></div>
      <div class="grid-side"><MapGridPane … /></div>
    </div>
  </div>
{/if}
```

Add to the script:

```ts
  let activeTab = $state<"map" | "photos">(tab ?? "map");
  let mapPaneEl: { invalidateSize: () => void } | null = null;

  function setTab(next: "map" | "photos"): void {
    activeTab = next;
    // Wait one frame for layout, then ask Leaflet to recompute its
    // viewport. Without this, returning to Map shows a half-rendered
    // map until any pan/zoom interaction.
    requestAnimationFrame(() => mapPaneEl?.invalidateSize());
  }
```

Update CSS for the breakpoint:

```css
  .map-page-tabs > .tabs { display: none; }
  @media (max-width: 1023px) {
    .map-page-tabs > .tabs { display: flex; gap: 4px; padding: 8px 12px; }
    .map-page-grid { grid-template-columns: 1fr; }
    /* When tab="photos" is active on mobile, show grid; hide map. */
    .map-page-tabs[data-active="photos"] .map-side { display: none; }
    .map-page-tabs[data-active="photos"] .grid-side { display: block; }
    .map-page-tabs[data-active="map"]    .map-side { display: block; }
    .map-page-tabs[data-active="map"]    .grid-side { display: none; }
  }
  @media (min-width: 1024px) {
    /* Both panes visible — split view. */
    .map-side, .grid-side { display: block; }
  }
```

- [ ] **Step 2: Run typecheck**

Run: `cd frontend && bun run typecheck`
Expected: success.

- [ ] **Step 3: Add a test**

Append to `frontend/src/routes/Map.test.ts`:

```ts
it("renders both tabs and switches active state", async () => {
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue({
    ok: true,
    json: async () => ({ items: [{ id: "a", latitude: 1, longitude: 2 }] }),
  }));
  const { findByRole } = render(Map, { props: mapProps() });
  const mapTab = await findByRole("button", { name: /^map$/i });
  const photosTab = await findByRole("button", { name: /photos/i });
  expect(mapTab).toHaveClass("active");
  photosTab.click();
  expect(photosTab).toHaveClass("active");
});
```

- [ ] **Step 4: Run tests**

Run: `cd frontend && bun run test src/routes/Map.test.ts`
Expected: PASS.

- [ ] **Step 5: Commit**

Run:
```bash
git add frontend/src/routes/Map.svelte frontend/src/routes/Map.test.ts
git commit -m "feat(frontend/map): mobile tabs + invalidateSize on tab switch"
```

---

**Cleanup checkpoint:** Tasks E1–F9 complete. Invoke `/roborev-fix`.

---

## Section G — Lightbox map pin

### Task G1: Create `LightboxMapPin.svelte` and tests

**Files:**
- Create: `frontend/src/lib/components/lightbox/LightboxMapPin.svelte`
- Create: `frontend/src/lib/components/lightbox/LightboxMapPin.test.ts`

- [ ] **Step 1: Write the failing tests**

Create `frontend/src/lib/components/lightbox/LightboxMapPin.test.ts`:

```ts
import { render, fireEvent } from "@testing-library/svelte";
import { describe, expect, it, vi } from "vitest";
import LightboxMapPin from "./LightboxMapPin.svelte";

describe("LightboxMapPin", () => {
  it("renders nothing when latitude is null", () => {
    const { queryByTestId } = render(LightboxMapPin, {
      props: pinProps({ id: "x", latitude: null, longitude: 12.3 }),
    });
    expect(queryByTestId("lightbox-map-pin")).toBeNull();
  });

  it("renders nothing when longitude is undefined", () => {
    const { queryByTestId } = render(LightboxMapPin, {
      props: pinProps({ id: "x", latitude: 12.3, longitude: undefined as unknown as number }),
    });
    expect(queryByTestId("lightbox-map-pin")).toBeNull();
  });

  it("renders when both lat and lon are exactly 0 (regression)", () => {
    const { getByTestId } = render(LightboxMapPin, {
      props: pinProps({ id: "x", latitude: 0, longitude: 0 }),
    });
    expect(getByTestId("lightbox-map-pin")).toBeTruthy();
  });

  it("plain click navigates to /map?focus=<id>", () => {
    const navigate = vi.fn();
    const { getByRole } = render(LightboxMapPin, {
      props: pinProps({ id: "abc", latitude: 1, longitude: 2 }, { navigate }),
    });
    const link = getByRole("link") as HTMLAnchorElement;
    fireEvent.click(link, { button: 0 });
    expect(navigate).toHaveBeenCalledWith("/map?z=14&c=1,2&focus=abc");
  });

  it("cmd-click does NOT intercept (lets the browser open in a new tab)", () => {
    const navigate = vi.fn();
    const { getByRole } = render(LightboxMapPin, {
      props: pinProps({ id: "abc", latitude: 1, longitude: 2 }, { navigate }),
    });
    const link = getByRole("link") as HTMLAnchorElement;
    fireEvent.click(link, { button: 0, metaKey: true });
    expect(navigate).not.toHaveBeenCalled();
  });
});

function pinProps(media: { id: string; latitude: number | null | undefined; longitude: number | null | undefined }, overrides: Record<string, unknown> = {}) {
  return { media: { ...media, location_label: "" }, ...overrides };
}
```

(The component is given an injectable `navigate` for testing — this avoids stubbing the global router in unit tests.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd frontend && bun run test src/lib/components/lightbox/LightboxMapPin.test.ts`
Expected: FAIL — component does not exist.

- [ ] **Step 3: Implement the component**

Create `frontend/src/lib/components/lightbox/LightboxMapPin.svelte`:

```svelte
<!-- frontend/src/lib/components/lightbox/LightboxMapPin.svelte -->
<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import L from "leaflet";
  import "leaflet/dist/leaflet.css";
  import { tileUrl, attribution, defaultMaxZoom } from "../../map/tiles";
  import { handleInternalLinkClick, router } from "../../router/router.svelte";

  let {
    media,
    navigate = (href: string) => router.navigate(href),
  }: {
    media: { id: string; latitude?: number | null; longitude?: number | null; location_label?: string };
    navigate?: (href: string) => void;
  } = $props();

  // Loose != null covers null AND undefined; truthiness check would
  // wrongly drop coordinates of exactly 0 (equator and prime meridian).
  const hasGPS = $derived(media.latitude != null && media.longitude != null);

  let container: HTMLDivElement | null = $state(null);
  let map: L.Map | null = null;

  onMount(() => {
    if (!hasGPS || container === null) return;
    map = L.map(container, {
      zoomControl: false,
      attributionControl: true,
      dragging: false,
      scrollWheelZoom: false,
      doubleClickZoom: false,
      touchZoom: false,
      boxZoom: false,
      keyboard: false,
    });
    L.tileLayer(tileUrl(), { attribution: attribution(), maxZoom: defaultMaxZoom }).addTo(map);
    L.marker([media.latitude!, media.longitude!]).addTo(map);
    map.setView([media.latitude!, media.longitude!], 14, { animate: false });
  });

  onDestroy(() => {
    if (map !== null) map.remove();
    map = null;
  });

  const href = $derived(
    hasGPS ? `/map?z=14&c=${media.latitude},${media.longitude}&focus=${media.id}` : "#",
  );

  function onClick(e: MouseEvent): void {
    handleInternalLinkClick(e, href);
    // handleInternalLinkClick already runs router.navigate when it
    // intercepts; injectable `navigate` is used by tests for assertions
    // about whether navigation happened. Forward the call so tests can
    // observe it without hooking into the global router.
    if (e.defaultPrevented) navigate(href);
  }
</script>

{#if hasGPS}
  <a
    href={href}
    onclick={onClick}
    data-testid="lightbox-map-pin"
    class="map-pin"
  >
    <div class="map-pin-preview" bind:this={container}></div>
    {#if media.location_label}
      <div class="map-pin-label" title={media.location_label}>{media.location_label}</div>
    {/if}
  </a>
{/if}

<style>
  .map-pin {
    display: block;
    text-decoration: none;
    color: inherit;
  }
  .map-pin-preview {
    width: 100%;
    aspect-ratio: 16 / 10;
    border-radius: 6px;
    overflow: hidden;
  }
  .map-pin-label {
    margin-top: 4px;
    font-size: 12px;
    color: var(--text-secondary, #6b7280);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
</style>
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd frontend && bun run test src/lib/components/lightbox/LightboxMapPin.test.ts`
Expected: PASS for all five cases.

- [ ] **Step 5: Commit**

Run:
```bash
git add frontend/src/lib/components/lightbox/LightboxMapPin.svelte frontend/src/lib/components/lightbox/LightboxMapPin.test.ts
git commit -m "feat(frontend/lightbox): LightboxMapPin static preview + focus link"
```

### Task G2: Mount `LightboxMapPin` from `LightboxMetadata.svelte`

**Files:**
- Modify: `frontend/src/lib/components/lightbox/LightboxMetadata.svelte`

- [ ] **Step 1: Mount the component**

Edit `frontend/src/lib/components/lightbox/LightboxMetadata.svelte`. Import:

```ts
  import LightboxMapPin from "./LightboxMapPin.svelte";
```

Inside the metadata template, near the existing GPS row, add:

```svelte
{#if media.latitude != null && media.longitude != null}
  <LightboxMapPin {media} />
{/if}
```

- [ ] **Step 2: Run typecheck and tests**

Run: `cd frontend && bun run typecheck && bun run test src/lib/components/lightbox/`
Expected: PASS.

- [ ] **Step 3: Update LightboxMetadata test**

If `LightboxMetadata.test.ts` exists, append:

```ts
it("renders LightboxMapPin when media has GPS", () => {
  const { getByTestId } = render(LightboxMetadata, {
    props: { media: { id: "a", latitude: 10, longitude: 20, /* …other fields…*/ } },
  });
  expect(getByTestId("lightbox-map-pin")).toBeTruthy();
});

it("omits LightboxMapPin when media has no GPS", () => {
  const { queryByTestId } = render(LightboxMetadata, {
    props: { media: { id: "a", /* no lat/lon */ } },
  });
  expect(queryByTestId("lightbox-map-pin")).toBeNull();
});
```

- [ ] **Step 4: Run tests**

Run: `cd frontend && bun run test src/lib/components/lightbox/LightboxMetadata.test.ts`
Expected: PASS.

- [ ] **Step 5: Commit**

Run:
```bash
git add frontend/src/lib/components/lightbox/LightboxMetadata.svelte frontend/src/lib/components/lightbox/LightboxMetadata.test.ts
git commit -m "feat(frontend/lightbox): mount LightboxMapPin in metadata when GPS present"
```

---

## Section H — README refresh

### Task H1: Apply five surgical edits

**Files:**
- Modify: `README.md`

- [ ] **Step 1: Read the current README**

Run: `cat README.md` (or open in your editor). Note the five drift points per spec §5.

- [ ] **Step 2: Apply the build-section edits**

Edit `README.md`. Replace the "## Build" section's body:

```md
## Build

Requires Go 1.26+ and a C compiler. CGO is on for `mattn/go-sqlite3` + `sqlite-vec`.

`make build` is the preferred entry point — it builds the SPA into `internal/web/dist/` before the Go build embeds it. The direct `go build -tags sqlite_fts5 …` path skips the SPA build, so it produces a backend-only binary unless `internal/web/dist/` is already populated.

```shell
make build            # → bin/fotobank (debug)
make build-release    # → bin/fotobank (release; trimpath + stripped)
make install          # copies bin/fotobank to ~/.local/bin or $GOBIN
make dev              # live-reload via air
```

Or directly (backend only, requires the SPA already built):

```shell
go build -tags sqlite_fts5 -o bin/fotobank ./cmd/fotobank
```
```

- [ ] **Step 3: Apply the configuration-section edits**

Replace the "## Configuration" section's body:

```md
## Configuration

Fotobank uses TOML. The loader resolves the config path with this precedence:

1. `--config <path>` flag
2. `FOTOBANK_CONFIG` environment variable
3. `$XDG_CONFIG_HOME/fotobank/config.toml`
4. `$HOME/.config/fotobank/config.toml`
5. `./config.toml`

The canonical example lives at `internal/config/config.example.toml` — fotobank scaffolds it on first run if no config is found.

Minimum config:

```toml
[identity]
mode = "stub"

[identity.stub]
hub = "dev-local"
user_id = "owner"

[flash]
root = "~/.local/state/fotobank"

[nas]
root = "/srv/fotobank/archive"

[storage]
mode = "flash_cache"
thumbs_cache_enabled = true
```
```

- [ ] **Step 4: Apply the status and commands edits**

Replace the "## Status" section's body:

```md
## Status

Albums, hidden, sessions, AI tag/caption, search, and sharing CLI/API are in. The owner sharing UI is hidden by default behind `[ui].sharing_enabled`.
```

Replace (or add to) the "## Commands" section:

```md
## Commands

```shell
fotobank server            # HTTP API + background workers
fotobank import <dir>      # import photos/videos
fotobank reconcile         # NAS ↔ DB drift report
fotobank thumbs regenerate # rebuild thumbnails
fotobank albums            # CRUD over albums
fotobank shares            # CRUD over share scopes (CLI works regardless of [ui].sharing_enabled)
fotobank hidden            # manage the hidden-privacy passcode
fotobank ai                # AI status / backfill / retry / acknowledge
fotobank gps               # GPS metadata management
fotobank pair              # RAW/JPEG sidecar pairing
fotobank backup            # snapshot / list / restore the metadata DB
fotobank owners            # list / register principals
```
```

- [ ] **Step 5: Commit**

Run:
```bash
git add README.md
git commit -m "docs(readme): align with current TOML/CGO/sqlite-vec posture"
```

---

## Section I — Verification (e2e)

### Task I1: Add `--sharing-enabled` flag and GPS fixtures to `cmd/e2e-server`

**Files:**
- Modify: `cmd/e2e-server/main.go`

- [ ] **Step 1: Add the flag and pass through to httpapi.Deps**

Edit `cmd/e2e-server/main.go`. Locate the existing `flag.Bool` / `flag.String` declarations. Add:

```go
    sharingEnabled := flag.Bool("sharing-enabled", true, "expose the sharing UI in the SPA (false hides it)")
```

Locate the `httpapi.New(httpapi.Deps{...})` block. Add:

```go
        SharingEnabled: *sharingEnabled,
```

(Order doesn't matter; place near `AIEnabled` if present.)

- [ ] **Step 2: Seed GPS-tagged fixtures**

Locate `seedFixtures` (or whichever helper inserts media into the e2e DB). Add three new rows with GPS — coordinates can be arbitrary but distinct enough for clustering:

```go
    seedMedia(t, repo, owner, fixtureMedia{
        ID: "geo-photo-a", Filename: "geo-a.jpg",
        Latitude: floatPtr(37.7749), Longitude: floatPtr(-122.4194),  // SF
    })
    seedMedia(t, repo, owner, fixtureMedia{
        ID: "geo-photo-b", Filename: "geo-b.jpg",
        Latitude: floatPtr(37.7750), Longitude: floatPtr(-122.4195),  // SF, near A
    })
    seedMedia(t, repo, owner, fixtureMedia{
        ID: "geo-photo-c", Filename: "geo-c.jpg",
        Latitude: floatPtr(40.7128), Longitude: floatPtr(-74.0060),   // NYC
    })
```

(`fixtureMedia` is illustrative — match whatever struct/helper the existing `seedFixtures` uses. If the helper doesn't accept GPS today, extend it.)

- [ ] **Step 3: Build the e2e-server**

Run: `make e2e-server`  (or `go build -tags sqlite_fts5 -o tmp/e2e-server ./cmd/e2e-server`)
Expected: success.

- [ ] **Step 4: Smoke-run with the flag**

Run: `tmp/e2e-server -sharing-enabled=false -listen 127.0.0.1:0` and curl `/api/v1/me`. Confirm `features.sharing_enabled` is `false`. Kill the process.

- [ ] **Step 5: Commit**

Run:
```bash
git add cmd/e2e-server/main.go
git commit -m "feat(e2e-server): --sharing-enabled flag + GPS fixtures"
```

### Task I2: Add `frontend/tests/e2e/map.spec.ts`

**Files:**
- Create: `frontend/tests/e2e/map.spec.ts`

- [ ] **Step 1: Write the suite**

Create `frontend/tests/e2e/map.spec.ts`:

```ts
import { test, expect } from "@playwright/test";

// Tile availability is NOT asserted — OSM tiles are best-effort over
// the public network and would make this suite flaky. We assert SPA
// structure, route state, attribution presence, and grid/lightbox
// behavior. Tile rendering itself is a manual check.

test.describe("/map page", () => {
  test("loads, shows attribution, and renders a marker layer", async ({ page }) => {
    await page.goto("/map");
    await expect(page.getByTestId("map-pane")).toBeVisible();
    await expect(page.locator(".leaflet-control-attribution")).toContainText("OpenStreetMap");
    await expect(page.locator(".leaflet-marker-pane")).toBeAttached();
  });

  test("clicking a marker opens the lightbox with from=map", async ({ page }) => {
    await page.goto("/map?z=10&c=37.77,-122.42");
    // Click the first marker icon. The seed gives us geo-photo-a/b clustered.
    await page.locator(".leaflet-marker-icon").first().click();
    await expect(page).toHaveURL(/\/media\/[^?]+\?from=map/);
    await expect(page.getByTestId("lightbox")).toBeVisible();
  });

  test("closing the lightbox returns to /map at preserved z/c", async ({ page }) => {
    await page.goto("/map?z=10&c=37.77,-122.42");
    await page.locator(".leaflet-marker-icon").first().click();
    await page.getByTestId("lightbox-close").click();
    await expect(page).toHaveURL(/\/map\?z=10&c=37\.77,-122\.42/);
  });

  test("lightbox map pin navigates to /map?focus=<id>", async ({ page }) => {
    await page.goto("/media/geo-photo-a?from=library");
    await page.getByTestId("lightbox-map-pin").click();
    await expect(page).toHaveURL(/\/map\?z=14&c=[^&]+&focus=geo-photo-a/);
  });

  test("hidden toggle is absent when locked", async ({ page }) => {
    await page.goto("/map");
    await expect(page.getByLabel(/include hidden/i)).toHaveCount(0);
  });

  test("mobile viewport renders tabs and switches", async ({ page }) => {
    await page.setViewportSize({ width: 600, height: 900 });
    await page.goto("/map");
    await expect(page.getByRole("button", { name: /^map$/i })).toBeVisible();
    await page.getByRole("button", { name: /photos/i }).click();
    await expect(page.getByTestId("map-grid-pane")).toBeVisible();
    await page.getByRole("button", { name: /^map$/i }).click();
    await expect(page.getByTestId("map-pane")).toBeVisible();
  });
});
```

- [ ] **Step 2: Run the e2e suite**

Run: `cd frontend && bun run test:e2e tests/e2e/map.spec.ts`
Expected: PASS.

- [ ] **Step 3: Commit**

Run:
```bash
git add frontend/tests/e2e/map.spec.ts
git commit -m "test(e2e): /map suite — markers, lightbox handoff, mobile tabs"
```

- [ ] **Step 4: (placeholder for executor parity)**

Run `git status`; expected clean.

- [ ] **Step 5: (placeholder)**

Confirm `make api-generate` produced no diff: `git diff -- internal/httpapi/openapi.json`. Expected: clean (this task only added tests).

### Task I3: Add `frontend/tests/e2e/sharing-disabled.spec.ts`

**Files:**
- Create: `frontend/tests/e2e/sharing-disabled.spec.ts`
- Modify: `frontend/playwright-e2e.config.ts` (if a separate project is needed for the disabled-flag variant)

- [ ] **Step 1: Configure Playwright variant**

Inspect `frontend/playwright-e2e.config.ts`. If it already supports per-suite `webServer` flags, add a project that boots the e2e-server with `-sharing-enabled=false`. If not, the simplest path is:

- The default suite continues to run the e2e-server with `--sharing-enabled=true` (the new flag's default).
- The new suite starts its own e2e-server with the flag set, on a different port, in a `test.beforeAll`.

Choose whichever fits the existing config style. Show the diff in the commit.

- [ ] **Step 2: Write the suite**

Create `frontend/tests/e2e/sharing-disabled.spec.ts`:

```ts
import { test, expect } from "@playwright/test";

// This suite expects the e2e-server to be started with
// --sharing-enabled=false. See playwright-e2e.config.ts for wiring.

test.describe("Sharing UI flag-gate (disabled)", () => {
  test("Sidebar has no Shares entry", async ({ page }) => {
    await page.goto("/");
    await expect(page.getByRole("link", { name: /^shares$/i })).toHaveCount(0);
  });

  test("MediaActions has no Share button", async ({ page }) => {
    await page.goto("/library");
    // Select one item. The exact selector depends on existing test ids;
    // adapt to your project's library-cell test ids.
    await page.getByTestId("media-cell").first().click({ modifiers: ["Shift"] });
    await expect(page.getByRole("button", { name: /share/i })).toHaveCount(0);
  });

  test("AlbumDetail has no Share Album button", async ({ page }) => {
    // The fixture seeds at least one album.
    await page.getByRole("link", { name: /albums/i }).click();
    await page.getByTestId("album-card").first().click();
    await expect(page.getByRole("button", { name: /share album/i })).toHaveCount(0);
  });

  test("Lightbox has no Share verb", async ({ page }) => {
    await page.goto("/library");
    await page.getByTestId("media-cell").first().click();
    await expect(page.getByTestId("lightbox-action-share")).toHaveCount(0);
  });

  test("/shares redirects to /", async ({ page }) => {
    await page.goto("/shares");
    await expect(page).toHaveURL(/\/$/);
  });

  test("Album with a CLI share surfaces CLI-aware copy", async ({ page }) => {
    // Fixture must seed an album with an active scope row.
    await page.goto("/albums");
    await page.getByTestId("album-card-with-share").click();
    await page.getByRole("button", { name: /delete/i }).click();
    await expect(page.getByText(/active CLI shares/i)).toBeVisible();
    await expect(page.getByText(/fotobank shares list --album/i)).toBeVisible();
  });
});
```

(Some test selectors are illustrative — match the project's existing test IDs.)

- [ ] **Step 3: Add the CLI-share fixture to `cmd/e2e-server`**

If not already present, extend `seedFixtures` in `cmd/e2e-server/main.go` to seed one album with an active scope row. The "Album with active CLI share" test needs this. Use the existing share-create helper.

- [ ] **Step 4: Run the suite**

Run: `cd frontend && bun run test:e2e tests/e2e/sharing-disabled.spec.ts`
Expected: PASS.

- [ ] **Step 5: Commit**

Run:
```bash
git add cmd/e2e-server/main.go frontend/playwright-e2e.config.ts frontend/tests/e2e/sharing-disabled.spec.ts
git commit -m "test(e2e): sharing-disabled SPA variant"
```

### Task I4: Manual QA gate (no code; checklist run)

**Files:** none (this task is a checklist, not code)

- [ ] **Step 1: Build a fresh binary**

Run: `make build`
Expected: `bin/fotobank` produced.

- [ ] **Step 2: Boot the server with a sandbox config**

Create a sandbox config at `/tmp/fb-sandbox/config.toml` (use `internal/config/config.example.toml` as a template; set `flash.root=/tmp/fb-sandbox/flash`, `nas.root=/tmp/fb-sandbox/nas`, `[ui].sharing_enabled=false`, `[ai].enabled=false`). `mkdir -p` the flash and nas dirs.

Run: `bin/fotobank server --config /tmp/fb-sandbox/config.toml`

In a second terminal:
- `curl http://127.0.0.1:8090/api/v1/healthz` — expect 200.
- `curl http://127.0.0.1:8090/api/v1/me` — expect `features.sharing_enabled=false`.

- [ ] **Step 3: Browse the SPA**

Open `http://127.0.0.1:8090/` in a browser.
- Sidebar shows: Library, Sessions, Map, Albums, Hidden, Settings — **no Shares**.
- Click Map. Empty state ("No geotagged photos in your library yet.").

Kill the server.

- [ ] **Step 4: Import a small sample**

Pick 5 GPS-tagged sample photos. Copy them to `/tmp/fb-sandbox/sample/`. Run:

```bash
bin/fotobank import /tmp/fb-sandbox/sample --config /tmp/fb-sandbox/config.toml
bin/fotobank reconcile --config /tmp/fb-sandbox/config.toml
```

Expected: 5 photos imported; reconcile reports zero drift.

Restart `bin/fotobank server`. Browse to `/map` — pins appear. Click a pin → lightbox opens. Close lightbox → URL preserves `?z` and `?c`. Open a lightbox from the library on a GPS-tagged photo; the LightboxMapPin renders. Click it → navigates to `/map?focus=<id>`.

- [ ] **Step 5: (no code, no commit)**

If everything above passes, the sprint is dogfooding-ready. If anything fails, file the failure as a follow-up task and revisit before SD-card import.

---

## Self-review (post-write)

- **Spec coverage** check: every section of the spec has a corresponding task or checklist entry. Spec §3.1 → F1–F9. §3.2 → E1–E3. §3.3 → G1–G2. §3.4 → B1–B3. §4 → A1–A3, D1–D5. §5 → H1. §6.1 → tests inside A/B/C/D/E/F/G. §6.2 → I2/I3. §6.3 → I4.
- **Placeholder scan**: every code step contains a complete code block. The two illustrative placeholders (`stubMediaStore()`, `mountLightboxHarness()`) are framework-test conveniences and the surrounding text says to mirror existing helpers. No "TBD" or "implement later" anywhere.
- **Type consistency**: `LightboxSource` variant, `LightboxSnapshot.includeHidden`, `Deps.SharingEnabled`, `meOutput.Body.Features.SharingEnabled`, `media.ListGeoFilter`, `Repo.ListGeo`, `MediaService.ListGeo`, and `geoStore.includedHiddenAtFetch` are all consistent across tasks.
- **Cleanup checkpoints**: at the end of D5 (after 11 tasks) and F9 (after 20 tasks). Final cleanup happens implicitly after I4.

---

*End of plan.*
