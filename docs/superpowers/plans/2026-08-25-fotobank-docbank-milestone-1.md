# Fotobank on Docbank Milestone 1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use
> superpowers:subagent-driven-development (recommended) or
> superpowers:executing-plans to implement this plan task-by-task. Steps use
> checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make Docbank the only authority for newly imported original bytes,
replace Fotobank's one-row-per-file model with opaque assets and media files,
serve full and ranged originals through the embedded Docbank API, and recover
imports interrupted at either database boundary.

**Architecture:** Fotobank opens one embedded Docbank vault through
`internal/content`, the sole package allowed to import `go.kenn.io/docbank`.
Fotobank SQLite owns assets, file relationships, cached Docbank coordinates,
and a durable operation ledger; Docbank owns SHA-256 identity, immutable
versions, and original bytes. A pending asset becomes visible only after every
file operation has a matching Docbank receipt.

**Tech Stack:** Go 1.27, SQLite with `mattn/go-sqlite3` and `sqlite-vec`,
Docbank embedded Go API, Huma v2, Cobra, Testify, and real temporary Docbank
vaults in storage contract tests.

**Spec:**
[`docs/superpowers/specs/2026-08-25-fotobank-docbank-master-design.md`](../specs/2026-08-25-fotobank-docbank-master-design.md)

## Pinned baselines

- Fotobank planning baseline: `0aa045c` on `main`.
- Docbank source baseline: `origin/main` at
  `32a91309ae43b344039909220160646868689d78`.
- Fotobank F01 consumes released Docbank `v0.14.0` at
  `41a0fbba06f173aa0690505d16584addb58cff5d` because its public embedded API
  matches the source baseline used by this plan.
- Docbank currently depends on `go.kenn.io/kit v0.17.1`.
- D02 must land and receive a release tag before F03 updates Fotobank from
  `v0.14.0`. Record that exact tag and commit in F03; do not write the future
  version into this plan before it exists.

## Global constraints

- Docbank is the only authority for original bytes and SHA-256 identity after
  F03. No MD5, legacy original path, fallback read, or dual write remains.
- Fotobank asset and file IDs are random opaque UUIDs. Docbank node IDs and
  virtual paths never cross an HTTP or sharing boundary.
- Only `internal/content` imports `go.kenn.io/docbank`; enforce this with an
  `rg` check in every Fotobank PR.
- Docbank loose compression stays disabled and Fotobank never calls
  `Vault.Pack` for originals.
- Edit `000001_initial_schema.{up,down}.sql` in place. There is no data
  migration because Fotobank has no deployed databases.
- Preserve repo → service → transport layering. Repositories remain DB-only;
  authorization remains in services.
- Full reads use Docbank's verified stream contract and reach EOF or call
  `Verify` before success. Byte ranges are catalog-authorized slices and do not
  claim whole-object verification.
- Keep the vault root, Fotobank SQLite file, thumbnail-artifact roots, import
  sources, and future checkout roots disjoint.
- Fotobank work is committed directly to `main` as its repository instructions
  require. Docbank work uses a feature branch and pull request; never commit to
  Docbank `main`.
- Do not push, open a pull request, merge, or publish a Docbank release unless
  the user authorizes that external action during execution.

## Pull-request order

```text
D02 ───────────────────────────────────────────────┐
                                                   ▼
F01 → F02a → F02b → F03 → F04 → F05 → Milestone 1 gate
```

D02 can be developed and reviewed while F01 and F02a proceed, but F03 cannot
start until D02 is merged and released. F02b is intentionally broad: it is the
single forward cutover of every active product consumer from the old `media`
row to the asset/file model. Splitting it across running product paths would
either create dual models or leave imports invisible to migrated consumers.

## File structure

### Docbank D02

- Modify `types.go`: public range options/result and range sentinel.
- Modify `vault.go`: exact-version range validation, catalog lookup, leased
  range reader, and physical-failure classification.
- Modify `internal/blob/blob.go`: retain logical size from Kit's seekable open.
- Modify `vault_external_test.go`: public raw, compressed, packed, historical,
  invalid-range, and unavailable-content contracts.
- Modify `internal/blob/placement_test.go`: seekable read after authority moves
  to a filesystem secondary.

### Fotobank F01

- Modify `go.mod` and `go.sum`: add released Docbank `v0.14.0`.
- Modify `internal/config/config.go`, `config.example.toml`, and
  `config_test.go`: add and validate `[docbank].root`.
- Create `internal/content/adapter.go`: vault lifecycle and public type
  translation.
- Create `internal/content/path.go`: stable virtual paths and basename
  sanitation.
- Create `internal/content/errors.go`: Docbank-to-Fotobank error identity.
- Create `internal/content/adapter_test.go` and `path_test.go`: real-vault
  contracts.
- Modify `internal/errs/errs.go` and `errs_test.go`: content conflict and
  unavailable sentinels.
- Modify `internal/cli/server.go` and `server_test.go`: open and close the vault
  with server lifetime without routing product reads or writes through it.

### Fotobank F02a

- Modify `internal/db/migrations/000001_initial_schema.up.sql` and
  `000001_initial_schema.down.sql`: add the asset/file/relationship tables
  beside the still-active `media` table.
- Modify `internal/db/migrations_test.go`: assert keys, checks, indexes, and
  relationship triggers.
- Create `internal/media/asset.go`: asset, file, relationship, role, and state
  types.
- Create `internal/media/asset_repo.go` and `asset_repo_test.go`: additive
  DB-only graph repository.
- Modify `internal/owners/owners.go`, `repo.go`, and their tests: make the
  storage key an opaque UUID contract.
- Modify `internal/service/owner_service.go` and
  `owner_service_test.go`: generate a key only on first registration and return
  the stored owner.
- Modify `internal/config/config.example.toml`, `internal/cli/server.go`,
  `internal/cli/import.go`, and their tests: stop deriving a storage key from
  the stub user ID.

### Fotobank F02b

- Modify `internal/db/migrations/000001_initial_schema.up.sql` and
  `000001_initial_schema.down.sql`: remove `media`, make `assets` active, and
  retarget product foreign keys from `media_id` to `asset_id`.
- Replace `internal/media/media.go` and `repo.go` with the active asset/file
  model; fold in or delete the additive F02a files so there is one repository.
- Modify `internal/album/album.go` and `repo.go`.
- Modify `internal/share/share.go`, `repo.go`, and `resolver.go`.
- Modify `internal/service/media_service.go`, `album_service.go`,
  `share_service.go`, `shared_read_service.go`, `thumb_service.go`, and the
  exact sibling tests `media_service_test.go`, `album_service_test.go`,
  `share_service_test.go`, `shared_read_service_test.go`, and
  `thumb_service_test.go`.
- Modify `internal/httpapi/media.go`, `media_geo.go`, `media_original.go`,
  `media_thumb.go`, `hidden_media.go`, `albums.go`, `shares.go`, `shared.go`,
  `shared_bytes.go`, `facets.go`, `search.go`, `events.go`, and their tests.
