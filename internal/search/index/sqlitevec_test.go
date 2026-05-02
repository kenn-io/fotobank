package index_test

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/ai"
	"github.com/wesm/fotobank/internal/ai/embedding"
	"github.com/wesm/fotobank/internal/db"
	"github.com/wesm/fotobank/internal/errs"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/search/index"
	"github.com/wesm/fotobank/internal/testutil"
)

// vecForText returns a deterministic 768-dim float32 vector seeded by a
// SHA-256 of text. Identical text yields identical vectors. Components
// are uniform on [-1, 1) so the L2 distance between two vectors maps
// roughly to text similarity (identical text → distance 0; different
// text → larger distance).
//
// This is just a stand-in for a real model embedding in tests; it
// satisfies the contract sqlite-vec needs (a fixed-dim float32 slice
// where vec_f32(blob) MATCH compares by distance) while staying
// reproducible across runs.
func vecForText(text string) []float32 {
	const dim = 768
	sum := sha256.Sum256([]byte(text))
	var seed = sum
	src := rand.NewChaCha8(seed)
	r := rand.New(src) //nolint:gosec // deterministic test fixture, not crypto.
	out := make([]float32, dim)
	for i := range out {
		out[i] = r.Float32()*2 - 1
	}
	return out
}

// ftsCorpus is the slice of fields the search corpus indexes; only
// caption + tag fields are exercised in these tests.
type ftsCorpus struct {
	Caption string
	Tags    string
}

// mustWriteFTSCorpus inserts a row directly into media_fts for mediaID
// with the supplied caption and tag text. Bypasses RefreshMediaFTS to
// keep the fixtures terse and decoupled from the J1 helper's full
// caption/tag-result indirection.
func mustWriteFTSCorpus(t *testing.T, d *db.DB, mediaID string, c ftsCorpus) {
	t.Helper()
	ctx := context.Background()
	// Delete any prior row so re-runs are idempotent — the FTS5 table
	// has no UNIQUE on media_id (UNINDEXED) so we manage it ourselves.
	_, err := d.WriteDB().ExecContext(ctx,
		`DELETE FROM media_fts WHERE media_id = ?`, mediaID)
	require.NoError(t, err)
	_, err = d.WriteDB().ExecContext(ctx,
		`INSERT INTO media_fts (media_id, caption_text, tag_label, filename, camera, lens, location_label)
		 VALUES (?, ?, ?, '', '', '', '')`,
		mediaID, c.Caption, c.Tags)
	require.NoError(t, err)
}

// seedSearchMedia inserts a single media row owned by p. The path and
// import_source_path values are derived from id so each row is unique
// within the (owner, path) and (owner, checksum) UNIQUE constraints.
// Returns the new id.
func seedSearchMedia(t *testing.T, d *db.DB, p owners.Principal) string {
	t.Helper()
	id := uuid.NewString()
	_, err := d.WriteDB().ExecContext(context.Background(),
		`INSERT INTO media(
			id, owner_hub, owner_user_id, media_type, mime_type, path,
			imported_at, size, checksum,
			thumb_status, thumb_version, import_source_path
		) VALUES (?,?,?, 'photo','image/jpeg', ?, ?, 0, ?, 'ready', 1, ?)`,
		id, p.Hub, p.UserID, "/photos/"+id+".jpg", time.Now().UTC(), id+"-checksum", id)
	require.NoError(t, err)
	return id
}

// mustCreateActiveGenWithVectors creates a building generation, writes
// each (mediaID, vec) mapping into the per-generation vec0 table, then
// promotes the generation to 'active'. Returns the populated row.
//
// Each test gets a fingerprint derived from t.Name() so rows from
// concurrent test runs (or repeated runs in the same process) don't
// collide on the unique fingerprint hash.
func mustCreateActiveGenWithVectors(t *testing.T, d *db.DB, dim int, m map[string][]float32) embedding.Row {
	t.Helper()
	ctx := context.Background()
	g := embedding.NewGenerations(d.WriteDB(), d.ReadDB())
	fp := ai.Fingerprint{
		ModelID:      "siglip2",
		InputProfile: "search-" + t.Name(),
	}
	row, err := g.FindOrCreateBuilding(ctx, fp, dim)
	require.NoError(t, err)

	mp := embedding.NewMapping(d.WriteDB())
	for mid, vec := range m {
		_, err := mp.WriteVector(ctx, row, mid, vec)
		require.NoError(t, err)
	}
	require.NoError(t, g.Promote(ctx, row.ID))

	got, err := g.GetByID(ctx, row.ID)
	require.NoError(t, err)
	return *got
}

