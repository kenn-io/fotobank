# Plan D: Albums Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship a complete albums subsystem (repo + service + HTTP routes + CLI + E2E) for the single caller identified by the stub identity provider, reusing the existing `albums` / `album_media` tables and triggers.

**Architecture:** Follow the media/thumb layering that's already in the tree: a DB-only `album.Repo` (no auth), an auth-scoped `service.AlbumService` (every exported method takes `caller owners.Principal`), and huma-registered HTTP routes that marshal DTOs and delegate to the service. CLI subcommands also go through the service — not the repo — so ownership is enforced exactly once. A Plan-D-local HTTP error translator overrides `httpapi.Translate` for `errs.ErrOwnerMismatch` so the albums surface never leaks a 403.

**Tech Stack:** Go 1.26, `modernc.org/sqlite`, `database/sql`, `huma/v2`, `cobra`, `testify/require`, `github.com/google/uuid`.

**Spec:** `docs/superpowers/specs/2026-04-23-fotobank-plan-d-albums-design.md`

---

## File structure

```
internal/album/
├── album.go                 — Album, AlbumListItem, CoverRef, AlbumMediaFilter, sentinels
├── repo.go                  — *album.Repo: CRUD + GetDetailByID + ListByOwner + AddMedia/RemoveMedia/ListMedia
├── repo_test.go             — repo unit tests
└── trigger_test.go          — repo-level defence-in-depth trigger test

internal/db/migrations/
├── 000002_album_indexes.up.sql
└── 000002_album_indexes.down.sql

internal/service/
├── album_service.go         — *AlbumService: auth boundary, dedupe, pre-flight ownership
└── album_service_test.go

internal/httpapi/
├── albums.go                — CRUD routes + album_media routes + translateAlbumError
├── albums_test.go           — route tests + translator unit test
└── api.go                   — MODIFIED: add AlbumService to Deps, register the new routes

internal/cli/
├── albums.go                — cobra subcommands: create/rename/delete/list/show/add/remove
├── albums_test.go           — CLI unit tests
├── root.go                  — MODIFIED: root.AddCommand(newAlbumsCmd())
├── server.go                — MODIFIED: construct AlbumService and pass into httpapi.Deps
└── e2e_media_test.go        — MODIFIED: append an album round-trip section at the end
```

Each file has one clear responsibility. `internal/album/repo.go` may grow to ~400 lines; keep album_media operations physically adjacent to the media-side CRUD so the conflict/dedupe logic stays readable.

---

## Sentinel / type reference (used by multiple tasks)

```go
// internal/album/album.go
package album

import (
    "errors"
    "time"

    "github.com/wesm/fotobank/internal/owners"
)

type Album struct {
    ID        string
    Owner     owners.Principal
    Name      string
    CreatedAt time.Time
    UpdatedAt time.Time
}

// CoverRef is the minimum the web app needs to construct
// /api/v1/media/{MediaID}/thumb?size=grid&v={ThumbVersion}.
type CoverRef struct {
    MediaID      string
    ThumbVersion int
}

// AlbumListItem is the shape returned by ListByOwner and GetDetailByID.
// ItemCount and Cover are derived in SQL so clients do not do N+1 calls.
type AlbumListItem struct {
    Album
    ItemCount int
    Cover     *CoverRef // nil when the album is empty OR has no ready thumb yet
}

// AlbumMediaFilter narrows ListMedia.
type AlbumMediaFilter struct {
    Limit  int
    Offset int
    // SortBy is "added" (default) or "imported". The service rejects
    // anything else with ErrInvalidSort.
    SortBy string
    // SortAsc inverts the default DESC sort. A plain bool so the HTTP
    // layer can bind it directly from ?sort_asc=true without pointer
    // plumbing; omitted → DESC, which is what the UI wants.
    SortAsc bool
}

const (
    // NameMaxLen caps trimmed album names. Service validation enforces this.
    NameMaxLen = 200
    // BatchMaxLen caps deduped AddMedia batches. Service validation enforces this.
    BatchMaxLen = 500
)

var (
    ErrInvalidName  = errors.New("album: name must be 1..200 chars after trim")
    ErrInvalidBatch = errors.New("album: media_ids batch must be 1..500")
    ErrInvalidSort  = errors.New("album: sort_by must be \"added\" or \"imported\"")
)
```

---

### Task 1: Index migration + album package skeleton

**Files:**
- Create: `internal/db/migrations/000002_album_indexes.up.sql`
- Create: `internal/db/migrations/000002_album_indexes.down.sql`
- Create: `internal/album/album.go`

- [ ] **Step 1: Write the up migration**

Create `internal/db/migrations/000002_album_indexes.up.sql`:

```sql
-- Covers ListByOwner's ORDER BY updated_at DESC, id after owner filter.
CREATE INDEX albums_owner_updated_idx
    ON albums(owner_hub, owner_user_id, updated_at DESC, id);

-- Covers the cover subquery and ListMedia sort_by=added
-- (most-recent-added-first per album).
CREATE INDEX album_media_album_added_idx
    ON album_media(album_id, added_at DESC);
```

- [ ] **Step 2: Write the down migration**

Create `internal/db/migrations/000002_album_indexes.down.sql`:

```sql
DROP INDEX IF EXISTS album_media_album_added_idx;
DROP INDEX IF EXISTS albums_owner_updated_idx;
```

- [ ] **Step 3: Write the album package types**

Create `internal/album/album.go` with the exact contents of the "Sentinel / type reference" block above.

- [ ] **Step 4: Verify everything compiles**

Run: `go build ./...`
Expected: no output, exit 0.

- [ ] **Step 5: Verify migrations apply on a fresh DB**

Run: `go test ./internal/db/... -run TestMigrations -count=1`
Expected: PASS. If a `TestMigrations` test is not present, run `go test ./internal/db/...` instead. The migration runner in `internal/migrate` discovers files by filename — 000002 is picked up automatically.

- [ ] **Step 6: Commit**

```bash
git add internal/db/migrations/000002_album_indexes.up.sql \
        internal/db/migrations/000002_album_indexes.down.sql \
        internal/album/album.go
git commit -m "Add album package skeleton and index migration"
```

---

### Task 2: album.Repo CRUD (Insert/GetByID/Rename/Delete)

**Files:**
- Create: `internal/album/repo.go`
- Create: `internal/album/repo_test.go`

- [ ] **Step 1: Write the repo scaffolding and CRUD methods**

Create `internal/album/repo.go`:

```go
package album

import (
    "context"
    "database/sql"
    "errors"
    "fmt"
    "time"

    "github.com/wesm/fotobank/internal/errs"
    "github.com/wesm/fotobank/internal/owners"
)

// Repo is a SQLite-backed store of album + album_media rows. It uses a
// split read/write pool: writes go through rw, reads through ro.
type Repo struct {
    rw *sql.DB
    ro *sql.DB
}

// NewRepo constructs a Repo. rw must be the writer pool, ro the reader.
func NewRepo(rw, ro *sql.DB) *Repo { return &Repo{rw: rw, ro: ro} }

const albumSelect = `SELECT id, owner_hub, owner_user_id, name, created_at, updated_at FROM albums`

// Insert stores a new album row.
func (r *Repo) Insert(ctx context.Context, a Album) error {
    _, err := r.rw.ExecContext(ctx,
        `INSERT INTO albums (id, owner_hub, owner_user_id, name, created_at, updated_at)
         VALUES (?, ?, ?, ?, ?, ?)`,
        a.ID, a.Owner.Hub, a.Owner.UserID, a.Name, a.CreatedAt, a.UpdatedAt,
    )
    if err != nil {
        return fmt.Errorf("insert album: %w", err)
    }
    return nil
}

// GetByID returns the bare album row. ErrNotFound if missing.
func (r *Repo) GetByID(ctx context.Context, id string) (Album, error) {
    row := r.ro.QueryRowContext(ctx, albumSelect+" WHERE id = ?", id)
    a, err := scanAlbum(row)
    if errors.Is(err, sql.ErrNoRows) {
        return Album{}, fmt.Errorf("%w: album id=%s", errs.ErrNotFound, id)
    }
    if err != nil {
        return Album{}, fmt.Errorf("get album: %w", err)
    }
    return a, nil
}

// Rename updates name and updated_at. ErrNotFound if missing.
func (r *Repo) Rename(ctx context.Context, id, name string, now time.Time) error {
    res, err := r.rw.ExecContext(ctx,
        `UPDATE albums SET name = ?, updated_at = ? WHERE id = ?`,
        name, now, id,
    )
    if err != nil {
        return fmt.Errorf("rename album: %w", err)
    }
    n, err := res.RowsAffected()
    if err != nil {
        return fmt.Errorf("rename rows affected: %w", err)
    }
    if n == 0 {
        return fmt.Errorf("%w: album id=%s", errs.ErrNotFound, id)
    }
    return nil
}

// Delete removes the album. album_media is cascaded by the FK ON DELETE
// CASCADE in the schema. ErrNotFound if missing.
func (r *Repo) Delete(ctx context.Context, id string) error {
    res, err := r.rw.ExecContext(ctx, `DELETE FROM albums WHERE id = ?`, id)
    if err != nil {
        return fmt.Errorf("delete album: %w", err)
    }
    n, err := res.RowsAffected()
    if err != nil {
        return fmt.Errorf("delete rows affected: %w", err)
    }
    if n == 0 {
        return fmt.Errorf("%w: album id=%s", errs.ErrNotFound, id)
    }
    return nil
}

type rowScanner interface {
    Scan(dest ...any) error
}

func scanAlbum(s rowScanner) (Album, error) {
    var a Album
    if err := s.Scan(
        &a.ID, &a.Owner.Hub, &a.Owner.UserID, &a.Name, &a.CreatedAt, &a.UpdatedAt,
    ); err != nil {
        return Album{}, err
    }
    return a, nil
}
```

- [ ] **Step 2: Write the seed helpers and first test**

Create `internal/album/repo_test.go`:

```go
package album_test

import (
    "context"
    "database/sql"
    "testing"
    "time"

    "github.com/google/uuid"
    "github.com/stretchr/testify/require"

    "github.com/wesm/fotobank/internal/album"
    "github.com/wesm/fotobank/internal/errs"
    "github.com/wesm/fotobank/internal/owners"
    "github.com/wesm/fotobank/internal/testutil"
)

// seedOwner inserts a minimal owners row so album FK constraints resolve.
func seedOwner(t *testing.T, rw *sql.DB, p owners.Principal, sk string) {
    t.Helper()
    _, err := rw.ExecContext(context.Background(),
        `INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
        p.Hub, p.UserID, sk, time.Now().UTC(),
    )
    require.NoError(t, err)
}

// seedAlbum inserts an album via the repo.
func seedAlbum(t *testing.T, r *album.Repo, p owners.Principal, name string) album.Album {
    t.Helper()
    now := time.Now().UTC().Truncate(time.Second)
    a := album.Album{
        ID:        uuid.NewString(),
        Owner:     p,
        Name:      name,
        CreatedAt: now,
        UpdatedAt: now,
    }
    require.NoError(t, r.Insert(context.Background(), a))
    return a
}

func TestRepoInsertAndGetByID(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    repo := album.NewRepo(d.WriteDB(), d.ReadDB())
    p := owners.Principal{Hub: "h", UserID: "u"}
    seedOwner(t, d.WriteDB(), p, "sk")

    a := seedAlbum(t, repo, p, "Trip")
    got, err := repo.GetByID(context.Background(), a.ID)
    r.NoError(err)
    r.Equal(a.ID, got.ID)
    r.Equal("Trip", got.Name)
    r.Equal(p, got.Owner)
}

func TestRepoGetByIDMissing(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    repo := album.NewRepo(d.WriteDB(), d.ReadDB())
    _, err := repo.GetByID(context.Background(), "nope")
    r.ErrorIs(err, errs.ErrNotFound)
}
```

- [ ] **Step 3: Run the two tests**

Run: `go test ./internal/album/... -run "TestRepoInsertAndGetByID|TestRepoGetByIDMissing" -v`
Expected: both PASS.

- [ ] **Step 4: Add Rename test**

Append to `internal/album/repo_test.go`:

```go
func TestRepoRenameUpdatesName(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    repo := album.NewRepo(d.WriteDB(), d.ReadDB())
    p := owners.Principal{Hub: "h", UserID: "u"}
    seedOwner(t, d.WriteDB(), p, "sk")
    a := seedAlbum(t, repo, p, "Old")

    // Move the clock forward so updated_at can change.
    later := a.UpdatedAt.Add(2 * time.Second)
    r.NoError(repo.Rename(context.Background(), a.ID, "New", later))

    got, err := repo.GetByID(context.Background(), a.ID)
    r.NoError(err)
    r.Equal("New", got.Name)
    r.True(got.UpdatedAt.Equal(later), "updated_at should advance: got %v want %v", got.UpdatedAt, later)
    r.True(got.CreatedAt.Equal(a.CreatedAt), "created_at must not change")
}

func TestRepoRenameMissing(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    repo := album.NewRepo(d.WriteDB(), d.ReadDB())
    err := repo.Rename(context.Background(), "nope", "x", time.Now().UTC())
    r.ErrorIs(err, errs.ErrNotFound)
}
```

- [ ] **Step 5: Add Delete test**

Append:

```go
func TestRepoDelete(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    repo := album.NewRepo(d.WriteDB(), d.ReadDB())
    p := owners.Principal{Hub: "h", UserID: "u"}
    seedOwner(t, d.WriteDB(), p, "sk")
    a := seedAlbum(t, repo, p, "Trip")

    r.NoError(repo.Delete(context.Background(), a.ID))

    _, err := repo.GetByID(context.Background(), a.ID)
    r.ErrorIs(err, errs.ErrNotFound)
}

func TestRepoDeleteMissing(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    repo := album.NewRepo(d.WriteDB(), d.ReadDB())
    err := repo.Delete(context.Background(), "nope")
    r.ErrorIs(err, errs.ErrNotFound)
}
```

- [ ] **Step 6: Run the full album test package**

Run: `go test ./internal/album/... -v`
Expected: all PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/album/repo.go internal/album/repo_test.go
git commit -m "Add album.Repo CRUD (Insert/GetByID/Rename/Delete)"
```

---

### Task 3: album.Repo.GetDetailByID + ListByOwner

**Files:**
- Modify: `internal/album/repo.go`
- Modify: `internal/album/repo_test.go`

- [ ] **Step 1: Append GetDetailByID to repo.go**

```go
// GetDetailByID returns one album with derived ItemCount + Cover using
// the same count/cover subquery shape as ListByOwner. ErrNotFound on miss.
func (r *Repo) GetDetailByID(ctx context.Context, id string) (AlbumListItem, error) {
    const q = `
SELECT a.id, a.owner_hub, a.owner_user_id, a.name, a.created_at, a.updated_at,
       COALESCE(cnt.n, 0) AS item_count,
       cv.media_id, cv.thumb_version
  FROM albums a
  LEFT JOIN (
    SELECT album_id, COUNT(*) AS n
      FROM album_media
     WHERE album_id = ?
     GROUP BY album_id
  ) cnt ON cnt.album_id = a.id
  LEFT JOIN (
    SELECT am.album_id, am.media_id, m.thumb_version,
           ROW_NUMBER() OVER (
             PARTITION BY am.album_id
             ORDER BY am.added_at DESC, am.media_id ASC
           ) AS rn
      FROM album_media am
      JOIN media m ON m.id = am.media_id
     WHERE am.album_id = ?
       AND m.thumb_status = 'ready'
  ) cv ON cv.album_id = a.id AND cv.rn = 1
 WHERE a.id = ?;
`
    row := r.ro.QueryRowContext(ctx, q, id, id, id)
    item, err := scanAlbumListItem(row)
    if errors.Is(err, sql.ErrNoRows) {
        return AlbumListItem{}, fmt.Errorf("%w: album id=%s", errs.ErrNotFound, id)
    }
    if err != nil {
        return AlbumListItem{}, fmt.Errorf("get album detail: %w", err)
    }
    return item, nil
}

func scanAlbumListItem(s rowScanner) (AlbumListItem, error) {
    var (
        item        AlbumListItem
        coverMedia  sql.NullString
        coverThumbV sql.NullInt64
    )
    if err := s.Scan(
        &item.ID, &item.Owner.Hub, &item.Owner.UserID, &item.Name,
        &item.CreatedAt, &item.UpdatedAt,
        &item.ItemCount,
        &coverMedia, &coverThumbV,
    ); err != nil {
        return AlbumListItem{}, err
    }
    if coverMedia.Valid {
        item.Cover = &CoverRef{
            MediaID:      coverMedia.String,
            ThumbVersion: int(coverThumbV.Int64),
        }
    }
    return item, nil
}
```

- [ ] **Step 2: Add GetDetailByID test with a seeded album_media row**

Append to `repo_test.go`:

```go
// seedMediaRow inserts a minimal media row. We do this with raw SQL
// (not through media.Repo) to keep album tests independent of the media
// package's insert surface, which requires many more fields.
func seedMediaRow(t *testing.T, rw *sql.DB, p owners.Principal, id, checksum, thumbStatus string, thumbVersion int) {
    t.Helper()
    _, err := rw.ExecContext(context.Background(), `
INSERT INTO media (
    id, owner_hub, owner_user_id, media_type, mime_type, path, original_filename,
    imported_at, timestamp, size, checksum,
    make, model, focal_length, shutter, width, height, iso, aperture,
    duration_ms,
    thumb_status, thumb_version, thumb_updated_at
) VALUES (?, ?, ?, 'photo', 'image/jpeg', ?, NULL, ?, NULL, 0, ?,
          NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL,
          ?, ?, NULL)`,
        id, p.Hub, p.UserID, "p/"+id, time.Now().UTC(), checksum, thumbStatus, thumbVersion,
    )
    require.NoError(t, err)
}

// seedAlbumMedia inserts directly into album_media (bypassing the service)
// using the given added_at.
func seedAlbumMedia(t *testing.T, rw *sql.DB, albumID, mediaID string, addedAt time.Time) {
    t.Helper()
    _, err := rw.ExecContext(context.Background(),
        `INSERT INTO album_media(album_id, media_id, added_at) VALUES(?,?,?)`,
        albumID, mediaID, addedAt,
    )
    require.NoError(t, err)
}

func TestRepoGetDetailByIDEmpty(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    repo := album.NewRepo(d.WriteDB(), d.ReadDB())
    p := owners.Principal{Hub: "h", UserID: "u"}
    seedOwner(t, d.WriteDB(), p, "sk")
    a := seedAlbum(t, repo, p, "Empty")

    got, err := repo.GetDetailByID(context.Background(), a.ID)
    r.NoError(err)
    r.Equal(0, got.ItemCount)
    r.Nil(got.Cover)
    r.Equal("Empty", got.Name)
}

func TestRepoGetDetailByIDWithReadyCover(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    repo := album.NewRepo(d.WriteDB(), d.ReadDB())
    p := owners.Principal{Hub: "h", UserID: "u"}
    seedOwner(t, d.WriteDB(), p, "sk")
    a := seedAlbum(t, repo, p, "Trip")

    // Older pending member, newer ready member — cover should be the newer one.
    older := uuid.NewString()
    newer := uuid.NewString()
    seedMediaRow(t, d.WriteDB(), p, older, "cs-o", "pending", 0)
    seedMediaRow(t, d.WriteDB(), p, newer, "cs-n", "ready", 3)
    base := time.Now().UTC().Truncate(time.Second)
    seedAlbumMedia(t, d.WriteDB(), a.ID, older, base)
    seedAlbumMedia(t, d.WriteDB(), a.ID, newer, base.Add(time.Second))

    got, err := repo.GetDetailByID(context.Background(), a.ID)
    r.NoError(err)
    r.Equal(2, got.ItemCount)
    r.NotNil(got.Cover)
    r.Equal(newer, got.Cover.MediaID)
    r.Equal(3, got.Cover.ThumbVersion)
}

func TestRepoGetDetailByIDPendingOnlyNilCover(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    repo := album.NewRepo(d.WriteDB(), d.ReadDB())
    p := owners.Principal{Hub: "h", UserID: "u"}
    seedOwner(t, d.WriteDB(), p, "sk")
    a := seedAlbum(t, repo, p, "Pending")

    m := uuid.NewString()
    seedMediaRow(t, d.WriteDB(), p, m, "cs", "pending", 0)
    seedAlbumMedia(t, d.WriteDB(), a.ID, m, time.Now().UTC())

    got, err := repo.GetDetailByID(context.Background(), a.ID)
    r.NoError(err)
    r.Equal(1, got.ItemCount)
    r.Nil(got.Cover, "all members pending → cover must be nil")
}

func TestRepoGetDetailByIDMissing(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    repo := album.NewRepo(d.WriteDB(), d.ReadDB())
    _, err := repo.GetDetailByID(context.Background(), "nope")
    r.ErrorIs(err, errs.ErrNotFound)
}
```

- [ ] **Step 3: Run the new tests to verify they pass**

Run: `go test ./internal/album/... -run TestRepoGetDetail -v`
Expected: all four PASS.

- [ ] **Step 4: Append ListByOwner to repo.go**

```go
// ListByOwner returns albums belonging to owner, paginated by limit /
// offset and ordered by updated_at DESC, id ASC. ItemCount and Cover
// are derived in the same statement.
func (r *Repo) ListByOwner(
    ctx context.Context,
    owner owners.Principal,
    limit, offset int,
) ([]AlbumListItem, error) {
    if limit <= 0 {
        limit = 100
    }
    if offset < 0 {
        offset = 0
    }
    const q = `
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
  ) cv ON cv.album_id = oa.id AND cv.rn = 1
 ORDER BY oa.updated_at DESC, oa.id ASC;
`
    rows, err := r.ro.QueryContext(ctx, q, owner.Hub, owner.UserID, limit, offset)
    if err != nil {
        return nil, fmt.Errorf("list albums: %w", err)
    }
    defer func() { _ = rows.Close() }()

    var out []AlbumListItem
    for rows.Next() {
        item, err := scanAlbumListItem(rows)
        if err != nil {
            return nil, fmt.Errorf("scan album list item: %w", err)
        }
        out = append(out, item)
    }
    if err := rows.Err(); err != nil {
        return nil, fmt.Errorf("iterate albums: %w", err)
    }
    return out, nil
}
```

- [ ] **Step 5: Add ListByOwner tests**

Append to `repo_test.go`:

```go
func TestRepoListByOwnerSortsByUpdatedDesc(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    repo := album.NewRepo(d.WriteDB(), d.ReadDB())
    p := owners.Principal{Hub: "h", UserID: "u"}
    seedOwner(t, d.WriteDB(), p, "sk")

    oldA := seedAlbum(t, repo, p, "Old")
    // Advance the updated_at on the second album explicitly so the
    // test doesn't depend on same-second timestamps colliding.
    newA := seedAlbum(t, repo, p, "New")
    later := newA.UpdatedAt.Add(10 * time.Second)
    r.NoError(repo.Rename(context.Background(), newA.ID, "New", later))

    items, err := repo.ListByOwner(context.Background(), p, 10, 0)
    r.NoError(err)
    r.Len(items, 2)
    r.Equal(newA.ID, items[0].ID, "most recently updated album first")
    r.Equal(oldA.ID, items[1].ID)
}

func TestRepoListByOwnerIsolatesOwners(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    repo := album.NewRepo(d.WriteDB(), d.ReadDB())
    pA := owners.Principal{Hub: "h", UserID: "a"}
    pB := owners.Principal{Hub: "h", UserID: "b"}
    seedOwner(t, d.WriteDB(), pA, "sk-a")
    seedOwner(t, d.WriteDB(), pB, "sk-b")

    seedAlbum(t, repo, pA, "A-1")
    seedAlbum(t, repo, pA, "A-2")
    seedAlbum(t, repo, pB, "B-1")

    itemsA, err := repo.ListByOwner(context.Background(), pA, 10, 0)
    r.NoError(err)
    r.Len(itemsA, 2)
    for _, it := range itemsA {
        r.Equal(pA, it.Owner)
    }
}

func TestRepoListByOwnerPagination(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    repo := album.NewRepo(d.WriteDB(), d.ReadDB())
    p := owners.Principal{Hub: "h", UserID: "u"}
    seedOwner(t, d.WriteDB(), p, "sk")

    for i := 0; i < 5; i++ {
        a := seedAlbum(t, repo, p, "x")
        r.NoError(repo.Rename(context.Background(), a.ID, "x", a.UpdatedAt.Add(time.Duration(i)*time.Second)))
    }

    page1, err := repo.ListByOwner(context.Background(), p, 2, 0)
    r.NoError(err)
    r.Len(page1, 2)

    page2, err := repo.ListByOwner(context.Background(), p, 2, 2)
    r.NoError(err)
    r.Len(page2, 2)

    page3, err := repo.ListByOwner(context.Background(), p, 2, 4)
    r.NoError(err)
    r.Len(page3, 1)

    // No duplicates across pages.
    seen := map[string]struct{}{}
    for _, it := range page1 {
        seen[it.ID] = struct{}{}
    }
    for _, it := range page2 {
        _, dup := seen[it.ID]
        r.False(dup)
        seen[it.ID] = struct{}{}
    }
}

func TestRepoListByOwnerDerivesCoverAndCount(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    repo := album.NewRepo(d.WriteDB(), d.ReadDB())
    p := owners.Principal{Hub: "h", UserID: "u"}
    seedOwner(t, d.WriteDB(), p, "sk")
    a := seedAlbum(t, repo, p, "Trip")

    ready := uuid.NewString()
    seedMediaRow(t, d.WriteDB(), p, ready, "cs-r", "ready", 1)
    seedAlbumMedia(t, d.WriteDB(), a.ID, ready, time.Now().UTC())

    items, err := repo.ListByOwner(context.Background(), p, 10, 0)
    r.NoError(err)
    r.Len(items, 1)
    r.Equal(1, items[0].ItemCount)
    r.NotNil(items[0].Cover)
    r.Equal(ready, items[0].Cover.MediaID)
}
```

- [ ] **Step 6: Run the full album test package**

Run: `go test ./internal/album/... -v`
Expected: all PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/album/repo.go internal/album/repo_test.go
git commit -m "Add album.Repo GetDetailByID and ListByOwner"
```

---

### Task 4: album.Repo album_media operations (AddMedia/RemoveMedia/ListMedia)

**Files:**
- Modify: `internal/album/repo.go`
- Modify: `internal/album/repo_test.go`

- [ ] **Step 1: Append media-side methods to repo.go**

```go
// AddMedia is mechanical and tolerant of empty input: the service layer
// enforces the 1..500 bound. Empty input → (0, 0, nil) without touching
// the DB. Uses a batched INSERT ... VALUES (?,?,?),... ON CONFLICT
// DO NOTHING; reports added = RowsAffected, alreadyPresent = len - added.
// Does NOT bump albums.updated_at; that is a non-goal for Plan D.
func (r *Repo) AddMedia(
    ctx context.Context,
    albumID string,
    mediaIDs []string,
    now time.Time,
) (added, alreadyPresent int, err error) {
    if len(mediaIDs) == 0 {
        return 0, 0, nil
    }
    // Build "(?,?,?),(?,?,?),..." with 3 args per row.
    values := make([]string, 0, len(mediaIDs))
    args := make([]any, 0, len(mediaIDs)*3)
    for _, mid := range mediaIDs {
        values = append(values, "(?,?,?)")
        args = append(args, albumID, mid, now)
    }
    q := `INSERT INTO album_media (album_id, media_id, added_at) VALUES ` +
        join(values, ",") +
        ` ON CONFLICT (album_id, media_id) DO NOTHING`
    res, err := r.rw.ExecContext(ctx, q, args...)
    if err != nil {
        return 0, 0, fmt.Errorf("add album media: %w", err)
    }
    n, err := res.RowsAffected()
    if err != nil {
        return 0, 0, fmt.Errorf("add album media rows affected: %w", err)
    }
    added = int(n)
    alreadyPresent = len(mediaIDs) - added
    return added, alreadyPresent, nil
}

// join is a tiny local helper to avoid importing strings for a single call.
func join(parts []string, sep string) string {
    if len(parts) == 0 {
        return ""
    }
    out := parts[0]
    for _, s := range parts[1:] {
        out += sep + s
    }
    return out
}

// RemoveMedia removes one media_id from an album. ErrNotFound if the
// album does not exist or the pair (album_id, media_id) is absent —
// SQLite cannot distinguish the two cases at this layer, which is fine:
// the service's caller-owner check already gates cross-owner album IDs.
func (r *Repo) RemoveMedia(ctx context.Context, albumID, mediaID string) error {
    res, err := r.rw.ExecContext(ctx,
        `DELETE FROM album_media WHERE album_id = ? AND media_id = ?`,
        albumID, mediaID,
    )
    if err != nil {
        return fmt.Errorf("remove album media: %w", err)
    }
    n, err := res.RowsAffected()
    if err != nil {
        return fmt.Errorf("remove rows affected: %w", err)
    }
    if n == 0 {
        return fmt.Errorf("%w: album_media (album=%s, media=%s)",
            errs.ErrNotFound, albumID, mediaID)
    }
    return nil
}
```

- [ ] **Step 2: Add AddMedia tests**

Append to `repo_test.go`:

```go
func TestRepoAddMediaHappyPath(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    repo := album.NewRepo(d.WriteDB(), d.ReadDB())
    p := owners.Principal{Hub: "h", UserID: "u"}
    seedOwner(t, d.WriteDB(), p, "sk")
    a := seedAlbum(t, repo, p, "Trip")
    m1 := uuid.NewString()
    m2 := uuid.NewString()
    seedMediaRow(t, d.WriteDB(), p, m1, "cs1", "ready", 1)
    seedMediaRow(t, d.WriteDB(), p, m2, "cs2", "ready", 1)

    added, already, err := repo.AddMedia(context.Background(), a.ID, []string{m1, m2}, time.Now().UTC())
    r.NoError(err)
    r.Equal(2, added)
    r.Equal(0, already)
}

func TestRepoAddMediaIsIdempotent(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    repo := album.NewRepo(d.WriteDB(), d.ReadDB())
    p := owners.Principal{Hub: "h", UserID: "u"}
    seedOwner(t, d.WriteDB(), p, "sk")
    a := seedAlbum(t, repo, p, "Trip")
    m := uuid.NewString()
    seedMediaRow(t, d.WriteDB(), p, m, "cs", "ready", 1)

    added1, already1, err := repo.AddMedia(context.Background(), a.ID, []string{m}, time.Now().UTC())
    r.NoError(err)
    r.Equal(1, added1)
    r.Equal(0, already1)

    added2, already2, err := repo.AddMedia(context.Background(), a.ID, []string{m}, time.Now().UTC())
    r.NoError(err)
    r.Equal(0, added2)
    r.Equal(1, already2)
}

func TestRepoAddMediaEmptyInputNoOp(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    repo := album.NewRepo(d.WriteDB(), d.ReadDB())
    p := owners.Principal{Hub: "h", UserID: "u"}
    seedOwner(t, d.WriteDB(), p, "sk")
    a := seedAlbum(t, repo, p, "Empty")

    added, already, err := repo.AddMedia(context.Background(), a.ID, nil, time.Now().UTC())
    r.NoError(err)
    r.Equal(0, added)
    r.Equal(0, already)
}

func TestRepoAddMediaScalesToBatchCap(t *testing.T) {
    // Proves the INSERT handles the 500-row batch size that the service layer caps at.
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    repo := album.NewRepo(d.WriteDB(), d.ReadDB())
    p := owners.Principal{Hub: "h", UserID: "u"}
    seedOwner(t, d.WriteDB(), p, "sk")
    a := seedAlbum(t, repo, p, "Big")

    ids := make([]string, 500)
    for i := range ids {
        id := uuid.NewString()
        ids[i] = id
        seedMediaRow(t, d.WriteDB(), p, id, "cs"+id, "ready", 1)
    }
    added, already, err := repo.AddMedia(context.Background(), a.ID, ids, time.Now().UTC())
    r.NoError(err)
    r.Equal(500, added)
    r.Equal(0, already)
}
```

- [ ] **Step 3: Add RemoveMedia tests**

Append:

```go
func TestRepoRemoveMediaHappyPath(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    repo := album.NewRepo(d.WriteDB(), d.ReadDB())
    p := owners.Principal{Hub: "h", UserID: "u"}
    seedOwner(t, d.WriteDB(), p, "sk")
    a := seedAlbum(t, repo, p, "Trip")
    m := uuid.NewString()
    seedMediaRow(t, d.WriteDB(), p, m, "cs", "ready", 1)
    seedAlbumMedia(t, d.WriteDB(), a.ID, m, time.Now().UTC())

    r.NoError(repo.RemoveMedia(context.Background(), a.ID, m))
}

func TestRepoRemoveMediaNotInAlbum(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    repo := album.NewRepo(d.WriteDB(), d.ReadDB())
    p := owners.Principal{Hub: "h", UserID: "u"}
    seedOwner(t, d.WriteDB(), p, "sk")
    a := seedAlbum(t, repo, p, "Trip")

    err := repo.RemoveMedia(context.Background(), a.ID, "nonesuch")
    r.ErrorIs(err, errs.ErrNotFound)
}

func TestRepoDeleteAlbumCascadesAlbumMedia(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    repo := album.NewRepo(d.WriteDB(), d.ReadDB())
    p := owners.Principal{Hub: "h", UserID: "u"}
    seedOwner(t, d.WriteDB(), p, "sk")
    a := seedAlbum(t, repo, p, "Trip")
    m1 := uuid.NewString()
    m2 := uuid.NewString()
    seedMediaRow(t, d.WriteDB(), p, m1, "cs1", "ready", 1)
    seedMediaRow(t, d.WriteDB(), p, m2, "cs2", "ready", 1)
    seedAlbumMedia(t, d.WriteDB(), a.ID, m1, time.Now().UTC())
    seedAlbumMedia(t, d.WriteDB(), a.ID, m2, time.Now().UTC())

    r.NoError(repo.Delete(context.Background(), a.ID))

    var n int
    r.NoError(d.ReadDB().QueryRowContext(context.Background(),
        `SELECT COUNT(*) FROM album_media WHERE album_id = ?`, a.ID).Scan(&n))
    r.Equal(0, n)
}