- Modify `internal/thumb/queue.go`, `worker.go`, and their tests.
- Modify `internal/ai/jobs/queue.go`, `results/repo.go`, `failures/repo.go`,
  `skipped/repo.go`, `embedding/mapping.go`, `embedding/activator.go`,
  `embedding/on_thumb_regen.go`, `gapscanner/scanner.go`,
  `imginput/resolver.go`, both AI workers, and their tests.
- Modify `internal/search/index/fts.go`, `index/sqlitevec.go`,
  `hybrid/filter.go`, and their tests.
- Modify `internal/service/facets/service.go`,
  `internal/service/facets/service_test.go`,
  `internal/service/search/autocomplete.go`,
  `internal/service/search/autocomplete_test.go`,
  `internal/service/search/completeness.go`,
  `internal/service/search/completeness_test.go`, and
  `internal/service/search/service.go`.
- Modify `internal/ingest/importer.go`, `pair.go`, CLI import/pair/GPS code,
  reconciliation code, test seeders, backup tests, and end-to-end fixtures to
  use asset IDs and a selected primary file.

### Fotobank F03

- Modify `go.mod` and `go.sum`: consume the released D02 version.
- Modify the initial schema pair: remove active original `storage_path` and
  MD5, enforce ready-file Docbank mappings, and add `content_operations`.
- Create `internal/content/ledger.go` and `ledger_test.go`: durable pending and
  completed operation rows.
- Extend `internal/content/adapter.go` and tests: range reads from D02.
- Modify `internal/ingest/discover.go`, `pair.go`, `checksum.go`, and
  `importer.go`: stable-source SHA-256 grouping and idempotent Docbank creates.
- Modify owner/shared original services and HTTP tests to stream from Docbank.
- Modify thumbnail and GPS source readers to use Docbank; retain the existing
  storage package only for rebuildable thumbnail artifacts.
- Modify server/import/GPS/thumb CLI wiring and end-to-end tests.
- Delete the old NAS-original reconciliation command and implementation; F05
  adds Docbank reconciliation rather than preserving the old path.

### Fotobank F04

- Create `internal/contentresolve/resolver.go` and `resolver_test.go`: shared
  current/exact source resolution with node/version ownership checks.
- Refactor original, thumbnail, GPS, and future projection seams to consume the
  resolver rather than assemble file coordinates independently.

### Fotobank F05

- Create `internal/content/recovery.go` and `recovery_test.go`: pending-create
  replay and receipt adoption.
- Create `internal/content/reconcile.go` and `reconcile_test.go`: bounded owner
  tree traversal and unmatched-orphan reporting.
- Modify `internal/media/repo.go`: transactional receipt adoption and pending
  asset finalization.
- Modify `internal/cli/server.go` and `import.go`: recover before listener bind
  or new import work.
- Create `internal/cli/e2e_docbank_m1_test.go`: restart boundaries, grouped
  RAW/JPEG/XMP import, verified photo bytes, and video ranges.

---

### Task 1: D02 — Exact-version logical byte ranges in Docbank

**Repository:** `/home/wesm/code/docbank`

**Files:**
- Modify `types.go`
- Modify `vault.go`
- Modify `internal/blob/blob.go`
- Modify `vault_external_test.go`
- Modify `internal/blob/placement_test.go`

**Interfaces:**
- Consumes: `Vault.OpenVersionContent`, `store.ContentVersionByID`, and
  Kit `packstore.Store.Open(ctx, hash)` from `v0.17.1`.
- Produces:

```go
var ErrInvalidContentRange = errors.New("docbank invalid content range")

type ContentRangeOptions struct {
    Offset int64
    Length int64
}

type VersionContentRange struct {
    Version ContentVersion
    Offset  int64
    Length  int64
    Reader  io.ReadCloser
}

func (v *Vault) OpenVersionContentRange(
    ctx context.Context,
    versionID string,
    opts ContentRangeOptions,
) (*VersionContentRange, error)
```

The method requires `Offset >= 0`, `Length > 0`, and
`Offset + Length <= Version.Size`, checked without integer overflow as
`Length <= Version.Size-Offset`. It returns `ErrInvalidContentRange` for an
invalid slice, `ErrNotFound` for an unknown version, and
`ErrContentUnavailable` for missing, malformed, or size-mismatched physical
authority. The returned reader yields exactly `Length` logical decoded bytes,
holds the vault lifecycle lease until `Close`, and does not claim whole-object
verification.

- [ ] **Step 1: Create the Docbank feature branch from the pinned baseline**

Run:

```bash
git fetch origin
git switch -c feat/embedded-version-ranges 32a91309ae43b344039909220160646868689d78
```

Expected: the new branch has a clean worktree and its merge base with
`origin/main` is the pinned commit. If `origin/main` moved, rebase before the
pull request and repeat all verification.

- [ ] **Step 2: Write failing public range tests**

Add table-driven tests to `vault_external_test.go` that:

```go
got, err := vault.OpenVersionContentRange(t.Context(), receipt.Version.ID,
    docbank.ContentRangeOptions{Offset: 2, Length: 4})
require.NoError(t, err)
defer got.Reader.Close()
body, err := io.ReadAll(got.Reader)
require.NoError(t, err)
require.Equal(t, []byte("2345"), body)
require.Equal(t, receipt.Version, got.Version)
require.Equal(t, int64(2), got.Offset)
require.Equal(t, int64(4), got.Length)
```

Exercise raw loose content, zstd loose content, packed content, and a historical
version after `Put` advances the node. Assert `require.ErrorIs` for negative
offset, zero/negative length, start at EOF, end past EOF, missing version, and
deleted physical bytes. Also force a short physical read after open and assert
the range reader returns `io.ErrUnexpectedEOF`, never a successful short body.

Run:

```bash
go test -tags fts5 . -run 'TestOpenVersionContentRange' -count=1
```

Expected: FAIL because the public types and method do not exist.

- [ ] **Step 3: Preserve logical size in the internal seekable open**

Add this internal method and keep `OpenContext` as its size-discarding caller:

```go
func (s *Store) OpenSeekableContext(
    ctx context.Context, hash string,
) (io.ReadSeekCloser, int64, error) {
    parsed, err := packstore.ParseHash(hash)
    if err != nil {
        return nil, 0, fmt.Errorf("blob hash %q: %w", hash, ErrInvalidHash)
    }
    reader, size, err := s.reader.Open(ctx, parsed)
    if err != nil {
        return nil, 0, fmt.Errorf("opening blob %s: %w", hash, err)
    }
    return reader, size, nil
}
```

`OpenContext` calls `OpenSeekableContext` and returns only the reader. Do not
open hash-shard files or pack files directly in the public vault layer.

- [ ] **Step 4: Implement the leased range**

