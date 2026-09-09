# Fotobank

Fotobank is a photo system of record for you and your agents, built on
[Docbank](https://github.com/kenn-io/docbank).

Your photos are among the most important things you own. They hold family
history, creative work, and moments you cannot recreate. Fotobank exists to
keep that record under your control—and make it useful as your collection,
tools, and ways of working change.

It combines an exact, versioned archive with a photo catalog and a web app.
The ambition goes further: a place where agents can help organize, describe,
find, and work with your photographs without becoming the owners of the record.

Fotobank is pre-alpha. Core workflows are implemented, but interfaces and
schemas still change. Keep independent copies of irreplaceable files and test
recovery before relying on it.

## Why another photo application?

Fotobank is not an attempt to match [Immich](https://github.com/immich-app/immich)
feature for feature. It starts with a particular question: what should hold
your photographic record when both people and agents work with it?

A photograph is often more than one file. There may be a camera RAW, a JPEG,
an XMP sidecar, and several edits. An album records a choice you made. A caption
may be your own description or a model's interpretation. A search index is
useful, but it is not the photograph.

Those distinctions shape Fotobank:

- Exact files and their versions belong in durable storage, independent of
  the editor, model, or interface using them.
- Related files, albums, privacy, and sharing belong in a photo catalog,
  rather than being repeatedly guessed from directory names.
- External tools need ordinary working files. Their edits should return as
  explicit new versions, not silently replace the archive.
- Agents need documented operations and inspectable results, not permission
  to rearrange storage internals.
- Generated descriptions, previews, and indexes should help you use the
  record—not define what survives when a tool or provider changes.

The web app matters. So do editing workflows, automation, and recovery. They
are different ways of working with the same photo record.

## Part of a personal OS for the agentic era

A personal OS brings the important parts of your life into systems you
control, with interfaces that let your chosen tools and agents work together.

Docbank provides a system of record for documents and files. Fotobank builds
the photographic part of that world: the meaning of related files, the
collections you curate, what stays private, and what you choose to share.

The goal is to support work such as finding photographs by what they show,
proposing tags and descriptions, preparing an album for review, or handing
selected files to an editing tool. These are directions for agent workflows,
not claims that Fotobank already has an autonomous assistant.

The architectural commitment is one daemon-owned HTTP API for the web app,
CLI, and eventually MCP. Much of the CLI already follows it; the remaining
migrations are identified in the [automation guide](docs/guides/automation.md).
There is no separate agent-owned catalog to keep in sync.

## Docbank stores the record. Fotobank understands the photo library.

Fotobank embeds Docbank as a Go library; it does not require a second daemon.
Docbank holds exact content, immutable versions, and the storage machinery
behind them. It already supplies source metadata and canonical image previews
that Fotobank uses for photo details and thumbnails.

Fotobank owns photographs and their file relationships, albums, privacy,
sharing, browsing, and writable checkouts. Each cataloged file points to an
exact Docbank version. Complete recovery archives capture both the catalog and
Docbank content: your album choices and file relationships cannot be recovered
from the original bytes alone.

Reusable intelligence belongs in Docbank. Tagging, enrichment, renditions,
embeddings, and semantic retrieval should be capabilities applications can
share, not pipelines each application has to reinvent. Fotobank's job is to
turn those capabilities into useful photographic workflows.

That integration is still in progress. Optional tagging, captioning,
embedding, and hybrid-search code currently runs in Fotobank; it has not all
moved to Docbank. Broader enrichment and agent workflows remain aspirations.
AI is disabled by default. Enabling it requires configuring providers and
acknowledging processing of hidden media. Hidden-media controls are application
privacy, not encryption; understand that boundary before enabling processing.

## What you can do today

- Import photos, videos, RAW files, and sidecars without modifying the source.
  Keep related files together and detect duplicate content within an owner.
- Browse the library, inspect photo details, organize albums, and manage
  privacy and sharing. Owner-side sharing UI is optional; its CLI and API
  remain available.
- Create partial, writable checkouts for editors and shell tools. Inspect
  changes and explicitly commit settled edits as new versions, with conflicts
  reported when the stored base has changed.
- Try optional AI tagging, captioning, and hybrid text/vector search with
  configured providers. Generated results retain model and input provenance.
- Create complete recovery archives, schedule backups with retention, and
  verify a restore into separate storage.

These are development capabilities, not a promise of a finished consumer
product. Start with the [workflow guide](website/guide.md),
[operating documentation](docs/index.md), and
[current architecture](docs/architecture/README.md).

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
rebuildable files. Complete backups use a separately initialized repository.
The default stub identity is usable for local development.

Validate and run:

```sh
bin/fotobank config validate
bin/fotobank daemon start
bin/fotobank import /path/to/source
```

Start prints the web UI URL. By default the server listens on
`127.0.0.1:8090`; ports and lifecycle settings are configurable. Use
`bin/fotobank serve` instead for foreground operation.

Scheduled backups are disabled by default. Initialize an archive repository
with `bin/fotobank backup init --repo /backups/photos`, then set
`[backup].enabled = true` and `[backup].repository = "/backups/photos"` in the
configuration. The default schedule captures a complete archive every 24 hours
and retains 30 scheduled recovery points. Manual archives are retained
independently.
See the [backup guide](docs/guides/backup.md) for configuration and recovery
drills.

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
bin/fotobank backup            # init / create / list / verify / restore complete archives
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
