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

	"github.com/wesm/fotobank/internal/httpapi"
	"github.com/wesm/fotobank/internal/identity"
	"github.com/wesm/fotobank/internal/media"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/service"
	"github.com/wesm/fotobank/internal/storage"
	"github.com/wesm/fotobank/internal/testutil"
)

// mediaAPIFixture bundles the server + collaborators a media HTTP test
// needs: the running test server, the caller's principal, the repo for
// seeding media rows, and the writable DB for seeding additional owners.
type mediaAPIFixture struct {
	srv   *httptest.Server
	owner owners.Principal
	repo  *media.Repo
	rw    *sql.DB
}

func newMediaAPITest(t *testing.T) mediaAPIFixture {
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
	return mediaAPIFixture{srv: srv, owner: p, repo: repo, rw: d.WriteDB()}
}

// seedMedia inserts a minimal media row for p and returns it.
func seedMedia(t *testing.T, repo *media.Repo, p owners.Principal, path, checksum string, typ media.Type) media.Media {
	t.Helper()
	mime := "image/jpeg"
	if typ == media.TypeVideo {
		mime = "video/mp4"
	}
	m := media.Media{
		ID:               uuid.NewString(),
		Owner:            p,
		Type:             typ,
		MimeType:         mime,
		Path:             path,
		OriginalFilename: "x.jpg",
		ImportedAt:       time.Now().UTC().Truncate(time.Second),
		Size:             100,
		Checksum:         checksum,
		ThumbStatus:      "pending",
	}
	require.NoError(t, repo.Insert(context.Background(), m))
	return m
}

type listMediaResponse struct {
	Items []struct {
		ID           string `json:"id"`
		Type         string `json:"type"`
		Path         string `json:"path"`
		Checksum     string `json:"checksum"`
		ThumbStatus  string `json:"thumb_status"`
		ThumbVersion int    `json:"thumb_version"`
	} `json:"items"`
	NextOffset *int `json:"next_offset"`
	Total      *int `json:"total"`
}

func decodeList(t *testing.T, resp *http.Response) listMediaResponse {
	t.Helper()
	var out listMediaResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	return out
}

func TestListMediaReturnsOwnerRows(t *testing.T) {
	r := require.New(t)
	fx := newMediaAPITest(t)

	seedMedia(t, fx.repo, fx.owner, "2024/a.jpg", "cs-a", media.TypePhoto)
	seedMedia(t, fx.repo, fx.owner, "2024/b.jpg", "cs-b", media.TypePhoto)

	resp, err := http.Get(fx.srv.URL + "/api/v1/media")
	r.NoError(err)
	defer resp.Body.Close()
	r.Equal(http.StatusOK, resp.StatusCode)

	body := decodeList(t, resp)
	r.Len(body.Items, 2)
}

func TestListMediaFiltersByMediaType(t *testing.T) {
	r := require.New(t)
	fx := newMediaAPITest(t)

	seedMedia(t, fx.repo, fx.owner, "2024/p1.jpg", "cs-p1", media.TypePhoto)
	seedMedia(t, fx.repo, fx.owner, "2024/p2.jpg", "cs-p2", media.TypePhoto)
	seedMedia(t, fx.repo, fx.owner, "2024/v.mp4", "cs-v", media.TypeVideo)

	resp, err := http.Get(fx.srv.URL + "/api/v1/media?media_type=photo&limit=1")
	r.NoError(err)
	defer resp.Body.Close()
	r.Equal(http.StatusOK, resp.StatusCode)

	body := decodeList(t, resp)
	r.Len(body.Items, 1)
	r.Equal("photo", body.Items[0].Type)
	// limit=1 and there's another photo row, so the page is full and a
	// next_offset hint is returned.
	r.NotNil(body.NextOffset)
	r.Equal(1, *body.NextOffset)
}

