# Docbank D02 Exact-Version Logical Ranges Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use
> superpowers:subagent-driven-development (recommended) or
> superpowers:executing-plans to implement this plan task-by-task. Steps use
> checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a public embedded Docbank operation that returns one bounded
logical byte range from an exact immutable content version.

**Architecture:** The vault resolves the version through the catalog, opens the
catalog-authorized logical representation through `internal/blob`, seeks to the
requested decoded offset, and returns a length-limited leased reader. Raw loose
content uses native filesystem seeking; compressed loose and packed content may
materialize a temporary seekable representation through Kit. A range proves
catalog authorization and slice bounds, not whole-object verification.

**Tech Stack:** Go 1.27, Docbank embedded API, Kit `packstore` v0.17.1,
Testify, mattn SQLite with CGO, and modernc SQLite without CGO.

**Spec:** Fotobank
[`2026-08-25-fotobank-docbank-master-design.md`](../specs/2026-08-25-fotobank-docbank-master-design.md),
especially §§6.2, 12, 15.1, 16 D02, and 17.

## Global Constraints

- Target repository: `go.kenn.io/docbank`.
- Base: `origin/main` at
  `32a91309ae43b344039909220160646868689d78`.
- Kit dependency: `go.kenn.io/kit v0.17.1`.
- Existing exact read: `Vault.OpenVersionContent(ctx, versionID)`.
- Existing catalog lookup: `store.ContentVersionByID(ctx, versionID)`.
- Existing logical seekable open: `packstore.Store.Open(ctx, hash)` returns
  `(io.ReadSeekCloser, logicalSize, error)`.
- Native offsets are promised only for raw loose content. The public operation
  promises identical logical bytes across raw loose, compressed loose, packed,
  and secondary authority.
- Do not expose blob hashes, physical paths, pack coordinates, Kit types, or an
  unverified claim through the public API.
- Do not push or open the Docbank pull request until the complete Docbank test
  matrix below passes. Do not merge it; the user owns merge and release.

## Produced public contract

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
`Length <= Version.Size-Offset` after proving `Offset <= Version.Size`. It
returns `ErrInvalidContentRange` for an invalid slice, `ErrNotFound` for an
unknown version, and `ErrContentUnavailable` for absent, malformed,
size-mismatched, unseekable, or prematurely short physical authority. The
reader returns exactly `Length` decoded bytes or a non-nil error and holds the
vault lifecycle lease until `Close`.

---

### Task 1: Establish the isolated Docbank branch

**Files:** None.

- [ ] **Step 1: Read the target repository instructions**

Run from the Docbank repository root:

```bash
sed -n '1,240p' AGENTS.md
kata quickstart
```

Expected: feature branches and pull requests are required; both SQLite modes
and the `fts5` tag are part of the supported test matrix.

- [ ] **Step 2: Confirm the pinned base still exists**

```bash
git fetch --no-tags origin main
test "$(git rev-parse origin/main)" = \
  32a91309ae43b344039909220160646868689d78
```

Expected: PASS. If `origin/main` differs, stop and re-plan D02 against the new
public and internal signatures; do not silently execute this stale plan.

- [ ] **Step 3: Create the isolated worktree**

Use `superpowers:using-git-worktrees`, then create the feature branch it
selects as:

```text
feat/embedded-version-ranges
```

Expected: the worktree is clean, its branch is not `main`, and its merge base
with `origin/main` is the pinned commit.

---

### Task 2: Define and validate the public range contract

**Files:**
- Modify: `types.go`
- Modify: `vault.go`
- Modify: `vault_external_test.go`

**Interfaces:** Produces the public types and method shown above. The first
implementation may return a classified error after validation; Task 4 adds the
physical reader.

- [ ] **Step 1: Add a failing invalid-range external test**

Add this local helper beside the external tests:

