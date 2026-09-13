# Product Features

Fotobank groups related files into photos, organizes them into albums, and
controls access to them. This page describes the services behind those features.

## Import and metadata

`internal/ingest` copies supported photos, videos, RAW files, and XMP sidecars
into Docbank. It ignores common filesystem metadata. A file lock prevents
concurrent imports, and a worker limit bounds parallel processing.

For each import, it:

1. Waits for source files to stop changing.
2. Groups related files into assets and computes SHA-256 checksums.
3. Records each operation and creates exact versions in Docbank.
4. Queues search and derived work after every file mapping is complete.

Docbank extracts capture time, camera, lens, exposure, dimensions, duration,
orientation, and GPS evidence from the exact immutable primary version.
`internal/media.ProjectSourceMetadata` selects the fields used by the photo
library, and `internal/geo` resolves coordinates to a coarse location label
from embedded Natural Earth data. The stored projection carries the Docbank
version, extractor fingerprint, and metadata checksum that produced it. GPS
relabeling can update place names without processing media; full backfill asks
Docbank to ensure metadata only after the primary node, version, SHA-256, and
size match the Fotobank mapping. It applies GPS only while that version remains
the asset's current primary content. Incomplete, malformed, non-finite,
out-of-range, and null-island coordinates are treated as absent without
discarding other source metadata.

One JPEG plus one camera RAW becomes one asset: JPEG is the primary display
file and RAW is the camera source. XMP is a sidecar of the RAW when present,
otherwise of the primary. Ambiguous same-basename groups are conflicts rather
than silent guesses.

## Thumbnails

`internal/thumb` owns a durable database queue and worker pool. A claim records
the media ID, thumbnail version, and lease time. Completion succeeds only if
the claim is still current; abandoned claims return to the queue after the
lease timeout.

The worker asks Docbank for the asset's canonical exact-version preview,
resizes those verified JPEG pixels to the fixed sizes, and writes versioned
JPEG artifacts. Docbank owns ordinary-image decoding, orientation, and
embedded-preview extraction for supported camera RAW formats. Videos and
formats without a supported producer become `no_preview`. A regenerated
thumbnail invalidates embeddings built from the previous preview version.

Thumbnail files are disposable. The database records whether a version is
pending, working, ready, failed, or has no preview. Serving checks both status
and requested version before opening the artifact.

## Albums

Albums are owner-scoped named collections with ordered media membership.
Repositories persist membership; `AlbumService` enforces ownership and
coordinates deletion with shares. Album APIs and CLI commands use the same
service path.

Album timestamps describe album metadata changes, not every membership edit.
Code must not infer a synchronization protocol from `updated_at`.

## Sharing

A share is a scope over explicit media or an album. It records one owner, one
grantee principal, permissions such as download, and an opaque share UUID.
Sharing changes metadata only; it does not copy media.

Owner-side services validate every target and persist a publish or revoke
transition. `internal/shareworker` calls either the stub broker or
`internal/brokerexec`, then records success or retry state. Broker commands
receive a narrow environment and structured arguments; they are not a general
shell hook.

Grantee-side services expand scopes into visible albums and media. They re-check
scope state, expiry, hidden status, and download permission on each read. Share
capabilities and media UUIDs are opaque identifiers, not substitutes for these
checks.

`[ui].sharing_enabled` hides owner sharing controls in the frontend. It does
not disable sharing through the CLI or HTTP API.

## Hidden media

Hidden media is excluded by default from library lists, maps, facets, search,
albums, shares, thumbnail-serving routes, and full-size byte routes. Owner
actions can unlock hidden access with a short-lived cookie after passcode
verification.

The background thumbnail queue does not filter `hidden_at`. It may generate a
disposable local thumbnail for hidden media, but the service checks hidden
access again before serving that artifact. Hiding an item does not delete
existing thumbnails or cancel other queued projection work.

Passcodes are hashed. Failed attempts and lockout state are persisted.
Production cookies use the `__Host-` prefix, `Secure`, `HttpOnly`, and
appropriate same-site policy. Loopback development can explicitly select an
insecure cookie name; protocol headers do not silently downgrade it.

Hidden state is an application privacy boundary, not cryptographic storage.
Operational logs avoid media paths and sensitive payloads on ordinary paths.

## Settings and events

User settings store owner-scoped JSON values. Application settings store
operator-controlled runtime overrides, including AI configuration, with
revision and update metadata. Admin routes require a configured principal
allowlist.

`/api/v1/events` is a server-sent event stream used to invalidate frontend
views after relevant changes. Events are hints; the database remains the
source of product state and clients can recover by refetching.