// noFilter returns a FilterCTE that selects every media row owned by
// p. This is the smallest CTE that satisfies the JOIN contract used by
// Backend; the M1 task replaces it with the real per-request filter
// resolver.
func noFilter(p owners.Principal) index.FilterCTE {
	return index.FilterCTE{
		SQL: `SELECT id, timestamp, imported_at FROM media
		      WHERE owner_hub = ? AND owner_user_id = ?
		        AND hidden_at IS NULL`,
		Args: []any{p.Hub, p.UserID},
	}
}

// blobLen verifies the byte length of the embedding blob the test wrote
// matches the expected dim. Sanity check that VecToBlob and the vec0
// FLOAT[N] schema agree at the byte level.
func blobLen(t *testing.T, d *db.DB, gen embedding.Row) int {
	t.Helper()
	var blob []byte
	err := d.ReadDB().QueryRowContext(context.Background(),
		fmt.Sprintf(`SELECT embedding FROM %s LIMIT 1`, gen.VecTableName),
	).Scan(&blob)
	require.NoError(t, err)
	return len(blob)
}

func TestSQLiteVec_FusedSearch_HappyPath(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	d := testutil.OpenTestDB(t)
	owner := testutil.SeedOwner(t, d.WriteDB(), "local", "alice")

	mids := []string{
		seedSearchMedia(t, d, owner),
		seedSearchMedia(t, d, owner),
		seedSearchMedia(t, d, owner),
	}
	captions := []string{"dog beach", "cat tree", "small dog on a beach"}
	tags := []string{"dog beach", "cat tree", "small dog beach"}
	for i, mid := range mids {
		mustWriteFTSCorpus(t, d, mid, ftsCorpus{Caption: captions[i], Tags: tags[i]})
	}

	gen := mustCreateActiveGenWithVectors(t, d, 768, map[string][]float32{
		mids[0]: vecForText("dog beach"),
		mids[1]: vecForText("cat tree"),
		mids[2]: vecForText("small dog on a beach"),
	})
	// Sanity-check the vec table holds 4*768 byte vectors as written.
	r.Equal(4*768, blobLen(t, d, gen))

	b := index.NewSQLiteVecBackend(d.ReadDB(), gen)
	hits, err := b.FusedSearch(ctx, index.SearchInput{
		// FTS5 MATCH defaults to AND across tokens; the L1 backend
		// sees the already-tokenized expression. Use explicit OR so
		// every seeded media row has a chance to score and the RRF
		// fusion is observable across all three.
		Query:       "small OR dog OR beach",
		QueryVector: vecForText("small dog on a beach"),
		Owner:       owner,
		Filter:      noFilter(owner),
		KPerSignal:  200,
		RRFK:        60,
		Limit:       10,
	})
	r.NoError(err)
	r.NotEmpty(hits)
	r.Equal(mids[2], hits[0].MediaID, "best hit must be the 'small dog on a beach' row")

	// RRF score components are populated and the top hit beat the others
	// on the fused score.
	r.NotNil(hits[0].ScoreComponents)
	r.NotNil(hits[0].ScoreComponents.RRF)
	for _, h := range hits[1:] {
		r.GreaterOrEqual(*hits[0].ScoreComponents.RRF, *h.ScoreComponents.RRF)
	}
}

func TestSQLiteVec_BM25Only_RanksByBM25(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	d := testutil.OpenTestDB(t)
	owner := testutil.SeedOwner(t, d.WriteDB(), "local", "alice")

	mids := []string{
		seedSearchMedia(t, d, owner),
		seedSearchMedia(t, d, owner),
		seedSearchMedia(t, d, owner),
	}
	// mids[2]'s caption matches all three query tokens; the others share
	// only one token. BM25 must rank the multi-match hit first.
	mustWriteFTSCorpus(t, d, mids[0], ftsCorpus{Caption: "dog at the park", Tags: "dog park"})
	mustWriteFTSCorpus(t, d, mids[1], ftsCorpus{Caption: "cat on a tree", Tags: "cat tree"})
	mustWriteFTSCorpus(t, d, mids[2], ftsCorpus{Caption: "small dog on a beach", Tags: "small dog beach"})

	// Empty (zero-value) generation; BM25Only path doesn't touch vec0.
	b := index.NewSQLiteVecBackend(d.ReadDB(), embedding.Row{})
	hits, err := b.BM25Only(ctx, index.SearchInput{
		// OR semantics so every row that mentions any of the tokens
		// shows up — the assertion is that the row sharing the most
		// tokens with the query ranks first.
		Query:      "small OR dog OR beach",
		Owner:      owner,
		Filter:     noFilter(owner),
		KPerSignal: 200,
		Limit:      10,
	})
	r.NoError(err)
	r.NotEmpty(hits)
	r.Equal(mids[2], hits[0].MediaID, "BM25 ranks the row that matches every token first")

	// BM25 scores are populated; top hit's bm25 must be the smallest
	// (most relevant) — bm25() returns negative scores where lower is better.
	r.NotNil(hits[0].ScoreComponents)
	r.NotNil(hits[0].ScoreComponents.BM25)
	for _, h := range hits[1:] {
		r.LessOrEqual(*hits[0].ScoreComponents.BM25, *h.ScoreComponents.BM25)
	}

	// No vector signal feeding into BM25Only.
	r.Nil(hits[0].ScoreComponents.Vector)
	r.Nil(hits[0].ScoreComponents.RankVector)
}

