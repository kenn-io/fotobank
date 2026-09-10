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

The flash, Docbank, and NAS roots become canonical absolute paths during
validation. Invalid enum values, unsafe overlaps, incomplete identity
boundaries, non-loopback admin listeners, and impossible retention settings
fail before server startup.

`fotobank config diagnose` is the read-only operational check. It reports the
effective file, environment, and default configuration together with the
availability of SQLite, the Docbank catalog and blob directory, NAS artifacts,
checkout boundary configuration, identity mode, and the initialized backup
repository when scheduling is enabled. It never initializes or migrates SQLite,
opens Docbank through its mutating vault
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

`internal/backup` coordinates complete recovery archives containing the Fotobank
catalog and authoritative Docbank content. Repository storage, manifests,
locking, verification, forgetting recovery points, and pruning belong to
Docbank and are exposed through `internal/content`. Fotobank owns the capture
schedule and which recovery points scheduled retention selects.

`backup init`, `backup create`, `backup list`, `backup verify`, and
`backup restore` require an explicit `--repo`. Creation opens an initialized
repository; only `init` creates one. Repository listing and verification
run through the authenticated operator API and require configuration, but not
the original vault when the daemon runs in explicit recovery mode. Restore additionally
requires `--target` and uses a separate empty directory.

`internal/backup.CreateArchive` snapshots Fotobank SQLite into private temporary
storage during Docbank's mutation freeze and declares it as
`application/catalog.sqlite` in the same manifest. Preparation first checks
SQLite integrity and the `schema_migrations` marker using `ValidateSnapshot`;
empty or unrelated databases are rejected before snapshot creation. SQLite uses
a separate connection without running Fotobank migrations and captures live
WAL state with `VACUUM INTO`. The temporary snapshot remains
until archive creation returns, then is removed. Content already referenced by
that catalog exists before Docbank pins its state; later content appends do not
invalidate the recovery point. Manual `backup create` calls
`BackupService.Create` through the running server's authenticated local operator
interface. The service checks the configured stub principal and captures the
whole deployment using the server's existing vault and catalog path; the CLI
opens neither SQLite nor Docbank. This is a trusted host-operator capability,
not an owner-scoped photo API. The scheduled worker calls the same archive
operation independently. Both paths retain the existing repository locking and
short mutation freeze, so capture runs while the server remains available.

Scheduling is opt-in through `[backup].enabled`, which defaults to false. When
enabled, `backup.repository` must explicitly name an initialized repository
with an absolute path; home expansion is supported. `backup.interval` defaults
to 24 hours and `backup.keep_last` to 30; both must be positive. Configuration
validation checks values without initializing or opening the repository.
Runtime readiness and diagnostics inspect the configured repository rather
than creating a destination directory.

`internal/backup.Worker` runs serially and reads the latest persisted
`fotobank:scheduled` timestamp to decide when capture is due. No scheduled point
means capture is due immediately. Restarts preserve the schedule; a failed
attempt retries after `min(interval, 5 minutes)`. A stored timestamp in the
future makes capture due immediately, so clock rollback cannot postpone it
indefinitely. The server owns the worker's context and waits for it to exit
before closing the vault or catalog.

`internal/backup/scheduled_retention.go` selects only recovery points tagged
`fotobank:scheduled`. The tag is reserved and rejected by manual
`backup create --tag`. After a successful capture, the worker retains the point
just created and the newest remaining scheduled points up to `keep_last`.
Manual archives are never selected for forgetting. The worker calls
`BackupRepository.Forget` and then `BackupRepository.Prune`; it does not delete
repository files itself. Docbank coordinates those operations and preserves
content referenced by retained recovery points.

Capture failure does not run cleanup. Cleanup failure preserves the newly
created archive, logs a warning, and records a separate retention failure
metric. The next successful capture retries cleanup. Archive-success metrics
therefore describe successful publication independently of retention outcomes.
Pruning removes unused packs and rewrites sparse packs with less than 50% live
indexed bytes. Packs at or above that threshold can retain unused bytes;
pruning does not rewrite every partially used pack.

