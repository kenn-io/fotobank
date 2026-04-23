package httpapi_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/album"
	"github.com/wesm/fotobank/internal/errs"
	"github.com/wesm/fotobank/internal/httpapi"
	"github.com/wesm/fotobank/internal/identity"
	"github.com/wesm/fotobank/internal/media"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/service"
	"github.com/wesm/fotobank/internal/testutil"
)

func TestTranslateAlbumErrorOwnerMismatchMapsTo500(t *testing.T) {
	r := require.New(t)
	got := httpapi.TranslateAlbumErrorForTest(errs.ErrOwnerMismatch)
	r.NotNil(got)
	r.Equal(500, httpapi.StatusFrom(got))
	r.NotContains(got.Error(), errs.ErrOwnerMismatch.Error(),
		"500 body must not leak the sentinel message")

	r.Equal(403, httpapi.StatusFrom(httpapi.Translate(errs.ErrOwnerMismatch)))
}

func TestTranslateAlbumErrorInvalidSentinels(t *testing.T) {
	r := require.New(t)
	cases := []struct {
		in       error
		wantCode int
		wantBody string
	}{
		{album.ErrInvalidName, 400, "name must be 1..200 chars"},
		{album.ErrInvalidBatch, 400, "batch size must be 1..500"},
		{album.ErrInvalidSort, 400, "sort_by must be added or imported"},
	}
	for _, c := range cases {
		got := httpapi.TranslateAlbumErrorForTest(c.in)
		r.NotNil(got)
		r.Equal(c.wantCode, httpapi.StatusFrom(got), "%v", c.in)
		r.Equal(c.wantBody, got.Error())
		r.NotContains(got.Error(), "album:",
			"wire string must not echo the sentinel prefix")
	}
}

func TestTranslateAlbumErrorDelegatesToShared(t *testing.T) {
	r := require.New(t)
	r.Equal(404, httpapi.StatusFrom(httpapi.TranslateAlbumErrorForTest(errs.ErrNotFound)))
	r.Equal(500, httpapi.StatusFrom(httpapi.TranslateAlbumErrorForTest(errors.New("x"))))
	r.Nil(httpapi.TranslateAlbumErrorForTest(nil))
}

type albumsAPIFixture struct {
	srv   *httptest.Server
	owner owners.Principal
	svc   *service.AlbumService
	rw    *sql.DB
}

