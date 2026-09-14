package hybrid_test

import (
	"fmt"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/ai/embedding"
	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/search/hybrid"
	"go.kenn.io/fotobank/internal/search/index"
	"go.kenn.io/fotobank/internal/testutil"
)

// Exercise the real SQL for every search mode, including score/date ties,
// missing capture dates, hidden/other-owner rows, and full terminal pages.
func TestSearchPagination(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	owner := testutil.SeedOwner(t, d.WriteDB(), "example", "photographer")
	other := testutil.SeedOwner(t, d.WriteDB(), "example", "other")
	gens := embedding.NewGenerations(d.WriteDB(), d.ReadDB())
	gen := seedActiveGeneration(t, gens)
	mapping := embedding.NewMapping(d.WriteDB())
	ids := make([]string, 0, 5)
	stamp := time.Date(2026, 1, 1, 12, 0, 0, 123, time.UTC)
	for i := range 7 {
		principal := owner
		if i == 6 {
			principal = other
		}
		id := testutil.SeedPhoto(t, d.WriteDB(), principal, fmt.Sprintf("sunset-%d", i))
		var captured, hidden any
		if i < 2 {
			captured = stamp
		}
		if i == 2 {
			captured = stamp.Add(time.Hour)
		}
		if i == 5 {
			hidden = stamp
		}
		_, err := d.WriteDB().ExecContext(t.Context(), `UPDATE assets SET timestamp=?, imported_at=?, hidden_at=? WHERE id=?`, captured, stamp, hidden, id)
		r.NoError(err)
		_, err = d.WriteDB().ExecContext(t.Context(), `INSERT INTO media_fts(media_id, filename) VALUES (?, 'sunset.jpg')`, id)
		r.NoError(err)
		_, err = mapping.WriteVector(t.Context(), *gen, id, make([]float32, 768))
		r.NoError(err)
		if i < 5 {
			ids = append(ids, id)
		}
	}
	ties := slices.Sorted(slices.Values(ids[:2]))
	undated := slices.Sorted(slices.Values(ids[3:]))
	for _, mode := range []string{"filter", "lexical", "hybrid"} {
		for _, sort := range []string{"relevance", "newest", "oldest"} {
			for _, limit := range []int{1, 2, 5} {
				t.Run(fmt.Sprintf("%s/%s/%d", mode, sort, limit), func(t *testing.T) {
					r := require.New(t)
					var provider embedding.ClientIface
					if mode == "hybrid" {
						provider = &fakeTextClient{vec: make([]float32, 768)}
					}
					engine := hybrid.NewEngine(index.NewSQLiteVecBackend(d.ReadDB(), embedding.Row{}), provider, gens, engineCfg())
					req := hybrid.Request{Owner: owner, Query: "sunset", Sort: sort, Limit: limit}
					if mode == "filter" {
						req.Query = ""
					}
					var got []string
					for page := 0; ; page++ {
						r.Less(page, 6, "pagination must terminate")
						result, err := engine.Search(t.Context(), req)
						r.NoError(err)
						r.LessOrEqual(len(result.Hits), limit)
						for _, hit := range result.Hits {
							got = append(got, hit.MediaID)
						}
						if len(got) >= len(ids) {
							r.False(result.HasMore, "full last page must not advertise more")
							r.Empty(result.NextCursor)
							break
						}
						r.True(result.HasMore)
						r.NotEmpty(result.NextCursor)
						req.Cursor = result.NextCursor
					}
					want := slices.Sorted(slices.Values(ids))
					if sort == "oldest" {
						want = append(append(slices.Clone(ties), ids[2]), undated...)
					}
					if sort == "newest" || (mode == "filter" && sort == "relevance") {
						want = append(append([]string{ids[2]}, ties...), undated...)
					}
					r.Equal(want, got)

					req.Cursor = ""
					req.Limit = 1
					first, err := engine.Search(t.Context(), req)
					r.NoError(err)
					req.Cursor = first.NextCursor
					req.Query = "different"
					_, err = engine.Search(t.Context(), req)
					r.ErrorIs(err, errs.ErrInvalidArgument)
				})
			}
		}
	}

	engine := hybrid.NewEngine(index.NewSQLiteVecBackend(d.ReadDB(), embedding.Row{}), nil, gens, engineCfg())
	req := hybrid.Request{Owner: owner, Limit: 2}
	first, err := engine.Search(t.Context(), req)
	r.NoError(err)
	r.NotEmpty(first.NextCursor)
	req.Cursor = first.NextCursor
	for _, changed := range []hybrid.Request{
		{Owner: other, Limit: 2, Cursor: req.Cursor},
		{Owner: owner, Limit: 2, IncludeHidden: true, Cursor: req.Cursor},
		{Owner: owner, Limit: 2, Filter: hybrid.Input{HasGPS: new(true)}, Cursor: req.Cursor},
	} {
		_, err := engine.Search(t.Context(), changed)
		r.ErrorIs(err, errs.ErrInvalidArgument)
	}
	cursor, err := hybrid.DecodeCursor(req.Cursor)
	r.NoError(err)
	for _, offset := range []int{-1, 0, math.MaxInt} {
		cursor.Offset = offset
		req.Cursor = hybrid.EncodeCursor(cursor)
		_, err := engine.Search(t.Context(), req)
		r.ErrorIs(err, errs.ErrInvalidArgument)
	}
}
