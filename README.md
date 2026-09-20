# Fotobank

Fotobank is a photo system of record for you and your agents, built on
[Docbank](https://docbank.ai/).

Fotobank is a self-hosted photo library. It keeps your original files and
every later version, keeps related files together, and lets you browse and
organize through a web app, the command line, or an agent.

Fotobank is pre-alpha software, with no stability guarantees. Expect bugs and
changing interfaces and schemas. Start with copies of a small collection, keep
independent originals, and test recovery separately.

[Build from source](docs/guides/setup.md) ·
[Set up with an agent](docs/guides/agent-setup.md) ·
[How it works](website/guide.md) · [Website](https://fotobank.ai)

![Fotobank's library displaying a sample landscape collection](website/images/library.jpg)

The running app with a sample library.
[View full size](website/images/library.jpg). Photos from
[Unsplash](https://unsplash.com); [image credits](docs/publishing.md#sample-library-screenshot).

## Why another photo application?

Fotobank is not trying to match [Immich](https://github.com/immich-app/immich)
feature for feature. It asks what should hold your photographic record when
both people and agents work with it.

One photograph may have a camera RAW, a JPEG, an XMP sidecar, and several edits.
Fotobank keeps those files related without treating them as interchangeable.
An album records a choice you made; a generated caption is a model's
interpretation. Neither replaces the original.

Editors get ordinary working files. Agents get documented operations and
inspectable results. Changes return as explicit new versions, not silent
replacements of the archive. Your record should outlast any editor, model, or
interface you use with it.

## What you can do today

- Import photos, videos, RAW files, and sidecars without modifying the source.
  Keep related files together and detect duplicate content.
- Browse photos, inspect details, organize albums, and manage privacy.
  Sharing with another person requires external identity and broker setup;
  the default single-user configuration does not publish shares remotely.
  See [sharing setup and limits](docs/guides/automation.md#manage-sharing).
- Edit selected files through [checkouts](docs/guides/checkouts.md): ordinary
  working copies whose tracked edits you explicitly commit as new versions.
- Search metadata without AI. Configure providers for optional tagging,
  captioning, and semantic search. Read the [AI guide](docs/guides/ai.md)
  for consent, costs, privacy, and stopping processing.
- Use the CLI and documented HTTP API to find photos, download verified
  originals and attachments, and manage albums without opening the database.
- Create complete recovery archives, schedule backups with retention, and
  test a restore into separate storage.

Check [file and preview support](docs/guides/formats.md) before a large import.
Storing an original does not guarantee a thumbnail or browser playback.

## Try a first library

There is no public tagged release yet. The [setup guide](docs/guides/setup.md)
explains how to build the current source, configure storage, and start the
server. Local folders under your own OS account are enough; you do not need a
NAS, a separate Docbank server, or user registration.

After setup, use the same configuration and OS account for these commands:

```sh
fotobank daemon start                 # prints the web UI URL
fotobank import /path/to/sample-photos
fotobank media list --limit 5 --json
fotobank media search "sunset" --json
```

Then [create a backup and test recovery](docs/guides/backup.md). If an agent is
doing the setup, follow the [setup and handoff procedure](docs/guides/agent-setup.md).
The [command reference](docs/guides/automation.md) covers structured results,
pagination, and limits.

## Built on Docbank

Docbank runs inside Fotobank as a Go library. It stores exact content and
immutable versions, and supplies source metadata and standard image previews.

Fotobank owns the photo catalog: file relationships, albums, privacy, sharing,
browsing, and working copies. Each file points to an exact Docbank version.
Recovery archives capture both systems' records because albums and file
relationships cannot be rebuilt from the original bytes alone.
See [how storage and editing work](website/guide.md) or the
[architecture map](docs/architecture/README.md).

## Related projects

Fotobank is one of several Kenn projects for keeping personal data in software
you run yourself. Docbank stores files. [msgvault](https://msgvault.io)
([source](https://github.com/kenn-io/msgvault)) stores messages. Each one has
commands and an API so your tools and agents can use it.

Optional AI and search currently run in Fotobank. Some of that may move into
Docbank later. People grouping, MCP integration, and other AI features are not
built. Fotobank has no autonomous assistant.
AI is disabled by default. Hidden-media access controls do not encrypt files,
and enabling AI requires acknowledging its hidden-media processing policy.

## Development

```sh
make test             # Go tests with the sqlite_fts5 tag
make test-short       # Short Go tests
make frontend-check   # Svelte checks and frontend unit tests
make test-e2e         # Playwright e2e suite
make lint             # Go lint and testify helper checks
make api-generate     # Regenerate OpenAPI and TypeScript schema
make docs-check       # Build the site and check links
```

## License

Copyright 2026 Kenn Software LLC. Licensed under the Apache License 2.0.
See [LICENSE](LICENSE) and [NOTICE](NOTICE).
