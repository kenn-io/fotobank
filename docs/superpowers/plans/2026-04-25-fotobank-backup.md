# Fotobank Backup & Disaster Recovery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Periodic SQLite metadata snapshots to NAS with tiered retention; restore tooling that handles flash-loss recovery; in-server lifetime lock so restore can refuse to clobber a live DB.

**Architecture:** New `internal/backup/` package with `Snapshot`, `Sweep`, `Restore`, and `Worker`. Worker runs in `runServer`'s `bgWG` group. Three CLI subcommands (`backup snapshot`, `list`, `restore`) reuse the same primitives. Server takes a `gofrs/flock` lock at boot; `Restore` tries to acquire the same lock and refuses if held.

**Tech Stack:** Go 1.26, modernc.org/sqlite (pure Go), gofrs/flock (already in tree), testify/require, slog. Spec at `docs/superpowers/specs/2026-04-25-fotobank-backup-design.md`.

---

## File structure

| File | Task | Responsibility |
|------|------|----------------|
| `internal/config/config.go` | T1 | replace legacy `Backup` struct + applyDefaults + Validate clauses |
| `internal/config/config.example.toml` | T1 | replace legacy `[backup]` block |
| `internal/config/config_test.go` | T1 | new tests for backup defaults / IsDefined / validation |
| `internal/backup/errors.go` | T2 | `ErrServerHoldsLock` sentinel |
| `internal/backup/quote.go` | T2 | `sqlQuoteLiteral` for VACUUM INTO |
| `internal/backup/seam.go` | T2 | package-private `syncDir` test seam |
| `internal/backup/quote_test.go` | T2 | golden cases for quoting |
| `internal/backup/snapshot.go` | T3 | `Snapshot`, `SnapshotPath` |
| `internal/backup/snapshot_test.go` | T3 | success, partial cleanup, link collision, sync seam |
| `internal/backup/list.go` | T4 | `Snapshot` model + `List(dir)` newest-first |
| `internal/backup/list_test.go` | T4 | sort order, malformed-skip, partial-skip |
| `internal/backup/retention.go` | T5 | `Policy`, `SweepResult`, `Sweep` |
| `internal/backup/retention_test.go` | T5 | tier-bucket, future-delete, partial cleanup at 24h cutoff |
| `internal/backup/restore.go` | T6 | `RestoreResult`, `Restore` |
| `internal/backup/restore_test.go` | T6 | success, lock contention, rollback on copy/migration fail |
| `internal/backup/worker.go` | T7 | `Worker.Run(ctx)` |
| `internal/backup/worker_test.go` | T7 | tick fires snapshot+sweep, stale-warning suppression |
| `internal/cli/dbpath.go` | T8 | `resolveDBPath`, `lockPathFor` |
| `internal/cli/dbpath_test.go` | T8 | env override + default |
| `internal/cli/server.go` | T9 | acquire flock at boot; spawn backup worker in bgWG |
| `internal/cli/backup.go` | T10 | `backup snapshot/list/restore` subcommands |
| `internal/cli/backup_test.go` | T10 | per-subcommand CLI invocation tests |
| `internal/cli/e2e_backup_test.go` | T11 | Layer C integration: real server boot, snapshot lands |
| `docs/superpowers/specs/2026-04-22-fotobank-go-core-phase1-design.md` | T12 | replace legacy backup TOML snippet |
| `docs/superpowers/plans/2026-04-22-fotobank-go-core-plan-a-foundation.md` | T12 | replace legacy `Backup` struct snippet |

---

## Task 1: Migrate `config.Backup` schema

**Files:**
- Modify: `internal/config/config.go` (struct at lines 141-145, applyDefaults around line 280, Validate after line 187)
- Modify: `internal/config/config.example.toml` (lines 83-86)
- Modify: `internal/config/config_test.go`

- [ ] **Step 1: Write failing test for new defaults**

Add to `internal/config/config_test.go`:

```go
func TestBackupDefaults(t *testing.T) {
	r := require.New(t)
	cfg, err := config.Load(filepath.Join("..", "..", "testdata", "config", "minimal.toml"))
	r.NoError(err)
	r.True(cfg.Backup.Enabled)        // defaulted true when [backup] absent
	r.Equal("", cfg.Backup.Dir)       // empty = derive from nas.root at use site
	r.Equal(4, cfg.Backup.Keep15Min)
	r.Equal(24, cfg.Backup.KeepHourly)
	r.Equal(7, cfg.Backup.KeepDaily)
}

func TestBackupExplicitDisabledHonored(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "c.toml")
	require.NoError(t, os.WriteFile(p, []byte(`
[nas]
root = "/tmp/nas"
[backup]
enabled = false
`), 0o600))
	cfg, err := config.Load(p)
	require.NoError(t, err)
	require.False(t, cfg.Backup.Enabled)
}

func TestBackupValidationRejectsZeroKeepCount(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "c.toml")
	require.NoError(t, os.WriteFile(p, []byte(`
[nas]
root = "/tmp/nas"
[backup]
keep_15min = 0
`), 0o600))
	_, err := config.Load(p)
	require.ErrorIs(t, err, errs.ErrBadConfiguration)
}

func TestBackupValidationRejectsRelativeDir(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "c.toml")
	require.NoError(t, os.WriteFile(p, []byte(`
[nas]
root = "/tmp/nas"
[backup]
dir = "relative/path"
`), 0o600))
	_, err := config.Load(p)
	require.ErrorIs(t, err, errs.ErrBadConfiguration)
}
```

- [ ] **Step 2: Run tests to verify failures**

Run: `go test ./internal/config -run TestBackup -v`
Expected: compilation error — fields `Enabled`, `Dir`, `Keep15Min`, `KeepHourly`, `KeepDaily` do not exist on `config.Backup`.

- [ ] **Step 3: Replace the `Backup` struct**

In `internal/config/config.go` lines 141-145, replace the existing `Backup` struct with:

```go
type Backup struct {
	Enabled    bool   `toml:"enabled"`
	Dir        string `toml:"dir"`
	Keep15Min  int    `toml:"keep_15min"`
	KeepHourly int    `toml:"keep_hourly"`
	KeepDaily  int    `toml:"keep_daily"`
}
```

