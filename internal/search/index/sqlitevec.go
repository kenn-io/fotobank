package index

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"fmt"
	"strings"

	kithybrid "go.kenn.io/kit/search/hybrid"
	"go.kenn.io/kit/search/sqlitefts"
	"go.kenn.io/kit/search/sqlquery"

	"go.kenn.io/fotobank/internal/ai/embedding"
	"go.kenn.io/fotobank/internal/errs"
)

// annOverfetchFactor controls how many extra ANN candidates the backend
// asks sqlite-vec for relative to KPerSignal. The vec0 MATCH operator
// has no way to apply a SQL filter (e.g. owner scoping) before the
// LIMIT — it returns the top-k vectors over the entire generation, and
// the filter join narrows that pool afterwards. If the filter shrinks
// the pool drastically (e.g. owner A's 5 photos in a corpus of 1000),
// the bare KPerSignal-cap on ann_raw can leave the post-join set empty
// because most of the top-k vectors belong to other owners.
//
// Multiplying KPerSignal by this factor over-fetches at the vec layer
// so the post-filter set is dense enough for the RRF fusion. v1 picks
// 10 — enough to absorb a 10:1 owner imbalance — at the cost of
// scanning 10× as many vec rows per query. M3 will replace this with
// a per-request iteration that asks for more ANN candidates only when
// the filter narrows them out.
const annOverfetchFactor = 10

// SQLiteVecBackend is the production Backend. It runs the composed
// BM25 + ANN + filter intersection + RRF fusion in one read transaction
// against the read-only pool, joining the per-generation vec0 virtual
// table with media_fts and the resolved filter CTE.
type SQLiteVecBackend struct {
	ro  *sql.DB
	gen embedding.Row
}

// NewSQLiteVecBackend returns a Backend bound to ro and the supplied
// active generation. gen carries the application-derived
// VecTableName ("media_embeddings_g{id}") and the dimension; the empty
// (zero-value) Row is acceptable for the BM25Only / FilterOnly paths
// because they don't consult the vec table.
func NewSQLiteVecBackend(ro *sql.DB, gen embedding.Row) *SQLiteVecBackend {
	return &SQLiteVecBackend{ro: ro, gen: gen}
}

// fusedOrderBy returns the trailing ORDER BY clause for FusedSearch's
// final SELECT. SortNewest / SortOldest sort the relevance-selected
// candidate pool by date for the page; SortRelevance (and any other
// value) falls through to RRF DESC. The returned string includes
// the trailing newline so the caller can append "LIMIT ?" without
// formatting gymnastics.
//
// The split between candidate selection (relevance) and page sort
// (date) mirrors the msgvault pattern: the user sees their date-
// ordered photos but the engine still confines the page to the
// most relevant N candidates. A different split — sort the entire
// owner library by date and rank — would defeat the relevance
// signal for any but the very first matches.
func fusedOrderBy(s Sort) string {
	switch s {
	case SortNewest:
		return "ORDER BY m.timestamp IS NULL, m.timestamp DESC, m.imported_at DESC, m.id\n"
	case SortOldest:
		return "ORDER BY m.timestamp IS NULL, m.timestamp ASC, m.imported_at ASC, m.id\n"
	default:
		return "ORDER BY rrf DESC, m.id\n"
	}
}

// bm25OrderBy is the BM25Only counterpart to fusedOrderBy. The
// fallback (SortRelevance / zero) sorts by BM25 ascending — bm25()
// returns negative scores where lower is better.
func bm25OrderBy(s Sort) string {
	switch s {
	case SortNewest:
		return "ORDER BY m.timestamp IS NULL, m.timestamp DESC, m.imported_at DESC, m.id\n"
	case SortOldest:
		return "ORDER BY m.timestamp IS NULL, m.timestamp ASC, m.imported_at ASC, m.id\n"
	default:
		return "ORDER BY bm25.score, m.id\n"
	}
}

// validateFilter enforces the security contract that every Backend
// call must arrive with an owner-conditioned filter CTE. Earlier
// revisions silently substituted a scan-all-media SELECT when
// Filter.SQL was empty — convenient for tests but a cross-owner
// data-leak risk if the engine path ever forwarded a SearchInput
// with an unset Filter.SQL into production. Fail closed instead:
// callers (engine, tests) must always supply an explicit filter
// (M1's Resolve emits an owner-scoped body even when the request
// has no other filters).
func validateFilter(in SearchInput) error {
	if in.Filter.SQL == "" {
		return fmt.Errorf("Filter.SQL required: %w", errs.ErrInvalidArgument)
	}
	return nil
}

