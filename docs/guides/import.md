# Import and recover

An import copies supported media into Docbank. It does not rename, move, or
delete the source directory.

Import runs through the daemon and starts it if needed. Use the same OS account
and configuration as the daemon, with `identity.mode = "stub"`. The source is
a directory on that machine, not a file upload from a remote client.

```sh
fotobank import /media/card-or-export
fotobank import /media/card-or-export --workers 2 --wait 30s --json
```

Fotobank discovers JPEG, PNG, GIF, WebP, HEIC, common camera RAW formats, XMP
sidecars, and common video containers. A JPEG and RAW file with the same name
stem become one asset; a matching XMP file becomes a sidecar. Ambiguous groups
and XMP files without a primary image are rejected.

The command prints its absolute source path before submitting the import. Use
`fotobank config diagnose` to inspect configured storage locations. The source
must not be inside Docbank, the artifact root, or the flash directory, and
symbolic-link media files are rejected.

The server stays running. You can browse completed imports while background
workers build thumbnails and other enabled projections. `--workers` defaults
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

Import uses durable operation records across Fotobank SQLite and Docbank. Run
the recovery command after a crash or interrupted copy, with the server stopped:

```sh
fotobank content recover
```

Recovery adopts matching Docbank content and finishes ready assets. It reports
conflicts and unmatched Docbank paths without deleting or overwriting them.
Automation can request structured output:

```sh
fotobank content recover --json
```

If a pending operation still needs source bytes, run the original import again
with the same source tree. The importer reuses the reserved identities instead
of creating a second asset.
