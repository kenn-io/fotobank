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
```

Do not parse human progress output when a JSON form exists. Commands return zero
on success, one for runtime failures, and two for invalid command usage.

`config diagnose` and `import` currently produce human-readable output only.
Checkout list and status ask the daemon for saved catalog observations, not a
fresh filesystem scan. They do not open or initialize a database in the CLI.

## Know which process owns the vault

The running server holds the embedded Docbank vault open exclusively. Commands
that open that same vault cannot run alongside it. For the configured deployment:

| Operation | Server state |
| --- | --- |
| Import or content recovery | Stop the server first; restart after the command finishes. |
| Every checkout command, or manual backup create | Requires the running server in stub mode, the same OS account, configuration, and application version. |
| Browse or use the HTTP API, scan checkout edits, scheduled backups | Keep the server running. |
| Backup init, list with `--repo`, verify, or restore to a separate target | Do not need the source vault open. |

Stop the service through the supervisor you use to run it, or interrupt a
foreground `fotobank serve`, and wait for shutdown to complete. Do not remove
lock files or start a second vault owner to work around this limitation. The
CLI submits every checkout command and manual backup creation to the server.
Import, content recovery, and GPS backfill still open Docbank separately;
albums, shares, owners, privacy/admin, thumbnails, and AI commands still use
direct catalog connections. These are remaining migration gaps, not alternate
ways to access a daemon-owned deployment.

For tracked edits: keep the server running to create the checkout, edit and
settle files, inspect `checkout status --json`, then explicitly
commit with the server still running. See
[checkouts](checkouts.md) for the complete workflow.

## Keep authority changes explicit

- Validate configuration before imports or checkout writeback.
- Confirm the resolved paths printed at the start of an import.
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
