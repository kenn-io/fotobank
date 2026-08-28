# Fotobank Architecture

These documents describe how Fotobank works now and the architectural
boundaries that code changes must preserve. They are living documentation:
when a pull request changes a boundary, data flow, invariant, or operational
contract, it updates the relevant page in the same pull request.

They are not a project history or an implementation tracker. Proposed work and
dependencies belong in kata. Pull requests explain the change being reviewed.
Temporary design notes and execution checklists are not committed.

## System map

- [Runtime and boundaries](runtime.md) explains process composition, package
  layering, identity, HTTP, and background work.
- [Data model](data-model.md) explains SQLite ownership, media records, assets,
  albums, shares, hidden state, and transaction rules.
- [Content and storage](content-and-storage.md) explains imported media bytes,
  Docbank, NAS and flash storage, thumbnails, and the active authority
  transition.
- [Product features](product-features.md) explains import, metadata, RAW/JPEG
  relationships, albums, sharing, and hidden media.
- [Search and AI](search-and-ai.md) explains full-text search, vectors,
  generation activation, AI jobs, provenance, and privacy boundaries.
- [Frontend](frontend.md) explains the embedded Svelte application, routes,
  API generation, browser state, and end-to-end tests.
- [Operations](operations.md) explains configuration, startup and shutdown,
  backup, observability, maintenance, testing, and CI.

## Architectural rules

1. Domain repositories are database-only. Services enforce caller ownership.
   CLI and HTTP transports call services rather than bypassing them.
2. Public media and share identifiers are opaque UUIDs. Storage paths and
   Docbank catalog identifiers are internal coordinates, not authorization.
3. Imported source files are copied and left untouched.
4. Fotobank owns product meaning: assets, file relationships, metadata,
   albums, sharing, privacy, thumbnails, search, and AI projections.
5. Docbank is the intended authority for imported media bytes and immutable
   versions. Fotobank accesses it only through `internal/content`.
6. Thumbnails, full-text indexes, vectors, and extracted metadata are
   rebuildable projections. They never become media-byte authority.
7. Hidden-media controls are application privacy, not encryption. Every list,
   search, share, thumbnail, and byte-read path enforces the same visibility
   policy.
8. Cross-database mutations use durable operation records and idempotent
   content operations. Code must not pretend Fotobank SQLite and Docbank can
   share a transaction.
9. This pre-alpha repository edits the single initial migration in place.
   There is no compatibility layer for databases that have not shipped.

## Reading the code

Start at `internal/cli/server.go` for runtime composition and
`internal/httpapi/api.go` for the request surface. The schema is
`internal/db/migrations/000001_initial_schema.up.sql`. Each domain package owns
its database operations; `internal/service` owns authorization policy.

When documentation and code disagree, verify the code and fix the document in
the same change. Do not preserve stale text as historical context.
