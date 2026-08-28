package httpapi_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/album"
	"go.kenn.io/fotobank/internal/content"
	"go.kenn.io/fotobank/internal/db"
	"go.kenn.io/fotobank/internal/httpapi"
	"go.kenn.io/fotobank/internal/identity"
	"go.kenn.io/fotobank/internal/media"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/service"
	"go.kenn.io/fotobank/internal/share"
	"go.kenn.io/fotobank/internal/storage"
	"go.kenn.io/fotobank/internal/testutil"
	"go.kenn.io/fotobank/internal/testutil/assetfixture"
	"go.kenn.io/fotobank/internal/thumb"
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
	content *content.Adapter
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
		{Hub: "h", UserID: "alice"}: "550e8400-e29b-41d4-a716-44665544000e",
		{Hub: "h", UserID: "bob"}:   "00000000-0000-4000-8000-61db0d8bb01d",
	})
	contentStore, err := content.Open(context.Background(), content.Config{Root: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, contentStore.Close()) })
	return sharedFxInputs{
		t: t, d: d, shares: shares, mediaR: mRepo, albumsR: aRepo,
		store: store, content: contentStore, now: now,
	}
}

// buildSharedFx wires a SharedReadService over the inputs and returns a
// running http.Handler whose IdentityProvider always reports grantee +
// scopes. Tests seed before this step so the scopes slice lines up with
// UUIDs returned from share.Repo.Insert.
func buildSharedFx(in sharedFxInputs, grantee owners.Principal, scopes []string) http.Handler {
	in.t.Helper()
	resolver := share.NewScopeResolver(in.shares, func() time.Time { return in.now }, nil)
	sharedSvc := service.NewSharedReadService(in.shares, in.mediaR, in.albumsR, in.store, in.content, resolver)
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
	repo := media.NewRepo(rw, rw)
	m := media.Media{
		ID: uuid.NewString(), Owner: p, Type: media.TypePhoto,
		MimeType:         "image/jpeg",
		OriginalFilename: "x.jpg",
		ImportedAt:       time.Now().UTC().Truncate(time.Second),
		ThumbStatus:      "pending",
	}
	m = assetfixture.Insert(t, repo, m)
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
		id := uuid.NewString()
		assetfixture.Insert(t, mRepo, media.Media{
			ID: id, Owner: owner, Type: media.TypePhoto,
			MimeType:         "image/jpeg",
			OriginalFilename: "x.jpg",
			ImportedAt:       now, Timestamp: &ts,
			ThumbStatus: "pending",
		})
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
	sharedHTTPSeedOwner(t, in.d.WriteDB(), bob, "00000000-0000-4000-8000-61db0d8bb01d")

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
	sharedHTTPSeedOwner(t, in.d.WriteDB(), alice, "550e8400-e29b-41d4-a716-44665544000e")
	sharedHTTPSeedOwner(t, in.d.WriteDB(), bob, "00000000-0000-4000-8000-61db0d8bb01d")
	sharedHTTPSeedOwner(t, in.d.WriteDB(), charlie, "00000000-0000-4000-8000-96616be8194d")

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
	sharedHTTPSeedOwner(t, in.d.WriteDB(), alice, "550e8400-e29b-41d4-a716-44665544000e")
	sharedHTTPSeedOwner(t, in.d.WriteDB(), bob, "00000000-0000-4000-8000-61db0d8bb01d")

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
	sharedHTTPSeedOwner(t, in.d.WriteDB(), alice, "550e8400-e29b-41d4-a716-44665544000e")
	sharedHTTPSeedOwner(t, in.d.WriteDB(), bob, "00000000-0000-4000-8000-61db0d8bb01d")

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
	sharedHTTPSeedOwner(t, in.d.WriteDB(), alice, "550e8400-e29b-41d4-a716-44665544000e")
	sharedHTTPSeedOwner(t, in.d.WriteDB(), bob, "00000000-0000-4000-8000-61db0d8bb01d")

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

// sharedHTTPSeedMediaTS inserts a media row with an explicit timestamp
// so display_time = COALESCE(timestamp, imported_at) is deterministic
// for pagination tests that seed multiple rows in one hub/user.
func sharedHTTPSeedMediaTS(t *testing.T, rw *sql.DB, p owners.Principal, ts time.Time) string {
	t.Helper()
	repo := media.NewRepo(rw, rw)
	m := media.Media{
		ID: uuid.NewString(), Owner: p, Type: media.TypePhoto,
		MimeType:         "image/jpeg",
		OriginalFilename: "x.jpg",
		ImportedAt:       time.Now().UTC().Truncate(time.Second),
		Timestamp:        &ts,
		ThumbStatus:      "pending",
	}
	m = assetfixture.Insert(t, repo, m)
	return m.ID
}

func TestSharedHTTPListMediaPaginates(t *testing.T) {
	r := require.New(t)
	in := setupSharedFxInputs(t)
	alice := owners.Principal{Hub: "h", UserID: "alice"}
	bob := owners.Principal{Hub: "h", UserID: "bob"}
	sharedHTTPSeedOwner(t, in.d.WriteDB(), alice, "550e8400-e29b-41d4-a716-44665544000e")
	sharedHTTPSeedOwner(t, in.d.WriteDB(), bob, "00000000-0000-4000-8000-61db0d8bb01d")

	t0 := in.now.Add(-2 * time.Hour)
	t1 := in.now.Add(-1 * time.Hour)
	t2 := in.now
	m1 := sharedHTTPSeedMediaTS(t, in.d.WriteDB(), alice, t0)
	m2 := sharedHTTPSeedMediaTS(t, in.d.WriteDB(), alice, t1)
	m3 := sharedHTTPSeedMediaTS(t, in.d.WriteDB(), alice, t2)
	s := sharedHTTPMakeMediaSetScope(t, in.shares, alice, bob, in.now, true, m1, m2, m3)
	sharedHTTPBumpActive(t, in.d.WriteDB(), s.UUID, in.now)

	h := buildSharedFx(in, bob, []string{s.UUID})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/shared/media?limit=2", nil)
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
	// display_time DESC: newest first, so page 1 = [m3, m2].
	r.Equal(m3, page1.Items[0]["id"])
	r.Equal(m2, page1.Items[1]["id"])

	req = httptest.NewRequest(http.MethodGet,
		"/api/v1/shared/media?limit=2"+
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
	r.Equal(m1, page2.Items[0]["id"])
	r.False(page2.HasMore)
	r.Empty(page2.NextCursorID)
}

// Partial cursors (only one of cursor_time / cursor_id set) must be
// rejected with 400; a half-cursor cannot skip deterministically given
// the (display_time DESC, id ASC) tuple ordering.
func TestSharedHTTPListMediaPartialCursorReturns400(t *testing.T) {
	r := require.New(t)
	in := setupSharedFxInputs(t)
	alice := owners.Principal{Hub: "h", UserID: "alice"}
	bob := owners.Principal{Hub: "h", UserID: "bob"}
	sharedHTTPSeedOwner(t, in.d.WriteDB(), alice, "550e8400-e29b-41d4-a716-44665544000e")
	sharedHTTPSeedOwner(t, in.d.WriteDB(), bob, "00000000-0000-4000-8000-61db0d8bb01d")
	m := sharedHTTPSeedMedia(t, in.d.WriteDB(), alice)
	s := sharedHTTPMakeMediaSetScope(t, in.shares, alice, bob, in.now, false, m)
	sharedHTTPBumpActive(t, in.d.WriteDB(), s.UUID, in.now)
	h := buildSharedFx(in, bob, []string{s.UUID})

	// Only cursor_id — missing cursor_time.
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/shared/media?cursor_id="+m, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	r.Equal(http.StatusBadRequest, rec.Code, rec.Body.String())

	// Only cursor_time — missing cursor_id.
	req = httptest.NewRequest(http.MethodGet,
		"/api/v1/shared/media?cursor_time="+in.now.Format(time.RFC3339Nano), nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	r.Equal(http.StatusBadRequest, rec.Code, rec.Body.String())
}

func TestSharedHTTPGetMediaUnauthorizedReturns404(t *testing.T) {
	r := require.New(t)
	in := setupSharedFxInputs(t)
	alice := owners.Principal{Hub: "h", UserID: "alice"}
	bob := owners.Principal{Hub: "h", UserID: "bob"}
	sharedHTTPSeedOwner(t, in.d.WriteDB(), alice, "550e8400-e29b-41d4-a716-44665544000e")
	sharedHTTPSeedOwner(t, in.d.WriteDB(), bob, "00000000-0000-4000-8000-61db0d8bb01d")
	m := sharedHTTPSeedMedia(t, in.d.WriteDB(), alice)
	// No scope at all — bob must see 404, not a permission error.
	h := buildSharedFx(in, bob, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/shared/media/"+m, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	r.Equal(http.StatusNotFound, rec.Code, rec.Body.String())
}

func TestSharedHTTPGetMediaAuthorizedReturnsCanDownload(t *testing.T) {
	r := require.New(t)
	in := setupSharedFxInputs(t)
	alice := owners.Principal{Hub: "h", UserID: "alice"}
	bob := owners.Principal{Hub: "h", UserID: "bob"}
	sharedHTTPSeedOwner(t, in.d.WriteDB(), alice, "550e8400-e29b-41d4-a716-44665544000e")
	sharedHTTPSeedOwner(t, in.d.WriteDB(), bob, "00000000-0000-4000-8000-61db0d8bb01d")
	m := sharedHTTPSeedMedia(t, in.d.WriteDB(), alice)
	s := sharedHTTPMakeMediaSetScope(t, in.shares, alice, bob, in.now, true, m)
	sharedHTTPBumpActive(t, in.d.WriteDB(), s.UUID, in.now)

	h := buildSharedFx(in, bob, []string{s.UUID})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/shared/media/"+m, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	var body map[string]any
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &body))
	r.Equal(m, body["id"])
	r.Equal(true, body["can_download"])
	r.Equal("h", body["owner"].(map[string]any)["hub"])
	r.Equal("alice", body["owner"].(map[string]any)["user_id"])
}

// sharedHTTPSeedStoredMedia inserts a ready asset whose exact original
// version exists in Docbank.
func sharedHTTPSeedStoredMedia(t *testing.T, in sharedFxInputs, p owners.Principal, body string) string {
	t.Helper()
	m := media.Media{
		ID: uuid.NewString(), Owner: p, Type: media.TypePhoto,
		MimeType:         "image/jpeg",
		OriginalFilename: "x.jpg",
		ImportedAt:       time.Now().UTC().Truncate(time.Second),
		ThumbStatus:      "pending",
	}
	m = assetfixture.InsertContent(t, in.mediaR, in.content, []byte(body), m)
	return m.ID
}

// sharedHTTPSeedMediaWithReadyThumb seeds a media row with
// thumb_status='ready' and writes thumb bytes into the fixture's
// storage at thumb.ThumbKey. Returns (id, version).
func sharedHTTPSeedMediaWithReadyThumb(t *testing.T, in sharedFxInputs, p owners.Principal, body string) (string, int) {
	t.Helper()
	version := 1
	updatedAt := time.Now().UTC().Truncate(time.Second)
	m := media.Media{
		ID: uuid.NewString(), Owner: p, Type: media.TypePhoto,
		MimeType:         "image/jpeg",
		OriginalFilename: "x.jpg",
		ImportedAt:       updatedAt,
		ThumbStatus:      "pending", ThumbVersion: version,
	}
	m = assetfixture.Insert(t, in.mediaR, m)
	_, err := in.d.WriteDB().ExecContext(context.Background(),
		`UPDATE assets SET thumb_status='ready', thumb_updated_at=? WHERE id=?`,
		updatedAt, m.ID)
	require.NoError(t, err)
	key := thumb.ThumbKey(m.ID, version, thumb.SizeGrid)
	_, err = in.store.Write(context.Background(), p, key, bytes.NewReader([]byte(body)))
	require.NoError(t, err)
	return m.ID, version
}

func TestSharedHTTPThumbAuthorized(t *testing.T) {
	r := require.New(t)
	in := setupSharedFxInputs(t)
	alice := owners.Principal{Hub: "h", UserID: "alice"}
	bob := owners.Principal{Hub: "h", UserID: "bob"}
	sharedHTTPSeedOwner(t, in.d.WriteDB(), alice, "550e8400-e29b-41d4-a716-44665544000e")
	sharedHTTPSeedOwner(t, in.d.WriteDB(), bob, "00000000-0000-4000-8000-61db0d8bb01d")
	mID, version := sharedHTTPSeedMediaWithReadyThumb(t, in, alice, "PNGBYTES")
	// download=false — thumbs ignore the flag.
	s := sharedHTTPMakeMediaSetScope(t, in.shares, alice, bob, in.now, false, mID)
	sharedHTTPBumpActive(t, in.d.WriteDB(), s.UUID, in.now)

	h := buildSharedFx(in, bob, []string{s.UUID})

	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/shared/media/"+mID+"/thumb?size=grid&v="+strconv.Itoa(version), nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())
	r.Equal("image/jpeg", rec.Result().Header.Get("Content-Type"))
	r.Equal("no-store", rec.Result().Header.Get("Cache-Control"))
	r.Equal("PNGBYTES", rec.Body.String())
}

func TestSharedHTTPThumbUnauthorizedReturns404(t *testing.T) {
	r := require.New(t)
	in := setupSharedFxInputs(t)
	alice := owners.Principal{Hub: "h", UserID: "alice"}
	bob := owners.Principal{Hub: "h", UserID: "bob"}
	sharedHTTPSeedOwner(t, in.d.WriteDB(), alice, "550e8400-e29b-41d4-a716-44665544000e")
	sharedHTTPSeedOwner(t, in.d.WriteDB(), bob, "00000000-0000-4000-8000-61db0d8bb01d")
	mID, _ := sharedHTTPSeedMediaWithReadyThumb(t, in, alice, "PNGBYTES")

	h := buildSharedFx(in, bob, nil) // no scopes
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/shared/media/"+mID+"/thumb?size=grid&v=1", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	r.Equal(http.StatusNotFound, rec.Code, rec.Body.String())
	r.Equal("no-store", rec.Result().Header.Get("Cache-Control"))
}

func TestSharedHTTPOriginalAllowDownloadTrue(t *testing.T) {
	r := require.New(t)
	in := setupSharedFxInputs(t)
	alice := owners.Principal{Hub: "h", UserID: "alice"}
	bob := owners.Principal{Hub: "h", UserID: "bob"}
	sharedHTTPSeedOwner(t, in.d.WriteDB(), alice, "550e8400-e29b-41d4-a716-44665544000e")
	sharedHTTPSeedOwner(t, in.d.WriteDB(), bob, "00000000-0000-4000-8000-61db0d8bb01d")
	mID := sharedHTTPSeedStoredMedia(t, in, alice, "photobytes")
	s := sharedHTTPMakeMediaSetScope(t, in.shares, alice, bob, in.now, true, mID)
	sharedHTTPBumpActive(t, in.d.WriteDB(), s.UUID, in.now)

	h := buildSharedFx(in, bob, []string{s.UUID})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/shared/media/"+mID+"/original", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())
	body, err := io.ReadAll(rec.Result().Body)
	r.NoError(err)
	r.Equal("photobytes", string(body))
	r.Equal("no-store", rec.Result().Header.Get("Cache-Control"))
	r.Equal("bytes", rec.Result().Header.Get("Accept-Ranges"))
}

func TestSharedHTTPOriginalAllowDownloadFalseReturns403(t *testing.T) {
	r := require.New(t)
	in := setupSharedFxInputs(t)
	alice := owners.Principal{Hub: "h", UserID: "alice"}
	bob := owners.Principal{Hub: "h", UserID: "bob"}
	sharedHTTPSeedOwner(t, in.d.WriteDB(), alice, "550e8400-e29b-41d4-a716-44665544000e")
	sharedHTTPSeedOwner(t, in.d.WriteDB(), bob, "00000000-0000-4000-8000-61db0d8bb01d")
	mID := sharedHTTPSeedStoredMedia(t, in, alice, "photobytes")
	s := sharedHTTPMakeMediaSetScope(t, in.shares, alice, bob, in.now, false, mID)
	sharedHTTPBumpActive(t, in.d.WriteDB(), s.UUID, in.now)

	h := buildSharedFx(in, bob, []string{s.UUID})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/shared/media/"+mID+"/original", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	r.Equal(http.StatusForbidden, rec.Code, rec.Body.String())
}

func TestSharedHTTPOriginalRangeReturns206(t *testing.T) {
	r := require.New(t)
	in := setupSharedFxInputs(t)
	alice := owners.Principal{Hub: "h", UserID: "alice"}
	bob := owners.Principal{Hub: "h", UserID: "bob"}
	sharedHTTPSeedOwner(t, in.d.WriteDB(), alice, "550e8400-e29b-41d4-a716-44665544000e")
	sharedHTTPSeedOwner(t, in.d.WriteDB(), bob, "00000000-0000-4000-8000-61db0d8bb01d")
	mID := sharedHTTPSeedStoredMedia(t, in, alice, "0123456789")
	s := sharedHTTPMakeMediaSetScope(t, in.shares, alice, bob, in.now, true, mID)
	sharedHTTPBumpActive(t, in.d.WriteDB(), s.UUID, in.now)

	h := buildSharedFx(in, bob, []string{s.UUID})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/shared/media/"+mID+"/original", nil)
	req.Header.Set("Range", "bytes=2-5")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	r.Equal(http.StatusPartialContent, rec.Code, rec.Body.String())
	r.Equal("bytes 2-5/10", rec.Result().Header.Get("Content-Range"))
	r.Equal("2345", rec.Body.String())
}

func TestSharedHTTPOriginalUnsatisfiableRangeReturns416(t *testing.T) {
	r := require.New(t)
	in := setupSharedFxInputs(t)
	alice := owners.Principal{Hub: "h", UserID: "alice"}
	bob := owners.Principal{Hub: "h", UserID: "bob"}
	sharedHTTPSeedOwner(t, in.d.WriteDB(), alice, "550e8400-e29b-41d4-a716-44665544000e")
	sharedHTTPSeedOwner(t, in.d.WriteDB(), bob, "00000000-0000-4000-8000-61db0d8bb01d")
	mID := sharedHTTPSeedStoredMedia(t, in, alice, "0123456789")
	s := sharedHTTPMakeMediaSetScope(t, in.shares, alice, bob, in.now, true, mID)
	sharedHTTPBumpActive(t, in.d.WriteDB(), s.UUID, in.now)

	h := buildSharedFx(in, bob, []string{s.UUID})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/shared/media/"+mID+"/original", nil)
	req.Header.Set("Range", "bytes=999-")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	r.Equal(http.StatusRequestedRangeNotSatisfiable, rec.Code, rec.Body.String())
	r.Equal("bytes */10", rec.Result().Header.Get("Content-Range"))
}

// sharedHTTPHideMedia stamps hidden_at on a media row.
func sharedHTTPHideMedia(t *testing.T, rw *sql.DB, mediaID string) {
	t.Helper()
	_, err := rw.ExecContext(context.Background(),
		`UPDATE assets SET hidden_at = ? WHERE id = ?`, time.Now().UTC(), mediaID)
	require.NoError(t, err)
}

// TestSharedHTTPListMediaExcludesHidden verifies that hidden shared
// photos are absent from GET /api/v1/shared/media.
func TestSharedHTTPListMediaExcludesHidden(t *testing.T) {
	r := require.New(t)
	in := setupSharedFxInputs(t)
	alice := owners.Principal{Hub: "h", UserID: "alice"}
	bob := owners.Principal{Hub: "h", UserID: "bob"}
	sharedHTTPSeedOwner(t, in.d.WriteDB(), alice, "550e8400-e29b-41d4-a716-44665544000e")
	sharedHTTPSeedOwner(t, in.d.WriteDB(), bob, "00000000-0000-4000-8000-61db0d8bb01d")

	visible := sharedHTTPSeedMedia(t, in.d.WriteDB(), alice)
	hidden := sharedHTTPSeedMedia(t, in.d.WriteDB(), alice)
	sharedHTTPHideMedia(t, in.d.WriteDB(), hidden)

	s := sharedHTTPMakeMediaSetScope(t, in.shares, alice, bob, in.now, false, visible, hidden)
	sharedHTTPBumpActive(t, in.d.WriteDB(), s.UUID, in.now)

	h := buildSharedFx(in, bob, []string{s.UUID})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/shared/media", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	var resp struct {
		Items []map[string]any `json:"items"`
	}
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &resp))
	r.Len(resp.Items, 1)
	r.Equal(visible, resp.Items[0]["id"])
}

