# Fotobank Plan C — Thumbnail Pipeline (Design Spec)

> Companion: `2026-04-22-fotobank-go-core-phase1-design.md` (master spec).
> Master §11 and §9 are the load-bearing references. This spec narrows
> those to a concrete implementation plan for the next increment.

**Status:** Draft, awaiting user review.
**Date:** 2026-04-22.
**Predecessor:** Plan B (import + read-only HTTP), merged to master.
**Successor:** Plan D will cover albums and sharing.

---

## 1. Goal

Extend the Go port with a thumbnail pipeline that:

1. Decodes imported photos (JPEG/GIF) and RAW files (ARW/RAF/DNG/CR2
   via embedded EXIF previews) into three JPEG sizes. *(Encoder
   decision: §7.2. Previously targeted WebP; the only viable pure-
   Go WebP encoder proved lossless-only, so we ship JPEG instead.)*
2. Runs as an in-process worker inside `fotobank server`, fed by a
   DB-backed claim/lease queue using the existing `thumb_*` columns.
3. Serves derivatives over `/api/v1/media/{id}/thumb?size=…&v=…`
   with the caching headers specified in master §9.
4. Supports targeted regeneration via `fotobank thumbs regenerate`.

**Non-goals for Plan C:**
- Video poster frames (punted to a later plan; videos terminate at
  `thumb_status = 'no_preview'`).
- HEIC/HEIF decoding (punted; master §11.4 — requires CGO).
- Albums, sharing, per-scope broker, identity-header mode (Plan D+).

---

## 2. Architecture overview

```
fotobank server (single process)
├── http handler (existing)
│   └── GET /api/v1/media/{id}/thumb  ← new route
├── ThumbService (new)
│   ├── ClaimBatch(ctx, n)      – atomic UPDATE…RETURNING
│   ├── SweepLeases(ctx, d)     – reclaim stale 'working' rows
│   ├── Enqueue(ctx, ids, opts) – set rows to 'pending', bump version
│   └── ReadThumb(ctx, id, size) – fetch bytes via storage
└── ThumbWorker (new)
    ├── poll ticker  → ClaimBatch → fanOut
    ├── sweep ticker → SweepLeases
    └── processOne: decode → resize 3× → encode JPEG → write

fotobank thumbs regenerate (CLI, separate process)
└── calls ThumbService.Enqueue via DB only; the running server’s
    worker picks up newly-pending rows on its next poll cycle.
```

The CLI never signals the server directly; they coordinate through
the DB. Regenerate latency is bounded by `PollInterval` (default 5s),
which is fine for an interactive CLI and avoids IPC.

---

## 3. DB schema

**No migration needed.** Plan A's migration already provides:

```sql
thumb_status     TEXT NOT NULL CHECK (
    thumb_status IN ('pending', 'working', 'ready', 'no_preview', 'failed')
)
thumb_claimed_at TIMESTAMP
thumb_version    INTEGER NOT NULL DEFAULT 0
thumb_updated_at TIMESTAMP

CREATE INDEX media_thumb_pending_idx ON media(thumb_status, thumb_claimed_at)
    WHERE thumb_status IN ('pending', 'working');
```

**Status machine:**
- `pending` → `working` (via `ClaimBatch`)
- `working` → `ready` | `no_preview` | `failed` (via `processOne`)
- `working` → `pending` (via `SweepLeases` — also bumps
  `thumb_version`, see §5.1)
- `ready` / `failed` / `no_preview` → `pending` (via `Enqueue` /
  regenerate — also bumps `thumb_version`)

Both transitions into `pending` (sweep and regenerate) bump
`thumb_version`. This keeps on-disk keys and HTTP cache URLs in
sync with state: every time a row goes back into the queue, its
next set of thumbs lands under a fresh `v{N}/` directory.

Ingest already writes `pending` for new rows. No change there.

---

## 4. New package: `internal/thumb`

Name aligns with master spec §3 (`internal/thumb/`). The CLI
subcommand is still `fotobank thumbs regenerate` — that's a
user-facing noun, unrelated to the Go package name.

```
internal/thumb/
├── queue.go       – Queue: ClaimBatch, SweepLeases, Enqueue, MarkReady,
│                    MarkNoPreview, MarkFailed (DB-only)
├── queue_test.go
├── worker.go      – Worker: Run, processOne, drain (wires Queue + storage
│                    + codecs)
├── worker_test.go
├── decode.go      – decodeSource: dispatch by MIME → image.Image
├── decode_test.go
├── encode.go      – EncodeJPEG, Resize
├── encode_test.go
├── raw.go         – extractEmbeddedPreview for RAW formats
├── raw_test.go
├── sizes.go       – Size enum (grid, preview, lightbox), thumbKey
└── sizes_test.go
```

