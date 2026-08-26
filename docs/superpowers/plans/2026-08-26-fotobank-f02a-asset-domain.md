# Fotobank F02a Final-Shaped Asset and File Domain Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use
> superpowers:subagent-driven-development (recommended) or
> superpowers:executing-plans to implement this plan task-by-task. Steps use
> checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add the opaque asset, media-file, relationship, cached Docbank
mapping, and stable owner-storage-key domain without changing any active
Fotobank product read or write path.

**Architecture:** New `assets`, `media_files`, and
`media_file_relationships` tables sit beside the still-authoritative `media`
table. A DB-only `AssetRepo` persists a complete graph transactionally and
enforces exactly one primary before an asset becomes ready. Owner storage keys
become immutable UUIDs generated once at registration. No importer or product
consumer writes both media models; F03 later performs the atomic product and
Docbank-authority cutover.

**Tech Stack:** Go 1.27, SQLite through Fotobank's existing sqlx wrapper,
`github.com/google/uuid`, Testify, and the editable pre-alpha initial migration.

**Spec:**
[`2026-08-25-fotobank-docbank-master-design.md`](../specs/2026-08-25-fotobank-docbank-master-design.md),
especially §§3, 5, 6, 7, 15.3, 16 F02a, and 17.

## Global Constraints

- Target repository: `go.kenn.io/fotobank`.
- Production-code baseline before F01: `origin/main` at
  `b8a9dc35f00a3e07f72d23eaae924d870aa859a8`.
- Direct pull-request dependency: merged F01. F02a does not call the F01
  adapter, but it starts from the merged F01 branch and retains its sole-import
  boundary.
- At execution, compare the merged F01 versions of every file in this plan to
  the pinned baseline. Re-plan changed signatures; do not layer guessed edits
  over them.
- Edit `000001_initial_schema.up.sql` and `.down.sql` in place. There is no
  numbered data migration because no Fotobank database has shipped.
- Keep the existing `media` table and every active product foreign key in F02a.
- No product importer, service, worker, transport, or fixture writes the new
  tables in F02a.
- Do not add aliases, views, dual writes, fallback reads, or a `Media = Asset`
  compatibility type.
- The inactive file model is final-shaped: it contains only Docbank mapping and
  media semantics, never the legacy storage path or MD5 identity.

## Produced domain contract

```go
type AssetState string

const (
    AssetPending  AssetState = "pending"
    AssetReady    AssetState = "ready"
    AssetConflict AssetState = "conflict"
)

type FileRole string

const (
    RolePrimary   FileRole = "primary"
    RoleOriginal  FileRole = "original"
    RoleSidecar   FileRole = "sidecar"
    RoleAlternate FileRole = "alternate"
)

type RelationshipKind string

const (
    SidecarOf   RelationshipKind = "sidecar_of"
    DerivedFrom RelationshipKind = "derived_from"
    PairedWith  RelationshipKind = "paired_with"
)

type Asset struct {
    ID         string
    Owner      owners.Principal
    State      AssetState
    Type       Type
    ImportedAt time.Time
    Timestamp  *time.Time

    Make, Model, LensModel, FocalLength, Shutter string
    Width, Height, ISO                           *int
    Aperture                                     *float64
    DurationMs                                   *int64
    Latitude, Longitude                          *float64
    GPSAt                                        *time.Time
    LocationLabel                                string
    ThumbStatus                                  string
    ThumbVersion                                 int
    ThumbUpdatedAt                               *time.Time
    HiddenAt                                     *time.Time
}

type File struct {
    ID, AssetID                         string
    Owner                               owners.Principal
    Role                                FileRole
    MimeType, OriginalFilename          string
    ImportSourcePath                    string
    Size                                int64
    DocbankNodeID                       *int64
    DocbankVirtualPath, CurrentVersionID string
    SHA256                              string
}

type FileRelationship struct {
    SourceFileID string
    TargetFileID string
    Kind         RelationshipKind
}

func NewAssetRepo(rw, ro *sql.DB) *AssetRepo
func (r *AssetRepo) InsertGraph(
    ctx context.Context,
    asset Asset,
    files []File,
    relationships []FileRelationship,
) error
func (r *AssetRepo) GetAsset(
    context.Context, string,
) (Asset, error)
func (r *AssetRepo) ListFiles(
    context.Context, string,
) ([]File, error)
func (r *AssetRepo) GetFile(
    context.Context, string,
) (File, error)
func (r *AssetRepo) GetPrimaryFile(
    context.Context, string,
) (File, error)
```

