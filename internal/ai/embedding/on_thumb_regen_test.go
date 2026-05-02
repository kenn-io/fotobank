package embedding_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

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

// TestOnThumbRegen_SupersedesInFlightEmbedJobs pins the supersede
// contract: any pending/working/blocked embed job for the regenerated
// media must be flipped to status='superseded' so a worker mid-flight
// rolls back instead of writing a vector keyed to the prior preview.
// Other tasks (tag, caption) and other media's embed jobs must be
// untouched. The schema's ai_jobs_active_idx allows at most one
// in-flight row per (media, task), so this test parameterises over the
// three live statuses with one media each — proving the supersede
// targets all three independently.
//
// Each subtest seeds a mapping in an active generation BEFORE invoking
// OnThumbRegen so the supersede gate (only fire when mappings were
// invalidated) is satisfied — the post-fix posture restricts supersede
// to the genuine regen case (a prior embed exists), not the first
// thumb-ready transition.
func TestOnThumbRegen_SupersedesInFlightEmbedJobs(t *testing.T) {
	for _, status := range []string{"pending", "working", "blocked"} {
		t.Run(status, func(t *testing.T) {
			r := require.New(t)
			ctx := context.Background()
			d := testutil.OpenTestDB(t)
			owner := testutil.SeedOwner(t, d.WriteDB(), "hub", "alice")
			mid := testutil.SeedPhoto(t, d.WriteDB(), owner, "p1")
			otherMid := testutil.SeedPhoto(t, d.WriteDB(), owner, "p2")

			// Seed a prior mapping for mid so OnThumbRegen actually has
			// something to invalidate. Without this, the new gate
			// (invalidated > 0) would skip supersede — that's the
			// first-thumb case covered by a separate test.
			gen := mustCreateActiveGen(t, d, 768)
			mustWriteMapping(t, d, gen, mid, mockVec(768))

			// In-flight embed for mid in the loop's status. 'working'
			// requires claimed_at (NOT NULL not enforced but the
			// worker's claim-fence keys on it, so populate for realism).
			now := time.Now().UTC()
			var err error
			if status == "working" {
				_, err = d.WriteDB().ExecContext(ctx, `
					INSERT INTO ai_jobs(id, media_id, task, fingerprint, status, attempts, enqueued_at, claimed_at)
					VALUES ('j-mid', ?, 'embed', 'fp', ?, 0, ?, ?)`,
					mid, status, now, now)
			} else {
				_, err = d.WriteDB().ExecContext(ctx, `
					INSERT INTO ai_jobs(id, media_id, task, fingerprint, status, attempts, enqueued_at)
					VALUES ('j-mid', ?, 'embed', 'fp', ?, 0, ?)`,
					mid, status, now)
			}
			r.NoError(err)

			// Pending embed for OTHER media (must stay untouched).
			_, err = d.WriteDB().ExecContext(ctx, `
				INSERT INTO ai_jobs(id, media_id, task, fingerprint, status, attempts, enqueued_at)
				VALUES ('j-other', ?, 'embed', 'fp', 'pending', 0, ?)`,
				otherMid, now)
			r.NoError(err)

			// Pending TAG for mid (must stay untouched — only embed
			// supersedes on thumb regen; tag/caption aren't pixel-
			// dependent in the same way).
			_, err = d.WriteDB().ExecContext(ctx, `
				INSERT INTO ai_jobs(id, media_id, task, fingerprint, status, attempts, enqueued_at)
				VALUES ('j-tag', ?, 'tag', 'fp', 'pending', 0, ?)`,
				mid, now)
			r.NoError(err)

			// Already-done embed for mid (terminal — must stay 'done').
			_, err = d.WriteDB().ExecContext(ctx, `
				INSERT INTO ai_jobs(id, media_id, task, fingerprint, status, attempts, enqueued_at, completed_at)
				VALUES ('j-done', ?, 'embed', 'fp', 'done', 0, ?, ?)`,
				mid, now, now)
			r.NoError(err)

			r.NoError(withTx(d, func(tx *sql.Tx) error {
				return embedding.OnThumbRegen(ctx, tx, mid)
			}))

			statusOf := func(jobID string) string {
				t.Helper()
				var s string
				r.NoError(d.ReadDB().QueryRowContext(ctx,
					`SELECT status FROM ai_jobs WHERE id=?`, jobID,
				).Scan(&s))
				return s
			}
			r.Equal("superseded", statusOf("j-mid"),
				"%s embed for regenerated media must be superseded", status)
			r.Equal("pending", statusOf("j-other"),
				"another media's embed must NOT be superseded")
			r.Equal("pending", statusOf("j-tag"),
				"non-embed task must NOT be superseded")
			r.Equal("done", statusOf("j-done"),
				"terminal embed row must remain done")
		})
	}
}

