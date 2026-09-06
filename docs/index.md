# Fotobank technical documentation

Fotobank is a self-hosted photo archive and browsing application built on
Docbank's content-addressed storage. Docbank keeps the exact bytes and immutable
versions. Fotobank adds the photo-library model and the workflows photographers
need around those records.

Fotobank is pre-alpha. These pages describe the code as it works now. They are
living architecture documentation, not a promise that unfinished features are
available.

## Start with the system map

The [architecture map](architecture/README.md) defines the package boundaries,
sources of authority, and invariants that new code must preserve.

The most important split is simple:

- Docbank owns exact content, immutable versions, and storage integrity.
- Fotobank owns assets, file relationships, curation, privacy, sharing, and
  working-file checkouts.
- Generated metadata and search indexes are rebuildable. They never become the
  authority for a photo or video.

## Use Fotobank

- [Set up Fotobank](guides/setup.md) — create and validate a configuration.
- [Import and recover](guides/import.md) — copy media into Docbank and finish
  interrupted imports.
- [Work with checkouts](guides/checkouts.md) — materialize ordinary writable
  files and commit tracked edits.
- [Back up and restore](guides/backup.md) — create or schedule complete archives
  and recover into separate storage.
- [Automate Fotobank](guides/automation.md) — invoke commands predictably from
  scripts and agents.

## Read by concern

- [Content and storage](architecture/content-and-storage.md) — imports,
  Docbank, NAS, flash storage, thumbnails, and checkouts.
- [Product features](architecture/product-features.md) — asset grouping,
  metadata, albums, sharing, and hidden media.
- [Search and AI](architecture/search-and-ai.md) — the current Fotobank-owned
  search and optional AI implementation.
- [Operations](architecture/operations.md) — configuration, backup,
  observability, testing, and CI.

For a product-level explanation, read the [photo authority guide](/guide/).
