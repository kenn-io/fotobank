# Plan B — Import pipeline + read-only HTTP Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Each task is bite-sized and self-contained. Commit directly to master (no feature branches or worktrees for fotobank). After every 5 completed tasks, invoke `/roborev-fix` to clear the review queue.

**Goal:** Deliver a fully working import → query → stream pipeline for photos and videos, behind the existing stub identity. After Plan B, `fotobank import -D root/ src/` lands bytes under `{nas.root}/{storage_key}/…` with `media` rows inserted, and `/api/v1/media` + `/api/v1/media/{id}/original` serve them. `fotobank reconcile` surfaces drift between NAS and DB.

**Architecture:** Four new packages — `internal/storage` (Store interface + NASOnly + FlashCache), `internal/exifread` (photo + video metadata), `internal/media` (repo + types), `internal/ingest` + `internal/reconcile` (pipelines) — plus a new `service.MediaService`. The Plan A `httpapi` package grows three new routes; `cli` package grows two new commands. Thumbnails (Plan C) and shares/albums (Plan D) remain out of scope; media rows still carry the `thumb_status='pending'` seed so Plan C picks them up without migration work.

**Tech Stack:** Go 1.26.0; `dsoprea/go-exif/v3` for EXIF; `abema/go-mp4` for MP4/MOV container metadata; `gofrs/flock` for the import lock; existing huma/cobra/modernc.org/sqlite/migrate stack.

**Deferred to later plans:**
- Thumbnails (§§9.6, 11, `/thumb`) → Plan C
- Albums, shares, broker (§§9.3–9.5, 12.3 write routes) → Plan D
- Multi-owner visibility (`resolveVisibleMedia` for grantees) → Plan D

---

## Part 1 — Storage layer (§7)

### Task 1: `internal/storage` package scaffold

**Files:**
- Create: `internal/storage/storage.go`
- Create: `internal/storage/storage_test.go`

- [ ] **Step 1: Write the types and sentinel errors**

```go
// internal/storage/storage.go
package storage

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/wesm/fotobank/internal/owners"
)

// Tier indicates which backing tier served a read or the logical
// location of a key.
type Tier string

const (
	TierFlash Tier = "flash"
	TierNAS   Tier = "nas"
)

// StoreInfo describes a stored object.
type StoreInfo struct {
	Size    int64
	ModTime time.Time
	Tier    Tier
}

// ErrPathOccupied indicates that the no-clobber finalize step saw an
// existing file at the final path. Callers decide whether to retry
// with a bumped sequence number (photo path) or adopt the orphan
// (content-addressed video path).
var ErrPathOccupied = errors.New("storage: path already occupied")

// Store persists media bytes. Implementations must be safe for
// concurrent use by multiple goroutines within a single process; the
// import pipeline additionally serialises multiprocess access via a
// file lock (see internal/ingest).
type Store interface {
	Stat(ctx context.Context, owner owners.Principal, key string) (StoreInfo, error)
	ReadRange(ctx context.Context, owner owners.Principal, key string, offset, length int64) (io.ReadCloser, error)
	Write(ctx context.Context, owner owners.Principal, key string, src io.Reader) (string, error)
	Delete(ctx context.Context, owner owners.Principal, key string) error
}
```

- [ ] **Step 2: Commit (scaffold only; tests land with backends)**

```bash
git add internal/storage/
git commit -m "Scaffold internal/storage with Store interface and sentinel errors"
```

---

### Task 2: NAS-only backend with no-clobber finalize

**Files:**
- Create: `internal/storage/nas_only.go`
- Create: `internal/storage/nas_only_test.go`

- [ ] **Step 1: Write failing tests (Write, ReadRange, Stat, Delete, collision)**

```go
// internal/storage/nas_only_test.go
package storage_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/storage"
)

func newNASStore(t *testing.T) (*storage.NASOnly, string, owners.Principal) {
	t.Helper()
	root := t.TempDir()
	p := owners.Principal{Hub: "h", UserID: "u"}
	return storage.NewNASOnly(root, storageKeyFor(p, "key1")), root, p
}

// storageKeyFor resolves a (principal -> storage_key) lookup in tests.
// NASOnly takes a static map so tests can stub without the full owners repo.
func storageKeyFor(p owners.Principal, key string) map[owners.Principal]string {
	return map[owners.Principal]string{p: key}
}

func TestNASOnlyWriteThenRead(t *testing.T) {
	r := require.New(t)
	s, root, p := newNASStore(t)

	_, err := s.Write(context.Background(), p, "2024/a.jpg", bytes.NewReader([]byte("hello")))
	r.NoError(err)

	// File landed at {root}/{storage_key}/2024/a.jpg.
	b, err := os.ReadFile(filepath.Join(root, "key1", "2024", "a.jpg"))
	r.NoError(err)
	r.Equal("hello", string(b))

	rc, err := s.ReadRange(context.Background(), p, "2024/a.jpg", 0, -1)
	r.NoError(err)
	defer rc.Close()
	got, err := io.ReadAll(rc)
	r.NoError(err)
	r.Equal("hello", string(got))
}

func TestNASOnlyReadRange(t *testing.T) {
	r := require.New(t)
	s, _, p := newNASStore(t)
	_, err := s.Write(context.Background(), p, "f.bin", bytes.NewReader([]byte("0123456789")))
	r.NoError(err)

	rc, err := s.ReadRange(context.Background(), p, "f.bin", 3, 4)
	r.NoError(err)
	defer rc.Close()
	got, err := io.ReadAll(rc)
	r.NoError(err)
	r.Equal("3456", string(got))
}

func TestNASOnlyWriteNoClobberReturnsErrPathOccupied(t *testing.T) {
	r := require.New(t)
	s, _, p := newNASStore(t)
	_, err := s.Write(context.Background(), p, "f.jpg", bytes.NewReader([]byte("first")))
	r.NoError(err)

	_, err = s.Write(context.Background(), p, "f.jpg", bytes.NewReader([]byte("second")))
	r.ErrorIs(err, storage.ErrPathOccupied)

	// First bytes are unchanged.
	rc, err := s.ReadRange(context.Background(), p, "f.jpg", 0, -1)
	r.NoError(err)
	defer rc.Close()
	got, err := io.ReadAll(rc)
	r.NoError(err)
	r.Equal("first", string(got))
}

func TestNASOnlyStat(t *testing.T) {
	r := require.New(t)
	s, _, p := newNASStore(t)
	_, err := s.Write(context.Background(), p, "s.bin", bytes.NewReader([]byte("abc")))
	r.NoError(err)

	info, err := s.Stat(context.Background(), p, "s.bin")
	r.NoError(err)
	r.Equal(int64(3), info.Size)
	r.Equal(storage.TierNAS, info.Tier)
}

func TestNASOnlyDeleteIsIdempotent(t *testing.T) {
	r := require.New(t)
	s, _, p := newNASStore(t)
	_, err := s.Write(context.Background(), p, "d.bin", bytes.NewReader([]byte("x")))
	r.NoError(err)
	r.NoError(s.Delete(context.Background(), p, "d.bin"))
	r.NoError(s.Delete(context.Background(), p, "d.bin")) // idempotent
	_, err = s.Stat(context.Background(), p, "d.bin")
	r.True(errors.Is(err, os.ErrNotExist))
}
```

- [ ] **Step 2: Run, verify fail**

- [ ] **Step 3: Implement NAS-only backend**

