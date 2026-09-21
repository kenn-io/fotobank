# Use Fotobank

Keep original photos and their versions, organize your library, and edit files
in your own tools. Fotobank runs on your machine and stores original files in
embedded [Docbank](https://docbank.ai/).

Start with [Set up Fotobank](guides/setup.md) to install it, import a small
collection, and open the web app. Then [create a backup and test recovery](guides/backup.md)
into separate storage.

## Fotobank 0.1.0

Read the [0.1.0 changelog](changelog.md#010) for new features, improvements,
and fixes. This is a pre-alpha release with no stability guarantees. Expect
bugs and changing interfaces and schemas. Keep independent copies of
irreplaceable photos.

The guides cover 0.1.0. Changes that require a newer development build are
identified separately; architecture pages describe the source revision being built.

## Choose a task

| I want to… | Guide |
| --- | --- |
| Try my first library | [Set up Fotobank](guides/setup.md) |
| Prepare a library for someone else | [Setup and handoff](guides/agent-setup.md) |
| Find photos and organize albums | [Browse and find photos](guides/browse.md) |
| Check file formats and preview limits | [Files and previews](guides/formats.md) |
| Import a folder or finish an interrupted import | [Import photos](guides/import.md) |
| Edit files in Lightroom or another tool | [Edit files in a checkout](guides/checkouts.md) |
| Back up the library or recover it | [Back up and restore](guides/backup.md) |
| Use scripts, agents, or the HTTP API | [Automate Fotobank](guides/automation.md) |
| Add optional tags, captions, or image search | [Choose whether to use AI](guides/ai.md) |

## Understand or change the code

The [architecture map](architecture/README.md) explains which component owns
each part of the system and links to the implementation details. The CLI and
web app use the same server; scripts and agents use its documented operations.

For a product overview, read the [storage and editing guide](/guide/).
To update these pages, follow [Maintaining the documentation](publishing.md).
