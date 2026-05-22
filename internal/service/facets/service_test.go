package facets_test

import (
	"context"
	"database/sql"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/auth/hidden"
	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/media"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/service/facets"
	"go.kenn.io/fotobank/internal/testutil"
	"go.kenn.io/fotobank/internal/testutil/mediaseed"
)

var seedOwner = owners.Principal{Hub: "h", UserID: "u"}

// fakeHiddenChecker drives the unlock-claim gate. valid is what Valid
// returns regardless of the claim contents — facets tests aim at the
// service's branching, not the checker's internal contract. Mirrors
// the fake declared in internal/service/search/service_test.go.
type fakeHiddenChecker struct {
	valid bool
}

func (f fakeHiddenChecker) Valid(*hidden.UnlockClaim, owners.Principal) bool {
	return f.valid
}

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

// newFacetService bundles a fresh DB plus a default-deny
// HiddenChecker. Tests that exercise the IncludeHidden gate use
// newFacetServiceWithChecker to override the checker; the rest accept
// the deny default since none of them set IncludeHidden=true.
func newFacetService(t *testing.T) (*facets.Service, *sql.DB) {
	t.Helper()
	return newFacetServiceWithChecker(t, fakeHiddenChecker{valid: false})
}

// newFacetServiceWithChecker is the explicit-checker variant used by
// the IncludeHidden gate tests.
func newFacetServiceWithChecker(t *testing.T, hc facets.HiddenChecker) (*facets.Service, *sql.DB) {
	t.Helper()
	d := testutil.OpenTestDB(t)
	rw := d.WriteDB()
	ro := d.ReadDB()
	seedOwners(t, rw, seedOwner)
	return facets.New(ro, hc), rw
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

// TestAggregate_IncludeHidden_RejectedWithoutClaim asserts the
// service-layer hidden gate is fail-closed: IncludeHidden=true with a
// nil UnlockClaim must short-circuit to errs.ErrPermissionDenied
// before any aggregation query runs. The fake checker is set to
// valid=true to prove the nil-claim path denies on its own — the
// service must not call into the checker without a non-nil claim, and
// even if it did, the gate must still deny.
func TestAggregate_IncludeHidden_RejectedWithoutClaim(t *testing.T) {
	r := require.New(t)
	svc, rw := newFacetServiceWithChecker(t, fakeHiddenChecker{valid: true})
	insertSeedFixtures(t, rw)

	_, err := svc.Aggregate(context.Background(), seedOwner, facets.Filters{
		IncludeHidden: true,
		UnlockClaim:   nil,
	})

	r.ErrorIs(err, errs.ErrPermissionDenied)
}

// TestAggregate_IncludeHidden_HonoredWithValidClaim asserts the gate
// passes when the checker returns true and IncludeHidden=true: hidden
// rows must contribute to the aggregations alongside visible rows.
// Seeds the canonical 3-row Sony/Canon fixture, then flips the
// non-geotagged Sony video to hidden via a direct UPDATE (the
// established pattern across the codebase). With IncludeHidden=true
// and a valid claim, the camera facet must still count all three rows
// (Sony=2, Canon=1) — proving hidden rows are not filtered out.
func TestAggregate_IncludeHidden_HonoredWithValidClaim(t *testing.T) {
	r := require.New(t)
	svc, rw := newFacetServiceWithChecker(t, fakeHiddenChecker{valid: true})
	insertSeedFixtures(t, rw)
	_, err := rw.ExecContext(context.Background(),
		`UPDATE media SET hidden_at = ? WHERE id = ?`,
		time.Now().UTC(), "m-sony-nogeo-video")
	r.NoError(err)

	claim := &hidden.UnlockClaim{
		Principal: seedOwner,
		ExpiresAt: time.Now().Add(time.Hour),
	}
	resp, err := svc.Aggregate(context.Background(), seedOwner, facets.Filters{
		IncludeHidden: true,
		UnlockClaim:   claim,
	})
	r.NoError(err)

	r.Equal([]facets.ValueCount{
		{Value: "Sony A7R IV", Count: 2},
		{Value: "Canon EOS R5", Count: 1},
	}, resp.Cameras, "hidden Sony video must still be counted under IncludeHidden")
}
