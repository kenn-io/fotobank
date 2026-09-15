# Work with checkouts

A checkout is an ordinary writable copy for editors, file managers, and shell
tools. To save an edit in Docbank, wait until Fotobank marks the file pending,
then run `checkout commit`.

## Estimate the copy

Every checkout command uses the daemon, starting it when needed. Run these
commands on the server host under the same OS account, with the same stub-mode
configuration and application version. They use the local operator connection,
not the photo listener, and never open a second vault.

To choose individual photos, use `media list --json` or `media search --json`,
then inspect each with `media show <media-uuid> --json`. Pass its `id` to
`--asset`. See [finding photos](automation.md#find-and-inspect-photos) for filters
and pagination.

Select assets, albums, capture years, or the complete visible library:

```sh
fotobank checkout estimate --year 2025
fotobank checkout estimate --album <album-uuid>
fotobank checkout estimate --asset <asset-uuid>
fotobank checkout estimate --all
fotobank checkout estimate --year 2025 --json
```

Selectors are repeatable and may be combined. Hidden assets are excluded. An
all-library checkout still requires an explicit byte limit at creation so it
cannot silently create a second full archive copy.

## Create the working folder

With the server running, create an empty directory outside every
Fotobank-managed storage root, then run:

```sh
mkdir -p /work/photos-2025
fotobank checkout create /work/photos-2025 --year 2025
```

For the full visible library:

```sh
fotobank checkout create /work/all-photos --all --max-bytes 500000000000
```

Do not open or edit the directory until creation finishes. Fotobank copies exact
Docbank versions. Each working file is a separate copy, so editing it does not
change the stored version.

Relative destinations are resolved from the command's working directory. The
server validates the destination against its own storage configuration.
On Windows, use a fully qualified path such as `C:\work\photos` or an ordinary
relative path such as `.\photos`; drive-relative and drive-less rooted paths
such as `C:photos` and `\photos` are rejected.

Use `--json` for `checkout_id`, `root`, selected `files` and `bytes`, and
`materialized` (files recorded so far). Errors exit nonzero and include `error`.
If creation fails after reserving a checkout, the result retains its ID so you
can inspect `checkout status <checkout-uuid> --json`. Partial working files are
not rolled back. If the connection is lost, inspect `checkout list --json` for
the destination before retrying; a lost response does not mean no files were
created. Creation is not automatically retried or resumed.

The server scans active checkouts while you edit.
A tracked file must remain unchanged across separate scans spanning
`checkouts.settle_interval` before it becomes pending for writeback. Scanning
never commits an edit by itself.

## Inspect working-copy state

List your checkouts and their file-state totals:

```sh
fotobank checkout list
```

Inspect one checkout's saved selection and the files that need attention:

```sh
fotobank checkout status <checkout-uuid>
```

The status view shows pending edits, conflicts, missing files, and scan errors.
Use `--json` with either command for structured output in scripts and agent
workflows. Both require the running server under the same OS account,
stub-mode configuration, and application version as other checkout commands.
They report saved catalog observations through the daemon; the CLI does not
open a database, scan, commit, rebuild, or remove working files. New untracked
files are not included in this status view.

For an edited file, find its `path` in `problems`. Wait for its `state` to be
`pending` and its `observed_sha256` to match the bytes you intend to commit.
`checkout.entries.pending` is only a count: editing an already-pending file does
not increase it, and zero does not prove that a recent edit has been scanned.
Check again after the configured scan and settle intervals, and stop on missing
files, conflicts, or errors.

## Commit tracked edits

Wait for the server's scanner to mark the edits pending and check status.
Keep the server running. Do not edit the working files during
commit. Use the checkout identifier printed by `checkout create`:

```sh
fotobank checkout commit <checkout-uuid>
fotobank checkout commit <checkout-uuid> --json
```

Each changed tracked file becomes a new immutable Docbank version. If the stored
base version has changed, Fotobank reports a conflict and keeps the newer version.
Current writeback does not import new untracked files, apply deletions, infer
renames, or resolve conflicts.

Commit connects to the running server using local operator authentication. Run
it under the same OS account, with the same configuration and application
version as `serve`. This currently requires stub identity mode. The command
starts a missing daemon before sending its request; it never falls back to
opening the vault itself.

JSON output contains `checkout_id`, `pending`, `committed`, and `conflicts`, with
an `error` when work fails. Some entries may commit before another fails; inspect
the counts even on a nonzero exit. If you cancel or lose the connection, inspect
`checkout status --json` before retrying. Committed versions remain committed;
retrying does not repeat an already completed entry.

To check what was saved, inspect the photo again and download it to a new path:

```sh
fotobank media show <media-uuid> --json
fotobank media download <media-uuid> --output /work/verified-photo.jpg --json
```

Compare the returned checksum with the edit you intended to save. The download
verifies the stored bytes; it does not read the working copy. See
[downloads](automation.md#download-an-original-or-attachment) for destination
requirements and retry behavior.

Uncommitted working files are excluded from archive backups. Commit edits before
capturing an archive that must include them. Automatic reconstruction of working
trees is not yet exposed as a command.

## Stop tracking a working folder

Retirement stops Fotobank scanning or committing a checkout. The working files
stay where they are; Docbank originals and versions are unchanged.

```sh
fotobank checkout retire <checkout-uuid>
fotobank checkout retire <checkout-uuid> --confirm --json
```

The first command only shows the last recorded status. Review pending edits,
conflicts, missing files and errors before confirming. This is not a fresh scan:
external edits may be newer than the recorded observations. Commit any edits
you want saved to Docbank before retiring; retirement does not save them.

The confirmed command uses the normal daemon and waits for in-flight checkout
work to finish. It retains the history in `checkout list` and `checkout status`,
including their JSON output, and releases the folder reservation. It can retire
a missing folder or an incomplete checkout, and repeating it is harmless.
There is no reactivation command. A new checkout still requires an empty folder;
Fotobank will not adopt or overwrite the files left by the retired checkout.