The four Docbank mapping fields are either all absent or all present; a pending
graph may use the absent state. F02a carries no authority coordinate other than
this mapping.

---

### Task 1: Establish the F02a worktree

**Files:** None.

- [ ] **Step 1: Read the merged repository policy and issue**

```bash
sed -n '1,240p' AGENTS.md
kata quickstart
kata search "F02a asset file domain" --agent
```

Expected: F02a is blocked by F01 in kata and F01 is closed with a merged commit
before implementation starts.

- [ ] **Step 2: Create the isolated F02a worktree**

Use `superpowers:using-git-worktrees` from the merged F01 base and create:

```text
feat/asset-file-domain
```

Expected: clean non-`main` worktree whose merge base is the merged F01 commit.

- [ ] **Step 3: Confirm F01's boundary remains intact**

```bash
test -z "$(rg -l 'go\.kenn\.io/docbank' --glob '*.go' |
  grep -v '^internal/content/' || true)"
```

Expected: PASS. F02a does not expand the Docbank import surface.

---

### Task 2: Add the asset and file tables

**Files:**
- Modify: `internal/db/migrations/000001_initial_schema.up.sql`
- Modify: `internal/db/migrations/000001_initial_schema.down.sql`
- Modify: `internal/db/migrations_test.go`

**Interfaces:** Produces additive `assets`, `media_files`, and
`media_file_relationships` tables. The old `media` table remains.

- [ ] **Step 1: Add a failing table-presence migration test**

Query `sqlite_master` on a fresh migrated database:

```sql
SELECT name
FROM sqlite_master
WHERE type = 'table'
  AND name IN ('media', 'assets', 'media_files',
               'media_file_relationships')
ORDER BY name
```

Assert all four names. This proves the additive boundary explicitly.
Also inspect `PRAGMA table_info(media_files)` and assert the final-shaped
Docbank mapping columns exist. Do not encode an absence test for deleted code;
the branch diff and the F02a review check prove no legacy bridge columns were
introduced.

- [ ] **Step 2: Run the table-presence test**

```bash
go test -tags sqlite_fts5 ./internal/db \
  -run TestSchemaAssetTablesAreAdditive -count=1
```

Expected: FAIL because the three new tables are absent.

- [ ] **Step 3: Add the `assets` table**

Add these columns and checks after `media` so owner and legacy dependencies
already exist:

```sql
CREATE TABLE assets (
    id                UUID PRIMARY KEY,
    owner_hub         TEXT NOT NULL,
    owner_user_id     TEXT NOT NULL,
    state             TEXT NOT NULL
                      CHECK (state IN ('pending', 'ready', 'conflict')),
    media_type        TEXT NOT NULL
                      CHECK (media_type IN ('photo', 'video')),
    imported_at       TIMESTAMP NOT NULL,
    timestamp         TIMESTAMP,
    make              TEXT,
    model             TEXT,
    lens_model        TEXT,
    focal_length      TEXT,
    shutter           TEXT,
    width             INTEGER,
    height            INTEGER,
    iso               INTEGER,
    aperture          REAL,
    duration_ms       INTEGER,
    latitude          REAL,
    longitude         REAL,
    gps_at            TIMESTAMP,
    location_label    TEXT,
    thumb_status      TEXT NOT NULL CHECK (
        thumb_status IN ('pending', 'working', 'ready',
                         'no_preview', 'failed')
    ),
    thumb_claimed_at  TIMESTAMP,
    thumb_version     INTEGER NOT NULL DEFAULT 0,
    thumb_updated_at  TIMESTAMP,
    hidden_at         TIMESTAMP,
    FOREIGN KEY (owner_hub, owner_user_id)
      REFERENCES owners(hub, user_id),
    UNIQUE (id, owner_hub, owner_user_id)
);
```

- [ ] **Step 4: Add the `media_files` table**

