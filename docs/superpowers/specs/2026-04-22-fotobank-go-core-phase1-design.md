# Fotobank Go Core — Phase 1 Design Sub-Spec

**Status:** Draft (v0.1)
**Date:** 2026-04-22
**Parent:** [2026-04-22-fotobank-vision.md](./2026-04-22-fotobank-vision.md)
**Scope:** Phase 1 (milestones 1a + 1b + 1c) from master §14 — the
complete Go reimplementation of fotobank, covering module layout,
schema, storage, import pipeline, thumbnails, albums, local share
scaffolding, HTTP API (huma + OpenAPI), CLI, and one-shot migration
from the Python tool. Broker integration stays stub-only; real broker
and the web frontend are Phase 2 and Phase 3.

This sub-spec settles every non-obvious implementation choice so the
follow-up implementation plan can be executed without new architectural
decisions.

---

## 1. Problem and scope

The master vision spec pins down *what* fotobank is (multi-user,
NAS-backed, grant-aware, identity delegated to an external broker) and
*how* its pieces relate. This sub-spec pins down *how* to build the
Phase 1 slice in Go: concrete package layout, libraries, schema DDL,
interfaces, and the exact behaviour of each CLI and HTTP endpoint.

### 1.1 Delivered in Phase 1

- Single Go binary `fotobank` with both server (`fotobank server …`)
  and CLI (`fotobank import …`, etc.) entry points.
- Separate `cmd/fotobank-openapi` binary that dumps the OpenAPI spec
  to stdout / a file — run via `make api-generate` so downstream
  consumers (future frontend, agents) can regenerate typed clients.
- SQLite schema for all Phase 1 tables: `owners`, `media`,
  `principal_display`, `albums`, `album_media`, `scopes`,
  `scope_media`.
- Embedded migrations run on startup.
- Storage abstraction with NAS-only and flash-cache tier modes.
- Import pipeline with the atomic-write sequence from master §4.4
  (temp + rename + DB commit), concurrent-import file lock, and
  within-owner MD5 dedup.
- Reconcile command covering orphan bytes, stale temp files, and DB
  rows with no file.
- EXIF extraction (photos) and container-metadata extraction
  (videos). HEIC deferred as `thumb_status='no_preview'` with a
  logged warning.
- Asynchronous thumbnail worker with DB-backed queue (master §10),
  three WebP sizes, RAW embedded-preview path, video poster
  extraction. *(Execution note — 2026-04-22: the thumbnail pipeline
  ships in Plan C with photos + RAW only; video poster extraction is
  split out to Plan E. See `2026-04-22-fotobank-plan-c-thumbnails-
  design.md` §17. The worker/queue/storage design in §11 below still
  stands; Plan E picks up the §11.2 poster path.)*
- Albums: CRUD in the service layer, CLI surface, and HTTP API.
  Owner-consistency trigger on `album_media`.
- Shares: scope mint / list / retry / revoke in the service layer,
  outbox worker driving a stub `BrokerRegistrar`, owner-consistency
  trigger on `scope_media`. All grants are "local only" at this
  milestone — grantee HTTP requests are unreachable until Phase 2's
  real header provider plus broker land.
- Dev-stub `IdentityProvider` with the `--unsafe-dev-stub` safety
  behaviour collapsed into a startup warning per master §6.4.
- Header-based `IdentityProvider` (used when `[identity.mode] =
  "header"`) along with the direct-access guard from master §6.2.
- HTTP API via huma/v2 on the `humago` net/http adapter, covering
  both viewer read endpoints and admin write endpoints. Same service
  layer the CLI uses.
- One-shot migration from the Python tool (`fotobank migrate
  --from-legacy …`), with symlink and move modes, movies table
  backfill, and the `--legacy-durable` acknowledgement for symlink
  mode (master §12).
- Full Makefile, golangci-lint config, nilaway wiring, prek hooks,
  and mise toolchain pinning matching middleman's conventions.

### 1.2 Deferred to later sub-specs

- Real `exec.BrokerRegistrar` and the external identity service
  integration (Phase 2).
- Watched-folder import (Phase 2; only "fotobank import <dir>" is
  in this sub-spec).
- Web frontend (Phase 3).
- Web upload UX, face detection, GPS redaction, saved-query grants,
  re-share grants, perceptual-hash dedup, real RAW decoding,
  cross-owner CAS migration, undo/transactional metadata edits
  (Phase 4+).

## 2. Module and package layout

### 2.1 Go module

```
module github.com/wesm/fotobank

go 1.26.0
```

Repository layout:

```
fotobank/
├── cmd/
│   ├── fotobank/                    -- server + CLI, single binary
│   └── fotobank-openapi/            -- dumps OpenAPI spec, used by make api-generate
├── internal/
│   ├── config/                      -- TOML loading, validation, defaults
│   ├── errs/                        -- sentinel errors, wrap helpers
│   ├── db/
│   │   ├── db.go                    -- Open(), RW/RO pools, WAL setup
│   │   ├── migrations.go            -- //go:embed, runMigrations
│   │   ├── queries/                 -- typed query helpers (not sqlc; hand-written)
│   │   └── migrations/              -- NNNNNN_name.up.sql / .down.sql
│   ├── owners/                      -- owner registration model + repo
│   ├── media/                       -- media model + repo + EXIF mapping
│   ├── album/                       -- album model + repo
│   ├── share/                       -- scope model + repo + outbox worker
│   ├── identity/                    -- IdentityProvider interface + stub + header
│   ├── broker/                      -- BrokerRegistrar interface + stub + exec (stub only in P1)
│   ├── exifread/                    -- EXIF + container metadata extraction
│   ├── storage/                     -- Store interface, NAS-only impl, flash cache impl
│   ├── thumb/                       -- thumbnail generation + worker
│   ├── ingest/                      -- import pipeline ("import" is a reserved keyword context)
│   ├── reconcile/                   -- reconcile walker
│   ├── migrate/                     -- one-shot migration from Python tool
│   ├── service/                     -- domain services composing the repos
│   │   ├── media_service.go
│   │   ├── album_service.go
│   │   ├── share_service.go
│   │   └── …
│   ├── httpapi/                     -- huma registration, middleware, handlers
│   ├── cli/                         -- subcommand dispatch, per-command handlers
│   ├── testutil/                    -- openTestDB, sample fixtures, helpers
│   └── version/                     -- version/commit/buildDate ldflags home
├── docs/
│   └── superpowers/specs/           -- this sub-spec and parent
├── testdata/                        -- golden files, fixture photos/videos
├── scripts/                         -- release, helpers
├── Makefile
├── mise.toml
├── prek.toml
├── .golangci.yml
├── .air.toml
├── config.example.toml
├── go.mod
└── go.sum
```

Rationale:

- `internal/` boundary prevents external imports; fotobank is an app,
  not a library.
- `cmd/fotobank/main.go` is thin — parse args, dispatch to
  `internal/cli`. The server flag path instantiates the HTTP server
  from `internal/httpapi`.
- Domain packages (`owners`, `media`, `album`, `share`) own their
  repository (DB access) and model types; `service/` composes them
  into the transactional operations the CLI and HTTP share.
- `ingest`, `reconcile`, `migrate`, `thumb` are vertical concerns
  that cut across repos; they live as their own packages rather than
  being crammed into a generic `service`.

### 2.2 Binaries

Two binaries, both pure Go (no CGO):

- `fotobank` — server + all CLI subcommands. Single binary, dispatched
  by the first positional arg (§13).
- `fotobank-openapi` — dumps the OpenAPI spec. Minimal `main.go` that
  constructs the huma API the way `fotobank server` does and calls
  `huma.OpenAPI()` serialisation.

No separate `fotobank-server` binary. The server is invoked as
`fotobank server`.

## 3. Toolchain and conventions

Copied from middleman unless noted.

### 3.1 Go version, mise, Make

- Go 1.26.0 (`go.mod` toolchain directive pins it).
- `mise.toml` pins `golangci-lint` to a specific version. Other tools
  (`air`, `nilaway`) installed via `go install` or the Makefile helper
  targets.
- `Makefile` is the canonical entry point for build, test, lint.

Example Makefile targets (copied/adapted from middleman):

```
build              # build ./cmd/fotobank with ldflags
build-release      # same with -trimpath -s -w
install            # build-release and copy to ~/.local/bin or GOPATH/bin
dev                # air live-reload for server
test               # go test ./... -shuffle=on
test-short         # go test ./... -short -shuffle=on
vet                # go vet ./...
lint               # mise exec -- golangci-lint run --fix ; testify-helper-check
nilaway            # nilaway -include-pkgs=<module> ./...
tidy               # go mod tidy
api-generate       # run cmd/fotobank-openapi > openapi.json
install-hooks      # prek install -f
clean              # rm fotobank binary, build artefacts
help               # default; prints target list
```

### 3.2 Linting

`.golangci.yml` mirrors middleman's:

- Enabled linters: `errcheck`, `forbidigo`, `govet` (with `shadow`
  and `fieldalignment` disabled), `ineffassign`, `staticcheck`,
  `unused`, `modernize`, `testifylint`.
- `forbidigo` forbids `t.Fatal`, `t.Error`, etc. (use testify), and
  `time.Local`, `time.LoadLocation`, `time.FixedZone` (UTC rule —
  storage + API are UTC only; local conversion belongs in the
  eventual frontend).
- `errcheck` allows blank and type-assertion skipping (low signal).
- Test files excluded from `errcheck` (conventional).

`nilaway` is a pre-push hook only (too slow for pre-commit) and is
scoped to `-include-pkgs=<module>` to keep analysis time tractable.

### 3.3 Testing discipline

Rules copied verbatim from middleman's CLAUDE.md:

- Always pass `-shuffle=on` when invoking `go test` directly.
  `make test`/`test-short` already set it.