func TestRepoDeleteMediaCascadesAlbumMedia(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    repo := album.NewRepo(d.WriteDB(), d.ReadDB())
    p := owners.Principal{Hub: "h", UserID: "u"}
    seedOwner(t, d.WriteDB(), p, "sk")
    a := seedAlbum(t, repo, p, "Trip")
    m := uuid.NewString()
    seedMediaRow(t, d.WriteDB(), p, m, "cs", "ready", 1)
    seedAlbumMedia(t, d.WriteDB(), a.ID, m, time.Now().UTC())

    _, err := d.WriteDB().ExecContext(context.Background(), `DELETE FROM media WHERE id = ?`, m)
    r.NoError(err)

    var n int
    r.NoError(d.ReadDB().QueryRowContext(context.Background(),
        `SELECT COUNT(*) FROM album_media WHERE album_id = ?`, a.ID).Scan(&n))
    r.Equal(0, n, "deleting media should cascade to album_media via FK")
}
```

- [ ] **Step 4: Append ListMedia to repo.go**

```go
const albumMediaMediaSelect = `SELECT
    m.id, m.owner_hub, m.owner_user_id, m.media_type, m.mime_type, m.path, m.original_filename,
    m.imported_at, m.timestamp, m.size, m.checksum,
    m.make, m.model, m.focal_length, m.shutter, m.width, m.height, m.iso, m.aperture,
    m.duration_ms,
    m.thumb_status, m.thumb_version, m.thumb_updated_at
FROM album_media am JOIN media m ON m.id = am.media_id`

// ListMedia returns paginated media rows that belong to albumID. The
// SortBy / SortAsc fields must be validated by the caller (service);
// the repo trusts SortBy ∈ {"added","imported"}.
func (r *Repo) ListMedia(
    ctx context.Context,
    albumID string,
    filter AlbumMediaFilter,
) ([]media.Media, error) {
    limit := filter.Limit
    if limit <= 0 {
        limit = 100
    }
    offset := filter.Offset
    if offset < 0 {
        offset = 0
    }
    direction := "DESC"
    if filter.SortAsc {
        direction = "ASC"
    }

    var orderBy string
    switch filter.SortBy {
    case "", "added":
        orderBy = "am.added_at " + direction + ", am.media_id " + direction
    case "imported":
        orderBy = "m.imported_at " + direction + " NULLS LAST, m.id " + direction
    default:
        // The service validates SortBy; hitting this means a caller bypassed it.
        return nil, fmt.Errorf("album.ListMedia: invalid SortBy %q", filter.SortBy)
    }

    q := albumMediaMediaSelect +
        " WHERE am.album_id = ?" +
        " ORDER BY " + orderBy +
        " LIMIT ? OFFSET ?"
    rows, err := r.ro.QueryContext(ctx, q, albumID, limit, offset)
    if err != nil {
        return nil, fmt.Errorf("list album media: %w", err)
    }
    defer func() { _ = rows.Close() }()

    var out []media.Media
    for rows.Next() {
        m, err := media.ScanMediaForAlbum(rows)
        if err != nil {
            return nil, fmt.Errorf("scan album media: %w", err)
        }
        out = append(out, m)
    }
    if err := rows.Err(); err != nil {
        return nil, fmt.Errorf("iterate album media: %w", err)
    }
    return out, nil
}
```

Add the import `"github.com/wesm/fotobank/internal/media"` to `repo.go`.

- [ ] **Step 5: Export the media scanner**

The `scanMedia` helper in `internal/media/repo.go` is currently unexported but the album package needs it. Append a thin re-export:

Edit `internal/media/repo.go` — locate the `scanMedia` function (around line 237) and immediately after it, add:

```go
// ScanMediaForAlbum is scanMedia re-exported for internal/album.
// internal packages are in the same module so a cross-package helper
// is fine; kept narrowly named so callers don't repurpose it.
func ScanMediaForAlbum(s interface {
    Scan(dest ...any) error
}) (Media, error) {
    return scanMedia(s)
}
```

- [ ] **Step 6: Add ListMedia tests**

Append to `repo_test.go`:

```go
func TestRepoListMediaSortModes(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    repo := album.NewRepo(d.WriteDB(), d.ReadDB())
    p := owners.Principal{Hub: "h", UserID: "u"}
    seedOwner(t, d.WriteDB(), p, "sk")
    a := seedAlbum(t, repo, p, "Trip")

    first := uuid.NewString()
    second := uuid.NewString()
    third := uuid.NewString()
    seedMediaRow(t, d.WriteDB(), p, first, "cs1", "ready", 1)
    seedMediaRow(t, d.WriteDB(), p, second, "cs2", "ready", 1)
    seedMediaRow(t, d.WriteDB(), p, third, "cs3", "ready", 1)

    base := time.Now().UTC().Truncate(time.Second)
    seedAlbumMedia(t, d.WriteDB(), a.ID, first, base)                  // added earliest
    seedAlbumMedia(t, d.WriteDB(), a.ID, second, base.Add(time.Second))
    seedAlbumMedia(t, d.WriteDB(), a.ID, third, base.Add(2*time.Second)) // added latest

    cases := []struct {
        name    string
        filter  album.AlbumMediaFilter
        wantIDs []string
    }{
        {"added-desc (default)", album.AlbumMediaFilter{SortBy: "added"}, []string{third, second, first}},
        {"added-asc", album.AlbumMediaFilter{SortBy: "added", SortAsc: true}, []string{first, second, third}},
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            got, err := repo.ListMedia(context.Background(), a.ID, tc.filter)
            require.NoError(t, err)
            ids := make([]string, 0, len(got))
            for _, m := range got {
                ids = append(ids, m.ID)
            }
            require.Equal(t, tc.wantIDs, ids)
        })
    }
    _ = r
}

func TestRepoListMediaPagination(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    repo := album.NewRepo(d.WriteDB(), d.ReadDB())
    p := owners.Principal{Hub: "h", UserID: "u"}
    seedOwner(t, d.WriteDB(), p, "sk")
    a := seedAlbum(t, repo, p, "Trip")

    ids := make([]string, 3)
    base := time.Now().UTC().Truncate(time.Second)
    for i := range ids {
        ids[i] = uuid.NewString()
        seedMediaRow(t, d.WriteDB(), p, ids[i], "cs"+ids[i], "ready", 1)
        seedAlbumMedia(t, d.WriteDB(), a.ID, ids[i], base.Add(time.Duration(i)*time.Second))
    }

    got, err := repo.ListMedia(context.Background(), a.ID,
        album.AlbumMediaFilter{SortBy: "added", Limit: 2, Offset: 1})
    r.NoError(err)
    r.Len(got, 2)
    // added-desc → [ids[2], ids[1], ids[0]], offset=1 → [ids[1], ids[0]]
    r.Equal(ids[1], got[0].ID)
    r.Equal(ids[0], got[1].ID)
}

func TestRepoListMediaImportedSort(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    repo := album.NewRepo(d.WriteDB(), d.ReadDB())
    p := owners.Principal{Hub: "h", UserID: "u"}
    seedOwner(t, d.WriteDB(), p, "sk")
    a := seedAlbum(t, repo, p, "Trip")

    // seedMediaRow hardcodes imported_at = time.Now() at call time, so
    // we order the calls to match our expectation.
    early := uuid.NewString()
    late := uuid.NewString()
    seedMediaRow(t, d.WriteDB(), p, early, "cs-e", "ready", 1)
    time.Sleep(10 * time.Millisecond)
    seedMediaRow(t, d.WriteDB(), p, late, "cs-l", "ready", 1)
    seedAlbumMedia(t, d.WriteDB(), a.ID, early, time.Now().UTC())
    seedAlbumMedia(t, d.WriteDB(), a.ID, late, time.Now().UTC())

    got, err := repo.ListMedia(context.Background(), a.ID,
        album.AlbumMediaFilter{SortBy: "imported"})
    r.NoError(err)
    r.Len(got, 2)
    r.Equal(late, got[0].ID, "imported-desc → latest first")
    r.Equal(early, got[1].ID)
}
```

- [ ] **Step 7: Run the full album test package**

Run: `go test ./internal/album/... -v`
Expected: all PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/album/repo.go internal/album/repo_test.go internal/media/repo.go
git commit -m "Add album_media operations (AddMedia/RemoveMedia/ListMedia)"
```

---

### Task 5: Repo-level trigger defence-in-depth test

**Files:**
- Create: `internal/album/trigger_test.go`
- Modify: `internal/album/repo.go` (translate trigger error to ErrOwnerMismatch)

- [ ] **Step 1: Wrap the trigger error in AddMedia**

In `internal/album/repo.go`, replace the `AddMedia` error path so trigger failures surface as `errs.ErrOwnerMismatch`. Locate:

```go
    res, err := r.rw.ExecContext(ctx, q, args...)
    if err != nil {
        return 0, 0, fmt.Errorf("add album media: %w", err)
    }
```

and replace with:

```go
    res, err := r.rw.ExecContext(ctx, q, args...)
    if err != nil {
        // The album_media owner-consistency trigger raises with text
        // "album and media must share owner" when the album and media
        // belong to different owners. The service layer's pre-flight
        // should catch this first; hitting it here means the service
        // invariant is violated. Wrap as ErrOwnerMismatch so the HTTP
        // layer can translate it to 500 + log.
        if isAlbumMediaOwnerMismatch(err) {
            return 0, 0, fmt.Errorf("%w: album=%s media=%v",
                errs.ErrOwnerMismatch, albumID, mediaIDs)
        }
        return 0, 0, fmt.Errorf("add album media: %w", err)
    }
```

And append to `repo.go` (after `join`):

```go
// isAlbumMediaOwnerMismatch reports whether err is the SQLite trigger
// error raised by album_media_owner_consistency_{insert,update}. The
// trigger uses RAISE(ABORT, 'album and media must share owner') so the
// surface is stable and string-matchable.
func isAlbumMediaOwnerMismatch(err error) bool {
    return err != nil && strings.Contains(err.Error(), "album and media must share owner")
}
```

Add `"strings"` to the imports.

- [ ] **Step 2: Write the trigger test**

Create `internal/album/trigger_test.go`:

```go
package album_test

import (
    "context"
    "testing"
    "time"

    "github.com/google/uuid"
    "github.com/stretchr/testify/require"

    "github.com/wesm/fotobank/internal/album"
    "github.com/wesm/fotobank/internal/errs"
    "github.com/wesm/fotobank/internal/owners"
    "github.com/wesm/fotobank/internal/testutil"
)

// TestAddMediaCrossOwnerTrigger verifies the defence-in-depth path: if
// the service's pre-flight is bypassed and cross-owner IDs reach the
// repo, the SQLite trigger aborts the insert and the repo wraps the
// raw error as errs.ErrOwnerMismatch so errors.Is matches.
func TestAddMediaCrossOwnerTrigger(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    repo := album.NewRepo(d.WriteDB(), d.ReadDB())

    ownerA := owners.Principal{Hub: "h", UserID: "a"}
    ownerB := owners.Principal{Hub: "h", UserID: "b"}
    seedOwner(t, d.WriteDB(), ownerA, "sk-a")
    seedOwner(t, d.WriteDB(), ownerB, "sk-b")

    a := seedAlbum(t, repo, ownerA, "A-Trip") // album belongs to A
    mB := uuid.NewString()
    seedMediaRow(t, d.WriteDB(), ownerB, mB, "cs-b", "ready", 1) // media belongs to B

    _, _, err := repo.AddMedia(context.Background(), a.ID, []string{mB}, time.Now().UTC())
    r.Error(err)
    r.ErrorIs(err, errs.ErrOwnerMismatch)
}
```

- [ ] **Step 3: Run the trigger test**

Run: `go test ./internal/album/... -run TestAddMediaCrossOwnerTrigger -v`
Expected: PASS. If the test fails with an unwrapped SQLite error, the `strings.Contains` check in Step 1 is missing the trigger text — inspect `err.Error()` in the test.

- [ ] **Step 4: Commit**

```bash
git add internal/album/repo.go internal/album/trigger_test.go
git commit -m "Wrap album_media trigger error as errs.ErrOwnerMismatch"
```

---

### Task 6: AlbumService scaffolding + CRUD

**Files:**
- Create: `internal/service/album_service.go`
- Create: `internal/service/album_service_test.go`

- [ ] **Step 1: Write the service skeleton + Create / Get / GetDetail**

Create `internal/service/album_service.go`:

```go
// Package service — AlbumService is the auth-scoped entry point for
// album reads and writes. Every exported method takes caller
// owners.Principal; cross-owner access returns errs.ErrNotFound rather
// than ErrPermissionDenied so the surface cannot be used to probe for
// other owners' album or media IDs.
package service

import (
    "context"
    "fmt"
    "strings"
    "time"

    "github.com/google/uuid"

    "github.com/wesm/fotobank/internal/album"
    "github.com/wesm/fotobank/internal/errs"
    "github.com/wesm/fotobank/internal/media"
    "github.com/wesm/fotobank/internal/owners"
)

type AlbumService struct {
    albums *album.Repo
    media  *media.Repo
    now    func() time.Time
}

// NewAlbumService constructs an AlbumService. now defaults to time.Now().UTC();
// tests inject a deterministic clock via newAlbumServiceWithClock.
func NewAlbumService(a *album.Repo, m *media.Repo) *AlbumService {
    return &AlbumService{albums: a, media: m, now: func() time.Time { return time.Now().UTC() }}
}

// Create persists a new album owned by caller.
func (s *AlbumService) Create(
    ctx context.Context,
    caller owners.Principal,
    name string,
) (album.AlbumListItem, error) {
    trimmed := strings.TrimSpace(name)
    if trimmed == "" || len(trimmed) > album.NameMaxLen {
        return album.AlbumListItem{}, album.ErrInvalidName
    }
    now := s.now()
    a := album.Album{
        ID:        uuid.NewString(),
        Owner:     caller,
        Name:      trimmed,
        CreatedAt: now,
        UpdatedAt: now,
    }
    if err := s.albums.Insert(ctx, a); err != nil {
        return album.AlbumListItem{}, err
    }
    return album.AlbumListItem{Album: a, ItemCount: 0, Cover: nil}, nil
}

// Get returns the bare album if caller is its owner; ErrNotFound otherwise.
func (s *AlbumService) Get(
    ctx context.Context,
    id string,
    caller owners.Principal,
) (album.Album, error) {
    a, err := s.albums.GetByID(ctx, id)
    if err != nil {
        return album.Album{}, err
    }
    if a.Owner != caller {
        return album.Album{}, fmt.Errorf("%w: album id=%s", errs.ErrNotFound, id)
    }
    return a, nil
}

// GetDetail returns the album with derived ItemCount + Cover. ErrNotFound
// if missing or cross-owner.
func (s *AlbumService) GetDetail(
    ctx context.Context,
    id string,
    caller owners.Principal,
) (album.AlbumListItem, error) {
    item, err := s.albums.GetDetailByID(ctx, id)
    if err != nil {
        return album.AlbumListItem{}, err
    }
    if item.Owner != caller {
        return album.AlbumListItem{}, fmt.Errorf("%w: album id=%s", errs.ErrNotFound, id)
    }
    return item, nil
}
```

- [ ] **Step 2: Start the service test file with Create / Get / GetDetail coverage**

Create `internal/service/album_service_test.go`:

```go
package service_test

import (
    "context"
    "database/sql"
    "strings"
    "testing"
    "time"

    "github.com/stretchr/testify/require"

    "github.com/wesm/fotobank/internal/album"
    "github.com/wesm/fotobank/internal/errs"
    "github.com/wesm/fotobank/internal/media"
    "github.com/wesm/fotobank/internal/owners"
    "github.com/wesm/fotobank/internal/service"
    "github.com/wesm/fotobank/internal/testutil"
)

// albumSvcFixture bundles the service with the helpers tests need.
type albumSvcFixture struct {
    svc     *service.AlbumService
    albums  *album.Repo
    media   *media.Repo
    rw      *sql.DB
    caller  owners.Principal
}

func newAlbumSvcFixture(t *testing.T) albumSvcFixture {
    t.Helper()
    d := testutil.OpenTestDB(t)
    aRepo := album.NewRepo(d.WriteDB(), d.ReadDB())
    mRepo := media.NewRepo(d.WriteDB(), d.ReadDB())
    caller := owners.Principal{Hub: "h", UserID: "u"}
    seedOwnerSvc(t, d.WriteDB(), caller, "sk")
    return albumSvcFixture{
        svc:    service.NewAlbumService(aRepo, mRepo),
        albums: aRepo,
        media:  mRepo,
        rw:     d.WriteDB(),
        caller: caller,
    }
}

func seedOwnerSvc(t *testing.T, rw *sql.DB, p owners.Principal, sk string) {
    t.Helper()
    _, err := rw.ExecContext(context.Background(),
        `INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
        p.Hub, p.UserID, sk, time.Now().UTC(),
    )
    require.NoError(t, err)
}