```sql
CREATE TABLE media_files (
    id                    UUID PRIMARY KEY,
    asset_id              UUID NOT NULL,
    owner_hub             TEXT NOT NULL,
    owner_user_id         TEXT NOT NULL,
    role                  TEXT NOT NULL CHECK (
        role IN ('primary', 'original', 'sidecar', 'alternate')
    ),
    mime_type             TEXT NOT NULL,
    original_filename     TEXT NOT NULL,
    import_source_path    TEXT NOT NULL DEFAULT '',
    size                  INTEGER NOT NULL CHECK (size >= 0),
    docbank_node_id       INTEGER,
    docbank_virtual_path  TEXT,
    current_version_id    TEXT,
    sha256                TEXT,
    FOREIGN KEY (asset_id, owner_hub, owner_user_id)
      REFERENCES assets(id, owner_hub, owner_user_id) ON DELETE CASCADE,
    CHECK (
      (docbank_node_id IS NULL AND docbank_virtual_path IS NULL AND
       current_version_id IS NULL AND sha256 IS NULL) OR
      (docbank_node_id IS NOT NULL AND docbank_node_id > 0 AND
       docbank_virtual_path IS NOT NULL AND
       length(docbank_virtual_path) > 1 AND
       substr(docbank_virtual_path, 1, 1) = '/' AND
       docbank_virtual_path = trim(docbank_virtual_path) AND
       instr(docbank_virtual_path, char(0)) = 0 AND
       instr(docbank_virtual_path, char(92)) = 0 AND
       docbank_virtual_path NOT LIKE '%//%' AND
       docbank_virtual_path NOT LIKE '%/./%' AND
       docbank_virtual_path NOT LIKE '%/../%' AND
       substr(docbank_virtual_path, -2) <> '/.' AND
       substr(docbank_virtual_path, -3) <> '/..' AND
       substr(docbank_virtual_path, -1) <> '/' AND
       docbank_virtual_path LIKE '/owners/%/media/' || id || '/%' AND
       length(docbank_virtual_path) -
         length(replace(docbank_virtual_path, '/', '')) = 5 AND
       current_version_id IS NOT NULL AND
       length(current_version_id) = 36 AND
       current_version_id = lower(current_version_id) AND
       substr(current_version_id, 9, 1) = '-' AND
       substr(current_version_id, 14, 1) = '-' AND
       substr(current_version_id, 15, 1) = '4' AND
       substr(current_version_id, 19, 1) = '-' AND
       substr(current_version_id, 20, 1) GLOB '[89ab]' AND
       substr(current_version_id, 24, 1) = '-' AND
       length(replace(current_version_id, '-', '')) = 32 AND
       replace(current_version_id, '-', '') NOT GLOB '*[^0-9a-f]*' AND
       sha256 IS NOT NULL AND
       length(sha256) = 64 AND sha256 = lower(sha256) AND
       sha256 NOT GLOB '*[^0-9a-f]*')
    )
);

CREATE UNIQUE INDEX media_files_one_primary_uq
  ON media_files(asset_id) WHERE role = 'primary';
CREATE UNIQUE INDEX media_files_docbank_node_uq
  ON media_files(docbank_node_id) WHERE docbank_node_id IS NOT NULL;
CREATE UNIQUE INDEX media_files_docbank_path_uq
  ON media_files(docbank_virtual_path)
  WHERE docbank_virtual_path IS NOT NULL;
CREATE UNIQUE INDEX media_files_current_version_uq
  ON media_files(current_version_id) WHERE current_version_id IS NOT NULL;
CREATE INDEX media_files_asset_idx ON media_files(asset_id, role, id);
```

Do not make SHA-256 unique. Docbank deduplicates physical content while
Fotobank may intentionally represent identical bytes as different files or
relationships.

- [ ] **Step 5: Add the relationship table**

```sql
CREATE TABLE media_file_relationships (
    source_file_id UUID NOT NULL
      REFERENCES media_files(id) ON DELETE CASCADE,
    target_file_id UUID NOT NULL
      REFERENCES media_files(id) ON DELETE CASCADE,
    kind TEXT NOT NULL CHECK (
      kind IN ('sidecar_of', 'derived_from', 'paired_with')
    ),
    PRIMARY KEY (source_file_id, target_file_id, kind),
    CHECK (source_file_id <> target_file_id)
);

CREATE INDEX media_file_relationships_target_idx
  ON media_file_relationships(target_file_id, kind, source_file_id);
```

- [ ] **Step 6: Update the down migration**

Before dropping `media` or `owners`, drop in dependency order:

