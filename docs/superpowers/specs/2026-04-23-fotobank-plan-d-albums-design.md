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

The `albums` and `album_media` tables plus owner-consistency triggers (insert + update) were landed in Plan A's initial migration. Plan D adds Go code, HTTP routes, and a CLI — no new migrations, no schema changes. The trigger already aborts any insert where the album and media have different owners; Plan D's `AlbumService` performs the same check in Go as defence-in-depth so callers get a clean `ErrForbidden` rather than a raw SQLite trigger error.

## 3. Architecture and package layout

New files, all mirroring existing patterns established in Plans B and C:

```
internal/album/
├── album.go            — Album, AlbumListItem, CoverRef, AlbumMediaEntry
└── repo.go             — *album.Repo: CRUD + AddMedia/RemoveMedia/ListMedia/ListByOwner

internal/service/album_service.go
                        — *AlbumService: auth-scoped wrapper, forces caller-owner everywhere

internal/httpapi/albums.go
                        — huma-registered CRUD routes (list/create/get/rename/delete)
internal/httpapi/album_media.go
                        — raw mux routes for the paginated list + batch add + single delete

internal/cli/albums.go
                        — cobra subcommands (create/rename/delete/list/show/add/remove)
```

Layering identical to the media stack:
- `album.Repo` is DB-only. No auth, no identity plumbing.
- `service.AlbumService` is the auth boundary. Every exported method takes `caller owners.Principal` and either scopes queries to that principal or returns `errs.ErrNotFound` for cross-owner access.
- HTTP handlers marshal request/response DTOs and delegate.
- CLI commands use the repo directly (same pattern as `thumbs regenerate`), scoped to `cfg.Identity.Stub.{Hub,UserID}`.

`AlbumService` takes `*album.Repo` and `*media.Repo` (the latter for the pre-flight ownership check on `AddMedia`). No new background goroutines.

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

Computed in one SQL statement using a correlated subquery over `album_media` JOIN `media`, picking the most recently added `media.thumb_status='ready'` row per album. Pseudo-SQL:

```sql
SELECT a.id, a.owner_hub, a.owner_user_id, a.name, a.created_at, a.updated_at,
       COALESCE(ic.cnt, 0) AS item_count,
       cv.media_id, cv.thumb_version
FROM albums a
LEFT JOIN (
  SELECT album_id, COUNT(*) AS cnt FROM album_media GROUP BY album_id
) ic ON ic.album_id = a.id
LEFT JOIN (
  SELECT am.album_id, am.media_id, m.thumb_version,
         ROW_NUMBER() OVER (
           PARTITION BY am.album_id
           ORDER BY am.added_at DESC, am.media_id ASC
         ) AS rn
    FROM album_media am
    JOIN media m ON m.id = am.media_id
   WHERE m.thumb_status = 'ready'
) cv ON cv.album_id = a.id AND cv.rn = 1
WHERE a.owner_hub = ? AND a.owner_user_id = ?
ORDER BY a.updated_at DESC, a.id ASC
LIMIT ? OFFSET ?;
```

Scans null values for empty albums or albums whose only members are still `pending`/`working`. No secondary query per album.

#### `AddMedia` idempotency

Single `INSERT INTO album_media(album_id, media_id, added_at) VALUES (?,?,?), (?,?,?), ...  ON CONFLICT (album_id, media_id) DO NOTHING`. Reports `added = RowsAffected()`; `alreadyPresent = len(mediaIDs) - added`. Empty input is a no-op returning `(0, 0, nil)`.

#### Index use

Existing indexes (`albums_owner_idx`, `media_owner_imported_idx`) cover the list queries. `album_media` has an implicit index from its `PRIMARY KEY (album_id, media_id)` which covers the cover subquery's `album_id` lookup and `ListMedia`'s album-scoped scan. No new indexes.

## 6. Service surface

