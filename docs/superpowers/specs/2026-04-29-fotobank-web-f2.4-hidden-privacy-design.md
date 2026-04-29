# F2.4 Hidden Privacy — Design

**Status:** Design locked 2026-04-29. Awaiting writing-plans handoff.

**Phase:** Slots between F2.3 (albums + sharing, shipped) and F2.5 (lightbox).
Implementation-orthogonal to F2.5.

**Master spec source:** `docs/superpowers/specs/2026-04-26-fotobank-web-frontend-design.md` §9.

**Scope summary:** Per-photo Hidden flag with passcode-gated owner-only viewing. SPA route, top-bar
indicator, CLI tooling, Playwright e2e coverage. Sidecar cascade is included; album hidden-count
chip is included; SPA Settings UI is deferred (CLI only in v1).

---

## 1. Threat Model & Scope

### 1.1 What "Hidden" is

App-level privacy, not encryption. The threat addressed is *bystander glance* — someone briefly
looking at the user's screen while they scroll photos. Hidden does NOT defend against:

- Device compromise (an attacker with shell access reads SQLite directly).
- Filesystem inspection (the bytes still live unencrypted under the NAS root and flash cache).
- Network traffic on a compromised host (TLS termination is upstream of fotobank).

What Hidden DOES defend against:

- Photos appearing in Library / Album / Sessions scroll views.
- Photos being returned by direct `/media/:id` reads without explicit re-auth.
- Photos appearing in shares to grantees (forever; even on shares created before the hide).
- Photos appearing in metadata listings (counts adjusted via a separate `hidden_count`).

### 1.2 Sidecar cascade

Hiding a JPEG primary must also hide its RAW sidecars (and any other sidecars pointing at it via
`paired_with_id`). Unhiding a primary unhides its sidecars. The visible/hidden state of a primary
and its sidecars is always consistent. Standalone RAW rows (no `paired_with_id`) are normal media
and may be hidden/unhidden directly.

The cascade runs in SQL inside the same transaction as the primary's mutation; sidecar rows are
not addressed by their own ids on the input. Service-layer Hide/Unhide rejects any input id whose
`paired_with_id IS NOT NULL` with `errs.ErrInvalidArgument`.

### 1.3 Where enforcement lives

- **Repos** filter `WHERE hidden_at IS NULL` by default; expose an `IncludeHidden bool` for
  explicit callers.
- **Services** call repos with the appropriate `IncludeHidden` based on caller context (owner with
  unlock claim → may include; grantee → never).
- **Byte services** (`/thumb/:id`, `/original/:id`) re-check the row before streaming.
- **Middleware** validates the unlock cookie and attaches an unlock claim to request context.
  Filtering itself is service/repo, not middleware.
- **Shared (grantee) endpoints** filter unconditionally; the unlock cookie is never honored on the
  grantee surface.

---

## 2. Backend

### 2.1 Schema

Folded into `internal/db/migrations/000001_initial_schema.up.sql` (pre-prod policy; no separate
migration). The matching `.down.sql` drops the same set.

Inside the existing `CREATE TABLE media`: add `hidden_at TIMESTAMP` (nullable). Plus indexes and
new tables:

