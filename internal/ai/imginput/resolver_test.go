package imginput_test

import (
	"bytes"
	"context"
	"database/sql"
	"image"
	"image/color"
	"image/jpeg"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/ai/imginput"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/storage"
	"github.com/wesm/fotobank/internal/testutil"
	"github.com/wesm/fotobank/internal/thumb"
)

func TestResolverReturnsBytesAndStatus(t *testing.T) {
	require := require.New(t)
	rw, ro := testutil.OpenTestDBPair(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	mid := testutil.SeedPhoto(t, rw, owner, "p1")

	store, version := newResolverStoreWithPreview(t, rw, owner, mid, makeJPEGForResolver(t, 2560, 1700))
	require.Equal(1, version)

	r := imginput.NewResolver(ro, store)
	data, status, err := r.ResolveAndEncode(context.Background(), mid)
	require.NoError(err)
	require.Equal("ready", status)
	require.NotEmpty(data)
}

func TestResolverFollowsThumbVersionBump(t *testing.T) {
	require := require.New(t)
	rw, ro := testutil.OpenTestDBPair(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	mid := testutil.SeedPhoto(t, rw, owner, "p1")

	store, _ := newResolverStoreWithPreview(t, rw, owner, mid, makeJPEGForResolver(t, 2560, 1700))

	// Bump version on the row and write a fresh preview at the new key.
	// The old key (v1) stays on disk; the resolver must read v2.
	_, err := rw.ExecContext(context.Background(),
		`UPDATE media SET thumb_version=2 WHERE id=?`, mid)
	require.NoError(err)
	writePreview(t, store, owner, mid, 2, makeJPEGForResolver(t, 1280, 800))

	r := imginput.NewResolver(ro, store)
	data, status, err := r.ResolveAndEncode(context.Background(), mid)
	require.NoError(err)
	require.Equal("ready", status)
	require.NotEmpty(data)
}

func TestResolverPropagatesMissingPreview(t *testing.T) {
	rw, ro := testutil.OpenTestDBPair(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	mid := testutil.SeedPhoto(t, rw, owner, "p1")

	// Set up a NAS store that knows about the owner but has no blob written.
	store := storage.NewNASOnly(t.TempDir(), map[owners.Principal]string{
		owner: "alice",
	})

	r := imginput.NewResolver(ro, store)
	_, status, err := r.ResolveAndEncode(context.Background(), mid)
	require.Error(t, err)
	require.Equal(t, "ready", status)
}

// newResolverStoreWithPreview creates a NAS-only Store rooted at a temp
// dir and writes a JPEG at the versioned preview key for the seeded
// photo. Returns the store and the row's current thumb_version.
func newResolverStoreWithPreview(t *testing.T, rw *sql.DB, owner owners.Principal, mid string, jpg []byte) (storage.Store, int) {
	t.Helper()
	store := storage.NewNASOnly(t.TempDir(), map[owners.Principal]string{
		owner: "alice",
	})
	var version int
	require.NoError(t, rw.QueryRowContext(context.Background(),
		`SELECT thumb_version FROM media WHERE id=?`, mid).Scan(&version))
	writePreview(t, store, owner, mid, version, jpg)
	return store, version
}

func writePreview(t *testing.T, store storage.Store, owner owners.Principal, mid string, version int, jpg []byte) {
	t.Helper()
	key := thumb.ThumbKey(mid, version, thumb.SizePreview)
	_, err := store.Write(context.Background(), owner, key, bytes.NewReader(jpg))
	require.NoError(t, err)
}

// makeJPEGForResolver is a local copy of the helper in encode_test.go;
// keeping resolver_test.go self-contained avoids cross-file test deps.
func makeJPEGForResolver(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 0x40, A: 0xff})
		}
	}
	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}))
	return buf.Bytes()
}
