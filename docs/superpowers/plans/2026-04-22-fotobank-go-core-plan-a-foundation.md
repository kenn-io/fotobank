# Fotobank Go Core — Plan A: Foundation

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the Go skeleton for fotobank — project layout, tooling, config, DB + full schema, identity, owners service, HTTP server skeleton, owners CLI — producing a runnable `fotobank server` that responds to `/api/v1/me` as the configured stub owner.

**Architecture:** Single pure-Go binary (no CGO) dispatched by first positional arg. HTTP via huma/v2 on the humago adapter for OpenAPI readiness. SQLite via modernc.org/sqlite with split RW (`MaxOpenConns=1`) / RO (`MaxOpenConns=4`) pools and `golang-migrate/v4` iofs-embedded migrations. Identity abstracted behind a `Provider` interface with `StubProvider` and `HeaderProvider` (the latter guarded by a direct-access check).

**Tech Stack:** Go 1.26.0, huma/v2, humago, modernc.org/sqlite, golang-migrate/v4, BurntSushi/toml, google/uuid, gofrs/flock, stretchr/testify, golangci-lint (mise-pinned), nilaway (pre-push), air, prek.

**Source spec:** `docs/superpowers/specs/2026-04-22-fotobank-go-core-phase1-design.md`

**Out-of-scope for this plan (covered in later plans):** storage layer, media/album/scope schemas beyond DDL, ingest, thumbnails, HTTP routes other than `/healthz` and `/me`, legacy migration.

---

## Part 1 — Project scaffold & tooling

### Task 1: Initialize Go module and directory tree

**Files:**
- Create: `go.mod`
- Create: `cmd/fotobank/main.go`
- Create: `cmd/fotobank-openapi/main.go`
- Modify: `.gitignore`

- [ ] **Step 1: Initialize the module**

```bash
cd /path/to/fotobank
go mod init github.com/wesm/fotobank
```

- [ ] **Step 2: Create the directory tree from spec §2.1**

```bash
mkdir -p cmd/fotobank cmd/fotobank-openapi \
  internal/config internal/errs internal/version \
  internal/db/queries internal/db/migrations \
  internal/owners internal/media internal/album internal/share \
  internal/identity internal/broker internal/exifread internal/storage \
  internal/thumb internal/ingest internal/reconcile internal/migrate \
  internal/service internal/httpapi internal/cli internal/testutil \
  tools/migrationhistorycheck tools/testifyhelpercheck \
  testdata scripts
```

- [ ] **Step 3: Write placeholder main files (so `go build` succeeds)**

```go
// cmd/fotobank/main.go
package main

func main() {}
```

```go
// cmd/fotobank-openapi/main.go
package main

func main() {}
```

- [ ] **Step 4: Write `.gitignore`**

```gitignore
# binaries
/fotobank
/fotobank-openapi
*.exe

# build/test artefacts
/tmp/
*.out
*.test
coverage.out

# editor
.vscode/
.idea/

# env
.env
.env.local

# air
/tmp/air/

# IDE swap/temp
*~
.DS_Store
```

- [ ] **Step 5: Verify build**

```bash
go build ./...
```
Expected: no output (success).

- [ ] **Step 6: Commit**

```bash
git add go.mod cmd/ internal/ tools/ testdata/ scripts/ .gitignore
git commit -m "Initialize Go module and directory layout"
```

---

### Task 2: Toolchain pinning (mise, linting, live-reload)

**Files:**
- Create: `mise.toml`
- Create: `.golangci.yml`
- Create: `.air.toml`

- [ ] **Step 1: Write `mise.toml`**

Pin golangci-lint to the same version reference-project uses (check `/path/to/reference-project/mise.toml`). Go itself is pinned via `go.mod`'s `toolchain` directive (set in Task 3).

```toml
[tools]
golangci-lint = "2.11.4"
```

- [ ] **Step 2: Write `.golangci.yml` (ported from reference-project)**

Copy from `/path/to/reference-project/.golangci.yml` verbatim — same linters (`errcheck`, `forbidigo`, `govet` with `shadow`/`fieldalignment` disabled, `ineffassign`, `staticcheck`, `unused`, `modernize`, `testifylint`), same forbidigo patterns (`t.Fatal/Error`, `time.Local/LoadLocation/FixedZone`), same test-file `errcheck` exclusion.

- [ ] **Step 3: Write `.air.toml`**

Copy from `/path/to/reference-project/.air.toml` if present; otherwise produce:

```toml
root = "."
tmp_dir = "tmp/air"

[build]
cmd = "go build -o ./fotobank ./cmd/fotobank"
bin = "./fotobank server"
include_ext = ["go", "sql"]
include_dir = ["cmd", "internal"]
exclude_dir = ["tmp", "testdata"]
delay = 500
```

- [ ] **Step 4: Install the pinned toolchain**

```bash
mise install
mise exec -- golangci-lint --version
```
Expected: prints `golangci-lint has version 2.11.4`.

- [ ] **Step 5: Commit**

```bash
git add mise.toml .golangci.yml .air.toml
git commit -m "Pin golangci-lint via mise; import reference-project lint + air configs"
```

---

### Task 3: Pin Go toolchain and add initial dependencies

**Files:**
- Modify: `go.mod`

- [ ] **Step 1: Set the toolchain directive**

Edit `go.mod` to include:

```
go 1.26.0

toolchain go1.26.0
```

- [ ] **Step 2: Add initial direct dependencies**

```bash
go get github.com/danielgtaylor/huma/v2@latest
go get github.com/BurntSushi/toml@latest
go get github.com/google/uuid@latest
go get github.com/gofrs/flock@latest
go get github.com/stretchr/testify@latest
go get github.com/golang-migrate/migrate/v4@latest
go get modernc.org/sqlite@latest
```

- [ ] **Step 3: Tidy**

```bash
go mod tidy
```

- [ ] **Step 4: Verify build still clean**

```bash
go build ./...
```

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum
git commit -m "Pin Go 1.26 toolchain and pull in direct dependencies"
```

---

### Task 4: Makefile

**Files:**
- Create: `Makefile`

- [ ] **Step 1: Write Makefile (adapted from reference-project)**

Remove all frontend targets. Keep: `build`, `build-release`, `install`, `dev`, `test`, `test-short`, `vet`, `lint`, `nilaway`, `testify-helper-check`, `tidy`, `api-generate`, `install-hooks`, `clean`, `help`.

Key content:

```makefile
.DEFAULT_GOAL := help

VERSION    := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT     := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_DATE := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")

LDFLAGS         := -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.buildDate=$(BUILD_DATE)
LDFLAGS_RELEASE := $(LDFLAGS) -s -w

BINARY := fotobank

.PHONY: build build-release install dev test test-short vet lint nilaway testify-helper-check migration-history-check tidy api-generate install-hooks clean help

build:
	go build -ldflags="$(LDFLAGS)" -o $(BINARY) ./cmd/fotobank

build-release:
	go build -ldflags="$(LDFLAGS_RELEASE)" -trimpath -o $(BINARY) ./cmd/fotobank

install: build-release
	@if [ -d "$(HOME)/.local/bin" ]; then \
		echo "Installing to ~/.local/bin/$(BINARY)"; \
		cp $(BINARY) "$(HOME)/.local/bin/$(BINARY)"; \
	else \
		INSTALL_DIR="$${GOBIN:-$$(go env GOBIN)}"; \
		[ -z "$$INSTALL_DIR" ] && INSTALL_DIR="$$(go env GOPATH)/bin"; \
		mkdir -p "$$INSTALL_DIR"; \
		cp $(BINARY) "$$INSTALL_DIR/$(BINARY)"; \
	fi

dev:
	air

test:
	go test ./... -shuffle=on

test-short:
	go test ./... -short -shuffle=on

vet:
	go vet ./...

lint:
	mise exec -- golangci-lint run --fix
	$(MAKE) testify-helper-check

testify-helper-check:
	go run ./tools/testifyhelpercheck ./...

migration-history-check:
	go run ./tools/migrationhistorycheck

nilaway:
	go run go.uber.org/nilaway/cmd/nilaway -include-pkgs=github.com/wesm/fotobank ./...

tidy:
	go mod tidy

api-generate:
	go run ./cmd/fotobank-openapi > openapi.json

install-hooks:
	prek install -f

clean:
	rm -f $(BINARY) openapi.json

help:
	@awk 'BEGIN{FS=":.*##"} /^[a-zA-Z_-]+:.*?##/ {printf "  %-20s %s\n", $$1, $$2}' $(MAKEFILE_LIST)
```

- [ ] **Step 2: Smoke-test targets that work pre-code**

```bash
make help
make vet
make tidy
```

- [ ] **Step 3: Commit**

```bash
git add Makefile
git commit -m "Add Makefile with build/test/lint targets"
```

---

### Task 5: prek git hooks

**Files:**
- Create: `prek.toml`

- [ ] **Step 1: Port reference-project's `prek.toml`**

Copy `/path/to/reference-project/prek.toml`, then:
- Remove the `frontend-check` hook.
- Remove the `api-generate` hook pattern for frontend files; replace with a fotobank-appropriate version gated on Go/OpenAPI files.
- Keep `migration-history-check`, `gofmt`, `golangci-lint`, `testify-helper-check`, `go-test-short`, `nilaway`, and all builtin hooks.

Resulting `api-generate` hook (replaces reference-project's frontend-aware version):

```toml
{
  id = "api-generate",
  name = "regenerate openapi spec",
  language = "system",
  entry = "make api-generate",
  files = "^(go\\.mod|go\\.sum|cmd/fotobank-openapi/.*\\.go|internal/httpapi/.*\\.go|internal/db/.*\\.go)$",
  pass_filenames = false,
  priority = 0,
},
```

- [ ] **Step 2: Install hooks**

```bash
make install-hooks
```
Expected: `prek installed at .git/hooks/pre-commit` etc.

- [ ] **Step 3: Verify hooks don't fail on empty diff**

```bash
prek run --all-files 2>&1 | tail -20
```
Expected: either pass or skip-where-no-matches. Any failure is a real problem to fix now (e.g. `go test -short` failing because there are no tests is fine — it returns `ok` with `[no test files]`).

- [ ] **Step 4: Commit**

```bash
git add prek.toml
git commit -m "Add prek git hooks config (ported from reference-project)"
```

---

### Task 6: Port `migration-history-check` tool

**Files:**
- Create: `tools/migrationhistorycheck/main.go`
- Create: `tools/migrationhistorycheck/main_test.go`

- [ ] **Step 1: Copy the tool from reference-project**

```bash
cp /path/to/reference-project/tools/migrationhistorycheck/main.go tools/migrationhistorycheck/
cp /path/to/reference-project/tools/migrationhistorycheck/main_test.go tools/migrationhistorycheck/
```

- [ ] **Step 2: Update any hardcoded module paths or migration directories**

Open `tools/migrationhistorycheck/main.go` and change any `github.com/wesm/reference-project` references to `github.com/wesm/fotobank`. Confirm the hook looks at `internal/db/migrations/` (matches our layout).

- [ ] **Step 3: Run tool tests**

```bash
go test ./tools/migrationhistorycheck/... -shuffle=on
```
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add tools/migrationhistorycheck/
git commit -m "Port migration-history-check tool from reference-project"
```

---

### Task 7: Port `testify-helper-check` tool

**Files:**
- Create: `tools/testifyhelpercheck/analyzer.go`
- Create: `tools/testifyhelpercheck/analyzer_test.go`
- Create: `tools/testifyhelpercheck/testdata/...`

- [ ] **Step 1: Copy the tool + testdata from reference-project**

```bash
cp -r /path/to/reference-project/tools/testifyhelpercheck/* tools/testifyhelpercheck/
```

- [ ] **Step 2: Update module path references**

Replace any `github.com/wesm/reference-project` with `github.com/wesm/fotobank` in non-testdata files. Leave `testdata/` untouched — the vendored testify files there must keep their actual import paths.

- [ ] **Step 3: Write a thin `cmd/` wrapper if reference-project has one**

If `tools/testifyhelpercheck/` has a separate `cmd/` in reference-project, port that too; otherwise ensure `go run ./tools/testifyhelpercheck ./...` works (the Makefile target expects it).

- [ ] **Step 4: Run tests**

```bash
go test ./tools/testifyhelpercheck/... -shuffle=on
make testify-helper-check
```
Expected: both PASS (the second with no violations because we have no test files yet).

- [ ] **Step 5: Commit**

```bash
git add tools/testifyhelpercheck/
git commit -m "Port testify-helper-check tool from reference-project"
```

---

## Part 2 — Errors and version stamping

### Task 8: Sentinel errors

**Files:**
- Create: `internal/errs/errs.go`
- Create: `internal/errs/errs_test.go`

- [ ] **Step 1: Write the failing test**

```go
package errs_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wesm/fotobank/internal/errs"
)

func TestSentinelsAreDistinct(t *testing.T) {
	require := require.New(t)
	sentinels := []error{
		errs.ErrNotFound, errs.ErrAlreadyExists, errs.ErrInvalidArgument,
		errs.ErrPermissionDenied, errs.ErrOwnerMismatch, errs.ErrConcurrentImport,
		errs.ErrBrokerUnavailable, errs.ErrIdentityMissing, errs.ErrDirectAccessBlocked,
		errs.ErrMigrationPrecondition, errs.ErrBadConfiguration,
	}
	for i, a := range sentinels {
		for j, b := range sentinels {
			if i == j {
				require.True(errors.Is(a, b))
				continue
			}
			require.False(errors.Is(a, b), "sentinels at %d and %d collapsed", i, j)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/errs/... -shuffle=on
```
Expected: FAIL (package does not yet exist).

- [ ] **Step 3: Implement**

