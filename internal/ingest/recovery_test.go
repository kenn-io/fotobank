package ingest_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/content"
	"go.kenn.io/fotobank/internal/ingest"
	"go.kenn.io/fotobank/internal/media"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/testutil"
)

func TestRecoverOwnerAdoptsCreateAfterVaultRestart(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	owner := testutil.SeedOwner(t, d.WriteDB(), "local", "alice")
	storageKey := ownerStorageKey(t, d.ReadDB(), owner)
	assets := media.NewAssetRepo(d.WriteDB(), d.ReadDB())
	body := []byte("photo bytes written before the process stopped")
	pending := reserveRecoveryAsset(t, assets, owner, storageKey, body, "photo.jpg")

	vaultRoot := t.TempDir()
	first, err := content.Open(t.Context(), content.Config{Root: vaultRoot})
	r.NoError(err)
	receipt, err := first.Create(t.Context(), content.CreateRequest{
		VirtualPath: pending.VirtualPath,
		MediaType:   pending.File.MimeType,
		Expected:    content.Identity{SHA256: pending.SHA256, Size: pending.Size},
		Reader:      bytes.NewReader(body),
	})
	r.NoError(err)
	r.NoError(first.Close())

	reopened, err := content.Open(t.Context(), content.Config{Root: vaultRoot})
	r.NoError(err)
	t.Cleanup(func() { r.NoError(reopened.Close()) })
	recoverer := ingest.NewImporter(
		reopened,
		assets,
		media.NewRepo(d.WriteDB(), d.ReadDB()),
		storageKey,
		nil,
	)
	aiCalls := &countingAIEnqueuer{}
	recoverer.SetAIEnqueuer(aiCalls)
	result, err := recoverer.RecoverOwner(t.Context(), owner)
	r.NoError(err)
	r.Equal(1, result.Adopted)
	r.Equal(1, result.Finalized)
	r.Zero(result.Pending)
	r.Zero(result.Conflicts)
	r.Empty(result.Orphans)

	asset, err := assets.GetAsset(t.Context(), pending.File.AssetID)
	r.NoError(err)
	r.Equal(media.AssetReady, asset.State)
	r.Equal(receipt.Version.ID, asset.SourceMetadataVersionID)
	r.Len(asset.SourceMetadataExtractorFingerprint, sha256.Size*2)
	r.Len(asset.SourceMetadataChecksum, sha256.Size*2)
	file, err := assets.GetFile(t.Context(), pending.File.ID)
	r.NoError(err)
	r.Equal(receipt.Node.ID, *file.DocbankNodeID)
	r.Equal(receipt.Version.ID, file.CurrentVersionID)
	r.Equal(1, aiCalls.photos)

	result, err = recoverer.RecoverOwner(t.Context(), owner)
	r.NoError(err)
	r.Zero(result.Finalized)
	r.Equal(1, aiCalls.photos, "an already-ready asset must not be rescheduled")
}

func TestRecoverOwnerLeavesMissingCreatePending(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	owner := testutil.SeedOwner(t, d.WriteDB(), "local", "alice")
	storageKey := ownerStorageKey(t, d.ReadDB(), owner)
	assets := media.NewAssetRepo(d.WriteDB(), d.ReadDB())
	pending := reserveRecoveryAsset(t, assets, owner, storageKey, []byte("not copied yet"), "pending.jpg")
	adapter, err := content.Open(t.Context(), content.Config{Root: t.TempDir()})
	r.NoError(err)
	t.Cleanup(func() { r.NoError(adapter.Close()) })

	result, err := ingest.NewImporter(
		adapter, assets, media.NewRepo(d.WriteDB(), d.ReadDB()), storageKey, nil,
	).RecoverOwner(t.Context(), owner)
	r.NoError(err)
	r.Zero(result.Adopted)
	r.Zero(result.Finalized)
	r.Equal(1, result.Pending)
	r.Empty(result.Orphans)
	asset, err := assets.GetAsset(t.Context(), pending.File.AssetID)
	r.NoError(err)
	r.Equal(media.AssetPending, asset.State)
}

