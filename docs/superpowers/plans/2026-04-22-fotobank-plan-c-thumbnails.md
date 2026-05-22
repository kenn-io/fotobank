# Fotobank Plan C — Thumbnail Pipeline Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a DB-backed thumbnail pipeline (worker + service + HTTP + CLI) that renders three WebP sizes per photo, surfaces them at `/api/v1/media/{id}/thumb`, and supports targeted regeneration via `fotobank thumbs regenerate`.

**Architecture:** New `internal/thumb` package (Queue, Worker, decode, encode, raw, sizes) fed by Plan A's `thumb_*` columns. Worker claims rows, decodes (JPEG/GIF/RAW preview), resizes, encodes WebP, writes to versioned keys `.thumbs/{id}/v{N}/{size}.webp`. `SweepLeases` bumps `thumb_version` so lease retries never hit the no-clobber `Store.Write`. Videos and HEIC short-circuit to `no_preview` in this plan.

**Tech Stack:** Go 1.26, `golang.org/x/image/draw` (resize), `github.com/HugoSmits86/nativewebp` (encoder, subject to Task 1 validation), `github.com/dsoprea/go-exif/v3` (RAW preview IFD walk, EXIF orientation), existing `modernc.org/sqlite`, `huma/v2`, cobra.

**Spec:** `docs/superpowers/specs/2026-04-22-fotobank-plan-c-thumbnails-design.md`

---

## File plan

**New files (all under `internal/thumb/`):**

| File | Responsibility |
|---|---|
| `sizes.go` | `Size` enum (grid/preview/lightbox), pixel targets, `ThumbKey(id, version, size) string` |
| `sizes_test.go` | Size enum, ThumbKey table tests |
| `encode.go` | `Resize(img image.Image, maxEdge int) image.Image`, `EncodeWebP(w io.Writer, img image.Image, quality int) error` |
| `encode_test.go` | Round-trip WebP decode test, resize dims test |
| `decode.go` | `Decode(mime string, r io.Reader) (image.Image, error)` dispatcher + EXIF orientation correction |
| `decode_test.go` | JPEG+orientation tests, GIF first-frame test, HEIC rejection test |
| `raw.go` | `ExtractPreview(r io.ReadSeeker) (image.Image, error)` — dsoprea IFD walk |
| `raw_test.go` | Synthetic TIFF-with-preview test (real samples deferred) |
| `queue.go` | `Queue` struct: `ClaimBatch`, `SweepLeases`, `MarkReady`, `MarkNoPreview`, `MarkFailed`, `Enqueue`; `Claim` struct; `EnqueueFilter` struct; `ErrClaimLost` sentinel |
| `queue_test.go` | Atomicity, fencing, version-bump-on-sweep, enqueue filter composition |
| `worker.go` | `Worker` struct: `Run`, `runPoll`, `runSweep`, `drain`, `processOne`; `Config` struct |
| `worker_test.go` | Full-loop integration with fake Store |

**New files (other packages):**

| File | Responsibility |
|---|---|
| `internal/service/thumb_service.go` | `ThumbService` wrapping `thumb.Queue` with owner-scoped auth |
| `internal/service/thumb_service_test.go` | Auth scoping, stream-through-storage tests |
| `internal/httpapi/media_thumb.go` | `GET /api/v1/media/{id}/thumb` with `?size=` and `?v=` validation |
| `internal/httpapi/media_thumb_test.go` | Route behavior, cache headers, version mismatch → 404 |
| `internal/cli/thumbs.go` | `fotobank thumbs regenerate [--all|--id|--type|--status|--since]` |
| `internal/cli/thumbs_test.go` | Flag validation, row-count output, principal scoping |
| `testdata/raw/synthetic-preview.tiff` | Minimal TIFF with embedded JPEG preview tag for RAW tests |

**Modified files:**

| File | Change |
|---|---|
| `internal/storage/flash_cache.go` | Route `.thumbs/*` keys to a separate `thumbsRoot`; gate on `ThumbsCacheEnabled` |
| `internal/storage/flash_cache_test.go` | Sibling-cache tests, janitor-leaves-thumbs-alone test |
| `internal/cli/server.go` | Construct `thumb.Queue` + `thumb.Worker`, start alongside flash janitor |
| `internal/cli/root.go` | Register `newThumbsCmd()` on the root command |
| `internal/httpapi/api.go` | Wire `ThumbService` into `Deps`, call `registerMediaThumb` |
| `internal/httpapi/media.go` | Add `thumb_version` field to `mediaDTO` |
| `internal/service/media_service.go` *(only if needed for Read plumbing — keep minimal)* | (Usually untouched; document here if a helper is added) |
| `go.mod` | `+ github.com/HugoSmits86/nativewebp`, `+ golang.org/x/image` |

**No DB migration.** Plan A's `000001_initial_schema.up.sql` already carries every column Plan C needs.

---

## Task 1: Validate the WebP encoder (DECISION GATE)

This task is a blocker: the rest of the plan assumes `HugoSmits86/nativewebp` produces usable output at reasonable throughput. If the probe fails, pause and escalate before continuing.

**Files:**
- Create: `internal/thumb/encoder_probe_test.go` (delete after gate passes)
- Modify: `go.mod`

- [ ] **Step 1: Add the dependency and `golang.org/x/image`**

```bash
go get github.com/HugoSmits86/nativewebp@latest
go get golang.org/x/image@latest
```

Expected: both resolve without errors.

- [ ] **Step 2: Write the probe test**

Create `internal/thumb/encoder_probe_test.go`:

```go
package thumb_test

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/HugoSmits86/nativewebp"
	"github.com/stretchr/testify/require"
	xwebp "golang.org/x/image/webp"
)

// TestEncoderProbe is a DECISION-GATE probe for nativewebp. It is not
// shipped — delete after Plan C lands. Runs on:
//   - a synthetic 4000x3000 gradient (stress),
//   - the existing photo-with-timestamp.jpg fixture (real-world).
// Verifies the output round-trips through the standard decoder at the
// expected dimensions, file sizes are sane, and a single encode takes
// under 2s. Failing any assertion means we need to escalate before
// continuing the plan (consider CGO libwebp or fall back to JPEG).
func TestEncoderProbe(t *testing.T) {
	r := require.New(t)

	t.Run("synthetic-4kx3k", func(t *testing.T) {
		img := image.NewRGBA(image.Rect(0, 0, 4000, 3000))
		for y := range 3000 {
			for x := range 4000 {
				img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 128, A: 255})
			}
		}
		var buf bytes.Buffer
		start := time.Now()
		r.NoError(nativewebp.Encode(&buf, img, &nativewebp.Options{Quality: 75}))
		elapsed := time.Since(start)
		r.Less(elapsed, 2*time.Second, "encode took %v (budget 2s)", elapsed)
		r.Greater(buf.Len(), 1000, "output suspiciously small")
		r.Less(buf.Len(), 5_000_000, "output larger than input — encoder broken?")

		decoded, err := xwebp.Decode(&buf)
		r.NoError(err)
		r.Equal(4000, decoded.Bounds().Dx())
		r.Equal(3000, decoded.Bounds().Dy())
	})

	t.Run("real-jpeg", func(t *testing.T) {
		f, err := os.Open(filepath.Join("..", "..", "testdata", "exif", "photo-with-timestamp.jpg"))
		r.NoError(err)
		defer func() { _ = f.Close() }()
		img, err := jpeg.Decode(f)
		r.NoError(err)

		var buf bytes.Buffer
		r.NoError(nativewebp.Encode(&buf, img, &nativewebp.Options{Quality: 75}))
		decoded, err := xwebp.Decode(&buf)
		r.NoError(err)
		r.Equal(img.Bounds().Dx(), decoded.Bounds().Dx())
		r.Equal(img.Bounds().Dy(), decoded.Bounds().Dy())
	})
}
```

- [ ] **Step 3: Run the probe**

Run: `go test ./internal/thumb/ -run TestEncoderProbe -v`

Expected: PASS on both subtests. If either fails, STOP and escalate — the plan assumes `nativewebp` works. The user preselected Option 1 (pure-Go nativewebp) with Options 2 (CGO libwebp) and 3 (JPEG fallback) held in reserve; a probe failure requires re-brainstorming the encoder choice before writing more code.

- [ ] **Step 4: Delete the probe**

Once the probe passes, remove it — we only needed the decision gate. The real encode tests in Task 3 will exercise the library going forward.

```bash
rm internal/thumb/encoder_probe_test.go
```

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum
git commit -m "Add nativewebp + golang.org/x/image for Plan C thumbnails"
```

---

## Task 2: `internal/thumb/sizes.go` — Size enum and key scheme

**Files:**
- Create: `internal/thumb/sizes.go`
- Create: `internal/thumb/sizes_test.go`

- [ ] **Step 1: Write the test**

Create `internal/thumb/sizes_test.go`:

```go
package thumb_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/thumb"
)

func TestSizeMaxEdge(t *testing.T) {
	r := require.New(t)
	r.Equal(256, thumb.SizeGrid.MaxEdge())
	r.Equal(1024, thumb.SizePreview.MaxEdge())
	r.Equal(2048, thumb.SizeLightbox.MaxEdge())
}

func TestParseSizeValid(t *testing.T) {
	cases := []struct {
		in   string
		want thumb.Size
	}{
		{"grid", thumb.SizeGrid},
		{"preview", thumb.SizePreview},
		{"lightbox", thumb.SizeLightbox},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, err := thumb.ParseSize(c.in)
			require.NoError(t, err)
			require.Equal(t, c.want, got)
		})
	}
}

func TestParseSizeInvalid(t *testing.T) {
	_, err := thumb.ParseSize("huge")
	require.ErrorIs(t, err, thumb.ErrUnknownSize)
}

func TestAllSizes(t *testing.T) {
	// Workers iterate AllSizes; the slice must stay in sync with the
	// enum, and ordering matters only for deterministic write order.
	require.Equal(t,
		[]thumb.Size{thumb.SizeGrid, thumb.SizePreview, thumb.SizeLightbox},
		thumb.AllSizes(),
	)
}

func TestThumbKey(t *testing.T) {
	// Versioned path per spec §7.1 — changes here break the HTTP
	// handler and the worker simultaneously.
	require.Equal(t,
		".thumbs/abc-123/v5/grid.webp",
		thumb.ThumbKey("abc-123", 5, thumb.SizeGrid),
	)
	require.Equal(t,
		".thumbs/abc-123/v0/preview.webp",
		thumb.ThumbKey("abc-123", 0, thumb.SizePreview),
	)
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/thumb/ -run TestSize -v`
Expected: build failure "package github.com/wesm/fotobank/internal/thumb is not in std …"

- [ ] **Step 3: Implement**

Create `internal/thumb/sizes.go`:

```go
// Package thumb implements the fotobank thumbnail pipeline: a DB-backed
// claim/lease Queue, a Worker that decodes/resizes/encodes, and the
// shared Size/key vocabulary used by the HTTP and CLI layers.
package thumb

import (
	"errors"
	"fmt"
)

// Size identifies one of the three thumbnail dimensions. The underlying
// string is the stable on-the-wire name (query param, filename stem).
type Size string

const (
	SizeGrid     Size = "grid"
	SizePreview  Size = "preview"
	SizeLightbox Size = "lightbox"
)

// ErrUnknownSize is returned by ParseSize when the input isn't a
// recognized size name. HTTP handlers translate this to 400.
var ErrUnknownSize = errors.New("thumb: unknown size")

// MaxEdge returns the target pixel length of the longest edge. Values
// match the vision spec §12.
func (s Size) MaxEdge() int {
	switch s {
	case SizeGrid:
		return 256
	case SizePreview:
		return 1024
	case SizeLightbox:
		return 2048
	}
	return 0
}

// ParseSize maps a query-param / filename string to a Size. Empty input
// is treated as invalid — the HTTP handler defaults to "grid" before
// calling in.
func ParseSize(s string) (Size, error) {
	switch s {
	case "grid":
		return SizeGrid, nil
	case "preview":
		return SizePreview, nil
	case "lightbox":
		return SizeLightbox, nil
	}
	return "", fmt.Errorf("%w: %q", ErrUnknownSize, s)
}

// AllSizes returns every Size in the order the worker emits them.
// Callers mutate the returned slice at their own risk; we return a
// fresh slice so callers can iterate freely.
func AllSizes() []Size {
	return []Size{SizeGrid, SizePreview, SizeLightbox}
}

// ThumbKey is the storage key for one thumbnail. The versioned v{N}/
// subdirectory makes every write write-once — critical because
// storage.Store.Write rejects overwrites (see spec §7.1).
func ThumbKey(mediaID string, version int, size Size) string {
	return fmt.Sprintf(".thumbs/%s/v%d/%s.webp", mediaID, version, size)
}
```

- [ ] **Step 4: Run to verify**

Run: `go test ./internal/thumb/ -run TestSize -v` and `go test ./internal/thumb/ -run TestThumbKey -v`
Expected: PASS on all cases.

- [ ] **Step 5: Commit**

```bash
git add internal/thumb/sizes.go internal/thumb/sizes_test.go
git commit -m "Add thumb.Size enum and versioned ThumbKey"
```

---

## Task 3: `internal/thumb/encode.go` — resize + WebP

**Files:**
- Create: `internal/thumb/encode.go`
- Create: `internal/thumb/encode_test.go`

- [ ] **Step 1: Write the test**

Create `internal/thumb/encode_test.go`:

```go
package thumb_test

import (
	"bytes"
	"image"
	"image/color"
	"testing"

	"github.com/stretchr/testify/require"
	xwebp "golang.org/x/image/webp"

	"github.com/wesm/fotobank/internal/thumb"
)

func newGradient(w, h int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 64, A: 255})
		}
	}
	return img
}

func TestResizePreservesAspectLandscape(t *testing.T) {
	// 4000x3000 → maxEdge 256 → 256x192 (aspect preserved, long edge pinned).
	out := thumb.Resize(newGradient(4000, 3000), 256)
	require.Equal(t, 256, out.Bounds().Dx())
	require.Equal(t, 192, out.Bounds().Dy())
}

