# Plan E1: Sharing — Owner-Side Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship the owner-side half of the sharing subsystem: scopes CRUD, a broker-outbox worker, an `/api/v1/shares` HTTP surface, a `fotobank shares …` CLI, and the album-delete interlock that prevents orphaning remote grants.

**Architecture:** Follow the repo→service→transport layering used by Plan D. `share.Repo` is DB-only; `ShareService` is the auth boundary; HTTP + CLI both go through the service. A `broker.BrokerClient` interface decouples the service/worker from the (currently non-existent) broker — prod wires `NoopBroker`, tests wire `brokertest.Fake`. The worker is tick-driven, uses fenced SQL updates for state transitions, and records attempts via a new `broker_next_attempt_at` column with exponential backoff. Album delete runs share purge + block-check + album row delete atomically in one `db.Tx`.

**Tech Stack:** Go 1.26, `modernc.org/sqlite`, `database/sql`, `huma/v2`, `cobra`, `testify/require`, `github.com/google/uuid`.

**Spec:** `docs/superpowers/specs/2026-04-23-fotobank-plan-e1-sharing-owner-side-design.md`

---

## File structure

```
internal/share/
├── share.go                — Scope, ScopeDetail, filter/enum/const/sentinel declarations
├── share_test.go           — trivial type/constant invariants (one file per package per convention)
├── repo.go                 — *share.Repo: CRUD + state UPDATEs + PrepareAlbumDeleteTx
├── repo_test.go            — repo unit tests
└── trigger_test.go         — owner-consistency trigger defence-in-depth test

internal/broker/
├── broker.go               — BrokerClient interface + package sentinels
├── noop.go                 — NoopBroker (prod default)
├── broker_test.go          — tiny NoopBroker sanity test
└── brokertest/
    ├── fake.go             — scripted test double
    └── fake_test.go

internal/shareworker/
├── backoff.go              — Backoff pure function + constants
├── backoff_test.go         — deterministic-jitter table tests
├── worker.go               — tick-driven Worker (Run, RunOnce)
└── worker_test.go          — worker tests (use brokertest.Fake + share.Repo)

internal/service/
├── share_service.go        — *ShareService: Create / Get / List / Revoke / Retry
├── share_service_test.go
├── album_service.go        — MODIFIED: add shares, db deps; Delete via db.Tx + PrepareAlbumDeleteTx
└── album_service_test.go   — MODIFIED: pass new deps; cover the share-blocks-delete path

internal/album/
├── repo.go                 — MODIFIED: add DeleteTx(ctx, tx, id); refactor Delete to delegate
└── repo_test.go            — MODIFIED: DeleteTx test

internal/httpapi/
├── shares.go               — /api/v1/shares routes + translateShareError + DTOs
├── shares_test.go
├── albums.go               — MODIFIED: translateAlbumError adds share.ErrAlbumHasLiveScopes → 409
├── albums_test.go          — MODIFIED: case in the translator test
├── api.go                  — MODIFIED: Deps adds ShareService; registerShares called
└── openapi.go              — regenerated

internal/cli/
├── shares.go               — cobra `fotobank shares` subcommands
├── shares_test.go
├── root.go                 — MODIFIED: root.AddCommand(newSharesCmd())
├── albums.go               — MODIFIED: loadAlbumCtx threads share.Repo + *db.DB into AlbumService
├── server.go               — MODIFIED: construct ShareService + Worker; pass Worker Run into errgroup
└── e2e_test.go             — MODIFIED: append a shares round-trip section

internal/db/migrations/
├── 000003_scopes_backoff.up.sql
└── 000003_scopes_backoff.down.sql
```

Each file has one clear responsibility. `internal/share/repo.go` is expected to grow to ~500 lines; keep state-transition UPDATEs physically adjacent so the fenced-WHERE pattern stays readable in one screen.

---

## Sentinel / type reference (used by multiple tasks)

The canonical shapes below are implemented in Task 2 (domain types) and Task 3 (broker interface). Later tasks reference them without re-declaring.

```go
// internal/share/share.go
package share

import (
    "database/sql"
    "errors"
    "time"

    "github.com/wesm/fotobank/internal/owners"
)

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

type Scope struct {
    UUID          string
    Owner         owners.Principal
    Grantee       owners.Principal
    TargetType    TargetType
    TargetAlbumID *string
    AllowDownload bool
    Label         string
    CreatedAt     time.Time
    ExpiresAt     *time.Time

    RevokedAt *time.Time

    BrokerStatus        BrokerStatus
    BrokerRegisteredAt  *time.Time
    BrokerGrantedAt     *time.Time
    BrokerRevokedAt     *time.Time
    BrokerLastError     string
    BrokerAttempts      int
    BrokerNextAttemptAt *time.Time
}

type ScopeDetail struct {
    Scope
    MediaIDs []string // empty for album_live
}

type ScopeFilter struct {
    AlbumID        string
    Grantee        owners.Principal
    Status         []BrokerStatus
    IncludeSettled bool
    Limit          int
    Offset         int
}

const (
    LabelMaxLen       = 200
    MediaSetMaxLen    = 1000
    MaxBrokerAttempts = 10
    PrincipalFieldMaxLen = 255
)

var (
    ErrInvalidGrantee      = errors.New("share: grantee principal is empty, oversized, or equal to caller")
    ErrInvalidLabel        = errors.New("share: label exceeds 200 chars")
    ErrInvalidMediaSet     = errors.New("share: media_set must be 1..1000 unique media ids")
    ErrInvalidTargetCombo  = errors.New("share: target_type does not match payload")
    ErrAlbumEmpty          = errors.New("share: cannot share an empty album_live")
    ErrScopeAlreadyRevoked = errors.New("share: scope is already revoked")
    ErrRetryNotApplicable  = errors.New("share: retry only applies to failed scopes")
    ErrAlbumHasLiveScopes  = errors.New("share: album has outstanding broker grants; revoke them first")
)
```

```go
// internal/broker/broker.go
package broker

import (
    "context"
    "errors"

    "github.com/wesm/fotobank/internal/share"
)

type BrokerClient interface {
    PublishScope(ctx context.Context, s share.Scope) error
    RevokeScope(ctx context.Context, uuid string) error
}

var (
    ErrBrokerTransient = errors.New("broker: transient error")
    ErrBrokerPermanent = errors.New("broker: permanent error")
)
```

---

## Task 1: Migration 000003 — `broker_next_attempt_at` column + ready index

**Files:**
- Create: `internal/db/migrations/000003_scopes_backoff.up.sql`
- Create: `internal/db/migrations/000003_scopes_backoff.down.sql`
- Test: `internal/db/db_test.go` (new test, appended)

- [ ] **Step 1: Write the up migration**

Create `internal/db/migrations/000003_scopes_backoff.up.sql`:

```sql
ALTER TABLE scopes ADD COLUMN broker_next_attempt_at TIMESTAMP;

-- Worker poll ordering index. Partial to keep it tiny: only rows the
-- worker might act on (pending or revoking). Ordered by
-- broker_next_attempt_at so SELECT ... LIMIT N returns due rows first.
CREATE INDEX scopes_broker_ready_idx
    ON scopes(broker_next_attempt_at)
    WHERE broker_status IN ('pending', 'revoking');
```

- [ ] **Step 2: Write the down migration**

Create `internal/db/migrations/000003_scopes_backoff.down.sql`:

```sql
DROP INDEX IF EXISTS scopes_broker_ready_idx;
ALTER TABLE scopes DROP COLUMN broker_next_attempt_at;
```

- [ ] **Step 3: Write the failing DB-level test**

Append to `internal/db/db_test.go`:

```go
func TestScopesBackoffMigration(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    var name string
    err := d.ReadDB().QueryRowContext(context.Background(),
        `SELECT name FROM pragma_table_info('scopes') WHERE name = 'broker_next_attempt_at'`,
    ).Scan(&name)
    r.NoError(err)
    r.Equal("broker_next_attempt_at", name)

    var idx string
    err = d.ReadDB().QueryRowContext(context.Background(),
        `SELECT name FROM sqlite_master WHERE type='index' AND name='scopes_broker_ready_idx'`,
    ).Scan(&idx)
    r.NoError(err)
    r.Equal("scopes_broker_ready_idx", idx)
}
```

Import additions (add if missing): `"context"`, `"github.com/wesm/fotobank/internal/testutil"`.

- [ ] **Step 4: Run the test — expect failure before migrations are created**

Run: `go test ./internal/db/... -run TestScopesBackoffMigration -v`
Expected: FAIL with "no such column: broker_next_attempt_at" or similar if this test is run before step 1+2 committed. If you performed steps 1+2 already, step 5 expects PASS; delete the files, rerun, then restore to confirm the test is truly exercising the migration.

- [ ] **Step 5: Run the test again — expect PASS**

Run: `go test ./internal/db/... -run TestScopesBackoffMigration -v`
Expected: PASS.

- [ ] **Step 6: Verify every other test still passes**

Run: `go test ./... -count=1`
Expected: PASS across the board — the new column is nullable so pre-existing migration/scopes tests are unaffected.

- [ ] **Step 7: Commit**

```bash
git add internal/db/migrations/000003_scopes_backoff.up.sql \
        internal/db/migrations/000003_scopes_backoff.down.sql \
        internal/db/db_test.go
git commit -m "Add scopes.broker_next_attempt_at column + ready index"
```

---

## Task 2: Share domain package (`internal/share/share.go`)

**Files:**
- Create: `internal/share/share.go`
- Create: `internal/share/share_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/share/share_test.go`:

```go
package share_test

import (
    "testing"

    "github.com/stretchr/testify/require"

    "github.com/wesm/fotobank/internal/share"
)

func TestBrokerStatusConstants(t *testing.T) {
    r := require.New(t)
    r.Equal(share.BrokerStatus("pending"), share.StatusPending)
    r.Equal(share.BrokerStatus("active"), share.StatusActive)
    r.Equal(share.BrokerStatus("failed"), share.StatusFailed)
    r.Equal(share.BrokerStatus("revoking"), share.StatusRevoking)
    r.Equal(share.BrokerStatus("revoked_remote"), share.StatusRevokedRemote)
}

func TestTargetTypeConstants(t *testing.T) {
    r := require.New(t)
    r.Equal(share.TargetType("album_live"), share.TargetAlbumLive)
    r.Equal(share.TargetType("media_set"), share.TargetMediaSet)
}

func TestSentinelsAreDistinct(t *testing.T) {
    r := require.New(t)
    all := []error{
        share.ErrInvalidGrantee,
        share.ErrInvalidLabel,
        share.ErrInvalidMediaSet,
        share.ErrInvalidTargetCombo,
        share.ErrAlbumEmpty,
        share.ErrScopeAlreadyRevoked,
        share.ErrRetryNotApplicable,
        share.ErrAlbumHasLiveScopes,
    }
    seen := map[string]bool{}
    for _, e := range all {
        r.False(seen[e.Error()], "duplicate sentinel: %s", e.Error())
        seen[e.Error()] = true
    }
}

func TestLimitsMatchSpec(t *testing.T) {
    r := require.New(t)
    r.Equal(200, share.LabelMaxLen)
    r.Equal(1000, share.MediaSetMaxLen)
    r.Equal(10, share.MaxBrokerAttempts)
    r.Equal(255, share.PrincipalFieldMaxLen)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/share/... -run TestBrokerStatusConstants -v`
Expected: FAIL — package `share` has no types yet.

- [ ] **Step 3: Write the domain types**

Create `internal/share/share.go`:

```go
// Package share defines the scope / scope_media domain types and the
// validation / state-machine sentinels used by the share repo, service,
// HTTP transport, CLI, and outbox worker.
package share

import (
    "errors"
    "time"

    "github.com/wesm/fotobank/internal/owners"
)

// TargetType is the binding category of a scope. Stored verbatim in
// scopes.target_type.
type TargetType string

const (
    TargetAlbumLive TargetType = "album_live"
    TargetMediaSet  TargetType = "media_set"
)

// BrokerStatus is the broker-side state of a scope. Stored verbatim in
// scopes.broker_status.
type BrokerStatus string

const (
    StatusPending       BrokerStatus = "pending"
    StatusActive        BrokerStatus = "active"
    StatusFailed        BrokerStatus = "failed"
    StatusRevoking      BrokerStatus = "revoking"
    StatusRevokedRemote BrokerStatus = "revoked_remote"
)

// Scope mirrors a row in the scopes table. Nullable DB columns are
// *-typed so "never set" stays distinguishable from "set to zero".
type Scope struct {
    UUID          string
    Owner         owners.Principal
    Grantee       owners.Principal
    TargetType    TargetType
    TargetAlbumID *string
    AllowDownload bool
    Label         string
    CreatedAt     time.Time
    ExpiresAt     *time.Time

    RevokedAt *time.Time

    BrokerStatus        BrokerStatus
    BrokerRegisteredAt  *time.Time
    BrokerGrantedAt     *time.Time
    BrokerRevokedAt     *time.Time
    BrokerLastError     string
    BrokerAttempts      int
    BrokerNextAttemptAt *time.Time
}

// ScopeDetail pairs a Scope with its frozen media_set membership.
// MediaIDs is always empty for TargetAlbumLive scopes.
type ScopeDetail struct {
    Scope
    MediaIDs []string
}

// ScopeFilter narrows Repo.ListByOwner / ShareService.List. An empty
// Status slice + IncludeSettled=false means "owner-actionable rows
// only"; the SQL filter hides broker_status = 'revoked_remote'. A
// non-empty Status slice is an exact broker_status IN (...) filter and
// ignores IncludeSettled.
type ScopeFilter struct {
    AlbumID        string
    Grantee        owners.Principal
    Status         []BrokerStatus
    IncludeSettled bool
    Limit          int
    Offset         int
}

const (
    // LabelMaxLen caps scope labels. Service validation enforces this.
    LabelMaxLen = 200
    // MediaSetMaxLen caps deduped media_set membership at mint.
    MediaSetMaxLen = 1000
    // MaxBrokerAttempts is the retry ceiling. After this many attempts
    // against the broker, the worker flips the row to StatusFailed and
    // leaves it there for the owner to Retry or Revoke.
    MaxBrokerAttempts = 10
    // PrincipalFieldMaxLen bounds the hub and user_id fields of a
    // grantee principal. Keeps the broker from receiving garbage.
    PrincipalFieldMaxLen = 255
)

var (
    ErrInvalidGrantee      = errors.New("share: grantee principal is empty, oversized, or equal to caller")
    ErrInvalidLabel        = errors.New("share: label exceeds 200 chars")
    ErrInvalidMediaSet     = errors.New("share: media_set must be 1..1000 unique media ids")
    ErrInvalidTargetCombo  = errors.New("share: target_type does not match payload")
    ErrAlbumEmpty          = errors.New("share: cannot share an empty album_live")
    ErrScopeAlreadyRevoked = errors.New("share: scope is already revoked")
    ErrRetryNotApplicable  = errors.New("share: retry only applies to failed scopes")
    ErrAlbumHasLiveScopes  = errors.New("share: album has outstanding broker grants; revoke them first")
)
```

- [ ] **Step 4: Run the test**

Run: `go test ./internal/share/... -v`
Expected: PASS on all four test functions.

- [ ] **Step 5: Commit**

```bash
git add internal/share/share.go internal/share/share_test.go
git commit -m "Add share domain types, constants, and sentinel errors"
```

---

## Task 3: Broker interface + `NoopBroker`

**Files:**
- Create: `internal/broker/broker.go`
- Create: `internal/broker/noop.go`
- Create: `internal/broker/broker_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/broker/broker_test.go`:

```go
package broker_test

import (
    "context"
    "errors"
    "testing"

    "github.com/stretchr/testify/require"

    "github.com/wesm/fotobank/internal/broker"
    "github.com/wesm/fotobank/internal/share"
)

func TestNoopBrokerImplementsInterface(t *testing.T) {
    r := require.New(t)
    var _ broker.BrokerClient = &broker.NoopBroker{}
    c := &broker.NoopBroker{}
    r.NoError(c.PublishScope(context.Background(), share.Scope{UUID: "u"}))
    r.NoError(c.RevokeScope(context.Background(), "u"))
}

func TestBrokerSentinelsAreDistinct(t *testing.T) {
    r := require.New(t)
    r.False(errors.Is(broker.ErrBrokerTransient, broker.ErrBrokerPermanent))
    r.False(errors.Is(broker.ErrBrokerPermanent, broker.ErrBrokerTransient))
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/broker/... -v`
Expected: FAIL — package `broker` has no types.

- [ ] **Step 3: Create the interface + sentinels**

Create `internal/broker/broker.go`:

```go
// Package broker defines the BrokerClient interface that share.Repo's
// outbox worker uses to register / revoke scopes with an external
// identity-and-grant broker, and ships the production default
// implementation (NoopBroker). Tests use the scripted fake under
// brokertest/.
package broker

import (
    "context"
    "errors"

    "github.com/wesm/fotobank/internal/share"
)

// BrokerClient is the external broker's wire API as seen by fotobank.
// Implementations MUST be idempotent: the outbox worker may retry any
// method after crash or transient failure, and may call RevokeScope on
// a UUID the broker has never seen (success is the correct response).
//
// Error semantics:
//   - errors.Is(err, ErrBrokerTransient) → the worker retries with
//     exponential backoff; the row stays in its current phase.
//   - errors.Is(err, ErrBrokerPermanent) → the worker flips the row to
//     StatusFailed; the owner must Retry or Revoke.
//   - context errors pass through unchanged; the worker aborts this
//     iteration without touching the row.
//   - any other error is treated as transient (err on the side of
//     retry; a permanent misclassification would orphan the row).
type BrokerClient interface {
    // PublishScope registers the scope with the broker and creates the
    // grantee-side grant. Implementations must not inspect scope_media
    // membership; the broker is deliberately ignorant of fotobank's
    // per-scope media set.
    PublishScope(ctx context.Context, s share.Scope) error

    // RevokeScope revokes the grant at the broker. Must treat "never
    // heard of this UUID" as success so the worker can drive pending
    // rows to revoked_remote without first confirming registration.
    RevokeScope(ctx context.Context, uuid string) error
}

var (
    ErrBrokerTransient = errors.New("broker: transient error")
    ErrBrokerPermanent = errors.New("broker: permanent error")
)
```

- [ ] **Step 4: Create NoopBroker**

Create `internal/broker/noop.go`:

```go
package broker

import (
    "context"

    "github.com/wesm/fotobank/internal/share"
)

// NoopBroker is the production default for single-owner deployments
// without a real broker. Both methods return nil immediately. The
// outbox worker treats that as success, so scopes walk the state
// machine pending→active and revoking→revoked_remote cleanly even
// though nothing is happening remotely. Operators wiring a real
// broker replace this with another BrokerClient implementation.
type NoopBroker struct{}

func (NoopBroker) PublishScope(ctx context.Context, _ share.Scope) error { return nil }
func (NoopBroker) RevokeScope(ctx context.Context, _ string) error       { return nil }
```

- [ ] **Step 5: Run the test**

Run: `go test ./internal/broker/... -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/broker/broker.go internal/broker/noop.go internal/broker/broker_test.go
git commit -m "Add BrokerClient interface and NoopBroker production default"
```

---

## Task 4: `brokertest.Fake` scripted test double

**Files:**
- Create: `internal/broker/brokertest/fake.go`
- Create: `internal/broker/brokertest/fake_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/broker/brokertest/fake_test.go`:

```go
package brokertest_test

import (
    "context"
    "errors"
    "testing"

    "github.com/stretchr/testify/require"

    "github.com/wesm/fotobank/internal/broker"
    "github.com/wesm/fotobank/internal/broker/brokertest"
    "github.com/wesm/fotobank/internal/share"
)

func TestFakeZeroValueBehavesLikeNoop(t *testing.T) {
    r := require.New(t)
    f := &brokertest.Fake{}
    r.NoError(f.PublishScope(context.Background(), share.Scope{UUID: "u1"}))
    r.NoError(f.RevokeScope(context.Background(), "u1"))
    r.Equal([]string{"u1"}, f.ObservedPublishes())
    r.Equal([]string{"u1"}, f.ObservedRevokes())
}

func TestFakeQueuedErrorIsReturnedOnce(t *testing.T) {
    r := require.New(t)
    f := &brokertest.Fake{}
    f.QueuePublishError("u1", broker.ErrBrokerTransient)
    err := f.PublishScope(context.Background(), share.Scope{UUID: "u1"})
    r.ErrorIs(err, broker.ErrBrokerTransient)
    // Second call falls back to success — queue is consumed once.
    err = f.PublishScope(context.Background(), share.Scope{UUID: "u1"})
    r.NoError(err)
}

func TestFakePermanentThenTransientQueue(t *testing.T) {
    r := require.New(t)
    f := &brokertest.Fake{}
    f.QueueRevokeError("u1", broker.ErrBrokerPermanent)
    f.QueueRevokeError("u1", broker.ErrBrokerTransient)
    err := f.RevokeScope(context.Background(), "u1")
    r.ErrorIs(err, broker.ErrBrokerPermanent)
    err = f.RevokeScope(context.Background(), "u1")
    r.ErrorIs(err, broker.ErrBrokerTransient)
    err = f.RevokeScope(context.Background(), "u1")
    r.NoError(err)
}

func TestFakeUnrelatedUUIDIsSuccess(t *testing.T) {
    r := require.New(t)
    f := &brokertest.Fake{}
    f.QueuePublishError("u1", errors.New("boom"))
    err := f.PublishScope(context.Background(), share.Scope{UUID: "u2"})
    r.NoError(err)
}

func TestFakeImplementsBrokerClient(t *testing.T) {
    var _ broker.BrokerClient = &brokertest.Fake{}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/broker/brokertest/... -v`
Expected: FAIL — package doesn't exist.

- [ ] **Step 3: Implement the fake**

Create `internal/broker/brokertest/fake.go`:

```go
// Package brokertest provides a scripted broker.BrokerClient used by
// share + shareworker tests. Lives under internal/broker/ so
// production code cannot accidentally import it.
package brokertest

import (
    "context"
    "sync"

    "github.com/wesm/fotobank/internal/broker"
    "github.com/wesm/fotobank/internal/share"
)

// Fake is a scripted BrokerClient. Tests queue per-UUID errors via
// QueuePublishError / QueueRevokeError; each queued error is returned
// once by the matching method call (FIFO per UUID), after which calls
// for that UUID fall back to success. Calls for UUIDs with no queue
// are always success. All calls are recorded so assertions can check
// order and count.
//
// The zero value is usable; no constructor is needed.
type Fake struct {
    mu             sync.Mutex
    publishQueue   map[string][]error
    revokeQueue    map[string][]error
    publishesSeen  []string
    revokesSeen    []string
}

var _ broker.BrokerClient = (*Fake)(nil)

// QueuePublishError appends err to the publish queue for uuid. A
// subsequent PublishScope(s) where s.UUID == uuid returns err instead
// of success, and the queue entry is consumed. Call multiple times to
// script a sequence of responses.
func (f *Fake) QueuePublishError(uuid string, err error) {
    f.mu.Lock()
    defer f.mu.Unlock()
    if f.publishQueue == nil {
        f.publishQueue = map[string][]error{}
    }
    f.publishQueue[uuid] = append(f.publishQueue[uuid], err)
}

// QueueRevokeError is the revoke-side equivalent.
func (f *Fake) QueueRevokeError(uuid string, err error) {
    f.mu.Lock()
    defer f.mu.Unlock()
    if f.revokeQueue == nil {
        f.revokeQueue = map[string][]error{}
    }
    f.revokeQueue[uuid] = append(f.revokeQueue[uuid], err)
}

// ObservedPublishes returns a snapshot of every UUID PublishScope has
// been called with, in call order.
func (f *Fake) ObservedPublishes() []string {
    f.mu.Lock()
    defer f.mu.Unlock()
    out := make([]string, len(f.publishesSeen))
    copy(out, f.publishesSeen)
    return out
}

// ObservedRevokes is the revoke-side equivalent.
func (f *Fake) ObservedRevokes() []string {
    f.mu.Lock()
    defer f.mu.Unlock()
    out := make([]string, len(f.revokesSeen))
    copy(out, f.revokesSeen)
    return out
}

// PublishScope consumes one queued error for s.UUID if present and
// returns it; otherwise returns nil.
func (f *Fake) PublishScope(ctx context.Context, s share.Scope) error {
    f.mu.Lock()
    defer f.mu.Unlock()
    f.publishesSeen = append(f.publishesSeen, s.UUID)
    return popFront(f.publishQueue, s.UUID)
}

// RevokeScope consumes one queued error for uuid if present and
// returns it; otherwise returns nil.
func (f *Fake) RevokeScope(ctx context.Context, uuid string) error {
    f.mu.Lock()
    defer f.mu.Unlock()
    f.revokesSeen = append(f.revokesSeen, uuid)
    return popFront(f.revokeQueue, uuid)
}

func popFront(queue map[string][]error, key string) error {
    if queue == nil {
        return nil
    }
    q, ok := queue[key]
    if !ok || len(q) == 0 {
        return nil
    }
    err := q[0]
    queue[key] = q[1:]
    if len(queue[key]) == 0 {
        delete(queue, key)
    }
    return err
}
```

