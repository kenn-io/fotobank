package httpapi_test

import (
	"context"
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/content"
	"go.kenn.io/fotobank/internal/httpapi"
	"go.kenn.io/fotobank/internal/identity"
	"go.kenn.io/fotobank/internal/media"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/service"
	"go.kenn.io/fotobank/internal/testutil"
	"go.kenn.io/fotobank/internal/testutil/assetfixture"
)

// mediaOriginalFixture bundles everything a /original test needs: the
// running server, caller principal, media repo for seeding rows, and the
// backing store so tests can write bytes at the row's path.
type mediaOriginalFixture struct {
	srv     *httptest.Server
	owner   owners.Principal
	repo    *media.Repo
	content *content.Adapter
	rw      *sql.DB
}

func newMediaOriginalTest(t *testing.T) mediaOriginalFixture {
	t.Helper()
	d := testutil.OpenTestDB(t)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	p := owners.Principal{Hub: "h", UserID: "u"}
	_, err := d.WriteDB().ExecContext(context.Background(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		p.Hub, p.UserID, "550e8400-e29b-41d4-a716-446655440000", time.Now().UTC(),
	)
	require.NoError(t, err)
	contentStore, err := content.Open(context.Background(), content.Config{Root: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, contentStore.Close()) })
	svc := service.NewMediaService(repo, contentStore)
	idp := identity.NewStub(p, "Test User")
	h, err := httpapi.New(httpapi.Deps{IdentityProvider: idp, MediaService: svc})
	require.NoError(t, err)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return mediaOriginalFixture{srv: srv, owner: p, repo: repo, content: contentStore, rw: d.WriteDB()}
}

// seedOriginal inserts a media row for p with the given bytes written at
// its storage path and returns the row.
func seedOriginal(t *testing.T, fx mediaOriginalFixture, p owners.Principal, body []byte) media.Media {
	t.Helper()
	m := media.Media{
		ID:               uuid.NewString(),
		Owner:            p,
		Type:             media.TypePhoto,
		MimeType:         "image/jpeg",
		OriginalFilename: "x.jpg",
		ImportedAt:       time.Now().UTC().Truncate(time.Second),
		Size:             int64(len(body)),
		ThumbStatus:      "pending",
	}
	return assetfixture.InsertContent(t, fx.repo, fx.content, body, m)
}

func TestGetMediaOriginalReturnsFullBodyAndHeaders(t *testing.T) {
	r := require.New(t)
	fx := newMediaOriginalTest(t)

	body := []byte("hello")
	m := seedOriginal(t, fx, fx.owner, body)

	resp, err := http.Get(fx.srv.URL + "/api/v1/media/" + m.ID + "/original")
	r.NoError(err)
	defer resp.Body.Close()

	r.Equal(http.StatusOK, resp.StatusCode)
	gotBody, err := io.ReadAll(resp.Body)
	r.NoError(err)
	r.Equal(body, gotBody)

	r.Equal(`"`+m.SHA256+`"`, resp.Header.Get("ETag"))
	r.Equal("image/jpeg", resp.Header.Get("Content-Type"))
	r.Equal("5", resp.Header.Get("Content-Length"))
	r.Equal("private, no-cache", resp.Header.Get("Cache-Control"))
	r.Equal("bytes", resp.Header.Get("Accept-Ranges"))
	r.Equal(m.ImportedAt.UTC().Format(http.TimeFormat), resp.Header.Get("Last-Modified"))
}

func TestGetMediaOriginalIfNoneMatchReturns304(t *testing.T) {
	r := require.New(t)
	fx := newMediaOriginalTest(t)

	m := seedOriginal(t, fx, fx.owner, []byte("hello"))

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, fx.srv.URL+"/api/v1/media/"+m.ID+"/original", nil)
	r.NoError(err)
	req.Header.Set("If-None-Match", `"`+m.SHA256+`"`)
	resp, err := http.DefaultClient.Do(req)
	r.NoError(err)
	defer resp.Body.Close()

	r.Equal(http.StatusNotModified, resp.StatusCode)
	gotBody, err := io.ReadAll(resp.Body)
	r.NoError(err)
	r.Empty(gotBody)
}

func TestGetMediaOriginalRangeReturns206(t *testing.T) {
	r := require.New(t)
	fx := newMediaOriginalTest(t)

	body := []byte("0123456789")
	m := seedOriginal(t, fx, fx.owner, body)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, fx.srv.URL+"/api/v1/media/"+m.ID+"/original", nil)
	r.NoError(err)
	req.Header.Set("Range", "bytes=3-6")
	resp, err := http.DefaultClient.Do(req)
	r.NoError(err)
	defer resp.Body.Close()

	r.Equal(http.StatusPartialContent, resp.StatusCode)
	gotBody, err := io.ReadAll(resp.Body)
	r.NoError(err)
	r.Equal([]byte("3456"), gotBody)
	r.Equal("bytes 3-6/10", resp.Header.Get("Content-Range"))
	r.Equal("4", resp.Header.Get("Content-Length"))
}

