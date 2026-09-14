package httpapi_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/ai"
	"go.kenn.io/fotobank/internal/ai/embedding"
	"go.kenn.io/fotobank/internal/auth/hidden"
	"go.kenn.io/fotobank/internal/httpapi"
	"go.kenn.io/fotobank/internal/identity"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/search"
	"go.kenn.io/fotobank/internal/search/hybrid"
	"go.kenn.io/fotobank/internal/search/index"
	searchsvc "go.kenn.io/fotobank/internal/service/search"
	"go.kenn.io/fotobank/internal/testutil"
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

// searchFakeSettingsOn is the AI-Inspection-enabled twin used by the
// explain=true positive control. The split keeps the default-off
// posture intact for every other test.
type searchFakeSettingsOn struct{}

func (searchFakeSettingsOn) AIInspectionEnabled(_ context.Context, _ owners.Principal) (bool, error) {
	return true, nil
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
	// rw is exposed so the autocomplete smoke test can seed tag rows
	// against the same DB the service reads from. Search tests do not
	// need it; nil fields are fine.
	rw *sql.DB
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
	return newSearchAPIFixtureWithSettings(t, false, opts)
}

// newSearchAPIFixtureWithSettings builds the fixture with an
// optionally-enabled AI Inspection setting. inspectionOn=true is the
// happy path for the explain=true test that needs the service to NOT
// silently downgrade the request's diagnostics flag.
func newSearchAPIFixtureWithSettings(t *testing.T, inspectionOn bool, opts *searchFixtureOpts) searchAPIFixture {
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
	eng := hybrid.NewEngine(be, func(context.Context) embedding.ClientIface { return tc }, gens, cfg)
	checker := &searchFakeChecker{valid: false}

	var settings searchsvc.UserSettingsRepo = searchFakeSettings{}
	if inspectionOn {
		settings = searchFakeSettingsOn{}
	}
	svc := searchsvc.New(eng, settings, searchFakeTags{}, checker, gens, ro)

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
		rw:      rw,
	}
}

// searchBodyDTO mirrors the wire shape so tests can decode JSON without
// reaching into the unexported types in search.go. Field names mirror
// the JSON tags on SearchBody.
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

// TestRoute_Search_ExplainFalseHidesScoreComponents pins the
// post-fix gate: when explain=false (the default, and what the
// service hands back when AI Inspection is off), the response must
// omit score_components even if the backend populated them on the
// hit. The pre-fix DTO converter always emitted score_components
// so a diagnostics-mode payload leaked into ordinary responses.
func TestRoute_Search_ExplainFalseHidesScoreComponents(t *testing.T) {
	r := require.New(t)
	now := time.Now().UTC().Truncate(time.Second)
	rrf := 0.5
	bm25 := -1.5
	rankBM25 := 1
	fx := newSearchAPIFixtureWith(t, &searchFixtureOpts{
		hits: []index.Hit{{
			MediaID:    "m1",
			MediaType:  "photo",
			ImportedAt: now,
			Score:      rrf,
			ScoreComponents: &index.ScoreComponents{
				RRF:      &rrf,
				BM25:     &bm25,
				RankBM25: &rankBM25,
			},
		}},
	})

	q := url.Values{}
	q.Set("q", "puppy")
	q.Set("sort", "relevance")
	// explain not set → defaults to false.

	resp, body := doGetSearch(t, fx, q)
	r.Equal(http.StatusOK, resp.StatusCode)
	r.NotNil(body)
	r.Len(body.Results, 1)
	r.Nil(body.Results[0].ScoreComponents,
		"explain=false must omit score_components even if backend populated them")
}

