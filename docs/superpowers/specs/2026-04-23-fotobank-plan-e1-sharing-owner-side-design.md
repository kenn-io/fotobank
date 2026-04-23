# Plan E1: Sharing — Owner-Side Design Spec

**Date:** 2026-04-23
**Scope:** Phase 1 sharing subsystem, owner-side only. Ships scopes
CRUD, a broker outbox worker, and the CLI/HTTP surfaces an owner uses
to mint, retry, and revoke shares. Grantee-side reads (header-mode
identity, cross-owner media/album reads, `X-Auth-Scopes` enforcement)
are deferred to Plan E2.
**Depends on:** Plans A (foundation), B (import), C (thumbnails),
D (albums) — all complete.
**Followed by:** Plan E2 (grantee-side: header-mode identity provider,
scope-gated reads on `/api/v1/media` and `/api/v1/albums`).

## 1. Goal and non-goals

### Goal

Ship the owner-side half of sharing: let the owner of a fotobank
instance mint, inspect, retry, and revoke **scopes** — the grant
objects defined in the vision doc (§5.4). A scope is a row in the
existing `scopes` table plus (for `media_set` scopes) its
`scope_media` members; minting a scope also registers it with an
external broker via an interface fotobank owns. Plan E1 delivers:

- a `share.Repo` for the `scopes`/`scope_media` tables;
- a `ShareService` that is the auth boundary for every owner-facing
  share mutation;
- a `BrokerClient` interface plus two implementations — a production
  `NoopBroker` default and a scripted `brokertest.Fake` for tests;
- a single-tenant outbox worker (`internal/shareworker`) that drives
  every scope through its broker-side state machine;
- HTTP routes under `/api/v1/shares` (huma-registered) and a
  `fotobank shares …` cobra subcommand tree;
- a small migration adding a `broker_next_attempt_at` column plus
  index to make retry/backoff state explicit.

At the end of Plan E1, an operator can create an album (Plan D), run
`fotobank shares create --album <uuid> --grantee hub:user`, watch the
scope walk from `pending` through `active`, and revoke it with the
grantee eventually disappearing from the owner-facing share list. No
grantee can actually *use* the scope yet — that is Plan E2.

### Non-goals

- **Grantee reads.** Cross-owner media/album reads, the
  header-mode identity provider, `X-Auth-Scopes` parsing, and the
  Direct-Access Guard (vision §6.2) are Plan E2. Plan E1 does not
  modify `/api/v1/media` or `/api/v1/albums` response filtering.
- **Scope binding mutation.** `target_type`, `target_album_id`,
  `scope_media` rows, `grantee_*`, and `allow_download` are
  immutable for the life of the scope (vision §5.4). There is no
  "edit share" operation; the user revokes and re-creates.
- **Expiry enforcement.** `expires_at` is stored and returned but
  not enforced in E1. No reaper, no automatic state transition on
  expiry. Enforcement happens at grantee read time in E2. Active
  expired scopes still block album deletion (see §8) because the
  remote grant may still be live.
- **Owner-picked scope labels as first-class UX.** `label` is
  passed through verbatim. No search, no uniqueness, no length
  policing beyond a hard cap for storage sanity.
- **Album-snapshot scopes.** The vision doc (§5.4) floats a future
  "snapshot of album at mint time" target type; Plan E1 ships only
  the two types the schema already knows about (`album_live`,
  `media_set`).
- **Multi-worker outbox.** The worker is designed for a single
  process. A second instance pointed at the same DB is expected to
  race safely against itself (the state-machine UPDATEs are all
  fenced — see §7), but Plan E1 does not ship atomic claim
  semantics or a leader-election primitive. Thumb-style
  `UPDATE … RETURNING` claim can be bolted on later if it ever
  matters.
- **Broker-CLI shell-out.** Vision §6.3 sketches an
  `exec.BrokerRegistrar` that shells out to a configurable broker
  CLI. Plan E1 ships `NoopBroker` as the prod default and
  `brokertest.Fake` for tests; the shell-out implementation is
  deferred. The interface is designed so adding it later is a
  one-file change.

## 2. Prior art already in the tree

The `scopes` and `scope_media` tables plus owner-consistency
triggers landed in Plan A's initial migration
(`000001_initial_schema.up.sql:112-213`). Plan E1 reuses them
wholesale. The specific shape that matters:

- `scopes.broker_status` CHECK enum:
  `pending|active|failed|revoking|revoked_remote`.
- `scopes.broker_registered_at`, `broker_granted_at`,
  `broker_revoked_at`, `broker_last_error`, `broker_attempts`.
- `scopes.target_type` CHECK enum: `album_live|media_set`, plus
  the paired constraint that `target_album_id` is NOT NULL iff
  `target_type='album_live'`.
- `FOREIGN KEY (owner_hub, owner_user_id) REFERENCES owners(hub,
  user_id)`.
- Owner-consistency triggers on `scope_media` (insert + update)
  and on `scopes.target_album_id` (insert + update). These
  trigger-level guards are last-line defence, the same way the
  album triggers are in Plan D: `ShareService` is the primary
  guard; the trigger exists so a repo bug cannot silently create
  a cross-owner scope.