func TestAlbumServiceCreateReturnsListItem(t *testing.T) {
    r := require.New(t)
    fx := newAlbumSvcFixture(t)

    it, err := fx.svc.Create(context.Background(), fx.caller, "Trip")
    r.NoError(err)
    r.NotEmpty(it.ID)
    r.Equal("Trip", it.Name)
    r.Equal(0, it.ItemCount)
    r.Nil(it.Cover)
    r.Equal(fx.caller, it.Owner)
}

func TestAlbumServiceCreateTrimsName(t *testing.T) {
    r := require.New(t)
    fx := newAlbumSvcFixture(t)

    it, err := fx.svc.Create(context.Background(), fx.caller, "  Trip  ")
    r.NoError(err)
    r.Equal("Trip", it.Name)
}

func TestAlbumServiceCreateRejectsInvalidName(t *testing.T) {
    fx := newAlbumSvcFixture(t)
    cases := []string{"", "   ", strings.Repeat("x", album.NameMaxLen+1)}
    for _, name := range cases {
        _, err := fx.svc.Create(context.Background(), fx.caller, name)
        require.ErrorIs(t, err, album.ErrInvalidName, "name=%q", name)
    }
}

func TestAlbumServiceGetCrossOwnerReturnsNotFound(t *testing.T) {
    r := require.New(t)
    fx := newAlbumSvcFixture(t)

    other := owners.Principal{Hub: "h", UserID: "other"}
    seedOwnerSvc(t, fx.rw, other, "sk-o")
    otherItem, err := fx.svc.Create(context.Background(), other, "OtherAlbum")
    r.NoError(err)

    _, err = fx.svc.Get(context.Background(), otherItem.ID, fx.caller)
    r.ErrorIs(err, errs.ErrNotFound)
}

func TestAlbumServiceGetDetailCrossOwnerReturnsNotFound(t *testing.T) {
    r := require.New(t)
    fx := newAlbumSvcFixture(t)

    other := owners.Principal{Hub: "h", UserID: "other"}
    seedOwnerSvc(t, fx.rw, other, "sk-o")
    otherItem, err := fx.svc.Create(context.Background(), other, "OtherAlbum")
    r.NoError(err)

    _, err = fx.svc.GetDetail(context.Background(), otherItem.ID, fx.caller)
    r.ErrorIs(err, errs.ErrNotFound)
}

func TestAlbumServiceGetDetailHappyPath(t *testing.T) {
    r := require.New(t)
    fx := newAlbumSvcFixture(t)

    it, err := fx.svc.Create(context.Background(), fx.caller, "Trip")
    r.NoError(err)

    got, err := fx.svc.GetDetail(context.Background(), it.ID, fx.caller)
    r.NoError(err)
    r.Equal(it.ID, got.ID)
    r.Equal(0, got.ItemCount)
    r.Nil(got.Cover)
}
```

- [ ] **Step 3: Run the service tests**

Run: `go test ./internal/service/... -run TestAlbumService -v`
Expected: all PASS.

- [ ] **Step 4: Append Rename + Delete + List to album_service.go**

```go
// Rename validates the new name, updates the row, and returns the fresh
// detail (with ItemCount + Cover computed as of *after* the update).
// ErrNotFound if the album is missing or cross-owner.
func (s *AlbumService) Rename(
    ctx context.Context,
    id, name string,
    caller owners.Principal,
) (album.AlbumListItem, error) {
    trimmed := strings.TrimSpace(name)
    if trimmed == "" || len(trimmed) > album.NameMaxLen {
        return album.AlbumListItem{}, album.ErrInvalidName
    }
    // Cheap owner check before the UPDATE.
    if _, err := s.Get(ctx, id, caller); err != nil {
        return album.AlbumListItem{}, err
    }
    if err := s.albums.Rename(ctx, id, trimmed, s.now()); err != nil {
        return album.AlbumListItem{}, err
    }
    return s.albums.GetDetailByID(ctx, id)
}

// Delete removes the album. album_media is cascaded by the FK.
// ErrNotFound if the album is missing or cross-owner.
func (s *AlbumService) Delete(
    ctx context.Context,
    id string,
    caller owners.Principal,
) error {
    if _, err := s.Get(ctx, id, caller); err != nil {
        return err
    }
    return s.albums.Delete(ctx, id)
}

// List returns the caller's albums with derived ItemCount + Cover.
func (s *AlbumService) List(
    ctx context.Context,
    caller owners.Principal,
    limit, offset int,
) ([]album.AlbumListItem, error) {
    return s.albums.ListByOwner(ctx, caller, limit, offset)
}
```

- [ ] **Step 5: Add Rename / Delete / List tests**

Append to `album_service_test.go`:

```go
func TestAlbumServiceRenameReturnsUpdatedDetail(t *testing.T) {
    r := require.New(t)
    fx := newAlbumSvcFixture(t)

    it, err := fx.svc.Create(context.Background(), fx.caller, "Old")
    r.NoError(err)

    got, err := fx.svc.Rename(context.Background(), it.ID, "New", fx.caller)
    r.NoError(err)
    r.Equal("New", got.Name)
    r.Equal(it.ID, got.ID)
    r.True(got.UpdatedAt.After(it.UpdatedAt) || got.UpdatedAt.Equal(it.UpdatedAt))
}

func TestAlbumServiceRenameCrossOwner(t *testing.T) {
    r := require.New(t)
    fx := newAlbumSvcFixture(t)

    other := owners.Principal{Hub: "h", UserID: "other"}
    seedOwnerSvc(t, fx.rw, other, "sk-o")
    otherIt, err := fx.svc.Create(context.Background(), other, "Theirs")
    r.NoError(err)

    _, err = fx.svc.Rename(context.Background(), otherIt.ID, "Mine", fx.caller)
    r.ErrorIs(err, errs.ErrNotFound)
}

func TestAlbumServiceRenameInvalidName(t *testing.T) {
    r := require.New(t)
    fx := newAlbumSvcFixture(t)
    it, err := fx.svc.Create(context.Background(), fx.caller, "x")
    r.NoError(err)

    _, err = fx.svc.Rename(context.Background(), it.ID, "", fx.caller)
    r.ErrorIs(err, album.ErrInvalidName)
}

func TestAlbumServiceDeleteCrossOwner(t *testing.T) {
    r := require.New(t)
    fx := newAlbumSvcFixture(t)

    other := owners.Principal{Hub: "h", UserID: "other"}
    seedOwnerSvc(t, fx.rw, other, "sk-o")
    otherIt, err := fx.svc.Create(context.Background(), other, "Theirs")
    r.NoError(err)

    err = fx.svc.Delete(context.Background(), otherIt.ID, fx.caller)
    r.ErrorIs(err, errs.ErrNotFound)
}

func TestAlbumServiceDeleteHappyPath(t *testing.T) {
    r := require.New(t)
    fx := newAlbumSvcFixture(t)

    it, err := fx.svc.Create(context.Background(), fx.caller, "Trip")
    r.NoError(err)
    r.NoError(fx.svc.Delete(context.Background(), it.ID, fx.caller))
    _, err = fx.svc.Get(context.Background(), it.ID, fx.caller)
    r.ErrorIs(err, errs.ErrNotFound)
}

func TestAlbumServiceListIsolatesOwners(t *testing.T) {
    r := require.New(t)
    fx := newAlbumSvcFixture(t)

    other := owners.Principal{Hub: "h", UserID: "other"}
    seedOwnerSvc(t, fx.rw, other, "sk-o")

    _, err := fx.svc.Create(context.Background(), fx.caller, "Mine-1")
    r.NoError(err)
    _, err = fx.svc.Create(context.Background(), fx.caller, "Mine-2")
    r.NoError(err)
    _, err = fx.svc.Create(context.Background(), other, "Theirs")
    r.NoError(err)

    items, err := fx.svc.List(context.Background(), fx.caller, 10, 0)
    r.NoError(err)
    r.Len(items, 2)
    for _, it := range items {
        r.Equal(fx.caller, it.Owner)
    }
}
```

- [ ] **Step 6: Run the full service test package**

Run: `go test ./internal/service/... -run TestAlbumService -v`
Expected: all PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/service/album_service.go internal/service/album_service_test.go
git commit -m "Add AlbumService CRUD + Get/GetDetail/List"
```

---

### Task 7: AlbumService.AddMedia / RemoveMedia / ListMedia

**Files:**
- Modify: `internal/service/album_service.go`
- Modify: `internal/service/album_service_test.go`

- [ ] **Step 1: Append AddMedia to album_service.go**

Add `"errors"` to the import block at the top of `album_service.go` (used below by `errors.Is`).

```go
// AddMedia validates the album is caller-owned, deduplicates input IDs
// (preserving first-seen order), length-checks the deduped batch, then
// performs a per-ID pre-flight ownership check via media.Repo.GetByID
// before the batched INSERT.
//
// Errors:
//   - errs.ErrNotFound if the album is missing or cross-owner, OR if any
//     media_id is missing, OR if any media_id belongs to a different
//     owner. The three are indistinguishable by design — a user who does
//     not own a media row must not learn whether it exists.
//   - album.ErrInvalidBatch if the deduped batch is empty or > 500.
//
// errs.ErrOwnerMismatch is reserved for the defence-in-depth path: it
// only surfaces if the trigger fires, which means this pre-flight missed
// a row. The HTTP layer maps that to 500, not 403.
func (s *AlbumService) AddMedia(
    ctx context.Context,
    albumID string,
    mediaIDs []string,
    caller owners.Principal,
) (added, alreadyPresent int, err error) {
    // Owner check on the album.
    if _, err := s.Get(ctx, albumID, caller); err != nil {
        return 0, 0, err
    }

    deduped := dedupeStrings(mediaIDs)
    if len(deduped) == 0 || len(deduped) > album.BatchMaxLen {
        return 0, 0, album.ErrInvalidBatch
    }

    for _, mid := range deduped {
        m, mErr := s.media.GetByID(ctx, mid)
        if mErr != nil {
            if errors.Is(mErr, errs.ErrNotFound) {
                return 0, 0, fmt.Errorf("%w: media id=%s", errs.ErrNotFound, mid)
            }
            return 0, 0, mErr
        }
        if m.Owner != caller {
            // Mask cross-owner as not-found (existence-oracle protection).
            return 0, 0, fmt.Errorf("%w: media id=%s", errs.ErrNotFound, mid)
        }
    }

    return s.albums.AddMedia(ctx, albumID, deduped, s.now())
}

// dedupeStrings returns ids with later duplicates removed, preserving
// first-seen order. Empty input returns a nil slice.
func dedupeStrings(ids []string) []string {
    if len(ids) == 0 {
        return nil
    }
    seen := make(map[string]struct{}, len(ids))
    out := make([]string, 0, len(ids))
    for _, id := range ids {
        if _, ok := seen[id]; ok {
            continue
        }
        seen[id] = struct{}{}
        out = append(out, id)
    }
    return out
}
```

- [ ] **Step 2: Add AddMedia tests**

Add `"github.com/google/uuid"` to the import block at the top of `album_service_test.go` (used below by `uuid.NewString()`).

Append to `album_service_test.go` — first the `seedMediaSvc` helper (new in this task; Task 6 had no tests that needed media rows), then the tests:

```go
// seedMediaSvc inserts a minimal media row directly.
func seedMediaSvc(t *testing.T, rw *sql.DB, p owners.Principal, id, checksum string) {
    t.Helper()
    _, err := rw.ExecContext(context.Background(), `
INSERT INTO media (
    id, owner_hub, owner_user_id, media_type, mime_type, path, original_filename,
    imported_at, timestamp, size, checksum,
    make, model, focal_length, shutter, width, height, iso, aperture,
    duration_ms,
    thumb_status, thumb_version, thumb_updated_at
) VALUES (?, ?, ?, 'photo', 'image/jpeg', ?, NULL, ?, NULL, 0, ?,
          NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL,
          'ready', 1, NULL)`,
        id, p.Hub, p.UserID, "p/"+id, time.Now().UTC(), checksum,
    )
    require.NoError(t, err)
}

func TestAlbumServiceAddMediaHappyPath(t *testing.T) {
    r := require.New(t)
    fx := newAlbumSvcFixture(t)
    it, err := fx.svc.Create(context.Background(), fx.caller, "Trip")
    r.NoError(err)
    m1 := uuid.NewString()
    m2 := uuid.NewString()
    seedMediaSvc(t, fx.rw, fx.caller, m1, "cs1")
    seedMediaSvc(t, fx.rw, fx.caller, m2, "cs2")

    added, already, err := fx.svc.AddMedia(context.Background(), it.ID,
        []string{m1, m2}, fx.caller)
    r.NoError(err)
    r.Equal(2, added)
    r.Equal(0, already)
}

func TestAlbumServiceAddMediaDedupesInput(t *testing.T) {
    r := require.New(t)
    fx := newAlbumSvcFixture(t)
    it, err := fx.svc.Create(context.Background(), fx.caller, "Trip")
    r.NoError(err)
    m1 := uuid.NewString()
    m2 := uuid.NewString()
    seedMediaSvc(t, fx.rw, fx.caller, m1, "cs1")
    seedMediaSvc(t, fx.rw, fx.caller, m2, "cs2")

    added, already, err := fx.svc.AddMedia(context.Background(), it.ID,
        []string{m1, m1, m2}, fx.caller)
    r.NoError(err)
    r.Equal(2, added)
    r.Equal(0, already)

    added2, already2, err := fx.svc.AddMedia(context.Background(), it.ID,
        []string{m1, m2}, fx.caller)
    r.NoError(err)
    r.Equal(0, added2)
    r.Equal(2, already2)
}

func TestAlbumServiceAddMediaEmptyBatchInvalid(t *testing.T) {
    r := require.New(t)
    fx := newAlbumSvcFixture(t)
    it, err := fx.svc.Create(context.Background(), fx.caller, "Trip")
    r.NoError(err)

    _, _, err = fx.svc.AddMedia(context.Background(), it.ID, nil, fx.caller)
    r.ErrorIs(err, album.ErrInvalidBatch)
}

func TestAlbumServiceAddMediaOverCapInvalid(t *testing.T) {
    r := require.New(t)
    fx := newAlbumSvcFixture(t)
    it, err := fx.svc.Create(context.Background(), fx.caller, "Trip")
    r.NoError(err)

    ids := make([]string, album.BatchMaxLen+1)
    for i := range ids {
        ids[i] = uuid.NewString()
    }
    _, _, err = fx.svc.AddMedia(context.Background(), it.ID, ids, fx.caller)
    r.ErrorIs(err, album.ErrInvalidBatch)
}

func TestAlbumServiceAddMediaDedupePassesLengthCheck(t *testing.T) {
    // 502 ids with 2 duplicates → dedupes to 500 → valid.
    r := require.New(t)
    fx := newAlbumSvcFixture(t)
    it, err := fx.svc.Create(context.Background(), fx.caller, "Trip")
    r.NoError(err)

    ids := make([]string, 0, 502)
    distinct := make([]string, 500)
    for i := range distinct {
        id := uuid.NewString()
        distinct[i] = id
        seedMediaSvc(t, fx.rw, fx.caller, id, "cs-"+id)
    }
    ids = append(ids, distinct...)
    // Introduce 2 duplicates.
    ids = append(ids, distinct[0], distinct[1])

    added, already, err := fx.svc.AddMedia(context.Background(), it.ID, ids, fx.caller)
    r.NoError(err)
    r.Equal(500, added)
    r.Equal(0, already)
}

func TestAlbumServiceAddMediaUnknownIDIsNotFound(t *testing.T) {
    r := require.New(t)
    fx := newAlbumSvcFixture(t)
    it, err := fx.svc.Create(context.Background(), fx.caller, "Trip")
    r.NoError(err)

    _, _, err = fx.svc.AddMedia(context.Background(), it.ID,
        []string{uuid.NewString()}, fx.caller)
    r.ErrorIs(err, errs.ErrNotFound)
}

func TestAlbumServiceAddMediaCrossOwnerMaskedAsNotFound(t *testing.T) {
    // Cross-owner media must NOT return ErrOwnerMismatch (which would
    // leak existence). The spec requires ErrNotFound here.
    r := require.New(t)
    fx := newAlbumSvcFixture(t)
    it, err := fx.svc.Create(context.Background(), fx.caller, "Trip")
    r.NoError(err)
    other := owners.Principal{Hub: "h", UserID: "other"}
    seedOwnerSvc(t, fx.rw, other, "sk-o")
    otherMedia := uuid.NewString()
    seedMediaSvc(t, fx.rw, other, otherMedia, "cs-o")

    _, _, err = fx.svc.AddMedia(context.Background(), it.ID,
        []string{otherMedia}, fx.caller)
    r.ErrorIs(err, errs.ErrNotFound)
    r.NotErrorIs(err, errs.ErrOwnerMismatch, "must mask cross-owner as not-found")
}

func TestAlbumServiceAddMediaCrossOwnerAlbumNotFound(t *testing.T) {
    r := require.New(t)
    fx := newAlbumSvcFixture(t)
    other := owners.Principal{Hub: "h", UserID: "other"}
    seedOwnerSvc(t, fx.rw, other, "sk-o")
    otherIt, err := fx.svc.Create(context.Background(), other, "Theirs")
    r.NoError(err)
    m := uuid.NewString()
    seedMediaSvc(t, fx.rw, fx.caller, m, "cs")

    _, _, err = fx.svc.AddMedia(context.Background(), otherIt.ID,
        []string{m}, fx.caller)
    r.ErrorIs(err, errs.ErrNotFound)
}
```

