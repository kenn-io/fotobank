package hybrid_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/ai"
	"go.kenn.io/fotobank/internal/ai/embedding"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/search"
	"go.kenn.io/fotobank/internal/search/hybrid"
	"go.kenn.io/fotobank/internal/search/index"
	"go.kenn.io/fotobank/internal/testutil"
)

// fakeBackend is a minimal index.Backend that records which method
// the engine called and returns canned hits. Each method tracks its
// own call count so a single test can confirm both that the expected
// method fired and that the unexpected ones didn't. The hits slice
// is shared across all three methods for simplicity — no test needs
// per-mode hit shapes.
type fakeBackend struct {
	hits         []index.Hit
	fusedCalls   atomic.Int32
	bm25Calls    atomic.Int32
	filterCalls  atomic.Int32
	lastFusedIn  index.SearchInput
	lastBM25In   index.SearchInput
	lastFilterIn index.SearchInput
	fusedErr     error
	bm25Err      error
	filterErr    error
}

func (f *fakeBackend) FusedSearch(_ context.Context, in index.SearchInput) ([]index.Hit, error) {
	f.fusedCalls.Add(1)
	f.lastFusedIn = in
	if f.fusedErr != nil {
		return nil, f.fusedErr
	}
	return f.hits, nil
}

func (f *fakeBackend) BM25Only(_ context.Context, in index.SearchInput) ([]index.Hit, error) {
	f.bm25Calls.Add(1)
	f.lastBM25In = in
	if f.bm25Err != nil {
		return nil, f.bm25Err
	}
	return f.hits, nil
}

func (f *fakeBackend) FilterOnly(_ context.Context, in index.SearchInput) ([]index.Hit, error) {
	f.filterCalls.Add(1)
	f.lastFilterIn = in
	if f.filterErr != nil {
		return nil, f.filterErr
	}
	return f.hits, nil
}

// fakeTextClient is the engine-side analog of fakeEmbedClient in the
// embedding worker tests. It satisfies embedding.ClientIface (via
// EmbedImages) plus the engine's local textEmbedder extension (via
// EmbedTexts) so the engine's type assertion succeeds and the test
// can drive the query-embedding path.
//
// embedTextsErr forces EmbedTexts to return an error so the
// query_embedding_failed degradation branch is exercisable.
// embedCalls counts how many times EmbedTexts fired so a test can
// confirm the engine skipped the call when no active generation is
// resolved.
type fakeTextClient struct {
	vec           []float32
	embedTextsErr error
	embedCalls    atomic.Int32
}

func (f *fakeTextClient) EmbedImages(_ context.Context, _ string, _ int, _ [][]byte) ([][]float32, error) {
	// Engine never calls this; return an obvious failure so a
	// regression that fires it shows up as a test failure rather than
	// an undetected dead path.
	return nil, errors.New("EmbedImages should not be called by the engine")
}

func (f *fakeTextClient) EmbedTexts(_ context.Context, _ string, _ int, texts []string) ([][]float32, error) {
	f.embedCalls.Add(1)
	if f.embedTextsErr != nil {
		return nil, f.embedTextsErr
	}
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = f.vec
	}
	return out, nil
}

// imageOnlyClient satisfies embedding.ClientIface but not the engine's
// textEmbedder extension; used to exercise the
// "client doesn't expose EmbedTexts" degradation.
type imageOnlyClient struct{}

func (imageOnlyClient) EmbedImages(_ context.Context, _ string, _ int, _ [][]byte) ([][]float32, error) {
	return nil, errors.New("not used")
}

// engineCfg returns a Config with non-zero KPerSignal / RRFK so the
// engine forwards realistic values to the backend. Picked from the
// config defaults.
func engineCfg() search.Config {
	return search.Config{KPerSignal: 200, RRFK: 60}
}

