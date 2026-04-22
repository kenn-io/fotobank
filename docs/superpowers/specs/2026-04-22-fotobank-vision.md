# Fotobank — Vision and Architecture Spec

**Status:** Draft (v0.1)
**Date:** 2026-04-22
**Scope:** Master spec for the redesign of fotobank from its current single-user
Python tool into a Go-based, multi-user, grant-aware personal photo platform
with a web viewer and NAS-backed storage. Subsystems referenced here produce
their own follow-up sub-specs, each of which yields a separate implementation
plan. This document settles cross-cutting decisions so sub-specs can proceed
independently without relitigating them.

---

## 1. Problem

The current fotobank is a small Python CLI (see `fotobank/` in this
repository) that organises a single user's photo collection on disk and in a
local SQLite registry. It handles import, MD5 deduplication, year-based
filename layout, and EXIF extraction via `exifread` with an `exiftool`
subprocess fallback. It has served its purpose as a single-user archival
tool, but several limits have surfaced:

1. **Single-user only.** All photos belong to one implicit owner. There is no
   mechanism for another person to be granted visibility into a subset of the
   library.
2. **CLI-only.** There is no browsing experience. Viewing photos requires a
   separate tool (Lightroom, Finder, etc.) pointed at the on-disk layout.
3. **No storage tiering.** Everything lives in one directory tree. A
   common homelab topology — a server with fast local flash and a slower NAS —
   has no way to express "keep recent photos fast, archive old photos to NAS."
4. **`exiftool` dependency.** Pure-Python EXIF extraction works for most
   files, but the `exiftool` fallback remains a hard external dependency for
   edge cases. A pure-Go implementation can eliminate it.
5. **No sharing primitives.** The tool has no notion of albums, shared
   selections, or access control. Sharing photos with family means exporting
   copies out-of-band.

**The new vision:** fotobank becomes a self-hosted personal photo platform
that manages a NAS-backed library, exposes a web viewer, and supports
multi-user scoped sharing on top of an external identity/grant broker.
Fotobank remains the owner of photo metadata and the semantics of what a
"share" means; it delegates user identity, authentication, and opaque grant
distribution to the external broker. The current Python tool is replaced
by a Go reimplementation; the existing on-disk library and SQLite registry
have a clean one-shot migration path.

## 2. Design principles

1. **Fotobank owns photo semantics; the broker owns identity.** Fotobank
   mints grant **scopes** (opaque UUIDs) representing "this album" or "these
   photos, download-allowed." The external broker authenticates users and
   tells fotobank which scope UUIDs a given user holds. Fotobank resolves
   scopes to semantic targets and filters accordingly. Fotobank never stores
   passwords, never runs OIDC, never manages sessions beyond reading trusted
   headers from its upstream reverse proxy.

2. **NAS is authoritative; flash is a cache.** Every durable byte — every
   original, every derivative thumbnail — lives on NAS. Flash storage on the
   fotobank server is a read-through cache for speed. Backup concerns apply
   to NAS, not flash. Flash loss is a performance event, not a data event.

3. **One write path, two entry points.** Fotobank ships as a single Go
   binary with two modes of invocation: an HTTP server and a CLI. Both link
   the same internal service layer directly — no subprocess calls between
   them, no duplicated write logic. Agents drive the CLI; humans drive the
   web. Both receive identical validation and invariants.

4. **Grants are pure metadata.** Creating, modifying, or revoking a share
   never moves, copies, or links filesystem bytes. Sharing is a row in
   fotobank's SQLite plus a grant registered with the external broker.

5. **Per-owner namespaces on NAS.** Each owner gets their own subtree
   (`{nas_root}/{owner_id}/…`). No cross-owner content-addressable dedup in
   v1; dedup happens within an owner's library via MD5, the same as today.
   The migration path to cross-owner dedup (a future content-addressable
   store) is preserved, not prematurely paid for.

6. **Deprecate the exiftool dependency.** The Go port uses a pure-Go EXIF
   library. No subprocess fallback, no Perl in the install path.

7. **Coexistence with Lightroom Classic.** The NAS tree remains an ordinary
   per-owner directory hierarchy that Lightroom can watch. Fotobank does not
   rename files out from under Lightroom; shares do not move bytes.

8. **Avoid premature complexity.** This spec describes architecture, trust
   boundaries, and interfaces. Concrete mechanisms (exact table DDL, HTTP
   route shapes, frontend framework choice, background-job scheduler, exact
   EXIF library) belong in subsystem sub-specs (§15).

## 3. System components