// FusedSearch retrieves both candidate legs on one read snapshot, fuses their
// ranks with Kit, then hydrates and sorts the bounded candidate set in SQL.
// Fotobank owns the ANN query because its vector layout and L2 indexes predate
// Kit's store. Unit-normalized vectors have the same L2 and cosine ordering.
func (b *SQLiteVecBackend) FusedSearch(ctx context.Context, in SearchInput) ([]Hit, error) {
	gen := b.gen
	if in.Gen != nil {
		gen = *in.Gen
	}
	if gen.ID == 0 || gen.VecTableName == "" {
		return nil, fmt.Errorf("FusedSearch requires an active embedding generation")
	}
	if len(in.QueryVector) == 0 {
		return nil, fmt.Errorf("FusedSearch requires a non-empty QueryVector")
	}
	if in.Query == "" {
		return nil, fmt.Errorf("FusedSearch requires a non-empty Query")
	}
	if err := validateFilter(in); err != nil {
		return nil, err
	}

	lexical, err := lexicalCandidates(in)
	if err != nil {
		return nil, err
	}
	ann := sqlquery.Query{
		SQL: fmt.Sprintf(`WITH filter AS (%s), ann_raw AS (
   SELECT vec_id, distance FROM %s
   WHERE embedding MATCH vec_f32(?) AND k = ?
  )
  SELECT x.media_id, ann_raw.distance FROM ann_raw
  JOIN media_embedding_ids x ON x.generation_id = ? AND x.vec_id = ann_raw.vec_id
  JOIN filter f ON f.id = x.media_id
  ORDER BY ann_raw.distance, x.media_id LIMIT ?`, in.Filter.SQL, gen.VecTableName),
		Args: append(append([]any{}, in.Filter.Args...), embedding.VecToBlob(in.QueryVector), in.KPerSignal*annOverfetchFactor, gen.ID, in.KPerSignal),
	}
	tx, err := b.ro.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("begin search snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	bm25Scores, vectorScores := map[string]float64{}, map[string]float64{}
	result, err := kithybrid.Run(ctx, tx, float64(in.RRFK), []kithybrid.Leg[string]{
		{Name: "bm25", Weight: 1, Query: lexical, CandidateLimit: in.KPerSignal,
			Scan: func(rows *sql.Rows) (string, error) {
				var id string
				var score float64
				err := rows.Scan(&id, &score)
				bm25Scores[id] = -score // Kit exposes higher-is-better; the API exposes SQLite BM25.
				return id, err
			}},
		{Name: "vector", Weight: 1, Query: ann, CandidateLimit: in.KPerSignal,
			Scan: func(rows *sql.Rows) (string, error) {
				var id string
				var distance float64
				err := rows.Scan(&id, &distance)
				vectorScores[id] = distance
				return id, err
			}},
	})
	if err != nil {
		return nil, fmt.Errorf("fuse search candidates: %w", err)
	}
	// Keep final eligibility and date sorting at the existing SQL boundary.
	// JSON carries only the bounded candidate rows, with parameterized values.
	type candidate struct {
		ID         string   `json:"id"`
		Score      float64  `json:"score"`
		BM25       *float64 `json:"bm25"`
		Vector     *float64 `json:"vector"`
		RankBM25   *int     `json:"rank_bm25"`
		RankVector *int     `json:"rank_vector"`
	}
	candidates := make([]candidate, 0, len(result.Hits))
	for _, hit := range result.Hits {
		c := candidate{ID: hit.Key, Score: hit.Score}
		for _, contribution := range hit.Contributions {
			switch contribution.Leg {
			case "bm25":
				c.BM25, c.RankBM25 = new(bm25Scores[hit.Key]), new(contribution.Rank)
			case "vector":
				c.Vector, c.RankVector = new(vectorScores[hit.Key]), new(contribution.Rank)
			}
		}
		candidates = append(candidates, c)
	}
	payload, err := json.Marshal(candidates)
	if err != nil {
		return nil, fmt.Errorf("encode search candidates: %w", err)
	}
	rows, err := tx.QueryContext(ctx, `
 SELECT m.id, m.media_type, m.timestamp, m.imported_at, m.width, m.height, m.thumb_version, m.thumb_status,
  json_extract(c.value, '$.score') AS rrf,
  json_extract(c.value, '$.bm25'), json_extract(c.value, '$.vector'),
  json_extract(c.value, '$.rank_bm25'), json_extract(c.value, '$.rank_vector')
 FROM json_each(?) c JOIN assets m ON m.id = json_extract(c.value, '$.id') AND m.state = 'ready'
 `+fusedOrderBy(in.Sort)+`LIMIT ? OFFSET ?`, string(payload), in.Limit, in.Offset)
	if err != nil {
		return nil, fmt.Errorf("hydrate fused search: %w", err)
	}
	hits, err := scanFusedHits(rows)
	closeErr := rows.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close fused search: %w", closeErr)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit search snapshot: %w", err)
	}
	return hits, nil
}