```go
// internal/storage/nas_only.go
package storage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/wesm/fotobank/internal/owners"
)

// NASOnly is a Store backed by a single local filesystem root. Writes
// use a no-clobber finalize (tmp file + os.Link) so concurrent workers
// cannot silently overwrite each other's bytes.
type NASOnly struct {
	root        string
	storageKeys map[owners.Principal]string
}

// NewNASOnly constructs a NASOnly Store rooted at root. storageKeys maps
// known principals to their on-disk subdirectory (owners.storage_key).
// Unknown principals are rejected at request time — callers must
// re-initialise when a new owner is added.
func NewNASOnly(root string, storageKeys map[owners.Principal]string) *NASOnly {
	return &NASOnly{root: root, storageKeys: storageKeys}
}

func (s *NASOnly) ownerPath(p owners.Principal, key string) (string, error) {
	sk, ok := s.storageKeys[p]
	if !ok {
		return "", fmt.Errorf("storage: unknown owner %s", p)
	}
	return filepath.Join(s.root, sk, filepath.FromSlash(key)), nil
}

func (s *NASOnly) Stat(_ context.Context, p owners.Principal, key string) (StoreInfo, error) {
	full, err := s.ownerPath(p, key)
	if err != nil {
		return StoreInfo{}, err
	}
	fi, err := os.Stat(full)
	if err != nil {
		return StoreInfo{}, err
	}
	return StoreInfo{Size: fi.Size(), ModTime: fi.ModTime(), Tier: TierNAS}, nil
}

func (s *NASOnly) ReadRange(_ context.Context, p owners.Principal, key string, offset, length int64) (io.ReadCloser, error) {
	full, err := s.ownerPath(p, key)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(full)
	if err != nil {
		return nil, err
	}
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			_ = f.Close()
			return nil, err
		}
	}
	if length < 0 {
		return f, nil
	}
	return &limitedReadCloser{R: io.LimitReader(f, length), C: f}, nil
}

type limitedReadCloser struct {
	R io.Reader
	C io.Closer
}

func (lrc *limitedReadCloser) Read(p []byte) (int, error) { return lrc.R.Read(p) }
func (lrc *limitedReadCloser) Close() error               { return lrc.C.Close() }

func (s *NASOnly) Write(_ context.Context, p owners.Principal, key string, src io.Reader) (string, error) {
	full, err := s.ownerPath(p, key)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		return "", fmt.Errorf("storage: mkdir: %w", err)
	}
	tmp := full + tmpSuffix()
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("storage: open tmp: %w", err)
	}
	if _, err := io.Copy(f, src); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return "", fmt.Errorf("storage: write tmp: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return "", fmt.Errorf("storage: sync tmp: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("storage: close tmp: %w", err)
	}
	if err := os.Link(tmp, full); err != nil {
		_ = os.Remove(tmp)
		if errors.Is(err, os.ErrExist) {
			return "", ErrPathOccupied
		}
		return "", fmt.Errorf("storage: link: %w", err)
	}
	_ = os.Remove(tmp)
	return key, nil
}

func (s *NASOnly) Delete(_ context.Context, p owners.Principal, key string) error {
	full, err := s.ownerPath(p, key)
	if err != nil {
		return err
	}
	if err := os.Remove(full); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func tmpSuffix() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf(".tmp-%d-%d-%s", os.Getpid(), time.Now().UnixNano(), hex.EncodeToString(b[:]))
}
```

- [ ] **Step 4: Run, verify pass**

- [ ] **Step 5: Commit**

```bash
git add internal/storage/
git commit -m "Add NASOnly Store with no-clobber finalize"
```

---

### Task 3: Concurrent-write collision test

**Files:**
- Modify: `internal/storage/nas_only_test.go`

- [ ] **Step 1: Write the test**

```go
func TestNASOnlyConcurrentWriteResolvesToSingleWinner(t *testing.T) {
	// Two goroutines racing on the same canonical path. Exactly one
	// should win with a nil error; the other must see ErrPathOccupied.
	// This is the key invariant that makes the import pipeline safe.
	r := require.New(t)
	s, _, p := newNASStore(t)

	type result struct {
		err error
	}
	results := make(chan result, 2)
	for i := 0; i < 2; i++ {
		i := i
		go func() {
			_, err := s.Write(context.Background(), p, "race.bin",
				bytes.NewReader([]byte(fmt.Sprintf("payload-%d", i))))
			results <- result{err: err}
		}()
	}
	var okCount, occCount int
	for i := 0; i < 2; i++ {
		res := <-results
		switch {
		case res.err == nil:
			okCount++
		case errors.Is(res.err, storage.ErrPathOccupied):
			occCount++
		default:
			r.Fail("unexpected error", res.err.Error())
		}
	}
	r.Equal(1, okCount)
	r.Equal(1, occCount)
}
```

Add `"fmt"` import to the test file.

- [ ] **Step 2: Run, verify pass (impl from Task 2 already handles this)**

- [ ] **Step 3: Commit**

```bash
git add internal/storage/nas_only_test.go
git commit -m "Test concurrent write resolves to a single winner"
```

---

### Task 4: Flash cache backend

**Files:**
- Create: `internal/storage/flash_cache.go`
- Create: `internal/storage/flash_cache_test.go`

- [ ] **Step 1: Write failing tests (read-through, populate-on-miss, janitor eviction)**

```go
// internal/storage/flash_cache_test.go
package storage_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/storage"
)

func newFlashCache(t *testing.T) (*storage.FlashCache, string, string, owners.Principal) {
	t.Helper()
	nasRoot := t.TempDir()
	flashRoot := t.TempDir()
	p := owners.Principal{Hub: "h", UserID: "u"}
	keys := map[owners.Principal]string{p: "key1"}
	nas := storage.NewNASOnly(nasRoot, keys)
	fc := storage.NewFlashCache(nas, flashRoot, keys, storage.FlashCacheOptions{
		OriginalsCacheDays:     7,
		OriginalsCacheMaxMedia: 100,
	})
	return fc, nasRoot, flashRoot, p
}

func TestFlashCacheWriteGoesThroughToNAS(t *testing.T) {
	r := require.New(t)
	fc, nasRoot, _, p := newFlashCache(t)
	_, err := fc.Write(context.Background(), p, "a.jpg", bytes.NewReader([]byte("bytes")))
	r.NoError(err)

	// NAS has the bytes.
	nasBytes, err := os.ReadFile(filepath.Join(nasRoot, "key1", "a.jpg"))
	r.NoError(err)
	r.Equal("bytes", string(nasBytes))
}

func TestFlashCacheReadPopulatesFlashOnMiss(t *testing.T) {
	r := require.New(t)
	fc, _, flashRoot, p := newFlashCache(t)
	_, err := fc.Write(context.Background(), p, "a.jpg", bytes.NewReader([]byte("bytes")))
	r.NoError(err)

	// Read via FlashCache; on miss it should copy NAS → flash
	// synchronously by the time the read returns (test-mode
	// flag on the cache disables the async background copy).
	rc, err := fc.ReadRange(context.Background(), p, "a.jpg", 0, -1)
	r.NoError(err)
	got, err := io.ReadAll(rc)
	r.NoError(rc.Close())
	r.NoError(err)
	r.Equal("bytes", string(got))

	// Give the populate goroutine a moment in case it runs async;
	// poll for the flash file to appear (bounded wait).
	flashPath := filepath.Join(flashRoot, "key1", "a.jpg")
	for i := 0; i < 50; i++ {
		if _, err := os.Stat(flashPath); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	r.Fail("flash cache did not populate", flashPath)
}

func TestFlashCacheJanitorEvictsByAge(t *testing.T) {
	r := require.New(t)
	fc, _, flashRoot, p := newFlashCache(t)
	_, err := fc.Write(context.Background(), p, "old.bin", bytes.NewReader([]byte("old")))
	r.NoError(err)
	// Populate flash via a read.
	_, err = fc.ReadRange(context.Background(), p, "old.bin", 0, -1)
	r.NoError(err)

	// Back-date the flash file to look old.
	flashPath := filepath.Join(flashRoot, "key1", "old.bin")
	// Poll until the populate finishes.
	for i := 0; i < 50; i++ {
		if _, err := os.Stat(flashPath); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	old := time.Now().Add(-10 * 24 * time.Hour)
	r.NoError(os.Chtimes(flashPath, old, old))

	// Run the janitor directly (not on a timer).
	r.NoError(fc.Evict(context.Background()))

	_, err = os.Stat(flashPath)
	r.True(os.IsNotExist(err))
}
```

- [ ] **Step 2: Implement FlashCache**

