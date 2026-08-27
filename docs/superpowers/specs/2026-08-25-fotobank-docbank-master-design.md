# Fotobank on Docbank — Development Master Spec

**Status:** Draft v0.6
**Date:** 2026-08-25
**Scope:** Governing architecture and development sequence for rebuilding
Fotobank on Docbank as an embedded Go library. Each implementation stage below
is split into reviewable pull requests. Detailed implementation plans belong in
separate plan documents after this spec is approved.

This document supersedes the storage-authority, ordinary-NAS-tree, import
migration, checksum, and Lightroom-coexistence decisions in
[`2026-04-22-fotobank-vision.md`](./2026-04-22-fotobank-vision.md). Product
decisions from that document remain in force where they do not conflict with
this spec, including Fotobank ownership of photo semantics, albums, sharing,
authorization, and the web experience.

---

## 1. Context

Fotobank currently combines two responsibilities:

1. durable storage and organization of original media bytes; and
2. the photo application built around those bytes.

Docbank now provides the stronger foundation for the first responsibility: an
embedded content-addressed store, stable nodes, immutable content versions,
provenance, verified reads, trash, repair, packing, and multi-store storage
machinery. Rebuilding those capabilities independently in Fotobank would create
two competing storage platforms.

This is greenfield development. There are no deployed Fotobank databases or
legacy libraries to preserve. Existing Fotobank code is reusable implementation
material, not a compatibility contract. Schema, package, API, identifier, and
filesystem changes may be made directly when they improve the design. The
project will not add migration adapters, dual read/write paths, MD5 identity,
or compatibility aliases for the current implementation.

The main product constraint is Lightroom Classic. Lightroom needs ordinary,
writable files and may update XMP sidecars, modify writable image formats, make
DNG conversions, create exports, and rename files. Making that directory
authoritative would forfeit Docbank's immutable version and content authority.
Making it read-only would break the Lightroom workflow. Fotobank therefore
uses a third model: a writable, application-managed checkout.

## 2. Goals

The rebuild must provide:

- Docbank authority for original bytes, immutable versions, integrity,
  provenance, and physical storage lifecycle.
- Fotobank authority for photo assets, file relationships, ownership, albums,
  visibility, sharing, annotations, and product policy.
- A writable Lightroom checkout whose changes can be committed back into
  Docbank without exposing the content-addressed store to in-place mutation.
- Opaque Fotobank identifiers at every HTTP, album, search, and sharing
  boundary. Docbank's vault-local integer node IDs remain internal.
- Rebuildable EXIF, thumbnail, lexical-search, vector-search, and AI-result
  projections keyed to exact Docbank versions.
- Crash-recoverable coordination between the Docbank catalog and Fotobank's
  SQLite database without pretending they form one transaction.
- A measured storage design that does not silently require a second full copy
  of the complete archive.
- A sequence of independently testable pull requests across Fotobank and, only
  where required, Docbank.

## 3. Non-goals

The rebuild will not:

- preserve current Fotobank database rows, MD5 values, media UUIDs, filesystem
  paths, configuration keys, or HTTP response shapes solely for compatibility;
- make Docbank understand cameras, lenses, albums, photo pairing, visibility,
  sharing, or Lightroom;
- move Fotobank's sqlite-vec generation lifecycle or hybrid search engine into
  Docbank;
- make the pending Docbank document-conversion pull-request stack a dependency;
- use Docbank provenance reference strings as a relationship database;
- implement general-purpose two-way filesystem synchronization;
- hardlink writable checkout files to content-addressed objects;
- expose Docbank node IDs through public or shared URLs; or
- optimize full-archive checkout storage before measurements demonstrate the
  required topology.

## 4. Settled architectural decisions

### 4.1 Authority is divided by concern

| Concern | Authority |
|---|---|
| Logical media bytes and version history | Docbank |
| SHA-256 content identity and integrity | Docbank |
| Physical replicas, authority, repair, trash, and garbage collection | Docbank |
| Photo assets and relationships among RAW, JPEG, XMP, DNG, video, and exports | Fotobank |
| Owners, albums, hidden state, shares, and application annotations | Fotobank |
| Checkout coordinates and checkout conflict state | Fotobank |
| EXIF, thumbnails, captions, tags, embeddings, and search indexes | Rebuildable Fotobank projections |
| Lightroom-visible files | Mutable checkout derived from both authorities |

Neither database is a replica of the other. Each owns a different set of
facts. A backup is complete only when it contains both authorities.

### 4.2 Docbank is embedded