// TestRoute_Search_ExplainTrueIncludesScoreComponents is the
// positive control: with AI Inspection enabled and explain=true
// the response carries score_components. Different fixture from
// the default — this one wires a settings repo that returns true.
func TestRoute_Search_ExplainTrueIncludesScoreComponents(t *testing.T) {
	r := require.New(t)
	now := time.Now().UTC().Truncate(time.Second)
	rrf := 0.5
	bm25 := -1.5
	fx := newSearchAPIFixtureWithSettings(t, true, &searchFixtureOpts{
		hits: []index.Hit{{
			MediaID:    "m1",
			MediaType:  "photo",
			ImportedAt: now,
			Score:      rrf,
			ScoreComponents: &index.ScoreComponents{
				RRF:  &rrf,
				BM25: &bm25,
			},
		}},
	})

	q := url.Values{}
	q.Set("q", "puppy")
	q.Set("sort", "relevance")
	q.Set("explain", "true")

	resp, body := doGetSearch(t, fx, q)
	r.Equal(http.StatusOK, resp.StatusCode)
	r.NotNil(body)
	r.Len(body.Results, 1)
	r.NotNil(body.Results[0].ScoreComponents,
		"explain=true with inspection enabled must emit score_components")
	r.InDelta(rrf, body.Results[0].ScoreComponents.RRF, 1e-9)
}

// TestRoute_Search_ExplainTrueWithInspectionOffOmitsScoreComponents
// pins the second half of the explain gate: a request with explain=true
// against a fixture where AI Inspection is OFF must STILL omit
// score_components. The service downgrades the request's diagnostics
// flag before passing it to the engine, and the route's DTO converter
// keys on the resolved post-service flag — not the raw query param.
// Without this gate, a curious user could flip explain=true on the URL
// and force the diagnostics payload through even when their account
// hasn't opted into it.
func TestRoute_Search_ExplainTrueWithInspectionOffOmitsScoreComponents(t *testing.T) {
	r := require.New(t)
	now := time.Now().UTC().Truncate(time.Second)
	rrf := 0.5
	bm25 := -1.5
	rankBM25 := 1
	// Default settings: AI Inspection OFF (the same fixture as
	// TestRoute_Search_ExplainFalseHidesScoreComponents).
	fx := newSearchAPIFixtureWith(t, &searchFixtureOpts{
		hits: []index.Hit{{
			MediaID:    "m1",
			MediaType:  "photo",
			ImportedAt: now,
			Score:      rrf,
			ScoreComponents: &index.ScoreComponents{
				RRF:      &rrf,
				BM25:     &bm25,
				RankBM25: &rankBM25,
			},
		}},
	})

	q := url.Values{}
	q.Set("q", "puppy")
	q.Set("sort", "relevance")
	q.Set("explain", "true") // user-supplied — must be downgraded by the service.

	resp, body := doGetSearch(t, fx, q)
	r.Equal(http.StatusOK, resp.StatusCode)
	r.NotNil(body)
	r.Len(body.Results, 1)
	r.Nil(body.Results[0].ScoreComponents,
		"explain=true with inspection OFF must omit score_components — settings outrank the query param")
}

// TestRoute_Search_LimitTooLargeReturns400 pins the page-size cap:
// limit=10000 (well above the 200 max) must surface as a 4xx error
// from huma's `maximum:` validation (the framework emits 422
// Unprocessable Entity for query-binding violations). The cap
// protects the server from a client that asks for a huge page that
// would consume server memory and bandwidth.
func TestRoute_Search_LimitTooLargeReturns400(t *testing.T) {
	r := require.New(t)
	fx := newSearchAPIFixture(t)

	q := url.Values{}
	q.Set("q", "puppy")
	q.Set("limit", "10000")

	resp, _ := doGetSearch(t, fx, q)
	// huma surfaces query-binding validation errors as 422; the cap
	// being enforced at all is what matters, not the exact 4xx code.
	r.GreaterOrEqual(resp.StatusCode, 400)
	r.Less(resp.StatusCode, 500,
		"limit above the cap must surface as a 4xx, not 200/5xx")
}

