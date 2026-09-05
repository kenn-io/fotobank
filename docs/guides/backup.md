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

Complete-archive recovery currently has an embedded restore operation and an
automated catalog-and-original recovery test, but no Fotobank archive restore
command. `backup restore` below accepts only metadata snapshots. Keep a tested
storage-level recovery path until the complete restore workflow is available.

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

A useful drill restores the Fotobank snapshot and the corresponding Docbank
backup into isolated locations, starts Fotobank against those restored roots,
and checks that referenced originals can be opened. SQLite integrity alone does
not prove that the media archive is recoverable.

`backup verify` checks stored bytes, including the captured catalog, but does
not validate Fotobank's catalog-to-content relationships. A recovery drill
must also open the restored catalog and resolve its recorded node, version,
checksum, and size against restored originals. SQLite structural integrity
and whole-catalog metadata validation are separate checks.