**Why split the queue from the worker:** `Queue` is DB-only and
test-friendly (no filesystem, no codecs — just SQL against a test DB).
`Worker` is the I/O-heavy layer that wires queue + storage + codecs.
Tests of queue atomicity (concurrent `ClaimBatch`, lease sweeps,
enqueue-bumps-version) don't need real encoders; tests of the decode
path don't need the DB. Clean split.

**Naming:** avoiding `Service` in this package because `internal/
service/` is the app/auth layer (see §11). `thumb.Queue` and
`thumb.Worker` make the separation unambiguous.

---

## 5. Worker main loop

```go
const sweepInterval = 1 * time.Minute

// Run starts the poll and sweep loops in independent goroutines and
// blocks until ctx is cancelled. Sweep MUST run on its own goroutine
// so a stuck decode inside drain cannot keep leases from being
// reclaimed.
func (w *Worker) Run(ctx context.Context) error {
    var wg sync.WaitGroup
    wg.Add(2)
    go func() { defer wg.Done(); w.runPoll(ctx) }()
    go func() { defer wg.Done(); w.runSweep(ctx) }()
    wg.Wait()
    return ctx.Err()
}

// runPoll ticks at PollInterval and fires drain at most one at a
// time. A still-running drain absorbs additional ticks via the
// single-flight guard so we never stack overlapping claims.
func (w *Worker) runPoll(ctx context.Context) {
    t := time.NewTicker(w.cfg.PollInterval)
    defer t.Stop()
    for {
        select {
        case <-ctx.Done():
            return
        case <-t.C:
            if !w.drainLock.TryLock() { continue }
            w.drain(ctx)
            w.drainLock.Unlock()
        }
    }
}

// runSweep runs independent of drain so a hung decode cannot block
// lease reclamation.
func (w *Worker) runSweep(ctx context.Context) {
    t := time.NewTicker(sweepInterval)
    defer t.Stop()
    for {
        select {
        case <-ctx.Done():
            return
        case <-t.C:
            if err := w.queue.SweepLeases(ctx, w.cfg.LeaseTimeout); err != nil {
                w.logErr("sweep", err)
            }
        }
    }
}

func (w *Worker) drain(ctx context.Context) {
    claims, err := w.queue.ClaimBatch(ctx, 2*w.cfg.WorkerConcurrency)
    if err != nil { w.logErr("claim", err); return }
    sem := make(chan struct{}, w.cfg.WorkerConcurrency)
    var wg sync.WaitGroup
    for _, c := range claims {
        sem <- struct{}{}
        wg.Add(1)
        go func(claim Claim) {
            defer wg.Done(); defer func() { <-sem }()
            // Per-item ceiling so a single pathological source file
            // cannot starve the worker pool. Claims whose decode
            // exceeds this get marked failed; SweepLeases is the
            // second line of defense if the goroutine itself wedges.
            cctx, cancel := context.WithTimeout(ctx, w.cfg.LeaseTimeout/2)
            defer cancel()
            w.processOne(cctx, claim)
        }(c)
    }
    wg.Wait()
}
```

**Why the dual-goroutine split:** if `Run` were a single select loop
with `drain` called synchronously, a hung decode inside `drain` would
prevent the sweep tick from firing and nothing would ever reclaim
stale `working` rows — they'd stay claimed until the server restarts.
Splitting gives us three independent guarantees:

1. `runSweep` always makes progress (no shared state with decode).
2. `runPoll` can skip a tick if drain is still running
   (single-flight via `drainLock sync.Mutex` field on Worker).
3. `processOne` itself runs under `context.WithTimeout(ctx,
   LeaseTimeout/2)` so one pathological input cannot hang a pool
   slot forever.

**Config defaults (all from `cfg.Thumbs`, present since Plan A):**
- `WorkerConcurrency` = 4
- `PollInterval`     = 5s
- `LeaseTimeout`     = 10m  *(conservative: RAW decoding can be slow)*

**Internal constants, no TOML binding** (per the "no speculative
config knobs" rule — promote to TOML only if a future plan actually
tunes them):
- `batchSize = 2 * WorkerConcurrency` (computed at worker startup)
- `sweepInterval = 1 * time.Minute` (package constant)

**ClaimBatch SQL** (one statement, atomic — no read/update race):

```sql
UPDATE media
   SET thumb_status    = 'working',
       thumb_claimed_at = ?
 WHERE id IN (
     SELECT id FROM media
      WHERE thumb_status = 'pending'
      ORDER BY imported_at ASC, id ASC
      LIMIT ?
 )
RETURNING id, owner_hub, owner_user_id, media_type, mime_type, path,
          thumb_version, checksum, thumb_claimed_at;
```

SQLite ≥ 3.35 supports `UPDATE … RETURNING`. `go.mod` pins
`modernc.org/sqlite v1.49.1` which embeds SQLite 3.46+; ok.

The `id ASC` tie-breaker mirrors the Plan B pagination fix — without
it, two rows with identical `imported_at` could produce nondeterministic
claim order, and in theory the same row could be claimed twice across
concurrent workers if the deterministic ordering broke. `UPDATE … IN
(SELECT … LIMIT n)` already serializes the claim inside one SQLite
write, but the tie-breaker is free insurance.