Fotobank opens one `docbank.Vault` in process and owns its lifetime. It does not
run a second daemon or call a Docbank CLI. One vault serves one Fotobank
deployment. Fotobank's service layer remains the only authorization boundary;
Docbank receives already-authorized operations.

One internal Fotobank adapter is the sole package allowed to use Docbank's
public API. The adapter owns:

- vault lifecycle;
- the Docbank virtual-path convention;
- serialized content mutations;
- conversion between Fotobank IDs and Docbank nodes/versions;
- idempotent mutation receipts and recovery; and
- translation of Docbank errors into Fotobank domain errors.

No transport, worker, repository, or product service calls `docbank.Vault`
directly.

### 4.3 The checkout is a worktree, not storage authority

A checkout is a materialized working view comparable to a source-control
worktree:

- materialization records the exact Docbank version used as its base;
- an unchanged file may be discarded and recreated at any time;
- a changed file is committed as a new immutable Docbank version;
- a newly created file is imported into a new Docbank node and associated with
  a Fotobank asset;
- a missing checkout file does not delete authoritative content;
- a stale-base change becomes an explicit conflict and is never silently
  overwritten; and
- rebuilding into an empty directory must reproduce every clean checkout entry.

This is not generic synchronization. Fotobank owns both the checkout manifest
and every commit operation.

### 4.4 Forward-only development

Schema changes continue to edit `000001_initial_schema.{up,down}.sql` in place.
There is no data migration. Each pull request must leave its repository
buildable and testable, but it need not retain an obsolete storage or API path.
When a new path becomes authoritative, the replaced path is removed in the same
pull request.

## 5. System architecture

```text
 Lightroom Classic                       Browser / API / CLI
        │ writable files                           │
        ▼                                          ▼
 ┌──────────────────┐                    ┌──────────────────────┐
 │ Fotobank checkout│                    │ Fotobank transports  │
 │ ordinary tree    │                    └──────────┬───────────┘
 └────────┬─────────┘                               │
          │ scan, settle, reconcile                 ▼
          ▼                              ┌──────────────────────┐
 ┌──────────────────┐                   │ Fotobank services    │
 │ checkout manager │◄─────────────────►│ authorization/policy │
 └────────┬─────────┘                   └───────┬──────────────┘
          │                                     │
          └─────────────────┬───────────────────┘
                            ▼
                 ┌──────────────────────┐
                 │ Fotobank SQLite      │
                 │ assets, files,       │
                 │ relationships,       │
                 │ checkout, jobs,      │
                 │ albums, shares       │
                 └──────────┬───────────┘
                            │ opaque file ID ↔ node/version
                            ▼
                 ┌──────────────────────┐
                 │ Docbank adapter      │
                 │ sole embedded caller│
                 └──────────┬───────────┘
                            ▼
                 ┌──────────────────────┐
                 │ Embedded Docbank     │
                 │ catalog + CAS        │
                 └───────┬──────────────┘
                         │ physical placement
                   ┌─────┴─────┐
                   ▼           ▼
              flash landing   NAS authority
```

The checkout and Docbank vault roots must not overlap. Operators must never
point a Fotobank import at a Docbank internal storage directory.

## 6. Domain and identity model

### 6.1 Media assets

A **media asset** is the product object shown in the library, placed in albums,
indexed, hidden, or shared. Its ID is a randomly generated opaque UUID. Albums,
shares, routes, jobs, and search results refer to this ID.

An asset owns one or more **media files**. A file is a stable logical file slot
backed by one Docbank node and its immutable version history. Each file also has
an opaque Fotobank UUID. The initial file roles are:

- `primary` — the default display/download representation;
- `original` — an additional camera/source representation;
- `sidecar` — metadata that describes another file, initially XMP; and
- `alternate` — another representation of the same asset, such as a DNG.

Every ready asset has exactly one `primary` file. A RAW-only import uses the
RAW as primary; a grouped camera JPEG is preferred when present; otherwise the
single DNG, video, or supported source file is primary. A pending import may
temporarily have no primary, but it is not listable until the asset transaction
selects one.

The schema stores explicit file relationships:

- `sidecar_of`: source file contains metadata for target file;
- `derived_from`: source file was produced from target file; and
- `paired_with`: source and target are peer camera representations when neither
  is strictly derived from the other.

Relationship direction is significant. Product code validates that both files
belong to the same owner. The relationship table, not Docbank provenance, owns
these facts.

Files from the same shutter event or source basename normally belong to one
asset. A newly generated DNG belongs to its source asset and records
`derived_from`. An unrecognized new JPEG export becomes a new asset unless the
import operation has concrete source information that establishes a relation.

