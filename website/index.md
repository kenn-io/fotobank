# A photo system of record you control

Your photographs hold family history, creative work, and moments you cannot
recreate. Fotobank brings them into a library you can browse, organize, and
work with through your own tools and agents.

[Try Fotobank](/docs/guides/setup/) or [see how it works](/guide/).

Fotobank is pre-alpha software, with no stability guarantees. Expect bugs and
changing interfaces and schemas. Keep independent copies of irreplaceable photos.

![Fotobank's library displaying landscape photos, with browsing filters and a capture-date timeline.](/images/library.jpg)

The running app with a sample library. [View full size](/images/library.jpg).
Sample photos from [Unsplash](https://unsplash.com).

## One photograph can have many files

A camera RAW, a JPEG, an XMP sidecar, and an edit tell different parts of the
same story. Fotobank keeps those relationships in its photo catalog and exact
file versions in Docbank.

1. **Import:** Copy files after they stop changing, leaving the source untouched.
2. **Browse:** Find photos by date, camera, tags, or location. Metadata search
   works without AI.
3. **Organize:** Keep related files together and collect photographs into albums.
4. **Edit:** Create ordinary working files, then explicitly save tracked edits
   as new versions.
5. **Back up:** Capture the photo catalog and stored content together. Test
   recovery into separate storage.

## Built on Docbank, made for photographs

Fotobank embeds [Docbank](https://github.com/kenn-io/docbank) as a library, not a
second server. Docbank stores exact files and immutable versions and supplies
source metadata and image previews.

Fotobank owns photographs and related files, albums, privacy, sharing,
browsing, search, and working copies. Docbank identifies originals by checksum
and checks stored bytes against that checksum.

Both must be backed up. Albums, file relationships, and sharing choices cannot
be reconstructed from Docbank's files alone. Fotobank's recovery archives
capture the catalog and Docbank content together.

## Related files stay related

A JPEG, camera RAW file, and XMP sidecar can represent one photograph. Fotobank
records that relationship directly instead of inferring it every time from
filenames. Each file remains an exact Docbank record.

## Work with the record, not around it

### Use files in an editor

A checkout is a working copy of selected files. The server scans tracked edits;
`checkout commit` saves them as new versions. Uncommitted edits are not in
archive backups. [Read the workflow and limits](/docs/guides/checkouts/).

### Use commands with an agent

List and search photos, inspect metadata, and manage albums through the CLI
and documented HTTP API. Commands use the same daemon as the web app.
[See command examples](/docs/guides/automation/).

### Choose whether to use AI

Optional tagging, captions, and embedding-based search require configured
providers. AI is disabled by default. Read the
[processing and hidden-media rules](/docs/architecture/search-and-ai/#failure-and-privacy-rules)
before enabling it.

### Know what a backup includes

Recovery archives include stored content and the photo catalog, but not
configuration files, provider credentials, or working copies. Repositories are
not encrypted. [Set up and test a backup](/docs/guides/backup/).

## Part of a personal OS for the agentic era

The idea is simple: keep the important parts of your life in systems you
control, with interfaces your chosen tools and agents can use. Fotobank is
the photographic part of that work, alongside
[Docbank](https://github.com/kenn-io/docbank) and [msgvault](https://msgvault.io).

Today, optional AI and search run in Fotobank. Moving reusable intelligence into
Docbank is work ahead. Photo relationships, albums, privacy, and sharing stay
in Fotobank.

Broader enrichment, people curation, and MCP integration are aspirations, not
shipped features. Fotobank has no autonomous assistant.
[Try the current source](/docs/guides/setup/) with copies of a small collection.

Copyright 2026 Kenn Software LLC. Licensed under Apache-2.0.