- [ ] **Step 3: Run the AddMedia tests**

Run: `go test ./internal/service/... -run TestAlbumServiceAddMedia -v`
Expected: all PASS.

- [ ] **Step 4: Append RemoveMedia + ListMedia to album_service.go**

```go
// RemoveMedia removes one media row from an album. ErrNotFound if the
// album is missing/cross-owner, or if (album, media) is not present.
func (s *AlbumService) RemoveMedia(
    ctx context.Context,
    albumID, mediaID string,
    caller owners.Principal,
) error {
    if _, err := s.Get(ctx, albumID, caller); err != nil {
        return err
    }
    return s.albums.RemoveMedia(ctx, albumID, mediaID)
}

// ListMedia returns paginated media rows for an album. The caller's
// ownership is checked via the album's owner; cross-owner returns
// ErrNotFound. SortBy is validated here so the repo never sees invalid
// values.
func (s *AlbumService) ListMedia(
    ctx context.Context,
    albumID string,
    filter album.AlbumMediaFilter,
    caller owners.Principal,
) ([]media.Media, error) {
    switch filter.SortBy {
    case "", "added", "imported":
        // ok
    default:
        return nil, album.ErrInvalidSort
    }
    if _, err := s.Get(ctx, albumID, caller); err != nil {
        return nil, err
    }
    return s.albums.ListMedia(ctx, albumID, filter)
}
```

- [ ] **Step 5: Add RemoveMedia + ListMedia tests**

Append to `album_service_test.go`:

```go
func TestAlbumServiceRemoveMediaHappyPath(t *testing.T) {
    r := require.New(t)
    fx := newAlbumSvcFixture(t)
    it, err := fx.svc.Create(context.Background(), fx.caller, "Trip")
    r.NoError(err)
    m := uuid.NewString()
    seedMediaSvc(t, fx.rw, fx.caller, m, "cs")
    _, _, err = fx.svc.AddMedia(context.Background(), it.ID, []string{m}, fx.caller)
    r.NoError(err)

    r.NoError(fx.svc.RemoveMedia(context.Background(), it.ID, m, fx.caller))
}

func TestAlbumServiceRemoveMediaCrossOwnerAlbum(t *testing.T) {
    r := require.New(t)
    fx := newAlbumSvcFixture(t)
    other := owners.Principal{Hub: "h", UserID: "other"}
    seedOwnerSvc(t, fx.rw, other, "sk-o")
    otherIt, err := fx.svc.Create(context.Background(), other, "Theirs")
    r.NoError(err)

    err = fx.svc.RemoveMedia(context.Background(), otherIt.ID, "anything", fx.caller)
    r.ErrorIs(err, errs.ErrNotFound)
}

func TestAlbumServiceRemoveMediaNotInAlbum(t *testing.T) {
    r := require.New(t)
    fx := newAlbumSvcFixture(t)
    it, err := fx.svc.Create(context.Background(), fx.caller, "Trip")
    r.NoError(err)

    err = fx.svc.RemoveMedia(context.Background(), it.ID, "nonesuch", fx.caller)
    r.ErrorIs(err, errs.ErrNotFound)
}

func TestAlbumServiceListMediaInvalidSort(t *testing.T) {
    r := require.New(t)
    fx := newAlbumSvcFixture(t)
    it, err := fx.svc.Create(context.Background(), fx.caller, "Trip")
    r.NoError(err)

    _, err = fx.svc.ListMedia(context.Background(), it.ID,
        album.AlbumMediaFilter{SortBy: "name"}, fx.caller)
    r.ErrorIs(err, album.ErrInvalidSort)
}

func TestAlbumServiceListMediaCrossOwner(t *testing.T) {
    r := require.New(t)
    fx := newAlbumSvcFixture(t)
    other := owners.Principal{Hub: "h", UserID: "other"}
    seedOwnerSvc(t, fx.rw, other, "sk-o")
    otherIt, err := fx.svc.Create(context.Background(), other, "Theirs")
    r.NoError(err)

    _, err = fx.svc.ListMedia(context.Background(), otherIt.ID,
        album.AlbumMediaFilter{SortBy: "added"}, fx.caller)
    r.ErrorIs(err, errs.ErrNotFound)
}

func TestAlbumServiceListMediaHappyPath(t *testing.T) {
    r := require.New(t)
    fx := newAlbumSvcFixture(t)
    it, err := fx.svc.Create(context.Background(), fx.caller, "Trip")
    r.NoError(err)
    m1 := uuid.NewString()
    m2 := uuid.NewString()
    seedMediaSvc(t, fx.rw, fx.caller, m1, "cs1")
    seedMediaSvc(t, fx.rw, fx.caller, m2, "cs2")
    _, _, err = fx.svc.AddMedia(context.Background(), it.ID, []string{m1, m2}, fx.caller)
    r.NoError(err)

    got, err := fx.svc.ListMedia(context.Background(), it.ID,
        album.AlbumMediaFilter{SortBy: "added"}, fx.caller)
    r.NoError(err)
    r.Len(got, 2)
}
```

- [ ] **Step 6: Run the service tests**

Run: `go test ./internal/service/... -run TestAlbumService -v`
Expected: all PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/service/album_service.go internal/service/album_service_test.go
git commit -m "Add AlbumService AddMedia/RemoveMedia/ListMedia with dedupe + preflight"
```

---

### Task 8: HTTP translator + album CRUD routes

**Files:**
- Create: `internal/httpapi/albums.go`
- Create: `internal/httpapi/albums_test.go`
- Modify: `internal/httpapi/api.go`

- [ ] **Step 1: Scaffold the translator and register function**

Create `internal/httpapi/albums.go`:

```go
package httpapi

import (
    "context"
    "errors"
    "net/http"
    "time"

    "github.com/danielgtaylor/huma/v2"

    "github.com/wesm/fotobank/internal/album"
    "github.com/wesm/fotobank/internal/errs"
    "github.com/wesm/fotobank/internal/service"
)

// translateAlbumError maps service-layer errors to huma.StatusError with
// Plan D's rules (which differ from the global Translate):
//
//   - errs.ErrOwnerMismatch → 500. In the albums surface this means the
//     AlbumService pre-flight missed a row and the DB trigger fired.
//     The global Translate would return 403, leaking existence.
//   - album.ErrInvalidName / ErrInvalidBatch / ErrInvalidSort → 400 with
//     explicit wire strings (not the sentinels' "album: ..." prefix).
//   - everything else → delegate to the shared httpapi.Translate.
//
// Callers log the original err before returning; this wrapper only maps.
func translateAlbumError(err error) huma.StatusError {
    switch {
    case errors.Is(err, errs.ErrOwnerMismatch):
        return huma.Error500InternalServerError(http.StatusText(http.StatusInternalServerError))
    case errors.Is(err, album.ErrInvalidName):
        return huma.Error400BadRequest("name must be 1..200 chars")
    case errors.Is(err, album.ErrInvalidBatch):
        return huma.Error400BadRequest("batch size must be 1..500")
    case errors.Is(err, album.ErrInvalidSort):
        return huma.Error400BadRequest("sort_by must be added or imported")
    default:
        return Translate(err)
    }
}

// coverDTO / albumDTO are the wire shapes for albums.
type coverDTO struct {
    MediaID      string `json:"media_id"`
    ThumbVersion int    `json:"thumb_version"`
}

type albumDTO struct {
    ID        string    `json:"id"`
    Name      string    `json:"name"`
    ItemCount int       `json:"item_count"`
    Cover     *coverDTO `json:"cover,omitempty"`
    CreatedAt time.Time `json:"created_at"`
    UpdatedAt time.Time `json:"updated_at"`
}

func toAlbumDTO(it album.AlbumListItem) albumDTO {
    out := albumDTO{
        ID:        it.ID,
        Name:      it.Name,
        ItemCount: it.ItemCount,
        CreatedAt: it.CreatedAt,
        UpdatedAt: it.UpdatedAt,
    }
    if it.Cover != nil {
        out.Cover = &coverDTO{MediaID: it.Cover.MediaID, ThumbVersion: it.Cover.ThumbVersion}
    }
    return out
}

// registerAlbums is the entry point called from buildAPI. svc may be
// nil; in that case every operation answers 503 Service Unavailable so
// the OpenAPI spec dumper can pass an empty Deps.
func registerAlbums(api huma.API, svc *service.AlbumService) {
    registerAlbumsCRUD(api, svc)
    registerAlbumMedia(api, svc)
}

// Placeholder so Step 2 (CRUD registration) has a symbol to call into.
func registerAlbumsCRUD(api huma.API, svc *service.AlbumService) {
    _ = api
    _ = svc
}

// Placeholder used by Step 4 (album_media routes).
func registerAlbumMedia(api huma.API, svc *service.AlbumService) {
    _ = api
    _ = svc
}
```

- [ ] **Step 2: Write translator unit tests**

Create `internal/httpapi/albums_test.go`:

```go
package httpapi_test

import (
    "errors"
    "testing"

    "github.com/stretchr/testify/require"

    "github.com/wesm/fotobank/internal/album"
    "github.com/wesm/fotobank/internal/errs"
    "github.com/wesm/fotobank/internal/httpapi"
)

func TestTranslateAlbumErrorOwnerMismatchMapsTo500(t *testing.T) {
    // Albums override: the global Translate returns 403 for
    // ErrOwnerMismatch; the albums surface must never leak a 403.
    r := require.New(t)
    got := httpapi.TranslateAlbumErrorForTest(errs.ErrOwnerMismatch)
    r.NotNil(got)
    r.Equal(500, httpapi.StatusFrom(got))
    r.NotContains(got.Error(), errs.ErrOwnerMismatch.Error(),
        "500 body must not leak the sentinel message")

    // The shared Translate still returns 403 — the local wrapper is a
    // per-surface override, not a global mutation.
    r.Equal(403, httpapi.StatusFrom(httpapi.Translate(errs.ErrOwnerMismatch)))
}

func TestTranslateAlbumErrorInvalidSentinels(t *testing.T) {
    cases := []struct {
        in       error
        wantCode int
        wantBody string
    }{
        {album.ErrInvalidName, 400, "name must be 1..200 chars"},
        {album.ErrInvalidBatch, 400, "batch size must be 1..500"},
        {album.ErrInvalidSort, 400, "sort_by must be added or imported"},
    }
    for _, c := range cases {
        got := httpapi.TranslateAlbumErrorForTest(c.in)
        require.NotNil(t, got)
        require.Equal(t, c.wantCode, httpapi.StatusFrom(got), "%v", c.in)
        require.Equal(t, c.wantBody, got.Error())
        require.NotContains(t, got.Error(), "album:",
            "wire string must not echo the sentinel prefix")
    }
}

func TestTranslateAlbumErrorDelegatesToShared(t *testing.T) {
    r := require.New(t)
    // ErrNotFound is not handled locally; it should flow through Translate → 404.
    r.Equal(404, httpapi.StatusFrom(httpapi.TranslateAlbumErrorForTest(errs.ErrNotFound)))
    // A random error → 500 via shared default.
    r.Equal(500, httpapi.StatusFrom(httpapi.TranslateAlbumErrorForTest(errors.New("x"))))
    // Nil → nil.
    r.Nil(httpapi.TranslateAlbumErrorForTest(nil))
}
```

Because `translateAlbumError` is unexported, add a test-only export at the bottom of `internal/httpapi/albums.go`:

```go
// TranslateAlbumErrorForTest is an internal-only export so albums_test
// (in package httpapi_test) can unit-test translateAlbumError without
// promoting the symbol into the public API.
func TranslateAlbumErrorForTest(err error) huma.StatusError {
    if err == nil {
        return nil
    }
    return translateAlbumError(err)
}
```

- [ ] **Step 3: Run the translator tests**

Run: `go test ./internal/httpapi/... -run TestTranslateAlbumError -v`
Expected: all PASS.

- [ ] **Step 4: Implement CRUD route registration**

Replace the placeholder `registerAlbumsCRUD` in `internal/httpapi/albums.go`:

```go
const (
    albumsListDefaultLimit = 100
    albumsListMaxLimit     = 1000
)

type listAlbumsInput struct {
    Limit  int `query:"limit" doc:"max rows to return (default 100, cap 1000)"`
    Offset int `query:"offset" doc:"pagination offset"`
}

type listAlbumsOutput struct {
    Body struct {
        Items      []albumDTO `json:"items"`
        NextOffset *int       `json:"next_offset,omitempty"`
    }
}

type createAlbumInput struct {
    Body struct {
        Name string `json:"name"`
    }
}

type createAlbumOutput struct {
    Status int
    Body   albumDTO
}

type getAlbumInput struct {
    ID string `path:"id"`
}

type getAlbumOutput struct {
    Body albumDTO
}

type patchAlbumInput struct {
    ID   string `path:"id"`
    Body struct {
        Name string `json:"name"`
    }
}

type deleteAlbumInput struct {
    ID string `path:"id"`
}

type deleteAlbumOutput struct {
    Status int
}

func clampLimit(in, def, maxCap int) int {
    if in <= 0 {
        return def
    }
    if in > maxCap {
        return maxCap
    }
    return in
}

func registerAlbumsCRUD(api huma.API, svc *service.AlbumService) {
    huma.Register(api, huma.Operation{
        OperationID: "list-albums",
        Method:      http.MethodGet,
        Path:        "/api/v1/albums",
        Summary:     "List albums belonging to the caller",
    }, func(ctx context.Context, in *listAlbumsInput) (*listAlbumsOutput, error) {
        if svc == nil {
            return nil, huma.Error503ServiceUnavailable("album service not configured")
        }
        id, ok := IdentityFromContext(ctx)
        if !ok {
            return nil, huma.Error401Unauthorized(errs.ErrIdentityMissing.Error())
        }
        limit := clampLimit(in.Limit, albumsListDefaultLimit, albumsListMaxLimit)
        offset := max(in.Offset, 0)
        rows, err := svc.List(ctx, id.Principal.OwnersPrincipal(), limit+1, offset)
        if err != nil {
            return nil, translateAlbumError(err)
        }
        out := &listAlbumsOutput{}
        hasMore := len(rows) > limit
        if hasMore {
            rows = rows[:limit]
            next := offset + limit
            out.Body.NextOffset = &next
        }
        out.Body.Items = make([]albumDTO, 0, len(rows))
        for _, it := range rows {
            out.Body.Items = append(out.Body.Items, toAlbumDTO(it))
        }
        return out, nil
    })

    huma.Register(api, huma.Operation{
        OperationID:   "create-album",
        Method:        http.MethodPost,
        Path:          "/api/v1/albums",
        Summary:       "Create an album owned by the caller",
        DefaultStatus: http.StatusCreated,
    }, func(ctx context.Context, in *createAlbumInput) (*createAlbumOutput, error) {
        if svc == nil {
            return nil, huma.Error503ServiceUnavailable("album service not configured")
        }
        id, ok := IdentityFromContext(ctx)
        if !ok {
            return nil, huma.Error401Unauthorized(errs.ErrIdentityMissing.Error())
        }
        it, err := svc.Create(ctx, id.Principal.OwnersPrincipal(), in.Body.Name)
        if err != nil {
            return nil, translateAlbumError(err)
        }
        return &createAlbumOutput{Status: http.StatusCreated, Body: toAlbumDTO(it)}, nil
    })

    huma.Register(api, huma.Operation{
        OperationID: "get-album",
        Method:      http.MethodGet,
        Path:        "/api/v1/albums/{id}",
        Summary:     "Return detail for a single album",
    }, func(ctx context.Context, in *getAlbumInput) (*getAlbumOutput, error) {
        if svc == nil {
            return nil, huma.Error503ServiceUnavailable("album service not configured")
        }
        id, ok := IdentityFromContext(ctx)
        if !ok {
            return nil, huma.Error401Unauthorized(errs.ErrIdentityMissing.Error())
        }
        it, err := svc.GetDetail(ctx, in.ID, id.Principal.OwnersPrincipal())
        if err != nil {
            return nil, translateAlbumError(err)
        }
        return &getAlbumOutput{Body: toAlbumDTO(it)}, nil
    })

    huma.Register(api, huma.Operation{
        OperationID: "rename-album",
        Method:      http.MethodPatch,
        Path:        "/api/v1/albums/{id}",
        Summary:     "Rename an album (returns the updated detail)",
    }, func(ctx context.Context, in *patchAlbumInput) (*getAlbumOutput, error) {
        if svc == nil {
            return nil, huma.Error503ServiceUnavailable("album service not configured")
        }
        id, ok := IdentityFromContext(ctx)
        if !ok {
            return nil, huma.Error401Unauthorized(errs.ErrIdentityMissing.Error())
        }
        it, err := svc.Rename(ctx, in.ID, in.Body.Name, id.Principal.OwnersPrincipal())
        if err != nil {
            return nil, translateAlbumError(err)
        }
        return &getAlbumOutput{Body: toAlbumDTO(it)}, nil
    })

    huma.Register(api, huma.Operation{
        OperationID:   "delete-album",
        Method:        http.MethodDelete,
        Path:          "/api/v1/albums/{id}",
        Summary:       "Delete an album (cascades album_media)",
        DefaultStatus: http.StatusNoContent,
    }, func(ctx context.Context, in *deleteAlbumInput) (*deleteAlbumOutput, error) {
        if svc == nil {
            return nil, huma.Error503ServiceUnavailable("album service not configured")
        }
        id, ok := IdentityFromContext(ctx)
        if !ok {
            return nil, huma.Error401Unauthorized(errs.ErrIdentityMissing.Error())
        }
        if err := svc.Delete(ctx, in.ID, id.Principal.OwnersPrincipal()); err != nil {
            return nil, translateAlbumError(err)
        }
        return &deleteAlbumOutput{Status: http.StatusNoContent}, nil
    })
}
```

- [ ] **Step 5: Wire registerAlbums into buildAPI**

Edit `internal/httpapi/api.go`. Locate the `Deps` struct (around line 21) and append:

```go
    // AlbumService powers /api/v1/albums. Nil means those routes aren't
    // registered; tests and the OpenAPI spec dumper that don't need
    // album ops can pass Deps without an AlbumService.
    AlbumService *service.AlbumService
