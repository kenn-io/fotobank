# F2.4 Hidden Privacy Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship app-level Hidden privacy: server-enforced hidden filtering, passcode unlock sessions, CLI recovery/setup tools, `/hidden` SPA route, Hide/Unhide actions, album hidden counts, and e2e coverage.

**Architecture:** Hidden is an owner-only app privacy layer, not encryption. The backend stores `media.hidden_at`, validates opaque unlock-cookie sessions in middleware, and keeps filtering decisions in service/repo methods so byte routes, list routes, albums, and grantee shared reads each enforce their own boundary. The frontend keeps visible Library state and route-local Hidden state separate; shared `MediaStore` may cache hidden rows by id but never inserts them into visible month groups while `hidden_at != null`.

**Tech Stack:** Go 1.x, modernc.org/sqlite, huma/v2, cobra, slog, `golang.org/x/crypto/argon2`, Svelte 5 runes, TypeScript, Vitest, Playwright, Bun, OpenAPI codegen.

**Spec:** `docs/superpowers/specs/2026-04-29-fotobank-web-f2.4-hidden-privacy-design.md`

---

## File Map

**Create**

- `internal/auth/hidden/repo.go` — SQLite repo for hidden credentials, sessions, failures, and lockouts.
- `internal/auth/hidden/repo_test.go`
- `internal/auth/hidden/service.go` — passcode hashing, unlock token lifecycle, lockout logic, setup/change/disable/admin reset.
- `internal/auth/hidden/service_test.go`
- `internal/auth/hidden/cookie.go` — cookie name/options, token generation/hash helpers, context claim helpers if kept outside httpapi.
- `internal/auth/hidden/cookie_test.go`
- `internal/httpapi/hidden.go` — `/api/v1/auth/hidden/*`, `/api/v1/hidden/media`, `/api/v1/media:hidden`, `/api/v1/media:unhide`.
- `internal/httpapi/hidden_test.go`
- `internal/cli/hidden.go` — `fotobank hidden setup|change|disable` and `fotobank admin reset-hidden-passcode`.
- `internal/cli/hidden_test.go`
- `frontend/src/lib/hidden/hiddenStore.svelte.ts`
- `frontend/src/lib/hidden/hiddenStore.test.ts`
- `frontend/src/lib/hidden/hiddenMediaStore.svelte.ts`
- `frontend/src/lib/hidden/hiddenMediaStore.test.ts`
- `frontend/src/lib/components/HiddenGate.svelte`
- `frontend/src/lib/components/HiddenGate.test.ts`
- `frontend/src/lib/components/HiddenLockStrip.svelte`
- `frontend/src/lib/components/HiddenLockStrip.test.ts`
- `frontend/src/lib/components/ToastStack.svelte`
- `frontend/src/lib/components/ToastStack.test.ts`
- `frontend/src/routes/HiddenLibrary.svelte`
- `frontend/src/routes/HiddenLibrary.test.ts`
- `frontend/tests/e2e/hidden.spec.ts`

**Modify**

- `internal/db/migrations/000001_initial_schema.up.sql` — add `media.hidden_at`, visible index, hidden auth tables/indexes.
- `internal/db/migrations/000001_initial_schema.down.sql` — matching prior-state snapshot.
- `internal/config/config.go`, `internal/config/config.example.toml`, `internal/config/config_test.go` — `http.dev_insecure_cookies`.
- `go.mod`, `go.sum` — ensure `golang.org/x/crypto/argon2` is available.
- `internal/errs/errs.go`, `internal/httpapi/errors.go`, `internal/httpapi/errors_test.go` — new sentinel(s) for hidden not configured / lockout if needed.
- `internal/httpapi/api.go`, `internal/httpapi/middleware.go`, `internal/httpapi/middleware_test.go` — hidden service deps, unlock-cookie middleware, claim accessors.
- `internal/cli/root.go`, `internal/cli/server.go`, `cmd/e2e-server/main.go` — command/server wiring and e2e seed fixtures.
- `internal/media/media.go`, `internal/media/repo.go`, `internal/media/repo_test.go` — `HiddenAt`, hidden-aware projections/list/get/cascade/list-hidden.
- `internal/service/media_service.go`, `internal/service/media_service_test.go` — Hide/Unhide, IncludeHidden owner reads, hidden direct access.
- `internal/service/thumb_service.go`, `internal/service/thumb_service_test.go` — hidden-aware owner thumb reads.
- `internal/httpapi/media.go`, `internal/httpapi/media_test.go` — `hidden_at` DTO, detail/list filtering, unlocked direct detail.
- `internal/httpapi/media_original.go`, `internal/httpapi/media_original_test.go` — unlocked direct original access, 404 when locked.
- `internal/httpapi/media_thumb.go`, `internal/httpapi/media_thumb_test.go` — unlocked direct thumb access, 404 when locked.
- `internal/service/shared_read_service.go`, `internal/service/shared_read_service_test.go`, `internal/httpapi/shared*.go`, `internal/httpapi/shared*_test.go` — grantee filtering.
- `internal/album/album.go`, `internal/album/repo.go`, `internal/album/repo_test.go` — `HiddenCount`, visible `ItemCount`, hidden-aware cover/list/media count.
- `internal/service/album_service.go`, `internal/service/album_service_test.go` — album add hidden-id unlock carve-out.
- `internal/httpapi/albums.go`, `internal/httpapi/albums_test.go` — `hidden_count` DTO and unlock-aware add-media.
- `openapi.json`, `frontend/src/lib/api/generated/schema.ts` — regenerated after HTTP shape changes.
- `frontend/src/lib/media/mediaStore.svelte.ts`, `frontend/src/lib/media/mediaStore.test.ts` — `hidden_at`, hidden-aware merge, `removeMany`.
- `frontend/src/lib/components/MediaActions.svelte`, `frontend/src/lib/components/MediaActions.test.ts` — Hide/Unhide contexts.
- `frontend/src/lib/components/Sidebar.svelte`, `frontend/src/lib/components/Sidebar.test.ts` — Hidden entry in BROWSE.
- `frontend/src/lib/router/router.svelte.ts`, `frontend/src/lib/router/router.test.ts` — `/hidden`, `back(fallback)`.
- `frontend/src/App.svelte` — construct HiddenStore, route `/hidden`, top strip, visibilitychange lock.
- `frontend/src/routes/Library.svelte`, `frontend/src/routes/Sessions.svelte`, `frontend/src/routes/AlbumDetail.svelte`, `frontend/src/routes/MediaDetail.svelte`, `frontend/src/routes/AlbumsIndex.svelte` — action wiring, chip rendering, refresh hooks.
- `frontend/src/lib/albums/albumsStore.svelte.ts`, `frontend/src/lib/albums/albumsStore.test.ts` — `hidden_count`, `markStale`.
- `frontend/src/lib/albums/albumDetailStore.svelte.ts`, `frontend/src/lib/albums/albumDetailStore.test.ts` — `hidden_count`, `refreshMeta`, `pruneHidden`.

**Do not create**

- New migration files. F2.4 follows pre-prod policy and edits `000001_initial_schema` in place.
- Settings UI for setup/change/disable.
- Search hidden toggles.
- SSE/cross-tab lock fanout.
- Unhide-and-share flow.

---

## Cross-Cutting Contracts

**Hidden predicates**