// seedActiveGeneration inserts and promotes a single generation row
// so the engine's FindActive call returns a non-nil row. The vec
// table itself is created by FindOrCreateBuilding; the table content
// is irrelevant to engine routing tests because the fakeBackend
// never executes SQL against it.
func seedActiveGeneration(t *testing.T, gens *embedding.Generations) *embedding.Row {
	t.Helper()
	ctx := context.Background()
	row, err := gens.FindOrCreateBuilding(ctx, ai.Fingerprint{
		ModelID:      "test-model",
		InputProfile: "test-profile",
	}, 768)
	require.NoError(t, err)
	require.NoError(t, gens.Promote(ctx, row.ID))
	got, err := gens.GetByID(ctx, row.ID)
	require.NoError(t, err)
	return got
}

// engineTestOwner is the principal threaded through engine tests. The
// values are arbitrary — engine tests don't assert on the bind-arg
// content; the filter resolver tests already pin that contract.
var engineTestOwner = owners.Principal{Hub: "test-hub", UserID: "test-user"}

// TestEngine_FilterOnlyForEmptyQ — Q="" routes to FilterOnly and
// never touches FusedSearch / BM25Only / the embed client. The
// effective sort coerces to "newest" because relevance has no
// meaning without a query.
func TestEngine_FilterOnlyForEmptyQ(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	gens := embedding.NewGenerations(d.WriteDB(), d.ReadDB())
	_ = seedActiveGeneration(t, gens)

	be := &fakeBackend{hits: []index.Hit{{MediaID: "m1"}}}
	tc := &fakeTextClient{vec: make([]float32, 768)}
	eng := hybrid.NewEngine(be, tc, gens, engineCfg())

	resp, err := eng.Search(context.Background(), hybrid.Request{
		Owner: engineTestOwner,
		Query: "",
		Sort:  "relevance",
		Limit: 50,
	})
	r.NoError(err)

	r.Equal(int32(1), be.filterCalls.Load(), "FilterOnly should have fired")
	r.Equal(int32(0), be.fusedCalls.Load(), "FusedSearch should not have fired")
	r.Equal(int32(0), be.bm25Calls.Load(), "BM25Only should not have fired")
	r.Equal(int32(0), tc.embedCalls.Load(), "EmbedTexts should not have fired")
	r.Equal("newest", resp.EffectiveSort)
	r.False(resp.SemanticUnavailable, "empty Q does not flip semantic_unavailable")
	r.Empty(resp.SemanticUnavailableReason)
	r.Len(resp.Hits, 1)
}

// TestEngine_RelevanceWithoutQCoercedToNewest — confirms the empty-Q +
// Sort=relevance combination resolves to effective sort "newest" and
// does not flip semantic_unavailable. Same routing as
// TestEngine_FilterOnlyForEmptyQ; this test pins the EffectiveSort
// value so a regression that flips the coercion direction is caught.
func TestEngine_RelevanceWithoutQCoercedToNewest(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	gens := embedding.NewGenerations(d.WriteDB(), d.ReadDB())
	_ = seedActiveGeneration(t, gens)

	be := &fakeBackend{hits: nil}
	tc := &fakeTextClient{vec: make([]float32, 768)}
	eng := hybrid.NewEngine(be, tc, gens, engineCfg())

	resp, err := eng.Search(context.Background(), hybrid.Request{
		Owner: engineTestOwner,
		Query: "",
		Sort:  "relevance",
		Limit: 25,
	})
	r.NoError(err)
	r.Equal("newest", resp.EffectiveSort)
	r.False(resp.SemanticUnavailable)
	r.Equal(int32(1), be.filterCalls.Load())
}

