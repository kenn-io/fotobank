# Operations

## Configuration

Fotobank loads TOML from an explicit `--config`, `FOTOBANK_CONFIG`, the XDG
config directory, the user config directory, or `./config.toml`, in that order.
The canonical example is `internal/config/config.example.toml`.

Configuration covers flash, Docbank, NAS, storage mode, identity, HTTP,
imports, thumbnails, broker, backup, observability, admins, AI, search, and UI
feature flags. Defaults and the narrow documented environment overrides are
applied before validation. Database-backed application overrides are merged
before validating the effective runtime view.

All configured roots are canonical absolute paths after load. Invalid enum
values, unsafe overlaps, incomplete identity boundaries, non-loopback admin
listeners, and impossible retention settings fail before server startup.

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
database file without its WAL state. Publication uses a temporary file,
durability sync, and rename. Retention never treats an unparseable file as a
valid managed snapshot.

Current backup covers Fotobank metadata. Once Docbank is media authority, a
coordinated backup must guarantee that every Docbank version referenced by the
Fotobank snapshot exists in the published Docbank backup. It can take the short
Fotobank snapshot first, fence destructive Docbank operations, then stream the
append-only content backup without blocking ordinary appends for the full
archive duration.

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

The active NAS authority still has `reconcile` for filesystem/database drift
and `pair` for legacy RAW/JPEG rows. These commands disappear when the asset
and Docbank cutover removes their underlying model. Docbank recovery and orphan
reporting then operate from durable content operations instead of scanning NAS
paths.

Garbage collection and destructive pruning are deliberate maintenance actions,
not side effects of ordinary reads or cache eviction. Rebuildable caches may be
evicted automatically; authoritative content may not.

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
