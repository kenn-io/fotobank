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

Create an empty directory outside every Fotobank-managed storage root, then run:

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

The running server scans active checkouts. A tracked file must remain unchanged
across the configured settle interval before it becomes pending for writeback.

## Commit tracked edits

Use the checkout identifier printed by `checkout create`:

```sh
fotobank checkout commit <checkout-uuid>
```

Each changed tracked file becomes a new immutable Docbank version. Concurrent
changes become visible conflicts rather than overwriting newer authority.
Current writeback does not import new untracked files, apply deletions, infer
renames, or resolve conflicts.