// TestEngine_BM25OnlyWhenNoActiveGen — non-empty Q with no active
// generation degrades to BM25Only with reason
// "no_active_generation". The fake text client is supplied to prove
// the engine does not call EmbedTexts when the generation is missing.
func TestEngine_BM25OnlyWhenNoActiveGen(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	// No generation seeded → FindActive returns (nil, nil).
	gens := embedding.NewGenerations(d.WriteDB(), d.ReadDB())

	be := &fakeBackend{hits: []index.Hit{{MediaID: "m1"}}}
	tc := &fakeTextClient{vec: make([]float32, 768)}
	eng := hybrid.NewEngine(be, tc, gens, engineCfg())

	resp, err := eng.Search(context.Background(), hybrid.Request{
		Owner: engineTestOwner,
		Query: "puppy",
		Sort:  "relevance",
		Limit: 50,
	})
	r.NoError(err)

	r.Equal(int32(1), be.bm25Calls.Load(), "BM25Only should have fired")
	r.Equal(int32(0), be.fusedCalls.Load())
	r.Equal(int32(0), be.filterCalls.Load())
	r.Equal(int32(0), tc.embedCalls.Load(), "EmbedTexts should not fire when no active gen")
	r.True(resp.SemanticUnavailable)
	r.Equal("no_active_generation", resp.SemanticUnavailableReason)
	r.Equal("relevance", resp.EffectiveSort)
}

// TestEngine_DegradesOnQueryEmbeddingFailure — active gen present,
// EmbedTexts errors → engine falls back to BM25Only with reason
// "query_embedding_failed". Same Backend method as the
// no_active_generation case but with a different reason string, so
// the UI banner can disambiguate.
func TestEngine_DegradesOnQueryEmbeddingFailure(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	gens := embedding.NewGenerations(d.WriteDB(), d.ReadDB())
	_ = seedActiveGeneration(t, gens)

	be := &fakeBackend{hits: []index.Hit{{MediaID: "m1"}}}
	tc := &fakeTextClient{embedTextsErr: errors.New("provider 5xx")}
	eng := hybrid.NewEngine(be, tc, gens, engineCfg())

	resp, err := eng.Search(context.Background(), hybrid.Request{
		Owner: engineTestOwner,
		Query: "puppy",
		Sort:  "relevance",
		Limit: 50,
	})
	r.NoError(err)

	r.Equal(int32(1), be.bm25Calls.Load())
	r.Equal(int32(0), be.fusedCalls.Load())
	r.Equal(int32(1), tc.embedCalls.Load(), "EmbedTexts should have been attempted once")
	r.True(resp.SemanticUnavailable)
	r.Equal("query_embedding_failed", resp.SemanticUnavailableReason)
}

// TestEngine_DateSortHybridCandidateSelection — non-empty Q with
// Sort=newest and an active generation still routes to FusedSearch
// (the L1 backend's per-mode SELECT applies the date sort; the over-
// fetch optimization is deferred per the plan). Verifies the hybrid
// path is chosen and the engine forwards the effective sort to the
// SearchInput.
func TestEngine_DateSortHybridCandidateSelection(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	gens := embedding.NewGenerations(d.WriteDB(), d.ReadDB())
	_ = seedActiveGeneration(t, gens)

	be := &fakeBackend{hits: []index.Hit{{MediaID: "m1"}}}
	tc := &fakeTextClient{vec: make([]float32, 768)}
	eng := hybrid.NewEngine(be, tc, gens, engineCfg())

	resp, err := eng.Search(context.Background(), hybrid.Request{
		Owner: engineTestOwner,
		Query: "puppy",
		Sort:  "newest",
		Limit: 50,
	})
	r.NoError(err)

	r.Equal(int32(1), be.fusedCalls.Load(), "FusedSearch should have fired for hybrid date sort")
	r.Equal(int32(0), be.bm25Calls.Load())
	r.Equal(int32(0), be.filterCalls.Load())
	r.Equal(int32(1), tc.embedCalls.Load())
	r.False(resp.SemanticUnavailable)
	r.Equal("newest", resp.EffectiveSort)
	r.Equal(index.SortNewest, be.lastFusedIn.Sort, "engine forwards effective sort to SearchInput")
}

