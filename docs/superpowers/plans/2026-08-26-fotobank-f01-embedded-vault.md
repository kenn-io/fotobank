# Fotobank F01 Embedded Docbank Vault Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use
> superpowers:subagent-driven-development (recommended) or
> superpowers:executing-plans to implement this plan task-by-task. Steps use
> checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add released Docbank as an embedded library behind one Fotobank-owned
adapter and bind the vault lifecycle to the server without moving any product
content path yet.

**Architecture:** `internal/content` is the only package allowed to import
`go.kenn.io/docbank`. It translates public Docbank values and error identities
into Fotobank-owned types, preserves verified-read semantics, and serializes
content mutations. Configuration gives the vault a dedicated root, and server
startup opens and closes it alongside the Fotobank database; import, original,
thumbnail, search, and AI paths remain unchanged in F01.

**Tech Stack:** Go 1.27, Fotobank SQLite with `sqlite_fts5`, Docbank v0.14.0,
TOML configuration, Cobra server lifecycle, and Testify with real temporary
Docbank vaults.

**Spec:**
[`2026-08-25-fotobank-docbank-master-design.md`](../specs/2026-08-25-fotobank-docbank-master-design.md),
especially §§3, 4, 6.2, 7, 9.2, 15.1, 16 F01, and 17.

## Global Constraints

- Target repository: `go.kenn.io/fotobank`.
- Production-code baseline: `origin/main` at
  `b8a9dc35f00a3e07f72d23eaae924d870aa859a8`.
- The governing planning pull request changes documentation and agent policy,
  not the F01 Go surfaces. At execution, verify no later merge changed the
  files named in this plan; re-plan if it did.
- Docbank module: released `v0.14.0` at
  `41a0fbba06f173aa0690505d16584addb58cff5d`.
- Only `internal/content` may import Docbank in Go source or tests.
- F01 does not write or read product originals through Docbank.
- Loose compression stays disabled through Docbank's zero-value configuration.
- Do not add a local module `replace`, compatibility wrapper, dual read, or
  fallback path.

## Produced Fotobank contract

```go
type Config struct {
    Root string
}

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

type Source struct {
    Kind        string
    Description string
    Reference   string
    ModifiedAt  *time.Time
}

type CreateRequest struct {
    VirtualPath string
    MediaType   string
    Expected    Identity
    Source      Source
    Reader      io.Reader
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
    NodeID    int64
    VersionID string
    SHA256    string
    MediaType string
    Size      int64
    Reader    VerifiedReadCloser
}

func Open(ctx context.Context, cfg Config) (*Adapter, error)
func (a *Adapter) Close() error
func (a *Adapter) Create(
    context.Context, CreateRequest,
) (CreateReceipt, error)
func (a *Adapter) Stat(context.Context, string) (Node, error)
func (a *Adapter) OpenCurrent(
    context.Context, string,
) (*Read, error)
func (a *Adapter) OpenVersion(
    context.Context, string,
) (*Read, error)
```

`Node.SHA256` and `Version.SHA256` map Docbank's public `BlobHash`, which is the
computed logical SHA-256 at the pinned release. The adapter does not expose
physical receipts or the underlying vault.

---

### Task 1: Establish the F01 worktree and dependency

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`

- [ ] **Step 1: Read current repository instructions and issue context**

```bash
sed -n '1,220p' AGENTS.md
kata quickstart
kata search "F01 embed Docbank vault" --agent
```

Expected: pull-request development is required and the F01 kata issue from the
planning index is available after the planning PR merges.

- [ ] **Step 2: Create an isolated F01 worktree**

Use `superpowers:using-git-worktrees` from current `origin/main` and create:

```text
feat/docbank-embedded-vault
```

Expected: clean worktree, non-`main` branch, and no production-file diff from
the pinned baseline in the files listed by this plan.

- [ ] **Step 3: Add the exact released module**

```bash
go get go.kenn.io/docbank@v0.14.0
go mod tidy
```

- [ ] **Step 4: Inspect the module graph change**

```bash
go list -m -f '{{.Path}} {{.Version}}' go.kenn.io/docbank
git diff -- go.mod go.sum
```

Expected: Docbank is a direct `v0.14.0` requirement. No `replace` exists and no
unrelated direct module was upgraded.

- [ ] **Step 5: Compile before adding consumers**

```bash
go test -tags sqlite_fts5 ./internal/config ./internal/errs -count=1
```

Expected: PASS with the new module unused.

---

### Task 2: Add and validate the dedicated vault root

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `internal/config/config.example.toml`

**Interfaces:**

```go
type Docbank struct {
    Root string `toml:"root"`
}

