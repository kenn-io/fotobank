# Plan D: Albums — Design Spec

**Date:** 2026-04-23
**Scope:** Phase 1 albums subsystem, standalone from sharing.
**Depends on:** Plans A (foundation), B (import), C (thumbnails) — all complete.
**Followed by:** Plan E (sharing — scopes, broker stub, outbox worker).

## 1. Goal and non-goals

### Goal

Ship owner-scoped albums: lightweight, manually curated collections of the caller's own media. Plan D delivers a CRUD HTTP API, a CLI command surface, and the service/repo layer a future web app will consume. An album is "a named pointer to a set of this owner's media rows"; no files move, no bytes are copied.

### Non-goals

- **Sharing.** Scopes, grants, broker registration, and cross-owner reads are Plan E.
- **Reordering UX.** The schema's `album_media.position` column is preserved for future use but no operation in Plan D reads or writes it; list order is always `added_at DESC` by default.
- **Owner-picked covers.** Cover thumbs are derived from the album's members (most recently added, `thumb_status='ready'`). No `cover_media_id` column, no cover-selection endpoint.
- **Album metadata beyond name.** No description, no tags, no color — just the schema's existing `name`, timestamps, and owner fields.
- **Smart albums / saved queries.** "Every photo from my Canon in 2024" is Phase 4+.
- **Nested albums / folders.** Flat namespace per owner.

## 2. Prior art already in the tree

The `albums` and `album_media` tables plus owner-consistency triggers (insert + update) were landed in Plan A's initial migration. Plan D adds Go code, HTTP routes, a CLI, and **two new indexes** (see "New indexes" in §5) — no table or trigger changes. The trigger already aborts any insert where the album and media have different owners. In Plan D, `AlbumService` is the primary ownership guard (a pre-flight `media.Repo.GetByID` per ID, returning `errs.ErrNotFound` for any miss or cross-owner row); the SQLite trigger is the last-line defence-in-depth in case the pre-flight misses a row, in which case the repo wraps the raw trigger error as `errs.ErrOwnerMismatch` and the HTTP layer logs + returns 500. See §6 for the full rationale.

## 3. Architecture and package layout

New files, all mirroring existing patterns established in Plans B and C:

```
internal/album/
├── album.go            — Album, AlbumListItem, CoverRef, AlbumMediaEntry, package sentinels
└── repo.go             — *album.Repo: CRUD + AddMedia/RemoveMedia/ListMedia/ListByOwner

internal/db/migrations/
└── 000002_album_indexes.up.sql   — two new indexes (see "New indexes" in §5)

internal/service/album_service.go
                        — *AlbumService: auth-scoped wrapper, forces caller-owner everywhere

internal/httpapi/albums.go
                        — huma-registered routes: CRUD + album-media list/add/remove

internal/cli/albums.go
                        — cobra subcommands (create/rename/delete/list/show/add/remove)
```

Layering identical to the media stack:
- `album.Repo` is DB-only. No auth, no identity plumbing. Every method takes `id`-style arguments without knowing which owner they should belong to.
- `service.AlbumService` is the auth boundary. Every exported method takes `caller owners.Principal` and either scopes queries to that principal or returns `errs.ErrNotFound` for cross-owner access. **Both the HTTP handlers and the CLI go through `AlbumService`** — the CLI does not touch `album.Repo` directly, because repo methods do not enforce ownership. A bug or CLI argument typo (e.g. feeding in a UUID that belongs to a different owner) must not let stub user A mutate user B's rows.
- HTTP handlers marshal request/response DTOs and delegate.
- CLI commands construct `caller` from `cfg.Identity.Stub.{Hub,UserID}` and pass it into every `AlbumService` method.

`AlbumService` takes `*album.Repo` and `*media.Repo` (the latter for the pre-flight ownership check on `AddMedia`). All routes are huma-registered, matching the JSON+OpenAPI pattern established by `/api/v1/media` CRUD. No new background goroutines.

## 4. Domain types