// TestEngine_ImageOnlyClientDegrades — extra coverage that the
// degradation wiring catches a client missing the optional textEmbedder
// capability. Production never hits this (the *embedding.Client always
// implements EmbedTexts), but the test pins the contract for any
// future client substitution.
func TestEngine_ImageOnlyClientDegrades(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	gens := embedding.NewGenerations(d.WriteDB(), d.ReadDB())
	_ = seedActiveGeneration(t, gens)

	be := &fakeBackend{hits: []index.Hit{{MediaID: "m1"}}}
	eng := hybrid.NewEngine(be, imageOnlyClient{}, gens, engineCfg())

	resp, err := eng.Search(context.Background(), hybrid.Request{
		Owner: engineTestOwner,
		Query: "puppy",
		Sort:  "relevance",
		Limit: 50,
	})
	r.NoError(err)
	r.Equal(int32(1), be.bm25Calls.Load())
	r.True(resp.SemanticUnavailable)
	r.Equal("query_embedding_failed", resp.SemanticUnavailableReason)
}

// TestEngine_PureJunkQueryFallsThroughToFilter — every token in Q is
// stripped (the BuildMatchExpr "all tokens drop → empty" case). The
// engine recurses as FilterOnly so the request still returns the
// owner's media in date order rather than 400-ing.
func TestEngine_PureJunkQueryFallsThroughToFilter(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	gens := embedding.NewGenerations(d.WriteDB(), d.ReadDB())
	_ = seedActiveGeneration(t, gens)

	be := &fakeBackend{hits: nil}
	tc := &fakeTextClient{vec: make([]float32, 768)}
	eng := hybrid.NewEngine(be, tc, gens, engineCfg())

	resp, err := eng.Search(context.Background(), hybrid.Request{
		Owner: engineTestOwner,
		Query: "!! ?",
		Sort:  "relevance",
		Limit: 50,
	})
	r.NoError(err)
	r.Equal(int32(1), be.filterCalls.Load())
	r.Equal(int32(0), be.bm25Calls.Load())
	r.Equal(int32(0), be.fusedCalls.Load())
	r.Equal(int32(0), tc.embedCalls.Load())
	// Sort coerced from relevance → newest because there is no query
	// signal to rank by.
	r.Equal("newest", resp.EffectiveSort)
}

// TestEngine_FilterOnly_NoActiveGenDoesNotSetSemanticUnavailable
// pins the FilterOnly clear: when Q is empty (or all tokens drop)
// the engine routes to FilterOnly, which never consults the semantic
// signal. Even if no active generation has been promoted, the
// response must not flag semantic_unavailable=true — the request
// never asked for a semantic signal, so surfacing the degradation
// banner would mislead the user into thinking something is broken.
func TestEngine_FilterOnly_NoActiveGenDoesNotSetSemanticUnavailable(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	// No generation seeded → FindActive returns (nil, nil), the same
	// state that would otherwise flip semantic_unavailable=true on a
	// hybrid path.
	gens := embedding.NewGenerations(d.WriteDB(), d.ReadDB())

	be := &fakeBackend{hits: nil}
	tc := &fakeTextClient{vec: make([]float32, 768)}
	eng := hybrid.NewEngine(be, tc, gens, engineCfg())

	resp, err := eng.Search(context.Background(), hybrid.Request{
		Owner: engineTestOwner,
		Query: "",
		Sort:  "newest",
		Limit: 25,
	})
	r.NoError(err)
	r.Equal(int32(1), be.filterCalls.Load(), "FilterOnly should fire for empty Q")
	r.False(resp.SemanticUnavailable,
		"FilterOnly never consults semantic signal — flag must stay false")
	r.Empty(resp.SemanticUnavailableReason)
}

