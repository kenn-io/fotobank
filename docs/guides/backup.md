# Back up and restore

Fotobank's current backup commands protect the SQLite metadata catalog only.
They do **not** back up the authoritative Docbank vault. Until coordinated
backup lands, protect the Docbank root with a separate storage-level backup and
test both recovery paths together.

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

Fotobank does not yet create one coordinated manifest tying those two backups
together. Treat a metadata snapshot as one part of recovery, not a complete
photo-system backup.