func TestSQLiteVec_FilterOnly_NoQueryNoVector(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	d := testutil.OpenTestDB(t)
	owner := testutil.SeedOwner(t, d.WriteDB(), "local", "alice")

	// Three media with distinct timestamps so SortNewest is observable.
	// SeedPhoto leaves timestamp NULL, so set them explicitly here.
	mids := []string{
		seedSearchMedia(t, d, owner),
		seedSearchMedia(t, d, owner),
		seedSearchMedia(t, d, owner),
	}
	timestamps := []time.Time{
		time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC),
	}
	for i, mid := range mids {
		_, err := d.WriteDB().ExecContext(ctx,
			`UPDATE media SET timestamp = ? WHERE id = ?`, timestamps[i], mid)
		r.NoError(err)
	}

	b := index.NewSQLiteVecBackend(d.ReadDB(), embedding.Row{})
	hits, err := b.FilterOnly(ctx, index.SearchInput{
		Query:       "",
		QueryVector: nil,
		Owner:       owner,
		Filter:      noFilter(owner),
		Sort:        index.SortNewest,
		Limit:       10,
	})
	r.NoError(err)
	r.Len(hits, 3, "every owner-visible row is returned")
	// SortNewest → timestamps DESC: 2025-12, 2025-06, 2025-01.
	r.Equal(mids[2], hits[0].MediaID)
	r.Equal(mids[1], hits[1].MediaID)
	r.Equal(mids[0], hits[2].MediaID)

	// FilterOnly hits carry no FTS / ANN scores.
	for _, h := range hits {
		if h.ScoreComponents != nil {
			r.Nil(h.ScoreComponents.BM25)
			r.Nil(h.ScoreComponents.Vector)
		}
	}

	// Reverse sort: SortOldest puts 2025-01 first.
	hits, err = b.FilterOnly(ctx, index.SearchInput{
		Owner:  owner,
		Filter: noFilter(owner),
		Sort:   index.SortOldest,
		Limit:  10,
	})
	r.NoError(err)
	r.Len(hits, 3)
	r.Equal(mids[0], hits[0].MediaID)
	r.Equal(mids[2], hits[2].MediaID)
}

