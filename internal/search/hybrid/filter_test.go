package hybrid_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/search/hybrid"
)

// testOwner is the principal threaded through every Resolve call in
// this file. The exact values are arbitrary — the tests only assert
// they appear at the head of args in the documented order. Tests
// build pointer-typed Input fields with the Go 1.26 expression form
// of new() — `new("Paris")` produces *string pointing at "Paris".
var testOwner = owners.Principal{Hub: "hub-a", UserID: "user-1"}

// TestFilter_OwnerOnlyBaseline pins the minimum-shape contract: a
// pure-owner Input projects (id, timestamp, imported_at) FROM assets m
// with both owner predicates plus the default hidden-exclusion
// clause. args is exactly [hub, userID] — no per-filter binds.
func TestFilter_OwnerOnlyBaseline(t *testing.T) {
	r := require.New(t)
	cte, args := hybrid.Resolve(hybrid.Input{Owner: testOwner})

	r.Contains(cte, "SELECT m.id, m.timestamp, m.imported_at FROM assets m WHERE")
	r.Contains(cte, "m.owner_hub = ?")
	r.Contains(cte, "m.owner_user_id = ?")
	r.Contains(cte, "m.hidden_at IS NULL")
	r.Equal([]any{"hub-a", "user-1"}, args)
}

// TestFilter_DateRangeHalfOpen confirms the >=/< split: DateAfter is
// inclusive, DateBefore is exclusive. The args slice carries the two
// time.Time values immediately after the owner pair, in input order
// (after, then before).
func TestFilter_DateRangeHalfOpen(t *testing.T) {
	r := require.New(t)
	after := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	before := time.Date(2025, 7, 1, 0, 0, 0, 0, time.UTC)

	cte, args := hybrid.Resolve(hybrid.Input{
		Owner:      testOwner,
		DateAfter:  new(after),
		DateBefore: new(before),
	})

	r.Contains(cte, "m.timestamp >= ?")
	r.Contains(cte, "m.timestamp < ?")
	r.Equal([]any{"hub-a", "user-1", after, before}, args)
}

// TestFilter_TagsAreANDComposed asserts that two TagKeys produce two
// separate EXISTS clauses (so a media row must carry both tags) and
// that each EXISTS scopes to ai_results.task='tag' AND status='active'.
// The two tag-key binds appear in the args slice in input order, just
// after the owner pair.
func TestFilter_TagsAreANDComposed(t *testing.T) {
	r := require.New(t)
	cte, args := hybrid.Resolve(hybrid.Input{
		Owner:   testOwner,
		TagKeys: []string{"beach", "sunset"},
	})

	// Two EXISTS clauses, one per key.
	r.Equal(2, strings.Count(cte, "EXISTS (SELECT 1 FROM media_tags mt"))
	// Tag-corpus selectors.
	r.Contains(cte, "r.task = 'tag'")
	r.Contains(cte, "r.status = 'active'")
	r.Contains(cte, "mt.tag_key = ?")
	r.Equal([]any{"hub-a", "user-1", "beach", "sunset"}, args)
}

// TestFilter_LocationExactMatch confirms LocationLabel is bound as an
// exact-match predicate (no LIKE / no fuzz) against m.location_label.
func TestFilter_LocationExactMatch(t *testing.T) {
	r := require.New(t)
	cte, args := hybrid.Resolve(hybrid.Input{
		Owner:         testOwner,
		LocationLabel: new("Paris"),
	})

	r.Contains(cte, "m.location_label = ?")
	r.Equal([]any{"hub-a", "user-1", "Paris"}, args)
}

// TestFilter_MediaTypeFilter confirms MediaType binds against
// m.media_type with an exact-match predicate.
func TestFilter_MediaTypeFilter(t *testing.T) {
	r := require.New(t)
	cte, args := hybrid.Resolve(hybrid.Input{
		Owner:     testOwner,
		MediaType: new("photo"),
	})

	r.Contains(cte, "m.media_type = ?")
	r.Equal([]any{"hub-a", "user-1", "photo"}, args)
}

// TestFilter_HiddenExclusionByDefault: with IncludeHidden=false (the
// zero value), the resolver injects `m.hidden_at IS NULL` and consumes
// no arg.
func TestFilter_HiddenExclusionByDefault(t *testing.T) {
	r := require.New(t)
	cte, args := hybrid.Resolve(hybrid.Input{Owner: testOwner})

	r.Contains(cte, "m.hidden_at IS NULL")
	r.Equal([]any{"hub-a", "user-1"}, args)
}

