# Sidebar Facets Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a sidebar `FILTERS` group with five facet sub-sections (Cameras, Lenses, Tags, Places, Media Types) plus an active-filters chip strip above the photo grid on /library, /search, /map.

**Architecture:** URL is the source of truth for active filters. Backend extends `hybrid.Input`, `media.ListFilter`, `media.ListGeoFilter` with the same fields independently. New `GET /api/v1/facets` endpoint runs five exclude-self aggregations against an extended FilterCTE. Frontend ports two components from `~/code/agentsview` (re-themed) and adds a route → ActiveFilters → URL helper plus a debounced facets store and a mediaStore reset protocol.

**Tech Stack:** Go 1.x (mattn/go-sqlite3 + sqlite-vec, huma v2, sqlx) for backend; Svelte 5 (runes) + TypeScript + Vitest + Playwright for frontend.

**Spec:** `docs/superpowers/specs/2026-05-04-sidebar-facets-design.md` — read before starting.

---

## File Structure

### Backend (Go)

| Path | Action | Responsibility |
|---|---|---|
| `internal/search/hybrid/filter.go` | modify | `Input` struct gains `Cameras`, `Lenses`, `AnyTagKeys`, `HasGPS`. `Resolve` appends new conds. |
| `internal/search/hybrid/filter_test.go` | modify | Tests for new fields (one per facet, plus combo). |
| `internal/media/media.go` | modify | `ListFilter` and `ListGeoFilter` gain new fields. (`MediaType` already on `ListFilter` as `Type`; **add** `Type` to `ListGeoFilter`.) |
| `internal/media/repo.go` | modify | `Repo.List` and `Repo.ListGeo` apply new fields to their WHERE clauses. |
| `internal/media/repo_test.go` | modify | Repo tests for the new fields (verify rows filtered correctly). |
| `internal/db/migrations/000001_initial_schema.up.sql` | modify | Add expression indexes for camera + lens. |
| `internal/db/migrations/000001_initial_schema.down.sql` | modify | Mirror DROP for the new indexes. |
| `internal/service/facets/service.go` | **create** | `FacetService.Aggregate` runs five exclude-self queries, returns the response struct. |
| `internal/service/facets/service_test.go` | **create** | Service tests with `testutil.OpenTestDB`. |
| `internal/httpapi/facets.go` | **create** | `GET /api/v1/facets` huma route. DTO + handler. |
| `internal/httpapi/facets_test.go` | **create** | Route tests. |
| `internal/httpapi/api.go` | modify | `Deps` gains `Facets *facets.Service`. `buildAPI` calls `registerFacetsRoutes`. |
| `internal/cli/server.go` | modify | Wire new `*facets.Service` into the daemon's `httpapi.Deps`. |

### Frontend (Svelte/TS)

| Path | Action | Responsibility |
|---|---|---|
| `frontend/src/lib/router/router.svelte.ts` | modify | `RouteMatch` gains filter params on /library, /map, /search. Build callbacks parse them. |
| `frontend/src/lib/filters/activeFilters.ts` | **create** | `ActiveFilters` type + `fromRoute`, `withToggled`, `withFilters`, `filterKey`. |
| `frontend/src/lib/filters/activeFilters.test.ts` | **create** | Vitest unit tests for the helpers. |
| `frontend/src/lib/filters/facetsStore.svelte.ts` | **create** | Fetches `/api/v1/facets`, debounce + cache. |
| `frontend/src/lib/filters/facetsStore.test.ts` | **create** | Vitest unit tests. |
| `frontend/src/lib/filters/FacetList.svelte` | **create** | Inline scroll + checkbox + search; ported from agentsview. |
| `frontend/src/lib/filters/FacetList.test.ts` | **create** | Vitest component tests. |
| `frontend/src/lib/filters/FacetSection.svelte` | **create** | Collapsible header wrapping a `FacetList`. |
| `frontend/src/lib/filters/FacetSection.test.ts` | **create** | Vitest component tests. |
| `frontend/src/lib/filters/FilterSidebar.svelte` | **create** | The FILTERS group composing five `FacetSection`s. |
| `frontend/src/lib/filters/FilterSidebar.test.ts` | **create** | Vitest component tests. |
| `frontend/src/lib/filters/FilterChipStrip.svelte` | **create** | Active-filter chips strip. |
| `frontend/src/lib/filters/FilterChipStrip.test.ts` | **create** | Vitest component tests. |
| `frontend/src/lib/components/Sidebar.svelte` | modify | Mount `FilterSidebar` after MANAGE group. |
| `frontend/src/lib/media/mediaStore.svelte.ts` | modify | Add filter param API, implement filter-change reset protocol. |
| `frontend/src/lib/media/mediaStore.test.ts` | modify | Tests for reset protocol. |
| `frontend/src/routes/Library.svelte` | modify | Mount `FilterChipStrip`; pass `ActiveFilters` to `mediaStore`. |
| `frontend/src/routes/Search.svelte` | modify | Mount `FilterChipStrip`; pass new params to searchStore. |
| `frontend/src/routes/Map.svelte` | modify | Mount `FilterChipStrip`; pass filters to geo store. |
| `frontend/tests/e2e/filters.spec.ts` | **create** | Playwright e2e for sidebar facets. |

---

## Conventions

- **Per-task TDD**: write failing test, run to confirm fail, implement, run to confirm pass, commit.
- **Per-task commits**: each task ends with one commit (or two — separate failing-test commit if the agent prefers, but a single commit at task end is fine).
- **Commit format**: conventional commits (`feat(scope):`, `fix(scope):`, `test(scope):`, `refactor(scope):`). Imperative mood, ≤72 char subject. Add the standard Co-Authored-By trailer.
- **Backend tests**: `make test` runs `go test ./... -shuffle=on` with `-tags sqlite_fts5`. For a single package: `go test -tags sqlite_fts5 ./internal/path/... -run TestName -v`.
- **Frontend unit tests**: `cd frontend && bun run test -- <path-pattern>` (vitest).
- **Frontend type-check**: `cd frontend && bun run check`.
- **Frontend e2e**: `cd frontend && bun run test:e2e`.
- **OpenAPI regen**: when an HTTP route changes, the prek pre-commit hook regenerates `frontend/src/lib/api/openapi.json`. Don't pre-emptively run `make api-generate` — let the hook do it on commit.
- **Schema policy** (per CLAUDE.md): edit `000001_initial_schema.{up,down}.sql` in place; both files move together.
- **Branch policy** (per CLAUDE.md): commit directly to master. No feature branches.

---

## Task 1 — Extend `hybrid.Input` with new facet fields

**Files:**
- Modify: `internal/search/hybrid/filter.go`
- Modify: `internal/search/hybrid/filter_test.go`

The package-level docstring on `filter.go` enumerates the supported filters; update it. `Input`'s field list must grow and `Resolve` must emit conds in a deterministic order.

- [ ] **Step 1: Add four failing tests covering each new field**

Append to `internal/search/hybrid/filter_test.go`:

```go
// TestFilter_Cameras — Cameras []string emits a single
// (make || ' ' || model) IN (?, ?, ...) cond, with one bind per value
// in input order, after the date conds.
func TestFilter_Cameras(t *testing.T) {
	r := require.New(t)
	cte, args := hybrid.Resolve(hybrid.Input{
		Owner:   testOwner,
		Cameras: []string{"Sony A7R IV", "iPhone 15 Pro"},
	})
	r.Contains(cte, "(m.make || ' ' || m.model) IN (?, ?)")
	r.Equal([]any{"hub-a", "user-1", "Sony A7R IV", "iPhone 15 Pro"}, args)
}

// TestFilter_Lenses — Lenses []string emits a single
// lens_model IN (?, ...) cond.
func TestFilter_Lenses(t *testing.T) {
	r := require.New(t)
	cte, args := hybrid.Resolve(hybrid.Input{
		Owner:  testOwner,
		Lenses: []string{"FE 24-70mm F2.8 GM"},
	})
	r.Contains(cte, "m.lens_model IN (?)")
	r.Equal([]any{"hub-a", "user-1", "FE 24-70mm F2.8 GM"}, args)
}

// TestFilter_AnyTagKeys — OR-composed tag predicate. Emits ONE EXISTS
// subquery with tag_key IN (?, ?, ...). Distinct from TagKeys which
// emits one EXISTS per key (AND across keys).
func TestFilter_AnyTagKeys(t *testing.T) {
	r := require.New(t)
	cte, args := hybrid.Resolve(hybrid.Input{
		Owner:       testOwner,
		AnyTagKeys:  []string{"dog", "cat"},
	})
	// A single EXISTS — the substring 'EXISTS (' should appear once.
	r.Equal(1, strings.Count(cte, "EXISTS ("))
	r.Contains(cte, "AND mt.tag_key IN (?, ?)")
	r.Equal([]any{"hub-a", "user-1", "dog", "cat"}, args)
}

// TestFilter_HasGPS — pointer tri-state. true → IS NOT NULL pair;
// false → (IS NULL OR IS NULL); nil omits the cond.
func TestFilter_HasGPSTrue(t *testing.T) {
	r := require.New(t)
	yes := true
	cte, args := hybrid.Resolve(hybrid.Input{Owner: testOwner, HasGPS: &yes})
	r.Contains(cte, "m.latitude IS NOT NULL AND m.longitude IS NOT NULL")
	r.Equal([]any{"hub-a", "user-1"}, args)
}

func TestFilter_HasGPSFalse(t *testing.T) {
	r := require.New(t)
	no := false
	cte, _ := hybrid.Resolve(hybrid.Input{Owner: testOwner, HasGPS: &no})
	r.Contains(cte, "(m.latitude IS NULL OR m.longitude IS NULL)")
}
```

- [ ] **Step 2: Run tests to verify failure**

```
go test -tags sqlite_fts5 ./internal/search/hybrid/ -run 'TestFilter_(Cameras|Lenses|AnyTagKeys|HasGPS)' -v
```

Expected: build failure ("unknown field Cameras in struct literal of type hybrid.Input" — etc.).

- [ ] **Step 3: Extend `hybrid.Input` and `Resolve`**

In `internal/search/hybrid/filter.go`:

Append four fields to the `Input` struct (before the closing brace) and update its docstring. Insert the field block after `MediaType *string`:

```go
	// Cameras, when non-empty, exact-matches (make || ' ' || model)
	// against any value (OR-composed). Each value adds one bind in
	// input order to the args slice.
	Cameras []string
	// Lenses, when non-empty, exact-matches lens_model against any
	// value (OR-composed). Each value adds one bind in input order.
	Lenses []string
	// AnyTagKeys lists tag stems where a media is a hit if it carries
	// ANY of them (OR-composed). Distinct from TagKeys which is
	// AND-composed across multiple typed search chips. The sidebar
	// facet drives this field; the chip resolver drives TagKeys.
	AnyTagKeys []string
	// HasGPS, when non-nil, narrows on latitude/longitude presence.
	// *true means latitude AND longitude are both NOT NULL; *false
	// means either is NULL. nil omits the predicate entirely.
	HasGPS *bool
```

In `Resolve`, add the new conds after `MediaType`'s block and before the `IncludeHidden` block:

```go
	if len(in.Cameras) > 0 {
		placeholders := strings.Repeat("?, ", len(in.Cameras))
		placeholders = placeholders[:len(placeholders)-2] // drop trailing ", "
		conds = append(conds,
			fmt.Sprintf("(m.make || ' ' || m.model) IN (%s)", placeholders))
		for _, v := range in.Cameras {
			args = append(args, v)
		}
	}

	if len(in.Lenses) > 0 {
		placeholders := strings.Repeat("?, ", len(in.Lenses))
		placeholders = placeholders[:len(placeholders)-2]
		conds = append(conds,
			fmt.Sprintf("m.lens_model IN (%s)", placeholders))
		for _, v := range in.Lenses {
			args = append(args, v)
		}
	}

	if len(in.AnyTagKeys) > 0 {
		placeholders := strings.Repeat("?, ", len(in.AnyTagKeys))
		placeholders = placeholders[:len(placeholders)-2]
		conds = append(conds, fmt.Sprintf(
			`EXISTS (SELECT 1 FROM media_tags mt
                      JOIN ai_results r ON mt.result_id = r.id
                     WHERE r.media_id = m.id AND r.task = 'tag' AND r.status = 'active'
                       AND mt.tag_key IN (%s))`,
			placeholders))
		for _, v := range in.AnyTagKeys {
			args = append(args, v)
		}
	}

	if in.HasGPS != nil {
		if *in.HasGPS {
			conds = append(conds, "m.latitude IS NOT NULL AND m.longitude IS NOT NULL")
		} else {
			conds = append(conds, "(m.latitude IS NULL OR m.longitude IS NULL)")
		}
	}
```

Also update the package-level docstring at the top to mention the new fields.

- [ ] **Step 4: Run tests to verify pass**

```
go test -tags sqlite_fts5 ./internal/search/hybrid/ -v
```

Expected: PASS for all five new tests plus existing tests.

- [ ] **Step 5: Commit**

```bash
git add internal/search/hybrid/filter.go internal/search/hybrid/filter_test.go
git commit -m "$(cat <<'EOF'
feat(hybrid): add camera/lens/any-tag/has-gps filter fields

Cameras and Lenses are AnyOf-composed. AnyTagKeys is OR-composed
(single EXISTS with IN list) — distinct from TagKeys which keeps its
existing AND-composed semantics for the typed-chip path. HasGPS is
nil-tri-state. Sidebar-facets v1 spec.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 2 — Extend `media.ListFilter` and `Repo.List`

**Files:**
- Modify: `internal/media/media.go`
- Modify: `internal/media/repo.go`
- Modify: `internal/media/repo_test.go`

`ListFilter` already has `Type` (== media_type). Add the four new fields. `Repo.List` builds its WHERE inline (different code path from `hybrid.Resolve`), so we extend it parallel.

- [ ] **Step 1: Write failing tests**

Append to `internal/media/repo_test.go` (find an existing list-test fixture to model after — e.g. `TestRepoList...` style; build small fixtures and assert filtered output). One test per new field plus a combo test:

```go
// TestRepoList_Cameras — narrows on (make || ' ' || model) IN (...)
func TestRepoList_Cameras(t *testing.T) {
	r := require.New(t)
	db := testutil.OpenTestDB(t)
	repo := media.NewRepo(db, db) // pattern from existing tests
	owner := owners.Principal{Hub: "h", UserID: "u"}

	insertMedia(t, db, owner, "id-sony", media.Media{Make: "Sony", Model: "A7R IV"})
	insertMedia(t, db, owner, "id-canon", media.Media{Make: "Canon", Model: "EOS R5"})
	insertMedia(t, db, owner, "id-iphone", media.Media{Make: "Apple", Model: "iPhone 15 Pro"})

	got, err := repo.List(t.Context(), media.ListFilter{
		Owner:   owner,
		Cameras: []string{"Sony A7R IV", "Apple iPhone 15 Pro"},
	})
	r.NoError(err)
	gotIDs := idsOf(got)
	sort.Strings(gotIDs)
	r.Equal([]string{"id-iphone", "id-sony"}, gotIDs)
}

// TestRepoList_Lenses — narrows on lens_model IN (...)
func TestRepoList_Lenses(t *testing.T) {
	r := require.New(t)
	db := testutil.OpenTestDB(t)
	repo := media.NewRepo(db, db)
	owner := owners.Principal{Hub: "h", UserID: "u"}

	insertMedia(t, db, owner, "id-2470", media.Media{LensModel: "FE 24-70mm F2.8 GM"})
	insertMedia(t, db, owner, "id-50", media.Media{LensModel: "FE 50mm F1.4 GM"})

	got, err := repo.List(t.Context(), media.ListFilter{
		Owner:  owner,
		Lenses: []string{"FE 24-70mm F2.8 GM"},
	})
	r.NoError(err)
	r.Equal([]string{"id-2470"}, idsOf(got))
}

// TestRepoList_HasGPS_True returns only geotagged rows.
func TestRepoList_HasGPS_True(t *testing.T) {
	r := require.New(t)
	db := testutil.OpenTestDB(t)
	repo := media.NewRepo(db, db)
	owner := owners.Principal{Hub: "h", UserID: "u"}

	insertMedia(t, db, owner, "id-geo", media.Media{Latitude: ptrFloat(48.8), Longitude: ptrFloat(2.3)})
	insertMedia(t, db, owner, "id-nogeo", media.Media{})

	yes := true
	got, err := repo.List(t.Context(), media.ListFilter{Owner: owner, HasGPS: &yes})
	r.NoError(err)
	r.Equal([]string{"id-geo"}, idsOf(got))
}

// TestRepoList_HasGPS_False returns only non-geotagged rows.
func TestRepoList_HasGPS_False(t *testing.T) {
	r := require.New(t)
	db := testutil.OpenTestDB(t)
	repo := media.NewRepo(db, db)
	owner := owners.Principal{Hub: "h", UserID: "u"}

	insertMedia(t, db, owner, "id-geo", media.Media{Latitude: ptrFloat(48.8), Longitude: ptrFloat(2.3)})
	insertMedia(t, db, owner, "id-nogeo", media.Media{})

	no := false
	got, err := repo.List(t.Context(), media.ListFilter{Owner: owner, HasGPS: &no})
	r.NoError(err)
	r.Equal([]string{"id-nogeo"}, idsOf(got))
}