```sql
DROP TABLE IF EXISTS media_file_relationships;
DROP TABLE IF EXISTS media_files;
DROP TABLE IF EXISTS assets;
```

- [ ] **Step 7: Run the table-presence test**

```bash
go test -tags sqlite_fts5 ./internal/db \
  -run TestSchemaAssetTablesAreAdditive -count=1
```

Expected: PASS.

---

### Task 3: Enforce graph invariants in SQLite

**Files:**
- Modify: the initial migration pair
- Modify: `internal/db/migrations_test.go`

- [ ] **Step 1: Add a failing one-primary test**

Insert a pending asset and one primary file, then attempt a second primary and
assert the unique-index error. Do not test a repository pre-check; exercise the
database constraint.

- [ ] **Step 2: Run the primary test**

```bash
go test -tags sqlite_fts5 ./internal/db \
  -run TestSchemaAssetExactlyOnePrimary -count=1
```

Expected: the second-primary case passes once Task 2's index exists, while the
database rejects the second primary.

- [ ] **Step 3: Add ready-state and file-coordinate triggers**

Add triggers with these exact outcomes:

1. A direct insert with `state = 'ready'` aborts; graphs start pending.
2. Updating an asset to ready aborts unless exactly one primary exists.
3. Updating an asset to ready aborts if any owned file lacks its complete
   Docbank mapping.
4. Deleting the primary of a ready asset aborts.
5. Demoting the primary of a ready asset aborts.
6. Updating a file's `asset_id`, `owner_hub`, or `owner_user_id` aborts. A
   file's asset and owner coordinate are immutable after insertion, so a
   primary cannot be moved away from a ready asset and existing relationship
   invariants cannot be invalidated indirectly.

Use stable error text such as `ready asset requires exactly one primary` and
`ready asset requires mapped files` for readiness violations,
`cannot remove primary from ready asset` for primary removal, and `file asset
and owner are immutable` for coordinate changes, so migration tests can
distinguish the constraints.

The mapping branch of the ready-update trigger uses the all-null state defined
by the table check:

```sql
WHEN NEW.state = 'ready' AND EXISTS (
  SELECT 1 FROM media_files
  WHERE asset_id = NEW.id AND docbank_node_id IS NULL
)
BEGIN
  SELECT RAISE(ABORT, 'ready asset requires mapped files');
END;
```

- [ ] **Step 4: Add ready-state and file-coordinate test cases**

Exercise all six outcomes through SQL against a fresh migrated database and
assert the operation fails at the database boundary.

- [ ] **Step 5: Run the primary and ready tests**

```bash
go test -tags sqlite_fts5 ./internal/db \
  -run 'TestSchemaAsset(ExactlyOnePrimary|ReadyInvariant)' -count=1
```

Expected: PASS.

- [ ] **Step 6: Add failing relationship-consistency tests**

Create files in:

- the same asset and owner;
- two assets under the same owner; and
- assets under different owners.

Assert only the first relationship insert succeeds. Add an update case that
attempts to retarget a valid relationship to another asset.

- [ ] **Step 7: Add relationship owner/asset triggers**

For insert and update, compare source and target rows in `media_files` and
abort unless both `asset_id` and owner columns match. Keep the self-reference
`CHECK` as a separate invariant.

- [ ] **Step 8: Run relationship tests**

```bash
go test -tags sqlite_fts5 ./internal/db \
  -run TestSchemaMediaFileRelationshipConsistency -count=1
```

Expected: PASS.

- [ ] **Step 9: Add mapping all-or-none tests**

Insert one file with all four mapping fields null and one with all four fields
present and valid. Then assert the table check rejects:

- each partial null/non-null combination;
- zero and negative node IDs;
- empty, relative, backslash-containing, repeated-separator, dot-segment,
  wrong-prefix, and wrong-file-ID virtual paths;
- empty, noncanonical, non-v4, or uppercase version IDs; and
- empty, short, uppercase, or non-hex SHA-256 values.

These are direct SQL tests of the persistent cache boundary. Task 5 separately
tests the stronger repository rule that the path must equal the F01 path for
the owning storage key and file ID.

- [ ] **Step 10: Run all new schema tests**

```bash
go test -tags sqlite_fts5 ./internal/db -run TestSchemaAsset -count=1
```

Expected: PASS.

---

### Task 4: Add the inactive asset/file domain types