### 5.1 Claim fencing

Every row in the claim result comes with its `thumb_claimed_at`
timestamp, which acts as a **claim token**. Every terminal update
(`MarkReady`, `MarkNoPreview`, `MarkFailed`) carries this token and
matches on the full `(id, thumb_version, thumb_claimed_at)` triple:

```sql
UPDATE media
   SET thumb_status = 'ready', thumb_updated_at = ?, thumb_claimed_at = NULL
 WHERE id = ? AND thumb_version = ? AND thumb_claimed_at = ?
```

If any field no longer matches, `RowsAffected == 0`, the queue method
returns `ErrClaimLost`, and the worker logs-and-discards — whoever
owns the current claim is responsible for finishing the job.

**SweepLeases SQL — bumps `thumb_version`:**

```sql
UPDATE media
   SET thumb_status     = 'pending',
       thumb_claimed_at = NULL,
       thumb_version    = thumb_version + 1,
       thumb_updated_at = ?
 WHERE thumb_status = 'working'
   AND thumb_claimed_at < ?
```

This is load-bearing. An earlier design had sweep only reset
`thumb_status` and `thumb_claimed_at`. That left a hole:

1. Worker A claims row at version `v=5`, writes `.thumbs/{id}/v5/
   grid.jpg` successfully, then hangs before writing `preview.jpg`.
2. `SweepLeases` times out A's lease, sets `pending` without bumping
   version.
3. Worker B claims the same row at `v=5`, starts writing under the
   *same* `v5/` directory. `Store.Write`'s no-clobber finalize
   (`internal/storage/storage.go:35`) rejects `grid.jpg` with
   `ErrPathOccupied` — B fails the whole row and marks it `failed`.
4. When A eventually wakes up, its `MarkReady(id, v=5, T_A)` now
   returns `ErrClaimLost` (token mismatch from the sweep/reclaim)
   but the damage is done: the row is stuck `failed` until an
   operator runs `regenerate`.

**Bumping version on sweep fixes this cleanly:** the reclaimer sees
`v=6`, writes under a fresh `.thumbs/{id}/v6/` directory, and A's
partial `v5/` directory becomes an orphan (same harmless orphan we
already accept after regenerate). Sweep is effectively a
machine-driven regeneration trigger and should behave like one —
consistent with the master spec §9.6 invariant that "version bumps
happen at the start of the regeneration lifecycle."

**Why the `(id, version, thumb_claimed_at)` triple is sufficient
for fencing, not just version:** the stale worker's WHERE clause
pins version to the *old* `v=5`, so a stale `MarkReady` after a
sweep-plus-reclaim matches zero rows regardless of what
`thumb_claimed_at` is. Version by itself is enough to catch
sweep-then-reclaim races. We still include `thumb_claimed_at` in
the WHERE clause as defense in depth for the narrower case where
two claims happen at the same version (e.g., someone adds a future
feature that replaces a ready row's working claim without bumping
version) — it's a cheap extra column to check and removes a class
of future footguns. The timestamp's nanosecond-uniqueness
*microbehavior* is no longer a correctness pillar; version is.

**Storage note:** SQLite stores `TIMESTAMP` as RFC3339 text at the
driver level (master §4.3), which truncates sub-microsecond
resolution. This was a concern in the previous revision of this
spec when token uniqueness was the correctness story; it is not a
concern now that version bumping carries that weight.

---

## 6. `processOne` — per-row pipeline

Each claim is a `Claim{Media, ClaimedAt time.Time}` where `ClaimedAt`
is the fencing token from §5.1. Terminal calls thread it back in.

```go
func (w *Worker) processOne(ctx context.Context, c Claim) {
    m, token := c.Media, c.ClaimedAt

    // Hard skip types we don't decode in Plan C.
    if m.Type == media.TypeVideo || isHEIC(m.MimeType) {
        w.markNoPreview(ctx, m, token); return
    }

    src, err := w.fetchSource(ctx, m)
    if err != nil { w.markFailed(ctx, m, token, err); return }
    defer src.Close()

    img, err := w.decode(m, src)        // image.Image, EXIF-oriented
    if err != nil {
        if errors.Is(err, errNoEmbeddedPreview) {
            w.markNoPreview(ctx, m, token); return
        }
        w.markFailed(ctx, m, token, err); return
    }

    for _, size := range allSizes {     // grid, preview, lightbox
        buf, err := EncodeJPEG(Resize(img, size.MaxEdge()), jpegQuality)
        if err != nil { w.markFailed(ctx, m, token, err); return }
        // Versioned key — writes never collide with prior versions
        // or sibling retries; see §7 for the scheme.
        key := thumbKey(m.ID, m.ThumbVersion, size.name)
        if err := w.store.Write(ctx, m.Owner, key, bytes.NewReader(buf)); err != nil {
            w.markFailed(ctx, m, token, err); return
        }
    }
    if err := w.queue.MarkReady(ctx, m.ID, m.ThumbVersion, token); err != nil {
        // ErrClaimLost is the only expected error here: sweep or
        // regenerate won the race. Nothing to do; the winning
        // claim owns the finish.
        if !errors.Is(err, thumb.ErrClaimLost) { w.logErr("mark ready", err) }
    }
}
```