### 6.2 Docbank mapping

Each media file records:

- `docbank_node_id` — vault-local integer, unique within the deployment;
- `docbank_virtual_path` — stable internal coordinate;
- `current_version_id` — cached current Docbank version for fast projection
  invalidation;
- current SHA-256, byte count, and media type as cached projections; and
- original filename and import source information as Fotobank metadata.

Docbank remains authoritative if a cache disagrees. Reconciliation repairs the
Fotobank projection from `Vault.Stat` and version information.

The stable virtual path is:

```text
/owners/{owner-storage-key}/media/{file-uuid}/{sanitized-original-basename}
```

`owner-storage-key` is an immutable, path-safe, opaque UUID assigned when
Fotobank first registers an owner and stored in the `owners` row. It is never
derived from a mutable handle, hub, or user ID. Stub mode may supply the value
explicitly for deterministic development; otherwise owner registration
generates it. Re-registering an owner with a different key is an error.

Checkout paths and user-visible names never change this coordinate. Allocating
the file UUID before calling `Vault.Create` makes creation idempotent and makes
orphan recovery deterministic.

### 6.3 Public identity

Docbank node IDs and virtual paths never appear in public API payloads or URLs.
The Fotobank asset UUID is the public media identity. File UUIDs are exposed
only where the product explicitly addresses an alternate or sidecar. Public
sharing uses its own opaque share capability; knowing an asset UUID grants no
access by itself.

## 7. Cross-database mutation protocol

Docbank and Fotobank SQLite cannot commit atomically. Fotobank therefore uses
an operation ledger and idempotent steps instead of a distributed transaction.

Every content mutation follows this pattern:

1. Allocate stable Fotobank IDs and record a pending operation in Fotobank
   SQLite.
2. Compute the source SHA-256 and size.
3. Call Docbank with the stable virtual path and expected content identity.
4. Record the returned node ID, version ID, identity, and completed operation
   state in one Fotobank transaction.
5. Enqueue dependent projection work only after the mapping is complete.

On restart, the recovery worker examines pending operations:

- If Docbank contains the expected path and identity, recovery adopts the
  receipt and completes the Fotobank transaction.
- If Docbank has no node, recovery retries creation.
- If the path has different authority, recovery records a conflict and does
  not overwrite it.
- A Docbank node whose UUID-bearing virtual path has no Fotobank row is an
  orphan. Reconciliation either completes its matching pending operation or
  reports it for explicit cleanup.

The adapter serializes Docbank content mutations to bound local concurrency, but
that mutex is not the stale-write correctness boundary. F08 depends on D01 and
passes the checkout's base revision or version into one conditional Docbank
`Put`. Docbank validates that precondition in the same catalog transaction that
publishes the new version. An earlier Fotobank read may report an obvious
conflict sooner, but a race after that read still fails the conditional `Put`;
correctness never depends on a caller holding an application mutex across
separate calls.

## 8. Import flow

Import is copy semantics. Source files remain untouched unless a later, separate
feature explicitly introduces move semantics.

For each stable source file, Fotobank:

1. before discovery, canonicalizes the source root and Docbank root through
   their deepest existing filesystem ancestors and rejects overlap in either
   direction, including overlap through symlink aliases;
2. waits until size and modification time remain unchanged for the configured
   settle interval;
3. computes SHA-256 and byte count;
4. extracts enough local metadata to group related files and choose an asset;
5. allocates asset/file IDs and a pending operation;
6. calls `Vault.Create` with required expected identity and filesystem-source
   provenance;
7. commits the mapping and relationship rows;
8. queues EXIF, thumbnail, and search/AI projections against the returned
   version ID; and
9. optionally materializes the file into selected checkouts.

Docbank's SHA-256 is the only durable content identity. Fotobank does not
compute or retain MD5. Deduplication follows Docbank's content identity while
Fotobank separately decides whether identical bytes represent the same product
asset, a new relationship, or a duplicate import to reject.

## 9. Checkout model

### 9.1 Checkout catalog

A checkout has an opaque ID, owner, absolute root, selection policy, layout
policy, and lifecycle state. The selection policy initially supports:

- explicit assets;
- albums;
- capture-year ranges; and
- all assets, as an explicit capacity-consuming option.

Capture-year selection uses the normalized asset capture time produced by the
EXIF projection. A newly imported asset does not enter a year-selected checkout
until extraction commits that time; checkout reconciliation then materializes
it. Assets without a capture time remain absent from year ranges but are still
reachable through explicit, album, and all-assets selections.

