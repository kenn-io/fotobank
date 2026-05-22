package media_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/db"
	"go.kenn.io/fotobank/internal/media"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/testutil/mediaseed"
)

// Scale benchmarks. Run with:
//   go test -tags sqlite_fts5 -run '^$' -bench BenchmarkRepoList_100k -benchmem ./internal/media
//
// They share a single 100k-row SQLite file across all benches in the
// process via sync.Once. The DB lives in a leaked temp dir
// (os.MkdirTemp); per-Benchmark TempDir would force a fresh seed for
// every variant, and seeding 100k rows takes seconds even with the
// transactional path in mediaseed.SeedScaleLibrary. The OS reclaims
// /tmp on reboot.

const benchScale = 100_000

var benchOwner = owners.Principal{Hub: "bench-hub", UserID: "bench-user"}

var (
	fixOnce sync.Once
	fixDB   *db.DB
	fixIDs  []string
	fixErr  error
)

// loadScaleFixture seeds (or returns the cached) 100k-row library.
// First call captures the supplied *testing.B for failure reporting;
// later calls reuse the cached DB. Callers MUST NOT mutate the DB
// (the bench surface is read-only — Repo.List, Aggregate, etc.).
func loadScaleFixture(b *testing.B) (*db.DB, []string) {
	b.Helper()
	fixOnce.Do(func() {
		db.RegisterSqliteVec()
		dir, err := os.MkdirTemp("", "fotobank-scale-bench-*")
		if err != nil {
			fixErr = fmt.Errorf("mkdir tmp: %w", err)
			return
		}
		d, err := db.Open(filepath.Join(dir, "scale.db"))
		if err != nil {
			fixErr = fmt.Errorf("open db: %w", err)
			return
		}
		ids := mediaseed.SeedScaleLibrary(b, d.WriteDB(), benchOwner,
			mediaseed.DefaultScaleOpts(benchScale))
		fixDB = d
		fixIDs = ids
	})
	require.NoError(b, fixErr)
	if fixDB == nil {
		panic("loadScaleFixture: fixDB nil after error-free seed — fixture init is broken")
	}
	return fixDB, fixIDs
}

// BenchmarkRepoList_100k_FirstPage measures the cost of loading the
// first page of a 100k-row library — the path the user hits when
// they open /library cold. Stable Limit/Offset, default sort.
func BenchmarkRepoList_100k_FirstPage(b *testing.B) {
	d, _ := loadScaleFixture(b)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	filter := media.ListFilter{
		Owner:    benchOwner,
		Limit:    200,
		Offset:   0,
		SortDesc: true,
	}
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		rows, err := repo.List(ctx, filter)
		require.NoError(b, err)
		require.Len(b, rows, 200)
	}
}

// BenchmarkRepoList_100k_DeepOffset measures the cost of paging deep
// into the same 100k library — the worst case for offset pagination
// (SQLite scans-and-discards N rows before yielding the page). If
// this benchmark is dramatically slower than _FirstPage, that's the
// signal we need keyset pagination, not offset.
//
// Offset is set to half the visible-row count, not 100k - 200,
// because the seed marks ~5% of rows hidden_at IS NOT NULL and the
// default ListFilter excludes them — pushing past the end would
// quietly yield zero rows and stop measuring anything.
func BenchmarkRepoList_100k_DeepOffset(b *testing.B) {
	d, _ := loadScaleFixture(b)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	filter := media.ListFilter{
		Owner:    benchOwner,
		Limit:    200,
		Offset:   benchScale / 2, // ~50k rows skipped in the visible set
		SortDesc: true,
	}
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		rows, err := repo.List(ctx, filter)
		require.NoError(b, err)
		require.Len(b, rows, 200)
	}
}