Remove the `time` import if it becomes unused after this change (it isn't — `BrokerExec.CallTimeout` still uses `time.Duration`).

- [ ] **Step 4: Add applyDefaults entries**

Add inside `applyDefaults` (after the existing thumbs_cache_enabled IsDefined block around line 280-282):

```go
// Default Enabled to true unless the operator explicitly set it.
if !meta.IsDefined("backup", "enabled") {
	c.Backup.Enabled = true
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
```

- [ ] **Step 5: Add Validate clauses**

Add inside the existing `Validate()` method (after the broker-mode check, around line 220):

```go
if c.Backup.Keep15Min < 1 {
	return fmt.Errorf("%w: backup.keep_15min must be >= 1", errs.ErrBadConfiguration)
}
if c.Backup.KeepHourly < 1 {
	return fmt.Errorf("%w: backup.keep_hourly must be >= 1", errs.ErrBadConfiguration)
}
if c.Backup.KeepDaily < 1 {
	return fmt.Errorf("%w: backup.keep_daily must be >= 1", errs.ErrBadConfiguration)
}
if c.Backup.Dir != "" && !filepath.IsAbs(c.Backup.Dir) {
	return fmt.Errorf("%w: backup.dir must be absolute when set", errs.ErrBadConfiguration)
}
```

- [ ] **Step 6: Update `config.example.toml`**

Replace lines 83-86:

```toml
[backup]
# enabled = true                     # default; set to false to disable scheduled snapshots
# dir = ""                           # absolute path; default: {nas.root}/.fotobank/snapshots/
# keep_15min = 4                     # last hour of 15-min slots
# keep_hourly = 24                   # last day of hourly slots
# keep_daily = 7                     # last week of daily slots
```

- [ ] **Step 7: Run tests to verify they pass**

Run: `go test ./internal/config -run TestBackup -v`
Expected: PASS for all four.

- [ ] **Step 8: Run full config tests**

Run: `go test ./internal/config -v`
Expected: all green. Existing tests for legacy fields (if any) need to be removed; the brokerexec migration removed them.

- [ ] **Step 9: Lint and commit**

Run: `make lint`
Expected: clean.

```bash
git add internal/config/config.go internal/config/config.example.toml internal/config/config_test.go
git commit -m "Migrate backup config schema to enabled/dir/tiered keep_*"
```

---

## Task 2: backup package foundation (errors, quote, seam)

**Files:**
- Create: `internal/backup/errors.go`
- Create: `internal/backup/quote.go`
- Create: `internal/backup/seam.go`
- Create: `internal/backup/quote_test.go`

- [ ] **Step 1: Write failing tests for `sqlQuoteLiteral`**

Create `internal/backup/quote_test.go`:

```go
package backup

import "testing"

func TestSqlQuoteLiteral(t *testing.T) {
	cases := []struct{ in, want string }{
		{"foo", "'foo'"},
		{"with space", "'with space'"},
		{"O'Reilly", "'O''Reilly'"},
		{"trailing'", "'trailing'''"},
		{"''", "''''''"},                    // two-single-quote input → six (open + 2*2 + close)
		{"unicode/路径", "'unicode/路径'"},
		{`back\slash`, `'back\slash'`},      // backslash is not special to SQLite literals
	}
	for _, c := range cases {
		got := sqlQuoteLiteral(c.in)
		if got != c.want {
			t.Errorf("sqlQuoteLiteral(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/backup -run TestSqlQuoteLiteral -v`
Expected: build fails — `sqlQuoteLiteral` undefined.

- [ ] **Step 3: Implement `quote.go`**

```go
package backup

import "strings"

// sqlQuoteLiteral wraps s as a SQLite string literal: doubles any
// internal single quotes and surrounds the result with single quotes.
// VACUUM INTO requires its destination as a literal in the SQL text;
// driver parameter binding is not supported for that statement, so we
// quote correctly to protect against malformed paths.
func sqlQuoteLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
```

- [ ] **Step 4: Implement `errors.go`**

```go
package backup

import "errors"

// ErrServerHoldsLock is returned by Restore when another process is
// currently holding the lifetime flock on the DB. Callers (CLI) check
// via errors.Is to distinguish "operator forgot to stop the server"
// from other failure modes.
var ErrServerHoldsLock = errors.New("backup: another fotobank process is using the DB")
```

- [ ] **Step 5: Implement `seam.go`**

```go
package backup

import "os"

// syncDir is the package-level fsync seam used by Snapshot's parent-dir
// fsync and Restore's post-rename fsync. Tests swap this via setSyncDir
// to count calls or inject errors.
var syncDir = realSyncDir

func realSyncDir(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// setSyncDir installs fn as the package-level dir-fsync function for
// the duration of the test, restoring the original on cleanup.
func setSyncDir(t interface{ Cleanup(func()) }, fn func(string) error) {
	prev := syncDir
	syncDir = fn
	t.Cleanup(func() { syncDir = prev })
}
```

- [ ] **Step 6: Run tests to verify**

Run: `go test ./internal/backup -v`
Expected: PASS for `TestSqlQuoteLiteral`. No other tests yet.

- [ ] **Step 7: Lint and commit**

Run: `make lint`
Expected: clean.

```bash
git add internal/backup/errors.go internal/backup/quote.go internal/backup/seam.go internal/backup/quote_test.go
git commit -m "Add backup package foundation: errors, quote, sync seam"
```

---

## Task 3: `Snapshot` and `SnapshotPath`

**Files:**
- Create: `internal/backup/snapshot.go`
- Create: `internal/backup/snapshot_test.go`

- [ ] **Step 1: Write failing tests**

Create `internal/backup/snapshot_test.go`:

```go
package backup

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// makeSourceDB creates a tiny SQLite DB at path with one table and one row.
func makeSourceDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec("CREATE TABLE t(x INTEGER); INSERT INTO t VALUES (42);")
	require.NoError(t, err)
	return db
}

func integrityOk(t *testing.T, path string) bool {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	require.NoError(t, err)
	defer db.Close()
	var s string
	require.NoError(t, db.QueryRow("PRAGMA integrity_check").Scan(&s))
	return s == "ok"
}

func TestSnapshotWritesIntegralCopy(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src.sqlite")
	db := makeSourceDB(t, src)
	dst := filepath.Join(tmp, "snap.sqlite")

	r.NoError(Snapshot(context.Background(), db, dst))
	r.FileExists(dst)
	r.True(integrityOk(t, dst), "destination must pass integrity_check")

	// .partial gone; source unchanged
	_, err := os.Stat(dst + ".partial")
	r.True(os.IsNotExist(err))
}

func TestSnapshotRefusesExistingDestination(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src.sqlite")
	db := makeSourceDB(t, src)
	dst := filepath.Join(tmp, "snap.sqlite")
	r.NoError(os.WriteFile(dst, []byte("preexisting"), 0o600))

	err := Snapshot(context.Background(), db, dst)
	r.Error(err, "must not silently replace an existing destination file")
	// preexisting content still on disk
	b, _ := os.ReadFile(dst)
	r.Equal("preexisting", string(b))
}

func TestSnapshotCallsSyncDirOnParent(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src.sqlite")
	db := makeSourceDB(t, src)
	dst := filepath.Join(tmp, "snap.sqlite")

	calls := []string{}
	setSyncDir(t, func(p string) error {
		calls = append(calls, p)
		return nil
	})

	r.NoError(Snapshot(context.Background(), db, dst))
	r.Contains(calls, tmp, "syncDir must be called on the destination's parent dir")
}

func TestSnapshotPathOpensWritableDSN(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src.sqlite")
	makeSourceDB(t, src) // initialize file
	dst := filepath.Join(tmp, "snap.sqlite")

	r.NoError(SnapshotPath(context.Background(), src, dst))
	r.True(integrityOk(t, dst))
}

func TestSnapshotPathErrorsOnMissingSource(t *testing.T) {
	tmp := t.TempDir()
	missing := filepath.Join(tmp, "no-such.sqlite")
	dst := filepath.Join(tmp, "snap.sqlite")
	err := SnapshotPath(context.Background(), missing, dst)
	require.Error(t, err)
}

func TestSnapshotSurfaceErrorFromSyncDir(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src.sqlite")
	db := makeSourceDB(t, src)
	dst := filepath.Join(tmp, "snap.sqlite")

	want := errors.New("synthetic")
	setSyncDir(t, func(string) error { return want })

	err := Snapshot(context.Background(), db, dst)
	r.ErrorIs(err, want)
}
```

- [ ] **Step 2: Run tests to verify failure**

Run: `go test ./internal/backup -run TestSnapshot -v`
Expected: build fails — `Snapshot` and `SnapshotPath` undefined.

- [ ] **Step 3: Implement `snapshot.go`**

```go
package backup

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// Snapshot writes a defragmented self-contained copy of db's contents to
// dst via VACUUM INTO. The destination must not already exist; collision
// is a hard error to prevent silent overwrite of a prior snapshot.
//
// Sequence:
//  1. mkdir -p parent of dst.
//  2. tmp := dst + ".partial". VACUUM INTO refuses to overwrite, so a
//     stale .partial from a prior crash surfaces here as a clear error;
//     retention sweep deletes .partial files older than 24h.
//  3. fsync the partial file.
//  4. os.Link(tmp, dst): atomic create-if-not-exists. Same filesystem
//     because tmp lives in dst's directory.
//  5. os.Remove(tmp): drop the link source.
//  6. syncDir on dst's parent for durable rename across power loss.
//
// The DB pool must be operating outside any active transaction; VACUUM
// INTO fails inside one.
func Snapshot(ctx context.Context, db *sql.DB, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return fmt.Errorf("mkdir snapshot dir: %w", err)
	}
	tmp := dst + ".partial"

	if _, err := db.ExecContext(ctx, "VACUUM INTO "+sqlQuoteLiteral(tmp)); err != nil {
		return fmt.Errorf("VACUUM INTO %s: %w", tmp, err)
	}

	if err := fsyncFile(tmp); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("fsync partial: %w", err)
	}

	if err := os.Link(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("link snapshot to dst: %w", err)
	}
	if err := os.Remove(tmp); err != nil {
		return fmt.Errorf("remove partial after link: %w", err)
	}
	if err := syncDir(filepath.Dir(dst)); err != nil {
		return fmt.Errorf("fsync snapshot dir: %w", err)
	}
	return nil
}

// SnapshotPath opens its own writable SQLite connection at srcDB
// (without running migrations) and runs Snapshot. Used by the CLI when
// the caller has only a path, not an existing pool.
func SnapshotPath(ctx context.Context, srcDB, dst string) error {
	if _, err := os.Stat(srcDB); err != nil {
		return fmt.Errorf("stat source DB: %w", err)
	}
	dsn := buildDSN(srcDB)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return fmt.Errorf("open source DB: %w", err)
	}
	defer db.Close()
	return Snapshot(ctx, db, dst)
}

// buildDSN returns a writable file: URI for path with the project's
// standard pragmas. Any reserved characters in path are percent-escaped
// via net/url so paths containing '?', '#', or whitespace are safe.
func buildDSN(path string) string {
	u := url.URL{Scheme: "file", Opaque: ""}
	// modernc.org/sqlite uses the path component, not Opaque.
	u.Path = path
	q := u.Query()
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	u.RawQuery = q.Encode()
	return u.String()
}

func fsyncFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
```

- [ ] **Step 4: Run tests to verify**

Run: `go test ./internal/backup -run TestSnapshot -v`
Expected: PASS for all six.

- [ ] **Step 5: Lint and commit**

Run: `make lint`
Expected: clean.

```bash
git add internal/backup/snapshot.go internal/backup/snapshot_test.go
git commit -m "Add backup.Snapshot and SnapshotPath"
```

---

## Task 4: `List` and `Snapshot` model

**Files:**
- Create: `internal/backup/list.go`
- Create: `internal/backup/list_test.go`

- [ ] **Step 1: Write failing tests**

Create `internal/backup/list_test.go`:

```go
package backup

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// makeFile writes name in dir with the given mtime via os.Chtimes,
// content is two random-looking bytes for size assertions.
func makeFile(t *testing.T, dir, name string, mtime time.Time) string {
	t.Helper()
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, []byte("xx"), 0o600))
	require.NoError(t, os.Chtimes(p, mtime, mtime))
	return p
}

func TestListReturnsNewestFirst(t *testing.T) {
	r := require.New(t)
	dir := t.TempDir()
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)

	makeFile(t, dir, "2026-04-25T11:00:00.000Z.sqlite", now.Add(-time.Hour))
	makeFile(t, dir, "2026-04-25T11:30:00.000Z.sqlite", now.Add(-30*time.Minute))
	makeFile(t, dir, "2026-04-25T11:45:00.000Z.sqlite", now.Add(-15*time.Minute))

	got, err := List(dir)
	r.NoError(err)
	r.Len(got, 3)
	r.Equal("2026-04-25T11:45:00.000Z.sqlite", filepath.Base(got[0].Path))
	r.Equal("2026-04-25T11:30:00.000Z.sqlite", filepath.Base(got[1].Path))
	r.Equal("2026-04-25T11:00:00.000Z.sqlite", filepath.Base(got[2].Path))
}

func TestListSkipsMalformedAndPartials(t *testing.T) {
	r := require.New(t)
	dir := t.TempDir()
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)

	makeFile(t, dir, "2026-04-25T11:00:00.000Z.sqlite", now)
	makeFile(t, dir, "not-a-snapshot.txt", now)
	makeFile(t, dir, "2026-04-25T11:00:00.000Z.sqlite.partial", now)
	makeFile(t, dir, "garbage.sqlite", now)

	got, err := List(dir)
	r.NoError(err)
	r.Len(got, 1)
	r.Equal("2026-04-25T11:00:00.000Z.sqlite", filepath.Base(got[0].Path))
}

func TestListEmptyDir(t *testing.T) {
	dir := t.TempDir()
	got, err := List(dir)
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestListMissingDirIsEmpty(t *testing.T) {
	got, err := List(filepath.Join(t.TempDir(), "no-such"))
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestListParsesTimestampsCorrectly(t *testing.T) {
	r := require.New(t)
	dir := t.TempDir()
	makeFile(t, dir, "2026-04-25T11:30:45.123Z.sqlite", time.Now())
	got, err := List(dir)
	r.NoError(err)
	r.Len(got, 1)
	r.Equal(time.Date(2026, 4, 25, 11, 30, 45, 123_000_000, time.UTC), got[0].Timestamp)
}
```

- [ ] **Step 2: Run tests to verify failure**

Run: `go test ./internal/backup -run TestList -v`
Expected: build fails — `List` and `Snapshot` (model type) undefined.

- [ ] **Step 3: Implement `list.go`**

```go
package backup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// stampLayout is the on-disk filename layout: RFC3339 with millisecond
// precision and a literal "Z" suffix. UTC.
const stampLayout = "2006-01-02T15:04:05.000Z"

// snapshotExt is the suffix every snapshot filename carries.
const snapshotExt = ".sqlite"

// Snapshot is a single retained snapshot.
type Snapshot struct {
	Path      string
	Timestamp time.Time
	Size      int64
}

// List enumerates valid snapshot files in dir, newest-first. Files
// whose names do not match the timestamp layout are skipped silently;
// .partial files are skipped. A missing directory returns an empty
// slice, not an error (callers may have a dir that the worker has not
// created yet).
func List(dir string) ([]Snapshot, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("readdir %s: %w", dir, err)
	}
	var out []Snapshot
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, snapshotExt) {
			continue
		}
		stamp := strings.TrimSuffix(name, snapshotExt)
		ts, err := time.Parse(stampLayout, stamp)
		if err != nil {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, Snapshot{
			Path:      filepath.Join(dir, name),
			Timestamp: ts,
			Size:      info.Size(),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Timestamp.After(out[j].Timestamp)
	})
	return out, nil
}
```

- [ ] **Step 4: Run tests to verify**

Run: `go test ./internal/backup -run TestList -v`
Expected: PASS for all five.

- [ ] **Step 5: Lint and commit**

```bash
git add internal/backup/list.go internal/backup/list_test.go
git commit -m "Add backup.List for newest-first snapshot enumeration"
```

---

## Task 5: `Sweep` retention

**Files:**
- Create: `internal/backup/retention.go`
- Create: `internal/backup/retention_test.go`

- [ ] **Step 1: Write failing tests**

Create `internal/backup/retention_test.go`:

```go
package backup

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func mkSnap(t *testing.T, dir string, ts time.Time) string {
	t.Helper()
	name := ts.UTC().Format(stampLayout) + snapshotExt
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, []byte("x"), 0o600))
	require.NoError(t, os.Chtimes(p, ts, ts))
	return p
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func surviving(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	slices.Sort(out)
	return out
}

func TestSweepKeeps15MinTier(t *testing.T) {
	r := require.New(t)
	dir := t.TempDir()
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)

	// Five snapshots at 15-min intervals; Keep15Min=4 means oldest is dropped.
	for i := 0; i < 5; i++ {
		mkSnap(t, dir, now.Add(-time.Duration(i)*15*time.Minute))
	}
	policy := Policy{Keep15Min: 4, KeepHourly: 24, KeepDaily: 7}
	res, err := Sweep(dir, policy, now, quietLogger())
	r.NoError(err)
	r.Equal(4, res.Kept15Min)
	r.Equal(0, res.KeptHourly)
	r.Equal(0, res.KeptDaily)
	// The 5th snapshot at age=60min (exactly 1h) is in the hourly tier,
	// not the 15-min tier. It survives as the only hourly representative.
	r.Equal(0, res.Deleted)
	r.Len(surviving(t, dir), 5)
}

func TestSweepDoesNotPromoteYoungSnapshotPastFullTier(t *testing.T) {
	r := require.New(t)
	dir := t.TempDir()
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	// Six snapshots within the last hour, two per 15-min slot.
	// Keep15Min=4 means we keep four (one per slot, newest each).
	mkSnap(t, dir, now.Add(-1*time.Minute))
	mkSnap(t, dir, now.Add(-2*time.Minute))
	mkSnap(t, dir, now.Add(-16*time.Minute))
	mkSnap(t, dir, now.Add(-17*time.Minute))
	mkSnap(t, dir, now.Add(-31*time.Minute))
	mkSnap(t, dir, now.Add(-46*time.Minute))
	policy := Policy{Keep15Min: 4, KeepHourly: 24, KeepDaily: 7}
	res, err := Sweep(dir, policy, now, quietLogger())
	r.NoError(err)
	r.Equal(4, res.Kept15Min)
	r.Equal(0, res.KeptHourly, "young snapshot whose 15-min slot is full must NOT be promoted to hourly")
	r.Equal(2, res.Deleted) // the two duplicates in the -1/-2 and -16/-17 slots
}

func TestSweepDeletesFutureSnapshots(t *testing.T) {
	r := require.New(t)
	dir := t.TempDir()
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	future := mkSnap(t, dir, now.Add(time.Hour))
	policy := Policy{Keep15Min: 4, KeepHourly: 24, KeepDaily: 7}
	_, err := Sweep(dir, policy, now, quietLogger())
	r.NoError(err)
	_, statErr := os.Stat(future)
	r.True(os.IsNotExist(statErr), "future snapshot must be deleted")
}

func TestSweepDeletesOlderThanWeek(t *testing.T) {
	r := require.New(t)
	dir := t.TempDir()
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	old := mkSnap(t, dir, now.Add(-8*24*time.Hour))
	policy := Policy{Keep15Min: 4, KeepHourly: 24, KeepDaily: 7}
	res, err := Sweep(dir, policy, now, quietLogger())
	r.NoError(err)
	r.Equal(1, res.Deleted)
	_, statErr := os.Stat(old)
	r.True(os.IsNotExist(statErr))
}

func TestSweepCleansPartialsOlderThan24h(t *testing.T) {
	r := require.New(t)
	dir := t.TempDir()
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)

	// Two .partial files: one 25h old (should be deleted), one 23h old (kept).
	old := filepath.Join(dir, "old.sqlite.partial")
	r.NoError(os.WriteFile(old, []byte("x"), 0o600))
	r.NoError(os.Chtimes(old, now.Add(-25*time.Hour), now.Add(-25*time.Hour)))

	young := filepath.Join(dir, "young.sqlite.partial")
	r.NoError(os.WriteFile(young, []byte("x"), 0o600))
	r.NoError(os.Chtimes(young, now.Add(-23*time.Hour), now.Add(-23*time.Hour)))

	policy := Policy{Keep15Min: 4, KeepHourly: 24, KeepDaily: 7}
	_, err := Sweep(dir, policy, now, quietLogger())
	r.NoError(err)

	_, statErr := os.Stat(old)
	r.True(os.IsNotExist(statErr), "25h-old partial must be deleted")
	_, statErr = os.Stat(young)
	r.NoError(statErr, "23h-old partial must be kept")
}

func TestSweepNilLoggerOK(t *testing.T) {
	dir := t.TempDir()
	policy := Policy{Keep15Min: 4, KeepHourly: 24, KeepDaily: 7}
	_, err := Sweep(dir, policy, time.Now(), nil)
	require.NoError(t, err)
}
```

- [ ] **Step 2: Run tests to verify failure**

Run: `go test ./internal/backup -run TestSweep -v`
Expected: build fails — `Policy`, `SweepResult`, `Sweep` undefined.

- [ ] **Step 3: Implement `retention.go`**

```go
package backup

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Policy is the per-tier retention count. A field of 0 retains nothing
// of that tier; config validation rejects that.
type Policy struct {
	Keep15Min  int
	KeepHourly int
	KeepDaily  int
}

// SweepResult describes the per-tier outcome of a single Sweep call.
// Kept counts are total retained per tier; Deleted is non-future
// deletions only. Future-snapshot deletions are emitted via slog
// warnings, not counted here.
type SweepResult struct {
	Kept15Min  int
	KeptHourly int
	KeptDaily  int
	Deleted    int
}

// Sweep applies tiered retention to dir. Snapshots are bucketed by age
// into 15-min / hourly / daily tiers; the newest representative of each
// slot is kept up to the per-tier count cap. Older-than-7d snapshots
// are deleted. Future-dated snapshots (negative age) are deleted with a
// warn-level log. Stale .partial files older than 24h are also deleted.
//
// Tier selection is decoupled from "bucket full": a young snapshot
// whose 15-min slots are saturated is deleted, NOT promoted into the
// hourly tier.
func Sweep(dir string, policy Policy, now time.Time, logger *slog.Logger) (SweepResult, error) {
	if logger == nil {
		logger = slog.Default()
	}
	files, err := List(dir)
	if err != nil {
		return SweepResult{}, fmt.Errorf("list snapshots: %w", err)
	}

	seen15 := map[time.Time]bool{}
	seenH := map[time.Time]bool{}
	seenD := map[time.Time]bool{}
	var res SweepResult

	for _, f := range files {
		age := now.Sub(f.Timestamp)
		if age < 0 {
			if rmErr := os.Remove(f.Path); rmErr != nil {
				logger.Warn("future snapshot delete failed",
					"path", f.Path, "err", rmErr)
				continue
			}
			logger.Warn("future snapshot deleted",
				"path", f.Path, "ts", f.Timestamp,
				"clock_skew_seconds", -int64(age.Seconds()))
			continue
		}

		keep := false
		switch {
		case age < time.Hour:
			slot := f.Timestamp.Truncate(15 * time.Minute)
			if !seen15[slot] && len(seen15) < policy.Keep15Min {
				seen15[slot] = true
				res.Kept15Min++
				keep = true
			}
		case age < 24*time.Hour:
			slot := f.Timestamp.Truncate(time.Hour)
			if !seenH[slot] && len(seenH) < policy.KeepHourly {
				seenH[slot] = true
				res.KeptHourly++
				keep = true
			}
		case age < 7*24*time.Hour:
			slot := f.Timestamp.Truncate(24 * time.Hour)
			if !seenD[slot] && len(seenD) < policy.KeepDaily {
				seenD[slot] = true
				res.KeptDaily++
				keep = true
			}
		}

		if !keep {
			if rmErr := os.Remove(f.Path); rmErr != nil {
				logger.Warn("retention delete failed",
					"path", f.Path, "err", rmErr)
				continue
			}
			res.Deleted++
		}
	}

	if err := cleanStalePartials(dir, now, logger); err != nil {
		return res, err
	}
	return res, nil
}

func cleanStalePartials(dir string, now time.Time, logger *slog.Logger) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("readdir for partials: %w", err)
	}
	const partialAgeCutoff = 24 * time.Hour
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, snapshotExt+".partial") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if now.Sub(info.ModTime()) <= partialAgeCutoff {
			continue
		}
		p := filepath.Join(dir, name)
		if rmErr := os.Remove(p); rmErr != nil {
			logger.Warn("partial cleanup failed", "path", p, "err", rmErr)
			continue
		}
		logger.Info("partial cleanup",
			"path", p, "age_hours", now.Sub(info.ModTime()).Hours())
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify**

Run: `go test ./internal/backup -run TestSweep -v`
Expected: PASS for all six.

- [ ] **Step 5: Lint and commit**

```bash
git add internal/backup/retention.go internal/backup/retention_test.go
git commit -m "Add backup.Sweep tiered retention"
```

---

## Task 6: `Restore`

**Files:**
- Create: `internal/backup/restore.go`
- Create: `internal/backup/restore_test.go`

- [ ] **Step 1: Write failing tests**

Create `internal/backup/restore_test.go`:

```go
package backup

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func makeBaselineDB(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	require.NoError(t, err)
	defer db.Close()
	// Mirror what db.Open would do in production: enable WAL, run a
	// schema_migrations bootstrap so subsequent db.Open's idempotent
	// migrations land cleanly.
	_, err = db.Exec("PRAGMA journal_mode=WAL")
	require.NoError(t, err)
	_, err = db.Exec("CREATE TABLE schema_migrations (version BIGINT NOT NULL PRIMARY KEY, dirty BOOLEAN NOT NULL)")
	require.NoError(t, err)
	_, err = db.Exec("CREATE TABLE example (k TEXT PRIMARY KEY, v TEXT)")
	require.NoError(t, err)
}

func putRow(t *testing.T, dbPath, k, v string) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer db.Close()
	_, err = db.Exec("INSERT OR REPLACE INTO example(k, v) VALUES (?, ?)", k, v)
	require.NoError(t, err)
}

