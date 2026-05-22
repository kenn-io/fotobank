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
   (`{nas_root}/{owner_storage_key}/…`). No cross-owner content-addressable
   dedup in v1; dedup happens within an owner's library via MD5, the same
   as today. The migration path to cross-owner dedup (a future
   content-addressable store) is preserved, not prematurely paid for.

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
   ┌──────────────────────┐    ┌────────────────────────────────────────┐
   │  flash (local)       │    │  NAS (authoritative)                   │
   │  recent originals    │    │  /{owner_storage_key}/YYYY/…           │
   │  all thumbnails      │    │  /{owner_storage_key}/movies/…         │
   │  SQLite DB           │    │  /{owner_storage_key}/.thumbs/{media_id}/ │
   └──────────────────────┘    │  SQLite snapshots (backup)             │
                               └────────────────────────────────────────┘
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

The storage abstraction (`storage.Store`) hides the tiering from the
service layer. Its interface, at a minimum:

- `Stat(owner, key) → StoreInfo` — size, modtime, tier hint for
  `ETag` / `Last-Modified` headers.
- `ReadRange(owner, key, offset, length) → io.ReadCloser` — covers both
  plain reads (`offset=0, length=-1`) and HTTP `Range` requests used
  by video playback and resumable downloads. Callers do not need a
  separate `Seek`-able handle; range reads are the one primitive.
- `Write(owner, key, reader) → (key, error)` — writes via the atomic
  `*.tmp-*` + rename sequence (§4.4); returns the final key in case
  the store has to disambiguate (e.g., `_{seq}` bump).
- `Delete(owner, key) → error` — used by media deletion and reconcile.
  Deletes from both tiers; a flash-only delete is an internal operation
  not exposed.

Range-read support is load-bearing for videos: browsers always issue
`Range` requests for `<video>` playback. Serving a video via
whole-file reads breaks seeking and memory on large files.

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
fotobank albums add <album_id> <media_ids...>
fotobank albums list
fotobank shares create --album <id> --grantee <principal> [--download] [--expires <duration>]
fotobank shares create --media <id...> --grantee <principal> [...]
fotobank shares list
fotobank shares revoke <scope_uuid>
fotobank thumbs regenerate <media_id|--all>
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

Per-owner subtree under a configured NAS root. Directory names use
`{owner_storage_key}` — the `owners.storage_key` column (§5.1), a
path-safe ASCII slug registered per owner at init/migration time.
Decoupling the path slug from the broker-managed user ID means
upstream handle changes never require a filesystem rename.

```
{nas_root}/
├── {owner_storage_key}/
│   ├── YYYY/                   -- photos by timestamp year
│   │   ├── YYYYMMDD_HHMMSS_0.jpg
│   │   ├── YYYYMMDD_HHMMSS_1.arw
│   │   └── …
│   ├── unknown_date/
│   │   └── …
│   ├── movies/                 -- videos stored content-addressed
│   │   └── {md5}.{ext}
│   └── .thumbs/                -- derivatives keyed by media_id
│       └── {media_id}/
│           ├── grid.webp       # 256px
│           ├── preview.webp    # 1024px
│           └── lightbox.webp   # 2048px (videos: poster frame only)
└── .fotobank/
    ├── snapshots/              -- SQLite backup shipments
    └── config/                 -- shared deployment config (optional)
```

Videos keep the existing `movies/{md5}.{ext}` layout inherited from
the current Python tool — videos often lack reliable creation
timestamps, content-addressed naming is stable either way, and this
keeps the Lightroom coexistence story unchanged.

### 4.2 File naming

Existing convention preserved: `YYYYMMDD_HHMMSS_{seq}.{ext}` for photos,
`{md5}.{ext}` for videos (content-addressed, year-independent),
`unknown_date/` for **photos** without usable timestamps. `{seq}` is an
integer increment to disambiguate multiple photos in the same second.
This keeps the library readable to Lightroom and consistent with the
existing Python tool.

### 4.3 Thumbnails

Three fixed sizes, WebP format:

- `grid` — 256px on the long edge, for infinite-scroll grids.
- `preview` — 1024px, for medium-detail views.
- `lightbox` — 2048px, for full-screen detail; also the largest size a
  grantee without `allow_download` can ever receive.

Keyed by **media_id**, not by filename, so renames (rare but possible) do
not break derivatives.

**Eager-enqueue, async-generate.** Thumbnail generation is scheduled
as part of every successful import but runs asynchronously in a
background worker; see §10 for the full lifecycle and the
`thumb_status` state machine. The media row is committed with
`thumb_status='pending'` and becomes immediately listable; thumbnails
arrive shortly after.

For RAW files (ARW, CR2, DNG, RAF, NEF, …) the worker extracts the
**embedded JPEG preview** from EXIF and resizes from there — no
`libraw` / `dcraw` dependency. If a RAW has no embedded preview, the
worker sets `thumb_status='no_preview'` and the viewer renders a
placeholder. A later sub-spec can add optional real RAW decoding.

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

**Write ordering and atomicity.** Imports follow a strict sequence that
keeps NAS and SQLite reconcilable even under crash:

1. **Dedup probe.** Compute MD5 on the source file. If
   `(owner, checksum)` already exists in `media`, skip (noop import).
2. **Resolve canonical path.** From the owner's storage root and the
   media's timestamp, compute the candidate path. If a file exists at
   that path with a different checksum, increment the `_{seq}` suffix
   until a free slot is found. The DB also enforces a
   `UNIQUE (owner_hub, owner_user_id, path)` constraint so a concurrent
   importer cannot claim the same slot.
3. **Write to temp path.** Stream the source bytes to
   `{canonical_path}.tmp-{import_id}` on NAS. Verify the written length
   matches the source length.
4. **Atomic rename.** `rename(tmp, canonical)` — atomic on POSIX same-
   filesystem. If this fails, the temp file is cleaned up in a `defer`
   and the import aborts with no partial state.
5. **Commit DB row.** Open a transaction, insert the `media` row
   (including the final `path`), commit.
6. **Post-commit side effects.** Enqueue thumbnail generation; async
   flash cache population. Failures here do not roll back the commit —
   the media item is durably stored and visible; thumbnails and cache
   warming are eventually-consistent.

**Failure windows.** Two narrow windows remain:

- Step 4 succeeds, step 5 fails (rare: SQLite commit after NAS rename).
  An orphan byte exists on NAS with no DB row.
- Step 3 partial-write followed by a crash. A stale `*.tmp-*` file
  exists on NAS.

Both are recoverable by `fotobank reconcile` (§8.1), which walks the
NAS tree and the `media` table and reports discrepancies:

- Canonical file on NAS, no DB row → candidate orphan.
- DB row, no file on NAS → the existing "sync-metadata" case;
  operator confirms and the row is deleted (or restored from backup).
- `*.tmp-*` files older than a grace period → candidate leftover from a
  crashed import.

Reconcile never deletes unilaterally; it reports and offers
`--commit-deletes` / `--commit-recoveries` flags gated on operator
review.

**Concurrent imports.** A file lock at `{nas_root}/.fotobank/import.lock`
serialises imports per deployment. Combined with the per-row DB
`UNIQUE(owner, path)` and `UNIQUE(owner, checksum)` constraints, this
prevents both sequencing races on filename slots and duplicate rows
if two importers race on the same source file. Concurrent *reads*
(viewer traffic) are unaffected.

**Flash population.** Writing to flash is a non-blocking follow-up to
a successful commit. Flash write failures are logged but do not abort
the import — the media item remains durable and readable from NAS.

**Graceful degradation.** If no flash tier is configured, fotobank reads
and writes NAS directly. No hot-cache codepath is special-cased beyond
"maybe skip it."

## 5. Data model

This section describes interface-level concepts. Exact DDL, indexes, and
migration shape belong in the Go core sub-spec (§15.1). Identifier types
are illustrative.

### 5.1 Principals and owners

A **principal** is always the tuple `(hub, user_id)` — the hub origin
plus a stable identifier from that hub. Same-hub deployments can treat
`hub` as implicit in UI, but the tuple is always materialised in storage
so cross-hub principals can be represented without schema migration.

An **owner** is a principal that owns media hosted by this fotobank
deployment. Owners have one registered row each; every other table
references them by principal tuple (FK to `owners(hub, user_id)`).

```
owners (
  hub             TEXT NOT NULL,
  user_id         TEXT NOT NULL,
  storage_key     TEXT NOT NULL UNIQUE,       -- path-safe slug used in NAS paths
  display_handle  TEXT,                       -- cached from broker for UI
  created_at      TIMESTAMP NOT NULL,
  PRIMARY KEY (hub, user_id)
)
```

**`storage_key`** is the on-disk directory name — an ASCII path-safe
string like `wes` or `ac3e2df7` or `wes-mckinney`. It is decoupled from
the principal tuple so an owner's handle can change upstream without
having to rename NAS directories. Uniqueness is enforced at the DB
level. The owner registers their desired `storage_key` at fotobank init
(or migration) time; changing it later requires a deliberate filesystem
move and DB update.

**`display_handle`** is a cache of the broker's current handle for this
owner, for share-UI labels. Non-authoritative; refreshed opportunistically.

A separate display cache serves non-owner principals (grantees) the same
way:

```
principal_display (
  hub             TEXT NOT NULL,
  user_id         TEXT NOT NULL,
  handle          TEXT,                       -- e.g. "@mom@hub.example"
  cached_at       TIMESTAMP NOT NULL,
  PRIMARY KEY (hub, user_id)
)
```

Purely for UX. The broker is authoritative; a stale handle is a display
inconvenience, not a security issue.

### 5.2 Media

Photos and videos share a single `media` table differentiated by
`media_type`. EXIF fields are nullable; video-specific fields
(`duration_ms`) are likewise nullable for photos.

