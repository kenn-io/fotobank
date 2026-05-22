package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/media"
	"go.kenn.io/fotobank/internal/owners"
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
		OriginalFilename: "x.jpg",
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

// seedMediaGPSWithCamera inserts a GPS-tagged primary and immediately
// backfills make/model via the writeable repo. seedMediaGPS doesn't
// write make/model directly — the camera-narrowing geo tests need
// concrete values, so this helper composes the two steps.
func seedMediaGPSWithCamera(
	t *testing.T,
	fx hiddenMediaFixture,
	path, checksum string,
	lat, lon float64,
	make, model string,
) media.Media {
	t.Helper()
	m := seedMediaGPS(t, fx.repo, fx.owner, path, checksum, lat, lon)
	_, err := fx.rw.ExecContext(context.Background(),
		`UPDATE media SET make = ?, model = ? WHERE id = ?`, make, model, m.ID)
	require.NoError(t, err)
	return m
}

// seedMediaGPSWithLens mirrors seedMediaGPSWithCamera but stamps
// lens_model. The lens-narrowing geo route reads media.lens_model, so
// this helper composes a GPS row with a concrete lens for the
// `?lens=` filter test.
func seedMediaGPSWithLens(
	t *testing.T,
	fx hiddenMediaFixture,
	path, checksum string,
	lat, lon float64,
	lens string,
) media.Media {
	t.Helper()
	m := seedMediaGPS(t, fx.repo, fx.owner, path, checksum, lat, lon)
	_, err := fx.rw.ExecContext(context.Background(),
		`UPDATE media SET lens_model = ? WHERE id = ?`, lens, m.ID)
	require.NoError(t, err)
	return m
}

