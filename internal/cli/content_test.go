package cli_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/cli"
	"go.kenn.io/fotobank/internal/content"
	"go.kenn.io/fotobank/internal/db"
	"go.kenn.io/fotobank/internal/media"
	"go.kenn.io/fotobank/internal/owners"
)

func TestContentRecoverFinishesInterruptedImport(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeBasicConfig(t, tmp)
	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)

	d, err := db.Open(dbPath)
	r.NoError(err)
	owner := owners.Principal{Hub: "h", UserID: "u"}
	storageKey := "550e8400-e29b-41d4-a716-446655440000"
	_, err = d.WriteDB().ExecContext(t.Context(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES (?, ?, ?, ?)`,
		owner.Hub, owner.UserID, storageKey, time.Now().UTC())
	r.NoError(err)
	assetID, fileID, operationID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	virtualPath, err := content.VirtualPath(storageKey, fileID, "recovered.jpg")
	r.NoError(err)
	body := []byte("created before the interrupted process recorded its receipt")
	digest := sha256.Sum256(body)
	identity := content.Identity{SHA256: fmt.Sprintf("%x", digest), Size: int64(len(body))}
	assets := media.NewAssetRepo(d.WriteDB(), d.ReadDB())
	r.NoError(assets.ReserveImport(t.Context(), media.Asset{
		ID: assetID, Owner: owner, State: media.AssetPending, Type: media.TypePhoto,
		ImportedAt: time.Now().UTC(), ThumbStatus: "pending",
	}, []media.PendingContent{{
		OperationID: operationID,
		File: media.File{
			ID: fileID, AssetID: assetID, Owner: owner, Role: media.RolePrimary,
			MimeType: "image/jpeg", OriginalFilename: "recovered.jpg",
			ImportSourcePath: "recovered.jpg", Size: int64(len(body)),
		},
		SHA256: identity.SHA256, Size: identity.Size, VirtualPath: virtualPath,
	}}, nil))
	adapter, err := content.Open(t.Context(), content.Config{
		Root: filepath.Join(tmp, "flash", "docbank"),
	})
	r.NoError(err)
	_, err = adapter.Create(t.Context(), content.CreateRequest{
		VirtualPath: virtualPath, MediaType: "image/jpeg",
		Expected: identity, Reader: bytes.NewReader(body),
	})
	r.NoError(err)
	r.NoError(adapter.Close())
	r.NoError(d.Close())

	var stdout, stderr bytes.Buffer
	code := cli.RunContext(context.Background(),
		[]string{"content", "recover", "--config", cfgPath}, &stdout, &stderr)
	r.Equal(0, code, "stderr=%s stdout=%s", stderr.String(), stdout.String())
	r.Contains(stdout.String(), "adopted=1")
	r.Contains(stdout.String(), "finalized=1")
	r.Contains(stdout.String(), "pending=0")
	r.Empty(stderr.String())

	d, err = db.Open(dbPath)
	r.NoError(err)
	defer func() { r.NoError(d.Close()) }()
	asset, err := media.NewAssetRepo(d.WriteDB(), d.ReadDB()).GetAsset(t.Context(), assetID)
	r.NoError(err)
	r.Equal(media.AssetReady, asset.State)
}