// TestSharedHTTPListAlbumMediaExcludesHidden verifies that hidden album
// members are absent from GET /api/v1/shared/albums/{id}/media.
func TestSharedHTTPListAlbumMediaExcludesHidden(t *testing.T) {
	r := require.New(t)
	in := setupSharedFxInputs(t)
	alice := owners.Principal{Hub: "h", UserID: "alice"}
	bob := owners.Principal{Hub: "h", UserID: "bob"}
	sharedHTTPSeedOwner(t, in.d.WriteDB(), alice, "550e8400-e29b-41d4-a716-44665544000e")
	sharedHTTPSeedOwner(t, in.d.WriteDB(), bob, "00000000-0000-4000-8000-61db0d8bb01d")

	albumID, mIDs := sharedHTTPSeedAlbum(t, in.d.WriteDB(), alice, in.now, 2)
	sharedHTTPHideMedia(t, in.d.WriteDB(), mIDs[1])

	live := sharedHTTPMakeAlbumLiveScope(t, in.shares, alice, bob, albumID, in.now, false)
	sharedHTTPBumpActive(t, in.d.WriteDB(), live.UUID, in.now)

	h := buildSharedFx(in, bob, []string{live.UUID})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/shared/albums/"+albumID+"/media", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	var resp struct {
		Items []map[string]any `json:"items"`
	}
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &resp))
	r.Len(resp.Items, 1, "hidden album member must be excluded from shared album media")
	r.Equal(mIDs[0], resp.Items[0]["id"])
}