func TestResizePreservesAspectPortrait(t *testing.T) {
	// 3000x4000 → maxEdge 256 → 192x256.
	out := thumb.Resize(newGradient(3000, 4000), 256)
	require.Equal(t, 192, out.Bounds().Dx())
	require.Equal(t, 256, out.Bounds().Dy())
}

func TestResizeSkipsUpscale(t *testing.T) {
	// Source smaller than target: return source unchanged. Upscaling
	// produces blurry thumbnails and bloats bytes.
	src := newGradient(100, 75)
	out := thumb.Resize(src, 256)
	require.Equal(t, 100, out.Bounds().Dx())
	require.Equal(t, 75, out.Bounds().Dy())
}

func TestEncodeWebPRoundTrip(t *testing.T) {
	src := newGradient(256, 192)
	var buf bytes.Buffer
	require.NoError(t, thumb.EncodeWebP(&buf, src, 75))
	require.Greater(t, buf.Len(), 200, "empty output?")

	decoded, err := xwebp.Decode(&buf)
	require.NoError(t, err)
	require.Equal(t, 256, decoded.Bounds().Dx())
	require.Equal(t, 192, decoded.Bounds().Dy())
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/thumb/ -run "TestResize|TestEncodeWebP" -v`
Expected: FAIL with undefined `Resize` / `EncodeWebP`.

- [ ] **Step 3: Implement**

Create `internal/thumb/encode.go`:

```go
package thumb

import (
	"fmt"
	"image"
	"io"

	"github.com/HugoSmits86/nativewebp"
	"golang.org/x/image/draw"
)

// Resize returns a new image.Image whose longest edge is at most
// maxEdge pixels, preserving aspect ratio. If the source is already
// within the budget it's returned unchanged — upscaling produces
// blurry thumbnails and wastes bytes. Uses draw.CatmullRom (bicubic),
// which is the Go team's default good-quality downscaler.
func Resize(src image.Image, maxEdge int) image.Image {
	b := src.Bounds()
	srcW, srcH := b.Dx(), b.Dy()
	if srcW <= maxEdge && srcH <= maxEdge {
		return src
	}
	var dstW, dstH int
	if srcW >= srcH {
		dstW = maxEdge
		dstH = srcH * maxEdge / srcW
	} else {
		dstH = maxEdge
		dstW = srcW * maxEdge / srcH
	}
	dst := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Over, nil)
	return dst
}

// EncodeWebP encodes img to WebP at the given quality (1-100) using
// HugoSmits86/nativewebp, a pure-Go encoder. The encoded bytes are
// written to w.
func EncodeWebP(w io.Writer, img image.Image, quality int) error {
	if quality < 1 || quality > 100 {
		return fmt.Errorf("thumb: webp quality out of range: %d", quality)
	}
	return nativewebp.Encode(w, img, &nativewebp.Options{Quality: quality})
}
```

- [ ] **Step 4: Run to verify**

Run: `go test ./internal/thumb/ -run "TestResize|TestEncodeWebP" -v`
Expected: PASS on all 4 tests.

- [ ] **Step 5: Commit**

```bash
git add internal/thumb/encode.go internal/thumb/encode_test.go go.mod go.sum
git commit -m "Add thumb.Resize + thumb.EncodeWebP"
```

---

## Task 4: `internal/thumb/decode.go` — JPEG/GIF decode + EXIF orientation

**Files:**
- Create: `internal/thumb/decode.go`
- Create: `internal/thumb/decode_test.go`
- Create: `testdata/thumb/portrait-orient6.jpg` *(hand-crafted: a 100x200 JPEG with EXIF Orientation=6 so pixel width/height display as 200x100)*

- [ ] **Step 1: Produce the orientation fixture**

Hand-craft a JPEG test fixture with EXIF orientation tag 6. The simplest path:

```bash
# Create a base 100x200 image via Go stdlib, then inject an EXIF APP1
# marker with Orientation=6 using exiftool or a small Go helper.
cd /tmp
go run - <<'EOF'
package main

import (
	"image"
	"image/color"
	"image/jpeg"
	"os"
)

func main() {
	img := image.NewRGBA(image.Rect(0, 0, 100, 200))
	for y := range 200 {
		for x := range 100 {
			c := color.RGBA{R: 200, G: uint8(y * 255 / 200), B: uint8(x * 255 / 100), A: 255}
			img.Set(x, y, c)
		}
	}
	f, _ := os.Create("/tmp/portrait-plain.jpg")
	jpeg.Encode(f, img, &jpeg.Options{Quality: 90})
	f.Close()
}
EOF

# Now inject Orientation=6 via exiftool (install via `brew install exiftool` if missing).
exiftool -Orientation=6 -overwrite_original /tmp/portrait-plain.jpg
mkdir -p /path/to/fotobank/testdata/thumb
mv /tmp/portrait-plain.jpg /path/to/fotobank/testdata/thumb/portrait-orient6.jpg
```

If exiftool is not available, skip the orientation fixture for now and mark the orientation test as `t.Skip("portrait-orient6.jpg fixture unavailable — see Task 4 notes")`. The implementation code still needs to be correct; the test can be unskipped later.

- [ ] **Step 2: Write the test**

Create `internal/thumb/decode_test.go`:

```go
package thumb_test

import (
	"bytes"
	"errors"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/thumb"
)

func TestDecodeJPEGNoOrientation(t *testing.T) {
	r := require.New(t)
	// photo-with-timestamp.jpg has no orientation → dimensions unchanged.
	bs, err := os.ReadFile(filepath.Join("..", "..", "testdata", "exif", "photo-with-timestamp.jpg"))
	r.NoError(err)
	ref, err := jpeg.Decode(bytes.NewReader(bs))
	r.NoError(err)

	img, err := thumb.Decode("image/jpeg", bytes.NewReader(bs))
	r.NoError(err)
	r.Equal(ref.Bounds().Dx(), img.Bounds().Dx())
	r.Equal(ref.Bounds().Dy(), img.Bounds().Dy())
}

func TestDecodeJPEGOrientation6RotatesDimensions(t *testing.T) {
	// Orientation=6 means "rotate 90° CW" — a 100×200 pixel buffer
	// displays as 200×100. Decode must apply the rotation so downstream
	// resize/encode sees display-correct dimensions.
	r := require.New(t)
	path := filepath.Join("..", "..", "testdata", "thumb", "portrait-orient6.jpg")
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		t.Skip("portrait-orient6.jpg fixture unavailable — see Task 4 notes")
	}
	bs, err := os.ReadFile(path)
	r.NoError(err)
	img, err := thumb.Decode("image/jpeg", bytes.NewReader(bs))
	r.NoError(err)
	r.Equal(200, img.Bounds().Dx(), "orientation=6 should swap W/H")
	r.Equal(100, img.Bounds().Dy())
}

func TestDecodeHEICRejected(t *testing.T) {
	// HEIC → no_preview per spec §11.4. Decode never opens the reader.
	_, err := thumb.Decode("image/heic", bytes.NewReader(nil))
	require.ErrorIs(t, err, thumb.ErrNoPreview)
}

func TestDecodeUnknownMIMERejected(t *testing.T) {
	_, err := thumb.Decode("application/octet-stream", bytes.NewReader(nil))
	require.Error(t, err)
	require.NotErrorIs(t, err, thumb.ErrNoPreview)
}
```

- [ ] **Step 3: Run to verify it fails**

Run: `go test ./internal/thumb/ -run TestDecode -v`
Expected: FAIL with undefined `Decode` / `ErrNoPreview`.

- [ ] **Step 4: Implement**

Create `internal/thumb/decode.go`:

```go
package thumb

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/gif"
	"image/jpeg"
	"io"

	"github.com/dsoprea/go-exif/v3"
)

// ErrNoPreview indicates the source format is one we intentionally do
// not decode in Plan C (HEIC, video). Callers translate this to
// thumb_status='no_preview' without retrying.
var ErrNoPreview = errors.New("thumb: no preview available")

// Decode returns an image.Image from src, applying EXIF orientation so
// downstream resize/encode produces display-correct pixels. Dispatch
// is by MIME type:
//
//   - image/jpeg            → image/jpeg + EXIF orientation
//   - image/gif             → image/gif (first frame; animation flattened)
//   - image/heic, image/heif → ErrNoPreview
//   - anything else         → error
//
// RAW callers should call ExtractPreview first and hand the returned
// JPEG bytes back to Decode("image/jpeg", ...).
func Decode(mime string, src io.Reader) (image.Image, error) {
	switch mime {
	case "image/jpeg":
		return decodeJPEG(src)
	case "image/gif":
		g, err := gif.Decode(src)
		if err != nil {
			return nil, fmt.Errorf("gif decode: %w", err)
		}
		return g, nil
	case "image/heic", "image/heif":
		return nil, fmt.Errorf("%w: %s (HEIC decoding requires CGO; deferred to a future plan)", ErrNoPreview, mime)
	}
	return nil, fmt.Errorf("thumb: unsupported mime %q", mime)
}

func decodeJPEG(src io.Reader) (image.Image, error) {
	// Buffer fully so we can do two passes: one for image bytes, one
	// for EXIF orientation tag. Photos are typically 2-10 MB; this is
	// fine and avoids needing a seekable reader.
	bs, err := io.ReadAll(src)
	if err != nil {
		return nil, fmt.Errorf("read jpeg: %w", err)
	}
	img, err := jpeg.Decode(bytes.NewReader(bs))
	if err != nil {
		return nil, fmt.Errorf("jpeg decode: %w", err)
	}
	orient := readExifOrientation(bs)
	return applyOrientation(img, orient), nil
}

// readExifOrientation returns the EXIF Orientation tag (1..8). Any
// parse failure — missing EXIF, no Orientation tag, unsupported
// variant — returns 1 (no rotation), which is the safe identity.
func readExifOrientation(bs []byte) int {
	raw, err := exif.SearchAndExtractExif(bs)
	if err != nil {
		return 1
	}
	_, _, err = exif.GetFlatExifData(raw, nil)
	if err != nil {
		return 1
	}
	entries, _, err := exif.GetFlatExifData(raw, nil)
	if err != nil {
		return 1
	}
	for _, e := range entries {
		if e.TagName == "Orientation" {
			if vs, ok := e.Value.([]uint16); ok && len(vs) > 0 {
				v := int(vs[0])
				if v >= 1 && v <= 8 {
					return v
				}
			}
		}
	}
	return 1
}

// applyOrientation returns a new image rotated/flipped so its pixels
// read as the EXIF orientation tag intends them to display. Tags 1-8
// per the EXIF spec; unknown → identity.
func applyOrientation(img image.Image, orient int) image.Image {
	if orient == 1 {
		return img
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	var (
		outW, outH int
		remap      func(x, y int) (int, int)
	)
	switch orient {
	case 2: // horizontal flip
		outW, outH = w, h
		remap = func(x, y int) (int, int) { return w - 1 - x, y }
	case 3: // 180°
		outW, outH = w, h
		remap = func(x, y int) (int, int) { return w - 1 - x, h - 1 - y }
	case 4: // vertical flip
		outW, outH = w, h
		remap = func(x, y int) (int, int) { return x, h - 1 - y }
	case 5: // transpose (flip + 90° CCW)
		outW, outH = h, w
		remap = func(x, y int) (int, int) { return y, x }
	case 6: // 90° CW
		outW, outH = h, w
		remap = func(x, y int) (int, int) { return y, w - 1 - x }
	case 7: // transverse (flip + 90° CW)
		outW, outH = h, w
		remap = func(x, y int) (int, int) { return h - 1 - y, w - 1 - x }
	case 8: // 90° CCW
		outW, outH = h, w
		remap = func(x, y int) (int, int) { return h - 1 - y, x }
	default:
		return img
	}
	dst := image.NewRGBA(image.Rect(0, 0, outW, outH))
	// Copy pixel-by-pixel. For 20MP images this is ~5M pixel fetches
	// — runs in under 100ms on any modern machine and is only invoked
	// when orientation ≠ 1.
	for y := range outH {
		for x := range outW {
			sx, sy := remap(x, y)
			dst.Set(x, y, img.At(b.Min.X+sx, b.Min.Y+sy))
		}
	}
	_ = draw.Draw // ensure import graph stays stable during future edits
	return dst
}
```

- [ ] **Step 5: Run to verify**

Run: `go test ./internal/thumb/ -run TestDecode -v`
Expected: PASS on all 4 tests (the orientation test may SKIP if the fixture isn't available — that's acceptable).

- [ ] **Step 6: Commit**

```bash
git add internal/thumb/decode.go internal/thumb/decode_test.go testdata/thumb/
git commit -m "Add thumb.Decode with EXIF orientation correction"
```

---

## Task 5: `internal/thumb/raw.go` — RAW embedded-preview extraction

**Files:**
- Create: `internal/thumb/raw.go`
- Create: `internal/thumb/raw_test.go`
- Create: `testdata/raw/synthetic-preview.tiff` *(generated by the test on first run)*

This task ships the mechanism without real RAW samples; a future plan can add ARW/RAF/DNG/CR2 fixtures and exercise the same code path.

- [ ] **Step 1: Write the test**

Create `internal/thumb/raw_test.go`:

```go
package thumb_test

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/thumb"
)

// buildSyntheticTIFFWithPreview constructs a minimal TIFF that embeds a
// JPEG "thumbnail" in IFD0's JPEGInterchangeFormat (513) + Length (514)
// tag pair. Real ARW/RAF/DNG/CR2 files use the same tag convention
// (sometimes in a SubIFD) so the extractor logic is the same.
func buildSyntheticTIFFWithPreview(t *testing.T) []byte {
	t.Helper()
	// JPEG preview payload.
	img := image.NewRGBA(image.Rect(0, 0, 32, 32))
	for y := range 32 {
		for x := range 32 {
			img.Set(x, y, color.RGBA{R: uint8(x * 8), G: uint8(y * 8), B: 255, A: 255})
		}
	}
	var jbuf bytes.Buffer
	require.NoError(t, jpeg.Encode(&jbuf, img, &jpeg.Options{Quality: 80}))
	jpegBytes := jbuf.Bytes()

	// TIFF header: II (little-endian), 42, offset 8.
	var buf bytes.Buffer
	buf.WriteString("II")
	_ = binary.Write(&buf, binary.LittleEndian, uint16(42))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(8))

	// IFD0: 2 entries, then offset to next IFD (0 = end).
	_ = binary.Write(&buf, binary.LittleEndian, uint16(2))

	// JPEG data will land after IFD0 + 2 entries + next-IFD offset.
	ifdSize := uint32(2 + 2*12 + 4) // entry count + entries + next-offset
	jpegOffset := uint32(8) + ifdSize

	// Tag 513: JPEGInterchangeFormat (LONG, count=1, value=offset).
	_ = binary.Write(&buf, binary.LittleEndian, uint16(513))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(4)) // LONG
	_ = binary.Write(&buf, binary.LittleEndian, uint32(1))
	_ = binary.Write(&buf, binary.LittleEndian, jpegOffset)

	// Tag 514: JPEGInterchangeFormatLength (LONG, count=1, value=len).
	_ = binary.Write(&buf, binary.LittleEndian, uint16(514))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(4))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(1))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(len(jpegBytes)))

	_ = binary.Write(&buf, binary.LittleEndian, uint32(0)) // next IFD
	buf.Write(jpegBytes)
	return buf.Bytes()
}

