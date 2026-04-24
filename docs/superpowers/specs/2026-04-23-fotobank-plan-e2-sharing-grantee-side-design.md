# Plan E2: Sharing — Grantee-Side Design Spec

**Date:** 2026-04-23
**Scope:** Phase 2 sharing subsystem, grantee-side. Turns the scopes
minted in Plan E1 into real, usable access: header-mode identity wiring,
a cross-owner scope resolver, grantee-scoped read endpoints under
`/api/v1/shared/*`, and owner-side share listing/preview polish.
**Depends on:** Plans A–D and Plan E1 — all complete.
**Followed by:** none planned. After E2 the sharing subsystem is
feature-complete for the vision doc's §5–§6.

## 1. Goal and non-goals

### Goal

Ship the grantee-side half of sharing. After E2, a principal who holds
one or more live scopes granted to them can point a browser (or any
HTTP client routed through the trusted reverse proxy) at this fotobank
instance and:

- list the scopes they have been granted (their "share inbox");
- list and inspect the media / albums that those scopes make visible to
  them, **without** seeing anything outside their grants, **without**
  seeing any of the owner's private metadata;
- download full-size bytes for items whose governing scope has
  `allow_download = true` (with HTTP `Range` support for media players);
- read thumbnail bytes for every item they can see, regardless of
  `allow_download` (thumbs are how you navigate a grant);
- hit an existence-oracle-free 404 on any UUID they have no scope for,
  indistinguishable from "row does not exist".

The owner also gets two small quality-of-life additions for share
minting/management:

- a read-only `GET /api/v1/shares/{uuid}/preview` that shows exactly
  what a grantee will see (media_ids, album_live materialised membership,
  `can_download` projection), so the owner can sanity-check a scope
  before a grantee complains;
- grantee display-handle surfacing on owner-facing share responses,
  backed by the existing `principal_display` cache.