// Config gains:
Docbank Docbank `toml:"docbank"`
```

- [ ] **Step 1: Add a failing explicit-root config test**

Write a complete minimal TOML file under `t.TempDir()` with `os.WriteFile`,
using distinct temporary paths, then load it and assert:

```go
require.Equal(t, vaultRoot, cfg.Docbank.Root)
```

Use an explicit `[docbank] root`; the current config tests construct their
temporary files directly and do not provide a shared config-file helper.

- [ ] **Step 2: Run the explicit-root test**

```bash
go test -tags sqlite_fts5 ./internal/config \
  -run TestLoadDocbankRoot -count=1
```

Expected: FAIL because `Config.Docbank` is absent.

- [ ] **Step 3: Add the config shape and home expansion**

Add the `Docbank` type and field exactly as defined above, then include
`&c.Docbank.Root` in `expandHomePaths`.

- [ ] **Step 4: Default the vault beneath flash state**

At the start of `applyDefaults`, after `Flash.Root` is set:

```go
if c.Docbank.Root == "" {
    c.Docbank.Root = filepath.Join(c.Flash.Root, "docbank")
}
```

This is a dedicated subdirectory, not the flash root itself.

- [ ] **Step 5: Run the explicit and default tests**

Add one assertion for the default, then run:

```bash
go test -tags sqlite_fts5 ./internal/config \
  -run 'TestLoadDocbankRoot|TestDefaults' -count=1
```

Expected: PASS.

- [ ] **Step 6: Add failing overlap validation tests**

Cover exact cleaned-path equality for:

- Docbank root and `Flash.Root`; and
- Docbank root and `NAS.Root`.

Each case must use `require.ErrorIs(t, err, errs.ErrBadConfiguration)`.

- [ ] **Step 7: Run the overlap tests**

```bash
go test -tags sqlite_fts5 ./internal/config \
  -run TestValidateDocbankRoot -count=1
```

Expected: FAIL because overlap validation is absent.

- [ ] **Step 8: Implement cleaned absolute-path equality checks**

Add a small unexported normalizer:

```go
func cleanAbsolutePath(value string) (string, error) {
    absolute, err := filepath.Abs(value)
    if err != nil {
        return "", err
    }
    return filepath.Clean(absolute), nil
}
```

Require a non-empty Docbank root, normalize it and the two storage roots, and
reject equality. Do not reject the intended Docbank subdirectory beneath
`Flash.Root`, and do not attempt symlink identity inference.

- [ ] **Step 9: Run all config tests**

```bash
go test -tags sqlite_fts5 ./internal/config -count=1
```

Expected: PASS.

- [ ] **Step 10: Document `[docbank]`**

Add this operator-facing section near flash/NAS storage:

```toml
[docbank]
# Authoritative content-addressed vault. Defaults to <flash.root>/docbank.
# Keep this distinct from import sources and the NAS artifact root.
root = ""
```

Do not enable compression or packing options in Fotobank configuration.

---

### Task 3: Define Fotobank content errors and stable paths

**Files:**
- Modify: `internal/errs/errs.go`
- Modify: `internal/errs/errs_test.go`
- Create: `internal/content/errors.go`
- Create: `internal/content/path.go`
- Create: `internal/content/path_test.go`

**Interfaces:** Produces `errs.ErrContentConflict`,
`errs.ErrContentUnavailable`, and:

```go
func VirtualPath(
    ownerStorageKey, fileID, originalBasename string,
) (string, error)
```

- [ ] **Step 1: Add the two Fotobank sentinels**

```go
ErrContentConflict    = errors.New("content conflict")
ErrContentUnavailable = errors.New("content unavailable")
```

Add them to the existing sentinel uniqueness/classification test table.

- [ ] **Step 2: Run the errors tests**

```bash
go test -tags sqlite_fts5 ./internal/errs -count=1
```

Expected: PASS.

- [ ] **Step 3: Add failing virtual-path tests**

Use fixed valid UUIDs and assert:

```go
got, err := content.VirtualPath(ownerKey, fileID, "IMG_0001.JPG")
require.NoError(t, err)
require.Equal(t,
    "/owners/550e8400-e29b-41d4-a716-446655440000/media/"+
        "7d9b9b0e-77d4-4f80-87eb-7bfbe74716b3/IMG_0001.JPG",
    got,
)
```

Add cases for invalid owner UUID, invalid file UUID, invalid UTF-8, empty,
`.`, `..`, NFC normalization, and an input whose directory components must be
discarded by `filepath.Base`.

- [ ] **Step 4: Run the path tests**

```bash
go test -tags sqlite_fts5 ./internal/content -run TestVirtualPath -count=1
```

Expected: FAIL because the package and function are absent.

- [ ] **Step 5: Implement the virtual path**

Validate both identifiers with the repository's existing UUID dependency,
take `filepath.Base`, reject invalid names, normalize with
`norm.NFC.String`, and join virtual components with `path.Join`:

```go
return path.Join(
    "/owners", ownerStorageKey, "media", fileID, basename,
), nil
```

Filesystem path parsing is host-specific; virtual path assembly is always
slash-separated.

- [ ] **Step 6: Run the path tests**

```bash
go test -tags sqlite_fts5 ./internal/content -run TestVirtualPath -count=1
```

Expected: PASS on the current platform.

- [ ] **Step 7: Implement Docbank error translation**

In `internal/content/errors.go`:

```go
func translateError(err error) error {
    if err == nil {
        return nil
    }
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
}
```

No other package imports Docbank to classify these errors.

---

### Task 4: Wrap create, stat, and verified reads

**Files:**
- Create: `internal/content/adapter.go`
- Create: `internal/content/adapter_test.go`

**Interfaces:** Produces the complete F01 contract at the top of this plan.

- [ ] **Step 1: Add a failing open/close lifecycle test**

```go
adapter, err := content.Open(t.Context(), content.Config{Root: t.TempDir()})
require.NoError(t, err)
require.NoError(t, adapter.Close())
require.NoError(t, adapter.Close())
_, err = adapter.Stat(t.Context(), "/missing.txt")
require.ErrorIs(t, err, errs.ErrContentUnavailable)
```

- [ ] **Step 2: Run the lifecycle test**

```bash
go test -tags sqlite_fts5 ./internal/content \
  -run TestAdapterLifecycle -count=1