```go
// internal/album/album.go

type Album struct {
    ID        string           // uuid
    Owner     owners.Principal
    Name      string
    CreatedAt time.Time
    UpdatedAt time.Time
}

// AlbumListItem is the shape returned by ListByOwner. ItemCount and
// Cover are derived in SQL so the web app can render a grid without
// N+1 calls.
type AlbumListItem struct {
    Album
    ItemCount int
    Cover     *CoverRef // nil when album is empty OR has no ready thumb yet
}

// CoverRef is the minimum the web app needs to construct
// /api/v1/media/{MediaID}/thumb?size=grid&v={ThumbVersion}
type CoverRef struct {
    MediaID      string
    ThumbVersion int
}
```

## 5. Repo surface

```go
// internal/album/repo.go

type Repo struct { rw, ro *sql.DB }

func NewRepo(rw, ro *sql.DB) *Repo

// CRUD
func (r *Repo) Insert(ctx context.Context, a Album) error
func (r *Repo) GetByID(ctx context.Context, id string) (Album, error)           // ErrNotFound on miss
func (r *Repo) Rename(ctx context.Context, id, name string, now time.Time) error // updates updated_at
func (r *Repo) Delete(ctx context.Context, id string) error                       // cascades album_media

// List albums for one owner with derived ItemCount and Cover.
func (r *Repo) ListByOwner(
    ctx context.Context,
    owner owners.Principal,
    limit, offset int,
) ([]AlbumListItem, error)

// album_media side
// AddMedia is idempotent: already-present ids are no-op'd, not errored.
// Returns (added, alreadyPresent) based on RowsAffected + input size.
func (r *Repo) AddMedia(
    ctx context.Context,
    albumID string,
    mediaIDs []string,
    now time.Time,
) (added, alreadyPresent int, err error)

func (r *Repo) RemoveMedia(ctx context.Context, albumID, mediaID string) error // ErrNotFound if not in album

// ListMedia returns paginated media rows for the album.
func (r *Repo) ListMedia(
    ctx context.Context,
    albumID string,
    filter AlbumMediaFilter,
) ([]media.Media, error)

type AlbumMediaFilter struct {
    Limit    int
    Offset   int
    SortBy   string // "added" (default) | "imported"
    SortDesc bool   // default true
}
```

### Key implementation notes

#### `ListByOwner` cover derivation

One SQL statement. The outer CTE filters `albums` to the caller's rows **first** so the count/cover subqueries only see the tiny per-owner set, not the global `album_media` table. Covers and counts are derived via correlated window subqueries:

```sql
WITH owner_albums AS (
  SELECT id, owner_hub, owner_user_id, name, created_at, updated_at
    FROM albums
   WHERE owner_hub = ? AND owner_user_id = ?
   ORDER BY updated_at DESC, id ASC
   LIMIT ? OFFSET ?
)
SELECT oa.id, oa.owner_hub, oa.owner_user_id, oa.name, oa.created_at, oa.updated_at,
       COALESCE(cnt.n, 0) AS item_count,
       cv.media_id, cv.thumb_version
  FROM owner_albums oa
  LEFT JOIN (
    SELECT album_id, COUNT(*) AS n
      FROM album_media
     WHERE album_id IN (SELECT id FROM owner_albums)
     GROUP BY album_id
  ) cnt ON cnt.album_id = oa.id
  LEFT JOIN (
    SELECT am.album_id, am.media_id, m.thumb_version,
           ROW_NUMBER() OVER (
             PARTITION BY am.album_id
             ORDER BY am.added_at DESC, am.media_id ASC
           ) AS rn
      FROM album_media am
      JOIN media m ON m.id = am.media_id
     WHERE am.album_id IN (SELECT id FROM owner_albums)
       AND m.thumb_status = 'ready'
  ) cv ON cv.album_id = oa.id AND cv.rn = 1;
```

Cover is null for empty albums or albums whose only members are still `pending`/`working`.

#### `AddMedia` idempotency and input dedupe

1. Service deduplicates `mediaIDs` on entry (preserving first-seen order). A request like `[X, X, Y]` is treated as `[X, Y]`. The caller's intent is "these items should be in the album"; duplicating an ID in the request body is not meaningful.
2. Single batched `INSERT INTO album_media(album_id, media_id, added_at) VALUES (?,?,?), ... ON CONFLICT (album_id, media_id) DO NOTHING`. Reports `added = RowsAffected()`; `alreadyPresent = len(deduped) - added`.
3. Empty input is a no-op returning `(0, 0, nil)`.

