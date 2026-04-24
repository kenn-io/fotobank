package httpapi_test

import (
	"context"
	"database/sql"
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
	"github.com/wesm/fotobank/internal/storage"
	"github.com/wesm/fotobank/internal/testutil"
)

// sharedFxInputs carries the pre-built repos + storage + time needed to
// construct a shared-HTTP fixture. Tests seed scopes and albums via the
// repos here, collect the scope UUIDs that will be presented to the
// grantee, then call buildSharedFx to build the actual HTTP handler
// bound to that grantee and those scopes.
type sharedFxInputs struct {
	t       *testing.T
	d       *db.DB
	shares  *share.Repo
	mediaR  *media.Repo
	albumsR *album.Repo
	store   storage.Store
	now     time.Time
}

func setupSharedFxInputs(t *testing.T) sharedFxInputs {
	t.Helper()
	d := testutil.OpenTestDB(t)
	shares := share.NewRepo(d.WriteDB(), d.ReadDB())
	mRepo := media.NewRepo(d.WriteDB(), d.ReadDB())
	aRepo := album.NewRepo(d.WriteDB(), d.ReadDB())
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	// Storage keys must be seeded up front for every principal whose
	// bytes NASOnly will ever see. Fixture covers alice (owner) and bob
	// (grantee); other tests can extend it as needed.
	store := storage.NewNASOnly(t.TempDir(), map[owners.Principal]string{
		{Hub: "h", UserID: "alice"}: "alice-sk",
		{Hub: "h", UserID: "bob"}:   "bob-sk",
	})
	return sharedFxInputs{
		t: t, d: d, shares: shares, mediaR: mRepo, albumsR: aRepo,
		store: store, now: now,
	}
}

// buildSharedFx wires a SharedReadService over the inputs and returns a
// running http.Handler whose IdentityProvider always reports grantee +
// scopes. Tests seed before this step so the scopes slice lines up with
// UUIDs returned from share.Repo.Insert.
func buildSharedFx(in sharedFxInputs, grantee owners.Principal, scopes []string) http.Handler {
	in.t.Helper()
	resolver := share.NewScopeResolver(in.shares, func() time.Time { return in.now }, nil)
	sharedSvc := service.NewSharedReadService(in.shares, in.mediaR, in.albumsR, in.store, resolver)
	h, err := httpapi.New(httpapi.Deps{
		IdentityProvider: identity.NewStubWithScopes(grantee, "", scopes),
		SharedRead:       sharedSvc,
	})
	require.NoError(in.t, err)
	return h
}

// --- seeding helpers (mirrors of service/shared_read_service_test.go).

