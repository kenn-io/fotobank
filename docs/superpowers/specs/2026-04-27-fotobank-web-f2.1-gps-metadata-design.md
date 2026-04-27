# F2.1 GPS Metadata — Design

> Sub-plan of the F2 viewer-suite milestone. F2 ships in five sub-plans:
> F2.0 Viewer Foundation → **F2.1 GPS Metadata** (this doc) → F2.2 Albums + Sharing →
> F2.3 Hidden Privacy → F2.4 Lightbox Viewer. F2.1 is independent of F2.2 and
> F2.3 and may run in parallel with them. F2.4's lightbox info panel consumes
> the `location_label` produced here.

## 1. Goal

Capture GPS coordinates from photo EXIF, resolve them to coarse
human-readable location labels via an embedded offline gazetteer, surface
them through the media DTO, and render them on the (currently stub) media
detail view. No network calls, no background workers, no map UI.

## 2. Scope

In scope:

- New nullable columns on `media`: `latitude`, `longitude`, `gps_at`,
  `location_label`.
- EXIF GPS IFD parsing in `internal/exifread` (photos only).
- New `internal/geo` leaf package: offline reverse-geocoder backed by
  embedded Natural Earth 1:10m GeoJSON.
- `internal/cli/gps.go` cobra subcommand: `fotobank gps backfill`.
- HTTP DTO additions on the existing media endpoints.
- A non-stub `MediaDetail.svelte` that renders the photo + a small info
  block including the location label.

Explicitly **out of scope**, deferred to later work:

- Video GPS (QuickTime/MP4 location boxes).
- Street- or neighborhood-granularity labels (e.g. "Hayes Valley"). A
  configured external geocoder (Photon, self-hosted Nominatim) can refine
  later by re-resolving against existing `latitude`/`longitude`.
- Map UI of any kind. F2.0 explicitly flagged "MapLibre or another heavy
  view (F2.1 GPS map, possibly)" — that "possibly" resolves to *no* in
  F2.1; if a map view is desired, it will be its own sub-plan (F2.5+).
- Spatial-search queries ("photos near here", bbox filters). The partial
  index added in this slice keeps such queries cheap when they land later,
  but no API surfaces them in F2.1.
- Audit history of label changes. When the embedded gazetteer is
  upgraded and `gps backfill --mode=relabel` is run, the new label
  overwrites in place; we keep no record of the prior value.

## 3. Architecture

A new leaf package `internal/geo` owns the offline reverse-geocoder. It
embeds three Natural Earth 1:10m GeoJSON files at compile time, parses
them once at startup, and exposes a single resolver type. After init the
resolver does **no I/O** — no network, no DB, no filesystem reads. (No
allocations after init is an *implementation target* validated by
benchmark, not part of the public contract.)

The `PlaceResolver` interface is defined on the **consumer** side
(`internal/ingest`), per Go's "interfaces where they're used" convention:

```go
// internal/ingest
type PlaceResolver interface {
    Resolve(lat, lon float64) (label string, ok bool)
}
```

The concrete `*geo.NaturalEarth` satisfies it implicitly. Tests in
`internal/ingest` use a tiny fake; tests in `internal/geo` exercise the
real impl against known coordinates.

The importer takes the resolver via constructor injection. `nil` is
allowed:

```go
func NewImporter(store storage.Store, repo *media.Repo, places PlaceResolver) *Importer
```

When `places == nil`, the importer still extracts and stores
`latitude`/`longitude`/`gps_at` from EXIF — only `location_label` is
left empty. This keeps integration tests cheap (no embedded gazetteer
init) and avoids forcing geo init in narrow code paths that only care
about raw GPS. The server boot path and `fotobank import` CLI both
construct a real `*geo.NaturalEarth` and fail-fast if it fails to load;
`nil` is reserved for tests.

Data flow:

```
Importer
  └─ extractMetadata(c)                           [exifread]
       ├─ Timestamp, Make, ...                    (existing)
       └─ Latitude, Longitude, GPSAt              (new)
  └─ buildMediaRow(c, ..., meta, places, ...)     [ingest]
       ├─ existing fields
       └─ when meta.Latitude != nil && meta.Longitude != nil:
            ├─ row.Latitude       = meta.Latitude
            ├─ row.Longitude      = meta.Longitude
            ├─ row.GPSAt          = meta.GPSAt           (may be nil)
            └─ if places != nil:
                  if label, ok := places.Resolve(*lat, *lon); ok:
                       row.LocationLabel = label
  └─ repo.Insert(row)                             [media]
```

Backfill is a new `internal/cli/gps.go` cobra subcommand that goes
through `service.MediaService` → `media.Repo`. HTTP DTO surfacing
happens in `internal/httpapi`. Frontend changes live in
`MediaDetail.svelte`. **No new background workers, no new queues.**
The reverse-geocoder is microsecond-cheap; an async pipeline would be
strictly more complex and slower end-to-end.

