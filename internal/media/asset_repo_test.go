package media_test

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/content"
	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/media"
	"go.kenn.io/fotobank/internal/testutil"
)

func TestContentConflictTerminalizesWholeAssetGraph(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	owner := testutil.SeedOwner(t, d.WriteDB(), "local", "alice")
	var storageKey string
	r.NoError(d.ReadDB().QueryRowContext(t.Context(),
		`SELECT storage_key FROM owners WHERE hub=? AND user_id=?`, owner.Hub, owner.UserID,
	).Scan(&storageKey))

	assetID := uuid.NewString()
	makePending := func(name, body string, role media.FileRole) media.PendingContent {
		fileID := uuid.NewString()
		path, err := content.VirtualPath(storageKey, fileID, name)
		r.NoError(err)
		digest := sha256.Sum256([]byte(body))
		return media.PendingContent{
			OperationID: uuid.NewString(),
			File: media.File{
				ID: fileID, AssetID: assetID, Owner: owner, Role: role,
				MimeType: "image/jpeg", OriginalFilename: name, Size: int64(len(body)),
			},
			SHA256: hex.EncodeToString(digest[:]), Size: int64(len(body)), VirtualPath: path,
		}
	}
	primary := makePending("photo.jpg", "primary", media.RolePrimary)
	original := makePending("photo.raw", "original", media.RoleOriginal)
	repo := media.NewAssetRepo(d.WriteDB(), d.ReadDB())
	r.NoError(repo.ReserveImport(t.Context(), media.Asset{
		ID: assetID, Owner: owner, State: media.AssetPending, Type: media.TypePhoto,
		ImportedAt: time.Now().UTC(), ThumbStatus: "pending",
	}, []media.PendingContent{primary, original}, []media.FileRelationship{{
		SourceFileID: original.File.ID, TargetFileID: primary.File.ID, Kind: media.PairedWith,
	}}))

	r.NoError(repo.ApplyContentReceipt(t.Context(), media.ContentReceipt{
		OperationID: primary.OperationID, NodeID: 1, VersionID: uuid.NewString(),
		SHA256: primary.SHA256, Size: primary.Size,
	}))
	conflicted, err := repo.MarkContentConflict(t.Context(), assetID, errs.ErrContentConflict)
	r.NoError(err)
	r.True(conflicted)

	asset, err := repo.GetAsset(t.Context(), assetID)
	r.NoError(err)
	r.Equal(media.AssetConflict, asset.State)
	rows, err := d.ReadDB().QueryContext(t.Context(),
		`SELECT status FROM content_operations WHERE asset_id=? ORDER BY id`, assetID)
	r.NoError(err)
	defer rows.Close()
	var statuses []string
	for rows.Next() {
		var status string
		r.NoError(rows.Scan(&status))
		statuses = append(statuses, status)
	}
	r.NoError(rows.Err())
	r.Equal([]string{"conflict", "conflict"}, statuses)

	err = repo.ApplyContentReceipt(t.Context(), media.ContentReceipt{
		OperationID: original.OperationID, NodeID: 2, VersionID: uuid.NewString(),
		SHA256: original.SHA256, Size: original.Size,
	})
	r.ErrorIs(err, errs.ErrInvalidArgument)
	r.ErrorIs(repo.FinalizeReady(t.Context(), assetID), errs.ErrContentConflict)
}

func TestContentConflictLeavesReadyWinnerUnchanged(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	owner := testutil.SeedOwner(t, d.WriteDB(), "local", "alice")
	var storageKey string
	r.NoError(d.ReadDB().QueryRowContext(t.Context(),
		`SELECT storage_key FROM owners WHERE hub=? AND user_id=?`, owner.Hub, owner.UserID,
	).Scan(&storageKey))

	assetID, fileID := uuid.NewString(), uuid.NewString()
	path, err := content.VirtualPath(storageKey, fileID, "photo.jpg")
	r.NoError(err)
	digest := sha256.Sum256([]byte("photo"))
	pending := media.PendingContent{
		OperationID: uuid.NewString(),
		File: media.File{
			ID: fileID, AssetID: assetID, Owner: owner, Role: media.RolePrimary,
			MimeType: "image/jpeg", OriginalFilename: "photo.jpg", Size: 5,
		},
		SHA256: hex.EncodeToString(digest[:]), Size: 5, VirtualPath: path,
	}
	repo := media.NewAssetRepo(d.WriteDB(), d.ReadDB())
	r.NoError(repo.ReserveImport(t.Context(), media.Asset{
		ID: assetID, Owner: owner, Type: media.TypePhoto,
		ImportedAt: time.Now().UTC(), ThumbStatus: "pending",
	}, []media.PendingContent{pending}, nil))
	r.NoError(repo.ApplyContentReceipt(t.Context(), media.ContentReceipt{
		OperationID: pending.OperationID, NodeID: 1, VersionID: uuid.NewString(),
		SHA256: pending.SHA256, Size: pending.Size,
	}))
	r.NoError(repo.FinalizeReady(t.Context(), assetID))

	conflicted, err := repo.MarkContentConflict(t.Context(), assetID, errs.ErrContentIdentityMismatch)
	r.NoError(err)
	r.False(conflicted)
	asset, err := repo.GetAsset(t.Context(), assetID)
	r.NoError(err)
	r.Equal(media.AssetReady, asset.State)
	var operationStatus string
	r.NoError(d.ReadDB().QueryRowContext(t.Context(),
		`SELECT status FROM content_operations WHERE id=?`, pending.OperationID,
	).Scan(&operationStatus))
	r.Equal("applied", operationStatus)
}

func TestApplyContentReceiptIsIdempotent(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	owner := testutil.SeedOwner(t, d.WriteDB(), "local", "alice")
	var storageKey string
	r.NoError(d.ReadDB().QueryRowContext(t.Context(),
		`SELECT storage_key FROM owners WHERE hub=? AND user_id=?`, owner.Hub, owner.UserID,
	).Scan(&storageKey))
	assetID, fileID := uuid.NewString(), uuid.NewString()
	path, err := content.VirtualPath(storageKey, fileID, "photo.jpg")
	r.NoError(err)
	digest := sha256.Sum256([]byte("photo"))
	pending := media.PendingContent{
		OperationID: uuid.NewString(),
		File: media.File{
			ID: fileID, AssetID: assetID, Owner: owner, Role: media.RolePrimary,
			MimeType: "image/jpeg", OriginalFilename: "photo.jpg", Size: 5,
		},
		SHA256: hex.EncodeToString(digest[:]), Size: 5, VirtualPath: path,
	}
	repo := media.NewAssetRepo(d.WriteDB(), d.ReadDB())
	r.NoError(repo.ReserveImport(t.Context(), media.Asset{
		ID: assetID, Owner: owner, Type: media.TypePhoto,
		ImportedAt: time.Now().UTC(), ThumbStatus: "pending",
	}, []media.PendingContent{pending}, nil))
	receipt := media.ContentReceipt{
		OperationID: pending.OperationID, NodeID: 1, VersionID: uuid.NewString(),
		SHA256: pending.SHA256, Size: pending.Size,
	}
	r.NoError(repo.ApplyContentReceipt(t.Context(), receipt))
	r.NoError(repo.ApplyContentReceipt(t.Context(), receipt))

	other := receipt
	other.VersionID = uuid.NewString()
	r.ErrorIs(repo.ApplyContentReceipt(t.Context(), other), errs.ErrContentConflict)
}