```go
// internal/errs/errs.go
package errs

import "errors"

var (
	ErrNotFound              = errors.New("not found")
	ErrAlreadyExists         = errors.New("already exists")
	ErrInvalidArgument       = errors.New("invalid argument")
	ErrPermissionDenied      = errors.New("permission denied")
	ErrOwnerMismatch         = errors.New("owner mismatch")
	ErrConcurrentImport      = errors.New("another import is in progress")
	ErrBrokerUnavailable     = errors.New("broker unavailable")
	ErrIdentityMissing       = errors.New("identity unavailable")
	ErrDirectAccessBlocked   = errors.New("direct access blocked")
	ErrMigrationPrecondition = errors.New("migration precondition failed")
	ErrBadConfiguration      = errors.New("bad configuration")
)
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./internal/errs/... -shuffle=on
```
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/errs/
git commit -m "Add sentinel error taxonomy"
```

---

### Task 9: Version stamping

**Files:**
- Create: `internal/version/version.go`
- Create: `internal/version/version_test.go`

- [ ] **Step 1: Write the failing test**

```go
package version_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wesm/fotobank/internal/version"
)

func TestDefaultsWhenLdflagsAbsent(t *testing.T) {
	require := require.New(t)
	require.NotEmpty(version.Short)
	require.NotEmpty(version.Commit)
	require.NotEmpty(version.BuildDate)
}

func TestFormatRendersAllThreeFields(t *testing.T) {
	out := version.Format()
	require.Contains(t, out, version.Short)
	require.Contains(t, out, version.Commit)
	require.Contains(t, out, version.BuildDate)
}
```

- [ ] **Step 2: Run, verify fail**

```bash
go test ./internal/version/... -shuffle=on
```
Expected: FAIL.

- [ ] **Step 3: Implement**

```go
// internal/version/version.go
package version

import "fmt"

var (
	Short     = "dev"
	Commit    = "unknown"
	BuildDate = "unknown"
)

func Format() string {
	return fmt.Sprintf("fotobank %s (%s) built %s", Short, Commit, BuildDate)
}
```

- [ ] **Step 4: Run, verify pass**

```bash
go test ./internal/version/... -shuffle=on
```

- [ ] **Step 5: Wire ldflags through to this package**

Update `cmd/fotobank/main.go` to propagate `-ldflags "-X main.version=..."` into `internal/version`. The cleanest pattern mirrors reference-project: `main.go` holds the symbols that ldflags targets; `main()` copies them into `version.Short` etc. before dispatching.

```go
// cmd/fotobank/main.go
package main

import (
	"os"

	"github.com/wesm/fotobank/internal/version"
)

var (
	// Overwritten by -ldflags "-X main.version=..." in release builds.
	vVersion   = "dev"
	vCommit    = "unknown"
	vBuildDate = "unknown"
)

func main() {
	version.Short = vVersion
	version.Commit = vCommit
	version.BuildDate = vBuildDate
	os.Exit(0)
}
```

Update the `Makefile` `LDFLAGS` line to target `main.vVersion`, `main.vCommit`, `main.vBuildDate`:

```makefile
LDFLAGS := -X main.vVersion=$(VERSION) -X main.vCommit=$(COMMIT) -X main.vBuildDate=$(BUILD_DATE)
```

- [ ] **Step 6: Verify**

```bash
make build
./fotobank   # exits 0; silent for now
```

- [ ] **Step 7: Commit**

```bash
git add internal/version/ cmd/fotobank/main.go Makefile
git commit -m "Add version package with ldflags injection"
```

---

## Part 3 — Config

### Task 10: Config types and Load

**Files:**
- Create: `internal/config/config.go`
- Create: `internal/config/config_test.go`
- Create: `testdata/config/minimal.toml`

- [ ] **Step 1: Write the minimal test fixture**

```toml
# testdata/config/minimal.toml
[nas]
root = "/tmp/test-nas"
```

- [ ] **Step 2: Write failing tests**

```go
package config_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wesm/fotobank/internal/config"
)

func TestLoadAppliesDefaults(t *testing.T) {
	r := require.New(t)
	cfg, err := config.Load(filepath.Join("..", "..", "testdata", "config", "minimal.toml"))
	r.NoError(err)

	r.Equal("/tmp/test-nas", cfg.NAS.Root)
	r.NotEmpty(cfg.Flash.Root)                      // defaulted
	r.Equal("flash_cache", cfg.Storage.Mode)        // defaulted
	r.Equal("stub", cfg.Identity.Mode)              // defaulted
	r.Equal("127.0.0.1:8090", cfg.HTTP.ListenAddress)
	r.Equal(30*time.Second, cfg.HTTP.RequestTimeout)
	r.Equal(60*time.Second, cfg.HTTP.WriteTimeout)
	r.Equal(2, cfg.Imports.ConcurrentWorkers)
	r.Equal(4, cfg.Thumbs.WorkerConcurrency)
	r.Equal("stub", cfg.Broker.Mode)
}

func TestLoadMissingFileIsError(t *testing.T) {
	_, err := config.Load("/no/such/path.toml")
	require.Error(t, err)
}
```

- [ ] **Step 3: Run, verify fail**

```bash
go test ./internal/config/... -shuffle=on
```
Expected: FAIL (package missing).

- [ ] **Step 4: Implement types**

```go
// internal/config/config.go
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Flash    Flash    `toml:"flash"`
	NAS      NAS      `toml:"nas"`
	Storage  Storage  `toml:"storage"`
	Identity Identity `toml:"identity"`
	HTTP     HTTP     `toml:"http"`
	Imports  Imports  `toml:"imports"`
	Thumbs   Thumbs   `toml:"thumbs"`
	Broker   Broker   `toml:"broker"`
	Backup   Backup   `toml:"backup"`
}

type Flash struct {
	Root string `toml:"root"`
}

type NAS struct {
	Root string `toml:"root"`
}

type Storage struct {
	Mode                    string `toml:"mode"`
	OriginalsCacheDays      int    `toml:"originals_cache_days"`
	OriginalsCacheMaxMedia  int    `toml:"originals_cache_max_media"`
	ThumbsCacheEnabled      bool   `toml:"thumbs_cache_enabled"`
}

type Identity struct {
	Mode   string         `toml:"mode"`
	Stub   IdentityStub   `toml:"stub"`
	Header IdentityHeader `toml:"header"`
}

type IdentityStub struct {
	Hub        string `toml:"hub"`
	UserID     string `toml:"user_id"`
	Handle     string `toml:"handle"`
	StorageKey string `toml:"storage_key"`
}

type IdentityHeader struct {
	UserIDHeader       string   `toml:"user_id_header"`
	HubHeader          string   `toml:"hub_header"`
	HandleHeader       string   `toml:"handle_header"`
	ScopesHeader       string   `toml:"scopes_header"`
	RequestIDHeader    string   `toml:"request_id_header"`
	TrustedProxyCIDRs  []string `toml:"trusted_proxy_cidrs"`
	ProxySecretHeader  string   `toml:"proxy_secret_header"`
	ProxySecret        string   `toml:"proxy_secret"`
	ProxyMTLSCAFile    string   `toml:"proxy_mtls_ca_file"`
}

type HTTP struct {
	ListenAddress  string        `toml:"listen_address"`
	BaseURL        string        `toml:"base_url"`
	RequestTimeout time.Duration `toml:"request_timeout"`
	WriteTimeout   time.Duration `toml:"write_timeout"`
	CORSOrigins    []string      `toml:"cors_origins"`
}

type Imports struct {
	ConcurrentWorkers int    `toml:"concurrent_workers"`
	FileLockPath      string `toml:"file_lock_path"`
}

type Thumbs struct {
	WorkerConcurrency int           `toml:"worker_concurrency"`
	PollInterval      time.Duration `toml:"poll_interval"`
	LeaseTimeout      time.Duration `toml:"lease_timeout"`
}

type Broker struct {
	Mode string     `toml:"mode"`
	Exec BrokerExec `toml:"exec"`
}

type BrokerExec struct {
	Command          string        `toml:"command"`
	PublishScopeArgs []string      `toml:"publish_scope_args"`
	RevokeScopeArgs  []string      `toml:"revoke_scope_args"`
	CallTimeout      time.Duration `toml:"call_timeout"`
	Env              []string      `toml:"env"`
}

type Backup struct {
	Enabled    bool   `toml:"enabled"`
	Dir        string `toml:"dir"`
	Keep15Min  int    `toml:"keep_15min"`
	KeepHourly int    `toml:"keep_hourly"`
	KeepDaily  int    `toml:"keep_daily"`
}

// Load reads the file at path, applies defaults, and returns the
// parsed config. Missing file is a fatal error.
func Load(path string) (*Config, error) {
	bytes, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %q: %w", path, err)
	}
	var cfg Config
	if err := toml.Unmarshal(bytes, &cfg); err != nil {
		return nil, fmt.Errorf("parse config %q: %w", path, err)
	}
	applyDefaults(&cfg)
	return &cfg, nil
}

func applyDefaults(c *Config) {
	if c.Flash.Root == "" {
		c.Flash.Root = defaultFlashRoot()
	}
	if c.Storage.Mode == "" {
		c.Storage.Mode = "flash_cache"
	}
	if c.Storage.OriginalsCacheDays == 0 {
		c.Storage.OriginalsCacheDays = 30
	}
	if c.Storage.OriginalsCacheMaxMedia == 0 {
		c.Storage.OriginalsCacheMaxMedia = 100_000
	}
	if !c.Storage.ThumbsCacheEnabled {
		c.Storage.ThumbsCacheEnabled = true
	}
	if c.Identity.Mode == "" {
		c.Identity.Mode = "stub"
	}
	if c.Identity.Stub.Hub == "" {
		c.Identity.Stub.Hub = "dev-local"
	}
	if c.Identity.Stub.UserID == "" {
		c.Identity.Stub.UserID = "owner"
	}
	if c.Identity.Stub.Handle == "" {
		c.Identity.Stub.Handle = "owner"
	}
	if c.Identity.Header.UserIDHeader == "" {
		c.Identity.Header.UserIDHeader = "X-Auth-User-Id"
	}
	if c.Identity.Header.HubHeader == "" {
		c.Identity.Header.HubHeader = "X-Auth-Hub"
	}
	if c.Identity.Header.HandleHeader == "" {
		c.Identity.Header.HandleHeader = "X-Auth-Handle"
	}
	if c.Identity.Header.ScopesHeader == "" {
		c.Identity.Header.ScopesHeader = "X-Auth-Scopes"
	}
	if c.Identity.Header.RequestIDHeader == "" {
		c.Identity.Header.RequestIDHeader = "X-Auth-Request-Id"
	}
	if c.HTTP.ListenAddress == "" {
		c.HTTP.ListenAddress = "127.0.0.1:8090"
	}
	if c.HTTP.RequestTimeout == 0 {
		c.HTTP.RequestTimeout = 30 * time.Second
	}
	if c.HTTP.WriteTimeout == 0 {
		c.HTTP.WriteTimeout = 60 * time.Second
	}
	if c.Imports.ConcurrentWorkers == 0 {
		c.Imports.ConcurrentWorkers = 2
	}
	if c.Thumbs.WorkerConcurrency == 0 {
		c.Thumbs.WorkerConcurrency = 4
	}
	if c.Thumbs.PollInterval == 0 {
		c.Thumbs.PollInterval = 5 * time.Second
	}
	if c.Thumbs.LeaseTimeout == 0 {
		c.Thumbs.LeaseTimeout = 10 * time.Minute
	}
	if c.Broker.Mode == "" {
		c.Broker.Mode = "stub"
	}
	if c.Backup.Keep15Min == 0 {
		c.Backup.Keep15Min = 4
	}
	if c.Backup.KeepHourly == 0 {
		c.Backup.KeepHourly = 24
	}
	if c.Backup.KeepDaily == 0 {
		c.Backup.KeepDaily = 7
	}
}

func defaultFlashRoot() string {
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, "fotobank")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".local", "state", "fotobank")
	}
	return "./.fotobank-state"
}
```

- [ ] **Step 5: Run, verify pass**

```bash
go test ./internal/config/... -shuffle=on
```

- [ ] **Step 6: Commit**

```bash
git add internal/config/ testdata/config/minimal.toml
git commit -m "Add TOML config loader with defaults"
```

---

### Task 11: Config validation

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Create: `testdata/config/missing-nas.toml`
- Create: `testdata/config/header-no-guard.toml`

- [ ] **Step 1: Write fixtures for invalid configs**

```toml
# testdata/config/missing-nas.toml
# (intentionally empty to exercise required-field check)
```

```toml
# testdata/config/header-no-guard.toml
[nas]
root = "/tmp/nas"

[identity]
mode = "header"

[http]
listen_address = "0.0.0.0:8090"   # not loopback/UDS → guard must be configured elsewhere
```

- [ ] **Step 2: Add failing tests**

```go
func TestValidateRequiresNASRoot(t *testing.T) {
	_, err := config.Load(filepath.Join("..", "..", "testdata", "config", "missing-nas.toml"))
	require.Error(t, err)
	require.ErrorIs(t, err, errs.ErrBadConfiguration)
}

func TestValidateHeaderModeRequiresGuard(t *testing.T) {
	_, err := config.Load(filepath.Join("..", "..", "testdata", "config", "header-no-guard.toml"))
	require.Error(t, err)
	require.ErrorIs(t, err, errs.ErrBadConfiguration)
}

func TestValidateAcceptsLoopbackInHeaderMode(t *testing.T) {
	// Loopback bind satisfies the guard on its own.
	tmp := t.TempDir()
	p := filepath.Join(tmp, "c.toml")
	err := os.WriteFile(p, []byte(`
[nas]
root = "/tmp/nas"
[identity]
mode = "header"
[http]
listen_address = "127.0.0.1:8090"
`), 0o600)
	require.NoError(t, err)
	_, err = config.Load(p)
	require.NoError(t, err)
}
```

- [ ] **Step 3: Run, verify fail**

- [ ] **Step 4: Implement `Validate` and invoke it from `Load`**

Add at end of `Load`:

```go
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
```

Add method:

```go
func (c *Config) Validate() error {
	if c.NAS.Root == "" {
		return fmt.Errorf("%w: [nas].root is required", errs.ErrBadConfiguration)
	}
	switch c.Identity.Mode {
	case "stub":
		// nothing extra
	case "header":
		if err := c.validateHeaderGuard(); err != nil {
			return err
		}
	default:
		return fmt.Errorf("%w: [identity].mode=%q (must be stub|header)", errs.ErrBadConfiguration, c.Identity.Mode)
	}
	switch c.Storage.Mode {
	case "nas_only", "flash_cache":
	default:
		return fmt.Errorf("%w: [storage].mode=%q", errs.ErrBadConfiguration, c.Storage.Mode)
	}
	switch c.Broker.Mode {
	case "stub", "exec":
	default:
		return fmt.Errorf("%w: [broker].mode=%q", errs.ErrBadConfiguration, c.Broker.Mode)
	}
	return nil
}

