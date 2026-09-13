# Back up and restore

Fotobank backs up its photo catalog and stored Docbank files together
in a complete recovery archive. You can create an archive manually or enable a
schedule in the running server. Scheduling is disabled by default.

## Create a complete archive

Initialize a separate repository once, then capture the archive through the
running server. Run the CLI as the server's OS account, using the same
configuration and Fotobank version in stub identity mode:

1. Start the daemon:

   ```sh
   fotobank daemon start
   ```

2. Initialize the repository:

   ```sh
   fotobank backup init --repo /backups/photos
   ```

3. Create an archive:

   ```sh
   fotobank backup create --repo /backups/photos --tag before-upgrade
   ```

4. List the saved recovery points:

   ```sh
   fotobank backup list --repo /backups/photos
   ```

5. Verify the latest recovery point:

   ```sh
   fotobank backup verify --repo /backups/photos
   ```

`create` requires an existing repository; it never initializes a missing one.
Use `--config /path/to/fotobank.toml` on each command to select the deployment.
`backup create` starts a missing daemon in normal mode and never opens a second
vault. If the connection is interrupted, list and verify the repository before retrying:
the server may already have published the recovery point.
`init`, `list --repo`, and `verify` require an already-running daemon. They never
start one or choose its mode. When source storage is unavailable, start recovery
mode using the saved configuration:

```sh
fotobank daemon start --recovery --config /saved/fotobank.toml
fotobank backup list --repo /backups/photos --config /saved/fotobank.toml
fotobank backup verify --repo /backups/photos --all --config /saved/fotobank.toml
```

Use `daemon restart --recovery` instead if the daemon is already running.
Recovery mode opens no photo storage and has no web UI or photo operations.
It uses the same local operator authentication as normal mode. Only one mode
can run for a configuration at a time.
After repairing storage or configuring the recovered copy, use `daemon restart`
with that configuration to return to normal operation. Configuration and the
adjacent daemon discovery directory must live on available local storage.

Each command supports `--json`. Verification selects the latest
recovery point by default; pass its ID or `--all` to select older points.

The archive includes all owners and hidden media. The catalog is captured as
`application/catalog.sqlite` during Docbank's brief metadata freeze and is
covered by the same manifest checks as the content. Catalog settings and stored
authentication hashes, including hidden-media passcode hashes, are preserved.
Configuration files, provider credentials, disposable thumbnails, and working
checkout files are excluded. Commit checkout edits before taking the archive
if you need those edits captured. Keep configuration files and provider
credentials separately.

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
storage; recovery does not recreate that location. Start the recovery daemon
explicitly (use `restart --recovery` if a normal daemon is running):

```sh
fotobank daemon start --recovery --config /saved/fotobank.toml
fotobank backup restore --repo /backups/photos \
  --target /recovery/photos --config /saved/fotobank.toml --json
```

Run both commands as the same OS account. The saved configuration directory must
be writable for daemon runtime state. If the original installation used
`FOTOBANK_DB_PATH`, set that original path when **starting the recovery daemon**,
even if the path no longer exists. The daemon owns the source-path selection;
changing the client's environment does not change it. Restore never starts a
daemon or switches its mode automatically.
If you edit the vault, NAS, flash, or backup repository roots while recovery is
running, restore asks you to check the source paths and restart with `--recovery`
before continuing. It will not silently drop the original path protections.

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
`catalog_path`. Restore configuration files and provider credentials separately.
The catalog retains old checkout paths, but the archive does not contain those
working files: review those paths before starting the server, whose scanner
will inspect active checkouts. This command does not relocate checkouts or
switch the running installation to the recovered copy.

For an isolated drill, do not mount the original working folders into the test
environment. After starting the normal daemon with the recovered configuration,
inspect and retire obsolete bindings:

```sh
fotobank checkout list --config /saved/recovered.toml --json
fotobank checkout retire <checkout-uuid> --config /saved/recovered.toml
fotobank checkout retire <checkout-uuid> --config /saved/recovered.toml --confirm --json
```

Retirement works even when the old folder is missing. It keeps the historical
entries and stops further scanning and commits; it never removes files or saves
uncommitted edits. The recovery daemon does not expose this operation because
it does not open the recovered catalog.

## Try the recovered library

A successful restore is the start of the drill, not its finish. Use a separate
machine or isolated environment where the original storage and working folders
are unavailable. Do not delete your real library to simulate a loss.

1. Restore an archive as described above. Save its `vault_root` and
   `catalog_path` output. Stop the recovery daemon before opening the restored
   vault in normal mode:

   ```sh
   fotobank daemon stop --config /saved/fotobank.toml
   ```

2. Create `/saved/recovered.toml` using the saved configuration. Keep the same
   owner identity. Set `[docbank].root` to `vault_root`, and point `[nas].root`
   and `[flash].root` to separate, already-provisioned test directories. Review
   other host paths, including `[imports].file_lock_path`. Disable scheduled
   backups and optional AI processing for the drill, and bind listeners to
   loopback so the copy does not act as another live deployment.

   Set the catalog path in the environment of both the daemon and subsequent
   commands. For the example restore target above:

   ```sh
   export FOTOBANK_DB_PATH=/recovery/photos/application/catalog.sqlite
   fotobank daemon start --config /saved/recovered.toml
   ```

   Use the actual `catalog_path` from your restore output. The start command
   prints the recovered web UI address. Inspect and retire old checkout
   bindings using the commands above; their working files are not in the archive.

3. Open the recovered web UI. Find a known album and confirm its membership.
   Download representative originals and attached files, such as XMP sidecars,
   and compare their checksums with records saved before the drill. You can also
   inspect album membership through the daemon-backed CLI:

   ```sh
   fotobank albums list --config /saved/recovered.toml --json
   fotobank albums show <album-uuid> --config /saved/recovered.toml
   ```

4. Rebuild the disposable thumbnails, then confirm the recovered photos display
   correctly. Enqueuing work alone does not establish that rebuilding succeeded:

   ```sh
   fotobank thumbs regenerate --all --config /saved/recovered.toml
   ```

5. Stop the test daemon when finished:

   ```sh
   fotobank daemon stop --config /saved/recovered.toml
   ```

The automated recovery test exercises this flow with synthetic JPEGs, an XMP
attachment, an album, and a recorded checkout. It removes its temporary source
storage before restoring, checks downloaded bytes against the inputs, and
fetches rebuilt thumbnails through the recovered photo API. It does not prove
that uncommitted working-folder edits or separately held credentials are backed
up, and it does not exercise every media format or optional AI provider.

`backup verify` checks stored bytes, including the captured catalog, but does
not validate Fotobank's catalog-to-content relationships. `backup restore
--repo` performs that additional reference check. SQLite structural integrity
and whole-catalog metadata validation are separate checks; restore does not
claim to validate every application relationship.
