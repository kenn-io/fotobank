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
	h       http.Handler
	owner   owners.Principal
	shares  *service.ShareService
	albums  *service.AlbumService
	media   *media.Repo
	display *share.PrincipalDisplayRepo
	db      *db.DB
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
	displayRepo := share.NewPrincipalDisplayRepo(d.WriteDB(), d.ReadDB())
	albumSvc := service.NewAlbumService(albumsRepo, mediaRepo, shareRepo, d)
	shareSvc := service.NewShareService(shareRepo, albumsRepo, mediaRepo)

	h, err := httpapi.New(httpapi.Deps{
		IdentityProvider: identity.NewStub(owner, "Test User"),
		AlbumService:     albumSvc,
		ShareService:     shareSvc,
		PrincipalDisplay: displayRepo,
	})
	require.NoError(t, err)
	return &sharesHTTPFixture{
		h: h, owner: owner, shares: shareSvc, albums: albumSvc,
		media: mediaRepo, display: displayRepo, db: d,
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

func TestSharesGet404WhenCrossOwnerExistingScope(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	fx := newSharesHTTPFixture(t)
	// Seed a second owner + album + scope owned by that other owner.
	other := owners.Principal{Hub: "h", UserID: "other"}
	_, err := fx.db.WriteDB().ExecContext(ctx,
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		other.Hub, other.UserID, "sk2", time.Now().UTC())
	r.NoError(err)
	otherAlbumID := uuid.NewString()
	now := time.Now().UTC()
	_, err = fx.db.WriteDB().ExecContext(ctx,
		`INSERT INTO albums(id, owner_hub, owner_user_id, name, created_at, updated_at)
		 VALUES(?,?,?,?,?,?)`,
		otherAlbumID, other.Hub, other.UserID, "T", now, now)
	r.NoError(err)
	// Insert a scope directly so we don't need a ShareService scoped to other.
	scopeUUID := uuid.NewString()
	_, err = fx.db.WriteDB().ExecContext(ctx,
		`INSERT INTO scopes(uuid, owner_hub, owner_user_id, grantee_hub, grantee_user_id,
		                    target_type, target_album_id, created_at, broker_status)
		 VALUES(?,?,?,?,?,?,?,?,?)`,
		scopeUUID, other.Hub, other.UserID, "h", "a", "album_live", otherAlbumID, now, "pending")
	r.NoError(err)

	// fx.owner calls GET /api/v1/shares/{scopeUUID} — must see 404, not 403.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/shares/"+scopeUUID, nil)
	rec := httptest.NewRecorder()
	fx.h.ServeHTTP(rec, req)
	r.Equal(http.StatusNotFound, rec.Code)
}

func TestSharesGetReturnsScopeBody(t *testing.T) {
	r := require.New(t)
	fx := newSharesHTTPFixture(t)
	albumID := fx.seedAlbumWithMedia(t)
	s, err := fx.shares.Create(context.Background(), service.CreateShareRequest{
		Grantee: owners.Principal{Hub: "h", UserID: "alice"}, TargetType: share.TargetAlbumLive, AlbumID: albumID,
	}, fx.owner)
	r.NoError(err)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/shares/"+s.UUID, nil)
	rec := httptest.NewRecorder()
	fx.h.ServeHTTP(rec, req)
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	var body map[string]any
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &body))
	r.Equal(s.UUID, body["uuid"])
	r.Equal("pending", body["broker_status"])
	r.Equal(albumID, body["target_album_id"])
	grantee, ok := body["grantee"].(map[string]any)
	r.True(ok, "grantee field must be an object")
	r.Equal("alice", grantee["user_id"])
	_, present := body["media_ids"]
	r.False(present, "album_live response should omit media_ids")
}

