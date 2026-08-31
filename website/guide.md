# The photo authority lifecycle

Follow one photograph from source import through exact storage, asset
relationships, working checkouts, and recovery. The original remains exact
while each layer adds explicit, replaceable meaning around it.

## 1. Observe a settled source

Fotobank waits until size and modification time stop changing, then reads the
exact bytes. The source is never renamed, rearranged, or edited in place.

## 2. Preserve exact content

Fotobank supplies the expected SHA-256 and size. Docbank accepts those bytes
into content-addressed storage and creates an immutable version under a stable
node.

## 3. Relate the files as one photo

The product catalog records which file is primary, which is the camera source,
and which sidecar belongs to it. Each relationship points to an exact Docbank
version.

## 4. Project useful views

Fotobank extracts photo metadata and generates thumbnails from the recorded
version. These outputs speed up browsing, but they can be discarded and rebuilt
without changing the record.

## 5. Check out ordinary files

A checkout materializes selected exact versions into a deliberate working
directory. External editors, file managers, and shell tools can use normal
files there.

When a settled tracked file changes, Fotobank commits it as a new immutable
Docbank version. The checkout never becomes the silent archive authority.

## 6. Organize without rewriting the record

Albums, visibility, sharing, and other catalog choices stay in Fotobank. They
can change without copying the original or changing what a Docbank version
means.

## 7. Prove the archive still holds

Docbank verifies retained content. Fotobank snapshots and restores its product
catalog under a coordinated database lock. Checkouts and projections can be
reconstructed from those authorities.

## Direction: shared intelligence

**Planned — not available yet.**

Photo metadata, canonical previews, embeddings, and retrieval that describe
one exact file version belong in Docbank. Ratings, picks, albums, people
identities, and other photographer decisions belong in Fotobank.

This avoids building the same generic intelligence twice while keeping the
photo-library experience focused.

Continue with the [technical documentation](/docs/).
