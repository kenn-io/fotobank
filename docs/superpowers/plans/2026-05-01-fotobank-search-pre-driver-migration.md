# Fotobank Search v1 — Pre-Step Driver Migration Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` to implement this plan task-by-task. **Every subagent dispatched for this plan uses the Opus model** — pass `model: "opus"` to the Agent tool on every dispatch (the `model` parameter overrides the agent definition's default). Steps use checkbox (`- [ ]`) syntax for tracking. **After every 5th task, invoke `/roborev-fix` on the current branch as a cleanup checkpoint.** Do not start the next 5-task chunk until the cleanup checkpoint completes.

**Goal:** Swap fotobank's SQLite driver from `modernc.org/sqlite` (pure Go) to `mattn/go-sqlite3` + the `sqlite-vec` auto-extension (CGO), with the database, backup/restore, time scanning, test infrastructure, build pipeline, and documentation all verified end-to-end. **No search work begins until this plan completes and a green review gate is passed.**

**Architecture:** One driver registration for the whole app via a `database/sql` driver name change (`"sqlite"` → `"sqlite3"`). DSN format moves from modernc's `?_pragma=…` query keys to mattn's `?_busy_timeout=…&_fk=…` keys. The `sqlite-vec` extension is registered exactly once per process via `sqlite_vec.Auto()` guarded by `sync.Once`, invoked from a single helper that both the production `db.Open` and `testutil.OpenTestDB` call before the first connection opens. Time-column scanning under mattn returns native `time.Time` for both direct and COALESCE'd selects, so the modernc-specific `parseSQLiteTimeString` helper goes away. CGO becomes part of the build pipeline; pre-commit and (if/when) CI need a CGO toolchain available.

**Tech Stack:** `github.com/mattn/go-sqlite3` (driver), `github.com/asg017/sqlite-vec-go-bindings/cgo` (auto-extension), `database/sql`, Go's stdlib `time`, `golang-migrate` (already in use).

**Reference docs:**
- Spec: `docs/superpowers/specs/2026-05-01-fotobank-search-design.md` §4
- Pre-alpha schema policy (per `CLAUDE.md`): edit `internal/db/migrations/000001_initial_schema.up.sql` in place; matching `.down.sql` is a wholesale teardown.
- This plan does **not** touch the schema; the schema additions for search land in the v1 search plan.

---

## File structure

Backend (modified — no new packages, but one new file):

| File | Responsibility |
|---|---|
| `go.mod`, `go.sum` | (modify) add `github.com/mattn/go-sqlite3` and `github.com/asg017/sqlite-vec-go-bindings/cgo`; remove `modernc.org/sqlite` |
| `internal/db/sqlitevec.go` | (new) one-time `sqlite-vec` auto-extension registration helper |
| `internal/db/db.go` | (modify) driver name `"sqlite" → "sqlite3"`, DSN format swap, calls registration helper |
| `internal/db/db_test.go` | (modify) regression tests for null-time + TZ round-trips, concurrent-write smoke |
| `internal/backup/snapshot.go` | (modify) DSN format swap on the snapshot-side DSN |
| `internal/backup/snapshot_test.go`, `restore_test.go`, `worker_test.go` | (modify) drop `_ "modernc.org/sqlite"` import (db package handles registration) |
| `internal/cli/e2e_backup_test.go`, `e2e_brokerexec_test.go`, `e2e_shared_test.go` | (modify) drop `_ "modernc.org/sqlite"` import |
| `internal/share/repo.go` | (modify) remove `parseSQLiteTimeString`; replace caller with direct `time.Time` Scan |
| `internal/share/repo_test.go` | (modify) verify the replaced Scan path |
| `internal/testutil/testdb.go` | (modify) call `db.RegisterSqliteVec()` once before first `Open` |
| `Makefile` | (no change expected; `go build` picks up CGO automatically because the deps require it) |
| `prek.toml` | (no change unless `go test (short)` exceeds budget; budget-check task verifies) |
| `CLAUDE.md` | (modify) replace the "no CGO" line; document cross-build expectation |

Test plan (the verification surface for this migration):

| Test | What it proves |
|---|---|
| `internal/db/db_test.go::TestOpen_RoundTripNullableTime` | Mattn round-trips `*time.Time` (nil and non-nil) without losing TZ |
| `internal/db/db_test.go::TestOpen_RoundTripCoalescedTime` | A `COALESCE(ts1, ts2)` expression scans into `time.Time` directly (the modernc-specific reason `parseSQLiteTimeString` existed is gone) |
| `internal/db/db_test.go::TestOpen_ConcurrentWriters` | Two goroutines hammering through the RW pool succeed under the new busy-timeout DSN |
| `internal/db/sqlitevec_test.go::TestRegistered` | `vec_version()` returns a non-empty string after `RegisterSqliteVec()` |
| `internal/backup/restore_test.go::TestRestore_RoundTrip` | `VACUUM INTO` snapshot + restore re-open path works under mattn's connection lifecycle |
| `internal/share/repo_test.go::TestListByOwner_*` | The previously-COALESCE'd time path scans cleanly without `parseSQLiteTimeString` |

---

## Conventions and warnings for the executor

