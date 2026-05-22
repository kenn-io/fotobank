# F2.1 GPS Metadata Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Capture EXIF GPS, resolve coordinates to coarse country/region/city labels via an embedded offline gazetteer, surface them in the media DTO, and render them on a non-stub `MediaDetail.svelte`.

**Architecture:** Three-tier per-domain (`repo → service → transport`). New leaf package `internal/geo` embeds Natural Earth 1:10m GeoJSON and exposes `*NaturalEarth.Resolve(lat, lon)`. The `PlaceResolver` interface lives in `internal/ingest` (consumer-side) and is injected into `Importer` via constructor. A new `internal/cli/gps.go` cobra subcommand drives backfill through `service.MediaService → media.Repo`. No new background workers, no queues, no network at runtime.

**Tech Stack:** Go 1.23+, modernc.org/sqlite (pure Go), cobra, golang-migrate, huma/v2, testify, dsoprea/go-exif/v3, paulmach/orb (new dep), Svelte 5 runes, TypeScript (strict), Vitest, Playwright.

**Spec:** [docs/superpowers/specs/2026-04-27-fotobank-web-f2.1-gps-metadata-design.md](../specs/2026-04-27-fotobank-web-f2.1-gps-metadata-design.md)

---

## File Structure

**New files:**

- `internal/geo/data/ne_10m_admin_0_countries.geojson` — Natural Earth 1:10m countries.
- `internal/geo/data/ne_10m_admin_1_states_provinces.geojson` — Natural Earth 1:10m admin-1.
- `internal/geo/data/ne_10m_populated_places.geojson` — Natural Earth 1:10m populated places.
- `internal/geo/data/PROVENANCE.md` — source URL, NE release version, conversion command, SHA256s.
- `internal/geo/data/LICENSE` — Natural Earth public-domain terms.
- `internal/geo/geo.go` — `NaturalEarth` type, `NewNaturalEarth`, `Resolve(lat, lon)`. Embeds `data/*.geojson`.
- `internal/geo/geo_test.go` — known-city lookups, ocean, antimeridian, coord-swap regression, init benchmark, checksum verification.
- `internal/exifread/parse_internal_test.go` — internal-package test (`package exifread`) for unexported `parseExif` GPS-validation matrix.
- `internal/cli/gps.go` — `fotobank gps backfill` cobra subcommand.
- `internal/cli/gps_test.go` — CLI tests (bad `--since`, scope/stub bypass, mode behavior, video skip, summary).
- `frontend/src/lib/format/coords.ts` — `formatCoord(lat, lon)`.
- `frontend/src/lib/format/coords.test.ts` — vitest for `formatCoord`.
- `frontend/src/routes/MediaDetail.test.ts` — vitest for the rewritten detail view.
- `testdata/exif/photo-with-gps.jpg` — clean GPS fixture (Paris coords + GPSAt).
- `testdata/exif/photo-null-island-gps.jpg` — `lat==0 && lon==0` fixture asserting drop.

**Modified files:**

- `prek.toml` — exclude `internal/geo/data/*.geojson` from `check-added-large-files` so the vendored ~10MB GeoJSON files commit cleanly without raising the global threshold.
- `internal/db/migrations/000001_initial_schema.up.sql` — add four nullable columns inside `media` (`latitude`, `longitude`, `gps_at`, `location_label`) and a partial index `media_owner_geo_idx`.
- `internal/db/migrations/000001_initial_schema.down.sql` — prior-state snapshot updated to match.
- `internal/media/media.go` — add `Latitude *float64`, `Longitude *float64`, `GPSAt *time.Time`, `LocationLabel string` to `Media`.
- `internal/media/repo.go` — update `mediaSelect`, `mediaColumnsQualified`, `mediaInsert`, `scanMedia`, `Insert`. Add `UpdateGPS`, `GPSBackfillMode` (enum-style int), `ListGPSBackfillCandidates`.
- `internal/album/repo.go` — update `albumMediaMediaSelect` projection.
- `internal/exifread/exifread.go` — extend `Metadata` with `Latitude`, `Longitude`, `GPSAt`.
- `internal/exifread/photo.go` — add GPS parsing in `parseExif`; export `ExtractPhotoFromReader`; refactor `ExtractPhoto` as a thin wrapper.
- `internal/exifread/photo_test.go` — fixture-based asserts (Paris-clean, null-island).
- `internal/ingest/importer.go` — define `PlaceResolver` interface; add third param to `NewImporter`; thread `places` through `processCandidate` / `processPhoto` / `processVideo` to `buildMediaRow`; populate four GPS fields when `meta` has them.
- `internal/ingest/importer_test.go` — pass `nil` for `places` in existing tests; add one new test using a stub resolver.
- `internal/service/media_service.go` — add `UpdateGPS`.
- `internal/service/media_service_test.go` — caller-mismatch returns `errs.ErrNotFound`; caller-match round-trips.
- `internal/cli/import.go` — instantiate `*geo.NaturalEarth` once at command entry; pass into `NewImporter`.
- `internal/cli/cli.go` (or wherever `newRootCmd` lives) — register the new `gps` command group.
- `internal/httpapi/media.go` — extend `mediaDTO` with four GPS fields; populate in `toMediaDTO`.
- `internal/httpapi/media_test.go` — assert list and detail JSON include GPS fields when present and OMIT them (raw-byte assertion) when absent.
- `frontend/src/lib/media/mediaStore.svelte.ts` — extend the inline `Media` type with `thumbVersion: number` and four optional GPS fields; update the `Exclude<keyof Media, …>` guard at line ~71 and the `unchanged` predicate at line ~106; expose `get(id): Media | undefined` backed by a parallel `byMediaId: Map<string, Media>`; update the JSON adapter to populate `thumbVersion`, `latitude`, `longitude`, `gps_at`, `location_label`.
- `frontend/src/lib/media/mediaStore.test.ts` — add tests for `get(id)` and merge field coverage.
- `frontend/src/routes/MediaDetail.svelte` — accept `mediaStore` prop; look up via `mediaStore.get(id)`; fetch `/api/v1/media/{id}` on miss and merge; render photo at `size=preview` + inline-conditional `<dl>`.
- `frontend/src/App.svelte` — pass `mediaStore` prop into `MediaDetail`.
- `frontend/tests/e2e/library.spec.ts` — append two MediaDetail GPS rendering tests (label visible when seeded with GPS; absent when not).
- `openapi.json` — regenerated by the `api-generate` prek hook on commit of any `internal/httpapi/*.go` change.

---

## Pre-flight check

- [ ] **Run baseline test suites and confirm they're green.**

```bash
cd /path/to/fotobank && go test ./... -short -shuffle=on
cd /path/to/fotobank/frontend && bun run typecheck && bun run test
cd /path/to/fotobank/frontend && bunx playwright test
```

Expected: all green. If anything is red on master before starting, stop and fix that first. (Per `CLAUDE.md`, all work commits directly to master; no feature branch.)

> **One-time prerequisite for the migration edit (Task 2).** The `migration-history-check` prek hook (defined in `prek.toml` and implemented at `tools/migrationhistorycheck/main.go`) refuses to commit edits to migration files that already exist on `origin/master`. F2.1 spec §4.1 deliberately edits `000001_initial_schema` because fotobank has not deployed beyond the developer box. To allow this single commit, set `FOTOBANK_MIGRATION_BASE_REF` to a SHA that predates the planned edit (the hook compares the staged file against the file at this ref). Use the SHA of the most recent commit before starting Task 2; record it as `BASE_REF=$(git rev-parse HEAD)` at the very start of the task. The env var is consumed only for the migration-edit commit; later commits in this plan don't need it.

---

## Task 1: Vendor Natural Earth data + provenance + prek hook adjustment

**Files:**
- Create: `internal/geo/data/ne_10m_admin_0_countries.geojson`
- Create: `internal/geo/data/ne_10m_admin_1_states_provinces.geojson`
- Create: `internal/geo/data/ne_10m_populated_places.geojson`
- Create: `internal/geo/data/PROVENANCE.md`
- Create: `internal/geo/data/LICENSE`
- Modify: `prek.toml`

This task vendors the offline gazetteer data the geo package will embed. Natural Earth ships ESRI shapefiles; we convert to GeoJSON via `ogr2ogr` so Go can parse them with `paulmach/orb` (added as a dep in Task 6). The total payload is ~10–15 MB. Because the prek `check-added-large-files` hook is set to `--maxkb=1024` (1 MB), we add an `exclude` regex that lets the three vendored data files through without lowering the safety net for accidental large-file commits elsewhere. Per the spec §6.1, every release upgrade re-runs this same task (re-download, re-checksum, update PROVENANCE).

- [ ] **Step 1: Create the data directory.**

```bash
mkdir -p /path/to/fotobank/internal/geo/data
```

- [ ] **Step 2: Download and convert the Natural Earth 1:10m cultural vectors.**

Run from a scratch working directory (not inside the repo). The exact source URLs and conversion commands are recorded later in `PROVENANCE.md` so future contributors can reproduce the vendoring bit-for-bit:

```bash
WORK=$(mktemp -d) && cd "$WORK"

# Countries (admin_0).
curl -fLOJ https://naciscdn.org/naturalearth/10m/cultural/ne_10m_admin_0_countries.zip
unzip -o ne_10m_admin_0_countries.zip
ogr2ogr -f GeoJSON ne_10m_admin_0_countries.geojson ne_10m_admin_0_countries.shp

# States/provinces (admin_1).
curl -fLOJ https://naciscdn.org/naturalearth/10m/cultural/ne_10m_admin_1_states_provinces.zip
unzip -o ne_10m_admin_1_states_provinces.zip
ogr2ogr -f GeoJSON ne_10m_admin_1_states_provinces.geojson ne_10m_admin_1_states_provinces.shp

# Populated places (cities).
curl -fLOJ https://naciscdn.org/naturalearth/10m/cultural/ne_10m_populated_places.zip
unzip -o ne_10m_populated_places.zip
ogr2ogr -f GeoJSON ne_10m_populated_places.geojson ne_10m_populated_places.shp
```

Copy the three `.geojson` files into `internal/geo/data/`. Discard the rest. (`ogr2ogr` comes from GDAL: `brew install gdal` on macOS.)

- [ ] **Step 3: Compute SHA256s and capture the source-archive SHA256s.**

```bash
cd /path/to/fotobank/internal/geo/data
shasum -a 256 ne_10m_admin_0_countries.geojson \
                ne_10m_admin_1_states_provinces.geojson \
                ne_10m_populated_places.geojson

cd "$WORK"
shasum -a 256 ne_10m_admin_0_countries.zip \
                ne_10m_admin_1_states_provinces.zip \
                ne_10m_populated_places.zip
```

Record both sets of hashes for the next step.

- [ ] **Step 4: Write `PROVENANCE.md`.**

Create `internal/geo/data/PROVENANCE.md` with the actual values from Step 3 substituted in:

```markdown
# Natural Earth gazetteer — provenance

These GeoJSON files are vendored from Natural Earth 1:10m cultural vectors
and embedded into the `fotobank` binary by `internal/geo`. Update this
file every time the data is re-vendored.

## Release

Natural Earth release: **v5.1.2** (latest on naturalearthdata.com at
vendor time). Update this line if a future re-vendor uses a different
release.

## Sources

| File | Source URL | Source archive SHA256 |
|---|---|---|
| `ne_10m_admin_0_countries.geojson` | https://naciscdn.org/naturalearth/10m/cultural/ne_10m_admin_0_countries.zip | `<SHA256-of-zip>` |
| `ne_10m_admin_1_states_provinces.geojson` | https://naciscdn.org/naturalearth/10m/cultural/ne_10m_admin_1_states_provinces.zip | `<SHA256-of-zip>` |
| `ne_10m_populated_places.geojson` | https://naciscdn.org/naturalearth/10m/cultural/ne_10m_populated_places.zip | `<SHA256-of-zip>` |

## Conversion

Each file was produced by:

    ogr2ogr -f GeoJSON <out>.geojson <in>.shp

with default options (WGS84, no projection change). Tested with GDAL
v3.x; later majors should produce byte-identical output for these
inputs.

## Output checksums

Verified by `internal/geo/geo_test.go::TestEmbeddedDataChecksums`. Update
when re-vendoring.

| File | SHA256 |
|---|---|
| `ne_10m_admin_0_countries.geojson` | `<SHA256-of-geojson>` |
| `ne_10m_admin_1_states_provinces.geojson` | `<SHA256-of-geojson>` |
| `ne_10m_populated_places.geojson` | `<SHA256-of-geojson>` |

## License

Natural Earth data is in the public domain. Full terms in `LICENSE` in
this directory; canonical text at
https://www.naturalearthdata.com/about/terms-of-use/.
```

- [ ] **Step 5: Write `LICENSE`.**

Copy the Natural Earth terms-of-use text from the canonical URL above into `internal/geo/data/LICENSE`. The exact text varies year-to-year; pull the current page and save it verbatim.

- [ ] **Step 6: Adjust `prek.toml` to allow the three large data files.**

Open `/path/to/fotobank/prek.toml`. Update the `check-added-large-files` hook entry inside the builtin repos block:

```toml
{ id = "check-added-large-files", args = ["--maxkb=1024"], exclude = "^internal/geo/data/.*\\.geojson$" },
```

The exclude regex is anchored and ends with `\.geojson$` so a future stray binary in the same directory still trips the hook. Other large-file scenarios (e.g. accidental `*.zip` in `data/`) remain blocked.

- [ ] **Step 7: Verify the prek hook accepts the data files in a dry run.**

```bash
cd /path/to/fotobank
git add prek.toml internal/geo/data/
prek run check-added-large-files --files \
    internal/geo/data/ne_10m_admin_0_countries.geojson \
    internal/geo/data/ne_10m_admin_1_states_provinces.geojson \
    internal/geo/data/ne_10m_populated_places.geojson \
    prek.toml
```

