package storage_test

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/storage"
)

func newThumbCache(t *testing.T) (*storage.ThumbCache, *storage.NASOnly, string, string, owners.Principal) {
	t.Helper()
	nasRoot := t.TempDir()
	flashRoot := t.TempDir()
	owner := owners.Principal{Hub: "h", UserID: "u"}
	keys := map[owners.Principal]string{owner: "key1"}
	nas := storage.NewNASOnly(nasRoot, keys)
	cache := storage.NewThumbCache(nas, flashRoot)
	return cache, nas, nasRoot, flashRoot, owner
}

func TestThumbCacheUsesCurrentOwnerKeys(t *testing.T) {
	r := require.New(t)
	cache, nas, _, _, _ := newThumbCache(t)
	owner := owners.Principal{Hub: "h", UserID: "new-owner"}
	key := ".thumbs/example/v1-grid.jpg"
	_, err := cache.Write(t.Context(), owner, key, strings.NewReader("bytes"))
	r.Error(err)
	nas.SetOwnerKey(owner, "key2")
	_, err = cache.Write(t.Context(), owner, key, strings.NewReader("bytes"))
	r.NoError(err)
	reader, err := nas.ReadRange(t.Context(), owner, key, 0, -1)
	r.NoError(err)
	body, err := io.ReadAll(reader)
	r.NoError(err)
	r.NoError(reader.Close())
	r.Equal("bytes", string(body))
	nas.RemoveOwnerKey(owner)
	_, err = cache.ReadRange(t.Context(), owner, key, 0, -1)
	r.Error(err)
}

func TestThumbCacheWritesArtifactAndCachedThumbnail(t *testing.T) {
	r := require.New(t)
	cache, _, nasRoot, flashRoot, owner := newThumbCache(t)
	key := ".thumbs/asset/v1/grid.jpg"

	_, err := cache.Write(t.Context(), owner, key, bytes.NewReader([]byte("bytes")))
	r.NoError(err)

	nasBytes, err := os.ReadFile(filepath.Join(nasRoot, "key1", filepath.FromSlash(key)))
	r.NoError(err)
	r.Equal("bytes", string(nasBytes))
	flashBytes, err := os.ReadFile(filepath.Join(flashRoot, "key1", filepath.FromSlash(key)))
	r.NoError(err)
	r.Equal("bytes", string(flashBytes))
}

func TestThumbCacheReadPopulatesThumbnailMiss(t *testing.T) {
	r := require.New(t)
	cache, nas, _, flashRoot, owner := newThumbCache(t)
	key := ".thumbs/asset/v1/preview.jpg"
	_, err := nas.Write(t.Context(), owner, key, bytes.NewReader([]byte("bytes")))
	r.NoError(err)

	rc, err := cache.ReadRange(t.Context(), owner, key, 0, -1)
	r.NoError(err)
	got, err := io.ReadAll(rc)
	r.NoError(err)
	r.NoError(rc.Close())
	r.Equal("bytes", string(got))

	flashBytes, err := os.ReadFile(filepath.Join(flashRoot, "key1", filepath.FromSlash(key)))
	r.NoError(err)
	r.Equal("bytes", string(flashBytes))
}

func TestThumbCacheAcceptsOnlySafeThumbnailKeys(t *testing.T) {
	r := require.New(t)
	cache, _, _, _, owner := newThumbCache(t)

	for _, key := range []string{
		"original.jpg",
		"../secret.txt",
		"/etc/passwd",
		".thumbs/a//b",
		".thumbs/a/./b",
		".thumbs/a/../b",
		"",
	} {
		_, err := cache.Write(t.Context(), owner, key, bytes.NewReader([]byte("x")))
		r.ErrorIs(err, storage.ErrInvalidKey, "Write should reject key %q", key)
		_, err = cache.ReadRange(t.Context(), owner, key, 0, -1)
		r.ErrorIs(err, storage.ErrInvalidKey, "ReadRange should reject key %q", key)
		_, err = cache.Stat(t.Context(), owner, key)
		r.ErrorIs(err, storage.ErrInvalidKey, "Stat should reject key %q", key)
		r.ErrorIs(cache.Delete(t.Context(), owner, key), storage.ErrInvalidKey,
			"Delete should reject key %q", key)
	}
}
