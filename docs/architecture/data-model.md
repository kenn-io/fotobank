# Data Model

## SQLite ownership

Fotobank SQLite is authoritative for product state: owners, assets, metadata,
albums, shares, privacy, thumbnail state, AI results, search generations, and
application settings. Docbank owns media content and version history; it does
not own Fotobank's product relationships.

The project is pre-alpha. The complete schema is the single editable migration
`000001_initial_schema.{up,down}.sql`. Both directions change together and are
tested from an empty database. No numbered upgrade migration or compatibility
view is added until a real database compatibility promise exists.

## Owners

`owners` uses `(hub, user_id)` as its primary key. `storage_key` is a canonical
lowercase UUID generated on first registration unless a valid explicit UUID is
provided. Re-registering an owner with no requested key returns the stored key;
an explicit conflicting key is rejected.

Owner coordinates are copied into owner-scoped rows. Database triggers enforce
that relationships do not cross owners even when a repository bug attempts an
invalid insert.

## Assets and files

An asset is the user-visible photo or video. A file is one physical
representation within that asset: primary display media, camera source,
sidecar, or alternate. Relationships record `sidecar_of`, `derived_from`, and
`paired_with` without forcing each file to become a separate library item.

Ready assets have exactly one primary file. Every file in a ready asset has a
complete Docbank mapping: node ID, stable virtual path, current version ID, and
SHA-256. Database triggers prevent later inserts, updates, deletions, or owner
changes from breaking those invariants.

Product columns named `media_id` use “media” as product language. Their values
are asset UUIDs and their foreign keys target `assets`.

## Albums and sharing

`albums` belongs to one owner. `album_media` is ordered membership; its
`media_id` identifies the product media item, not a storage file.

`scopes` is the owner-side sharing record. A scope targets either an explicit
media set or an album and records the grantee, permissions, broker state,
retry state, and revision. `scope_media` stores explicit membership.
`principal_display` is a cache of human-readable handles and is not identity
authority.

Share creation and revocation use persisted state transitions. The broker
worker publishes or revokes outside the database transaction, then records the
result. Grantee-side reads expand the scope and re-check visibility and
download permission at read time.

## Hidden media

`hidden_at` is product visibility state. Hidden credentials, sessions,
failures, and lockouts live in separate `auth_hidden_*` tables. Hiding an item
removes it from ordinary list, search, sharing, thumbnail-serving, and
byte-read paths.
An unlock cookie permits owner access for a bounded session.

This is not encryption. Media bytes remain readable to an operator with direct
storage or database access.

## Checkouts

`checkouts` owns one canonical absolute working root and one owner. Separate
selection tables retain explicit asset IDs, album IDs, and capture-year ranges;
`include_all` represents the deliberately capacity-bounded whole-library case.

`checkout_entries` binds each materialized file to its relative working path
and immutable Docbank base version. The initial observation records size,
modification time, and SHA-256. Entry paths and file IDs are unique within a
checkout so two product files cannot silently claim the same working file.
The scanner updates that observation only after a file settles and is hashed.
An unequal stable hash produces `pending`; an equal hash produces `clean`; an
absent working file produces `missing` without changing content authority.

`checkout_scan_candidates` persists the first and latest size/mtime observation
used by the settle window. A tracked candidate has a composite foreign key to
the checkout entry at the same relative path. An untracked candidate has no
file ID and remains `pending` after hashing for the later new-file import flow.
Candidate progress is durable queue state, not an event log, so a restart does
not restart the settle decision or lose an already discovered local file.

Checkout ledgers are retained independently from their source assets. An owner
with any checkout cannot be removed until a future explicit checkout-deletion
lifecycle defines what happens to its working files and retained history.

## Derived state

Thumbnail status and version live with the product media row. Claim timestamps
belong to the thumbnail queue lease and are not general asset metadata.

AI results, tags, captions, jobs, failures, skips, embedding generations,
vector mappings, and the FTS5 table all use product media IDs. They are
projections that can be rebuilt from authoritative media and configuration.

## Transaction rules

- A repository method that changes a domain invariant performs the complete
  change in one SQLite write transaction.
- Database triggers are the final guard for ownership, relationship, mapping,
  and ready-state invariants.
- Content writes cannot share a transaction with Fotobank SQLite. The importer
  records an operation before the Docbank call, then records the receipt
  idempotently afterward. Pending and conflict states are durable product
  state, not log messages.