func TestSharesGetMediaSetReturnsMediaIDs(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	fx := newSharesHTTPFixture(t)
	// Seed one media owned by fx.owner so the scope has something to point at.
	m := media.Media{
		ID: uuid.NewString(), Owner: fx.owner, Type: media.TypePhoto,
		MimeType: "image/jpeg", Path: "2024/" + uuid.NewString() + ".jpg",
		OriginalFilename: "x.jpg", ImportedAt: time.Now().UTC().Truncate(time.Second),
		Size: 100, Checksum: "cs" + uuid.NewString(), ThumbStatus: "pending",
	}
	r.NoError(fx.media.Insert(ctx, m))

	s, err := fx.shares.Create(ctx, service.CreateShareRequest{
		Grantee: owners.Principal{Hub: "h", UserID: "alice"}, TargetType: share.TargetMediaSet,
		MediaIDs: []string{m.ID},
	}, fx.owner)
	r.NoError(err)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/shares/"+s.UUID, nil)
	rec := httptest.NewRecorder()
	fx.h.ServeHTTP(rec, req)
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	var body map[string]any
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &body))
	r.Equal("media_set", body["target_type"])
	ids, ok := body["media_ids"].([]any)
	r.True(ok, "media_ids should be an array")
	r.Len(ids, 1)
	r.Equal(m.ID, ids[0])
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
	var first map[string]any
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &first))
	r.Equal("revoking", first["broker_status"])

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
	visible, err := fx.shares.Create(context.Background(), service.CreateShareRequest{
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
	r.Equal(visible.UUID, resp.Items[0]["uuid"])

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
	r.Equal(hidden.UUID, resp.Items[0]["uuid"])
}

func TestSharesListUnknownStatus400(t *testing.T) {
	r := require.New(t)
	fx := newSharesHTTPFixture(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/shares?status=bogus", nil)
	rec := httptest.NewRecorder()
	fx.h.ServeHTTP(rec, req)
	r.Equal(http.StatusBadRequest, rec.Code)
}

func TestSharesListMixedInvalidStatus400(t *testing.T) {
	r := require.New(t)
	fx := newSharesHTTPFixture(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/shares?status=pending,bogus", nil)
	rec := httptest.NewRecorder()
	fx.h.ServeHTTP(rec, req)
	r.Equal(http.StatusBadRequest, rec.Code)
}

func TestSharesPreviewHappyPath(t *testing.T) {
	r := require.New(t)
	fx := newSharesHTTPFixture(t)
	albumID := fx.seedAlbumWithMedia(t)

	// Create the share via the service so we have a UUID to preview.
	s, err := fx.shares.Create(context.Background(), service.CreateShareRequest{
		Grantee: owners.Principal{Hub: "h", UserID: "alice"}, TargetType: share.TargetAlbumLive, AlbumID: albumID,
	}, fx.owner)
	r.NoError(err)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/shares/"+s.UUID+"/preview", nil)
	rec := httptest.NewRecorder()
	fx.h.ServeHTTP(rec, req)
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	var resp struct {
		Scope    map[string]any   `json:"scope"`
		Media    []map[string]any `json:"media"`
		Album    *map[string]any  `json:"album"`
		Warnings []string         `json:"warnings"`
	}
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &resp))
	r.Equal(s.UUID, resp.Scope["uuid"])
	r.NotNil(resp.Album)
	r.Equal(albumID, (*resp.Album)["id"])
	r.Len(resp.Media, 1)
	// Freshly-created album_live scope with a pending thumb must surface
	// both broker_not_active and missing_thumbs; we assert the prior.
	r.Contains(resp.Warnings, "broker_not_active")
}

func TestSharesPreviewMediaSetExposesFrozenMediaIDs(t *testing.T) {
	r := require.New(t)
	fx := newSharesHTTPFixture(t)
	// Seed two media rows so we can build a media_set scope over them.
	m1 := media.Media{
		ID: uuid.NewString(), Owner: fx.owner, Type: media.TypePhoto,
		MimeType: "image/jpeg", Path: "2024/" + uuid.NewString() + ".jpg",
		OriginalFilename: "x.jpg", ImportedAt: time.Now().UTC().Truncate(time.Second),
		Size: 100, Checksum: "cs1-" + uuid.NewString(), ThumbStatus: "pending",
	}
	m2 := m1
	m2.ID = uuid.NewString()
	m2.Path = "2024/" + uuid.NewString() + ".jpg"
	m2.Checksum = "cs2-" + uuid.NewString()
	r.NoError(fx.media.Insert(context.Background(), m1))
	r.NoError(fx.media.Insert(context.Background(), m2))

	s, err := fx.shares.Create(context.Background(), service.CreateShareRequest{
		Grantee: owners.Principal{Hub: "h", UserID: "alice"}, TargetType: share.TargetMediaSet,
		MediaIDs: []string{m1.ID, m2.ID},
	}, fx.owner)
	r.NoError(err)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/shares/"+s.UUID+"/preview", nil)
	rec := httptest.NewRecorder()
	fx.h.ServeHTTP(rec, req)
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	var resp struct {
		Scope struct {
			UUID     string   `json:"uuid"`
			MediaIDs []string `json:"media_ids"`
		} `json:"scope"`
	}
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &resp))
	r.Equal(s.UUID, resp.Scope.UUID)
	r.ElementsMatch([]string{m1.ID, m2.ID}, resp.Scope.MediaIDs,
		"media_set preview must carry frozen media_ids to match /shares/{uuid} detail")
}