```
media (
  id               UUID PRIMARY KEY,
  owner_hub        TEXT NOT NULL,
  owner_user_id    TEXT NOT NULL,
  media_type       TEXT NOT NULL,             -- 'photo' | 'video'
  mime_type        TEXT NOT NULL,             -- e.g. 'image/jpeg', 'video/mp4'
  path             TEXT NOT NULL,             -- relative, owner-namespace
  original_filename TEXT,                     -- source path at import time
  imported_at      TIMESTAMP NOT NULL,
  timestamp        TIMESTAMP,                 -- creation date (EXIF / container)
  size             BIGINT NOT NULL,
  checksum         TEXT NOT NULL,             -- MD5 hex, lowercase

  -- Photo-specific EXIF (nullable for videos)
  make             TEXT,
  model            TEXT,
  focal_length     TEXT,
  shutter          TEXT,
  width            INTEGER,
  height           INTEGER,
  iso              INTEGER,
  aperture         REAL,

  -- Video-specific (nullable for photos)
  duration_ms      INTEGER,

  thumb_status     TEXT NOT NULL,             -- see §10: pending | working | ready | no_preview | failed
  thumb_claimed_at TIMESTAMP,                  -- lease stamp when status = 'working'

  FOREIGN KEY (owner_hub, owner_user_id) REFERENCES owners(hub, user_id),
  UNIQUE (owner_hub, owner_user_id, checksum), -- within-owner dedup
  UNIQUE (owner_hub, owner_user_id, path)      -- canonical path claim
)
```

The owner principal is stored inline on every row as FK into `owners`,
not duplicated in a join table. Dedup is within-owner by MD5.

**Videos in v1.** Videos appear in the web viewer alongside photos,
sorted by timestamp where available (NULL-timestamp videos sort last
or into a dated-unknown bucket — UX detail). The grid shows a poster
thumbnail (extracted frame) generated asynchronously like photo
thumbnails; clicking plays the video via the browser's native
`<video>` element, served directly from NAS. No transcoding in v1 —
codecs the browser does not support render as an "unsupported"
placeholder with a download link (subject to `allow_download`).

**Video on-disk layout.** Videos always live at
`{owner_storage_key}/movies/{md5}.{ext}`, regardless of whether a
timestamp was recovered. Content-addressed naming is the stable
layout inherited from the Python tool; the `timestamp` column on the
`media` row carries the extracted creation time (best-effort, see
§9.2), and the viewer, not the filesystem, resolves ordering. Videos
never land under `unknown_date/` — that path applies only to photos
whose filesystem name is timestamp-derived.

### 5.3 Albums

Albums group media items for browsing and sharing. Strictly per-owner —
a user cannot add another user's media to their album.

```
albums (
  id              UUID PRIMARY KEY,
  owner_hub       TEXT NOT NULL,
  owner_user_id   TEXT NOT NULL,
  name            TEXT NOT NULL,
  created_at      TIMESTAMP NOT NULL,
  updated_at      TIMESTAMP NOT NULL,
  FOREIGN KEY (owner_hub, owner_user_id) REFERENCES owners(hub, user_id)
)

album_media (
  album_id        UUID NOT NULL REFERENCES albums(id) ON DELETE CASCADE,
  media_id        UUID NOT NULL REFERENCES media(id)  ON DELETE CASCADE,
  added_at        TIMESTAMP NOT NULL,
  position        INTEGER,                    -- nullable, for manual ordering
  PRIMARY KEY (album_id, media_id)
)
```

**Owner-consistency constraint.** An `album_media` row is only valid if
the album and the media item share the same owner. This cannot be
expressed as a plain FK in portable SQL, so it is enforced by a DB
trigger on insert/update rather than at the service layer alone — a
defence-in-depth measure against service-layer bugs. Violating the
constraint aborts the transaction.

Re-share grants (granting a share onward to a third party) are out of
scope for v1; see §14 Phase 4+.

### 5.4 Scopes — grant semantics

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
  target_type      TEXT NOT NULL,             -- 'album_live' | 'media_set'
  target_album_id  UUID,                      -- when target_type = 'album_live'
  allow_download   BOOLEAN NOT NULL DEFAULT false,
  label            TEXT,                      -- human-visible, e.g. "Summer 2024"
  created_at       TIMESTAMP NOT NULL,
  expires_at       TIMESTAMP,                 -- nullable; null = indefinite
  revoked_at       TIMESTAMP,                 -- nullable; set on revocation
  broker_status    TEXT NOT NULL,             -- 'pending' | 'active' | 'failed'
                                              -- | 'revoking' | 'revoked_remote'
  broker_registered_at TIMESTAMP,             -- RegisterScope succeeded
  broker_granted_at    TIMESTAMP,             -- CreateGrant succeeded
  broker_revoked_at    TIMESTAMP,             -- RevokeGrant succeeded
  broker_last_error TEXT,                     -- last error message when failed
  broker_attempts  INTEGER NOT NULL DEFAULT 0,
  FOREIGN KEY (owner_hub, owner_user_id) REFERENCES owners(hub, user_id)
)

