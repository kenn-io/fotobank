# F2.2 RAW + JPEG Pairing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Treat same-basename JPEG + RAW imports as one image with two files: hide the RAW from list views, surface the pair on the JPEG's detail page, and stream the RAW bytes via the existing `/original` endpoint.

**Architecture:** Schema gains `import_source_path` + `paired_with_id` (FK to `media.id` with `ON DELETE SET NULL`, a self-reference CHECK, owner-consistency triggers, and a per-owner-directory index). A single-threaded post-import barrier pass computes pair relationships against the union of just-imported rows + existing rows in touched `(owner, directory)` keys. A new `fotobank pair backfill` CLI runs the same pure pairing function across the library. HTTP detail endpoints embed sidecar/primary references; list endpoints filter sidecars at the SQL layer. The primary's `MediaDetail` route renders a Files row; sidecar direct URLs render a banner-plus-download layout. Cross-subsystem changes: `AlbumService` and `ShareService` reject sidecar IDs at create/add time; `share.Repo.CoverMediaByScopes` amends to cover sidecars whose primary is in scope; `thumb.isRAWMime` adds NEF.

**Tech Stack:** Go (modernc.org/sqlite, cobra, huma/v2, golang-migrate, testify), Svelte 5 frontend, `golang.org/x/text/unicode/norm` (new direct dep).

**Spec:** `docs/superpowers/specs/2026-04-28-fotobank-web-f2.2-raw-jpeg-pairing-design.md`

---

## File map

**Created**
- `internal/ingest/pair.go` — `PairClass` classifier + pure `Compute([]PairCandidate) []PairUpdate`.
- `internal/ingest/pair_test.go` — idempotency, commutativity, ambiguity-transition, bidirectional tests.
- `internal/cli/pair.go` — `fotobank pair backfill` subcommand (mirrors `internal/cli/gps.go`).
- `internal/cli/pair_test.go` — CLI happy-path + flag validation tests.

**Modified**
- `go.mod`, `go.sum` — add `golang.org/x/text` direct dep.
- `internal/db/migrations/000001_initial_schema.up.sql` — `import_source_path`, `paired_with_id`, CHECK, two triggers, one index.
- `internal/media/media.go` — `Media.ImportSourcePath`, `Media.PairedWithID`, `Media.Sidecars`; `ListFilter.IncludeSidecars`.
- `internal/media/repo.go` — Insert / Scan / List learn the new columns; new `ListByOwnerDirectories`, `UpdatePairedWithID`, `GetSidecars` helpers.
- `internal/media/repo_test.go` — round-trip + filter + helper tests.
- `internal/service/media_service.go` — `List` server-side clamps `IncludeSidecars`; `GetSidecars(primaryID, caller)` passthrough.
- `internal/service/media_service_test.go` — sidecar filter tests.
- `internal/service/album_service.go:194-206` — sidecar rejection in the per-ID pre-flight loop.
- `internal/service/album_service_test.go` — sidecar rejection test.
- `internal/service/share_service.go:107-115` — sidecar rejection in the media-set per-ID pre-flight loop.
- `internal/service/share_service_test.go` — sidecar rejection test.
- `internal/share/repo.go::CoverMediaByScopes` — `OR media.paired_with_id IN (...)` clause.
- `internal/share/repo_test.go` — recipient-sidecar-access test, ListSharedMediaIDs primary-only test.
- `internal/ingest/importer.go::buildMediaRow` — `ImportSourcePath` capture, `OriginalFilename` flipped to `filepath.Base(c.Path)`.
- `internal/ingest/importer.go::ImportDirectory` — single-threaded post-barrier pairing pass.
- `internal/ingest/importer_test.go` — fixture updates + bidirectional pairing test.
- `internal/cli/import_test.go` — fixture updates for the basename flip.
- `internal/cli/root.go` — register `newPairCmd`.
- `internal/httpapi/media.go` — `mediaDTO.PairedWithID`, `PairedWith`, `Sidecars`; list handler defaults to primaries-only; detail handler embeds sidecars / paired_with summary.
- `internal/httpapi/media_test.go` — sidecar list-filter, primary-detail-with-sidecars, sidecar-detail-with-paired-with tests.
- `internal/thumb/worker.go::isRAWMime` — adds `image/x-nikon-nef`.
- `internal/thumb/raw_test.go` — NEF fixture (or synthetic NEF-MIME row pointed at an embedded-JPEG TIFF).
- `internal/errs/errs.go` — `ErrPairedSidecarsExist` sentinel (future-contract; not yet thrown by any service path).
- `internal/httpapi/errors.go::Translate` — sentinel → 409 Conflict mapping.
- `internal/httpapi/errors_test.go` — translator test for the new sentinel.
- `frontend/src/lib/media/mediaStore.svelte.ts` — `Media` type adds `paired_with_id?`, `paired_with?`, `sidecars?`; `merge()` guard updated; `toMedia` populates new fields.
- `frontend/src/lib/media/mediaStore.test.ts` — type-level extension tests.
- `frontend/src/routes/MediaDetail.svelte` — Files row on primaries with sidecars; banner-plus-download layout on direct sidecar visits.
- `frontend/tests/e2e/library.spec.ts` — primary-detail Files row, sidecar direct page.
- `openapi.json`, `frontend/src/lib/api/generated/schema.ts` — regenerated by `make api-generate` after the DTO change.

**Untouched (per spec)**
- `internal/db/migrations/000001_initial_schema.down.sql` — already drops `media` whole; subsumes the schema change.
- `internal/reconcile/...` — no special handling for paired rows.
- `internal/media/repo.go::ListGPSBackfillCandidates` — GPS backfill stays unfiltered.

---

## Task 1: Pre-flight + add `golang.org/x/text` direct dep

The implementation plan begins with the pre-flight ritual that the F2.x sub-plans established for in-place edits to `000001_initial_schema.up.sql`, plus a new direct dependency on `golang.org/x/text/unicode/norm` for NFC normalization in the pairing pass.

**Files:**
- Modify: `go.mod`, `go.sum`

- [ ] **Step 1: Capture the current migration base ref**

```bash
git rev-parse HEAD
```

Record the SHA. We'll set `FOTOBANK_MIGRATION_BASE_REF=<SHA>` in step 2 so the prek `migration-history-check` hook permits the in-place edit during this F2.2 sub-plan. The same ref is what the F2.1 implementation plan used at its Task 2.

- [ ] **Step 2: Export `FOTOBANK_MIGRATION_BASE_REF` for the rest of this plan**

```bash
export FOTOBANK_MIGRATION_BASE_REF=<SHA-from-step-1>
```

This must be exported in every shell that runs `git commit` for tasks 2-18. Without it, the prek hook will reject the in-place edit to `000001_initial_schema.up.sql`. The hook reads the env var at commit time.

- [ ] **Step 3: Add the new direct Go dependency**

```bash
go get golang.org/x/text/unicode/norm
go mod tidy
```

`go.mod` should now have `golang.org/x/text vX.Y.Z` (latest stable at execution time) in the direct require block. `go.sum` updates with checksums.

- [ ] **Step 4: Verify the dep is direct, not indirect**

```bash
grep -A1 'require' go.mod | grep 'golang.org/x/text'
```

Expected: `golang.org/x/text vX.Y.Z` WITHOUT the `// indirect` suffix. If it's marked indirect, the implementation has not yet imported it; that's fine — Task 8 will import it.

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum
git commit -m "chore(deps): add golang.org/x/text for NFC normalization

F2.2 pairing pass needs unicode/norm.NFC to compare directory
paths across mixed macOS NFD / Linux NFC source filesystems."
```

---

## Task 2: Schema — columns + CHECK + triggers + index

Squash the F2.2 schema additions into `000001_initial_schema.up.sql` in place, mirroring the F2.0/F2.1 pattern.

**Files:**
- Modify: `internal/db/migrations/000001_initial_schema.up.sql:23-71`

- [ ] **Step 1: Add `import_source_path` and `paired_with_id` columns + self-reference CHECK to the `media` CREATE TABLE**

Edit `internal/db/migrations/000001_initial_schema.up.sql` between the existing `location_label TEXT,` (line 50) and the `thumb_status` block. Insert these column declarations before the existing FK + UNIQUE clauses:

```sql
    -- F2.2 RAW + JPEG pairing.
    -- Root-relative original path captured at import time; substrate
    -- for pair detection.
    import_source_path TEXT NOT NULL DEFAULT '',
    -- FK to JPEG primary; NULL on primaries and standalones.
    -- ON DELETE SET NULL is the referential-integrity floor; the
    -- service layer (§8.7) blocks user-facing deletes when sidecars
    -- exist.
    paired_with_id     UUID REFERENCES media(id) ON DELETE SET NULL,
```

Add a self-reference CHECK in the table-level constraint block (after the existing UNIQUE constraints, before the closing `)` of the `CREATE TABLE media` statement):

```sql
    CHECK (paired_with_id IS NULL OR paired_with_id <> id),
```

- [ ] **Step 2: Add the `media_owner_import_source_path_idx` index**

Append after the existing `media_owner_geo_idx` (currently at line 70):

```sql
CREATE INDEX media_owner_import_source_path_idx
    ON media(owner_hub, owner_user_id, import_source_path);
```

- [ ] **Step 3: Add the owner-consistency trigger pair**

Append after the new index, before the next `CREATE TABLE` (`albums`):

```sql
-- Owner-consistency triggers on paired_with_id. Mirrors the
-- album_media_owner_consistency_* pair below; defence in depth even
-- though the service-layer pairing pass restricts candidates to one
-- owner per (owner, directory) group.
CREATE TRIGGER media_paired_with_owner_consistency_insert
BEFORE INSERT ON media
FOR EACH ROW
WHEN NEW.paired_with_id IS NOT NULL
BEGIN
    SELECT CASE
        WHEN (SELECT owner_hub FROM media WHERE id = NEW.paired_with_id)
                 != NEW.owner_hub
          OR (SELECT owner_user_id FROM media WHERE id = NEW.paired_with_id)
                 != NEW.owner_user_id
        THEN RAISE(ABORT, 'sidecar and primary must share owner')
    END;
END;

CREATE TRIGGER media_paired_with_owner_consistency_update
BEFORE UPDATE OF paired_with_id, owner_hub, owner_user_id ON media
FOR EACH ROW
WHEN NEW.paired_with_id IS NOT NULL
BEGIN
    SELECT CASE
        WHEN (SELECT owner_hub FROM media WHERE id = NEW.paired_with_id)
                 != NEW.owner_hub
          OR (SELECT owner_user_id FROM media WHERE id = NEW.paired_with_id)
                 != NEW.owner_user_id
        THEN RAISE(ABORT, 'sidecar and primary must share owner')
    END;
END;
```

- [ ] **Step 4: Verify the schema compiles via the test DB**

```bash
go test ./internal/testutil/... -run TestOpenTestDB -count=1
```

Expected: PASS. `OpenTestDB` runs the migrations against a fresh SQLite file; if the SQL is malformed, this test fails first.

- [ ] **Step 5: Verify the prek migration hook accepts the in-place edit**

```bash
git add internal/db/migrations/000001_initial_schema.up.sql
git -c hooks.allowtrespass=false commit -m "feat(db): F2.2 paired_with_id schema

Add import_source_path + paired_with_id (FK to media.id with
ON DELETE SET NULL + self-reference CHECK), owner-consistency
trigger pair, and media_owner_import_source_path_idx in place
on 000001_initial_schema.up.sql. Pre-deploy migration via
FOTOBANK_MIGRATION_BASE_REF.

Down file unchanged (it already drops media whole).
"
```

If the prek hook rejects, verify `FOTOBANK_MIGRATION_BASE_REF` is exported (Task 1 step 2).

---

## Task 3: `media.Media` struct + repo column round-trip

Add the new fields to the in-memory type and teach the repo's Insert / Scan / List statements about them. The `Sidecars` field is in-memory only — never persisted; it's populated on detail reads.

**Files:**
- Modify: `internal/media/media.go`
- Modify: `internal/media/repo.go`
- Modify: `internal/media/repo_test.go`

- [ ] **Step 1: Add fields to `media.Media`**

Edit `internal/media/media.go`. After the existing `LocationLabel string` field (around line 50), add:

```go
	// F2.2 RAW+JPEG pairing.
	// Root-relative original path captured at import time. Substrate
	// for pair detection. Empty for pre-F2.2 dev rows.
	ImportSourcePath string
	// FK to the JPEG primary when this row is a sidecar. NULL on
	// primaries and standalones.
	PairedWithID *string
	// Sidecars is in-memory only — never persisted. Populated by
	// MediaService.GetSidecars when the handler builds a primary's
	// detail DTO.
	Sidecars []Media