Expected: `check-added-large-files .................. Passed`. (`prek` is the same binary the project uses for its hook runner; if `prek` isn't on PATH locally, the user uses `pre-commit run` instead — same flag set.)

- [ ] **Step 8: Commit.**

```bash
cd /path/to/fotobank
git add prek.toml internal/geo/data/
git commit -m "feat(geo): vendor Natural Earth 1:10m gazetteer

Three GeoJSON files (~10MB total) for the F2.1 offline reverse-geocoder.
Public-domain. PROVENANCE.md records source URLs, archive SHA256s,
output SHA256s, and the ogr2ogr conversion command. prek
check-added-large-files now excludes internal/geo/data/*.geojson; other
large-file scenarios remain blocked."
```

Expected: prek hooks pass; commit lands.

---

## Task 2: Migration 000001 — add GPS columns + partial index

**Files:**
- Modify: `internal/db/migrations/000001_initial_schema.up.sql`
- Modify: `internal/db/migrations/000001_initial_schema.down.sql`

The spec §4.1 deliberately edits the existing initial migration rather than adding `000004_gps_columns`. Reason: fotobank has not deployed beyond the developer box, every dev DB will be wiped and re-migrated on the next `make build && fotobank server` boot, and editing in place keeps the schema history clean. The `migration-history-check` prek hook will block this commit by default; we override for this single commit by setting `FOTOBANK_MIGRATION_BASE_REF` to a SHA that predates the staged change.

- [ ] **Step 1: Capture a base ref before staging.**

```bash
cd /path/to/fotobank
BASE_REF=$(git rev-parse HEAD)
echo "BASE_REF=$BASE_REF"   # save for Step 5
```

- [ ] **Step 2: Add the four GPS columns to `000001_initial_schema.up.sql`.**

Open `internal/db/migrations/000001_initial_schema.up.sql`. Inside the `CREATE TABLE media (...)` block, insert four new column definitions between the existing `duration_ms       INTEGER,` line and the blank line that precedes `thumb_status`:

```sql
    duration_ms       INTEGER,

    latitude          REAL,
    longitude         REAL,
    gps_at            TIMESTAMP,
    location_label    TEXT,

    thumb_status      TEXT NOT NULL CHECK (
```

Then, immediately after the existing `CREATE INDEX media_thumb_pending_idx ...` block (which is the last `media`-related index), add the partial geo index:

```sql
CREATE INDEX media_owner_geo_idx
    ON media(owner_hub, owner_user_id, latitude, longitude)
    WHERE latitude IS NOT NULL AND longitude IS NOT NULL;
```

- [ ] **Step 3: Update `000001_initial_schema.down.sql` as a prior-state snapshot.**

Open `internal/db/migrations/000001_initial_schema.down.sql`. The down file rolls the schema back to "nothing existed"; it should be a `DROP TABLE` cascade rather than a column-drop. Verify by reading the existing file — if it already does `DROP TABLE media` (and similar for albums/scopes/etc.), no changes are required because dropping the table also drops the columns. If it instead snapshots a prior schema, update that snapshot to match the post-up state with the four new columns and the new index. **Read the existing file content first; a one-line DROP-style down needs nothing.**

- [ ] **Step 4: Run the migration test to verify the schema applies.**

```bash
cd /path/to/fotobank
go test ./internal/db/... -run TestMigrateUpDown -v
```

Expected: PASS. If `TestMigrateUpDown` doesn't exist, run the broader migration tests:

```bash
go test ./internal/db/... -v
go test ./internal/migrate/... -v
```

Both must be green; the migration framework already exercises up+down on a fresh SQLite DB.

- [ ] **Step 5: Commit with the migration-base-ref override.**

```bash
cd /path/to/fotobank
git add internal/db/migrations/000001_initial_schema.up.sql \
        internal/db/migrations/000001_initial_schema.down.sql
FOTOBANK_MIGRATION_BASE_REF=$BASE_REF git commit -m "feat(db): add GPS columns + partial index to media

Adds latitude, longitude, gps_at, location_label as nullable columns
inside the existing CREATE TABLE media in 000001_initial_schema, plus a
partial index media_owner_geo_idx for owner-scoped geo queries (kept
small by the WHERE latitude IS NOT NULL AND longitude IS NOT NULL
predicate).

Spec §4.1 deliberately edits 000001 rather than adding 000004 because
fotobank has not deployed beyond the developer box. The
migration-history-check prek hook is bypassed for this single commit
via FOTOBANK_MIGRATION_BASE_REF. Subsequent commits do not need this
override."
```

Expected: prek hooks pass (the migration-history-check sees `BASE_REF` lacks the staged file, so 'edits to migration on main' is not flagged). Commit lands.

---

## Task 3: media.Media struct + repo plumbing (4 projection sites)

**Files:**
- Modify: `internal/media/media.go`
- Modify: `internal/media/repo.go`
- Modify: `internal/album/repo.go`
- Modify: `internal/media/repo_test.go` (may already exist)

This task threads the four new GPS columns through every read/write site in the media repo and the album repo's media projection. The "four sites that must stay in sync" risk from spec §4.5 is the highest correctness risk in the slice; we update them all in one commit so a bisect lands on a fully consistent state.

- [ ] **Step 1: Extend `media.Media` with four fields.**

Open `internal/media/media.go`. After `DurationMs *int64`, add:

```go
	DurationMs  *int64

	// GPS metadata. Latitude and Longitude are atomic — emitted
	// together or not at all. GPSAt is independent. LocationLabel is
	// the offline-resolved country/region/city string from internal/geo;
	// empty when no resolver match.
	Latitude       *float64
	Longitude      *float64
	GPSAt          *time.Time
	LocationLabel  string
```

- [ ] **Step 2: Update `mediaSelect` projection in `internal/media/repo.go`.**

The existing const ends with `thumb_updated_at`. Add the four new columns alongside the photo metadata block:

```go
const mediaSelect = `SELECT
	id, owner_hub, owner_user_id, media_type, mime_type, path, original_filename,
	imported_at, timestamp, size, checksum,
	make, model, focal_length, shutter, width, height, iso, aperture,
	duration_ms,
	latitude, longitude, gps_at, location_label,
	thumb_status, thumb_version, thumb_updated_at
FROM media`
```

- [ ] **Step 3: Update `mediaColumnsQualified` projection.**

```go
const mediaColumnsQualified = `
    m.id, m.owner_hub, m.owner_user_id, m.media_type, m.mime_type, m.path, m.original_filename,
    m.imported_at, m.timestamp, m.size, m.checksum,
    m.make, m.model, m.focal_length, m.shutter, m.width, m.height, m.iso, m.aperture,
    m.duration_ms,
    m.latitude, m.longitude, m.gps_at, m.location_label,
    m.thumb_status, m.thumb_version, m.thumb_updated_at`
```

- [ ] **Step 4: Update `mediaInsert` (column list, placeholders, and `Insert` arg list).**

```go
const mediaInsert = `INSERT INTO media (
	id, owner_hub, owner_user_id, media_type, mime_type, path, original_filename,
	imported_at, timestamp, size, checksum,
	make, model, focal_length, shutter, width, height, iso, aperture,
	duration_ms,
	latitude, longitude, gps_at, location_label,
	thumb_status, thumb_version, thumb_updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
```

(27 placeholders — count them.) In `Insert`, after the `nullInt64(m.DurationMs),` line, add four new args matching the column order:

```go
		nullInt64(m.DurationMs),
		nullFloat(m.Latitude),
		nullFloat(m.Longitude),
		nullTime(m.GPSAt),
		nullStr(m.LocationLabel),
		m.ThumbStatus,
```

- [ ] **Step 5: Update `scanMedia` to read the four columns.**

In `scanMedia`, declare four new locals next to the existing nullables:

```go
	var (
		m                Media
		mediaType        string
		originalFilename sql.NullString
		timestamp        sql.NullTime
		makeN            sql.NullString
		modelN           sql.NullString
		focalLength      sql.NullString
		shutter          sql.NullString
		width            sql.NullInt64
		height           sql.NullInt64
		iso              sql.NullInt64
		aperture         sql.NullFloat64
		durationMs       sql.NullInt64
		latitude         sql.NullFloat64
		longitude        sql.NullFloat64
		gpsAt            sql.NullTime
		locationLabel    sql.NullString
		thumbUpdatedAt   sql.NullTime
	)
```

In the `s.Scan(...)` call, add four new pointers in column order, between `&durationMs` and `&m.ThumbStatus`:

```go
		&durationMs,
		&latitude,
		&longitude,
		&gpsAt,
		&locationLabel,
		&m.ThumbStatus,
```

After the existing `if durationMs.Valid { ... }` block, populate the new fields:

```go
	if latitude.Valid {
		v := latitude.Float64
		m.Latitude = &v
	}
	if longitude.Valid {
		v := longitude.Float64
		m.Longitude = &v
	}
	if gpsAt.Valid {
		t := gpsAt.Time
		m.GPSAt = &t
	}
	m.LocationLabel = locationLabel.String
```

(`locationLabel.String` is `""` when `Valid==false`; the `nullStr` round-trip preserves that.)

- [ ] **Step 6: Update `albumMediaMediaSelect` in `internal/album/repo.go`.**

Open `internal/album/repo.go` around line 385:

```go
const albumMediaMediaSelect = `SELECT
    m.id, m.owner_hub, m.owner_user_id, m.media_type, m.mime_type, m.path, m.original_filename,
    m.imported_at, m.timestamp, m.size, m.checksum,
    m.make, m.model, m.focal_length, m.shutter, m.width, m.height, m.iso, m.aperture,
    m.duration_ms,
    m.latitude, m.longitude, m.gps_at, m.location_label,
    m.thumb_status, m.thumb_version, m.thumb_updated_at
FROM album_media am JOIN media m ON m.id = am.media_id`
```

Update the cross-reference comment immediately above to acknowledge it now covers four projection sites:

```go
// albumMediaMediaSelect projects media columns joined via album_media.
// Kept in sync with media.mediaSelect, media.mediaColumnsQualified, and
// media.mediaInsert (four-way sync applies on any schema column
// add/remove).
```

- [ ] **Step 7: Add a round-trip test in `internal/media/repo_test.go`.**

If a `TestRepoInsertGetByIDRoundTrip` (or similar) test already exists, extend it to populate the four GPS fields. Otherwise add a fresh test:

```go
func TestRepoInsertGetByIDPreservesGPS(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	owner := owners.Principal{Hub: "h", UserID: "u"}
	_, err := d.WriteDB().ExecContext(context.Background(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		owner.Hub, owner.UserID, "sk", time.Now().UTC(),
	)
	r.NoError(err)

	lat, lon := 48.8566, 2.3522
	gps := time.Date(2024, 6, 15, 14, 30, 22, 0, time.UTC)
	id := uuid.NewString()
	r.NoError(repo.Insert(context.Background(), media.Media{
		ID: id, Owner: owner, Type: media.TypePhoto, MimeType: "image/jpeg",
		Path: "x.jpg", ImportedAt: time.Now().UTC(), Size: 1, Checksum: "c-" + id,
		Latitude: &lat, Longitude: &lon, GPSAt: &gps,
		LocationLabel: "Paris, Île-de-France, France",
		ThumbStatus:   "pending",
	}))

	got, err := repo.GetByID(context.Background(), id)
	r.NoError(err)
	r.NotNil(got.Latitude)
	r.NotNil(got.Longitude)
	r.NotNil(got.GPSAt)
	r.InDelta(48.8566, *got.Latitude, 1e-9)
	r.InDelta(2.3522, *got.Longitude, 1e-9)
	r.True(got.GPSAt.Equal(gps), "got %v", got.GPSAt)
	r.Equal("Paris, Île-de-France, France", got.LocationLabel)
}

func TestRepoInsertGetByIDPreservesAbsentGPS(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	owner := owners.Principal{Hub: "h", UserID: "u"}
	_, err := d.WriteDB().ExecContext(context.Background(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		owner.Hub, owner.UserID, "sk", time.Now().UTC(),
	)
	r.NoError(err)

	id := uuid.NewString()
	r.NoError(repo.Insert(context.Background(), media.Media{
		ID: id, Owner: owner, Type: media.TypePhoto, MimeType: "image/jpeg",
		Path: "x.jpg", ImportedAt: time.Now().UTC(), Size: 1, Checksum: "c-" + id,
		ThumbStatus: "pending",
		// no GPS fields
	}))

	got, err := repo.GetByID(context.Background(), id)
	r.NoError(err)
	r.Nil(got.Latitude)
	r.Nil(got.Longitude)
	r.Nil(got.GPSAt)
	r.Empty(got.LocationLabel)
}
```

- [ ] **Step 8: Run the media repo tests and the wider build to catch column-count mismatches.**

```bash
cd /path/to/fotobank
go test ./internal/media/... -shuffle=on
go build ./...
```

Expected: PASS / no build errors. If the build fails with "column index out of range" or "expected N args, got M" in `Insert`, the placeholder count or argument count drifted from the column list — fix and rerun.

- [ ] **Step 9: Run the album repo tests to confirm the joined projection still scans cleanly.**

```bash
go test ./internal/album/... -shuffle=on
```

Expected: PASS.

- [ ] **Step 10: Commit.**

```bash
git add internal/media/media.go internal/media/repo.go internal/media/repo_test.go internal/album/repo.go
git commit -m "feat(media): thread GPS columns through media + album repos

Adds Latitude/Longitude/GPSAt/LocationLabel to media.Media and updates
the four projection/write sites that must stay in sync per spec §4.5:
mediaSelect, mediaColumnsQualified, mediaInsert (+ Insert arg list +
scanMedia), and album.albumMediaMediaSelect. Round-trip tests cover
both populated and absent GPS."
```

Expected: prek hooks pass; commit lands.

---

## Task 4: media.Repo new methods (UpdateGPS + ListGPSBackfillCandidates)

**Files:**
- Modify: `internal/media/repo.go`
- Modify: `internal/media/repo_test.go`

The backfill CLI needs two new repo methods: `UpdateGPS` to write back GPS state for one row, and `ListGPSBackfillCandidates` to enumerate target rows for the three modes (full / fill-missing / relabel) without leaking those semantics into the public `ListFilter`. Both go on `*media.Repo`.

- [ ] **Step 1: Define the `GPSBackfillMode` type and add `UpdateGPS`.**

Append to `internal/media/repo.go` (anywhere after the existing `Delete` method):

```go
// GPSBackfillMode discriminates what `gps backfill` considers a target.
// See spec §4.7 / §7.4.
type GPSBackfillMode int

const (
	// GPSBackfillModeFull selects every photo row regardless of GPS state.
	GPSBackfillModeFull GPSBackfillMode = iota
	// GPSBackfillModeFillMissing selects only photo rows where BOTH
	// latitude AND longitude are NULL. Rows with one coord set are
	// partial state from a prior run and are intentionally not targets
	// of fill-missing.
	GPSBackfillModeFillMissing
	// GPSBackfillModeRelabel selects only photo rows where lat AND lon
	// are NOT NULL. Used to refresh location_label after the embedded
	// gazetteer is bumped.
	GPSBackfillModeRelabel
)

// UpdateGPS sets the four GPS columns on an existing row. Used by the
// backfill CLI; the importer uses Insert. Returns errs.ErrNotFound if
// the row is gone.
func (r *Repo) UpdateGPS(
	ctx context.Context,
	id string,
	lat, lon *float64,
	gpsAt *time.Time,
	label string,
) error {
	res, err := r.rw.ExecContext(ctx,
		`UPDATE media
		    SET latitude = ?, longitude = ?, gps_at = ?, location_label = ?
		  WHERE id = ?`,
		nullFloat(lat),
		nullFloat(lon),
		nullTime(gpsAt),
		nullStr(label),
		id,
	)
	if err != nil {
		return fmt.Errorf("update media gps: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update media gps rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("%w: media id=%s", errs.ErrNotFound, id)
	}
	return nil
}
```

- [ ] **Step 2: Add `ListGPSBackfillCandidates`.**

Append after `UpdateGPS`:

```go
// ListGPSBackfillCandidates enumerates rows for `gps backfill`. Always
// excludes media_type='video' (video GPS is out of scope per spec §5.5).
// Pages via LIMIT/OFFSET; rows are ordered by id for stable paging.
//
// `since` filters by imported_at >= *since; pass nil to disable.
func (r *Repo) ListGPSBackfillCandidates(
	ctx context.Context,
	owner owners.Principal,
	mode GPSBackfillMode,
	since *time.Time,
	limit, offset int,
) ([]Media, error) {
	conds := []string{
		"owner_hub = ?",
		"owner_user_id = ?",
		"media_type = 'photo'",
	}
	args := []any{owner.Hub, owner.UserID}

	switch mode {
	case GPSBackfillModeFull:
		// no GPS predicate
	case GPSBackfillModeFillMissing:
		conds = append(conds, "latitude IS NULL AND longitude IS NULL")
	case GPSBackfillModeRelabel:
		conds = append(conds, "latitude IS NOT NULL AND longitude IS NOT NULL")
	default:
		return nil, fmt.Errorf("list gps backfill candidates: unknown mode %d", mode)
	}
	if since != nil {
		conds = append(conds, "imported_at >= ?")
		args = append(args, *since)
	}

	if limit <= 0 {
		limit = defaultListLimit
	}
	offset = max(offset, 0)

	query := mediaSelect +
		" WHERE " + strings.Join(conds, " AND ") +
		" ORDER BY id LIMIT ? OFFSET ?"
	args = append(args, limit, offset)

	rows, err := r.ro.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list gps backfill candidates: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Media
	for rows.Next() {
		m, err := scanMedia(rows)
		if err != nil {
			return nil, fmt.Errorf("scan gps backfill candidate: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate gps backfill candidates: %w", err)
	}
	return out, nil
}
```

- [ ] **Step 3: Add tests for `UpdateGPS`.**

Append to `internal/media/repo_test.go`:

```go
func TestUpdateGPSRoundTrips(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	owner := owners.Principal{Hub: "h", UserID: "u"}
	_, err := d.WriteDB().ExecContext(context.Background(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		owner.Hub, owner.UserID, "sk", time.Now().UTC(),
	)
	r.NoError(err)

	id := uuid.NewString()
	r.NoError(repo.Insert(context.Background(), media.Media{
		ID: id, Owner: owner, Type: media.TypePhoto, MimeType: "image/jpeg",
		Path: "x.jpg", ImportedAt: time.Now().UTC(), Size: 1, Checksum: "c-" + id,
		ThumbStatus: "pending",
	}))

	lat, lon := 48.8566, 2.3522
	gps := time.Date(2024, 6, 15, 14, 30, 22, 0, time.UTC)
	r.NoError(repo.UpdateGPS(context.Background(), id, &lat, &lon, &gps, "Paris, France"))

	got, err := repo.GetByID(context.Background(), id)
	r.NoError(err)
	r.NotNil(got.Latitude)
	r.InDelta(48.8566, *got.Latitude, 1e-9)
	r.InDelta(2.3522, *got.Longitude, 1e-9)
	r.True(got.GPSAt.Equal(gps))
	r.Equal("Paris, France", got.LocationLabel)
}

func TestUpdateGPSClearsAllFieldsWhenNil(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	owner := owners.Principal{Hub: "h", UserID: "u"}
	_, err := d.WriteDB().ExecContext(context.Background(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		owner.Hub, owner.UserID, "sk", time.Now().UTC(),
	)
	r.NoError(err)

	lat, lon := 1.0, 2.0
	id := uuid.NewString()
	r.NoError(repo.Insert(context.Background(), media.Media{
		ID: id, Owner: owner, Type: media.TypePhoto, MimeType: "image/jpeg",
		Path: "x.jpg", ImportedAt: time.Now().UTC(), Size: 1, Checksum: "c-" + id,
		Latitude: &lat, Longitude: &lon, LocationLabel: "Old", ThumbStatus: "pending",
	}))

	r.NoError(repo.UpdateGPS(context.Background(), id, nil, nil, nil, ""))

	got, err := repo.GetByID(context.Background(), id)
	r.NoError(err)
	r.Nil(got.Latitude)
	r.Nil(got.Longitude)
	r.Nil(got.GPSAt)
	r.Empty(got.LocationLabel)
}

func TestUpdateGPSReturnsNotFoundForMissingRow(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())

	err := repo.UpdateGPS(context.Background(), "no-such-id", nil, nil, nil, "")
	r.ErrorIs(err, errs.ErrNotFound)
}
```

- [ ] **Step 4: Add tests for `ListGPSBackfillCandidates` covering each mode + video skip + since filter.**

```go
func TestListGPSBackfillCandidatesByMode(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	owner := owners.Principal{Hub: "h", UserID: "u"}
	_, err := d.WriteDB().ExecContext(context.Background(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		owner.Hub, owner.UserID, "sk", time.Now().UTC(),
	)
	r.NoError(err)

	mk := func(id string, t media.Type, lat, lon *float64) {
		r.NoError(repo.Insert(context.Background(), media.Media{
			ID: id, Owner: owner, Type: t, MimeType: "image/jpeg",
			Path: id + ".jpg", ImportedAt: time.Now().UTC(),
			Size: 1, Checksum: "c-" + id,
			Latitude: lat, Longitude: lon, ThumbStatus: "pending",
		}))
	}
	one := 1.0
	mk("photo-no-gps", media.TypePhoto, nil, nil)
	mk("photo-with-gps", media.TypePhoto, &one, &one)
	mk("video-with-gps", media.TypeVideo, &one, &one) // must be excluded
	// Partial-coord row exists in theory but Insert preserves whatever
	// caller passed; this test confirms fill-missing still skips it.
	mk("photo-partial-coord", media.TypePhoto, &one, nil)

	full, err := repo.ListGPSBackfillCandidates(context.Background(), owner, media.GPSBackfillModeFull, nil, 100, 0)
	r.NoError(err)
	ids := func(ms []media.Media) []string {
		out := make([]string, 0, len(ms))
		for _, m := range ms {
			out = append(out, m.ID)
		}
		return out
	}
	r.ElementsMatch([]string{"photo-no-gps", "photo-with-gps", "photo-partial-coord"}, ids(full))

	missing, err := repo.ListGPSBackfillCandidates(context.Background(), owner, media.GPSBackfillModeFillMissing, nil, 100, 0)
	r.NoError(err)
	r.ElementsMatch([]string{"photo-no-gps"}, ids(missing))

	relabel, err := repo.ListGPSBackfillCandidates(context.Background(), owner, media.GPSBackfillModeRelabel, nil, 100, 0)
	r.NoError(err)
	r.ElementsMatch([]string{"photo-with-gps"}, ids(relabel))
}

func TestListGPSBackfillCandidatesSinceFilter(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	owner := owners.Principal{Hub: "h", UserID: "u"}
	_, err := d.WriteDB().ExecContext(context.Background(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		owner.Hub, owner.UserID, "sk", time.Now().UTC(),
	)
	r.NoError(err)

	old := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	new := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	r.NoError(repo.Insert(context.Background(), media.Media{
		ID: "old", Owner: owner, Type: media.TypePhoto, MimeType: "image/jpeg",
		Path: "old.jpg", ImportedAt: old, Size: 1, Checksum: "c-old",
		ThumbStatus: "pending",
	}))
	r.NoError(repo.Insert(context.Background(), media.Media{
		ID: "new", Owner: owner, Type: media.TypePhoto, MimeType: "image/jpeg",
		Path: "new.jpg", ImportedAt: new, Size: 1, Checksum: "c-new",
		ThumbStatus: "pending",
	}))

	cutoff := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	got, err := repo.ListGPSBackfillCandidates(context.Background(), owner, media.GPSBackfillModeFull, &cutoff, 100, 0)
	r.NoError(err)
	r.Len(got, 1)
	r.Equal("new", got[0].ID)
}
```

- [ ] **Step 5: Run repo tests.**

```bash
cd /path/to/fotobank
go test ./internal/media/... -shuffle=on -run "TestUpdateGPS|TestListGPSBackfillCandidates"
```

Expected: PASS.

- [ ] **Step 6: Commit.**

```bash
git add internal/media/repo.go internal/media/repo_test.go
git commit -m "feat(media): add UpdateGPS + ListGPSBackfillCandidates repo methods

UpdateGPS sets all four GPS columns atomically; ErrNotFound when the
row is gone. ListGPSBackfillCandidates enumerates target rows for the
three backfill modes (full / fill-missing / relabel), always excludes
videos, and supports a --since cutoff against imported_at."
```

---

## Task 5: exifread — Metadata GPS fields + parseExif GPS validation + ExtractPhotoFromReader

**Files:**
- Modify: `internal/exifread/exifread.go`
- Modify: `internal/exifread/photo.go`
- Modify: `internal/exifread/photo_test.go`
- Create: `internal/exifread/parse_internal_test.go`
- Create: `testdata/exif/photo-with-gps.jpg` (binary fixture)
- Create: `testdata/exif/photo-null-island-gps.jpg` (binary fixture)

This task adds GPS parsing to `parseExif`, exports `ExtractPhotoFromReader` so the backfill CLI can feed bytes from `storage.Store.ReadRange` directly, and locks the validation matrix from spec §5.3 with table tests in an internal-package test file. Two real-fixture JPEGs cover the integration path.

- [ ] **Step 1: Extend `Metadata` with three new fields.**

Open `internal/exifread/exifread.go`. After the existing `DurationMs int64` line, add:

```go
	// Videos.
	DurationMs int64

	// GPS — populated when the EXIF GPS IFD parses cleanly. Latitude
	// and Longitude are atomic: emitted together or not at all. GPSAt
	// is independent and may be nil even when coords are set.
	Latitude  *float64
	Longitude *float64
	GPSAt     *time.Time
}
```

- [ ] **Step 2: Refactor `ExtractPhoto` into a thin wrapper around a new `ExtractPhotoFromReader`.**

Open `internal/exifread/photo.go`. Replace the body of `ExtractPhoto`:

```go
// ExtractPhoto reads EXIF from the given path and returns the
// normalised metadata. Files without an EXIF segment return an
// empty Metadata with a nil error; only read/parse errors surface.
func ExtractPhoto(path string) (Metadata, error) {
	f, err := os.Open(path)
	if err != nil {
		return Metadata{}, err
	}
	defer f.Close()
	return ExtractPhotoFromReader(f)
}

// ExtractPhotoFromReader is identical to ExtractPhoto but reads from r
// instead of a file path. Used by the gps backfill CLI to stream EXIF
// from storage.Store.ReadRange without an intermediate temp file.
func ExtractPhotoFromReader(r io.Reader) (Metadata, error) {
	raw, err := exif.SearchAndExtractExifWithReader(r)
	if err != nil {
		if errors.Is(err, exif.ErrNoExif) {
			return Metadata{}, nil
		}
		return Metadata{}, fmt.Errorf("search exif: %w", err)
	}
	return parseExif(raw)
}
```

Add `"io"` to the import block if not already present.

- [ ] **Step 3: Add GPS parsing inside `parseExif`.**

In `parseExif`, after the existing `if _, ok := by["PreviewImageStart"]; ok { ... }` block (the embedded-preview detection), add a call to a new helper:

```go
	if _, ok := by["PreviewImageStart"]; ok {
		m.HasEmbeddedPreview = true
	} else if _, ok := by["ThumbnailImageStart"]; ok {
		m.HasEmbeddedPreview = true
	}
	if lat, lon, ok := parseExifGPSCoords(by); ok {
		m.Latitude = &lat
		m.Longitude = &lon
	}
	if t, ok := parseExifGPSTimestamp(by); ok {
		m.GPSAt = &t
	}
	return m, nil
}
```

Then define the two helpers below `parseExifTimestamp`:

```go
// parseExifGPSCoords enforces the validation matrix from F2.1 spec §5.3.
// Returns (lat, lon, true) only when every condition holds: rationals
// present and non-zero-denominator, refs are exactly N|S and E|W,
// resulting decimals are in -90..90 / -180..180, and (lat, lon) is not
// the literal null-island origin.
func parseExifGPSCoords(by map[string]exif.ExifTag) (float64, float64, bool) {
	latRaw, ok1 := by["GPSLatitude"]
	lonRaw, ok2 := by["GPSLongitude"]
	latRefRaw, ok3 := by["GPSLatitudeRef"]
	lonRefRaw, ok4 := by["GPSLongitudeRef"]
	if !ok1 || !ok2 || !ok3 || !ok4 {
		return 0, 0, false
	}
	latDMS, ok := dmsRationals(latRaw)
	if !ok {
		return 0, 0, false
	}
	lonDMS, ok := dmsRationals(lonRaw)
	if !ok {
		return 0, 0, false
	}
	latRef, ok := strictRef(latRefRaw, "N", "S")
	if !ok {
		return 0, 0, false
	}
	lonRef, ok := strictRef(lonRefRaw, "E", "W")
	if !ok {
		return 0, 0, false
	}
	lat, ok := dmsToDecimal(latDMS, latRef)
	if !ok {
		return 0, 0, false
	}
	lon, ok := dmsToDecimal(lonDMS, lonRef)
	if !ok {
		return 0, 0, false
	}
	if lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		return 0, 0, false
	}
	if lat == 0 && lon == 0 {
		// Null-island anti-pattern: cameras often emit literal 0,0
		// before GPS has acquired a fix. We prefer to lose any real
		// 0,0 photo over ingesting noise on every broken-fix camera.
		return 0, 0, false
	}
	return lat, lon, true
}

// dmsRationals extracts a degrees/minutes/seconds triple. Returns ok=false
// if the value is not three rationals or if any denominator is zero.
func dmsRationals(t exif.ExifTag) ([3]exifcommon.Rational, bool) {
	rs, ok := t.Value.([]exifcommon.Rational)
	if !ok || len(rs) < 3 {
		return [3]exifcommon.Rational{}, false
	}
	for i := 0; i < 3; i++ {
		if rs[i].Denominator == 0 {
			return [3]exifcommon.Rational{}, false
		}
	}
	return [3]exifcommon.Rational{rs[0], rs[1], rs[2]}, true
}

// strictRef returns the value if it is exactly one of the two allowed
// strings (length-1, case-sensitive); otherwise ok=false.
func strictRef(t exif.ExifTag, a, b string) (string, bool) {
	s, ok := t.Value.(string)
	if !ok {
		return "", false
	}
	if s == a || s == b {
		return s, true
	}
	return "", false
}

// dmsToDecimal converts a DMS rational triple + cardinal ref into a
// signed decimal degree. Caller has already validated denominators
// are non-zero (see dmsRationals).
func dmsToDecimal(dms [3]exifcommon.Rational, ref string) (float64, bool) {
	deg := float64(dms[0].Numerator) / float64(dms[0].Denominator)
	min := float64(dms[1].Numerator) / float64(dms[1].Denominator)
	sec := float64(dms[2].Numerator) / float64(dms[2].Denominator)
	v := deg + min/60.0 + sec/3600.0
	if ref == "S" || ref == "W" {
		v = -v
	}
	return v, true
}

// parseExifGPSTimestamp combines GPSDateStamp ("YYYY:MM:DD") and
// GPSTimeStamp (rational triple, UTC) into a single time.Time. Returns
// (zero, false) on any validation failure: missing tags, zero
// denominators, or out-of-range hour/minute/second.
func parseExifGPSTimestamp(by map[string]exif.ExifTag) (time.Time, bool) {
	dateRaw, ok := by["GPSDateStamp"]
	if !ok {
		return time.Time{}, false
	}
	dateStr, ok := dateRaw.Value.(string)
	if !ok {
		return time.Time{}, false
	}
	d, err := time.ParseInLocation("2006:01:02", dateStr, time.UTC)
	if err != nil {
		return time.Time{}, false
	}
	timeRaw, ok := by["GPSTimeStamp"]
	if !ok {
		return time.Time{}, false
	}
	hms, ok := dmsRationals(timeRaw)
	if !ok {
		return time.Time{}, false
	}
	hour := float64(hms[0].Numerator) / float64(hms[0].Denominator)
	minute := float64(hms[1].Numerator) / float64(hms[1].Denominator)
	second := float64(hms[2].Numerator) / float64(hms[2].Denominator)
	if hour < 0 || hour >= 24 {
		return time.Time{}, false
	}
	if minute < 0 || minute >= 60 {
		return time.Time{}, false
	}
	if second < 0 || second >= 60 {
		return time.Time{}, false
	}
	return time.Date(d.Year(), d.Month(), d.Day(),
		int(hour), int(minute), int(second), 0, time.UTC), true
}
```

- [ ] **Step 4: Add `internal/exifread/parse_internal_test.go` with the validation matrix.**

```go
package exifread

import (
	"testing"

	exif "github.com/dsoprea/go-exif/v3"
	exifcommon "github.com/dsoprea/go-exif/v3/common"
	"github.com/stretchr/testify/require"
)

// rational makes a rational tag value; denom=0 is allowed (callers test
// that we reject it).
func rational(num, denom int64) exif.ExifTag {
	return exif.ExifTag{Value: []exifcommon.Rational{{
		Numerator: uint32(num), Denominator: uint32(denom),
	}}}
}

// dms3 makes a DMS triple for use as GPSLatitude / GPSLongitude /
// GPSTimeStamp.
func dms3(d, m, s int64) exif.ExifTag {
	return exif.ExifTag{Value: []exifcommon.Rational{
		{Numerator: uint32(d), Denominator: 1},
		{Numerator: uint32(m), Denominator: 1},
		{Numerator: uint32(s), Denominator: 1},
	}}
}

func dms3Denom(d, dDen, m, mDen, s, sDen int64) exif.ExifTag {
	return exif.ExifTag{Value: []exifcommon.Rational{
		{Numerator: uint32(d), Denominator: uint32(dDen)},
		{Numerator: uint32(m), Denominator: uint32(mDen)},
		{Numerator: uint32(s), Denominator: uint32(sDen)},
	}}
}

func strTag(s string) exif.ExifTag { return exif.ExifTag{Value: s} }

func TestParseExifGPSCoordsValidation(t *testing.T) {
	type tc struct {
		name string
		by   map[string]exif.ExifTag
		ok   bool
		lat  float64
		lon  float64
	}
	clean := map[string]exif.ExifTag{
		"GPSLatitude":     dms3(48, 51, 24),
		"GPSLatitudeRef":  strTag("N"),
		"GPSLongitude":    dms3(2, 21, 8),
		"GPSLongitudeRef": strTag("E"),
	}
	cases := []tc{
		{"clean fix Paris-ish", clean, true, 48.856_667, 2.352_222},
		{"missing latitude", map[string]exif.ExifTag{
			"GPSLatitudeRef": strTag("N"), "GPSLongitude": dms3(2, 21, 8), "GPSLongitudeRef": strTag("E"),
		}, false, 0, 0},
		{"missing longitude ref", map[string]exif.ExifTag{
			"GPSLatitude": dms3(48, 51, 24), "GPSLatitudeRef": strTag("N"), "GPSLongitude": dms3(2, 21, 8),
		}, false, 0, 0},
		{"latitude zero denominator", map[string]exif.ExifTag{
			"GPSLatitude": dms3Denom(48, 0, 51, 1, 24, 1), "GPSLatitudeRef": strTag("N"),
			"GPSLongitude": dms3(2, 21, 8), "GPSLongitudeRef": strTag("E"),
		}, false, 0, 0},
		{"latitude ref garbage", map[string]exif.ExifTag{
			"GPSLatitude": dms3(48, 51, 24), "GPSLatitudeRef": strTag("Q"),
			"GPSLongitude": dms3(2, 21, 8), "GPSLongitudeRef": strTag("E"),
		}, false, 0, 0},
		{"latitude ref lowercase", map[string]exif.ExifTag{
			"GPSLatitude": dms3(48, 51, 24), "GPSLatitudeRef": strTag("n"),
			"GPSLongitude": dms3(2, 21, 8), "GPSLongitudeRef": strTag("E"),
		}, false, 0, 0},
		{"latitude out of range", map[string]exif.ExifTag{
			"GPSLatitude": dms3(91, 0, 0), "GPSLatitudeRef": strTag("N"),
			"GPSLongitude": dms3(2, 21, 8), "GPSLongitudeRef": strTag("E"),
		}, false, 0, 0},
		{"longitude out of range", map[string]exif.ExifTag{
			"GPSLatitude": dms3(48, 51, 24), "GPSLatitudeRef": strTag("N"),
			"GPSLongitude": dms3(181, 0, 0), "GPSLongitudeRef": strTag("E"),
		}, false, 0, 0},
		{"null island", map[string]exif.ExifTag{
			"GPSLatitude": dms3(0, 0, 0), "GPSLatitudeRef": strTag("N"),
			"GPSLongitude": dms3(0, 0, 0), "GPSLongitudeRef": strTag("E"),
		}, false, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := require.New(t)
			lat, lon, ok := parseExifGPSCoords(c.by)
			r.Equal(c.ok, ok)
			if ok {
				r.InDelta(c.lat, lat, 1e-3)
				r.InDelta(c.lon, lon, 1e-3)
			}
		})
	}
}

func TestParseExifGPSTimestampValidation(t *testing.T) {
	type tc struct {
		name string
		by   map[string]exif.ExifTag
		ok   bool
	}
	clean := map[string]exif.ExifTag{
		"GPSDateStamp": strTag("2024:06:15"),
		"GPSTimeStamp": dms3(14, 30, 22),
	}
	cases := []tc{
		{"clean", clean, true},
		{"missing date", map[string]exif.ExifTag{"GPSTimeStamp": dms3(14, 30, 22)}, false},
		{"missing time", map[string]exif.ExifTag{"GPSDateStamp": strTag("2024:06:15")}, false},
		{"date parse fails", map[string]exif.ExifTag{
			"GPSDateStamp": strTag("not-a-date"), "GPSTimeStamp": dms3(14, 30, 22),
		}, false},
		{"hour out of range", map[string]exif.ExifTag{
			"GPSDateStamp": strTag("2024:06:15"), "GPSTimeStamp": dms3(25, 0, 0),
		}, false},
		{"minute out of range", map[string]exif.ExifTag{
			"GPSDateStamp": strTag("2024:06:15"), "GPSTimeStamp": dms3(14, 60, 0),
		}, false},
		{"second out of range", map[string]exif.ExifTag{
			"GPSDateStamp": strTag("2024:06:15"), "GPSTimeStamp": dms3(14, 30, 60),
		}, false},
		{"hour zero denominator", map[string]exif.ExifTag{
			"GPSDateStamp": strTag("2024:06:15"),
			"GPSTimeStamp": dms3Denom(14, 0, 30, 1, 22, 1),
		}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := require.New(t)
			_, ok := parseExifGPSTimestamp(c.by)
			r.Equal(c.ok, ok)
		})
	}
}

func TestParseExifGPSCleanTimestampInUTC(t *testing.T) {
	r := require.New(t)
	by := map[string]exif.ExifTag{
		"GPSDateStamp": strTag("2024:06:15"),
		"GPSTimeStamp": dms3(14, 30, 22),
	}
	got, ok := parseExifGPSTimestamp(by)
	r.True(ok)
	r.Equal(time.UTC, got.Location())
	r.Equal(2024, got.Year())
	r.Equal(time.June, got.Month())
	r.Equal(15, got.Day())
	r.Equal(14, got.Hour())
	r.Equal(30, got.Minute())
	r.Equal(22, got.Second())
}
```

Add the `time` import to the file.

- [ ] **Step 5: Acquire the binary fixtures.**

The implementer must produce two JPEG fixtures with EXIF GPS:

1. `testdata/exif/photo-with-gps.jpg` — any small JPEG (≤50KB) carrying GPSLatitude / GPSLongitude / GPSLatitudeRef / GPSLongitudeRef tags resolving to roughly 48.8566, 2.3522 (Paris) plus GPSDateStamp + GPSTimeStamp resolving to 2024-06-15 14:30:22 UTC. Easiest path: take an existing camera JPEG and rewrite the GPS tags using `exiftool`:

   ```bash
   exiftool -GPSLatitude=48.8566 -GPSLatitudeRef=N \
            -GPSLongitude=2.3522 -GPSLongitudeRef=E \
            -GPSDateStamp="2024:06:15" \
            -GPSTimeStamp="14:30:22" \
            -overwrite_original photo-with-gps.jpg
   ```

2. `testdata/exif/photo-null-island-gps.jpg` — same flow but with lat=0 / lon=0:

   ```bash
   exiftool -GPSLatitude=0 -GPSLatitudeRef=N \
            -GPSLongitude=0 -GPSLongitudeRef=E \
            -overwrite_original photo-null-island-gps.jpg
   ```

The fixtures are checked in. Keep both small (<50KB) so they don't trip the large-file hook.

- [ ] **Step 6: Add fixture-driven tests in `photo_test.go`.**

Append to `internal/exifread/photo_test.go`:

```go
func TestExtractPhotoParsesGPS(t *testing.T) {
	r := require.New(t)
	md, err := exifread.ExtractPhoto(filepath.Join("..", "..", "testdata", "exif", "photo-with-gps.jpg"))
	r.NoError(err)
	r.NotNil(md.Latitude)
	r.NotNil(md.Longitude)
	r.InDelta(48.8566, *md.Latitude, 1e-3)
	r.InDelta(2.3522, *md.Longitude, 1e-3)
	r.NotNil(md.GPSAt)
	expected := time.Date(2024, 6, 15, 14, 30, 22, 0, time.UTC)
	r.True(md.GPSAt.Equal(expected), "got %v", md.GPSAt)
}

func TestExtractPhotoDropsNullIslandGPS(t *testing.T) {
	r := require.New(t)
	md, err := exifread.ExtractPhoto(filepath.Join("..", "..", "testdata", "exif", "photo-null-island-gps.jpg"))
	r.NoError(err)
	r.Nil(md.Latitude)
	r.Nil(md.Longitude)
}

func TestExtractPhotoFromReaderMatchesPathVariant(t *testing.T) {
	r := require.New(t)
	path := filepath.Join("..", "..", "testdata", "exif", "photo-with-gps.jpg")
	md1, err := exifread.ExtractPhoto(path)
	r.NoError(err)

	f, err := os.Open(path)
	r.NoError(err)
	defer f.Close()
	md2, err := exifread.ExtractPhotoFromReader(f)
	r.NoError(err)

	r.InDelta(*md1.Latitude, *md2.Latitude, 1e-9)
	r.InDelta(*md1.Longitude, *md2.Longitude, 1e-9)
}
```

(Add `"os"` to the imports.)

- [ ] **Step 7: Run exifread tests.**

```bash
cd /path/to/fotobank
go test ./internal/exifread/... -v
```

Expected: PASS, including the two new fixture tests and the validation matrix.

- [ ] **Step 8: Commit.**

```bash
git add internal/exifread/exifread.go \
        internal/exifread/photo.go \
        internal/exifread/photo_test.go \
        internal/exifread/parse_internal_test.go \
        testdata/exif/photo-with-gps.jpg \
        testdata/exif/photo-null-island-gps.jpg
git commit -m "feat(exifread): parse GPS coords + timestamp; export ExtractPhotoFromReader

Adds Latitude/Longitude/GPSAt to Metadata. parseExif validates per spec
§5.3: rationals with non-zero denominators, strict N/S and E/W refs,
range checks, null-island drop. Two fixture JPEGs cover the integration
path; an internal-package test exercises the full validation matrix
without real files. ExtractPhotoFromReader is a new exported helper used
by the gps backfill CLI to stream EXIF from storage.Store directly;
ExtractPhoto becomes a thin wrapper."
```

---

## Task 6: internal/geo package — NaturalEarth resolver

**Files:**
- Create: `internal/geo/geo.go`
- Create: `internal/geo/geo_test.go`
- Modify: `go.mod`, `go.sum`

This task implements the offline reverse-geocoder. It embeds the three GeoJSON files vendored in Task 1, parses them once at startup, and exposes `Resolve(lat, lon) (label, ok)`. The coord-order foot-gun (§6.4) is locked by a regression test that asserts swapped inputs do NOT resolve to a Paris-shaped label. Field selection is locked per §6.6.

- [ ] **Step 1: Add the orb dependency.**

```bash
cd /path/to/fotobank
go get github.com/paulmach/orb@latest
go mod tidy
```

Verify `go.mod` gained the dependency and `go.sum` updated.

- [ ] **Step 2: Write the failing test scaffold first.**

Create `internal/geo/geo_test.go` with a test that calls `NewNaturalEarth()` and `Resolve` against known coordinates. The test will not compile yet because `geo.go` doesn't exist. This pins the public API:

```go
package geo_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/geo"
)

func TestNaturalEarthResolveKnownCities(t *testing.T) {
	r := require.New(t)
	g, err := geo.NewNaturalEarth()
	r.NoError(err)

	type tc struct {
		name           string
		lat, lon       float64
		mustContain    []string
		mustNotContain []string
	}
	cases := []tc{
		{"Paris", 48.8566, 2.3522, []string{"France"}, nil},
		{"NYC", 40.7128, -74.0060, []string{"New York", "United States"}, nil},
		{"Tokyo", 35.6762, 139.6503, []string{"Japan"}, nil},
		{"Sydney", -33.8688, 151.2093, []string{"Australia"}, nil},
		{"Cape Town", -33.9249, 18.4241, []string{"South Africa"}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := require.New(t)
			label, ok := g.Resolve(c.lat, c.lon)
			r.True(ok, "expected resolve to succeed; got label=%q ok=%v", label, ok)
			for _, sub := range c.mustContain {
				r.True(strings.Contains(label, sub),
					"label %q missing %q", label, sub)
			}
			for _, sub := range c.mustNotContain {
				r.False(strings.Contains(label, sub),
					"label %q unexpectedly contains %q", label, sub)
			}
		})
	}
}

// TestNaturalEarthCoordOrderFootgun guards against the orb.Point{lon, lat}
// vs. orb.Point{lat, lon} silent bug (spec §6.4). Resolving with the
// arguments swapped MUST NOT produce a Paris-shaped label — Paris's
// (lat=48.8566, lon=2.3522) swapped becomes (lat=2.3522, lon=48.8566)
// which lands in the Indian Ocean / Somalia.
func TestNaturalEarthCoordOrderFootgun(t *testing.T) {
	r := require.New(t)
	g, err := geo.NewNaturalEarth()
	r.NoError(err)
	label, _ := g.Resolve(2.3522, 48.8566)
	r.False(strings.Contains(label, "France"),
		"swapped Paris coords resolved to a France-shaped label %q — "+
			"orb.Point construction order is wrong", label)
	r.False(strings.Contains(label, "Paris"), "label=%q", label)
}

func TestNaturalEarthOpenOceanReturnsFalse(t *testing.T) {
	r := require.New(t)
	g, err := geo.NewNaturalEarth()
	r.NoError(err)
	_, ok := g.Resolve(0, -30) // mid-Atlantic
	r.False(ok)
}

func TestNaturalEarthSouthPole(t *testing.T) {
	r := require.New(t)
	g, err := geo.NewNaturalEarth()
	r.NoError(err)
	label, ok := g.Resolve(-89.9, 0)
	r.True(ok)
	r.Contains(label, "Antarctica")
}

func TestNaturalEarthAntimeridian(t *testing.T) {
	r := require.New(t)
	g, err := geo.NewNaturalEarth()
	r.NoError(err)

	// Russian Far East: Petropavlovsk-Kamchatsky-ish.
	label, ok := g.Resolve(53.0, 158.7)
	r.True(ok)
	r.Contains(label, "Russia")

	// Suva, Fiji — straddles antimeridian as a country, but Suva itself
	// is at lon ≈ 178.4 (just west of 180).
	label, ok = g.Resolve(-18.1416, 178.4419)
	r.True(ok)
	r.Contains(label, "Fiji")
}

// TestNaturalEarthSameCountryGate makes sure the city-threshold gate
// drops a populated-place from a different country than the resolved
// admin_0. Pick a coord just over a border: e.g., a point inside Mexico
// near the US border should NOT pull in El Paso; the label should be
// region+country (Mexico), not "El Paso, Texas, United States".
func TestNaturalEarthSameCountryGate(t *testing.T) {
	r := require.New(t)
	g, err := geo.NewNaturalEarth()
	r.NoError(err)

	// Ciudad Juárez sits right across from El Paso, TX. The resolved
	// label must contain "Mexico" and must NOT contain "United States".
	label, ok := g.Resolve(31.6904, -106.4245)
	r.True(ok)
	r.Contains(label, "Mexico")
	r.NotContains(label, "United States")
}

// TestEmbeddedDataChecksums verifies the three embedded GeoJSON files
// match the SHA256s in PROVENANCE.md. Catches accidental re-vendoring
// of a different release — see Task 1.
func TestEmbeddedDataChecksums(t *testing.T) {
	r := require.New(t)
	expected := map[string]string{
		"ne_10m_admin_0_countries.geojson":        os.Getenv("FOTOBANK_GEO_SHA_ADMIN0"),
		"ne_10m_admin_1_states_provinces.geojson": os.Getenv("FOTOBANK_GEO_SHA_ADMIN1"),
		"ne_10m_populated_places.geojson":         os.Getenv("FOTOBANK_GEO_SHA_PLACES"),
	}
	// If the env vars aren't set, fall back to reading PROVENANCE.md and
	// scraping the table. For simplicity we hard-code the actual SHA256s
	// the implementer captured in Task 1 and edit them here directly.
	for name, want := range expected {
		if want == "" {
			t.Skipf("expected SHA env var unset for %s; populate from PROVENANCE.md", name)
		}
		path := filepath.Join("data", name)
		data, err := os.ReadFile(path)
		r.NoError(err)
		got := hex.EncodeToString(sha256.Sum256(data)[:])
		r.Equal(want, got, "embedded %s SHA256 drift", name)
	}
}

// BenchmarkNewNaturalEarth captures the boot-time cost of parsing
// the embedded GeoJSON. Spec §6.8: if this exceeds ~1s on the
// developer machine, escalate.
func BenchmarkNewNaturalEarth(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, err := geo.NewNaturalEarth()
		if err != nil {
			b.Fatal(err)
		}
	}
}
```

The implementer's actual SHA256s from Task 1 should be substituted into the `TestEmbeddedDataChecksums` body (the env-var indirection above is a placeholder; replace with literal hex strings before committing).

- [ ] **Step 3: Run the test scaffold and confirm it fails to compile.**

```bash
cd /path/to/fotobank
go test ./internal/geo/... 2>&1 | head -20
```

Expected: build error `package geo` not found, or `undefined: geo.NewNaturalEarth`.

- [ ] **Step 4: Implement `internal/geo/geo.go`.**

```go
// Package geo provides an offline reverse geocoder backed by Natural
// Earth 1:10m. It produces coarse country/region/city labels suitable
// for an info panel; it does NOT produce neighborhood/street-level
// labels — that's an explicit out-of-scope deferral, see F2.1 spec.
package geo

import (
	"embed"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/paulmach/orb"
	"github.com/paulmach/orb/geojson"
	"github.com/paulmach/orb/planar"
)

//go:embed data/ne_10m_admin_0_countries.geojson
//go:embed data/ne_10m_admin_1_states_provinces.geojson
//go:embed data/ne_10m_populated_places.geojson
var dataFS embed.FS

// City-threshold defaults from spec §6.5.
const (
	cityMaxDistanceKm = 25.0
	earthRadiusKm     = 6371.0
)

// NaturalEarth is a parsed in-memory copy of the embedded gazetteer.
// Construct via NewNaturalEarth; safe for concurrent Resolve calls.
type NaturalEarth struct {
	countries []countryFeature
	regions   []regionFeature
	cities    []cityFeature
}

type countryFeature struct {
	name string
	bbox orb.Bound
	geom orb.Geometry
}

type regionFeature struct {
	name string
	bbox orb.Bound
	geom orb.Geometry
}

type cityFeature struct {
	name      string
	country   string
	admin1    string
	point     orb.Point // [lon, lat]
}

// NewNaturalEarth parses the embedded gazetteer once. Returns an error
// if the embedded data is missing or malformed (a programming/build
// error). After this returns, Resolve does no I/O.
func NewNaturalEarth() (*NaturalEarth, error) {
	g := &NaturalEarth{}

	if err := loadCountries(g); err != nil {
		return nil, fmt.Errorf("load countries: %w", err)
	}
	if err := loadRegions(g); err != nil {
		return nil, fmt.Errorf("load regions: %w", err)
	}
	if err := loadCities(g); err != nil {
		return nil, fmt.Errorf("load cities: %w", err)
	}
	return g, nil
}

func loadCountries(g *NaturalEarth) error {
	data, err := dataFS.ReadFile("data/ne_10m_admin_0_countries.geojson")
	if err != nil {
		return err
	}
	fc, err := geojson.UnmarshalFeatureCollection(data)
	if err != nil {
		return err
	}
	// Stable sort by name so "first match wins" on overlap is deterministic.
	sort.SliceStable(fc.Features, func(i, j int) bool {
		return featureName(fc.Features[i], "NAME", "ADMIN") <
			featureName(fc.Features[j], "NAME", "ADMIN")
	})
	for _, f := range fc.Features {
		name := featureName(f, "NAME", "ADMIN")
		if name == "" {
			continue
		}
		g.countries = append(g.countries, countryFeature{
			name: name,
			bbox: f.Geometry.Bound(),
			geom: f.Geometry,
		})
	}
	return nil
}

func loadRegions(g *NaturalEarth) error {
	data, err := dataFS.ReadFile("data/ne_10m_admin_1_states_provinces.geojson")
	if err != nil {
		return err
	}
	fc, err := geojson.UnmarshalFeatureCollection(data)
	if err != nil {
		return err
	}
	sort.SliceStable(fc.Features, func(i, j int) bool {
		return featureName(fc.Features[i], "name", "NAME") <
			featureName(fc.Features[j], "name", "NAME")
	})
	for _, f := range fc.Features {
		name := featureName(f, "name", "NAME")
		if name == "" {
			continue
		}
		g.regions = append(g.regions, regionFeature{
			name: name,
			bbox: f.Geometry.Bound(),
			geom: f.Geometry,
		})
	}
	return nil
}

func loadCities(g *NaturalEarth) error {
	data, err := dataFS.ReadFile("data/ne_10m_populated_places.geojson")
	if err != nil {
		return err
	}
	fc, err := geojson.UnmarshalFeatureCollection(data)
	if err != nil {
		return err
	}
	for _, f := range fc.Features {
		pt, ok := f.Geometry.(orb.Point)
		if !ok {
			continue
		}
		name := featureName(f, "NAMEASCII", "NAME")
		if name == "" {
			continue
		}
		country, _ := f.Properties["ADM0NAME"].(string)
		admin1, _ := f.Properties["ADM1NAME"].(string)
		g.cities = append(g.cities, cityFeature{
			name: name, country: country, admin1: admin1, point: pt,
		})
	}
	return nil
}

func featureName(f *geojson.Feature, primary, fallback string) string {
	if v, ok := f.Properties[primary].(string); ok && v != "" {
		return v
	}
	if v, ok := f.Properties[fallback].(string); ok {
		return v
	}
	return ""
}

// Resolve returns a coarse human-readable label for the input
// coordinate. Returns ("", false) when no admin_0 polygon contains
// the point (open ocean) or when the input is out of range.
//
// IMPORTANT: public API is (lat, lon); GeoJSON / orb.Point use
// [lon, lat]. Every internal orb.Point construction below reorders.
func (n *NaturalEarth) Resolve(lat, lon float64) (string, bool) {
	if lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		return "", false
	}
	pt := orb.Point{lon, lat} // §6.4 — NOT {lat, lon}

	country := pointInPolygonName(pt, n.countriesAsBoundedFeatures())
	if country == "" {
		return "", false
	}

	region := pointInPolygonName(pt, n.regionsAsBoundedFeatures())

	city := nearestCity(pt, n.cities, country, region)

	parts := make([]string, 0, 3)
	if city != "" {
		parts = append(parts, city)
	}
	if region != "" {
		parts = append(parts, region)
	}
	parts = append(parts, country)
	return strings.Join(parts, ", "), true
}

