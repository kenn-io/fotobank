package facets_test

import (
	"context"
	"database/sql"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/media"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/service/facets"
	"github.com/wesm/fotobank/internal/testutil"
	"github.com/wesm/fotobank/internal/testutil/mediaseed"
)

var seedOwner = owners.Principal{Hub: "h", UserID: "u"}

// insertSeedFixtures plants the canonical 3-row Sony/Canon fixture
// shape every Aggregate test reuses unless it asserts a more focused
// scope. Two Sony rows (one geotagged photo, one non-geotagged video),
// one geotagged Canon photo. Two photos share the FE 24-70mm lens.
// The geotagged Sony photo carries tag "dog"; the geotagged Canon
// photo carries tag "cat".
func insertSeedFixtures(t *testing.T, rw *sql.DB) {
	t.Helper()
	mediaseed.InsertMedia(t, rw, seedOwner, "m-sony-geo-photo", media.Media{
		Type: media.TypePhoto, Make: "Sony", Model: "A7R IV",
		LensModel: "FE 24-70mm F2.8 GM",
		Latitude:  new(48.8), Longitude: new(2.3),
	})
	mediaseed.InsertMedia(t, rw, seedOwner, "m-sony-nogeo-video", media.Media{
		Type: media.TypeVideo, Make: "Sony", Model: "A7R IV",
	})
	mediaseed.InsertMedia(t, rw, seedOwner, "m-canon-geo-photo", media.Media{
		Type: media.TypePhoto, Make: "Canon", Model: "EOS R5",
		LensModel: "FE 24-70mm F2.8 GM",
		Latitude:  new(40.7), Longitude: new(-74.0),
	})
	mediaseed.InsertTag(t, rw, seedOwner, "m-sony-geo-photo", "dog", "Dog")
	mediaseed.InsertTag(t, rw, seedOwner, "m-canon-geo-photo", "cat", "Cat")
}

// seedOwners writes the owners row for principal so media inserts'
// FK to owners resolves. testutil.SeedOwner is the SeedPhoto-style
// helper that exists exactly for this.
func seedOwners(t *testing.T, rw *sql.DB, principals ...owners.Principal) {
	t.Helper()
	for _, p := range principals {
		testutil.SeedOwner(t, rw, p.Hub, p.UserID)
	}
}

func newFacetService(t *testing.T) (*facets.Service, *sql.DB) {
	t.Helper()
	d := testutil.OpenTestDB(t)
	rw := d.WriteDB()
	ro := d.ReadDB()
	seedOwners(t, rw, seedOwner)
	return facets.New(ro), rw
}

// TestAggregate_Cameras asserts the camera facet returns each (make,
// model) bucket sorted by descending count then ascending value, and
// that owner-scoped seed fixtures all land in the result.
func TestAggregate_Cameras(t *testing.T) {
	r := require.New(t)
	svc, rw := newFacetService(t)
	insertSeedFixtures(t, rw)

	resp, err := svc.Aggregate(context.Background(), seedOwner, facets.Filters{})
	r.NoError(err)

	r.Equal([]facets.ValueCount{
		{Value: "Sony A7R IV", Count: 2},
		{Value: "Canon EOS R5", Count: 1},
	}, resp.Cameras)
}

// TestAggregate_Lenses asserts the lens facet returns one bucket per
// distinct lens_model string. Both Sony-geo-photo and Canon-geo-photo
// share the FE 24-70mm; the non-geotagged Sony video has no lens.
func TestAggregate_Lenses(t *testing.T) {
	r := require.New(t)
	svc, rw := newFacetService(t)
	insertSeedFixtures(t, rw)

	resp, err := svc.Aggregate(context.Background(), seedOwner, facets.Filters{})
	r.NoError(err)

	r.Equal([]facets.ValueCount{
		{Value: "FE 24-70mm F2.8 GM", Count: 2},
	}, resp.Lenses)
}

// TestAggregate_Tags asserts tag aggregation joins ai_results +
// media_tags and returns both the canonical key (URL param) and the
// human-readable label so the FilterSidebar can render chips without
// a second round-trip.
func TestAggregate_Tags(t *testing.T) {
	r := require.New(t)
	svc, rw := newFacetService(t)
	insertSeedFixtures(t, rw)

	resp, err := svc.Aggregate(context.Background(), seedOwner, facets.Filters{})
	r.NoError(err)

	keys := make([]string, 0, len(resp.Tags))
	labels := make(map[string]string, len(resp.Tags))
	for _, tc := range resp.Tags {
		keys = append(keys, tc.Key)
		labels[tc.Key] = tc.Label
	}
	sort.Strings(keys)
	r.Equal([]string{"cat", "dog"}, keys)
	r.Equal("Cat", labels["cat"])
	r.Equal("Dog", labels["dog"])
}

