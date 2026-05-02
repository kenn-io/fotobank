package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/media"
	"github.com/wesm/fotobank/internal/owners"
)

// seedMediaGPS inserts a minimal primary photo with the given GPS coords
// for the given owner. Modeled on seedMedia but adds latitude/longitude
// so geo-route tests can assert on rows that ListGeo will return.
func seedMediaGPS(
	t *testing.T,
	repo *media.Repo,
	p owners.Principal,
	path, checksum string,
	lat, lon float64,
) media.Media {
	t.Helper()
	m := media.Media{
		ID:               uuid.NewString(),
		Owner:            p,
		Type:             media.TypePhoto,
		MimeType:         "image/jpeg",
		Path:             path,
		OriginalFilename: path, // tests assert on this; keep it equal to path for visibility
		ImportedAt:       time.Now().UTC().Truncate(time.Second),
		Size:             100,
		Checksum:         checksum,
		ThumbStatus:      "pending",
		Latitude:         &lat,
		Longitude:        &lon,
	}
	require.NoError(t, repo.Insert(context.Background(), m))
	return m
}

func TestGeoRoute_EmptyOwnerReturnsEmptyItems(t *testing.T) {
	r := require.New(t)
	fx := newHiddenMediaFixture(t)

	resp, err := http.Get(fx.srv.URL + "/api/v1/media/geo")
	r.NoError(err)
	defer resp.Body.Close()
	r.Equal(http.StatusOK, resp.StatusCode)

	var body struct {
		Items []map[string]any `json:"items"`
	}
	r.NoError(json.NewDecoder(resp.Body).Decode(&body))
	r.NotNil(body.Items)
	r.Empty(body.Items)
}

func TestGeoRoute_VisibleByDefault(t *testing.T) {
	r := require.New(t)
	fx := newHiddenMediaFixture(t)
	seedMediaGPS(t, fx.repo, fx.owner, "visible.jpg", "cs-vis", 10.0, 20.0)

	resp, err := http.Get(fx.srv.URL + "/api/v1/media/geo")
	r.NoError(err)
	defer resp.Body.Close()
	r.Equal(http.StatusOK, resp.StatusCode)

	var body struct {
		Items []map[string]any `json:"items"`
	}
	r.NoError(json.NewDecoder(resp.Body).Decode(&body))
	r.Len(body.Items, 1)
	r.Equal("visible.jpg", body.Items[0]["original_filename"])
}

func TestGeoRoute_IncludeHiddenWithoutUnlockReturns403(t *testing.T) {
	r := require.New(t)
	fx := newHiddenMediaFixture(t)
	seedMediaGPS(t, fx.repo, fx.owner, "anything.jpg", "cs-any", 10.0, 20.0)

	resp, err := http.Get(fx.srv.URL + "/api/v1/media/geo?include_hidden=true")
	r.NoError(err)
	defer resp.Body.Close()
	r.Equal(http.StatusForbidden, resp.StatusCode)
}

func TestGeoRoute_IncludeHiddenWithValidClaimReturnsHidden(t *testing.T) {
	r := require.New(t)
	fx := newHiddenMediaFixture(t)
	ctx := context.Background()

	visible := seedMediaGPS(t, fx.repo, fx.owner, "v.jpg", "cs-v", 10.0, 20.0)
	hidden := seedMediaGPS(t, fx.repo, fx.owner, "h.jpg", "cs-h", 30.0, 40.0)
	r.NoError(fx.repo.SetHiddenCascade(ctx, fx.owner, []string{hidden.ID}, time.Now().UTC()))

	cookie := setupHiddenAndUnlock(t, fx)
	req, err := http.NewRequest(http.MethodGet, fx.srv.URL+"/api/v1/media/geo?include_hidden=true", nil)
	r.NoError(err)
	req.AddCookie(cookie)
	resp, err := http.DefaultClient.Do(req)
	r.NoError(err)
	defer resp.Body.Close()
	r.Equal(http.StatusOK, resp.StatusCode)

	var body struct {
		Items []map[string]any `json:"items"`
	}
	r.NoError(json.NewDecoder(resp.Body).Decode(&body))
	r.Len(body.Items, 2)
	ids := []any{body.Items[0]["id"], body.Items[1]["id"]}
	r.ElementsMatch([]any{visible.ID, hidden.ID}, ids)
}

// Route conflict regression: GET /api/v1/media/geo MUST resolve to the
// geo handler, not GET /api/v1/media/{id} with id="geo". huma's route
// dispatch is order-sensitive; this test pins behavior so a future
// reorder doesn't silently break the geo endpoint.
func TestGeoRoute_DoesNotCollideWithGetMediaByID(t *testing.T) {
	r := require.New(t)
	fx := newHiddenMediaFixture(t)

	resp, err := http.Get(fx.srv.URL + "/api/v1/media/geo")
	r.NoError(err)
	defer resp.Body.Close()
	r.Equal(http.StatusOK, resp.StatusCode)

	var body struct {
		Items *[]any `json:"items"`
	}
	r.NoError(json.NewDecoder(resp.Body).Decode(&body))
	r.NotNil(body.Items, "items array present means geo handler ran (detail handler shape would differ)")
}
