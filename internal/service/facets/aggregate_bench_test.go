package facets_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/auth/hidden"
	"go.kenn.io/fotobank/internal/db"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/service/facets"
	"go.kenn.io/fotobank/internal/testutil/mediaseed"
)

// Scale benchmarks for FacetService.Aggregate at 100k rows. Run with:
//   go test -tags sqlite_fts5 -run '^$' -bench Aggregate_100k -benchmem ./internal/service/facets

const benchScale = 100_000

var benchOwner = owners.Principal{Hub: "bench-hub", UserID: "bench-user"}

// denyChecker rejects every claim. Aggregate benches don't exercise
// IncludeHidden=true, so the gate's branch is never taken — keeping
// the checker minimal avoids importing fakeHiddenChecker from the
// service_test.go internal-test package.
type denyChecker struct{}

func (denyChecker) Valid(*hidden.UnlockClaim, owners.Principal) bool { return false }

var (
	fixOnce sync.Once
	fixDB   *db.DB
	fixErr  error
)

func loadScaleFixture(b *testing.B) *db.DB {
	b.Helper()
	fixOnce.Do(func() {
		db.RegisterSqliteVec()
		dir, err := os.MkdirTemp("", "fotobank-facets-bench-*")
		if err != nil {
			fixErr = fmt.Errorf("mkdir tmp: %w", err)
			return
		}
		d, err := db.Open(filepath.Join(dir, "scale.db"))
		if err != nil {
			fixErr = fmt.Errorf("open db: %w", err)
			return
		}
		mediaseed.SeedScaleLibrary(b, d.WriteDB(), benchOwner,
			mediaseed.DefaultScaleOpts(benchScale))
		fixDB = d
	})
	require.NoError(b, fixErr)
	if fixDB == nil {
		panic("loadScaleFixture: fixDB nil after error-free seed — fixture init is broken")
	}
	return fixDB
}

// BenchmarkAggregate_100k_NoFilters times a cold sidebar load: every
// facet aggregation runs against the full 100k library with no
// narrowing predicate. This is the path /facets hits when the user
// arrives on /library with an empty filter set.
func BenchmarkAggregate_100k_NoFilters(b *testing.B) {
	d := loadScaleFixture(b)
	svc := facets.New(d.ReadDB(), denyChecker{})
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		resp, err := svc.Aggregate(ctx, benchOwner, facets.Filters{})
		require.NoError(b, err)
		require.NotEmpty(b, resp.Cameras)
	}
}

// BenchmarkAggregate_100k_WithFilters runs the same aggregation under
// a populated filter (camera + tag) so the hybrid.Resolve `filter` CTE
// applies its narrowing JOINs before the per-facet aggregations. The
// exclude-self rule means each facet's own clause is dropped before
// resolving, but the other narrowing clauses still run — so this
// measures the JOIN-heavy path, not the cheap unfiltered path.
func BenchmarkAggregate_100k_WithFilters(b *testing.B) {
	d := loadScaleFixture(b)
	svc := facets.New(d.ReadDB(), denyChecker{})

	camera := mostUsedCamera(b, d)
	tagKey := mostUsedTagKey(b, d)

	filters := facets.Filters{
		Cameras:    []string{camera},
		AnyTagKeys: []string{tagKey},
	}
	ctx := context.Background()

	// Probe once to confirm the filter resolves to a non-empty result.
	// MediaTypes is the right facet to assert on: the exclude-self
	// rule clears each facet's OWN selection before aggregating, so
	// probe.Cameras runs without the camera filter and would surface
	// rows even if the camera+tag intersection were empty. MediaTypes
	// keeps BOTH the camera and tag filters applied, so a non-empty
	// MediaTypes result proves the JOIN-heavy filter path the bench
	// is meant to measure actually narrows to >0 rows.
	probe, err := svc.Aggregate(ctx, benchOwner, filters)
	require.NoError(b, err)
	require.NotEmptyf(b, probe.MediaTypes,
		"WithFilters resolved to empty media-types facet under camera+tag — "+
			"seed drifted; camera=%q tag=%q", camera, tagKey)
	b.Logf("WithFilters: cameras=%d lenses=%d tags=%d media_types=%d",
		len(probe.Cameras), len(probe.Lenses), len(probe.Tags), len(probe.MediaTypes))

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_, err := svc.Aggregate(ctx, benchOwner, filters)
		require.NoError(b, err)
	}
}

func mostUsedCamera(b *testing.B, d *db.DB) string {
	b.Helper()
	var camera string
	err := d.ReadDB().QueryRowContext(context.Background(), `
		SELECT make || ' ' || model
		  FROM assets
		 WHERE state = 'ready' AND make IS NOT NULL AND model IS NOT NULL
		 GROUP BY make, model
		 ORDER BY COUNT(*) DESC
		 LIMIT 1`).Scan(&camera)
	require.NoError(b, err)
	return camera
}

func mostUsedTagKey(b *testing.B, d *db.DB) string {
	b.Helper()
	var tagKey string
	err := d.ReadDB().QueryRowContext(context.Background(), `
		SELECT tag_key
		  FROM media_tags
		 GROUP BY tag_key
		 ORDER BY COUNT(*) DESC
		 LIMIT 1`).Scan(&tagKey)
	require.NoError(b, err)
	return tagKey
}