// TestOnThumbRegen_SupersedesWorkingJobEvenIfNoMappings pins the
// "always supersede working" posture: a worker that has CLAIMED an
// embed job (status='working') has resolved the prior preview JPEG
// and is mid-flight on the encode/embed call. Even when no
// mappings exist yet (a first-time embed concurrent with the first
// thumb regen), letting that worker commit its vector keyed to the
// stale preview would write the wrong vector.
//
// The fix supersedes working unconditionally; pending/blocked still
// gate on invalidated > 0 so the first-thumb pending-embed case
// (covered by TestOnThumbRegen_FirstThumbDoesNotSupersedePendingEmbed)
// is preserved.
func TestOnThumbRegen_SupersedesWorkingJobEvenIfNoMappings(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	d := testutil.OpenTestDB(t)
	owner := testutil.SeedOwner(t, d.WriteDB(), "hub", "alice")
	mid := testutil.SeedPhoto(t, d.WriteDB(), owner, "p1")

	// Active gen exists but no mapping for mid — the first-time
	// embed has not committed yet. A worker has, however, already
	// claimed the row and is in mid-flight (status='working').
	_ = mustCreateActiveGen(t, d, 768)

	now := time.Now().UTC()
	_, err := d.WriteDB().ExecContext(ctx, `
		INSERT INTO ai_jobs(id, media_id, task, fingerprint, status, attempts, enqueued_at, claimed_at)
		VALUES ('j-working', ?, 'embed', 'fp', 'working', 0, ?, ?)`,
		mid, now, now)
	r.NoError(err)

	r.NoError(withTx(d, func(tx *sql.Tx) error {
		return embedding.OnThumbRegen(ctx, tx, mid)
	}))

	var status string
	r.NoError(d.ReadDB().QueryRowContext(ctx,
		`SELECT status FROM ai_jobs WHERE id=?`, "j-working",
	).Scan(&status))
	r.Equal("superseded", status,
		"working embed job must be superseded even when no mapping existed yet")
}

// TestOnThumbRegen_FirstThumbDoesNotSupersedePendingEmbed pins the
// post-fix posture: when no mappings exist in any non-retired
// generation (the first thumb-ready transition), OnThumbRegen must
// leave any pending/blocked embed jobs alone. The importer
// enqueues the embed job at the same moment the thumb worker is
// queued; the thumb worker invokes OnThumbRegen on the FIRST 'ready'
// MarkReady too, not just on a real regen. Superseding that
// legitimate pending job would force the gap scanner to re-enqueue
// it — the post-fix gate (invalidated > 0) avoids that churn.
//
// Working jobs are superseded unconditionally — see
// TestOnThumbRegen_SupersedesWorkingJobEvenIfNoMappings — so this
// test seeds only a pending row.
func TestOnThumbRegen_FirstThumbDoesNotSupersedePendingEmbed(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	d := testutil.OpenTestDB(t)
	owner := testutil.SeedOwner(t, d.WriteDB(), "hub", "alice")
	mid := testutil.SeedPhoto(t, d.WriteDB(), owner, "p1")

	// Active generation exists but no mapping for mid — i.e. the embed
	// pipeline knows about this fingerprint but hasn't yet processed
	// this media. This is exactly the first-thumb-ready state.
	_ = mustCreateActiveGen(t, d, 768)

	// Pending embed job for mid (the importer enqueued it upfront).
	now := time.Now().UTC()
	_, err := d.WriteDB().ExecContext(ctx, `
		INSERT INTO ai_jobs(id, media_id, task, fingerprint, status, attempts, enqueued_at)
		VALUES ('j-pending-embed', ?, 'embed', 'fp', 'pending', 0, ?)`,
		mid, now)
	r.NoError(err)

	r.NoError(withTx(d, func(tx *sql.Tx) error {
		return embedding.OnThumbRegen(ctx, tx, mid)
	}))

	// The legitimate pending embed job must remain pending — the gap
	// scanner has no extra work to do, the worker can claim it on its
	// next tick.
	var status string
	r.NoError(d.ReadDB().QueryRowContext(ctx,
		`SELECT status FROM ai_jobs WHERE id=?`, "j-pending-embed",
	).Scan(&status))
	r.Equal("pending", status,
		"first thumb-ready must NOT supersede a legitimate pending embed job")
}
