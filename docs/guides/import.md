# Import photos

Copy a folder into your photo library while Fotobank stays running. The import
stores originals in Docbank and keeps related RAW and XMP files together.
Your source files keep their names, locations, and contents.

Import runs through the daemon and starts it if needed. Use the same OS account
and configuration as the daemon, with `identity.mode = "stub"`. The source is
a directory on that machine, not a file upload from a remote client.

```sh
fotobank import /media/card-or-export
fotobank import /media/card-or-export --workers 2 --wait 30s --json
```

Fotobank discovers JPEG, PNG, GIF, WebP, HEIC, common camera RAW formats, XMP
sidecars, and common video containers. A JPEG and RAW file with the same name
stem become one photo; a matching XMP file becomes an attachment. Ambiguous
groups and XMP files without a primary image are rejected.
See [files and previews](formats.md) for supported extensions and preview limits.

The command prints its absolute source path before submitting the import. Use
`fotobank config diagnose` to inspect configured storage locations. The source
must not be inside Docbank, the artifact root, or the flash directory, and
symbolic-link media files are rejected.

The server stays running. You can browse completed imports while background
workers build thumbnails and other enabled derived data. `--workers` defaults
to the daemon's import configuration; `--wait` controls how long to wait for
another import to finish (zero fails immediately if busy).

`--json` writes one final result to stdout and progress to stderr. The result
contains imported, duplicate, and conflict counts, individual failures, and an
optional error. Partial failures exit nonzero without discarding successful
imports. Ctrl-C, a lost connection, or daemon shutdown cancels unfinished work.
A lost final response is an error, even if some files were imported. Rerun the
same source to reconcile completed files and continue; the CLI does not retry
automatically.

## Interrupted imports

Finish an interrupted import with `content recover`. It checks saved progress
against files already stored in Docbank. This command repairs import records;
to recover a lost library, use [backup restore](backup.md#restore-a-complete-archive).

Run it after a crash or interrupted copy. It uses the daemon, starting it if
needed, with the same local operator access as import:

```sh
fotobank content recover
```

Recovery links matching Docbank files to their saved import records and marks
completed photos ready. It reports
conflicts and unmatched Docbank paths without deleting or overwriting them.
It checks every registered owner, not just the configured photo owner. Recovery
and imports share a lock; use `--wait 30s` to wait for an import to finish.
Automation can request structured output:

```sh
fotobank content recover --json
```

JSON contains `reports` (one per owner) and an `error` when work could not
finish. Errors exit nonzero and preserve any completed work. Cancellation or a
lost connection is not proof of completion; rerun the command to reconcile it.

If a pending operation still needs source bytes, run the original import again
with the same source tree. The importer reuses the reserved identities instead
of creating a second asset.

## Refresh photo locations

Refresh photo coordinates or place names with `gps backfill`. It reads Docbank
metadata and uses a local place-name lookup. Originals stay unchanged; videos
are excluded. Run it under the daemon's OS account and configuration. The
command starts the daemon if needed.

```sh
fotobank gps backfill --mode fill-missing --since 168h --json
fotobank gps backfill --mode relabel
fotobank gps backfill --mode full --all-owners
```

`full` refreshes GPS from the source metadata and clears stored coordinates
when the source has none. `fill-missing` only checks photos without coordinates.
`relabel` updates place names from existing coordinates without reading originals.
`--since` limits the run by import time, not capture time.

By default, the command uses the configured stub owner. Host administrators can
select `--owner hub:user` or `--all-owners`; header mode requires one of those
explicit scopes. This operation is not available through the photo-user API.

The final result reports processed, updated, unchanged and failed counts.
`--json` includes per-photo failures and an error when the run did not fully
succeed. Errors exit nonzero, but successful updates remain saved. A canceled
or disconnected run can be rerun; the client does not retry it automatically.
