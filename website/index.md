# A photo system of record you control

Fotobank is a self-hosted photo archive. Docbank stores the exact files and
their version history. Fotobank groups related files into photographs and
provides the catalog around them.

## A photo archive needs more than a directory tree

It must preserve each file, record which files belong to the same photograph,
and support external editors without treating a working directory as the
archive.

1. **Import:** Copy files after they stop changing, leaving the source untouched.
2. **Store:** Keep exact bytes and immutable versions in Docbank.
3. **Link:** Group JPEG, RAW, sidecar, and edited files as one photograph.
4. **Edit:** Put selected versions in a writable checkout.
5. **Recover:** Restore the photo catalog and stored content together, then
   rebuild derived data.

## Docbank stores files; Fotobank models photographs

Docbank stores exact originals and version history. It identifies file content
by its checksum and checks that stored bytes match that checksum.

Fotobank answers “what does it mean in a photo library?” It owns assets and
file relationships, albums, privacy and sharing, and photographer workflows
such as timelines, maps, review, and writable checkouts.

Each Fotobank file record points to one exact Docbank version. That reference
is the boundary between the archive and the photo catalog.

Both must be backed up. Albums, file relationships, and sharing choices cannot
be reconstructed from Docbank's files alone. Fotobank's recovery archives
capture the catalog and Docbank content together.

## Related files stay related

Fotobank can record a primary JPEG, camera RAW file, and XMP sidecar as one
photograph. Each file remains an exact Docbank record. Fotobank stores the
relationship instead of inferring it every time from filenames.

## How Fotobank preserves your record

- Import does not mutate the source. A retry finds the same content or reports
  a conflict.
- External tools edit checkout files. An explicit `checkout commit` saves a
  settled edit as a new Docbank version and rejects a changed base.
- Thumbnails, extracted metadata, and search data can be rebuilt from the exact
  stored version.
- Fotobank checks ownership and visibility before it lists, serves, exports, or
  shares a file.

## Shared intelligence, built on Docbank

Docbank supplies metadata from the original files and standard image previews
for supported formats. Fotobank uses them for photo details and thumbnails.

Optional AI processing and search currently run in Fotobank. The goal is to move
reusable AI processing into Docbank. That integration is not implemented yet.
Photo relationships, albums, privacy, and sharing stay in Fotobank.

Continue with the [guide to storage and editing](/guide/) or the [technical
documentation](/docs/).

## Project status

Fotobank is pre-alpha software. Core archive workflows are taking shape, but
this is not yet a supported photo product. The source is licensed under
Apache-2.0.