```
     browser user                           CLI user / agent
          │ HTTPS                                  │ (local shell)
          ▼                                        ▼
   ┌─────────────────────────────┐       ┌──────────────────────┐
   │  Identity-aware reverse     │       │  fotobank CLI         │
   │  proxy (external)           │       │  (same binary,        │
   │  • terminates TLS           │       │   CLI entry point)    │
   │  • authenticates user       │       └───────────┬──────────┘
   │  • strips client X-Auth-*   │                   │
   │  • injects X-Auth-User-Id   │                   │
   │    X-Auth-Hub, X-Auth-Scopes│                   │
   └─────────────┬───────────────┘                   │
                 │ trusted headers                   │
                 ▼                                   │
   ┌──────────────────────────────────────────────────────────────────┐
   │                   fotobank server (Go binary)                    │
   │  ┌──────────────────────────────────────────────────────────┐   │
   │  │ HTTP API (chi router)                                    │   │
   │  │   viewer: photos, albums, thumbs, originals              │   │
   │  │   admin:  imports, album CRUD, share CRUD                │   │
   │  └──────────────────┬───────────────────────────────────────┘   │
   │                     │                                            │
   │  ┌──────────────────▼──────────────────────────────────────┐    │
   │  │ Internal service layer                                  │    │
   │  │   IdentityProvider interface (stub | header reader)     │    │
   │  │   PhotoService / AlbumService / ShareService            │    │
   │  │   ImportService / ThumbService                          │    │
   │  └─┬──────────┬──────────────┬─────────────────┬──────────┘    │
   │    │          │              │                 │                │
   │  ┌─▼──────┐ ┌─▼──────────┐ ┌─▼────────────┐ ┌─▼────────────┐   │
   │  │metadata│ │ byte store │ │ thumb worker │ │ import       │   │
   │  │SQLite  │ │ hot/cold   │ │ (eager)      │ │ watcher      │   │
   │  └────────┘ └─┬──────────┘ └─┬───────────┘ └──────────────┘   │
   │               │              │                                 │
   └───────────────┼──────────────┼─────────────────────────────────┘
                   ▼              ▼
   ┌──────────────────────┐    ┌──────────────────────────────────┐
   │  flash (local)       │    │  NAS (authoritative)             │
   │  recent originals    │    │  /{owner_id}/YYYY/…              │
   │  all thumbnails      │    │  /{owner_id}/movies/…            │
   │  SQLite DB           │    │  /{owner_id}/.thumbs/{photo_id}/ │
   └──────────────────────┘    │  SQLite snapshots (backup)       │
                               └──────────────────────────────────┘
```

### 3.1 fotobank server (Go binary)

A single Go binary with two entry points: `fotobank-server` (long-running
HTTP service) and `fotobank` (CLI). Both link the same internal service
layer; neither shells out to the other. The CLI is the tool agents invoke
on the same host as the server.

### 3.2 Internal service layer

All domain operations — creating albums, minting scopes, resolving scopes
to photo-id sets, filtering listings by grant, recording imports, triggering
thumbnail generation — live here. HTTP handlers and CLI commands are thin
adapters that call into the service layer. The service layer is the
**single write path** principle in code.

Concrete package boundaries are a sub-spec concern (§15.1); conceptually:

- `service.PhotoService` — list / get / stream originals / delete.
- `service.AlbumService` — create, rename, add/remove photos, list, delete.
- `service.ShareService` — mint scopes, attach to albums or ad-hoc sets,
  register with external broker, revoke.
- `service.ImportService` — ingest a directory or individual file, dedup,
  write originals to NAS, enqueue thumbnail generation.
- `service.ThumbService` — generate, persist, and serve derivatives.
- `identity.Provider` — interface that returns `(hub, user_id, handle?,
  [scope_uuids])` from a request, with a header-based production impl and
  a stub impl for dev.

### 3.3 Metadata store

SQLite file on the fotobank server's local flash, accessed via `database/sql`
with a lightweight driver (e.g., `modernc.org/sqlite` for pure-Go
deployment, or `mattn/go-sqlite3` if CGO is acceptable — choice deferred to
the Go core sub-spec). SQLite is on flash, not NAS, because
networked-filesystem-backed SQLite is unreliable and slow. Periodic
snapshots are shipped to NAS for durability (§11).

### 3.4 Byte storage

Two tiers:

- **NAS (authoritative).** All originals, all derivatives. Survives flash
  loss. Backed up externally per the operator's own NAS backup policy.
- **Flash (cache).** Recent originals (by import-time recency, §4.4) and
  typically all thumbnails (small enough to fit). Read-through cache;
  misses fall back to NAS and repopulate flash.

The storage abstraction (`storage.Store`) exposes `Read(owner, key) →
Reader` and `Write(owner, key, reader) → error`, hiding the tiering from
the service layer.

### 3.5 External identity/grant broker

An independent service outside fotobank's boundary. Fotobank interacts with
it in two directions:

- **Inbound (request path):** The broker terminates TLS via a reverse proxy
  in front of fotobank, authenticates users (by whatever mechanism it
  chooses — passkey, OIDC, etc.), strips client-forged `X-Auth-*` headers,
  and injects trusted headers identifying the user and the scope UUIDs the
  user holds for this fotobank instance.
- **Outbound (admin path):** When an owner creates or revokes a share,
  fotobank registers or revokes the grant with the broker. In v1 this is
  done via a CLI/IPC mechanism exposed by whatever local daemon the broker
  provides; the exact mechanism is pluggable behind `ShareService`'s
  `BrokerRegistrar` dependency (§6.3).

