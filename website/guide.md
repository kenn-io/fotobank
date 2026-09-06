# How Fotobank stores and edits a photograph

The stored file and the photo catalog have different jobs. This guide shows
where each piece of information lives.

## Import a stable file

Fotobank waits until size and modification time stop changing, then reads the
exact bytes. It does not rename, move, or edit the source.

## Store the exact content in Docbank

Fotobank supplies the expected SHA-256 and size. Docbank accepts those bytes
into content-addressed storage and creates an immutable version under a stable
node.

## Record the file relationships

Fotobank records which file is primary, which is the camera source, and which
sidecar belongs to it. Each file points to an exact Docbank version.

## Build disposable data for browsing

Docbank extracts source metadata, including EXIF and GPS, and supplies canonical
image previews for supported formats. Fotobank projects those facts into its
catalog, sizes thumbnails for browsing, and maintains search indexes. These
derived outputs can be rebuilt from the stored versions.

## Edit files through a checkout

A checkout copies selected versions into a working directory. External editors,
file managers, and shell tools can use normal files there.

The running server scans tracked files and marks settled edits as pending.
Only an explicit `fotobank checkout commit` saves them as new Docbank versions.
Uncommitted edits exist only in the working copy and are not in archive backups.

Today, stop the server before import, checkout creation, or checkout commit;
these commands need to open the vault themselves. Start it again to scan edits
and refresh browsing data. See the [checkout workflow](/docs/guides/checkouts/)
for the full sequence and current limits.

## Keep catalog decisions in Fotobank

Albums, visibility, sharing, and other catalog choices stay in Fotobank. They
can change without copying the original or changing what a Docbank version
means.

## Restore content and catalog together

Fotobank's recovery archive captures its SQLite catalog and Docbank content in
one manifest. Restore verifies the saved bytes and the catalog's references to
those bytes. Albums and other catalog choices need that saved catalog; they
cannot be rebuilt from the files alone.

Configuration, credentials, disposable thumbnails, and working checkout files
are not included. Restore into a separate directory and review configuration
and saved checkout paths before starting the recovered deployment. See
[backup and restore](/docs/guides/backup/).

## Intelligence today and next

Metadata extraction and canonical previews already come from Docbank. Optional
AI jobs, embeddings, and search still run in Fotobank. Moving that reusable
intelligence into Docbank remains work ahead. Photographer decisions stay in
Fotobank; planned features such as ratings and people curation are not yet
available.

Continue with the [technical documentation](/docs/).