```

- [ ] **Step 2: Write a failing repo round-trip test**

Edit `internal/media/repo_test.go`. Append:

```go
func TestRepoInsertGetByIDPreservesPairingFields(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	owner := testutil.SeedOwner(t, d)

	primary := newTestMedia(owner, "2024/a.jpg", "cs-pri")
	primary.ImportSourcePath = "2024-Paris/IMG_1234.JPG"
	r.NoError(repo.Insert(context.Background(), primary))

	sidecarID := uuid.NewString()
	sidecar := newTestMedia(owner, "2024/a.dng", "cs-sid")
	sidecar.ID = sidecarID
	sidecar.ImportSourcePath = "2024-Paris/IMG_1234.DNG"
	sidecar.PairedWithID = &primary.ID
	r.NoError(repo.Insert(context.Background(), sidecar))

	gotPrimary, err := repo.GetByID(context.Background(), primary.ID)
	r.NoError(err)
	r.Equal("2024-Paris/IMG_1234.JPG", gotPrimary.ImportSourcePath)
	r.Nil(gotPrimary.PairedWithID)

	gotSidecar, err := repo.GetByID(context.Background(), sidecarID)
	r.NoError(err)
	r.Equal("2024-Paris/IMG_1234.DNG", gotSidecar.ImportSourcePath)
	r.NotNil(gotSidecar.PairedWithID)
	r.Equal(primary.ID, *gotSidecar.PairedWithID)
}
```

If `newTestMedia` doesn't already exist in `repo_test.go`, search the file for the existing helper that builds a baseline `media.Media` (typical name pattern is `makeMedia` or similar); if none, lift the helper from `internal/service/media_service_test.go::insertTestMedia` style.

- [ ] **Step 3: Run the test to verify it fails**

```bash
go test ./internal/media/... -run TestRepoInsertGetByIDPreservesPairingFields -count=1 -v
```

Expected: FAIL — either compile error (`unknown field ImportSourcePath`) or runtime fail because `Insert`'s SQL doesn't list the new columns.

- [ ] **Step 4: Update `repo.go` Insert SQL + Scan helper**

In `internal/media/repo.go`, update the `INSERT INTO media (...)` column list to include `import_source_path` and `paired_with_id`, with matching positional placeholders. Update the Scan helper (`func scanMedia` or similar — search the file) to read the two new columns into the new struct fields. `paired_with_id` is nullable: read into a `sql.NullString` then assign `&ns.String` to `m.PairedWithID` when `ns.Valid`.

The exact column-list lines need editing in both the `INSERT` and the `SELECT` projections used by `GetByID`, `List`, and any other callers. Search for the existing column list (it currently ends with `thumb_updated_at`) and add the two new columns at the end of the projection. The argument lists used by `Insert` must match.

- [ ] **Step 5: Run the test to verify it passes**

```bash
go test ./internal/media/... -run TestRepoInsertGetByIDPreservesPairingFields -count=1 -v
```

Expected: PASS.

- [ ] **Step 6: Run the full media + service test suites to confirm no regression from the column-list change**

```bash
go test ./internal/media/... ./internal/service/... -count=1
```

Expected: PASS. If any test fails because the SELECT projection added unread columns, update the Scan helper to consume them.

- [ ] **Step 7: Commit**

```bash
git add internal/media/media.go internal/media/repo.go internal/media/repo_test.go
git commit -m "feat(media): persist import_source_path + paired_with_id

Round-trip the F2.2 schema additions through media.Media and
the repo's Insert/Scan/Get/List statements. Sidecars is the
in-memory-only field populated by the handler when building
primary detail DTOs."
```

---

## Task 4: `ListFilter.IncludeSidecars` + repo SQL clamp + service clamp

By default, the list endpoint must hide sidecars. The flag exists for internal callers (the pairing pass, the backfill CLI, reconcile) but no HTTP route surfaces `true`.

**Files:**
- Modify: `internal/media/media.go::ListFilter`
- Modify: `internal/media/repo.go::List`
- Modify: `internal/service/media_service.go::List`
- Modify: `internal/service/media_service_test.go`

- [ ] **Step 1: Add `IncludeSidecars` to `ListFilter`**

Edit `internal/media/media.go`. Append to the `ListFilter` struct:

```go
	// F2.2 RAW+JPEG pairing. False at the service layer by default;
	// service.MediaService.List clamps caller-supplied true values
	// back to false so no HTTP route can surface sidecars in lists.
	// Internal callers (pairing pass, backfill, reconcile) may set
	// true.
	IncludeSidecars bool
```

- [ ] **Step 2: Write a failing service test for the default-hide behavior**

Append to `internal/service/media_service_test.go`:

```go
func TestMediaServiceListHidesSidecarsByDefault(t *testing.T) {
	r := require.New(t)
	fx := newMediaServiceTest(t)
	ctx := context.Background()

	primary := insertTestMedia(t, fx.repo, fx.owner, "2024/a.jpg", "cs-pri")
	sidecar := insertTestMedia(t, fx.repo, fx.owner, "2024/a.dng", "cs-sid")
	r.NoError(fx.repo.UpdatePairedWithID(ctx, sidecar.ID, &primary.ID))

	rows, err := fx.svc.List(ctx, media.ListFilter{}, fx.owner)
	r.NoError(err)
	r.Len(rows, 1)
	r.Equal(primary.ID, rows[0].ID)
}

func TestMediaServiceListClampsIncludeSidecars(t *testing.T) {
	r := require.New(t)
	fx := newMediaServiceTest(t)
	ctx := context.Background()

	primary := insertTestMedia(t, fx.repo, fx.owner, "2024/a.jpg", "cs-pri")
	sidecar := insertTestMedia(t, fx.repo, fx.owner, "2024/a.dng", "cs-sid")
	r.NoError(fx.repo.UpdatePairedWithID(ctx, sidecar.ID, &primary.ID))

	// Caller asks for true; service must clamp it back to false.
	rows, err := fx.svc.List(ctx, media.ListFilter{IncludeSidecars: true}, fx.owner)
	r.NoError(err)
	r.Len(rows, 1)
	r.Equal(primary.ID, rows[0].ID)
}
```

These reference `repo.UpdatePairedWithID` which is added in Task 5; if Task 5 lands first, the tests should compile. For now we accept that this task's tests temporarily fail to compile — Task 5 unblocks them. (Reorder Tasks 4 and 5 if the implementer prefers; the tests are written here because they belong with the filter logic.)

- [ ] **Step 3: Run the tests to verify they fail (or fail to compile)**

```bash
go test ./internal/service/... -run TestMediaServiceListHidesSidecars -count=1 -v
```

Expected: FAIL or compile error citing the missing helper.

- [ ] **Step 4: Update `repo.List` SQL**

In `internal/media/repo.go::List`, when `filter.IncludeSidecars` is false (default), append `AND paired_with_id IS NULL` to the WHERE clause. Use the same parameter-binding style as the existing predicate builders.

- [ ] **Step 5: Update `service.MediaService.List` to clamp**

In `internal/service/media_service.go::List`, before calling `s.repo.List(ctx, f)`, force `f.IncludeSidecars = false`:

```go
func (s *MediaService) List(ctx context.Context, f media.ListFilter, caller owners.Principal) ([]media.Media, error) {
	f.Owner = caller
	f.IncludeSidecars = false
	return s.repo.List(ctx, f)
}
```

- [ ] **Step 6: Run the tests (after Task 5 lands `UpdatePairedWithID`)**

```bash
go test ./internal/service/... -count=1
```

Expected: PASS for both new tests.

- [ ] **Step 7: Commit**

```bash
git add internal/media/media.go internal/media/repo.go internal/service/media_service.go internal/service/media_service_test.go
git commit -m "feat(media): hide sidecars from list by default

ListFilter.IncludeSidecars defaults to false; repo.List adds
AND paired_with_id IS NULL when false; service.MediaService.List
clamps caller-supplied true back to false so no HTTP route can
surface sidecars in lists. Internal callers (pairing pass,
backfill CLI, reconcile) may set true."
```

---

## Task 5: Repo helpers — `ListByOwnerDirectories`, `UpdatePairedWithID`, `GetSidecars`

The pairing pass needs to fetch existing rows in touched `(owner, directory)` keys (`ListByOwnerDirectories`), write `paired_with_id` updates (`UpdatePairedWithID`), and the HTTP detail handler needs to fetch a primary's sidecars sorted by `original_filename` (`GetSidecars`).

**Files:**
- Modify: `internal/media/repo.go`
- Modify: `internal/media/repo_test.go`

- [ ] **Step 1: Write failing tests for the three helpers**

Append to `internal/media/repo_test.go`:

```go
func TestRepoListByOwnerDirectories(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	owner := testutil.SeedOwner(t, d)
	other := testutil.SeedOwnerWithKey(t, d, "h", "u2", "sk-u2")

	a := newTestMediaWith(owner, "2024/a.jpg", "cs-a", "trip-paris/IMG_1.JPG")
	b := newTestMediaWith(owner, "2024/b.dng", "cs-b", "trip-paris/IMG_1.DNG")
	c := newTestMediaWith(owner, "2024/c.jpg", "cs-c", "trip-rome/IMG_2.JPG")
	d2 := newTestMediaWith(other, "2024/d.jpg", "cs-d", "trip-paris/IMG_3.JPG")
	for _, m := range []media.Media{a, b, c, d2} {
		r.NoError(repo.Insert(context.Background(), m))
	}

	// Caller asks for owner=owner, dirs={"trip-paris"} — must return
	// a + b only; not c (different dir) and not d2 (different owner).
	rows, err := repo.ListByOwnerDirectories(context.Background(), owner, []string{"trip-paris"})
	r.NoError(err)
	gotIDs := make([]string, 0, len(rows))
	for _, m := range rows {
		gotIDs = append(gotIDs, m.ID)
	}
	r.ElementsMatch([]string{a.ID, b.ID}, gotIDs)
}

func TestRepoUpdatePairedWithIDRoundTrips(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	owner := testutil.SeedOwner(t, d)
	primary := newTestMedia(owner, "2024/a.jpg", "cs-pri")
	sidecar := newTestMedia(owner, "2024/a.dng", "cs-sid")
	r.NoError(repo.Insert(context.Background(), primary))
	r.NoError(repo.Insert(context.Background(), sidecar))

	// Pair.
	r.NoError(repo.UpdatePairedWithID(context.Background(), sidecar.ID, &primary.ID))
	got, err := repo.GetByID(context.Background(), sidecar.ID)
	r.NoError(err)
	r.NotNil(got.PairedWithID)
	r.Equal(primary.ID, *got.PairedWithID)

	// Unpair (write nil).
	r.NoError(repo.UpdatePairedWithID(context.Background(), sidecar.ID, nil))
	got, err = repo.GetByID(context.Background(), sidecar.ID)
	r.NoError(err)
	r.Nil(got.PairedWithID)
}

