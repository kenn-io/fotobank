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
fotobank backup list --repo /backups/photos --json
fotobank backup verify --repo /backups/photos --all --json
fotobank backup restore --repo /backups/photos --target /recovery/photos --json
fotobank checkout list --json
fotobank checkout status <checkout-uuid> --json
```

Do not parse human progress output when a JSON form exists. Commands return zero
on success, one for runtime failures, and two for invalid command usage.

`config diagnose`, `import`, and checkout `estimate`, `create`, and `commit`
currently produce human-readable output only. Checkout list and status report
saved catalog observations, not a fresh filesystem scan. Their startup still
opens the normal database and ensures the configured owner, so they are not
strictly read-only database probes. Use them against an initialized deployment.

## Know which process owns the vault

The running server holds the embedded Docbank vault open exclusively. Commands
that open that same vault cannot run alongside it. For the configured deployment:

| Operation | Server state |
| --- | --- |
| Import, content recovery, checkout create or commit, manual backup create | Stop the server first; restart after the command finishes. |
| Browse or use the HTTP API, scan checkout edits, scheduled backups | Keep the server running. |
| Checkout estimate, list, or status | May run alongside the server; these do not open Docbank. |
| Backup init, list with `--repo`, verify, or restore to a separate target | Do not need the source vault open. |

Stop the service through the supervisor you use to run it, or interrupt a
foreground `fotobank serve`, and wait for shutdown to complete. Do not remove
lock files or start a second vault owner to work around this limitation. The
CLI does not yet submit import or checkout writes to the running server.

For tracked edits: create the checkout with the server stopped, run the server
while editing and settling, inspect `checkout status --json`, then stop the
server and explicitly commit. Restart it for browsing and further scans. See
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
`fotobank backup create --repo /backups/photos --json` requires the server to
be stopped so the command can own the vault. Manual archives remain outside
scheduled retention; `fotobank:scheduled` is reserved for the server.

The HTTP API and CLI share application services, but not every command has a
machine-readable form yet. An agent should fail on unexpected output rather
than guessing from partially parsed text.

The checked-in OpenAPI schema is also incomplete: search, AI, and facets are
omitted by the generator's dependency-free setup. Raw byte and event routes
are outside that JSON contract. Consult the [HTTP architecture](../architecture/runtime.md#http)
and route implementations for those surfaces. Scripts should use supported
commands and API operations rather than writing directly to the catalog or
Docbank's storage.