- [ ] **Step 4: Run the test**

Run: `go test ./internal/broker/brokertest/... -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/broker/brokertest/fake.go internal/broker/brokertest/fake_test.go
git commit -m "Add brokertest.Fake scripted double for share + worker tests"
```

---

## Task 5: `share.Repo` — skeleton, `Insert`, `GetByUUID`

**Files:**
- Create: `internal/share/repo.go`
- Create: `internal/share/repo_test.go`
- Create: `internal/share/trigger_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/share/repo_test.go`:

```go
package share_test

import (
    "context"
    "database/sql"
    "testing"
    "time"

    "github.com/google/uuid"
    "github.com/stretchr/testify/require"

    "github.com/wesm/fotobank/internal/errs"
    "github.com/wesm/fotobank/internal/media"
    "github.com/wesm/fotobank/internal/owners"
    "github.com/wesm/fotobank/internal/share"
    "github.com/wesm/fotobank/internal/testutil"
)

// seedOwner inserts a minimal owners row so scopes FK-checks pass.
func seedOwner(t *testing.T, rw *sql.DB, p owners.Principal, storageKey string) {
    t.Helper()
    _, err := rw.ExecContext(context.Background(),
        `INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
        p.Hub, p.UserID, storageKey, time.Now().UTC(),
    )
    require.NoError(t, err)
}

// seedAlbum inserts a minimal album row owned by p.
func seedAlbum(t *testing.T, rw *sql.DB, p owners.Principal) string {
    t.Helper()
    id := uuid.NewString()
    now := time.Now().UTC()
    _, err := rw.ExecContext(context.Background(),
        `INSERT INTO albums(id, owner_hub, owner_user_id, name, created_at, updated_at)
         VALUES(?,?,?,?,?,?)`,
        id, p.Hub, p.UserID, "t", now, now)
    require.NoError(t, err)
    return id
}

// seedMedia inserts a minimal media row owned by p and returns its ID.
func seedMedia(t *testing.T, rw *sql.DB, p owners.Principal, checksum string) string {
    t.Helper()
    repo := media.NewRepo(rw, rw)
    m := media.Media{
        ID: uuid.NewString(), Owner: p, Type: media.TypePhoto,
        MimeType: "image/jpeg", Path: "2024/" + checksum + ".jpg",
        OriginalFilename: "x.jpg", ImportedAt: time.Now().UTC().Truncate(time.Second),
        Size: 100, Checksum: checksum, ThumbStatus: "pending",
    }
    require.NoError(t, repo.Insert(context.Background(), m))
    return m.ID
}

func TestRepoInsertAlbumLiveAndGet(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    owner := owners.Principal{Hub: "h", UserID: "o"}
    seedOwner(t, d.WriteDB(), owner, "sk")
    albumID := seedAlbum(t, d.WriteDB(), owner)

    repo := share.NewRepo(d.WriteDB(), d.ReadDB())
    s := share.Scope{
        UUID: uuid.NewString(), Owner: owner,
        Grantee:    owners.Principal{Hub: "h", UserID: "g"},
        TargetType: share.TargetAlbumLive,
        TargetAlbumID: &albumID,
        Label:      "Summer", CreatedAt: time.Now().UTC().Truncate(time.Second),
        BrokerStatus: share.StatusPending,
    }
    r.NoError(repo.Insert(context.Background(), s, nil))

    got, err := repo.GetByUUID(context.Background(), s.UUID)
    r.NoError(err)
    r.Equal(s.UUID, got.UUID)
    r.Equal(owner, got.Owner)
    r.Equal(share.TargetAlbumLive, got.TargetType)
    r.NotNil(got.TargetAlbumID)
    r.Equal(albumID, *got.TargetAlbumID)
    r.Equal(share.StatusPending, got.BrokerStatus)
    r.Empty(got.MediaIDs)
}

func TestRepoInsertMediaSetAndGet(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    owner := owners.Principal{Hub: "h", UserID: "o"}
    seedOwner(t, d.WriteDB(), owner, "sk")
    m1 := seedMedia(t, d.WriteDB(), owner, "c1")
    m2 := seedMedia(t, d.WriteDB(), owner, "c2")

    repo := share.NewRepo(d.WriteDB(), d.ReadDB())
    s := share.Scope{
        UUID: uuid.NewString(), Owner: owner,
        Grantee:    owners.Principal{Hub: "h", UserID: "g"},
        TargetType: share.TargetMediaSet,
        CreatedAt:    time.Now().UTC().Truncate(time.Second),
        BrokerStatus: share.StatusPending,
    }
    r.NoError(repo.Insert(context.Background(), s, []string{m1, m2}))

    got, err := repo.GetByUUID(context.Background(), s.UUID)
    r.NoError(err)
    r.Equal(share.TargetMediaSet, got.TargetType)
    r.Nil(got.TargetAlbumID)
    r.ElementsMatch([]string{m1, m2}, got.MediaIDs)
}

func TestRepoGetByUUIDNotFound(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    repo := share.NewRepo(d.WriteDB(), d.ReadDB())
    _, err := repo.GetByUUID(context.Background(), uuid.NewString())
    r.ErrorIs(err, errs.ErrNotFound)
}

func TestRepoInsertRoundtripsAllColumns(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    owner := owners.Principal{Hub: "h", UserID: "o"}
    seedOwner(t, d.WriteDB(), owner, "sk")
    albumID := seedAlbum(t, d.WriteDB(), owner)

    expires := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Second)
    repo := share.NewRepo(d.WriteDB(), d.ReadDB())
    s := share.Scope{
        UUID: uuid.NewString(), Owner: owner,
        Grantee:       owners.Principal{Hub: "h", UserID: "g"},
        TargetType:    share.TargetAlbumLive,
        TargetAlbumID: &albumID,
        AllowDownload: true,
        Label:         "Trip",
        ExpiresAt:     &expires,
        CreatedAt:     time.Now().UTC().Truncate(time.Second),
        BrokerStatus:  share.StatusPending,
    }
    r.NoError(repo.Insert(context.Background(), s, nil))

    got, err := repo.GetByUUID(context.Background(), s.UUID)
    r.NoError(err)
    r.True(got.AllowDownload)
    r.Equal("Trip", got.Label)
    r.NotNil(got.ExpiresAt)
    r.True(got.ExpiresAt.Equal(expires))
}
```

Create `internal/share/trigger_test.go`:

```go
package share_test

import (
    "context"
    "strings"
    "testing"
    "time"

    "github.com/google/uuid"
    "github.com/stretchr/testify/require"

    "github.com/wesm/fotobank/internal/owners"
    "github.com/wesm/fotobank/internal/share"
    "github.com/wesm/fotobank/internal/testutil"
)

// TestOwnerConsistencyTriggerOnScopeMedia ensures the pre-existing SQL
// trigger fires when a scope_media row is inserted with a cross-owner
// media_id. ShareService.Create's per-ID pre-flight is supposed to
// catch this, but the trigger is defence-in-depth and Repo.Insert
// bypasses the service, so we test the trigger directly here.
func TestOwnerConsistencyTriggerOnScopeMedia(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    a := owners.Principal{Hub: "h", UserID: "a"}
    b := owners.Principal{Hub: "h", UserID: "b"}
    seedOwner(t, d.WriteDB(), a, "ska")
    seedOwner(t, d.WriteDB(), b, "skb")
    mBID := seedMedia(t, d.WriteDB(), b, "cB")

    repo := share.NewRepo(d.WriteDB(), d.ReadDB())
    s := share.Scope{
        UUID: uuid.NewString(), Owner: a,
        Grantee:    owners.Principal{Hub: "h", UserID: "g"},
        TargetType: share.TargetMediaSet,
        CreatedAt:    time.Now().UTC().Truncate(time.Second),
        BrokerStatus: share.StatusPending,
    }
    err := repo.Insert(context.Background(), s, []string{mBID})
    r.Error(err)
    r.True(strings.Contains(err.Error(), "album and media must share owner") ||
        strings.Contains(err.Error(), "scope and media must share owner"),
        "expected SQLite trigger ABORT to surface, got %v", err)

    // Confirm nothing was written (rollback).
    row := d.ReadDB().QueryRowContext(context.Background(),
        `SELECT COUNT(*) FROM scopes WHERE uuid = ?`, s.UUID)
    var n int
    r.NoError(row.Scan(&n))
    r.Equal(0, n)
}
```

- [ ] **Step 2: Run the failing tests**

Run: `go test ./internal/share/... -v`
Expected: FAIL — `share.NewRepo`, `share.Repo.Insert`, `share.Repo.GetByUUID` do not exist yet.

- [ ] **Step 3: Implement the repo skeleton + Insert + GetByUUID**

Create `internal/share/repo.go`:

```go
package share

import (
    "context"
    "database/sql"
    "errors"
    "fmt"
    "strings"

    "github.com/wesm/fotobank/internal/errs"
)

// Repo is a SQLite-backed store of scopes + scope_media rows. Split
// read/write pool: writes go through rw, reads through ro.
type Repo struct {
    rw *sql.DB
    ro *sql.DB
}

// NewRepo constructs a Repo. rw must be the writer pool, ro the reader.
func NewRepo(rw, ro *sql.DB) *Repo { return &Repo{rw: rw, ro: ro} }

// All scope columns in a canonical order used by SELECT and Scan.
const scopeSelect = `
    uuid, owner_hub, owner_user_id, grantee_hub, grantee_user_id,
    target_type, target_album_id, allow_download, label,
    created_at, expires_at, revoked_at,
    broker_status, broker_registered_at, broker_granted_at,
    broker_revoked_at, broker_last_error, broker_attempts,
    broker_next_attempt_at
`

// Insert writes the scopes row and (when target_type = media_set) its
// scope_media rows in one transaction. Owner-consistency triggers on
// scope_media and scopes.target_album_id are last-line defence; the
// service layer pre-flights ownership.
func (r *Repo) Insert(ctx context.Context, s Scope, mediaIDs []string) error {
    tx, err := r.rw.BeginTx(ctx, nil)
    if err != nil {
        return fmt.Errorf("begin: %w", err)
    }
    defer func() { _ = tx.Rollback() }()

    _, err = tx.ExecContext(ctx, `
        INSERT INTO scopes (
            uuid, owner_hub, owner_user_id, grantee_hub, grantee_user_id,
            target_type, target_album_id, allow_download, label,
            created_at, expires_at,
            broker_status, broker_attempts
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0)`,
        s.UUID, s.Owner.Hub, s.Owner.UserID, s.Grantee.Hub, s.Grantee.UserID,
        string(s.TargetType), s.TargetAlbumID, boolToInt(s.AllowDownload), nullString(s.Label),
        s.CreatedAt, nullTime(s.ExpiresAt),
        string(s.BrokerStatus),
    )
    if err != nil {
        return fmt.Errorf("insert scope: %w", err)
    }
    if s.TargetType == TargetMediaSet {
        stmt, perr := tx.PrepareContext(ctx,
            `INSERT INTO scope_media (scope_uuid, media_id) VALUES (?, ?)`)
        if perr != nil {
            return fmt.Errorf("prepare scope_media: %w", perr)
        }
        defer stmt.Close()
        for _, mid := range mediaIDs {
            if _, err := stmt.ExecContext(ctx, s.UUID, mid); err != nil {
                return fmt.Errorf("insert scope_media: %w", err)
            }
        }
    }
    return tx.Commit()
}

// GetByUUID returns the scope plus (for media_set) its membership.
// Returns errs.ErrNotFound when no row exists.
func (r *Repo) GetByUUID(ctx context.Context, uuidStr string) (ScopeDetail, error) {
    row := r.ro.QueryRowContext(ctx,
        `SELECT `+scopeSelect+` FROM scopes WHERE uuid = ?`, uuidStr)
    s, err := scanScope(row)
    if errors.Is(err, sql.ErrNoRows) {
        return ScopeDetail{}, fmt.Errorf("%w: scope uuid=%s", errs.ErrNotFound, uuidStr)
    }
    if err != nil {
        return ScopeDetail{}, fmt.Errorf("get scope: %w", err)
    }
    det := ScopeDetail{Scope: s}
    if s.TargetType == TargetMediaSet {
        rows, qerr := r.ro.QueryContext(ctx,
            `SELECT media_id FROM scope_media WHERE scope_uuid = ? ORDER BY media_id`,
            uuidStr)
        if qerr != nil {
            return ScopeDetail{}, fmt.Errorf("list scope_media: %w", qerr)
        }
        defer rows.Close()
        for rows.Next() {
            var mid string
            if err := rows.Scan(&mid); err != nil {
                return ScopeDetail{}, fmt.Errorf("scan scope_media: %w", err)
            }
            det.MediaIDs = append(det.MediaIDs, mid)
        }
        if err := rows.Err(); err != nil {
            return ScopeDetail{}, fmt.Errorf("iter scope_media: %w", err)
        }
    }
    return det, nil
}

type rowScanner interface {
    Scan(dest ...any) error
}

func scanScope(s rowScanner) (Scope, error) {
    var (
        sc            Scope
        targetAlbumID sql.NullString
        allowDownload int64
        label         sql.NullString
        expiresAt     sql.NullTime
        revokedAt     sql.NullTime
        brokerRegAt   sql.NullTime
        brokerGrAt    sql.NullTime
        brokerRevAt   sql.NullTime
        brokerLastErr sql.NullString
        brokerNextAt  sql.NullTime
        targetType    string
        brokerStatus  string
    )
    if err := s.Scan(
        &sc.UUID, &sc.Owner.Hub, &sc.Owner.UserID,
        &sc.Grantee.Hub, &sc.Grantee.UserID,
        &targetType, &targetAlbumID, &allowDownload, &label,
        &sc.CreatedAt, &expiresAt, &revokedAt,
        &brokerStatus, &brokerRegAt, &brokerGrAt,
        &brokerRevAt, &brokerLastErr, &sc.BrokerAttempts,
        &brokerNextAt,
    ); err != nil {
        return Scope{}, err
    }
    sc.TargetType = TargetType(targetType)
    sc.BrokerStatus = BrokerStatus(brokerStatus)
    if targetAlbumID.Valid {
        v := targetAlbumID.String
        sc.TargetAlbumID = &v
    }
    sc.AllowDownload = allowDownload != 0
    if label.Valid {
        sc.Label = label.String
    }
    if expiresAt.Valid {
        v := expiresAt.Time
        sc.ExpiresAt = &v
    }
    if revokedAt.Valid {
        v := revokedAt.Time
        sc.RevokedAt = &v
    }
    if brokerRegAt.Valid {
        v := brokerRegAt.Time
        sc.BrokerRegisteredAt = &v
    }
    if brokerGrAt.Valid {
        v := brokerGrAt.Time
        sc.BrokerGrantedAt = &v
    }
    if brokerRevAt.Valid {
        v := brokerRevAt.Time
        sc.BrokerRevokedAt = &v
    }
    if brokerLastErr.Valid {
        sc.BrokerLastError = brokerLastErr.String
    }
    if brokerNextAt.Valid {
        v := brokerNextAt.Time
        sc.BrokerNextAttemptAt = &v
    }
    return sc, nil
}

func boolToInt(b bool) int {
    if b {
        return 1
    }
    return 0
}

func nullString(s string) any {
    if s == "" {
        return nil
    }
    return s
}

func nullTime(t *time.Time) any {
    if t == nil {
        return nil
    }
    return *t
}
```

Imports for `internal/share/repo.go`:

```go
import (
    "context"
    "database/sql"
    "errors"
    "fmt"
    "time"

    "github.com/wesm/fotobank/internal/errs"
)
```

(`strings` and a `statusPlaceholders` helper land in Task 6 with their
first caller; leaving them unused here would trip the linter.)

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/share/... -v`
Expected: PASS on all Insert/Get/trigger cases.

- [ ] **Step 5: Commit**

```bash
git add internal/share/repo.go internal/share/repo_test.go internal/share/trigger_test.go
git commit -m "Add share.Repo Insert and GetByUUID with scope_media membership"
```

---

## Task 6: `share.Repo.ListByOwner` + `ListReady`

**Files:**
- Modify: `internal/share/repo.go` (append methods)
- Modify: `internal/share/repo_test.go` (append tests)

- [ ] **Step 1: Write the failing tests**

Append to `internal/share/repo_test.go`:

```go
func TestRepoListByOwnerDefaultHidesRevokedRemote(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    owner := owners.Principal{Hub: "h", UserID: "o"}
    seedOwner(t, d.WriteDB(), owner, "sk")
    albumID := seedAlbum(t, d.WriteDB(), owner)
    repo := share.NewRepo(d.WriteDB(), d.ReadDB())

    insertScope := func(status share.BrokerStatus, revokedAt *time.Time) string {
        s := share.Scope{
            UUID: uuid.NewString(), Owner: owner,
            Grantee: owners.Principal{Hub: "h", UserID: "g"},
            TargetType:   share.TargetAlbumLive,
            TargetAlbumID: &albumID,
            CreatedAt:    time.Now().UTC().Truncate(time.Second),
            BrokerStatus: status,
            RevokedAt:    revokedAt,
        }
        r.NoError(repo.Insert(context.Background(), s, nil))
        if status != share.StatusPending || revokedAt != nil {
            now := time.Now().UTC()
            _, err := d.WriteDB().ExecContext(context.Background(),
                `UPDATE scopes SET broker_status = ?, revoked_at = ? WHERE uuid = ?`,
                string(status), nullableTime(revokedAt, now), s.UUID)
            r.NoError(err)
        }
        return s.UUID
    }
    now := time.Now().UTC()
    pendingID  := insertScope(share.StatusPending,       nil)
    activeID   := insertScope(share.StatusActive,        nil)
    failedID   := insertScope(share.StatusFailed,        nil)
    revokingID := insertScope(share.StatusRevoking,      &now)
    remoteID   := insertScope(share.StatusRevokedRemote, &now)

    // Default filter hides revoked_remote; everything else visible.
    got, err := repo.ListByOwner(context.Background(), owner, share.ScopeFilter{})
    r.NoError(err)
    got = ids(got)
    r.ElementsMatch([]string{pendingID, activeID, failedID, revokingID}, got)
    r.NotContains(got, remoteID)

    // IncludeSettled=true returns everything.
    got, err = repo.ListByOwner(context.Background(), owner, share.ScopeFilter{IncludeSettled: true})
    r.NoError(err)
    got = ids(got)
    r.Contains(got, remoteID)

    // Explicit status filter returns exactly those statuses.
    got, err = repo.ListByOwner(context.Background(), owner, share.ScopeFilter{
        Status: []share.BrokerStatus{share.StatusFailed, share.StatusRevokedRemote},
    })
    r.NoError(err)
    got = ids(got)
    r.ElementsMatch([]string{failedID, remoteID}, got)
}

func TestRepoListByOwnerScopedToCaller(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    a := owners.Principal{Hub: "h", UserID: "a"}
    b := owners.Principal{Hub: "h", UserID: "b"}
    seedOwner(t, d.WriteDB(), a, "ska")
    seedOwner(t, d.WriteDB(), b, "skb")
    alA := seedAlbum(t, d.WriteDB(), a)
    alB := seedAlbum(t, d.WriteDB(), b)
    repo := share.NewRepo(d.WriteDB(), d.ReadDB())

    insert := func(owner owners.Principal, albumID string) string {
        s := share.Scope{
            UUID: uuid.NewString(), Owner: owner,
            Grantee:       owners.Principal{Hub: "h", UserID: "g"},
            TargetType:    share.TargetAlbumLive,
            TargetAlbumID: &albumID,
            CreatedAt:     time.Now().UTC().Truncate(time.Second),
            BrokerStatus:  share.StatusPending,
        }
        r.NoError(repo.Insert(context.Background(), s, nil))
        return s.UUID
    }
    sA := insert(a, alA)
    sB := insert(b, alB)

    got, err := repo.ListByOwner(context.Background(), a, share.ScopeFilter{})
    r.NoError(err)
    ids := ids(got)
    r.ElementsMatch([]string{sA}, ids)
    r.NotContains(ids, sB)
}

func TestRepoListReadyReturnsDueRowsOnly(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    owner := owners.Principal{Hub: "h", UserID: "o"}
    seedOwner(t, d.WriteDB(), owner, "sk")
    albumID := seedAlbum(t, d.WriteDB(), owner)
    repo := share.NewRepo(d.WriteDB(), d.ReadDB())

    future := time.Now().UTC().Add(1 * time.Hour)
    past   := time.Now().UTC().Add(-1 * time.Hour)

    insert := func(status share.BrokerStatus, nextAt *time.Time) string {
        s := share.Scope{
            UUID: uuid.NewString(), Owner: owner,
            Grantee:       owners.Principal{Hub: "h", UserID: "g"},
            TargetType:    share.TargetAlbumLive,
            TargetAlbumID: &albumID,
            CreatedAt:     time.Now().UTC().Truncate(time.Second),
            BrokerStatus:  share.StatusPending,
        }
        r.NoError(repo.Insert(context.Background(), s, nil))
        _, err := d.WriteDB().ExecContext(context.Background(),
            `UPDATE scopes SET broker_status = ?, broker_next_attempt_at = ? WHERE uuid = ?`,
            string(status), nullableTime(nextAt, time.Now().UTC()), s.UUID)
        r.NoError(err)
        return s.UUID
    }
    pendingNow    := insert(share.StatusPending,  nil)
    pendingPast   := insert(share.StatusPending,  &past)
    pendingFuture := insert(share.StatusPending,  &future)
    revokingNow   := insert(share.StatusRevoking, nil)
    active        := insert(share.StatusActive,   nil)

    rows, err := repo.ListReady(context.Background(), time.Now().UTC(), 100)
    r.NoError(err)
    got := ids(rows)
    r.ElementsMatch([]string{pendingNow, pendingPast, revokingNow}, got)
    r.NotContains(got, pendingFuture)
    r.NotContains(got, active)
}

func TestRepoListReadyRespectsMaxBrokerAttempts(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    owner := owners.Principal{Hub: "h", UserID: "o"}
    seedOwner(t, d.WriteDB(), owner, "sk")
    albumID := seedAlbum(t, d.WriteDB(), owner)
    repo := share.NewRepo(d.WriteDB(), d.ReadDB())

    s := share.Scope{
        UUID: uuid.NewString(), Owner: owner,
        Grantee:       owners.Principal{Hub: "h", UserID: "g"},
        TargetType:    share.TargetAlbumLive,
        TargetAlbumID: &albumID,
        CreatedAt:     time.Now().UTC().Truncate(time.Second),
        BrokerStatus:  share.StatusPending,
    }
    r.NoError(repo.Insert(context.Background(), s, nil))
    _, err := d.WriteDB().ExecContext(context.Background(),
        `UPDATE scopes SET broker_attempts = ? WHERE uuid = ?`,
        share.MaxBrokerAttempts, s.UUID)
    r.NoError(err)

    rows, err := repo.ListReady(context.Background(), time.Now().UTC(), 100)
    r.NoError(err)
    r.NotContains(ids(rows), s.UUID)
}

// ids extracts UUIDs so callers can use ElementsMatch cleanly.
func ids(rows []share.Scope) []string {
    out := make([]string, len(rows))
    for i, s := range rows {
        out[i] = s.UUID
    }
    return out
}

// nullableTime returns t for UPDATE binding; when t is nil returns
// nil (maps to SQL NULL). fallback unused; kept for call-site symmetry.
func nullableTime(t *time.Time, _ time.Time) any {
    if t == nil {
        return nil
    }
    return *t
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/share/... -v`
Expected: FAIL — `ListByOwner`, `ListReady` don't exist.

- [ ] **Step 3: Implement the methods**

Append to `internal/share/repo.go`:

```go
// ListByOwner returns scopes owned by this principal, filtered and
// paginated. When len(filter.Status) == 0 and filter.IncludeSettled
// is false, broker_status = 'revoked_remote' is hidden by default;
// every other row is visible. When len(filter.Status) > 0, only those
// statuses are included and IncludeSettled is ignored. AlbumID /
// Grantee further narrow the result when non-zero.
func (r *Repo) ListByOwner(ctx context.Context, owner owners.Principal, filter ScopeFilter) ([]Scope, error) {
    var (
        sb   strings.Builder
        args []any
    )
    sb.WriteString(`SELECT ` + scopeSelect + ` FROM scopes WHERE owner_hub = ? AND owner_user_id = ?`)
    args = append(args, owner.Hub, owner.UserID)

    switch {
    case len(filter.Status) > 0:
        ph, sargs := statusPlaceholders(filter.Status)
        sb.WriteString(` AND broker_status IN (` + ph + `)`)
        args = append(args, sargs...)
    case !filter.IncludeSettled:
        sb.WriteString(` AND broker_status != 'revoked_remote'`)
    }
    if filter.AlbumID != "" {
        sb.WriteString(` AND target_album_id = ?`)
        args = append(args, filter.AlbumID)
    }
    if !filter.Grantee.IsZero() {
        sb.WriteString(` AND grantee_hub = ? AND grantee_user_id = ?`)
        args = append(args, filter.Grantee.Hub, filter.Grantee.UserID)
    }
    sb.WriteString(` ORDER BY created_at DESC, uuid ASC`)
    if filter.Limit > 0 {
        sb.WriteString(` LIMIT ?`)
        args = append(args, filter.Limit)
        if filter.Offset > 0 {
            sb.WriteString(` OFFSET ?`)
            args = append(args, filter.Offset)
        }
    }

    rows, err := r.ro.QueryContext(ctx, sb.String(), args...)
    if err != nil {
        return nil, fmt.Errorf("list scopes: %w", err)
    }
    defer rows.Close()
    var out []Scope
    for rows.Next() {
        s, err := scanScope(rows)
        if err != nil {
            return nil, fmt.Errorf("scan scope: %w", err)
        }
        out = append(out, s)
    }
    return out, rows.Err()
}

// ListReady returns up to limit scopes the outbox worker should attempt
// right now: status in (pending, revoking), broker_next_attempt_at is
// either NULL or <= now, and broker_attempts < MaxBrokerAttempts.
// Ordered by broker_next_attempt_at ASC NULLS FIRST, then created_at
// ASC, so newly-inserted rows are picked up promptly.
func (r *Repo) ListReady(ctx context.Context, now time.Time, limit int) ([]Scope, error) {
    if limit <= 0 {
        limit = 20
    }
    rows, err := r.ro.QueryContext(ctx,
        `SELECT `+scopeSelect+` FROM scopes
          WHERE broker_status IN ('pending', 'revoking')
            AND (broker_next_attempt_at IS NULL OR broker_next_attempt_at <= ?)
            AND broker_attempts < ?
          ORDER BY (broker_next_attempt_at IS NULL) DESC,
                   broker_next_attempt_at ASC,
                   created_at ASC
          LIMIT ?`,
        now, MaxBrokerAttempts, limit)
    if err != nil {
        return nil, fmt.Errorf("list ready scopes: %w", err)
    }
    defer rows.Close()
    var out []Scope
    for rows.Next() {
        s, err := scanScope(rows)
        if err != nil {
            return nil, fmt.Errorf("scan scope: %w", err)
        }
        out = append(out, s)
    }
    return out, rows.Err()
}

// statusPlaceholders renders `IN (?,?,?)` argument tuples. Returns the
// placeholder string and the []any args, both empty when the input is
// empty. Used here by ListByOwner and in later tasks for filtered
// UPDATEs.
func statusPlaceholders(statuses []BrokerStatus) (string, []any) {
    if len(statuses) == 0 {
        return "", nil
    }
    parts := make([]string, len(statuses))
    args := make([]any, len(statuses))
    for i, s := range statuses {
        parts[i] = "?"
        args[i] = string(s)
    }
    return strings.Join(parts, ","), args
}
```

Update the import block at the top of `internal/share/repo.go` to
include `"strings"` (needed by both `strings.Builder` above and
`statusPlaceholders`):

```go
import (
    "context"
    "database/sql"
    "errors"
    "fmt"
    "strings"
    "time"

    "github.com/wesm/fotobank/internal/errs"
)
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/share/... -v`
Expected: PASS on all ListByOwner / ListReady tests.

- [ ] **Step 5: Commit**

```bash
git add internal/share/repo.go internal/share/repo_test.go
git commit -m "Add share.Repo ListByOwner and ListReady"
```

---

## Task 7: `share.Repo` state-transition UPDATEs (publish side)

**Files:**
- Modify: `internal/share/repo.go`
- Modify: `internal/share/repo_test.go`

Implements `MarkPublished`, `MarkAttemptFailed`, `MarkFailed`. These
are the write-side methods the worker calls after `PublishScope`.
Each uses a fenced `WHERE` so a concurrent `Revoke` or parallel
worker cannot corrupt state.

- [ ] **Step 1: Write the failing tests**

Append to `internal/share/repo_test.go`:

```go
func TestRepoMarkPublishedPendingToActive(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    repo := share.NewRepo(d.WriteDB(), d.ReadDB())
    uuidStr := seedPendingAlbumScope(t, d, repo)

    now := time.Now().UTC().Truncate(time.Second)
    n, err := repo.MarkPublished(context.Background(), uuidStr, now)
    r.NoError(err)
    r.Equal(int64(1), n)

    got, err := repo.GetByUUID(context.Background(), uuidStr)
    r.NoError(err)
    r.Equal(share.StatusActive, got.BrokerStatus)
    r.NotNil(got.BrokerRegisteredAt)
    r.NotNil(got.BrokerGrantedAt)
    r.True(got.BrokerRegisteredAt.Equal(now))
    r.True(got.BrokerGrantedAt.Equal(now))
    r.Empty(got.BrokerLastError)
    r.Nil(got.BrokerNextAttemptAt)
}

func TestRepoMarkPublishedRevokingRecordsTimestampsButKeepsStatus(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    repo := share.NewRepo(d.WriteDB(), d.ReadDB())
    uuidStr := seedPendingAlbumScope(t, d, repo)

    // Owner-Revoke raced the worker: flip to revoking directly.
    revokedAt := time.Now().UTC().Truncate(time.Second)
    _, err := d.WriteDB().ExecContext(context.Background(),
        `UPDATE scopes SET broker_status='revoking', revoked_at=? WHERE uuid=?`,
        revokedAt, uuidStr)
    r.NoError(err)

    now := revokedAt.Add(1 * time.Second)
    n, err := repo.MarkPublished(context.Background(), uuidStr, now)
    r.NoError(err)
    r.Equal(int64(1), n) // update still lands; status is unchanged.

    got, err := repo.GetByUUID(context.Background(), uuidStr)
    r.NoError(err)
    r.Equal(share.StatusRevoking, got.BrokerStatus)
    r.NotNil(got.BrokerRegisteredAt)
    r.NotNil(got.BrokerGrantedAt)
    r.NotNil(got.RevokedAt)
}

func TestRepoMarkPublishedNoopOnTerminalStatus(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    repo := share.NewRepo(d.WriteDB(), d.ReadDB())
    uuidStr := seedPendingAlbumScope(t, d, repo)

    // Move to revoked_remote directly.
    _, err := d.WriteDB().ExecContext(context.Background(),
        `UPDATE scopes SET broker_status='revoked_remote' WHERE uuid=?`,
        uuidStr)
    r.NoError(err)

    n, err := repo.MarkPublished(context.Background(), uuidStr, time.Now().UTC())
    r.NoError(err)
    r.Equal(int64(0), n) // fence rejected — row was terminal.
}

func TestRepoMarkAttemptFailedIncrementsAttempts(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    repo := share.NewRepo(d.WriteDB(), d.ReadDB())
    uuidStr := seedPendingAlbumScope(t, d, repo)

    nextAt := time.Now().UTC().Add(30 * time.Second).Truncate(time.Second)
    n, err := repo.MarkAttemptFailed(context.Background(), uuidStr, share.StatusPending, "boom", nextAt)
    r.NoError(err)
    r.Equal(int64(1), n)

    got, err := repo.GetByUUID(context.Background(), uuidStr)
    r.NoError(err)
    r.Equal(share.StatusPending, got.BrokerStatus)
    r.Equal(1, got.BrokerAttempts)
    r.Equal("boom", got.BrokerLastError)
    r.NotNil(got.BrokerNextAttemptAt)
    r.True(got.BrokerNextAttemptAt.Equal(nextAt))
}

func TestRepoMarkAttemptFailedFencedToPhase(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    repo := share.NewRepo(d.WriteDB(), d.ReadDB())
    uuidStr := seedPendingAlbumScope(t, d, repo)

    // Row is pending; calling with phase=revoking must be a no-op.
    nextAt := time.Now().UTC().Add(30 * time.Second).Truncate(time.Second)
    n, err := repo.MarkAttemptFailed(context.Background(), uuidStr, share.StatusRevoking, "wrong", nextAt)
    r.NoError(err)
    r.Equal(int64(0), n)

    got, err := repo.GetByUUID(context.Background(), uuidStr)
    r.NoError(err)
    r.Equal(0, got.BrokerAttempts)
    r.Empty(got.BrokerLastError)
    r.Nil(got.BrokerNextAttemptAt)
}

func TestRepoMarkFailedFlipsToFailed(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    repo := share.NewRepo(d.WriteDB(), d.ReadDB())
    uuidStr := seedPendingAlbumScope(t, d, repo)

    n, err := repo.MarkFailed(context.Background(), uuidStr, share.StatusPending, "fatal")
    r.NoError(err)
    r.Equal(int64(1), n)

    got, err := repo.GetByUUID(context.Background(), uuidStr)
    r.NoError(err)
    r.Equal(share.StatusFailed, got.BrokerStatus)
    r.Equal(1, got.BrokerAttempts)
    r.Equal("fatal", got.BrokerLastError)
    r.Nil(got.BrokerNextAttemptAt)
    r.Nil(got.RevokedAt)
}

// seedPendingAlbumScope returns the UUID of a freshly-inserted album_live
// scope in status pending. Reused across state-transition tests.
func seedPendingAlbumScope(t *testing.T, d *dbDB, repo *share.Repo) string {
    t.Helper()
    owner := owners.Principal{Hub: "h", UserID: "o"}
    // seedOwner is idempotent-enough for a freshly-minted DB: insert once.
    _, _ = d.WriteDB().ExecContext(context.Background(),
        `INSERT OR IGNORE INTO owners(hub, user_id, storage_key, created_at)
         VALUES(?,?,?,?)`, owner.Hub, owner.UserID, "sk", time.Now().UTC())
    albumID := seedAlbum(t, d.WriteDB(), owner)
    s := share.Scope{
        UUID: uuid.NewString(), Owner: owner,
        Grantee:       owners.Principal{Hub: "h", UserID: "g"},
        TargetType:    share.TargetAlbumLive,
        TargetAlbumID: &albumID,
        CreatedAt:     time.Now().UTC().Truncate(time.Second),
        BrokerStatus:  share.StatusPending,
    }
    require.NoError(t, repo.Insert(context.Background(), s, nil))
    return s.UUID
}

// dbDB is a tiny interface alias so seedPendingAlbumScope can accept
// either *db.DB or a test fake that exposes WriteDB(). Only WriteDB()
// is needed, so an interface avoids importing the real db package
// transitively in test helpers that callers may share across files.
type dbDB interface {
    WriteDB() *sql.DB
    ReadDB() *sql.DB
}
```

- [ ] **Step 2: Run the failing tests**

Run: `go test ./internal/share/... -run TestRepoMark -v`
Expected: FAIL — `MarkPublished`, `MarkAttemptFailed`, `MarkFailed` don't exist.

- [ ] **Step 3: Implement the UPDATEs**

Append to `internal/share/repo.go`:

```go
// MarkPublished records a successful PublishScope. Timestamps land
// whether the row is still 'pending' or has already moved to
// 'revoking' (owner-Revoke raced), so the owner-facing UI shows
// "was granted" honestly. Only the status transition to 'active'
// is fenced to broker_status = 'pending'; if the row is already
// 'revoking', status stays 'revoking' and the worker issues
// RevokeScope on the next tick.
//
// Rows-affected = 1 does NOT mean broker_status is now 'active';
// it may still be 'revoking'. Callers that care must re-read.
func (r *Repo) MarkPublished(ctx context.Context, uuidStr string, at time.Time) (int64, error) {
    res, err := r.rw.ExecContext(ctx,
        `UPDATE scopes
            SET broker_registered_at = COALESCE(broker_registered_at, ?),
                broker_granted_at    = COALESCE(broker_granted_at, ?),
                broker_status = CASE WHEN broker_status = 'pending' THEN 'active' ELSE broker_status END,
                broker_last_error = CASE WHEN broker_status = 'pending' THEN '' ELSE broker_last_error END,
                broker_next_attempt_at = CASE WHEN broker_status = 'pending' THEN NULL ELSE broker_next_attempt_at END
          WHERE uuid = ? AND broker_status IN ('pending', 'revoking')`,
        at, at, uuidStr)
    if err != nil {
        return 0, fmt.Errorf("mark published: %w", err)
    }
    n, err := res.RowsAffected()
    if err != nil {
        return 0, fmt.Errorf("mark published rows affected: %w", err)
    }
    return n, nil
}

// MarkAttemptFailed records a retryable broker failure. Bumps
// broker_attempts, stores err, and schedules the next attempt. Fenced
// to the caller-declared phase (pending or revoking) so the row
// cannot drift into the wrong state if owner Revoke raced between
// ListReady and this UPDATE.
func (r *Repo) MarkAttemptFailed(ctx context.Context, uuidStr string, phase BrokerStatus, errMsg string, nextAt time.Time) (int64, error) {
    res, err := r.rw.ExecContext(ctx,
        `UPDATE scopes
            SET broker_attempts = broker_attempts + 1,
                broker_last_error = ?,
                broker_next_attempt_at = ?
          WHERE uuid = ? AND broker_status = ?`,
        errMsg, nextAt, uuidStr, string(phase))
    if err != nil {
        return 0, fmt.Errorf("mark attempt failed: %w", err)
    }
    n, err := res.RowsAffected()
    if err != nil {
        return 0, fmt.Errorf("mark attempt failed rows affected: %w", err)
    }
    return n, nil
}

// MarkFailed flips a scope to broker_status = 'failed' after exceeding
// MaxBrokerAttempts or on a permanent error. Fenced to the caller-
// declared phase. broker_attempts is bumped one more time (so the row
// records that the final attempt happened).
func (r *Repo) MarkFailed(ctx context.Context, uuidStr string, phase BrokerStatus, errMsg string) (int64, error) {
    res, err := r.rw.ExecContext(ctx,
        `UPDATE scopes
            SET broker_status = 'failed',
                broker_attempts = broker_attempts + 1,
                broker_last_error = ?,
                broker_next_attempt_at = NULL
          WHERE uuid = ? AND broker_status = ?`,
        errMsg, uuidStr, string(phase))
    if err != nil {
        return 0, fmt.Errorf("mark failed: %w", err)
    }
    n, err := res.RowsAffected()
    if err != nil {
        return 0, fmt.Errorf("mark failed rows affected: %w", err)
    }
    return n, nil
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/share/... -v`
Expected: PASS across old + new cases.

- [ ] **Step 5: Commit**

```bash
git add internal/share/repo.go internal/share/repo_test.go
git commit -m "Add share.Repo publish-side state UPDATEs (MarkPublished, MarkAttemptFailed, MarkFailed)"
```

---

## Task 8: `share.Repo` revoke / retry UPDATEs

**Files:**
- Modify: `internal/share/repo.go`
- Modify: `internal/share/repo_test.go`

Implements `SetRevoking` (service-called, from any live state),
`MarkRevoked` (worker-called on `RevokeScope` success), `RetryPublish`,
`RetryRevoke`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/share/repo_test.go`:

```go
func TestRepoSetRevokingFromPending(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    repo := share.NewRepo(d.WriteDB(), d.ReadDB())
    uuidStr := seedPendingAlbumScope(t, d, repo)

    now := time.Now().UTC().Truncate(time.Second)
    n, err := repo.SetRevoking(context.Background(), uuidStr, now)
    r.NoError(err)
    r.Equal(int64(1), n)

    got, err := repo.GetByUUID(context.Background(), uuidStr)
    r.NoError(err)
    r.Equal(share.StatusRevoking, got.BrokerStatus)
    r.NotNil(got.RevokedAt)
    r.True(got.RevokedAt.Equal(now))
    r.Equal(0, got.BrokerAttempts)
    r.Nil(got.BrokerNextAttemptAt)
}

func TestRepoSetRevokingFromFailedPublishPhase(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    repo := share.NewRepo(d.WriteDB(), d.ReadDB())
    uuidStr := seedPendingAlbumScope(t, d, repo)
    _, err := d.WriteDB().ExecContext(context.Background(),
        `UPDATE scopes SET broker_status='failed', broker_attempts=10, broker_last_error='gone' WHERE uuid=?`,
        uuidStr)
    r.NoError(err)

    n, err := repo.SetRevoking(context.Background(), uuidStr, time.Now().UTC())
    r.NoError(err)
    r.Equal(int64(1), n)

    got, err := repo.GetByUUID(context.Background(), uuidStr)
    r.NoError(err)
    r.Equal(share.StatusRevoking, got.BrokerStatus)
    r.Equal(0, got.BrokerAttempts)
    r.Empty(got.BrokerLastError)
}

func TestRepoSetRevokingRejectsRevokedRemote(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    repo := share.NewRepo(d.WriteDB(), d.ReadDB())
    uuidStr := seedPendingAlbumScope(t, d, repo)
    now := time.Now().UTC()
    _, err := d.WriteDB().ExecContext(context.Background(),
        `UPDATE scopes SET broker_status='revoked_remote', revoked_at=? WHERE uuid=?`,
        now, uuidStr)
    r.NoError(err)

    n, err := repo.SetRevoking(context.Background(), uuidStr, now.Add(time.Second))
    r.NoError(err)
    r.Equal(int64(0), n)
}

func TestRepoSetRevokingRejectsRevokingRow(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    repo := share.NewRepo(d.WriteDB(), d.ReadDB())
    uuidStr := seedPendingAlbumScope(t, d, repo)
    now := time.Now().UTC()
    _, err := d.WriteDB().ExecContext(context.Background(),
        `UPDATE scopes SET broker_status='revoking', revoked_at=? WHERE uuid=?`,
        now, uuidStr)
    r.NoError(err)

    n, err := repo.SetRevoking(context.Background(), uuidStr, now.Add(time.Second))
    r.NoError(err)
    r.Equal(int64(0), n)
}

func TestRepoMarkRevokedRevokingToRevokedRemote(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    repo := share.NewRepo(d.WriteDB(), d.ReadDB())
    uuidStr := seedPendingAlbumScope(t, d, repo)
    _, err := d.WriteDB().ExecContext(context.Background(),
        `UPDATE scopes SET broker_status='revoking', revoked_at=? WHERE uuid=?`,
        time.Now().UTC(), uuidStr)
    r.NoError(err)

    at := time.Now().UTC().Add(time.Second).Truncate(time.Second)
    n, err := repo.MarkRevoked(context.Background(), uuidStr, at)
    r.NoError(err)
    r.Equal(int64(1), n)

    got, err := repo.GetByUUID(context.Background(), uuidStr)
    r.NoError(err)
    r.Equal(share.StatusRevokedRemote, got.BrokerStatus)
    r.NotNil(got.BrokerRevokedAt)
    r.True(got.BrokerRevokedAt.Equal(at))
    r.Nil(got.BrokerNextAttemptAt)
}

func TestRepoMarkRevokedRejectsOtherStates(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    repo := share.NewRepo(d.WriteDB(), d.ReadDB())
    uuidStr := seedPendingAlbumScope(t, d, repo) // still 'pending'

    n, err := repo.MarkRevoked(context.Background(), uuidStr, time.Now().UTC())
    r.NoError(err)
    r.Equal(int64(0), n)
}

func TestRepoRetryPublishOnlyFailedWithoutRevokedAt(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    repo := share.NewRepo(d.WriteDB(), d.ReadDB())
    uuidStr := seedPendingAlbumScope(t, d, repo)
    _, err := d.WriteDB().ExecContext(context.Background(),
        `UPDATE scopes SET broker_status='failed', broker_attempts=10, broker_last_error='x' WHERE uuid=?`,
        uuidStr)
    r.NoError(err)

    n, err := repo.RetryPublish(context.Background(), uuidStr)
    r.NoError(err)
    r.Equal(int64(1), n)
    got, err := repo.GetByUUID(context.Background(), uuidStr)
    r.NoError(err)
    r.Equal(share.StatusPending, got.BrokerStatus)
    r.Equal(0, got.BrokerAttempts)
    r.Empty(got.BrokerLastError)
    r.Nil(got.BrokerNextAttemptAt)
}

func TestRepoRetryPublishRejectsRevokedFailed(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    repo := share.NewRepo(d.WriteDB(), d.ReadDB())
    uuidStr := seedPendingAlbumScope(t, d, repo)
    now := time.Now().UTC()
    _, err := d.WriteDB().ExecContext(context.Background(),
        `UPDATE scopes SET broker_status='failed', broker_attempts=10, revoked_at=? WHERE uuid=?`,
        now, uuidStr)
    r.NoError(err)

    n, err := repo.RetryPublish(context.Background(), uuidStr)
    r.NoError(err)
    r.Equal(int64(0), n)
}

func TestRepoRetryRevokeOnlyFailedWithRevokedAt(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    repo := share.NewRepo(d.WriteDB(), d.ReadDB())
    uuidStr := seedPendingAlbumScope(t, d, repo)
    now := time.Now().UTC()
    _, err := d.WriteDB().ExecContext(context.Background(),
        `UPDATE scopes SET broker_status='failed', broker_attempts=10, revoked_at=? WHERE uuid=?`,
        now, uuidStr)
    r.NoError(err)

    n, err := repo.RetryRevoke(context.Background(), uuidStr)
    r.NoError(err)
    r.Equal(int64(1), n)
    got, err := repo.GetByUUID(context.Background(), uuidStr)
    r.NoError(err)
    r.Equal(share.StatusRevoking, got.BrokerStatus)
    r.Equal(0, got.BrokerAttempts)
}
```

- [ ] **Step 2: Run the failing tests**

Run: `go test ./internal/share/... -run 'TestRepoSetRevoking|TestRepoMarkRevoked|TestRepoRetry' -v`
Expected: FAIL — methods don't exist.

- [ ] **Step 3: Implement the UPDATEs**

Append to `internal/share/repo.go`:

```go
// SetRevoking is the service-level entry point for Revoke. It marks
// the scope as revoked locally and queues the broker revocation. The
// caller-declared "at" is written only if revoked_at is currently
// NULL, so a second revoke is a no-op instead of clobbering the
// original revoke timestamp. Fenced to states that are still
// owner-actionable.
//
// Returns rows-affected. 0 means the row was already revoking,
// revoked_remote, or does not exist; the service converts that to
// ErrScopeAlreadyRevoked.
func (r *Repo) SetRevoking(ctx context.Context, uuidStr string, at time.Time) (int64, error) {
    res, err := r.rw.ExecContext(ctx,
        `UPDATE scopes
            SET broker_status = 'revoking',
                revoked_at = COALESCE(revoked_at, ?),
                broker_attempts = 0,
                broker_next_attempt_at = NULL,
                broker_last_error = ''
          WHERE uuid = ?
            AND broker_status IN ('pending', 'active', 'failed')
            AND revoked_at IS NULL`,
        at, uuidStr)
    if err != nil {
        return 0, fmt.Errorf("set revoking: %w", err)
    }
    n, err := res.RowsAffected()
    if err != nil {
        return 0, fmt.Errorf("set revoking rows affected: %w", err)
    }
    return n, nil
}

// MarkRevoked transitions a revoking scope to revoked_remote after a
// successful RevokeScope call. Fenced to broker_status = 'revoking'
// AND revoked_at IS NOT NULL — both invariants are established by
// SetRevoking, so a row that satisfies the status fence without a
// revoked_at is a bug upstream; we refuse to transition rather than
// silently producing a revoked_remote row with no local revoke
// timestamp.
func (r *Repo) MarkRevoked(ctx context.Context, uuidStr string, at time.Time) (int64, error) {
    res, err := r.rw.ExecContext(ctx,
        `UPDATE scopes
            SET broker_status = 'revoked_remote',
                broker_revoked_at = COALESCE(broker_revoked_at, ?),
                broker_last_error = '',
                broker_next_attempt_at = NULL
          WHERE uuid = ?
            AND broker_status = 'revoking'
            AND revoked_at IS NOT NULL`,
        at, uuidStr)
    if err != nil {
        return 0, fmt.Errorf("mark revoked: %w", err)
    }
    n, err := res.RowsAffected()
    if err != nil {
        return 0, fmt.Errorf("mark revoked rows affected: %w", err)
    }
    return n, nil
}

