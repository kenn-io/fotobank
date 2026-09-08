# Runtime and Boundaries

## Process shape

Fotobank is one Go binary with two main uses:

- Cobra commands in `internal/cli` provide import, maintenance, administration,
  and server entry points.
- `fotobank daemon run` (or `serve`) composes the HTTP API, embedded frontend, repositories,
  services, content and artifact stores, and background workers.

The Svelte application is built into `internal/web/dist` and embedded in the Go
binary. The outer HTTP mux sends `/api/` to the API handler and all other paths
to the single-page application.

Server composition happens at the process edge in `internal/cli/server.go`.
Packages receive explicit collaborators; they do not open a second database,
Docbank vault, or storage tree for convenience.

## Domain layering

The normal call path is:

```text
HTTP or CLI transport → authorization service → domain repository → SQLite
                                      └──────→ content/artifact boundary
```

- Repositories under `internal/<domain>` own SQL and domain persistence. They
  take domain identifiers and values, not caller credentials.
- Services under `internal/service` are the authorization boundary. Every
  user-scoped operation receives an `owners.Principal`, clamps queries to that
  caller, and normally returns `errs.ErrNotFound` for another owner's object.
- HTTP and CLI transports call services to keep owner scoping consistent.
  The host operator is trusted to control local configuration and storage;
  application ownership checks do not isolate data from that operator.
- Background workers claim durable queue rows, perform bounded work, and
  finalize the claim. They do not depend on request goroutines remaining alive.

Cross-cutting sentinels live in `internal/errs`. Code wraps them with operation
context. `internal/httpapi/errors.go` translates them to HTTP responses; a
surface with different disclosure rules may override a mapping locally and
delegate the rest.

## HTTP

`internal/httpapi/api.go` builds one `http.ServeMux` and one Huma API. Huma owns
JSON operations. Raw handlers own byte streams and server-sent events:

- `/api/v1/media/{id}/original`
- `/api/v1/media/{id}/files/{fileID}/content`
- `/api/v1/media/{id}/thumb`
- `/api/v1/shared/media/{id}/original`
- `/api/v1/shared/media/{id}/thumb`
- `/api/v1/events`

The runtime API and OpenAPI generator use the same JSON operation definitions,
including search, AI, and facets. Registration does not need a database,
Docbank vault, or provider. Missing search, AI, or facets services produce a
503 response after the handler's identity check, rather than removing the
operation from the contract. A configured server keeps its existing behavior.