// boundedFeature is the minimal interface point-in-polygon needs.
type boundedFeature struct {
	name string
	bbox orb.Bound
	geom orb.Geometry
}

func (n *NaturalEarth) countriesAsBoundedFeatures() []boundedFeature {
	out := make([]boundedFeature, len(n.countries))
	for i, c := range n.countries {
		out[i] = boundedFeature{name: c.name, bbox: c.bbox, geom: c.geom}
	}
	return out
}

func (n *NaturalEarth) regionsAsBoundedFeatures() []boundedFeature {
	out := make([]boundedFeature, len(n.regions))
	for i, r := range n.regions {
		out[i] = boundedFeature{name: r.name, bbox: r.bbox, geom: r.geom}
	}
	return out
}

func pointInPolygonName(pt orb.Point, fs []boundedFeature) string {
	for _, f := range fs {
		if !f.bbox.Contains(pt) {
			continue
		}
		switch g := f.geom.(type) {
		case orb.Polygon:
			if planar.PolygonContains(g, pt) {
				return f.name
			}
		case orb.MultiPolygon:
			if planar.MultiPolygonContains(g, pt) {
				return f.name
			}
		}
	}
	return ""
}

// nearestCity scans cities linearly. Honors the §6.5 gates: distance
// ≤ cityMaxDistanceKm, same country, same admin_1 (when admin_1
// resolved). Returns "" if no city qualifies.
func nearestCity(pt orb.Point, cities []cityFeature, country, region string) string {
	bestName := ""
	bestKm := cityMaxDistanceKm + 1
	for _, c := range cities {
		if country != "" && c.country != "" && c.country != country {
			continue
		}
		if region != "" && c.admin1 != "" && c.admin1 != region {
			continue
		}
		km := haversineKm(pt, c.point)
		if km > cityMaxDistanceKm {
			continue
		}
		if km < bestKm {
			bestKm = km
			bestName = c.name
		}
	}
	return bestName
}

