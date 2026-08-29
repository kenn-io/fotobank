package storage_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/storage"
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
	fc, nasRoot, flashRoot, p := newFlashCache(t)
	_, err := fc.Write(context.Background(), p, "a.jpg", bytes.NewReader([]byte("bytes")))
	r.NoError(err)

	// NAS has the bytes.
	nasBytes, err := os.ReadFile(filepath.Join(nasRoot, "key1", "a.jpg"))
	r.NoError(err)
	r.Equal("bytes", string(nasBytes))

	// Wait for the background flash populate so t.TempDir cleanup
	// does not race the populate goroutine.
	waitForFlash(t, filepath.Join(flashRoot, "key1", "a.jpg"))
}

// waitForFlash polls up to ~500ms for the given flash path to exist.
// Used to synchronise with the background populate goroutine so tests
// don't race with t.TempDir cleanup.
func waitForFlash(t *testing.T, path string) {
	t.Helper()
	for range 50 {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.FailNow(t, "flash populate did not complete", "path=%s", path)
}

func TestFlashCacheReadPopulatesFlashOnMiss(t *testing.T) {
	r := require.New(t)
	fc, _, flashRoot, p := newFlashCache(t)
	_, err := fc.Write(context.Background(), p, "a.jpg", bytes.NewReader([]byte("bytes")))
	r.NoError(err)

	// Read via FlashCache; on miss it should copy NAS -> flash.
	rc, err := fc.ReadRange(context.Background(), p, "a.jpg", 0, -1)
	r.NoError(err)
	got, err := io.ReadAll(rc)
	r.NoError(rc.Close())
	r.NoError(err)
	r.Equal("bytes", string(got))

	// Give the populate goroutine a moment; poll for the flash file.
	flashPath := filepath.Join(flashRoot, "key1", "a.jpg")
	for range 50 {
		if _, err := os.Stat(flashPath); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	r.Fail("flash cache did not populate", flashPath)
}

func TestFlashCacheRejectsTraversalAndAbsoluteKeys(t *testing.T) {
	r := require.New(t)
	fc, _, flashRoot, p := newFlashCache(t)

	// Seed a flash-side target the traversal would try to escape to.
	target := filepath.Join(flashRoot, "secret.txt")
	r.NoError(os.WriteFile(target, []byte("do not touch"), 0o600))

	for _, key := range []string{"../secret.txt", "/etc/passwd", "a//b", "a/./b", "a/../b", ""} {
		_, err := fc.Write(context.Background(), p, key, bytes.NewReader([]byte("x")))
		r.ErrorIs(err, storage.ErrInvalidKey, "Write should reject key %q", key)
		_, err = fc.ReadRange(context.Background(), p, key, 0, -1)
		r.ErrorIs(err, storage.ErrInvalidKey, "ReadRange should reject key %q", key)
		_, err = fc.Stat(context.Background(), p, key)
		r.ErrorIs(err, storage.ErrInvalidKey, "Stat should reject key %q", key)
		r.ErrorIs(fc.Delete(context.Background(), p, key), storage.ErrInvalidKey, "Delete should reject key %q", key)
	}

	b, err := os.ReadFile(target)
	r.NoError(err)
	r.Equal("do not touch", string(b))
}

func TestFlashCacheJanitorEvictsByAge(t *testing.T) {
	r := require.New(t)
	fc, _, flashRoot, _ := newFlashCache(t)
	flashPath := filepath.Join(flashRoot, "key1", "old.bin")
	r.NoError(os.MkdirAll(filepath.Dir(flashPath), 0o700))
	r.NoError(os.WriteFile(flashPath, []byte("old"), 0o600))

	// Back-date the flash file to look old.
	old := time.Now().Add(-10 * 24 * time.Hour)
	r.NoError(os.Chtimes(flashPath, old, old))

	// Run the janitor directly (not on a timer).
	r.NoError(fc.Evict(context.Background()))

	_, err := os.Stat(flashPath)
	r.ErrorIs(err, os.ErrNotExist)
}

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
	keys := map[owners.Principal]string{p: "550e8400-e29b-41d4-a716-446655440000"}
	nas := storage.NewNASOnly(nasRoot, keys)
	cache := storage.NewFlashCache(nas, originalsRoot, keys, storage.FlashCacheOptions{})
	cache.EnableThumbs(thumbsRoot)

	_, err := cache.Write(context.Background(), p, ".thumbs/abc/v0/grid.jpg", strings.NewReader("bytes"))
	r.NoError(err)

	// Allow the best-effort populate goroutine to finish.
	deadline := time.Now().Add(2 * time.Second)
	thumbPath := filepath.Join(thumbsRoot, "550e8400-e29b-41d4-a716-446655440000", ".thumbs", "abc", "v0", "grid.jpg")
	for time.Now().Before(deadline) {
		if _, err := os.Stat(thumbPath); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	_, statErr := os.Stat(thumbPath)
	r.NoError(statErr, "thumbs bytes missing from thumbs root")
	// Must NOT have populated originals root.
	origPath := filepath.Join(originalsRoot, "550e8400-e29b-41d4-a716-446655440000", ".thumbs", "abc", "v0", "grid.jpg")
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
	keys := map[owners.Principal]string{p: "550e8400-e29b-41d4-a716-446655440000"}
	nas := storage.NewNASOnly(nasRoot, keys)
	cache := storage.NewFlashCache(nas, originalsRoot, keys, storage.FlashCacheOptions{})
	// EnableThumbs never called — cache disabled for thumbs.

	_, err := cache.Write(context.Background(), p, ".thumbs/abc/v0/grid.jpg", strings.NewReader("bytes"))
	r.NoError(err)

	// Nothing lands in either flash root — writes go straight to NAS.
	time.Sleep(100 * time.Millisecond)
	_, origErr := os.Stat(filepath.Join(originalsRoot, "550e8400-e29b-41d4-a716-446655440000", ".thumbs", "abc", "v0", "grid.jpg"))
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
	keys := map[owners.Principal]string{p: "550e8400-e29b-41d4-a716-446655440000"}
	nas := storage.NewNASOnly(filepath.Join(tmp, "nas"), keys)
	cache := storage.NewFlashCache(nas, originalsRoot, keys, storage.FlashCacheOptions{OriginalsCacheDays: 1})
	cache.EnableThumbs(thumbsRoot)

	r.NoError(cache.Evict(context.Background()))
	_, err := os.Stat(sentinel)
	r.NoError(err, "janitor swept thumbs root")
}
