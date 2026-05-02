package index

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/wesm/fotobank/internal/ai/embedding"
	"github.com/wesm/fotobank/internal/errs"
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
// BM25 + ANN + filter intersection + RRF fusion in one SQL statement
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

// FusedSearch runs the composed BM25 + ANN + filter intersection +
// RRF fusion. The skeleton lives in the plan; this is the concrete
// SQL with both CTEs intersected against the filter CTE.
//
// SQLite has no FULL OUTER JOIN, so the `fused` CTE emulates one with
// two LEFT JOINs UNION'd: every BM25 row (with its optional ANN
// counterpart) and every ANN-only row that didn't appear in BM25.
//
// The vec_table_name segment is interpolated via fmt.Sprintf because
// it's application-derived from gen.ID and never user input. Every
// other value is parameterised with `?`.
func (b *SQLiteVecBackend) FusedSearch(ctx context.Context, in SearchInput) ([]Hit, error) {
	if b.gen.ID == 0 || b.gen.VecTableName == "" {
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

	// SQL skeleton documented in the plan. Inline the CTE bodies in
	// the order: filter, bm25_raw + bm25 (FTS5 disallows bm25() inside
	// a window function in the same context, so the score is computed
	// once in bm25_raw and ROW_NUMBER is assigned over its alias),
	// ann_raw + ann (sqlite-vec MATCH must be the only constraint on
	// the vec0 base table, so the filter and rank assignment happen in
	// outer CTEs), fused (UNION-emulated FULL OUTER JOIN).
	var sb strings.Builder
	sb.WriteString("WITH\n")
	sb.WriteString("  filter AS (")
	sb.WriteString(in.Filter.SQL)
	sb.WriteString("),\n")
	sb.WriteString("  bm25_raw AS (\n")
	sb.WriteString("    SELECT mf.media_id AS id, bm25(media_fts) AS score\n")
	sb.WriteString("    FROM media_fts mf JOIN filter f ON f.id = mf.media_id\n")
	sb.WriteString("    WHERE media_fts MATCH ?\n")
	sb.WriteString("    ORDER BY score\n")
	sb.WriteString("    LIMIT ?\n")
	sb.WriteString("  ),\n")
	sb.WriteString("  bm25 AS (\n")
	sb.WriteString("    SELECT id, score, ROW_NUMBER() OVER (ORDER BY score) AS rank_bm25\n")
	sb.WriteString("    FROM bm25_raw\n")
	sb.WriteString("  ),\n")
	sb.WriteString("  ann_raw AS (\n")
	sb.WriteString("    SELECT v.vec_id, v.distance\n")
	fmt.Fprintf(&sb, "    FROM %s v\n", b.gen.VecTableName)
	sb.WriteString("    WHERE v.embedding MATCH vec_f32(?) AND v.k = ?\n")
	sb.WriteString("  ),\n")
	sb.WriteString("  ann AS (\n")
	sb.WriteString("    SELECT x.media_id AS id, ann_raw.distance AS score,\n")
	sb.WriteString("           ROW_NUMBER() OVER (ORDER BY ann_raw.distance) AS rank_vector\n")
	sb.WriteString("    FROM ann_raw\n")
	sb.WriteString("    JOIN media_embedding_ids x ON x.generation_id = ? AND x.vec_id = ann_raw.vec_id\n")
	sb.WriteString("    JOIN filter f ON f.id = x.media_id\n")
	sb.WriteString("  ),\n")
	sb.WriteString("  fused AS (\n")
	sb.WriteString("    SELECT b.id AS id, b.rank_bm25 AS rank_bm25, a.rank_vector AS rank_vector,\n")
	sb.WriteString("           b.score AS bm25, a.score AS vec\n")
	sb.WriteString("    FROM bm25 b LEFT JOIN ann a ON a.id = b.id\n")
	sb.WriteString("    UNION ALL\n")
	sb.WriteString("    SELECT a.id, NULL, a.rank_vector, NULL, a.score\n")
	sb.WriteString("    FROM ann a LEFT JOIN bm25 b ON b.id = a.id\n")
	sb.WriteString("    WHERE b.id IS NULL\n")
	sb.WriteString("  )\n")
	sb.WriteString("SELECT m.id, m.media_type, m.timestamp, m.imported_at, m.width, m.height, m.thumb_version,\n")
	sb.WriteString("       (CASE WHEN fused.rank_bm25 IS NOT NULL THEN 1.0 / (? + fused.rank_bm25) ELSE 0 END +\n")
	sb.WriteString("        CASE WHEN fused.rank_vector IS NOT NULL THEN 1.0 / (? + fused.rank_vector) ELSE 0 END) AS rrf,\n")
	sb.WriteString("       fused.bm25, fused.vec, fused.rank_bm25, fused.rank_vector\n")
	sb.WriteString("FROM fused JOIN media m ON m.id = fused.id\n")
	sb.WriteString("ORDER BY rrf DESC, m.id\n")
	sb.WriteString("LIMIT ?")

	args := make([]any, 0, len(in.Filter.Args)+8)
	args = append(args, in.Filter.Args...)
	// bm25_raw: MATCH ? then LIMIT ?
	args = append(args, in.Query, in.KPerSignal)
	// ann_raw: vec_f32(?), k = KPerSignal * annOverfetchFactor.
	// vec0 MATCH applies its k-cap before the filter join, so a query
	// that filters down to a small slice (owner-scoped, hidden=false)
	// can come up empty if k=KPerSignal happens to be filled by other
	// owners' vectors. Over-fetching gives the filter room to narrow.
	args = append(args, embedding.VecToBlob(in.QueryVector), in.KPerSignal*annOverfetchFactor)
	// ann: generation_id = ?
	args = append(args, b.gen.ID)
	// SELECT: RRF k for BM25 then for vector, then outer LIMIT.
	args = append(args, in.RRFK, in.RRFK, in.Limit)

	rows, err := b.ro.QueryContext(ctx, sb.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("fused search: %w", err)
	}
	defer func() { _ = rows.Close() }()

	return scanFusedHits(rows)
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

	var sb strings.Builder
	sb.WriteString("WITH\n")
	sb.WriteString("  filter AS (")
	sb.WriteString(in.Filter.SQL)
	sb.WriteString("),\n")
	sb.WriteString("  bm25_raw AS (\n")
	sb.WriteString("    SELECT mf.media_id AS id, bm25(media_fts) AS score\n")
	sb.WriteString("    FROM media_fts mf JOIN filter f ON f.id = mf.media_id\n")
	sb.WriteString("    WHERE media_fts MATCH ?\n")
	sb.WriteString("    ORDER BY score\n")
	sb.WriteString("    LIMIT ?\n")
	sb.WriteString("  ),\n")
	sb.WriteString("  bm25 AS (\n")
	sb.WriteString("    SELECT id, score, ROW_NUMBER() OVER (ORDER BY score) AS rank_bm25\n")
	sb.WriteString("    FROM bm25_raw\n")
	sb.WriteString("  )\n")
	sb.WriteString("SELECT m.id, m.media_type, m.timestamp, m.imported_at, m.width, m.height, m.thumb_version,\n")
	sb.WriteString("       bm25.score AS bm25, bm25.rank_bm25\n")
	sb.WriteString("FROM bm25 JOIN media m ON m.id = bm25.id\n")
	sb.WriteString("ORDER BY bm25.score, m.id\n")
	sb.WriteString("LIMIT ?")

	args := make([]any, 0, len(in.Filter.Args)+3)
	args = append(args, in.Filter.Args...)
	args = append(args, in.Query, in.KPerSignal, in.Limit)

	rows, err := b.ro.QueryContext(ctx, sb.String(), args...)
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
			&h.MediaID, &h.MediaType, &ts, &h.ImportedAt, &width, &height, &h.ThumbVersion,
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
	sb.WriteString("SELECT m.id, m.media_type, m.timestamp, m.imported_at, m.width, m.height, m.thumb_version\n")
	sb.WriteString("FROM filter f JOIN media m ON m.id = f.id\n")
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
	sb.WriteString("LIMIT ?")

	args := make([]any, 0, len(in.Filter.Args)+1)
	args = append(args, in.Filter.Args...)
	args = append(args, in.Limit)

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
			&h.MediaID, &h.MediaType, &ts, &h.ImportedAt, &width, &height, &h.ThumbVersion,
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
			&h.MediaID, &h.MediaType, &ts, &h.ImportedAt, &width, &height, &h.ThumbVersion,
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