- Partial indexes: `scopes_grantee_idx` (for grantee-side
  lookups — dormant until E2), `scopes_owner_idx` (for the
  owner-facing list), `scopes_broker_pending_idx`
  (`WHERE broker_status IN ('pending', 'revoking')` — the
  worker's poll source).

The `internal/share/` and `internal/broker/` directories exist but
are empty; Plan E1 fills them.

One migration is added in E1:
`000003_scopes_backoff.{up,down}.sql`. See §5 for the exact DDL.

## 3. Architecture and package layout

New files, mirroring the repo→service→transport layering established
by Plans B–D and the worker pattern established by Plan C:

```
internal/broker/
├── broker.go            — BrokerClient interface, package errors
├── noop.go              — NoopBroker: prod default, always-success no-op
└── brokertest/
    └── fake.go          — scripted fake for tests (sync map of
                           per-UUID behaviours; no filesystem, no goroutines)

internal/share/
├── share.go             — Scope, ScopeFilter, ScopeState constants,
                           package sentinels
└── repo.go              — *share.Repo: Insert, GetByUUID, ListByOwner,
                           ListReady, state-transition UPDATEs,
                           PurgeTerminalForAlbum, helper for album-delete guards

internal/service/share_service.go
                         — *ShareService: Create, Revoke, Retry, Get, List

internal/shareworker/
├── worker.go            — tick-driven Worker struct
└── backoff.go           — pure Backoff function + constants

internal/httpapi/shares.go
                         — huma-registered routes under /api/v1/shares

internal/cli/shares.go
                         — cobra subcommands (create / list / show /
                           revoke / retry)

internal/db/migrations/
├── 000003_scopes_backoff.up.sql    — one ALTER TABLE, one CREATE INDEX
└── 000003_scopes_backoff.down.sql  — reverse (DROP INDEX, ALTER TABLE DROP COLUMN)
```

Layering identical to albums:

- `share.Repo` is DB-only. No auth, no identity plumbing. Takes
  `scopeUUID`, `Principal`-as-opaque-tuple, or filter structs;
  returns rows. Ownership is the service's job.
- `service.ShareService` is the auth boundary. Every exported
  method takes `caller owners.Principal` and either scopes
  queries to that principal or returns `errs.ErrNotFound` for
  cross-owner access. **Both the HTTP handlers and the CLI go
  through `ShareService`** — CLI typos or deliberate tampering
  must never let stub user A mutate user B's scopes, same as
  albums.
- `broker.BrokerClient` is an interface the service and worker
  depend on. The prod wiring is `NoopBroker`; tests inject
  `brokertest.Fake`. No implementation shells out to anything in
  E1.
- `shareworker.Worker` is a tick-driven background goroutine
  launched by `fotobank server`. It holds a `*share.Repo` and a
  `broker.BrokerClient` — **not** a `ShareService`. The worker
  moves rows through their state machine on behalf of the owner
  who already passed the service-layer auth check at mint time,
  so it does not need ownership re-verification.

`ShareService` takes `*share.Repo`, `*album.Repo`, `*media.Repo`
(the latter two for pre-flight ownership checks on `Create` — an
`album_live` scope requires a `media.Repo` check only when the
caller explicitly passes a media set, which an `album_live` scope
never does; an `album_live` scope's pre-flight is
`album.Repo.GetByID`). `shareworker.Worker` takes only
`*share.Repo` and `broker.BrokerClient`.

## 4. Domain types

```go
// internal/share/share.go

type Scope struct {
    UUID          string
    Owner         owners.Principal
    Grantee       owners.Principal
    TargetType    TargetType
    TargetAlbumID *string        // non-nil iff TargetType == TargetAlbumLive
    AllowDownload bool
    Label         string         // may be ""
    CreatedAt     time.Time
    ExpiresAt     *time.Time     // nil = indefinite

    RevokedAt *time.Time         // nil = live; set on Revoke, never cleared

    BrokerStatus        BrokerStatus
    BrokerRegisteredAt  *time.Time
    BrokerGrantedAt     *time.Time
    BrokerRevokedAt     *time.Time
    BrokerLastError     string   // "" when clean
    BrokerAttempts      int
    BrokerNextAttemptAt *time.Time // nil = eligible immediately
}

// ScopeDetail is a Scope plus its frozen media-set membership when
// TargetType == TargetMediaSet. Empty for album_live.
type ScopeDetail struct {
    Scope
    MediaIDs []string
}

type TargetType string

const (
    TargetAlbumLive TargetType = "album_live"
    TargetMediaSet  TargetType = "media_set"
)

type BrokerStatus string

const (
    StatusPending       BrokerStatus = "pending"
    StatusActive        BrokerStatus = "active"
    StatusFailed        BrokerStatus = "failed"
    StatusRevoking      BrokerStatus = "revoking"
    StatusRevokedRemote BrokerStatus = "revoked_remote"
)

// ScopeFilter narrows List.
type ScopeFilter struct {
    // AlbumID, when set, restricts to album_live scopes pointing at
    // this album. Empty = no filter.
    AlbumID string
    // Grantee, when non-zero, restricts to scopes granted to exactly
    // this principal. Zero-valued principal = no filter.
    Grantee owners.Principal
    // Status filters by broker_status. An empty slice selects the
    // default "owner-actionable" view (see §9.4); callers pass an
    // explicit slice to see settled rows.
    Status []BrokerStatus
    // IncludeSettled overrides the default actionable filter. When
    // true and Status is empty, all statuses are included. When
    // Status is non-empty this field is ignored.
    IncludeSettled bool
    Limit          int
    Offset         int
}

const (
    // LabelMaxLen caps scope labels. Service validation enforces this.
    LabelMaxLen = 200
    // MediaSetMaxLen caps media_set membership at mint. Service
    // validation enforces this.
    MediaSetMaxLen = 1000
    // MaxBrokerAttempts is the retry ceiling. After this many
    // attempts against the broker, a row flips to StatusFailed and
    // sits there until the owner retries or revokes.
    MaxBrokerAttempts = 10
)

var (
    ErrInvalidGrantee      = errors.New("share: grantee principal is empty, oversized, or equal to caller")
    ErrInvalidLabel        = errors.New("share: label exceeds 200 chars")
    ErrInvalidMediaSet     = errors.New("share: media_set must be 1..1000 unique media ids")
    ErrInvalidTargetCombo  = errors.New("share: target_type does not match payload")
    ErrAlbumEmpty          = errors.New("share: cannot share an empty album_live")
    ErrScopeImmutable      = errors.New("share: scope binding is immutable; revoke and recreate")
    ErrScopeAlreadyRevoked = errors.New("share: scope is already revoked")
    ErrRetryNotApplicable  = errors.New("share: retry only applies to failed scopes")
    ErrAlbumHasLiveScopes  = errors.New("share: album has outstanding broker grants; revoke them first")
)
```

### 4.1 State machine

The vision doc's enum was underspecified for retry semantics. Plan
E1 pins the transitions precisely. `(broker_status, revoked_at)` is
treated as the state:

```
  (NULL states indicated as ∅ for revoked_at; non-NULL as R)

        +---------+   owner Revoke (row had pending/active/failed∅)
        | pending |  --------------------------------------------+
        +---------+                                               v
             | worker publish ok                            +----------+
             v                                              | revoking |
        +---------+   owner Revoke                          +----------+
        | active  | -------------------------------------->      |
        +---------+                                               | worker revoke ok
             |                                                    v
             | publish fail (attempt >= max)            +------------------+
             v                                          | revoked_remote   |
        +-----------+   owner Retry (revoked_at = ∅)    +------------------+
        | failed ∅  | ---> pending                             (terminal)
        +-----------+
             ^                                          +------------------+
             | publish fail                             |   failed R       |
             +                                          +------------------+
                                                              |  ^
           revoke fail (attempt >= max)                       |  | owner Retry
           from revoking ----------------------------> failed R   (revoked_at = R)
                                                              |
                                                              v
                                                         revoking
```

Key refinements from the vision sketch:

- **`failed` is phase-aware.** Whether a `failed` row is "a stuck
  publish" or "a stuck revoke" is encoded by `revoked_at`:
  `failed AND revoked_at IS NULL` = publish-side stall;
  `failed AND revoked_at IS NOT NULL` = revoke-side stall. Retry
  routes to the right next state.
- **Revocation is always asynchronous.** `ShareService.Revoke`
  sets `revoked_at = now()` and `broker_status = 'revoking'`
  regardless of the starting `broker_status` (even from `pending`
  or `failed`). The worker is always responsible for calling the
  broker; the service never calls the broker inline. This
  preserves outbox semantics uniformly. If the starting state was
  `pending` (registration never happened) the worker still issues
  `RevokeScope`; `NoopBroker` and `brokertest.Fake` treat it as a
  no-op and short-circuit to `revoked_remote`. For a real broker,
  `RevokeScope` is required to be idempotent on "never heard of
  this UUID."
- **`broker_attempts` resets on retry.** Both retry paths zero
  `broker_attempts` and clear `broker_next_attempt_at` when they
  move a `failed` row back into `pending` or `revoking`.
- **`broker_revoked_at` marks the `revoked_remote` transition,
  not the local revoke.** `revoked_at` is the *local* decision
  timestamp (set by `ShareService.Revoke`). `broker_revoked_at`
  is the *remote* confirmation timestamp (set by the worker when
  `RevokeScope` succeeds). They are intentionally distinct.

## 5. Database changes

One new migration pair. No table or trigger changes beyond adding a
column and a supporting partial index.

```sql
-- 000003_scopes_backoff.up.sql
ALTER TABLE scopes ADD COLUMN broker_next_attempt_at TIMESTAMP;

-- Worker poll index. Same partial predicate as
-- scopes_broker_pending_idx so the planner can pick whichever covers
-- the current query shape; ordering by broker_next_attempt_at lets
-- the worker SELECT ... LIMIT N in due-order.
CREATE INDEX scopes_broker_ready_idx
    ON scopes(broker_next_attempt_at)
    WHERE broker_status IN ('pending', 'revoking');
```

```sql
-- 000003_scopes_backoff.down.sql
DROP INDEX IF EXISTS scopes_broker_ready_idx;
ALTER TABLE scopes DROP COLUMN broker_next_attempt_at;
```

A `NULL` `broker_next_attempt_at` means "eligible immediately" —
this is the state for a freshly-inserted `pending` row or a freshly
`revoking` row. `SELECT` predicates treat `NULL` as due; see §7.

No trigger changes. No `scope_media`-level changes. The existing
`scopes_broker_pending_idx` is kept — it stays useful for
point-lookups by status (`ListByOwner` with a status filter) that
don't care about `broker_next_attempt_at`.

## 6. BrokerClient interface

```go
// internal/broker/broker.go
package broker

type BrokerClient interface {
    // PublishScope registers the scope with the external broker and
    // creates the grantee-side grant. Implementations MUST be
    // idempotent on scope.UUID (and for the grant, on
    // (scope.UUID, scope.Grantee)). The outbox worker may retry.
    //
    // Return ErrBrokerTransient for retryable errors (network,
    // 5xx), ErrBrokerPermanent for non-retryable errors (broker
    // refused, 4xx), or a context error on cancellation. Any other
    // error is treated as transient to err on the side of retry.
    //
    // The worker passes the *local* share.Scope struct. Implementations
    // must not inspect scope_media membership; the broker is
    // deliberately ignorant of fotobank's media set — for album_live,
    // membership is evaluated live on the fotobank side at grantee
    // read time, and for media_set, the broker only needs the scope
    // UUID and the (grantee, allow_download, expires_at, label)
    // envelope.
    PublishScope(ctx context.Context, s share.Scope) error

    // RevokeScope revokes the grant at the broker. Idempotent on
    // scope UUID including the case "never heard of this UUID"
    // (which must succeed). Same error contract as PublishScope.
    RevokeScope(ctx context.Context, uuid string) error
}
```

**Register vs. grant columns.** Vision §6.3 sketches a two-step
broker protocol (`RegisterScope` then `CreateGrant`) tracked by two
columns (`broker_registered_at`, `broker_granted_at`). E1 fuses
both into a single `PublishScope` call for simplicity, but keeps
both columns and sets them together on success. The column
distinction is load-bearing for the album-delete purge predicate
(§8.3): `broker_granted_at IS NULL` means "publish never succeeded,"
which is the only `failed AND revoked_at IS NOT NULL` row that can
be safely purged without orphaning a remote grant. Keeping both
columns also preserves the upgrade path to a native two-step broker
client later.

```go
var (
    ErrBrokerTransient = errors.New("broker: transient error")
    ErrBrokerPermanent = errors.New("broker: permanent error")
)
```

Two implementations ship:

- `broker.NoopBroker` — both methods return `nil` immediately. This
  is the production default for single-owner stub deployments,
  mirroring `stub.BrokerRegistrar` in the vision doc. The worker
  treats "publish ok" as `active` and "revoke ok" as
  `revoked_remote`; a NoopBroker thus makes scopes appear to
  round-trip cleanly even though no remote system exists. This is
  desirable: it keeps the owner-facing state machine realistic
  even when a broker is not wired up yet.
- `broker.brokertest.Fake` — a scripted test double.
  `Fake` exposes methods that tests use to queue per-UUID behaviour
  (`QueuePublishError(uuid string, err error)`,
  `QueueRevokeError(uuid string, err error)`,
  `ObservedPublishes() []string`, `ObservedRevokes() []string`).
  A zero-valued `Fake` behaves like `NoopBroker`.

**Package placement.** `brokertest` is a subpackage of `internal/broker`
rather than sitting in `internal/testutil`, so production code under
`internal/` cannot accidentally import the fake. The `_test.go`
files in `internal/shareworker/` and `internal/service/` are the
only intended consumers.

## 7. Repo surface

```go
// internal/share/repo.go

type Repo struct { rw, ro *sql.DB }

// Insert writes the scopes row and (if media set) scope_media rows
// in one transaction. broker_status is always 'pending',
// broker_next_attempt_at is NULL. The owner-consistency triggers
// enforce cross-owner safety.
func (r *Repo) Insert(ctx context.Context, s Scope, mediaIDs []string) error

// GetByUUID returns the scope plus (for media_set) its frozen
// membership. Returns errs.ErrNotFound if no row.
func (r *Repo) GetByUUID(ctx context.Context, uuid string) (ScopeDetail, error)

// ListByOwner returns scopes owned by this principal, filtered and
// paginated. The SQL applies the default "owner-actionable" filter
// when filter.Status is empty and filter.IncludeSettled is false.
func (r *Repo) ListByOwner(ctx context.Context, owner owners.Principal, filter ScopeFilter) ([]Scope, error)

// ListReady returns up to `limit` scopes that the worker should
// attempt right now. Selection:
//   broker_status IN ('pending', 'revoking')
//   AND (broker_next_attempt_at IS NULL OR broker_next_attempt_at <= now)
//   AND broker_attempts < MaxBrokerAttempts
// Order by broker_next_attempt_at ASC NULLS FIRST, then created_at ASC.
// This is a read; there is no atomic claim — see §10.
func (r *Repo) ListReady(ctx context.Context, now time.Time, limit int) ([]Scope, error)

// MarkRegistered transitions a pending scope toward active on a
// successful PublishScope. Fenced so a racing revoke cannot be
// overwritten.
//
//   UPDATE scopes
//      SET broker_status='active',
//          broker_registered_at = COALESCE(broker_registered_at, ?),
//          broker_granted_at    = COALESCE(broker_granted_at, ?),
//          broker_last_error    = '',
//          broker_next_attempt_at = NULL
//    WHERE uuid = ? AND broker_status = 'pending'
//
// Returns rows-affected. 0 means some other transition beat us
// (e.g. revoke arrived); the worker re-reads and decides again.
func (r *Repo) MarkRegistered(ctx context.Context, uuid string, at time.Time) (int64, error)

// MarkRevoked transitions a revoking scope to revoked_remote on a
// successful RevokeScope.
//
//   UPDATE scopes
//      SET broker_status='revoked_remote',
//          broker_revoked_at      = COALESCE(broker_revoked_at, ?),
//          broker_last_error      = '',
//          broker_next_attempt_at = NULL
//    WHERE uuid = ? AND broker_status = 'revoking'
func (r *Repo) MarkRevoked(ctx context.Context, uuid string, at time.Time) (int64, error)

// MarkAttemptFailed records an attempt that failed and is *not*
// terminal (attempts+1 < MaxBrokerAttempts). It stays in the current
// broker_status (pending or revoking) but bumps attempts and sets
// the next-attempt-at.
//
//   UPDATE scopes
//      SET broker_attempts         = broker_attempts + 1,
//          broker_last_error       = ?,
//          broker_next_attempt_at  = ?
//    WHERE uuid = ? AND broker_status = ?   -- fenced to the phase
func (r *Repo) MarkAttemptFailed(ctx context.Context, uuid string, phase BrokerStatus, errMsg string, nextAt time.Time) (int64, error)

// MarkFailed flips a scope to 'failed' after the attempt ceiling or
// on a permanent error. Fenced to the caller-declared phase.
//
//   UPDATE scopes
//      SET broker_status          = 'failed',
//          broker_attempts        = broker_attempts + 1,
//          broker_last_error      = ?,
//          broker_next_attempt_at = NULL
//    WHERE uuid = ? AND broker_status = ?
func (r *Repo) MarkFailed(ctx context.Context, uuid string, phase BrokerStatus, errMsg string) (int64, error)

// SetRevoking transitions any of (pending, active, failed with
// revoked_at NULL) into revoking and sets revoked_at. Fenced by the
// current broker_status set so a concurrent worker transition
// cannot quietly overwrite the owner's intent.
//
//   UPDATE scopes
//      SET broker_status          = 'revoking',
//          revoked_at             = COALESCE(revoked_at, ?),
//          broker_attempts        = 0,
//          broker_next_attempt_at = NULL,
//          broker_last_error      = ''
//    WHERE uuid = ?
//      AND broker_status IN ('pending','active','failed')
//      AND revoked_at IS NULL
//
// Returns rows-affected. 0 means the row was already revoked or
// revoke-completed; ShareService converts that to
// ErrScopeAlreadyRevoked.
func (r *Repo) SetRevoking(ctx context.Context, uuid string, at time.Time) (int64, error)

// RetryPublish moves a failed-pending-phase scope back to pending.
// Fenced so it only applies to failed rows whose revoked_at is NULL.
//
//   UPDATE scopes
//      SET broker_status          = 'pending',
//          broker_attempts        = 0,
//          broker_next_attempt_at = NULL,
//          broker_last_error      = ''
//    WHERE uuid = ? AND broker_status = 'failed' AND revoked_at IS NULL
func (r *Repo) RetryPublish(ctx context.Context, uuid string) (int64, error)

// RetryRevoke moves a failed-revoke-phase scope back to revoking.
// Fenced so it only applies to failed rows whose revoked_at is NOT NULL.
//
//   UPDATE scopes
//      SET broker_status          = 'revoking',
//          broker_attempts        = 0,
//          broker_next_attempt_at = NULL,
//          broker_last_error      = ''
//    WHERE uuid = ? AND broker_status = 'failed' AND revoked_at IS NOT NULL
func (r *Repo) RetryRevoke(ctx context.Context, uuid string) (int64, error)

// HasBlockingScopesForAlbum returns true if the album has any scope
// row that must be resolved before album delete can proceed. See §8
// for the exact blocking predicate.
func (r *Repo) HasBlockingScopesForAlbum(ctx context.Context, albumID string) (bool, error)

// PurgeTerminalForAlbum deletes scopes linked to the album that are
// safe to drop on album delete: broker_status = 'revoked_remote' OR
// (broker_status = 'failed' AND revoked_at IS NOT NULL AND
//  broker_granted_at IS NULL). Returns rows-deleted.
// See §8 for the rationale of the purge predicate.
func (r *Repo) PurgeTerminalForAlbum(ctx context.Context, albumID string) (int64, error)
```

**Split pools.** `share.Repo` takes `rw` and `ro` like `album.Repo`;
writes go through `rw`, reads through `ro`. `ListReady` reads through
`ro` — it's a poll, inexact reads are fine.

**"Phase" parameter vs. enum.** `MarkAttemptFailed` and `MarkFailed`
take a `phase BrokerStatus` argument that must be one of `pending`
or `revoking`; any other value is a caller bug. The service never
calls these directly — only the worker does, and the worker knows
which phase it just attempted. The fenced WHERE is the guard
against the scope having moved between the ListReady that read it
and the UPDATE that records the attempt.

## 8. ShareService

```go
// internal/service/share_service.go

type ShareService struct {
    shares *share.Repo
    albums *album.Repo
    media  *media.Repo
    uuid   func() string        // overridable for tests
    now    func() time.Time     // overridable for tests
}

type CreateShareRequest struct {
    Label         string
    Grantee       owners.Principal
    AllowDownload bool
    ExpiresAt     *time.Time
    TargetType    share.TargetType   // "album_live" | "media_set"
    AlbumID       string             // set iff TargetType == TargetAlbumLive
    MediaIDs      []string           // set iff TargetType == TargetMediaSet
}

// Create mints a new scope owned by `caller`. Validation + pre-flight
// ownership checks happen before the DB write. Returns the scope in
// its freshly-inserted StatusPending state; the worker will move it
// to StatusActive asynchronously.
func (s *ShareService) Create(ctx context.Context, req CreateShareRequest, caller owners.Principal) (share.Scope, error)

// Revoke marks the scope as revoked locally and schedules broker
// revocation. Returns the updated scope. Idempotent on "already
// revoked" (returns ErrScopeAlreadyRevoked, which HTTP maps to 409
// or 204 — see §9.5).
func (s *ShareService) Revoke(ctx context.Context, uuid string, caller owners.Principal) (share.Scope, error)

// Retry re-enables a StatusFailed scope. Chooses pending or
// revoking based on whether revoked_at is NULL. Any other status
// returns ErrRetryNotApplicable.
func (s *ShareService) Retry(ctx context.Context, uuid string, caller owners.Principal) (share.Scope, error)

// Get returns the scope + (for media_set) its membership. Caller
// must be the scope owner; cross-owner returns errs.ErrNotFound.
func (s *ShareService) Get(ctx context.Context, uuid string, caller owners.Principal) (share.ScopeDetail, error)

// List returns scopes owned by the caller. Filter.Status empty +
// IncludeSettled false returns the default "owner-actionable" view:
// pending, active, failed with revoked_at IS NULL, revoking.
// Settled rows (revoked_remote and failed with revoked_at IS NOT NULL)
// are hidden by default. filter.Status != nil bypasses the filter.
func (s *ShareService) List(ctx context.Context, filter share.ScopeFilter, caller owners.Principal) ([]share.Scope, error)
```

### 8.1 Validation order for Create

In order, short-circuiting on the first failure:

1. **Label length.** `len(req.Label) <= LabelMaxLen`; else
   `ErrInvalidLabel`.
2. **Grantee syntactic validity.** Non-empty `Hub` and `UserID`;
   each field `<= 255` runes; the principal is not equal to the
   caller (self-share is rejected by default in E1 — see §8.2);
   else `ErrInvalidGrantee`.
3. **Target type / payload consistency.** If
   `TargetType == TargetAlbumLive`, `AlbumID` required and
   `MediaIDs` empty; if `TargetType == TargetMediaSet`,
   `MediaIDs` required and `AlbumID` empty; else
   `ErrInvalidTargetCombo`.
4. **Media set bounds.** For `TargetMediaSet`:
   `1 <= len(deduped MediaIDs) <= MediaSetMaxLen`; else
   `ErrInvalidMediaSet`. Deduplication is on the service side
   (map[string]struct{} in caller-provided order preserved).
5. **Ownership pre-flight.**
   - For `TargetAlbumLive`: `album.Repo.GetDetailByID(albumID)`;
     if the result is missing or belongs to a different owner,
     `errs.ErrNotFound`. If `ItemCount == 0` at mint time, return
     `ErrAlbumEmpty`. Empty `album_live` scopes would register
     with the broker as "a grant that resolves to nothing"; this
     is confusing and cheap to reject at mint. Using
     `GetDetailByID` (rather than `GetByID`) piggybacks on the
     count that Plan D already derives in SQL.
   - For `TargetMediaSet`: one `media.Repo.GetByID` per media ID
     (N calls, N ≤ 1000). Any missing row or cross-owner row →
     `errs.ErrNotFound`. The pre-flight happens before the INSERT
     because the owner-consistency trigger only fires on
     `scope_media` rows; failing fast gives a clean 404 instead
     of a trigger-raised 500.
6. **Insert.** Generate UUID, write `scopes` + `scope_media`,
   return the row.

Errors that escape pre-flight but surface from the trigger (e.g. a
race where the media was deleted between pre-flight and insert) are
wrapped as `errs.ErrOwnerMismatch`, logged, and returned as 500 by
HTTP (see §9.5). Same policy as albums.

### 8.2 Grantee validation rationale

Grantees are opaque `(hub, user_id)` principals. Plan E1 does not
call out to the broker to validate them, and it does not require an
`owners` row for the grantee (grantees are not owners; they are
principals on some other fotobank). That leaves three things the
service can still check cheaply:

- **Non-empty and bounded.** Prevents pathological rows
  (e.g. empty string principals, megabyte-sized hub strings) and
  keeps the broker from receiving garbage.
- **Not the zero principal.** `owners.Principal{}` is used as a
  sentinel for "unset" throughout the codebase; accepting it here
  would silently mint scopes granted to nobody.
- **Not the caller.** Self-share is a confused operation —
  a scope granted to the owner offers no capability the owner
  doesn't already have, and it clutters the owner's share list.
  A flag to allow it (for test fixtures) is a YAGNI candidate and
  not in E1.

Validation that the grantee exists on a real broker, has a display
handle, or is reachable, is the broker's job; fotobank trusts the
broker implementation.

### 8.3 Album-delete coupling

Plan D's `AlbumService.Delete` relies on `album_media`'s
`ON DELETE CASCADE` to clean up membership. Scopes complicate this
because some of them represent outstanding broker grants. E1 adds a
pre-check before the existing album delete and an auto-purge for
safely-resolvable scopes.

```
AlbumService.Delete(albumID, caller):
    ... existing owner check ...

    tx := begin
        purged, err := share.Repo.PurgeTerminalForAlbum(tx, albumID)
        if err { rollback; return err }

        blocking, err := share.Repo.HasBlockingScopesForAlbum(tx, albumID)
        if err         { rollback; return err }
        if blocking    { rollback; return ErrAlbumHasLiveScopes }

        delete from albums where id = ?                 // cascades album_media
    tx.commit
```

The **auto-purge predicate** for `PurgeTerminalForAlbum`:

```sql
DELETE FROM scopes
 WHERE target_album_id = ?
   AND (
         broker_status = 'revoked_remote'
      OR (broker_status = 'failed'
          AND revoked_at IS NOT NULL
          AND broker_granted_at IS NULL)
       )
```

The **blocking predicate** for `HasBlockingScopesForAlbum` is "any
row pointing at this album that is NOT in the purge set." In SQL:

```sql
SELECT 1 FROM scopes
 WHERE target_album_id = ?
   AND NOT (
         broker_status = 'revoked_remote'
      OR (broker_status = 'failed'
          AND revoked_at IS NOT NULL
          AND broker_granted_at IS NULL)
       )
 LIMIT 1
```

Spelled out: what's safe to purge and what blocks:

| `broker_status`     | `revoked_at` | `broker_granted_at` | Action         | Reason |
|---------------------|--------------|---------------------|----------------|--------|
| `pending`           | NULL         | any                 | **BLOCK**      | Publish not yet attempted or retrying; the grant may still land and owner must explicitly revoke. |
| `active`            | NULL         | set                 | **BLOCK**      | Live grant outstanding; even if expired, the broker still thinks it's granted until revoked. |
| `active`            | NULL, expired in local time | set    | **BLOCK**      | E1 does not enforce expiry; the remote grant may still be live. |
| `failed`            | NULL         | any                 | **BLOCK**      | Publish failed but could be retried; safer to force explicit revoke. |
| `failed`            | NOT NULL     | NULL                | PURGE          | Publish never succeeded, owner has already chosen to revoke, and the revoke cannot be delivered. Broker has no grant record. Dropping the local row is safe. |
| `failed`            | NOT NULL     | set                 | **BLOCK**      | Publish succeeded once (grant exists on broker), owner asked to revoke, revoke stalled. Purging the local row would orphan the remote grant. Owner must retry the revoke or manually clean up. |
| `revoking`          | NOT NULL     | any                 | **BLOCK**      | Revocation in flight; wait for `revoked_remote`. |
| `revoked_remote`    | NOT NULL     | any                 | PURGE          | Fully settled; broker confirmed revoke. |

The "block" path returns `ErrAlbumHasLiveScopes`. The HTTP layer
maps this to 409; the owner-facing message is *"this album has
outstanding shares; revoke them from the shares UI first."* A
future UI flag `?force=true` could mass-revoke and wait, but E1
does not ship that.

### 8.4 Retry dispatch

```
ShareService.Retry(uuid, caller):
    detail := shares.GetByUUID(uuid)
    if detail.Owner != caller: return errs.ErrNotFound
    if detail.BrokerStatus != StatusFailed: return ErrRetryNotApplicable
    if detail.RevokedAt == nil:
        rows := shares.RetryPublish(uuid)
    else:
        rows := shares.RetryRevoke(uuid)
    if rows == 0: return ErrRetryNotApplicable  // race: moved under us
    return shares.GetByUUID(uuid).Scope
```

### 8.5 Owner-boundary cross-owner reads

Every `ShareService` method does the same existence-oracle
protection that `AlbumService` does: a cross-owner UUID returns
`errs.ErrNotFound`, never `errs.ErrOwnerMismatch`. The HTTP layer
translates to 404. This is important even for Plan E1 (single-owner
stub) because Plan E2 will introduce the header-mode provider and
the same service will then be called with multiple principals.

## 9. HTTP surface

Routes live under `/api/v1/shares` and are huma-registered on the
same mux as `/api/v1/media`, `/api/v1/albums`. Error translation
uses `httpapi.Translate` with a local override that maps
`share.ErrAlbumHasLiveScopes → 409` and
`share.ErrScopeAlreadyRevoked → 409` and
`share.ErrRetryNotApplicable → 409` (the Translate default covers
the `ErrInvalid*` sentinels as 400, `ErrNotFound` as 404,
`ErrAlreadyExists` as 409, `ErrOwnerMismatch` as 500 via the local
override — see §9.6).

### 9.1 DTOs

```go
type ScopeDTO struct {
    UUID                string    `json:"uuid"`
    Owner               PrincipalDTO `json:"owner"`
    Grantee             PrincipalDTO `json:"grantee"`
    TargetType          string    `json:"target_type"`
    TargetAlbumID       *string   `json:"target_album_id,omitempty"`
    AllowDownload       bool      `json:"allow_download"`
    Label               string    `json:"label"`
    CreatedAt           time.Time `json:"created_at"`
    ExpiresAt           *time.Time `json:"expires_at,omitempty"`
    Expired             bool      `json:"expired"`  // derived: now >= expires_at
    RevokedAt           *time.Time `json:"revoked_at,omitempty"`
    BrokerStatus        string    `json:"broker_status"`
    BrokerRegisteredAt  *time.Time `json:"broker_registered_at,omitempty"`
    BrokerGrantedAt     *time.Time `json:"broker_granted_at,omitempty"`
    BrokerRevokedAt     *time.Time `json:"broker_revoked_at,omitempty"`
    BrokerLastError     string    `json:"broker_last_error,omitempty"`
    BrokerAttempts      int       `json:"broker_attempts"`
    BrokerNextAttemptAt *time.Time `json:"broker_next_attempt_at,omitempty"`
}

type ScopeDetailDTO struct {
    ScopeDTO
    MediaIDs []string `json:"media_ids,omitempty"`
}

type PrincipalDTO struct {
    Hub    string `json:"hub"`
    UserID string `json:"user_id"`
}
```

`Expired` is a convenience derived from `now() >= expires_at`
for owner-UI rendering; fotobank itself does not change behaviour
based on it in E1.

### 9.2 Endpoints

| Verb | Path | Body | Response | Notes |
|------|------|------|----------|-------|
| POST | `/api/v1/shares` | `CreateShareRequestDTO` | `ScopeDTO` (201) | `target_type` must be `album_live` or `media_set` with paired payload. |
| GET  | `/api/v1/shares` | — | `{"items":[ScopeDTO]}` | Query params: `album_id`, `grantee_hub`, `grantee_user_id`, `status` (comma-sep), `include_settled`, `limit`, `offset`. Default = owner-actionable view. |
| GET  | `/api/v1/shares/{uuid}` | — | `ScopeDetailDTO` | 404 if not found OR cross-owner. |
| POST | `/api/v1/shares/{uuid}/revoke` | — | `ScopeDTO` | 404 if missing/cross-owner; 409 if already revoked. |
| POST | `/api/v1/shares/{uuid}/retry`  | — | `ScopeDTO` | 404 if missing/cross-owner; 409 if not `failed`. |

Request validation (beyond service-level): `limit` default 100, max
500; `status` values are rejected at the HTTP layer if they are not
one of the enum values. Unknown statuses return 400, not an empty
list, to make client bugs loud.

### 9.3 Why POST for revoke/retry?

DELETE on the scope is tempting but wrong: revocation is a state
transition, not a deletion. The row lives on so the worker can
propagate to the broker and the owner can see the audit trail.
POST on a sub-path mirrors the thumbnails retry route style
(`/thumbs/{id}/retry`).

### 9.4 List semantics

Default (`status` empty AND `include_settled=false`): returns
`broker_status IN ('pending','active','revoking') OR (broker_status = 'failed' AND revoked_at IS NULL)`.
This is "rows that still need the owner's attention." Explicit
`status=revoked_remote,failed` or `include_settled=true` shows the
full history.

Sort order: `created_at DESC`. There is no configurable sort in E1.

### 9.5 Error mapping

`httpapi/shares.go` defines a local translator following the same
pattern Plan D introduced for albums:

```go
func translateShareError(err error) huma.StatusError {
    switch {
    case errors.Is(err, errs.ErrOwnerMismatch):
        return huma.Error500InternalServerError(http.StatusText(http.StatusInternalServerError))
    case errors.Is(err, share.ErrAlbumHasLiveScopes):
        return huma.Error409Conflict("album has outstanding shares; revoke them first")
    case errors.Is(err, share.ErrScopeAlreadyRevoked):
        return huma.Error409Conflict("scope is already revoked")
    case errors.Is(err, share.ErrRetryNotApplicable):
        return huma.Error409Conflict("retry only applies to failed scopes")
    case errors.Is(err, share.ErrInvalidGrantee):
        return huma.Error400BadRequest("grantee hub/user_id must be non-empty, bounded, and not equal to caller")
    case errors.Is(err, share.ErrInvalidLabel):
        return huma.Error400BadRequest("label exceeds 200 chars")
    case errors.Is(err, share.ErrInvalidMediaSet):
        return huma.Error400BadRequest("media_ids must be 1..1000 unique ids")
    case errors.Is(err, share.ErrInvalidTargetCombo):
        return huma.Error400BadRequest("target_type does not match payload")
    case errors.Is(err, share.ErrAlbumEmpty):
        return huma.Error400BadRequest("cannot share an empty album")
    default:
        return Translate(err)   // shared cross-cutting mapping
    }
}
```

The global `Translate` maps `errs.ErrOwnerMismatch → 403`, which
would leak the existence of other-owner scopes on the share
surface. The local override returns 500 (same as albums).
Cross-owner lookups never reach the override because the service
returns `errs.ErrNotFound` first; `ErrOwnerMismatch` can only
surface here if a repo pre-flight missed a row and the DB trigger
fired.

### 9.6 OpenAPI

After the routes land, `make api-generate` regenerates
`openapi.json`. The prek hook runs this automatically on commit.

## 10. The outbox worker

```go
// internal/shareworker/worker.go

type Worker struct {
    repo       *share.Repo
    client     broker.BrokerClient
    tick       time.Duration
    batch      int
    backoffFn  func(attempt int) time.Duration
    now        func() time.Time
    logger     *slog.Logger
}

func (w *Worker) Run(ctx context.Context) error
func (w *Worker) RunOnce(ctx context.Context) (processed int, err error)
```

`Run` loops: on each tick, `RunOnce` pulls a batch of ready rows and
processes them serially. `RunOnce` is factored out so tests can
drive the worker deterministically without goroutines.

### 10.1 Per-row decision

```
for each scope in ListReady(limit=batch):
    switch scope.BrokerStatus:
    case StatusPending:
        err := client.PublishScope(ctx, scope)
        if err == nil:
            repo.MarkRegistered(scope.UUID, now())
            continue
        if isPermanent(err) || scope.BrokerAttempts + 1 >= MaxBrokerAttempts:
            repo.MarkFailed(scope.UUID, StatusPending, err.Error())
        else:
            repo.MarkAttemptFailed(scope.UUID, StatusPending,
                err.Error(), now().Add(backoffFn(scope.BrokerAttempts + 1)))

    case StatusRevoking:
        err := client.RevokeScope(ctx, scope.UUID)
        if err == nil:
            repo.MarkRevoked(scope.UUID, now())
            continue
        if isPermanent(err) || scope.BrokerAttempts + 1 >= MaxBrokerAttempts:
            repo.MarkFailed(scope.UUID, StatusRevoking, err.Error())
        else:
            repo.MarkAttemptFailed(scope.UUID, StatusRevoking,
                err.Error(), now().Add(backoffFn(scope.BrokerAttempts + 1)))
```

`isPermanent` returns `errors.Is(err, broker.ErrBrokerPermanent)`.
Other non-context errors are transient.

**Context cancellation.** Context errors propagate unchanged; no
state update happens. The row stays on whatever it was (still
eligible; `broker_next_attempt_at` unchanged), and the next tick
retries. This means a killed worker re-processes at most one row
per in-flight broker call.

**Fenced updates.** Every state-mutating call uses the fenced
`WHERE broker_status = ?` predicate from §7. A rows-affected-zero
return means a concurrent `Revoke` or another worker has already
moved the row; the worker logs it and moves on. The next
`ListReady` sees the current state.

### 10.2 Backoff

```go
// internal/shareworker/backoff.go

const (
    baseDelay = 30 * time.Second
    maxDelay  = 1 * time.Hour
    jitterPct = 10  // ± 10 %
)

// Backoff returns the delay before attempt n (1-based). Attempt 1
// of a freshly-retried scope waits baseDelay (30s), attempt 10 is
// capped at maxDelay. Total wall-clock budget across 10 attempts is
// ~2h15m without jitter.
func Backoff(attempt int, rng *rand.Rand) time.Duration {
    if attempt < 1 { attempt = 1 }
    raw := float64(baseDelay) * math.Pow(2, float64(attempt-1))
    if raw > float64(maxDelay) { raw = float64(maxDelay) }
    // jitter: ± jitterPct %
    jitter := raw * (float64(jitterPct) / 100) * (2*rng.Float64() - 1)
    return time.Duration(raw + jitter)
}
```

Pure function, easy to unit-test. `rng` is injected so tests can
pin the jitter to zero for deterministic delays.

### 10.3 Startup, shutdown, ticks

`fotobank server` wires the worker in the same place as the
thumbnail worker. Default tick: 15 seconds. Default batch: 20
rows. Both are `config.yaml`-overridable (`share_worker.tick`,
`share_worker.batch`).

Worker shutdown is cooperative: `ctx.Done()` breaks the outer loop
at the next iteration boundary; an in-flight broker call races
against the context and returns `ctx.Err()`, which the worker
propagates to skip the state update for that row.

### 10.4 Single-worker assumption

The worker has no atomic claim. If two processes point at the same
DB both running `RunOnce`, both will `ListReady` the same rows and
both will call `PublishScope`. Because `PublishScope` and
`RevokeScope` are required idempotent, this is safe correctness-wise
— it's wasteful, not dangerous. The fenced UPDATEs ensure the
state machine can't be corrupted by the race; at worst one worker's
success update rows-affected-zeros and it logs "moved under us."

If multi-worker ever becomes relevant, the upgrade path is the same
as the thumb queue: `UPDATE … RETURNING` to atomically claim rows
with a per-claim lease. Plan E1 documents this as the intended
future path but does not ship it.

## 11. CLI

`fotobank shares` cobra subcommand tree, each subcommand resolving
`caller` from `cfg.Identity.Stub.{Hub,UserID}`:

```
fotobank shares create --album <uuid> --grantee hub:user [--label "…"] [--allow-download] [--expires RFC3339]
fotobank shares create --media <uuid[,uuid…]> --grantee hub:user [--label "…"] [--allow-download] [--expires RFC3339]
fotobank shares list   [--album <uuid>] [--grantee hub:user] [--status pending,active,…] [--include-settled] [--limit N] [--offset N]
fotobank shares show   <scope-uuid>
fotobank shares revoke <scope-uuid>
fotobank shares retry  <scope-uuid>
```

`--grantee` parses `hub:user` into `owners.Principal{Hub, UserID}`.
A `--grantee-handle` convenience flag is out of scope for E1; display
handles are a grantee-side concern.

Table output for `list`:

```
UUID                                  STATUS    TARGET                             GRANTEE           CREATED              LAST ERROR
7e5a…  active     album   Summer 2024 (…6b8f)   hub:alice         2026-04-23T14:00:00  -
9fd1…  pending    media   (7 items)             hub:bob           2026-04-23T14:02:00  -
03bc…  failed     album   Trip (…aa12)          hub:carol         2026-04-23T14:05:00  broker timeout
```

`show` prints the scope plus (for `media_set`) the frozen media ID
list. `--json` flag flips to raw JSON (same DTO the HTTP layer
returns) on all three read commands.

## 12. Testing

Integration tests use `testutil.OpenTestDB(t)` (fresh migrated
SQLite per test) plus a seeded owner and, when needed, a seeded
album + media. `brokertest.Fake` is injected anywhere a test needs
to observe or influence broker calls. Unit tests for
`shareworker.Backoff` are pure-function.

Test plan highlights:

- `share.Repo` tests:
  - Insert album_live / media_set happy paths.
  - Owner-consistency triggers fire when a cross-owner media ID is
    fed in directly (bypassing the service pre-flight).
  - Every fenced UPDATE: call with the wrong predicate →
    rows-affected = 0, no state change.
  - `PurgeTerminalForAlbum` / `HasBlockingScopesForAlbum` across
    all 8 rows of the §8.3 table.
- `ShareService` tests:
  - Cross-owner `Get`/`Revoke`/`Retry` → `errs.ErrNotFound`.
  - Grantee validation: empty fields, caller==grantee, oversized
    hub/user_id all return `ErrInvalidGrantee`.
  - Empty album_live → `ErrAlbumEmpty`.
  - Media set with a cross-owner ID → `errs.ErrNotFound`,
    no DB write.
  - Retry routes `failed∅` → `pending` and `failedR` → `revoking`.
  - Revoke on a `pending` row → `revoking` (worker will short-circuit).
- `shareworker` tests (driving `RunOnce` with `brokertest.Fake`):
  - `pending` → `active` on success.
  - `pending` → transient fail → `broker_next_attempt_at` set,
    `attempts` incremented, `broker_status` stays `pending`.
  - `pending` → transient fail × `MaxBrokerAttempts` → `failed∅`.
  - `pending` → permanent fail → `failed∅` immediately.
  - `revoking` branch mirrors above into `revoked_remote` / `failedR`.
  - `ListReady` respects `broker_next_attempt_at > now`.
  - A concurrent `SetRevoking` between `ListReady` and the worker's
    UPDATE → worker's UPDATE rows-affected = 0, no state corruption,
    next tick picks up the new `revoking` state.
- HTTP tests: one end-to-end per endpoint; error surfaces for the
  three 409 sentinels; status-filter parsing rejects unknown values.
- Album-delete integration: album with pending, active, failed∅,
  failedR (with and without `broker_granted_at`), revoking,
  `revoked_remote` scopes → verifies the block/purge table of §8.3.

Assertion style: `require` (testify), with project-wide
`r := require.New(t)` per the `testify-helper-check` lint. Sentinel
checks use `r.ErrorIs`, never `==`.

## 13. Open questions

None that block implementation. Explicitly deferred:

- **Expiry enforcement.** Out of E1; Plan E2 enforces on read.
- **Broker shell-out implementation.** Deferred; `NoopBroker` is
  enough to validate the outbox flow.
- **Multi-worker atomic claim.** Single-worker is the assumption;
  upgrade path documented in §10.4.
- **Self-share.** Rejected by default; a future `--allow-self`
  flag can be added if a real use case appears.
- **Album-snapshot target_type.** Listed as "future variant" in
  the vision doc; remains deferred.

---

Ready for review. Next: writing-plans skill to produce the
implementation plan.