For local development and for any deployment without the broker present,
fotobank ships a **dev-stub** `IdentityProvider` that injects a single
configured owner principal and grants full access. See §6.4.

### 3.6 CLI

The CLI exposes the same write verbs the HTTP admin endpoints do, plus
read verbs for scripting. Target subcommands (non-exhaustive):

```
fotobank import <directory>          # trigger ingest (as today)
fotobank albums create <name>
fotobank albums add <album_id> <photo_ids...>
fotobank albums list
fotobank shares create --album <id> --grantee <principal> [--download] [--expires <duration>]
fotobank shares create --photos <id...> --grantee <principal> [...]
fotobank shares list
fotobank shares revoke <scope_uuid>
fotobank thumbs regenerate <photo_id|--all>
```

CLI commands emit human output by default and machine-readable JSON under
`--json`, so agents can drive them reliably.

**Single-primary-owner assumption.** Although the schema supports
multiple owners, the typical v1 deployment has one primary owner (the
person whose server hosts fotobank). CLI commands act on that owner's
resources by default, configured via `FOTOBANK_PRIMARY_OWNER=<hub>:<user_id>`
in the server's environment/config. An explicit `--owner <hub>:<user_id>`
flag overrides the default for deployments that genuinely host multiple
owners. Grantees are not owners and have no CLI access — they only ever
see the library through the HTTP viewer path.

## 4. Storage layout

### 4.1 NAS namespace

Per-owner subtree under a configured NAS root:

```
{nas_root}/
├── {owner_id}/
│   ├── YYYY/
│   │   ├── YYYYMMDD_HHMMSS_0.jpg
│   │   ├── YYYYMMDD_HHMMSS_1.arw
│   │   └── …
│   ├── unknown_date/
│   │   └── …
│   ├── movies/
│   │   └── {md5}.{ext}
│   └── .thumbs/
│       └── {photo_id}/
│           ├── grid.webp       # 256px
│           ├── preview.webp    # 1024px
│           └── lightbox.webp   # 2048px
└── .fotobank/
    ├── snapshots/              # SQLite backup shipments
    └── config/                 # shared deployment config (optional)
```

`{owner_id}` is an opaque string keying the owner's principal. Typical
form in production: a UUID or a sanitised slug derived from the identity
broker's handle. In dev-stub mode: a configured value like `dev-owner`.

### 4.2 File naming

Existing convention preserved: `YYYYMMDD_HHMMSS_{seq}.{ext}` for photos,
`{md5}.{ext}` for videos, `unknown_date/` for files without usable
timestamps. `{seq}` is an integer increment to disambiguate multiple photos
in the same second. This keeps the library readable to Lightroom and
consistent with the existing Python tool.

### 4.3 Thumbnails

Three fixed sizes, WebP format:

- `grid` — 256px on the long edge, for infinite-scroll grids.
- `preview` — 1024px, for medium-detail views.
- `lightbox` — 2048px, for full-screen detail; also the largest size a
  grantee without `allow_download` can ever receive.

Keyed by **photo_id**, not by filename, so renames (rare but possible) do
not break derivatives.

Eager generation: thumbnails are built as part of `ImportService`'s
pipeline before an import is reported complete. For RAW files (ARW, CR2,
DNG, RAF, NEF, …) fotobank extracts the **embedded JPEG preview** from EXIF
and resizes from there — no `libraw` / `dcraw` dependency. If a RAW has no
embedded preview, fotobank records the photo row but flags it as
`thumb_status = 'no_preview'`; the photo is listed but renders a placeholder
in the viewer. A later sub-spec can add optional real RAW decoding.

### 4.4 Tiering policy

**Originals.** NAS holds every original. Flash caches recent imports by
recency — the last *N* days or *N* photos by `imported_at`, whichever
bound is reached first, with *N* configurable. Older imports age out of
flash but remain on NAS. Cache miss on read → pull from NAS, repopulate
flash, serve.

**Thumbnails.** NAS holds every thumbnail. Flash caches on read; for
realistic personal libraries (100k photos ≈ 100GB of thumbs at three
sizes), flash can typically hold the entire set with no eviction
pressure. Very-large libraries may evict by LRU; this is a configuration
concern, not a v1 design decision.

**Writes.** Imports write to NAS first. Only after the NAS write returns
success is the photo row committed in SQLite. Flash population is a
non-blocking follow-up — if flash write fails, the photo is still
durable and readable from NAS.

**Graceful degradation.** If no flash tier is configured, fotobank reads
and writes NAS directly. No hot-cache codepath is special-cased beyond
"maybe skip it."

## 5. Data model

This section describes interface-level concepts. Exact DDL, indexes, and
migration shape belong in the Go core sub-spec (§15.1). Identifier types
are illustrative.

### 5.1 Photos