// Cursor continuation must advance through real SQL results at the HTTP boundary.
func TestRoute_Search_CursorRoundTrip(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	owner := testutil.SeedOwner(t, d.WriteDB(), "local", "alice")
	var ids []string
	for i := range 3 {
		id := testutil.SeedPhoto(t, d.WriteDB(), owner, fmt.Sprintf("puppy-%d", i))
		_, err := d.WriteDB().ExecContext(t.Context(), `INSERT INTO media_fts(media_id, filename) VALUES (?, 'puppy.jpg')`, id)
		r.NoError(err)
		ids = append(ids, id)
	}
	gens := embedding.NewGenerations(d.WriteDB(), d.ReadDB())
	cfg := search.Config{}
	cfg.ApplyDefaults()
	eng := hybrid.NewEngine(index.NewSQLiteVecBackend(d.ReadDB(), embedding.Row{}), nil, gens, cfg)
	svc := searchsvc.New(eng, searchFakeSettings{}, searchFakeTags{}, &searchFakeChecker{}, gens, d.ReadDB())
	h, err := httpapi.New(httpapi.Deps{IdentityProvider: identity.NewStub(owner, "Alice"), Search: svc})
	r.NoError(err)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	fx := searchAPIFixture{srv: srv, owner: owner}

	q := url.Values{"q": {"puppy"}, "sort": {"relevance"}, "limit": {"2"}}
	resp1, body1 := doGetSearch(t, fx, q)
	r.Equal(http.StatusOK, resp1.StatusCode)
	r.NotNil(body1)
	r.Len(body1.Results, 2)
	r.True(body1.HasMore)
	r.NotNil(body1.NextCursor)
	r.NotEmpty(*body1.NextCursor)

	q.Set("cursor", *body1.NextCursor)
	resp2, body2 := doGetSearch(t, fx, q)
	r.Equal(http.StatusOK, resp2.StatusCode)
	r.NotNil(body2)
	r.Len(body2.Results, 1)
	r.False(body2.HasMore)
	r.Nil(body2.NextCursor)
	got := []string{body1.Results[0].MediaID, body1.Results[1].MediaID, body2.Results[0].MediaID}
	r.ElementsMatch(ids, got)

	q.Set("cursor", "this-is-not-a-valid-cursor-blob")
	resp3, _ := doGetSearch(t, fx, q)
	defer resp3.Body.Close()
	r.Equal(http.StatusBadRequest, resp3.StatusCode)
}

// autocompleteTagsBodyDTO mirrors the wire shape of the
// /autocomplete/tags response so tests can decode JSON without
// reaching into the unexported types in search.go. Field names mirror
// the JSON tags on autocompleteTagsBody.
type autocompleteTagsBodyDTO struct {
	Tags []struct {
		Key   string `json:"key"`
		Label string `json:"label"`
		Count int    `json:"count"`
	} `json:"tags"`
}

// autocompleteLocationsBodyDTO mirrors the wire shape of the
// /autocomplete/locations response.
type autocompleteLocationsBodyDTO struct {
	Locations []struct {
		Label string `json:"label"`
		Count int    `json:"count"`
	} `json:"locations"`
}

// seedAutocompleteTag inserts an active ai_results + media_tags row
// against the fixture's DB. The fixture's owner is pinned so the
// route's owner-scoped query surfaces the row for the test caller.
func seedAutocompleteTag(t *testing.T, rw *sql.DB, mediaID, tagKey, tagLabel string) {
	t.Helper()
	resultID := uuid.NewString()
	ctx := context.Background()
	_, err := rw.ExecContext(ctx,
		`INSERT INTO ai_results(id, media_id, task, model_id, prompt_version, prompt_hash,
		 input_profile, status, generated_at) VALUES (?,?, 'tag', ?, ?, ?, ?, 'active', ?)`,
		resultID, mediaID, "test-model", "tag-v1", "test-hash", "test-profile", time.Now().UTC())
	require.NoError(t, err)
	_, err = rw.ExecContext(ctx,
		`INSERT INTO media_tags(result_id, tag_key, tag_label, rank) VALUES (?,?,?,?)`,
		resultID, tagKey, tagLabel, 1)
	require.NoError(t, err)
}

