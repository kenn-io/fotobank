# F2.3 Albums + Owner-side Sharing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship the web/SPA layer for the existing albums and owner-side sharing backends — `/albums`, `/albums/:id`, `/shares` routes, `MediaActions` cluster (consumed by MediaDetail and route-aware ActionBar), bulk-select-by-group on every `MonthChunk` header, plus three small backend additions (album sort_by=taken, share target_summary, share next_offset).

**Architecture:** Three new Svelte routes added under sidebar groups BROWSE / CURATE / MANAGE; three new stores (`AlbumsStore`, `AlbumDetailStore`, `SharesStore`) mirror the `MediaStore` runes pattern; the shared `MediaStore.mergeRaw` is reused so a row viewed in Library and Album shares state. `VirtualGrid` grows a `timelineChrome` prop (flat mode for albums) and a forwarding `headerAction?: Snippet` prop that relays to each `MonthChunk` it constructs. Sessions wires `headerAction` directly on `MonthChunk` since it constructs them itself. ActionBar becomes route-aware: each media-bearing route renders it with its own action set; non-media routes don't render it. `MediaActions.svelte` is the F2.5-required action cluster — same component for single-photo (MediaDetail) and bulk (ActionBar) paths via a `mediaIds: string[]` prop. Backend touches are surgical: a third `AlbumMediaFilter.SortBy` value, a `target_summary` field on share DTOs computed in service/repo (not the share domain package), and a `next_offset` field on the share list output.

**Tech Stack:** Go (modernc.org/sqlite, sqlx, huma/v2, golang-migrate, testify), Svelte 5 + TypeScript (runes, vitest, playwright), bun + bunx, oxlint, prek pre-commit hooks.

**Spec:** `docs/superpowers/specs/2026-04-28-fotobank-web-f2.3-albums-sharing-design.md`

**Implementer note (Svelte 5 syntax):** The spec's snippet examples use `{#snippet name(args)}` inside `<Component>...</Component>` children. The current `VirtualGrid` is prop-based (`<VirtualGrid months={...} />` — no children consumed). When implementing Tasks 6–8 + 13, pick the Svelte 5 syntax that actually compiles for whichever component owns the snippet. The contract is: `VirtualGrid` accepts an optional `headerAction` prop of type `Snippet<[chunk: Month]>` and forwards it to each `MonthChunk`; `MonthChunk` accepts `headerAction?: Snippet` and renders it inside `.day-header`. The snippet author is the route component (Library, Sessions). Verify against `frontend/src/lib/grid/VirtualGrid.svelte` and `frontend/src/lib/grid/MonthChunk.svelte` patterns before authoring tests.

---

## File map

**Created (Go)**

- `internal/album/repo.go` — extended (`SortBy="taken"` ORDER BY case + `GetNamesByIDs(ids)` helper for share `target_summary`).
- (No new Go files; everything lives in existing packages.)

**Created (frontend, new components / stores)**

- `frontend/src/lib/albums/albumsStore.svelte.ts` — `AlbumsStore` runes store.
- `frontend/src/lib/albums/albumsStore.test.ts`
- `frontend/src/lib/albums/albumDetailStore.svelte.ts` — `AlbumDetailStore` runes store with `MediaStore` reuse + `removeMany` bounded-parallelism.
- `frontend/src/lib/albums/albumDetailStore.test.ts`
- `frontend/src/lib/shares/sharesStore.svelte.ts` — `SharesStore` runes store with poll + lazy detail/preview caches.
- `frontend/src/lib/shares/sharesStore.test.ts`
- `frontend/src/lib/format/normalizeForSearch.ts` — `normalizeForSearch(s: string): string` helper (trim + toLocaleLowerCase).
- `frontend/src/lib/format/normalizeForSearch.test.ts`
- `frontend/src/lib/format/parseGrantee.ts` — `parseGrantee(raw: string): {hub: string; user_id: string} | null` helper.
- `frontend/src/lib/format/parseGrantee.test.ts`
- `frontend/src/lib/components/GroupSelectButton.svelte`
- `frontend/src/lib/components/GroupSelectButton.test.ts`
- `frontend/src/lib/components/MediaActions.svelte`
- `frontend/src/lib/components/MediaActions.test.ts`
- `frontend/src/lib/components/AddToAlbumModal.svelte`
- `frontend/src/lib/components/AddToAlbumModal.test.ts`
- `frontend/src/lib/components/ShareModal.svelte`
- `frontend/src/lib/components/ShareModal.test.ts`
- `frontend/src/lib/components/NewAlbumForm.svelte` — shared by `/albums` "+ New Album" + AddToAlbumModal create-new flow.
- `frontend/src/lib/components/NewAlbumForm.test.ts`
- `frontend/src/lib/components/RenameAlbumModal.svelte`
- `frontend/src/lib/components/RenameAlbumModal.test.ts`
- `frontend/src/lib/components/ConfirmModal.svelte` — generic confirm modal (Delete album, Revoke share).
- `frontend/src/lib/components/ConfirmModal.test.ts`
- `frontend/src/lib/components/SharesList.svelte`
- `frontend/src/lib/components/SharesList.test.ts`
- `frontend/src/lib/components/ShareStatePill.svelte`
- `frontend/src/lib/components/ShareStatePill.test.ts`
- `frontend/src/lib/components/ShareDrawer.svelte`
- `frontend/src/lib/components/ShareDrawer.test.ts`
- `frontend/src/lib/components/AlbumGrid.svelte`
- `frontend/src/lib/components/AlbumGrid.test.ts`
- `frontend/src/routes/AlbumsIndex.svelte` — `/albums` route.
- `frontend/src/routes/AlbumDetail.svelte` — `/albums/:id` route.
- `frontend/src/routes/SharesPage.svelte` — `/shares` route.
- `frontend/tests/e2e/albums.spec.ts`
- `frontend/tests/e2e/shares.spec.ts`

**Modified (Go)**

- `internal/album/album.go` — `ErrInvalidSort` message lists `taken`; `AlbumMediaFilter` validator allows it.
- `internal/album/repo.go` — `ListMedia` ORDER BY adds `taken`; new `GetNamesByIDs(ctx, ids []string) (map[string]string, error)`.
- `internal/album/repo_test.go` — sort-by-taken tests (ASC, DESC, NULLS LAST), `GetNamesByIDs` tests.
- `internal/service/album_service.go` — validator accepts `taken`.
- `internal/service/album_service_test.go` — sort validator coverage.
- `internal/share/repo.go` — `CountSharedMediaByScopes(ctx, ids []string) (map[string]int, error)` (batch variant of existing single-id helper).
- `internal/share/repo_test.go` — batch counter tests.
- `internal/service/share_service.go` — `PopulateTargetSummary(ctx, scopes []share.Scope) (map[string]TargetSummary, error)` orchestrator that calls album.Repo.GetNamesByIDs + share.Repo.CountSharedMediaByScopes.
- `internal/service/share_service_test.go` — `PopulateTargetSummary` happy path + mixed media_set/album_live + missing album row + zero scopes.
- `internal/share/share.go` — domain type `TargetSummary{Label string; ItemCount *int}` only; no DB lookups.
- `internal/share/share_test.go` — TargetSummary constant invariants.
- `internal/httpapi/shares.go` — `targetSummaryDTO` mirror; `scopeDTO` and `scopeDetailDTO` gain `TargetSummary *targetSummaryDTO`; list and detail handlers populate it via the new service helper; list output gains `NextOffset *int` via the limit+1 pattern.
- `internal/httpapi/shares_test.go` — target_summary on list+detail for both target types; pagination next_offset on/off.
- `internal/httpapi/albums.go` — no change (sort_by pass-through is already in place).
- `openapi.json`, `frontend/src/lib/api/generated/schema.ts` — regenerated by `make api-generate`.

**Modified (frontend)**

- `frontend/src/lib/selection/selectionStore.svelte.ts` — `addAll`, `removeAll`, `hasAll` methods; `lastAnchor` semantics per §13.4.
- `frontend/src/lib/selection/selectionStore.test.ts` — new method coverage.
- `frontend/src/lib/grid/MonthChunk.svelte` — accepts `headerAction?: Snippet` prop; renders inside `.day-header` next to the label.
- `frontend/src/lib/grid/VirtualGrid.svelte` — accepts `timelineChrome?: boolean` (default `true`) and `headerAction?: Snippet<[chunk: Month]>`; in flat mode, hides StickyMonthBar + YearScrubber and forwards `label={undefined}` to each `MonthChunk`; always forwards `headerAction` to MonthChunk.
- `frontend/src/lib/components/Sidebar.svelte` — three labelled groups with new entries.
- `frontend/src/lib/components/ActionBar.svelte` — accepts `actions?: Snippet` prop; renders the snippet next to the count when selection is non-empty.
- `frontend/src/lib/components/ActionBar.test.ts` — actions snippet rendering coverage.
- `frontend/src/lib/router/router.svelte.ts` — registers `/albums`, `/albums/:id`, `/shares` routes; reads `?album_id` and `?show_revoked` query params on `/shares`.
- `frontend/src/lib/router/router.test.ts` — new route matching tests.
- `frontend/src/App.svelte` — wires the new routes, hoists `AlbumsStore` and `SharesStore` next to `MediaStore`.
- `frontend/src/routes/Library.svelte` — passes `headerAction` snippet to VirtualGrid; mounts ActionBar with `actions` snippet (Add to album, Share via MediaActions).
- `frontend/src/routes/Sessions.svelte` — passes `headerAction` snippet directly on each MonthChunk; mounts ActionBar with the same actions snippet as Library.
- `frontend/src/routes/MediaDetail.svelte` — mounts `<MediaActions mediaIds={[media.id]} />` in its header (only when `media.paired_with_id` is null — sidecar direct page does not mount it, per F2.2 §12 contract).
- `frontend/src/lib/api/client.ts` — no change (huma-generated types regen automatically).

**Untouched (per spec)**

- `internal/album/album.go::Album` struct fields — no new columns.
- `internal/share/share.go::Scope` struct fields — no new columns.
- `internal/db/migrations/` — F2.3 adds zero migrations.
- `internal/reconcile/` — no new behavior.
- `internal/cli/` — no new subcommands; existing `albums` / `shares` subcommands already cover what's needed for ops use.
- Mobile / touch UX — F2.5 mobile pass.

---

## Sentinel / type reference (used by multiple tasks)

```go
// internal/share/share.go (added by Task 2)
type TargetSummary struct {
    Label     string
    ItemCount *int  // nil for album_live, set for media_set
}

// internal/httpapi/shares.go (added by Task 2)
type targetSummaryDTO struct {
    Label     string `json:"label"`
    ItemCount *int   `json:"item_count,omitempty"`
}
```

```ts
// frontend/src/lib/albums/albumsStore.svelte.ts (added by Task 10)
type AlbumListItem = {
  id: string;
  name: string;
  created_at: string;
  updated_at: string;
  item_count: number;
  cover: { media_id: string; thumb_version: number } | null;
};

// frontend/src/lib/shares/sharesStore.svelte.ts (added by Task 20)
type ShareTargetType = "media_set" | "album_live";
type ShareBrokerStatus =
  | "pending" | "active" | "failed" | "revoking" | "revoked_remote";

type ScopeListRow = {
  uuid: string;
  target_type: ShareTargetType;
  target_album_id: string | null;
  target_summary: { label: string; item_count?: number } | null;
  grantee: { hub: string; user_id: string };
  grantee_handle?: string;
  allow_download: boolean;
  label: string;
  created_at: string;
  expires_at: string | null;
  revoked_at: string | null;
  broker_status: ShareBrokerStatus;
  broker_attempts: number;
  broker_last_error: string;
};
```

---

## Task 1: Album sort_by=taken with NULLS LAST

**Files:**
- Modify: `internal/album/album.go`
- Modify: `internal/album/repo.go`
- Modify: `internal/album/repo_test.go`
- Modify: `internal/service/album_service.go`
- Modify: `internal/service/album_service_test.go`

- [ ] **Step 1: Write the failing repo test for sort_by=taken DESC NULLS LAST**

Append to `internal/album/repo_test.go`:

```go
func TestRepoListMediaSortByTakenDescNullsLast(t *testing.T) {
    d := testutil.OpenTestDB(t)
    defer d.Close()
    repo := album.NewRepo(d.WriteDB(), d.ReadDB())
    ctx := context.Background()
    owner := owners.Principal{Hub: "h", UserID: "u"}

    seedOwner(t, d.WriteDB(), owner, "sk")
    a := seedAlbum(t, repo, owner, "T1")

    // m1: timestamp 2024-01-01, m2: timestamp 2025-06-01, m3: NULL timestamp.
    // seedMediaRow inserts via raw SQL (the helper at the bottom of
    // repo_test.go) so album tests stay independent of media.Repo's
    // larger insert surface. Extend it with a *time.Time variant for
    // NULL-timestamp rows if the existing helper doesn't already cover that.
    m1ID := newTestMediaWithTimestamp(t, d.WriteDB(), owner, "2024-01-01T00:00:00Z")
    m2ID := newTestMediaWithTimestamp(t, d.WriteDB(), owner, "2025-06-01T00:00:00Z")
    m3ID := newTestMediaNullTimestamp(t, d.WriteDB(), owner)

    // Add all three at the same timestamp; order would be alphabetical
    // by media_id under the "added" sort, so this test isolates the
    // taken sort behavior.
    _, _, err := repo.AddMedia(ctx, a.ID, []string{m1ID, m2ID, m3ID}, time.Now())
    require.NoError(t, err)

    rows, err := repo.ListMedia(ctx, a.ID, album.AlbumMediaFilter{SortBy: "taken", Limit: 10})
    require.NoError(t, err)
    require.Len(t, rows, 3)
    // Expect: m2 (2025) → m1 (2024) → m3 (NULL last).
    require.Equal(t, m2ID, rows[0].ID)
    require.Equal(t, m1ID, rows[1].ID)
    require.Equal(t, m3ID, rows[2].ID)
}

func TestRepoListMediaSortByTakenAscNullsLast(t *testing.T) {
    d := testutil.OpenTestDB(t)
    defer d.Close()
    repo := album.NewRepo(d.WriteDB(), d.ReadDB())
    ctx := context.Background()
    owner := owners.Principal{Hub: "h", UserID: "u"}

    seedOwner(t, d.WriteDB(), owner, "sk")
    a := seedAlbum(t, repo, owner, "T2")
    m1ID := newTestMediaWithTimestamp(t, d.WriteDB(), owner, "2024-01-01T00:00:00Z")
    m2ID := newTestMediaWithTimestamp(t, d.WriteDB(), owner, "2025-06-01T00:00:00Z")
    m3ID := newTestMediaNullTimestamp(t, d.WriteDB(), owner)
    _, _, err := repo.AddMedia(ctx, a.ID, []string{m1ID, m2ID, m3ID}, time.Now())
    require.NoError(t, err)

    rows, err := repo.ListMedia(ctx, a.ID, album.AlbumMediaFilter{SortBy: "taken", SortAsc: true, Limit: 10})
    require.NoError(t, err)
    require.Len(t, rows, 3)
    // Expect: m1 (2024) → m2 (2025) → m3 (NULL last in BOTH directions).
    require.Equal(t, m1.ID, rows[0].ID)
    require.Equal(t, m2.ID, rows[1].ID)
    require.Equal(t, m3.ID, rows[2].ID)
}
```

If `newTestMedia` / `newTestMediaNullTimestamp` helpers don't already exist with these exact signatures in `repo_test.go`, add them (return `media.Media` with the timestamp set or NULL respectively, all other fields populated minimally).

- [ ] **Step 2: Run repo tests to verify they fail**

```bash
go test ./internal/album/... -run TestRepoListMediaSortByTaken -v
```

Expected: FAIL with `album: sort_by must be "added" or "imported"` (current ErrInvalidSort) OR with the rows in default ordering, depending on how the validator gates.

- [ ] **Step 3: Update sort enum, validator, and ORDER BY clause**

Edit `internal/album/album.go` — update the `ErrInvalidSort` message:

```go
ErrInvalidSort = errors.New("album: sort_by must be \"added\", \"imported\", or \"taken\"")
```

Edit `internal/album/repo.go` `ListMedia` ORDER BY (find the existing switch on `f.SortBy` around the line that matches `am.added_at`):

```go
direction := "DESC"
if f.SortAsc {
    direction = "ASC"
}
var orderBy string
switch f.SortBy {
case "imported":
    orderBy = "m.imported_at " + direction + ", am.media_id " + direction
case "taken":
    orderBy = "m.timestamp " + direction + " NULLS LAST, am.media_id " + direction
default: // "added" or empty
    orderBy = "am.added_at " + direction + ", am.media_id " + direction
}
```

Edit `internal/service/album_service.go` validator (find the existing `if f.SortBy != "" && f.SortBy != "added" && f.SortBy != "imported"` check):

```go
switch f.SortBy {
case "", "added", "imported", "taken":
    // ok
default:
    return nil, album.ErrInvalidSort
}
```

- [ ] **Step 4: Add service-level test for the validator**

Append to `internal/service/album_service_test.go`:

```go
func TestAlbumServiceListMediaAcceptsSortByTaken(t *testing.T) {
    fx := newAlbumServiceFixture(t)
    a, err := fx.svc.Create(context.Background(), "Trip", fx.owner)
    require.NoError(t, err)
    _, err = fx.svc.ListMedia(context.Background(), a.ID, album.AlbumMediaFilter{SortBy: "taken", Limit: 10}, fx.owner)
    require.NoError(t, err)
}

func TestAlbumServiceListMediaRejectsUnknownSortBy(t *testing.T) {
    fx := newAlbumServiceFixture(t)
    a, err := fx.svc.Create(context.Background(), "Trip", fx.owner)
    require.NoError(t, err)
    _, err = fx.svc.ListMedia(context.Background(), a.ID, album.AlbumMediaFilter{SortBy: "garbage", Limit: 10}, fx.owner)
    require.ErrorIs(t, err, album.ErrInvalidSort)
}
```

- [ ] **Step 5: Run all album tests to verify they pass**

```bash
go test ./internal/album/... ./internal/service/... -run 'Album|TestRepoListMedia' -v
```

Expected: PASS.

- [ ] **Step 6: Run lint**

```bash
make lint
```

Expected: clean.

- [ ] **Step 7: Commit**

```bash
git add internal/album/album.go internal/album/repo.go internal/album/repo_test.go internal/service/album_service.go internal/service/album_service_test.go
git commit -m "feat(album): sort_by=taken with NULLS LAST"
```

---

## Task 2: scopeDTO.target_summary + service helper

**Files:**
- Modify: `internal/album/repo.go`
- Modify: `internal/album/repo_test.go`
- Modify: `internal/share/share.go`
- Modify: `internal/share/share_test.go`
- Modify: `internal/share/repo.go`
- Modify: `internal/share/repo_test.go`
- Modify: `internal/service/share_service.go`
- Modify: `internal/service/share_service_test.go`
- Modify: `internal/httpapi/shares.go`
- Modify: `internal/httpapi/shares_test.go`

- [ ] **Step 1: Write the failing test for `album.Repo.GetNamesByIDs`**

Append to `internal/album/repo_test.go`:

```go
func TestRepoGetNamesByIDsHappy(t *testing.T) {
    d := testutil.OpenTestDB(t)
    defer d.Close()
    repo := album.NewRepo(d.WriteDB(), d.ReadDB())
    ctx := context.Background()
    owner := owners.Principal{Hub: "h", UserID: "u"}

    seedOwner(t, d.WriteDB(), owner, "sk")
    a1 := seedAlbum(t, repo, owner, "Italy 2025")
    a2 := seedAlbum(t, repo, owner, "Family")

    names, err := repo.GetNamesByIDs(ctx, []string{a1.ID, a2.ID, "missing"})
    require.NoError(t, err)
    require.Equal(t, "Italy 2025", names[a1.ID])
    require.Equal(t, "Family", names[a2.ID])
    _, ok := names["missing"]
    require.False(t, ok)
}

func TestRepoGetNamesByIDsEmptyInput(t *testing.T) {
    d := testutil.OpenTestDB(t)
    defer d.Close()
    repo := album.NewRepo(d.WriteDB(), d.ReadDB())
    names, err := repo.GetNamesByIDs(context.Background(), nil)
    require.NoError(t, err)
    require.Empty(t, names)
}
```

- [ ] **Step 2: Run to verify failing**

```bash
go test ./internal/album/... -run TestRepoGetNamesByIDs -v
```

Expected: FAIL `repo.GetNamesByIDs undefined`.

- [ ] **Step 3: Implement `album.Repo.GetNamesByIDs`**

Append to `internal/album/repo.go`:

```go
// GetNamesByIDs returns a map of album_id → name for the given IDs.
// Missing IDs are simply absent from the map; no error is returned for
// not-found rows. The implementation chunks at 250 IDs (500 bind vars)
// to stay safely under SQLite's 999-variable cap.
func (r *Repo) GetNamesByIDs(ctx context.Context, ids []string) (map[string]string, error) {
    out := map[string]string{}
    if len(ids) == 0 {
        return out, nil
    }
    const chunkSize = 250
    for start := 0; start < len(ids); start += chunkSize {
        end := start + chunkSize
        if end > len(ids) {
            end = len(ids)
        }
        chunk := ids[start:end]
        placeholders := strings.Repeat("?,", len(chunk))
        placeholders = placeholders[:len(placeholders)-1]
        query := `SELECT id, name FROM albums WHERE id IN (` + placeholders + `)`
        args := make([]any, len(chunk))
        for i, id := range chunk {
            args[i] = id
        }
        rows, err := r.ro.QueryContext(ctx, query, args...)
        if err != nil {
            return nil, fmt.Errorf("album: GetNamesByIDs: %w", err)
        }
        for rows.Next() {
            var id, name string
            if err := rows.Scan(&id, &name); err != nil {
                rows.Close()
                return nil, fmt.Errorf("album: GetNamesByIDs scan: %w", err)
            }
            out[id] = name
        }
        if err := rows.Err(); err != nil {
            rows.Close()
            return nil, fmt.Errorf("album: GetNamesByIDs iter: %w", err)
        }
        rows.Close()
    }
    return out, nil
}
```

If `strings` and `fmt` aren't already imported in `repo.go`, add them.

- [ ] **Step 4: Write the failing test for `share.Repo.CountSharedMediaByScopes`**

Append to `internal/share/repo_test.go`:

```go
func TestRepoCountSharedMediaByScopesBatch(t *testing.T) {
    db := testutil.OpenTestDB(t)
    defer db.Close()
    repo := share.NewRepo(db)
    ctx := context.Background()

    s1 := mustCreateMediaSetScope(t, repo, []string{"m1", "m2", "m3"})
    s2 := mustCreateMediaSetScope(t, repo, []string{"m1"})
    s3 := mustCreateAlbumLiveScope(t, repo)

    counts, err := repo.CountSharedMediaByScopes(ctx, []string{s1.UUID, s2.UUID, s3.UUID, "missing"})
    require.NoError(t, err)
    require.Equal(t, 3, counts[s1.UUID])
    require.Equal(t, 1, counts[s2.UUID])
    // album_live: scope_media has zero rows; the helper returns 0 for it.
    require.Equal(t, 0, counts[s3.UUID])
    _, ok := counts["missing"]
    require.False(t, ok)
}

func TestRepoCountSharedMediaByScopesEmpty(t *testing.T) {
    db := testutil.OpenTestDB(t)
    defer db.Close()
    repo := share.NewRepo(db)
    counts, err := repo.CountSharedMediaByScopes(context.Background(), nil)
    require.NoError(t, err)
    require.Empty(t, counts)
}
```

If `mustCreateMediaSetScope` / `mustCreateAlbumLiveScope` helpers don't already exist in `repo_test.go`, add them as small thin wrappers that build a `share.Scope` and call `repo.Create`.

- [ ] **Step 5: Run to verify failing**

```bash
go test ./internal/share/... -run TestRepoCountSharedMediaByScopes -v
```

Expected: FAIL `repo.CountSharedMediaByScopes undefined`.

- [ ] **Step 6: Implement `share.Repo.CountSharedMediaByScopes`**

Append to `internal/share/repo.go`:

```go
// CountSharedMediaByScopes returns a map of scope_uuid → count of
// scope_media rows. Scopes with zero rows (album_live, or media_set
// whose membership was never inserted) appear in the map with count 0.
// Scopes whose UUID does not exist in the scopes table are absent
// from the output (callers can distinguish "scope exists with zero
// media" from "unknown uuid"). Chunks at 250 IDs (500 bind vars) to
// stay under SQLite's 999-variable cap. Two-query pattern: an
// existence pass against scopes seeds count=0 for every uuid that
// resolves to a row; a count pass against scope_media layers actual
// counts on top.
func (r *Repo) CountSharedMediaByScopes(ctx context.Context, uuids []string) (map[string]int, error) {
    out := map[string]int{}
    if len(uuids) == 0 {
        return out, nil
    }
    const chunkSize = 250
    for start := 0; start < len(uuids); start += chunkSize {
        end := start + chunkSize
        if end > len(uuids) {
            end = len(uuids)
        }
        chunk := uuids[start:end]
        placeholders := strings.Repeat("?,", len(chunk))
        placeholders = placeholders[:len(placeholders)-1]
        args := make([]any, len(chunk))
        for i, u := range chunk {
            args[i] = u
        }
        // Existence pass: seed count=0 only for uuids that actually
        // resolve to a scopes row. Missing uuids stay absent.
        existsRows, err := r.ro.QueryContext(ctx,
            `SELECT uuid FROM scopes WHERE uuid IN (`+placeholders+`)`, args...)
        if err != nil {
            return nil, fmt.Errorf("share: CountSharedMediaByScopes exists: %w", err)
        }
        for existsRows.Next() {
            var u string
            if err := existsRows.Scan(&u); err != nil {
                existsRows.Close()
                return nil, err
            }
            out[u] = 0
        }
        if err := existsRows.Close(); err != nil {
            return nil, err
        }
        query := `SELECT scope_uuid, COUNT(*) FROM scope_media
                  WHERE scope_uuid IN (` + placeholders + `)
                  GROUP BY scope_uuid`
        rows, err := r.ro.QueryContext(ctx, query, args...)
        if err != nil {
            return nil, fmt.Errorf("share: CountSharedMediaByScopes: %w", err)
        }
        for rows.Next() {
            var uuid string
            var count int
            if err := rows.Scan(&uuid, &count); err != nil {
                rows.Close()
                return nil, fmt.Errorf("share: CountSharedMediaByScopes scan: %w", err)
            }
            out[uuid] = count
        }
        if err := rows.Err(); err != nil {
            rows.Close()
            return nil, fmt.Errorf("share: CountSharedMediaByScopes iter: %w", err)
        }
        rows.Close()
    }
    // Final cleanup: scopes that were initialized to 0 but never
    // appeared in scope_media (e.g. album_live with no media membership)
    // remain at 0; that's correct. UUIDs that were truly absent from
    // the scopes table at all also remain at 0 — that's the only
    // ambiguity. Callers needing strict existence should prefer
    // ListByOwner / GetByUUID before counting.
    return out, nil
}
```

(Note the disambiguation comment: this helper does NOT verify existence; it only counts membership. Callers with valid `share.Scope` rows in hand are safe; callers with arbitrary UUIDs must look them up separately.)

- [ ] **Step 7: Add `TargetSummary` domain type**

Append to `internal/share/share.go` (under the existing struct declarations):

```go
// TargetSummary is a UI-friendly summary of a Scope's target. The
// helper that builds these lives in service/repo (see PopulateTargetSummary
// in share_service.go); the domain package owns the type, not the
// DB lookup.
type TargetSummary struct {
    Label     string
    ItemCount *int
}
```

Append to `internal/share/share_test.go`:

```go
func TestTargetSummaryConstantInvariants(t *testing.T) {
    // ItemCount nil for album_live; non-nil for media_set. The
    // helper enforces this — this test merely pins the zero value
    // shape so future renames don't accidentally re-type ItemCount.
    var s share.TargetSummary
    require.Equal(t, "", s.Label)
    require.Nil(t, s.ItemCount)
}
```

- [ ] **Step 8: Write the failing test for `ShareService.PopulateTargetSummary`**

Append to `internal/service/share_service_test.go`:

```go
func TestShareServicePopulateTargetSummaryMixed(t *testing.T) {
    fx := newShareServiceFixture(t)
    ctx := context.Background()
    owner := fx.owner

    // album_live scope
    a, err := fx.albumSvc.Create(ctx, "Italy 2025", owner)
    require.NoError(t, err)
    require.NoError(t, fx.albumSvc.AddMedia(ctx, a.ID, []string{"m1", "m2"}, owner))
    sLive, err := fx.shareSvc.Create(ctx, service.CreateShareRequest{
        TargetType: share.TargetAlbumLive, AlbumID: a.ID,
        Grantee: owners.Principal{Hub: "h", UserID: "g"},
    }, owner)
    require.NoError(t, err)

    // media_set scope with 3 ids
    sSet, err := fx.shareSvc.Create(ctx, service.CreateShareRequest{
        TargetType: share.TargetMediaSet, MediaIDs: []string{"m1", "m2", "m3"},
        Grantee: owners.Principal{Hub: "h", UserID: "g"},
    }, owner)
    require.NoError(t, err)

    summaries, err := fx.shareSvc.PopulateTargetSummary(ctx, []share.Scope{sLive.Scope, sSet.Scope})
    require.NoError(t, err)

    live := summaries[sLive.UUID]
    require.Equal(t, "Album: Italy 2025", live.Label)
    require.Nil(t, live.ItemCount)

    set := summaries[sSet.UUID]
    require.Equal(t, "3 photos", set.Label)
    require.NotNil(t, set.ItemCount)
    require.Equal(t, 3, *set.ItemCount)
}

func TestShareServicePopulateTargetSummaryMissingAlbum(t *testing.T) {
    fx := newShareServiceFixture(t)
    // Forge a scope row with a target_album_id that doesn't exist.
    s := share.Scope{
        UUID: "fake", Owner: fx.owner, TargetType: share.TargetAlbumLive,
        TargetAlbumID: ptr("nonexistent"),
    }
    summaries, err := fx.shareSvc.PopulateTargetSummary(context.Background(), []share.Scope{s})
    require.NoError(t, err)
    // Missing album → label still rendered with a placeholder, count nil.
    require.Equal(t, "Album: (deleted)", summaries["fake"].Label)
    require.Nil(t, summaries["fake"].ItemCount)
}

func TestShareServicePopulateTargetSummaryEmptyInput(t *testing.T) {
    fx := newShareServiceFixture(t)
    summaries, err := fx.shareSvc.PopulateTargetSummary(context.Background(), nil)
    require.NoError(t, err)
    require.Empty(t, summaries)
}
```

