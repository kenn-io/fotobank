package embedding_test

import (
	"context"
	"database/sql"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/ai"
	"github.com/wesm/fotobank/internal/ai/ack"
	"github.com/wesm/fotobank/internal/ai/embedding"
	"github.com/wesm/fotobank/internal/db"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/testutil"
)

// activatorFP is the canonical fingerprint used across the activator
// tests. Distinct from embedFP so a regression that crosses test
// fixtures surfaces cleanly.
func activatorFP() ai.Fingerprint {
	return ai.Fingerprint{
		ModelID:      "siglip2",
		InputProfile: "jpeg-384-q85-metadata-stripped-embed-v1",
	}
}

// seedReadyMedia creates n thumb_status='ready' media owned by p and
// returns their ids in insertion order. SeedPhoto already defaults to
// thumb_status='ready' so this is a thin helper that just collects
// the ids for downstream mapping inserts.
func seedReadyMedia(t *testing.T, rw *sql.DB, p owners.Principal, n int) []string {
	t.Helper()
	ids := make([]string, n)
	for i := range ids {
		ids[i] = testutil.SeedPhoto(t, rw, p, "p"+strconv.Itoa(i))
	}
	return ids
}

// insertMappingRaw bypasses Mapping.WriteVector and writes a
// media_embedding_ids row directly. Used by the activator tests to
// stage exactly N mappings (the activator only reads the registry, so
// allocating real vec0 rows would be over-engineering — but the FK to
// embedding_generations.id and PK on (generation_id, media_id) still
// have to be honoured).
//
// vecID is generated as a positional offset because the UNIQUE
// (generation_id, vec_id) constraint forbids re-using values within a
// generation.
func insertMappingRaw(t *testing.T, rw *sql.DB, generationID int64, mediaID string, vecID int) {
	t.Helper()
	_, err := rw.ExecContext(context.Background(),
		`INSERT INTO media_embedding_ids(generation_id, media_id, vec_id) VALUES (?,?,?)`,
		generationID, mediaID, vecID,
	)
	require.NoError(t, err)
}

// setEmbeddedCountRaw forces the cached counter to a specific value,
// used in TestActivator_AssertiveCount_IgnoresOrphanedMappings to
// demonstrate that the activator's recount disregards the cached
// field.
func setEmbeddedCountRaw(t *testing.T, rw *sql.DB, generationID int64, n int) {
	t.Helper()
	_, err := rw.ExecContext(context.Background(),
		`UPDATE embedding_generations SET embedded_count = ? WHERE id = ?`,
		n, generationID,
	)
	require.NoError(t, err)
}

// newActivator wires an Activator over the supplied test DB with the
// configured principal, threshold, and emitter. ack.Store is
// instantiated fresh per call so tests can stage Acknowledge state
// independently of the activator instance.
func newActivator(
	t *testing.T,
	d *db.DB,
	owner owners.Principal,
	thresholdPct int,
	emitter embedding.EventEmitter,
) (*embedding.Activator, *embedding.Generations, *ack.Store) {
	t.Helper()
	gens := embedding.NewGenerations(d.WriteDB(), d.ReadDB())
	ackS := ack.New(d.WriteDB(), d.ReadDB())
	a := embedding.NewActivator(d.ReadDB(), gens, ackS, emitter, nil, embedding.ActivatorCfg{
		Principal:    owner,
		ThresholdPct: thresholdPct,
		Tick:         50 * time.Millisecond,
	})
	return a, gens, ackS
}

// genState returns the embedding_generations.state value for id —
// "building" / "active" / "retired" — so tests can assert the
// activator's effect with one query.
func genState(t *testing.T, d *db.DB, id int64) string {
	t.Helper()
	var s string
	require.NoError(t, d.ReadDB().QueryRowContext(context.Background(),
		`SELECT state FROM embedding_generations WHERE id = ?`, id,
	).Scan(&s))
	return s
}

