package httpapi_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/auth/hidden"
	"go.kenn.io/fotobank/internal/httpapi"
	"go.kenn.io/fotobank/internal/identity"
	"go.kenn.io/fotobank/internal/media"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/service/facets"
	"go.kenn.io/fotobank/internal/testutil"
	"go.kenn.io/fotobank/internal/testutil/mediaseed"
)

// facetsRouteHiddenChecker satisfies facets.HiddenChecker for tests that
// don't exercise the IncludeHidden gate. The route tests in this file
// never set IncludeHidden=true, so the Valid implementation is never
// called; the field exists purely so facets.New has a non-nil checker.
type facetsRouteHiddenChecker struct{}

func (facetsRouteHiddenChecker) Valid(_ *hidden.UnlockClaim, _ owners.Principal) bool {
	return false
}

// facetsRouteOwner is the principal each facets route test runs as.
// Mirrors the seedOwner used in service-level tests so the seed
// fixture's owner aligns with the request principal.
var facetsRouteOwner = owners.Principal{Hub: "h", UserID: "u"}

// facetsRouteFakeMediaPrivacy is a no-op MediaPrivacy implementation
// used to stand up a hidden.Service for the route fixture. The route
// tests do not exercise hidden state so the implementation can no-op.
type facetsRouteFakeMediaPrivacy struct{}

func (facetsRouteFakeMediaPrivacy) ClearAllHiddenForOwner(_ context.Context, _ owners.Principal) error {
	return nil
}

// seedFacetsRouteFixtures plants the canonical 3-row Sony/Canon fixture
// the route tests reuse. Two Sony rows (geo photo + non-geo video), one
// geotagged Canon photo. Mirrors insertSeedFixtures from the
// internal/service/facets service tests so the route tests see the same
// shape the service tests pin.
func seedFacetsRouteFixtures(t *testing.T, rw *sql.DB) {
	t.Helper()
	mediaseed.InsertMedia(t, rw, facetsRouteOwner, "m-sony-geo-photo", media.Media{
		Type: media.TypePhoto, Make: "Sony", Model: "A7R IV",
		LensModel: "FE 24-70mm F2.8 GM",
		Latitude:  new(48.8), Longitude: new(2.3),
	})
	mediaseed.InsertMedia(t, rw, facetsRouteOwner, "m-sony-nogeo-video", media.Media{
		Type: media.TypeVideo, Make: "Sony", Model: "A7R IV",
	})
	mediaseed.InsertMedia(t, rw, facetsRouteOwner, "m-canon-geo-photo", media.Media{
		Type: media.TypePhoto, Make: "Canon", Model: "EOS R5",
		LensModel: "FE 24-70mm F2.8 GM",
		Latitude:  new(40.7), Longitude: new(-74.0),
	})
	mediaseed.InsertTag(t, rw, facetsRouteOwner, "m-sony-geo-photo", "dog", "Dog")
	mediaseed.InsertTag(t, rw, facetsRouteOwner, "m-canon-geo-photo", "cat", "Cat")
}