// lexicalCandidates applies the existing owner/visibility filter before the
// candidate cap. The shared helper returns negated BM25 and orders ties by ID.
func lexicalCandidates(in SearchInput) (sqlquery.Query, error) {
	helper, err := sqlitefts.New(
		sqlitefts.WithIndexTable("media_fts"), sqlitefts.WithIndexKey("media_id"),
		sqlitefts.WithSourceTable("filter"), sqlitefts.WithSourceKey("id"),
	)
	if err != nil {
		return sqlquery.Query{}, fmt.Errorf("configure lexical candidates: %w", err)
	}
	q, err := helper.Build(sqlitefts.Request{Match: in.Query, CandidateLimit: in.KPerSignal})
	if err != nil {
		return sqlquery.Query{}, fmt.Errorf("build lexical candidates: %w", err)
	}
	q.SQL = "WITH filter AS (" + in.Filter.SQL + ") " + q.SQL
	q.Args = append(append([]any{}, in.Filter.Args...), q.Args...)
	return q, nil
}

// BM25Only runs only the bm25 CTE against the filter, returning hits
// in BM25 ascending order. Used when the engine has query text but no
// semantic signal (no active generation, or the request opted out).
func (b *SQLiteVecBackend) BM25Only(ctx context.Context, in SearchInput) ([]Hit, error) {
	if in.Query == "" {
		return nil, fmt.Errorf("BM25Only requires a non-empty Query")
	}
	if err := validateFilter(in); err != nil {
		return nil, err
	}

	candidates, err := lexicalCandidates(in)
	if err != nil {
		return nil, err
	}
	query := `WITH bm25_raw AS (` + candidates.SQL + `), bm25 AS (
 SELECT doc_key AS id, -score AS score,
  ROW_NUMBER() OVER (ORDER BY score DESC, doc_key) AS rank_bm25 FROM bm25_raw
 )
 SELECT m.id, m.media_type, m.timestamp, m.imported_at, m.width, m.height, m.thumb_version, m.thumb_status,
  bm25.score, bm25.rank_bm25
 FROM bm25 JOIN assets m ON m.id = bm25.id AND m.state = 'ready'
 ` + bm25OrderBy(in.Sort) + `LIMIT ? OFFSET ?`
	args := append(candidates.Args, in.Limit, in.Offset)

	rows, err := b.ro.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("bm25-only search: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Hit
	for rows.Next() {
		var (
			h         Hit
			ts        sql.NullTime
			width     sql.NullInt64
			height    sql.NullInt64
			bm25Score sql.NullFloat64
			rankBM25  sql.NullInt64
		)
		if err := rows.Scan(
			&h.MediaID, &h.MediaType, &ts, &h.ImportedAt, &width, &height, &h.ThumbVersion, &h.ThumbStatus,
			&bm25Score, &rankBM25,
		); err != nil {
			return nil, fmt.Errorf("scan bm25 hit: %w", err)
		}
		applyOptionals(&h, ts, width, height)
		h.ScoreComponents = &ScoreComponents{}
		if bm25Score.Valid {
			v := bm25Score.Float64
			h.ScoreComponents.BM25 = &v
			h.Score = v
		}
		if rankBM25.Valid {
			v := int(rankBM25.Int64)
			h.ScoreComponents.RankBM25 = &v
		}
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iter bm25 rows: %w", err)
	}
	return out, nil
}

// FilterOnly returns owner-visible media without consulting BM25 or
// ANN. Used when the request has no query text and no query vector —
// e.g. a date-range page in the search UI.
//
// The Sort field selects the ORDER BY: SortNewest is the default
// (timestamp DESC NULLS LAST then imported_at DESC), SortOldest
// reverses both, SortRelevance falls through to SortNewest because
// "relevance" has no meaning without a query signal.
func (b *SQLiteVecBackend) FilterOnly(ctx context.Context, in SearchInput) ([]Hit, error) {
	if err := validateFilter(in); err != nil {
		return nil, err
	}
	var sb strings.Builder
	sb.WriteString("WITH filter AS (")
	sb.WriteString(in.Filter.SQL)
	sb.WriteString(")\n")
	sb.WriteString("SELECT m.id, m.media_type, m.timestamp, m.imported_at, m.width, m.height, m.thumb_version, m.thumb_status\n")
	sb.WriteString("FROM filter f JOIN assets m ON m.id = f.id AND m.state = 'ready'\n")
	switch in.Sort {
	case SortOldest:
		// Oldest first: NULL timestamps last so a row with a known
		// early timestamp beats one with no timestamp at all.
		sb.WriteString("ORDER BY m.timestamp IS NULL, m.timestamp ASC, m.imported_at ASC, m.id\n")
	default:
		// SortNewest / SortRelevance / zero value all collapse to
		// "newest first" — relevance is meaningless without a query.
		sb.WriteString("ORDER BY m.timestamp IS NULL, m.timestamp DESC, m.imported_at DESC, m.id\n")
	}
	sb.WriteString("LIMIT ? OFFSET ?")

	args := make([]any, 0, len(in.Filter.Args)+2)
	args = append(args, in.Filter.Args...)
	args = append(args, in.Limit, in.Offset)

	rows, err := b.ro.QueryContext(ctx, sb.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("filter-only search: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Hit
	for rows.Next() {
		var (
			h      Hit
			ts     sql.NullTime
			width  sql.NullInt64
			height sql.NullInt64
		)
		if err := rows.Scan(
			&h.MediaID, &h.MediaType, &ts, &h.ImportedAt, &width, &height, &h.ThumbVersion, &h.ThumbStatus,
		); err != nil {
			return nil, fmt.Errorf("scan filter hit: %w", err)
		}
		applyOptionals(&h, ts, width, height)
		// FilterOnly carries no score components; leave ScoreComponents
		// nil so a caller observing it can distinguish the empty mode
		// from a hit that came through BM25Only or FusedSearch.
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iter filter rows: %w", err)
	}
	return out, nil
}

// scanFusedHits drains rows from the FusedSearch query, populating
// ScoreComponents from the per-CTE columns. The SELECT shape is fixed
// — twelve columns in the order documented at the SELECT site.
func scanFusedHits(rows *sql.Rows) ([]Hit, error) {
	var out []Hit
	for rows.Next() {
		var (
			h          Hit
			ts         sql.NullTime
			width      sql.NullInt64
			height     sql.NullInt64
			rrf        sql.NullFloat64
			bm25Score  sql.NullFloat64
			vecScore   sql.NullFloat64
			rankBM25   sql.NullInt64
			rankVector sql.NullInt64
		)
		if err := rows.Scan(
			&h.MediaID, &h.MediaType, &ts, &h.ImportedAt, &width, &height, &h.ThumbVersion, &h.ThumbStatus,
			&rrf, &bm25Score, &vecScore, &rankBM25, &rankVector,
		); err != nil {
			return nil, fmt.Errorf("scan fused hit: %w", err)
		}
		applyOptionals(&h, ts, width, height)
		h.ScoreComponents = &ScoreComponents{}
		if rrf.Valid {
			v := rrf.Float64
			h.ScoreComponents.RRF = &v
			h.Score = v
		}
		if bm25Score.Valid {
			v := bm25Score.Float64
			h.ScoreComponents.BM25 = &v
		}
		if vecScore.Valid {
			v := vecScore.Float64
			h.ScoreComponents.Vector = &v
		}
		if rankBM25.Valid {
			v := int(rankBM25.Int64)
			h.ScoreComponents.RankBM25 = &v
		}
		if rankVector.Valid {
			v := int(rankVector.Int64)
			h.ScoreComponents.RankVector = &v
		}
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iter fused rows: %w", err)
	}
	return out, nil
}

// applyOptionals copies the nullable timestamp / width / height columns
// onto h. Hoisted so all three Backend modes share one shape; a
// followup that grows the optional set only edits this helper.
func applyOptionals(h *Hit, ts sql.NullTime, width, height sql.NullInt64) {
	if ts.Valid {
		v := ts.Time
		h.Timestamp = &v
	}
	if width.Valid {
		v := int(width.Int64)
		h.Width = &v
	}
	if height.Valid {
		v := int(height.Int64)
		h.Height = &v
	}
}