Partial checkout is the default. It avoids silently storing a second full copy
of the archive. An operator who selects all assets must receive an estimated
byte count and explicitly accept the required space.

Each checkout entry records:

- checkout ID and media-file ID;
- relative working path;
- base Docbank version ID and SHA-256;
- last observed size, modification time, and SHA-256;
- state; and
- the last error or conflict record, if any.

The entry states are:

- `clean` — working bytes match the recorded base version;
- `pending` — a settled local change is queued for commit;
- `conflict` — the local base is stale or the destination relationship is
  ambiguous;
- `missing` — the working file was removed, while authoritative content remains;
  and
- `error` — processing failed and is retryable or requires operator action.

### 9.2 Materialization

The baseline materializer writes to a temporary file in the checkout directory,
flushes and closes it, then renames it into place. It uses a verified Docbank
read and records the exact version only after publication succeeds.

Writable checkout files must never be hardlinks to Docbank objects. A hardlink
would let Lightroom change the content-addressed inode in place. Ordinary copies
are always valid. Reflinks are valid because writes copy on write, but they may
be used only through a future Docbank materialization API that proves the source
is a raw loose object on the same compatible filesystem and falls back to a
copy. Fotobank must not derive or open Docbank's internal hash-shard paths.

Embedded Docbank loose compression remains disabled for photo and video
originals. Fotobank does not call `Vault.Pack` for them. This keeps physical
storage predictable, but the checkout remains correct if a blob is remote,
compressed, or packed because the baseline uses the public verified read API.

### 9.3 Change detection

Filesystem notifications are an optimization, not authority. Network shares
and process restarts can lose events. A periodic scanner compares the checkout
tree with the catalog and settles changes before reading them.

The scanner:

- ignores Fotobank staging files and configured Lightroom transient files;
- waits for stable size and modification time;
- hashes only a settled candidate;
- treats an unchanged hash as clean even if timestamps changed;
- records a missing entry without deleting content;
- discovers untracked files as candidate imports; and
- persists enough progress to resume after restart.

### 9.4 Committing a changed file

For a settled change to a tracked file, the checkout manager:

1. hashes the working bytes;
2. calls one adapter operation with the checkout entry's base revision/version,
   the expected new content identity, and the working bytes;
3. the adapter calls D01's conditional Docbank `Put`, which validates the base
   and publishes the new version atomically in Docbank;
4. records `conflict` and stops if Docbank reports a stale base;
5. atomically updates the Fotobank file cache and checkout base to the returned
   version; and
6. invalidates and queues every version-sensitive projection.

An XMP auto-write therefore becomes a new version of the existing XMP file
node. A writable JPEG or DNG modification becomes a new version of that file
node. Prior versions remain available in Docbank.

### 9.5 New files, renames, and removals

- A new XMP whose basename uniquely matches a checked-out media file becomes a
  sidecar in the same asset and records `sidecar_of`.
- A new DNG produced beside a source RAW becomes an alternate in the same asset
  and records `derived_from` when the source is unambiguous.
- Any other new supported media file enters the normal import flow as a new
  asset.
- A rename event updates the checkout relative path only. It never renames the
  Docbank virtual path.
- If a native rename event was lost, a unique unchanged content identity may
  recover the rename during reconciliation. Ambiguous matches become conflicts.
- Removing a checkout file records `missing`. Explicit product deletion is a
  separate authenticated operation that trashes the Docbank node and updates
  Fotobank semantics through the operation ledger.

### 9.6 Rebuild guarantee

Given restored Docbank and Fotobank authorities, `fotobank checkout rebuild`
must recreate a selected checkout in an empty directory. It must not overwrite
untracked or locally changed files in a non-empty directory. A dry run reports
creates, unchanged entries, conflicts, missing authoritative content, and
required bytes before mutation.

## 10. Storage topology and cost

### 10.1 Thin-slice topology

The semantic thin slice uses an embedded vault rooted on local scratch storage
and a copy-based partial checkout. The vault may use the intended
`<flash.root>/docbank` directory, but its canonical path must remain disjoint
from the NAS root and every admitted import source. That storage topology needs
no Docbank placement or authority-transfer change; Milestone 1 separately
requires D02 for video reads. The data is disposable and is not treated as the
production archive.

### 10.2 Production topology

The production target is:

- Fotobank SQLite and Docbank catalog on local flash;
- new loose content durably landed on flash;
- authoritative content replicated to NAS;
- authority transferred to the NAS copy; and
- the flash landing copy retired or retained only under an explicit cache
  policy.

