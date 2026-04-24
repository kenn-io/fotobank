# Plan E2: Sharing — Grantee-Side Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship the grantee-side half of the sharing subsystem: header-mode identity wiring, a cross-owner scope resolver, grantee-scoped read endpoints under `/api/v1/shared/*`, an owner-facing share preview endpoint, and a `principal_display` caching middleware.

**Architecture:** Keep the repo→service→transport layering used by E1. `share.Repo` grows read-side query methods (`ValidateHeaderScopes`, `ListSharedMediaIDs`, `ListSharedAlbumIDs`, `ExpandScope`, `CountSharedMediaByScope`). A new `share.ScopeResolver` component at the service tier owns one decision — "which of these presented scopes authorise what?" — without touching media/album/storage state. `service.SharedReadService` is the new grantee-side auth boundary; it holds the resolver plus direct repo deps and never calls the owner services. HTTP routes under `/api/v1/shared/*` go through `SharedReadService` exclusively. The owner path gains one new route, `GET /api/v1/shares/{uuid}/preview`, backed by a separate `ExpandScope` materializer so the resolver never runs in a caller-as-grantee mode. Byte-streaming response logic (Range/206 for originals, plain copy for thumbs) is extracted from the existing owner handlers into shared helpers that both owner and grantee routes call.

**Tech Stack:** Go 1.26, `modernc.org/sqlite`, `database/sql`, `huma/v2`, `cobra`, `testify/require`, `github.com/google/uuid`.

**Spec:** `docs/superpowers/specs/2026-04-23-fotobank-plan-e2-sharing-grantee-side-design.md`

---

## File structure

```
internal/share/
├── resolver.go              — NEW: *ScopeResolver + AccessPath, AccessDecision, ResolvedScopes
├── resolver_test.go         — NEW
├── repo.go                  — MODIFY: add ValidateHeaderScopes, ListSharedMediaIDs,
│                               ListSharedAlbumIDs, ExpandScope, CountSharedMediaByScope
├── repo_test.go             — MODIFY: cover new query methods
├── display.go               — NEW: *PrincipalDisplayRepo (Upsert, Get, GetBatch)
└── display_test.go          — NEW

internal/service/
├── shared_read_service.go   — NEW: *SharedReadService
├── shared_read_service_test.go
├── share_service.go         — MODIFY: add PreviewScope + display-handle hooks
└── share_service_test.go    — MODIFY: cover PreviewScope

internal/media/
├── repo.go                  — MODIFY: add GetByIDs (order-preserving batch fetch)
└── repo_test.go             — MODIFY

internal/album/
├── repo.go                  — MODIFY: add GetDetailsByIDs (order-preserving batch fetch)
└── repo_test.go             — MODIFY

internal/httpapi/
├── shared.go                — NEW: /api/v1/shared/* routes + DTOs
├── shared_test.go           — NEW
├── originals.go             — NEW: writeOriginalResponse helper (extracted)
├── thumbs.go                — NEW: writeThumbResponse helper (extracted)
├── media_original.go        — MODIFY: call writeOriginalResponse
├── media_thumb.go           — MODIFY: call writeThumbResponse
├── middleware.go            — MODIFY: add WithPrincipalDisplayCache
├── middleware_test.go       — MODIFY
├── shares.go                — MODIFY: /preview route, scopeDTO.grantee_handle
├── shares_test.go           — MODIFY
└── api.go                   — MODIFY: Deps gains SharedReadService + PrincipalDisplayRepo;
                                register /shared/*; chain WithPrincipalDisplayCache

internal/identity/
├── stub.go                  — MODIFY: add NewStubWithScopes
└── stub_test.go             — MODIFY

internal/cli/
├── server.go                — MODIFY: construct Header provider when configured,
│                               PrincipalDisplayRepo + SharedReadService + display middleware
└── e2e_shared_test.go       — NEW: header-mode end-to-end test
```

---

## Sentinel / type reference (used by multiple tasks)

These shapes appear across tasks; the definitive declarations live in Tasks 1, 2, and 11. Later tasks reference them without re-declaring.

```go
// internal/share/resolver.go
package share

type AccessPath struct {
    ScopeUUID     string
    AlbumID       *string // non-nil iff the path runs through an album_live scope
    AllowDownload bool
}

type AccessDecision struct {
    Authorized bool
    Paths      []AccessPath
}

func (d AccessDecision) CanDownload() bool

type ResolvedScopes struct {
    ScopeUUIDs    []string           // sorted ascending, post-degradation
    Owner         owners.Principal   // retained owner per §6.1
    AllowDownload bool               // any validated scope has allow_download=true
}

const MaxHeaderScopes = 100

type ScopeResolver struct {
    shares *Repo
    now    func() time.Time
}
```

```go
// internal/share/repo.go — new methods, same package
// Live predicate = revoked_at IS NULL AND broker_status = 'active'
//                  AND (expires_at IS NULL OR expires_at > ?)
func (r *Repo) ValidateHeaderScopes(ctx context.Context, caller owners.Principal,
    uuids []string, now time.Time) ([]Scope, error)

// ListSharedMediaIDs returns (media_id, display_time, can_download) rows
// for the retained-owner validated set, cursor-paginated by display_time
// DESC + id ASC. See spec §6.4.
type SharedMediaRow struct {
    MediaID     string
    DisplayTime time.Time
    CanDownload bool
}

type SharedMediaCursor struct {
    AfterDisplayTime time.Time
    AfterID          string
    Limit            int
}

func (r *Repo) ListSharedMediaIDs(ctx context.Context, validated []Scope,
    owner owners.Principal, albumID string, cursor SharedMediaCursor) ([]SharedMediaRow, error)

// ListSharedAlbumIDs returns (album_id, can_download) for the album_live
// subset of validated, collapsed by album_id with MAX(allow_download).
type SharedAlbumRow struct {
    AlbumID     string
    CanDownload bool
}

func (r *Repo) ListSharedAlbumIDs(ctx context.Context, validated []Scope,
    owner owners.Principal) ([]SharedAlbumRow, error)

// CountSharedMediaByScope returns the item_count of one scope's coverage:
// len(scope_media) for media_set, album.item_count for album_live.
func (r *Repo) CountSharedMediaByScope(ctx context.Context, scopeUUID string) (int, error)

// ExpandScope is the pure materializer used by ShareService.PreviewScope.
// It reads scope + membership without grantee-identity plumbing; callers
// are expected to have already performed the owner-scoped auth check.
type ExpandedScope struct {
    Scope    Scope
    MediaIDs []string       // frozen membership for media_set; live album_media for album_live
    Album    *AlbumSummary  // non-nil iff target_type == album_live
}

type AlbumSummary struct {
    ID, Name  string
    ItemCount int
    UpdatedAt time.Time
}

func (r *Repo) ExpandScope(ctx context.Context, scopeUUID string) (ExpandedScope, error)
```

```go
// internal/share/display.go
type PrincipalDisplayRepo struct{ rw, ro *sql.DB }

func NewPrincipalDisplayRepo(rw, ro *sql.DB) *PrincipalDisplayRepo

// Upsert is conflict-aware: keeps the row with the newest cached_at.
// now is passed as time.Time so callers inject it (tests + middleware).
func (r *PrincipalDisplayRepo) Upsert(ctx context.Context, p identity.Principal, now time.Time) error

// Get returns ("", false) when the row is missing; handle empty when row
// exists but handle is NULL/empty.
func (r *PrincipalDisplayRepo) Get(ctx context.Context, owner owners.Principal) (string, bool, error)

// GetBatch fetches handles for a set of principals in one query and
// returns them as a map keyed by owners.Principal. Missing principals
// are absent from the map.
func (r *PrincipalDisplayRepo) GetBatch(ctx context.Context, principals []owners.Principal) (map[owners.Principal]string, error)
```

```go
// internal/service/shared_read_service.go
type SharedReadService struct {
    shares   *share.Repo
    media    *media.Repo
    albums   *album.Repo
    storage  storage.Store
    resolver *share.ScopeResolver
    now      func() time.Time
}

// Result structs (SharedScope, SharedAlbum, SharedMedia, ...)
// follow the spec §4.3 shapes verbatim.
```

```go
// internal/httpapi/originals.go
// Extracted from media_original.go; owns Range parsing, Content-Range,
// 206/416 semantics. Does not set Cache-Control / Vary / Content-Type /
// ETag / Last-Modified — callers set all of those first.
func writeOriginalResponse(
    w http.ResponseWriter,
    r *http.Request,
    m media.Media,
    open func(offset, length int64) (io.ReadCloser, error),
)

// internal/httpapi/thumbs.go
// Extracted from media_thumb.go; plain io.Copy on success (thumbs are
// small, no Range). Same cache-header policy as writeOriginalResponse:
// caller sets headers before invoking.
func writeThumbResponse(
    w http.ResponseWriter,
    r *http.Request,
    open func() (io.ReadCloser, error),
)
```

---

## Task 1: `share.Repo.ValidateHeaderScopes`

**Files:**
- Modify: `internal/share/repo.go`
- Modify: `internal/share/repo_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/share/repo_test.go`:

```go
func TestValidateHeaderScopesFiltersByGranteeAndLivePredicate(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    seedOwner(t, d, "h", "alice")
    seedOwner(t, d, "h", "bob")

    sharesRepo := share.NewRepo(d.WriteDB(), d.ReadDB())
    now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

    // 1. live + granted to bob — kept
    live := makeMediaSetScope(t, d, "h/alice", "h/bob", nil, now)
    bumpActive(t, d, live.UUID, now)

    // 2. revoked — dropped
    revoked := makeMediaSetScope(t, d, "h/alice", "h/bob", nil, now)
    bumpActive(t, d, revoked.UUID, now)
    _, err := sharesRepo.SetRevoking(context.Background(), revoked.UUID, now)
    r.NoError(err)

    // 3. pending (not active yet) — dropped
    pending := makeMediaSetScope(t, d, "h/alice", "h/bob", nil, now)

    // 4. expired — dropped
    past := now.Add(-time.Hour)
    expired := makeMediaSetScope(t, d, "h/alice", "h/bob", &past, now)
    bumpActive(t, d, expired.UUID, now)

    // 5. granted to someone else — dropped
    other := makeMediaSetScope(t, d, "h/alice", "h/charlie", nil, now)
    bumpActive(t, d, other.UUID, now)

    got, err := sharesRepo.ValidateHeaderScopes(context.Background(),
        owners.Principal{Hub: "h", UserID: "bob"},
        []string{live.UUID, revoked.UUID, pending.UUID, expired.UUID, other.UUID},
        now)
    r.NoError(err)
    r.Len(got, 1)
    r.Equal(live.UUID, got[0].UUID)
}

func TestValidateHeaderScopesEmptyInputReturnsEmpty(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    sharesRepo := share.NewRepo(d.WriteDB(), d.ReadDB())
    got, err := sharesRepo.ValidateHeaderScopes(context.Background(),
        owners.Principal{Hub: "h", UserID: "bob"}, nil, time.Now())
    r.NoError(err)
    r.Empty(got)
}
```

Imports to add (if missing): `"context"`, `"time"`, `"github.com/wesm/fotobank/internal/owners"`, `"github.com/wesm/fotobank/internal/share"`, `"github.com/wesm/fotobank/internal/testutil"`. The helpers `seedOwner`, `makeMediaSetScope`, `bumpActive` are existing test helpers from `internal/share/repo_test.go` (E1). If `bumpActive` is missing, add it inline:

```go
// bumpActive flips a freshly-inserted scope's broker_status to 'active'.
func bumpActive(t *testing.T, d *testutil.TestDB, uuid string, at time.Time) {
    t.Helper()
    _, err := d.WriteDB().ExecContext(context.Background(),
        `UPDATE scopes SET broker_status='active', broker_granted_at=?, broker_registered_at=? WHERE uuid=?`,
        at, at, uuid)
    require.NoError(t, err)
}
```

- [ ] **Step 2: Run the test — expect failure**

Run: `go test ./internal/share/... -run TestValidateHeaderScopes -v`
Expected: FAIL with "sharesRepo.ValidateHeaderScopes undefined".

- [ ] **Step 3: Implement `ValidateHeaderScopes`**

Append to `internal/share/repo.go`:

```go
// ValidateHeaderScopes returns the live, grantee-matching subset of
// uuids. Live means revoked_at IS NULL AND broker_status = 'active' AND
// (expires_at IS NULL OR expires_at > now). Order of returned rows is
// unspecified; callers who need a stable order sort themselves.
// Empty uuids returns (nil, nil) without a query.
func (r *Repo) ValidateHeaderScopes(
    ctx context.Context,
    caller owners.Principal,
    uuids []string,
    now time.Time,
) ([]Scope, error) {
    if len(uuids) == 0 {
        return nil, nil
    }
    placeholders := strings.Repeat("?,", len(uuids))
    placeholders = placeholders[:len(placeholders)-1]
    q := `SELECT ` + scopeColumns + `
  FROM scopes
 WHERE uuid IN (` + placeholders + `)
   AND grantee_hub = ?
   AND grantee_user_id = ?
   AND revoked_at IS NULL
   AND broker_status = 'active'
   AND (expires_at IS NULL OR expires_at > ?)`
    args := make([]any, 0, len(uuids)+3)
    for _, u := range uuids {
        args = append(args, u)
    }
    args = append(args, caller.Hub, caller.UserID, now)

    rows, err := r.ro.QueryContext(ctx, q, args...)
    if err != nil {
        return nil, fmt.Errorf("validate header scopes: %w", err)
    }
    defer rows.Close()
    var out []Scope
    for rows.Next() {
        s, err := scanScope(rows)
        if err != nil {
            return nil, fmt.Errorf("scan validated scope: %w", err)
        }
        out = append(out, s)
    }
    return out, rows.Err()
}
```

If `scopeColumns` does not yet exist as a package-level constant, introduce it as a shared string derived from the column list that `scanScope` already expects; locate it adjacent to `scanScope`.

Imports to add (if missing): `"strings"`.

- [ ] **Step 4: Run the test — expect PASS**

Run: `go test ./internal/share/... -run TestValidateHeaderScopes -v`
Expected: PASS.

- [ ] **Step 5: Run the full share package tests to ensure no regressions**

Run: `go test ./internal/share/... -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/share/repo.go internal/share/repo_test.go
git commit -m "share.Repo: add ValidateHeaderScopes for grantee-side reads"
```

---

## Task 2: `share.ScopeResolver` skeleton + `ResolveAll`

**Files:**
- Create: `internal/share/resolver.go`
- Create: `internal/share/resolver_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/share/resolver_test.go`:

```go
package share_test

import (
    "context"
    "sort"
    "testing"
    "time"

    "github.com/stretchr/testify/require"

    "github.com/wesm/fotobank/internal/owners"
    "github.com/wesm/fotobank/internal/share"
    "github.com/wesm/fotobank/internal/testutil"
)

func newResolver(t *testing.T, now time.Time) (*share.ScopeResolver, *share.Repo, *testutil.TestDB) {
    t.Helper()
    d := testutil.OpenTestDB(t)
    repo := share.NewRepo(d.WriteDB(), d.ReadDB())
    r := share.NewScopeResolver(repo, func() time.Time { return now })
    return r, repo, d
}

func TestResolveAllDropsMalformedDedupsAndCaps(t *testing.T) {
    r := require.New(t)
    now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
    resolver, _, d := newResolver(t, now)
    seedOwner(t, d, "h", "alice")
    seedOwner(t, d, "h", "bob")

    live := makeMediaSetScope(t, d, "h/alice", "h/bob", nil, now)
    bumpActive(t, d, live.UUID, now)

    input := []string{
        "",                         // empty, dropped
        "not-a-uuid",               // invalid syntax, dropped
        live.UUID,
        live.UUID,                  // duplicate, deduped
    }
    got, err := resolver.ResolveAll(context.Background(),
        owners.Principal{Hub: "h", UserID: "bob"}, input)
    r.NoError(err)
    r.Len(got.ScopeUUIDs, 1)
    r.Equal(live.UUID, got.ScopeUUIDs[0])
    r.Equal(owners.Principal{Hub: "h", UserID: "alice"}, got.Owner)
}

func TestResolveAllAppliesMaxHeaderScopesCap(t *testing.T) {
    r := require.New(t)
    now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
    resolver, _, d := newResolver(t, now)
    seedOwner(t, d, "h", "alice")
    seedOwner(t, d, "h", "bob")

    // Seed MaxHeaderScopes + 5 live scopes; verify we kept at most MaxHeaderScopes.
    uuids := make([]string, 0, share.MaxHeaderScopes+5)
    for i := 0; i < share.MaxHeaderScopes+5; i++ {
        s := makeMediaSetScope(t, d, "h/alice", "h/bob", nil, now)
        bumpActive(t, d, s.UUID, now)
        uuids = append(uuids, s.UUID)
    }
    got, err := resolver.ResolveAll(context.Background(),
        owners.Principal{Hub: "h", UserID: "bob"}, uuids)
    r.NoError(err)
    r.Len(got.ScopeUUIDs, share.MaxHeaderScopes)
    // Kept UUIDs must be a subset of the first MaxHeaderScopes post-dedupe,
    // sorted ascending on the way out.
    sorted := append([]string(nil), got.ScopeUUIDs...)
    r.True(sort.StringsAreSorted(sorted))
}

func TestResolveAllMultiOwnerKeepsLexSmallest(t *testing.T) {
    r := require.New(t)
    now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
    resolver, _, d := newResolver(t, now)
    seedOwner(t, d, "hubA", "alice")
    seedOwner(t, d, "hubB", "alice")   // different hub, same user_id
    seedOwner(t, d, "hubA", "bob")

    a := makeMediaSetScope(t, d, "hubA/alice", "hubA/bob", nil, now)
    bumpActive(t, d, a.UUID, now)
    b := makeMediaSetScope(t, d, "hubB/alice", "hubA/bob", nil, now)
    bumpActive(t, d, b.UUID, now)

    got, err := resolver.ResolveAll(context.Background(),
        owners.Principal{Hub: "hubA", UserID: "bob"}, []string{a.UUID, b.UUID})
    r.NoError(err)
    // ("hubA","alice") < ("hubB","alice"), so a wins.
    r.Equal(owners.Principal{Hub: "hubA", UserID: "alice"}, got.Owner)
    r.Len(got.ScopeUUIDs, 1)
    r.Equal(a.UUID, got.ScopeUUIDs[0])
}
```

- [ ] **Step 2: Run the test — expect failure**

Run: `go test ./internal/share/... -run TestResolveAll -v`
Expected: FAIL (ScopeResolver undefined).

- [ ] **Step 3: Implement `ScopeResolver` + `ResolveAll`**

Create `internal/share/resolver.go`:

```go
package share

import (
    "context"
    "sort"
    "time"

    "github.com/google/uuid"

    "github.com/wesm/fotobank/internal/owners"
)

// AccessPath is one authorisation route from a caller to a specific
// target. Overlapping scopes are common and callers must OR across paths.
type AccessPath struct {
    ScopeUUID     string
    AlbumID       *string
    AllowDownload bool
}

// AccessDecision is the answer to a targeted check.
type AccessDecision struct {
    Authorized bool
    Paths      []AccessPath
}

// CanDownload is the OR across paths.
func (d AccessDecision) CanDownload() bool {
    for _, p := range d.Paths {
        if p.AllowDownload {
            return true
        }
    }
    return false
}

// ResolvedScopes is the fully-materialised view used by listing endpoints.
type ResolvedScopes struct {
    ScopeUUIDs    []string
    Owner         owners.Principal
    AllowDownload bool
    // Validated is the retained-owner slice; downstream repo calls use
    // it as a VALUES-CTE input without re-querying scopes.
    Validated []Scope
}

// MaxHeaderScopes caps the number of distinct scope UUIDs the resolver
// considers per request. Presentations beyond the cap are truncated
// (earliest-in-input wins after dedupe).
const MaxHeaderScopes = 100

// ScopeResolver validates presented scopes and answers access questions.
// It reads only scopes / scope_media; it never touches media, album, or
// storage state.
type ScopeResolver struct {
    shares *Repo
    now    func() time.Time
}

// NewScopeResolver constructs a resolver with prod defaults.
func NewScopeResolver(r *Repo, now func() time.Time) *ScopeResolver {
    if now == nil {
        now = func() time.Time { return time.Now().UTC() }
    }
    return &ScopeResolver{shares: r, now: now}
}

// sanitizeHeaderScopes dedupes, caps, and drops syntactically-invalid
// UUIDs. Empty input returns nil.
func sanitizeHeaderScopes(in []string) []string {
    if len(in) == 0 {
        return nil
    }
    seen := make(map[string]struct{}, len(in))
    out := make([]string, 0, len(in))
    for _, s := range in {
        if s == "" {
            continue
        }
        if _, err := uuid.Parse(s); err != nil {
            continue
        }
        if _, ok := seen[s]; ok {
            continue
        }
        seen[s] = struct{}{}
        out = append(out, s)
        if len(out) == MaxHeaderScopes {
            break
        }
    }
    return out
}

// ResolveAll validates the presented scopes and returns the fully-
// expanded view, applying the single-owner degradation rule from
// spec §6.1.
func (r *ScopeResolver) ResolveAll(
    ctx context.Context,
    caller owners.Principal,
    headerScopes []string,
) (ResolvedScopes, error) {
    sanitized := sanitizeHeaderScopes(headerScopes)
    if len(sanitized) == 0 {
        return ResolvedScopes{}, nil
    }
    validated, err := r.shares.ValidateHeaderScopes(ctx, caller, sanitized, r.now())
    if err != nil {
        return ResolvedScopes{}, err
    }
    if len(validated) == 0 {
        return ResolvedScopes{}, nil
    }
    retained := retainSmallestOwner(validated)
    uuids := make([]string, 0, len(retained))
    allowAny := false
    for _, s := range retained {
        uuids = append(uuids, s.UUID)
        if s.AllowDownload {
            allowAny = true
        }
    }
    sort.Strings(uuids)
    return ResolvedScopes{
        ScopeUUIDs:    uuids,
        Owner:         retained[0].Owner,
        AllowDownload: allowAny,
        Validated:     retained,
    }, nil
}

// retainSmallestOwner returns the subset of scopes belonging to the
// lexicographically smallest (hub, user_id) owner. See spec §6.1.
func retainSmallestOwner(scopes []Scope) []Scope {
    if len(scopes) == 0 {
        return nil
    }
    smallest := scopes[0].Owner
    for _, s := range scopes[1:] {
        if lessOwner(s.Owner, smallest) {
            smallest = s.Owner
        }
    }
    out := make([]Scope, 0, len(scopes))
    for _, s := range scopes {
        if s.Owner == smallest {
            out = append(out, s)
        }
    }
    return out
}

func lessOwner(a, b owners.Principal) bool {
    if a.Hub != b.Hub {
        return a.Hub < b.Hub
    }
    return a.UserID < b.UserID
}
```

