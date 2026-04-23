package httpapi_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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
	"github.com/wesm/fotobank/internal/thumb"
)

// newThumbAPITest wires up a server identical to mediaAPIFixture but
// with a ThumbService on Deps so the /thumb route is registered.
func newThumbAPITest(t *testing.T) (*httptest.Server, *media.Repo, owners.Principal, storage.Store) {
	t.Helper()
	d := testutil.OpenTestDB(t)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	q := thumb.NewQueue(d.WriteDB(), d.ReadDB())
	p := owners.Principal{Hub: "h", UserID: "u"}
	_, err := d.WriteDB().ExecContext(context.Background(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		p.Hub, p.UserID, "sk", time.Now().UTC(),
	)
	require.NoError(t, err)
	store := storage.NewNASOnly(t.TempDir(), map[owners.Principal]string{p: "sk"})
	mediaSvc := service.NewMediaService(repo, store)
	thumbSvc := service.NewThumbService(repo, q, store)
	h, err := httpapi.New(httpapi.Deps{
		IdentityProvider: identity.NewStub(p, "Test User"),
		MediaService:     mediaSvc,
		ThumbService:     thumbSvc,
	})
	require.NoError(t, err)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, repo, p, store
}

// seedReadyThumb inserts a media row in "ready" status at the given
// version and writes bytes for the grid size at the versioned key. The
// test server can then serve the thumb via /api/v1/media/{id}/thumb.
func seedReadyThumb(t *testing.T, repo *media.Repo, store storage.Store, p owners.Principal, version int) media.Media {
	t.Helper()
	id := uuid.NewString()
	m := media.Media{
		ID: id, Owner: p, Type: media.TypePhoto, MimeType: "image/jpeg",
		Path: "x-" + id + ".jpg", ImportedAt: time.Now().UTC(),
		Size: 1, Checksum: id, ThumbStatus: "ready", ThumbVersion: version,
	}
	require.NoError(t, repo.Insert(context.Background(), m))
	_, err := store.Write(context.Background(), p,
		thumb.ThumbKey(id, version, thumb.SizeGrid),
		strings.NewReader("grid bytes"))
	require.NoError(t, err)
	return m
}

func TestThumbRouteReturnsBytesOnMatchingVersion(t *testing.T) {
	r := require.New(t)
	srv, repo, p, store := newThumbAPITest(t)
	m := seedReadyThumb(t, repo, store, p, 7)

	resp, err := http.Get(srv.URL + "/api/v1/media/" + m.ID + "/thumb?size=grid&v=7")
	r.NoError(err)
	defer func() { _ = resp.Body.Close() }()
	r.Equal(http.StatusOK, resp.StatusCode)
	r.Equal(`"`+m.ID+`-grid-v7"`, resp.Header.Get("ETag"))
	r.Contains(resp.Header.Get("Cache-Control"), "immutable")
	r.Equal("image/jpeg", resp.Header.Get("Content-Type"))
	body, err := io.ReadAll(resp.Body)
	r.NoError(err)
	r.Equal([]byte("grid bytes"), body)
}

func TestThumbRouteVersionMismatchReturns404(t *testing.T) {
	r := require.New(t)
	srv, repo, p, store := newThumbAPITest(t)
	m := seedReadyThumb(t, repo, store, p, 5)

	resp, err := http.Get(srv.URL + "/api/v1/media/" + m.ID + "/thumb?size=grid&v=4")
	r.NoError(err)
	defer func() { _ = resp.Body.Close() }()
	r.Equal(http.StatusNotFound, resp.StatusCode)
}

func TestThumbRouteMissingVersionReturns404(t *testing.T) {
	r := require.New(t)
	srv, repo, p, store := newThumbAPITest(t)
	m := seedReadyThumb(t, repo, store, p, 0)

	resp, err := http.Get(srv.URL + "/api/v1/media/" + m.ID + "/thumb?size=grid")
	r.NoError(err)
	defer func() { _ = resp.Body.Close() }()
	r.Equal(http.StatusNotFound, resp.StatusCode)
}

func TestThumbRouteUnknownSizeReturns400(t *testing.T) {
	r := require.New(t)
	srv, _, _, _ := newThumbAPITest(t)

	resp, err := http.Get(srv.URL + "/api/v1/media/any-id/thumb?size=huge&v=0")
	r.NoError(err)
	defer func() { _ = resp.Body.Close() }()
	r.Equal(http.StatusBadRequest, resp.StatusCode)
}

func TestListMediaDTOIncludesThumbVersion(t *testing.T) {
	r := require.New(t)
	srv, repo, p, store := newThumbAPITest(t)
	_ = seedReadyThumb(t, repo, store, p, 3)

	resp, err := http.Get(srv.URL + "/api/v1/media")
	r.NoError(err)
	defer func() { _ = resp.Body.Close() }()
	r.Equal(http.StatusOK, resp.StatusCode)
	bs, err := io.ReadAll(resp.Body)
	r.NoError(err)
	r.Contains(string(bs), `"thumb_version":3`)
}

// TestThumbRouteNotFoundHasNoStoreCacheControl verifies that 404
// responses for pre-ready / version-mismatched thumbs set
// Cache-Control: no-store so clients refetch once the thumb becomes
// ready (a cached 404 would make the client miss the version bump).
func TestThumbRouteNotFoundHasNoStoreCacheControl(t *testing.T) {
	r := require.New(t)
	srv, repo, p, store := newThumbAPITest(t)
	m := seedReadyThumb(t, repo, store, p, 5)

	resp, err := http.Get(srv.URL + "/api/v1/media/" + m.ID + "/thumb?size=grid&v=4")
	r.NoError(err)
	defer func() { _ = resp.Body.Close() }()
	r.Equal(http.StatusNotFound, resp.StatusCode)
	r.Equal("no-store", resp.Header.Get("Cache-Control"))
}
