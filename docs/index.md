# Use and understand Fotobank

Use these guides to set up Fotobank, manage your photos, and recover your library.
Fotobank is a self-hosted photo system of record built on Docbank.

Fotobank is pre-alpha software, with no stability guarantees. Expect bugs and
changing interfaces and schemas. Keep independent copies of irreplaceable
photos. These pages describe current development code; planned features are
identified separately.

## Start here

Follow [Set up Fotobank](guides/setup.md) to build the application, choose storage,
start the daemon, and import a small collection. Then [create a backup and test
recovery](guides/backup.md) into separate storage.

If you are writing a script or using an agent, start with
[Automate Fotobank](guides/automation.md). The CLI and web app use the same
daemon; an agent does not need direct database access.

## Use Fotobank

- [Set up Fotobank](guides/setup.md) — build, configure, and try a first import.
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