## 4. Database schema

### 4.1 Migration approach

F2.1 edits the existing `internal/db/migrations/000001_initial_schema`
pair directly. It does **not** add a `000004_gps_columns` migration.

Rationale: fotobank has not deployed beyond the developer box. The
project rule "never edit a migration that's already on main" exists to
protect live deployments; with no deployments, editing the initial
schema is the cleaner choice — every existing dev DB will be wiped
and re-migrated regardless. The prek hook that flags edits to existing
migrations will need to be allowed for this single change; the
implementation plan should call this out explicitly so the implementer
doesn't burn time fighting the hook.

### 4.2 Column additions on `media`

Inside the existing `CREATE TABLE media (...)` in `000001_initial_schema.up.sql`,
add four nullable columns alongside the existing photo metadata columns:

```sql
latitude        REAL,
longitude       REAL,
gps_at          TIMESTAMP,
location_label  TEXT,
```

The matching `down.sql` is updated as a prior-state snapshot (i.e. the
schema *without* the GPS columns), not as `ALTER TABLE DROP COLUMN`.

### 4.3 Partial index

Also added in `000001_initial_schema.up.sql`:

```sql
CREATE INDEX media_owner_geo_idx
  ON media(owner_hub, owner_user_id, latitude, longitude)
  WHERE latitude IS NOT NULL AND longitude IS NOT NULL;
```

This keeps owner-scoped bbox / "photos near here" queries cheap when
they eventually land, without requiring a future migration. The partial
predicate keeps the index size negligible on ownership pools where most
photos lack GPS.

### 4.4 Go struct (`internal/media/media.go`)

`media.Media` gains four fields, mirroring the existing nullable
conventions:

```go
Latitude       *float64    // nil if no GPS
Longitude      *float64    // nil if no GPS
GPSAt          *time.Time  // nil if not present in EXIF
LocationLabel  string      // "" when no resolver match
```

`Latitude` and `Longitude` are **atomic** — never one without the other.
`GPSAt` is independent: a row can have coords but no GPS time, or
(theoretically) GPS time but no coords; in practice the importer drops
both halves when the coord half fails validation, so the second case is
not produced.

`LocationLabel` is `string` (not `*string`) following the existing
camera-string convention (`Make`, `Model`, etc.); empty string serializes
as NULL via `nullStr()`.

### 4.5 Repo plumbing — four sites that must stay in sync

This is the biggest correctness risk in this slice. Adding columns to
`media` means updating **four** projection / write sites in lock-step;
missing one silently breaks scans or inserts.

1. `internal/media/repo.go` const **`mediaSelect`**.
2. `internal/media/repo.go` const **`mediaColumnsQualified`** — the
   `m.`-prefixed variant used when the query joins a CTE that also has
   an `id` column.
3. `internal/media/repo.go` const **`mediaInsert`** — column list,
   matching `?` placeholders, and the `Insert` method's argument list.
4. `internal/album/repo.go` const **`albumMediaMediaSelect`** — duplicate
   projection used for album-joined queries; comment in `media/repo.go`
   already warns about this.

`scanMedia` in `internal/media/repo.go` gains four new locals (`lat
sql.NullFloat64`, `lon sql.NullFloat64`, `gpsAt sql.NullTime`, `locLabel
sql.NullString`) and four assignments to populate the struct.

`Insert` uses `nullFloat(m.Latitude)`, `nullFloat(m.Longitude)`,
`nullTime(m.GPSAt)`, `nullStr(m.LocationLabel)`.

### 4.6 New repo method: `UpdateGPS`

```go
// UpdateGPS sets the four GPS columns on an existing row. Used by the
// backfill CLI; the importer uses Insert. Returns errs.ErrNotFound if
// the row is gone.
func (r *Repo) UpdateGPS(ctx context.Context, id string, lat, lon *float64, gpsAt *time.Time, label string) error
```

### 4.7 New repo method: `ListGPSBackfillCandidates`

A dedicated repo method for the backfill CLI, kept separate from
`ListFilter` so the backfill semantics don't leak into the public list
API:

```go
// ListGPSBackfillCandidates enumerates rows for `gps backfill`, filtered
// by mode, owner scope, and an optional `since` cutoff against
// imported_at. Returns rows ordered by id for stable paging. Always
// excludes media_type='video'.
func (r *Repo) ListGPSBackfillCandidates(ctx context.Context, owner owners.Principal, mode GPSBackfillMode, since *time.Time, limit, offset int) ([]Media, error)
```

`mode` discriminates the WHERE clause:

- `GPSBackfillModeFull` — all photo rows, no GPS predicate.
- `GPSBackfillModeFillMissing` — `latitude IS NULL AND longitude IS NULL`.
  ("BOTH null" — see §7.4. A row with one coord set is partial state from
  a prior run and is not a fill-missing target; it's untouched.)
- `GPSBackfillModeRelabel` — `latitude IS NOT NULL AND longitude IS NOT NULL`.

`since != nil` adds `imported_at >= since`.

Pages via `LIMIT/OFFSET` like the existing `List` / `ListAll` methods.

## 5. EXIF GPS extraction (`internal/exifread`)

### 5.1 Metadata struct extension

Three new fields on `exifread.Metadata`:

```go
type Metadata struct {
    // ... existing fields ...

    // Latitude and Longitude are atomic — emitted together or not at all.
    // GPSAt is optional and independent: a row can have coords with no
    // GPSAt. Never emit one coordinate without the other.
    Latitude  *float64
    Longitude *float64
    GPSAt     *time.Time   // GPSDateStamp + GPSTimeStamp combined into UTC
}
```

### 5.2 Tags consulted

- `GPSLatitude` (rational triple: degrees, minutes, seconds) +
  `GPSLatitudeRef` (`"N"`/`"S"`) — sign comes from ref.
- `GPSLongitude` + `GPSLongitudeRef` (`"E"`/`"W"`).
- `GPSDateStamp` (`"YYYY:MM:DD"`) + `GPSTimeStamp` (rational triple:
  hours, minutes, seconds, UTC) — combined into a single `time.Time`
  in UTC.

The implementation may use
`exif.NewGpsDegreesFromRationals(ref, raw).Decimal()` from
`dsoprea/go-exif/v3` for the DMS conversion, but **only after**
pre-validating the ref length and rational denominators (see §5.3).
The library helper does not do this validation and will produce
garbage on malformed inputs.

### 5.3 Validation — drop conditions

If any of these fire, the GPS portion of the result is empty (importer
behaves as if EXIF had no GPS at all):

- Missing or non-rational `GPSLatitude` / `GPSLongitude`.
- Any zero-denominator rational in `GPSLatitude` / `GPSLongitude`
  (would divide-by-zero; common in malformed sidecar exports).
- Missing `GPSLatitudeRef` / `GPSLongitudeRef` — sign is ambiguous.
- Lat ref not exactly `N` or `S`; lon ref not exactly `E` or `W`.
  Anything else (empty, multi-char, lowercase, garbage) drops.
- Resulting decimal latitude outside `-90..90` or longitude outside
  `-180..180`.
- `lat == 0 && lon == 0` ("null island"). Common camera bug when GPS
  hadn't acquired a fix; we'd rather lose a real `0,0` photo than
  ingest noise on every camera-with-broken-fix.

`GPSAt` is parsed independently. Failures keep `Latitude/Longitude` and
leave `GPSAt = nil`:

- Any zero-denominator rational in `GPSTimeStamp`.
- Hour outside `0..23`, minute outside `0..59`, or second outside
  `0..<60` (after rational conversion).
- `GPSDateStamp` failing `time.Parse("2006:01:02", ...)`.

### 5.4 Tests

A new `internal/exifread/parse_internal_test.go` lives in **package
`exifread`** (not `exifread_test`) so it can call the unexported
`parseExif` directly. Two table tests:

- Coordinate matrix: every drop condition above (each as its own row),
  plus a clean fix that produces the expected decimal pair.
- GPSAt matrix: each timestamp drop condition; one clean case asserting
  the parsed `time.Time` is in UTC and exactly the expected instant.

Two real-fixture JPEGs join `internal/exifread/testdata/`:

- `paris-clean-gps.jpg` — known coords near 48.8566 / 2.3522 with
  `GPSAt`. Tested via the existing `ExtractPhoto` integration entry.
- `null-island-broken.jpg` — `lat == 0 && lon == 0`, asserts the drop.

### 5.5 Out of scope for §5

Video GPS (QuickTime/MP4 location boxes — `udta`, `©xyz`, `loci`) is
**not** implemented in F2.1. `internal/exifread/video.go` is unchanged.
The backfill CLI also skips `media_type='video'` rows (§7.4).

## 6. Offline geocoder (`internal/geo`)

### 6.1 Embedded data

Three Natural Earth 1:10m GeoJSON files vendored into
`internal/geo/data/`:

```
internal/geo/data/
  ne_10m_admin_0_countries.geojson
  ne_10m_admin_1_states_provinces.geojson
  ne_10m_populated_places.geojson
  PROVENANCE.md
  LICENSE
```

`PROVENANCE.md` records: source URL
(<https://www.naturalearthdata.com/downloads/10m-cultural-vectors/>),
Natural Earth release version, the conversion command (Natural Earth
ships ESRI shapefiles natively; the GeoJSON conversion uses
`ogr2ogr -f GeoJSON` with the exact arguments and source-archive SHA256
recorded), and SHA256 of each output file. `LICENSE` is the public-domain
terms text from
<https://www.naturalearthdata.com/about/terms-of-use/>.

A CI/lint check (or a one-line `go test`) verifies SHA256 of each
embedded file matches `PROVENANCE.md`. This catches accidental
re-vendoring of a different release.

### 6.2 Library

`github.com/paulmach/orb` for GeoJSON decoding + ring-with-holes
point-in-polygon + bbox prefilter. One new dep. Justified over a
hand-rolled PNP because Natural Earth polygons include islands (rings
with holes) and antimeridian-split features (Russia, Fiji) that
hand-rolled PNP gets subtly wrong.

### 6.3 Public API

```go
// Package geo provides an offline reverse geocoder backed by Natural
// Earth 1:10m. It produces coarse country/region/city labels suitable
// for an info panel; it does NOT produce neighborhood/street-level
// labels — that's an explicit out-of-scope deferral, see F2.1 spec.
package geo

type NaturalEarth struct { /* polygons + city points, all cached */ }

// NewNaturalEarth parses the embedded gazetteer once. Allocates ~10MB
// of in-memory polygons + city points. Returns an error if the embedded
// data is missing or malformed (a programming/build error). After this
// returns, Resolve does no I/O.
func NewNaturalEarth() (*NaturalEarth, error)

// Resolve returns a coarse human-readable label for the input
// coordinate. Returns ("", false) when no admin_0 polygon contains
// the point (open ocean) or when the input is out of range.
func (n *NaturalEarth) Resolve(lat, lon float64) (label string, ok bool)
```

The `PlaceResolver` interface that `*NaturalEarth` satisfies lives in
`internal/ingest` (consumer side, per §3).

### 6.4 Coordinate-order foot-gun

The public API takes `(lat, lon)`. **GeoJSON and `orb` use `[lon, lat]`
order.** Every internal construction of an `orb.Point` from public
inputs must reorder:

```go
pt := orb.Point{lon, lat}   // not orb.Point{lat, lon}
```

This is the kind of silent-bug place where the wrong answer is plausible
(a coord pair looks fine; it just resolves to the wrong country), so
§6.7 below mandates a regression test specifically for the swap.

### 6.5 Resolve algorithm

Deterministic order; first match in a stably-sorted feature index wins
on overlap:

1. **Country lookup.** Bbox-prefilter → point-in-polygon over admin_0
   features. Take the chosen field (§6.6). Empty if no match (open ocean).
2. **Region lookup.** Same on admin_1 features. Empty if no admin_1
   covers the point (some countries lack admin_1 detail).
3. **Nearest city.** Great-circle distance from query point to each
   populated_places point (~7K linear scan, sub-millisecond). Take the
   nearest **only if** all hold:
   - Distance ≤ **25 km** (default).
   - The populated-place's `ADM0NAME` matches the country name chosen
     in step 1.
   - When step 2 produced a region, the populated-place's `ADM1NAME`
     matches it.
   `ADM0NAME` and `ADM1NAME` are Natural Earth populated_places
   attributes; if a particular release lacks them, the gate fails open
   (no city included) — acceptable defensive behavior. The 25 km
   threshold avoids false precision in rural / coastal areas where the
   nearest city may be the *only* nearby populated place but is still
   many tens of kilometers away. Implementers may raise to a max of
   50 km if §6.7 testing shows under-coverage in plausible
   personal-photo scenarios.
4. **Format label** by joining non-empty parts with `", "`:
   - `city + region + country` → e.g. `"Yosemite, California, United States of America"`
   - `region + country` → `"California, United States of America"`
   - `country` → `"United States of America"`
   - All empty → `("", false)`.

### 6.6 Field selection — locked, not implementer choice

Natural Earth features expose multiple name fields per layer
(`NAME`, `NAME_LONG`, `ADMIN`, `FORMAL_EN`, `NAMEASCII`, ...). Field
choice is locked here, not left to implementer taste, so tests can
assert exact substring matches:

- **admin_0 country name:** `NAME` first; if empty, fall back to `ADMIN`.
- **admin_1 region name:** `name` first (Natural Earth admin_1 uses
  lowercase `name`); fall back to `NAME` if absent.
- **populated_places city name:** `NAMEASCII` first (avoids encoding
  surprises in tests and labels); fall back to `NAME`.

If the implementer sanity-checks output against the canonical five
known points (Eiffel Tower, Statue of Liberty, Yosemite, Sydney
Opera House, Tokyo Skytree) and finds any of these fields produces an
awkward label, escalate to a spec amendment rather than silently
swapping fields.

### 6.7 Tests

`internal/geo/geo_test.go` exercises:

- **Five known-city coords** return non-empty labels containing the
  expected country and (where available) region/city substrings:
  Paris, NYC, Tokyo, Sydney, Cape Town.
- **Coord-order swap regression.** Resolve with `(lat=2.3522,
  lon=48.8566)` (i.e. swapped) does *not* return a Paris-shaped label.
  This catches the `orb.Point{lon, lat}` foot-gun (§6.4).
- **Mid-Atlantic ocean** (e.g. `lat=0, lon=-30`) returns `("", false)`.
- **South Pole** (`lat=-89.9, lon=0`) returns `"Antarctica"` (admin_0
  only — no region, no city).
- **Antimeridian crossings.** A Russian Far East coord and a Fijian
  coord both resolve to their expected country; this is the case
  hand-rolled PNP would have gotten wrong.
- **Same-country gating** for the city threshold: a coord that's
  geographically near a city in a different country (e.g. across a
  border) returns the *region+country* label, not the cross-border
  city's label.

### 6.8 Init benchmark + size guard

The implementation plan records, as concrete numbers:

- Total embedded data size in bytes (≈10–20MB for 10m GeoJSON).
- `NewNaturalEarth()` wall time on the developer machine, captured by
  a `go test -bench` benchmark in `internal/geo`.

If init exceeds ~1s on the developer machine, the plan should escalate
(consider 1:50m fallback, lazy-init, or pre-parsed binary blob).
Otherwise eager init at boot time is fine.

## 7. Importer wiring + backfill CLI

### 7.1 Importer changes

`NewImporter` gains a third parameter:

```go
// NewImporter constructs an Importer. places may be nil — when nil,
// ingest still extracts and stores latitude/longitude/gps_at from EXIF
// and leaves LocationLabel empty. This keeps integration tests cheap
// and avoids forcing geo init in narrowly scoped paths. Production
// callers (server, fotobank import) MUST pass a real *geo.NaturalEarth.
func NewImporter(store storage.Store, repo *media.Repo, places PlaceResolver) *Importer
```

`buildMediaRow` signature gains `places PlaceResolver` as a final
parameter; populates the four GPS fields per §3 data flow.

### 7.2 Caller updates

- `internal/cli/server.go` — server boot. Calls `geo.NewNaturalEarth()`
  once; failure aborts boot. Passes the resolver into `NewImporter`.
- `internal/cli/import.go` — one-shot `fotobank import`. Same.
- `internal/cli/gps.go` — backfill (new). Same.
- `internal/ingest/importer_test.go` — existing tests pass `nil`;
  unaffected. A new test case passes a stub resolver and asserts all
  four GPS fields land in the inserted row.

### 7.3 Service method

```go
// internal/service/media.go
//
// UpdateGPS persists the four GPS columns on a row owned by caller.
// Uses Get(ctx, id, caller) for the owner check, then repo.UpdateGPS.
// The CLI orchestrates "open NAS bytes, run exifread, resolve label"
// itself — the service layer stays simple and auth-scoped.
func (s *MediaService) UpdateGPS(
    ctx context.Context,
    caller owners.Principal,
    id string,
    lat, lon *float64,
    gpsAt *time.Time,
    label string,
) error
```

### 7.4 CLI surface

```
fotobank gps backfill [scope] [--since=DURATION] [--mode=MODE]

Scope (mutually exclusive; --owner and --all-owners both bypass the
default stub-mode-required check, mirroring `thumbs regenerate`):

  --owner=hub:user        Restrict to one principal. Bypasses stub-mode.
  --all-owners            Walk every principal. Bypasses stub-mode.
  (neither set)           Stub mode required; uses configured stub principal.

Time filter:

  --since=DURATION        Only rows where imported_at >= now - DURATION.
                          DURATION is a Go time.Duration (e.g. 24h, 7d).
                          Must be > 0; 0/negative/malformed is a usage
                          error (exit 2) raised BEFORE opening the DB.

Mode (default `full`):

  --mode=full             For every selected photo row: re-extract EXIF
                          GPS from NAS bytes; if found, set
                          lat/lon/gps_at/label. If EXIF has no valid
                          GPS, CLEAR the four GPS fields (full is
                          authoritative — a row that lost its EXIF
                          loses its DB GPS). Always re-resolves label.
  --mode=fill-missing     Only touch rows where BOTH latitude AND
                          longitude are NULL. (Skip if either is set —
                          partial state means a prior `relabel` ran on
                          a row that had coords; don't overwrite.) Same
                          extract+label flow as full for selected rows.
  --mode=relabel          Only touch rows where lat AND lon are NOT
                          NULL. Skip EXIF re-extraction entirely; just
                          re-resolve label against existing coords.
                          Use case: the embedded gazetteer was bumped
                          and labels need refreshing without re-walking
                          NAS bytes.
```

`media_type='video'` rows are skipped in **all three modes** (video GPS
is out of scope, §5.5).

`--since` is parsed up-front by `time.ParseDuration` before opening the
DB, mirroring the F2.0 `thumbs regenerate` `newUsageError` pattern, so
malformed inputs yield exit code 2 without any side effects.

### 7.5 CLI orchestration

The CLI owns the "open NAS bytes, run exifread, resolve label" loop.
For each candidate row from
`repo.ListGPSBackfillCandidates(...)`:

```
mode == relabel:
    label, ok := places.Resolve(*row.Latitude, *row.Longitude)
    new = (row.Latitude, row.Longitude, row.GPSAt, label_if_ok_else_empty)

mode == full | fill-missing:
    rc, err := store.ReadRange(ctx, owner, row.Path, 0, -1)
    meta := exifread.parseFromReader(rc)   # or read to temp + ExtractPhoto
    if meta has GPS:
        lat, lon, gpsAt = meta.Latitude, meta.Longitude, meta.GPSAt
        label, _ = places.Resolve(*lat, *lon)
        new = (lat, lon, gpsAt, label)
    else (mode == full):
        new = (nil, nil, nil, "")     # full is authoritative-clearing
    else (mode == fill-missing):
        skip                          # leave row untouched

service.UpdateGPS(ctx, caller, row.ID, new...)
```

`exifread` may need a small sibling to `ExtractPhoto` that takes an
`io.Reader` instead of a path; if so, `ExtractPhoto` becomes a thin
wrapper that opens the file and delegates. (The existing implementation
already uses `os.Open` then `SearchAndExtractExifWithReader`, so the
refactor is tiny.)

### 7.6 Progress + summary

Log every 100 rows: `gps backfill: processed=N updated=U unchanged=K
skipped_no_exif=S failed=F`. Emit a final summary line with the same
shape **always**, including for runs of fewer than 100 rows.

### 7.7 CLI tests

`internal/cli/gps_test.go`:

- `TestGPSBackfillBadSinceErrorsBeforeOpeningDB` — parallels
  `TestThumbsRegenerateBadSinceErrorsBeforeOpeningDB`. Asserts exit 2
  and that the configured DB file does not appear on disk after the run.
- `TestGPSBackfillOwnerScopeBypassesStubModeRequirement` — parallels
  the F2.0 thumbs equivalent.
- `TestGPSBackfillFullClearsRowWhenExifHasNoGPS` — full is authoritative.
- `TestGPSBackfillFillMissingSkipsRowsWithCoords` — opt-out on either
  coord populated.
- `TestGPSBackfillRelabelSkipsRowsWithoutCoords`.
- `TestGPSBackfillSkipsVideos` — seed a video row with bogus GPS in
  hypothetical metadata, run any mode, assert row is untouched.
- `TestGPSBackfillFinalSummaryEvenForSmallRuns` — seed 3 rows, assert
  the summary line is emitted.

`internal/ingest/importer_test.go`: a new case seeds a JPEG fixture
with EXIF GPS, passes a stub `PlaceResolver` returning
`"Test City, Test Region, Test Country"`, runs import, and asserts the
resulting `media.Media` row has all four GPS fields set as expected.

`internal/service/media_test.go`: a new case for `UpdateGPS` —
caller-mismatch returns `errs.ErrOwnerMismatch`-mapped error;
caller-match writes through to the repo.

## 8. HTTP DTO + Frontend MediaDetail

### 8.1 DTO

`MediaDTO` (returned by `GET /api/v1/media` and `GET /api/v1/media/{id}`)
gains four nullable fields:

```go
type MediaDTO struct {
    // ... existing fields ...
    Latitude      *float64   `json:"latitude,omitempty"`
    Longitude     *float64   `json:"longitude,omitempty"`
    GPSAt         *time.Time `json:"gps_at,omitempty"`
    LocationLabel string     `json:"location_label,omitempty"`
}
```

`omitempty` keeps the wire payload small for the 95%+ of rows without
GPS, and lets the frontend branch on truthy/non-empty rather than
explicit-null checks.

`make api-generate` regenerates `openapi.json`. Run as part of the
implementation; the prek hook would otherwise block the commit.

### 8.2 HTTP tests

In `internal/httpapi/media_test.go`:

- `TestListMediaDTOIncludesGPSWhenPresent` — seed a row with all four
  GPS fields, list, assert all four appear with correct values.
- `TestListMediaDTOOmitsGPSWhenAbsent` — seed a row with **no** GPS,
  list, assert none of the four keys appear in the JSON. Asserts on
  raw JSON bytes (not the unmarshalled struct), because Go round-trips
  a missing-key field into a nil pointer regardless of `omitempty`;
  only raw-bytes assertion catches wire-shape regressions.
- `TestGetMediaDTOIncludesGPSWhenPresent` — same for the single-item
  endpoint.
- `TestGetMediaDTOOmitsGPSWhenAbsent` — same omission check on the
  single-item endpoint.

### 8.3 Frontend `Media` type

The frontend `Media` type (in `frontend/src/lib/media/types.ts`) gains
five fields:

```ts
export interface Media {
  // ... existing fields ...
  thumbVersion: number;     // mirrors thumb_version from the JSON DTO; needed
                            // so the detail view can construct
                            // /thumb?size=preview&v=<thumbVersion> without
                            // re-parsing the existing derived `thumbUrl`
  latitude?: number;
  longitude?: number;
  gps_at?: string;          // ISO 8601 string; parse on display, do not eagerly
                            // wrap as Date — F2.1 has no UI that needs it as
                            // a Date object
  location_label?: string;
}
```

`thumbVersion` is **required** (`number`, not optional) because F2.0's
JSON adapter already coerces a missing/invalid `thumb_version` to `0`
(see `mediaStore.svelte.ts` line ~150). The four GPS fields are
optional because the DTO emits them with `omitempty` (§8.1).

F2.0's compile-time field guard in `MediaStore.merge()`
(`mediaStore.svelte.ts` line 71) is the
`Exclude<keyof Media, "id" | "timestamp" | "taken" | "aspect" | "thumbUrl">`
type assertion that fails to compile when `Media` gains a field outside
the listed set. F2.1 MUST update both:

1. The `Exclude<...>` literal to include `"thumbVersion" | "latitude" | "longitude" | "gps_at" | "location_label"`.
2. The `unchanged` predicate adjacent to it (around line ~106) to
   compare each new field on re-merge so the dirty-mark logic still
   works correctly.

If either is missed, new fields silently bypass dirty-tracking and
buckets keep stale values forever — exactly the bug the guard exists
to prevent.

### 8.4 `mediaStore.get(id): Media | undefined`

F2.0's `MediaStore` does not expose a public `get(id)` method (the
internal `byId` map is `id → monthKey`, not `id → Media`). F2.1 adds:

```ts
class MediaStore {
  // ... existing ...

  /** Return the cached row for id, or undefined if not in the store. */
  get(id: string): Media | undefined { ... }
}
```

Implementation: maintain a parallel `byMediaId: Map<string, Media>`
populated on every store mutation. The map is the single source of
truth for `get`; consumers should not reach into private state.

### 8.5 `MediaDetail.svelte`

Receives the hoisted `mediaStore` as a prop from `App.svelte`,
identically to `Library.svelte` and `Sessions.svelte` in F2.0:

```svelte
<script lang="ts">
  import type { MediaStore } from "$lib/media/store";
  let { mediaStore }: { mediaStore: MediaStore } = $props();
  ...
</script>
```

On mount, look up the row via `mediaStore.get(id)`. On miss, fetch
`/api/v1/media/{id}` once and merge the result into the store. On
error, render the F2.0-style error fallback (already present from
the stub).

### 8.6 Layout

```
┌─────────────────────────────────────────────────┐
│ ← Back to Library                               │
├─────────────────────────────────────────────────┤
│                                                 │
│              [photo, size=preview]              │
│              max-w/max-h = viewport,            │
│              object-fit: contain                │
│                                                 │
├─────────────────────────────────────────────────┤
│ Captured        2024-06-15 14:30 UTC            │
│ Camera          Sony α7 IV                      │
│ Lens            85mm · f/1.8 · 1/250s · ISO 200 │
│ Location        Paris, Île-de-France, France    │
│                 48.8566° N, 2.3522° E           │  small secondary
└─────────────────────────────────────────────────┘
```

The image source uses `size=preview` (max edge 2560), **not** `size=large`.
`large` (max edge 4096) is reserved for F2.4's lightbox 1:1 view; this
interim detail page is fit-to-viewport, where `preview` is the right
balance. F2.4 will switch to `large` for its zoom mode. The URL is
built directly from `media.id` + `media.thumbVersion`:

```ts
const previewUrl = `/api/v1/media/${media.id}/thumb?size=preview&v=${media.thumbVersion}`;
```

Not from the existing derived `thumbUrl` (which is hard-coded to
`size=grid` at adapter time).

Image error fallback uses the F2.0 `imgError` `$effect` reset pattern
from `MediaCell` so a busted image doesn't blank the page.

### 8.7 Info section structure

The `<dl>` is built as **inline conditional rows in `MediaDetail.svelte`**.
A nested `<InfoRow>` component is *not* used for the `<dt>/<dd>` pair:
Svelte 5 component children inside a `<dl>` can be wrapped in a way
that breaks valid-HTML structure or layout. Inline conditionals keep
the markup explicit and unambiguous:

```svelte
<dl>
  {#if media.timestamp}
    <dt>Captured</dt><dd>{formatTimestamp(media.timestamp)}</dd>
  {/if}
  {#if media.make || media.model}
    <dt>Camera</dt><dd>{[media.make, media.model].filter(Boolean).join(" ")}</dd>
  {/if}
  ...
  {#if media.location_label}
    <dt>Location</dt>
    <dd>
      {media.location_label}
      {#if media.latitude != null && media.longitude != null}
        <small class="coord">{formatCoord(media.latitude, media.longitude)}</small>
      {/if}
    </dd>
  {/if}
</dl>
```

`gps_at` is NOT surfaced in the F2.1 UI — it's stored for future use
(e.g. F2.x camera-clock-drift detection) but the info panel doesn't
render it. The frontend leaves `gps_at` as the JSON string and never
parses it.

### 8.8 Coord formatting helper

`frontend/src/lib/format/coords.ts`:

```ts
export function formatCoord(lat: number, lon: number): string {
  const ns = lat >= 0 ? "N" : "S";
  const ew = lon >= 0 ? "E" : "W";
  return `${Math.abs(lat).toFixed(4)}° ${ns}, ${Math.abs(lon).toFixed(4)}° ${ew}`;
}
```

Decimal degrees with sign-via-cardinal. DMS adds confusion without
payoff for personal use. Convention for `lat == 0` / `lon == 0`
hemispheres: this should never occur in practice (null-island is
dropped at extraction §5.3), but for defensive correctness `lat == 0`
formats as N and `lon == 0` formats as E. Documented and tested.

### 8.9 Frontend tests

- **Vitest for `formatCoord`**: positive/negative pairs in each
  hemisphere, the `lat == 0` / `lon == 0` defensive cases.
- **Vitest for `mediaStore.get`**: returns the row when present, returns
  `undefined` when absent, returns the merged row after a `merge()`.
- **Vitest for `MediaDetail.svelte`**:
  - Given a media object with all GPS fields, both `Location` row and
    coords appear.
  - Given one without GPS, neither appears.
  - Given coords but no `location_label`, only coords appear (defensive
    — practically rare since the resolver populates label whenever
    coords exist over land).
  - Given a `location_label` but no coords (impossible in practice but
    harmless), the `<small class="coord">` does not render.
- **Playwright e2e**: navigate to `/media/<id>` for a row seeded with
  GPS, assert the `location_label` text is visible; do the same for a
  row without GPS and assert it is not.

## 9. Out-of-scope reminders

- Video GPS extraction (QuickTime/MP4 location boxes). Backfill skips
  videos in all modes.
- Street/neighborhood-granularity labels. Natural Earth gives "San
  Francisco, California, United States of America" — not "Hayes Valley".
  A configured external geocoder is the path; out of F2.1.
- Map UI (MapLibre or otherwise). Possibly a future F2.5 sub-plan.
- Spatial-search APIs ("photos near here", bbox filters). The partial
  index keeps them cheap when added; F2.1 surfaces no such API.
- Audit history of label changes. Re-running `gps backfill --mode=relabel`
  overwrites in place.
- Showing `gps_at` in the UI. Stored for future use; not rendered.
- Refreshing `gps_at` on relabel runs. Relabel mode does not touch
  `gps_at` — it's an EXIF-sourced value and only `--mode=full` /
  `--mode=fill-missing` re-read EXIF.

## 10. Risks

- **Editing migration 000001.** The prek hook flags this. Spec
  intentionally calls for it (§4.1); the implementer must understand
  this is intentional and not a hook misconfiguration.
- **Coordinate-order swap (§6.4).** Silent class of bugs; only the
  regression test in §6.7 catches it.
- **Field-choice fragility on Natural Earth (§6.6).** Locked field
  fallback order; deviations escalate to spec amendment, not
  implementer judgment.
- **Four projection sites (§4.5).** Adding a column without updating
  all four breaks scans or inserts silently. The plan should bundle
  these into a single task that touches all four sites in one commit.
- **Init time at boot.** 10–20MB GeoJSON parse at startup. The
  benchmark in §6.8 should be run early in the implementation; if
  init exceeds ~1s on the developer machine, escalate before going
  further.
- **F1-vintage rows lack GPS.** Mirrors F2.0's "F1-vintage rows lack
  large.jpg" risk. After F2.1 deploys, operator runs
  `fotobank gps backfill --all-owners --mode=fill-missing` to populate
  them. Documented in the plan as a post-deploy operator action.

## 11. Dependencies

- **Depends on F2.0** for: hoisted `MediaStore`, route to `/media/:id`,
  the `MediaDetail.svelte` stub that this spec replaces, the thumb
  endpoint serving `size=preview`. No F2.0 changes required.
- **Independent of F2.2 / F2.3.** Can run in parallel.
- **F2.4 depends on F2.1** for `location_label` in the lightbox info
  panel. F2.4 can swap `size=preview` → `size=large` in the (by then
  rewritten) `MediaDetail.svelte`.
