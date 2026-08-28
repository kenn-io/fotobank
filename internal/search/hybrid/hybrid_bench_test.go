package hybrid_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/ai/embedding"
	"go.kenn.io/fotobank/internal/db"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/search"
	"go.kenn.io/fotobank/internal/search/hybrid"
	"go.kenn.io/fotobank/internal/search/index"
	"go.kenn.io/fotobank/internal/testutil/mediaseed"
)

// Hybrid scale benchmarks for search v1 sizing. Run with:
//
//	go test -tags sqlite_fts5 -run '^$' -bench BenchmarkHybrid -benchmem ./internal/search/hybrid
//
// Three shapes:
//
//   - FilterOnly: no query, owner-scoped CTE with all SF-3 facet
//     predicates active. Measures the camera/lens/tag/GPS expression-
//     index path at 100k rows.
//   - BM25Only:   "sunset" against the seeded caption corpus, no
//     filters. Measures FTS5 matchinfo + the filter CTE join when
//     the filter is just owner.
//   - BM25 + filters: "sunset" combined with the most-used camera +
//     lens + tag predicates. Measures the JOIN-heavy intersection
//     between FTS5 candidates and the filter CTE.
//
// FusedSearch (BM25 + ANN + RRF) is intentionally out of scope here —
// it requires sqlite-vec embeddings populated for the active
// generation, which is search v1 work (kata #8) and lives elsewhere.
//
// The fixture is shared across all benches in the process via
// sync.Once, the same pattern internal/media/repo_bench_test.go uses.
// The DB lives in a leaked temp dir; the OS reclaims it on reboot.

const benchScale = 100_000

var benchOwner = owners.Principal{Hub: "bench-hub", UserID: "bench-user"}

var (
	fixOnce sync.Once
	fixDB   *db.DB
	fixErr  error
)

// loadHybridScaleFixture seeds (or returns the cached) 100k-row library
// with media_fts populated. SeedScaleLibrary handles the media + tag
// rows; SeedFTSCorpus layers caption rows + a bulk media_fts INSERT on
// top so BM25 has a non-trivial corpus to rank against. Callers MUST
// NOT mutate the DB.
func loadHybridScaleFixture(b *testing.B) *db.DB {
	b.Helper()
	fixOnce.Do(func() {
		db.RegisterSqliteVec()
		dir, err := os.MkdirTemp("", "fotobank-hybrid-bench-*")
		if err != nil {
			fixErr = fmt.Errorf("mkdir tmp: %w", err)
			return
		}
		d, err := db.Open(filepath.Join(dir, "scale.db"))
		if err != nil {
			fixErr = fmt.Errorf("open db: %w", err)
			return
		}
		_ = mediaseed.SeedScaleLibrary(b, d.WriteDB(), benchOwner,
			mediaseed.DefaultScaleOpts(benchScale))
		mediaseed.SeedFTSCorpus(b, d.WriteDB(), benchOwner,
			mediaseed.DefaultFTSOpts())
		fixDB = d
	})
	require.NoError(b, fixErr)
	if fixDB == nil {
		panic("loadHybridScaleFixture: fixDB nil after error-free seed — fixture init is broken")
	}
	return fixDB
}

// newBenchEngine wires a hybrid.Engine over the supplied DB with nil
// generations + nil embed client. With no active generation the
// engine's routing logic flips semanticUnavailable=true on every
// non-empty-query request and falls back to BM25Only — exactly the
// path we want to measure here. FusedSearch (BM25 + ANN) is a search
// v1 follow-up and intentionally out of scope.
func newBenchEngine(d *db.DB) *hybrid.Engine {
	backend := index.NewSQLiteVecBackend(d.ReadDB(), embedding.Row{})
	cfg := search.Config{KPerSignal: 200, RRFK: 60}
	return hybrid.NewEngine(backend, nil, nil, cfg)
}

// BenchmarkHybrid_FilterOnly_100k measures the empty-query route at
// 100k rows with every composable filter predicate active: cameras +
// lenses + tag keys + hasGPS + media type. The most-used values are
// probed at setup so the intersection produces a non-trivial result
// set even if mediaseed's distribution shifts. A regression in the
// SF-3 expression-index path or the EXISTS subquery for tags surfaces
// here as a multi-x slowdown vs the repo-only AllFacetFilters bench.
func BenchmarkHybrid_FilterOnly_100k(b *testing.B) {
	d := loadHybridScaleFixture(b)
	eng := newBenchEngine(d)

	camera := mostUsedCamera(b, d.ReadDB())
	lens := mostUsedLens(b, d.ReadDB())
	tagKey := mostUsedTagKey(b, d.ReadDB())
	hasGPS := true
	photoType := "photo"

	req := hybrid.Request{
		Owner: benchOwner,
		Query: "",
		Filter: hybrid.Input{
			Cameras:    []string{camera},
			Lenses:     []string{lens},
			AnyTagKeys: []string{tagKey},
			HasGPS:     &hasGPS,
			MediaType:  &photoType,
		},
		Limit: 200,
	}
	ctx := context.Background()

	// Cross-check probe: with the most-popular camera/lens/tag the
	// result set should be substantial. A 1-row intersection would let
	// the bench measure SQLite's degenerate fast path. Floor of 50
	// matches the repo-bench AllFacetFilters guard.
	probe, err := eng.Search(ctx, req)
	require.NoError(b, err)
	const minProbeRows = 50
	require.GreaterOrEqualf(b, len(probe.Hits), minProbeRows,
		"FilterOnly probe too small (%d < %d) — seed distribution drifted; "+
			"camera=%q lens=%q tag=%q. Bench should exercise the JOIN at "+
			"non-trivial cardinality.",
		len(probe.Hits), minProbeRows, camera, lens, tagKey)
	b.Logf("FilterOnly resolves to %d row(s) (page Limit=200)", len(probe.Hits))

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_, err := eng.Search(ctx, req)
		require.NoError(b, err)
	}
}