- Never `-count=1` (default; wastes tokens, disables build cache).
  `-count=N` only for flake hunting with `N > 1`.
- No `-v` by default.
- Table-driven tests for Go.
- `testify` consistently — `require` for setup/preconditions,
  `assert` for non-blocking checks.
- When a test function has more than three assertions, create a
  local helper: `assert := require.New(t)` (or `assert.New(t)`).
  The helper-usage rule is enforced by a small
  `testify-helper-check` tool (§3.6) ported from middleman.
- No `t.Fatal/Error/Fail*` anywhere — golangci-lint `forbidigo`
  enforces this; testify is the only assertion channel.
- `t.TempDir()` for any temp directories.
- Fast, isolated tests.

### 3.4 Git hooks (prek)

`prek.toml` mirrors middleman's minus the frontend-check hook (no
frontend in Phase 1):

- `pre-commit`: gofmt, golangci-lint (make lint), testify-helper-check,
  migration-history-check, go test -short.
- `pre-push`: nilaway.
- Builtin prek hooks: check-case-conflict, check-merge-conflict,
  detect-private-key, no-commit-to-branch (blocks commits on main),
  check-added-large-files, trailing-whitespace, end-of-file-fixer,
  check-json/toml/yaml.

`migration-history-check` is a small `tools/migrationhistorycheck`
binary we port from middleman: it refuses to edit migrations once they
land on the main branch, preventing silent schema-history rewrites.

### 3.5 UTC datetime rule

Every `TIMESTAMP` stored in SQLite is UTC. Every `time.Time` sent
across the HTTP API is UTC, encoded RFC 3339 by huma. Local-time
rendering is the frontend's problem (Phase 3). golangci-lint's
`forbidigo` guards this at the code level; tests may override with a
targeted `//nolint:forbidigo` for clock fixtures but not for business
logic.

### 3.6 Dev live-reload

`.air.toml` drives `make dev`. Watches `./cmd`, `./internal`,
`./migrations`; reruns `go build -o ./fotobank ./cmd/fotobank` then
starts the server. Matches middleman's shape.

### 3.7 `testify-helper-check` tool

Tiny Go tool ported from middleman. Rule: any `*_test.go` function
with more than three testify calls must bind a local helper
(`assert := require.New(t)` or similar). Enforced as a pre-commit
hook. Keeps assertion-heavy tests readable without repeating `t`
everywhere.

## 4. Config

### 4.1 TOML schema

Single TOML file loaded at startup. Schema:

```toml
# Where fotobank stores its SQLite DB and (optionally) flash-cached
# originals and thumbnails.
[flash]
root = "~/.local/state/fotobank"     # default; ~ expanded

# Where durable bytes live. Imports write originals here before the
# media row is committed; thumbnails are written here asynchronously
# by the background worker after the row commits (see §10, §11).
[nas]
root = "/mnt/nas/fotobank"           # required

# "nas_only" (no flash tier), "flash_cache" (default)
[storage]
mode = "flash_cache"
originals_cache_days = 30            # recency window for original cache
originals_cache_max_media = 100000   # hard ceiling; whichever hits first
thumbs_cache_enabled = true          # thumbs cached on flash

[identity]
mode = "stub"                        # "stub" | "header"

[identity.stub]
hub = "dev-local"
user_id = "owner"
handle = "owner"
# auto-registers the owner with storage_key = "owner" on first start

[identity.header]
# Names can be overridden per deployment; defaults shown.
user_id_header = "X-Auth-User-Id"
hub_header = "X-Auth-Hub"
handle_header = "X-Auth-Handle"
scopes_header = "X-Auth-Scopes"
request_id_header = "X-Auth-Request-Id"
# Direct-access guard inputs (at least one must be set in header mode).
# The bind address itself lives under [http].listen_address — the
# guard inspects that field when deciding if loopback satisfies it.
trusted_proxy_cidrs = []             # e.g. ["10.0.0.0/24"]
proxy_secret_header = ""             # when set, require X-Auth-Proxy-Secret match
proxy_secret = ""                    # loaded from env FOTOBANK_PROXY_SECRET if unset here
proxy_mtls_ca_file = ""              # PEM file of CA certs for client auth

[http]
listen_address = "127.0.0.1:8090"    # TCP host:port or "unix:/path/to/socket"
                                     # in header mode, loopback/UDS satisfies the direct-access guard
base_url = ""                        # used to build absolute URLs in API responses; empty = omit
request_timeout = "30s"
write_timeout = "60s"                # accommodates range streams and large originals
cors_origins = []                    # typically set to the frontend origin in Phase 3

[imports]
concurrent_workers = 2               # goroutines doing EXIF + hash inside a single 'fotobank import' invocation
file_lock_path = ""                  # default: {nas.root}/.fotobank/import.lock

[thumbs]
worker_concurrency = 4               # goroutines generating thumbnails per server process
poll_interval = "5s"                 # how often to scan for pending rows
lease_timeout = "10m"                # working rows reclaimed after this

[broker]
# v1 ships stub only. When Phase 2 lands, mode = "exec" with commands.
mode = "stub"

[broker.exec]
# Reserved for Phase 2. Example shape (broker name omitted
# intentionally; fill in when the broker CLI is available):
# command = "/usr/local/bin/<broker-cli>"
# register_scope_args = ["scope", "register", "--app", "fotobank", ...]

[backup]
snapshot_interval = "15m"
snapshot_retention = 96              # keep last N snapshots
wal_shipping = false                 # future
```

### 4.2 Config file location

`$FOTOBANK_CONFIG` env var → `--config` flag → default
`$XDG_CONFIG_HOME/fotobank/config.toml` (or `~/.config/fotobank/config.toml`).
Missing file at the default path triggers a one-shot scaffold: write
`config.example.toml` contents to the default location with a logged
warning and exit `0`. Every other path error is fatal.

### 4.3 Environment variable overrides

Narrow set, documented in README:

- `FOTOBANK_CONFIG` — path override.
- `FOTOBANK_PROXY_SECRET` — loaded into `identity.header.proxy_secret`
  if unset in TOML. Avoids committing secrets to disk.
- `FOTOBANK_DEV_HUB`, `FOTOBANK_DEV_USER_ID`, `FOTOBANK_DEV_HANDLE` —
  stub overrides (useful in CI, testing, and the migration flow).

Other config comes from the TOML file only; no env sprawl.

### 4.4 `fotobank config` CLI

Mirrors middleman's `config read`:

- `fotobank config read <dotted.key>` — prints a value.
- `fotobank config path` — prints the resolved config file path.
- `fotobank config validate` — loads and validates; exit 0 on ok.

No `config write` in v1. Config is human-edited.

## 5. Error taxonomy

### 5.1 Sentinel errors

Per-package sentinels, wrapped with `fmt.Errorf("…: %w", err)` at
call sites. Handlers use `errors.Is` / `errors.As` to switch on them.

```
internal/errs/errs.go
---------------------
var (
    ErrNotFound              = errors.New("not found")
    ErrAlreadyExists         = errors.New("already exists")
    ErrInvalidArgument       = errors.New("invalid argument")
    ErrPermissionDenied      = errors.New("permission denied")
    ErrOwnerMismatch         = errors.New("owner mismatch")
    ErrConcurrentImport      = errors.New("another import is in progress")
    ErrBrokerUnavailable     = errors.New("broker unavailable")
    ErrIdentityMissing       = errors.New("identity unavailable")
    ErrDirectAccessBlocked   = errors.New("direct access blocked")
    ErrMigrationPrecondition = errors.New("migration precondition failed")
    ErrBadConfiguration      = errors.New("bad configuration")
)
```

Per-domain sentinels can live in their own package when meaningful
(e.g., `media.ErrMediaCorrupt`). `errs` holds only the cross-cutting
ones that HTTP needs to translate.

### 5.2 Error → HTTP status mapping

`internal/httpapi/errors.go` translates sentinels to huma errors in a
single function called from every handler via a small wrapper:

| Sentinel | HTTP |
|---|---|
| `ErrNotFound` | 404 |
| `ErrAlreadyExists` | 409 |
| `ErrInvalidArgument` | 400 |
| `ErrPermissionDenied` | 403 |
| `ErrOwnerMismatch` | 403 |
| `ErrConcurrentImport` | 409 |
| `ErrBrokerUnavailable` | 503 |
| `ErrIdentityMissing` | 401 |
| `ErrDirectAccessBlocked` | 403 |
| `ErrBadConfiguration` | 500 |
| any other | 500 |

Huma error bodies match huma's default RFC 7807 shape; we add no
custom error envelope.

### 5.3 Wrap style

```go
if err := thing(); err != nil {
    return fmt.Errorf("doing thing with %q: %w", name, err)
}
```

Wrap in verbs (`creating scope`, `writing to NAS`) and names, not with
function identifiers. Standard Go convention.

## 6. SQLite schema and migrations

### 6.1 Migration tooling

`golang-migrate/migrate/v4` with the `iofs` source on `//go:embed
migrations/*.sql`. File naming:
`NNNNNN_description.up.sql` / `NNNNNN_description.down.sql`, six-digit
sequence, zero-padded.

Every migration is committed as a pair (`up` + `down`). A small
`tools/migrationhistorycheck` binary ported from middleman runs as a
pre-commit hook: once a migration has landed on `main`, its content
cannot be edited. Changes to that migration require a new migration.

### 6.2 Connection pool design

Two `*sql.DB` handles to the same file, mirroring middleman's
`internal/db/db.go`:

- **RW** — `MaxOpenConns=1`. Single writer, serialising writes without
  busy-retry storms. Used for all inserts/updates/deletes.
- **RO** — `MaxOpenConns=4`. Parallel reads. Used by list/get
  endpoints and by the thumbnail worker's claim queries (though the
  claim UPDATE goes through RW).

