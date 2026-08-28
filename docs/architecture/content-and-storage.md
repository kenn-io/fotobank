# Content and Storage

## Authority model

Docbank is authoritative for every imported photo, video, RAW file, and XMP
sidecar. Fotobank SQLite stores product meaning and the exact Docbank node,
virtual path, version, SHA-256, and size for each file. NAS and flash storage
hold rebuildable artifacts and backups, not a second copy used as media
authority.

## Import semantics

Import is copy semantics. The importer leaves source paths and bytes untouched.
It observes size and modification time twice across `imports.settle_interval`,
then revalidates them immediately before creating content.

Related files are grouped before any reservation. A JPEG and camera RAW with
the same directory and normalized stem become one asset; an XMP becomes a
sidecar of the RAW when present, otherwise of the primary. XMP-only and
ambiguous groups are rejected. The importer computes SHA-256 and size, reserves
stable IDs and durable operations, creates each file in Docbank, records exact
receipts, and makes the asset ready only after every operation is applied.
Thumbnail, metadata, full-text, and AI work starts after that ready transition.

Docbank SHA-256 is the content identity. Fotobank decides separately whether
equal bytes mean a duplicate import, another file in an asset, or a distinct
product item. Content deduplication does not decide product identity.

## Docbank boundary

Only `internal/content` imports `go.kenn.io/docbank`. It owns:

- vault open and close;
- stable virtual-path construction;
- create requests with required expected SHA-256 and size;
- exact-version and current-version reads;
- error translation into Fotobank sentinels; and
- serialization of content mutations to bound local concurrency.

The embedded vault is configured with loose compression disabled and Fotobank
does not pack imported media. Callers still use public Docbank read APIs and
must not derive blob shard paths or depend on the physical representation.

Virtual paths are internal stable names:

```text
/owners/{owner-storage-uuid}/media/{file-uuid}/{source-basename}
```

The path is allocated before content creation. Public APIs expose the asset
UUID, never this path or the sequential Docbank node ID.

`internal/content` exposes sequential verified readers. A caller
must drain a full stream and check verification before treating the read as
successful. Exact-version range reads serve video and HTTP Range responses; a
partial range proves catalog access and range bounds, not whole-object
integrity.

## Cross-database writes

Fotobank SQLite and Docbank cannot commit atomically. Content-changing code
uses this durable operation protocol:

1. Allocate asset, file, and operation UUIDs and record the expected identity.
2. Call Docbank with the stable virtual path and expected content.
3. Record the returned node, version, SHA-256, and size in one Fotobank
   transaction.
4. Make the asset ready and enqueue projections only when every file is mapped.

Repeating the same create with the same path and identity is idempotent.
Different content at the reserved path marks the asset and all sibling
operations as a durable conflict. Receipts are rejected after conflict and the
asset cannot become ready. Pending-operation recovery and orphan reporting are
separate operational capabilities; pending rows remain durable until that
recovery path processes them.

## Artifact storage

`internal/storage` currently offers two implementations:

- NAS-only writes and reads beneath each owner's opaque storage directory.
- Flash-cache mode writes durable objects to NAS and keeps selected local cache
  entries under the flash root.

Storage keys are relative POSIX paths. Absolute paths, backslashes, empty
segments, and traversal are rejected. Owner storage keys are one safe path
component.

This store carries only rebuildable thumbnails and other Fotobank artifacts.
Thumbnail keys include the asset/media ID,
thumbnail version, and requested size so regeneration never silently reuses an
old source projection.

## Root isolation

The Docbank root must not overlap NAS or flash-managed trees in either
direction. Configuration canonicalizes existing symlinks, rejects unresolved
symlink ancestors and symlink-plus-`..` aliases, and compares case-insensitively
for portable safety.

The import command applies the same canonical, symlink-aware comparison to its
source root before discovery so the vault cannot become its own import source.

Writable checkouts for Lightroom are not implemented yet. Their architectural
boundary is already fixed: a checkout is a materialized working copy, never
authority, and must never hardlink writable files to Docbank's content-addressed
objects.