// TestRepoList_AnyTagKeys filters on at-least-one tag.
func TestRepoList_AnyTagKeys(t *testing.T) {
	r := require.New(t)
	db := testutil.OpenTestDB(t)
	repo := media.NewRepo(db, db)
	owner := owners.Principal{Hub: "h", UserID: "u"}

	insertMedia(t, db, owner, "id-dog", media.Media{})
	insertMedia(t, db, owner, "id-cat", media.Media{})
	insertMedia(t, db, owner, "id-tree", media.Media{})
	insertTag(t, db, owner, "id-dog", "dog", "Dog")
	insertTag(t, db, owner, "id-cat", "cat", "Cat")
	insertTag(t, db, owner, "id-tree", "tree", "Tree")

	got, err := repo.List(t.Context(), media.ListFilter{
		Owner:      owner,
		AnyTagKeys: []string{"dog", "cat"},
	})
	r.NoError(err)
	gotIDs := idsOf(got)
	sort.Strings(gotIDs)
	r.Equal([]string{"id-cat", "id-dog"}, gotIDs)
}
```

`insertMedia` and `insertTag` are test helpers — if they don't exist yet, add them at the bottom of `repo_test.go`. Look at neighboring tests in the file for the existing seed pattern; they likely INSERT directly into `media` and `media_tags`/`ai_results`. Match that style.

`idsOf(got)` and `ptrFloat` are tiny helpers — define them in the test file if not already present:

```go
func idsOf(ms []media.Media) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.ID)
	}
	return out
}

func ptrFloat(f float64) *float64 { return &f }
```

- [ ] **Step 2: Run tests to verify failure**

```
go test -tags sqlite_fts5 ./internal/media/ -run 'TestRepoList_(Cameras|Lenses|HasGPS|AnyTagKeys)' -v
```

Expected: build failure ("unknown field Cameras in struct literal of type media.ListFilter").

- [ ] **Step 3: Extend `ListFilter`**

In `internal/media/media.go`, after the `IncludeHidden` field of `ListFilter`, add:

```go
	// Cameras, when non-empty, narrows on (make || ' ' || model) matching
	// any of the values (OR-composed). Each value is the canonical
	// "<make> <model>" string used as both URL param and chip label.
	Cameras []string
	// Lenses, when non-empty, narrows on lens_model matching any of the
	// values (OR-composed).
	Lenses []string
	// AnyTagKeys, when non-empty, narrows to media that carry AT LEAST
	// ONE of the supplied tag keys (OR-composed via a single EXISTS).
	AnyTagKeys []string
	// HasGPS narrows on lat/lng presence. *true → both NOT NULL; *false →
	// at least one NULL; nil → no predicate.
	HasGPS *bool
```

- [ ] **Step 4: Extend `Repo.List`**

In `internal/media/repo.go`, inside `List`, after the existing `f.DateTo` block and before the `direction := "ASC"` line, add the new filter conds (mirror the hybrid.Resolve shape):

```go
	if len(f.Cameras) > 0 {
		placeholders := strings.Repeat("?, ", len(f.Cameras))
		placeholders = placeholders[:len(placeholders)-2]
		conds = append(conds,
			"(make || ' ' || model) IN ("+placeholders+")")
		for _, v := range f.Cameras {
			args = append(args, v)
		}
	}
	if len(f.Lenses) > 0 {
		placeholders := strings.Repeat("?, ", len(f.Lenses))
		placeholders = placeholders[:len(placeholders)-2]
		conds = append(conds, "lens_model IN ("+placeholders+")")
		for _, v := range f.Lenses {
			args = append(args, v)
		}
	}
	if len(f.AnyTagKeys) > 0 {
		placeholders := strings.Repeat("?, ", len(f.AnyTagKeys))
		placeholders = placeholders[:len(placeholders)-2]
		conds = append(conds,
			`EXISTS (SELECT 1 FROM media_tags mt
                      JOIN ai_results r ON mt.result_id = r.id
                     WHERE r.media_id = media.id AND r.task = 'tag' AND r.status = 'active'
                       AND mt.tag_key IN (`+placeholders+`))`)
		for _, v := range f.AnyTagKeys {
			args = append(args, v)
		}
	}
	if f.HasGPS != nil {
		if *f.HasGPS {
			conds = append(conds, "latitude IS NOT NULL AND longitude IS NOT NULL")
		} else {
			conds = append(conds, "(latitude IS NULL OR longitude IS NULL)")
		}
	}
```

(`Repo.List` uses unqualified column names — see existing predicates like `make = ?` style — so don't add `m.` prefixes.)

- [ ] **Step 5: Run tests to verify pass**

```
go test -tags sqlite_fts5 ./internal/media/ -run 'TestRepoList_' -v
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/media/media.go internal/media/repo.go internal/media/repo_test.go
git commit -m "$(cat <<'EOF'
feat(media): extend ListFilter with camera/lens/any-tag/has-gps

Mirrors the hybrid.Input extension. Repo.List grows four new conditional
blocks; existing fields and order preserved. Sidebar-facets v1 spec.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 3 — Extend `media.ListGeoFilter` and `Repo.ListGeo`

**Files:**
- Modify: `internal/media/media.go`
- Modify: `internal/media/repo.go`
- Modify: `internal/media/repo_test.go`

ListGeo's contract is geotagged-only, so `HasGPS` is **not** added. Cameras, Lenses, AnyTagKeys, and Type (media_type) are added. The current `ListGeo` implementation hardcodes the WHERE — refactor to a builder shape similar to `List`.

- [ ] **Step 1: Write failing tests**

Append to `internal/media/repo_test.go`:

```go
// TestRepoListGeo_Cameras — narrows geotagged rows by camera.
func TestRepoListGeo_Cameras(t *testing.T) {
	r := require.New(t)
	db := testutil.OpenTestDB(t)
	repo := media.NewRepo(db, db)
	owner := owners.Principal{Hub: "h", UserID: "u"}

	insertMedia(t, db, owner, "id-sony-geo", media.Media{
		Make: "Sony", Model: "A7R IV",
		Latitude: ptrFloat(48.8), Longitude: ptrFloat(2.3),
	})
	insertMedia(t, db, owner, "id-canon-geo", media.Media{
		Make: "Canon", Model: "EOS R5",
		Latitude: ptrFloat(40.7), Longitude: ptrFloat(-74.0),
	})
	// Non-geotagged Sony — must NOT appear (ListGeo's contract).
	insertMedia(t, db, owner, "id-sony-nogeo", media.Media{
		Make: "Sony", Model: "A7R IV",
	})

	got, err := repo.ListGeo(t.Context(), media.ListGeoFilter{
		Owner:   owner,
		Cameras: []string{"Sony A7R IV"},
	})
	r.NoError(err)
	r.Equal([]string{"id-sony-geo"}, idsOf(got))
}

// TestRepoListGeo_Type — narrows on media_type.
func TestRepoListGeo_Type(t *testing.T) {
	r := require.New(t)
	db := testutil.OpenTestDB(t)
	repo := media.NewRepo(db, db)
	owner := owners.Principal{Hub: "h", UserID: "u"}

	insertMedia(t, db, owner, "id-photo", media.Media{
		Type:     media.TypePhoto,
		Latitude: ptrFloat(48.8), Longitude: ptrFloat(2.3),
	})
	insertMedia(t, db, owner, "id-video", media.Media{
		Type:     media.TypeVideo,
		Latitude: ptrFloat(48.8), Longitude: ptrFloat(2.3),
	})

	video := media.TypeVideo
	got, err := repo.ListGeo(t.Context(), media.ListGeoFilter{
		Owner: owner,
		Type:  &video,
	})
	r.NoError(err)
	r.Equal([]string{"id-video"}, idsOf(got))
}
```

- [ ] **Step 2: Run tests to verify failure**

```
go test -tags sqlite_fts5 ./internal/media/ -run 'TestRepoListGeo_(Cameras|Type)' -v
```

Expected: build failure ("unknown field Cameras in struct literal of type media.ListGeoFilter").

- [ ] **Step 3: Extend `ListGeoFilter`**

In `internal/media/media.go`, replace the `ListGeoFilter` struct body with:

```go
type ListGeoFilter struct {
	Owner owners.Principal
	// IncludeHidden, when false (default), excludes rows whose
	// hidden_at IS NOT NULL. Set true only by callers that have
	// validated an unlock claim before calling.
	IncludeHidden bool
	// Type narrows on media_type. nil means both photo and video.
	Type *Type
	// Cameras narrows on (make || ' ' || model) matching any value.
	Cameras []string
	// Lenses narrows on lens_model matching any value.
	Lenses []string
	// AnyTagKeys narrows to media carrying at least one of the keys.
	AnyTagKeys []string
}
```

- [ ] **Step 4: Refactor `Repo.ListGeo` to a builder**

In `internal/media/repo.go`, replace the entire `ListGeo` implementation with a builder that mirrors `List`. The geotagged-only invariant (`latitude IS NOT NULL AND longitude IS NOT NULL`) and the `paired_with_id IS NULL` predicate are baseline conds:

```go
func (r *Repo) ListGeo(ctx context.Context, f ListGeoFilter) ([]Media, error) {
	conds := []string{
		"owner_hub = ?", "owner_user_id = ?",
		"latitude IS NOT NULL", "longitude IS NOT NULL",
		"paired_with_id IS NULL",
	}
	args := []any{f.Owner.Hub, f.Owner.UserID}

	if !f.IncludeHidden {
		conds = append(conds, "hidden_at IS NULL")
	}
	if f.Type != nil {
		conds = append(conds, "media_type = ?")
		args = append(args, string(*f.Type))
	}
	if len(f.Cameras) > 0 {
		placeholders := strings.Repeat("?, ", len(f.Cameras))
		placeholders = placeholders[:len(placeholders)-2]
		conds = append(conds, "(make || ' ' || model) IN ("+placeholders+")")
		for _, v := range f.Cameras {
			args = append(args, v)
		}
	}
	if len(f.Lenses) > 0 {
		placeholders := strings.Repeat("?, ", len(f.Lenses))
		placeholders = placeholders[:len(placeholders)-2]
		conds = append(conds, "lens_model IN ("+placeholders+")")
		for _, v := range f.Lenses {
			args = append(args, v)
		}
	}
	if len(f.AnyTagKeys) > 0 {
		placeholders := strings.Repeat("?, ", len(f.AnyTagKeys))
		placeholders = placeholders[:len(placeholders)-2]
		conds = append(conds,
			`EXISTS (SELECT 1 FROM media_tags mt
                      JOIN ai_results r ON mt.result_id = r.id
                     WHERE r.media_id = media.id AND r.task = 'tag' AND r.status = 'active'
                       AND mt.tag_key IN (`+placeholders+`))`)
		for _, v := range f.AnyTagKeys {
			args = append(args, v)
		}
	}

	q := mediaSelect + " WHERE " + strings.Join(conds, " AND ") +
		" ORDER BY timestamp IS NULL ASC, timestamp DESC, imported_at DESC, id DESC"
	rows, err := r.ro.QueryContext(ctx, q, args...)
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

- [ ] **Step 5: Run all tests in the media package**

```
go test -tags sqlite_fts5 ./internal/media/ -v
```

Expected: ALL existing ListGeo tests still pass + new Cameras/Type tests pass. If an existing test asserts an exact SQL string, update it to match the new builder output.

- [ ] **Step 6: Commit**

```bash
git add internal/media/media.go internal/media/repo.go internal/media/repo_test.go
git commit -m "$(cat <<'EOF'
feat(media): extend ListGeoFilter with type/camera/lens/any-tag

Refactor ListGeo to share the conds-builder shape used by List.
HasGPS is intentionally not added: ListGeo's contract is geotagged-only.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 4 — Add expression indexes for camera + lens

**Files:**
- Modify: `internal/db/migrations/000001_initial_schema.up.sql`
- Modify: `internal/db/migrations/000001_initial_schema.down.sql`

Per CLAUDE.md schema policy, both files move together. Add the indexes near the existing `media_tags_key_idx` declaration in the up file; add their DROPs in the matching position in the down file.

- [ ] **Step 1: Inspect current migration**

Read `internal/db/migrations/000001_initial_schema.up.sql` and locate the existing `CREATE INDEX media_*` declarations and the `media_tags_key_idx`. New indexes go beside the other media indexes for locality.

- [ ] **Step 2: Add expression indexes to up migration**

In `internal/db/migrations/000001_initial_schema.up.sql`, after the last existing `CREATE INDEX media_*` line, add:

```sql
-- Sidebar facets: aggregations on (make || ' ' || model) and lens_model.
-- Owner-scoped, partial-indexed to skip hidden rows and sidecars (which
-- are already excluded by every user-facing list query).
CREATE INDEX media_owner_camera_visible_idx
  ON media(owner_hub, owner_user_id, (make || ' ' || model))
  WHERE hidden_at IS NULL AND paired_with_id IS NULL
    AND make IS NOT NULL AND model IS NOT NULL;

CREATE INDEX media_owner_lens_visible_idx
  ON media(owner_hub, owner_user_id, lens_model)
  WHERE hidden_at IS NULL AND paired_with_id IS NULL
    AND lens_model IS NOT NULL;
```

- [ ] **Step 3: Mirror DROPs in down migration**

In `internal/db/migrations/000001_initial_schema.down.sql`, add (in the symmetric position — usually before the table DROPs):

```sql
DROP INDEX IF EXISTS media_owner_lens_visible_idx;
DROP INDEX IF EXISTS media_owner_camera_visible_idx;
```

- [ ] **Step 4: Run migrations forward and back**

```
go test -tags sqlite_fts5 ./internal/db/ ./internal/migrate/ -v
```

Expected: PASS. The migration tests (if any) will roll forward, then roll back, then forward again.

If no migration round-trip test exists, manually:
```
go test -tags sqlite_fts5 ./internal/testutil/ -v
```
Expected: `OpenTestDB` continues to apply the migration cleanly.

- [ ] **Step 5: Commit**

```bash
git add internal/db/migrations/000001_initial_schema.up.sql internal/db/migrations/000001_initial_schema.down.sql
git commit -m "$(cat <<'EOF'
feat(db): expression indexes for camera + lens facets

Owner-scoped partial indexes covering the (make||' '||model) and
lens_model aggregations the /facets endpoint will run. Pre-alpha
migration policy: edited 000001_initial_schema.{up,down}.sql in place.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 5 — Create `FacetService.Aggregate`

**Files:**
- Create: `internal/service/facets/service.go`
- Create: `internal/service/facets/service_test.go`

The service runs five queries — one per facet — each built from a `hybrid.Resolve` CTE that omits the facet's own selection (the exclude-self rule). It's owner-scoped; the principal stamps onto the `hybrid.Input` before each `Resolve` call.

- [ ] **Step 1: Write failing tests**

Create `internal/service/facets/service_test.go`:

```go
package facets_test

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/service/facets"
	"github.com/wesm/fotobank/internal/testutil"
)

var owner = owners.Principal{Hub: "h", UserID: "u"}

// TestAggregate_Cameras — top-N cameras by count, owner-scoped.
func TestAggregate_Cameras(t *testing.T) {
	r := require.New(t)
	db := testutil.OpenTestDB(t)
	insertSeedFixtures(t, db) // see helper below
	svc := facets.New(db)

	out, err := svc.Aggregate(t.Context(), owner, facets.Filters{})
	r.NoError(err)
	r.Len(out.Cameras, 2)
	r.Equal("Sony A7R IV", out.Cameras[0].Value)
	r.Equal(2, out.Cameras[0].Count)
	r.Equal("Canon EOS R5", out.Cameras[1].Value)
	r.Equal(1, out.Cameras[1].Count)
}

// TestAggregate_Lenses — top-N lenses by count.
func TestAggregate_Lenses(t *testing.T) {
	r := require.New(t)
	db := testutil.OpenTestDB(t)
	insertSeedFixtures(t, db)
	svc := facets.New(db)

	out, err := svc.Aggregate(t.Context(), owner, facets.Filters{})
	r.NoError(err)
	r.Len(out.Lenses, 1)
	r.Equal("FE 24-70mm F2.8 GM", out.Lenses[0].Value)
}

// TestAggregate_Tags — tag aggregation joins ai_results+media_tags.
func TestAggregate_Tags(t *testing.T) {
	r := require.New(t)
	db := testutil.OpenTestDB(t)
	insertSeedFixtures(t, db)
	svc := facets.New(db)

	out, err := svc.Aggregate(t.Context(), owner, facets.Filters{})
	r.NoError(err)
	keys := make([]string, len(out.Tags))
	for i, t := range out.Tags {
		keys[i] = t.Key
	}
	sort.Strings(keys)
	r.Equal([]string{"cat", "dog"}, keys)
}

// TestAggregate_Places — with/without GPS counts.
func TestAggregate_Places(t *testing.T) {
	r := require.New(t)
	db := testutil.OpenTestDB(t)
	insertSeedFixtures(t, db)
	svc := facets.New(db)

	out, err := svc.Aggregate(t.Context(), owner, facets.Filters{})
	r.NoError(err)
	r.Equal(2, out.Places.WithGPS)
	r.Equal(1, out.Places.WithoutGPS)
}

// TestAggregate_MediaTypes — both photo and video buckets.
func TestAggregate_MediaTypes(t *testing.T) {
	r := require.New(t)
	db := testutil.OpenTestDB(t)
	insertSeedFixtures(t, db)
	svc := facets.New(db)

	out, err := svc.Aggregate(t.Context(), owner, facets.Filters{})
	r.NoError(err)
	r.Len(out.MediaTypes, 2)
}