- [ ] **Step 4: Run the test — expect PASS**

Run: `go test ./internal/share/... -run TestResolveAll -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/share/resolver.go internal/share/resolver_test.go
git commit -m "share.ScopeResolver: ResolveAll + MaxHeaderScopes cap + lex-smallest owner"
```

---

## Task 3: `share.ScopeResolver.CheckMediaAccess`

**Files:**
- Modify: `internal/share/resolver.go`
- Modify: `internal/share/resolver_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/share/resolver_test.go`:

```go
func TestCheckMediaAccessViaMediaSet(t *testing.T) {
    r := require.New(t)
    now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
    resolver, _, d := newResolver(t, now)
    seedOwner(t, d, "h", "alice")
    seedOwner(t, d, "h", "bob")

    m := seedMedia(t, d, "h/alice")
    s := makeMediaSetScope(t, d, "h/alice", "h/bob", nil, now, m)
    bumpActive(t, d, s.UUID, now)

    dec, err := resolver.CheckMediaAccess(context.Background(),
        owners.Principal{Hub: "h", UserID: "bob"}, []string{s.UUID}, m)
    r.NoError(err)
    r.True(dec.Authorized)
    r.Len(dec.Paths, 1)
    r.Equal(s.UUID, dec.Paths[0].ScopeUUID)
    r.Nil(dec.Paths[0].AlbumID)
}

func TestCheckMediaAccessViaAlbumLive(t *testing.T) {
    r := require.New(t)
    now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
    resolver, _, d := newResolver(t, now)
    seedOwner(t, d, "h", "alice")
    seedOwner(t, d, "h", "bob")

    album := seedAlbumWithMedia(t, d, "h/alice", 2)
    s := makeAlbumLiveScope(t, d, "h/alice", "h/bob", album.ID, nil, now)
    bumpActive(t, d, s.UUID, now)

    dec, err := resolver.CheckMediaAccess(context.Background(),
        owners.Principal{Hub: "h", UserID: "bob"}, []string{s.UUID}, album.Media[0])
    r.NoError(err)
    r.True(dec.Authorized)
    r.Len(dec.Paths, 1)
    r.Equal(s.UUID, dec.Paths[0].ScopeUUID)
    r.NotNil(dec.Paths[0].AlbumID)
    r.Equal(album.ID, *dec.Paths[0].AlbumID)
}

func TestCheckMediaAccessUnauthorizedReturnsNoPaths(t *testing.T) {
    r := require.New(t)
    now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
    resolver, _, d := newResolver(t, now)
    seedOwner(t, d, "h", "alice")
    seedOwner(t, d, "h", "bob")
    m := seedMedia(t, d, "h/alice")
    other := seedMedia(t, d, "h/alice")
    s := makeMediaSetScope(t, d, "h/alice", "h/bob", nil, now, m)
    bumpActive(t, d, s.UUID, now)

    dec, err := resolver.CheckMediaAccess(context.Background(),
        owners.Principal{Hub: "h", UserID: "bob"}, []string{s.UUID}, other)
    r.NoError(err)
    r.False(dec.Authorized)
    r.Empty(dec.Paths)
}

func TestCheckMediaAccessOverlappingScopesOrdDownload(t *testing.T) {
    r := require.New(t)
    now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
    resolver, _, d := newResolver(t, now)
    seedOwner(t, d, "h", "alice")
    seedOwner(t, d, "h", "bob")

    album := seedAlbumWithMedia(t, d, "h/alice", 1)
    media := album.Media[0]
    // album_live without download
    s1 := makeAlbumLiveScope(t, d, "h/alice", "h/bob", album.ID, nil, now)
    bumpActive(t, d, s1.UUID, now)
    // media_set with download, same media
    s2 := makeMediaSetScopeWithDownload(t, d, "h/alice", "h/bob", nil, now, media, true)
    bumpActive(t, d, s2.UUID, now)

    dec, err := resolver.CheckMediaAccess(context.Background(),
        owners.Principal{Hub: "h", UserID: "bob"}, []string{s1.UUID, s2.UUID}, media)
    r.NoError(err)
    r.True(dec.Authorized)
    r.Len(dec.Paths, 2)
    r.True(dec.CanDownload())
}
```

If `seedMedia`, `seedAlbumWithMedia`, `makeAlbumLiveScope`, and `makeMediaSetScopeWithDownload` are missing from `internal/share/repo_test.go`'s helper set, add them as thin wrappers around existing E1 test helpers. Keep them in `internal/share/repo_test.go` so the `*_test.go` packages share them via the shared test package.

- [ ] **Step 2: Run the test — expect failure**

Run: `go test ./internal/share/... -run TestCheckMediaAccess -v`
Expected: FAIL (method undefined).

- [ ] **Step 3: Implement `CheckMediaAccess`**

Append to `internal/share/resolver.go`:

```go
// CheckMediaAccess answers "can caller see media mediaID via one of
// these presented scopes?" Runs the two-pass shape from spec §6.3.
func (r *ScopeResolver) CheckMediaAccess(
    ctx context.Context,
    caller owners.Principal,
    headerScopes []string,
    mediaID string,
) (AccessDecision, error) {
    sanitized := sanitizeHeaderScopes(headerScopes)
    if len(sanitized) == 0 {
        return AccessDecision{}, nil
    }
    validated, err := r.shares.ValidateHeaderScopes(ctx, caller, sanitized, r.now())
    if err != nil {
        return AccessDecision{}, err
    }
    if len(validated) == 0 {
        return AccessDecision{}, nil
    }
    retained := retainSmallestOwner(validated)
    return r.shares.CoverMediaByScopes(ctx, retained, retained[0].Owner, mediaID)
}
```

Now add `CoverMediaByScopes` to `internal/share/repo.go`:

```go
// CoverMediaByScopes runs pass 2 of CheckMediaAccess: given a
// retained-owner validated slice, returns one AccessPath per covering
// scope. See spec §6.3 pass 2.
func (r *Repo) CoverMediaByScopes(
    ctx context.Context,
    validated []Scope,
    owner owners.Principal,
    mediaID string,
) (AccessDecision, error) {
    if len(validated) == 0 {
        return AccessDecision{}, nil
    }
    // Build the VALUES-CTE. SQLite accepts ("uuid","target_type", ...).
    // Bind one arg per column per row.
    const cols = 4
    valRows := make([]string, 0, len(validated))
    args := make([]any, 0, len(validated)*cols+3)
    for _, s := range validated {
        valRows = append(valRows, "(?, ?, ?, ?)")
        var albumID any
        if s.TargetAlbumID != nil {
            albumID = *s.TargetAlbumID
        }
        args = append(args, s.UUID, string(s.TargetType), albumID, boolToInt(s.AllowDownload))
    }
    // Coverage args: owner hub, owner user_id, mediaID (used twice — once per EXISTS).
    args = append(args, owner.Hub, owner.UserID, mediaID, mediaID)

    q := `
WITH validated(uuid, target_type, target_album_id, allow_download) AS (
    VALUES ` + strings.Join(valRows, ",") + `
)
SELECT v.uuid, v.target_type, v.target_album_id, v.allow_download
  FROM validated v
  JOIN scopes s ON s.uuid = v.uuid
 WHERE s.owner_hub = ? AND s.owner_user_id = ?
   AND (
         (v.target_type = 'media_set' AND EXISTS (
             SELECT 1 FROM scope_media sm
              WHERE sm.scope_uuid = v.uuid AND sm.media_id = ?
         ))
      OR (v.target_type = 'album_live' AND EXISTS (
             SELECT 1 FROM album_media am
              WHERE am.album_id = v.target_album_id AND am.media_id = ?
         ))
       )
`
    rows, err := r.ro.QueryContext(ctx, q, args...)
    if err != nil {
        return AccessDecision{}, fmt.Errorf("cover media by scopes: %w", err)
    }
    defer rows.Close()
    paths := make([]AccessPath, 0, len(validated))
    for rows.Next() {
        var (
            p         AccessPath
            albumID   sql.NullString
            allowInt  int
            targetStr string
        )
        if err := rows.Scan(&p.ScopeUUID, &targetStr, &albumID, &allowInt); err != nil {
            return AccessDecision{}, fmt.Errorf("scan cover row: %w", err)
        }
        if albumID.Valid {
            a := albumID.String
            p.AlbumID = &a
        }
        p.AllowDownload = allowInt != 0
        paths = append(paths, p)
    }
    if err := rows.Err(); err != nil {
        return AccessDecision{}, err
    }
    return AccessDecision{Authorized: len(paths) > 0, Paths: paths}, nil
}
```

Note the belt-and-braces `s.owner_hub = ? AND s.owner_user_id = ?` guard per spec §6.3.

- [ ] **Step 4: Run the test — expect PASS**

Run: `go test ./internal/share/... -run TestCheckMediaAccess -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/share/resolver.go internal/share/resolver_test.go internal/share/repo.go internal/share/repo_test.go
git commit -m "share.ScopeResolver: add CheckMediaAccess + Repo.CoverMediaByScopes"
```

---

## Task 4: `share.ScopeResolver.CheckAlbumAccess` (album_live only)

**Files:**
- Modify: `internal/share/resolver.go`
- Modify: `internal/share/repo.go`
- Modify: `internal/share/resolver_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/share/resolver_test.go`:

```go
func TestCheckAlbumAccessAlbumLiveAuthorized(t *testing.T) {
    r := require.New(t)
    now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
    resolver, _, d := newResolver(t, now)
    seedOwner(t, d, "h", "alice")
    seedOwner(t, d, "h", "bob")

    album := seedAlbumWithMedia(t, d, "h/alice", 1)
    s := makeAlbumLiveScope(t, d, "h/alice", "h/bob", album.ID, nil, now)
    bumpActive(t, d, s.UUID, now)

    dec, err := resolver.CheckAlbumAccess(context.Background(),
        owners.Principal{Hub: "h", UserID: "bob"}, []string{s.UUID}, album.ID)
    r.NoError(err)
    r.True(dec.Authorized)
    r.Len(dec.Paths, 1)
    r.NotNil(dec.Paths[0].AlbumID)
    r.Equal(album.ID, *dec.Paths[0].AlbumID)
}

func TestCheckAlbumAccessMediaSetDoesNotImplyAlbumVisibility(t *testing.T) {
    r := require.New(t)
    now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
    resolver, _, d := newResolver(t, now)
    seedOwner(t, d, "h", "alice")
    seedOwner(t, d, "h", "bob")

    album := seedAlbumWithMedia(t, d, "h/alice", 1)
    // media_set scope covering every media in the album — must NOT authorise the album.
    s := makeMediaSetScope(t, d, "h/alice", "h/bob", nil, now, album.Media...)
    bumpActive(t, d, s.UUID, now)

    dec, err := resolver.CheckAlbumAccess(context.Background(),
        owners.Principal{Hub: "h", UserID: "bob"}, []string{s.UUID}, album.ID)
    r.NoError(err)
    r.False(dec.Authorized)
    r.Empty(dec.Paths)
}
```

- [ ] **Step 2: Run the test — expect failure**

Run: `go test ./internal/share/... -run TestCheckAlbumAccess -v`
Expected: FAIL.

- [ ] **Step 3: Implement `CheckAlbumAccess`**

Append to `internal/share/resolver.go`:

```go
// CheckAlbumAccess answers "can caller see album albumID's metadata and
// contents?" It checks album_live only — a media_set scope does not
// imply album visibility even if its membership happens to belong to
// that album. See spec §4.1 + §11.6.
func (r *ScopeResolver) CheckAlbumAccess(
    ctx context.Context,
    caller owners.Principal,
    headerScopes []string,
    albumID string,
) (AccessDecision, error) {
    sanitized := sanitizeHeaderScopes(headerScopes)
    if len(sanitized) == 0 {
        return AccessDecision{}, nil
    }
    validated, err := r.shares.ValidateHeaderScopes(ctx, caller, sanitized, r.now())
    if err != nil {
        return AccessDecision{}, err
    }
    if len(validated) == 0 {
        return AccessDecision{}, nil
    }
    retained := retainSmallestOwner(validated)
    return r.shares.CoverAlbumByScopes(ctx, retained, retained[0].Owner, albumID)
}
```

Add `CoverAlbumByScopes` to `internal/share/repo.go`:

```go
// CoverAlbumByScopes runs the album_live-only pass 2 of
// CheckAlbumAccess. Rows are produced from the retained validated slice;
// media_set scopes are silently ignored even if their members belong to
// albumID.
func (r *Repo) CoverAlbumByScopes(
    ctx context.Context,
    validated []Scope,
    owner owners.Principal,
    albumID string,
) (AccessDecision, error) {
    if len(validated) == 0 {
        return AccessDecision{}, nil
    }
    // Filter to album_live entries up front — pass zero media_set rows
    // into the VALUES-CTE so the SQL has no branch.
    live := validated[:0]
    for _, s := range validated {
        if s.TargetType == TargetAlbumLive && s.TargetAlbumID != nil {
            live = append(live, s)
        }
    }
    if len(live) == 0 {
        return AccessDecision{}, nil
    }
    valRows := make([]string, 0, len(live))
    args := make([]any, 0, len(live)*3+3)
    for _, s := range live {
        valRows = append(valRows, "(?, ?, ?)")
        args = append(args, s.UUID, *s.TargetAlbumID, boolToInt(s.AllowDownload))
    }
    args = append(args, owner.Hub, owner.UserID, albumID)

    q := `
WITH validated(uuid, target_album_id, allow_download) AS (
    VALUES ` + strings.Join(valRows, ",") + `
)
SELECT v.uuid, v.target_album_id, v.allow_download
  FROM validated v
  JOIN scopes s ON s.uuid = v.uuid
 WHERE s.owner_hub = ? AND s.owner_user_id = ?
   AND v.target_album_id = ?
`
    rows, err := r.ro.QueryContext(ctx, q, args...)
    if err != nil {
        return AccessDecision{}, fmt.Errorf("cover album by scopes: %w", err)
    }
    defer rows.Close()
    paths := make([]AccessPath, 0, len(live))
    for rows.Next() {
        var (
            p        AccessPath
            albumStr string
            allowInt int
        )
        if err := rows.Scan(&p.ScopeUUID, &albumStr, &allowInt); err != nil {
            return AccessDecision{}, fmt.Errorf("scan cover album row: %w", err)
        }
        a := albumStr
        p.AlbumID = &a
        p.AllowDownload = allowInt != 0
        paths = append(paths, p)
    }
    return AccessDecision{Authorized: len(paths) > 0, Paths: paths}, rows.Err()
}
```

- [ ] **Step 4: Run the test — expect PASS**

Run: `go test ./internal/share/... -run TestCheckAlbumAccess -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/share/resolver.go internal/share/repo.go internal/share/resolver_test.go
git commit -m "share.ScopeResolver: add CheckAlbumAccess (album_live only)"
```

---

## Task 5: `share.Repo.ListSharedMediaIDs`

**Files:**
- Modify: `internal/share/repo.go`
- Modify: `internal/share/repo_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/share/repo_test.go`:

```go
func TestListSharedMediaIDsDedupesAndOrdersByDisplayTime(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    seedOwner(t, d, "h", "alice")
    seedOwner(t, d, "h", "bob")

    now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
    m1 := seedMediaWithTimestamp(t, d, "h/alice", now.Add(-3*time.Hour))
    m2 := seedMediaWithTimestamp(t, d, "h/alice", now.Add(-2*time.Hour))
    m3 := seedMediaWithTimestamp(t, d, "h/alice", now.Add(-1*time.Hour))

    // media_set covering {m1, m2} without download
    s1 := makeMediaSetScope(t, d, "h/alice", "h/bob", nil, now, m1, m2)
    bumpActive(t, d, s1.UUID, now)
    // overlapping media_set covering {m2, m3} with download
    s2 := makeMediaSetScopeWithDownload(t, d, "h/alice", "h/bob", nil, now, true, m2, m3)
    bumpActive(t, d, s2.UUID, now)

    repo := share.NewRepo(d.WriteDB(), d.ReadDB())
    resolver := share.NewScopeResolver(repo, func() time.Time { return now })
    resolved, err := resolver.ResolveAll(context.Background(),
        owners.Principal{Hub: "h", UserID: "bob"}, []string{s1.UUID, s2.UUID})
    r.NoError(err)

    rows, err := repo.ListSharedMediaIDs(context.Background(),
        resolved.Validated, resolved.Owner, "",
        share.SharedMediaCursor{Limit: 10})
    r.NoError(err)
    r.Len(rows, 3)
    // display_time DESC: m3, m2, m1
    r.Equal(m3, rows[0].MediaID)
    r.Equal(m2, rows[1].MediaID)
    r.Equal(m1, rows[2].MediaID)
    // can_download is OR'd across scopes: m2 and m3 covered by s2 (download=true); m1 only s1 (false).
    r.True(rows[0].CanDownload)
    r.True(rows[1].CanDownload)
    r.False(rows[2].CanDownload)
}

func TestListSharedMediaIDsCursorPagesCorrectly(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    seedOwner(t, d, "h", "alice")
    seedOwner(t, d, "h", "bob")
    now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

    ids := make([]string, 0, 5)
    for i := 0; i < 5; i++ {
        ids = append(ids, seedMediaWithTimestamp(t, d, "h/alice", now.Add(time.Duration(-i)*time.Hour)))
    }
    s := makeMediaSetScope(t, d, "h/alice", "h/bob", nil, now, ids...)
    bumpActive(t, d, s.UUID, now)

    repo := share.NewRepo(d.WriteDB(), d.ReadDB())
    resolver := share.NewScopeResolver(repo, func() time.Time { return now })
    resolved, err := resolver.ResolveAll(context.Background(),
        owners.Principal{Hub: "h", UserID: "bob"}, []string{s.UUID})
    r.NoError(err)

    // Page 1: limit 2 → two newest.
    p1, err := repo.ListSharedMediaIDs(context.Background(), resolved.Validated,
        resolved.Owner, "", share.SharedMediaCursor{Limit: 2})
    r.NoError(err)
    r.Len(p1, 2)
    r.Equal(ids[0], p1[0].MediaID) // newest
    r.Equal(ids[1], p1[1].MediaID)

    // Page 2: cursor = last of page 1.
    p2, err := repo.ListSharedMediaIDs(context.Background(), resolved.Validated,
        resolved.Owner, "",
        share.SharedMediaCursor{
            AfterDisplayTime: p1[1].DisplayTime,
            AfterID:          p1[1].MediaID,
            Limit:            2,
        })
    r.NoError(err)
    r.Len(p2, 2)
    r.Equal(ids[2], p2[0].MediaID)
    r.Equal(ids[3], p2[1].MediaID)
}
```

- [ ] **Step 2: Run the test — expect failure**

Run: `go test ./internal/share/... -run TestListSharedMediaIDs -v`
Expected: FAIL.

- [ ] **Step 3: Implement `ListSharedMediaIDs`**

Append to `internal/share/repo.go`:

```go
// SharedMediaRow is one row from ListSharedMediaIDs.
type SharedMediaRow struct {
    MediaID     string
    DisplayTime time.Time
    CanDownload bool
}

// SharedMediaCursor paginates ListSharedMediaIDs by (display_time, id).
// Limit is clamped to 1..500 by the service; zero = unbounded (the repo
// itself does not enforce a cap — callers must).
type SharedMediaCursor struct {
    AfterDisplayTime time.Time
    AfterID          string
    Limit            int
}

// ListSharedMediaIDs returns the deduped union of media visible via the
// retained-owner validated slice. Rows are ordered display_time DESC,
// id ASC; can_download is MAX(allow_download) across covering scopes.
// If albumID is non-empty the result is restricted to album_media
// members of that album; this is how ListAlbumMedia is served.
// See spec §6.4.
func (r *Repo) ListSharedMediaIDs(
    ctx context.Context,
    validated []Scope,
    owner owners.Principal,
    albumID string,
    cursor SharedMediaCursor,
) ([]SharedMediaRow, error) {
    if len(validated) == 0 {
        return nil, nil
    }
    const cols = 4
    valRows := make([]string, 0, len(validated))
    args := make([]any, 0, len(validated)*cols+8)
    for _, s := range validated {
        valRows = append(valRows, "(?, ?, ?, ?)")
        var albumArg any
        if s.TargetAlbumID != nil {
            albumArg = *s.TargetAlbumID
        }
        args = append(args, s.UUID, string(s.TargetType), albumArg, boolToInt(s.AllowDownload))
    }

    // Cursor binds: has_cursor (1 = cursor present), cursor_time,
    // cursor_time (again for the tie-break), cursor_id.
    hasCursor := 0
    if !cursor.AfterDisplayTime.IsZero() || cursor.AfterID != "" {
        hasCursor = 1
    }
    args = append(args, owner.Hub, owner.UserID)
    args = append(args, hasCursor, cursor.AfterDisplayTime, cursor.AfterDisplayTime, cursor.AfterID)

    // Optional album filter.
    albumPredicate := ""
    if albumID != "" {
        albumPredicate = ` AND EXISTS (
            SELECT 1 FROM album_media am
             WHERE am.album_id = ? AND am.media_id = m.id
        )`
        args = append(args, albumID)
    }

    // Limit handling. Zero or negative = no LIMIT clause; otherwise bind
    // limit as the last arg.
    limitClause := ""
    if cursor.Limit > 0 {
        limitClause = " LIMIT ?"
        args = append(args, cursor.Limit)
    }

    q := `
WITH validated(uuid, target_type, target_album_id, allow_download) AS (
    VALUES ` + strings.Join(valRows, ",") + `
)
SELECT m.id,
       COALESCE(m.timestamp, m.imported_at) AS display_time,
       MAX(covers.allow_download) AS can_download
  FROM media m
  JOIN (
      SELECT sm.media_id AS media_id, v.allow_download
        FROM scope_media sm
        JOIN validated v ON v.uuid = sm.scope_uuid
       WHERE v.target_type = 'media_set'
      UNION ALL
      SELECT am.media_id AS media_id, v.allow_download
        FROM album_media am
        JOIN validated v ON v.target_album_id = am.album_id
       WHERE v.target_type = 'album_live'
  ) covers ON covers.media_id = m.id
 WHERE m.owner_hub = ? AND m.owner_user_id = ?
   AND (
         ? = 0
      OR COALESCE(m.timestamp, m.imported_at) < ?
      OR (COALESCE(m.timestamp, m.imported_at) = ? AND m.id > ?)
       )` + albumPredicate + `
 GROUP BY m.id
 ORDER BY display_time DESC, m.id ASC` + limitClause

    rows, err := r.ro.QueryContext(ctx, q, args...)
    if err != nil {
        return nil, fmt.Errorf("list shared media ids: %w", err)
    }
    defer rows.Close()

    out := make([]SharedMediaRow, 0, 32)
    for rows.Next() {
        var (
            row      SharedMediaRow
            allowInt int
        )
        if err := rows.Scan(&row.MediaID, &row.DisplayTime, &allowInt); err != nil {
            return nil, fmt.Errorf("scan shared media row: %w", err)
        }
        row.CanDownload = allowInt != 0
        out = append(out, row)
    }
    return out, rows.Err()
}
```

