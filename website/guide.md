# How Fotobank stores and edits photos

Follow one photograph from import through browsing, editing, and recovery.
Fotobank keeps the catalog. Embedded [Docbank](https://docbank.ai/) keeps the
exact files and versions.

Fotobank is pre-alpha software with no stability guarantees. Keep independent
copies of irreplaceable photos.

## Import

One photograph is often several files. A camera RAW, a JPEG, and an XMP sidecar
are recorded as one photo with three files.

Fotobank waits until a file's size and modification time stop changing, then
reads it. It does not rename, move, or edit your source files.
[Import your first collection](/docs/guides/import/).

An example file group:

| File | Role |
| --- | --- |
| `IMG_1042.JPG` | Display file |
| `IMG_1042.CR2` | Camera source |
| `IMG_1042.CR2.XMP` | Edit sidecar |

Each file points to its own stored version.

**[Docbank](https://docbank.ai/) keeps the files.** Fotobank passes each file's
SHA-256 checksum and size. Docbank verifies the bytes and stores them as a
version that never changes. Later edits create new versions.
[Content and storage](/docs/architecture/content-and-storage/).

**Fotobank keeps the relationships.** The catalog records which file is the
display file, the camera source, and the sidecar. Albums, visibility, and
sharing change in the catalog without copying or changing stored files.
[How the catalog works](/docs/architecture/data-model/).

## Browse and search

Browse by date, camera, tags, or location. Collect photographs into albums.
Search by metadata does not need AI.
[Check supported files and previews](/docs/guides/formats/).

Docbank reads metadata such as camera settings and GPS coordinates and renders
previews for supported formats. Fotobank builds thumbnails and search indexes
from them.

Thumbnails and indexes can be rebuilt. Albums cannot. They live in the photo
catalog, which needs a backup.

![The running Fotobank app showing landscape photos, browsing filters, and a capture-date timeline.](/images/library.jpg)

The running app with a sample library. [View full size](/images/library.jpg).
Sample photos from [Unsplash](https://unsplash.com).

## Edit

A **checkout** copies selected versions into a working directory as ordinary
files. Edit them with any editor, file manager, or shell tool.

The server notices edits once they stop changing and marks them pending. Nothing
is stored until you run `fotobank checkout commit`, which saves the edited files
as new Docbank versions.

An example version history:

1. **Version 1: stored file.** Kept in Docbank, unchanged.
2. **Working copy: your edits.** Ordinary files in your checkout folder. Changes
   stay here until you commit.
3. **Version 2: committed file.** New bytes stored in Docbank. Version 1 is still
   available.

Edits you have not committed are not in backups. The server must be running to
create checkouts, scan and commit edits, and browse. Import starts the server if
needed. [Checkout workflow and current limits](/docs/guides/checkouts/).

## Back up and restore

A backup archive contains both Docbank content and the Fotobank catalog. Restore
checks the saved bytes and the catalog's references to them.

| In the archive | What it holds |
| --- | --- |
| Docbank content | Stored files and versions |
| Fotobank catalog | File relationships, albums, visibility, sharing, settings, and stored authentication hashes |

Both belong in the backup. The files alone cannot rebuild albums or file
relationships.

Restore into a separate directory. Check configuration and saved checkout paths
before starting the restored deployment. Then rebuild thumbnails and indexes.
[Make a backup and test recovery](/docs/guides/backup/).

**Not in the archive.** Configuration files, provider credentials, and checkout
directories. Back these up separately. Thumbnails are also excluded and can be
rebuilt. Catalog settings and authentication hashes, including hidden-media
passcode hashes, are in the archive. Backup repositories are not encrypted.

## AI features

Metadata and previews come from Docbank without AI. Optional tagging, captions,
and semantic search run in Fotobank and need a configured provider. Some of that
may move into Docbank later. Ratings and people grouping are not built.

[Try a small collection](/docs/guides/setup/) or
[set up with an agent](/docs/guides/agent-setup/).

[Fotobank on GitHub](https://github.com/kenn-io/fotobank) ·
[Join the Kenn community on Discord](https://discord.gg/nEB7VaAnU9).
