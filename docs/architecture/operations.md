# Operations

## Configuration

Fotobank loads TOML from an explicit `--config`, `FOTOBANK_CONFIG`, the XDG
config directory, the user config directory, or `./config.toml`, in that order.
`fotobank config init` writes the embedded canonical example to the selected
path without replacing an existing file. The same example remains in
`internal/config/config.example.toml` for source readers.

Configuration covers local state, Docbank, NAS artifacts, identity, HTTP,
imports, thumbnails, broker, backup, observability, admins, AI, search, and UI
feature flags. Defaults and the narrow documented environment overrides are
applied before validation. Database-backed application overrides are merged
before validating the effective runtime view.

All configured roots are canonical absolute paths after load. Invalid enum
values, unsafe overlaps, incomplete identity boundaries, non-loopback admin
listeners, and impossible retention settings fail before server startup.

`fotobank config diagnose` is the read-only operational check. It reports the
effective file, environment, and default configuration together with the
availability of SQLite, the Docbank catalog and blob directory, NAS artifacts,
checkout boundary configuration, identity mode, and the backup destination. It
never initializes or migrates SQLite, opens Docbank through its mutating vault
lifecycle, creates directories, or tests storage by writing a file. A healthy
result therefore establishes readable structure and valid boundaries, not a
full database integrity check or proof that the service account can write.

## Startup and shutdown

`fotobank serve` opens the SQLite database and migrations, registers the owner,
opens the embedded Docbank adapter, builds repositories and services, starts
workers, then starts the HTTP listeners.

Readiness reports whether required local dependencies and worker initialization
are usable. Liveness is a simple process endpoint and does not turn transient
external AI failure into process failure.

Shutdown stops HTTP intake, waits for in-flight handlers, cancels workers,
waits for bounded worker completion, closes the Docbank adapter, and finally
closes database resources. A worker must not outlive a collaborator it uses.

## Backup and restore

`internal/backup` creates timestamped SQLite snapshots, lists them, applies
tiered retention, and restores through an explicit command. Snapshot names use
a filesystem-portable UTC format and the reader accepts the formats that may
still be inside the configured retention window.

SQLite backup uses SQLite's online backup behavior rather than copying a live
database file without its WAL state. For the default NAS destination, SQLite
first writes a private local staging snapshot because `VACUUM INTO` requires a
pathname. Fotobank then copies, syncs, and atomically publishes that snapshot
through one retained NAS root; stat and retention operations use the same root.
This requires temporary local space equal to the metadata snapshot but prevents
mount disappearance or replacement from redirecting backup writes. Retention
never treats an unparseable file as a valid managed snapshot. Readiness opens
the NAS root and creates and probes the relative snapshot directory on every
check, so it recovers as soon as a missing mount returns without waiting for a
backup tick. Snapshot-copy cancellation closes both transfer handles;
completion still depends on the operating system returning from any filesystem
call already in progress.

Every CLI database user canonicalizes the SQLite path through existing
symlinks—or through the deepest existing ancestor for a new database—before
opening it or deriving process-lock paths. Database users acquire one shared
lifetime lock before opening SQLite and retain it until their pools close.
Restore takes that same lock exclusively, so it refuses to replace the database
while the server, an import, or another command is using it, including when
configuration names the database through an alias.

`backup init`, `backup create`, `backup list --repo`, and `backup verify`
manage complete recovery archives through `internal/content`. They require an
explicit repository path; creation opens an initialized repository rather than
silently creating a missing destination. Repository listing and verification
require neither configuration nor the original vault.

`internal/backup.CreateArchive` snapshots Fotobank SQLite into private temporary
storage during Docbank's mutation freeze and declares it as
`application/catalog.sqlite` in the same manifest. Preparation first checks
SQLite integrity and the `schema_migrations` marker using `ValidateSnapshot`;
empty or unrelated databases are rejected before snapshot creation. SQLite uses a separate
connection without running Fotobank migrations. The temporary snapshot remains
until archive creation returns, then is removed. Content already referenced by
that catalog exists before Docbank pins its state; later content appends do not
invalidate the recovery point. The CLI holds the shared database lifetime lock
and owns the embedded vault for the operation, so `backup create` requires the
server to be stopped. A future in-process caller can reuse the same capture
operation with its already-open vault.

