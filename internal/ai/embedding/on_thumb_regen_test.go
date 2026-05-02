package embedding_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/ai"
	"github.com/wesm/fotobank/internal/ai/embedding"
	"github.com/wesm/fotobank/internal/db"
	"github.com/wesm/fotobank/internal/testutil"
)

// mustCreateActiveGen creates a fresh building generation and promotes
// it to active. The fingerprint is derived from t.Name + suffix so each
// helper invocation produces a distinct registry row — the
// embedding_generations.fingerprint UNIQUE constraint would otherwise
// reject collisions across helper calls within the same test.
func mustCreateActiveGen(t *testing.T, d *db.DB, dim int) embedding.Row {
	t.Helper()
	g := embedding.NewGenerations(d.WriteDB(), d.ReadDB())
	fp := ai.Fingerprint{
		ModelID:      "siglip2",
		InputProfile: fmt.Sprintf("active-%s", t.Name()),
	}
	row, err := g.FindOrCreateBuilding(context.Background(), fp, dim)
	require.NoError(t, err)
	require.NoError(t, g.Promote(context.Background(), row.ID))
	// Re-read so the returned row reflects the post-Promote state
	// (state='active', activated_at set). Tests that read EmbeddedCount
	// later still go through GetByID for the freshest counters.
	got, err := g.GetByID(context.Background(), row.ID)
	require.NoError(t, err)
	return *got
}

// mustCreateRetiredGen creates a building gen, promotes it, and then
// retires it — walking the full lifecycle so the row carries an
// activated_at AND a retired_at. This shape exercises OnThumbRegen's
// "leave retired alone" branch more realistically than retiring a
// never-active row.
func mustCreateRetiredGen(t *testing.T, d *db.DB, dim int) embedding.Row {
	t.Helper()
	g := embedding.NewGenerations(d.WriteDB(), d.ReadDB())
	fp := ai.Fingerprint{
		ModelID:      "siglip2",
		InputProfile: fmt.Sprintf("retired-%s", t.Name()),
	}
	row, err := g.FindOrCreateBuilding(context.Background(), fp, dim)
	require.NoError(t, err)
	require.NoError(t, g.Promote(context.Background(), row.ID))
	require.NoError(t, g.Retire(context.Background(), row.ID))
	got, err := g.GetByID(context.Background(), row.ID)
	require.NoError(t, err)
	return *got
}

// mustWriteMapping writes a single (gen, mid) mapping + vec0 row in its
// own transaction. Mirrors the production WriteVectorTx call pattern —
// the +1/0 delta is reconciled into embedded_count so the row's cached
// counter matches the actual mapping count after seeding.
func mustWriteMapping(t *testing.T, d *db.DB, gen embedding.Row, mid string, vec []float32) {
	t.Helper()
	ctx := context.Background()
	tx, err := d.WriteDB().BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	delta, err := embedding.WriteVectorTx(ctx, tx, gen, mid, vec)
	require.NoError(t, err)
	if delta != 0 {
		// Reconcile cached count so embedded_count matches the actual
		// mapping count post-seed. OnThumbRegen will subtract 1 when it
		// drops the mapping; the test asserts the post-state count.
		_, err = tx.ExecContext(ctx,
			`UPDATE embedding_generations SET embedded_count = embedded_count + ? WHERE id = ?`,
			delta, gen.ID)
		require.NoError(t, err)
	}
	require.NoError(t, tx.Commit())
}

