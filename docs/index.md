# Fotobank technical documentation

Use these guides to set up Fotobank, manage your photos, and recover your library.
Fotobank is a self-hosted photo system of record built on Docbank.

Fotobank is pre-alpha. These pages describe the code as it works now. They are
living architecture documentation, not a promise that unfinished features are
available.

## Use Fotobank

- [Set up Fotobank](guides/setup.md) — create and validate a configuration.
- [Import and recover](guides/import.md) — copy media into Docbank and finish
  interrupted imports.
- [Work with checkouts](guides/checkouts.md) — create ordinary writable
  files and commit tracked edits.
- [Back up and restore](guides/backup.md) — create or schedule complete archives
  and recover into separate storage.
- [Automate Fotobank](guides/automation.md) — invoke commands predictably from
  scripts and agents.

## Understand or change the code

Start with the [architecture map](architecture/README.md) for package ownership
and links to each subsystem.

- [Content and storage](architecture/content-and-storage.md) — imports,
  Docbank, NAS, flash storage, thumbnails, and checkouts.
- [Product features](architecture/product-features.md) — asset grouping,
  metadata, albums, sharing, and hidden media.
- [Search and AI](architecture/search-and-ai.md) — the current Fotobank-owned
  search and optional AI implementation.
- [Operations](architecture/operations.md) — configuration, backup,
  observability, testing, and CI.

For a product overview, read the [storage and editing guide](/guide/).
