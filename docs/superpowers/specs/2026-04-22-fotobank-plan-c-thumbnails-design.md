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
   via embedded EXIF previews) into three WebP sizes.
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
    └── processOne: decode → resize 3× → encode WebP → write

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
- `working` → `pending` (via `SweepLeases` when lease expired)
- `ready` → `pending` (via `Enqueue` / regenerate)

Ingest already writes `pending` for new rows. No change there.

---

## 4. New package: `internal/thumbs`

```
internal/thumbs/
├── queue.go       – Queue: ClaimBatch, SweepLeases, Enqueue, MarkReady,
│                    MarkNoPreview, MarkFailed (DB-only)
├── queue_test.go
├── worker.go      – Worker: Run, processOne, drain (wires Queue + storage
│                    + codecs)
├── worker_test.go
├── decode.go      – decodeSource: dispatch by MIME → image.Image
├── decode_test.go
├── encode.go      – encodeWebP, resize
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
service/` is the app/auth layer (see §11). `thumbs.Queue` and
`thumbs.Worker` make the separation unambiguous.

---

## 5. Worker main loop

```go
const sweepInterval = 1 * time.Minute

func (w *Worker) Run(ctx context.Context) error {
    poll  := time.NewTicker(w.cfg.PollInterval)
    sweep := time.NewTicker(sweepInterval)
    defer poll.Stop(); defer sweep.Stop()
    for {
        select {
        case <-ctx.Done():
            return ctx.Err()
        case <-poll.C:
            w.drain(ctx)
        case <-sweep.C:
            if err := w.queue.SweepLeases(ctx, w.cfg.LeaseTimeout); err != nil {
                w.logErr("sweep", err)
            }
        }
    }
}

func (w *Worker) drain(ctx context.Context) {
    rows, err := w.queue.ClaimBatch(ctx, 2*w.cfg.WorkerConcurrency)
    if err != nil { w.logErr("claim", err); return }
    sem := make(chan struct{}, w.cfg.WorkerConcurrency)
    var wg sync.WaitGroup
    for _, r := range rows {
        sem <- struct{}{}
        wg.Add(1)
        go func(row media.Media) {
            defer wg.Done(); defer func() { <-sem }()
            w.processOne(ctx, row)
        }(r)
    }
    wg.Wait()
}
```

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
RETURNING id, owner_hub, owner_user_id, type, mime_type, path,
          thumb_version, checksum;
```

SQLite ≥ 3.35 supports `UPDATE … RETURNING`. `go.mod` pins
`modernc.org/sqlite v1.49.1` which embeds SQLite 3.46+; ok.

The `id ASC` tie-breaker mirrors the Plan B pagination fix — without
it, two rows with identical `imported_at` could produce nondeterministic
claim order, and in theory the same row could be claimed twice across
concurrent workers if the deterministic ordering broke. `UPDATE … IN
(SELECT … LIMIT n)` already serializes the claim inside one SQLite
write, but the tie-breaker is free insurance.

**SweepLeases SQL:**

```sql
UPDATE media
   SET thumb_status     = 'pending',
       thumb_claimed_at = NULL
 WHERE thumb_status = 'working'
   AND thumb_claimed_at < ?
