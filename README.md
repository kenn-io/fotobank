# fotobank

Fotobank is a self-hosted photo archive and browsing app built around Docbank's
content-addressed storage. Fotobank owns the photo-library model—metadata,
albums, sharing, privacy, thumbnails, and search—while Docbank is the authority
for imported photos, videos, RAW files, sidecars, and their versions.

Import copies source files and leaves them untouched. Writable, partial
checkouts provide ordinary files for photo editors, file managers, and
shell tools without making a working directory the archive authority.

Fotobank is not trying to be every photo product for every household. It is a
small, inspectable archive manager optimized for my own storage topology,
workflow, and preferences.

## Status

Pre-alpha. This repository is public-looking code, but the project is not yet
ready for broad public use.

Albums, hidden, sessions, AI tag/caption, search, and sharing CLI/API are in.
The owner sharing UI is hidden by default behind `[ui].sharing_enabled` in
`config.toml`; the CLI works regardless of the flag.

Expect schema changes, incomplete operator documentation, rough upgrade paths,
and implementation details that still assume a developer/operator who is
comfortable reading the code. The README is written to explain where the
project is headed, not to promise a stable install experience today.

## Why This Exists

Fotobank is built around a few opinions:

- **Working files matter.** Selected media should be available as ordinary
  writable files for external photo tools, file managers, and shell tools.
  Those files are explicit checkouts, not hidden storage internals.
- **Docbank is content authority.** Imported media and immutable versions live
  in Docbank. NAS can host Docbank and durable backups; local flash and
  Fotobank thumbnails remain disposable performance layers.
- **SQLite is enough for the metadata core.** The app is meant to be simple to
  run, snapshot, inspect, and restore.
- **The CLI and web app should share one write path.** Humans use the web UI;
  scripts and agents use the CLI. Both go through the same service layer.
- **Sharing is metadata, not file movement.** Shares are scopes over albums or
  media sets. Creating or revoking a share does not copy or rearrange original
  files.
- **AI is optional and provenance-aware.** AI workers are disabled by default,
  send downscaled metadata-stripped images to the configured endpoint, and keep
  model/input provenance for generated tags, captions, and embeddings.

## What Works Today

The codebase currently includes:

- CLI import with owner-scoped deduplication and Docbank-backed EXIF/GPS metadata,
  date-based storage, reconcile support, and thumbnail generation.
- A Svelte web app with library browsing, sessions, media detail pages,
  lightbox viewing, albums, shares, hidden media, AI settings, and search.
- Album CRUD and album membership through both HTTP and CLI surfaces.
- Scope-based sharing with owner-side management, grantee-side read endpoints,
  download permissions, and a stub or exec-backed broker adapter.
- Hidden media as app-level privacy: passcode-gated owner access, default
  exclusion from library/search/share surfaces, and sidecar-aware hide/unhide.
  This is not encryption.
- Optional AI tagging, captioning, and hybrid search using OpenAI-compatible
  endpoints, SQLite FTS5, and sqlite-vec embeddings.
- Operational basics: backup snapshots and restore, structured logging,
  readiness checks, Prometheus metrics, OpenAPI generation, and frontend tests.

This list describes the development state, not a support guarantee.

## Fotobank And Immich