func TestSharesPreviewCrossOwnerReturns404(t *testing.T) {
	r := require.New(t)
	fx := newSharesHTTPFixture(t)
	albumID := fx.seedAlbumWithMedia(t)
	s, err := fx.shares.Create(context.Background(), service.CreateShareRequest{
		Grantee: owners.Principal{Hub: "h", UserID: "alice"}, TargetType: share.TargetAlbumLive, AlbumID: albumID,
	}, fx.owner)
	r.NoError(err)

	// Build a second handler bound to a different identity; PreviewScope
	// must return 404 for a cross-owner caller.
	intruder := owners.Principal{Hub: "h", UserID: "intruder"}
	_, err = fx.db.WriteDB().ExecContext(context.Background(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		intruder.Hub, intruder.UserID, "sk-i", time.Now().UTC())
	r.NoError(err)
	h, err := httpapi.New(httpapi.Deps{
		IdentityProvider: identity.NewStub(intruder, ""),
		ShareService:     fx.shares,
	})
	r.NoError(err)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/shares/"+s.UUID+"/preview", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	r.Equal(http.StatusNotFound, rec.Code, rec.Body.String())
}

func TestSharesListHydratesGranteeHandle(t *testing.T) {
	r := require.New(t)
	fx := newSharesHTTPFixture(t)
	albumID := fx.seedAlbumWithMedia(t)

	// Mint a scope granted to bob, then seed a display row so the
	// hydration path has something to hit.
	bob := owners.Principal{Hub: "h", UserID: "bob"}
	_, err := fx.shares.Create(context.Background(), service.CreateShareRequest{
		Grantee: bob, TargetType: share.TargetAlbumLive, AlbumID: albumID,
	}, fx.owner)
	r.NoError(err)
	r.NoError(fx.display.Upsert(context.Background(),
		identity.Principal{Hub: bob.Hub, UserID: bob.UserID, Handle: "Bob"},
		time.Now().UTC()))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/shares", nil)
	rec := httptest.NewRecorder()
	fx.h.ServeHTTP(rec, req)
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	var resp struct {
		Items []map[string]any `json:"items"`
	}
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &resp))
	r.Len(resp.Items, 1)
	r.Equal("Bob", resp.Items[0]["grantee_handle"])
}

func TestSharesGetHydratesGranteeHandle(t *testing.T) {
	r := require.New(t)
	fx := newSharesHTTPFixture(t)
	albumID := fx.seedAlbumWithMedia(t)
	bob := owners.Principal{Hub: "h", UserID: "bob"}
	s, err := fx.shares.Create(context.Background(), service.CreateShareRequest{
		Grantee: bob, TargetType: share.TargetAlbumLive, AlbumID: albumID,
	}, fx.owner)
	r.NoError(err)
	r.NoError(fx.display.Upsert(context.Background(),
		identity.Principal{Hub: bob.Hub, UserID: bob.UserID, Handle: "Bob"},
		time.Now().UTC()))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/shares/"+s.UUID, nil)
	rec := httptest.NewRecorder()
	fx.h.ServeHTTP(rec, req)
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	var body map[string]any
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &body))
	r.Equal("Bob", body["grantee_handle"])
}