scope_media (
  scope_uuid       UUID NOT NULL REFERENCES scopes(uuid) ON DELETE CASCADE,
  media_id         UUID NOT NULL REFERENCES media(id)    ON DELETE CASCADE,
  PRIMARY KEY (scope_uuid, media_id)
)
-- Populated only when target_type = 'media_set'.
```

As with `album_media`, an **owner-consistency trigger** enforces that
every row in `scope_media` has the same owner as the scope. The
scope's `target_album_id` (when present) likewise must reference an
album owned by the same principal.

**Binding immutability, membership liveness.** The scope's *binding*
— its `target_type`, `target_album_id`, `scope_media` rows,
`grantee_*`, `allow_download` — is immutable for the life of the
scope. The *resolved membership* is not, and this depends on the
target type:

- **`album_live`.** The grantee sees the album's current members at
  each request. Adding a media item to the album later makes it
  visible to the grantee automatically. Removing an item hides it.
  This matches consumer expectations for shared albums (Google
  Photos, iCloud, Lightroom Shared Albums) but means owners must be
  aware that "share this album" is a standing grant over future
  additions. The owner-facing share UI surfaces this explicitly.
- **`media_set`.** A fixed set of media IDs at mint time. Adding
  media to other albums, or modifying `album_media`, does not
  change what this grantee can see. The set is frozen in
  `scope_media`.

If an owner wants a snapshot of an album's current contents without
future additions, they create a `media_set` scope from the album's
current members. Album snapshots as a first-class `target_type` are
tracked as a potential future variant (§16).

**Enforcement vs display.** The external broker is authoritative for
whether a given user actually *holds* a scope right now — it's the
broker that injects `X-Auth-Scopes` on the grantee's requests, and a
scope that the broker has revoked will simply stop appearing there.
Fotobank's local `grantee_*` fields are used for the owner-facing UI
("who has access to this album?"), for expiry/revocation bookkeeping,
**and** for a defence-in-depth principal check at request time
(§7.2) — the requester's principal must equal `grantee_*` for the
scope to apply. The service-layer enforcement flow:

  broker-injected scope UUID → local `scopes` row
  → verify `revoked_at IS NULL` and `expires_at` not passed
  → verify requester principal equals `(grantee_hub, grantee_user_id)`
  → resolve `target_type` to media IDs.

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

**Direct-access guard (load-bearing invariant).** Trusting incoming
`X-Auth-*` headers is only sound if fotobank cannot be reached by
anything *other* than the stripping/injecting proxy. Otherwise any
client on the network can forge identity. When the header-based
provider is selected, fotobank **must** run with at least one of the
following configured; startup fails closed if none is set:

1. **Loopback-only bind.** HTTP listener on `127.0.0.1:<port>` or a
   Unix domain socket. The proxy, co-located on the same host,
   connects locally. External clients have no route to fotobank.
2. **Trusted-proxy CIDRs.** Listener binds to a routable address, but
   fotobank rejects any connection whose source address is outside a
   configured allowlist of proxy CIDRs. Useful when the proxy is on
   a separate host in a known subnet.
3. **Shared proxy secret.** The proxy sends a configured
   `X-Auth-Proxy-Secret` header on every request; fotobank rejects any
   request without a matching value. Constant-time compared. Useful
   when CIDRs are not stable (container orchestration) but co-located
   bind is not possible.
4. **Proxy mTLS.** The proxy presents a client certificate issued by
   a configured CA when connecting to fotobank. Highest-assurance
   option; also the most operational overhead.

These are mutually compatible — an operator can combine, say, a
trusted-CIDR bind with mTLS for defence in depth. The config schema
captures which mode(s) are active and refuses to start if the selected
identity provider is `header` and none of the above is configured.

The dev-stub provider imposes no direct-access guard because it does
not trust any header contents. Attackers forging `X-Auth-*` headers
while the stub is active are ignored; requests still resolve to the
single configured owner.

### 6.3 Scope registration with the external broker

Scope creation is an **outbox-backed** operation. The local DB is the
single write target inside the user-visible transaction; broker calls
happen afterwards and are retried if they fail.

```go
type BrokerRegistrar interface {
    // All three methods MUST be idempotent by scope UUID (and for
    // CreateGrant, by the (scope_uuid, grantee) pair). Repeated
    // calls with the same arguments must either succeed or return a
    // well-defined already-exists signal that the caller treats as
    // success. The outbox worker relies on this to retry safely
    // after partial failure.
    RegisterScope(ctx context.Context, scope ScopeHandle) error
    CreateGrant(ctx context.Context, scope ScopeHandle, grantee Principal, opts GrantOptions) error
    RevokeGrant(ctx context.Context, scopeUUID string) error
}
```

v1 ships:

- `stub.BrokerRegistrar` — no-op for single-owner dev deployments.
  Scopes it returns success for flip straight to `broker_status='active'`
  (there is no remote state to diverge). Idempotent by construction.
- `exec.BrokerRegistrar` — shells out to a configurable broker CLI
  (`{broker_cli} scope register …`, `… grant create …`, etc.). Command
  templates are in fotobank config. The broker CLI is expected to be
  idempotent; if it is not, the registrar implementation must probe
  first (e.g., `broker_cli scope show` before `scope register`) to
  avoid duplicate-creation errors on retry. Non-idempotent broker CLIs
  are a configuration error.

A future native-protocol implementation can be added without touching
the service layer.

**Fine-grained progress columns** on the `scopes` row support the
outbox worker in skipping already-done steps even when the broker
happens not to be idempotent:

```
broker_registered_at  TIMESTAMP,    -- set when RegisterScope succeeds
broker_granted_at     TIMESTAMP,    -- set when CreateGrant succeeds
broker_revoked_at     TIMESTAMP     -- set when RevokeGrant succeeds
```

The worker's per-scope decision: if `broker_registered_at IS NULL`,
call `RegisterScope`; then if `broker_granted_at IS NULL`, call
`CreateGrant`. For a revocation: call `RevokeGrant` if
`broker_revoked_at IS NULL`. These columns reduce redundant calls in
the common case — they do **not** relax the idempotency requirement
on the interface. A crash between a successful remote call and the
local UPDATE writing the timestamp is recoverable only if the next
retry is a safe no-op, so idempotency remains mandatory regardless of
what the progress columns currently say.

**Share create flow:**

1. `ShareService.CreateShare(...)` runs a DB transaction: insert the
   `scopes` row with `broker_status='pending'`, insert any
   `scope_media` rows. Commit.
2. A **broker-sync worker** reads `broker_status='pending'` rows and
   calls `RegisterScope` then `CreateGrant`. On success, updates
   `broker_status='active'`. On failure, records `broker_last_error`,
   bumps `broker_attempts`, and either re-queues (transient errors,
   exponential backoff) or flips to `broker_status='failed'` after a
   configured maximum of attempts.
3. The CLI and HTTP share-create endpoints return the scope row as
   soon as step 1 commits — the grantee will not receive the scope
   until the broker reflects it, but the owner has a stable handle
   to the share immediately and can track its state.

**Share revoke flow** is symmetric. `RevokeShare` sets
`revoked_at = now()` and `broker_status = 'revoking'` in a local
transaction. The worker calls `RevokeGrant` on the broker; on
success, `broker_status = 'revoked_remote'`. Local scope enforcement
(§7.2) already refuses the scope by virtue of `revoked_at`, so
revocation is effective for fotobank-served traffic immediately; the
worker flow exists to propagate revocation to the broker so the
scope UUID stops appearing in the grantee's `X-Auth-Scopes` header.

**Owner-visible states.** The share-list UI surfaces non-`active`
scopes explicitly:

- `pending` — "Waiting for broker to register this share…"
- `failed` — "Share could not be registered with the broker. Retry?"
  (CLI: `fotobank shares retry <scope_uuid>`; or `shares revoke` to
  abandon it.)
- `revoking` — "Revocation in progress." (Rendered alongside
  `revoked_at`; disappears from the share list once
  `revoked_remote`.)

`broker_status` never affects enforcement. A `failed` scope with no
grantee-held header simply means nobody ever received the grant; the
scope exists locally but is inert. Rolling the local row back on
broker failure would lose the error message and the retry affordance,
which is why the outbox pattern is preferred over in-transaction
broker calls.

### 6.4 Dev-stub `IdentityProvider`

When fotobank is started without an identity broker in front of it (local
development, single-user NAS homelab, migration from the Python tool),
the stub provider:

- Returns a configured single-owner principal on every request. Default:
  `hub="dev-local", user_id="owner", handle="owner"`. Configurable via env
  vars: `FOTOBANK_DEV_HUB`, `FOTOBANK_DEV_USER_ID`, `FOTOBANK_DEV_HANDLE`.
- Ignores `X-Auth-*` headers entirely (clients cannot forge a different
  principal even if they try).
- Grants the principal full access to everything owned by that principal
  — which in single-owner mode is everything.

**Sharing under dev-stub.** The CLI runs in local-admin mode (it does
not go through the `IdentityProvider`), so the owner can create and
revoke scopes against the local `scopes` table for testing. What the
dev-stub *cannot* do is route grantee HTTP requests: there is no broker
to authenticate non-owner users or inject `X-Auth-Scopes`. Any HTTP
request reaching fotobank is resolved to the configured owner, and that
owner's requests are never subject to scope enforcement (owner access
is by ownership, not grant).

On startup, if the `scopes` table has any non-revoked rows, the
dev-stub logs a clear warning:

```
dev-stub identity provider: N non-revoked scope rows exist in this
database, but no broker is configured. Grantee requests cannot be
served. Attach a real identity broker (§6.2 header contract) to
enable sharing.
```

"Non-revoked" is the literal condition (`revoked_at IS NULL`) and
covers every local state that might plausibly want to serve grantee
traffic — `pending`, `active`, `failed`, or `revoking` `broker_status`.
It is deliberately broader than `broker_status='active'` so the
warning fires as soon as any share work exists locally, not only
once broker sync has completed.

This surfaces the Phase 1→2 boundary (local share scaffolding works;
remote share serving requires a broker) without gating on it.

The stub is also used for the one-shot migration from the existing
Python tool (§12): the legacy library has exactly one owner by
definition, and no shares exist before migration.

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
3. `ShareService.ResolveScopes(requester, scope_uuids)` reads `scopes`
   rows. For each scope it drops any row that fails **any** of:
   - `revoked_at IS NULL`,
   - `expires_at IS NULL OR expires_at > now()`,
   - `(grantee_hub, grantee_user_id) = requester_principal` — the
     defence-in-depth principal check. If the broker or reverse proxy
     leaks a scope UUID to the wrong user, this check blocks it.
   Scopes that survive are unioned into a set of media IDs:
   `target_type='album_live'` expands to the album's current
   `album_media`; `target_type='media_set'` expands to `scope_media`.
4. Endpoint filters:
   - **List / browse:** returned media items are limited to the resolved
     set. Albums the grantee holds a scope for appear as "shared" entries.
   - **Fetch original bytes** (`/media/{id}/original`): the endpoint
     additionally checks that at least one applicable scope has
     `allow_download = true`. If none, the response is `403`; the
     viewer is expected to use a derivative endpoint (e.g.,
     `/media/{id}/thumb?size=lightbox`) for display of view-only
     shares. No silent fallback — that would break caching and make
     client behaviour opaque.
   - **Fetch thumbnail / derivative:** always allowed if the media item
     is in the resolved set. `grid`, `preview`, and `lightbox` sizes are
     available to any grantee whose scope resolves to this item,
     regardless of `allow_download`. For videos the derivatives are
     poster frames.
   - **Stream video bytes** (HTTP Range requests against
     `/media/{id}/original`): subject to the same `allow_download`
     check. Video playback from the browser therefore requires a
     download-enabled scope; view-only video shares are represented in
     the viewer as a playable poster with no seek/play — v1 does not
     support proxied streaming of view-only videos, and this is called
     out as a limitation in the share UI.
   - **Any admin op** (create album, delete, import, create share): `403`.
     Only the owner can mutate.
5. Any media item not in the resolved set is treated as nonexistent —
   404, not 403, to avoid leaking existence.

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
  originals to NAS following the atomic sequence in §4.4, commits media
  rows, enqueues thumbnail generation. Dedup (within-owner MD5) skips
  already-imported files. Concurrent imports are serialised via a file
  lock at `{nas_root}/.fotobank/import.lock`.

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

- **Reconcile:** `fotobank reconcile` walks both NAS and the `media`
  table and reports discrepancies as described in §4.4 — orphan bytes,
  stale temp files, and DB rows pointing at missing files. The command
  is read-only by default; `--commit-deletes` and `--commit-recoveries`
  flags act on what the operator has reviewed. `fotobank reconcile`
  subsumes the old `sync-metadata` command from the Python tool, which
  only handled the "DB row, no file" direction.

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

Both CLI and web, both via the same `ShareService`. The outbox flow is
authoritative; see §6.3 for the state machine and retry semantics.

- **Create.** `ShareService.CreateShare(...)` mints a scope UUID,
  commits the `scopes` row with `broker_status='pending'` plus any
  `scope_media` rows in a single transaction, and returns the scope
  handle to the caller. The broker-sync worker subsequently
  `RegisterScope` + `CreateGrant` and advances the row to `active`.
- **Revoke.** `ShareService.RevokeShare(scope_uuid)` sets `revoked_at`
  and `broker_status='revoking'` locally in a transaction, which
  makes the scope immediately inert for fotobank-served traffic
  (§7.2). The worker subsequently `RevokeGrant` with the broker and
  advances to `revoked_remote`.
- **Retry failed.** `ShareService.RetryShare(scope_uuid)` resets a
  `failed` scope back to `pending` so the worker picks it up again.
  Useful after fixing a misconfigured broker CLI or broker outage.

Web UX for share management is out of scope in *this* vision doc —
it's a web-frontend sub-spec concern. The service-layer API it will
consume is fixed here.

## 9. EXIF and metadata

### 9.1 Pure-Go EXIF

The Go port drops `exiftool` and `exifread`. A pure-Go library (candidates:
`github.com/dsoprea/go-exif/v3`, `github.com/rwcarlsen/goexif/exif` — choice
deferred to the core sub-spec) replaces both.

No subprocess fallback. If EXIF extraction fails for a file, the photo is
imported with minimal metadata: MD5, size, dimensions if derivable from
the image container, `timestamp = NULL`, and `thumb_status = 'pending'`
like any other successful import. The file is still browsable and
searchable by import time; it just lands under `unknown_date/` on NAS.
The thumbnail worker will later flip it to `ready` (if the worker can
decode the image), `no_preview` (if a RAW with no embedded preview), or
`failed` (if something truly unreadable slipped through — rare).

### 9.2 Supported formats

Preserved from the current tool:

- **Photos (EXIF extracted):** JPG, JPEG, GIF, PNG, HEIC, ARW, RAF, DNG,
  CR2, NEF. (HEIC and PNG are new additions; the current Python tool
  handles the rest.)
- **Movies (checksum + best-effort timestamp):** MP4, AVI, MOV, MP2,
  MPG, M4V. Unlike the Python tool, the Go port extracts creation
  time from the container where present — QuickTime / ISO-BMFF
  `moov/mvhd/creation_time`, the `com.apple.quicktime.creationdate`
  key where set by iPhones, and equivalent AVI / MPG header fields.
  Library choice (pure-Go MP4 parser vs. a narrow custom reader) is
  a sub-spec decision (§15.2). Extraction is best-effort; missing
  timestamps land as `NULL` and do not block import. Duration is
  captured where readable.

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
(§15.3). High-level:

- **The DB is the durable queue.** `thumb_status='pending'` on a
  `media` row IS the queued job; there is no separate in-memory queue
  to lose across processes or restarts. This matters because
  fotobank ships a CLI and a server as separate processes sharing one
  SQLite database: `fotobank import` run from a shell cannot push
  into the server's memory, but it *can* commit `pending` rows, and
  the server's thumbnail worker will pick them up.
- **Worker claim loop.** The server runs a thumbnail worker that
  periodically (e.g., every few seconds, with a shorter poll when
  work was just found) claims a small batch of `pending` rows by
  transitioning them to `thumb_status='working'` using a conditional
  UPDATE. Only one worker claims any given row — the conditional
  update is the mutex. After generating and writing thumbnails, the
  worker transitions to `ready` / `no_preview` / `failed`. A crash
  mid-claim leaves the row in `working`; a lease-expiry sweep (rows
  in `working` longer than *T*) puts them back to `pending`.
- **Optional in-process fast path.** When an import happens
  in-process with the server (HTTP-triggered import), a wakeup
  channel can signal the worker to poll immediately instead of
  waiting for the next tick. This is an optimisation over the
  DB-claim loop, never a replacement.
- **Generation itself.** As part of a successful import the media
  row is committed with `thumb_status='pending'`. The worker
  generates the three sizes, writes them to NAS (atomically,
  same-tier temp + rename), and updates the row. Import is reported
  complete as soon as the row commits; the media item is visible
  and listable immediately, with a placeholder until its thumbnails
  are ready.
- **Viewer behaviour under `pending`.** The grid renders a placeholder
  (e.g., EXIF dimensions as a blurred background colour, or a generic
  loading tile). The web UI may poll or subscribe for completion;
  detailed UX belongs in the web sub-spec.
- **Why async.** Thumbnail generation for a large import (thousands of
  files) can run for minutes and is CPU-bound; blocking imports on it
  would make bulk ingestion feel broken and starve the watched-folder
  trigger.
- Three sizes: `grid` (256px), `preview` (1024px), `lightbox` (2048px).
  All WebP.
- RAW input: extract embedded JPEG preview, resize. No `libraw` dependency.
- `thumb_status` state machine: `pending` → `working` → `ready` /
  `no_preview` / `failed`, with `working` → `pending` on lease
  expiry.
- Regeneration command: `fotobank thumbs regenerate <media_id|--all>`.
  Sets the targeted rows back to `pending`; the worker picks them up.

## 11. Backup and durability

- **NAS is authoritative for bytes.** Operators back up NAS via their own
  mechanism (NAS-level snapshots, rsync to offsite, etc.). Fotobank does
  not take responsibility for byte durability beyond writing to NAS.

- **SQLite lives on flash.** A background job periodically snapshots SQLite
  (via `VACUUM INTO` or the SQLite backup API) to
  `{nas_root}/.fotobank/snapshots/{timestamp}.sqlite`, keeping the last *N*
  snapshots. Default interval and retention: sub-spec concern.

  **Non-zero metadata RPO.** Between snapshots, any new imports, album
  edits, or share changes exist only on flash. A flash failure strictly
  between snapshots loses that delta — bytes remain durably on NAS, but
  the DB rows describing them do not. Reconcile (§8.1) can partially
  rebuild from NAS (re-register orphan bytes as re-imports), but album
  memberships and scope rows are lost unless reconstructed from
  broker-side audit state. Operators who need tighter RPO should
  shorten the snapshot interval or configure WAL shipping to NAS; both
  are sub-spec parameters.

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

1. Operator runs
   `fotobank migrate --from-legacy {legacy_base}
     --owner <hub>:<user_id> --storage-key <slug>
     [--mode symlink|move]`.
2. The migrator creates an `owners` row for the given principal with the
   given `storage_key`.
3. It reads the legacy `registry.sqlite`, maps columns to the new
   `media` schema (photos), stamps every row with the owner principal,
   and also scans the legacy `movies/` directory to register videos as
   `media` rows with `media_type='video'`.
4. Filesystem bytes go from `{legacy_base}/YYYY/…` to
   `{nas_root}/{owner_storage_key}/YYYY/…` — physically moved under
   `--mode move`, or left in place with a per-owner symlink tree under
   `--mode symlink` (recommended for active Lightroom users, see below).
5. Thumbnail generation is enqueued for all migrated media items.
6. The new SQLite is written to the fotobank server's flash.
7. The legacy SQLite is not modified; the operator deletes it once
   they've verified the new installation.

Migration runs with the dev-stub identity provider (single-owner mode).
After migration, the operator can attach an identity broker and start
sharing without re-importing.

**Lightroom catalog coexistence.** Operators using Lightroom Classic
against the existing library must account for path changes when the
legacy layout (`{legacy_base}/YYYY/…`) becomes
`{nas_root}/{owner_storage_key}/YYYY/…`. Two supported paths:

- **Symlink-in-place (recommended for active LR users, with a
  caveat).** The migrator leaves bytes where they are and creates
  the per-owner namespace as a symlink tree pointing at the
  originals. LR's catalog keeps working. The operator can physically
  consolidate later at a time of their choosing.

  **Caveat:** symlink mode preserves the legacy base as the
  durability surface. Fotobank's "NAS is authoritative" guarantee
  (§2.2, §11) holds only if the symlink target is itself on backed-up
  NAS-grade storage. If the legacy library lives on a laptop's local
  SSD or an unreliable drive, symlink mode inherits that fragility.
  `fotobank migrate` refuses `--mode symlink` unless
  `--legacy-durable` is also passed, asserting the operator has
  confirmed the legacy path is durable; the flag is an explicit
  acknowledgement, not a check fotobank can perform. Operators
  unsure about legacy durability should use `--mode move` and rely
  on the new `{nas_root}` as the single durable surface.
- **Physical move + Lightroom relocate.** The migrator moves bytes
  into the new layout. The operator uses LR's "Locate Folder" or
  "Update Folder Location" flow to point the catalog at the new
  path once. One interruption; clean final state.

Migration also scans the legacy `movies/` directory and registers each
video as a `media` row (`media_type='video'`), closing the gap where
the Python tool kept video bytes on disk but did not record them in the
DB.

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

### Phase 1 — Go core (three milestones)

Phase 1 replaces the Python tool with a Go core that a dev-stub
single-owner deployment can run end-to-end. It is split into three
milestones so each can land, be tested, and be reviewed independently
before the next begins. Nothing in Phase 1 depends on a real broker.

**1a — Parity and migration.** The bedrock.

- Single Go binary with `fotobank-server` and `fotobank` entry points.
- `owners`, `media` schema (multi-owner capable from day one, even
  though only one owner is registered).
- Pure-Go EXIF extraction; drop `exiftool`.
- SQLite on flash; NAS-authoritative byte writes with the atomic
  sequence in §4.4.
- `storage.Store` with Stat / ReadRange / Write / Delete, flash cache
  for originals.
- CLI subcommands at parity with the Python tool:
  `fotobank import <dir>`, `fotobank reconcile` (replaces
  `sync-metadata` and extends it with orphan-byte reporting).
- `fotobank migrate --from-legacy` — one-shot importer from the Python
  DB, including `movies/` registration.
- Dev-stub `IdentityProvider`. No HTTP server yet — the binary runs as
  a CLI only at this milestone.

**1b — Thumbnails and albums.** Browsing substrate.

- Thumbnail pipeline (WebP, three sizes, eager generation, RAW
  embedded-preview extraction, `thumb_status` tracking).
- Video poster extraction.
- `albums` and `album_media` tables plus owner-consistency trigger.
- `fotobank albums` subcommands (create / list / add / remove / rename
  / delete) with `--json` output for agents.
- `fotobank thumbs regenerate` for forced rebuilds.
- Minimal HTTP server (no auth): `/media`, `/media/{id}/original`,
  `/media/{id}/thumb?size=…`, `/albums`, `/albums/{id}`. Dev-stub
  identity only — no broker integration yet, so the server effectively
  serves a single-owner library. Enough to validate the read path
  with a real browser.

**1c — Share scaffolding.** Local-only sharing primitives.

- `scopes` and `scope_media` tables plus owner-consistency trigger.
- `ShareService` mint / revoke / list operations backed by
  `stub.BrokerRegistrar` (no-op outbound). CLI surface:
  `fotobank shares create --album <id> --grantee <principal>
  [--download] [--expires …]`, `fotobank shares list`,
  `fotobank shares revoke <scope_uuid>`.
- Dev-stub startup warning when active scopes exist without a broker
  (§6.4).
- Owner-side HTTP read path unchanged (owners access by ownership).
  Grantee HTTP path is **not** operative in 1c; that requires real
  broker integration (Phase 2).

Phase 1 is complete when the Python tool can be deleted, a dev-stub
single-owner deployment matches or exceeds current functionality, and
the shares scaffolding exists locally for Phase 2 to activate.

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
11. **First-class album snapshot scope.** `media_set` already captures
    "freeze the current album at mint time" when the owner expands the
    album into a fixed list. A dedicated `album_snapshot` target_type
    (stores the album ID plus a frozen members snapshot) might be
    nicer in the UI — the grantee sees an album title, not an ad-hoc
    set, but the members don't drift. Worth revisiting once real share
    UX exists.
12. **Video streaming for view-only shares.** §7.2 punts this: view-only
    videos show a non-playing poster because enabling Range requests
    against `/media/{id}/original` requires `allow_download = true`.
    An alternative would be a separate streaming endpoint that serves
    compressed-bitrate H.264 derivatives — but that requires transcoding
    and is out of scope for v1. Confirm v1 users are fine with
    "view-only = photo only" or escalate to a transcoding sub-spec.
13. **SQLite snapshot cadence and WAL shipping.** §11 calls out non-zero
    RPO. The backup sub-spec needs to commit to a default cadence
    (e.g., every 15 minutes + hourly + daily retention) and whether
    WAL shipping to NAS is wired up in Phase 1 or deferred.

---

*End of vision spec.*
