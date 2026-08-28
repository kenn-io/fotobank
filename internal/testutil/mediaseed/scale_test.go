package mediaseed_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/testutil"
	"go.kenn.io/fotobank/internal/testutil/mediaseed"
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
	r.NoError(ro.QueryRowContext(ctx, `SELECT COUNT(*) FROM assets`).Scan(&nMedia))
	r.Equal(1000, nMedia)

	var nGPS int
	r.NoError(ro.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM assets WHERE latitude IS NOT NULL`).Scan(&nGPS))
	// 30% target ± 5pp under a fixed seed.
	r.InDelta(300, nGPS, 50, "GPS count %d outside expected band", nGPS)

	var nHidden int
	r.NoError(ro.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM assets WHERE hidden_at IS NOT NULL`).Scan(&nHidden))
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

// TestSeedScaleLibrary_TwoSeedsByteIdentical pins the byte-level
// determinism contract: two SeedScaleLibrary calls with the same opts
// against fresh DBs produce identical projections of the row content.
// We compare a small but meaningful set of columns across both DBs —
// any single nondeterministic path (uuid.NewString, time.Now, RNG
// reseed) would surface here. This is the regression test the
// reviewer asked for in roborev #18129.
func TestSeedScaleLibrary_TwoSeedsByteIdentical(t *testing.T) {
	r := require.New(t)
	owner := owners.Principal{Hub: "h", UserID: "u"}
	opts := mediaseed.DefaultScaleOpts(200) // small for speed

	probe := func() (mediaProj, resultsProj, tagsProj, ownerProj string) {
		d := testutil.OpenTestDB(t)
		_ = mediaseed.SeedScaleLibrary(t, d.WriteDB(), owner, opts)
		ctx := context.Background()
		ro := d.ReadDB()
		// Concatenate every media row's identifying fields plus
		// imported_at and (nullable) hidden_at into one string. Two
		// seeds with identical opts must produce identical strings.
		r.NoError(ro.QueryRowContext(ctx, `
			SELECT GROUP_CONCAT(id || '|' || docbank_virtual_path || '|' || size || '|' ||
			                    make || '|' || model || '|' || COALESCE(lens_model, '') || '|' ||
			                    imported_at || '|' || COALESCE(latitude, 0) || '|' ||
			                    COALESCE(longitude, 0) || '|' || COALESCE(hidden_at, ''),
			                    char(10))
			  FROM (
			    SELECT a.*, f.docbank_virtual_path, f.size
			    FROM assets a JOIN media_files f ON f.asset_id=a.id AND f.role='primary'
			    ORDER BY a.id
			  )`).Scan(&mediaProj))
		r.NoError(ro.QueryRowContext(ctx, `
			SELECT GROUP_CONCAT(id || '|' || media_id || '|' || generated_at, char(10))
			  FROM (SELECT * FROM ai_results ORDER BY id)`).Scan(&resultsProj))
		// Tag fan-out is part of the determinism contract — Zipf
		// pick + tag_label + rank must all be reproducible. Without
		// this projection a future drift in mediaseed's tag RNG
		// (different sample, different rank assignment) would slip
		// through.
		r.NoError(ro.QueryRowContext(ctx, `
			SELECT GROUP_CONCAT(result_id || '|' || tag_key || '|' || tag_label || '|' || rank, char(10))
			  FROM (SELECT * FROM media_tags ORDER BY result_id, tag_key)`).Scan(&tagsProj))
		r.NoError(ro.QueryRowContext(ctx, `
			SELECT hub || '|' || user_id || '|' || storage_key || '|' || created_at
			  FROM owners`).Scan(&ownerProj))
		return
	}

	m1, r1, t1, o1 := probe()
	m2, r2, t2, o2 := probe()
	r.Equal(m1, m2, "media projection diverged across two seeds with identical opts")
	r.Equal(r1, r2, "ai_results projection diverged across two seeds")
	r.Equal(t1, t2, "media_tags projection diverged across two seeds")
	r.Equal(o1, o2, "owners projection diverged across two seeds")
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

// TestSeedFTSCorpus_PopulatesIndex verifies that after the helper
// runs, media_fts is populated for every seeded row, captions are
// inserted at roughly the requested fraction, and a known-vocabulary
// query ("sunset") lands MATCH hits at a non-trivial rate. Without
// this, a future refactor that silently breaks the bulk INSERT or the
// caption fan-out would surface only as flat BM25 numbers in the
// hybrid bench — much harder to diagnose.
func TestSeedFTSCorpus_PopulatesIndex(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	owner := owners.Principal{Hub: "h", UserID: "u"}

	const total = 1000
	ids := mediaseed.SeedScaleLibrary(t, d.WriteDB(), owner,
		mediaseed.DefaultScaleOpts(total))
	r.Len(ids, total)

	mediaseed.SeedFTSCorpus(t, d.WriteDB(), owner, mediaseed.DefaultFTSOpts())

	ctx := context.Background()
	ro := d.ReadDB()

	// Every media row should have one media_fts row. The bulk INSERT
	// drives one row per media, captioned or not.
	var nFTS int
	r.NoError(ro.QueryRowContext(ctx, `SELECT COUNT(*) FROM media_fts`).Scan(&nFTS))
	r.Equal(total, nFTS)

	// CaptionFraction=0.30 ± 5pp under a fixed seed at n=1000.
	var nCaptions int
	r.NoError(ro.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM ai_results WHERE task='caption' AND status='active'`).
		Scan(&nCaptions))
	r.InDelta(300, nCaptions, 50, "caption count %d outside expected band", nCaptions)

	// Every caption ai_results row must have a media_captions text.
	var nCaptionsLinked int
	r.NoError(ro.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM ai_results r
		  JOIN media_captions mc ON mc.result_id = r.id
		 WHERE r.task='caption' AND r.status='active'`).Scan(&nCaptionsLinked))
	r.Equal(nCaptions, nCaptionsLinked, "caption ai_results without media_captions row")

	// The canonical bench query "sunset" must match a non-trivial
	// fraction of caption rows. With 6 words drawn from a 30-word
	// pool, ~18% of captions contain "sunset"; at 30% caption
	// coverage and total=1000, expect ~50 hits. Floor of 10 leaves
	// generous slack for RNG variance at this small n while still
	// failing if the corpus is empty or if the tokenizer is
	// misconfigured.
	var nHits int
	r.NoError(ro.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM media_fts WHERE media_fts MATCH 'sunset'`).Scan(&nHits))
	r.GreaterOrEqual(nHits, 10,
		"BM25 hits for 'sunset' too low (%d) — corpus or tokenizer regression", nHits)

	// Tag labels must populate the corpus. The seed labels are all
	// "scale-tag-NNNN", which the unicode61 tokenizer splits on the
	// hyphens — every tagged row gets the "scale" and "tag" tokens.
	// Probing for "scale" therefore approximates "any tagged row has a
	// non-empty tag_label". With ~50% tag coverage at total=1000,
	// expect a few hundred hits; floor of 100 leaves room for RNG
	// variance while still failing if the bulk INSERT skipped
	// tag_label entirely.
	var nTagHits int
	r.NoError(ro.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM media_fts WHERE media_fts MATCH 'scale'`).Scan(&nTagHits))
	r.GreaterOrEqual(nTagHits, 100,
		"tag_label column underpopulated (%d 'scale' hits) — bulk INSERT regression?",
		nTagHits)
}
