# Import and recover

An import copies supported media into Docbank. It does not rename, move, or
delete the source directory.

```sh
fotobank import /media/card-or-export
```

Fotobank discovers JPEG, PNG, GIF, WebP, HEIC, common camera RAW formats, XMP
sidecars, and common video containers. A JPEG and RAW file with the same name
stem become one asset; a matching XMP file becomes a sidecar. Ambiguous groups
and XMP files without a primary image are rejected.

The command prints its resolved source, Docbank root, and SQLite path before it
writes content. Stop if those paths are not the intended locations. The source
must not be inside Docbank, the artifact root, or the flash directory, and
symbolic-link media files are rejected.

## Interrupted imports

Import uses durable operation records across Fotobank SQLite and Docbank. Run
the recovery command after a crash or interrupted copy:

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