// mappingExistsInGen reports whether (gen, mid) has a mapping row.
// Reads through the ro pool; OnThumbRegen runs in the writer's tx, so a
// post-Commit ro read sees the result of any DELETE that fired.
func mappingExistsInGen(t *testing.T, d *db.DB, gen embedding.Row, mid string) bool {
	t.Helper()
	var n int
	require.NoError(t, d.ReadDB().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM media_embedding_ids WHERE generation_id = ? AND media_id = ?`,
		gen.ID, mid,
	).Scan(&n))
	return n > 0
}

// vecRowCountInGen returns the number of rows in the per-generation
// vec0 table. OnThumbRegen drops the vec0 row alongside the mapping;
// this helper lets the test check the §5.4 invariant (mapping + vec0
// move together) is preserved.
func vecRowCountInGen(t *testing.T, d *db.DB, gen embedding.Row) int {
	t.Helper()
	var n int
	// Vec table name is application-derived ("media_embeddings_g{id}");
	// safe to interpolate as it cannot be user-controlled.
	require.NoError(t, d.ReadDB().QueryRowContext(context.Background(),
		fmt.Sprintf(`SELECT COUNT(*) FROM %s`, gen.VecTableName),
	).Scan(&n))
	return n
}

// withTx runs fn inside a write transaction on d, committing on success
// and rolling back on error — used to drive OnThumbRegen the way the
// thumb worker will (one tx per finalize call).
func withTx(d *db.DB, fn func(tx *sql.Tx) error) error {
	tx, err := d.WriteDB().BeginTx(context.Background(), nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// mustGet unwraps a Generations.GetByID call's (*Row, error) tuple,
// failing the test on error and dereferencing on success. Local to
// this file so the call sites read like the plan's `must(...)`
// shorthand without needing tuple-spread tricks.
func mustGet(t *testing.T, gens *embedding.Generations, id int64) embedding.Row {
	t.Helper()
	row, err := gens.GetByID(context.Background(), id)
	require.NoError(t, err)
	require.NotNil(t, row)
	return *row
}

func TestOnThumbRegen_InvalidatesAcrossNonRetiredGens(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	d := testutil.OpenTestDB(t)
	owner := testutil.SeedOwner(t, d.WriteDB(), "hub", "alice")
	mid := testutil.SeedPhoto(t, d.WriteDB(), owner, "p1")

	// Seed two non-retired gens — one active, one building — each with
	// a mapping for the same media. The invalidation must fire on both.
	a := mustCreateActiveGen(t, d, 768)
	b := mustCreateBuildingGen(t, d, 768)
	mustWriteMapping(t, d, a, mid, mockVec(768))
	mustWriteMapping(t, d, b, mid, mockVec(768))

	// Sanity: pre-state shows mappings + counts on both.
	r.True(mappingExistsInGen(t, d, a, mid))
	r.True(mappingExistsInGen(t, d, b, mid))
	r.Equal(1, vecRowCountInGen(t, d, a))
	r.Equal(1, vecRowCountInGen(t, d, b))

	// Run OnThumbRegen inside one write tx.
	r.NoError(withTx(d, func(tx *sql.Tx) error {
		return embedding.OnThumbRegen(ctx, tx, mid)
	}))

	// Mappings + vec0 rows gone for both non-retired gens.
	r.False(mappingExistsInGen(t, d, a, mid), "active gen mapping must be dropped")
	r.False(mappingExistsInGen(t, d, b, mid), "building gen mapping must be dropped")
	r.Equal(0, vecRowCountInGen(t, d, a), "active gen vec0 row must be dropped")
	r.Equal(0, vecRowCountInGen(t, d, b), "building gen vec0 row must be dropped")

	// embedded_count decremented on both gens (was 1, now 0).
	gens := embedding.NewGenerations(d.WriteDB(), d.ReadDB())
	rowA := mustGet(t, gens, a.ID)
	r.Equal(0, rowA.EmbeddedCount, "active gen embedded_count must decrement to 0")
	rowB := mustGet(t, gens, b.ID)
	r.Equal(0, rowB.EmbeddedCount, "building gen embedded_count must decrement to 0")

	// Re-enqueue is NOT done in OnThumbRegen — gap scanner picks up.
	// (No ai_jobs row asserted here; that's covered by the gap scanner
	// tests in Task G1.)
}

func TestOnThumbRegen_LeavesRetiredGensAlone(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	d := testutil.OpenTestDB(t)
	owner := testutil.SeedOwner(t, d.WriteDB(), "hub", "alice")
	mid := testutil.SeedPhoto(t, d.WriteDB(), owner, "p1")

	// One retired gen + one active gen. The retired gen must be
	// untouched; the active gen must have its mapping invalidated.
	retired := mustCreateRetiredGen(t, d, 768)
	active := mustCreateActiveGen(t, d, 768)
	mustWriteMapping(t, d, retired, mid, mockVec(768))
	mustWriteMapping(t, d, active, mid, mockVec(768))

	// Sanity pre-state.
	r.True(mappingExistsInGen(t, d, retired, mid))
	r.True(mappingExistsInGen(t, d, active, mid))

	r.NoError(withTx(d, func(tx *sql.Tx) error {
		return embedding.OnThumbRegen(ctx, tx, mid)
	}))

	// Retired gen's mapping + vec0 row + embedded_count must be intact.
	r.True(mappingExistsInGen(t, d, retired, mid),
		"retired gen mapping must NOT be dropped")
	r.Equal(1, vecRowCountInGen(t, d, retired),
		"retired gen vec0 row must remain")
	gens := embedding.NewGenerations(d.WriteDB(), d.ReadDB())
	rowRetired := mustGet(t, gens, retired.ID)
	r.Equal(1, rowRetired.EmbeddedCount,
		"retired gen embedded_count must remain unchanged")

	// Active gen's mapping must be gone.
	r.False(mappingExistsInGen(t, d, active, mid),
		"active gen mapping must be dropped")
	rowActive := mustGet(t, gens, active.ID)
	r.Equal(0, rowActive.EmbeddedCount,
		"active gen embedded_count must decrement to 0")
}

// TestOnThumbRegen_NoMappingIsNoOp covers the case where the media has
// never been embedded in any generation (e.g. thumb regenerated before
// the embed worker ever picked it up). OnThumbRegen must succeed and
// leave embedded_count unchanged on every non-retired gen.
func TestOnThumbRegen_NoMappingIsNoOp(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	d := testutil.OpenTestDB(t)
	owner := testutil.SeedOwner(t, d.WriteDB(), "hub", "alice")
	mid := testutil.SeedPhoto(t, d.WriteDB(), owner, "p1")

	// One non-retired gen, but no mapping for mid. Pre-bump the
	// embedded_count so we can detect any spurious decrement.
	a := mustCreateActiveGen(t, d, 768)
	gens := embedding.NewGenerations(d.WriteDB(), d.ReadDB())
	r.NoError(gens.SetEmbeddedCount(ctx, a.ID, 7))

	r.NoError(withTx(d, func(tx *sql.Tx) error {
		return embedding.OnThumbRegen(ctx, tx, mid)
	}))

	// embedded_count must be untouched — no mapping was dropped.
	rowA := mustGet(t, gens, a.ID)
	r.Equal(7, rowA.EmbeddedCount,
		"embedded_count must NOT decrement when no mapping existed")
}

// TestOnThumbRegen_NoGenerationsIsNoOp covers the early-bootstrap case
// where embedding_generations is empty (boot-time probe hasn't run, or
// no embed model was ever configured). The helper must succeed without
// error so the thumb worker's success path doesn't fail when embeddings
// are disabled.
func TestOnThumbRegen_NoGenerationsIsNoOp(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	d := testutil.OpenTestDB(t)
	owner := testutil.SeedOwner(t, d.WriteDB(), "hub", "alice")
	mid := testutil.SeedPhoto(t, d.WriteDB(), owner, "p1")

	r.NoError(withTx(d, func(tx *sql.Tx) error {
		return embedding.OnThumbRegen(ctx, tx, mid)
	}))
}