- Visible list surfaces always apply `hidden_at IS NULL`; unlock cookie is irrelevant for `/library`, `/albums`, `/albums/{id}/media`.
- Direct owner reads of a hidden id (`GET /media/{id}`, `/thumb`, `/original`) return 200 only with a valid unlock cookie; otherwise 404.
- `/hidden/media` and `/media:unhide` require a valid unlock cookie and return 403 without it.
- `/media:hidden` requires Hidden configured but no unlock cookie; return 409 `hidden_not_configured` when no credential exists.
- Shared grantee reads always apply `hidden_at IS NULL`; owner unlock cookie is never honored.

**Status codes**

- 401: principal resolution failure only.
- 403: wrong passcode or missing/expired unlock cookie on unlock-required routes.
- 404: direct-by-id hidden read without unlock cookie, same shape as missing media.
- 409: Hidden not configured on `POST /api/v1/media:hidden`.
- 429: lockout, with `Retry-After`.

**Bulk response**

```json
{
  "succeeded": ["id1"],
  "failed": [
    {"id": "id2", "code": "not_found"},
    {"id": "id3", "code": "invalid_sidecar"}
  ]
}
```

`not_found` covers missing and cross-owner ids.

**Cookie names**

- Prod: `__Host-fotobank-hidden`, `HttpOnly; Secure; SameSite=Strict; Path=/`.
- Dev/e2e: `fotobank-hidden`, `HttpOnly; SameSite=Strict; Path=/`, no `Secure`.
- Config flag: `http.dev_insecure_cookies`, default `false`.

**Argon2id**

Use `m=19456 KiB, t=2, p=1`, salt 16 bytes, hash 32 bytes. Do not make these configurable in F2.4.

---

## Task 1: Schema Fold, Config Flag, and Media Hidden Field

**Files:**
- Modify: `internal/db/migrations/000001_initial_schema.up.sql`
- Modify: `internal/db/migrations/000001_initial_schema.down.sql`
- Modify: `internal/media/media.go`
- Modify: `internal/media/repo.go`
- Modify: `internal/media/repo_test.go`
- Modify: `internal/album/repo.go`
- Modify: `internal/config/config.go`
- Modify: `internal/config/config.example.toml`
- Modify: `internal/config/config_test.go`

- [ ] **Step 1: Write schema/projection tests first**

Add media repo tests that prove `hidden_at` scans through `GetByID`, inserts correctly through `mediaInsert`, and is included in the projection drift coverage. Do not add hidden filtering assertions yet; those belong to Task 6 when the repo filter is introduced. Extend the existing projection drift tests in `internal/media/repo_test.go` rather than adding a parallel mechanism.

Test skeleton:

```go
func TestRepoGetByIDScansHiddenAt(t *testing.T) {
    d := testutil.OpenTestDB(t)
    defer d.Close()
    repo := media.NewRepo(d.WriteDB(), d.ReadDB())
    owner := owners.Principal{Hub: "h", UserID: "u"}
    seedOwner(t, d.WriteDB(), owner, "sk")

    hiddenAt := time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC)
    id := seedMediaWithHiddenAt(t, d.WriteDB(), owner, hiddenAt)

    got, err := repo.GetByID(context.Background(), id)
    require.NoError(t, err)
    require.NotNil(t, got.HiddenAt)
    require.True(t, got.HiddenAt.Equal(hiddenAt))
}
```

Add config tests:

```go
func TestHTTPDevInsecureCookiesDefaultsFalse(t *testing.T) {
    cfg := loadMinimalConfigForTest(t, "")
    require.False(t, cfg.HTTP.DevInsecureCookies)
}
```

- [ ] **Step 2: Run focused tests and confirm failure**

Run:

```bash
go test ./internal/media ./internal/config
```

Expected: fail because `hidden_at`, config field, and scan plumbing do not exist.

- [ ] **Step 3: Edit the initial schema in place**

In `media` table, add:

```sql
hidden_at TIMESTAMP,
```

Add indexes/tables after existing media indexes:

```sql
CREATE INDEX media_visible_idx
    ON media(owner_hub, owner_user_id, timestamp DESC)
    WHERE hidden_at IS NULL;

CREATE TABLE auth_hidden_credential (
    principal_hub      TEXT NOT NULL,
    principal_user_id  TEXT NOT NULL,
    passcode_hash      TEXT NOT NULL,
    created_at         TIMESTAMP NOT NULL,
    updated_at         TIMESTAMP NOT NULL,
    PRIMARY KEY (principal_hub, principal_user_id),
    FOREIGN KEY (principal_hub, principal_user_id)
        REFERENCES owners(hub, user_id)
);

CREATE TABLE auth_hidden_session (
    token_sha256       BLOB NOT NULL PRIMARY KEY,
    principal_hub      TEXT NOT NULL,
    principal_user_id  TEXT NOT NULL,
    issued_at          TIMESTAMP NOT NULL,
    expires_at         TIMESTAMP NOT NULL,
    revoked_at         TIMESTAMP,
    FOREIGN KEY (principal_hub, principal_user_id)
        REFERENCES owners(hub, user_id)
);
CREATE INDEX auth_hidden_session_principal_active_idx
    ON auth_hidden_session(principal_hub, principal_user_id)
    WHERE revoked_at IS NULL;
CREATE INDEX auth_hidden_session_expiry_idx
    ON auth_hidden_session(expires_at)
    WHERE revoked_at IS NULL;

CREATE TABLE auth_hidden_failure (
    principal_hub      TEXT NOT NULL,
    principal_user_id  TEXT NOT NULL,
    occurred_at        TIMESTAMP NOT NULL,
    FOREIGN KEY (principal_hub, principal_user_id)
        REFERENCES owners(hub, user_id)
);
CREATE INDEX auth_hidden_failure_owner_idx
    ON auth_hidden_failure(principal_hub, principal_user_id, occurred_at DESC);

CREATE TABLE auth_hidden_lockout (
    principal_hub      TEXT NOT NULL,
    principal_user_id  TEXT NOT NULL,
    locked_until       TIMESTAMP NOT NULL,
    updated_at         TIMESTAMP NOT NULL,
    PRIMARY KEY (principal_hub, principal_user_id),
    FOREIGN KEY (principal_hub, principal_user_id)
        REFERENCES owners(hub, user_id)
);
```

Update `000001_initial_schema.down.sql` as the matching prior-state snapshot.

- [ ] **Step 4: Update media structs and projections**

In `internal/media/media.go`:

```go
HiddenAt *time.Time
```

Add `hidden_at` to all four projection/write sites:

- `mediaSelect`
- `mediaColumnsQualified`
- `mediaInsert`
- `internal/album/repo.go::albumMediaMediaSelect`

Update `scanMedia` with `sql.NullTime`; update `Insert` args with `nullTime(m.HiddenAt)`.

- [ ] **Step 5: Add config flag**

In `internal/config/config.go`:

```go
type HTTP struct {
    ListenAddress      string        `toml:"listen_address"`
    BaseURL            string        `toml:"base_url"`
    RequestTimeout     time.Duration `toml:"request_timeout"`
    WriteTimeout       time.Duration `toml:"write_timeout"`
    CORSOrigins        []string      `toml:"cors_origins"`
    DevInsecureCookies bool          `toml:"dev_insecure_cookies"`
}
```