```

Expected: FAIL because `Adapter` is absent.

- [ ] **Step 3: Implement adapter ownership and close**

```go
type Adapter struct {
    vault    *docbank.Vault
    mutation sync.Mutex
}

func Open(ctx context.Context, cfg Config) (*Adapter, error) {
    vault, err := docbank.New(ctx, docbank.Config{Root: cfg.Root})
    if err != nil {
        return nil, translateError(err)
    }
    return &Adapter{vault: vault}, nil
}

func (a *Adapter) Close() error {
    if a == nil || a.vault == nil {
        return nil
    }
    return translateError(a.vault.Close())
}
```

Docbank's zero-value compression policy remains disabled.

- [ ] **Step 4: Run the lifecycle test**

```bash
go test -tags sqlite_fts5 ./internal/content \
  -run TestAdapterLifecycle -count=1
```

Expected: PASS.

- [ ] **Step 5: Add a failing create/idempotency test**

Create known bytes and expected identity, call `Adapter.Create` twice with the
same path, source, MIME type, and bytes, then assert:

```go
require.True(t, first.Created)
require.False(t, second.Created)
require.Equal(t, first.Node.ID, second.Node.ID)
require.Equal(t, first.Version.ID, second.Version.ID)
require.Equal(t, expected, first.Identity)
```

Also assert a different expected identity at the same path returns
`errs.ErrContentConflict`.

- [ ] **Step 6: Run the create test**

```bash
go test -tags sqlite_fts5 ./internal/content \
  -run TestAdapterCreate -count=1
```

Expected: FAIL because `Create` is absent.

- [ ] **Step 7: Implement create translation under the mutation lock**

Build `docbank.CreateOptions` from the request, including a non-nil
`ProvenanceSource` only when the Fotobank source has content. Hold
`a.mutation` across the upstream call and translate the receipt into Fotobank
types. Copy the request's virtual path into the returned `Node.VirtualPath`.

- [ ] **Step 8: Run create tests under the race detector**

```bash
go test -race -tags sqlite_fts5 ./internal/content \
  -run TestAdapterCreate -count=1
```

Expected: PASS.

- [ ] **Step 9: Add failing stat and verified-read tests**

After create:

1. `Stat` returns the same node projection and requested virtual path.
2. `OpenCurrent` reads identical bytes to terminal EOF and `Verify` succeeds.
3. `OpenVersion` by the immutable version ID returns the same identity.
4. Missing stat/current/version calls wrap `errs.ErrNotFound`.

- [ ] **Step 10: Run the read tests**

```bash
go test -tags sqlite_fts5 ./internal/content \
  -run 'TestAdapter(Stat|OpenCurrent|OpenVersion)' -count=1