#### New indexes (Plan D migration)

The existing `albums_owner_idx(owner_hub, owner_user_id, name)` and `album_media` primary key do not cover the Plan D query patterns. Two new indexes land as `internal/db/migrations/000002_album_indexes.up.sql`:

```sql
-- Covers ListByOwner's ORDER BY updated_at DESC, id after owner filter.
CREATE INDEX albums_owner_updated_idx
    ON albums(owner_hub, owner_user_id, updated_at DESC, id);

-- Covers the cover subquery and ListMedia's sort_by=added path
-- (most-recent-added-first per album).
CREATE INDEX album_media_album_added_idx
    ON album_media(album_id, added_at DESC);
```

`ListMedia` with `sort_by=imported` uses the existing `media_owner_imported_idx` after joining on `album_media(album_id, ...)`.

## 6. Service surface

```go
// internal/service/album_service.go

type AlbumService struct {
    albums *album.Repo
    media  *media.Repo
}

func NewAlbumService(a *album.Repo, m *media.Repo) *AlbumService

// Create returns the new album with derived fields (ItemCount=0, Cover=nil).
// Name is trimmed + length-validated.
func (s *AlbumService) Create(ctx context.Context, caller owners.Principal, name string) (album.AlbumListItem, error)

// Get returns the bare album; ErrNotFound if missing OR not owned by caller.
func (s *AlbumService) Get(ctx context.Context, id string, caller owners.Principal) (album.Album, error)

// GetDetail returns the album with ItemCount + Cover — the shape used by
// HTTP detail endpoints and the response body after a successful Rename.
// ErrNotFound if missing OR not owned by caller.
func (s *AlbumService) GetDetail(ctx context.Context, id string, caller owners.Principal) (album.AlbumListItem, error)

// Rename validates the new name, updates the row, and returns the updated
// detail (with current ItemCount + Cover). ErrNotFound if the album does
// not belong to the caller.
func (s *AlbumService) Rename(ctx context.Context, id, name string, caller owners.Principal) (album.AlbumListItem, error)

// Delete removes the album (album_media cascades). ErrNotFound for
// cross-owner or missing.
func (s *AlbumService) Delete(ctx context.Context, id string, caller owners.Principal) error

// List returns the caller's albums with ItemCount + Cover.
func (s *AlbumService) List(
    ctx context.Context,
    caller owners.Principal,
    limit, offset int,
) ([]album.AlbumListItem, error)

// AddMedia validates the album is caller-owned AND every deduped media_id
// exists AND belongs to the caller (pre-flight, before the DB trigger).
// Returns counts of inserted vs. already-present rows.
//
// Errors:
//   - errs.ErrNotFound if the album is missing/cross-owner, if any media_id
//     does not exist, OR if any media_id exists but belongs to a different
//     owner. Mixed-owner input is reported as ErrNotFound so the response
//     does not leak existence of rows the caller does not own.
//   - errs.ErrInvalidBatch if the deduped batch is empty or > 500.
//
// errs.ErrOwnerMismatch is reserved for the DB trigger path (defence in
// depth): if the pre-flight somehow misses a cross-owner row, the trigger
// aborts the insert and the repo wraps the raw SQLite error as
// ErrOwnerMismatch. This is an internal invariant violation, not a user
// error — it maps to HTTP 500, not 403.
func (s *AlbumService) AddMedia(
    ctx context.Context,
    albumID string,
    mediaIDs []string,
    caller owners.Principal,
) (added, alreadyPresent int, err error)

// RemoveMedia removes one media row from the album. ErrNotFound if
// the album is not caller-owned, if the media is not in the album,
// or if the album does not exist.
func (s *AlbumService) RemoveMedia(
    ctx context.Context,
    albumID, mediaID string,
    caller owners.Principal,
) error

// ListMedia returns paginated media rows. Auth checked via the album's
// owner; ErrNotFound if cross-owner.
func (s *AlbumService) ListMedia(
    ctx context.Context,
    albumID string,
    filter album.AlbumMediaFilter,
    caller owners.Principal,
) ([]media.Media, error)
```

