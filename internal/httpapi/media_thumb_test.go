package httpapi_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/httpapi"
	"go.kenn.io/fotobank/internal/identity"
	"go.kenn.io/fotobank/internal/media"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/service"
	"go.kenn.io/fotobank/internal/storage"
	"go.kenn.io/fotobank/internal/testutil"
	"go.kenn.io/fotobank/internal/thumb"
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
// version and writes bytes for every size in thumb.AllSizes() at the
// versioned key. The payload for each size is "<size> bytes" (e.g.
// "grid bytes", "preview bytes", "large bytes") so callers can assert
// on the body to verify the route served the right size.
func seedReadyThumb(t *testing.T, repo *media.Repo, store storage.Store, p owners.Principal, version int) media.Media {
	t.Helper()
	id := uuid.NewString()
	m := media.Media{
		ID: id, Owner: p, Type: media.TypePhoto, MimeType: "image/jpeg",
		Path: "x-" + id + ".jpg", ImportedAt: time.Now().UTC(),
		Size: 1, Checksum: id, ThumbStatus: "ready", ThumbVersion: version,
	}
	require.NoError(t, repo.Insert(context.Background(), m))
	for _, sz := range thumb.AllSizes() {
		_, err := store.Write(context.Background(), p,
			thumb.ThumbKey(id, version, sz),
			strings.NewReader(string(sz)+" bytes"))
		require.NoError(t, err)
	}
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
	r.Equal("private, no-cache", resp.Header.Get("Cache-Control"))
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

func TestThumbRouteLightboxReturns400(t *testing.T) {
	// SizeLightbox was retired in F2.0; ?size=lightbox must now hit
	// the same 400 path as any unknown size. This regression-locks
	// the size vocabulary against accidental re-introduction.
	r := require.New(t)
	srv, _, _, _ := newThumbAPITest(t)
	resp, err := http.Get(srv.URL + "/api/v1/media/anything/thumb?size=lightbox&v=0")
	r.NoError(err)
	defer func() { _ = resp.Body.Close() }()
	r.Equal(http.StatusBadRequest, resp.StatusCode)
}

func TestThumbRouteLargeHappyPath(t *testing.T) {
	r := require.New(t)
	srv, repo, p, store := newThumbAPITest(t)
	m := seedReadyThumb(t, repo, store, p, 5)
	url := fmt.Sprintf("%s/api/v1/media/%s/thumb?size=large&v=%d", srv.URL, m.ID, m.ThumbVersion)
	resp, err := http.Get(url)
	r.NoError(err)
	defer func() { _ = resp.Body.Close() }()
	r.Equal(http.StatusOK, resp.StatusCode)
	r.Equal("image/jpeg", resp.Header.Get("Content-Type"))
	// Asserting on body bytes (not just status) catches a future bug
	// where the handler resolves ?size=large to a different size's
	// blob (e.g., "grid bytes") yet still returns 200.
	body, err := io.ReadAll(resp.Body)
	r.NoError(err)
	r.Equal([]byte("large bytes"), body)
}

func TestThumbRouteStaleReadyRowReturns404ForNewSize(t *testing.T) {
	// F2.0 upgrade contract: rows whose thumb_status='ready' was set
	// under the F1 vocabulary lack the new large.jpg blob (F1 emitted
	// grid + preview + lightbox; F2.0 emits grid + preview + large).
	// After F2.0 deploy, ?size=large&v=N for those rows must 404 (not
	// 500, not silently rewrite to a different size) until an operator
	// runs `thumbs regenerate`. The 404 is what MediaCell's placeholder
	// fallback keys off of; this locks the contract so a future
	// "convenience fallback" can't silently degrade to ?size=preview.
	r := require.New(t)
	srv, repo, p, store := newThumbAPITest(t)
	m := seedReadyThumb(t, repo, store, p, 5)
	// Simulate an F1-vintage row by removing the large blob — leaves
	// the ready row with the F1-era set on disk (grid + preview).
	r.NoError(store.Delete(context.Background(), p,
		thumb.ThumbKey(m.ID, m.ThumbVersion, thumb.SizeLarge)))
	url := fmt.Sprintf("%s/api/v1/media/%s/thumb?size=large&v=%d", srv.URL, m.ID, m.ThumbVersion)
	resp, err := http.Get(url)
	r.NoError(err)
	defer func() { _ = resp.Body.Close() }()
	r.Equal(http.StatusNotFound, resp.StatusCode,
		"stale F1 ready row must 404 for size=large until regenerate runs")
}