func (c *Config) validateHeaderGuard() error {
	h := c.Identity.Header
	if isLoopbackBind(c.HTTP.ListenAddress) ||
		len(h.TrustedProxyCIDRs) > 0 ||
		(h.ProxySecretHeader != "" && (h.ProxySecret != "" || os.Getenv("FOTOBANK_PROXY_SECRET") != "")) ||
		h.ProxyMTLSCAFile != "" {
		return nil
	}
	return fmt.Errorf("%w: [identity].mode=header requires loopback bind, trusted_proxy_cidrs, proxy_secret, or mtls", errs.ErrBadConfiguration)
}

func isLoopbackBind(addr string) bool {
	if strings.HasPrefix(addr, "unix:") {
		return true
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
```

Add imports: `errors` (already via errs), `net`, `strings`, `github.com/wesm/fotobank/internal/errs`.

- [ ] **Step 5: Run, verify pass; check `go vet` and lint**

```bash
go test ./internal/config/... -shuffle=on
make vet lint
```

- [ ] **Step 6: Commit**

```bash
git add internal/config/ testdata/config/
git commit -m "Validate config; header mode requires guard"
```

---

### Task 12: Config file location + first-run scaffold

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Create: `internal/config/config.example.toml`  ← package-local so `//go:embed` works without traversing parents

- [ ] **Step 1: Write `internal/config/config.example.toml`**

Copy the full TOML schema from spec §4.1 verbatim (with all comments). The file lives inside the `internal/config/` package directory so the later `//go:embed` pattern (Step 4) does not need to traverse parent directories — Go's embed system rejects patterns containing `..`.

- [ ] **Step 2: Write failing tests**

```go
func TestDefaultConfigPathHonoursXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg")
	t.Setenv("FOTOBANK_CONFIG", "")
	require.Equal(t, "/tmp/xdg/fotobank/config.toml", config.DefaultConfigPath())
}

func TestDefaultConfigPathHonoursEnvOverride(t *testing.T) {
	t.Setenv("FOTOBANK_CONFIG", "/custom/c.toml")
	require.Equal(t, "/custom/c.toml", config.DefaultConfigPath())
}

func TestEnsureDefaultWritesExampleOnFirstRun(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.toml")

	scaffolded, err := config.EnsureDefault(path)
	require.NoError(t, err)
	require.True(t, scaffolded)

	// File now exists; loading should succeed.
	_, err = os.Stat(path)
	require.NoError(t, err)

	// Second run is a no-op.
	scaffolded, err = config.EnsureDefault(path)
	require.NoError(t, err)
	require.False(t, scaffolded)
}
```

- [ ] **Step 3: Run, verify fail**

- [ ] **Step 4: Implement**

Embed the example config (file lives at `internal/config/config.example.toml`):

```go
import _ "embed"

//go:embed config.example.toml
var exampleTOML []byte

func DefaultConfigPath() string {
	if v := os.Getenv("FOTOBANK_CONFIG"); v != "" {
		return v
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "fotobank", "config.toml")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".config", "fotobank", "config.toml")
	}
	return "./config.toml"
}

// EnsureDefault writes the example config if path does not exist.
// Returns (scaffolded=true) when it wrote a new file.
func EnsureDefault(path string) (bool, error) {
	if _, err := os.Stat(path); err == nil {
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("stat %q: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, fmt.Errorf("mkdir %q: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, exampleTOML, 0o600); err != nil {
		return false, fmt.Errorf("write %q: %w", path, err)
	}
	return true, nil
}
```

The embed pattern is package-relative: the canonical source lives at `internal/config/config.example.toml`. Go's `//go:embed` rejects `..` path traversal, so we never embed across the package boundary.

- [ ] **Step 5: Run, verify pass**

- [ ] **Step 6: Commit**

```bash
git add internal/config/ config.example.toml
git commit -m "Add config default path + first-run scaffold"
```

---

### Task 13: Environment variable overrides

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

- [ ] **Step 1: Write failing test**

```go
func TestProxySecretFromEnvIfUnset(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "c.toml")
	require.NoError(t, os.WriteFile(p, []byte(`
[nas]
root = "/tmp/nas"
[identity]
mode = "header"
[identity.header]
proxy_secret_header = "X-Foo"
[http]
listen_address = "0.0.0.0:9090"
`), 0o600))

	t.Setenv("FOTOBANK_PROXY_SECRET", "s3cret")
	cfg, err := config.Load(p)
	require.NoError(t, err)
	require.Equal(t, "s3cret", cfg.Identity.Header.ProxySecret)
}
```

- [ ] **Step 2: Run, verify fail**

- [ ] **Step 3: Implement**

In `applyDefaults`, after TOML load, layer env overrides:

```go
if c.Identity.Header.ProxySecret == "" {
    c.Identity.Header.ProxySecret = os.Getenv("FOTOBANK_PROXY_SECRET")
}
if v := os.Getenv("FOTOBANK_DEV_HUB"); v != "" {
    c.Identity.Stub.Hub = v
}
if v := os.Getenv("FOTOBANK_DEV_USER_ID"); v != "" {
    c.Identity.Stub.UserID = v
}
if v := os.Getenv("FOTOBANK_DEV_HANDLE"); v != "" {
    c.Identity.Stub.Handle = v
}
```

- [ ] **Step 4: Run, verify pass**

- [ ] **Step 5: Commit**

```bash
git add internal/config/
git commit -m "Layer env-var overrides onto config"
```

---

## Part 4 — Database and schema

### Task 14: Database open + pools + WAL + Tx helper

**Files:**
- Create: `internal/db/db.go`
- Create: `internal/db/db_test.go`
- Create: `internal/testutil/testdb.go`

- [ ] **Step 1: Write the failing test**

```go
package db_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wesm/fotobank/internal/db"
)

func TestOpenEnablesWALAndReturnsBothPools(t *testing.T) {
	r := require.New(t)
	d, err := db.Open(filepath.Join(t.TempDir(), "test.sqlite"))
	r.NoError(err)
	defer d.Close()

	var mode string
	row := d.ReadDB().QueryRow("PRAGMA journal_mode")
	r.NoError(row.Scan(&mode))
	r.Equal("wal", mode)

	r.NotNil(d.WriteDB())
	r.NotNil(d.ReadDB())
}

func TestTxCommitsOnNilError(t *testing.T) {
	r := require.New(t)
	d, err := db.Open(filepath.Join(t.TempDir(), "c.sqlite"))
	r.NoError(err)
	defer d.Close()

	r.NoError(d.Tx(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`CREATE TABLE t (x INTEGER)`)
		return err
	}))

	var n int
	r.NoError(d.ReadDB().QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE name='t'`,
	).Scan(&n))
	r.Equal(1, n)
}

func TestTxRollsBackOnError(t *testing.T) {
	r := require.New(t)
	d, err := db.Open(filepath.Join(t.TempDir(), "r.sqlite"))
	r.NoError(err)
	defer d.Close()

	_, err = d.WriteDB().Exec(`CREATE TABLE t (x INTEGER)`)
	r.NoError(err)

	boom := errors.New("boom")
	r.ErrorIs(d.Tx(context.Background(), func(tx *sql.Tx) error {
		_, _ = tx.Exec(`INSERT INTO t VALUES (1)`)
		return boom
	}), boom)

	var n int
	r.NoError(d.ReadDB().QueryRow(`SELECT COUNT(*) FROM t`).Scan(&n))
	r.Equal(0, n)
}
```

- [ ] **Step 2: Run, verify fail**

- [ ] **Step 3: Implement (port reference-project's `internal/db/db.go`, minus legacy handling)**

```go
// internal/db/db.go
package db

import (
	"context"
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

type DB struct {
	rw *sql.DB
	ro *sql.DB
}

func Open(path string) (*DB, error) {
	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	rw, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	rw.SetMaxOpenConns(1)

	ro, err := sql.Open("sqlite", dsn)
	if err != nil {
		rw.Close()
		return nil, fmt.Errorf("open db ro: %w", err)
	}
	ro.SetMaxOpenConns(4)

	d := &DB{rw: rw, ro: ro}
	if err := d.init(); err != nil {
		d.Close()
		return nil, err
	}
	return d, nil
}

func (d *DB) init() error {
	if _, err := d.rw.Exec("PRAGMA journal_mode=WAL"); err != nil {
		return fmt.Errorf("enable WAL: %w", err)
	}
	return nil
}

func (d *DB) Close() error {
	d.ro.Close()
	return d.rw.Close()
}

func (d *DB) ReadDB() *sql.DB  { return d.ro }
func (d *DB) WriteDB() *sql.DB { return d.rw }

func (d *DB) Tx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := d.rw.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}
```

- [ ] **Step 4: Add a test helper**

```go
// internal/testutil/testdb.go
package testutil

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wesm/fotobank/internal/db"
)

// OpenTestDB returns an opened DB backed by a fresh file in t.TempDir().
// Caller should not need to close; cleanup runs automatically.
func OpenTestDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "fotobank.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })
	return d
}
```

- [ ] **Step 5: Run, verify pass**

```bash
go test ./internal/db/... ./internal/testutil/... -shuffle=on
```

- [ ] **Step 6: Commit**

```bash
git add internal/db/ internal/testutil/
git commit -m "Open SQLite with RW/RO pools, WAL, and Tx helper"
```

---

### Task 15: Embedded migrations runner + initial schema

**Files:**
- Create: `internal/db/migrations.go`
- Create: `internal/db/migrations_test.go`
- Create: `internal/db/migrations/000001_initial_schema.up.sql`
- Create: `internal/db/migrations/000001_initial_schema.down.sql`

> **Why runner + first migration in the same task?** Go's `//go:embed migrations/*.sql` fails to compile if the pattern matches zero files. Keeping a `.gitkeep` in the directory doesn't help because `.gitkeep` doesn't match `*.sql`. The cleanest resolution is to land the first real migration alongside the runner so the embed always has content.

- [ ] **Step 1: Write the initial schema SQL** — `internal/db/migrations/000001_initial_schema.up.sql`

Copy the entire DDL block from spec §6.4 into `000001_initial_schema.up.sql` — `owners`, `principal_display`, `media` (with `UNIQUE(owner_hub, owner_user_id, checksum)`, `UNIQUE(owner_hub, owner_user_id, path)`, `thumb_version`, `thumb_updated_at`, all three `media_*_idx` indexes), `albums`, `album_media` + both consistency triggers, `scopes` + indexes, `scope_media` + both consistency triggers, and the two `scopes_target_album_owner_consistency_*` triggers.

Down migration at `000001_initial_schema.down.sql`:

```sql
DROP TRIGGER IF EXISTS scopes_target_album_owner_consistency_update;
DROP TRIGGER IF EXISTS scopes_target_album_owner_consistency_insert;
DROP TRIGGER IF EXISTS scope_media_owner_consistency_update;
DROP TRIGGER IF EXISTS scope_media_owner_consistency_insert;
DROP TABLE IF EXISTS scope_media;
DROP TABLE IF EXISTS scopes;
DROP TRIGGER IF EXISTS album_media_owner_consistency_update;
DROP TRIGGER IF EXISTS album_media_owner_consistency_insert;
DROP TABLE IF EXISTS album_media;
DROP TABLE IF EXISTS albums;
DROP TABLE IF EXISTS media;
DROP TABLE IF EXISTS principal_display;
DROP TABLE IF EXISTS owners;
```

- [ ] **Step 2: Write failing tests**

```go
package db_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wesm/fotobank/internal/db"
)

func TestInitialSchemaCreatesAllTables(t *testing.T) {
	r := require.New(t)
	d, err := db.Open(filepath.Join(t.TempDir(), "s.sqlite"))
	r.NoError(err)
	defer d.Close()

	tables := []string{
		"owners", "principal_display", "media",
		"albums", "album_media",
		"scopes", "scope_media",
	}
	for _, name := range tables {
		var count int
		r.NoError(d.ReadDB().QueryRow(
			`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, name,
		).Scan(&count))
		r.Equal(1, count, "table %q missing", name)
	}
}

func TestAlbumMediaOwnerConsistencyTrigger(t *testing.T) {
	r := require.New(t)
	d, err := db.Open(filepath.Join(t.TempDir(), "t.sqlite"))
	r.NoError(err)
	defer d.Close()

	rw := d.WriteDB()
	mustExec := func(q string, args ...any) { _, err := rw.Exec(q, args...); r.NoError(err) }
	mustExec(`INSERT INTO owners VALUES('h1','u1','k1','u1',datetime('now'))`)
	mustExec(`INSERT INTO owners VALUES('h2','u2','k2','u2',datetime('now'))`)
	mustExec(`INSERT INTO albums (id,owner_hub,owner_user_id,name,created_at,updated_at)
	          VALUES('aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa','h1','u1','a',datetime('now'),datetime('now'))`)
	mustExec(`INSERT INTO media (id,owner_hub,owner_user_id,media_type,mime_type,path,imported_at,size,checksum,thumb_status,thumb_version,thumb_updated_at)
	          VALUES('bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb','h2','u2','photo','image/jpeg','a.jpg',datetime('now'),1,'cs','pending',1,datetime('now'))`)

	_, err = rw.Exec(
		`INSERT INTO album_media VALUES('aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa','bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb',datetime('now'),NULL)`,
	)
	r.Error(err)
	r.Contains(err.Error(), "album and media must share owner")
}
```

- [ ] **Step 3: Implement runMigrations**

Port reference-project's `internal/db/migrations.go`, simplified: no legacy-schema-version handling, no `reconcileWorkspaceSetupMigrationVersion10`. Keep the iofs+migrate driver wiring, `latestMigrationVersion()` helper, dirty-state check, newer-than-binary check.

```go
// internal/db/migrations.go
package db

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strconv"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	migratedb "github.com/golang-migrate/migrate/v4/database"
	migratesqlite "github.com/golang-migrate/migrate/v4/database/sqlite"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

