# Fotobank Architecture

Use this map to find who owns each part of Fotobank and which rules a code
change must preserve. These pages describe the current implementation.

Update the relevant page when a change affects ownership, data flow, or behavior.
Track proposed work and dependencies in kata.

## System map

- [Runtime and boundaries](runtime.md) explains process composition, package
  layering, identity, HTTP, and background work.
- [Data model](data-model.md) explains SQLite ownership, assets and files,
  albums, shares, hidden state, and transaction rules.
- [Content and storage](content-and-storage.md) explains imported media bytes,
  Docbank, NAS and flash storage, and thumbnails.
- [Product features](product-features.md) explains import, metadata, RAW/JPEG
  relationships, albums, sharing, and hidden media.
- [Search and AI](search-and-ai.md) explains full-text search, vectors,
  generation activation, AI jobs, provenance, and privacy boundaries.
- [Frontend](frontend.md) explains the embedded Svelte application, routes,
  API generation, browser state, and end-to-end tests.
- [Operations](operations.md) explains configuration, startup and shutdown,
  backup, observability, maintenance, testing, and CI.

## Reading the code

Start at `internal/cli/server.go` for runtime composition and
`internal/httpapi/api.go` for the request surface. The schema is
`internal/db/migrations/000001_initial_schema.up.sql`. Each domain package owns
its database operations; `internal/service` owns authorization policy.

When documentation and code disagree, verify the code and fix the document in
the same change. Do not preserve stale text as historical context.