func readRow(t *testing.T, dbPath, k string) string {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer db.Close()
	var v string
	require.NoError(t, db.QueryRow("SELECT v FROM example WHERE k = ?", k).Scan(&v))
	return v
}

func TestRestoreReplacesDB(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "live.sqlite")
	snapPath := filepath.Join(tmp, "snap.sqlite")
	lockPath := dbPath + ".lock"

	makeBaselineDB(t, dbPath)
	putRow(t, dbPath, "live", "before-restore")

	// Take a snapshot WHILE the db is still in initial state, then mutate.
	makeBaselineDB(t, snapPath)
	putRow(t, snapPath, "live", "from-snapshot")

	res, err := Restore(context.Background(), snapPath, dbPath, lockPath)
	r.NoError(err)
	r.Equal(snapPath, res.SnapshotPath)
	r.Equal(dbPath, res.DBPath)
	r.NotEmpty(res.PreRestoreSuffix)
	r.NotEmpty(res.MovedAside)
	// dbPath now contains snapshot's content
	r.Equal("from-snapshot", readRow(t, dbPath, "live"))
	// Pre-restore copy still exists
	preDB := dbPath + res.PreRestoreSuffix
	r.FileExists(preDB)
}

func TestRestoreRefusesIfLockHeld(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "live.sqlite")
	snapPath := filepath.Join(tmp, "snap.sqlite")
	lockPath := dbPath + ".lock"

	makeBaselineDB(t, dbPath)
	makeBaselineDB(t, snapPath)

	holder := flock.New(lockPath)
	ok, err := holder.TryLock()
	r.NoError(err)
	r.True(ok)
	t.Cleanup(func() { _ = holder.Unlock() })

	_, err = Restore(context.Background(), snapPath, dbPath, lockPath)
	r.ErrorIs(err, ErrServerHoldsLock)
}