// TestAggregate_Places asserts the with-GPS / without-GPS bucket counts
// match the seed (2 geotagged + 1 non-geotagged).
func TestAggregate_Places(t *testing.T) {
	r := require.New(t)
	svc, rw := newFacetService(t)
	insertSeedFixtures(t, rw)

	resp, err := svc.Aggregate(context.Background(), seedOwner, facets.Filters{})
	r.NoError(err)

	r.Equal(facets.PlacesCount{WithGPS: 2, WithoutGPS: 1}, resp.Places)
}

// TestAggregate_MediaTypes asserts both photo and video buckets are
// emitted and counted (2 photo + 1 video from the seed).
func TestAggregate_MediaTypes(t *testing.T) {
	r := require.New(t)
	svc, rw := newFacetService(t)
	insertSeedFixtures(t, rw)

	resp, err := svc.Aggregate(context.Background(), seedOwner, facets.Filters{})
	r.NoError(err)

	r.Equal([]facets.ValueCount{
		{Value: "photo", Count: 2},
		{Value: "video", Count: 1},
	}, resp.MediaTypes)
}

// TestAggregate_ExcludeSelfRule_Cameras asserts the Lightroom
// exclude-self rule. When the caller selects Cameras=["Sony A7R IV"],
// the camera facet itself MUST still surface every camera in the
// caller's library so the user can switch selection. The other facets
// (lenses, places, media types, tags) MUST reflect the Sony scope.
func TestAggregate_ExcludeSelfRule_Cameras(t *testing.T) {
	r := require.New(t)
	svc, rw := newFacetService(t)
	insertSeedFixtures(t, rw)

	resp, err := svc.Aggregate(context.Background(), seedOwner, facets.Filters{
		Cameras: []string{"Sony A7R IV"},
	})
	r.NoError(err)

	r.Equal([]facets.ValueCount{
		{Value: "Sony A7R IV", Count: 2},
		{Value: "Canon EOS R5", Count: 1},
	}, resp.Cameras, "camera facet ignores its own selection")

	r.Equal([]facets.ValueCount{
		{Value: "FE 24-70mm F2.8 GM", Count: 1},
	}, resp.Lenses, "lens facet narrows to Sony rows")

	r.Equal(facets.PlacesCount{WithGPS: 1, WithoutGPS: 1}, resp.Places,
		"places facet narrows to Sony rows")

	r.Equal([]facets.ValueCount{
		{Value: "photo", Count: 1},
		{Value: "video", Count: 1},
	}, resp.MediaTypes, "media-type facet narrows to Sony rows")

	tagKeys := make([]string, 0, len(resp.Tags))
	for _, tc := range resp.Tags {
		tagKeys = append(tagKeys, tc.Key)
	}
	r.Equal([]string{"dog"}, tagKeys, "tag facet narrows to Sony rows")
}

// TestAggregate_OwnerScoped asserts a different principal's rows do
// not bleed into the caller's facet counts. Seeds the canonical Sony
// fixtures plus an unrelated Pentax row owned by another principal;
// asserts only Sony/Canon appear for the caller.
func TestAggregate_OwnerScoped(t *testing.T) {
	r := require.New(t)
	svc, rw := newFacetService(t)
	other := owners.Principal{Hub: "h", UserID: "other"}
	seedOwners(t, rw, other)
	insertSeedFixtures(t, rw)
	mediaseed.InsertMedia(t, rw, other, "m-other-pentax", media.Media{
		Type: media.TypePhoto, Make: "Pentax", Model: "K-3 III",
	})

	resp, err := svc.Aggregate(context.Background(), seedOwner, facets.Filters{})
	r.NoError(err)

	values := make([]string, 0, len(resp.Cameras))
	for _, vc := range resp.Cameras {
		values = append(values, vc.Value)
	}
	sort.Strings(values)
	r.Equal([]string{"Canon EOS R5", "Sony A7R IV"}, values)
}
