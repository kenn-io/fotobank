package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/ai"
	"github.com/wesm/fotobank/internal/ai/embedding"
	"github.com/wesm/fotobank/internal/auth/hidden"
	"github.com/wesm/fotobank/internal/httpapi"
	"github.com/wesm/fotobank/internal/identity"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/search"
	"github.com/wesm/fotobank/internal/search/hybrid"
	"github.com/wesm/fotobank/internal/search/index"
	searchsvc "github.com/wesm/fotobank/internal/service/search"
	"github.com/wesm/fotobank/internal/testutil"
)

// searchFakeBackend records which engine method fired and returns
// canned hits. Mirrors the hybrid_test fakeBackend; copied locally so
// the route tests don't depend on hybrid's _test.go exports.
type searchFakeBackend struct {
	hits        []index.Hit
	fusedCalls  atomic.Int32
	bm25Calls   atomic.Int32
	filterCalls atomic.Int32
}

func (f *searchFakeBackend) FusedSearch(_ context.Context, _ index.SearchInput) ([]index.Hit, error) {
	f.fusedCalls.Add(1)
	return f.hits, nil
}

func (f *searchFakeBackend) BM25Only(_ context.Context, _ index.SearchInput) ([]index.Hit, error) {
	f.bm25Calls.Add(1)
	return f.hits, nil
}

func (f *searchFakeBackend) FilterOnly(_ context.Context, _ index.SearchInput) ([]index.Hit, error) {
	f.filterCalls.Add(1)
	return f.hits, nil
}

// searchFakeText satisfies embedding.ClientIface (via EmbedImages) and
// the engine's local textEmbedder extension (via EmbedTexts). When
// embedTextsErr is set, EmbedTexts errors so the route test exercises
// the query_embedding_failed degradation path.
type searchFakeText struct {
	vec           []float32
	embedTextsErr error
}

func (f *searchFakeText) EmbedImages(_ context.Context, _ string, _ int, _ [][]byte) ([][]float32, error) {
	return nil, errors.New("EmbedImages should not be called from search route")
}

func (f *searchFakeText) EmbedTexts(_ context.Context, _ string, _ int, texts []string) ([][]float32, error) {
	if f.embedTextsErr != nil {
		return nil, f.embedTextsErr
	}
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = f.vec
	}
	return out, nil
}

// searchFakeSettings backs UserSettingsRepo. Returns AI inspection off by
// default so explain=true is suppressed unless a test opts in.
type searchFakeSettings struct{}

func (searchFakeSettings) AIInspectionEnabled(_ context.Context, _ owners.Principal) (bool, error) {
	return false, nil
}

// searchFakeTags is a pass-through tag resolver: every label maps to
// itself. Sufficient for route tests that don't exercise label
// canonicalisation.
type searchFakeTags struct{}

func (searchFakeTags) LabelsToKeys(_ context.Context, _ owners.Principal, labels []string) ([]string, error) {
	return labels, nil
}

// searchFakeChecker rejects every claim by default — the IncludeHidden
// 403 test relies on this.
type searchFakeChecker struct {
	valid bool
}

func (f *searchFakeChecker) Valid(_ *hidden.UnlockClaim, _ owners.Principal) bool {
	return f.valid
}

// searchAPIFixture wires a server with a real engine over fake backend +
// fake text client. The fixture exposes the levers the tests flip
// (the embed client error, the hidden checker validity, the canned hits)
// without leaking the engine internals.
type searchAPIFixture struct {
	srv     *httptest.Server
	owner   owners.Principal
	backend *searchFakeBackend
	text    *searchFakeText
	checker *searchFakeChecker
}

func newSearchAPIFixture(t *testing.T) searchAPIFixture {
	return newSearchAPIFixtureWith(t, nil)
}

// searchFixtureOpts lets a test customise the fake collaborators
// without forking the constructor for each variation.
type searchFixtureOpts struct {
	hits          []index.Hit
	embedTextsErr error
}