```go
// internal/storage/flash_cache.go
package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/wesm/fotobank/internal/owners"
)

// FlashCacheOptions tunes the recency janitor.
type FlashCacheOptions struct {
	OriginalsCacheDays     int
	OriginalsCacheMaxMedia int
}

// FlashCache wraps a NAS-backed Store with a flash-tier mirror. Writes
// go through to the wrapped Store then populate flash best-effort;
// reads hit flash first and fall back to the wrapped Store, populating
// flash on the way back.
type FlashCache struct {
	nas         Store
	flashRoot   string
	storageKeys map[owners.Principal]string
	opts        FlashCacheOptions
}

// NewFlashCache builds a FlashCache. storageKeys mirrors the map given
// to the wrapped Store so the cache can compute its own file paths.
func NewFlashCache(nas Store, flashRoot string, storageKeys map[owners.Principal]string, opts FlashCacheOptions) *FlashCache {
	return &FlashCache{nas: nas, flashRoot: flashRoot, storageKeys: storageKeys, opts: opts}
}

func (c *FlashCache) flashPath(p owners.Principal, key string) (string, error) {
	sk, ok := c.storageKeys[p]
	if !ok {
		return "", fmt.Errorf("storage: unknown owner %s", p)
	}
	return filepath.Join(c.flashRoot, sk, filepath.FromSlash(key)), nil
}

func (c *FlashCache) Stat(ctx context.Context, p owners.Principal, key string) (StoreInfo, error) {
	// Authoritative metadata is on NAS.
	info, err := c.nas.Stat(ctx, p, key)
	if err != nil {
		return StoreInfo{}, err
	}
	flash, perr := c.flashPath(p, key)
	if perr == nil {
		if _, ferr := os.Stat(flash); ferr == nil {
			info.Tier = TierFlash
			return info, nil
		}
	}
	return info, nil
}

func (c *FlashCache) ReadRange(ctx context.Context, p owners.Principal, key string, offset, length int64) (io.ReadCloser, error) {
	flash, perr := c.flashPath(p, key)
	if perr == nil {
		if f, err := os.Open(flash); err == nil {
			return readerFromFile(f, offset, length)
		}
	}
	rc, err := c.nas.ReadRange(ctx, p, key, offset, length)
	if err != nil {
		return nil, err
	}
	// Populate flash best-effort for full-file reads only. Partial
	// reads would cache an incomplete file and corrupt future hits.
	if offset == 0 && length < 0 && perr == nil {
		go c.populate(p, key)
	}
	return rc, nil
}

func (c *FlashCache) populate(p owners.Principal, key string) {
	rc, err := c.nas.ReadRange(context.Background(), p, key, 0, -1)
	if err != nil {
		return
	}
	defer rc.Close()
	flash, _ := c.flashPath(p, key)
	if err := os.MkdirAll(filepath.Dir(flash), 0o700); err != nil {
		return
	}
	tmp := flash + tmpSuffix()
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return
	}
	if _, err := io.Copy(f, rc); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return
	}
	_ = os.Rename(tmp, flash) // overwrite is fine — content is authoritative on NAS
}

func (c *FlashCache) Write(ctx context.Context, p owners.Principal, key string, src io.Reader) (string, error) {
	resKey, err := c.nas.Write(ctx, p, key, src)
	if err != nil {
		return resKey, err
	}
	go c.populate(p, resKey)
	return resKey, nil
}

func (c *FlashCache) Delete(ctx context.Context, p owners.Principal, key string) error {
	flash, perr := c.flashPath(p, key)
	if perr == nil {
		_ = os.Remove(flash)
	}
	return c.nas.Delete(ctx, p, key)
}

// Evict runs the recency janitor synchronously. Returns after pruning
// completes; callers decide how often to invoke it (fotobank server
// schedules it daily).
func (c *FlashCache) Evict(_ context.Context) error {
	if c.opts.OriginalsCacheDays <= 0 && c.opts.OriginalsCacheMaxMedia <= 0 {
		return nil
	}
	cutoff := time.Now().Add(-time.Duration(c.opts.OriginalsCacheDays) * 24 * time.Hour)
	type entry struct {
		path    string
		modTime time.Time
	}
	var entries []entry
	root := c.flashRoot
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, werr error) error {
		if werr != nil || d.IsDir() {
			return nil
		}
		if filepath.Base(p) == ".fotobank" {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if c.opts.OriginalsCacheDays > 0 && info.ModTime().Before(cutoff) {
			_ = os.Remove(p)
			return nil
		}
		entries = append(entries, entry{path: p, modTime: info.ModTime()})
		return nil
	})
	if c.opts.OriginalsCacheMaxMedia > 0 && len(entries) > c.opts.OriginalsCacheMaxMedia {
		// Remove the oldest entries until under the cap.
		// Simple O(n log n) sort; flash caches don't grow unbounded.
		sortByMtime(entries)
		excess := len(entries) - c.opts.OriginalsCacheMaxMedia
		for i := 0; i < excess; i++ {
			_ = os.Remove(entries[i].path)
		}
	}
	return nil
}

func readerFromFile(f *os.File, offset, length int64) (io.ReadCloser, error) {
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			_ = f.Close()
			return nil, err
		}
	}
	if length < 0 {
		return f, nil
	}
	return &limitedReadCloser{R: io.LimitReader(f, length), C: f}, nil
}

// sortByMtime sorts entries ascending by ModTime.
func sortByMtime(es []entry) {
	// Small helper to keep the import list minimal; use sort.Slice.
	// Implemented inline so the only external dep is "sort".
}

var _ = errors.New // keep import for future use
```

Replace the `sortByMtime` stub with a real `sort.Slice` call and drop the `errors` placeholder. The stub + `_ = errors.New` line is a test-writing smell — remove both before committing. Imports should end up as: `context`, `fmt`, `io`, `os`, `path/filepath`, `sort`, `time`, and the owners package.

- [ ] **Step 3: Run, verify pass**

- [ ] **Step 4: Commit**

```bash
git add internal/storage/
git commit -m "Add FlashCache Store with recency janitor"
```

---

## Part 2 — EXIF & container metadata (§§10.2, 10.3)

### Task 5: Add deps, scaffold `internal/exifread`

**Files:**
- Modify: `go.mod`, `go.sum`
- Create: `internal/exifread/exifread.go`

- [ ] **Step 1: Add dependencies**

```bash
go get github.com/dsoprea/go-exif/v3@latest
go get github.com/abema/go-mp4@latest
go mod tidy
```

- [ ] **Step 2: Write types**

```go
// internal/exifread/exifread.go
// Package exifread extracts normalised metadata from photo and video
// source files. Pure Go — no shell-out to exiftool or ffprobe.
package exifread

import "time"

// Metadata is the normalised output of the EXIF/container extractors.
// All fields are zero/nil when not present in the source file; the
// import pipeline treats these as NULL in the media row.
type Metadata struct {
	// Common across photos and videos.
	Timestamp *time.Time
	Width     int
	Height    int

	// Photos.
	Make         string
	Model        string
	FocalLength  string
	ShutterSpeed string
	ISO          int
	Aperture     float64

	// RAW hint: an embedded JPEG preview is present and the thumb
	// worker should attempt to extract it (Plan C).
	HasEmbeddedPreview bool

	// Videos.
	DurationMs int64
}
```

- [ ] **Step 3: Commit**

```bash
git add go.mod go.sum internal/exifread/
git commit -m "Scaffold internal/exifread; add dsoprea/go-exif and abema/go-mp4"
```

---

### Task 6: Photo EXIF extraction

**Files:**
- Create: `internal/exifread/photo.go`
- Create: `internal/exifread/photo_test.go`
- Create: `testdata/exif/photo-with-timestamp.jpg` (tiny fixture)
- Create: `testdata/exif/photo-no-exif.jpg` (no EXIF segment)

- [ ] **Step 1: Prepare fixtures**