func sharedHTTPSeedOwner(t *testing.T, rw *sql.DB, p owners.Principal, sk string) {
	t.Helper()
	_, err := rw.ExecContext(context.Background(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		p.Hub, p.UserID, sk, time.Now().UTC())
	require.NoError(t, err)
}

func sharedHTTPSeedMedia(t *testing.T, rw *sql.DB, p owners.Principal) string {
	t.Helper()
	cs := uuid.NewString()
	repo := media.NewRepo(rw, rw)
	m := media.Media{
		ID: uuid.NewString(), Owner: p, Type: media.TypePhoto,
		MimeType: "image/jpeg", Path: "2024/" + cs + ".jpg",
		OriginalFilename: "x.jpg",
		ImportedAt:       time.Now().UTC().Truncate(time.Second),
		Size:             100, Checksum: cs, ThumbStatus: "pending",
	}
	require.NoError(t, repo.Insert(context.Background(), m))
	return m.ID
}

func sharedHTTPBumpActive(t *testing.T, rw *sql.DB, uuidStr string, at time.Time) {
	t.Helper()
	_, err := rw.ExecContext(context.Background(),
		`UPDATE scopes SET broker_status='active', broker_granted_at=?, broker_registered_at=? WHERE uuid=?`,
		at, at, uuidStr)
	require.NoError(t, err)
}

func sharedHTTPMakeMediaSetScope(
	t *testing.T, repo *share.Repo,
	owner, grantee owners.Principal, now time.Time, download bool, mediaIDs ...string,
) share.Scope {
	t.Helper()
	s := share.Scope{
		UUID: uuid.NewString(), Owner: owner, Grantee: grantee,
		TargetType:    share.TargetMediaSet,
		AllowDownload: download,
		CreatedAt:     now,
		BrokerStatus:  share.StatusPending,
	}
	require.NoError(t, repo.Insert(context.Background(), s, mediaIDs))
	return s
}

func sharedHTTPMakeAlbumLiveScope(
	t *testing.T, repo *share.Repo,
	owner, grantee owners.Principal, albumID string, now time.Time, download bool,
) share.Scope {
	t.Helper()
	s := share.Scope{
		UUID: uuid.NewString(), Owner: owner, Grantee: grantee,
		TargetType:    share.TargetAlbumLive,
		TargetAlbumID: &albumID,
		AllowDownload: download,
		CreatedAt:     now,
		BrokerStatus:  share.StatusPending,
	}
	require.NoError(t, repo.Insert(context.Background(), s, nil))
	return s
}

// sharedHTTPSeedAlbum inserts an album and attaches n media in baseTime-
// descending order. Returns (albumID, mediaIDs in newest-first order).
func sharedHTTPSeedAlbum(t *testing.T, rw *sql.DB, owner owners.Principal, baseTime time.Time, n int) (string, []string) {
	t.Helper()
	now := time.Now().UTC()
	albumID := uuid.NewString()
	_, err := rw.ExecContext(context.Background(),
		`INSERT INTO albums(id, owner_hub, owner_user_id, name, created_at, updated_at) VALUES(?,?,?,?,?,?)`,
		albumID, owner.Hub, owner.UserID, "t", now, now)
	require.NoError(t, err)
	mRepo := media.NewRepo(rw, rw)
	mediaIDs := make([]string, 0, n)
	for i := range n {
		ts := baseTime.Add(-time.Duration(i) * time.Minute)
		cs := uuid.NewString()
		id := uuid.NewString()
		require.NoError(t, mRepo.Insert(context.Background(), media.Media{
			ID: id, Owner: owner, Type: media.TypePhoto,
			MimeType: "image/jpeg", Path: "2024/" + cs + ".jpg",
			OriginalFilename: "x.jpg",
			ImportedAt:       now, Timestamp: &ts,
			Size: 100, Checksum: cs, ThumbStatus: "pending",
		}))
		_, err := rw.ExecContext(context.Background(),
			`INSERT INTO album_media(album_id, media_id, added_at) VALUES(?,?,?)`,
			albumID, id, now)
		require.NoError(t, err)
		mediaIDs = append(mediaIDs, id)
	}
	return albumID, mediaIDs
}

// --- tests ---

func TestSharedHTTPGetScopeUnknownReturns404(t *testing.T) {
	r := require.New(t)
	in := setupSharedFxInputs(t)
	bob := owners.Principal{Hub: "h", UserID: "bob"}
	sharedHTTPSeedOwner(t, in.d.WriteDB(), bob, "bob-sk")

	h := buildSharedFx(in, bob, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/shared/scopes/"+uuid.NewString(), nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	r.Equal(http.StatusNotFound, rec.Code, rec.Body.String())
}

func TestSharedHTTPListScopesReturnsAuthorized(t *testing.T) {
	r := require.New(t)
	in := setupSharedFxInputs(t)
	alice := owners.Principal{Hub: "h", UserID: "alice"}
	bob := owners.Principal{Hub: "h", UserID: "bob"}
	charlie := owners.Principal{Hub: "h", UserID: "charlie"}
	sharedHTTPSeedOwner(t, in.d.WriteDB(), alice, "alice-sk")
	sharedHTTPSeedOwner(t, in.d.WriteDB(), bob, "bob-sk")
	sharedHTTPSeedOwner(t, in.d.WriteDB(), charlie, "charlie-sk")

	m := sharedHTTPSeedMedia(t, in.d.WriteDB(), alice)
	bob1 := sharedHTTPMakeMediaSetScope(t, in.shares, alice, bob, in.now, false, m)
	sharedHTTPBumpActive(t, in.d.WriteDB(), bob1.UUID, in.now)
	bob2 := sharedHTTPMakeMediaSetScope(t, in.shares, alice, bob, in.now, true, m)
	sharedHTTPBumpActive(t, in.d.WriteDB(), bob2.UUID, in.now)
	// Scope granted to charlie — header-attested list for bob must not include it.
	other := sharedHTTPMakeMediaSetScope(t, in.shares, alice, charlie, in.now, false, m)
	sharedHTTPBumpActive(t, in.d.WriteDB(), other.UUID, in.now)

	h := buildSharedFx(in, bob, []string{bob1.UUID, bob2.UUID})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/shared/scopes", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	var resp struct {
		Items []map[string]any `json:"items"`
	}
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &resp))
	r.Len(resp.Items, 2)
	uuids := []string{resp.Items[0]["uuid"].(string), resp.Items[1]["uuid"].(string)}
	r.ElementsMatch([]string{bob1.UUID, bob2.UUID}, uuids)
	// List responses omit media_ids (detail-only field).
	for _, it := range resp.Items {
		_, present := it["media_ids"]
		r.False(present, "list items must not carry media_ids")
	}
}

