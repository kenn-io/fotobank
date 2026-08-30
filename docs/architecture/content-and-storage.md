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

An owner can reserve a SHA-256 identity only once. Concurrent imports either
create that reservation or resume the committed winner. Re-running an import
after interruption reuses its asset, file, operation, and virtual-path IDs;
Docbank's idempotent create then returns the original receipt.

Docbank SHA-256 is the content identity. Fotobank decides separately whether
equal bytes mean a duplicate import, another file in an asset, or a distinct
product item. Content deduplication does not decide product identity.

## Docbank boundary

Only `internal/content` imports `go.kenn.io/docbank`. It owns:

- vault open and close;
- stable virtual-path construction;
- create requests with required expected SHA-256 and size;
- exact-version and current-version reads;
- bounded traversal of an owner's media subtree for reconciliation;
- error translation into Fotobank sentinels; and
- serialization of content mutations to bound local concurrency.

`internal/contentresolver` is the shared product-to-content boundary above the
adapter. It resolves a ready asset and either its primary or a named attached
file, then binds an immutable version to that file's recorded Docbank node.
Owner and shared-read services apply authorization and hidden-media policy
before opening the resolved reference. Projection workers use the same
resolver without transport authorization.

Current references use the version cached on the file and require Docbank's
SHA-256 and size to match the Fotobank projection. Historical references may
name any immutable version of the same Docbank node; a version belonging to a
different file is rejected even when the caller can access the asset. This is
the read primitive used by current downloads and projections and reserved for
later checkout materialization and rebuilds.

The embedded vault is configured with loose compression disabled and Fotobank
does not pack imported media. Callers still use public Docbank read APIs and
must not derive blob shard paths or depend on the physical representation.

Virtual paths are internal stable names:

```text
/owners/{owner-storage-uuid}/media/{file-uuid}/{source-basename}
```

The path is allocated before content creation. Public APIs expose the asset
UUID, never this path or the sequential Docbank node ID.

`internal/content` exposes sequential verified readers. The resolver converts
full-stream verification into ordinary read errors at EOF, so consumers must
drain a full stream and handle its terminal error before treating the read as
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
asset cannot become ready. A later import of the same source identities resumes
pending operations. The content recovery command adopts matching creates left
across a process interruption, completes fully applied assets, and reports
unmatched Docbank files without changing them.

The owner routes serve the primary through `/api/v1/media/{asset}/original`
and attached RAW/XMP content through
`/api/v1/media/{asset}/files/{file}/content`. Both authorize through the asset
and read the recorded immutable Docbank version.

## Artifact storage

`internal/storage` currently offers two implementations:

- NAS-only writes and reads beneath each owner's opaque storage directory.
- Flash-cache mode writes durable objects to NAS and keeps selected local cache
  entries under the flash root.

Storage keys are relative POSIX paths. Absolute paths, backslashes, empty
segments, and traversal are rejected. Owner storage keys are one safe path
component. Writers require the configured NAS root to exist and create only
directories beneath an opened root-bound filesystem view; they never recreate
the external root during an outage.

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
source root before discovery. The source cannot overlap the vault, NAS, or
flash root, so authoritative content and managed artifacts cannot become an
import source. Discovery traverses that canonical root and rejects supported
media paths that are symbolic links instead of following them beyond the
validated tree.

## Writable checkouts

A checkout is a materialized working copy for external tools, never content
authority. `fotobank checkout estimate` reports the distinct file and byte
count selected by explicit assets, albums, inclusive capture-year ranges, or
all ready visible assets. Hidden assets are excluded because the CLI has no
hidden-media unlock session. An all-assets checkout requires a caller-supplied
byte ceiling so a second full archive copy is never created implicitly.
Selector validation and candidate loading share one SQLite read transaction,
so an explicit asset cannot disappear between those two views.

`fotobank checkout create` requires an existing empty directory outside the
Docbank, NAS, and flash-managed roots. It resolves every selected file to its
recorded immutable Docbank version and publishes a verified ordinary copy with
an atomic no-replace operation inside a root-bound filesystem view. It never
hardlinks a writable file to a Docbank content-addressed object. Temporary
copies use a reserved top-level staging directory rather than a user filename
directory. Fotobank removes it before activation and syncs the checkout root on
platforms that support directory synchronization.

