# Automate Fotobank

Scripts and agents work with the same library as the web app. In the default
single-user setup, no owner-registration or login command is needed. See
[Set up Fotobank](setup.md#create-the-configuration) for that setup.

Set one configuration path for the whole operation instead of relying on the
current working directory:

```sh
export FOTOBANK_CONFIG=/var/lib/fotobank-control/config.toml
fotobank config validate
```

An individual command can use `--config` instead. The explicit flag takes
precedence over the environment variable.

## Prefer structured output

Use `--json` where the command provides it, including:

```sh
fotobank config diagnose --json
fotobank import /media/card-or-export --json
fotobank content recover --json
fotobank backup create --repo /backups/photos --json
fotobank backup list --repo /backups/photos --json
fotobank backup verify --repo /backups/photos --all --json
fotobank backup restore --repo /backups/photos --target /recovery/photos --json
fotobank checkout list --json
fotobank checkout status <checkout-uuid> --json
fotobank checkout estimate --year 2025 --json
fotobank checkout create /work/photos-2025 --year 2025 --json
fotobank checkout commit <checkout-uuid> --json
fotobank daemon status --json
fotobank albums list --json
fotobank shares list --json
```

Do not parse human progress output when a JSON form exists. Commands return zero
on success, one for runtime failures, and two for invalid command usage.

`config diagnose --json` writes an array of checks to stdout. Each check has
`name`, `status`, and `detail`, plus `action` when there is a suggested next step.
Statuses are `ok`, `error`, or `disabled`; disabled backups do not count as an
error. Match checks by name rather than array position, and use the exit code:
the command still emits its results when a check fails, then exits with one.
A configuration that cannot be loaded or validated produces a single
`configuration` error check. The failure summary goes to stderr.

Diagnostics inspect local configuration and storage without starting a daemon
or initializing missing storage. They do not prove full catalog integrity or
that a configured mount is healthy.

Import writes its final JSON result to stdout and live progress to stderr; interrupted
connections and partial failures exit nonzero.
Checkout list and status ask the daemon for saved catalog observations, not a
fresh filesystem scan. They do not open or initialize a database in the CLI.

## Know which process owns the vault

The daemon is Fotobank's background server. It owns the catalog, the Docbank
vault, and application operations. Commands send requests to it. Some commands
start it automatically; recovery requires an explicit choice:

| Operation | Server state |
| --- | --- |
| Content recovery | Uses the daemon and starts it if needed; checks interrupted imports across all owners. |
| GPS backfill | Uses the daemon and starts it if needed; header mode requires `--owner` or `--all-owners`. |
| Thumbnail regeneration | Uses the daemon and starts it if needed; header mode requires `--owner` or `--all-owners`. |
| Owner registration, listing, or removal | Uses the daemon's local operator API and starts it if needed; supports stub and header mode. |
| Hidden passcode setup, change, or disable | Uses the daemon in stub mode; reads and validates passcodes and confirmation before starting it. |
| AI status or processing acknowledgment | Uses the daemon in stub mode and starts it if needed; acknowledgment requires `--hidden-processing`. |
| AI backfill or retry-failed | Uses the daemon in stub mode for tag, caption, and embed tasks; requires recorded processing acknowledgment. |
| Embedding-generation listing, promotion, or compaction | Uses the daemon's local operator API in stub mode. Promotion asks for confirmation unless `--yes`; compaction supports `--dry-run`. |
| Admin hidden-passcode reset | Uses the daemon's local operator API; requires `--confirm` and an explicit `--owner` in header mode. |
| Import, album and sharing commands, every checkout command, or manual backup create | Uses the daemon in stub mode and starts it if needed; run under the same OS account and configuration. |
| Browse or use the HTTP API, scan checkout edits, scheduled backups | Keep the server running. |
| Backup init, list with `--repo`, or verify | Requires an already-running daemon in normal or recovery mode; never auto-starts. Requires configuration but no source storage in recovery mode. |
| Restore an archive to a separate target | Requires an already-running recovery daemon; never auto-starts or changes modes. Requires saved configuration but no source storage. |

Use `fotobank daemon stop` and `fotobank daemon start` for background operation.
For a supervised service, use its supervisor; for a foreground `fotobank serve`,
interrupt it and wait for shutdown to complete. Do not remove lock files or
start a second process that opens the same vault. See the
[backup guide](backup.md#restore-a-complete-archive) for recovery mode.

For tracked edits: keep the server running to create the checkout, edit and
settle files, inspect `checkout status --json`, then explicitly
commit with the server still running. See
[checkouts](checkouts.md) for the complete workflow.

## Regenerate thumbnails

Use `fotobank thumbs regenerate --all --json` to queue new thumbnails for the
configured stub owner. The daemon stays responsible for the catalog and workers;
the command reports queued work, not finished images. Originals remain unchanged.

Select assets with repeatable `--id`, `--type photo|video`, `--status`, or
`--since 2026-01-01T00:00:00Z`. Filters combine, including alongside `--all`.
Only ready, non-hidden assets are eligible. Host operators may use `--owner hub:user`
or `--all-owners`; header-mode deployments require one of these explicit scopes.
These permissions belong to the local operator, not ordinary photo users.

The shared HTTP operation is `POST /api/v1/operator/thumbs/regenerate`.
JSON returns `items` with `hub`, `user_id`, and `enqueued` for each processed owner.
An `error` means the run stopped after any reported successes, and the CLI exits
nonzero. Regeneration increments thumbnail versions: inspect media thumbnail
status before retrying an interrupted request instead of blindly repeating it.

## Find and inspect photos

Find photo IDs before adding them to albums or selecting a checkout:

```sh
fotobank media list --type photo --limit 20 --json
fotobank media list --camera "Example Camera" --lens "Wide" --has-gps --json
fotobank media search "sunset" --type photo --limit 20 --json
fotobank media show <media-uuid> --json
fotobank albums add <album-uuid> <media-uuid> --json
```

`media list --json` returns `items` and an optional `next_offset`. Continue with
`--offset` and the same filters and sorting. Pages default to 100 items and
accept 1–1,000. Sorting is by capture timestamp, oldest first; `--sort-desc`
reverses it. Pages are live reads, not a snapshot across concurrent imports.

`--type` selects `photo` or `video`. Camera and lens values match exactly;
repeat `--camera`, `--lens`, or `--tag` to match any value within that filter.
Different filters combine. `--tag` takes a tag key, not a free-text search.
Use `--has-gps` for geotagged photos or `--has-gps=false` for those without GPS.

`media search [query]` uses the same search as the web interface. Quote queries
containing spaces. Omit the query to browse by filters. Metadata search works
without AI; when embeddings are available, text queries also use semantic search.
Search accepts the type, camera, lens, tag-key, and GPS filters above. Add
`--tag-label` to require a tag label; repeated labels must all match. Use
`--location` for an exact location label and RFC3339 timestamps with
`--date-after` (inclusive) or `--date-before` (exclusive).

Search JSON returns `results`, `has_more`, and an optional `next_cursor`:

```sh
fotobank media search "sunset" --type photo --limit 20 --cursor <next-cursor> --json
fotobank media search --date-after 2026-01-01T00:00:00Z --sort newest --json
```

Keep the query, filters, and sort unchanged when continuing. Pages default to
60 results and accept 1–200. Sorting supports `relevance`, `newest`, and `oldest`;
queries without text default to newest. Search pages are live reads, and text
search covers a bounded candidate set, not an exhaustive catalog export.
If the daemon rejects a cursor with HTTP 400, restart without `--cursor`.
The CLI does not retry automatically. `effective_sort`, `semantic_unavailable`,
and `semantic_unavailable_reason` describe how the returned page was searched.
See [search architecture](../architecture/search-and-ai.md#search-indexes)
for the candidate limit and cursor rules.

`media show --json` returns the photo's metadata and primary filename, size,
and checksum. `files` contains attached files with their IDs, roles, sizes,
and checksums; an ordinary single-file photo has an empty `files` array.
Without `--json`, these commands print a readable summary. Failed requests
leave stdout empty and exit nonzero.

These commands use the existing photo HTTP endpoints and start the daemon if
needed. They use the configured stub owner and do not unlock hidden media or
read other owners' photos.

## Download an original or attachment

Save the primary file, or select an attachment ID from `media show`:

```sh
fotobank media download <photo-id> --output photo.jpg
fotobank media download <photo-id> --file <file-id> --output photo.xmp --json
```

`--output` is required. Relative paths are relative to the CLI's working
directory, not the daemon's. The parent directory must already exist and support
hardlinks. An existing file, directory, or symlink is never replaced. Binary
stdout, bulk downloads, resume, and overwrite are not supported.

The CLI checks the destination before starting the daemon. It downloads to a
temporary file in that directory, verifies the size and SHA-256 from the photo's
metadata, then publishes the completed file. The hardlink is between two names
of this new local copy, never a link to Docbank storage. The final name appears
only after verification. Ctrl-C cancels the request. Failures before publication
remove the temporary file during normal cleanup. An error during later cleanup
or receipt output can leave the completed file; check the destination before
retrying. Force-killing the process can leave a `.fotobank-download-*.tmp` file;
remove it after confirming the command is no longer running.

JSON success contains `media_id`, optional `file_id`, the absolute `output` path,
`size`, and `sha256`. A failed request or verification writes no success receipt
and exits nonzero. If the stored content changes between reading its metadata
and downloading it, verification can fail; retry the command. The CLI does not
retry automatically or unlock hidden media. A downloaded file is an ordinary
copy: editing it does not update Fotobank. Use a [checkout](checkouts.md) when
you want to commit edits as new versions.

## Organize albums

Album commands use the configured stub owner and start the daemon if needed.
Run them under the same OS account and configuration as the server:

```sh
fotobank albums create "Autumn walk" --json
fotobank albums add <album-uuid> <media-uuid> [<media-uuid>...] --json
fotobank albums list --json --limit 100 --offset 0
fotobank albums show <album-uuid> --sort-by taken --sort-asc --json
fotobank albums rename <album-uuid> "Autumn walks" --json
fotobank albums remove <album-uuid> <media-uuid>
fotobank albums delete <album-uuid>
```

`list --json` returns an object with `items` and, when more albums remain,
`next_offset`. Pass that offset to the next request. List and member pages
default to 100 rows and are capped at 1,000. `show` accepts `--limit`, `--offset`,
and sorting by `added`, `imported`, or `taken`.

`create --json` and `rename --json` return the album object, including its `id`.
`add --json` returns `added` and `already_present` counts, so adding the same
photos again has an inspectable result. `show --json` returns `album` details
and a `media` page with `items` and optional `next_offset`. Continue that page
with `--offset` while keeping the same sort flags. The details and member page
are separate daemon reads, not an atomic snapshot of a concurrently edited album.

These JSON commands write a result only after their requests succeed. On a
request failure, stdout is empty, stderr explains the failure, and the exit code
is nonzero. `remove` and `delete` still print human confirmations; neither API
operation returns a result body. Use `fotobank albums --help` for examples and
each subcommand's `--help` for its flags.

Adding existing members is harmless. Removing a member or deleting an album
does not delete the photos; outstanding shares can block album deletion.
Hidden photos cannot be added through these commands, and hidden members are
excluded from the displayed member list. The CLI does not provide an unlock
session or a header-mode user login. Other owners' albums remain inaccessible.
After a connection failure during a change, inspect the album before retrying.

## Manage sharing

Sharing commands also use the configured stub owner through the daemon:

```sh
fotobank shares create --album <album-uuid> --grantee hub:user
fotobank shares create --media <media-uuid>,<media-uuid> --grantee hub:user --allow-download
fotobank shares list --json --grantee hub:user --limit 100
fotobank shares show <share-uuid>
fotobank shares revoke <share-uuid>
fotobank shares list --status failed --json
fotobank shares retry <share-uuid>
```

Choose exactly one of `--album` or `--media`. Creation also accepts `--label`
and an RFC3339 `--expires` time. Create, show, revoke, and retry return the same
JSON objects as the HTTP API. `list --json` returns `items` and an optional
`next_offset`; use `--offset` to continue. Lists default to 100 rows, cap at
500, and support album, grantee, and broker-status filters. Use
`--include-settled` to include shares whose remote revocation is complete.

Creation and revocation queue work for the daemon's sharing worker. A successful
command does not mean that broker work has finished; inspect `broker_status`
with `show` or `list`. Retry applies only to failed shares. Retrying another
state or revoking an already-revoked share returns a conflict and exits nonzero.
Commands do not retry automatically after a lost response; inspect the share
before repeating a change. These commands do not provide header-mode login.

## Queue AI work and manage search generations

These commands use the daemon in stub mode. Backfill queues work that is
missing for the current AI settings; retry-failed queues recorded failures.
Both require processing acknowledgment. Their counts describe queued jobs,
not completed tagging, captions, or embeddings.

```sh
fotobank ai backfill --task tag,caption --json
fotobank ai retry-failed --task embed --json
fotobank ai list-generations --state retired
fotobank ai promote-generation <generation-id> --yes --json
fotobank ai compact-retired-generations --dry-run --json
fotobank ai compact-retired-generations --json
```

Backfill and retry JSON contains the daemon's `enqueued` count for each
completed task, for example `{"tag":{"enqueued":12},"caption":{"enqueued":8}}`.
Tasks run in the requested order and stop at the first failure. If a later
task fails, stdout still contains completed task results; stderr explains the
failure and the exit code is nonzero. Failed and unattempted tasks have no
entry. If no task completed, stdout is empty. A failed task may have queued
some jobs before failing; these responses cannot report that partial count.
Inspect `ai status` before retrying after a request failure.

A search **generation** holds embeddings made with the same model and input
settings. Promotion makes a retired generation active again; JSON returns its
`id` and `fingerprint`. Without `--yes`, JSON mode puts the confirmation prompt
on stderr. Declining leaves stdout empty and exits nonzero.

Compaction removes retired generations older than the configured retention
window. Its JSON contains `candidates`, `dropped`, and an optional `error`.
Dry-run lists candidates without deleting them. A failed sweep still reports
how many generations it dropped, then exits nonzero; earlier deletions are not
undone. A failed dry-run also returns the error as JSON. A failed HTTP request
leaves stdout empty. Promotion and compaction do not require a working AI
provider or processing acknowledgment.

Without `--json`, these four commands retain their readable output.
`ai status` and `ai list-generations` always return JSON. See the
[AI architecture](../architecture/search-and-ai.md#embedding-generations)
for generation ownership and processing rules.

## Manage registered owners

This is advanced administration. The single-user setup registers your configured
identity automatically; ordinary photo workflows do not need these commands.

An **owner** records whose photos, albums, and other catalog records these are.
It is not a copyright claim or a person recognized in a photo. Fotobank identifies
an owner with `hub` (the identity provider or namespace) and `user_id` (the user
within it). `handle` is a display name. `storage_key` is a stable storage UUID,
not a password or access token.

Registration creates that catalog record. It does not create a login, invite
someone, or change the identity selected by the running daemon. In `stub` mode,
configuration selects one identity for every request. In `header` mode, an
external proxy supplies the authenticated identity. Owners share one embedded
Docbank vault; Fotobank controls photo access, not separate Docbank accounts.

These commands belong to the host operator: the person administering the daemon
and storage, rather than an ordinary photo user. Run them under the daemon's OS
account with the same configuration, in either stub or header mode:

```sh
fotobank owners add --hub example --user-id user-a --handle "User A" --json
fotobank owners list --json
fotobank owners remove --hub example --user-id user-a
```

All three commands accept `--config`. Adding an owner generates a storage UUID
unless you supply `--storage-key`. Repeating an add without a key keeps the
existing key; specifying a different key for an existing owner returns a
conflict. A non-empty `--handle` updates the display name.

`add --json` returns the registered owner with `hub`, `user_id`, `storage_key`,
`handle`, and `created_at`. Repeating registration returns the existing storage
key and creation time, with any requested display-name update. `list --json`
returns these same records under `items`. Failed requests leave stdout empty,
explain the failure on stderr, and exit nonzero. After a lost response, list
owners before retrying; the registration may have succeeded.

The configured stub owner is registered automatically when the daemon starts;
you do not need to add it first.
To choose its storage UUID, set `identity.stub.storage_key` before the first start.

New registrations are usable without a restart. A storage UUID already assigned
to another owner returns a conflict.

Removal unregisters an owner; it does not delete photos or working files. Owners
referenced by assets, saved checkouts, albums, or shares cannot be removed, and
`--purge` is not implemented. The active configured stub owner cannot be removed:
change the identity configuration and restart first. After a lost response, list
owners before repeating a change.

## Before a command changes stored data

- Validate configuration before imports or checkout writeback.
- Confirm the source path printed at import startup and use `config diagnose`
  to inspect storage locations.
- Run only one import or content-recovery operation at a time; the application
  lock rejects concurrent mutation.
- Estimate a checkout before creating it and set `--max-bytes` for `--all`.
- Review pending edits before `checkout commit`; it saves new stored versions.
- Verify the archive before a recovery drill and restore into a separate empty
  directory. Starting the recovered deployment is a separate operator action.

For routine backups, initialize a repository and enable the server's
[archive schedule](backup.md#enable-scheduled-archives). Manual
`fotobank backup create --repo /backups/photos --json` uses the running server's
vault and captures all owners, including hidden media. After a lost connection,
list and verify the repository before retrying. Manual archives remain outside
scheduled retention; `fotobank:scheduled` is reserved for the server.

The HTTP API and CLI share application services, but not every command has a
machine-readable form yet. An agent should fail on unexpected output rather
than guessing from partially parsed text.

Discover JSON operations, including checkout and backup commands, search, AI,
and filter counts, through
`/api/openapi.json` on the running server or the repository's `openapi.yaml`.
Interactive documentation is at `/api/docs`. A documented operation can still
report that its service is unavailable; schema presence does not mean AI is
enabled. Raw byte and event routes are outside that JSON contract. Consult the
[HTTP architecture](../architecture/runtime.md#http) and route implementations
for those surfaces. Scripts should use supported
commands and API operations rather than writing directly to the catalog or
Docbank's storage.