In `config.example.toml`, add under `[http]`:

```toml
# Development/e2e only: allow the Hidden unlock cookie over HTTP loopback.
dev_insecure_cookies = false
```

- [ ] **Step 6: Run tests**

Run:

```bash
go test ./internal/db ./internal/media ./internal/album ./internal/config
```

Expected: pass.

- [ ] **Step 7: Commit**

```bash
git add internal/db/migrations/000001_initial_schema.up.sql internal/db/migrations/000001_initial_schema.down.sql internal/media internal/album internal/config
git commit -m "feat(hidden): add schema and media hidden field"
```

---

## Task 2: Hidden Auth Repository

**Files:**
- Create: `internal/auth/hidden/repo.go`
- Create: `internal/auth/hidden/repo_test.go`

- [ ] **Step 1: Write failing repo tests**

Cover:

- credential get/upsert/delete
- session insert/lookup-active/revoke/revoke-all/sweep
- failure insert/count-recent/purge-old
- lockout get/upsert/delete

Representative test:

```go
func TestRepoLookupActiveSessionRejectsExpiredAndRevoked(t *testing.T) {
    d := testutil.OpenTestDB(t)
    defer d.Close()
    repo := hidden.NewRepo(d.WriteDB(), d.ReadDB())
    p := owners.Principal{Hub: "h", UserID: "u"}
    seedOwner(t, d.WriteDB(), p, "sk")
    now := time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC)
    tok := sha256.Sum256([]byte("token"))

    require.NoError(t, repo.InsertSession(context.Background(), hidden.Session{
        TokenSHA256: tok[:], Principal: p, IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Minute),
    }))
    got, err := repo.LookupActiveSession(context.Background(), tok[:], now)
    require.NoError(t, err)
    require.Equal(t, p, got.Principal)

    require.NoError(t, repo.RevokeSession(context.Background(), tok[:], now))
    _, err = repo.LookupActiveSession(context.Background(), tok[:], now)
    require.ErrorIs(t, err, errs.ErrNotFound)
}
```

- [ ] **Step 2: Run tests and confirm failure**

```bash
go test ./internal/auth/hidden
```

Expected: fail because package does not exist.

- [ ] **Step 3: Implement repo and types**

Create types:

```go
type Credential struct {
    Principal owners.Principal
    PasscodeHash string
    CreatedAt time.Time
    UpdatedAt time.Time
}

type Session struct {
    TokenSHA256 []byte
    Principal owners.Principal
    IssuedAt time.Time
    ExpiresAt time.Time
    RevokedAt *time.Time
}

type Lockout struct {
    Principal owners.Principal
    LockedUntil time.Time
    UpdatedAt time.Time
}
```

Implement methods listed in the spec, returning `errs.ErrNotFound` for misses. Use `rw` for writes and `ro` for reads, matching repo patterns elsewhere.

- [ ] **Step 4: Run tests**

```bash
go test ./internal/auth/hidden
```

Expected: pass.

- [ ] **Step 5: Commit**

```bash
git add internal/auth/hidden
git commit -m "feat(hidden): add auth repository"
```

---

## Task 3: Hidden Auth Service, Argon2id, Tokens, and Lockout

**Files:**
- Modify: `internal/auth/hidden/service.go`
- Modify: `internal/auth/hidden/service_test.go`
- Modify: `internal/auth/hidden/cookie.go`
- Modify: `internal/auth/hidden/cookie_test.go`
- Modify: `go.mod`, `go.sum`

- [ ] **Step 1: Write failing service tests**

Cover:

- setup stores credential and rejects duplicate setup
- passcode length is enforced by byte length before hashing
- unlock returns 32-byte token and expiry
- wrong passcode inserts failure row
- five failures in 60s creates 5-minute lockout
- active lockout returns retry-after
- successful unlock/change/disable clears failure rows and lockout row
- change revokes all sessions
- disable clears all hidden flags through media repo collaborator
- admin reset deletes credential and revokes sessions without touching hidden flags
- sweep revokes expired sessions and purges failures older than the lockout window

Use a fake media-clear collaborator for `Disable` so auth service tests do not need media repo.

- [ ] **Step 2: Run tests and confirm failure**

```bash
go test ./internal/auth/hidden
```

Expected: fail due missing service.

- [ ] **Step 3: Implement hash encoding helpers**

Use `golang.org/x/crypto/argon2`. Store one encoded string in `auth_hidden_credential.passcode_hash`:

```text
argon2id$m=19456,t=2,p=1$<base64url-salt>$<base64url-hash>
```

Implement:

```go
func HashPasscode(passcode string) (string, error)
func VerifyPasscode(encoded, passcode string) (bool, error)
func validatePasscode(passcode string) error
```

Use `crypto/subtle.ConstantTimeCompare` for hash comparison.

- [ ] **Step 4: Implement service**

Constructor shape:

```go
type MediaPrivacy interface {
    ClearAllHiddenForOwner(ctx context.Context, owner owners.Principal) error
}

type Service struct {
    repo *Repo
    media MediaPrivacy
    now func() time.Time
    rand io.Reader
    ttl time.Duration
    failureWindow time.Duration
    lockoutDuration time.Duration
}
```

Default `ttl=5*time.Minute`, `failureWindow=time.Minute`, `lockoutDuration=5*time.Minute`.

Expose test hooks only if needed:

```go
func NewService(repo *Repo, media MediaPrivacy) *Service
func (s *Service) SetClockForTest(func() time.Time)
func (s *Service) SetLockoutForTest(window, duration time.Duration)
```

Expose one sweeper entry point for the server ticker:

```go
func (s *Service) Sweep(ctx context.Context) error
```

`Sweep` revokes expired sessions and purges failure rows older than the sliding-window horizon. It must not delete active lockout rows; successful passcode verification owns lockout reset.

- [ ] **Step 5: Implement token helpers**

```go
func NewToken(r io.Reader) (raw string, sha []byte, err error)
```

Raw token is `base64.RawURLEncoding` over 32 random bytes. Store SHA-256 bytes.

- [ ] **Step 6: Run tests**

```bash
go test ./internal/auth/hidden
```

Expected: pass.

- [ ] **Step 7: Commit**

```bash
git add internal/auth/hidden go.mod go.sum
git commit -m "feat(hidden): add passcode service"
```

---

## Task 4: HTTP Unlock Middleware and Cookie Options

**Files:**
- Modify: `internal/auth/hidden/cookie.go`
- Modify: `internal/auth/hidden/cookie_test.go`
- Modify: `internal/httpapi/api.go`
- Modify: `internal/httpapi/middleware.go`
- Modify: `internal/httpapi/middleware_test.go`

- [ ] **Step 1: Write failing middleware/cookie tests**

Test cases:

- prod cookie name is `__Host-fotobank-hidden`, Secure, Path `/`, SameSite Strict, HttpOnly
- dev cookie name is `fotobank-hidden`, no Secure
- missing cookie attaches no claim and does not reject request
- valid cookie attaches claim for same principal
- expired/revoked/wrong-principal cookie attaches no claim

- [ ] **Step 2: Run tests and confirm failure**

```bash
go test ./internal/auth/hidden ./internal/httpapi
```

Expected: fail.

- [ ] **Step 3: Add claim context helpers**

Either in `internal/auth/hidden` or `internal/httpapi`, implement:

```go
type UnlockClaim struct {
    Principal owners.Principal
    ExpiresAt time.Time
}

func WithUnlockClaim(ctx context.Context, claim UnlockClaim) context.Context
func UnlockClaimFromContext(ctx context.Context) (UnlockClaim, bool)
```

- [ ] **Step 4: Add middleware wiring**

Extend `httpapi.Deps`:

```go
HiddenAuth *hidden.Service
DevInsecureHiddenCookies bool
```

In `httpapi.New`, apply wrappers inside-out: start with the mux, apply `WithPrincipalDisplayCache` when configured, then apply `WithHiddenUnlock`, then apply the outer `WithMiddleware` identity/metrics wrapper. That makes the final request order:

```text
metrics → recovery → identity → hidden-unlock → display-cache → mux
```

The hidden middleware reads `IdentityFromContext`, hashes the cookie token, calls the hidden service/repo lookup, and attaches the claim only on success.

- [ ] **Step 5: Keep runtime server wiring deferred**

Do not instantiate the hidden service in `internal/cli/server.go` in this task. The real service wiring depends on `MediaService.ClearAllHiddenForOwner`, which lands with the media privacy service work. Middleware tests should inject a hidden service built with a fake `MediaPrivacy` collaborator.

- [ ] **Step 6: Run tests**

```bash
go test ./internal/auth/hidden ./internal/httpapi
```

Expected: pass.

- [ ] **Step 7: Commit**

```bash
git add internal/auth/hidden internal/httpapi
git commit -m "feat(hidden): wire unlock cookie middleware"
```

---

## Task 5: Auth HTTP Routes

**Files:**
- Create: `internal/httpapi/hidden.go`
- Create/modify: `internal/httpapi/hidden_test.go`
- Modify: `internal/httpapi/api.go`
- Modify: `openapi.json`
- Modify: `frontend/src/lib/api/generated/schema.ts`

- [ ] **Step 1: Write failing HTTP tests**

Cover:

- `/state` false when not configured
- `/setup` 204 then `/state` configured true
- duplicate `/setup` returns 409
- `/unlock` wrong passcode returns 403 and no cookie
- `/unlock` correct passcode returns 204 and Set-Cookie
- `/change` verifies old passcode and revokes sessions
- `/disable` clears credential and hidden flags
- `/lock` always returns 204 and clears cookie
- 5 wrong attempts returns 429 with `Retry-After`

- [ ] **Step 2: Run tests and confirm failure**

```bash
go test ./internal/httpapi -run Hidden
```

Expected: fail.

- [ ] **Step 3: Register routes**

Add `registerHiddenAuth(api, deps.HiddenAuth, cookieConfig)` and call from `buildAPI`.

Register the operations even when `deps.HiddenAuth == nil`, returning 503 from handlers in that case. This matches the existing album/share pattern and keeps `OpenAPISpec` complete when it calls `buildAPI(Deps{})`.

Define request/response shapes:

```go
type hiddenPasscodeInput struct {
    Body struct {
        Passcode string `json:"passcode" minLength:"1" maxLength:"1024"`
    }
}

type hiddenChangeInput struct {
    Body struct {
        OldPasscode string `json:"old_passcode" minLength:"1" maxLength:"1024"`
        NewPasscode string `json:"new_passcode" minLength:"1" maxLength:"1024"`
    }
}

type hiddenStateOutput struct {
    Body struct {
        Configured bool `json:"configured"`
        Unlocked bool `json:"unlocked"`
        ExpiresAt *time.Time `json:"expires_at,omitempty"`
    }
}
```

Map service errors directly in this handler so passcode errors return 403, lockout returns 429, and configured conflicts return 409.

- [ ] **Step 4: Run focused tests**

```bash
go test ./internal/httpapi -run Hidden
```

Expected: pass.

- [ ] **Step 5: Regenerate API artifacts**

Run:

```bash
make api-generate
```

Expected: `openapi.json` and `frontend/src/lib/api/generated/schema.ts` update.

- [ ] **Step 6: Run broader tests**

```bash
go test ./internal/httpapi ./internal/auth/hidden
```

Expected: pass.

- [ ] **Step 7: Commit**

```bash
git add internal/httpapi openapi.json frontend/src/lib/api/generated/schema.ts
git commit -m "feat(hidden): add auth HTTP routes"
```

---

## Task 6: Media Repo Hidden Filtering and Cascades

**Files:**
- Modify: `internal/media/media.go`
- Modify: `internal/media/repo.go`
- Modify: `internal/media/repo_test.go`

- [ ] **Step 1: Write failing repo tests**

Cover:

- `List` excludes hidden by default
- `List` includes hidden only with `IncludeHidden`
- `GetByIDVisible(ctx, id, includeHidden)` or equivalent returns not found for hidden when include false
- `SetHiddenCascade` hides primaries and sidecars in one transaction
- `ClearHiddenCascade` clears primaries and sidecars in one transaction
- `ClearAllHiddenForOwner` clears all hidden rows for that owner only
- `ListHidden` returns primary/standalone hidden rows only, sorted by `timestamp IS NULL ASC, timestamp DESC, imported_at DESC, id DESC`

- [ ] **Step 2: Run tests and confirm failure**

```bash
go test ./internal/media -run Hidden
```

Expected: fail.

- [ ] **Step 3: Extend filter and repo methods**

In `ListFilter`:

```go
IncludeHidden bool
```

Add hidden predicate to `List`:

```go
if !f.IncludeHidden {
    conds = append(conds, "hidden_at IS NULL")
}
```

Add helpers:

```go
func (r *Repo) GetByIDVisible(ctx context.Context, id string, includeHidden bool) (Media, error)
func (r *Repo) SetHiddenCascade(ctx context.Context, owner owners.Principal, ids []string, at time.Time) error
func (r *Repo) ClearHiddenCascade(ctx context.Context, owner owners.Principal, ids []string) error
func (r *Repo) ClearAllHiddenForOwner(ctx context.Context, owner owners.Principal) error
func (r *Repo) ListHidden(ctx context.Context, owner owners.Principal, limit, offset int) ([]Media, error)
```

`SetHiddenCascade` update shape:

```sql
UPDATE media
   SET hidden_at = ?
 WHERE owner_hub = ? AND owner_user_id = ?
   AND (id IN (...) OR paired_with_id IN (...))
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/media
```

Expected: pass.

- [ ] **Step 5: Commit**

```bash
git add internal/media
git commit -m "feat(hidden): add media hidden filtering"
```

---

## Task 7: Media Service, Owner HTTP Media Boundaries, and Hidden Media Endpoints

**Files:**
- Modify: `internal/service/media_service.go`
- Modify: `internal/service/media_service_test.go`
- Modify: `internal/service/thumb_service.go`
- Modify: `internal/service/thumb_service_test.go`
- Modify: `internal/httpapi/media.go`
- Modify: `internal/httpapi/media_test.go`
- Modify: `internal/httpapi/media_original.go`
- Modify: `internal/httpapi/media_original_test.go`
- Modify: `internal/httpapi/media_thumb.go`
- Modify: `internal/httpapi/media_thumb_test.go`
- Modify/Create: `internal/httpapi/hidden.go`
- Modify/Create: `internal/httpapi/hidden_test.go`
- Modify: `internal/cli/server.go`
- Modify: `openapi.json`
- Modify: `frontend/src/lib/api/generated/schema.ts`

