# Automate Fotobank

Scripts and agents should make storage and identity choices explicit. Set one
configuration path for the whole operation instead of relying on the current
working directory:

```sh
export FOTOBANK_CONFIG=/etc/fotobank/config.toml
fotobank config validate
```

An individual command can use `--config` instead. The explicit flag takes
precedence over the environment variable.

## Prefer structured output

Use `--json` where the command provides it, including:

```sh
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
fotobank owners list --json
```

Do not parse human progress output when a JSON form exists. Commands return zero
on success, one for runtime failures, and two for invalid command usage.

`config diagnose` currently produces human-readable output only. Import writes
its final JSON result to stdout and live progress to stderr; interrupted
connections and partial failures exit nonzero.
Checkout list and status ask the daemon for saved catalog observations, not a
fresh filesystem scan. They do not open or initialize a database in the CLI.

## Know which process owns the vault

The running server holds the embedded Docbank vault open exclusively. Commands
that open that same vault cannot run alongside it. For the configured deployment:

| Operation | Server state |
| --- | --- |
| Content recovery | Uses the daemon and starts it if needed; checks interrupted imports across all owners. |
| GPS backfill | Uses the daemon and starts it if needed; header mode requires `--owner` or `--all-owners`. |
| Owner registration, listing, or removal | Uses the daemon's local operator API and starts it if needed; supports stub and header mode. |
| Import, album and sharing commands, every checkout command, or manual backup create | Uses the daemon in stub mode and starts it if needed; run under the same OS account and configuration. |
| Browse or use the HTTP API, scan checkout edits, scheduled backups | Keep the server running. |
| Backup init, list with `--repo`, verify, or restore to a separate target | Do not need the source vault open. |

Use `fotobank daemon stop` and `fotobank daemon start` for background operation.
For a supervised service, use its supervisor; for a foreground `fotobank serve`,
interrupt it and wait for shutdown to complete. Do not remove
lock files or start a second vault owner to work around this limitation. The
CLI submits imports, interrupted-import recovery, GPS backfill, album and sharing
commands, owner management, every checkout command, and manual backup creation to the server.
Privacy/admin, thumbnails, and AI commands still use
direct catalog connections. These are remaining migration gaps, not alternate
ways to access a daemon-owned deployment.

For tracked edits: keep the server running to create the checkout, edit and
settle files, inspect `checkout status --json`, then explicitly
commit with the server still running. See
[checkouts](checkouts.md) for the complete workflow.

## Organize albums

Album commands use the configured stub owner and start the daemon if needed.
Run them under the same OS account and configuration as the server:

```sh
fotobank albums create "Autumn walk"
fotobank albums add <album-uuid> <media-uuid> [<media-uuid>...]
fotobank albums list --json --limit 100 --offset 0
fotobank albums show <album-uuid> --sort-by taken --sort-asc
fotobank albums rename <album-uuid> "Autumn walks"
fotobank albums remove <album-uuid> <media-uuid>
fotobank albums delete <album-uuid>
```

`list --json` returns an object with `items` and, when more albums remain,
`next_offset`. Pass that offset to the next request. List and member pages
default to 100 rows and are capped at 1,000. `show` accepts `--limit`, `--offset`,
and sorting by `added`, `imported`, or `taken`.

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

## Manage registered owners

An owner identifies whose photos and catalog records Fotobank manages. These
commands are host administration, not a photo-user login. Run them under the
daemon's OS account with the same configuration, in either stub or header mode:

```sh
fotobank owners add --hub example --user-id user-a --handle "User A"
fotobank owners list --json
fotobank owners remove --hub example --user-id user-a
```

All three commands accept `--config`. Adding an owner generates a storage UUID
unless you supply `--storage-key`. Repeating an add without a key keeps the
existing key; specifying a different key for an existing owner returns a
conflict. A non-empty `--handle` updates the display name. Listing returns
`items` in JSON, with each owner's hub, user ID, storage key, handle, and creation
time. The configured stub owner is registered automatically when the daemon
starts; you do not need to add it first.
To choose its storage UUID, set `identity.stub.storage_key` before the first start.

New registrations are usable without a restart. A storage UUID already assigned
to another owner returns a conflict.

Removal unregisters an owner; it does not delete photos or working files. Owners
referenced by assets, saved checkouts, albums, or shares cannot be removed, and
`--purge` is not implemented. The active configured stub owner cannot be removed:
change the identity configuration and restart first. After a lost response, list
owners before repeating a change.

## Keep authority changes explicit

- Validate configuration before imports or checkout writeback.
- Confirm the source path printed at import startup and use `config diagnose`
  to inspect storage locations.
- Run only one import or content-recovery operation at a time; the application
  lock rejects concurrent mutation.
- Estimate a checkout before creating it and set `--max-bytes` for `--all`.
- Treat `checkout commit` as an authority-changing action.
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
`/api/openapi.json` on the running server or the repository's `openapi.json`.
Interactive documentation is at `/api/docs`. A documented operation can still
report that its service is unavailable; schema presence does not mean AI is
enabled. Raw byte and event routes are outside that JSON contract. Consult the
[HTTP architecture](../architecture/runtime.md#http) and route implementations
for those surfaces. Scripts should use supported
commands and API operations rather than writing directly to the catalog or
Docbank's storage.
