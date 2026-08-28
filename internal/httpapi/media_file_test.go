package httpapi_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/content"
	"go.kenn.io/fotobank/internal/media"
)

func seedAttachedFile(t *testing.T, fx mediaOriginalFixture, asset media.Media, body []byte) media.File {
	t.Helper()
	var storageKey string
	require.NoError(t, fx.rw.QueryRowContext(t.Context(),
		`SELECT storage_key FROM owners WHERE hub=? AND user_id=?`, asset.Owner.Hub, asset.Owner.UserID,
	).Scan(&storageKey))
	fileID := uuid.NewString()
	virtualPath, err := content.VirtualPath(storageKey, fileID, "source.dng")
	require.NoError(t, err)
	digest := sha256.Sum256(body)
	sha := hex.EncodeToString(digest[:])
	receipt, err := fx.content.Create(t.Context(), content.CreateRequest{
		VirtualPath: virtualPath, MediaType: "image/x-adobe-dng",
		Expected: content.Identity{SHA256: sha, Size: int64(len(body))},
		Reader:   bytes.NewReader(body),
	})
	require.NoError(t, err)
	_, err = fx.rw.ExecContext(t.Context(), `INSERT INTO media_files (
		id, asset_id, owner_hub, owner_user_id, role, mime_type,
		original_filename, size, docbank_node_id, docbank_virtual_path,
		current_version_id, sha256
	) VALUES (?, ?, ?, ?, 'original', ?, 'source.dng', ?, ?, ?, ?, ?)`,
		fileID, asset.ID, asset.Owner.Hub, asset.Owner.UserID, "image/x-adobe-dng",
		len(body), receipt.Node.ID, virtualPath, receipt.Version.ID, sha)
	require.NoError(t, err)
	return media.File{
		ID: fileID, AssetID: asset.ID, Owner: asset.Owner, Role: media.RoleOriginal,
		MimeType: "image/x-adobe-dng", OriginalFilename: "source.dng",
		Size: int64(len(body)), DocbankNodeID: &receipt.Node.ID,
		DocbankVirtualPath: virtualPath, CurrentVersionID: receipt.Version.ID, SHA256: sha,
	}
}

func TestGetMediaFileReturnsExactAttachment(t *testing.T) {
	r := require.New(t)
	fx := newMediaOriginalTest(t)
	asset := seedOriginal(t, fx, fx.owner, []byte("jpeg"))
	body := []byte("raw camera bytes")
	file := seedAttachedFile(t, fx, asset, body)

	resp, err := http.Get(fx.srv.URL + "/api/v1/media/" + asset.ID + "/files/" + file.ID + "/content")
	r.NoError(err)
	defer resp.Body.Close()
	r.Equal(http.StatusOK, resp.StatusCode)
	got, err := io.ReadAll(resp.Body)
	r.NoError(err)
	r.Equal(body, got)
	r.Equal(file.MimeType, resp.Header.Get("Content-Type"))
	r.Equal(`"`+file.SHA256+`"`, resp.Header.Get("ETag"))
	r.Equal("bytes", resp.Header.Get("Accept-Ranges"))
}

func TestGetMediaFileHidesFileFromAnotherAsset(t *testing.T) {
	r := require.New(t)
	fx := newMediaOriginalTest(t)
	first := seedOriginal(t, fx, fx.owner, []byte("first"))
	second := seedOriginal(t, fx, fx.owner, []byte("second"))
	file := seedAttachedFile(t, fx, second, []byte("second raw"))

	resp, err := http.Get(fx.srv.URL + "/api/v1/media/" + first.ID + "/files/" + file.ID + "/content")
	r.NoError(err)
	defer resp.Body.Close()
	r.Equal(http.StatusNotFound, resp.StatusCode)
}