// TestFilter_HiddenIncludedRequiresUnlockClaim: when IncludeHidden=true
// the resolver omits the `hidden_at IS NULL` predicate entirely so
// hidden rows can flow through. The unlock-claim check is the service
// layer's job (N1) — the filter resolver only honors the input flag.
func TestFilter_HiddenIncludedRequiresUnlockClaim(t *testing.T) {
	r := require.New(t)
	cte, args := hybrid.Resolve(hybrid.Input{
		Owner:         testOwner,
		IncludeHidden: true,
	})

	r.NotContains(cte, "hidden_at")
	r.Equal([]any{"hub-a", "user-1"}, args)
}

// TestFilter_AllSignalsArgOrder pins the full deterministic ordering
// the plan's "Watchouts" call out: hub, userID, date_after?,
// date_before?, tag_keys..., location?, media_type?. IncludeHidden
// contributes no arg in either branch.
func TestFilter_AllSignalsArgOrder(t *testing.T) {
	r := require.New(t)
	after := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	before := time.Date(2025, 7, 1, 0, 0, 0, 0, time.UTC)

	_, args := hybrid.Resolve(hybrid.Input{
		Owner:         testOwner,
		DateAfter:     new(after),
		DateBefore:    new(before),
		TagKeys:       []string{"beach", "sunset"},
		LocationLabel: new("Paris"),
		MediaType:     new("photo"),
	})

	r.Equal([]any{
		"hub-a", "user-1",
		after, before,
		"beach", "sunset",
		"Paris",
		"photo",
	}, args)
}

// TestFilter_Cameras — Cameras []string emits a single
// (make || ' ' || model) IN (?, ?, ...) cond, with one bind per value
// in input order, after the date conds.
func TestFilter_Cameras(t *testing.T) {
	r := require.New(t)
	cte, args := hybrid.Resolve(hybrid.Input{
		Owner:   testOwner,
		Cameras: []string{"Sony A7R IV", "iPhone 15 Pro"},
	})
	r.Contains(cte, "(m.make || ' ' || m.model) IN (?, ?)")
	r.Equal([]any{"hub-a", "user-1", "Sony A7R IV", "iPhone 15 Pro"}, args)
}

// TestFilter_Lenses — Lenses []string emits a single
// lens_model IN (?, ...) cond.
func TestFilter_Lenses(t *testing.T) {
	r := require.New(t)
	cte, args := hybrid.Resolve(hybrid.Input{
		Owner:  testOwner,
		Lenses: []string{"FE 24-70mm F2.8 GM"},
	})
	r.Contains(cte, "m.lens_model IN (?)")
	r.Equal([]any{"hub-a", "user-1", "FE 24-70mm F2.8 GM"}, args)
}

// TestFilter_AnyTagKeys — OR-composed tag predicate. Emits ONE EXISTS
// subquery with tag_key IN (?, ?, ...). Distinct from TagKeys which
// emits one EXISTS per key (AND across keys).
func TestFilter_AnyTagKeys(t *testing.T) {
	r := require.New(t)
	cte, args := hybrid.Resolve(hybrid.Input{
		Owner:      testOwner,
		AnyTagKeys: []string{"dog", "cat"},
	})
	// A single EXISTS — the substring 'EXISTS (' should appear once.
	r.Equal(1, strings.Count(cte, "EXISTS ("))
	r.Contains(cte, "AND mt.tag_key IN (?, ?)")
	r.Equal([]any{"hub-a", "user-1", "dog", "cat"}, args)
}

// TestFilter_HasGPS — pointer tri-state. true → IS NOT NULL pair;
// false → (IS NULL OR IS NULL); nil omits the cond.
func TestFilter_HasGPSTrue(t *testing.T) {
	r := require.New(t)
	cte, args := hybrid.Resolve(hybrid.Input{Owner: testOwner, HasGPS: new(true)})
	r.Contains(cte, "m.latitude IS NOT NULL AND m.longitude IS NOT NULL")
	r.Equal([]any{"hub-a", "user-1"}, args)
}

func TestFilter_HasGPSFalse(t *testing.T) {
	r := require.New(t)
	cte, args := hybrid.Resolve(hybrid.Input{Owner: testOwner, HasGPS: new(false)})
	r.Contains(cte, "(m.latitude IS NULL OR m.longitude IS NULL)")
	r.Equal([]any{"hub-a", "user-1"}, args)
}