**Files:**
- Create: `internal/media/asset.go`
- Create: `internal/media/asset_test.go`

**Interfaces:** Produces the domain types at the top of the plan without
changing `Media` or `Repo`.

- [ ] **Step 1: Add compile-time enum tests**

Table-test every state, role, and relationship constant against its exact
stored string. Add a zero-value rejection test for small validation helpers:

```go
require.Error(t, media.ValidateFileRole(""))
require.NoError(t, media.ValidateFileRole(media.RolePrimary))
```

- [ ] **Step 2: Run the type tests**

```bash
go test -tags sqlite_fts5 ./internal/media -run TestAsset -count=1
```

Expected: FAIL because the types are absent.

- [ ] **Step 3: Add exact domain types and validators**

Implement the produced contract. Reuse the existing `media.Type` and its photo
and video constants. Validators use exhaustive switches and wrap
`errs.ErrInvalidArgument` for unknown values.

- [ ] **Step 4: Run the type tests**

```bash
go test -tags sqlite_fts5 ./internal/media -run TestAsset -count=1
```

Expected: PASS.

---

### Task 5: Persist one asset graph atomically

**Files:**
- Create: `internal/media/asset_repo.go`
- Create: `internal/media/asset_repo_test.go`

**Interfaces:** Produces `NewAssetRepo` and `InsertGraph`.

- [ ] **Step 1: Add a failing ready-graph repository test**

Use `testutil.OpenTestDB(t)`, a valid owner fixture, one ready photo asset, one
primary file with a complete synthetic Docbank mapping, and no relationships.
Call `InsertGraph`, then query all three tables and assert one asset, one file,
and zero relationships.

- [ ] **Step 2: Run the ready-graph test**

```bash
go test -tags sqlite_fts5 ./internal/media \
  -run TestAssetRepoInsertGraphReady -count=1
```

Expected: FAIL because `AssetRepo` is absent.

- [ ] **Step 3: Implement constructor and transaction shell**

```go
type AssetRepo struct {
    rw *sql.DB
    ro *sql.DB
}

func NewAssetRepo(rw, ro *sql.DB) *AssetRepo {
    return &AssetRepo{rw: rw, ro: ro}
}
```

`InsertGraph` validates local IDs/enums before `BeginTx`, starts the transaction,
reads the owner's immutable storage key, validates every optional Docbank
mapping against that key, inserts the asset as pending, inserts every file and
relationship with prepared statements, and commits only after the requested
final state is applied.

Read the storage key inside that transaction with the graph owner's complete
principal coordinate:

```sql
SELECT storage_key FROM owners WHERE hub = ? AND user_id = ?
```

Return a wrapped `errs.ErrNotFound` when the owner row does not exist.

- [ ] **Step 4: Insert the asset projection**

Add one named SQL statement containing every `Asset` projection field. Store
the row as pending even when `asset.State` is ready; remember the requested
state locally.

- [ ] **Step 5: Validate mappings, then insert files and relationships**

Reject a file whose `AssetID` or owner differs from the asset before SQL. For
each file, apply this private validation before its insert:

```go
func validateDocbankMapping(file File, ownerStorageKey string) error {
    absent := file.DocbankNodeID == nil &&
        file.DocbankVirtualPath == "" &&
        file.CurrentVersionID == "" && file.SHA256 == ""
    if absent {
        return nil
    }
    if file.DocbankNodeID == nil || *file.DocbankNodeID <= 0 {
        return fmt.Errorf("%w: invalid Docbank node ID", errs.ErrInvalidArgument)
    }
    versionID, err := uuid.Parse(file.CurrentVersionID)
    if err != nil || versionID.Version() != 4 ||
        versionID.String() != file.CurrentVersionID {
        return fmt.Errorf("%w: invalid Docbank version ID", errs.ErrInvalidArgument)
    }
    digest, err := hex.DecodeString(file.SHA256)
    if err != nil || len(digest) != sha256.Size ||
        hex.EncodeToString(digest) != file.SHA256 {
        return fmt.Errorf("%w: invalid Docbank SHA-256", errs.ErrInvalidArgument)
    }
    expectedPath, err := content.VirtualPath(
        ownerStorageKey, file.ID, file.OriginalFilename,
    )
    if err != nil || file.DocbankVirtualPath != expectedPath {
        return fmt.Errorf("%w: invalid Docbank virtual path", errs.ErrInvalidArgument)
    }
    return nil
}
```

