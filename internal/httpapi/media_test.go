package httpapi_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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
		p.Hub, p.UserID, "550e8400-e29b-41d4-a716-446655440000", time.Now().UTC(),
	)
	require.NoError(t, err)
	store := storage.NewNASOnly(t.TempDir(), map[owners.Principal]string{p: "550e8400-e29b-41d4-a716-446655440000"})
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

// seedPairedMedia inserts a media row whose original_filename and
// mime_type are caller-supplied, then sets paired_with_id when
// pairedWithID is non-nil. Used by RAW+JPEG pairing tests where the
// filename and MIME need to look like a real sidecar (e.g. a.dng with
// image/x-adobe-dng) and the row must FK to its primary.
func seedPairedMedia(
	t *testing.T,
	repo *media.Repo,
	p owners.Principal,
	path, checksum, mimeType, originalFilename string,
	pairedWithID *string,
) media.Media {
	t.Helper()
	m := media.Media{
		ID:               uuid.NewString(),
		Owner:            p,
		Type:             media.TypePhoto,
		MimeType:         mimeType,
		Path:             path,
		OriginalFilename: originalFilename,
		ImportedAt:       time.Now().UTC().Truncate(time.Second),
		Size:             100,
		Checksum:         checksum,
		ThumbStatus:      "pending",
	}
	require.NoError(t, repo.Insert(context.Background(), m))
	if pairedWithID != nil {
		require.NoError(t, repo.UpdatePairedWithID(context.Background(), m.ID, pairedWithID))
		m.PairedWithID = pairedWithID
	}
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
		ownerB.Hub, ownerB.UserID, "550e8400-e29b-41d4-a716-446655440002", time.Now().UTC(),
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

func TestListMediaFiltersByCamera(t *testing.T) {
	// Pins the new ?camera= query binding (SF-17): seeds two cameras'
	// rows for the same owner, then ?camera=Sony+A7R+IV must return
	// only the Sony row. seedMedia doesn't set make/model, so we
	// backfill via the writeable repo.
	r := require.New(t)
	fx := newMediaAPITest(t)

	sony := seedMedia(t, fx.repo, fx.owner, "2024/sony.jpg", "cs-sony", media.TypePhoto)
	canon := seedMedia(t, fx.repo, fx.owner, "2024/canon.jpg", "cs-canon", media.TypePhoto)
	_, err := fx.rw.ExecContext(context.Background(),
		`UPDATE media SET make = ?, model = ? WHERE id = ?`, "Sony", "A7R IV", sony.ID)
	r.NoError(err)
	_, err = fx.rw.ExecContext(context.Background(),
		`UPDATE media SET make = ?, model = ? WHERE id = ?`, "Canon", "EOS R5", canon.ID)
	r.NoError(err)

	resp, err := http.Get(fx.srv.URL + "/api/v1/media?camera=Sony+A7R+IV")
	r.NoError(err)
	defer func() { _ = resp.Body.Close() }()
	r.Equal(http.StatusOK, resp.StatusCode)
	body := decodeList(t, resp)
	r.Len(body.Items, 1)
	r.Equal(sony.ID, body.Items[0].ID)
}

func TestListMediaFiltersByCameraExplodeBindsRepeats(t *testing.T) {
	// Pins the ,explode binding: ?camera=A&camera=B must OR the two
	// values together, not collapse to the last value silently.
	r := require.New(t)
	fx := newMediaAPITest(t)

	sony := seedMedia(t, fx.repo, fx.owner, "2024/sony.jpg", "cs-sony", media.TypePhoto)
	canon := seedMedia(t, fx.repo, fx.owner, "2024/canon.jpg", "cs-canon", media.TypePhoto)
	leica := seedMedia(t, fx.repo, fx.owner, "2024/leica.jpg", "cs-leica", media.TypePhoto)
	for _, row := range []struct {
		id, make, model string
	}{
		{sony.ID, "Sony", "A7R IV"},
		{canon.ID, "Canon", "EOS R5"},
		{leica.ID, "Leica", "Q3"},
	} {
		_, err := fx.rw.ExecContext(context.Background(),
			`UPDATE media SET make = ?, model = ? WHERE id = ?`, row.make, row.model, row.id)
		r.NoError(err)
	}

	resp, err := http.Get(
		fx.srv.URL + "/api/v1/media?camera=Sony+A7R+IV&camera=Canon+EOS+R5",
	)
	r.NoError(err)
	defer func() { _ = resp.Body.Close() }()
	r.Equal(http.StatusOK, resp.StatusCode)
	body := decodeList(t, resp)
	r.Len(body.Items, 2)
	got := map[string]bool{}
	for _, it := range body.Items {
		got[it.ID] = true
	}
	r.True(got[sony.ID])
	r.True(got[canon.ID])
	r.False(got[leica.ID])
}

func TestListMediaFiltersByHasGPS(t *testing.T) {
	// Pins the has_gps=true binding: only geotagged rows survive.
	r := require.New(t)
	fx := newMediaAPITest(t)

	noGPS := seedMedia(t, fx.repo, fx.owner, "2024/no-gps.jpg", "cs-no", media.TypePhoto)
	withGPSID := uuid.NewString()
	lat, lon := 48.8566, 2.3522
	r.NoError(fx.repo.Insert(context.Background(), media.Media{
		ID: withGPSID, Owner: fx.owner, Type: media.TypePhoto, MimeType: "image/jpeg",
		Path: "2024/with-gps.jpg", ImportedAt: time.Now().UTC(), Size: 1, Checksum: "cs-yes",
		Latitude: &lat, Longitude: &lon, ThumbStatus: "pending",
	}))

	resp, err := http.Get(fx.srv.URL + "/api/v1/media?has_gps=true")
	r.NoError(err)
	defer func() { _ = resp.Body.Close() }()
	r.Equal(http.StatusOK, resp.StatusCode)
	body := decodeList(t, resp)
	r.Len(body.Items, 1)
	r.Equal(withGPSID, body.Items[0].ID)

	resp2, err := http.Get(fx.srv.URL + "/api/v1/media?has_gps=false")
	r.NoError(err)
	defer func() { _ = resp2.Body.Close() }()
	r.Equal(http.StatusOK, resp2.StatusCode)
	body2 := decodeList(t, resp2)
	r.Len(body2.Items, 1)
	r.Equal(noGPS.ID, body2.Items[0].ID)
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

func TestListMediaDTOIncludesGPSWhenPresent(t *testing.T) {
	r := require.New(t)
	fx := newMediaAPITest(t)

	id := uuid.NewString()
	lat, lon := 48.8566, 2.3522
	gps := time.Date(2024, 6, 15, 14, 30, 22, 0, time.UTC)
	r.NoError(fx.repo.Insert(context.Background(), media.Media{
		ID: id, Owner: fx.owner, Type: media.TypePhoto, MimeType: "image/jpeg",
		Path: "x.jpg", ImportedAt: time.Now().UTC(), Size: 1, Checksum: "c-" + id,
		Latitude: &lat, Longitude: &lon, GPSAt: &gps,
		LocationLabel: "Paris, Île-de-France, France",
		ThumbStatus:   "pending",
	}))

	resp, err := http.Get(fx.srv.URL + "/api/v1/media")
	r.NoError(err)
	defer func() { _ = resp.Body.Close() }()
	r.Equal(http.StatusOK, resp.StatusCode)
	bs, err := io.ReadAll(resp.Body)
	r.NoError(err)
	body := string(bs)
	r.Contains(body, `"latitude":48.8566`)
	r.Contains(body, `"longitude":2.3522`)
	r.Contains(body, `"gps_at":"2024-06-15T14:30:22Z"`)
	r.Contains(body, `"location_label":"Paris, Île-de-France, France"`)
}

func TestListMediaDTOOmitsGPSWhenAbsent(t *testing.T) {
	r := require.New(t)
	fx := newMediaAPITest(t)

	id := uuid.NewString()
	r.NoError(fx.repo.Insert(context.Background(), media.Media{
		ID: id, Owner: fx.owner, Type: media.TypePhoto, MimeType: "image/jpeg",
		Path: "x.jpg", ImportedAt: time.Now().UTC(), Size: 1, Checksum: "c-" + id,
		ThumbStatus: "pending",
	}))

	resp, err := http.Get(fx.srv.URL + "/api/v1/media")
	r.NoError(err)
	defer func() { _ = resp.Body.Close() }()
	bs, err := io.ReadAll(resp.Body)
	r.NoError(err)
	body := string(bs)
	r.NotContains(body, "latitude")
	r.NotContains(body, "longitude")
	r.NotContains(body, "gps_at")
	r.NotContains(body, "location_label")
}

func TestGetMediaDTOIncludesGPSWhenPresent(t *testing.T) {
	r := require.New(t)
	fx := newMediaAPITest(t)

	id := uuid.NewString()
	lat, lon := 48.8566, 2.3522
	r.NoError(fx.repo.Insert(context.Background(), media.Media{
		ID: id, Owner: fx.owner, Type: media.TypePhoto, MimeType: "image/jpeg",
		Path: "x.jpg", ImportedAt: time.Now().UTC(), Size: 1, Checksum: "c-" + id,
		Latitude: &lat, Longitude: &lon, LocationLabel: "Paris, France",
		ThumbStatus: "pending",
	}))

	resp, err := http.Get(fx.srv.URL + "/api/v1/media/" + id)
	r.NoError(err)
	defer func() { _ = resp.Body.Close() }()
	r.Equal(http.StatusOK, resp.StatusCode)
	bs, err := io.ReadAll(resp.Body)
	r.NoError(err)
	body := string(bs)
	r.Contains(body, `"latitude":48.8566`)
	r.Contains(body, `"location_label":"Paris, France"`)
}

func TestGetMediaDTOOmitsGPSWhenAbsent(t *testing.T) {
	r := require.New(t)
	fx := newMediaAPITest(t)

	id := uuid.NewString()
	r.NoError(fx.repo.Insert(context.Background(), media.Media{
		ID: id, Owner: fx.owner, Type: media.TypePhoto, MimeType: "image/jpeg",
		Path: "x.jpg", ImportedAt: time.Now().UTC(), Size: 1, Checksum: "c-" + id,
		ThumbStatus: "pending",
	}))

	resp, err := http.Get(fx.srv.URL + "/api/v1/media/" + id)
	r.NoError(err)
	defer func() { _ = resp.Body.Close() }()
	bs, err := io.ReadAll(resp.Body)
	r.NoError(err)
	body := string(bs)
	r.NotContains(body, "latitude")
	r.NotContains(body, "location_label")
}

func TestMediaListHidesSidecars(t *testing.T) {
	r := require.New(t)
	fx := newMediaAPITest(t)

	primary := seedPairedMedia(t, fx.repo, fx.owner,
		"2024/primary.jpg", "cs-pri", "image/jpeg", "primary.jpg", nil)
	pid := primary.ID
	_ = seedPairedMedia(t, fx.repo, fx.owner,
		"2024/primary.dng", "cs-sid", "image/x-adobe-dng", "primary.dng", &pid)

	resp, err := http.Get(fx.srv.URL + "/api/v1/media")
	r.NoError(err)
	defer func() { _ = resp.Body.Close() }()
	r.Equal(http.StatusOK, resp.StatusCode)

	var body struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	r.NoError(json.NewDecoder(resp.Body).Decode(&body))
	ids := make([]string, 0, len(body.Items))
	for _, it := range body.Items {
		ids = append(ids, it.ID)
	}
	r.Equal([]string{primary.ID}, ids)
}

func TestMediaDetailPrimaryEmbedsSidecars(t *testing.T) {
	r := require.New(t)
	fx := newMediaAPITest(t)

	primary := seedPairedMedia(t, fx.repo, fx.owner,
		"2024/a.jpg", "cs-pri", "image/jpeg", "a.jpg", nil)
	pid := primary.ID
	dng := seedPairedMedia(t, fx.repo, fx.owner,
		"2024/a.dng", "cs-sid", "image/x-adobe-dng", "a.dng", &pid)

	resp, err := http.Get(fx.srv.URL + "/api/v1/media/" + primary.ID)
	r.NoError(err)
	defer func() { _ = resp.Body.Close() }()
	r.Equal(http.StatusOK, resp.StatusCode)

	var body struct {
		ID       string `json:"id"`
		Sidecars []struct {
			ID           string `json:"id"`
			PairedWithID string `json:"paired_with_id"`
		} `json:"sidecars"`
		PairedWithID *string `json:"paired_with_id,omitempty"`
	}
	r.NoError(json.NewDecoder(resp.Body).Decode(&body))
	r.Equal(primary.ID, body.ID)
	r.Nil(body.PairedWithID)
	r.Len(body.Sidecars, 1)
	r.Equal(dng.ID, body.Sidecars[0].ID)
	r.Equal(primary.ID, body.Sidecars[0].PairedWithID)
}

func TestMediaDetailSidecarReturnsPairedWithSummary(t *testing.T) {
	r := require.New(t)
	fx := newMediaAPITest(t)

	primary := seedPairedMedia(t, fx.repo, fx.owner,
		"2024/a.jpg", "cs-pri", "image/jpeg", "a.jpg", nil)
	pid := primary.ID
	dng := seedPairedMedia(t, fx.repo, fx.owner,
		"2024/a.dng", "cs-sid", "image/x-adobe-dng", "a.dng", &pid)

	resp, err := http.Get(fx.srv.URL + "/api/v1/media/" + dng.ID)
	r.NoError(err)
	defer func() { _ = resp.Body.Close() }()
	r.Equal(http.StatusOK, resp.StatusCode)

	var body struct {
		ID           string `json:"id"`
		PairedWithID string `json:"paired_with_id"`
		PairedWith   *struct {
			ID               string `json:"id"`
			OriginalFilename string `json:"original_filename"`
		} `json:"paired_with"`
		Sidecars []any `json:"sidecars,omitempty"`
	}
	r.NoError(json.NewDecoder(resp.Body).Decode(&body))
	r.Equal(dng.ID, body.ID)
	r.Equal(primary.ID, body.PairedWithID)
	r.NotNil(body.PairedWith)
	r.Equal(primary.ID, body.PairedWith.ID)
	r.Equal("a.jpg", body.PairedWith.OriginalFilename)
	r.Empty(body.Sidecars, "sidecar's own DTO should not list further sidecars")
}