// searchCapturingFakeBackend records the index.SearchInput passed to
// each engine method so the route test can assert that the sidebar
// facet wire params reach the engine's filter args. Mirrors
// searchFakeBackend's call counters; the additional capture lets the
// SF-18 route test pin the `,explode` query binding.
type searchCapturingFakeBackend struct {
	hits        []index.Hit
	lastFilter  string
	lastArgs    []any
	fusedCalls  atomic.Int32
	bm25Calls   atomic.Int32
	filterCalls atomic.Int32
}

func (f *searchCapturingFakeBackend) capture(in index.SearchInput) {
	f.lastFilter = in.Filter.SQL
	f.lastArgs = append([]any(nil), in.Filter.Args...)
}

func (f *searchCapturingFakeBackend) FusedSearch(_ context.Context, in index.SearchInput) ([]index.Hit, error) {
	f.fusedCalls.Add(1)
	f.capture(in)
	return f.hits, nil
}

func (f *searchCapturingFakeBackend) BM25Only(_ context.Context, in index.SearchInput) ([]index.Hit, error) {
	f.bm25Calls.Add(1)
	f.capture(in)
	return f.hits, nil
}

func (f *searchCapturingFakeBackend) FilterOnly(_ context.Context, in index.SearchInput) ([]index.Hit, error) {
	f.filterCalls.Add(1)
	f.capture(in)
	return f.hits, nil
}

// TestRoute_Search_BindsSidebarFacetExplodeParams pins the SF-18 wire
// contract: ?camera=A&camera=B must bind to a 2-element slice (the
// `,explode` modifier on the SearchInput field is load-bearing —
// without it huma comma-splits a single value and the second
// `?camera=B` is silently dropped). lens, facet_tag mirror the same
// rule. has_gps is a literal "true"/"false". The test inspects the
// engine's FilterArgs directly so a regression where the wire param
// reaches the route but never makes it to hybrid.Input lights up here.
func TestRoute_Search_BindsSidebarFacetExplodeParams(t *testing.T) {
	r := require.New(t)
	t.Helper()

	// Build a fixture identical to newSearchAPIFixture but with the
	// capturing backend so we can read FilterArgs after the call.
	d := testutil.OpenTestDB(t)
	rw, ro := d.WriteDB(), d.ReadDB()
	owner := testutil.SeedOwner(t, rw, "local", "alice")

	gens := embedding.NewGenerations(rw, ro)
	row, err := gens.FindOrCreateBuilding(context.Background(),
		ai.Fingerprint{ModelID: "fake-model", InputProfile: "fake-profile"}, 64)
	r.NoError(err)
	r.NoError(gens.Promote(context.Background(), row.ID))

	be := &searchCapturingFakeBackend{}
	tc := &searchFakeText{vec: make([]float32, 64)}
	cfg := search.Config{}
	cfg.ApplyDefaults()
	eng := hybrid.NewEngine(be, func(context.Context) embedding.ClientIface { return tc }, gens, cfg)
	checker := &searchFakeChecker{valid: false}
	svc := searchsvc.New(eng, searchFakeSettings{}, searchFakeTags{}, checker, gens, ro)

	idp := identity.NewStub(owner, "Alice")
	h, err := httpapi.New(httpapi.Deps{
		IdentityProvider: idp,
		Search:           svc,
	})
	r.NoError(err)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	q := url.Values{}
	q.Add("camera", "Sony A7R IV")
	q.Add("camera", "Canon EOS R5")
	q.Add("lens", "FE 24-70mm F2.8 GM")
	q.Add("facet_tag", "dog")
	q.Add("facet_tag", "beach")
	q.Set("has_gps", "true")
	// q is empty so the engine takes the FilterOnly path — that's
	// what the assertions below read from.

	resp, err := srv.Client().Get(srv.URL + "/api/v1/search?" + q.Encode())
	r.NoError(err)
	r.Equal(http.StatusOK, resp.StatusCode)
	defer func() { _ = resp.Body.Close() }()

	// FilterArgs ordering is documented at hybrid.Resolve: hub, userID,
	// then optional date_after / date_before / TagKeys / location /
	// media_type / Cameras / Lenses / AnyTagKeys. With only the
	// sidebar facets supplied (no date / typed-tag / location /
	// media_type), the slice tail is exactly the two cameras, then the
	// lens, then the two facet tags. has_gps contributes no arg.
	r.Equal(int32(1), be.filterCalls.Load(), "empty Q should route to FilterOnly")
	args := be.lastArgs
	r.GreaterOrEqual(len(args), 7, "expected at least owner.Hub + UserID + 2 cameras + 1 lens + 2 facet tags")
	// The owner pair is first (Hub, UserID); then the sidebar facets in
	// declaration order. We assert by membership rather than positional
	// equality to stay loose against future arg-order tweaks while still
	// pinning that every value reached the engine.
	flat := make(map[string]bool)
	for _, v := range args {
		if s, ok := v.(string); ok {
			flat[s] = true
		}
	}
	r.True(flat["Sony A7R IV"], "camera=A must reach engine FilterArgs")
	r.True(flat["Canon EOS R5"], "camera=B must reach engine FilterArgs (regression for ,explode binding)")
	r.True(flat["FE 24-70mm F2.8 GM"], "lens must reach engine FilterArgs")
	r.True(flat["dog"], "facet_tag=dog must reach engine FilterArgs")
	r.True(flat["beach"], "facet_tag=beach must reach engine FilterArgs")
	r.Contains(be.lastFilter, "latitude IS NOT NULL", "has_gps=true must add the GPS-presence predicate")
}

