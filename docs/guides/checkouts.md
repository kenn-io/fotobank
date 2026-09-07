# Work with checkouts

A checkout is an ordinary writable copy for editors, file managers, and shell
tools. Docbank remains authoritative until you explicitly commit a settled
tracked edit.

## Estimate the copy

Keep `fotobank serve` running for every checkout command. Run these
commands on the server host under the same OS account, with the same stub-mode
configuration and application version. They use the local operator connection,
not the photo API, and never start a server or open a second vault.

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

## Create the working tree

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
Docbank versions and never hardlinks writable files to content-addressed blobs.

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

## Commit tracked edits

Wait for the server's scanner to mark the edits pending and check status.
Keep the server running. Do not edit the working files during
commit. Use the checkout identifier printed by `checkout create`:

```sh
fotobank checkout commit <checkout-uuid>
fotobank checkout commit <checkout-uuid> --json
```

Each changed tracked file becomes a new immutable Docbank version. Concurrent
changes become visible conflicts rather than overwriting newer authority.
Current writeback does not import new untracked files, apply deletions, infer
renames, or resolve conflicts.

Commit connects to the running server using local operator authentication. Run
it under the same OS account, with the same configuration and application
version as `serve`. This currently requires stub identity mode. The command
does not start the server or fall back to opening the vault when it is stopped.

JSON output contains `checkout_id`, `pending`, `committed`, and `conflicts`, with
an `error` when work fails. Some entries may commit before another fails; inspect
the counts even on a nonzero exit. If you cancel or lose the connection, inspect
`checkout status --json` before retrying. Committed versions remain committed;
retrying does not repeat an already completed entry.

Uncommitted working files
are excluded from archive backups, so commit edits before capturing an archive
that must include them. Checkout retirement and automatic reconstruction of
working trees are not yet exposed as commands.
