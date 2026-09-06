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
```

Do not parse human progress output when a JSON form exists. Commands return zero
on success, one for runtime failures, and two for invalid command usage.

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