```

Expected: FAIL because these methods are absent.

- [ ] **Step 11: Implement stat and current reads**

`Stat` calls `Vault.Stat`. `OpenCurrent` calls `Vault.OpenContent`, maps the
returned node's current version, hash, size, and MIME type, and passes through
the upstream `VerifiedReadCloser` without buffering or auto-draining it.

- [ ] **Step 12: Implement exact-version reads**

`OpenVersion` calls `Vault.OpenVersionContent`, maps version ID, node ID, hash,
size, and MIME type, and passes through the verified reader.

- [ ] **Step 13: Run all adapter tests**

```bash
go test -tags sqlite_fts5 ./internal/content -count=1
```

Expected: PASS.

---

### Task 5: Enforce the sole-import boundary

**Files:** No new tracked file; this is a pull-request check.

- [ ] **Step 1: Search for imports outside the adapter**

```bash
outside=$(rg -l 'go\.kenn\.io/docbank' --glob '*.go' |
  grep -v '^internal/content/' || true)
test -z "$outside"
```

Expected: PASS with no output.

- [ ] **Step 2: Search for leaked upstream types**

```bash
rg -n 'docbank\.' internal --glob '*.go' |
  grep -v '^internal/content/' && exit 1 || true
```

Expected: no match outside `internal/content`.

- [ ] **Step 3: Run focused package tests**

```bash
go test -tags sqlite_fts5 ./internal/content ./internal/errs -count=1
```

Expected: PASS.

Do not encode the import search as a Go test that reads repository source; keep
it as an architectural review check.

---

### Task 6: Bind vault lifetime to the server

**Files:**
- Modify: `internal/cli/server.go`
- Modify: `internal/cli/server_test.go`

**Interfaces:** The server owns one `*content.Adapter`. No product service
receives it in F01.

- [ ] **Step 1: Add a failing server lifetime test**

Start `runServer` with temporary, disjoint database, NAS, and Docbank roots.
After the test listener is ready, call `content.Open` on the same vault root and
assert it fails while the server is running. Cancel the server, wait for
`runServer` to return, then open and close the same root successfully.

- [ ] **Step 2: Run the focused server test**

```bash
go test -tags sqlite_fts5 ./internal/cli \
  -run TestRunServerOwnsDocbankVaultForLifetime -count=1
```

Expected: FAIL because the server does not open Docbank.

- [ ] **Step 3: Open the adapter after Fotobank database ownership**

In `runServer`, after the database is open and before constructing product
repositories:

```go
contentStore, err := content.Open(
    ctx, content.Config{Root: cfg.Docbank.Root},
)
if err != nil {
    return fmt.Errorf("open Docbank vault: %w", err)
}
defer contentStore.Close()
```

Place cleanup so HTTP handlers and background workers finish before the vault
closes. Do not inject `contentStore` into a product service in this PR.

- [ ] **Step 4: Run the server lifetime test**

```bash
go test -tags sqlite_fts5 ./internal/cli \
  -run TestRunServerOwnsDocbankVaultForLifetime -count=1
```

Expected: PASS.

- [ ] **Step 5: Prove product traffic does not use the adapter**

```bash
rg -n 'contentStore|\*content\.Adapter' internal \
  --glob '*.go' | sort
```

Expected: hits are limited to `internal/content` and server lifecycle wiring;
no importer, service, HTTP route, thumbnail worker, search package, or AI
package receives the adapter.

---

### Task 7: Verify and open F01

**Files:** All F01 files named above.

- [ ] **Step 1: Run focused tests**

```bash
go test -tags sqlite_fts5 \
  ./internal/config ./internal/content ./internal/errs ./internal/cli \
  -count=1
```

Expected: PASS.

- [ ] **Step 2: Run focused race tests**

```bash
go test -race -tags sqlite_fts5 \
  ./internal/content ./internal/cli -count=1
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

- [ ] **Step 4: Re-run boundary and no-compatibility searches**

```bash
test -z "$(rg -l 'go\.kenn\.io/docbank' --glob '*.go' |
  grep -v '^internal/content/' || true)"
! rg -n 'legacy|fallback|dual.write|type .* = .*' internal/content \
  --glob '*.go'
```

Expected: both checks pass. Review any natural-language false positive rather
than weakening the patterns globally.

- [ ] **Step 5: Review the final diff**

```bash
git status --short
git diff --stat origin/main...HEAD
git diff origin/main...HEAD -- \
  go.mod go.sum internal/config internal/content internal/errs \
  internal/cli/server.go internal/cli/server_test.go
```

Expected: F01 adds only dependency, configuration, adapter, errors, and server
lifecycle. Product reads and writes remain unchanged.

- [ ] **Step 6: Commit F01**

Use `kenn:commit` with subject:

```text
feat(content): embed the Docbank vault
```

- [ ] **Step 7: Update the F01 kata issue**

Comment with the branch, commit, exact Docbank version, boundary evidence, and
verification. Do not close before merge.

- [ ] **Step 8: Push and open the pull request**

Use `kenn:commit-push-pr`. The description leads with the new embedded vault
boundary and states explicitly that no product content path changed. Do not add
a routine validation checklist and do not merge.