// TestAggregate_ExcludeSelfRule_Cameras — when Cameras is selected,
// Camera counts are computed AS IF Cameras were not applied; other
// facets honor it.
func TestAggregate_ExcludeSelfRule_Cameras(t *testing.T) {
	r := require.New(t)
	db := testutil.OpenTestDB(t)
	insertSeedFixtures(t, db)
	svc := facets.New(db)

	// Filter to "Sony A7R IV" only. Camera counts should still show
	// both cameras (exclude-self), but Lenses/Places/MediaTypes/Tags
	// should reflect the Sony scope.
	out, err := svc.Aggregate(t.Context(), owner, facets.Filters{
		Cameras: []string{"Sony A7R IV"},
	})
	r.NoError(err)
	cameraValues := make([]string, len(out.Cameras))
	for i, c := range out.Cameras {
		cameraValues[i] = c.Value
	}
	sort.Strings(cameraValues)
	r.Equal([]string{"Canon EOS R5", "Sony A7R IV"}, cameraValues,
		"camera dropdown should still show Canon — exclude-self rule")
}

// TestAggregate_OwnerScoped — rows belonging to a different principal
// must not appear in any facet's counts.
func TestAggregate_OwnerScoped(t *testing.T) {
	r := require.New(t)
	db := testutil.OpenTestDB(t)
	insertSeedFixtures(t, db)
	other := owners.Principal{Hub: "h", UserID: "OTHER"}
	insertMedia(t, db, other, "other-1", media.Media{Make: "Pentax", Model: "K1"})
	svc := facets.New(db)

	out, err := svc.Aggregate(t.Context(), owner, facets.Filters{})
	r.NoError(err)
	for _, c := range out.Cameras {
		r.NotEqual("Pentax K1", c.Value)
	}
}
```

The helper `insertSeedFixtures` lives in the test file:

```go
// 3 media: 2 Sony (one geotagged + one not), 1 Canon (geotagged).
// 1 video Sony, 2 photo. Two with the same lens (Sony 24-70). Tags:
// the geotagged Sony has "dog", the Canon has "cat".
func insertSeedFixtures(t *testing.T, db *sql.DB) {
	t.Helper()
	insertMedia(t, db, owner, "m-sony-geo-photo", media.Media{
		Type: media.TypePhoto, Make: "Sony", Model: "A7R IV",
		LensModel: "FE 24-70mm F2.8 GM",
		Latitude:  ptrFloat(48.8), Longitude: ptrFloat(2.3),
	})
	insertMedia(t, db, owner, "m-sony-nogeo-video", media.Media{
		Type: media.TypeVideo, Make: "Sony", Model: "A7R IV",
	})
	insertMedia(t, db, owner, "m-canon-geo-photo", media.Media{
		Type: media.TypePhoto, Make: "Canon", Model: "EOS R5",
		LensModel: "FE 24-70mm F2.8 GM",
		Latitude:  ptrFloat(40.7), Longitude: ptrFloat(-74.0),
	})
	insertTag(t, db, owner, "m-sony-geo-photo", "dog", "Dog")
	insertTag(t, db, owner, "m-canon-geo-photo", "cat", "Cat")
}
```

(`insertMedia`, `insertTag`, `ptrFloat` are the same helpers from Task 2's test file. The cleanest approach: factor them into `internal/testutil/seed.go` as exported helpers so both `media_test` and `facets_test` can use them. Add them there before writing the facets tests if not already factored.)

- [ ] **Step 2: Run tests to verify failure**

```
go test -tags sqlite_fts5 ./internal/service/facets/ -v
```

Expected: build failure ("package facets is not in std").

- [ ] **Step 3: Implement `FacetService`**

Create `internal/service/facets/service.go`:

```go
// Package facets is the auth boundary for the /api/v1/facets endpoint.
// It composes hybrid.Resolve five times — one per facet — each call
// omitting the facet's own selection (the Lightroom exclude-self rule)
// so each dropdown shows reachable alternatives.
package facets

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/search/hybrid"
)

// Filters is the auth-stamped facet input. Owner is set inside Aggregate
// from the caller principal; clients of this package never set it.
type Filters struct {
	Cameras    []string
	Lenses     []string
	AnyTagKeys []string
	HasGPS     *bool
	MediaType  *string
	// /search-only filters that /facets honors when present:
	DateAfter     *time.Time
	DateBefore    *time.Time
	TagKeys       []string // AND-composed (the typed-chip resolver path)
	LocationLabel *string
	IncludeHidden bool
}

// CameraCount, LensCount, MediaTypeCount share a shape: { Value, Count }.
type ValueCount struct {
	Value string
	Count int
}

// TagCount carries the canonical key (URL param) plus the display label.
type TagCount struct {
	Key   string
	Label string
	Count int
}

// PlacesCount aggregates lat/lng presence into two buckets.
type PlacesCount struct {
	WithGPS    int
	WithoutGPS int
}

// Response is the value Aggregate returns. Cameras/Lenses/MediaTypes are
// ordered by count desc, then value asc as tiebreaker.
type Response struct {
	Cameras    []ValueCount
	Lenses     []ValueCount
	Tags       []TagCount
	Places     PlacesCount
	MediaTypes []ValueCount
}

const facetTopN = 200

// Service is owner-scoped. ro is the read-only DB pool.
type Service struct {
	ro *sql.DB
}

// New constructs a Service.
func New(ro *sql.DB) *Service { return &Service{ro: ro} }

// Aggregate stamps owner onto the input and runs five exclude-self
// queries. f.Owner is ignored — the caller principal is authoritative.
func (s *Service) Aggregate(
	ctx context.Context, caller owners.Principal, f Filters,
) (Response, error) {
	in := s.toHybridInput(caller, f)

	cameras, err := s.aggregateCameras(ctx, withoutCameras(in))
	if err != nil {
		return Response{}, fmt.Errorf("facets cameras: %w", err)
	}
	lenses, err := s.aggregateLenses(ctx, withoutLenses(in))
	if err != nil {
		return Response{}, fmt.Errorf("facets lenses: %w", err)
	}
	tags, err := s.aggregateTags(ctx, withoutAnyTagKeys(in))
	if err != nil {
		return Response{}, fmt.Errorf("facets tags: %w", err)
	}
	places, err := s.aggregatePlaces(ctx, withoutHasGPS(in))
	if err != nil {
		return Response{}, fmt.Errorf("facets places: %w", err)
	}
	mediaTypes, err := s.aggregateMediaTypes(ctx, withoutMediaType(in))
	if err != nil {
		return Response{}, fmt.Errorf("facets media types: %w", err)
	}

	return Response{
		Cameras: cameras, Lenses: lenses, Tags: tags,
		Places: places, MediaTypes: mediaTypes,
	}, nil
}

func (s *Service) toHybridInput(caller owners.Principal, f Filters) hybrid.Input {
	return hybrid.Input{
		Owner:         caller,
		DateAfter:     f.DateAfter,
		DateBefore:    f.DateBefore,
		TagKeys:       f.TagKeys,
		LocationLabel: f.LocationLabel,
		MediaType:     f.MediaType,
		IncludeHidden: f.IncludeHidden,
		Cameras:       f.Cameras,
		Lenses:        f.Lenses,
		AnyTagKeys:    f.AnyTagKeys,
		HasGPS:        f.HasGPS,
	}
}

// withoutCameras returns a copy of in with Cameras cleared.
func withoutCameras(in hybrid.Input) hybrid.Input {
	in.Cameras = nil
	return in
}
func withoutLenses(in hybrid.Input) hybrid.Input {
	in.Lenses = nil
	return in
}
func withoutAnyTagKeys(in hybrid.Input) hybrid.Input {
	in.AnyTagKeys = nil
	return in
}
func withoutHasGPS(in hybrid.Input) hybrid.Input {
	in.HasGPS = nil
	return in
}
func withoutMediaType(in hybrid.Input) hybrid.Input {
	in.MediaType = nil
	return in
}

func (s *Service) aggregateCameras(ctx context.Context, in hybrid.Input) ([]ValueCount, error) {
	cte, args := hybrid.Resolve(in)
	q := fmt.Sprintf(`
WITH filter AS (%s)
SELECT (m.make || ' ' || m.model) AS value, COUNT(*) AS count
FROM filter f JOIN media m ON m.id = f.id
WHERE m.make IS NOT NULL AND m.model IS NOT NULL
GROUP BY value
ORDER BY count DESC, value ASC
LIMIT %d`, cte, facetTopN)
	return scanValueCount(ctx, s.ro, q, args)
}

func (s *Service) aggregateLenses(ctx context.Context, in hybrid.Input) ([]ValueCount, error) {
	cte, args := hybrid.Resolve(in)
	q := fmt.Sprintf(`
WITH filter AS (%s)
SELECT m.lens_model AS value, COUNT(*) AS count
FROM filter f JOIN media m ON m.id = f.id
WHERE m.lens_model IS NOT NULL
GROUP BY value
ORDER BY count DESC, value ASC
LIMIT %d`, cte, facetTopN)
	return scanValueCount(ctx, s.ro, q, args)
}