// TestSharedHTTPGetMediaHiddenReturns404 verifies that GET
// /api/v1/shared/media/{id} returns 404 for a hidden shared photo.
func TestSharedHTTPGetMediaHiddenReturns404(t *testing.T) {
	r := require.New(t)
	in := setupSharedFxInputs(t)
	alice := owners.Principal{Hub: "h", UserID: "alice"}
	bob := owners.Principal{Hub: "h", UserID: "bob"}
	sharedHTTPSeedOwner(t, in.d.WriteDB(), alice, "550e8400-e29b-41d4-a716-44665544000e")
	sharedHTTPSeedOwner(t, in.d.WriteDB(), bob, "00000000-0000-4000-8000-61db0d8bb01d")

	mID := sharedHTTPSeedMedia(t, in.d.WriteDB(), alice)
	sharedHTTPHideMedia(t, in.d.WriteDB(), mID)
	s := sharedHTTPMakeMediaSetScope(t, in.shares, alice, bob, in.now, true, mID)
	sharedHTTPBumpActive(t, in.d.WriteDB(), s.UUID, in.now)

	h := buildSharedFx(in, bob, []string{s.UUID})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/shared/media/"+mID, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	r.Equal(http.StatusNotFound, rec.Code, rec.Body.String())
}

// TestSharedHTTPOriginalHiddenReturns404 verifies that GET
// /api/v1/shared/media/{id}/original returns 404 for a hidden photo.
func TestSharedHTTPOriginalHiddenReturns404(t *testing.T) {
	r := require.New(t)
	in := setupSharedFxInputs(t)
	alice := owners.Principal{Hub: "h", UserID: "alice"}
	bob := owners.Principal{Hub: "h", UserID: "bob"}
	sharedHTTPSeedOwner(t, in.d.WriteDB(), alice, "550e8400-e29b-41d4-a716-44665544000e")
	sharedHTTPSeedOwner(t, in.d.WriteDB(), bob, "00000000-0000-4000-8000-61db0d8bb01d")

	mID := sharedHTTPSeedStoredMedia(t, in, alice, "photobytes")
	sharedHTTPHideMedia(t, in.d.WriteDB(), mID)
	s := sharedHTTPMakeMediaSetScope(t, in.shares, alice, bob, in.now, true, mID)
	sharedHTTPBumpActive(t, in.d.WriteDB(), s.UUID, in.now)

	h := buildSharedFx(in, bob, []string{s.UUID})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/shared/media/"+mID+"/original", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	r.Equal(http.StatusNotFound, rec.Code, rec.Body.String())
}