- [ ] **Step 1: Write failing service tests**

Cover:

- `MediaService.Get` excludes hidden by default
- `MediaService.Get(... includeHidden=true)` returns hidden owned row
- `OpenOriginal` and `ThumbService.Get` return `ErrNotFound` for hidden without include
- `Hide` rejects paired sidecar ids with invalid-sidecar failure
- `Hide` cascades to sidecars
- `Unhide` requires include/unlock path and cascades to sidecars
- `Hide` returns partial bulk shape for missing/cross-owner ids

- [ ] **Step 2: Write failing HTTP tests**

Cover:

- `GET /api/v1/media/{hidden}` returns 404 without cookie and 200 with valid cookie
- owner thumb/original match same boundary
- `GET /api/v1/hidden/media` returns 403 without cookie, list with cookie, and `next_offset` using the same limit+1 sniff pattern as `/api/v1/media`
- `POST /api/v1/media:hidden` returns 409 when no credential exists
- `POST /api/v1/media:hidden` hides visible primary and sidecar
- `POST /api/v1/media:unhide` returns 403 without cookie and succeeds with cookie
- `hidden_at` appears in owner DTO where applicable

- [ ] **Step 3: Run tests and confirm failure**

```bash
go test ./internal/service ./internal/httpapi -run 'Hidden|MediaOriginal|MediaThumb'
```

Expected: fail.

- [ ] **Step 4: Implement service APIs**

Add request/result types:

```go
type HiddenBulkFailure struct {
    ID string `json:"id"`
    Code string `json:"code"` // not_found | invalid_sidecar
}

type HiddenBulkResult struct {
    Succeeded []string
    Failed []HiddenBulkFailure
}
```

Add:

```go
func (s *MediaService) Get(ctx context.Context, id string, caller owners.Principal, includeHidden ...bool) (media.Media, error)
func (s *MediaService) List(ctx context.Context, filter media.ListFilter, caller owners.Principal) ([]media.Media, error)
func (s *MediaService) OpenOriginal(ctx context.Context, id string, caller owners.Principal, offset, length int64, includeHidden ...bool) (io.ReadCloser, media.Media, error)
func (s *MediaService) Hide(ctx context.Context, caller owners.Principal, ids []string) (HiddenBulkResult, error)
func (s *MediaService) Unhide(ctx context.Context, caller owners.Principal, ids []string) (HiddenBulkResult, error)
func (s *MediaService) ClearAllHiddenForOwner(ctx context.Context, owner owners.Principal) error
func (s *MediaService) ListHidden(ctx context.Context, caller owners.Principal, limit, offset int) ([]media.Media, error)
```

Add the same hidden-read option to `ThumbService.Get`, or add a clearly named `GetIncludingHidden` wrapper. Owner thumb reads must share the same direct-by-id boundary as media detail/original: hidden rows are 404 without the unlock claim and 200 with it.

Keep existing callers compiling by using a variadic `includeHidden ...bool` or by adding `GetVisible`/`GetAnyOwned` wrappers and updating call sites explicitly. Prefer explicit wrapper names if the change stays readable.

`MediaService.List` must clamp `filter.IncludeHidden = false` for ordinary owner list routes. `/hidden/media` uses `ListHidden`; direct-by-id reads use the unlock-aware `Get`/`OpenOriginal`/`ThumbService.Get` path. Do not let an HTTP caller smuggle hidden rows into `/api/v1/media` by setting a filter flag.

In `Hide` and `Unhide`, validate the caller-owned input ids before calling repo cascade methods. An input row with `paired_with_id != nil` becomes a per-id `invalid_sidecar` failure. Standalone RAW rows with `paired_with_id == nil` are normal media and may be hidden directly. Repo cascade methods receive only ids that passed this service validation.

- [ ] **Step 5: Implement HTTP endpoints**

Add `HiddenAt *time.Time json:"hidden_at,omitempty"` to `mediaDTO`.

For direct owner detail/thumb/original handlers, read unlock claim:

```go
includeHidden := false
if claim, ok := hidden.UnlockClaimFromContext(ctx); ok && claim.Principal == caller {
    includeHidden = true
}
```

Use include only for direct-by-id reads, not list routes.

Pass the same `includeHidden` value through both phases of the original handler: the initial `Get` used for headers and the later `OpenOriginal` call inside the range writer. Otherwise an unlocked hidden original can pass the header read and fail during streaming.

Register:

- `GET /api/v1/hidden/media`
- `POST /api/v1/media:hidden`
- `POST /api/v1/media:unhide`

As with auth routes, register the OpenAPI operations even if one collaborator is nil; handlers should return 503 when `MediaService` or `HiddenAuth` is unavailable. The spec dumper must still see the route shapes.

`GET /api/v1/hidden/media` input/output matches the existing media list shape:

```go
type listHiddenMediaInput struct {
    Limit int `query:"limit"`
    Offset int `query:"offset"`
}
type listHiddenMediaOutput struct {
    Body struct {
        Items []mediaDTO `json:"items"`
        NextOffset *int `json:"next_offset,omitempty"`
    }
}
```

Clamp limit with the same default/max as `/api/v1/media`, request `limit+1` rows from `ListHidden`, trim to `limit`, and emit `next_offset` only when the extra row exists.

If Huma rejects colon paths, fall back to:

- `POST /api/v1/media/hidden:bulk`
- `POST /api/v1/media/unhide:bulk`

and update the frontend/API generated types and spec note before proceeding.

- [ ] **Step 6: Wire server runtime**

In `internal/cli/server.go`, after DB open and after `mediaSvc` exists:

```go
hiddenRepo := hidden.NewRepo(d.WriteDB(), d.ReadDB())
hiddenSvc := hidden.NewService(hiddenRepo, mediaSvc)
```

Pass `hiddenSvc` and `cfg.HTTP.DevInsecureCookies` into `httpapi.Deps`.

Start a background sweeper goroutine that calls hidden service sweep methods every five minutes and exits on context cancel.

- [ ] **Step 7: Regenerate API artifacts**

```bash
make api-generate
```

- [ ] **Step 8: Run tests**

```bash
go test ./internal/media ./internal/service ./internal/httpapi ./internal/cli
```

Expected: pass.

- [ ] **Step 9: Commit**

```bash
git add internal/service internal/httpapi internal/cli/server.go openapi.json frontend/src/lib/api/generated/schema.ts
git commit -m "feat(hidden): add media hide endpoints"
```

---

## Task 8: Hidden CLI Commands

**Files:**
- Create: `internal/cli/hidden.go`
- Create: `internal/cli/hidden_test.go`
- Modify: `internal/cli/root.go`

- [ ] **Step 1: Write failing CLI tests**

Cover:

- `fotobank hidden setup` refuses non-stub mode
- setup prompts twice and rejects mismatched confirmation
- setup stores credential for stub principal
- change verifies current and revokes sessions
- disable prompts confirmation and clears hidden flags
- `fotobank admin reset-hidden-passcode --confirm --owner h:u` works in non-stub mode
- admin reset without `--owner` in non-stub mode returns usage error
- admin reset preserves `media.hidden_at`