```
photos (
  id              UUID PRIMARY KEY,
  owner_hub       TEXT NOT NULL,              -- identity broker origin
  owner_user_id   TEXT NOT NULL,              -- stable user UUID from broker
  path            TEXT NOT NULL,              -- relative, e.g. "YYYY/name.jpg"
  original_filename TEXT,                     -- source path at import time
  imported_at     TIMESTAMP NOT NULL,
  timestamp       TIMESTAMP,                  -- from EXIF, nullable
  size            BIGINT NOT NULL,
  checksum        TEXT NOT NULL,              -- MD5 hex, lowercase
  make, model, focal_length, shutter          -- EXIF text fields
  width, height, iso                          -- EXIF integers
  aperture        REAL,
  thumb_status    TEXT NOT NULL,              -- 'ready' | 'no_preview' | 'failed'
  UNIQUE (owner_hub, owner_user_id, checksum) -- within-owner dedup
)
```

The owner principal is stored inline on every row, not via a join table.
Identity caching (display handle) is optional and lives in a separate
`principal_display` table used only for UX.

### 5.2 Albums

```
albums (
  id              UUID PRIMARY KEY,
  owner_hub       TEXT NOT NULL,
  owner_user_id   TEXT NOT NULL,
  name            TEXT NOT NULL,
  created_at      TIMESTAMP NOT NULL,
  updated_at      TIMESTAMP NOT NULL
)

album_photos (
  album_id        UUID NOT NULL REFERENCES albums(id),
  photo_id        UUID NOT NULL REFERENCES photos(id),
  added_at        TIMESTAMP NOT NULL,
  position        INTEGER,                    -- nullable, for manual ordering
  PRIMARY KEY (album_id, photo_id)
)
```

Albums are strictly per-owner. A user cannot add another user's photo to
their album — if they want to "use" a photo in a share, they do it via a
scope (§5.3) targeting the original owner's photo id. Re-share grants
(granting a share onward to a third party) are out of scope for v1; see
§14 Phase 4+.

### 5.3 Scopes — grant semantics

A **scope** is what gets granted. Each scope is minted by fotobank, has an
opaque UUID, and is registered with the external broker. The broker tells
fotobank which scopes a given user holds; fotobank looks up the scope's
semantic row here and filters accordingly.

**One scope per share.** A scope represents a single share action:
one target × one grantee × one set of settings. Sharing the same album
with two people mints two scopes. This keeps each scope's meaning
narrow and the audit / revocation story one-grantee-at-a-time.

```
scopes (
  uuid             UUID PRIMARY KEY,          -- the opaque grant identifier
  owner_hub        TEXT NOT NULL,             -- the owner who created the share
  owner_user_id    TEXT NOT NULL,
  grantee_hub      TEXT NOT NULL,             -- who the share is for
  grantee_user_id  TEXT NOT NULL,
  target_type      TEXT NOT NULL,             -- 'album' | 'photo_set'
  target_album_id  UUID,                      -- when target_type = 'album'
  allow_download   BOOLEAN NOT NULL DEFAULT false,
  label            TEXT,                      -- human-visible, e.g. "Summer 2024"
  created_at       TIMESTAMP NOT NULL,
  expires_at       TIMESTAMP,                 -- nullable; null = indefinite
  revoked_at       TIMESTAMP                  -- nullable; set on revocation
)

scope_photos (
  scope_uuid       UUID NOT NULL REFERENCES scopes(uuid),
  photo_id         UUID NOT NULL REFERENCES photos(id),
  PRIMARY KEY (scope_uuid, photo_id)
)
-- Populated only when target_type = 'photo_set'.
```

A scope is immutable in the semantic sense — its `target_type`,
`target_album_id`, `scope_photos` rows, `grantee_*`, and `allow_download`
do not mutate. Changing what's accessible means minting a new scope (and
revoking the old if appropriate). This keeps the audit story simple: a
scope UUID's meaning never changes over its lifetime.

**Enforcement vs display.** The external broker is authoritative for
whether a given user actually *holds* a scope right now — it's the
broker that injects `X-Auth-Scopes` on the grantee's requests, and a
scope that the broker has revoked will simply stop appearing there.
Fotobank's local `grantee_*` fields are for the owner-facing UI ("who
has access to this album?") and for bookkeeping around expiry and
revocation; they are not consulted during request-time enforcement.
Enforcement always follows the path: broker-injected scope UUID →
local `scopes` row → local `revoked_at` / `expires_at` / target lookup.

### 5.4 Principals and identity references

A **principal** is always the tuple `(hub, user_id)`. Same-hub deployments
can treat `hub` as implicit in UI, but it is always present in storage so
cross-hub principals can be represented without schema migration.

Optional display cache:

```
principal_display (
  hub             TEXT NOT NULL,
  user_id         TEXT NOT NULL,
  handle          TEXT,                       -- e.g. "@mom@hub.example"
  cached_at       TIMESTAMP NOT NULL,
  PRIMARY KEY (hub, user_id)
)
```

Purely for UX. The broker remains authoritative; a stale handle is a
display inconvenience, not a security issue.

## 6. Identity and grants integration

### 6.1 `IdentityProvider` interface

A narrow interface that the HTTP server and CLI both depend on:

```go
type Principal struct {
    Hub      string
    UserID   string
    Handle   string   // optional, display only
}

type Identity struct {
    Principal Principal
    Scopes    []string  // scope UUIDs
    RequestID string    // optional, for correlation
}

type Provider interface {
    // Extract identity from a request (HTTP) or environment (CLI).
    Identify(ctx context.Context, r *http.Request) (Identity, error)
}
```

