# A photo system of record you control

Fotobank keeps exact originals and immutable versions in Docbank, then adds the
relationships, curation, and working-file experience a photographer needs.

**Development status:** Fotobank is pre-alpha. Core archive workflows are
taking shape, but this is not yet a supported photo product.

## Your library is more than a folder

A photo archive must preserve the bytes, explain which files belong together,
and let tools edit ordinary files without quietly becoming the authority.

1. **Ingest:** Copy a settled source without changing it.
2. **Preserve:** Keep exact bytes and immutable versions in Docbank.
3. **Relate:** Model JPEG, RAW, sidecar, and edited files as one asset.
4. **Work:** Materialize selected versions as explicit writable checkouts.
5. **Recover:** Verify the content authority and restore the catalog
   deliberately.

## Two systems, one clean line

Docbank answers “what exact content do we have?” It owns content-addressed
originals, immutable versions, provenance, integrity, storage, and recovery.

Fotobank answers “what does it mean in a photo library?” It owns assets and
file relationships, albums, privacy and sharing, and photographer workflows
such as timelines, maps, review, and writable checkouts.

The boundary between them is an exact content version.

## One photograph may be several files

Fotobank can relate a primary JPEG, a camera RAW, and an XMP sidecar as one
asset. Each file remains an exact Docbank record. The asset supplies stable
photo identity and product meaning without flattening the relationship into a
filename convention.

## Operating principles

- **Exact originals:** Imports do not mutate the source. Retries find the same
  record or stop on a conflict.
- **Ordinary working files:** Selected versions become normal files. Settled
  edits commit as new immutable versions.
- **Rebuildable views:** Thumbnails, extracted metadata, and search projections
  can be regenerated from a known content version.
- **Visible boundaries:** Fotobank checks ownership and visibility before it
  lists, serves, exports, or shares a record.

## Direction: shared photo intelligence

**Planned — not available yet.**

Docbank is growing a shared processing layer for retained renditions, metadata,
embeddings, and retrieval. Fotobank will use that layer instead of maintaining
a second generic AI stack.

The governing rule will stay simple: generic facts about one content version
belong in Docbank; photographer decisions about an asset belong in Fotobank.

Continue with the [photo authority guide](/guide/) or the [technical
documentation](/docs/).