func haversineKm(a, b orb.Point) float64 {
	lat1, lon1 := deg2rad(a[1]), deg2rad(a[0])
	lat2, lon2 := deg2rad(b[1]), deg2rad(b[0])
	dLat := lat2 - lat1
	dLon := lon2 - lon1
	h := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1)*math.Cos(lat2)*math.Sin(dLon/2)*math.Sin(dLon/2)
	c := 2 * math.Atan2(math.Sqrt(h), math.Sqrt(1-h))
	return earthRadiusKm * c
}

func deg2rad(d float64) float64 { return d * math.Pi / 180 }

// Compile-time guard: NaturalEarth must satisfy the unexported
// PlaceResolver interface from internal/ingest. We re-declare the
// interface here to avoid an import cycle (geo must not depend on
// ingest). If ingest's interface changes, this guard breaks.
var _ interface {
	Resolve(lat, lon float64) (string, bool)
} = (*NaturalEarth)(nil)

// Silence unused import warnings if json is removed during refactor.
var _ = json.Marshal
```

(Note: `encoding/json` import is unused above but `geojson.UnmarshalFeatureCollection` may transitively need it via orb; remove `encoding/json` and the silencer line if `go build` succeeds without them.)

- [ ] **Step 5: Implement `TestEmbeddedDataChecksums` with the actual SHA256s.**

Replace the env-var-indirection placeholder in `geo_test.go` with the actual SHA256 hex strings captured in Task 1 Step 3:

```go
func TestEmbeddedDataChecksums(t *testing.T) {
	r := require.New(t)
	expected := map[string]string{
		"ne_10m_admin_0_countries.geojson":        "<paste-actual-sha256>",
		"ne_10m_admin_1_states_provinces.geojson": "<paste-actual-sha256>",
		"ne_10m_populated_places.geojson":         "<paste-actual-sha256>",
	}
	for name, want := range expected {
		path := filepath.Join("data", name)
		data, err := os.ReadFile(path)
		r.NoError(err)
		got := hex.EncodeToString(sha256.Sum256(data)[:])
		r.Equal(want, got, "embedded %s SHA256 drift", name)
	}
}
```

- [ ] **Step 6: Run all geo tests.**

```bash
cd /path/to/fotobank
go test ./internal/geo/... -v
```

Expected: all PASS, including `TestNaturalEarthCoordOrderFootgun` (proves the orb.Point construction is `{lon, lat}` not `{lat, lon}`).

- [ ] **Step 7: Run the init benchmark and capture the wall time.**

```bash
go test -bench=BenchmarkNewNaturalEarth -benchtime=3x ./internal/geo/...
```

Expected: each iteration completes in well under 1s. If init exceeds ~1s, escalate per spec §6.8 (consider 1:50m fallback). Record the actual ns/op number for the implementation summary.

- [ ] **Step 8: Confirm build is clean.**

```bash
go build ./...
go vet ./...
```

Expected: no errors / no warnings.

- [ ] **Step 9: Commit.**

```bash
git add internal/geo/ go.mod go.sum
git commit -m "feat(geo): offline reverse-geocoder backed by Natural Earth 1:10m

