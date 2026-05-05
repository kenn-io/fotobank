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
	"github.com/wesm/fotobank/internal/auth/hidden"
	"github.com/wesm/fotobank/internal/db"
	"github.com/wesm/fotobank/internal/errs"
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

// completenessFakeChecker accepts every claim. The completeness
// tests pass a nil claim for includeHidden=false (the gate is
// inactive in that branch) and a non-nil claim for
// includeHidden=true; the checker validates whatever it's given so
// the happy path exercises the full validation chain.
type completenessFakeChecker struct{}

func (completenessFakeChecker) Valid(_ *hidden.UnlockClaim, _ owners.Principal) bool {
	return true
}

// newCompletenessSvc bundles a Service wired against the test DB. The
// auth-side dependencies (settings/tags) are nil because no completeness
// path exercises them; the engine is also nil for the same reason. The
// hidden checker accepts every claim so happy-path tests can exercise
// the validation chain. gens and ro are consulted directly by the
// completeness queries.
func newCompletenessSvc(t *testing.T, d *db.DB) (*searchsvc.Service, *embedding.Generations) {
	t.Helper()
	gens := embedding.NewGenerations(d.WriteDB(), d.ReadDB())
	svc := searchsvc.New(nil, nil, nil, completenessFakeChecker{}, gens, d.ReadDB())
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

// insertMappingRaw writes a media_embedding_ids row directly AND bumps
// the cached embedding_generations.embedded_count, mirroring what the
// production worker does (insert mapping → IncEmbeddedCount(+1)).
// EmbeddingCompleteness now trusts the cached counter for the
// numerator (see completeness.go's "Read-time vs assertive" doc), so
// a raw mapping insert without the matching counter bump would yield
// 0 from the cached read even though the row exists.
//
// vecID is supplied positionally because UNIQUE (generation_id, vec_id)
// forbids reuse within a generation.
func insertMappingRaw(t *testing.T, rw *sql.DB, generationID int64, mediaID string, vecID int) {
	t.Helper()
	ctx := context.Background()
	_, err := rw.ExecContext(ctx,
		`INSERT INTO media_embedding_ids(generation_id, media_id, vec_id) VALUES (?,?,?)`,
		generationID, mediaID, vecID)
	require.NoError(t, err)
	_, err = rw.ExecContext(ctx,
		`UPDATE embedding_generations SET embedded_count = embedded_count + 1 WHERE id = ?`,
		generationID)
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

	// includeHidden=false: claim is irrelevant — the gate is
	// inactive — so pass nil.
	got, err := svc.EmbeddingCompleteness(context.Background(), owner, false, nil)
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

	claim := hidden.UnlockClaim{Principal: owner}
	got, err := svc.EmbeddingCompleteness(context.Background(), owner, true, &claim)
	r.NoError(err)
	r.InDelta(6.0/10.0, got, 0.001, "include-hidden count: 6/10 (no hidden are mapped)")
}

// TestCompleteness_IncludeHiddenWithoutClaimDenied pins the new
// fail-closed gate: includeHidden=true with a nil claim must surface
// errs.ErrPermissionDenied. Even if Search has already validated a
// claim earlier on the same request, EmbeddingCompleteness validates
// independently — defense-in-depth against a misuse that exercises
// only the completeness path.
func TestCompleteness_IncludeHiddenWithoutClaimDenied(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	owner, _, _ := seedHiddenAwareFixture(t, d)
	svc, _ := newCompletenessSvc(t, d)

	_, err := svc.EmbeddingCompleteness(context.Background(), owner, true, nil)
	r.ErrorIs(err, errs.ErrPermissionDenied,
		"includeHidden=true with nil claim must deny")
}

// TestCompleteness_TrustsCachedCounterAfterHide pins the read-time
// tradeoff. After a previously-mapped row is hidden, the cached
// embedding_generations.embedded_count is unchanged (the hide event
// does not decrement it). With includeHidden=false, the visible-only
// eligible denominator drops by one, but the numerator does NOT — so
// the ratio appears to have INCREASED relative to a fully-assertive
// recount that would also drop the numerator.
//
// This drift is the tradeoff documented in completeness.go: the pill
// is rendering progress, not gating behavior. We accept the slight
// over-report in exchange for skipping a JOIN-heavy query on every
// search render. The activator's separate assertive embeddedCount
// (a.embeddedCount) remains the source of truth for promotion.
//
// Concretely: 7 visible + 3 hidden, 6 visible mapped → 6/7 visible
// ratio. Hide the first mapped row → 5 visible mapped + 6 visible
// eligible. An assertive recount would yield 5/6 (≈0.833); the
// cached-counter path yields min(6, 6)/6 = 1.0 because embedded_count
// stays at 6 and the clamp caps it at the eligible denominator.
func TestCompleteness_TrustsCachedCounterAfterHide(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	owner, _, mids := seedHiddenAwareFixture(t, d)
	svc, _ := newCompletenessSvc(t, d)

	// Hide the first mapped row (mids[0]). The cached counter stays
	// at 6; visible eligible drops from 7 → 6; assertive numerator
	// would drop from 6 → 5 but we don't compute that anymore.
	hideMedia(t, d.WriteDB(), mids[0])

	got, err := svc.EmbeddingCompleteness(context.Background(), owner, false, nil)
	r.NoError(err)
	r.InDelta(1.0, got, 0.001,
		"cached counter (6) clamped to eligible (6) yields 1.0; "+
			"a fully-assertive recount would yield 5/6 (≈0.833). "+
			"This drift is the documented read-time vs assertive split.")
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

	got, err := svc.EmbeddingCompleteness(context.Background(), owner, false, nil)
	r.NoError(err)
	r.InDelta(0.0, got, 0.0, "no active generation must yield 0, not an error")
}