func TestSharesListNoDisplayRowOmitsGranteeHandle(t *testing.T) {
	r := require.New(t)
	fx := newSharesHTTPFixture(t)
	albumID := fx.seedAlbumWithMedia(t)
	// No principal_display row for the grantee.
	_, err := fx.shares.Create(context.Background(), service.CreateShareRequest{
		Grantee: owners.Principal{Hub: "h", UserID: "bob"}, TargetType: share.TargetAlbumLive, AlbumID: albumID,
	}, fx.owner)
	r.NoError(err)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/shares", nil)
	rec := httptest.NewRecorder()
	fx.h.ServeHTTP(rec, req)
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	var resp struct {
		Items []map[string]any `json:"items"`
	}
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &resp))
	r.Len(resp.Items, 1)
	_, present := resp.Items[0]["grantee_handle"]
	r.False(present, "grantee_handle must be absent from JSON when no display row exists")
}

func TestSharesListIncludesTargetSummaryForBothTypes(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	fx := newSharesHTTPFixture(t)

	// album_live with two members, then rename it for label assertion.
	albumID := fx.seedAlbumWithMedia(t)
	_, err := fx.db.WriteDB().ExecContext(ctx,
		`UPDATE albums SET name = ? WHERE id = ?`, "Italy 2025", albumID)
	r.NoError(err)
	liveScope, err := fx.shares.Create(ctx, service.CreateShareRequest{
		Grantee:    owners.Principal{Hub: "h", UserID: "alice"},
		TargetType: share.TargetAlbumLive, AlbumID: albumID,
	}, fx.owner)
	r.NoError(err)

	// media_set with two frozen members.
	m1 := media.Media{
		ID: uuid.NewString(), Owner: fx.owner, Type: media.TypePhoto,
		MimeType: "image/jpeg", Path: "2024/" + uuid.NewString() + ".jpg",
		OriginalFilename: "x.jpg", ImportedAt: time.Now().UTC().Truncate(time.Second),
		Size: 100, Checksum: "cs" + uuid.NewString(), ThumbStatus: "pending",
	}
	r.NoError(fx.media.Insert(ctx, m1))
	m2 := media.Media{
		ID: uuid.NewString(), Owner: fx.owner, Type: media.TypePhoto,
		MimeType: "image/jpeg", Path: "2024/" + uuid.NewString() + ".jpg",
		OriginalFilename: "y.jpg", ImportedAt: time.Now().UTC().Truncate(time.Second),
		Size: 100, Checksum: "cs" + uuid.NewString(), ThumbStatus: "pending",
	}
	r.NoError(fx.media.Insert(ctx, m2))
	setScope, err := fx.shares.Create(ctx, service.CreateShareRequest{
		Grantee:    owners.Principal{Hub: "h", UserID: "alice"},
		TargetType: share.TargetMediaSet, MediaIDs: []string{m1.ID, m2.ID},
	}, fx.owner)
	r.NoError(err)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/shares", nil)
	rec := httptest.NewRecorder()
	fx.h.ServeHTTP(rec, req)
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	var resp struct {
		Items []map[string]any `json:"items"`
	}
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &resp))
	r.Len(resp.Items, 2)
	byUUID := map[string]map[string]any{}
	for _, it := range resp.Items {
		uuidStr, _ := it["uuid"].(string)
		byUUID[uuidStr] = it
	}

	live := byUUID[liveScope.UUID]
	r.NotNil(live, "list must include the album_live scope")
	liveSummary, ok := live["target_summary"].(map[string]any)
	r.True(ok, "album_live scope must have target_summary")
	r.Equal("Album: Italy 2025", liveSummary["label"])
	_, hasItemCount := liveSummary["item_count"]
	r.False(hasItemCount, "album_live target_summary omits item_count")

	set := byUUID[setScope.UUID]
	r.NotNil(set, "list must include the media_set scope")
	setSummary, ok := set["target_summary"].(map[string]any)
	r.True(ok, "media_set scope must have target_summary")
	r.Equal("2 photos", setSummary["label"])
	r.EqualValues(2, setSummary["item_count"])
}

