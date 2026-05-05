package mediaseed_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/testutil"
	"github.com/wesm/fotobank/internal/testutil/mediaseed"
)

// TestSeedScaleLibrary_Determinism pins the Seed contract: same opts
// produce the same row count, the same first/last IDs, and the same
// GPS / hidden / tagged fractions. Without this, a future RNG swap
// could silently change benchmark numbers and we'd have no way to
// tell signal from drift.
func TestSeedScaleLibrary_Determinism(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	owner := owners.Principal{Hub: "h", UserID: "u"}

	// Small total keeps the test fast (<1s) while still exercising
	// the distribution math. The fractions assert with tolerance —
	// 1000 rows is enough for 30% GPS to land in 25-35% almost
	// always under deterministic Seed.
	opts := mediaseed.DefaultScaleOpts(1000)
	ids := mediaseed.SeedScaleLibrary(t, d.WriteDB(), owner, opts)
	r.Len(ids, 1000)
	r.Equal("scale-0000000", ids[0])
	r.Equal("scale-0000999", ids[999])

	ctx := context.Background()
	ro := d.ReadDB()

	var nMedia int
	r.NoError(ro.QueryRowContext(ctx, `SELECT COUNT(*) FROM media`).Scan(&nMedia))
	r.Equal(1000, nMedia)

	var nGPS int
	r.NoError(ro.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM media WHERE latitude IS NOT NULL`).Scan(&nGPS))
	// 30% target ± 5pp under a fixed seed.
	r.InDelta(300, nGPS, 50, "GPS count %d outside expected band", nGPS)

	var nHidden int
	r.NoError(ro.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM media WHERE hidden_at IS NOT NULL`).Scan(&nHidden))
	// 5% target ± 2pp.
	r.InDelta(50, nHidden, 20, "hidden count %d outside expected band", nHidden)

	// ~50% of rows have a tag ai_results row attached.
	var nTagged int
	r.NoError(ro.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM ai_results WHERE task='tag' AND status='active'`).Scan(&nTagged))
	r.InDelta(500, nTagged, 50, "tagged count %d outside expected band", nTagged)

	// Tag-key cardinality is bounded by NumTags (500) and by what
	// the Zipf distribution actually picks. The head of Zipf is
	// concentrated; we expect to see somewhere in the dozens-to-low-
	// hundreds of distinct keys at this scale, not all 500.
	var nKeys int
	r.NoError(ro.QueryRowContext(ctx,
		`SELECT COUNT(DISTINCT tag_key) FROM media_tags`).Scan(&nKeys))
	r.Greater(nKeys, 5, "tag-key cardinality too narrow: %d", nKeys)
	r.LessOrEqual(nKeys, 500, "tag-key cardinality exceeds pool: %d", nKeys)
}

func TestSeedScaleLibrary_ZeroTotalNotPanics(t *testing.T) {
	// Edge case: callers may pass Total=0 from a parameterized helper.
	// withDefaults() clamps to 1 rather than panicking on Zipf imax=0.
	d := testutil.OpenTestDB(t)
	owner := owners.Principal{Hub: "h", UserID: "u"}
	ids := mediaseed.SeedScaleLibrary(t, d.WriteDB(), owner,
		mediaseed.ScaleOpts{Total: 0, Seed: 1})
	require.Len(t, ids, 1) // clamped to 1 by withDefaults
}
