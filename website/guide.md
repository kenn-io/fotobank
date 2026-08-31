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
sidecar belongs to it. Each relationship points to an exact Docbank version.

## Build disposable data for browsing

Fotobank extracts EXIF and GPS metadata, generates thumbnails, and updates
search from the recorded version. These outputs can be deleted and rebuilt
without changing the stored photograph.

## Edit files through a checkout

A checkout copies selected versions into a working directory. External editors,
file managers, and shell tools can use normal files there.

When a tracked file stops changing, Fotobank records it as a new Docbank
version. A checkout is never the only copy of the archive.

## Keep catalog decisions in Fotobank

Albums, visibility, sharing, and other catalog choices stay in Fotobank. They
can change without copying the original or changing what a Docbank version
means.

## Verify content before rebuilding

Docbank verifies retained content. Fotobank snapshots and restores its catalog
under a coordinated database lock. Checkouts, thumbnails, metadata, and search
data can then be rebuilt.

## Planned Docbank work

Metadata, image previews, embeddings, and retrieval results that describe one
exact file version will live in Docbank. Ratings, picks, albums, people
identities, and other catalog decisions will remain in Fotobank.

Continue with the [technical documentation](/docs/).