Use `cmd.SetIn(...)` / `cmd.InOrStdin()` for tests. If secure terminal password reading is awkward in tests, implement a `readPasscode(cmd, prompt)` helper that uses `term.ReadPassword` only when stdin is a terminal and falls back to line reads in tests.

- [ ] **Step 2: Run tests and confirm failure**

```bash
go test ./internal/cli -run Hidden
```

Expected: fail.

- [ ] **Step 3: Implement CLI context loader**

Mirror `loadAlbumCtx` / `loadShareCtx`, but wire hidden service and media repo. `hidden setup/change/disable` require stub mode. `admin reset-hidden-passcode`:

- in stub mode: `--owner` optional
- in non-stub mode: `--owner hub:user` required
- `--confirm` required

- [ ] **Step 4: Add commands to root**

In `newRootCmd()`:

```go
root.AddCommand(newHiddenCmd())
root.AddCommand(newAdminCmd())
```

If an admin command group does not exist, create it in `hidden.go` with only `reset-hidden-passcode`.

- [ ] **Step 5: Run tests**

```bash
go test ./internal/cli
```

Expected: pass.

- [ ] **Step 6: Commit**

```bash
git add internal/cli
git commit -m "feat(hidden): add hidden CLI commands"
```

---

## Task 9: Shared Grantee Filtering

**Files:**
- Modify: `internal/service/shared_read_service.go`
- Modify: `internal/service/shared_read_service_test.go`
- Modify: `internal/httpapi/shared.go`
- Modify: `internal/httpapi/shared_bytes.go`
- Modify: `internal/httpapi/shared_test.go` — extend this for shared JSON and byte route coverage; there is no separate `shared_bytes_test.go` today.
- Modify: `internal/share/repo.go`
- Modify: `internal/share/repo_test.go`

- [ ] **Step 1: Write failing grantee tests**

Cover every shared surface:

- `ListMedia` omits hidden shared photo
- `ListAlbumMedia` omits hidden album member
- `GetMedia` returns `ErrNotFound` for hidden shared photo
- `OpenOriginal` returns `ErrNotFound` for hidden shared photo
- `OpenThumb` returns `ErrNotFound` for hidden shared photo
- share cover/preview queries do not select hidden media

- [ ] **Step 2: Run tests and confirm failure**

```bash
go test ./internal/share ./internal/service ./internal/httpapi -run Shared
```

Expected: fail.

- [ ] **Step 3: Add hidden predicates**

Apply `m.hidden_at IS NULL` to repo queries used by shared reads. Do not expose an `IncludeHidden` escape hatch in grantee code. If a shared read path currently fetches a media row by id after authorization, use a visible-only repo getter.

- [ ] **Step 4: Run tests**

```bash
go test ./internal/share ./internal/service ./internal/httpapi -run Shared
```

Expected: pass.

- [ ] **Step 5: Commit**

```bash
git add internal/share internal/service/shared_read_service.go internal/service/shared_read_service_test.go internal/httpapi/shared*
git commit -m "feat(hidden): filter shared reads"
```

---

## Task 10: Album Hidden Count and Add-Hidden Carve-Out

**Files:**
- Modify: `internal/album/album.go`
- Modify: `internal/album/repo.go`
- Modify: `internal/album/repo_test.go`
- Modify: `internal/service/album_service.go`
- Modify: `internal/service/album_service_test.go`
- Modify: `internal/httpapi/albums.go`
- Modify: `internal/httpapi/albums_test.go`
- Modify: `openapi.json`
- Modify: `frontend/src/lib/api/generated/schema.ts`

- [ ] **Step 1: Write failing album repo tests**

Cover:

- `ListByOwner` `ItemCount` counts visible members only
- `ListByOwner` `HiddenCount` counts hidden members
- `GetDetailByID` returns same counts
- cover derivation ignores hidden rows
- `ListMedia` excludes hidden rows regardless of unlock cookie

- [ ] **Step 2: Write failing service/HTTP tests**

Cover:

- `AlbumService.AddMedia` treats hidden id as not found by default
- `AlbumService.AddMedia` accepts hidden id when called with include-hidden/unlock option
- `POST /api/v1/albums/{id}/media` accepts hidden id only with valid unlock cookie
- album DTO includes `hidden_count`

- [ ] **Step 3: Run tests and confirm failure**

```bash
go test ./internal/album ./internal/service ./internal/httpapi -run Album
```

Expected: fail.

- [ ] **Step 4: Implement domain and repo**

Add to `album.AlbumListItem`:

```go
HiddenCount int
```

Update cover CTEs and count expressions:

```sql
COUNT(CASE WHEN m.hidden_at IS NULL THEN 1 END) AS item_count,
COUNT(CASE WHEN m.hidden_at IS NOT NULL THEN 1 END) AS hidden_count
```

Cover CTE must include `m.hidden_at IS NULL`.

- [ ] **Step 5: Implement album service carve-out**

Add an option to `AddMedia` without breaking current call sites:

```go
type AddMediaOption func(*addMediaOptions)
func WithHiddenMediaAllowed() AddMediaOption
func (s *AlbumService) AddMedia(ctx context.Context, albumID string, mediaIDs []string, caller owners.Principal, opts ...AddMediaOption) ...
```

When hidden media is not allowed, hidden ids return `errs.ErrNotFound`. When allowed, they pass owner/sidecar checks and insert as album members.

HTTP handler passes `WithHiddenMediaAllowed()` only when unlock claim is valid for caller.

- [ ] **Step 6: Regenerate API artifacts**

```bash
make api-generate
```

- [ ] **Step 7: Run tests**

```bash
go test ./internal/album ./internal/service ./internal/httpapi
```

Expected: pass.

- [ ] **Step 8: Commit**

```bash
git add internal/album internal/service/album_service.go internal/service/album_service_test.go internal/httpapi/albums.go internal/httpapi/albums_test.go openapi.json frontend/src/lib/api/generated/schema.ts
git commit -m "feat(hidden): integrate albums"
```

---

## Task 11: Frontend MediaStore Hidden Invariant

**Files:**
- Modify: `frontend/src/lib/media/mediaStore.svelte.ts`
- Modify: `frontend/src/lib/media/mediaStore.test.ts`

- [ ] **Step 1: Write failing MediaStore tests**

Add tests:

- raw hidden row is cached by id but not inserted into `months`
- visible row re-merged as hidden is removed from `months`
- hidden cached row re-merged as visible enters `months`
- `removeMany(ids, hiddenAt)` removes visible rows and sets cached `hidden_at`
- identity-field compile guard includes `hidden_at`

- [ ] **Step 2: Run tests and confirm failure**

```bash
cd frontend && bun test src/lib/media/mediaStore.test.ts
```

Expected: fail.

- [ ] **Step 3: Implement type and adapter changes**

Add to `Media`:

```ts
hidden_at?: string | null;
```

In `toMedia`, assign `hidden_at` when raw value is a string or `null` if present. Update identity-field guard and unchanged predicate.

- [ ] **Step 4: Make `merge` hidden-aware**

Before inserting into a month bucket:

```ts
if (it.hidden_at != null) {
  this.removeFromVisibleIndexes(it.id);
  this.byMediaId.set(it.id, it);
  continue;
}
```

Add private helper:

```ts
private removeFromVisibleIndexes(id: string): void
```

that removes from `byMonth`, `byId`, prunes empty buckets, and rebuilds `months`.

Add public:

```ts
removeMany(ids: string[], hiddenAt: string = new Date().toISOString()): void
```

- [ ] **Step 5: Run tests**

```bash
cd frontend && bun test src/lib/media/mediaStore.test.ts
```

Expected: pass.

- [ ] **Step 6: Commit**

```bash
git add frontend/src/lib/media/mediaStore.svelte.ts frontend/src/lib/media/mediaStore.test.ts
git commit -m "feat(hidden): keep hidden rows out of visible media store"
```

---

## Task 12: Frontend Hidden Stores, Route, Gate, Sidebar, and Router Back

**Files:**
- Create: `frontend/src/lib/hidden/hiddenStore.svelte.ts`
- Create: `frontend/src/lib/hidden/hiddenStore.test.ts`
- Create: `frontend/src/lib/hidden/hiddenMediaStore.svelte.ts`
- Create: `frontend/src/lib/hidden/hiddenMediaStore.test.ts`
- Create: `frontend/src/lib/components/HiddenGate.svelte`
- Create: `frontend/src/lib/components/HiddenGate.test.ts`
- Create: `frontend/src/routes/HiddenLibrary.svelte`
- Create: `frontend/src/routes/HiddenLibrary.test.ts`
- Modify: `frontend/src/lib/router/router.svelte.ts`
- Modify: `frontend/src/lib/router/router.test.ts`
- Modify: `frontend/src/lib/components/Sidebar.svelte`
- Modify: `frontend/src/lib/components/Sidebar.test.ts`
- Modify: `frontend/src/App.svelte`

- [ ] **Step 1: Write failing frontend tests**

Cover:

- router matches `/hidden`
- `router.back("/library")` calls history back when possible and fallback when not
- Sidebar shows Hidden in BROWSE
- HiddenStore refresh/unlock/lock/hide/unhide calls expected endpoints
- keepalive lock optimistically clears state and does not refresh
- HiddenMediaStore loads `/api/v1/hidden/media` and keeps local cache
- HiddenGate maps 403/429/400/401 errors
- HiddenLibrary renders CTA, gate, or grid based on store state

- [ ] **Step 2: Run tests and confirm failure**

```bash
cd frontend && bun test src/lib/router/router.test.ts src/lib/components/Sidebar.test.ts src/lib/hidden src/lib/components/HiddenGate.test.ts src/routes/HiddenLibrary.test.ts
```

Expected: fail.

- [ ] **Step 3: Implement router/sidebar**

Add route:

```ts
| { route: "hidden" }
```

Pattern:

```ts
{ re: /^\/hidden\/?$/, build: () => ({ route: "hidden" }) }
```

Add:

```ts
back(fallback: string) {
  if (window.history.length > 1) window.history.back();
  else this.navigate(fallback);
}
```

Add Hidden entry to Sidebar BROWSE.

- [ ] **Step 4: Implement stores**

HiddenStore methods:

```ts
refresh(): Promise<void>
unlock(passcode: string): Promise<void>
lock(opts?: { keepalive?: boolean }): Promise<void>
hide(ids: string[]): Promise<HiddenBulkResult>
unhide(ids: string[]): Promise<HiddenBulkResult>
```

HiddenMediaStore mirrors `MediaStore` but loads `/api/v1/hidden/media` and never touches shared `MediaStore`.

- [ ] **Step 5: Implement route/gate and App wiring**

In `App.svelte`, construct `const hiddenStore = new HiddenStore(api); hiddenStore.refresh();` and pass to `Sidebar`, `HiddenLibrary`, `MediaActions` consumers later. Route `/hidden` to `HiddenLibrary`.

- [ ] **Step 6: Run tests**

```bash
cd frontend && bun test src/lib/router/router.test.ts src/lib/components/Sidebar.test.ts src/lib/hidden src/lib/components/HiddenGate.test.ts src/routes/HiddenLibrary.test.ts
```

Expected: pass.

- [ ] **Step 7: Commit**

```bash
git add frontend/src/lib/hidden frontend/src/lib/components/HiddenGate.svelte frontend/src/lib/components/HiddenGate.test.ts frontend/src/routes/HiddenLibrary.svelte frontend/src/routes/HiddenLibrary.test.ts frontend/src/lib/router frontend/src/lib/components/Sidebar.svelte frontend/src/lib/components/Sidebar.test.ts frontend/src/App.svelte
git commit -m "feat(hidden): add hidden route and stores"
```

---

## Task 13: Hidden Lock Strip, Toasts, and MediaActions Hide/Unhide

**Files:**
- Create: `frontend/src/lib/components/HiddenLockStrip.svelte`
- Create: `frontend/src/lib/components/HiddenLockStrip.test.ts`
- Create: `frontend/src/lib/components/ToastStack.svelte`
- Create: `frontend/src/lib/components/ToastStack.test.ts`
- Modify: `frontend/src/lib/components/MediaActions.svelte`
- Modify: `frontend/src/lib/components/MediaActions.test.ts`
- Modify: `frontend/src/App.svelte`
- Modify: `frontend/src/routes/Library.svelte`
- Modify: `frontend/src/routes/Sessions.svelte`
- Modify: `frontend/src/routes/AlbumDetail.svelte`
- Modify: `frontend/src/routes/MediaDetail.svelte`
- Modify: `frontend/src/routes/HiddenLibrary.svelte`

- [ ] **Step 1: Write failing component/route tests**

Cover:

- HiddenLockStrip renders countdown, calls lock, unmounts when locked
- ToastStack renders dismissible partial-failure messages and details
- MediaActions button matrix:
  - library/session/album/media-detail visible: Add, Share, Hide if configured
  - hidden/media-detail hidden: Unhide, Add only
  - no Hide when unconfigured
- MediaDetail visible hide navigates `/library`
- MediaDetail hidden unhide clones local row with `hidden_at=null`

- [ ] **Step 2: Run tests and confirm failure**

```bash
cd frontend && bun test src/lib/components/HiddenLockStrip.test.ts src/lib/components/ToastStack.test.ts src/lib/components/MediaActions.test.ts src/routes/MediaDetail.test.ts
```

Expected: fail.

- [ ] **Step 3: Implement ToastStack**

Keep minimal:

```ts
type Toast = {
  id: string;
  message: string;
  details?: string[];
  kind?: "info" | "error";
};
```

Expose callbacks or a small store local to `App.svelte`; do not overbuild global toast infrastructure.

- [ ] **Step 4: Implement HiddenLockStrip and App triggers**

Mount strip above `ThreeColumnLayout`. Add `visibilitychange` effect:

```ts
$effect(() => {
  const onVis = () => {
    if (document.visibilityState === "hidden") hiddenStore.lock({ keepalive: true });
  };
  document.addEventListener("visibilitychange", onVis);
  return () => document.removeEventListener("visibilitychange", onVis);
});
```

- [ ] **Step 5: Extend MediaActions props**

Add props:

```ts
context?: "library" | "session" | "album" | "media-detail" | "hidden";
hiddenConfigured?: boolean;
isHidden?: boolean;
onHide?: (ids: string[]) => void;
onUnhide?: (ids: string[]) => void;
```

Render buttons per spec.

- [ ] **Step 6: Wire route flows**

Library/Session:

- Hide confirms, calls `hiddenStore.hide(ids)`, calls `mediaStore.removeMany(succeeded)`, clears succeeded selection, marks album stale.

