package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/album"
	"github.com/wesm/fotobank/internal/db"
	"github.com/wesm/fotobank/internal/httpapi"
	"github.com/wesm/fotobank/internal/identity"
	"github.com/wesm/fotobank/internal/media"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/service"
	"github.com/wesm/fotobank/internal/share"
	"github.com/wesm/fotobank/internal/testutil"
)

type sharesHTTPFixture struct {
	h      http.Handler
	owner  owners.Principal
	shares *service.ShareService
	albums *service.AlbumService
	media  *media.Repo
	db     *db.DB
}

func newSharesHTTPFixture(t *testing.T) *sharesHTTPFixture {
	t.Helper()
	ctx := context.Background()
	d := testutil.OpenTestDB(t)
	owner := owners.Principal{Hub: "h", UserID: "o"}
	_, err := d.WriteDB().ExecContext(ctx,
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		owner.Hub, owner.UserID, "sk", time.Now().UTC())
	require.NoError(t, err)

	albumsRepo := album.NewRepo(d.WriteDB(), d.ReadDB())
	mediaRepo := media.NewRepo(d.WriteDB(), d.ReadDB())
	shareRepo := share.NewRepo(d.WriteDB(), d.ReadDB())
	albumSvc := service.NewAlbumService(albumsRepo, mediaRepo, shareRepo, d)
	shareSvc := service.NewShareService(shareRepo, albumsRepo, mediaRepo)

	h, err := httpapi.New(httpapi.Deps{
		IdentityProvider: identity.NewStub(owner, "Test User"),
		AlbumService:     albumSvc,
		ShareService:     shareSvc,
	})
	require.NoError(t, err)
	return &sharesHTTPFixture{
		h: h, owner: owner, shares: shareSvc, albums: albumSvc,
		media: mediaRepo, db: d,
	}
}

func (fx *sharesHTTPFixture) seedAlbumWithMedia(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	a, err := fx.albums.Create(ctx, fx.owner, "T")
	require.NoError(t, err)
	m := media.Media{
		ID: uuid.NewString(), Owner: fx.owner, Type: media.TypePhoto,
		MimeType: "image/jpeg", Path: "2024/" + uuid.NewString() + ".jpg",
		OriginalFilename: "x.jpg", ImportedAt: time.Now().UTC().Truncate(time.Second),
		Size: 100, Checksum: "cs" + uuid.NewString(), ThumbStatus: "pending",
	}
	require.NoError(t, fx.media.Insert(ctx, m))
	_, _, err = fx.albums.AddMedia(ctx, a.ID, []string{m.ID}, fx.owner)
	require.NoError(t, err)
	return a.ID
}

func TestSharesCreateAlbumLive201(t *testing.T) {
	r := require.New(t)
	fx := newSharesHTTPFixture(t)
	albumID := fx.seedAlbumWithMedia(t)

	body, _ := json.Marshal(map[string]any{
		"label":       "Summer",
		"grantee":     map[string]string{"hub": "h", "user_id": "alice"},
		"target_type": "album_live",
		"album_id":    albumID,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/shares", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	fx.h.ServeHTTP(rec, req)
	r.Equal(http.StatusCreated, rec.Code, rec.Body.String())

	var resp map[string]any
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &resp))
	r.Equal("pending", resp["broker_status"])
	r.Equal(albumID, resp["target_album_id"])
}