func TestSharesGetIncludesTargetSummary(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	fx := newSharesHTTPFixture(t)
	albumID := fx.seedAlbumWithMedia(t)
	_, err := fx.db.WriteDB().ExecContext(ctx,
		`UPDATE albums SET name = ? WHERE id = ?`, "Family", albumID)
	r.NoError(err)
	s, err := fx.shares.Create(ctx, service.CreateShareRequest{
		Grantee:    owners.Principal{Hub: "h", UserID: "alice"},
		TargetType: share.TargetAlbumLive, AlbumID: albumID,
	}, fx.owner)
	r.NoError(err)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/shares/"+s.UUID, nil)
	rec := httptest.NewRecorder()
	fx.h.ServeHTTP(rec, req)
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	var body map[string]any
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &body))
	summary, ok := body["target_summary"].(map[string]any)
	r.True(ok, "detail response must include target_summary")
	r.Equal("Album: Family", summary["label"])
	_, hasItemCount := summary["item_count"]
	r.False(hasItemCount)
}

func TestSharesGetMediaSetIncludesTargetSummary(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	fx := newSharesHTTPFixture(t)
	m := media.Media{
		ID: uuid.NewString(), Owner: fx.owner, Type: media.TypePhoto,
		MimeType: "image/jpeg", Path: "2024/" + uuid.NewString() + ".jpg",
		OriginalFilename: "x.jpg", ImportedAt: time.Now().UTC().Truncate(time.Second),
		Size: 100, Checksum: "cs" + uuid.NewString(), ThumbStatus: "pending",
	}
	r.NoError(fx.media.Insert(ctx, m))
	s, err := fx.shares.Create(ctx, service.CreateShareRequest{
		Grantee:    owners.Principal{Hub: "h", UserID: "alice"},
		TargetType: share.TargetMediaSet, MediaIDs: []string{m.ID},
	}, fx.owner)
	r.NoError(err)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/shares/"+s.UUID, nil)
	rec := httptest.NewRecorder()
	fx.h.ServeHTTP(rec, req)
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	var body map[string]any
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &body))
	summary, ok := body["target_summary"].(map[string]any)
	r.True(ok, "media_set detail must include target_summary")
	r.Equal("1 photo", summary["label"])
	r.EqualValues(1, summary["item_count"])
}

// TestSharesListNextOffsetPaginates walks /api/v1/shares with limit=2
// across five media_set scopes. Pages 1 and 2 (each two items) must
// emit next_offset; the final page (one item) must omit it. This pins
// the limit+1 sniff so a coincidentally-full final page is not
// mis-reported as having more.
func TestSharesListNextOffsetPaginates(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	fx := newSharesHTTPFixture(t)

	// Seed five media rows owned by fx.owner and mint one media_set
	// scope per row so List has five scopes to paginate over.
	for range 5 {
		m := media.Media{
			ID: uuid.NewString(), Owner: fx.owner, Type: media.TypePhoto,
			MimeType: "image/jpeg", Path: "2024/" + uuid.NewString() + ".jpg",
			OriginalFilename: "x.jpg", ImportedAt: time.Now().UTC().Truncate(time.Second),
			Size: 100, Checksum: "cs" + uuid.NewString(), ThumbStatus: "pending",
		}
		r.NoError(fx.media.Insert(ctx, m))
		_, err := fx.shares.Create(ctx, service.CreateShareRequest{
			Grantee: owners.Principal{Hub: "h", UserID: "alice"}, TargetType: share.TargetMediaSet,
			MediaIDs: []string{m.ID},
		}, fx.owner)
		r.NoError(err)
	}

	type listPage struct {
		Items      []map[string]any `json:"items"`
		NextOffset *int             `json:"next_offset"`
	}
	get := func(url string) listPage {
		req := httptest.NewRequest(http.MethodGet, url, nil)
		rec := httptest.NewRecorder()
		fx.h.ServeHTTP(rec, req)
		r.Equal(http.StatusOK, rec.Code, rec.Body.String())
		var resp listPage
		r.NoError(json.Unmarshal(rec.Body.Bytes(), &resp))
		return resp
	}

	page1 := get("/api/v1/shares?limit=2&offset=0")
	r.Len(page1.Items, 2)
	r.NotNil(page1.NextOffset)
	r.Equal(2, *page1.NextOffset)

	page2 := get("/api/v1/shares?limit=2&offset=2")
	r.Len(page2.Items, 2)
	r.NotNil(page2.NextOffset)
	r.Equal(4, *page2.NextOffset)

	page3 := get("/api/v1/shares?limit=2&offset=4")
	r.Len(page3.Items, 1)
	r.Nil(page3.NextOffset, "final page must omit next_offset")
}