An all-absent mapping is allowed only while the graph remains pending or
conflict. Reject a requested ready state if any file mapping is absent; the
database ready trigger is the final enforcement boundary. Import
`internal/content` only for its pure `VirtualPath` contract—`AssetRepo` does not
open or call the Docbank adapter. Insert all file columns and then all
relationship rows in caller order.

- [ ] **Step 6: Finalize the requested state**

If requested state is ready or conflict, update the inserted asset inside the
same transaction. The ready trigger proves the primary invariant. If requested
state is pending, leave it pending.

- [ ] **Step 7: Run the ready-graph test**

```bash
go test -tags sqlite_fts5 ./internal/media \
  -run TestAssetRepoInsertGraphReady -count=1
```

Expected: PASS.

- [ ] **Step 8: Add a failing rollback test**

Insert a graph whose relationship crosses assets or references an absent file.
Assert `InsertGraph` errors and subsequent counts show no asset, file, or
relationship from that graph.

Add table cases for a zero node ID, malformed version UUID, malformed SHA-256,
a virtual path with the wrong owner storage key, and a path with the wrong file
ID. Each must wrap `errs.ErrInvalidArgument` and leave all graph tables
unchanged.

- [ ] **Step 9: Run the rollback test**

```bash
go test -tags sqlite_fts5 ./internal/media \
  -run TestAssetRepoInsertGraphRollsBack -count=1
```

Expected: PASS after transaction rollback is correct.

- [ ] **Step 10: Add pending-graph coverage**

Insert a pending asset with primary/original/sidecar files and valid
`paired_with` plus `sidecar_of` relationships. Assert the graph remains pending
and every relationship is stored once.

- [ ] **Step 11: Run all insert tests under the race detector**

```bash
go test -race -tags sqlite_fts5 ./internal/media \
  -run TestAssetRepoInsertGraph -count=1
```

Expected: PASS.

---

### Task 6: Add graph read methods

**Files:**
- Modify: `internal/media/asset_repo.go`
- Modify: `internal/media/asset_repo_test.go`

- [ ] **Step 1: Add a failing `GetAsset` test**

Read the ready fixture by ID and compare every projection field. Assert a
random valid missing UUID returns `errs.ErrNotFound`.

- [ ] **Step 2: Implement `GetAsset`**

Select every asset column from `r.ro`, scan nullable values with the same
repository conventions as `media.Repo`, and translate `sql.ErrNoRows` to
`errs.ErrNotFound`.

- [ ] **Step 3: Run the asset getter test**

```bash
go test -tags sqlite_fts5 ./internal/media \
  -run TestAssetRepoGetAsset -count=1
```

Expected: PASS.

- [ ] **Step 4: Add failing file getter tests**

Assert `ListFiles` orders by role then ID, `GetFile` returns one exact row, and
`GetPrimaryFile` returns the sole primary. Cover missing asset, file, and
primary with `errs.ErrNotFound`.

- [ ] **Step 5: Implement one shared file scanner**

Use one private `scanFile` helper with a fixed column list so all three methods
map null Docbank fields identically. Do not return Docbank public types.

- [ ] **Step 6: Implement the three file reads**

Scope `ListFiles` and `GetPrimaryFile` by asset ID. `GetFile` uses opaque file
ID. These are DB-only methods; they take no caller principal and perform no
authorization.

- [ ] **Step 7: Run repository tests**

```bash
go test -tags sqlite_fts5 ./internal/media \
  -run TestAssetRepo -count=1
```

Expected: PASS.

---

### Task 7: Make owner storage keys immutable UUIDs

**Files:**
- Modify: the initial migration pair and migration tests
- Modify: `internal/owners/repo.go`
- Modify: `internal/owners/repo_test.go`
- Modify: `internal/service/owner_service.go`
- Modify: `internal/service/owner_service_test.go`

**Interfaces:** Replaces `OwnerService.Ensure` with:

```go
func (s *OwnerService) Ensure(
    ctx context.Context,
    principal owners.Principal,
    requestedStorageKey string,
) (owners.Owner, error)
```

- [ ] **Step 1: Change the schema declaration to UUID**

Change `owners.storage_key` from `TEXT NOT NULL` to `UUID NOT NULL`. Keep the
unique index. Update migration fixtures to use valid UUIDs.

