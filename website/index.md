# A photo system of record you control

Fotobank is a self-hosted photo library. It keeps your original files and every
later version, keeps related files together, and lets you browse and organize
through a web app, the command line, or an agent.

[Build from source](/docs/guides/setup/) or
[set up with an agent](/docs/guides/agent-setup/).

Fotobank is pre-alpha software with no stability guarantees. Expect bugs and
changing interfaces and schemas. Keep independent copies of irreplaceable photos.

![Fotobank's library displaying landscape photos, with browsing filters and a capture-date timeline.](/images/library.jpg)

The running app with a sample library. [View full size](/images/library.jpg).
Sample photos from [Unsplash](https://unsplash.com).

## What you can do

Start with copies of a small collection.

### Browse and organize

Import reads your files without changing them. Find photos by date, camera,
tags, or location, and collect them into albums. Search by metadata does not
need AI. [Check supported files and previews](/docs/guides/formats/).

### Edit files with your own tools

A checkout is a working copy of selected files. Edit them with any program, then
commit the edits as new versions. Edits you have not committed are not in
backups. [Work with checkouts](/docs/guides/checkouts/).

### Use commands or an agent

List and search photos, read metadata, and manage albums from the command line
or the HTTP API. Both talk to the same server as the web app.
[Set up a first library](/docs/guides/agent-setup/) or
[read the command reference](/docs/guides/automation/).

### Turn on AI if you want it

AI is off by default. Tagging, captions, and semantic search need a configured
provider. [Read about providers, consent, and costs](/docs/guides/ai/) before
turning it on.

Before importing more, [back up and test recovery](/docs/guides/backup/).
Backups include stored files and the photo catalog. They do not include
configuration files, provider credentials, or checkouts. Backup repositories are
not encrypted.

## Related files stay together

A camera RAW, a JPEG, and an XMP sidecar can all belong to one photograph.
Fotobank records them as one photo with three files.
[Docbank](https://docbank.ai/) stores the exact bytes of each file and every
version, so an edit does not replace the earlier file.

For example, one photo record can connect `IMG_1042.JPG` (the display file),
`IMG_1042.CR2` (the camera source), and `IMG_1042.CR2.XMP` (the edit sidecar).

Docbank also reads source metadata and renders previews. It runs inside the
Fotobank process, not as a second server. Fotobank owns albums, privacy,
sharing, and the links between files. Restoring a library needs both.
[See how storage, editing, and recovery fit together](/guide/).

## Related projects

Fotobank is one of several Kenn projects for keeping personal data in software
you run yourself. [Docbank](https://docbank.ai/) stores files.
[msgvault](https://msgvault.io) stores messages. Each one has commands and an
API so your tools and agents can use it.

Optional AI and search currently run in Fotobank. Some of that may move into
Docbank later. People grouping, MCP integration, and other AI features are not
built. Fotobank has no autonomous assistant.

[Build from source and try a small collection](/docs/guides/setup/), or follow
the [agent setup guide](/docs/guides/agent-setup/).

[Fotobank on GitHub](https://github.com/kenn-io/fotobank) ·
[Join the Kenn community on Discord](https://discord.gg/nEB7VaAnU9).

Copyright 2026 Kenn Software LLC. Licensed under Apache-2.0.