func TestExtractPreviewReturnsEmbeddedJPEG(t *testing.T) {
	r := require.New(t)
	raw := buildSyntheticTIFFWithPreview(t)

	img, err := thumb.ExtractPreview(bytes.NewReader(raw))
	r.NoError(err)
	r.Equal(32, img.Bounds().Dx())
	r.Equal(32, img.Bounds().Dy())
}

func TestExtractPreviewReturnsErrNoPreviewWhenMissing(t *testing.T) {
	// A TIFF with no JPEGInterchangeFormat tag → extractor reports
	// ErrNoPreview; the worker promotes this to thumb_status=no_preview.
	var buf bytes.Buffer
	buf.WriteString("II")
	_ = binary.Write(&buf, binary.LittleEndian, uint16(42))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(8))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(0)) // zero entries
	_ = binary.Write(&buf, binary.LittleEndian, uint32(0))

	_, err := thumb.ExtractPreview(bytes.NewReader(buf.Bytes()))
	require.ErrorIs(t, err, thumb.ErrNoPreview)
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/thumb/ -run TestExtractPreview -v`
Expected: FAIL with undefined `ExtractPreview`.

- [ ] **Step 3: Implement**

Create `internal/thumb/raw.go`:

```go
package thumb

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"io"

	"github.com/dsoprea/go-exif/v3"
	"github.com/dsoprea/go-exif/v3/common"
)

// ExtractPreview walks the TIFF/EXIF IFDs in src and returns the
// embedded JPEG preview decoded as an image.Image. Supports the
// JPEGInterchangeFormat (513) + JPEGInterchangeFormatLength (514)
// convention used by ARW, RAF, DNG, and CR2 — the preview may live
// in IFD0 or a SubIFD depending on the manufacturer.
//
// Returns ErrNoPreview when no JPEG preview is present; callers
// translate this to thumb_status='no_preview' without retrying.
func ExtractPreview(src io.Reader) (image.Image, error) {
	bs, err := io.ReadAll(src)
	if err != nil {
		return nil, fmt.Errorf("read raw: %w", err)
	}
	offset, length, found, err := findPreviewTags(bs)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("%w: no JPEGInterchangeFormat tag in IFDs", ErrNoPreview)
	}
	if int(offset)+int(length) > len(bs) {
		return nil, fmt.Errorf("thumb: preview bytes (offset=%d len=%d) exceed file size %d", offset, length, len(bs))
	}
	img, err := jpeg.Decode(bytes.NewReader(bs[offset : offset+length]))
	if err != nil {
		return nil, fmt.Errorf("decode embedded jpeg: %w", err)
	}
	return img, nil
}

// findPreviewTags scans every IFD (IFD0 and any SubIFDs) for the
// (JPEGInterchangeFormat, JPEGInterchangeFormatLength) pair that
// points to an embedded JPEG. Returns (offset, length, found, err).
func findPreviewTags(bs []byte) (offset uint32, length uint32, found bool, err error) {
	im, err := exifcommon.NewIfdMappingWithStandard()
	if err != nil {
		return 0, 0, false, fmt.Errorf("ifd mapping: %w", err)
	}
	ti := exif.NewTagIndex()

	// Collapse IFD+EXIF walking into a flat iteration. The dsoprea
	// library exposes Visit at several layers; GetFlatExifDataUniversalSearch
	// crawls every IFD and returns every tag, which is what we want.
	entries, _, err := exif.GetFlatExifDataUniversalSearch(bs, nil, false)
	if err != nil {
		// Some "RAWs" are actually TIFF; try raw TIFF walk as fallback.
		return findPreviewTagsTIFF(bs, im, ti)
	}
	for _, e := range entries {
		switch e.TagId {
		case 513: // JPEGInterchangeFormat
			if vs, ok := e.Value.([]uint32); ok && len(vs) > 0 {
				offset = vs[0]
			}
		case 514: // JPEGInterchangeFormatLength
			if vs, ok := e.Value.([]uint32); ok && len(vs) > 0 {
				length = vs[0]
			}
		}
	}
	if offset > 0 && length > 0 {
		return offset, length, true, nil
	}
	return findPreviewTagsTIFF(bs, im, ti)
}

func findPreviewTagsTIFF(
	bs []byte,
	im *exifcommon.IfdMapping,
	ti *exif.TagIndex,
) (offset uint32, length uint32, found bool, err error) {
	// Fallback TIFF walker: parse the TIFF header, walk each IFD,
	// collect tags 513/514. Used when GetFlatExifDataUniversalSearch
	// rejects the file (pure TIFF without an EXIF segment).
	_, index, err := exif.Collect(im, ti, bs)
	if err != nil {
		return 0, 0, false, nil // treat collect failure as "no preview found"
	}
	for _, ifd := range index.Ifds {
		for _, ite := range ifd.Entries() {
			if ite.TagId() == 513 {
				if vs, err := ite.Value(); err == nil {
					if u32s, ok := vs.([]uint32); ok && len(u32s) > 0 {
						offset = u32s[0]
					}
				}
			}
			if ite.TagId() == 514 {
				if vs, err := ite.Value(); err == nil {
					if u32s, ok := vs.([]uint32); ok && len(u32s) > 0 {
						length = u32s[0]
					}
				}
			}
		}
	}
	if offset > 0 && length > 0 {
		return offset, length, true, nil
	}
	return 0, 0, false, nil
}
```

- [ ] **Step 4: Run to verify**

Run: `go test ./internal/thumb/ -run TestExtractPreview -v`
Expected: PASS on both tests.

If the dsoprea API surface differs from what this file assumes (API names have drifted across minor versions), adjust the walker to match the actual package contents — the logic stays the same: collect tag 513 + 514 values from any IFD, slice the bytes, JPEG-decode. Run `go doc github.com/dsoprea/go-exif/v3` to see current symbols if needed.

- [ ] **Step 5: Commit**

```bash
git add internal/thumb/raw.go internal/thumb/raw_test.go
git commit -m "Add thumb.ExtractPreview for RAW embedded JPEGs"
```

---

## Task 6: `internal/thumb/queue.go` — claim/sweep/fencing

**Files:**
- Create: `internal/thumb/queue.go`
- Create: `internal/thumb/queue_test.go`

- [ ] **Step 1: Write the test**

Create `internal/thumb/queue_test.go`:

```go
package thumb_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/media"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/testutil"
	"github.com/wesm/fotobank/internal/thumb"
)

// queueFixture seeds an owner + N pending rows and returns them plus
// the opened Queue.
type queueFixture struct {
	q     *thumb.Queue
	owner owners.Principal
	ids   []string
}