Add the public types and sentinel, then implement the method in `vault.go`:

```go
func (v *Vault) OpenVersionContentRange(
    ctx context.Context, versionID string, opts ContentRangeOptions,
) (*VersionContentRange, error) {
    if err := v.begin(); err != nil {
        return nil, err
    }
    version, err := v.metadata.ContentVersionByID(ctx, versionID)
    if err != nil {
        v.lifecycle.RUnlock()
        return nil, err
    }
    if opts.Offset < 0 || opts.Length <= 0 || opts.Offset > version.Size ||
        opts.Length > version.Size-opts.Offset {
        v.lifecycle.RUnlock()
        return nil, fmt.Errorf("version %q offset=%d length=%d size=%d: %w",
            versionID, opts.Offset, opts.Length, version.Size,
            ErrInvalidContentRange)
    }
    reader, size, err := v.blobs.OpenSeekableContext(ctx, version.BlobHash)
    if err != nil {
        v.lifecycle.RUnlock()
        return nil, fmt.Errorf("opening content version range %q: %w: %w",
            versionID, ErrContentUnavailable, err)
    }
    if size != version.Size {
        closeErr := reader.Close()
        v.lifecycle.RUnlock()
        return nil, errors.Join(fmt.Errorf(
            "physical size %d does not match version size %d: %w",
            size, version.Size, ErrContentUnavailable), closeErr)
    }
    if _, err := reader.Seek(opts.Offset, io.SeekStart); err != nil {
        closeErr := reader.Close()
        v.lifecycle.RUnlock()
        return nil, errors.Join(fmt.Errorf(
            "seeking content version range %q: %w: %w",
            versionID, ErrContentUnavailable, err), closeErr)
    }
    return &VersionContentRange{
        Version: fromStoreVersion(version), Offset: opts.Offset,
        Length: opts.Length,
        Reader: newLeasedLimitedReader(reader, opts.Length, v.lifecycle.RUnlock),
    }, nil
}
```

Implement the lease helper with `sync.Once`:

```go
type leasedLimitedReader struct {
    source  io.ReadSeekCloser
    limited *io.LimitedReader
    release func()
    once    sync.Once
    closeErr error
}

func newLeasedLimitedReader(
    source io.ReadSeekCloser, length int64, release func(),
) *leasedLimitedReader {
    return &leasedLimitedReader{
        source: source, limited: &io.LimitedReader{R: source, N: length},
        release: release,
    }
}

func (r *leasedLimitedReader) Read(p []byte) (int, error) {
    n, err := r.limited.Read(p)
    if errors.Is(err, io.EOF) && r.limited.N > 0 {
        err = io.ErrUnexpectedEOF
    }
    return n, err
}

func (r *leasedLimitedReader) Close() error {
    r.once.Do(func() {
        r.closeErr = r.source.Close()
        r.release()
    })
    return r.closeErr
}
```

- [ ] **Step 5: Prove secondary and lifecycle behavior**

In `internal/blob/placement_test.go`, extend the existing
`TestPlacementRunnerCopiesVerifiesAndRetiresLooseSource` path: after the only
authority is the filesystem secondary, call `OpenSeekableContext`, seek to a
non-zero offset, and assert the logical suffix. In `vault_external_test.go`,
start `Vault.Close` while a range is open, assert it waits, close the range, and
assert `Close` returns.

Run:

```bash
go test -tags fts5 . ./internal/blob \
  -run 'TestOpenVersionContentRange|TestPlacementRunnerCopiesVerifiesAndRetiresLooseSource' \
  -count=1
```

Expected: PASS.

- [ ] **Step 6: Verify Docbank in both SQLite modes**

Run:

```bash
go test -tags fts5 ./...
CGO_ENABLED=0 go test -tags fts5 ./...
make lint
make docs-build
prek run --all-files
```

Expected: all commands pass. Review `git diff --check` and confirm the public
doc comment says partial ranges are not whole-object verification.

- [ ] **Step 7: Commit D02**

```bash
git add types.go vault.go vault_external_test.go internal/blob/blob.go internal/blob/placement_test.go
git commit -m "feat: expose embedded exact-version ranges"
```

Do not push or open the pull request without user authorization. F03 remains
blocked until D02 is merged and a release tag exists.

---

### Task 2: F01 — Embedded vault lifecycle and sole adapter

**Repository:** `/home/wesm/code/fotobank`

**Files:**
- Modify `go.mod`, `go.sum`
- Modify `internal/config/config.go`, `config.example.toml`, `config_test.go`
- Create `internal/content/adapter.go`, `adapter_test.go`
- Create `internal/content/path.go`, `path_test.go`
- Create `internal/content/errors.go`
- Modify `internal/errs/errs.go`, `errs_test.go`
- Modify `internal/cli/server.go`, `server_test.go`

**Interfaces:**

```go
type Config struct { Root string }

type Identity struct {
    SHA256 string
    Size   int64
}

type Node struct {
    ID               int64
    VirtualPath      string
    CurrentVersionID string
    SHA256           string
    Size             int64
    MediaType        string
    Revision         int64
}

type Version struct {
    ID        string
    NodeID    int64
    SHA256    string
    Size      int64
    MediaType string
}

type CreateRequest struct {
    VirtualPath string
    MediaType   string
    Expected    Identity
    Source      Source
    Reader      io.Reader
}

type Source struct {
    Kind        string
    Description string
    Reference   string
    ModifiedAt  *time.Time
}

type CreateReceipt struct {
    Node     Node
    Version  Version
    Identity Identity
    Created  bool
}

type VerifiedReadCloser interface {
    io.ReadCloser
    Verify() error
}

type Read struct {
    NodeID int64
    VersionID, SHA256, MediaType string
    Size int64
    Reader VerifiedReadCloser
}

func Open(ctx context.Context, cfg Config) (*Adapter, error)
func (a *Adapter) Close() error
func (a *Adapter) Create(context.Context, CreateRequest) (CreateReceipt, error)
func (a *Adapter) Stat(context.Context, string) (Node, error)
func (a *Adapter) OpenCurrent(context.Context, string) (*Read, error)
func (a *Adapter) OpenVersion(context.Context, string) (*Read, error)
```

`Read.Reader` exposes `io.ReadCloser` plus `Verify() error`. The adapter owns a
`sync.Mutex` around content mutation methods. It does not expose the upstream
vault or upstream types.

- [ ] **Step 1: Add the released module and inspect graph changes**

Run:

```bash
go get go.kenn.io/docbank@v0.14.0
go mod tidy
git diff -- go.mod go.sum
```

Expected: `go.kenn.io/docbank v0.14.0` is a direct requirement. Review every
direct-version change rather than accepting unrelated upgrades. Do not add a
`replace` directive pointing at the sibling checkout.

- [ ] **Step 2: Write failing config and lifecycle tests**

Add tests for:

```go
cfg, err := config.Load(testConfigPath(t, `
[flash]
root = "/tmp/fotobank"
[nas]
root = "/tmp/fotobank-nas"
[docbank]
root = "/tmp/fotobank-vault"
`))
require.NoError(t, err)
require.Equal(t, "/tmp/fotobank-vault", cfg.Docbank.Root)
```

Also assert `~` expansion, empty-root rejection after defaults, and rejection
when the Docbank root equals the Fotobank SQLite parent or NAS artifact root.
In `adapter_test.go`, open a real temporary vault, create bytes with an expected
SHA-256, retry the same request idempotently, stat it, read it to verified EOF,
open its exact version, close the adapter twice, and assert later calls wrap
`errs.ErrContentUnavailable` or the documented closed error.

Run:

```bash
go test -tags sqlite_fts5 ./internal/config ./internal/content -count=1
```

Expected: FAIL because the config section and adapter do not exist.

- [ ] **Step 3: Add configuration**

Add:

```go
type Config struct {
    // existing fields
    Docbank Docbank `toml:"docbank"`
}

type Docbank struct {
    Root string `toml:"root"`
}
```

Default `Docbank.Root` to `filepath.Join(Flash.Root, "docbank")`, add it to
`expandHomePaths`, and document `[docbank]` in the example config. Validation
must compare cleaned absolute paths and reject equality with `Flash.Root` or
`NAS.Root`; a dedicated subdirectory beneath `Flash.Root` is the intended
default. Do not try to infer arbitrary symlink aliasing in config validation.

- [ ] **Step 4: Implement the adapter and error translation**

Map errors by identity:

```go
switch {
case errors.Is(err, docbank.ErrNotFound):
    return fmt.Errorf("%w: %w", errs.ErrNotFound, err)
case errors.Is(err, docbank.ErrContentConflict):
    return fmt.Errorf("%w: %w", errs.ErrContentConflict, err)
case errors.Is(err, docbank.ErrDigestMismatch),
     errors.Is(err, docbank.ErrSizeMismatch):
    return fmt.Errorf("%w: %w", errs.ErrInvalidArgument, err)
case errors.Is(err, docbank.ErrContentUnavailable),
     errors.Is(err, docbank.ErrClosed):
    return fmt.Errorf("%w: %w", errs.ErrContentUnavailable, err)
default:
    return err
}
```

Add distinct `ErrContentConflict` and `ErrContentUnavailable` sentinels to
`internal/errs`. Wrap the upstream verified reader; do not read into memory or
hide `Verify`.

- [ ] **Step 5: Implement stable virtual paths**

`VirtualPath(ownerStorageKey, fileID, originalBasename string)` validates both
UUIDs with the repository's UUID package, takes `filepath.Base`, normalizes it
to NFC, rejects invalid UTF-8, empty, `.`, and `..`, and returns:

```go
path.Join("/owners", ownerStorageKey, "media", fileID, basename)
```

Tests must cover Unicode normalization, Windows separators on Windows via
`filepath.Base`, traversal-looking input, and stable output independent of a
checkout rename.

- [ ] **Step 6: Check the sole-import boundary**

Run a repository check rather than adding a source-text test:

```bash
outside=$(rg -l 'go\.kenn\.io/docbank' --glob '*.go' | grep -v '^internal/content/' || true)
test -z "$outside"
```

Expected: no production or test package outside `internal/content` imports
Docbank. This check belongs in each Fotobank PR handoff; it is an architectural
review check, not product behavior to encode in a Go test.

Then run:

```bash
go test -tags sqlite_fts5 ./internal/content ./internal/errs -count=1
```

Expected: PASS.

- [ ] **Step 7: Wire server lifetime without product traffic**

Open the adapter after configuration and before binding listeners. Close it
only after HTTP handlers and background workers have joined, alongside the
Fotobank DB cleanup. Add a server test that holds a vault open, asserts a second
open of the same root fails with Docbank's hierarchy lock error, cancels the
server, and then successfully reopens the root.

No service, transport, importer, or worker calls the adapter in F01.

- [ ] **Step 8: Verify and commit F01**

Run:

```bash
go test -tags sqlite_fts5 ./internal/config ./internal/content ./internal/errs ./internal/cli -count=1
go test -race -tags sqlite_fts5 ./internal/content ./internal/cli -count=1
make test-short
make lint
make nilaway
```

Expected: all commands pass.

```bash
git add go.mod go.sum internal/config internal/content internal/errs internal/cli/server.go internal/cli/server_test.go
git commit -m "feat(content): embed the Docbank vault"
```

---

### Task 3: F02a — Additive asset, file, and relationship domain

**Files:**
- Modify the initial migration up/down pair and migration tests
- Create `internal/media/asset.go`, `asset_repo.go`, `asset_repo_test.go`
- Modify owner domain/repo/service/config/CLI files listed above

**Interfaces:**

```go
type AssetState string
const (
    AssetPending AssetState = "pending"
    AssetReady   AssetState = "ready"
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
    SidecarOf  RelationshipKind = "sidecar_of"
    DerivedFrom RelationshipKind = "derived_from"
    PairedWith RelationshipKind = "paired_with"
)

type Asset struct {
    ID string
    Owner owners.Principal
    State AssetState
    Type Type
    ImportedAt time.Time
    Timestamp *time.Time
    Make, Model, LensModel, FocalLength, Shutter string
    Width, Height, ISO *int
    Aperture *float64
    DurationMs *int64
    Latitude, Longitude *float64
    GPSAt *time.Time
    LocationLabel string
    ThumbStatus string
    ThumbVersion int
    ThumbUpdatedAt *time.Time
    HiddenAt *time.Time
}

type File struct {
    ID, AssetID string
    Owner owners.Principal
    Role FileRole
    MimeType, OriginalFilename, ImportSourcePath string
    Size int64
    StoragePath, MD5 string // active only until F03
    DocbankNodeID *int64
    DocbankVirtualPath, CurrentVersionID, SHA256 string
}

type FileRelationship struct {
    SourceFileID, TargetFileID string
    Kind RelationshipKind
}
```

- [ ] **Step 1: Write failing migration and repository tests**

Tests must prove:

- `assets`, `media_files`, and `media_file_relationships` exist on a fresh DB;
- only one `primary` file can exist per asset;
- a ready asset requires exactly one primary;
- relationship endpoints must share owner and asset and cannot self-reference;
- Docbank mapping columns are either all present or all absent;
- `InsertGraph` commits asset, files, and relationships atomically; and
- a failed relationship leaves no partial graph.

Run:

```bash
go test -tags sqlite_fts5 ./internal/db ./internal/media \
  -run 'TestSchema_Asset|TestAssetRepo' -count=1
```

Expected: FAIL because the tables and repository are absent.

- [ ] **Step 2: Add the additive schema**

Use these load-bearing columns and constraints:

```sql
CREATE TABLE assets (
    id UUID PRIMARY KEY,
    owner_hub TEXT NOT NULL,
    owner_user_id TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('pending','ready','conflict')),
    media_type TEXT NOT NULL CHECK (media_type IN ('photo','video')),
    imported_at TIMESTAMP NOT NULL,
    timestamp TIMESTAMP,
    make TEXT,
    model TEXT,
    lens_model TEXT,
    focal_length TEXT,
    shutter TEXT,
    width INTEGER,
    height INTEGER,
    iso INTEGER,
    aperture REAL,
    duration_ms INTEGER,
    latitude REAL,
    longitude REAL,
    gps_at TIMESTAMP,
    location_label TEXT,
    thumb_status TEXT NOT NULL CHECK (
      thumb_status IN ('pending','working','ready','no_preview','failed')
    ),
    thumb_claimed_at TIMESTAMP,
    thumb_version INTEGER NOT NULL DEFAULT 0,
    thumb_updated_at TIMESTAMP,
    hidden_at TIMESTAMP,
    FOREIGN KEY (owner_hub, owner_user_id) REFERENCES owners(hub, user_id),
    UNIQUE (id, owner_hub, owner_user_id)
);

CREATE TABLE media_files (
    id UUID PRIMARY KEY,
    asset_id UUID NOT NULL,
    owner_hub TEXT NOT NULL,
    owner_user_id TEXT NOT NULL,
    role TEXT NOT NULL CHECK (role IN ('primary','original','sidecar','alternate')),
    mime_type TEXT NOT NULL,
    original_filename TEXT NOT NULL,
    import_source_path TEXT NOT NULL DEFAULT '',
    size INTEGER NOT NULL CHECK (size >= 0),
    storage_path TEXT,
    md5 TEXT,
    docbank_node_id INTEGER,
    docbank_virtual_path TEXT,
    current_version_id TEXT,
    sha256 TEXT,
    FOREIGN KEY (asset_id, owner_hub, owner_user_id)
      REFERENCES assets(id, owner_hub, owner_user_id) ON DELETE CASCADE,
    CHECK ((docbank_node_id IS NULL AND docbank_virtual_path IS NULL AND
            current_version_id IS NULL AND sha256 IS NULL) OR
           (docbank_node_id IS NOT NULL AND docbank_virtual_path IS NOT NULL AND
            current_version_id IS NOT NULL AND sha256 IS NOT NULL))
);

CREATE UNIQUE INDEX media_files_one_primary_uq
    ON media_files(asset_id) WHERE role = 'primary';
```

Add relationship checks with triggers because cross-row owner/asset equality
cannot be expressed as a simple `CHECK`. Add ready-state triggers that permit
building a pending graph, require one primary when marking ready, and prevent
deleting or demoting the primary of a ready asset.

- [ ] **Step 3: Implement additive graph persistence**

Implement:

```go
func NewAssetRepo(rw, ro *sql.DB) *AssetRepo
func (r *AssetRepo) InsertGraph(
    ctx context.Context, asset Asset, files []File,
    relationships []FileRelationship,
) error
func (r *AssetRepo) GetAsset(ctx context.Context, id string) (Asset, error)
func (r *AssetRepo) ListFiles(ctx context.Context, assetID string) ([]File, error)
func (r *AssetRepo) GetFile(ctx context.Context, fileID string) (File, error)
func (r *AssetRepo) GetPrimaryFile(ctx context.Context, assetID string) (File, error)
```

`InsertGraph` uses one writer transaction and marks an initially pending asset
ready only after all files and relationships exist. Keep the existing
`media.Repo` untouched in F02a; no product path writes both models.

- [ ] **Step 4: Make owner storage keys opaque and stable**

Change `OwnerService.Ensure` to:

```go
func (s *OwnerService) Ensure(
    ctx context.Context, p owners.Principal, requestedStorageKey string,
) (owners.Owner, error)
```

For a missing owner, validate a supplied key as a UUID or generate
`uuid.NewString()` when empty. For an existing owner, empty means “use the
stored key”; an explicit different key returns `errs.ErrAlreadyExists`.
Update stub boot/import callers to use the returned `Owner`. Remove the
fallback from storage key to `Stub.UserID`. Put a synthetic UUID in the example
config comment, but leave `storage_key` absent by default so first registration
generates it.

- [ ] **Step 5: Verify the additive boundary**

Run:

```bash
go test -tags sqlite_fts5 ./internal/db ./internal/media ./internal/owners ./internal/service ./internal/config ./internal/cli -count=1
make test-short
```

Expected: PASS. Run this query in a migration test and assert both models exist
in F02a:

```sql
SELECT name FROM sqlite_master
 WHERE type='table' AND name IN ('media','assets','media_files','media_file_relationships')
 ORDER BY name;
```

- [ ] **Step 6: Commit F02a**

```bash
git add internal/db/migrations internal/db/migrations_test.go internal/media \
  internal/owners internal/service/owner_service.go internal/service/owner_service_test.go \
  internal/config internal/cli/server.go internal/cli/server_test.go \
  internal/cli/import.go internal/cli/import_test.go
git commit -m "feat(media): add asset and file domain"
```

---

### Task 4: F02b — Forward-cut every product consumer to assets

**Files:** All F02b files in the file-structure inventory above.

**Interfaces:**
- `media.Repo` is replaced by `media.AssetRepo` at every service and worker
  constructor.
- Public `/media/{id}` IDs remain opaque asset UUIDs; “media” remains a product
  route noun, not a database row type.
- List/detail projections return `media.Asset` plus its selected
  `media.File`; they never flatten a Docbank node ID or virtual path into an
  HTTP DTO.

- [ ] **Step 1: Record the exact mechanical surface**

Run and save the output in the PR working notes:

```bash
rg -l 'FROM media|JOIN media|UPDATE media|INSERT INTO media|DELETE FROM media|media_id|paired_with_id|media\.Media' \
  internal --glob '*.go' --glob '*.sql' | sort
```

Every production hit must be handled in this PR. Test hits change with their
owner package; do not add an alias, view, or compatibility wrapper.

- [ ] **Step 2: Write the asset-facing core tests first**

Rewrite repository and service fixtures to create one ready asset with one
primary file. Add assertions that:

```go
asset, err := repo.GetByID(ctx, assetID)
require.NoError(t, err)
require.Equal(t, assetID, asset.ID)
require.Equal(t, fileID, asset.Primary.ID)
require.Equal(t, media.RolePrimary, asset.Primary.Role)
```

List tests must exclude pending assets. Hidden state, albums, shares, search,
AI jobs, and thumbnails key to `asset.ID`. Run the focused tests and observe
compile/query failures before implementation:

```bash
go test -tags sqlite_fts5 ./internal/media ./internal/service ./internal/album ./internal/share -count=1
```

- [ ] **Step 3: Make the asset schema authoritative**

