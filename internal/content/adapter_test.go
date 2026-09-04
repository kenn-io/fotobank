package content_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.kenn.io/fotobank/internal/content"
	"go.kenn.io/fotobank/internal/errs"
)

func TestAdapterBackupRoundTrip(t *testing.T) {
	r := require.New(t)
	adapter, err := content.Open(t.Context(), content.Config{Root: filepath.Join(t.TempDir(), "live")})
	r.NoError(err)
	t.Cleanup(func() { r.NoError(adapter.Close()) })

	body := []byte("recoverable photo bytes\n")
	receipt, err := adapter.Create(t.Context(), content.CreateRequest{
		VirtualPath: "/owners/owner/media/file/photo.jpg",
		Reader:      bytes.NewReader(body),
		Expected:    identityFor(body),
		MediaType:   "image/jpeg",
	})
	r.NoError(err)

	repositoryRoot := filepath.Join(t.TempDir(), "repository")
	repository, err := content.InitBackupRepository(repositoryRoot)
	r.NoError(err)
	r.NotEmpty(repository.ID())
	r.Equal(repositoryRoot, repository.Root())
	prepared := 0
	snapshot, err := adapter.CreateBackup(t.Context(), repository, content.BackupOptions{
		Tag: "fotobank-test",
		Prepare: func(context.Context) error {
			prepared++
			return nil
		},
	})
	r.NoError(err)
	r.Equal(1, prepared)
	r.NotEmpty(snapshot.ID)
	r.Equal("fotobank-test", snapshot.Tag)

	snapshots, err := repository.Snapshots()
	r.NoError(err)
	r.Len(snapshots, 1)
	r.Equal(snapshot.ID, snapshots[0].ID)
	reopened, err := content.OpenBackupRepository(repositoryRoot)
	r.NoError(err)
	r.Equal(repository.ID(), reopened.ID())
	verified, err := reopened.Verify(t.Context(), content.BackupVerifyOptions{SnapshotID: snapshot.ID})
	r.NoError(err)
	r.Equal([]string{snapshot.ID}, verified.Snapshots)
	r.Empty(verified.Problems)

	restoredRoot := filepath.Join(t.TempDir(), "restored")
	restored, err := adapter.RestoreBackup(t.Context(), reopened, content.BackupRestoreOptions{
		SnapshotID: snapshot.ID,
		Target:     restoredRoot,
	})
	r.NoError(err)
	r.Equal(snapshot.ID, restored.SnapshotID)
	r.True(restored.ContentVerified)
	r.True(restored.CatalogIntegrityVerified)

	restoredAdapter, err := content.Open(t.Context(), content.Config{Root: restoredRoot})
	r.NoError(err)
	t.Cleanup(func() { r.NoError(restoredAdapter.Close()) })
	opened, err := restoredAdapter.OpenVersion(t.Context(), receipt.Version.ID)
	r.NoError(err)
	got, err := io.ReadAll(opened.Reader)
	r.NoError(err)
	r.NoError(opened.Reader.Verify())
	r.NoError(opened.Reader.Close())
	r.Equal(body, got)
}

func TestAdapterBackupReleasesContentWritesAfterDocbankPinsSnapshot(t *testing.T) {
	r := require.New(t)
	adapter, err := content.Open(t.Context(), content.Config{Root: filepath.Join(t.TempDir(), "live")})
	r.NoError(err)
	t.Cleanup(func() { r.NoError(adapter.Close()) })
	before := []byte("before\n")
	_, err = adapter.Create(t.Context(), content.CreateRequest{
		VirtualPath: "/owners/owner/media/file/before.jpg",
		Reader:      bytes.NewReader(before),
		Expected:    identityFor(before),
		MediaType:   "image/jpeg",
	})
	r.NoError(err)
	repository, err := content.InitBackupRepository(filepath.Join(t.TempDir(), "repository"))
	r.NoError(err)

	prepareEntered := make(chan struct{})
	allowPrepare := make(chan struct{})
	pinned := make(chan struct{})
	resume := make(chan struct{})
	var pinnedOnce sync.Once
	backupDone := make(chan error, 1)
	go func() {
		_, backupErr := adapter.CreateBackup(t.Context(), repository, content.BackupOptions{
			Prepare: func(context.Context) error {
				close(prepareEntered)
				<-allowPrepare
				return nil
			},
			Progress: func(progress content.BackupProgress) {
				if progress.Stage == "freeze" && progress.Final {
					pinnedOnce.Do(func() { close(pinned) })
					<-resume
				}
			},
		})
		backupDone <- backupErr
	}()
	select {
	case <-prepareEntered:
	case <-time.After(5 * time.Second):
		r.FailNow("backup did not enter coordinated preparation")
	}

	after := []byte("after\n")
	writeDone := make(chan error, 1)
	go func() {
		_, createErr := adapter.Create(t.Context(), content.CreateRequest{
			VirtualPath: "/owners/owner/media/file/after.jpg",
			Reader:      bytes.NewReader(after),
			Expected:    identityFor(after),
			MediaType:   "image/jpeg",
		})
		writeDone <- createErr
	}()
	select {
	case err := <-writeDone:
		close(allowPrepare)
		close(resume)
		r.NoError(err)
		r.FailNow("content write completed while coordinated preparation was running")
	case <-time.After(100 * time.Millisecond):
	}
	close(allowPrepare)
	select {
	case <-pinned:
	case <-time.After(5 * time.Second):
		close(resume)
		r.FailNow("backup did not pin its Docbank snapshot")
	}
	select {
	case err := <-writeDone:
		r.NoError(err)
	case <-time.After(5 * time.Second):
		close(resume)
		r.FailNow("content write remained blocked after Docbank pinned the backup")
	}
	close(resume)
	r.NoError(<-backupDone)
}

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