`markNoPreview` / `markFailed` are thin helpers that call the
equivalent `Queue` method with `(id, version, token)` and swallow
`ErrClaimLost` the same way.

**Source fetch:** reuses `storage.Store.ReadRange(ctx, owner, path, 0,
-1)` from Plan B. In flash-cache mode this means the original is
pulled through the cache, so subsequent retries (or neighboring thumb
regenerations) hit flash. Stream into memory for decode — no temp
file; decode libraries need a seekable reader.

**Decoder dispatch** in `decode.go`:

| MIME                    | Path                                       |
|-------------------------|--------------------------------------------|
| `image/jpeg`            | `image/jpeg.Decode` + EXIF orientation     |
| `image/gif`             | `image/gif.Decode` (first frame)           |
| `image/x-sony-arw` etc. | `raw.extractEmbeddedPreview` → JPEG decode |
| `image/heic`, `image/heif` | mark `no_preview`, no decode attempt    |

**EXIF orientation** is applied unconditionally for JPEG: an iPhone
portrait photo has pixel data in landscape + orientation tag 6; if we
skip rotation, every portrait thumbnail renders sideways. `dsoprea/
go-exif/v3` reads the tag; rotate with a pure-Go helper on the
decoded `image.Image` before resize.

**Memory budget note:** a 6000×4000 JPEG decoded to RGBA is ~95 MB.
With `WorkerConcurrency=4` and `BatchSize=8`, peak RSS during a large
backfill can approach ~400–500 MB. This is documented but not gated
by any runtime limit; if it becomes a problem on the flash host, add a
semaphore around the decode step separately from `WorkerConcurrency`.

---

## 7. Encoding & sizing

**Three sizes** (longest edge, letterbox-style; aspect ratio
preserved):

| Name     | Max edge | Expected size | Purpose                       |
|----------|----------|---------------|-------------------------------|
| grid     | 256 px   | 8–30 KB       | Browse grid (many per page)   |
| preview  | 1024 px  | 80–250 KB     | Medium view, pre-lightbox     |
| lightbox | 2048 px  | 300–800 KB    | Full-screen viewer            |

These match the vision spec (`docs/superpowers/specs/2026-04-22-
fotobank-vision.md` §12) so one canonical size list stays honest
across docs.

**JPEG quality:** `85` (pragmatic default; stdlib `image/jpeg` is
perceptually transparent at this setting for continuous-tone photos).

**Resize algorithm:** `draw.CatmullRom` from `golang.org/x/image/
draw` — bicubic, pure Go, well-maintained, good quality for
downscaling in this range. If output looks soft at the grid size we
can bump to a custom Lanczos kernel, but CatmullRom is the default
ship-worthy choice for the first revision.

### 7.1 Key scheme — versioned, write-once

Thumbnails live at:

```
.thumbs/{media_id}/v{thumb_version}/{size}.jpg
```

*(2026-04-22 execution note — original Plan C guidance was
`.webp`. Task 1's encoder probe discovered that the only pure-Go
WebP encoder, `HugoSmits86/nativewebp`, is VP8L-only (lossless),
yielding thumbnail sizes 4–5× heavier than the design budget.
Rather than accept CGO (`chai2010/webp`) or ship lossless WebP,
Plan C falls back to stdlib `image/jpeg` at quality 85. File sizes
return to the design budget at ~1.3× a hypothetical lossy-WebP
baseline. Everything else in the pipeline — keys, HTTP, cache
headers, worker — is unchanged except the `.jpg` extension and
`image/jpeg` MIME.)*

Every `(media_id, thumb_version, size)` triple has its own unique
object on disk. This matters for two reasons:

1. **Plan B's `Store.Write` is no-clobber.** `NASOnly` finalizes via
   `os.Link` into the final path and returns `ErrPathOccupied` if
   the path already exists (see `internal/storage/storage.go:35`).
   Stable keys like `.thumbs/{id}/grid.jpg` would fail on every
   regenerate after the first. Versioned keys make each write the
   *only* write to that path — ever.
2. **Retry-after-partial-failure is safe.** If encoding `grid`
   succeeds but `preview` fails at `size=1024`, the worker marks
   `failed` under the current version; the operator runs
   `regenerate --status failed`, which bumps `thumb_version` and
   sets `pending`. The re-run writes under a fresh version
   directory; the half-complete prior-version dir is harmlessly
   orphaned on disk.

**HTTP endpoint (§9)** computes the key from the row's current
`thumb_version`, so clients always see the latest. The immutable
URL model from §9.3 works end-to-end because URL identity (`v=N`)
matches on-disk identity (`v{N}/`).