```

Locate `buildAPI` (around line 57) and add one line after the existing registrations:

```go
    registerAlbums(api, deps.AlbumService)
```

- [ ] **Step 6: Write album CRUD HTTP tests**

Append to `internal/httpapi/albums_test.go`:

```go
import (
    "bytes"
    "context"
    "database/sql"
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "testing"
    "time"

    "github.com/stretchr/testify/require"

    "github.com/wesm/fotobank/internal/album"
    "github.com/wesm/fotobank/internal/errs"
    "github.com/wesm/fotobank/internal/httpapi"
    "github.com/wesm/fotobank/internal/identity"
    "github.com/wesm/fotobank/internal/media"
    "github.com/wesm/fotobank/internal/owners"
    "github.com/wesm/fotobank/internal/service"
    "github.com/wesm/fotobank/internal/testutil"
)
```

Note: consolidate imports with the ones added earlier — Go compilers fail on duplicates. Replace the existing import block at the top of `albums_test.go` with the single merged block above.

Then append the fixture + tests:

```go
type albumsAPIFixture struct {
    srv    *httptest.Server
    owner  owners.Principal
    svc    *service.AlbumService
    rw     *sql.DB
}

func newAlbumsAPIFixture(t *testing.T) albumsAPIFixture {
    t.Helper()
    d := testutil.OpenTestDB(t)
    p := owners.Principal{Hub: "h", UserID: "u"}
    _, err := d.WriteDB().ExecContext(context.Background(),
        `INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
        p.Hub, p.UserID, "sk", time.Now().UTC(),
    )
    require.NoError(t, err)
    aRepo := album.NewRepo(d.WriteDB(), d.ReadDB())
    mRepo := media.NewRepo(d.WriteDB(), d.ReadDB())
    svc := service.NewAlbumService(aRepo, mRepo)
    idp := identity.NewStub(p, "Test User")
    h, err := httpapi.New(httpapi.Deps{IdentityProvider: idp, AlbumService: svc})
    require.NoError(t, err)
    srv := httptest.NewServer(h)
    t.Cleanup(srv.Close)
    return albumsAPIFixture{srv: srv, owner: p, svc: svc, rw: d.WriteDB()}
}

func TestCreateAlbumReturns201(t *testing.T) {
    r := require.New(t)
    fx := newAlbumsAPIFixture(t)

    body, _ := json.Marshal(map[string]string{"name": "Trip"})
    resp, err := http.Post(fx.srv.URL+"/api/v1/albums", "application/json", bytes.NewReader(body))
    r.NoError(err)
    defer resp.Body.Close()
    r.Equal(http.StatusCreated, resp.StatusCode)

    var got map[string]any
    r.NoError(json.NewDecoder(resp.Body).Decode(&got))
    r.Equal("Trip", got["name"])
    r.EqualValues(0, got["item_count"])
    r.NotContains(got, "cover") // omitempty
}

func TestCreateAlbumInvalidName(t *testing.T) {
    r := require.New(t)
    fx := newAlbumsAPIFixture(t)

    body, _ := json.Marshal(map[string]string{"name": "  "})
    resp, err := http.Post(fx.srv.URL+"/api/v1/albums", "application/json", bytes.NewReader(body))
    r.NoError(err)
    defer resp.Body.Close()
    r.Equal(http.StatusBadRequest, resp.StatusCode)
}

func TestGetAlbumDetail(t *testing.T) {
    r := require.New(t)
    fx := newAlbumsAPIFixture(t)
    it, err := fx.svc.Create(context.Background(), fx.owner, "Trip")
    r.NoError(err)

    resp, err := http.Get(fx.srv.URL + "/api/v1/albums/" + it.ID)
    r.NoError(err)
    defer resp.Body.Close()
    r.Equal(http.StatusOK, resp.StatusCode)

    var got map[string]any
    r.NoError(json.NewDecoder(resp.Body).Decode(&got))
    r.Equal(it.ID, got["id"])
}

func TestGetAlbumCrossOwnerReturns404(t *testing.T) {
    r := require.New(t)
    fx := newAlbumsAPIFixture(t)
    other := owners.Principal{Hub: "h", UserID: "o"}
    _, err := fx.rw.ExecContext(context.Background(),
        `INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
        other.Hub, other.UserID, "sk-o", time.Now().UTC(),
    )
    r.NoError(err)
    otherIt, err := fx.svc.Create(context.Background(), other, "Theirs")
    r.NoError(err)

    resp, err := http.Get(fx.srv.URL + "/api/v1/albums/" + otherIt.ID)
    r.NoError(err)
    defer resp.Body.Close()
    r.Equal(http.StatusNotFound, resp.StatusCode,
        "cross-owner must be 404, not 403")
}

func TestRenameAlbumReturnsUpdatedDTO(t *testing.T) {
    r := require.New(t)
    fx := newAlbumsAPIFixture(t)
    it, err := fx.svc.Create(context.Background(), fx.owner, "Old")
    r.NoError(err)

    body, _ := json.Marshal(map[string]string{"name": "New"})
    req, err := http.NewRequest(http.MethodPatch, fx.srv.URL+"/api/v1/albums/"+it.ID, bytes.NewReader(body))
    r.NoError(err)
    req.Header.Set("Content-Type", "application/json")
    resp, err := http.DefaultClient.Do(req)
    r.NoError(err)
    defer resp.Body.Close()
    r.Equal(http.StatusOK, resp.StatusCode)

    var got map[string]any
    r.NoError(json.NewDecoder(resp.Body).Decode(&got))
    r.Equal("New", got["name"])
}

func TestDeleteAlbumReturns204(t *testing.T) {
    r := require.New(t)
    fx := newAlbumsAPIFixture(t)
    it, err := fx.svc.Create(context.Background(), fx.owner, "Trip")
    r.NoError(err)

    req, err := http.NewRequest(http.MethodDelete, fx.srv.URL+"/api/v1/albums/"+it.ID, nil)
    r.NoError(err)
    resp, err := http.DefaultClient.Do(req)
    r.NoError(err)
    defer resp.Body.Close()
    r.Equal(http.StatusNoContent, resp.StatusCode)
}

func TestListAlbumsIsolatesOwner(t *testing.T) {
    r := require.New(t)
    fx := newAlbumsAPIFixture(t)
    other := owners.Principal{Hub: "h", UserID: "o"}
    _, err := fx.rw.ExecContext(context.Background(),
        `INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
        other.Hub, other.UserID, "sk-o", time.Now().UTC(),
    )
    r.NoError(err)
    _, err = fx.svc.Create(context.Background(), fx.owner, "Mine")
    r.NoError(err)
    _, err = fx.svc.Create(context.Background(), other, "Theirs")
    r.NoError(err)

    resp, err := http.Get(fx.srv.URL + "/api/v1/albums")
    r.NoError(err)
    defer resp.Body.Close()
    r.Equal(http.StatusOK, resp.StatusCode)

    var got struct {
        Items []map[string]any `json:"items"`
    }
    r.NoError(json.NewDecoder(resp.Body).Decode(&got))
    r.Len(got.Items, 1)
    r.Equal("Mine", got.Items[0]["name"])
}
```

- [ ] **Step 7: Run the albums HTTP tests**

Run: `go test ./internal/httpapi/... -v -run "TestCreateAlbum|TestGetAlbum|TestRenameAlbum|TestDeleteAlbum|TestListAlbums|TestTranslateAlbumError"`
Expected: all PASS.

- [ ] **Step 8: Regenerate openapi.json (prek hook does this automatically on commit)**

Run: `make api-generate`
Expected: `openapi.json` updated with the new album operations.

- [ ] **Step 9: Commit**

```bash
git add internal/httpapi/albums.go internal/httpapi/albums_test.go internal/httpapi/api.go openapi.json
git commit -m "Add album CRUD HTTP routes and translateAlbumError"
```

---

### Task 9: HTTP album_media routes

**Files:**
- Modify: `internal/httpapi/albums.go`
- Modify: `internal/httpapi/albums_test.go`

- [ ] **Step 1: Replace the registerAlbumMedia placeholder**

In `internal/httpapi/albums.go`, replace the `registerAlbumMedia` placeholder with:

```go
type listAlbumMediaInput struct {
    AlbumID string `path:"id"`
    Limit   int    `query:"limit" doc:"max rows to return (default 100, cap 1000)"`
    Offset  int    `query:"offset" doc:"pagination offset"`
    SortBy  string `query:"sort_by" doc:"added (default) or imported"`
    SortAsc bool   `query:"sort_asc" doc:"invert the default DESC sort when true"`
}

type listAlbumMediaOutput struct {
    Body struct {
        Items      []mediaDTO `json:"items"`
        NextOffset *int       `json:"next_offset,omitempty"`
    }
}

type addAlbumMediaInput struct {
    AlbumID string `path:"id"`
    Body    struct {
        MediaIDs []string `json:"media_ids"`
    }
}

type addAlbumMediaOutput struct {
    Body struct {
        Added          int `json:"added"`
        AlreadyPresent int `json:"already_present"`
    }
}

type removeAlbumMediaInput struct {
    AlbumID string `path:"id"`
    MediaID string `path:"media_id"`
}

type removeAlbumMediaOutput struct {
    Status int
}

func registerAlbumMedia(api huma.API, svc *service.AlbumService) {
    huma.Register(api, huma.Operation{
        OperationID: "list-album-media",
        Method:      http.MethodGet,
        Path:        "/api/v1/albums/{id}/media",
        Summary:     "List media in an album",
    }, func(ctx context.Context, in *listAlbumMediaInput) (*listAlbumMediaOutput, error) {
        if svc == nil {
            return nil, huma.Error503ServiceUnavailable("album service not configured")
        }
        id, ok := IdentityFromContext(ctx)
        if !ok {
            return nil, huma.Error401Unauthorized(errs.ErrIdentityMissing.Error())
        }
        limit := clampLimit(in.Limit, albumsListDefaultLimit, albumsListMaxLimit)
        offset := max(in.Offset, 0)
        filter := album.AlbumMediaFilter{
            Limit:   limit + 1,
            Offset:  offset,
            SortBy:  in.SortBy,
            SortAsc: in.SortAsc,
        }
        rows, err := svc.ListMedia(ctx, in.AlbumID, filter, id.Principal.OwnersPrincipal())
        if err != nil {
            return nil, translateAlbumError(err)
        }
        out := &listAlbumMediaOutput{}
        hasMore := len(rows) > limit
        if hasMore {
            rows = rows[:limit]
            next := offset + limit
            out.Body.NextOffset = &next
        }
        out.Body.Items = make([]mediaDTO, 0, len(rows))
        for _, m := range rows {
            out.Body.Items = append(out.Body.Items, toMediaDTO(m))
        }
        return out, nil
    })

    huma.Register(api, huma.Operation{
        OperationID: "add-album-media",
        Method:      http.MethodPost,
        Path:        "/api/v1/albums/{id}/media",
        Summary:     "Add media to an album (idempotent, deduped)",
    }, func(ctx context.Context, in *addAlbumMediaInput) (*addAlbumMediaOutput, error) {
        if svc == nil {
            return nil, huma.Error503ServiceUnavailable("album service not configured")
        }
        id, ok := IdentityFromContext(ctx)
        if !ok {
            return nil, huma.Error401Unauthorized(errs.ErrIdentityMissing.Error())
        }
        added, already, err := svc.AddMedia(ctx, in.AlbumID, in.Body.MediaIDs, id.Principal.OwnersPrincipal())
        if err != nil {
            return nil, translateAlbumError(err)
        }
        out := &addAlbumMediaOutput{}
        out.Body.Added = added
        out.Body.AlreadyPresent = already
        return out, nil
    })

    huma.Register(api, huma.Operation{
        OperationID:   "remove-album-media",
        Method:        http.MethodDelete,
        Path:          "/api/v1/albums/{id}/media/{media_id}",
        Summary:       "Remove a media row from an album",
        DefaultStatus: http.StatusNoContent,
    }, func(ctx context.Context, in *removeAlbumMediaInput) (*removeAlbumMediaOutput, error) {
        if svc == nil {
            return nil, huma.Error503ServiceUnavailable("album service not configured")
        }
        id, ok := IdentityFromContext(ctx)
        if !ok {
            return nil, huma.Error401Unauthorized(errs.ErrIdentityMissing.Error())
        }
        if err := svc.RemoveMedia(ctx, in.AlbumID, in.MediaID, id.Principal.OwnersPrincipal()); err != nil {
            return nil, translateAlbumError(err)
        }
        return &removeAlbumMediaOutput{Status: http.StatusNoContent}, nil
    })
}
```

- [ ] **Step 2: Add album_media HTTP tests**

Append to `internal/httpapi/albums_test.go`:

```go
// seedMediaRowAPI inserts a minimal media row directly. Colocated with
// the HTTP tests so the fixture doesn't depend on media.Repo's full
// insert surface.
func seedMediaRowAPI(t *testing.T, rw *sql.DB, p owners.Principal, id, checksum string) {
    t.Helper()
    _, err := rw.ExecContext(context.Background(), `
INSERT INTO media (
    id, owner_hub, owner_user_id, media_type, mime_type, path, original_filename,
    imported_at, timestamp, size, checksum,
    make, model, focal_length, shutter, width, height, iso, aperture,
    duration_ms,
    thumb_status, thumb_version, thumb_updated_at
) VALUES (?, ?, ?, 'photo', 'image/jpeg', ?, NULL, ?, NULL, 0, ?,
          NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL,
          'ready', 1, NULL)`,
        id, p.Hub, p.UserID, "p/"+id, time.Now().UTC(), checksum,
    )
    require.NoError(t, err)
}

func TestAddAlbumMediaBatchResponseShape(t *testing.T) {
    r := require.New(t)
    fx := newAlbumsAPIFixture(t)
    it, err := fx.svc.Create(context.Background(), fx.owner, "Trip")
    r.NoError(err)
    m1 := "m-1-" + it.ID
    m2 := "m-2-" + it.ID
    seedMediaRowAPI(t, fx.rw, fx.owner, m1, "cs-1")
    seedMediaRowAPI(t, fx.rw, fx.owner, m2, "cs-2")

    body, _ := json.Marshal(map[string]any{"media_ids": []string{m1, m2}})
    resp, err := http.Post(fx.srv.URL+"/api/v1/albums/"+it.ID+"/media",
        "application/json", bytes.NewReader(body))
    r.NoError(err)
    defer resp.Body.Close()
    r.Equal(http.StatusOK, resp.StatusCode)

    var got struct {
        Added          int `json:"added"`
        AlreadyPresent int `json:"already_present"`
    }
    r.NoError(json.NewDecoder(resp.Body).Decode(&got))
    r.Equal(2, got.Added)
    r.Equal(0, got.AlreadyPresent)
}

func TestAddAlbumMediaDuplicateInputCollapses(t *testing.T) {
    r := require.New(t)
    fx := newAlbumsAPIFixture(t)
    it, err := fx.svc.Create(context.Background(), fx.owner, "Trip")
    r.NoError(err)
    m1 := "dup-1-" + it.ID
    m2 := "dup-2-" + it.ID
    seedMediaRowAPI(t, fx.rw, fx.owner, m1, "cs-1")
    seedMediaRowAPI(t, fx.rw, fx.owner, m2, "cs-2")

    // [m1, m1, m2] dedupes to [m1, m2] → added=2, already_present=0.
    body, _ := json.Marshal(map[string]any{"media_ids": []string{m1, m1, m2}})
    resp, err := http.Post(fx.srv.URL+"/api/v1/albums/"+it.ID+"/media",
        "application/json", bytes.NewReader(body))
    r.NoError(err)
    defer resp.Body.Close()
    r.Equal(http.StatusOK, resp.StatusCode)

    var got struct {
        Added          int `json:"added"`
        AlreadyPresent int `json:"already_present"`
    }
    r.NoError(json.NewDecoder(resp.Body).Decode(&got))
    r.Equal(2, got.Added)
    r.Equal(0, got.AlreadyPresent)
}

func TestAddAlbumMediaCrossOwnerReturns404NotFound(t *testing.T) {
    // Plan D rule: the batch endpoint must not be an existence oracle —
    // a media_id the caller does not own returns 404, not 403.
    r := require.New(t)
    fx := newAlbumsAPIFixture(t)
    it, err := fx.svc.Create(context.Background(), fx.owner, "Trip")
    r.NoError(err)
    other := owners.Principal{Hub: "h", UserID: "other"}
    _, err = fx.rw.ExecContext(context.Background(),
        `INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
        other.Hub, other.UserID, "sk-o", time.Now().UTC(),
    )
    r.NoError(err)
    theirMedia := "foreign-" + it.ID
    seedMediaRowAPI(t, fx.rw, other, theirMedia, "cs-o")

    body, _ := json.Marshal(map[string]any{"media_ids": []string{theirMedia}})
    resp, err := http.Post(fx.srv.URL+"/api/v1/albums/"+it.ID+"/media",
        "application/json", bytes.NewReader(body))
    r.NoError(err)
    defer resp.Body.Close()
    r.Equal(http.StatusNotFound, resp.StatusCode,
        "cross-owner media must be 404, not 403")
}

