# A photo system of record you control

Fotobank is a self-hosted photo archive. Docbank stores the exact files and
their version history. Fotobank groups related files into photographs and
provides the catalog around them.

## A photo archive needs more than a directory tree

It must preserve each file, record which files belong to the same photograph,
and support external editors without treating a working directory as the
archive.

1. **Import:** Read a settled source without changing it.
2. **Store:** Keep exact bytes and immutable versions in Docbank.
3. **Link:** Group JPEG, RAW, sidecar, and edited files as one photograph.
4. **Edit:** Put selected versions in a writable checkout.
5. **Recover:** Verify stored content before rebuilding the catalog and its
   derived data.

## Docbank stores files; Fotobank models photographs

Docbank answers “what exact content do we have?” It owns content-addressed
originals, immutable versions, provenance, integrity, storage, and recovery.

Fotobank answers “what does it mean in a photo library?” It owns assets and
file relationships, albums, privacy and sharing, and photographer workflows
such as timelines, maps, review, and writable checkouts.

Each Fotobank file record points to one exact Docbank version. That reference
is the boundary between the archive and the photo catalog.

## Related files stay related

Fotobank can record a primary JPEG, camera RAW file, and XMP sidecar as one
photograph. Each file remains an exact Docbank record. Fotobank stores the
relationship instead of inferring it every time from filenames.

## What this boundary prevents

- Import does not mutate the source. A retry finds the same content or reports
  a conflict.
- External tools edit checkout files. A settled edit becomes a new Docbank
  version and cannot overwrite a newer base.
- Thumbnails, extracted metadata, and search data can be rebuilt from the exact
  stored version.
- Fotobank checks ownership and visibility before it lists, serves, exports, or
  shares a file.

## Planned work in Docbank

Docbank is adding source metadata, image previews, embeddings, and retrieval
for exact content versions. Fotobank will use those APIs instead of keeping
separate generic processing code.

Results that describe one file version will live in Docbank. Decisions about a
photograph will remain in Fotobank.

Continue with the [guide to storage and editing](/guide/) or the [technical
documentation](/docs/).

## Project status

Fotobank is pre-alpha software. Core archive workflows are taking shape, but
this is not yet a supported photo product. The source is licensed under
Apache-2.0.
