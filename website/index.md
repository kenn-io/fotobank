# A photo system of record you control

Your photographs hold family history, creative work, and moments you cannot
recreate. Fotobank brings them into a library you can browse, organize, and
work with through your own tools and agents.

[Build from source](/docs/guides/setup/) or
[set up with an agent](/docs/guides/agent-setup/).

Fotobank is pre-alpha software, with no stability guarantees. Expect bugs and
changing interfaces and schemas. Keep independent copies of irreplaceable photos.

![Fotobank's library displaying landscape photos, with browsing filters and a capture-date timeline.](/images/library.jpg)

The running app with a sample library. [View full size](/images/library.jpg).
Sample photos from [Unsplash](https://unsplash.com).

## Your library, your way of working

Start with copies of a small collection. Browse in the web app, work with files
in an editor, or use commands with an agent.

### Browse and organize

Import without modifying the source. Find photos by date, camera, tags, or
location, and collect them into albums. Metadata search works without AI.
[Check supported files and previews](/docs/guides/formats/).

### Edit ordinary files

A checkout is a working copy of selected files. Use your editor, then explicitly
commit tracked edits as new versions. Uncommitted edits are not in archive
backups. [Work with checkouts](/docs/guides/checkouts/).

### Work with an agent

List and search photos, inspect metadata, and manage albums through commands
and a documented HTTP API. They use the same server as the web app.
[Set up a first library](/docs/guides/agent-setup/) or
[read the command reference](/docs/guides/automation/).

### Choose whether to use AI

Optional tagging, captions, and semantic search require configured providers.
AI is disabled by default. [Read about providers, consent, and costs](/docs/guides/ai/)
before enabling it.

Before importing more, [back up and test recovery](/docs/guides/backup/).
Archives include stored files and the photo catalog, but not configuration
files, provider credentials, or working copies. Backup repositories are not encrypted.

## One photograph. Every file and version.

A camera RAW, a JPEG, and an XMP sidecar can belong to the same photograph.
Fotobank keeps them together in its catalog.
[Docbank](https://docbank.ai/) stores their exact bytes and
versions, so an edit does not replace the earlier file.

For example, one photo record can connect `IMG_1042.JPG` (the primary display
file), `IMG_1042.CR2` (the camera source), and `IMG_1042.CR2.XMP` (the edit
metadata sidecar).

Docbank also supplies source metadata and image previews. It runs inside
Fotobank, not as a second server. Albums, privacy, sharing, and file relationships
belong to Fotobank; recovery needs both systems' records.
[See how storage, editing, and recovery fit together](/guide/).

## Part of a personal OS for the agentic era

The idea is simple: keep the important parts of your life in systems you
control, with interfaces your chosen tools and agents can use. Fotobank is
the photographic part of that work, alongside
[Docbank](https://docbank.ai/) and [msgvault](https://msgvault.io).

Today, optional AI and search run in Fotobank. Moving reusable intelligence into
Docbank is work ahead. Photo relationships, albums, privacy, and sharing stay
in Fotobank.

Broader enrichment, people curation, and MCP integration are aspirations, not
shipped features. Fotobank has no autonomous assistant.

[Build from source and try a small collection](/docs/guides/setup/), or follow
the [agent setup guide](/docs/guides/agent-setup/).

[Fotobank on GitHub](https://github.com/kenn-io/fotobank) ·
[Join the Kenn community on Discord](https://discord.gg/nEB7VaAnU9).

Copyright 2026 Kenn Software LLC. Licensed under Apache-2.0.