Album:

- Hide confirms, calls `albumDetailStore.pruneHidden(succeeded)`, preserves membership, marks album stale.

MediaDetail visible:

- Hide confirms, calls `hiddenStore.hide([id])`, `mediaStore.removeMany([id])`, marks album stale, navigates `/library`.

HiddenLibrary:

- Unhide calls `hiddenStore.unhide(ids)`, prunes `HiddenMediaStore`, marks album stale.

MediaDetail hidden:

- Unhide clones local media with `hidden_at=null`, calls `mediaStore.mergeRaw([updatedRaw])`, no navigation.

- [ ] **Step 7: Run tests**

```bash
cd frontend && bun test src/lib/components/HiddenLockStrip.test.ts src/lib/components/ToastStack.test.ts src/lib/components/MediaActions.test.ts src/routes
```

Expected: pass.

- [ ] **Step 8: Commit**

```bash
git add frontend/src/lib/components frontend/src/routes frontend/src/App.svelte
git commit -m "feat(hidden): add hide and unhide UI"
```

---

## Task 14: Album Store Refresh and Hidden Count UI

**Files:**
- Modify: `frontend/src/lib/albums/albumsStore.svelte.ts`
- Modify: `frontend/src/lib/albums/albumsStore.test.ts`
- Modify: `frontend/src/lib/albums/albumDetailStore.svelte.ts`
- Modify: `frontend/src/lib/albums/albumDetailStore.test.ts`
- Modify: `frontend/src/lib/components/AlbumGrid.svelte`
- Modify: `frontend/src/routes/AlbumDetail.svelte`
- Modify: `frontend/src/routes/AlbumsIndex.svelte`

- [ ] **Step 1: Write failing tests**

Cover:

- AlbumsStore parses `hidden_count`
- `markStale()` causes next `/albums` mount/load to refetch
- AlbumDetailStore parses `hidden_count`
- `refreshMeta()` updates header counts without resetting item ids
- `pruneHidden(ids)` removes item ids/membership and decrements visible item count
- AlbumGrid footer renders `95 photos · 5 hidden`
- AlbumDetail header renders hidden chip

- [ ] **Step 2: Run tests and confirm failure**

```bash
cd frontend && bun test src/lib/albums src/lib/components/AlbumGrid.test.ts src/routes/AlbumDetail.test.ts
```

Expected: fail.

- [ ] **Step 3: Implement store fields/methods**

Add `hidden_count` to album types. Add:

```ts
markStale(): void
async refreshIfStale(): Promise<void>
```

to `AlbumsStore`, and:

```ts
async refreshMeta(): Promise<void>
pruneHidden(ids: string[]): void
```

to `AlbumDetailStore`.

- [ ] **Step 4: Wire UI**

Update album tile footer and detail header. Ensure Hide/Unhide/add-from-hidden route handlers call refresh hooks.

- [ ] **Step 5: Run tests**

```bash
cd frontend && bun test src/lib/albums src/lib/components/AlbumGrid.test.ts src/routes/AlbumDetail.test.ts
```

Expected: pass.

- [ ] **Step 6: Commit**

```bash
git add frontend/src/lib/albums frontend/src/lib/components/AlbumGrid.svelte frontend/src/routes/AlbumDetail.svelte frontend/src/routes/AlbumsIndex.svelte
git commit -m "feat(hidden): show album hidden counts"
```

---

## Task 15: E2E Fixture and Hidden Playwright Coverage

**Files:**
- Modify: `cmd/e2e-server/main.go`
- Create: `frontend/tests/e2e/hidden.spec.ts`
- Modify: `frontend/playwright.config.ts` if needed

- [ ] **Step 1: Write failing Playwright tests**

Create `frontend/tests/e2e/hidden.spec.ts` covering the 15 scenarios in spec §3.11:

- Sidebar Hidden visible
- `/hidden` unconfigured CTA
- wrong passcode
- correct passcode unlock
- top strip
- manual lock
- tab-hidden auto-lock
- hide from Library
- unhide from `/hidden`
- Library unchanged by unlock cookie
- sidecar cascade
- album hidden_count chip
- lockout
- grantee never sees hidden
- hide button gated by configured

- [ ] **Step 2: Run e2e and confirm failure**

```bash
make test-e2e
```

Expected: fail because seed fixtures and/or implementation wiring are incomplete.

- [ ] **Step 3: Extend e2e seed**

In `cmd/e2e-server/main.go::seedFixtures`:

- seed `auth_hidden_credential` for stub principal with passcode `e2e-passcode`
- support env var to skip credential for configured-false test
- seed `hidden-prehidden-1` with `hidden_at = now`
- seed `hidden-target-1` with `hidden_at = NULL`
- reuse pair fixtures and Italy album for cascade/chip tests
- support `FOTOBANK_E2E_LOCKOUT_WINDOW=5s`

- [ ] **Step 4: Run e2e**

```bash
make test-e2e
```

Expected: pass.

- [ ] **Step 5: Commit**

```bash
git add cmd/e2e-server/main.go frontend/tests/e2e/hidden.spec.ts frontend/playwright.config.ts
git commit -m "test(hidden): add hidden privacy e2e coverage"
```

---

## Task 16: Full Verification and Integration Cleanup

**Files:**
- Potentially any touched files from prior tasks.

- [ ] **Step 1: Run Go tests**

```bash
go test ./...
```

Expected: pass.

- [ ] **Step 2: Run frontend checks**

```bash
cd frontend && bun test
```

Expected: pass.

- [ ] **Step 3: Run API generation check**

```bash
make api-generate
git diff --exit-code openapi.json frontend/src/lib/api/generated/schema.ts
```

Expected: no diff after generation.

- [ ] **Step 4: Run e2e**

```bash
make test-e2e
```

Expected: pass.

- [ ] **Step 5: Run formatting/lint hooks**

```bash
prek run --all-files
```

Expected: pass. If `prek` is unavailable in the environment, run the repository's documented equivalent and record the gap.

- [ ] **Step 6: Inspect privacy-critical behavior manually**

Start dev server and verify:

```bash
make build
```

Then run server against a dev DB with Hidden configured. Manually check:

- hidden media absent from Library and Albums while unlocked
- direct `/media/{hidden}` returns 404 locked and 200 unlocked
- grantee shared listing omits hidden rows
- sidecar direct URL returns 404 when primary is hidden and locked

- [ ] **Step 7: Final commit if verification caused fixes**

```bash
git status --short
git add <changed files>
git commit -m "fix(hidden): address integration verification"
```

Expected: working tree clean except unrelated pre-existing files.

---

## Implementation Notes

- Current worktree already has unrelated untracked `e2e-server` and `frontend/tmp/` entries. Do not add or delete them unless the implementer confirms they are relevant.
- Use `rg` before editing any path named here; F2.3/F2.2 code may have shifted line numbers.
- Keep commits task-sized. If a task becomes too large, split by backend/frontend boundary but keep tests green at each commit.
- Do not introduce a new migration. If pre-commit complains about editing `000001`, use the established `FOTOBANK_MIGRATION_BASE_REF` escape hatch documented by earlier F2.x specs.
- The plan intentionally puts backend privacy enforcement before frontend UI. Do not build UI paths that can render hidden data until backend filters and byte-route checks are already tested.