- [ ] **Step 4: Run the test — expect PASS**

Run: `go test ./internal/share/... -run TestListSharedMediaIDs -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/share/repo.go internal/share/repo_test.go
git commit -m "share.Repo.ListSharedMediaIDs: cursor + MAX(allow_download) across scopes"
```

---

## Task 6: `share.Repo.ListSharedAlbumIDs` + `CountSharedMediaByScope`

**Files:**
- Modify: `internal/share/repo.go`
- Modify: `internal/share/repo_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/share/repo_test.go`:

```go
func TestListSharedAlbumIDsReturnsAlbumLiveOnly(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    seedOwner(t, d, "h", "alice")
    seedOwner(t, d, "h", "bob")
    now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

    album := seedAlbumWithMedia(t, d, "h/alice", 2)
    // two album_live scopes over same album, one allow=true
    s1 := makeAlbumLiveScope(t, d, "h/alice", "h/bob", album.ID, nil, now)
    bumpActive(t, d, s1.UUID, now)
    s2 := makeAlbumLiveScopeWithDownload(t, d, "h/alice", "h/bob", album.ID, nil, now, true)
    bumpActive(t, d, s2.UUID, now)
    // media_set scope over the same media — must NOT appear in album listing.
    ms := makeMediaSetScope(t, d, "h/alice", "h/bob", nil, now, album.Media...)
    bumpActive(t, d, ms.UUID, now)

    repo := share.NewRepo(d.WriteDB(), d.ReadDB())
    resolver := share.NewScopeResolver(repo, func() time.Time { return now })
    resolved, err := resolver.ResolveAll(context.Background(),
        owners.Principal{Hub: "h", UserID: "bob"},
        []string{s1.UUID, s2.UUID, ms.UUID})
    r.NoError(err)

    rows, err := repo.ListSharedAlbumIDs(context.Background(), resolved.Validated, resolved.Owner)
    r.NoError(err)
    r.Len(rows, 1)
    r.Equal(album.ID, rows[0].AlbumID)
    r.True(rows[0].CanDownload) // OR across s1 and s2
}

func TestCountSharedMediaByScopeAlbumLive(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    seedOwner(t, d, "h", "alice")
    seedOwner(t, d, "h", "bob")
    now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
    album := seedAlbumWithMedia(t, d, "h/alice", 3)
    s := makeAlbumLiveScope(t, d, "h/alice", "h/bob", album.ID, nil, now)
    repo := share.NewRepo(d.WriteDB(), d.ReadDB())
    n, err := repo.CountSharedMediaByScope(context.Background(), s.UUID)
    r.NoError(err)
    r.Equal(3, n)
}

func TestCountSharedMediaByScopeMediaSet(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    seedOwner(t, d, "h", "alice")
    seedOwner(t, d, "h", "bob")
    now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
    m1 := seedMedia(t, d, "h/alice")
    m2 := seedMedia(t, d, "h/alice")
    s := makeMediaSetScope(t, d, "h/alice", "h/bob", nil, now, m1, m2)
    repo := share.NewRepo(d.WriteDB(), d.ReadDB())
    n, err := repo.CountSharedMediaByScope(context.Background(), s.UUID)
    r.NoError(err)
    r.Equal(2, n)
}
```

- [ ] **Step 2: Run the tests — expect failure**

Run: `go test ./internal/share/... -run "TestListSharedAlbumIDs|TestCountSharedMediaByScope" -v`
Expected: FAIL.

- [ ] **Step 3: Implement both methods**

Append to `internal/share/repo.go`:

```go
// SharedAlbumRow is one row from ListSharedAlbumIDs.
type SharedAlbumRow struct {
    AlbumID     string
    CanDownload bool
}

// ListSharedAlbumIDs returns distinct album ids authorised by the
// album_live subset of validated, with MAX(allow_download) collapsed
// per album. Media_set scopes are silently ignored.
func (r *Repo) ListSharedAlbumIDs(
    ctx context.Context,
    validated []Scope,
    owner owners.Principal,
) ([]SharedAlbumRow, error) {
    if len(validated) == 0 {
        return nil, nil
    }
    live := validated[:0]
    for _, s := range validated {
        if s.TargetType == TargetAlbumLive && s.TargetAlbumID != nil {
            live = append(live, s)
        }
    }
    if len(live) == 0 {
        return nil, nil
    }
    valRows := make([]string, 0, len(live))
    args := make([]any, 0, len(live)*3+2)
    for _, s := range live {
        valRows = append(valRows, "(?, ?, ?)")
        args = append(args, s.UUID, *s.TargetAlbumID, boolToInt(s.AllowDownload))
    }
    args = append(args, owner.Hub, owner.UserID)

    q := `
WITH validated(uuid, target_album_id, allow_download) AS (
    VALUES ` + strings.Join(valRows, ",") + `
)
SELECT a.id, MAX(v.allow_download)
  FROM validated v
  JOIN albums a ON a.id = v.target_album_id
 WHERE a.owner_hub = ? AND a.owner_user_id = ?
 GROUP BY a.id
`
    rows, err := r.ro.QueryContext(ctx, q, args...)
    if err != nil {
        return nil, fmt.Errorf("list shared album ids: %w", err)
    }
    defer rows.Close()
    out := make([]SharedAlbumRow, 0, len(live))
    for rows.Next() {
        var (
            row      SharedAlbumRow
            allowInt int
        )
        if err := rows.Scan(&row.AlbumID, &allowInt); err != nil {
            return nil, fmt.Errorf("scan shared album row: %w", err)
        }
        row.CanDownload = allowInt != 0
        out = append(out, row)
    }
    return out, rows.Err()
}

// CountSharedMediaByScope returns the number of media covered by the
// scope, reading scope_media for media_set and album_media for
// album_live. Returns errs.ErrNotFound if the scope row does not exist.
func (r *Repo) CountSharedMediaByScope(ctx context.Context, scopeUUID string) (int, error) {
    var (
        targetType TargetType
        albumID    sql.NullString
    )
    err := r.ro.QueryRowContext(ctx,
        `SELECT target_type, target_album_id FROM scopes WHERE uuid = ?`, scopeUUID,
    ).Scan(&targetType, &albumID)
    if errors.Is(err, sql.ErrNoRows) {
        return 0, fmt.Errorf("%w: scope uuid=%s", errs.ErrNotFound, scopeUUID)
    }
    if err != nil {
        return 0, fmt.Errorf("count scope media: load scope: %w", err)
    }
    switch targetType {
    case TargetMediaSet:
        var n int
        err := r.ro.QueryRowContext(ctx,
            `SELECT COUNT(*) FROM scope_media WHERE scope_uuid = ?`, scopeUUID,
        ).Scan(&n)
        if err != nil {
            return 0, fmt.Errorf("count scope_media: %w", err)
        }
        return n, nil
    case TargetAlbumLive:
        if !albumID.Valid {
            return 0, fmt.Errorf("album_live scope %s missing target_album_id", scopeUUID)
        }
        var n int
        err := r.ro.QueryRowContext(ctx,
            `SELECT COUNT(*) FROM album_media WHERE album_id = ?`, albumID.String,
        ).Scan(&n)
        if err != nil {
            return 0, fmt.Errorf("count album_media: %w", err)
        }
        return n, nil
    default:
        return 0, fmt.Errorf("unknown target_type %q", string(targetType))
    }
}
```

- [ ] **Step 4: Run the tests — expect PASS**

Run: `go test ./internal/share/... -run "TestListSharedAlbumIDs|TestCountSharedMediaByScope" -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/share/repo.go internal/share/repo_test.go
git commit -m "share.Repo: add ListSharedAlbumIDs + CountSharedMediaByScope"
```

---

## Task 7: `share.Repo.ExpandScope` (preview materializer)

**Files:**
- Modify: `internal/share/repo.go`
- Modify: `internal/share/repo_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/share/repo_test.go`:

```go
func TestExpandScopeAlbumLive(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    seedOwner(t, d, "h", "alice")
    seedOwner(t, d, "h", "bob")
    now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
    album := seedAlbumWithMedia(t, d, "h/alice", 2)
    s := makeAlbumLiveScope(t, d, "h/alice", "h/bob", album.ID, nil, now)

    repo := share.NewRepo(d.WriteDB(), d.ReadDB())
    exp, err := repo.ExpandScope(context.Background(), s.UUID)
    r.NoError(err)
    r.Equal(share.TargetAlbumLive, exp.Scope.TargetType)
    r.NotNil(exp.Album)
    r.Equal(album.ID, exp.Album.ID)
    r.Equal(2, exp.Album.ItemCount)
    r.Len(exp.MediaIDs, 2)
}

func TestExpandScopeMediaSet(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    seedOwner(t, d, "h", "alice")
    seedOwner(t, d, "h", "bob")
    now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
    m1 := seedMedia(t, d, "h/alice")
    m2 := seedMedia(t, d, "h/alice")
    s := makeMediaSetScope(t, d, "h/alice", "h/bob", nil, now, m1, m2)

    repo := share.NewRepo(d.WriteDB(), d.ReadDB())
    exp, err := repo.ExpandScope(context.Background(), s.UUID)
    r.NoError(err)
    r.Equal(share.TargetMediaSet, exp.Scope.TargetType)
    r.Nil(exp.Album)
    r.ElementsMatch([]string{m1, m2}, exp.MediaIDs)
}

func TestExpandScopeUnknownReturnsNotFound(t *testing.T) {
    d := testutil.OpenTestDB(t)
    repo := share.NewRepo(d.WriteDB(), d.ReadDB())
    _, err := repo.ExpandScope(context.Background(), "not-a-uuid")
    require.ErrorIs(t, err, errs.ErrNotFound)
}
```

- [ ] **Step 2: Run — expect FAIL**

Run: `go test ./internal/share/... -run TestExpandScope -v`
Expected: FAIL.

- [ ] **Step 3: Implement `ExpandScope`**

Append to `internal/share/repo.go`:

```go
// AlbumSummary is the minimal album view attached to ExpandedScope for
// album_live scopes. Kept here (rather than reusing album.AlbumListItem)
// to avoid a share→album dependency in this direction.
type AlbumSummary struct {
    ID        string
    Name      string
    ItemCount int
    UpdatedAt time.Time
}

// ExpandedScope is the pure materializer output used by PreviewScope.
// MediaIDs is always populated: frozen membership for media_set, live
// album_media order for album_live. Album is non-nil iff target_type
// == album_live.
type ExpandedScope struct {
    Scope    Scope
    MediaIDs []string
    Album    *AlbumSummary
}

// ExpandScope reads a scope and its materialised membership without
// any grantee-identity plumbing. Callers must have already performed
// the owner-scoped auth check (see service.ShareService.PreviewScope).
func (r *Repo) ExpandScope(ctx context.Context, scopeUUID string) (ExpandedScope, error) {
    detail, err := r.GetByUUID(ctx, scopeUUID)
    if err != nil {
        return ExpandedScope{}, err
    }
    exp := ExpandedScope{Scope: detail.Scope}
    switch detail.TargetType {
    case TargetMediaSet:
        exp.MediaIDs = append([]string(nil), detail.MediaIDs...)
    case TargetAlbumLive:
        if detail.TargetAlbumID == nil {
            return ExpandedScope{}, fmt.Errorf("album_live scope %s has no target_album_id", scopeUUID)
        }
        mediaIDs, err := r.listAlbumMediaIDs(ctx, *detail.TargetAlbumID)
        if err != nil {
            return ExpandedScope{}, err
        }
        exp.MediaIDs = mediaIDs
        summary, err := r.albumSummary(ctx, *detail.TargetAlbumID)
        if err != nil {
            return ExpandedScope{}, err
        }
        exp.Album = &summary
    default:
        return ExpandedScope{}, fmt.Errorf("unknown target_type %q", string(detail.TargetType))
    }
    return exp, nil
}

// listAlbumMediaIDs returns album_media rows ordered by added_at DESC,
// media_id ASC — the same order the owner UI uses.
func (r *Repo) listAlbumMediaIDs(ctx context.Context, albumID string) ([]string, error) {
    rows, err := r.ro.QueryContext(ctx,
        `SELECT media_id FROM album_media
          WHERE album_id = ?
          ORDER BY added_at DESC, media_id ASC`, albumID)
    if err != nil {
        return nil, fmt.Errorf("list album media ids: %w", err)
    }
    defer rows.Close()
    out := make([]string, 0, 16)
    for rows.Next() {
        var id string
        if err := rows.Scan(&id); err != nil {
            return nil, fmt.Errorf("scan album media id: %w", err)
        }
        out = append(out, id)
    }
    return out, rows.Err()
}

// albumSummary reads just the name / updated_at / item_count for one
// album. Returns errs.ErrNotFound when the album row is missing.
func (r *Repo) albumSummary(ctx context.Context, albumID string) (AlbumSummary, error) {
    var s AlbumSummary
    err := r.ro.QueryRowContext(ctx,
        `SELECT a.id, a.name, a.updated_at,
                (SELECT COUNT(*) FROM album_media am WHERE am.album_id = a.id)
           FROM albums a WHERE a.id = ?`, albumID,
    ).Scan(&s.ID, &s.Name, &s.UpdatedAt, &s.ItemCount)
    if errors.Is(err, sql.ErrNoRows) {
        return AlbumSummary{}, fmt.Errorf("%w: album id=%s", errs.ErrNotFound, albumID)
    }
    if err != nil {
        return AlbumSummary{}, fmt.Errorf("read album summary: %w", err)
    }
    return s, nil
}
```

- [ ] **Step 4: Run the tests — expect PASS**

Run: `go test ./internal/share/... -run TestExpandScope -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/share/repo.go internal/share/repo_test.go
git commit -m "share.Repo.ExpandScope: preview materializer for owner previews"
```

---

## Task 8: Order-preserving batch fetch — `media.Repo.GetByIDs` + `album.Repo.GetDetailsByIDs`

**Files:**
- Modify: `internal/media/repo.go`
- Modify: `internal/media/repo_test.go`
- Modify: `internal/album/repo.go`
- Modify: `internal/album/repo_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/media/repo_test.go`:

```go
func TestMediaGetByIDsPreservesInputOrder(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    seedOwner(t, d, "h", "alice")
    repo := media.NewRepo(d.WriteDB(), d.ReadDB())

    a := seedOneMedia(t, repo, "h/alice")
    b := seedOneMedia(t, repo, "h/alice")
    c := seedOneMedia(t, repo, "h/alice")

    got, err := repo.GetByIDs(context.Background(), []string{c, a, b})
    r.NoError(err)
    r.Len(got, 3)
    r.Equal(c, got[0].ID)
    r.Equal(a, got[1].ID)
    r.Equal(b, got[2].ID)
}

func TestMediaGetByIDsSkipsMissing(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    seedOwner(t, d, "h", "alice")
    repo := media.NewRepo(d.WriteDB(), d.ReadDB())
    a := seedOneMedia(t, repo, "h/alice")
    got, err := repo.GetByIDs(context.Background(), []string{a, "00000000-0000-0000-0000-000000000000"})
    r.NoError(err)
    r.Len(got, 1)
    r.Equal(a, got[0].ID)
}

func TestMediaGetByIDsEmptyInputReturnsNil(t *testing.T) {
    d := testutil.OpenTestDB(t)
    repo := media.NewRepo(d.WriteDB(), d.ReadDB())
    got, err := repo.GetByIDs(context.Background(), nil)
    require.NoError(t, err)
    require.Empty(t, got)
}
```

Append to `internal/album/repo_test.go`:

```go
func TestAlbumGetDetailsByIDsPreservesOrder(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    seedOwner(t, d, "h", "alice")
    repo := album.NewRepo(d.WriteDB(), d.ReadDB())

    a := seedOneAlbum(t, repo, "h/alice", "A")
    b := seedOneAlbum(t, repo, "h/alice", "B")
    c := seedOneAlbum(t, repo, "h/alice", "C")

    got, err := repo.GetDetailsByIDs(context.Background(), []string{b, c, a})
    r.NoError(err)
    r.Len(got, 3)
    r.Equal(b, got[0].ID)
    r.Equal(c, got[1].ID)
    r.Equal(a, got[2].ID)
}
```

Add thin seed helpers `seedOneMedia` / `seedOneAlbum` if they do not already exist adjacent to the other test helpers.

- [ ] **Step 2: Run — expect FAIL**

Run: `go test ./internal/media/... ./internal/album/... -run "GetByIDs|GetDetailsByIDs" -v`
Expected: FAIL.

- [ ] **Step 3: Implement `media.Repo.GetByIDs`**

Append to `internal/media/repo.go`:

```go
// GetByIDs returns media rows in the same order as ids. Missing ids are
// silently dropped from the result. Empty input returns (nil, nil)
// without querying. Order preservation uses a VALUES-CTE that carries
// the caller-supplied position.
func (r *Repo) GetByIDs(ctx context.Context, ids []string) ([]Media, error) {
    if len(ids) == 0 {
        return nil, nil
    }
    valRows := make([]string, 0, len(ids))
    args := make([]any, 0, len(ids)*2)
    for i, id := range ids {
        valRows = append(valRows, "(?, ?)")
        args = append(args, id, i)
    }
    q := `
WITH ord(id, pos) AS (VALUES ` + strings.Join(valRows, ",") + `)
SELECT ` + mediaColumns + `
  FROM media m
  JOIN ord ON ord.id = m.id
 ORDER BY ord.pos
`
    rows, err := r.ro.QueryContext(ctx, q, args...)
    if err != nil {
        return nil, fmt.Errorf("get media by ids: %w", err)
    }
    defer rows.Close()
    out := make([]Media, 0, len(ids))
    for rows.Next() {
        m, err := scanMedia(rows)
        if err != nil {
            return nil, fmt.Errorf("scan media by id: %w", err)
        }
        out = append(out, m)
    }
    return out, rows.Err()
}
```

If `mediaColumns` is not already a package constant, extract the column list used by `scanMedia` into one. Keep it adjacent to `scanMedia` for readability.

- [ ] **Step 4: Implement `album.Repo.GetDetailsByIDs`**

Append to `internal/album/repo.go`:

```go
// GetDetailsByIDs returns AlbumListItem rows in the same order as ids.
// Missing ids are silently dropped. Uses the same count-and-cover
// subqueries as GetDetailByID; callers get the same ItemCount + Cover
// projection per album without N+1 round-trips.
func (r *Repo) GetDetailsByIDs(ctx context.Context, ids []string) ([]AlbumListItem, error) {
    if len(ids) == 0 {
        return nil, nil
    }
    valRows := make([]string, 0, len(ids))
    args := make([]any, 0, len(ids)*2)
    for i, id := range ids {
        valRows = append(valRows, "(?, ?)")
        args = append(args, id, i)
    }
    q := `
WITH ord(id, pos) AS (VALUES ` + strings.Join(valRows, ",") + `)
SELECT a.id, a.owner_hub, a.owner_user_id, a.name, a.created_at, a.updated_at,
       (SELECT COUNT(*) FROM album_media am WHERE am.album_id = a.id) AS item_count,
       cv.media_id, cv.thumb_version
  FROM ord
  JOIN albums a ON a.id = ord.id
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
 ORDER BY ord.pos
`
    rows, err := r.ro.QueryContext(ctx, q, args...)
    if err != nil {
        return nil, fmt.Errorf("get album details by ids: %w", err)
    }
    defer rows.Close()
    out := make([]AlbumListItem, 0, len(ids))
    for rows.Next() {
        item, err := scanAlbumListItem(rows)
        if err != nil {
            return nil, fmt.Errorf("scan album detail: %w", err)
        }
        out = append(out, item)
    }
    return out, rows.Err()
}
```

- [ ] **Step 5: Run tests — expect PASS**

Run: `go test ./internal/media/... ./internal/album/... -run "GetByIDs|GetDetailsByIDs" -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/media/repo.go internal/media/repo_test.go internal/album/repo.go internal/album/repo_test.go
git commit -m "media.Repo.GetByIDs + album.Repo.GetDetailsByIDs (order-preserving)"
```

---

## Task 9: `share.PrincipalDisplayRepo`

**Files:**
- Create: `internal/share/display.go`
- Create: `internal/share/display_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/share/display_test.go`:

```go
package share_test

import (
    "context"
    "testing"
    "time"

    "github.com/stretchr/testify/require"

    "github.com/wesm/fotobank/internal/identity"
    "github.com/wesm/fotobank/internal/owners"
    "github.com/wesm/fotobank/internal/share"
    "github.com/wesm/fotobank/internal/testutil"
)

func TestPrincipalDisplayUpsertAndGet(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    repo := share.NewPrincipalDisplayRepo(d.WriteDB(), d.ReadDB())

    now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
    err := repo.Upsert(context.Background(),
        identity.Principal{Hub: "h", UserID: "alice", Handle: "Alice"}, now)
    r.NoError(err)

    handle, ok, err := repo.Get(context.Background(),
        owners.Principal{Hub: "h", UserID: "alice"})
    r.NoError(err)
    r.True(ok)
    r.Equal("Alice", handle)
}

func TestPrincipalDisplayUpsertKeepsNewest(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    repo := share.NewPrincipalDisplayRepo(d.WriteDB(), d.ReadDB())

    t1 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
    t2 := t1.Add(time.Hour)
    r.NoError(repo.Upsert(context.Background(),
        identity.Principal{Hub: "h", UserID: "alice", Handle: "Old"}, t2))
    // Earlier cached_at must not overwrite the newer row.
    r.NoError(repo.Upsert(context.Background(),
        identity.Principal{Hub: "h", UserID: "alice", Handle: "Stale"}, t1))

    handle, ok, err := repo.Get(context.Background(),
        owners.Principal{Hub: "h", UserID: "alice"})
    r.NoError(err)
    r.True(ok)
    r.Equal("Old", handle)
}

func TestPrincipalDisplayGetMissingReturnsFalse(t *testing.T) {
    d := testutil.OpenTestDB(t)
    repo := share.NewPrincipalDisplayRepo(d.WriteDB(), d.ReadDB())
    _, ok, err := repo.Get(context.Background(),
        owners.Principal{Hub: "h", UserID: "ghost"})
    require.NoError(t, err)
    require.False(t, ok)
}

func TestPrincipalDisplayGetBatchReturnsKnown(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    repo := share.NewPrincipalDisplayRepo(d.WriteDB(), d.ReadDB())
    now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
    r.NoError(repo.Upsert(context.Background(),
        identity.Principal{Hub: "h", UserID: "alice", Handle: "Alice"}, now))
    r.NoError(repo.Upsert(context.Background(),
        identity.Principal{Hub: "h", UserID: "bob", Handle: "Bob"}, now))

    got, err := repo.GetBatch(context.Background(), []owners.Principal{
        {Hub: "h", UserID: "alice"},
        {Hub: "h", UserID: "ghost"},
        {Hub: "h", UserID: "bob"},
    })
    r.NoError(err)
    r.Equal("Alice", got[owners.Principal{Hub: "h", UserID: "alice"}])
    r.Equal("Bob", got[owners.Principal{Hub: "h", UserID: "bob"}])
    _, ok := got[owners.Principal{Hub: "h", UserID: "ghost"}]
    r.False(ok)
}
```

