# Product Features

## Import and metadata

`internal/ingest` discovers supported photos and videos, ignores common
filesystem metadata, prevents concurrent import processes with a file lock,
and bounds worker concurrency. The active importer computes its legacy
checksum, extracts metadata, chooses a storage key, writes through
`storage.Store`, inserts the media row, refreshes full-text search, and queues
derived work.

`internal/exifread` extracts capture time, camera, lens, exposure, dimensions,
duration, orientation, and GPS data without sending media to an external
service. `internal/geo` resolves coordinates to a coarse location label from
embedded Natural Earth data. GPS relabeling can update place names without
re-reading media; full backfill re-reads bytes when coordinates are missing.

The replacement asset importer will group related files before content writes.
One JPEG plus one camera RAW becomes one asset: JPEG is the primary display
file and RAW is the camera source. XMP is a sidecar of the RAW when present,
otherwise of the primary. Ambiguous same-basename groups are conflicts rather
than silent guesses.

## Thumbnails

`internal/thumb` owns a durable database queue and worker pool. A claim records
the media ID, thumbnail version, and lease time. Completion succeeds only if
the claim is still current; abandoned claims return to the queue after the
lease timeout.

The worker reads the source, extracts an embedded preview for supported RAW
formats or decodes ordinary images with orientation, resizes to the fixed
sizes, and writes versioned JPEG artifacts. Videos and formats without a
supported decoder become `no_preview`. A regenerated thumbnail invalidates
embeddings built from the previous preview version.

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
not disable the CLI or backend sharing data plane.

## Hidden media

Hidden media is excluded by default from library lists, maps, facets, search,
albums, shares, thumbnails, and full-size byte routes. Owner actions can unlock
hidden access with a short-lived cookie after passcode verification.

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