Complete archives include all owners and hidden media. They do not include
configuration files, provider credentials, disposable artifacts, or checkout
files that have not been committed. Repository verification checks the stored
catalog bytes, not Fotobank relationship semantics. The archive integration
test restores both databases and resolves a catalog file through
`contentresolver`, checking node, version, digest, size, and original bytes.

`backup restore [snapshot-id] --repo ... --target ...` uses
the recovery-only `ArchiveRestoreService`, which calls
`internal/backup.RestoreArchive` and the repository-only content restore API.
The CLI requires an already-running recovery daemon and sends the repository,
snapshot ID, and target through the documented Huma operation. The source database
selection is captured from the daemon's configuration and environment at startup,
not from the requesting client's environment.
It never opens or creates the original vault or catalog. Configuration loading
permits absent source storage but retains the configured aliases and validated
roots, plus the database directory (including `FOTOBANK_DB_PATH`) and configured
backup repository, as protected roots for Docbank's target validation.
The target must be separate and empty; the CLI does not expose overwrite.
`config.ArchiveRestorePaths` permits dangling source database symlinks, including
parent-directory links, without opening or recreating them. It checks configured
aliases lexically and supplies resolved destinations to Docbank's filesystem
checks. Ordinary database opening and lifetime-lock resolution remain strict.

After Docbank restores and verifies its snapshot and host files, Fotobank
validates the captured SQLite catalog without migrations. It resolves current
media-file mappings, applied import receipts, and retained checkout base
versions against the restored vault, checking virtual paths against their
recorded nodes where retained and comparing version ownership,
digests, and sizes and reading each referenced version through verification.
The catalog need not name the latest Docbank head: later appends can be included
in the same snapshot. A failed check leaves the isolated target for diagnosis
and returns an error rather than reporting a usable recovery. Success reports
the vault and catalog paths; it does not change the deployment configuration.

Checkout records retain their original working paths. Recovery does not restore
working files, relocate checkout roots, or activate the recovered deployment.
Operators must review those paths before running its checkout scanner. Archive
verification establishes byte integrity, and restore adds the named reference
checks; neither claims whole-application metadata validation.

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

`fotobank content recover` asks the daemon to check every registered owner,
using its existing vault and the same lock as imports. The CLI starts a missing
daemon through the shared lifecycle path. When a pending path
already contains the reserved identity, it adopts the Docbank node and version
and finishes the asset. Different authority at the path terminalizes the asset
as a conflict. A missing path remains pending because only the original import
source can supply those bytes; re-running that import retries the stable create.
The command also walks each owner's Docbank media subtree through the bounded
embedded traversal API and reports files with no operation-ledger row. It never
deletes, moves, or overwrites unmatched authority.

Garbage collection of live authoritative content is an explicit maintenance
action. Rebuildable caches may be evicted automatically. Archive retention is
separately authorized by enabling the backup schedule: it removes only expired
scheduled recovery points and unused repository storage after a new archive
succeeds. Ordinary reads do not prune either live content or archive storage.

`fotobank checkout estimate` and `checkout create` use the running server's
local operator interface. The server resolves selections for its configured
owner, validates destination storage boundaries, and holds the creation lock
through materialization. Their CLI opens neither Docbank nor SQLite. Creation
results preserve the reserved checkout ID and completed-file count on failure;
partial files remain for inspection. Connection loss requires checking the
saved checkout list/status before retrying, not assuming creation did nothing.

`fotobank checkout commit <checkout-id>` is an explicit writeback operation for
settled tracked edits. It does not import untracked files, apply working-file
deletions, infer renames, or resolve conflicts. It uses the running server's authenticated local
operator interface (`internal/operator`). `--json` preserves result counts and
errors, including partial completion. The CLI never opens a second vault for
commits. Operators inspect the durable
state first with `fotobank checkout list` and `fotobank checkout status
<checkout-id>`; these daemon queries do not scan or mutate the working
copy. The status view shows the saved selection, entry-state totals, and every
file that is pending, conflicted, missing, or errored.
Those states remain visible in the checkout ledger for their dedicated
lifecycle operations. A primary edit
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
