# From camera files to a library you control

Follow a photograph from import to browsing, editing, and recovery. Fotobank
organizes the library. Embedded Docbank keeps its exact files and versions.

Fotobank is pre-alpha software, with no stability guarantees. Keep independent
copies of irreplaceable photos.

## Bring the whole photograph

A photograph can be more than one file. A camera RAW, a JPEG, and an XMP sidecar
can belong together without becoming the same thing.

Fotobank waits until file size and modification time stop changing, then reads
the exact bytes. It does not rename, move, or edit your source files.
[Import your first collection](/docs/guides/import/).

An example file group:

| File | Role |
| --- | --- |
| `IMG_1042.JPG` | Primary display file |
| `IMG_1042.CR2` | Camera source |
| `IMG_1042.CR2.XMP` | Edit metadata sidecar |

Each file points to its own exact stored version.

**Docbank keeps the files.** Fotobank supplies each file's SHA-256 checksum and
size. The checksum identifies the contents. Docbank checks the bytes and records
an immutable version: later edits create new versions, not replacements.
[Content and storage](/docs/architecture/content-and-storage/).

**Fotobank keeps the relationships.** The photo catalog records the primary file,
camera source, and sidecars. Albums, visibility, and sharing can change without
copying the original or changing its stored version.
[How the catalog works](/docs/architecture/data-model/).

## A library you can see and search

Browse by date, camera, tags, or location. Collect photographs into albums.
Metadata search works without AI.
[Check supported files and previews](/docs/guides/formats/).

Docbank reads source metadata, such as camera settings and GPS coordinates, and
supplies image previews for supported formats. Fotobank uses those facts, sizes
thumbnails, and maintains search indexes.

Thumbnails and indexes are rebuildable. Your album choices are not: those belong
to the photo catalog, which needs a backup.

![The running Fotobank app showing landscape photos, browsing filters, and a capture-date timeline.](/images/library.jpg)

The running app with a sample library. [View full size](/images/library.jpg).
Sample photos from [Unsplash](https://unsplash.com).

## Edit a copy. Keep the original.

A **checkout** copies selected versions into a working directory. Use an editor,
file manager, or shell tool on these ordinary files.

The running server notices settled edits and marks them as pending. Only an
explicit `fotobank checkout commit` saves them as new Docbank versions.

An example version history:

1. **Version 1 — stored file.** Kept in Docbank, unchanged.
2. **Working copy — your edits.** Ordinary files in your checkout folder. Changes
   stay here until you commit.
3. **Version 2 — committed file.** New bytes stored in Docbank. Version 1 remains
   available.

Uncommitted edits are not in archive backups. Keep the server running to create
checkouts, scan and commit edits, and browse. Import also uses the daemon and
starts it if needed. [Checkout workflow and current limits](/docs/guides/checkouts/).

## Recover the library. Not just the files.

A recovery archive contains both Docbank content and the Fotobank catalog.
Restore verifies the saved bytes and the catalog's references to them.

| In the archive | What it preserves |
| --- | --- |
| Docbank content | Stored files and versions |
| Fotobank catalog | File relationships, albums, visibility, sharing, settings, and stored authentication hashes |

Both belong in the backup. The files alone cannot reconstruct your catalog choices.

Restore into a separate directory. Review configuration and saved checkout paths
before starting the recovered deployment. Then rebuild disposable thumbnails and
indexes as needed. [Make a backup and test recovery](/docs/guides/backup/).

**Outside the archive.** Keep configuration files, provider credentials, and
working checkout files separately. Disposable thumbnails are also excluded and
can be rebuilt. Catalog settings and authentication hashes, including hidden-media
passcode hashes, are preserved. Backup repositories are not encrypted.

## Intelligence today and next

Metadata and standard previews already come from Docbank. Optional AI processing
and search currently run in Fotobank. Moving reusable AI processing into Docbank
remains work ahead. Photographer decisions stay in Fotobank; planned features
such as ratings and people curation are not yet available.

[Try a small collection](/docs/guides/setup/) or
[set up with an agent](/docs/guides/agent-setup/).

[Fotobank on GitHub](https://github.com/kenn-io/fotobank) ·
[Join the Kenn community on Discord](https://discord.gg/nEB7VaAnU9).