// TestEngine_FilterOnly_PureJunkNoActiveGenDoesNotSetSemanticUnavailable
// is the all-tokens-dropped twin of the empty-Q test: a query that
// collapses to no FTS5 terms after stripping must take the same
// FilterOnly clear, regardless of whether an active generation
// exists.
func TestEngine_FilterOnly_PureJunkNoActiveGenDoesNotSetSemanticUnavailable(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	gens := embedding.NewGenerations(d.WriteDB(), d.ReadDB())

	be := &fakeBackend{hits: nil}
	tc := &fakeTextClient{vec: make([]float32, 768)}
	eng := hybrid.NewEngine(be, tc, gens, engineCfg())

	resp, err := eng.Search(context.Background(), hybrid.Request{
		Owner: engineTestOwner,
		Query: "!! ?",
		Sort:  "relevance",
		Limit: 50,
	})
	r.NoError(err)
	r.Equal(int32(1), be.filterCalls.Load())
	r.False(resp.SemanticUnavailable)
	r.Empty(resp.SemanticUnavailableReason)
}

func TestEngine_HasMoreRequiresLookahead(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	gens := embedding.NewGenerations(d.WriteDB(), d.ReadDB())
	_ = seedActiveGeneration(t, gens)

	be := &fakeBackend{hits: []index.Hit{
		{MediaID: "m1", Score: 0.5},
		{MediaID: "m2", Score: 0.4},
		{MediaID: "m3", Score: 0.3},
	}}
	tc := &fakeTextClient{vec: make([]float32, 768)}
	eng := hybrid.NewEngine(be, tc, gens, engineCfg())

	resp, err := eng.Search(context.Background(), hybrid.Request{
		Owner: engineTestOwner,
		Query: "puppy",
		Sort:  "relevance",
		Limit: 2,
	})
	r.NoError(err)
	r.True(resp.HasMore)
	r.NotEmpty(resp.NextCursor)

	c, err := hybrid.DecodeCursor(resp.NextCursor)
	r.NoError(err)
	r.Equal(2, c.Offset)
	r.Len(resp.Hits, 2)
	r.NotEmpty(c.ReqHash)
}

// TestEngine_FusedSearchUsesActiveGenerationFromRequest pins the
// per-request generation override. The engine resolves the active
// generation via FindActive each request and stamps it onto
// SearchInput.Gen — that's the field the production-wired backend
// consults (its construction-time gen is the zero-value Row). The
// fakeBackend captures the SearchInput so this test can assert
// SearchInput.Gen is the freshly-resolved row.
//
// Without the per-request stamp, FusedSearch in production would
// fail with "FusedSearch requires an active embedding generation"
// because the backend was constructed with embedding.Row{}. That
// pre-fix posture isn't observable through the engine's fakeBackend
// (no SQL is executed), so this test pins the contract at the
// SearchInput level.
func TestEngine_FusedSearchUsesActiveGenerationFromRequest(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	gens := embedding.NewGenerations(d.WriteDB(), d.ReadDB())
	gen := seedActiveGeneration(t, gens)

	be := &fakeBackend{hits: []index.Hit{{MediaID: "m1"}}}
	tc := &fakeTextClient{vec: make([]float32, 768)}
	eng := hybrid.NewEngine(be, tc, gens, engineCfg())

	resp, err := eng.Search(context.Background(), hybrid.Request{
		Owner: engineTestOwner,
		Query: "puppy",
		Sort:  "relevance",
		Limit: 50,
	})
	r.NoError(err)
	r.Equal(int32(1), be.fusedCalls.Load(), "FusedSearch should fire on hybrid path")
	r.False(resp.SemanticUnavailable)

	// The engine must populate SearchInput.Gen with the active row so
	// the production SQLiteVecBackend (constructed with a zero-value
	// gen) can route queries to the per-generation vec0 table.
	r.NotNil(be.lastFusedIn.Gen,
		"engine must populate SearchInput.Gen so the backend can route to the right vec0 table")
	r.Equal(gen.ID, be.lastFusedIn.Gen.ID)
	r.Equal(gen.VecTableName, be.lastFusedIn.Gen.VecTableName)
}