func TestAddAlbumMediaEmptyBatchReturns400(t *testing.T) {
    r := require.New(t)
    fx := newAlbumsAPIFixture(t)
    it, err := fx.svc.Create(context.Background(), fx.owner, "Trip")
    r.NoError(err)

    body, _ := json.Marshal(map[string]any{"media_ids": []string{}})
    resp, err := http.Post(fx.srv.URL+"/api/v1/albums/"+it.ID+"/media",
        "application/json", bytes.NewReader(body))
    r.NoError(err)
    defer resp.Body.Close()
    r.Equal(http.StatusBadRequest, resp.StatusCode)
}

func TestRemoveAlbumMediaNotInAlbumReturns404(t *testing.T) {
    r := require.New(t)
    fx := newAlbumsAPIFixture(t)
    it, err := fx.svc.Create(context.Background(), fx.owner, "Trip")
    r.NoError(err)

    req, err := http.NewRequest(http.MethodDelete,
        fx.srv.URL+"/api/v1/albums/"+it.ID+"/media/nonesuch", nil)
    r.NoError(err)
    resp, err := http.DefaultClient.Do(req)
    r.NoError(err)
    defer resp.Body.Close()
    r.Equal(http.StatusNotFound, resp.StatusCode)
}

func TestListAlbumMediaPagination(t *testing.T) {
    r := require.New(t)
    fx := newAlbumsAPIFixture(t)
    it, err := fx.svc.Create(context.Background(), fx.owner, "Trip")
    r.NoError(err)

    ids := []string{"pg-a-" + it.ID, "pg-b-" + it.ID, "pg-c-" + it.ID}
    for _, id := range ids {
        seedMediaRowAPI(t, fx.rw, fx.owner, id, "cs-"+id)
    }
    _, _, err = fx.svc.AddMedia(context.Background(), it.ID, ids, fx.owner)
    r.NoError(err)

    // Page 1: limit=2 → 2 rows + next_offset=2.
    resp, err := http.Get(fx.srv.URL + "/api/v1/albums/" + it.ID + "/media?limit=2")
    r.NoError(err)
    defer resp.Body.Close()
    r.Equal(http.StatusOK, resp.StatusCode)
    var page1 struct {
        Items      []map[string]any `json:"items"`
        NextOffset *int             `json:"next_offset"`
    }
    r.NoError(json.NewDecoder(resp.Body).Decode(&page1))
    r.Len(page1.Items, 2)
    r.NotNil(page1.NextOffset)
    r.Equal(2, *page1.NextOffset)

    // Page 2: offset=2&limit=2 → 1 row and no next_offset.
    resp2, err := http.Get(fx.srv.URL + "/api/v1/albums/" + it.ID + "/media?limit=2&offset=2")
    r.NoError(err)
    defer resp2.Body.Close()
    var page2 struct {
        Items      []map[string]any `json:"items"`
        NextOffset *int             `json:"next_offset"`
    }
    r.NoError(json.NewDecoder(resp2.Body).Decode(&page2))
    r.Len(page2.Items, 1)
    r.Nil(page2.NextOffset)
}
```

- [ ] **Step 3: Run the album_media HTTP tests**

Run: `go test ./internal/httpapi/... -v -run "TestAddAlbumMedia|TestRemoveAlbumMedia|TestListAlbumMedia"`
Expected: all PASS.

- [ ] **Step 4: Regenerate openapi.json**

Run: `make api-generate`

- [ ] **Step 5: Commit**

```bash
git add internal/httpapi/albums.go internal/httpapi/albums_test.go openapi.json
git commit -m "Add album_media HTTP routes"
```

---

### Task 10: CLI albums subcommand tree

**Files:**
- Create: `internal/cli/albums.go`
- Create: `internal/cli/albums_test.go`
- Modify: `internal/cli/root.go`

- [ ] **Step 1: Write the CLI scaffolding and loader**

Create `internal/cli/albums.go`:

```go
package cli

import (
    "context"
    "encoding/json"
    "fmt"
    "io"
    "os"
    "path/filepath"
    "text/tabwriter"

    "github.com/spf13/cobra"

    "github.com/wesm/fotobank/internal/album"
    "github.com/wesm/fotobank/internal/config"
    "github.com/wesm/fotobank/internal/db"
    "github.com/wesm/fotobank/internal/media"
    "github.com/wesm/fotobank/internal/owners"
    "github.com/wesm/fotobank/internal/service"
)

// loadAlbumCtx loads the CLI's configuration, opens the DB (respecting
// FOTOBANK_DB_PATH), and constructs an AlbumService plus the caller
// Principal. Enforces stub-mode identity like `thumbs regenerate`
// because we need a single well-defined caller for operator tooling.
type albumCtx struct {
    svc    *service.AlbumService
    caller owners.Principal
    close  func()
}

func loadAlbumCtx(cfgPath string) (*albumCtx, error) {
    path := cfgPath
    if path == "" {
        path = config.DefaultConfigPath()
    }
    cfg, err := config.Load(path)
    if err != nil {
        return nil, err
    }
    if cfg.Identity.Mode != "stub" {
        return nil, fmt.Errorf(
            "fotobank albums requires identity.mode = stub (got %q)",
            cfg.Identity.Mode)
    }
    dbPath := os.Getenv("FOTOBANK_DB_PATH")
    if dbPath == "" {
        dbPath = filepath.Join(cfg.Flash.Root, "fotobank.sqlite")
    }
    d, err := db.Open(dbPath)
    if err != nil {
        return nil, err
    }
    return &albumCtx{
        svc:    service.NewAlbumService(album.NewRepo(d.WriteDB(), d.ReadDB()), media.NewRepo(d.WriteDB(), d.ReadDB())),
        caller: owners.Principal{Hub: cfg.Identity.Stub.Hub, UserID: cfg.Identity.Stub.UserID},
        close:  func() { _ = d.Close() },
    }, nil
}

func newAlbumsCmd() *cobra.Command {
    cmd := &cobra.Command{
        Use:   "albums",
        Short: "Manage albums (create, rename, delete, add/remove media)",
        Args:  usageArgs(cobra.NoArgs),
        RunE: func(cmd *cobra.Command, _ []string) error {
            _ = cmd.Usage()
            return newUsageError("a subcommand is required")
        },
    }
    cmd.AddCommand(newAlbumsCreateCmd())
    cmd.AddCommand(newAlbumsRenameCmd())
    cmd.AddCommand(newAlbumsDeleteCmd())
    cmd.AddCommand(newAlbumsListCmd())
    cmd.AddCommand(newAlbumsShowCmd())
    cmd.AddCommand(newAlbumsAddCmd())
    cmd.AddCommand(newAlbumsRemoveCmd())
    return cmd
}
```

- [ ] **Step 2: Append create / rename / delete subcommands**

```go
func newAlbumsCreateCmd() *cobra.Command {
    var cfgPath string
    cmd := &cobra.Command{
        Use:   "create <name>",
        Short: "Create a new album",
        Args:  usageArgs(cobra.ExactArgs(1)),
        RunE: func(cmd *cobra.Command, args []string) error {
            return runAlbumsCreate(cmd.Context(), cfgPath, args[0], cmd.OutOrStdout())
        },
    }
    cmd.Flags().StringVar(&cfgPath, "config", "", "path to config file (defaults to DefaultConfigPath)")
    return cmd
}

func runAlbumsCreate(ctx context.Context, cfgPath, name string, stdout io.Writer) error {
    ac, err := loadAlbumCtx(cfgPath)
    if err != nil {
        return err
    }
    defer ac.close()
    it, err := ac.svc.Create(ctx, ac.caller, name)
    if err != nil {
        return err
    }
    fmt.Fprintf(stdout, "%s\t%s\n", it.ID, it.Name)
    return nil
}

func newAlbumsRenameCmd() *cobra.Command {
    var cfgPath string
    cmd := &cobra.Command{
        Use:   "rename <id> <name>",
        Short: "Rename an album",
        Args:  usageArgs(cobra.ExactArgs(2)),
        RunE: func(cmd *cobra.Command, args []string) error {
            ac, err := loadAlbumCtx(cfgPath)
            if err != nil {
                return err
            }
            defer ac.close()
            it, err := ac.svc.Rename(cmd.Context(), args[0], args[1], ac.caller)
            if err != nil {
                return err
            }
            fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\n", it.ID, it.Name)
            return nil
        },
    }
    cmd.Flags().StringVar(&cfgPath, "config", "", "path to config file (defaults to DefaultConfigPath)")
    return cmd
}

func newAlbumsDeleteCmd() *cobra.Command {
    var cfgPath string
    cmd := &cobra.Command{
        Use:   "delete <id>",
        Short: "Delete an album (cascades album_media)",
        Args:  usageArgs(cobra.ExactArgs(1)),
        RunE: func(cmd *cobra.Command, args []string) error {
            ac, err := loadAlbumCtx(cfgPath)
            if err != nil {
                return err
            }
            defer ac.close()
            if err := ac.svc.Delete(cmd.Context(), args[0], ac.caller); err != nil {
                return err
            }
            fmt.Fprintln(cmd.OutOrStdout(), "deleted", args[0])
            return nil
        },
    }
    cmd.Flags().StringVar(&cfgPath, "config", "", "path to config file (defaults to DefaultConfigPath)")
    return cmd
}
```

- [ ] **Step 3: Append list / show subcommands**

```go
func newAlbumsListCmd() *cobra.Command {
    var (
        cfgPath string
        limit   int
        offset  int
        asJSON  bool
    )
    cmd := &cobra.Command{
        Use:   "list",
        Short: "List albums belonging to the caller",
        Args:  usageArgs(cobra.NoArgs),
        RunE: func(cmd *cobra.Command, _ []string) error {
            ac, err := loadAlbumCtx(cfgPath)
            if err != nil {
                return err
            }
            defer ac.close()
            items, err := ac.svc.List(cmd.Context(), ac.caller, limit, offset)
            if err != nil {
                return err
            }
            if asJSON {
                return json.NewEncoder(cmd.OutOrStdout()).Encode(items)
            }
            tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
            fmt.Fprintln(tw, "UUID\tName\tItems\tUpdated")
            for _, it := range items {
                fmt.Fprintf(tw, "%s\t%s\t%d\t%s\n", it.ID, it.Name, it.ItemCount, it.UpdatedAt.Format("2006-01-02 15:04:05"))
            }
            return tw.Flush()
        },
    }
    cmd.Flags().StringVar(&cfgPath, "config", "", "path to config file (defaults to DefaultConfigPath)")
    cmd.Flags().IntVar(&limit, "limit", 100, "max rows to return")
    cmd.Flags().IntVar(&offset, "offset", 0, "pagination offset")
    cmd.Flags().BoolVar(&asJSON, "json", false, "emit rows as JSON")
    return cmd
}

func newAlbumsShowCmd() *cobra.Command {
    var (
        cfgPath string
        limit   int
        offset  int
        sortBy  string
        sortAsc bool
    )
    cmd := &cobra.Command{
        Use:   "show <id>",
        Short: "Show album detail + a page of member media IDs",
        Args:  usageArgs(cobra.ExactArgs(1)),
        RunE: func(cmd *cobra.Command, args []string) error {
            ac, err := loadAlbumCtx(cfgPath)
            if err != nil {
                return err
            }
            defer ac.close()
            detail, err := ac.svc.GetDetail(cmd.Context(), args[0], ac.caller)
            if err != nil {
                return err
            }
            out := cmd.OutOrStdout()
            fmt.Fprintf(out, "id:         %s\n", detail.ID)
            fmt.Fprintf(out, "name:       %s\n", detail.Name)
            fmt.Fprintf(out, "items:      %d\n", detail.ItemCount)
            fmt.Fprintf(out, "updated_at: %s\n", detail.UpdatedAt.Format("2006-01-02 15:04:05"))
            if detail.Cover != nil {
                fmt.Fprintf(out, "cover:      media=%s version=%d\n", detail.Cover.MediaID, detail.Cover.ThumbVersion)
            }
            rows, err := ac.svc.ListMedia(cmd.Context(), args[0], album.AlbumMediaFilter{
                Limit: limit, Offset: offset, SortBy: sortBy, SortAsc: sortAsc,
            }, ac.caller)
            if err != nil {
                return err
            }
            fmt.Fprintln(out, "members:")
            for _, m := range rows {
                fmt.Fprintln(out, "  ", m.ID)
            }
            return nil
        },
    }
    cmd.Flags().StringVar(&cfgPath, "config", "", "path to config file (defaults to DefaultConfigPath)")
    cmd.Flags().IntVar(&limit, "limit", 100, "max rows to return")
    cmd.Flags().IntVar(&offset, "offset", 0, "pagination offset")
    cmd.Flags().StringVar(&sortBy, "sort-by", "added", "added or imported")
    cmd.Flags().BoolVar(&sortAsc, "sort-asc", false, "invert the default DESC sort")
    return cmd
}
```

- [ ] **Step 4: Append add / remove subcommands**

```go
func newAlbumsAddCmd() *cobra.Command {
    var cfgPath string
    cmd := &cobra.Command{
        Use:   "add <album_id> <media_id> [<media_id>...]",
        Short: "Add one or more media rows to an album (idempotent)",
        Args:  usageArgs(cobra.MinimumNArgs(2)),
        RunE: func(cmd *cobra.Command, args []string) error {
            ac, err := loadAlbumCtx(cfgPath)
            if err != nil {
                return err
            }
            defer ac.close()
            added, already, err := ac.svc.AddMedia(cmd.Context(), args[0], args[1:], ac.caller)
            if err != nil {
                return err
            }
            fmt.Fprintf(cmd.OutOrStdout(), "added %d, already present %d\n", added, already)
            return nil
        },
    }
    cmd.Flags().StringVar(&cfgPath, "config", "", "path to config file (defaults to DefaultConfigPath)")
    return cmd
}

func newAlbumsRemoveCmd() *cobra.Command {
    var cfgPath string
    cmd := &cobra.Command{
        Use:   "remove <album_id> <media_id>",
        Short: "Remove a media row from an album",
        Args:  usageArgs(cobra.ExactArgs(2)),
        RunE: func(cmd *cobra.Command, args []string) error {
            ac, err := loadAlbumCtx(cfgPath)
            if err != nil {
                return err
            }
            defer ac.close()
            if err := ac.svc.RemoveMedia(cmd.Context(), args[0], args[1], ac.caller); err != nil {
                return err
            }
            fmt.Fprintln(cmd.OutOrStdout(), "removed", args[1])
            return nil
        },
    }
    cmd.Flags().StringVar(&cfgPath, "config", "", "path to config file (defaults to DefaultConfigPath)")
    return cmd
}
```

- [ ] **Step 5: Wire the subcommand into the root**

Edit `internal/cli/root.go`. Locate the existing `root.AddCommand(newThumbsCmd())` call and add one line below:

```go
    root.AddCommand(newAlbumsCmd())