// TestSQLiteVec_FusedSearchFiltersANNCandidates is the regression for
// the ANN over-fetch fix. sqlite-vec's vec0 MATCH applies its k-cap
// BEFORE the filter join, so a query that filters down to a small
// slice (e.g. one owner-scoped match in a corpus where the top-k is
// dominated by another owner) can come up empty under k=KPerSignal.
// The post-fix code over-fetches at the vec layer
// (k = KPerSignal * annOverfetchFactor) so the filter has room to
// narrow.
//
// Setup:
//   - 5 owner B media whose vectors are all the query baseline
//     (distance 0; they fill the top-5 ANN slots).
//   - 1 owner A media whose vector is moderately distant from the
//     query (distance > 0; ranks 6th in the global ANN order).
//
// Owner A's BM25 corpus contains tokens that DO NOT match the query
// string, so the BM25 CTE cannot surface owner A — only the ANN
// signal can. With KPerSignal=5 and no over-fetch, ann_raw returns
// owner B's 5 vectors and the post-filter set is empty for owner A.
// With over-fetch (k=50), ann_raw includes owner A's vector and the
// filter join surfaces it.
//
// If the over-fetch is reverted, this test fails (owner A's media
// won't appear in the result).
func TestSQLiteVec_FusedSearchFiltersANNCandidates(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	d := testutil.OpenTestDB(t)
	ownerA := testutil.SeedOwner(t, d.WriteDB(), "local", "alice")
	ownerB := testutil.SeedOwner(t, d.WriteDB(), "local", "bob")

	const bCount = 5
	bMids := make([]string, bCount)
	for i := range bCount {
		mid := seedSearchMedia(t, d, ownerB)
		bMids[i] = mid
		mustWriteFTSCorpus(t, d, mid, ftsCorpus{
			Caption: "owner_b_text matches_query_text",
			Tags:    "owner_b_only",
		})
	}
	aMid := seedSearchMedia(t, d, ownerA)
	mustWriteFTSCorpus(t, d, aMid, ftsCorpus{
		Caption: "owner_a_irrelevant_text",
		Tags:    "owner_a_only",
	})

	// Insert mappings explicitly so B's vectors get earlier rowids in
	// the vec0 table — sqlite-vec breaks ties on rowid, which makes
	// the ordering deterministic across runs. B's vectors are all at
	// the query baseline (distance 0); A's vector is from a different
	// text so it lives at a non-zero distance.
	g := embedding.NewGenerations(d.WriteDB(), d.ReadDB())
	fp := ai.Fingerprint{
		ModelID:      "siglip2",
		InputProfile: "search-overfetch-" + t.Name(),
	}
	gen, err := g.FindOrCreateBuilding(ctx, fp, 768)
	r.NoError(err)
	mp := embedding.NewMapping(d.WriteDB())
	queryBaseline := vecForText("owner-b-baseline")
	for _, mid := range bMids {
		_, err := mp.WriteVector(ctx, gen, mid, queryBaseline)
		r.NoError(err)
	}
	_, err = mp.WriteVector(ctx, gen, aMid, vecForText("owner-a-distant"))
	r.NoError(err)
	r.NoError(g.Promote(ctx, gen.ID))
	got, err := g.GetByID(ctx, gen.ID)
	r.NoError(err)
	b := index.NewSQLiteVecBackend(d.ReadDB(), *got)

	// Query: BM25 string matches owner B's caption but NOT owner A's,
	// and the query vector is the owner B baseline. So owner B's 5
	// vectors are the top-5 ANN candidates — without over-fetch, the
	// filter join (owner=A) lands on an empty set and BM25 has nothing
	// to rescue owner A with either, so the result is empty.
	hits, err := b.FusedSearch(ctx, index.SearchInput{
		Query:       "matches_query_text",
		QueryVector: queryBaseline,
		Owner:       ownerA,
		Filter:      noFilter(ownerA),
		KPerSignal:  5,
		RRFK:        60,
		Limit:       10,
	})
	r.NoError(err)
	r.Len(hits, 1,
		"owner A's media must surface via the over-fetched ANN slate")
	r.Equal(aMid, hits[0].MediaID,
		"the only hit must be owner A's lone media")
	r.NotNil(hits[0].ScoreComponents)
	r.NotNil(hits[0].ScoreComponents.Vector,
		"the hit must carry a vector score — proving ANN, not BM25, surfaced it")
}