func TestRecoverOwnerTerminalizesMismatchedAuthority(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	owner := testutil.SeedOwner(t, d.WriteDB(), "local", "alice")
	storageKey := ownerStorageKey(t, d.ReadDB(), owner)
	assets := media.NewAssetRepo(d.WriteDB(), d.ReadDB())
	pending := reserveRecoveryAsset(t, assets, owner, storageKey, []byte("expected"), "conflict.jpg")
	adapter, err := content.Open(t.Context(), content.Config{Root: t.TempDir()})
	r.NoError(err)
	t.Cleanup(func() { r.NoError(adapter.Close()) })
	other := []byte("different authority")
	_, err = adapter.Create(t.Context(), content.CreateRequest{
		VirtualPath: pending.VirtualPath,
		MediaType:   pending.File.MimeType,
		Expected:    recoveryIdentity(other),
		Reader:      bytes.NewReader(other),
	})
	r.NoError(err)

	result, err := ingest.NewImporter(
		adapter, assets, media.NewRepo(d.WriteDB(), d.ReadDB()), storageKey, nil,
	).RecoverOwner(t.Context(), owner)
	r.NoError(err)
	r.Equal(1, result.Conflicts)
	r.Zero(result.Finalized)
	r.Empty(result.Orphans, "the ledger still owns the conflicting path")
	asset, err := assets.GetAsset(t.Context(), pending.File.AssetID)
	r.NoError(err)
	r.Equal(media.AssetConflict, asset.State)
}

func TestRecoverOwnerReportsUnmatchedDocbankFiles(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	owner := testutil.SeedOwner(t, d.WriteDB(), "local", "alice")
	storageKey := ownerStorageKey(t, d.ReadDB(), owner)
	assets := media.NewAssetRepo(d.WriteDB(), d.ReadDB())
	adapter, err := content.Open(t.Context(), content.Config{Root: t.TempDir()})
	r.NoError(err)
	t.Cleanup(func() { r.NoError(adapter.Close()) })
	orphanPath, err := content.VirtualPath(storageKey, uuid.NewString(), "orphan.jpg")
	r.NoError(err)
	body := []byte("unmatched authority")
	receipt, err := adapter.Create(t.Context(), content.CreateRequest{
		VirtualPath: orphanPath,
		MediaType:   "image/jpeg",
		Expected:    recoveryIdentity(body),
		Reader:      bytes.NewReader(body),
	})
	r.NoError(err)

	result, err := ingest.NewImporter(
		adapter, assets, media.NewRepo(d.WriteDB(), d.ReadDB()), storageKey, nil,
	).RecoverOwner(t.Context(), owner)
	r.NoError(err)
	r.Len(result.Orphans, 1)
	r.Equal(orphanPath, result.Orphans[0].VirtualPath)
	r.Equal(receipt.Node.ID, result.Orphans[0].ID)

	_, err = adapter.Stat(t.Context(), orphanPath)
	r.NoError(err, "reporting an orphan must not alter Docbank authority")
}

func reserveRecoveryAsset(
	t *testing.T,
	assets *media.AssetRepo,
	owner owners.Principal,
	storageKey string,
	body []byte,
	filename string,
) media.PendingContent {
	t.Helper()
	assetID := uuid.NewString()
	fileID := uuid.NewString()
	virtualPath, err := content.VirtualPath(storageKey, fileID, filename)
	require.NoError(t, err)
	pending := media.PendingContent{
		OperationID: uuid.NewString(),
		File: media.File{
			ID: fileID, AssetID: assetID, Owner: owner, Role: media.RolePrimary,
			MimeType: "image/jpeg", OriginalFilename: filename,
			ImportSourcePath: filename, Size: int64(len(body)),
		},
		SHA256: recoveryIdentity(body).SHA256,
		Size:   int64(len(body)), VirtualPath: virtualPath,
	}
	require.NoError(t, assets.ReserveImport(t.Context(), media.Asset{
		ID: assetID, Owner: owner, State: media.AssetPending, Type: media.TypePhoto,
		ImportedAt: time.Now().UTC(), ThumbStatus: "pending",
	}, []media.PendingContent{pending}, nil))
	return pending
}

func recoveryIdentity(body []byte) content.Identity {
	digest := sha256.Sum256(body)
	return content.Identity{SHA256: fmt.Sprintf("%x", digest), Size: int64(len(body))}
}

func ownerStorageKey(t *testing.T, db *sql.DB, owner owners.Principal) string {
	t.Helper()
	var storageKey string
	require.NoError(t, db.QueryRowContext(t.Context(),
		`SELECT storage_key FROM owners WHERE hub = ? AND user_id = ?`,
		owner.Hub, owner.UserID,
	).Scan(&storageKey))
	return storageKey
}

type countingAIEnqueuer struct {
	photos int
	videos int
}

func (c *countingAIEnqueuer) EnqueueForPhoto(_ context.Context, _ string) error {
	c.photos++
	return nil
}

func (c *countingAIEnqueuer) RecordVideoSkip(_ context.Context, _ string) error {
	c.videos++
	return nil
}