- [ ] **Step 2: Add failing generated-key service tests**

For a missing owner and empty requested key, assert returned owner fields match
the principal, `uuid.Parse(returned.StorageKey)` succeeds, and the repo stores
that exact key.

- [ ] **Step 3: Add failing existing-owner tests**

Prove:

- empty requested key returns the stored owner unchanged;
- the same explicit UUID returns the stored owner;
- a different explicit UUID returns `errs.ErrAlreadyExists`; and
- an invalid explicit key returns `errs.ErrInvalidArgument` without insert.

- [ ] **Step 4: Run focused owner-service tests**

```bash
go test -tags sqlite_fts5 ./internal/service \
  -run TestOwnerServiceEnsure -count=1
```

Expected: FAIL to compile against the old return signature.

- [ ] **Step 5: Implement requested-key normalization**

```go
func normalizeStorageKey(requested string) (string, error) {
    if requested == "" {
        return uuid.NewString(), nil
    }
    parsed, err := uuid.Parse(requested)
    if err != nil {
        return "", fmt.Errorf("%w: storage key must be a UUID",
            errs.ErrInvalidArgument)
    }
    return parsed.String(), nil
}
```

Generate only after the first lookup proves the owner is missing.

- [ ] **Step 6: Return the authoritative stored owner**

On successful insert, return the inserted owner. On a concurrent unique race,
re-read and return the winner when an empty request allowed either generated
key; for an explicit request, require the stored canonical UUID to match or
return `errs.ErrAlreadyExists`.

- [ ] **Step 7: Run owner-service tests under the race detector**

```bash
go test -race -tags sqlite_fts5 ./internal/service \
  -run TestOwnerServiceEnsure -count=1
```

Expected: PASS, including the existing concurrent-insert fixture adapted to
the returned owner.

- [ ] **Step 8: Run owner repository and migration tests**

```bash
go test -tags sqlite_fts5 ./internal/owners ./internal/db -count=1
```

Expected: PASS with valid UUID fixtures.

---

### Task 8: Update active owner-registration callers

**Files:**
- Modify: `internal/cli/server.go`
- Modify: `internal/cli/server_test.go`
- Modify: `internal/cli/import.go`
- Modify: `internal/cli/import_test.go`
- Modify: `internal/cli/owners.go`
- Modify: `internal/cli/owners_test.go`
- Modify: `internal/config/config.example.toml`

**Interfaces:** Active product behavior changes only in owner registration:
missing keys are generated once and stored. Media writes still use the old
`media` table.

- [ ] **Step 1: Add a failing stub-registration test**

Start stub setup with no configured storage key, then assert the owner row has
a parseable UUID rather than the stub user ID. Run setup twice and assert the
second run preserves the first key.

- [ ] **Step 2: Remove the stub-user fallback in server setup**

Replace:

```go
if storageKey == "" {
    storageKey = cfg.Identity.Stub.UserID
}
```

with the returned owner contract:

```go
owner, err := ownerSvc.Ensure(
    ctx, principal, cfg.Identity.Stub.StorageKey,
)
```

Use `owner.StorageKey` wherever the setup path needs the storage key.

- [ ] **Step 3: Run stub server tests**

```bash
go test -tags sqlite_fts5 ./internal/cli \
  -run TestBuildIdentityProvider -count=1
```

Expected: PASS.

- [ ] **Step 4: Add a failing import-registration test**

With stub storage key omitted, run the import setup far enough to register the
owner, then assert its generated UUID is the key passed to the current storage
layer. The test continues to use the legacy media importer in F02a.

- [ ] **Step 5: Update import registration**

Call the returned-owner `Ensure`, remove the user-ID fallback, and seed the
storage-key map with the returned owner or the existing `loadStorageKeys`
result. Do not write an asset graph.

- [ ] **Step 6: Run import tests**

```bash
go test -tags sqlite_fts5 ./internal/cli \
  -run 'TestImport.*Owner|TestRunImport' -count=1
```

Expected: PASS.

- [ ] **Step 7: Make `owners add --storage-key` optional**

Require only `--hub` and `--user-id`. Pass an empty requested key when the flag
is absent, print the returned owner's key with the success response, and retain
explicit UUID input for deterministic provisioning.