// TestSQLiteVec_FusedSearchANNCappedPostFilter pins the post-filter
// cap on the ANN CTE. ann_raw over-fetches at
// k=KPerSignal*annOverfetchFactor to absorb owner-imbalance, but
// the ann CTE itself is then capped at KPerSignal so the
// over-fetched candidates never bleed into the fusion pool.
//
// Setup: a single owner with N=20 media whose BM25 corpus does NOT
// match the query (so BM25Only would return zero hits) and whose
// vectors all sit at the query baseline (distance 0; ann_raw picks
// them all). With KPerSignal=5, the over-fetched ann_raw returns
// up to 50 candidates — but the post-filter ann CTE must cap at 5,
// and the final result set must therefore not exceed 5.
//
// If the LIMIT on ann is missing, every owner-scoped candidate
// surfaces and the result count exceeds KPerSignal — failing the
// assertion.
func TestSQLiteVec_FusedSearchANNCappedPostFilter(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	d := testutil.OpenTestDB(t)
	owner := testutil.SeedOwner(t, d.WriteDB(), "local", "alice")

	const N = 20
	mids := make([]string, N)
	for i := range mids {
		mid := seedSearchMedia(t, d, owner)
		mids[i] = mid
		// FTS corpus that does NOT match the query — so BM25 carries
		// no hits and the result set is purely ANN-driven.
		mustWriteFTSCorpus(t, d, mid, ftsCorpus{
			Caption: "irrelevant_caption",
			Tags:    "irrelevant_tag",
		})
	}

	g := embedding.NewGenerations(d.WriteDB(), d.ReadDB())
	fp := ai.Fingerprint{
		ModelID:      "siglip2",
		InputProfile: "search-anncap-" + t.Name(),
	}
	gen, err := g.FindOrCreateBuilding(ctx, fp, 768)
	r.NoError(err)
	mp := embedding.NewMapping(d.WriteDB())
	queryBaseline := vecForText("baseline")
	for _, mid := range mids {
		_, err := mp.WriteVector(ctx, gen, mid, queryBaseline)
		r.NoError(err)
	}
	r.NoError(g.Promote(ctx, gen.ID))
	got, err := g.GetByID(ctx, gen.ID)
	r.NoError(err)
	b := index.NewSQLiteVecBackend(d.ReadDB(), *got)

	const K = 5
	hits, err := b.FusedSearch(ctx, index.SearchInput{
		Query:       "tokens_that_match_no_caption",
		QueryVector: queryBaseline,
		Owner:       owner,
		Filter:      noFilter(owner),
		KPerSignal:  K,
		RRFK:        60,
		Limit:       100, // outer LIMIT > KPerSignal so the cap, not Limit, bounds the count
	})
	r.NoError(err)
	r.LessOrEqual(len(hits), K,
		"ann CTE must cap candidates at KPerSignal even when the filter is broad")
	// Sanity: at least one ANN-driven hit surfaces (otherwise the test
	// is degenerate).
	r.NotEmpty(hits)
}

// TestSQLiteVec_EmptyFilterIsRejected pins the fail-closed contract:
// every Backend call must arrive with an owner-conditioned
// Filter.SQL. The earlier "scan-all-media SELECT" fallback was a
// debugging convenience that turned into a cross-owner data-leak
// risk if any production path forwarded a SearchInput with an unset
// Filter.SQL. The post-fix backend rejects such calls with
// errs.ErrInvalidArgument.
func TestSQLiteVec_EmptyFilterIsRejected(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	d := testutil.OpenTestDB(t)
	owner := testutil.SeedOwner(t, d.WriteDB(), "local", "alice")
	mid := seedSearchMedia(t, d, owner)
	mustWriteFTSCorpus(t, d, mid, ftsCorpus{Caption: "dog beach", Tags: "dog beach"})

	gen := mustCreateActiveGenWithVectors(t, d, 768, map[string][]float32{
		mid: vecForText("dog beach"),
	})
	b := index.NewSQLiteVecBackend(d.ReadDB(), gen)

	// FilterOnly with empty Filter.SQL: must surface ErrInvalidArgument.
	_, err := b.FilterOnly(ctx, index.SearchInput{
		Owner:  owner,
		Filter: index.FilterCTE{},
		Limit:  10,
	})
	r.ErrorIs(err, errs.ErrInvalidArgument,
		"FilterOnly must reject empty Filter.SQL")

	// BM25Only with empty Filter.SQL: same reject.
	_, err = b.BM25Only(ctx, index.SearchInput{
		Query:      "dog",
		Owner:      owner,
		Filter:     index.FilterCTE{},
		KPerSignal: 10,
		Limit:      10,
	})
	r.ErrorIs(err, errs.ErrInvalidArgument,
		"BM25Only must reject empty Filter.SQL")

	// FusedSearch with empty Filter.SQL: same reject.
	_, err = b.FusedSearch(ctx, index.SearchInput{
		Query:       "dog",
		QueryVector: vecForText("dog beach"),
		Owner:       owner,
		Filter:      index.FilterCTE{},
		KPerSignal:  10,
		RRFK:        60,
		Limit:       10,
	})
	r.ErrorIs(err, errs.ErrInvalidArgument,
		"FusedSearch must reject empty Filter.SQL")
}

// TestVecToBlob_RoundTrip cross-checks that VecToBlob produces the same
// little-endian float32 byte layout the embedding package writes via
// vec_f32(?). The backend reuses VecToBlob for the query-time vector,
// so the layout must match what mapping.go writes.
func TestVecToBlob_RoundTrip(t *testing.T) {
	r := require.New(t)
	vec := []float32{1.0, -2.5, 3.25, 0.125}
	blob := embedding.VecToBlob(vec)
	r.Len(blob, 4*len(vec))
	for i, v := range vec {
		raw := binary.LittleEndian.Uint32(blob[i*4 : (i+1)*4])
		got := math.Float32frombits(raw)
		r.InDelta(v, got, 1e-7)
	}
}