- [ ] **Step 2: Run — expect FAIL**

Run: `go test ./internal/share/... -run TestPrincipalDisplay -v`
Expected: FAIL.

- [ ] **Step 3: Implement `PrincipalDisplayRepo`**

Create `internal/share/display.go`:

```go
package share

import (
    "context"
    "database/sql"
    "fmt"
    "strings"
    "time"

    "github.com/wesm/fotobank/internal/identity"
    "github.com/wesm/fotobank/internal/owners"
)

// PrincipalDisplayRepo reads and writes the principal_display cache
// table. The table maps (hub, user_id) to the human-readable handle
// observed on the last request that carried one. Its rows are
// best-effort: missing / stale entries return empty handles rather
// than errors.
type PrincipalDisplayRepo struct {
    rw, ro *sql.DB
}

// NewPrincipalDisplayRepo constructs the repo.
func NewPrincipalDisplayRepo(rw, ro *sql.DB) *PrincipalDisplayRepo {
    return &PrincipalDisplayRepo{rw: rw, ro: ro}
}

// Upsert writes or updates the cached handle for p. The conflict-aware
// predicate keeps the row with the newest cached_at; callers that see
// a stale `now` will no-op against a fresher row.
func (r *PrincipalDisplayRepo) Upsert(ctx context.Context, p identity.Principal, now time.Time) error {
    const q = `
INSERT INTO principal_display (hub, user_id, handle, cached_at)
VALUES (?, ?, ?, ?)
ON CONFLICT (hub, user_id) DO UPDATE SET
    handle = excluded.handle,
    cached_at = excluded.cached_at
  WHERE principal_display.cached_at < excluded.cached_at;
`
    if _, err := r.rw.ExecContext(ctx, q, p.Hub, p.UserID, p.Handle, now); err != nil {
        return fmt.Errorf("upsert principal_display: %w", err)
    }
    return nil
}

// Get reads the cached handle for owner; returns ("", false, nil) when
// no row exists and (handle, true, nil) otherwise. A row with handle
// stored as NULL or "" returns ("", true, nil) — ok=true to keep the
// caller from upserting over an intentionally cleared row.
func (r *PrincipalDisplayRepo) Get(ctx context.Context, owner owners.Principal) (string, bool, error) {
    var handle sql.NullString
    err := r.ro.QueryRowContext(ctx,
        `SELECT handle FROM principal_display WHERE hub = ? AND user_id = ?`,
        owner.Hub, owner.UserID).Scan(&handle)
    if err == sql.ErrNoRows {
        return "", false, nil
    }
    if err != nil {
        return "", false, fmt.Errorf("get principal_display: %w", err)
    }
    if !handle.Valid {
        return "", true, nil
    }
    return handle.String, true, nil
}

// GetBatch fetches handles for many principals in one query. Missing
// principals are absent from the returned map; present-but-null handles
// map to "".
func (r *PrincipalDisplayRepo) GetBatch(ctx context.Context, principals []owners.Principal) (map[owners.Principal]string, error) {
    out := make(map[owners.Principal]string, len(principals))
    if len(principals) == 0 {
        return out, nil
    }
    pairs := make([]string, 0, len(principals))
    args := make([]any, 0, len(principals)*2)
    for _, p := range principals {
        pairs = append(pairs, "(?, ?)")
        args = append(args, p.Hub, p.UserID)
    }
    q := `
WITH want(hub, user_id) AS (VALUES ` + strings.Join(pairs, ",") + `)
SELECT d.hub, d.user_id, d.handle
  FROM want
  JOIN principal_display d ON d.hub = want.hub AND d.user_id = want.user_id
`
    rows, err := r.ro.QueryContext(ctx, q, args...)
    if err != nil {
        return nil, fmt.Errorf("batch principal_display: %w", err)
    }
    defer rows.Close()
    for rows.Next() {
        var (
            hub, user string
            handle    sql.NullString
        )
        if err := rows.Scan(&hub, &user, &handle); err != nil {
            return nil, fmt.Errorf("scan principal_display: %w", err)
        }
        key := owners.Principal{Hub: hub, UserID: user}
        if handle.Valid {
            out[key] = handle.String
        } else {
            out[key] = ""
        }
    }
    return out, rows.Err()
}
```

- [ ] **Step 4: Run — expect PASS**

Run: `go test ./internal/share/... -run TestPrincipalDisplay -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/share/display.go internal/share/display_test.go
git commit -m "share.PrincipalDisplayRepo: Upsert/Get/GetBatch against principal_display"
```

---

## Task 10: `identity.NewStubWithScopes` (test-only)

**Files:**
- Modify: `internal/identity/stub.go`
- Modify: `internal/identity/stub_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/identity/stub_test.go`:

```go
func TestStubWithScopesCarriesScopesOnEveryRequest(t *testing.T) {
    r := require.New(t)
    p := owners.Principal{Hub: "h", UserID: "bob"}
    stub := identity.NewStubWithScopes(p, "Bob", []string{"scope-a", "scope-b"})

    req := httptest.NewRequest(http.MethodGet, "/api/v1/shared/scopes", nil)
    id, err := stub.FromRequest(context.Background(), req)
    r.NoError(err)
    r.Equal("bob", id.Principal.UserID)
    r.Equal([]string{"scope-a", "scope-b"}, id.Scopes)

    // Second request must carry the same scopes (not mutated on read).
    id2, err := stub.FromRequest(context.Background(), req)
    r.NoError(err)
    r.Equal([]string{"scope-a", "scope-b"}, id2.Scopes)
}
```

Imports to add: `"net/http/httptest"`, `"github.com/wesm/fotobank/internal/identity"`, `"github.com/wesm/fotobank/internal/owners"`, `"context"`.

- [ ] **Step 2: Run — expect FAIL**

Run: `go test ./internal/identity/... -run TestStubWithScopes -v`
Expected: FAIL.

- [ ] **Step 3: Extend `Stub`**

Modify `internal/identity/stub.go`:

```go
// Stub is a Provider that returns a preconfigured Principal for every
// request. It is intended for local development and tests where real
// authentication is out of scope.
type Stub struct {
    principal Principal
    scopes    []string
}

// NewStub constructs a Stub that always returns the given owners.Principal
// with the supplied display handle.
func NewStub(p owners.Principal, handle string) *Stub {
    return &Stub{principal: Principal{Hub: p.Hub, UserID: p.UserID, Handle: handle}}
}

// NewStubWithScopes is a test-only constructor: the returned stub
// annotates every Identity with a fixed Scopes slice, letting handler
// tests exercise /api/v1/shared/* without spinning up the full
// header-mode chain. Production wiring never uses this — fotobank
// server only calls NewStub or identity.Header.
func NewStubWithScopes(p owners.Principal, handle string, scopes []string) *Stub {
    dup := append([]string(nil), scopes...)
    return &Stub{
        principal: Principal{Hub: p.Hub, UserID: p.UserID, Handle: handle},
        scopes:    dup,
    }
}

// FromRequest returns the configured principal and (if set) scopes
// without inspecting the request.
func (s *Stub) FromRequest(_ context.Context, _ *http.Request) (Identity, error) {
    var scopes []string
    if len(s.scopes) > 0 {
        scopes = append([]string(nil), s.scopes...)
    }
    return Identity{Principal: s.principal, Scopes: scopes}, nil
}
```

The copy on both construction and return is deliberate: tests that mutate the returned `Identity.Scopes` must not perturb the stub for subsequent requests.

- [ ] **Step 4: Run — expect PASS**

Run: `go test ./internal/identity/... -run TestStubWithScopes -v`
Expected: PASS.

- [ ] **Step 5: Run the full identity package to catch regressions**

Run: `go test ./internal/identity/... -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/identity/stub.go internal/identity/stub_test.go
git commit -m "identity.Stub: add NewStubWithScopes for handler tests"
```

---

## Task 11: `SharedReadService` skeleton + `ListScopes` + `GetScope`

**Files:**
- Create: `internal/service/shared_read_service.go`
- Create: `internal/service/shared_read_service_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/service/shared_read_service_test.go`:

```go
package service_test

import (
    "context"
    "testing"
    "time"

    "github.com/stretchr/testify/require"

    "github.com/wesm/fotobank/internal/album"
    "github.com/wesm/fotobank/internal/errs"
    "github.com/wesm/fotobank/internal/media"
    "github.com/wesm/fotobank/internal/owners"
    "github.com/wesm/fotobank/internal/service"
    "github.com/wesm/fotobank/internal/share"
    "github.com/wesm/fotobank/internal/storage"
    "github.com/wesm/fotobank/internal/testutil"
)

// sharedReadFixture is the collaborator bundle for SharedReadService
// tests. Keep it in one place so every TestSharedRead* uses the same
// wiring and seeding functions.
type sharedReadFixture struct {
    t        *testing.T
    db       *testutil.TestDB
    shares   *share.Repo
    mediaR   *media.Repo
    albumsR  *album.Repo
    store    storage.Store
    resolver *share.ScopeResolver
    svc      *service.SharedReadService
    now      time.Time
}

func newSharedReadFixture(t *testing.T) sharedReadFixture {
    t.Helper()
    d := testutil.OpenTestDB(t)
    shares := share.NewRepo(d.WriteDB(), d.ReadDB())
    mRepo := media.NewRepo(d.WriteDB(), d.ReadDB())
    aRepo := album.NewRepo(d.WriteDB(), d.ReadDB())
    store := testutil.NewMemStore(t)
    now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
    resolver := share.NewScopeResolver(shares, func() time.Time { return now })
    svc := service.NewSharedReadService(shares, mRepo, aRepo, store, resolver)
    return sharedReadFixture{
        t: t, db: d, shares: shares, mediaR: mRepo, albumsR: aRepo,
        store: store, resolver: resolver, svc: svc, now: now,
    }
}

func TestSharedReadListScopesReturnsAuthorizedOnly(t *testing.T) {
    r := require.New(t)
    fx := newSharedReadFixture(t)
    seedOwner(t, fx.db, "h", "alice")
    seedOwner(t, fx.db, "h", "bob")

    m := seedMedia(t, fx.db, "h/alice")
    live := makeMediaSetScope(t, fx.db, "h/alice", "h/bob", nil, fx.now, m)
    bumpActive(t, fx.db, live.UUID, fx.now)
    // un-granted scope that should not appear:
    other := makeMediaSetScope(t, fx.db, "h/alice", "h/charlie", nil, fx.now, m)
    bumpActive(t, fx.db, other.UUID, fx.now)

    got, err := fx.svc.ListScopes(context.Background(),
        owners.Principal{Hub: "h", UserID: "bob"},
        []string{live.UUID, other.UUID})
    r.NoError(err)
    r.Len(got, 1)
    r.Equal(live.UUID, got[0].UUID)
    r.Equal(1, got[0].ItemCount)
}

func TestSharedReadGetScopeEnforcesHeaderMembership(t *testing.T) {
    r := require.New(t)
    fx := newSharedReadFixture(t)
    seedOwner(t, fx.db, "h", "alice")
    seedOwner(t, fx.db, "h", "bob")

    m := seedMedia(t, fx.db, "h/alice")
    live := makeMediaSetScope(t, fx.db, "h/alice", "h/bob", nil, fx.now, m)
    bumpActive(t, fx.db, live.UUID, fx.now)
    ghost := makeMediaSetScope(t, fx.db, "h/alice", "h/bob", nil, fx.now, m)
    bumpActive(t, fx.db, ghost.UUID, fx.now)

    // Only `live` is in the header; ghost must not be fetchable.
    _, err := fx.svc.GetScope(context.Background(),
        owners.Principal{Hub: "h", UserID: "bob"},
        []string{live.UUID}, ghost.UUID)
    require.ErrorIs(t, err, errs.ErrNotFound)

    got, err := fx.svc.GetScope(context.Background(),
        owners.Principal{Hub: "h", UserID: "bob"},
        []string{live.UUID}, live.UUID)
    r.NoError(err)
    r.Equal(live.UUID, got.UUID)
    r.ElementsMatch([]string{m}, got.MediaIDs)
}

func TestSharedReadGetScopeRevokedReturnsNotFound(t *testing.T) {
    fx := newSharedReadFixture(t)
    seedOwner(t, fx.db, "h", "alice")
    seedOwner(t, fx.db, "h", "bob")
    m := seedMedia(t, fx.db, "h/alice")
    live := makeMediaSetScope(t, fx.db, "h/alice", "h/bob", nil, fx.now, m)
    bumpActive(t, fx.db, live.UUID, fx.now)
    _, err := fx.shares.SetRevoking(context.Background(), live.UUID, fx.now)
    require.NoError(t, err)

    _, err = fx.svc.GetScope(context.Background(),
        owners.Principal{Hub: "h", UserID: "bob"},
        []string{live.UUID}, live.UUID)
    require.ErrorIs(t, err, errs.ErrNotFound)
}
```

`testutil.NewMemStore` is the in-memory `storage.Store` double; if it does not yet exist, add it to `internal/testutil` as a minimal map-backed implementation of the four Store methods. (Plan C established the pattern.)

- [ ] **Step 2: Run — expect FAIL**

Run: `go test ./internal/service/... -run TestSharedRead -v`
Expected: FAIL (SharedReadService undefined).

- [ ] **Step 3: Implement skeleton + ListScopes + GetScope**

Create `internal/service/shared_read_service.go`:

```go
package service

import (
    "context"
    "fmt"
    "sort"
    "time"

    "github.com/wesm/fotobank/internal/album"
    "github.com/wesm/fotobank/internal/errs"
    "github.com/wesm/fotobank/internal/media"
    "github.com/wesm/fotobank/internal/owners"
    "github.com/wesm/fotobank/internal/share"
    "github.com/wesm/fotobank/internal/storage"
)

// SharedReadService is the auth boundary for every /api/v1/shared/*
// read. It depends on the resolver for authorisation and on the same
// repos the owner services use for data — but it never calls the owner
// services, which enforce caller == owner.
type SharedReadService struct {
    shares   *share.Repo
    media    *media.Repo
    albums   *album.Repo
    storage  storage.Store
    resolver *share.ScopeResolver
    now      func() time.Time
}

// NewSharedReadService constructs a SharedReadService with prod
// defaults.
func NewSharedReadService(
    shares *share.Repo,
    m *media.Repo,
    a *album.Repo,
    s storage.Store,
    r *share.ScopeResolver,
) *SharedReadService {
    return &SharedReadService{
        shares: shares, media: m, albums: a, storage: s, resolver: r,
        now: func() time.Time { return time.Now().UTC() },
    }
}

// SharedScope is the caller's view of a scope granted to them.
type SharedScope struct {
    UUID          string
    TargetType    share.TargetType
    Owner         owners.Principal
    TargetAlbumID *string
    AllowDownload bool
    Label         string
    ExpiresAt     *time.Time
    ItemCount     int
}

// SharedScopeDetail is a SharedScope plus the media_set membership.
// MediaIDs is empty for album_live.
type SharedScopeDetail struct {
    SharedScope
    MediaIDs []string
}

// ListScopes returns the live, grantee-matched scopes carried in
// headerScopes. ItemCount is populated via CountSharedMediaByScope.
func (s *SharedReadService) ListScopes(
    ctx context.Context,
    caller owners.Principal,
    headerScopes []string,
) ([]SharedScope, error) {
    resolved, err := s.resolver.ResolveAll(ctx, caller, headerScopes)
    if err != nil {
        return nil, err
    }
    if len(resolved.Validated) == 0 {
        return nil, nil
    }
    out := make([]SharedScope, 0, len(resolved.Validated))
    // Sort validated by UUID ascending for stable response order.
    sort.Slice(resolved.Validated, func(i, j int) bool {
        return resolved.Validated[i].UUID < resolved.Validated[j].UUID
    })
    for _, sc := range resolved.Validated {
        n, err := s.shares.CountSharedMediaByScope(ctx, sc.UUID)
        if err != nil {
            return nil, fmt.Errorf("count shared scope %s: %w", sc.UUID, err)
        }
        out = append(out, toSharedScope(sc, n))
    }
    return out, nil
}

// GetScope returns a single scope by UUID. The UUID must be present in
// headerScopes — a caller who has two scopes from the same owner cannot
// probe for a third by UUID.
func (s *SharedReadService) GetScope(
    ctx context.Context,
    caller owners.Principal,
    headerScopes []string,
    uuid string,
) (SharedScopeDetail, error) {
    // Presence check: the UUID must be in the caller-presented set. A
    // header that doesn't list the UUID is treated as 404 regardless of
    // whether the row exists.
    presented := false
    for _, u := range headerScopes {
        if u == uuid {
            presented = true
            break
        }
    }
    if !presented {
        return SharedScopeDetail{}, fmt.Errorf("%w: scope uuid=%s", errs.ErrNotFound, uuid)
    }
    resolved, err := s.resolver.ResolveAll(ctx, caller, headerScopes)
    if err != nil {
        return SharedScopeDetail{}, err
    }
    var match *share.Scope
    for i := range resolved.Validated {
        if resolved.Validated[i].UUID == uuid {
            match = &resolved.Validated[i]
            break
        }
    }
    if match == nil {
        return SharedScopeDetail{}, fmt.Errorf("%w: scope uuid=%s", errs.ErrNotFound, uuid)
    }
    detail, err := s.shares.GetByUUID(ctx, uuid)
    if err != nil {
        return SharedScopeDetail{}, err
    }
    out := SharedScopeDetail{SharedScope: toSharedScope(*match, len(detail.MediaIDs))}
    if match.TargetType == share.TargetMediaSet {
        out.MediaIDs = append([]string(nil), detail.MediaIDs...)
    }
    return out, nil
}

// toSharedScope projects a share.Scope + count into the grantee view.
func toSharedScope(s share.Scope, itemCount int) SharedScope {
    return SharedScope{
        UUID:          s.UUID,
        TargetType:    s.TargetType,
        Owner:         s.Owner,
        TargetAlbumID: s.TargetAlbumID,
        AllowDownload: s.AllowDownload,
        Label:         s.Label,
        ExpiresAt:     s.ExpiresAt,
        ItemCount:     itemCount,
    }
}
```

- [ ] **Step 4: Run — expect PASS**

Run: `go test ./internal/service/... -run TestSharedRead -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/service/shared_read_service.go internal/service/shared_read_service_test.go internal/testutil/
git commit -m "service.SharedReadService: skeleton + ListScopes + GetScope"
```

---

## Task 12: `SharedReadService.ListAlbums` + `GetAlbum` + `ListAlbumMedia`

**Files:**
- Modify: `internal/service/shared_read_service.go`
- Modify: `internal/service/shared_read_service_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/service/shared_read_service_test.go`:

```go
func TestSharedReadListAlbumsReturnsAlbumLiveOnly(t *testing.T) {
    r := require.New(t)
    fx := newSharedReadFixture(t)
    seedOwner(t, fx.db, "h", "alice")
    seedOwner(t, fx.db, "h", "bob")

    album := seedAlbumWithMedia(t, fx.db, "h/alice", 2)
    live := makeAlbumLiveScope(t, fx.db, "h/alice", "h/bob", album.ID, nil, fx.now)
    bumpActive(t, fx.db, live.UUID, fx.now)
    // media_set: must not surface as an album.
    ms := makeMediaSetScope(t, fx.db, "h/alice", "h/bob", nil, fx.now, album.Media...)
    bumpActive(t, fx.db, ms.UUID, fx.now)

    got, err := fx.svc.ListAlbums(context.Background(),
        owners.Principal{Hub: "h", UserID: "bob"}, []string{live.UUID, ms.UUID})
    r.NoError(err)
    r.Len(got, 1)
    r.Equal(album.ID, got[0].ID)
    r.Equal(2, got[0].ItemCount)
}

func TestSharedReadGetAlbumUnauthorizedReturns404(t *testing.T) {
    fx := newSharedReadFixture(t)
    seedOwner(t, fx.db, "h", "alice")
    seedOwner(t, fx.db, "h", "bob")
    album := seedAlbumWithMedia(t, fx.db, "h/alice", 1)
    _, err := fx.svc.GetAlbum(context.Background(),
        owners.Principal{Hub: "h", UserID: "bob"}, nil, album.ID)
    require.ErrorIs(t, err, errs.ErrNotFound)
}

func TestSharedReadListAlbumMediaPaginates(t *testing.T) {
    r := require.New(t)
    fx := newSharedReadFixture(t)
    seedOwner(t, fx.db, "h", "alice")
    seedOwner(t, fx.db, "h", "bob")
    album := seedAlbumWithMediaTimestamped(t, fx.db, "h/alice", fx.now, 3)
    live := makeAlbumLiveScope(t, fx.db, "h/alice", "h/bob", album.ID, nil, fx.now)
    bumpActive(t, fx.db, live.UUID, fx.now)

    page, nextCursor, err := fx.svc.ListAlbumMedia(context.Background(),
        owners.Principal{Hub: "h", UserID: "bob"},
        []string{live.UUID}, album.ID,
        service.SharedMediaCursor{Limit: 2})
    r.NoError(err)
    r.Len(page, 2)
    r.NotZero(nextCursor.AfterID)

    page2, nextCursor2, err := fx.svc.ListAlbumMedia(context.Background(),
        owners.Principal{Hub: "h", UserID: "bob"},
        []string{live.UUID}, album.ID, nextCursor)
    r.NoError(err)
    r.Len(page2, 1)
    r.Zero(nextCursor2.AfterID) // exhausted
}
```

- [ ] **Step 2: Run — expect FAIL**

Run: `go test ./internal/service/... -run "TestSharedReadListAlbums|TestSharedReadGetAlbum|TestSharedReadListAlbumMedia" -v`
Expected: FAIL.

- [ ] **Step 3: Implement the three methods + shared DTOs/cursor**

Append to `internal/service/shared_read_service.go`:

```go
// SharedAlbumCover is the minimum a shared client needs to render
// /api/v1/shared/media/{id}/thumb. Cover existence implies a ready
// thumb.
type SharedAlbumCover struct {
    MediaID      string
    ThumbVersion int
}

// SharedAlbum is the grantee view of one album.
type SharedAlbum struct {
    ID          string
    Name        string
    Owner       owners.Principal
    ItemCount   int
    Cover       *SharedAlbumCover
    CreatedAt   time.Time
    UpdatedAt   time.Time
    CanDownload bool
}

// SharedAlbumDetail mirrors SharedAlbum for now; kept distinct so the
// detail endpoint can add fields without widening the listing body.
type SharedAlbumDetail struct {
    SharedAlbum
}

// SharedMedia is the grantee view of one media row.
type SharedMedia struct {
    ID           string
    Owner        owners.Principal
    MediaType    media.Type
    MimeType     string
    DisplayTime  time.Time
    Width        *int
    Height       *int
    DurationMs   *int64
    ThumbStatus  string
    ThumbVersion int
    CanDownload  bool
}

// SharedMediaCursor paginates ListMedia / ListAlbumMedia.
type SharedMediaCursor struct {
    AfterDisplayTime time.Time
    AfterID          string
    Limit            int
}

const (
    sharedMediaDefaultLimit = 100
    sharedMediaMaxLimit     = 500
)

// clampSharedMediaLimit applies the default+max policy.
func clampSharedMediaLimit(n int) int {
    if n <= 0 {
        return sharedMediaDefaultLimit
    }
    if n > sharedMediaMaxLimit {
        return sharedMediaMaxLimit
    }
    return n
}

// ListAlbums returns distinct albums authorised by the album_live
// subset of headerScopes.
func (s *SharedReadService) ListAlbums(
    ctx context.Context,
    caller owners.Principal,
    headerScopes []string,
) ([]SharedAlbum, error) {
    resolved, err := s.resolver.ResolveAll(ctx, caller, headerScopes)
    if err != nil {
        return nil, err
    }
    rows, err := s.shares.ListSharedAlbumIDs(ctx, resolved.Validated, resolved.Owner)
    if err != nil {
        return nil, err
    }
    if len(rows) == 0 {
        return nil, nil
    }
    perm := make(map[string]bool, len(rows))
    ids := make([]string, 0, len(rows))
    for _, row := range rows {
        perm[row.AlbumID] = row.CanDownload
        ids = append(ids, row.AlbumID)
    }
    details, err := s.albums.GetDetailsByIDs(ctx, ids)
    if err != nil {
        return nil, err
    }
    // Sort details by updated_at DESC, id ASC for a stable listing order.
    sort.Slice(details, func(i, j int) bool {
        if !details[i].UpdatedAt.Equal(details[j].UpdatedAt) {
            return details[i].UpdatedAt.After(details[j].UpdatedAt)
        }
        return details[i].ID < details[j].ID
    })
    out := make([]SharedAlbum, 0, len(details))
    for _, a := range details {
        out = append(out, toSharedAlbum(a, perm[a.ID]))
    }
    return out, nil
}

// GetAlbum returns album metadata iff a live album_live scope in
// headerScopes points at albumID.
func (s *SharedReadService) GetAlbum(
    ctx context.Context,
    caller owners.Principal,
    headerScopes []string,
    albumID string,
) (SharedAlbumDetail, error) {
    dec, err := s.resolver.CheckAlbumAccess(ctx, caller, headerScopes, albumID)
    if err != nil {
        return SharedAlbumDetail{}, err
    }
    if !dec.Authorized {
        return SharedAlbumDetail{}, fmt.Errorf("%w: album id=%s", errs.ErrNotFound, albumID)
    }
    detail, err := s.albums.GetDetailByID(ctx, albumID)
    if err != nil {
        return SharedAlbumDetail{}, err
    }
    return SharedAlbumDetail{SharedAlbum: toSharedAlbum(detail, dec.CanDownload())}, nil
}

// ListAlbumMedia returns one page of an album's media, honouring the
// album's AccessDecision for CanDownload and the per-media OR with any
// covering media_set scopes.
func (s *SharedReadService) ListAlbumMedia(
    ctx context.Context,
    caller owners.Principal,
    headerScopes []string,
    albumID string,
    cursor SharedMediaCursor,
) ([]SharedMedia, SharedMediaCursor, error) {
    dec, err := s.resolver.CheckAlbumAccess(ctx, caller, headerScopes, albumID)
    if err != nil {
        return nil, SharedMediaCursor{}, err
    }
    if !dec.Authorized {
        return nil, SharedMediaCursor{}, fmt.Errorf("%w: album id=%s", errs.ErrNotFound, albumID)
    }
    resolved, err := s.resolver.ResolveAll(ctx, caller, headerScopes)
    if err != nil {
        return nil, SharedMediaCursor{}, err
    }
    limit := clampSharedMediaLimit(cursor.Limit)
    repoRows, err := s.shares.ListSharedMediaIDs(ctx, resolved.Validated, resolved.Owner, albumID,
        share.SharedMediaCursor{
            AfterDisplayTime: cursor.AfterDisplayTime,
            AfterID:          cursor.AfterID,
            Limit:            limit + 1,
        })
    if err != nil {
        return nil, SharedMediaCursor{}, err
    }
    page, next := pageSharedMediaRows(repoRows, limit)
    medias, err := s.fetchSharedMediaByRows(ctx, page)
    if err != nil {
        return nil, SharedMediaCursor{}, err
    }
    return medias, next, nil
}

// pageSharedMediaRows splits repo rows into (page, next-cursor) using
// the limit+1 convention.
func pageSharedMediaRows(rows []share.SharedMediaRow, limit int) ([]share.SharedMediaRow, SharedMediaCursor) {
    if len(rows) <= limit {
        return rows, SharedMediaCursor{}
    }
    last := rows[limit-1]
    return rows[:limit], SharedMediaCursor{
        AfterDisplayTime: last.DisplayTime,
        AfterID:          last.MediaID,
        Limit:            limit,
    }
}

// fetchSharedMediaByRows expands the repo rows into SharedMedia,
// preserving the input order and carrying the per-row CanDownload.
func (s *SharedReadService) fetchSharedMediaByRows(ctx context.Context, rows []share.SharedMediaRow) ([]SharedMedia, error) {
    if len(rows) == 0 {
        return nil, nil
    }
    ids := make([]string, 0, len(rows))
    can := make(map[string]bool, len(rows))
    for _, row := range rows {
        ids = append(ids, row.MediaID)
        can[row.MediaID] = row.CanDownload
    }
    mediaRows, err := s.media.GetByIDs(ctx, ids)
    if err != nil {
        return nil, err
    }
    out := make([]SharedMedia, 0, len(mediaRows))
    for _, m := range mediaRows {
        out = append(out, toSharedMedia(m, can[m.ID]))
    }
    return out, nil
}

// toSharedAlbum projects album.AlbumListItem + can_download into the
// shared view. ItemCount and Cover come straight from the detail row.
func toSharedAlbum(a album.AlbumListItem, canDownload bool) SharedAlbum {
    out := SharedAlbum{
        ID:          a.ID,
        Name:        a.Name,
        Owner:       a.Owner,
        ItemCount:   a.ItemCount,
        CreatedAt:   a.CreatedAt,
        UpdatedAt:   a.UpdatedAt,
        CanDownload: canDownload,
    }
    if a.Cover != nil {
        out.Cover = &SharedAlbumCover{
            MediaID:      a.Cover.MediaID,
            ThumbVersion: a.Cover.ThumbVersion,
        }
    }
    return out
}

// toSharedMedia projects media.Media + can_download into the grantee
// view. display_time = COALESCE(timestamp, imported_at).
func toSharedMedia(m media.Media, canDownload bool) SharedMedia {
    display := m.ImportedAt
    if m.Timestamp != nil {
        display = *m.Timestamp
    }
    return SharedMedia{
        ID:           m.ID,
        Owner:        m.Owner,
        MediaType:    m.Type,
        MimeType:     m.MimeType,
        DisplayTime:  display,
        Width:        m.Width,
        Height:       m.Height,
        DurationMs:   m.DurationMs,
        ThumbStatus:  m.ThumbStatus,
        ThumbVersion: m.ThumbVersion,
        CanDownload:  canDownload,
    }
}
```

- [ ] **Step 4: Run — expect PASS**

Run: `go test ./internal/service/... -run "TestSharedReadListAlbums|TestSharedReadGetAlbum|TestSharedReadListAlbumMedia" -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/service/shared_read_service.go internal/service/shared_read_service_test.go
git commit -m "SharedReadService: ListAlbums + GetAlbum + ListAlbumMedia + DTOs"
```

---

## Task 13: `SharedReadService.ListMedia` + `GetMedia`

**Files:**
- Modify: `internal/service/shared_read_service.go`
- Modify: `internal/service/shared_read_service_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/service/shared_read_service_test.go`:

```go
func TestSharedReadListMediaUnionOfScopes(t *testing.T) {
    r := require.New(t)
    fx := newSharedReadFixture(t)
    seedOwner(t, fx.db, "h", "alice")
    seedOwner(t, fx.db, "h", "bob")

    t0 := fx.now.Add(-2 * time.Hour)
    t1 := fx.now.Add(-1 * time.Hour)
    t2 := fx.now
    m1 := seedMediaWithTimestamp(t, fx.db, "h/alice", t0)
    m2 := seedMediaWithTimestamp(t, fx.db, "h/alice", t1)
    m3 := seedMediaWithTimestamp(t, fx.db, "h/alice", t2)

    s1 := makeMediaSetScope(t, fx.db, "h/alice", "h/bob", nil, fx.now, m1, m2)
    bumpActive(t, fx.db, s1.UUID, fx.now)
    s2 := makeMediaSetScopeWithDownload(t, fx.db, "h/alice", "h/bob", nil, fx.now, true, m2, m3)
    bumpActive(t, fx.db, s2.UUID, fx.now)

    page, next, err := fx.svc.ListMedia(context.Background(),
        owners.Principal{Hub: "h", UserID: "bob"},
        []string{s1.UUID, s2.UUID},
        service.SharedMediaCursor{Limit: 10})
    r.NoError(err)
    r.Len(page, 3)
    r.Equal(m3, page[0].ID)
    r.Equal(m2, page[1].ID)
    r.Equal(m1, page[2].ID)
    r.True(page[0].CanDownload) // s2
    r.True(page[1].CanDownload) // s2 OR s1 → true
    r.False(page[2].CanDownload)
    r.Zero(next.AfterID)
}

func TestSharedReadGetMediaUnauthorizedReturns404(t *testing.T) {
    fx := newSharedReadFixture(t)
    seedOwner(t, fx.db, "h", "alice")
    seedOwner(t, fx.db, "h", "bob")
    m := seedMedia(t, fx.db, "h/alice")
    _, err := fx.svc.GetMedia(context.Background(),
        owners.Principal{Hub: "h", UserID: "bob"}, nil, m)
    require.ErrorIs(t, err, errs.ErrNotFound)
}

func TestSharedReadGetMediaAuthorizedSetsCanDownload(t *testing.T) {
    r := require.New(t)
    fx := newSharedReadFixture(t)
    seedOwner(t, fx.db, "h", "alice")
    seedOwner(t, fx.db, "h", "bob")
    m := seedMedia(t, fx.db, "h/alice")
    s := makeMediaSetScopeWithDownload(t, fx.db, "h/alice", "h/bob", nil, fx.now, true, m)
    bumpActive(t, fx.db, s.UUID, fx.now)

    got, err := fx.svc.GetMedia(context.Background(),
        owners.Principal{Hub: "h", UserID: "bob"}, []string{s.UUID}, m)
    r.NoError(err)
    r.Equal(m, got.ID)
    r.True(got.CanDownload)
}
```

- [ ] **Step 2: Run — expect FAIL**

Run: `go test ./internal/service/... -run "TestSharedReadListMedia|TestSharedReadGetMedia" -v`
Expected: FAIL.

- [ ] **Step 3: Implement `ListMedia` + `GetMedia`**

Append to `internal/service/shared_read_service.go`:

```go
// ListMedia returns one page of every distinct media the caller can
// see across all validated scopes, cursor-paginated by display_time
// DESC + id ASC.
func (s *SharedReadService) ListMedia(
    ctx context.Context,
    caller owners.Principal,
    headerScopes []string,
    cursor SharedMediaCursor,
) ([]SharedMedia, SharedMediaCursor, error) {
    resolved, err := s.resolver.ResolveAll(ctx, caller, headerScopes)
    if err != nil {
        return nil, SharedMediaCursor{}, err
    }
    if len(resolved.Validated) == 0 {
        return nil, SharedMediaCursor{}, nil
    }
    limit := clampSharedMediaLimit(cursor.Limit)
    repoRows, err := s.shares.ListSharedMediaIDs(ctx, resolved.Validated, resolved.Owner, "",
        share.SharedMediaCursor{
            AfterDisplayTime: cursor.AfterDisplayTime,
            AfterID:          cursor.AfterID,
            Limit:            limit + 1,
        })
    if err != nil {
        return nil, SharedMediaCursor{}, err
    }
    page, next := pageSharedMediaRows(repoRows, limit)
    medias, err := s.fetchSharedMediaByRows(ctx, page)
    if err != nil {
        return nil, SharedMediaCursor{}, err
    }
    return medias, next, nil
}

// GetMedia returns one media row iff CheckMediaAccess authorises it.
func (s *SharedReadService) GetMedia(
    ctx context.Context,
    caller owners.Principal,
    headerScopes []string,
    mediaID string,
) (SharedMedia, error) {
    dec, err := s.resolver.CheckMediaAccess(ctx, caller, headerScopes, mediaID)
    if err != nil {
        return SharedMedia{}, err
    }
    if !dec.Authorized {
        return SharedMedia{}, fmt.Errorf("%w: media id=%s", errs.ErrNotFound, mediaID)
    }
    m, err := s.media.GetByID(ctx, mediaID)
    if err != nil {
        return SharedMedia{}, err
    }
    return toSharedMedia(m, dec.CanDownload()), nil
}
```

- [ ] **Step 4: Run — expect PASS**

Run: `go test ./internal/service/... -run "TestSharedReadListMedia|TestSharedReadGetMedia" -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/service/shared_read_service.go internal/service/shared_read_service_test.go
git commit -m "SharedReadService: ListMedia + GetMedia"
```

---

## Task 14: Extract `writeOriginalResponse` + `writeThumbResponse`

**Files:**
- Create: `internal/httpapi/originals.go`
- Create: `internal/httpapi/originals_test.go`
- Create: `internal/httpapi/thumbs.go`
- Create: `internal/httpapi/thumbs_test.go`
- Modify: `internal/httpapi/media_original.go`
- Modify: `internal/httpapi/media_thumb.go`

The current owner routes inline Range parsing and body copying. Extract them into helpers that own only the body-streaming semantics. Caller sets Cache-Control, Vary, Content-Type, ETag, and Last-Modified **before** invoking. Owner cache policy (`private, max-age=31536000, immutable`) is preserved; shared routes (T17) will set their own.

- [ ] **Step 1: Write failing helper tests**

Create `internal/httpapi/originals_test.go`:

```go
package httpapi

import (
    "io"
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"
    "time"

    "github.com/stretchr/testify/require"

    "github.com/wesm/fotobank/internal/media"
)

func TestWriteOriginalResponseFullBody(t *testing.T) {
    r := require.New(t)
    body := "hello world"
    w := httptest.NewRecorder()
    req := httptest.NewRequest(http.MethodGet, "/x", nil)
    m := media.Media{Size: int64(len(body)), MimeType: "text/plain",
        ImportedAt: time.Now().UTC()}
    writeOriginalResponse(w, req, m, func(off, length int64) (io.ReadCloser, error) {
        r.Equal(int64(0), off)
        r.Equal(int64(-1), length)
        return io.NopCloser(strings.NewReader(body)), nil
    })
    res := w.Result()
    defer res.Body.Close()
    r.Equal(http.StatusOK, res.StatusCode)
    got, err := io.ReadAll(res.Body)
    r.NoError(err)
    r.Equal(body, string(got))
}

func TestWriteOriginalResponseRange(t *testing.T) {
    r := require.New(t)
    body := "0123456789"
    req := httptest.NewRequest(http.MethodGet, "/x", nil)
    req.Header.Set("Range", "bytes=2-5")
    w := httptest.NewRecorder()
    m := media.Media{Size: int64(len(body)), MimeType: "text/plain",
        ImportedAt: time.Now().UTC()}
    writeOriginalResponse(w, req, m, func(off, length int64) (io.ReadCloser, error) {
        r.Equal(int64(2), off)
        r.Equal(int64(4), length)
        return io.NopCloser(strings.NewReader(body[off : off+length])), nil
    })
    res := w.Result()
    defer res.Body.Close()
    r.Equal(http.StatusPartialContent, res.StatusCode)
    r.Equal("bytes 2-5/10", res.Header.Get("Content-Range"))
    got, err := io.ReadAll(res.Body)
    r.NoError(err)
    r.Equal("2345", string(got))
}

func TestWriteOriginalResponseUnsatisfiableRange(t *testing.T) {
    req := httptest.NewRequest(http.MethodGet, "/x", nil)
    req.Header.Set("Range", "bytes=999-")
    w := httptest.NewRecorder()
    m := media.Media{Size: 10, MimeType: "text/plain", ImportedAt: time.Now().UTC()}
    writeOriginalResponse(w, req, m, func(off, length int64) (io.ReadCloser, error) {
        t.Fatal("open must not be called on unsatisfiable range")
        return nil, nil
    })
    require.Equal(t, http.StatusRequestedRangeNotSatisfiable, w.Result().StatusCode)
}
```

Create `internal/httpapi/thumbs_test.go`:

```go
package httpapi

import (
    "io"
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"

    "github.com/stretchr/testify/require"
)

func TestWriteThumbResponseFullBody(t *testing.T) {
    r := require.New(t)
    body := "PNGBYTES"
    req := httptest.NewRequest(http.MethodGet, "/x", nil)
    w := httptest.NewRecorder()
    writeThumbResponse(w, req, func() (io.ReadCloser, error) {
        return io.NopCloser(strings.NewReader(body)), nil
    })
    res := w.Result()
    defer res.Body.Close()
    r.Equal(http.StatusOK, res.StatusCode)
    got, _ := io.ReadAll(res.Body)
    r.Equal(body, string(got))
}
```

- [ ] **Step 2: Run — expect FAIL**

Run: `go test ./internal/httpapi/... -run "TestWriteOriginalResponse|TestWriteThumbResponse" -v`
Expected: FAIL.

- [ ] **Step 3: Implement `writeOriginalResponse`**

Create `internal/httpapi/originals.go`:

```go
package httpapi

import (
    "errors"
    "fmt"
    "io"
    "log/slog"
    "net/http"
    "strconv"
    "strings"

    "github.com/wesm/fotobank/internal/media"
)

// writeOriginalResponse streams the original bytes, honouring an HTTP
// Range request when present. Caller has already performed auth and
// set Cache-Control / Vary / Content-Type / ETag / Last-Modified. open
// is called exactly once after Range parsing succeeds; it returns the
// (offset, length)-sliced reader. length == -1 means "to EOF".
//
// Writes Content-Length (both 200 and 206), Content-Range (206 only),
// Accept-Ranges (unconditionally), and the response status.
func writeOriginalResponse(
    w http.ResponseWriter,
    r *http.Request,
    m media.Media,
    open func(offset, length int64) (io.ReadCloser, error),
) {
    size := m.Size
    h := w.Header()
    h.Set("Accept-Ranges", "bytes")

    offset, length, partial, err := parseRangeHeader(r.Header.Get("Range"), size)
    if errors.Is(err, errRangeUnsatisfiable) {
        h.Set("Content-Range", fmt.Sprintf("bytes */%d", size))
        http.Error(w, http.StatusText(http.StatusRequestedRangeNotSatisfiable),
            http.StatusRequestedRangeNotSatisfiable)
        return
    }
    if err != nil {
        http.Error(w, "bad range", http.StatusBadRequest)
        return
    }

    rc, err := open(offset, length)
    if err != nil {
        slog.Error("open original bytes", "err", err, "id", m.ID)
        http.Error(w, http.StatusText(http.StatusInternalServerError),
            http.StatusInternalServerError)
        return
    }
    defer func() { _ = rc.Close() }()

    if partial {
        total := length
        if total < 0 {
            total = size - offset
        }
        h.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", offset, offset+total-1, size))
        h.Set("Content-Length", strconv.FormatInt(total, 10))
        w.WriteHeader(http.StatusPartialContent)
    } else {
        h.Set("Content-Length", strconv.FormatInt(size, 10))
        w.WriteHeader(http.StatusOK)
    }
    if _, err := io.Copy(w, rc); err != nil {
        slog.Error("original stream", "err", err, "id", m.ID)
    }
}

var errRangeUnsatisfiable = errors.New("range unsatisfiable")

// parseRangeHeader handles the subset of RFC 7233 we need:
//   bytes=START-END
//   bytes=START-
//   bytes=-SUFFIX
// length == -1 means "to EOF" for the open closure. A Range header
// outside [0, size) returns errRangeUnsatisfiable; other malformed
// inputs return a generic error (mapped to 400).
func parseRangeHeader(raw string, size int64) (offset, length int64, partial bool, err error) {
    raw = strings.TrimSpace(raw)
    if raw == "" {
        return 0, -1, false, nil
    }
    if !strings.HasPrefix(raw, "bytes=") {
        return 0, 0, false, fmt.Errorf("unsupported range unit: %s", raw)
    }
    spec := strings.TrimPrefix(raw, "bytes=")
    if strings.Contains(spec, ",") {
        return 0, 0, false, fmt.Errorf("multi-range not supported")
    }
    dash := strings.IndexByte(spec, '-')
    if dash < 0 {
        return 0, 0, false, fmt.Errorf("malformed range: %s", raw)
    }
    startStr, endStr := spec[:dash], spec[dash+1:]
    switch {
    case startStr == "" && endStr == "":
        return 0, 0, false, fmt.Errorf("malformed range: %s", raw)
    case startStr == "":
        // suffix: bytes=-N
        n, err := strconv.ParseInt(endStr, 10, 64)
        if err != nil || n <= 0 {
            return 0, 0, false, fmt.Errorf("bad suffix: %s", raw)
        }
        if n > size {
            n = size
        }
        return size - n, n, true, nil
    case endStr == "":
        start, err := strconv.ParseInt(startStr, 10, 64)
        if err != nil || start < 0 {
            return 0, 0, false, fmt.Errorf("bad start: %s", raw)
        }
        if start >= size {
            return 0, 0, false, errRangeUnsatisfiable
        }
        return start, size - start, true, nil
    default:
        start, err1 := strconv.ParseInt(startStr, 10, 64)
        end, err2 := strconv.ParseInt(endStr, 10, 64)
        if err1 != nil || err2 != nil || start < 0 || end < start {
            return 0, 0, false, fmt.Errorf("bad range: %s", raw)
        }
        if start >= size {
            return 0, 0, false, errRangeUnsatisfiable
        }
        if end >= size {
            end = size - 1
        }
        return start, end - start + 1, true, nil
    }
}
```

- [ ] **Step 4: Implement `writeThumbResponse`**

Create `internal/httpapi/thumbs.go`:

```go
package httpapi

import (
    "io"
    "log/slog"
    "net/http"
)

// writeThumbResponse streams cached thumb bytes. Caller has already
// performed auth and set Cache-Control / Vary / Content-Type / ETag /
// Last-Modified. open returns the full-file reader; thumbs are small
// enough that Range is not honoured.
func writeThumbResponse(
    w http.ResponseWriter,
    _ *http.Request,
    open func() (io.ReadCloser, error),
) {
    rc, err := open()
    if err != nil {
        slog.Error("open thumb bytes", "err", err)
        http.Error(w, http.StatusText(http.StatusInternalServerError),
            http.StatusInternalServerError)
        return
    }
    defer func() { _ = rc.Close() }()
    w.WriteHeader(http.StatusOK)
    if _, err := io.Copy(w, rc); err != nil {
        slog.Error("thumb stream", "err", err)
    }
}
```

- [ ] **Step 5: Refactor the owner original handler**

Modify `internal/httpapi/media_original.go`: replace the inline Range/Content-Range logic with a call to `writeOriginalResponse`, keeping the existing ETag / Last-Modified / Cache-Control / Content-Type header writes **before** the call. The resulting handler body should look roughly like:

```go
etag := `"` + m.Checksum + `"`
h := w.Header()
h.Set("ETag", etag)
h.Set("Last-Modified", m.ImportedAt.UTC().Format(http.TimeFormat))
h.Set("Cache-Control", "private, max-age=31536000, immutable")
h.Set("Content-Type", m.MimeType)
if ifNoneMatch := r.Header.Get("If-None-Match"); ifNoneMatch != "" && etagMatches(ifNoneMatch, etag) {
    w.WriteHeader(http.StatusNotModified)
    return
}
writeOriginalResponse(w, r, m, func(off, length int64) (io.ReadCloser, error) {
    return svc.OpenOriginal(r.Context(), id, caller, off, length)
})
```

Adapt the closure to the actual `ThumbService` / `MediaService` signature that already exists in the owner path (`svc.OpenOriginal` or equivalent). Keep existing error mapping unchanged.

- [ ] **Step 6: Refactor the owner thumb handler**

Modify `internal/httpapi/media_thumb.go` similarly: keep the existing ETag + Cache-Control + Content-Type header writes (owner thumb returns `image/jpeg`), then call `writeThumbResponse` with a closure that returns the thumb reader.

- [ ] **Step 7: Run helper tests + existing owner tests — expect PASS**

Run: `go test ./internal/httpapi/... -count=1`
Expected: PASS. The owner-route behaviour must be byte-identical to before (cache headers unchanged, Range semantics unchanged).

- [ ] **Step 8: Commit**

```bash
git add internal/httpapi/originals.go internal/httpapi/originals_test.go \
        internal/httpapi/thumbs.go internal/httpapi/thumbs_test.go \
        internal/httpapi/media_original.go internal/httpapi/media_thumb.go
git commit -m "httpapi: extract writeOriginalResponse + writeThumbResponse helpers"
```

---

## Task 15: `SharedReadService.OpenOriginal` + `OpenThumb`

**Files:**
- Modify: `internal/service/shared_read_service.go`
- Modify: `internal/service/shared_read_service_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/service/shared_read_service_test.go`:

```go
func TestSharedReadOpenOriginalAuthorizedWithDownload(t *testing.T) {
    r := require.New(t)
    fx := newSharedReadFixture(t)
    seedOwner(t, fx.db, "h", "alice")
    seedOwner(t, fx.db, "h", "bob")

    m, body := seedStoredMedia(t, fx, "h/alice", "hello")
    s := makeMediaSetScopeWithDownload(t, fx.db, "h/alice", "h/bob", nil, fx.now, true, m)
    bumpActive(t, fx.db, s.UUID, fx.now)

    rc, row, err := fx.svc.OpenOriginal(context.Background(),
        owners.Principal{Hub: "h", UserID: "bob"}, []string{s.UUID}, m, 0, -1)
    r.NoError(err)
    defer rc.Close()
    got, err := io.ReadAll(rc)
    r.NoError(err)
    r.Equal(body, string(got))
    r.Equal(m, row.ID)
}

func TestSharedReadOpenOriginalAuthorizedWithoutDownloadReturnsForbidden(t *testing.T) {
    fx := newSharedReadFixture(t)
    seedOwner(t, fx.db, "h", "alice")
    seedOwner(t, fx.db, "h", "bob")
    m, _ := seedStoredMedia(t, fx, "h/alice", "hello")
    s := makeMediaSetScope(t, fx.db, "h/alice", "h/bob", nil, fx.now, m) // download=false
    bumpActive(t, fx.db, s.UUID, fx.now)

    _, _, err := fx.svc.OpenOriginal(context.Background(),
        owners.Principal{Hub: "h", UserID: "bob"}, []string{s.UUID}, m, 0, -1)
    require.ErrorIs(t, err, errs.ErrPermissionDenied)
}

func TestSharedReadOpenOriginalUnauthorizedReturnsNotFound(t *testing.T) {
    fx := newSharedReadFixture(t)
    seedOwner(t, fx.db, "h", "alice")
    seedOwner(t, fx.db, "h", "bob")
    m, _ := seedStoredMedia(t, fx, "h/alice", "hello")
    _, _, err := fx.svc.OpenOriginal(context.Background(),
        owners.Principal{Hub: "h", UserID: "bob"}, nil, m, 0, -1)
    require.ErrorIs(t, err, errs.ErrNotFound)
}

func TestSharedReadOpenThumbAuthorizedIgnoresDownload(t *testing.T) {
    r := require.New(t)
    fx := newSharedReadFixture(t)
    seedOwner(t, fx.db, "h", "alice")
    seedOwner(t, fx.db, "h", "bob")
    m := seedMediaWithReadyThumb(t, fx, "h/alice", "jpegbytes")
    s := makeMediaSetScope(t, fx.db, "h/alice", "h/bob", nil, fx.now, m) // download=false
    bumpActive(t, fx.db, s.UUID, fx.now)

    rc, row, err := fx.svc.OpenThumb(context.Background(),
        owners.Principal{Hub: "h", UserID: "bob"},
        []string{s.UUID}, m, thumb.SizeGrid, row.ThumbVersion /* see helper */)
    _ = row
    r.NoError(err)
    defer rc.Close()
    r.NotZero(rc)
}
```

Add `seedStoredMedia` and `seedMediaWithReadyThumb` helpers in the same test file: they write a media row, insert bytes into the fixture store under the media's path, and (for the thumb helper) mark `thumb_status='ready'` and populate the thumb cache.

- [ ] **Step 2: Run — expect FAIL**

Run: `go test ./internal/service/... -run "TestSharedReadOpenOriginal|TestSharedReadOpenThumb" -v`
Expected: FAIL.

- [ ] **Step 3: Implement `OpenOriginal` + `OpenThumb`**

Append to `internal/service/shared_read_service.go`:

```go
// OpenOriginal returns the storage reader for the full-resolution
// bytes, gated by CheckMediaAccess *and* CanDownload. Returns
// errs.ErrPermissionDenied when access exists but download is
// disallowed, errs.ErrNotFound otherwise. offset/length are forwarded
// verbatim to storage.Store.ReadRange; length == -1 means "to EOF".
func (s *SharedReadService) OpenOriginal(
    ctx context.Context,
    caller owners.Principal,
    headerScopes []string,
    mediaID string,
    offset, length int64,
) (io.ReadCloser, media.Media, error) {
    dec, err := s.resolver.CheckMediaAccess(ctx, caller, headerScopes, mediaID)
    if err != nil {
        return nil, media.Media{}, err
    }
    if !dec.Authorized {
        return nil, media.Media{}, fmt.Errorf("%w: media id=%s", errs.ErrNotFound, mediaID)
    }
    if !dec.CanDownload() {
        return nil, media.Media{}, fmt.Errorf("%w: media id=%s download disabled", errs.ErrPermissionDenied, mediaID)
    }
    m, err := s.media.GetByID(ctx, mediaID)
    if err != nil {
        return nil, media.Media{}, err
    }
    rc, err := s.storage.ReadRange(ctx, m.Owner, m.Path, offset, length)
    if err != nil {
        return nil, media.Media{}, err
    }
    return rc, m, nil
}

// OpenThumb returns the thumb reader, gated only by CheckMediaAccess
// (thumbs ignore allow_download). The signature mirrors
// ThumbService.Get — media row comes back alongside the reader so the
// handler can derive ETag without a second repo round-trip.
func (s *SharedReadService) OpenThumb(
    ctx context.Context,
    caller owners.Principal,
    headerScopes []string,
    mediaID string, size thumb.Size, version int,
) (io.ReadCloser, media.Media, error) {
    dec, err := s.resolver.CheckMediaAccess(ctx, caller, headerScopes, mediaID)
    if err != nil {
        return nil, media.Media{}, err
    }
    if !dec.Authorized {
        return nil, media.Media{}, fmt.Errorf("%w: media id=%s", errs.ErrNotFound, mediaID)
    }
    m, err := s.media.GetByID(ctx, mediaID)
    if err != nil {
        return nil, media.Media{}, err
    }
    if m.ThumbStatus != "ready" {
        return nil, media.Media{}, fmt.Errorf("%w: media id=%s thumb not ready", errs.ErrNotFound, mediaID)
    }
    if m.ThumbVersion != version {
        return nil, media.Media{}, fmt.Errorf("%w: media id=%s thumb version mismatch (want %d have %d)",
            errs.ErrNotFound, mediaID, version, m.ThumbVersion)
    }
    key := thumb.ThumbKey(mediaID, version, size)
    rc, err := s.storage.ReadRange(ctx, m.Owner, key, 0, -1)
    if err != nil {
        return nil, media.Media{}, err
    }
    return rc, m, nil
}
```

Imports to add: `"io"`, `"github.com/wesm/fotobank/internal/thumb"`.

- [ ] **Step 4: Run — expect PASS**

Run: `go test ./internal/service/... -run "TestSharedReadOpenOriginal|TestSharedReadOpenThumb" -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/service/shared_read_service.go internal/service/shared_read_service_test.go
git commit -m "SharedReadService: OpenOriginal (with CanDownload gate) + OpenThumb"
```

---

## Task 16: HTTP — `/api/v1/shared/scopes` + `/api/v1/shared/albums*`

**Files:**
- Create: `internal/httpapi/shared.go`
- Create: `internal/httpapi/shared_test.go`
- Modify: `internal/httpapi/api.go`

- [ ] **Step 1: Write failing tests**

Create `internal/httpapi/shared_test.go`:

```go
package httpapi_test

import (
    "context"
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "testing"
    "time"

    "github.com/stretchr/testify/require"

    "github.com/wesm/fotobank/internal/httpapi"
    "github.com/wesm/fotobank/internal/identity"
    "github.com/wesm/fotobank/internal/owners"
    "github.com/wesm/fotobank/internal/service"
    "github.com/wesm/fotobank/internal/share"
    "github.com/wesm/fotobank/internal/testutil"
)

type sharedFx struct {
    t      *testing.T
    db     *testutil.TestDB
    shared *service.SharedReadService
    srv    *httptest.Server
    client *http.Client
    now    time.Time
}

func newSharedFx(t *testing.T, grantee owners.Principal, scopes []string) sharedFx {
    t.Helper()
    d := testutil.OpenTestDB(t)
    shares := share.NewRepo(d.WriteDB(), d.ReadDB())
    mRepo := media.NewRepo(d.WriteDB(), d.ReadDB())
    aRepo := album.NewRepo(d.WriteDB(), d.ReadDB())
    store := testutil.NewMemStore(t)
    now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
    resolver := share.NewScopeResolver(shares, func() time.Time { return now })
    svc := service.NewSharedReadService(shares, mRepo, aRepo, store, resolver)

    deps := httpapi.Deps{
        Identity:   identity.NewStubWithScopes(grantee, "", scopes),
        SharedRead: svc,
    }
    mux, err := httpapi.New(context.Background(), deps)
    require.NoError(t, err)
    srv := httptest.NewServer(mux)
    t.Cleanup(srv.Close)
    return sharedFx{t: t, db: d, shared: svc, srv: srv, client: srv.Client(), now: now}
}

func TestSharedHTTPListScopesHappyPath(t *testing.T) {
    r := require.New(t)
    seedOwner(t, nil, "", "") // placeholder; real fixture uses fx.db

    // Construct scope ahead of the fixture so the stub can carry its UUID.
    // Pattern: build fx, then seed via fx.db, then re-make the stub — but
    // we keep a single builder here for brevity.
    // (Actual implementation can choose between the two-step pattern
    // and a helper that builds both at once.)

    // ... see source for the full pattern; the gist:
    // 1. seed owners + a media_set scope granted to bob in a side DB
    // 2. build httpapi via newSharedFx with the scope's UUID
    // 3. GET /api/v1/shared/scopes
    // 4. decode and assert Items carries exactly the scope
    //
    // Mark this placeholder so implementers adopt whichever seeding
    // pattern is already in use by shares_test.go.
    r.True(true) // stub assertion; replaced by real test flow
}
```

The above is an outline. Because the fixture needs the scope UUID at stub-construction time, the real test file follows the shape used in `internal/httpapi/shares_test.go` (T16 from E1): seed first, then build the HTTP fixture with the right headerScopes. Each of the tests below uses that shape:

```go
func TestSharedHTTPGetScopeUnknownReturns404(t *testing.T)
func TestSharedHTTPListScopesReturnsAuthorized(t *testing.T)
func TestSharedHTTPListAlbumsReturnsAlbumLiveOnly(t *testing.T)
func TestSharedHTTPGetAlbumUnauthorizedReturns404(t *testing.T)
func TestSharedHTTPListAlbumMediaPaginates(t *testing.T)
```

Each test seeds via DB, builds `httpapi.Deps` with a stub carrying the right scope UUIDs, and hits the endpoints with `srv.Client().Get(srv.URL + "/api/v1/shared/...")`, decoding JSON with the shared DTO types below.

- [ ] **Step 2: Run — expect FAIL**

Run: `go test ./internal/httpapi/... -run TestSharedHTTP -v`
Expected: FAIL.

- [ ] **Step 3: Implement shared routes (scopes + albums)**

Create `internal/httpapi/shared.go`:

```go
package httpapi

import (
    "context"
    "errors"
    "net/http"
    "time"

    "github.com/danielgtaylor/huma/v2"

    "github.com/wesm/fotobank/internal/errs"
    "github.com/wesm/fotobank/internal/service"
)

// --- wire DTOs ---

type sharedPrincipalDTO struct {
    Hub    string `json:"hub"`
    UserID string `json:"user_id"`
}

type sharedScopeDTO struct {
    UUID          string             `json:"uuid"`
    TargetType    string             `json:"target_type"`
    Owner         sharedPrincipalDTO `json:"owner"`
    TargetAlbumID string             `json:"target_album_id,omitempty"`
    AllowDownload bool               `json:"allow_download"`
    Label         string             `json:"label,omitempty"`
    ExpiresAt     *time.Time         `json:"expires_at,omitempty"`
    ItemCount     int                `json:"item_count,omitempty"`
    MediaIDs      []string           `json:"media_ids,omitempty"`
}

type sharedAlbumCoverDTO struct {
    MediaID      string `json:"media_id"`
    ThumbVersion int    `json:"thumb_version"`
}

type sharedAlbumDTO struct {
    ID          string               `json:"id"`
    Name        string               `json:"name"`
    Owner       sharedPrincipalDTO   `json:"owner"`
    ItemCount   int                  `json:"item_count"`
    Cover       *sharedAlbumCoverDTO `json:"cover,omitempty"`
    CreatedAt   time.Time            `json:"created_at"`
    UpdatedAt   time.Time            `json:"updated_at"`
    CanDownload bool                 `json:"can_download"`
}

type sharedMediaDTO struct {
    ID           string             `json:"id"`
    Owner        sharedPrincipalDTO `json:"owner"`
    MediaType    string             `json:"media_type"`
    MimeType     string             `json:"mime_type"`
    DisplayTime  time.Time          `json:"display_time"`
    Width        *int               `json:"width,omitempty"`
    Height       *int               `json:"height,omitempty"`
    DurationMs   *int64             `json:"duration_ms,omitempty"`
    ThumbStatus  string             `json:"thumb_status"`
    ThumbVersion int                `json:"thumb_version"`
    CanDownload  bool               `json:"can_download"`
}

// translateSharedError turns SharedReadService errors into huma status
// errors. Unknown errors delegate to Translate (our cross-cutting map).
func translateSharedError(err error) huma.StatusError {
    switch {
    case errors.Is(err, errs.ErrPermissionDenied):
        return huma.Error403Forbidden("download disabled for this share")
    default:
        return Translate(err)
    }
}

// registerShared wires /api/v1/shared/* on api. svc == nil answers 503
// so the OpenAPI dumper can build the spec without real deps.
func registerShared(api huma.API, svc *service.SharedReadService) {
    registerSharedListScopes(api, svc)
    registerSharedGetScope(api, svc)
    registerSharedListAlbums(api, svc)
    registerSharedGetAlbum(api, svc)
    registerSharedListAlbumMedia(api, svc)
    registerSharedListMedia(api, svc)
    registerSharedGetMedia(api, svc)
}

// --- helper: lift identity + scopes from context ---

type sharedCall struct {
    caller owners.Principal
    scopes []string
}

func callerAndScopes(ctx context.Context) (sharedCall, error) {
    id, ok := IdentityFromContext(ctx)
    if !ok {
        return sharedCall{}, errs.ErrIdentityMissing
    }
    return sharedCall{
        caller: id.Principal.OwnersPrincipal(),
        scopes: id.Scopes,
    }, nil
}

// --- scope list / get ---

type sharedListScopesOutput struct {
    Body struct {
        Items []sharedScopeDTO `json:"items"`
    }
}

func registerSharedListScopes(api huma.API, svc *service.SharedReadService) {
    huma.Register(api, huma.Operation{
        OperationID: "shared-list-scopes",
        Method:      http.MethodGet,
        Path:        "/api/v1/shared/scopes",
    }, func(ctx context.Context, _ *struct{}) (*sharedListScopesOutput, error) {
        if svc == nil {
            return nil, huma.Error503ServiceUnavailable("shared read unavailable")
        }
        call, err := callerAndScopes(ctx)
        if err != nil {
            return nil, translateSharedError(err)
        }
        rows, err := svc.ListScopes(ctx, call.caller, call.scopes)
        if err != nil {
            return nil, translateSharedError(err)
        }
        out := &sharedListScopesOutput{}
        out.Body.Items = make([]sharedScopeDTO, 0, len(rows))
        for _, r := range rows {
            out.Body.Items = append(out.Body.Items, toSharedScopeDTO(r, nil))
        }
        return out, nil
    })
}

type sharedScopeUUIDParam struct {
    UUID string `path:"uuid"`
}

type sharedScopeOutput struct {
    Body sharedScopeDTO
}

func registerSharedGetScope(api huma.API, svc *service.SharedReadService) {
    huma.Register(api, huma.Operation{
        OperationID: "shared-get-scope",
        Method:      http.MethodGet,
        Path:        "/api/v1/shared/scopes/{uuid}",
    }, func(ctx context.Context, in *sharedScopeUUIDParam) (*sharedScopeOutput, error) {
        if svc == nil {
            return nil, huma.Error503ServiceUnavailable("shared read unavailable")
        }
        call, err := callerAndScopes(ctx)
        if err != nil {
            return nil, translateSharedError(err)
        }
        got, err := svc.GetScope(ctx, call.caller, call.scopes, in.UUID)
        if err != nil {
            return nil, translateSharedError(err)
        }
        return &sharedScopeOutput{Body: toSharedScopeDTO(got.SharedScope, got.MediaIDs)}, nil
    })
}

func toSharedScopeDTO(s service.SharedScope, mediaIDs []string) sharedScopeDTO {
    dto := sharedScopeDTO{
        UUID:          s.UUID,
        TargetType:    string(s.TargetType),
        Owner:         sharedPrincipalDTO{Hub: s.Owner.Hub, UserID: s.Owner.UserID},
        AllowDownload: s.AllowDownload,
        Label:         s.Label,
        ExpiresAt:     s.ExpiresAt,
        ItemCount:     s.ItemCount,
    }
    if s.TargetAlbumID != nil {
        dto.TargetAlbumID = *s.TargetAlbumID
    }
    if len(mediaIDs) > 0 {
        dto.MediaIDs = mediaIDs
    }
    return dto
}

// --- album list / get / album-media ---

type sharedListAlbumsOutput struct {
    Body struct {
        Items []sharedAlbumDTO `json:"items"`
    }
}

func registerSharedListAlbums(api huma.API, svc *service.SharedReadService) {
    huma.Register(api, huma.Operation{
        OperationID: "shared-list-albums",
        Method:      http.MethodGet,
        Path:        "/api/v1/shared/albums",
    }, func(ctx context.Context, _ *struct{}) (*sharedListAlbumsOutput, error) {
        if svc == nil {
            return nil, huma.Error503ServiceUnavailable("shared read unavailable")
        }
        call, err := callerAndScopes(ctx)
        if err != nil {
            return nil, translateSharedError(err)
        }
        rows, err := svc.ListAlbums(ctx, call.caller, call.scopes)
        if err != nil {
            return nil, translateSharedError(err)
        }
        out := &sharedListAlbumsOutput{}
        out.Body.Items = make([]sharedAlbumDTO, 0, len(rows))
        for _, r := range rows {
            out.Body.Items = append(out.Body.Items, toSharedAlbumDTO(r))
        }
        return out, nil
    })
}

type sharedAlbumIDParam struct {
    ID string `path:"id"`
}

type sharedAlbumOutput struct {
    Body sharedAlbumDTO
}

func registerSharedGetAlbum(api huma.API, svc *service.SharedReadService) {
    huma.Register(api, huma.Operation{
        OperationID: "shared-get-album",
        Method:      http.MethodGet,
        Path:        "/api/v1/shared/albums/{id}",
    }, func(ctx context.Context, in *sharedAlbumIDParam) (*sharedAlbumOutput, error) {
        if svc == nil {
            return nil, huma.Error503ServiceUnavailable("shared read unavailable")
        }
        call, err := callerAndScopes(ctx)
        if err != nil {
            return nil, translateSharedError(err)
        }
        got, err := svc.GetAlbum(ctx, call.caller, call.scopes, in.ID)
        if err != nil {
            return nil, translateSharedError(err)
        }
        return &sharedAlbumOutput{Body: toSharedAlbumDTO(got.SharedAlbum)}, nil
    })
}

func toSharedAlbumDTO(a service.SharedAlbum) sharedAlbumDTO {
    out := sharedAlbumDTO{
        ID:          a.ID,
        Name:        a.Name,
        Owner:       sharedPrincipalDTO{Hub: a.Owner.Hub, UserID: a.Owner.UserID},
        ItemCount:   a.ItemCount,
        CreatedAt:   a.CreatedAt,
        UpdatedAt:   a.UpdatedAt,
        CanDownload: a.CanDownload,
    }
    if a.Cover != nil {
        out.Cover = &sharedAlbumCoverDTO{
            MediaID:      a.Cover.MediaID,
            ThumbVersion: a.Cover.ThumbVersion,
        }
    }
    return out
}

type sharedAlbumMediaInput struct {
    ID        string    `path:"id"`
    Limit     int       `query:"limit"`
    CursorTS  time.Time `query:"cursor_time"`
    CursorID  string    `query:"cursor_id"`
}

type sharedMediaListOutput struct {
    Body struct {
        Items          []sharedMediaDTO `json:"items"`
        NextCursorTime *time.Time       `json:"next_cursor_time,omitempty"`
        NextCursorID   string           `json:"next_cursor_id,omitempty"`
        HasMore        bool             `json:"has_more"`
    }
}

func registerSharedListAlbumMedia(api huma.API, svc *service.SharedReadService) {
    huma.Register(api, huma.Operation{
        OperationID: "shared-list-album-media",
        Method:      http.MethodGet,
        Path:        "/api/v1/shared/albums/{id}/media",
    }, func(ctx context.Context, in *sharedAlbumMediaInput) (*sharedMediaListOutput, error) {
        if svc == nil {
            return nil, huma.Error503ServiceUnavailable("shared read unavailable")
        }
        call, err := callerAndScopes(ctx)
        if err != nil {
            return nil, translateSharedError(err)
        }
        page, next, err := svc.ListAlbumMedia(ctx, call.caller, call.scopes, in.ID,
            service.SharedMediaCursor{
                AfterDisplayTime: in.CursorTS,
                AfterID:          in.CursorID,
                Limit:            in.Limit,
            })
        if err != nil {
            return nil, translateSharedError(err)
        }
        return buildSharedMediaListOutput(page, next), nil
    })
}

func buildSharedMediaListOutput(page []service.SharedMedia, next service.SharedMediaCursor) *sharedMediaListOutput {
    out := &sharedMediaListOutput{}
    out.Body.Items = make([]sharedMediaDTO, 0, len(page))
    for _, m := range page {
        out.Body.Items = append(out.Body.Items, toSharedMediaDTO(m))
    }
    if next.AfterID != "" {
        ts := next.AfterDisplayTime
        out.Body.NextCursorTime = &ts
        out.Body.NextCursorID = next.AfterID
        out.Body.HasMore = true
    }
    return out
}

func toSharedMediaDTO(m service.SharedMedia) sharedMediaDTO {
    return sharedMediaDTO{
        ID:           m.ID,
        Owner:        sharedPrincipalDTO{Hub: m.Owner.Hub, UserID: m.Owner.UserID},
        MediaType:    string(m.MediaType),
        MimeType:     m.MimeType,
        DisplayTime:  m.DisplayTime,
        Width:        m.Width,
        Height:       m.Height,
        DurationMs:   m.DurationMs,
        ThumbStatus:  m.ThumbStatus,
        ThumbVersion: m.ThumbVersion,
        CanDownload:  m.CanDownload,
    }
}
```

