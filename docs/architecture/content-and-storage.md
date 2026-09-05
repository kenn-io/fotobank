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
Before the ready transition, the importer asks Docbank to ensure source
metadata for the exact primary version and stores Fotobank's query projection
with its version, extractor, and checksum fence. Thumbnail, full-text, and AI
work starts after the asset is ready.

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
- exact-version source-metadata processing and projection into dependency-free
  values;
- exact-version canonical visual-preview processing and verified reads;
- backup repository creation, verification, and isolated restore;
- bounded traversal of an owner's media subtree for reconciliation;
- error translation into Fotobank sentinels; and
- serialization of content mutations to bound local concurrency.

A coordinated backup declares the Fotobank SQLite snapshot as a host file and
provides the callback that creates it. Docbank runs that callback inside its
short metadata freeze, captures the host file in the same manifest as the
content snapshot, and releases content writers before immutable backup bytes
stream. Host files that contain credentials or tokens retain Docbank's
sensitivity marker and require an explicit plaintext-backup opt-in. Backup and
restore reports are projected into Fotobank types, and restore always targets a
separate vault root rather than replacing
the open authority. The adapter supplies Docbank with every configured NAS and
flash-managed root as protected storage, so restore rejects their descendants
and filesystem aliases before it creates or overwrites a target. One boundary
set retains each configured alias, its validated destination, and the target
resolved when the adapter opened. Restore, import, and checkout validation
resolve that complete set again before use, so a retargeted storage alias does
not expose either its earlier or current destination. Configuration validation
keeps canonical roots for ordinary I/O but also preserves absolute unresolved
versions of the paths the operator configured for this boundary.

`BackupRepository.Restore` can recover without an open source adapter. It
accepts protected storage roots from its caller and delegates restoration and
target coordination to Docbank. The product archive command supplies those
roots from configuration, restores into an empty separate target, and checks
the restored Fotobank catalog's references before reporting success. It does
not bootstrap a new source vault to recover an old one.

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
4. Ensure and project metadata from the exact primary version.
5. Make the asset ready and enqueue later projections only when every file is
   mapped and its source metadata projection is recorded.

Repeating the same create with the same path and identity is idempotent.
Different content at the reserved path marks the asset and all sibling
operations as a durable conflict. Receipts are rejected after conflict and the
asset cannot become ready. A later import of the same source identities resumes
pending operations. The content recovery command adopts matching creates left
across a process interruption, projects the recovered primary version before
completing a fully applied asset, and reports unmatched Docbank files without
changing them.

The owner routes serve the primary through `/api/v1/media/{asset}/original`
and attached RAW/XMP content through
`/api/v1/media/{asset}/files/{file}/content`. Both authorize through the asset
and read the recorded immutable Docbank version.

## Artifact storage

`internal/storage` is the rebuildable artifact boundary. Its filesystem store
writes beneath each owner's opaque directory in the NAS artifact root. An
optional local decorator caches versioned thumbnail keys under
`{flash.root}/thumbs`; it rejects non-thumbnail keys rather than becoming a
second route for original bytes.

Storage keys are relative POSIX paths. Absolute paths, backslashes, empty
segments, and traversal are rejected. Owner storage keys are one safe path
component. Writers require the configured NAS root to exist and create only
directories beneath an opened root-bound filesystem view; they never recreate
the external root during an outage.

This store currently carries only rebuildable thumbnails. Thumbnail keys
include the asset/media ID, thumbnail version, and requested size so
regeneration never silently reuses an old source projection. The NAS artifact
remains the backing copy when the local thumbnail cache is enabled.

For JPEG, PNG, GIF, WebP, and supported camera RAW originals, the thumbnail
worker asks Docbank to produce or reuse the canonical preview for the recorded
exact version. It validates that the preview belongs to the asset's primary
file, verifies the complete preview stream, and derives Fotobank's UI sizes
from those JPEG pixels. Unsupported and failed preview results become terminal
thumbnail outcomes rather than falling back to decoding the authoritative
original.

## Root isolation

The Docbank root must not overlap NAS or flash-managed trees in either
direction. Configuration canonicalizes existing symlinks, rejects
symlink-plus-`..` aliases, and compares case-insensitively for portable safety.
Normal commands reject unresolved symlink ancestors. Server validation may
resolve a NAS symlink through its missing external target so health endpoints
remain reachable, while still checking the intended target for overlap before
startup.

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
scan cancellation, while permission and I/O failures on tracked files become
visible entry errors rather than being treated as concurrent edits. Failures
reading untracked files are reported and retained for retry without preventing
missing-file reconciliation elsewhere. A traversal failure is contained to its
affected subtree: tracked files there become errors and pending untracked
candidates remain available for retry, while accessible parts of the checkout
still complete reconciliation. Untracked paths that cannot use the catalog's
portable slash-separated form are reported without entering the durable queue
or stopping reconciliation.

Stable untracked files remain in `checkout_scan_candidates` with an empty file
ID and `pending` state for the later new-file import lifecycle. The scanner
always skips its reserved staging directory. Operator-supplied
`checkouts.ignore_patterns` use Go `path.Match` syntax and apply only to
untracked files, so they can exclude tool-specific transient files without
hiding a tracked media edit.

Scanning does not write new Docbank versions. `fotobank checkout commit
<checkout-id>` consumes settled tracked `pending` entries. Each replacement is
bound to the Docbank node revision for the entry's exact base version, so a
newer head becomes a durable checkout conflict instead of being overwritten.

The pending checkout entry is also the recovery record across the Fotobank and
Docbank databases. If Docbank committed the expected bytes but Fotobank did not
record the receipt before interruption, the next commit adopts that exact
current node version. Any other current identity is a conflict. Applying a
receipt advances the product file and checkout base together. A primary-file
commit first ensures and projects source metadata for the new exact version;
processing failure leaves the checkout entry pending, so retry adopts the
already-written Docbank version and tries again. Successful publication stores
the new metadata fence, queues a thumbnail, invalidates current AI and vector
projections, and refreshes the lexical corpus in one Fotobank transaction
without deleting immutable Docbank history.