```go
func createRangeFixture(
    t *testing.T, vault *docbank.Vault, virtualPath string, content []byte,
) docbank.PutReceipt {
    t.Helper()
    sum := sha256.Sum256(content)
    receipt, err := vault.Create(
        t.Context(), virtualPath, bytes.NewReader(content),
        docbank.CreateOptions{
            MediaType: "application/octet-stream",
            Expected: docbank.ContentIdentity{
                SHA256: hex.EncodeToString(sum[:]),
                Size: int64(len(content)),
            },
        },
    )
    require.NoError(t, err)
    return receipt
}
```

Then add one table-driven test around a real created version:

```go
func TestOpenVersionContentRangeRejectsInvalidSlices(t *testing.T) {
    vault, err := docbank.New(t.Context(), docbank.Config{Root: t.TempDir()})
    require.NoError(t, err)
    t.Cleanup(func() { require.NoError(t, vault.Close()) })
    receipt := createRangeFixture(
        t, vault, "/ranges/value.bin", []byte("0123456789"),
    )

    cases := []docbank.ContentRangeOptions{
        {Offset: -1, Length: 1},
        {Offset: 0, Length: 0},
        {Offset: 0, Length: -1},
        {Offset: 10, Length: 1},
        {Offset: 9, Length: 2},
        {Offset: math.MaxInt64, Length: math.MaxInt64},
    }
    for _, opts := range cases {
        _, err := vault.OpenVersionContentRange(
            t.Context(), receipt.Version.ID, opts,
        )
        require.ErrorIs(t, err, docbank.ErrInvalidContentRange)
    }
}
```

Use the repository's existing external real-vault helpers rather than adding a
second fixture framework.

- [ ] **Step 2: Run the focused test and observe the contract failure**

```bash
go test -tags fts5 . -run TestOpenVersionContentRangeRejectsInvalidSlices -count=1
```

Expected: FAIL to compile because the public options, sentinel, and method do
not exist.

- [ ] **Step 3: Add the public types and sentinel**

In `types.go`, add the exact `ContentRangeOptions` and `VersionContentRange`
types from the produced contract. Put `ErrInvalidContentRange` beside the other
public sentinel errors in `vault.go`.

- [ ] **Step 4: Add range validation without opening content**

Add the method shell:

```go
func (v *Vault) OpenVersionContentRange(
    ctx context.Context, versionID string, opts ContentRangeOptions,
) (*VersionContentRange, error) {
    if err := v.begin(); err != nil {
        return nil, err
    }
    defer v.lifecycle.RUnlock()

    version, err := v.metadata.ContentVersionByID(ctx, versionID)
    if err != nil {
        return nil, err
    }
    if opts.Offset < 0 || opts.Length <= 0 ||
        opts.Offset > version.Size ||
        opts.Length > version.Size-opts.Offset {
        return nil, fmt.Errorf(
            "version %q offset=%d length=%d size=%d: %w",
            versionID, opts.Offset, opts.Length, version.Size,
            ErrInvalidContentRange,
        )
    }
    return nil, ErrContentUnavailable
}
```

The subtraction order is load-bearing: check `Offset > Size` before computing
`Size-Offset`, and never validate by adding offset and length.

- [ ] **Step 5: Run the invalid-range test**

```bash
go test -tags fts5 . -run TestOpenVersionContentRangeRejectsInvalidSlices -count=1
```

Expected: PASS.

- [ ] **Step 6: Add a missing-version test**

Use a valid version-shaped UUID that has no catalog row:

```go
_, err := vault.OpenVersionContentRange(t.Context(),
    "00000000-0000-4000-8000-000000000000",
    docbank.ContentRangeOptions{Offset: 0, Length: 1})
require.ErrorIs(t, err, docbank.ErrNotFound)
```

- [ ] **Step 7: Run the missing-version test**

```bash
go test -tags fts5 . -run 'TestOpenVersionContentRange.*Missing' -count=1
```

Expected: PASS through the existing catalog error identity.

---

### Task 3: Preserve logical size in the internal seekable open

**Files:**
- Modify: `internal/blob/blob.go`
- Modify: `internal/blob/blob_test.go`

**Interfaces:**

```go
func (s *Store) OpenSeekableContext(
    ctx context.Context, hash string,
) (io.ReadSeekCloser, int64, error)
```