Production implementation: read trusted `X-Auth-*` headers from the
upstream reverse proxy. Dev-stub implementation: return the configured
owner principal with an empty scope list (the stub path is only for the
single-owner case).

Writes triggered via the CLI on the server host run under an implicit
"local admin" principal — fotobank trusts local shell access to the
same extent the operating system does. CLI callers do not carry identity
broker headers.

### 6.2 Header contract

The reverse proxy is expected to inject the following headers:

- `X-Auth-User-Id` — stable user UUID (or other stable identifier) from
  the identity broker.
- `X-Auth-Hub` — origin of the identity broker that attests to this
  user. Lets fotobank distinguish `(hub-a, uuid)` from `(hub-b, uuid)`.
- `X-Auth-Handle` — human-readable handle, for display. Optional.
- `X-Auth-Scopes` — space-separated scope UUIDs the user holds for this
  fotobank instance. May be empty.
- `X-Auth-Request-Id` — per-request correlation identifier.

The reverse proxy strips any client-forged `X-Auth-*` before adding its
own. Fotobank trusts these headers unconditionally when they arrive; it
does **not** apply additional verification of their contents. Misbehaving
proxy = broken deployment.

Exact header names are configurable per deployment. Fotobank's config
schema lets an operator rename the prefix (e.g., to match whichever
proxy is in use). Only the meaning of each field is fixed.

### 6.3 Scope registration with the external broker

When `ShareService` creates a new scope, it:

1. Inserts the semantic row in the local `scopes` table.
2. Registers the scope UUID and its label with the external broker via a
   `BrokerRegistrar` dependency.
3. Creates the grant (scope → grantee principal) via the same registrar.

`BrokerRegistrar` is an interface, not a concrete protocol:

```go
type BrokerRegistrar interface {
    RegisterScope(ctx context.Context, scope ScopeHandle) error
    CreateGrant(ctx context.Context, scope ScopeHandle, grantee Principal, opts GrantOptions) error
    RevokeGrant(ctx context.Context, scopeUUID string) error
}
```

v1 ships:

- `stub.BrokerRegistrar` — no-op for single-owner dev deployments.
- `exec.BrokerRegistrar` — shells out to a configurable broker CLI
  (`{broker_cli} scope register …`, `… grant create …`, etc.). Command
  templates are in fotobank config.

A future native-protocol implementation can be added without touching the
service layer.

### 6.4 Dev-stub `IdentityProvider`

When fotobank is started without an identity broker in front of it (local
development, single-user NAS homelab), the stub provider:

- Returns a configured single-owner principal on every request. Default:
  `hub="dev-local", user_id="owner", handle="owner"`. Configurable via env
  vars: `FOTOBANK_DEV_HUB`, `FOTOBANK_DEV_USER_ID`, `FOTOBANK_DEV_HANDLE`.
- Ignores `X-Auth-*` headers entirely (clients cannot forge a different
  principal even if they try).
- Grants the principal full access to everything owned by that principal
  — which in single-owner mode is everything.
- Refuses to start if `scopes` contains any rows (i.e., if someone has
  been sharing), to prevent accidentally collapsing a real multi-user
  deployment into single-owner mode. Overridable with an explicit
  `--unsafe-dev-stub` flag.

The stub is also useful for testing and for the migration from the
existing Python tool (§12): the legacy library has exactly one owner
by definition.

## 7. Request flow

### 7.1 Owner request

```
browser → reverse proxy → fotobank server
```

1. Proxy authenticates the owner, injects headers, forwards the request.
2. `IdentityProvider.Identify()` returns `Principal{hub, user_id}` and the
   user's scope list (often empty for the owner themselves, who accesses
   their own library through ownership, not through grants).
3. Service layer checks whether the resource's `owner_hub / owner_user_id`
   match the requester's principal. Match → full access (view + download
   + admin ops, depending on endpoint semantics).
4. Handler serves the response — metadata from SQLite, bytes from the
   storage tier (flash preferred, NAS fallback).

### 7.2 Grantee request

```
browser → reverse proxy → fotobank server
```

1. Proxy authenticates the grantee, injects headers including
   `X-Auth-Scopes`.
2. `IdentityProvider.Identify()` returns the grantee's principal and their
   scope UUIDs.
3. `ShareService.ResolveScopes(scope_uuids)` reads `scopes` rows:
   - For each scope, filter out those where `revoked_at IS NOT NULL` or
     `expires_at <= now()`.
   - Union the targets: for `target_type='album'`, expand to the album's
     `album_photos`; for `target_type='photo_set'`, expand to
     `scope_photos`. Result is a set of photo IDs.