```

The sweep runs independently of poll so a stuck goroutine inside
`drain` cannot block lease reclamation.

---

## 6. `processOne` — per-row pipeline

```go
func (w *Worker) processOne(ctx context.Context, m media.Media) {
    // Hard skip types we don't decode in Plan C.
    if m.Type == media.TypeVideo || isHEIC(m.MimeType) {
        w.queue.MarkNoPreview(ctx, m.ID, m.ThumbVersion)
        return
    }

    src, err := w.fetchSource(ctx, m)   // io.ReadCloser via storage.ReadRange
    if err != nil { w.markFailed(ctx, m, err); return }
    defer src.Close()

    img, err := w.decode(m, src)        // image.Image, EXIF-oriented
    if err != nil {
        if errors.Is(err, errNoEmbeddedPreview) {
            w.queue.MarkNoPreview(ctx, m.ID, m.ThumbVersion); return
        }
        w.markFailed(ctx, m, err); return
    }

    for _, size := range allSizes {     // grid, preview, lightbox
        buf, err := encodeWebP(resize(img, size.maxEdge), webpQuality)
        if err != nil { w.markFailed(ctx, m, err); return }
        key := thumbKey(m.ID, size.name) // .thumbs/{id}/{size}.webp
        if err := w.store.Write(ctx, m.Owner, key, bytes.NewReader(buf)); err != nil {
            w.markFailed(ctx, m, err); return
        }
    }
    w.queue.MarkReady(ctx, m.ID, m.ThumbVersion)
}
```

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
| grid     | 320 px   | 10–40 KB      | Browse grid (many per page)   |
| preview  | 1024 px  | 80–250 KB     | Medium view, pre-lightbox     |
| lightbox | 2048 px  | 300–800 KB    | Full-screen viewer            |

**WebP quality:** `75` (pragmatic default; Google's reference encoder
hits perceptually transparent for most photos in this range).

**Resize algorithm:** Lanczos3. We'll use `golang.org/x/image/draw`
with `draw.CatmullRom` as a first pass (pure Go, reasonable quality)
and revisit if output looks poor.

### 7.1 WebP encoder — PRIMARY UNCERTAINTY

This is the biggest open question in Plan C and deserves its own
investigation before the main implementation begins.

Go's stdlib (`golang.org/x/image/webp`) supports **decoding** WebP but
not encoding. Pure-Go WebP encoding options:

| Library                            | CGO  | Status                        |
|------------------------------------|------|-------------------------------|
| `github.com/chai2010/webp`         | Yes  | Stable, widely used (libwebp) |
| `github.com/HugoSmits86/nativewebp`| No   | Pure Go; newer, less vetted   |
| `github.com/Kagami/go-avif`        | Yes  | AVIF not WebP                 |

**Constraint:** the Go port aims to stay CGO-free (per Plan B, per
master §4). That rules out `chai2010/webp` unless we relax the
constraint.

**Options for Plan C:**

1. **`HugoSmits86/nativewebp`** — pure Go, keeps CGO-free story. Risk:
   maturity, encoder correctness, performance. Must validate with real
   sample photos and a quality check (SSIM vs libwebp reference) before
   committing.
2. **Relax CGO for thumb encoding only** — use `chai2010/webp`. Every
   platform we ship to has libwebp available; the binary gains one
   dynamic dep. Lower engineering risk, but reverses a design principle.
3. **Emit JPEG instead of WebP** — `image/jpeg` in stdlib handles
   encoding at quality 85 with acceptable file sizes (~1.3× WebP).
   Boringly reliable. Client-facing API would need renaming to
   `grid.jpg`, etc.

**Plan:** Task 1 of implementation is "validate `nativewebp` on a
sample set" — decode 20–30 representative photos (JPEG, iPhone portrait
with orientation, overexposed, low-light, high-contrast, RAW-extracted
previews), encode at quality 75, compare visually to libwebp reference,
benchmark encode throughput. If `nativewebp` is acceptable, proceed
with (1). If it fails, escalate: do we accept CGO, or fall back to
JPEG? The spec does NOT pre-commit to an answer; the plan's first task
is the decision gate.

**Whichever encoder ships, the on-disk format name stays WebP** (i.e.,
we don't ship JPEG-named-as-WebP). If we fall back to JPEG, rename keys
and bump the master spec §7.4 accordingly.

---

## 8. Storage layer

`storage.Store` already supports `Read`, `Write`, `Delete`, and the
`owner + key` addressing model. Thumbnail writes reuse this interface
— `.thumbs/{id}/grid.webp` is just another key, owned by the same
principal as the source media.

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

- `size` defaults to `grid` when omitted.
- `v` is optional and informational only — the server always serves
  the current `thumb_version`'s bytes. Clients append `v` to enable
  aggressive caching (see §9.3).
- Unknown `size` values → `400 Bad Request`.

### 9.2 Status handling

| `thumb_status` | Response                                       |
|----------------|------------------------------------------------|
| `ready`        | `200 OK`, bytes from storage                   |
| `pending`, `working` | `404 Not Found`                          |
| `no_preview`   | `404 Not Found`                                |
| `failed`       | `404 Not Found`                                |

404-on-not-ready matches the user's preference from brainstorming.
Simpler for web clients than distinguishing "try again later" from
"never"; the list endpoint already exposes `thumb_status` if the
client wants to skip thumb requests pre-emptively.

**Auth/scope:** same as `/original` in Plan B — caller principal must
own the media row, else 404 (not 403; don't leak existence).

### 9.3 Caching headers (from master §9)

```
ETag:           W/"{media_id}-{size}-v{thumb_version}"
Last-Modified:  {thumb_updated_at as HTTP-date}
Cache-Control:  private, max-age=86400, must-revalidate
```

The weak ETag is correct even if the encoder output shifts slightly
between runs at the same `thumb_version` (e.g., library upgrade) —
content-equivalence is preserved.

`If-None-Match` on the ETag yields cheap 304s. The list endpoint will
start including `thumb_version` in the media DTO (new field) so that
clients can append `?v=N` to image src URLs, which gives them safe
immutable caching under the `must-revalidate` directive.

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

Service layer sits between HTTP handlers and `internal/thumbs`. Wraps
the `thumbs.Queue` with auth scoping (owner principal) and pulls in
the storage layer for the Read path.

```go
type ThumbService struct {
    repo  *media.Repo
    queue *thumbs.Queue
    store storage.Store
}