const migrationTableName = "schema_migrations"

//go:embed migrations/*.sql
var migrationFiles embed.FS

func runMigrations(rw *sql.DB) error {
	sourceDriver, err := iofs.New(migrationFiles, "migrations")
	if err != nil {
		return fmt.Errorf("load embedded migrations: %w", err)
	}

	databaseDriver, err := migratesqlite.WithInstance(rw, &migratesqlite.Config{
		MigrationsTable: migrationTableName,
	})
	if err != nil {
		return fmt.Errorf("open migration driver: %w", err)
	}

	version, dirty, err := databaseDriver.Version()
	if err != nil {
		return fmt.Errorf("read migration version: %w", err)
	}
	if dirty {
		return fmt.Errorf("database is in a dirty migration state")
	}

	latest, err := latestMigrationVersion()
	if err != nil {
		return fmt.Errorf("read embedded migration versions: %w", err)
	}
	if version != migratedb.NilVersion && version > latest {
		return fmt.Errorf(
			"schema version %d is newer than this binary (expects %d); upgrade fotobank",
			version, latest,
		)
	}

	m, err := migrate.NewWithInstance("iofs", sourceDriver, "sqlite", databaseDriver)
	if err != nil {
		return fmt.Errorf("create migrator: %w", err)
	}
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}

func latestMigrationVersion() (int, error) {
	files, err := fs.Glob(migrationFiles, "migrations/*.up.sql")
	if err != nil {
		return 0, err
	}
	latest := migratedb.NilVersion
	for _, file := range files {
		name := path.Base(file)
		prefix := strings.TrimSuffix(name, ".up.sql")
		versionText, _, found := strings.Cut(prefix, "_")
		if !found {
			return 0, fmt.Errorf("parse migration version from %q", name)
		}
		v, err := strconv.Atoi(versionText)
		if err != nil {
			return 0, fmt.Errorf("parse migration version from %q: %w", name, err)
		}
		if v > latest {
			latest = v
		}
	}
	if latest == migratedb.NilVersion {
		return 0, nil // no migrations yet is valid
	}
	return latest, nil
}
```

Wire `runMigrations` into `db.init()` (after WAL pragma):

```go
func (d *DB) init() error {
	if _, err := d.rw.Exec("PRAGMA journal_mode=WAL"); err != nil {
		return fmt.Errorf("enable WAL: %w", err)
	}
	if err := runMigrations(d.rw); err != nil {
		return err
	}
	return nil
}
```

- [ ] **Step 4: Run, verify pass**

```bash
go test ./internal/db/... -shuffle=on
```

- [ ] **Step 5: Commit**

```bash
git add internal/db/migrations.go internal/db/migrations_test.go internal/db/migrations/000001_initial_schema.up.sql internal/db/migrations/000001_initial_schema.down.sql
git commit -m "Add embedded migrations runner + initial schema (all Phase 1 tables + triggers)"
```

---

### Task 16: *(merged into Task 15)*

The initial schema migration was folded into Task 15 to avoid an invalid empty `//go:embed migrations/*.sql` compile state between the runner landing and the first migration landing. Task numbers 17+ are preserved for continuity with prior task references.

---

## Part 5 — Owners service

### Task 17: owners package types and repo

**Files:**
- Create: `internal/owners/owners.go`
- Create: `internal/owners/repo.go`
- Create: `internal/owners/repo_test.go`

- [ ] **Step 1: Write types**

```go
// internal/owners/owners.go
package owners

import "time"

type Principal struct {
	Hub    string
	UserID string
}

func (p Principal) String() string { return p.Hub + ":" + p.UserID }
func (p Principal) IsZero() bool   { return p.Hub == "" && p.UserID == "" }

type Owner struct {
	Principal     Principal
	StorageKey    string
	DisplayHandle string
	CreatedAt     time.Time
}
```

- [ ] **Step 2: Write failing repo tests**

```go
package owners_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/testutil"
)

func TestInsertThenGet(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := owners.NewRepo(d.WriteDB(), d.ReadDB())

	o := owners.Owner{
		Principal:     owners.Principal{Hub: "h", UserID: "u"},
		StorageKey:    "key1",
		DisplayHandle: "User",
		CreatedAt:     time.Now().UTC(),
	}
	r.NoError(repo.Insert(context.Background(), o))

	got, err := repo.GetByPrincipal(context.Background(), o.Principal)
	r.NoError(err)
	r.Equal(o.StorageKey, got.StorageKey)
	r.Equal(o.DisplayHandle, got.DisplayHandle)
}

func TestInsertDuplicatePrincipalFails(t *testing.T) {
	d := testutil.OpenTestDB(t)
	repo := owners.NewRepo(d.WriteDB(), d.ReadDB())

	o := owners.Owner{
		Principal: owners.Principal{Hub: "h", UserID: "u"}, StorageKey: "k",
		CreatedAt: time.Now().UTC(),
	}
	require.NoError(t, repo.Insert(context.Background(), o))
	require.Error(t, repo.Insert(context.Background(), o))
}

func TestInsertDuplicateStorageKeyFails(t *testing.T) {
	d := testutil.OpenTestDB(t)
	repo := owners.NewRepo(d.WriteDB(), d.ReadDB())
	now := time.Now().UTC()
	require.NoError(t, repo.Insert(context.Background(),
		owners.Owner{Principal: owners.Principal{Hub: "h", UserID: "u1"}, StorageKey: "k", CreatedAt: now}))
	require.Error(t, repo.Insert(context.Background(),
		owners.Owner{Principal: owners.Principal{Hub: "h", UserID: "u2"}, StorageKey: "k", CreatedAt: now}))
}

func TestListReturnsAll(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := owners.NewRepo(d.WriteDB(), d.ReadDB())
	now := time.Now().UTC()
	for i := 0; i < 3; i++ {
		r.NoError(repo.Insert(context.Background(), owners.Owner{
			Principal: owners.Principal{Hub: "h", UserID: fmt.Sprintf("u%d", i)},
			StorageKey: fmt.Sprintf("k%d", i),
			CreatedAt: now,
		}))
	}
	all, err := repo.List(context.Background())
	r.NoError(err)
	r.Len(all, 3)
}

func TestDeleteRemoves(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := owners.NewRepo(d.WriteDB(), d.ReadDB())
	p := owners.Principal{Hub: "h", UserID: "u"}
	r.NoError(repo.Insert(context.Background(),
		owners.Owner{Principal: p, StorageKey: "k", CreatedAt: time.Now().UTC()}))
	r.NoError(repo.Delete(context.Background(), p))
	_, err := repo.GetByPrincipal(context.Background(), p)
	r.ErrorIs(err, errs.ErrNotFound)
}
```

- [ ] **Step 3: Run, verify fail**

- [ ] **Step 4: Implement the repo**

```go
// internal/owners/repo.go
package owners

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/wesm/fotobank/internal/errs"
)

type Repo struct {
	rw *sql.DB
	ro *sql.DB
}

func NewRepo(rw, ro *sql.DB) *Repo { return &Repo{rw: rw, ro: ro} }

func (r *Repo) Insert(ctx context.Context, o Owner) error {
	_, err := r.rw.ExecContext(ctx,
		`INSERT INTO owners (hub, user_id, storage_key, display_handle, created_at)
		 VALUES (?, ?, ?, ?, ?)`,
		o.Principal.Hub, o.Principal.UserID, o.StorageKey, nullIfEmpty(o.DisplayHandle), o.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert owner: %w", err)
	}
	return nil
}

func (r *Repo) GetByPrincipal(ctx context.Context, p Principal) (Owner, error) {
	var o Owner
	var handle sql.NullString
	err := r.ro.QueryRowContext(ctx,
		`SELECT hub, user_id, storage_key, display_handle, created_at
		   FROM owners WHERE hub = ? AND user_id = ?`,
		p.Hub, p.UserID,
	).Scan(&o.Principal.Hub, &o.Principal.UserID, &o.StorageKey, &handle, &o.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Owner{}, errs.ErrNotFound
	}
	if err != nil {
		return Owner{}, fmt.Errorf("get owner: %w", err)
	}
	o.DisplayHandle = handle.String
	return o, nil
}

func (r *Repo) List(ctx context.Context) ([]Owner, error) {
	rows, err := r.ro.QueryContext(ctx,
		`SELECT hub, user_id, storage_key, display_handle, created_at FROM owners ORDER BY hub, user_id`,
	)
	if err != nil {
		return nil, fmt.Errorf("list owners: %w", err)
	}
	defer rows.Close()
	var out []Owner
	for rows.Next() {
		var o Owner
		var handle sql.NullString
		if err := rows.Scan(&o.Principal.Hub, &o.Principal.UserID, &o.StorageKey, &handle, &o.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan owner: %w", err)
		}
		o.DisplayHandle = handle.String
		out = append(out, o)
	}
	return out, rows.Err()
}

func (r *Repo) Delete(ctx context.Context, p Principal) error {
	res, err := r.rw.ExecContext(ctx,
		`DELETE FROM owners WHERE hub = ? AND user_id = ?`, p.Hub, p.UserID,
	)
	if err != nil {
		return fmt.Errorf("delete owner: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errs.ErrNotFound
	}
	return nil
}

func (r *Repo) UpdateDisplayHandle(ctx context.Context, p Principal, handle string) error {
	res, err := r.rw.ExecContext(ctx,
		`UPDATE owners SET display_handle = ? WHERE hub = ? AND user_id = ?`,
		nullIfEmpty(handle), p.Hub, p.UserID,
	)
	if err != nil {
		return fmt.Errorf("update owner display handle: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errs.ErrNotFound
	}
	return nil
}

func nullIfEmpty(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}
```

- [ ] **Step 5: Run, verify pass**

- [ ] **Step 6: Commit**

```bash
git add internal/owners/
git commit -m "Add owners types and SQLite repo"
```

---

### Task 18: OwnerService

**Files:**
- Create: `internal/service/owner_service.go`
- Create: `internal/service/owner_service_test.go`

- [ ] **Step 1: Write failing tests**

```go
package service_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wesm/fotobank/internal/errs"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/service"
	"github.com/wesm/fotobank/internal/testutil"
)

func TestEnsureIsIdempotent(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	svc := service.NewOwnerService(owners.NewRepo(d.WriteDB(), d.ReadDB()))

	p := owners.Principal{Hub: "h", UserID: "u"}
	r.NoError(svc.Ensure(context.Background(), p, "k"))
	r.NoError(svc.Ensure(context.Background(), p, "k")) // second call no-op
}

func TestEnsureConflictingStorageKeyErrors(t *testing.T) {
	d := testutil.OpenTestDB(t)
	svc := service.NewOwnerService(owners.NewRepo(d.WriteDB(), d.ReadDB()))

	p := owners.Principal{Hub: "h", UserID: "u"}
	require.NoError(t, svc.Ensure(context.Background(), p, "k1"))
	err := svc.Ensure(context.Background(), p, "k2")
	require.ErrorIs(t, err, errs.ErrAlreadyExists)
}

func TestRemoveRefusesWhenMediaExists(t *testing.T) {
	// Plan A: Remove(purge=false) must succeed when no media rows reference the owner.
	// Plan B adds the "with media" case and purge behaviour. Here we
	// cover the happy case plus a direct media-row insert to exercise
	// the refusal path.
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	svc := service.NewOwnerService(owners.NewRepo(d.WriteDB(), d.ReadDB()))
	p := owners.Principal{Hub: "h", UserID: "u"}
	r.NoError(svc.Ensure(context.Background(), p, "k"))

	// Insert a raw media row for this owner.
	_, err := d.WriteDB().Exec(`
		INSERT INTO media (id, owner_hub, owner_user_id, media_type, mime_type, path,
		                   imported_at, size, checksum, thumb_status, thumb_version, thumb_updated_at)
		VALUES ('c0000000-0000-0000-0000-000000000001', 'h', 'u', 'photo', 'image/jpeg',
		        'x.jpg', datetime('now'), 1, 'cs', 'pending', 1, datetime('now'))`)
	r.NoError(err)

	r.ErrorIs(svc.Remove(context.Background(), p, false), errs.ErrInvalidArgument)
}

func TestRemoveSucceedsWhenEmpty(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	svc := service.NewOwnerService(owners.NewRepo(d.WriteDB(), d.ReadDB()))
	p := owners.Principal{Hub: "h", UserID: "u"}
	r.NoError(svc.Ensure(context.Background(), p, "k"))
	r.NoError(svc.Remove(context.Background(), p, false))
}
```

- [ ] **Step 2: Run, verify fail**

- [ ] **Step 3: Implement**