// TestActivator_PromotesWhenThresholdMet covers the happy path: 20
// thumb-ready media, 19 mapped (95% — exactly at the default cutoff).
// One Tick promotes and emits the activation event.
func TestActivator_PromotesWhenThresholdMet(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	d := testutil.OpenTestDB(t)
	owner := testutil.SeedOwner(t, d.WriteDB(), "local", "alice")
	require.NoError(t, ack.New(d.WriteDB(), d.ReadDB()).Acknowledge(ctx, owner))

	mids := seedReadyMedia(t, d.WriteDB(), owner, 20)

	emitter := &recordingEmitter{}
	a, gens, _ := newActivator(t, d, owner, 95, emitter)

	building, err := gens.FindOrCreateBuilding(ctx, activatorFP(), 768)
	r.NoError(err)

	// Map 19 of 20: 95% ratio.
	for i := range 19 {
		insertMappingRaw(t, d.WriteDB(), building.ID, mids[i], i+1)
	}

	r.NoError(a.Tick(ctx))
	r.Equal("active", genState(t, d, building.ID), "20→19 (95%) promotes at threshold=95")
	r.Equal(int32(1), emitter.activated.Load(), "activation event must fire on promote")
}

// TestActivator_NoOpsBelowThreshold covers the negative threshold
// case: 20 thumb-ready media, 18 mapped (90%) → activator leaves the
// building row alone and emits nothing.
func TestActivator_NoOpsBelowThreshold(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	d := testutil.OpenTestDB(t)
	owner := testutil.SeedOwner(t, d.WriteDB(), "local", "alice")
	require.NoError(t, ack.New(d.WriteDB(), d.ReadDB()).Acknowledge(ctx, owner))

	mids := seedReadyMedia(t, d.WriteDB(), owner, 20)

	emitter := &recordingEmitter{}
	a, gens, _ := newActivator(t, d, owner, 95, emitter)

	building, err := gens.FindOrCreateBuilding(ctx, activatorFP(), 768)
	r.NoError(err)

	// Map 18 of 20: 90% ratio, below the 95% cutoff.
	for i := range 18 {
		insertMappingRaw(t, d.WriteDB(), building.ID, mids[i], i+1)
	}

	r.NoError(a.Tick(ctx))
	r.Equal("building", genState(t, d, building.ID), "below-threshold ratio must not promote")
	r.Equal(int32(0), emitter.activated.Load(), "no activation event when no promote happens")
}

// TestActivator_PausedOnAckRequired covers the ack gate: even at 100%
// embedded, an unacknowledged principal blocks promotion. The
// activator returns nil (it's a no-op, not an error) and the building
// row stays building.
func TestActivator_PausedOnAckRequired(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	d := testutil.OpenTestDB(t)
	owner := testutil.SeedOwner(t, d.WriteDB(), "local", "alice")
	// Deliberately do NOT call ackS.Acknowledge — IsAcknowledged
	// returns false, IsRequired (in the plan's terms) is true → pause.

	mids := seedReadyMedia(t, d.WriteDB(), owner, 20)

	emitter := &recordingEmitter{}
	a, gens, _ := newActivator(t, d, owner, 95, emitter)

	building, err := gens.FindOrCreateBuilding(ctx, activatorFP(), 768)
	r.NoError(err)

	// Map every photo: 100%. Without the ack gate this would promote.
	for i, mid := range mids {
		insertMappingRaw(t, d.WriteDB(), building.ID, mid, i+1)
	}

	r.NoError(a.Tick(ctx))
	r.Equal("building", genState(t, d, building.ID), "ack required must veto promotion at 100%")
	r.Equal(int32(0), emitter.activated.Load())
}

