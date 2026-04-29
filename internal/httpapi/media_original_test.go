package httpapi_test

import (
	"bytes"
	"context"
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/httpapi"
	"github.com/wesm/fotobank/internal/identity"
	"github.com/wesm/fotobank/internal/media"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/service"
	"github.com/wesm/fotobank/internal/storage"
	"github.com/wesm/fotobank/internal/testutil"
)

// mediaOriginalFixture bundles everything a /original test needs: the
// running server, caller principal, media repo for seeding rows, and the
// backing store so tests can write bytes at the row's path.
type mediaOriginalFixture struct {
	srv   *httptest.Server
	owner owners.Principal
	repo  *media.Repo
	store storage.Store
	rw    *sql.DB
}

func newMediaOriginalTest(t *testing.T) mediaOriginalFixture {
	t.Helper()
	d := testutil.OpenTestDB(t)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	p := owners.Principal{Hub: "h", UserID: "u"}
	_, err := d.WriteDB().ExecContext(context.Background(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		p.Hub, p.UserID, "sk", time.Now().UTC(),
	)
	require.NoError(t, err)
	store := storage.NewNASOnly(t.TempDir(), map[owners.Principal]string{p: "sk"})
	svc := service.NewMediaService(repo, store)
	idp := identity.NewStub(p, "Test User")
	h, err := httpapi.New(httpapi.Deps{IdentityProvider: idp, MediaService: svc})
	require.NoError(t, err)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return mediaOriginalFixture{srv: srv, owner: p, repo: repo, store: store, rw: d.WriteDB()}
}

// seedOriginal inserts a media row for p with the given bytes written at
// its storage path and returns the row.
func seedOriginal(t *testing.T, fx mediaOriginalFixture, p owners.Principal, path, checksum string, body []byte) media.Media {
	t.Helper()
	m := media.Media{
		ID:               uuid.NewString(),
		Owner:            p,
		Type:             media.TypePhoto,
		MimeType:         "image/jpeg",
		Path:             path,
		OriginalFilename: "x.jpg",
		ImportedAt:       time.Now().UTC().Truncate(time.Second),
		Size:             int64(len(body)),
		Checksum:         checksum,
		ThumbStatus:      "pending",
	}
	require.NoError(t, fx.repo.Insert(context.Background(), m))
	_, err := fx.store.Write(context.Background(), p, path, bytes.NewReader(body))
	require.NoError(t, err)
	return m
}

func TestGetMediaOriginalReturnsFullBodyAndHeaders(t *testing.T) {
	r := require.New(t)
	fx := newMediaOriginalTest(t)

	body := []byte("hello")
	m := seedOriginal(t, fx, fx.owner, "2024/hello.jpg", "cs-hello", body)

	resp, err := http.Get(fx.srv.URL + "/api/v1/media/" + m.ID + "/original")
	r.NoError(err)
	defer resp.Body.Close()

	r.Equal(http.StatusOK, resp.StatusCode)
	gotBody, err := io.ReadAll(resp.Body)
	r.NoError(err)
	r.Equal(body, gotBody)

	r.Equal(`"cs-hello"`, resp.Header.Get("ETag"))
	r.Equal("image/jpeg", resp.Header.Get("Content-Type"))
	r.Equal("5", resp.Header.Get("Content-Length"))
	r.Equal("private, max-age=31536000, must-revalidate", resp.Header.Get("Cache-Control"))
	r.Equal("bytes", resp.Header.Get("Accept-Ranges"))
	r.Equal(m.ImportedAt.UTC().Format(http.TimeFormat), resp.Header.Get("Last-Modified"))
}

func TestGetMediaOriginalIfNoneMatchReturns304(t *testing.T) {
	r := require.New(t)
	fx := newMediaOriginalTest(t)

	m := seedOriginal(t, fx, fx.owner, "2024/hello.jpg", "cs-hello", []byte("hello"))

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, fx.srv.URL+"/api/v1/media/"+m.ID+"/original", nil)
	r.NoError(err)
	req.Header.Set("If-None-Match", `"cs-hello"`)
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
	m := seedOriginal(t, fx, fx.owner, "2024/digits.jpg", "cs-digits", body)

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
	m := seedOriginal(t, fx, fx.owner, "2024/digits.jpg", "cs-digits", body)

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
	m := seedOriginal(t, fx, fx.owner, "2024/digits.jpg", "cs-digits", body)

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

	m := seedOriginal(t, fx, fx.owner, "2024/digits.jpg", "cs-digits", []byte("0123456789"))

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, fx.srv.URL+"/api/v1/media/"+m.ID+"/original", nil)
	r.NoError(err)
	req.Header.Set("Range", "bytes=100-200")
	resp, err := http.DefaultClient.Do(req)
	r.NoError(err)
	defer resp.Body.Close()

	r.Equal(http.StatusRequestedRangeNotSatisfiable, resp.StatusCode)
	r.Equal("bytes */10", resp.Header.Get("Content-Range"))
}

func TestGetMediaOriginalMalformedRangeReturns416(t *testing.T) {
	r := require.New(t)
	fx := newMediaOriginalTest(t)

	m := seedOriginal(t, fx, fx.owner, "2024/digits.jpg", "cs-digits", []byte("0123456789"))

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
		ownerB.Hub, ownerB.UserID, "sk-b", time.Now().UTC(),
	)
	r.NoError(err)
	// Row seeded directly on the repo; no bytes needed since the auth
	// check must reject before any storage access.
	mB := media.Media{
		ID:               uuid.NewString(),
		Owner:            ownerB,
		Type:             media.TypePhoto,
		MimeType:         "image/jpeg",
		Path:             "2024/other.jpg",
		OriginalFilename: "x.jpg",
		ImportedAt:       time.Now().UTC().Truncate(time.Second),
		Size:             5,
		Checksum:         "cs-b",
		ThumbStatus:      "pending",
	}
	r.NoError(fx.repo.Insert(context.Background(), mB))

	resp, err := http.Get(fx.srv.URL + "/api/v1/media/" + mB.ID + "/original")
	r.NoError(err)
	defer resp.Body.Close()
	r.Equal(http.StatusNotFound, resp.StatusCode)
}