The content adapter opens a checkout root, verifies that exact directory
against the canonical path and storage boundaries, then returns an opaque
single-use capability retaining the open directory. Immediately before
transfer, the capability resolves the catalog path again, reapplies the Docbank,
NAS, and flash boundaries, and checks that the path still names the retained
directory at its original canonical path. Materialization uses that same handle
instead of reopening the root. A renamed, replaced, or newly aliased root is
rejected rather than leaving the catalog pointed at a different directory from
the materialized files. Once a checkout row exists, every later failure attempts
the `error`
transition through a short cleanup context independent of caller cancellation;
if that database write also fails, the returned error reports both failures.
Materialization repeats the retained-directory check before recording each
entry and before activation. A root moved during creation therefore leaves an
explicit errored checkout instead of an active ledger for a different path.
Every checkout command acquires a shared database lifetime lock before opening
or migrating SQLite and retains it until the connection pools close. Restore
requires the same lock exclusively, so database replacement cannot overlap an
estimate or materialization. Creation also holds its exclusive creation lock.
After acquiring the creation lock, the next creator marks any
remaining `building` rows for its owner as interrupted; a live creator cannot
be misclassified because it would still hold the lock. A `building` or `active`
checkout reserves its entire root tree: another checkout cannot use that root,
an ancestor, or a descendant. An operator can empty a partial interrupted
directory and retry it after recovery moves the old row to `error`.

The `capture_date` layout keeps every asset's related files together beneath
`YYYY/MM/DD/{asset-uuid}/`; assets without capture time use
`undated/{asset-uuid}/`. Original filenames must also be portable to Windows:
checkout rejects reserved device names, reserved characters, control
characters, and trailing dots or spaces on every host. It does not silently
rename originals because normalization can change tool-visible names or
collapse distinct source names onto one working path.

Each entry records its relative path, exact base version, SHA-256, size,
modification time, and platform file identity. Fotobank reopens the final
root-bound path and derives that observation from one file handle; a file
replaced during publication cannot be recorded as a clean entry. Here `clean`
means the last Fotobank observation matched the base version; it is not a
continuous claim about a writable file after that observation. The operator
must not open or edit the working root until checkout creation returns.

Checkout creation is `building` until every entry is published and then becomes
`active`. Selector and file identifiers are detached historical snapshots, so
deleting a source album or asset does not erase the checkout's saved selection
or file bindings. A materialization failure makes the durable checkout `error`
without pretending the partial working tree is usable.

The server periodically walks every active checkout. A complete scan is the
correctness path; filesystem notifications are not required. The scanner opens
the cataloged root through the same Docbank and managed-storage boundary used
for creation, then traverses it through a root-bound filesystem handle. It
never follows a working-file symlink as media.

Changed metadata starts or refreshes a durable settle observation. Size and
modification time and platform file identity must remain unchanged across
separate scans for at least `checkouts.settle_interval` before Fotobank opens
and hashes the file. The observation survives restart. The unchanged fast path
also requires a non-empty matching file identity; on a platform where identity
is unavailable, Fotobank hashes instead. A stable hash equal to the base
returns a tracked entry to `clean`, including timestamp-only edits; a different
hash changes it to `pending`. A missing working file changes the entry to
`missing` without deleting or modifying its Docbank version. Hashing observes
scan cancellation, while permission and I/O failures become visible entry
errors rather than being treated as concurrent edits.

Stable untracked files remain in `checkout_scan_candidates` with an empty file
ID and `pending` state for the later new-file import lifecycle. The scanner
always skips its reserved staging directory. Operator-supplied
`checkouts.ignore_patterns` use Go `path.Match` syntax and apply only to
untracked files, so they can exclude tool-specific transient files without
hiding a tracked media edit.

Scanning does not write new Docbank versions. A later checkout-commit boundary
consumes tracked `pending` entries with an atomic base-version precondition;
until then, Docbank remains unchanged and the durable queue records the local
work that is waiting.
