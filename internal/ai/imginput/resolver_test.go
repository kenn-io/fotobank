package imginput_test

import (
	"bytes"
	"context"
	"database/sql"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"testing"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/ai/imginput"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/storage"
	"go.kenn.io/fotobank/internal/testutil"
	"go.kenn.io/fotobank/internal/thumb"
)

func TestResolverFollowsThumbVersionBump(t *testing.T) {
	require := require.New(t)
	rw, ro := testutil.OpenTestDBPair(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	mid := testutil.SeedPhoto(t, rw, owner, "p1")

	// v1 is wide-landscape (3:2) and v2 is portrait-tall (1:2). Decoding
	// the returned preview and inspecting bounds unambiguously
	// distinguishes which version the resolver read; without the
	// version-aware fix, the resolver would still load v1 and the bounds
	// check fails.
	store, _ := newResolverStoreWithPreview(t, rw, owner, mid, makeJPEGForResolver(t, 2400, 1600))

	_, err := rw.ExecContext(context.Background(),
		`UPDATE media SET thumb_version=2 WHERE id=?`, mid)
	require.NoError(err)
	writePreview(t, store, owner, mid, 2, makeJPEGForResolver(t, 800, 1600))

	r := imginput.NewResolver(ro, store)
	data, status, err := r.ResolvePreviewJPEG(context.Background(), mid)
	require.NoError(err)
	require.Equal("ready", status)
	require.NotEmpty(data)

	decoded, err := jpeg.Decode(bytes.NewReader(data))
	require.NoError(err)
	b := decoded.Bounds()
	require.Lessf(b.Dx(), b.Dy(), "v2 preview is portrait; resolver returned landscape (likely read stale v1): %dx%d", b.Dx(), b.Dy())
}

func TestResolver_ResolvePreviewJPEG_ReturnsRawBytes(t *testing.T) {
	require := require.New(t)
	rw, ro := testutil.OpenTestDBPair(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	mid := testutil.SeedPhoto(t, rw, owner, "p1")

	srcJPEG := makeJPEGForResolver(t, 2560, 1700)
	store, version := newResolverStoreWithPreview(t, rw, owner, mid, srcJPEG)
	require.Equal(1, version)

	r := imginput.NewResolver(ro, store)
	jpg, status, err := r.ResolvePreviewJPEG(context.Background(), mid)
	require.NoError(err)
	require.Equal("ready", status)

	// Bytes match the on-disk preview verbatim — no re-encode.
	key := thumb.ThumbKey(mid, version, thumb.SizePreview)
	rc, err := store.ReadRange(context.Background(), owner, key, 0, -1)
	require.NoError(err)
	defer func() { _ = rc.Close() }()
	expected, err := io.ReadAll(rc)
	require.NoError(err)
	require.True(bytes.Equal(expected, jpg))
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
	_, status, err := r.ResolvePreviewJPEG(context.Background(), mid)
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

// makeJPEGForResolver builds a synthetic JPEG of the given dimensions.
// Mirrors the helper in encode/encode_test.go; keeping a local copy
// avoids cross-package test deps for the resolver suite.
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