```go
// internal/service/owner_service.go
package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/wesm/fotobank/internal/errs"
	"github.com/wesm/fotobank/internal/owners"
)

type OwnerService struct {
	repo *owners.Repo
	now  func() time.Time
}

func NewOwnerService(repo *owners.Repo) *OwnerService {
	return &OwnerService{repo: repo, now: func() time.Time { return time.Now().UTC() }}
}

// Ensure inserts an owners row if missing; no-op if the same
// principal+storage_key already exists; returns ErrAlreadyExists if
// the principal exists with a different storage_key.
func (s *OwnerService) Ensure(ctx context.Context, p owners.Principal, storageKey string) error {
	existing, err := s.repo.GetByPrincipal(ctx, p)
	switch {
	case err == nil:
		if existing.StorageKey != storageKey {
			return fmt.Errorf("%w: owner %s has storage_key %q, got %q",
				errs.ErrAlreadyExists, p, existing.StorageKey, storageKey)
		}
		return nil
	case errors.Is(err, errs.ErrNotFound):
		return s.repo.Insert(ctx, owners.Owner{
			Principal: p, StorageKey: storageKey, CreatedAt: s.now(),
		})
	default:
		return err
	}
}

func (s *OwnerService) List(ctx context.Context) ([]owners.Owner, error) {
	return s.repo.List(ctx)
}

func (s *OwnerService) Remove(ctx context.Context, p owners.Principal, purge bool) error {
	if !purge {
		// Refuse if any media rows exist for this owner.
		var n int
		row := s.repo.DB().QueryRowContext(ctx,
			`SELECT COUNT(*) FROM media WHERE owner_hub=? AND owner_user_id=?`, p.Hub, p.UserID)
		if err := row.Scan(&n); err != nil {
			return fmt.Errorf("count media for owner: %w", err)
		}
		if n > 0 {
			return fmt.Errorf("%w: owner %s has %d media rows (use --purge)",
				errs.ErrInvalidArgument, p, n)
		}
	} else {
		return fmt.Errorf("%w: --purge requires the storage layer (Plan B)", errs.ErrInvalidArgument)
	}
	return s.repo.Delete(ctx, p)
}

func (s *OwnerService) UpdateDisplay(ctx context.Context, p owners.Principal, handle string) error {
	return s.repo.UpdateDisplayHandle(ctx, p, handle)
}

// Note: --purge needs storage-layer access to delete bytes; it is
// explicitly rejected in Plan A and unlocked in Plan B. sql.DB access
// is exposed via repo.DB() for the count query.
var _ = sql.ErrNoRows // keep import used
```

Extend `internal/owners/repo.go` with:

```go
// DB returns the RO handle for callers that need ad-hoc queries
// spanning owners + other tables (e.g., "count media for owner").
func (r *Repo) DB() *sql.DB { return r.ro }
```

- [ ] **Step 4: Run, verify pass**

- [ ] **Step 5: Commit**

```bash
git add internal/service/ internal/owners/repo.go
git commit -m "Add OwnerService (Ensure, List, Remove, UpdateDisplay)"
```

---

## Part 6 — Identity

### Task 19: Provider interface, Principal/Identity types, StubProvider

**Files:**
- Create: `internal/identity/identity.go`
- Create: `internal/identity/stub.go`
- Create: `internal/identity/stub_test.go`

- [ ] **Step 1: Write failing test**

```go
package identity_test

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wesm/fotobank/internal/identity"
	"github.com/wesm/fotobank/internal/owners"
)

func TestStubReturnsConfigured(t *testing.T) {
	sp := identity.NewStub(owners.Principal{Hub: "h", UserID: "u"}, "User")
	r := httptest.NewRequest("GET", "/x", nil)
	id, err := sp.FromRequest(context.Background(), r)
	require.NoError(t, err)
	require.Equal(t, "h", id.Principal.Hub)
	require.Equal(t, "u", id.Principal.UserID)
	require.Equal(t, "User", id.Principal.Handle)
	require.Empty(t, id.Scopes)
}
```

- [ ] **Step 2: Run, verify fail**

- [ ] **Step 3: Implement**

```go
// internal/identity/identity.go
package identity

import (
	"context"
	"net/http"

	"github.com/wesm/fotobank/internal/owners"
)

// Principal carries the display handle alongside the owners Principal.
type Principal struct {
	Hub    string
	UserID string
	Handle string
}

func (p Principal) OwnersPrincipal() owners.Principal {
	return owners.Principal{Hub: p.Hub, UserID: p.UserID}
}

type Identity struct {
	Principal Principal
	Scopes    []string
	RequestID string
}

type Provider interface {
	FromRequest(ctx context.Context, r *http.Request) (Identity, error)
}
```

```go
// internal/identity/stub.go
package identity

import (
	"context"
	"net/http"

	"github.com/wesm/fotobank/internal/owners"
)

type Stub struct {
	principal Principal
}

func NewStub(p owners.Principal, handle string) *Stub {
	return &Stub{principal: Principal{Hub: p.Hub, UserID: p.UserID, Handle: handle}}
}

func (s *Stub) FromRequest(_ context.Context, _ *http.Request) (Identity, error) {
	return Identity{Principal: s.principal}, nil
}
```

- [ ] **Step 4: Run, verify pass**

- [ ] **Step 5: Commit**

```bash
git add internal/identity/
git commit -m "Add identity Provider interface and StubProvider"
```

---

### Task 20: Direct-access guard

**Files:**
- Create: `internal/identity/guard.go`
- Create: `internal/identity/guard_test.go`

- [ ] **Step 1: Write failing tests (table-driven covers all four modes)**

```go
package identity_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wesm/fotobank/internal/identity"
)

func TestGuardAcceptsLoopbackBind(t *testing.T) {
	g := identity.NewGuard(identity.GuardConfig{ListenAddress: "127.0.0.1:8090"})
	require.NoError(t, g.Check(httptest.NewRequest("GET", "/", nil)))
}

func TestGuardAcceptsUDSBind(t *testing.T) {
	g := identity.NewGuard(identity.GuardConfig{ListenAddress: "unix:/tmp/x.sock"})
	require.NoError(t, g.Check(httptest.NewRequest("GET", "/", nil)))
}

func TestGuardRejectsPublicBindWithoutOtherChecks(t *testing.T) {
	g := identity.NewGuard(identity.GuardConfig{ListenAddress: "0.0.0.0:8090"})
	err := g.Check(httptest.NewRequest("GET", "/", nil))
	require.Error(t, err)
}

func TestGuardAllowsTrustedCIDR(t *testing.T) {
	g := identity.NewGuard(identity.GuardConfig{
		ListenAddress:    "0.0.0.0:8090",
		TrustedProxyCIDRs: []string{"10.0.0.0/24"},
	})
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.7:33445"
	require.NoError(t, g.Check(r))

	r.RemoteAddr = "8.8.8.8:33445"
	require.Error(t, g.Check(r))
}

func TestGuardChecksProxySecretConstantTime(t *testing.T) {
	g := identity.NewGuard(identity.GuardConfig{
		ListenAddress:     "0.0.0.0:8090",
		ProxySecretHeader: "X-Proxy-Secret",
		ProxySecret:       "topsecret",
	})
	r := httptest.NewRequest("GET", "/", nil)
	require.Error(t, g.Check(r))

	r.Header.Set("X-Proxy-Secret", "wrong")
	require.Error(t, g.Check(r))

	r.Header.Set("X-Proxy-Secret", "topsecret")
	require.NoError(t, g.Check(r))
}

func TestGuardAcceptsWhenMTLSConfigured(t *testing.T) {
	// With mTLS configured, the guard trusts the TLS layer to reject
	// unverified clients; Check returns nil.
	g := identity.NewGuard(identity.GuardConfig{
		ListenAddress:   "0.0.0.0:8090",
		ProxyMTLSCAFile: "/etc/ssl/ca.pem",
	})
	require.NoError(t, g.Check(httptest.NewRequest("GET", "/", nil)))
}

func TestGuardAdditiveChecks(t *testing.T) {
	// When CIDR + proxy secret both set, both must pass.
	g := identity.NewGuard(identity.GuardConfig{
		ListenAddress:     "0.0.0.0:8090",
		TrustedProxyCIDRs: []string{"10.0.0.0/24"},
		ProxySecretHeader: "X-Proxy-Secret",
		ProxySecret:       "t",
	})
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.1:1"
	require.Error(t, g.Check(r), "needs secret too")
	r.Header.Set("X-Proxy-Secret", "t")
	require.NoError(t, g.Check(r))
}

// Suppress unused import lint when no test body needs it.
var _ = strings.HasPrefix
var _ http.Handler = http.DefaultServeMux
```

- [ ] **Step 2: Run, verify fail**

- [ ] **Step 3: Implement**

```go
// internal/identity/guard.go
package identity

import (
	"crypto/subtle"
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/wesm/fotobank/internal/errs"
)

type GuardConfig struct {
	ListenAddress     string
	TrustedProxyCIDRs []string
	ProxySecretHeader string
	ProxySecret       string
	ProxyMTLSCAFile   string
}

type Guard struct {
	cfg     GuardConfig
	cidrs   []*net.IPNet
	mode    guardMode
}

type guardMode struct {
	loopbackBind bool
	cidrCheck    bool
	secretCheck  bool
	mtls         bool
}

func NewGuard(cfg GuardConfig) *Guard {
	var nets []*net.IPNet
	for _, c := range cfg.TrustedProxyCIDRs {
		if _, n, err := net.ParseCIDR(c); err == nil {
			nets = append(nets, n)
		}
	}
	return &Guard{
		cfg:   cfg,
		cidrs: nets,
		mode: guardMode{
			loopbackBind: isLoopbackOrUDS(cfg.ListenAddress),
			cidrCheck:    len(nets) > 0,
			secretCheck:  cfg.ProxySecretHeader != "" && cfg.ProxySecret != "",
			mtls:         cfg.ProxyMTLSCAFile != "",
		},
	}
}

func (g *Guard) Check(r *http.Request) error {
	// With loopback/UDS bind OR mTLS-at-server, the TLS/transport layer
	// already does the work — remote address is, by construction, trusted.
	if g.mode.loopbackBind || g.mode.mtls {
		// But: if cidrCheck or secretCheck are *also* configured, they
		// must pass too (additive).
	} else if !g.mode.cidrCheck && !g.mode.secretCheck {
		return fmt.Errorf("%w: no ingress check satisfied", errs.ErrDirectAccessBlocked)
	}

	if g.mode.cidrCheck {
		ip := remoteIP(r.RemoteAddr)
		if ip == nil || !anyContains(g.cidrs, ip) {
			return fmt.Errorf("%w: remote %s not in trusted CIDRs",
				errs.ErrDirectAccessBlocked, r.RemoteAddr)
		}
	}
	if g.mode.secretCheck {
		got := r.Header.Get(g.cfg.ProxySecretHeader)
		if subtle.ConstantTimeCompare([]byte(got), []byte(g.cfg.ProxySecret)) != 1 {
			return fmt.Errorf("%w: proxy secret mismatch", errs.ErrDirectAccessBlocked)
		}
	}
	return nil
}

func isLoopbackOrUDS(addr string) bool {
	if strings.HasPrefix(addr, "unix:") {
		return true
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func remoteIP(addr string) net.IP {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return net.ParseIP(addr)
	}
	return net.ParseIP(host)
}

func anyContains(nets []*net.IPNet, ip net.IP) bool {
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Run, verify pass**

```bash
go test ./internal/identity/... -shuffle=on
```

- [ ] **Step 5: Commit**

```bash
git add internal/identity/guard.go internal/identity/guard_test.go
git commit -m "Add direct-access guard (loopback/UDS, CIDR, proxy-secret, mTLS)"
```

---

### Task 21: HeaderProvider

**Files:**
- Create: `internal/identity/header.go`
- Create: `internal/identity/header_test.go`

- [ ] **Step 1: Write failing tests**

```go
func TestHeaderProviderReadsConfiguredHeaders(t *testing.T) {
	r := require.New(t)
	guard := identity.NewGuard(identity.GuardConfig{ListenAddress: "127.0.0.1:8090"})
	hp := identity.NewHeader(identity.HeaderConfig{
		UserIDHeader: "X-Auth-User-Id", HubHeader: "X-Auth-Hub",
		HandleHeader: "X-Auth-Handle", ScopesHeader: "X-Auth-Scopes",
		RequestIDHeader: "X-Auth-Request-Id",
	}, guard)

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Auth-Hub", "h")
	req.Header.Set("X-Auth-User-Id", "u")
	req.Header.Set("X-Auth-Handle", "User")
	req.Header.Set("X-Auth-Scopes", "s1 s2 s3")
	req.Header.Set("X-Auth-Request-Id", "rq-1")

	id, err := hp.FromRequest(context.Background(), req)
	r.NoError(err)
	r.Equal("h", id.Principal.Hub)
	r.Equal("u", id.Principal.UserID)
	r.Equal("User", id.Principal.Handle)
	r.Equal([]string{"s1", "s2", "s3"}, id.Scopes)
	r.Equal("rq-1", id.RequestID)
}

func TestHeaderProviderRequiresUserIDAndHub(t *testing.T) {
	hp := identity.NewHeader(identity.HeaderConfig{
		UserIDHeader: "X-Auth-User-Id", HubHeader: "X-Auth-Hub",
	}, identity.NewGuard(identity.GuardConfig{ListenAddress: "127.0.0.1:8090"}))

	req := httptest.NewRequest("GET", "/", nil)
	_, err := hp.FromRequest(context.Background(), req)
	require.ErrorIs(t, err, errs.ErrIdentityMissing)
}

func TestHeaderProviderCallsGuard(t *testing.T) {
	hp := identity.NewHeader(identity.HeaderConfig{
		UserIDHeader: "X-Auth-User-Id", HubHeader: "X-Auth-Hub",
	}, identity.NewGuard(identity.GuardConfig{ListenAddress: "0.0.0.0:8090"})) // no guard

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Auth-Hub", "h")
	req.Header.Set("X-Auth-User-Id", "u")
	_, err := hp.FromRequest(context.Background(), req)
	require.ErrorIs(t, err, errs.ErrDirectAccessBlocked)
}
```

- [ ] **Step 2: Run, verify fail**

- [ ] **Step 3: Implement**

```go
// internal/identity/header.go
package identity

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/wesm/fotobank/internal/errs"
)

type HeaderConfig struct {
	UserIDHeader    string
	HubHeader       string
	HandleHeader    string
	ScopesHeader    string
	RequestIDHeader string
}

type Header struct {
	cfg   HeaderConfig
	guard *Guard
}

func NewHeader(cfg HeaderConfig, guard *Guard) *Header {
	return &Header{cfg: cfg, guard: guard}
}

func (h *Header) FromRequest(_ context.Context, r *http.Request) (Identity, error) {
	if err := h.guard.Check(r); err != nil {
		return Identity{}, err
	}
	hub := r.Header.Get(h.cfg.HubHeader)
	uid := r.Header.Get(h.cfg.UserIDHeader)
	if hub == "" || uid == "" {
		return Identity{}, fmt.Errorf("%w: missing %s or %s", errs.ErrIdentityMissing,
			h.cfg.HubHeader, h.cfg.UserIDHeader)
	}
	return Identity{
		Principal: Principal{
			Hub: hub, UserID: uid, Handle: r.Header.Get(h.cfg.HandleHeader),
		},
		Scopes:    splitScopes(r.Header.Get(h.cfg.ScopesHeader)),
		RequestID: r.Header.Get(h.cfg.RequestIDHeader),
	}, nil
}