func TestAdapterReplaceRequiresAndRecoversFromExactBase(t *testing.T) {
	r := require.New(t)
	adapter, err := content.Open(t.Context(), content.Config{Root: t.TempDir()})
	r.NoError(err)
	t.Cleanup(func() { r.NoError(adapter.Close()) })

	original := []byte("original photo bytes")
	created, err := adapter.Create(t.Context(), content.CreateRequest{
		VirtualPath: "/owners/owner/media/file/IMG_0001.JPG",
		MediaType:   "image/jpeg",
		Expected:    identityFor(original),
		Reader:      bytes.NewReader(original),
	})
	r.NoError(err)
	replacement := []byte("edited photo bytes")
	request := content.ReplaceRequest{
		VirtualPath: created.Node.VirtualPath,
		NodeID:      created.Node.ID, BaseVersionID: created.Version.ID,
		Base: created.Identity, MediaType: "image/jpeg",
		Expected: identityFor(replacement), Reader: bytes.NewReader(replacement),
	}
	replaced, err := adapter.Replace(t.Context(), request)
	r.NoError(err)
	r.False(replaced.Adopted)
	r.NotEqual(created.Version.ID, replaced.Version.ID)

	request.Reader = bytes.NewReader(replacement)
	recovered, err := adapter.Replace(t.Context(), request)
	r.NoError(err)
	r.True(recovered.Adopted)
	r.Equal(replaced.Version.ID, recovered.Version.ID)

	request.Expected = identityFor([]byte("another edit"))
	request.Reader = bytes.NewReader([]byte("another edit"))
	_, err = adapter.Replace(t.Context(), request)
	r.ErrorIs(err, errs.ErrContentConflict)

	current, err := adapter.Stat(t.Context(), created.Node.VirtualPath)
	r.NoError(err)
	r.Equal(replaced.Version.ID, current.CurrentVersionID)
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

func TestAdapterEnsureSourceMetadataUsesExactVersion(t *testing.T) {
	r := require.New(t)
	adapter, err := content.Open(t.Context(), content.Config{Root: t.TempDir()})
	r.NoError(err)
	t.Cleanup(func() { r.NoError(adapter.Close()) })
	payload, err := os.ReadFile(filepath.Join("..", "..", "testdata", "exif", "photo-with-timestamp.jpg"))
	r.NoError(err)
	receipt, err := adapter.Create(t.Context(), content.CreateRequest{
		VirtualPath: "/owners/owner/media/file/photo.jpg",
		MediaType:   "image/jpeg",
		Expected:    identityFor(payload),
		Reader:      bytes.NewReader(payload),
	})
	r.NoError(err)

	metadata, err := adapter.EnsureSourceMetadata(t.Context(), receipt.Version.ID)
	r.NoError(err)
	r.Equal(receipt.Version.ID, metadata.VersionID)
	r.Len(metadata.ExtractorFingerprint, sha256.Size*2)
	r.Len(metadata.Checksum, sha256.Size*2)
	r.Equal("Canon", metadata.Fields["image.exif.camera_make"].String)
	r.Equal("timestamp", metadata.Fields["created"].Kind)
	r.Equal("2024-06-15T14:30:22", metadata.Fields["created"].Timestamp.Normalized)
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
