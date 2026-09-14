# How Fotobank stores and edits a photograph

The stored file and the photo catalog have different jobs. This guide shows
where each piece of information lives.

Fotobank is pre-alpha software, with no stability guarantees. Keep independent
copies of irreplaceable photos. To try it, start with the
[setup guide](/docs/guides/setup/).

## Import a stable file

Fotobank waits until size and modification time stop changing, then reads the
exact bytes. It does not rename, move, or edit the source.

## Store the exact content in Docbank

Fotobank supplies the file's SHA-256 checksum and size. The checksum identifies
its exact contents. Docbank checks the bytes and records an immutable version:
later edits create new versions without replacing this one.

## Record the file relationships

Fotobank records which file is primary, which is the camera source, and which
sidecar belongs to it. Each file points to an exact Docbank version.

## Build disposable data for browsing

Docbank reads metadata recorded in the file, such as camera settings and GPS
coordinates. It also supplies standard image previews for supported formats.
Fotobank copies those facts into its catalog, sizes thumbnails for browsing,
and maintains search indexes. Fotobank can rebuild this data from stored versions.

## Edit files through a checkout

A checkout copies selected versions into a working directory. External editors,
file managers, and shell tools can use normal files there.

The running server scans tracked files and marks settled edits as pending.
Only an explicit `fotobank checkout commit` saves them as new Docbank versions.
Uncommitted edits exist only in the working copy and are not in archive backups.

Keep the server running to create checkouts, scan and commit tracked edits,
and browse your library. Import also uses the daemon and starts it if needed.
See the [import guide](/docs/guides/import/) and [checkout workflow](/docs/guides/checkouts/)
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

Configuration files, provider credentials, disposable thumbnails, and working
checkout files are not included. Catalog settings and stored authentication
hashes, including hidden-media passcode hashes, are preserved. Restore into a
separate directory and review configuration and saved checkout paths before
starting the recovered deployment. See
[backup and restore](/docs/guides/backup/).

## Intelligence today and next

Metadata and standard previews already come from Docbank. Optional AI processing
and search currently run in Fotobank. Moving reusable AI processing into Docbank
remains work ahead. Photographer decisions stay in
Fotobank; planned features such as ratings and people curation are not yet
available.

Continue with the [technical documentation](/docs/).