func splitScopes(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return strings.Fields(s)
}
```

- [ ] **Step 4: Run, verify pass; ensure mode=header config-validation test still passes**

```bash
go test ./internal/identity/... ./internal/config/... -shuffle=on
```

- [ ] **Step 5: Commit**

```bash
git add internal/identity/header.go internal/identity/header_test.go
git commit -m "Add HeaderProvider with guard integration"
```

---

### Task 22: LocalAdmin identity for CLI

**Files:**
- Create: `internal/identity/local_admin.go`
- Create: `internal/identity/local_admin_test.go`

- [ ] **Step 1: Write failing test**

```go
func TestLocalAdminSynthesizesRequestID(t *testing.T) {
	p := identity.Principal{Hub: "h", UserID: "u", Handle: "a"}
	id := identity.LocalAdmin(p)
	require.Equal(t, p, id.Principal)
	require.Empty(t, id.Scopes)
	require.Contains(t, id.RequestID, "cli-")
}
```

- [ ] **Step 2: Run, verify fail**

- [ ] **Step 3: Implement**

```go
// internal/identity/local_admin.go
package identity

import (
	"fmt"
	"os"
	"time"
)

func LocalAdmin(p Principal) Identity {
	return Identity{
		Principal: p,
		RequestID: fmt.Sprintf("cli-%d-%d", os.Getpid(), time.Now().UnixNano()),
	}
}
```

- [ ] **Step 4: Run, verify pass**

- [ ] **Step 5: Commit**

```bash
git add internal/identity/local_admin.go internal/identity/local_admin_test.go
git commit -m "Add CLI LocalAdmin identity synthesis"
```

---

## Part 7 — HTTP skeleton

### Task 23: httpapi package — huma setup and health

**Files:**
- Create: `internal/httpapi/api.go`
- Create: `internal/httpapi/api_test.go`

- [ ] **Step 1: Write failing test**

```go
package httpapi_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wesm/fotobank/internal/httpapi"
)

func TestHealthzReturnsOK(t *testing.T) {
	r := require.New(t)
	h, err := httpapi.New(httpapi.Deps{})
	r.NoError(err)

	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/healthz")
	r.NoError(err)
	defer resp.Body.Close()
	r.Equal(http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	r.NoError(err)
	var out map[string]any
	r.NoError(json.Unmarshal(body, &out))
	r.Equal("ok", out["status"])
}
```

- [ ] **Step 2: Run, verify fail**

- [ ] **Step 3: Implement (huma + humago)**

```go
// internal/httpapi/api.go
package httpapi

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"github.com/wesm/fotobank/internal/identity"
	"github.com/wesm/fotobank/internal/service"
	"github.com/wesm/fotobank/internal/version"
)

type Deps struct {
	IdentityProvider identity.Provider
	OwnerService     *service.OwnerService
}

// New wires the huma API onto a net/http mux and returns the handler.
func New(deps Deps) (http.Handler, error) {
	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("Fotobank", version.Short))
	api.OpenAPI().Info.Description = "Fotobank HTTP API"

	if err := registerHealthz(api); err != nil {
		return nil, err
	}
	// /me registered in Task 25.
	return mux, nil
}

type healthzOutput struct {
	Body struct {
		Status string `json:"status"`
	}
}

func registerHealthz(api huma.API) error {
	huma.Register(api, huma.Operation{
		OperationID: "healthz",
		Method:      http.MethodGet,
		Path:        "/api/v1/healthz",
		Summary:     "Health check",
	}, func(_ context.Context, _ *struct{}) (*healthzOutput, error) {
		out := &healthzOutput{}
		out.Body.Status = "ok"
		return out, nil
	})
	return nil
}
```

- [ ] **Step 4: Run, verify pass**

- [ ] **Step 5: Commit**

```bash
git add internal/httpapi/
git commit -m "Add huma + humago HTTP skeleton with /healthz"
```

---

### Task 24: httpapi middleware (identity + request id + slog)

**Files:**
- Create: `internal/httpapi/middleware.go`
- Create: `internal/httpapi/middleware_test.go`
- Modify: `internal/httpapi/api.go`

- [ ] **Step 1: Write failing test**

```go
func TestMiddlewareAttachesIdentityToContext(t *testing.T) {
	// Build a test server with the middleware wrapping a handler that
	// pulls identity from context and echoes principal.
	r := require.New(t)
	idp := identity.NewStub(owners.Principal{Hub: "h", UserID: "u"}, "User")

	var captured identity.Identity
	h := httpapi.WithMiddleware(idp)(http.HandlerFunc(func(_ http.ResponseWriter, req *http.Request) {
		captured, _ = httpapi.IdentityFromContext(req.Context())
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))

	r.Equal("h", captured.Principal.Hub)
	r.Equal("u", captured.Principal.UserID)
}

func TestMiddlewareGeneratesRequestIDIfAbsent(t *testing.T) {
	idp := identity.NewStub(owners.Principal{Hub: "h", UserID: "u"}, "")
	var got string
	h := httpapi.WithMiddleware(idp)(http.HandlerFunc(func(_ http.ResponseWriter, req *http.Request) {
		got = httpapi.RequestIDFromContext(req.Context())
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
	require.NotEmpty(t, got)
}

func TestMiddlewareSurfacesIdentityError(t *testing.T) {
	// Stub that always errors — middleware should return 401.
	idp := &errIdentityProvider{err: errs.ErrIdentityMissing}
	h := httpapi.WithMiddleware(idp)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	require.Equal(t, 401, rec.Code)
}

type errIdentityProvider struct{ err error }

func (e *errIdentityProvider) FromRequest(context.Context, *http.Request) (identity.Identity, error) {
	return identity.Identity{}, e.err
}
```

- [ ] **Step 2: Run, verify fail**

- [ ] **Step 3: Implement middleware**

```go
// internal/httpapi/middleware.go
package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/wesm/fotobank/internal/errs"
	"github.com/wesm/fotobank/internal/identity"
)

type ctxKey int

const (
	ctxKeyIdentity ctxKey = iota
	ctxKeyRequestID
)

// WithMiddleware returns a middleware that populates identity and
// request-id into the request context, logs the request, and returns
// 401 if identity extraction fails.
func WithMiddleware(idp identity.Provider) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			id, err := idp.FromRequest(r.Context(), r)
			if err != nil {
				status := http.StatusInternalServerError
				switch {
				case errors.Is(err, errs.ErrIdentityMissing):
					status = http.StatusUnauthorized
				case errors.Is(err, errs.ErrDirectAccessBlocked):
					status = http.StatusForbidden
				}
				http.Error(w, err.Error(), status)
				slog.Warn("request rejected",
					"method", r.Method, "path", r.URL.Path,
					"status", status, "err", err, "dur", time.Since(start))
				return
			}
			reqID := id.RequestID
			if reqID == "" {
				reqID = uuid.NewString()
			}
			ctx := context.WithValue(r.Context(), ctxKeyIdentity, id)
			ctx = context.WithValue(ctx, ctxKeyRequestID, reqID)

			rw := &statusCapture{ResponseWriter: w, code: 200}
			next.ServeHTTP(rw, r.WithContext(ctx))

			slog.Info("request",
				"method", r.Method, "path", r.URL.Path,
				"status", rw.code, "principal", id.Principal.OwnersPrincipal().String(),
				"req", reqID, "dur", time.Since(start))
		})
	}
}

func IdentityFromContext(ctx context.Context) (identity.Identity, bool) {
	id, ok := ctx.Value(ctxKeyIdentity).(identity.Identity)
	return id, ok
}

func RequestIDFromContext(ctx context.Context) string {
	s, _ := ctx.Value(ctxKeyRequestID).(string)
	return s
}

type statusCapture struct {
	http.ResponseWriter
	code int
}

func (c *statusCapture) WriteHeader(code int) {
	c.code = code
	c.ResponseWriter.WriteHeader(code)
}
```

- [ ] **Step 4: Wire middleware into `New`**

Update `New` in `api.go`:

```go
func New(deps Deps) (http.Handler, error) {
	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("Fotobank", version.Short))
	api.OpenAPI().Info.Description = "Fotobank HTTP API"

	if err := registerHealthz(api); err != nil {
		return nil, err
	}
	if err := registerMe(api); err != nil {
		return nil, err
	}

	if deps.IdentityProvider != nil {
		return WithMiddleware(deps.IdentityProvider)(mux), nil
	}
	return mux, nil
}
```

(`registerMe` added in the next task; add an empty stub now to satisfy build.)

- [ ] **Step 5: Run, verify pass**

- [ ] **Step 6: Commit**

```bash
git add internal/httpapi/
git commit -m "Add identity + request-id + logging middleware"
```

---

### Task 25: `/api/v1/me` endpoint

**Files:**
- Create: `internal/httpapi/me.go`
- Create: `internal/httpapi/me_test.go`

- [ ] **Step 1: Write failing test**

```go
func TestMeReturnsStubPrincipal(t *testing.T) {
	r := require.New(t)
	idp := identity.NewStub(owners.Principal{Hub: "h", UserID: "u"}, "User")
	h, err := httpapi.New(httpapi.Deps{IdentityProvider: idp})
	r.NoError(err)
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/me")
	r.NoError(err)
	defer resp.Body.Close()
	r.Equal(200, resp.StatusCode)

	var body struct {
		Principal struct {
			Hub, UserID, Handle string
		} `json:"principal"`
		Scopes []string `json:"scopes"`
	}
	r.NoError(json.NewDecoder(resp.Body).Decode(&body))
	r.Equal("h", body.Principal.Hub)
	r.Equal("u", body.Principal.UserID)
	r.Equal("User", body.Principal.Handle)
}
```

- [ ] **Step 2: Run, verify fail**

- [ ] **Step 3: Implement**

```go
// internal/httpapi/me.go
package httpapi

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/wesm/fotobank/internal/errs"
)

type meOutput struct {
	Body struct {
		Principal struct {
			Hub    string `json:"hub"`
			UserID string `json:"user_id"`
			Handle string `json:"handle,omitempty"`
		} `json:"principal"`
		Scopes []string `json:"scopes"`
	}
}

func registerMe(api huma.API) error {
	huma.Register(api, huma.Operation{
		OperationID: "me",
		Method:      http.MethodGet,
		Path:        "/api/v1/me",
		Summary:     "Return identity of the caller",
	}, func(ctx context.Context, _ *struct{}) (*meOutput, error) {
		id, ok := IdentityFromContext(ctx)
		if !ok {
			return nil, huma.Error401Unauthorized(errs.ErrIdentityMissing.Error())
		}
		out := &meOutput{}
		out.Body.Principal.Hub = id.Principal.Hub
		out.Body.Principal.UserID = id.Principal.UserID
		out.Body.Principal.Handle = id.Principal.Handle
		out.Body.Scopes = id.Scopes
		return out, nil
	})
	return nil
}
```

- [ ] **Step 4: Run, verify pass**

- [ ] **Step 5: Commit**

```bash
git add internal/httpapi/me.go internal/httpapi/me_test.go
git commit -m "Add /api/v1/me endpoint"
```

---

### Task 26: Error → huma error translation

**Files:**
- Create: `internal/httpapi/errors.go`
- Create: `internal/httpapi/errors_test.go`

- [ ] **Step 1: Write failing tests**

```go
func TestTranslateMapsSentinels(t *testing.T) {
	cases := []struct {
		in   error
		want int
	}{
		{errs.ErrNotFound, 404},
		{errs.ErrAlreadyExists, 409},
		{errs.ErrInvalidArgument, 400},
		{errs.ErrPermissionDenied, 403},
		{errs.ErrOwnerMismatch, 403},
		{errs.ErrConcurrentImport, 409},
		{errs.ErrBrokerUnavailable, 503},
		{errs.ErrIdentityMissing, 401},
		{errs.ErrDirectAccessBlocked, 403},
		{errs.ErrBadConfiguration, 500},
		{errors.New("random"), 500},
	}
	for _, c := range cases {
		got := httpapi.Translate(c.in)
		require.NotNil(t, got)
		require.Equal(t, c.want, httpapi.StatusFrom(got), "%v", c.in)
	}
}
```

- [ ] **Step 2: Run, verify fail**

- [ ] **Step 3: Implement**

```go
// internal/httpapi/errors.go
package httpapi

import (
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/wesm/fotobank/internal/errs"
)

// Translate converts a domain error into a huma status error.
func Translate(err error) huma.StatusError {
	switch {
	case errors.Is(err, errs.ErrNotFound):
		return huma.Error404NotFound(err.Error())
	case errors.Is(err, errs.ErrAlreadyExists), errors.Is(err, errs.ErrConcurrentImport):
		return huma.Error409Conflict(err.Error())
	case errors.Is(err, errs.ErrInvalidArgument):
		return huma.Error400BadRequest(err.Error())
	case errors.Is(err, errs.ErrPermissionDenied),
		errors.Is(err, errs.ErrOwnerMismatch),
		errors.Is(err, errs.ErrDirectAccessBlocked):
		return huma.Error403Forbidden(err.Error())
	case errors.Is(err, errs.ErrIdentityMissing):
		return huma.Error401Unauthorized(err.Error())
	case errors.Is(err, errs.ErrBrokerUnavailable):
		return huma.Error503ServiceUnavailable(err.Error())
	default:
		return huma.Error500InternalServerError(err.Error())
	}
}

// StatusFrom extracts the numeric status code from a huma StatusError.
func StatusFrom(err huma.StatusError) int {
	if err == nil {
		return http.StatusOK
	}
	return err.GetStatus()
}
```

- [ ] **Step 4: Run, verify pass**

- [ ] **Step 5: Commit**

```bash
git add internal/httpapi/errors.go internal/httpapi/errors_test.go
git commit -m "Translate sentinel errors to huma HTTP responses"
```

---

### Task 27: cmd/fotobank-openapi

**Files:**
- Modify: `cmd/fotobank-openapi/main.go`
- Create: `cmd/fotobank-openapi/main_test.go`

- [ ] **Step 1: Write the binary**

```go
// cmd/fotobank-openapi/main.go
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/wesm/fotobank/internal/httpapi"
)