func TestRestoreAllowsMissingDB(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "live.sqlite") // does not exist
	snapPath := filepath.Join(tmp, "snap.sqlite")
	lockPath := dbPath + ".lock"

	makeBaselineDB(t, snapPath)
	putRow(t, snapPath, "live", "fresh-from-snap")

	res, err := Restore(context.Background(), snapPath, dbPath, lockPath)
	r.NoError(err)
	r.Empty(res.MovedAside, "no files to move aside on flash-loss recovery")
	r.Equal("fresh-from-snap", readRow(t, dbPath, "live"))
}

func TestRestoreRejectsCorruptedSnapshot(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "live.sqlite")
	snapPath := filepath.Join(tmp, "snap.sqlite")
	lockPath := dbPath + ".lock"

	makeBaselineDB(t, dbPath)
	putRow(t, dbPath, "live", "still-live")
	r.NoError(os.WriteFile(snapPath, []byte("not-a-sqlite-file"), 0o600))

	_, err := Restore(context.Background(), snapPath, dbPath, lockPath)
	r.Error(err)
	// Original DB unchanged
	r.Equal("still-live", readRow(t, dbPath, "live"))
}

func TestRestoreRollbackOnSyncDirFailure(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "live.sqlite")
	snapPath := filepath.Join(tmp, "snap.sqlite")
	lockPath := dbPath + ".lock"

	makeBaselineDB(t, dbPath)
	putRow(t, dbPath, "live", "still-live")
	makeBaselineDB(t, snapPath)
	putRow(t, snapPath, "live", "would-have-been")

	// Inject a syncDir failure AFTER move-aside + copy succeed.
	want := errors.New("synthetic-syncdir")
	setSyncDir(t, func(string) error { return want })

	_, err := Restore(context.Background(), snapPath, dbPath, lockPath)
	r.Error(err)
	r.ErrorIs(err, want, "original error must surface")
	// Original DB content restored from rollback
	r.Equal("still-live", readRow(t, dbPath, "live"))
}

// These two test helpers cover the lock contract from the design and
// help reviewers verify the fix in T9 (server-side lock) interacts
// correctly with restore.
func TestRestoreRetriesAfterLockReleased(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "live.sqlite")
	snapPath := filepath.Join(tmp, "snap.sqlite")
	lockPath := dbPath + ".lock"

	makeBaselineDB(t, dbPath)
	makeBaselineDB(t, snapPath)
	putRow(t, snapPath, "live", "after-stop")

	holder := flock.New(lockPath)
	ok, err := holder.TryLock()
	r.NoError(err)
	r.True(ok)

	// First attempt: blocked.
	_, err = Restore(context.Background(), snapPath, dbPath, lockPath)
	r.ErrorIs(err, ErrServerHoldsLock)

	// Release: simulating server stop.
	r.NoError(holder.Unlock())

	// Retry now succeeds.
	res, err := Restore(context.Background(), snapPath, dbPath, lockPath)
	r.NoError(err)
	r.NotEmpty(res.MovedAside)
	r.Equal("after-stop", readRow(t, dbPath, "live"))
}

// Quiet a deprecation linter on unused time import in some test layouts.
var _ = time.Now
```

- [ ] **Step 2: Run tests to verify failure**

Run: `go test ./internal/backup -run TestRestore -v`
Expected: build fails — `Restore`, `RestoreResult` undefined.

- [ ] **Step 3: Implement `restore.go`**

```go
package backup

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"

	"github.com/wesm/fotobank/internal/db"
)

// RestoreResult describes a successful restore. PreRestoreSuffix is the
// common suffix appended to the moved-aside files (".pre-restore.{ms-
// timestamp}") so the operator can identify them in stderr output.
// MovedAside lists each file that was actually moved aside; missing
// sidecars (flash-loss recovery) are absent.
type RestoreResult struct {
	SnapshotPath     string
	DBPath           string
	PreRestoreSuffix string
	MovedAside       []string
}

// Restore replaces dbPath with the contents of snapshotPath, holding a
// non-blocking flock on lockPath for the duration to prevent races with
// a live server. On success, the previous DB and its sidecars are
// preserved at "{path}.pre-restore.{ns-timestamp}" — the operator
// deletes them when satisfied.
//
// Failure paths roll back: any post-move-aside failure restores the
// pre-restore trio so the operator's existing DB lands cleanly. Rollback
// errors are joined to the original via errors.Join — the original is
// the lead, with rollback errors as added context.
//
// A missing dbPath / dbPath-wal / dbPath-shm is tolerated (flash-loss
// recovery has nothing to move aside).
//
// Returns are named so the rollback-on-failure defer can wrap retErr
// with errors.Join.
func Restore(ctx context.Context, snapshotPath, dbPath, lockPath string) (res RestoreResult, retErr error) {
	// 1. Validate the snapshot file is a real SQLite DB.
	if err := ValidateSnapshot(ctx, snapshotPath); err != nil {
		return RestoreResult{}, fmt.Errorf("validate snapshot: %w", err)
	}

	// 2. Acquire the lifetime lock. Non-blocking; refuse if held.
	l := flock.New(lockPath)
	ok, err := l.TryLock()
	if err != nil {
		return RestoreResult{}, fmt.Errorf("flock: %w", err)
	}
	if !ok {
		return RestoreResult{}, fmt.Errorf("%w: %s", ErrServerHoldsLock, dbPath)
	}
	defer func() { _ = l.Unlock() }()

	// 3. Arm rollback BEFORE any move-aside. This guards against partial
	//    move-aside failure (first rename succeeds, second fails).
	suffix := ".pre-restore." + time.Now().UTC().Format("20060102T150405.000000000Z")
	var movedAside []string
	var success bool
	var openedDB *db.DB

	defer func() {
		if success {
			return
		}
		var rollbackErrs []error
		if openedDB != nil {
			rollbackErrs = append(rollbackErrs, openedDB.Close())
		}
		// Remove any freshly-installed files; ignore not-exist.
		// dbPath + ".incoming" is included to defend against a process
		// crash inside copyFile that strands the temp file — without
		// the rollback covering it, the next Restore would fail at
		// O_EXCL with a misleading "create incoming" error.
		for _, p := range []string{dbPath, dbPath + "-wal", dbPath + "-shm", dbPath + ".incoming"} {
			if rmErr := os.Remove(p); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
				rollbackErrs = append(rollbackErrs, fmt.Errorf("rollback remove %s: %w", p, rmErr))
			}
		}
		// Restore each moved-aside file, in reverse order.
		for i := len(movedAside) - 1; i >= 0; i-- {
			movedTo := movedAside[i]
			origPath := strings.TrimSuffix(movedTo, suffix)
			if rmErr := os.Rename(movedTo, origPath); rmErr != nil {
				rollbackErrs = append(rollbackErrs, fmt.Errorf("rollback rename %s -> %s: %w", movedTo, origPath, rmErr))
			}
		}
		// errors.Join wraps the original error with rollback context.
		// retErr is the named return, so this mutation IS observed by
		// the caller — that's why named returns are required here.
		if len(rollbackErrs) > 0 {
			retErr = errors.Join(append([]error{retErr}, rollbackErrs...)...)
		}
	}()

	// 4. Move-aside.
	for _, p := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		movedTo := p + suffix
		if rmErr := os.Rename(p, movedTo); rmErr != nil {
			if errors.Is(rmErr, os.ErrNotExist) {
				continue
			}
			return RestoreResult{}, fmt.Errorf("move aside %s: %w", p, rmErr)
		}
		movedAside = append(movedAside, movedTo)
	}

	// 5. Copy snapshot in atomically.
	if err := copyFile(snapshotPath, dbPath); err != nil {
		return RestoreResult{}, fmt.Errorf("copy snapshot: %w", err)
	}
	if err := syncDir(filepath.Dir(dbPath)); err != nil {
		return RestoreResult{}, fmt.Errorf("fsync db dir: %w", err)
	}

	// 6. Forward-port the schema. db.Open enables WAL and runs migrations
	//    idempotently. Close immediately on success so the server can
	//    open the DB freshly.
	openedDB, err = db.Open(dbPath)
	if err != nil {
		return RestoreResult{}, fmt.Errorf("open restored db: %w", err)
	}
	if err := openedDB.Close(); err != nil {
		openedDB = nil // already closed; don't double-close in rollback
		return RestoreResult{}, fmt.Errorf("close restored db: %w", err)
	}
	openedDB = nil

	// 7. Disarm rollback.
	success = true
	return RestoreResult{
		SnapshotPath:     snapshotPath,
		DBPath:           dbPath,
		PreRestoreSuffix: suffix,
		MovedAside:       append([]string(nil), movedAside...),
	}, nil
}

