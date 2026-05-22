package embedding_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/ai/ack"
	"go.kenn.io/fotobank/internal/ai/embedding"
	"go.kenn.io/fotobank/internal/db"
	"go.kenn.io/fotobank/internal/obs"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/testutil/mediaseed"
)

// Scale benchmarks for the embedding activator's count queries at
// 100k rows. Run with:
//   go test -tags sqlite_fts5 -run '^$' -bench Activator_100k -benchmem ./internal/ai/embedding
//
// EligibleCount and EmbeddedCount drive the search-completeness pill
// in the SearchHeader and gate the activator's threshold check —
// they're called on every search render and on every activator tick.
// At 100k these need to stay sub-10ms or the search route's hydration
// will visibly stall.

const (
	benchScale         = 100_000
	benchEmbeddedCount = 70_000 // ~70% of media has a mapping row
)

var benchOwner = owners.Principal{Hub: "bench-hub", UserID: "bench-user"}

var (
	fixOnce         sync.Once
	fixDB           *db.DB
	fixGenerationID int64
	fixErr          error
)

// loadActivatorFixture seeds (or returns the cached) 100k-row library
// plus a building embedding_generations row whose media_embedding_ids
// table is populated for ~70% of media. The 70% target mirrors the
// search-completeness "still indexing" state — the activator's
// hottest path is the one that runs while the build is in progress.
func loadActivatorFixture(b *testing.B) (*db.DB, int64) {
	b.Helper()
	fixOnce.Do(func() {
		db.RegisterSqliteVec()
		dir, err := os.MkdirTemp("", "fotobank-activator-bench-*")
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

		// Build one embedding_generations row in 'building' state plus
		// media_embedding_ids for the first benchEmbeddedCount media.
		// All inserts share one transaction so this stage is fast even
		// at 70k mapping rows.
		genID, err := seedBuildingGeneration(d, ids[:benchEmbeddedCount])
		if err != nil {
			fixErr = fmt.Errorf("seed generation: %w", err)
			return
		}
		fixDB = d
		fixGenerationID = genID
	})
	require.NoError(b, fixErr)
	if fixDB == nil {
		panic("loadActivatorFixture: fixDB nil after error-free seed — fixture init is broken")
	}
	return fixDB, fixGenerationID
}