func TestRepoGetSidecarsReturnsSortedByOriginalFilename(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	owner := testutil.SeedOwner(t, d)

	primary := newTestMedia(owner, "2024/a.jpg", "cs-pri")
	r.NoError(repo.Insert(context.Background(), primary))

	// Insert two sidecars with original_filenames sorted intentionally
	// out of order to confirm the helper sorts.
	for _, filename := range []string{"Z.dng", "A.dng"} {
		s := newTestMedia(owner, "2024/"+filename, "cs-"+filename)
		s.OriginalFilename = filename
		s.PairedWithID = &primary.ID
		r.NoError(repo.Insert(context.Background(), s))
	}

	sidecars, err := repo.GetSidecars(context.Background(), primary.ID)
	r.NoError(err)
	r.Len(sidecars, 2)
	r.Equal("A.dng", sidecars[0].OriginalFilename)
	r.Equal("Z.dng", sidecars[1].OriginalFilename)
}
```

`newTestMediaWith(owner, path, checksum, importSourcePath)` is a small variant of `newTestMedia` — add it to the test file alongside the existing helper if not present. `testutil.SeedOwner` already exists per F2.1; `SeedOwnerWithKey` likewise (used by other tests). If either helper is missing, lift the inline insert pattern from `media_service_test.go::TestMediaServiceListFiltersByCaller`.

- [ ] **Step 2: Run the tests to verify they fail**

```bash
go test ./internal/media/... -run 'TestRepoListByOwnerDirectories|TestRepoUpdatePairedWithIDRoundTrips|TestRepoGetSidecarsReturnsSortedByOriginalFilename' -count=1 -v
```

Expected: FAIL (method not defined on `*Repo`).

- [ ] **Step 3: Implement `ListByOwnerDirectories`**

Append to `internal/media/repo.go`:

```go
// ListByOwnerDirectories returns rows for owner whose
// dir(import_source_path) is in dirs. Used by the F2.2 pairing pass
// to fetch existing rows in directories touched by the just-imported
// batch. Empty dirs returns nil. Rows with empty import_source_path
// are excluded.
func (r *Repo) ListByOwnerDirectories(
	ctx context.Context,
	owner owners.Principal,
	dirs []string,
) ([]Media, error) {
	if len(dirs) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(dirs))
	args := make([]any, 0, len(dirs)+2)
	args = append(args, owner.Hub, owner.UserID)
	for i, dir := range dirs {
		placeholders[i] = "?"
		args = append(args, dir)
	}
	q := fmt.Sprintf(`
SELECT %s
FROM media
WHERE owner_hub = ? AND owner_user_id = ?
  AND import_source_path != ''
  AND fb_dirname(import_source_path) IN (%s)
`, mediaProjection, strings.Join(placeholders, ","))
	rows, err := r.read.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list by owner directories: %w", err)
	}
	defer rows.Close()
	out := make([]Media, 0)
	for rows.Next() {
		m, err := scanMedia(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
```

This requires a SQLite scalar function `fb_dirname` registered when the DB opens — modernc.org/sqlite supports user-defined functions via `sqlite.MustRegisterFunction` (or similar). The cleanest alternative if scalar registration is fragile in the project's wrapper: select all rows for owner with non-empty `import_source_path` and filter directories in Go.

The Go-side filter is acceptable performance-wise because `media_owner_import_source_path_idx` keeps the SELECT scoped to the owner's index range. Use the Go-filter approach: replace `fb_dirname(import_source_path) IN (...)` with `import_source_path LIKE ?` for each dir (with the trailing `/`), or read all owner+non-empty rows and filter by `filepath.Dir(m.ImportSourcePath)` in the Go loop.

If the Go-filter is chosen, the SQL becomes:

```go
q := fmt.Sprintf(`
SELECT %s
FROM media
WHERE owner_hub = ? AND owner_user_id = ?
  AND import_source_path != ''
`, mediaProjection)
```

Then in the loop, before `out = append(out, m)`:

```go
if !dirSet[filepath.Dir(m.ImportSourcePath)] {
    continue
}
```

with `dirSet := make(map[string]bool, len(dirs)); for _, d := range dirs { dirSet[d] = true }`. The constant `mediaProjection` is the existing column list used by other read methods (search the file).

Pick the Go-filter path for portability.

- [ ] **Step 4: Implement `UpdatePairedWithID`**

Append:

```go
// UpdatePairedWithID writes the paired_with_id column for a single
// row. nil clears the FK to NULL; non-nil sets it to the primary's
// id. Caller is responsible for caller-scope (this is a repo-level
// helper called from the importer pairing pass + backfill CLI, both
// owner-scoped at higher layers).
func (r *Repo) UpdatePairedWithID(
	ctx context.Context,
	id string,
	primaryID *string,
) error {
	res, err := r.write.ExecContext(ctx,
		`UPDATE media SET paired_with_id = ? WHERE id = ?`,
		nullStringPtr(primaryID), id,
	)
	if err != nil {
		return fmt.Errorf("update paired_with_id: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("%w: media id=%s", errs.ErrNotFound, id)
	}
	return nil
}

// nullStringPtr converts a *string to a sql.Null* compatible value for
// SQLite drivers that don't accept *string directly as nullable text.
func nullStringPtr(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}
```

If the file already has a similar nullable helper (search for `sql.NullString`), reuse it instead.

- [ ] **Step 5: Implement `GetSidecars`**

Append:

```go
// GetSidecars returns rows whose paired_with_id == primaryID, sorted
// by original_filename ascending. Used by the HTTP detail handler to
// embed sidecars in a primary's DTO.
func (r *Repo) GetSidecars(
	ctx context.Context,
	primaryID string,
) ([]Media, error) {
	q := fmt.Sprintf(`
SELECT %s
FROM media
WHERE paired_with_id = ?
ORDER BY COALESCE(original_filename, '') ASC, id ASC
`, mediaProjection)
	rows, err := r.read.QueryContext(ctx, q, primaryID)
	if err != nil {
		return nil, fmt.Errorf("get sidecars: %w", err)
	}
	defer rows.Close()
	out := make([]Media, 0)
	for rows.Next() {
		m, err := scanMedia(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
```

The `, id ASC` tiebreaker keeps ordering deterministic when two sidecars somehow share a basename.

- [ ] **Step 6: Run the tests to verify they pass**

```bash
go test ./internal/media/... -count=1
```

Expected: PASS for all three new tests + existing repo tests.

- [ ] **Step 7: Commit**

```bash
git add internal/media/repo.go internal/media/repo_test.go
git commit -m "feat(media): add ListByOwnerDirectories, UpdatePairedWithID, GetSidecars

ListByOwnerDirectories scopes a query to (owner, dir(import_source_path))
keys for the F2.2 pairing pass. UpdatePairedWithID writes the FK or
clears to NULL. GetSidecars returns a primary's children sorted by
original_filename for the detail-DTO embed."
```

---

## Task 6: `original_filename` semantics flip — `c.Path` → `filepath.Base(c.Path)`

The pairing logic and the new Files dl row both want the basename. Audited callers in the spec §4.2 don't depend on the absolute-path semantics, but fixtures need updates.

**Files:**
- Modify: `internal/ingest/importer.go::buildMediaRow` (line 304)
- Modify: `internal/ingest/importer_test.go`
- Modify: `internal/cli/import_test.go`

- [ ] **Step 1: Grep for any other consumer relying on the absolute path**

```bash
rg -n 'OriginalFilename' internal/ frontend/src/ | grep -v _test.go
```

Read each non-test hit. Per spec §4.2, expected hits are: `internal/ingest/importer.go`, `internal/httpapi/media.go::toMediaDTO` (passes through), `frontend/src/lib/media/mediaStore.svelte.ts` (already expects basename for display). Anything else: stop and re-evaluate.

- [ ] **Step 2: Edit `buildMediaRow`**

In `internal/ingest/importer.go` at the `OriginalFilename` line in `buildMediaRow`, change:

```go
		OriginalFilename: c.Path,
```

to:

```go
		OriginalFilename: filepath.Base(c.Path),
```

Confirm `path/filepath` is already imported at the top of the file; if not, add `"path/filepath"` to the import block (the file likely already imports it for the path-resolution helpers below).

- [ ] **Step 3: Update fixtures in `internal/ingest/importer_test.go`**

Search the test file for assertions or expectations on `OriginalFilename`:

```bash
rg -n 'OriginalFilename' internal/ingest/importer_test.go
```

For each, change the expected value from the absolute path string to `filepath.Base(...)` of that path. Concretely, expectations of the form `Equal(t, "/tmp/T/...", row.OriginalFilename)` become `Equal(t, "IMG_1234.JPG", row.OriginalFilename)`.

- [ ] **Step 4: Update fixtures in `internal/cli/import_test.go`**

Same pattern:

```bash
rg -n 'OriginalFilename' internal/cli/import_test.go
```

Update each expectation to the basename.

- [ ] **Step 5: Run the importer + cli test suites**

```bash
go test ./internal/ingest/... ./internal/cli/... -count=1
```

Expected: PASS. If anything still fails, the assertion was on a path the previous step missed; correct it.

- [ ] **Step 6: Commit**

```bash
git add internal/ingest/importer.go internal/ingest/importer_test.go internal/cli/import_test.go
git commit -m "refactor(ingest): flip OriginalFilename to basename

buildMediaRow now writes filepath.Base(Candidate.Path) instead of
the absolute source path. Pairing logic + the F2.2 Files dl row
both use original_filename for display, and the column finally
means what its name says. Audited callers in spec §4.2 don't
depend on the absolute-path semantics; fixtures updated."
```

---

## Task 7: Pair classifier + pure `pair.Compute`

The pairing core lives in `internal/ingest/pair.go` alongside `discover.go`. The classifier maps mime to `JPEG | RAW | Other`; `Compute` is the pure function consumed by both the importer pass and the CLI backfill. TDD with idempotency, commutativity, and ambiguity-transition tests.

**Files:**
- Create: `internal/ingest/pair.go`
- Create: `internal/ingest/pair_test.go`

- [ ] **Step 1: Write failing tests for `PairClass` classifier**

Create `internal/ingest/pair_test.go`:

```go
package ingest_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/ingest"
)

func TestPairClassFromMime(t *testing.T) {
	r := require.New(t)
	r.Equal(ingest.PairClassJPEG, ingest.PairClassFromMime("image/jpeg"))
	r.Equal(ingest.PairClassRAW, ingest.PairClassFromMime("image/x-sony-arw"))
	r.Equal(ingest.PairClassRAW, ingest.PairClassFromMime("image/x-fuji-raf"))
	r.Equal(ingest.PairClassRAW, ingest.PairClassFromMime("image/x-adobe-dng"))
	r.Equal(ingest.PairClassRAW, ingest.PairClassFromMime("image/x-canon-cr2"))
	r.Equal(ingest.PairClassRAW, ingest.PairClassFromMime("image/x-nikon-nef"))
	r.Equal(ingest.PairClassOther, ingest.PairClassFromMime("image/png"))
	r.Equal(ingest.PairClassOther, ingest.PairClassFromMime("video/mp4"))
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
go test ./internal/ingest/... -run TestPairClassFromMime -count=1 -v
```

Expected: FAIL — `ingest.PairClass*` and `PairClassFromMime` not defined.

- [ ] **Step 3: Create `internal/ingest/pair.go` with the classifier**

```go
// Package ingest — pair.go holds the F2.2 pairing classifier and the
// pure Compute function. The classifier maps mime to the JPEG / RAW /
// Other extension class used by §5.1 of the design doc. Compute is
// the deterministic, idempotent pure function consumed by both the
// importer's post-import barrier pass and the fotobank pair backfill
// CLI.
package ingest

import (
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/text/unicode/norm"
)

// PairClass enumerates the pairing role of a row's mime type.
type PairClass int

const (
	PairClassOther PairClass = iota
	PairClassJPEG
	PairClassRAW
)

// PairClassFromMime maps a media mime to its pair class. Unknown
// mimes are PairClassOther — Compute will skip them.
func PairClassFromMime(mime string) PairClass {
	switch mime {
	case "image/jpeg":
		return PairClassJPEG
	case "image/x-sony-arw",
		"image/x-fuji-raf",
		"image/x-adobe-dng",
		"image/x-canon-cr2",
		"image/x-nikon-nef":
		return PairClassRAW
	}
	return PairClassOther
}
```

- [ ] **Step 4: Run the classifier test to verify it passes**

```bash
go test ./internal/ingest/... -run TestPairClassFromMime -count=1 -v
```

Expected: PASS.

- [ ] **Step 5: Write failing tests for `pair.Compute`**

Extend the import block in `internal/ingest/pair_test.go` so it now reads:

```go
import (
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/ingest"
)
```

Then append the new tests + helper:

```go
type byID []ingest.PairUpdate

func (a byID) Len() int           { return len(a) }
func (a byID) Less(i, j int) bool { return a[i].ID < a[j].ID }
func (a byID) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }

func cand(id, dir, base, mime string, current *string) ingest.PairCandidate {
	return ingest.PairCandidate{
		ID:                id,
		ImportSourcePath:  filepath.Join(dir, base),
		Class:             ingest.PairClassFromMime(mime),
		MimeType:          mime,
		CurrentPairedWith: current,
	}
}

func TestPairComputeBasicOneJPEGOneRAW(t *testing.T) {
	r := require.New(t)
	rows := []ingest.PairCandidate{
		cand("p", "trip", "IMG_1.JPG", "image/jpeg", nil),
		cand("s", "trip", "IMG_1.DNG", "image/x-adobe-dng", nil),
	}
	updates := ingest.Compute(rows)
	r.Len(updates, 1)
	r.Equal("s", updates[0].ID)
	r.NotNil(updates[0].PairedWithID)
	r.Equal("p", *updates[0].PairedWithID)
}

func TestPairComputeIdempotency(t *testing.T) {
	r := require.New(t)
	primary := "p"
	rows := []ingest.PairCandidate{
		cand("p", "trip", "IMG_1.JPG", "image/jpeg", nil),
		cand("s", "trip", "IMG_1.DNG", "image/x-adobe-dng", &primary),
	}
	// Sidecar already paired correctly — Compute returns no updates.
	updates := ingest.Compute(rows)
	r.Empty(updates)
}

func TestPairComputeCommutativity(t *testing.T) {
	r := require.New(t)
	rowsA := []ingest.PairCandidate{
		cand("p", "trip", "IMG_1.JPG", "image/jpeg", nil),
		cand("s1", "trip", "IMG_1.DNG", "image/x-adobe-dng", nil),
		cand("s2", "trip", "IMG_1.ARW", "image/x-sony-arw", nil),
	}
	rowsB := []ingest.PairCandidate{
		cand("s2", "trip", "IMG_1.ARW", "image/x-sony-arw", nil),
		cand("p", "trip", "IMG_1.JPG", "image/jpeg", nil),
		cand("s1", "trip", "IMG_1.DNG", "image/x-adobe-dng", nil),
	}
	updatesA := ingest.Compute(rowsA)
	updatesB := ingest.Compute(rowsB)
	sort.Sort(byID(updatesA))
	sort.Sort(byID(updatesB))
	r.Equal(updatesA, updatesB)
}

func TestPairComputeOneJPEGTwoRAWPairsAll(t *testing.T) {
	r := require.New(t)
	rows := []ingest.PairCandidate{
		cand("p", "trip", "IMG_1.JPG", "image/jpeg", nil),
		cand("s1", "trip", "IMG_1.DNG", "image/x-adobe-dng", nil),
		cand("s2", "trip", "IMG_1.ARW", "image/x-sony-arw", nil),
	}
	updates := ingest.Compute(rows)
	sort.Sort(byID(updates))
	r.Len(updates, 2)
	for _, u := range updates {
		r.NotNil(u.PairedWithID)
		r.Equal("p", *u.PairedWithID)
	}
}

func TestPairComputeAmbiguousTwoJPEGsLeaveRAWUnpaired(t *testing.T) {
	r := require.New(t)
	rows := []ingest.PairCandidate{
		cand("p1", "trip", "IMG_1.JPG", "image/jpeg", nil),
		cand("p2", "trip", "IMG_1.JPEG", "image/jpeg", nil),
		cand("s", "trip", "IMG_1.DNG", "image/x-adobe-dng", nil),
	}
	updates := ingest.Compute(rows)
	// JPEGs unchanged (they can't pair to each other). RAW must be
	// emitted as PairedWithID = nil so any pre-existing pair is cleared.
	r.Len(updates, 1)
	r.Equal("s", updates[0].ID)
	r.Nil(updates[0].PairedWithID)
}

func TestPairComputeAmbiguityFlipsExistingPairToNull(t *testing.T) {
	r := require.New(t)
	primary := "p1"
	rows := []ingest.PairCandidate{
		cand("p1", "trip", "IMG_1.JPG", "image/jpeg", nil),
		cand("p2", "trip", "IMG_1.JPEG", "image/jpeg", nil),
		// Sidecar was already paired to p1. Adding p2 makes the
		// directory ambiguous; Compute must emit s → NULL.
		cand("s", "trip", "IMG_1.DNG", "image/x-adobe-dng", &primary),
	}
	updates := ingest.Compute(rows)
	r.Len(updates, 1)
	r.Equal("s", updates[0].ID)
	r.Nil(updates[0].PairedWithID)
}

func TestPairComputeCaseInsensitiveStem(t *testing.T) {
	r := require.New(t)
	rows := []ingest.PairCandidate{
		cand("p", "trip", "img_1.jpg", "image/jpeg", nil),
		cand("s", "trip", "IMG_1.DNG", "image/x-adobe-dng", nil),
	}
	updates := ingest.Compute(rows)
	r.Len(updates, 1)
	r.Equal("s", updates[0].ID)
	r.NotNil(updates[0].PairedWithID)
	r.Equal("p", *updates[0].PairedWithID)
}

func TestPairComputeIgnoresEmptySourcePath(t *testing.T) {
	r := require.New(t)
	rows := []ingest.PairCandidate{
		{ID: "x", ImportSourcePath: "", Class: ingest.PairClassJPEG, MimeType: "image/jpeg"},
		{ID: "y", ImportSourcePath: "", Class: ingest.PairClassRAW, MimeType: "image/x-adobe-dng"},
	}
	updates := ingest.Compute(rows)
	r.Empty(updates)
}

func TestPairComputeNFCNormalizesDirectory(t *testing.T) {
	r := require.New(t)
	// "café" in NFC vs NFD. Pairing must not split the directory by
	// normalization form — both rows are same dir, same stem.
	const dirNFC = "café"   // é precomposed
	const dirNFD = "café" // e + combining acute
	rows := []ingest.PairCandidate{
		cand("p", dirNFC, "IMG_1.JPG", "image/jpeg", nil),
		cand("s", dirNFD, "IMG_1.DNG", "image/x-adobe-dng", nil),
	}
	updates := ingest.Compute(rows)
	r.Len(updates, 1)
	r.Equal("s", updates[0].ID)
	r.NotNil(updates[0].PairedWithID)
	r.Equal("p", *updates[0].PairedWithID)
}
```

- [ ] **Step 6: Run the new tests to verify they fail**

```bash
go test ./internal/ingest/... -run 'TestPairCompute' -count=1 -v
```

Expected: FAIL — `ingest.PairCandidate`, `PairUpdate`, `Compute` not defined.

- [ ] **Step 7: Implement `PairCandidate`, `PairUpdate`, and `Compute`**

Append to `internal/ingest/pair.go`:

```go
// PairCandidate is the input row for Compute. The pairing pass
// projects media rows down to this minimal shape so the pure
// function has no dependency on internal/media.
type PairCandidate struct {
	ID                string
	ImportSourcePath  string
	Class             PairClass
	MimeType          string
	// CurrentPairedWith is the row's existing paired_with_id. nil
	// means NULL. Compute uses this to emit only the updates needed
	// to reach the desired state.
	CurrentPairedWith *string
}

// PairUpdate describes a single paired_with_id mutation. nil
// PairedWithID means SET paired_with_id = NULL.
type PairUpdate struct {
	ID           string
	PairedWithID *string
}

// Compute returns the deterministic set of paired_with_id updates
// needed to bring rows into the F2.2 pairing contract (§5). The
// function is idempotent and commutative; running it twice on the
// same row set yields identical output, and reordering the input
// does not change the output.
//
// Rows with empty ImportSourcePath are skipped. Rows whose Class is
// PairClassOther never pair. Updates are sorted by ID so callers
// observe a stable order regardless of iteration nondeterminism.
func Compute(rows []PairCandidate) []PairUpdate {
	type bucketKey struct {
		dir  string
		stem string
	}
	type bucket struct {
		jpegs []PairCandidate
		raws  []PairCandidate
	}
	buckets := make(map[bucketKey]*bucket)
	for _, row := range rows {
		if row.ImportSourcePath == "" {
			continue
		}
		if row.Class != PairClassJPEG && row.Class != PairClassRAW {
			continue
		}
		dir := norm.NFC.String(filepath.Dir(row.ImportSourcePath))
		stem := strings.ToLower(strings.TrimSuffix(
			filepath.Base(row.ImportSourcePath),
			filepath.Ext(row.ImportSourcePath),
		))
		stem = norm.NFC.String(stem)
		k := bucketKey{dir: dir, stem: stem}
		b := buckets[k]
		if b == nil {
			b = &bucket{}
			buckets[k] = b
		}
		switch row.Class {
		case PairClassJPEG:
			b.jpegs = append(b.jpegs, row)
		case PairClassRAW:
			b.raws = append(b.raws, row)
		}
	}

	desired := make(map[string]*string, len(rows))
	for _, b := range buckets {
		switch len(b.jpegs) {
		case 0:
			// No JPEG primary in the bucket — RAWs stay unpaired.
			for _, raw := range b.raws {
				desired[raw.ID] = nil
			}
		case 1:
			primaryID := b.jpegs[0].ID
			for _, raw := range b.raws {
				p := primaryID
				desired[raw.ID] = &p
			}
		default:
			// Ambiguous: multiple JPEGs share a stem. RAWs forced to NULL.
			for _, raw := range b.raws {
				desired[raw.ID] = nil
			}
		}
	}

	out := make([]PairUpdate, 0, len(desired))
	for _, row := range rows {
		want, hasDesired := desired[row.ID]
		if !hasDesired {
			continue
		}
		if pairEqual(row.CurrentPairedWith, want) {
			continue
		}
		out = append(out, PairUpdate{ID: row.ID, PairedWithID: want})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func pairEqual(a, b *string) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}
```

- [ ] **Step 8: Run all pair tests to verify they pass**

```bash
go test ./internal/ingest/... -run 'TestPair' -count=1 -v
```

Expected: PASS for all eight + the classifier test.

- [ ] **Step 9: Commit**

```bash
git add internal/ingest/pair.go internal/ingest/pair_test.go
git commit -m "feat(ingest): pure Compute pairing function + classifier

PairClass + PairClassFromMime map mime → JPEG/RAW/Other.
Compute([]PairCandidate) []PairUpdate is the deterministic,
idempotent, commutative pure function consumed by the importer
post-barrier pass and the fotobank pair backfill CLI.

Tests cover idempotency, commutativity, ambiguous-multi-JPEG
clearing existing pairs, case-insensitive stems, NFC/NFD
directory normalization, and empty-import_source_path skipping."
```

---

## Task 8: Importer post-barrier pass + `import_source_path` capture

Capture the relative source path on row build, then run the pairing pass after the worker `WaitGroup` returns. The pass groups by touched `(owner, dir)` keys, queries existing rows in those dirs via `ListByOwnerDirectories`, computes updates via `Compute`, and applies them via `UpdatePairedWithID`.

**Files:**
- Modify: `internal/ingest/importer.go::buildMediaRow` and `ImportDirectory`
- Modify: `internal/ingest/importer_test.go`

- [ ] **Step 1: Capture `ImportSourcePath` in `buildMediaRow`**

The candidate's source path is absolute (`/tmp/import-root/2024-Paris/IMG_1234.JPG`). Convert to root-relative by stripping the import-root prefix. The importer already knows the root via `ImportDirectory`'s argument; thread it down to `buildMediaRow` as a new parameter. Edit `buildMediaRow` signature:

```go
func buildMediaRow(c Candidate, owner owners.Principal, key, checksum string, size int64, meta exifread.Metadata, importedAt time.Time, places PlaceResolver, sourceRoot string) media.Media {
```

In the body, set `ImportSourcePath`:

```go
	rel, err := filepath.Rel(sourceRoot, c.Path)
	if err != nil || strings.HasPrefix(rel, "..") {
		rel = c.Path // fallback: keep absolute, pairing will still work via the same dir/stem if both rows fall through this branch
	}
	rel = filepath.ToSlash(rel)
	m := media.Media{
		// ... existing fields ...
		ImportSourcePath: rel,
		// ... rest ...
	}
```

`filepath.ToSlash` keeps the path stable across OSes. Audit callers of `buildMediaRow` and pass `sourceRoot` from `ImportDirectory` (which receives it as its `source` argument).

- [ ] **Step 2: Add the pairing pass to `ImportDirectory`**

After the existing `wg.Wait()` (or whatever the worker barrier is — search for `sync.WaitGroup` or `Wait()` in `importer.go`), but before `ImportDirectory` returns its result, insert:

```go
	if err := imp.runPairingPass(ctx, opts.Owner, importedIDs); err != nil {
		// Pairing failures don't fail the import — the rows are
		// already in. Log via the existing failure-collection path
		// so the operator sees them in the result struct.
		res.Failures = append(res.Failures,
			fmt.Sprintf("pair pass: %v", err))
	}
```

Define `imp.runPairingPass`:

```go
// runPairingPass runs the F2.2 post-barrier pairing pass. It computes
// the (owner, dir) keys touched by the just-imported batch, fetches
// the existing rows in those dirs (so a JPEG imported today pairs
// with a RAW imported last week and vice versa), runs Compute over
// the union, and applies PairUpdate writes.
func (imp *Importer) runPairingPass(
	ctx context.Context,
	owner owners.Principal,
	importedIDs []string,
) error {
	if len(importedIDs) == 0 {
		return nil
	}
	imported := make([]media.Media, 0, len(importedIDs))
	for _, id := range importedIDs {
		m, err := imp.repo.GetByID(ctx, id)
		if err != nil {
			return fmt.Errorf("get imported row %s: %w", id, err)
		}
		imported = append(imported, m)
	}
	dirSet := make(map[string]bool)
	for _, m := range imported {
		if m.ImportSourcePath == "" {
			continue
		}
		dirSet[filepath.Dir(m.ImportSourcePath)] = true
	}
	dirs := make([]string, 0, len(dirSet))
	for d := range dirSet {
		dirs = append(dirs, d)
	}
	if len(dirs) == 0 {
		return nil
	}
	existing, err := imp.repo.ListByOwnerDirectories(ctx, owner, dirs)
	if err != nil {
		return err
	}
	// Combine, deduping by id (an imported row is also returned by
	// ListByOwnerDirectories).
	seen := make(map[string]bool, len(imported)+len(existing))
	candidates := make([]PairCandidate, 0, len(imported)+len(existing))
	add := func(m media.Media) {
		if seen[m.ID] {
			return
		}
		seen[m.ID] = true
		candidates = append(candidates, PairCandidate{
			ID:                m.ID,
			ImportSourcePath:  m.ImportSourcePath,
			Class:             PairClassFromMime(m.MimeType),
			MimeType:          m.MimeType,
			CurrentPairedWith: m.PairedWithID,
		})
	}
	for _, m := range imported {
		add(m)
	}
	for _, m := range existing {
		add(m)
	}
	updates := Compute(candidates)
	for _, u := range updates {
		if err := imp.repo.UpdatePairedWithID(ctx, u.ID, u.PairedWithID); err != nil {
			return fmt.Errorf("apply pair update for %s: %w", u.ID, err)
		}
	}
	return nil
}
```

`importedIDs` must be collected in the worker loop. Find the existing place where successful inserts increment `res.Imported` and append the row id to a slice (declare `var importedIDs []string` near the top of `ImportDirectory`, append from inside the worker after a successful Insert, protect with the existing mutex if any).

- [ ] **Step 3: Write a failing integration test for bidirectional pairing**

Append to `internal/ingest/importer_test.go`:

```go
func TestImporterPairsJPEGWithExistingRAW(t *testing.T) {
	r := require.New(t)
	// Stage 1: import only the RAW.
	srcRoot, _ := filepath.Abs(t.TempDir())
	rawDir := filepath.Join(srcRoot, "trip-paris")
	r.NoError(os.MkdirAll(rawDir, 0o755))
	rawPath := filepath.Join(rawDir, "IMG_1234.DNG")
	r.NoError(os.WriteFile(rawPath, fixtureDNGBytes(), 0o644))

	fx := newImporterFixture(t)
	res, err := fx.imp.ImportDirectory(context.Background(), srcRoot, ingest.Options{Owner: fx.owner, ConcurrentWorkers: 2})
	r.NoError(err)
	r.Equal(1, res.Imported)

	// Stage 2: drop the JPEG into the same directory and import again.
	jpegPath := filepath.Join(rawDir, "IMG_1234.JPG")
	r.NoError(os.WriteFile(jpegPath, fixtureJPEGBytes(), 0o644))
	res, err = fx.imp.ImportDirectory(context.Background(), srcRoot, ingest.Options{Owner: fx.owner, ConcurrentWorkers: 2})
	r.NoError(err)
	r.Equal(1, res.Imported) // The DNG is now a duplicate-by-checksum, ignored.

	// Both rows must exist; the DNG (imported in stage 1) should now
	// have paired_with_id pointing at the JPEG.
	rows, err := fx.repo.List(context.Background(), media.ListFilter{Owner: fx.owner, IncludeSidecars: true})
	r.NoError(err)
	r.Len(rows, 2)
	var jpeg, dng media.Media
	for _, m := range rows {
		switch m.OriginalFilename {
		case "IMG_1234.JPG":
			jpeg = m
		case "IMG_1234.DNG":
			dng = m
		}
	}
	r.Nil(jpeg.PairedWithID, "JPEG primary should not have paired_with_id")
	r.NotNil(dng.PairedWithID, "DNG sidecar should be paired")
	r.Equal(jpeg.ID, *dng.PairedWithID)
}
```

`newImporterFixture`, `fixtureDNGBytes`, `fixtureJPEGBytes` are existing helpers in `importer_test.go` per F2.1's pattern; if names differ, search the file. If `fixtureDNGBytes` doesn't exist, lift the embedded-JPEG TIFF fixture from `internal/thumb/raw_test.go` (which already constructs synthetic RAW bytes).

- [ ] **Step 4: Run the test to verify it fails**

```bash
go test ./internal/ingest/... -run TestImporterPairsJPEGWithExistingRAW -count=1 -v
```

Expected: FAIL until the importer pass implementation is in place.

- [ ] **Step 5: Run the full ingest suite to verify the pass works**

```bash
go test ./internal/ingest/... -count=1
```

Expected: PASS for the new test plus all existing ingest tests.

- [ ] **Step 6: Commit**

```bash
git add internal/ingest/importer.go internal/ingest/importer_test.go
git commit -m "feat(ingest): post-import pairing pass + import_source_path capture

buildMediaRow now records the root-relative source path so the
pairing pass and the backfill CLI can recompute pairs without
revisiting the source filesystem. After the worker WaitGroup
barrier, ImportDirectory runs runPairingPass: it groups touched
(owner, dir) keys, queries existing rows via ListByOwnerDirectories
(so a JPEG imported today pairs with a RAW imported last week and
vice versa), runs the pure Compute, and applies PairUpdate writes.

Pair-pass failures are non-fatal — they're collected in
res.Failures rather than aborting the import."
```

---

## Task 9: CLI `fotobank pair backfill`

Mirror the F2.1 `internal/cli/gps.go` shape: a `pair` command group with one `backfill` subcommand. Same flags (`--config`, `--since`, `--mode=full`, `--owner`, `--all-owners`), same lock semantics, same per-principal iteration.

**Files:**
- Create: `internal/cli/pair.go`
- Create: `internal/cli/pair_test.go`
- Modify: `internal/cli/root.go` — register `newPairCmd`

- [ ] **Step 1: Write a failing CLI happy-path test**

Create `internal/cli/pair_test.go`:

```go
package cli_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/cli"
	"github.com/wesm/fotobank/internal/db"
	"github.com/wesm/fotobank/internal/media"
	"github.com/wesm/fotobank/internal/owners"
)

func runPair(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	stdout, stderr := &strings.Builder{}, &strings.Builder{}
	code := cli.RunContext(context.Background(), append([]string{"pair"}, args...), stdout, stderr)
	return code, stdout.String(), stderr.String()
}

func TestPairBackfillRecomputesPairs(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "config.toml")
	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	r.NoError(os.Setenv("FOTOBANK_DB_PATH", dbPath))
	t.Cleanup(func() { _ = os.Unsetenv("FOTOBANK_DB_PATH") })
	writeBasicConfig(t, cfgPath, tmp)

	d, err := db.Open(dbPath)
	r.NoError(err)
	t.Cleanup(func() { _ = d.Close() })
	owner := owners.Principal{Hub: "h", UserID: "u"}
	seedOwner(t, d, owner)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())

	// Pre-pair-CLI state: a JPEG and a DNG sharing dir/stem with
	// paired_with_id NOT yet set (simulating rows that landed before
	// this codepath existed).
	primary := newCLIRow(owner, "img.jpg", "IMG_1.JPG", "trip/IMG_1.JPG", "image/jpeg", "cs-1")
	sidecar := newCLIRow(owner, "img.dng", "IMG_1.DNG", "trip/IMG_1.DNG", "image/x-adobe-dng", "cs-2")
	r.NoError(repo.Insert(context.Background(), primary))
	r.NoError(repo.Insert(context.Background(), sidecar))
	r.NoError(d.Close())

	code, stdout, stderr := runPair(t, "backfill", "--config", cfgPath)
	r.Equal(0, code, "stderr=%s", stderr)
	r.Contains(stdout, "pair backfill")

	d, err = db.Open(dbPath)
	r.NoError(err)
	repo = media.NewRepo(d.WriteDB(), d.ReadDB())
	got, err := repo.GetByID(context.Background(), sidecar.ID)
	r.NoError(err)
	r.NotNil(got.PairedWithID)
	r.Equal(primary.ID, *got.PairedWithID)
}

func TestPairBackfillRejectsBadMode(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "config.toml")
	writeBasicConfig(t, cfgPath, tmp)
	code, _, stderr := runPair(t, "backfill", "--config", cfgPath, "--mode", "nope")
	r.Equal(2, code)
	r.Contains(stderr, "--mode")
}

func TestPairBackfillRejectsOwnerAndAllOwners(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "config.toml")
	writeBasicConfig(t, cfgPath, tmp)
	code, _, stderr := runPair(t, "backfill", "--config", cfgPath, "--owner", "h:u", "--all-owners")
	r.Equal(2, code)
	r.Contains(stderr, "mutually exclusive")
}
```

`newCLIRow`, `writeBasicConfig`, `seedOwner` are existing helpers in `internal/cli/*_test.go`; the F2.1 `gps_test.go` is the closest analogue — copy its helper bodies into a new `pair_test_helpers.go` if duplicated cross-file usage is awkward.

- [ ] **Step 2: Run the tests to verify they fail**

```bash
go test ./internal/cli/... -run 'TestPairBackfill' -count=1 -v
```

Expected: FAIL — the `pair` subcommand doesn't exist yet, so cobra returns a usage error.

- [ ] **Step 3: Create `internal/cli/pair.go`**

```go
package cli

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/wesm/fotobank/internal/config"
	"github.com/wesm/fotobank/internal/db"
	"github.com/wesm/fotobank/internal/ingest"
	"github.com/wesm/fotobank/internal/media"
	"github.com/wesm/fotobank/internal/owners"
)

func newPairCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pair",
		Short: "Manage RAW + JPEG pairing",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			_ = cmd.Usage()
			return newUsageError("a subcommand is required")
		},
	}
	cmd.AddCommand(newPairBackfillCmd())
	return cmd
}

type pairBackfillOpts struct {
	cfgPath   string
	since     string
	sinceTime *time.Time
	mode      string
	owner     string
	allOwners bool
}

func newPairBackfillCmd() *cobra.Command {
	var opts pairBackfillOpts
	cmd := &cobra.Command{
		Use:   "backfill",
		Short: "Recompute paired_with_id by re-scanning import_source_path groupings",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runPairBackfill(cmd.Context(), &opts, cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	cmd.Flags().StringVar(&opts.cfgPath, "config", "",
		"path to config file (defaults to DefaultConfigPath)")
	cmd.Flags().StringVar(&opts.since, "since", "",
		"filter to recent rows; pair state recomputes against ALL rows in their dirs")
	cmd.Flags().StringVar(&opts.mode, "mode", "full",
		"only 'full' is supported (idempotent recompute-only)")
	cmd.Flags().StringVar(&opts.owner, "owner", "",
		"admin: backfill for a single principal in <hub>:<user> form")
	cmd.Flags().BoolVar(&opts.allOwners, "all-owners", false,
		"admin: backfill for every registered principal")
	return cmd
}

func validatePairBackfillOpts(opts *pairBackfillOpts) error {
	if opts.owner != "" && opts.allOwners {
		return newUsageError("--owner and --all-owners are mutually exclusive")
	}
	if opts.owner != "" {
		if _, err := parseOwner(opts.owner); err != nil {
			return err
		}
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
	if opts.mode != "full" {
		return newUsageError(
			"--mode must be 'full' in v1 (got %q)", opts.mode)
	}
	return nil
}

type pairTally struct {
	processed         int
	paired            int
	unchanged         int
	ambiguousUnpaired int
}

func (t pairTally) summary() string {
	return fmt.Sprintf(
		"pair backfill: processed=%d paired=%d unchanged=%d ambiguous_unpaired=%d",
		t.processed, t.paired, t.unchanged, t.ambiguousUnpaired)
}

func runPairBackfill(ctx context.Context, opts *pairBackfillOpts, stdout, stderr io.Writer) error {
	if err := validatePairBackfillOpts(opts); err != nil {
		return err
	}
	requireStub := opts.owner == "" && !opts.allOwners
	cfg, err := loadPairConfig(opts.cfgPath, requireStub)
	if err != nil {
		return err
	}
	d, err := openDB(cfg)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()

	lockPath := cfg.Imports.FileLockPath
	if lockPath == "" {
		lockPath = filepath.Join(cfg.NAS.Root, ".fotobank", "import.lock")
	}
	unlock, err := ingest.Acquire(ctx, lockPath, 0)
	if err != nil {
		return fmt.Errorf("acquire import lock: %w", err)
	}
	defer unlock()

	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	principals, err := selectPairPrincipals(ctx, d, cfg, opts)
	if err != nil {
		return err
	}
	tally := pairTally{}
	for _, p := range principals {
		if err := pairBackfillFor(ctx, repo, p, opts, &tally); err != nil {
			return err
		}
	}
	fmt.Fprintln(stdout, tally.summary())
	return nil
}

func loadPairConfig(cfgPath string, requireStub bool) (*config.Config, error) {
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
			"fotobank pair backfill requires identity.mode = stub (got %q)",
			cfg.Identity.Mode)
	}
	return cfg, nil
}

func selectPairPrincipals(
	ctx context.Context,
	d *db.DB,
	cfg *config.Config,
	opts *pairBackfillOpts,
) ([]owners.Principal, error) {
	switch {
	case opts.owner != "":
		p, err := parseOwner(opts.owner)
		if err != nil {
			return nil, err
		}
		return []owners.Principal{p}, nil
	case opts.allOwners:
		ownersRepo := owners.NewRepo(d.WriteDB(), d.ReadDB())
		list, err := ownersRepo.List(ctx)
		if err != nil {
			return nil, fmt.Errorf("list owners: %w", err)
		}
		out := make([]owners.Principal, 0, len(list))
		for _, o := range list {
			out = append(out, o.Principal)
		}
		return out, nil
	default:
		return []owners.Principal{
			{Hub: cfg.Identity.Stub.Hub, UserID: cfg.Identity.Stub.UserID},
		}, nil
	}
}

func pairBackfillFor(
	ctx context.Context,
	repo *media.Repo,
	owner owners.Principal,
	opts *pairBackfillOpts,
	tally *pairTally,
) error {
	allRows, err := repo.List(ctx, media.ListFilter{
		Owner:           owner,
		IncludeSidecars: true,
	})
	if err != nil {
		return fmt.Errorf("list rows for %s:%s: %w", owner.Hub, owner.UserID, err)
	}
	dirSet := make(map[string]bool)
	for _, m := range allRows {
		if m.ImportSourcePath == "" {
			continue
		}
		if opts.sinceTime != nil && m.ImportedAt.Before(*opts.sinceTime) {
			continue
		}
		dirSet[filepath.Dir(m.ImportSourcePath)] = true
	}
	dirs := make([]string, 0, len(dirSet))
	for d := range dirSet {
		dirs = append(dirs, d)
	}
	if len(dirs) == 0 {
		return nil
	}
	candidates := make([]ingest.PairCandidate, 0, len(allRows))
	for _, m := range allRows {
		if m.ImportSourcePath == "" {
			tally.unchanged++
			continue
		}
		if !dirSet[filepath.Dir(m.ImportSourcePath)] {
			continue
		}
		candidates = append(candidates, ingest.PairCandidate{
			ID:                m.ID,
			ImportSourcePath:  m.ImportSourcePath,
			Class:             ingest.PairClassFromMime(m.MimeType),
			MimeType:          m.MimeType,
			CurrentPairedWith: m.PairedWithID,
		})
		tally.processed++
	}
	updates := ingest.Compute(candidates)
	for _, u := range updates {
		if err := repo.UpdatePairedWithID(ctx, u.ID, u.PairedWithID); err != nil {
			return fmt.Errorf("update paired_with_id for %s: %w", u.ID, err)
		}
		if u.PairedWithID == nil {
			tally.ambiguousUnpaired++
		} else {
			tally.paired++
		}
	}
	return nil
}
```

- [ ] **Step 4: Wire `newPairCmd` into the root command**

In `internal/cli/root.go`, find where other subcommands are added (search for `cmd.AddCommand(newGPSCmd())`). Add `cmd.AddCommand(newPairCmd())` next to it.

- [ ] **Step 5: Run the CLI tests to verify they pass**

```bash
go test ./internal/cli/... -count=1
```

Expected: PASS for the new tests and no regression on existing CLI tests.

- [ ] **Step 6: Commit**

```bash
git add internal/cli/pair.go internal/cli/pair_test.go internal/cli/root.go
git commit -m "feat(cli): fotobank pair backfill subcommand

Mirrors fotobank gps backfill: --config / --since / --mode=full /
--owner / --all-owners. Runs the same idempotent ingest.Compute
function over rows scoped to the recent (--since) directories,
recomputing against ALL non-empty import_source_path rows in
those dirs so an older JPEG paired with a recent RAW (or vice
versa) is discovered.

Holds the import file lock to serialize against in-flight imports."
```

---

## Task 10: HTTP DTO additions + List filter + Detail endpoints + `make api-generate`

Add `paired_with_id`, `paired_with`, and `sidecars` to the media DTO. List handler keeps the default-hide behavior (already enforced server-side at the service layer); detail handler embeds sidecars on primary, paired_with summary on sidecar.

**Files:**
- Modify: `internal/httpapi/media.go`
- Modify: `internal/httpapi/media_test.go`
- Modify: `openapi.json`, `frontend/src/lib/api/generated/schema.ts` (regenerated)

- [ ] **Step 1: Write failing handler tests for the three new shapes**

Append to `internal/httpapi/media_test.go`:

```go
func TestMediaListHidesSidecars(t *testing.T) {
	// Sidecars must not appear in /api/v1/media list responses.
	r := require.New(t)
	fx := newHandlerFixture(t)
	primaryID := fx.insertMedia("primary.jpg", "image/jpeg", nil)
	pid := primaryID
	_ = fx.insertMedia("primary.dng", "image/x-adobe-dng", &pid)

	resp := fx.do(http.MethodGet, "/api/v1/media", nil)
	r.Equal(200, resp.Code)
	var body struct {
		Items []struct{ ID string `json:"id"` } `json:"items"`
	}
	r.NoError(json.NewDecoder(resp.Body).Decode(&body))
	ids := make([]string, 0, len(body.Items))
	for _, it := range body.Items {
		ids = append(ids, it.ID)
	}
	r.Equal([]string{primaryID}, ids)
}

func TestMediaDetailPrimaryEmbedsSidecars(t *testing.T) {
	r := require.New(t)
	fx := newHandlerFixture(t)
	primaryID := fx.insertMedia("a.jpg", "image/jpeg", nil)
	pid := primaryID
	dngID := fx.insertMedia("a.dng", "image/x-adobe-dng", &pid)

	resp := fx.do(http.MethodGet, "/api/v1/media/"+primaryID, nil)
	r.Equal(200, resp.Code)
	var body struct {
		ID       string `json:"id"`
		Sidecars []struct {
			ID            string `json:"id"`
			PairedWithID  string `json:"paired_with_id"`
		} `json:"sidecars"`
		PairedWithID *string `json:"paired_with_id,omitempty"`
	}
	r.NoError(json.NewDecoder(resp.Body).Decode(&body))
	r.Equal(primaryID, body.ID)
	r.Nil(body.PairedWithID)
	r.Len(body.Sidecars, 1)
	r.Equal(dngID, body.Sidecars[0].ID)
	r.Equal(primaryID, body.Sidecars[0].PairedWithID)
}

func TestMediaDetailSidecarReturnsPairedWithSummary(t *testing.T) {
	r := require.New(t)
	fx := newHandlerFixture(t)
	primaryID := fx.insertMedia("a.jpg", "image/jpeg", nil)
	pid := primaryID
	dngID := fx.insertMedia("a.dng", "image/x-adobe-dng", &pid)

	resp := fx.do(http.MethodGet, "/api/v1/media/"+dngID, nil)
	r.Equal(200, resp.Code)
	var body struct {
		ID           string `json:"id"`
		PairedWithID string `json:"paired_with_id"`
		PairedWith   *struct {
			ID               string `json:"id"`
			OriginalFilename string `json:"original_filename"`
		} `json:"paired_with"`
		Sidecars []any `json:"sidecars,omitempty"`
	}
	r.NoError(json.NewDecoder(resp.Body).Decode(&body))
	r.Equal(dngID, body.ID)
	r.Equal(primaryID, body.PairedWithID)
	r.NotNil(body.PairedWith)
	r.Equal(primaryID, body.PairedWith.ID)
	r.Empty(body.Sidecars, "sidecar's own DTO should not list further sidecars")
}
```

`newHandlerFixture` and `insertMedia` are existing helpers; if `insertMedia` doesn't yet take a `*pairedWithID` argument, extend it to do so.

- [ ] **Step 2: Run the tests to verify they fail**

```bash
go test ./internal/httpapi/... -run 'TestMedia(List|Detail)' -count=1 -v
```

Expected: FAIL — DTO fields missing, detail handler doesn't issue the extra queries.

- [ ] **Step 3: Update `mediaDTO`**

In `internal/httpapi/media.go`, find the existing `type mediaDTO struct` (search for `mediaDTO`) and add at the end:

```go
	// F2.2 RAW + JPEG pairing.
	PairedWithID *string         `json:"paired_with_id,omitempty"`
	PairedWith   *pairSummaryDTO `json:"paired_with,omitempty"`
	Sidecars     []mediaDTO      `json:"sidecars,omitempty"`
```

And below the struct (before the next type or function), add:

```go
type pairSummaryDTO struct {
	ID               string `json:"id"`
	OriginalFilename string `json:"original_filename"`
}
```

- [ ] **Step 4: Update `toMediaDTO` to populate `PairedWithID`**

In `internal/httpapi/media.go::toMediaDTO`, add:

```go
	if m.PairedWithID != nil {
		id := *m.PairedWithID
		dto.PairedWithID = &id
	}
```

`Sidecars` and `PairedWith` are NOT populated here — only the detail handler populates them, to avoid recursive shapes on list endpoints.

- [ ] **Step 5: Update the detail handler to populate sidecars / paired_with**

Find the detail handler (search `Get /api/v1/media/{id}` registration; typical name is `getMedia` or `mediaDetail`). After the existing `mediaDTO` is built but before returning, add:

```go
	if dto.PairedWithID == nil {
		// Primary path: embed sidecars.
		sidecars, err := h.svc.GetSidecars(ctx, m.ID, caller)
		if err != nil {
			return nil, Translate(err)
		}
		dto.Sidecars = make([]mediaDTO, 0, len(sidecars))
		for _, s := range sidecars {
			child := toMediaDTO(s)
			// Sidecar's own paired_with summary points back to this primary.
			child.PairedWith = &pairSummaryDTO{
				ID:               m.ID,
				OriginalFilename: m.OriginalFilename,
			}
			// Children never recurse.
			child.Sidecars = nil
			dto.Sidecars = append(dto.Sidecars, child)
		}
	} else {
		// Sidecar path: fetch primary summary.
		primary, err := h.svc.Get(ctx, *m.PairedWithID, caller)
		if err == nil {
			dto.PairedWith = &pairSummaryDTO{
				ID:               primary.ID,
				OriginalFilename: primary.OriginalFilename,
			}
		}
		// If the primary lookup fails (e.g. ON DELETE SET NULL race),
		// PairedWith stays nil but the response still 200s.
	}
```

`MediaService.GetSidecars` is added to the service in this same task (next step).

- [ ] **Step 6: Add `MediaService.GetSidecars`**

In `internal/service/media_service.go`, append:

```go
// GetSidecars returns the sidecars of a primary owned by caller.
// The owner check goes through Get, which returns errs.ErrNotFound
// on caller mismatch. Returns nil + nil if the row has no sidecars.
func (s *MediaService) GetSidecars(
	ctx context.Context,
	primaryID string,
	caller owners.Principal,
) ([]media.Media, error) {
	if _, err := s.Get(ctx, primaryID, caller); err != nil {
		return nil, err
	}
	return s.repo.GetSidecars(ctx, primaryID)
}
```

- [ ] **Step 7: Regenerate the API surface**

```bash
make api-generate
```

This regenerates `openapi.json` and `frontend/src/lib/api/generated/schema.ts`. The new fields appear on `Media` in the schema.

- [ ] **Step 8: Run the handler tests to verify they pass**

```bash
go test ./internal/httpapi/... -count=1
```

Expected: PASS for the three new tests + no regression.

- [ ] **Step 9: Commit**

```bash
git add internal/httpapi/media.go internal/httpapi/media_test.go internal/service/media_service.go openapi.json frontend/src/lib/api/generated/schema.ts
git commit -m "feat(httpapi): surface RAW+JPEG pairing on media DTO

mediaDTO gains paired_with_id (always when set), paired_with
summary (sidecar detail), and sidecars list (primary detail).
List endpoint already filters sidecars at the SQL layer via the
service-side IncludeSidecars clamp.

Detail handler issues one extra query per response: GetSidecars
on a primary or Get(primaryID) on a sidecar. Embedded sidecar
DTOs never recurse.

api-generate regenerates openapi.json + the frontend schema."
```

---

## Task 11: Frontend `Media` type + `mediaStore.merge` guard + `toMedia`

Extend the frontend `Media` type with three optional fields, update the merge-guard's `Exclude` literal so the compile-time field check stays honest, and populate the new fields in `toMedia`.

**Files:**
- Modify: `frontend/src/lib/media/mediaStore.svelte.ts`
- Modify: `frontend/src/lib/media/mediaStore.test.ts`

- [ ] **Step 1: Write a failing store test**

Append to `frontend/src/lib/media/mediaStore.test.ts`:

```ts
import { describe, expect, it } from "vitest";
import { MediaStore, toMedia } from "./mediaStore.svelte";

describe("toMedia for paired rows", () => {
  it("populates paired_with_id, paired_with, and sidecars", () => {
    const raw = {
      id: "p",
      owner_hub: "h",
      owner_user_id: "u",
      type: "photo",
      mime_type: "image/jpeg",
      original_filename: "IMG_1.JPG",
      timestamp: null,
      imported_at: "2024-06-15T14:30:00Z",
      thumb_status: "ready",
      thumb_version: 1,
      sidecars: [
        {
          id: "s",
          owner_hub: "h",
          owner_user_id: "u",
          type: "photo",
          mime_type: "image/x-adobe-dng",
          original_filename: "IMG_1.DNG",
          timestamp: null,
          imported_at: "2024-06-15T14:30:00Z",
          thumb_status: "ready",
          thumb_version: 1,
          paired_with_id: "p",
          paired_with: { id: "p", original_filename: "IMG_1.JPG" },
        },
      ],
    };
    const m = toMedia(raw);
    expect(m.sidecars).toBeDefined();
    expect(m.sidecars).toHaveLength(1);
    expect(m.sidecars![0].paired_with_id).toBe("p");
    expect(m.sidecars![0].paired_with?.id).toBe("p");
  });
});
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
cd frontend && pnpm vitest run src/lib/media/mediaStore.test.ts
```

Expected: FAIL — `Property 'sidecars' does not exist on type 'Media'`.

- [ ] **Step 3: Extend the `Media` type**

In `frontend/src/lib/media/mediaStore.svelte.ts`, find the `type Media = { ... }` declaration and add at the end (before the closing brace):

```ts
  // F2.2 RAW + JPEG pairing.
  paired_with_id?: string;
  paired_with?: { id: string; original_filename: string };
  sidecars?: Media[];
```

The frontend's tsconfig has `exactOptionalPropertyTypes: true`, so callers must omit these properties when source data lacks them rather than assigning `undefined`.

- [ ] **Step 4: Update the `merge()` field-guard literal**

Search the file for the `Exclude<keyof Media, ...>` pattern (it's the compile-time guard that fails CI when a new Media field is added without updating the merge logic). Add the three new field names to the literal:

```ts
type _MergeFieldsCovered = Exclude<
  keyof Media,
  | "id"
  | /* ... existing fields ... */
  | "paired_with_id"
  | "paired_with"
  | "sidecars"
>;
```

The exact existing literal will need scanning; add the three names to whatever set is there.

- [ ] **Step 5: Update the `unchanged` predicate inside `merge()`**

Search for the `unchanged` helper (it returns `true` when an incoming row matches the stored row and merge can skip the dirty-flag flip). Add comparison for the three new fields:

```ts
function unchangedPair(a: Media, b: Media): boolean {
  return (
    (a.paired_with_id ?? null) === (b.paired_with_id ?? null) &&
    (a.paired_with?.id ?? null) === (b.paired_with?.id ?? null) &&
    sidecarIdsEqual(a.sidecars, b.sidecars)
  );
}

function sidecarIdsEqual(a?: Media[], b?: Media[]): boolean {
  if (!a && !b) return true;
  if (!a || !b) return false;
  if (a.length !== b.length) return false;
  for (let i = 0; i < a.length; i++) if (a[i].id !== b[i].id) return false;
  return true;
}
```

Then OR `unchangedPair(stored, incoming)` into the existing `unchanged` boolean expression.

- [ ] **Step 6: Update `toMedia`**

Find the `toMedia` function (typically near the bottom of the file). After populating the existing fields, add:

```ts
  if (raw.paired_with_id) m.paired_with_id = raw.paired_with_id;
  if (raw.paired_with) m.paired_with = { id: raw.paired_with.id, original_filename: raw.paired_with.original_filename };
  if (Array.isArray(raw.sidecars) && raw.sidecars.length > 0) {
    m.sidecars = raw.sidecars.map(toMedia);
  }
```

The conditional assignments are required by `exactOptionalPropertyTypes: true`.

- [ ] **Step 7: Run the test to verify it passes**

```bash
cd frontend && pnpm vitest run src/lib/media/mediaStore.test.ts
```

Expected: PASS.

- [ ] **Step 8: Type-check the whole frontend**

```bash
cd frontend && pnpm tsc --noEmit
```

Expected: PASS. The compile-time guard would have failed if the merge literal wasn't updated.

- [ ] **Step 9: Commit**

```bash
git add frontend/src/lib/media/mediaStore.svelte.ts frontend/src/lib/media/mediaStore.test.ts
git commit -m "feat(frontend): Media type extension for RAW+JPEG pairing

Three optional fields on Media: paired_with_id, paired_with,
sidecars. mediaStore.merge() guard and unchanged predicate
updated. toMedia populates the new fields conditionally to
satisfy exactOptionalPropertyTypes."
```

---

## Task 12: `MediaDetail.svelte` — primary Files row + sidecar direct page

Branch on `media.paired_with_id`: render the existing primary layout (with a new Files row when sidecars exist) for `null`, and the banner-plus-download layout when set.

**Files:**
- Modify: `frontend/src/routes/MediaDetail.svelte`

- [ ] **Step 1: Read the existing component**

```bash
cat frontend/src/routes/MediaDetail.svelte
```

Familiarize with the F2.1 dl structure (Captured / Location rows). The Files row is appended to the same dl; the sidecar layout replaces the photo+dl block entirely.

- [ ] **Step 2: Add the Files row on primaries with sidecars**

Inside the existing primary-render block, after the Location row's `</dl>` (or before it if appending inside the dl), add:

```svelte
{#if media.sidecars && media.sidecars.length > 0}
  <dt>Files</dt>
  <dd class="files">
    <a href="/api/v1/media/{media.id}/original" download>
      {media.original_filename}
    </a>
    {#each media.sidecars as sidecar (sidecar.id)}
      <br>
      <a href="/api/v1/media/{sidecar.id}/original" download>
        {sidecar.original_filename}
      </a>
    {/each}
  </dd>
{/if}
```

The primary appears first; sidecars follow in `original_filename` order (already sorted by the backend).

- [ ] **Step 3: Add the sidecar direct-page branch**

At the top of the render block, branch:

```svelte
{#if media.paired_with_id}
  <a href="/library" use:handleInternalLinkClick>← Back to Library</a>
  <h1>RAW sidecar for
    {#if media.paired_with}
      <a href="/media/{media.paired_with.id}" use:handleInternalLinkClick>
        {media.paired_with.original_filename}
      </a>
    {/if}
  </h1>
  <dl>
    <dt>File</dt><dd>{media.original_filename}</dd>
    <dt>Size</dt><dd>{formatBytes(media.size)}</dd>
    {#if media.timestamp}
      <dt>Captured</dt><dd>{formatTimestamp(media.timestamp)}</dd>
    {/if}
    {#if media.location_label}
      <dt>Location</dt><dd>{media.location_label}</dd>
    {/if}
  </dl>
  <a class="download" href="/api/v1/media/{media.id}/original" download>
    Download {media.original_filename}
  </a>
{:else}
  <!-- existing primary render block -->
{/if}
```

`handleInternalLinkClick` is already imported (per F2.0). `formatBytes` may already exist; if not, a small helper like `${(size / 1024 / 1024).toFixed(1)} MB` inline is fine.

- [ ] **Step 4: Type-check**

```bash
cd frontend && pnpm tsc --noEmit
```

Expected: PASS.

- [ ] **Step 5: Smoke-test in the browser (manual)**

```bash
make dev
```

Then in a browser, navigate to a primary's detail page (a JPEG) — confirm the Files row renders if sidecars exist. Then visit a sidecar's URL directly (`/media/<dng-id>`) and confirm the banner-plus-download layout renders. Ctrl-C the dev server when done.

- [ ] **Step 6: Commit**

```bash
git add frontend/src/routes/MediaDetail.svelte
git commit -m "feat(frontend): MediaDetail Files row + sidecar direct page

When the row has sidecars, append a Files row to the existing dl
listing primary + sidecar download links sorted by
original_filename. Direct visits to a sidecar URL render a
banner-plus-download layout: link to primary, info dl, download
button. No photo or thumb is rendered on the sidecar page —
file-detail / download, not a second viewer."
```

---

## Task 13: e2e tests for sidecars

Two new playwright tests, mirroring the F2.1 GPS-fixture pattern: a JPEG seeded with a paired DNG sidecar, and a direct visit to the sidecar URL.

**Files:**
- Modify: `frontend/tests/e2e/library.spec.ts`
- Modify: `cmd/e2e-server/...` (seed the new pair fixture; search for `gps-fixture-1` to find the existing seed file)

- [ ] **Step 1: Add fixture seeds in the e2e server**

Find where the e2e server seeds rows (search for `gps-fixture-1`). Add a primary + sidecar pair:

```go
seedMedia(d, owner, media.Media{
    ID:               "pair-fixture-primary",
    OriginalFilename: "IMG_1.JPG",
    MimeType:         "image/jpeg",
    Path:             "fixtures/IMG_1.JPG",
    ImportSourcePath: "fixtures/IMG_1.JPG",
    // ... existing required fields ...
})
primaryID := "pair-fixture-primary"
seedMedia(d, owner, media.Media{
    ID:               "pair-fixture-sidecar",
    OriginalFilename: "IMG_1.DNG",
    MimeType:         "image/x-adobe-dng",
    Path:             "fixtures/IMG_1.DNG",
    ImportSourcePath: "fixtures/IMG_1.DNG",
    PairedWithID:     &primaryID,
    // ... existing required fields ...
})
```

The exact `seedMedia` signature lives in the e2e-server source — match what the GPS fixture already does.

- [ ] **Step 2: Write failing playwright tests**

Append to `frontend/tests/e2e/library.spec.ts`:

```ts
test("MediaDetail primary shows Files row when sidecars exist", async ({ page }) => {
  await page.goto("/media/pair-fixture-primary");
  await expect(page.getByText("fotobank")).toBeVisible();
  await expect(page.getByText("Files")).toBeVisible();
  await expect(page.getByRole("link", { name: "IMG_1.JPG" })).toBeVisible();
  await expect(page.getByRole("link", { name: "IMG_1.DNG" })).toBeVisible();
});

test("MediaDetail sidecar direct page renders banner + download", async ({ page }) => {
  await page.goto("/media/pair-fixture-sidecar");
  await expect(page.getByText("fotobank")).toBeVisible();
  await expect(page.getByText(/RAW sidecar for/i)).toBeVisible();
  // Banner link to the primary uses SPA routing — no thumb, no img.
  await expect(page.getByRole("link", { name: "IMG_1.JPG" })).toBeVisible();
  await expect(
    page.getByRole("link", { name: /Download IMG_1\.DNG/i })
  ).toBeVisible();
  // Sidecar page intentionally renders no <img> from the photo bytes.
  await expect(page.locator("main img")).toHaveCount(0);
});

test("Library list omits sidecars", async ({ page }) => {
  await page.goto("/library");
  await expect(page.getByLabel("Photo pair-fixture-primary")).toBeVisible();
  await expect(page.getByLabel("Photo pair-fixture-sidecar")).toHaveCount(0);
});
```

- [ ] **Step 3: Run the e2e tests to verify they fail**

```bash
cd frontend && pnpm playwright test
```

Expected: FAIL until the seeds are in place AND the frontend changes from Task 12 are deployed via `make build && make api-generate`.

- [ ] **Step 4: Run the e2e tests to verify they pass**

```bash
cd frontend && pnpm playwright test
```

Expected: PASS for the three new tests + no regression on the existing 11.

- [ ] **Step 5: Commit**

```bash
git add frontend/tests/e2e/library.spec.ts cmd/e2e-server/<file>.go
git commit -m "test(e2e): RAW+JPEG sidecar visibility

Three playwright tests: primary shows Files row, direct sidecar
URL renders banner+download (no img element), library list omits
sidecars. e2e-server seeds pair-fixture-primary + sidecar pointing
at it via paired_with_id."
```

---

## Task 14: `AlbumService.AddMedia` sidecar rejection

Per spec §8.1: per-ID pre-flight loop adds a sidecar check returning `errs.ErrInvalidArgument` (HTTP 400).

**Files:**
- Modify: `internal/service/album_service.go:194-206`
- Modify: `internal/service/album_service_test.go`

- [ ] **Step 1: Write a failing service test**

Append to `internal/service/album_service_test.go`:

```go
func TestAlbumServiceAddMediaRejectsSidecar(t *testing.T) {
	r := require.New(t)
	fx := newAlbumServiceTest(t)
	ctx := context.Background()

	primary := insertTestMedia(t, fx.mediaRepo, fx.owner, "2024/p.jpg", "cs-pri")
	sidecar := insertTestMedia(t, fx.mediaRepo, fx.owner, "2024/p.dng", "cs-sid")
	r.NoError(fx.mediaRepo.UpdatePairedWithID(ctx, sidecar.ID, &primary.ID))

	a, err := fx.svc.Create(ctx, fx.owner, "Trip")
	r.NoError(err)

	// Primary alone is fine.
	_, _, err = fx.svc.AddMedia(ctx, a.ID, []string{primary.ID}, fx.owner)
	r.NoError(err)

	// Sidecar must be rejected with ErrInvalidArgument (HTTP 400).
	_, _, err = fx.svc.AddMedia(ctx, a.ID, []string{sidecar.ID}, fx.owner)
	r.ErrorIs(err, errs.ErrInvalidArgument)
}
```

`newAlbumServiceTest` is the existing fixture; verify its name in `album_service_test.go`.

- [ ] **Step 2: Run the test to verify it fails**

```bash
go test ./internal/service/... -run TestAlbumServiceAddMediaRejectsSidecar -count=1 -v
```

Expected: FAIL — sidecar currently inserts without complaint.

- [ ] **Step 3: Add the sidecar check to `AlbumService.AddMedia`**

In `internal/service/album_service.go`, inside the per-ID loop (currently at lines 194-206), add immediately after the existing `m.Owner != caller` check:

```go
		if m.PairedWithID != nil {
			return 0, 0, fmt.Errorf(
				"%w: media id=%s is a sidecar; albums reference primaries only",
				errs.ErrInvalidArgument, mid,
			)
		}
```

- [ ] **Step 4: Run the test to verify it passes**

```bash
go test ./internal/service/... -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/service/album_service.go internal/service/album_service_test.go
git commit -m "feat(albums): reject sidecars in AddMedia

Albums reference primaries only. AddMedia's per-ID pre-flight
loop now rejects rows with paired_with_id != nil with
errs.ErrInvalidArgument (HTTP 400 via the existing translator).
The error message names the sidecar id and the directive
'albums reference primaries only'."
```

---

## Task 15: `ShareService.Create` sidecar rejection (media_set)

Per spec §8.2: per-ID pre-flight loop adds a sidecar check returning `errs.ErrInvalidArgument`.

**Files:**
- Modify: `internal/service/share_service.go:107-115`
- Modify: `internal/service/share_service_test.go`

- [ ] **Step 1: Write a failing service test**

Append to `internal/service/share_service_test.go`:

```go
func TestShareServiceCreateRejectsSidecarInMediaSet(t *testing.T) {
	r := require.New(t)
	fx := newShareServiceTest(t)
	ctx := context.Background()

	primary := insertTestMedia(t, fx.mediaRepo, fx.owner, "2024/p.jpg", "cs-pri")
	sidecar := insertTestMedia(t, fx.mediaRepo, fx.owner, "2024/p.dng", "cs-sid")
	r.NoError(fx.mediaRepo.UpdatePairedWithID(ctx, sidecar.ID, &primary.ID))

	// Primary alone — fine.
	_, err := fx.svc.Create(ctx, service.CreateShareRequest{
		Grantee:    owners.Principal{Hub: "h", UserID: "g"},
		TargetType: share.TargetMediaSet,
		MediaIDs:   []string{primary.ID},
	}, fx.owner)
	r.NoError(err)

	// Sidecar — rejected.
	_, err = fx.svc.Create(ctx, service.CreateShareRequest{
		Grantee:    owners.Principal{Hub: "h", UserID: "g"},
		TargetType: share.TargetMediaSet,
		MediaIDs:   []string{sidecar.ID},
	}, fx.owner)
	r.ErrorIs(err, errs.ErrInvalidArgument)
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
go test ./internal/service/... -run TestShareServiceCreateRejectsSidecar -count=1 -v
```

Expected: FAIL.

- [ ] **Step 3: Add the sidecar check to `ShareService.Create`**

In `internal/service/share_service.go`, in the per-ID loop at lines 107-115, immediately after the existing `m.Owner != caller` check (line 112-114), add:

```go
			if m.PairedWithID != nil {
				return share.Scope{}, fmt.Errorf(
					"%w: media id=%s is a sidecar; shares reference primaries only",
					errs.ErrInvalidArgument, mid,
				)
			}
```

- [ ] **Step 4: Run the test to verify it passes**

```bash
go test ./internal/service/... -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/service/share_service.go internal/service/share_service_test.go
git commit -m "feat(share): reject sidecars in Create media_set

ShareService.Create's media_set per-ID pre-flight loop now
rejects rows with paired_with_id != nil. Without this rejection,
sidecar-only scopes could exist whose recipient grid is empty
under ListSharedMediaIDs (which lists primaries only) —
confusing surface."
```

---

## Task 16: `share.Repo.CoverMediaByScopes` amendment

Add the sidecar-coverage clause so a recipient with scope on a JPEG can fetch the RAW.

**Files:**
- Modify: `internal/share/repo.go::CoverMediaByScopes`
- Modify: `internal/share/repo_test.go`

- [ ] **Step 1: Read the existing function**

```bash
sed -n '570,640p' internal/share/repo.go
```

Note the existing predicate that selects rows whose `media.id` is in the resolved set.

- [ ] **Step 2: Write failing tests**

Append to `internal/share/repo_test.go`:

```go
func TestCoverMediaByScopesIncludesSidecars(t *testing.T) {
	r := require.New(t)
	fx := newShareRepoTest(t)
	ctx := context.Background()

	primary := insertTestMedia(t, fx.mediaRepo, fx.owner, "2024/p.jpg", "cs-pri")
	sidecar := insertTestMedia(t, fx.mediaRepo, fx.owner, "2024/p.dng", "cs-sid")
	r.NoError(fx.mediaRepo.UpdatePairedWithID(ctx, sidecar.ID, &primary.ID))

	scope := seedMediaSetScope(t, fx, fx.owner, fx.grantee, []string{primary.ID})

	// Direct primary ID — covered.
	covered, err := fx.shareRepo.CoverMediaByScopes(ctx, fx.grantee, []string{primary.ID})
	r.NoError(err)
	r.ElementsMatch([]string{primary.ID}, covered)

	// Sidecar ID — covered transitively because its primary is in scope.
	covered, err = fx.shareRepo.CoverMediaByScopes(ctx, fx.grantee, []string{sidecar.ID})
	r.NoError(err)
	r.ElementsMatch([]string{sidecar.ID}, covered)

	_ = scope
}

func TestCoverMediaByScopesPrimaryOnlyScopeStillExcludesNonScoped(t *testing.T) {
	r := require.New(t)
	fx := newShareRepoTest(t)
	ctx := context.Background()

	a := insertTestMedia(t, fx.mediaRepo, fx.owner, "2024/a.jpg", "cs-a")
	b := insertTestMedia(t, fx.mediaRepo, fx.owner, "2024/b.jpg", "cs-b")
	_ = seedMediaSetScope(t, fx, fx.owner, fx.grantee, []string{a.ID})

	// Recipient asks for b — not in scope — must NOT be covered.
	covered, err := fx.shareRepo.CoverMediaByScopes(ctx, fx.grantee, []string{b.ID})
	r.NoError(err)
	r.Empty(covered)
}

func TestListSharedMediaIDsExcludesSidecars(t *testing.T) {
	r := require.New(t)
	fx := newShareRepoTest(t)
	ctx := context.Background()

	primary := insertTestMedia(t, fx.mediaRepo, fx.owner, "2024/p.jpg", "cs-pri")
	sidecar := insertTestMedia(t, fx.mediaRepo, fx.owner, "2024/p.dng", "cs-sid")
	r.NoError(fx.mediaRepo.UpdatePairedWithID(ctx, sidecar.ID, &primary.ID))
	_ = seedMediaSetScope(t, fx, fx.owner, fx.grantee, []string{primary.ID})

	ids, err := fx.shareRepo.ListSharedMediaIDs(ctx, fx.grantee)
	r.NoError(err)
	r.Equal([]string{primary.ID}, ids, "shared-grid lists primaries only")
}
```

`newShareRepoTest`, `seedMediaSetScope`, `fx.shareRepo`, `fx.grantee` follow the existing pattern in `internal/share/repo_test.go`; verify names.

- [ ] **Step 3: Run the tests to verify they fail**

```bash
go test ./internal/share/... -run 'TestCoverMediaByScopes|TestListSharedMediaIDs' -count=1 -v
```

Expected: the sidecar-coverage test fails (current SQL doesn't include sidecars), the others pass.

- [ ] **Step 4: Amend the SQL in `CoverMediaByScopes`**

The exact SQL is at `internal/share/repo.go:581` (search for `CoverMediaByScopes`). Find the existing `WHERE` predicate that selects covered rows; the current shape is roughly:

```sql
WHERE m.id IN (SELECT media_id FROM resolved_set)
```

Change to:

```sql
WHERE m.id IN (SELECT media_id FROM resolved_set)
   OR m.paired_with_id IN (SELECT media_id FROM resolved_set)
```

The `resolved_set` is whatever CTE or subquery the function builds; preserve its identity, just OR the new condition.

- [ ] **Step 5: Run the tests to verify they pass**

```bash
go test ./internal/share/... -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/share/repo.go internal/share/repo_test.go
git commit -m "feat(share): cover sidecars whose primary is in scope

CoverMediaByScopes adds OR media.paired_with_id IN (resolved set)
so a recipient with scope on a JPEG can fetch its RAW via the
shared-bytes endpoint. Primary-only scopes continue to behave
identically; ListSharedMediaIDs unchanged (primaries only).

Tests cover: (a) sidecar covered transitively, (b) non-scoped
primary still excluded, (c) shared-grid list excludes sidecars."
```

---

## Task 17: `thumb.isRAWMime` NEF alignment

Add `image/x-nikon-nef` to the switch so a NEF sidecar takes the embedded-JPEG extraction path.

**Files:**
- Modify: `internal/thumb/worker.go::isRAWMime` (lines 348-357)
- Modify: `internal/thumb/raw_test.go`

- [ ] **Step 1: Write a failing test**

Append to `internal/thumb/raw_test.go`:

```go
func TestExtractPreviewHandlesNEF(t *testing.T) {
	r := require.New(t)
	// Synthesize a minimal TIFF with a SubIFD pointing at an embedded
	// JPEG — same fixture shape used by the existing ARW test.
	bytes := buildEmbeddedJPEGTIFF(t)
	preview, _, err := thumb.ExtractPreview(context.Background(), bytes, "image/x-nikon-nef")
	r.NoError(err)
	r.NotEmpty(preview)
}
```

`buildEmbeddedJPEGTIFF` is the existing helper in `raw_test.go` (search for the ARW test pattern); reuse it. If the fixture builder takes a mime arg or a flag, adjust.

- [ ] **Step 2: Run the test to verify it fails**

```bash
go test ./internal/thumb/... -run TestExtractPreviewHandlesNEF -count=1 -v
```

Expected: FAIL — `image/x-nikon-nef` not in the `isRAWMime` switch, so `ExtractPreview` rejects it.

- [ ] **Step 3: Add NEF to the switch**

Edit `internal/thumb/worker.go:348-357`. Add `"image/x-nikon-nef"` to the switch:

```go
func isRAWMime(mime string) bool {
	switch mime {
	case "image/x-sony-arw",
		"image/x-fuji-raf",
		"image/x-adobe-dng",
		"image/x-canon-cr2",
		"image/x-nikon-nef":
		return true
	}
	return false
}
```

- [ ] **Step 4: Run the test to verify it passes**

```bash
go test ./internal/thumb/... -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/thumb/worker.go internal/thumb/raw_test.go
git commit -m "fix(thumb): isRAWMime knows NEF

internal/ingest/discover.go classifies NEF as image/x-nikon-nef
photo, so without this entry a NEF sidecar would be queued for
thumbs and then fail because the worker would try to decode it as
a regular image. F2.2 preserves the every-photo-row-has-a-thumb
invariant for all five RAW formats §1 of the spec lists."
```

---

## Task 18: `errs.ErrPairedSidecarsExist` sentinel + 409 translator mapping

Future-contract per spec §8.7: ship the sentinel and the HTTP mapping now so a future delete UI inherits the right shape.

**Files:**
- Modify: `internal/errs/errs.go`
- Modify: `internal/httpapi/errors.go::Translate`
- Modify: `internal/httpapi/errors_test.go`

- [ ] **Step 1: Add the sentinel**

In `internal/errs/errs.go`, add to the existing `var (...)` block:

```go
	// ErrPairedSidecarsExist is the future-contract delete sentinel
	// per F2.2 spec §8.7: a primary cannot be deleted while sidecars
	// reference it via paired_with_id. F2.2 ships the sentinel and the
	// HTTP translator mapping (409 Conflict) but no service path
	// throws it yet — Delete is deferred per the broader roadmap.
	ErrPairedSidecarsExist = errors.New("paired sidecars exist")
```

- [ ] **Step 2: Write a failing translator test**

Append to `internal/httpapi/errors_test.go`:

```go
func TestTranslateErrPairedSidecarsExist(t *testing.T) {
	r := require.New(t)
	se := httpapi.Translate(fmt.Errorf("delete: %w", errs.ErrPairedSidecarsExist))
	r.NotNil(se)
	r.Equal(409, se.GetStatus())
}
```

- [ ] **Step 3: Run the test to verify it fails**

```bash
go test ./internal/httpapi/... -run TestTranslateErrPairedSidecarsExist -count=1 -v
```

Expected: FAIL — falls through to 500.

- [ ] **Step 4: Add the mapping**

In `internal/httpapi/errors.go::Translate`, add a case:

```go
	case errors.Is(err, errs.ErrPairedSidecarsExist):
		return huma.Error409Conflict(errs.ErrPairedSidecarsExist.Error())
```

Place it next to the other 409 cases (`ErrAlreadyExists`, `ErrConcurrentImport`).

- [ ] **Step 5: Run the test to verify it passes**

```bash
go test ./internal/httpapi/... -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/errs/errs.go internal/httpapi/errors.go internal/httpapi/errors_test.go
git commit -m "feat(errs): add ErrPairedSidecarsExist + 409 translator mapping

Future-contract sentinel per F2.2 spec §8.7. F2.2 doesn't ship a
delete UI; the sentinel is here so a future delete service path
inherits the right HTTP shape without a translator change."
```

---

## Spec coverage cross-check

| Spec section | Implementing task |
|---|---|
| §3 Architecture | Tasks 8 (importer pass), 9 (CLI), 10-13 (HTTP/frontend) |
| §4.1 Columns | Task 2 |
| §4.1a Triggers | Task 2 |
| §4.2 original_filename flip | Task 6 |
| §4.3 Index | Task 2 |
| §4.4 Down file unchanged | Task 2 (no edit) |
| §4.5 No data migration | Task 1 (env var) + implicit |
| §5.1 Pairing rule | Task 7 (Compute) |
| §5.2 Multi-sibling | Task 7 (Compute ambiguity tests) |
| §5.3 Post-import barrier pass | Task 8 |
| §5.4 Backfill CLI | Task 9 |
| §5.5 Idempotency contract | Task 7 (idempotency + commutativity tests) |
| §6.1 ListFilter.IncludeSidecars | Task 4 |
| §6.2 mediaDTO additions | Task 10 |
| §6.3 List endpoint filter | Task 4 + 10 |
| §6.4 Detail primary embeds | Task 10 |
| §6.5 Detail sidecar paired_with | Task 10 |
| §6.6 Bytes endpoints unchanged | (verified by no edits) |
| §6.7 OpenAPI regen | Task 10 step 7 |
| §7.1 Library grid no change | (verified by Task 13's "Library list omits sidecars" test) |
| §7.2 MediaDetail primary Files row | Task 12 |
| §7.3 MediaDetail sidecar layout | Task 12 |
| §7.4 Routing | Task 12 (branch on paired_with_id) |
| §7.5 Frontend type | Task 11 |
| §8.1 Albums sidecar reject | Task 14 |
| §8.2 Sharing sidecar reject + cover | Tasks 15 + 16 |
| §8.3 GPS backfill unchanged | (verified by no edits to gps.go) |
| §8.4 Hidden privacy forward dep | Out of scope per spec |
| §8.5 Reconcile no special handling | (verified by no edits to reconcile) |
| §8.6 Thumb NEF alignment | Task 17 |
| §8.7 Future delete contract | Task 18 |
| §9 Risks | All tasks have tests covering their risks |
| §10 Dependencies | Task 1 (x/text dep) |
| §11 CLI | Task 9 |