func main() {
	out := flag.String("out", "", "output file (default stdout)")
	flag.Parse()

	spec, err := httpapi.OpenAPISpec()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	var w io.Writer = os.Stdout
	if *out != "" {
		f, err := os.Create(*out)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		defer f.Close()
		w = f
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(spec); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
```

Add a helper in `httpapi`:

```go
// internal/httpapi/openapi.go
package httpapi

import (
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"github.com/wesm/fotobank/internal/version"
)

// OpenAPISpec builds the huma API with no runtime dependencies and
// returns the OpenAPI document for dumping.
func OpenAPISpec() (*huma.OpenAPI, error) {
	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("Fotobank", version.Short))
	api.OpenAPI().Info.Description = "Fotobank HTTP API"
	if err := registerHealthz(api); err != nil {
		return nil, err
	}
	if err := registerMe(api); err != nil {
		return nil, err
	}
	return api.OpenAPI(), nil
}
```

- [ ] **Step 2: Test it emits valid JSON with healthz + me**

```go
func TestBinaryEmitsOpenAPIWithKnownPaths(t *testing.T) {
	out := filepath.Join(t.TempDir(), "spec.json")
	cmd := exec.Command("go", "run", "./cmd/fotobank-openapi", "-out", out)
	cmd.Dir = repoRoot(t)
	require.NoError(t, cmd.Run())

	bytes, err := os.ReadFile(out)
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(bytes, &doc))

	paths, _ := doc["paths"].(map[string]any)
	_, hasHealthz := paths["/api/v1/healthz"]
	_, hasMe := paths["/api/v1/me"]
	require.True(t, hasHealthz && hasMe)
}

func repoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	require.NoError(t, err)
	return strings.TrimSpace(string(out))
}
```

- [ ] **Step 3: Run**

```bash
go test ./cmd/fotobank-openapi/... -shuffle=on
make api-generate
```

- [ ] **Step 4: Commit**

```bash
git add cmd/fotobank-openapi/ internal/httpapi/openapi.go
git commit -m "Add OpenAPI spec dumper binary"
```

---

## Part 8 — CLI and server

### Task 28: CLI dispatcher

**Files:**
- Create: `internal/cli/dispatch.go`
- Create: `internal/cli/dispatch_test.go`

- [ ] **Step 1: Write failing tests**

```go
func TestUnknownSubcommandIsUsageError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cli.Run([]string{"nosuch"}, &stdout, &stderr)
	require.Equal(t, 2, code)
	require.Contains(t, stderr.String(), "usage")
}

func TestHelpExitsZero(t *testing.T) {
	var stdout, stderr bytes.Buffer
	require.Equal(t, 0, cli.Run([]string{"help"}, &stdout, &stderr))
	require.NotEmpty(t, stdout.String())
}

func TestVersionPrintsVersionInfo(t *testing.T) {
	var stdout, stderr bytes.Buffer
	require.Equal(t, 0, cli.Run([]string{"version"}, &stdout, &stderr))
	require.Contains(t, stdout.String(), "fotobank")
}
```

- [ ] **Step 2: Run, verify fail**

- [ ] **Step 3: Implement dispatch**

```go
// internal/cli/dispatch.go
package cli

import (
	"fmt"
	"io"

	"github.com/wesm/fotobank/internal/version"
)

// Run dispatches a fotobank CLI invocation. args is os.Args[1:].
// Returns the process exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printUsage(stderr)
		return 2
	}
	switch args[0] {
	case "server":
		return runServer(args[1:], stdout, stderr)
	case "owners":
		return runOwners(args[1:], stdout, stderr)
	case "config":
		return runConfigCmd(args[1:], stdout, stderr)
	case "version":
		fmt.Fprintln(stdout, version.Format())
		return 0
	case "help", "-h", "--help":
		printUsage(stdout)
		return 0
	default:
		printUsage(stderr)
		return 2
	}
}

func printUsage(w io.Writer) {
	fmt.Fprint(w, `usage: fotobank <command> [flags]

commands:
  server        Start the HTTP server
  owners        Manage owners (add/list/remove)
  config        Inspect configuration (path/read/validate)
  version       Print version
  help          Show this help
`)
}

// Placeholder stubs (filled in by later tasks).
func runServer(args []string, stdout, stderr io.Writer) int { return 0 }
func runOwners(args []string, stdout, stderr io.Writer) int { return 0 }
func runConfigCmd(args []string, stdout, stderr io.Writer) int { return 0 }
```

- [ ] **Step 4: Wire main.go to dispatch**

```go
// cmd/fotobank/main.go
package main

import (
	"os"

	"github.com/wesm/fotobank/internal/cli"
	"github.com/wesm/fotobank/internal/version"
)

var (
	vVersion   = "dev"
	vCommit    = "unknown"
	vBuildDate = "unknown"
)

func main() {
	version.Short = vVersion
	version.Commit = vCommit
	version.BuildDate = vBuildDate
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
```

- [ ] **Step 5: Run, verify pass**

```bash
go test ./internal/cli/... -shuffle=on
make build
./fotobank help
./fotobank version
./fotobank nosuch   # prints usage to stderr and exits 2
```

- [ ] **Step 6: Commit**

```bash
git add internal/cli/dispatch.go internal/cli/dispatch_test.go cmd/fotobank/main.go
git commit -m "Add CLI dispatcher with help + version"
```

---

### Task 29: `fotobank config` subcommands

**Files:**
- Create: `internal/cli/config.go`
- Create: `internal/cli/config_test.go`

- [ ] **Step 1: Write failing tests**

```go
func TestConfigPathPrintsResolvedPath(t *testing.T) {
	t.Setenv("FOTOBANK_CONFIG", "/tmp/example.toml")
	var out, eout bytes.Buffer
	require.Equal(t, 0, cli.Run([]string{"config", "path"}, &out, &eout))
	require.Contains(t, out.String(), "/tmp/example.toml")
}

func TestConfigValidateWithValidFile(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "c.toml")
	require.NoError(t, os.WriteFile(p, []byte(`[nas]
root = "/tmp/nas"
`), 0o600))
	t.Setenv("FOTOBANK_CONFIG", p)

	var out, eout bytes.Buffer
	require.Equal(t, 0, cli.Run([]string{"config", "validate"}, &out, &eout))
}

func TestConfigReadReturnsScalar(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "c.toml")
	require.NoError(t, os.WriteFile(p, []byte(`[nas]
root = "/my/nas"
`), 0o600))
	t.Setenv("FOTOBANK_CONFIG", p)

	var out, eout bytes.Buffer
	require.Equal(t, 0, cli.Run([]string{"config", "read", "nas.root"}, &out, &eout))
	require.Equal(t, "/my/nas\n", out.String())
}
```

- [ ] **Step 2: Run, verify fail**

- [ ] **Step 3: Implement**

Replace the placeholder `runConfigCmd` in `dispatch.go` with a real handler, and add `internal/cli/config.go`:

```go
// internal/cli/config.go
package cli

import (
	"flag"
	"fmt"
	"io"
	"reflect"
	"strings"

	"github.com/wesm/fotobank/internal/config"
)

func runConfigCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: fotobank config <path|read|validate>")
		return 2
	}
	switch args[0] {
	case "path":
		return runConfigPath(args[1:], stdout, stderr)
	case "read":
		return runConfigRead(args[1:], stdout, stderr)
	case "validate":
		return runConfigValidate(args[1:], stdout, stderr)
	default:
		fmt.Fprintln(stderr, "usage: fotobank config <path|read|validate>")
		return 2
	}
}

func runConfigPath(_ []string, stdout, _ io.Writer) int {
	fmt.Fprintln(stdout, config.DefaultConfigPath())
	return 0
}

func runConfigValidate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("config validate", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cfgPath := fs.String("config", config.DefaultConfigPath(), "path to config file")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if _, err := config.Load(*cfgPath); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintln(stdout, "ok")
	return 0
}

func runConfigRead(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("config read", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cfgPath := fs.String("config", config.DefaultConfigPath(), "path to config file")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "config read requires one key (e.g. nas.root)")
		return 2
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	v, err := traverse(cfg, fs.Arg(0))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintln(stdout, v)
	return 0
}

// traverse resolves a dotted TOML key against the config struct using
// toml tags. Supports scalars only; nested tables only as path segments.
func traverse(cfg *config.Config, key string) (string, error) {
	parts := strings.Split(key, ".")
	var v reflect.Value = reflect.ValueOf(cfg).Elem()
	for _, p := range parts {
		t := v.Type()
		found := false
		for i := 0; i < t.NumField(); i++ {
			tag := strings.Split(t.Field(i).Tag.Get("toml"), ",")[0]
			if tag == p {
				v = v.Field(i)
				found = true
				break
			}
		}
		if !found {
			return "", fmt.Errorf("unknown config key %q", key)
		}
	}
	switch v.Kind() {
	case reflect.String, reflect.Bool, reflect.Int, reflect.Int64, reflect.Float64:
		return fmt.Sprintf("%v", v.Interface()), nil
	default:
		return "", fmt.Errorf("config key %q is not a scalar", key)
	}
}
```

- [ ] **Step 4: Run, verify pass**

- [ ] **Step 5: Commit**

```bash
git add internal/cli/config.go internal/cli/config_test.go internal/cli/dispatch.go
git commit -m "Add fotobank config path|read|validate subcommands"
```

---

### Task 30: `fotobank server` — boot the HTTP server

**Files:**
- Create: `internal/cli/server.go`
- Create: `internal/cli/server_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestServerRespondsToHealthz(t *testing.T) {
	r := require.New(t)

	// Prepare a minimal valid config pointing at a freshly created NAS root
	// and an ephemeral listen address.
	tmp := t.TempDir()
	nasRoot := filepath.Join(tmp, "nas")
	r.NoError(os.MkdirAll(nasRoot, 0o700))

	cfgPath := filepath.Join(tmp, "c.toml")
	r.NoError(os.WriteFile(cfgPath, []byte(fmt.Sprintf(`
[nas]
root = %q
[flash]
root = %q
[http]
listen_address = "127.0.0.1:0"   # ephemeral
`, nasRoot, filepath.Join(tmp, "flash"))), 0o600))
	t.Setenv("FOTOBANK_CONFIG", cfgPath)
	t.Setenv("FOTOBANK_DB_PATH", filepath.Join(tmp, "fotobank.sqlite"))

	// Run the server in a goroutine. It exits on context cancel / signal.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan int, 1)
	actualAddr := make(chan string, 1)
	t.Setenv("FOTOBANK_TEST_ADDR_SINK", "1") // signals runServer to publish its resolved addr
	// … see server.go: test hook reads FOTOBANK_TEST_ADDR_SINK and writes addr to a file.

	go func() {
		var out, eout bytes.Buffer
		errCh <- cli.RunContext(ctx, []string{"server", "--config", cfgPath}, &out, &eout)
	}()

	// Poll until the address file appears (server has bound).
	addrFile := filepath.Join(tmp, "addr")
	var resolved string
	for i := 0; i < 50; i++ {
		if b, err := os.ReadFile(addrFile); err == nil {
			resolved = strings.TrimSpace(string(b))
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	r.NotEmpty(resolved, "server never published its bind address")
	_ = actualAddr

	resp, err := http.Get("http://" + resolved + "/api/v1/healthz")
	r.NoError(err)
	defer resp.Body.Close()
	r.Equal(200, resp.StatusCode)

	cancel()
	r.Equal(0, <-errCh)
}
```

Note: use `t.Setenv` + an env-driven "write resolved addr to file" hook in `server.go` to avoid racing on port 0 resolution. Alternative, use `net.Listen("tcp", "127.0.0.1:0")` directly in the CLI code, publish the address, and have the CLI always accept a pre-bound listener via an env hook `FOTOBANK_TEST_LISTEN_ADDR_SINK=<path>`.

- [ ] **Step 2: Run, verify fail**

- [ ] **Step 3: Implement `RunContext` + `runServer`**

Add a context-aware variant `cli.RunContext` (and keep `cli.Run` as `RunContext(context.Background(), ...)`):

```go
// internal/cli/dispatch.go additions
func RunContext(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printUsage(stderr)
		return 2
	}
	switch args[0] {
	case "server":
		return runServer(ctx, args[1:], stdout, stderr)
	// … others unchanged …
	}
}
```

`internal/cli/server.go`:

```go
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/wesm/fotobank/internal/config"
	"github.com/wesm/fotobank/internal/db"
	"github.com/wesm/fotobank/internal/httpapi"
	"github.com/wesm/fotobank/internal/identity"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/service"
)