// RetryPublish moves a failed scope back to pending so the worker
// retries PublishScope. Applies only to failed rows whose revoked_at
// is NULL (publish-side stall). Returns rows-affected.
func (r *Repo) RetryPublish(ctx context.Context, uuidStr string) (int64, error) {
    res, err := r.rw.ExecContext(ctx,
        `UPDATE scopes
            SET broker_status = 'pending',
                broker_attempts = 0,
                broker_next_attempt_at = NULL,
                broker_last_error = ''
          WHERE uuid = ? AND broker_status = 'failed' AND revoked_at IS NULL`,
        uuidStr)
    if err != nil {
        return 0, fmt.Errorf("retry publish: %w", err)
    }
    return res.RowsAffected()
}

// RetryRevoke moves a failed scope back to revoking so the worker
// retries RevokeScope. Applies only to failed rows whose revoked_at
// is NOT NULL (revoke-side stall). Returns rows-affected.
func (r *Repo) RetryRevoke(ctx context.Context, uuidStr string) (int64, error) {
    res, err := r.rw.ExecContext(ctx,
        `UPDATE scopes
            SET broker_status = 'revoking',
                broker_attempts = 0,
                broker_next_attempt_at = NULL,
                broker_last_error = ''
          WHERE uuid = ? AND broker_status = 'failed' AND revoked_at IS NOT NULL`,
        uuidStr)
    if err != nil {
        return 0, fmt.Errorf("retry revoke: %w", err)
    }
    return res.RowsAffected()
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/share/... -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/share/repo.go internal/share/repo_test.go
git commit -m "Add share.Repo SetRevoking, MarkRevoked, and retry UPDATEs"
```

---

## Task 9: `share.Repo.PrepareAlbumDeleteTx` + `HasBlockingScopesForAlbum`

**Files:**
- Modify: `internal/share/repo.go`
- Modify: `internal/share/repo_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/share/repo_test.go`:

```go
func TestRepoPrepareAlbumDeleteTxBlocksOnLiveScopes(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    repo := share.NewRepo(d.WriteDB(), d.ReadDB())
    uuidStr := seedPendingAlbumScope(t, d, repo)

    got, err := repo.GetByUUID(context.Background(), uuidStr)
    r.NoError(err)
    r.NotNil(got.TargetAlbumID)
    albumID := *got.TargetAlbumID

    tx, err := d.WriteDB().BeginTx(context.Background(), nil)
    r.NoError(err)
    defer tx.Rollback()
    err = repo.PrepareAlbumDeleteTx(context.Background(), tx, albumID)
    r.ErrorIs(err, share.ErrAlbumHasLiveScopes)
}

func TestRepoPrepareAlbumDeleteTxPurgesOnlyRevokedRemote(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    owner := owners.Principal{Hub: "h", UserID: "o"}
    seedOwner(t, d.WriteDB(), owner, "sk")
    albumID := seedAlbum(t, d.WriteDB(), owner)
    repo := share.NewRepo(d.WriteDB(), d.ReadDB())

    insertStatus := func(status share.BrokerStatus, revokedAt *time.Time, brokerGrantedAt *time.Time) string {
        s := share.Scope{
            UUID: uuid.NewString(), Owner: owner,
            Grantee:       owners.Principal{Hub: "h", UserID: "g"},
            TargetType:    share.TargetAlbumLive,
            TargetAlbumID: &albumID,
            CreatedAt:     time.Now().UTC().Truncate(time.Second),
            BrokerStatus:  share.StatusPending,
        }
        r.NoError(repo.Insert(context.Background(), s, nil))
        _, err := d.WriteDB().ExecContext(context.Background(),
            `UPDATE scopes SET broker_status=?, revoked_at=?, broker_granted_at=? WHERE uuid=?`,
            string(status), nullablePtr(revokedAt), nullablePtr(brokerGrantedAt), s.UUID)
        r.NoError(err)
        return s.UUID
    }
    now := time.Now().UTC()
    remote1 := insertStatus(share.StatusRevokedRemote, &now, &now)
    remote2 := insertStatus(share.StatusRevokedRemote, &now, &now)

    tx, err := d.WriteDB().BeginTx(context.Background(), nil)
    r.NoError(err)
    defer tx.Rollback()
    err = repo.PrepareAlbumDeleteTx(context.Background(), tx, albumID)
    r.NoError(err)
    r.NoError(tx.Commit())

    // Both revoked_remote rows dropped.
    _, err = repo.GetByUUID(context.Background(), remote1)
    r.ErrorIs(err, errs.ErrNotFound)
    _, err = repo.GetByUUID(context.Background(), remote2)
    r.ErrorIs(err, errs.ErrNotFound)
}

func TestRepoPrepareAlbumDeleteTxMixedPurgeAndBlock(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    owner := owners.Principal{Hub: "h", UserID: "o"}
    seedOwner(t, d.WriteDB(), owner, "sk")
    albumID := seedAlbum(t, d.WriteDB(), owner)
    repo := share.NewRepo(d.WriteDB(), d.ReadDB())

    insertStatus := func(status share.BrokerStatus, revokedAt *time.Time) string {
        s := share.Scope{
            UUID: uuid.NewString(), Owner: owner,
            Grantee:       owners.Principal{Hub: "h", UserID: "g"},
            TargetType:    share.TargetAlbumLive,
            TargetAlbumID: &albumID,
            CreatedAt:     time.Now().UTC().Truncate(time.Second),
            BrokerStatus:  share.StatusPending,
        }
        r.NoError(repo.Insert(context.Background(), s, nil))
        _, err := d.WriteDB().ExecContext(context.Background(),
            `UPDATE scopes SET broker_status=?, revoked_at=? WHERE uuid=?`,
            string(status), nullablePtr(revokedAt), s.UUID)
        r.NoError(err)
        return s.UUID
    }
    now := time.Now().UTC()
    remoteID  := insertStatus(share.StatusRevokedRemote, &now)
    pendingID := insertStatus(share.StatusPending, nil)

    // First pass: blocks; purge not applied (tx rolled back by caller).
    tx, err := d.WriteDB().BeginTx(context.Background(), nil)
    r.NoError(err)
    err = repo.PrepareAlbumDeleteTx(context.Background(), tx, albumID)
    r.ErrorIs(err, share.ErrAlbumHasLiveScopes)
    r.NoError(tx.Rollback())

    // After rollback, both rows still present.
    _, err = repo.GetByUUID(context.Background(), remoteID)
    r.NoError(err)
    _, err = repo.GetByUUID(context.Background(), pendingID)
    r.NoError(err)

    // Drive pending to revoked_remote, retry delete.
    _, err = d.WriteDB().ExecContext(context.Background(),
        `UPDATE scopes SET broker_status='revoked_remote', revoked_at=? WHERE uuid=?`,
        now, pendingID)
    r.NoError(err)

    tx, err = d.WriteDB().BeginTx(context.Background(), nil)
    r.NoError(err)
    r.NoError(repo.PrepareAlbumDeleteTx(context.Background(), tx, albumID))
    r.NoError(tx.Commit())

    _, err = repo.GetByUUID(context.Background(), remoteID)
    r.ErrorIs(err, errs.ErrNotFound)
    _, err = repo.GetByUUID(context.Background(), pendingID)
    r.ErrorIs(err, errs.ErrNotFound)
}

func TestRepoPrepareAlbumDeleteTxEmptyIsNoop(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    owner := owners.Principal{Hub: "h", UserID: "o"}
    seedOwner(t, d.WriteDB(), owner, "sk")
    albumID := seedAlbum(t, d.WriteDB(), owner)
    repo := share.NewRepo(d.WriteDB(), d.ReadDB())

    tx, err := d.WriteDB().BeginTx(context.Background(), nil)
    r.NoError(err)
    defer tx.Rollback()
    r.NoError(repo.PrepareAlbumDeleteTx(context.Background(), tx, albumID))
}

func TestRepoHasBlockingScopesForAlbum(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    owner := owners.Principal{Hub: "h", UserID: "o"}
    seedOwner(t, d.WriteDB(), owner, "sk")
    albumID := seedAlbum(t, d.WriteDB(), owner)
    repo := share.NewRepo(d.WriteDB(), d.ReadDB())

    blocking, err := repo.HasBlockingScopesForAlbum(context.Background(), albumID)
    r.NoError(err)
    r.False(blocking)

    // Add a revoked_remote: still not blocking.
    s := share.Scope{
        UUID: uuid.NewString(), Owner: owner,
        Grantee:       owners.Principal{Hub: "h", UserID: "g"},
        TargetType:    share.TargetAlbumLive,
        TargetAlbumID: &albumID,
        CreatedAt:     time.Now().UTC().Truncate(time.Second),
        BrokerStatus:  share.StatusPending,
    }
    r.NoError(repo.Insert(context.Background(), s, nil))
    now := time.Now().UTC()
    _, err = d.WriteDB().ExecContext(context.Background(),
        `UPDATE scopes SET broker_status='revoked_remote', revoked_at=? WHERE uuid=?`,
        now, s.UUID)
    r.NoError(err)
    blocking, err = repo.HasBlockingScopesForAlbum(context.Background(), albumID)
    r.NoError(err)
    r.False(blocking)

    // Add a pending: now blocking.
    s2 := share.Scope{
        UUID: uuid.NewString(), Owner: owner,
        Grantee:       owners.Principal{Hub: "h", UserID: "g2"},
        TargetType:    share.TargetAlbumLive,
        TargetAlbumID: &albumID,
        CreatedAt:     time.Now().UTC().Truncate(time.Second),
        BrokerStatus:  share.StatusPending,
    }
    r.NoError(repo.Insert(context.Background(), s2, nil))
    blocking, err = repo.HasBlockingScopesForAlbum(context.Background(), albumID)
    r.NoError(err)
    r.True(blocking)
}

// nullablePtr is the write-side equivalent of nullableTime used above.
func nullablePtr(t *time.Time) any {
    if t == nil {
        return nil
    }
    return *t
}
```

- [ ] **Step 2: Run the failing tests**

Run: `go test ./internal/share/... -run 'PrepareAlbumDelete|HasBlocking' -v`
Expected: FAIL — methods don't exist.

- [ ] **Step 3: Implement the methods**

Append to `internal/share/repo.go`:

```go
// PrepareAlbumDeleteTx runs inside an album-delete transaction (opened
// by AlbumService on the rw pool). It purges safely-terminal scopes
// linked to the album (broker_status = 'revoked_remote' only, because
// a worker crash between PublishScope success and MarkPublished means
// no other status can be proved safe) and returns
// ErrAlbumHasLiveScopes if any row remains that is not
// revoked_remote. See the spec's §8.3 for the crash-window rationale.
// Caller commits the tx (purging on success) or rolls it back (no
// changes on block).
func (r *Repo) PrepareAlbumDeleteTx(ctx context.Context, tx *sql.Tx, albumID string) error {
    if _, err := tx.ExecContext(ctx,
        `DELETE FROM scopes
          WHERE target_album_id = ? AND broker_status = 'revoked_remote'`,
        albumID); err != nil {
        return fmt.Errorf("purge revoked_remote scopes: %w", err)
    }
    row := tx.QueryRowContext(ctx,
        `SELECT 1 FROM scopes
          WHERE target_album_id = ? AND broker_status != 'revoked_remote'
          LIMIT 1`, albumID)
    var one int
    switch err := row.Scan(&one); {
    case errors.Is(err, sql.ErrNoRows):
        return nil
    case err != nil:
        return fmt.Errorf("check blocking scopes: %w", err)
    default:
        return ErrAlbumHasLiveScopes
    }
}

// HasBlockingScopesForAlbum is the non-tx diagnostic helper. Reads
// through the ro pool; small races against a concurrent state
// transition are acceptable because this is a preview, not the
// authoritative album-delete decision.
func (r *Repo) HasBlockingScopesForAlbum(ctx context.Context, albumID string) (bool, error) {
    row := r.ro.QueryRowContext(ctx,
        `SELECT 1 FROM scopes
          WHERE target_album_id = ? AND broker_status != 'revoked_remote'
          LIMIT 1`, albumID)
    var one int
    switch err := row.Scan(&one); {
    case errors.Is(err, sql.ErrNoRows):
        return false, nil
    case err != nil:
        return false, fmt.Errorf("has blocking scopes: %w", err)
    default:
        return true, nil
    }
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/share/... -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/share/repo.go internal/share/repo_test.go
git commit -m "Add share.Repo PrepareAlbumDeleteTx and HasBlockingScopesForAlbum"
```

---

## Task 10: `album.Repo.DeleteTx` (+ refactor `Delete`)

**Files:**
- Modify: `internal/album/repo.go`
- Modify: `internal/album/repo_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/album/repo_test.go`:

```go
func TestRepoDeleteTxCommitRemovesAlbum(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    repo := album.NewRepo(d.WriteDB(), d.ReadDB())
    owner := owners.Principal{Hub: "h", UserID: "o"}
    seedOwnerForAlbum(t, d.WriteDB(), owner)
    a := album.Album{
        ID: uuid.NewString(), Owner: owner,
        Name: "t", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
    }
    r.NoError(repo.Insert(context.Background(), a))

    tx, err := d.WriteDB().BeginTx(context.Background(), nil)
    r.NoError(err)
    r.NoError(repo.DeleteTx(context.Background(), tx, a.ID))
    r.NoError(tx.Commit())

    _, err = repo.GetByID(context.Background(), a.ID)
    r.ErrorIs(err, errs.ErrNotFound)
}

func TestRepoDeleteTxRollbackLeavesAlbum(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    repo := album.NewRepo(d.WriteDB(), d.ReadDB())
    owner := owners.Principal{Hub: "h", UserID: "o"}
    seedOwnerForAlbum(t, d.WriteDB(), owner)
    a := album.Album{
        ID: uuid.NewString(), Owner: owner,
        Name: "t", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
    }
    r.NoError(repo.Insert(context.Background(), a))

    tx, err := d.WriteDB().BeginTx(context.Background(), nil)
    r.NoError(err)
    r.NoError(repo.DeleteTx(context.Background(), tx, a.ID))
    r.NoError(tx.Rollback())

    got, err := repo.GetByID(context.Background(), a.ID)
    r.NoError(err)
    r.Equal(a.ID, got.ID)
}

func TestRepoDeleteTxNotFound(t *testing.T) {
    r := require.New(t)
    d := testutil.OpenTestDB(t)
    repo := album.NewRepo(d.WriteDB(), d.ReadDB())
    tx, err := d.WriteDB().BeginTx(context.Background(), nil)
    r.NoError(err)
    defer tx.Rollback()
    err = repo.DeleteTx(context.Background(), tx, uuid.NewString())
    r.ErrorIs(err, errs.ErrNotFound)
}

// seedOwnerForAlbum inserts a minimal owners row if not present.
func seedOwnerForAlbum(t *testing.T, rw *sql.DB, p owners.Principal) {
    t.Helper()
    _, err := rw.ExecContext(context.Background(),
        `INSERT OR IGNORE INTO owners(hub, user_id, storage_key, created_at)
         VALUES(?,?,?,?)`, p.Hub, p.UserID, "sk", time.Now().UTC())
    require.NoError(t, err)
}
```

Ensure imports at the top of `internal/album/repo_test.go` include `database/sql`, `"github.com/google/uuid"`, `"github.com/wesm/fotobank/internal/errs"`, `"github.com/wesm/fotobank/internal/testutil"`, `"github.com/wesm/fotobank/internal/owners"` — add any missing ones (goimports will place them).

- [ ] **Step 2: Run the failing test**

Run: `go test ./internal/album/... -run DeleteTx -v`
Expected: FAIL — `DeleteTx` does not exist.

- [ ] **Step 3: Implement `DeleteTx` and refactor `Delete`**

Replace the existing `Delete` method in `internal/album/repo.go` with the
following pair:

```go
// Delete removes the album. album_media is cascaded by the FK
// ON DELETE CASCADE in the schema. ErrNotFound if missing.
//
// This is a convenience wrapper that opens a one-shot transaction on
// the rw pool and calls DeleteTx. Callers that need to combine the
// delete with other writes (e.g. share-purge) use DeleteTx directly
// inside their own tx.
func (r *Repo) Delete(ctx context.Context, id string) error {
    tx, err := r.rw.BeginTx(ctx, nil)
    if err != nil {
        return fmt.Errorf("begin: %w", err)
    }
    if err := r.DeleteTx(ctx, tx, id); err != nil {
        _ = tx.Rollback()
        return err
    }
    return tx.Commit()
}

// DeleteTx executes the album delete within an existing transaction.
// album_media rows cascade via the schema FK. Returns errs.ErrNotFound
// if the album does not exist. Plan-E1 AlbumService.Delete wraps this
// with share-purge/block under one db.Tx so the two writes commit
// atomically.
func (r *Repo) DeleteTx(ctx context.Context, tx *sql.Tx, id string) error {
    res, err := tx.ExecContext(ctx, `DELETE FROM albums WHERE id = ?`, id)
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
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/album/... -v`
Expected: PASS on all existing + new tests.

- [ ] **Step 5: Commit**

```bash
git add internal/album/repo.go internal/album/repo_test.go
git commit -m "Add album.Repo.DeleteTx; refactor Delete to delegate"
```

---

## Task 11: Backoff pure function

**Files:**
- Create: `internal/shareworker/backoff.go`
- Create: `internal/shareworker/backoff_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/shareworker/backoff_test.go`:

```go
package shareworker_test

import (
    "math/rand"
    "testing"
    "time"

    "github.com/stretchr/testify/require"

    "github.com/wesm/fotobank/internal/shareworker"
)

// zeroJitterRng is a *rand.Rand whose Float64 always returns 0.5 so the
// jitter term cancels to zero (jitter = raw * pct * (2*0.5 - 1) == 0).
func zeroJitterRng() *rand.Rand {
    return rand.New(halfSource{})
}

type halfSource struct{}

func (halfSource) Int63() int64 { return 1 << 62 }    // maps Float64 to 0.5
func (halfSource) Seed(int64)   {}

func TestBackoffTableWithoutJitter(t *testing.T) {
    r := require.New(t)
    rng := zeroJitterRng()
    cases := []struct {
        attempt int
        want    time.Duration
    }{
        {1, 30 * time.Second},
        {2, 60 * time.Second},
        {3, 120 * time.Second},
        {4, 240 * time.Second},
        {5, 480 * time.Second},
        {6, 960 * time.Second},
        {7, 1920 * time.Second},
        {8, 1 * time.Hour},
        {9, 1 * time.Hour},
        {10, 1 * time.Hour},
    }
    for _, tc := range cases {
        got := shareworker.Backoff(tc.attempt, rng)
        r.Equalf(tc.want, got, "attempt=%d", tc.attempt)
    }
}

func TestBackoffClampsAttemptsBelowOne(t *testing.T) {
    r := require.New(t)
    rng := zeroJitterRng()
    r.Equal(30*time.Second, shareworker.Backoff(0, rng))
    r.Equal(30*time.Second, shareworker.Backoff(-5, rng))
}

func TestBackoffJitterBandedAroundRaw(t *testing.T) {
    r := require.New(t)
    rng := rand.New(rand.NewSource(42))
    for i := 0; i < 100; i++ {
        got := shareworker.Backoff(3, rng) // raw = 120s
        r.GreaterOrEqual(got, 108*time.Second) // 120s - 10% = 108s
        r.LessOrEqual(got, 132*time.Second)    // 120s + 10% = 132s
    }
}
```

- [ ] **Step 2: Run the failing test**

Run: `go test ./internal/shareworker/... -v`
Expected: FAIL — package doesn't exist.

- [ ] **Step 3: Implement Backoff**

Create `internal/shareworker/backoff.go`:

```go
// Package shareworker is the outbox worker that drives scopes through
// their broker-side state machine.
package shareworker

import (
    "math"
    "math/rand"
    "time"
)

const (
    baseDelay = 30 * time.Second
    maxDelay  = 1 * time.Hour
    jitterPct = 10
)

// Backoff returns the delay AFTER attempt n has failed, before attempt
// n+1. The worker calls Backoff(scope.BrokerAttempts + 1) on a
// transient failure. Attempt 1 post-fail waits baseDelay (30s),
// attempt 2 waits 60s, and so on; beyond ~attempt 8 the delay is
// capped at maxDelay (1h). Jitter is ± jitterPct % of the raw
// delay; rng is injected so tests can pin it.
//
// No-jitter delays in seconds (n=1..9):
//   30, 60, 120, 240, 480, 960, 1920, 3600, 3600.
// Sum of n=1..9 (the nine gaps before the 10th and terminal attempt)
// is ~11010 s ≈ 3h03m.
func Backoff(attempt int, rng *rand.Rand) time.Duration {
    if attempt < 1 {
        attempt = 1
    }
    raw := float64(baseDelay) * math.Pow(2, float64(attempt-1))
    if raw > float64(maxDelay) {
        raw = float64(maxDelay)
    }
    jitter := raw * (float64(jitterPct) / 100) * (2*rng.Float64() - 1)
    return time.Duration(raw + jitter)
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/shareworker/... -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/shareworker/backoff.go internal/shareworker/backoff_test.go
git commit -m "Add shareworker.Backoff exponential-backoff-with-jitter"
```

---

## Task 12: `shareworker.Worker` (RunOnce + Run)

**Files:**
- Create: `internal/shareworker/worker.go`
- Create: `internal/shareworker/worker_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/shareworker/worker_test.go`:

```go
package shareworker_test

import (
    "context"
    "database/sql"
    "math/rand"
    "testing"
    "time"

    "github.com/google/uuid"
    "github.com/stretchr/testify/require"

    "github.com/wesm/fotobank/internal/broker"
    "github.com/wesm/fotobank/internal/broker/brokertest"
    "github.com/wesm/fotobank/internal/owners"
    "github.com/wesm/fotobank/internal/share"
    "github.com/wesm/fotobank/internal/shareworker"
    "github.com/wesm/fotobank/internal/testutil"
)

type workerFixture struct {
    repo  *share.Repo
    fake  *brokertest.Fake
    w     *shareworker.Worker
    owner owners.Principal
    db    *sql.DB
    now   time.Time
}

func newWorkerFixture(t *testing.T) *workerFixture {
    t.Helper()
    d := testutil.OpenTestDB(t)
    owner := owners.Principal{Hub: "h", UserID: "o"}
    _, err := d.WriteDB().ExecContext(context.Background(),
        `INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
        owner.Hub, owner.UserID, "sk", time.Now().UTC())
    require.NoError(t, err)
    repo := share.NewRepo(d.WriteDB(), d.ReadDB())
    fake := &brokertest.Fake{}
    now := time.Date(2026, 4, 23, 10, 0, 0, 0, time.UTC)
    w := shareworker.New(shareworker.Config{
        Repo:   repo,
        Broker: fake,
        Now:    func() time.Time { return now },
        Rand:   rand.New(rand.NewSource(1)),
    })
    return &workerFixture{repo: repo, fake: fake, w: w, owner: owner, db: d.WriteDB(), now: now}
}

func (fx *workerFixture) insertPending(t *testing.T) string {
    t.Helper()
    id := uuid.NewString()
    _, err := fx.db.ExecContext(context.Background(),
        `INSERT INTO albums(id, owner_hub, owner_user_id, name, created_at, updated_at)
         VALUES(?,?,?,?,?,?)`,
        id, fx.owner.Hub, fx.owner.UserID, "t", fx.now, fx.now)
    require.NoError(t, err)
    s := share.Scope{
        UUID: uuid.NewString(), Owner: fx.owner,
        Grantee:       owners.Principal{Hub: "h", UserID: "g"},
        TargetType:    share.TargetAlbumLive,
        TargetAlbumID: &id,
        CreatedAt:     fx.now,
        BrokerStatus:  share.StatusPending,
    }
    require.NoError(t, fx.repo.Insert(context.Background(), s, nil))
    return s.UUID
}

func TestWorkerPendingToActiveOnSuccess(t *testing.T) {
    r := require.New(t)
    fx := newWorkerFixture(t)
    id := fx.insertPending(t)

    processed, err := fx.w.RunOnce(context.Background())
    r.NoError(err)
    r.Equal(1, processed)

    got, err := fx.repo.GetByUUID(context.Background(), id)
    r.NoError(err)
    r.Equal(share.StatusActive, got.BrokerStatus)
    r.NotNil(got.BrokerGrantedAt)
    r.Equal([]string{id}, fx.fake.ObservedPublishes())
}

func TestWorkerPendingTransientFailSchedulesRetry(t *testing.T) {
    r := require.New(t)
    fx := newWorkerFixture(t)
    id := fx.insertPending(t)
    fx.fake.QueuePublishError(id, broker.ErrBrokerTransient)

    processed, err := fx.w.RunOnce(context.Background())
    r.NoError(err)
    r.Equal(1, processed)

    got, err := fx.repo.GetByUUID(context.Background(), id)
    r.NoError(err)
    r.Equal(share.StatusPending, got.BrokerStatus)
    r.Equal(1, got.BrokerAttempts)
    r.NotEmpty(got.BrokerLastError)
    r.NotNil(got.BrokerNextAttemptAt)
    r.True(got.BrokerNextAttemptAt.After(fx.now))
}

func TestWorkerPendingPermanentFailMarksFailed(t *testing.T) {
    r := require.New(t)
    fx := newWorkerFixture(t)
    id := fx.insertPending(t)
    fx.fake.QueuePublishError(id, broker.ErrBrokerPermanent)

    _, err := fx.w.RunOnce(context.Background())
    r.NoError(err)

    got, err := fx.repo.GetByUUID(context.Background(), id)
    r.NoError(err)
    r.Equal(share.StatusFailed, got.BrokerStatus)
    r.Nil(got.RevokedAt)
    r.Equal(1, got.BrokerAttempts)
}

func TestWorkerPendingTransientXMaxAttemptsMarksFailed(t *testing.T) {
    r := require.New(t)
    fx := newWorkerFixture(t)
    id := fx.insertPending(t)

    // Simulate 9 prior failed attempts.
    _, err := fx.db.ExecContext(context.Background(),
        `UPDATE scopes SET broker_attempts = ?, broker_next_attempt_at = NULL WHERE uuid = ?`,
        share.MaxBrokerAttempts-1, id)
    r.NoError(err)
    fx.fake.QueuePublishError(id, broker.ErrBrokerTransient)

    _, err = fx.w.RunOnce(context.Background())
    r.NoError(err)

    got, err := fx.repo.GetByUUID(context.Background(), id)
    r.NoError(err)
    r.Equal(share.StatusFailed, got.BrokerStatus)
    r.Equal(share.MaxBrokerAttempts, got.BrokerAttempts)
}

func TestWorkerRevokingToRevokedRemoteOnSuccess(t *testing.T) {
    r := require.New(t)
    fx := newWorkerFixture(t)
    id := fx.insertPending(t)
    _, err := fx.db.ExecContext(context.Background(),
        `UPDATE scopes SET broker_status='revoking', revoked_at=? WHERE uuid=?`,
        fx.now, id)
    r.NoError(err)

    _, err = fx.w.RunOnce(context.Background())
    r.NoError(err)

    got, err := fx.repo.GetByUUID(context.Background(), id)
    r.NoError(err)
    r.Equal(share.StatusRevokedRemote, got.BrokerStatus)
    r.NotNil(got.BrokerRevokedAt)
}

func TestWorkerPublishSuccessThenRevokeRace(t *testing.T) {
    r := require.New(t)
    fx := newWorkerFixture(t)
    id := fx.insertPending(t)

    // Simulate: ListReady already read this row as pending, then
    // owner Revoke lands BEFORE the worker issues PublishScope.
    // We fast-forward by manually flipping the row to revoking.
    _, err := fx.db.ExecContext(context.Background(),
        `UPDATE scopes SET broker_status='revoking', revoked_at=? WHERE uuid=?`,
        fx.now, id)
    r.NoError(err)

    // The worker's internal read is fresh; simulate the race by
    // driving RunOnce twice with a publish-then-revoke Fake sequence.
    // In practice the race is asserted by directly calling
    // MarkPublished while status='revoking' — that path is covered by
    // repo tests. Here we simply drive the full outcome: the worker
    // issues RevokeScope (success, short-circuit for Noop) → revoked_remote.
    _, err = fx.w.RunOnce(context.Background())
    r.NoError(err)
    got, err := fx.repo.GetByUUID(context.Background(), id)
    r.NoError(err)
    r.Equal(share.StatusRevokedRemote, got.BrokerStatus)
}

func TestWorkerSkipsFutureNextAttempt(t *testing.T) {
    r := require.New(t)
    fx := newWorkerFixture(t)
    id := fx.insertPending(t)
    future := fx.now.Add(time.Hour)
    _, err := fx.db.ExecContext(context.Background(),
        `UPDATE scopes SET broker_next_attempt_at = ? WHERE uuid = ?`,
        future, id)
    r.NoError(err)

    processed, err := fx.w.RunOnce(context.Background())
    r.NoError(err)
    r.Equal(0, processed)
    r.Empty(fx.fake.ObservedPublishes())
}
```

- [ ] **Step 2: Run the failing tests**

Run: `go test ./internal/shareworker/... -v`
Expected: FAIL — `shareworker.New`, `shareworker.Worker`, `shareworker.Config` don't exist.

- [ ] **Step 3: Implement the worker**

Create `internal/shareworker/worker.go`:

```go
package shareworker

import (
    "context"
    "errors"
    "fmt"
    "log/slog"
    "math/rand"
    "time"

    "github.com/wesm/fotobank/internal/broker"
    "github.com/wesm/fotobank/internal/share"
)

// Config carries the worker's collaborators + tunables.
type Config struct {
    Repo   *share.Repo
    Broker broker.BrokerClient
    // Tick is how often Run triggers RunOnce. Defaults to 15s.
    Tick time.Duration
    // Batch is the max number of rows drained per tick. Defaults to 20.
    Batch int
    // Now returns the current time; overridable for tests.
    Now func() time.Time
    // Rand is the jitter source for Backoff. Non-nil in prod; tests
    // pin it to a deterministic seed.
    Rand *rand.Rand
    // Logger is optional; a no-op slog.Logger is used if nil.
    Logger *slog.Logger
}

// Worker drives the outbox. Call New to construct, then Run (long-
// running) or RunOnce (per-tick, for tests).
type Worker struct {
    cfg Config
}

// New constructs a Worker with defaults filled in.
func New(cfg Config) *Worker {
    if cfg.Tick <= 0 {
        cfg.Tick = 15 * time.Second
    }
    if cfg.Batch <= 0 {
        cfg.Batch = 20
    }
    if cfg.Now == nil {
        cfg.Now = func() time.Time { return time.Now().UTC() }
    }
    if cfg.Rand == nil {
        cfg.Rand = rand.New(rand.NewSource(time.Now().UnixNano()))
    }
    if cfg.Logger == nil {
        cfg.Logger = slog.New(slog.NewTextHandler(discardWriter{}, nil))
    }
    return &Worker{cfg: cfg}
}

// Run loops: on each tick, calls RunOnce. Returns when ctx is
// cancelled. A RunOnce error is logged and the loop continues.
func (w *Worker) Run(ctx context.Context) error {
    t := time.NewTicker(w.cfg.Tick)
    defer t.Stop()
    // Trigger the first drain immediately so the worker starts making
    // progress without waiting one full tick on startup.
    for {
        if _, err := w.RunOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
            w.cfg.Logger.Error("shareworker tick failed", "err", err)
        }
        select {
        case <-ctx.Done():
            return ctx.Err()
        case <-t.C:
        }
    }
}

// RunOnce drains up to Batch ready rows once and returns the number
// processed. Non-terminal errors from a single row are logged and do
// not stop the drain.
func (w *Worker) RunOnce(ctx context.Context) (int, error) {
    rows, err := w.cfg.Repo.ListReady(ctx, w.cfg.Now(), w.cfg.Batch)
    if err != nil {
        return 0, fmt.Errorf("list ready: %w", err)
    }
    n := 0
    for _, s := range rows {
        if ctx.Err() != nil {
            return n, ctx.Err()
        }
        switch s.BrokerStatus {
        case share.StatusPending:
            w.processPending(ctx, s)
        case share.StatusRevoking:
            w.processRevoking(ctx, s)
        default:
            w.cfg.Logger.Warn("shareworker unexpected status",
                "uuid", s.UUID, "status", s.BrokerStatus)
        }
        n++
    }
    return n, nil
}

func (w *Worker) processPending(ctx context.Context, s share.Scope) {
    err := w.cfg.Broker.PublishScope(ctx, s)
    if err == nil {
        if _, merr := w.cfg.Repo.MarkPublished(ctx, s.UUID, w.cfg.Now()); merr != nil {
            w.cfg.Logger.Error("mark published failed", "uuid", s.UUID, "err", merr)
        }
        return
    }
    if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
        return
    }
    w.recordFailure(ctx, s, share.StatusPending, err)
}

func (w *Worker) processRevoking(ctx context.Context, s share.Scope) {
    err := w.cfg.Broker.RevokeScope(ctx, s.UUID)
    if err == nil {
        if _, merr := w.cfg.Repo.MarkRevoked(ctx, s.UUID, w.cfg.Now()); merr != nil {
            w.cfg.Logger.Error("mark revoked failed", "uuid", s.UUID, "err", merr)
        }
        return
    }
    if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
        return
    }
    w.recordFailure(ctx, s, share.StatusRevoking, err)
}

func (w *Worker) recordFailure(ctx context.Context, s share.Scope, phase share.BrokerStatus, err error) {
    permanent := errors.Is(err, broker.ErrBrokerPermanent)
    exhausted := s.BrokerAttempts+1 >= share.MaxBrokerAttempts
    if permanent || exhausted {
        if _, merr := w.cfg.Repo.MarkFailed(ctx, s.UUID, phase, err.Error()); merr != nil {
            w.cfg.Logger.Error("mark failed failed", "uuid", s.UUID, "err", merr)
        }
        return
    }
    nextAt := w.cfg.Now().Add(Backoff(s.BrokerAttempts+1, w.cfg.Rand))
    if _, merr := w.cfg.Repo.MarkAttemptFailed(ctx, s.UUID, phase, err.Error(), nextAt); merr != nil {
        w.cfg.Logger.Error("mark attempt-failed failed", "uuid", s.UUID, "err", merr)
    }
}

// discardWriter satisfies io.Writer for a no-op slog handler.
type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/shareworker/... -v`
Expected: PASS on all cases.

- [ ] **Step 5: Commit**

```bash
git add internal/shareworker/worker.go internal/shareworker/worker_test.go
git commit -m "Add shareworker.Worker with RunOnce + Run tick loop"
```

---

## Task 13: `ShareService.Create` with full validation

**Files:**
- Create: `internal/service/share_service.go`
- Create: `internal/service/share_service_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/service/share_service_test.go`:

```go
package service_test

import (
    "context"
    "database/sql"
    "testing"
    "time"

    "github.com/google/uuid"
    "github.com/stretchr/testify/require"

    "github.com/wesm/fotobank/internal/album"
    "github.com/wesm/fotobank/internal/errs"
    "github.com/wesm/fotobank/internal/media"
    "github.com/wesm/fotobank/internal/owners"
    "github.com/wesm/fotobank/internal/service"
    "github.com/wesm/fotobank/internal/share"
    "github.com/wesm/fotobank/internal/testutil"
)

type shareFixture struct {
    svc    *service.ShareService
    shares *share.Repo
    albums *album.Repo
    media  *media.Repo
    owner  owners.Principal
    rw     *sql.DB
}

func newShareFixture(t *testing.T) *shareFixture {
    t.Helper()
    d := testutil.OpenTestDB(t)
    owner := owners.Principal{Hub: "h", UserID: "o"}
    _, err := d.WriteDB().ExecContext(context.Background(),
        `INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
        owner.Hub, owner.UserID, "sk", time.Now().UTC())
    require.NoError(t, err)

    shares := share.NewRepo(d.WriteDB(), d.ReadDB())
    albums := album.NewRepo(d.WriteDB(), d.ReadDB())
    mediaRepo := media.NewRepo(d.WriteDB(), d.ReadDB())
    svc := service.NewShareService(shares, albums, mediaRepo)
    return &shareFixture{
        svc: svc, shares: shares, albums: albums, media: mediaRepo,
        owner: owner, rw: d.WriteDB(),
    }
}

func (fx *shareFixture) seedAlbum(t *testing.T, items int) string {
    t.Helper()
    svcAlbums := service.NewAlbumServiceForTest(fx.albums, fx.media, nil, nil)
    a, err := svcAlbums.Create(context.Background(), fx.owner, "Trip")
    require.NoError(t, err)
    for i := 0; i < items; i++ {
        m := media.Media{
            ID: uuid.NewString(), Owner: fx.owner, Type: media.TypePhoto,
            MimeType: "image/jpeg", Path: "2024/x" + uuid.NewString() + ".jpg",
            OriginalFilename: "x.jpg", ImportedAt: time.Now().UTC().Truncate(time.Second),
            Size: 100, Checksum: "cs" + uuid.NewString(), ThumbStatus: "pending",
        }
        require.NoError(t, fx.media.Insert(context.Background(), m))
        _, err := svcAlbums.AddMedia(context.Background(), a.ID, []string{m.ID}, fx.owner)
        require.NoError(t, err)
    }
    return a.ID
}

func (fx *shareFixture) seedMediaRow(t *testing.T) string {
    t.Helper()
    m := media.Media{
        ID: uuid.NewString(), Owner: fx.owner, Type: media.TypePhoto,
        MimeType: "image/jpeg", Path: "2024/" + uuid.NewString() + ".jpg",
        OriginalFilename: "x.jpg", ImportedAt: time.Now().UTC().Truncate(time.Second),
        Size: 100, Checksum: "cs" + uuid.NewString(), ThumbStatus: "pending",
    }
    require.NoError(t, fx.media.Insert(context.Background(), m))
    return m.ID
}

func TestShareCreateAlbumLiveHappyPath(t *testing.T) {
    r := require.New(t)
    fx := newShareFixture(t)
    albumID := fx.seedAlbum(t, 2)

    got, err := fx.svc.Create(context.Background(), service.CreateShareRequest{
        Label:      "Summer",
        Grantee:    owners.Principal{Hub: "h", UserID: "alice"},
        TargetType: share.TargetAlbumLive,
        AlbumID:    albumID,
    }, fx.owner)
    r.NoError(err)
    r.NotEmpty(got.UUID)
    r.Equal(share.StatusPending, got.BrokerStatus)
    r.NotNil(got.TargetAlbumID)
    r.Equal(albumID, *got.TargetAlbumID)
}

func TestShareCreateMediaSetHappyPath(t *testing.T) {
    r := require.New(t)
    fx := newShareFixture(t)
    m1 := fx.seedMediaRow(t)
    m2 := fx.seedMediaRow(t)

    got, err := fx.svc.Create(context.Background(), service.CreateShareRequest{
        Grantee:    owners.Principal{Hub: "h", UserID: "alice"},
        TargetType: share.TargetMediaSet,
        MediaIDs:   []string{m1, m2, m1}, // duplicate deduped
    }, fx.owner)
    r.NoError(err)

    // ShareService.Get lands in Task 14 — read membership via the repo here.
    det, err := fx.shares.GetByUUID(context.Background(), got.UUID)
    r.NoError(err)
    r.ElementsMatch([]string{m1, m2}, det.MediaIDs)
}

func TestShareCreateRejectsEmptyAlbum(t *testing.T) {
    r := require.New(t)
    fx := newShareFixture(t)
    albumID := fx.seedAlbum(t, 0)

    _, err := fx.svc.Create(context.Background(), service.CreateShareRequest{
        Grantee:    owners.Principal{Hub: "h", UserID: "a"},
        TargetType: share.TargetAlbumLive,
        AlbumID:    albumID,
    }, fx.owner)
    r.ErrorIs(err, share.ErrAlbumEmpty)
}

func TestShareCreateRejectsCrossOwnerAlbum(t *testing.T) {
    r := require.New(t)
    fx := newShareFixture(t)
    otherOwner := owners.Principal{Hub: "h", UserID: "other"}
    _, err := fx.rw.ExecContext(context.Background(),
        `INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
        otherOwner.Hub, otherOwner.UserID, "sk2", time.Now().UTC())
    r.NoError(err)
    fxOther := &shareFixture{
        albums: fx.albums, media: fx.media, owner: otherOwner, rw: fx.rw,
    }
    _ = fxOther
    otherAlbumID := uuid.NewString()
    now := time.Now().UTC()
    _, err = fx.rw.ExecContext(context.Background(),
        `INSERT INTO albums(id, owner_hub, owner_user_id, name, created_at, updated_at)
         VALUES(?,?,?,?,?,?)`,
        otherAlbumID, otherOwner.Hub, otherOwner.UserID, "t", now, now)
    r.NoError(err)

    _, err = fx.svc.Create(context.Background(), service.CreateShareRequest{
        Grantee:    owners.Principal{Hub: "h", UserID: "a"},
        TargetType: share.TargetAlbumLive,
        AlbumID:    otherAlbumID,
    }, fx.owner)
    r.ErrorIs(err, errs.ErrNotFound)
}

func TestShareCreateRejectsCrossOwnerMediaSet(t *testing.T) {
    r := require.New(t)
    fx := newShareFixture(t)
    m := fx.seedMediaRow(t)
    otherOwner := owners.Principal{Hub: "h", UserID: "other"}
    _, err := fx.rw.ExecContext(context.Background(),
        `INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
        otherOwner.Hub, otherOwner.UserID, "sk2", time.Now().UTC())
    r.NoError(err)
    otherM := media.Media{
        ID: uuid.NewString(), Owner: otherOwner, Type: media.TypePhoto,
        MimeType: "image/jpeg", Path: "2024/other.jpg",
        OriginalFilename: "x.jpg", ImportedAt: time.Now().UTC().Truncate(time.Second),
        Size: 100, Checksum: "cso", ThumbStatus: "pending",
    }
    r.NoError(fx.media.Insert(context.Background(), otherM))

    _, err = fx.svc.Create(context.Background(), service.CreateShareRequest{
        Grantee:    owners.Principal{Hub: "h", UserID: "a"},
        TargetType: share.TargetMediaSet,
        MediaIDs:   []string{m, otherM.ID},
    }, fx.owner)
    r.ErrorIs(err, errs.ErrNotFound)
}

func TestShareCreateValidatesGrantee(t *testing.T) {
    fx := newShareFixture(t)
    albumID := fx.seedAlbum(t, 1)
    cases := []struct {
        name    string
        grantee owners.Principal
    }{
        {"zero", owners.Principal{}},
        {"empty hub", owners.Principal{UserID: "a"}},
        {"empty user", owners.Principal{Hub: "h"}},
        {"caller self", fx.owner},
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            r := require.New(t)
            _, err := fx.svc.Create(context.Background(), service.CreateShareRequest{
                Grantee:    tc.grantee,
                TargetType: share.TargetAlbumLive,
                AlbumID:    albumID,
            }, fx.owner)
            r.ErrorIs(err, share.ErrInvalidGrantee)
        })
    }
}

func TestShareCreateValidatesTargetCombo(t *testing.T) {
    r := require.New(t)
    fx := newShareFixture(t)
    albumID := fx.seedAlbum(t, 1)

    _, err := fx.svc.Create(context.Background(), service.CreateShareRequest{
        Grantee:    owners.Principal{Hub: "h", UserID: "a"},
        TargetType: share.TargetAlbumLive,
        AlbumID:    albumID,
        MediaIDs:   []string{"m1"}, // illegal combo
    }, fx.owner)
    r.ErrorIs(err, share.ErrInvalidTargetCombo)

    _, err = fx.svc.Create(context.Background(), service.CreateShareRequest{
        Grantee:    owners.Principal{Hub: "h", UserID: "a"},
        TargetType: share.TargetMediaSet,
        AlbumID:    albumID, // illegal combo
        MediaIDs:   []string{"m1"},
    }, fx.owner)
    r.ErrorIs(err, share.ErrInvalidTargetCombo)
}

func TestShareCreateValidatesMediaSetSize(t *testing.T) {
    r := require.New(t)
    fx := newShareFixture(t)

    _, err := fx.svc.Create(context.Background(), service.CreateShareRequest{
        Grantee:    owners.Principal{Hub: "h", UserID: "a"},
        TargetType: share.TargetMediaSet,
        MediaIDs:   nil, // empty after dedupe
    }, fx.owner)
    r.ErrorIs(err, share.ErrInvalidMediaSet)
}
```

Note the helper `service.NewAlbumServiceForTest` — this is a shim used only
to seed fixtures; Task 15 adds it alongside the constructor change and the
test above will compile at that point. During Task 13 alone, the plan
*creates `ShareService`*; the AlbumService shim lives in Task 15. In the
interim, the tests that call `NewAlbumServiceForTest` will fail to
compile unless it already exists. To keep Task 13 self-contained, replace
the seeding helper with a direct SQL-level album insert like the one in
`share/repo_test.go`. Use the following replacement for
`(fx *shareFixture) seedAlbum`:

```go
func (fx *shareFixture) seedAlbum(t *testing.T, items int) string {
    t.Helper()
    id := uuid.NewString()
    now := time.Now().UTC()
    _, err := fx.rw.ExecContext(context.Background(),
        `INSERT INTO albums(id, owner_hub, owner_user_id, name, created_at, updated_at)
         VALUES(?,?,?,?,?,?)`,
        id, fx.owner.Hub, fx.owner.UserID, "Trip", now, now)
    require.NoError(t, err)
    for i := 0; i < items; i++ {
        mid := fx.seedMediaRow(t)
        _, err := fx.rw.ExecContext(context.Background(),
            `INSERT INTO album_media(album_id, media_id, added_at) VALUES(?,?,?)`,
            id, mid, now)
        require.NoError(t, err)
    }
    return id
}
```

Use this version. Delete the `NewAlbumServiceForTest` helper — Task 13
does not use it.

- [ ] **Step 2: Run the failing tests**

Run: `go test ./internal/service/... -run TestShareCreate -v`
Expected: FAIL — `service.ShareService`, `service.CreateShareRequest`, `service.NewShareService` don't exist.

- [ ] **Step 3: Implement `ShareService.Create`**

Create `internal/service/share_service.go`:

```go
package service

import (
    "context"
    "fmt"
    "time"

    "github.com/google/uuid"

    "github.com/wesm/fotobank/internal/album"
    "github.com/wesm/fotobank/internal/errs"
    "github.com/wesm/fotobank/internal/media"
    "github.com/wesm/fotobank/internal/owners"
    "github.com/wesm/fotobank/internal/share"
)

// ShareService is the auth-scoped entry point for scope mutations.
// Every exported method takes caller owners.Principal. Cross-owner
// access returns errs.ErrNotFound (never ErrOwnerMismatch), so the
// surface cannot be probed for other-owner scope UUIDs.
type ShareService struct {
    shares *share.Repo
    albums *album.Repo
    media  *media.Repo
    now    func() time.Time
    uuid   func() string
}

// NewShareService constructs a ShareService with production defaults.
func NewShareService(s *share.Repo, a *album.Repo, m *media.Repo) *ShareService {
    return &ShareService{
        shares: s, albums: a, media: m,
        now:  func() time.Time { return time.Now().UTC() },
        uuid: uuid.NewString,
    }
}

// CreateShareRequest is the service-level shape for Create.
type CreateShareRequest struct {
    Label         string
    Grantee       owners.Principal
    AllowDownload bool
    ExpiresAt     *time.Time
    TargetType    share.TargetType
    AlbumID       string
    MediaIDs      []string
}

// Create validates the request, pre-flights ownership on the target,
// and writes the scope row (+ scope_media for media_set) in one
// transaction. Returns the scope in its freshly-inserted StatusPending
// state.
func (s *ShareService) Create(ctx context.Context, req CreateShareRequest, caller owners.Principal) (share.Scope, error) {
    if len(req.Label) > share.LabelMaxLen {
        return share.Scope{}, share.ErrInvalidLabel
    }
    if !granteeValid(req.Grantee, caller) {
        return share.Scope{}, share.ErrInvalidGrantee
    }
    switch req.TargetType {
    case share.TargetAlbumLive:
        if req.AlbumID == "" || len(req.MediaIDs) > 0 {
            return share.Scope{}, share.ErrInvalidTargetCombo
        }
    case share.TargetMediaSet:
        if req.AlbumID != "" {
            return share.Scope{}, share.ErrInvalidTargetCombo
        }
    default:
        return share.Scope{}, share.ErrInvalidTargetCombo
    }

    scope := share.Scope{
        UUID:          s.uuid(),
        Owner:         caller,
        Grantee:       req.Grantee,
        TargetType:    req.TargetType,
        AllowDownload: req.AllowDownload,
        Label:         req.Label,
        ExpiresAt:     req.ExpiresAt,
        CreatedAt:     s.now(),
        BrokerStatus:  share.StatusPending,
    }

    var mediaIDs []string
    switch req.TargetType {
    case share.TargetAlbumLive:
        detail, err := s.albums.GetDetailByID(ctx, req.AlbumID)
        if err != nil {
            return share.Scope{}, err
        }
        if detail.Owner != caller {
            return share.Scope{}, fmt.Errorf("%w: album id=%s", errs.ErrNotFound, req.AlbumID)
        }
        if detail.ItemCount == 0 {
            return share.Scope{}, share.ErrAlbumEmpty
        }
        id := detail.ID
        scope.TargetAlbumID = &id

    case share.TargetMediaSet:
        deduped, err := dedupeMediaIDs(req.MediaIDs)
        if err != nil {
            return share.Scope{}, err
        }
        for _, mid := range deduped {
            m, err := s.media.GetByID(ctx, mid)
            if err != nil {
                return share.Scope{}, err
            }
            if m.Owner != caller {
                return share.Scope{}, fmt.Errorf("%w: media id=%s", errs.ErrNotFound, mid)
            }
        }
        mediaIDs = deduped
    }

    if err := s.shares.Insert(ctx, scope, mediaIDs); err != nil {
        return share.Scope{}, err
    }
    return scope, nil
}

// granteeValid enforces the four checks from §8.2: non-empty,
// bounded, not the zero principal, not equal to caller.
func granteeValid(g, caller owners.Principal) bool {
    if g.Hub == "" || g.UserID == "" {
        return false
    }
    if len(g.Hub) > share.PrincipalFieldMaxLen || len(g.UserID) > share.PrincipalFieldMaxLen {
        return false
    }
    if g == caller {
        return false
    }
    if g.IsZero() {
        return false
    }
    return true
}

// dedupeMediaIDs removes duplicates (preserving first-seen order) and
// checks the bound. Returns ErrInvalidMediaSet if the deduped list is
// empty or exceeds share.MediaSetMaxLen.
func dedupeMediaIDs(ids []string) ([]string, error) {
    seen := make(map[string]struct{}, len(ids))
    out := make([]string, 0, len(ids))
    for _, id := range ids {
        if id == "" {
            return nil, share.ErrInvalidMediaSet
        }
        if _, ok := seen[id]; ok {
            continue
        }
        seen[id] = struct{}{}
        out = append(out, id)
    }
    if len(out) == 0 || len(out) > share.MediaSetMaxLen {
        return nil, share.ErrInvalidMediaSet
    }
    return out, nil
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/service/... -run TestShareCreate -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/service/share_service.go internal/service/share_service_test.go
git commit -m "Add ShareService.Create with validation and ownership pre-flights"
```

---

## Task 14: `ShareService.Get` / `List` / `Revoke` / `Retry`

**Files:**
- Modify: `internal/service/share_service.go`
- Modify: `internal/service/share_service_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/service/share_service_test.go`:

```go
func TestShareGetReturnsOwnedScope(t *testing.T) {
    r := require.New(t)
    fx := newShareFixture(t)
    albumID := fx.seedAlbum(t, 1)
    s, err := fx.svc.Create(context.Background(), service.CreateShareRequest{
        Grantee:    owners.Principal{Hub: "h", UserID: "a"},
        TargetType: share.TargetAlbumLive,
        AlbumID:    albumID,
    }, fx.owner)
    r.NoError(err)

    got, err := fx.svc.Get(context.Background(), s.UUID, fx.owner)
    r.NoError(err)
    r.Equal(s.UUID, got.UUID)
}

func TestShareGetCrossOwnerNotFound(t *testing.T) {
    r := require.New(t)
    fx := newShareFixture(t)
    albumID := fx.seedAlbum(t, 1)
    s, err := fx.svc.Create(context.Background(), service.CreateShareRequest{
        Grantee:    owners.Principal{Hub: "h", UserID: "a"},
        TargetType: share.TargetAlbumLive,
        AlbumID:    albumID,
    }, fx.owner)
    r.NoError(err)

    intruder := owners.Principal{Hub: "h", UserID: "intruder"}
    _, err = fx.svc.Get(context.Background(), s.UUID, intruder)
    r.ErrorIs(err, errs.ErrNotFound)
}

func TestShareListScopedToCaller(t *testing.T) {
    r := require.New(t)
    fx := newShareFixture(t)
    albumID := fx.seedAlbum(t, 1)
    s1, err := fx.svc.Create(context.Background(), service.CreateShareRequest{
        Grantee: owners.Principal{Hub: "h", UserID: "a"}, TargetType: share.TargetAlbumLive, AlbumID: albumID,
    }, fx.owner)
    r.NoError(err)

    got, err := fx.svc.List(context.Background(), share.ScopeFilter{}, fx.owner)
    r.NoError(err)
    found := false
    for _, s := range got {
        if s.UUID == s1.UUID {
            found = true
        }
    }
    r.True(found)

    intruder := owners.Principal{Hub: "h", UserID: "intruder"}
    got, err = fx.svc.List(context.Background(), share.ScopeFilter{}, intruder)
    r.NoError(err)
    r.Empty(got)
}

func TestShareRevokeTransitionsPendingToRevoking(t *testing.T) {
    r := require.New(t)
    fx := newShareFixture(t)
    albumID := fx.seedAlbum(t, 1)
    s, err := fx.svc.Create(context.Background(), service.CreateShareRequest{
        Grantee: owners.Principal{Hub: "h", UserID: "a"}, TargetType: share.TargetAlbumLive, AlbumID: albumID,
    }, fx.owner)
    r.NoError(err)

    got, err := fx.svc.Revoke(context.Background(), s.UUID, fx.owner)
    r.NoError(err)
    r.Equal(share.StatusRevoking, got.BrokerStatus)
    r.NotNil(got.RevokedAt)
}

func TestShareRevokeIdempotentError(t *testing.T) {
    r := require.New(t)
    fx := newShareFixture(t)
    albumID := fx.seedAlbum(t, 1)
    s, err := fx.svc.Create(context.Background(), service.CreateShareRequest{
        Grantee: owners.Principal{Hub: "h", UserID: "a"}, TargetType: share.TargetAlbumLive, AlbumID: albumID,
    }, fx.owner)
    r.NoError(err)

    _, err = fx.svc.Revoke(context.Background(), s.UUID, fx.owner)
    r.NoError(err)
    _, err = fx.svc.Revoke(context.Background(), s.UUID, fx.owner)
    r.ErrorIs(err, share.ErrScopeAlreadyRevoked)
}

func TestShareRevokeCrossOwnerNotFound(t *testing.T) {
    r := require.New(t)
    fx := newShareFixture(t)
    albumID := fx.seedAlbum(t, 1)
    s, err := fx.svc.Create(context.Background(), service.CreateShareRequest{
        Grantee: owners.Principal{Hub: "h", UserID: "a"}, TargetType: share.TargetAlbumLive, AlbumID: albumID,
    }, fx.owner)
    r.NoError(err)

    intruder := owners.Principal{Hub: "h", UserID: "intruder"}
    _, err = fx.svc.Revoke(context.Background(), s.UUID, intruder)
    r.ErrorIs(err, errs.ErrNotFound)
}

func TestShareRetryPublishRoutesFailedToPending(t *testing.T) {
    r := require.New(t)
    fx := newShareFixture(t)
    albumID := fx.seedAlbum(t, 1)
    s, err := fx.svc.Create(context.Background(), service.CreateShareRequest{
        Grantee: owners.Principal{Hub: "h", UserID: "a"}, TargetType: share.TargetAlbumLive, AlbumID: albumID,
    }, fx.owner)
    r.NoError(err)
    _, err = fx.rw.ExecContext(context.Background(),
        `UPDATE scopes SET broker_status='failed', broker_attempts=10, broker_last_error='x' WHERE uuid=?`,
        s.UUID)
    r.NoError(err)

    got, err := fx.svc.Retry(context.Background(), s.UUID, fx.owner)
    r.NoError(err)
    r.Equal(share.StatusPending, got.BrokerStatus)
    r.Equal(0, got.BrokerAttempts)
}

func TestShareRetryRevokeRoutesFailedToRevoking(t *testing.T) {
    r := require.New(t)
    fx := newShareFixture(t)
    albumID := fx.seedAlbum(t, 1)
    s, err := fx.svc.Create(context.Background(), service.CreateShareRequest{
        Grantee: owners.Principal{Hub: "h", UserID: "a"}, TargetType: share.TargetAlbumLive, AlbumID: albumID,
    }, fx.owner)
    r.NoError(err)
    now := time.Now().UTC()
    _, err = fx.rw.ExecContext(context.Background(),
        `UPDATE scopes SET broker_status='failed', broker_attempts=10, revoked_at=? WHERE uuid=?`,
        now, s.UUID)
    r.NoError(err)

    got, err := fx.svc.Retry(context.Background(), s.UUID, fx.owner)
    r.NoError(err)
    r.Equal(share.StatusRevoking, got.BrokerStatus)
}

func TestShareRetryRejectsNonFailed(t *testing.T) {
    r := require.New(t)
    fx := newShareFixture(t)
    albumID := fx.seedAlbum(t, 1)
    s, err := fx.svc.Create(context.Background(), service.CreateShareRequest{
        Grantee: owners.Principal{Hub: "h", UserID: "a"}, TargetType: share.TargetAlbumLive, AlbumID: albumID,
    }, fx.owner)
    r.NoError(err)

    _, err = fx.svc.Retry(context.Background(), s.UUID, fx.owner)
    r.ErrorIs(err, share.ErrRetryNotApplicable)
}
```

- [ ] **Step 2: Run the failing tests**

Run: `go test ./internal/service/... -run 'TestShareGet|TestShareList|TestShareRevoke|TestShareRetry' -v`
Expected: FAIL — methods don't exist.

- [ ] **Step 3: Implement `Get`, `List`, `Revoke`, `Retry`**

Append to `internal/service/share_service.go`:

```go
// Get returns the scope detail if caller is its owner, else
// errs.ErrNotFound. The scope's MediaIDs are populated for media_set
// targets.
func (s *ShareService) Get(ctx context.Context, uuidStr string, caller owners.Principal) (share.ScopeDetail, error) {
    det, err := s.shares.GetByUUID(ctx, uuidStr)
    if err != nil {
        return share.ScopeDetail{}, err
    }
    if det.Owner != caller {
        return share.ScopeDetail{}, fmt.Errorf("%w: scope uuid=%s", errs.ErrNotFound, uuidStr)
    }
    return det, nil
}

// List returns the caller's scopes. filter is passed through verbatim
// after Owner is forced to caller (defence-in-depth against a filter
// that set Grantee==caller or similar).
func (s *ShareService) List(ctx context.Context, filter share.ScopeFilter, caller owners.Principal) ([]share.Scope, error) {
    return s.shares.ListByOwner(ctx, caller, filter)
}

// Revoke marks the scope revoked locally and schedules broker
// revocation. Always idempotent from the worker's point of view; the
// service returns ErrScopeAlreadyRevoked if the row was already
// revoked (or has completed revoke).
func (s *ShareService) Revoke(ctx context.Context, uuidStr string, caller owners.Principal) (share.Scope, error) {
    det, err := s.shares.GetByUUID(ctx, uuidStr)
    if err != nil {
        return share.Scope{}, err
    }
    if det.Owner != caller {
        return share.Scope{}, fmt.Errorf("%w: scope uuid=%s", errs.ErrNotFound, uuidStr)
    }
    n, err := s.shares.SetRevoking(ctx, uuidStr, s.now())
    if err != nil {
        return share.Scope{}, err
    }
    if n == 0 {
        return share.Scope{}, share.ErrScopeAlreadyRevoked
    }
    fresh, err := s.shares.GetByUUID(ctx, uuidStr)
    if err != nil {
        return share.Scope{}, err
    }
    return fresh.Scope, nil
}

// Retry reopens a failed scope. Whether the retry routes through
// pending or revoking depends on whether the row was mid-publish or
// mid-revoke when it failed (encoded by revoked_at).
func (s *ShareService) Retry(ctx context.Context, uuidStr string, caller owners.Principal) (share.Scope, error) {
    det, err := s.shares.GetByUUID(ctx, uuidStr)
    if err != nil {
        return share.Scope{}, err
    }
    if det.Owner != caller {
        return share.Scope{}, fmt.Errorf("%w: scope uuid=%s", errs.ErrNotFound, uuidStr)
    }
    if det.BrokerStatus != share.StatusFailed {
        return share.Scope{}, share.ErrRetryNotApplicable
    }
    var n int64
    if det.RevokedAt == nil {
        n, err = s.shares.RetryPublish(ctx, uuidStr)
    } else {
        n, err = s.shares.RetryRevoke(ctx, uuidStr)
    }
    if err != nil {
        return share.Scope{}, err
    }
    if n == 0 {
        return share.Scope{}, share.ErrRetryNotApplicable
    }
    fresh, err := s.shares.GetByUUID(ctx, uuidStr)
    if err != nil {
        return share.Scope{}, err
    }
    return fresh.Scope, nil
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/service/... -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/service/share_service.go internal/service/share_service_test.go
git commit -m "Add ShareService Get / List / Revoke / Retry"
```

---

## Task 15: `AlbumService.Delete` via `PrepareAlbumDeleteTx`

**Files:**
- Modify: `internal/service/album_service.go`
- Modify: `internal/service/album_service_test.go`
- Modify: `internal/cli/albums.go`
- Modify: `internal/cli/server.go`

This task rewires `AlbumService` to take `*share.Repo` and `*db.DB`
so its `Delete` can run share purge/block-check + album delete
atomically in one tx. All existing Plan-D callers need to pass the
new deps.

- [ ] **Step 1: Write the failing tests**

Append to `internal/service/album_service_test.go`:

```go
func TestAlbumDeleteBlocksWhenLiveScopes(t *testing.T) {
    r := require.New(t)
    ctx := context.Background()
    d := testutil.OpenTestDB(t)
    owner := owners.Principal{Hub: "h", UserID: "o"}
    _, err := d.WriteDB().ExecContext(ctx,
        `INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
        owner.Hub, owner.UserID, "sk", time.Now().UTC())
    r.NoError(err)
    albums := album.NewRepo(d.WriteDB(), d.ReadDB())
    shares := share.NewRepo(d.WriteDB(), d.ReadDB())
    mediaRepo := media.NewRepo(d.WriteDB(), d.ReadDB())
    svc := service.NewAlbumService(albums, mediaRepo, shares, d)
    shareSvc := service.NewShareService(shares, albums, mediaRepo)

    a, err := svc.Create(ctx, owner, "Trip")
    r.NoError(err)
    // Add one media so the album is non-empty (needed to mint an album_live scope).
    m := media.Media{
        ID: uuid.NewString(), Owner: owner, Type: media.TypePhoto,
        MimeType: "image/jpeg", Path: "2024/t.jpg",
        OriginalFilename: "x.jpg", ImportedAt: time.Now().UTC().Truncate(time.Second),
        Size: 100, Checksum: "cs", ThumbStatus: "pending",
    }
    r.NoError(mediaRepo.Insert(ctx, m))
    _, err = svc.AddMedia(ctx, a.ID, []string{m.ID}, owner)
    r.NoError(err)

    _, err = shareSvc.Create(ctx, service.CreateShareRequest{
        Grantee: owners.Principal{Hub: "h", UserID: "a"}, TargetType: share.TargetAlbumLive, AlbumID: a.ID,
    }, owner)
    r.NoError(err)

    err = svc.Delete(ctx, a.ID, owner)
    r.ErrorIs(err, share.ErrAlbumHasLiveScopes)
    // Album row still present.
    _, err = albums.GetByID(ctx, a.ID)
    r.NoError(err)
}

func TestAlbumDeletePurgesRevokedRemote(t *testing.T) {
    r := require.New(t)
    ctx := context.Background()
    d := testutil.OpenTestDB(t)
    owner := owners.Principal{Hub: "h", UserID: "o"}
    _, err := d.WriteDB().ExecContext(ctx,
        `INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
        owner.Hub, owner.UserID, "sk", time.Now().UTC())
    r.NoError(err)
    albums := album.NewRepo(d.WriteDB(), d.ReadDB())
    shares := share.NewRepo(d.WriteDB(), d.ReadDB())
    mediaRepo := media.NewRepo(d.WriteDB(), d.ReadDB())
    svc := service.NewAlbumService(albums, mediaRepo, shares, d)
    shareSvc := service.NewShareService(shares, albums, mediaRepo)

    a, err := svc.Create(ctx, owner, "Trip")
    r.NoError(err)
    m := media.Media{
        ID: uuid.NewString(), Owner: owner, Type: media.TypePhoto,
        MimeType: "image/jpeg", Path: "2024/t.jpg",
        OriginalFilename: "x.jpg", ImportedAt: time.Now().UTC().Truncate(time.Second),
        Size: 100, Checksum: "cs", ThumbStatus: "pending",
    }
    r.NoError(mediaRepo.Insert(ctx, m))
    _, err = svc.AddMedia(ctx, a.ID, []string{m.ID}, owner)
    r.NoError(err)

    s, err := shareSvc.Create(ctx, service.CreateShareRequest{
        Grantee: owners.Principal{Hub: "h", UserID: "a"}, TargetType: share.TargetAlbumLive, AlbumID: a.ID,
    }, owner)
    r.NoError(err)
    // Drive to revoked_remote directly.
    now := time.Now().UTC()
    _, err = d.WriteDB().ExecContext(ctx,
        `UPDATE scopes SET broker_status='revoked_remote', revoked_at=?, broker_revoked_at=? WHERE uuid=?`,
        now, now, s.UUID)
    r.NoError(err)

    r.NoError(svc.Delete(ctx, a.ID, owner))
    _, err = albums.GetByID(ctx, a.ID)
    r.ErrorIs(err, errs.ErrNotFound)
    _, err = shares.GetByUUID(ctx, s.UUID)
    r.ErrorIs(err, errs.ErrNotFound)
}
```

Import additions for the test file (add if not already present):
`"github.com/wesm/fotobank/internal/share"`, `"github.com/wesm/fotobank/internal/testutil"`,
`"github.com/google/uuid"`, etc.

All **existing** callers of `service.NewAlbumService(albums, media)`
must also be updated to the new four-arg form
`service.NewAlbumService(albums, media, shares, d)`, where `shares`
comes from `share.NewRepo(d.WriteDB(), d.ReadDB())` and `d` is the
testutil DB handle. Apply a sweeping edit across:

- `internal/service/album_service_test.go`
- `internal/httpapi/albums_test.go` (the `newAlbumsAPIFixture` helper)

See Step 5's checklist.

- [ ] **Step 2: Run the failing tests**

Run: `go test ./internal/service/... -run TestAlbumDelete -v`
Expected: FAIL with compilation errors in the constructor signature + new tests missing.

- [ ] **Step 3: Update `AlbumService` constructor and `Delete`**

Edit `internal/service/album_service.go`. Change the imports and
struct to include share and db; add shares + db fields and rewrite
Delete. The final relevant portions look like:

```go
import (
    "context"
    "errors"
    "fmt"
    "strings"
    "time"

    "github.com/google/uuid"

    "github.com/wesm/fotobank/internal/album"
    "github.com/wesm/fotobank/internal/db"
    "github.com/wesm/fotobank/internal/errs"
    "github.com/wesm/fotobank/internal/media"
    "github.com/wesm/fotobank/internal/owners"
    "github.com/wesm/fotobank/internal/share"
)

type AlbumService struct {
    albums *album.Repo
    media  *media.Repo
    shares *share.Repo
    db     *db.DB
    now    func() time.Time
}

func NewAlbumService(a *album.Repo, m *media.Repo, s *share.Repo, d *db.DB) *AlbumService {
    return &AlbumService{
        albums: a, media: m, shares: s, db: d,
        now: func() time.Time { return time.Now().UTC() },
    }
}

// Delete removes the album. Share purge + block-check run in the same
// tx as the album row delete so a concurrent mutation cannot slip a
// blocking scope in between the check and the delete.
// ErrNotFound if the album is missing or cross-owner.
// share.ErrAlbumHasLiveScopes if any non-revoked_remote scope points
// at the album; the album row and all scope rows remain unchanged in
// that case.
func (s *AlbumService) Delete(ctx context.Context, id string, caller owners.Principal) error {
    if _, err := s.Get(ctx, id, caller); err != nil {
        return err
    }
    return s.db.Tx(ctx, func(tx *sql.Tx) error {
        if err := s.shares.PrepareAlbumDeleteTx(ctx, tx, id); err != nil {
            return err
        }
        return s.albums.DeleteTx(ctx, tx, id)
    })
}
```

Add `"database/sql"` to the import block as needed. Silence the
unused-`errors` warning if it appears by removing `errors` from the
import list — or keep it if other methods still use it.

- [ ] **Step 4: Update the Plan-D test fixtures**

Apply the following mechanical sweep over
`internal/service/album_service_test.go`:

1. Add imports: `"github.com/wesm/fotobank/internal/share"`,
   `"github.com/wesm/fotobank/internal/testutil"` (likely already present),
   and any missing `"github.com/google/uuid"`.
2. In every test that constructs `AlbumService`, open the DB with
   `d := testutil.OpenTestDB(t)`, create
   `shares := share.NewRepo(d.WriteDB(), d.ReadDB())`, and pass
   both into the constructor. Many tests will already have `d`.
3. For tests that build a fresh repo directly (e.g.
   `svc := service.NewAlbumService(album.NewRepo(...), media.NewRepo(...))`),
   rewrite to
   `svc := service.NewAlbumService(album.NewRepo(...), media.NewRepo(...), share.NewRepo(d.WriteDB(), d.ReadDB()), d)`.

- [ ] **Step 5: Update callers outside the test file**

Edit `internal/cli/albums.go`: the `loadAlbumCtx` function now needs
to construct a share.Repo too.

Replace the relevant block:

```go
return &albumCtx{
    svc: service.NewAlbumService(
        album.NewRepo(d.WriteDB(), d.ReadDB()),
        media.NewRepo(d.WriteDB(), d.ReadDB()),
    ),
    caller: owners.Principal{Hub: cfg.Identity.Stub.Hub, UserID: cfg.Identity.Stub.UserID},
    close:  func() { _ = d.Close() },
}, nil
```

with:

```go
return &albumCtx{
    svc: service.NewAlbumService(
        album.NewRepo(d.WriteDB(), d.ReadDB()),
        media.NewRepo(d.WriteDB(), d.ReadDB()),
        share.NewRepo(d.WriteDB(), d.ReadDB()),
        d,
    ),
    caller: owners.Principal{Hub: cfg.Identity.Stub.Hub, UserID: cfg.Identity.Stub.UserID},
    close:  func() { _ = d.Close() },
}, nil
```

Add `"github.com/wesm/fotobank/internal/share"` to that file's imports.

Edit `internal/cli/server.go`: `albumSvc` construction now takes the
same two extra arguments. Replace:

```go
albumSvc := service.NewAlbumService(
    album.NewRepo(d.WriteDB(), d.ReadDB()),
    media.NewRepo(d.WriteDB(), d.ReadDB()),
)
```

with:

```go
sharesRepo := share.NewRepo(d.WriteDB(), d.ReadDB())
albumSvc := service.NewAlbumService(
    album.NewRepo(d.WriteDB(), d.ReadDB()),
    media.NewRepo(d.WriteDB(), d.ReadDB()),
    sharesRepo,
    d,
)
```

Add `"github.com/wesm/fotobank/internal/share"` to that file's imports.
`sharesRepo` will be re-used in Task 17 for the share HTTP surface.

Edit `internal/httpapi/albums_test.go`: the `newAlbumsAPIFixture` helper
constructs an `AlbumService` with the old two-arg form. Update it to
the four-arg form:

```go
aRepo := album.NewRepo(d.WriteDB(), d.ReadDB())
mRepo := media.NewRepo(d.WriteDB(), d.ReadDB())
sRepo := share.NewRepo(d.WriteDB(), d.ReadDB())
svc := service.NewAlbumService(aRepo, mRepo, sRepo, d)
```

Add `"github.com/wesm/fotobank/internal/share"` to that file's imports.

- [ ] **Step 6: Run the full test suite**

Run: `go test ./... -count=1`
Expected: PASS across the board (share service + updated album service tests + CLI + server tests all happy).

- [ ] **Step 7: Commit**

```bash
git add internal/service/album_service.go \
        internal/service/album_service_test.go \
        internal/cli/albums.go \
        internal/cli/server.go \
        internal/httpapi/albums_test.go
git commit -m "Wire AlbumService.Delete through db.Tx + PrepareAlbumDeleteTx"
```

---

## Task 16: HTTP surface — `/api/v1/shares` + translator updates

**Files:**
- Create: `internal/httpapi/shares.go`
- Create: `internal/httpapi/shares_test.go`
- Modify: `internal/httpapi/albums.go` (add share.ErrAlbumHasLiveScopes case)
- Modify: `internal/httpapi/albums_test.go` (extend translator unit test)
- Modify: `internal/httpapi/api.go` (Deps + buildAPI)
- Modify: `internal/httpapi/openapi.go` (regenerated via `make api-generate`)

> **Note (2026-04-23 sweep 3):** Steps 1-4 below (translator case + unit test for
> `share.ErrAlbumHasLiveScopes`) were pulled forward into roborev sweep 3 to close
> a live finding on the T15 commit. Verify these files already have the changes
> before skipping: `internal/httpapi/albums.go::translateAlbumError` and the test
> `TestTranslateAlbumErrorShareAlbumHasLiveScopes`. If present, skip to Step 5.

- [ ] **Step 1: Extend the album translator test**

Edit `internal/httpapi/albums_test.go`. Find the existing
`TestTranslateAlbumErrorInvalidSentinels` (or sibling) test and add a
case for `share.ErrAlbumHasLiveScopes`:

```go
func TestTranslateAlbumErrorShareAlbumHasLiveScopes(t *testing.T) {
    r := require.New(t)
    got := httpapi.TranslateAlbumErrorForTest(share.ErrAlbumHasLiveScopes)
    r.Equal(409, httpapi.StatusFrom(got))
}
```

Add `"github.com/wesm/fotobank/internal/share"` to the file imports.

- [ ] **Step 2: Run the failing test**

Run: `go test ./internal/httpapi/... -run AlbumErrorShareAlbumHas -v`
Expected: FAIL — translator currently maps the sentinel to 500 via default.

- [ ] **Step 3: Update `translateAlbumError`**

Edit `internal/httpapi/albums.go` and extend the switch:

```go
import (
    // existing ...
    "github.com/wesm/fotobank/internal/share"
)

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
    case errors.Is(err, share.ErrAlbumHasLiveScopes):
        return huma.Error409Conflict("album has outstanding shares; revoke or retry them first")
    default:
        return Translate(err)
    }
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/httpapi/... -run AlbumErrorShareAlbumHas -v`
Expected: PASS.

- [ ] **Step 5: Write the failing share-routes tests**

Create `internal/httpapi/shares_test.go`:

```go
package httpapi_test

import (
    "bytes"
    "context"
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "testing"
    "time"

    "github.com/google/uuid"
    "github.com/stretchr/testify/require"

    "github.com/wesm/fotobank/internal/album"
    "github.com/wesm/fotobank/internal/db"
    "github.com/wesm/fotobank/internal/httpapi"
    "github.com/wesm/fotobank/internal/identity"
    "github.com/wesm/fotobank/internal/media"
    "github.com/wesm/fotobank/internal/owners"
    "github.com/wesm/fotobank/internal/service"
    "github.com/wesm/fotobank/internal/share"
    "github.com/wesm/fotobank/internal/testutil"
)

type sharesHTTPFixture struct {
    h      http.Handler
    owner  owners.Principal
    shares *service.ShareService
    albums *service.AlbumService
    media  *media.Repo
    db     *db.DB
}

func newSharesHTTPFixture(t *testing.T) *sharesHTTPFixture {
    t.Helper()
    ctx := context.Background()
    d := testutil.OpenTestDB(t)
    owner := owners.Principal{Hub: "h", UserID: "o"}
    _, err := d.WriteDB().ExecContext(ctx,
        `INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
        owner.Hub, owner.UserID, "sk", time.Now().UTC())
    require.NoError(t, err)

    albumsRepo := album.NewRepo(d.WriteDB(), d.ReadDB())
    mediaRepo := media.NewRepo(d.WriteDB(), d.ReadDB())
    shareRepo := share.NewRepo(d.WriteDB(), d.ReadDB())
    albumSvc := service.NewAlbumService(albumsRepo, mediaRepo, shareRepo, d)
    shareSvc := service.NewShareService(shareRepo, albumsRepo, mediaRepo)

    h, err := httpapi.New(httpapi.Deps{
        IdentityProvider: identity.NewStub(owner, "Test User"),
        AlbumService:     albumSvc,
        ShareService:     shareSvc,
    })
    require.NoError(t, err)
    return &sharesHTTPFixture{
        h: h, owner: owner, shares: shareSvc, albums: albumSvc,
        media: mediaRepo, db: d,
    }
}

func (fx *sharesHTTPFixture) seedAlbumWithMedia(t *testing.T) string {
    t.Helper()
    ctx := context.Background()
    a, err := fx.albums.Create(ctx, fx.owner, "T")
    require.NoError(t, err)
    m := media.Media{
        ID: uuid.NewString(), Owner: fx.owner, Type: media.TypePhoto,
        MimeType: "image/jpeg", Path: "2024/" + uuid.NewString() + ".jpg",
        OriginalFilename: "x.jpg", ImportedAt: time.Now().UTC().Truncate(time.Second),
        Size: 100, Checksum: "cs" + uuid.NewString(), ThumbStatus: "pending",
    }
    require.NoError(t, fx.media.Insert(ctx, m))
    _, err = fx.albums.AddMedia(ctx, a.ID, []string{m.ID}, fx.owner)
    require.NoError(t, err)
    return a.ID
}

func TestSharesCreateAlbumLive201(t *testing.T) {
    r := require.New(t)
    fx := newSharesHTTPFixture(t)
    albumID := fx.seedAlbumWithMedia(t)

    body, _ := json.Marshal(map[string]any{
        "label":       "Summer",
        "grantee":     map[string]string{"hub": "h", "user_id": "alice"},
        "target_type": "album_live",
        "album_id":    albumID,
    })
    req := httptest.NewRequest(http.MethodPost, "/api/v1/shares", bytes.NewReader(body))
    req.Header.Set("Content-Type", "application/json")
    rec := httptest.NewRecorder()
    fx.h.ServeHTTP(rec, req)
    r.Equal(http.StatusCreated, rec.Code, rec.Body.String())

    var resp map[string]any
    r.NoError(json.Unmarshal(rec.Body.Bytes(), &resp))
    r.Equal("pending", resp["broker_status"])
    r.Equal(albumID, resp["target_album_id"])
}

func TestSharesCreateInvalidTargetCombo400(t *testing.T) {
    r := require.New(t)
    fx := newSharesHTTPFixture(t)
    albumID := fx.seedAlbumWithMedia(t)
    body, _ := json.Marshal(map[string]any{
        "grantee":     map[string]string{"hub": "h", "user_id": "alice"},
        "target_type": "media_set",
        "album_id":    albumID, // wrong combo
    })
    req := httptest.NewRequest(http.MethodPost, "/api/v1/shares", bytes.NewReader(body))
    req.Header.Set("Content-Type", "application/json")
    rec := httptest.NewRecorder()
    fx.h.ServeHTTP(rec, req)
    r.Equal(http.StatusBadRequest, rec.Code)
}

func TestSharesGet404WhenCrossOwner(t *testing.T) {
    r := require.New(t)
    fx := newSharesHTTPFixture(t)
    req := httptest.NewRequest(http.MethodGet, "/api/v1/shares/"+uuid.NewString(), nil)
    rec := httptest.NewRecorder()
    fx.h.ServeHTTP(rec, req)
    r.Equal(http.StatusNotFound, rec.Code)
}

func TestSharesRevoke201ThenAlreadyRevoked409(t *testing.T) {
    r := require.New(t)
    fx := newSharesHTTPFixture(t)
    albumID := fx.seedAlbumWithMedia(t)
    s, err := fx.shares.Create(context.Background(), service.CreateShareRequest{
        Grantee: owners.Principal{Hub: "h", UserID: "a"}, TargetType: share.TargetAlbumLive, AlbumID: albumID,
    }, fx.owner)
    r.NoError(err)

    req := httptest.NewRequest(http.MethodPost, "/api/v1/shares/"+s.UUID+"/revoke", nil)
    rec := httptest.NewRecorder()
    fx.h.ServeHTTP(rec, req)
    r.Equal(http.StatusOK, rec.Code, rec.Body.String())

    // Second call -> 409.
    req = httptest.NewRequest(http.MethodPost, "/api/v1/shares/"+s.UUID+"/revoke", nil)
    rec = httptest.NewRecorder()
    fx.h.ServeHTTP(rec, req)
    r.Equal(http.StatusConflict, rec.Code)
}

func TestSharesRetryNotApplicable409(t *testing.T) {
    r := require.New(t)
    fx := newSharesHTTPFixture(t)
    albumID := fx.seedAlbumWithMedia(t)
    s, err := fx.shares.Create(context.Background(), service.CreateShareRequest{
        Grantee: owners.Principal{Hub: "h", UserID: "a"}, TargetType: share.TargetAlbumLive, AlbumID: albumID,
    }, fx.owner)
    r.NoError(err)
    req := httptest.NewRequest(http.MethodPost, "/api/v1/shares/"+s.UUID+"/retry", nil)
    rec := httptest.NewRecorder()
    fx.h.ServeHTTP(rec, req)
    r.Equal(http.StatusConflict, rec.Code)
}

func TestSharesListDefaultHidesRevokedRemote(t *testing.T) {
    r := require.New(t)
    fx := newSharesHTTPFixture(t)
    albumID := fx.seedAlbumWithMedia(t)
    _, err := fx.shares.Create(context.Background(), service.CreateShareRequest{
        Grantee: owners.Principal{Hub: "h", UserID: "visible"}, TargetType: share.TargetAlbumLive, AlbumID: albumID,
    }, fx.owner)
    r.NoError(err)
    hidden, err := fx.shares.Create(context.Background(), service.CreateShareRequest{
        Grantee: owners.Principal{Hub: "h", UserID: "hidden"}, TargetType: share.TargetAlbumLive, AlbumID: albumID,
    }, fx.owner)
    r.NoError(err)
    // Flip hidden → revoked_remote directly via the fixture's DB handle.
    now := time.Now().UTC()
    _, err = fx.db.WriteDB().ExecContext(context.Background(),
        `UPDATE scopes SET broker_status='revoked_remote', revoked_at=?, broker_revoked_at=? WHERE uuid=?`,
        now, now, hidden.UUID)
    r.NoError(err)

    // Default: only `visible` is returned — `hidden` is in revoked_remote
    // and the default view suppresses that terminal purge-eligible state.
    req := httptest.NewRequest(http.MethodGet, "/api/v1/shares", nil)
    rec := httptest.NewRecorder()
    fx.h.ServeHTTP(rec, req)
    r.Equal(http.StatusOK, rec.Code)
    var resp struct {
        Items []map[string]any `json:"items"`
    }
    r.NoError(json.Unmarshal(rec.Body.Bytes(), &resp))
    r.Len(resp.Items, 1)

    // status=revoked_remote: the explicit filter surfaces `hidden`.
    req = httptest.NewRequest(http.MethodGet, "/api/v1/shares?status=revoked_remote", nil)
    rec = httptest.NewRecorder()
    fx.h.ServeHTTP(rec, req)
    r.Equal(http.StatusOK, rec.Code)
    resp = struct{ Items []map[string]any `json:"items"` }{}
    r.NoError(json.Unmarshal(rec.Body.Bytes(), &resp))
    r.Len(resp.Items, 1)
}

func TestSharesListUnknownStatus400(t *testing.T) {
    r := require.New(t)
    fx := newSharesHTTPFixture(t)
    req := httptest.NewRequest(http.MethodGet, "/api/v1/shares?status=bogus", nil)
    rec := httptest.NewRecorder()
    fx.h.ServeHTTP(rec, req)
    r.Equal(http.StatusBadRequest, rec.Code)
}
```

- [ ] **Step 6: Run the failing tests**

Run: `go test ./internal/httpapi/... -run Shares -v`
Expected: FAIL — routes, DTOs, translator not implemented; `httpapi.Deps.ShareService` doesn't exist.

- [ ] **Step 7: Extend `Deps` and register the routes**

Edit `internal/httpapi/api.go`. Add the field to `Deps`:

```go
type Deps struct {
    // ... existing fields ...
    ShareService *service.ShareService
}
```

Extend `buildAPI` to wire the share routes:

```go
func buildAPI(deps Deps) (*http.ServeMux, huma.API) {
    mux := http.NewServeMux()
    api := humago.New(mux, huma.DefaultConfig("Fotobank", version.Short))
    api.OpenAPI().Info.Description = "Fotobank HTTP API"
    registerHealthz(api)
    registerMe(api)
    registerMedia(api, deps.MediaService)
    registerMediaOriginal(mux, deps.MediaService)
    registerMediaThumb(mux, deps.ThumbService)
    registerAlbums(api, deps.AlbumService)
    registerShares(api, deps.ShareService)   // new
    return mux, api
}
```

- [ ] **Step 8: Create `internal/httpapi/shares.go`**

Create `internal/httpapi/shares.go`:

```go
package httpapi

import (
    "context"
    "errors"
    "net/http"
    "strings"
    "time"

    "github.com/danielgtaylor/huma/v2"

    "github.com/wesm/fotobank/internal/errs"
    "github.com/wesm/fotobank/internal/owners"
    "github.com/wesm/fotobank/internal/service"
    "github.com/wesm/fotobank/internal/share"
)

// translateShareError maps service-layer errors to the share HTTP
// surface's expected status codes. Delegates unknown errors to the
// cross-cutting Translate.
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
        return Translate(err)
    }
}

// principalDTO / scopeDTO / scopeDetailDTO are the wire shapes.
type principalDTO struct {
    Hub    string `json:"hub"`
    UserID string `json:"user_id"`
}

type scopeDTO struct {
    UUID                string       `json:"uuid"`
    Owner               principalDTO `json:"owner"`
    Grantee             principalDTO `json:"grantee"`
    TargetType          string       `json:"target_type"`
    TargetAlbumID       string       `json:"target_album_id,omitempty"`
    AllowDownload       bool         `json:"allow_download"`
    Label               string       `json:"label,omitempty"`
    CreatedAt           time.Time    `json:"created_at"`
    ExpiresAt           *time.Time   `json:"expires_at,omitempty"`
    Expired             bool         `json:"expired"`
    RevokedAt           *time.Time   `json:"revoked_at,omitempty"`
    BrokerStatus        string       `json:"broker_status"`
    BrokerRegisteredAt  *time.Time   `json:"broker_registered_at,omitempty"`
    BrokerGrantedAt     *time.Time   `json:"broker_granted_at,omitempty"`
    BrokerRevokedAt     *time.Time   `json:"broker_revoked_at,omitempty"`
    BrokerLastError     string       `json:"broker_last_error,omitempty"`
    BrokerAttempts      int          `json:"broker_attempts"`
    BrokerNextAttemptAt *time.Time   `json:"broker_next_attempt_at,omitempty"`
}

type scopeDetailDTO struct {
    scopeDTO
    MediaIDs []string `json:"media_ids,omitempty"`
}

func toScopeDTO(s share.Scope) scopeDTO {
    out := scopeDTO{
        UUID:    s.UUID,
        Owner:   principalDTO{Hub: s.Owner.Hub, UserID: s.Owner.UserID},
        Grantee: principalDTO{Hub: s.Grantee.Hub, UserID: s.Grantee.UserID},
        TargetType:          string(s.TargetType),
        AllowDownload:       s.AllowDownload,
        Label:               s.Label,
        CreatedAt:           s.CreatedAt,
        ExpiresAt:           s.ExpiresAt,
        RevokedAt:           s.RevokedAt,
        BrokerStatus:        string(s.BrokerStatus),
        BrokerRegisteredAt:  s.BrokerRegisteredAt,
        BrokerGrantedAt:     s.BrokerGrantedAt,
        BrokerRevokedAt:     s.BrokerRevokedAt,
        BrokerLastError:     s.BrokerLastError,
        BrokerAttempts:      s.BrokerAttempts,
        BrokerNextAttemptAt: s.BrokerNextAttemptAt,
    }
    if s.TargetAlbumID != nil {
        out.TargetAlbumID = *s.TargetAlbumID
    }
    if s.ExpiresAt != nil && time.Now().UTC().After(*s.ExpiresAt) {
        out.Expired = true
    }
    return out
}

func toScopeDetailDTO(d share.ScopeDetail) scopeDetailDTO {
    return scopeDetailDTO{scopeDTO: toScopeDTO(d.Scope), MediaIDs: d.MediaIDs}
}

func callerFromCtx(ctx context.Context) (owners.Principal, error) {
    id, ok := IdentityFromContext(ctx)
    if !ok {
        return owners.Principal{}, errs.ErrIdentityMissing
    }
    return id.Principal.OwnersPrincipal(), nil
}

// registerShares wires /api/v1/shares. svc == nil answers 503 so the
// OpenAPI dumper can build the spec without real deps.
func registerShares(api huma.API, svc *service.ShareService) {
    registerSharesCreate(api, svc)
    registerSharesList(api, svc)
    registerSharesGet(api, svc)
    registerSharesRevoke(api, svc)
    registerSharesRetry(api, svc)
}

// --- inputs/outputs ---

type createShareInput struct {
    Body struct {
        Label         string       `json:"label,omitempty"`
        Grantee       principalDTO `json:"grantee"`
        AllowDownload bool         `json:"allow_download,omitempty"`
        ExpiresAt     *time.Time   `json:"expires_at,omitempty"`
        TargetType    string       `json:"target_type"`
        AlbumID       string       `json:"album_id,omitempty"`
        MediaIDs      []string     `json:"media_ids,omitempty"`
    }
}

type scopeOutput struct {
    Status int
    Body   scopeDTO
}

type scopeDetailOutput struct {
    Status int
    Body   scopeDetailDTO
}

type listSharesInput struct {
    AlbumID        string `query:"album_id"`
    GranteeHub     string `query:"grantee_hub"`
    GranteeUserID  string `query:"grantee_user_id"`
    Status         string `query:"status" doc:"comma-separated broker_status filter"`
    IncludeSettled bool   `query:"include_settled"`
    Limit          int    `query:"limit" doc:"max rows (default 100, cap 500)"`
    Offset         int    `query:"offset"`
}

type listSharesOutput struct {
    Body struct {
        Items []scopeDTO `json:"items"`
    }
}

// --- handlers ---

func registerSharesCreate(api huma.API, svc *service.ShareService) {
    huma.Register(api, huma.Operation{
        OperationID: "shares-create",
        Method:      http.MethodPost,
        Path:        "/api/v1/shares",
        Summary:     "Create a scope (share) over an album or media set",
        DefaultStatus: http.StatusCreated,
    }, func(ctx context.Context, in *createShareInput) (*scopeOutput, error) {
        if svc == nil {
            return nil, huma.Error503ServiceUnavailable("share service unavailable")
        }
        caller, err := callerFromCtx(ctx)
        if err != nil {
            return nil, translateShareError(err)
        }
        req := service.CreateShareRequest{
            Label:         in.Body.Label,
            Grantee:       owners.Principal{Hub: in.Body.Grantee.Hub, UserID: in.Body.Grantee.UserID},
            AllowDownload: in.Body.AllowDownload,
            ExpiresAt:     in.Body.ExpiresAt,
            TargetType:    share.TargetType(in.Body.TargetType),
            AlbumID:       in.Body.AlbumID,
            MediaIDs:      in.Body.MediaIDs,
        }
        s, err := svc.Create(ctx, req, caller)
        if err != nil {
            return nil, translateShareError(err)
        }
        return &scopeOutput{Status: http.StatusCreated, Body: toScopeDTO(s)}, nil
    })
}

func registerSharesList(api huma.API, svc *service.ShareService) {
    huma.Register(api, huma.Operation{
        OperationID: "shares-list",
        Method:      http.MethodGet,
        Path:        "/api/v1/shares",
    }, func(ctx context.Context, in *listSharesInput) (*listSharesOutput, error) {
        if svc == nil {
            return nil, huma.Error503ServiceUnavailable("share service unavailable")
        }
        caller, err := callerFromCtx(ctx)
        if err != nil {
            return nil, translateShareError(err)
        }
        statuses, err := parseStatusFilter(in.Status)
        if err != nil {
            return nil, err
        }
        if in.Limit <= 0 {
            in.Limit = 100
        }
        if in.Limit > 500 {
            in.Limit = 500
        }
        filter := share.ScopeFilter{
            AlbumID:        in.AlbumID,
            Grantee:        owners.Principal{Hub: in.GranteeHub, UserID: in.GranteeUserID},
            Status:         statuses,
            IncludeSettled: in.IncludeSettled,
            Limit:          in.Limit,
            Offset:         in.Offset,
        }
        rows, err := svc.List(ctx, filter, caller)
        if err != nil {
            return nil, translateShareError(err)
        }
        out := &listSharesOutput{}
        out.Body.Items = make([]scopeDTO, 0, len(rows))
        for _, s := range rows {
            out.Body.Items = append(out.Body.Items, toScopeDTO(s))
        }
        return out, nil
    })
}

func parseStatusFilter(raw string) ([]share.BrokerStatus, error) {
    if raw == "" {
        return nil, nil
    }
    parts := strings.Split(raw, ",")
    out := make([]share.BrokerStatus, 0, len(parts))
    for _, p := range parts {
        p = strings.TrimSpace(p)
        if p == "" {
            continue
        }
        switch share.BrokerStatus(p) {
        case share.StatusPending, share.StatusActive, share.StatusFailed,
            share.StatusRevoking, share.StatusRevokedRemote:
            out = append(out, share.BrokerStatus(p))
        default:
            return nil, huma.Error400BadRequest("unknown status: " + p)
        }
    }
    return out, nil
}

type scopeUUIDParam struct {
    UUID string `path:"uuid"`
}

func registerSharesGet(api huma.API, svc *service.ShareService) {
    huma.Register(api, huma.Operation{
        OperationID: "shares-get",
        Method:      http.MethodGet,
        Path:        "/api/v1/shares/{uuid}",
    }, func(ctx context.Context, in *scopeUUIDParam) (*scopeDetailOutput, error) {
        if svc == nil {
            return nil, huma.Error503ServiceUnavailable("share service unavailable")
        }
        caller, err := callerFromCtx(ctx)
        if err != nil {
            return nil, translateShareError(err)
        }
        det, err := svc.Get(ctx, in.UUID, caller)
        if err != nil {
            return nil, translateShareError(err)
        }
        return &scopeDetailOutput{Status: http.StatusOK, Body: toScopeDetailDTO(det)}, nil
    })
}

func registerSharesRevoke(api huma.API, svc *service.ShareService) {
    huma.Register(api, huma.Operation{
        OperationID: "shares-revoke",
        Method:      http.MethodPost,
        Path:        "/api/v1/shares/{uuid}/revoke",
    }, func(ctx context.Context, in *scopeUUIDParam) (*scopeOutput, error) {
        if svc == nil {
            return nil, huma.Error503ServiceUnavailable("share service unavailable")
        }
        caller, err := callerFromCtx(ctx)
        if err != nil {
            return nil, translateShareError(err)
        }
        s, err := svc.Revoke(ctx, in.UUID, caller)
        if err != nil {
            return nil, translateShareError(err)
        }
        return &scopeOutput{Status: http.StatusOK, Body: toScopeDTO(s)}, nil
    })
}

func registerSharesRetry(api huma.API, svc *service.ShareService) {
    huma.Register(api, huma.Operation{
        OperationID: "shares-retry",
        Method:      http.MethodPost,
        Path:        "/api/v1/shares/{uuid}/retry",
    }, func(ctx context.Context, in *scopeUUIDParam) (*scopeOutput, error) {
        if svc == nil {
            return nil, huma.Error503ServiceUnavailable("share service unavailable")
        }
        caller, err := callerFromCtx(ctx)
        if err != nil {
            return nil, translateShareError(err)
        }
        s, err := svc.Retry(ctx, in.UUID, caller)
        if err != nil {
            return nil, translateShareError(err)
        }
        return &scopeOutput{Status: http.StatusOK, Body: toScopeDTO(s)}, nil
    })
}
```

`IdentityFromContext` already exists in the httpapi package; confirm
with `grep -n "func IdentityFromContext" internal/httpapi/*.go` and
reuse the existing signature. If the current package exports it under
a different name, adjust the callsite.

- [ ] **Step 9: Run the share-routes tests**

Run: `go test ./internal/httpapi/... -v`
Expected: PASS on all Shares* tests and the updated translator test.

- [ ] **Step 10: Regenerate OpenAPI**

Run: `make api-generate`
Expected: `internal/httpapi/openapi.go` (or the JSON-under-test file) updated with the new routes.

- [ ] **Step 11: Commit**

```bash
git add internal/httpapi/shares.go \
        internal/httpapi/shares_test.go \
        internal/httpapi/albums.go \
        internal/httpapi/albums_test.go \
        internal/httpapi/api.go \
        internal/httpapi/openapi.go
git commit -m "Add /api/v1/shares routes, translator, and OpenAPI regen"
```

---

## Task 17: CLI — `fotobank shares` subcommand tree

**Files:**
- Create: `internal/cli/shares.go`
- Create: `internal/cli/shares_test.go`
- Modify: `internal/cli/root.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/cli/shares_test.go`:

```go
package cli_test

import (
    "bytes"
    "context"
    "strings"
    "testing"

    "github.com/stretchr/testify/require"

    "github.com/wesm/fotobank/internal/cli"
)

func TestSharesCmdUsageErrorWithoutSub(t *testing.T) {
    r := require.New(t)
    root := cli.NewRoot()
    root.SetArgs([]string{"shares"})
    var out bytes.Buffer
    root.SetOut(&out)
    root.SetErr(&out)
    err := root.ExecuteContext(context.Background())
    r.Error(err)
    r.True(strings.Contains(out.String(), "a subcommand is required") ||
        strings.Contains(err.Error(), "a subcommand is required"))
}

func TestSharesCreateMissingFlags(t *testing.T) {
    r := require.New(t)
    root := cli.NewRoot()
    root.SetArgs([]string{"shares", "create"})
    var out bytes.Buffer
    root.SetOut(&out)
    root.SetErr(&out)
    err := root.ExecuteContext(context.Background())
    r.Error(err)
}
```

Add smoke tests for the parser here only; behavioural coverage of the
actual DB writes goes through the e2e test in Task 18.

- [ ] **Step 2: Run the failing tests**

Run: `go test ./internal/cli/... -run TestShares -v`
Expected: FAIL — `shares` subcommand not registered.

- [ ] **Step 3: Implement the subcommands**

Create `internal/cli/shares.go`:

```go
package cli

import (
    "context"
    "encoding/json"
    "errors"
    "fmt"
    "io"
    "os"
    "path/filepath"
    "strings"
    "text/tabwriter"
    "time"

    "github.com/spf13/cobra"

    "github.com/wesm/fotobank/internal/album"
    "github.com/wesm/fotobank/internal/config"
    "github.com/wesm/fotobank/internal/db"
    "github.com/wesm/fotobank/internal/media"
    "github.com/wesm/fotobank/internal/owners"
    "github.com/wesm/fotobank/internal/service"
    "github.com/wesm/fotobank/internal/share"
)

type shareCtx struct {
    svc    *service.ShareService
    caller owners.Principal
    close  func()
}

func loadShareCtx(cfgPath string) (*shareCtx, error) {
    path := cfgPath
    if path == "" {
        path = config.DefaultConfigPath()
    }
    cfg, err := config.Load(path)
    if err != nil {
        return nil, err
    }
    if cfg.Identity.Mode != "stub" {
        return nil, fmt.Errorf("fotobank shares requires identity.mode = stub (got %q)", cfg.Identity.Mode)
    }
    dbPath := os.Getenv("FOTOBANK_DB_PATH")
    if dbPath == "" {
        dbPath = filepath.Join(cfg.Flash.Root, "fotobank.sqlite")
    }
    d, err := db.Open(dbPath)
    if err != nil {
        return nil, err
    }
    return &shareCtx{
        svc: service.NewShareService(
            share.NewRepo(d.WriteDB(), d.ReadDB()),
            album.NewRepo(d.WriteDB(), d.ReadDB()),
            media.NewRepo(d.WriteDB(), d.ReadDB()),
        ),
        caller: owners.Principal{Hub: cfg.Identity.Stub.Hub, UserID: cfg.Identity.Stub.UserID},
        close:  func() { _ = d.Close() },
    }, nil
}

func newSharesCmd() *cobra.Command {
    cmd := &cobra.Command{
        Use:   "shares",
        Short: "Manage shares (scopes) for albums or media sets",
        Args:  usageArgs(cobra.NoArgs),
        RunE: func(cmd *cobra.Command, _ []string) error {
            _ = cmd.Usage()
            return newUsageError("a subcommand is required")
        },
    }
    cmd.AddCommand(newSharesCreateCmd())
    cmd.AddCommand(newSharesListCmd())
    cmd.AddCommand(newSharesShowCmd())
    cmd.AddCommand(newSharesRevokeCmd())
    cmd.AddCommand(newSharesRetryCmd())
    return cmd
}

// --- create ---

func newSharesCreateCmd() *cobra.Command {
    var (
        cfgPath       string
        albumID       string
        mediaCSV      string
        granteeRaw    string
        label         string
        allowDownload bool
        expires       string
    )
    cmd := &cobra.Command{
        Use:   "create",
        Short: "Create a scope over an album (--album) or media set (--media)",
        RunE: func(cmd *cobra.Command, args []string) error {
            return runSharesCreate(cmd.Context(), sharesCreateOpts{
                cfgPath:       cfgPath,
                albumID:       albumID,
                mediaCSV:      mediaCSV,
                granteeRaw:    granteeRaw,
                label:         label,
                allowDownload: allowDownload,
                expires:       expires,
                w:             cmd.OutOrStdout(),
            })
        },
    }
    cmd.Flags().StringVar(&cfgPath, "config", "", "config file path")
    cmd.Flags().StringVar(&albumID, "album", "", "album UUID (album_live target)")
    cmd.Flags().StringVar(&mediaCSV, "media", "", "comma-separated media UUIDs (media_set target)")
    cmd.Flags().StringVar(&granteeRaw, "grantee", "", "grantee principal in hub:user form (required)")
    cmd.Flags().StringVar(&label, "label", "", "optional label (<=200 chars)")
    cmd.Flags().BoolVar(&allowDownload, "allow-download", false, "grant download capability")
    cmd.Flags().StringVar(&expires, "expires", "", "optional RFC3339 expiry timestamp")
    _ = cmd.MarkFlagRequired("grantee")
    return cmd
}

type sharesCreateOpts struct {
    cfgPath, albumID, mediaCSV, granteeRaw, label, expires string
    allowDownload                                          bool
    w                                                      io.Writer
}

func runSharesCreate(ctx context.Context, o sharesCreateOpts) error {
    if (o.albumID == "") == (o.mediaCSV == "") {
        return newUsageError("exactly one of --album or --media is required")
    }
    grantee, err := parseHubUser(o.granteeRaw)
    if err != nil {
        return newUsageError(err.Error())
    }
    var expiresAt *time.Time
    if o.expires != "" {
        t, terr := time.Parse(time.RFC3339, o.expires)
        if terr != nil {
            return newUsageError("invalid --expires: " + terr.Error())
        }
        expiresAt = &t
    }
    sctx, err := loadShareCtx(o.cfgPath)
    if err != nil {
        return err
    }
    defer sctx.close()

    req := service.CreateShareRequest{
        Label:         o.label,
        Grantee:       grantee,
        AllowDownload: o.allowDownload,
        ExpiresAt:     expiresAt,
    }
    if o.albumID != "" {
        req.TargetType = share.TargetAlbumLive
        req.AlbumID = o.albumID
    } else {
        req.TargetType = share.TargetMediaSet
        req.MediaIDs = splitCSV(o.mediaCSV)
    }
    s, err := sctx.svc.Create(ctx, req, sctx.caller)
    if err != nil {
        return err
    }
    enc := json.NewEncoder(o.w)
    enc.SetIndent("", "  ")
    return enc.Encode(s)
}

// --- list / show / revoke / retry ---

func newSharesListCmd() *cobra.Command {
    var (
        cfgPath, albumID, granteeRaw, statusRaw string
        includeSettled                          bool
        asJSON                                  bool
        limit, offset                           int
    )
    cmd := &cobra.Command{
        Use:   "list",
        Short: "List scopes (default: owner-actionable only)",
        RunE: func(cmd *cobra.Command, args []string) error {
            return runSharesList(cmd.Context(), sharesListOpts{
                cfgPath:        cfgPath,
                albumID:        albumID,
                granteeRaw:     granteeRaw,
                statusRaw:      statusRaw,
                includeSettled: includeSettled,
                limit:          limit,
                offset:         offset,
                asJSON:         asJSON,
                w:              cmd.OutOrStdout(),
            })
        },
    }
    cmd.Flags().StringVar(&cfgPath, "config", "", "")
    cmd.Flags().StringVar(&albumID, "album", "", "filter by album_id")
    cmd.Flags().StringVar(&granteeRaw, "grantee", "", "hub:user filter")
    cmd.Flags().StringVar(&statusRaw, "status", "", "comma-separated broker_status filter")
    cmd.Flags().BoolVar(&includeSettled, "include-settled", false, "include revoked_remote rows")
    cmd.Flags().BoolVar(&asJSON, "json", false, "raw JSON instead of table")
    cmd.Flags().IntVar(&limit, "limit", 100, "max rows")
    cmd.Flags().IntVar(&offset, "offset", 0, "pagination offset")
    return cmd
}

type sharesListOpts struct {
    cfgPath, albumID, granteeRaw, statusRaw string
    includeSettled, asJSON                  bool
    limit, offset                           int
    w                                       io.Writer
}

func runSharesList(ctx context.Context, o sharesListOpts) error {
    sctx, err := loadShareCtx(o.cfgPath)
    if err != nil {
        return err
    }
    defer sctx.close()
    statuses, err := parseCLIStatusFilter(o.statusRaw)
    if err != nil {
        return newUsageError(err.Error())
    }
    filter := share.ScopeFilter{
        AlbumID:        o.albumID,
        Status:         statuses,
        IncludeSettled: o.includeSettled,
        Limit:          o.limit,
        Offset:         o.offset,
    }
    if o.granteeRaw != "" {
        g, gerr := parseHubUser(o.granteeRaw)
        if gerr != nil {
            return newUsageError(gerr.Error())
        }
        filter.Grantee = g
    }
    rows, err := sctx.svc.List(ctx, filter, sctx.caller)
    if err != nil {
        return err
    }
    if o.asJSON {
        enc := json.NewEncoder(o.w)
        enc.SetIndent("", "  ")
        return enc.Encode(rows)
    }
    tw := tabwriter.NewWriter(o.w, 0, 0, 2, ' ', 0)
    fmt.Fprintln(tw, "UUID\tSTATUS\tTARGET\tGRANTEE\tCREATED\tLAST ERROR")
    for _, s := range rows {
        target := string(s.TargetType)
        if s.TargetAlbumID != nil {
            target = "album " + shortUUID(*s.TargetAlbumID)
        }
        fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
            s.UUID, s.BrokerStatus, target, s.Grantee.String(),
            s.CreatedAt.Format(time.RFC3339), s.BrokerLastError)
    }
    return tw.Flush()
}

func newSharesShowCmd() *cobra.Command {
    var cfgPath string
    var asJSON bool
    cmd := &cobra.Command{
        Use:   "show <uuid>",
        Short: "Show a single scope (including media set membership)",
        Args:  usageArgs(cobra.ExactArgs(1)),
        RunE: func(cmd *cobra.Command, args []string) error {
            sctx, err := loadShareCtx(cfgPath)
            if err != nil {
                return err
            }
            defer sctx.close()
            det, err := sctx.svc.Get(cmd.Context(), args[0], sctx.caller)
            if err != nil {
                return err
            }
            enc := json.NewEncoder(cmd.OutOrStdout())
            enc.SetIndent("", "  ")
            if asJSON {
                return enc.Encode(det)
            }
            return enc.Encode(det) // for now the table-ish form is the same JSON
        },
    }
    cmd.Flags().StringVar(&cfgPath, "config", "", "")
    cmd.Flags().BoolVar(&asJSON, "json", true, "raw JSON")
    return cmd
}

func newSharesRevokeCmd() *cobra.Command {
    var cfgPath string
    cmd := &cobra.Command{
        Use:   "revoke <uuid>",
        Short: "Revoke a scope",
        Args:  usageArgs(cobra.ExactArgs(1)),
        RunE: func(cmd *cobra.Command, args []string) error {
            sctx, err := loadShareCtx(cfgPath)
            if err != nil {
                return err
            }
            defer sctx.close()
            s, err := sctx.svc.Revoke(cmd.Context(), args[0], sctx.caller)
            if errors.Is(err, share.ErrScopeAlreadyRevoked) {
                fmt.Fprintln(cmd.OutOrStdout(), "already revoked")
                return nil
            }
            if err != nil {
                return err
            }
            enc := json.NewEncoder(cmd.OutOrStdout())
            enc.SetIndent("", "  ")
            return enc.Encode(s)
        },
    }
    cmd.Flags().StringVar(&cfgPath, "config", "", "")
    return cmd
}

func newSharesRetryCmd() *cobra.Command {
    var cfgPath string
    cmd := &cobra.Command{
        Use:   "retry <uuid>",
        Short: "Retry a failed scope",
        Args:  usageArgs(cobra.ExactArgs(1)),
        RunE: func(cmd *cobra.Command, args []string) error {
            sctx, err := loadShareCtx(cfgPath)
            if err != nil {
                return err
            }
            defer sctx.close()
            s, err := sctx.svc.Retry(cmd.Context(), args[0], sctx.caller)
            if err != nil {
                return err
            }
            enc := json.NewEncoder(cmd.OutOrStdout())
            enc.SetIndent("", "  ")
            return enc.Encode(s)
        },
    }
    cmd.Flags().StringVar(&cfgPath, "config", "", "")
    return cmd
}

// --- helpers ---

func parseHubUser(raw string) (owners.Principal, error) {
    i := strings.IndexByte(raw, ':')
    if i <= 0 || i == len(raw)-1 {
        return owners.Principal{}, fmt.Errorf("invalid hub:user %q", raw)
    }
    return owners.Principal{Hub: raw[:i], UserID: raw[i+1:]}, nil
}

func splitCSV(raw string) []string {
    if raw == "" {
        return nil
    }
    parts := strings.Split(raw, ",")
    out := make([]string, 0, len(parts))
    for _, p := range parts {
        p = strings.TrimSpace(p)
        if p != "" {
            out = append(out, p)
        }
    }
    return out
}

func shortUUID(s string) string {
    if len(s) <= 8 {
        return s
    }
    return s[:4] + ".." + s[len(s)-4:]
}

func parseCLIStatusFilter(raw string) ([]share.BrokerStatus, error) {
    if raw == "" {
        return nil, nil
    }
    parts := strings.Split(raw, ",")
    out := make([]share.BrokerStatus, 0, len(parts))
    for _, p := range parts {
        p = strings.TrimSpace(p)
        if p == "" {
            continue
        }
        switch share.BrokerStatus(p) {
        case share.StatusPending, share.StatusActive, share.StatusFailed,
            share.StatusRevoking, share.StatusRevokedRemote:
            out = append(out, share.BrokerStatus(p))
        default:
            return nil, fmt.Errorf("unknown status: %s", p)
        }
    }
    return out, nil
}
```

- [ ] **Step 4: Register the subcommand**

Edit `internal/cli/root.go`. Find the existing `AddCommand` section
(alongside `newAlbumsCmd()`) and add:

```go
cmd.AddCommand(newSharesCmd())
```

- [ ] **Step 5: Run the CLI tests**

Run: `go test ./internal/cli/... -v`
Expected: PASS (shares usage / flag errors; existing tests unchanged).

- [ ] **Step 6: Commit**

```bash
git add internal/cli/shares.go internal/cli/shares_test.go internal/cli/root.go
git commit -m "Add fotobank shares CLI subcommand tree"
```

---

## Task 18: Server wiring — plumb `ShareService` + `shareworker.Worker`

**Files:**
- Modify: `internal/cli/server.go`
- Modify: `internal/cli/e2e_test.go`

- [ ] **Step 1: Add an end-to-end test**

Append to `internal/cli/e2e_test.go`:

```go
func TestE2ESharesRoundTrip(t *testing.T) {
    r := require.New(t)
    env := newServerEnv(t)
    defer env.close()

    // 1) Create an album, add a media row (use the existing E2E flow
    //    helpers — reuse newServerEnv's media and album helpers).
    albumID := env.createAlbumWithOneMedia(t, "trip")

    // 2) POST /api/v1/shares with album_live.
    body, _ := json.Marshal(map[string]any{
        "grantee":     map[string]string{"hub": "h", "user_id": "alice"},
        "target_type": "album_live",
        "album_id":    albumID,
    })
    resp, err := env.client.Post(env.url("/api/v1/shares"), "application/json", bytes.NewReader(body))
    r.NoError(err)
    r.Equal(http.StatusCreated, resp.StatusCode)
    var scope struct {
        UUID         string `json:"uuid"`
        BrokerStatus string `json:"broker_status"`
    }
    r.NoError(json.NewDecoder(resp.Body).Decode(&scope))
    r.Equal("pending", scope.BrokerStatus)

    // 3) Poll until the NoopBroker-backed worker flips it to active.
    r.Eventually(func() bool {
        resp, err := env.client.Get(env.url("/api/v1/shares/" + scope.UUID))
        if err != nil || resp.StatusCode != http.StatusOK {
            return false
        }
        var det map[string]any
        _ = json.NewDecoder(resp.Body).Decode(&det)
        return det["broker_status"] == "active"
    }, 5*time.Second, 50*time.Millisecond)

    // 4) Album delete is blocked while the scope is active.
    req, _ := http.NewRequest(http.MethodDelete, env.url("/api/v1/albums/"+albumID), nil)
    resp, err = env.client.Do(req)
    r.NoError(err)
    r.Equal(http.StatusConflict, resp.StatusCode)

    // 5) Revoke the scope, wait for revoked_remote, then delete album.
    req, _ = http.NewRequest(http.MethodPost, env.url("/api/v1/shares/"+scope.UUID+"/revoke"), nil)
    resp, err = env.client.Do(req)
    r.NoError(err)
    r.Equal(http.StatusOK, resp.StatusCode)
    r.Eventually(func() bool {
        resp, err := env.client.Get(env.url("/api/v1/shares/" + scope.UUID + "?" + "include_settled=true"))
        if err != nil || resp.StatusCode != http.StatusOK {
            return false
        }
        var det map[string]any
        _ = json.NewDecoder(resp.Body).Decode(&det)
        return det["broker_status"] == "revoked_remote"
    }, 5*time.Second, 50*time.Millisecond)

    req, _ = http.NewRequest(http.MethodDelete, env.url("/api/v1/albums/"+albumID), nil)
    resp, err = env.client.Do(req)
    r.NoError(err)
    r.True(resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNoContent,
        "expected 200/204, got %d", resp.StatusCode)
}
```

The `newServerEnv`, `env.createAlbumWithOneMedia`, `env.client`,
`env.url`, and `env.close` helpers already exist in
`internal/cli/e2e_test.go`. If `createAlbumWithOneMedia` doesn't
exist by that name, factor it from existing Plan D e2e helpers. The
intent is: seed an album with exactly one media ID via the HTTP API.

- [ ] **Step 2: Run the failing test**

Run: `go test ./internal/cli/... -run TestE2EShares -v`
Expected: FAIL — server doesn't yet wire `ShareService` or the worker.

- [ ] **Step 3: Wire `ShareService` into the server handler**

Edit `internal/cli/server.go`. Right after the existing
`albumSvc := ...` block (the one modified in Task 15, which already
constructs `sharesRepo`), construct and wire the share service:

```go
shareSvc := service.NewShareService(
    sharesRepo,
    album.NewRepo(d.WriteDB(), d.ReadDB()),
    media.NewRepo(d.WriteDB(), d.ReadDB()),
)
```

Update the `httpapi.New(httpapi.Deps{...})` call to include
`ShareService: shareSvc`.

- [ ] **Step 4: Wire the `shareworker.Worker`**

Still in `internal/cli/server.go`, after thumb worker wiring, add:

```go
shareW := shareworker.New(shareworker.Config{
    Repo:   sharesRepo,
    Broker: broker.NoopBroker{},
})
```

Import additions: `"github.com/wesm/fotobank/internal/broker"`,
`"github.com/wesm/fotobank/internal/shareworker"`.

Find the existing errgroup/waitgroup that runs the thumb worker (look
for `thumbWorker.Run(ctx)` or similar). Add a sibling goroutine:

```go
g.Go(func() error {
    if err := shareW.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
        return err
    }
    return nil
})
```

If the existing file uses a plain `sync.WaitGroup` + channel instead
of an errgroup, mirror the same pattern — see `thumbWorker.Run`'s
goroutine for the canonical shape.

- [ ] **Step 5: Run the full test suite**

Run: `make test`
Expected: PASS across all packages, including the new e2e test.

- [ ] **Step 6: Run lint and nilaway**

Run: `make lint`
Expected: PASS.

Run: `make nilaway`
Expected: PASS.

- [ ] **Step 7: Regenerate OpenAPI one last time (if dirty)**

Run: `make api-generate`
Expected: no-op if Task 16's generate already captured the final shape; otherwise commit the refresh.

- [ ] **Step 8: Commit**

```bash
git add internal/cli/server.go internal/cli/e2e_test.go
[ -n "$(git status --porcelain internal/httpapi/openapi.go)" ] && git add internal/httpapi/openapi.go
git commit -m "Wire ShareService and shareworker.Worker into fotobank server"
```

---

## Self-review

**Spec coverage.**
- §1 Goal/non-goals — covered by Tasks 2–18; non-goals stay out of scope.
- §2 Prior art — reused verbatim (scopes + scope_media + triggers); no change.
- §3 Architecture / package layout — realised in Tasks 2–4 (packages), 5–9 (repo), 10 (album.Repo tx), 11–12 (worker), 13–14 (service), 15 (album integration), 16 (HTTP), 17 (CLI), 18 (server).
- §4 Domain types — Task 2.
- §4.1 State machine — Task 7 (publish side), Task 8 (revoke/retry side); worker-level transitions Task 12.
- §5 Migration — Task 1.
- §6 BrokerClient — Task 3 (interface + NoopBroker + register/grant rationale), Task 4 (Fake).
- §7 Repo surface (every method) — Tasks 5 (`Insert`, `GetByUUID`), 6 (`ListByOwner`, `ListReady`), 7 (`MarkPublished`, `MarkAttemptFailed`, `MarkFailed`), 8 (`SetRevoking`, `MarkRevoked`, `RetryPublish`, `RetryRevoke`), 9 (`PrepareAlbumDeleteTx`, `HasBlockingScopesForAlbum`).
- §8 ShareService — Task 13 (Create + validation + §8.1 order + §8.2 grantee rules), Task 14 (Get/List/Revoke/Retry), Task 15 (album-delete coupling per §8.3).
- §9 HTTP — Task 16 (DTOs, all 5 routes, translator, OpenAPI, translateAlbumError extension).
- §10 Worker — Tasks 11 (Backoff), 12 (Worker + RunOnce + Run + single-worker assumption).
- §11 CLI — Task 17.
- §12 Tests — covered inline in each task.
- §13 Open questions — deferred items are not implemented by design.

**Placeholder scan.** Searched for "TBD", "TODO", "later", "similar to", empty steps: none present except the explicit non-goals list in the header section which is spec-level, not plan-level.

**Type consistency.** `share.Scope`, `share.ScopeDetail`, `share.ScopeFilter`, `share.BrokerStatus`, `share.TargetType`, `share.Repo.*`, `shareworker.Backoff`, `broker.BrokerClient`, `broker.NoopBroker`, `brokertest.Fake` match across every task.  Method names `MarkPublished`, `MarkAttemptFailed`, `MarkFailed`, `SetRevoking`, `MarkRevoked`, `RetryPublish`, `RetryRevoke`, `PrepareAlbumDeleteTx`, `HasBlockingScopesForAlbum`, `DeleteTx` are consistent across §7 of the spec, the repo tests, and the worker.  `CreateShareRequest` fields line up between service, HTTP, and CLI. `ScopeFilter` default semantics (empty slice + !IncludeSettled hides only `revoked_remote`) is consistent across repo, service, HTTP, CLI.

---

Plan complete and saved to `docs/superpowers/plans/2026-04-23-fotobank-plan-e1-sharing-owner-side.md`. Two execution options:

**1. Subagent-Driven (recommended)** — I dispatch a fresh subagent per task, review between tasks, fast iteration.

**2. Inline Execution** — Execute tasks in this session using executing-plans, batch execution with checkpoints.

Which approach?