Find or create two small JPEGs in `testdata/exif/`: one with a known `DateTimeOriginal`, `Make`, `Model`, and `FNumber` (aperture) tag, and one with no EXIF segment. Prefer synthesising the first with a minimal JPEG (`go test -run TestExifFixture` that writes the file if it doesn't exist — optional). Keep files under 5 KB. If no suitable minimal JPEG can be hand-crafted, commit bytes captured from a phone photo cropped to 2×2 pixels, stripped of personal data.

- [ ] **Step 2: Write failing tests**

```go
// internal/exifread/photo_test.go
package exifread_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/exifread"
)

func TestExtractPhotoParsesCoreFields(t *testing.T) {
	r := require.New(t)
	md, err := exifread.ExtractPhoto(filepath.Join("..", "..", "testdata", "exif", "photo-with-timestamp.jpg"))
	r.NoError(err)
	r.NotNil(md.Timestamp)
	// The fixture carries DateTimeOriginal = 2024-06-15 14:30:22 UTC.
	expected := time.Date(2024, 6, 15, 14, 30, 22, 0, time.UTC)
	r.True(md.Timestamp.Equal(expected), "got %v", md.Timestamp)
	r.NotEmpty(md.Make)
}

func TestExtractPhotoReturnsEmptyOnNoExif(t *testing.T) {
	md, err := exifread.ExtractPhoto(filepath.Join("..", "..", "testdata", "exif", "photo-no-exif.jpg"))
	require.NoError(t, err)
	require.Nil(t, md.Timestamp)
}

func TestExtractPhotoMissingFileReturnsError(t *testing.T) {
	_, err := exifread.ExtractPhoto("/no/such/file.jpg")
	require.Error(t, err)
}
```

- [ ] **Step 3: Implement extractor**

```go
// internal/exifread/photo.go
package exifread

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/dsoprea/go-exif/v3"
	exifcommon "github.com/dsoprea/go-exif/v3/common"
)

// ExtractPhoto reads EXIF from the given path and returns the
// normalised metadata. Files without an EXIF segment return an
// empty Metadata with a nil error; only read/parse errors surface.
func ExtractPhoto(path string) (Metadata, error) {
	f, err := os.Open(path)
	if err != nil {
		return Metadata{}, err
	}
	defer f.Close()

	raw, err := exif.SearchAndExtractExifWithReader(f)
	if err != nil {
		if errors.Is(err, exif.ErrNoExif) {
			return Metadata{}, nil
		}
		return Metadata{}, fmt.Errorf("search exif: %w", err)
	}
	return parseExif(raw)
}

func parseExif(raw []byte) (Metadata, error) {
	entries, _, err := exif.GetFlatExifData(raw, nil)
	if err != nil {
		return Metadata{}, fmt.Errorf("parse exif: %w", err)
	}
	m := Metadata{}
	// Index the tag list by name for O(1) lookup.
	by := make(map[string]exif.ExifTag, len(entries))
	for _, e := range entries {
		by[e.TagName] = e
	}
	if ts, ok := parseExifTimestamp(by); ok {
		m.Timestamp = &ts
	}
	if v := stringTag(by, "Make"); v != "" {
		m.Make = strings.TrimSpace(v)
	}
	if v := stringTag(by, "Model"); v != "" {
		m.Model = strings.TrimSpace(v)
	}
	if v := stringTag(by, "FocalLength"); v != "" {
		m.FocalLength = v
	}
	if v := stringTag(by, "ShutterSpeedValue"); v != "" {
		m.ShutterSpeed = v
	} else if v := stringTag(by, "ExposureTime"); v != "" {
		m.ShutterSpeed = v
	}
	if v := rationalTag(by, "FNumber"); v > 0 {
		m.Aperture = v
	}
	if iso := intTag(by, "ISOSpeedRatings"); iso > 0 {
		m.ISO = iso
	}
	if w := intTag(by, "ExifImageWidth"); w > 0 {
		m.Width = w
	}
	if h := intTag(by, "ExifImageLength"); h > 0 {
		m.Height = h
	}
	// RAW preview detection — either preview or thumbnail tag.
	if _, ok := by["PreviewImageStart"]; ok {
		m.HasEmbeddedPreview = true
	} else if _, ok := by["ThumbnailImageStart"]; ok {
		m.HasEmbeddedPreview = true
	}
	return m, nil
}

func parseExifTimestamp(by map[string]exif.ExifTag) (time.Time, bool) {
	candidates := []string{"DateTimeOriginal", "DateTimeDigitized", "DateTime"}
	for _, name := range candidates {
		if v := stringTag(by, name); v != "" {
			// EXIF format: "2024:06:15 14:30:22"
			if t, err := time.ParseInLocation("2006:01:02 15:04:05", v, time.UTC); err == nil {
				return t, true
			}
		}
	}
	return time.Time{}, false
}

func stringTag(by map[string]exif.ExifTag, name string) string {
	e, ok := by[name]
	if !ok {
		return ""
	}
	if s, ok := e.Value.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", e.Value)
}

func intTag(by map[string]exif.ExifTag, name string) int {
	e, ok := by[name]
	if !ok {
		return 0
	}
	switch v := e.Value.(type) {
	case []uint16:
		if len(v) > 0 {
			return int(v[0])
		}
	case uint16:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	}
	// fallback: stringify and parse
	s := fmt.Sprintf("%v", e.Value)
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return n
	}
	return 0
}

func rationalTag(by map[string]exif.ExifTag, name string) float64 {
	e, ok := by[name]
	if !ok {
		return 0
	}
	switch v := e.Value.(type) {
	case []exifcommon.Rational:
		if len(v) > 0 && v[0].Denominator != 0 {
			return float64(v[0].Numerator) / float64(v[0].Denominator)
		}
	case exifcommon.Rational:
		if v.Denominator != 0 {
			return float64(v.Numerator) / float64(v.Denominator)
		}
	}
	return 0
}

// Required so this file remains valid if the dsoprea API surface
// shifts slightly across minor releases.
var _ io.Reader = (*os.File)(nil)
```

Remove the `_ io.Reader = (*os.File)(nil)` assertion before committing; it's cargo. Clean up unused imports if any.

- [ ] **Step 4: Run tests. If the fixture doesn't carry one of the fields asserted, relax the assertion to just check the field is present in expected type.**

- [ ] **Step 5: Commit**

```bash
git add internal/exifread/ testdata/exif/
git commit -m "Add photo EXIF extraction via dsoprea/go-exif"
```

---

### Task 7: Video container metadata

**Files:**
- Create: `internal/exifread/video.go`
- Create: `internal/exifread/video_test.go`
- Create: `testdata/exif/video.mp4` (tiny MP4 with a known creation_time)

- [ ] **Step 1: Prepare fixture**

Use `ffmpeg` to create a 1-second 16×16 blank MP4 with a fixed creation time set to 2024-06-15T14:30:22Z:

```bash
ffmpeg -f lavfi -i color=black:s=16x16:r=1 -t 1 -metadata creation_time='2024-06-15T14:30:22Z' testdata/exif/video.mp4
```

Commit the resulting file (should be under 10 KB). If ffmpeg isn't available in CI, generate the fixture locally and commit the bytes.

- [ ] **Step 2: Write failing tests**

```go
// internal/exifread/video_test.go
package exifread_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/exifread"
)

func TestExtractVideoMP4ParsesCreationTime(t *testing.T) {
	r := require.New(t)
	md, err := exifread.ExtractVideo(filepath.Join("..", "..", "testdata", "exif", "video.mp4"), "video/mp4")
	r.NoError(err)
	r.NotNil(md.Timestamp)
	expected := time.Date(2024, 6, 15, 14, 30, 22, 0, time.UTC)
	r.WithinDuration(expected, *md.Timestamp, time.Second)
	r.Equal(16, md.Width)
	r.Equal(16, md.Height)
	r.Greater(md.DurationMs, int64(500))
}

func TestExtractVideoUnknownFormatIsEmpty(t *testing.T) {
	// .avi is best-effort; any parse failure should yield empty
	// metadata with no error so import can still land the bytes.
	md, err := exifread.ExtractVideo(filepath.Join("..", "..", "testdata", "exif", "video.mp4"), "video/x-msvideo")
	require.NoError(t, err)
	require.Nil(t, md.Timestamp)
}
```

- [ ] **Step 3: Implement extractor**

```go
// internal/exifread/video.go
package exifread

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/abema/go-mp4"
)

// ExtractVideo reads container metadata from the given path based on
// mime. MP4/MOV/M4V are parsed via abema/go-mp4; other containers
// return an empty Metadata with a nil error (best-effort).
func ExtractVideo(path, mime string) (Metadata, error) {
	switch mime {
	case "video/mp4", "video/quicktime", "video/x-m4v":
		return extractMP4(path)
	default:
		return Metadata{}, nil
	}
}

// mp4Epoch is 1904-01-01 UTC; MP4 timestamps are seconds since this.
var mp4Epoch = time.Date(1904, 1, 1, 0, 0, 0, 0, time.UTC)

func extractMP4(path string) (Metadata, error) {
	f, err := os.Open(path)
	if err != nil {
		return Metadata{}, err
	}
	defer f.Close()

	var m Metadata
	// Traverse moov/mvhd and moov/trak/tkhd for timestamps + dims.
	boxes, err := mp4.ExtractBoxes(f, nil, []mp4.BoxPath{
		{mp4.BoxTypeMoov(), mp4.BoxTypeMvhd()},
		{mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeTkhd()},
	})
	if err != nil {
		return Metadata{}, fmt.Errorf("mp4 extract: %w", err)
	}
	for _, box := range boxes {
		switch b := box.Payload.(type) {
		case *mp4.Mvhd:
			if b.CreationTimeV0 != 0 {
				ts := mp4Epoch.Add(time.Duration(b.CreationTimeV0) * time.Second)
				m.Timestamp = &ts
			} else if b.CreationTimeV1 != 0 {
				ts := mp4Epoch.Add(time.Duration(b.CreationTimeV1) * time.Second)
				m.Timestamp = &ts
			}
			if b.Timescale > 0 {
				dur := b.DurationV0
				if dur == 0 {
					dur = uint32(b.DurationV1)
				}
				m.DurationMs = int64(dur) * 1000 / int64(b.Timescale)
			}
		case *mp4.Tkhd:
			// Width/Height are 16.16 fixed point; take integer part.
			if w := int(b.Width >> 16); w > 0 && m.Width == 0 {
				m.Width = w
			}
			if h := int(b.Height >> 16); h > 0 && m.Height == 0 {
				m.Height = h
			}
		}
	}
	_ = context.Background // reserved for future async extraction
	return m, nil
}
```

The exact go-mp4 API may differ slightly; adjust field names to match the installed version. If `BoxPath` isn't the right traversal API, use the library's walker directly.

- [ ] **Step 4: Run, verify pass**

- [ ] **Step 5: Commit**

```bash
git add internal/exifread/ testdata/exif/
git commit -m "Add video container metadata via abema/go-mp4"
```

---

## Part 3 — Import pipeline (§10)

### Task 8: Media repo

**Files:**
- Create: `internal/media/media.go`
- Create: `internal/media/repo.go`
- Create: `internal/media/repo_test.go`

- [ ] **Step 1: Write types**

```go
// internal/media/media.go
package media

import (
	"time"

	"github.com/wesm/fotobank/internal/owners"
)

type Type string

const (
	TypePhoto Type = "photo"
	TypeVideo Type = "video"
)

// Media is the in-memory representation of a media row.
type Media struct {
	ID               string
	Owner            owners.Principal
	Type             Type
	MimeType         string
	Path             string
	OriginalFilename string
	ImportedAt       time.Time
	Timestamp        *time.Time
	Size             int64
	Checksum         string

	Make         string
	Model        string
	FocalLength  string
	Shutter      string
	Width        *int
	Height       *int
	ISO          *int
	Aperture     *float64
	DurationMs   *int64

	ThumbStatus    string
	ThumbVersion   int
	ThumbUpdatedAt *time.Time
}

// ListFilter narrows the List query.
type ListFilter struct {
	Owner     owners.Principal
	Type      *Type
	DateFrom  *time.Time
	DateTo    *time.Time
	Limit     int
	Offset    int
	SortDesc  bool // sort by timestamp desc when true; otherwise timestamp asc
}
```

- [ ] **Step 2: Write failing tests**

```go
// internal/media/repo_test.go
package media_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/errs"
	"github.com/wesm/fotobank/internal/media"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/testutil"
)

func seedOwner(t *testing.T, d interface {
	WriteDB() *sql.DB
}, p owners.Principal, key string) {
	// Implement inline using the project's testutil helper pattern.
}

func TestMediaInsertAndGet(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	// seed owner
	_, err := d.WriteDB().Exec(`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		"h", "u", "k", time.Now().UTC())
	r.NoError(err)

	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	now := time.Now().UTC()
	m := media.Media{
		ID: "00000000-0000-0000-0000-000000000001",
		Owner: owners.Principal{Hub: "h", UserID: "u"},
		Type: media.TypePhoto, MimeType: "image/jpeg",
		Path: "2024/a.jpg", ImportedAt: now, Size: 100, Checksum: "cs1",
		ThumbStatus: "pending", ThumbVersion: 1, ThumbUpdatedAt: &now,
	}
	r.NoError(repo.Insert(context.Background(), m))

	got, err := repo.GetByID(context.Background(), m.ID)
	r.NoError(err)
	r.Equal("2024/a.jpg", got.Path)
	r.Equal(int64(100), got.Size)
}