Docbank already contains storage-placement machinery, and embedded configuration
can bind filesystem or S3 secondaries. Its public embedded API does not yet let
Fotobank register placement intent, replicate authority, transfer authority, or
retire the primary copy. Those operations must be exposed in Docbank before
irreplaceable production imports begin. Fotobank will consume the public
operations rather than depend on Docbank internals.

### 10.3 Required measurements

Before finalizing placement batch sizes and worker concurrency, the project
runs two repeatable workloads on the intended hardware:

1. import 10,000 representative photo/video files, reporting source bytes,
   elapsed time, files per second, bytes per second, and latency percentiles;
2. apply repeated Lightroom-style XMP updates, reporting commit latency,
   catalog growth, version count, and recovery after interruption.

The same import workload runs against local flash and the actual NAS path. The
benchmark records direct durable copy time as context; it does not weaken
Docbank's durability guarantees to reach an arbitrary speed target. Results and
the chosen concurrency/batch configuration are committed as a development
report.

The current embedded vault serializes each complete `Create` and `Put`,
including blob streaming and durable publication, under its mutation mutex.
Fotobank import workers can overlap source hashing and metadata extraction, but
their vault writes queue behind one active file. The workload therefore records
preprocessing time, time queued for the vault, and time inside the vault
separately. Worker concurrency is tuned only for preprocessing unless Docbank's
write contract changes.

If serialized vault publication is a material bottleneck in the recorded
deployment workload, D07 performs a separate Docbank design and implementation
to permit safe concurrent content-addressed staging/publication while retaining
serialized catalog authority. The master spec does not assume that moving the
mutex is safe: cleanup, deduplication, packing exclusion, and receipt recovery
must be resolved in that Docbank design.

### 10.4 Checkout capacity policy

The supported baseline is a partial ordinary-copy checkout. Full copy is
supported when the operator accepts the estimate. A reflink optimization is a
separate Docbank/Fotobank pair of pull requests and is justified only when all
of these are true:

- full or large checkouts are required;
- the authoritative filesystem supports reliable copy-on-write cloning;
- the checkout and selected authority are on that filesystem; and
- measured duplicate storage is operationally material.

No hardlink mode will exist.

## 11. Derived metadata, thumbnails, and search

Photo-specific processing stays in Fotobank:

- EXIF extraction and normalized camera/lens/exposure/GPS projections;
- RAW preview resolution;
- thumbnails and video posters;
- user annotations and AI-generated tags/captions;
- sqlite-vec generation management; and
- lexical/vector hybrid search.

Every derived row or artifact records the source Docbank version ID and its own
generation/fingerprint. A content-version change makes older derivatives stale
and queues replacements. Serving code uses only derivatives that match the
current file version and active generation. Missing derivatives are a
performance or presentation event, not content loss.

Thumbnails remain outside Docbank initially because they are rebuildable and
Fotobank-specific. A future shared derivative service requires a demonstrated
second consumer and its own design.

## 12. HTTP, video, and sharing

Fotobank services authorize an opaque asset ID, resolve its selected media file,
and then ask the Docbank adapter for bytes. Routes never accept a node ID or
Docbank path.

Photo responses use the current Docbank version's SHA-256 as the strong content
ETag. Hidden and shared-media cache policy remains a Fotobank authorization
concern. The ETag is emitted only after Fotobank authorizes the original-byte
request. It is a cache validator, not a secrecy boundary; any caller allowed to
receive it is also allowed to receive and hash the same bytes.

Video playback requires byte ranges. Docbank's current embedded content reader
is sequential and verified but does not expose range reads or random access.
Docbank must add a catalog-authorized version range operation that works across
raw loose, packed, and secondary representations. The operation always returns
the requested logical decoded byte range. Raw loose content uses native offset
reads. Compressed loose and packed representations may decode from the beginning
or materialize a temporary seekable representation; the API does not promise
native random access for those representations. Fotobank originals keep
compression and packing disabled, so their normal path does not pay that
fallback cost. Fotobank must not bypass the catalog by opening physical files
directly.

Albums and shares target Fotobank asset IDs. File-level alternates are resolved
only after the caller is authorized for the asset. Share capability IDs remain
separate from asset IDs.

## 13. Backup, restore, retention, and deletion

Before production data is entrusted to the system:

- Docbank must expose its backup lifecycle through the embedded library;
- Fotobank must snapshot its SQLite authority;
- one Fotobank backup command must guarantee that every Docbank version
  referenced by the Fotobank snapshot exists in the Docbank snapshot; and
- a manifest must bind the Fotobank snapshot, Docbank snapshot, vault identity,
  schema versions, and creation time.

