package httpapi_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/auth/hidden"
	"go.kenn.io/fotobank/internal/httpapi"
	"go.kenn.io/fotobank/internal/identity"
	"go.kenn.io/fotobank/internal/media"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/service"
	"go.kenn.io/fotobank/internal/storage"
	"go.kenn.io/fotobank/internal/testutil"
	"go.kenn.io/fotobank/internal/thumb"
)

// hiddenMediaFixture wires up a full server with hidden auth + media
// service + thumb service so all the boundary tests can reuse it.
type hiddenMediaFixture struct {
	srv        *httptest.Server
	owner      owners.Principal
	repo       *media.Repo
	rw         *sql.DB
	store      *storage.NASOnly
	hiddenSvc  *hidden.Service
	hiddenRepo *hidden.Repo
	mediaSvc   *service.MediaService
	cookieCfg  hidden.CookieConfig
}

func newHiddenMediaFixture(t *testing.T) hiddenMediaFixture {
	t.Helper()
	d := testutil.OpenTestDB(t)
	p := owners.Principal{Hub: "h", UserID: "u"}
	_, err := d.WriteDB().ExecContext(context.Background(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		p.Hub, p.UserID, "550e8400-e29b-41d4-a716-446655440000", time.Now().UTC(),
	)
	require.NoError(t, err)

	store := storage.NewNASOnly(t.TempDir(), map[owners.Principal]string{p: "550e8400-e29b-41d4-a716-446655440000"})
	mediaRepo := media.NewRepo(d.WriteDB(), d.ReadDB())
	mediaSvc := service.NewMediaService(mediaRepo, store)

	thumbQ := thumb.NewQueue(d.WriteDB(), d.ReadDB())
	thumbSvc := service.NewThumbService(mediaRepo, thumbQ, store)

	hiddenRepo := hidden.NewRepo(d.WriteDB(), d.ReadDB())
	// MediaService implements MediaPrivacy via ClearAllHiddenForOwner.
	hiddenSvc := hidden.NewService(hiddenRepo, mediaSvc)
	cookieCfg := hidden.CookieConfigFor(true) // dev insecure for tests

	idp := identity.NewStub(p, "Test User")
	h, err := httpapi.New(httpapi.Deps{
		IdentityProvider:         idp,
		MediaService:             mediaSvc,
		ThumbService:             thumbSvc,
		HiddenAuth:               hiddenSvc,
		DevInsecureHiddenCookies: true,
	})
	require.NoError(t, err)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	return hiddenMediaFixture{
		srv:        srv,
		owner:      p,
		repo:       mediaRepo,
		rw:         d.WriteDB(),
		store:      store,
		hiddenSvc:  hiddenSvc,
		hiddenRepo: hiddenRepo,
		mediaSvc:   mediaSvc,
		cookieCfg:  cookieCfg,
	}
}

// setupHiddenAndUnlock seeds a passcode and returns an unlock cookie.
func setupHiddenAndUnlock(t *testing.T, fx hiddenMediaFixture) *http.Cookie {
	t.Helper()
	require.NoError(t, fx.hiddenSvc.Setup(context.Background(), fx.owner, "test-passcode"))
	resp := postJSON(t, fx.srv.URL+"/api/v1/auth/hidden/unlock", `{"passcode":"test-passcode"}`)
	defer resp.Body.Close()
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	for _, c := range resp.Cookies() {
		if c.Name == fx.cookieCfg.Name {
			return c
		}
	}
	require.FailNow(t, "no unlock cookie returned")
	return nil
}

// --- Detail (GET /api/v1/media/{id}) hidden boundary tests ---

func TestGetMediaHiddenReturns404WithoutCookie(t *testing.T) {
	r := require.New(t)
	fx := newHiddenMediaFixture(t)
	ctx := context.Background()

	m := seedMedia(t, fx.repo, fx.owner, "2024/h.jpg", "cs-h1", media.TypePhoto)
	r.NoError(fx.repo.SetHiddenCascade(ctx, fx.owner, []string{m.ID}, time.Now().UTC()))

	resp, err := http.Get(fx.srv.URL + "/api/v1/media/" + m.ID)
	r.NoError(err)
	defer resp.Body.Close()
	r.Equal(http.StatusNotFound, resp.StatusCode, "hidden media must return 404 without cookie")
}

func TestGetMediaHiddenReturns200WithCookie(t *testing.T) {
	r := require.New(t)
	fx := newHiddenMediaFixture(t)
	ctx := context.Background()

	m := seedMedia(t, fx.repo, fx.owner, "2024/h2.jpg", "cs-h2", media.TypePhoto)
	r.NoError(fx.repo.SetHiddenCascade(ctx, fx.owner, []string{m.ID}, time.Now().UTC()))

	cookie := setupHiddenAndUnlock(t, fx)

	resp := getWithCookie(t, fx.srv.URL+"/api/v1/media/"+m.ID, cookie)
	defer resp.Body.Close()
	r.Equal(http.StatusOK, resp.StatusCode, "hidden media must return 200 with valid unlock cookie")

	var body map[string]any
	r.NoError(json.NewDecoder(resp.Body).Decode(&body))
	r.Equal(m.ID, body["id"])
	// hidden_at must be present in the DTO.
	r.NotNil(body["hidden_at"], "hidden_at must be in the owner DTO when set")
}

// --- Original (GET /api/v1/media/{id}/original) hidden boundary tests ---

func TestGetMediaOriginalHiddenReturns404WithoutCookie(t *testing.T) {
	r := require.New(t)
	fx := newHiddenMediaFixture(t)
	ctx := context.Background()

	// The auth check happens before any bytes are read, so we don't
	// need to write actual bytes — the 404 fires at the Get step.
	m := seedMedia(t, fx.repo, fx.owner, "2024/oh.jpg", "cs-oh", media.TypePhoto)
	r.NoError(fx.repo.SetHiddenCascade(ctx, fx.owner, []string{m.ID}, time.Now().UTC()))

	resp, err := http.Get(fx.srv.URL + "/api/v1/media/" + m.ID + "/original")
	r.NoError(err)
	defer resp.Body.Close()
	r.Equal(http.StatusNotFound, resp.StatusCode)
}

func TestGetMediaOriginalHiddenReturns200WithCookie(t *testing.T) {
	r := require.New(t)
	fx := newHiddenMediaFixture(t)
	ctx := context.Background()

	// seedOriginal writes bytes and sizes the row correctly; use it to
	// avoid the Content-Length/body-size mismatch from seedMedia.
	payload := []byte("secret-bytes")
	// We need access to the underlying db to use seedOriginal, so we
	// use mediaOriginalFixture helper pattern directly.
	m := media.Media{
		ID:               uuid.NewString(),
		Owner:            fx.owner,
		Type:             media.TypePhoto,
		MimeType:         "image/jpeg",
		Path:             "2024/oh2.jpg",
		OriginalFilename: "oh2.jpg",
		ImportedAt:       time.Now().UTC().Truncate(time.Second),
		Size:             int64(len(payload)),
		Checksum:         "cs-oh2",
		ThumbStatus:      "pending",
	}
	require.NoError(t, fx.repo.Insert(ctx, m))
	_, err := fx.store.Write(ctx, fx.owner, "2024/oh2.jpg", bytes.NewReader(payload))
	r.NoError(err)
	r.NoError(fx.repo.SetHiddenCascade(ctx, fx.owner, []string{m.ID}, time.Now().UTC()))

	cookie := setupHiddenAndUnlock(t, fx)

	resp := getWithCookie(t, fx.srv.URL+"/api/v1/media/"+m.ID+"/original", cookie)
	defer resp.Body.Close()
	r.Equal(http.StatusOK, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	r.NoError(err)
	r.Equal(payload, body)
}

// --- Thumb hidden boundary tests ---

func newHiddenThumbFixture(t *testing.T) (hiddenMediaFixture, *thumb.Queue) {
	t.Helper()
	d := testutil.OpenTestDB(t)
	p := owners.Principal{Hub: "h", UserID: "u"}
	_, err := d.WriteDB().ExecContext(context.Background(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		p.Hub, p.UserID, "550e8400-e29b-41d4-a716-446655440000", time.Now().UTC(),
	)
	require.NoError(t, err)

	store := storage.NewNASOnly(t.TempDir(), map[owners.Principal]string{p: "550e8400-e29b-41d4-a716-446655440000"})
	mediaRepo := media.NewRepo(d.WriteDB(), d.ReadDB())
	mediaSvc := service.NewMediaService(mediaRepo, store)

	thumbQ := thumb.NewQueue(d.WriteDB(), d.ReadDB())
	thumbSvc := service.NewThumbService(mediaRepo, thumbQ, store)

	hiddenRepo := hidden.NewRepo(d.WriteDB(), d.ReadDB())
	hiddenSvc := hidden.NewService(hiddenRepo, mediaSvc)
	cookieCfg := hidden.CookieConfigFor(true)

	idp := identity.NewStub(p, "Test User")
	h, err := httpapi.New(httpapi.Deps{
		IdentityProvider:         idp,
		MediaService:             mediaSvc,
		ThumbService:             thumbSvc,
		HiddenAuth:               hiddenSvc,
		DevInsecureHiddenCookies: true,
	})
	require.NoError(t, err)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	fx := hiddenMediaFixture{
		srv:        srv,
		owner:      p,
		repo:       mediaRepo,
		store:      store,
		hiddenSvc:  hiddenSvc,
		hiddenRepo: hiddenRepo,
		mediaSvc:   mediaSvc,
		cookieCfg:  cookieCfg,
	}
	return fx, thumbQ
}

func TestGetMediaThumbHiddenReturns404WithoutCookie(t *testing.T) {
	r := require.New(t)
	fx, _ := newHiddenThumbFixture(t)
	ctx := context.Background()

	// Seed a "ready" thumb row.
	id := uuid.NewString()
	m := media.Media{
		ID:          id,
		Owner:       fx.owner,
		Type:        media.TypePhoto,
		MimeType:    "image/jpeg",
		Path:        "2024/" + id + ".jpg",
		ImportedAt:  time.Now().UTC(),
		Size:        1,
		Checksum:    id,
		ThumbStatus: "ready", ThumbVersion: 1,
	}
	require.NoError(t, fx.repo.Insert(ctx, m))
	key := thumb.ThumbKey(id, 1, thumb.SizeGrid)
	_, err := fx.store.Write(ctx, fx.owner, key, strings.NewReader("thumb-bytes"))
	r.NoError(err)
	r.NoError(fx.repo.SetHiddenCascade(ctx, fx.owner, []string{id}, time.Now().UTC()))

	resp, err := http.Get(fx.srv.URL + "/api/v1/media/" + id + "/thumb?v=1&size=grid")
	r.NoError(err)
	defer resp.Body.Close()
	r.Equal(http.StatusNotFound, resp.StatusCode)
}

func TestGetMediaThumbHiddenReturns200WithCookie(t *testing.T) {
	r := require.New(t)
	fx, _ := newHiddenThumbFixture(t)
	ctx := context.Background()

	id := uuid.NewString()
	m := media.Media{
		ID:          id,
		Owner:       fx.owner,
		Type:        media.TypePhoto,
		MimeType:    "image/jpeg",
		Path:        "2024/" + id + ".jpg",
		ImportedAt:  time.Now().UTC(),
		Size:        1,
		Checksum:    id,
		ThumbStatus: "ready", ThumbVersion: 1,
	}
	require.NoError(t, fx.repo.Insert(ctx, m))
	key := thumb.ThumbKey(id, 1, thumb.SizeGrid)
	payload := []byte("hidden-thumb")
	_, err := fx.store.Write(ctx, fx.owner, key, bytes.NewReader(payload))
	r.NoError(err)
	r.NoError(fx.repo.SetHiddenCascade(ctx, fx.owner, []string{id}, time.Now().UTC()))

	cookie := setupHiddenAndUnlock(t, fx)

	resp := getWithCookie(t, fx.srv.URL+"/api/v1/media/"+id+"/thumb?v=1&size=grid", cookie)
	defer resp.Body.Close()
	r.Equal(http.StatusOK, resp.StatusCode)
	body, readErr := io.ReadAll(resp.Body)
	r.NoError(readErr)
	r.Equal(payload, body)
}

// --- GET /api/v1/hidden/media ---

func TestListHiddenMediaReturns403WithoutCookie(t *testing.T) {
	r := require.New(t)
	fx := newHiddenMediaFixture(t)

	// No unlock cookie — even the list route must require it.
	resp, err := http.Get(fx.srv.URL + "/api/v1/hidden/media")
	r.NoError(err)
	defer resp.Body.Close()
	r.Equal(http.StatusForbidden, resp.StatusCode)
}

func TestListHiddenMediaReturnsItemsWithCookie(t *testing.T) {
	r := require.New(t)
	fx := newHiddenMediaFixture(t)
	ctx := context.Background()

	m1 := seedMedia(t, fx.repo, fx.owner, "2024/lh1.jpg", "cs-lh1", media.TypePhoto)
	m2 := seedMedia(t, fx.repo, fx.owner, "2024/lh2.jpg", "cs-lh2", media.TypePhoto)
	r.NoError(fx.repo.SetHiddenCascade(ctx, fx.owner, []string{m1.ID, m2.ID}, time.Now().UTC()))

	cookie := setupHiddenAndUnlock(t, fx)

	resp := getWithCookie(t, fx.srv.URL+"/api/v1/hidden/media", cookie)
	defer resp.Body.Close()
	r.Equal(http.StatusOK, resp.StatusCode)

	var out map[string]any
	r.NoError(json.NewDecoder(resp.Body).Decode(&out))
	items, ok := out["items"].([]any)
	r.True(ok, "items must be an array")
	r.Len(items, 2)
}

func TestListHiddenMediaNextOffset(t *testing.T) {
	r := require.New(t)
	fx := newHiddenMediaFixture(t)
	ctx := context.Background()

	// Seed 3 hidden rows; request limit=2 → next_offset should be 2.
	for i := range 3 {
		m := seedMedia(t, fx.repo, fx.owner,
			"2024/lhno"+string(rune('0'+i))+".jpg",
			"cs-lhno"+string(rune('0'+i)),
			media.TypePhoto,
		)
		r.NoError(fx.repo.SetHiddenCascade(ctx, fx.owner, []string{m.ID}, time.Now().UTC()))
	}

	cookie := setupHiddenAndUnlock(t, fx)

	resp := getWithCookie(t, fx.srv.URL+"/api/v1/hidden/media?limit=2", cookie)
	defer resp.Body.Close()
	r.Equal(http.StatusOK, resp.StatusCode)

	var out map[string]any
	r.NoError(json.NewDecoder(resp.Body).Decode(&out))
	items := out["items"].([]any)
	r.Len(items, 2)
	r.NotNil(out["next_offset"], "next_offset must be present when there are more rows")
	r.InDelta(float64(2), out["next_offset"], 0.001)
}

// --- POST /api/v1/media/hidden:bulk (Hide) ---

func TestHideMediaReturns409WhenNotConfigured(t *testing.T) {
	r := require.New(t)
	fx := newHiddenMediaFixture(t)
	ctx := context.Background()

	m := seedMedia(t, fx.repo, fx.owner, "2024/409.jpg", "cs-409", media.TypePhoto)

	// No passcode configured → 409.
	body, _ := json.Marshal(map[string]any{"media_ids": []string{m.ID}})
	resp, err := http.Post(
		fx.srv.URL+"/api/v1/media/hidden:bulk",
		"application/json",
		bytes.NewReader(body),
	)
	r.NoError(err)
	defer resp.Body.Close()
	_ = ctx
	r.Equal(http.StatusConflict, resp.StatusCode)
}

func TestHideMediaHidesPrimaryAndSidecar(t *testing.T) {
	r := require.New(t)
	fx := newHiddenMediaFixture(t)
	ctx := context.Background()

	primary := seedMedia(t, fx.repo, fx.owner, "2024/hp.jpg", "cs-hp", media.TypePhoto)
	sidecar := seedMedia(t, fx.repo, fx.owner, "2024/hp.dng", "cs-hs", media.TypePhoto)
	r.NoError(fx.repo.UpdatePairedWithID(ctx, sidecar.ID, &primary.ID))

	// Hide does NOT require unlock cookie — just a configured credential.
	r.NoError(fx.hiddenSvc.Setup(ctx, fx.owner, "pass"))

	body, _ := json.Marshal(map[string]any{"media_ids": []string{primary.ID}})
	resp, err := http.Post(
		fx.srv.URL+"/api/v1/media/hidden:bulk",
		"application/json",
		bytes.NewReader(body),
	)
	r.NoError(err)
	defer resp.Body.Close()
	r.Equal(http.StatusOK, resp.StatusCode)

	var out map[string]any
	r.NoError(json.NewDecoder(resp.Body).Decode(&out))
	succeeded := out["succeeded"].([]any)
	r.Len(succeeded, 1)
	r.Equal(primary.ID, succeeded[0])
	failed, ok := out["failed"]
	r.True(ok)
	r.Empty(failed.([]any))

	// Primary and sidecar both hidden.
	gotPrimary, err := fx.repo.GetByID(ctx, primary.ID)
	r.NoError(err)
	r.NotNil(gotPrimary.HiddenAt)
	gotSidecar, err := fx.repo.GetByID(ctx, sidecar.ID)
	r.NoError(err)
	r.NotNil(gotSidecar.HiddenAt)
}

// --- POST /api/v1/media/unhide:bulk (Unhide) ---

func TestUnhideMediaReturns403WithoutCookie(t *testing.T) {
	r := require.New(t)
	fx := newHiddenMediaFixture(t)
	ctx := context.Background()

	m := seedMedia(t, fx.repo, fx.owner, "2024/un403.jpg", "cs-un403", media.TypePhoto)
	r.NoError(fx.repo.SetHiddenCascade(ctx, fx.owner, []string{m.ID}, time.Now().UTC()))
	r.NoError(fx.hiddenSvc.Setup(ctx, fx.owner, "pass"))

	body, _ := json.Marshal(map[string]any{"media_ids": []string{m.ID}})
	resp, err := http.Post(
		fx.srv.URL+"/api/v1/media/unhide:bulk",
		"application/json",
		bytes.NewReader(body),
	)
	r.NoError(err)
	defer resp.Body.Close()
	r.Equal(http.StatusForbidden, resp.StatusCode)
}

func TestUnhideMediaSucceedsWithCookie(t *testing.T) {
	r := require.New(t)
	fx := newHiddenMediaFixture(t)
	ctx := context.Background()

	m := seedMedia(t, fx.repo, fx.owner, "2024/un200.jpg", "cs-un200", media.TypePhoto)
	r.NoError(fx.repo.SetHiddenCascade(ctx, fx.owner, []string{m.ID}, time.Now().UTC()))

	cookie := setupHiddenAndUnlock(t, fx)

	body, _ := json.Marshal(map[string]any{"media_ids": []string{m.ID}})
	req, err := http.NewRequest(http.MethodPost,
		fx.srv.URL+"/api/v1/media/unhide:bulk",
		bytes.NewReader(body),
	)
	r.NoError(err)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	resp, err := http.DefaultClient.Do(req)
	r.NoError(err)
	defer resp.Body.Close()
	r.Equal(http.StatusOK, resp.StatusCode)

	var out map[string]any
	r.NoError(json.NewDecoder(resp.Body).Decode(&out))
	succeeded := out["succeeded"].([]any)
	r.Len(succeeded, 1)
	r.Equal(m.ID, succeeded[0])

	// Row is now visible.
	got, err := fx.repo.GetByID(ctx, m.ID)
	r.NoError(err)
	r.Nil(got.HiddenAt)
}

// --- mediaDTO hidden_at field ---

func TestMediaDTOHiddenAtPresentForHiddenRow(t *testing.T) {
	r := require.New(t)
	fx := newHiddenMediaFixture(t)
	ctx := context.Background()

	m := seedMedia(t, fx.repo, fx.owner, "2024/dto-h.jpg", "cs-dto-h", media.TypePhoto)
	r.NoError(fx.repo.SetHiddenCascade(ctx, fx.owner, []string{m.ID}, time.Now().UTC()))

	cookie := setupHiddenAndUnlock(t, fx)
	resp := getWithCookie(t, fx.srv.URL+"/api/v1/media/"+m.ID, cookie)
	defer resp.Body.Close()
	r.Equal(http.StatusOK, resp.StatusCode)

	var body map[string]any
	r.NoError(json.NewDecoder(resp.Body).Decode(&body))
	r.NotNil(body["hidden_at"], "hidden_at must be present in owner DTO when media is hidden")
}

func TestMediaDTOHiddenAtAbsentForVisibleRow(t *testing.T) {
	r := require.New(t)
	fx := newHiddenMediaFixture(t)

	m := seedMedia(t, fx.repo, fx.owner, "2024/dto-v.jpg", "cs-dto-v", media.TypePhoto)

	resp, err := http.Get(fx.srv.URL + "/api/v1/media/" + m.ID)
	r.NoError(err)
	defer resp.Body.Close()
	r.Equal(http.StatusOK, resp.StatusCode)

	var body map[string]any
	r.NoError(json.NewDecoder(resp.Body).Decode(&body))
	_, hasHiddenAt := body["hidden_at"]
	r.False(hasHiddenAt, "hidden_at must be absent (omitempty) when media is visible")
}

// --- GET /api/v1/media list must never surface hidden rows ---

// TestListMediaNeverReturnsHiddenEvenWithUnlockCookie proves the HTTP
// boundary: even with a valid unlock cookie, GET /api/v1/media clamps
// IncludeHidden=false and omits hidden rows from the response.
func TestListMediaNeverReturnsHiddenEvenWithUnlockCookie(t *testing.T) {
	r := require.New(t)
	fx := newHiddenMediaFixture(t)
	ctx := context.Background()

	visible := seedMedia(t, fx.repo, fx.owner, "2024/vis.jpg", "cs-vis", media.TypePhoto)
	hidden := seedMedia(t, fx.repo, fx.owner, "2024/hid.jpg", "cs-hid", media.TypePhoto)
	r.NoError(fx.repo.SetHiddenCascade(ctx, fx.owner, []string{hidden.ID}, time.Now().UTC()))

	cookie := setupHiddenAndUnlock(t, fx)

	resp := getWithCookie(t, fx.srv.URL+"/api/v1/media", cookie)
	defer resp.Body.Close()
	r.Equal(http.StatusOK, resp.StatusCode)

	var out map[string]any
	r.NoError(json.NewDecoder(resp.Body).Decode(&out))
	items, ok := out["items"].([]any)
	r.True(ok, "items must be an array")

	ids := make([]string, 0, len(items))
	for _, item := range items {
		m, ok := item.(map[string]any)
		r.True(ok)
		ids = append(ids, m["id"].(string))
	}
	r.Contains(ids, visible.ID, "visible row must appear in list")
	r.NotContains(ids, hidden.ID, "hidden row must never appear in list even with unlock cookie")
}

// --- POST /api/v1/media/hidden:bulk DB error must not be masked as 409 ---

// TestHideMediaBulkBubbles5xxOnDBError verifies that when GetCredential
// returns a non-ErrNotFound error (e.g. a closed DB pool), the handler
// translates it as a 5xx and does NOT return 409 Conflict.
func TestHideMediaBulkBubbles5xxOnDBError(t *testing.T) {
	r := require.New(t)

	// Build the fixture inline so we have access to the db.DB and can
	// force a read-pool error after the server is wired up.
	d := testutil.OpenTestDB(t)
	p := owners.Principal{Hub: "h", UserID: "u"}
	_, err := d.WriteDB().ExecContext(context.Background(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		p.Hub, p.UserID, "550e8400-e29b-41d4-a716-446655440000", time.Now().UTC(),
	)
	r.NoError(err)

	store := storage.NewNASOnly(t.TempDir(), map[owners.Principal]string{p: "550e8400-e29b-41d4-a716-446655440000"})
	mediaRepo := media.NewRepo(d.WriteDB(), d.ReadDB())
	mediaSvc := service.NewMediaService(mediaRepo, store)
	thumbQ := thumb.NewQueue(d.WriteDB(), d.ReadDB())
	thumbSvc := service.NewThumbService(mediaRepo, thumbQ, store)
	hiddenRepo := hidden.NewRepo(d.WriteDB(), d.ReadDB())
	hiddenSvc := hidden.NewService(hiddenRepo, mediaSvc)

	idp := identity.NewStub(p, "Test User")
	h, err := httpapi.New(httpapi.Deps{
		IdentityProvider:         idp,
		MediaService:             mediaSvc,
		ThumbService:             thumbSvc,
		HiddenAuth:               hiddenSvc,
		DevInsecureHiddenCookies: true,
	})
	r.NoError(err)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	m := seedMedia(t, mediaRepo, p, "2024/dberr.jpg", "cs-dberr", media.TypePhoto)

	// Close the read pool to force a real DB error (not ErrNotFound) on
	// GetCredential — the handler must not convert this to 409.
	r.NoError(d.ReadDB().Close())

	body, _ := json.Marshal(map[string]any{"media_ids": []string{m.ID}})
	resp, err := http.Post(
		srv.URL+"/api/v1/media/hidden:bulk",
		"application/json",
		bytes.NewReader(body),
	)
	r.NoError(err)
	defer resp.Body.Close()
	r.NotEqual(http.StatusConflict, resp.StatusCode, "DB errors must not be masked as 409")
	r.GreaterOrEqual(resp.StatusCode, 500, "DB errors must produce a 5xx response")
}
