package cli_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/cli"
	"go.kenn.io/fotobank/internal/client"
	"go.kenn.io/fotobank/internal/content"
	"go.kenn.io/fotobank/internal/db"
	"go.kenn.io/fotobank/internal/httpapi"
	"go.kenn.io/fotobank/internal/ingest"
	"go.kenn.io/fotobank/internal/media"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/version"
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
	other := owners.Principal{Hub: "h", UserID: "other"}
	otherKey := uuid.NewString()
	_, err = d.WriteDB().ExecContext(t.Context(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES (?, ?, ?, ?)`,
		other.Hub, other.UserID, otherKey, time.Now().UTC())
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
	orphanPath, err := content.VirtualPath(otherKey, uuid.NewString(), "unmatched.jpg")
	r.NoError(err)
	_, err = adapter.Create(t.Context(), content.CreateRequest{
		VirtualPath: orphanPath, MediaType: "image/jpeg",
		Expected: identity, Reader: bytes.NewReader(body),
	})
	r.NoError(err)
	r.NoError(adapter.Close())
	r.NoError(d.Close())

	var stdout, stderr bytes.Buffer
	var code int
	t.Run("live recovery", func(t *testing.T) {
		r := require.New(t)
		startCheckoutServer(t, cfgPath, dbPath)
		code = cli.RunContext(context.Background(),
			[]string{"content", "recover", "--config", cfgPath}, &stdout, &stderr)
		var repeated, repeatErrors bytes.Buffer
		r.Zero(cli.RunContext(t.Context(),
			[]string{"content", "recover", "--config", cfgPath, "--json"}, &repeated, &repeatErrors), repeatErrors.String())
		var result httpapi.ContentRecoveryResult
		r.NoError(json.Unmarshal(repeated.Bytes(), &result))
		r.Len(result.Reports, 2)
		for _, report := range result.Reports {
			r.Zero(report.Adopted)
			r.Zero(report.Finalized)
			if report.Owner == other {
				r.Equal([]string{orphanPath}, report.OrphanPaths)
			}
		}
	})
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

func TestContentRecoveryOperatorAndImportLock(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeBasicConfig(t, tmp)
	dbPath := filepath.Join(tmp, "catalog.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)
	var invalidOutput, invalidErrors bytes.Buffer
	r.NotZero(cli.RunContext(t.Context(), []string{"content", "recover", "--config", cfgPath, "--wait=-1s", "--json"}, &invalidOutput, &invalidErrors))
	var invalidResult httpapi.ContentRecoveryResult
	r.NoError(json.Unmarshal(invalidOutput.Bytes(), &invalidResult))
	r.NotEmpty(invalidResult.Error)
	_, err := os.Stat(cfgPath + ".operator")
	r.ErrorIs(err, os.ErrNotExist)
	record := startCheckoutServer(t, cfgPath, dbPath)
	for _, tc := range []struct {
		name, base, token, user string
		status                  int
	}{
		{"operator", record.Endpoint().BaseURL(), record.Metadata["token"], "u", 200},
		{"no token", record.Endpoint().BaseURL(), "", "u", 401},
		{"wrong principal", record.Endpoint().BaseURL(), record.Metadata["token"], "other", 403},
		{"photo listener", record.Metadata["web_url"], record.Metadata["token"], "u", 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := require.New(t)
			body := fmt.Sprintf(`{"hub":"h","user_id":%q}`, tc.user)
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, tc.base+"/api/v1/operator/content/recover", bytes.NewBufferString(body))
			r.NoError(err)
			req.Header.Set("Content-Type", "application/json")
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			response, err := http.DefaultClient.Do(req)
			r.NoError(err)
			defer response.Body.Close()
			_, err = io.Copy(io.Discard, response.Body)
			r.NoError(err)
			r.Equal(tc.status, response.StatusCode)
		})
	}
	unlock, err := ingest.Acquire(t.Context(), filepath.Join(tmp, "flash", ".fotobank", "import.lock"), 0)
	r.NoError(err)
	defer unlock()
	request := httpapi.ContentRecoveryRequest{Hub: "h", UserID: "u", Wait: "0s"}
	result, err := client.RecoverContent(t.Context(), cfgPath, version.Short, request)
	r.Error(err)
	r.NotEmpty(result.Error)
	r.Empty(result.Reports)
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	request.Wait = "30s"
	_, err = client.RecoverContent(ctx, cfgPath, version.Short, request)
	r.ErrorIs(err, context.DeadlineExceeded)
	unlock()
	request.Wait = "1s"
	result, err = client.RecoverContent(t.Context(), cfgPath, version.Short, request)
	r.NoError(err)
	r.Len(result.Reports, 1)
}