func TestGetMediaOriginalSuffixRange(t *testing.T) {
	r := require.New(t)
	fx := newMediaOriginalTest(t)

	body := []byte("0123456789")
	m := seedOriginal(t, fx, fx.owner, body)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, fx.srv.URL+"/api/v1/media/"+m.ID+"/original", nil)
	r.NoError(err)
	req.Header.Set("Range", "bytes=-3")
	resp, err := http.DefaultClient.Do(req)
	r.NoError(err)
	defer resp.Body.Close()

	r.Equal(http.StatusPartialContent, resp.StatusCode)
	gotBody, err := io.ReadAll(resp.Body)
	r.NoError(err)
	r.Equal([]byte("789"), gotBody)
	r.Equal("bytes 7-9/10", resp.Header.Get("Content-Range"))
}

func TestGetMediaOriginalOpenRange(t *testing.T) {
	r := require.New(t)
	fx := newMediaOriginalTest(t)

	body := []byte("0123456789")
	m := seedOriginal(t, fx, fx.owner, body)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, fx.srv.URL+"/api/v1/media/"+m.ID+"/original", nil)
	r.NoError(err)
	req.Header.Set("Range", "bytes=7-")
	resp, err := http.DefaultClient.Do(req)
	r.NoError(err)
	defer resp.Body.Close()

	r.Equal(http.StatusPartialContent, resp.StatusCode)
	gotBody, err := io.ReadAll(resp.Body)
	r.NoError(err)
	r.Equal([]byte("789"), gotBody)
	r.Equal("bytes 7-9/10", resp.Header.Get("Content-Range"))
}

func TestGetMediaOriginalRangeOutOfBoundsReturns416(t *testing.T) {
	r := require.New(t)
	fx := newMediaOriginalTest(t)

	m := seedOriginal(t, fx, fx.owner, []byte("0123456789"))

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, fx.srv.URL+"/api/v1/media/"+m.ID+"/original", nil)
	r.NoError(err)
	req.Header.Set("Range", "bytes=100-200")
	resp, err := http.DefaultClient.Do(req)
	r.NoError(err)
	defer resp.Body.Close()

	r.Equal(http.StatusRequestedRangeNotSatisfiable, resp.StatusCode)
	r.Equal("bytes */10", resp.Header.Get("Content-Range"))
}

func TestGetEmptyMediaOriginalSuffixRangeReturns416(t *testing.T) {
	r := require.New(t)
	fx := newMediaOriginalTest(t)
	m := seedOriginal(t, fx, fx.owner, []byte{})

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, fx.srv.URL+"/api/v1/media/"+m.ID+"/original", nil)
	r.NoError(err)
	req.Header.Set("Range", "bytes=-3")
	resp, err := http.DefaultClient.Do(req)
	r.NoError(err)
	defer resp.Body.Close()

	r.Equal(http.StatusRequestedRangeNotSatisfiable, resp.StatusCode)
	r.Equal("bytes */0", resp.Header.Get("Content-Range"))
}

func TestGetMediaOriginalMalformedRangeReturns416(t *testing.T) {
	r := require.New(t)
	fx := newMediaOriginalTest(t)

	m := seedOriginal(t, fx, fx.owner, []byte("0123456789"))

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, fx.srv.URL+"/api/v1/media/"+m.ID+"/original", nil)
	r.NoError(err)
	req.Header.Set("Range", "bytes=abc")
	resp, err := http.DefaultClient.Do(req)
	r.NoError(err)
	defer resp.Body.Close()

	r.Equal(http.StatusRequestedRangeNotSatisfiable, resp.StatusCode)
}

func TestGetMediaOriginalNotFoundForOtherOwner(t *testing.T) {
	r := require.New(t)
	fx := newMediaOriginalTest(t)

	ownerB := owners.Principal{Hub: "h", UserID: "b"}
	_, err := fx.rw.ExecContext(context.Background(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		ownerB.Hub, ownerB.UserID, "550e8400-e29b-41d4-a716-446655440002", time.Now().UTC(),
	)
	r.NoError(err)
	mB := media.Media{
		ID:               uuid.NewString(),
		Owner:            ownerB,
		Type:             media.TypePhoto,
		MimeType:         "image/jpeg",
		OriginalFilename: "x.jpg",
		ImportedAt:       time.Now().UTC().Truncate(time.Second),
		Size:             5,
		ThumbStatus:      "pending",
	}
	mB = assetfixture.InsertContent(t, fx.repo, fx.content, []byte("other"), mB)

	resp, err := http.Get(fx.srv.URL + "/api/v1/media/" + mB.ID + "/original")
	r.NoError(err)
	defer resp.Body.Close()
	r.Equal(http.StatusNotFound, resp.StatusCode)
}