// TestActivator_AssertiveCount_IgnoresOrphanedMappings verifies that
// the activator recounts via JOIN media rather than trusting the
// cached embedding_generations.embedded_count field. Setup:
//
//   - 20 thumb-ready media, 19 mapped → real ratio 19/20 = 95%.
//   - One additional mapping inserted for a 21st media that is then
//     marked hidden_at. Cached embedded_count is set to 21 (forced
//     drift, simulates a stale counter).
//   - Activator's eligible recount: 20 (the hidden one is excluded).
//   - Activator's embedded recount: 19 (the hidden mapping's media
//     fails the (hidden_at IS NULL OR false) predicate, so the JOIN
//     drops it).
//   - 19/20 = 95% → promote.
//
// If the activator trusted the cached counter (21) and the cached
// eligible (also stale), it might either over- or under-count and
// promote spuriously. The fact that it converges on the JOIN-derived
// numbers is what this test pins down.
func TestActivator_AssertiveCount_IgnoresOrphanedMappings(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	d := testutil.OpenTestDB(t)
	owner := testutil.SeedOwner(t, d.WriteDB(), "local", "alice")
	require.NoError(t, ack.New(d.WriteDB(), d.ReadDB()).Acknowledge(ctx, owner))

	emitter := &recordingEmitter{}
	a, gens, _ := newActivator(t, d, owner, 95, emitter)

	building, err := gens.FindOrCreateBuilding(ctx, activatorFP(), 768)
	r.NoError(err)

	// Visible eligible set: 20 thumb-ready photos, all visible.
	mids := seedReadyMedia(t, d.WriteDB(), owner, 20)
	for i := range 19 {
		insertMappingRaw(t, d.WriteDB(), building.ID, mids[i], i+1)
	}

	// "Orphan" mapping #1: a media row that exists but is hidden_at,
	// so the (hidden_at IS NULL OR false) predicate excludes it from
	// both the eligible and embedded recounts. The mapping itself
	// stays in the table (no FK violation), demonstrating the
	// activator's recount is honest.
	hiddenMID := testutil.SeedPhoto(t, d.WriteDB(), owner, "p-hidden")
	_, err = d.WriteDB().ExecContext(ctx,
		`UPDATE media SET hidden_at = ? WHERE id = ?`, time.Now().UTC(), hiddenMID)
	r.NoError(err)
	insertMappingRaw(t, d.WriteDB(), building.ID, hiddenMID, 100)

	// Force the cached counter to a high (incorrect) value. If the
	// activator trusted this it would compute embedded=21 and either
	// over-count or panic on division — the recount path is what
	// keeps the math right.
	setEmbeddedCountRaw(t, d.WriteDB(), building.ID, 21)

	r.NoError(a.Tick(ctx))
	r.Equal("active", genState(t, d, building.ID),
		"recount under hidden predicate must yield 19/20 = 95% → promote")
	r.Equal(int32(1), emitter.activated.Load())
}

// TestActivator_RunTicksAndCancels exercises the H2 long-running loop:
// Run wakes every cfg.Tick, calls Tick, and returns nil when the
// supplied context is canceled. The test seeds an over-threshold
// generation so the first tick promotes — Run's correctness is
// observable as either (a) the row flipping to active, or (b) the
// emitter recording at least one activation event. Asserting on the
// emitter is the more direct signal.
func TestActivator_RunTicksAndCancels(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	d := testutil.OpenTestDB(t)
	owner := testutil.SeedOwner(t, d.WriteDB(), "local", "alice")
	r.NoError(ack.New(d.WriteDB(), d.ReadDB()).Acknowledge(ctx, owner))

	// Stage data the very first tick will promote: 20 ready media, 19
	// mapped (95%, exactly at the default cutoff).
	mids := seedReadyMedia(t, d.WriteDB(), owner, 20)
	emitter := &recordingEmitter{}
	a, gens, _ := newActivator(t, d, owner, 95, emitter)
	building, err := gens.FindOrCreateBuilding(ctx, activatorFP(), 768)
	r.NoError(err)
	for i := range 19 {
		insertMappingRaw(t, d.WriteDB(), building.ID, mids[i], i+1)
	}

	// Cancel-on-cleanup so a panic in the goroutine driver below cannot
	// leak the Run goroutine across test boundaries.
	ctx2, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)

	done := make(chan error, 1)
	go func() { done <- a.Run(ctx2) }()

	// Sleep long enough for at least one tick (cfg.Tick = 50ms in
	// newActivator) to fire and promote the row. 200ms gives ~3 ticks of
	// headroom on a slow CI host.
	time.Sleep(200 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		r.NoError(err, "Run must return nil on ctx cancellation")
	case <-time.After(time.Second):
		r.FailNow("Run did not return within 1s of ctx cancel")
	}

	r.GreaterOrEqual(emitter.activated.Load(), int32(1),
		"Run loop must have invoked Tick at least once and promoted the building generation")
	r.Equal("active", genState(t, d, building.ID),
		"the staged building row must have been promoted by Run's tick")
}