func newQueueFixture(t *testing.T, nRows int) queueFixture {
	t.Helper()
	d := testutil.OpenTestDB(t)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	q := thumb.NewQueue(d.WriteDB(), d.ReadDB())
	p := owners.Principal{Hub: "h", UserID: "u"}
	_, err := d.WriteDB().ExecContext(context.Background(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		p.Hub, p.UserID, "sk", time.Now().UTC(),
	)
	require.NoError(t, err)
	ids := make([]string, nRows)
	for i := range nRows {
		m := media.Media{
			ID:               uuid.NewString(),
			Owner:            p,
			Type:             media.TypePhoto,
			MimeType:         "image/jpeg",
			Path:             "2024/a.jpg",
			OriginalFilename: "a.jpg",
			ImportedAt:       time.Now().UTC().Add(time.Duration(i) * time.Second),
			Size:             100,
			Checksum:         uuid.NewString(),
			ThumbStatus:      "pending",
		}
		m.Path = "2024/" + m.ID + ".jpg" // ensure per-row path uniqueness
		require.NoError(t, repo.Insert(context.Background(), m))
		ids[i] = m.ID
	}
	return queueFixture{q: q, owner: p, ids: ids}
}

func TestClaimBatchReturnsRowsAndMarksWorking(t *testing.T) {
	r := require.New(t)
	fx := newQueueFixture(t, 3)
	claims, err := fx.q.ClaimBatch(context.Background(), 2)
	r.NoError(err)
	r.Len(claims, 2)
	for _, c := range claims {
		r.NotZero(c.ClaimedAt)
		r.Equal(fx.owner, c.Media.Owner)
	}
}

func TestClaimBatchIsAtomicAcrossCallers(t *testing.T) {
	// Two callers claiming 10 rows each on a 3-row table must see a
	// total of 3 claimed (not 6). SQLite's write lock serializes them.
	r := require.New(t)
	fx := newQueueFixture(t, 3)

	a, err := fx.q.ClaimBatch(context.Background(), 10)
	r.NoError(err)
	b, err := fx.q.ClaimBatch(context.Background(), 10)
	r.NoError(err)
	r.Equal(3, len(a)+len(b))

	seen := map[string]bool{}
	for _, c := range append(a, b...) {
		r.False(seen[c.Media.ID], "row %s claimed twice", c.Media.ID)
		seen[c.Media.ID] = true
	}
}

func TestMarkReadySucceedsWithMatchingToken(t *testing.T) {
	r := require.New(t)
	fx := newQueueFixture(t, 1)
	claims, err := fx.q.ClaimBatch(context.Background(), 1)
	r.NoError(err)
	r.Len(claims, 1)

	c := claims[0]
	r.NoError(fx.q.MarkReady(context.Background(), c.Media.ID, c.Media.ThumbVersion, c.ClaimedAt))
}

func TestMarkReadyReturnsErrClaimLostOnStaleToken(t *testing.T) {
	r := require.New(t)
	fx := newQueueFixture(t, 1)
	claims, err := fx.q.ClaimBatch(context.Background(), 1)
	r.NoError(err)
	c := claims[0]

	// Token far in the past cannot match the DB's current claimed_at.
	stale := c.ClaimedAt.Add(-time.Hour)
	err = fx.q.MarkReady(context.Background(), c.Media.ID, c.Media.ThumbVersion, stale)
	r.ErrorIs(err, thumb.ErrClaimLost)
}

func TestSweepLeasesBumpsVersionAndResetsClaim(t *testing.T) {
	r := require.New(t)
	fx := newQueueFixture(t, 1)
	claims, err := fx.q.ClaimBatch(context.Background(), 1)
	r.NoError(err)
	c := claims[0]

	// Now sweep with a zero lease duration — every claim qualifies
	// as expired.
	n, err := fx.q.SweepLeases(context.Background(), 0)
	r.NoError(err)
	r.Equal(1, n)

	// The stale worker's MarkReady must fail.
	err = fx.q.MarkReady(context.Background(), c.Media.ID, c.Media.ThumbVersion, c.ClaimedAt)
	r.ErrorIs(err, thumb.ErrClaimLost)

	// Re-claim should now see version+1.
	next, err := fx.q.ClaimBatch(context.Background(), 1)
	r.NoError(err)
	r.Len(next, 1)
	r.Equal(c.Media.ThumbVersion+1, next[0].Media.ThumbVersion)
}

func TestEnqueueBumpsVersionForMatchingRows(t *testing.T) {
	r := require.New(t)
	fx := newQueueFixture(t, 2)

	n, err := fx.q.Enqueue(context.Background(), thumb.EnqueueFilter{All: true, Owner: fx.owner})
	r.NoError(err)
	r.Equal(2, n)

	// Claim and verify version bumped from 0 to 1 for each row.
	claims, err := fx.q.ClaimBatch(context.Background(), 2)
	r.NoError(err)
	r.Len(claims, 2)
	for _, c := range claims {
		r.Equal(1, c.Media.ThumbVersion)
	}
}

func TestRegenerateWhileWorkingLosesClaim(t *testing.T) {
	// Worker claims row at v0; regenerate enqueues, bumping to v1.
	// The worker's MarkReady at v0 must lose.
	r := require.New(t)
	fx := newQueueFixture(t, 1)
	claims, err := fx.q.ClaimBatch(context.Background(), 1)
	r.NoError(err)
	c := claims[0]

	_, err = fx.q.Enqueue(context.Background(),
		thumb.EnqueueFilter{IDs: []string{c.Media.ID}, Owner: fx.owner})
	r.NoError(err)

	err = fx.q.MarkReady(context.Background(), c.Media.ID, c.Media.ThumbVersion, c.ClaimedAt)
	r.True(errors.Is(err, thumb.ErrClaimLost))
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/thumb/ -run TestClaimBatch -v`
Expected: FAIL with undefined `NewQueue`, `ClaimBatch`, etc.

- [ ] **Step 3: Implement**

Create `internal/thumb/queue.go`:

```go
package thumb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wesm/fotobank/internal/media"
	"github.com/wesm/fotobank/internal/owners"
)

// ErrClaimLost is returned by terminal Mark* methods when the
// (id, version, token) WHERE clause matches zero rows — another
// worker's sweep or an Enqueue has superseded our claim.
var ErrClaimLost = errors.New("thumb: claim lost (sweep or regenerate won)")

// Claim is one row handed out by ClaimBatch: the media to process
// plus the claim token (thumb_claimed_at) the worker must pass back
// into Mark* calls.
type Claim struct {
	Media     media.Media
	ClaimedAt time.Time
}

// EnqueueFilter narrows which rows Enqueue targets. Either All or at
// least one of IDs/MediaType/Status/Since must be set. Owner scopes
// the filter to one principal.
type EnqueueFilter struct {
	Owner     owners.Principal
	All       bool
	IDs       []string
	MediaType media.Type
	Status    string
	Since     *time.Time
}

// Queue is the DB-only layer of the thumbnail pipeline. It owns the
// claim/lease SQL and is safe to share across goroutines.
type Queue struct {
	rw *sql.DB
	ro *sql.DB
}

// NewQueue constructs a Queue against the split read/write pools used
// elsewhere in the codebase.
func NewQueue(rw, ro *sql.DB) *Queue {
	return &Queue{rw: rw, ro: ro}
}

const claimBatchSQL = `
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
          thumb_version, checksum, thumb_claimed_at
`

// ClaimBatch transitions up to n pending rows to 'working' and returns
// the claimed rows with their claim tokens. Uses a single UPDATE…
// RETURNING so the claim is atomic under SQLite's write lock.
func (q *Queue) ClaimBatch(ctx context.Context, n int) ([]Claim, error) {
	if n <= 0 {
		return nil, nil
	}
	now := time.Now().UTC()
	rows, err := q.rw.QueryContext(ctx, claimBatchSQL, now, n)
	if err != nil {
		return nil, fmt.Errorf("claim batch: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Claim
	for rows.Next() {
		var c Claim
		var mediaType string
		var claimedAt time.Time
		if err := rows.Scan(
			&c.Media.ID,
			&c.Media.Owner.Hub,
			&c.Media.Owner.UserID,
			&mediaType,
			&c.Media.MimeType,
			&c.Media.Path,
			&c.Media.ThumbVersion,
			&c.Media.Checksum,
			&claimedAt,
		); err != nil {
			return nil, fmt.Errorf("scan claim: %w", err)
		}
		c.Media.Type = media.Type(mediaType)
		c.ClaimedAt = claimedAt
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("claim rows: %w", err)
	}
	return out, nil
}

const sweepLeasesSQL = `
UPDATE media
   SET thumb_status     = 'pending',
       thumb_claimed_at = NULL,
       thumb_version    = thumb_version + 1,
       thumb_updated_at = ?
 WHERE thumb_status = 'working'
   AND thumb_claimed_at < ?
`

// SweepLeases returns 'working' rows whose lease has expired (older
// than `after`) back to 'pending', bumping thumb_version so lease
// retries never collide with the no-clobber storage write. Returns
// the number of rows reset.
func (q *Queue) SweepLeases(ctx context.Context, after time.Duration) (int, error) {
	now := time.Now().UTC()
	cutoff := now.Add(-after)
	res, err := q.rw.ExecContext(ctx, sweepLeasesSQL, now, cutoff)
	if err != nil {
		return 0, fmt.Errorf("sweep leases: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

const markReadySQL = `
UPDATE media
   SET thumb_status     = 'ready',
       thumb_updated_at = ?,
       thumb_claimed_at = NULL
 WHERE id = ? AND thumb_version = ? AND thumb_claimed_at = ?
`

// MarkReady transitions a claimed row to 'ready'. Returns ErrClaimLost
// if the (id, version, token) triple no longer matches — meaning a
// sweep or regenerate claimed this slot from under us.
func (q *Queue) MarkReady(ctx context.Context, id string, version int, token time.Time) error {
	return q.finalize(ctx, markReadySQL, id, version, token)
}

const markNoPreviewSQL = `
UPDATE media
   SET thumb_status     = 'no_preview',
       thumb_updated_at = ?,
       thumb_claimed_at = NULL
 WHERE id = ? AND thumb_version = ? AND thumb_claimed_at = ?
`

// MarkNoPreview transitions a claimed row to 'no_preview'. Used when
// the source type (HEIC, video) is known-unsupported in this plan or
// when a RAW has no embedded preview.
func (q *Queue) MarkNoPreview(ctx context.Context, id string, version int, token time.Time) error {
	return q.finalize(ctx, markNoPreviewSQL, id, version, token)
}

const markFailedSQL = `
UPDATE media
   SET thumb_status     = 'failed',
       thumb_updated_at = ?,
       thumb_claimed_at = NULL
 WHERE id = ? AND thumb_version = ? AND thumb_claimed_at = ?
`

// MarkFailed transitions a claimed row to 'failed'. cause is logged at
// the call site; we don't persist it in Plan C (no column for it).
func (q *Queue) MarkFailed(ctx context.Context, id string, version int, token time.Time, _ error) error {
	return q.finalize(ctx, markFailedSQL, id, version, token)
}

func (q *Queue) finalize(ctx context.Context, query, id string, version int, token time.Time) error {
	res, err := q.rw.ExecContext(ctx, query, time.Now().UTC(), id, version, token)
	if err != nil {
		return fmt.Errorf("finalize %s: %w", id, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("%w: id=%s version=%d", ErrClaimLost, id, version)
	}
	return nil
}

// Enqueue bumps thumb_version and sets thumb_status='pending' on every
// row matching filter. Returns the number of rows updated. Caller-
// facing scoping (principal) is enforced via filter.Owner.
func (q *Queue) Enqueue(ctx context.Context, filter EnqueueFilter) (int, error) {
	where, args, err := buildEnqueueWhere(filter)
	if err != nil {
		return 0, err
	}
	now := time.Now().UTC()
	query := `UPDATE media
		  SET thumb_status     = 'pending',
		      thumb_version    = thumb_version + 1,
		      thumb_updated_at = ?,
		      thumb_claimed_at = NULL
		   WHERE ` + where
	execArgs := append([]any{now}, args...)
	res, err := q.rw.ExecContext(ctx, query, execArgs...)
	if err != nil {
		return 0, fmt.Errorf("enqueue: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func buildEnqueueWhere(filter EnqueueFilter) (string, []any, error) {
	if (filter.Owner == owners.Principal{}) {
		return "", nil, errors.New("thumb: Enqueue requires Owner")
	}
	parts := []string{"owner_hub = ?", "owner_user_id = ?"}
	args := []any{filter.Owner.Hub, filter.Owner.UserID}
	specific := false
	if filter.All {
		specific = true
	}
	if len(filter.IDs) > 0 {
		placeholders := strings.Repeat("?,", len(filter.IDs))
		placeholders = placeholders[:len(placeholders)-1]
		parts = append(parts, "id IN ("+placeholders+")")
		for _, id := range filter.IDs {
			args = append(args, id)
		}
		specific = true
	}
	if filter.MediaType != "" {
		parts = append(parts, "media_type = ?")
		args = append(args, string(filter.MediaType))
		specific = true
	}
	if filter.Status != "" {
		parts = append(parts, "thumb_status = ?")
		args = append(args, filter.Status)
		specific = true
	}
	if filter.Since != nil {
		parts = append(parts, "imported_at >= ?")
		args = append(args, *filter.Since)
		specific = true
	}
	if !specific {
		return "", nil, errors.New("thumb: Enqueue requires All or at least one selector")
	}
	return strings.Join(parts, " AND "), args, nil
}
```

- [ ] **Step 4: Run to verify**

Run: `go test ./internal/thumb/ -v`
Expected: PASS on all queue tests (plus the existing size/encode/decode/raw tests).

- [ ] **Step 5: Commit**

```bash
git add internal/thumb/queue.go internal/thumb/queue_test.go
git commit -m "Add thumb.Queue with version-bumping sweep and claim fencing"
```

---

## Task 7: `internal/thumb/worker.go` — the full pipeline

**Files:**
- Create: `internal/thumb/worker.go`
- Create: `internal/thumb/worker_test.go`

- [ ] **Step 1: Write the test**

Create `internal/thumb/worker_test.go`:

```go
package thumb_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"image/jpeg"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/media"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/storage"
	"github.com/wesm/fotobank/internal/testutil"
	"github.com/wesm/fotobank/internal/thumb"
)

func seedPhotoRow(t *testing.T, repo *media.Repo, p owners.Principal, nasRoot, sk string) media.Media {
	t.Helper()
	id := uuid.NewString()
	img, err := os.ReadFile(filepath.Join("..", "..", "testdata", "exif", "photo-with-timestamp.jpg"))
	require.NoError(t, err)
	// Place the source bytes where NASOnly will find them.
	rel := "2024/" + id + ".jpg"
	full := filepath.Join(nasRoot, sk, rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o700))
	require.NoError(t, os.WriteFile(full, img, 0o600))

	m := media.Media{
		ID:               id,
		Owner:            p,
		Type:             media.TypePhoto,
		MimeType:         "image/jpeg",
		Path:             rel,
		OriginalFilename: "x.jpg",
		ImportedAt:       time.Now().UTC(),
		Size:             int64(len(img)),
		Checksum:         hex.EncodeToString([]byte(id))[:32],
		ThumbStatus:      "pending",
	}
	require.NoError(t, repo.Insert(context.Background(), m))
	return m
}

func TestWorkerDrainsPendingRowToReady(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	q := thumb.NewQueue(d.WriteDB(), d.ReadDB())

	tmp := t.TempDir()
	nasRoot := filepath.Join(tmp, "nas")
	r.NoError(os.MkdirAll(nasRoot, 0o700))

	p := owners.Principal{Hub: "h", UserID: "u"}
	_, err := d.WriteDB().ExecContext(context.Background(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		p.Hub, p.UserID, "sk", time.Now().UTC(),
	)
	r.NoError(err)
	store := storage.NewNASOnly(nasRoot, map[owners.Principal]string{p: "sk"})
	m := seedPhotoRow(t, repo, p, nasRoot, "sk")

	w := thumb.NewWorker(q, store, thumb.Config{
		WorkerConcurrency: 2,
		PollInterval:      10 * time.Millisecond,
		LeaseTimeout:      5 * time.Second,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	// Poll the DB until the row reaches 'ready' or we time out.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got, err := repo.GetByID(context.Background(), m.ID)
		r.NoError(err)
		if got.ThumbStatus == "ready" {
			cancel()
			<-done
			// Bytes must exist at the versioned key.
			key := thumb.ThumbKey(m.ID, got.ThumbVersion, thumb.SizeGrid)
			rc, err := store.ReadRange(context.Background(), p, key, 0, -1)
			r.NoError(err)
			defer func() { _ = rc.Close() }()
			bs, err := io.ReadAll(rc)
			r.NoError(err)
			r.Greater(len(bs), 100, "grid.webp empty")
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("worker did not mark row ready within 5s")
}

func TestWorkerSkipsVideoAsNoPreview(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	q := thumb.NewQueue(d.WriteDB(), d.ReadDB())

	tmp := t.TempDir()
	nasRoot := filepath.Join(tmp, "nas")
	r.NoError(os.MkdirAll(nasRoot, 0o700))

	p := owners.Principal{Hub: "h", UserID: "u"}
	_, err := d.WriteDB().ExecContext(context.Background(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		p.Hub, p.UserID, "sk", time.Now().UTC(),
	)
	r.NoError(err)
	store := storage.NewNASOnly(nasRoot, map[owners.Principal]string{p: "sk"})

	m := media.Media{
		ID:               uuid.NewString(),
		Owner:            p,
		Type:             media.TypeVideo,
		MimeType:         "video/mp4",
		Path:             "movies/fake.mp4",
		OriginalFilename: "fake.mp4",
		ImportedAt:       time.Now().UTC(),
		Size:             100,
		Checksum:         "v" + uuid.NewString(),
		ThumbStatus:      "pending",
	}
	r.NoError(repo.Insert(context.Background(), m))

	w := thumb.NewWorker(q, store, thumb.Config{
		WorkerConcurrency: 1, PollInterval: 10 * time.Millisecond, LeaseTimeout: 5 * time.Second,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() { _ = w.Run(ctx) }()

	for time.Now().Before(time.Now().Add(3 * time.Second)) {
		got, err := repo.GetByID(context.Background(), m.ID)
		r.NoError(err)
		if got.ThumbStatus == "no_preview" {
			cancel()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("video row never reached no_preview")
}

// TestWorkerStaleWriteDoesNotCorruptReclaim exercises the critical
// SweepLeases-bumps-version regression: worker A writes partial v0
// bytes, sweep fires, worker B claims at v1 and writes successfully
// without hitting ErrPathOccupied.
func TestWorkerStaleWriteDoesNotCorruptReclaim(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	q := thumb.NewQueue(d.WriteDB(), d.ReadDB())
	tmp := t.TempDir()
	nasRoot := filepath.Join(tmp, "nas")
	r.NoError(os.MkdirAll(nasRoot, 0o700))

	p := owners.Principal{Hub: "h", UserID: "u"}
	_, err := d.WriteDB().ExecContext(context.Background(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		p.Hub, p.UserID, "sk", time.Now().UTC(),
	)
	r.NoError(err)
	store := storage.NewNASOnly(nasRoot, map[owners.Principal]string{p: "sk"})
	m := seedPhotoRow(t, repo, p, nasRoot, "sk")

	// Simulate worker A's partial v0 write by shipping a grid.webp
	// at that key before any worker runs.
	buf := &bytes.Buffer{}
	img, err := jpeg.Decode(bytes.NewReader(mustReadFile(t,
		filepath.Join("..", "..", "testdata", "exif", "photo-with-timestamp.jpg"))))
	r.NoError(err)
	r.NoError(thumb.EncodeWebP(buf, thumb.Resize(img, 256), 75))
	staleKey := thumb.ThumbKey(m.ID, 0, thumb.SizeGrid)
	_, err = store.Write(context.Background(), p, staleKey, bytes.NewReader(buf.Bytes()))
	r.NoError(err)

	// Hand-claim then sweep-with-zero-grace to bump thumb_version.
	claims, err := q.ClaimBatch(context.Background(), 1)
	r.NoError(err)
	r.Len(claims, 1)
	_, err = q.SweepLeases(context.Background(), 0)
	r.NoError(err)

	// Now start the worker; it should claim at v1 and succeed.
	w := thumb.NewWorker(q, store, thumb.Config{
		WorkerConcurrency: 1, PollInterval: 10 * time.Millisecond, LeaseTimeout: 5 * time.Second,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = w.Run(ctx) }()

	for time.Now().Before(time.Now().Add(5 * time.Second)) {
		got, err := repo.GetByID(context.Background(), m.ID)
		r.NoError(err)
		if got.ThumbStatus == "ready" {
			r.Equal(1, got.ThumbVersion, "reclaimer should be at v1, not v0")
			cancel()
			// v1 bytes must exist; v0 bytes should still be there (orphaned).
			_, err := store.ReadRange(context.Background(), p, thumb.ThumbKey(m.ID, 1, thumb.SizeGrid), 0, -1)
			r.NoError(err)
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("row never reached ready after sweep+reclaim")
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	bs, err := os.ReadFile(path)
	require.NoError(t, err)
	return bs
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/thumb/ -run TestWorker -v`
Expected: FAIL with undefined `Worker` / `NewWorker` / `Config`.

- [ ] **Step 3: Implement**

Create `internal/thumb/worker.go`:

```go
package thumb

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/wesm/fotobank/internal/media"
	"github.com/wesm/fotobank/internal/storage"
)

// sweepInterval is how often the sweep goroutine runs. Not TOML-tunable
// until a call site demands it.
const sweepInterval = 1 * time.Minute

// Config tunes worker runtime behavior. Wired from cfg.Thumbs in the
// CLI; tests construct directly.
type Config struct {
	WorkerConcurrency int
	PollInterval      time.Duration
	LeaseTimeout      time.Duration
}

// Worker owns the poll + sweep goroutines that drain the thumbnail
// queue. One worker instance per server process.
type Worker struct {
	queue     *Queue
	store     storage.Store
	cfg       Config
	drainLock sync.Mutex
}

// NewWorker builds a Worker. Callers wire it up in cli/server.go
// alongside the flash janitor.
func NewWorker(q *Queue, s storage.Store, cfg Config) *Worker {
	if cfg.WorkerConcurrency <= 0 {
		cfg.WorkerConcurrency = 4
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 5 * time.Second
	}
	if cfg.LeaseTimeout <= 0 {
		cfg.LeaseTimeout = 10 * time.Minute
	}
	return &Worker{queue: q, store: s, cfg: cfg}
}

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

func (w *Worker) runPoll(ctx context.Context) {
	t := time.NewTicker(w.cfg.PollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if !w.drainLock.TryLock() {
				continue
			}
			w.drain(ctx)
			w.drainLock.Unlock()
		}
	}
}

func (w *Worker) runSweep(ctx context.Context) {
	t := time.NewTicker(sweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := w.queue.SweepLeases(ctx, w.cfg.LeaseTimeout); err != nil {
				slog.Error("thumb sweep", "err", err)
			}
		}
	}
}

func (w *Worker) drain(ctx context.Context) {
	claims, err := w.queue.ClaimBatch(ctx, 2*w.cfg.WorkerConcurrency)
	if err != nil {
		slog.Error("thumb claim", "err", err)
		return
	}
	sem := make(chan struct{}, w.cfg.WorkerConcurrency)
	var wg sync.WaitGroup
	for _, c := range claims {
		sem <- struct{}{}
		wg.Add(1)
		go func(claim Claim) {
			defer wg.Done()
			defer func() { <-sem }()
			cctx, cancel := context.WithTimeout(ctx, w.cfg.LeaseTimeout/2)
			defer cancel()
			w.processOne(cctx, claim)
		}(c)
	}
	wg.Wait()
}

func (w *Worker) processOne(ctx context.Context, c Claim) {
	m, token := c.Media, c.ClaimedAt

	if m.Type == media.TypeVideo || isHEICMime(m.MimeType) {
		w.finalizeNoPreview(ctx, m, token)
		return
	}

	img, err := w.decodeSource(ctx, m)
	if err != nil {
		if errors.Is(err, ErrNoPreview) {
			w.finalizeNoPreview(ctx, m, token)
			return
		}
		w.finalizeFailed(ctx, m, token, err)
		return
	}

	for _, sz := range AllSizes() {
		var buf bytes.Buffer
		if err := EncodeWebP(&buf, Resize(img, sz.MaxEdge()), 75); err != nil {
			w.finalizeFailed(ctx, m, token, fmt.Errorf("encode %s: %w", sz, err))
			return
		}
		key := ThumbKey(m.ID, m.ThumbVersion, sz)
		if _, err := w.store.Write(ctx, m.Owner, key, bytes.NewReader(buf.Bytes())); err != nil {
			w.finalizeFailed(ctx, m, token, fmt.Errorf("write %s: %w", sz, err))
			return
		}
	}
	if err := w.queue.MarkReady(ctx, m.ID, m.ThumbVersion, token); err != nil {
		if !errors.Is(err, ErrClaimLost) {
			slog.Error("thumb mark ready", "id", m.ID, "err", err)
		}
	}
}

func (w *Worker) decodeSource(ctx context.Context, m media.Media) (img imageSource, err error) {
	rc, err := w.store.ReadRange(ctx, m.Owner, m.Path, 0, -1)
	if err != nil {
		return nil, fmt.Errorf("read source: %w", err)
	}
	defer func() { _ = rc.Close() }()
	if isRAWMime(m.MimeType) {
		return ExtractPreview(rc)
	}
	return Decode(m.MimeType, rc)
}

// imageSource is a narrowing of image.Image that lets decodeSource
// return either Decode or ExtractPreview results uniformly.
type imageSource = interface {
	// We only need what image.Image exposes — this alias lets callers
	// treat both decode paths identically.
	Bounds() imageBounds
	At(x, y int) imageColor
}

// NOTE — the imageSource / imageBounds / imageColor aliases above are
// awkward; use image.Image directly. Delete this interface and switch
// decodeSource's return type to (image.Image, error) plus the
// corresponding `import "image"` at the top of the file. Left as an
// explicit plan signal so the implementer sees the need to drop the
// aliases rather than silently propagating them.
```

*(Implementation note to the engineer: the `imageSource` alias scaffold in the block above is a sign you've copy-pasted awkwardness. Before committing, replace it with a direct `image.Image` return type. The cleaner version reads:)*

```go
func (w *Worker) decodeSource(ctx context.Context, m media.Media) (image.Image, error) {
	rc, err := w.store.ReadRange(ctx, m.Owner, m.Path, 0, -1)
	if err != nil {
		return nil, fmt.Errorf("read source: %w", err)
	}
	defer func() { _ = rc.Close() }()
	if isRAWMime(m.MimeType) {
		return ExtractPreview(rc)
	}
	return Decode(m.MimeType, rc)
}
```

Add `"image"` to the import block and delete the aliases.

Also add the helper:

```go
func (w *Worker) finalizeNoPreview(ctx context.Context, m media.Media, token time.Time) {
	if err := w.queue.MarkNoPreview(ctx, m.ID, m.ThumbVersion, token); err != nil {
		if !errors.Is(err, ErrClaimLost) {
			slog.Error("thumb mark no_preview", "id", m.ID, "err", err)
		}
	}
}

func (w *Worker) finalizeFailed(ctx context.Context, m media.Media, token time.Time, cause error) {
	slog.Error("thumb processing failed", "id", m.ID, "cause", cause)
	if err := w.queue.MarkFailed(ctx, m.ID, m.ThumbVersion, token, cause); err != nil {
		if !errors.Is(err, ErrClaimLost) {
			slog.Error("thumb mark failed", "id", m.ID, "err", err)
		}
	}
}

func isHEICMime(mime string) bool {
	return mime == "image/heic" || mime == "image/heif"
}

func isRAWMime(mime string) bool {
	switch mime {
	case "image/x-sony-arw", "image/x-fuji-raf",
		"image/x-adobe-dng", "image/x-canon-cr2":
		return true
	}
	return false
}

// Read with io.ReadAll for the RAW path because ExtractPreview needs
// the full bytes. The JPEG/GIF path in Decode also buffers (see
// decode.go). Streaming decode for large RAWs is deferred — typical
// photos are <50 MB and fit comfortably in memory.
var _ = io.ReadAll
```

- [ ] **Step 4: Run to verify**

Run: `go test ./internal/thumb/ -v`
Expected: PASS on all worker tests. The "stale write doesn't corrupt reclaim" test is the critical regression guard for spec §5.1.

- [ ] **Step 5: Commit**

```bash
git add internal/thumb/worker.go internal/thumb/worker_test.go
git commit -m "Add thumb.Worker with claim-fenced processOne pipeline"
```

---

## Task 8: Wire `ThumbsCacheEnabled` into `storage.FlashCache`

**Files:**
- Modify: `internal/storage/flash_cache.go`
- Modify: `internal/storage/flash_cache_test.go`

- [ ] **Step 1: Write the test**

Add to `internal/storage/flash_cache_test.go`:

```go
func TestFlashCacheRoutesThumbsToThumbsRootWhenEnabled(t *testing.T) {
	// Thumbs keys (".thumbs/…") populate the thumbs root, not the
	// originals root. Janitor walks only originals.
	r := require.New(t)
	tmp := t.TempDir()
	nasRoot := filepath.Join(tmp, "nas")
	originalsRoot := filepath.Join(tmp, "flash", "originals")
	thumbsRoot := filepath.Join(tmp, "flash", "thumbs")
	r.NoError(os.MkdirAll(nasRoot, 0o700))

	p := owners.Principal{Hub: "h", UserID: "u"}
	keys := map[owners.Principal]string{p: "sk"}
	nas := storage.NewNASOnly(nasRoot, keys)
	cache := storage.NewFlashCache(nas, originalsRoot, keys, storage.FlashCacheOptions{})
	cache.EnableThumbs(thumbsRoot)

	_, err := cache.Write(context.Background(), p, ".thumbs/abc/v0/grid.webp", strings.NewReader("bytes"))
	r.NoError(err)

	// Allow the best-effort populate goroutine to finish.
	deadline := time.Now().Add(2 * time.Second)
	thumbPath := filepath.Join(thumbsRoot, "sk", ".thumbs", "abc", "v0", "grid.webp")
	for time.Now().Before(deadline) {
		if _, err := os.Stat(thumbPath); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	_, statErr := os.Stat(thumbPath)
	r.NoError(statErr, "thumbs bytes missing from thumbs root")
	// Must NOT have populated originals root.
	origPath := filepath.Join(originalsRoot, "sk", ".thumbs", "abc", "v0", "grid.webp")
	_, origErr := os.Stat(origPath)
	r.True(os.IsNotExist(origErr), "thumbs bytes leaked into originals root")
}

func TestFlashCacheSkipsThumbsWhenDisabled(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	nasRoot := filepath.Join(tmp, "nas")
	originalsRoot := filepath.Join(tmp, "flash", "originals")
	r.NoError(os.MkdirAll(nasRoot, 0o700))

	p := owners.Principal{Hub: "h", UserID: "u"}
	keys := map[owners.Principal]string{p: "sk"}
	nas := storage.NewNASOnly(nasRoot, keys)
	cache := storage.NewFlashCache(nas, originalsRoot, keys, storage.FlashCacheOptions{})
	// EnableThumbs never called — cache disabled for thumbs.

	_, err := cache.Write(context.Background(), p, ".thumbs/abc/v0/grid.webp", strings.NewReader("bytes"))
	r.NoError(err)

	// Nothing lands in either flash root — writes go straight to NAS.
	time.Sleep(100 * time.Millisecond)
	_, origErr := os.Stat(filepath.Join(originalsRoot, "sk", ".thumbs", "abc", "v0", "grid.webp"))
	r.True(os.IsNotExist(origErr))
}

func TestFlashJanitorIgnoresThumbsRoot(t *testing.T) {
	// Sibling thumbs root under {flash.root} must survive an Evict run
	// that walks the originals root.
	r := require.New(t)
	tmp := t.TempDir()
	originalsRoot := filepath.Join(tmp, "flash", "originals")
	thumbsRoot := filepath.Join(tmp, "flash", "thumbs")
	r.NoError(os.MkdirAll(originalsRoot, 0o700))
	r.NoError(os.MkdirAll(thumbsRoot, 0o700))

	sentinel := filepath.Join(thumbsRoot, "sentinel")
	r.NoError(os.WriteFile(sentinel, []byte("thumb bytes"), 0o600))
	old := time.Now().Add(-365 * 24 * time.Hour)
	r.NoError(os.Chtimes(sentinel, old, old))

	p := owners.Principal{Hub: "h", UserID: "u"}
	keys := map[owners.Principal]string{p: "sk"}
	nas := storage.NewNASOnly(filepath.Join(tmp, "nas"), keys)
	cache := storage.NewFlashCache(nas, originalsRoot, keys, storage.FlashCacheOptions{OriginalsCacheDays: 1})
	cache.EnableThumbs(thumbsRoot)

	r.NoError(cache.Evict(context.Background()))
	_, err := os.Stat(sentinel)
	r.NoError(err, "janitor swept thumbs root")
}
```

Add the needed imports (`"strings"`, etc.) at the top of the file if missing.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/storage/ -run "TestFlashCacheRoutesThumbs|TestFlashCacheSkipsThumbs|TestFlashJanitorIgnoresThumbs" -v`
Expected: FAIL (compilation error — `EnableThumbs` undefined).

- [ ] **Step 3: Implement**

Modify `internal/storage/flash_cache.go`:

```go
// At the top, in the FlashCache struct, add thumbsRoot:

type FlashCache struct {
	nas         Store
	flashRoot   string // originals root
	thumbsRoot  string // "" when disabled
	storageKeys map[owners.Principal]string
	opts        FlashCacheOptions
}

// EnableThumbs activates the thumbs sibling cache at the given root.
// Calling this is how cfg.Storage.ThumbsCacheEnabled takes effect; when
// never called, .thumbs/* keys bypass the flash tier entirely.
func (c *FlashCache) EnableThumbs(thumbsRoot string) {
	c.thumbsRoot = thumbsRoot
}

// Adjust flashPath to route by key prefix:

func (c *FlashCache) flashPath(p owners.Principal, key string) (string, error) {
	if err := validateKey(key); err != nil {
		return "", err
	}
	sk, ok := c.storageKeys[p]
	if !ok {
		return "", fmt.Errorf("storage: unknown owner %s", p)
	}
	if err := ValidateStorageKey(sk); err != nil {
		return "", err
	}
	root := c.flashRoot
	if strings.HasPrefix(key, ".thumbs/") {
		if c.thumbsRoot == "" {
			return "", errThumbCacheDisabled
		}
		root = c.thumbsRoot
	}
	return filepath.Join(root, sk, filepath.FromSlash(key)), nil
}

// Add near the top of the file:
var errThumbCacheDisabled = errors.New("storage: thumbs flash cache disabled")
```

Also update the `populate` and `ReadRange` call sites: both currently swallow `flashPath` errors, which is already the correct behavior for `errThumbCacheDisabled` (skip caching, fall back to NAS). Confirm by re-reading the current file — no logic change needed beyond the routing in `flashPath`.

Add `"errors"` and `"strings"` to the imports if not already present.

- [ ] **Step 4: Run to verify**

Run: `go test ./internal/storage/ -v`
Expected: PASS on all storage tests including the three new thumbs-routing tests. Existing tests must not regress.

- [ ] **Step 5: Commit**

```bash
git add internal/storage/flash_cache.go internal/storage/flash_cache_test.go
git commit -m "Route .thumbs keys to a sibling flash cache root"
```

---

## Task 9: `internal/service/thumb_service.go` — app-level service

**Files:**
- Create: `internal/service/thumb_service.go`
- Create: `internal/service/thumb_service_test.go`

- [ ] **Step 1: Write the test**

Create `internal/service/thumb_service_test.go`:

```go
package service_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/errs"
	"github.com/wesm/fotobank/internal/media"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/service"
	"github.com/wesm/fotobank/internal/storage"
	"github.com/wesm/fotobank/internal/testutil"
	"github.com/wesm/fotobank/internal/thumb"
)

func TestThumbServiceGetReturnsBytesForOwnedReadyRow(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	q := thumb.NewQueue(d.WriteDB(), d.ReadDB())
	tmp := t.TempDir()
	p := owners.Principal{Hub: "h", UserID: "u"}
	_, err := d.WriteDB().ExecContext(context.Background(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		p.Hub, p.UserID, "sk", time.Now().UTC(),
	)
	r.NoError(err)
	store := storage.NewNASOnly(filepath.Join(tmp, "nas"), map[owners.Principal]string{p: "sk"})

	id := uuid.NewString()
	r.NoError(repo.Insert(context.Background(), media.Media{
		ID: id, Owner: p, Type: media.TypePhoto, MimeType: "image/jpeg",
		Path: "2024/" + id + ".jpg", ImportedAt: time.Now().UTC(),
		Size: 1, Checksum: id, ThumbStatus: "ready", ThumbVersion: 3,
	}))
	// Place thumb bytes directly at the versioned key.
	key := thumb.ThumbKey(id, 3, thumb.SizeGrid)
	_, err = store.Write(context.Background(), p, key, bytes.NewReader([]byte("thumb bytes")))
	r.NoError(err)

	svc := service.NewThumbService(repo, q, store)
	rc, _, err := svc.Get(context.Background(), id, thumb.SizeGrid, 3, p)
	r.NoError(err)
	defer func() { _ = rc.Close() }()
	bs, err := io.ReadAll(rc)
	r.NoError(err)
	r.Equal([]byte("thumb bytes"), bs)
}

func TestThumbServiceGetReturnsNotFoundOnVersionMismatch(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	q := thumb.NewQueue(d.WriteDB(), d.ReadDB())
	p := owners.Principal{Hub: "h", UserID: "u"}
	_, err := d.WriteDB().ExecContext(context.Background(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		p.Hub, p.UserID, "sk", time.Now().UTC(),
	)
	r.NoError(err)
	store := storage.NewNASOnly(t.TempDir(), map[owners.Principal]string{p: "sk"})

	id := uuid.NewString()
	r.NoError(repo.Insert(context.Background(), media.Media{
		ID: id, Owner: p, Type: media.TypePhoto, MimeType: "image/jpeg",
		Path: "x.jpg", ImportedAt: time.Now().UTC(),
		Size: 1, Checksum: id, ThumbStatus: "ready", ThumbVersion: 5,
	}))

	svc := service.NewThumbService(repo, q, store)
	_, _, err = svc.Get(context.Background(), id, thumb.SizeGrid, 4, p)
	r.ErrorIs(err, errs.ErrNotFound)
}

func TestThumbServiceGetReturnsNotFoundWhenNotReady(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	q := thumb.NewQueue(d.WriteDB(), d.ReadDB())
	p := owners.Principal{Hub: "h", UserID: "u"}
	_, err := d.WriteDB().ExecContext(context.Background(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		p.Hub, p.UserID, "sk", time.Now().UTC(),
	)
	r.NoError(err)
	store := storage.NewNASOnly(t.TempDir(), map[owners.Principal]string{p: "sk"})

	id := uuid.NewString()
	r.NoError(repo.Insert(context.Background(), media.Media{
		ID: id, Owner: p, Type: media.TypePhoto, MimeType: "image/jpeg",
		Path: "x.jpg", ImportedAt: time.Now().UTC(),
		Size: 1, Checksum: id, ThumbStatus: "pending", ThumbVersion: 0,
	}))

	svc := service.NewThumbService(repo, q, store)
	_, _, err = svc.Get(context.Background(), id, thumb.SizeGrid, 0, p)
	r.ErrorIs(err, errs.ErrNotFound)
}

func TestThumbServiceGetReturnsNotFoundForOtherOwner(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	q := thumb.NewQueue(d.WriteDB(), d.ReadDB())
	pA := owners.Principal{Hub: "h", UserID: "A"}
	pB := owners.Principal{Hub: "h", UserID: "B"}
	for _, p := range []owners.Principal{pA, pB} {
		_, err := d.WriteDB().ExecContext(context.Background(),
			`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
			p.Hub, p.UserID, "sk-"+p.UserID, time.Now().UTC(),
		)
		r.NoError(err)
	}
	store := storage.NewNASOnly(t.TempDir(), map[owners.Principal]string{
		pA: "sk-A", pB: "sk-B",
	})

	id := uuid.NewString()
	r.NoError(repo.Insert(context.Background(), media.Media{
		ID: id, Owner: pB, Type: media.TypePhoto, MimeType: "image/jpeg",
		Path: "b.jpg", ImportedAt: time.Now().UTC(),
		Size: 1, Checksum: id, ThumbStatus: "ready", ThumbVersion: 0,
	}))

	svc := service.NewThumbService(repo, q, store)
	// A requests B's thumb → not found (don't leak existence via 403).
	_, _, err := svc.Get(context.Background(), id, thumb.SizeGrid, 0, pA)
	r.True(errors.Is(err, errs.ErrNotFound))
}

func TestThumbServiceEnqueueScopesToOwner(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	q := thumb.NewQueue(d.WriteDB(), d.ReadDB())
	pA := owners.Principal{Hub: "h", UserID: "A"}
	pB := owners.Principal{Hub: "h", UserID: "B"}
	for _, p := range []owners.Principal{pA, pB} {
		_, err := d.WriteDB().ExecContext(context.Background(),
			`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
			p.Hub, p.UserID, "sk-"+p.UserID, time.Now().UTC(),
		)
		r.NoError(err)
	}
	store := storage.NewNASOnly(t.TempDir(), map[owners.Principal]string{
		pA: "sk-A", pB: "sk-B",
	})
	// Seed one row per owner.
	for _, p := range []owners.Principal{pA, pB} {
		r.NoError(repo.Insert(context.Background(), media.Media{
			ID: uuid.NewString(), Owner: p, Type: media.TypePhoto, MimeType: "image/jpeg",
			Path: "x-" + p.UserID + ".jpg", ImportedAt: time.Now().UTC(),
			Size: 1, Checksum: uuid.NewString(), ThumbStatus: "ready",
		}))
	}

	svc := service.NewThumbService(repo, q, store)
	// A calls Enqueue with All=true — only A's row should be affected.
	n, err := svc.Enqueue(context.Background(), pA, thumb.EnqueueFilter{All: true})
	r.NoError(err)
	r.Equal(1, n)
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/service/ -run TestThumbService -v`
Expected: FAIL with undefined `NewThumbService`.

- [ ] **Step 3: Implement**

Create `internal/service/thumb_service.go`:

```go
package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/wesm/fotobank/internal/errs"
	"github.com/wesm/fotobank/internal/media"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/storage"
	"github.com/wesm/fotobank/internal/thumb"
)

// ThumbService is the app-level wrapper around thumb.Queue and the
// storage layer. HTTP handlers and the CLI route through it so auth
// scoping and storage lookups live in one place.
type ThumbService struct {
	repo  *media.Repo
	queue *thumb.Queue
	store storage.Store
}

// NewThumbService constructs a ThumbService.
func NewThumbService(repo *media.Repo, q *thumb.Queue, s storage.Store) *ThumbService {
	return &ThumbService{repo: repo, queue: q, store: s}
}

// Get returns a reader for the (id, size, version) thumb for caller.
// Returns errs.ErrNotFound when:
//   - no such media row;
//   - caller does not own the row;
//   - thumb_status != 'ready';
//   - thumb_version != version (client is one generation stale).
func (s *ThumbService) Get(
	ctx context.Context,
	id string, size thumb.Size, version int,
	caller owners.Principal,
) (io.ReadCloser, media.Media, error) {
	m, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, media.Media{}, err
	}
	if m.Owner != caller {
		return nil, media.Media{}, fmt.Errorf("%w: media id=%s", errs.ErrNotFound, id)
	}
	if m.ThumbStatus != "ready" {
		return nil, media.Media{}, fmt.Errorf("%w: media id=%s not ready (%s)", errs.ErrNotFound, id, m.ThumbStatus)
	}
	if m.ThumbVersion != version {
		return nil, media.Media{}, fmt.Errorf("%w: media id=%s version mismatch (want %d have %d)", errs.ErrNotFound, id, version, m.ThumbVersion)
	}
	key := thumb.ThumbKey(id, version, size)
	rc, err := s.store.ReadRange(ctx, m.Owner, key, 0, -1)
	if err != nil {
		// Bytes should exist for ready rows; this is a storage anomaly.
		// Translate to not-found so the client retries rather than 500.
		if errors.Is(err, errs.ErrNotFound) {
			return nil, media.Media{}, err
		}
		return nil, media.Media{}, fmt.Errorf("read thumb: %w", err)
	}
	return rc, m, nil
}

// Enqueue scopes filter.Owner to caller unconditionally; the caller
// cannot regenerate someone else's rows.
func (s *ThumbService) Enqueue(
	ctx context.Context,
	caller owners.Principal,
	filter thumb.EnqueueFilter,
) (int, error) {
	filter.Owner = caller
	return s.queue.Enqueue(ctx, filter)
}

// Guard against stale imports.
var _ = time.Second
```

- [ ] **Step 4: Run to verify**

Run: `go test ./internal/service/ -run TestThumbService -v`
Expected: PASS on all five tests.

- [ ] **Step 5: Commit**

```bash
git add internal/service/thumb_service.go internal/service/thumb_service_test.go
git commit -m "Add service.ThumbService with owner-scoped Get/Enqueue"
```

---

## Task 10: HTTP `GET /api/v1/media/{id}/thumb`

**Files:**
- Create: `internal/httpapi/media_thumb.go`
- Create: `internal/httpapi/media_thumb_test.go`
- Modify: `internal/httpapi/api.go`
- Modify: `internal/httpapi/media.go` (add `thumb_version` to DTO)

- [ ] **Step 1: Modify `Deps` and wire the handler**

Add `ThumbService` to `internal/httpapi/api.go`:

```go
type Deps struct {
	IdentityProvider identity.Provider
	OwnerService     *service.OwnerService
	MediaService     *service.MediaService
	ThumbService     *service.ThumbService // NEW
}

// In buildAPI, after registerMediaOriginal:
	registerMediaThumb(mux, deps.ThumbService)
```

- [ ] **Step 2: Add `thumb_version` to `mediaDTO`**

Modify `internal/httpapi/media.go`:

```go
// In mediaDTO struct, add after Checksum:
	Checksum     string `json:"checksum"`
	ThumbStatus  string `json:"thumb_status"`
	ThumbVersion int    `json:"thumb_version"`

// In toMediaDTO, set them:
	Checksum:     m.Checksum,
	ThumbStatus:  m.ThumbStatus,
	ThumbVersion: m.ThumbVersion,
```

- [ ] **Step 3: Write the test**

Create `internal/httpapi/media_thumb_test.go`:

```go
package httpapi_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/httpapi"
	"github.com/wesm/fotobank/internal/identity"
	"github.com/wesm/fotobank/internal/media"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/service"
	"github.com/wesm/fotobank/internal/storage"
	"github.com/wesm/fotobank/internal/testutil"
	"github.com/wesm/fotobank/internal/thumb"
)

func newThumbAPITest(t *testing.T) (*httptest.Server, *media.Repo, owners.Principal, storage.Store) {
	t.Helper()
	d := testutil.OpenTestDB(t)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	q := thumb.NewQueue(d.WriteDB(), d.ReadDB())
	p := owners.Principal{Hub: "h", UserID: "u"}
	_, err := d.WriteDB().ExecContext(context.Background(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		p.Hub, p.UserID, "sk", time.Now().UTC(),
	)
	require.NoError(t, err)
	store := storage.NewNASOnly(t.TempDir(), map[owners.Principal]string{p: "sk"})
	mediaSvc := service.NewMediaService(repo, store)
	thumbSvc := service.NewThumbService(repo, q, store)
	h, err := httpapi.New(httpapi.Deps{
		IdentityProvider: identity.NewStub(p, "Test User"),
		MediaService:     mediaSvc,
		ThumbService:     thumbSvc,
	})
	require.NoError(t, err)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, repo, p, store
}

func seedReadyThumb(t *testing.T, repo *media.Repo, store storage.Store, p owners.Principal, version int) media.Media {
	t.Helper()
	id := uuid.NewString()
	m := media.Media{
		ID: id, Owner: p, Type: media.TypePhoto, MimeType: "image/jpeg",
		Path: "x-" + id + ".jpg", ImportedAt: time.Now().UTC(),
		Size: 1, Checksum: id, ThumbStatus: "ready", ThumbVersion: version,
	}
	require.NoError(t, repo.Insert(context.Background(), m))
	_, err := store.Write(context.Background(), p,
		thumb.ThumbKey(id, version, thumb.SizeGrid),
		stringReader("grid bytes"))
	require.NoError(t, err)
	return m
}

type stringReaderT string

func (s stringReaderT) Read(p []byte) (int, error) {
	n := copy(p, s)
	if n < len(s) {
		return n, nil
	}
	return n, io.EOF
}

func stringReader(s string) io.Reader { return stringReaderT(s) }

func TestThumbRouteReturnsBytesOnMatchingVersion(t *testing.T) {
	r := require.New(t)
	srv, repo, p, store := newThumbAPITest(t)
	m := seedReadyThumb(t, repo, store, p, 7)

	resp, err := http.Get(srv.URL + "/api/v1/media/" + m.ID + "/thumb?size=grid&v=7")
	r.NoError(err)
	defer func() { _ = resp.Body.Close() }()
	r.Equal(http.StatusOK, resp.StatusCode)
	r.Equal(`"`+m.ID+`-grid-v7"`, resp.Header.Get("ETag"))
	r.Contains(resp.Header.Get("Cache-Control"), "immutable")
}

func TestThumbRouteVersionMismatchReturns404(t *testing.T) {
	r := require.New(t)
	srv, repo, p, store := newThumbAPITest(t)
	m := seedReadyThumb(t, repo, store, p, 5)

	resp, err := http.Get(srv.URL + "/api/v1/media/" + m.ID + "/thumb?size=grid&v=4")
	r.NoError(err)
	defer func() { _ = resp.Body.Close() }()
	r.Equal(http.StatusNotFound, resp.StatusCode)
}

func TestThumbRouteMissingVersionReturns404(t *testing.T) {
	r := require.New(t)
	srv, repo, p, store := newThumbAPITest(t)
	m := seedReadyThumb(t, repo, store, p, 0)

	resp, err := http.Get(srv.URL + "/api/v1/media/" + m.ID + "/thumb?size=grid")
	r.NoError(err)
	defer func() { _ = resp.Body.Close() }()
	r.Equal(http.StatusNotFound, resp.StatusCode)
}

func TestThumbRouteUnknownSizeReturns400(t *testing.T) {
	r := require.New(t)
	srv, _, _, _ := newThumbAPITest(t)

	resp, err := http.Get(srv.URL + "/api/v1/media/any-id/thumb?size=huge&v=0")
	r.NoError(err)
	defer func() { _ = resp.Body.Close() }()
	r.Equal(http.StatusBadRequest, resp.StatusCode)
}

func TestListMediaDTOIncludesThumbVersion(t *testing.T) {
	r := require.New(t)
	srv, repo, p, store := newThumbAPITest(t)
	_ = seedReadyThumb(t, repo, store, p, 3)

	resp, err := http.Get(srv.URL + "/api/v1/media")
	r.NoError(err)
	defer func() { _ = resp.Body.Close() }()
	r.Equal(http.StatusOK, resp.StatusCode)
	bs, _ := io.ReadAll(resp.Body)
	r.Contains(string(bs), `"thumb_version":3`)
}
```

- [ ] **Step 4: Run to verify it fails**

Run: `go test ./internal/httpapi/ -run "Thumb" -v`
Expected: FAIL — `registerMediaThumb` undefined, `ThumbService` field on `Deps` unused.

- [ ] **Step 5: Implement the handler**

Create `internal/httpapi/media_thumb.go`:

```go
package httpapi

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/wesm/fotobank/internal/errs"
	"github.com/wesm/fotobank/internal/service"
	"github.com/wesm/fotobank/internal/thumb"
)

// registerMediaThumb wires GET /api/v1/media/{id}/thumb onto mux. See
// Plan C spec §9 for the full contract. Summary:
//   - ?size=grid|preview|lightbox (defaults to grid)
//   - ?v=<int> is REQUIRED and must match the row's thumb_version
//   - 200 with bytes when ready+matching; 404 for any other state.
//   - Strong ETag + immutable cache, permitted because URL identity
//     (via v) guarantees byte identity.
func registerMediaThumb(mux *http.ServeMux, svc *service.ThumbService) {
	if svc == nil {
		return
	}
	mux.Handle("GET /api/v1/media/{id}/thumb", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		ident, ok := IdentityFromContext(r.Context())
		if !ok {
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
			return
		}

		sizeStr := r.URL.Query().Get("size")
		if sizeStr == "" {
			sizeStr = "grid"
		}
		size, err := thumb.ParseSize(sizeStr)
		if err != nil {
			http.Error(w, "unknown size", http.StatusBadRequest)
			return
		}

		vStr := r.URL.Query().Get("v")
		if vStr == "" {
			http.Error(w, "thumb not found", http.StatusNotFound)
			return
		}
		version, err := strconv.Atoi(vStr)
		if err != nil || version < 0 {
			http.Error(w, "thumb not found", http.StatusNotFound)
			return
		}

		caller := ident.Principal.OwnersPrincipal()
		rc, m, err := svc.Get(r.Context(), id, size, version, caller)
		if err != nil {
			if errors.Is(err, errs.ErrNotFound) {
				http.Error(w, "thumb not found", http.StatusNotFound)
				return
			}
			slog.Error("thumb get", "err", err, "id", id)
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		defer func() { _ = rc.Close() }()

		etag := `"` + m.ID + "-" + string(size) + "-v" + strconv.Itoa(m.ThumbVersion) + `"`
		h := w.Header()
		h.Set("ETag", etag)
		if m.ThumbUpdatedAt != nil {
			h.Set("Last-Modified", m.ThumbUpdatedAt.UTC().Format(http.TimeFormat))
		}
		h.Set("Cache-Control", "private, max-age=31536000, immutable")
		h.Set("Content-Type", "image/webp")

		if ifNoneMatch := r.Header.Get("If-None-Match"); ifNoneMatch != "" && etagMatches(ifNoneMatch, etag) {
			w.WriteHeader(http.StatusNotModified)
			return
		}

		if _, err := io.Copy(w, rc); err != nil {
			slog.Error("thumb stream", "err", err, "id", id)
		}
	}))
}
```

- [ ] **Step 6: Wire `ThumbService` through `cli/server.go`**

This lands properly in Task 12; here, just add the field to `Deps` so the test compiles. Task 12 hooks the real service into the server.

- [ ] **Step 7: Run to verify**

Run: `go test ./internal/httpapi/ -v`
Expected: PASS on all five new Thumb tests + the DTO test. Existing media tests must not regress.

- [ ] **Step 8: Regenerate the OpenAPI spec**

Run: `make api-generate`
Expected: updates `openapi.json` to include the new route. Commit the regenerated file.

- [ ] **Step 9: Commit**

```bash
git add internal/httpapi/media_thumb.go internal/httpapi/media_thumb_test.go internal/httpapi/api.go internal/httpapi/media.go openapi.json
git commit -m "Add /api/v1/media/{id}/thumb with validated ?v= and immutable caching"
```

---

## Task 11: `fotobank thumbs regenerate`

**Files:**
- Create: `internal/cli/thumbs.go`
- Create: `internal/cli/thumbs_test.go`
- Modify: `internal/cli/root.go`

- [ ] **Step 1: Write the test**

Create `internal/cli/thumbs_test.go`:

```go
package cli_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/cli"
	"github.com/wesm/fotobank/internal/db"
	"github.com/wesm/fotobank/internal/media"
	"github.com/wesm/fotobank/internal/owners"
)

func writeBasicConfig(t *testing.T, tmp string) string {
	t.Helper()
	cfgPath := filepath.Join(tmp, "c.toml")
	require.NoError(t, os.WriteFile(cfgPath, fmt.Appendf(nil, `
[nas]
root = %q
[flash]
root = %q
[identity]
mode = "stub"
[identity.stub]
hub = "h"
user_id = "u"
`, filepath.Join(tmp, "nas"), filepath.Join(tmp, "flash")), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(tmp, "nas"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(tmp, "flash"), 0o700))
	return cfgPath
}

func seedReadyRow(t *testing.T, dbPath string) media.Media {
	t.Helper()
	d, err := db.Open(dbPath)
	require.NoError(t, err)
	defer func() { _ = d.Close() }()
	p := owners.Principal{Hub: "h", UserID: "u"}
	_, err = d.WriteDB().ExecContext(context.Background(),
		`INSERT OR IGNORE INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		p.Hub, p.UserID, "u", time.Now().UTC(),
	)
	require.NoError(t, err)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	m := media.Media{
		ID: uuid.NewString(), Owner: p, Type: media.TypePhoto, MimeType: "image/jpeg",
		Path: "x.jpg", ImportedAt: time.Now().UTC(),
		Size: 1, Checksum: uuid.NewString(), ThumbStatus: "ready", ThumbVersion: 2,
	}
	require.NoError(t, repo.Insert(context.Background(), m))
	return m
}

func TestThumbsRegenerateAllBumpsVersion(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeBasicConfig(t, tmp)
	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_CONFIG", cfgPath)
	t.Setenv("FOTOBANK_DB_PATH", dbPath)

	// First invocation triggers migrations.
	var out, eout bytes.Buffer
	r.Equal(0, cli.RunContext(context.Background(),
		[]string{"owners", "list", "--config", cfgPath}, &out, &eout))
	m := seedReadyRow(t, dbPath)

	out.Reset()
	eout.Reset()
	code := cli.RunContext(context.Background(),
		[]string{"thumbs", "regenerate", "--all", "--config", cfgPath}, &out, &eout)
	r.Equal(0, code)
	r.Contains(out.String(), "1 rows enqueued")

	// Confirm version bumped.
	d, err := db.Open(dbPath)
	r.NoError(err)
	defer func() { _ = d.Close() }()
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	got, err := repo.GetByID(context.Background(), m.ID)
	r.NoError(err)
	r.Equal(3, got.ThumbVersion)
	r.Equal("pending", got.ThumbStatus)
}

func TestThumbsRegenerateWithNoSelectorErrors(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeBasicConfig(t, tmp)
	t.Setenv("FOTOBANK_CONFIG", cfgPath)
	t.Setenv("FOTOBANK_DB_PATH", filepath.Join(tmp, "fotobank.sqlite"))

	var out, eout bytes.Buffer
	code := cli.RunContext(context.Background(),
		[]string{"thumbs", "regenerate", "--config", cfgPath}, &out, &eout)
	r.Equal(2, code)
	r.Contains(eout.String(), "at least one")
}

func TestThumbsRegenerateByIDTargetsOnlyMatch(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeBasicConfig(t, tmp)
	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_CONFIG", cfgPath)
	t.Setenv("FOTOBANK_DB_PATH", dbPath)

	var out, eout bytes.Buffer
	_ = cli.RunContext(context.Background(),
		[]string{"owners", "list", "--config", cfgPath}, &out, &eout)

	m1 := seedReadyRow(t, dbPath)
	m2 := seedReadyRow(t, dbPath)

	out.Reset()
	eout.Reset()
	code := cli.RunContext(context.Background(),
		[]string{"thumbs", "regenerate", "--id", m1.ID, "--config", cfgPath}, &out, &eout)
	r.Equal(0, code)
	r.Contains(out.String(), "1 rows enqueued")

	d, err := db.Open(dbPath)
	r.NoError(err)
	defer func() { _ = d.Close() }()
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	got1, err := repo.GetByID(context.Background(), m1.ID)
	r.NoError(err)
	r.Equal("pending", got1.ThumbStatus)

	got2, err := repo.GetByID(context.Background(), m2.ID)
	r.NoError(err)
	r.Equal("ready", got2.ThumbStatus)
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/cli/ -run TestThumbsRegenerate -v`
Expected: FAIL — `thumbs` subcommand unknown (unknown command).

- [ ] **Step 3: Implement**

Create `internal/cli/thumbs.go`:

```go
package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/wesm/fotobank/internal/config"
	"github.com/wesm/fotobank/internal/db"
	"github.com/wesm/fotobank/internal/media"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/thumb"
)

func newThumbsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "thumbs",
		Short: "Manage thumbnail generation",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			_ = cmd.Usage()
			return newUsageError("a subcommand is required")
		},
	}
	cmd.AddCommand(newThumbsRegenerateCmd())
	return cmd
}

type regenerateOpts struct {
	cfgPath string
	all     bool
	ids     []string
	kind    string
	status  string
	since   string
}

func newThumbsRegenerateCmd() *cobra.Command {
	var opts regenerateOpts
	cmd := &cobra.Command{
		Use:   "regenerate",
		Short: "Enqueue media rows for thumbnail regeneration",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runThumbsRegenerate(cmd.Context(), opts, cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	cmd.Flags().StringVar(&opts.cfgPath, "config", "", "path to config file (defaults to DefaultConfigPath)")
	cmd.Flags().BoolVar(&opts.all, "all", false, "regenerate every row for the caller")
	cmd.Flags().StringSliceVar(&opts.ids, "id", nil, "regenerate specific media IDs (repeatable)")
	cmd.Flags().StringVar(&opts.kind, "type", "", "filter: 'photo' or 'video'")
	cmd.Flags().StringVar(&opts.status, "status", "", "filter by current thumb_status (pending/working/ready/failed/no_preview)")
	cmd.Flags().StringVar(&opts.since, "since", "", "filter: imported_at >= RFC3339 date")
	return cmd
}

func runThumbsRegenerate(ctx context.Context, opts regenerateOpts, stdout, _ io.Writer) error {
	path := opts.cfgPath
	if path == "" {
		path = config.DefaultConfigPath()
	}
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	dbPath := os.Getenv("FOTOBANK_DB_PATH")
	if dbPath == "" {
		dbPath = filepath.Join(cfg.Flash.Root, "fotobank.sqlite")
	}
	d, err := db.Open(dbPath)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()

	filter := thumb.EnqueueFilter{
		Owner: owners.Principal{Hub: cfg.Identity.Stub.Hub, UserID: cfg.Identity.Stub.UserID},
		All:   opts.all,
		IDs:   opts.ids,
	}
	if opts.kind != "" {
		filter.MediaType = media.Type(opts.kind)
	}
	if opts.status != "" {
		filter.Status = opts.status
	}
	if opts.since != "" {
		ts, err := time.Parse(time.RFC3339, opts.since)
		if err != nil {
			return newUsageError("--since must be RFC3339: %v", err)
		}
		filter.Since = &ts
	}

	if !opts.all && len(opts.ids) == 0 && opts.kind == "" && opts.status == "" && opts.since == "" {
		return newUsageError("at least one of --all, --id, --type, --status, --since is required")
	}

	q := thumb.NewQueue(d.WriteDB(), d.ReadDB())
	n, err := q.Enqueue(ctx, filter)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%d rows enqueued for regeneration.\n", n)
	return nil
}
```

Register the command by editing `internal/cli/root.go`:

```go
// In newRootCmd, after the existing AddCommand calls:
	root.AddCommand(newThumbsCmd())
```

- [ ] **Step 4: Run to verify**

Run: `go test ./internal/cli/ -run TestThumbsRegenerate -v`
Expected: PASS on all three tests.

- [ ] **Step 5: Commit**

```bash
git add internal/cli/thumbs.go internal/cli/thumbs_test.go internal/cli/root.go
git commit -m "Add fotobank thumbs regenerate CLI command"
```

---

## Task 12: Wire the Worker into `fotobank server`

**Files:**
- Modify: `internal/cli/server.go`

- [ ] **Step 1: Write the test**

Extend `internal/cli/server_test.go`. Add a test after `TestServerRespondsToHealthz`:

```go
func TestServerDrainsPendingThumbRow(t *testing.T) {
	// Smoke test: seed a ready JPEG row pre-import, boot the server,
	// poll until thumb_status becomes 'ready' (worker has drained it).
	// Uses the existing photo-with-timestamp fixture as the source.
	r := require.New(t)
	tmp := t.TempDir()
	nasRoot := filepath.Join(tmp, "nas")
	r.NoError(os.MkdirAll(filepath.Join(nasRoot, "u", "2024"), 0o700))
	// Place a source JPEG where NASOnly expects it.
	fixture, err := os.ReadFile(filepath.Join("..", "..", "testdata", "exif", "photo-with-timestamp.jpg"))
	r.NoError(err)
	srcPath := filepath.Join(nasRoot, "u", "2024", "a.jpg")
	r.NoError(os.WriteFile(srcPath, fixture, 0o600))

	cfgPath := filepath.Join(tmp, "c.toml")
	r.NoError(os.WriteFile(cfgPath, fmt.Appendf(nil, `
[nas]
root = %q
[flash]
root = %q
[identity]
mode = "stub"
[identity.stub]
hub = "h"
user_id = "u"
[http]
listen_address = "127.0.0.1:0"
[thumbs]
poll_interval = "20ms"
worker_concurrency = 1
`, nasRoot, filepath.Join(tmp, "flash")), 0o600))

	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_CONFIG", cfgPath)
	t.Setenv("FOTOBANK_DB_PATH", dbPath)

	// Migrate the DB by opening+closing once, then insert a pending row.
	d, err := db.Open(dbPath)
	r.NoError(err)
	p := owners.Principal{Hub: "h", UserID: "u"}
	_, err = d.WriteDB().ExecContext(context.Background(),
		`INSERT OR IGNORE INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		p.Hub, p.UserID, "u", time.Now().UTC(),
	)
	r.NoError(err)
	mediaID := uuid.NewString()
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	r.NoError(repo.Insert(context.Background(), media.Media{
		ID: mediaID, Owner: p, Type: media.TypePhoto, MimeType: "image/jpeg",
		Path: "2024/a.jpg", ImportedAt: time.Now().UTC(),
		Size: int64(len(fixture)), Checksum: mediaID, ThumbStatus: "pending",
	}))
	_ = d.Close()

	addrFile := filepath.Join(tmp, "addr")
	t.Setenv("FOTOBANK_TEST_LISTEN_ADDR_SINK", addrFile)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan int, 1)
	go func() {
		var out, eout bytes.Buffer
		errCh <- cli.RunContext(ctx, []string{"server", "--config", cfgPath}, &out, &eout)
	}()

	// Wait for boot.
	for range 100 {
		if b, err := os.ReadFile(addrFile); err == nil && len(b) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Poll DB until the worker drains the row.
	deadline := time.Now().Add(10 * time.Second)
	d2, err := db.Open(dbPath)
	r.NoError(err)
	repo2 := media.NewRepo(d2.WriteDB(), d2.ReadDB())
	for time.Now().Before(deadline) {
		got, err := repo2.GetByID(context.Background(), mediaID)
		r.NoError(err)
		if got.ThumbStatus == "ready" {
			_ = d2.Close()
			cancel()
			<-errCh
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = d2.Close()
	cancel()
	<-errCh
	t.Fatal("worker did not drain pending row within 10s")
}
```

Add missing imports at the top of `server_test.go`: `"github.com/google/uuid"`, `"github.com/wesm/fotobank/internal/db"`, `"github.com/wesm/fotobank/internal/media"`, `"github.com/wesm/fotobank/internal/owners"`.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/cli/ -run TestServerDrainsPendingThumbRow -v`
Expected: FAIL — worker never runs; row stays `pending`.

- [ ] **Step 3: Wire the worker in `runServer`**

Modify `internal/cli/server.go`. After `mediaSvc` is constructed and before `handler := httpapi.New(...)`:

```go
	thumbQueue := thumb.NewQueue(d.WriteDB(), d.ReadDB())
	thumbSvc := service.NewThumbService(
		media.NewRepo(d.WriteDB(), d.ReadDB()),
		thumbQueue,
		storeLayer,
	)

	handler, err := httpapi.New(httpapi.Deps{
		IdentityProvider: idp,
		OwnerService:     ownerSvc,
		MediaService:     mediaSvc,
		ThumbService:     thumbSvc,
	})
```

After the flash janitor goroutine is spawned:

```go
	thumbWorker := thumb.NewWorker(thumbQueue, storeLayer, thumb.Config{
		WorkerConcurrency: cfg.Thumbs.WorkerConcurrency,
		PollInterval:      cfg.Thumbs.PollInterval,
		LeaseTimeout:      cfg.Thumbs.LeaseTimeout,
	})
	go func() {
		if err := thumbWorker.Run(sigCtx); err != nil && !errors.Is(err, context.Canceled) {
			fmt.Fprintln(opts.stderr, "thumb worker exited:", err)
		}
	}()
```

Add `"github.com/wesm/fotobank/internal/thumb"` to the imports if not already present.

Also, if flash_cache mode is active AND `cfg.Storage.ThumbsCacheEnabled`, call `flashCache.EnableThumbs`:

```go
// Inside buildStorageLayer, after constructing fc:
	if cfg.Storage.ThumbsCacheEnabled {
		thumbsCacheRoot := filepath.Join(cfg.Flash.Root, "thumbs")
		fc.EnableThumbs(thumbsCacheRoot)
	}
```

- [ ] **Step 4: Run to verify**

Run: `go test ./internal/cli/ -run TestServerDrainsPendingThumbRow -v`
Expected: PASS. Also run the full CLI test suite: `go test ./internal/cli/ -v` — no regressions.

- [ ] **Step 5: Commit**

```bash
git add internal/cli/server.go internal/cli/server_test.go
git commit -m "Start thumb worker alongside the HTTP server and flash janitor"
```

---

## Task 13: Add HTTP `thumb_version` to `listMediaResponse` decoder

**Files:**
- Modify: `internal/httpapi/media_test.go` (decoder struct needs the new field to avoid silent drops)

- [ ] **Step 1: Update the test decoder**

In `internal/httpapi/media_test.go`, extend `listMediaResponse.Items` to include `thumb_version`:

```go
type listMediaResponse struct {
	Items []struct {
		ID           string `json:"id"`
		Type         string `json:"type"`
		Path         string `json:"path"`
		Checksum     string `json:"checksum"`
		ThumbStatus  string `json:"thumb_status"`
		ThumbVersion int    `json:"thumb_version"`
	} `json:"items"`
	NextOffset *int `json:"next_offset"`
	Total      *int `json:"total"`
}
```

No new tests — the existing list tests now decode and ignore the new fields. This task exists to keep the decoder honest; skipping it will cause silent drift in the test helper.

- [ ] **Step 2: Run**

Run: `go test ./internal/httpapi/ -v`
Expected: PASS unchanged.

- [ ] **Step 3: Commit**

```bash
git add internal/httpapi/media_test.go
git commit -m "Include thumb_version in list-media test decoder"
```

---

## Task 14: End-to-end test — import → drain → serve → regenerate

**Files:**
- Modify: `internal/e2e/pipeline_test.go` (or wherever Plan B put `TestE2EMediaPipeline`) — if the file does not exist at that path, find it via `grep -r "TestE2EMediaPipeline" internal/`

- [ ] **Step 1: Extend the existing pipeline test**

Find the existing E2E test:

```bash
grep -rn "TestE2EMediaPipeline" /path/to/fotobank/internal/ | head -3
```

Add a follow-on scenario after the existing body:

```go
// After the existing assertions that /original streams correctly:

// Poll until the worker drains the imported row (thumb_status=ready).
var readyID string
var thumbV int
deadline := time.Now().Add(15 * time.Second)
for time.Now().Before(deadline) {
	body := doListMedia(t, srvURL) // whatever helper the test uses
	if len(body.Items) > 0 && body.Items[0].ThumbStatus == "ready" {
		readyID = body.Items[0].ID
		thumbV = body.Items[0].ThumbVersion
		break
	}
	time.Sleep(100 * time.Millisecond)
}
require.NotEmpty(t, readyID, "worker did not drain imported row within 15s")

// Fetch the grid thumb at the current version.
resp, err := http.Get(srvURL + "/api/v1/media/" + readyID + "/thumb?size=grid&v=" + strconv.Itoa(thumbV))
require.NoError(t, err)
defer func() { _ = resp.Body.Close() }()
require.Equal(t, http.StatusOK, resp.StatusCode)

// Regenerate; version bumps; old URL returns 404, new URL eventually 200.
{
	var out, eout bytes.Buffer
	code := cli.RunContext(context.Background(),
		[]string{"thumbs", "regenerate", "--all", "--config", cfgPath}, &out, &eout)
	require.Equal(t, 0, code, "stderr=%s", eout.String())
}

// Old URL: 404 immediately.
resp2, err := http.Get(srvURL + "/api/v1/media/" + readyID + "/thumb?size=grid&v=" + strconv.Itoa(thumbV))
require.NoError(t, err)
_ = resp2.Body.Close()
require.Equal(t, http.StatusNotFound, resp2.StatusCode)

// New URL: wait for worker to rebuild at v+1.
deadline2 := time.Now().Add(15 * time.Second)
for time.Now().Before(deadline2) {
	resp3, err := http.Get(srvURL + "/api/v1/media/" + readyID + "/thumb?size=grid&v=" + strconv.Itoa(thumbV+1))
	require.NoError(t, err)
	code := resp3.StatusCode
	_ = resp3.Body.Close()
	if code == http.StatusOK {
		return
	}
	time.Sleep(100 * time.Millisecond)
}
t.Fatal("new thumb version never became ready")
```

Adapt variable names (`srvURL`, `cfgPath`, `doListMedia`) to whatever the existing E2E helper function uses. Add any missing imports (`"strconv"`, `cli`, `bytes`, etc.).

- [ ] **Step 2: Run**

Run: `go test ./internal/e2e/ -run TestE2EMediaPipeline -v`
Expected: PASS.

- [ ] **Step 3: Run the full suite**

```bash
make vet test lint
```

Expected: all green.

- [ ] **Step 4: Commit**

```bash
git add internal/e2e/pipeline_test.go
git commit -m "Extend E2E test to cover thumbnail drain + regenerate round-trip"
```

---

## Self-review notes (inline fixes applied during writing)

- **Spec §7.1 versioned keys** — Task 2 (`ThumbKey`), Task 7 (write path), Task 9/10 (read path) all use `ThumbKey(id, version, size)`. Consistent.
- **Spec §5.1 claim fencing** — Task 6's `Queue` returns `Claim{Media, ClaimedAt}`; Task 7's `processOne` threads the token through `Mark*`. Test coverage: `TestMarkReadyReturnsErrClaimLostOnStaleToken`, `TestSweepLeasesBumpsVersionAndResetsClaim`, `TestRegenerateWhileWorkingLosesClaim`, `TestWorkerStaleWriteDoesNotCorruptReclaim`.
- **Spec §8 flash-cache wiring** — Task 8 adds `EnableThumbs`; Task 12 calls it in `buildStorageLayer` when `cfg.Storage.ThumbsCacheEnabled`. Task 8's janitor test guards the sibling-state regression.
- **Spec §9 HTTP semantics** — Task 10 enforces `?v=` validation, 404 on any not-ready / mismatch, strong ETag, `immutable` cache.
- **Spec §10 CLI** — Task 11 implements `--all/--id/--type/--status/--since/--dry-run` (dry-run not explicitly listed in the CLI code block above — add a `--dry-run` flag that, when set, runs a `SELECT COUNT(*)` with the same WHERE clause and prints the result without executing the UPDATE. If time-constrained, defer dry-run to a follow-up; the rest of the command is complete without it).
- **WebP encoder decision gate** — Task 1 as the first task, with explicit escalation if the probe fails.
- **Videos and HEIC** — `processOne` short-circuits both to `no_preview`; `TestWorkerSkipsVideoAsNoPreview` guards.

**Known rough edges the implementer should smooth during integration:**

1. The dsoprea API surface in Task 5 — actual package names / function signatures may differ from `GetFlatExifDataUniversalSearch` / `Collect`. Run `go doc github.com/dsoprea/go-exif/v3` to see current symbols and adjust.
2. The `--dry-run` flag for Task 11 is mentioned in the spec §10 but not implemented in the sample code above; add it as a final step in Task 11 before committing.
3. Task 7 has an `imageSource` interface sketch that exists only to flag the copy-paste awkwardness — the note immediately below explains the cleaner direct `image.Image` return type.

---

Plan complete and saved to `docs/superpowers/plans/2026-04-22-fotobank-plan-c-thumbnails.md`.

Two execution options:

**1. Subagent-Driven (recommended)** — fresh subagent per task, two-stage review (spec compliance + code quality) between tasks, fast iteration. Established pattern from Plan B.

**2. Inline Execution** — execute tasks in this session with checkpoints for review.

Per the `/roborev-fix every 5 tasks` convention from Plan B, subagent-driven execution with an Opus implementer is the established rhythm for this project. Proceed with that?