// seedMediaGPSTyped inserts a GPS-tagged primary with the caller-supplied
// media.Type. seedMediaGPS hardcodes media.TypePhoto; the media-type
// route filter test needs both a photo and a video to assert the
// narrowing actually splits the set.
func seedMediaGPSTyped(
	t *testing.T,
	repo *media.Repo,
	p owners.Principal,
	path, checksum string,
	lat, lon float64,
	typ media.Type,
) media.Media {
	t.Helper()
	mime := "image/jpeg"
	filename := "x.jpg"
	if typ == media.TypeVideo {
		mime = "video/mp4"
		filename = "x.mp4"
	}
	m := media.Media{
		ID:               uuid.NewString(),
		Owner:            p,
		Type:             typ,
		MimeType:         mime,
		Path:             path,
		OriginalFilename: filename,
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

// seedTagForGPSMedia attaches a single tag to a media row via the
// ai_results + media_tags pair the facet_tag query reads. Mirrors the
// pattern in internal/service/search/autocomplete_test.go::seedTagsForMedia.
// status='active' is what the join filter requires; without it the row
// is invisible to facet_tag narrowing.
func seedTagForGPSMedia(t *testing.T, fx hiddenMediaFixture, mediaID, tagKey, tagLabel string) {
	t.Helper()
	resultID := uuid.NewString()
	ctx := context.Background()
	_, err := fx.rw.ExecContext(ctx,
		`INSERT INTO ai_results(id, media_id, task, model_id, prompt_version, prompt_hash,
		 input_profile, status, generated_at) VALUES (?,?, 'tag', ?, ?, ?, ?, 'active', ?)`,
		resultID, mediaID, "test-model", "tag-v1", "test-hash", "test-profile", time.Now().UTC())
	require.NoError(t, err)
	_, err = fx.rw.ExecContext(ctx,
		`INSERT INTO media_tags(result_id, tag_key, tag_label, rank) VALUES (?,?,?,?)`,
		resultID, tagKey, tagLabel, 1)
	require.NoError(t, err)
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
	m := seedMediaGPS(t, fx.repo, fx.owner, "visible.jpg", "cs-vis", 10.0, 20.0)

	resp, err := http.Get(fx.srv.URL + "/api/v1/media/geo")
	r.NoError(err)
	defer resp.Body.Close()
	r.Equal(http.StatusOK, resp.StatusCode)

	var body struct {
		Items []map[string]any `json:"items"`
	}
	r.NoError(json.NewDecoder(resp.Body).Decode(&body))
	r.Len(body.Items, 1)
	r.Equal(m.ID, body.Items[0]["id"])
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

// TestGeoRoute_FiltersByCamera pins SF-19's ?camera= narrowing on the
// geotagged surface. Two geo-tagged rows with distinct cameras are
// seeded; the route is hit with one of them and only that row must
// return.
func TestGeoRoute_FiltersByCamera(t *testing.T) {
	r := require.New(t)
	fx := newHiddenMediaFixture(t)

	sony := seedMediaGPSWithCamera(t, fx, "2024/sony.jpg", "cs-sony", 10.0, 20.0, "Sony", "A7R IV")
	seedMediaGPSWithCamera(t, fx, "2024/canon.jpg", "cs-canon", 30.0, 40.0, "Canon", "EOS R5")

	resp, err := http.Get(fx.srv.URL + "/api/v1/media/geo?camera=Sony+A7R+IV")
	r.NoError(err)
	defer resp.Body.Close()
	r.Equal(http.StatusOK, resp.StatusCode)

	var body struct {
		Items []map[string]any `json:"items"`
	}
	r.NoError(json.NewDecoder(resp.Body).Decode(&body))
	r.Len(body.Items, 1)
	r.Equal(sony.ID, body.Items[0]["id"])
}

// TestGeoRoute_FiltersByLens mirrors the camera filter test for the
// ?lens= narrowing. Two GPS rows with distinct lens_model values seed
// the fixture; the route is hit with one and only that row must return.
func TestGeoRoute_FiltersByLens(t *testing.T) {
	r := require.New(t)
	fx := newHiddenMediaFixture(t)

	g := seedMediaGPSWithLens(t, fx, "2024/g.jpg", "cs-g", 10.0, 20.0, "FE 24-70mm F2.8 GM")
	seedMediaGPSWithLens(t, fx, "2024/zoom.jpg", "cs-zoom", 30.0, 40.0, "FE 70-200mm F2.8 GM")

	resp, err := http.Get(fx.srv.URL + "/api/v1/media/geo?lens=FE+24-70mm+F2.8+GM")
	r.NoError(err)
	defer resp.Body.Close()
	r.Equal(http.StatusOK, resp.StatusCode)

	var body struct {
		Items []map[string]any `json:"items"`
	}
	r.NoError(json.NewDecoder(resp.Body).Decode(&body))
	r.Len(body.Items, 1)
	r.Equal(g.ID, body.Items[0]["id"])
}

// TestGeoRoute_FiltersByMediaType pins the ?media_type= narrowing.
// The handler does custom string-to-pointer decoding (huma v2 panics
// on *string query params), so this regression-pins the photo/video
// split independent of the camera/lens path.
func TestGeoRoute_FiltersByMediaType(t *testing.T) {
	r := require.New(t)
	fx := newHiddenMediaFixture(t)

	seedMediaGPSTyped(t, fx.repo, fx.owner, "2024/photo.jpg", "cs-photo", 10.0, 20.0, media.TypePhoto)
	video := seedMediaGPSTyped(t, fx.repo, fx.owner, "2024/clip.mp4", "cs-video", 30.0, 40.0, media.TypeVideo)

	resp, err := http.Get(fx.srv.URL + "/api/v1/media/geo?media_type=video")
	r.NoError(err)
	defer resp.Body.Close()
	r.Equal(http.StatusOK, resp.StatusCode)

	var body struct {
		Items []map[string]any `json:"items"`
	}
	r.NoError(json.NewDecoder(resp.Body).Decode(&body))
	r.Len(body.Items, 1)
	r.Equal(video.ID, body.Items[0]["id"])
}

// TestGeoRoute_FiltersByFacetTag pins the ?facet_tag= narrowing. Both
// rows are GPS-tagged; only one carries the dog tag via the
// ai_results+media_tags join the facet path reads. The other row must
// be excluded from the response.
func TestGeoRoute_FiltersByFacetTag(t *testing.T) {
	r := require.New(t)
	fx := newHiddenMediaFixture(t)

	tagged := seedMediaGPS(t, fx.repo, fx.owner, "2024/tagged.jpg", "cs-tagged", 10.0, 20.0)
	seedMediaGPS(t, fx.repo, fx.owner, "2024/untagged.jpg", "cs-untagged", 30.0, 40.0)
	seedTagForGPSMedia(t, fx, tagged.ID, "dog", "Dog")

	resp, err := http.Get(fx.srv.URL + "/api/v1/media/geo?facet_tag=dog")
	r.NoError(err)
	defer resp.Body.Close()
	r.Equal(http.StatusOK, resp.StatusCode)

	var body struct {
		Items []map[string]any `json:"items"`
	}
	r.NoError(json.NewDecoder(resp.Body).Decode(&body))
	r.Len(body.Items, 1)
	r.Equal(tagged.ID, body.Items[0]["id"])
}

// TestGeoRoute_ResolvesAsListNotDetail asserts that GET /api/v1/media/geo
// resolves to the list-shaped geo handler, not GET /api/v1/media/{id}
// with id="geo". Go 1.22+ ServeMux pattern specificity makes this
// behavior independent of registration order, so this test pins the
// resolution shape rather than the registration order.
func TestGeoRoute_ResolvesAsListNotDetail(t *testing.T) {
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
