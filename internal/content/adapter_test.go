package content_test

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
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

func TestAdapterManagedRootCreationPolicy(t *testing.T) {
	t.Run("external root must exist", func(t *testing.T) {
		r := require.New(t)
		managedRoot := filepath.Join(t.TempDir(), "nas")
		_, err := content.Open(t.Context(), content.Config{
			Root: t.TempDir(), ManagedRoots: []content.ManagedRoot{{Path: managedRoot}},
		})
		r.ErrorIs(err, errs.ErrBadConfiguration)
		r.NoDirExists(managedRoot)
	})

	t.Run("local root may be created", func(t *testing.T) {
		r := require.New(t)
		managedRoot := filepath.Join(t.TempDir(), "flash")
		adapter, err := content.Open(t.Context(), content.Config{
			Root: t.TempDir(), ManagedRoots: []content.ManagedRoot{{
				Path: managedRoot, CreateIfMissing: true,
			}},
		})
		r.NoError(err)
		t.Cleanup(func() { r.NoError(adapter.Close()) })
		r.DirExists(managedRoot)
	})

	t.Run("external root availability may be deferred", func(t *testing.T) {
		r := require.New(t)
		managedRoot := filepath.Join(t.TempDir(), "nas")
		adapter, err := content.Open(t.Context(), content.Config{
			Root: t.TempDir(), ManagedRoots: []content.ManagedRoot{{
				Path: managedRoot, AllowUnavailable: true,
			}},
		})
		r.NoError(err)
		t.Cleanup(func() { r.NoError(adapter.Close()) })
		r.NoDirExists(managedRoot)

		checkoutRoot := t.TempDir()
		_, err = adapter.ResolveCheckoutRoot(checkoutRoot)
		r.ErrorIs(err, errs.ErrBadConfiguration)

		r.NoError(os.Mkdir(managedRoot, 0o700))
		validated, err := adapter.ResolveCheckoutRoot(checkoutRoot)
		r.NoError(err)
		r.NoError(validated.Close())
	})
}

func TestAdapterResolveImportRootReturnsSymlinkTarget(t *testing.T) {
	r := require.New(t)
	adapter, err := content.Open(t.Context(), content.Config{Root: t.TempDir()})
	r.NoError(err)
	t.Cleanup(func() { r.NoError(adapter.Close()) })
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "import-link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}

	resolved, err := adapter.ResolveImportRoot(link)
	r.NoError(err)
	want, err := filepath.EvalSymlinks(target)
	r.NoError(err)
	want, err = filepath.Abs(want)
	r.NoError(err)
	r.Equal(filepath.Clean(want), resolved)
}

func TestAdapterResolveImportRootRejectsManagedStorageAlias(t *testing.T) {
	r := require.New(t)
	managedRoot := t.TempDir()
	adapter, err := content.Open(t.Context(), content.Config{
		Root: t.TempDir(), ManagedRoots: []content.ManagedRoot{{Path: managedRoot}},
	})
	r.NoError(err)
	t.Cleanup(func() { r.NoError(adapter.Close()) })
	alias := filepath.Join(t.TempDir(), "managed-link")
	if err := os.Symlink(managedRoot, alias); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}

	_, err = adapter.ResolveImportRoot(alias)
	r.ErrorIs(err, errs.ErrBadConfiguration)
}

func TestAdapterResolveCheckoutRootRejectsManagedStorageAlias(t *testing.T) {
	r := require.New(t)
	managedRoot := t.TempDir()
	adapter, err := content.Open(t.Context(), content.Config{
		Root: t.TempDir(), ManagedRoots: []content.ManagedRoot{{Path: managedRoot}},
	})
	r.NoError(err)
	t.Cleanup(func() { r.NoError(adapter.Close()) })
	alias := filepath.Join(t.TempDir(), "checkout-link")
	if err := os.Symlink(managedRoot, alias); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}

	_, err = adapter.ResolveCheckoutRoot(alias)
	r.ErrorIs(err, errs.ErrBadConfiguration)
}

func TestAdapterResolveCheckoutRootRejectsPathReplacementBeforeUse(t *testing.T) {
	r := require.New(t)
	managedRoot := t.TempDir()
	adapter, err := content.Open(t.Context(), content.Config{
		Root: t.TempDir(), ManagedRoots: []content.ManagedRoot{{Path: managedRoot}},
	})
	r.NoError(err)
	t.Cleanup(func() { r.NoError(adapter.Close()) })
	parent := t.TempDir()
	checkoutRoot := filepath.Join(parent, "checkout")
	r.NoError(os.Mkdir(checkoutRoot, 0o755))

	validated, err := adapter.ResolveCheckoutRoot(checkoutRoot)
	r.NoError(err)
	t.Cleanup(func() { r.NoError(validated.Close()) })
	movedRoot := filepath.Join(managedRoot, "validated-checkout")
	if err := os.Rename(checkoutRoot, movedRoot); err != nil {
		t.Skipf("renaming an opened directory is unavailable: %v", err)
	}
	if err := os.Symlink(movedRoot, checkoutRoot); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}

	_, err = validated.Take()
	r.ErrorIs(err, errs.ErrBadConfiguration)
}