// seedBuildingGeneration inserts one embedding_generations row in
// state='building' and one media_embedding_ids row per supplied
// media id. vec_table_name names a vec0 virtual table; we omit the
// actual vec table because the EligibleCount / EmbeddedCount queries
// are pure relational counts that never touch it. If a future query
// JOINs the vec table this seed will need to create it explicitly.
func seedBuildingGeneration(d *db.DB, mediaIDs []string) (int64, error) {
	ctx := context.Background()
	tx, err := d.WriteDB().BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx, `
		INSERT INTO embedding_generations (
			fingerprint, fingerprint_hash, model_id, input_profile,
			vec_table_name, dimension, state, embedded_count, threshold_pct,
			created_at
		) VALUES (?, ?, ?, ?, ?, ?, 'building', ?, 95, ?)`,
		"scale-fp", "scale-fp-hash",
		"scale-model", "scale-profile",
		"scale_vec_table", 384,
		len(mediaIDs),
		time.Now().UTC(),
	)
	if err != nil {
		_ = tx.Rollback()
		return 0, err
	}
	genID, err := res.LastInsertId()
	if err != nil {
		_ = tx.Rollback()
		return 0, err
	}

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO media_embedding_ids (generation_id, media_id, vec_id)
		VALUES (?, ?, ?)`)
	if err != nil {
		_ = tx.Rollback()
		return 0, err
	}
	defer stmt.Close()
	for i, id := range mediaIDs {
		if _, err := stmt.ExecContext(ctx, genID, id, int64(i)); err != nil {
			_ = tx.Rollback()
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return genID, nil
}

// BenchmarkActivator_100k_EligibleCount times the eligible-count
// query — runs once per activator tick and once per search render.
// The query is owner-scoped, thumb_status='ready', hidden-aware,
// and excludes ai_skipped rows.
func BenchmarkActivator_100k_EligibleCount(b *testing.B) {
	d, _ := loadActivatorFixture(b)
	gens := embedding.NewGenerations(d.WriteDB(), d.ReadDB())
	a := embedding.NewActivator(d.ReadDB(), gens, ack.New(d.WriteDB(), d.ReadDB()),
		nil, &obs.Metrics{},
		embedding.ActivatorCfg{Principal: benchOwner, ThresholdPct: 95, Tick: time.Second})
	ctx := context.Background()

	// Cross-check: probe the activator's count against a hand-rolled
	// SQL count using the same predicate. Equality between the two
	// proves we're benching the intended dataset, not a smaller one
	// that some future fixture or predicate change might silently
	// produce. Equality alone isn't enough — a fixture drift that
	// zeroes out both queries would still pass — so the floor pins
	// a meaningful cardinality (~half the seeded total after the
	// hidden/skipped predicates trim it down).
	expected := directEligibleCount(b, d)
	const minEligible = benchScale / 2
	require.GreaterOrEqualf(b, expected, minEligible,
		"eligible fixture too small (%d < %d) — seed or predicate drifted",
		expected, minEligible)
	probe, err := a.EligibleCount(ctx)
	require.NoError(b, err)
	require.Equal(b, expected, probe,
		"EligibleCount diverged from the direct SQL count — fixture or predicate drift")
	b.Logf("EligibleCount = %d", probe)

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_, err := a.EligibleCount(ctx)
		require.NoError(b, err)
	}
}

// BenchmarkActivator_100k_EmbeddedCount times the assertive
// embedded-count JOIN — re-derived from media_embedding_ids JOIN
// media on every call (the cached embedding_generations.embedded_count
// can drift). At 70k mapping rows + 100k media the JOIN is the cost
// the activator's promote-on-threshold decision pays on every tick.
func BenchmarkActivator_100k_EmbeddedCount(b *testing.B) {
	d, genID := loadActivatorFixture(b)
	gens := embedding.NewGenerations(d.WriteDB(), d.ReadDB())
	a := embedding.NewActivator(d.ReadDB(), gens, ack.New(d.WriteDB(), d.ReadDB()),
		nil, &obs.Metrics{},
		embedding.ActivatorCfg{Principal: benchOwner, ThresholdPct: 95, Tick: time.Second})
	ctx := context.Background()

	expected := directEmbeddedCount(b, d, genID)
	// Floor pins meaningful cardinality. Fixture seeds 70% mapping
	// coverage; after the hidden/skipped predicates the JOIN result
	// should still be well above benchScale/2.
	const minEmbedded = benchScale / 2
	require.GreaterOrEqualf(b, expected, minEmbedded,
		"embedded fixture too small (%d < %d) — seed or predicate drifted",
		expected, minEmbedded)
	probe, err := a.EmbeddedCount(ctx, genID)
	require.NoError(b, err)
	require.Equal(b, expected, probe,
		"EmbeddedCount diverged from the direct SQL count — fixture or predicate drift")
	b.Logf("EmbeddedCount = %d", probe)

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_, err := a.EmbeddedCount(ctx, genID)
		require.NoError(b, err)
	}
}

// directEligibleCount mirrors a.EligibleCount's predicate via raw SQL.
// Used as a cross-check: any predicate or seed drift surfaces as an
// inequality before the bench's per-iteration loop runs, so the
// reported numbers are always against the dataset we think we're
// measuring.
func directEligibleCount(b *testing.B, d *db.DB) int {
	b.Helper()
	var n int
	err := d.ReadDB().QueryRowContext(context.Background(), `
		SELECT COUNT(*) FROM media m
		 WHERE m.owner_hub = ? AND m.owner_user_id = ?
		   AND m.thumb_status = 'ready'
		   AND m.hidden_at IS NULL
		   AND NOT EXISTS (
		     SELECT 1 FROM ai_skipped sk
		      WHERE sk.media_id = m.id AND sk.task = 'embed'
		   )`,
		benchOwner.Hub, benchOwner.UserID,
	).Scan(&n)
	require.NoError(b, err)
	return n
}

// directEmbeddedCount mirrors a.EmbeddedCount's predicate via raw SQL
// for the same reason: cross-check the bench is exercising the
// intended dataset.
func directEmbeddedCount(b *testing.B, d *db.DB, genID int64) int {
	b.Helper()
	var n int
	err := d.ReadDB().QueryRowContext(context.Background(), `
		SELECT COUNT(*) FROM media_embedding_ids x
		  JOIN media m ON m.id = x.media_id
		 WHERE x.generation_id = ?
		   AND m.owner_hub = ? AND m.owner_user_id = ?
		   AND m.thumb_status = 'ready'
		   AND m.hidden_at IS NULL
		   AND NOT EXISTS (
		     SELECT 1 FROM ai_skipped sk
		      WHERE sk.media_id = m.id AND sk.task = 'embed'
		   )`,
		genID, benchOwner.Hub, benchOwner.UserID,
	).Scan(&n)
	require.NoError(b, err)
	return n
}