```sql
CREATE INDEX media_visible_idx
  ON media (owner_hub, owner_user_id, timestamp DESC)
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
  ON auth_hidden_session (principal_hub, principal_user_id)
  WHERE revoked_at IS NULL;
CREATE INDEX auth_hidden_session_expiry_idx
  ON auth_hidden_session (expires_at)
  WHERE revoked_at IS NULL;

CREATE TABLE auth_hidden_failure (
  principal_hub      TEXT NOT NULL,
  principal_user_id  TEXT NOT NULL,
  occurred_at        TIMESTAMP NOT NULL,
  FOREIGN KEY (principal_hub, principal_user_id)
    REFERENCES owners(hub, user_id)
);
CREATE INDEX auth_hidden_failure_owner_idx
  ON auth_hidden_failure (principal_hub, principal_user_id, occurred_at DESC);

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

The two session indexes serve different paths: `_principal_active_idx` powers
`revoke-all-for-principal`; `_expiry_idx` powers the background sweeper that closes expired
unrevoked rows.

### 2.2 Repos

`internal/auth/hidden/repo.go`:

- `GetCredential(ctx, principal) (*Credential, error)`
- `UpsertCredential(ctx, principal, hash, now)`
- `DeleteCredential(ctx, principal)`
- `InsertSession(ctx, row)`
- `LookupActiveSession(ctx, sha256) (*Session, error)` — checks
  `revoked_at IS NULL AND expires_at > now`
- `RevokeSession(ctx, sha256, now)`
- `RevokeAllSessionsForPrincipal(ctx, principal, now)`
- `SweepExpiredSessions(ctx, now)`
- `InsertFailure(ctx, principal, now)`
- `CountRecentFailures(ctx, principal, since) int`
- `PurgeOldFailures(ctx, before)`
- `GetLockout(ctx, principal) (*Lockout, error)`
- `UpsertLockout(ctx, principal, lockedUntil, now)`

`internal/media/repo.go` extensions:

- `SetHiddenCascade(ctx, owner, ids, at)` — single transaction, owner-scoped, sidecar cascade in
  SQL via `WHERE id IN (?…) OR paired_with_id IN (?…)`.
- `ClearHiddenCascade(ctx, owner, ids)` — same shape.
- Existing list/get queries gain an `IncludeHidden bool`; when false, queries match the partial
  `media_visible_idx`.

The "reject sidecar ids" rule is an input-validation rule on the service. The repo's cascade
methods are the only mutators of `media.hidden_at`, and they always cascade.

### 2.3 Services + sharing seams

**`internal/auth/hidden/service.go`:**

- `Setup(ctx, principal, passcode)` — refuses if a credential already exists.
- `Change(ctx, principal, oldPasscode, newPasscode)` — verifies old; revokes all sessions on
  success.
- `Disable(ctx, principal, passcode)` — atomic: delete credential + clear all hidden flags for
  principal (via `ClearHiddenCascade`) + revoke all sessions.
- `Unlock(ctx, principal, passcode) (token, expiresAt)` — generates token, inserts session.
- `Lock(ctx, tokenSHA)` — best-effort, idempotent (no error if token unknown or already revoked).
- `AdminReset(ctx, principal)` — used by `fotobank admin reset-hidden-passcode`.

Every passcode-bearing method enforces `1 <= len([]byte(passcode)) <= 1024` before hashing or
comparison. The OpenAPI string-length validation is defense in depth, not source of truth.

**Argon2id parameters (locked):** `m=19456 KiB, t=2, p=1`. Tuned for moderate defense against
offline attack on consumer hardware. Implementation may benchmark on the deployment target to
catch pathological slowness; it may NOT silently choose different parameters and may NOT add
config knobs in F2.4. Pathological slowness is a design escalation, not a quiet override.

**Lockout logic** (used by `Change`, `Disable`, `Unlock`):

1. Check `auth_hidden_lockout.locked_until > now` for the principal. If so, return 429.
2. Attempt the operation.
3. On failure: INSERT into `auth_hidden_failure`; SELECT count over last 60s; if count ≥ 5 AND no
   active lockout, UPSERT `auth_hidden_lockout` with `locked_until = now + 5min`.
4. On success: leave the lockout row alone; the failure rows age out via the background purge.

The failure-event table records attempts for sliding-window counting; `auth_hidden_lockout`
stores the active lockout decision so a process restart, transient DB blip, or rapid retry can't
reset the lockout.

**`internal/media/service.Hide/Unhide(ctx, caller, ids)`:**

1. Owner-scope each input id; reject foreign with `errs.ErrNotFound`.
2. Reject any id whose `paired_with_id IS NOT NULL` with `errs.ErrInvalidArgument` — sidecars
   must not be addressed directly.
3. Call `repo.SetHiddenCascade` / `ClearHiddenCascade` in one transaction; sidecar rows are
   mutated by the cascade clause, not by being added to the input.

**`internal/share/SharedReadService`:**

`hidden_at IS NULL` is applied at every method (`ListMedia`, `ListAlbumMedia`, `GetMedia`,
`OpenOriginal`, `OpenThumb`) and the underlying repo queries each calls. Hidden media is
invisible to grantees regardless of when the share was created.

### 2.4 HTTP

**Auth surface** (`/api/v1/auth/hidden/*`):

| Route | Method | Body | Success | Errors |
|---|---|---|---|---|
| `/setup` | POST | `{passcode}` | 204 | 409 already configured; 400 invalid |
| `/change` | POST | `{old_passcode, new_passcode}` | 204 | 403 wrong old; 400 invalid; 429 |
| `/disable` | POST | `{passcode}` | 204 | 403 wrong; 400 invalid; 429 |
| `/unlock` | POST | `{passcode}` | 204 + Set-Cookie | 403 wrong; 400 invalid; 429 |
| `/lock` | POST | — | 204 always | — |
| `/state` | GET | — | 200 `{configured, unlocked, expires_at?}` | — |

`/lock` is **idempotent** — always 204, always clears the cookie, no auth required. The SPA can
call it freely from tab-hidden / TTL-expiry / manual paths without conditionals.

**Media surface:**

| Route | Method | Cookie | Configured | Body | Success | Errors |
|---|---|---|---|---|---|---|
| `/api/v1/hidden/media` | GET | yes | yes (implied) | `?limit=&offset=` | 200 `{items, next_offset?}` | 403 |
| `/api/v1/media:hidden` | POST | no | **yes (else 409)** | `{media_ids}` | 200 `{succeeded, failed}` | 400; 409 |
| `/api/v1/media:unhide` | POST | yes | yes (implied) | `{media_ids}` | 200 `{succeeded, failed}` | 403; 400 |

`GET /api/v1/hidden/media` query (locked sort):

```sql
WHERE owner = ? AND hidden_at IS NOT NULL AND paired_with_id IS NULL
ORDER BY timestamp IS NULL ASC, timestamp DESC, imported_at DESC, id DESC
LIMIT ? OFFSET ?
```

The compound sort matches the existing media surface's null-safe deterministic ordering and
avoids unstable pagination when rows lack capture time or share the same timestamp. Sidecars are
excluded from the Hidden grid; they remain attached as downloadable sidecars on their primary's
MediaDetail.

**Album-add carve-out:** `POST /api/v1/albums/{id}/media` accepts hidden ids when a valid unlock
cookie is present (treats hidden as a direct-by-id mutation, consistent with
detail/thumb/original). Without cookie, hidden ids are reported as `not_found` per the standard
convention. This is what makes the SPA's "Add to album" hidden-context flow work.

**Bulk failure shape** (mirrors `albums:add-media`):

```json
{
  "succeeded": ["id1", "id2"],
  "failed": [
    {"id": "id3", "code": "not_found"},
    {"id": "id4", "code": "invalid_sidecar"}
  ]
}
```

`failed[].code ∈ {not_found, invalid_sidecar}`. Cross-owner and missing both report `not_found`,
matching the existing anti-probing convention. A valid bulk request returns 200 with per-id
failures; 403 is reserved for the unlock-cookie-required routes.

**Status code conventions:**

- `401` — principal-resolution failure (no identity, broken stub config). Never used for
  cookie/passcode failures.
- `403` — wrong passcode; missing/expired unlock cookie on routes that require it.
- `404` — direct-by-id read of a hidden id without unlock cookie (`GET /media/{id}`, `/thumb`,
  `/original`). Same body shape as "no such media" — anti-enumeration.
- `409` — `hidden_not_configured` on `POST /media:hidden` when no credential row exists.
- `429` — lockout. Body mirrors the 403 wording so the limiter signal isn't trivially
  distinguishable; no claim is made about timing equivalence.

**Passcode body validation:** every passcode field declares `minLength: 1, maxLength: 1024` in
the OpenAPI schema (UTF-8 character bound) AND the service re-checks
`1 <= len([]byte(passcode)) <= 1024` (UTF-8 byte bound) before hash work.

**Lockout response:** 429 with `Retry-After` header. Body is the generic error model,
deliberately shaped like the wrong-passcode body.

### 2.5 CLI

```
fotobank hidden setup            # owner-scoped; stub mode only
fotobank hidden change           # owner-scoped; stub mode only
fotobank hidden disable          # owner-scoped; stub mode only
fotobank admin reset-hidden-passcode [--owner hub:user]
```

`hidden setup/change/disable` refuse to run outside stub mode (where exactly one principal
exists). `admin reset-hidden-passcode`:

- Stub mode: `--owner` optional, defaults to the configured stub principal.
- Non-stub: `--owner hub:user` is **required**. No auto-pick.
- Effect: delete credential + revoke all active sessions. Hidden flags on media are preserved;
  operator/owner re-runs `setup` to attach a new passcode.

### 2.6 Cancellation, locking, audit

- All write paths take `ctx context.Context` and propagate cancellation per existing fotobank
  conventions.
- Two background tickers, each every 5 minutes:
  - Failure-log purge: deletes rows older than the lockout window.
  - Session sweeper: marks `revoked_at` on rows where `expires_at < now AND revoked_at IS NULL`.
- Audit: Setup/Change/Disable/AdminReset write structured INFO logs with
  `event=auth.hidden.<op>`, `principal=hub:user`, `outcome=ok|denied|locked`. No passcode
  material is ever logged.

### 2.7 Cookie config

Production cookie:
- Name: `__Host-fotobank-hidden`
- `HttpOnly; Secure; SameSite=Strict; Path=/`
- `Max-Age` matches session `expires_at`
- Value: base64url(32 random bytes)

Dev/e2e cookie (HTTP loopback):
- Name: `fotobank-hidden` (no `__Host-` prefix; the prefix requires `Secure`)
- `HttpOnly; SameSite=Strict; Path=/` (no `Secure`)

The choice is gated by an explicit config flag `http.dev_insecure_cookies` (default false). The
server does NOT sniff `X-Forwarded-Proto` to decide — that opens a downgrade path against an
untrusted proxy. Operators terminating TLS at a reverse proxy must configure the proxy to actually
run TLS; the server assumes the connection at the cookie boundary is HTTPS-equivalent and emits
`Secure` accordingly.

### 2.8 Boundary table

| Surface | Behavior |
|---|---|
| `/library`, `/albums`, `/albums/{id}/media` (lists) | always filter `hidden_at IS NOT NULL`; cookie irrelevant |
| `GET /media/{id}` / `/thumb` / `/original` — visible id | 200 |
| Same — hidden id, valid unlock cookie | 200 |
| Same — hidden id, no/expired cookie | 404 |
| `GET /hidden/media` | 200 with cookie; 403 without |
| `POST /media:hidden` (Hide) | 200 if configured; 409 if not configured (cookie irrelevant) |
| `POST /media:unhide` | 200 with cookie; 403 without |
| `POST /albums/{id}/media` (album add) | hidden ids accepted with valid unlock cookie; reported as `not_found` without |
| `SharedReadService.*` (grantee) | always filter; cookie never honored |
| Admin / CLI | not gated |

The unlock cookie's existence does NOT change `/library` or `/albums/*` rendering. List endpoints
filter unconditionally; only the Hidden list, direct-by-id reads of hidden ids, and album-add of
hidden ids consume the unlock claim.

---

## 3. Frontend

### 3.1 Routes

`/hidden` mounts `HiddenLibrary.svelte`, which switches on `HiddenStore` state:

| State | Render |
|---|---|
| `!configured` | CTA card: "Hidden privacy isn't set up. Run `fotobank hidden setup` on the host." (with docs link) |
| `configured && !unlocked` | `HiddenGate.svelte` — full-route passcode entry (not modal) |
| `configured && unlocked` | `VirtualGrid` over a route-local `HiddenMediaStore` |

There is no `/hidden/album/:id`. Hidden is a flat photo view in F2.4. Album detail in normal
Library always filters hidden out (per the locked boundary table); the album view is unaffected
by the unlock cookie's presence.

### 3.2 Sidebar entry

Component is `Sidebar.svelte`. Hidden joins BROWSE alongside Library and Sessions:

| Group | Entries |
|---|---|
| BROWSE | Library · Sessions · **Hidden** |
| CURATE | Albums |
| MANAGE | Shares |

Hidden is **unconditionally visible**, regardless of `hiddenStore.configured`. This explicitly
overrides the master design's "render only when configured" qualifier. In v1 the sidebar entry is
the discoverability path that points users at the CLI command; clicking when not configured
shows the CTA in 3.1.

### 3.3 Hidden gate

`HiddenGate.svelte` (full-route, not modal — owns the viewport so the rest of the SPA can't peek
behind it):

- Single `<input type="password">` (autofocus on mount), Cancel + Unlock buttons.
- Cancel calls `router.back("/library")`.
- Unlock POSTs `/api/v1/auth/hidden/unlock`. On 204, the browser stores the unlock cookie
  automatically (its name is a server-side detail; the SPA never references it). The component
  awaits `hiddenStore.refresh()` so `expiresAt` is populated, and the parent re-renders into the
  unlocked branch.
- Inline error mapping:
  - 403 → "Passcode incorrect."
  - 429 → "Too many attempts. Try again in {Retry-After / 60} min."
  - 400 → "Passcode must be 1–1024 bytes." (Client also rejects empty submit.)
  - 401 → "Identity not configured." (Server-side stub config issue; surface-level only.)
  - Other → "Could not reach server."

### 3.4 HiddenStore

`frontend/src/lib/hidden/hiddenStore.svelte.ts`:

```ts
configured = $state(false);
unlocked = $state(false);
expiresAt = $state<string | null>(null);
```

Methods (all `async`, all use the typed `api` Client):

- `refresh()` — GET `/state`. **Called once at app boot from `App.svelte`** so reload during a
  valid window restores the strip and unlocks `/hidden` immediately.
- `unlock(passcode)` — POST `/unlock`, on 204 calls `refresh()`. Throws typed error on non-204 so
  the gate can render the inline message.
- `lock({ keepalive = false } = {})`:
  ```ts
  // Optimistic clear so UI updates even if network call is cancelled by tab teardown.
  this.unlocked = false;
  this.expiresAt = null;
  if (keepalive) {
    void fetch("/api/v1/auth/hidden/lock", { method: "POST", keepalive: true });
    return;
  }
  await api.POST("/api/v1/auth/hidden/lock", {});
  await this.refresh();
  ```
- `hide(ids)` — POST `/api/v1/media:hidden`. Returns `{succeeded, failed}`. Caller is responsible
  for pruning the originating visible list.
- `unhide(ids)` — POST `/api/v1/media:unhide`. On 403, calls `refresh()` (cookie expired
  mid-action) and throws so callers can surface the "Hidden re-locked" toast.

Constructed once at app boot (`App.svelte`), passed by prop to routes/components. Not a global
singleton — same pattern as `MediaStore` / `AlbumsStore`.

### 3.5 Top-bar lock indicator

`HiddenLockStrip.svelte`, mounted globally in `App.svelte`:

- Renders only when `hiddenStore.unlocked === true`.
- Layout: lock-open glyph · "Hidden unlocked" · live mm:ss countdown to `expiresAt` · `[Lock now]`.
- Lock button: `hiddenStore.lock()` — manual, awaits + refreshes.
- Countdown reaches zero → `hiddenStore.lock()` once.
- The strip's presence does NOT change list filtering on `/library` or `/albums/*` — those keep
  their normal filtered view per the locked boundary.

The strip is **global**, not scoped to `/hidden`, because the unlock cookie also unlocks
direct-by-id reads (`/media/:id`, `/original`, `/thumb`) per the locked boundary. The user needs
to know the cookie is live regardless of which route they're on.

### 3.6 MediaActions: Hide / Unhide

`MediaActions.svelte` extends its existing `context` discriminator with `"hidden"`:

| Context | Buttons |
|---|---|
| `library` | Add to album · Share · **Hide** *(if `hiddenStore.configured`)* |
| `session` | Add to album · Share · **Hide** *(if `hiddenStore.configured`)* |
| `album` | Add to album · Share · **Hide** *(if `hiddenStore.configured`)* |
| `media-detail` (visible row) | Add to album · Share · **Hide** *(if `hiddenStore.configured`)* |
| `media-detail` (hidden row, requires unlocked context) | **Unhide** · Add to album |
| `hidden` | **Unhide** · Add to album |

Hide is gated on `hiddenStore.configured === true` everywhere. No configured credential means no
Hide action.

There is **no Share button** in the `hidden` context or on a hidden media-detail page. Sharing a
hidden photo would require an "unhide-and-share" flow that's deferred. Add-to-album works because
the album-add carve-out (§2.4) accepts hidden ids when a valid unlock cookie is present.

**Hide flows:**

- **Library / Session:** bulk select N → Hide → confirm modal "Hide N photos? They'll move to
  Hidden and stop appearing in Library and Albums." → `hiddenStore.hide(ids)` →
  `mediaStore.removeMany(succeeded)` prunes the visible list →
  `selection.removeAll(succeeded)` → album refresh hooks (3.7) → toast on partial failure.
- **Album:** same shape but UI prune is `albumDetailStore.pruneHidden(succeeded)` — UI-only, no
  network, preserves album membership. (Membership-removal would be the wrong call here; hidden
  photos remain album members.)
- **MediaDetail (visible row, no unlock cookie):** confirm → `hiddenStore.hide([id])` →
  `mediaStore.removeMany([id])` → album refresh hooks → `router.navigate("/library")`. Staying
  on the page would mislead since the row is no longer fetchable without a cookie.

**Unhide flows:**

- **`/hidden`:** select → Unhide (no confirm; reversible) →
  `hiddenMediaStore.removeMany(succeeded)` prunes the hidden grid → album refresh hooks → toast
  on partial failure.
- **MediaDetail (hidden row, cookie present):** Unhide → flip route-local `media.hidden_at` to
  null → `mediaStore.mergeRaw([updatedRaw])` (which inserts into the appropriate visible month
  bucket per 3.14 invariant) → no `hiddenMediaStore` call (that store is route-local to `/hidden`
  and may not exist) → no auto-navigation.

### 3.7 Album hidden_count chip + DTO additions + refresh hooks

**DTO additions:**

- Owner `Media` DTO: `hidden_at: string | null`. Omitted on responses where it'd never be
  non-null (e.g. shared-side responses); present on owner-side list/get and the Hidden list.
- `AlbumSummary` and `AlbumDetail`: `hidden_count: number`. Computed alongside `item_count`.
- **Semantic clarification:** `item_count` is now the **visible-only** count (members where
  `hidden_at IS NULL`). `hidden_count` is the additional hidden count. AlbumDetail header reads
  "95 photos · 5 hidden" not "100 photos · 5 hidden."

**SPA store additions:**

- `AlbumsStore.markStale()` — flips `stale: boolean`. Next mount of `/albums` checks the flag,
  refetches if stale, resets the flag.
- `AlbumDetailStore.refreshMeta()` — refetches only the album header (`GET
  /api/v1/albums/{id}`), not the item list.
- `AlbumDetailStore.pruneHidden(ids)` — UI-only removal from local item list and membership
  cache; no network call.

**Refresh hook fires after every successful Hide / Unhide / add-to-album-from-hidden:**

```ts
albumsStore.markStale();
if (albumDetailStoreMounted) await albumDetailStore.refreshMeta();
```

Wired in each action's route handler; the stores stay agnostic.

**UI:**

- `AlbumsIndex` tile footer: `{item_count} photos · {hidden_count} hidden` when
  `hidden_count > 0`; otherwise just `{item_count} photos`.
- `AlbumDetail` header: small grey pill `{hidden_count} hidden` when > 0.

### 3.8 Auto-lock triggers

Three triggers, wired in `App.svelte`:

1. **Tab-hidden:**
   ```ts
   document.addEventListener("visibilitychange", () => {
     if (document.visibilityState === "hidden") {
       hiddenStore.lock({ keepalive: true });
     }
   });
   ```
   `keepalive: true` lets the POST complete during tab teardown; without it the fetch is
   cancellable and the lock may not reach the server. Documented as best-effort: a backgrounded
   mobile tab might still drop the request, in which case the session sweeper closes the row at
   TTL.
2. **TTL expiry** — handled by `HiddenLockStrip` (3.5). No `keepalive` needed; the tab is
   foregrounded.
3. **Manual Lock** — strip's button.

Route-change-away is **not** a trigger.

### 3.9 Failure surfaces / toasts

`ToastStack.svelte` (new minimal component) surfaces:

- Hide partial failure — "Hid 8 of 10 photos. 2 failed." with expandable details listing
  failed ids.
- Unhide partial failure — same shape.
- Cookie-expired-mid-action — when `unhide()` or `GET /hidden/media` returns 403 mid-session,
  the store calls `refresh()`; the strip and `/hidden` both re-render to the locked state;
  toast: "Hidden re-locked. Re-enter passcode to continue."

The toast stack is also useful for F2.5 lightbox copy/share confirmations, so introducing it here
pulls forward shared infrastructure without scope creep.

### 3.10 Not covered in F2.4

- No SPA Settings UI for change/disable. CLI only in scope C.
- No "Hidden" toggle in search filters. Search itself is deferred.
- No SSE / live-update on lock state across tabs. A second tab catches re-lock on the next API
  call (403) and re-fetches state.
- No "unhide and share" inline flow.

### 3.11 Playwright e2e coverage

`frontend/tests/e2e/hidden.spec.ts`:

| Scenario | Asserts |
|---|---|
| Sidebar Hidden visible in BROWSE | Link present in `Sidebar`; navigates to `/hidden` |
| `/hidden` when not configured | CTA card shown; no Hide button anywhere in `/library` |
| Gate: wrong passcode | 403 → inline "Passcode incorrect"; field stays focused; no cookie set |
| Gate: correct passcode | Cookie set; gate unmounts; hidden grid renders seeded photo |
| Top-bar strip visible while unlocked | `HiddenLockStrip` renders with countdown + Lock button |
| Manual Lock | Strip unmounts; navigating back to `/hidden` re-shows the gate |
| Tab-hidden auto-lock | `page.evaluate` dispatches `visibilitychange` with `visibilityState=hidden`; strip unmounts |
| Hide flow from Library | Select 2 → Hide → confirm → both disappear from `/library` (no reload); after unlock, both visible in `/hidden` |
| Unhide flow from `/hidden` | Select → Unhide → both gone from `/hidden`; visible again in `/library` |
| Library list NOT affected by unlock cookie | After unlock, `/library` still excludes hidden rows |
| Sidecar cascade | Hide a primary with sidecar → `/media/<sidecar-id>` returns the not-found body |
| Album hidden_count chip | Hide a member of seeded album → tile shows "{n} hidden"; album detail header pill present |
| Lockout | 5 wrong passcodes → 6th gets 429 inline copy ("Try again in N min") |
| Grantee never sees hidden | Hide a photo in an active share → fetch the share's listing → hidden id absent |
| Hide button gated on configured | Without credential seeded, Hide button absent everywhere |

Lockout window shrinks via `FOTOBANK_E2E_LOCKOUT_WINDOW=5s` env var on the e2e-server so the test
exercises the limiter without 5-minute waits. Production defaults stay 60s/5min.

### 3.12 e2e seed data

`cmd/e2e-server/main.go::seedFixtures` extends with:

1. `auth_hidden_credential` row for the stub principal with passcode `"e2e-passcode"`
   (Argon2id-hashed at seed time). Skipped via env-var override for the "configured-false"
   variant.
2. `hidden-prehidden-1` — owner stub principal, `hidden_at = time.Now()` at seed time. Used by
   gate-render and `/hidden` grid render tests.
3. `hidden-target-1` — owner stub principal, `hidden_at = NULL`. Used by Hide-flow test, which
   mutates it via UI.
4. Re-uses existing `pair-fixture-primary` / `pair-fixture-sidecar` for the cascade test.
5. Re-uses `E2E Italy 2025` album for the chip test.

The seed never inserts a row with `hidden_at != NULL` for the Hide-flow tests — the tests
trigger the hide via the UI so the cascade and store paths are exercised end-to-end.

### 3.13 HiddenMediaStore

`frontend/src/lib/hidden/hiddenMediaStore.svelte.ts`:

- Mirrors `MediaStore`'s shape: paginated list, route-local by-id cache, `loadInitial` /
  `loadMore` / `removeMany(ids)`.
- Loads from `GET /api/v1/hidden/media`.
- Does NOT merge into the shared `MediaStore.months`. The shared by-id cache
  (`MediaStore.byMediaId`) and the hidden by-id cache are separate. Hidden rows leaking into
  Library's grouping is the failure mode this prevents.
- Constructed inside `HiddenLibrary.svelte` (route-scoped lifetime); torn down when the route
  unmounts.

### 3.14 MediaStore extensions for F2.4

The existing `MediaStore` uses two indexes: `byId` (id → monthKey) and `byMediaId` (id → row).
F2.4 adds:

- **`mergeRaw` is hidden-aware:**
  - Hidden rows (`hidden_at != null`) are stored in `byMediaId` only; not inserted into
    `byMonth`, `byId`, or visible `months`.
  - A previously-visible row re-merged with `hidden_at != null` is removed from `byMonth` /
    `byId` / `months`; its `byMediaId` entry is updated in place (not deleted), so a subsequent
    unlocked direct-detail fetch can short-circuit on the cache.
  - A previously-hidden by-id-cached row re-merged with `hidden_at == null` is inserted into
    `byMonth` / `byId` and the appropriate visible month bucket.
- **`removeMany(ids: string[], hiddenAt: string = new Date().toISOString())`:**
  - Removes ids from `byMonth`, `byId`, and visible `months`.
  - Updates `byMediaId[id].hidden_at = hiddenAt` in place. Does NOT delete `byMediaId` entries;
    eviction is governed by the existing cache policy elsewhere.

The invariant: `mediaStore.months` only ever contains rows where `hidden_at IS NULL`. This is the
single backstop preventing leaks from direct-detail fetches that return a hidden row under a
valid unlock cookie.

---

## 4. Out of Scope, Forward Dependencies, Implementation

### 4.1 Deferred to a future phase

- **SPA Settings UI** for setup/change/disable. CLI only in v1; the HTTP surface ships in F2.4
  regardless, so a future Settings page is purely additive.
- **Search filtering for hidden.** Future search must explicitly filter `hidden_at IS NULL` in
  its query path. The `media_visible_idx` partial index supports the typical visible-only query
  shapes but is NOT a default — search code paths apply the filter explicitly. A "search hidden"
  mode will additionally require the unlock cookie at query time.
- **SSE / cross-tab live lock state.** v1 is best-effort: a second tab catches re-lock on the
  next API call (403) and re-fetches state.
- **"Unhide and share" inline flow.** Sharing a hidden photo requires unhide-then-share in v1.
- **Hidden album as a first-class concept.** An album whose own existence is concealed is
  broader than per-photo hiding and out of scope.
- **Multi-vault Hidden.** One credential, one bucket per principal in v1.
- **WebAuthn / passkey unlock as alternative to passcode.** Master spec listed it as a v2
  candidate; F2.4 lands passcode first and supersedes the v2 candidate as deferred.

### 4.2 Explicitly not on the roadmap

- **Hidden over the grantee surface, ever.** Permanent design constraint. `SharedReadService.*`
  filters unconditionally; the unlock cookie is never honored grantee-side.
- **Per-photo timed exposure** ("show this for 30 seconds, then re-hide"). Out of scope.

### 4.3 Forward dependencies (F2.5 lightbox)

- **Prev/next navigation.** Lightbox navigates within a single source list and MUST NOT bridge
  across visible/hidden lists. Library-context lightbox skips hidden naturally (absent from
  `mediaStore.months`). Hidden-context lightbox stays inside `HiddenMediaStore`. Direct-detail
  lightbox reads the by-id cache; the cookie gate on `GET /media/{id}` enforces access.
- **Actions.** Hide / Unhide / Add-to-album are bulk-shaped and accept single-id payloads. The
  lightbox re-uses `MediaActions` directly with a single-id selection.
- **Hide in lightbox** preserves source context: remove the item from the current source list
  (`mediaStore.removeMany([id])` for Library/Session, `albumDetailStore.pruneHidden([id])` for
  Album, `hiddenMediaStore.removeMany([id])` for Hidden), close the lightbox, return to the
  originating route. Direct-URL lightbox (no history entry to return to) falls back to
  `/library`.
- **Future export / backup.** Must exclude hidden by default; an "include hidden" flag (if
  added) requires the unlock cookie at export time.
- **Future passphrase rotation.** AdminReset is the floor (delete credential + revoke sessions,
  preserve hidden flags). A rotation tool would be a Change ergonomics improvement.

### 4.4 Implementation slices (~27 tasks)

| Slice | Tasks |
|---|---|
| 1. Schema fold | (1) Add `hidden_at` column + partial visible index; create credential, session (+ 2 indexes), failure (+ index), lockout tables; all FK to `owners`; folded into `000001_initial_schema`. |
| 2. hidden auth repo | (2) `internal/auth/hidden/repo.go` + tests for credential CRUD, session CRUD, failure log, lockout. |
| 3. hidden auth service | (3) Setup/Change/Disable/Unlock/Lock/AdminReset; (4) Argon2id helper + 1..1024-byte enforcement; (5) sliding-window lockout with persisted `auth_hidden_lockout`. |
| 4. hidden auth HTTP | (6) huma routes `/setup`, `/change`, `/disable`, `/unlock` (Set-Cookie), `/lock` (idempotent), `/state`; (6a) `make api-generate`; (6b) regenerate frontend TS bindings. |
| 5. CLI | (7) `fotobank hidden setup/change/disable` with stub-mode guard; (8) `fotobank admin reset-hidden-passcode [--owner hub:user]`. |
| 6. media repo | (9) `SetHiddenCascade` / `ClearHiddenCascade` (one txn, owner-scoped, sidecar cascade in SQL); (10) `IncludeHidden` flag threaded through existing list/get queries. |
| 7. media service + HTTP | (11) `media.service.Hide/Unhide` with sidecar input rejection; (12) `POST /media:hidden` (409 if not configured), `POST /media:unhide`, `GET /hidden/media` (sidecars excluded, locked sort); (12a) regenerate openapi + TS bindings. |
| 8. Shared filter | (13) `SharedReadService.*` applies `hidden_at IS NULL`; tests assert filter at every method. |
| 9. Album integration | (14) `hidden_count` on `AlbumSummary` / `AlbumDetail`; `item_count` becomes visible-only; (15) album-add accepts hidden ids when unlock cookie present; (15a) regenerate openapi + TS bindings. |
| 10. SPA HiddenStore | (16) `HiddenStore` class + boot-time `refresh()` from `App.svelte`. |
| 11. SPA HiddenLibrary | (17) `/hidden` route, `HiddenGate`, `HiddenLibrary`, `HiddenMediaStore`; sidebar entry in BROWSE. |
| 12. SPA top-bar strip | (18) `HiddenLockStrip`; visibilitychange + TTL + manual triggers; keepalive POST on tab-hidden; optimistic state clear. |
| 13. SPA media surface | (19) `MediaStore.mergeRaw` hidden-aware; (20) `MediaStore.removeMany(ids, hiddenAt?)`; (21) `MediaActions` Hide/Unhide buttons across `library`/`session`/`album`/`media-detail`/`hidden` contexts; Hide-from-MediaDetail navigates to `/library`. |
| 14. SPA album refresh | (22) `AlbumsStore.markStale`; `AlbumDetailStore.refreshMeta` + `pruneHidden`; chip rendering on tile and detail header. |
| 15. SPA toasts + router | (23) `ToastStack` minimal component + cookie-expired-mid-action surface; `router.back(fallback)`. |
| 16. e2e | (24) `cmd/e2e-server/main.go::seedFixtures` extensions (credential, `hidden-prehidden-1`, `hidden-target-1`); (25) `frontend/tests/e2e/hidden.spec.ts` covering all 15 scenarios from §3.11; (26) `FOTOBANK_E2E_LOCKOUT_WINDOW` env-var override on e2e-server. |

### 4.5 Open implementer judgment calls

1. **huma colon-path routes.** `POST /api/v1/media:hidden` and `:unhide` use the colon-action
   pattern. Confirm fotobank's huma config accepts it during slice 7; fall back to a non-colon
   shape (e.g. `POST /api/v1/media/hidden:bulk`) only if rejected.
2. **Session sweeper cadence.** 5 min default; make it a config knob only if a measured need
   arises.
3. **`ToastStack` reuse.** §3.9 introduces it as new infrastructure. If a small toast pattern
   materialized between F2.3 and F2.4, prefer the existing one over duplicating.