- **Go:** `database/sql`. Driver name is now `"sqlite3"` (mattn); never mix `"sqlite"` and `"sqlite3"` registrations in one process. The driver import that registers the name is `_ "github.com/mattn/go-sqlite3"`. Do **not** add this import in test files: the production `internal/db` package owns the import, and that's the only place that registers the driver.
- **CGO:** The deps require CGO. `go build` will fail with a clear error if `CC` is unset on a host without a default compiler. macOS dev machines have Xcode CLT; Linux dev/CI hosts need `build-essential` or equivalent.
- **DSN format under mattn:**
  - `_busy_timeout=5000` (was `_pragma=busy_timeout(5000)`)
  - `_fk=1` (was `_pragma=foreign_keys(1)`)
  - `_journal=WAL` is set on the RW pool too, so the `PRAGMA journal_mode=WAL` exec after open can stay or move into the DSN — keep the explicit exec for clarity (one less semantics shift in this PR).
  - Read-only: `mode=ro` is the same on both drivers.
  - Filenames with special characters need the `file:` URI prefix (the existing read-only DSN already uses it; the RW DSN does not — fine because dev/test paths are simple).
- **Sqlite-vec registration:** `RegisterSqliteVec()` is a `sync.Once`-guarded wrapper around `sqlite_vec.Auto()`. Calling it twice is a no-op; calling it after the first `sql.Open("sqlite3", …)` is **too late** and the next `Open` would return a connection without the extension. Always call it before any `sql.Open` in a process. The migration plan does **not** create any vec0 virtual tables — that lands in the search plan. The point of registering it now is to prove the toolchain works.
- **Tests:** `testutil.OpenTestDB(t)` continues to be the canonical entry point. After this plan, it transparently registers `sqlite-vec` once per test process. No test should call `sqlite_vec.Auto()` directly.
- **Time scanning under mattn:** mattn's default time-column behavior is to scan into `time.Time` natively, including for COALESCE'd expressions. The current `parseSQLiteTimeString` helper exists because modernc loses TIMESTAMP affinity through expressions and returns the Go `Time.String()` output as a string. After the swap, the helper goes away **and** its caller swaps to a direct `Scan(&t)` against a `time.Time` — verify with the regression test before deleting the helper.
- **Migrations:** `golang-migrate` works with both drivers because it uses the `database/sql` interface. The internal driver-name string in `internal/db/migrations.go` (or wherever it picks the migrate driver) needs to flip from `"sqlite"` to `"sqlite3"`. Check `internal/db/migrations.go` carefully — there's exactly one driver-name reference there.
- **Backup:** `internal/backup/snapshot.go` opens a separate DB handle to issue `VACUUM INTO`. Its DSN needs the same treatment as the live DB's DSN. The restore path opens the snapshot DB read-only with mode=ro; same DSN swap.
- **Existing `_ "modernc.org/sqlite"` imports:** Six locations carry side-effect imports for the old driver. After the swap, the **only** package that imports the driver (mattn) is `internal/db`. Test files and other packages reach the registered driver transitively through `db.Open`, so they no longer need their own driver imports. Removing them tightens the dependency surface.
- **Commits:** conventional, ≤72 char subject, scoped (`refactor(db):`, `chore(deps):`, `test(db):`, `docs:`). One logical change per commit; cleanup checkpoints commit their own follow-up fixes if any.
- **Pre-commit hooks:** `make test-short` runs in the `pre-commit` stage. CGO compile is meaningfully slower than pure-Go on a cold cache; budget-check task verifies the warm-cache budget hasn't blown the prek hook timeout. If profiling shows it has, raise the timeout (or split test-short to a faster subset) — do **not** bypass with `--no-verify`.
- **Cross-build:** `GOOS=linux GOARCH=amd64 go build` from a Mac requires a `CC` cross-compiler. `zig cc` or `musl-cross` are the usual choices. We're not solving cross-build in this plan beyond documenting the expectation in `CLAUDE.md` so the next-time-you-deploy path is reproducible.

---

## Section A — Dependencies and the registration helper

### Task 1 — Add mattn/go-sqlite3 and sqlite-vec dependencies

**Files:**
- Modify: `go.mod`, `go.sum`

**Step 1: Add the new module dependencies.**

```bash
go get github.com/mattn/go-sqlite3@latest
go get github.com/asg017/sqlite-vec-go-bindings@latest
```