// BenchmarkRepoList_100k_AllFacetFilters exercises every composable
// WHERE clause: cameras + lenses + tag keys + hasGPS + media type.
// Pulls the most-used values out of the live DB so the filter
// resolves to a non-trivial result set even if mediaseed's seed
// distribution shifts in the future. A regression in the SF-3
// expression-index path or in the EXISTS subquery for tags would
// show up here as a multi-x slowdown vs _FirstPage.
func BenchmarkRepoList_100k_AllFacetFilters(b *testing.B) {
	d, _ := loadScaleFixture(b)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())

	camera := mostUsedCamera(b, d.ReadDB())
	lens := mostUsedLens(b, d.ReadDB())
	tagKey := mostUsedTagKey(b, d.ReadDB())
	hasGPS := true
	photoType := media.TypePhoto

	filter := media.ListFilter{
		Owner:      benchOwner,
		Type:       &photoType,
		Cameras:    []string{camera},
		Lenses:     []string{lens},
		AnyTagKeys: []string{tagKey},
		HasGPS:     &hasGPS,
		Limit:      200,
		Offset:     0,
		SortDesc:   true,
	}
	ctx := context.Background()

	// Sanity check pre-loop: with the most-popular camera, lens, and
	// tag selected the result set should fill (or nearly fill) the
	// page. NotEmpty is too weak — a 1-row intersection would let
	// the bench measure SQLite's degenerate "I have one row" path
	// instead of the JOIN-heavy filter intersect at meaningful cardinality.
	//
	// The floor of 50 is set well below what current seed distributions
	// produce (probe currently fills the full 200-row page for the
	// most-common camera+lens+tag combo). If a future seed shift drops
	// the intersection below 50, the bench is no longer measuring what
	// it was designed to measure and the probe should be revisited.
	probe, err := repo.List(ctx, filter)
	require.NoError(b, err)
	const minProbeRows = 50
	require.GreaterOrEqualf(b, len(probe), minProbeRows,
		"AllFacetFilters intersection too small (%d < %d) — seed distribution "+
			"drifted; camera=%q lens=%q tag=%q. Bench should exercise the JOIN "+
			"at non-trivial cardinality, not the near-empty fast path.",
		len(probe), minProbeRows, camera, lens, tagKey)
	b.Logf("AllFacetFilters resolves to %d row(s) (page Limit=200)", len(probe))

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_, err := repo.List(ctx, filter)
		require.NoError(b, err)
	}
}

// BenchmarkRepoListGeo_30k measures the cost of fetching every
// geotagged row for the owner — the path /api/v1/media/geo drives
// when /map opens. ListGeo has no LIMIT (the SPA renders all GPS
// markers in one shot via Leaflet), so the bench pulls the entire
// ~30k-row result set on every iteration; that's the realistic shape.
//
// IncludeHidden=false matches the production /map default (and the
// /api/v1/media/geo handler default). The hidden-row variant is out
// of scope per the kata; if it becomes a bottleneck we'll add it
// later.
//
// Cross-check probe: seed produces ~30% GPS rows × 100k = ~30k, with
// ~5% of those also hidden → after the IncludeHidden=false filter,
// expect ~28.5k. The 25k-35k band absorbs RNG variance without letting
// the bench silently degrade if the seed distribution shifts.
func BenchmarkRepoListGeo_30k(b *testing.B) {
	d, _ := loadScaleFixture(b)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())

	filter := media.ListGeoFilter{
		Owner:         benchOwner,
		IncludeHidden: false,
	}
	ctx := context.Background()

	probe, err := repo.ListGeo(ctx, filter)
	require.NoError(b, err)
	const minGeoRows = 25_000
	const maxGeoRows = 35_000
	require.GreaterOrEqualf(b, len(probe), minGeoRows,
		"ListGeo probe too small (%d < %d) — GPS fraction in seed dropped?",
		len(probe), minGeoRows)
	require.LessOrEqualf(b, len(probe), maxGeoRows,
		"ListGeo probe too large (%d > %d) — GPS fraction in seed jumped?",
		len(probe), maxGeoRows)
	b.Logf("ListGeo resolves to %d row(s)", len(probe))

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_, err := repo.ListGeo(ctx, filter)
		require.NoError(b, err)
	}
}

// mostUsedCamera returns the canonical "<make> <model>" string for
// the camera with the most rows. Probing the DB instead of hardcoding
// the well-known seed head ("Sony scale-cam-000") keeps the bench
// robust if the seed's distribution math shifts.
func mostUsedCamera(b *testing.B, ro *sql.DB) string {
	b.Helper()
	var camera string
	err := ro.QueryRowContext(context.Background(), `
		SELECT make || ' ' || model
		  FROM media
		 WHERE make IS NOT NULL AND model IS NOT NULL
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
		  FROM media
		 WHERE lens_model IS NOT NULL
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