func TestCheckoutRootRevalidateRejectsMoveAfterTake(t *testing.T) {
	r := require.New(t)
	managedRoot := t.TempDir()
	adapter, err := content.Open(t.Context(), content.Config{
		Root: t.TempDir(), ManagedRoots: []content.ManagedRoot{{Path: managedRoot}},
	})
	r.NoError(err)
	t.Cleanup(func() { r.NoError(adapter.Close()) })
	parent := t.TempDir()
	checkoutRoot := filepath.Join(parent, "checkout")
	r.NoError(os.Mkdir(checkoutRoot, 0o755))
	validated, err := adapter.ResolveCheckoutRoot(checkoutRoot)
	r.NoError(err)
	t.Cleanup(func() { r.NoError(validated.Close()) })
	workingRoot, err := validated.Take()
	r.NoError(err)
	t.Cleanup(func() { r.NoError(workingRoot.Close()) })

	movedRoot := filepath.Join(managedRoot, "checkout")
	if err := os.Rename(checkoutRoot, movedRoot); err != nil {
		t.Skipf("renaming an opened directory is unavailable: %v", err)
	}
	if err := os.Symlink(movedRoot, checkoutRoot); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}

	r.ErrorIs(validated.Revalidate(), errs.ErrBadConfiguration)
}

func TestCheckoutRootRevalidateRejectsAliasToMovedDirectory(t *testing.T) {
	r := require.New(t)
	adapter, err := content.Open(t.Context(), content.Config{Root: t.TempDir()})
	r.NoError(err)
	t.Cleanup(func() { r.NoError(adapter.Close()) })
	parent := t.TempDir()
	checkoutRoot := filepath.Join(parent, "checkout")
	r.NoError(os.Mkdir(checkoutRoot, 0o755))
	validated, err := adapter.ResolveCheckoutRoot(checkoutRoot)
	r.NoError(err)
	t.Cleanup(func() { r.NoError(validated.Close()) })
	workingRoot, err := validated.Take()
	r.NoError(err)
	t.Cleanup(func() { r.NoError(workingRoot.Close()) })

	movedRoot := filepath.Join(parent, "moved-checkout")
	if err := os.Rename(checkoutRoot, movedRoot); err != nil {
		t.Skipf("renaming an opened directory is unavailable: %v", err)
	}
	if err := os.Symlink(movedRoot, checkoutRoot); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}

	r.ErrorIs(validated.Revalidate(), errs.ErrBadConfiguration)
}

func TestCheckoutRootRevalidateRejectsMissingManagedRoot(t *testing.T) {
	r := require.New(t)
	managedRoot := filepath.Join(t.TempDir(), "managed")
	r.NoError(os.Mkdir(managedRoot, 0o700))
	adapter, err := content.Open(t.Context(), content.Config{
		Root: t.TempDir(), ManagedRoots: []content.ManagedRoot{{Path: managedRoot}},
	})
	r.NoError(err)
	t.Cleanup(func() { r.NoError(adapter.Close()) })
	checkoutRoot := t.TempDir()
	validated, err := adapter.ResolveCheckoutRoot(checkoutRoot)
	r.NoError(err)
	t.Cleanup(func() { r.NoError(validated.Close()) })
	workingRoot, err := validated.Take()
	r.NoError(err)
	t.Cleanup(func() { r.NoError(workingRoot.Close()) })
	r.NoError(os.Rename(managedRoot, managedRoot+"-moved"))

	r.ErrorIs(validated.Revalidate(), errs.ErrBadConfiguration)
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

func TestAdapterOpenVersionRange(t *testing.T) {
	require := require.New(t)
	adapter, _, payload, receipt := createTestContent(t)

	got, err := adapter.OpenVersionRange(t.Context(), receipt.Version.ID, 2, 5)
	require.NoError(err)
	require.Equal(receipt.Node.ID, got.NodeID)
	require.Equal(receipt.Version.ID, got.VersionID)
	require.Equal(receipt.Identity.SHA256, got.SHA256)
	require.Equal(int64(2), got.Offset)
	require.Equal(int64(5), got.Length)
	bytesRead, err := io.ReadAll(got.Reader)
	require.NoError(err)
	require.Equal(payload[2:7], bytesRead)
	require.NoError(got.Reader.Close())
}

func TestAdapterTranslatesReaderErrors(t *testing.T) {
	require := require.New(t)
	root := t.TempDir()
	adapter, err := content.Open(t.Context(), content.Config{Root: root})
	require.NoError(err)
	t.Cleanup(func() { require.NoError(adapter.Close()) })

	payload := []byte("test photo bytes")
	virtualPath := "/owners/owner/media/file/IMG.JPG"
	receipt, err := adapter.Create(t.Context(), content.CreateRequest{
		VirtualPath: virtualPath,
		MediaType:   "image/jpeg",
		Expected:    identityFor(payload),
		Reader:      bytes.NewReader(payload),
	})
	require.NoError(err)
	blobPath := filepath.Join(root, "blobs", receipt.Identity.SHA256[:2], receipt.Identity.SHA256)
	require.NoError(os.WriteFile(blobPath, bytes.Repeat([]byte("x"), len(payload)), 0o600))

	current, err := adapter.OpenCurrent(t.Context(), virtualPath)
	require.NoError(err)
	_, readErr := io.ReadAll(current.Reader)
	readCloseErr := current.Reader.Close()
	require.ErrorIs(readErr, errs.ErrContentUnavailable)
	require.ErrorIs(readCloseErr, errs.ErrContentUnavailable)

	version, err := adapter.OpenVersion(t.Context(), receipt.Version.ID)
	require.NoError(err)
	verifyErr := version.Reader.Verify()
	verifyCloseErr := version.Reader.Close()
	require.ErrorIs(verifyErr, errs.ErrContentUnavailable)
	require.ErrorIs(verifyCloseErr, errs.ErrContentUnavailable)

	incomplete, err := adapter.OpenCurrent(t.Context(), virtualPath)
	require.NoError(err)
	require.ErrorIs(incomplete.Reader.Close(), errs.ErrContentUnavailable)
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
