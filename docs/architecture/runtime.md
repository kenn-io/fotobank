# Runtime and Boundaries

## Process shape

Fotobank is one Go binary with two main uses:

- Cobra commands in `internal/cli` provide import, maintenance, administration,
  and server entry points.
- `fotobank serve` composes the HTTP API, embedded frontend, repositories,
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

Docbank holds an exclusive vault lock for that lifetime. Standalone import,
content recovery, and manual archive creation
also open the vault, so the server must be stopped before those commands run.
The CLI does not forward those operations to the server. Checkout creation,
commits, scheduled archives and checkout scanning reuse the server's adapter.
Checkout list and status omit the adapter and can run alongside the server, although their CLI
startup still opens the normal database and ensures the configured owner.

### Local operator commands

In stub identity mode, `serve` also owns a separate ephemeral loopback listener
from `internal/operator`. Checkout estimate, create, and commit discover it beside the canonical
SQLite path, in `<database>.operator/`. Kit publishes a runtime record atomically
inside a current-user-only directory. A fresh random credential lives in that
record. The client requires Kit's possession proof before sending the bearer
credential and accepts only loopback endpoints with a matching service and
reported application version. It does not start a server or open the database
or vault itself. No record, a stale record, or an incompatible server means the
operator must start the matching server explicitly.

The listener requires the credential for commands and its separate Huma
`/openapi.json` and `/docs` endpoints. It is not mounted on the photo API or the
observability listener. `POST /checkouts/{id}/commit` accepts the configured hub
and user ID, checks them against the server's stub owner, and calls the existing
`CheckoutService.Commit`. `POST /checkouts/estimate` and `POST /checkouts` use
the same owner check and service for selection and creation. The create request
includes an absolute local destination, selectors, and capacity limit. The CLI
prefixes relative destinations with its working directory without cleaning
symlink-sensitive `..` components. `CheckoutService.CreateAt` binds and validates
the destination through the server's content adapter before materialization.
Only authenticated local operators can request host-file creation, not photo users.
Header identity mode does not start this interface.

The command returns pending, committed, and conflict counts plus an optional
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
removes discovery, stops accepting work, and joins handlers before storage closes.

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
