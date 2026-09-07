# Work with checkouts

A checkout is an ordinary writable copy for editors, file managers, and shell
tools. Docbank remains authoritative until you explicitly commit a settled
tracked edit.

## Estimate the copy

Select assets, albums, capture years, or the complete visible library:

```sh
fotobank checkout estimate --year 2025
fotobank checkout estimate --album <album-uuid>
fotobank checkout estimate --asset <asset-uuid>
fotobank checkout estimate --all
```

Selectors are repeatable and may be combined. Hidden assets are excluded. An
all-library checkout still requires an explicit byte limit at creation so it
cannot silently create a second full archive copy.

## Create the working tree

Stop the Fotobank server and wait for it to exit: creation needs exclusive
ownership of the embedded Docbank vault. Create an empty directory outside
every Fotobank-managed storage root, then run:

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

Start the server again after creation. It scans active checkouts while you edit.
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
workflows. They may run alongside the server and do not open the Docbank vault.
They report saved catalog observations; they do not scan, commit, rebuild, or
remove working files. Startup still opens the normal database and ensures the
configured owner, so use them against an initialized deployment rather than as
strictly read-only database probes. New untracked files are not included in
this status view.

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
