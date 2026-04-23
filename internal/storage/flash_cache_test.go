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
	fc, _, flashRoot, p := newFlashCache(t)
	_, err := fc.Write(context.Background(), p, "old.bin", bytes.NewReader([]byte("old")))
	r.NoError(err)
	// Populate flash via a read.
	rc, err := fc.ReadRange(context.Background(), p, "old.bin", 0, -1)
	r.NoError(err)
	r.NoError(rc.Close())

	// Poll until the populate finishes.
	flashPath := filepath.Join(flashRoot, "key1", "old.bin")
	for range 50 {
		if _, err := os.Stat(flashPath); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Back-date the flash file to look old.
	old := time.Now().Add(-10 * 24 * time.Hour)
	r.NoError(os.Chtimes(flashPath, old, old))

	// Run the janitor directly (not on a timer).
	r.NoError(fc.Evict(context.Background()))

	_, err = os.Stat(flashPath)
	r.ErrorIs(err, os.ErrNotExist)
}