func (s *Service) aggregateTags(ctx context.Context, in hybrid.Input) ([]TagCount, error) {
	cte, args := hybrid.Resolve(in)
	q := fmt.Sprintf(`
WITH filter AS (%s)
SELECT mt.tag_key AS key, MAX(mt.tag_label) AS label, COUNT(DISTINCT f.id) AS count
FROM filter f
JOIN ai_results r ON r.media_id = f.id
                 AND r.task = 'tag' AND r.status = 'active'
JOIN media_tags mt ON mt.result_id = r.id
GROUP BY mt.tag_key
ORDER BY count DESC, key ASC
LIMIT %d`, cte, facetTopN)
	rows, err := s.ro.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []TagCount{}
	for rows.Next() {
		var t TagCount
		if err := rows.Scan(&t.Key, &t.Label, &t.Count); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Service) aggregatePlaces(ctx context.Context, in hybrid.Input) (PlacesCount, error) {
	cte, args := hybrid.Resolve(in)
	q := fmt.Sprintf(`
WITH filter AS (%s)
SELECT
  COUNT(*) FILTER (WHERE m.latitude IS NOT NULL AND m.longitude IS NOT NULL) AS with_gps,
  COUNT(*) FILTER (WHERE m.latitude IS NULL OR m.longitude IS NULL) AS without_gps
FROM filter f JOIN media m ON m.id = f.id`, cte)
	row := s.ro.QueryRowContext(ctx, q, args...)
	var p PlacesCount
	if err := row.Scan(&p.WithGPS, &p.WithoutGPS); err != nil {
		return PlacesCount{}, err
	}
	return p, nil
}

func (s *Service) aggregateMediaTypes(ctx context.Context, in hybrid.Input) ([]ValueCount, error) {
	cte, args := hybrid.Resolve(in)
	q := fmt.Sprintf(`
WITH filter AS (%s)
SELECT m.media_type AS value, COUNT(*) AS count
FROM filter f JOIN media m ON m.id = f.id
GROUP BY value
ORDER BY count DESC, value ASC`, cte)
	return scanValueCount(ctx, s.ro, q, args)
}

func scanValueCount(ctx context.Context, ro *sql.DB, q string, args []any) ([]ValueCount, error) {
	rows, err := ro.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []ValueCount{}
	for rows.Next() {
		var vc ValueCount
		if err := rows.Scan(&vc.Value, &vc.Count); err != nil {
			return nil, err
		}
		out = append(out, vc)
	}
	return out, rows.Err()
}
```

Add the `time` import (`Filters` carries `*time.Time` fields).

- [ ] **Step 4: Run tests to verify pass**

```
go test -tags sqlite_fts5 ./internal/service/facets/ -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/service/facets/service.go internal/service/facets/service_test.go
git commit -m "$(cat <<'EOF'
feat(facets): owner-scoped FacetService.Aggregate

Five exclude-self queries — Cameras, Lenses, Tags (joining
ai_results+media_tags), Places (with/without GPS), Media Types — each
built from hybrid.Resolve with the facet's own selection cleared
before the call. /api/v1/facets handler will plug into this service.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 6 — Add `GET /api/v1/facets` HTTP route

**Files:**
- Create: `internal/httpapi/facets.go`
- Create: `internal/httpapi/facets_test.go`
- Modify: `internal/httpapi/api.go`

The route accepts the union of v1 facet params plus the /search filter context (date_after/before, location, tag, include_hidden, media_type). It does **not** accept `q` (per spec).

- [ ] **Step 1: Write failing test**

Create `internal/httpapi/facets_test.go`:

```go
package httpapi_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/httpapi"
	"github.com/wesm/fotobank/internal/service/facets"
	"github.com/wesm/fotobank/internal/testutil"
)

// TestFacetsRoute_OwnerScoped — caller principal flows from middleware
// into the service. Empty filter set returns the full facet response.
func TestFacetsRoute_OwnerScoped(t *testing.T) {
	r := require.New(t)
	db := testutil.OpenTestDB(t)
	seedFacetsRouteFixtures(t, db) // helper inserts 3 media as in Task 5
	hiddenAuth := hidden.NewService(...) // mirror /search's test fixture
	svc := facets.New(db.ReadDB(), hiddenAuth)

	handler, err := httpapi.New(httpapi.Deps{
		IdentityProvider: testutil.StubIdentity(testutil.PrincipalA),
		Facets:           svc,
		HiddenAuth:       hiddenAuth,
	})
	r.NoError(err)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/facets", nil)
	handler.ServeHTTP(rec, req)

	r.Equal(http.StatusOK, rec.Code)
	r.Contains(rec.Body.String(), `"cameras":`)
	r.Contains(rec.Body.String(), `"places":`)
	r.Contains(rec.Body.String(), `"media_types":`)
}

// TestFacetsRoute_FilterParams — facet filters round-trip from query
// string to the service.
func TestFacetsRoute_FilterParams(t *testing.T) {
	r := require.New(t)
	db := testutil.OpenTestDB(t)
	seedFacetsRouteFixtures(t, db)
	hiddenAuth := hidden.NewService(...) // mirror /search's test fixture
	svc := facets.New(db.ReadDB(), hiddenAuth)

	handler, err := httpapi.New(httpapi.Deps{
		IdentityProvider: testutil.StubIdentity(testutil.PrincipalA),
		Facets:           svc,
		HiddenAuth:       hiddenAuth,
	})
	r.NoError(err)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/facets?camera=Sony+A7R+IV&has_gps=1", nil)
	handler.ServeHTTP(rec, req)

	r.Equal(http.StatusOK, rec.Code)
	// Lenses/Places counts should reflect the Sony+geotagged scope.
	// Cameras still shows both (exclude-self).
	r.Contains(rec.Body.String(), `"Sony A7R IV"`)
	r.Contains(rec.Body.String(), `"Canon EOS R5"`)
}
```

(`testutil.StubIdentity` and `testutil.PrincipalA` are existing helpers — search for `StubIdentity` in `internal/testutil/` and use the existing pattern. If only the `testutil.OpenTestDB` form exists, replicate the stub-identity middleware pattern from `internal/httpapi/me_test.go`.)

`seedFacetsRouteFixtures` mirrors `insertSeedFixtures` from Task 5 — pin `owner` to whichever principal `testutil.PrincipalA` is.

- [ ] **Step 2: Run test to verify failure**

```
go test -tags sqlite_fts5 ./internal/httpapi/ -run TestFacetsRoute -v
```

Expected: build failure ("Facets is not a field of httpapi.Deps").

- [ ] **Step 3: Create the route file**

Create `internal/httpapi/facets.go`:

```go
// Package httpapi — facets surface. GET /api/v1/facets returns
// per-facet counts for the caller's library, computed under the
// exclude-self rule so each dropdown shows reachable alternatives.
package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/wesm/fotobank/internal/auth/hidden"
	"github.com/wesm/fotobank/internal/identity"
	"github.com/wesm/fotobank/internal/service/facets"
)

// registerFacetsRoutes binds GET /api/v1/facets. The IncludeHidden
// gate lives inside facets.Service.Aggregate (it's the only call site
// that mutates SQL based on IncludeHidden), so the route only needs
// the service handle — hiddenAuth is plumbed into the service via
// facets.New, not threaded through here.
func registerFacetsRoutes(api huma.API, svc *facets.Service) {
	if svc == nil {
		return
	}
	huma.Register(api, huma.Operation{
		OperationID: "facets",
		Method:      http.MethodGet,
		Path:        "/api/v1/facets",
		Summary:     "Per-facet counts for the caller's library (exclude-self semantics)",
	}, func(ctx context.Context, in *facetsInput) (*facetsOutput, error) {
		return handleFacets(ctx, svc, in)
	})
}

// facetsInput is the bound query-string surface. Multi-value params
// (Camera, Lens, FacetTag, Tag) bind via huma's repeat-param convention,
// which requires the `,explode` modifier — without it, repeated
// `?camera=A&camera=B` collapses to the last value silently. Mirrors
// the existing /search route's `Tag []string `query:"tag,explode"``.
type facetsInput struct {
	Camera        []string `query:"camera,explode"`
	Lens          []string `query:"lens,explode"`
	FacetTag      []string `query:"facet_tag,explode"`
	Tag           []string `query:"tag,explode"`
	HasGPS        *bool    `query:"has_gps"`
	MediaType     *string  `query:"media_type" enum:"photo,video"`
	DateAfter     *string  `query:"date_after"`
	DateBefore    *string  `query:"date_before"`
	Location      *string  `query:"location"`
	IncludeHidden bool     `query:"include_hidden"`
}

type facetValueDTO struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

type facetTagDTO struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Count int    `json:"count"`
}

type facetPlacesDTO struct {
	WithGPS    int `json:"with_gps"`
	WithoutGPS int `json:"without_gps"`
}

type facetsOutput struct {
	Body struct {
		Cameras    []facetValueDTO `json:"cameras"`
		Lenses     []facetValueDTO `json:"lenses"`
		Tags       []facetTagDTO   `json:"tags"`
		Places     facetPlacesDTO  `json:"places"`
		MediaTypes []facetValueDTO `json:"media_types"`
	}
}

func handleFacets(
	ctx context.Context,
	svc *facets.Service,
	in *facetsInput,
) (*facetsOutput, error) {
	caller, err := identity.PrincipalFromContext(ctx)
	if err != nil {
		return nil, err
	}

	// Pass IncludeHidden + UnlockClaim through unchanged. The service
	// does the validation in Aggregate and returns
	// errs.ErrPermissionDenied when IncludeHidden=true but the claim
	// is invalid — that maps to 403 via Translate, matching /search's
	// hidden gate. Pre-validating here would re-create the bypass
	// SF-5's commit 2c728e2 closed (non-HTTP callers like the CLI
	// must inherit the same gate).
	f := facets.Filters{
		Cameras:       in.Camera,
		Lenses:        in.Lens,
		AnyTagKeys:    in.FacetTag,
		TagKeys:       in.Tag,
		HasGPS:        in.HasGPS,
		MediaType:     in.MediaType,
		LocationLabel: in.Location,
		IncludeHidden: in.IncludeHidden,
		UnlockClaim:   hidden.ClaimFromContext(ctx),
	}
	if in.DateAfter != nil {
		t, err := time.Parse(time.RFC3339, *in.DateAfter)
		if err != nil {
			return nil, huma.Error400BadRequest("date_after must be RFC3339")
		}
		f.DateAfter = &t
	}
	if in.DateBefore != nil {
		t, err := time.Parse(time.RFC3339, *in.DateBefore)
		if err != nil {
			return nil, huma.Error400BadRequest("date_before must be RFC3339")
		}
		f.DateBefore = &t
	}

	res, err := svc.Aggregate(ctx, caller, f)
	if err != nil {
		return nil, Translate(err)
	}

	out := &facetsOutput{}
	out.Body.Cameras = toValueDTOs(res.Cameras)
	out.Body.Lenses = toValueDTOs(res.Lenses)
	out.Body.Tags = toTagDTOs(res.Tags)
	out.Body.Places = facetPlacesDTO{
		WithGPS: res.Places.WithGPS, WithoutGPS: res.Places.WithoutGPS,
	}
	out.Body.MediaTypes = toValueDTOs(res.MediaTypes)
	return out, nil
}

func toValueDTOs(in []facets.ValueCount) []facetValueDTO {
	out := make([]facetValueDTO, len(in))
	for i, v := range in {
		out[i] = facetValueDTO{Value: v.Value, Count: v.Count}
	}
	return out
}
func toTagDTOs(in []facets.TagCount) []facetTagDTO {
	out := make([]facetTagDTO, len(in))
	for i, t := range in {
		out[i] = facetTagDTO{Key: t.Key, Label: t.Label, Count: t.Count}
	}
	return out
}
```

(If `identity.PrincipalFromContext` and `hidden.ClaimFromContext` aren't exported with those exact names, use the existing convention — search for them in `internal/httpapi/search.go`'s handler implementations and copy the pattern.)

- [ ] **Step 4: Wire into `Deps` and `buildAPI`**

In `internal/httpapi/api.go`, add to imports:

```go
facetssvc "github.com/wesm/fotobank/internal/service/facets"
```

Add to `Deps` struct (after `Search`):

```go
	// Facets backs GET /api/v1/facets. Nil leaves the route
	// unregistered so the OpenAPI dumper can pass an empty Deps.
	Facets *facetssvc.Service
```

In `buildAPI`, add after the `registerSearchRoutes` line:

```go
	registerFacetsRoutes(api, deps.Facets)
```

- [ ] **Step 5: Run tests to verify pass**

```
go test -tags sqlite_fts5 ./internal/httpapi/ -run TestFacetsRoute -v
```

Expected: PASS.

- [ ] **Step 6: Run all httpapi tests**

```
go test -tags sqlite_fts5 ./internal/httpapi/ -v
```

Expected: existing routes still pass. The prek hook will regenerate the OpenAPI on commit.

- [ ] **Step 7: Commit**

```bash
git add internal/httpapi/facets.go internal/httpapi/facets_test.go internal/httpapi/api.go
git commit -m "$(cat <<'EOF'
feat(httpapi): GET /api/v1/facets route

Bound query-string surface mirrors /search's filter context (camera,
lens, facet_tag, tag, has_gps, media_type, date_after/before, location,
include_hidden) — but NOT q (per spec, /facets is filter-only). Hidden
gate requires a valid unlock claim. OpenAPI regenerates via prek hook.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 7 — Wire `*facets.Service` into the daemon

**Files:**
- Modify: `internal/cli/server.go`

The daemon constructs the various services and passes them through `httpapi.Deps`. Add `*facets.Service` to that wiring.

- [ ] **Step 1: Locate the wiring**

```
grep -n 'searchsvc.New\|searchsvc\.Service\|httpapi.Deps{' internal/cli/server.go
```

You're looking for the block where `httpapi.Deps` is constructed and where other services like `searchsvc` are built.

- [ ] **Step 2: Add the facets service**

Add an import:

```go
facetssvc "github.com/wesm/fotobank/internal/service/facets"
```

Construct the service alongside the others. Per SF-5 (commit 2c728e2) the constructor signature is `facetssvc.New(ro *sql.DB, hiddenChecker HiddenChecker) *Service` — it takes the read-only `*sql.DB` and a `HiddenChecker`. The project's `*hidden.Service` does NOT implement `HiddenChecker` directly (no `Valid` method on that exact name — search the codebase for the existing adapter pattern; SF-6 used a route-test-local fake, but production wiring needs the adapter shim that mirrors how `searchsvc` consumes the same checker, see `internal/cli/server.go` for the existing precedent). Reuse the existing adapter if one exists; otherwise add a tiny one alongside the search service's wiring:

```go
facetsService := facetssvc.New(roDB, hiddenCheckAdapter{hiddenAuth})
```

Set `Facets: facetsService` in the `httpapi.Deps{...}` literal.

- [ ] **Step 3: Verify build**

```
make build
```

Expected: clean build.

- [ ] **Step 4: Run integration smoke test if one exists**

```
go test -tags sqlite_fts5 ./internal/cli/ -v -run Server
```

If no such test exists, skip — Task 6's route test already covers handler wiring; Task 7 is only the `cmd/fotobank` plumbing.

- [ ] **Step 5: Commit**

```bash
git add internal/cli/server.go
git commit -m "$(cat <<'EOF'
feat(cli): wire FacetService into daemon

Constructs facetssvc.New(roDB) and passes it on httpapi.Deps so the
/api/v1/facets route is registered at boot.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 8 — Extend `RouteMatch` with filter params

**Files:**
- Modify: `frontend/src/lib/router/router.svelte.ts`
- Modify: `frontend/src/lib/router/router.test.ts` (or wherever the router parse tests live; if absent, add a small test file)

The discriminated union `RouteMatch` gains new optional fields on /library, /map, /search. Build callbacks parse them via `URLSearchParams.getAll()` for multi-value, and `parseBoolParam`/`get` for scalars.

- [ ] **Step 1: Locate router parse tests**

```
grep -rn 'RouteMatch\|parseRoute\|matchRoute' frontend/src/lib/router/ | head
```

If a test file exists, append; if not, create `frontend/src/lib/router/router.test.ts`.

- [ ] **Step 2: Write failing tests**

```ts
// In frontend/src/lib/router/router.test.ts (create if absent).
import { describe, it, expect } from "vitest";
import { matchRoute } from "./router.svelte"; // adjust import to whatever the file exports

describe("router filter params", () => {
  it("/library parses camera, lens, facet_tag, has_gps, media_type", () => {
    history.pushState({}, "", "/library?camera=Sony+A7R+IV&camera=iPhone+15+Pro&lens=FE+24-70mm+F2.8+GM&facet_tag=dog&has_gps=1&media_type=photo");
    const r = matchRoute(window.location.pathname);
    expect(r).toEqual({
      route: "library",
      camera: ["Sony A7R IV", "iPhone 15 Pro"],
      lens: ["FE 24-70mm F2.8 GM"],
      facet_tag: ["dog"],
      has_gps: true,
      media_type: "photo",
    });
  });

  it("/library with no params is bare", () => {
    history.pushState({}, "", "/library");
    expect(matchRoute(window.location.pathname)).toEqual({ route: "library" });
  });

  it("/map parses camera + lens but never has_gps", () => {
    history.pushState({}, "", "/map?camera=Sony+A7R+IV&has_gps=1");
    const r = matchRoute(window.location.pathname);
    expect(r).toMatchObject({
      route: "map",
      camera: ["Sony A7R IV"],
    });
    expect(r).not.toHaveProperty("has_gps");
  });

  it("/search merges new facet params alongside existing ones", () => {
    history.pushState({}, "", "/search?q=mountain&tag=Dog&camera=Sony+A7R+IV&facet_tag=cat&has_gps=0");
    const r = matchRoute(window.location.pathname);
    expect(r).toMatchObject({
      route: "search",
      q: "mountain",
      tag: ["Dog"],
      camera: ["Sony A7R IV"],
      facet_tag: ["cat"],
      has_gps: false,
    });
  });
});
```

- [ ] **Step 3: Run tests to verify failure**

```
cd frontend && bun run test -- router.test
```

Expected: FAIL (the route shape rejects the new properties or doesn't parse them).

- [ ] **Step 4: Extend the discriminated union**

In `frontend/src/lib/router/router.svelte.ts`, replace the relevant variants of `RouteMatch`:

```ts
  | {
      route: "library";
      camera?: string[];
      lens?: string[];
      facet_tag?: string[];
      has_gps?: boolean;
      media_type?: "photo" | "video";
    }
  | {
      route: "map";
      z?: number;
      c?: [number, number];
      focus?: string;
      tab?: "map" | "photos";
      camera?: string[];
      lens?: string[];
      facet_tag?: string[];
      media_type?: "photo" | "video";
      // No has_gps on /map (forced true).
    }
  | {
      route: "search";
      q?: string;
      sort?: SearchSort;
      date_after?: string;
      date_before?: string;
      tag?: string[];
      location?: string;
      media_type?: "photo" | "video";
      include_hidden?: boolean;
      camera?: string[];
      lens?: string[];
      facet_tag?: string[];
      has_gps?: boolean;
    }
```

- [ ] **Step 5: Update build callbacks**

Add a small parser helper at the top of the file (private, used by all three routes):

```ts
function parseFilterParams(sp: URLSearchParams): {
  camera?: string[];
  lens?: string[];
  facet_tag?: string[];
  media_type?: "photo" | "video";
} {
  const out: {
    camera?: string[]; lens?: string[]; facet_tag?: string[];
    media_type?: "photo" | "video";
  } = {};
  const camera = sp.getAll("camera");
  if (camera.length > 0) out.camera = camera;
  const lens = sp.getAll("lens");
  if (lens.length > 0) out.lens = lens;
  const facet_tag = sp.getAll("facet_tag");
  if (facet_tag.length > 0) out.facet_tag = facet_tag;
  const mt = sp.get("media_type");
  if (mt === "photo" || mt === "video") out.media_type = mt;
  return out;
}

function parseHasGps(sp: URLSearchParams): boolean | undefined {
  const v = sp.get("has_gps");
  if (v === "1") return true;
  if (v === "0") return false;
  return undefined;
}
```

Replace the `/library` build callback:

```ts
  { re: /^\/$/,           build: () => buildLibraryRoute() },
  { re: /^\/library$/,    build: () => buildLibraryRoute() },
```

Add `buildLibraryRoute` near the other helpers:

```ts
function buildLibraryRoute(): RouteMatch {
  const sp = new URLSearchParams(window.location.search);
  const filters = parseFilterParams(sp);
  const hasGps = parseHasGps(sp);
  return {
    route: "library" as const,
    ...filters,
    ...(hasGps !== undefined ? { has_gps: hasGps } : {}),
  };
}
```

Update the `/map` build callback to merge in `parseFilterParams` (but NOT `parseHasGps`); update the `/search` build callback to merge in both `parseFilterParams` and `parseHasGps` (alongside its existing parsing).

(The exact structure of the `/map` and `/search` build callbacks is in the file — apply the same `...filters` spread pattern to merge.)

- [ ] **Step 6: Run tests to verify pass**

```
cd frontend && bun run test -- router.test
```

Expected: PASS.

- [ ] **Step 7: Run type-check**

```
cd frontend && bun run check
```

Expected: clean (or only pre-existing warnings).

- [ ] **Step 8: Commit**

```bash
git add frontend/src/lib/router/router.svelte.ts frontend/src/lib/router/router.test.ts
git commit -m "$(cat <<'EOF'
feat(router): parse facet filter params on /library, /map, /search

Multi-value via repeat (URLSearchParams.getAll). has_gps is "1"/"0"
boolean. /map intentionally omits has_gps (route is geotagged-only).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 9 — `activeFilters.ts` helper

**Files:**
- Create: `frontend/src/lib/filters/activeFilters.ts`
- Create: `frontend/src/lib/filters/activeFilters.test.ts`

Helper that converts between `RouteMatch` and a normalized `ActiveFilters` struct, plus URL-merging.

- [ ] **Step 1: Write failing tests**

Create `frontend/src/lib/filters/activeFilters.test.ts`:

```ts
import { describe, it, expect } from "vitest";
import {
  fromRoute,
  withToggled,
  withFilters,
  filterKey,
  isEmpty,
  type ActiveFilters,
} from "./activeFilters";

describe("activeFilters.fromRoute", () => {
  it("library with no filters → empty", () => {
    expect(fromRoute({ route: "library" })).toEqual({
      cameras: [], lenses: [], tagKeys: [], hasGps: null, mediaType: null,
    });
  });

  it("library with all five facets", () => {
    expect(fromRoute({
      route: "library",
      camera: ["Sony A7R IV"],
      lens: ["FE 24-70mm F2.8 GM"],
      facet_tag: ["dog"],
      has_gps: true,
      media_type: "photo",
    })).toEqual({
      cameras: ["Sony A7R IV"],
      lenses: ["FE 24-70mm F2.8 GM"],
      tagKeys: ["dog"],
      hasGps: true,
      mediaType: "photo",
    });
  });

  it("non-filter routes return empty", () => {
    expect(fromRoute({ route: "albums" })).toEqual({
      cameras: [], lenses: [], tagKeys: [], hasGps: null, mediaType: null,
    });
  });
});

describe("activeFilters.withToggled", () => {
  const empty: ActiveFilters = {
    cameras: [], lenses: [], tagKeys: [], hasGps: null, mediaType: null,
  };

  it("adds a camera when not present", () => {
    expect(withToggled(empty, "cameras", "Sony A7R IV").cameras).toEqual(["Sony A7R IV"]);
  });

  it("removes a camera when already present", () => {
    const f = { ...empty, cameras: ["Sony A7R IV", "Canon EOS R5"] };
    expect(withToggled(f, "cameras", "Sony A7R IV").cameras).toEqual(["Canon EOS R5"]);
  });

  it("hasGps toggles tri-state nil → true → null", () => {
    const a = withToggled(empty, "hasGps", "true");
    expect(a.hasGps).toBe(true);
    const b = withToggled(a, "hasGps", "true");
    expect(b.hasGps).toBe(null);
    const c = withToggled(empty, "hasGps", "false");
    expect(c.hasGps).toBe(false);
  });

  it("mediaType toggles between photo, video, null", () => {
    const a = withToggled(empty, "mediaType", "photo");
    expect(a.mediaType).toBe("photo");
    const b = withToggled(a, "mediaType", "photo");
    expect(b.mediaType).toBe(null);
  });
});

describe("activeFilters.withFilters", () => {
  it("preserves non-filter params", () => {
    const cur = new URLSearchParams("q=mountain&sort=score&date_after=2024-01-01");
    const f: ActiveFilters = {
      cameras: ["Sony A7R IV"], lenses: [], tagKeys: ["dog"],
      hasGps: true, mediaType: null,
    };
    const sp = withFilters(cur, f);
    expect(sp.get("q")).toBe("mountain");
    expect(sp.get("sort")).toBe("score");
    expect(sp.get("date_after")).toBe("2024-01-01");
    expect(sp.getAll("camera")).toEqual(["Sony A7R IV"]);
    expect(sp.getAll("facet_tag")).toEqual(["dog"]);
    expect(sp.get("has_gps")).toBe("1");
  });

  it("strips filter params not present in new filters", () => {
    const cur = new URLSearchParams("camera=Old&lens=Stale");
    const sp = withFilters(cur, {
      cameras: [], lenses: [], tagKeys: [], hasGps: null, mediaType: null,
    });
    expect(sp.toString()).toBe("");
  });

  it("encodes hasGps=false as has_gps=0", () => {
    const sp = withFilters(new URLSearchParams(), {
      cameras: [], lenses: [], tagKeys: [], hasGps: false, mediaType: null,
    });
    expect(sp.get("has_gps")).toBe("0");
  });
});

describe("activeFilters.filterKey", () => {
  it("two equivalent filter sets produce the same key", () => {
    const a: ActiveFilters = {
      cameras: ["Sony", "Canon"], lenses: [], tagKeys: [], hasGps: null, mediaType: null,
    };
    const b: ActiveFilters = {
      cameras: ["Canon", "Sony"], lenses: [], tagKeys: [], hasGps: null, mediaType: null,
    };
    expect(filterKey(a)).toBe(filterKey(b));
  });
  it("different filter sets produce different keys", () => {
    const a: ActiveFilters = {
      cameras: ["Sony"], lenses: [], tagKeys: [], hasGps: null, mediaType: null,
    };
    const b: ActiveFilters = {
      cameras: ["Canon"], lenses: [], tagKeys: [], hasGps: null, mediaType: null,
    };
    expect(filterKey(a)).not.toBe(filterKey(b));
  });
});

describe("activeFilters.isEmpty", () => {
  it("empty filters", () => {
    expect(isEmpty({ cameras: [], lenses: [], tagKeys: [], hasGps: null, mediaType: null })).toBe(true);
  });
  it("non-empty cameras", () => {
    expect(isEmpty({ cameras: ["Sony"], lenses: [], tagKeys: [], hasGps: null, mediaType: null })).toBe(false);
  });
  it("hasGps=false counts as active", () => {
    expect(isEmpty({ cameras: [], lenses: [], tagKeys: [], hasGps: false, mediaType: null })).toBe(false);
  });
});
```

- [ ] **Step 2: Run tests to verify failure**

```
cd frontend && bun run test -- activeFilters.test
```

Expected: FAIL ("Cannot find module './activeFilters'").

- [ ] **Step 3: Implement the helper**

Create `frontend/src/lib/filters/activeFilters.ts`:

```ts
import type { RouteMatch } from "../router/router.svelte";

export type FacetGroup = "cameras" | "lenses" | "tagKeys" | "hasGps" | "mediaType";

export type ActiveFilters = {
  cameras: string[];
  lenses: string[];
  tagKeys: string[];          // sourced from facet_tag URL param (NOT tag)
  hasGps: boolean | null;     // null = no filter applied
  mediaType: "photo" | "video" | null;
};

const EMPTY: ActiveFilters = {
  cameras: [], lenses: [], tagKeys: [], hasGps: null, mediaType: null,
};

const FILTER_PARAM_KEYS = ["camera", "lens", "facet_tag", "has_gps", "media_type"];

/** Extract the active filter set from the route. Non-filter routes return EMPTY. */
export function fromRoute(r: RouteMatch): ActiveFilters {
  if (r.route !== "library" && r.route !== "search" && r.route !== "map") {
    return { ...EMPTY };
  }
  const f: ActiveFilters = { ...EMPTY };
  if ("camera" in r && r.camera) f.cameras = [...r.camera];
  if ("lens" in r && r.lens) f.lenses = [...r.lens];
  if ("facet_tag" in r && r.facet_tag) f.tagKeys = [...r.facet_tag];
  if ("has_gps" in r && r.has_gps !== undefined) f.hasGps = r.has_gps;
  if ("media_type" in r && r.media_type) f.mediaType = r.media_type;
  return f;
}

/**
 * Toggle a single value within a facet group. For multi-select groups
 * (cameras/lenses/tagKeys) adds/removes the value. For single-select
 * groups (hasGps/mediaType) toggles between the value and null.
 */
export function withToggled(f: ActiveFilters, group: FacetGroup, value: string): ActiveFilters {
  switch (group) {
    case "cameras":
    case "lenses":
    case "tagKeys": {
      const cur = f[group];
      const next = cur.includes(value) ? cur.filter((v) => v !== value) : [...cur, value];
      return { ...f, [group]: next };
    }
    case "hasGps": {
      const desired = value === "true" ? true : false;
      return { ...f, hasGps: f.hasGps === desired ? null : desired };
    }
    case "mediaType": {
      if (value !== "photo" && value !== "video") return f;
      return { ...f, mediaType: f.mediaType === value ? null : value };
    }
  }
}

/**
 * Merge `f` into `current`, preserving non-filter params (q, sort,
 * date_after, etc.). Strips the known filter param keys before
 * re-applying so toggling off a filter does the right thing.
 */
export function withFilters(current: URLSearchParams, f: ActiveFilters): URLSearchParams {
  const sp = new URLSearchParams(current);
  for (const k of FILTER_PARAM_KEYS) sp.delete(k);
  for (const v of f.cameras) sp.append("camera", v);
  for (const v of f.lenses) sp.append("lens", v);
  for (const v of f.tagKeys) sp.append("facet_tag", v);
  if (f.hasGps !== null) sp.set("has_gps", f.hasGps ? "1" : "0");
  if (f.mediaType !== null) sp.set("media_type", f.mediaType);
  return sp;
}

/** Stable key for caching: sort multi-value lists deterministically. */
export function filterKey(f: ActiveFilters): string {
  const norm = {
    cameras: [...f.cameras].sort(),
    lenses: [...f.lenses].sort(),
    tagKeys: [...f.tagKeys].sort(),
    hasGps: f.hasGps,
    mediaType: f.mediaType,
  };
  return JSON.stringify(norm);
}

export function isEmpty(f: ActiveFilters): boolean {
  return (
    f.cameras.length === 0 &&
    f.lenses.length === 0 &&
    f.tagKeys.length === 0 &&
    f.hasGps === null &&
    f.mediaType === null
  );
}
```

- [ ] **Step 4: Run tests to verify pass**

```
cd frontend && bun run test -- activeFilters.test
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/lib/filters/activeFilters.ts frontend/src/lib/filters/activeFilters.test.ts
git commit -m "$(cat <<'EOF'
feat(filters): activeFilters helper (route ↔ struct ↔ URL)

Single source of truth conversion helpers — fromRoute, withToggled,
withFilters (merging into existing URLSearchParams to preserve
q/sort/date_after etc.), filterKey for cache stability, isEmpty.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 10 — `facetsStore.svelte.ts` (fetch + cache + debounce)

**Files:**
- Create: `frontend/src/lib/filters/facetsStore.svelte.ts`
- Create: `frontend/src/lib/filters/facetsStore.test.ts`

Holds the latest /facets response keyed by `(route, ActiveFilters)`. Debounces fetches (100ms) and discards stale responses via a fetch token.

- [ ] **Step 1: Write failing test**

Create `frontend/src/lib/filters/facetsStore.test.ts`:

```ts
import { describe, it, expect, vi } from "vitest";
import { FacetsStore, type FacetsResponse } from "./facetsStore.svelte";
import type { ActiveFilters } from "./activeFilters";

const empty: ActiveFilters = {
  cameras: [], lenses: [], tagKeys: [], hasGps: null, mediaType: null,
};

const fakeResponse: FacetsResponse = {
  cameras: [{ value: "Sony A7R IV", count: 5 }],
  lenses: [], tags: [], places: { with_gps: 0, without_gps: 0 }, media_types: [],
};

function fakeClient(seq: FacetsResponse[]) {
  const calls: string[] = [];
  let i = 0;
  const GET = vi.fn(async (path: string) => {
    calls.push(path);
    return { data: seq[Math.min(i++, seq.length - 1)] };
  });
  return { client: { GET }, calls };
}

describe("FacetsStore", () => {
  it("fetches and stores response for a filter set", async () => {
    const { client } = fakeClient([fakeResponse]);
    const s = new FacetsStore(client as never, /*debounceMs=*/ 0);
    await s.fetch("library", empty);
    expect(s.response).toEqual(fakeResponse);
  });

  it("caches by (route + filters); same key skips refetch", async () => {
    const { client, calls } = fakeClient([fakeResponse]);
    const s = new FacetsStore(client as never, 0);
    await s.fetch("library", empty);
    await s.fetch("library", empty);
    expect(calls.length).toBe(1);
  });

  it("different routes use different cache slots", async () => {
    const { client, calls } = fakeClient([fakeResponse, fakeResponse]);
    const s = new FacetsStore(client as never, 0);
    await s.fetch("library", empty);
    await s.fetch("search", empty);
    expect(calls.length).toBe(2);
  });

  it("rapid filter changes coalesce via debounce", async () => {
    const { client, calls } = fakeClient([fakeResponse, fakeResponse]);
    const s = new FacetsStore(client as never, 50);
    s.fetch("library", empty);
    s.fetch("library", { ...empty, cameras: ["Sony A7R IV"] });
    s.fetch("library", { ...empty, cameras: ["Canon EOS R5"] });
    await new Promise((r) => setTimeout(r, 100));
    // Only the final request actually fires.
    expect(calls.length).toBe(1);
    expect(calls[0]).toContain("camera=Canon");
  });

  it("stale responses are dropped", async () => {
    let resolveFirst: (v: FacetsResponse) => void = () => {};
    const slow = new Promise<FacetsResponse>((r) => (resolveFirst = r));
    const fast = Promise.resolve(fakeResponse);
    let i = 0;
    const GET = vi.fn(async () => ({ data: i++ === 0 ? await slow : await fast }));
    const s = new FacetsStore({ GET } as never, 0);

    const p1 = s.fetch("library", empty);
    const p2 = s.fetch("library", { ...empty, cameras: ["Sony A7R IV"] });
    await p2;
    // Now resolve the first (stale) request — must NOT overwrite store.
    resolveFirst({ ...fakeResponse, cameras: [{ value: "STALE", count: 1 }] });
    await p1;
    expect(s.response?.cameras[0]?.value).not.toBe("STALE");
  });
});
```

- [ ] **Step 2: Run tests to verify failure**

```
cd frontend && bun run test -- facetsStore.test
```

Expected: FAIL.

- [ ] **Step 3: Implement the store**

Create `frontend/src/lib/filters/facetsStore.svelte.ts`:

```ts
import type { Client } from "../api/client";
import { filterKey, type ActiveFilters } from "./activeFilters";

export type FacetValue = { value: string; count: number };
export type FacetTag = { key: string; label: string; count: number };
export type FacetPlaces = { with_gps: number; without_gps: number };

export type FacetsResponse = {
  cameras: FacetValue[];
  lenses: FacetValue[];
  tags: FacetTag[];
  places: FacetPlaces;
  media_types: FacetValue[];
};

export type RouteContext = "library" | "search" | "map";

export class FacetsStore {
  response = $state<FacetsResponse | null>(null);
  loading = $state(false);
  error = $state<string | null>(null);

  private cache = new Map<string, FacetsResponse>();
  private fetchToken = 0;
  private debounceTimer: ReturnType<typeof setTimeout> | null = null;
  // pendingResolvers accumulates the promise resolvers from every
  // debounced caller while a timer is in flight. When the timer fires
  // and the fetch settles, we resolve them all together so each
  // caller's `await fetch(...)` continues only after the latest state
  // is in `this.response`. A single pendingResolve scalar would either
  // leak earlier callers (their resolver gets overwritten) or — if we
  // resolve them at clearTimeout — let them continue while state is
  // still stale.
  private pendingResolvers: Array<() => void> = [];

  constructor(
    private client: Pick<Client, "GET">,
    private debounceMs = 100,
  ) {}

  /**
   * Fetch facets for the given route + filters. Identical key → cached.
   * Multiple rapid calls coalesce into the latest one (debounced).
   * Stale responses are dropped via fetchToken.
   */
  fetch(route: RouteContext, filters: ActiveFilters): Promise<void> {
    const key = `${route}|${filterKey(filters)}`;
    const cached = this.cache.get(key);
    if (cached) {
      // Cache hit invalidates any in-flight fetch: bump fetchToken so
      // a still-running GET cannot overwrite this.response when its
      // promise settles, drain any queued resolvers, return resolved.
      ++this.fetchToken;
      if (this.debounceTimer !== null) clearTimeout(this.debounceTimer);
      this.response = cached;
      const resolvers = this.pendingResolvers;
      this.pendingResolvers = [];
      for (const r of resolvers) r();
      return Promise.resolve();
    }

    // Rapid calls coalesce: clear the prior debounce timer and bump
    // fetchToken SYNCHRONOUSLY so any in-flight callback from an
    // earlier timer immediately becomes stale and short-circuits
    // every post-await check. Without the synchronous bump, an
    // earlier fetch could resolve and drain pendingResolvers —
    // including this newer caller's resolver — with stale state still
    // in this.response. (Bumping inside the timer callback closes the
    // window from timer-fire onward but leaves a window between the
    // previous timer firing and this new schedule arriving.)
    if (this.debounceTimer !== null) clearTimeout(this.debounceTimer);
    const myToken = ++this.fetchToken;

    return new Promise<void>((resolve) => {
      this.pendingResolvers.push(resolve);
      this.debounceTimer = setTimeout(async () => {
        // Defensive: clearTimeout in a later fetch should have
        // prevented this callback from running, but re-check before
        // any work in case the timer fires anyway.
        if (myToken !== this.fetchToken) return;
        this.loading = true;
        this.error = null;
        try {
          const path = this.buildPath(route, filters);
          const res = await this.client.GET(path as never);
          if (myToken !== this.fetchToken) return; // stale, drop
          // openapi-fetch surfaces non-2xx responses on res.error and
          // leaves res.data undefined. Without reading res.error a
          // server-side 400/5xx silently leaves stale this.response
          // visible. Map huma's {title, detail} (or thrown JS Error
          // {message}) to a string and bail before writing the cache.
          const r = res as {
            data?: FacetsResponse;
            error?: { detail?: string; title?: string; message?: string };
          };
          if (r.error) {
            const e = r.error;
            this.error = e.detail ?? e.message ?? e.title ?? "Failed to load facets";
          } else if (r.data) {
            this.cache.set(key, r.data);
            this.response = r.data;
          }
        } catch (e) {
          if (myToken !== this.fetchToken) return;
          this.error = e instanceof Error ? e.message : String(e);
        } finally {
          if (myToken === this.fetchToken) {
            this.loading = false;
            // Settle every resolver queued while this token was the
            // latest. A newer fetch would have bumped fetchToken
            // synchronously, leaving this branch unreachable — its
            // own settle path will drain the resolvers (including
            // any pushed during our await) when it lands.
            const resolvers = this.pendingResolvers;
            this.pendingResolvers = [];
            for (const r of resolvers) r();
          }
        }
      }, this.debounceMs);
    });
  }

  private buildPath(route: RouteContext, f: ActiveFilters): string {
    const sp = new URLSearchParams();
    for (const v of f.cameras) sp.append("camera", v);
    for (const v of f.lenses) sp.append("lens", v);
    for (const v of f.tagKeys) sp.append("facet_tag", v);
    // /facets accepts has_gps as the literal strings "true"/"false"
    // (huma `enum:"true,false"` constraint, see internal/httpapi/facets.go).
    // The URL convention used elsewhere in the SPA is "1"/"0"; keep
    // the mapping local rather than spreading two formats.
    if (f.hasGps !== null) sp.set("has_gps", f.hasGps ? "true" : "false");
    if (f.mediaType !== null) sp.set("media_type", f.mediaType);
    // /map's facet counts must scope to geotagged photos — the user
    // can't pin a non-geotagged photo on the map. Force has_gps=true
    // when the caller hasn't pinned it. (The Places facet is hidden
    // on /map per SF-14, so the user has no way to toggle this.)
    if (route === "map" && f.hasGps === null) {
      sp.set("has_gps", "true");
    }
    const qs = sp.toString();
    return qs ? `/api/v1/facets?${qs}` : `/api/v1/facets`;
  }
}
```

(If the project uses an `openapi-fetch`-style typed client, the `as never` cast is a fallback because `/api/v1/facets` may not yet be in the generated types when this task runs. After the OpenAPI regen committed in Task 6 lands, swap the cast for a properly-typed call. Note in a TODO comment if this is the case.)

- [ ] **Step 4: Run tests to verify pass**

```
cd frontend && bun run test -- facetsStore.test
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/lib/filters/facetsStore.svelte.ts frontend/src/lib/filters/facetsStore.test.ts
git commit -m "$(cat <<'EOF'
feat(filters): facetsStore — cached, debounced, stale-resilient

Caches by (route + filterKey). 100ms debounce coalesces rapid filter
changes. Fetch token discards stale responses (e.g. slow first request
arriving after a faster second one). Single response shape per fetch.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 11 — `mediaStore` filter-change reset protocol

**Files:**
- Modify: `frontend/src/lib/media/mediaStore.svelte.ts`
- Modify: `frontend/src/lib/media/mediaStore.test.ts`

Without this, /library can interleave responses from a prior filter set with the new one. The store accepts a `filters` param via the constructor or via a setter; it computes `filterKey`; on key change it resets pagination, clears `byMonth` / `byId` / `byMediaId`, increments a `fetchToken`, and discards late responses.

- [ ] **Step 1: Write failing test**

Add to `frontend/src/lib/media/mediaStore.test.ts`:

```ts
// Mocked Client.GET that returns different rows depending on the
// camera filter passed in the URL.
function clientReturning(rowsByCamera: Record<string, unknown[]>) {
  return {
    GET: vi.fn(async (_path: string, opts: { params?: { query?: { camera?: string[] } } }) => {
      const cam = opts.params?.query?.camera?.[0] ?? "*";
      return { data: { months: [{ key: "2025-01", items: rowsByCamera[cam] ?? [] }] } };
    }),
  };
}

describe("MediaStore filter-change reset protocol", () => {
  it("clearing months on filter change", async () => {
    const client = clientReturning({
      "*": [{ id: "any-1", timestamp: "2025-01-01T00:00:00Z", aspect: 1.5,
              thumbUrl: "/x", thumbStatus: "ready", thumbVersion: 1 }],
      "Sony A7R IV": [{ id: "sony-1", timestamp: "2025-01-01T00:00:00Z", aspect: 1.5,
                        thumbUrl: "/y", thumbStatus: "ready", thumbVersion: 1 }],
    });
    const store = new MediaStore(client as never);
    await store.loadInitial();
    expect(store.get("any-1")).toBeDefined();

    store.setFilters({ cameras: ["Sony A7R IV"], lenses: [], tagKeys: [], hasGps: null, mediaType: null });
    await store.loadInitial();

    expect(store.get("any-1")).toBeUndefined(); // cleared
    expect(store.get("sony-1")).toBeDefined();
  });

  it("late stale response is dropped", async () => {
    let resolveStale: (v: unknown) => void = () => {};
    const stale = new Promise((r) => (resolveStale = r));
    const fast = Promise.resolve({
      data: { months: [{ key: "2025-01",
        items: [{ id: "fast-1", timestamp: "2025-01-01T00:00:00Z", aspect: 1.5,
                  thumbUrl: "/f", thumbStatus: "ready", thumbVersion: 1 }] }] },
    });
    let call = 0;
    const client = {
      GET: vi.fn(async () => (call++ === 0 ? await stale : await fast)),
    };
    const store = new MediaStore(client as never);

    const p1 = store.loadInitial();
    store.setFilters({ cameras: ["X"], lenses: [], tagKeys: [], hasGps: null, mediaType: null });
    const p2 = store.loadInitial();
    await p2;
    resolveStale({
      data: { months: [{ key: "2025-01",
        items: [{ id: "STALE-1", timestamp: "2025-01-01T00:00:00Z", aspect: 1.5,
                  thumbUrl: "/s", thumbStatus: "ready", thumbVersion: 1 }] }] },
    });
    await p1;
    expect(store.get("STALE-1")).toBeUndefined();
    expect(store.get("fast-1")).toBeDefined();
  });
});
```

- [ ] **Step 2: Run tests to verify failure**

```
cd frontend && bun run test -- mediaStore.test
```

Expected: FAIL ("setFilters is not a function").

- [ ] **Step 3: Add the reset protocol to `MediaStore`**

In `frontend/src/lib/media/mediaStore.svelte.ts`, add an import:

```ts
import { filterKey, type ActiveFilters } from "../filters/activeFilters";
```

Add fields and methods to the class (after `private inflight`):

```ts
  private filters: ActiveFilters = {
    cameras: [], lenses: [], tagKeys: [], hasGps: null, mediaType: null,
  };
  private currentFilterKey = filterKey(this.filters);
  private fetchToken = 0;

  /**
   * Update the active filters. If the filterKey changes, the store
   * resets pagination, clears all cached rows, and bumps fetchToken so
   * any in-flight responses against the previous filter are dropped.
   * Callers should re-call loadInitial() after setFilters.
   */
  setFilters(next: ActiveFilters): void {
    const nextKey = filterKey(next);
    if (nextKey === this.currentFilterKey) return;
    this.filters = next;
    this.currentFilterKey = nextKey;
    this.fetchToken++;
    this.byMonth.clear();
    this.byId.clear();
    this.byMediaId.clear();
    this.months = [];
    this.exhausted = false;
    this.nextOffset = 0;
    this.inflight = null;
  }
```

Modify `loadMore()` so the in-flight fetch captures `this.fetchToken` at issue time and rejects late responses:

```ts
  loadMore(): Promise<void> {
    if (this.exhausted) return Promise.resolve();
    if (this.inflight !== null) return this.inflight;

    const myToken = this.fetchToken;
    this.loading = true;
    this.inflight = (async () => {
      try {
        // ... existing fetch body, EXCEPT: before writing to byMonth,
        // check (myToken === this.fetchToken). If not, return without
        // mutating any state.
        const response = await this.client.GET("/api/v1/media", /* params with this.filters */);
        if (myToken !== this.fetchToken) return; // stale; drop
        // existing merge logic...
      } finally {
        if (myToken === this.fetchToken) this.loading = false;
        this.inflight = null;
      }
    })();
    return this.inflight;
  }
```

(The existing `loadMore` body in the file does the actual fetch and merge — wrap it in the `myToken` check. Inspect the current body to see exactly where the merge happens; the stale-token check goes immediately before the merge and inside the `try`.)

Also, in the GET call, pass the filter params from `this.filters`. The /api/v1/media route may not yet accept these params; if `MediaService.List` already supports them server-side via Task 2, it does — confirm during impl.

- [ ] **Step 4: Run tests to verify pass**

```
cd frontend && bun run test -- mediaStore.test
```

Expected: PASS for new tests AND existing tests.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/lib/media/mediaStore.svelte.ts frontend/src/lib/media/mediaStore.test.ts
git commit -m "$(cat <<'EOF'
feat(media): mediaStore filter-change reset protocol

setFilters bumps a fetchToken, clears months/byMonth/byId, and resets
pagination. loadMore captures the token at issue time and discards late
responses — no interleaving of old + new filter results in the grid.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 12 — Port `FacetList.svelte` from agentsview

**Files:**
- Create: `frontend/src/lib/filters/FacetList.svelte`
- Create: `frontend/src/lib/filters/FacetList.test.ts`

Source: `~/code/agentsview/frontend/src/lib/components/usage/FilterDropdown.svelte`. Port the inline scroll + checkbox list (the popover wrapping is replaced — fotobank uses inline placement). Re-theme to fotobank's CSS tokens (`--surface`, `--surface-2`, `--ink`, `--ink-2`, `--ink-3`, `--ink-4`, `--border`, `--border-2`, `--amber`, `--amber-deep`, `--font-mono`, `--space-*`, `--text-*`).

- [ ] **Step 1: Write component test**

Create `frontend/src/lib/filters/FacetList.test.ts`:

```ts
import { describe, it, expect, vi } from "vitest";
import { render, fireEvent } from "@testing-library/svelte";
import FacetList from "./FacetList.svelte";

describe("FacetList", () => {
  const items = [
    { value: "Sony A7R IV", count: 845, selected: false },
    { value: "iPhone 15 Pro", count: 302, selected: true },
    { value: "Canon EOS R5", count: 100, selected: false },
  ];

  it("renders one row per item with count", () => {
    const { getByText } = render(FacetList, { items, onToggle: () => {} });
    expect(getByText("Sony A7R IV")).toBeTruthy();
    expect(getByText("845")).toBeTruthy();
  });

  it("emits toggle on row click", async () => {
    const onToggle = vi.fn();
    const { getByText } = render(FacetList, { items, onToggle });
    await fireEvent.click(getByText("Sony A7R IV"));
    expect(onToggle).toHaveBeenCalledWith("Sony A7R IV");
  });

  it("shows search input only when items > 8", () => {
    const big = Array.from({ length: 9 }, (_, i) => ({
      value: `V${i}`, count: i, selected: false,
    }));
    const { container, queryByPlaceholderText } = render(FacetList, {
      items, onToggle: () => {}, searchPlaceholder: "Search…",
    });
    expect(queryByPlaceholderText("Search…")).toBeNull();
    const big2 = render(FacetList, {
      items: big, onToggle: () => {}, searchPlaceholder: "Search…",
    });
    expect(big2.queryByPlaceholderText("Search…")).toBeTruthy();
  });

  it("substring search filters items", async () => {
    const big = Array.from({ length: 10 }, (_, i) => ({
      value: `Camera-${i}`, count: i, selected: false,
    }));
    const { getByPlaceholderText, queryByText } = render(FacetList, {
      items: big, onToggle: () => {}, searchPlaceholder: "Search…",
    });
    const input = getByPlaceholderText("Search…");
    await fireEvent.input(input, { target: { value: "ra-3" } });
    expect(queryByText("Camera-3")).toBeTruthy();
    expect(queryByText("Camera-2")).toBeNull();
  });
});
```

- [ ] **Step 2: Run test to verify failure**

```
cd frontend && bun run test -- FacetList.test
```

Expected: FAIL ("Cannot find module './FacetList.svelte'").

- [ ] **Step 3: Create the component**

Create `frontend/src/lib/filters/FacetList.svelte`:

```svelte
<script lang="ts">
  type Item = { value: string; count: number; selected: boolean; label?: string };

  let {
    items,
    onToggle,
    searchPlaceholder = "Search…",
    searchThreshold = 8,
  }: {
    items: Item[];
    onToggle: (value: string) => void;
    searchPlaceholder?: string;
    searchThreshold?: number;
  } = $props();

  let query = $state("");
  const showSearch = $derived(items.length > searchThreshold);
  const filtered = $derived.by((): Item[] => {
    const q = query.trim().toLowerCase();
    if (q === "") return items;
    return items.filter((it) =>
      (it.label ?? it.value).toLowerCase().includes(q),
    );
  });
</script>

<div class="facet-list">
  {#if showSearch}
    <input
      class="facet-search"
      type="text"
      placeholder={searchPlaceholder}
      bind:value={query}
    />
  {/if}
  <div class="rows">
    {#each filtered as it (it.value)}
      <button
        type="button"
        class="row"
        class:selected={it.selected}
        onclick={() => onToggle(it.value)}
      >
        <span class="check" class:on={it.selected}>
          {#if it.selected}
            <svg width="8" height="8" viewBox="0 0 8 8" aria-hidden="true">
              <path d="M1.5 4 L3.2 5.8 L6.5 2.2"
                fill="none" stroke="white" stroke-width="1.2"
                stroke-linecap="round" stroke-linejoin="round"/>
            </svg>
          {/if}
        </span>
        <span class="name">{it.label ?? it.value}</span>
        <span class="count">{it.count}</span>
      </button>
    {:else}
      <div class="empty">{query ? "No matches" : "No items"}</div>
    {/each}
  </div>
</div>

<style>
  .facet-list { display: flex; flex-direction: column; gap: var(--space-2); }
  .facet-search {
    width: 100%;
    padding: var(--space-2) var(--space-3);
    background: var(--surface-2);
    border: 1px solid var(--border);
    color: var(--ink);
    font-size: var(--text-sm);
    outline: none;
  }
  .facet-search:focus { border-color: var(--amber); }
  .facet-search::placeholder { color: var(--ink-4); }
  .rows {
    display: flex; flex-direction: column;
    max-height: 220px; overflow-y: auto;
    border: 1px solid var(--border);
  }
  .row {
    display: flex; align-items: center; gap: var(--space-2);
    width: 100%;
    padding: var(--space-2) var(--space-3);
    background: transparent; color: var(--ink-2);
    border: 0; border-bottom: 1px solid var(--border);
    text-align: left; cursor: pointer;
    font-size: var(--text-sm);
    transition: background 100ms, color 100ms;
  }
  .row:last-child { border-bottom: 0; }
  .row:hover { background: var(--surface-2); color: var(--ink); }
  .row.selected { color: var(--amber); }
  .check {
    width: 12px; height: 12px;
    border: 1px solid var(--border-2);
    flex-shrink: 0;
    display: flex; align-items: center; justify-content: center;
  }
  .check.on { background: var(--amber); border-color: var(--amber); }
  .name {
    flex: 1; min-width: 0;
    overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  }
  .count {
    flex-shrink: 0;
    font-family: var(--font-mono);
    font-variant-numeric: tabular-nums;
    font-size: var(--text-xs);
    color: var(--ink-4);
  }
  .empty {
    padding: var(--space-3);
    color: var(--ink-4); font-size: var(--text-sm);
    text-align: center;
  }
</style>
```

- [ ] **Step 4: Run tests to verify pass**

```
cd frontend && bun run test -- FacetList.test
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/lib/filters/FacetList.svelte frontend/src/lib/filters/FacetList.test.ts
git commit -m "$(cat <<'EOF'
feat(filters): FacetList primitive

Inline scroll + checkbox + (>8 items) substring search. Ported from
agentsview's FilterDropdown.svelte, restructured to render inline (no
popover wrapping) and re-themed to fotobank tokens.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 13 — `FacetSection.svelte` (collapsible wrapper)

**Files:**
- Create: `frontend/src/lib/filters/FacetSection.svelte`
- Create: `frontend/src/lib/filters/FacetSection.test.ts`

Collapsible header with chevron + label + total count + active-count badge (when collapsed and any selected). Wraps a `FacetList`. Persists collapse state in `localStorage`.

- [ ] **Step 1: Write component test**

Create `frontend/src/lib/filters/FacetSection.test.ts`:

```ts
import { describe, it, expect } from "vitest";
import { render, fireEvent } from "@testing-library/svelte";
import FacetSection from "./FacetSection.svelte";

describe("FacetSection", () => {
  const items = [
    { value: "Sony", count: 5, selected: true },
    { value: "Canon", count: 3, selected: false },
  ];

  it("renders the label and total count", () => {
    const { getByText } = render(FacetSection, {
      label: "Cameras", totalCount: 8, items, onToggle: () => {},
      storageKey: "facet:test:cameras", expandedDefault: true,
    });
    expect(getByText("Cameras")).toBeTruthy();
    expect(getByText("8")).toBeTruthy();
  });

  it("collapsed state hides the FacetList body", async () => {
    localStorage.setItem("facet:test:cameras", "collapsed");
    const { queryByText } = render(FacetSection, {
      label: "Cameras", totalCount: 8, items, onToggle: () => {},
      storageKey: "facet:test:cameras", expandedDefault: true,
    });
    expect(queryByText("Sony")).toBeNull();
  });

  it("active-count badge shows when collapsed and items selected", async () => {
    localStorage.setItem("facet:test:cameras", "collapsed");
    const { getByText } = render(FacetSection, {
      label: "Cameras", totalCount: 8, items, onToggle: () => {},
      storageKey: "facet:test:cameras", expandedDefault: true,
    });
    expect(getByText("1")).toBeTruthy(); // 1 selected
  });

  it("toggling header flips collapse state and persists", async () => {
    localStorage.removeItem("facet:test:cameras");
    const { getByRole } = render(FacetSection, {
      label: "Cameras", totalCount: 8, items, onToggle: () => {},
      storageKey: "facet:test:cameras", expandedDefault: true,
    });
    const header = getByRole("button", { name: /Cameras/ });
    await fireEvent.click(header);
    expect(localStorage.getItem("facet:test:cameras")).toBe("collapsed");
  });
});
```

- [ ] **Step 2: Run tests to verify failure**

```
cd frontend && bun run test -- FacetSection.test
```

Expected: FAIL.

- [ ] **Step 3: Create the component**

Create `frontend/src/lib/filters/FacetSection.svelte`:

```svelte
<script lang="ts">
  import FacetList from "./FacetList.svelte";

  type Item = { value: string; count: number; selected: boolean; label?: string };

  let {
    label,
    totalCount,
    items,
    onToggle,
    storageKey,
    expandedDefault = true,
    searchPlaceholder = "Search…",
  }: {
    label: string;
    totalCount: number;
    items: Item[];
    onToggle: (value: string) => void;
    storageKey: string;
    expandedDefault?: boolean;
    searchPlaceholder?: string;
  } = $props();

  function readInitial(): boolean {
    if (typeof localStorage === "undefined") return expandedDefault;
    const v = localStorage.getItem(storageKey);
    if (v === "expanded") return true;
    if (v === "collapsed") return false;
    return expandedDefault;
  }

  let expanded = $state(readInitial());
  const activeCount = $derived(items.filter((i) => i.selected).length);

  function toggle() {
    expanded = !expanded;
    if (typeof localStorage !== "undefined") {
      localStorage.setItem(storageKey, expanded ? "expanded" : "collapsed");
    }
  }
</script>

<div class="facet-section">
  <button
    type="button"
    class="header"
    aria-expanded={expanded}
    onclick={toggle}
  >
    <span class="chevron" class:expanded>▸</span>
    <span class="label">{label}</span>
    {#if !expanded && activeCount > 0}
      <span class="active-badge">{activeCount}</span>
    {/if}
    <span class="total">{totalCount}</span>
  </button>
  {#if expanded}
    <div class="body">
      <FacetList {items} {onToggle} {searchPlaceholder} />
    </div>
  {/if}
</div>

<style>
  .facet-section + .facet-section { margin-top: var(--space-3); }
  .header {
    display: flex; align-items: center; gap: var(--space-2);
    width: 100%;
    padding: var(--space-2) 0;
    background: transparent; border: 0;
    color: var(--ink-3);
    font-size: var(--text-xs); font-weight: 600;
    text-transform: uppercase;
    letter-spacing: var(--label-track);
    cursor: pointer;
    transition: color 100ms;
  }
  .header:hover { color: var(--ink); }
  .chevron {
    display: inline-block;
    width: 10px;
    transition: transform 120ms;
    color: var(--ink-4);
  }
  .chevron.expanded { transform: rotate(90deg); }
  .label { flex: 1; text-align: left; }
  .active-badge {
    background: var(--amber);
    color: var(--bg);
    font-family: var(--font-mono);
    font-variant-numeric: tabular-nums;
    font-size: 10px;
    padding: 0 5px;
    border-radius: 8px;
    min-width: 16px;
    text-align: center;
    line-height: 14px;
  }
  .total {
    font-family: var(--font-mono);
    font-variant-numeric: tabular-nums;
    font-size: 11px;
    color: var(--ink-4);
  }
  .body { margin-top: var(--space-2); }
</style>
```

- [ ] **Step 4: Run tests to verify pass**

```
cd frontend && bun run test -- FacetSection.test
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/lib/filters/FacetSection.svelte frontend/src/lib/filters/FacetSection.test.ts
git commit -m "$(cat <<'EOF'
feat(filters): FacetSection collapsible wrapper

Header with chevron + label + total count + (when collapsed)
active-count badge. Persists collapse state in localStorage so the
sidebar layout survives reloads.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 14 — `FilterSidebar.svelte`

**Files:**
- Create: `frontend/src/lib/filters/FilterSidebar.svelte`
- Create: `frontend/src/lib/filters/FilterSidebar.test.ts`

Composes five `FacetSection`s (Cameras, Lenses, Tags, Places, Media Types). Reads `ActiveFilters` from props (parent passes `fromRoute(router.current)`); writes by calling `router.navigate` with the URL produced by `withFilters`. Hides Places when the route is /map.

- [ ] **Step 1: Write component test**

Create `frontend/src/lib/filters/FilterSidebar.test.ts`:

```ts
import { describe, it, expect, vi } from "vitest";
import { render, fireEvent } from "@testing-library/svelte";
import FilterSidebar from "./FilterSidebar.svelte";
import type { FacetsResponse } from "./facetsStore.svelte";
import type { ActiveFilters } from "./activeFilters";

const empty: ActiveFilters = {
  cameras: [], lenses: [], tagKeys: [], hasGps: null, mediaType: null,
};
const fakeResp: FacetsResponse = {
  cameras: [{ value: "Sony A7R IV", count: 5 }],
  lenses: [{ value: "FE 24-70mm F2.8 GM", count: 3 }],
  tags: [{ key: "dog", label: "Dog", count: 2 }],
  places: { with_gps: 4, without_gps: 1 },
  media_types: [{ value: "photo", count: 8 }, { value: "video", count: 1 }],
};

describe("FilterSidebar", () => {
  it("renders all five sections on /library", () => {
    const { getByText } = render(FilterSidebar, {
      route: "library", filters: empty, response: fakeResp, onChange: () => {},
    });
    expect(getByText("CAMERAS")).toBeTruthy();
    expect(getByText("LENSES")).toBeTruthy();
    expect(getByText("TAGS")).toBeTruthy();
    expect(getByText("PLACES")).toBeTruthy();
    expect(getByText("MEDIA TYPE")).toBeTruthy();
  });

  it("hides PLACES on /map", () => {
    const { queryByText } = render(FilterSidebar, {
      route: "map", filters: empty, response: fakeResp, onChange: () => {},
    });
    expect(queryByText("PLACES")).toBeNull();
  });

  it("emits onChange with the toggled filter set", async () => {
    const onChange = vi.fn();
    const { getByText } = render(FilterSidebar, {
      route: "library", filters: empty, response: fakeResp, onChange,
    });
    await fireEvent.click(getByText("Sony A7R IV"));
    expect(onChange).toHaveBeenCalledWith(expect.objectContaining({
      cameras: ["Sony A7R IV"],
    }));
  });
});
```

- [ ] **Step 2: Run tests to verify failure**

```
cd frontend && bun run test -- FilterSidebar.test
```

Expected: FAIL.

- [ ] **Step 3: Create the component**

Create `frontend/src/lib/filters/FilterSidebar.svelte`:

```svelte
<script lang="ts">
  import FacetSection from "./FacetSection.svelte";
  import { withToggled, isEmpty, type ActiveFilters, type FacetGroup } from "./activeFilters";
  import type { FacetsResponse } from "./facetsStore.svelte";

  let {
    route,
    filters,
    response,
    onChange,
  }: {
    route: "library" | "search" | "map";
    filters: ActiveFilters;
    response: FacetsResponse | null;
    onChange: (next: ActiveFilters) => void;
  } = $props();

  const cameraItems = $derived(
    (response?.cameras ?? []).map((c) => ({
      value: c.value,
      count: c.count,
      selected: filters.cameras.includes(c.value),
    })),
  );
  const lensItems = $derived(
    (response?.lenses ?? []).map((l) => ({
      value: l.value,
      count: l.count,
      selected: filters.lenses.includes(l.value),
    })),
  );
  const tagItems = $derived(
    (response?.tags ?? []).map((t) => ({
      value: t.key,
      label: t.label,
      count: t.count,
      selected: filters.tagKeys.includes(t.key),
    })),
  );
  const placesItems = $derived(
    response
      ? [
          { value: "true", label: "Has GPS", count: response.places.with_gps,
            selected: filters.hasGps === true },
          { value: "false", label: "No GPS", count: response.places.without_gps,
            selected: filters.hasGps === false },
        ]
      : [],
  );
  const mediaTypeItems = $derived(
    (response?.media_types ?? []).map((m) => ({
      value: m.value,
      label: m.value === "photo" ? "Photo" : "Video",
      count: m.count,
      selected: filters.mediaType === m.value,
    })),
  );

  const cameraTotal = $derived(cameraItems.reduce((n, i) => n + i.count, 0));
  const lensTotal   = $derived(lensItems.reduce((n, i) => n + i.count, 0));
  const tagTotal    = $derived(tagItems.reduce((n, i) => n + i.count, 0));
  const placesTotal = $derived((response?.places.with_gps ?? 0) + (response?.places.without_gps ?? 0));
  const mtTotal     = $derived(mediaTypeItems.reduce((n, i) => n + i.count, 0));

  function toggle(group: FacetGroup, value: string) {
    onChange(withToggled(filters, group, value));
  }

  function clearAll() {
    onChange({ cameras: [], lenses: [], tagKeys: [], hasGps: null, mediaType: null });
  }
</script>

<div class="filter-sidebar">
  <div class="group-header">
    <span class="label">FILTERS</span>
    {#if !isEmpty(filters)}
      <button type="button" class="clear" onclick={clearAll}>Clear</button>
    {/if}
  </div>

  <FacetSection
    label="Cameras"
    totalCount={cameraTotal}
    items={cameraItems}
    onToggle={(v) => toggle("cameras", v)}
    storageKey="fotobank:facet:cameras"
    searchPlaceholder="Search cameras…"
  />
  <FacetSection
    label="Lenses"
    totalCount={lensTotal}
    items={lensItems}
    onToggle={(v) => toggle("lenses", v)}
    storageKey="fotobank:facet:lenses"
    searchPlaceholder="Search lenses…"
  />
  <FacetSection
    label="Tags"
    totalCount={tagTotal}
    items={tagItems}
    onToggle={(v) => toggle("tagKeys", v)}
    storageKey="fotobank:facet:tags"
    searchPlaceholder="Search tags…"
  />
  {#if route !== "map"}
    <FacetSection
      label="Places"
      totalCount={placesTotal}
      items={placesItems}
      onToggle={(v) => toggle("hasGps", v)}
      storageKey="fotobank:facet:places"
    />
  {/if}
  <FacetSection
    label="Media Type"
    totalCount={mtTotal}
    items={mediaTypeItems}
    onToggle={(v) => toggle("mediaType", v)}
    storageKey="fotobank:facet:mediatype"
  />
</div>

<style>
  .filter-sidebar {
    padding: 18px 20px;
    border-top: 1px solid var(--border);
  }
  .group-header {
    display: flex; align-items: center; justify-content: space-between;
    margin-bottom: var(--space-3);
  }
  .label {
    font-size: var(--text-xs); font-weight: 600;
    color: var(--ink-3);
    text-transform: uppercase;
    letter-spacing: var(--label-track);
  }
  .clear {
    background: transparent; border: 0;
    color: var(--amber);
    font-size: var(--text-xs);
    cursor: pointer;
  }
  .clear:hover { color: var(--amber-deep); }
</style>
```

- [ ] **Step 4: Run tests to verify pass**

```
cd frontend && bun run test -- FilterSidebar.test
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/lib/filters/FilterSidebar.svelte frontend/src/lib/filters/FilterSidebar.test.ts
git commit -m "$(cat <<'EOF'
feat(filters): FilterSidebar composing five FacetSections

Cameras, Lenses, Tags, Places, Media Type. Places hidden on /map (route
contract is geotagged-only). Group header carries a Clear link visible
only when any filter is active. Stateless — parent owns ActiveFilters.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 15 — `FilterChipStrip.svelte`

**Files:**
- Create: `frontend/src/lib/filters/FilterChipStrip.svelte`
- Create: `frontend/src/lib/filters/FilterChipStrip.test.ts`

Renders one chip per active filter value with × to remove. "Clear all" appears when ≥2 chips.

- [ ] **Step 1: Write component test**

Create `frontend/src/lib/filters/FilterChipStrip.test.ts`:

```ts
import { describe, it, expect, vi } from "vitest";
import { render, fireEvent } from "@testing-library/svelte";
import FilterChipStrip from "./FilterChipStrip.svelte";
import type { ActiveFilters } from "./activeFilters";

describe("FilterChipStrip", () => {
  const empty: ActiveFilters = {
    cameras: [], lenses: [], tagKeys: [], hasGps: null, mediaType: null,
  };

  it("renders nothing when no filters are active", () => {
    const { container } = render(FilterChipStrip, {
      filters: empty, tagLabels: {}, onChange: () => {},
    });
    expect(container.querySelector(".chip")).toBeNull();
  });

  it("renders a chip per camera + clicking × removes it", async () => {
    const onChange = vi.fn();
    const filters: ActiveFilters = { ...empty, cameras: ["Sony", "Canon"] };
    const { getAllByText, container } = render(FilterChipStrip, {
      filters, tagLabels: {}, onChange,
    });
    expect(container.querySelectorAll(".chip").length).toBe(2);
    const xButtons = container.querySelectorAll(".chip-x");
    await fireEvent.click(xButtons[0] as Element);
    expect(onChange).toHaveBeenCalledWith({ ...empty, cameras: ["Canon"] });
  });

  it("Clear all only when ≥2 chips", () => {
    const a = render(FilterChipStrip, {
      filters: { ...empty, cameras: ["Sony"] }, tagLabels: {}, onChange: () => {},
    });
    expect(a.queryByText("Clear all")).toBeNull();
    const b = render(FilterChipStrip, {
      filters: { ...empty, cameras: ["Sony", "Canon"] }, tagLabels: {}, onChange: () => {},
    });
    expect(b.queryByText("Clear all")).toBeTruthy();
  });

  it("tag chips render display label not key", () => {
    const { getByText } = render(FilterChipStrip, {
      filters: { ...empty, tagKeys: ["dog"] },
      tagLabels: { dog: "Dog" }, onChange: () => {},
    });
    expect(getByText(/Dog/)).toBeTruthy();
  });
});
```

- [ ] **Step 2: Run tests to verify failure**

```
cd frontend && bun run test -- FilterChipStrip.test
```

Expected: FAIL.

- [ ] **Step 3: Create the component**

Create `frontend/src/lib/filters/FilterChipStrip.svelte`:

```svelte
<script lang="ts">
  import { isEmpty, type ActiveFilters } from "./activeFilters";

  let {
    filters,
    tagLabels,
    onChange,
  }: {
    filters: ActiveFilters;
    tagLabels: Record<string, string>; // tag_key → tag_label for chip display
    onChange: (next: ActiveFilters) => void;
  } = $props();

  type Chip = { kind: string; value: string; display: string };

  const chips = $derived.by((): Chip[] => {
    const out: Chip[] = [];
    for (const v of filters.cameras) out.push({ kind: "camera", value: v, display: v });
    for (const v of filters.lenses) out.push({ kind: "lens", value: v, display: v });
    for (const k of filters.tagKeys) {
      out.push({ kind: "tag", value: k, display: `tag: ${tagLabels[k] ?? k}` });
    }
    if (filters.hasGps === true) out.push({ kind: "has_gps", value: "true", display: "Has GPS" });
    if (filters.hasGps === false) out.push({ kind: "has_gps", value: "false", display: "No GPS" });
    if (filters.mediaType !== null) {
      out.push({ kind: "media_type", value: filters.mediaType,
        display: filters.mediaType === "photo" ? "Photo" : "Video" });
    }
    return out;
  });

  function remove(kind: string, value: string) {
    const f = { ...filters };
    switch (kind) {
      case "camera":     f.cameras = f.cameras.filter((v) => v !== value); break;
      case "lens":       f.lenses = f.lenses.filter((v) => v !== value); break;
      case "tag":        f.tagKeys = f.tagKeys.filter((v) => v !== value); break;
      case "has_gps":    f.hasGps = null; break;
      case "media_type": f.mediaType = null; break;
    }
    onChange(f);
  }

  function clearAll() {
    onChange({ cameras: [], lenses: [], tagKeys: [], hasGps: null, mediaType: null });
  }
</script>

{#if !isEmpty(filters)}
  <div class="strip">
    <span class="leading">Filters:</span>
    {#each chips as c (c.kind + ":" + c.value)}
      <button
        type="button"
        class="chip"
        title="Remove"
        onclick={() => remove(c.kind, c.value)}
      >
        <span class="display">{c.display}</span>
        <span class="chip-x" aria-hidden="true">×</span>
      </button>
    {/each}
    {#if chips.length >= 2}
      <button type="button" class="clear-all" onclick={clearAll}>Clear all</button>
    {/if}
  </div>
{/if}

<style>
  .strip {
    display: flex; flex-wrap: wrap; align-items: center;
    gap: var(--space-2);
    padding: var(--space-3) var(--space-4);
    border-bottom: 1px solid var(--border);
    background: var(--surface);
  }
  .leading {
    font-size: var(--text-xs); color: var(--ink-4);
    text-transform: uppercase;
    letter-spacing: var(--label-track);
  }
  .chip {
    display: inline-flex; align-items: center; gap: var(--space-2);
    height: 24px; padding: 0 8px;
    background: color-mix(in srgb, var(--amber) 14%, transparent);
    color: var(--amber);
    border: 1px solid color-mix(in srgb, var(--amber) 24%, transparent);
    font-size: var(--text-sm); font-weight: 500;
    cursor: pointer;
    transition: background 100ms;
  }
  .chip:hover { background: color-mix(in srgb, var(--amber) 22%, transparent); }
  .chip-x { font-size: 14px; opacity: 0.65; line-height: 1; }
  .clear-all {
    margin-left: auto;
    background: transparent; border: 0;
    color: var(--ink-3);
    font-size: var(--text-sm);
    cursor: pointer;
  }
  .clear-all:hover { color: var(--ink); }
</style>
```

- [ ] **Step 4: Run tests to verify pass**

```
cd frontend && bun run test -- FilterChipStrip.test
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/lib/filters/FilterChipStrip.svelte frontend/src/lib/filters/FilterChipStrip.test.ts
git commit -m "$(cat <<'EOF'
feat(filters): FilterChipStrip — active-filter chips with Clear all

One chip per active filter value. Tag chips show display label sourced
from the facets response. Clear all appears at ≥2 chips. Stateless;
parent owns ActiveFilters.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 16 — Mount `FilterSidebar` in `Sidebar.svelte`

**Files:**
- Modify: `frontend/src/lib/components/Sidebar.svelte`

The sidebar component now needs a `route` prop (current route name), an `activeFilters` prop, a `facetsResponse` prop, and an `onFiltersChange` callback. Pass them through to `FilterSidebar`.

- [ ] **Step 1: Inspect callers of Sidebar**

```
grep -rn '<Sidebar' frontend/src/ --include='*.svelte'
```

Find every place Sidebar is rendered (likely `App.svelte`). Each will need to pass the new props.

- [ ] **Step 2: Modify `Sidebar.svelte`**

Add to the `<script>` block:

```ts
import FilterSidebar from "../filters/FilterSidebar.svelte";
import type { ActiveFilters } from "../filters/activeFilters";
import type { FacetsResponse } from "../filters/facetsStore.svelte";
```

Extend the props:

```ts
let {
  active = "",
  appConfig,
  route,
  activeFilters,
  facetsResponse,
  onFiltersChange,
}: {
  active?: string;
  appConfig: AppConfigStore;
  route: "library" | "search" | "map" | "albums" | "albums.detail" | "sessions" | "settings" | "settings.ai" | "shares" | "media" | "hidden" | "notfound";
  activeFilters: ActiveFilters;
  facetsResponse: FacetsResponse | null;
  onFiltersChange: (next: ActiveFilters) => void;
} = $props();
```

After the `<nav>` block, mount `FilterSidebar` only on routes that support filters:

```svelte
{#if route === "library" || route === "search" || route === "map"}
  <FilterSidebar
    {route}
    filters={activeFilters}
    response={facetsResponse}
    onChange={onFiltersChange}
  />
{/if}
```

- [ ] **Step 3: Wire from App.svelte**

In `frontend/src/App.svelte` (or wherever Sidebar is mounted):

Add imports:

```ts
import { FacetsStore } from "./lib/filters/facetsStore.svelte";
import { fromRoute, withFilters, type ActiveFilters } from "./lib/filters/activeFilters";
```

Add a singleton facets store:

```ts
const facetsStore = new FacetsStore(client);
```

Compute current `ActiveFilters` from the route:

```ts
const activeFilters = $derived(fromRoute(router.current));
```

Trigger a fetch when route or filters change:

```ts
$effect(() => {
  if (router.current.route === "library" ||
      router.current.route === "search" ||
      router.current.route === "map") {
    facetsStore.fetch(router.current.route, activeFilters);
  }
});
```

Add the change handler:

```ts
function onFiltersChange(next: ActiveFilters) {
  const sp = withFilters(new URLSearchParams(window.location.search), next);
  const qs = sp.toString();
  router.navigate(`${window.location.pathname}${qs ? "?" + qs : ""}`);
}
```

Pass to Sidebar:

```svelte
<Sidebar
  {active}
  {appConfig}
  route={router.current.route}
  {activeFilters}
  facetsResponse={facetsStore.response}
  {onFiltersChange}
/>
```

- [ ] **Step 4: Run unit tests**

```
cd frontend && bun run test
```

Expected: existing Sidebar.test passes (you'll need to update its render with the new required props — pass `activeFilters: empty`, `facetsResponse: null`, `onFiltersChange: () => {}`, `route: "library"`).

- [ ] **Step 5: Run type-check**

```
cd frontend && bun run check
```

Expected: clean.

- [ ] **Step 6: Commit**

```bash
git add frontend/src/lib/components/Sidebar.svelte frontend/src/App.svelte frontend/src/lib/components/Sidebar.test.ts
git commit -m "$(cat <<'EOF'
feat(sidebar): mount FilterSidebar on /library, /search, /map

App.svelte owns the FacetsStore singleton + the URL writer; Sidebar is
the dumb mounting point that conditions on route. Other routes
(albums, sessions, settings, media, etc.) skip the FILTERS group
entirely.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 17 — Mount `FilterChipStrip` in `Library.svelte`; pass filters to mediaStore

**Files:**
- Modify: `internal/httpapi/media.go` (extend `listMediaInput` with new query fields; map into `media.ListFilter`)
- Modify: `internal/httpapi/media_test.go` (add a route-level test that hits `/api/v1/media?camera=…&lens=…&facet_tag=…&has_gps=1` and asserts the narrowed result set)
- Modify: `frontend/src/routes/Library.svelte`
- Modify: `frontend/src/App.svelte` (thread `tagLabels` + `onFiltersChange` props to Library)

Library.svelte already exists; mount the chip strip above the grid and call `mediaStore.setFilters(activeFilters)` whenever `activeFilters` changes. **Backend gap:** `media.ListFilter` accepts the new fields after Task 2, but the `/api/v1/media` HTTP route still ignores `camera`, `lens`, `facet_tag`, and `has_gps`. Extend the input first so the frontend filter call actually narrows the response.

- [ ] **Step 0: Extend `/api/v1/media` HTTP route inputs**

Open `internal/httpapi/media.go` and locate `listMediaInput`. Add the four facet fields (note `,explode` for slices, mirroring `/search`'s `Tag []string `query:"tag,explode"`):

```go
Camera   []string `query:"camera,explode"`
Lens     []string `query:"lens,explode"`
FacetTag []string `query:"facet_tag,explode"`
HasGPS   *bool    `query:"has_gps"`
```

In the handler that maps `listMediaInput` → `media.ListFilter`, forward these:

```go
ListFilter{
    // ... existing fields
    Cameras:    in.Camera,
    Lenses:     in.Lens,
    AnyTagKeys: in.FacetTag,
    HasGPS:     in.HasGPS,
}
```

Add a route test (Go side) that seeds two cameras' rows for one owner, hits `/api/v1/media?camera=Sony%20A7R%20IV`, and asserts only the matching IDs come back. Re-run `make api-generate` to refresh `openapi.json`.

- [ ] **Step 1: Inspect Library.svelte structure**

```
head -80 frontend/src/routes/Library.svelte
```

Locate the `<script>` block (where the mediaStore is constructed) and the place where the grid is rendered.

- [ ] **Step 2: Modify Library.svelte**

In the `<script>` block, add imports:

```ts
import FilterChipStrip from "../lib/filters/FilterChipStrip.svelte";
import { fromRoute, type ActiveFilters } from "../lib/filters/activeFilters";
import { router } from "../lib/router/router.svelte";
```

Receive `activeFilters`, `tagLabels`, and `onFiltersChange` from props (App.svelte will pass them):

```ts
let {
  activeFilters,
  tagLabels,
  onFiltersChange,
  // ...existing props
}: {
  activeFilters: ActiveFilters;
  tagLabels: Record<string, string>;
  onFiltersChange: (next: ActiveFilters) => void;
  // ...
} = $props();
```

Drive the mediaStore on filter change:

```ts
$effect(() => {
  mediaStore.setFilters(activeFilters);
  void mediaStore.loadInitial();
});
```

Render the chip strip immediately above the photo grid:

```svelte
<FilterChipStrip filters={activeFilters} {tagLabels} onChange={onFiltersChange} />
<!-- existing grid markup follows -->
```

Empty-state branch — when filters are active and the grid yields zero rows, show a placeholder instead of a blank grid. Find the grid render block and wrap with:

```svelte
{#if mediaStore.months.length === 0 && !mediaStore.loading && !isEmpty(activeFilters)}
  <div class="empty-filtered" role="status">
    <p>No photos match these filters.</p>
    <button type="button" onclick={() => onFiltersChange({
      cameras: [], lenses: [], tagKeys: [], hasGps: null, mediaType: null,
    })}>Clear all</button>
  </div>
{:else}
  <!-- existing grid markup -->
{/if}
```

Add `import { isEmpty } from "../lib/filters/activeFilters";` to the script. Style the `.empty-filtered` block in the file's `<style>` block — center the text with generous padding to match the grid's negative space.

In `App.svelte`, build `tagLabels` from the facets response and pass through:

```ts
const tagLabels = $derived.by((): Record<string, string> => {
  const out: Record<string, string> = {};
  for (const t of facetsStore.response?.tags ?? []) out[t.key] = t.label;
  return out;
});
```

```svelte
<Library {activeFilters} {tagLabels} {onFiltersChange} {/* ... */} />
```

- [ ] **Step 3: Run tests**

```
cd frontend && bun run test -- Library
```

Expected: existing Library tests pass with the new props plumbed in (tests may need updating to pass the new props).

- [ ] **Step 4: Run dev and manually verify**

```
make dev
```

Open `http://localhost:<port>/library`. Click a camera in the sidebar. Verify:
- Chip appears above the grid.
- URL shows `?camera=<value>`.
- Grid contents change.
- Clicking the chip × removes the filter and reloads.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/routes/Library.svelte frontend/src/App.svelte
git commit -m "$(cat <<'EOF'
feat(library): wire chip strip + filter-driven mediaStore reload

Library mounts FilterChipStrip above the grid and reactively calls
mediaStore.setFilters whenever activeFilters changes. App.svelte
threads tagLabels (sourced from the facets response) through so chips
display 'tag: Dog' rather than 'tag: dog'.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 18 — Wire filters into `Search.svelte`

**Files:**
- Modify: `internal/httpapi/search.go` (extend `searchInput` with new query fields)
- Modify: `internal/service/search/service.go` (extend `searchsvc.Request` and propagate into `hybrid.Input`)
- Modify: `internal/httpapi/search_test.go` and/or `internal/service/search/service_test.go`
- Modify: `frontend/src/routes/Search.svelte`
- Modify: `frontend/src/lib/search/searchStore.svelte.ts`

Search already supports `?q=` and a few filter params. Extend its store call to include the new facet params. **Backend gap:** `hybrid.Input` accepts the four new fields after Task 1, but `/api/v1/search` still ignores `camera`, `lens`, `facet_tag`, and `has_gps` — the searchInput query struct, `searchsvc.Request`, and the service-to-`hybrid.Input` mapping all need extending before the frontend call honours these filters.

- [ ] **Step 0: Extend `/api/v1/search` backend**

In `internal/httpapi/search.go`, add to the search input struct (mirror the `,explode` convention already in use for `Tag`):

```go
Camera   []string `query:"camera,explode"`
Lens     []string `query:"lens,explode"`
FacetTag []string `query:"facet_tag,explode"`
HasGPS   *bool    `query:"has_gps"`
```

In `internal/service/search/service.go`, extend `searchsvc.Request` with the same fields and forward them in the `hybrid.Input` build:

```go
Cameras:    req.Cameras,
Lenses:     req.Lenses,
AnyTagKeys: req.FacetTag,
HasGPS:     req.HasGPS,
```

Add a service-level test that exercises one new field end-to-end, and a route-level test that confirms repeated `?camera=A&camera=B` binds to a 2-element slice (regression for the `,explode` modifier). Re-run `make api-generate`.

- [ ] **Step 1: Inspect searchStore**

```
grep -n 'GET\|fetch\|client\.' frontend/src/lib/search/searchStore.svelte.ts | head
```

Locate where the request URL is assembled.

- [ ] **Step 2: Add new facet params to the request**

Extend the search request builder to include `camera`, `lens`, `facet_tag`, `has_gps` from `ActiveFilters`. Mirror the URL conventions used by `withFilters`.

For example, if the existing builder constructs a `URLSearchParams`:

```ts
for (const v of activeFilters.cameras) sp.append("camera", v);
for (const v of activeFilters.lenses) sp.append("lens", v);
for (const v of activeFilters.tagKeys) sp.append("facet_tag", v);
if (activeFilters.hasGps !== null) sp.set("has_gps", activeFilters.hasGps ? "1" : "0");
// media_type already wired
```

Add a reset trigger when `filterKey(activeFilters)` changes — searchStore likely already has a reset on `q` change; extend the trigger to include the new params.

- [ ] **Step 3: Mount FilterChipStrip in Search.svelte**

Same pattern as Library:

```svelte
<FilterChipStrip filters={activeFilters} {tagLabels} onChange={onFiltersChange} />
<!-- existing /search grid -->
```

Wire the prop chain through App.svelte (already in place from Task 17).

- [ ] **Step 4: Run tests**

```
cd frontend && bun run test -- searchStore Search
```

Expected: PASS (update existing tests to expect the new params if they assert URL shape).

- [ ] **Step 5: Manual verify**

Open `/search?q=mountain`. Click a camera. URL becomes `?q=mountain&camera=...`. Results narrow.

- [ ] **Step 6: Commit**

```bash
git add frontend/src/routes/Search.svelte frontend/src/lib/search/searchStore.svelte.ts
git commit -m "$(cat <<'EOF'
feat(search): facet params on /search

Extend searchStore request URL with camera/lens/facet_tag/has_gps and
add a filterKey-keyed reset trigger so q + new facets compose without
interleaving stale results. Mounts FilterChipStrip above the grid.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 19 — Wire filters into `Map.svelte`

**Files:**
- Modify: `internal/httpapi/media_geo.go` (extend the geo handler input with new query fields; map into `media.ListGeoFilter`)
- Modify: `internal/service/media_service.go` (extend `MediaService.ListGeo` to accept and forward the new fields)
- Modify: `internal/httpapi/media_geo_test.go` (route test for repeated facet params)
- Modify: `frontend/src/routes/Map.svelte`
- Modify: `frontend/src/lib/geo/geoStore.svelte.ts` (or wherever the geo data is fetched)

The geo store is one-shot per page-load — on filter change we re-fetch from `/api/v1/media/geo` with the new params. Note that `has_gps` is NOT a /map param. **Backend gap:** `media.ListGeoFilter` accepts the new fields after Task 3, but `/api/v1/media/geo` and `MediaService.ListGeo` still ignore them — extend the route input + service wrapper first so the frontend re-fetch actually narrows the pin set.

- [ ] **Step 0: Extend `/api/v1/media/geo` backend**

In `internal/httpapi/media_geo.go`, add to the geo input struct (note: `has_gps` is intentionally excluded — `/geo`'s contract is geotagged-only):

```go
Camera    []string `query:"camera,explode"`
Lens      []string `query:"lens,explode"`
FacetTag  []string `query:"facet_tag,explode"`
MediaType *string  `query:"media_type" enum:"photo,video"`
```

In the handler, forward these into the `MediaService.ListGeo` call. Extend `MediaService.ListGeo` (and `ListGeoFilter` already extended in Task 3) to accept and forward the new fields. Add a route test that hits `/api/v1/media/geo?camera=…` and asserts pin narrowing. Re-run `make api-generate`.

- [ ] **Step 1: Extend the geo fetch URL**

Locate the geo store's fetch call. Pass `camera`, `lens`, `facet_tag`, `media_type` (NOT `has_gps`) into the query string.

- [ ] **Step 2: Reset on filter change**

Reactively re-fetch when `filterKey(activeFilters)` changes. Map should clear pin clusters and redraw on new data.

- [ ] **Step 3: Mount FilterChipStrip**

In `Map.svelte`, render the strip above the map pane. Note: /map's vertical real estate is constrained — use a compact variant if needed. For v1, mount the same component as on /library and /search; if it visually crowds the map, address in a follow-up.

- [ ] **Step 4: Verify Places facet hidden**

Per Task 14, `FilterSidebar` already hides the Places sub-section when `route === "map"`. No additional work here, just confirm at runtime.

- [ ] **Step 5: Run tests**

```
cd frontend && bun run test -- Map
```

Expected: PASS.

- [ ] **Step 6: Manual verify**

Open `/map`. Confirm:
- Sidebar FILTERS group has Cameras, Lenses, Tags, Media Type — but NO Places.
- Toggling a camera reduces pin count.
- URL gains `?camera=<value>`.

- [ ] **Step 7: Commit**

```bash
git add frontend/src/routes/Map.svelte frontend/src/lib/geo/geoStore.svelte.ts
git commit -m "$(cat <<'EOF'
feat(map): facet params on /map (Places hidden)

geoStore re-fetches on filterKey change. Camera/Lens/Tag/MediaType
flow through; HasGPS is intentionally not a /map param (route is
geotagged-only). Sidebar's Places section hides on /map per Task 14.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 20 — Playwright e2e for sidebar facets

**Files:**
- Create: `frontend/tests/e2e/filters.spec.ts`

Smoke tests for the end-to-end flow: sidebar checkbox → URL update → grid re-renders → chip appears → × removes filter.

- [ ] **Step 1: Inspect existing e2e fixtures**

```
ls cmd/e2e-server/ && grep -n 'fixture\|seed\|insertMedia' cmd/e2e-server/main.go | head
```

The e2e-server seeds DB fixtures at boot. Find how it's invoked from the playwright config and whether there's a flag (like the `--sharing-enabled` flag from the summary) for seeding facet-test fixtures.

- [ ] **Step 2: Add facet-test seed data to e2e-server**

Modify `cmd/e2e-server/main.go` to seed (when present, behind an `--ai-tags` or similar flag, or unconditionally if your seed is small):
- 3 cameras' worth of media (e.g., 5 Sony A7R IV photos, 3 Canon EOS R5 photos, 2 iPhone 15 Pro photos).
- 2 lenses (FE 24-70mm on the Sony, RF 50mm on the Canon).
- AI tags: 3 photos tagged "dog", 2 photos tagged "cat".
- Mix of geotagged + non-geotagged.
- Mix of photo + video.

If seed code already exists, augment it; otherwise add a new helper. Keep it deterministic so the spec can assert exact counts.

- [ ] **Step 3: Write the spec**

Create `frontend/tests/e2e/filters.spec.ts`:

```ts
import { test, expect } from "@playwright/test";

test.describe("Sidebar facets", () => {
  test("camera filter narrows the library and renders a chip", async ({ page }) => {
    await page.goto("/library");
    // Wait for the FILTERS group to appear (depends on facets fetch).
    await expect(page.getByText("FILTERS")).toBeVisible();

    // Expand Cameras (it should be expanded by default; this is a no-op
    // when expanded but ensures determinism if the localStorage state
    // disagrees).
    const cameraSection = page.getByRole("button", { name: /Cameras/ });
    if (await cameraSection.getAttribute("aria-expanded") === "false") {
      await cameraSection.click();
    }

    await page.getByText("Sony A7R IV").click();

    // URL gains camera param.
    await expect(page).toHaveURL(/camera=Sony\+A7R\+IV/);
    // Chip strip above grid shows the chip.
    await expect(page.locator(".chip", { hasText: "Sony A7R IV" })).toBeVisible();
    // Photo grid narrows — the precise count is fixture-dependent.
    // Assert the grid has at least one and at most the seeded count.
  });

  test("clicking chip × removes the filter", async ({ page }) => {
    await page.goto("/library?camera=Sony+A7R+IV");
    await expect(page.locator(".chip", { hasText: "Sony A7R IV" })).toBeVisible();
    await page.locator(".chip-x").first().click();
    await expect(page).not.toHaveURL(/camera=/);
    await expect(page.locator(".chip", { hasText: "Sony A7R IV" })).not.toBeVisible();
  });

  test("Places hidden on /map", async ({ page }) => {
    await page.goto("/map");
    await expect(page.getByText("FILTERS")).toBeVisible();
    await expect(page.getByText("PLACES")).not.toBeVisible();
    await expect(page.getByText("CAMERAS")).toBeVisible();
  });

  test("filter survives /library → lightbox → back navigation", async ({ page }) => {
    await page.goto("/library?camera=Sony+A7R+IV");
    // Click first photo to open the lightbox.
    const firstPhoto = page.locator(".grid img").first();
    await firstPhoto.click();
    await expect(page).toHaveURL(/\/media\//);
    await page.goBack();
    await expect(page).toHaveURL(/camera=Sony\+A7R\+IV/);
    await expect(page.locator(".chip", { hasText: "Sony A7R IV" })).toBeVisible();
  });
});
```

- [ ] **Step 4: Run e2e**

```
cd frontend && bun run test:e2e -- filters
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add frontend/tests/e2e/filters.spec.ts cmd/e2e-server/main.go
git commit -m "$(cat <<'EOF'
test(e2e): playwright smoke for sidebar facets

Camera click → URL update → chip appears → × removes; Places hidden
on /map; filter survives lightbox round-trip. Seeds 3 cameras /
2 lenses / mixed-tag fixtures in e2e-server.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Final verification

After Task 20:

- [ ] `make test` — full Go suite passes.
- [ ] `make lint` — clean.
- [ ] `cd frontend && bun run check` — clean.
- [ ] `cd frontend && bun run test` — all unit suites green.
- [ ] `cd frontend && bun run test:e2e` — full e2e green.
- [ ] `make build` — clean release build.
- [ ] Manual smoke: `make dev`, navigate /library → toggle two facets → confirm URL, chips, grid, sidebar all stay in sync. Reload — state persists. Navigate to /search and /map — facet UI mounts correctly per spec.