```

- [ ] **Step 6: Run `go build ./...`**

Run: `go build ./...`
Expected: exit 0.

- [ ] **Step 7: Write CLI happy-path tests**

Create `internal/cli/albums_test.go`:

```go
package cli_test

import (
    "bytes"
    "context"
    "fmt"
    "os"
    "path/filepath"
    "strings"
    "testing"

    "github.com/stretchr/testify/require"

    "github.com/wesm/fotobank/internal/cli"
)

// writeStubConfig builds a minimal stub-mode config (matching the
// style of writeBasicConfig in thumbs_test.go) and returns the config
// path plus the DB path. The caller sets FOTOBANK_CONFIG and
// FOTOBANK_DB_PATH env vars after calling this.
func writeStubConfig(t *testing.T) (cfgPath, dbPath string) {
    t.Helper()
    tmp := t.TempDir()
    flashRoot := filepath.Join(tmp, "flash")
    nasRoot := filepath.Join(tmp, "nas")
    require.NoError(t, os.MkdirAll(flashRoot, 0o700))
    require.NoError(t, os.MkdirAll(nasRoot, 0o700))
    cfgPath = filepath.Join(tmp, "c.toml")
    require.NoError(t, os.WriteFile(cfgPath, fmt.Appendf(nil, `
[nas]
root = %q
[flash]
root = %q
[identity]
mode = "stub"
[identity.stub]
hub = "h"
user_id = "u"
`, nasRoot, flashRoot), 0o600))
    dbPath = filepath.Join(flashRoot, "fotobank.sqlite")
    return cfgPath, dbPath
}

// runCLI invokes cli.Run with args and returns stdout, stderr, exit code.
func runCLI(ctx context.Context, args ...string) (string, string, int) {
    var stdout, stderr bytes.Buffer
    code := cli.RunContext(ctx, args, &stdout, &stderr)
    return stdout.String(), stderr.String(), code
}

// bootstrapDB runs `fotobank server` briefly so the migrations apply
// and the stub owner is inserted. We don't actually need the server to
// stay up — we only need the side effects of identity stub registration
// (which happens during CLI init before Serve). Instead, the simpler
// route is to call `fotobank owners add` which inserts the row.
//
// Bootstrap by running `fotobank owners add` — this opens the DB,
// applies migrations, and inserts an owner row.
func bootstrapOwner(t *testing.T) {
    t.Helper()
    // The first CLI invocation opens the DB through clictx, which
    // triggers db.Open → runMigrations. Use owners add because albums
    // rows FK into owners; the hub/user-id here must match what
    // writeStubConfig wrote.
    _, _, code := runCLI(context.Background(),
        "owners", "add",
        "--hub", "h", "--user-id", "u",
        "--storage-key", "sk",
    )
    require.Equal(t, 0, code, "bootstrap owner must succeed")
}

func TestCLIAlbumsCreateHappyPath(t *testing.T) {
    r := require.New(t)
    cfgPath, dbPath := writeStubConfig(t)
    t.Setenv("FOTOBANK_CONFIG", cfgPath)
    t.Setenv("FOTOBANK_DB_PATH", dbPath)

    bootstrapOwner(t)

    stdout, stderr, code := runCLI(context.Background(), "albums", "create", "Trip")
    r.Equal(0, code, "stderr=%s", stderr)
    r.NotEmpty(stdout)
    r.Contains(stdout, "Trip")
}

func TestCLIAlbumsListShowsCreatedAlbum(t *testing.T) {
    r := require.New(t)
    cfgPath, dbPath := writeStubConfig(t)
    t.Setenv("FOTOBANK_CONFIG", cfgPath)
    t.Setenv("FOTOBANK_DB_PATH", dbPath)

    bootstrapOwner(t)
    _, _, code := runCLI(context.Background(), "albums", "create", "Trip")
    r.Equal(0, code)

    stdout, _, code := runCLI(context.Background(), "albums", "list")
    r.Equal(0, code)
    r.Contains(stdout, "Trip")
}

func TestCLIAlbumsRenameUpdatesList(t *testing.T) {
    r := require.New(t)
    cfgPath, dbPath := writeStubConfig(t)
    t.Setenv("FOTOBANK_CONFIG", cfgPath)
    t.Setenv("FOTOBANK_DB_PATH", dbPath)
    bootstrapOwner(t)

    stdout, _, code := runCLI(context.Background(), "albums", "create", "Old")
    r.Equal(0, code)
    // The create output is "<id>\t<name>\n"; take the first field.
    id := strings.Fields(stdout)[0]

    _, _, code = runCLI(context.Background(), "albums", "rename", id, "New")
    r.Equal(0, code)

    stdout, _, _ = runCLI(context.Background(), "albums", "list")
    r.Contains(stdout, "New")
    r.NotContains(stdout, "Old")
}

func TestCLIAlbumsDeleteRemovesFromList(t *testing.T) {
    r := require.New(t)
    cfgPath, dbPath := writeStubConfig(t)
    t.Setenv("FOTOBANK_CONFIG", cfgPath)
    t.Setenv("FOTOBANK_DB_PATH", dbPath)
    bootstrapOwner(t)

    stdout, _, _ := runCLI(context.Background(), "albums", "create", "Trip")
    id := strings.Fields(stdout)[0]

    _, _, code := runCLI(context.Background(), "albums", "delete", id)
    r.Equal(0, code)

    stdout, _, _ = runCLI(context.Background(), "albums", "list")
    r.NotContains(stdout, id)
}

func TestCLIAlbumsRejectsNonStubIdentity(t *testing.T) {
    // Swap the stub config for a header-mode config and verify the
    // albums CLI refuses to operate. Header mode validates fine against
    // a loopback listen_address, so config.Load succeeds and the
    // stub-mode guard in loadAlbumCtx is what surfaces the error.
    r := require.New(t)
    tmp := t.TempDir()
    flashRoot := filepath.Join(tmp, "flash")
    nasRoot := filepath.Join(tmp, "nas")
    r.NoError(os.MkdirAll(flashRoot, 0o700))
    r.NoError(os.MkdirAll(nasRoot, 0o700))
    cfgPath := filepath.Join(tmp, "c.toml")
    r.NoError(os.WriteFile(cfgPath, fmt.Appendf(nil, `
[nas]
root = %q
[flash]
root = %q
[identity]
mode = "header"
[http]
listen_address = "127.0.0.1:9999"
`, nasRoot, flashRoot), 0o600))
    t.Setenv("FOTOBANK_CONFIG", cfgPath)
    t.Setenv("FOTOBANK_DB_PATH", filepath.Join(flashRoot, "fotobank.sqlite"))

    _, stderr, code := runCLI(context.Background(), "albums", "create", "Trip")
    r.Equal(1, code)
    r.Contains(stderr, "identity.mode = stub")
}
```

- [ ] **Step 8: Run the CLI tests**

Run: `go test ./internal/cli/... -run TestCLIAlbums -v`
Expected: all PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/cli/albums.go internal/cli/albums_test.go internal/cli/root.go
git commit -m "Add fotobank albums CLI subcommand tree"
```

---

### Task 11: Server wiring + E2E extension

**Files:**
- Modify: `internal/cli/server.go`
- Modify: `internal/cli/e2e_media_test.go`

- [ ] **Step 1: Wire AlbumService into the running server**

Edit `internal/cli/server.go`. Locate the block that constructs `mediaSvc` (around line 118) and add right after it:

```go
    albumSvc := service.NewAlbumService(
        album.NewRepo(d.WriteDB(), d.ReadDB()),
        media.NewRepo(d.WriteDB(), d.ReadDB()),
    )
```

Locate the `httpapi.New(httpapi.Deps{...})` call and add the field:

```go
    handler, err := httpapi.New(httpapi.Deps{
        IdentityProvider: idp,
        OwnerService:     ownerSvc,
        MediaService:     mediaSvc,
        ThumbService:     thumbSvc,
        AlbumService:     albumSvc,
    })
```

Add the import `"github.com/wesm/fotobank/internal/album"` at the top.

- [ ] **Step 2: Verify the build**

Run: `go build ./...`
Expected: exit 0.

- [ ] **Step 3: Append the E2E round-trip to e2e_media_test.go**

Read `internal/cli/e2e_media_test.go` carefully. Note that the existing test populates a local variable called `photos` (a slice of `photoRow` structs with `ID` and `Version` fields) after asserting `r.Len(photos, 2, "expected two photo rows")`. The variables `ctx`, `addr`, and `r` are in scope throughout.

Just before `TestE2EMediaPipeline`'s final assertions on shutdown (look for the block that cancels `ctx` and reads `serveErr`), insert an albums round-trip. Place the imports `bytes` and `net/http` at the top of the file if not already imported.

```go
    // --- Albums round-trip ---------------------------------------------------
    // Create an album, add both photo rows, list albums and assert cover
    // is populated, then delete the album and list again.
    base := "http://" + addr
    cBody, _ := json.Marshal(map[string]string{"name": "E2E Trip"})
    cReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
        base+"/api/v1/albums", bytes.NewReader(cBody))
    r.NoError(err)
    cReq.Header.Set("Content-Type", "application/json")
    cResp, err := http.DefaultClient.Do(cReq)
    r.NoError(err)
    var created struct {
        ID string `json:"id"`
    }
    r.NoError(json.NewDecoder(cResp.Body).Decode(&created))
    cResp.Body.Close()
    r.Equal(http.StatusCreated, cResp.StatusCode)
    r.NotEmpty(created.ID)

    // photos was populated earlier in this test when the two imported
    // photo rows were listed via /api/v1/media. Build the add-media
    // request from it.
    photoIDs := make([]string, 0, len(photos))
    for _, p := range photos {
        photoIDs = append(photoIDs, p.ID)
    }
    addBody, _ := json.Marshal(map[string]any{"media_ids": photoIDs})
    addReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
        base+"/api/v1/albums/"+created.ID+"/media", bytes.NewReader(addBody))
    r.NoError(err)
    addReq.Header.Set("Content-Type", "application/json")
    addResp, err := http.DefaultClient.Do(addReq)
    r.NoError(err)
    var addOut struct {
        Added          int `json:"added"`
        AlreadyPresent int `json:"already_present"`
    }
    r.NoError(json.NewDecoder(addResp.Body).Decode(&addOut))
    addResp.Body.Close()
    r.Equal(http.StatusOK, addResp.StatusCode)
    r.Equal(len(photoIDs), addOut.Added)

    // By the time this code runs, earlier sections of the test have
    // already waited for thumbs to reach 'ready' for both photos, so
    // /api/v1/albums should return a non-nil cover on the first call.
    // Keep a short bounded poll to tolerate one extra scheduling hop.
    var sawCover bool
    for i := 0; i < 20; i++ {
        listReq, err := http.NewRequestWithContext(ctx, http.MethodGet,
            base+"/api/v1/albums", nil)
        r.NoError(err)
        listResp, err := http.DefaultClient.Do(listReq)
        r.NoError(err)
        var listOut struct {
            Items []struct {
                ID    string `json:"id"`
                Cover *struct {
                    MediaID      string `json:"media_id"`
                    ThumbVersion int    `json:"thumb_version"`
                } `json:"cover"`
            } `json:"items"`
        }
        r.NoError(json.NewDecoder(listResp.Body).Decode(&listOut))
        listResp.Body.Close()
        for _, it := range listOut.Items {
            if it.ID == created.ID && it.Cover != nil {
                sawCover = true
                break
            }
        }
        if sawCover {
            break
        }
        time.Sleep(50 * time.Millisecond)
    }
    r.True(sawCover, "cover should appear after thumbs reach 'ready'")

    // Delete the album and confirm it leaves the list.
    delReq, err := http.NewRequestWithContext(ctx, http.MethodDelete,
        base+"/api/v1/albums/"+created.ID, nil)
    r.NoError(err)
    delResp, err := http.DefaultClient.Do(delReq)
    r.NoError(err)
    delResp.Body.Close()
    r.Equal(http.StatusNoContent, delResp.StatusCode)

    listReq2, err := http.NewRequestWithContext(ctx, http.MethodGet,
        base+"/api/v1/albums", nil)
    r.NoError(err)
    listResp2, err := http.DefaultClient.Do(listReq2)
    r.NoError(err)
    var listOut2 struct {
        Items []struct {
            ID string `json:"id"`
        } `json:"items"`
    }
    r.NoError(json.NewDecoder(listResp2.Body).Decode(&listOut2))
    listResp2.Body.Close()
    for _, it := range listOut2.Items {
        r.NotEqual(created.ID, it.ID, "album should be absent after delete")
    }
```

If the existing test uses a slightly different shape for `photos` (e.g. the field names differ), adjust the `photoIDs` loop to read the correct field name but keep the rest of the block unchanged.

- [ ] **Step 4: Run the E2E test**

Run: `go test ./internal/cli/... -run TestE2EMediaPipeline -v -timeout 120s`
Expected: PASS. The test takes around 20–40 seconds because it boots a full server and waits for thumbs to drain.

- [ ] **Step 5: Commit**

```bash
git add internal/cli/server.go internal/cli/e2e_media_test.go
git commit -m "Wire AlbumService into server and extend E2E with album round-trip"
```

---

### Task 12: Final integration check

**Files:** none (verification only).

- [ ] **Step 1: Full test suite with race detector**

Run: `go test -race -shuffle=on ./...`
Expected: all PASS.

- [ ] **Step 2: Lint**

Run: `make lint`
Expected: `golangci-lint run` clean, `testify-helper-check` clean.

- [ ] **Step 3: Nilaway**

Run: `make nilaway`
Expected: no findings on any album/service/httpapi/cli code introduced.

- [ ] **Step 4: OpenAPI regeneration confirms no drift**

Run: `make api-generate && git diff --exit-code openapi.json`
Expected: exit 0. A non-zero exit means the committed `openapi.json` is out of date with the huma routes — regenerate and commit.

- [ ] **Step 5: Manual smoke (optional)**

Run a local server against a scratch config and exercise the CLI:

```bash
./bin/fotobank owners add --hub local --user-id alice --storage-key alice-sk
./bin/fotobank albums create "Smoke Test"
./bin/fotobank albums list
```

- [ ] **Step 6: No stray commits**

Run: `git status`
Expected: clean working tree.

---

## Self-review

### Spec coverage

Every spec section has a corresponding task:
- §1 Goals / non-goals — implicit in the task set; tasks do not introduce sharing, reordering, owner-picked covers, metadata beyond name, smart albums, or nested albums.
- §2 Prior art — Task 1 (migration) and Task 5 (trigger wrap) preserve the existing tables and trigger.
- §3 Architecture — file structure in this plan matches the spec layout.
- §4 Domain types — Task 1 creates `album.go`; Task 8 adds `coverDTO`/`albumDTO`.
- §5 Repo surface — Tasks 2, 3, 4 implement every method; Task 3 uses the exact CTE; Task 4 has the ListMedia plan.
- §5 New indexes — Task 1 creates both up + down files.
- §6 Service surface — Tasks 6 and 7 implement Create/Get/GetDetail/Rename/Delete/List/AddMedia/RemoveMedia/ListMedia.
- §6 Validation rules — Tasks 6 (name) and 7 (batch + sort) enforce them with tests.
- §6 Ownership checks — Task 7 tests `AddMedia` cross-owner → `ErrNotFound` (not `ErrOwnerMismatch`).
- §7 HTTP API surface — Tasks 8 and 9 register every route via huma; all 8 paths match.
- §7 Error mapping + album-specific translator — Task 8 implements and unit-tests it.
- §8 CLI surface — Task 10 implements every subcommand and tests each on the happy path, plus the stub-mode guard.
- §9 Error taxonomy — Task 1 creates the sentinels.
- §10 Testing strategy — Tasks 2–9 cover repo + trigger + service + HTTP tests; Task 10 covers CLI; Task 11 covers E2E.
- §11 Out-of-scope — the plan does not add search, undo, activity feed, etc.
- §12 Open points — none; the all-huma decision is already baked into Tasks 8 and 9.
- §13 Success criteria — Task 12 validates them.

### Placeholder scan

None. Every step that changes code shows the code. The one intentional scaffolding moment (Task 2 Step 2) is replaced in Step 3 with a complete file; that's explicit, and the intermediate state is not committed.

### Type consistency

- `album.AlbumListItem` (plan) / `AlbumListItem` (spec §4) — consistent.
- `album.AlbumMediaFilter` fields `SortBy`/`SortAsc` — used identically in repo, service, HTTP, and CLI.
- `album.ErrInvalidName` / `ErrInvalidBatch` / `ErrInvalidSort` — introduced in Task 1, referenced from Tasks 6, 7, 8, 10.
- Method signatures match between the spec's §5/§6 and the code in Tasks 2–7. `GetDetailByID` returns `AlbumListItem` everywhere; `Rename` returns `AlbumListItem` everywhere.
- `translateAlbumError` signature matches its usage inside every HTTP handler.
- CLI arg order is `<album_id> <media_id>…` for add and `<album_id> <media_id>` for remove, matching spec §8.