// ValidateSnapshot opens path with the same DSN as snapshot creation
// (mode=rw, no create) and runs PRAGMA integrity_check to confirm it's
// a real, intact SQLite database. Used by Restore as a precondition
// and by the CLI `backup restore --dry-run` so an operator finds out
// about a bad snapshot before any move-aside runs. Reads only — the
// rw mode is just to share buildDSN; integrity_check does not write.
func ValidateSnapshot(ctx context.Context, path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	dsn := buildDSN(path)
	d, err := sql.Open("sqlite", dsn)
	if err != nil {
		return fmt.Errorf("open snapshot: %w", err)
	}
	defer d.Close()
	var s string
	if err := d.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&s); err != nil {
		return fmt.Errorf("integrity_check: %w", err)
	}
	if s != "ok" {
		return fmt.Errorf("integrity_check returned %q", s)
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	defer in.Close()
	tmp := dst + ".incoming"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create incoming: %w", err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("copy: %w", err)
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("fsync incoming: %w", err)
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close incoming: %w", err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename incoming: %w", err)
	}
	return nil
}
```

Add `strings` to the import block.

- [ ] **Step 4: Run tests to verify**

Run: `go test ./internal/backup -run TestRestore -v`
Expected: PASS for all six.

- [ ] **Step 5: Lint and commit**

```bash
git add internal/backup/restore.go internal/backup/restore_test.go
git commit -m "Add backup.Restore with lock + rollback"
```

---

## Task 7: `Worker`

**Files:**
- Create: `internal/backup/worker.go`
- Create: `internal/backup/worker_test.go`

- [ ] **Step 1: Write failing tests**

Create `internal/backup/worker_test.go`:

```go
package backup

