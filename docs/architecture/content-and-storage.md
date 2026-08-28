# Content and Storage

## Authority model

Fotobank is moving from a NAS-path authority model to Docbank content
authority. The code is deliberately explicit about the transition:

- Today, the active importer writes full-size media through `storage.Store`,
  records a NAS-relative path in `media`, and current byte readers resolve that
  path. NAS is therefore still authoritative for the active product path.
- The server now also opens one embedded Docbank vault through
  `internal/content`. The replacement asset/file schema stores Docbank
  coordinates, but active imports and reads do not use them yet.
- The stable architecture stores every imported photo, video, RAW file, and XMP
  sidecar in Docbank. Fotobank keeps semantic metadata and cached content
  coordinates. NAS and flash storage remain for rebuildable artifacts and
  backups, not as a second media authority.

Architecture documentation must keep this section synchronized with the
actual cutover. Do not describe planned authority as already active.

## Import semantics

Import is copy semantics. Fotobank waits for a source file to settle, reads it,
and leaves the source path and bytes untouched. A future move operation would
be a separate, explicit feature.

The stable import flow groups related source files into one asset, computes
SHA-256 and byte size, reserves stable Fotobank IDs, writes each file to
Docbank, records the returned version mapping, then enqueues derived work.
Dependent thumbnail, metadata, full-text, and AI work starts only after the
asset has complete mappings.

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

Full reads use verified readers. A caller must drain the stream and check
verification before treating the read as successful. Exact-version byte ranges
support video and HTTP range responses; a partial range proves catalog access
and range bounds, not whole-object integrity.

## Cross-database writes

Fotobank SQLite and Docbank cannot commit atomically. Content-changing code
uses a durable operation protocol:

1. Allocate asset, file, and operation UUIDs and record the expected identity.
2. Call Docbank with the stable virtual path and expected content.
3. Record the returned node, version, SHA-256, and size in one Fotobank
   transaction.
4. Make the asset ready and enqueue projections only when every file is mapped.

Repeating the same create with the same path and identity is idempotent.
Different content at the reserved path is a durable conflict and is never
overwritten automatically. Recovery examines pending operations; unreferenced
UUID-bearing Docbank paths are reported as possible orphans.

## Artifact storage

`internal/storage` currently offers two implementations:

- NAS-only writes and reads beneath each owner's opaque storage directory.
- Flash-cache mode writes durable objects to NAS and keeps selected local cache
  entries under the flash root.

Storage keys are relative POSIX paths. Absolute paths, backslashes, empty
segments, and traversal are rejected. Owner storage keys are one safe path
component.

During the transition, this store still carries full-size active media and
thumbnails. In the stable architecture it carries only rebuildable thumbnails
and other Fotobank artifacts. Thumbnail keys include the asset/media ID,
thumbnail version, and requested size so regeneration never silently reuses an
old source projection.

## Root isolation

The Docbank root must not overlap NAS or flash-managed trees in either
direction. Configuration canonicalizes existing symlinks, rejects unresolved
symlink ancestors and symlink-plus-`..` aliases, and compares case-insensitively
for portable safety. Runtime import also rejects a source root that overlaps
the vault before discovery begins.

Writable checkouts for Lightroom are not implemented yet. Their architectural
boundary is already fixed: a checkout is a materialized working copy, never
authority, and must never hardlink writable files to Docbank's content-addressed
objects.