**Cost on disk:** each regenerate leaves one stale thumb trio
behind (~1 MB total per photo at default sizes). On a 100k-photo
library with 5 regenerations lifetime that's ~500 MB of orphaned
thumbs. Acceptable for Plan C; an eviction pass that prunes
`v{N < current}` directories is deferred to the same future plan
that adds thumb-cache eviction (master §4.4).

**Orphan cleanup can also be added to reconcile** — it already
walks the NAS tree and knows current `thumb_version` per row; a
later plan can teach it to delete `.thumbs/{id}/v{N}/` whenever
`N != current_version`. Not in scope for Plan C.

### 7.2 Encoder decision — JPEG (stdlib)

**Resolution of the encoder question (2026-04-22):** Task 1's
validation probe confirmed that the only pure-Go WebP encoder,
`github.com/HugoSmits86/nativewebp`, supports only VP8L (lossless).
On noise-heavy 4000×3000 input, lossless output ran 4.4–5.5× the
size of stdlib `image/jpeg` at quality 85:

| Size | nativewebp (VP8L) | JPEG q85 | Ratio |
|------|-------------------|----------|-------|
| 256 px grid | 119 KB | 22 KB | 5.5× |
| 1024 px preview | 1.85 MB | 425 KB | 4.4× |
| 2048 px lightbox | 7.4 MB | 1.55 MB | 4.7× |

These numbers break the design size budget (grid 8–30 KB, preview
80–250 KB, lightbox 300–800 KB) hard enough that grid thumbnails
at ~120 KB would defeat infinite-scroll UX.

**Decision:** emit **JPEG at quality 85** via stdlib `image/jpeg`.
Trade-offs considered and rejected:

1. `HugoSmits86/nativewebp` — rejected. VP8L-only; produces files
   4–5× the design budget; would need downstream UX compromises.
2. `github.com/chai2010/webp` (CGO wrapping libwebp) — rejected.
   Reverses Plan B's pure-Go design principle and adds a runtime
   libwebp dependency on the deployment host. Held in reserve if a
   later plan justifies CGO.
3. **JPEG via stdlib** — chosen. Stdlib-only, zero supply-chain
   risk, ~1.3× the size of hypothetical lossy-WebP, every client
   handles JPEG natively. Only impact on the rest of the plan:
   the on-disk file extension is `.jpg`, MIME is `image/jpeg`,
   and the resize→encode path calls `jpeg.Encode` instead of
   `nativewebp.Encode`.

The `nativewebp` dependency added by Task 1 is removed in a
follow-up commit; `golang.org/x/image` stays because
`draw.CatmullRom` is still the resize kernel.

If a future plan wants lossy WebP (or AVIF), swap the encoder in
`thumb.Encode` and bump the format extension in `ThumbKey` —
everything else in the pipeline is format-agnostic.

---

## 8. Storage layer

`storage.Store` already supports `ReadRange`, `Write`, `Delete`, and
the `owner + key` addressing model. Thumbnail writes reuse this
interface — `.thumbs/{id}/v{N}/grid.jpg` (per §7.1) is just another
key, owned by the same principal as the source media.

**ThumbsCache wiring.** `cfg.Storage.ThumbsCacheEnabled` exists since
Plan A but is currently unwired. In Plan C, when both
`Mode=flash_cache` and `ThumbsCacheEnabled=true`, the storage layer
gains a second flash-backed layer at `{flash.root}/thumbs/{id}/…`.

**Implementation approach** — add a `thumbsFlashCache` to the
existing `FlashCache` struct (or a sibling type if it gets too
tangled), parametrized by subdir `"thumbs"` vs the originals'
`"originals"`. Read path: check thumbs cache → fall back to NAS,
populate on miss. Write path: tee to both. No eviction in v1 (per
master §4.4 note).

**Janitor:** the existing flash janitor (`storage.FlashCache.Evict`)
walks `{flash.root}/originals/` only. For Plan C it must *also* not
touch `{flash.root}/thumbs/`. The explicit subdir layout from Plan B's
bug fix already guarantees this (both subdirs are siblings under
`{flash.root}`, and the janitor only walks its configured root).
Verify with a regression test.

**Uncertainty:** the cleanest refactor may be to generalize
`FlashCache` to accept a caller-supplied subdir and instantiate it
twice. The plan should land the refactor in an early task so both
call sites land on the same primitive.

---

## 9. HTTP endpoint

### 9.1 Route

`GET /api/v1/media/{id}/thumb?size=grid|preview|lightbox&v={N}`

- `size` defaults to `grid` when omitted. Unknown `size` → `400 Bad
  Request`.
- `v` is **validated against the row's current `thumb_version`**. It
  is NOT informational: it makes the URL the identity of a specific
  version's bytes, which is what enables aggressive, immutable
  caching (§9.3). Omitted / mismatched / unparseable → `404 Not
  Found`. Clients obtain `v` from the list/detail endpoint (which
  now includes `thumb_version`) and include it in `<img src>`.