// TestSharedHTTPThumbHiddenReturns404 verifies that GET
// /api/v1/shared/media/{id}/thumb returns 404 for a hidden photo.
func TestSharedHTTPThumbHiddenReturns404(t *testing.T) {
	r := require.New(t)
	in := setupSharedFxInputs(t)
	alice := owners.Principal{Hub: "h", UserID: "alice"}
	bob := owners.Principal{Hub: "h", UserID: "bob"}
	sharedHTTPSeedOwner(t, in.d.WriteDB(), alice, "550e8400-e29b-41d4-a716-44665544000e")
	sharedHTTPSeedOwner(t, in.d.WriteDB(), bob, "00000000-0000-4000-8000-61db0d8bb01d")

	mID, version := sharedHTTPSeedMediaWithReadyThumb(t, in, alice, "thumbbytes")
	sharedHTTPHideMedia(t, in.d.WriteDB(), mID)
	s := sharedHTTPMakeMediaSetScope(t, in.shares, alice, bob, in.now, false, mID)
	sharedHTTPBumpActive(t, in.d.WriteDB(), s.UUID, in.now)

	h := buildSharedFx(in, bob, []string{s.UUID})
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/shared/media/"+mID+"/thumb?size=grid&v="+strconv.Itoa(version), nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	r.Equal(http.StatusNotFound, rec.Code, rec.Body.String())
}