(`ptr` is a small generic helper; if it doesn't already exist in test scaffolding, add `func ptr[T any](v T) *T { return &v }`.)

- [ ] **Step 9: Run to verify failing**

```bash
go test ./internal/service/... -run TestShareServicePopulateTargetSummary -v
```

Expected: FAIL `shareSvc.PopulateTargetSummary undefined`.

- [ ] **Step 10: Implement `ShareService.PopulateTargetSummary`**

Append to `internal/service/share_service.go`:

```go
// PopulateTargetSummary builds the share.TargetSummary for each scope
// in scopes. For album_live scopes it looks up album names via
// album.Repo.GetNamesByIDs; for media_set scopes it counts membership
// via share.Repo.CountSharedMediaByScopes. Both lookups are batched.
// Scopes whose target_album_id is missing get a "(deleted)" placeholder
// label; scopes whose target_type is unknown are skipped.
func (s *ShareService) PopulateTargetSummary(
    ctx context.Context,
    scopes []share.Scope,
) (map[string]share.TargetSummary, error) {
    out := map[string]share.TargetSummary{}
    if len(scopes) == 0 {
        return out, nil
    }
    var albumIDs []string
    var setUUIDs []string
    for _, sc := range scopes {
        switch sc.TargetType {
        case share.TargetAlbumLive:
            if sc.TargetAlbumID != nil {
                albumIDs = append(albumIDs, *sc.TargetAlbumID)
            }
        case share.TargetMediaSet:
            setUUIDs = append(setUUIDs, sc.UUID)
        }
    }
    names, err := s.albumRepo.GetNamesByIDs(ctx, albumIDs)
    if err != nil {
        return nil, fmt.Errorf("share: PopulateTargetSummary album names: %w", err)
    }
    counts, err := s.shareRepo.CountSharedMediaByScopes(ctx, setUUIDs)
    if err != nil {
        return nil, fmt.Errorf("share: PopulateTargetSummary media counts: %w", err)
    }
    for _, sc := range scopes {
        switch sc.TargetType {
        case share.TargetAlbumLive:
            label := "Album: (deleted)"
            if sc.TargetAlbumID != nil {
                if name, ok := names[*sc.TargetAlbumID]; ok {
                    label = "Album: " + name
                }
            }
            out[sc.UUID] = share.TargetSummary{Label: label}
        case share.TargetMediaSet:
            n := counts[sc.UUID]
            label := strconv.Itoa(n) + " photos"
            if n == 1 {
                label = "1 photo"
            }
            out[sc.UUID] = share.TargetSummary{Label: label, ItemCount: &n}
        }
    }
    return out, nil
}
```

`ShareService` needs a reference to `*album.Repo`. If it doesn't already hold one, add a field `albumRepo *album.Repo` and thread it through `NewShareService`. Update the wiring in `internal/cli/server.go` (or wherever `NewShareService` is constructed) to pass the existing `album.Repo`.

`strconv` import: add if missing.

- [ ] **Step 11: Wire `target_summary` into HTTP DTOs**

Edit `internal/httpapi/shares.go`. Find the existing `scopeDTO` struct and `toScopeDTO` function. Add the type:

```go
type targetSummaryDTO struct {
    Label     string `json:"label"`
    ItemCount *int   `json:"item_count,omitempty"`
}
```

Add a field to `scopeDTO` AND `scopeDetailDTO`:

```go
TargetSummary *targetSummaryDTO `json:"target_summary,omitempty"`
```

Edit the list handler (`registerSharesList`) — after `rows, err := svc.List(...)`:

```go
summaries, err := svc.PopulateTargetSummary(ctx, rows)
if err != nil {
    return nil, translateShareError(err)
}
out.Body.Items = make([]scopeDTO, 0, len(rows))
for _, sc := range rows {
    dto := toScopeDTO(sc)
    dto.GranteeHandle = handles[sc.Grantee]
    if sum, ok := summaries[sc.UUID]; ok {
        dto.TargetSummary = &targetSummaryDTO{Label: sum.Label, ItemCount: sum.ItemCount}
    }
    out.Body.Items = append(out.Body.Items, dto)
}
```

Edit the detail handler (`registerSharesGet`) similarly — call `svc.PopulateTargetSummary(ctx, []share.Scope{det.Scope})` and populate the field on the detail DTO.

- [ ] **Step 12: Add HTTP tests for target_summary on list + detail**

Append to `internal/httpapi/shares_test.go`:

```go
func TestSharesListReturnsTargetSummaryForBothTargetTypes(t *testing.T) {
    fx := newSharesAPIFixture(t)
    // Seed: one media_set with 4 ids, one album_live.
    fx.mustCreateMediaSetScope([]string{"m1", "m2", "m3", "m4"})
    fx.mustCreateAlbumLiveScope("Italy 2025")

    body := fx.getJSON("/api/v1/shares")
    items := body["items"].([]any)
    require.Len(t, items, 2)

    var sawSet, sawLive bool
    for _, it := range items {
        m := it.(map[string]any)
        ts := m["target_summary"].(map[string]any)
        switch m["target_type"] {
        case "media_set":
            require.Equal(t, "4 photos", ts["label"])
            require.Equal(t, float64(4), ts["item_count"])
            sawSet = true
        case "album_live":
            require.Equal(t, "Album: Italy 2025", ts["label"])
            _, hasCount := ts["item_count"]
            require.False(t, hasCount, "album_live target_summary must not include item_count")
            sawLive = true
        }
    }
    require.True(t, sawSet)
    require.True(t, sawLive)
}

func TestSharesGetIncludesTargetSummary(t *testing.T) {
    fx := newSharesAPIFixture(t)
    s := fx.mustCreateMediaSetScope([]string{"m1"})
    body := fx.getJSON("/api/v1/shares/" + s.UUID)
    ts := body["target_summary"].(map[string]any)
    require.Equal(t, "1 photo", ts["label"])
    require.Equal(t, float64(1), ts["item_count"])
}
```

(`newSharesAPIFixture`, `mustCreateMediaSetScope`, `mustCreateAlbumLiveScope`, `getJSON` are test helpers already present in `shares_test.go`; if not, follow the existing pattern from `media_test.go`.)

- [ ] **Step 13: Run all share tests**

```bash
go test ./internal/share/... ./internal/service/... ./internal/httpapi/... -run 'Share|TargetSummary' -v
```

Expected: PASS.

- [ ] **Step 14: Regenerate openapi.json**

```bash
make api-generate
```

The prek hook does this automatically on commit; running it now lets us verify the `target_summary` field is in the spec before commit. Diff `openapi.json` to confirm.

- [ ] **Step 15: Commit**

```bash
git add internal/album/repo.go internal/album/repo_test.go internal/share/share.go internal/share/share_test.go internal/share/repo.go internal/share/repo_test.go internal/service/share_service.go internal/service/share_service_test.go internal/httpapi/shares.go internal/httpapi/shares_test.go internal/cli/server.go
git commit -m "feat(share): target_summary on scopeDTO + service helper"
```

(Note: `openapi.json` is gitignored per the F2.2 commit pattern; the prek hook regenerates it.)

---

## Task 3: listSharesOutput.next_offset (limit+1 pattern)

**Files:**
- Modify: `internal/httpapi/shares.go`
- Modify: `internal/httpapi/shares_test.go`

- [ ] **Step 1: Write the failing test for next_offset**

Append to `internal/httpapi/shares_test.go`:

```go
func TestSharesListNextOffsetPaginates(t *testing.T) {
    fx := newSharesAPIFixture(t)
    // Seed 5 scopes; request limit=2 to force two pages of next_offset.
    for i := 0; i < 5; i++ {
        fx.mustCreateMediaSetScope([]string{fmt.Sprintf("m%d", i)})
    }

    page1 := fx.getJSON("/api/v1/shares?limit=2&offset=0")
    require.Len(t, page1["items"].([]any), 2)
    require.Equal(t, float64(2), page1["next_offset"])

    page2 := fx.getJSON("/api/v1/shares?limit=2&offset=2")
    require.Len(t, page2["items"].([]any), 2)
    require.Equal(t, float64(4), page2["next_offset"])

    page3 := fx.getJSON("/api/v1/shares?limit=2&offset=4")
    require.Len(t, page3["items"].([]any), 1)
    _, hasNext := page3["next_offset"]
    require.False(t, hasNext, "final page must omit next_offset")
}
```

- [ ] **Step 2: Run to verify failing**

```bash
go test ./internal/httpapi/... -run TestSharesListNextOffsetPaginates -v
```

Expected: FAIL — `next_offset` field absent.

- [ ] **Step 3: Implement limit+1 pattern**

Edit `internal/httpapi/shares.go`. Find `registerSharesList` and the `listSharesOutput` body. Add field:

```go
type listSharesOutput struct {
    Body struct {
        Items      []scopeDTO `json:"items"`
        NextOffset *int       `json:"next_offset,omitempty"`
    }
}
```

Update the handler — replace the existing `filter.Limit = limit` with the limit+1 sniff:

```go
limit := clampLimit(in.Limit, sharesListDefaultLimit, sharesListMaxLimit)
filter := share.ScopeFilter{
    AlbumID:        in.AlbumID,
    Grantee:        owners.Principal{Hub: in.GranteeHub, UserID: in.GranteeUserID},
    Status:         statuses,
    IncludeSettled: in.IncludeSettled,
    Limit:          limit + 1,
    Offset:         in.Offset,
}
rows, err := svc.List(ctx, filter, caller)
if err != nil {
    return nil, translateShareError(err)
}
hasMore := len(rows) > limit
if hasMore {
    rows = rows[:limit]
}
// (existing handles + summaries population goes here, on the trimmed rows)
out := &listSharesOutput{}
if hasMore {
    next := in.Offset + limit
    out.Body.NextOffset = &next
}
```

- [ ] **Step 4: Run shares HTTP tests to verify they pass**

```bash
go test ./internal/httpapi/... -run TestSharesList -v
```

Expected: PASS, including the new pagination test and the existing list test.

- [ ] **Step 5: Regenerate openapi.json**

```bash
make api-generate
```

Diff to confirm `next_offset` is in the spec.

- [ ] **Step 6: Commit**

```bash
git add internal/httpapi/shares.go internal/httpapi/shares_test.go
git commit -m "feat(httpapi): next_offset on /api/v1/shares (limit+1)"
```

---

## Task 4: SelectionStore addAll/removeAll/hasAll

**Files:**
- Modify: `frontend/src/lib/selection/selectionStore.svelte.ts`
- Modify: `frontend/src/lib/selection/selectionStore.test.ts`

- [ ] **Step 1: Write the failing tests**

Append to `frontend/src/lib/selection/selectionStore.test.ts`:

```ts
import { describe, it, expect, beforeEach } from "vitest";
import { SelectionStore } from "./selectionStore.svelte";

describe("SelectionStore.addAll", () => {
  let s: SelectionStore;
  beforeEach(() => { s = new SelectionStore(); });

  it("adds every id and sets lastAnchor to the last id in the iterable", () => {
    s.addAll(["a", "b", "c"]);
    expect(s.ids.has("a")).toBe(true);
    expect(s.ids.has("b")).toBe(true);
    expect(s.ids.has("c")).toBe(true);
    expect(s.ids.size).toBe(3);
    expect(s.lastAnchor).toBe("c");
  });

  it("is a no-op for an empty iterable; lastAnchor unchanged", () => {
    s.toggle("seed");
    s.addAll([]);
    expect(s.lastAnchor).toBe("seed");
    expect(s.ids.size).toBe(1);
  });

  it("merges with existing selection without duplicates", () => {
    s.addAll(["a", "b"]);
    s.addAll(["b", "c"]);
    expect(s.ids.size).toBe(3);
  });
});

describe("SelectionStore.removeAll", () => {
  let s: SelectionStore;
  beforeEach(() => {
    s = new SelectionStore();
    s.addAll(["a", "b", "c", "d"]);
  });

  it("removes every id and leaves lastAnchor untouched", () => {
    const anchor = s.lastAnchor;
    s.removeAll(["b", "c"]);
    expect(s.ids.has("a")).toBe(true);
    expect(s.ids.has("b")).toBe(false);
    expect(s.ids.has("c")).toBe(false);
    expect(s.ids.has("d")).toBe(true);
    expect(s.lastAnchor).toBe(anchor);
  });

  it("is tolerant of unknown ids", () => {
    s.removeAll(["z"]);
    expect(s.ids.size).toBe(4);
  });
});

describe("SelectionStore.hasAll", () => {
  let s: SelectionStore;
  beforeEach(() => {
    s = new SelectionStore();
    s.addAll(["a", "b", "c"]);
  });

  it("returns true when every id is present", () => {
    expect(s.hasAll(["a", "b"])).toBe(true);
    expect(s.hasAll(["a", "b", "c"])).toBe(true);
  });

  it("returns false when any id is missing", () => {
    expect(s.hasAll(["a", "z"])).toBe(false);
  });

  it("returns false for empty input (so empty chunks don't render Deselect)", () => {
    expect(s.hasAll([])).toBe(false);
  });
});
```

- [ ] **Step 2: Run to verify failing**

```bash
cd frontend && bunx vitest run src/lib/selection/selectionStore.test.ts
```

Expected: FAIL — methods undefined.

- [ ] **Step 3: Implement the three methods**

Edit `frontend/src/lib/selection/selectionStore.svelte.ts`. Add (next to existing `toggle` / `range` / `clear`):

```ts
  addAll(ids: Iterable<string>): void {
    let last: string | undefined;
    for (const id of ids) {
      this.ids.add(id);
      last = id;
    }
    if (last !== undefined) {
      this.lastAnchor = last;
    }
  }

  removeAll(ids: Iterable<string>): void {
    for (const id of ids) {
      this.ids.delete(id);
    }
    // lastAnchor intentionally untouched — deselecting a group should
    // not move the range-select anchor (§13.4).
  }

  hasAll(ids: Iterable<string>): boolean {
    let any = false;
    for (const id of ids) {
      any = true;
      if (!this.ids.has(id)) return false;
    }
    return any; // empty iterable returns false
  }
```

- [ ] **Step 4: Run to verify passing**

```bash
cd frontend && bunx vitest run src/lib/selection/selectionStore.test.ts
```

Expected: PASS.

- [ ] **Step 5: Run lint**

```bash
cd frontend && bunx oxlint src/lib/selection/
```

Expected: clean.

- [ ] **Step 6: Commit**

```bash
git add frontend/src/lib/selection/selectionStore.svelte.ts frontend/src/lib/selection/selectionStore.test.ts
git commit -m "feat(selection): addAll/removeAll/hasAll for group-select"
```

---

## Task 5: MonthChunk headerAction prop

**Files:**
- Modify: `frontend/src/lib/grid/MonthChunk.svelte`

- [ ] **Step 1: Write the failing test**

Create `frontend/src/lib/grid/MonthChunk.test.ts`:

```ts
import { render } from "@testing-library/svelte";
import { describe, it, expect } from "vitest";
import MonthChunk from "./MonthChunk.svelte";
import HeaderActionFixture from "./HeaderActionFixture.svelte";

describe("MonthChunk headerAction slot", () => {
  it("renders the headerAction snippet inside the day-header", () => {
    const { container } = render(HeaderActionFixture, {
      props: { label: "April 2024" },
    });
    const header = container.querySelector("header.day-header");
    expect(header).not.toBeNull();
    expect(header!.querySelector("button.test-action")).not.toBeNull();
    expect(header!.textContent).toContain("April 2024");
    expect(header!.textContent).toContain("ACT");
  });

  it("does not render header at all when label is undefined (flat mode)", () => {
    const { container } = render(HeaderActionFixture, {
      props: { label: undefined },
    });
    expect(container.querySelector("header.day-header")).toBeNull();
  });
});
```

Create the fixture `frontend/src/lib/grid/HeaderActionFixture.svelte`:

```svelte
<script lang="ts">
  import MonthChunk from "./MonthChunk.svelte";
  let { label }: { label?: string } = $props();
</script>

<MonthChunk
  items={[]}
  {label}
  options={{ containerWidth: 800, targetRowHeight: 200, gap: 4 }}
>
  {#snippet headerAction()}
    <button class="test-action">ACT</button>
  {/snippet}
  {#snippet renderCell()}<span></span>{/snippet}
</MonthChunk>
```

- [ ] **Step 2: Run to verify failing**

```bash
cd frontend && bunx vitest run src/lib/grid/MonthChunk.test.ts
```

Expected: FAIL — `headerAction` prop unknown.

- [ ] **Step 3: Add `headerAction` prop to MonthChunk**

Edit `frontend/src/lib/grid/MonthChunk.svelte`. Update the `$props()` destructuring and the header markup:

```svelte
<script lang="ts">
  import type { Snippet } from "svelte";
  import { computeMonthLayout, type LayoutOptions } from "./layout";
  import type { MediaLite } from "./types";

  let { items, options, label, renderCell, headerAction }: {
    items: MediaLite[];
    options: LayoutOptions;
    label?: string;
    renderCell?: Snippet<[MediaLite, { x: number; y: number; w: number; h: number }]>;
    headerAction?: Snippet;
  } = $props();

  let computed = $derived(computeMonthLayout(items, options, !!label));
</script>

<section class="month-chunk">
  {#if label}
    <header class="day-header">
      <span class="label">{label}</span>
      {#if headerAction}
        <span class="header-action">{@render headerAction()}</span>
      {/if}
    </header>
  {/if}
  <div class="cells" style="position: relative; height: {computed.layout.totalHeight}px;">
    {#each computed.layout.rows as row (row.y)}
      <!-- existing row markup -->
    {/each}
  </div>
</section>
```

(The existing row markup inside `.cells` is unchanged — preserve it verbatim.)

Add CSS to position `.header-action` to the right:

```css
  .day-header {
    /* existing rules preserved */
    display: flex;
    align-items: center;
    justify-content: space-between;
  }
  .header-action {
    margin-left: auto;
  }
```

- [ ] **Step 4: Run to verify passing**

```bash
cd frontend && bunx vitest run src/lib/grid/MonthChunk.test.ts
```

Expected: PASS.

- [ ] **Step 5: Run typecheck**

```bash
cd frontend && bunx tsc --noEmit
```

Expected: clean.

- [ ] **Step 6: Commit**

```bash
git add frontend/src/lib/grid/MonthChunk.svelte frontend/src/lib/grid/MonthChunk.test.ts frontend/src/lib/grid/HeaderActionFixture.svelte
git commit -m "feat(grid): MonthChunk headerAction snippet"
```

---

## Task 6: VirtualGrid timelineChrome + label suppression

**Files:**
- Modify: `frontend/src/lib/grid/VirtualGrid.svelte`

- [ ] **Step 1: Write the failing test**

Create `frontend/src/lib/grid/VirtualGrid.flat.test.ts`:

```ts
import { render } from "@testing-library/svelte";
import { describe, it, expect } from "vitest";
import VirtualGrid from "./VirtualGrid.svelte";

describe("VirtualGrid timelineChrome=false", () => {
  const months = [{ key: "album:abc", items: [
    { id: "m1", aspect: 1.5, thumbUrl: "/t/m1" },
    { id: "m2", aspect: 1.0, thumbUrl: "/t/m2" },
  ]}];

  it("does not render StickyMonthBar in flat mode", () => {
    const { container } = render(VirtualGrid, {
      props: { months, onLoadMore: () => {}, targetRowHeight: 200, timelineChrome: false },
    });
    expect(container.querySelector(".sticky-month-bar")).toBeNull();
  });

  it("does not render YearScrubber in flat mode", () => {
    const { container } = render(VirtualGrid, {
      props: { months, onLoadMore: () => {}, targetRowHeight: 200, timelineChrome: false },
    });
    expect(container.querySelector(".year-scrubber")).toBeNull();
  });

  it("does not render the MonthChunk header in flat mode", () => {
    const { container } = render(VirtualGrid, {
      props: { months, onLoadMore: () => {}, targetRowHeight: 200, timelineChrome: false },
    });
    expect(container.querySelector("header.day-header")).toBeNull();
  });

  it("still renders chrome when timelineChrome defaults to true", () => {
    const { container } = render(VirtualGrid, {
      props: { months, onLoadMore: () => {}, targetRowHeight: 200 },
    });
    expect(container.querySelector(".sticky-month-bar")).not.toBeNull();
  });
});
```

- [ ] **Step 2: Run to verify failing**

```bash
cd frontend && bunx vitest run src/lib/grid/VirtualGrid.flat.test.ts
```

Expected: FAIL — chrome elements still present, header still rendered.

- [ ] **Step 3: Add `timelineChrome` prop and gate chrome + label**

Edit `frontend/src/lib/grid/VirtualGrid.svelte`. Update prop destructuring and template:

```svelte
<script lang="ts">
  import type { Snippet } from "svelte";
  import StickyMonthBar from "../components/StickyMonthBar.svelte";
  import YearScrubber from "../components/YearScrubber.svelte";
  import MonthChunk from "./MonthChunk.svelte";
  import type { Month } from "../media/mediaStore.svelte";

  let {
    months,
    onLoadMore,
    targetRowHeight = 200,
    timelineChrome = true,
    headerAction,
  }: {
    months: Month[];
    onLoadMore: () => void;
    targetRowHeight?: number;
    timelineChrome?: boolean;
    headerAction?: Snippet<[Month]>;
  } = $props();

  // (existing derivations, refs, ResizeObserver, IntersectionObserver
  //  remain unchanged — preserve them.)
</script>

<div class="virtual-grid" bind:this={containerEl}>
  {#if timelineChrome}
    <StickyMonthBar label={activeStickyLabel} />
    <YearScrubber {months} onJump={jumpTo} />
  {/if}
  {#each months as month (month.key)}
    {@const outerHeaderAction = headerAction}
    <MonthChunk
      items={toLite(month.items)}
      label={timelineChrome ? month.key : undefined}
      options={{ containerWidth, targetRowHeight, gap: 4 }}
    >
      {#if outerHeaderAction}
        {#snippet headerAction()}{@render outerHeaderAction(month)}{/snippet}
      {/if}
      {#snippet renderCell(m, rect)}
        <!-- existing cell render block -->
      {/snippet}
    </MonthChunk>
  {/each}
  <div bind:this={sentinel} style="height:1px"></div>
</div>
```

(The existing cell render block, sentinel observer, and sticky-bar tracking effect are preserved verbatim.)

Note: the current `Month` type only has `{key, items}`; there is no `label` field. Tasks 7/8 pass `month.key` straight through as the human label (it's "YYYY-MM", which is acceptable for v1). A future polish task could add a friendly month-name formatter; v1 ships with the raw key.

- [ ] **Step 4: Run to verify passing**

```bash
cd frontend && bunx vitest run src/lib/grid/VirtualGrid.flat.test.ts
```

Expected: PASS.

- [ ] **Step 5: Run typecheck and lint**

```bash
cd frontend && bunx tsc --noEmit && bunx oxlint src/lib/grid/
```

Expected: clean.

- [ ] **Step 6: Commit**

```bash
git add frontend/src/lib/grid/VirtualGrid.svelte frontend/src/lib/grid/VirtualGrid.flat.test.ts
git commit -m "feat(grid): VirtualGrid timelineChrome + headerAction forwarding"
```

---

## Task 7: GroupSelectButton + Library wiring

**Files:**
- Create: `frontend/src/lib/components/GroupSelectButton.svelte`
- Create: `frontend/src/lib/components/GroupSelectButton.test.ts`
- Modify: `frontend/src/routes/Library.svelte`

- [ ] **Step 1: Write the failing GroupSelectButton tests**

Create `frontend/src/lib/components/GroupSelectButton.test.ts`:

```ts
import { render, fireEvent } from "@testing-library/svelte";
import { describe, it, expect, beforeEach } from "vitest";
import GroupSelectButton from "./GroupSelectButton.svelte";
import { selection } from "../selection/selectionStore.svelte";

describe("GroupSelectButton", () => {
  beforeEach(() => { selection.clear(); });

  it("renders 'Select group' when no ids are selected", () => {
    const { getByRole } = render(GroupSelectButton, {
      props: { ids: ["a", "b"], label: "April 2024" },
    });
    const btn = getByRole("button");
    expect(btn.textContent?.trim()).toBe("Select group");
    expect(btn.getAttribute("aria-label")).toBe("Select 2 photos in April 2024");
  });

  it("renders 'Deselect group' when all ids are already selected", () => {
    selection.addAll(["a", "b"]);
    const { getByRole } = render(GroupSelectButton, {
      props: { ids: ["a", "b"], label: "April 2024" },
    });
    const btn = getByRole("button");
    expect(btn.textContent?.trim()).toBe("Deselect group");
    expect(btn.getAttribute("aria-label")).toBe("Deselect 2 photos in April 2024");
  });

  it("clicking adds all ids when none are selected", async () => {
    const { getByRole } = render(GroupSelectButton, {
      props: { ids: ["a", "b", "c"], label: "April" },
    });
    await fireEvent.click(getByRole("button"));
    expect(selection.ids.has("a")).toBe(true);
    expect(selection.ids.has("b")).toBe(true);
    expect(selection.ids.has("c")).toBe(true);
    expect(selection.lastAnchor).toBe("c");
  });

  it("clicking removes all ids when every id is already selected", async () => {
    selection.addAll(["a", "b"]);
    selection.toggle("anchor"); // sets lastAnchor to "anchor"
    selection.addAll(["a", "b"]); // re-adds; lastAnchor moves to "b"
    const { getByRole } = render(GroupSelectButton, {
      props: { ids: ["a", "b"], label: "April" },
    });
    await fireEvent.click(getByRole("button"));
    expect(selection.ids.has("a")).toBe(false);
    expect(selection.ids.has("b")).toBe(false);
    // lastAnchor must NOT change on removeAll.
    expect(selection.lastAnchor).toBe("b");
  });
});
```

- [ ] **Step 2: Run to verify failing**

```bash
cd frontend && bunx vitest run src/lib/components/GroupSelectButton.test.ts
```

Expected: FAIL — component does not exist.

- [ ] **Step 3: Implement GroupSelectButton**

Create `frontend/src/lib/components/GroupSelectButton.svelte`:

```svelte
<script lang="ts">
  import { selection } from "../selection/selectionStore.svelte";
  let { ids, label }: { ids: string[]; label: string } = $props();
  const allSelected = $derived(selection.hasAll(ids));
  function toggle() {
    if (allSelected) {
      selection.removeAll(ids);
    } else {
      selection.addAll(ids);
    }
  }
</script>

<button
  type="button"
  class="group-select"
  onclick={toggle}
  aria-label={allSelected
    ? `Deselect ${ids.length} photos in ${label}`
    : `Select ${ids.length} photos in ${label}`}
>
  {allSelected ? "Deselect group" : "Select group"}
</button>

<style>
  .group-select {
    background: transparent;
    border: 1px solid var(--border);
    color: var(--text-muted);
    padding: 2px 8px;
    font-size: 11px;
    border-radius: 4px;
    cursor: pointer;
  }
  .group-select:hover {
    color: var(--text);
    border-color: var(--text-muted);
  }
  .group-select:focus-visible {
    outline: 2px solid var(--accent);
    outline-offset: 2px;
  }
</style>
```

- [ ] **Step 4: Run to verify passing**

```bash
cd frontend && bunx vitest run src/lib/components/GroupSelectButton.test.ts
```

Expected: PASS.

- [ ] **Step 5: Wire GroupSelectButton through Library**

Edit `frontend/src/routes/Library.svelte`. Add the snippet on the existing VirtualGrid:

```svelte
<script lang="ts">
  import VirtualGrid from "../lib/grid/VirtualGrid.svelte";
  import GroupSelectButton from "../lib/components/GroupSelectButton.svelte";
  import type { MediaStore } from "../lib/media/mediaStore.svelte";
  import { DensityStore } from "../lib/density/densityStore.svelte";
  import DensityControl from "../lib/components/DensityControl.svelte";
  import { api } from "../lib/api/client";

  let { mediaStore }: { mediaStore: MediaStore } = $props();

  const density = new DensityStore(api, "library");
  density.load();
</script>

<header style="display:flex; justify-content: flex-end; padding: 6px 12px;">
  <DensityControl store={density} />
</header>

<VirtualGrid
  months={mediaStore.months}
  onLoadMore={() => mediaStore.loadMore()}
  targetRowHeight={density.targetRowHeight}
>
  {#snippet headerAction(month)}
    <GroupSelectButton
      ids={month.items.map((it) => it.id)}
      label={month.key}
    />
  {/snippet}
</VirtualGrid>

{#if mediaStore.loading}
  <div style="padding:12px; color: var(--text-muted)">Loading…</div>
{/if}
{#if mediaStore.months.length === 0 && !mediaStore.loading}
  <div style="padding:24px; color: var(--text-secondary)">No photos yet.</div>
{/if}
```

- [ ] **Step 6: Verify Library renders the button via the existing dev server**

```bash
make dev
# Open http://localhost:8080/library and confirm a "Select group" button
# appears on each month chunk's header. Click one; selection count
# in ActionBar (top of page) reflects all photos in that month.
```

(If `make dev` is not running, start it; UI verification is required per CLAUDE.md "test the UI in a browser before reporting complete".)

- [ ] **Step 7: Commit**

```bash
git add frontend/src/lib/components/GroupSelectButton.svelte frontend/src/lib/components/GroupSelectButton.test.ts frontend/src/routes/Library.svelte
git commit -m "feat(library): GroupSelectButton on month chunks"
```

---

## Task 8: Sessions wiring (direct on MonthChunk)

**Files:**
- Modify: `frontend/src/routes/Sessions.svelte`

- [ ] **Step 1: Add GroupSelectButton snippet directly on MonthChunk**

Edit `frontend/src/routes/Sessions.svelte`. Add the import and the snippet on each MonthChunk in the existing `{#each sessions}` loop:

```svelte
<script lang="ts">
  import MonthChunk from "../lib/grid/MonthChunk.svelte";
  import GroupSelectButton from "../lib/components/GroupSelectButton.svelte";
  // (existing imports preserved)

  // (existing $derived + $effect blocks preserved verbatim)
</script>

<header style="display:flex; justify-content: flex-end; padding: 6px 12px;">
  <DensityControl store={density} />
</header>

<div bind:this={containerEl} style="padding: 8px;">
  {#each sessions as s (s.id)}
    {@const first = s.items[0]}
    {#if first}
      <MonthChunk
        items={s.items.map((m) => ({ id: m.id, aspect: m.aspect, thumbUrl: m.thumbUrl }))}
        label={`${first.taken.toUTCString().slice(0, 16)} · ${s.items.length} photos`}
        options={{ containerWidth, targetRowHeight: density.targetRowHeight, gap: 4 }}
      >
        {#snippet headerAction()}
          <GroupSelectButton
            ids={s.items.map((m) => m.id)}
            label={`${first.taken.toUTCString().slice(0, 16)} session`}
          />
        {/snippet}
        {#snippet renderCell(m)}
          <MediaCell
            media={m}
            selected={selection.ids.has(m.id)}
            onCellClick={(e) => handleCellClick(e, m.id)}
          />
        {/snippet}
      </MonthChunk>
    {/if}
  {/each}
  <div bind:this={sentinel} style="height:1px"></div>
</div>
```

- [ ] **Step 2: Verify in browser**

```bash
make dev
# Open http://localhost:8080/sessions and confirm a "Select group" button
# appears on each session header. Click one; ActionBar shows N selected.
```

- [ ] **Step 3: Commit**

```bash
git add frontend/src/routes/Sessions.svelte
git commit -m "feat(sessions): GroupSelectButton on session headers"
```

---

## Task 9: Sidebar grouping (BROWSE / CURATE / MANAGE)

**Files:**
- Modify: `frontend/src/lib/components/Sidebar.svelte`

- [ ] **Step 1: Write the failing test**

Create `frontend/src/lib/components/Sidebar.test.ts`:

```ts
import { render } from "@testing-library/svelte";
import { describe, it, expect } from "vitest";
import Sidebar from "./Sidebar.svelte";

describe("Sidebar grouped entries", () => {
  it("renders BROWSE / CURATE / MANAGE group headers in order", () => {
    const { container } = render(Sidebar, { props: { active: "library" } });
    const headers = Array.from(container.querySelectorAll(".group-header"))
      .map((el) => el.textContent?.trim());
    expect(headers).toEqual(["BROWSE", "CURATE", "MANAGE"]);
  });

  it("renders Library + Sessions under BROWSE", () => {
    const { container } = render(Sidebar, { props: { active: "library" } });
    const browseGroup = container.querySelector(".group[data-group='browse']");
    expect(browseGroup?.textContent).toContain("Library");
    expect(browseGroup?.textContent).toContain("Sessions");
  });

  it("renders Albums under CURATE", () => {
    const { container } = render(Sidebar, { props: { active: "albums" } });
    const curate = container.querySelector(".group[data-group='curate']");
    expect(curate?.textContent).toContain("Albums");
  });

  it("renders Shares under MANAGE", () => {
    const { container } = render(Sidebar, { props: { active: "shares" } });
    const manage = container.querySelector(".group[data-group='manage']");
    expect(manage?.textContent).toContain("Shares");
  });

  it("highlights the active entry", () => {
    const { container } = render(Sidebar, { props: { active: "albums" } });
    const active = container.querySelector("a.active");
    expect(active?.textContent?.trim()).toBe("Albums");
  });
});
```

- [ ] **Step 2: Run to verify failing**

```bash
cd frontend && bunx vitest run src/lib/components/Sidebar.test.ts
```

Expected: FAIL — group structure doesn't exist yet.

- [ ] **Step 3: Update Sidebar to render grouped entries**

Edit `frontend/src/lib/components/Sidebar.svelte`:

```svelte
<script lang="ts">
  import { handleInternalLinkClick } from "../router/router.svelte";
  let { active = "" }: { active?: string } = $props();

  const groups = [
    {
      key: "browse",
      label: "BROWSE",
      entries: [
        { id: "library", label: "Library", href: "/library" },
        { id: "sessions", label: "Sessions", href: "/sessions" },
      ],
    },
    {
      key: "curate",
      label: "CURATE",
      entries: [{ id: "albums", label: "Albums", href: "/albums" }],
    },
    {
      key: "manage",
      label: "MANAGE",
      entries: [{ id: "shares", label: "Shares", href: "/shares" }],
    },
  ];
</script>

<nav>
  {#each groups as group (group.key)}
    <div class="group" data-group={group.key}>
      <div class="group-header">{group.label}</div>
      {#each group.entries as entry (entry.id)}
        <a
          href={entry.href}
          class:active={active === entry.id}
          onclick={(e) => handleInternalLinkClick(e, entry.href)}
        >
          {entry.label}
        </a>
      {/each}
    </div>
  {/each}
</nav>

<style>
  nav { padding: 12px 8px; display: flex; flex-direction: column; gap: 12px; }
  .group { display: flex; flex-direction: column; gap: 2px; }
  .group-header {
    text-transform: uppercase;
    font-size: 11px;
    color: var(--text-muted);
    margin: 4px 4px 2px;
    letter-spacing: 0.04em;
  }
  a {
    display: block;
    padding: 6px 8px;
    color: var(--text);
    text-decoration: none;
    border-radius: 4px;
  }
  a:hover { background: var(--bg-elevated); }
  a.active { background: var(--bg-elevated); color: var(--text); font-weight: 600; }
</style>
```

- [ ] **Step 4: Run to verify passing**

```bash
cd frontend && bunx vitest run src/lib/components/Sidebar.test.ts
```

Expected: PASS.

- [ ] **Step 5: Update App.svelte sidebar `active` derivation**

`App.svelte` already maps `route.name` → `active`. Confirm `albums` and `shares` are in the mapping for `/albums(/.*)?` and `/shares` (sub-routes inherit). If not, add:

```ts
const active = $derived(
  route.name === "media" ? "library" :
  route.name === "albums" || route.name.startsWith("albums.") ? "albums" :
  route.name === "shares" ? "shares" :
  route.name === "notfound" ? "" :
  route.name
);
```

- [ ] **Step 6: Verify in browser**

```bash
make dev
# Open http://localhost:8080/library — sidebar shows BROWSE, CURATE,
# MANAGE groups. Click between Library / Sessions / Albums / Shares
# (Albums and Shares will 404 until later tasks; sidebar grouping
# is what's being verified here).
```

- [ ] **Step 7: Commit**

```bash
git add frontend/src/lib/components/Sidebar.svelte frontend/src/lib/components/Sidebar.test.ts frontend/src/App.svelte
git commit -m "feat(sidebar): BROWSE / CURATE / MANAGE groups"
```

---

## Task 10: AlbumsStore (data layer)

**Files:**
- Create: `frontend/src/lib/albums/albumsStore.svelte.ts`
- Create: `frontend/src/lib/albums/albumsStore.test.ts`

- [ ] **Step 1: Write the failing tests**

Create `frontend/src/lib/albums/albumsStore.test.ts`:

```ts
import { describe, it, expect, vi } from "vitest";
import { AlbumsStore } from "./albumsStore.svelte";

function fakeClient(responses: Array<{ data?: any; error?: any }>) {
  const calls: Array<{ method: string; path: string; opts: any }> = [];
  let i = 0;
  const handler = vi.fn(async (path: string, opts: any = {}) => {
    calls.push({ method: "called", path, opts });
    return responses[i++] ?? { data: { items: [] } };
  });
  return {
    GET: handler,
    POST: handler,
    PATCH: handler,
    DELETE: handler,
    calls,
  };
}

describe("AlbumsStore.loadInitial", () => {
  it("populates albums and tracks pagination via next_offset", async () => {
    const client = fakeClient([
      {
        data: {
          items: [
            { id: "a1", name: "Italy", item_count: 12, cover: { media_id: "m1", thumb_version: 1 }, created_at: "2025-01-01", updated_at: "2025-04-01" },
            { id: "a2", name: "Family", item_count: 0, cover: null, created_at: "2024-06-01", updated_at: "2024-06-01" },
          ],
          next_offset: 100,
        },
      },
    ]);
    const store = new AlbumsStore(client as any);
    await store.loadInitial();
    expect(store.albums.length).toBe(2);
    expect(store.albums[0].name).toBe("Italy");
    expect(store.exhausted).toBe(false);
  });

  it("marks exhausted when next_offset is null", async () => {
    const client = fakeClient([{ data: { items: [], next_offset: null } }]);
    const store = new AlbumsStore(client as any);
    await store.loadInitial();
    expect(store.exhausted).toBe(true);
  });
});

describe("AlbumsStore.create", () => {
  it("POSTs the new album then refetches page 1", async () => {
    const client = fakeClient([
      { data: { id: "new", name: "Trip", item_count: 0, cover: null, created_at: "2026-04-28", updated_at: "2026-04-28" } },
      { data: { items: [{ id: "new", name: "Trip", item_count: 0, cover: null, created_at: "2026-04-28", updated_at: "2026-04-28" }], next_offset: null } },
    ]);
    const store = new AlbumsStore(client as any);
    await store.create("Trip");
    expect(store.albums.length).toBe(1);
    expect(store.albums[0].name).toBe("Trip");
    expect(client.calls[0].path).toBe("/api/v1/albums"); // POST
    expect(client.calls[1].path).toBe("/api/v1/albums"); // GET refetch
  });

  it("rejects empty / overlong names client-side without POST", async () => {
    const client = fakeClient([]);
    const store = new AlbumsStore(client as any);
    await expect(store.create("")).rejects.toThrow();
    await expect(store.create("x".repeat(201))).rejects.toThrow();
    expect(client.calls).toHaveLength(0);
  });
});

describe("AlbumsStore.rename", () => {
  it("PATCHes and merges returned DTO into the list", async () => {
    const client = fakeClient([
      { data: { items: [{ id: "a1", name: "Old", item_count: 0, cover: null, created_at: "x", updated_at: "x" }], next_offset: null } },
      { data: { id: "a1", name: "New", item_count: 0, cover: null, created_at: "x", updated_at: "y" } },
    ]);
    const store = new AlbumsStore(client as any);
    await store.loadInitial();
    await store.rename("a1", "New");
    expect(store.byId("a1")?.name).toBe("New");
  });
});

describe("AlbumsStore.delete", () => {
  it("DELETEs and removes from list on 204", async () => {
    const client = fakeClient([
      { data: { items: [{ id: "a1", name: "X", item_count: 0, cover: null, created_at: "x", updated_at: "x" }], next_offset: null } },
      { data: null },
    ]);
    const store = new AlbumsStore(client as any);
    await store.loadInitial();
    await store.delete("a1");
    expect(store.byId("a1")).toBeUndefined();
  });

  it("rethrows 409 errors so caller can surface the live-scopes toast", async () => {
    const client = fakeClient([
      { data: { items: [{ id: "a1", name: "X", item_count: 0, cover: null, created_at: "x", updated_at: "x" }], next_offset: null } },
      { error: { status: 409, message: "album has outstanding broker grants" } },
    ]);
    const store = new AlbumsStore(client as any);
    await store.loadInitial();
    await expect(store.delete("a1")).rejects.toMatchObject({ status: 409 });
    expect(store.byId("a1")).not.toBeUndefined();
  });
});
```

- [ ] **Step 2: Run to verify failing**

```bash
cd frontend && bunx vitest run src/lib/albums/albumsStore.test.ts
```

Expected: FAIL — store does not exist.

- [ ] **Step 3: Implement AlbumsStore**

Create `frontend/src/lib/albums/albumsStore.svelte.ts`:

```ts
import type { Client } from "../api/client";

export type AlbumListItem = {
  id: string;
  name: string;
  created_at: string;
  updated_at: string;
  item_count: number;
  cover: { media_id: string; thumb_version: number } | null;
};

export class AlbumsStore {
  albums = $state<AlbumListItem[]>([]);
  loading = $state(false);
  exhausted = $state(false);
  private nextOffset: number | null = 0;

  constructor(private client: Pick<Client, "GET" | "POST" | "PATCH" | "DELETE">) {}

  async loadInitial(): Promise<void> {
    this.albums = [];
    this.nextOffset = 0;
    this.exhausted = false;
    await this.loadMore();
  }

  async loadMore(): Promise<void> {
    if (this.loading || this.exhausted) return;
    this.loading = true;
    try {
      const res = await this.client.GET("/api/v1/albums", {
        params: { query: { limit: 100, offset: this.nextOffset ?? 0 } } as never,
      });
      if (res.error || !res.data) return;
      const data = res.data as { items?: AlbumListItem[]; next_offset?: number | null };
      const items = data.items ?? [];
      this.albums = [...this.albums, ...items];
      const next = data.next_offset ?? null;
      this.nextOffset = next;
      if (next === null) this.exhausted = true;
    } finally {
      this.loading = false;
    }
  }

  async create(name: string): Promise<void> {
    const trimmed = name.trim();
    if (trimmed.length === 0) throw new Error("Name is required");
    if (trimmed.length > 200) throw new Error("Name exceeds 200 characters");
    const res = await this.client.POST("/api/v1/albums", { body: { name: trimmed } as never });
    if (res.error) throw res.error;
    // Refetch page 1 so backend ordering / cover derivation is honored.
    await this.loadInitial();
  }

  async rename(id: string, name: string): Promise<void> {
    const trimmed = name.trim();
    if (trimmed.length === 0) throw new Error("Name is required");
    if (trimmed.length > 200) throw new Error("Name exceeds 200 characters");
    const res = await this.client.PATCH("/api/v1/albums/{id}", {
      params: { path: { id } } as never,
      body: { name: trimmed } as never,
    });
    if (res.error) throw res.error;
    const updated = res.data as AlbumListItem;
    const idx = this.albums.findIndex((a) => a.id === id);
    if (idx >= 0) this.albums[idx] = { ...this.albums[idx], ...updated };
  }

  async delete(id: string): Promise<void> {
    const res = await this.client.DELETE("/api/v1/albums/{id}", {
      params: { path: { id } } as never,
    });
    if (res.error) throw res.error;
    this.albums = this.albums.filter((a) => a.id !== id);
  }

  byId(id: string): AlbumListItem | undefined {
    return this.albums.find((a) => a.id === id);
  }
}
```

- [ ] **Step 4: Run to verify passing**

```bash
cd frontend && bunx vitest run src/lib/albums/albumsStore.test.ts
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/lib/albums/albumsStore.svelte.ts frontend/src/lib/albums/albumsStore.test.ts
git commit -m "feat(albums): AlbumsStore data layer"
```

---

## Task 11: /albums route + AlbumGrid + new-album modal

**Files:**
- Create: `frontend/src/routes/AlbumsIndex.svelte`
- Create: `frontend/src/lib/components/AlbumGrid.svelte`
- Create: `frontend/src/lib/components/NewAlbumForm.svelte`
- Modify: `frontend/src/lib/router/router.svelte.ts`
- Modify: `frontend/src/App.svelte`

- [ ] **Step 1: Add `/albums` route to the router**

Edit `frontend/src/lib/router/router.svelte.ts`. Add to the route table:

```ts
{ name: "albums", pattern: /^\/albums\/?$/, params: () => ({}) },
```

(Keep existing routes; add this one alongside `library` / `sessions`.)

- [ ] **Step 2: Add a router test for the new route**

Append to `frontend/src/lib/router/router.test.ts`:

```ts
it("matches /albums to the albums route", () => {
  const r = parseRoute("/albums");
  expect(r.name).toBe("albums");
});
```

- [ ] **Step 3: Run to verify**

```bash
cd frontend && bunx vitest run src/lib/router/router.test.ts
```

Expected: PASS.

- [ ] **Step 4: Implement NewAlbumForm**

Create `frontend/src/lib/components/NewAlbumForm.svelte`:

```svelte
<script lang="ts">
  let {
    onCreate,
    onCancel,
  }: {
    onCreate: (name: string) => Promise<void>;
    onCancel?: () => void;
  } = $props();

  let name = $state("");
  let error = $state<string | null>(null);
  let pending = $state(false);

  const trimmed = $derived(name.trim());
  const valid = $derived(trimmed.length >= 1 && trimmed.length <= 200);

  async function submit(e: Event) {
    e.preventDefault();
    if (!valid || pending) return;
    pending = true;
    error = null;
    try {
      await onCreate(trimmed);
      name = "";
    } catch (e: any) {
      error = e?.message ?? "Failed to create album";
    } finally {
      pending = false;
    }
  }
</script>

<form onsubmit={submit} class="new-album-form">
  <label>
    <span>Name</span>
    <input
      type="text"
      bind:value={name}
      maxlength="200"
      autofocus
      placeholder="Album name"
      aria-invalid={error !== null}
    />
  </label>
  {#if error}<div class="error" role="alert">{error}</div>{/if}
  <div class="actions">
    {#if onCancel}<button type="button" onclick={onCancel} disabled={pending}>Cancel</button>{/if}
    <button type="submit" disabled={!valid || pending}>{pending ? "Creating…" : "Create"}</button>
  </div>
</form>

<style>
  .new-album-form { display: flex; flex-direction: column; gap: 12px; }
  label { display: flex; flex-direction: column; gap: 4px; }
  .error { color: var(--danger); font-size: 13px; }
  .actions { display: flex; gap: 8px; justify-content: flex-end; }
</style>
```

- [ ] **Step 5: Implement AlbumGrid**

Create `frontend/src/lib/components/AlbumGrid.svelte`:

```svelte
<script lang="ts">
  import { handleInternalLinkClick } from "../router/router.svelte";
  import type { AlbumListItem } from "../albums/albumsStore.svelte";

  let { albums }: { albums: AlbumListItem[] } = $props();

  function thumbUrl(a: AlbumListItem): string | null {
    if (!a.cover) return null;
    return `/api/v1/media/${a.cover.media_id}/thumb?size=grid&v=${a.cover.thumb_version}`;
  }
</script>

<div class="album-grid">
  {#each albums as a (a.id)}
    <a
      class="tile"
      href={`/albums/${a.id}`}
      onclick={(e) => handleInternalLinkClick(e, `/albums/${a.id}`)}
    >
      {#if thumbUrl(a)}
        <img class="cover" src={thumbUrl(a)} alt={a.name} loading="lazy" />
      {:else}
        <div class="cover placeholder" aria-hidden="true">
          <span class="icon">▢</span>
        </div>
      {/if}
      <div class="meta">
        <div class="name" title={a.name}>{a.name}</div>
        <div class="count">{a.item_count} {a.item_count === 1 ? "photo" : "photos"}</div>
      </div>
    </a>
  {/each}
</div>

<style>
  .album-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(180px, 1fr));
    gap: 12px;
    padding: 12px;
  }
  .tile {
    display: flex;
    flex-direction: column;
    text-decoration: none;
    color: var(--text);
  }
  .cover {
    aspect-ratio: 1 / 1;
    object-fit: cover;
    width: 100%;
    border-radius: 6px;
    background: var(--bg-elevated);
  }
  .placeholder {
    display: flex;
    align-items: center;
    justify-content: center;
    border: 1px solid var(--border);
    color: var(--text-muted);
  }
  .placeholder .icon { font-size: 32px; }
  .meta { padding: 6px 4px; }
  .name {
    font-size: 14px;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .count { font-size: 12px; color: var(--text-muted); }
</style>
```

- [ ] **Step 6: Implement AlbumsIndex route**

Create `frontend/src/routes/AlbumsIndex.svelte`:

```svelte
<script lang="ts">
  import AlbumGrid from "../lib/components/AlbumGrid.svelte";
  import NewAlbumForm from "../lib/components/NewAlbumForm.svelte";
  import type { AlbumsStore } from "../lib/albums/albumsStore.svelte";

  let { albumsStore }: { albumsStore: AlbumsStore } = $props();

  let modalOpen = $state(false);

  // The mount effect needs both guards. `albums.length === 0` ensures
  // we don't re-run loadInitial after a successful first fetch (without
  // it, a second response with `next_offset` set leaves `exhausted=false`
  // and the effect retriggers when loading flips back to false). And
  // `!exhausted` ensures an account with truly zero albums doesn't loop
  // (length stays 0; exhausted=true after the first response, gating
  // the effect). Both guards together cover the populated-paginated AND
  // empty-account cases without an extra "hasLoadedInitial" flag.
  $effect(() => {
    if (
      albumsStore.albums.length === 0 &&
      !albumsStore.loading &&
      !albumsStore.exhausted
    ) {
      albumsStore.loadInitial();
    }
  });

  let sentinel: HTMLDivElement | null = $state(null);
  $effect(() => {
    if (!sentinel) return;
    const io = new IntersectionObserver((entries) => {
      if (entries[0]?.isIntersecting) albumsStore.loadMore();
    }, { rootMargin: "200px 0px" });
    io.observe(sentinel);
    return () => io.disconnect();
  });

  async function onCreate(name: string) {
    await albumsStore.create(name);
    modalOpen = false;
  }
</script>

<header class="page-header">
  <h1>Albums</h1>
  <button type="button" onclick={() => (modalOpen = true)}>+ New Album</button>
</header>

{#if albumsStore.albums.length > 0}
  <AlbumGrid albums={albumsStore.albums} />
{:else if !albumsStore.loading}
  <div class="empty">
    <p>No albums yet</p>
    <button type="button" onclick={() => (modalOpen = true)}>Create your first album</button>
  </div>
{/if}

{#if albumsStore.loading}<div class="loading">Loading…</div>{/if}
<div bind:this={sentinel} style="height:1px"></div>

{#if modalOpen}
  <div class="modal-backdrop" onclick={() => (modalOpen = false)}>
    <div class="modal" onclick={(e) => e.stopPropagation()}>
      <h2>New album</h2>
      <NewAlbumForm {onCreate} onCancel={() => (modalOpen = false)} />
    </div>
  </div>
{/if}

<style>
  .page-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 12px 16px;
    border-bottom: 1px solid var(--border);
  }
  .page-header h1 { font-size: 18px; margin: 0; }
  .empty {
    padding: 64px 16px;
    text-align: center;
    color: var(--text-secondary);
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 12px;
  }
  .loading { padding: 12px; color: var(--text-muted); }
  .modal-backdrop {
    position: fixed; inset: 0; background: rgba(0,0,0,0.5);
    display: flex; align-items: center; justify-content: center;
    z-index: 100;
  }
  .modal {
    background: var(--bg);
    border: 1px solid var(--border);
    border-radius: 8px;
    padding: 16px;
    min-width: 320px;
  }
  .modal h2 { margin-top: 0; }
</style>
```

- [ ] **Step 7: Wire AlbumsIndex into App.svelte**

Edit `frontend/src/App.svelte`:

```svelte
<script lang="ts">
  import { router } from "./lib/router/router.svelte";
  import { MediaStore } from "./lib/media/mediaStore.svelte";
  import { AlbumsStore } from "./lib/albums/albumsStore.svelte";
  import Library from "./routes/Library.svelte";
  import Sessions from "./routes/Sessions.svelte";
  import MediaDetail from "./routes/MediaDetail.svelte";
  import AlbumsIndex from "./routes/AlbumsIndex.svelte";
  import NotFound from "./routes/NotFound.svelte";
  import { api } from "./lib/api/client";
  // (existing imports)

  const mediaStore = new MediaStore(api);
  const albumsStore = new AlbumsStore(api);
  // (existing wiring)
</script>

<!-- inside the route switch: -->
{#if router.current.name === "library"}<Library {mediaStore} />{:else if router.current.name === "sessions"}<Sessions {mediaStore} />{:else if router.current.name === "media"}<MediaDetail id={router.current.params.id} {mediaStore} />{:else if router.current.name === "albums"}<AlbumsIndex {albumsStore} />{:else}<NotFound />{/if}
```

(Preserve existing routes; just add the `albums` branch.)

- [ ] **Step 8: Verify in browser**

```bash
make dev
# Open http://localhost:8080/albums — see "No albums yet" empty state.
# Click "Create your first album", enter "Test", confirm — see the new
# tile in the grid. Refresh; tile persists. Click a tile — should
# navigate to /albums/<id> (will 404 in NotFound until Task 13).
```

- [ ] **Step 9: Commit**

```bash
git add frontend/src/lib/router/router.svelte.ts frontend/src/lib/router/router.test.ts frontend/src/lib/components/NewAlbumForm.svelte frontend/src/lib/components/AlbumGrid.svelte frontend/src/routes/AlbumsIndex.svelte frontend/src/App.svelte
git commit -m "feat(albums): /albums route + cover grid + new-album modal"
```

---

## Task 12: AlbumDetailStore (data layer with MediaStore reuse)

**Files:**
- Create: `frontend/src/lib/albums/albumDetailStore.svelte.ts`
- Create: `frontend/src/lib/albums/albumDetailStore.test.ts`

- [ ] **Step 1: Write the failing tests**

Create `frontend/src/lib/albums/albumDetailStore.test.ts`:

```ts
import { describe, it, expect, vi } from "vitest";
import { AlbumDetailStore } from "./albumDetailStore.svelte";
import { MediaStore } from "../media/mediaStore.svelte";

function fakeClient(responses: Array<any>) {
  let i = 0;
  const calls: Array<{ method: string; path: string; opts: any }> = [];
  const handler = vi.fn(async (path: string, opts: any = {}) => {
    calls.push({ method: "h", path, opts });
    return responses[i++] ?? { data: null };
  });
  return { GET: handler, POST: handler, PATCH: handler, DELETE: handler, calls };
}

const fakeMedia = (id: string, ts = "2025-01-01T00:00:00Z") => ({
  id,
  timestamp: ts,
  width: 100,
  height: 100,
  thumb_version: 0,
});

describe("AlbumDetailStore.load", () => {
  it("fetches album metadata + first page of media and merges into MediaStore", async () => {
    const client = fakeClient([
      { data: { id: "a1", name: "Italy", item_count: 2, cover: null, created_at: "x", updated_at: "x" } },
      { data: { items: [fakeMedia("m1"), fakeMedia("m2")], next_offset: null } },
    ]);
    const ms = new MediaStore(client as any);
    const store = new AlbumDetailStore(client as any, ms);
    await store.load("a1");
    expect(store.album?.name).toBe("Italy");
    expect(store.itemIds).toEqual(["m1", "m2"]);
    expect(ms.get("m1")).not.toBeUndefined();
    expect(ms.get("m2")).not.toBeUndefined();
    expect(store.exhausted).toBe(true);
  });
});

describe("AlbumDetailStore.setSort", () => {
  it("resets pagination and refetches with the new sort", async () => {
    const client = fakeClient([
      { data: { id: "a1", name: "X", item_count: 4, cover: null, created_at: "x", updated_at: "x" } },
      { data: { items: [fakeMedia("m1"), fakeMedia("m2")], next_offset: 2 } },
      { data: { items: [fakeMedia("m3"), fakeMedia("m4")], next_offset: null } },
    ]);
    const ms = new MediaStore(client as any);
    const store = new AlbumDetailStore(client as any, ms);
    await store.load("a1");
    expect(store.itemIds).toEqual(["m1", "m2"]);

    await store.setSort("added");
    // After reset, fetch happens again. Confirm the new fetch call
    // included sort_by=added.
    const lastQuery = client.calls.at(-1)?.opts.params?.query;
    expect(lastQuery?.sort_by).toBe("added");
    expect(store.itemIds).toEqual(["m3", "m4"]);
  });
});

describe("AlbumDetailStore.removeMany", () => {
  it("returns succeeded + failed and removes successful IDs from itemIds", async () => {
    const client = fakeClient([
      { data: { id: "a1", name: "X", item_count: 3, cover: null, created_at: "x", updated_at: "x" } },
      { data: { items: [fakeMedia("m1"), fakeMedia("m2"), fakeMedia("m3")], next_offset: null } },
      { data: null },
      { error: { status: 500, message: "boom" } },
      { data: null },
    ]);
    const ms = new MediaStore(client as any);
    const store = new AlbumDetailStore(client as any, ms);
    await store.load("a1");
    const result = await store.removeMany(["m1", "m2", "m3"]);
    expect(result.succeeded.sort()).toEqual(["m1", "m3"]);
    expect(result.failed).toEqual(["m2"]);
    expect(store.itemIds).toEqual(["m2"]);
  });
});

describe("AlbumDetailStore.hasInAlbum", () => {
  it("returns true only for IDs present in itemIds", async () => {
    const client = fakeClient([
      { data: { id: "a1", name: "X", item_count: 1, cover: null, created_at: "x", updated_at: "x" } },
      { data: { items: [fakeMedia("m1")], next_offset: null } },
    ]);
    const store = new AlbumDetailStore(client as any, new MediaStore(client as any));
    await store.load("a1");
    expect(store.hasInAlbum("m1")).toBe(true);
    expect(store.hasInAlbum("m2")).toBe(false);
  });
});
```

- [ ] **Step 2: Run to verify failing**

```bash
cd frontend && bunx vitest run src/lib/albums/albumDetailStore.test.ts
```

Expected: FAIL — store does not exist.

- [ ] **Step 3: Implement AlbumDetailStore**

Create `frontend/src/lib/albums/albumDetailStore.svelte.ts`:

```ts
import type { Client } from "../api/client";
import type { MediaStore } from "../media/mediaStore.svelte";

export type Album = {
  id: string;
  name: string;
  created_at: string;
  updated_at: string;
  item_count: number;
};

export type AlbumSort = "taken" | "added";

export class AlbumDetailStore {
  album = $state<Album | null>(null);
  itemIds = $state<string[]>([]);
  loading = $state(false);
  exhausted = $state(false);
  sort = $state<AlbumSort>("taken");

  private nextOffset: number | null = 0;
  private albumId: string | null = null;
  private membership = new Set<string>();

  constructor(
    private client: Pick<Client, "GET" | "DELETE">,
    private media: MediaStore,
  ) {}

  async load(id: string): Promise<void> {
    this.albumId = id;
    this.itemIds = [];
    this.membership = new Set();
    this.nextOffset = 0;
    this.exhausted = false;

    const meta = await this.client.GET("/api/v1/albums/{id}", {
      params: { path: { id } } as never,
    });
    if (meta.error || !meta.data) return;
    const a = meta.data as Album;
    this.album = {
      id: a.id, name: a.name, created_at: a.created_at,
      updated_at: a.updated_at, item_count: a.item_count,
    };

    await this.loadMore();
  }

  async loadMore(): Promise<void> {
    if (!this.albumId || this.loading || this.exhausted) return;
    this.loading = true;
    try {
      const res = await this.client.GET("/api/v1/albums/{id}/media", {
        params: {
          path: { id: this.albumId },
          query: {
            limit: 200,
            offset: this.nextOffset ?? 0,
            sort_by: this.sort,
            sort_asc: false,
          },
        } as never,
      });
      if (res.error || !res.data) return;
      const data = res.data as { items?: any[]; next_offset?: number | null };
      const items = data.items ?? [];
      this.media.mergeRaw(items);
      const newIds = items.map((it: any) => it.id as string);
      this.itemIds = [...this.itemIds, ...newIds];
      for (const id of newIds) this.membership.add(id);
      const next = data.next_offset ?? null;
      this.nextOffset = next;
      if (next === null) this.exhausted = true;
    } finally {
      this.loading = false;
    }
  }

  async setSort(next: AlbumSort): Promise<void> {
    if (this.sort === next || !this.albumId) return;
    this.sort = next;
    this.itemIds = [];
    this.membership = new Set();
    this.nextOffset = 0;
    this.exhausted = false;
    await this.loadMore();
  }

  async removeMany(ids: string[]): Promise<{ succeeded: string[]; failed: string[] }> {
    if (!this.albumId) return { succeeded: [], failed: [] };
    const albumId = this.albumId;
    const concurrency = 4;
    const succeeded: string[] = [];
    const failed: string[] = [];
    let i = 0;
    async function worker(client: Pick<Client, "DELETE">) {
      while (i < ids.length) {
        const myIdx = i++;
        const mediaId = ids[myIdx]!;
        const res = await client.DELETE("/api/v1/albums/{id}/media/{media_id}", {
          params: { path: { id: albumId, media_id: mediaId } } as never,
        });
        if (res.error) failed.push(mediaId);
        else succeeded.push(mediaId);
      }
    }
    await Promise.all(
      Array.from({ length: Math.min(concurrency, ids.length) }, () => worker(this.client)),
    );
    if (succeeded.length > 0) {
      const succSet = new Set(succeeded);
      this.itemIds = this.itemIds.filter((id) => !succSet.has(id));
      for (const id of succeeded) this.membership.delete(id);
    }
    return { succeeded, failed };
  }

  hasInAlbum(id: string): boolean {
    return this.membership.has(id);
  }
}
```

- [ ] **Step 4: Run to verify passing**

```bash
cd frontend && bunx vitest run src/lib/albums/albumDetailStore.test.ts
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/lib/albums/albumDetailStore.svelte.ts frontend/src/lib/albums/albumDetailStore.test.ts
git commit -m "feat(albums): AlbumDetailStore with MediaStore reuse"
```

---

## Task 13: /albums/:id route + AlbumDetail header + sort + flat grid

**Files:**
- Create: `frontend/src/routes/AlbumDetail.svelte`
- Modify: `frontend/src/lib/router/router.svelte.ts`
- Modify: `frontend/src/App.svelte`

- [ ] **Step 1: Add `/albums/:id` route**

Edit `frontend/src/lib/router/router.svelte.ts`. Add:

```ts
{
  name: "albums.detail",
  pattern: /^\/albums\/([^/]+)\/?$/,
  params: (m) => ({ id: m[1] }),
},
```

- [ ] **Step 2: Add router test**

Append to `frontend/src/lib/router/router.test.ts`:

```ts
it("matches /albums/abc to albums.detail with id param", () => {
  const r = parseRoute("/albums/abc-123");
  expect(r.name).toBe("albums.detail");
  expect(r.params.id).toBe("abc-123");
});
```

- [ ] **Step 3: Run router tests**

```bash
cd frontend && bunx vitest run src/lib/router/router.test.ts
```

Expected: PASS.

- [ ] **Step 4: Implement AlbumDetail route**

Create `frontend/src/routes/AlbumDetail.svelte`:

```svelte
<script lang="ts">
  import VirtualGrid from "../lib/grid/VirtualGrid.svelte";
  import type { MediaStore, Month, Media } from "../lib/media/mediaStore.svelte";
  import { AlbumDetailStore, type AlbumSort } from "../lib/albums/albumDetailStore.svelte";
  import { api } from "../lib/api/client";

  let { id, mediaStore }: { id: string; mediaStore: MediaStore } = $props();

  const detail = new AlbumDetailStore(api, mediaStore);

  $effect(() => {
    detail.load(id);
  });

  // Synthetic single-month feed for VirtualGrid timelineChrome=false.
  const months = $derived<Month[]>(() => {
    if (!detail.album) return [];
    const items: Media[] = detail.itemIds
      .map((mid) => mediaStore.get(mid))
      .filter((m): m is Media => m !== undefined);
    return [{ key: `album:${id}`, items }];
  });

  function loadMore() { detail.loadMore(); }

  function changeSort(e: Event) {
    const next = (e.target as HTMLSelectElement).value as AlbumSort;
    detail.setSort(next);
  }
</script>

{#if detail.album}
  <header class="album-header">
    <div class="title-row">
      <h1>{detail.album.name}</h1>
      <span class="count">{detail.album.item_count} {detail.album.item_count === 1 ? "photo" : "photos"}</span>
    </div>
    <div class="action-row">
      <div class="actions"><!-- Rename / Delete / Share buttons land in Tasks 14, 19 --></div>
      <label class="sort">
        Sort:
        <select value={detail.sort} onchange={changeSort}>
          <option value="taken">Date taken</option>
          <option value="added">Recently added</option>
        </select>
      </label>
    </div>
  </header>
{/if}

{#if detail.itemIds.length > 0}
  <VirtualGrid
    {months}
    onLoadMore={loadMore}
    targetRowHeight={200}
    timelineChrome={false}
  />
{:else if !detail.loading && detail.album}
  <div class="empty">
    Empty album.<br />
    Select photos in Library, then use <strong>Add to album</strong> in the action bar.
  </div>
{/if}

{#if detail.loading}<div class="loading">Loading…</div>{/if}

<style>
  .album-header {
    padding: 12px 16px;
    border-bottom: 1px solid var(--border);
  }
  .title-row { display: flex; align-items: baseline; gap: 12px; }
  .title-row h1 { margin: 0; font-size: 20px; }
  .count { color: var(--text-muted); font-size: 14px; }
  .action-row {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-top: 8px;
  }
  .actions { display: flex; gap: 8px; }
  .sort { font-size: 13px; color: var(--text-muted); }
  .empty { padding: 64px 16px; text-align: center; color: var(--text-secondary); }
  .loading { padding: 12px; color: var(--text-muted); }
</style>
```

- [ ] **Step 5: Wire AlbumDetail into App.svelte**

Edit `frontend/src/App.svelte`:

```svelte
{:else if router.current.name === "albums.detail"}
  <AlbumDetail id={router.current.params.id} {mediaStore} />
```

(Plus the `import AlbumDetail from "./routes/AlbumDetail.svelte"` at the top.)

- [ ] **Step 6: Verify in browser**

```bash
make dev
# Open http://localhost:8080/albums, create an album, then click into
# it. Confirm: header shows album name + count, "Empty album" message,
# Sort dropdown defaults to "Date taken". (Add-to-album from Library
# multi-select lands in Task 18.)
```

- [ ] **Step 7: Commit**

```bash
git add frontend/src/lib/router/router.svelte.ts frontend/src/lib/router/router.test.ts frontend/src/routes/AlbumDetail.svelte frontend/src/App.svelte
git commit -m "feat(albums): /albums/:id route with sort and flat grid"
```

---

## Task 14: Rename + Delete album (with 409 deep link)

**Files:**
- Create: `frontend/src/lib/components/RenameAlbumModal.svelte`
- Create: `frontend/src/lib/components/ConfirmModal.svelte`
- Modify: `frontend/src/routes/AlbumDetail.svelte`
- Modify: `frontend/src/lib/albums/albumDetailStore.svelte.ts`

- [ ] **Step 1: Implement RenameAlbumModal**

Create `frontend/src/lib/components/RenameAlbumModal.svelte`:

```svelte
<script lang="ts">
  let {
    initialName,
    onSave,
    onCancel,
  }: {
    initialName: string;
    onSave: (name: string) => Promise<void>;
    onCancel: () => void;
  } = $props();

  let name = $state(initialName);
  let pending = $state(false);
  let error = $state<string | null>(null);
  const trimmed = $derived(name.trim());
  const valid = $derived(trimmed.length >= 1 && trimmed.length <= 200 && trimmed !== initialName.trim());

  async function submit(e: Event) {
    e.preventDefault();
    if (!valid || pending) return;
    pending = true;
    error = null;
    try {
      await onSave(trimmed);
    } catch (e: any) {
      error = e?.message ?? "Failed to save";
    } finally {
      pending = false;
    }
  }
</script>

<div class="modal-backdrop" onclick={onCancel}>
  <div class="modal" onclick={(e) => e.stopPropagation()}>
    <h2>Rename album</h2>
    <form onsubmit={submit}>
      <label>
        <span>Name</span>
        <input type="text" bind:value={name} maxlength="200" autofocus />
      </label>
      {#if error}<div class="error" role="alert">{error}</div>{/if}
      <div class="actions">
        <button type="button" onclick={onCancel} disabled={pending}>Cancel</button>
        <button type="submit" disabled={!valid || pending}>{pending ? "Saving…" : "Save"}</button>
      </div>
    </form>
  </div>
</div>

<style>
  .modal-backdrop {
    position: fixed; inset: 0; background: rgba(0,0,0,0.5);
    display: flex; align-items: center; justify-content: center; z-index: 100;
  }
  .modal { background: var(--bg); border: 1px solid var(--border); border-radius: 8px; padding: 16px; min-width: 320px; }
  .modal h2 { margin-top: 0; }
  label { display: flex; flex-direction: column; gap: 4px; }
  .error { color: var(--danger); font-size: 13px; margin-top: 8px; }
  .actions { display: flex; gap: 8px; justify-content: flex-end; margin-top: 12px; }
</style>
```

- [ ] **Step 2: Implement generic ConfirmModal**

Create `frontend/src/lib/components/ConfirmModal.svelte`:

```svelte
<script lang="ts">
  let {
    title,
    body,
    confirmLabel = "Confirm",
    cancelLabel = "Cancel",
    danger = false,
    onConfirm,
    onCancel,
  }: {
    title: string;
    body: string;
    confirmLabel?: string;
    cancelLabel?: string;
    danger?: boolean;
    onConfirm: () => Promise<void> | void;
    onCancel: () => void;
  } = $props();

  let pending = $state(false);

  async function confirm() {
    pending = true;
    try {
      await onConfirm();
    } finally {
      pending = false;
    }
  }
</script>

<div class="modal-backdrop" onclick={onCancel}>
  <div class="modal" onclick={(e) => e.stopPropagation()}>
    <h2>{title}</h2>
    <p>{body}</p>
    <div class="actions">
      <button type="button" onclick={onCancel} disabled={pending}>{cancelLabel}</button>
      <button
        type="button"
        class:danger
        onclick={confirm}
        disabled={pending}
      >
        {pending ? "Working…" : confirmLabel}
      </button>
    </div>
  </div>
</div>

<style>
  .modal-backdrop { position: fixed; inset: 0; background: rgba(0,0,0,0.5); display: flex; align-items: center; justify-content: center; z-index: 100; }
  .modal { background: var(--bg); border: 1px solid var(--border); border-radius: 8px; padding: 16px; min-width: 320px; max-width: 480px; }
  .modal h2 { margin-top: 0; }
  .actions { display: flex; gap: 8px; justify-content: flex-end; margin-top: 12px; }
  .danger { background: var(--danger); color: white; border-color: var(--danger); }
</style>
```

- [ ] **Step 3: Add rename + delete methods to AlbumDetailStore**

Edit `frontend/src/lib/albums/albumDetailStore.svelte.ts`. Append to the class:

```ts
  async rename(name: string): Promise<void> {
    if (!this.albumId) return;
    const trimmed = name.trim();
    if (trimmed.length === 0) throw new Error("Name is required");
    if (trimmed.length > 200) throw new Error("Name exceeds 200 characters");
    const res = await this.client.PATCH("/api/v1/albums/{id}", {
      params: { path: { id: this.albumId } } as never,
      body: { name: trimmed } as never,
    });
    if (res.error) throw res.error;
    if (res.data && this.album) {
      const a = res.data as Album;
      this.album = { ...this.album, name: a.name, updated_at: a.updated_at };
    }
  }

  async delete(): Promise<void> {
    if (!this.albumId) return;
    const res = await this.client.DELETE("/api/v1/albums/{id}", {
      params: { path: { id: this.albumId } } as never,
    });
    if (res.error) throw res.error;
  }
```

(Update the constructor's `client` parameter type to include `PATCH`: `Pick<Client, "GET" | "DELETE" | "PATCH">`.)

- [ ] **Step 4: Wire Rename + Delete buttons into AlbumDetail**

Edit `frontend/src/routes/AlbumDetail.svelte`. Replace the empty `<div class="actions">` with:

```svelte
<script lang="ts">
  // (existing imports)
  import RenameAlbumModal from "../lib/components/RenameAlbumModal.svelte";
  import ConfirmModal from "../lib/components/ConfirmModal.svelte";
  import { router, handleInternalLinkClick } from "../lib/router/router.svelte";

  // (existing detail/load/months derivations)

  let renaming = $state(false);
  let confirmingDelete = $state(false);
  let deleteConflictAlbumId = $state<string | null>(null);

  async function onRename(name: string) {
    await detail.rename(name);
    renaming = false;
  }

  async function onConfirmDelete() {
    try {
      await detail.delete();
      router.navigate("/albums");
    } catch (e: any) {
      confirmingDelete = false;
      if (e?.status === 409) {
        deleteConflictAlbumId = id;
      } else {
        // Generic toast surface lands in Task 21+; for now, log.
        console.error("delete failed:", e);
      }
    }
  }
</script>

<!-- inside <div class="actions"> in the action-row: -->
<button type="button" onclick={() => (renaming = true)}>Rename</button>
<button type="button" class="danger" onclick={() => (confirmingDelete = true)}>Delete</button>

<!-- after </header>: -->
{#if renaming && detail.album}
  <RenameAlbumModal
    initialName={detail.album.name}
    onSave={onRename}
    onCancel={() => (renaming = false)}
  />
{/if}

{#if confirmingDelete && detail.album}
  <ConfirmModal
    title={`Delete '${detail.album.name}'?`}
    body="Photos remain in your library."
    confirmLabel="Delete"
    danger
    onConfirm={onConfirmDelete}
    onCancel={() => (confirmingDelete = false)}
  />
{/if}

{#if deleteConflictAlbumId}
  <div class="conflict-toast" role="alert">
    This album has active shares. Revoke them in Shares first.
    <a
      href={`/shares?album_id=${deleteConflictAlbumId}`}
      onclick={(e) => handleInternalLinkClick(e, `/shares?album_id=${deleteConflictAlbumId}`)}
    >
      View shares →
    </a>
    <button type="button" onclick={() => (deleteConflictAlbumId = null)}>×</button>
  </div>
{/if}

<style>
  /* (existing styles preserved) */
  .danger { background: var(--danger); color: white; border-color: var(--danger); }
  .conflict-toast {
    position: fixed; bottom: 16px; right: 16px;
    background: var(--bg-elevated); border: 1px solid var(--danger);
    padding: 12px 16px; border-radius: 6px; z-index: 200;
    display: flex; align-items: center; gap: 12px;
  }
  .conflict-toast a { color: var(--accent); text-decoration: underline; }
</style>
```

- [ ] **Step 5: Verify in browser**

```bash
make dev
# Create an album → open it → click Rename → change name → confirm
# header updates. Click Delete → confirm → routes back to /albums.
# (409 conflict path is tested via e2e in Task 23.)
```

- [ ] **Step 6: Commit**

```bash
git add frontend/src/lib/components/RenameAlbumModal.svelte frontend/src/lib/components/ConfirmModal.svelte frontend/src/lib/albums/albumDetailStore.svelte.ts frontend/src/routes/AlbumDetail.svelte
git commit -m "feat(albums): rename + delete with 409 conflict toast"
```

---

## Task 15: AddToAlbumModal full behavior

**Files:**
- Create: `frontend/src/lib/components/AddToAlbumModal.svelte`
- Create: `frontend/src/lib/components/AddToAlbumModal.test.ts`
- Create: `frontend/src/lib/format/normalizeForSearch.ts`
- Create: `frontend/src/lib/format/normalizeForSearch.test.ts`

- [ ] **Step 1: Implement and test normalizeForSearch helper**

Create `frontend/src/lib/format/normalizeForSearch.ts`:

```ts
// normalizeForSearch lowercases (locale-aware) and trims a string for
// case-insensitive substring matching against album names. The corpus
// is small (typically <100 albums) so no NFC normalization is needed
// — Unicode equivalence pitfalls are unlikely in practice for album
// names users type in.
export function normalizeForSearch(s: string): string {
  return s.trim().toLocaleLowerCase();
}
```

Create `frontend/src/lib/format/normalizeForSearch.test.ts`:

```ts
import { describe, it, expect } from "vitest";
import { normalizeForSearch } from "./normalizeForSearch";

describe("normalizeForSearch", () => {
  it("lowercases ASCII", () => {
    expect(normalizeForSearch("Italy")).toBe("italy");
  });
  it("trims whitespace", () => {
    expect(normalizeForSearch("  Trip  ")).toBe("trip");
  });
  it("locale-aware lowercases Unicode letters", () => {
    // Turkish dotted I → i (locale default; en-US-acceptable for our use).
    expect(normalizeForSearch("İSTANBUL")).toContain("stanbul");
  });
});
```

- [ ] **Step 2: Run normalize tests**

```bash
cd frontend && bunx vitest run src/lib/format/normalizeForSearch.test.ts
```

Expected: PASS.

- [ ] **Step 3: Write the failing AddToAlbumModal test**

Create `frontend/src/lib/components/AddToAlbumModal.test.ts`:

```ts
import { render, fireEvent } from "@testing-library/svelte";
import { describe, it, expect, vi } from "vitest";
import AddToAlbumModal from "./AddToAlbumModal.svelte";
import type { AlbumsStore } from "../albums/albumsStore.svelte";

function makeStore(albums: any[]): AlbumsStore {
  return {
    albums,
    loading: false,
    exhausted: true,
    create: vi.fn(),
    rename: vi.fn(),
    delete: vi.fn(),
    loadInitial: vi.fn(),
    loadMore: vi.fn(),
    byId: (id: string) => albums.find((a) => a.id === id),
  } as any;
}

const fakeAlbums = [
  { id: "a1", name: "Italy", item_count: 12, cover: null, created_at: "x", updated_at: "x" },
  { id: "a2", name: "Family", item_count: 5, cover: null, created_at: "x", updated_at: "x" },
];

describe("AddToAlbumModal", () => {
  it("primary button is disabled until a target album is selected", () => {
    const onAdd = vi.fn();
    const { getByRole } = render(AddToAlbumModal, {
      props: {
        mediaIds: ["m1", "m2"],
        albumsStore: makeStore(fakeAlbums),
        onAdd,
        onClose: vi.fn(),
      },
    });
    const primary = getByRole("button", { name: /^Add 2 photos$/ });
    expect(primary.hasAttribute("disabled")).toBe(true);
  });

  it("clicking a row enables the primary button (does not auto-fire)", async () => {
    const onAdd = vi.fn();
    const { getByRole, getByText } = render(AddToAlbumModal, {
      props: {
        mediaIds: ["m1", "m2"],
        albumsStore: makeStore(fakeAlbums),
        onAdd,
        onClose: vi.fn(),
      },
    });
    await fireEvent.click(getByText("Italy"));
    expect(onAdd).not.toHaveBeenCalled();
    const primary = getByRole("button", { name: /^Add 2 photos$/ });
    expect(primary.hasAttribute("disabled")).toBe(false);
  });

  it("clicking primary calls onAdd with selected album id", async () => {
    const onAdd = vi.fn().mockResolvedValue({ added: 2, already_present: 0 });
    const { getByRole, getByText } = render(AddToAlbumModal, {
      props: {
        mediaIds: ["m1", "m2"],
        albumsStore: makeStore(fakeAlbums),
        onAdd,
        onClose: vi.fn(),
      },
    });
    await fireEvent.click(getByText("Italy"));
    await fireEvent.click(getByRole("button", { name: /^Add 2 photos$/ }));
    expect(onAdd).toHaveBeenCalledWith("a1");
  });

  it("filters albums by name (case-insensitive)", async () => {
    const { getByPlaceholderText, queryByText } = render(AddToAlbumModal, {
      props: {
        mediaIds: ["m1"],
        albumsStore: makeStore(fakeAlbums),
        onAdd: vi.fn(),
        onClose: vi.fn(),
      },
    });
    const input = getByPlaceholderText("Search albums...");
    await fireEvent.input(input, { target: { value: "fam" } });
    expect(queryByText("Italy")).toBeNull();
    expect(queryByText("Family")).not.toBeNull();
  });

  it("create-new flow leaves modal in 'ready to add' state with new album selected", async () => {
    const store = makeStore(fakeAlbums);
    store.create = vi.fn(async (n: string) => {
      // Simulate a successful create that pushes the new album to the front.
      (store as any).albums = [{ id: "anew", name: n, item_count: 0, cover: null, created_at: "x", updated_at: "x" }, ...fakeAlbums];
    });
    const { getByText, getByRole, getByPlaceholderText } = render(AddToAlbumModal, {
      props: {
        mediaIds: ["m1"],
        albumsStore: store,
        onAdd: vi.fn(),
        onClose: vi.fn(),
      },
    });
    await fireEvent.click(getByText("+ Create new album"));
    await fireEvent.input(getByPlaceholderText("Album name"), { target: { value: "Trip" } });
    await fireEvent.click(getByRole("button", { name: "Create" }));
    // After create, modal is back in list view with "Trip" highlighted.
    const primary = getByRole("button", { name: /^Add 1 photo$/ });
    expect(primary.hasAttribute("disabled")).toBe(false);
  });
});
```

- [ ] **Step 4: Run to verify failing**

```bash
cd frontend && bunx vitest run src/lib/components/AddToAlbumModal.test.ts
```

Expected: FAIL — component does not exist.

- [ ] **Step 5: Implement AddToAlbumModal**

Create `frontend/src/lib/components/AddToAlbumModal.svelte`:

```svelte
<script lang="ts">
  import type { AlbumsStore, AlbumListItem } from "../albums/albumsStore.svelte";
  import { normalizeForSearch } from "../format/normalizeForSearch";
  import NewAlbumForm from "./NewAlbumForm.svelte";

  let {
    mediaIds,
    albumsStore,
    onAdd,
    onClose,
  }: {
    mediaIds: string[];
    albumsStore: AlbumsStore;
    onAdd: (albumId: string) => Promise<{ added: number; already_present: number }>;
    onClose: () => void;
  } = $props();

  let mode = $state<"list" | "create">("list");
  let query = $state("");
  let selectedId = $state<string | null>(null);
  let pending = $state(false);
  let error = $state<string | null>(null);

  // Users may open Add-to-album before ever visiting /albums, so the
  // store may not yet be hydrated. Same dual-guard pattern as
  // AlbumsIndex / SharesPage: empty-list AND not-loading AND not-
  // exhausted is the only state that warrants a loadInitial.
  $effect(() => {
    if (
      albumsStore.albums.length === 0 &&
      !albumsStore.loading &&
      !albumsStore.exhausted
    ) {
      albumsStore.loadInitial();
    }
  });

  const subtitle = $derived(`${mediaIds.length} ${mediaIds.length === 1 ? "photo" : "photos"}`);
  const primaryLabel = $derived(`Add ${mediaIds.length} ${mediaIds.length === 1 ? "photo" : "photos"}`);

  const filtered = $derived<AlbumListItem[]>(() => {
    const q = normalizeForSearch(query);
    if (q === "") return albumsStore.albums;
    return albumsStore.albums.filter((a) => normalizeForSearch(a.name).includes(q));
  });

  async function onCreateNew(name: string) {
    await albumsStore.create(name);
    // After create, find the new album by name (top of refetched list).
    const fresh = albumsStore.albums.find((a) => a.name === name.trim());
    if (fresh) selectedId = fresh.id;
    mode = "list";
  }

  async function submit() {
    if (!selectedId || pending) return;
    pending = true;
    error = null;
    try {
      await onAdd(selectedId);
      onClose();
    } catch (e: any) {
      error = e?.message ?? "Failed to add photos";
    } finally {
      pending = false;
    }
  }
</script>

<div class="modal-backdrop" onclick={onClose}>
  <div class="modal" onclick={(e) => e.stopPropagation()}>
    <h2>Add to album</h2>
    <div class="subtitle">{subtitle}</div>

    {#if mode === "list"}
      <input
        type="text"
        class="search"
        placeholder="Search albums..."
        bind:value={query}
        autofocus
      />
      <div class="list">
        <button type="button" class="row create-new" onclick={() => (mode = "create")}>
          + Create new album
        </button>
        {#each filtered as a (a.id)}
          <button
            type="button"
            class="row"
            class:selected={selectedId === a.id}
            onclick={() => (selectedId = a.id)}
          >
            <span class="name">{a.name}</span>
            <span class="count">{a.item_count}</span>
          </button>
        {/each}
      </div>
      {#if error}<div class="error" role="alert">{error}</div>{/if}
      <div class="actions">
        <button type="button" onclick={onClose} disabled={pending}>Cancel</button>
        <button type="button" onclick={submit} disabled={!selectedId || pending}>
          {pending ? "Adding…" : primaryLabel}
        </button>
      </div>
    {:else}
      <NewAlbumForm onCreate={onCreateNew} onCancel={() => (mode = "list")} />
    {/if}
  </div>
</div>

<style>
  .modal-backdrop {
    position: fixed; inset: 0; background: rgba(0,0,0,0.5);
    display: flex; align-items: center; justify-content: center; z-index: 100;
  }
  .modal {
    background: var(--bg);
    border: 1px solid var(--border);
    border-radius: 8px;
    padding: 16px;
    min-width: 400px;
    max-height: 80vh;
    display: flex; flex-direction: column;
  }
  .modal h2 { margin-top: 0; }
  .subtitle { color: var(--text-muted); font-size: 13px; margin-bottom: 12px; }
  .search { padding: 6px 8px; border: 1px solid var(--border); background: var(--bg-elevated); color: var(--text); border-radius: 4px; }
  .list {
    margin-top: 12px;
    overflow-y: auto;
    flex: 1;
    border: 1px solid var(--border);
    border-radius: 4px;
  }
  .row {
    display: flex;
    justify-content: space-between;
    width: 100%;
    padding: 8px 12px;
    background: transparent;
    border: 0;
    border-bottom: 1px solid var(--border);
    color: var(--text);
    cursor: pointer;
    text-align: left;
  }
  .row:last-child { border-bottom: 0; }
  .row:hover { background: var(--bg-elevated); }
  .row.selected { background: var(--bg-elevated); font-weight: 600; outline: 2px solid var(--accent); outline-offset: -2px; }
  .row.create-new { color: var(--accent); font-weight: 500; }
  .count { color: var(--text-muted); font-size: 12px; }
  .error { color: var(--danger); font-size: 13px; margin-top: 8px; }
  .actions { display: flex; gap: 8px; justify-content: flex-end; margin-top: 12px; }
</style>
```

- [ ] **Step 6: Run modal tests**

```bash
cd frontend && bunx vitest run src/lib/components/AddToAlbumModal.test.ts
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add frontend/src/lib/format/normalizeForSearch.ts frontend/src/lib/format/normalizeForSearch.test.ts frontend/src/lib/components/AddToAlbumModal.svelte frontend/src/lib/components/AddToAlbumModal.test.ts
git commit -m "feat(albums): AddToAlbumModal with two-step add and create-new"
```

---

## Task 16: ShareModal full behavior

**Files:**
- Create: `frontend/src/lib/components/ShareModal.svelte`
- Create: `frontend/src/lib/components/ShareModal.test.ts`
- Create: `frontend/src/lib/format/parseGrantee.ts`
- Create: `frontend/src/lib/format/parseGrantee.test.ts`

- [ ] **Step 1: Implement and test parseGrantee helper**

Create `frontend/src/lib/format/parseGrantee.ts`:

```ts
// parseGrantee turns "hub:user_id" into the API's object shape.
// Returns null if the input doesn't have exactly one colon, if either
// side is empty, or if either side exceeds 255 chars.
export type Grantee = { hub: string; user_id: string };

const MAX_PRINCIPAL_FIELD = 255;

export function parseGrantee(raw: string): Grantee | null {
  const trimmed = raw.trim();
  if (trimmed.length === 0) return null;
  if (/\s/.test(trimmed)) return null;
  const idx = trimmed.indexOf(":");
  if (idx <= 0 || idx === trimmed.length - 1) return null;
  if (trimmed.indexOf(":", idx + 1) !== -1) return null; // more than one colon
  const hub = trimmed.slice(0, idx);
  const user_id = trimmed.slice(idx + 1);
  if (hub.length > MAX_PRINCIPAL_FIELD) return null;
  if (user_id.length > MAX_PRINCIPAL_FIELD) return null;
  return { hub, user_id };
}
```

Create `frontend/src/lib/format/parseGrantee.test.ts`:

```ts
import { describe, it, expect } from "vitest";
import { parseGrantee } from "./parseGrantee";

describe("parseGrantee", () => {
  it("parses valid hub:user_id", () => {
    expect(parseGrantee("myhub:bob")).toEqual({ hub: "myhub", user_id: "bob" });
  });
  it("trims whitespace", () => {
    expect(parseGrantee("  myhub:bob  ")).toEqual({ hub: "myhub", user_id: "bob" });
  });
  it("rejects missing colon", () => {
    expect(parseGrantee("myhub")).toBeNull();
  });
  it("rejects empty hub", () => {
    expect(parseGrantee(":bob")).toBeNull();
  });
  it("rejects empty user_id", () => {
    expect(parseGrantee("myhub:")).toBeNull();
  });
  it("rejects internal whitespace", () => {
    expect(parseGrantee("my hub:bob")).toBeNull();
  });
  it("rejects multiple colons", () => {
    expect(parseGrantee("hub:user:id")).toBeNull();
  });
  it("rejects oversized fields", () => {
    expect(parseGrantee("a".repeat(300) + ":bob")).toBeNull();
    expect(parseGrantee("hub:" + "b".repeat(300))).toBeNull();
  });
});
```

- [ ] **Step 2: Run parseGrantee tests**

```bash
cd frontend && bunx vitest run src/lib/format/parseGrantee.test.ts
```

Expected: PASS.

- [ ] **Step 3: Write failing ShareModal tests**

Create `frontend/src/lib/components/ShareModal.test.ts`:

```ts
import { render, fireEvent } from "@testing-library/svelte";
import { describe, it, expect, vi } from "vitest";
import ShareModal from "./ShareModal.svelte";

describe("ShareModal media_set", () => {
  it("primary button is disabled until grantee parses", () => {
    const onCreate = vi.fn();
    const { getByRole } = render(ShareModal, {
      props: {
        target: { type: "media_set" as const, mediaIds: ["m1"] },
        onCreate,
        onClose: vi.fn(),
      },
    });
    const primary = getByRole("button", { name: "Create share" });
    expect(primary.hasAttribute("disabled")).toBe(true);
  });

  it("enables primary once grantee is valid", async () => {
    const { getByRole, getByPlaceholderText } = render(ShareModal, {
      props: {
        target: { type: "media_set" as const, mediaIds: ["m1"] },
        onCreate: vi.fn(),
        onClose: vi.fn(),
      },
    });
    const input = getByPlaceholderText("myhub:bob");
    await fireEvent.input(input, { target: { value: "h:b" } });
    expect(getByRole("button", { name: "Create share" }).hasAttribute("disabled")).toBe(false);
  });

  it("disables primary with media_set > 1000 helper text", () => {
    const { getByRole, getByText } = render(ShareModal, {
      props: {
        target: { type: "media_set" as const, mediaIds: Array.from({ length: 1500 }, (_, i) => `m${i}`) },
        onCreate: vi.fn(),
        onClose: vi.fn(),
      },
    });
    expect(getByRole("button", { name: "Create share" }).hasAttribute("disabled")).toBe(true);
    expect(getByText(/Selection too large/)).not.toBeNull();
  });

  it("submit calls onCreate with parsed grantee object and proper body shape", async () => {
    const onCreate = vi.fn().mockResolvedValue(undefined);
    const { getByRole, getByPlaceholderText, getByLabelText } = render(ShareModal, {
      props: {
        target: { type: "media_set" as const, mediaIds: ["m1", "m2"] },
        onCreate,
        onClose: vi.fn(),
      },
    });
    await fireEvent.input(getByPlaceholderText("myhub:bob"), { target: { value: "myhub:bob" } });
    await fireEvent.input(getByLabelText(/Label/), { target: { value: "Trip share" } });
    await fireEvent.click(getByLabelText(/Allow download/));
    await fireEvent.click(getByRole("button", { name: "Create share" }));
    expect(onCreate).toHaveBeenCalledWith({
      target_type: "media_set",
      media_ids: ["m1", "m2"],
      grantee: { hub: "myhub", user_id: "bob" },
      label: "Trip share",
      allow_download: true,
    });
  });
});

describe("ShareModal album_live", () => {
  it("title reflects album-share context and submit uses album_id", async () => {
    const onCreate = vi.fn().mockResolvedValue(undefined);
    const { getByRole, getByPlaceholderText, getByText } = render(ShareModal, {
      props: {
        target: { type: "album_live" as const, albumId: "a1", albumName: "Italy 2025" },
        onCreate,
        onClose: vi.fn(),
      },
    });
    expect(getByText(/Share album: Italy 2025/)).not.toBeNull();
    await fireEvent.input(getByPlaceholderText("myhub:bob"), { target: { value: "h:b" } });
    await fireEvent.click(getByRole("button", { name: "Create share" }));
    expect(onCreate).toHaveBeenCalledWith({
      target_type: "album_live",
      album_id: "a1",
      grantee: { hub: "h", user_id: "b" },
      label: "",
      allow_download: false,
    });
  });
});
```

- [ ] **Step 4: Run to verify failing**

```bash
cd frontend && bunx vitest run src/lib/components/ShareModal.test.ts
```

Expected: FAIL — component does not exist.

- [ ] **Step 5: Implement ShareModal**

Create `frontend/src/lib/components/ShareModal.svelte`:

```svelte
<script lang="ts">
  import { parseGrantee } from "../format/parseGrantee";

  export type ShareTarget =
    | { type: "media_set"; mediaIds: string[] }
    | { type: "album_live"; albumId: string; albumName: string };

  export type CreateShareBody =
    | {
        target_type: "media_set";
        media_ids: string[];
        grantee: { hub: string; user_id: string };
        label: string;
        allow_download: boolean;
      }
    | {
        target_type: "album_live";
        album_id: string;
        grantee: { hub: string; user_id: string };
        label: string;
        allow_download: boolean;
      };

  let {
    target,
    onCreate,
    onClose,
  }: {
    target: ShareTarget;
    onCreate: (body: CreateShareBody) => Promise<void>;
    onClose: () => void;
  } = $props();

  let granteeRaw = $state("");
  let label = $state("");
  let allowDownload = $state(false);
  let pending = $state(false);
  let error = $state<string | null>(null);

  const grantee = $derived(parseGrantee(granteeRaw));
  const labelTrimmed = $derived(label.trim());
  const labelOk = $derived(labelTrimmed.length <= 200);
  const setTooLarge = $derived(target.type === "media_set" && target.mediaIds.length > 1000);
  const valid = $derived(grantee !== null && labelOk && !setTooLarge);

  const title = $derived(() => {
    if (target.type === "album_live") return `Share album: ${target.albumName}`;
    const n = target.mediaIds.length;
    return `Share ${n} ${n === 1 ? "photo" : "photos"}`;
  });

  const summary = $derived(() => {
    if (target.type === "album_live") return "Live album";
    return `${target.mediaIds.length} ${target.mediaIds.length === 1 ? "photo" : "photos"} selected`;
  });

  async function submit(e: Event) {
    e.preventDefault();
    if (!valid || pending || !grantee) return;
    pending = true;
    error = null;
    try {
      const body: CreateShareBody =
        target.type === "media_set"
          ? {
              target_type: "media_set",
              media_ids: target.mediaIds,
              grantee,
              label: labelTrimmed,
              allow_download: allowDownload,
            }
          : {
              target_type: "album_live",
              album_id: target.albumId,
              grantee,
              label: labelTrimmed,
              allow_download: allowDownload,
            };
      await onCreate(body);
    } catch (e: any) {
      error = e?.message ?? "Failed to create share";
    } finally {
      pending = false;
    }
  }
</script>

<div class="modal-backdrop" onclick={onClose}>
  <div class="modal" onclick={(e) => e.stopPropagation()}>
    <h2>{title}</h2>
    <div class="summary">{summary}</div>
    <form onsubmit={submit}>
      <label>
        <span>Grantee</span>
        <input
          type="text"
          class="grantee"
          placeholder="myhub:bob"
          bind:value={granteeRaw}
          aria-invalid={granteeRaw !== "" && grantee === null}
          autofocus
        />
        <span class="helper">hub:user_id</span>
      </label>
      <label>
        <span>Label</span>
        <input type="text" bind:value={label} maxlength="200" />
        <span class="helper">Helps identify this share later.</span>
      </label>
      <label class="checkbox">
        <input type="checkbox" bind:checked={allowDownload} />
        <span>Allow download</span>
      </label>

      {#if setTooLarge}
        <div class="warning">Selection too large (max 1000 per share)</div>
      {/if}
      {#if error}<div class="error" role="alert">{error}</div>{/if}

      <div class="actions">
        <button type="button" onclick={onClose} disabled={pending}>Cancel</button>
        <button type="submit" disabled={!valid || pending}>
          {pending ? "Creating…" : "Create share"}
        </button>
      </div>
    </form>
  </div>
</div>

<style>
  .modal-backdrop { position: fixed; inset: 0; background: rgba(0,0,0,0.5); display: flex; align-items: center; justify-content: center; z-index: 100; }
  .modal { background: var(--bg); border: 1px solid var(--border); border-radius: 8px; padding: 16px; min-width: 400px; }
  .modal h2 { margin-top: 0; }
  .summary { color: var(--text-muted); font-size: 13px; margin-bottom: 12px; }
  form { display: flex; flex-direction: column; gap: 12px; }
  label { display: flex; flex-direction: column; gap: 4px; }
  label.checkbox { flex-direction: row; align-items: center; gap: 8px; }
  .grantee { font-family: monospace; }
  .helper { font-size: 12px; color: var(--text-muted); }
  .warning { color: var(--warn); font-size: 13px; }
  .error { color: var(--danger); font-size: 13px; }
  .actions { display: flex; gap: 8px; justify-content: flex-end; margin-top: 8px; }
</style>
```

- [ ] **Step 6: Run modal tests**

```bash
cd frontend && bunx vitest run src/lib/components/ShareModal.test.ts
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add frontend/src/lib/format/parseGrantee.ts frontend/src/lib/format/parseGrantee.test.ts frontend/src/lib/components/ShareModal.svelte frontend/src/lib/components/ShareModal.test.ts
git commit -m "feat(share): ShareModal with grantee parsing and validation"
```

---

## Task 17: MediaActions component + mount in MediaDetail

**Files:**
- Create: `frontend/src/lib/components/MediaActions.svelte`
- Create: `frontend/src/lib/components/MediaActions.test.ts`
- Modify: `frontend/src/routes/MediaDetail.svelte`

- [ ] **Step 1: Write the failing MediaActions tests**

Create `frontend/src/lib/components/MediaActions.test.ts`:

```ts
import { render, fireEvent } from "@testing-library/svelte";
import { describe, it, expect, vi } from "vitest";
import MediaActions from "./MediaActions.svelte";

describe("MediaActions", () => {
  it("renders Add to album and Share buttons by default", () => {
    const { getByRole } = render(MediaActions, {
      props: { mediaIds: ["m1"], onAdd: vi.fn(), onShare: vi.fn() },
    });
    expect(getByRole("button", { name: "Add to album" })).not.toBeNull();
    expect(getByRole("button", { name: "Share" })).not.toBeNull();
  });

  it("renders Remove from this album when context=album and onRemove is set", () => {
    const { getByRole } = render(MediaActions, {
      props: {
        mediaIds: ["m1"],
        context: "album",
        albumId: "a1",
        onAdd: vi.fn(),
        onShare: vi.fn(),
        onRemove: vi.fn(),
      },
    });
    expect(getByRole("button", { name: "Remove from this album" })).not.toBeNull();
  });

  it("does NOT render Remove when context!=album", () => {
    const { queryByRole } = render(MediaActions, {
      props: { mediaIds: ["m1"], onAdd: vi.fn(), onShare: vi.fn() },
    });
    expect(queryByRole("button", { name: "Remove from this album" })).toBeNull();
  });

  it("clicking Add invokes onAdd with mediaIds", async () => {
    const onAdd = vi.fn();
    const { getByRole } = render(MediaActions, {
      props: { mediaIds: ["m1", "m2"], onAdd, onShare: vi.fn() },
    });
    await fireEvent.click(getByRole("button", { name: "Add to album" }));
    expect(onAdd).toHaveBeenCalledWith(["m1", "m2"]);
  });

  it("clicking Share invokes onShare with mediaIds", async () => {
    const onShare = vi.fn();
    const { getByRole } = render(MediaActions, {
      props: { mediaIds: ["m1"], onAdd: vi.fn(), onShare },
    });
    await fireEvent.click(getByRole("button", { name: "Share" }));
    expect(onShare).toHaveBeenCalledWith(["m1"]);
  });

  it("hides all buttons when mediaIds is empty (defense)", () => {
    const { queryByRole } = render(MediaActions, {
      props: { mediaIds: [], onAdd: vi.fn(), onShare: vi.fn() },
    });
    expect(queryByRole("button", { name: "Add to album" })).toBeNull();
    expect(queryByRole("button", { name: "Share" })).toBeNull();
  });
});
```

- [ ] **Step 2: Run to verify failing**

```bash
cd frontend && bunx vitest run src/lib/components/MediaActions.test.ts
```

Expected: FAIL — component does not exist.

- [ ] **Step 3: Implement MediaActions**

Create `frontend/src/lib/components/MediaActions.svelte`:

```svelte
<script lang="ts">
  let {
    mediaIds,
    context = "library",
    albumId,
    onAdd,
    onShare,
    onRemove,
  }: {
    mediaIds: string[];
    context?: "library" | "session" | "album";
    albumId?: string;
    onAdd: (ids: string[]) => void;
    onShare: (ids: string[]) => void;
    onRemove?: (ids: string[]) => void;
  } = $props();
</script>

{#if mediaIds.length > 0}
  <div class="media-actions">
    <button type="button" onclick={() => onAdd(mediaIds)}>Add to album</button>
    <button type="button" onclick={() => onShare(mediaIds)}>Share</button>
    {#if context === "album" && onRemove && albumId}
      <button type="button" class="danger" onclick={() => onRemove(mediaIds)}>
        Remove from this album
      </button>
    {/if}
  </div>
{/if}

<style>
  .media-actions { display: flex; gap: 8px; }
  .danger { color: var(--danger); border-color: var(--danger); }
</style>
```

- [ ] **Step 4: Run to verify passing**

```bash
cd frontend && bunx vitest run src/lib/components/MediaActions.test.ts
```

Expected: PASS.

- [ ] **Step 5: Mount MediaActions in MediaDetail header**

Edit `frontend/src/routes/MediaDetail.svelte`. Find the existing header section that branches on `paired_with_id` (added in F2.2 Task 12). Mount MediaActions only on the primary path. Add modal state + handlers:

```svelte
<script lang="ts">
  // (existing imports preserved)
  import MediaActions from "../lib/components/MediaActions.svelte";
  import AddToAlbumModal from "../lib/components/AddToAlbumModal.svelte";
  import ShareModal from "../lib/components/ShareModal.svelte";
  import type { AlbumsStore } from "../lib/albums/albumsStore.svelte";
  import { api } from "../lib/api/client";

  let { id, mediaStore, albumsStore }: {
    id: string;
    mediaStore: MediaStore;
    albumsStore: AlbumsStore;
  } = $props();

  // (existing media-loading effect preserved)

  let addOpen = $state(false);
  let shareOpen = $state(false);
  let pendingMediaIds = $state<string[]>([]);

  function openAdd(ids: string[]) {
    pendingMediaIds = ids;
    addOpen = true;
  }
  function openShare(ids: string[]) {
    pendingMediaIds = ids;
    shareOpen = true;
  }

  async function onAdd(albumId: string) {
    const res = await api.POST("/api/v1/albums/{id}/media", {
      params: { path: { id: albumId } } as never,
      body: { media_ids: pendingMediaIds } as never,
    });
    if (res.error) throw res.error;
    return res.data as { added: number; already_present: number };
  }

  async function onCreateShare(body: any) {
    const res = await api.POST("/api/v1/shares", { body: body as never });
    if (res.error) throw res.error;
    shareOpen = false;
  }
</script>

<!-- inside the existing primary-path branch (i.e., NOT on sidecar direct page): -->
{#if media && !media.paired_with_id}
  <header class="media-actions-header">
    <MediaActions
      mediaIds={[media.id]}
      onAdd={openAdd}
      onShare={openShare}
    />
  </header>
{/if}

{#if addOpen}
  <AddToAlbumModal
    mediaIds={pendingMediaIds}
    {albumsStore}
    {onAdd}
    onClose={() => (addOpen = false)}
  />
{/if}

{#if shareOpen}
  <ShareModal
    target={{ type: "media_set", mediaIds: pendingMediaIds }}
    onCreate={onCreateShare}
    onClose={() => (shareOpen = false)}
  />
{/if}
```

Pass `albumsStore` from `App.svelte` into `<MediaDetail ... />` via props.

- [ ] **Step 6: Verify in browser**

```bash
make dev
# Open a photo via /media/<id> — see Add to album and Share buttons
# in the header. Click Add to album, pick an album, confirm with
# "Add 1 photo" → toast / modal closes. Click Share, type
# "test:user", click "Create share" → modal closes.
# Open a sidecar URL (RAW direct page) — buttons absent.
```

- [ ] **Step 7: Commit**

```bash
git add frontend/src/lib/components/MediaActions.svelte frontend/src/lib/components/MediaActions.test.ts frontend/src/routes/MediaDetail.svelte frontend/src/App.svelte
git commit -m "feat(media): MediaActions cluster on MediaDetail primary"
```

---

## Task 18: ActionBar route-aware + bulk Add/Share on Library/Sessions

**Files:**
- Modify: `frontend/src/lib/components/ActionBar.svelte`
- Modify: `frontend/src/lib/components/ActionBar.test.ts`
- Modify: `frontend/src/routes/Library.svelte`
- Modify: `frontend/src/routes/Sessions.svelte`
- Modify: `frontend/src/App.svelte`

- [ ] **Step 1: Update ActionBar tests**

Edit `frontend/src/lib/components/ActionBar.test.ts`. Add cases for the new `actions` snippet prop:

```ts
import { render } from "@testing-library/svelte";
import { describe, it, expect } from "vitest";
import ActionBar from "./ActionBar.svelte";
import { selection } from "../selection/selectionStore.svelte";
import ActionsFixture from "./ActionsFixture.svelte";

describe("ActionBar with actions snippet", () => {
  it("renders the actions snippet beside the count", () => {
    selection.clear();
    selection.toggle("a");
    selection.toggle("b");
    const { container } = render(ActionsFixture);
    expect(container.querySelector(".count")?.textContent).toContain("2 selected");
    expect(container.querySelector("button.test-fix")).not.toBeNull();
  });
});
```

Create `frontend/src/lib/components/ActionsFixture.svelte`:

```svelte
<script lang="ts">
  import ActionBar from "./ActionBar.svelte";
  import { selection } from "../selection/selectionStore.svelte";
</script>

<ActionBar {selection}>
  {#snippet actions()}
    <button type="button" class="test-fix">Test Action</button>
  {/snippet}
</ActionBar>
```

- [ ] **Step 2: Update ActionBar to accept the snippet**

Edit `frontend/src/lib/components/ActionBar.svelte`:

```svelte
<script lang="ts">
  import type { Snippet } from "svelte";
  import type { SelectionStore } from "../selection/selectionStore.svelte";
  // selectedCount overrides selection.ids.size for routes that scope
  // selection (e.g. AlbumDetail, where only album-member ids count).
  // Default to selection.ids.size for the global Library/Sessions case.
  let { selection, actions, selectedCount }: {
    selection: SelectionStore;
    actions?: Snippet;
    selectedCount?: number;
  } = $props();
  const count = $derived(selectedCount ?? selection.ids.size);
</script>

{#if count > 0}
  <div class="action-bar">
    <span class="count">{count} selected</span>
    {#if actions}
      <span class="actions">{@render actions()}</span>
    {/if}
    <button type="button" onclick={() => selection.clear()}>Done</button>
  </div>
{/if}

<style>
  .action-bar {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 8px 12px;
    background: var(--bg-elevated);
    border-bottom: 1px solid var(--border);
  }
  .count { font-weight: 600; }
  .actions { display: flex; gap: 8px; }
</style>
```

- [ ] **Step 3: Run ActionBar tests**

```bash
cd frontend && bunx vitest run src/lib/components/ActionBar.test.ts
```

Expected: PASS.

- [ ] **Step 4: Mount route-specific ActionBar in Library**

Edit `frontend/src/routes/Library.svelte`. Add ActionBar with MediaActions + modals:

```svelte
<script lang="ts">
  // (existing imports)
  import ActionBar from "../lib/components/ActionBar.svelte";
  import MediaActions from "../lib/components/MediaActions.svelte";
  import AddToAlbumModal from "../lib/components/AddToAlbumModal.svelte";
  import ShareModal from "../lib/components/ShareModal.svelte";
  import { selection } from "../lib/selection/selectionStore.svelte";
  import type { AlbumsStore } from "../lib/albums/albumsStore.svelte";
  import { api } from "../lib/api/client";

  let { mediaStore, albumsStore }: { mediaStore: MediaStore; albumsStore: AlbumsStore } = $props();

  // (existing density block)

  let addOpen = $state(false);
  let shareOpen = $state(false);
  let pendingIds = $state<string[]>([]);

  function openAdd(ids: string[]) { pendingIds = ids; addOpen = true; }
  function openShare(ids: string[]) { pendingIds = ids; shareOpen = true; }

  async function onAdd(albumId: string) {
    const res = await api.POST("/api/v1/albums/{id}/media", {
      params: { path: { id: albumId } } as never,
      body: { media_ids: pendingIds } as never,
    });
    if (res.error) throw res.error;
    selection.clear();
    return res.data as { added: number; already_present: number };
  }

  async function onCreateShare(body: any) {
    const res = await api.POST("/api/v1/shares", { body: body as never });
    if (res.error) throw res.error;
    selection.clear();
    shareOpen = false;
  }
</script>

<ActionBar {selection}>
  {#snippet actions()}
    <MediaActions
      mediaIds={Array.from(selection.ids)}
      onAdd={openAdd}
      onShare={openShare}
    />
  {/snippet}
</ActionBar>

<!-- (existing density header + VirtualGrid + loading + empty state preserved) -->

{#if addOpen}
  <AddToAlbumModal
    mediaIds={pendingIds}
    {albumsStore}
    {onAdd}
    onClose={() => (addOpen = false)}
  />
{/if}

{#if shareOpen}
  <ShareModal
    target={{ type: "media_set", mediaIds: pendingIds }}
    onCreate={onCreateShare}
    onClose={() => (shareOpen = false)}
  />
{/if}
```

- [ ] **Step 5: Mount the same ActionBar in Sessions**

Edit `frontend/src/routes/Sessions.svelte` to mount ActionBar with the same snippet + modal handlers as Library. (Code is structurally identical to Step 4 above; copy the relevant blocks.)

- [ ] **Step 6: Pass `albumsStore` to Library and Sessions**

Edit `frontend/src/App.svelte`:

```svelte
{#if router.current.name === "library"}
  <Library {mediaStore} {albumsStore} />
{:else if router.current.name === "sessions"}
  <Sessions {mediaStore} {albumsStore} />
{:else if router.current.name === "media"}
  <MediaDetail id={router.current.params.id} {mediaStore} {albumsStore} />
{:else if router.current.name === "albums"}
  <AlbumsIndex {albumsStore} />
{:else if router.current.name === "albums.detail"}
  <AlbumDetail id={router.current.params.id} {mediaStore} {albumsStore} />
{:else}
  <NotFound />
{/if}
```

- [ ] **Step 7: Verify in browser**

```bash
make dev
# Open /library, multi-select 3 photos via shift-click. ActionBar shows
# "3 selected", Add to album, Share, Done. Click Add to album → modal
# opens with "3 photos" subtitle. Pick an album → "Add 3 photos"
# button. Confirm → modal closes, selection clears.
# Confirm /albums and /shares do NOT show ActionBar (route-aware).
```

- [ ] **Step 8: Commit**

```bash
git add frontend/src/lib/components/ActionBar.svelte frontend/src/lib/components/ActionBar.test.ts frontend/src/lib/components/ActionsFixture.svelte frontend/src/routes/Library.svelte frontend/src/routes/Sessions.svelte frontend/src/App.svelte
git commit -m "feat(actionbar): route-aware bulk Add/Share on Library/Sessions"
```

---

## Task 19: Album-detail bulk actions + Share-album button

**Files:**
- Modify: `frontend/src/routes/AlbumDetail.svelte`

- [ ] **Step 1: Wire ActionBar with selection∩album intersection**

Edit `frontend/src/routes/AlbumDetail.svelte`. Add ActionBar with MediaActions in album context, plus the Share-album button:

```svelte
<script lang="ts">
  // (existing imports)
  import ActionBar from "../lib/components/ActionBar.svelte";
  import MediaActions from "../lib/components/MediaActions.svelte";
  import AddToAlbumModal from "../lib/components/AddToAlbumModal.svelte";
  import ShareModal from "../lib/components/ShareModal.svelte";
  import { selection } from "../lib/selection/selectionStore.svelte";
  import type { AlbumsStore } from "../lib/albums/albumsStore.svelte";
  import { api } from "../lib/api/client";

  let { id, mediaStore, albumsStore }: {
    id: string;
    mediaStore: MediaStore;
    albumsStore: AlbumsStore;
  } = $props();

  // (existing detail/load/months/sort blocks)

  // Route-scoped selection: only album members.
  const selectedInAlbum = $derived(
    Array.from(selection.ids).filter((sid) => detail.hasInAlbum(sid)),
  );

  let addOpen = $state(false);
  let shareOpen = $state(false);
  let shareAlbumOpen = $state(false);
  let pendingIds = $state<string[]>([]);

  function openAdd(ids: string[]) { pendingIds = ids; addOpen = true; }
  function openShare(ids: string[]) { pendingIds = ids; shareOpen = true; }

  async function onRemove(ids: string[]) {
    const result = await detail.removeMany(ids);
    if (result.succeeded.length > 0) {
      selection.removeAll(result.succeeded);
    }
    if (result.failed.length > 0) {
      console.warn("partial remove failure:", result.failed);
      // Toast surface lands in Task 22 e2e + general toast; for now log.
    }
  }

  async function onAdd(albumId: string) {
    const res = await api.POST("/api/v1/albums/{id}/media", {
      params: { path: { id: albumId } } as never,
      body: { media_ids: pendingIds } as never,
    });
    if (res.error) throw res.error;
    selection.clear();
    if (albumId === id) {
      // Same album → refetch this view to repair counts/membership.
      await detail.load(id);
    }
    return res.data as { added: number; already_present: number };
  }

  async function onCreateShare(body: any) {
    const res = await api.POST("/api/v1/shares", { body: body as never });
    if (res.error) throw res.error;
    selection.clear();
    shareOpen = false;
    shareAlbumOpen = false;
  }
</script>

<!-- inside <div class="actions">, add Share-album button next to Rename/Delete: -->
<button type="button" onclick={() => (shareAlbumOpen = true)} disabled={!detail.album || detail.album.item_count === 0}>
  Share album
</button>

<!--
  ActionBar reads `selectedCount` from the prop, not from
  `selection.ids.size`, so on the album-detail route it shows only the
  count of selections that intersect this album. This avoids a
  misleading bar that displays unrelated Library/Sessions selections
  when the user navigates over with stale global selection.
-->
<ActionBar {selection} selectedCount={selectedInAlbum.length}>
  {#snippet actions()}
    {#if selectedInAlbum.length > 0}
      <MediaActions
        mediaIds={selectedInAlbum}
        context="album"
        albumId={id}
        onAdd={openAdd}
        onShare={openShare}
        {onRemove}
      />
    {/if}
  {/snippet}
</ActionBar>

{#if addOpen}
  <AddToAlbumModal
    mediaIds={pendingIds}
    {albumsStore}
    {onAdd}
    onClose={() => (addOpen = false)}
  />
{/if}

{#if shareOpen}
  <ShareModal
    target={{ type: "media_set", mediaIds: pendingIds }}
    onCreate={onCreateShare}
    onClose={() => (shareOpen = false)}
  />
{/if}

{#if shareAlbumOpen && detail.album}
  <ShareModal
    target={{ type: "album_live", albumId: id, albumName: detail.album.name }}
    onCreate={onCreateShare}
    onClose={() => (shareAlbumOpen = false)}
  />
{/if}
```

- [ ] **Step 2: Verify in browser**

```bash
make dev
# 1. Create album "X", add 3 photos from Library multi-select.
# 2. Navigate to /albums/<id> for X.
# 3. Click 2 of the 3 photos → ActionBar shows
#    "2 selected" with Add / Share / Remove from this album.
# 4. Multi-select something in Library, then navigate back to album X.
#    Confirm Library selections that aren't in X don't appear in
#    selectedInAlbum (Remove only operates on the intersection).
# 5. Click Remove → 2 photos disappear from the album view, count
#    drops to 1.
# 6. Click "Share album" → ShareModal opens with title
#    "Share album: X" and target_type=album_live in the body.
```

- [ ] **Step 3: Commit**

```bash
git add frontend/src/routes/AlbumDetail.svelte
git commit -m "feat(albums): album-detail bulk Add/Share/Remove + Share-album"
```

---

## Task 20: SharesStore (data layer + polling)

**Files:**
- Create: `frontend/src/lib/shares/sharesStore.svelte.ts`
- Create: `frontend/src/lib/shares/sharesStore.test.ts`

- [ ] **Step 1: Write the failing tests**

Create `frontend/src/lib/shares/sharesStore.test.ts`:

```ts
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { SharesStore } from "./sharesStore.svelte";

function fakeClient(responses: Array<any>) {
  let i = 0;
  const calls: Array<{ method: string; path: string; opts: any }> = [];
  const handler = vi.fn(async (path: string, opts: any = {}) => {
    calls.push({ method: "h", path, opts });
    return responses[i++] ?? { data: null };
  });
  return { GET: handler, POST: handler, DELETE: handler, calls };
}

const baseRow = {
  uuid: "s1",
  target_type: "media_set" as const,
  target_album_id: null,
  target_summary: { label: "1 photo", item_count: 1 },
  grantee: { hub: "h", user_id: "b" },
  grantee_handle: "Bob",
  allow_download: false,
  label: "",
  created_at: "2026-04-28T00:00:00Z",
  expires_at: null,
  revoked_at: null,
  broker_status: "active" as const,
  broker_attempts: 0,
  broker_last_error: "",
};

describe("SharesStore.loadInitial", () => {
  it("fetches and stores list rows", async () => {
    const client = fakeClient([
      { data: { items: [baseRow], next_offset: null } },
    ]);
    const store = new SharesStore(client as any);
    await store.loadInitial();
    expect(store.scopes).toHaveLength(1);
    expect(store.scopes[0].uuid).toBe("s1");
    expect(store.exhausted).toBe(true);
  });
});

describe("SharesStore.create", () => {
  it("POSTs and refetches the list", async () => {
    const client = fakeClient([
      { data: { uuid: "new", ...baseRow, uuid: "new" } },
      { data: { items: [{ ...baseRow, uuid: "new" }], next_offset: null } },
    ]);
    const store = new SharesStore(client as any);
    await store.create({
      target_type: "media_set",
      media_ids: ["m1"],
      grantee: { hub: "h", user_id: "b" },
      label: "",
      allow_download: false,
    });
    expect(store.scopes).toHaveLength(1);
    expect(store.scopes[0].uuid).toBe("new");
  });
});

describe("SharesStore.revoke / retry", () => {
  it("revoke posts then refetches", async () => {
    const client = fakeClient([
      { data: { items: [baseRow], next_offset: null } },
      { data: null },
      { data: { items: [{ ...baseRow, broker_status: "revoking" }], next_offset: null } },
    ]);
    const store = new SharesStore(client as any);
    await store.loadInitial();
    await store.revoke("s1");
    expect(store.scopes[0].broker_status).toBe("revoking");
  });

  it("retry posts then refetches", async () => {
    const client = fakeClient([
      { data: { items: [{ ...baseRow, broker_status: "failed" }], next_offset: null } },
      { data: null },
      { data: { items: [{ ...baseRow, broker_status: "pending" }], next_offset: null } },
    ]);
    const store = new SharesStore(client as any);
    await store.loadInitial();
    await store.retry("s1");
    expect(store.scopes[0].broker_status).toBe("pending");
  });
});

describe("SharesStore polling", () => {
  beforeEach(() => { vi.useFakeTimers(); });
  afterEach(() => { vi.useRealTimers(); });

  it("starts polling when a row is pending", async () => {
    const client = fakeClient([
      { data: { items: [{ ...baseRow, broker_status: "pending" }], next_offset: null } },
      { data: { items: [{ ...baseRow, broker_status: "pending" }], next_offset: null } },
      { data: { items: [{ ...baseRow, broker_status: "active" }], next_offset: null } },
    ]);
    const store = new SharesStore(client as any);
    await store.loadInitial();
    // First poll tick
    await vi.advanceTimersByTimeAsync(5000);
    expect(store.scopes[0].broker_status).toBe("pending");
    // Second tick — broker activates the share
    await vi.advanceTimersByTimeAsync(5000);
    expect(store.scopes[0].broker_status).toBe("active");
    // Third tick — should NOT happen (poll auto-stops on settled)
    await vi.advanceTimersByTimeAsync(5000);
    expect(client.calls.length).toBe(3);
  });

  it("stops polling once all rows are settled", async () => {
    const client = fakeClient([
      { data: { items: [{ ...baseRow, broker_status: "active" }], next_offset: null } },
    ]);
    const store = new SharesStore(client as any);
    await store.loadInitial();
    await vi.advanceTimersByTimeAsync(20000);
    expect(client.calls.length).toBe(1);
  });
});

describe("SharesStore.getDetail / getPreview", () => {
  it("caches detail after first fetch", async () => {
    const client = fakeClient([
      { data: { uuid: "s1", ...baseRow, media_ids: ["m1"] } },
    ]);
    const store = new SharesStore(client as any);
    const det1 = await store.getDetail("s1");
    const det2 = await store.getDetail("s1");
    expect(client.calls.length).toBe(1);
    expect(det1).toBe(det2);
  });
});
```

- [ ] **Step 2: Run to verify failing**

```bash
cd frontend && bunx vitest run src/lib/shares/sharesStore.test.ts
```

Expected: FAIL — store does not exist.

- [ ] **Step 3: Implement SharesStore**

Create `frontend/src/lib/shares/sharesStore.svelte.ts`:

```ts
import type { Client } from "../api/client";

export type ShareTargetType = "media_set" | "album_live";
export type ShareBrokerStatus =
  | "pending" | "active" | "failed" | "revoking" | "revoked_remote";

export type ScopeListRow = {
  uuid: string;
  target_type: ShareTargetType;
  target_album_id: string | null;
  target_summary: { label: string; item_count?: number } | null;
  grantee: { hub: string; user_id: string };
  grantee_handle?: string;
  allow_download: boolean;
  label: string;
  created_at: string;
  expires_at: string | null;
  revoked_at: string | null;
  broker_status: ShareBrokerStatus;
  broker_attempts: number;
  broker_last_error: string;
};

export type ScopeDetail = ScopeListRow & { media_ids?: string[] };
export type SharePreview = unknown; // shape comes from /preview endpoint; consumer renders raw

const POLL_INTERVAL_MS = 5000;

export type CreateShareInput =
  | {
      target_type: "media_set";
      media_ids: string[];
      grantee: { hub: string; user_id: string };
      label: string;
      allow_download: boolean;
    }
  | {
      target_type: "album_live";
      album_id: string;
      grantee: { hub: string; user_id: string };
      label: string;
      allow_download: boolean;
    };

export class SharesStore {
  scopes = $state<ScopeListRow[]>([]);
  loading = $state(false);
  exhausted = $state(false);
  showRevoked = $state(false);
  albumIDFilter = $state<string | null>(null);

  private nextOffset: number | null = 0;
  private pollHandle: ReturnType<typeof setInterval> | null = null;
  private detailCache = new Map<string, ScopeDetail>();
  private previewCache = new Map<string, SharePreview>();

  constructor(private client: Pick<Client, "GET" | "POST" | "DELETE">) {}

  async loadInitial(): Promise<void> {
    this.scopes = [];
    this.nextOffset = 0;
    this.exhausted = false;
    await this.loadMore();
    this.maybeStartPolling();
  }

  async loadMore(): Promise<void> {
    if (this.loading || this.exhausted) return;
    this.loading = true;
    try {
      const query: Record<string, unknown> = {
        limit: 100,
        offset: this.nextOffset ?? 0,
        include_settled: this.showRevoked,
      };
      if (this.albumIDFilter) query.album_id = this.albumIDFilter;
      const res = await this.client.GET("/api/v1/shares", { params: { query } as never });
      if (res.error || !res.data) return;
      const data = res.data as { items?: ScopeListRow[]; next_offset?: number | null };
      const items = data.items ?? [];
      this.scopes = [...this.scopes, ...items];
      const next = data.next_offset ?? null;
      this.nextOffset = next;
      if (next === null) this.exhausted = true;
    } finally {
      this.loading = false;
    }
  }

  async create(input: CreateShareInput): Promise<void> {
    const res = await this.client.POST("/api/v1/shares", { body: input as never });
    if (res.error) throw res.error;
    await this.refetchListPreservingFilter();
    this.maybeStartPolling();
  }

  async revoke(uuid: string): Promise<void> {
    const res = await this.client.POST("/api/v1/shares/{uuid}/revoke", {
      params: { path: { uuid } } as never,
    });
    if (res.error) throw res.error;
    this.detailCache.delete(uuid);
    this.previewCache.delete(uuid);
    await this.refetchListPreservingFilter();
    this.maybeStartPolling();
  }

  async retry(uuid: string): Promise<void> {
    const res = await this.client.POST("/api/v1/shares/{uuid}/retry", {
      params: { path: { uuid } } as never,
    });
    if (res.error) throw res.error;
    this.detailCache.delete(uuid);
    await this.refetchListPreservingFilter();
    this.maybeStartPolling();
  }

  async getDetail(uuid: string): Promise<ScopeDetail | null> {
    const cached = this.detailCache.get(uuid);
    if (cached) return cached;
    const res = await this.client.GET("/api/v1/shares/{uuid}", {
      params: { path: { uuid } } as never,
    });
    if (res.error || !res.data) return null;
    const det = res.data as ScopeDetail;
    this.detailCache.set(uuid, det);
    return det;
  }

  async getPreview(uuid: string): Promise<SharePreview | null> {
    const cached = this.previewCache.get(uuid);
    if (cached) return cached;
    const res = await this.client.GET("/api/v1/shares/{uuid}/preview", {
      params: { path: { uuid } } as never,
    });
    if (res.error || !res.data) return null;
    this.previewCache.set(uuid, res.data);
    return res.data;
  }

  setShowRevoked(v: boolean): void {
    if (this.showRevoked === v) return;
    this.showRevoked = v;
    this.refetchListPreservingFilter();
  }

  setAlbumIDFilter(v: string | null): void {
    if (this.albumIDFilter === v) return;
    this.albumIDFilter = v;
    this.refetchListPreservingFilter();
  }

  private async refetchListPreservingFilter(): Promise<void> {
    this.scopes = [];
    this.nextOffset = 0;
    this.exhausted = false;
    await this.loadMore();
  }

  private maybeStartPolling(): void {
    const needs = this.scopes.some(
      (s) => s.broker_status === "pending" || s.broker_status === "revoking",
    );
    if (needs && this.pollHandle === null) {
      this.pollHandle = setInterval(() => this.poll(), POLL_INTERVAL_MS);
    } else if (!needs && this.pollHandle !== null) {
      clearInterval(this.pollHandle);
      this.pollHandle = null;
    }
  }

  private async poll(): Promise<void> {
    // Polling MUST NOT replace the list — that would drop rows the
    // user has already paginated past. Refetch the first 200 rows
    // (covers any sane pending-row count) and merge by uuid into the
    // current scopes. Older rows update on the next user-driven
    // loadMore (acceptable: settled rows don't change state, and
    // pending rows are almost always recent).
    //
    // include_settled is true regardless of the user's filter: a row
    // transitioning revoking → revoked must be observable so polling
    // can stop. If we honored showRevoked here, a revoked row would
    // drop out of the response and the local copy would stay stuck at
    // "revoking" forever, polling indefinitely. The user-facing filter
    // is applied in the route view, not at the polling boundary.
    const query: Record<string, unknown> = {
      limit: 200,
      offset: 0,
      include_settled: true,
    };
    if (this.albumIDFilter) query.album_id = this.albumIDFilter;
    const res = await this.client.GET("/api/v1/shares", { params: { query } as never });
    if (res.error || !res.data) return;
    const data = res.data as { items?: ScopeListRow[] };
    const fresh = new Map<string, ScopeListRow>();
    for (const row of data.items ?? []) fresh.set(row.uuid, row);
    this.scopes = this.scopes.map((row) => fresh.get(row.uuid) ?? row);
    this.maybeStartPolling();
  }

  stopPolling(): void {
    if (this.pollHandle !== null) {
      clearInterval(this.pollHandle);
      this.pollHandle = null;
    }
  }
}
```

- [ ] **Step 4: Run to verify passing**

```bash
cd frontend && bunx vitest run src/lib/shares/sharesStore.test.ts
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/lib/shares/sharesStore.svelte.ts frontend/src/lib/shares/sharesStore.test.ts
git commit -m "feat(shares): SharesStore with polling and lazy detail/preview"
```

---

## Task 21: /shares route + table + state pills + Revoke/Retry

**Files:**
- Create: `frontend/src/routes/SharesPage.svelte`
- Create: `frontend/src/lib/components/SharesList.svelte`
- Create: `frontend/src/lib/components/ShareStatePill.svelte`
- Create: `frontend/src/lib/components/ShareStatePill.test.ts`
- Modify: `frontend/src/lib/router/router.svelte.ts`
- Modify: `frontend/src/App.svelte`

- [ ] **Step 1: Add `/shares` route**

Edit `frontend/src/lib/router/router.svelte.ts`. Add:

```ts
{
  name: "shares",
  pattern: /^\/shares\/?$/,
  params: () => ({}),
},
```

Append router test:

```ts
it("matches /shares to the shares route", () => {
  const r = parseRoute("/shares");
  expect(r.name).toBe("shares");
});
```

- [ ] **Step 2: Implement ShareStatePill**

Create `frontend/src/lib/components/ShareStatePill.svelte`:

```svelte
<script lang="ts">
  import type { ScopeListRow } from "../shares/sharesStore.svelte";

  let { scope }: { scope: ScopeListRow } = $props();

  type Visual = { label: string; cls: string; icon: string; aria: string };

  const visual = $derived<Visual>(() => {
    // Expired override
    if (scope.expires_at) {
      const expired = new Date(scope.expires_at) < new Date();
      if (expired && scope.broker_status !== "revoked_remote") {
        return { label: "Expired", cls: "muted", icon: "⌛", aria: "Expired" };
      }
    }
    switch (scope.broker_status) {
      case "pending":  return { label: "Pending",  cls: "warn",   icon: "⧗", aria: "Pending publish" };
      case "active":   return { label: "Active",   cls: "ok",     icon: "✓", aria: "Active" };
      case "failed":   return { label: "Failed",   cls: "danger", icon: "!", aria: "Publish failed" };
      case "revoking": return { label: "Revoking…", cls: "orange", icon: "↻", aria: "Revoking" };
      case "revoked_remote":
      default:         return { label: "Revoked",  cls: "muted",  icon: "—", aria: "Revoked" };
    }
  });
</script>

<span class="pill {visual.cls}" aria-label={visual.aria}>
  <span class="icon" aria-hidden="true">{visual.icon}</span>
  <span class="label">{visual.label}</span>
</span>

<style>
  .pill {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    padding: 2px 8px;
    border-radius: 12px;
    font-size: 12px;
    border: 1px solid currentColor;
  }
  .pill.warn { color: var(--warn); }
  .pill.ok { color: var(--ok); }
  .pill.danger { color: var(--danger); }
  .pill.orange { color: var(--orange, #d97706); }
  .pill.muted { color: var(--text-muted); }
</style>
```

Create `frontend/src/lib/components/ShareStatePill.test.ts`:

```ts
import { render } from "@testing-library/svelte";
import { describe, it, expect } from "vitest";
import ShareStatePill from "./ShareStatePill.svelte";

const base = {
  uuid: "x", target_type: "media_set" as const, target_album_id: null,
  target_summary: null, grantee: { hub: "h", user_id: "u" },
  allow_download: false, label: "",
  created_at: "2026-04-28T00:00:00Z", expires_at: null, revoked_at: null,
  broker_attempts: 0, broker_last_error: "",
};

describe("ShareStatePill", () => {
  it("renders Active for active state", () => {
    const { getByLabelText, getByText } = render(ShareStatePill, {
      props: { scope: { ...base, broker_status: "active" as const } },
    });
    expect(getByLabelText("Active")).not.toBeNull();
    expect(getByText("Active")).not.toBeNull();
  });

  it("renders Expired override when expires_at is in the past", () => {
    const { getByLabelText } = render(ShareStatePill, {
      props: {
        scope: {
          ...base,
          broker_status: "active" as const,
          expires_at: "2020-01-01T00:00:00Z",
        },
      },
    });
    expect(getByLabelText("Expired")).not.toBeNull();
  });

  it("does NOT override to Expired when broker_status is revoked_remote", () => {
    const { getByLabelText } = render(ShareStatePill, {
      props: {
        scope: {
          ...base,
          broker_status: "revoked_remote" as const,
          expires_at: "2020-01-01T00:00:00Z",
        },
      },
    });
    expect(getByLabelText("Revoked")).not.toBeNull();
  });
});
```

- [ ] **Step 3: Implement SharesList**

Create `frontend/src/lib/components/SharesList.svelte`:

```svelte
<script lang="ts">
  import type { ScopeListRow } from "../shares/sharesStore.svelte";
  import ShareStatePill from "./ShareStatePill.svelte";

  let {
    scopes,
    onOpen,
    onRevoke,
    onRetry,
  }: {
    scopes: ScopeListRow[];
    onOpen: (uuid: string) => void;
    onRevoke: (uuid: string) => void;
    onRetry: (uuid: string) => void;
  } = $props();

  function fmtRelative(iso: string): string {
    const d = new Date(iso);
    const ms = Date.now() - d.getTime();
    const days = Math.floor(ms / (1000 * 60 * 60 * 24));
    if (days < 1) return "today";
    if (days === 1) return "yesterday";
    if (days < 30) return `${days} days ago`;
    return d.toLocaleDateString();
  }

  function granteeText(s: ScopeListRow): string {
    if (s.grantee_handle) return s.grantee_handle;
    return `${s.grantee.hub}:${s.grantee.user_id}`;
  }

  function labelText(s: ScopeListRow): string {
    if (s.label) return s.label;
    if (s.target_summary?.label) return s.target_summary.label;
    return "(unlabeled)";
  }

  function expired(s: ScopeListRow): boolean {
    if (!s.expires_at) return false;
    return new Date(s.expires_at) < new Date();
  }

  function canRevoke(s: ScopeListRow): boolean {
    if (s.broker_status === "revoking" || s.broker_status === "revoked_remote") return false;
    return true;
  }

  function canRetry(s: ScopeListRow): boolean {
    return s.broker_status === "failed";
  }
</script>

<table class="shares">
  <thead>
    <tr>
      <th>Label</th>
      <th>Type</th>
      <th>Grantee</th>
      <th>State</th>
      <th>Created</th>
      <th class="actions-col">Actions</th>
    </tr>
  </thead>
  <tbody>
    {#each scopes as s (s.uuid)}
      <tr onclick={() => onOpen(s.uuid)}>
        <td>{labelText(s)}</td>
        <td><span class="type">{s.target_type === "media_set" ? "Photos" : "Album"}</span></td>
        <td class="mono">{granteeText(s)}</td>
        <td><ShareStatePill scope={s} /></td>
        <td title={s.created_at}>{fmtRelative(s.created_at)}</td>
        <td class="actions-col" onclick={(e) => e.stopPropagation()}>
          {#if canRetry(s)}
            <button type="button" onclick={() => onRetry(s.uuid)}>Retry</button>
          {/if}
          {#if canRevoke(s)}
            <button type="button" class="danger" onclick={() => onRevoke(s.uuid)}>Revoke</button>
          {/if}
        </td>
      </tr>
    {/each}
  </tbody>
</table>

<style>
  table.shares { width: 100%; border-collapse: collapse; }
  th, td { padding: 8px 12px; text-align: left; border-bottom: 1px solid var(--border); }
  th { background: var(--bg-elevated); color: var(--text-muted); font-weight: 500; font-size: 12px; }
  tbody tr { cursor: pointer; }
  tbody tr:hover { background: var(--bg-elevated); }
  .actions-col { width: 1%; white-space: nowrap; }
  .type { color: var(--text-muted); font-size: 12px; }
  .mono { font-family: monospace; font-size: 13px; }
  .danger { color: var(--danger); border-color: var(--danger); }
</style>
```

- [ ] **Step 4: Implement SharesPage**

Create `frontend/src/routes/SharesPage.svelte`:

```svelte
<script lang="ts">
  import SharesList from "../lib/components/SharesList.svelte";
  import ConfirmModal from "../lib/components/ConfirmModal.svelte";
  import type { SharesStore } from "../lib/shares/sharesStore.svelte";

  let { sharesStore }: { sharesStore: SharesStore } = $props();

  let revokingUuid = $state<string | null>(null);

  // Same dual-guard pattern as AlbumsIndex: `scopes.length === 0`
  // prevents re-runs after a successful paginated first fetch,
  // `!exhausted` covers the truly-zero-shares case, both together avoid
  // the loadInitial loop.
  $effect(() => {
    if (
      sharesStore.scopes.length === 0 &&
      !sharesStore.loading &&
      !sharesStore.exhausted
    ) {
      sharesStore.loadInitial();
    }
  });

  function onOpen(uuid: string) {
    // Drawer wiring lands in Task 22.
    console.log("open share", uuid);
  }

  function onRevoke(uuid: string) {
    revokingUuid = uuid;
  }

  async function confirmRevoke() {
    if (!revokingUuid) return;
    await sharesStore.revoke(revokingUuid);
    revokingUuid = null;
  }

  async function onRetry(uuid: string) {
    await sharesStore.retry(uuid);
  }

  const revokingScope = $derived(
    revokingUuid ? sharesStore.scopes.find((s) => s.uuid === revokingUuid) : null,
  );
  const granteeName = $derived(
    revokingScope ? (revokingScope.grantee_handle ?? `${revokingScope.grantee.hub}:${revokingScope.grantee.user_id}`) : "",
  );
</script>

<header class="page-header">
  <h1>Shares</h1>
  <label class="show-revoked">
    <input
      type="checkbox"
      checked={sharesStore.showRevoked}
      onchange={(e) => sharesStore.setShowRevoked((e.currentTarget as HTMLInputElement).checked)}
    />
    <span>Show revoked</span>
  </label>
</header>

{#if sharesStore.scopes.length > 0}
  <SharesList scopes={sharesStore.scopes} {onOpen} {onRevoke} {onRetry} />
{:else if !sharesStore.loading}
  <div class="empty">No shares yet. Share photos or albums from the Library or an album page.</div>
{/if}

{#if sharesStore.loading}<div class="loading">Loading…</div>{/if}

{#if revokingUuid}
  <ConfirmModal
    title={`Revoke share to ${granteeName}?`}
    body="Photos will become inaccessible to them."
    confirmLabel="Revoke"
    danger
    onConfirm={confirmRevoke}
    onCancel={() => (revokingUuid = null)}
  />
{/if}

<style>
  .page-header { display: flex; justify-content: space-between; align-items: center; padding: 12px 16px; border-bottom: 1px solid var(--border); }
  .page-header h1 { margin: 0; font-size: 18px; }
  .show-revoked { display: flex; gap: 6px; align-items: center; font-size: 13px; color: var(--text-muted); }
  .empty { padding: 64px 16px; text-align: center; color: var(--text-secondary); }
  .loading { padding: 12px; color: var(--text-muted); }
</style>
```

- [ ] **Step 5: Wire SharesStore + SharesPage into App.svelte**

Edit `frontend/src/App.svelte`:

```svelte
<script lang="ts">
  // (existing)
  import { SharesStore } from "./lib/shares/sharesStore.svelte";
  import SharesPage from "./routes/SharesPage.svelte";

  const sharesStore = new SharesStore(api);
</script>

<!-- inside route switch: -->
{:else if router.current.name === "shares"}
  <SharesPage {sharesStore} />
```

- [ ] **Step 6: Verify in browser**

```bash
make dev
# 1. Create a share via MediaDetail (Task 17 wired this).
# 2. Navigate to /shares → see the row in the table.
# 3. State pill should be "Pending" or "Failed" (no broker configured).
# 4. Click Retry on a Failed row → state goes back to Pending.
# 5. Click Revoke → confirmation modal → confirm → state changes.
# 6. Toggle "Show revoked" → revoked rows appear.
```

- [ ] **Step 7: Commit**

```bash
git add frontend/src/lib/router/router.svelte.ts frontend/src/lib/router/router.test.ts frontend/src/lib/components/ShareStatePill.svelte frontend/src/lib/components/ShareStatePill.test.ts frontend/src/lib/components/SharesList.svelte frontend/src/routes/SharesPage.svelte frontend/src/App.svelte
git commit -m "feat(shares): /shares route with table, pills, revoke/retry"
```

---

## Task 22: Share drawer + ?album_id query filter

**Files:**
- Create: `frontend/src/lib/components/ShareDrawer.svelte`
- Modify: `frontend/src/routes/SharesPage.svelte`
- Modify: `frontend/src/lib/router/router.svelte.ts`

- [ ] **Step 1: Add `?album_id` query parsing to router**

Edit `frontend/src/lib/router/router.svelte.ts`. The `/shares` matcher should expose `query` from `location.search`. Update the route entry:

```ts
{
  name: "shares",
  pattern: /^\/shares\/?$/,
  params: () => {
    const sp = new URLSearchParams(window.location.search);
    return {
      album_id: sp.get("album_id") ?? undefined,
      show_revoked: sp.get("show_revoked") === "true",
    };
  },
},
```

(If the router currently doesn't support per-route param functions reading `location.search`, add minimal support there. The pattern: `params(match)` already takes match groups; extend to also read `window.location.search` when needed.)

Append router test:

```ts
it("parses ?album_id from /shares", () => {
  Object.defineProperty(window, "location", {
    value: { search: "?album_id=abc-123", pathname: "/shares" },
    writable: true,
  });
  const r = parseRoute("/shares");
  expect(r.name).toBe("shares");
  expect(r.params.album_id).toBe("abc-123");
});
```

- [ ] **Step 2: Implement ShareDrawer**

Create `frontend/src/lib/components/ShareDrawer.svelte`:

```svelte
<script lang="ts">
  import type {
    ScopeListRow,
    ScopeDetail,
    SharesStore,
  } from "../shares/sharesStore.svelte";
  import { handleInternalLinkClick } from "../router/router.svelte";
  import ShareStatePill from "./ShareStatePill.svelte";

  let {
    scope,
    sharesStore,
    onClose,
    onRevoke,
    onRetry,
  }: {
    scope: ScopeListRow;
    sharesStore: SharesStore;
    onClose: () => void;
    onRevoke: (uuid: string) => void;
    onRetry: (uuid: string) => void;
  } = $props();

  let detail = $state<ScopeDetail | null>(null);
  let preview = $state<unknown | null>(null);
  let detailLoading = $state(false);
  let previewLoading = $state(false);

  $effect(() => {
    detail = null;
    detailLoading = true;
    sharesStore.getDetail(scope.uuid).then((d) => {
      detail = d;
      detailLoading = false;
    });
  });

  function granteeText(s: ScopeListRow): string {
    if (s.grantee_handle) return s.grantee_handle;
    return `${s.grantee.hub}:${s.grantee.user_id}`;
  }

  async function loadPreview() {
    previewLoading = true;
    preview = await sharesStore.getPreview(scope.uuid);
    previewLoading = false;
  }

  const summaryLabel = $derived(scope.target_summary?.label ?? "(unknown)");
</script>

<aside class="drawer" role="complementary">
  <header>
    <button type="button" class="close" onclick={onClose} aria-label="Close drawer">×</button>
    <h2>{scope.label || summaryLabel}</h2>
    <ShareStatePill {scope} />
  </header>

  <dl>
    <dt>Grantee</dt>
    <dd class="mono">{granteeText(scope)}</dd>
    <dt>Created</dt>
    <dd>{new Date(scope.created_at).toLocaleString()}</dd>
    {#if scope.allow_download}
      <dt>Download</dt><dd>Allowed</dd>
    {/if}
    {#if scope.broker_status === "failed"}
      <dt>Attempts</dt><dd>{scope.broker_attempts}</dd>
      {#if scope.broker_last_error}
        <dt>Last error</dt><dd class="error">{scope.broker_last_error}</dd>
      {/if}
    {/if}
  </dl>

  {#if scope.target_type === "album_live" && scope.target_album_id}
    <a
      href={`/albums/${scope.target_album_id}`}
      onclick={(e) => handleInternalLinkClick(e, `/albums/${scope.target_album_id}`)}
    >
      Open album →
    </a>
  {:else if scope.target_type === "media_set"}
    {#if !preview}
      <button type="button" onclick={loadPreview} disabled={previewLoading}>
        {previewLoading ? "Loading preview…" : "Preview"}
      </button>
    {:else}
      <div class="preview">
        <pre>{JSON.stringify(preview, null, 2)}</pre>
      </div>
    {/if}
  {/if}

  <div class="actions">
    {#if scope.broker_status === "failed"}
      <button type="button" onclick={() => onRetry(scope.uuid)}>Retry</button>
    {/if}
    {#if scope.broker_status !== "revoking" && scope.broker_status !== "revoked_remote"}
      <button type="button" class="danger" onclick={() => onRevoke(scope.uuid)}>Revoke</button>
    {/if}
  </div>
</aside>

<style>
  .drawer {
    position: fixed; right: 0; top: 0; bottom: 0; width: 360px;
    background: var(--bg); border-left: 1px solid var(--border);
    padding: 16px; overflow-y: auto; z-index: 50;
    display: flex; flex-direction: column; gap: 12px;
  }
  .drawer header { display: flex; align-items: center; gap: 8px; }
  .drawer h2 { margin: 0; font-size: 16px; flex: 1; }
  .close { background: transparent; border: 0; font-size: 20px; cursor: pointer; color: var(--text-muted); }
  dl { display: grid; grid-template-columns: max-content 1fr; gap: 4px 12px; font-size: 13px; }
  dt { color: var(--text-muted); }
  dd { margin: 0; }
  .mono { font-family: monospace; font-size: 12px; }
  .error { color: var(--danger); white-space: pre-wrap; }
  .preview pre { background: var(--bg-elevated); padding: 8px; font-size: 11px; overflow: auto; border-radius: 4px; }
  .actions { display: flex; gap: 8px; }
  .danger { color: var(--danger); border-color: var(--danger); }
</style>
```

- [ ] **Step 3: Wire ShareDrawer + ?album_id filter into SharesPage**

Edit `frontend/src/routes/SharesPage.svelte`. Add the drawer + filter wiring:

```svelte
<script lang="ts">
  // (existing imports)
  import ShareDrawer from "../lib/components/ShareDrawer.svelte";
  import { router } from "../lib/router/router.svelte";

  let { sharesStore }: { sharesStore: SharesStore } = $props();

  let openUuid = $state<string | null>(null);
  let revokingUuid = $state<string | null>(null);

  // Sync filter from route params on mount.
  $effect(() => {
    const params = router.current.params as { album_id?: string; show_revoked?: boolean };
    sharesStore.setAlbumIDFilter(params.album_id ?? null);
    if (params.show_revoked) sharesStore.setShowRevoked(true);
  });

  // (existing scopes effect, onRevoke, etc.)

  function onOpen(uuid: string) { openUuid = uuid; }
  const openScope = $derived(openUuid ? sharesStore.scopes.find((s) => s.uuid === openUuid) ?? null : null);
</script>

<!-- Inside the page-header, add an active-filter banner if albumIDFilter is set: -->
{#if sharesStore.albumIDFilter}
  <div class="filter-banner">
    Showing shares for album <code>{sharesStore.albumIDFilter}</code>
    <button type="button" onclick={() => sharesStore.setAlbumIDFilter(null)}>Clear filter</button>
  </div>
{/if}

<!-- After </header>, after the SharesList block, add the drawer: -->
{#if openScope}
  <ShareDrawer
    scope={openScope}
    {sharesStore}
    onClose={() => (openUuid = null)}
    onRevoke={(uuid) => { revokingUuid = uuid; openUuid = null; }}
    onRetry={onRetry}
  />
{/if}

<style>
  /* (existing styles) */
  .filter-banner {
    background: var(--bg-elevated);
    padding: 8px 16px;
    display: flex;
    gap: 12px;
    align-items: center;
    font-size: 13px;
    border-bottom: 1px solid var(--border);
  }
  .filter-banner code { font-family: monospace; }
</style>
```

- [ ] **Step 4: Verify in browser**

```bash
make dev
# 1. /shares → click a row → drawer opens with grantee, created date,
#    allow-download badge if true.
# 2. For media_set: click Preview → preview JSON renders.
# 3. For album_live: see "Open album →" link, clicking routes to /albums/<id>.
# 4. Failed share: drawer shows attempts + last error.
# 5. Test the album-delete deep link: create an album, share it
#    (album_live), revoke nothing, then navigate to /albums/<id>,
#    click Delete → 409 toast → click "View shares →" → routes to
#    /shares?album_id=<id> → list filtered, banner visible.
```

- [ ] **Step 5: Commit**

```bash
git add frontend/src/lib/components/ShareDrawer.svelte frontend/src/routes/SharesPage.svelte frontend/src/lib/router/router.svelte.ts frontend/src/lib/router/router.test.ts
git commit -m "feat(shares): drawer + ?album_id filter for delete-conflict deep link"
```

---

## Task 23: e2e fixtures + playwright tests for albums and shares

**Files:**
- Modify: `cmd/e2e-server/main.go`
- Create: `frontend/tests/e2e/albums.spec.ts`
- Create: `frontend/tests/e2e/shares.spec.ts`

- [ ] **Step 1: Seed albums and shares fixtures**

Edit `cmd/e2e-server/main.go`. Add seeding alongside the existing media seeds (look for the F2.2 `pair-fixture-primary` / `pair-fixture-sidecar` block as a model):

```go
// Albums seed: create one empty album so the e2e suite can verify
// the empty-album view; add-via-multi-select tests will populate
// other albums dynamically.
emptyAlbum, err := albumSvc.Create(ctx, "E2E Empty Album", caller)
if err != nil {
    log.Fatalf("seed empty album: %v", err)
}
seededAlbum, err := albumSvc.Create(ctx, "E2E Italy 2025", caller)
if err != nil {
    log.Fatalf("seed populated album: %v", err)
}
if err := albumSvc.AddMedia(ctx, seededAlbum.ID, []string{
    "library-fixture-1", "library-fixture-2",
}, caller); err != nil {
    log.Fatalf("seed populated album members: %v", err)
}

// Shares seed: one active media_set share with a "noop:e2e" grantee,
// and one failed share so the tests exercise both Retry and Revoke.
_, err = shareSvc.Create(ctx, service.CreateShareRequest{
    TargetType: share.TargetMediaSet,
    MediaIDs:   []string{"library-fixture-1"},
    Grantee:    owners.Principal{Hub: "noop", UserID: "e2e"},
    Label:      "Active e2e share",
}, caller)
if err != nil {
    log.Fatalf("seed active share: %v", err)
}
// (Failed share fixture: depends on whether brokertest.Fake supports
// scripted failure here. If brokertest is not wired into e2e-server
// today, skip the failed-share fixture and rely on the fact that
// without a real broker the noop one will let the share sit pending,
// which exercises the polling path. The Failed/Retry paths can be
// covered by component tests in Task 20 instead.)
```

(If `brokertest.Fake` is wired into e2e-server, create a second share that fails; otherwise leave it out — component tests in Task 20 cover the Failed/Retry path.)

- [ ] **Step 2: Write the album e2e test**

Create `frontend/tests/e2e/albums.spec.ts`:

```ts
import { test, expect } from "@playwright/test";

test.describe("F2.3 albums", () => {
  test("create album, add 3 photos, switch sort, remove 1, delete", async ({ page }) => {
    await page.goto("/albums");
    await expect(page.getByRole("heading", { name: "Albums" })).toBeVisible();

    // Create a fresh album.
    await page.getByRole("button", { name: "+ New Album" }).click();
    await page.getByLabel("Name").fill("Trip 2026");
    await page.getByRole("button", { name: "Create" }).click();
    await expect(page.getByText("Trip 2026")).toBeVisible();

    // Multi-select 3 photos in Library, then bulk Add to album.
    await page.goto("/library");
    await page.locator(".media-cell").first().click({ modifiers: ["Meta"] });
    await page.locator(".media-cell").nth(1).click({ modifiers: ["Meta"] });
    await page.locator(".media-cell").nth(2).click({ modifiers: ["Meta"] });
    await expect(page.getByText("3 selected")).toBeVisible();
    await page.getByRole("button", { name: "Add to album" }).click();
    await page.getByText("Trip 2026").click(); // select target
    await page.getByRole("button", { name: "Add 3 photos" }).click();

    // Open the album and verify all 3 are visible.
    await page.goto("/albums");
    await page.getByText("Trip 2026").click();
    await expect(page.locator(".media-cell")).toHaveCount(3);

    // Switch sort to Recently added; refetch happens.
    await page.getByLabel("Sort:").selectOption("added");
    await expect(page.locator(".media-cell")).toHaveCount(3);

    // Multi-select 1 photo, click Remove from this album.
    await page.locator(".media-cell").first().click({ modifiers: ["Meta"] });
    await page.getByRole("button", { name: "Remove from this album" }).click();
    await expect(page.locator(".media-cell")).toHaveCount(2);

    // Delete the album.
    await page.getByRole("button", { name: "Delete" }).click();
    await page.getByRole("button", { name: "Delete" }).click(); // confirmation
    await expect(page).toHaveURL(/\/albums$/);
    await expect(page.getByText("Trip 2026")).not.toBeVisible();
  });

  test("bulk-select-by-group via month header", async ({ page }) => {
    await page.goto("/library");
    const firstHeader = page.locator(".day-header").first();
    await expect(firstHeader).toBeVisible();
    const groupBtn = firstHeader.getByRole("button", { name: /^Select \d+ photos in/ });
    await groupBtn.click();
    // Selection count in ActionBar > 0 (don't pin exact count — depends
    // on fixture data; just confirm the bar is showing a non-empty count).
    await expect(page.locator(".count")).toContainText("selected");
  });

  test("MediaActions hidden on sidecar direct page", async ({ page }) => {
    // F2.2 fixture: pair-fixture-sidecar is a sidecar with paired_with_id set.
    await page.goto("/media/pair-fixture-sidecar");
    await expect(page.getByText("RAW")).toBeVisible(); // banner from F2.2
    await expect(page.getByRole("button", { name: "Add to album" })).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Share" })).toHaveCount(0);
  });
});
```

- [ ] **Step 3: Write the shares e2e test**

Create `frontend/tests/e2e/shares.spec.ts`:

```ts
import { test, expect } from "@playwright/test";

test.describe("F2.3 owner-side sharing", () => {
  test("create media-set share from MediaDetail, see it in /shares, revoke", async ({ page }) => {
    await page.goto("/media/library-fixture-1");
    await page.getByRole("button", { name: "Share" }).click();

    // ShareModal opens — type grantee, click Create.
    await page.getByPlaceholder("myhub:bob").fill("noop:test-grantee");
    await page.getByLabel("Label").fill("Test share");
    await page.getByRole("button", { name: "Create share" }).click();

    // Navigate to /shares and verify the row appeared.
    await page.goto("/shares");
    await expect(page.getByText("Test share")).toBeVisible();
    await expect(page.locator(".pill", { hasText: /Pending|Active|Failed/ })).toBeVisible();

    // Revoke the share.
    const row = page.locator("tr", { has: page.getByText("Test share") });
    await row.getByRole("button", { name: "Revoke" }).click();
    await page.getByRole("button", { name: "Revoke" }).click(); // confirmation
    // Polling will eventually flip the state; for the test, just verify
    // the confirmation modal closed and the row reflects the revoke
    // intent (Revoking… or Revoked).
    await expect(row.locator(".pill")).toHaveText(/Revoking|Revoked/);
  });

  test("delete-album-with-active-share toast deep links to /shares?album_id", async ({ page }) => {
    // Create an album and share it.
    await page.goto("/albums");
    await page.getByRole("button", { name: "+ New Album" }).click();
    await page.getByLabel("Name").fill("Conflict Album");
    await page.getByRole("button", { name: "Create" }).click();
    await page.getByText("Conflict Album").click();
    // Need at least 1 photo for share-album; bulk-add via library:
    await page.goto("/library");
    await page.locator(".media-cell").first().click({ modifiers: ["Meta"] });
    await page.getByRole("button", { name: "Add to album" }).click();
    await page.getByText("Conflict Album").click();
    await page.getByRole("button", { name: "Add 1 photo" }).click();
    await page.goto("/albums");
    await page.getByText("Conflict Album").click();
    await page.getByRole("button", { name: "Share album" }).click();
    await page.getByPlaceholder("myhub:bob").fill("noop:conflict");
    await page.getByRole("button", { name: "Create share" }).click();

    // Now try to delete the album.
    await page.getByRole("button", { name: "Delete" }).click();
    await page.getByRole("button", { name: "Delete" }).click(); // confirm
    await expect(page.getByText(/active shares/)).toBeVisible();
    await page.getByRole("link", { name: /View shares/ }).click();
    await expect(page).toHaveURL(/\/shares\?album_id=/);
    await expect(page.getByText(/Showing shares for album/)).toBeVisible();
  });

  test("share drawer opens lazily on row click", async ({ page }) => {
    await page.goto("/shares");
    const firstRow = page.locator("tbody tr").first();
    await firstRow.click();
    await expect(page.getByRole("complementary")).toBeVisible();
    await expect(page.locator(".drawer dt", { hasText: "Grantee" })).toBeVisible();
  });
});
```

- [ ] **Step 4: Run the e2e suite**

```bash
cd frontend && bunx playwright test tests/e2e/albums.spec.ts tests/e2e/shares.spec.ts
```

Expected: PASS (all three album scenarios + three share scenarios). If any fails, debug — these tests are the primary correctness gate for the integrated UI.

- [ ] **Step 5: Final go test sweep**

```bash
make test
```

Expected: PASS.

- [ ] **Step 6: Final lint**

```bash
make lint
cd frontend && bunx oxlint src/ && bunx tsc --noEmit
```

Expected: clean.

- [ ] **Step 7: Refresh stale CLAUDE.md note about Plan D / E**

Edit `CLAUDE.md`. Find the "Plans" section and update:

```
- **Plan D** — Albums (CRUD service + HTTP + CLI; no sharing). **Done.**
- **Plan E** — Sharing: scopes, broker registration, outbox worker, cross-owner reads. **Done (owner side; grantee-side viewing deferred).**
```

(The earlier note marking these as "in design" / "deferred" was stale even before F2.3; this brings it current.)

- [ ] **Step 8: Commit**

```bash
git add cmd/e2e-server/main.go frontend/tests/e2e/albums.spec.ts frontend/tests/e2e/shares.spec.ts CLAUDE.md
git commit -m "test(e2e): albums + shares scenarios; refresh Plans note"
```

- [ ] **Step 9: Final roborev sweep**

After Task 23 commits, run:

```bash
roborev fix --open --list
```

Address any actionable findings on Tasks 1–23 commits. After each fix:

```bash
roborev comment --commenter roborev-fix --job <id> "<summary>"
roborev close <id>
```

Per CLAUDE.md, F2.3 commits directly to master with no branches. Per the F2.0/F2.1/F2.2 cadence, run `/roborev-fix` after every 5 tasks during implementation (after Task 5, Task 10, Task 15, Task 20, and finally after Task 23).

---
