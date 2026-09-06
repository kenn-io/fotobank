# Back up and restore

Fotobank backs up its SQLite catalog and authoritative Docbank content together
in a complete recovery archive. You can create an archive manually or enable a
schedule in the running server. Scheduling is disabled by default.

## Create a complete archive

Initialize a separate repository once, then stop the server before capturing
the archive. The command needs to own the embedded Docbank vault:

```sh
fotobank backup init --repo /backups/photos
fotobank backup create --repo /backups/photos --tag before-upgrade
fotobank backup list --repo /backups/photos
fotobank backup verify --repo /backups/photos
```

`create` requires an existing repository; it never initializes a missing one.
Use `--config /path/to/fotobank.toml` on `create` to select the deployment.
`init`, `list --repo`, and `verify` work without a Fotobank configuration or
source vault. Each command supports `--json`. Verification selects the latest
recovery point by default; pass its ID or `--all` to select older points.

The archive includes all owners and hidden media. The catalog is captured as
`application/catalog.sqlite` during Docbank's brief metadata freeze and is
covered by the same manifest checks as the content. Configuration files,
provider credentials, disposable thumbnails, and working checkout files are
excluded. Commit checkout edits before taking the archive if you need those
edits captured. Keep the configuration and credentials separately.

Repositories are not encrypted. Store them on protected storage. Manual
archives are retained independently of the schedule. The tag
`fotobank:scheduled` is reserved for the scheduler and cannot be passed to
`backup create --tag`.

## Enable scheduled archives

Initialize a repository with `fotobank backup init --repo /backups/photos`,
then add this configuration and start or restart the server:

```toml
[backup]
enabled = true
repository = "/backups/photos"
interval = "24h"
keep_last = 30
```

`repository` must be an absolute path to an initialized repository; a leading
`~` is expanded. Fotobank does not create a missing repository when the server
starts. `interval` must be a positive duration and defaults to `24h`.
`keep_last` must be positive and defaults to 30. Leaving out `enabled`, or
setting it to `false`, disables scheduling.

The server captures the first archive immediately when the repository has no
scheduled recovery point. Otherwise it schedules from the latest saved
scheduled timestamp, so restarting the server does not restart the waiting
period. Failed attempts retry after the shorter of the configured interval
and five minutes. Scheduled capture uses the server's open vault; you do not
need to stop the server for it.

After successfully creating an archive, Fotobank keeps the newest `keep_last`
scheduled recovery points, including the one just created. It forgets older
points bearing `fotobank:scheduled` and asks Docbank to prune unused archive
storage. Manual archives are untouched. A failed capture never starts cleanup.
If cleanup fails, the new archive remains available, a warning and failure
metric report the problem, and cleanup retries after the next successful
archive. Pruning removes unused packs and rewrites sparse packs whose retained
content occupies less than half their indexed bytes. Fuller packs can keep
unused bytes; pruning does not compact every partially used pack.

Configurations containing `backup.dir`, `backup.keep_15min`,
`backup.keep_hourly`, or `backup.keep_daily` are rejected with migration
instructions. Remove those settings and configure the complete-archive
repository above. Existing metadata-only SQLite backup files are not deleted
automatically; retire them only after verifying a complete recovery.

## Restore a complete archive

Choose a new or empty directory outside the configured storage and backup
repository. Restore needs the saved deployment configuration to identify paths
it must not replace, but the original catalog, vault, NAS, and flash storage
can be gone. The source database path may also be a dangling symlink into lost
storage; recovery does not recreate that location:

```sh
fotobank backup restore --repo /backups/photos \
  --target /recovery/photos --config /saved/fotobank.toml --json
```

The latest recovery point is selected by default. Pass a snapshot ID after
`restore` to select an older point. Both `--repo` and `--target` are required.
Recovery always uses a separate target; it has no overwrite, `--yes`, or
`--dry-run` option. Use `backup verify --repo /backups/photos` for a read-only
archive check.

The result names the restored vault and catalog and reports how many content
references were verified. Restore checks the catalog's current file mappings,
import receipts, and retained checkout base versions against the restored
content, including their recorded checksums and sizes. It does not switch the
running installation to the recovered copy. If validation fails after files
were restored, the command returns an error and leaves that separate directory
for inspection. Use a different empty target for another attempt.

For a recovery drill, keep the restored copy offline. Before starting it, make
a separate configuration with `[docbank].root` set to the reported `vault_root`
and separate NAS and flash roots. Set `FOTOBANK_DB_PATH` to the reported
`catalog_path`. Restore credentials separately. The catalog retains old
checkout paths, but the archive does not contain those working files: review
those paths before starting the server, whose scanner will inspect active
checkouts. This command does not relocate checkouts or automate deployment
cutover.

## What a recovery drill must prove

A useful drill runs complete archive restore into a separate directory and
checks the recovered originals. After reviewing configuration and checkout
paths, it should also start Fotobank against the recovered storage and exercise
the normal user workflows. SQLite integrity alone does not prove that the
media archive is recoverable.

`backup verify` checks stored bytes, including the captured catalog, but does
not validate Fotobank's catalog-to-content relationships. `backup restore
--repo` performs that additional reference check. SQLite structural integrity
and whole-catalog metadata validation are separate checks; restore does not
claim to validate every application relationship.