func newAlbumsAPIFixture(t *testing.T) albumsAPIFixture {
	t.Helper()
	d := testutil.OpenTestDB(t)
	p := owners.Principal{Hub: "h", UserID: "u"}
	_, err := d.WriteDB().ExecContext(context.Background(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		p.Hub, p.UserID, "sk", time.Now().UTC(),
	)
	require.NoError(t, err)
	aRepo := album.NewRepo(d.WriteDB(), d.ReadDB())
	mRepo := media.NewRepo(d.WriteDB(), d.ReadDB())
	svc := service.NewAlbumService(aRepo, mRepo)
	idp := identity.NewStub(p, "Test User")
	h, err := httpapi.New(httpapi.Deps{IdentityProvider: idp, AlbumService: svc})
	require.NoError(t, err)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return albumsAPIFixture{srv: srv, owner: p, svc: svc, rw: d.WriteDB()}
}

func TestCreateAlbumReturns201(t *testing.T) {
	r := require.New(t)
	fx := newAlbumsAPIFixture(t)

	body, _ := json.Marshal(map[string]string{"name": "Trip"})
	resp, err := http.Post(fx.srv.URL+"/api/v1/albums", "application/json", bytes.NewReader(body))
	r.NoError(err)
	defer resp.Body.Close()
	r.Equal(http.StatusCreated, resp.StatusCode)

	var got map[string]any
	r.NoError(json.NewDecoder(resp.Body).Decode(&got))
	r.Equal("Trip", got["name"])
	r.EqualValues(0, got["item_count"])
	r.NotContains(got, "cover")
}

func TestCreateAlbumInvalidName(t *testing.T) {
	r := require.New(t)
	fx := newAlbumsAPIFixture(t)

	body, _ := json.Marshal(map[string]string{"name": "  "})
	resp, err := http.Post(fx.srv.URL+"/api/v1/albums", "application/json", bytes.NewReader(body))
	r.NoError(err)
	defer resp.Body.Close()
	r.Equal(http.StatusBadRequest, resp.StatusCode)
}

func TestGetAlbumDetail(t *testing.T) {
	r := require.New(t)
	fx := newAlbumsAPIFixture(t)
	it, err := fx.svc.Create(context.Background(), fx.owner, "Trip")
	r.NoError(err)

	resp, err := http.Get(fx.srv.URL + "/api/v1/albums/" + it.ID)
	r.NoError(err)
	defer resp.Body.Close()
	r.Equal(http.StatusOK, resp.StatusCode)

	var got map[string]any
	r.NoError(json.NewDecoder(resp.Body).Decode(&got))
	r.Equal(it.ID, got["id"])
}

func TestGetAlbumCrossOwnerReturns404(t *testing.T) {
	r := require.New(t)
	fx := newAlbumsAPIFixture(t)
	other := owners.Principal{Hub: "h", UserID: "o"}
	_, err := fx.rw.ExecContext(context.Background(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		other.Hub, other.UserID, "sk-o", time.Now().UTC(),
	)
	r.NoError(err)
	otherIt, err := fx.svc.Create(context.Background(), other, "Theirs")
	r.NoError(err)

	resp, err := http.Get(fx.srv.URL + "/api/v1/albums/" + otherIt.ID)
	r.NoError(err)
	defer resp.Body.Close()
	r.Equal(http.StatusNotFound, resp.StatusCode,
		"cross-owner must be 404, not 403")
}

func TestRenameAlbumReturnsUpdatedDTO(t *testing.T) {
	r := require.New(t)
	fx := newAlbumsAPIFixture(t)
	it, err := fx.svc.Create(context.Background(), fx.owner, "Old")
	r.NoError(err)

	body, _ := json.Marshal(map[string]string{"name": "New"})
	req, err := http.NewRequest(http.MethodPatch, fx.srv.URL+"/api/v1/albums/"+it.ID, bytes.NewReader(body))
	r.NoError(err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	r.NoError(err)
	defer resp.Body.Close()
	r.Equal(http.StatusOK, resp.StatusCode)

	var got map[string]any
	r.NoError(json.NewDecoder(resp.Body).Decode(&got))
	r.Equal("New", got["name"])
}

func TestDeleteAlbumReturns204(t *testing.T) {
	r := require.New(t)
	fx := newAlbumsAPIFixture(t)
	it, err := fx.svc.Create(context.Background(), fx.owner, "Trip")
	r.NoError(err)

	req, err := http.NewRequest(http.MethodDelete, fx.srv.URL+"/api/v1/albums/"+it.ID, nil)
	r.NoError(err)
	resp, err := http.DefaultClient.Do(req)
	r.NoError(err)
	defer resp.Body.Close()
	r.Equal(http.StatusNoContent, resp.StatusCode)
}

func TestListAlbumsIsolatesOwner(t *testing.T) {
	r := require.New(t)
	fx := newAlbumsAPIFixture(t)
	other := owners.Principal{Hub: "h", UserID: "o"}
	_, err := fx.rw.ExecContext(context.Background(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		other.Hub, other.UserID, "sk-o", time.Now().UTC(),
	)
	r.NoError(err)
	_, err = fx.svc.Create(context.Background(), fx.owner, "Mine")
	r.NoError(err)
	_, err = fx.svc.Create(context.Background(), other, "Theirs")
	r.NoError(err)

	resp, err := http.Get(fx.srv.URL + "/api/v1/albums")
	r.NoError(err)
	defer resp.Body.Close()
	r.Equal(http.StatusOK, resp.StatusCode)

	var got struct {
		Items []map[string]any `json:"items"`
	}
	r.NoError(json.NewDecoder(resp.Body).Decode(&got))
	r.Len(got.Items, 1)
	r.Equal("Mine", got.Items[0]["name"])
}