The server publishes the contract at `/api/openapi.json` and interactive docs
at `/api/docs`. `make api-generate` writes the checked-in `openapi.json` and
frontend TypeScript bindings. Raw byte and event routes remain outside Huma's
JSON contract; their handlers own streaming, headers, and range behavior. See
[`frontend.md`](frontend.md#api-contract) for the frontend boundary.

The middleware execution order is request metrics and recovery, identity,
hidden-media unlock validation, principal-display caching, then routing.
Request IDs and principals are put in context before domain handlers run.

## Identity and authorization

An owner is the pair `(hub, user_id)`. `owners.Principal` is the in-process
value. Each owner also has an immutable opaque storage UUID that is safe for
internal path construction.

Two identity modes exist:

- `stub` supplies one configured principal for local development and
  single-user operation.
- `header` accepts identity headers only after its direct-access guard accepts
  the request. The guard accepts a loopback or Unix-socket listener, configured
  proxy network ranges, or a configured shared proxy secret. Network ranges
  and the secret are additive when both are configured.

Fotobank does not terminate TLS or inspect client certificates. A deployment
that uses mutual TLS must terminate it at the external proxy and must still
restrict Fotobank access with one of the guards above. The application never
treats a CA-file setting as evidence that a request passed mutual TLS.

Knowing an asset UUID, file UUID, Docbank node ID, or virtual path grants no
access. Services and share-capability checks make the authorization decision.

## Concurrency and lifecycle

SQLite uses separate read and write pools. Repository write operations group
related changes in explicit transactions. Queue claims use persisted lease
timestamps so abandoned work can be reclaimed.

The server owns one embedded Docbank adapter for its whole lifetime. Shutdown
stops incoming requests and workers before closing storage and database
resources. The content adapter translates errors that happen during streaming,
not only errors returned while opening a reader.

Docbank holds an exclusive vault lock for that lifetime. GPS backfill,
import, interrupted-import recovery, checkout creation,
commits, manual and scheduled archives, and checkout scanning reuse the server's
adapter.
Every checkout command uses the daemon. List and status inspect saved catalog
state there; the CLI opens neither the catalog nor the vault.

### Local operator commands

The server also owns a separate configurable loopback control listener
from `internal/operator`. Import, every checkout command, and manual backup creation
use the typed `internal/client` HTTP client to discover it beside the canonical SQLite path, in
`<database>.operator/`. Kit publishes a runtime record atomically
inside a current-user-only directory. A fresh random credential lives in that
record. The client requires Kit's possession proof before sending the bearer
credential and accepts only loopback endpoints with a matching service and
reported application version. `internal/client/lifecycle.go` uses Kit's Manager
and start lock to coordinate automatic or explicit startup, and StartDetached
to launch the current executable with the same configuration, working directory,
and canonical database path. Neither discovery nor launch opens the database
or vault in the client. A different running build version is stopped through
the authenticated shutdown operation before replacement. Development builds
with the same version string require explicit restart after rebuilding. There
is one API contract, with no protocol negotiation or compatibility layer.

`daemon start`, `restart`, `stop`, and `status` live in `internal/cli/daemon.go`.
Start/status return the shared `DaemonStatus` result; start/restart print its
web UI URL. The server derives that URL from `http.base_url`, or the actual
bound web address if unset. The web, control, and metrics ports are configured
with `http.listen_address`, `daemon.listen_address`, and
`observability.admin_listen`. See [setup](../guides/setup.md).

Stop/status never launch a process. The stop endpoint acknowledges before
canceling the server. Every shutdown path closes the operator listener before
draining the photo listener and workers, so it cannot accept new commands
during that drain. The client waits for the runtime record to disappear
after workers, storage, and lifetime locks have closed. Restart then starts
the replacement. Stop uses `daemon.stop_timeout`; startup/replacement uses
`daemon.start_timeout`. Timeouts report an error rather than force-killing
unfinished writes. Background logs live at `<database>.operator/daemon.log`.

The local and photo listeners use the same `httpapi.New` registrations and
OpenAPI document at `/api/openapi.json`, with documentation at `/api/docs`.
`internal/httpapi` owns the wire types shared with `internal/client`, following
Docbank's typed-client pattern. Kit owns runtime records, endpoints, proof,
process identity, launch locking, and detached startup;
Fotobank owns authorization and application services. There is no separate
operator-only schema. The local listener requires its credential before any
API request. Only it receives `OperatorDeps` and `DaemonDeps`; the photo listener rejects
operator operations even for an authenticated photo owner. The shared schema
marks these operations with the `localOperator` bearer requirement.

The migrated command/API pairs are below (paths start with
`/api/v1/operator`). List and status take `hub` and `user_id` query parameters;
photo operations take the configured principal in their JSON body. Lifecycle
operations use the host credential without a photo principal.

| CLI command | HTTP operation |
| --- | --- |
| `import <source>` | `POST /imports` (streamed progress and result) |
| `content recover` | `POST /content/recover` |
| `gps backfill` | `POST /gps/backfill` |
| `checkout list` | `GET /checkouts` |
| `checkout status <id>` | `GET /checkouts/{checkout_id}` |
| `checkout estimate` | `POST /checkouts/estimate` |
| `checkout create <root>` | `POST /checkouts` |
| `checkout commit <id>` | `POST /checkouts/{id}/commit` |
| `backup create` | `POST /backups` |
| `daemon status` | `GET /daemon` |
| `daemon stop` | `POST /daemon/stop`, then wait for cleanup |

Start launches the process locally when needed; restart combines stop and
start. They are process lifecycle operations, not alternate data paths.

Album commands use the existing photo API routes on the authenticated local
connection, not a second set of operator-only album handlers:

| CLI command | HTTP operation |
| --- | --- |
| `albums create` | `POST /api/v1/albums` |
| `albums rename` | `PATCH /api/v1/albums/{id}` |
| `albums delete` | `DELETE /api/v1/albums/{id}` |
| `albums list` | `GET /api/v1/albums` |
| `albums show` | `GET /api/v1/albums/{id}` and `GET /api/v1/albums/{id}/media` |
| `albums add` | `POST /api/v1/albums/{id}/media` |
| `albums remove` | `DELETE /api/v1/albums/{id}/media/{media_id}` |

`internal/client/albums.go` uses request and response types from the Huma
registrations in `internal/httpapi/albums.go`. The CLI validates argument syntax
before startup and requires stub mode. The daemon supplies the configured
principal; callers cannot override it. The same `AlbumService` owner checks,
hidden-media restrictions, and share-related deletion checks apply to HTTP and
CLI requests. These commands neither open the catalog nor construct services.
They start a missing daemon through the shared lifecycle and never retry an
HTTP mutation automatically. `albums list --json` returns the HTTP page shape
with `items` and optional `next_offset`.

Sharing commands follow the same pattern through `internal/client/shares.go`
and the existing Huma registrations in `internal/httpapi/shares.go`:

| CLI command | HTTP operation |
| --- | --- |
| `shares create` | `POST /api/v1/shares` |
| `shares list` | `GET /api/v1/shares` |
| `shares show` | `GET /api/v1/shares/{uuid}` |
| `shares revoke` | `POST /api/v1/shares/{uuid}/revoke` |
| `shares retry` | `POST /api/v1/shares/{uuid}/retry` |

The CLI requires stub mode, validates argument syntax before automatic startup,
and uses the daemon's configured principal. It opens no catalog and constructs
no share service. Shared HTTP types define creation options, list filters,
detail results and pages; the server applies list limits. Broker work remains
asynchronous in the daemon. Already-revoked and retry-not-applicable operations
return HTTP 409 and a nonzero CLI exit, not a CLI-only success. Sharing access
checks and hidden-content filtering remain in the existing services and byte
handlers. No new sharing authorization is granted by this transport change.

`POST /api/v1/operator/imports` calls `ImportService` using the daemon's catalog,
content adapter, and geo resolver. The configured owner is checked at both
the transport and service boundary. The service takes the existing import
lock and captures the current AI settings when that import begins. Workers,
settling, grouping, deduplication, and exact-content receipts use the existing
`ingest.Importer`; the CLI constructs neither storage handles nor an importer.

The Huma contract describes newline-delimited JSON `ImportEvent` records:
progress followed by one final result with partial counts and failures. The
typed client rejects EOF without a result and never resubmits a request.
Disconnect cancels the request, stops new file dispatch, and joins workers.
Discovery checks cancellation for every entry, including skipped files and
directories; source hashing checks between reads and closes its file on
cancellation. Completed imports and durable reservations remain available for a rerun.
Shutdown closes and joins the operator handlers before storage cleanup. There
is no detached import job or CLI storage fallback. Human progress remains on
stdout normally, or stderr with `import --json`; JSON stdout is the final result.

`POST /api/v1/operator/checkouts/{id}/commit` accepts the configured hub
and user ID, checks them against the server's stub owner, and calls the existing
`CheckoutService.Commit`. Estimate and create use
the same owner check and service for selection and creation. The create request
includes an absolute local destination, selectors, and capacity limit. The CLI
prefixes relative destinations with its working directory without cleaning
symlink-sensitive `..` components. `CheckoutService.CreateAt` binds and validates
the destination through the server's content adapter before materialization.
Only authenticated local operators can request host-file creation, not photo users.
Header identity mode exposes only lifecycle operations on the local control
interface; it does not grant photo-management permissions through that interface.

`POST /api/v1/operator/backups` checks the same configured stub principal and invokes
`BackupService.Create` with an absolute repository path and optional tag. It
captures all owners and hidden media, not just the configured owner's photos.
Only initialized repositories are accepted, and the scheduler's reserved tag
is rejected by the service. Relative CLI repository paths are made absolute
without cleaning symlink-sensitive parent components. Successful
`backup create --json` output remains the snapshot itself. Failures exit nonzero; connection
loss is not retried automatically because the archive may already be published.
List and verify the repository before retrying. Request cancellation reaches
archive capture, and shutdown joins the handler before closing the vault.

Checkout commit returns pending, committed, and conflict counts plus an optional
error. An operation may finish some entries before failing; its result retains
those counts and the CLI exits nonzero. Connection loss is not automatically
retried: the operator checks saved status before retrying. Existing exact-version
receipts handle retries without appending an already-adopted version. Request
cancellation reaches estimate, creation, and commit processing. Creation returns
selected file/byte totals, the number of recorded materialized files, and the
checkout ID once reserved, including on failure. Failure records an errored
checkout without deleting partial working files; an interrupted process is
reconciled by the next creation under the existing creation lock. Creation is
not automatically retried. Shutdown cancels operator requests,
stops accepting work, and joins handlers before storage closes. Discovery is
removed only after all storage and lifetime-lock cleanup has completed.

`content recover` calls `ImportService.Recover` through the operator API. The
configured host operator can reconcile every registered owner's interrupted
imports; this is not a photo-user permission. Recovery holds the same import
lock, captures current AI settings after acquiring it, and uses the daemon's
catalog, vault, and geo resolver. It returns per-owner reports, including the
partly completed owner if an error occurs, without deleting unmatched files.
Requests are canceled and joined during shutdown. The CLI validates `--wait`
before automatic startup and formats the shared result, including errors.

`gps backfill` uses `GPSService` with the daemon's content resolver, vault and
gazetteer. `GPSOperatorDeps` is provided only to the authenticated local
listener, in both stub and header modes. The host operator may select any
registered owner or all owners; without a scope, only stub mode supplies a
default owner. These are host-administration rights, not photo-user rights.
The service preserves keyset paging, skips videos, validates exact Docbank
content before extracting GPS, and writes only while the expected content
version remains current. Relabeling reads catalog coordinates, not source
bytes. Cancellation stops further rows; per-photo failures are collected while
other rows continue. The final result contains counts, failures and any error;
the CLI exits nonzero on partial failure and never retries automatically.

Owner registration, listing, and removal use `internal/client/owners.go` and
the host-operator routes in `internal/httpapi/operator_owners.go`:

| CLI command | HTTP operation |
| --- | --- |
| `owners add` | `POST /api/v1/operator/owners` |
| `owners list` | `GET /api/v1/operator/owners` |
| `owners remove` | `DELETE /api/v1/operator/owners?hub=…&user_id=…` |

The daemon supplies `OwnersOperator` only on the authenticated local listener,
in both stub and header mode. Photo-user identity does not grant these operations.
The CLI validates required arguments and storage UUIDs before automatic startup;
it never constructs an owner service or opens a catalog. `OwnerService` retains
registration idempotency, immutable storage keys, display-handle updates, and
refusal to remove owners referenced by assets or checkouts. Removal does not
delete files; bulk purge remains unsupported. `owners list --json` returns the
shared HTTP result with `items`. The configured stub owner is ensured at daemon
startup, so a fresh stub deployment already contains that owner.

The daemon-only command boundary is not yet complete. Privacy/admin,
thumbnails, and AI commands still construct catalog services in
the CLI. Backup repository inspection and restore also still run in the CLI.
These existing paths are migration work in kata, not exceptions to extend.
The accepted boundary is one daemon-owned implementation per application
operation, shared by HTTP, the CLI, and a future MCP client. Bootstrap and
lost-source recovery must retain that ownership boundary.

Long-running operations honor `context.Context`. Background loops use bounded
polling, concurrency, and shutdown waits; they do not start untracked
goroutines from transports.

The checkout scanner is one of those server-owned loops. It runs an immediate
full scan at startup and repeats at `checkouts.scan_interval`. Per-checkout
errors are logged without preventing other active roots from being scanned;
the next interval retries from the durable settle observations in SQLite.
The server may start while the external NAS root is absent, including when a
configured NAS symlink has no reachable target, so `/readyz` can report the
outage. Checkout and import root validation still fails closed until every
managed boundary resolves; the scanner retries after the NAS returns.
NAS artifact writers open the externally managed root rather than creating it,
so an absent mount cannot silently become a local directory.

The optional archive worker also belongs to the server. `[backup].enabled`
defaults to false; enabling it requires an explicit initialized
`backup.repository`. The worker reuses the server's live Docbank adapter and
catalog path to capture complete archives. It reads the latest persisted
scheduled recovery point at startup, captures immediately if none exists, and
otherwise honors `backup.interval` across restarts. Failed attempts retry after
the shorter of that interval and five minutes. Successful capture precedes
scheduled retention and pruning through the content boundary. Shutdown waits
for this worker before closing its storage collaborators. See
[backup and restore](operations.md#backup-and-restore) for capture and retention
ownership.
