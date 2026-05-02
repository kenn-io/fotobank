package search_test

import (
	"context"
	"database/sql"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/ai"
	"github.com/wesm/fotobank/internal/ai/embedding"
	"github.com/wesm/fotobank/internal/db"
	"github.com/wesm/fotobank/internal/owners"
	searchsvc "github.com/wesm/fotobank/internal/service/search"
	"github.com/wesm/fotobank/internal/testutil"
)

// completenessFP is the fingerprint used by every completeness test.
// Value is arbitrary — the completeness queries don't care about
// fingerprint content; they only need an active generation row to
// exist with a known ID for the embedded-count bind.
func completenessFP() ai.Fingerprint {
	return ai.Fingerprint{
		ModelID:      "siglip2",
		InputProfile: "jpeg-384-q85-metadata-stripped-embed-v1",
	}
}

// newCompletenessSvc bundles a Service wired against the test DB. The
// auth-side dependencies (settings/tags/checker) are nil because no
// completeness path exercises them; the engine is also nil for the
// same reason. gens and ro are the only fields the completeness
// method consults.
func newCompletenessSvc(t *testing.T, d *db.DB) (*searchsvc.Service, *embedding.Generations) {
	t.Helper()
	gens := embedding.NewGenerations(d.WriteDB(), d.ReadDB())
	svc := searchsvc.New(nil, nil, nil, nil, gens, d.ReadDB())
	return svc, gens
}

// seedActiveGen creates a building generation and promotes it. Returns
// the generation ID for downstream mapping inserts. The vec0 table
// itself is created by FindOrCreateBuilding's transaction; the
// completeness queries don't read it (they only count rows in
// media_embedding_ids), but FindOrCreateBuilding has to succeed for
// the test wiring to be honest.
func seedActiveGen(t *testing.T, gens *embedding.Generations) int64 {
	t.Helper()
	ctx := context.Background()
	row, err := gens.FindOrCreateBuilding(ctx, completenessFP(), 768)
	require.NoError(t, err)
	require.NoError(t, gens.Promote(ctx, row.ID))
	return row.ID
}

// hideMedia stamps hidden_at on a media row. The completeness queries
// gate on (hidden_at IS NULL OR includeHidden); a NOT NULL hidden_at
// removes the row from the visible-only tally.
func hideMedia(t *testing.T, rw *sql.DB, mediaID string) {
	t.Helper()
	_, err := rw.ExecContext(context.Background(),
		`UPDATE media SET hidden_at = ? WHERE id = ?`,
		time.Now().UTC(), mediaID)
	require.NoError(t, err)
}

// insertMappingRaw writes a media_embedding_ids row directly. The
// activator tests use the same trick — the completeness queries only
// inspect this table's row count + JOIN to media, so synthesising
// mappings without spinning up a real worker keeps the fixture short.
//
// vecID is supplied positionally because UNIQUE (generation_id, vec_id)
// forbids reuse within a generation.
func insertMappingRaw(t *testing.T, rw *sql.DB, generationID int64, mediaID string, vecID int) {
	t.Helper()
	_, err := rw.ExecContext(context.Background(),
		`INSERT INTO media_embedding_ids(generation_id, media_id, vec_id) VALUES (?,?,?)`,
		generationID, mediaID, vecID)
	require.NoError(t, err)
}

// seedHiddenAwareFixture builds the shared fixture used by the
// includeHidden=true / =false tests:
//
//   - 7 visible thumb-ready media (mids[0..6])
//   - 3 hidden thumb-ready media (mids[7..9])
//   - 6 of the 7 visible media are mapped under the active generation
//   - 0 of the 3 hidden media are mapped
//
// Returns the principal, the active generation id, and the mids slice
// (visible-then-hidden order so test code can reason about which row
// is mapped).
func seedHiddenAwareFixture(t *testing.T, d *db.DB) (owners.Principal, int64, []string) {
	t.Helper()
	rw := d.WriteDB()
	owner := testutil.SeedOwner(t, rw, "test-hub", "alice")

	mids := make([]string, 10)
	for i := range mids {
		mids[i] = testutil.SeedPhoto(t, rw, owner, "p"+strconv.Itoa(i))
	}
	// Hide the last three.
	for i := 7; i < 10; i++ {
		hideMedia(t, rw, mids[i])
	}

	// Build + promote a generation, then map 6 of the 7 visible media.
	_, gens := newCompletenessSvc(t, d)
	genID := seedActiveGen(t, gens)
	for i := range 6 {
		insertMappingRaw(t, rw, genID, mids[i], i+1)
	}

	return owner, genID, mids
}

// TestCompleteness_VisibleOnlyMatchesDenominator pins the v1 hidden
// invariant: includeHidden=false counts only visible media on both
// sides of the ratio. Fixture: 7 visible / 3 hidden, 6 of 7 visible
// mapped → 6/7. The hidden rows must not appear in the denominator,
// otherwise the pill leaks hidden-only progress to a locked session
// (search-design.md §6.6).
func TestCompleteness_VisibleOnlyMatchesDenominator(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	owner, _, _ := seedHiddenAwareFixture(t, d)
	svc, _ := newCompletenessSvc(t, d)

	got, err := svc.EmbeddingCompleteness(context.Background(), owner, false)
	r.NoError(err)
	r.InDelta(6.0/7.0, got, 0.001, "visible-only count: 6/7")
}

// TestCompleteness_IncludeHiddenWithUnlockSeesAll pins the inverse:
// when the request includes hidden, both sides of the ratio widen.
// Same fixture (no hidden are mapped) → 6/10. The numerator does not
// pick up the three hidden media because the fixture didn't map them;
// the denominator does because includeHidden=true flips the gate.
func TestCompleteness_IncludeHiddenWithUnlockSeesAll(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	owner, _, _ := seedHiddenAwareFixture(t, d)
	svc, _ := newCompletenessSvc(t, d)

	got, err := svc.EmbeddingCompleteness(context.Background(), owner, true)
	r.NoError(err)
	r.InDelta(6.0/10.0, got, 0.001, "include-hidden count: 6/10 (no hidden are mapped)")
}

// TestCompleteness_NoActiveGenReturnsZero exercises the
// before-first-promotion state: a fresh DB with media but no active
// generation row. The pill should render 0% in this state — returning
// an error would force every caller to special-case "library not yet
// indexed", which is the wrong UX (the pill is the user-facing signal
// that indexing hasn't started).
func TestCompleteness_NoActiveGenReturnsZero(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	rw := d.WriteDB()
	owner := testutil.SeedOwner(t, rw, "test-hub", "alice")
	// Seed media so the only thing missing is the active generation.
	for i := range 5 {
		_ = testutil.SeedPhoto(t, rw, owner, "p"+strconv.Itoa(i))
	}
	svc, _ := newCompletenessSvc(t, d)

	got, err := svc.EmbeddingCompleteness(context.Background(), owner, false)
	r.NoError(err)
	r.InDelta(0.0, got, 0.0, "no active generation must yield 0, not an error")
}
