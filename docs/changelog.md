# Changelog

See what changed in each Fotobank release. For installation and a first import,
start with [Set up Fotobank](guides/setup.md).

## 0.1.0

Fotobank 0.1.0 lets you keep related photo files together, edit tracked working
copies, and back up your library while the server runs. It is a pre-alpha
release with no stability guarantees. Keep independent copies of irreplaceable
photos and [test recovery](guides/backup.md#try-the-recovered-library).

Source: [v0.1.0](https://github.com/kenn-io/fotobank/tree/v0.1.0).

### New features

- Keep each photograph's primary file and related RAW or XMP files together.
  Imports store originals in Docbank, and photo details provide downloads for
  the primary file and attachments. See [files and previews](guides/formats.md).
- Create writable checkouts, tracked working folders for editing in Lightroom
  or other tools. Select photos, albums, or capture-year ranges, then explicitly
  commit edits as new versions. Fotobank reports conflicts instead of
  overwriting newer stored versions. See [working with checkouts](guides/checkouts.md).
- Inspect working folders for pending edits, conflicts, missing files, and
  errors. Retire a folder to stop tracking it without deleting its files.
  Retirement does not save uncommitted edits.
- Back up the catalog and original media together while Fotobank runs.
  Optional scheduling defaults to one archive every 24 hours and retains the
  latest 30 scheduled archives. Manual archives remain untouched. See
  [backup and restore](guides/backup.md).
- Inspect backups and restore an archive even when the original storage is
  unavailable. Start the server with `--recovery`, then restore into a separate
  empty directory. Restore verifies recorded file versions, checksums, and
  sizes; activating the recovered library remains a manual step. Archives
  exclude configuration, credentials, caches, and uncommitted working-folder edits.
- Start, stop, restart, and inspect Fotobank's background server with `daemon`
  commands. Start and restart print the web interface URL.
- Find and inspect photos from scripts with `media list`, `media show`, and
  `media search`. Filtering, pagination, and `--json` output support automation.
  See [automating Fotobank](guides/automation.md).
- Download originals and attachments with `media download`. Downloads verify
  size and checksum and refuse to replace an existing file. The destination
  directory must already exist and support hardlinks.
- Create an editable configuration with `fotobank config init`. Diagnose
  setup and storage problems with `fotobank config diagnose`, including JSON
  results and suggested actions, without changing data.

### Improvements

- Import photos, finish interrupted imports, refresh photo locations, and
  create or commit working folders without stopping Fotobank. Import sources
  remain local folders on the server's machine.
- Read structured JSON results for album management, owner registration,
  thumbnail regeneration, and AI queue and maintenance commands. Partial
  failures retain completed results where supported and return a nonzero exit code.
- Import WebP photos and view thumbnails derived from Docbank previews for
  JPEG, PNG, GIF, WebP, and supported camera RAW files.
- Search photo metadata without enabling AI. Changes to AI search settings
  apply to new queries without restarting the server; disabling AI search
  leaves metadata search available.
- Browse photos across the full phone screen and select individual photos by
  checkbox in Library, Sessions, Albums, and Hidden. Sharing forms and actions
  fit narrow screens.
- Keep search results in view with collapsible filters. Sorting and active
  filters remain visible, and filters remain removable while the panel is closed.
- Navigate search controls, album creation, share details, and confirmation
  dialogs by keyboard. Helper text and filter counts have more contrast against
  dark backgrounds.
- Find AI settings, Hidden photos, and import, editing, and backup guides
  from Settings. Empty libraries explain how to import a folder.
- Follow setup guides through a first import and a recovery check. The guides
  explain optional AI consent and provider configuration, preview limitations,
  and why the default single-user setup does not publish shares remotely.

### Bug fixes

- Load additional search results without repeating the first page.
  Pagination stops at the last page.
- Retry failed Library, Sessions, and Search requests without losing loaded
  photos, queries, or filters. Failed requests show errors instead of
  empty-result messages.
- Retry failed album actions without losing the selection. When removal
  partly succeeds, failed photos stay selected; a failed album deletion leaves
  the album open.
- Recover AI settings after a status-load failure with Retry. Failed saves no
  longer appear saved, and status outages no longer leave a healthy indicator visible.
- View ready thumbnails in Sessions instead of loading placeholders.
  Sessions no longer inherits Library filters.