// TestRoute_SearchAutocomplete is the HTTP-level smoke test pinning
// the contract for both autocomplete endpoints in one go: tags
// surface on the /tags route and locations surface on the /locations
// route, both 200 + non-empty JSON. End-to-end coverage of identity,
// query binding, service call, and DTO assembly. The service-level
// tests cover the unlock-claim, escape, and ownership invariants;
// this test pins that the wiring is intact.
func TestRoute_SearchAutocomplete(t *testing.T) {
	r := require.New(t)
	fx := newSearchAPIFixture(t)

	// Seed one tag and one location for the fixture owner. The
	// fixture's SeedOwner runs against rw, and the service reads from
	// the same DB's ro pool.
	mid1 := testutil.SeedPhoto(t, fx.rw, fx.owner, "p1")
	mid2 := testutil.SeedPhoto(t, fx.rw, fx.owner, "p2")
	seedAutocompleteTag(t, fx.rw, mid1, "dog", "Dog")
	seedAutocompleteTag(t, fx.rw, mid2, "doggo", "Doggo")
	_, err := fx.rw.ExecContext(context.Background(),
		`UPDATE assets SET location_label = ? WHERE id = ?`, "Paris, France", mid1)
	r.NoError(err)

	// Tags surface.
	q := url.Values{}
	q.Set("prefix", "dog")
	resp, err := fx.srv.Client().Get(fx.srv.URL + "/api/v1/search/autocomplete/tags?" + q.Encode())
	r.NoError(err)
	defer func() { _ = resp.Body.Close() }()
	r.Equal(http.StatusOK, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	r.NoError(err)
	var tagsBody autocompleteTagsBodyDTO
	r.NoError(json.Unmarshal(body, &tagsBody))
	r.Len(tagsBody.Tags, 2)

	// Locations surface.
	q2 := url.Values{}
	q2.Set("substring", "Paris")
	resp2, err := fx.srv.Client().Get(fx.srv.URL + "/api/v1/search/autocomplete/locations?" + q2.Encode())
	r.NoError(err)
	defer func() { _ = resp2.Body.Close() }()
	r.Equal(http.StatusOK, resp2.StatusCode)
	body2, err := io.ReadAll(resp2.Body)
	r.NoError(err)
	var locsBody autocompleteLocationsBodyDTO
	r.NoError(json.Unmarshal(body2, &locsBody))
	r.Len(locsBody.Locations, 1)
	r.Equal("Paris, France", locsBody.Locations[0].Label)
	r.Equal(1, locsBody.Locations[0].Count)
}