```go
// internal/service/album_service.go

type AlbumService struct {
    albums *album.Repo
    media  *media.Repo
}

func NewAlbumService(a *album.Repo, m *media.Repo) *AlbumService

// Create returns the new album. Name is trimmed + length-validated.
func (s *AlbumService) Create(ctx context.Context, caller owners.Principal, name string) (album.Album, error)

// Get returns the album; ErrNotFound if missing OR not owned by caller.
func (s *AlbumService) Get(ctx context.Context, id string, caller owners.Principal) (album.Album, error)

// Rename validates the new name and updates the row; ErrNotFound
// if the album does not belong to the caller.
func (s *AlbumService) Rename(ctx context.Context, id, name string, caller owners.Principal) error

// Delete removes the album (album_media cascades). ErrNotFound for
// cross-owner or missing.
func (s *AlbumService) Delete(ctx context.Context, id string, caller owners.Principal) error

// List returns the caller's albums with ItemCount + Cover.
func (s *AlbumService) List(
    ctx context.Context,
    caller owners.Principal,
    limit, offset int,
) ([]album.AlbumListItem, error)

// AddMedia validates the album is caller-owned AND every media_id is
// caller-owned (pre-flight, before the DB trigger). Returns counts.
// errs.ErrOwnerMismatch if any media belongs to a different owner.
// errs.ErrNotFound if any media_id does not exist.
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
- `mediaIDs` batch: 1 ≤ len ≤ 500. Zero or >500 → `ErrInvalidBatch`.
- `SortBy`: `"added"` or `"imported"`. Anything else → `ErrInvalidSort` at the service boundary (the HTTP layer translates to 400).

### Ownership checks

`AddMedia` fetches each `media_id`'s owner via `media.Repo.GetByID` before the insert. Mixed-owner input returns `errs.ErrOwnerMismatch` (HTTP 403) — this is one of two places where 403 leaks that the IDs exist at all, and it's intentional: the caller already proved ownership of *some* rows in the batch, so hiding existence of the rest gains nothing. `errs.ErrNotFound` is used everywhere else to avoid cross-owner existence leaks.

## 7. HTTP API surface

### Huma-registered routes (JSON CRUD)

| Method | Path | Behavior |
|---|---|---|
| `GET` | `/api/v1/albums?limit=&offset=` | list albums w/ cover |
| `POST` | `/api/v1/albums` | create; body `{"name": "..."}` |
| `GET` | `/api/v1/albums/{id}` | detail; 404 if missing or cross-owner |
| `PATCH` | `/api/v1/albums/{id}` | rename; body `{"name": "..."}` |
| `DELETE` | `/api/v1/albums/{id}` | delete (cascades album_media) |

Response DTOs:

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

Detail response omits `cover` only when the album is empty or no member has `thumb_status='ready'` yet.

### Raw-mux routes (paginated list + binary-ish semantics)

| Method | Path | Behavior |
|---|---|---|
| `GET` | `/api/v1/albums/{id}/media?limit=&offset=&sort_by=&sort_desc=` | paginated `mediaDTO` list |
| `POST` | `/api/v1/albums/{id}/media` | batch add, body `{"media_ids": [...]}` |
| `DELETE` | `/api/v1/albums/{id}/media/{media_id}` | remove one; 404 if not in album |

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
| `errs.ErrOwnerMismatch` | 403 | `{"detail": "media belongs to a different owner"}` (mixed-owner batch only) |
| `ErrInvalidName` | 400 | `{"detail": "name must be 1..200 chars"}` |
| `ErrInvalidBatch` | 400 | `{"detail": "batch size must be 1..500"}` |
| `ErrInvalidSort` | 400 | `{"detail": "sort_by must be added or imported"}` |
| anything else | 500 | generic |

Identity missing → 401. Caller without stub identity → handled by existing middleware.

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
- Each subcommand goes through `album.Repo` directly — no HTTP round-trip — for the same reason `thumbs regenerate` does: operator tooling shouldn't depend on the HTTP listener being up.
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

- Cross-owner `Get` → `ErrNotFound` (not 403).
- Cross-owner `Rename` / `Delete` / `ListMedia` → `ErrNotFound`.
- `AddMedia` with mixed-owner IDs → `errs.ErrOwnerMismatch` BEFORE any DB write.
- `AddMedia` with non-existent media ID → `ErrNotFound`.
- `AddMedia` happy path returns counts.
- `List` happy path returns owner's albums only.
- Name validation: empty, whitespace-only, > 200 chars → `ErrInvalidName`.
- Batch validation: 0 or 501 ids → `ErrInvalidBatch`.

### HTTP tests

- `albums_test.go`: each CRUD route's 200 / 401 / 404 paths. Create returns 201 with the new album body. Rename returns 200 with the updated row. Delete returns 204.
- `album_media_test.go`: list pagination (next_offset boundary cases), sort-by query param, batch-add response shape (`{added, already_present}`), 403 on mixed-owner batch, 400 on oversized batch, 404 on remove-not-in-album.

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

None blocking. One item to confirm at implementation time:

- Huma-vs-raw-mux boundary: the current split (JSON CRUD via huma, paginated-list/batch-endpoint via raw mux) mirrors Plan B+C. If huma's streaming/list ergonomics have improved since Plan B, the album_media routes could migrate to huma. Not a Plan D decision.

## 13. Success criteria

Plan D is done when:

1. All HTTP routes return correct responses under the test matrix above.
2. All CLI subcommands succeed + produce sensible output.
3. `go test -race ./...` green.
4. `golangci-lint run ./...` clean.
5. The E2E test demonstrates album create → add media → list with derived cover → delete round-trip.
6. No existing test regresses.