func runServer(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("server", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cfgPath := fs.String("config", config.DefaultConfigPath(), "path to config file")
	listen := fs.String("listen", "", "override [http].listen_address")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *listen != "" {
		cfg.HTTP.ListenAddress = *listen
	}
	dbPath := os.Getenv("FOTOBANK_DB_PATH")
	if dbPath == "" {
		dbPath = cfg.Flash.Root + "/fotobank.sqlite"
	}
	d, err := db.Open(dbPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer d.Close()

	ownerSvc := service.NewOwnerService(owners.NewRepo(d.WriteDB(), d.ReadDB()))

	var idp identity.Provider
	switch cfg.Identity.Mode {
	case "stub":
		p := owners.Principal{Hub: cfg.Identity.Stub.Hub, UserID: cfg.Identity.Stub.UserID}
		storageKey := cfg.Identity.Stub.StorageKey
		if storageKey == "" {
			storageKey = cfg.Identity.Stub.UserID
		}
		if err := ownerSvc.Ensure(ctx, p, storageKey); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		idp = identity.NewStub(p, cfg.Identity.Stub.Handle)
	case "header":
		guard := identity.NewGuard(identity.GuardConfig{
			ListenAddress: cfg.HTTP.ListenAddress,
			TrustedProxyCIDRs: cfg.Identity.Header.TrustedProxyCIDRs,
			ProxySecretHeader: cfg.Identity.Header.ProxySecretHeader,
			ProxySecret: cfg.Identity.Header.ProxySecret,
			ProxyMTLSCAFile: cfg.Identity.Header.ProxyMTLSCAFile,
		})
		idp = identity.NewHeader(identity.HeaderConfig{
			UserIDHeader:    cfg.Identity.Header.UserIDHeader,
			HubHeader:       cfg.Identity.Header.HubHeader,
			HandleHeader:    cfg.Identity.Header.HandleHeader,
			ScopesHeader:    cfg.Identity.Header.ScopesHeader,
			RequestIDHeader: cfg.Identity.Header.RequestIDHeader,
		}, guard)
	}

	handler, err := httpapi.New(httpapi.Deps{
		IdentityProvider: idp,
		OwnerService:     ownerSvc,
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	ln, err := net.Listen("tcp", cfg.HTTP.ListenAddress)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if sink := os.Getenv("FOTOBANK_TEST_LISTEN_ADDR_SINK"); sink != "" {
		_ = os.WriteFile(sink, []byte(ln.Addr().String()), 0o600)
	}
	fmt.Fprintln(stdout, "fotobank server listening on", ln.Addr())

	srv := &http.Server{
		Handler:      handler,
		ReadTimeout:  cfg.HTTP.RequestTimeout,
		WriteTimeout: cfg.HTTP.WriteTimeout,
	}

	sigCtx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	select {
	case err := <-serveErr:
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	case <-sigCtx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
}
```

Update the test to use `FOTOBANK_TEST_LISTEN_ADDR_SINK` to discover the ephemeral port, then hit `/healthz`.

- [ ] **Step 4: Run, verify pass**

```bash
go test ./internal/cli/... -shuffle=on -run TestServer
```

- [ ] **Step 5: Commit**

```bash
git add internal/cli/server.go internal/cli/server_test.go internal/cli/dispatch.go
git commit -m "Add fotobank server subcommand with graceful shutdown"
```

---

### Task 31: `fotobank owners` subcommands

**Files:**
- Create: `internal/cli/owners.go`
- Create: `internal/cli/owners_test.go`

- [ ] **Step 1: Write failing tests**

```go
func TestOwnersAddCreatesRow(t *testing.T) {
	r := require.New(t)
	tmp := newCLITempEnv(t)

	var out, eout bytes.Buffer
	code := cli.Run([]string{"owners", "add",
		"--hub", "h", "--user-id", "u", "--storage-key", "k", "--handle", "User",
	}, &out, &eout)
	r.Equal(0, code, eout.String())

	// Second invocation with same args should also succeed (idempotent).
	out.Reset(); eout.Reset()
	code = cli.Run([]string{"owners", "add",
		"--hub", "h", "--user-id", "u", "--storage-key", "k",
	}, &out, &eout)
	r.Equal(0, code, eout.String())

	// But conflict on storage_key change must fail.
	out.Reset(); eout.Reset()
	code = cli.Run([]string{"owners", "add",
		"--hub", "h", "--user-id", "u", "--storage-key", "different",
	}, &out, &eout)
	r.Equal(1, code)
	r.Contains(eout.String(), "already")
	_ = tmp
}

func TestOwnersListShowsAddedRow(t *testing.T) {
	_ = newCLITempEnv(t)

	var out, eout bytes.Buffer
	require.Equal(t, 0, cli.Run([]string{"owners", "add",
		"--hub", "h", "--user-id", "u", "--storage-key", "k",
	}, &out, &eout))

	out.Reset(); eout.Reset()
	require.Equal(t, 0, cli.Run([]string{"owners", "list", "--json"}, &out, &eout))
	var rows []map[string]any
	require.NoError(t, json.Unmarshal(out.Bytes(), &rows))
	require.Len(t, rows, 1)
	require.Equal(t, "h", rows[0]["hub"])
}

func TestOwnersRemoveSucceedsWhenEmpty(t *testing.T) {
	_ = newCLITempEnv(t)
	var out, eout bytes.Buffer
	require.Equal(t, 0, cli.Run([]string{"owners", "add",
		"--hub", "h", "--user-id", "u", "--storage-key", "k",
	}, &out, &eout))
	out.Reset(); eout.Reset()
	require.Equal(t, 0, cli.Run([]string{"owners", "remove",
		"--hub", "h", "--user-id", "u",
	}, &out, &eout))
}

// newCLITempEnv sets FOTOBANK_CONFIG + FOTOBANK_DB_PATH to t.TempDir()-backed
// values with a valid minimal config; returns the tempdir.
func newCLITempEnv(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	cfg := filepath.Join(tmp, "c.toml")
	require.NoError(t, os.WriteFile(cfg, []byte(fmt.Sprintf(`
[nas]
root = %q
`, filepath.Join(tmp, "nas"))), 0o600))
	t.Setenv("FOTOBANK_CONFIG", cfg)
	t.Setenv("FOTOBANK_DB_PATH", filepath.Join(tmp, "fotobank.sqlite"))
	return tmp
}
```

- [ ] **Step 2: Run, verify fail**

- [ ] **Step 3: Implement**

```go
// internal/cli/owners.go
package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"github.com/wesm/fotobank/internal/cli/clictx"
	"github.com/wesm/fotobank/internal/owners"
)

func runOwners(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: fotobank owners <add|list|remove>")
		return 2
	}
	switch args[0] {
	case "add":
		return runOwnersAdd(args[1:], stdout, stderr)
	case "list":
		return runOwnersList(args[1:], stdout, stderr)
	case "remove":
		return runOwnersRemove(args[1:], stdout, stderr)
	default:
		fmt.Fprintln(stderr, "usage: fotobank owners <add|list|remove>")
		return 2
	}
}

func runOwnersAdd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("owners add", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	hub := fs.String("hub", "", "")
	uid := fs.String("user-id", "", "")
	key := fs.String("storage-key", "", "")
	handle := fs.String("handle", "", "")
	if err := fs.Parse(args); err != nil || *hub == "" || *uid == "" || *key == "" {
		fmt.Fprintln(stderr, "usage: fotobank owners add --hub H --user-id U --storage-key K [--handle H]")
		return 2
	}
	svc, cleanup, err := clictx.LoadOwnerService()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer cleanup()
	p := owners.Principal{Hub: *hub, UserID: *uid}
	if err := svc.Ensure(context.Background(), p, *key); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *handle != "" {
		if err := svc.UpdateDisplay(context.Background(), p, *handle); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	fmt.Fprintln(stdout, "added", p)
	return 0
}

func runOwnersList(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("owners list", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	jsonOut := fs.Bool("json", false, "")
	_ = fs.Parse(args)

	svc, cleanup, err := clictx.LoadOwnerService()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer cleanup()
	rows, err := svc.List(context.Background())
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *jsonOut {
		out := make([]map[string]any, 0, len(rows))
		for _, o := range rows {
			out = append(out, map[string]any{
				"hub":         o.Principal.Hub,
				"user_id":     o.Principal.UserID,
				"storage_key": o.StorageKey,
				"handle":      o.DisplayHandle,
				"created_at":  o.CreatedAt,
			})
		}
		return encodeJSON(stdout, out)
	}
	for _, o := range rows {
		fmt.Fprintf(stdout, "%s\t%s\t%s\n", o.Principal, o.StorageKey, o.DisplayHandle)
	}
	return 0
}

func runOwnersRemove(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("owners remove", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	hub := fs.String("hub", "", "")
	uid := fs.String("user-id", "", "")
	purge := fs.Bool("purge", false, "also remove media rows (Plan B+)")
	if err := fs.Parse(args); err != nil || *hub == "" || *uid == "" {
		fmt.Fprintln(stderr, "usage: fotobank owners remove --hub H --user-id U [--purge]")
		return 2
	}
	svc, cleanup, err := clictx.LoadOwnerService()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer cleanup()
	if err := svc.Remove(context.Background(), owners.Principal{Hub: *hub, UserID: *uid}, *purge); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintln(stdout, "removed")
	return 0
}

func encodeJSON(w io.Writer, v any) int {
	enc := json.NewEncoder(w)
	if err := enc.Encode(v); err != nil {
		return 1
	}
	return 0
}
```

And a small shared loader in `internal/cli/clictx/clictx.go`:

```go
package clictx

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/wesm/fotobank/internal/config"
	"github.com/wesm/fotobank/internal/db"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/service"
)

func LoadOwnerService() (*service.OwnerService, func(), error) {
	cfg, err := config.Load(config.DefaultConfigPath())
	if err != nil {
		return nil, nil, fmt.Errorf("load config: %w", err)
	}
	dbPath := os.Getenv("FOTOBANK_DB_PATH")
	if dbPath == "" {
		dbPath = filepath.Join(cfg.Flash.Root, "fotobank.sqlite")
	}
	d, err := db.Open(dbPath)
	if err != nil {
		return nil, nil, err
	}
	svc := service.NewOwnerService(owners.NewRepo(d.WriteDB(), d.ReadDB()))
	return svc, func() { d.Close() }, nil
}
```

- [ ] **Step 4: Run, verify pass**

- [ ] **Step 5: Commit**

```bash
git add internal/cli/owners.go internal/cli/owners_test.go internal/cli/clictx/
git commit -m "Add fotobank owners add|list|remove subcommands"
```

---

## Part 9 — End-to-end verification

### Task 32: Integration test: full boot + `/me`

**Files:**
- Create: `internal/cli/e2e_test.go`

- [ ] **Step 1: Write the test**

Drive `cli.RunContext` with `server` in a goroutine, use `FOTOBANK_TEST_LISTEN_ADDR_SINK` to discover the bound port, fire `/api/v1/healthz` and `/api/v1/me`, assert the configured stub principal appears in the `/me` response, then cancel context and assert clean shutdown.

```go
func TestEndToEndServerStubPrincipal(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfg := filepath.Join(tmp, "c.toml")
	r.NoError(os.WriteFile(cfg, []byte(fmt.Sprintf(`
[nas]
root = %q
[identity.stub]
hub = "local"
user_id = "alice"
handle = "Alice"
[http]
listen_address = "127.0.0.1:0"
`, filepath.Join(tmp, "nas"))), 0o600))
	t.Setenv("FOTOBANK_CONFIG", cfg)
	t.Setenv("FOTOBANK_DB_PATH", filepath.Join(tmp, "fotobank.sqlite"))
	addrSink := filepath.Join(tmp, "addr")
	t.Setenv("FOTOBANK_TEST_LISTEN_ADDR_SINK", addrSink)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() {
		var so, se bytes.Buffer
		done <- cli.RunContext(ctx, []string{"server"}, &so, &se)
	}()

	var addr string
	for i := 0; i < 100; i++ {
		if b, err := os.ReadFile(addrSink); err == nil && len(b) > 0 {
			addr = strings.TrimSpace(string(b))
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	r.NotEmpty(addr, "server did not publish bind address")

	resp, err := http.Get("http://" + addr + "/api/v1/me")
	r.NoError(err)
	defer resp.Body.Close()
	r.Equal(200, resp.StatusCode)

	var body struct {
		Principal struct {
			Hub, UserID, Handle string
		} `json:"principal"`
	}
	r.NoError(json.NewDecoder(resp.Body).Decode(&body))
	r.Equal("local", body.Principal.Hub)
	r.Equal("alice", body.Principal.UserID)
	r.Equal("Alice", body.Principal.Handle)

	cancel()
	select {
	case code := <-done:
		r.Equal(0, code)
	case <-time.After(5 * time.Second):
		t.Fatal("server did not shut down")
	}
}
```

- [ ] **Step 2: Run**

```bash
go test ./internal/cli/... -shuffle=on -run TestEndToEnd
```

- [ ] **Step 3: Commit**

```bash
git add internal/cli/e2e_test.go
git commit -m "Add end-to-end test: stub principal via /api/v1/me"
```

---

### Task 33: CI-equivalent verification sweep

**Files:** no new files; verifies all prior tasks hold together.

- [ ] **Step 1: Full lint**

```bash
make vet
make lint
```
Expected: clean output. Fix any issues inline.

- [ ] **Step 2: Full test**

```bash
make test
```
Expected: all packages PASS.

- [ ] **Step 3: OpenAPI generation**

```bash
make api-generate
```
Expected: emits `openapi.json` containing `/api/v1/healthz` and `/api/v1/me`.

- [ ] **Step 4: Hooks dry-run**

```bash
prek run --all-files
```
Expected: all hooks pass. Any failure is a real issue to fix.

- [ ] **Step 5: Nilaway (pre-push tier)**

```bash
make nilaway
```
Expected: clean. Any violations get addressed.

- [ ] **Step 6: Build release binary**

```bash
make build-release
./fotobank version
```
Expected: `fotobank <git-describe> (<sha>) built <RFC3339 UTC>`.

- [ ] **Step 7: If anything failed, fix inline and commit the fixes**

```bash
# Only commit if fixes were required.
git add -A
git commit -m "Fix lint/test/nilaway issues from verification sweep"
```

---

## Completion criteria

Plan A is complete when:

- `make lint test api-generate` succeeds locally with no warnings.
- `./fotobank server` boots, serves `/api/v1/healthz` (status ok) and `/api/v1/me` (stub principal).
- `./fotobank owners add|list|remove` round-trip works end to end.
- `./fotobank config path|read|validate` works against the default and user-supplied config paths.
- `openapi.json` contains both `/api/v1/healthz` and `/api/v1/me`.
- The end-to-end test `TestEndToEndServerStubPrincipal` is green.

## What's next

- **Plan B** — Storage layer, media schema handling, EXIF/container extraction, import pipeline, reconcile, `fotobank import`/`reconcile` CLI, `/api/v1/media` + `/original` HTTP routes.
- **Plan C** — Thumbnail worker, `/api/v1/media/{id}/thumb`, `fotobank thumbs` CLI.
- **Plan D** — Albums, scopes, broker stub + outbox, HTTP + CLI surfaces.

Legacy migration from the Python tool is **out of scope** — old photos will be re-ingested through `fotobank import` on a fresh deployment. Sub-spec §14 is dead text and will be removed in a follow-up cleanup.
