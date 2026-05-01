package imginput_test

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/ai/imginput"
	"github.com/wesm/fotobank/internal/testutil"
)

func TestResolverReturnsBytesAndStatus(t *testing.T) {
	require := require.New(t)
	rw, ro := testutil.OpenTestDBPair(t)
	dir := t.TempDir()
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	mid := testutil.SeedPhoto(t, rw, owner, "p1")

	previewPath := filepath.Join(dir, mid+".preview.jpg")
	require.NoError(os.WriteFile(previewPath, makeJPEGForResolver(t, 2560, 1700), 0o644))

	r := imginput.NewResolver(rw, ro, imginput.LocateFunc(func(id string) string {
		return filepath.Join(dir, id+".preview.jpg")
	}))

	data, status, err := r.ResolveAndEncode(context.Background(), mid)
	require.NoError(err)
	require.Equal("ready", status)
	require.NotEmpty(data)
}

func TestResolverPropagatesMissingPreview(t *testing.T) {
	rw, ro := testutil.OpenTestDBPair(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	mid := testutil.SeedPhoto(t, rw, owner, "p1")

	r := imginput.NewResolver(rw, ro, imginput.LocateFunc(func(id string) string {
		return "/nonexistent/" + id
	}))
	_, status, err := r.ResolveAndEncode(context.Background(), mid)
	require.Error(t, err)
	require.Equal(t, "ready", status)
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