4. Endpoint filters:
   - **List / browse:** returned photos are limited to the resolved set.
     Albums the grantee holds a scope for appear as "shared" entries.
   - **Fetch original bytes** (`/photos/{id}/original`): the endpoint
     additionally checks that at least one applicable scope has
     `allow_download = true`. If none, the response is `403`; the
     viewer is expected to use a derivative endpoint (e.g.,
     `/photos/{id}/thumb?size=lightbox`) for display of view-only
     shares. No silent fallback — that would break caching and make
     client behaviour opaque.
   - **Fetch thumbnail / derivative:** always allowed if the photo is
     in the resolved set. `grid`, `preview`, and `lightbox` sizes are
     available to any grantee whose scope resolves to this photo,
     regardless of `allow_download`.
   - **Any admin op** (create album, delete, import, create share): `403`.
     Only the owner can mutate.
5. Any photo not in the resolved set is treated as nonexistent — 404, not
   403, to avoid leaking existence.

### 7.3 Scope resolution and caching

Scope resolution is a per-request DB lookup. It is cheap (handful of rows)
and does not need a dedicated cache. Revocation is therefore immediate:
setting `revoked_at` on a scope row stops serving resources under that
scope on the next request. There is no stale-cache window.

The broker-side grant is still the source of truth for whether the user
receives the scope UUID in their headers at all. If the broker lags on
revocation (e.g., the broker still includes a revoked scope UUID in the
next header injection), fotobank's local check catches it via
`revoked_at`. This is intentional defense in depth: revoking locally and
remotely both close the grant.

### 7.4 Download gating details

`allow_download` is per-scope, not per-photo. If a grantee holds two
scopes over the same album, one with download and one without, they get
download access (union of capabilities). The viewer surfaces the
capability so the UI can show a disabled download button for view-only
shares.

## 8. Write flows

All writes go through the internal service layer. HTTP handlers and CLI
commands are thin adapters.

### 8.1 Import

- **CLI:** `fotobank import <directory>` (matches current tool's entry
  point). Walks the directory, computes checksums, extracts EXIF, writes
  originals to NAS at the canonical path, enqueues thumbnail generation,
  commits photo rows. Dedup (within-owner MD5) skips already-imported files.

- **Watched folder:** `ImportService` can be configured with one or more
  inbox directories. A background watcher (filesystem-level `fsnotify`
  plus a periodic scan for NAS-mounted inboxes where inotify is unreliable)
  triggers the same ingest pipeline on new arrivals. After successful
  import, the source file is removed from the inbox by one of two
  configurable post-processing modes: **`move`** (relocate to an
  `imported/` subdirectory alongside the inbox — safer, keeps an audit
  trail until the operator clears it) or **`delete`** (remove outright —
  tidier). Files that fail import are moved to a `failed/` subdirectory
  with an adjacent `.error` file explaining why, never silently dropped.

Web upload is **out of scope for v1.** Large uploads from a browser need
chunked upload, progress UI, staging directory handling, and mobile
considerations — all worth doing later but not load-bearing for the
initial product.

### 8.2 Album management

Both CLI and web. The service methods are the same; the entry points
differ. Writes include create, rename, add photos, remove photos,
reorder, delete.

Agent-driven album creation is a first-class use case. The CLI's `--json`
output on album-returning commands emits stable, parseable JSON so agents
can chain operations reliably.

### 8.3 Share management

Both CLI and web. Creating a share mints a scope, registers it with the
broker, creates the grant. Revoking a share sets `revoked_at`, deregisters
the grant with the broker.

Web UX for share management is out of scope in *this* vision doc — it's a
web-frontend sub-spec concern. The service-layer API it will consume is
fixed here.

## 9. EXIF and metadata

### 9.1 Pure-Go EXIF

The Go port drops `exiftool` and `exifread`. A pure-Go library (candidates:
`github.com/dsoprea/go-exif/v3`, `github.com/rwcarlsen/goexif/exif` — choice
deferred to the core sub-spec) replaces both.

No subprocess fallback. If EXIF extraction fails for a file, the photo is
imported with minimal metadata: MD5, size, dimensions if derivable from the
image container, `timestamp = NULL`, `thumb_status` computed from whether a
preview exists. The file is still browsable and searchable by import
time; it just lands under `unknown_date/` on NAS.

### 9.2 Supported formats

Preserved from the current tool:

- **Photos (EXIF extracted):** JPG, JPEG, GIF, PNG, HEIC, ARW, RAF, DNG,
  CR2, NEF. (HEIC and PNG are new additions; the current Python tool
  handles the rest.)
- **Movies (checksum only):** MP4, AVI, MOV, MP2, MPG, M4V. Timestamp
  extraction from video container metadata is a future enhancement.

### 9.3 Timestamps, dimensions, camera metadata

Field mapping follows the current Python tool's conventions: primary
`DateTimeOriginal`, fallback `DateTimeDigitized`, then `DateTime`. Width
and height prefer EXIF `ExifImageWidth`/`Length`, falling back to the
container. Camera `Make`/`Model`, `FocalLength`, `ISO`, `ShutterSpeed`,
`FNumber` are stored as received; missing fields become `NULL` in the
new schema (the Python tool used sentinel strings like `'unknown'`; the
Go port prefers proper NULLs).

## 10. Thumbnails and derivatives