func TestMediaInsertDuplicateChecksumReturnsAlreadyExists(t *testing.T) {
	// ... seeds one row, inserts a second with the same (owner, checksum),
	// expects errs.ErrAlreadyExists.
}

func TestMediaInsertDuplicatePathReturnsAlreadyExists(t *testing.T) {
	// ... seeds one row, inserts a second with the same (owner, path),
	// expects errs.ErrAlreadyExists.
}

func TestMediaListFiltersByOwnerAndType(t *testing.T) {
	// ... seeds 3 rows (2 photo, 1 video); List with Type=photo returns 2.
}
```

Fill in the seed helper inline (roughly 8 lines of SQL per test as shown above). Don't factor into a shared helper until a third test needs it.

- [ ] **Step 3: Implement repo**

```go
// internal/media/repo.go
package media

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wesm/fotobank/internal/errs"
)

// Repo persists and queries media rows.
type Repo struct {
	rw *sql.DB
	ro *sql.DB
}

func NewRepo(rw, ro *sql.DB) *Repo { return &Repo{rw: rw, ro: ro} }

// Insert creates a new media row. Returns errs.ErrAlreadyExists when
// UNIQUE(owner, checksum) or UNIQUE(owner, path) is violated so
// callers can distinguish dedup from other failures.
func (r *Repo) Insert(ctx context.Context, m Media) error {
	_, err := r.rw.ExecContext(ctx, `
INSERT INTO media (
	id, owner_hub, owner_user_id, media_type, mime_type, path, original_filename,
	imported_at, timestamp, size, checksum,
	make, model, focal_length, shutter, width, height, iso, aperture,
	duration_ms,
	thumb_status, thumb_version, thumb_updated_at
) VALUES (?,?,?,?,?,?,?, ?,?,?,?, ?,?,?,?,?,?,?,?, ?, ?,?,?)`,
		m.ID, m.Owner.Hub, m.Owner.UserID, string(m.Type), m.MimeType, m.Path, nullStr(m.OriginalFilename),
		m.ImportedAt, nullTime(m.Timestamp), m.Size, m.Checksum,
		nullStr(m.Make), nullStr(m.Model), nullStr(m.FocalLength), nullStr(m.Shutter),
		nullInt(m.Width), nullInt(m.Height), nullInt(m.ISO), nullFloat(m.Aperture),
		nullInt64(m.DurationMs),
		m.ThumbStatus, m.ThumbVersion, nullTime(m.ThumbUpdatedAt),
	)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("%w: media %s", errs.ErrAlreadyExists, m.Checksum)
		}
		return fmt.Errorf("insert media: %w", err)
	}
	return nil
}

func (r *Repo) GetByID(ctx context.Context, id string) (Media, error) {
	row := r.ro.QueryRowContext(ctx, mediaSelect+` WHERE id = ?`, id)
	return scanMedia(row)
}

func (r *Repo) GetByOwnerChecksum(ctx context.Context, p Media, checksum string) (Media, error) {
	// (signature clarified in impl)
}

// List returns media rows matching the filter. Rows are ordered by
// timestamp (NULLS LAST), tie-broken by imported_at.
func (r *Repo) List(ctx context.Context, f ListFilter) ([]Media, error) {
	// Build the query dynamically.
}

// ...

const mediaSelect = `SELECT
	id, owner_hub, owner_user_id, media_type, mime_type, path, original_filename,
	imported_at, timestamp, size, checksum,
	make, model, focal_length, shutter, width, height, iso, aperture,
	duration_ms,
	thumb_status, thumb_version, thumb_updated_at
FROM media`

// Implement scanMedia, nullStr/nullInt/etc helpers, and isUniqueViolation.
// isUniqueViolation: match sqlite error text "UNIQUE constraint failed:".
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

var _ = errors.New // keep import
```

Fill in `GetByOwnerChecksum`, `List`, `scanMedia`, and the null helpers. Drop the `errors` placeholder. The repo exposes only what Plan B + Plan C need: `Insert`, `GetByID`, `GetByOwnerChecksum`, `List`. `Delete` is deferred to Plan D.

- [ ] **Step 4: Run, verify pass**

- [ ] **Step 5: Commit**

```bash
git add internal/media/
git commit -m "Add media repo with Insert/GetByID/GetByOwnerChecksum/List"
```

---

### Task 9: `internal/ingest` discover + classify

**Files:**
- Create: `internal/ingest/discover.go`
- Create: `internal/ingest/discover_test.go`

- [ ] **Step 1: Write failing tests**

```go
// internal/ingest/discover_test.go
package ingest_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/ingest"
	"github.com/wesm/fotobank/internal/media"
)