func newSearchAPIFixtureWith(t *testing.T, opts *searchFixtureOpts) searchAPIFixture {
	t.Helper()
	d := testutil.OpenTestDB(t)
	rw, ro := d.WriteDB(), d.ReadDB()
	owner := testutil.SeedOwner(t, rw, "local", "alice")

	// Seed an active embedding generation so the engine can route
	// hybrid; tests that want the no_active_generation path won't seed.
	gens := embedding.NewGenerations(rw, ro)
	row, err := gens.FindOrCreateBuilding(context.Background(),
		ai.Fingerprint{ModelID: "fake-model", InputProfile: "fake-profile"}, 64)
	require.NoError(t, err)
	require.NoError(t, gens.Promote(context.Background(), row.ID))

	if opts == nil {
		opts = &searchFixtureOpts{}
	}
	be := &searchFakeBackend{hits: opts.hits}
	tc := &searchFakeText{vec: make([]float32, 64), embedTextsErr: opts.embedTextsErr}
	cfg := search.Config{}
	cfg.ApplyDefaults()
	eng := hybrid.NewEngine(be, tc, gens, cfg)
	checker := &searchFakeChecker{valid: false}

	svc := searchsvc.New(eng, searchFakeSettings{}, searchFakeTags{}, checker, gens, ro)

	idp := identity.NewStub(owner, "Alice")
	h, err := httpapi.New(httpapi.Deps{
		IdentityProvider: idp,
		Search:           svc,
	})
	require.NoError(t, err)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	return searchAPIFixture{
		srv:     srv,
		owner:   owner,
		backend: be,
		text:    tc,
		checker: checker,
	}
}

// searchBodyDTO mirrors the wire shape so tests can decode JSON without
// reaching into the unexported types in search.go. Field names mirror
// the JSON tags on searchBody.
type searchBodyDTO struct {
	Results                   []searchResultBodyDTO `json:"results"`
	NextCursor                *string               `json:"next_cursor"`
	HasMore                   bool                  `json:"has_more"`
	Total                     *int                  `json:"total,omitempty"`
	EffectiveSort             string                `json:"effective_sort"`
	EmbeddingCompleteness     float64               `json:"embedding_completeness"`
	SemanticUnavailable       bool                  `json:"semantic_unavailable"`
	SemanticUnavailableReason string                `json:"semantic_unavailable_reason"`
}

type searchResultBodyDTO struct {
	MediaID         string                       `json:"media_id"`
	MediaType       string                       `json:"media_type"`
	Timestamp       *time.Time                   `json:"timestamp"`
	ImportedAt      time.Time                    `json:"imported_at"`
	Width           *int                         `json:"width"`
	Height          *int                         `json:"height"`
	ThumbVersion    int                          `json:"thumb_version"`
	Score           float64                      `json:"score,omitempty"`
	ScoreComponents *searchScoreComponentsBodyDT `json:"score_components,omitempty"`
}

type searchScoreComponentsBodyDT struct {
	RRF        float64  `json:"rrf"`
	BM25       *float64 `json:"bm25"`
	Vector     *float64 `json:"vector"`
	RankBM25   *int     `json:"rank_bm25"`
	RankVector *int     `json:"rank_vector"`
}