- [ ] **Step 8: Add owners CLI tests**

Cover generated output, explicit canonical UUID, invalid explicit key, and
idempotent repeated add.

- [ ] **Step 9: Run owners CLI tests**

```bash
go test -tags sqlite_fts5 ./internal/cli \
  -run TestOwnersAdd -count=1
```

Expected: PASS.

- [ ] **Step 10: Update the example config comment**

State that omitted `identity.stub.storage_key` generates and persists an
opaque UUID on first registration. Show a synthetic UUID only in a commented
deterministic-provisioning example; never default to the user ID.

---

### Task 9: Prove the additive no-dual-write boundary

**Files:** No new tracked file; these are review checks.

- [ ] **Step 1: Prove both schema models exist**

```bash
go test -tags sqlite_fts5 ./internal/db \
  -run TestSchemaAssetTablesAreAdditive -count=1
```

Expected: PASS with `media` and all three new tables.

- [ ] **Step 2: Search active packages for `AssetRepo` construction**

```bash
rg -n 'NewAssetRepo|InsertGraph' internal --glob '*.go'
```

Expected: hits only in `internal/media/asset_repo.go` and its tests. No CLI,
service, worker, or transport writes the additive model.

- [ ] **Step 3: Search for compatibility scaffolding**

```bash
! rg -n 'type Media = Asset|type Asset = Media|legacy.*asset|fallback.*media' \
  internal --glob '*.go'
```

Expected: PASS. Inspect false positives instead of adding broad exclusions.

- [ ] **Step 4: Recheck Docbank import ownership**

```bash
test -z "$(rg -l 'go\.kenn\.io/docbank' --glob '*.go' |
  grep -v '^internal/content/' || true)"
```

Expected: PASS.

---

### Task 10: Verify and open F02a

**Files:** All F02a files named above.

- [ ] **Step 1: Run focused domain tests**

```bash
go test -tags sqlite_fts5 \
  ./internal/db ./internal/media ./internal/owners \
  ./internal/service ./internal/config ./internal/cli \
  -count=1
```

Expected: PASS.

- [ ] **Step 2: Run focused race tests**

```bash
go test -race -tags sqlite_fts5 \
  ./internal/media ./internal/owners ./internal/service ./internal/cli \
  -count=1
```

Expected: PASS.

- [ ] **Step 3: Run repository checks**

```bash
make test-short
make lint
make nilaway
prek run
git diff --check
```

Expected: all commands pass.

- [ ] **Step 4: Review the migration in both directions**

```bash
git diff origin/main...HEAD -- \
  internal/db/migrations/000001_initial_schema.up.sql \
  internal/db/migrations/000001_initial_schema.down.sql \
  internal/db/migrations_test.go
```

Expected: additive asset tables and triggers appear in `up`; their dependent
objects drop before `media` and `owners` in `down`; the old media schema and
product foreign keys remain.

- [ ] **Step 5: Review the complete branch diff**

```bash
git status --short
git diff --stat origin/main...HEAD
git diff origin/main...HEAD -- \
  internal/media internal/owners internal/service/owner_service.go \
  internal/cli internal/config
```

Expected: one inactive asset/file repository plus the active stable-owner-key
change. No product consumer moved to assets.

- [ ] **Step 6: Commit the schema/domain slice**

Use `kenn:commit` with subject:

```text
feat(media): add asset and file domain
```

If owner-key changes form a separately reviewable commit on the same F02a
branch, use a second logical commit rather than combining unrelated commit
messages or amending.

- [ ] **Step 7: Update the F02a kata issue**

Comment with the branch, commits, verified graph invariants, and evidence that
no active path constructs `AssetRepo`. Do not close before merge.

- [ ] **Step 8: Push and open the pull request**

Use `kenn:commit-push-pr`. The description leads with the additive graph and
stable owner identity, explains why no product dual write exists, states the
F01 base, and avoids a routine test checklist. Do not merge.

- [ ] **Step 9: Trigger just-in-time F03 planning when dependencies land**

After the user merges F02a, close its kata issue with the merged commit and
typed evidence. Once D02 is also released, use `superpowers:brainstorming` and
`superpowers:writing-plans` against both exact baselines to author the atomic
F03 consumer-and-authority cutover plan. Do not reintroduce a separately
mergeable consumer cutover or copy speculative steps from the superseded
mega-plan.