Complete archives include all owners and hidden media. They do not include
configuration files, provider credentials, disposable artifacts, or checkout
files that have not been committed. Repository verification checks the stored
catalog bytes, not Fotobank relationship semantics. The archive integration
test restores both databases and resolves a catalog file through
`contentresolver`, checking node, version, digest, size, and original bytes.

Scheduled snapshots, their tiered retention, and `backup restore` still apply
only to metadata SQLite files. The complete-archive CLI does not yet expose
restore or retention. The embedded restore operation requires an open source
vault and publishes to a separate target; it is not yet a source-independent
disaster recovery command.

Restore drills verify referenced blob content through bounded embedded
Docbank verification. Whole-catalog metadata validation is a separate Docbank
capability and must not be implied by a content scrub.

## Observability

Fotobank uses structured `slog` logging with configurable text or JSON output,
level, and source locations. Request middleware attaches request and principal
context. Logs prefer opaque IDs and avoid source paths or media payloads.

The optional admin listener is loopback or Unix-socket only. It serves
readiness, Prometheus metrics, and optional pprof endpoints. Metrics cover HTTP,
imports, thumbnail and AI queues, search, shares, backups, and worker outcomes.
Metrics use bounded labels; media IDs, paths, prompts, and unbounded error text
do not become labels.

## Maintenance

The durable content-operation ledger, rather than a scan of NAS paths, is the
source for Docbank recovery and orphan reporting. A pending operation means the
cross-database import has not yet recorded a receipt; a conflict is terminal
until an explicit resolution workflow is invoked. Re-running an import resumes
matching pending identities with their recorded IDs and virtual paths.

`fotobank content recover` checks every registered owner. When a pending path
already contains the reserved identity, it adopts the Docbank node and version
and finishes the asset. Different authority at the path terminalizes the asset
as a conflict. A missing path remains pending because only the original import
source can supply those bytes; re-running that import retries the stable create.
The command also walks each owner's Docbank media subtree through the bounded
embedded traversal API and reports files with no operation-ledger row. It never
deletes, moves, or overwrites unmatched authority.

Garbage collection and destructive pruning are deliberate maintenance actions,
not side effects of ordinary reads or cache eviction. Rebuildable caches may be
evicted automatically; authoritative content may not.

`fotobank checkout commit <checkout-id>` is an explicit writeback operation for
settled tracked edits. It does not import untracked files, apply working-file
deletions, infer renames, or resolve conflicts. Those states remain visible in
the checkout ledger for their dedicated lifecycle operations. A primary edit
is not settled in Fotobank until Docbank source metadata for the committed
version is available; the pending checkout entry is the retry record.

## Tests and CI

Go code uses the `sqlite_fts5` tag, CGO, `mattn/go-sqlite3`, and sqlite-vec.
Tests open fresh migrated temporary databases. Content contract tests use a
real temporary embedded Docbank vault; fakes do not establish storage
behavior.

The normal local tiers are:

- `make test-short` for the fast Go suite;
- `make test` for the full Go suite;
- `make lint` for golangci-lint and the Testify helper analyzer;
- `make nilaway` for nilness analysis;
- `make frontend-check` and `make test-e2e` for the web product; and
- `prek run` for the commit hooks.

GitHub Actions validates Linux amd64 tests, Windows amd64 CGO/SQLite behavior,
lint, and the shared Testify rules. Pull-request dispatch selects approved
runners from the workflow on `main`, while the reusable workflow tests the pull
request commit. Runner admission is infrastructure policy, not an application
security boundary.

Performance workloads such as 10,000-file import and repeated XMP changes are
repeatable benchmarks, not timing assertions in ordinary CI. They report
hashing, queue wait, Docbank write, and projection time separately.