func TestGetMediaReturnsDetail(t *testing.T) {
	r := require.New(t)
	fx := newMediaAPITest(t)

	m := seedMedia(t, fx.repo, fx.owner, "2024/a.jpg", "cs-a", media.TypePhoto)

	resp, err := http.Get(fx.srv.URL + "/api/v1/media/" + m.ID)
	r.NoError(err)
	defer resp.Body.Close()
	r.Equal(http.StatusOK, resp.StatusCode)

	var body struct {
		ID       string `json:"id"`
		Type     string `json:"type"`
		Path     string `json:"path"`
		Checksum string `json:"checksum"`
	}
	r.NoError(json.NewDecoder(resp.Body).Decode(&body))
	r.Equal(m.ID, body.ID)
	r.Equal("photo", body.Type)
	r.Equal("2024/a.jpg", body.Path)
	r.Equal("cs-a", body.Checksum)
}

func TestGetMediaNotFoundForOtherOwner(t *testing.T) {
	r := require.New(t)
	fx := newMediaAPITest(t)

	// Seed a second owner B with a media row that the caller (A) must
	// not be able to fetch by ID.
	ownerB := owners.Principal{Hub: "h", UserID: "b"}
	_, err := fx.rw.ExecContext(context.Background(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		ownerB.Hub, ownerB.UserID, "sk-b", time.Now().UTC(),
	)
	r.NoError(err)
	mB := seedMedia(t, fx.repo, ownerB, "2024/b.jpg", "cs-b", media.TypePhoto)

	resp, err := http.Get(fx.srv.URL + "/api/v1/media/" + mB.ID)
	r.NoError(err)
	defer resp.Body.Close()
	r.Equal(http.StatusNotFound, resp.StatusCode)
}

func TestGetMediaNotFoundForUnknownID(t *testing.T) {
	r := require.New(t)
	fx := newMediaAPITest(t)

	resp, err := http.Get(fx.srv.URL + "/api/v1/media/nonsense-id")
	r.NoError(err)
	defer resp.Body.Close()
	r.Equal(http.StatusNotFound, resp.StatusCode)
}

func TestListMediaOmitsNextOffsetAtExactPageBoundary(t *testing.T) {
	// Regression: when the result set ends exactly at Limit the handler
	// must NOT return a next_offset; otherwise clients follow the hint
	// and fetch an empty page.
	r := require.New(t)
	fx := newMediaAPITest(t)

	seedMedia(t, fx.repo, fx.owner, "2024/a.jpg", "cs-a", media.TypePhoto)
	seedMedia(t, fx.repo, fx.owner, "2024/b.jpg", "cs-b", media.TypePhoto)

	resp, err := http.Get(fx.srv.URL + "/api/v1/media?limit=2")
	r.NoError(err)
	defer resp.Body.Close()
	r.Equal(http.StatusOK, resp.StatusCode)
	body := decodeList(t, resp)
	r.Len(body.Items, 2)
	r.Nil(body.NextOffset, "next_offset must be nil when the page exhausts the result set")
}

func TestListMediaIncludesNextOffsetOnFullPage(t *testing.T) {
	r := require.New(t)
	fx := newMediaAPITest(t)

	seedMedia(t, fx.repo, fx.owner, "2024/a.jpg", "cs-a", media.TypePhoto)
	seedMedia(t, fx.repo, fx.owner, "2024/b.jpg", "cs-b", media.TypePhoto)
	seedMedia(t, fx.repo, fx.owner, "2024/c.jpg", "cs-c", media.TypePhoto)

	// Page 1: limit=2 → 2 rows + next_offset=2.
	resp, err := http.Get(fx.srv.URL + "/api/v1/media?limit=2")
	r.NoError(err)
	defer resp.Body.Close()
	r.Equal(http.StatusOK, resp.StatusCode)
	page1 := decodeList(t, resp)
	r.Len(page1.Items, 2)
	r.NotNil(page1.NextOffset)
	r.Equal(2, *page1.NextOffset)

	// Page 2: offset=2&limit=2 → 1 row (the remaining one) and no
	// next_offset because the page came back with fewer than Limit rows.
	resp2, err := http.Get(fx.srv.URL + "/api/v1/media?limit=2&offset=2")
	r.NoError(err)
	defer resp2.Body.Close()
	r.Equal(http.StatusOK, resp2.StatusCode)
	page2 := decodeList(t, resp2)
	r.Len(page2.Items, 1)
	r.Nil(page2.NextOffset)
}