The backup command establishes a destructive-operation fence that blocks
product trash, version pruning, trash empty, and garbage collection for the
complete coordinated capture. It holds the general application mutation gate
only long enough to capture an immutable Fotobank SQLite snapshot, then releases
ordinary append-only imports and version writes while Docbank takes its short
metadata freeze and streams the archive. A later Docbank snapshot may therefore
be a logical superset of the Fotobank snapshot, but it cannot omit or destroy a
version referenced by Fotobank. The coordinated manifest is published before
the destructive-operation fence is released.

Restore is proven only by restoring both authorities into an empty location,
opening the vault, verifying referenced content, and rebuilding a checkout.
Whole-vault content scrubbing uses Docbank's existing bounded `Vault.Verify`
operation until its report says no further page remains; backup-repository
verification remains part of D04. Embedded restore verification proves blob
content integrity for catalog-authorized versions; it does not claim the
daemon-only whole-catalog metadata validation contract.

V1 retains every XMP and media content version. XMP churn is measured, but
version pruning is not a prerequisite unless the measurement shows material
operational growth. If pruning becomes necessary, Docbank's existing pruning
semantics should be exposed through the embedded API in a separate pull request.

Checkout removal never deletes content. Product deletion first removes or
marks Fotobank semantics, then trashes Docbank nodes through a recoverable
operation. Permanent garbage collection remains a deliberate Docbank
maintenance action subject to backup and retention policy.

## 14. Errors, conflicts, and observability

Errors crossing the Docbank adapter are mapped by identity, not by text.
Fotobank distinguishes at least:

- missing node/version;
- stale base revision/version;
- content identity mismatch;
- existing-path content conflict;
- unavailable physical content;
- checkout path collision;
- ambiguous relationship/rename;
- insufficient checkout capacity; and
- pending operation requiring recovery.

A checkout conflict is durable product state, not a log line. CLI and admin HTTP
surfaces list conflicts and provide explicit resolution operations: keep local
as a new version after rebase, discard local and rematerialize, or import local
as a separate file/asset. No automatic last-writer-wins resolution exists.

Metrics cover import throughput, Docbank write latency, placement backlog,
checkout scan/commit latency, pending operations, conflicts, missing content,
projection backlog, and backup age. Logs include opaque Fotobank IDs and
operation IDs; they avoid original filesystem paths unless diagnostic logging
is explicitly enabled.

## 15. Validation strategy

### 15.1 Contract tests

The Fotobank Docbank adapter has integration tests against a real embedded
vault. Fakes may test service policy but cannot establish storage behavior.
Contract tests cover create idempotency, exact-version reads, conditional
replacement, stale-base rejection, restart recovery, trash, and unavailable
content.

### 15.2 Checkout tests

Filesystem tests cover:

- copy materialization and exact base recording;
- XMP replacement as a new version;
- DNG creation and `derived_from` relationship;
- a true stale-base conflict;
- interrupted publication and restart recovery;
- lost notification recovered by scanning;
- rename recovery by unique content identity;
- ambiguous rename conflict;
- missing working file without content deletion;
- refusal to overwrite an untracked destination; and
- rebuild from an empty directory.

Tests must also prove checkout files do not share an inode with loose CAS
objects on platforms where inode identity is available.

### 15.3 Product regression tests

Existing albums, hidden-media behavior, shares, thumbnails, search, and AI
features are adapted to the asset/file model. Their tests assert product
behavior through Fotobank services and transports, never by reaching into
Docbank internals.

### 15.4 Scale and manual acceptance

The 10,000-file and XMP-churn workloads are repeatable benchmark commands, not
timing assertions in ordinary CI. CI covers their small deterministic form.

Before declaring the checkout usable, a real Lightroom catalog must:

1. open a materialized representative library;
2. save XMP metadata repeatedly;
3. create a DNG;
4. rename a file;
5. experience and resolve a deliberately created stale-base conflict; and
6. reopen a checkout rebuilt from empty.

The acceptance record includes observed filesystem behavior and any Lightroom
settings required for reproducibility.

## 16. Pull-request program

Prefixes identify the target repository: **F** is Fotobank and **D** is
Docbank. During alpha integration, a Docbank pull request lands before its
dependent Fotobank work pins that exact commit as a Go pseudo-version. Floating
branches, local replacements, and unmerged revisions are not dependency
boundaries. Once the dependent milestone gate demonstrates that the public API
is sufficient, Docbank tags the accepted revision and Fotobank moves to that
tag before real data is entrusted to the system. Provider/conversion work from
the existing Docbank stack is not part of this dependency graph.