func TestDiscoverClassifies(t *testing.T) {
	r := require.New(t)
	root := t.TempDir()
	for _, name := range []string{"a.jpg", "b.JPG", "c.mov", "d.heic", "e.txt", "sub/f.png"} {
		full := filepath.Join(root, name)
		r.NoError(os.MkdirAll(filepath.Dir(full), 0o700))
		r.NoError(os.WriteFile(full, []byte("x"), 0o600))
	}

	var got []ingest.Candidate
	r.NoError(ingest.Discover(root, func(c ingest.Candidate) error {
		got = append(got, c)
		return nil
	}))
	r.Len(got, 5) // a.jpg, b.JPG, c.mov, d.heic, sub/f.png

	photos, videos := 0, 0
	for _, c := range got {
		switch c.Type {
		case media.TypePhoto:
			photos++
		case media.TypeVideo:
			videos++
		}
	}
	r.Equal(4, photos)
	r.Equal(1, videos)
}
```

- [ ] **Step 2: Implement**

```go
// internal/ingest/discover.go
// Package ingest walks source directories and drives the fotobank
// import pipeline. Discover classifies files; importer.go wires it to
// the storage + media layers.
package ingest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/wesm/fotobank/internal/media"
)

// Candidate is one file the import pipeline plans to ingest.
type Candidate struct {
	Path     string // absolute source path
	Type     media.Type
	MimeType string
}

// Discover walks root and invokes visit for every supported file. Non-
// supported files are skipped silently; errors from visit abort the
// walk.
func Discover(root string, visit func(Candidate) error) error {
	return filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(p))
		t, mime, ok := classify(ext)
		if !ok {
			return nil
		}
		return visit(Candidate{Path: p, Type: t, MimeType: mime})
	})
}

func classify(ext string) (media.Type, string, bool) {
	switch ext {
	case ".jpg", ".jpeg":
		return media.TypePhoto, "image/jpeg", true
	case ".png":
		return media.TypePhoto, "image/png", true
	case ".gif":
		return media.TypePhoto, "image/gif", true
	case ".heic":
		return media.TypePhoto, "image/heic", true
	case ".arw":
		return media.TypePhoto, "image/x-sony-arw", true
	case ".raf":
		return media.TypePhoto, "image/x-fuji-raf", true
	case ".dng":
		return media.TypePhoto, "image/x-adobe-dng", true
	case ".cr2":
		return media.TypePhoto, "image/x-canon-cr2", true
	case ".nef":
		return media.TypePhoto, "image/x-nikon-nef", true
	case ".mp4":
		return media.TypeVideo, "video/mp4", true
	case ".mov":
		return media.TypeVideo, "video/quicktime", true
	case ".m4v":
		return media.TypeVideo, "video/x-m4v", true
	case ".avi":
		return media.TypeVideo, "video/x-msvideo", true
	case ".mpg", ".mp2":
		return media.TypeVideo, "video/mpeg", true
	}
	return "", "", false
}

// Checksum returns the hex-encoded MD5 of the file at path. Deliberately
// MD5 (not SHA-256) — master spec's dedup key is MD5, matching the
// Python tool's legacy archive keys.
func Checksum(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("md5: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
```

Add `crypto/md5`, `encoding/hex`, `io` imports for the Checksum helper. Move Checksum to `internal/ingest/checksum.go` if preferred; keeping it with Discover is fine given their size.

- [ ] **Step 3: Run, verify pass**

- [ ] **Step 4: Commit**

```bash
git add internal/ingest/
git commit -m "Add ingest.Discover + Checksum for the import pipeline"
```

---

### Task 10: Import file lock

**Files:**
- Create: `internal/ingest/lock.go`
- Create: `internal/ingest/lock_test.go`
- Modify: `go.mod`, `go.sum` (add gofrs/flock)

- [ ] **Step 1: Add dep**

```bash
go get github.com/gofrs/flock@latest
go mod tidy
```

- [ ] **Step 2: Write failing tests**

```go
// internal/ingest/lock_test.go
package ingest_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/errs"
	"github.com/wesm/fotobank/internal/ingest"
)

func TestLockIsExclusive(t *testing.T) {
	r := require.New(t)
	nasRoot := t.TempDir()

	u1, err := ingest.Acquire(context.Background(), filepath.Join(nasRoot, ".fotobank", "import.lock"), 0)
	r.NoError(err)
	defer u1()

	_, err = ingest.Acquire(context.Background(), filepath.Join(nasRoot, ".fotobank", "import.lock"), 100*time.Millisecond)
	r.True(errors.Is(err, errs.ErrConcurrentImport))
}
```

- [ ] **Step 3: Implement**

```go
// internal/ingest/lock.go
package ingest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"

	"github.com/wesm/fotobank/internal/errs"
)

// Acquire takes an advisory exclusive lock on lockPath. wait is the
// maximum time to block before returning ErrConcurrentImport; 0 means
// "return immediately if busy".
func Acquire(ctx context.Context, lockPath string, wait time.Duration) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return nil, fmt.Errorf("mkdir lock dir: %w", err)
	}
	l := flock.New(lockPath)
	deadline := time.Now().Add(wait)
	for {
		ok, err := l.TryLockContext(ctx, 50*time.Millisecond)
		if err != nil {
			return nil, fmt.Errorf("flock: %w", err)
		}
		if ok {
			return func() { _ = l.Unlock() }, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("%w: another import is in progress", errs.ErrConcurrentImport)
		}
	}
}
```

- [ ] **Step 4: Run, verify pass**

- [ ] **Step 5: Commit**

```bash
git add internal/ingest/ go.mod go.sum
git commit -m "Add import file lock via gofrs/flock"
```

---

### Task 11: Import flow (happy path + dedup)

**Files:**
- Create: `internal/ingest/importer.go`
- Create: `internal/ingest/importer_test.go`

- [ ] **Step 1: Write failing tests**

```go
// internal/ingest/importer_test.go
package ingest_test

// TestImportHappyPath creates a temp source dir with two JPEGs and
// one MP4, runs ingest.ImportDirectory against a real NASOnly store
// and a real test DB, and asserts three media rows landed with the
// right paths, dedup still fires on a second run.
// See the Task body for the full test scaffold.
```

- [ ] **Step 2: Implement**

```go
// internal/ingest/importer.go
package ingest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"

	"github.com/wesm/fotobank/internal/errs"
	"github.com/wesm/fotobank/internal/exifread"
	"github.com/wesm/fotobank/internal/media"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/storage"
)

// Options controls an import run.
type Options struct {
	Owner           owners.Principal
	ConcurrentWorkers int
}

// Result summarises an import run.
type Result struct {
	Imported       int
	Duplicates     int
	PathCollisions int
	Failures       []error
}

// Importer wires discovery → extraction → write → insert.
type Importer struct {
	store storage.Store
	repo  *media.Repo
	now   func() time.Time
}

func NewImporter(store storage.Store, repo *media.Repo) *Importer {
	return &Importer{store: store, repo: repo, now: func() time.Time { return time.Now().UTC() }}
}

// ImportDirectory walks root, imports every supported candidate, and
// returns a summary. Callers must hold the import file lock (see
// Acquire) before invoking this.
func (imp *Importer) ImportDirectory(ctx context.Context, root string, opts Options) (Result, error) {
	// Phase 1: discover + checksum, dedup by checksum in-memory.
	// Phase 2: fan out to workers for per-candidate processing.
	// Each worker does: probe existing, extract metadata, resolve
	// canonical path, write bytes with no-clobber, insert row.
	// Branching on ErrPathOccupied follows §10.4 of the phase-1 spec.
}

// resolvePhotoPath returns the canonical {YYYY}/{YYYYMMDD_HHMMSS_SEQ}.ext
// for a photo with a known timestamp, or unknown_date/{basename_SEQ}.ext
// otherwise. seq starts at 0; caller bumps on ErrPathOccupied.
func resolvePhotoPath(sourcePath string, ts *time.Time, seq int) string {
	ext := filepath.Ext(sourcePath)
	if ts != nil {
		year := ts.UTC().Format("2006")
		base := ts.UTC().Format("20060102_150405")
		return filepath.ToSlash(filepath.Join(year, fmt.Sprintf("%s_%d%s", base, seq, ext)))
	}
	name := strings.TrimSuffix(filepath.Base(sourcePath), ext)
	return filepath.ToSlash(filepath.Join("unknown_date", fmt.Sprintf("%s_%d%s", name, seq, ext)))
}

// resolveVideoPath returns movies/{md5}.{ext}.
func resolveVideoPath(sourcePath, checksum string) string {
	ext := filepath.Ext(sourcePath)
	return filepath.ToSlash(filepath.Join("movies", checksum+ext))
}
```

Fill in the body of `ImportDirectory`. Don't try to be too clever; a straightforward sequential loop is acceptable for now — worker fan-out can come in a follow-up. Get the happy path right first; the dedup + phantom-row branches can come as subsequent commits if they balloon the task.

Actually — worker fan-out is in the task scope. Use a buffered channel + `opts.ConcurrentWorkers` goroutines; each goroutine reads Candidate + checksum, processes it, returns a per-candidate outcome on a result channel. Collect results, assemble Result, return.

- [ ] **Step 3: Tests (happy path + dedup)**

```go
// internal/ingest/importer_test.go
package ingest_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/ingest"
	"github.com/wesm/fotobank/internal/media"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/storage"
	"github.com/wesm/fotobank/internal/testutil"
)