// BenchmarkHybrid_BM25Only_100k measures the BM25-only route — query
// "sunset" against the seeded caption corpus with only owner scoping
// (no facet filters). Exercises FTS5 matchinfo at 100k corpus size,
// the BM25 candidate cap (KPerSignal=200), and the filter CTE join
// when the CTE is owner-only.
func BenchmarkHybrid_BM25Only_100k(b *testing.B) {
	d := loadHybridScaleFixture(b)
	eng := newBenchEngine(d)

	req := hybrid.Request{
		Owner: benchOwner,
		Query: "sunset",
		Sort:  "relevance",
		Limit: 200,
	}
	ctx := context.Background()

	// Cross-check probe: SeedFTSCorpus with default opts puts ~5% of
	// rows into the "sunset" hit set (~5000 rows at 100k); the
	// per-page cap is 200, and KPerSignal=200 caps the candidate
	// pool, so the page should fill. Floor of 100 leaves slack for
	// vocabulary-pick variance.
	probe, err := eng.Search(ctx, req)
	require.NoError(b, err)
	const minBM25Rows = 100
	require.GreaterOrEqualf(b, len(probe.Hits), minBM25Rows,
		"BM25Only probe too small (%d < %d) — caption corpus or tokenizer "+
			"regression?", len(probe.Hits), minBM25Rows)
	require.True(b, probe.SemanticUnavailable,
		"engine wired with nil gens should flip semanticUnavailable")
	require.Equal(b, "no_active_generation", probe.SemanticUnavailableReason)
	b.Logf("BM25Only resolves to %d row(s) (page Limit=200)", len(probe.Hits))

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_, err := eng.Search(ctx, req)
		require.NoError(b, err)
	}
}

// BenchmarkHybrid_BM25WithFilters_100k measures the BM25 + facet
// filters intersection — "sunset" combined with the most-used camera
// + lens + any-tag predicates. The filter CTE narrows the candidate
// pool BEFORE the BM25 cap takes effect, so this is the path search
// users actually drive when they combine a query with sidebar facets.
// Slowdown vs BM25Only_100k indicates the JOIN cost is the dominant
// term; comparable timing means the filter is cheap enough to layer
// for free.
func BenchmarkHybrid_BM25WithFilters_100k(b *testing.B) {
	d := loadHybridScaleFixture(b)
	eng := newBenchEngine(d)

	camera := mostUsedCamera(b, d.ReadDB())
	lens := mostUsedLens(b, d.ReadDB())
	tagKey := mostUsedTagKey(b, d.ReadDB())

	req := hybrid.Request{
		Owner: benchOwner,
		Query: "sunset",
		Sort:  "relevance",
		Filter: hybrid.Input{
			Cameras:    []string{camera},
			Lenses:     []string{lens},
			AnyTagKeys: []string{tagKey},
		},
		Limit: 200,
	}
	ctx := context.Background()

	// Cross-check probe: the intersection of "sunset" caption hits
	// with the most-used facet values should still produce a
	// non-trivial set — caption picks and tag picks are independent
	// RNGs, and ~50% of rows are tagged ∩ ~5% have "sunset" ∩ Zipfian
	// camera/lens picks. Floor of 5 is generous because the
	// intersection cardinality drops fast; a result set near zero
	// would mean the bench measures the "I have no rows" path.
	probe, err := eng.Search(ctx, req)
	require.NoError(b, err)
	const minIntersectRows = 5
	require.GreaterOrEqualf(b, len(probe.Hits), minIntersectRows,
		"BM25WithFilters probe too small (%d < %d) — corpus + facet "+
			"intersection collapsed; camera=%q lens=%q tag=%q.",
		len(probe.Hits), minIntersectRows, camera, lens, tagKey)
	b.Logf("BM25WithFilters resolves to %d row(s) (page Limit=200)", len(probe.Hits))

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_, err := eng.Search(ctx, req)
		require.NoError(b, err)
	}
}

// mostUsedCamera/Lens/TagKey mirror the helpers in
// scale fixture queries. They stay local because the shared surface would
// still need to import sql and testing.B.
func mostUsedCamera(b *testing.B, ro *sql.DB) string {
	b.Helper()
	var camera string
	err := ro.QueryRowContext(context.Background(), `
		SELECT make || ' ' || model
		  FROM assets
		 WHERE state = 'ready' AND make IS NOT NULL AND model IS NOT NULL
		 GROUP BY make, model
		 ORDER BY COUNT(*) DESC
		 LIMIT 1`).Scan(&camera)
	require.NoError(b, err)
	return camera
}

func mostUsedLens(b *testing.B, ro *sql.DB) string {
	b.Helper()
	var lens string
	err := ro.QueryRowContext(context.Background(), `
		SELECT lens_model
		  FROM assets
		 WHERE state = 'ready' AND lens_model IS NOT NULL
		 GROUP BY lens_model
		 ORDER BY COUNT(*) DESC
		 LIMIT 1`).Scan(&lens)
	require.NoError(b, err)
	return lens
}

func mostUsedTagKey(b *testing.B, ro *sql.DB) string {
	b.Helper()
	var tagKey string
	err := ro.QueryRowContext(context.Background(), `
		SELECT tag_key
		  FROM media_tags
		 GROUP BY tag_key
		 ORDER BY COUNT(*) DESC
		 LIMIT 1`).Scan(&tagKey)
	require.NoError(b, err)
	return tagKey
}