Each pull request has one reviewer-visible outcome, focused tests, updated docs
where behavior changes, and no compatibility layer for the replaced design.

### Milestone 0 — Governing design

| PR | Outcome | Depends on |
|---|---|---|
| F00 | Commit this master spec and establish it as the rebuild authority. | — |

**Gate:** The master spec is approved before implementation plans are written.

### Milestone 1 — Content substrate and greenfield domain

| PR | Outcome | Depends on |
|---|---|---|
| F01 | Add the Docbank module, vault configuration/lifecycle, and the single internal adapter with real-vault integration tests. No product path writes content yet. | F00 |
| F02a | Add and test the final-shaped opaque asset, media-file, relationship, and cached Docbank-mapping schema/domain repositories. The new model is not yet used by product writes, so this additive review slice introduces no dual persistence or legacy storage fields. | F01 |
| D02 | Expose catalog-authorized exact-version logical byte ranges with the raw/packed/compressed behavior defined in §12. | — |
| F03 | Atomically cut existing foreign keys, product consumers, import writes, and all current-original reads—including video ranges—to the asset/file model and Docbank authority. Reject canonical or symlink-aliased overlap between import sources and the vault before discovery. Add SHA-256 identity, stable virtual paths, and pending-operation receipts; use Docbank SHA-256 for content ETags. Remove the superseded one-row-per-file schema, original-byte storage path, and MD5 identity in the same PR. | F02a, D02 |
| F04 | Add exact-version reads and the shared asset/file/version resolver used by checkout rebuilds and projection workers. | F03 |
| F05 | Add pending-operation restart recovery and orphan reconciliation for create/import operations. | F03 |

F03 deliberately owns both the active consumer cutover and the authority
cutover. Making those separately mergeable would require the new file model to
carry the legacy storage path or MD5 identity between pull requests, creating
the compatibility state this greenfield rebuild forbids. F02a absorbs as much
final-shaped inactive repository work as is useful; F03 may use reviewable
commits internally, but it lands as one forward cutover.

**Gate:** A fresh deployment imports a representative RAW/JPEG/XMP set into
Docbank, restarts at injected operation boundaries, and serves verified photo
bytes plus video byte ranges without the legacy storage implementation.

### Milestone 2 — Writable checkout thin slice

| PR | Outcome | Depends on |
|---|---|---|
| F06 | Add checkout and checkout-entry persistence, selection policies, capacity estimates, and copy materialization. | F04 |
| F07 | Add the periodic settle/scan/reconcile engine; notifications may enqueue scans but are not required for correctness. | F06 |
| D01 | Add caller-supplied revision or current-version preconditions to embedded `Put`, evaluated atomically with catalog version publication, with a stale-base contract and tests. | — |
| F08 | Commit tracked file modifications through one adapter operation backed by D01's conditional Docbank `Put`, then invalidate version-sensitive projections. | F07, D01 |
| F09 | Import new XMP and DNG files and create `sidecar_of`/`derived_from` relationships. | F08 |
| F10 | Persist checkout conflicts, missing entries, and explicit conflict-resolution operations. | F08 |
| F11 | Preserve checkout renames, recover lost rename events by unique identity, and add dry-run plus rebuild-from-empty commands. | F09, F10 |

**Gate:** The thin slice passes the real Lightroom sequence in §15.4 using
disposable local storage. A stale-base test must change Docbank authority after
materialization and prove the local checkout is not silently committed.

### Milestone 3 — Measurement and production storage

| PR | Outcome | Depends on |
|---|---|---|
| F12 | Add repeatable 10,000-file and XMP-churn workloads plus a committed result/report format. | F11 |
| D07 (conditional) | Redesign embedded content writes for measured parallel publication without weakening deduplication, cleanup, maintenance exclusion, or receipt recovery. | F12 evidence that serialized vault publication is a material bottleneck |
| D03a | Expose embedded placement inventory and dry-run planning with stable resumable work coordinates. | — |
| D03b | Expose embedded replication, authority transfer, primary retirement, and interrupted-operation recovery. | D03a |
| F14 | Configure flash landing plus NAS authority, run placement jobs, expose backlog/readiness, and recover interrupted handoffs. If F12 triggers D07, consume its released API before setting production concurrency. | F12, D03b; D07 when triggered |
| D04 | Expose Docbank backup create/list/verify/restore through its embedded public API while preserving the short metadata freeze and permitting append mutations during long content streaming. | — |
| F15 | Add coordinated Fotobank+Docbank backup manifests and prove restore into an empty deployment. | F05, D04 |