### Validation rules

- `name`: trimmed; 1 ≤ len ≤ 200 chars after trim. Empty or oversized → `ErrInvalidName`.
- Duplicate names within one owner are **allowed**. Humans reuse names ("Dog photos", "Trip"). No unique constraint.
- `mediaIDs` batch: the service deduplicates the input slice (preserving first-seen order) before length-checking. After dedupe, 1 ≤ len ≤ 500. Empty or > 500 → `ErrInvalidBatch`. A request with 10 IDs that collapse to 3 distinct IDs is a valid 3-ID batch.
- `SortBy`: `"added"` or `"imported"`. Anything else → `ErrInvalidSort` at the service boundary (the HTTP layer translates to 400).

### Ownership checks

`AddMedia` fetches each deduped `media_id`'s owner via `media.Repo.GetByID` before the insert. Any miss (row does not exist) or cross-owner row returns `errs.ErrNotFound`. The endpoint must not reveal whether an unknown `media_id` is missing, belongs to another user, or is a typo — all three collapse to the same 404.

`errs.ErrOwnerMismatch` is reserved for defence-in-depth: if the pre-flight check misses a cross-owner row (race, bug, future refactor), the SQLite trigger aborts the INSERT and `album.Repo.AddMedia` wraps the raw error as `ErrOwnerMismatch`. A user request should never see this — it means the service-layer invariant is violated, and the HTTP layer maps it to 500 plus a log line, not 403.

`Get`, `GetDetail`, `Rename`, `Delete`, `RemoveMedia`, and `ListMedia` all return `ErrNotFound` for cross-owner IDs. There are no user-facing 403s anywhere in the albums surface.

## 7. HTTP API surface