Imports above assume `owners.Principal` is already referenced via the package — add `"github.com/wesm/fotobank/internal/owners"` and `"github.com/wesm/fotobank/internal/service"` as needed.

- [ ] **Step 4: Wire the routes in `httpapi.New`**

Modify `internal/httpapi/api.go` to add `SharedRead *service.SharedReadService` to `Deps` and call `registerShared(api, deps.SharedRead)` inside `New`.

- [ ] **Step 5: Run — expect PASS**

Run: `go test ./internal/httpapi/... -run TestSharedHTTP -v`
Expected: PASS.

- [ ] **Step 6: Regenerate OpenAPI**

Run: `make api-generate`
Expected: the `openapi.json` diff adds the new operations under `/api/v1/shared/...`.

- [ ] **Step 7: Commit**

```bash
git add internal/httpapi/shared.go internal/httpapi/shared_test.go \
        internal/httpapi/api.go internal/httpapi/openapi.json
git commit -m "httpapi: /api/v1/shared scopes + albums (list/get + album/media)"
```

---

## Task 17: HTTP — `/api/v1/shared/media` (list + get)

**Files:**
- Modify: `internal/httpapi/shared.go`
- Modify: `internal/httpapi/shared_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/httpapi/shared_test.go` (same fixture shape as Task 16):

```go
func TestSharedHTTPListMediaPaginates(t *testing.T)
func TestSharedHTTPGetMediaUnauthorizedReturns404(t *testing.T)
func TestSharedHTTPGetMediaAuthorizedReturnsCanDownload(t *testing.T)
```

Each seeds the DB with a grantee + one or more scopes, builds `newSharedFx` with the scope UUIDs in the stub, issues a `GET /api/v1/shared/media(/{id})?` request, and asserts status + DTO fields.

- [ ] **Step 2: Run — expect FAIL**

Run: `go test ./internal/httpapi/... -run "TestSharedHTTPListMedia|TestSharedHTTPGetMedia" -v`
Expected: FAIL.

- [ ] **Step 3: Register the routes**

Append to `internal/httpapi/shared.go`:

```go
type sharedListMediaInput struct {
    Limit    int       `query:"limit"`
    CursorTS time.Time `query:"cursor_time"`
    CursorID string    `query:"cursor_id"`
}

func registerSharedListMedia(api huma.API, svc *service.SharedReadService) {
    huma.Register(api, huma.Operation{
        OperationID: "shared-list-media",
        Method:      http.MethodGet,
        Path:        "/api/v1/shared/media",
    }, func(ctx context.Context, in *sharedListMediaInput) (*sharedMediaListOutput, error) {
        if svc == nil {
            return nil, huma.Error503ServiceUnavailable("shared read unavailable")
        }
        call, err := callerAndScopes(ctx)
        if err != nil {
            return nil, translateSharedError(err)
        }
        page, next, err := svc.ListMedia(ctx, call.caller, call.scopes,
            service.SharedMediaCursor{
                AfterDisplayTime: in.CursorTS,
                AfterID:          in.CursorID,
                Limit:            in.Limit,
            })
        if err != nil {
            return nil, translateSharedError(err)
        }
        return buildSharedMediaListOutput(page, next), nil
    })
}

type sharedMediaIDParam struct {
    ID string `path:"id"`
}

type sharedMediaOutput struct {
    Body sharedMediaDTO
}

func registerSharedGetMedia(api huma.API, svc *service.SharedReadService) {
    huma.Register(api, huma.Operation{
        OperationID: "shared-get-media",
        Method:      http.MethodGet,
        Path:        "/api/v1/shared/media/{id}",
    }, func(ctx context.Context, in *sharedMediaIDParam) (*sharedMediaOutput, error) {
        if svc == nil {
            return nil, huma.Error503ServiceUnavailable("shared read unavailable")
        }
        call, err := callerAndScopes(ctx)
        if err != nil {
            return nil, translateSharedError(err)
        }
        got, err := svc.GetMedia(ctx, call.caller, call.scopes, in.ID)
        if err != nil {
            return nil, translateSharedError(err)
        }
        return &sharedMediaOutput{Body: toSharedMediaDTO(got)}, nil
    })
}
```

Both handlers are already called from `registerShared` added in Task 16.

- [ ] **Step 4: Run — expect PASS**

Run: `go test ./internal/httpapi/... -run "TestSharedHTTPListMedia|TestSharedHTTPGetMedia" -v`
Expected: PASS.

- [ ] **Step 5: Regenerate OpenAPI and commit**

```bash
make api-generate
git add internal/httpapi/shared.go internal/httpapi/shared_test.go internal/httpapi/openapi.json
git commit -m "httpapi: /api/v1/shared/media list + get"
```

---

## Task 18: HTTP — `/shared/media/{id}/thumb` and `/shared/media/{id}/original`

**Files:**
- Create: `internal/httpapi/shared_bytes.go`
- Modify: `internal/httpapi/shared_test.go`
- Modify: `internal/httpapi/api.go`