Remove `media` and its pairing triggers. Rename every product join column from
`media_id` to `asset_id`, including album membership, scope membership, AI
tables, embedding mappings, FTS rows, and vector mappings. Retarget their
foreign keys to `assets(id)`. Preserve all existing owner-consistency triggers
with `assets` as the owner source.

Keep `media_files.storage_path` and `media_files.md5` as the only active
original-location fields until F03. They are removed in F03, not read as a
fallback after Docbank authority starts.

- [ ] **Step 4: Replace the active media repository**

Fold the additive repository into one `media.AssetRepo`. Port existing list,
geo, facet, hidden, thumbnail-queue, and bulk methods to `assets`; join the
single primary file when a caller needs MIME type, size, or storage path.
Return a domain shape like:

```go
type Asset struct {
    ID string
    Owner owners.Principal
    Type Type
    State AssetState
    ImportedAt time.Time
    Timestamp *time.Time
    Primary File
    // EXIF/GPS/thumb/hidden projections
}
```

Do not retain `type Media = Asset`, forwarding methods, or field aliases.

- [ ] **Step 5: Cut services and transports**

Update service signatures and DTO builders to accept `media.Asset`. Original
opening still reads `asset.Primary.StoragePath` through the current storage
backend in F02b. Albums and scopes store asset IDs. Shared authorization checks
asset ownership and hidden state before resolving the primary file.

Run:

```bash
go test -tags sqlite_fts5 ./internal/service ./internal/httpapi ./internal/album ./internal/share -count=1
```

Expected: PASS after the cutover.

- [ ] **Step 6: Cut workers, search, AI, and fixtures**

Rename SQL columns and Go fields to `AssetID` throughout thumbnail claims, AI
jobs/results/failures/skips, embedding generations, FTS/vector indexes, event
payloads, test seeders, and benchmark fixtures. The thumbnail claim includes
the selected primary `media.File`; AI continues to consume the asset's preview
thumbnail. Do not move version-keyed projection work from Milestone 4 into this
PR.

The interim F02b importer creates one ready asset with one primary file per
candidate and writes only the asset model. Delete the old post-import
`paired_with_id` pass and `fotobank pair` command. F03 replaces this interim
single-file importer with pre-write grouping; do not preserve the row-pairing
algorithm as a fallback.

- [ ] **Step 7: Prove the old model is absent**

Run:

```bash
! rg -n 'FROM media|JOIN media|UPDATE media|INSERT INTO media|DELETE FROM media|paired_with_id|media\.Media|type Media struct' \
  internal --glob '*.go' --glob '*.sql'
! rg -n '\bmedia_id\b|MediaID' internal --glob '*.go' --glob '*.sql'
go test -tags sqlite_fts5 ./... -shuffle=on
make api-generate
make lint
make nilaway
```

Expected: searches return no matches and all commands pass. Route paths and
human-facing “media” wording are intentionally outside these source-pattern
checks.

- [ ] **Step 8: Commit F02b**

Stage the exact files reported by `git diff --name-only`; review the full diff,
then commit:

```bash
git commit -m "refactor(media): cut product state to assets"
```

---

### Task 5: F03 — Cut imports and original reads to Docbank

**Depends on:** released D02 and F02b.

**Files:** All F03 files in the file-structure inventory above.

**Interfaces:**

```go
type OperationState string
const (
    OperationPending  OperationState = "pending"
    OperationComplete OperationState = "complete"
    OperationConflict OperationState = "conflict"
)

type CreateOperation struct {
    ID, AssetID, FileID, VirtualPath string
    Owner owners.Principal
    Expected content.Identity
    MediaType, SourcePath string
    SourceModifiedAt time.Time
    State OperationState
    NodeID *int64
    VersionID string
    LastError string
}

type RangeRead struct {
    Version Version
    Offset, Length int64
    Reader io.ReadCloser
}

func (a *Adapter) OpenVersionRange(
    ctx context.Context, versionID string, offset, length int64,
) (*RangeRead, error)
```

Ready files have non-null Docbank node/path/version/SHA-256. Pending files may
have no receipt. No ready file has a NAS original path or MD5.

- [ ] **Step 1: Pin the released D02 version**

After D02 is merged and released, run:

```bash
git -C /home/wesm/code/docbank fetch origin --tags
d02_commit=$(git -C /home/wesm/code/docbank rev-parse origin/main)
d02_tag=$(git -C /home/wesm/code/docbank tag --points-at "$d02_commit" \
  --sort=-version:refname | head -1)
test -n "$d02_tag"
go get go.kenn.io/docbank@"$d02_tag"
go mod tidy
go list -m -f '{{.Version}} {{.Dir}}' go.kenn.io/docbank
```

Expected: `d02_tag` is the authorized release tag, the module output names that
tag, and a compile-time adapter test can call
`OpenVersionContentRange`. Do not use a local `replace`.

- [ ] **Step 2: Write failing ledger and grouped-import tests**

Add tests for:

- one pending asset/file/operation transaction;
- operation receipt completion and asset readiness in one transaction;
- a group containing `IMG_0001.JPG`, `IMG_0001.ARW`, and `IMG_0001.XMP` creates
  one asset, three files, one primary, `paired_with`, and `sidecar_of`;
- a RAW-only group selects RAW as primary;
- a video is a one-file primary asset;
- a duplicate complete group is reported as duplicate;
- a partially duplicate group is a conflict, not silently attached;
- source mutation after hashing is rejected by Docbank expected identity; and
- no projection job is visible before the asset becomes ready.

Run:

```bash
go test -tags sqlite_fts5 ./internal/content ./internal/ingest -count=1
```

Expected: FAIL because the ledger and grouped importer are absent.

- [ ] **Step 3: Replace the transitional schema**

Remove `storage_path` and `md5` from `media_files`. Add a file state and enforce
the ready mapping:

```sql
CHECK (
  (state = 'pending' AND docbank_node_id IS NULL AND current_version_id IS NULL) OR
  (state = 'ready' AND docbank_node_id IS NOT NULL AND
   docbank_virtual_path IS NOT NULL AND current_version_id IS NOT NULL AND
   sha256 IS NOT NULL)
)
```

Add `content_operations` with a unique `file_id`, immutable expected identity
and source facts, nullable receipt fields, state, timestamps, attempt count, and
last error. Foreign keys target the pending asset and file. Add indexes for
pending state and `(owner_hub, owner_user_id, state)`. Do not make SHA-256
unique in Fotobank: Docbank deduplicates physical content, while Fotobank may
intentionally model identical bytes as different files or relationships. The
importer makes the product-level duplicate decision before creating a pending
graph.

- [ ] **Step 4: Implement stable-source SHA-256 discovery**

Replace `Checksum` with:

```go
func SHA256File(path string) (content.Identity, error)
```

It streams through `crypto/sha256`, reports byte count from the bytes actually
read, and wraps open/read/close failures. Add `.xmp` discovery as
`application/rdf+xml` without assigning it an asset media type.

