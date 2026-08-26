package content_test

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.kenn.io/fotobank/internal/content"
	"go.kenn.io/fotobank/internal/errs"
)

func TestAdapterLifecycle(t *testing.T) {
	require := require.New(t)
	adapter, err := content.Open(t.Context(), content.Config{Root: t.TempDir()})
	require.NoError(err)
	require.NoError(adapter.Close())
	require.NoError(adapter.Close())

	_, err = adapter.Stat(t.Context(), "/missing.txt")
	require.ErrorIs(err, errs.ErrContentUnavailable)
}

func TestAdapterCreate(t *testing.T) {
	require := require.New(t)
	adapter, err := content.Open(t.Context(), content.Config{Root: t.TempDir()})
	require.NoError(err)
	t.Cleanup(func() { require.NoError(adapter.Close()) })

	payload := []byte("original photo bytes")
	expected := identityFor(payload)
	modifiedAt := time.Date(2026, time.August, 26, 12, 0, 0, 0, time.UTC)
	request := content.CreateRequest{
		VirtualPath: "/owners/owner/media/file/IMG_0001.JPG",
		MediaType:   "image/jpeg",
		Expected:    expected,
		Source: content.Source{
			Kind:        "filesystem",
			Description: "camera import",
			Reference:   "/imports/IMG_0001.JPG",
			ModifiedAt:  &modifiedAt,
		},
	}

	request.Reader = bytes.NewReader(payload)
	first, err := adapter.Create(t.Context(), request)
	require.NoError(err)
	request.Reader = bytes.NewReader(payload)
	second, err := adapter.Create(t.Context(), request)
	require.NoError(err)

	require.True(first.Created)
	require.False(second.Created)
	require.Equal(first.Node.ID, second.Node.ID)
	require.Equal(first.Version.ID, second.Version.ID)
	require.Equal(expected, first.Identity)
	require.Equal(request.VirtualPath, first.Node.VirtualPath)

	mismatch := request
	mismatch.VirtualPath = "/owners/owner/media/file/WRONG.JPG"
	mismatch.Expected.SHA256 = identityFor([]byte("different bytes")).SHA256
	mismatch.Reader = bytes.NewReader(payload)
	_, err = adapter.Create(t.Context(), mismatch)
	require.ErrorIs(err, errs.ErrContentIdentityMismatch)
	mismatch.VirtualPath = "/owners/owner/media/file/WRONG-SIZE.JPG"
	mismatch.Expected = expected
	mismatch.Expected.Size++
	mismatch.Reader = bytes.NewReader(payload)
	_, err = adapter.Create(t.Context(), mismatch)
	require.ErrorIs(err, errs.ErrContentIdentityMismatch)

	changed := []byte("changed original photo bytes")
	conflict := request
	conflict.Expected = identityFor(changed)
	conflict.Reader = bytes.NewReader(changed)
	_, err = adapter.Create(t.Context(), conflict)
	require.ErrorIs(err, errs.ErrContentConflict)
}

func identityFor(payload []byte) content.Identity {
	digest := sha256.Sum256(payload)
	return content.Identity{SHA256: fmt.Sprintf("%x", digest), Size: int64(len(payload))}
}

func TestAdapterStat(t *testing.T) {
	adapter, path, _, receipt := createTestContent(t)

	got, err := adapter.Stat(t.Context(), path)
	require.NoError(t, err)
	require.Equal(t, receipt.Node, got)

	_, err = adapter.Stat(t.Context(), "/missing.jpg")
	require.ErrorIs(t, err, errs.ErrNotFound)
}

func TestAdapterOpenCurrent(t *testing.T) {
	require := require.New(t)
	adapter, path, payload, receipt := createTestContent(t)

	got, err := adapter.OpenCurrent(t.Context(), path)
	require.NoError(err)
	require.Equal(receipt.Node.ID, got.NodeID)
	require.Equal(receipt.Version.ID, got.VersionID)
	require.Equal(receipt.Identity.SHA256, got.SHA256)
	require.Equal(receipt.Identity.Size, got.Size)
	require.Equal("image/jpeg", got.MediaType)
	bytesRead, err := io.ReadAll(got.Reader)
	require.NoError(err)
	require.Equal(payload, bytesRead)
	require.NoError(got.Reader.Verify())
	require.NoError(got.Reader.Close())

	_, err = adapter.OpenCurrent(t.Context(), "/missing.jpg")
	require.ErrorIs(err, errs.ErrNotFound)
}

func TestAdapterOpenVersion(t *testing.T) {
	require := require.New(t)
	adapter, _, payload, receipt := createTestContent(t)

	got, err := adapter.OpenVersion(t.Context(), receipt.Version.ID)
	require.NoError(err)
	require.Equal(receipt.Node.ID, got.NodeID)
	require.Equal(receipt.Version.ID, got.VersionID)
	require.Equal(receipt.Identity.SHA256, got.SHA256)
	require.Equal(receipt.Identity.Size, got.Size)
	require.Equal("image/jpeg", got.MediaType)
	bytesRead, err := io.ReadAll(got.Reader)
	require.NoError(err)
	require.Equal(payload, bytesRead)
	require.NoError(got.Reader.Verify())
	require.NoError(got.Reader.Close())

	_, err = adapter.OpenVersion(t.Context(), "01J00000000000000000000000")
	require.ErrorIs(err, errs.ErrNotFound)
}

func createTestContent(t *testing.T) (*content.Adapter, string, []byte, content.CreateReceipt) {
	t.Helper()
	adapter, err := content.Open(t.Context(), content.Config{Root: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, adapter.Close()) })
	payload := []byte("test photo bytes")
	virtualPath := "/owners/owner/media/file/IMG.JPG"
	receipt, err := adapter.Create(t.Context(), content.CreateRequest{
		VirtualPath: virtualPath,
		MediaType:   "image/jpeg",
		Expected:    identityFor(payload),
		Reader:      bytes.NewReader(payload),
	})
	require.NoError(t, err)
	return adapter, virtualPath, payload, receipt
}