Embeds the three vendored GeoJSON files at compile time; parses once
in NewNaturalEarth() and exposes Resolve(lat, lon) with deterministic
country / region / city lookup via paulmach/orb. Field selection is
locked per spec §6.6 (NAME→ADMIN for country, name→NAME for region,
NAMEASCII→NAME for city; ADM0NAME/ADM1NAME for the same-jurisdiction
gate). City threshold defaults to 25 km. Tests cover known cities,
ocean / South Pole / antimeridian, the orb.Point{lon,lat} coord-order
foot-gun (§6.4), the same-country city gate, embedded-data SHA256
verification, and a BenchmarkNewNaturalEarth init-time guard."
```

---

## Task 7: ingest — PlaceResolver interface + NewImporter signature

**Files:**
- Modify: `internal/ingest/importer.go`
- Modify: `internal/ingest/importer_test.go`

This task threads the resolver through the import pipeline. The `PlaceResolver` interface is consumer-side (lives in `ingest`, not `geo`, per §3). `NewImporter` gains a third parameter; `nil` is allowed and skips the label step. `buildMediaRow` populates the four GPS fields when EXIF has them.

- [ ] **Step 1: Define `PlaceResolver` interface.**

Open `internal/ingest/importer.go`. Near the top of the file (after the imports), add:

```go
// PlaceResolver returns a coarse human-readable label for a coordinate.
// Production callers pass a *geo.NaturalEarth; tests pass a stub or
// nil. When nil, the importer still extracts and stores
// latitude/longitude/gps_at from EXIF and leaves LocationLabel empty.
type PlaceResolver interface {
	Resolve(lat, lon float64) (label string, ok bool)
}
```

- [ ] **Step 2: Update `Importer` struct and constructor.**

```go
// Importer wires discovery to extraction, storage, and the media repo.
type Importer struct {
	store  storage.Store
	repo   *media.Repo
	places PlaceResolver
	now    func() time.Time
}

// NewImporter constructs an Importer with the default UTC wall clock.
// places may be nil — when nil, ingest still extracts and stores
// latitude/longitude/gps_at from EXIF and leaves LocationLabel empty.
// Production callers (the `fotobank import` and `fotobank gps backfill`
// CLIs) MUST pass a real *geo.NaturalEarth.
func NewImporter(store storage.Store, repo *media.Repo, places PlaceResolver) *Importer {
	return &Importer{
		store:  store,
		repo:   repo,
		places: places,
		now:    func() time.Time { return time.Now().UTC() },
	}
}
```

- [ ] **Step 3: Thread `places` into `buildMediaRow`.**

`buildMediaRow` is package-level; threading via the receiver is cleaner — but the existing function takes `meta exifread.Metadata` and other primitive args, not the importer. Change the call sites in `processPhoto` and `processVideo` to pass `imp.places`, and update `buildMediaRow` to accept a final `places PlaceResolver` parameter. Replace the existing `buildMediaRow`:

```go
// buildMediaRow assembles the media row. Nullable metadata fields are
// only populated when we actually have a value.
func buildMediaRow(c Candidate, owner owners.Principal, key, checksum string, size int64, meta exifread.Metadata, importedAt time.Time, places PlaceResolver) media.Media {
	m := media.Media{
		ID:               uuid.NewString(),
		Owner:            owner,
		Type:             c.Type,
		MimeType:         c.MimeType,
		Path:             key,
		OriginalFilename: c.Path,
		ImportedAt:       importedAt,
		Timestamp:        meta.Timestamp,
		Size:             size,
		Checksum:         checksum,
		Make:             meta.Make,
		Model:            meta.Model,
		FocalLength:      meta.FocalLength,
		Shutter:          meta.ShutterSpeed,
		ThumbStatus:      "pending",
	}
	if meta.Width > 0 {
		w := meta.Width
		m.Width = &w
	}
	if meta.Height > 0 {
		h := meta.Height
		m.Height = &h
	}
	if meta.ISO > 0 {
		iso := meta.ISO
		m.ISO = &iso
	}
	if meta.Aperture > 0 {
		a := meta.Aperture
		m.Aperture = &a
	}
	if meta.DurationMs > 0 {
		d := meta.DurationMs
		m.DurationMs = &d
	}
	if meta.Latitude != nil && meta.Longitude != nil {
		m.Latitude = meta.Latitude
		m.Longitude = meta.Longitude
		m.GPSAt = meta.GPSAt
		if places != nil {
			if label, ok := places.Resolve(*meta.Latitude, *meta.Longitude); ok {
				m.LocationLabel = label
			}
		}
	}
	return m
}
```

Update `processPhoto` and `processVideo` to pass `imp.places`. The existing `m := buildMediaRow(c, owner, landed, checksum, size, meta, imp.now())` becomes `m := buildMediaRow(c, owner, landed, checksum, size, meta, imp.now(), imp.places)` in both places.

- [ ] **Step 4: Update existing tests to pass `nil` for the third arg.**

In `internal/ingest/importer_test.go`, every `ingest.NewImporter(f.store, f.repo)` call (there are 8 per the earlier grep) becomes `ingest.NewImporter(f.store, f.repo, nil)`.

- [ ] **Step 5: Add a new test exercising the resolver path.**

Append to `internal/ingest/importer_test.go`:

```go
type stubResolver struct{ label string }

func (s stubResolver) Resolve(lat, lon float64) (string, bool) {
	return s.label, s.label != ""
}

func TestImporterPopulatesGPSWhenResolverReturnsLabel(t *testing.T) {
	r := require.New(t)
	f := newImporterFixture(t)
	imp := ingest.NewImporter(f.store, f.repo, stubResolver{label: "Test City, Test Region, Test Country"})

	// Use the same fixture flow as existing tests; reuses the
	// photo-with-gps.jpg fixture from Task 5.
	src := f.copyFixtureToImportRoot(t, filepath.Join("..", "..", "testdata", "exif", "photo-with-gps.jpg"))

	res, err := imp.ImportDirectory(context.Background(),
		filepath.Dir(src),
		ingest.Options{Owner: f.owner, ConcurrentWorkers: 1})
	r.NoError(err)
	r.Equal(1, res.Imported)
	r.Empty(res.Failures)

	all, err := f.repo.ListAll(context.Background(), f.owner)
	r.NoError(err)
	r.Len(all, 1)
	row := all[0]
	r.NotNil(row.Latitude)
	r.NotNil(row.Longitude)
	r.InDelta(48.8566, *row.Latitude, 1e-3)
	r.InDelta(2.3522, *row.Longitude, 1e-3)
	r.Equal("Test City, Test Region, Test Country", row.LocationLabel)
}

func TestImporterLeavesLocationLabelEmptyWhenResolverNil(t *testing.T) {
	r := require.New(t)
	f := newImporterFixture(t)
	imp := ingest.NewImporter(f.store, f.repo, nil)

	src := f.copyFixtureToImportRoot(t, filepath.Join("..", "..", "testdata", "exif", "photo-with-gps.jpg"))
	_, err := imp.ImportDirectory(context.Background(), filepath.Dir(src),
		ingest.Options{Owner: f.owner, ConcurrentWorkers: 1})
	r.NoError(err)

	all, err := f.repo.ListAll(context.Background(), f.owner)
	r.NoError(err)
	r.Len(all, 1)
	r.NotNil(all[0].Latitude)
	r.Empty(all[0].LocationLabel)
}
```

If the existing fixture struct doesn't expose `copyFixtureToImportRoot`, add it (or inline the copy with `os.WriteFile` / `io.Copy`). The pattern: copy the fixture into a `t.TempDir()` so the importer's directory walk picks it up.

- [ ] **Step 6: Run ingest tests.**

```bash
cd /path/to/fotobank
go test ./internal/ingest/... -v -shuffle=on
```

Expected: PASS, including the two new GPS tests.

- [ ] **Step 7: Build the whole module to verify no other callers were missed.**

```bash
go build ./...
```

Expected: no errors. (Production callers in `internal/cli/` are updated in Tasks 9 and 10.)

- [ ] **Step 8: Commit.**

```bash
git add internal/ingest/importer.go internal/ingest/importer_test.go
git commit -m "feat(ingest): inject PlaceResolver into Importer