func TestSharedHTTPListAlbumsReturnsAlbumLiveOnly(t *testing.T) {
	r := require.New(t)
	in := setupSharedFxInputs(t)
	alice := owners.Principal{Hub: "h", UserID: "alice"}
	bob := owners.Principal{Hub: "h", UserID: "bob"}
	sharedHTTPSeedOwner(t, in.d.WriteDB(), alice, "alice-sk")
	sharedHTTPSeedOwner(t, in.d.WriteDB(), bob, "bob-sk")

	albumID, mediaIDs := sharedHTTPSeedAlbum(t, in.d.WriteDB(), alice, in.now, 2)
	live := sharedHTTPMakeAlbumLiveScope(t, in.shares, alice, bob, albumID, in.now, false)
	sharedHTTPBumpActive(t, in.d.WriteDB(), live.UUID, in.now)
	// media_set over the same media must not surface as an album.
	ms := sharedHTTPMakeMediaSetScope(t, in.shares, alice, bob, in.now, false, mediaIDs...)
	sharedHTTPBumpActive(t, in.d.WriteDB(), ms.UUID, in.now)

	h := buildSharedFx(in, bob, []string{live.UUID, ms.UUID})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/shared/albums", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	var resp struct {
		Items []map[string]any `json:"items"`
	}
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &resp))
	r.Len(resp.Items, 1)
	r.Equal(albumID, resp.Items[0]["id"])
	r.EqualValues(2, resp.Items[0]["item_count"])
	r.Equal(false, resp.Items[0]["can_download"])
}

func TestSharedHTTPGetAlbumUnauthorizedReturns404(t *testing.T) {
	r := require.New(t)
	in := setupSharedFxInputs(t)
	alice := owners.Principal{Hub: "h", UserID: "alice"}
	bob := owners.Principal{Hub: "h", UserID: "bob"}
	sharedHTTPSeedOwner(t, in.d.WriteDB(), alice, "alice-sk")
	sharedHTTPSeedOwner(t, in.d.WriteDB(), bob, "bob-sk")

	albumID, _ := sharedHTTPSeedAlbum(t, in.d.WriteDB(), alice, in.now, 1)
	// No scope is seeded for bob, and the presented header scopes list is empty.
	h := buildSharedFx(in, bob, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/shared/albums/"+albumID, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	r.Equal(http.StatusNotFound, rec.Code, rec.Body.String())
}

func TestSharedHTTPListAlbumMediaPaginates(t *testing.T) {
	r := require.New(t)
	in := setupSharedFxInputs(t)
	alice := owners.Principal{Hub: "h", UserID: "alice"}
	bob := owners.Principal{Hub: "h", UserID: "bob"}
	sharedHTTPSeedOwner(t, in.d.WriteDB(), alice, "alice-sk")
	sharedHTTPSeedOwner(t, in.d.WriteDB(), bob, "bob-sk")

	albumID, _ := sharedHTTPSeedAlbum(t, in.d.WriteDB(), alice, in.now, 3)
	live := sharedHTTPMakeAlbumLiveScope(t, in.shares, alice, bob, albumID, in.now, false)
	sharedHTTPBumpActive(t, in.d.WriteDB(), live.UUID, in.now)

	h := buildSharedFx(in, bob, []string{live.UUID})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/shared/albums/"+albumID+"/media?limit=2", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	var page1 struct {
		Items          []map[string]any `json:"items"`
		NextCursorTime *time.Time       `json:"next_cursor_time,omitempty"`
		NextCursorID   string           `json:"next_cursor_id,omitempty"`
		HasMore        bool             `json:"has_more"`
	}
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &page1))
	r.Len(page1.Items, 2)
	r.True(page1.HasMore)
	r.NotEmpty(page1.NextCursorID)
	r.NotNil(page1.NextCursorTime)

	req = httptest.NewRequest(http.MethodGet,
		"/api/v1/shared/albums/"+albumID+"/media?limit=2"+
			"&cursor_time="+page1.NextCursorTime.UTC().Format(time.RFC3339Nano)+
			"&cursor_id="+page1.NextCursorID,
		nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	var page2 struct {
		Items          []map[string]any `json:"items"`
		NextCursorTime *time.Time       `json:"next_cursor_time,omitempty"`
		NextCursorID   string           `json:"next_cursor_id,omitempty"`
		HasMore        bool             `json:"has_more"`
	}
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &page2))
	r.Len(page2.Items, 1)
	r.False(page2.HasMore)
	r.Empty(page2.NextCursorID)
}