`OpenContext` remains source compatible and delegates to the new method while
discarding logical size.

- [ ] **Step 1: Add a failing logical-size test**

Extend the existing blob-store tests:

```go
reader, size, err := blobs.OpenSeekableContext(t.Context(), receipt.Hash)
require.NoError(t, err)
t.Cleanup(func() { require.NoError(t, reader.Close()) })
require.Equal(t, int64(len(content)), size)
```

Use a non-zero-size blob written through the existing store helper.

- [ ] **Step 2: Run the focused internal test**

```bash
go test -tags fts5 ./internal/blob -run TestOpenSeekableContext -count=1
```

Expected: FAIL to compile because the method is absent.

- [ ] **Step 3: Extract the size-preserving method**

Implement:

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

func (s *Store) OpenContext(
    ctx context.Context, hash string,
) (io.ReadSeekCloser, error) {
    reader, _, err := s.OpenSeekableContext(ctx, hash)
    return reader, err
}
```

Do not open shard files or pack files directly outside Kit.

- [ ] **Step 4: Run the blob tests**

```bash
go test -tags fts5 ./internal/blob -run 'TestOpenSeekableContext|TestOpen' -count=1
```

Expected: PASS with no behavior change to `OpenContext` callers.

---

### Task 4: Return an exact leased logical range

**Files:**
- Modify: `vault.go`
- Modify: `vault_external_test.go`

**Interfaces:** Consumes `blob.Store.OpenSeekableContext`. Completes
`Vault.OpenVersionContentRange`.

- [ ] **Step 1: Add a failing raw-loose happy-path test**

```go
got, err := vault.OpenVersionContentRange(
    t.Context(), receipt.Version.ID,
    docbank.ContentRangeOptions{Offset: 2, Length: 4},
)
require.NoError(t, err)
defer got.Reader.Close()

body, err := io.ReadAll(got.Reader)
require.NoError(t, err)
require.Equal(t, []byte("2345"), body)
require.Equal(t, receipt.Version, got.Version)
require.Equal(t, int64(2), got.Offset)
require.Equal(t, int64(4), got.Length)
```

- [ ] **Step 2: Run the raw-loose test**

```bash
go test -tags fts5 . -run TestOpenVersionContentRangeRawLoose -count=1
```

Expected: FAIL with `ErrContentUnavailable` from the method shell.

- [ ] **Step 3: Add the leased limited reader**

```go
type leasedLimitedReader struct {
    source   io.ReadSeekCloser
    limited  *io.LimitedReader
    release  func()
    once     sync.Once
    closeErr error
}