These are byte-streaming routes and use raw `http.HandlerFunc` on the mux (same pattern owner routes use — huma isn't used for streaming bodies).

- [ ] **Step 0: Extend `SharedMedia` with `Size`**

Because `writeOriginalResponse` needs `m.Size` for `Content-Length` and Range clamping, and the grantee has already passed the download gate, Size is safe to expose. Before the byte routes land:

1. Add `Size int64` to `service.SharedMedia`.
2. Populate it in `toSharedMedia` (`Size: m.Size`).
3. Add `Size int64 json:"size,omitempty"` to `sharedMediaDTO` in `internal/httpapi/shared.go`; populate it in `toSharedMediaDTO`.

Re-run `go test ./internal/service/... ./internal/httpapi/... -count=1`; PASS expected with no handler-level test changes.

- [ ] **Step 1: Write the failing tests**

Append to `internal/httpapi/shared_test.go`:

```go
func TestSharedHTTPThumbAuthorized(t *testing.T)                      // 200, jpeg body
func TestSharedHTTPThumbUnauthorizedReturns404(t *testing.T)
func TestSharedHTTPOriginalAllowDownloadTrue(t *testing.T)            // 200, body
func TestSharedHTTPOriginalAllowDownloadFalseReturns403(t *testing.T)
func TestSharedHTTPOriginalRangeReturns206(t *testing.T)              // verifies Content-Range header
func TestSharedHTTPOriginalUnsatisfiableRangeReturns416(t *testing.T)
```

- [ ] **Step 2: Run — expect FAIL**

Run: `go test ./internal/httpapi/... -run "TestSharedHTTPThumb|TestSharedHTTPOriginal" -v`
Expected: FAIL.

- [ ] **Step 3: Implement the byte routes**

Create `internal/httpapi/shared_bytes.go`:

```go
package httpapi

import (
    "errors"
    "io"
    "log/slog"
    "net/http"
    "strconv"

    "github.com/wesm/fotobank/internal/errs"
    "github.com/wesm/fotobank/internal/service"
    "github.com/wesm/fotobank/internal/thumb"
)

// registerSharedBytes wires raw byte handlers onto the mux. svc == nil
// registers nothing (consistent with the owner byte routes' pattern).
func registerSharedBytes(mux *http.ServeMux, svc *service.SharedReadService) {
    if svc == nil {
        return
    }
    mux.Handle("GET /api/v1/shared/media/{id}/thumb", sharedThumbHandler(svc))
    mux.Handle("GET /api/v1/shared/media/{id}/original", sharedOriginalHandler(svc))
}

func sharedThumbHandler(svc *service.SharedReadService) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        id := r.PathValue("id")
        ident, ok := IdentityFromContext(r.Context())
        if !ok {
            http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
            return
        }

        notFound := func() {
            w.Header().Set("Cache-Control", "no-store")
            http.Error(w, "thumb not found", http.StatusNotFound)
        }

        sizeStr := r.URL.Query().Get("size")
        if sizeStr == "" {
            sizeStr = "grid"
        }
        size, err := thumb.ParseSize(sizeStr)
        if err != nil {
            http.Error(w, "unknown size", http.StatusBadRequest)
            return
        }
        vStr := r.URL.Query().Get("v")
        if vStr == "" {
            notFound()
            return
        }
        version, err := strconv.Atoi(vStr)
        if err != nil || version < 0 {
            notFound()
            return
        }

        caller := ident.Principal.OwnersPrincipal()
        rc, m, err := svc.OpenThumb(r.Context(), caller, ident.Scopes, id, size, version)
        if err != nil {
            if errors.Is(err, errs.ErrNotFound) {
                notFound()
                return
            }
            slog.Error("shared thumb", "err", err, "id", id)
            http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
            return
        }
        defer func() { _ = rc.Close() }()

        etag := `"` + m.ID + "-" + string(size) + "-v" + strconv.Itoa(m.ThumbVersion) + `"`
        h := w.Header()
        h.Set("ETag", etag)
        if m.ThumbUpdatedAt != nil {
            h.Set("Last-Modified", m.ThumbUpdatedAt.UTC().Format(http.TimeFormat))
        }
        h.Set("Cache-Control", "private, max-age=31536000, immutable")
        h.Set("Vary", "X-Auth-Scopes")
        h.Set("Content-Type", "image/jpeg")

        if ifNoneMatch := r.Header.Get("If-None-Match"); ifNoneMatch != "" && etagMatches(ifNoneMatch, etag) {
            w.WriteHeader(http.StatusNotModified)
            return
        }
        writeThumbResponse(w, r, func() (io.ReadCloser, error) { return rc, nil })
    })
}

func sharedOriginalHandler(svc *service.SharedReadService) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        id := r.PathValue("id")
        ident, ok := IdentityFromContext(r.Context())
        if !ok {
            http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
            return
        }
        caller := ident.Principal.OwnersPrincipal()

        // First call: offset=0, length=0 — meaning "run the access check
        // and return the media row without opening the stream." We
        // implement this by passing offset=0, length=0 and treating it
        // as a dry run. However, the service does not have a dry-run
        // mode today, so we resolve access via a separate GetMedia call.
        m, err := svc.GetMedia(r.Context(), caller, ident.Scopes, id)
        if err != nil {
            if errors.Is(err, errs.ErrNotFound) {
                http.NotFound(w, r)
                return
            }
            slog.Error("shared original access", "err", err, "id", id)
            http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
            return
        }
        if !m.CanDownload {
            http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
            return
        }

        // Fetch the full media row (for Size + ImportedAt + Checksum) in
        // a second step. The service has no shared "get media row +
        // open reader" helper yet; do the minimum via GetMedia above
        // and a direct open below through OpenOriginal.
        etag := `"` + id + `"`
        h := w.Header()
        h.Set("ETag", etag)
        h.Set("Last-Modified", m.DisplayTime.UTC().Format(http.TimeFormat))
        h.Set("Cache-Control", "private, max-age=31536000, immutable")
        h.Set("Vary", "X-Auth-Scopes")
        h.Set("Content-Type", m.MimeType)

        if ifNoneMatch := r.Header.Get("If-None-Match"); ifNoneMatch != "" && etagMatches(ifNoneMatch, etag) {
            w.WriteHeader(http.StatusNotModified)
            return
        }

        writeOriginalResponse(w, r,
            media.Media{
                ID: m.ID, Owner: m.Owner, MimeType: m.MimeType,
                Size: m.Size, ImportedAt: m.DisplayTime,
            },
            func(off, length int64) (io.ReadCloser, error) {
                rc, _, err := svc.OpenOriginal(r.Context(), caller, ident.Scopes, id, off, length)
                return rc, err
            },
        )
    })
}
```

**Size on `SharedMedia`:** `writeOriginalResponse` needs `m.Size` for `Content-Length` and Range clamping, and the grantee is about to download the file anyway — Size is not a sensitive field in the "can_download=true" branch. Extend the domain and DTO before wiring the handler:

- Add `Size int64` to `service.SharedMedia`.
- Populate it in `toSharedMedia` (`Size: m.Size`).
- Add `Size int64 json:"size,omitempty"` to `sharedMediaDTO` and populate it in `toSharedMediaDTO`.

Do this as the first step of the task so the handler above compiles without a shim.

- [ ] **Step 4: Wire the mux routes in `httpapi.New`**

Modify `internal/httpapi/api.go` to call `registerSharedBytes(mux, deps.SharedRead)` alongside the existing owner byte-route registration.

- [ ] **Step 5: Run — expect PASS**

Run: `go test ./internal/httpapi/... -run "TestSharedHTTPThumb|TestSharedHTTPOriginal" -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/httpapi/shared_bytes.go internal/httpapi/shared_test.go \
        internal/httpapi/shared.go internal/httpapi/api.go \
        internal/service/shared_read_service.go internal/service/shared_read_service_test.go
git commit -m "httpapi: /shared/media/{id}/thumb + /original byte routes"
```

---

## Task 19: `WithPrincipalDisplayCache` middleware

**Files:**
- Modify: `internal/httpapi/middleware.go`
- Modify: `internal/httpapi/middleware_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/httpapi/middleware_test.go`:

```go
func TestPrincipalDisplayCacheUpsertsSynchronously(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    repo := share.NewPrincipalDisplayRepo(d.WriteDB(), d.ReadDB())

    var called bool
    next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
        called = true
        w.WriteHeader(http.StatusOK)
    })
    mw := httpapi.WithPrincipalDisplayCache(repo, testutil.DiscardLogger())

    srv := httptest.NewServer(mw(next))
    t.Cleanup(srv.Close)

    req := httptest.NewRequest(http.MethodGet, "/x", nil)
    ctx := httpapi.ContextWithIdentity(req.Context(), identity.Identity{
        Principal: identity.Principal{Hub: "h", UserID: "alice", Handle: "Alice"},
    })
    w := httptest.NewRecorder()
    mw(next).ServeHTTP(w, req.WithContext(ctx))
    r.True(called)

    handle, ok, err := repo.Get(context.Background(), owners.Principal{Hub: "h", UserID: "alice"})
    r.NoError(err)
    r.True(ok)
    r.Equal("Alice", handle)
}

func TestPrincipalDisplayCacheEmptyHandleSkipsUpsert(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    repo := share.NewPrincipalDisplayRepo(d.WriteDB(), d.ReadDB())
    next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
    mw := httpapi.WithPrincipalDisplayCache(repo, testutil.DiscardLogger())

    ctx := httpapi.ContextWithIdentity(context.Background(),
        identity.Identity{Principal: identity.Principal{Hub: "h", UserID: "alice"}})
    req := httptest.NewRequest(http.MethodGet, "/x", nil).WithContext(ctx)
    w := httptest.NewRecorder()
    mw(next).ServeHTTP(w, req)

    _, ok, err := repo.Get(context.Background(), owners.Principal{Hub: "h", UserID: "alice"})
    r.NoError(err)
    r.False(ok)
}

func TestPrincipalDisplayCacheNoIdentityIsNoOp(t *testing.T) {
    d := testutil.OpenTestDB(t)
    repo := share.NewPrincipalDisplayRepo(d.WriteDB(), d.ReadDB())
    called := false
    next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { called = true })
    mw := httpapi.WithPrincipalDisplayCache(repo, testutil.DiscardLogger())
    w := httptest.NewRecorder()
    mw(next).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
    require.True(t, called)
}
```

If `ContextWithIdentity` is not currently exported from `internal/httpapi/middleware.go`, add a test-only exported wrapper in a new `internal/httpapi/export_test.go` or export the constructor — it's only needed for tests. The existing `IdentityFromContext` already reads the context key.

- [ ] **Step 2: Run — expect FAIL**

Run: `go test ./internal/httpapi/... -run TestPrincipalDisplayCache -v`
Expected: FAIL.

- [ ] **Step 3: Implement the middleware**

Append to `internal/httpapi/middleware.go`:

```go
// displayCacheLRU tracks recent (hub, user_id) upserts so a burst of
// requests does not hammer principal_display. Separate from
// identity resolution; a process-local cache.
type displayCacheLRU struct {
    mu    sync.Mutex
    seen  map[owners.Principal]time.Time
    cap   int
    ttl   time.Duration
}

func newDisplayCacheLRU(cap int, ttl time.Duration) *displayCacheLRU {
    return &displayCacheLRU{
        seen: make(map[owners.Principal]time.Time, cap),
        cap:  cap, ttl: ttl,
    }
}

func (l *displayCacheLRU) shouldUpsert(p owners.Principal, now time.Time) bool {
    l.mu.Lock()
    defer l.mu.Unlock()
    if t, ok := l.seen[p]; ok && now.Sub(t) < l.ttl {
        return false
    }
    if len(l.seen) >= l.cap {
        // Trivial eviction — drop one arbitrary entry. Not LRU in the
        // strict sense but bounded memory is the actual requirement;
        // the TTL path is what keeps hits fresh.
        for k := range l.seen {
            delete(l.seen, k)
            break
        }
    }
    l.seen[p] = now
    return true
}

// WithPrincipalDisplayCache wraps next with a middleware that upserts
// the observed display handle into principal_display. The upsert is
// synchronous but bounded by a 50ms context timeout so a hung DB
// does not stall the grantee's read path. Errors are logged at warn
// and swallowed.
func WithPrincipalDisplayCache(repo *share.PrincipalDisplayRepo, logger *slog.Logger) func(http.Handler) http.Handler {
    lru := newDisplayCacheLRU(10_000, time.Minute)
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            ident, ok := IdentityFromContext(r.Context())
            if !ok || ident.Principal.Handle == "" {
                next.ServeHTTP(w, r)
                return
            }
            now := time.Now().UTC()
            if !lru.shouldUpsert(ident.Principal.OwnersPrincipal(), now) {
                next.ServeHTTP(w, r)
                return
            }
            ctx, cancel := context.WithTimeout(r.Context(), 50*time.Millisecond)
            defer cancel()
            if err := repo.Upsert(ctx, ident.Principal, now); err != nil {
                logger.Warn("principal_display upsert", "err", err,
                    "hub", ident.Principal.Hub, "user_id", ident.Principal.UserID)
            }
            next.ServeHTTP(w, r)
        })
    }
}
```

Imports to add: `"sync"`, `"time"`, `"log/slog"`, `"github.com/wesm/fotobank/internal/owners"`, `"github.com/wesm/fotobank/internal/share"`.

- [ ] **Step 4: Wire the middleware in `httpapi.New`**

Modify `internal/httpapi/api.go`. Add `PrincipalDisplay *share.PrincipalDisplayRepo` to `Deps`, and chain in the correct order per spec §8.2:

```go
handler := http.Handler(mux)
if deps.PrincipalDisplay != nil {
    handler = WithPrincipalDisplayCache(deps.PrincipalDisplay, slog.Default())(handler)
}
if deps.Identity != nil {
    handler = WithMiddleware(deps.Identity)(handler)
}
return handler, nil
```

- [ ] **Step 5: Run — expect PASS**

Run: `go test ./internal/httpapi/... -run TestPrincipalDisplayCache -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/httpapi/middleware.go internal/httpapi/middleware_test.go internal/httpapi/api.go
git commit -m "httpapi: WithPrincipalDisplayCache middleware (synchronous, bounded)"
```

---

## Task 20: `ShareService.PreviewScope` + `/api/v1/shares/{uuid}/preview`

**Files:**
- Modify: `internal/service/share_service.go`
- Modify: `internal/service/share_service_test.go`
- Modify: `internal/httpapi/shares.go`
- Modify: `internal/httpapi/shares_test.go`

- [ ] **Step 1: Write the failing service test**

Append to `internal/service/share_service_test.go`:

```go
func TestPreviewScopeAlbumLivePopulatesMediaAndAlbum(t *testing.T) {
    r := require.New(t)
    fx := newShareServiceFixture(t)
    alice := owners.Principal{Hub: "h", UserID: "alice"}
    bob := owners.Principal{Hub: "h", UserID: "bob"}
    seedOwner(t, fx.db, alice)
    seedOwner(t, fx.db, bob)
    album := seedAlbumWithMedia(t, fx.db, alice, 2)
    s, err := fx.svc.Create(context.Background(), service.CreateShareRequest{
        Grantee: bob, TargetType: share.TargetAlbumLive, AlbumID: album.ID,
    }, alice)
    r.NoError(err)

    prev, err := fx.svc.PreviewScope(context.Background(), s.UUID, alice)
    r.NoError(err)
    r.Equal(s.UUID, prev.Scope.UUID)
    r.NotNil(prev.Album)
    r.Equal(album.ID, prev.Album.ID)
    r.Len(prev.Media, 2)
}

func TestPreviewScopeCrossOwnerReturnsNotFound(t *testing.T) {
    fx := newShareServiceFixture(t)
    alice := owners.Principal{Hub: "h", UserID: "alice"}
    bob := owners.Principal{Hub: "h", UserID: "bob"}
    charlie := owners.Principal{Hub: "h", UserID: "charlie"}
    seedOwner(t, fx.db, alice)
    seedOwner(t, fx.db, bob)
    seedOwner(t, fx.db, charlie)
    album := seedAlbumWithMedia(t, fx.db, alice, 1)
    s, err := fx.svc.Create(context.Background(), service.CreateShareRequest{
        Grantee: bob, TargetType: share.TargetAlbumLive, AlbumID: album.ID,
    }, alice)
    require.NoError(t, err)

    _, err = fx.svc.PreviewScope(context.Background(), s.UUID, charlie)
    require.ErrorIs(t, err, errs.ErrNotFound)
}

func TestPreviewScopeSurfacesWarnings(t *testing.T) {
    r := require.New(t)
    fx := newShareServiceFixture(t)
    alice := owners.Principal{Hub: "h", UserID: "alice"}
    bob := owners.Principal{Hub: "h", UserID: "bob"}
    seedOwner(t, fx.db, alice)
    seedOwner(t, fx.db, bob)
    album := seedAlbumWithMedia(t, fx.db, alice, 1)
    past := fx.now.Add(-time.Hour)
    s, err := fx.svc.Create(context.Background(), service.CreateShareRequest{
        Grantee: bob, TargetType: share.TargetAlbumLive, AlbumID: album.ID,
        ExpiresAt: &past,
    }, alice)
    r.NoError(err)

    prev, err := fx.svc.PreviewScope(context.Background(), s.UUID, alice)
    r.NoError(err)
    r.Contains(prev.Warnings, "scope_expired")
    r.Contains(prev.Warnings, "broker_not_active") // freshly-created scopes are pending
}
```

- [ ] **Step 2: Run — expect FAIL**

Run: `go test ./internal/service/... -run TestPreviewScope -v`
Expected: FAIL.

- [ ] **Step 3: Implement `PreviewScope`**

Append to `internal/service/share_service.go`:

```go
// ScopePreview is the materialised-view response for PreviewScope.
// Media is the frozen membership for media_set, the live album_media
// order for album_live. Album is non-nil iff target_type == album_live.
type ScopePreview struct {
    Scope    share.Scope
    Media    []PreviewMedia
    Album    *share.AlbumSummary
    Warnings []string
}

// PreviewMedia is the owner-facing preview row for one media. Kept as
// a distinct type so the owner surface never accidentally reuses the
// grantee-side SharedMedia DTO.
type PreviewMedia struct {
    ID           string
    MediaType    media.Type
    MimeType     string
    DisplayTime  time.Time
    ThumbStatus  string
    ThumbVersion int
}

// PreviewScope returns the materialised view of what the grantee will
// see. Owner-only: cross-owner UUIDs return errs.ErrNotFound (never
// ErrOwnerMismatch). Uses share.Repo.ExpandScope so the grantee-side
// resolver is never invoked with caller-as-grantee semantics.
func (s *ShareService) PreviewScope(ctx context.Context, uuid string, caller owners.Principal) (ScopePreview, error) {
    exp, err := s.shares.ExpandScope(ctx, uuid)
    if err != nil {
        return ScopePreview{}, err
    }
    if exp.Scope.Owner != caller {
        return ScopePreview{}, fmt.Errorf("%w: scope uuid=%s", errs.ErrNotFound, uuid)
    }
    mediaRows, err := s.media.GetByIDs(ctx, exp.MediaIDs)
    if err != nil {
        return ScopePreview{}, err
    }
    out := ScopePreview{Scope: exp.Scope, Album: exp.Album}
    out.Media = make([]PreviewMedia, 0, len(mediaRows))
    for _, m := range mediaRows {
        display := m.ImportedAt
        if m.Timestamp != nil {
            display = *m.Timestamp
        }
        out.Media = append(out.Media, PreviewMedia{
            ID: m.ID, MediaType: m.Type, MimeType: m.MimeType,
            DisplayTime: display,
            ThumbStatus: m.ThumbStatus, ThumbVersion: m.ThumbVersion,
        })
    }
    out.Warnings = previewWarnings(exp, mediaRows, s.now())
    return out, nil
}

// previewWarnings implements spec §7.7's warning set.
func previewWarnings(exp share.ExpandedScope, mediaRows []media.Media, now time.Time) []string {
    var w []string
    if exp.Scope.BrokerStatus != share.StatusActive {
        w = append(w, "broker_not_active")
    }
    if exp.Scope.ExpiresAt != nil && !exp.Scope.ExpiresAt.After(now) {
        w = append(w, "scope_expired")
    }
    if exp.Scope.TargetType == share.TargetAlbumLive && exp.Album != nil && exp.Album.ItemCount == 0 {
        w = append(w, "empty_album")
    }
    if len(mediaRows) > 0 {
        missing := 0
        for _, m := range mediaRows {
            if m.ThumbStatus != "ready" {
                missing++
            }
        }
        if missing*100/len(mediaRows) > 25 {
            w = append(w, "missing_thumbs")
        }
    }
    return w
}
```

Modify `ShareService` to carry a `*media.Repo` dependency if it does not already — E1's `ShareService` already holds `m *media.Repo` for `Create`'s pre-flight, so `PreviewScope` reuses the same field.

- [ ] **Step 4: Run service test — expect PASS**

Run: `go test ./internal/service/... -run TestPreviewScope -v`
Expected: PASS.

- [ ] **Step 5: Write the failing HTTP test**

Append to `internal/httpapi/shares_test.go`:

```go
func TestSharesPreviewHappyPath(t *testing.T)
func TestSharesPreviewCrossOwnerReturns404(t *testing.T)
```

Follow the E1 shares HTTP-test shape: seed, hit `GET /api/v1/shares/{uuid}/preview`, decode, assert.

- [ ] **Step 6: Register the preview route**

Append to `internal/httpapi/shares.go`:

```go
type previewShareOutput struct {
    Body previewShareDTO
}

type previewShareDTO struct {
    Scope    scopeDTO          `json:"scope"`
    Media    []previewMediaDTO `json:"media"`
    Album    *previewAlbumDTO  `json:"album,omitempty"`
    Warnings []string          `json:"warnings,omitempty"`
}

type previewMediaDTO struct {
    ID           string    `json:"id"`
    MediaType    string    `json:"media_type"`
    MimeType     string    `json:"mime_type"`
    DisplayTime  time.Time `json:"display_time"`
    ThumbStatus  string    `json:"thumb_status"`
    ThumbVersion int       `json:"thumb_version"`
}

type previewAlbumDTO struct {
    ID        string    `json:"id"`
    Name      string    `json:"name"`
    ItemCount int       `json:"item_count"`
    UpdatedAt time.Time `json:"updated_at"`
}

func registerSharesPreview(api huma.API, svc *service.ShareService) {
    huma.Register(api, huma.Operation{
        OperationID: "shares-preview",
        Method:      http.MethodGet,
        Path:        "/api/v1/shares/{uuid}/preview",
    }, func(ctx context.Context, in *scopeUUIDParam) (*previewShareOutput, error) {
        if svc == nil {
            return nil, huma.Error503ServiceUnavailable("share service unavailable")
        }
        caller, err := callerFromCtx(ctx)
        if err != nil {
            return nil, translateShareError(err)
        }
        prev, err := svc.PreviewScope(ctx, in.UUID, caller)
        if err != nil {
            return nil, translateShareError(err)
        }
        return &previewShareOutput{Body: toPreviewShareDTO(prev)}, nil
    })
}

func toPreviewShareDTO(p service.ScopePreview) previewShareDTO {
    out := previewShareDTO{
        Scope:    toScopeDTO(p.Scope),
        Warnings: p.Warnings,
    }
    out.Media = make([]previewMediaDTO, 0, len(p.Media))
    for _, m := range p.Media {
        out.Media = append(out.Media, previewMediaDTO{
            ID: m.ID, MediaType: string(m.MediaType), MimeType: m.MimeType,
            DisplayTime: m.DisplayTime,
            ThumbStatus: m.ThumbStatus, ThumbVersion: m.ThumbVersion,
        })
    }
    if p.Album != nil {
        out.Album = &previewAlbumDTO{
            ID: p.Album.ID, Name: p.Album.Name,
            ItemCount: p.Album.ItemCount, UpdatedAt: p.Album.UpdatedAt,
        }
    }
    return out
}
```

Add `registerSharesPreview(api, svc)` to the existing `registerShares` dispatcher.

- [ ] **Step 7: Regenerate OpenAPI and run tests**

```bash
make api-generate
go test ./internal/service/... ./internal/httpapi/... -run "Preview|TestSharesPreview" -count=1
```

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/service/share_service.go internal/service/share_service_test.go \
        internal/httpapi/shares.go internal/httpapi/shares_test.go internal/httpapi/openapi.json
git commit -m "share: PreviewScope + /api/v1/shares/{uuid}/preview with warnings"
```

---

## Task 21: Grantee `grantee_handle` on owner share responses

**Files:**
- Modify: `internal/httpapi/shares.go`
- Modify: `internal/httpapi/api.go`
- Modify: `internal/httpapi/shares_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/httpapi/shares_test.go`:

```go
func TestSharesListHydratesGranteeHandle(t *testing.T) {
    // Seed a principal_display row for the grantee, mint a scope, list
    // /api/v1/shares, assert the returned items carry grantee_handle.
}

func TestSharesGetHydratesGranteeHandle(t *testing.T) {
    // Same, but GET /api/v1/shares/{uuid}.
}

func TestSharesListNoDisplayRowEmptyGranteeHandle(t *testing.T) {
    // No row in principal_display → grantee_handle is absent from JSON.
}
```

- [ ] **Step 2: Run — expect FAIL**

Run: `go test ./internal/httpapi/... -run TestSharesListHydratesGranteeHandle -v`
Expected: FAIL.

- [ ] **Step 3: Implement handle hydration in `shares.go`**

Add `GranteeHandle string json:"grantee_handle,omitempty"` to `scopeDTO`. Plumb a `*share.PrincipalDisplayRepo` into `registerSharesList` / `registerSharesGet` via a new field on `Deps`:

```go
type Deps struct {
    // ... existing fields ...
    PrincipalDisplay *share.PrincipalDisplayRepo
}
```

Update `registerShares` to take `displayRepo *share.PrincipalDisplayRepo` and thread it down. Inside `registerSharesList`:

```go
// After rows are fetched:
out.Body.Items = make([]scopeDTO, 0, len(rows))
if displayRepo != nil {
    principals := make([]owners.Principal, 0, len(rows))
    for _, s := range rows {
        principals = append(principals, s.Grantee)
    }
    handles, err := displayRepo.GetBatch(ctx, principals)
    if err != nil {
        return nil, translateShareError(err)
    }
    for _, s := range rows {
        dto := toScopeDTO(s)
        dto.GranteeHandle = handles[s.Grantee]
        out.Body.Items = append(out.Body.Items, dto)
    }
} else {
    for _, s := range rows {
        out.Body.Items = append(out.Body.Items, toScopeDTO(s))
    }
}
```

In `registerSharesGet`:

```go
dto := toScopeDetailDTO(det)
if displayRepo != nil {
    handle, _, err := displayRepo.Get(ctx, det.Grantee)
    if err != nil {
        return nil, translateShareError(err)
    }
    dto.GranteeHandle = handle
}
```

- [ ] **Step 4: Run — expect PASS**

Run: `go test ./internal/httpapi/... -run "TestSharesList|TestSharesGet" -count=1`
Expected: PASS (existing share tests still pass; new hydration tests added this task pass).

- [ ] **Step 5: Regenerate OpenAPI and commit**

```bash
make api-generate
git add internal/httpapi/shares.go internal/httpapi/shares_test.go \
        internal/httpapi/api.go internal/httpapi/openapi.json
git commit -m "httpapi: hydrate scopeDTO.grantee_handle from principal_display"
```

---

## Task 22: Server wiring — header provider + display middleware + SharedReadService

**Files:**
- Modify: `internal/cli/server.go`
- Modify: `internal/config/config.go` (iff header-mode config keys are not already present)

- [ ] **Step 1: Verify / extend header-mode config**

Check `internal/config/config.go` for the existing identity-mode fields. The header-mode selection path is already wired from Plan A:

```go
// Expected shape (verify in current tree):
type Identity struct {
    Mode   string        // "stub" | "header"
    Stub   StubConfig
    Header HeaderConfig  // references identity.HeaderConfig (UserID, Hub, Handle, Scopes, RequestID headers)
    Guard  GuardConfig
}
```

If `HeaderConfig` does not exist, add it and plumb it through `config.Load`. A passing test in `internal/config/config_test.go` that loads a YAML with `identity.mode: header` and asserts the `Header` field populates is sufficient.

- [ ] **Step 2: Wire in `cli/server.go`**

Modify `internal/cli/server.go` at the place where the current `identity.Stub` is constructed. Add:

```go
var idp identity.Provider
switch cfg.Identity.Mode {
case "stub":
    idp = identity.NewStub(stubPrincipal(cfg), cfg.Identity.Stub.Handle)
case "header":
    guard, err := identity.NewGuard(cfg.Identity.Guard)
    if err != nil {
        return fmt.Errorf("identity guard: %w", err)
    }
    idp = identity.NewHeader(cfg.Identity.Header, guard)
default:
    return fmt.Errorf("unknown identity.mode: %s", cfg.Identity.Mode)
}
```

Construct `PrincipalDisplayRepo`, `ScopeResolver`, and `SharedReadService`:

```go
displayRepo := share.NewPrincipalDisplayRepo(db.WriteDB(), db.ReadDB())
resolver := share.NewScopeResolver(sharesRepo, nil) // nil → time.Now().UTC()
sharedSvc := service.NewSharedReadService(sharesRepo, mediaRepo, albumsRepo, store, resolver)
```

Pass them into `httpapi.Deps`:

```go
httpDeps := httpapi.Deps{
    Identity:         idp,
    // ... existing fields ...
    SharedRead:       sharedSvc,
    PrincipalDisplay: displayRepo,
}
```

No code change to the shares-preview route beyond what T20 added.

- [ ] **Step 3: Run existing server tests**

Run: `go test ./internal/cli/... -count=1`
Expected: PASS. The existing stub-mode server test path is unchanged; the header-mode branch is only exercised by T22's e2e.

- [ ] **Step 4: Commit**

```bash
git add internal/cli/server.go internal/config/
git commit -m "cli/server: wire Header identity provider, PrincipalDisplayRepo, SharedReadService"
```

---

## Task 23: End-to-end header-mode test

**Files:**
- Create: `internal/cli/e2e_shared_test.go`

This is the single integration test that proves `fotobank server` in header mode actually serves grantees. Unlike the handler-level tests, this test boots the server twice: once in stub mode to seed content via the real import pipeline, once in header mode to read it.

- [ ] **Step 1: Write the failing test**

Create `internal/cli/e2e_shared_test.go`:

```go
package cli_test

import (
    "context"
    "encoding/json"
    "io"
    "net/http"
    "net/http/httptest"
    "os"
    "path/filepath"
    "testing"

    "github.com/stretchr/testify/require"
)

// TestSharedE2EHeaderMode boots fotobank server in header mode against
// a DB that was pre-populated via the real import pipeline, then
// exercises /api/v1/shared/* with grantee X-Auth-* headers.
func TestSharedE2EHeaderMode(t *testing.T) {
    r := require.New(t)
    tmp := t.TempDir()

    // 1. Write a stub-mode config, point it at tmp (DB + NAS both in tmp).
    stubCfg := filepath.Join(tmp, "stub.yaml")
    writeStubConfig(t, stubCfg, tmp, "h", "alice", "Alice")

    // 2. Seed a media via `fotobank import` against a fixture file.
    fixture := copyFixtureJPEG(t, tmp, "sample.jpg")
    runCLI(t, stubCfg, "import", fixture)

    // 3. Mint an album and a media_set scope with download.
    albumOut := runCLIJSON(t, stubCfg, "albums", "create", "--name", "Trip")
    var album struct{ ID string `json:"id"` }
    r.NoError(json.Unmarshal(albumOut, &album))
    mediasOut := runCLIJSON(t, stubCfg, "media", "list")
    var medias struct{ Items []struct{ ID string } }
    r.NoError(json.Unmarshal(mediasOut, &medias))
    r.NotEmpty(medias.Items)

    runCLI(t, stubCfg, "albums", "add-media", "--album", album.ID,
        "--media", medias.Items[0].ID)

    scopeOut := runCLIJSON(t, stubCfg, "shares", "create",
        "--album", album.ID,
        "--grantee-hub", "h", "--grantee-user-id", "bob",
        "--allow-download")
    var scope struct{ UUID string `json:"uuid"` }
    r.NoError(json.Unmarshal(scopeOut, &scope))

    // 4. Flip the scope to broker_status='active' directly in the DB
    // (production runs the worker; for the test we simulate the post-
    // publish terminal state so /shared/* authorises).
    bumpScopeActive(t, tmp, scope.UUID)

    // 5. Start the server in header mode against the same DB.
    headerCfg := filepath.Join(tmp, "header.yaml")
    writeHeaderConfig(t, headerCfg, tmp)
    srv, shutdown := startServer(t, headerCfg)
    t.Cleanup(shutdown)

    // 6. Helper: new request with grantee headers + scope header.
    newReq := func(path string) *http.Request {
        req := httptest.NewRequest(http.MethodGet, srv.URL+path, nil)
        req.Header.Set("X-Auth-Hub", "h")
        req.Header.Set("X-Auth-User-ID", "bob")
        req.Header.Set("X-Auth-Handle", "Bob")
        req.Header.Set("X-Auth-Scopes", scope.UUID)
        req.Header.Set("X-Proxy-Signature", testGuardSignature(t, req))
        req.RequestURI = ""
        return req
    }

    // 7. GET /api/v1/shared/scopes returns our scope.
    res, err := srv.Client().Do(newReq("/api/v1/shared/scopes"))
    r.NoError(err)
    defer res.Body.Close()
    r.Equal(http.StatusOK, res.StatusCode)
    body, _ := io.ReadAll(res.Body)
    r.Contains(string(body), scope.UUID)

    // 8. GET /api/v1/shared/albums/{id}/media.
    res2, err := srv.Client().Do(newReq("/api/v1/shared/albums/" + album.ID + "/media"))
    r.NoError(err)
    defer res2.Body.Close()
    r.Equal(http.StatusOK, res2.StatusCode)

    // 9. GET /api/v1/shared/media/{id}/original returns 200 + body.
    res3, err := srv.Client().Do(newReq("/api/v1/shared/media/" + medias.Items[0].ID + "/original"))
    r.NoError(err)
    defer res3.Body.Close()
    r.Equal(http.StatusOK, res3.StatusCode)
    bs, err := io.ReadAll(res3.Body)
    r.NoError(err)
    r.NotEmpty(bs)

    // 10. Request with NO X-Auth-Scopes → 404 on detail, empty list.
    reqNoScopes := newReq("/api/v1/shared/scopes")
    reqNoScopes.Header.Del("X-Auth-Scopes")
    res4, err := srv.Client().Do(reqNoScopes)
    r.NoError(err)
    defer res4.Body.Close()
    r.Equal(http.StatusOK, res4.StatusCode)
    // items: []
    body4, _ := io.ReadAll(res4.Body)
    r.Contains(string(body4), `"items":[]`)

    // 11. Direct-access attempt without the proxy signature → 403.
    reqBad := httptest.NewRequest(http.MethodGet, srv.URL+"/api/v1/shared/scopes", nil)
    reqBad.Header.Set("X-Auth-Hub", "h")
    reqBad.Header.Set("X-Auth-User-ID", "bob")
    reqBad.Header.Set("X-Auth-Scopes", scope.UUID)
    reqBad.RequestURI = ""
    res5, err := srv.Client().Do(reqBad)
    r.NoError(err)
    defer res5.Body.Close()
    r.Equal(http.StatusForbidden, res5.StatusCode)
}
```

Helpers referenced above:
- `writeStubConfig(t, path, root, hub, userID, handle)` — writes `identity.mode: stub` YAML.
- `writeHeaderConfig(t, path, root)` — writes `identity.mode: header` YAML with the header names and guard HMAC secret used by `testGuardSignature`.
- `runCLI(t, cfg, args...)`, `runCLIJSON(t, cfg, args...)` — invoke `fotobank` via `internal/cli.Run(args)` (already used by E1 e2e tests). JSON variant returns the captured stdout bytes.
- `copyFixtureJPEG` — already used in ingest tests; share it by importing from `internal/testutil`.
- `bumpScopeActive` — a direct DB nudge: flip `broker_status`, set `broker_registered_at`/`broker_granted_at`. Same as E1's shared test helper.
- `startServer(t, cfg)` — boots `fotobank server` in a goroutine, returns `*httptest.Server` + shutdown func. E1's `newServerEnv` equivalent.
- `testGuardSignature(t, req)` — computes the HMAC the guard expects (using the secret in `header.yaml`).

Adapt to whatever the current `internal/identity/guard.go` expects. If the guard uses a simpler check (e.g. a single expected header value), use that.

- [ ] **Step 2: Run — expect FAIL**

Run: `go test ./internal/cli/... -run TestSharedE2EHeaderMode -v`
Expected: FAIL (at least because `bumpScopeActive` and the server-mode helpers are new).

- [ ] **Step 3: Implement the helpers**

Add the missing helpers either at the top of `e2e_shared_test.go` or in a new `internal/cli/e2e_testutil.go` (test-file-tagged). Keep them terse — the point of this task is proving the integration, not inventing new test infrastructure.

- [ ] **Step 4: Run — expect PASS**

Run: `go test ./internal/cli/... -run TestSharedE2EHeaderMode -v`
Expected: PASS.

- [ ] **Step 5: Run the full suite**

Run: `go test ./... -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/cli/e2e_shared_test.go internal/cli/e2e_testutil.go
git commit -m "cli: end-to-end header-mode test for /api/v1/shared/*"
```

---

## Done criteria

Plan E2 is complete when:

1. Every task's tests pass and `go test ./... -count=1` is green on master.
2. `make lint` passes with no warnings.
3. `internal/httpapi/openapi.json` includes `/api/v1/shared/*` operations and the owner `/preview` route.
4. The e2e test (T23) exercises the full grantee read path through a header-mode server boot.
5. No new DB migrations were added (the spec target — verify with `ls internal/db/migrations/`).
6. The vision doc's §6.2 flow (proxy → headers → scope-gated reads) is demonstrably working end-to-end.