**Gate:** Representative content is imported through the production placement
path, served after the flash landing copy is absent, verified from NAS
authority, backed up with Fotobank semantics, and restored into an empty
deployment. No irreplaceable archive is imported before this gate passes.

### Milestone 4 — Rebuild the photo product on versioned assets

| PR | Outcome | Depends on |
|---|---|---|
| F16 | Re-key EXIF extraction and normalized metadata to asset/file/current-version identity, including invalidation after checkout commits. | F08 |
| F17 | Re-key RAW preview, thumbnail, and video-poster jobs/artifacts to exact source versions. | F03, F16 |
| F18 | Re-key AI tags, captions, embedding generations, lexical search, and hybrid search to assets plus exact source versions. | F16 |
| F19 | Complete album, hidden-media, share, and public-route semantics for opaque asset IDs and multi-file assets after the mechanical F03 cutover. | F03, F04 |
| F20 | Add explicit product deletion, Docbank trash coordination, recovery, and checkout cleanup without automatic garbage collection. | F05, F11, F19 |
| F21 | Add CLI/admin surfaces for checkout selection, status, conflicts, placement readiness, and recovery operations. | F10, F14 |

**Gate:** The existing Fotobank product behaviors operate on the new asset/file
model, and every derived result can name the exact Docbank version from which it
was produced.

### Milestone 5 — Dogfood and optimization decisions

| PR | Outcome | Depends on |
|---|---|---|
| F22 | Add the end-to-end deployment/dogfood scenario and record the Lightroom acceptance run, restore drill, capacity observations, and operational defaults. | F15–F21 |
| D05 (conditional) | Add Docbank-owned safe reflink-or-copy exact-version materialization. | F12 evidence and §10.4 criteria |
| F23 (conditional) | Use Docbank's safe materialization operation for eligible large checkouts, with copy fallback. | D05 |
| D06 (conditional) | Expose embedded version-pruning preview and execution. | F12 evidence of material XMP history growth |
| F24 (conditional) | Apply an explicit XMP version-retention policy through Docbank's embedded pruning API. | D06 |

Conditional pull requests are absent unless their evidence gate is met. Their
absence is a valid final state, not incomplete work.

## 17. Planning and delivery rules

- This master spec controls cross-cutting decisions. Each milestone or cohesive
  subset receives a separate implementation plan before code changes begin.
- Plans use the exact Docbank version and public signatures present when that
  milestone starts; they do not design against unmerged provider PRs.
- Cross-repository contracts are implemented and merged in Docbank first, then
  consumed at an exact tagged or pseudo-versioned commit by a small Fotobank
  dependency-update PR or the named dependent feature PR. Alpha integration
  may use pseudo-versions until the dependent milestone proves the contract.
- Pull requests may be stacked where dependencies require it, but every PR must
  state its base and remain independently reviewable.
- F02a may absorb final-shaped inactive repository work that reduces F03's
  mechanical breadth. The active asset/file consumer cutover and Docbank
  authority cutover remain one F03 pull request; review-sized commits do not
  permit an intermediate merge with dual product writes, fallback reads,
  legacy storage fields, or a compatibility adapter between media models.
- Performance claims require the workloads in §10.3. Storage optimizations do
  not precede those measurements.
- A milestone gate is part of the milestone, not optional follow-up work.
- Once this governing spec and an executable PR plan are approved, planning is
  frozen and implementation begins. Reopen a frozen plan only for an
  unimplementable contract, a violated governing invariant, a reachable
  data-loss path, or a missing dependency. Resolve ordinary implementation
  details and additional test cases in the affected code PR instead of cycling
  the planning PR.
- Kata tracks approved outstanding implementation work. The master spec itself
  is not a substitute for milestone issues and dependency links.

## 18. Definition of completion

The rebuild is complete when:

1. Docbank is the only authority for original media bytes and versions.
2. Fotobank is the only authority for photo-product semantics.
3. No legacy original storage, MD5 identity, or compatibility path remains.
4. A partial writable checkout supports the Lightroom acceptance sequence.
5. Conflicts are detected from a real stale base and require explicit
   resolution.
6. The complete checkout can be rebuilt from restored authorities into an empty
   directory.
7. Video byte ranges, storage placement, bounded whole-vault content
   verification, coordinated backup, and restore operate through public
   embedded Docbank APIs.
8. EXIF, thumbnails, search, and AI projections are keyed to exact Docbank
   versions and are rebuildable.
9. Albums, hidden state, and sharing use opaque Fotobank asset IDs without
   exposing Docbank coordinates.
10. The scale and Lightroom dogfood reports establish operational defaults for
    the intended deployment.