Connection string:

```
file:{path}?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)
```

### 6.3 Startup sequence

```
Open → both pools
    → PRAGMA journal_mode=WAL on RW
    → runMigrations(rw)
    → ensure owners row for identity.stub principal (stub mode only)
    → return DB
```

WAL is set once on the RW connection; it persists on the file.
`foreign_keys=1` is per-connection and set via pragma in the DSN.

### 6.4 Tables

Exact DDL (first migration, `000001_initial_schema.up.sql`):

```sql
-- Owners: the principals who own media on this deployment.
CREATE TABLE owners (
    hub              TEXT NOT NULL,
    user_id          TEXT NOT NULL,
    storage_key      TEXT NOT NULL,
    display_handle   TEXT,
    created_at       TIMESTAMP NOT NULL,
    PRIMARY KEY (hub, user_id)
);

CREATE UNIQUE INDEX owners_storage_key_uq ON owners(storage_key);

-- Optional display cache for non-owner principals.
CREATE TABLE principal_display (
    hub              TEXT NOT NULL,
    user_id          TEXT NOT NULL,
    handle           TEXT,
    cached_at        TIMESTAMP NOT NULL,
    PRIMARY KEY (hub, user_id)
);

-- Media: photos and videos.
CREATE TABLE media (
    id                UUID PRIMARY KEY,
    owner_hub         TEXT NOT NULL,
    owner_user_id     TEXT NOT NULL,
    media_type        TEXT NOT NULL CHECK (media_type IN ('photo', 'video')),
    mime_type         TEXT NOT NULL,
    path              TEXT NOT NULL,
    original_filename TEXT,
    imported_at       TIMESTAMP NOT NULL,
    timestamp         TIMESTAMP,
    size              INTEGER NOT NULL,
    checksum          TEXT NOT NULL,

    make              TEXT,
    model             TEXT,
    focal_length      TEXT,
    shutter           TEXT,
    width             INTEGER,
    height            INTEGER,
    iso               INTEGER,
    aperture          REAL,

    duration_ms       INTEGER,

    thumb_status      TEXT NOT NULL CHECK (
        thumb_status IN ('pending', 'working', 'ready', 'no_preview', 'failed')
    ),
    thumb_claimed_at  TIMESTAMP,
    thumb_version     INTEGER NOT NULL DEFAULT 0,  -- bumped on every regeneration
    thumb_updated_at  TIMESTAMP,                   -- when thumb_version was last bumped

    FOREIGN KEY (owner_hub, owner_user_id) REFERENCES owners(hub, user_id),
    UNIQUE (owner_hub, owner_user_id, checksum),
    UNIQUE (owner_hub, owner_user_id, path)
);

CREATE INDEX media_owner_timestamp_idx ON media(owner_hub, owner_user_id, timestamp DESC);
CREATE INDEX media_owner_imported_idx  ON media(owner_hub, owner_user_id, imported_at DESC);
CREATE INDEX media_thumb_pending_idx   ON media(thumb_status, thumb_claimed_at)
    WHERE thumb_status IN ('pending', 'working');

-- Albums.
CREATE TABLE albums (
    id               UUID PRIMARY KEY,
    owner_hub        TEXT NOT NULL,
    owner_user_id    TEXT NOT NULL,
    name             TEXT NOT NULL,
    created_at       TIMESTAMP NOT NULL,
    updated_at       TIMESTAMP NOT NULL,
    FOREIGN KEY (owner_hub, owner_user_id) REFERENCES owners(hub, user_id)
);

CREATE INDEX albums_owner_idx ON albums(owner_hub, owner_user_id, name);

CREATE TABLE album_media (
    album_id         UUID NOT NULL REFERENCES albums(id) ON DELETE CASCADE,
    media_id         UUID NOT NULL REFERENCES media(id)  ON DELETE CASCADE,
    added_at         TIMESTAMP NOT NULL,
    position         INTEGER,
    PRIMARY KEY (album_id, media_id)
);

-- Owner-consistency trigger on album_media inserts / updates.
CREATE TRIGGER album_media_owner_consistency_insert
BEFORE INSERT ON album_media
FOR EACH ROW
BEGIN
    SELECT CASE
        WHEN (SELECT owner_hub FROM albums WHERE id = NEW.album_id) !=
             (SELECT owner_hub FROM media  WHERE id = NEW.media_id)
          OR (SELECT owner_user_id FROM albums WHERE id = NEW.album_id) !=
             (SELECT owner_user_id FROM media  WHERE id = NEW.media_id)
        THEN RAISE(ABORT, 'album and media must share owner')
    END;
END;

CREATE TRIGGER album_media_owner_consistency_update
BEFORE UPDATE OF album_id, media_id ON album_media
FOR EACH ROW
BEGIN
    SELECT CASE
        WHEN (SELECT owner_hub FROM albums WHERE id = NEW.album_id) !=
             (SELECT owner_hub FROM media  WHERE id = NEW.media_id)
          OR (SELECT owner_user_id FROM albums WHERE id = NEW.album_id) !=
             (SELECT owner_user_id FROM media  WHERE id = NEW.media_id)
        THEN RAISE(ABORT, 'album and media must share owner')
    END;
END;

-- Scopes: grants minted by owners, registered with the broker.
CREATE TABLE scopes (
    uuid                 UUID PRIMARY KEY,
    owner_hub            TEXT NOT NULL,
    owner_user_id        TEXT NOT NULL,
    grantee_hub          TEXT NOT NULL,
    grantee_user_id      TEXT NOT NULL,
    target_type          TEXT NOT NULL CHECK (target_type IN ('album_live', 'media_set')),
    target_album_id      UUID REFERENCES albums(id),
    allow_download       BOOLEAN NOT NULL DEFAULT 0,
    label                TEXT,
    created_at           TIMESTAMP NOT NULL,
    expires_at           TIMESTAMP,
    revoked_at           TIMESTAMP,

    broker_status        TEXT NOT NULL CHECK (broker_status IN (
        'pending', 'active', 'failed', 'revoking', 'revoked_remote'
    )),
    broker_registered_at TIMESTAMP,
    broker_granted_at    TIMESTAMP,
    broker_revoked_at    TIMESTAMP,
    broker_last_error    TEXT,
    broker_attempts      INTEGER NOT NULL DEFAULT 0,

    FOREIGN KEY (owner_hub, owner_user_id) REFERENCES owners(hub, user_id),
    CHECK (
        (target_type = 'album_live'  AND target_album_id IS NOT NULL) OR
        (target_type = 'media_set'   AND target_album_id IS NULL)
    )
);

CREATE INDEX scopes_grantee_idx        ON scopes(grantee_hub, grantee_user_id) WHERE revoked_at IS NULL;
CREATE INDEX scopes_owner_idx          ON scopes(owner_hub, owner_user_id) WHERE revoked_at IS NULL;
-- Worker poll index. 'failed' is intentionally excluded — it's a
-- terminal state awaiting Retry; including it would keep the worker
-- polling rows that should be inert until operator intervention.
CREATE INDEX scopes_broker_pending_idx ON scopes(broker_status, broker_attempts)
    WHERE broker_status IN ('pending', 'revoking');

CREATE TABLE scope_media (
    scope_uuid       UUID NOT NULL REFERENCES scopes(uuid) ON DELETE CASCADE,
    media_id         UUID NOT NULL REFERENCES media(id)    ON DELETE CASCADE,
    PRIMARY KEY (scope_uuid, media_id)
);

-- Owner-consistency trigger on scope_media inserts / updates.
CREATE TRIGGER scope_media_owner_consistency_insert
BEFORE INSERT ON scope_media
FOR EACH ROW
BEGIN
    SELECT CASE
        WHEN (SELECT owner_hub FROM scopes WHERE uuid = NEW.scope_uuid) !=
             (SELECT owner_hub FROM media  WHERE id   = NEW.media_id)
          OR (SELECT owner_user_id FROM scopes WHERE uuid = NEW.scope_uuid) !=
             (SELECT owner_user_id FROM media  WHERE id   = NEW.media_id)
        THEN RAISE(ABORT, 'scope and media must share owner')
    END;
END;

CREATE TRIGGER scope_media_owner_consistency_update
BEFORE UPDATE OF scope_uuid, media_id ON scope_media
FOR EACH ROW
BEGIN
    SELECT CASE
        WHEN (SELECT owner_hub FROM scopes WHERE uuid = NEW.scope_uuid) !=
             (SELECT owner_hub FROM media  WHERE id   = NEW.media_id)
          OR (SELECT owner_user_id FROM scopes WHERE uuid = NEW.scope_uuid) !=
             (SELECT owner_user_id FROM media  WHERE id   = NEW.media_id)
        THEN RAISE(ABORT, 'scope and media must share owner')
    END;
END;

-- Owner-consistency trigger on scopes.target_album_id: an album_live
-- scope must reference an album owned by the same principal as the
-- scope. Fires on INSERT and on UPDATE of the relevant columns.
CREATE TRIGGER scopes_target_album_owner_consistency_insert
BEFORE INSERT ON scopes
FOR EACH ROW
WHEN NEW.target_album_id IS NOT NULL
BEGIN
    SELECT CASE
        WHEN (SELECT owner_hub FROM albums WHERE id = NEW.target_album_id) !=
             NEW.owner_hub
          OR (SELECT owner_user_id FROM albums WHERE id = NEW.target_album_id) !=
             NEW.owner_user_id
        THEN RAISE(ABORT, 'scope and target album must share owner')
    END;
END;

CREATE TRIGGER scopes_target_album_owner_consistency_update
BEFORE UPDATE OF owner_hub, owner_user_id, target_album_id ON scopes
FOR EACH ROW
WHEN NEW.target_album_id IS NOT NULL
BEGIN
    SELECT CASE
        WHEN (SELECT owner_hub FROM albums WHERE id = NEW.target_album_id) !=
             NEW.owner_hub
          OR (SELECT owner_user_id FROM albums WHERE id = NEW.target_album_id) !=
             NEW.owner_user_id
        THEN RAISE(ABORT, 'scope and target album must share owner')
    END;
END;
```