func TestSharesCreateInvalidTargetCombo400(t *testing.T) {
	r := require.New(t)
	fx := newSharesHTTPFixture(t)
	albumID := fx.seedAlbumWithMedia(t)
	body, _ := json.Marshal(map[string]any{
		"grantee":     map[string]string{"hub": "h", "user_id": "alice"},
		"target_type": "media_set",
		"album_id":    albumID, // wrong combo
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/shares", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	fx.h.ServeHTTP(rec, req)
	r.Equal(http.StatusBadRequest, rec.Code)
}

func TestSharesGet404WhenCrossOwner(t *testing.T) {
	r := require.New(t)
	fx := newSharesHTTPFixture(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/shares/"+uuid.NewString(), nil)
	rec := httptest.NewRecorder()
	fx.h.ServeHTTP(rec, req)
	r.Equal(http.StatusNotFound, rec.Code)
}

func TestSharesRevoke201ThenAlreadyRevoked409(t *testing.T) {
	r := require.New(t)
	fx := newSharesHTTPFixture(t)
	albumID := fx.seedAlbumWithMedia(t)
	s, err := fx.shares.Create(context.Background(), service.CreateShareRequest{
		Grantee: owners.Principal{Hub: "h", UserID: "a"}, TargetType: share.TargetAlbumLive, AlbumID: albumID,
	}, fx.owner)
	r.NoError(err)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/shares/"+s.UUID+"/revoke", nil)
	rec := httptest.NewRecorder()
	fx.h.ServeHTTP(rec, req)
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	// Second call -> 409.
	req = httptest.NewRequest(http.MethodPost, "/api/v1/shares/"+s.UUID+"/revoke", nil)
	rec = httptest.NewRecorder()
	fx.h.ServeHTTP(rec, req)
	r.Equal(http.StatusConflict, rec.Code)
}

func TestSharesRetryNotApplicable409(t *testing.T) {
	r := require.New(t)
	fx := newSharesHTTPFixture(t)
	albumID := fx.seedAlbumWithMedia(t)
	s, err := fx.shares.Create(context.Background(), service.CreateShareRequest{
		Grantee: owners.Principal{Hub: "h", UserID: "a"}, TargetType: share.TargetAlbumLive, AlbumID: albumID,
	}, fx.owner)
	r.NoError(err)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/shares/"+s.UUID+"/retry", nil)
	rec := httptest.NewRecorder()
	fx.h.ServeHTTP(rec, req)
	r.Equal(http.StatusConflict, rec.Code)
}

func TestSharesListDefaultHidesRevokedRemote(t *testing.T) {
	r := require.New(t)
	fx := newSharesHTTPFixture(t)
	albumID := fx.seedAlbumWithMedia(t)
	_, err := fx.shares.Create(context.Background(), service.CreateShareRequest{
		Grantee: owners.Principal{Hub: "h", UserID: "visible"}, TargetType: share.TargetAlbumLive, AlbumID: albumID,
	}, fx.owner)
	r.NoError(err)
	hidden, err := fx.shares.Create(context.Background(), service.CreateShareRequest{
		Grantee: owners.Principal{Hub: "h", UserID: "hidden"}, TargetType: share.TargetAlbumLive, AlbumID: albumID,
	}, fx.owner)
	r.NoError(err)
	// Flip hidden → revoked_remote directly via the fixture's DB handle.
	now := time.Now().UTC()
	_, err = fx.db.WriteDB().ExecContext(context.Background(),
		`UPDATE scopes SET broker_status='revoked_remote', revoked_at=?, broker_revoked_at=? WHERE uuid=?`,
		now, now, hidden.UUID)
	r.NoError(err)

	// Default: only `visible` is returned — `hidden` is in revoked_remote
	// and the default view suppresses that terminal purge-eligible state.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/shares", nil)
	rec := httptest.NewRecorder()
	fx.h.ServeHTTP(rec, req)
	r.Equal(http.StatusOK, rec.Code)
	var resp struct {
		Items []map[string]any `json:"items"`
	}
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &resp))
	r.Len(resp.Items, 1)

	// status=revoked_remote: the explicit filter surfaces `hidden`.
	req = httptest.NewRequest(http.MethodGet, "/api/v1/shares?status=revoked_remote", nil)
	rec = httptest.NewRecorder()
	fx.h.ServeHTTP(rec, req)
	r.Equal(http.StatusOK, rec.Code)
	resp = struct {
		Items []map[string]any `json:"items"`
	}{}
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &resp))
	r.Len(resp.Items, 1)
}

func TestSharesListUnknownStatus400(t *testing.T) {
	r := require.New(t)
	fx := newSharesHTTPFixture(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/shares?status=bogus", nil)
	rec := httptest.NewRecorder()
	fx.h.ServeHTTP(rec, req)
	r.Equal(http.StatusBadRequest, rec.Code)
}
