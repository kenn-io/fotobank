# Fotobank Backup & Disaster Recovery — Design Spec

> Brainstormed 2026-04-25. Implementation plan to follow via
> `superpowers:writing-plans` once this spec is approved.

## 1. Goals

- Tiered snapshot of the SQLite metadata DB to NAS so a flash failure
  loses at most ~15 minutes of writes (vision §11).
- Operator-friendly restore tooling that survives an "I just lost my
  flash drive at 2 a.m." scenario without referring to documentation.
- In-server scheduling so an operator-managed cron is not required.
- A CLI surface that supports both human and `--json` machine workflows
  and slots into the agent-driven CLI conventions called out in
  vision §3.6.

## 2. Non-goals

- **WAL shipping** is deferred (vision open question #13). Tighter RPO
  is a future sub-spec parameter.
- **Byte storage** (originals, thumbnails) is operator-managed via NAS-
  level snapshots or rsync per vision §11. Fotobank does not back up
  bytes.
- **Cross-platform support** beyond Unix. Lock acquisition uses POSIX
  advisory locks via `gofrs/flock`. Vision is Unix-only.
- **Snapshot encryption** is out of scope; if the NAS holds plaintext
  bytes, plaintext metadata snapshots are an acceptable match.
- **Cross-process CLI mutual exclusion** beyond server-vs-server and
  server-vs-restore. Other CLI commands that briefly open the DB
  (`import`, `reconcile`) bypass the lock by design (see §3).

## 3. Architecture

A single bgWG goroutine in `runServer` ticks every 15 minutes; each
tick takes one snapshot via `VACUUM INTO`, then runs a retention sweep.
The same primitives back three CLI subcommands:

```
fotobank backup snapshot [--out <path>] [--config <path>] [--json]
fotobank backup list [--config <path>] [--json]
fotobank backup restore <snapshot-path> [--config <path>] [--yes] [--dry-run] [--json]
```

The worker reuses `backup.Snapshot` and `backup.Sweep`; the CLI reuses
`backup.SnapshotPath` (a path-shaped wrapper that opens read-only),
`backup.List`, and `backup.Restore`.

### 3.1 Package layout

New package `internal/backup/`:

| File | Responsibility |
|------|----------------|
| `snapshot.go` | `Snapshot(ctx, db *sql.DB, dst string) error` and `SnapshotPath(ctx, srcDB, dst string) error`. Runs `VACUUM INTO`, fsyncs file and parent dir, atomic rename via `O_EXCL`. |
| `retention.go` | `Sweep(dir string, policy Policy, now time.Time) (SweepResult, error)`. Bucket-based retention. Deletes stale `*.partial`. |
| `list.go` | `List(dir string) ([]Snapshot, error)`. Newest-first. |
| `restore.go` | `Restore(ctx, snapshotPath, dbPath, lockPath string) error`. No logger argument. |
| `worker.go` | `Worker.Run(ctx) error`. Owns the 15-minute ticker, calls Snapshot then Sweep, tracks `lastSuccessAt`, emits stale warnings. |
| `quote.go` | unexported `sqlQuoteLiteral(s string) string`. SQLite literal escaping. |
| `errors.go` | `ErrServerHoldsLock` sentinel. |

### 3.2 CLI layout

| File | Responsibility |
|------|----------------|
| `internal/cli/backup.go` | `backup` parent + `snapshot`, `list`, `restore` subcommands. Each accepts `--config`. |
| `internal/cli/dbpath.go` | Shared `resolveDBPath(cfg)` and `lockPathFor(dbPath)` helpers used by `runServer` and `backup` commands. |

`runServer` is updated to (a) acquire its lifetime lock via `lockPathFor(dbPath)` before any DB work, and (b) start the backup
worker in `bgWG` when `cfg.Backup.Enabled`.

### 3.3 Lock semantics

A single POSIX advisory lock at `dbPath + ".lock"` (acquired via
`gofrs/flock` to reuse the helper already in `internal/ingest/lock.go`)
governs two scenarios:

1. **Two `runServer` instances on the same DB.** Second server fails
   to acquire the lock at boot and exits with a clear error.
2. **`backup restore` while a server is live.** Restore tries to
   acquire the lock non-blocking; failure returns
   `ErrServerHoldsLock`.

The lock does **not** cover other CLI commands. `import`, `reconcile`,
`backup snapshot`, etc. open the DB without consulting the lock; they
rely on SQLite's own POSIX file locks for transactional safety. Any
operator who runs those concurrent with a `backup restore` accepts
file-rename races by design. Documented as a maintainer-facing
contract in this spec.

`gofrs/flock` releases the lock automatically on process exit, so a
crashed server does not strand the lock.

## 4. Snapshot

### 4.1 Mechanism

`VACUUM INTO 'dst'` acquires an internal SQLite snapshot transaction
on the source and writes a defragmented self-contained copy. Source
DB stays available for readers and writers throughout. For fotobank's
expected metadata size (single-owner, tens of MB to low hundreds), the
operation is sub-second.

`VACUUM INTO` cannot bind path parameters via the SQL driver, so the
implementation uses an unexported `sqlQuoteLiteral` helper to apply
SQLite's literal escape rule (`'` → `''`, wrap in single quotes). The
path is server-generated (timestamp + config), so injection risk is
nil; we still escape correctly because the alternative (raw
interpolation) is a maintenance hazard.

### 4.2 Filename format

`{RFC3339-with-ms}.sqlite`, e.g.
`2026-04-25T14:30:00.000Z.sqlite`. UTC; millisecond precision avoids
collisions between scheduled and on-demand snapshots taken in the
same second. Filenames are lexicographically sortable, so `ls -1
{dir}` returns chronological order; `List` returns newest-first.

### 4.3 Durable-rename sequence

`Snapshot(ctx, db, dst string)`:

1. `mkdir -p` parent dir of `dst` (lazy creation; boot does not depend on NAS being mountable for snapshot dirs).
2. Build `tmp := dst + ".partial"`.
3. Run `VACUUM INTO {sqlQuoteLiteral(tmp)}` against `db`. SQLite refuses to VACUUM INTO a file that already exists, so a stale partial from a prior crash surfaces as a clear error here (and is cleaned up within 24h by the sweep, per §5.2).
4. Open `tmp`, `f.Sync()`, close.
5. `os.Link(tmp, dst)`. Atomic create-if-not-exists on the destination; collision = hard error. Same-filesystem (tmp lives next to dst) so `EXDEV` is not a concern.
6. `os.Remove(tmp)` to drop the link source.
7. Call `cfg.SyncDir(filepath.Dir(dst))` to fsync the parent directory.

`SnapshotPath(ctx, srcDB, dst string)`:

1. `db, err := sql.Open("sqlite", "file:" + srcDB + "?mode=ro&_busy_timeout=5000")`.
2. `defer db.Close()`.
3. `return Snapshot(ctx, db, dst)`.

### 4.4 Configuration

| Field | Purpose |
|-------|---------|
| `DB *sql.DB` | source pool (production: `d.WriteDB()`) |
| `Dir string` | snapshot directory |
| `Interval time.Duration` | tick interval (production: 15min) |
| `Policy Policy` | retention policy |
| `Logger *slog.Logger` | structured log handle |
| `SyncDir func(string) error` | injectable parent-dir fsync seam (default: open + Sync + close) |

## 5. Retention

### 5.1 Policy

```go
type Policy struct {
    Keep15Min  int  // retained within the last 1h, bucketed by 15-min slot
    KeepHourly int  // retained within the last 24h, bucketed by hour slot
    KeepDaily  int  // retained within the last 7 days, bucketed by day slot
}
```

Defaults: `{4, 24, 7}` (vision spec lean-conservative profile). Each
must be ≥ 1; zero would retain nothing of that tier and silently lose
history.

### 5.2 Algorithm

`Sweep(dir string, policy Policy, now time.Time) (SweepResult, error)`:

```go
type SweepResult struct {
    Kept15Min  int
    KeptHourly int
    KeptDaily  int
    Deleted    int  // non-future deletions only; future-snapshot deletions and .partial cleanup are accounted separately in slog
}
```

```
files := List(dir)            // newest-first; skips malformed names; skips *.partial
keep := map[path]bool{}
seen15  := map[truncated_15min]bool{}
seenH   := map[truncated_hour]bool{}
seenD   := map[truncated_day]bool{}

for f in files:
    age := now.Sub(f.ts)
    if age < 0:
        // Future-dated. Delete-with-warn (clock skew).
        os.Remove(f.path); slog.Warn("future snapshot deleted", ...)
        continue
    if age < 1h && len(seen15) < policy.Keep15Min:
        slot := f.ts.Truncate(15 * time.Minute)
        if !seen15[slot]:
            seen15[slot] = true; keep[f.path] = true
    else if age < 24h && len(seenH) < policy.KeepHourly:
        slot := f.ts.Truncate(time.Hour)
        if !seenH[slot]:
            seenH[slot] = true; keep[f.path] = true
    else if age < 7*24h && len(seenD) < policy.KeepDaily:
        slot := f.ts.Truncate(24 * time.Hour)
        if !seenD[slot]:
            seenD[slot] = true; keep[f.path] = true

for f in files where !keep[f.path]: os.Remove(f.path)

// Stale partial cleanup.
for p in partials_in(dir):
    if now.Sub(stat(p).ModTime()) > 24h: os.Remove(p)
```

**Properties:**
- O(snapshots) per sweep.
- A missed tick does not break promotion: the next 15-min snapshot becomes the hour's representative when bucketed.
- Operator-deleted snapshots tolerated (sweep just sees fewer files).
- Buckets are wall-clock relative; no per-tier counter state.
- Future-dated files (clock skew) are deleted, not skipped (Section 2 / Section 5 alignment).
- `.partial` files older than 24h are deleted (self-healing).

### 5.3 Worker loop

```go
func (w *Worker) Run(ctx context.Context) error {
    w.lastSuccessAt = time.Now()        // suppress false-stale on first 48h
    t := time.NewTicker(w.interval)
    defer t.Stop()
    for {
        select {
        case <-ctx.Done():
            return nil
        case <-t.C:
            w.tick(ctx)
        }
    }
}

func (w *Worker) tick(ctx context.Context) {
    dst := filepath.Join(w.dir, time.Now().UTC().Format(stamp) + ".sqlite")
    start := time.Now()
    if err := Snapshot(ctx, w.db, dst); err != nil {
        slog.Error(...)
    } else {
        w.lastSuccessAt = time.Now()
        slog.Info("backup snapshot ok", "path", dst,
                  "size_bytes", size, "duration_ms", elapsed)
    }
    res, err := Sweep(w.dir, w.policy, time.Now())
    if err != nil {
        slog.Warn("backup retention sweep failed", "err", err)
    } else {
        // Fields fold into the snapshot-ok log when present.
        // res.Kept15Min, res.KeptHourly, res.KeptDaily, res.Deleted
    }
    if hours := time.Since(w.lastSuccessAt).Hours(); hours > 48 && w.shouldStaleWarn(now) {
        slog.Warn("backup snapshot stale", "last_success_at", ..., "hours_since", hours)
    }
}
```

Single serial loop; ticker events coalesce naturally if a tick takes
longer than `Interval`. `shouldStaleWarn` rate-limits the stale
warning to once per 48h window.

## 6. Restore

### 6.1 Function signature

```go
func Restore(ctx context.Context, snapshotPath, dbPath, lockPath string) error
```

No logger argument; library is quiet except for returned errors. CLI
wrapper handles human progress output.

### 6.2 Steps

1. **Validate snapshot.** `os.Stat(snapshotPath)` must show a regular
   file. `sql.Open("sqlite", "file:"+snapshotPath+"?mode=ro")`, then
   `PRAGMA integrity_check` must return `ok`. Close.
2. **Acquire lock.** `flock.New(lockPath); l.TryLock()`. On failure,
   return `ErrServerHoldsLock` (wrapped with the underlying error).
   Defer `l.Unlock()`.
3. **Move-aside.** For each of `dbPath`, `dbPath+"-wal"`, `dbPath+"-shm"`:
   - `os.Rename(p, p + ".pre-restore." + ms-timestamp)`.
   - `os.IsNotExist` → skip silently (flash-loss recovery has nothing to move).
   - Any other rename error → return without touching anything else.
4. **Arm rollback.** Capture the moved-aside paths. Defer a function
   that, if the success flag is unset:
   - Closes any DB handle from step 6 if open.
   - Removes `dbPath`, `dbPath+"-wal"`, `dbPath+"-shm"` if present.
   - Renames each `.pre-restore.*` file back to its original name.
   - Joins any rollback errors via `errors.Join` and returns the
     original failure with rollback errors as added context.
5. **Copy snapshot.** Open `snapshotPath` for reading. Create
   `dbPath + ".incoming"` with `O_CREATE|O_EXCL|O_WRONLY`. `io.Copy`,
   `f.Sync`, close. `os.Rename(.incoming, dbPath)`. Call `SyncDir(filepath.Dir(dbPath))`.
   Any error → rollback fires.
6. **Forward-port schema.** `d, err := db.Open(dbPath)`. `db.Open`
   enables WAL and runs embedded migrations idempotently. On error,
   close `d` if non-nil; rollback fires. On success, close `d`
   explicitly so the server can open the DB freshly.
7. **Disarm rollback.** Set the success flag. Return nil.

### 6.3 Pre-restore artefacts

The `.pre-restore.{ts}` files are not auto-deleted. Operator's call.
A common follow-up is: confirm the restore worked end-to-end, then
delete the artefacts manually. Rationale: on a bad restore, those
files are the only copy of the operator's previous state.

### 6.4 Missing DB tolerated

If `dbPath`, `dbPath+"-wal"`, `dbPath+"-shm"` are all missing (flash-
loss recovery), step 3 is a no-op; rollback is a no-op; the snapshot
is copied in cleanly. This is the supported recovery path after a
flash failure.

## 7. Configuration

### 7.1 New `[backup]` schema

```toml
[backup]
enabled = true               # default; respect IsDefined so explicit false honored
dir = ""                     # default: {nas.root}/.fotobank/snapshots/
keep_15min = 4
keep_hourly = 24
keep_daily = 7
```

Go struct (replaces the legacy `Backup` struct in
`internal/config/config.go`):

```go
type Backup struct {
    Enabled    bool   `toml:"enabled"`
    Dir        string `toml:"dir"`
    Keep15Min  int    `toml:"keep_15min"`
    KeepHourly int    `toml:"keep_hourly"`
    KeepDaily  int    `toml:"keep_daily"`
}
```

### 7.2 Legacy schema removed

The legacy `Backup` struct exposed `snapshot_interval`,
`snapshot_retention`, and `wal_shipping`. None are referenced from
live code; this work removes them entirely:

- `snapshot_interval` → gone. Hardcoded 15min in `worker.go`. Tests
  override via `FOTOBANK_TEST_BACKUP_INTERVAL`.
- `snapshot_retention` → replaced by tiered `keep_15min`/
  `keep_hourly`/`keep_daily`.
- `wal_shipping` → gone. Vision open question #13 explicitly defers
  this.

Migration scope: `internal/config/config.go`, `internal/config/config.example.toml`,
`internal/config/config_test.go`, plus the foundation spec/plan docs
(`docs/superpowers/specs/2026-04-22-fotobank-go-core-phase1-design.md`
and `docs/superpowers/plans/2026-04-22-fotobank-go-core-plan-a-foundation.md`,
mirroring the recent `register_scope_args → publish_scope_args` migration).

### 7.3 Validation in `config.Validate`

- `keep_15min`, `keep_hourly`, `keep_daily` each ≥ 1.
- `dir` if non-empty must be absolute.
- `enabled` parsed via `toml.MetaData.IsDefined("backup", "enabled")`.
  Unset → true. Explicitly `false` → false. Mirrors the
  `storage.thumbs_cache_enabled` precedent.

### 7.4 Test override

`FOTOBANK_TEST_BACKUP_INTERVAL` parses as a `time.Duration`. Parse
error → print warning to stderr and fall back to 15min. Mirrors
`FOTOBANK_TEST_SHARE_WORKER_TICK`.

## 8. CLI surface

### 8.1 `fotobank backup snapshot`

```
fotobank backup snapshot [--out <path>] [--config <path>] [--json]
```

Calls `backup.SnapshotPath(ctx, resolveDBPath(cfg), dst)`. Default
`dst` is `{cfg.Backup.Dir or default}/{ms-timestamp}.sqlite`. Does
not acquire the flock; SnapshotPath opens the source read-only so a
live server is unaffected.

`--json` (stdout): `{"path": str, "size_bytes": int, "duration_ms": int, "timestamp": "RFC3339"}`.

Default human (stdout): `snapshot ok: {path} ({size} in {duration})`.

### 8.2 `fotobank backup list`

```
fotobank backup list [--config <path>] [--json]
```

Calls `backup.List(cfg.Backup.Dir or default)`. Output:

- Default human: one line per file, newest-first, `{ISO-timestamp}  {human-size}  {path}`.
- `--json` (stdout): `[{"timestamp": str, "size_bytes": int, "path": str}, ...]` newest-first.

### 8.3 `fotobank backup restore`

```
fotobank backup restore <snapshot-path> [--config <path>] [--yes] [--dry-run] [--json]
```

Calls `backup.Restore(ctx, snapshotPath, resolveDBPath(cfg), lockPathFor(dbPath))`.

- Default human: prompts `Restore from {snapshot} into {dbPath}? (yes/no): ` on stderr; success prints `restored from {snapshot} -> {dbPath}; previous DB moved aside: {paths}` to stdout.
- `--yes` skips the confirmation prompt.
- `--json` writes JSON to stdout. Confirmation prompts (when
  `--yes` is absent) still go to stderr; the eventual JSON only
  appears on success.
- `--dry-run` runs steps 1+2 only (validate snapshot + acquire-and-
  release lock to prove no server is holding it), reports what would
  happen, makes no file changes.

`--json` shape on success:
```json
{
  "restored_from": str,
  "db_path": str,
  "moved_aside": [str, ...]   // omits paths that did not exist pre-restore
}
```

`--json` does **not** imply `--yes`. Operators automating restore in
scripts pass both: `--yes --json`.

## 9. Failure handling and observability

### 9.1 Errors

- Config validation errors wrap `errs.ErrBadConfiguration`.
- `Snapshot`, `Sweep`, `Restore` return errors wrapped with operation
  context (`fmt.Errorf("backup snapshot: %w", err)`).
- Lock acquisition failure on the CLI returns sentinel
  `backup.ErrServerHoldsLock`. Tests assert via `errors.Is`.
- Restore rollback errors are joined with the original via
  `errors.Join`. Original failure leads; rollback errors carry
  "while attempting rollback: …" context.

### 9.2 Slog field table

| Event | Level | Fields |
|-------|-------|--------|
| Snapshot success (worker) | `Info` | `path`, `size_bytes`, `duration_ms`, `kept_15min`, `kept_hourly`, `kept_daily`, `deleted` |
| Snapshot failure | `Error` | `err`, `dst`, `duration_ms` |
| Retention sweep failure | `Warn` | `err`, `dir` |
| Stale-warning fired | `Warn` | `last_success_at`, `hours_since` |
| Future-snapshot deleted | `Warn` | `path`, `ts`, `clock_skew_seconds` |
| `.partial` cleanup | `Info` | `path`, `age_hours` |
| Worker start/stop | `Info` | `interval`, `dir`, `policy` |

Stale-warning suppression: at most one log per 48h window; otherwise
the warning fires every 15 minutes for days, drowning the log.

### 9.3 Library logging boundary

`backup.Restore` returns errors only — no Logger argument. Human
progress messages live in the `internal/cli/backup.go` wrapper. The
worker keeps its slog logger because it has no other surface.

## 10. Testing strategy

### 10.1 Unit tests in `internal/backup/`

- `snapshot_test.go` — real SQLite source DB; assert `dst` exists, `PRAGMA integrity_check` returns `ok`, source unmodified, `.partial` gone. Inject a counting `SyncDir` to assert it was called on the parent dir.
- `retention_test.go` — table-driven; synthesize files via `os.Chtimes` to backdate. Cover: tight 15-min cluster, missing tiers, future-dated files, malformed filenames, `.partial` aging at 24h cutoff (boundary cases at 24h±1s).
- `list_test.go` — sort order, timestamp parsing, malformed-skip behaviour.
- `restore_test.go` — real DB + real snapshot; assert post-state. Force-fail at each step (snapshot integrity, lock, copy, migrations) via injected I/O errors; assert rollback restores the original DB and that the joined error mentions both failures.
- `quote_test.go` — `sqlQuoteLiteral` golden cases including paths with `'`, `\\`, spaces, unicode.

### 10.2 CLI integration tests in `internal/cli/`

- `backup_test.go` — invokes `cli.RunContext` for each subcommand; asserts file-system effects and stdout JSON shape.
- Server-boot integration: extend an existing e2e test with `FOTOBANK_TEST_BACKUP_INTERVAL=50ms`; boot the server in stub mode; watch `cfg.Backup.Dir` for the first snapshot file; assert `PRAGMA integrity_check=ok` on it.
- `TestRestoreRefusesWhileServerRuns` — server up, attempt `backup restore` against a different temp DB path or with `--dry-run`, assert `ErrServerHoldsLock`.
- `TestRestoreSucceedsAfterServerStops` — server up → take snapshot to tempdir → cancel server → run restore against the test's DB path → assert post-state.

### 10.3 Explicitly skipped

- No load test for retention at 10K+ snapshot files. Policy caps the dir at ~35 entries; operator misuse is operator's problem.
- No fault-injection for NAS unmount mid-snapshot. The error path returns `os` errors verbatim.
- No clock-skew test beyond the daily window. The future-snapshot guard logs and deletes; sufficient.

## 11. Edge cases and contracts

- **VACUUM INTO inside a transaction** would fail with `cannot VACUUM from within a transaction`. The implementation never opens one around it; documented as a maintainer-facing contract.
- **Snapshot dir on flash** (operator misconfig): allowed but logged at Warn at boot — `"backup dir is on flash; flash loss will lose backups too"`. Doesn't block boot.
- **Concurrent `backup snapshot` and `restore`**: documented as "don't." Both will run; results are operator's problem (lost snapshot file, partial copy). The lock does not cover this; see §3.3.
- **Stale `.partial` blocking a retry.** A previous Snapshot crashing between steps 3 and 6 of §4.3 leaves `{dst}.partial` on disk. The retry against the same `dst` fails at step 3 with a clear SQLite error. In practice this is extraordinarily rare because each tick uses a fresh ms-precision `dst`, so retries against the same `dst` only happen if the operator manually re-runs `backup snapshot --out` with a colliding path. The sweep clears `.partial` files older than 24h regardless.

## 12. Sub-spec parameters (deferred)

- **WAL shipping** to NAS for sub-15-minute RPO. Vision open Q#13.
- **Cross-process CLI lock coverage.** Today the lock blocks
  server-vs-server and server-vs-restore only. Extending it to cover
  `import`, `reconcile`, `backup snapshot`, etc. is a future change
  if real foot-guns surface.
- **Snapshot encryption** if NAS becomes untrusted.