Notes on SQLite specifics:

- UUIDs are stored as `TEXT` — SQLite doesn't have a UUID type. Go
  reads and writes `uuid.UUID` via its `String()` form.
- `BOOLEAN` is `INTEGER` at the storage layer; `0`/`1`.
- `TIMESTAMP` is stored as `TEXT` in RFC 3339 UTC. The driver maps
  `time.Time` to/from this automatically.
- Partial indexes (`WHERE revoked_at IS NULL`) shrink hot paths.
- Scope's `target_album_id` is nullable but FK'd; SQLite allows
  nullable FKs.

### 6.5 Migration history policy

Once a migration commits to `main`, it's frozen. Bug fixes become new
migrations. `migrationhistorycheck` enforces this pre-commit. In
practice for fotobank this means the initial schema ships as a single
`000001_initial_schema.*` pair; subsequent schema evolution (e.g.,
adding face-detection tables) is additive migrations numbered from
`000002` onward.

## 7. Storage layer

### 7.1 `storage.Store` interface

```go
package storage

type Tier string

const (
    TierFlash Tier = "flash"
    TierNAS   Tier = "nas"
)

type StoreInfo struct {
    Size     int64
    ModTime  time.Time
    Tier     Tier   // where this read would satisfy from
}

type Store interface {
    Stat(ctx context.Context, owner owners.Principal, key string) (StoreInfo, error)

    // ReadRange opens a reader for [offset, offset+length). length == -1
    // reads to EOF. The returned reader is not seekable; callers issue
    // fresh ReadRange calls for each range.
    ReadRange(ctx context.Context, owner owners.Principal, key string, offset, length int64) (io.ReadCloser, error)

    // Write persists the full bytes from src to the given key using
    // temp + atomic rename on NAS. Returns the key actually used (may
    // differ from input key if the store had to disambiguate; the
    // import pipeline always passes the final key, so this is
    // normally equal).
    Write(ctx context.Context, owner owners.Principal, key string, src io.Reader) (string, error)

    // Delete removes the key from all configured tiers.
    Delete(ctx context.Context, owner owners.Principal, key string) error
}
```

`owners.Principal` is `struct { Hub, UserID string }` — the storage
layer only needs the principal to resolve `storage_key`, which it
looks up once at init (owners are not churn-heavy).

### 7.2 NAS-only backend

`storage.NASOnly` uses `os` syscalls against `{nas.root}/{storage_key}/…`.
Writes use **no-clobber finalization** — `os.Link` succeeds only if
the destination does not exist, which prevents silent byte-overwrite
from racing workers or stale orphans at a canonical path:

1. Create parent dir via `os.MkdirAll`.
2. Open `{final}.tmp-{process_pid}-{nanotime}-{random}`.
3. Stream `src` into the temp file, tracking bytes written; `f.Sync()`.
4. `os.Link(tmp, final)` — atomic, fails with `EEXIST` if something
   already lives at `final`. This is the no-clobber finalize.
5. On success: `os.Remove(tmp)`; return the final key.
6. On `EEXIST` at step 4: remove the temp file and return
   `storage.ErrPathOccupied`. The caller (import pipeline) decides
   whether to retry with a bumped `_seq` suffix (photo path) or to
   treat the existing bytes as a prior-import orphan that reconcile
   will handle (video content-addressed path).
7. On any other error: remove the temp file and return the error.

Reads use `os.Open` plus `io.CopyN` / `io.SectionReader` for ranges.

**Why not `os.Rename`.** `rename(2)` silently replaces any file at
the destination on POSIX. That's what the initial draft assumed,
but it creates two failure modes at import time: (a) two workers
resolving the same canonical path both rename — the loser's bytes
land at the final path with no collision error, and the DB insert
that follows with the winner's UUID then describes the wrong bytes;
(b) a stale orphan (crashed prior import) at the canonical path is
silently overwritten. `link` + `remove(tmp)` preserves the atomicity
guarantee while refusing to clobber, which is exactly the semantics
we want.

**Delete** uses `os.Remove`; tolerates `ENOENT` so deleting bytes
that were already removed elsewhere is idempotent.

### 7.3 Flash cache backend

`storage.FlashCache` wraps a NAS-only store with a flash mirror:

- Writes go to NAS first (via the wrapped store), then the cache
  populates flash asynchronously (goroutine, best-effort).
- Reads hit flash first; on miss, fall back to the wrapped store and
  populate flash on the way back.
- Eviction is recency-based over originals: `{flash.root}/media/{id}`
  files older than `storage.originals_cache_days`, or beyond
  `originals_cache_max_media`, are removed by a janitor goroutine
  that runs every few minutes.
- Thumbnails cache under `{flash.root}/thumbs/{id}/…` without
  eviction in v1 (see master §4.4).

### 7.4 Key scheme

Keys are relative paths inside the owner's NAS subtree. The layout
is exactly the master-spec §4.1 layout — no intermediate wrapper
directory, so Lightroom and any other tool that walks the per-owner
tree sees the canonical `YYYY/`, `movies/`, `.thumbs/` hierarchy.

For a media `row`:

- Photos: `row.path` — e.g. `2024/20240615_143022_0.jpg` or
  `unknown_date/IMG_0001.jpg`.
- Videos: `movies/{md5}.{ext}` — stored as the `row.path`.
- Thumbnails: `.thumbs/{media_id}/grid.webp` (plus
  `preview.webp`, `lightbox.webp`). Computed by the thumbnail
  service, not stored on the `media` row.

The storage layer prepends `{nas.root}/{storage_key}/` for NAS or
`{flash.root}/{storage_key}/` for flash and appends the key as-is.
There is no `original/` or other intermediate segment — the owner
directory contains year folders, `movies/`, and `.thumbs/` directly.

### 7.5 Concurrent safety

Within a process, writes acquire the import file lock (§10.6) before
touching the store. Between processes, the file lock suffices because
NAS rename is atomic. Reads are lock-free.

## 8. Identity

### 8.1 `identity.Provider` interface

```go
package identity

type Principal struct {
    Hub    string
    UserID string
    Handle string // optional, display only
}

type Identity struct {
    Principal Principal
    Scopes    []string // scope UUIDs
    RequestID string
}

type Provider interface {
    // FromRequest extracts identity from an HTTP request (header
    // provider) or returns the configured dev principal (stub).
    FromRequest(ctx context.Context, r *http.Request) (Identity, error)
}
```

### 8.2 `identity.StubProvider`

Constructed with the configured principal. `FromRequest` always
returns `Identity{Principal: configured, Scopes: nil}`.

On server startup:

- Ensure an `owners` row exists for the configured principal. If
  missing, create with `storage_key` = configured `user_id` (or an
  explicit config override `[identity.stub].storage_key`).
