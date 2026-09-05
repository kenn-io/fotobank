# Back up and restore

Fotobank can create a recovery archive containing its SQLite catalog and the
authoritative Docbank content in one manifest. Scheduled backups and the
`snapshot` command still produce metadata-only SQLite files. Choose the
complete archive commands explicitly when backing up original media.

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

Repositories are not encrypted. Store them on protected storage. Archive
retention is not yet exposed; these commands retain every recovery point.
The existing retention settings apply only to scheduled metadata snapshots.

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
`restore` to select an older point. There is no overwrite option for archive
recovery; `--yes` and `--dry-run` belong to metadata restore only. Use
`backup verify --repo /backups/photos` for a read-only archive check.

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

## Snapshot the Fotobank catalog

```sh
fotobank backup snapshot
fotobank backup list
```

By default, snapshots go beneath
`[nas].root/.fotobank/snapshots/`. Set `[backup].dir` to use another absolute
directory. Scheduled snapshots use the configured 15-minute, hourly, and daily
retention counts.

For automation, both commands support `--json`:

```sh
fotobank backup snapshot --json
fotobank backup list --json
```

## Validate and restore metadata

Stop the server and every other Fotobank command that uses the database. Check
the snapshot and database lock without changing files:

```sh
fotobank backup restore --dry-run /path/to/snapshot.sqlite
```

Then restore interactively, or use `--yes` only in automation that has already
selected and validated the exact snapshot:

```sh
fotobank backup restore /path/to/snapshot.sqlite
```

Restore moves the previous database files aside instead of deleting them.

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