func (s *ThumbService) Get(
    ctx context.Context,
    id string, size thumbs.Size, principal owners.Principal,
) (io.ReadCloser, media.Media, error)

func (s *ThumbService) Enqueue(
    ctx context.Context,
    principal owners.Principal,
    filter thumbs.EnqueueFilter,  // {All, IDs, Type, Status, Since}
) (rowsEnqueued int, err error)
```

`Enqueue`'s `principal` is used for authorization scoping on the HTTP
path (a user can only regenerate their own media). The CLI bypasses
scoping by supplying the principal from the config's stub owner, same
pattern as `fotobank reconcile`.

---

## 12. Testing strategy

**Unit tests:**
- `ClaimBatch` under concurrent callers (two goroutines calling
  simultaneously must not double-claim a row).
- `SweepLeases` (stale rows reset, fresh rows untouched).
- `Enqueue` (version bumps, status resets, multiple filters
  compose correctly).
- EXIF orientation: decode a test JPEG with orientation=6, verify
  output dimensions swap.
- RAW preview extraction: table-driven test per format
  (ARW/RAF/DNG/CR2) using real sample files under `testdata/raw/`.
  Fail gracefully on RAW without an embedded preview.
- WebP encoder byte-level test: decode encoded output with
  `golang.org/x/image/webp` and assert dimensions.

**Integration tests:**
- Full worker loop: seed `pending` rows, start worker with short
  intervals, assert rows reach `ready` within a deadline, assert
  bytes exist at `.thumbs/{id}/grid.webp`, assert ETag in HTTP
  response matches `thumb_version`.
- Crash-recovery: start worker, mark row `working` with an old
  `thumb_claimed_at`, run sweep, assert row returns to `pending`.
- Regenerate flow: `fotobank thumbs regenerate --id X` bumps version;
  subsequent HTTP `GET /thumb` returns new bytes after worker poll.
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
- `golang.org/x/image/webp` (pure-Go WebP *decoder* — needed only in
  tests to round-trip the encoder's output).

**New, pending WebP encoder decision (§7.1):**
- `github.com/HugoSmits86/nativewebp` — if Task 1 validation
  passes.
- Alternative paths (CGO libwebp or JPEG fallback) documented but
  not pre-committed.

**No new CGO deps** unless the encoder investigation forces it; in
that case the plan stops and escalates to the user before proceeding.

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

1. **WebP encoder choice (§7.1)** — the single biggest risk. Task 1
   of implementation validates `nativewebp` on a sample set. If it
   fails, we pause to decide: accept CGO via `chai2010/webp`, or fall
   back to JPEG encoding. The rest of the plan assumes `nativewebp`
   works.

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

- Video poster frames (future plan; Plan C: `no_preview`).
- HEIC/HEIF decode (future; Plan C: `no_preview`; requires CGO).
- Animated WebP for GIFs (future).
- Per-owner worker quotas (future; current design round-robins by
  `imported_at` globally).
- Admin HTTP endpoints for triggering regenerate (CLI only in Plan C).
- Thumb-cache eviction policy (master §4.4 defers to a future plan).
- Metrics/observability (logs only in Plan C; Prometheus later).