Summarised in §4.3 and §4.4; details belong in the thumbnail sub-spec
(§15.5). High-level:

- Generation is eager, part of the import pipeline. Import is reported
  complete only after thumbnails are written to NAS. Flash population is
  async.
- Three sizes: `grid` (256px), `preview` (1024px), `lightbox` (2048px).
  All WebP.
- RAW input: extract embedded JPEG preview, resize. No `libraw` dependency.
- `thumb_status` on the photo row tracks generation outcome.
- Regeneration command: `fotobank thumbs regenerate <id|--all>`. Useful if
  sizing policy changes or a generation bug is fixed.

## 11. Backup and durability

- **NAS is authoritative for bytes.** Operators back up NAS via their own
  mechanism (NAS-level snapshots, rsync to offsite, etc.). Fotobank does
  not take responsibility for byte durability beyond writing to NAS.

- **SQLite lives on flash.** A background job periodically snapshots SQLite
  (via `VACUUM INTO` or the SQLite backup API) to
  `{nas_root}/.fotobank/snapshots/{timestamp}.sqlite`, keeping the last *N*
  snapshots. Default interval and retention: sub-spec concern.

- **Thumbnails are regeneratable but persisted.** Kept on NAS so flash
  loss doesn't cost hours of CPU and NAS-read bandwidth. If a thumbnail
  is lost from both tiers, the import pipeline's regeneration path can
  reconstruct it from the original.

- **Flash loss scenario.** Recoverable: restore the latest SQLite snapshot
  from NAS, reattach the storage tier, allow the cache to warm naturally
  on reads. No data loss if the snapshot is current enough.

- **NAS loss scenario.** Catastrophic. Fotobank relies on the operator's
  NAS backup policy.

## 12. Migration from the existing Python tool

The current Python fotobank library has exactly one implicit owner and a
known on-disk layout (`{base}/YYYY/…`, `{base}/movies/…`,
`{base}/registry.sqlite`). Migration is a one-shot operation:

1. Operator runs `fotobank migrate --from-legacy {legacy_base}
   --owner {owner_id}`.
2. The migrator reads `registry.sqlite`, maps columns to the new schema,
   stamps every row with the given owner principal (hub + user_id).
3. Filesystem bytes move from `{legacy_base}/YYYY/…` to
   `{nas_root}/{owner_id}/YYYY/…`. Option to symlink in place instead of
   physically moving — useful if the legacy base is already on the NAS.
4. Thumbnail generation is enqueued for all migrated photos.
5. The new SQLite is written to the fotobank server's flash.
6. The legacy SQLite is not modified; the operator deletes it once they've
   verified the new installation.

Migration runs with the dev-stub identity provider (single-owner mode).
After migration, the operator can attach an identity broker and start
sharing without re-importing.

## 13. Threat model (summary)

**In scope for v1:**

- Fotobank trusts its upstream reverse proxy absolutely. If the proxy is
  honest about the `X-Auth-*` headers, fotobank's grant enforcement is
  correct.
- Grantees cannot access photos outside their resolved scope set, even by
  guessing photo IDs — filter is at the service layer, not the HTTP
  layer, and unauthorised photos return 404.
- Grantees cannot access originals of view-only shares; they receive
  `lightbox`-size derivatives at most. This is enforcement, not defence
  against screen capture.
- Grantees cannot mutate anything (create/delete albums, import, share).
- Revocation is effective immediately (by `revoked_at` check on the next
  request). No stale-cache window on the fotobank side.
- Cross-owner access is impossible by construction: the NAS layout is
  per-owner, SQLite rows carry owner tuples, and listings always filter
  by owner.

**Out of scope for v1:**

- Defence against a compromised reverse proxy or identity broker. Fotobank
  trusts them; hardening is their problem.
- Defence against a malicious operator with shell access to the fotobank
  server. Local shell implies full admin.
- Client-side capture / screenshot of view-only photos. Not a solvable
  problem at this layer.
- Per-photo metadata redaction (e.g., stripping EXIF GPS from shared
  originals). Future enhancement.
- Rate limiting and abuse handling. Out-of-process concern (reverse
  proxy or external WAF).

**Availability notes:**

- Identity broker outage: fotobank cannot identify new requests. Sessions
  already established at the proxy continue to work for their TTL if the
  proxy is caching; fotobank itself does not cache headers. Owner CLI
  access is unaffected (CLI uses local-admin path, not the broker).
- NAS outage: fotobank degrades to "metadata-only" mode. Listings still
  work; originals cannot be served until NAS returns. Thumbnails served
  from flash cache remain available for photos whose thumbs were cached.
- Flash loss: metadata-only outage until SQLite snapshot is restored.

## 14. Phasing roadmap

### Phase 1 — Go core and CLI parity

Rewrite fotobank in Go with:

- Multi-user schema (owner principals on every row), even though only one
  owner exists initially.
- Per-owner NAS layout.
- Pure-Go EXIF.
- SQLite metadata store on flash with NAS snapshots.
- Thumbnail generation pipeline, three sizes, WebP.
- CLI parity with the current tool (`import`, `sync-metadata`) plus
  `albums`, `shares`, `migrate`, `thumbs` subcommands.