`sqlite-vec-go-bindings` exposes two import paths: `.../cgo` (used here, ships the C extension as a CGO bind that auto-registers) and `.../ncruces` (a pure-Go variant we don't use). Verify with `go doc`:

```bash
go doc github.com/asg017/sqlite-vec-go-bindings/cgo Auto
```
Expected: a function signature `func Auto()` documented as registering the extension at next-Open time.

**Step 2: Verify the toolchain compiles.**

Run: `go build ./...`

If `cgo: C compiler "cc" not found`, install Xcode CLT (`xcode-select --install` on macOS) or `build-essential` on Linux. Re-run.

Expected output: build succeeds (CGO link warnings are fine; errors from the actual swap come in later tasks because the driver name hasn't moved yet).

**Step 3: Run `go mod tidy`.**

Run: `make tidy`

Expected: `go.sum` updates with mattn + sqlite-vec checksums; `modernc.org/*` lines stay (still imported until task 3).

**Step 4: Smoke `go test ./internal/db/`.**

Run: `go test ./internal/db/ -run "TestOpen$" -count=1`

Expected: PASS (the existing tests still use modernc until task 3).

**Step 5: Commit.**

```bash
git add go.mod go.sum
git commit -m "chore(deps): add mattn/go-sqlite3 and sqlite-vec"
```

---

### Task 2 — Sqlite-vec auto-extension registration helper

**Files:**
- Create: `internal/db/sqlitevec.go`
- Create: `internal/db/sqlitevec_test.go`

**Step 1: Write the failing test.**

```go
// internal/db/sqlitevec_test.go
package db_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wesm/fotobank/internal/db"
)

// TestRegisterSqliteVec_LoadsExtension proves that calling
// RegisterSqliteVec() before the first sql.Open registers the
// sqlite-vec extension on every subsequent connection. The smoke test
// is `SELECT vec_version()`, which is only available when the
// extension is loaded.
func TestRegisterSqliteVec_LoadsExtension(t *testing.T) {
	t.Helper()
	db.RegisterSqliteVec()

	dsn := "file:" + filepath.Join(t.TempDir(), "vec_smoke.sqlite") +
		"?_busy_timeout=5000&_fk=1"
	conn, err := sql.Open("sqlite3", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	var version string
	err = conn.QueryRow(`SELECT vec_version()`).Scan(&version)
	require.NoError(t, err, "vec_version() must work after RegisterSqliteVec")
	require.NotEmpty(t, version)
}

// TestRegisterSqliteVec_Idempotent proves that calling
// RegisterSqliteVec twice is a no-op.
func TestRegisterSqliteVec_Idempotent(t *testing.T) {
	db.RegisterSqliteVec()
	db.RegisterSqliteVec()
}
```

**Step 2: Run the test to verify it fails.**

Run: `go test ./internal/db/ -run "TestRegisterSqliteVec" -v`

Expected: FAIL — `db.RegisterSqliteVec` undefined.

**Step 3: Implement.**

```go
// internal/db/sqlitevec.go
package db

import (
	"sync"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	_ "github.com/mattn/go-sqlite3"
)

// registerOnce guards sqlite_vec.Auto so the extension is registered
// exactly once per process. Calling Auto twice would re-register and
// return an error from sqlite3 on the second registration; the
// sync.Once turns extra calls into no-ops without surfacing the error
// to callers.
var registerOnce sync.Once

// RegisterSqliteVec registers the sqlite-vec extension as an
// auto-extension on the mattn/go-sqlite3 driver. After this call,
// every subsequent sql.Open("sqlite3", …) loads sqlite-vec into the
// new connection.
//
// MUST be called before the first sql.Open in a process. Calling it
// after some connections have been opened would not retroactively
// register the extension on those connections.
func RegisterSqliteVec() {
	registerOnce.Do(sqlite_vec.Auto)
}
```

**Step 4: Run the test to verify it passes.**

Run: `go test ./internal/db/ -run "TestRegisterSqliteVec" -v`

Expected: PASS for both `TestRegisterSqliteVec_LoadsExtension` and `TestRegisterSqliteVec_Idempotent`.

**Step 5: Commit.**

```bash
git add internal/db/sqlitevec.go internal/db/sqlitevec_test.go
git commit -m "feat(db): sqlite-vec auto-extension registration helper"
```

---

## Section B — Driver swap

### Task 3 — Swap `internal/db` to mattn driver + DSN

**Files:**
- Modify: `internal/db/db.go`
- Modify: `internal/db/migrations.go` (driver-name reference for `golang-migrate`)
- Modify: `internal/db/db_test.go` (existing tests — driver-name update if any literal references)

**Step 1: Find the migrate driver-name reference.**

Run: `grep -n '"sqlite"' internal/db/`

Expected: at least one match in `internal/db/migrations.go` (the `golang-migrate` `WithInstance` driver factory selection). Capture the line number for step 3.

**Step 2: Run the existing db tests to capture the green baseline.**

Run: `go test ./internal/db/ -count=1`

Expected: PASS. (We will reproduce this PASS after the swap.)

**Step 3: Apply the swap.**

In `internal/db/db.go`:

- Remove `_ "modernc.org/sqlite"`. Add nothing in its place — the driver is now registered by `internal/db/sqlitevec.go` via the transitive `_ "github.com/mattn/go-sqlite3"` import.
- In `Open(path string)`, call `RegisterSqliteVec()` as the first line.
- Change `sql.Open("sqlite", rwDSN)` to `sql.Open("sqlite3", rwDSN)`.
- Change `sql.Open("sqlite", roDSN)` to `sql.Open("sqlite3", roDSN)`.
- Change the RW DSN from `path + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"` to `path + "?_busy_timeout=5000&_fk=1"`.
- Change the RO DSN from `"file:" + path + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&mode=ro"` to `"file:" + path + "?_busy_timeout=5000&_fk=1&mode=ro"`.
- Keep the explicit `PRAGMA journal_mode=WAL` exec — it's clearer than smuggling it into the DSN, and it survives both drivers identically.

In `internal/db/migrations.go`, the `golang-migrate` factory call (look for `sqlite.WithInstance` or `sqlite3.WithInstance`):

- If the import is `github.com/golang-migrate/migrate/v4/database/sqlite` (modernc-friendly), swap it to `github.com/golang-migrate/migrate/v4/database/sqlite3` (mattn-friendly). Both packages exist; sqlite3 is the right pair for the mattn driver.

**Step 4: Run the existing db tests under the new driver.**

Run: `go test ./internal/db/ -count=1`

Expected: PASS. If a test fails on a TIMESTAMP scan, that's the time-scan audit territory — capture the failing test name for Task 6 and continue.

**Step 5: Smoke the rest of the build.**

Run: `go build ./...`

Expected: build succeeds with no driver-name compile errors. Linker warnings about CGO are fine.

**Step 6: Commit.**

```bash
git add internal/db/db.go internal/db/migrations.go internal/db/db_test.go
git commit -m "refactor(db): swap to mattn/go-sqlite3 driver and DSN"
```

---

### Task 4 — Drop modernc imports from backup and CLI test files

**Files:**
- Modify: `internal/backup/snapshot.go`
- Modify: `internal/backup/restore_test.go`, `snapshot_test.go`, `worker_test.go`
- Modify: `internal/cli/e2e_backup_test.go`, `e2e_brokerexec_test.go`, `e2e_shared_test.go`

**Step 1: Survey.**

Run: `grep -rln '_ "modernc.org/sqlite"' .`

Expected: exactly the seven files listed above. Capture the list for step 2.

**Step 2: Remove the side-effect import from each file.**

In every file, delete the line `_ "modernc.org/sqlite"`. The driver is registered transitively via `internal/db/sqlitevec.go` → `_ "github.com/mattn/go-sqlite3"` — every file in this list either uses `db.Open` or routes through `internal/db`, so the registration arrives transitively.

In `internal/backup/snapshot.go`, also rewrite the snapshot-side DSN that passes `_pragma=busy_timeout(5000)` and `_pragma=foreign_keys(1)`:

```go
// before
q.Add("_pragma", "busy_timeout(5000)")
q.Add("_pragma", "foreign_keys(1)")
```

becomes

```go
q.Add("_busy_timeout", "5000")
q.Add("_fk", "1")
```

And the `sql.Open` call in `internal/backup/snapshot.go` flips from `"sqlite"` to `"sqlite3"`.

**Step 3: Verify each modified file compiles individually.**

Run: `go build ./internal/backup/... ./internal/cli/...`

Expected: build succeeds.

**Step 4: Run the affected test suites.**

Run: `go test ./internal/backup/... -count=1`
Run: `go test ./internal/cli/... -count=1 -run "TestE2E"`

Expected: PASS. CLI e2e suites take longer; if they time out under default `go test` timeout, run with `-timeout=180s`.

**Step 5: Commit.**

```bash
git add internal/backup/ internal/cli/e2e_backup_test.go internal/cli/e2e_brokerexec_test.go internal/cli/e2e_shared_test.go
git commit -m "refactor(db): drop redundant modernc imports across packages"
```

---

### Task 5 — Backup snapshot/restore DSN sanity test

**Files:**
- Modify: `internal/backup/snapshot_test.go` or `internal/backup/restore_test.go`

This task verifies the snapshot DB DSN under mattn — specifically that pragma defaults are applied (busy_timeout, foreign_keys) on the standalone snapshot connection that the restore path opens.

**Step 1: Write the failing test.**

```go
// internal/backup/snapshot_test.go (append)

// TestSnapshotPragmas_AfterRestore_RoundTrip proves that a snapshot
// created with VACUUM INTO and re-opened via the restore-side DSN
// honours the same busy_timeout and foreign_keys defaults as the live
// DB. Foreign-key enforcement is the one that bites silently — a
// snapshot opened without _fk=1 would let a child-row delete cascade
// disappear in tests that assert on FK behavior.
func TestSnapshotPragmas_AfterRestore_RoundTrip(t *testing.T) {
	d := testutil.OpenTestDB(t)
	// seed a parent row + a child row that depends on the parent FK.
	owner := testutil.SeedOwner(t, d, "self", "u1")
	mediaID := testutil.SeedMedia(t, d, owner)

	// Snapshot.
	snapPath := filepath.Join(t.TempDir(), "snap.sqlite")
	require.NoError(t, backup.Snapshot(context.Background(), d.WriteDB(), snapPath))

	// Re-open the snapshot via the same code path the restore tool uses.
	conn, err := sql.Open("sqlite3", "file:"+snapPath+"?_busy_timeout=5000&_fk=1&mode=ro")
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	// Confirm the seeded media row arrived intact.
	var got string
	require.NoError(t, conn.QueryRow(
		`SELECT id FROM media WHERE id = ?`, mediaID,
	).Scan(&got))
	require.Equal(t, mediaID, got)

	// Confirm foreign_keys is enforced on the snapshot connection.
	// Attempting to insert an album_media row with a non-existent
	// album_id must error with a constraint failure.
	_, err = conn.Exec(`INSERT INTO album_media (album_id, media_id, added_at)
	                       VALUES (?, ?, datetime('now'))`,
		"00000000-0000-0000-0000-000000000000", mediaID)
	require.Error(t, err, "foreign_keys=1 must be active on the snapshot connection")
}
```

(`testutil.SeedOwner`, `testutil.SeedMedia` may need to be added if not already present — check `internal/testutil/seed.go` first; if absent, inline the inserts directly using `d.WriteDB().Exec(...)` rather than introducing helpers in this plan.)

**Step 2: Run to see baseline state.**

Run: `go test ./internal/backup/ -run "TestSnapshotPragmas_AfterRestore_RoundTrip" -v -count=1`

Expected: PASS if the seeding helpers exist and the DSN swap from Task 4 is correct. If FAIL, the snapshot DSN didn't take the new pragma format — fix `internal/backup/snapshot.go` and re-run.

**Step 3: Run the full backup suite to make sure nothing else regressed.**

Run: `go test ./internal/backup/ -count=1`

Expected: PASS.

**Step 4: Commit.**

```bash
git add internal/backup/snapshot_test.go
git commit -m "test(backup): pragma + restore round-trip under mattn"
```

---

> **Cleanup checkpoint 1.** Run `/roborev-fix` on the current branch. Address any findings before starting Task 6. Commit the fixes as their own commit(s); do not amend.

---

## Section C — Time scanning

### Task 6 — Audit time.Time scan paths and remove parseSQLiteTimeString

**Files:**
- Modify: `internal/share/repo.go`
- Modify: `internal/share/repo_test.go`

**Context.** Modernc loses `TIMESTAMP` affinity through expressions (`COALESCE(a, b)`, `CASE`) and returns Go's default `time.Time.String()` output (`"2006-01-02 15:04:05.999999999 -0700 MST"`) which Scan cannot decode into a `time.Time`. `internal/share/repo.go::parseSQLiteTimeString` exists exactly to translate that string back. Mattn does not have this issue: COALESCE'd TIMESTAMP columns scan into `time.Time` directly. After the driver swap, the helper is dead code, and its caller can scan directly into a `time.Time`.

**Step 1: Find the caller(s).**

Run: `grep -n parseSQLiteTimeString internal/share/repo.go`

Expected: two matches — the function definition (~line 1232) and one or more call sites (e.g. line 871). Capture the call site context:

```go
// existing code at internal/share/repo.go:~870
var displayTime string
if err := row.Scan(..., &displayTime, ...); err != nil { ... }
dt, perr := parseSQLiteTimeString(displayTime)
```

**Step 2: Write a test that demonstrates the new direct-Scan path works.**

```go
// internal/share/repo_test.go (append or augment an existing test in
// the same file). The test name depends on which existing test covers
// the COALESCE'd-time path; if the file has no test naming the path
// directly, add a new one.

// TestRepo_DisplayTimeRoundTrip_DirectTimeScan proves that under the
// mattn driver, a COALESCE'd TIMESTAMP column scans directly into a
// time.Time without the modernc-era string-parse helper.
func TestRepo_DisplayTimeRoundTrip_DirectTimeScan(t *testing.T) {
	d := testutil.OpenTestDB(t)
	// Seed a scope row with broker_granted_at set and broker_revoked_at
	// null; the displayTime SELECT picks the first non-null. Then read
	// it back via the repo method that previously called
	// parseSQLiteTimeString and assert the time round-trips with no
	// loss-of-precision and TZ=UTC.
	// ... (concrete seeding mirrors the existing share repo tests)
}
```

(Lift the seeding patterns from neighbouring tests in `internal/share/repo_test.go`. If the existing test that covers `displayTime` is `TestListByOwner_*`, adapt one of those to assert on the time field rather than adding a brand-new test.)

**Step 3: Run the existing share tests under mattn to see what currently happens.**

Run: `go test ./internal/share/ -count=1`

Expected: PASS or FAIL is informative — if mattn returns a `time.Time` directly and the existing code immediately calls `parseSQLiteTimeString(timeAsString)`, that line stops compiling once you change the variable type. If it currently passes, the helper is harmlessly running; we still want to delete it and switch to a direct Scan.

**Step 4: Replace the call site with a direct `time.Time` Scan.**

```go
// internal/share/repo.go (around the existing call site)
var displayTime time.Time
if err := row.Scan(..., &displayTime, ...); err != nil { ... }
// no more parseSQLiteTimeString call — `displayTime` is already a time.Time
```

**Step 5: Delete `parseSQLiteTimeString` and its dedicated tests.**

Remove the function from `internal/share/repo.go`. If `repo_test.go` has a unit test that exercises `parseSQLiteTimeString` in isolation, delete that test too — coverage now lives in the round-trip test added in Step 2.

**Step 6: Verify.**

Run: `go test ./internal/share/ -count=1`
Run: `go vet ./internal/share/`

Expected: PASS, no `unused` warnings.

**Step 7: Commit.**

```bash
git add internal/share/repo.go internal/share/repo_test.go
git commit -m "refactor(share): drop modernc time-string parser, scan time.Time"
```

---

### Task 7 — Null-time + TZ regression tests in the db package

**Files:**
- Modify: `internal/db/db_test.go` (or create a new file `internal/db/time_test.go` if existing test file is large)

**Step 1: Write the failing tests.**

```go
// TestOpen_RoundTripNullableTime asserts that *time.Time scans round-trip
// nil and non-nil values cleanly under mattn. The test creates a tiny
// scratch table, writes (nullable_ts = NULL) and (nullable_ts = utcNow),
// then reads them back into *time.Time pointers and compares.
func TestOpen_RoundTripNullableTime(t *testing.T) {
	d := testutil.OpenTestDB(t)
	rw := d.WriteDB()

	_, err := rw.Exec(`CREATE TABLE t_time_test (
		id INTEGER PRIMARY KEY,
		ts TIMESTAMP
	)`)
	require.NoError(t, err)

	now := time.Now().UTC().Truncate(time.Microsecond)
	_, err = rw.Exec(`INSERT INTO t_time_test (id, ts) VALUES (1, ?), (2, NULL)`, now)
	require.NoError(t, err)

	var got1 *time.Time
	require.NoError(t, rw.QueryRow(`SELECT ts FROM t_time_test WHERE id=1`).Scan(&got1))
	require.NotNil(t, got1)
	require.True(t, got1.Equal(now), "want %v got %v", now, *got1)

	var got2 *time.Time
	require.NoError(t, rw.QueryRow(`SELECT ts FROM t_time_test WHERE id=2`).Scan(&got2))
	require.Nil(t, got2, "NULL TIMESTAMP must Scan into nil *time.Time")
}

// TestOpen_RoundTripCoalescedTime asserts that a COALESCE'd
// TIMESTAMP scans directly into time.Time — the modernc bug that
// motivated parseSQLiteTimeString is gone under mattn.
func TestOpen_RoundTripCoalescedTime(t *testing.T) {
	d := testutil.OpenTestDB(t)
	rw := d.WriteDB()

	_, err := rw.Exec(`CREATE TABLE t_coalesce (id INTEGER PRIMARY KEY, a TIMESTAMP, b TIMESTAMP)`)
	require.NoError(t, err)

	want := time.Now().UTC().Truncate(time.Microsecond)
	_, err = rw.Exec(`INSERT INTO t_coalesce (id, a, b) VALUES (1, NULL, ?)`, want)
	require.NoError(t, err)

	var got time.Time
	require.NoError(t, rw.QueryRow(
		`SELECT COALESCE(a, b) FROM t_coalesce WHERE id=1`,
	).Scan(&got))
	require.True(t, got.Equal(want))
}

// TestOpen_RoundTripTZ asserts that a TIMESTAMP column written as a
// non-UTC time round-trips with the same Unix instant. (Fotobank
// stores everything as UTC; the test exists to assert the driver
// doesn't drop sub-second precision or offset its understanding of
// the wall-clock value.)
func TestOpen_RoundTripTZ(t *testing.T) {
	d := testutil.OpenTestDB(t)
	rw := d.WriteDB()

	_, err := rw.Exec(`CREATE TABLE t_tz (id INTEGER PRIMARY KEY, ts TIMESTAMP)`)
	require.NoError(t, err)

	loc, err := time.LoadLocation("America/Los_Angeles")
	require.NoError(t, err)
	want := time.Date(2026, 5, 1, 9, 30, 0, 0, loc)
	_, err = rw.Exec(`INSERT INTO t_tz (id, ts) VALUES (1, ?)`, want)
	require.NoError(t, err)

	var got time.Time
	require.NoError(t, rw.QueryRow(`SELECT ts FROM t_tz WHERE id=1`).Scan(&got))
	require.True(t, got.Equal(want), "Unix instant must round-trip")
}
```

**Step 2: Run to verify pass.**

Run: `go test ./internal/db/ -run "TestOpen_RoundTrip" -v -count=1`

Expected: PASS (mattn handles all three patterns natively).

**Step 3: Commit.**

```bash
git add internal/db/db_test.go
git commit -m "test(db): regression coverage for time.Time scan under mattn"
```

---

## Section D — Test infrastructure

### Task 8 — testutil.OpenTestDB registers sqlite-vec

**Files:**
- Modify: `internal/testutil/testdb.go`

**Step 1: Update OpenTestDB.**

```go
// internal/testutil/testdb.go (modified)
package testutil

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wesm/fotobank/internal/db"
)

// OpenTestDB returns an opened DB backed by a fresh file in t.TempDir().
// Automatically closed at test teardown.
//
// Registers the sqlite-vec auto-extension before the first sql.Open
// in the test process. The registration is sync.Once-guarded inside
// internal/db, so calling OpenTestDB N times is safe.
func OpenTestDB(t *testing.T) *db.DB {
	t.Helper()
	db.RegisterSqliteVec()
	d, err := db.Open(filepath.Join(t.TempDir(), "fotobank.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// OpenTestDBAt opens (and migrates) a sqlite DB at path. Same
// registration guarantee as OpenTestDB; callers in CLI tests must
// not register the extension themselves.
func OpenTestDBAt(t *testing.T, path string) *db.DB {
	t.Helper()
	db.RegisterSqliteVec()
	d, err := db.Open(path)
	require.NoError(t, err)
	return d
}
```

(Strictly speaking `db.Open` already calls `RegisterSqliteVec()` internally as of Task 3. Re-calling it here is redundant but cheap — we keep the explicit call so test files that someday open `*sql.DB` directly without going through `db.Open` still get the registration.)

**Step 2: Add a test that proves the registration is in effect inside test runs.**

```go
// internal/testutil/testdb_test.go (new file or append)
package testutil_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wesm/fotobank/internal/testutil"
)

// TestOpenTestDB_VecRegistered proves that vec_version() resolves
// inside any test that has called OpenTestDB. The search-side tests
// added in plan 2 will rely on this — failing it now means the
// search-plan tests would fail later for a confusing reason.
func TestOpenTestDB_VecRegistered(t *testing.T) {
	d := testutil.OpenTestDB(t)
	var v string
	require.NoError(t, d.ReadDB().QueryRow(`SELECT vec_version()`).Scan(&v))
	require.NotEmpty(t, v)
}
```

**Step 3: Run.**

Run: `go test ./internal/testutil/ -count=1 -v`

Expected: PASS.

**Step 4: Commit.**

```bash
git add internal/testutil/testdb.go internal/testutil/testdb_test.go
git commit -m "test(testutil): register sqlite-vec in OpenTestDB"
```

---

### Task 9 — Concurrent-write stress test under mattn

**Files:**
- Modify: `internal/db/db_test.go` (append)

**Context.** The spec calls for "exercise the import + AI worker concurrent-write paths under a stress test before declaring the migration done." The point is to verify that mattn's busy-timeout behavior under the new `_busy_timeout=5000` DSN is functionally equivalent to modernc's prior behavior. Two writers both contending for the RW pool's single connection should both succeed within the timeout; neither should immediately error with `database is locked`.

**Step 1: Write the failing test.**

```go
// TestOpen_ConcurrentWriters spawns N goroutines that each insert
// into a scratch table through the RW pool. They all share the same
// single-connection RW pool and must serialise; the test asserts
// that all N inserts complete within a generous deadline and the
// final row count matches.
func TestOpen_ConcurrentWriters(t *testing.T) {
	if testing.Short() {
		t.Skip("stress test; not under -short")
	}
	d := testutil.OpenTestDB(t)
	rw := d.WriteDB()

	_, err := rw.Exec(`CREATE TABLE stress (id INTEGER PRIMARY KEY, v TEXT)`)
	require.NoError(t, err)

	const writers = 8
	const perWriter = 50
	var wg sync.WaitGroup
	wg.Add(writers)
	errCh := make(chan error, writers*perWriter)

	for w := 0; w < writers; w++ {
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				_, err := rw.Exec(`INSERT INTO stress (v) VALUES (?)`,
					fmt.Sprintf("w%d-%d", w, i))
				if err != nil {
					errCh <- err
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("concurrent insert: %v", err)
	}

	var total int
	require.NoError(t, rw.QueryRow(`SELECT COUNT(*) FROM stress`).Scan(&total))
	require.Equal(t, writers*perWriter, total)
}
```

**Step 2: Run.**

Run: `go test ./internal/db/ -run "TestOpen_ConcurrentWriters" -count=1 -v`

Expected: PASS within ~3s. If FAIL with "database is locked", the busy_timeout DSN didn't take effect — re-check Task 3's DSN edits.

**Step 3: Run the same test with `-race`.**

Run: `go test ./internal/db/ -run "TestOpen_ConcurrentWriters" -count=1 -race`

Expected: PASS with no race-detector reports.

**Step 4: Commit.**

```bash
git add internal/db/db_test.go
git commit -m "test(db): concurrent-writer stress under busy_timeout"
```

---

### Task 10 — Pre-commit hook chain budget verification

**Files:**
- Modify: `prek.toml` only if budget is exceeded; otherwise commit a no-op confirmation note via task notes (this task may end with no file changes).

**Context.** CGO compile is meaningfully slower than pure-Go on a cold cache. The `pre-commit` stage runs `make test-short`. If the post-swap warm-cache budget exceeds ~30s on the developer's machine, raise the timeout in `prek.toml` (or split test-short into a faster subset) — but never skip the hook.

**Step 1: Time the warm-cache budget.**

Run: `make test-short` (twice — first run is the cold cache, second is the realistic warm-cache budget the prek hook will see).

Capture wall-clock time for the second run. Acceptance: under 60s. If the second run exceeds 60s, profile with `go test ./... -short -shuffle=on -v -count=1` and note the slowest packages.

**Step 2: Time the cold cache.**

Run: `go clean -testcache && time make test-short`

This number is informative for understanding worst-case prek hook latency on a CI runner with no cache.

**Step 3: Decide.**

- If both runs are under budget: this task ends with no file changes. Note the timings in the commit message of Task 11 (CLAUDE.md update) for posterity.
- If budget is exceeded: open `prek.toml` and add `timeout` (or its prek equivalent) to the `go-test-short` hook entry. Re-run `make test-short` once via `prek run go-test-short` (or `prek run --all-files go-test-short`) to confirm the hook still passes.

**Step 4: Commit (if `prek.toml` changed).**

```bash
git add prek.toml
git commit -m "chore(prek): bump test-short timeout for CGO warm-cache budget"
```

(Skip the commit if no file changed.)

---

> **Cleanup checkpoint 2.** Run `/roborev-fix` on the current branch. Address any findings before starting Task 11. Commit the fixes as their own commit(s); do not amend.

---

## Section E — Documentation and smoke pass

### Task 11 — Update CLAUDE.md

**Files:**
- Modify: `CLAUDE.md`

**Step 1: Replace the runtime line.**

In the `## Conventions` section, replace:

```
- Runtime: pure Go, no CGO. SQLite via `modernc.org/sqlite`.
```

with:

```
- Runtime: SQLite via `mattn/go-sqlite3` with the `sqlite-vec` auto-extension. CGO is enabled. One driver registration across the app — see `internal/db/sqlitevec.go`.
- Cross-build (e.g. macOS host → Linux deploy): set `CC` to a cross-compiler (`zig cc -target x86_64-linux-musl`, `musl-cross`, or equivalent). Plain `GOOS=linux GOARCH=amd64 go build` without a cross `CC` will fail at link time.
```

**Step 2: Add a one-liner to the `Quick reference` section if it doesn't already cover CGO requirements.**

If a reader running `make build` for the first time would benefit from a one-line "needs a C compiler" hint, add it under `make build`:

```
make build            # debug binary → bin/fotobank (requires a C compiler; CGO)
```

**Step 3: Run a final sanity grep for stale references.**

Run: `grep -rn "modernc\|no CGO" CLAUDE.md`

Expected: zero matches.

**Step 4: Commit.**

```bash
git add CLAUDE.md
git commit -m "docs: switch CLAUDE.md to mattn/CGO posture"
```

---

### Task 12 — Manual smoke pass

**Files:** none modified by default. If the smoke pass surfaces a regression, file it as its own follow-up commit and re-run.

**Step 1: Run the entire test suite once, with shuffle.**

Run: `make test`

Expected: PASS. Note any flakes; flakes that reproduce twice in a row are real regressions and must be fixed before the migration is declared done.

**Step 2: Build a release binary.**

Run: `make build-release`

Expected: build succeeds, `bin/fotobank` is non-empty.

**Step 3: End-to-end smoke pass.**

Pick a small fixture directory with mixed JPEGs and one HEIC if available. With a temporary working directory and a stub-mode config:

```bash
mkdir -p /tmp/fb-smoke && cd /tmp/fb-smoke
cat > config.yaml <<'YAML'
identity:
  mode: stub
  stub:
    hub: self
    user_id: smoke
nas:
  base: ./nas
flash:
  base: ./flash
ai:
  enabled: false
YAML

mkdir -p nas flash

../fotobank-bin/fotobank import --config config.yaml /path/to/fixtures/jpegs
../fotobank-bin/fotobank reconcile --config config.yaml
../fotobank-bin/fotobank thumbs run --config config.yaml --once
../fotobank-bin/fotobank server --config config.yaml &
SERVER=$!
sleep 2
curl -fsS http://127.0.0.1:8080/api/v1/media | head -c 200
kill $SERVER || true

../fotobank-bin/fotobank backup snapshot --config config.yaml --out /tmp/fb-smoke/snap.sqlite
../fotobank-bin/fotobank backup restore --in /tmp/fb-smoke/snap.sqlite --out /tmp/fb-smoke/restored.sqlite
```

Expected:
- import succeeds, prints a non-zero count.
- reconcile reports zero missing.
- thumbs reports a non-zero ready count.
- server start works; `/api/v1/media` returns a JSON list with at least one entry.
- backup snapshot writes a non-empty file.
- backup restore writes a non-empty file and the open succeeds.

(Adapt CLI subcommand spelling to the actual `fotobank ai/import/reconcile/server/backup` surfaces — the smoke pass is a verification gate, not a script to be committed.)

**Step 4: Commit a verification note in the commit message of any docs follow-up, or leave a notes file at `/tmp/fb-smoke-notes.txt` for the next cleanup checkpoint to look at.** No production file change is required if every smoke step passed.

If the smoke pass produced a regression (e.g. a new `database is locked` from a code path the unit tests didn't cover, or a `vec_version() not found` because some startup path opens a `*sql.DB` without going through `db.Open`), fix the underlying cause and add a regression test to `internal/db/`. Commit the fix as its own commit.

---

> **Final cleanup checkpoint.** Run `/roborev-fix` on the current branch. Address any findings before declaring the migration plan done. Commit the fixes as their own commit(s); do not amend.

---

## Verification checklist (must all be true before the search-v1 plan begins)

- [ ] `grep -rn 'modernc.org/sqlite' .` returns zero `.go` matches (a stray reference in a comment is fine; an import is not).
- [ ] `grep -rn '_pragma=' internal/` returns zero matches.
- [ ] `grep -rn 'parseSQLiteTimeString' internal/` returns zero matches.
- [ ] `make test` passes with `-shuffle=on` (at least once cleanly without flakes).
- [ ] `make build-release` produces a non-empty binary.
- [ ] `make lint` passes.
- [ ] `make nilaway` passes (pre-push tier).
- [ ] `internal/db/sqlitevec_test.go` proves `vec_version()` resolves in tests.
- [ ] `internal/db/db_test.go` covers nullable-time, COALESCE'd-time, TZ round-trip, and concurrent writers.
- [ ] `internal/backup/snapshot_test.go` covers the snapshot-side DSN's pragma defaults under mattn.
- [ ] `CLAUDE.md` has been updated; no `modernc` or `no CGO` references remain.
- [ ] The end-to-end smoke pass (Task 12) was completed by a human and any regressions fixed.

When all the above are true, the migration plan is done. The search-v1 plan (`docs/superpowers/plans/2026-05-01-fotobank-search-v1.md`) can begin.