### 9.2 Status handling

| `thumb_status` | `v` present? | Response                        |
|----------------|--------------|---------------------------------|
| `ready`        | matches      | `200 OK`, bytes from storage    |
| `ready`        | missing/mismatch | `404 Not Found`             |
| `pending`, `working` | any    | `404 Not Found`                 |
| `no_preview`   | any          | `404 Not Found`                 |
| `failed`       | any          | `404 Not Found`                 |

404-on-not-ready matches the user's preference from brainstorming.
Simpler for web clients than distinguishing "try again later" from
"never"; the list endpoint already exposes `thumb_status` if the
client wants to skip thumb requests pre-emptively. 404 on version
mismatch is also cheap — the client will already have re-fetched the
media detail to pick up the new version and retries with the new URL.

**Auth/scope:** same as `/original` in Plan B — caller principal must
own the media row, else 404 (not 403; don't leak existence).

**Key lookup:** the handler maps `(id, size, v)` to the on-disk key
`.thumbs/{id}/v{v}/{size}.jpg` (per §7.1) only after the DB
confirms `thumb_version == v AND thumb_status == 'ready'`. Reading
the versioned key directly is safe because versioned paths are
write-once.

### 9.3 Caching headers

Because `v` is validated, the URL is *immutable* for its version — a
response for `/thumb?size=grid&v=5` will never change content for the
same row. We therefore get to serve aggressive cache headers:

```
ETag:           "{media_id}-{size}-v{thumb_version}"
Last-Modified:  {thumb_updated_at as HTTP-date}
Cache-Control:  private, max-age=31536000, immutable
```

This is a deliberate *tightening* of the master spec's §9 guidance
(which was `must-revalidate, max-age=86400` with a weak ETag), made
possible by the URL-identity choice above. The strong ETag is now
correct because version validation guarantees byte-identity for a
given version — if we change encoders, that's a new `thumb_version`
(bumped via `Enqueue`) and a new URL. The list endpoint includes the
current `thumb_version` so clients always get the latest URL.

**Why not rely on ETag revalidation alone** (i.e., drop `v`, use
only `must-revalidate`): every thumbnail request becomes a
conditional GET, and at hundreds of grid thumbs per page that's a
measurable per-load latency tax over NFS-backed metadata reads.
URL-versioning lets us be `immutable` and skip revalidation entirely.

---

## 10. CLI: `fotobank thumbs regenerate`

```
Usage:
  fotobank thumbs regenerate [flags]

Flags:
      --all                   regenerate all media rows
      --id stringArray        regenerate specific media IDs (repeatable)
      --type string           filter: 'photo' or 'video'
      --status string         filter: 'ready', 'failed', 'no_preview', ...
      --since string          filter: imported_at >= RFC3339 date
      --dry-run               report row count, do not enqueue
```

**Semantics:**
- `--all` overrides `--id`/filters.
- Without `--all`, at least one of `--id` / `--type` / `--status` /
  `--since` is required; empty invocation errors out (don't silently
  match 0 or all rows).
- Enqueue behavior: within one transaction, for each matched row,
  set `thumb_status='pending'`, `thumb_version = thumb_version + 1`,
  `thumb_updated_at = now`. This is `ThumbService.Enqueue`, called
  identically by the HTTP regenerate endpoint (future) and the CLI.
- Output: `{n} rows enqueued for regeneration.`

The CLI reuses the existing config+db-open plumbing from `fotobank
reconcile`. The server, if running, picks up pending rows on its next
poll; master §9's "version bump happens at the start of the lifecycle"
means clients see the new `thumb_version` in listings before the bytes
land, and their ETag requests trigger regeneration awareness
automatically.

**Why no `--wait` flag:** scripting regenerate-and-wait would require
re-polling the CLI against the DB, which amounts to rebuilding the
worker loop in the CLI. If a caller really needs to wait, they can
poll the media detail endpoint themselves. YAGNI.

---

## 11. Service API sketch (`internal/service/thumb_service.go`)

Service layer sits between HTTP handlers and `internal/thumb`. Wraps
the `thumb.Queue` with auth scoping (owner principal) and pulls in
the storage layer for the Read path.

```go
type ThumbService struct {
    repo  *media.Repo
    queue *thumb.Queue
    store storage.Store
}

// Get reads the (id, size, version) thumb for principal. The handler
// must pass version from the validated ?v= param; Get returns ErrNotFound
// if version disagrees with the DB (the client will have raced a
// regenerate and needs to refetch the list to pick up the new v).
func (s *ThumbService) Get(
    ctx context.Context,
    id string, size thumb.Size, version int, principal owners.Principal,
) (io.ReadCloser, media.Media, error)

func (s *ThumbService) Enqueue(
    ctx context.Context,
    principal owners.Principal,
    filter thumb.EnqueueFilter,  // {All, IDs, Type, Status, Since}
) (rowsEnqueued int, err error)
```

**`thumb.Queue` surface** (for reference — details in §5.1):

```go
type Claim struct { Media media.Media; ClaimedAt time.Time }

func (q *Queue) ClaimBatch(ctx context.Context, n int) ([]Claim, error)
func (q *Queue) SweepLeases(ctx context.Context, after time.Duration) (int, error)
func (q *Queue) MarkReady(ctx context.Context, id string, version int, token time.Time) error
func (q *Queue) MarkNoPreview(ctx context.Context, id string, version int, token time.Time) error
func (q *Queue) MarkFailed(ctx context.Context, id string, version int, token time.Time, cause error) error
func (q *Queue) Enqueue(ctx context.Context, filter EnqueueFilter) (int, error)

var ErrClaimLost = errors.New("thumb: claim lost (sweep or regenerate won)")
```

All terminal Mark* methods return `ErrClaimLost` if the WHERE clause
on `(id, thumb_version, thumb_claimed_at)` matches zero rows.

`Enqueue`'s `principal` is used for authorization scoping on the HTTP
path (a user can only regenerate their own media). The CLI bypasses
scoping by supplying the principal from the config's stub owner, same
pattern as `fotobank reconcile`.

---

## 12. Testing strategy

**Unit tests:**
- `ClaimBatch` under concurrent callers (two goroutines calling
  simultaneously must not double-claim a row).
- `ClaimBatch` returns a distinct `ClaimedAt` token for each batch.
- Terminal calls (`MarkReady`, `MarkNoPreview`, `MarkFailed`) are
  fenced: marking with a stale token returns `ErrClaimLost` and
  does not modify the row.
- `SweepLeases` (stale rows reset, fresh rows untouched); after
  sweep, the prior claim's token is invalid and `MarkReady` with
  that token returns `ErrClaimLost`.
- `SweepLeases` bumps `thumb_version` and `thumb_updated_at`: pre-
  sweep version `v=N`, post-sweep version `v=N+1`. The next claim
  returns `v=N+1`.
- `Enqueue` (version bumps, status resets, multiple filters
  compose correctly).
- Regenerate-while-working race: start processing, call
  `Enqueue` to bump version mid-flight, verify `MarkReady` with
  the original version returns `ErrClaimLost` and the new version
  is picked up on the next poll.
- EXIF orientation: decode a test JPEG with orientation=6, verify
  output dimensions swap.
- RAW preview extraction: table-driven test per format
  (ARW/RAF/DNG/CR2) using real sample files under `testdata/raw/`.
  Fail gracefully on RAW without an embedded preview.
- JPEG encoder byte-level test: decode encoded output with
  `image/jpeg` and assert dimensions.

**Integration tests:**
- Full worker loop: seed `pending` rows, start worker with short
  intervals, assert rows reach `ready` within a deadline, assert
  bytes exist at `.thumbs/{id}/v{N}/grid.jpg`, assert ETag in HTTP
  response matches `thumb_version`.
- Crash-recovery: start worker, mark row `working` with an old
  `thumb_claimed_at`, run sweep, assert row returns to `pending`
  with `thumb_version` bumped. Verify the orphaned worker
  goroutine's eventual `MarkReady` gets `ErrClaimLost` and does
  not corrupt the re-claimed row.
- Same-version lease-retry never hits `ErrPathOccupied`: seed
  `.thumbs/{id}/v5/grid.jpg` on disk to simulate a partial write
  from a swept claim, then run another claim→finish cycle; assert
  the new writes land under `v6/` (not `v5/`) and succeed
  end-to-end.
- Regenerate overwrites safely: start with a ready thumb, run
  `fotobank thumbs regenerate --id X`, wait for worker; assert the
  old `v{N-1}/` directory still exists on disk (orphaned, harmless)
  and the new `v{N}/` directory holds the new bytes.
- Regenerate flow: `fotobank thumbs regenerate --id X` bumps version;
  subsequent HTTP `GET /thumb?v={new}` returns new bytes; `GET
  /thumb?v={old}` returns 404 (the write-once prior version is on
  disk but the handler only serves current).
- Flash-cache wiring: with `ThumbsCacheEnabled=true`, first GET
  populates flash, second GET served without NAS hit (assert via a
  NAS stat probe or an injected Store spy).
- Flash janitor: sentinel file under `{flash.root}/thumbs/` survives
  eviction runs (mirrors Plan B's sibling-state regression test).

**Test data:** we need real RAW files. Plan B didn't ship any; Plan C
adds `testdata/raw/*.{arw,raf,dng,cr2}` (small samples from each
manufacturer — enough for the embedded-preview extractor to exercise
its tag walk). If real samples can't be licensed, fall back to
synthetic: hand-craft a TIFF-based file with a known JPEG preview tag.

**E2E:** extend Plan B's `TestE2EMediaPipeline` — after import, wait
for worker to drain, hit `/thumb`, compare ETag before/after calling
regenerate.

---

## 13. Configuration changes

Minimal. All needed keys already exist from Plan A:

- `[thumbs] worker_concurrency`  (default 4)
- `[thumbs] poll_interval`       (default 5s)
- `[thumbs] lease_timeout`       (default 10m)
- `[storage] thumbs_cache_enabled` (default true)

No new TOML keys in Plan C. `BatchSize` and `SweepInterval` stay
internal constants/computed values; promote to TOML only if a
subsequent plan needs it.

---

## 14. Dependencies

**Existing, reused:**
- `github.com/dsoprea/go-exif/v3` (for EXIF tag walking — RAW preview
  extraction + orientation).

**New (uncontroversial):**
- `golang.org/x/image/draw` (pure-Go resize, well-maintained by the Go
  team).
- `image/jpeg` from the stdlib — encoder + decoder.

**No new third-party encoder deps.** Task 1's probe added
`github.com/HugoSmits86/nativewebp` and `golang.org/x/image`; the
follow-up commit that resolved §7.2 removes `nativewebp` (unused
after the JPEG decision) and keeps `golang.org/x/image` for
`draw.CatmullRom`.

**No CGO deps.** The encoder decision explicitly rejected the CGO
path (`chai2010/webp`).

---

## 15. Migration, rollout, compatibility

- No DB migration.
- No API break: new route is additive. The list endpoint gains a
  `thumb_version` field in its response DTO; clients that ignore
  unknown fields are unaffected.
- Existing media rows all have `thumb_status='pending'` from Plan A
  ingest; when Plan C ships, the worker will drain them in `imported_
  at ASC` order. On a large existing library this is a one-time
  backfill; document expected throughput (e.g., ~1–3 photos/sec per
  worker thread with NVMe flash) in the PR.

---

## 16. Critical uncertainties — flagged for review

1. ~~**WebP encoder choice**~~ — **RESOLVED 2026-04-22.** Task 1's
   probe found `nativewebp` to be VP8L-only, producing files 4-5×
   the design budget. We now ship JPEG via stdlib `image/jpeg`; see
   §7.2. No longer an open question.

2. **RAW preview extraction robustness** — `dsoprea/go-exif/v3`
   exposes IFD walking but not a prebuilt "give me the preview bytes"
   API. We hand-write the tag walker. Coverage depends on having
   real ARW/RAF/DNG/CR2 samples in `testdata/raw/`. Without real
   samples, the extractor will ship under-tested; the plan should
   explicitly acquire test fixtures before the RAW task.

3. **Memory budget on backfills** (§6) — 4 concurrent decodes of
   large RAW-derived JPEGs can spike RSS past 500 MB. Not a
   correctness bug, but operationally relevant. If an initial deploy
   OOMs on the flash host, lower `WorkerConcurrency` to 2 via TOML
   override. Document this in the deployment notes, don't add code
   guards yet.

4. **FlashCache generalization** — the current `FlashCache` was
   written for originals only with a hardcoded `originals` subdir.
   Plan C needs a sibling thumbs layer. Refactor options: a second
   parallel `FlashCache` instance with a different subdir constant
   (simplest); a generalized `FlashCache` with an injectable subdir
   (cleaner but a bigger diff). Pick in the plan's first storage
   task; both should be straightforward.

5. **HTTP 404-vs-409 for in-progress thumbs** — we chose 404 for
   simplicity. If front-end UX evidence later says "we want to show
   a spinner when thumb is working, not a broken-image icon," revisit
   by adding a `thumb_status` field to the list-media response
   (cheap, clients drive the UX). Don't second-guess during Plan C.

6. **GIF first-frame thumbnails** — animated GIFs become single-frame
   stills in all three sizes. Acceptable for Plan C; a future plan
   can emit animated WebP if demand appears.

7. **Orientation tag in encoded WebP** — we bake orientation into
   the pixel data during decode, then write a thumb with no EXIF.
   This is the right call (thumbnails are derivative display bytes,
   not master images) but should be explicit in tests to prevent
   future regressions.

---

## 17. Out of scope for Plan C (deferred explicitly)

- **Video poster frames** — deferred to **Plan E** (after Plan D
  ships albums + sharing). Plan C: `no_preview`. This is a
  narrowing of master spec §2's Phase 1 scope, which originally
  bundled video posters with thumbnails; the master spec's §1
  "Planning note" has been amended to reflect the Plan B/C/D/E
  split. The master §11.2 design for poster extraction still
  stands and will drive Plan E.
- HEIC/HEIF decode (future; Plan C: `no_preview`; requires CGO).
- Animated WebP for GIFs (future).
- Per-owner worker quotas (future; current design round-robins by
  `imported_at` globally).
- Admin HTTP endpoints for triggering regenerate (CLI only in Plan C).
- Thumb-cache eviction policy (master §4.4 defers to a future plan).
- Metrics/observability (logs only in Plan C; Prometheus later).