[Immich](https://github.com/immich-app/immich) is a much larger and much more
mature self-hosted photo and video management project. Its official feature
list includes mobile backup apps, multi-user support, albums, sharing, RAW
support, metadata and map views, search, facial recognition, CLIP search,
external libraries, storage templates, and many other features. If you want a
polished self-hosted Google Photos-style experience today, Immich is the first
project I would look at.

Fotobank is different because it is narrower and more personal. I wanted a
system where:

- writable filesystem checkouts remain explicit and rebuildable;
- Docbank is the authoritative media archive and local flash remains
  disposable;
- external tools can work with ordinary files without becoming the archive
  authority;
- one Go binary owns both the CLI and server write paths;
- SQLite is the primary metadata store;
- sharing can be mediated by an external identity/grant broker; and
- AI/search features expose their provenance and respect hidden-photo
  boundaries.

So this is not "Immich, but smaller." It is a personal archive manager with a
web viewer, built for a particular workflow. Immich is a serious, established
project; Fotobank exists because I wanted to explore a different set of
trade-offs for my own library.

Useful Immich references for comparison:

- [Immich README](https://github.com/immich-app/immich)
- [Mobile Backup](https://docs.immich.app/features/mobile-backup/)
- [External Libraries](https://docs.immich.app/features/libraries)
- [Storage Templates](https://docs.immich.app/administration/storage-template)

## Architecture

Fotobank is a Go application with a Svelte frontend embedded into the server
binary.

- `cmd/fotobank` is the CLI entry point.
- `internal/cli` contains commands such as `server`, `import`, `reconcile`,
  `thumbs`, `albums`, `shares`, `hidden`, `ai`, and `backup`.
- `internal/httpapi` exposes the REST API and byte-streaming routes.
- `internal/service` is the auth-scoped service layer used by both CLI and
  HTTP transports.
- `internal/media`, `internal/album`, `internal/share`, `internal/thumb`,
  `internal/search`, and `internal/ai` hold the domain packages.
- `frontend/` is the Svelte app built into `internal/web/dist`.

The active import path writes exact file versions to embedded Docbank and
records the photo-specific asset graph in Fotobank. Writable checkouts expose
selected versions as ordinary files and can commit settled edits back as new
immutable Docbank versions. The current boundaries are documented in
[`docs/architecture/`](docs/architecture/README.md).

Identity supports local stub mode for development and header mode for
deployment behind a trusted identity-aware reverse proxy.

## Getting Started

The setup path is still developer-oriented.

Requirements:

- Go 1.27+
- A C compiler, because SQLite uses `mattn/go-sqlite3`, FTS5, and sqlite-vec
- Bun 1.3+ for frontend builds
- A writable NAS/archive directory

Build:

```sh
make build            # → bin/fotobank (debug)
make build-release    # → bin/fotobank (release; trimpath + stripped)
make install          # copies bin/fotobank to ~/.local/bin or $GOBIN
make dev              # live-reload via air
```

`make build` is the preferred entry point — it builds the SPA into
`internal/web/dist/` before the Go build embeds it. The direct
`go build -tags sqlite_fts5 …` path skips the SPA build, so it produces a
backend-only binary unless `internal/web/dist/` is already populated:

```sh
go build -tags sqlite_fts5 -o bin/fotobank ./cmd/fotobank
```

Create a config with the installed binary:

```sh
bin/fotobank config init
```

Fotobank uses TOML. The loader resolves the config path with this precedence:

1. `--config <path>` flag
2. `FOTOBANK_CONFIG` environment variable
3. `$XDG_CONFIG_HOME/fotobank/config.toml`
4. `$HOME/.config/fotobank/config.toml`
5. `./config.toml`

The command never overwrites an existing file. Edit `[docbank].root` and
`[nas].root` before first use. They should be separate directories on durable
storage: Docbank holds original media, while the NAS artifact root holds
rebuildable files and Fotobank metadata snapshots. The default stub identity is
usable for local development.

Validate and run:

```sh
bin/fotobank config validate
bin/fotobank import /path/to/source
bin/fotobank serve
```

By default the server listens on `127.0.0.1:8090`.

Common commands:

```sh
bin/fotobank serve             # HTTP API + background workers
bin/fotobank config init       # write an editable config without replacing one
bin/fotobank config path
bin/fotobank import <dir>      # import photos/videos
bin/fotobank content recover   # finish imports and report unmatched Docbank files
bin/fotobank checkout          # estimate/create working copies and commit edits
bin/fotobank thumbs regenerate # rebuild thumbnails
bin/fotobank albums            # CRUD over albums
bin/fotobank shares            # CRUD over share scopes (CLI works regardless of [ui].sharing_enabled)
bin/fotobank hidden            # manage the hidden-privacy passcode
bin/fotobank ai                # AI status / backfill / retry / acknowledge
bin/fotobank gps               # GPS metadata management
bin/fotobank backup            # snapshot / list / restore the metadata DB
bin/fotobank owners            # list / register principals
```

## Development

Useful targets:

```sh
make test             # Go test suite with sqlite_fts5 tag
make test-short       # Short Go tests
make frontend-check   # Svelte typecheck and frontend unit tests
make test-e2e         # Playwright e2e suite
make lint             # golangci-lint plus local testify helper check
make api-generate     # regenerate OpenAPI and TypeScript schema
```

## License

Copyright 2026 Kenn Software LLC. Licensed under the Apache License 2.0. See
[LICENSE](LICENSE) and [NOTICE](NOTICE).