Adds a consumer-side PlaceResolver interface and threads a third arg
through NewImporter. nil is allowed: ingest still stores raw GPS from
EXIF and leaves LocationLabel empty. Production CLIs will pass a real
*geo.NaturalEarth in subsequent tasks."
```

---

## Task 8: service.MediaService.UpdateGPS

**Files:**
- Modify: `internal/service/media_service.go`
- Modify: `internal/service/media_service_test.go`

The service layer is the auth boundary; `UpdateGPS` does an owner check via the existing `Get` (which returns `errs.ErrNotFound` on caller-mismatch — the anti-probing convention) and then delegates to `repo.UpdateGPS`.

- [ ] **Step 1: Add `UpdateGPS` to MediaService.**

Open `internal/service/media_service.go`. Append:

```go
// UpdateGPS persists the four GPS columns on a row owned by caller.
// Uses Get(ctx, id, caller) for the owner check (which returns
// errs.ErrNotFound on caller mismatch — preserving the anti-probing
// convention from line 31). The CLI orchestrates "open NAS bytes, run
// exifread, resolve label" itself; the service layer stays simple and
// auth-scoped.
func (s *MediaService) UpdateGPS(
	ctx context.Context,
	caller owners.Principal,
	id string,
	lat, lon *float64,
	gpsAt *time.Time,
	label string,
) error {
	if _, err := s.Get(ctx, id, caller); err != nil {
		return err
	}
	return s.repo.UpdateGPS(ctx, id, lat, lon, gpsAt, label)
}
```

(Add `time` to the imports if not already present.)

- [ ] **Step 2: Add tests for `UpdateGPS`.**

Append to `internal/service/media_service_test.go` (or create it if it doesn't exist):

```go
func TestMediaServiceUpdateGPSRoundTrips(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	store := storage.NewNASOnly(t.TempDir(), nil)
	svc := service.NewMediaService(repo, store)
	owner := owners.Principal{Hub: "h", UserID: "u"}
	_, err := d.WriteDB().ExecContext(context.Background(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		owner.Hub, owner.UserID, "sk", time.Now().UTC(),
	)
	r.NoError(err)

	id := uuid.NewString()
	r.NoError(repo.Insert(context.Background(), media.Media{
		ID: id, Owner: owner, Type: media.TypePhoto, MimeType: "image/jpeg",
		Path: "x.jpg", ImportedAt: time.Now().UTC(),
		Size: 1, Checksum: "c-" + id, ThumbStatus: "pending",
	}))

	lat, lon := 1.0, 2.0
	r.NoError(svc.UpdateGPS(context.Background(), owner, id, &lat, &lon, nil, "Foo, Bar"))

	got, err := repo.GetByID(context.Background(), id)
	r.NoError(err)
	r.NotNil(got.Latitude)
	r.Equal("Foo, Bar", got.LocationLabel)
}

func TestMediaServiceUpdateGPSCallerMismatchReturnsNotFound(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	store := storage.NewNASOnly(t.TempDir(), nil)
	svc := service.NewMediaService(repo, store)
	ownerA := owners.Principal{Hub: "h", UserID: "u-a"}
	ownerB := owners.Principal{Hub: "h", UserID: "u-b"}
	for _, p := range []owners.Principal{ownerA, ownerB} {
		_, err := d.WriteDB().ExecContext(context.Background(),
			`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
			p.Hub, p.UserID, "sk-"+p.UserID, time.Now().UTC(),
		)
		r.NoError(err)
	}

	id := uuid.NewString()
	r.NoError(repo.Insert(context.Background(), media.Media{
		ID: id, Owner: ownerA, Type: media.TypePhoto, MimeType: "image/jpeg",
		Path: "x.jpg", ImportedAt: time.Now().UTC(),
		Size: 1, Checksum: "c-" + id, ThumbStatus: "pending",
	}))

	lat, lon := 1.0, 2.0
	err := svc.UpdateGPS(context.Background(), ownerB, id, &lat, &lon, nil, "Foo")
	r.ErrorIs(err, errs.ErrNotFound)
}

func TestMediaServiceUpdateGPSMissingRowReturnsNotFound(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	store := storage.NewNASOnly(t.TempDir(), nil)
	svc := service.NewMediaService(repo, store)

	owner := owners.Principal{Hub: "h", UserID: "u"}
	err := svc.UpdateGPS(context.Background(), owner, "no-such-id", nil, nil, nil, "")
	r.ErrorIs(err, errs.ErrNotFound)
}
```

(Add the relevant imports: `context`, `testing`, `time`, `github.com/google/uuid`, `github.com/stretchr/testify/require`, `github.com/wesm/fotobank/internal/errs`, `github.com/wesm/fotobank/internal/media`, `github.com/wesm/fotobank/internal/owners`, `github.com/wesm/fotobank/internal/service`, `github.com/wesm/fotobank/internal/storage`, `github.com/wesm/fotobank/internal/testutil`. If the test file already exists, only add the missing ones.)

- [ ] **Step 3: Run service tests.**

```bash
cd /path/to/fotobank
go test ./internal/service/... -v -shuffle=on
```

Expected: PASS, including the three new UpdateGPS tests.

- [ ] **Step 4: Commit.**

```bash
git add internal/service/media_service.go internal/service/media_service_test.go
git commit -m "feat(service): add MediaService.UpdateGPS

Auth-scoped wrapper around repo.UpdateGPS that uses Get() for the
owner check (returning errs.ErrNotFound on caller mismatch — preserves
the anti-probing convention). The CLI owns I/O orchestration; the
service stays simple."
```

---

## Task 9: internal/cli/import.go — wire geo.NaturalEarth into fotobank import

**Files:**
- Modify: `internal/cli/import.go`

Production import paths must pass a real `*geo.NaturalEarth` so newly imported photos get their `location_label` set. Failure to construct the resolver aborts the command (fail-fast) — the operator should see a clear error rather than silently importing without labels.

- [ ] **Step 1: Read the current import.go to find the NewImporter call site.**

```bash
cd /path/to/fotobank
grep -n "NewImporter" internal/cli/import.go
```

Expected: a single line near 127 (`imp := ingest.NewImporter(storeLayer, repo)`).

- [ ] **Step 2: Construct `*geo.NaturalEarth` and thread into NewImporter.**

Open `internal/cli/import.go`. Near the existing setup (above the `imp := ingest.NewImporter(...)` call), add:

```go
	places, err := geo.NewNaturalEarth()
	if err != nil {
		return fmt.Errorf("load geo gazetteer: %w", err)
	}
	imp := ingest.NewImporter(storeLayer, repo, places)
```

Add `"github.com/wesm/fotobank/internal/geo"` to the import block.

- [ ] **Step 3: Build to confirm.**

```bash
go build ./cmd/fotobank
```

Expected: no errors.

- [ ] **Step 4: Smoke-test the import command end-to-end with a GPS-bearing fixture.**

This step is exploratory; the existing import tests already exercise `NewImporter`. Skip unless `internal/cli/import_test.go` already has an integration test; if it does, extend it to assert `LocationLabel` is set on a row imported from `testdata/exif/photo-with-gps.jpg`. Otherwise rely on Task 7's importer tests for coverage of the resolver-injection path.

- [ ] **Step 5: Commit.**

```bash
git add internal/cli/import.go
git commit -m "feat(cli): wire geo.NaturalEarth into fotobank import

The import command now constructs a *geo.NaturalEarth at entry and
passes it into ingest.NewImporter so newly imported photos get their
location_label populated. Boot fails fast if the embedded gazetteer
can't load."
```

---

## Task 10: internal/cli/gps.go — backfill command

**Files:**
- Create: `internal/cli/gps.go`
- Create: `internal/cli/gps_test.go`
- Modify: the cli root command registration (likely `internal/cli/cli.go` or similar)

This task implements `fotobank gps backfill` end-to-end: scope/stub-mode handling matching `thumbs regenerate`, `--since=DURATION` parsed up-front before opening the DB, three modes (`full` / `fill-missing` / `relabel`), and progress + final summary logging. Videos are skipped in all modes.

- [ ] **Step 1: Find the cobra root registration site for existing commands.**

```bash
cd /path/to/fotobank
grep -n "newThumbsCmd\|AddCommand" internal/cli/*.go | head -10
```

Locate the `*cobra.Command` builder that adds top-level subcommands (likely in `internal/cli/cli.go`). The new `gps` group will be registered there in Step 7.

- [ ] **Step 2: Write the failing flag-validation tests first.**

Create `internal/cli/gps_test.go`:

```go
package cli_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/cli"
	"github.com/wesm/fotobank/internal/media"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/testutil"
)

// runGPS invokes the gps subcommand and returns (exitCode, stdout, stderr).
// Mirrors the existing thumbs_test pattern.
func runGPS(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	cmd := cli.NewRootCmd()
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetArgs(append([]string{"gps"}, args...))
	err := cmd.ExecuteContext(context.Background())
	code := cli.ExitCodeFor(err)
	return code, stdout.String(), stderr.String()
}

func TestGPSBackfillBadSinceErrorsBeforeOpeningDB(t *testing.T) {
	r := require.New(t)
	cfgDir := t.TempDir()
	cfgPath := writeStubConfig(t, cfgDir)
	dbPath := filepath.Join(cfgDir, "fotobank.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)

	code, _, stderr := runGPS(t, "backfill", "--config", cfgPath, "--since", "garbage")
	r.Equal(2, code, "usage error → exit 2; got %s", stderr)
	r.NoFileExists(dbPath, "DB must not be created on bad --since")
}

func TestGPSBackfillNegativeSinceErrors(t *testing.T) {
	r := require.New(t)
	cfgDir := t.TempDir()
	cfgPath := writeStubConfig(t, cfgDir)

	code, _, stderr := runGPS(t, "backfill", "--config", cfgPath, "--since", "-1h")
	r.Equal(2, code, "got %s", stderr)
}

func TestGPSBackfillStubModeRequiredByDefault(t *testing.T) {
	r := require.New(t)
	cfgDir := t.TempDir()
	cfgPath := writeNonStubConfig(t, cfgDir)

	code, _, stderr := runGPS(t, "backfill", "--config", cfgPath)
	r.Equal(2, code, "got %s", stderr)
	r.Contains(stderr, "stub")
}

func TestGPSBackfillOwnerScopeBypassesStubModeRequirement(t *testing.T) {
	r := require.New(t)
	cfgDir := t.TempDir()
	cfgPath := writeNonStubConfig(t, cfgDir)
	dbPath := filepath.Join(cfgDir, "fotobank.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)

	code, _, stderr := runGPS(t, "backfill",
		"--config", cfgPath,
		"--owner", "h:u",
		"--mode", "relabel",
	)
	// Owner doesn't exist; this should exit 1 (not-found-style) rather
	// than 2 (usage/stub-required). The point is that the stub-mode
	// gate did NOT fire.
	r.NotEqual(2, code, "stderr=%q", stderr)
}

func TestGPSBackfillAllOwnersBypassesStubModeRequirement(t *testing.T) {
	r := require.New(t)
	cfgDir := t.TempDir()
	cfgPath := writeNonStubConfig(t, cfgDir)
	dbPath := filepath.Join(cfgDir, "fotobank.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)

	code, _, _ := runGPS(t, "backfill", "--config", cfgPath, "--all-owners", "--mode", "relabel")
	r.NotEqual(2, code)
}

func TestGPSBackfillOwnerAndAllOwnersMutuallyExclusive(t *testing.T) {
	r := require.New(t)
	cfgDir := t.TempDir()
	cfgPath := writeStubConfig(t, cfgDir)

	code, _, stderr := runGPS(t, "backfill",
		"--config", cfgPath,
		"--owner", "h:u",
		"--all-owners",
	)
	r.Equal(2, code, "got %s", stderr)
}

// writeStubConfig and writeNonStubConfig are helpers that mirror the
// existing thumbs_test.go pattern; if those helpers already exist
// (e.g. in a shared test util file), reuse them. Otherwise add:
func writeStubConfig(t *testing.T, dir string) string {
	t.Helper()
	cfg := []byte(`
identity:
  mode: stub
  stub:
    hub: "h"
    user_id: "u"
    display_handle: "Test"
flash:
  root: "` + dir + `"
nas:
  root: "` + dir + `/nas"
`)
	path := filepath.Join(dir, "config.yml")
	require.NoError(t, os.WriteFile(path, cfg, 0o600))
	return path
}

func writeNonStubConfig(t *testing.T, dir string) string {
	t.Helper()
	cfg := []byte(`
identity:
  mode: oauth
flash:
  root: "` + dir + `"
nas:
  root: "` + dir + `/nas"
`)
	path := filepath.Join(dir, "config.yml")
	require.NoError(t, os.WriteFile(path, cfg, 0o600))
	return path
}
```

These tests will not compile — `cli.NewRootCmd`, `cli.ExitCodeFor`, the gps subcommand wiring don't exist yet. That's the failing-test state.

- [ ] **Step 3: Run the test scaffold to confirm it fails to compile.**

```bash
go test ./internal/cli/... -run TestGPSBackfill 2>&1 | head -30
```

Expected: build errors mentioning `gps` undefined / `cli.NewRootCmd` etc. (If `NewRootCmd` and `ExitCodeFor` already exist for `thumbs_test`, they're fine to reuse — the test will only fail on the missing `gps` subcommand.)

- [ ] **Step 4: Implement `internal/cli/gps.go`.**

```go
package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/wesm/fotobank/internal/config"
	"github.com/wesm/fotobank/internal/db"
	"github.com/wesm/fotobank/internal/errs"
	"github.com/wesm/fotobank/internal/exifread"
	"github.com/wesm/fotobank/internal/geo"
	"github.com/wesm/fotobank/internal/media"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/service"
	"github.com/wesm/fotobank/internal/storage"
)

func newGPSCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "gps",
		Short: "Manage GPS metadata",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			_ = cmd.Usage()
			return newUsageError("a subcommand is required")
		},
	}
	cmd.AddCommand(newGPSBackfillCmd())
	return cmd
}

type gpsBackfillOpts struct {
	cfgPath   string
	since     string         // raw flag string
	sinceTime *time.Time     // parsed in validate; nil when --since unset
	mode      string         // "full", "fill-missing", "relabel"
	parsedMode media.GPSBackfillMode
	owner     string
	allOwners bool
}

func newGPSBackfillCmd() *cobra.Command {
	var opts gpsBackfillOpts
	cmd := &cobra.Command{
		Use:   "backfill",
		Short: "Extract or refresh GPS metadata for existing media rows",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runGPSBackfill(cmd.Context(), &opts, cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	cmd.Flags().StringVar(&opts.cfgPath, "config", "",
		"path to config file (defaults to DefaultConfigPath)")
	cmd.Flags().StringVar(&opts.since, "since", "",
		"only rows imported within this duration (e.g. 24h, 168h, 30m)")
	cmd.Flags().StringVar(&opts.mode, "mode", "full",
		"full | fill-missing | relabel (default full)")
	cmd.Flags().StringVar(&opts.owner, "owner", "",
		"admin: backfill for a single principal in <hub>:<user> form")
	cmd.Flags().BoolVar(&opts.allOwners, "all-owners", false,
		"admin: backfill for every registered principal")
	return cmd
}

func validateGPSBackfillOpts(opts *gpsBackfillOpts) error {
	if opts.owner != "" && opts.allOwners {
		return newUsageError("--owner and --all-owners are mutually exclusive")
	}
	if opts.since != "" {
		d, err := time.ParseDuration(opts.since)
		if err != nil {
			return newUsageError("invalid --since %q: %v", opts.since, err)
		}
		if d <= 0 {
			return newUsageError("--since must be positive (got %s)", opts.since)
		}
		t := time.Now().UTC().Add(-d)
		opts.sinceTime = &t
	}
	switch opts.mode {
	case "full":
		opts.parsedMode = media.GPSBackfillModeFull
	case "fill-missing":
		opts.parsedMode = media.GPSBackfillModeFillMissing
	case "relabel":
		opts.parsedMode = media.GPSBackfillModeRelabel
	default:
		return newUsageError("--mode must be one of full|fill-missing|relabel (got %q)", opts.mode)
	}
	return nil
}

func runGPSBackfill(ctx context.Context, opts *gpsBackfillOpts, stdout, stderr io.Writer) error {
	if err := validateGPSBackfillOpts(opts); err != nil {
		return err
	}
	requireStub := opts.owner == "" && !opts.allOwners
	cfg, err := loadGPSConfig(opts.cfgPath, requireStub)
	if err != nil {
		return err
	}

	places, err := geo.NewNaturalEarth()
	if err != nil {
		return fmt.Errorf("load gazetteer: %w", err)
	}

	d, err := openDB(cfg)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()

	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	storeLayer, err := buildStorageFromConfig(cfg)
	if err != nil {
		return err
	}
	svc := service.NewMediaService(repo, storeLayer)

	principals, err := selectPrincipals(ctx, d, cfg, opts)
	if err != nil {
		return err
	}

	tally := backfillTally{}
	for _, p := range principals {
		if err := backfillForPrincipal(ctx, p, opts, repo, svc, storeLayer, places, stderr, &tally); err != nil {
			return err
		}
	}
	fmt.Fprintln(stdout, tally.summary())
	return nil
}

// loadGPSConfig mirrors loadThumbsConfig: requireStub enforces
// identity.mode=stub when no admin scope flag is set.
func loadGPSConfig(cfgPath string, requireStub bool) (*config.Config, error) {
	path := cfgPath
	if path == "" {
		path = config.DefaultConfigPath()
	}
	cfg, err := config.Load(path)
	if err != nil {
		return nil, err
	}
	if requireStub && cfg.Identity.Mode != "stub" {
		return nil, newUsageError(
			"fotobank gps backfill requires identity.mode = stub (got %q)",
			cfg.Identity.Mode)
	}
	return cfg, nil
}

func selectPrincipals(ctx context.Context, d *db.DB, cfg *config.Config, opts *gpsBackfillOpts) ([]owners.Principal, error) {
	switch {
	case opts.owner != "":
		hub, user, ok := strings.Cut(opts.owner, ":")
		if !ok || hub == "" || user == "" {
			return nil, newUsageError("--owner must be <hub>:<user> (got %q)", opts.owner)
		}
		return []owners.Principal{{Hub: hub, UserID: user}}, nil
	case opts.allOwners:
		ownersRepo := owners.NewRepo(d.ReadDB())
		ps, err := ownersRepo.List(ctx)
		if err != nil {
			return nil, fmt.Errorf("list owners: %w", err)
		}
		return ps, nil
	default:
		return []owners.Principal{{Hub: cfg.Identity.Stub.Hub, UserID: cfg.Identity.Stub.UserID}}, nil
	}
}

const backfillBatch = 500

type backfillTally struct {
	processed int
	updated   int
	unchanged int
	failed    int
}

func (t backfillTally) summary() string {
	return fmt.Sprintf("gps backfill: processed=%d updated=%d unchanged=%d failed=%d",
		t.processed, t.updated, t.unchanged, t.failed)
}

func backfillForPrincipal(
	ctx context.Context,
	owner owners.Principal,
	opts *gpsBackfillOpts,
	repo *media.Repo,
	svc *service.MediaService,
	store storage.Store,
	places *geo.NaturalEarth,
	stderr io.Writer,
	tally *backfillTally,
) error {
	// Keyset cursor over id. afterID="" picks up the first page; the
	// last row's ID is passed to the next call. Repo.List...Candidates
	// requires keyset (not offset) because Full and Relabel mutate rows
	// that stay in the candidate set, and offset would skip-or-loop
	// depending on mode.
	afterID := ""
	for {
		page, err := repo.ListGPSBackfillCandidates(ctx, owner, opts.parsedMode, opts.sinceTime, afterID, backfillBatch)
		if err != nil {
			return fmt.Errorf("list gps candidates: %w", err)
		}
		if len(page) == 0 {
			return nil
		}
		for _, row := range page {
			if err := backfillOne(ctx, owner, row, opts.parsedMode, svc, store, places, stderr, tally); err != nil {
				// Per-row failures are tallied but don't abort the run.
				fmt.Fprintf(stderr, "gps backfill: row %s: %v\n", row.ID, err)
				tally.failed++
			}
			tally.processed++
			if tally.processed%100 == 0 {
				fmt.Fprintln(stderr, tally.summary())
			}
		}
		// FillMissing: every updated row leaves the candidate set, so
		// restart the cursor (the next un-updated row is now the
		// minimum id with both NULL). Full/Relabel: rows stay in the
		// candidate set — must advance the cursor past the last seen
		// id or we'd loop forever.
		if opts.parsedMode == media.GPSBackfillModeFillMissing {
			afterID = ""
		} else {
			afterID = page[len(page)-1].ID
		}
		if len(page) < backfillBatch {
			return nil
		}
	}
}

func backfillOne(
	ctx context.Context,
	owner owners.Principal,
	row media.Media,
	mode media.GPSBackfillMode,
	svc *service.MediaService,
	store storage.Store,
	places *geo.NaturalEarth,
	stderr io.Writer,
	tally *backfillTally,
) error {
	switch mode {
	case media.GPSBackfillModeRelabel:
		if row.Latitude == nil || row.Longitude == nil {
			tally.unchanged++
			return nil
		}
		label := ""
		if l, ok := places.Resolve(*row.Latitude, *row.Longitude); ok {
			label = l
		}
		if label == row.LocationLabel {
			tally.unchanged++
			return nil
		}
		if err := svc.UpdateGPS(ctx, owner, row.ID, row.Latitude, row.Longitude, row.GPSAt, label); err != nil {
			return err
		}
		tally.updated++
		return nil

	case media.GPSBackfillModeFull, media.GPSBackfillModeFillMissing:
		rc, err := store.ReadRange(ctx, owner, row.Path, 0, -1)
		if err != nil {
			return fmt.Errorf("read NAS bytes: %w", err)
		}
		defer func() { _ = rc.Close() }()
		meta, err := exifread.ExtractPhotoFromReader(rc)
		if err != nil {
			return fmt.Errorf("extract exif: %w", err)
		}
		// ExtractPhotoFromReader returns Metadata{} (no error) when the
		// file simply has no EXIF segment. Such rows naturally fall
		// through hasGPS=false below — no separate "skipped" counter.

		hasGPS := meta.Latitude != nil && meta.Longitude != nil
		if !hasGPS {
			if mode == media.GPSBackfillModeFillMissing {
				tally.unchanged++
				return nil
			}
			// full is authoritative: clear if EXIF has no GPS.
			if row.Latitude == nil && row.Longitude == nil && row.GPSAt == nil && row.LocationLabel == "" {
				tally.unchanged++
				return nil
			}
			if err := svc.UpdateGPS(ctx, owner, row.ID, nil, nil, nil, ""); err != nil {
				return err
			}
			tally.updated++
			return nil
		}

		label := ""
		if l, ok := places.Resolve(*meta.Latitude, *meta.Longitude); ok {
			label = l
		}
		if err := svc.UpdateGPS(ctx, owner, row.ID, meta.Latitude, meta.Longitude, meta.GPSAt, label); err != nil {
			return err
		}
		tally.updated++
		return nil

	default:
		return fmt.Errorf("unknown mode: %d", mode)
	}
}

```

> **NOTE for the implementer:** `gps.go` calls a shared storage helper. Before completing this step, factor `internal/cli/import.go`'s storage construction into `internal/cli/storage.go::buildStorage(cfg) (storage.Store, error)` (or whatever name matches the existing helper if present) and call it from both `import.go` and the new `gps.go`. Don't include a placeholder stub in the final implementation — and drop the `errors` import from `gps.go`'s import block if no other use survives.

- [ ] **Step 5: Register the gps command at the cobra root.**

In whatever `internal/cli/cli.go` (or equivalent `NewRootCmd`) constructs the root command, add:

```go
	cmd.AddCommand(newGPSCmd())
```

next to the existing `cmd.AddCommand(newThumbsCmd())` (or whatever neighbor exists).

- [ ] **Step 6: Implement the `--mode` happy-path behavioral tests.**

Append to `internal/cli/gps_test.go`. These tests seed media rows + (for full / fill-missing) NAS bytes; `relabel` only needs DB rows.

```go
func TestGPSBackfillRelabelOnlyTouchesRowsWithCoords(t *testing.T) {
	r := require.New(t)
	cfgDir := t.TempDir()
	cfgPath := writeStubConfig(t, cfgDir)
	t.Setenv("FOTOBANK_DB_PATH", filepath.Join(cfgDir, "fotobank.sqlite"))

	// Seed: open the DB outside the CLI to insert two rows — one with
	// coords and one without — then run the CLI, then assert only the
	// row-with-coords got an updated label.
	dbCtx := context.Background()
	d := testutil.OpenTestDBAt(t, filepath.Join(cfgDir, "fotobank.sqlite"))
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	owner := owners.Principal{Hub: "h", UserID: "u"}
	_, err := d.WriteDB().ExecContext(dbCtx,
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		owner.Hub, owner.UserID, "sk", time.Now().UTC(),
	)
	r.NoError(err)
	lat, lon := 48.8566, 2.3522
	rowWithGPS := uuid.NewString()
	rowNoGPS := uuid.NewString()
	r.NoError(repo.Insert(dbCtx, media.Media{
		ID: rowWithGPS, Owner: owner, Type: media.TypePhoto, MimeType: "image/jpeg",
		Path: "x.jpg", ImportedAt: time.Now().UTC(), Size: 1, Checksum: "c-" + rowWithGPS,
		Latitude: &lat, Longitude: &lon, ThumbStatus: "pending",
	}))
	r.NoError(repo.Insert(dbCtx, media.Media{
		ID: rowNoGPS, Owner: owner, Type: media.TypePhoto, MimeType: "image/jpeg",
		Path: "y.jpg", ImportedAt: time.Now().UTC(), Size: 1, Checksum: "c-" + rowNoGPS,
		ThumbStatus: "pending",
	}))
	d.Close()

	code, stdout, stderr := runGPS(t, "backfill", "--config", cfgPath, "--mode", "relabel")
	r.Equal(0, code, "stderr=%s", stderr)
	r.Contains(stdout, "gps backfill:")
	r.Contains(stdout, "updated=1")

	// Reopen and verify.
	d = testutil.OpenTestDBAt(t, filepath.Join(cfgDir, "fotobank.sqlite"))
	defer d.Close()
	repo = media.NewRepo(d.WriteDB(), d.ReadDB())
	got, err := repo.GetByID(dbCtx, rowWithGPS)
	r.NoError(err)
	r.Contains(got.LocationLabel, "France",
		"expected France-shaped label after relabel; got %q", got.LocationLabel)
	got, err = repo.GetByID(dbCtx, rowNoGPS)
	r.NoError(err)
	r.Empty(got.LocationLabel)
}

func TestGPSBackfillSkipsVideos(t *testing.T) {
	r := require.New(t)
	cfgDir := t.TempDir()
	cfgPath := writeStubConfig(t, cfgDir)
	t.Setenv("FOTOBANK_DB_PATH", filepath.Join(cfgDir, "fotobank.sqlite"))

	dbCtx := context.Background()
	d := testutil.OpenTestDBAt(t, filepath.Join(cfgDir, "fotobank.sqlite"))
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	owner := owners.Principal{Hub: "h", UserID: "u"}
	_, err := d.WriteDB().ExecContext(dbCtx,
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		owner.Hub, owner.UserID, "sk", time.Now().UTC(),
	)
	r.NoError(err)
	lat, lon := 48.8566, 2.3522
	videoID := uuid.NewString()
	r.NoError(repo.Insert(dbCtx, media.Media{
		ID: videoID, Owner: owner, Type: media.TypeVideo, MimeType: "video/mp4",
		Path: "v.mp4", ImportedAt: time.Now().UTC(), Size: 1, Checksum: "c-" + videoID,
		Latitude: &lat, Longitude: &lon, LocationLabel: "Original Label",
		ThumbStatus: "pending",
	}))
	d.Close()

	code, stdout, _ := runGPS(t, "backfill", "--config", cfgPath, "--mode", "relabel")
	r.Equal(0, code)
	r.Contains(stdout, "updated=0")

	d = testutil.OpenTestDBAt(t, filepath.Join(cfgDir, "fotobank.sqlite"))
	defer d.Close()
	repo = media.NewRepo(d.WriteDB(), d.ReadDB())
	got, err := repo.GetByID(dbCtx, videoID)
	r.NoError(err)
	r.Equal("Original Label", got.LocationLabel,
		"video row must be untouched by gps backfill")
}

func TestGPSBackfillFinalSummaryAlwaysEmitted(t *testing.T) {
	r := require.New(t)
	cfgDir := t.TempDir()
	cfgPath := writeStubConfig(t, cfgDir)
	t.Setenv("FOTOBANK_DB_PATH", filepath.Join(cfgDir, "fotobank.sqlite"))

	// Seed an owner with no rows. The summary should still print.
	dbCtx := context.Background()
	d := testutil.OpenTestDBAt(t, filepath.Join(cfgDir, "fotobank.sqlite"))
	_, err := d.WriteDB().ExecContext(dbCtx,
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		"h", "u", "sk", time.Now().UTC(),
	)
	r.NoError(err)
	d.Close()

	code, stdout, _ := runGPS(t, "backfill", "--config", cfgPath, "--mode", "relabel")
	r.Equal(0, code)
	r.True(strings.HasPrefix(strings.TrimSpace(stdout), "gps backfill:"),
		"expected summary line; got %q", stdout)
}
```

(`testutil.OpenTestDBAt(t, path)` opens at a specific path rather than using `t.TempDir`. If it doesn't exist, add it as a one-line helper next to `OpenTestDB`.)

- [ ] **Step 7: Run the new CLI tests.**

```bash
cd /path/to/fotobank
go test ./internal/cli/... -v -run "TestGPSBackfill"
```

Expected: PASS. If `OpenTestDBAt` is missing, add it:

```go
// In internal/testutil/db.go (or wherever OpenTestDB lives):
//
// OpenTestDBAt opens (and migrates) a sqlite DB at the given path —
// unlike OpenTestDB, it does NOT register a t.Cleanup closer because
// CLI tests routinely close + reopen across the subprocess boundary;
// the caller manages the lifetime explicitly.
func OpenTestDBAt(t *testing.T, path string) *db.DB {
	t.Helper()
	d, err := db.Open(path)
	require.NoError(t, err)
	require.NoError(t, migrate.RunUp(d))
	return d
}
```

- [ ] **Step 8: Run the full Go suite to catch any unexpected breakage.**

```bash
go test ./... -short -shuffle=on
```

Expected: PASS.

- [ ] **Step 9: Commit.**

```bash
git add internal/cli/gps.go internal/cli/gps_test.go internal/cli/cli.go internal/testutil/
git commit -m "feat(cli): fotobank gps backfill subcommand

Three modes: full re-extracts EXIF and resolves labels (clearing GPS
when EXIF lacks it); fill-missing only touches rows with both coords
NULL; relabel re-resolves labels for rows with coords without
re-reading EXIF. Owner scope (--owner / --all-owners) bypasses the
default stub-mode requirement, matching `fotobank thumbs regenerate`.
--since takes a Go time.Duration parsed before opening the DB so bad
input exits 2 with no side effects. Videos are skipped in all modes."
```

---

## Task 11: HTTP DTO additions + tests

**Files:**
- Modify: `internal/httpapi/media.go`
- Modify: `internal/httpapi/media_test.go`
- Regenerated: `openapi.json`

This task surfaces GPS through the JSON API on both list and detail endpoints. The omission tests assert against raw JSON bytes (not the unmarshalled struct) per spec §8.2 — that's the only way to catch wire-shape regressions when `omitempty` interacts with `*float64`.

- [ ] **Step 1: Extend `mediaDTO` with four GPS fields.**

Open `internal/httpapi/media.go`. After `DurationMs *int64 …`:

```go
	DurationMs    *int64     `json:"duration_ms,omitempty"`
	Latitude      *float64   `json:"latitude,omitempty"`
	Longitude     *float64   `json:"longitude,omitempty"`
	GPSAt         *time.Time `json:"gps_at,omitempty"`
	LocationLabel string     `json:"location_label,omitempty"`
}
```

- [ ] **Step 2: Update `toMediaDTO` to populate the new fields.**

```go
func toMediaDTO(m media.Media) mediaDTO {
	return mediaDTO{
		// ... existing assignments ...
		DurationMs:    m.DurationMs,
		Latitude:      m.Latitude,
		Longitude:     m.Longitude,
		GPSAt:         m.GPSAt,
		LocationLabel: m.LocationLabel,
	}
}
```

- [ ] **Step 3: Add HTTP tests for present + absent GPS.**

Append to `internal/httpapi/media_test.go`:

```go
func TestListMediaDTOIncludesGPSWhenPresent(t *testing.T) {
	r := require.New(t)
	srv, repo, p, _ := newMediaAPITest(t)

	id := uuid.NewString()
	lat, lon := 48.8566, 2.3522
	gps := time.Date(2024, 6, 15, 14, 30, 22, 0, time.UTC)
	r.NoError(repo.Insert(context.Background(), media.Media{
		ID: id, Owner: p, Type: media.TypePhoto, MimeType: "image/jpeg",
		Path: "x.jpg", ImportedAt: time.Now().UTC(), Size: 1, Checksum: "c-" + id,
		Latitude: &lat, Longitude: &lon, GPSAt: &gps,
		LocationLabel: "Paris, Île-de-France, France",
		ThumbStatus:   "pending",
	}))

	resp, err := http.Get(srv.URL + "/api/v1/media")
	r.NoError(err)
	defer func() { _ = resp.Body.Close() }()
	r.Equal(http.StatusOK, resp.StatusCode)
	bs, err := io.ReadAll(resp.Body)
	r.NoError(err)
	body := string(bs)
	r.Contains(body, `"latitude":48.8566`)
	r.Contains(body, `"longitude":2.3522`)
	r.Contains(body, `"gps_at":"2024-06-15T14:30:22Z"`)
	r.Contains(body, `"location_label":"Paris, Île-de-France, France"`)
}

func TestListMediaDTOOmitsGPSWhenAbsent(t *testing.T) {
	r := require.New(t)
	srv, repo, p, _ := newMediaAPITest(t)

	id := uuid.NewString()
	r.NoError(repo.Insert(context.Background(), media.Media{
		ID: id, Owner: p, Type: media.TypePhoto, MimeType: "image/jpeg",
		Path: "x.jpg", ImportedAt: time.Now().UTC(), Size: 1, Checksum: "c-" + id,
		ThumbStatus: "pending",
		// no GPS fields
	}))

	resp, err := http.Get(srv.URL + "/api/v1/media")
	r.NoError(err)
	defer func() { _ = resp.Body.Close() }()
	bs, err := io.ReadAll(resp.Body)
	r.NoError(err)
	body := string(bs)
	r.NotContains(body, "latitude")
	r.NotContains(body, "longitude")
	r.NotContains(body, "gps_at")
	r.NotContains(body, "location_label")
}

func TestGetMediaDTOIncludesGPSWhenPresent(t *testing.T) {
	r := require.New(t)
	srv, repo, p, _ := newMediaAPITest(t)

	id := uuid.NewString()
	lat, lon := 48.8566, 2.3522
	r.NoError(repo.Insert(context.Background(), media.Media{
		ID: id, Owner: p, Type: media.TypePhoto, MimeType: "image/jpeg",
		Path: "x.jpg", ImportedAt: time.Now().UTC(), Size: 1, Checksum: "c-" + id,
		Latitude: &lat, Longitude: &lon, LocationLabel: "Paris, France",
		ThumbStatus: "pending",
	}))

	resp, err := http.Get(srv.URL + "/api/v1/media/" + id)
	r.NoError(err)
	defer func() { _ = resp.Body.Close() }()
	r.Equal(http.StatusOK, resp.StatusCode)
	bs, err := io.ReadAll(resp.Body)
	r.NoError(err)
	body := string(bs)
	r.Contains(body, `"latitude":48.8566`)
	r.Contains(body, `"location_label":"Paris, France"`)
}

func TestGetMediaDTOOmitsGPSWhenAbsent(t *testing.T) {
	r := require.New(t)
	srv, repo, p, _ := newMediaAPITest(t)

	id := uuid.NewString()
	r.NoError(repo.Insert(context.Background(), media.Media{
		ID: id, Owner: p, Type: media.TypePhoto, MimeType: "image/jpeg",
		Path: "x.jpg", ImportedAt: time.Now().UTC(), Size: 1, Checksum: "c-" + id,
		ThumbStatus: "pending",
	}))

	resp, err := http.Get(srv.URL + "/api/v1/media/" + id)
	r.NoError(err)
	defer func() { _ = resp.Body.Close() }()
	bs, err := io.ReadAll(resp.Body)
	r.NoError(err)
	body := string(bs)
	r.NotContains(body, "latitude")
	r.NotContains(body, "location_label")
}
```

(`newMediaAPITest` is the existing fixture helper used by current tests; if it's named differently, match the existing pattern.)

- [ ] **Step 4: Run the httpapi tests.**

```bash
cd /path/to/fotobank
go test ./internal/httpapi/... -v -shuffle=on
```

Expected: PASS, including the four new GPS DTO tests.

- [ ] **Step 5: Regenerate the OpenAPI spec.**

```bash
make api-generate
```

Expected: `openapi.json` is rewritten and now contains the four new fields under the media schema. The `api-generate` prek hook will run automatically on commit anyway, but running it manually here surfaces any spec generation errors immediately.

- [ ] **Step 6: Commit.**

```bash
git add internal/httpapi/media.go internal/httpapi/media_test.go openapi.json
git commit -m "feat(httpapi): surface GPS in media list + detail DTOs

Adds latitude/longitude/gps_at/location_label to mediaDTO with
omitempty for absent fields. Tests assert on raw JSON bytes for both
the present and absent cases on both /api/v1/media and
/api/v1/media/{id}; only the raw-bytes assertion catches wire-shape
regressions caused by omitempty + *float64 round-trips."
```

---

## Task 12: Frontend Media type + thumbVersion + merge guard + get(id)

**Files:**
- Modify: `frontend/src/lib/media/mediaStore.svelte.ts`
- Modify: `frontend/src/lib/media/mediaStore.test.ts`

This task extends the inline `Media` type with `thumbVersion: number` and four optional GPS fields, updates the F2.0-era compile-time merge guard to include them, exposes `get(id): Media | undefined` backed by a parallel `byMediaId` map, and updates the JSON adapter that produces `Media` from API responses.

- [ ] **Step 1: Read the current Media type and merge implementation.**

```bash
sed -n '1,60p' /path/to/fotobank/frontend/src/lib/media/mediaStore.svelte.ts
sed -n '60,170p' /path/to/fotobank/frontend/src/lib/media/mediaStore.svelte.ts
```

The implementer must read the actual current shape before editing because the line numbers in the spec are a snapshot. The relevant landmarks: the inline `Media` interface near line 3, the `merge` method at line 64 with the compile-time `Exclude<keyof Media, ...>` guard at line 71, the `unchanged` predicate at line ~106, and the JSON adapter (probably named `toMedia` or similar) where `thumbUrl` is constructed around line 157.

- [ ] **Step 2: Extend the inline `Media` type.**

In `mediaStore.svelte.ts`, find the `export interface Media { … }` block and add five fields:

```ts
export interface Media {
  id: string;
  timestamp: string;
  taken: string;
  aspect: number;
  thumbUrl: string;
  thumbVersion: number;
  latitude?: number;
  longitude?: number;
  gps_at?: string;
  location_label?: string;
}
```

(Existing field set is whatever's in `keyof Media` at line 71 today. The implementer reads first and adapts the diff to the actual fields. Five new fields total.)

- [ ] **Step 3: Update the JSON adapter to populate `thumbVersion` and the four GPS fields.**

Find the `thumbUrl` construction (around line 157) and the surrounding object literal that produces `Media`. Add the new fields to the literal:

```ts
return {
  id,
  timestamp,
  taken,
  aspect,
  thumbUrl: `/api/v1/media/${id}/thumb?size=grid&v=${thumbVersion}`,
  thumbVersion,
  latitude: typeof raw["latitude"] === "number" ? raw["latitude"] : undefined,
  longitude: typeof raw["longitude"] === "number" ? raw["longitude"] : undefined,
  gps_at: typeof raw["gps_at"] === "string" ? raw["gps_at"] : undefined,
  location_label: typeof raw["location_label"] === "string" ? raw["location_label"] : undefined,
};
```

- [ ] **Step 4: Update the compile-time merge guard.**

Find the line:

```ts
type _IdentityFieldsCovered = Exclude<keyof Media, "id" | "timestamp" | "taken" | "aspect" | "thumbUrl">;
```

Extend the literal to include the new fields:

```ts
type _IdentityFieldsCovered = Exclude<
  keyof Media,
  "id" | "timestamp" | "taken" | "aspect" | "thumbUrl"
       | "thumbVersion" | "latitude" | "longitude" | "gps_at" | "location_label"
>;
```

- [ ] **Step 5: Update the `unchanged` predicate to compare the new fields.**

Find the `unchanged` block (around line 106 — looks something like `existing.thumbUrl === it.thumbUrl && …`). Append `&&` clauses for each new identity field:

```ts
const unchanged =
  existing.timestamp === it.timestamp
  && existing.taken === it.taken
  && existing.aspect === it.aspect
  && existing.thumbUrl === it.thumbUrl
  && existing.thumbVersion === it.thumbVersion
  && existing.latitude === it.latitude
  && existing.longitude === it.longitude
  && existing.gps_at === it.gps_at
  && existing.location_label === it.location_label;
```

(Match the actual surrounding code; the diff is "add five `&&` clauses for the five new fields".)

- [ ] **Step 6: Add the `byMediaId` parallel map and `get(id)` public method.**

Find the class declaration (it's probably `class MediaStore { ... }` and look for the existing private state). Add a new private map field next to the existing ones:

```ts
private byMediaId: Map<string, Media> = new Map();
```

In `merge(items)`, after the existing per-item bucket update, also do:

```ts
this.byMediaId.set(it.id, it);
```

(Insert this once per item; the `byMediaId` map is the source of truth for `get(id)`.)

Add a public method:

```ts
/** Return the cached row for id, or undefined if not in the store. */
get(id: string): Media | undefined {
  return this.byMediaId.get(id);
}
```

- [ ] **Step 7: Add vitest cases for `get(id)` and the merge field set.**

In `mediaStore.test.ts`, append:

```ts
it("get(id) returns merged row", () => {
  const store = new MediaStore({ list: vi.fn() } as never);
  const row: Media = {
    id: "a", timestamp: "2024-06-15T14:30:00Z", taken: "2024-06-15",
    aspect: 1.5, thumbUrl: "/api/v1/media/a/thumb?size=grid&v=3",
    thumbVersion: 3,
    latitude: 48.8566, longitude: 2.3522,
    gps_at: "2024-06-15T14:30:22Z",
    location_label: "Paris, France",
  };
  // @ts-expect-error: merge is private but test reaches in for setup
  store.merge([row]);
  expect(store.get("a")).toEqual(row);
});

it("get(id) returns undefined for missing id", () => {
  const store = new MediaStore({ list: vi.fn() } as never);
  expect(store.get("missing")).toBeUndefined();
});

it("merge updates byMediaId on subsequent merges", () => {
  const store = new MediaStore({ list: vi.fn() } as never);
  const v1: Media = {
    id: "a", timestamp: "t", taken: "d", aspect: 1, thumbUrl: "u1",
    thumbVersion: 1,
  };
  const v2: Media = { ...v1, thumbUrl: "u2", thumbVersion: 2 };
  // @ts-expect-error
  store.merge([v1]);
  expect(store.get("a")?.thumbUrl).toBe("u1");
  // @ts-expect-error
  store.merge([v2]);
  expect(store.get("a")?.thumbUrl).toBe("u2");
});
```

- [ ] **Step 8: Run typecheck and vitest.**

```bash
cd /path/to/fotobank/frontend
bun run typecheck
bun run test mediaStore
```

Expected: typecheck succeeds (the merge-guard `Exclude<...>` should evaluate to `never`, so the `_AssertNoUncoveredFields` type assertion still passes). Vitest passes including the three new tests.

- [ ] **Step 9: Commit.**

```bash
cd /path/to/fotobank
git add frontend/src/lib/media/mediaStore.svelte.ts frontend/src/lib/media/mediaStore.test.ts
git commit -m "feat(frontend): MediaStore exposes GPS + thumbVersion + get(id)

Extends Media with thumbVersion and four optional GPS fields. Updates
the F2.0 compile-time merge guard to cover the new fields and the
unchanged predicate to compare them on re-merge so dirty-tracking
keeps working. Adds a public get(id) backed by a parallel byMediaId
map; MediaDetail will use this in the next task to skip a fetch when
the row is already cached."
```

---

## Task 13: Frontend coords formatter + MediaDetail beefup

**Files:**
- Create: `frontend/src/lib/format/coords.ts`
- Create: `frontend/src/lib/format/coords.test.ts`
- Create: `frontend/src/routes/MediaDetail.test.ts`
- Modify: `frontend/src/routes/MediaDetail.svelte`
- Modify: `frontend/src/App.svelte`

This task takes `MediaDetail.svelte` from F2.0's stub (back link only) to a usable info view: photo at `size=preview`, plus a `<dl>` with Captured / Camera / Lens / Location rows. Inline-conditional rendering keeps the markup explicit and handles the "label OR coords" rule from §8.7.

- [ ] **Step 1: Create the coords formatter.**

```ts
// frontend/src/lib/format/coords.ts

/**
 * Format a (lat, lon) pair as "48.8566° N, 2.3522° E" (decimal degrees,
 * cardinal hemisphere). Defensive convention: lat=0 formats as N, lon=0
 * as E. The null-island case never reaches here in practice — exifread
 * drops it at extraction — but defining the convention keeps tests
 * deterministic.
 */
export function formatCoord(lat: number, lon: number): string {
  const ns = lat >= 0 ? "N" : "S";
  const ew = lon >= 0 ? "E" : "W";
  return `${Math.abs(lat).toFixed(4)}° ${ns}, ${Math.abs(lon).toFixed(4)}° ${ew}`;
}
```

- [ ] **Step 2: Test the coords formatter.**

```ts
// frontend/src/lib/format/coords.test.ts
import { describe, it, expect } from "vitest";
import { formatCoord } from "./coords";

describe("formatCoord", () => {
  it("northern + eastern hemisphere", () => {
    expect(formatCoord(48.8566, 2.3522)).toBe("48.8566° N, 2.3522° E");
  });
  it("southern + western hemisphere", () => {
    expect(formatCoord(-33.9249, -70.6483)).toBe("33.9249° S, 70.6483° W");
  });
  it("northern + western (e.g. NYC)", () => {
    expect(formatCoord(40.7128, -74.0060)).toBe("40.7128° N, 74.0060° W");
  });
  it("zero hemispheres default N / E", () => {
    expect(formatCoord(0, 0)).toBe("0.0000° N, 0.0000° E");
  });
  it("rounds to 4 decimal places", () => {
    expect(formatCoord(48.85664567, 2.35224567)).toBe("48.8566° N, 2.3522° E");
  });
});
```

- [ ] **Step 3: Run the formatter test.**

```bash
cd /path/to/fotobank/frontend
bun run test coords
```

Expected: PASS.

- [ ] **Step 4: Read the current MediaDetail.svelte stub.**

```bash
cat /path/to/fotobank/frontend/src/routes/MediaDetail.svelte
```

The stub renders a back link to `/library`. F2.1 keeps the back link (it stays SPA-routed via `handleInternalLinkClick` from F2.0) and adds the photo + info section.

- [ ] **Step 5: Rewrite `MediaDetail.svelte`.**

```svelte
<script lang="ts">
  import type { MediaStore, Media } from "../lib/media/mediaStore.svelte";
  import { handleInternalLinkClick } from "../lib/router/router.svelte";
  import { formatCoord } from "../lib/format/coords";
  import { onMount } from "svelte";

  let { id, mediaStore }: { id: string; mediaStore: MediaStore } = $props();

  let media = $state<Media | undefined>(mediaStore.get(id));
  let loadError = $state<string | undefined>(undefined);

  onMount(async () => {
    if (media) return;
    try {
      const resp = await fetch(`/api/v1/media/${id}`);
      if (!resp.ok) {
        loadError = `${resp.status}`;
        return;
      }
      const raw = await resp.json();
      // Reuse the store's own JSON-adapter pathway: merge a single-item
      // array so byMediaId is also populated. The store knows how to
      // build thumbUrl from the raw row.
      mediaStore.mergeRaw([raw]);
      media = mediaStore.get(id);
    } catch (e) {
      loadError = e instanceof Error ? e.message : "fetch failed";
    }
  });

  let previewUrl = $derived(
    media ? `/api/v1/media/${media.id}/thumb?size=preview&v=${media.thumbVersion}` : "",
  );

  let imgError = $state(false);
  let lastSrc: string | undefined = undefined;
  $effect(() => {
    if (previewUrl !== lastSrc) {
      lastSrc = previewUrl;
      imgError = false;
    }
  });

  function formatTimestamp(iso: string | undefined): string {
    if (!iso) return "";
    const d = new Date(iso);
    if (Number.isNaN(d.getTime())) return iso;
    return d.toISOString().replace("T", " ").replace(/:\d{2}\.\d{3}Z$/, " UTC");
  }
</script>

<div class="media-detail">
  <a
    href="/library"
    onclick={(e) => handleInternalLinkClick(e, "/library")}
    class="back-link"
  >
    ← Back to Library
  </a>

  {#if loadError}
    <p class="error">Could not load media: {loadError}</p>
  {:else if !media}
    <p>Loading…</p>
  {:else}
    <div class="photo">
      {#if !imgError}
        <img
          src={previewUrl}
          alt={media.location_label ?? media.id}
          onerror={() => (imgError = true)}
        />
      {:else}
        <div class="photo-placeholder">image unavailable</div>
      {/if}
    </div>

    <dl class="info">
      {#if media.timestamp}
        <dt>Captured</dt>
        <dd>{formatTimestamp(media.timestamp)}</dd>
      {/if}
      {#if media.location_label || (media.latitude != null && media.longitude != null)}
        <dt>Location</dt>
        <dd>
          {#if media.location_label}{media.location_label}{/if}
          {#if media.latitude != null && media.longitude != null}
            <small class="coord">{formatCoord(media.latitude, media.longitude)}</small>
          {/if}
        </dd>
      {/if}
    </dl>
  {/if}
</div>

<style>
  .media-detail { padding: 1rem; }
  .back-link { display: inline-block; margin-bottom: 1rem; }
  .photo img { max-width: 100%; max-height: 75vh; object-fit: contain; }
  .photo-placeholder {
    border: 1px dashed currentColor; padding: 2rem; text-align: center;
  }
  .info { display: grid; grid-template-columns: max-content 1fr; gap: 0.25rem 1rem; margin-top: 1rem; }
  .info dt { font-weight: 600; }
  .info .coord { display: block; opacity: 0.7; font-size: 0.85em; }
</style>
```

This template intentionally renders only `Captured` and `Location`. The Camera / Lens rows from the spec layout are out of scope here unless the existing JSON adapter already maps Make/Model/etc. into the frontend `Media` type — the implementer should grep for `make` / `model` in `mediaStore.svelte.ts`. If those fields are not yet on the frontend type, F2.1 does not add them; the spec §8.6 layout is illustrative and the only F2.1-mandated info row is Location.

- [ ] **Step 6: Add `mergeRaw` to MediaStore (if it doesn't already exist).**

The Svelte template above calls `mediaStore.mergeRaw([raw])`. The store already has an internal adapter that converts an API response into `Media` shapes. If a public `mergeRaw(rawItems: unknown[])` doesn't exist, add one — it's a thin wrapper that runs the existing adapter and then calls private `merge()`:

```ts
public mergeRaw(rawItems: unknown[]): void {
  const adapted = rawItems.map(/* whatever the existing list-response adapter does */);
  this.merge(adapted);
}
```

If the existing list-response handler in the store already exposes its adapter as a named function, call that directly.

- [ ] **Step 7: Update `App.svelte` to pass `mediaStore` into MediaDetail.**

```svelte
{#if router.current.route === "media"}
  <MediaDetail id={router.current.id} {mediaStore} />
{/if}
```

(Match whatever surrounding JSX/Svelte routing block already exists.)

- [ ] **Step 8: Add a vitest for MediaDetail rendering.**

```ts
// frontend/src/routes/MediaDetail.test.ts
import { describe, it, expect } from "vitest";
import { render } from "@testing-library/svelte";
import MediaDetail from "./MediaDetail.svelte";
import { MediaStore, type Media } from "../lib/media/mediaStore.svelte";

function fakeStore(rows: Media[]): MediaStore {
  const s = new MediaStore({ list: async () => ({ data: { items: [], next_offset: null } }) } as never);
  // @ts-expect-error: merge is private
  s.merge(rows);
  return s;
}

const baseRow = (over: Partial<Media> = {}): Media => ({
  id: "abc-123",
  timestamp: "2024-06-15T14:30:22Z",
  taken: "2024-06-15",
  aspect: 1.5,
  thumbUrl: "/api/v1/media/abc-123/thumb?size=grid&v=3",
  thumbVersion: 3,
  ...over,
});

describe("MediaDetail", () => {
  it("renders Location row when location_label is set", () => {
    const store = fakeStore([baseRow({
      location_label: "Paris, Île-de-France, France",
      latitude: 48.8566,
      longitude: 2.3522,
    })]);
    const { getByText } = render(MediaDetail, { props: { id: "abc-123", mediaStore: store } });
    expect(getByText("Location")).toBeTruthy();
    expect(getByText("Paris, Île-de-France, France")).toBeTruthy();
    expect(getByText("48.8566° N, 2.3522° E")).toBeTruthy();
  });

  it("renders coords-only when label is absent", () => {
    const store = fakeStore([baseRow({
      latitude: 48.8566,
      longitude: 2.3522,
    })]);
    const { getByText, queryByText } = render(MediaDetail, { props: { id: "abc-123", mediaStore: store } });
    expect(getByText("Location")).toBeTruthy();
    expect(getByText("48.8566° N, 2.3522° E")).toBeTruthy();
    expect(queryByText(/Paris/)).toBeNull();
  });

  it("renders no Location row when neither label nor coords", () => {
    const store = fakeStore([baseRow({})]);
    const { queryByText } = render(MediaDetail, { props: { id: "abc-123", mediaStore: store } });
    expect(queryByText("Location")).toBeNull();
  });
});
```

- [ ] **Step 9: Run frontend typecheck and vitest.**

```bash
cd /path/to/fotobank/frontend
bun run typecheck
bun run test
```

Expected: PASS, including the three new MediaDetail tests and the coords formatter tests.

- [ ] **Step 10: Manual smoke test in a browser.**

```bash
cd /path/to/fotobank
make dev   # spins up the server with live-reload via air
```

Then in another terminal: `fotobank import` against a directory containing `testdata/exif/photo-with-gps.jpg`, navigate to `http://localhost:8080/library`, click into the imported photo, verify:
- The photo appears at `size=preview` (not the tiny grid thumb, not a broken image).
- The Location row shows "France" (or finer) plus the formatted coords.

If anything is off, fix and re-run before committing.

- [ ] **Step 11: Commit.**

```bash
git add frontend/src/lib/format/coords.ts \
        frontend/src/lib/format/coords.test.ts \
        frontend/src/routes/MediaDetail.svelte \
        frontend/src/routes/MediaDetail.test.ts \
        frontend/src/App.svelte \
        frontend/src/lib/media/mediaStore.svelte.ts
git commit -m "feat(frontend): MediaDetail beefup with location info

Replaces the F2.0 stub with a functional detail view: photo at
size=preview, formatted timestamp, and Location row that renders when
either a location_label or coordinates are present (each is guarded
independently so coords-only rows still surface). MediaStore.get(id)
short-circuits the fetch when the row is already cached; on miss the
view fetches /api/v1/media/{id} once and merges. Coord formatting
uses decimal degrees with cardinal hemispheres."
```

---

## Task 14: Playwright e2e for MediaDetail GPS rendering + final hygiene

**Files:**
- Modify: `frontend/tests/e2e/library.spec.ts`

This task locks the end-to-end rendering contract: a `/media/<id>` reload for a row with GPS shows the location label, and a row without GPS shows neither the Location row nor any GPS noise. Plus a final hygiene pass over the full Go and frontend test suites.

- [ ] **Step 1: Append two e2e tests to `library.spec.ts`.**

```ts
test("MediaDetail shows location label when row has GPS", async ({ page }) => {
  // Seed a row via the API. The test harness already has an admin path
  // for inserting rows; if not, the simplest seeding is via fotobank
  // import against a tmp dir containing testdata/exif/photo-with-gps.jpg.
  // See playwright.config.ts globalSetup for the existing pattern.

  // For this test we assume a row is already seeded by globalSetup with
  // id=GPS_FIXTURE_ID and GPS pointing at Paris.
  const id = process.env.GPS_FIXTURE_ID;
  test.skip(!id, "GPS_FIXTURE_ID not set; configure in globalSetup to seed a row from photo-with-gps.jpg");

  await page.goto(`/media/${id}`);
  await expect(page.getByText("fotobank")).toBeVisible();
  await expect(page.getByText("Location")).toBeVisible();
  // Resolver produces a country containing "France" (and likely "Paris"
  // / "Île-de-France"). Match loosely.
  await expect(page.getByText(/France/)).toBeVisible();
  await expect(page.getByText(/° N, .*° E/)).toBeVisible();
});

test("MediaDetail hides location row when row has no GPS", async ({ page }) => {
  const id = process.env.NO_GPS_FIXTURE_ID;
  test.skip(!id, "NO_GPS_FIXTURE_ID not set; configure in globalSetup to seed a GPS-less row");

  await page.goto(`/media/${id}`);
  await expect(page.getByText("fotobank")).toBeVisible();
  await expect(page.getByText("Location")).not.toBeVisible();
});
```

> **Implementer note on seeding.** F2.0's e2e setup already imports a fixture directory. Extend the globalSetup to also import `testdata/exif/photo-with-gps.jpg` and `testdata/exif/photo-no-exif.jpg`, capture the resulting row IDs (e.g. by querying the API right after import), and expose them as `GPS_FIXTURE_ID` / `NO_GPS_FIXTURE_ID` env vars for the test process. If F2.0's setup uses a different mechanism (e.g. injects rows directly via SQL), adapt accordingly. The two `test.skip` calls above are deliberate guards so the test doesn't false-pass when seeding regresses.

- [ ] **Step 2: Run the e2e suite.**

```bash
cd /path/to/fotobank/frontend
bunx playwright test
```

Expected: PASS, including the two new MediaDetail tests. If they `skip`, the seeding wasn't extended — fix the globalSetup before continuing.

- [ ] **Step 3: Final-pass hygiene — run every gate the project enforces.**

```bash
cd /path/to/fotobank

# Go: full suite (not just -short).
go test ./... -shuffle=on

# Linter and nilaway.
make lint
make nilaway

# Frontend.
cd frontend
bun run typecheck
bun run test
bunx playwright test
```

Expected: every command green. Fix anything red before committing.

- [ ] **Step 4: Verify the make build still produces a runnable binary that boots.**

```bash
cd /path/to/fotobank
make build
./bin/fotobank --help | head
./bin/fotobank gps --help
./bin/fotobank gps backfill --help
```

Expected:
- `fotobank --help` lists `gps` as one of the commands.
- `fotobank gps --help` shows the subcommand list (`backfill`).
- `fotobank gps backfill --help` shows all five flags (`--config`, `--since`, `--mode`, `--owner`, `--all-owners`).

- [ ] **Step 5: Commit.**

```bash
git add frontend/tests/e2e/library.spec.ts frontend/playwright.config.ts frontend/tests/e2e/global-setup.ts
# (Adjust the global-setup path to whatever F2.0 uses.)
git commit -m "test(e2e): MediaDetail location row visibility

Two Playwright tests lock the GPS rendering contract: a row with GPS
shows the Location label + formatted coords on /media/<id>; a row
without GPS shows no Location row at all. globalSetup seeds the two
fixture rows and exports their IDs as env vars."
```

- [ ] **Step 6: (Optional) Operator post-deploy checklist note.**

The plan is now landable. After F2.1 deploys, the operator runs:

```bash
fotobank gps backfill --all-owners --mode=fill-missing
```

…to populate `latitude`/`longitude`/`location_label` for F1-vintage rows that imported before the EXIF GPS extractor existed. This is a one-time post-deploy action, mirroring the F2.0 thumbs-regenerate post-deploy step.

---

## Self-review checklist

Run this after every task is done to catch loose ends before declaring the plan complete:

- [ ] `git log --oneline | head -20` — every task should be one commit (some may be two if the implementer split a task into multiple commits during review loops; that's fine, the count just shouldn't be wildly off).
- [ ] `go test ./... -shuffle=on -count=2` — pass under shuffle and at multiple counts.
- [ ] `cd frontend && bun run test --run --no-watch` — vitest passes deterministically.
- [ ] `cd frontend && bunx playwright test --workers=1` — e2e passes serially.
- [ ] `make lint && make nilaway` — both clean.
- [ ] `make api-generate && git status` — nothing left to regenerate (the openapi.json commit landed in Task 11).
- [ ] **Manual:** `make dev`, navigate to `/library`, click an imported GPS-bearing photo, confirm the Location row reads the way the spec layout describes.
- [ ] **Manual:** `fotobank gps backfill --mode=relabel --config <test-config>` against a DB with one GPS row — verify the row's `location_label` updates and the summary line is emitted.
- [ ] **Manual:** `fotobank gps backfill --since=garbage --config <test-config>` — verify exit code 2 and that the DB file is not created on disk.

Spec coverage cross-check (every spec section maps to at least one task):

| Spec section | Implementing tasks |
|---|---|
| §3 Architecture | 6 (geo), 7 (PlaceResolver injection), 9 (server caller), 10 (CLI caller) |
| §4 DB schema | 2 (migration), 3 (struct + 4 projection sites + scan + Insert), 4 (UpdateGPS, ListGPSBackfillCandidates, GPSBackfillMode) |
| §5 EXIF GPS | 5 |
| §6 Geo package | 1 (data vendoring), 6 (resolver + tests + benchmark) |
| §7 Importer + CLI | 7 (importer), 8 (service), 9 (import.go), 10 (gps.go) |
| §8 HTTP DTO + Frontend | 11 (DTO), 12 (Media type + merge guard + get), 13 (MediaDetail), 14 (e2e) |
| §9 Out-of-scope reminders | enforced via test (no video GPS in Task 5; backfill skips videos in Task 10/4) |
| §10 Risks | migration-edit risk addressed in Task 2 (FOTOBANK_MIGRATION_BASE_REF), coord-swap in Task 6 test, four projection sites in Task 3 single commit, init time in Task 6 benchmark, F1-vintage rows in Task 14 Step 6 |
| §11 Dependencies | F2.0 prereqs verified in Pre-flight check |