Discovery records initial size and modification time for every candidate. Wait
`Imports.SettleInterval` once for the complete discovered batch, stat every
candidate again, and proceed only for candidates whose observations match.
After hashing, stat that candidate a third time and reject a changed source. Add
`[imports].settle_interval` with a non-negative default of `1s`; tests inject a
clock/wait function rather than sleeping.

- [ ] **Step 5: Group before any authoritative write**

Replace the row-pairing pass with a pure `GroupCandidates` function. Its bucket
key is owner + NFC-normalized relative directory + case-folded NFC stem. Rules:

- exactly one JPEG is primary; RAW/DNG peers are `original` and `paired_with`
  that JPEG;
- without a JPEG, one RAW or DNG is primary;
- XMP is `sidecar` and targets the unique RAW/DNG when present, otherwise the
  unique JPEG;
- standalone PNG/GIF/HEIC/video candidates each form their own primary asset;
- ambiguous multiple-JPEG or multiple-target groups split supported media into
  standalone assets and report the unattached XMP as a conflict.

The pure result contains allocated roles and relationship intents but no IDs or
database calls. Preserve the current directory/stem normalization tests and
replace `paired_with_id` expectations with file-role graph expectations.

- [ ] **Step 6: Implement the operation protocol**

For each group:

1. Hash and extract local metadata.
2. Allocate asset, file, and operation UUIDs.
3. Insert the pending graph and operations in one Fotobank transaction.
4. For each file, open the source and call `Adapter.Create` with its stable
   virtual path, expected identity, MIME type, and filesystem provenance.
5. In one Fotobank transaction, store each receipt and mark its operation
   complete and its file ready.
6. When every file is ready, mark the asset ready and enqueue
   thumbnail, search, and AI work.

The adapter mutation mutex serializes step 4. Never delete a Docbank node when
a later Fotobank transaction fails; F05 adopts it on restart.

- [ ] **Step 7: Cut original consumers to Docbank**

Extend `internal/content.Adapter` with a D02-backed range method. Owner and
shared services first authorize the asset, select its primary file, and then:

- use `OpenVersion` for a full response;
- use `OpenVersionContentRange` for a parsed HTTP range; and
- set the ETag from the file's SHA-256 only after authorization.

Translate Docbank's `ErrInvalidContentRange` to `errs.ErrInvalidArgument` at
the adapter boundary; the HTTP handler alone maps that classified error to
416 after parsing the request's `Range` syntax.

Update `writeOriginalResponse` to consume a small transport-neutral metadata
struct instead of the deleted row shape. Map invalid ranges to 416 and physical
unavailability to 500/no-store. Full-response success requires verified EOF;
surface a late verification failure in logs and metrics because headers may
already be committed.

Thumbnail source decode and GPS backfill open the exact cached current version
through the adapter. Thumbnail artifact reads/writes continue through
`internal/storage`; no original does.

- [ ] **Step 8: Remove the replaced implementation**

Delete MD5 code/tests, timestamped NAS original path selection, video orphan
adoption, original flash-cache population/retention settings, the old NAS
reconcile implementation, and the `fotobank reconcile` command. Keep only the
storage operations required for thumbnail artifacts.

Run:

```bash
! rg -n 'crypto/md5|md5\.|\bMD5\b|\.MD5\b|storage_path|OriginalsCache|resolvePhotoPath|resolveVideoPath|tryAdoptVideoOrphan' \
  internal --glob '*.go' --glob '*.sql' --glob '*.toml'
rg -n 'go\.kenn\.io/docbank' --glob '*.go' | grep -v '^internal/content/' && exit 1 || true
```

Expected: no legacy-original or forbidden Docbank import matches.

- [ ] **Step 9: Verify F03 behavior**

Run:

```bash
go test -tags sqlite_fts5 ./internal/content ./internal/ingest ./internal/service ./internal/httpapi ./internal/thumb ./internal/cli -count=1
go test -race -tags sqlite_fts5 ./internal/content ./internal/ingest -count=1
make api-generate
make test
make lint
make nilaway
```

Expected: all commands pass. Inspect the generated OpenAPI diff; byte-streaming
routes remain raw handlers and must not expose Docbank coordinates.

- [ ] **Step 10: Commit F03**

```bash
git add go.mod go.sum internal/db/migrations internal/db/migrations_test.go \
  internal/content internal/ingest internal/service internal/httpapi \
  internal/thumb internal/cli internal/config internal/storage \
  internal/reconcile internal/errs
git commit -m "feat(ingest): make Docbank original authority"
```

---

### Task 6: F04 — Shared current and exact-version resolver

**Files:**
- Create `internal/contentresolve/resolver.go`, `resolver_test.go`
- Modify current original, thumbnail, and GPS source consumers

**Interfaces:**

```go
type Source struct {
    AssetID, FileID string
    Owner owners.Principal
    Role media.FileRole
    NodeID int64
    VersionID, SHA256, MediaType string
    Size int64
}

type Resolver struct {
    files *media.AssetRepo
    content *content.Adapter
}

func New(files *media.AssetRepo, content *content.Adapter) *Resolver
func (r *Resolver) Current(
    ctx context.Context, assetID string, role media.FileRole,
) (Source, error)
func (r *Resolver) Exact(
    ctx context.Context, fileID, versionID string,
) (Source, error)
func (r *Resolver) Open(
    ctx context.Context, source Source,
) (*content.Read, error)
```

- [ ] **Step 1: Write failing resolver tests**

Use a real vault and Fotobank test DB. Prove current primary selection, exact
historical reads, wrong-node version rejection, absent mapping, non-ready asset,
and cached version/hash disagreement. A version returned by Docbank whose
`NodeID` differs from the file's cached node must return
`errs.ErrContentConflict`.

Run:

```bash
go test -tags sqlite_fts5 ./internal/contentresolve -count=1
```

Expected: FAIL because the package is absent.

- [ ] **Step 2: Implement DB resolution and Docbank validation**

`Current` reads one ready file by asset/role. `Exact` reads the file by opaque
file ID but does not authorize a caller; callers must already be service- or
queue-scoped. `Open` calls the adapter, compares returned node/version,
SHA-256, size, and MIME type with `Source`, closes on mismatch, and returns a
classified conflict.

Do not let this package import HTTP, owners' identity providers, checkout code,
or Docbank directly.

- [ ] **Step 3: Replace independent coordinate assembly**

Inject the resolver into owner/shared original services, thumbnail source
decode, and GPS backfill. Services still perform authorization before calling
it. Queue-driven workers receive asset/file IDs from rows produced through the
service/repo layer.

- [ ] **Step 4: Verify and commit F04**

Run:

```bash
go test -tags sqlite_fts5 ./internal/contentresolve ./internal/service ./internal/thumb ./internal/cli -count=1
go test -race -tags sqlite_fts5 ./internal/contentresolve -count=1
make test-short
make lint
make nilaway
```