// doGetSearch issues GET /api/v1/search with the supplied query string.
// Returns the response so the caller can assert on status; on a 200 the
// body is JSON-decoded into *searchBodyDTO.
func doGetSearch(t *testing.T, fx searchAPIFixture, q url.Values) (*http.Response, *searchBodyDTO) {
	t.Helper()
	resp, err := fx.srv.Client().Get(fx.srv.URL + "/api/v1/search?" + q.Encode())
	require.NoError(t, err)
	if resp.StatusCode != http.StatusOK {
		return resp, nil
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	var out searchBodyDTO
	require.NoError(t, json.Unmarshal(body, &out))
	return resp, &out
}

// TestRoute_Search_HappyPathHybrid — q="puppy" + sort=relevance with an
// active generation and a working embed client routes to FusedSearch
// and returns 200 with a populated results list.
func TestRoute_Search_HappyPathHybrid(t *testing.T) {
	r := require.New(t)
	now := time.Now().UTC().Truncate(time.Second)
	fx := newSearchAPIFixtureWith(t, &searchFixtureOpts{
		hits: []index.Hit{{MediaID: "m1", MediaType: "photo", ImportedAt: now, ThumbVersion: 1}},
	})

	q := url.Values{}
	q.Set("q", "puppy")
	q.Set("sort", "relevance")

	resp, body := doGetSearch(t, fx, q)
	r.Equal(http.StatusOK, resp.StatusCode)
	r.NotNil(body)
	r.Equal("relevance", body.EffectiveSort)
	r.Len(body.Results, 1)
	r.Equal("m1", body.Results[0].MediaID)
	r.Equal(int32(1), fx.backend.fusedCalls.Load(), "FusedSearch should fire on hybrid path")
	r.False(body.SemanticUnavailable)
	r.Empty(body.SemanticUnavailableReason)
}

// TestRoute_Search_EffectiveSortCoercion — q="" + sort=relevance must
// coerce effective_sort to "newest" because relevance has no signal
// without a query.
func TestRoute_Search_EffectiveSortCoercion(t *testing.T) {
	r := require.New(t)
	fx := newSearchAPIFixture(t)

	q := url.Values{}
	q.Set("sort", "relevance")
	// q is intentionally empty.

	resp, body := doGetSearch(t, fx, q)
	r.Equal(http.StatusOK, resp.StatusCode)
	r.NotNil(body)
	r.Equal("newest", body.EffectiveSort, "empty-Q + relevance must coerce to newest")
	r.Equal(int32(1), fx.backend.filterCalls.Load(), "FilterOnly should fire for empty Q")
}

// TestRoute_Search_EmbeddingFailureDegradesAndDoesNotError — when the
// embed client fails, the route must still return 200 with
// semantic_unavailable=true and reason="query_embedding_failed". The
// engine's BM25Only path takes over; the route does not surface the
// error to the client.
func TestRoute_Search_EmbeddingFailureDegradesAndDoesNotError(t *testing.T) {
	r := require.New(t)
	fx := newSearchAPIFixtureWith(t, &searchFixtureOpts{
		embedTextsErr: errors.New("provider 5xx"),
		hits:          []index.Hit{{MediaID: "m1", MediaType: "photo", ImportedAt: time.Now().UTC()}},
	})

	q := url.Values{}
	q.Set("q", "puppy")
	q.Set("sort", "relevance")

	resp, body := doGetSearch(t, fx, q)
	r.Equal(http.StatusOK, resp.StatusCode, "embed failure must degrade, not 5xx")
	r.NotNil(body)
	r.True(body.SemanticUnavailable)
	r.Equal("query_embedding_failed", body.SemanticUnavailableReason)
	r.Equal(int32(1), fx.backend.bm25Calls.Load(), "BM25Only should fire on degraded path")
	r.Equal(int32(0), fx.backend.fusedCalls.Load())
}

// TestRoute_Search_IncludeHiddenWithoutUnlockReturns403 — include_hidden=true
// without a valid unlock claim must surface as 403 via Translate(ErrPermissionDenied).
// The route does not own the gate; the service rejects, and the route
// translates the sentinel to the documented status.
func TestRoute_Search_IncludeHiddenWithoutUnlockReturns403(t *testing.T) {
	r := require.New(t)
	fx := newSearchAPIFixture(t)
	// Default checker.valid=false; with no unlock cookie the request
	// also has no claim, so the service short-circuits to ErrPermissionDenied.

	q := url.Values{}
	q.Set("q", "puppy")
	q.Set("include_hidden", "true")

	resp, _ := doGetSearch(t, fx, q)
	r.Equal(http.StatusForbidden, resp.StatusCode)
}

// TestRoute_Search_CursorRoundTrip — first page returns a non-empty
// next_cursor; reusing the same cursor with the same request shape
// returns 200 (the v1 engine validates the hash matches but doesn't
// yet skip rows). A tampered cursor (corrupted base64) must surface
// as 400 via Translate(ErrInvalidArgument).
func TestRoute_Search_CursorRoundTrip(t *testing.T) {
	r := require.New(t)
	fx := newSearchAPIFixtureWith(t, &searchFixtureOpts{
		hits: []index.Hit{
			{MediaID: "m1", ImportedAt: time.Now().UTC()},
			{MediaID: "m2", ImportedAt: time.Now().UTC()},
		},
	})

	// Page 1: limit=2 with len(hits)==2 fills the page so the engine
	// emits a cursor.
	q := url.Values{}
	q.Set("q", "puppy")
	q.Set("sort", "relevance")
	q.Set("limit", "2")

	resp1, body1 := doGetSearch(t, fx, q)
	r.Equal(http.StatusOK, resp1.StatusCode)
	r.NotNil(body1)
	r.True(body1.HasMore, "len(hits)==Limit must produce next_cursor + has_more")
	r.NotNil(body1.NextCursor)
	r.NotEmpty(*body1.NextCursor)

	// Page 2: replay the cursor with the same request shape. Hash
	// matches → engine accepts; v1 still serves the same rows because
	// the page-skip math is deferred. We only assert the round-trip
	// succeeds, not the row contents.
	q2 := url.Values{}
	q2.Set("q", "puppy")
	q2.Set("sort", "relevance")
	q2.Set("limit", "2")
	q2.Set("cursor", *body1.NextCursor)

	resp2, body2 := doGetSearch(t, fx, q2)
	r.Equal(http.StatusOK, resp2.StatusCode, "unchanged request shape + valid cursor must succeed")
	r.NotNil(body2)

	// Page 3: replay with a tampered cursor — base64 garbage that does
	// not decode. The engine surfaces ErrInvalidArgument and the route
	// maps it to 400.
	q3 := url.Values{}
	q3.Set("q", "puppy")
	q3.Set("sort", "relevance")
	q3.Set("limit", "2")
	q3.Set("cursor", "this-is-not-a-valid-cursor-blob")

	resp3, _ := doGetSearch(t, fx, q3)
	r.Equal(http.StatusBadRequest, resp3.StatusCode, "tampered cursor must surface as 400")
}