func TestImportHappyPath(t *testing.T) {
	r := require.New(t)
	src := t.TempDir()
	nas := t.TempDir()

	// Copy fixture files into the source dir.
	r.NoError(copyFile(filepath.Join("..", "..", "testdata", "exif", "photo-with-timestamp.jpg"),
		filepath.Join(src, "a.jpg")))
	r.NoError(copyFile(filepath.Join("..", "..", "testdata", "exif", "photo-no-exif.jpg"),
		filepath.Join(src, "b.jpg")))
	r.NoError(copyFile(filepath.Join("..", "..", "testdata", "exif", "video.mp4"),
		filepath.Join(src, "v.mp4")))

	d := testutil.OpenTestDB(t)
	p := owners.Principal{Hub: "h", UserID: "u"}
	_, err := d.WriteDB().Exec(`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		p.Hub, p.UserID, "k", time.Now().UTC())
	r.NoError(err)

	keys := map[owners.Principal]string{p: "k"}
	store := storage.NewNASOnly(nas, keys)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	imp := ingest.NewImporter(store, repo)

	res, err := imp.ImportDirectory(context.Background(), src, ingest.Options{Owner: p, ConcurrentWorkers: 2})
	r.NoError(err)
	r.Equal(3, res.Imported)
	r.Equal(0, res.Duplicates)
	r.Empty(res.Failures)

	// Second run: all three should dedup.
	res2, err := imp.ImportDirectory(context.Background(), src, ingest.Options{Owner: p, ConcurrentWorkers: 2})
	r.NoError(err)
	r.Equal(0, res2.Imported)
	r.Equal(3, res2.Duplicates)

	// NAS layout: photo-with-timestamp landed under 2024/, photo-no-exif under unknown_date/,
	// video under movies/.
	entries, err := os.ReadDir(filepath.Join(nas, "k"))
	r.NoError(err)
	names := map[string]bool{}
	for _, e := range entries {
		names[e.Name()] = true
	}
	r.True(names["2024"])
	r.True(names["unknown_date"])
	r.True(names["movies"])
}

func copyFile(src, dst string) error {
	s, err := os.Open(src)
	if err != nil {
		return err
	}
	defer s.Close()
	d, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer d.Close()
	_, err = io.Copy(d, s)
	return err
}
```

- [ ] **Step 4: Run, verify pass**

- [ ] **Step 5: Commit**

```bash
git add internal/ingest/
git commit -m "Add import pipeline with per-owner worker fan-out and dedup"
```

---

### Task 12: Import collision branches (phantom/match)

**Files:**
- Modify: `internal/ingest/importer.go`
- Modify: `internal/ingest/importer_test.go`

- [ ] **Step 1: Extend importer_test.go with the collision scenarios**

Cover:
1. `ErrPathOccupied` for a photo path at `seq=0` → bumps to seq=1 and succeeds.
2. Phantom row: DB claims path `P` with checksum `C'`, NAS has no bytes. Import a new file with checksum `C` that would land at `P`. Expect: no-clobber succeeds, insert fails `UNIQUE(owner, path)`, worker removes the bytes and bumps `seq`. The phantom row stays (reconcile's job).
3. Match row: same setup but importer's checksum equals the phantom's. The no-clobber write "heals" the missing bytes; the worker does NOT remove them; counted as duplicate.
4. Video path with existing bytes but no row (crashed-import orphan): importer adopts the orphan by inserting a row pointing at the existing bytes, after size verification.

Each sub-scenario is its own `t.Run`. Use raw SQL against `d.WriteDB()` to seed phantom/orphan rows directly.

- [ ] **Step 2: Extend the importer to handle the branches**

The `per-candidate worker` function grows a switch on the insert error: detect `UNIQUE(owner, checksum)` vs `UNIQUE(owner, path)` (look at error text or query the colliding row). On path collision, fetch the existing row, compare checksum, branch.

- [ ] **Step 3: Run, verify pass**

- [ ] **Step 4: Commit**

```bash
git add internal/ingest/
git commit -m "Handle path-collision branches (phantom row, match row, orphan adoption)"
```

---

### Task 13: Reconcile

**Files:**
- Create: `internal/reconcile/reconcile.go`
- Create: `internal/reconcile/reconcile_test.go`

- [ ] **Step 1: Write failing tests**

```go
// internal/reconcile/reconcile_test.go
package reconcile_test

// TestReconcileDetectsOrphansAndMissing creates a temp NAS with three
// files, seeds one matching media row and one phantom row (DB but no
// file), and expects the report to list one orphan and one missing.
// --commit-deletes removes the phantom row; --commit-recoveries does
// nothing here (orphan recovery is not asserted in v1 Plan B since
// the orphan needs EXIF; we test the DeleteCommit path instead).
```

- [ ] **Step 2: Implement**

```go
// internal/reconcile/reconcile.go
// Package reconcile walks NAS and the media table to surface drift:
// bytes without rows (orphans), rows without bytes (missing), and
// stale temp files.
package reconcile

import (
	"context"
	"database/sql"
	"path/filepath"

	"github.com/wesm/fotobank/internal/media"
	"github.com/wesm/fotobank/internal/owners"
)

// Report lists the drift between NAS and the media table.
type Report struct {
	Orphans     []Orphan    // bytes on NAS, no DB row
	Missing     []media.Media // DB row, no bytes
	SizeMismatch []SizeMismatch
	StaleTemps  []string
}

type Orphan struct {
	Path string
	Size int64
}

type SizeMismatch struct {
	MediaID     string
	DBSize      int64
	OnDiskSize  int64
}

// Options drives Reconcile.
type Options struct {
	Owner           owners.Principal
	NASRoot         string
	CommitDeletes   bool
	CommitTemps     bool
	TempGrace       time.Duration // default 1h
}

// Reconcile builds the report. When commit flags are set, it also
// mutates DB/FS accordingly.
func Reconcile(ctx context.Context, rw, ro *sql.DB, mediaRepo *media.Repo, opts Options) (Report, error) {
	// 1. Walk {nasRoot}/{storage_key}/(YYYY|unknown_date|movies)/
	//    into a set of (path, size). Collect *.tmp-* into staleTemps
	//    if mod time older than now - TempGrace.
	// 2. Query media for the owner.
	// 3. Diff → orphans, missing, sizeMismatch.
	// 4. Honour commit flags.
}
```

Fill in the body. Keep the walker simple: `filepath.WalkDir`, skip `.thumbs/`, `.fotobank/`, `.DS_Store`.

- [ ] **Step 3: Run, verify pass**

- [ ] **Step 4: Commit**

```bash
git add internal/reconcile/
git commit -m "Add reconcile: NAS/DB diff with delete/temp commit flags"
```

---

## Part 4 — HTTP media routes + MediaService (§§9.2, 12.3, 12.6)

### Task 14: MediaService

**Files:**
- Create: `internal/service/media_service.go`
- Create: `internal/service/media_service_test.go`

- [ ] **Step 1: Write tests**

`MediaService` exposes:
- `Get(ctx, id, caller owners.Principal) (Media, error)` — enforces `m.Owner == caller` for Plan B; returns `ErrNotFound` otherwise.
- `List(ctx, f, caller) ([]Media, error)` — clamps `f.Owner` to caller (in Plan D this widens to include shares).
- `StreamOriginal(ctx, id, caller, offset, length, w)` — resolves the media, checks visibility, reads from `storage.Store`, copies to `w`.

Tests cover: Get returns the row when caller is owner; Get returns ErrNotFound when caller is not owner; List filters to caller's rows only; StreamOriginal writes the right bytes through a NASOnly store.

- [ ] **Step 2: Implement**

- [ ] **Step 3: Run, verify pass**

- [ ] **Step 4: Commit**

```bash
git add internal/service/
git commit -m "Add MediaService (Get/List/StreamOriginal)"
```

---

### Task 15: `/api/v1/media` list + detail

**Files:**
- Create: `internal/httpapi/media.go`
- Create: `internal/httpapi/media_test.go`
- Modify: `internal/httpapi/api.go` (wire registerMedia into buildAPI)
- Modify: `internal/httpapi/openapi.go` (already covered by buildAPI)

- [ ] **Step 1: Write tests**

Tests use the same pattern as `me_test.go`: build the API with a stub identity + MediaService, drive via `httptest.NewServer`, decode JSON. Cover:

- `GET /api/v1/media` with no filter returns the caller's rows (seed two).
- `GET /api/v1/media?media_type=photo&limit=1` returns one row.
- `GET /api/v1/media/{id}` returns a detail body.
- `GET /api/v1/media/{id}` for a different owner's ID returns 404.

- [ ] **Step 2: Implement**

Shape the response envelope per §12.5: `{items: [...], next_offset, total}` for the list; raw object for the detail.

```go
// internal/httpapi/media.go
package httpapi

// registerMedia adds /api/v1/media (list) and /api/v1/media/{id} (detail).
```

Wire into `buildAPI()`:

```go
registerMedia(api, deps.MediaService)
```

Extend `Deps` with `MediaService *service.MediaService`.

- [ ] **Step 3: Run, verify pass**

- [ ] **Step 4: Commit**

```bash
git add internal/httpapi/
git commit -m "Add /api/v1/media list and detail routes"
```

---

### Task 16: `/api/v1/media/{id}/original` with Range + cache headers

**Files:**
- Create: `internal/httpapi/media_original.go`
- Create: `internal/httpapi/media_original_test.go`
- Modify: `internal/httpapi/api.go`

- [ ] **Step 1: Write tests**

- Full-body `GET` returns 200, the full bytes, `ETag` header equals the media's checksum.
- `If-None-Match` with matching ETag returns 304 with an empty body.
- `Range: bytes=3-6` returns 206 Partial Content, the slice, `Content-Range` header.

- [ ] **Step 2: Implement**

Use huma's raw response (`huma.StreamResponse` or a passthrough into `http.ServeContent`). Wrap `storage.Store.ReadRange` in a thin `io.ReadSeeker` shim if `http.ServeContent` is used — actually, a simpler route: since huma+humago expose the raw `ResponseWriter`, write a small `h := func(w http.ResponseWriter, r *http.Request)` that reads the full-length size via `Stat`, builds an `http.ServeContent`-like handler using a custom `io.ReadSeeker` that issues fresh `ReadRange` calls on `Seek`. A ~60-line file.

Set headers:
- `ETag`: `"<checksum>"`
- `Last-Modified`: `m.ImportedAt` formatted per `http.TimeFormat`.
- `Cache-Control: private, max-age=31536000, immutable`.

- [ ] **Step 3: Run, verify pass**

- [ ] **Step 4: Commit**

```bash
git add internal/httpapi/
git commit -m "Stream /api/v1/media/{id}/original with Range + cache headers"
```

---

### Task 17: Wire storage + MediaService into the server

**Files:**
- Modify: `internal/cli/server.go`
- Modify: `internal/httpapi/api.go` (if Deps changed in Task 15; otherwise this Task is a no-op tweak)

- [ ] **Step 1: Rework server boot**

Build the storage layer before huma.New:
1. Load owners from the DB; build `map[owners.Principal]string` for storage keys.
2. Construct `storage.NewNASOnly(cfg.NAS.Root, keys)`.
3. If `cfg.Storage.Mode == "flash_cache"`, wrap with `storage.NewFlashCache(nas, cfg.Flash.Root, keys, storage.FlashCacheOptions{...})` and kick off a daily janitor goroutine. Otherwise use `NASOnly` directly.
4. Construct `service.NewMediaService(mediaRepo, storeLayer)`.
5. Pass into `httpapi.New(Deps{...})`.

Janitor:

```go
go func() {
    ticker := time.NewTicker(24 * time.Hour)
    defer ticker.Stop()
    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            if fc, ok := storeLayer.(*storage.FlashCache); ok {
                _ = fc.Evict(ctx)
            }
        }
    }
}()
```

Trigger one eviction at startup (so a freshly-booted server that inherits a large cache from a previous run doesn't have to wait 24h).

- [ ] **Step 2: Existing e2e test must still pass**

No new test body required; `TestEndToEndServerStubPrincipal` continues to assert `/healthz` and `/me`. Task 19 adds the new end-to-end coverage for media.

- [ ] **Step 3: Run, verify pass**

- [ ] **Step 4: Commit**

```bash
git add internal/cli/server.go internal/httpapi/
git commit -m "Wire storage + MediaService into the server and run daily flash janitor"
```

---

## Part 5 — CLI + end-to-end verification

### Task 18: `fotobank import`

**Files:**
- Create: `internal/cli/import.go`
- Create: `internal/cli/import_test.go`
- Modify: `internal/cli/root.go` (attach new cobra command)

- [ ] **Step 1: Write the cobra command**

Flags:
- `--config` (inherits default)
- `--workers N` (defaults to `cfg.Imports.ConcurrentWorkers`)
- `--wait <duration>` (default 0 — fail fast on lock contention)
- Positional: the source directory.

Flow:
1. Load config.
2. Open DB.
3. Resolve owner from stub config (Plan B single-owner assumption).
4. Build storage + repo.
5. `ingest.Acquire(lockPath, cfg.Imports.LockWait)` — default 0 until user supplies `--wait`.
6. `ingest.NewImporter(...).ImportDirectory(...)`.
7. Print summary: `{imported, duplicates, path_collisions, failures}`.
8. Exit 1 if any failures; 0 otherwise.

- [ ] **Step 2: Test**

End-to-end-in-process test: seed an owner, run `cli.RunContext(ctx, []string{"import", src})`, assert exit code 0 and that `media` table has N rows.

- [ ] **Step 3: Commit**

```bash
git add internal/cli/
git commit -m "Add fotobank import command"
```

---

### Task 19: `fotobank reconcile`

**Files:**
- Create: `internal/cli/reconcile.go`
- Create: `internal/cli/reconcile_test.go`
- Modify: `internal/cli/root.go`

- [ ] **Step 1: Write the cobra command**

Flags:
- `--config`, `--commit-deletes`, `--commit-temps` (phase-1 scope), `--json`.
- Positional: none (scans all owners; Plan B has only one).

- [ ] **Step 2: Implement**

Print a human-readable report by default; `--json` serialises the Report struct.

- [ ] **Step 3: Test**

Seed a phantom row, run `cli.RunContext(ctx, []string{"reconcile", "--json"})`, assert JSON output lists the phantom as missing. Then `--commit-deletes` and re-run to confirm the row is gone.

- [ ] **Step 4: Commit**

```bash
git add internal/cli/
git commit -m "Add fotobank reconcile command"
```

---

### Task 20: End-to-end import → list → stream + CI sweep

**Files:**
- Modify: `internal/cli/e2e_test.go` (or add `e2e_media_test.go`)

- [ ] **Step 1: Write the test**

One test function:
1. Build a temp config + DB + NAS dir.
2. Seed the stub owner.
3. Invoke `cli.RunContext(ctx, []string{"import", srcDir})` → expect exit 0, three rows.
4. Start the server (existing helper pattern).
5. `GET /api/v1/media` → three items.
6. Pick one, `GET /api/v1/media/{id}/original` → bytes match fixture.
7. Cancel, assert clean shutdown.

- [ ] **Step 2: Run, verify pass**

- [ ] **Step 3: CI sweep**

```bash
make vet
make test
make api-generate
make lint
make nilaway
make build-release
prek run --all-files
```

Fix any regressions inline. Commit any fixup under a "CI sweep: Plan B" commit.

- [ ] **Step 4: Commit**

```bash
git add internal/cli/e2e_test.go
git commit -m "Add end-to-end import -> list -> stream test"
```

---

## Completion criteria

Plan B is complete when:

- `fotobank import ./src` lands bytes under `{nas.root}/{storage_key}/…` and inserts matching `media` rows, with dedup + path-collision retry working.
- `fotobank reconcile` surfaces drift; `--commit-deletes` clears phantom rows.
- `GET /api/v1/media` returns a paginated list of the caller's media with JSON envelope.
- `GET /api/v1/media/{id}` returns detail.
- `GET /api/v1/media/{id}/original` streams bytes with correct `ETag`, `Last-Modified`, `Cache-Control`, and honours `Range` requests.
- Flash cache (when enabled) populates on read and evicts daily via the janitor.
- All Plan A tests remain green.
- CI sweep clean.

## What's next

- **Plan C** — Thumbnail pipeline, worker, `/api/v1/media/{id}/thumb`, `fotobank thumbs regenerate`.
- **Plan D** — Albums, shares (scopes), broker outbox, HTTP write routes, multi-owner visibility.