All routes are huma-registered. The only endpoints on fotobank's HTTP surface that *aren't* huma are the ones that stream raw bytes (`/original`, `/thumb`); everything album-related is JSON in, JSON out, so there is no reason to drop out of huma. Going all-huma means the routes appear in `/openapi.json` (the web app's client-gen source of truth) and use the project-wide huma error shape via `huma.Error{4,5}xx` constructors.

| Method | Path | Behavior |
|---|---|---|
| `GET` | `/api/v1/albums?limit=&offset=` | list albums w/ cover |
| `POST` | `/api/v1/albums` | create; body `{"name": "..."}`; returns 201 + `albumDTO` |
| `GET` | `/api/v1/albums/{id}` | detail; 404 if missing or cross-owner |
| `PATCH` | `/api/v1/albums/{id}` | rename; body `{"name": "..."}`; returns 200 + `albumDTO` |
| `DELETE` | `/api/v1/albums/{id}` | delete (cascades album_media); returns 204 |
| `GET` | `/api/v1/albums/{id}/media?limit=&offset=&sort_by=&sort_desc=` | paginated `mediaDTO` list |
| `POST` | `/api/v1/albums/{id}/media` | batch add, body `{"media_ids": [...]}`; returns 200 + add-result |
| `DELETE` | `/api/v1/albums/{id}/media/{media_id}` | remove one; 404 if not in album; returns 204 |

### Response DTOs

```go
type albumDTO struct {
    ID        string    `json:"id"`
    Name      string    `json:"name"`
    ItemCount int       `json:"item_count"`
    Cover     *coverDTO `json:"cover,omitempty"`
    CreatedAt time.Time `json:"created_at"`
    UpdatedAt time.Time `json:"updated_at"`
}

type coverDTO struct {
    MediaID      string `json:"media_id"`
    ThumbVersion int    `json:"thumb_version"`
}
```

- `GET /api/v1/albums/{id}`: handler calls `AlbumService.GetDetail` and marshals the returned `AlbumListItem` into `albumDTO`.
- `PATCH /api/v1/albums/{id}`: handler calls `AlbumService.Rename`, which performs the update and returns the refreshed `AlbumListItem`; the handler marshals it into `albumDTO` and returns 200. This keeps the read-your-write contract without a second round-trip.
- `POST /api/v1/albums`: `AlbumService.Create` returns an `AlbumListItem` (ItemCount=0, Cover=nil) so the response shape matches PATCH and GET.
- `cover` is omitted when the album is empty or no member has `thumb_status='ready'`.

Batch-add response:

```json
{
  "added": 7,
  "already_present": 2
}
```

### Query params

- `limit`: default 100, cap 1000 (matches `/media`).
- `offset`: default 0.
- `sort_by`: `added` (default) or `imported`.
- `sort_desc`: `true` (default) or `false`.

Invalid `sort_by` → 400. Invalid `limit` (out of range) → clamped silently, same as `/media`.

### Error mapping

| Service error | HTTP | Body |
|---|---|---|
| `errs.ErrNotFound` | 404 | `{"detail": "album not found"}` or `{"detail": "media not in album"}` |
| `ErrInvalidName` | 400 | `{"detail": "name must be 1..200 chars"}` |
| `ErrInvalidBatch` | 400 | `{"detail": "batch size must be 1..500"}` |
| `ErrInvalidSort` | 400 | `{"detail": "sort_by must be added or imported"}` |
| `errs.ErrOwnerMismatch` | 500 | generic (internal invariant violation, logged; should never reach users if the pre-flight check is correct) |
| anything else | 500 | generic |

The albums surface deliberately has **no user-facing 403s**: cross-owner access returns 404 on every endpoint, so the API cannot be used as an existence oracle for another user's media IDs or album IDs. Identity missing → 401. Caller without stub identity → handled by existing middleware.

## 8. CLI surface

```
fotobank albums create <name>
fotobank albums rename <id> <name>
fotobank albums delete <id>
fotobank albums list                                           → table: UUID | Name | Items | Updated
fotobank albums show <id>                                      → metadata + first page of media IDs
fotobank albums add <album_id> <media_id> [<media_id>...]      → prints "added N, already present M"
fotobank albums remove <album_id> <media_id>
```

Patterns carried forward from `thumbs regenerate` and `reconcile`:

- `--config` flag with `FOTOBANK_CONFIG` fallback.
- `FOTOBANK_DB_PATH` override, falling back to `{cfg.Flash.Root}/fotobank.sqlite`.
- Stub-mode identity guard; any other mode rejected with a clear error.
- Usage errors (empty name, bad args) exit with code 2 via `newUsageError`.
- Each subcommand constructs a caller `owners.Principal` from `cfg.Identity.Stub.{Hub,UserID}` and goes through `AlbumService` — not `album.Repo` directly. The repo is ID-based and does not enforce ownership; a CLI typo (wrong UUID copy-pasted in a terminal) must not let stub user A mutate user B's rows, even in a single-user dev environment where only one owner is expected. Routing through the service keeps the auth check exactly one place.
- No HTTP round-trip — same reason `thumbs regenerate` has none: operator tooling shouldn't depend on the HTTP listener being up. The CLI opens the DB directly and wires up `AlbumService` in-process.
- `add` accepts multiple media IDs as positional args (idempotent batch).
- `show` paginates via `--limit` / `--offset` flags (defaults 100 / 0).

## 9. Error taxonomy additions

```go
// internal/album/album.go
var (
    ErrInvalidName  = errors.New("album: name must be 1..200 chars after trim")
    ErrInvalidBatch = errors.New("album: media_ids batch must be 1..500")
    ErrInvalidSort  = errors.New("album: sort_by must be \"added\" or \"imported\"")
)
```

Reuses existing `errs.ErrNotFound` and `errs.ErrOwnerMismatch` from the cross-cutting error package (`internal/errs/errs.go`).

## 10. Testing strategy

### Repo tests (`internal/album/repo_test.go`)

- `Insert` + `GetByID` happy path.
- `Rename` updates `updated_at`.
- `Delete` cascades `album_media` (two members pre-seed; after delete, `album_media` empty).
- Media delete cascade: deleting a `media` row drops it from every `album_media` (verifies the schema-level FK cascade works).
- `ListByOwner` derives `ItemCount` correctly for 0/1/many members.
- `ListByOwner` cover derivation: empty album → nil, album with only pending thumbs → nil, album with mixed states → picks most recently added `ready` row.
- `AddMedia` idempotency: same batch twice → second call returns (0, N).
- `AddMedia` empty input → (0, 0, nil).
- `AddMedia` over 500 → currently rejected at the service; repo test proves the INSERT scales to the batch size cap.
- `ListMedia` sort modes: `added_desc`, `added_asc`, `imported_desc`, `imported_asc`.
- `ListMedia` pagination: `limit=2&offset=1` returns the middle slice of three.

### Trigger test (`internal/album/trigger_test.go`)

Bypass the service, insert `album_media` via raw SQL where album and media have different owners. Expect the SQLite trigger to raise; repo wraps as `errs.ErrOwnerMismatch`.

### Service tests (`internal/service/album_service_test.go`)

- Cross-owner `Get`, `GetDetail`, `Rename`, `Delete`, `RemoveMedia`, `ListMedia` → all return `errs.ErrNotFound` (not 403).
- `AddMedia` with mixed-owner IDs → `errs.ErrNotFound` BEFORE any DB write (not `ErrOwnerMismatch`, so the endpoint cannot be used as an existence oracle).
- `AddMedia` with non-existent media ID → `errs.ErrNotFound`.
- `AddMedia` with duplicate input IDs (`[X, X, Y]`) dedupes to `[X, Y]`; first call returns `(2, 0)`, second call returns `(0, 2)`.
- `AddMedia` happy path returns counts.
- `Rename` happy path returns the updated `AlbumListItem` with fresh `UpdatedAt` and the cover/count computed at read time.
- `Create` returns an `AlbumListItem` with ItemCount=0 and Cover=nil.
- `GetDetail` derives ItemCount + Cover correctly for a seeded album.
- `List` happy path returns owner's albums only.
- Name validation: empty, whitespace-only, > 200 chars → `ErrInvalidName`.
- Batch validation: 0 ids → `ErrInvalidBatch`; 501 distinct ids → `ErrInvalidBatch`; 502 ids with 2 duplicates (dedupes to 500) → accepted, proving dedupe runs before the length check.

### HTTP tests

- `albums_test.go`: each CRUD route's 200 / 401 / 404 paths. Create returns 201 with an `albumDTO` (ItemCount=0, no cover). Rename returns 200 with the updated `albumDTO` (freshly computed ItemCount + cover). Delete returns 204. Test that `GET /api/v1/albums/{id}` on a cross-owner album returns 404, not 403.
- `album_media_test.go`: list pagination (next_offset boundary cases), sort-by query param, batch-add response shape (`{added, already_present}`), **404 (not 403) on mixed-owner batch**, 404 on unknown-media batch, 400 on oversized batch, 404 on remove-not-in-album. Test that duplicate input IDs in the batch POST collapse to distinct rows (request `[X, X, Y]` with album empty → response `{added: 2, already_present: 0}`).

### CLI tests (`internal/cli/albums_test.go`)

One test per subcommand covering happy path + primary error path. Stub-mode guard test. `add` with multiple IDs covers the batch-count output string.

### E2E extension (`internal/cli/e2e_media_test.go`)

After the existing thumb round-trip section: create an album, `POST /api/v1/albums/{id}/media` with the two photo IDs, `GET /api/v1/albums` and assert the response includes the new album with `cover` pointing to one of the photos, delete the album, list again — album absent.

## 11. Out-of-scope (for this plan)

- Everything in §1 non-goals.
- Search within album contents (full-text or tag).
- Export-to-zip of an album.
- Undo after delete (right now Delete is permanent — Plan D has no soft-delete).
- Album activity feed ("Alice added 3 photos 2h ago"). Phase 4+.

## 12. Open points

None. The huma-vs-raw-mux question is resolved in §7: every albums endpoint is JSON and every one is huma-registered, so OpenAPI stays complete and there is no second error-mapping path to maintain.

## 13. Success criteria

Plan D is done when:

1. All HTTP routes return correct responses under the test matrix above.
2. All CLI subcommands succeed + produce sensible output.
3. `go test -race ./...` green.
4. `golangci-lint run ./...` clean.
5. The E2E test demonstrates album create → add media → list with derived cover → delete round-trip.
6. No existing test regresses.