At the end of E2, the flow from vision §6.2 ("grantee arrives at proxy
with a token, proxy rewrites it into `X-*` headers, fotobank treats
those headers as the caller identity plus an allowlist of authorised
scopes") is fully implemented and exercised by an end-to-end test.

### Non-goals

- **Cross-owner *writes*.** Grantees never mutate anything. No album
  edits, no favourites, no annotations, no comments. Every
  `/api/v1/shared/*` handler is `GET`.
- **Share-side search / filtering.** The listing endpoints take at most
  a pagination cursor and (for media) an album filter. No free-text
  search, no date-range filter, no tag filter. The owner-facing search
  path is not extended to cross grant boundaries.
- **Expiry enforcement changes.** E1's position was "expiry is stored
  but not acted on." E2 changes this for the *read* side only: the
  resolver refuses any scope whose `expires_at` is in the past. The
  owner-facing store/worker behaviour is unchanged; a scope that
  expires does not auto-revoke on the broker side in E2. (A reaper /
  auto-revoke job remains future work.)
- **Grantee-side aggregation.** No "all my shared media combined into
  one timeline" view that spans multiple owners. If the grantee wants
  a unified feed across owners, that is a client-side concern. Each
  `/api/v1/shared/*` response is scoped to one owner (the owner who
  minted the scopes the grantee chose to present).
- **Rich proxy-identity negotiation.** The identity provider is the
  simple header reader from Plan A's `identity.Header`. No OIDC flow,
  no JWT validation, no token refresh. The proxy is trusted to have
  already authenticated the caller.
- **Scope-level ACL override.** A scope is a single allow bit
  (`allow_download`). There is no "watermark only", no "blur faces",
  no per-media override within a scope. If that is ever needed it's a
  new scope shape, not a flag.
- **Video streaming beyond `Range` on originals.** No HLS, no DASH, no
  server-side transcoding. Grantees get the same `ReadRange` semantics
  the owner uses.
- **Rate limiting or abuse controls.** Deferred to ops / reverse proxy.
- **Grantee-visible scope events / audit log.** Neither side sees a
  history of "this scope was accessed N times." Add later if needed.

## 2. Prior art already in the tree

Most of the plumbing E2 relies on already landed in earlier plans; the
main E2 work is wiring and new read-path surfaces, not new
infrastructure. Specifically:

- **`principal_display` table** lives in
  `internal/db/migrations/000001_initial_schema.up.sql:14`. Schema is
  `(hub, user_id, handle, cached_at)` with PK `(hub, user_id)`. E2
  adds a repo and a caching middleware; no new migration.
- **`scopes` / `scope_media` tables, including `scopes_grantee_idx`**
  (`WHERE revoked_at IS NULL`) are from Plan A. The grantee index is
  dormant in E1 and becomes the primary index for the E2 read path.
- **`identity.Identity` carries `Scopes []string`** already
  (`internal/identity/identity.go:30-34`). The `Header` provider
  (`internal/identity/header.go`) reads a whitespace-separated
  `X-Auth-Scopes` list and feeds it into `Identity.Scopes` without
  validation. E2 is the first caller that reads `Identity.Scopes` and
  treats it as authoritative-subject-to-validation.
- **`identity.Guard`** (`internal/identity/guard.go`) enforces the
  direct-access block from the vision doc. Already required by the
  `Header` provider's constructor; E2 does not change it.
- **`storage.Store`** takes `owners.Principal` on every method
  (`internal/storage/storage.go:45-50`), so cross-owner reads are a
  service-level auth decision, not a storage-level one. The byte
  handlers for `/original` and `/thumb` bytes call `ReadRange` /
  thumb-cache lookups that are already owner-parameterised.
- **Plan E1 `share.Repo`** has `GetByUUID` (returns `ScopeDetail`
  including `MediaIDs` for media_set), `ListByOwner`, and the state
  transition mutators. E2 adds sibling query methods for the
  grantee-side read path without touching the mutator surface.
- **`album.Repo.GetDetailByID` / `media.Repo.GetByID` / `List`**
  exist from Plans B–D. E2 uses them plus new batch-fetch helpers that
  preserve caller-supplied ID order.
- **`internal/service/album_service.go` and
  `internal/service/share_service.go`** exist as the auth boundary for
  the owner. E2 adds `SharedReadService` as a sibling boundary for the
  grantee; it does **not** call existing owner-scoped services, which
  would be wrong (they would reject cross-owner reads).

## 3. Architecture and package layout

New and modified files:

```
internal/share/
├── resolver.go              — *ScopeResolver: ValidateHeaderScopes,
                                ResolveAll, CheckMediaAccess, CheckAlbumAccess
├── resolver_test.go         — table-driven tests of the resolver
├── repo.go                  — add ValidateHeaderScopes,
                                ListSharedMediaIDs, ListSharedAlbumIDs,
                                ExpandScope, CountSharedMediaByScope
├── repo_test.go             — add coverage for the new queries
└── display.go               — *PrincipalDisplayRepo: Upsert, Get, GetBatch
└── display_test.go          — coverage for principal_display helpers

internal/service/
├── shared_read_service.go   — *SharedReadService: grantee-scoped reads
│                              backed by *ScopeResolver and repos
├── shared_read_service_test.go
├── share_service.go         — add PreviewScope (owner-only expansion)
                                and hook PrincipalDisplayRepo into list/get
                                for grantee_handle surfacing
└── share_service_test.go    — extend for PreviewScope + display hook

internal/httpapi/
├── shared.go                — huma-registered routes under /api/v1/shared
├── shared_test.go
├── shares.go                — add /preview route + grantee_handle field
├── shares_test.go           — extend
├── originals.go             — NEW: shared writeOriginalResponse helper
                                extracted from existing media/original
                                handler; Range parsing + Content-Range
├── media.go                 — original handler switches to
                                writeOriginalResponse; otherwise unchanged
├── thumbs.go                — extract writeThumbResponse helper mirror
                                (see §9.5); owner route unchanged
├── middleware.go            — WithPrincipalDisplayCache middleware
└── middleware_test.go

internal/identity/
└── stub.go                  — add NewStubWithScopes for handler tests
                                (does not affect production wiring)

internal/cli/
├── server.go                — wire identity.Header when configured,
                                wire WithPrincipalDisplayCache
└── e2e_shared_test.go       — header-mode end-to-end test
```

No new database migrations. All work reuses existing tables and indexes
except for one index audit called out in §5.

### 3.1 Layering

The existing three-tier layering is preserved with one addition:

- **`share.Repo`** (DB-only) gains new query methods, but every query
  takes the grantee principal plus validated scope UUIDs explicitly.
  The repo never trusts a raw header — validation is the resolver's
  job.
- **`share.ScopeResolver`** is a new component at the service tier.
  It owns one decision: "given a caller and the scopes their request
  presented, which rows may they see and with what download bit?" It
  does not read media/album/storage state on its own; it only reads
  `scopes` and `scope_media`. Its output is consumed by the service
  layer to gate other repo calls.
- **`service.SharedReadService`** is the grantee-side auth boundary.
  It holds a `*ScopeResolver` plus the same repos the owner services
  hold (`*share.Repo`, `*media.Repo`, `*album.Repo`, `storage.Store`).
  Every exported method takes `caller owners.Principal` **and**
  `headerScopes []string` (the value from `Identity.Scopes`). It never
  calls the owner-scoped services, because those enforce `caller ==
  owner` and would reject cross-owner reads.
- **`service.ShareService`** gains one new method — `PreviewScope` —
  that materialises what a grantee would see. This is owner-scoped
  (caller must own the scope) and uses a separate expansion path; it
  does **not** call the resolver with a caller-as-grantee hack.
- **`httpapi.shared.go`** registers huma routes under `/api/v1/shared`.
  Every handler extracts `Identity` from context and calls
  `SharedReadService` with `(identity.Principal.OwnersPrincipal(),
  identity.Scopes, ...)`.

The trust boundary is preserved: no grantee input is used as a query
predicate without going through the resolver, and no cross-owner
storage read happens without a prior `AccessDecision.Authorized`.

## 4. Domain types

### 4.1 Resolver

```go
// internal/share/resolver.go

// AccessPath is one authorisation route from a caller to a specific
// target. Overlapping scopes are common (a user shares Album A with
// allow_download=false on Monday, then a media_set containing one of
// A's items with allow_download=true on Tuesday) and must be exposed
// to callers so CanDownload can OR across paths.
type AccessPath struct {
    // ScopeUUID identifies the scope that authorises this path.
    ScopeUUID string
    // AlbumID is set iff the path runs through an album_live scope.
    // For media_set paths this is nil.
    AlbumID *string
    // AllowDownload is the scope's own allow_download bit.
    AllowDownload bool
}

// AccessDecision is the answer to a targeted check.
type AccessDecision struct {
    // Authorized is true iff at least one live validated scope grants
    // the caller access to the target.
    Authorized bool
    // Paths enumerates every scope that authorises the target. Empty
    // slice iff Authorized == false.
    Paths []AccessPath
}

// CanDownload is the OR across paths. A single allow_download=true
// path is sufficient to authorise downloads.
func (d AccessDecision) CanDownload() bool

// ResolvedScopes is the fully materialised view used by the listing
// endpoints. It is equivalent to calling CheckMediaAccess against
// every media id the caller can see, but produced by a single pair
// of bulk queries.
type ResolvedScopes struct {
    // ScopeUUIDs is the validated-and-live subset of the caller's
    // header scopes, sorted ascending. Safe to use as a predicate
    // value in follow-up queries.
    ScopeUUIDs []string
    // Owner is the single owner of all validated scopes. If the
    // header carried scopes from multiple owners, the resolver
    // keeps the lexicographically smallest retained-owner tuple
    // and drops the rest (§6.1).
    Owner owners.Principal
    // AllowDownload is true iff any validated scope has
    // allow_download = true. This is a coarse hint for UI; the
    // per-media AccessDecision is still authoritative.
    AllowDownload bool
}

// ScopeResolver validates presented scopes and answers access
// questions. It reads only scopes / scope_media; it never touches
// media, album, or storage state.
type ScopeResolver struct {
    shares *share.Repo
    now    func() time.Time
}

// MaxHeaderScopes caps the number of distinct scope UUIDs the
// resolver will consider from a single request. Presentations
// beyond this are truncated (earliest-in-list wins after dedupe)
// without an error, because headers can legitimately be set by
// misconfigured proxies and the grantee should still see as much
// as we can give them.
const MaxHeaderScopes = 100

// ResolveAll validates the presented scopes and returns the
// fully-expanded view. headerScopes is the raw value of
// Identity.Scopes; the resolver dedupes, caps at MaxHeaderScopes,
// drops syntactically-invalid UUIDs silently, then filters to
// live scopes the caller is actually the grantee on. Returns
// ResolvedScopes with empty ScopeUUIDs (and Authorized-effectively-
// false) when no live scopes remain.
func (r *ScopeResolver) ResolveAll(
    ctx context.Context,
    caller owners.Principal,
    headerScopes []string,
) (ResolvedScopes, error)

// CheckMediaAccess answers "can caller see media mediaID via one of
// these presented scopes?" It is cheaper than ResolveAll for the
// per-item endpoints (/shared/media/{id}[/thumb|/original]) because
// it does not materialise the full visible set — it intersects
// validated scopes with the item's coverage on the DB side.
//
// A media id is covered iff:
//   • there is an album_live scope owned by the caller-scope-owner
//     and that scope's album contains mediaID; or
//   • there is a media_set scope owned by the caller-scope-owner
//     with mediaID in its scope_media membership.
func (r *ScopeResolver) CheckMediaAccess(
    ctx context.Context,
    caller owners.Principal,
    headerScopes []string,
    mediaID string,
) (AccessDecision, error)

// CheckAlbumAccess answers "can caller see album albumID's
// metadata and contents?" It checks **album_live only** — a
// media_set scope does not imply album visibility even if every
// member happens to belong to that album. (This matches the
// vision doc: album_live is the "album share" primitive; media_set
// is a "pick exact items" primitive.)
func (r *ScopeResolver) CheckAlbumAccess(
    ctx context.Context,
    caller owners.Principal,
    headerScopes []string,
    albumID string,
) (AccessDecision, error)
```

The resolver is stateless and safe for concurrent use by many
requests (it holds only a `*share.Repo`).

### 4.2 SharedReadService

```go
// internal/service/shared_read_service.go

// SharedReadService is the auth boundary for every /api/v1/shared/*
// read. It depends on the resolver for authorisation and on the
// same repos the owner services use for data — but it never calls
// the owner services, because those enforce caller == owner.
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
) *SharedReadService

// ListScopes returns every live scope whose UUID appears in
// headerScopes and whose grantee == caller. Non-live or unowned
// scope UUIDs are silently dropped. The ItemCount on each returned
// entry is populated (album_live → album.item_count, media_set →
// len(scope_media)).
func (s *SharedReadService) ListScopes(
    ctx context.Context,
    caller owners.Principal,
    headerScopes []string,
) ([]SharedScope, error)

// GetScope returns a single scope by UUID, enforcing that the UUID
// is present in headerScopes (so a caller who has two scopes from
// the same owner cannot probe for a third by UUID). MediaIDs is
// populated for media_set; for album_live it is empty.
func (s *SharedReadService) GetScope(
    ctx context.Context,
    caller owners.Principal,
    headerScopes []string,
    uuid string,
) (SharedScopeDetail, error)

// ListAlbums returns every distinct album that a live album_live
// scope in headerScopes authorises. Ordered by updated_at DESC,
// then id ASC.
func (s *SharedReadService) ListAlbums(
    ctx context.Context,
    caller owners.Principal,
    headerScopes []string,
) ([]SharedAlbum, error)

// GetAlbum returns album metadata iff a live album_live scope for
// albumID appears in headerScopes.
func (s *SharedReadService) GetAlbum(
    ctx context.Context,
    caller owners.Principal,
    headerScopes []string,
    albumID string,
) (SharedAlbumDetail, error)

// ListAlbumMedia returns the media in an album (respecting the
// same visibility check as GetAlbum), paginated by display_time
// DESC / id ASC with a limit+1 cursor. Each media's can_download
// is the album-scope's allow_download OR'd with any media_set
// scope that also covers the same id.
func (s *SharedReadService) ListAlbumMedia(
    ctx context.Context,
    caller owners.Principal,
    headerScopes []string,
    albumID string,
    cursor SharedMediaCursor,
) ([]SharedMedia, SharedMediaCursor, error)

// ListMedia returns every distinct media id the caller can see
// across all validated scopes, deduped across album_live and
// media_set sources, paginated by display_time DESC / id ASC with
// a limit+1 cursor. can_download is MAX(allow_download) across
// covering scopes.
func (s *SharedReadService) ListMedia(
    ctx context.Context,
    caller owners.Principal,
    headerScopes []string,
    cursor SharedMediaCursor,
) ([]SharedMedia, SharedMediaCursor, error)

// GetMedia returns media metadata iff CheckMediaAccess(mediaID)
// is authorised.
func (s *SharedReadService) GetMedia(
    ctx context.Context,
    caller owners.Principal,
    headerScopes []string,
    mediaID string,
) (SharedMedia, error)

// OpenOriginal returns the storage reader for the full-resolution
// bytes, gated by CheckMediaAccess *and* CanDownload. The caller
// passes offset/length; SharedReadService forwards them verbatim
// to storage.Store.ReadRange. Returns errs.ErrPermissionDenied
// when access exists but download is disallowed (mapped to 403),
// errs.ErrNotFound otherwise (mapped to 404).
func (s *SharedReadService) OpenOriginal(
    ctx context.Context,
    caller owners.Principal,
    headerScopes []string,
    mediaID string,
    offset, length int64,
) (io.ReadCloser, media.Media, error)

// OpenThumb returns the thumb reader, gated only by
// CheckMediaAccess (thumbs ignore allow_download). The signature
// mirrors ThumbService.Get: the media row comes back alongside the
// reader so the handler can derive ETag/Last-Modified headers
// without a second repo round-trip. size and version come from the
// query string; a mismatch returns errs.ErrNotFound, same as owner.
func (s *SharedReadService) OpenThumb(
    ctx context.Context,
    caller owners.Principal,
    headerScopes []string,
    mediaID string, size thumb.Size, version int,
) (io.ReadCloser, media.Media, error)
```

Result structs (`SharedScope`, `SharedAlbum`, `SharedMedia`, …) are
defined alongside — see §4.3.

### 4.3 Result structs

These mirror the owner DTOs but omit fields the grantee must not see
(owner storage_key, import path, full filename, thumb timestamps
beyond version+status) and add the `CanDownload` projection from the
resolver.

```go
// SharedScope is the caller's view of a scope granted to them.
type SharedScope struct {
    UUID          string
    TargetType    share.TargetType
    Owner         owners.Principal   // scope owner (not the caller)
    TargetAlbumID *string            // nil for media_set
    AllowDownload bool
    Label         string
    ExpiresAt     *time.Time
    // ItemCount is album.item_count for album_live, len(scope_media)
    // for media_set. Populated by ListScopes only — GetScope does not
    // repopulate it (use len(MediaIDs) or the album lookup).
    ItemCount int
}

// SharedScopeDetail is a SharedScope with the media_set membership.
// MediaIDs is empty for album_live.
type SharedScopeDetail struct {
    SharedScope
    MediaIDs []string
}

// SharedAlbum is the caller's view of an album made visible via at
// least one album_live scope in their header.
type SharedAlbum struct {
    ID        string
    Name      string
    Owner     owners.Principal
    ItemCount int
    Cover     *SharedAlbumCover // nil when album has no cover
    CreatedAt time.Time
    UpdatedAt time.Time
    // CanDownload is the OR across every album_live scope in the
    // caller's header that points at this album id.
    CanDownload bool
}

// SharedAlbumDetail is the full view of a shared album. For E2 it
// adds no fields over SharedAlbum — it's a distinct type so the
// listing and detail response shapes stay separately versionable
// and the detail handler can add fields later (per-grantee
// listing cursor, etc.) without widening the listing payload.
type SharedAlbumDetail struct {
    SharedAlbum
}

type SharedAlbumCover struct {
    MediaID      string
    ThumbStatus  string
    ThumbVersion int
}

// SharedMedia is the caller's view of one media row made visible via
// at least one scope in their header. Download-only fields are
// omitted entirely (no storage path, no EXIF beyond the public
// projection, no owner-facing timestamps).
type SharedMedia struct {
    ID          string
    Owner       owners.Principal
    MediaType   media.Type
    MimeType    string
    // DisplayTime is COALESCE(timestamp, imported_at). The grantee
    // does not need to know which one it is.
    DisplayTime time.Time
    Width       *int
    Height      *int
    DurationMs  *int64
    ThumbStatus string
    ThumbVersion int
    // CanDownload is MAX(allow_download) across every covering scope.
    CanDownload bool
}

// SharedMediaCursor is the pagination cursor for ListMedia and
// ListAlbumMedia. The service treats an empty cursor as "start of
// first page." When the response has len == limit+1, the last row
// is dropped from the returned slice and its (DisplayTime, ID) are
// returned as the next cursor.
type SharedMediaCursor struct {
    AfterDisplayTime time.Time // zero means "no cursor"
    AfterID          string
    Limit            int       // clamped to 1..500 by the service
}
```

All `Owner owners.Principal` fields carry the *scope owner* — i.e.
the fotobank user who minted the scope — not the caller. This
intentionally exposes the owner's `(hub, user_id)` to the grantee;
the grantee already knows they received a share from that principal,
so revealing the owner identity is not a leak. The owner display
handle is *not* populated on the shared DTOs (deferred — the grantee
already knows who shared with them from the external system).

## 5. Database changes

**No new migrations.** E2's queries fit onto existing tables and
indexes.

Index audit against the schema in `000001_initial_schema.up.sql`:

- `scope_media` has PK `(scope_uuid, media_id)` and no secondary
  index. E2's scope_media queries always predicate on
  `scope_uuid` first (the validated set), so the PK fully covers
  `WHERE scope_uuid = ? AND media_id = ?` lookups and
  `scope_uuid IN (...)` scans. No new index.
- `album_media` has PK `(album_id, media_id)` and no standalone
  `media_id` index. E2 queries always scope `album_media` scans
  by a set of album_ids first (the grantee's album_live target
  set), so the PK is the correct index. No new index.
- `scopes_grantee_idx` is `(grantee_hub, grantee_user_id) WHERE
  revoked_at IS NULL` (from Plan A). E2's `ValidateHeaderScopes`
  SELECT is `WHERE uuid IN (?, ?, …) AND grantee_hub = ? AND
  grantee_user_id = ? AND revoked_at IS NULL AND broker_status =
  'active' AND (expires_at IS NULL OR expires_at > ?)`. Since the
  `IN` list is capped at 100 and the grantee index narrows the
  search, this is fine without a new composite. If profiling
  during implementation surfaces a hot scan we may revisit; the
  design commits to zero migrations at the spec level.

## 6. Resolver internals

### 6.1 Single-owner invariant

Every validated scope returned by the resolver shares a single
owner. **The production contract is that the reverse proxy
presents exactly one owner's scopes per request.** A grantee who
legitimately holds scopes from two owners must make two requests
with two different `X-Auth-Scopes` presentations (or the proxy
routes differently per owner). This is the expected shape of the
token-to-header rewrite in vision §6.2.

`ValidateHeaderScopes` filters by grantee but **not** by owner
(`WHERE uuid IN (...) AND grantee_hub = ? AND grantee_user_id = ?
AND revoked_at IS NULL AND broker_status = 'active' AND (...)`).
Nothing in that SQL would refuse a correctly-minted cross-owner
presentation. If the proxy is ever misconfigured and injects
scopes from multiple owners, the resolver applies the following
graceful-degradation rule in Go code, not in SQL:

1. Collect the distinct `(owner_hub, owner_user_id)` tuples across
   the validated rows.
2. If exactly one owner remains, proceed normally.
3. If more than one owner remains, **select the owner with the
   lexicographically smallest `(owner_hub, owner_user_id)` tuple**
   and drop every scope belonging to the other owners.
4. Log a `warn` with the caller principal, the dropped owner
   tuples, and the retained owner tuple.

Rationale: arbitrary silent selection would hide valid grants
randomly per request; lexicographic order is deterministic, so the
same grantee with the same header always sees the same subset, and
ops can reproduce and fix the proxy config. The dropped-owners log
is the operator signal.

This rule is visible on `ResolvedScopes.Owner`. Follow-up queries
(listing, per-item checks) thread that single owner through every
predicate. The invariant is not a security property — cross-owner
presentation never leaks data; it only constrains which of the
caller's legitimately-granted scopes they see in one request.

### 6.2 What counts as "live"

The resolver's live-scope predicate is:

- `revoked_at IS NULL`, **and**
- `broker_status = 'active'`, **and**
- `expires_at IS NULL OR expires_at > now()`.

All three must hold. A scope that is `pending` (broker hasn't
confirmed yet), `failed`, `revoking`, or `revoked_remote` is
treated as not-live and silently dropped during validation; the
grantee sees the same 404 / empty-list they would see for a scope
they don't hold at all. Expiry is the same — an expired scope is
dropped. This is unilateral: expiry does not require the outbox
worker to have revoked the broker-side grant. E1 explicitly left
expiry un-enforced; E2 enforces it for reads only.

Rationale for gating on `broker_status = 'active'`: a `pending`
scope cannot be vouched for by the broker, so the reverse proxy
would refuse to mint a token containing it anyway. Admitting
`pending` rows at the resolver would require the fotobank instance
to decide a policy the broker already owns. Safer and simpler to
require the broker-side confirmation that E1's state machine
already produces.

### 6.3 `CheckMediaAccess` SQL shape

The resolver runs this in two passes so the single-owner
degradation rule applies uniformly (same discipline as
`ResolveAll`). Pass 1 validates the header scopes with grantee /
revocation / broker / expiry predicates; pass 2 is a per-item
coverage check against the already-retained-owner subset.

**Pass 1 — validate and degrade.** Same shape as
`ValidateHeaderScopes` used by `ResolveAll`:

```
SELECT uuid, owner_hub, owner_user_id, target_type,
       target_album_id, allow_download
FROM scopes
WHERE uuid IN (?, ?, …)      -- deduped-and-capped header set
  AND grantee_hub = ?
  AND grantee_user_id = ?
  AND revoked_at IS NULL
  AND broker_status = 'active'
  AND (expires_at IS NULL OR expires_at > ?)
```

Resolver code groups the result by `(owner_hub, owner_user_id)`,
selects the lexicographically smallest retained owner, and filters
the slice down to that owner's scopes (§6.1). Call that filtered
slice `retained`.

**Pass 2 — coverage check.** Parameterised by `retained` as a
VALUES-CTE and the retained owner. If `retained` is empty, skip
pass 2 entirely and return `AccessDecision{Authorized: false}` —
a literal empty `VALUES (...)` list is invalid SQL on SQLite, and
the empty case is trivially unauthorized by construction.

```
WITH validated(uuid, owner_hub, owner_user_id, target_type,
               target_album_id, allow_download) AS (
    VALUES (?, ?, ?, ?, ?, ?), …    -- retained rows
)
SELECT v.uuid, v.target_type, v.target_album_id, v.allow_download
FROM validated v
WHERE v.owner_hub = ? AND v.owner_user_id = ?  -- retained owner
  AND (
       -- media_set coverage
       (v.target_type = 'media_set' AND EXISTS (
           SELECT 1 FROM scope_media sm
           WHERE sm.scope_uuid = v.uuid AND sm.media_id = ?
       ))
    OR -- album_live coverage
       (v.target_type = 'album_live' AND EXISTS (
           SELECT 1 FROM album_media am
           WHERE am.album_id = v.target_album_id AND am.media_id = ?
       ))
  )
```

The retained-owner predicate is redundant with the VALUES-CTE
when the Go filter is correct, but we include it as a belt-and-
braces guard so a bug in the degradation code cannot leak a
dropped-owner scope at the DB layer.

`CheckAlbumAccess` (album_live only; §6.3 consumers) uses the
same VALUES-CTE shell and short-circuit rule, but swaps the
coverage predicate for a direct album-target match:

```
SELECT v.uuid, v.target_type, v.target_album_id, v.allow_download
FROM validated v
WHERE v.owner_hub = ? AND v.owner_user_id = ?
  AND v.target_type = 'album_live'
  AND v.target_album_id = ?
```

This authorizes the shared album itself rather than media-item
coverage, so an empty album_live scope still yields an
authorized `AccessDecision` (the grantee is allowed to see an
empty album page, they just have nothing to list). `media_id` is
not an input to `CheckAlbumAccess`.

Each surviving row becomes an `AccessPath`. Empty result set →
`AccessDecision{Authorized: false}`.

### 6.4 `ResolveAll` SQL shape

Two queries:

1. `ValidateHeaderScopes` — same validated CTE as above, returned
   as a slice.
2. `ListSharedMediaIDs(retained, cursor)` — given the
   retained-owner slice (live + granted + single owner, §6.3
   pass 1 + §6.1 degradation), a single SELECT that unions
   per-scope coverage and `GROUP BY media_id` with
   `MAX(allow_download)`:

```
SELECT m.id AS media_id,
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
WHERE m.owner_hub = ? AND m.owner_user_id = ?  -- retained owner
  -- Cursor: ordering is display_time DESC, id ASC. The strict
  -- "after cursor" condition for that order is
  -- (display_time < cursor_time) OR
  -- (display_time = cursor_time AND id > cursor_id).
  AND (
       ? = 0  -- sentinel for "no cursor"; bind 1 when cursor present
    OR COALESCE(m.timestamp, m.imported_at) < ?
    OR (COALESCE(m.timestamp, m.imported_at) = ?
        AND m.id > ?)
  )
GROUP BY m.id
ORDER BY display_time DESC, m.id ASC
LIMIT ?
```

The cursor block binds four parameters when a cursor is present
(the "has_cursor" int, `cursor_time`, `cursor_time`, `cursor_id`)
and binds `0, '', '', ''` on the first page. Callers materialise
the cursor time in UTC and use the exact same `COALESCE` on the
client side that the SQL uses, so a tie on `display_time` sorts
consistently across pages.

`validated` is passed in as a VALUES-CTE keyed by `uuid` (SQLite
driver); the CTE carries the already-degraded retained rows.

### 6.5 Cap and dedupe in the resolver

The MaxHeaderScopes cap and UUID dedupe happen exclusively in the
resolver, before any SQL runs. `share.Repo.ValidateHeaderScopes` takes
an already-deduped `[]string` and trusts it. Rationale: the repo's
job is "run this query"; the resolver's job is "turn request inputs
into safe query inputs." Keeping this separation means the repo stays
easy to unit-test and the cap/dedupe logic has one home.

Syntactically-invalid UUIDs (not 36 chars, not matching the UUID
regex) are dropped silently. We do not 400 — see §7.4.

## 7. HTTP surface

All routes are huma-registered. Path prefix `/api/v1/shared/`. Every
handler uses the `withIdentity` wrapper (existing) to pull `Identity`
from context; request bodies are unused (all GET).

### 7.1 Route list

| Method | Path | Handler |
|---|---|---|
| GET | `/api/v1/shared/scopes` | `listSharedScopes` |
| GET | `/api/v1/shared/scopes/{uuid}` | `getSharedScope` |
| GET | `/api/v1/shared/albums` | `listSharedAlbums` |
| GET | `/api/v1/shared/albums/{id}` | `getSharedAlbum` |
| GET | `/api/v1/shared/albums/{id}/media` | `listSharedAlbumMedia` |
| GET | `/api/v1/shared/media` | `listSharedMedia` |
| GET | `/api/v1/shared/media/{id}` | `getSharedMedia` |
| GET | `/api/v1/shared/media/{id}/thumb` | `getSharedThumb` |
| GET | `/api/v1/shared/media/{id}/original` | `getSharedOriginal` |

Plus owner-side:

| Method | Path | Handler |
|---|---|---|
| GET | `/api/v1/shares/{uuid}/preview` | `previewShare` |

### 7.2 Wire DTOs

Shared DTOs live in `internal/httpapi/shared.go` and are distinct
from the owner `scopeDTO` / `albumDTO` / `mediaDTO`. Reusing the
owner DTOs would be a leak risk (e.g. `Path` on `mediaDTO` discloses
owner storage layout).

```go
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
    ItemCount     int                `json:"item_count,omitempty"` // list only
    MediaIDs      []string           `json:"media_ids,omitempty"`  // detail only
}

type sharedAlbumCoverDTO struct {
    MediaID      string `json:"media_id"`
    ThumbStatus  string `json:"thumb_status"`
    ThumbVersion int    `json:"thumb_version"`
}

type sharedAlbumDTO struct {
    ID          string              `json:"id"`
    Name        string              `json:"name"`
    Owner       sharedPrincipalDTO  `json:"owner"`
    ItemCount   int                 `json:"item_count"`
    Cover       *sharedAlbumCoverDTO `json:"cover,omitempty"`
    CreatedAt   time.Time           `json:"created_at"`
    UpdatedAt   time.Time           `json:"updated_at"`
    CanDownload bool                `json:"can_download"`
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
```

### 7.3 Pagination

`GET /api/v1/shared/media` and `GET /api/v1/shared/albums/{id}/media`
share the pagination shape:

- `limit` (query, default 100, cap 500)
- `cursor_time` (query, ISO-8601; optional)
- `cursor_id` (query, media UUID; required iff `cursor_time` set)

Response shape:

```json
{
  "items": [...],
  "next_cursor_time": "2024-01-02T03:04:05Z",
  "next_cursor_id": "9f2...",
  "has_more": true
}
```

The fetch-limit+1 probe convention (already used by albums media
listing) keeps this cheap. `next_cursor_*` are omitted when
`has_more == false`.

### 7.4 Status codes

The surface deliberately behaves as an existence oracle only for
rows that the caller's scope already reveals. Anything else is 404.

| Situation | Code |
|---|---|
| Happy path | 200 |
| Unknown UUID (never existed, or existed but not in caller's scope) | 404 |
| Authorised read but `allow_download=false` on all paths (original only) | 403 |
| Malformed path params (not a UUID) | 404 (not 400 — same reason) |
| Malformed `X-Auth-Scopes` tokens | silently dropped, no error |
| Identity headers missing (`ErrIdentityMissing`) | 401 |
| Direct-access guard rejected (`ErrDirectAccessBlocked`) | 403 |
| Stub mode, empty `Identity.Scopes` (list routes) | 200 with `{"items": []}` |
| Stub mode, empty `Identity.Scopes` (detail/bytes routes) | 404 |
| Internal error | 500 |

The split between 401 and 403 for identity failures is already
implemented by `WithMiddleware`
(`internal/httpapi/middleware.go:23`): `ErrIdentityMissing` maps
to 401, `ErrDirectAccessBlocked` maps to 403. The E2 surface
inherits this behaviour.

The "403 only when authorised-but-not-downloadable" distinction
matters: returning 403 for "unknown uuid" would leak existence. The
original-bytes handler is the only place `allow_download` is
consulted; list/get/thumb routes ignore it.

### 7.5 Original-bytes streaming

`GET /api/v1/shared/media/{id}/original` supports HTTP `Range`
exactly the same way the owner route does. Implementation detail:
the existing owner handler currently parses `Range`, slices the
stream, and sets `Content-Range` / `206 Partial Content` directly;
that chunk of logic is extracted into a shared helper
`writeOriginalResponse(w, r, media, open func(offset, length int64)
(io.ReadCloser, error))` and both handlers call it. `storage.Store.
ReadRange` itself takes `(offset, length)` but does not know about
HTTP headers — the helper owns the HTTP translation layer.

`getSharedOriginal`'s `open` closure calls
`SharedReadService.OpenOriginal`, which runs access + download
checks before returning the reader.

### 7.6 Thumb streaming

`GET /api/v1/shared/media/{id}/thumb` serves the cached thumb from
the flash tier. The owner path already has `writeThumbResponse`-ish
logic inlined; E2 factors it out the same way as the original
helper so both owner and shared handlers use a single code path.
(If inlining is already minimal enough that the "helper" is three
lines, we skip the extraction and duplicate. Call at
implementation time.)

### 7.7 Owner preview endpoint

`GET /api/v1/shares/{uuid}/preview` is owner-only. Caller must own
the scope; cross-owner access returns 404 (via the existing
`errs.ErrNotFound` flow). Response body:

```go
type previewShareDTO struct {
    // Scope is the same owner-facing scope DTO returned by
    // GET /api/v1/shares/{uuid} (full broker fields, etc.).
    Scope scopeDTO `json:"scope"`
    // Media is the full materialised list of media ids the grantee
    // will see via this scope, with CanDownload projected at the
    // scope level. For album_live scopes this is the current
    // membership of the album; for media_set it is the
    // frozen-at-mint membership.
    Media []previewMediaDTO `json:"media"`
    // Album is present iff the scope is album_live.
    Album *previewAlbumDTO `json:"album,omitempty"`
    // Warnings surfaces non-fatal issues the owner should notice
    // before sharing the URL with the grantee.
    Warnings []string `json:"warnings,omitempty"`
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
```

Warnings the preview emits (list may grow — stay conservative):

- `"broker_not_active"` if `broker_status != active` (the grantee
  will 403/hang at the reverse proxy).
- `"scope_expired"` if `expires_at` is in the past.
- `"empty_album"` if `album_live` and the album is currently empty.
- `"missing_thumbs"` if more than 25% of media have
  `thumb_status != 'ready'`.

Preview does **not** use the resolver with a caller-as-grantee trick.
It calls a dedicated `share.Repo.ExpandScope(ctx, scopeUUID)`
materializer that produces the media id set for either target type
without consulting grantee identity, and `ShareService.PreviewScope`
wraps it with the owner-scoped auth check. This keeps the
resolver's grantee guarantee pure.

### 7.8 Owner scope display handle

`scopeDTO` (owner-facing) gains:

```go
GranteeHandle string `json:"grantee_handle,omitempty"`
```

Populated from the `principal_display` cache via
`PrincipalDisplayRepo.GetBatch`. Missing / stale cache entries are
left empty — the field is an affordance, not authoritative. The
owner-facing list endpoint populates this in one batch query per
response; the single-scope get uses a point lookup.

## 8. Middleware and wiring

### 8.1 Principal display cache middleware

The `principal_display` table is written by a new `WithPrincipalDisplayCache`
middleware installed in the HTTP chain. For every request that
resolves an identity, the middleware upserts a row:

```sql
INSERT INTO principal_display (hub, user_id, handle, cached_at)
VALUES (?, ?, ?, ?)
ON CONFLICT (hub, user_id) DO UPDATE SET
    handle = excluded.handle,
    cached_at = excluded.cached_at
WHERE principal_display.cached_at < excluded.cached_at;
```

The upsert runs synchronously inside the request handler
goroutine, before `next.ServeHTTP` is called, under a bounded
sub-context:

```go
upsertCtx, cancel := context.WithTimeout(r.Context(), 50*time.Millisecond)
defer cancel()
if err := repo.Upsert(upsertCtx, ident.Principal, now); err != nil {
    slog.Warn("principal_display upsert", "err", err, …)
}
next.ServeHTTP(w, r)
```

This is synchronous best-effort with bounded latency: the request
is delayed by at most 50ms on a slow upsert, and any error is
logged at `warn` and swallowed (never propagated to the client).
The bound is deliberate — we want the cache fresh, but a hung
DB must not hang the grantee's read path. The middleware only
upserts when `Identity.Principal.Handle` is non-empty; stub-mode
callers typically leave Handle empty, so no cache churn in dev.

The middleware has a rate-limit escape valve: it upserts at most
once per `(hub, user_id)` per process per minute (in-memory LRU with
a 10k cap). This prevents a burst of requests from hammering the
table. The LRU lives on the middleware struct — no new package.

### 8.2 Middleware ordering

The order in `httpapi.New` is:

```go
handler := http.Handler(mux)
// Wrapped first (inner of the two): reads identity from context
// and upserts principal_display. No-op when context has no
// Identity.
if displayRepo != nil {
    handler = WithPrincipalDisplayCache(displayRepo, logger)(handler)
}
// Wrapped second (outer): resolves identity and injects it into
// the context. Runs first at request time, so the display cache
// middleware (wrapped underneath) sees the resolved Identity.
if idp != nil {
    handler = WithMiddleware(idp)(handler)
}
return handler, nil
```

At request time, identity is resolved first, then the display
cache runs against whatever identity was produced, then the mux
dispatches. The display cache middleware is a no-op if the
context has no identity (preserves the 401 path).

### 8.3 Server wiring

`cmd/fotobank server` grows a branch: when `identity.mode = "header"`
in config, `internal/cli/server.go` constructs `identity.Header` with
the config-provided header names and the production `identity.Guard`.
The existing stub-mode path is unchanged.

A new `SharedReadService` is constructed in both modes (stub mode
just never sees requests with matching scopes, because the stub
principal's `Identity.Scopes` is always nil — §9.7). The huma route
registration for `/api/v1/shared/*` is unconditional; the resolver
guarantees stub callers see nothing.

### 8.4 `identity.NewStubWithScopes`

Purely for handler-level tests. A test-only constructor that wraps
`NewStub` and annotates the returned identity with a `Scopes` slice
on every request. Not used by production wiring; lives in
`internal/identity/stub.go` with a comment declaring test-only
intent. The alternative (building a fake Header provider in tests)
pulls in guard setup and header parsing for no gain.

## 9. Contract details

### 9.1 `writeOriginalResponse` and `writeThumbResponse`

Extracted from `internal/httpapi/media_original.go` and
`internal/httpapi/media_thumb.go`. The helpers own only the
body-streaming semantics (Range parsing, `Content-Range` / 206 /
416 for originals; plain copy for thumbs). **They do not set
`Cache-Control`, `Vary`, `Content-Type`, `ETag`, or
`Last-Modified`** — those are caller responsibilities. Each
handler sets its own cache policy before invoking the helper, so
the owner path can keep `private, max-age=31536000, immutable`
and the shared path can pick something tighter.

```go
// writeOriginalResponse streams the original bytes, honouring an
// HTTP Range request when present. Caller has already performed
// auth, written ETag/Last-Modified/Cache-Control/Content-Type
// headers, and handled any If-None-Match short-circuit. open is
// called exactly once after Range parsing succeeds; it returns the
// (offset, length)-sliced reader. length < 0 means "to EOF".
//
// Sets only Content-Length, Content-Range (206), Accept-Ranges
// (if not already set by caller), and the response status.
func writeOriginalResponse(
    w http.ResponseWriter,
    r *http.Request,
    m media.Media,
    open func(offset, length int64) (io.ReadCloser, error),
)

// writeThumbResponse streams cached thumb bytes. Caller has
// already performed auth and written all response headers. Thumbs
// are small enough that Range is not honoured; on success the
// helper sets Content-Length (if the reader's size is known) and
// 200, then io.Copy.
func writeThumbResponse(
    w http.ResponseWriter,
    r *http.Request,
    open func() (io.ReadCloser, error),
)
```

Caller-side `Cache-Control` for the shared routes:

- `/shared/media/{id}/original`: `private, max-age=31536000,
  immutable` + `Vary: X-Auth-Scopes`. The URL carries no `?v=`
  parameter; bytes for a given media id never change (the
  checksum column is the physical-bytes identity), so `immutable`
  is safe. The `Vary` ensures a shared HTTP cache (reverse proxy,
  CDN, browser) never serves another grantee's cached body.
- `/shared/media/{id}/thumb`: `private, max-age=31536000,
  immutable` + `Vary: X-Auth-Scopes`. Version comes in on `?v=`
  (same as the owner route), so regenerates force a new URL.

The owner routes keep their existing `Cache-Control: private,
max-age=31536000, immutable` with no `Vary` header — nothing
changes in `media_original.go` or `media_thumb.go` beyond the
body-streaming extraction.

### 9.2 `/shared/media` default sort and cursor

Default sort: `display_time DESC, id ASC`, where `display_time =
COALESCE(timestamp, imported_at)`. Cursor uses
`(cursor_time, cursor_id)` as a strict `<` tuple. Limit clamps to
`[1, 500]`; default 100. Fetch probes `limit+1` to set
`has_more` / emit the next cursor.

### 9.3 Album-level `CanDownload`

A `sharedAlbumDTO.can_download` field set to `true` iff the
resolver has *any* album_live path to this album with
`allow_download = true`. This is independent of per-media
`can_download` (which may still differ if an overlapping
media_set also covers the item). The UI is expected to use album
`can_download` for the "download whole album" button and per-media
`can_download` for the per-item button.

### 9.4 Thumb content type

`/api/v1/shared/media/{id}/thumb` returns `Content-Type:
image/jpeg`, matching the owner route. The helper extraction
(§9.1) leaves Content-Type as a caller responsibility; both
handlers set it to `image/jpeg` before invoking the helper.

### 9.5 Thumb-vs-original symmetry

Thumbs ignore `allow_download`; originals respect it. This is a
deliberate asymmetry: a grantee needs thumbs to navigate the grant
(refusing thumbs for no-download shares makes the UI unusable),
but downloading originals is the thing `allow_download` governs.

### 9.6 Stub-mode behaviour of `/shared/*`

In stub mode the `Stub` provider returns the same principal with
`Scopes == nil` on every request. Every `/shared/*` request
therefore produces an empty `ResolvedScopes`:

- List routes (`/shared/scopes`, `/shared/albums`,
  `/shared/media`, `/shared/albums/{id}/media`) return `200` with
  an empty `items` array.
- Detail and bytes routes (`/shared/scopes/{uuid}`,
  `/shared/albums/{id}`, `/shared/media/{id}`, and the thumb /
  original byte routes) return `404`.

No special-casing in the handlers — the behaviour falls out of
the resolver returning an empty validated set.
`NewStubWithScopes` is only used by tests that want to exercise
the grantee path without spinning up header-mode.

### 9.7 Expiry surfacing

`SharedScope.ExpiresAt` is returned verbatim (it is part of the
grant the grantee has seen before). An expired scope is *not*
returned (resolver drops it), so the grantee never sees expired
`ExpiresAt` timestamps for scopes they cannot use. The owner still
sees expired scopes in their own `/api/v1/shares` listing (E1
behaviour).

### 9.8 Cross-origin / CSRF posture

`/shared/*` is read-only, so no CSRF token machinery is needed.
CORS is the reverse proxy's problem; fotobank does not emit CORS
headers.

## 10. Test strategy

Three tiers, each with a clear remit.

### 10.1 Unit: `share.ScopeResolver` and `share.Repo`

`resolver_test.go` is a table-driven test covering the branching
logic of `ValidateHeaderScopes` / `ResolveAll` / `CheckMediaAccess`
/ `CheckAlbumAccess`. Seed fixtures per case:

- one owner, multiple albums and media_sets
- caller as grantee on some scopes, not on others
- a cross-owner scope the caller does not hold
- scopes with past expires_at (dropped)
- scopes where `revoked_at IS NOT NULL` (dropped)
- header input with duplicates (deduped before query)
- header input over `MaxHeaderScopes` (cap in resolver, not repo)
- header input with syntactically-invalid UUIDs mixed in (dropped)
- overlapping scopes with different `allow_download` bits
  (verifies `MAX(allow_download)`)
- duplicate authorization rows — same media id covered by two
  scopes, must appear exactly once in `ListSharedMediaIDs`
  (verifies `GROUP BY media_id`)
- multi-owner presentation — header carries live scopes from two
  owners; resolver keeps only the lexicographically smallest
  `(owner_hub, owner_user_id)` tuple's scopes, drops the rest,
  emits a `warn` log (verifies §6.1 degradation rule)

Filter cases for `filters_non_active` (note: E1's enum does not
have a `publishing` state; test only the statuses that exist):
one row each for `pending`, `failed`, `revoking`, `revoked_remote`,
and one control row for `active`. The resolver must return only
the `active` row and drop the others. This matches the pinned
live predicate in §6.2: `broker_status = 'active'` is required;
every other status is not-live.

`repo_test.go` adds coverage for the new query methods
(`ValidateHeaderScopes`, `ListSharedMediaIDs`, `ListSharedAlbumIDs`,
`ExpandScope`, `CountSharedMediaByScope`) and verifies order
preservation for `GetByIDs`-style batch helpers.

### 10.2 HTTP: `/api/v1/shared/*` handlers

`shared_test.go` uses `identity.NewStubWithScopes` to bypass
header-mode guard setup. Each HTTP test seeds the DB with a fixed
owner, scope, and target, then fires one request and asserts
status code + response body. Coverage targets:

- every route on happy path
- every route on no-scope (404 or empty)
- every route on wrong-grantee (404)
- every route on expired scope (404)
- every route on revoked scope (404)
- original: 403 when authorised-but-no-download
- original: 206 + Content-Range on valid Range header
- original: 416 on unsatisfiable Range
- media list: pagination across two pages
- album-media list: pagination across two pages
- scopes list: multiple scopes returned in stable order
- **revocation immediacy**: revoke a scope mid-session and verify
  the next request sees 404 (no caching of resolver state between
  requests)

Tests run against `testutil.OpenTestDB(t)`. No `httptest.NewServer`
needed for pure handler tests — huma's in-process API tester works
and is the existing pattern.

A single `testFixtureHeaderMode` helper sets up the full
header-mode chain (guard + header provider + display middleware)
and is used only by the e2e test and one focused middleware test.
The rest of the shared HTTP tests use `NewStubWithScopes` and pay
no guard setup cost.

### 10.3 End-to-end: header-mode server

`internal/cli/e2e_shared_test.go` exercises the full path through
a real `fotobank server` invocation in header mode:

1. Start an httptest server configured with header mode, pointing
   at a fresh test DB.
2. Seed via **the existing CLI import pipeline** (not raw DB
   inserts): stub-mode config, `fotobank import` against a
   fixture directory, so that media rows *and* storage bytes both
   exist on the test NAS.
3. Switch config to header mode, restart the server.
4. Mint an album and a scope using the owner identity (still goes
   through the stub-mode CLI; scope minting is owner-side).
5. Issue requests to `/api/v1/shared/*` with the grantee's
   `X-Auth-*` headers set, including a non-empty `X-Auth-Scopes`.
6. Assert that:
   - `/shared/scopes` returns the expected scope
   - `/shared/albums/{id}/media` returns the album contents
   - `/shared/media/{id}/thumb` returns the thumb bytes
   - `/shared/media/{id}/original` returns the original bytes
     with a 200 (or 206 if `Range` is set)
   - a request with **no** `X-Auth-Scopes` returns empty lists /
     404s
   - a request with a bad direct-access attempt (header mode but
     guard would reject) returns 403 (per §7.4, matching
     WithMiddleware's mapping of `ErrDirectAccessBlocked`)

This one test is enough to catch integration regressions; the
handler-level tests stay focused and cheap.

### 10.4 Preview endpoint

`share_service_test.go` gains a `TestPreviewScope` that:

- returns owner-facing body for an `album_live` scope on a
  populated album
- returns owner-facing body for a `media_set` scope
- returns 404 (via `errs.ErrNotFound`) for cross-owner UUID
- surfaces `broker_not_active`, `scope_expired`, `empty_album`,
  and `missing_thumbs` warnings in isolation

## 11. Rejected alternatives

### 11.1 Listing shape: include scope context inline vs. discovered

**Option A (rejected):** every entry in `/shared/media` embeds the
scope UUID that authorised it. Rejected because overlapping scopes
make "the scope" ill-defined, and surfacing all scopes per item
adds payload weight without a client use case.

**Option B (chosen):** list entries carry only `can_download`; the
grantee can hit `/shared/scopes` separately for scope-level
context.

**Option C (rejected):** list entries carry both `can_download` and
a list of authorising scope UUIDs. Richer than B; rejected as
premature — we can add it when a client needs it.

### 11.2 `/shared/*` vs. scope-scoped sub-paths

**Option 1 (rejected):** `/api/v1/shares/{uuid}/media/{id}` and
`/api/v1/shares/{uuid}/album`, keying off the scope directly.
Cleaner URL semantics; rejected because overlapping scopes mean
the same media id reachable via two scopes would need two URLs,
and that breaks client-side caching.

**Option 2 (chosen):** `/api/v1/shared/media/{id}`, where the
resolver figures out which scope(s) authorise it from
`X-Auth-Scopes`.

### 11.3 Full materialisation in the resolver

**Rejected:** a single `ScopeResolver.Resolve(...) ([]mediaID,
[]albumID)` API. This forced a full expansion for every request,
including `/shared/media/{id}` where only one intersection is
needed. Replaced with the three-method API in §4.1
(`ResolveAll` + `CheckMediaAccess` + `CheckAlbumAccess`).

### 11.4 Weakening `ResolveAll` for preview

**Rejected:** running preview through the resolver with
`caller == owner` as a debug mode. Rejected because it bolts a
"disable the grantee check" flag onto an auth boundary that
should not have one. Preview uses a dedicated expansion path
(§7.7) that never touches the resolver.

### 11.5 Cross-owner aggregation in one response

**Rejected:** merging scopes from multiple owners into one
`/shared/*` response body. Rejected on both simplicity and
privacy grounds — see §6.1. Clients that need aggregation do it
themselves.

### 11.6 Media-set scope implies album visibility

**Rejected:** letting a `media_set` scope expose the set's *album*
(`GetAlbum` would return album metadata if any media_set covered
any of the album's items). Rejected as overly clever — a
media_set scope is "these exact items," not "a view of the album
these items came from." `CheckAlbumAccess` is album_live only.

## 12. Open questions and followups

- **Scope-level `item_count` cache.** `SharedScope.ItemCount` is
  computed per request (sub-query per scope). If the inbox grows
  big enough to matter, denormalise onto `scopes.item_count` as
  a future migration.
- **Display handle TTL.** `principal_display.cached_at` is used
  by `PrincipalDisplayRepo.Get` with no TTL today — stale handles
  remain visible forever. Revisit when someone changes their
  handle in the external system.
- **`Vary: X-Auth-Scopes` header length.** Browsers may
  normalise the vary key; not a correctness issue but may reduce
  cache hit rate. Revisit if profiling shows it matters.