- Query `scopes WHERE revoked_at IS NULL` and, if any, log the master
  §6.4 warning ("N non-revoked scope rows exist … broker is required
  to serve grantee requests"). Server still boots; the warning is
  surfaced in the CLI tooling.

### 8.3 `identity.HeaderProvider`

Constructed with the header-name map and the direct-access guard
configuration.

`FromRequest`:

1. Check the direct-access guard (§8.4). On violation, return
   `errs.ErrDirectAccessBlocked`.
2. Read the configured headers. `user_id_header` and `hub_header` are
   required; `handle_header` is optional; `scopes_header` is
   space-separated UUIDs (may be absent).
3. Return the `Identity`.

### 8.4 Direct-access guard

At server startup, `config.Validate` refuses to start in
`identity.mode = "header"` if no guard is configured (master §6.2).
Allowed combinations:

- **Loopback / UDS bind** — `[http].listen_address` starts with
  `127.0.0.1`, `::1`, or `unix:` → OK. The guard inspects the
  actual bind address; there is no separate `listen_address` on
  `[identity.header]`.
- `trusted_proxy_cidrs` non-empty → OK; HTTP middleware compares
  `r.RemoteAddr` against the CIDR list and rejects mismatches.
- `proxy_secret_header` + `proxy_secret` non-empty → OK; middleware
  requires the header and constant-time compares.
- `proxy_mtls_ca_file` non-empty → OK; HTTP server is built with
  `tls.Config{ClientAuth: RequireAndVerifyClientCert, ClientCAs:
  loaded}`.

Combinations are additive (all configured checks must pass at
request time). The stub provider skips the guard entirely.
`[http].listen_address` is the sole bind-address field in either
identity mode — stub mode still needs a listen address, which is
why it lives under `[http]` rather than under an identity-specific
block.

### 8.5 CLI local-admin identity

CLI paths (anything not arriving via HTTP) synthesise an identity via
`identity.LocalAdmin()`:

- `Principal = config.PrimaryOwner()` — either the stub principal or
  the `FOTOBANK_PRIMARY_OWNER=<hub>:<user_id>` env/config value.
- `Scopes = nil`.
- `RequestID = "cli-{pid}-{monotime}"`.

`--owner <hub>:<user_id>` overrides the primary owner for CLI
commands that accept it (e.g., multi-owner deployments).

## 9. Domain services

Services compose repos, enforce invariants, and are the shared
contract between CLI and HTTP. No SQL in handlers.

### 9.1 OwnerService

- `Ensure(ctx, principal, storageKey)` — idempotent owner
  registration.
- `List(ctx)` — all registered owners.
- `Remove(ctx, principal, purge bool)` — remove owner; refuses if
  media exist unless `purge=true` (which also removes media and
  bytes).
- `UpdateDisplay(ctx, principal, handle)` — refresh cached handle.

### 9.2 MediaService

- `Get(ctx, id)` — returns row, or `ErrNotFound`.
- `List(ctx, req)` — paginated; filters (`owner`, `media_type`,
  `date_from`, `date_to`, `album_id`, `sort`, `limit`, `offset`).
- `StreamOriginal(ctx, id, offset, length, w)` — enforces ownership
  or grant; under a scope without `allow_download`, returns
  `ErrPermissionDenied` for requests to `/original`.
- `StreamThumb(ctx, id, size, w)` — `grid` | `preview` | `lightbox`.
- `Delete(ctx, id, owner)` — owner-only; removes bytes and row.

All list/get/stream operations call `resolveVisibleMedia(ctx,
identity)` internally, returning the set the caller can see. For
owner principals: everything owned. For grantees: union of scopes'
resolved media IDs.

### 9.3 AlbumService

- `Create(ctx, owner, name)`, `Get`, `List(ctx, owner)`, `Rename`,
  `Delete`.
- `AddMedia(ctx, albumID, mediaIDs…)`, `RemoveMedia(ctx, albumID,
  mediaIDs…)`, `Reorder(ctx, albumID, ordered []mediaID)`.
- Owner-consistency check at service layer in addition to the DB
  trigger — caller gets a clean `ErrOwnerMismatch` rather than a raw
  SQLite error.

### 9.4 ShareService

- `Create(ctx, req)` where `req` is target (album or media IDs),
  grantee principal, allow_download, optional expiry, optional
  label. Atomically inserts `scopes` + `scope_media`. Returns scope
  row.
- `Get`, `ListByOwner`, `ListByGrantee` (for "shares with me"
  rendering, Phase 2 UX).
- `Retry(ctx, scopeUUID)` — transitions `failed` → `pending`.
- `Revoke(ctx, scopeUUID)` — sets `revoked_at` and
  `broker_status='revoking'`.
- `ResolveForRequester(ctx, requester)` — takes the scope UUIDs
  presented by the broker (via identity), filters by `revoked_at`,
  `expires_at`, and `grantee = requester`, expands to media IDs.
  Returned as a `ResolvedScopes` struct so callers know whether
  `allow_download` is on for any given media ID.

### 9.5 Broker outbox worker

A goroutine on the server:

- Polls `scopes WHERE broker_status IN ('pending', 'revoking')`.
  **`failed` is NOT polled** — it is a terminal state requiring
  explicit `Retry` (or `Revoke` to abandon) from the owner. An
  untouched `failed` row stays failed forever, which is what we
  want; automatic retry without user intent just relitigates the
  same error repeatedly. `ShareService.Retry` transitions
  `failed → pending`, at which point the worker picks it up on its
  next poll.
- For each polled row, drives the state machine:
  - `pending` + `broker_registered_at IS NULL` → `RegisterScope`; on
    success, set `broker_registered_at`. Idempotent per contract.
  - `pending` + `broker_granted_at IS NULL` → `CreateGrant`; on
    success, set `broker_granted_at`, transition to `active`.
  - `revoking` + `broker_revoked_at IS NULL` → `RevokeGrant`; on
    success, set `broker_revoked_at`, transition to `revoked_remote`.
- On transient error: bump `broker_attempts`, set
  `broker_last_error`, sleep with exponential backoff (capped).
  Retried on the next poll.
- On permanent failure (attempts > max): transition to `failed`,
  emit a warning log. The row drops out of the poll filter and
  waits for operator intervention.

For v1 the only `BrokerRegistrar` implementation is
`broker.Stub{}` — every method returns `nil` and the state machine
runs all the way to `active` / `revoked_remote` immediately. The
worker and state machine exist so Phase 2's `exec.BrokerRegistrar`
swap is a one-line change in `main.go`.

### 9.6 ThumbService

Thumbnail lifecycle: the version bumps at the **start** of each
lifecycle (enqueue), not at the terminal transitions. This keeps
cache-coherence correct across all terminal outcomes: whether a
regeneration ends in `ready`, `no_preview`, or `failed`, the client
sees a new version and invalidates its cache. A regeneration that
ends in `failed` therefore surfaces as "new version, 409 response"
rather than "same version, stale cached bytes."

- `Enqueue(ctx, mediaID)` — called by `thumbs regenerate` and any
  future regeneration trigger. Atomically: sets
  `thumb_status = 'pending'`, increments `thumb_version`, sets
  `thumb_updated_at = now()`. Refuses (no-op) if the row is
  already `pending` or `working` — don't double-bump a version
  that will otherwise only have one set of bytes behind it.
- The **import pipeline** inserts new media rows with
  `thumb_status = 'pending'`, `thumb_version = 1`,
  `thumb_updated_at = now()` directly in the insert, bypassing
  `Enqueue`. First-ever thumbs start at version 1.
- `ClaimBatch(ctx, limit)` — the worker's claim query: conditional
  UPDATE `pending → working` with `thumb_claimed_at = now()`
  returning rows. SQLite does not support `UPDATE … RETURNING`
  directly for all versions, so the worker uses a two-step pattern
  wrapped in a transaction: SELECT with `LIMIT` for candidates,
  UPDATE by primary key, check affected rowcount. `thumb_version`
  is NOT touched here.
- `MarkReady(ctx, mediaID)`, `MarkNoPreview(ctx, mediaID)`,
  `MarkFailed(ctx, mediaID, err)` — terminal state transitions
  from `working`. None of them bump `thumb_version`; it was
  already bumped at the start of this lifecycle. HTTP clients
  that fetched `/thumb?…v={N}` and received 409 during `working`
  simply retry once status settles.
- `SweepLeases(ctx)` — reverts `working` rows where
  `thumb_claimed_at < now() - lease_timeout` back to `pending`.
  Runs on a timer. Does not bump `thumb_version` (lease recovery
  is not a new lifecycle, just a continuation of the existing
  one).

The actual generation pipeline lives in `internal/thumb/` (§11).

## 10. Import pipeline

`internal/ingest` owns the import flow.

### 10.1 Discovery

`ingest.Discover(root)` walks a source directory (streaming,
`filepath.WalkDir`) and classifies each regular file:

- Extension → `photo` / `video` / `skip`.
- Yields a channel of `ingest.Candidate{path, mediaType, mimeType}`.

Supported extensions per master §9.2:

- Photos: `.jpg`, `.jpeg`, `.gif`, `.png`, `.heic` (imported but
  thumb-pending with `no_preview`), `.arw`, `.raf`, `.dng`, `.cr2`,
  `.nef`.
- Videos: `.mp4`, `.avi`, `.mov`, `.mp2`, `.mpg`, `.m4v`.

### 10.2 EXIF extraction (photos)

`internal/exifread/photo.go` uses `dsoprea/go-exif/v3`:

1. Open the file, scan for EXIF segment.
2. Read tags via `exif.SearchFileAndExtractExif` then
   `exif.Parse`.
3. Map tags to the normalized struct (`MediaMetadata`):
   - `timestamp`: `DateTimeOriginal` > `DateTimeDigitized` > `DateTime`.
   - `make`, `model`, `focal_length`, `shutter_speed`, `f_number`
     (`aperture`), `iso_speed_ratings`.
   - `width`, `height`: prefer `ExifImageWidth`/`Length`, fallback to
     container decoder (for JPEG, the SOF0/SOF2 frame header).

Errors:

- EXIF segment absent → return empty metadata; import proceeds with
  `timestamp = NULL`, no EXIF fields. The file lands in
  `unknown_date/`.
- EXIF parse error → log warning with file path; same fallback.

For RAWs the EXIF path also records whether an embedded JPEG preview
is present (tag `PreviewImage*` / `ThumbnailImage*`). The thumbnail
worker consumes this. Presence is boolean — we don't extract the
preview bytes at import time; the thumb worker does.

### 10.3 Container metadata (videos)

`internal/exifread/video.go` uses `abema/go-mp4` for MP4/MOV/M4V and
a tiny custom reader for AVI (RIFF `IART`/`ICRD`) and MPG (best-effort
PTS + program-stream header scan).

Extracted fields (nullable):

- `timestamp`: `moov/mvhd/creation_time` → Unix epoch conversion
  (MP4 timestamps are seconds since 1904-01-01 UTC);
  `moov/udta/meta/keys` lookup for
  `com.apple.quicktime.creationdate` (iPhones — often the more
  authoritative value, and already in local TZ; preferred when
  present).
- `duration_ms`: `moov/mvhd/duration` / timescale × 1000.
- `width`, `height`: track header `tkhd`.

For AVI/MPG, extraction is best-effort; failures leave fields NULL.

### 10.4 Import flow

`ingest.ImportDirectory(ctx, root, opts)`:

1. Acquire file lock (§10.6); defer release.
2. Resolve owner from `opts.Owner` or primary owner.
3. **Discover and checksum** — walk the source directory, compute MD5
   for each candidate, and build an in-memory map keyed by checksum.
   When two source paths share a checksum (identical bytes), pick one
   deterministically (first by sorted source path) and drop the
   rest. This in-process dedup prevents parallel workers from racing
   on the same checksum — every checksum passes through the import
   pipeline exactly once.
4. Fan out deduplicated candidates to `imports.concurrent_workers`
   goroutines. Each worker:
   a. Probe `media` for `(owner, checksum)` via the RO pool.
      Present → count as a duplicate of a prior-run import, skip.
   b. Extract EXIF (photo) or container metadata (video).
   c. Resolve a canonical `path`:
      - Photo with timestamp: `YYYY/YYYYMMDD_HHMMSS_SEQ.ext`.
        Start with `SEQ=0`.
      - Photo without timestamp: `unknown_date/{basename_SEQ}.ext`.
      - Video: `movies/{md5}.{ext}` (content-addressed; no seq).
   d. **Attempt the write at the current candidate path.**
      `storage.Write` uses no-clobber finalize (§7.2):
      - Success → proceed to insert.
      - `ErrPathOccupied` for a photo path → bump `SEQ` and retry
        up to 1000 attempts. Failure after the cap returns
        `ErrPathCollisionExhausted` to the caller.
      - `ErrPathOccupied` for a video path → the bytes at
        `movies/{md5}.{ext}` already exist. Two sub-cases:
        - If a `media` row with this checksum exists for this
          owner, treat as duplicate.
        - If no row exists, the bytes are an orphan from a crashed
          import. Insert the row pointing at the existing bytes
          (skipping the write step) after verifying the on-disk
          size matches. Reconcile would otherwise adopt it; doing
          it inline here avoids an orphan-report churn.
   e. **Insert the `media` row in a per-row transaction** with
      `thumb_status='pending'`, `path` = the canonical path that
      actually holds the bytes. Constraint-violation handling:
      - `UNIQUE(owner, checksum)` → another worker or a prior run
        already has this content. Roll back, `storage.Delete` the
        bytes we just wrote (safe — no-clobber finalize guarantees
        we created them this run), count as a duplicate, return.
      - `UNIQUE(owner, path)` → some other row already claims this
        canonical path. Given the in-process checksum dedup
        (step 3) and the file lock (§10.6), this should be
        vanishingly rare. The scenario that can still produce it:
        a pre-existing DB row that points at `path` whose bytes
        were deleted externally (phantom row). Our no-clobber
        finalize just succeeded at `path`, so the bytes now
        present are ours.

        Branch on the existing row's checksum:
        - **Phantom (checksums differ).** The DB row thinks
          path `X` holds content `C'`; our bytes at `X` have
          content `C`. Removing our bytes restores the
          pre-write state (DB still has a missing-file phantom
          reconcile will surface). Do the remove, bump `SEQ`,
          retry from step c at `X+1`. Abort after two retry
          cycles and return as a failure — the operator's job
          to reconcile the phantom.
        - **Match (checksums equal).** The DB row's claim on
          path `X` is legitimate, and our no-clobber write
          happened to restore its missing bytes. Do NOT remove
          our bytes — they belong to the existing row now.
          Count as a duplicate (the row already exists), return.
          Reconcile's previous "missing file" entry for this
          row is self-healed.

      This branch keeps the rule honest: bytes only get removed
      when we know they do not satisfy any legitimate DB row's
      pointer.
   f. On successful insert: if flash cache is enabled, enqueue async
      population. Return success.
5. Collect per-candidate results; return a summary
   `{imported, duplicates, path_collisions, failures}` with
   per-failure context. Non-zero `failures` → exit code `1`; zero
   failures → exit code `0`.

Step 4a uses the RO pool; step 4e uses the RW pool inside a
transaction per row.

**Invariant.** Every `media` row points at bytes on NAS at the row's
`path`. Every file under `{nas_root}/{storage_key}/(YYYY|movies|
unknown_date)/` either has a `media` row pointing at it, or is a
reconcile orphan awaiting operator review. There is no "bytes
present, DB thinks they are a different media item" state — the
no-clobber finalize plus cleanup-on-insert-failure makes that
impossible.

### 10.5 Deduplication

Within-owner by `checksum`. No cross-owner dedup in v1. The
`UNIQUE(owner_hub, owner_user_id, checksum)` constraint is the
authoritative guard; the pre-check in 3b is a politeness optimization
that short-circuits EXIF + NAS writes for duplicates.

### 10.6 Concurrent import locking

`internal/ingest/lock.go` uses `gofrs/flock` to acquire an advisory
lock on `{nas.root}/.fotobank/import.lock`. Lock held for the
duration of `ImportDirectory`. A second `fotobank import` invocation
waits with a small timeout and then returns `errs.ErrConcurrentImport`
with an explanatory message ("another import is in progress; re-run
or pass --wait").

HTTP import endpoints do not exist in Phase 1 (no web upload in v1).

### 10.7 Reconcile

`internal/reconcile` exposes `Reconcile(ctx, opts)`:

1. For each owner, walk `{nas.root}/{storage_key}/` (NAS only —
   flash isn't authoritative):
   - Photos under `YYYY/` or `unknown_date/` → build set of
     `(path, size)` tuples.
   - Videos under `movies/` → same.
   - Thumbs under `.thumbs/` → not reconciled (regeneratable; out of
     scope for this command).
   - Temp files matching `*.tmp-*` older than `grace` (default 1h) →
     candidate stale-temp list.
2. Query `media` for the owner; build set of `(path, size)`.
3. Diff:
   - NAS but not DB → candidate orphan.
   - DB but not NAS → candidate missing (the Python tool's old
     "sync-metadata" case).
   - DB and NAS with size mismatch → flag as suspicious (rare; log
     and include in report).
4. Emit a report (text or JSON). Flags:
   - `--commit-deletes`: delete the `media` rows for missing files.
     Bytes were already gone; this only affects DB.
   - `--commit-recoveries`: register NAS orphans as new `media`
     rows. Runs EXIF extraction on each, computes MD5, enqueues
     thumb generation.
   - `--delete-temp`: remove stale `.tmp-*` files.
   - Without any commit flag, reconcile is read-only.

`fotobank reconcile` in the CLI is the primary driver; HTTP doesn't
expose reconcile in Phase 1 (rare admin operation; CLI is enough).

## 11. Thumbnail pipeline

### 11.1 Worker design

`internal/thumb/worker.go` runs a goroutine started by `fotobank
server`:

```
for ctx.Err() == nil {
    claimed, err := thumbSvc.ClaimBatch(ctx, batchSize)
    if err != nil { log; sleep; continue }
    if len(claimed) == 0 {
        select { case <-ctx.Done(): return ; case <-time.After(pollInterval): }
        continue
    }
    fanOut(claimed, workerConcurrency, processOne)
}
```

`processOne`:

1. Open the original via `storage.ReadRange(... 0, -1)`.
2. Decode (`image.Decode` for photo formats; RAW → extract embedded
   preview; HEIC → `MarkNoPreview`; video → capture poster frame).
3. Resize to three sizes, encode WebP, write each to NAS via
   `storage.Write`.
4. Mark `ready` on success; `no_preview` or `failed` on known
   failures.

Lease expiry (`SweepLeases`) runs on a timer independent of the
claim loop.

### 11.2 Poster frames for videos

Uses `abema/go-mp4` to seek to the first video track and read a frame
via a minimal decoder hooked into `x/image` — or, if complexity grows,
shells out to `ffmpeg` **with a documented hard dependency** only if
all native options fail. Preference: native. The Phase 1 spec aims to
avoid `ffmpeg` dependency, accepting that some formats will land as
`no_preview` for videos too.

(This is the narrowest open question in the sub-spec. If pure-Go
decoding proves to be a rathole, we punt specific formats to
`no_preview` rather than drag in ffmpeg.)

### 11.3 RAW embedded preview

For ARW/CR2/DNG/RAF/NEF, the EXIF pass at import time sets a flag
(`has_embedded_preview` on an in-memory struct, not stored in DB).
The thumb worker re-opens the file, extracts the preview via
`dsoprea/go-exif/v3` tag walking, decodes as JPEG, resizes to the
three sizes. If extraction fails mid-pass, `MarkNoPreview`.

### 11.4 HEIC punt

HEIC files import successfully with EXIF metadata (date/camera tags
are standard EXIF inside HEIC), but the thumb worker sets
`no_preview` without attempting decode. A future sub-spec can add
`strukturag/libheif` bindings behind a build tag if HEIC support
matters enough to accept CGO; until then, the viewer renders a
placeholder and users export JPEG on ingest if they want thumbs.

### 11.5 Regeneration

`fotobank thumbs regenerate --all` (or with a specific ID) sets the
targeted rows to `pending` and nudges the worker (wakeup channel
optimisation). Out-of-date thumbnails on NAS are left; `Write`
overwrites them atomically when the new ones are produced.

## 12. HTTP API

### 12.1 Huma setup

`internal/httpapi` bootstraps huma over `humago`:

```go
mux := http.NewServeMux()
api := humago.New(mux, huma.DefaultConfig("Fotobank", version.Short))
api.OpenAPI().Info.Description = "Fotobank HTTP API"
// register routes…
srv := &http.Server{Addr: cfg.HTTP.ListenAddress, Handler: mux, …}
```

### 12.2 OpenAPI spec binary

`cmd/fotobank-openapi/main.go`:

1. Load config (or accept an empty one; most of the setup doesn't
   need it for spec dumping).
2. Build the same huma `api` as the server.
3. `json.NewEncoder(os.Stdout).Encode(api.OpenAPI())`.

`make api-generate` pipes this to `frontend/openapi/openapi.json`
(directory created in Phase 3 when the frontend lands; for now the
spec is dumped to the repo root or a stable path for client
consumers to pick up).

### 12.3 Route list

All routes are versioned under `/api/v1`. Huma auto-generates path
parameter binding from struct tags; each handler lives in a
`*_handler.go` file under `internal/httpapi/`.

**Read (any authenticated request):**

- `GET  /api/v1/me` — returns the caller's identity (principal,
  scopes resolved to a list of scope summaries).
- `GET  /api/v1/media` — paginated list; query params
  `owner` (scoped to requester's visibility), `media_type`,
  `date_from`, `date_to`, `album_id`, `limit`, `offset`, `sort`.
- `GET  /api/v1/media/{id}` — detail.
- `GET  /api/v1/media/{id}/original` — streams bytes. Range-aware.
  Applies `allow_download` gating for grantees.
- `GET  /api/v1/media/{id}/thumb?size=grid|preview|lightbox` —
  streams WebP. 404 if not in visible set; 409 if
  `thumb_status != ready`.
- `GET  /api/v1/albums` — paginated.
- `GET  /api/v1/albums/{id}` — detail, including ordered media.
- `GET  /api/v1/shares` — list scopes visible to the caller: owner
  sees their own, grantee sees shares granted to them. Returns a
  union with a `role` field (`owner` | `grantee`).

**Write (owner-only admin):**

- `POST /api/v1/albums` — create.
- `PATCH /api/v1/albums/{id}` — rename.
- `DELETE /api/v1/albums/{id}`.
- `POST /api/v1/albums/{id}/media` — add media IDs.
- `DELETE /api/v1/albums/{id}/media` — remove media IDs.
- `PUT /api/v1/albums/{id}/media/order` — reorder.
- `POST /api/v1/shares` — create scope.
- `POST /api/v1/shares/{uuid}/retry` — retry failed broker sync.
- `DELETE /api/v1/shares/{uuid}` — revoke.
- `DELETE /api/v1/media/{id}` — delete media and bytes.

No import or reconcile over HTTP in Phase 1 (CLI only).

### 12.4 Request middleware

Wrap every request with:

1. **Direct-access guard** (header mode) — reject connections that
   violate the configured ingress constraints (§8.4).
2. **Identity** — `identity.Provider.FromRequest`; attach
   `Identity` to the request context. `ErrIdentityMissing` → 401.
3. **Request ID** — inject `X-Auth-Request-Id` into context; if
   absent, generate one.
4. **Structured logging** — slog logger with request ID, method,
   path, status, duration. Bodies never logged.

### 12.5 Response envelope

Huma's default shapes (RFC 7807 for errors, raw JSON for success)
apply. Pagination uses a simple `{items: [...], next_offset: N,
total: M}` envelope declared once and reused via a generic helper.

### 12.6 Byte streaming, range, and cache headers

`/api/v1/media/{id}/original` and `/api/v1/media/{id}/thumb` use huma's
`StreamResponse` (or fall back to raw `http.ResponseWriter` via a huma
passthrough). Range handling uses `http.ServeContent` under the hood,
fed a `io.ReadSeeker` wrapping the storage layer — but since our
`Store.ReadRange` is not seekable, we implement a thin `ReadSeeker`
shim that issues fresh `ReadRange` calls on `Seek`. For small files
this is trivially efficient; for large videos, `http.ServeContent`
typically issues one Range per client request.

**Originals** — bytes never change once imported, so aggressive
caching is safe:

- `ETag`: the media's `checksum` (MD5).
- `Last-Modified`: `imported_at`.
- `Cache-Control: private, max-age=31536000, immutable`.

**Thumbnails** — can be regenerated at the same URL when sizing
policy changes or a generation bug is fixed. Aggressive caching
without a versioning handle would leave stale bytes in browsers.

- `ETag`: `W/"{media_id}-{size}-v{thumb_version}"`. Weak ETag
  because the same `thumb_version` may re-encode to slightly
  different bytes if the encoder is upgraded between runs; content
  equivalence is preserved but byte equality is not guaranteed.
- `Last-Modified`: `thumb_updated_at`.
- `Cache-Control: private, max-age=86400, must-revalidate`. Daily
  revalidation via `If-None-Match` yields cheap `304` when
  unchanged; when `thumb_version` bumps, the ETag changes and the
  client fetches the new bytes.

`fotobank thumbs regenerate` routes through `ThumbService.Enqueue`,
which atomically sets `thumb_status = 'pending'`, increments
`thumb_version`, and updates `thumb_updated_at`. The bump happens at
the **start** of the regeneration lifecycle — before the worker has
even picked up the job — so clients observing the new version via
listings or ETag revalidation invalidate their caches immediately,
regardless of whether the regeneration ultimately ends in `ready`,
`no_preview`, or `failed`. This keeps cache state coherent even
when regeneration fails: clients never keep serving a stale old
thumbnail under a URL whose server-side state says "regenerated."

List endpoints that return media summaries include the current
`thumb_version` so client-side image tags can append it as a query
parameter (e.g., `?v={thumb_version}`) for cache-busting if they
choose not to rely on ETag revalidation.

## 13. CLI surface

### 13.1 Subcommand dispatch

`internal/cli/dispatch.go` follows middleman's `runCLI` pattern:

```go
func runCLI(args []string, out io.Writer, errOut io.Writer) int {
    if len(args) == 0 {
        printUsage(errOut); return 2
    }
    switch args[0] {
    case "server":              return runServer(args[1:], out, errOut)
    case "import":              return runImport(args[1:], out, errOut)
    case "reconcile":           return runReconcile(args[1:], out, errOut)
    case "migrate":             return runMigrate(args[1:], out, errOut)
    case "albums":              return runAlbums(args[1:], out, errOut)
    case "shares":              return runShares(args[1:], out, errOut)
    case "thumbs":              return runThumbs(args[1:], out, errOut)
    case "owners":              return runOwners(args[1:], out, errOut)
    case "config":              return runConfig(args[1:], out, errOut)
    case "version":             return runVersion(out)
    case "help", "-h", "--help": printUsage(out); return 0
    default:                    printUsage(errOut); return 2
    }
}
```

Each subcommand has its own `flag.NewFlagSet` — no shared global
flags, but `--config` is accepted everywhere and defaults to the
resolved config path.

### 13.2 Global `--json`

Every read/write command that emits structured output supports
`--json`. Without the flag, output is human-readable (TTY-aware
coloring may come later). With the flag, stdout is a single JSON
object (for scalar commands) or JSON array (for list commands) per
run, terminated with a single newline. Errors still go to stderr as
plain text.

Stable JSON schemas are documented per command and covered by tests
(golden files under `testdata/cli-json/`).

### 13.3 Command list and summaries

```
fotobank server
    Start the HTTP server. Blocks until signal; graceful shutdown on
    SIGTERM. Honors --config, --listen.

fotobank import <directory> [--owner <hub>:<user_id>] [--wait] [--json]
    Walks <directory>, imports new media into the primary owner's
    library (or --owner). --wait makes concurrent imports block
    instead of erroring. Default: copy; --move to move source files.

fotobank reconcile [--owner <...>] [--json]
                   [--commit-deletes] [--commit-recoveries] [--delete-temp]
    Scans NAS + DB per §10.7.

fotobank migrate --from-legacy <path> --owner <hub>:<user_id>
                 --storage-key <slug> [--mode symlink|move]
                 [--legacy-durable] [--dry-run]
    One-shot migration from the Python tool. Required fields fail
    closed. symlink mode requires --legacy-durable.

fotobank albums create <name> [--owner <...>] [--json]
fotobank albums list [--owner <...>] [--json]
fotobank albums get <id> [--json]
fotobank albums rename <id> <new_name>
fotobank albums delete <id>
fotobank albums add <id> <media_id...>
fotobank albums remove <id> <media_id...>
fotobank albums reorder <id> <media_id...>   # positional order = new order

fotobank shares create
    --album <id> | --media <id...>
    --grantee <hub>:<user_id>
    [--download] [--expires <duration>] [--label <text>] [--json]
fotobank shares list [--owner <...>] [--json]
fotobank shares get <uuid> [--json]
fotobank shares retry <uuid>
fotobank shares revoke <uuid>

fotobank thumbs regenerate <media_id|--all>
fotobank thumbs status <media_id> [--json]

fotobank owners add --hub <h> --user-id <u> --storage-key <slug> [--handle <h>]
fotobank owners list [--json]
fotobank owners remove --hub <h> --user-id <u> [--purge]

fotobank config path
fotobank config read <dotted.key>
fotobank config validate

fotobank version
    Prints version/commit/build-date.
```

### 13.4 Exit codes

- `0` — success.
- `1` — runtime error (returns stderr message).
- `2` — usage error (args/flag parse failure).

## 14. Migration from the Python tool

### 14.1 Pre-flight

`fotobank migrate` first:

1. Validates the required flags.
2. Confirms `--mode symlink` has `--legacy-durable`.
3. Opens `{legacy_base}/registry.sqlite` read-only.
4. Resolves the migration state:
   - **First-run state:** no `owners` row matches
     `--owner`/`--storage-key` AND no `media` rows exist for that
     owner. Proceed normally.
   - **Resume state:** an `owners` row matches AND `media` rows
     exist for that owner. A prior `fotobank migrate` landed
     partial progress; treat this invocation as a resume. Log:
     `resuming prior migration: N media rows already present for
     owner <hub>:<user_id>`. Continue.
   - **Conflict state:** an `owners` row exists with a *different*
     `storage_key` than requested, OR the requested `storage_key`
     is taken by a *different* principal. This is operator error
     — abort with a clear message. The safe recovery is either
     picking a new `storage_key` or removing the existing owner
     with `fotobank owners remove`.
5. There is intentionally no "target NAS path must be empty"
   pre-flight. An already-populated tree is either a legitimate
   resume (existing rows point at those bytes, and
   UNIQUE(owner, checksum) will skip them) or foreign bytes at a
   colliding path — the per-row `os.Link` / `storage.Write` calls
   in §14.2 are no-clobber, so foreign bytes surface as per-row
   failures during execution rather than up front. Pre-flight
   catches the *identity* mistake (wrong owner / storage_key
   collision, step 4); fs-level no-clobber catches the *path*
   mistake during execution. Relying on filesystem emptiness here
   would block the legitimate resume path.

### 14.2 Execution

Migration is **per-row atomic, not run-level atomic**: each source
item's bytes are materialised first, verified, then the DB row is
inserted in its own transaction. A failure on item N leaves rows
1..N-1 fully migrated and item N reported as a failure. Re-running
`fotobank migrate` is idempotent — the within-owner
`UNIQUE(owner, checksum)` already skips items that made it through,
so the second run only processes the failures.

Top-level flow:

1. Insert/ensure the `owners` row in its own transaction.
2. Stream-read legacy `photos` rows one at a time. For each, run the
   per-row pipeline below.
3. Walk `{legacy_base}/movies/` one file at a time. For each, run the
   same per-row pipeline with `media_type='video'`.
4. Emit the final report (§14.4).

**Per-row pipeline** (target-first, DB commit, then source
removal — the source file is the safety net and is not touched
until the row is durably committed):

a. Generate a new UUID and compute the target key from the legacy
   row's timestamp + filename (photos) or MD5 (videos). Map legacy
   columns to the new schema (sentinel strings like
   `make='unknown'` → NULL per master §9.3).
b. **Place bytes at the target without removing the source.**
   - **`move` mode, same filesystem:** `os.Link(source, target)` —
     creates a second directory entry for the same inode. Both
     source and target now reference the bytes; source is not
     removed at this point. No-clobber naturally via `link(2)`:
     fails with `EEXIST` if the target is already present, which
     signals the path-collision handling (bump `_seq`, retry).
   - **`move` mode, cross-filesystem:** stream-copy source bytes
     through `storage.Write` (no-clobber finalize, §7.2). Source
     remains intact.
   - **`symlink` mode:** resolve the legacy source to an absolute
     path; `os.Symlink(abs_source, target)`. Source is the byte
     store by design.
c. Verify the target is readable: `os.Stat`. For `move`
   cross-filesystem, re-checksum the target and compare to the
   legacy row's checksum to detect copy corruption.
d. **Insert the `media` row in a per-row transaction**, `path`
   set to the target. Success → proceed to step e. Failures:
   - `UNIQUE(owner, checksum)` violation → the content was
     already migrated on a prior run. Remove the target bytes we
     just materialised (safe — no-clobber finalize guarantees we
     created them this run, and the legacy source is still
     intact), leave the source alone, count as
     `already_migrated`, return.
   - Any other insert error → remove the target bytes, leave the
     source alone, record as a failure, return.
e. **Only after a successful DB commit**, perform the final
   source-side action:
   - `move` mode: `os.Remove(source)`. The target is already
     durable and referenced by a committed row; source removal
     cannot lose data even if it fails (we simply leave the
     source behind as a cleanup task, reported as a warning).
   - `symlink` mode: no-op. The source IS the byte store.
f. If any of a-d fail: clean up any partially-materialised bytes
   at the target (`move`: `os.Remove(target)`; `symlink`: ditto).
   Source is untouched. Record the failure with source path and
   reason, continue to the next row.

**Invariant.** At every point in the per-row pipeline, the source
file exists until after the target is durably committed in the DB.
A SQLite error, a `UNIQUE` collision, or a crash cannot lose media
— the worst outcome is an orphan target file that
`fotobank reconcile` reports and the operator re-runs migration
against.

Step d uses per-row transactions rather than one big transaction so
a single bad row cannot invalidate the work of thousands of
successful ones, and because wrapping a long-running migration in
one SQLite transaction would hold a lock for the whole run.

### 14.3 Filesystem specifics

- **Cross-device move.** When `os.Link` fails with `EXDEV`, fall
  back to `storage.Write`-based stream-copy (still using the
  no-clobber finalize). Source is removed only after the per-row
  DB commit (§14.2 step e).
- **Symlink target.** Resolved to an absolute path so moving the
  fotobank deployment later does not break the links. Relative
  symlinks are a trap on migration.
- **Source-removal errors.** If `os.Remove(source)` fails in step
  e (permissions, read-only filesystem, race with another tool),
  the DB row is already committed and durable — the failure is
  logged as a warning in the final report. The operator's follow-
  up is to delete the source manually; fotobank will not retry.
  This is deliberately tolerant: source removal is the cleanup
  half of a move, not the durability half.
- **Lightroom catalog post-step.** In `move` mode, the final
  report includes a message directing the operator to update the
  LR catalog's folder location (master §12).

### 14.4 Reporting

Final report to stdout (or `--json`):

- `migrated_photos`, `migrated_videos` counts.
- `already_migrated` count (rows that hit `UNIQUE(owner, checksum)`
  — these are no-ops on re-runs).
- `failures` list: one entry per failed row with source path,
  reason, and target path if one was attempted. Non-empty
  `failures` → exit code `1`.
- `warnings` list (e.g., "symlink mode chosen; NAS authority
  depends on {legacy_base}/ being durable", "LR catalog must be
  re-pointed at the new NAS path").

The `failures` list is actionable: each entry is a source file the
operator can fix (permissions, missing file, etc.) and then re-run
`fotobank migrate` to pick up only the outstanding items — the
`UNIQUE(owner, checksum)` dedup on already-migrated rows means
re-runs are cheap.

### 14.5 Lightroom coexistence

Covered by master §12. The CLI prints a tailored post-migration
guidance message:

- For `--mode symlink`: LR catalog unchanged; do nothing.
- For `--mode move`: instruct the operator to run LR's "Locate
  Folder" against the new `{nas_root}/{owner_storage_key}/` path.

## 15. Testing strategy

### 15.1 Layering

- **Unit tests** per package (`*_test.go` alongside code). Table-
  driven.
- **Integration tests** under `_test.go` files using
  `testutil.OpenTestDB(t)`, `testutil.TempNAS(t)`, and a small fixture
  library of sample media files under `testdata/`.
- **CLI tests** invoke `runCLI` directly (no subprocess). Capture
  stdout/stderr to buffers, assert output.
- **HTTP tests** use `httptest.Server` wrapping the huma handler,
  drive it with a generated client from `cmd/fotobank-openapi` (the
  generated Go client lives under `internal/apiclient/generated/`
  like middleman).

### 15.2 Golden files

- EXIF extraction: for each supported format, a tiny checked-in
  sample under `testdata/exif/` plus a `.json` golden. Tests
  `reflect.DeepEqual` the extracted struct to the golden.
- CLI JSON output: golden files under `testdata/cli-json/`.
- OpenAPI spec: `testdata/openapi/openapi.json` regenerated via
  `make api-generate`; CI fails if `git diff` shows drift.

### 15.3 Concurrency tests

- Import race: two goroutines call `ImportDirectory` with overlapping
  sources; assert dedup is exact and no orphan bytes remain.
- Thumb worker race: N workers pointed at the same DB; assert each
  row transitions `pending → working → ready` exactly once, no
  duplicate writes.
- Direct-access guard: for each guard mode, send a request that
  violates it and assert the appropriate error.

### 15.4 Fixtures

`testdata/` includes:

- Small JPEGs with known EXIF (various cameras).
- One tiny ARW with an embedded preview.
- One HEIC (for the no-preview path).
- One short MP4 with QuickTime creation date.
- One tiny AVI.
- A few "broken" files (truncated JPEG, corrupt EXIF) to exercise
  error paths.
- Total size constrained to keep the repo small (< 5 MB across all
  fixtures).

### 15.5 Coverage targets

No hard percentage gate, but every sentinel error path and every
state transition on `thumb_status` / `broker_status` must have at
least one test. Reviewers check this during code review, not CI.

## 16. Build, release, install

### 16.1 Makefile

Already listed in §3.1. Key targets:

- `make build` — debug binary with `ldflags` injecting
  version/commit/buildDate.
- `make build-release` — `-trimpath -s -w` + release ldflags.
- `make install` — prefer `~/.local/bin`, fall back to `$GOBIN` /
  `$(go env GOPATH)/bin`.
- `make api-generate` — regenerate checked-in OpenAPI JSON.

### 16.2 Cross-compile

Pure Go end to end (no CGO) means trivial cross-compile. Release
tarballs built via a simple script matrix over `GOOS` × `GOARCH`
(darwin/amd64, darwin/arm64, linux/amd64, linux/arm64). Packaging
detail is release-time concern, not Phase 1 core.

### 16.3 Version stamping

Standard Go idiom:

```go
var (
    version   = "dev"
    commit    = "unknown"
    buildDate = "unknown"
)
```

Set via `-ldflags "-X main.version=..."`. `fotobank version` prints
them.

## 17. Open questions (to resolve during implementation)

1. **Video poster-frame decoding.** Pure-Go vs ffmpeg. Preference:
   pure-Go with a punt to `no_preview` for formats we can't decode,
   landing on the narrow set Go libraries cover. If the punt list
   becomes unreasonably large, escalate to a Phase 1 follow-up
   adding optional ffmpeg.
2. **Huma path-parameter UUID binding.** Huma validates and parses
   query/path parameters via struct tags. Confirm whether it has a
   native `uuid.UUID` decoder or if we need a custom type +
   `huma.Register` adjustment. Decide early in implementation.
3. **Backup sub-spec interplay.** §11 says SQLite snapshots go to
   `{nas_root}/.fotobank/snapshots/`. This sub-spec assumes the
   snapshot goroutine is a simple time ticker calling `VACUUM INTO`.
   If backup warrants its own sub-spec before shipping (master
   §15.10), this section can be minimized.
4. **Server graceful shutdown semantics.** Long-running imports
   triggered over HTTP don't exist in Phase 1, but the thumb worker
   and broker worker do. `fotobank server` must drain both on
   shutdown (cap at, say, 30s) so in-flight thumb generations finish
   cleanly. Implementation detail; called out here so the plan
   budgets time for it.
5. **`owners.storage_key` sanitisation.** Accept `[a-zA-Z0-9_-]+`
   only; reject slashes, dots, spaces. Confirm the regex during
   implementation; trivial to tighten.
6. **SQLite driver-specific UUID handling.** modernc's driver binds
   `[16]byte` automatically to BLOB; we use TEXT consistently via
   `uuid.UUID.String()`. Confirm the Scan path doesn't get clever
   and auto-convert.

---

*End of Phase 1 design sub-spec.*