// newFacetsRouteServer wires a httptest.Server backed by a fresh
// migrated DB, the canonical seed fixture, a real hidden.Service, and
// a *facets.Service with that hidden.Service as its checker. Returns
// the server so the test can issue requests against /api/v1/facets.
func newFacetsRouteServer(t *testing.T) *httptest.Server {
	t.Helper()
	d := testutil.OpenTestDB(t)
	rw, ro := d.WriteDB(), d.ReadDB()
	testutil.SeedOwner(t, rw, facetsRouteOwner.Hub, facetsRouteOwner.UserID)
	seedFacetsRouteFixtures(t, rw)

	hiddenSvc := hidden.NewService(hidden.NewRepo(rw, ro), facetsRouteFakeMediaPrivacy{})
	svc := facets.New(ro, facetsRouteHiddenChecker{})

	idp := identity.NewStub(facetsRouteOwner, "Test User")
	h, err := httpapi.New(httpapi.Deps{
		IdentityProvider: idp,
		Facets:           svc,
		HiddenAuth:       hiddenSvc,
	})
	require.NoError(t, err)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

// facetsBodyDTO mirrors the wire shape of /api/v1/facets so tests can
// decode JSON without reaching into the unexported types in facets.go.
type facetsBodyDTO struct {
	Cameras []struct {
		Value string `json:"value"`
		Count int    `json:"count"`
	} `json:"cameras"`
	Lenses []struct {
		Value string `json:"value"`
		Count int    `json:"count"`
	} `json:"lenses"`
	Tags []struct {
		Key   string `json:"key"`
		Label string `json:"label"`
		Count int    `json:"count"`
	} `json:"tags"`
	Places struct {
		WithGPS    int `json:"with_gps"`
		WithoutGPS int `json:"without_gps"`
	} `json:"places"`
	MediaTypes []struct {
		Value string `json:"value"`
		Count int    `json:"count"`
	} `json:"media_types"`
}

// doGetFacets issues GET /api/v1/facets with the supplied query string.
// Returns the response so the caller can assert on status; on a 200 the
// body is JSON-decoded into *facetsBodyDTO.
func doGetFacets(t *testing.T, srv *httptest.Server, q url.Values) (*http.Response, *facetsBodyDTO) {
	t.Helper()
	u := srv.URL + "/api/v1/facets"
	if encoded := q.Encode(); encoded != "" {
		u += "?" + encoded
	}
	resp, err := srv.Client().Get(u)
	require.NoError(t, err)
	if resp.StatusCode != http.StatusOK {
		return resp, nil
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	var out facetsBodyDTO
	require.NoError(t, json.Unmarshal(body, &out))
	return resp, &out
}

// TestFacetsRoute_OwnerScoped — caller principal flows from middleware
// into the service. Empty filter set returns the full facet response.
func TestFacetsRoute_OwnerScoped(t *testing.T) {
	r := require.New(t)
	srv := newFacetsRouteServer(t)

	resp, body := doGetFacets(t, srv, url.Values{})
	r.Equal(http.StatusOK, resp.StatusCode)
	r.NotNil(body)

	cameraValues := make([]string, 0, len(body.Cameras))
	for _, vc := range body.Cameras {
		cameraValues = append(cameraValues, vc.Value)
	}
	r.Contains(cameraValues, "Sony A7R IV")
	r.Contains(cameraValues, "Canon EOS R5")

	r.Equal(2, body.Places.WithGPS)
	r.Equal(1, body.Places.WithoutGPS)

	mediaTypeValues := make([]string, 0, len(body.MediaTypes))
	for _, vc := range body.MediaTypes {
		mediaTypeValues = append(mediaTypeValues, vc.Value)
	}
	r.Contains(mediaTypeValues, "photo")
	r.Contains(mediaTypeValues, "video")
}

// TestFacetsRoute_FilterParams — facet filters round-trip from query
// string to the service. With camera=Sony+A7R+IV and has_gps=1 the
// exclude-self rule still surfaces both cameras (the camera facet
// ignores its own selection) while the lenses/places facets narrow to
// the Sony+geotagged scope.
func TestFacetsRoute_FilterParams(t *testing.T) {
	r := require.New(t)
	srv := newFacetsRouteServer(t)

	q := url.Values{}
	q.Set("camera", "Sony A7R IV")
	q.Set("has_gps", "true")

	resp, body := doGetFacets(t, srv, q)
	r.Equal(http.StatusOK, resp.StatusCode)
	r.NotNil(body)

	// Cameras still shows both because of the exclude-self rule.
	cameraValues := make([]string, 0, len(body.Cameras))
	for _, vc := range body.Cameras {
		cameraValues = append(cameraValues, vc.Value)
	}
	r.Contains(cameraValues, "Sony A7R IV")
	r.Contains(cameraValues, "Canon EOS R5")

	// Lenses narrow to Sony+geotagged; only the Sony geo photo
	// (FE 24-70mm) survives.
	r.Len(body.Lenses, 1)
	r.Equal("FE 24-70mm F2.8 GM", body.Lenses[0].Value)
	r.Equal(1, body.Lenses[0].Count)

	// Places: camera=Sony narrows to Sony rows AND has_gps=true narrows
	// to geo rows. The Places facet excludes its own selection (HasGPS),
	// so it counts both with_gps and without_gps among Sony rows: one
	// geo photo + one non-geo video.
	r.Equal(1, body.Places.WithGPS)
	r.Equal(1, body.Places.WithoutGPS)
}