func newLeasedLimitedReader(
    source io.ReadSeekCloser, length int64, release func(),
) *leasedLimitedReader {
    return &leasedLimitedReader{
        source: source,
        limited: &io.LimitedReader{R: source, N: length},
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

`Close` is idempotent and releases the vault lease exactly once.

- [ ] **Step 4: Replace the method shell with physical open and seek**

Remove the deferred unlock. Keep the lifecycle lease on success and release it
on every failure before a reader exists:

```go
if err := v.begin(); err != nil {
    return nil, err
}
version, err := v.metadata.ContentVersionByID(ctx, versionID)
if err != nil {
    v.lifecycle.RUnlock()
    return nil, err
}
if opts.Offset < 0 || opts.Length <= 0 ||
    opts.Offset > version.Size ||
    opts.Length > version.Size-opts.Offset {
    v.lifecycle.RUnlock()
    return nil, fmt.Errorf(
        "version %q offset=%d length=%d size=%d: %w",
        versionID, opts.Offset, opts.Length, version.Size,
        ErrInvalidContentRange,
    )
}
```

Then open the physical logical representation and use explicit failure
cleanup:

```go
reader, size, err := v.blobs.OpenSeekableContext(ctx, version.BlobHash)
if err != nil {
    v.lifecycle.RUnlock()
    return nil, fmt.Errorf(
        "opening content version range %q: %w: %w",
        versionID, ErrContentUnavailable, err,
    )
}
if size != version.Size {
    closeErr := reader.Close()
    v.lifecycle.RUnlock()
    return nil, errors.Join(fmt.Errorf(
        "physical size %d does not match version size %d: %w",
        size, version.Size, ErrContentUnavailable,
    ), closeErr)
}
if _, err := reader.Seek(opts.Offset, io.SeekStart); err != nil {
    closeErr := reader.Close()
    v.lifecycle.RUnlock()
    return nil, errors.Join(fmt.Errorf(
        "seeking content version range %q: %w: %w",
        versionID, ErrContentUnavailable, err,
    ), closeErr)
}
return &VersionContentRange{
    Version: fromStoreVersion(version),
    Offset: opts.Offset,
    Length: opts.Length,
    Reader: newLeasedLimitedReader(
        reader, opts.Length, v.lifecycle.RUnlock,
    ),
}, nil
```

The result reader owns the remaining lifecycle read lock until `Close`.

- [ ] **Step 5: Run the raw-loose test**

```bash
go test -tags fts5 . -run TestOpenVersionContentRangeRawLoose -count=1
```

Expected: PASS.

- [ ] **Step 6: Add a historical-version test**

Create bytes, record the returned version, `Put` different bytes at the same
path, then range-open the first version ID and assert bytes from the first
identity rather than the current node.

- [ ] **Step 7: Run current and historical tests**

```bash
go test -tags fts5 . -run 'TestOpenVersionContentRange(RawLoose|Historical)' -count=1
```

Expected: PASS.

- [ ] **Step 8: Add a short-read unit test**

Construct `newLeasedLimitedReader` over a seekable test reader with two bytes
and a requested length of four. Assert `io.ReadAll` returns
`io.ErrUnexpectedEOF`, the two available bytes, and one release after two
`Close` calls.

- [ ] **Step 9: Run the short-read test**

```bash
go test -tags fts5 . -run TestLeasedLimitedReaderShortRead -count=1
```

Expected: PASS; a short physical body is never reported as successful.

---

### Task 5: Prove every supported physical representation

**Files:**
- Modify: `vault_external_test.go`
- Modify: `internal/blob/placement_test.go`

**Interfaces:** No new API. These tests prove the representation-independent
contract and raw-loose performance boundary.

- [ ] **Step 1: Add a compressed-loose range test**

Open a test vault with loose zstd compression enabled, create compressible
bytes, confirm the receipt reports compressed loose storage through existing
test inspection, then assert a non-zero logical range.

- [ ] **Step 2: Run the compressed-loose test**

```bash
go test -tags fts5 . -run TestOpenVersionContentRangeCompressedLoose -count=1
```

Expected: PASS through Kit's decoded seekable representation.

- [ ] **Step 3: Add a packed-content range test**

Create pack-eligible bytes, call the public explicit `Vault.Pack` test path,
confirm the loose authority is retired using existing helpers, and assert the
same non-zero logical range.

- [ ] **Step 4: Run the packed test**

```bash
go test -tags fts5 . -run TestOpenVersionContentRangePacked -count=1
```

Expected: PASS. The test asserts logical behavior, not native pack offsets.

- [ ] **Step 5: Extend the filesystem-secondary placement test**

In `TestPlacementRunnerCopiesVerifiesAndRetiresLooseSource`, after the only
authority is the filesystem secondary:

```go
reader, size, err := blobs.OpenSeekableContext(t.Context(), hash)
require.NoError(t, err)
defer reader.Close()
require.Equal(t, int64(len(content)), size)
_, err = reader.Seek(3, io.SeekStart)
require.NoError(t, err)
suffix, err := io.ReadAll(reader)
require.NoError(t, err)
require.Equal(t, content[3:], suffix)
```

- [ ] **Step 6: Run the placement test**

```bash
go test -tags fts5 ./internal/blob \
  -run TestPlacementRunnerCopiesVerifiesAndRetiresLooseSource -count=1
```

Expected: PASS with primary authority absent.

- [ ] **Step 7: Add unavailable-content classification cases**

Use existing test-only storage helpers to remove the sole physical authority
after catalog commit. Assert both open failure and catalog/physical size
disagreement wrap `ErrContentUnavailable`.

- [ ] **Step 8: Run unavailable-content tests**

```bash
go test -tags fts5 . -run TestOpenVersionContentRangeUnavailable -count=1
```

Expected: PASS with `require.ErrorIs`.

---

### Task 6: Prove vault lifecycle behavior

**Files:**
- Modify: `vault_external_test.go`

- [ ] **Step 1: Add a close-waits-for-range test**

Open a range, start `Vault.Close` in a goroutine, and use the existing bounded
test synchronization style to assert `Close` has not returned before the range
closes. Close the range and assert `Vault.Close` returns successfully.

- [ ] **Step 2: Run the lifecycle test under the race detector**

```bash
go test -race -tags fts5 . -run TestOpenVersionContentRangeHoldsVaultLease -count=1
```

Expected: PASS with no race report.

- [ ] **Step 3: Add closed-vault coverage**

Close a vault, call `OpenVersionContentRange`, and assert
`require.ErrorIs(t, err, docbank.ErrClosed)`.

- [ ] **Step 4: Run all range tests**

```bash
go test -tags fts5 . -run TestOpenVersionContentRange -count=1
```

Expected: PASS.

---

### Task 7: Document, verify, commit, and open D02

**Files:**
- Modify: `docs/embedding.md`
- Modify: `types.go`, `vault.go`, `vault_external_test.go`,
  `internal/blob/blob.go`, `internal/blob/blob_test.go`, and
  `internal/blob/placement_test.go`

- [ ] **Step 1: Document the public contract**

State all of these points in the embedded API reference and Go doc comments:

- ranges select an exact immutable version;
- offsets and lengths address decoded logical bytes;
- raw loose content has native offsets;
- compressed loose and packed content may materialize or decode from the
  beginning; and
- a partial range is not whole-object integrity verification.

- [ ] **Step 2: Run the native SQLite suite**

```bash
go test -tags fts5 ./...
```

Expected: PASS.

- [ ] **Step 3: Run the pure-Go SQLite suite**

```bash
CGO_ENABLED=0 go test -tags fts5 ./...
```

Expected: PASS.

- [ ] **Step 4: Run static and documentation checks**

```bash
make lint
make docs-build
prek run
git diff --check
```

Expected: all commands pass.

- [ ] **Step 5: Review the final branch diff**

```bash
git status --short
git diff --stat origin/main...HEAD
git diff origin/main...HEAD -- \
  types.go vault.go vault_external_test.go docs/embedding.md \
  internal/blob/blob.go internal/blob/blob_test.go \
  internal/blob/placement_test.go
```

Expected: only D02 code, tests, and its public API documentation changed. No
Fotobank code or physical-location API appears.

- [ ] **Step 6: Commit the implementation**

Use `kenn:commit` and commit the exact reviewed files with subject:

```text
feat: expose embedded exact-version ranges
```

- [ ] **Step 7: Update the Docbank kata issue**

Comment on the D02 issue with the branch name, commit, focused behavior, and
local verification. Do not close it before the pull request lands and its
released API is available.

- [ ] **Step 8: Draft a standalone Docbank pull request**

Use `kenn:pr-desc`. Lead with the embedded consumer outcome and explain the
logical-range versus whole-verification boundary. Link the Fotobank master
design at:

```text
https://github.com/kenn-io/fotobank/blob/main/docs/superpowers/specs/2026-08-25-fotobank-docbank-master-design.md
```

Do not assume the reviewer has read Fotobank history. Do not include a routine
test checklist in the body.

- [ ] **Step 9: Push and open the pull request**

Use `kenn:commit-push-pr`, including its private-data scrub and published-body
readback. Target Docbank `main`. Do not merge or release.

- [ ] **Step 10: Record the release gate**

After the user merges D02 and publishes a Docbank tag, record the exact tag and
commit in the Milestone 1 index and only then begin the F03 planning workflow.