import (
	"bytes"
	"context"
	"database/sql"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestWorkerRunsTickAndExitsOnContextCancel(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src.sqlite")
	makeSourceDB(t, src)
	db, err := sql.Open("sqlite", src+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	r.NoError(err)
	t.Cleanup(func() { _ = db.Close() })

	dir := filepath.Join(tmp, "snaps")
	w := NewWorker(Config{
		DB:       db,
		Dir:      dir,
		Interval: 30 * time.Millisecond,
		Policy:   Policy{Keep15Min: 4, KeepHourly: 24, KeepDaily: 7},
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	require.Eventually(t, func() bool {
		entries, _ := os.ReadDir(dir)
		return len(entries) >= 1
	}, 2*time.Second, 10*time.Millisecond, "worker must produce at least one snapshot file")

	cancel()
	r.NoError(<-done, "Run must return nil after context cancel")
}

func TestWorkerLogsSnapshotSuccessFields(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src.sqlite")
	makeSourceDB(t, src)
	db, err := sql.Open("sqlite", src+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	r.NoError(err)
	t.Cleanup(func() { _ = db.Close() })

	var buf bytes.Buffer
	w := NewWorker(Config{
		DB:       db,
		Dir:      filepath.Join(tmp, "snaps"),
		Interval: 30 * time.Millisecond,
		Policy:   Policy{Keep15Min: 4, KeepHourly: 24, KeepDaily: 7},
		Logger:   slog.New(slog.NewTextHandler(&buf, nil)),
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	require.Eventually(t, func() bool {
		return strings.Contains(buf.String(), "backup snapshot ok")
	}, 2*time.Second, 10*time.Millisecond)
	cancel()
	<-done

	logs := buf.String()
	r.Contains(logs, "size_bytes=")
	r.Contains(logs, "duration_ms=")
	r.Contains(logs, "kept_15min=")
}

func TestWorkerStaleWarningSuppression(t *testing.T) {
	// Fake worker time via private field manipulation — start lastSuccessAt
	// at "now" and verify no stale warn fires before 48h elapse. Then jump
	// time forward and verify exactly one warn per 48h window.
	r := require.New(t)
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src.sqlite")
	makeSourceDB(t, src)
	db, err := sql.Open("sqlite", src+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	r.NoError(err)
	t.Cleanup(func() { _ = db.Close() })

	var buf bytes.Buffer
	w := NewWorker(Config{
		DB:       db,
		Dir:      filepath.Join(tmp, "snaps"),
		Interval: time.Hour, // never ticks during test
		Policy:   Policy{Keep15Min: 4, KeepHourly: 24, KeepDaily: 7},
		Logger:   slog.New(slog.NewTextHandler(&buf, nil)),
	})
	w.lastSuccessAt = time.Now().Add(-1 * time.Hour) // fresh; no warn
	w.maybeWarnStale(time.Now())
	r.NotContains(buf.String(), "backup snapshot stale")

	w.lastSuccessAt = time.Now().Add(-49 * time.Hour) // stale
	w.maybeWarnStale(time.Now())
	r.Contains(buf.String(), "backup snapshot stale")

	// Subsequent calls within the 48h suppression window do not re-warn.
	before := strings.Count(buf.String(), "backup snapshot stale")
	w.maybeWarnStale(time.Now())
	after := strings.Count(buf.String(), "backup snapshot stale")
	r.Equal(before, after, "stale warn must rate-limit to one per 48h")
}
```

- [ ] **Step 2: Run tests to verify failure**

Run: `go test ./internal/backup -run TestWorker -v`
Expected: build fails — `NewWorker`, `Worker`, `Config` undefined.

- [ ] **Step 3: Implement `worker.go`**

```go
package backup

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// Config configures a Worker. Production constructs one from
// config.Backup; tests construct one directly.
type Config struct {
	DB       *sql.DB
	Dir      string
	Interval time.Duration
	Policy   Policy
	Logger   *slog.Logger
}

// Worker takes periodic snapshots and runs retention sweeps. One
// goroutine, single serial loop. Skipped ticks coalesce naturally.
type Worker struct {
	cfg             Config
	lastSuccessAt   time.Time
	lastStaleWarnAt time.Time
}

// staleAfter is the threshold past which a worker that has not had a
// successful snapshot warns. staleSuppress is the minimum interval
// between consecutive stale warnings to keep the log readable.
const (
	staleAfter    = 48 * time.Hour
	staleSuppress = 48 * time.Hour
)

func NewWorker(cfg Config) *Worker {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Worker{cfg: cfg}
}

func (w *Worker) Run(ctx context.Context) error {
	w.cfg.Logger.Info("backup worker starting",
		"dir", w.cfg.Dir,
		"interval", w.cfg.Interval,
		"keep_15min", w.cfg.Policy.Keep15Min,
		"keep_hourly", w.cfg.Policy.KeepHourly,
		"keep_daily", w.cfg.Policy.KeepDaily)
	defer w.cfg.Logger.Info("backup worker stopped")

	// Suppress false-stale: treat "never succeeded" as "just succeeded"
	// for the first 48h of uptime.
	w.lastSuccessAt = time.Now()

	t := time.NewTicker(w.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case now := <-t.C:
			w.tick(ctx, now)
			w.maybeWarnStale(now)
		}
	}
}

func (w *Worker) tick(ctx context.Context, now time.Time) {
	dst := filepath.Join(w.cfg.Dir, now.UTC().Format(stampLayout)+snapshotExt)
	start := time.Now()
	if err := Snapshot(ctx, w.cfg.DB, dst); err != nil {
		w.cfg.Logger.Error("backup snapshot failed",
			"err", err, "dst", dst,
			"duration_ms", time.Since(start).Milliseconds())
		return
	}
	w.lastSuccessAt = time.Now()
	var size int64
	if info, err := os.Stat(dst); err == nil {
		size = info.Size()
	}

	res, err := Sweep(w.cfg.Dir, w.cfg.Policy, now, w.cfg.Logger)
	if err != nil {
		w.cfg.Logger.Warn("backup retention sweep failed", "err", err, "dir", w.cfg.Dir)
		// Snapshot still succeeded; don't suppress the success log.
	}

	w.cfg.Logger.Info("backup snapshot ok",
		"path", dst,
		"size_bytes", size,
		"duration_ms", time.Since(start).Milliseconds(),
		"kept_15min", res.Kept15Min,
		"kept_hourly", res.KeptHourly,
		"kept_daily", res.KeptDaily,
		"deleted", res.Deleted)
}

func (w *Worker) maybeWarnStale(now time.Time) {
	staleness := now.Sub(w.lastSuccessAt)
	if staleness <= staleAfter {
		return
	}
	if !w.lastStaleWarnAt.IsZero() && now.Sub(w.lastStaleWarnAt) < staleSuppress {
		return
	}
	w.cfg.Logger.Warn("backup snapshot stale",
		"last_success_at", w.lastSuccessAt,
		"hours_since", staleness.Hours())
	w.lastStaleWarnAt = now
}
```

- [ ] **Step 4: Run tests to verify**

Run: `go test ./internal/backup -run TestWorker -v`
Expected: PASS for all three.

- [ ] **Step 5: Lint and commit**

```bash
git add internal/backup/worker.go internal/backup/worker_test.go
git commit -m "Add backup.Worker single-loop scheduler"
```

---

## Task 8: CLI helpers `resolveDBPath` and `lockPathFor`

**Files:**
- Create: `internal/cli/dbpath.go`
- Create: `internal/cli/dbpath_test.go`

- [ ] **Step 1: Write failing tests**

Create `internal/cli/dbpath_test.go`:

```go
package cli

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/config"
)

func TestResolveDBPathHonorsEnvOverride(t *testing.T) {
	t.Setenv("FOTOBANK_DB_PATH", "/custom/path/db.sqlite")
	cfg := &config.Config{}
	require.Equal(t, "/custom/path/db.sqlite", resolveDBPath(cfg))
}

func TestResolveDBPathFallsBackToFlashRoot(t *testing.T) {
	t.Setenv("FOTOBANK_DB_PATH", "")
	cfg := &config.Config{}
	cfg.Flash.Root = "/tmp/flashroot"
	require.Equal(t, filepath.Join("/tmp/flashroot", "fotobank.sqlite"), resolveDBPath(cfg))
}

func TestLockPathForDerivesFromDBPath(t *testing.T) {
	require.Equal(t, "/var/db/fotobank.sqlite.lock", lockPathFor("/var/db/fotobank.sqlite"))
	require.Equal(t, "/custom/x.sqlite.lock", lockPathFor("/custom/x.sqlite"))
}
```

- [ ] **Step 2: Run tests to verify failure**

Run: `go test ./internal/cli -run "TestResolveDBPath|TestLockPathFor" -v`
Expected: build fails — `resolveDBPath`, `lockPathFor` undefined.

- [ ] **Step 3: Implement `dbpath.go`**

```go
package cli

import (
	"os"
	"path/filepath"

	"github.com/wesm/fotobank/internal/config"
)

// resolveDBPath returns the absolute SQLite path the server and CLI
// agree on. FOTOBANK_DB_PATH wins; otherwise default to
// {flash}/fotobank.sqlite. Mirrors the existing logic in runServer
// so that backup commands (and any future shared logic) cannot drift.
func resolveDBPath(cfg *config.Config) string {
	if v := os.Getenv("FOTOBANK_DB_PATH"); v != "" {
		return v
	}
	return filepath.Join(cfg.Flash.Root, "fotobank.sqlite")
}

// lockPathFor returns the canonical lock-file path for a given dbPath.
// Both the server and backup.Restore use this so the lock is consistent
// regardless of FOTOBANK_DB_PATH overrides.
func lockPathFor(dbPath string) string {
	return dbPath + ".lock"
}
```

- [ ] **Step 4: Run tests to verify pass**

Run: `go test ./internal/cli -run "TestResolveDBPath|TestLockPathFor" -v`
Expected: PASS for all three.

- [ ] **Step 5: Lint and commit**

```bash
git add internal/cli/dbpath.go internal/cli/dbpath_test.go
git commit -m "Add resolveDBPath and lockPathFor CLI helpers"
```

---

## Task 9: Wire server.go (lifetime flock + backup worker)

**Files:**
- Modify: `internal/cli/server.go`

The existing structure of `runServer` is in `internal/cli/server.go`. Currently lines 96-99 inline the DB-path resolution; lines 190-196 hold the broker init we just landed; the `bgWG` workers (flash janitor at ~208, thumb worker at ~218, share worker after the broker init).

This task does two things in one commit:
1. Replace the inline DB-path resolution with `resolveDBPath(cfg)`.
2. Acquire `flock(LOCK_EX|LOCK_NB)` on `lockPathFor(dbPath)` before any DB work, hold it for the lifetime of the server.
3. Spawn the backup worker as a `bgWG` goroutine when `cfg.Backup.Enabled`.

- [ ] **Step 1: Read the current relevant region**

Run: `sed -n '90,135p' /Users/wesm/code/fotobank/internal/cli/server.go`
Expected output: the DB-open block (`dbPath := os.Getenv("FOTOBANK_DB_PATH")` etc.) plus the broker init block we hoisted. Confirm shape before editing.

- [ ] **Step 2: Replace the inline DB-path resolution**

Find this block (around lines 96-99):

```go
	dbPath := os.Getenv("FOTOBANK_DB_PATH")
	if dbPath == "" {
		dbPath = filepath.Join(cfg.Flash.Root, "fotobank.sqlite")
	}
```

Replace with:

```go
	dbPath := resolveDBPath(cfg)
```

- [ ] **Step 3: Acquire the lifetime lock immediately after `dbPath` is resolved**

Insert AFTER `dbPath := resolveDBPath(cfg)` and BEFORE `d, err := db.Open(dbPath)`:

```go
	// Lifetime advisory lock. Refuses two servers on the same DB and
	// blocks `backup restore` while we're running. POSIX advisory locks
	// release automatically on process exit, so a crash does not strand
	// the lock.
	lockPath := lockPathFor(dbPath)
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return fmt.Errorf("mkdir lock dir: %w", err)
	}
	lockFile := flock.New(lockPath)
	ok, err := lockFile.TryLock()
	if err != nil {
		return fmt.Errorf("acquire lifetime lock: %w", err)
	}
	if !ok {
		return fmt.Errorf("another fotobank process is using %s", dbPath)
	}
	defer func() { _ = lockFile.Unlock() }()
```

If `flock` is not yet imported in `server.go`, add `"github.com/gofrs/flock"` to the import block.

- [ ] **Step 4: Spawn the backup worker**

Insert AFTER the existing share worker spawn block (right after `shareW := shareworker.New(shareCfg); bgWG.Go(...)`):

```go
	if cfg.Backup.Enabled {
		backupDir := cfg.Backup.Dir
		if backupDir == "" {
			backupDir = filepath.Join(cfg.NAS.Root, ".fotobank", "snapshots")
		}
		interval := 15 * time.Minute
		if raw := os.Getenv("FOTOBANK_TEST_BACKUP_INTERVAL"); raw != "" {
			if d, err := time.ParseDuration(raw); err == nil {
				interval = d
			} else {
				fmt.Fprintf(opts.stderr, "FOTOBANK_TEST_BACKUP_INTERVAL parse error: %v\n", err)
			}
		}
		bw := backup.NewWorker(backup.Config{
			DB:       d.WriteDB(),
			Dir:      backupDir,
			Interval: interval,
			Policy: backup.Policy{
				Keep15Min:  cfg.Backup.Keep15Min,
				KeepHourly: cfg.Backup.KeepHourly,
				KeepDaily:  cfg.Backup.KeepDaily,
			},
			Logger: logger,
		})
		bgWG.Go(func() {
			if err := bw.Run(sigCtx); err != nil && !errors.Is(err, context.Canceled) {
				fmt.Fprintln(opts.stderr, "backup worker exited:", err)
			}
		})
	}
```

If `backup` is not yet imported, add `"github.com/wesm/fotobank/internal/backup"` to the import block.

- [ ] **Step 5: Build the binary**

Run: `make build`
Expected: clean build to `bin/fotobank`.

- [ ] **Step 6: Run cli tests**

Run: `go test ./internal/cli -v`
Expected: all green; existing tests use `mode = "stub"` and the worker only runs when enabled (default true), but the snapshot dir lives under nasRoot which `e2e_*` tests provide.

The existing e2e tests do NOT assert backup behaviour; they should still pass because `bgWG.Wait()` includes the new worker and the worker exits cleanly on context cancel. If a test fails because the backup worker can't write to nasRoot, set `t.Setenv("FOTOBANK_TEST_BACKUP_INTERVAL", "")` and verify; but the worker default 15min cadence won't fire within a 1s test anyway.

- [ ] **Step 7: Run full suite**

Run: `go test ./...`
Expected: all green.

- [ ] **Step 8: Lint and commit**

```bash
git add internal/cli/server.go
git commit -m "Wire lifetime flock and backup worker into runServer"
```

---

## Task 10: CLI `backup snapshot/list/restore` subcommands

**Files:**
- Create: `internal/cli/backup.go`
- Create: `internal/cli/backup_test.go`

- [ ] **Step 1: Write failing tests**

Create `internal/cli/backup_test.go`:

```go
package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/cli"
)

// writeMinimalConfig creates a TOML config sufficient for backup commands.
func writeBackupConfig(t *testing.T, tmp string) string {
	t.Helper()
	cfgPath := filepath.Join(tmp, "fotobank.toml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(`
[nas]
root = "`+filepath.Join(tmp, "nas")+`"
[flash]
root = "`+filepath.Join(tmp, "flash")+`"
[identity]
mode = "stub"
[identity.stub]
hub = "local"
user_id = "alice"
handle = "Alice"
storage_key = "alice-sk"
[http]
listen_address = "127.0.0.1:0"
[imports]
file_lock_path = "`+filepath.Join(tmp, "import.lock")+`"
`), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(tmp, "nas"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(tmp, "flash"), 0o700))
	return cfgPath
}

func TestBackupSnapshotCLIWritesFile(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeBackupConfig(t, tmp)

	// Create the DB by running an import (or any DB-touching subcommand).
	// Use the existing `import` subcommand on an empty source dir; it
	// creates the DB and writes no media rows but does run migrations.
	srcDir := filepath.Join(tmp, "src-empty")
	r.NoError(os.MkdirAll(srcDir, 0o700))

	var so, se bytes.Buffer
	code := cli.RunContext(context.Background(),
		[]string{"import", "--config", cfgPath, srcDir},
		&so, &se)
	r.Equal(0, code, "import bootstrap failed: %s / %s", so.String(), se.String())

	// Now take a snapshot.
	out := filepath.Join(tmp, "snap.sqlite")
	so.Reset()
	se.Reset()
	code = cli.RunContext(context.Background(),
		[]string{"backup", "snapshot", "--config", cfgPath, "--out", out},
		&so, &se)
	r.Equal(0, code, "snapshot failed: %s / %s", so.String(), se.String())
	r.FileExists(out)
}

func TestBackupSnapshotCLIJSON(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeBackupConfig(t, tmp)
	srcDir := filepath.Join(tmp, "src-empty")
	r.NoError(os.MkdirAll(srcDir, 0o700))

	var so, se bytes.Buffer
	code := cli.RunContext(context.Background(),
		[]string{"import", "--config", cfgPath, srcDir},
		&so, &se)
	r.Equal(0, code)

	out := filepath.Join(tmp, "snap.sqlite")
	so.Reset()
	code = cli.RunContext(context.Background(),
		[]string{"backup", "snapshot", "--config", cfgPath, "--out", out, "--json"},
		&so, &se)
	r.Equal(0, code)

	var got struct {
		Path        string `json:"path"`
		SizeBytes   int64  `json:"size_bytes"`
		DurationMs  int64  `json:"duration_ms"`
		Timestamp   string `json:"timestamp"`
	}
	r.NoError(json.Unmarshal(so.Bytes(), &got))
	r.Equal(out, got.Path)
	r.Greater(got.SizeBytes, int64(0))
}

func TestBackupListCLI(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeBackupConfig(t, tmp)
	srcDir := filepath.Join(tmp, "src-empty")
	r.NoError(os.MkdirAll(srcDir, 0o700))

	var so, se bytes.Buffer
	cli.RunContext(context.Background(),
		[]string{"import", "--config", cfgPath, srcDir}, &so, &se)

	// Take two snapshots into the configured backup dir.
	cfgDir := filepath.Join(tmp, "nas", ".fotobank", "snapshots")
	r.NoError(os.MkdirAll(cfgDir, 0o700))
	for i := 0; i < 2; i++ {
		so.Reset()
		code := cli.RunContext(context.Background(),
			[]string{"backup", "snapshot", "--config", cfgPath}, &so, &se)
		r.Equal(0, code, se.String())
	}

	so.Reset()
	code := cli.RunContext(context.Background(),
		[]string{"backup", "list", "--config", cfgPath, "--json"}, &so, &se)
	r.Equal(0, code)

	var got []struct {
		Timestamp string `json:"timestamp"`
		SizeBytes int64  `json:"size_bytes"`
		Path      string `json:"path"`
	}
	r.NoError(json.Unmarshal(so.Bytes(), &got))
	r.Len(got, 2)
}

func TestBackupRestoreCLIRefusesWhileServerRuns(t *testing.T) {
	// Spin up the server in stub mode; while it runs, attempt a restore
	// against a different temp DB. The error must wrap ErrServerHoldsLock.
	// (Keeping this minimal — the exhaustive integration is in T11.)

	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeBackupConfig(t, tmp)

	// Bootstrap DB.
	srcDir := filepath.Join(tmp, "src-empty")
	r.NoError(os.MkdirAll(srcDir, 0o700))
	var so, se bytes.Buffer
	cli.RunContext(context.Background(),
		[]string{"import", "--config", cfgPath, srcDir}, &so, &se)

	// Take a snapshot to use as restore source.
	snap := filepath.Join(tmp, "snap.sqlite")
	cli.RunContext(context.Background(),
		[]string{"backup", "snapshot", "--config", cfgPath, "--out", snap},
		&so, &se)

	// Boot the server (will hold the lifetime lock).
	t.Setenv("FOTOBANK_CONFIG", cfgPath)
	sink := filepath.Join(tmp, "addr")
	t.Setenv("FOTOBANK_TEST_LISTEN_ADDR_SINK", sink)
	srvCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan int, 1)
	go func() {
		var so, se bytes.Buffer
		done <- cli.RunContext(srvCtx, []string{"server"}, &so, &se)
	}()

	// Wait for the server to bind (proves the lock is acquired).
	require.Eventually(t, func() bool {
		_, err := os.Stat(sink)
		return err == nil
	}, 3*1e9, 30*1e6)

	// Attempt restore; expect non-zero exit + stderr mentioning the lock.
	so.Reset()
	se.Reset()
	code := cli.RunContext(context.Background(),
		[]string{"backup", "restore", "--config", cfgPath, "--yes", snap},
		&so, &se)
	r.NotEqual(0, code)
	r.Contains(strings.ToLower(se.String()+so.String()),
		"another fotobank process",
		"stderr/stdout must explain the lock contention")

	cancel()
	<-done
}
```

- [ ] **Step 2: Run tests to verify failure**

Run: `go test ./internal/cli -run TestBackup -v`
Expected: build fails — `backup` subcommand not registered.

- [ ] **Step 3: Implement `backup.go`**

```go
package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
	"github.com/spf13/cobra"

	"github.com/wesm/fotobank/internal/backup"
	"github.com/wesm/fotobank/internal/config"
)

func newBackupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Snapshot, list, and restore the metadata DB",
	}
	cmd.AddCommand(newBackupSnapshotCmd())
	cmd.AddCommand(newBackupListCmd())
	cmd.AddCommand(newBackupRestoreCmd())
	return cmd
}

func newBackupSnapshotCmd() *cobra.Command {
	var out string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "snapshot",
		Short: "Take a one-shot snapshot of the metadata DB",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfigFromCmd(cmd)
			if err != nil {
				return err
			}
			dst := out
			if dst == "" {
				dir := backupDirFor(cfg)
				if err := os.MkdirAll(dir, 0o700); err != nil {
					return fmt.Errorf("mkdir backup dir: %w", err)
				}
				dst = filepath.Join(dir,
					time.Now().UTC().Format("2006-01-02T15:04:05.000Z")+".sqlite")
			}
			start := time.Now()
			if err := backup.SnapshotPath(cmd.Context(), resolveDBPath(cfg), dst); err != nil {
				return err
			}
			elapsed := time.Since(start)
			info, err := os.Stat(dst)
			if err != nil {
				return fmt.Errorf("stat snapshot: %w", err)
			}
			if asJSON {
				type out struct {
					Path        string `json:"path"`
					SizeBytes   int64  `json:"size_bytes"`
					DurationMs  int64  `json:"duration_ms"`
					Timestamp   string `json:"timestamp"`
				}
				return json.NewEncoder(cmd.OutOrStdout()).Encode(out{
					Path:       dst,
					SizeBytes:  info.Size(),
					DurationMs: elapsed.Milliseconds(),
					Timestamp:  info.ModTime().UTC().Format(time.RFC3339Nano),
				})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "snapshot ok: %s (%d bytes in %s)\n",
				dst, info.Size(), elapsed)
			return nil
		},
	}
	cmd.Flags().StringVar(&out, "out", "", "destination path (default: backup.dir/{ms-timestamp}.sqlite)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON to stdout")
	cmd.Flags().String("config", "", "path to config file")
	return cmd
}

func newBackupListCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all snapshots in the configured backup dir, newest-first",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfigFromCmd(cmd)
			if err != nil {
				return err
			}
			snaps, err := backup.List(backupDirFor(cfg))
			if err != nil {
				return err
			}
			if asJSON {
				type item struct {
					Timestamp string `json:"timestamp"`
					SizeBytes int64  `json:"size_bytes"`
					Path      string `json:"path"`
				}
				items := make([]item, 0, len(snaps))
				for _, s := range snaps {
					items = append(items, item{
						Timestamp: s.Timestamp.Format(time.RFC3339Nano),
						SizeBytes: s.Size,
						Path:      s.Path,
					})
				}
				return json.NewEncoder(cmd.OutOrStdout()).Encode(items)
			}
			for _, s := range snaps {
				fmt.Fprintf(cmd.OutOrStdout(), "%s  %d  %s\n",
					s.Timestamp.Format(time.RFC3339), s.Size, s.Path)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON to stdout")
	cmd.Flags().String("config", "", "path to config file")
	return cmd
}

func newBackupRestoreCmd() *cobra.Command {
	var yes, asJSON, dryRun bool
	cmd := &cobra.Command{
		Use:   "restore <snapshot-path>",
		Short: "Restore the metadata DB from a snapshot file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfigFromCmd(cmd)
			if err != nil {
				return err
			}
			snap := args[0]
			dbPath := resolveDBPath(cfg)
			lockPath := lockPathFor(dbPath)

			if dryRun {
				return restoreDryRun(cmd, snap, dbPath, lockPath, asJSON)
			}

			if !yes {
				if err := promptRestoreConfirmation(cmd, snap, dbPath); err != nil {
					return err
				}
			}

			res, err := backup.Restore(cmd.Context(), snap, dbPath, lockPath)
			if err != nil {
				return err
			}
			if asJSON {
				type out struct {
					RestoredFrom string   `json:"restored_from"`
					DBPath       string   `json:"db_path"`
					MovedAside   []string `json:"moved_aside"`
				}
				return json.NewEncoder(cmd.OutOrStdout()).Encode(out{
					RestoredFrom: res.SnapshotPath,
					DBPath:       res.DBPath,
					MovedAside:   res.MovedAside,
				})
			}
			fmt.Fprintf(cmd.OutOrStdout(),
				"restored from %s -> %s\nprevious DB moved aside:\n",
				res.SnapshotPath, res.DBPath)
			for _, p := range res.MovedAside {
				fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", p)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the interactive confirmation prompt")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON to stdout")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "validate + acquire lock only; no file changes")
	cmd.Flags().String("config", "", "path to config file")
	return cmd
}

func promptRestoreConfirmation(cmd *cobra.Command, snap, dbPath string) error {
	fmt.Fprintf(cmd.ErrOrStderr(),
		"Restore from %s into %s?\n"+
			"This will move the existing DB to {dbPath}.pre-restore.{timestamp}.\n"+
			"Type 'yes' to proceed: ",
		snap, dbPath)
	in := cmd.InOrStdin()
	var resp [4]byte
	n, _ := io.ReadFull(in, resp[:])
	if string(resp[:n]) != "yes\n" && string(resp[:n]) != "yes\r" {
		return errors.New("restore cancelled")
	}
	return nil
}

func restoreDryRun(cmd *cobra.Command, snap, dbPath, lockPath string, asJSON bool) error {
	// Validate snapshot integrity, not just existence — a corrupt or
	// non-SQLite file at snap would fail the actual restore, and the
	// dry-run must surface that pre-emptively. backup.ValidateSnapshot
	// (exported in T6) opens the file read-only and runs PRAGMA
	// integrity_check.
	if err := backup.ValidateSnapshot(cmd.Context(), snap); err != nil {
		return fmt.Errorf("validate snapshot: %w", err)
	}
	// Try to acquire and immediately release the lock.
	l := flockNew(lockPath)
	ok, err := l.TryLock()
	if err != nil {
		return fmt.Errorf("flock: %w", err)
	}
	if !ok {
		return fmt.Errorf("%w: %s", backup.ErrServerHoldsLock, dbPath)
	}
	_ = l.Unlock()
	if asJSON {
		type out struct {
			DryRun       bool   `json:"dry_run"`
			SnapshotPath string `json:"snapshot_path"`
			DBPath       string `json:"db_path"`
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(out{
			DryRun: true, SnapshotPath: snap, DBPath: dbPath,
		})
	}
	fmt.Fprintf(cmd.OutOrStdout(),
		"dry-run ok: snapshot %s would replace %s\n", snap, dbPath)
	return nil
}

// backupDirFor returns the configured backup dir, defaulting to
// {nas.root}/.fotobank/snapshots.
func backupDirFor(cfg *config.Config) string {
	if cfg.Backup.Dir != "" {
		return cfg.Backup.Dir
	}
	return filepath.Join(cfg.NAS.Root, ".fotobank", "snapshots")
}

// flockNew is the package-level constructor injection point so tests can
// stub the lock behaviour. Production calls flock.New directly.
var flockNew = func(path string) interface {
	TryLock() (bool, error)
	Unlock() error
} {
	return flock.New(path)
}
```

- [ ] **Step 4: Wire `backup` subcommand into root**

Find the root command construction (search for `root` or `cobra.Command` in `internal/cli/root.go`). Add `cmd.AddCommand(newBackupCmd())` next to the other subcommand registrations.

```bash
grep -n "AddCommand" /Users/wesm/code/fotobank/internal/cli/root.go
```

Add a new line `cmd.AddCommand(newBackupCmd())` adjacent to the existing AddCommand calls.

- [ ] **Step 5: Implement `loadConfigFromCmd` if missing**

Run: `grep -n "loadConfigFromCmd\|loadConfig\|config.Load" /Users/wesm/code/fotobank/internal/cli/*.go`

If a similar helper exists (e.g. `loadConfig`), reuse it. If not, add to `internal/cli/dbpath.go`:

```go
func loadConfigFromCmd(cmd *cobra.Command) (*config.Config, error) {
	cfgPath, _ := cmd.Flags().GetString("config")
	if cfgPath == "" {
		cfgPath = config.DefaultConfigPath()
	}
	return config.Load(cfgPath)
}
```

(If a helper already exists, replace the body of this with a call to it instead.)

- [ ] **Step 6: (no longer needed)**

The earlier draft introduced a `flockBackend` shim because the dry-run path
was meant to avoid importing `gofrs/flock`. Step 3 above now imports the
package at the top of the file and `flockNew` calls `flock.New` directly,
so this step is intentionally a no-op. Skip it.

```go
// (no code; this step is preserved as a numbered marker so subsequent
//  steps keep their numbers but adds nothing to backup.go.)
}
```

- [ ] **Step 7: Run tests to verify**

Run: `go test ./internal/cli -run TestBackup -v`
Expected: PASS for all four.

- [ ] **Step 8: Run full suite**

Run: `go test ./...`
Expected: all green.

- [ ] **Step 9: Lint and commit**

Run: `make lint`
Expected: clean.

```bash
git add internal/cli/backup.go internal/cli/backup_test.go internal/cli/root.go internal/cli/dbpath.go
git commit -m "Add backup snapshot/list/restore CLI subcommands"
```

---

## Task 11: Layer C integration test

**Files:**
- Create: `internal/cli/e2e_backup_test.go`

The Layer C test boots a real fotobank server with `FOTOBANK_TEST_BACKUP_INTERVAL=50ms`, watches for the first snapshot file to appear in `cfg.Backup.Dir`, asserts integrity_check on it, and exercises the lock-contention contract end-to-end.

- [ ] **Step 1: Write the integration test**

Create `internal/cli/e2e_backup_test.go`:

```go
package cli_test

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	"github.com/wesm/fotobank/internal/cli"
)

func TestE2EBackupWorkerProducesSnapshot(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e tests skipped under -short")
	}
	r := require.New(t)
	tmp := t.TempDir()
	nasRoot := filepath.Join(tmp, "nas")
	flashRoot := filepath.Join(tmp, "flash")
	r.NoError(os.MkdirAll(nasRoot, 0o700))
	r.NoError(os.MkdirAll(flashRoot, 0o700))

	dbPath := filepath.Join(flashRoot, "fotobank.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)

	cfgPath := filepath.Join(tmp, "fotobank.toml")
	r.NoError(os.WriteFile(cfgPath, []byte(`
[nas]
root = "`+nasRoot+`"
[flash]
root = "`+flashRoot+`"
[identity]
mode = "stub"
[identity.stub]
hub = "local"
user_id = "alice"
handle = "Alice"
storage_key = "alice-sk"
[http]
listen_address = "127.0.0.1:0"
[imports]
file_lock_path = "`+filepath.Join(tmp, "import.lock")+`"
`), 0o600))

	t.Setenv("FOTOBANK_CONFIG", cfgPath)
	t.Setenv("FOTOBANK_TEST_BACKUP_INTERVAL", "50ms")
	sink := filepath.Join(tmp, "addr")
	t.Setenv("FOTOBANK_TEST_LISTEN_ADDR_SINK", sink)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	done := make(chan int, 1)
	go func() {
		var so, se bytes.Buffer
		done <- cli.RunContext(ctx, []string{"server"}, &so, &se)
	}()

	addr := waitForSink(t, sink)
	r.NotEmpty(addr, "server did not bind")

	snapDir := filepath.Join(nasRoot, ".fotobank", "snapshots")
	require.Eventually(t, func() bool {
		entries, err := os.ReadDir(snapDir)
		if err != nil {
			return false
		}
		for _, e := range entries {
			if filepath.Ext(e.Name()) == ".sqlite" {
				return true
			}
		}
		return false
	}, 5*time.Second, 30*time.Millisecond,
		"backup worker must produce at least one snapshot within 5s")

	// Pick the newest snapshot and integrity-check it.
	entries, err := os.ReadDir(snapDir)
	r.NoError(err)
	var newest string
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".sqlite" && e.Name() > newest {
			newest = e.Name()
		}
	}
	r.NotEmpty(newest)
	snapPath := filepath.Join(snapDir, newest)

	d, err := sql.Open("sqlite", "file:"+snapPath+"?mode=ro")
	r.NoError(err)
	defer d.Close()
	var s string
	r.NoError(d.QueryRow("PRAGMA integrity_check").Scan(&s))
	r.Equal("ok", s, "snapshot must pass integrity_check")

	cancel()
	select {
	case ec := <-done:
		r.Equal(0, ec)
	case <-time.After(5 * time.Second):
		r.Fail("server did not shut down")
	}
}

func TestE2ERestoreRefusesWhileServerRuns(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e tests skipped under -short")
	}
	r := require.New(t)
	tmp := t.TempDir()
	nasRoot := filepath.Join(tmp, "nas")
	flashRoot := filepath.Join(tmp, "flash")
	r.NoError(os.MkdirAll(nasRoot, 0o700))
	r.NoError(os.MkdirAll(flashRoot, 0o700))

	dbPath := filepath.Join(flashRoot, "fotobank.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)

	cfgPath := filepath.Join(tmp, "fotobank.toml")
	r.NoError(os.WriteFile(cfgPath, []byte(`
[nas]
root = "`+nasRoot+`"
[flash]
root = "`+flashRoot+`"
[identity]
mode = "stub"
[identity.stub]
hub = "local"
user_id = "alice"
handle = "Alice"
storage_key = "alice-sk"
[http]
listen_address = "127.0.0.1:0"
[imports]
file_lock_path = "`+filepath.Join(tmp, "import.lock")+`"
`), 0o600))

	t.Setenv("FOTOBANK_CONFIG", cfgPath)
	t.Setenv("FOTOBANK_TEST_BACKUP_INTERVAL", "50ms")
	sink := filepath.Join(tmp, "addr")
	t.Setenv("FOTOBANK_TEST_LISTEN_ADDR_SINK", sink)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	done := make(chan int, 1)
	go func() {
		var so, se bytes.Buffer
		done <- cli.RunContext(ctx, []string{"server"}, &so, &se)
	}()

	addr := waitForSink(t, sink)
	r.NotEmpty(addr)

	// Wait for at least one snapshot to use as restore source.
	snapDir := filepath.Join(nasRoot, ".fotobank", "snapshots")
	require.Eventually(t, func() bool {
		entries, err := os.ReadDir(snapDir)
		if err != nil {
			return false
		}
		for _, e := range entries {
			if filepath.Ext(e.Name()) == ".sqlite" {
				return true
			}
		}
		return false
	}, 5*time.Second, 30*time.Millisecond)

	entries, err := os.ReadDir(snapDir)
	r.NoError(err)
	var snap string
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".sqlite" {
			snap = filepath.Join(snapDir, e.Name())
			break
		}
	}

	// Attempt restore; must fail with lock-held.
	var so, se bytes.Buffer
	code := cli.RunContext(context.Background(),
		[]string{"backup", "restore", "--config", cfgPath, "--yes", snap},
		&so, &se)
	r.NotEqual(0, code, "restore must fail while server holds the lock")

	cancel()
	<-done
}
```

- [ ] **Step 2: Run the integration tests**

Run: `go test ./internal/cli -run TestE2EBackup -v`
Expected: PASS in under 10 seconds.

- [ ] **Step 3: Verify -short skips them**

Run: `go test ./internal/cli -short -run TestE2EBackup -v`
Expected: SKIP, no failures.

- [ ] **Step 4: Run the full suite**

Run: `go test ./...`
Expected: all green.

- [ ] **Step 5: Lint and commit**

Run: `make lint`
Expected: clean.

```bash
git add internal/cli/e2e_backup_test.go
git commit -m "Add Layer C integration test for backup worker and restore lock"
```

---

## Task 12: Update legacy planning docs

**Files:**
- Modify: `docs/superpowers/specs/2026-04-22-fotobank-go-core-phase1-design.md`
- Modify: `docs/superpowers/plans/2026-04-22-fotobank-go-core-plan-a-foundation.md`

These contain the legacy backup schema. Future readers should see the new shape.

- [ ] **Step 1: Find the legacy references**

Run:
```
grep -n "snapshot_interval\|snapshot_retention\|wal_shipping" \
  docs/superpowers/specs/2026-04-22-fotobank-go-core-phase1-design.md \
  docs/superpowers/plans/2026-04-22-fotobank-go-core-plan-a-foundation.md
```

- [ ] **Step 2: Replace the spec snippet**

In `docs/superpowers/specs/2026-04-22-fotobank-go-core-phase1-design.md`, find the `[backup]` block (likely a TOML example with `snapshot_interval = "15m"` etc.) and replace its body with:

```toml
[backup]
# enabled = true
# dir = ""                           # absolute path; default {nas.root}/.fotobank/snapshots/
# keep_15min = 4
# keep_hourly = 24
# keep_daily = 7
```

Preserve any surrounding prose.

- [ ] **Step 3: Replace the plan snippet**

In `docs/superpowers/plans/2026-04-22-fotobank-go-core-plan-a-foundation.md`, find the `Backup` Go struct (likely `SnapshotInterval time.Duration` etc.) and replace it with the current shape:

```go
type Backup struct {
    Enabled    bool   `toml:"enabled"`
    Dir        string `toml:"dir"`
    Keep15Min  int    `toml:"keep_15min"`
    KeepHourly int    `toml:"keep_hourly"`
    KeepDaily  int    `toml:"keep_daily"`
}
```

- [ ] **Step 4: Verify nothing else in live code references the old fields**

Run:
```
grep -rn "SnapshotInterval\|SnapshotRetention\|WALShipping\|snapshot_interval\|snapshot_retention\|wal_shipping" internal cmd
```

Expected: zero matches under `internal/` and `cmd/`. The brokerexec design and this plan itself contain intentional historical references in their migration sections; those grep paths are intentionally narrow.

- [ ] **Step 5: Commit**

```bash
git add docs/superpowers/specs/2026-04-22-fotobank-go-core-phase1-design.md \
        docs/superpowers/plans/2026-04-22-fotobank-go-core-plan-a-foundation.md
git commit -m "Update legacy plan/spec docs for new backup schema"
```

---

## Final verification

- [ ] **Run full lint and tests**

Run: `make lint && go test ./...`
Expected: zero warnings, all green.

- [ ] **Build and smoke test the binary**

Run: `make build && bin/fotobank backup --help`
Expected: subcommand help prints with `snapshot`, `list`, `restore`.

```bash
bin/fotobank backup snapshot --help
bin/fotobank backup list --help
bin/fotobank backup restore --help
```

Expected: each subcommand prints its flags.

- [ ] **Verify snapshot end-to-end manually (optional)**

Create a fresh smoke config:

```toml
[nas]
root = "/tmp/fb-backup-smoke-nas"
[flash]
root = "/tmp/fb-backup-smoke-flash"
[identity]
mode = "stub"
[identity.stub]
hub = "local"
user_id = "alice"
handle = "Alice"
storage_key = "alice-sk"
```

```bash
mkdir -p /tmp/fb-backup-smoke-nas /tmp/fb-backup-smoke-flash
FOTOBANK_CONFIG=/tmp/smoke.toml bin/fotobank import /dev/null   # bootstrap DB
FOTOBANK_CONFIG=/tmp/smoke.toml bin/fotobank backup snapshot
FOTOBANK_CONFIG=/tmp/smoke.toml bin/fotobank backup list
```

Expected: snapshot lands in `/tmp/fb-backup-smoke-nas/.fotobank/snapshots/`; list shows it.