- Dev-stub `IdentityProvider`. No real broker integration yet.
- Migration from the legacy Python library.

Phase 1 is complete when the Python tool can be deleted and a dev-stub
single-owner deployment matches or exceeds current functionality.

### Phase 2 — HTTP API and identity integration

- HTTP server with viewer and admin endpoints (JSON REST).
- Header-based `IdentityProvider` implementation.
- `exec.BrokerRegistrar` shelling out to the broker's CLI for scope
  registration and grant create/revoke.
- Storage tiering (flash cache + NAS authoritative) fully wired.
- Watched-folder import.

Phase 2 is complete when a multi-owner deployment behind a real identity
broker can create and revoke shares with human grantees.

### Phase 3 — Web frontend

- Browser viewer (framework choice deferred to the web sub-spec).
- Share-sheet UX for creating/revoking grants.
- Album management UX.
- Lightroom-coexistence verification testing.

### Phase 4+ (future)

- Web upload UX.
- RAW decoding beyond embedded preview.
- EXIF GPS redaction on shared originals.
- Saved-query grants (dynamic membership).
- Re-share grants.
- Perceptual-hash deduplication.
- Face detection / clustering (local ML).
- Cross-owner content-addressable store (B → C migration described in §2).
- Transactional metadata edits with undo.
- Mobile companion apps.

## 15. Sub-spec decomposition

Each of the following produces its own design sub-spec and implementation
plan. Roughly ordered by Phase 1 dependency.

1. **Go core data model and package layout.** Exact table DDL, indexes,
   migration framework (goose / golang-migrate), Go package boundaries
   (`cmd/`, `internal/…`), error-type taxonomy, config schema.
2. **EXIF and import pipeline.** Library choice, tag extraction, format
   coverage, RAW embedded-preview extraction, error handling.
3. **Thumbnail service.** WebP encoder choice, resize pipeline, worker
   pool sizing, regeneration command, handling of `thumb_status` states.
4. **Storage tiering.** `storage.Store` interface, flash eviction policy,
   NAS-write-then-commit ordering, graceful degradation when either tier
   is unavailable.
5. **CLI UX.** Complete subcommand tree, flag conventions, `--json`
   schemas for agent use, exit-code discipline, shell completion.
6. **Migration from legacy Python tool.** Walkthrough of source schema,
   filesystem move vs symlink option, verification, rollback plan.
7. **HTTP API and web server.** Route map, request/response schemas, error
   envelope, identity-provider plumbing, broker-registrar plumbing, CORS,
   logging, metrics.
8. **Identity and broker integration.** Header contract details, stub
   implementation, `exec.BrokerRegistrar` command templating, how the
   operator configures which broker CLI is used.
9. **Web frontend.** Framework (likely a conservative React/Vite stack,
   to be decided), grid view, lightbox, album UX, share UX,
   authentication UX that defers to the broker's login page.
10. **Backup and disaster recovery.** SQLite snapshot cadence, retention,
    restore procedure, NAS loss runbook, flash loss runbook.
11. **Observability.** Structured logging, request IDs, metric shape,
    optional tracing.
12. **Upgrade and deploy.** Single-binary packaging, config migration
    between versions, schema migration semantics on server start.

## 16. Open questions

Tracked for resolution during sub-specs or spec review.

1. **Exact SQLite driver** — `modernc.org/sqlite` (pure Go, slower) vs
   `mattn/go-sqlite3` (CGO, faster). Pick per Phase 1 performance needs.
2. **EXIF library choice.** Maintained options in 2026 differ from earlier
   surveys. Benchmark before committing.
3. **WebP encoder.** `kolesa-team/go-webp` (wraps libwebp via CGO) vs
   pure-Go options. Quality / speed tradeoff.
4. **HTTP router.** `chi` is the stated default; `net/http` with Go 1.22+
   routing is also viable. Not load-bearing either way.
5. **Config file format.** TOML vs YAML vs JSON. TOML leans with Go
   ecosystem norms.
6. **Handle change in the identity broker.** If an owner's handle
   changes upstream, what happens to cached handles and to
   human-readable labels in share UI? Display-only refresh is probably
   fine but worth confirming.
7. **Per-photo redaction policy.** When sharing, should EXIF GPS be
   stripped from served originals and thumbnails by default? Defaulting
   to "strip" is safer but surprising to power users. Tracked for v2.
8. **Cross-hub principals.** The schema supports them; the v1 broker
   integration may not. Confirm the broker path exercises the
   `(hub, user_id)` tuple consistently.
9. **Deletion semantics.** Current Python tool's `sync-metadata` removes
   rows whose files have disappeared. The Go port should offer the same,
   plus explicit `delete` that removes bytes and rows atomically. Sub-spec
   clarifies the exact UX and what happens to derivatives.
10. **Import on NAS-mounted inbox.** `fsnotify` on NAS is unreliable
    across SMB / NFS variants. The watched-folder design must specify a
    periodic-scan fallback and reconcile both triggers.

---

*End of vision spec.*