```bash
git add internal/contentresolve internal/service internal/thumb internal/cli
git commit -m "feat(content): resolve exact asset versions"
```

---

### Task 7: F05 — Restart recovery and orphan reconciliation

**Files:**
- Create `internal/content/recovery.go`, `recovery_test.go`
- Create `internal/content/reconcile.go`, `reconcile_test.go`
- Modify `internal/content/adapter.go`, `ledger.go`, and tests
- Modify `internal/media/repo.go` and tests
- Modify `internal/cli/server.go`, `import.go`, and tests
- Create `internal/cli/e2e_docbank_m1_test.go`

**Interfaces:**

```go
type RecoveryReport struct {
    Adopted, Retried, Completed, Conflicts, Errors int
}

type Orphan struct {
    FileID, VirtualPath string
    NodeID int64
    VersionID, SHA256 string
}

func RecoverPending(
    ctx context.Context, ledger *Ledger, assets *media.AssetRepo,
    vault *Adapter,
) (RecoveryReport, error)

func ReconcileOwner(
    ctx context.Context, owner owners.Owner, assets *media.AssetRepo,
    vault *Adapter,
) ([]Orphan, error)

type ChildrenPage struct {
    Items []Node
    Offset, Limit, Total int
}

func (a *Adapter) Children(
    ctx context.Context, directoryNodeID int64, limit, offset int,
) (ChildrenPage, error)
```

- [ ] **Step 1: Write crash-boundary tests against a real vault**

Use a `Creator` test wrapper around the real adapter to inject interruption:

1. after the Fotobank pending transaction but before `Create`;
2. after successful Docbank `Create` but before the receipt transaction;
3. after all receipts but before the asset-ready transaction.

Close both databases, reopen them, run `RecoverPending`, and assert one ready
asset, the original preallocated file UUID in its virtual path, one Docbank
node/version, and no duplicate history.

Add a divergent pre-existing path test and assert the operation becomes
`conflict` without a `Put` or overwrite.

Run:

```bash
go test -tags sqlite_fts5 ./internal/content \
  -run 'TestRecoverPending|TestReconcileOwner' -count=1
```

Expected: FAIL because recovery is absent.

- [ ] **Step 2: Implement idempotent pending recovery**

For each pending operation:

- `Stat` expected path;
- if found with matching expected SHA-256, size, MIME type, and current
  version, adopt its node/version receipt and complete the Fotobank
  transaction; the stable operation-owned path is the pre-create identity, so
  there is no prior Docbank node ID to compare after a pre-receipt crash;
- if not found, reopen the recorded source, revalidate expected identity via
  `Create`, and complete;
- if found with different authority, persist `conflict`; and
- if the source is missing before creation, keep the operation `pending`,
  increment its attempt count, record `last_error`, and leave the asset
  non-ready.

After each operation completion, call one transaction that marks the asset
ready and enqueues projections only when all its files are complete. Repeating
recovery after success is a no-op. Recovery also scans pending assets whose
operations are already complete, so a crash between the last receipt and the
asset-ready transaction finishes without replaying a content write.

- [ ] **Step 3: Traverse only the owned virtual subtree**

Use public `Stat` and paged `Children` through the adapter. Start at
`/owners/{owner-storage-key}/media`, advance each page by `len(Items)` until the
next offset reaches `Total`, then page each file-UUID directory the same way.
Validate the expected UUID-directory/single-basename shape and compare each
file node with the Fotobank row. A node with a matching pending operation is
recovered; a node with no file or operation becomes an `Orphan` report entry.

Reconciliation never trashes, moves, repairs, or deletes a node in Milestone 1.
Malformed unexpected subtrees are reported as errors with the virtual path,
not silently skipped.

- [ ] **Step 4: Run recovery before accepting new work**

Server boot and `fotobank import` run pending recovery after opening both
authorities and before listener bind, worker start, or import discovery. Log
aggregate counts and opaque operation/file IDs; omit source filesystem paths at
normal log level.

- [ ] **Step 5: Add the Milestone 1 end-to-end gate**

`e2e_docbank_m1_test.go` creates synthetic files in a temporary import root:

- valid small JPEG bytes;
- distinct RAW bytes with the same basename;
- XMP text with the same basename;
- a video byte sequence long enough for non-zero and suffix ranges.

It imports into a temporary Fotobank DB and real Docbank vault, restarts at all
three injected boundaries, then asserts:

- one grouped photo asset with three correctly related files;
- one video asset;
- full photo bytes reach verified EOF and equal the source;
- video `Range: bytes=2-5` and suffix requests return exact bytes and 206;
- ETags equal quoted Docbank SHA-256 after authorization;
- no Docbank node ID/path appears in JSON or route URLs; and
- the legacy NAS originals directory is absent.

- [ ] **Step 6: Verify the milestone gate and absence checks**

Run:

```bash
go test -tags sqlite_fts5 ./internal/content ./internal/cli \
  -run 'TestRecoverPending|TestReconcileOwner|TestE2EDocbankMilestone1' -count=1
go test -race -tags sqlite_fts5 ./internal/content ./internal/ingest ./internal/cli -count=1
! rg -n 'crypto/md5|paired_with_id|storage_path|OriginalsCache|resolvePhotoPath|resolveVideoPath' \
  internal --glob '*.go' --glob '*.sql' --glob '*.toml'
test "$(rg -l 'go\.kenn\.io/docbank' --glob '*.go' | sort)" = \
  "$(rg -l 'go\.kenn\.io/docbank' internal/content --glob '*.go' | sort)"
make api-generate
make test
make lint
make nilaway
git diff --check
```

Expected: all commands pass and both absence checks succeed.

- [ ] **Step 7: Commit F05**

```bash
git add internal/content internal/media internal/ingest internal/cli
git commit -m "feat(content): recover interrupted Docbank imports"
```

---

## Milestone 1 completion review

- [ ] Confirm D02 is merged and the exact released Docbank tag is recorded in
  `go.mod` and the F03 pull request.
- [ ] Confirm a fresh database schema contains assets/files/relationships and
  the operation ledger, but no `media` table, MD5 column, or original NAS path.
- [ ] Run the end-to-end gate from Task 7 without cached fixtures.
- [ ] Inspect the Docbank vault and prove RAW/JPEG/XMP/video nodes live under
  the UUID virtual-path convention.
- [ ] Confirm full photo reads verify and video ranges work after process
  restart.
- [ ] Confirm restart recovery handles all three cross-database boundaries and
  divergent authority becomes a durable conflict.
- [ ] Confirm unmatched Docbank UUID paths are reported and never deleted.
- [ ] Review the complete milestone diff for accidental Docbank-coordinate
  exposure, dual writes, fallback reads, or compatibility aliases.
- [ ] Record focused, race, full-suite, lint, NilAway, OpenAPI generation, and
  Docbank dual-SQLite-mode results in the implementation handoff.
