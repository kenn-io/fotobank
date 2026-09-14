package hybrid_test

import (
	"context"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/fotobank/internal/ai/embedding"
	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/search/hybrid"
	"go.kenn.io/fotobank/internal/search/index"
	"go.kenn.io/fotobank/internal/testutil"
)

func TestSearchCursorRejectsChangedQueryEmbedding(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	owner := testutil.SeedOwner(t, d.WriteDB(), "example", "photographer")
	gens := embedding.NewGenerations(d.WriteDB(), d.ReadDB())
	gen := seedActiveGeneration(t, gens)
	vectors := [2][]float32{make([]float32, 768), make([]float32, 768)}
	var ids [2]string
	for i := range vectors {
		vectors[i][i] = 1
		ids[i] = testutil.SeedPhoto(t, d.WriteDB(), owner, []string{"first", "second"}[i])
		_, err := embedding.NewMapping(d.WriteDB()).WriteVector(t.Context(), *gen, ids[i], vectors[i])
		r.NoError(err)
	}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		vec := vectors[0]
		if req.URL.Path == "/second/embeddings" || req.Header.Get("Authorization") == "Bearer synthetic-second-key" {
			vec = vectors[1]
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.MarshalWrite(w, map[string]any{"data": []any{map[string]any{"index": 0, "embedding": vec}}})
	}))
	t.Cleanup(provider.Close)
	for _, change := range []string{"endpoint", "credentials"} {
		t.Run(change, func(t *testing.T) {
			r := require.New(t)
			cfg := embedding.Config{Endpoint: provider.URL + "/first", APIKey: "synthetic-first-key"}
			client := embedding.NewClient(cfg)
			engine := hybrid.NewEngine(index.NewSQLiteVecBackend(d.ReadDB(), embedding.Row{}), func(context.Context) embedding.ClientIface {
				return client
			}, gens, engineCfg())
			req := hybrid.Request{Owner: owner, Query: "sunset", Limit: 1}
			first, err := engine.Search(t.Context(), req)
			r.NoError(err)
			r.Len(first.Hits, 1)
			r.Equal(ids[0], first.Hits[0].MediaID)
			r.NotEmpty(first.NextCursor)
			req.Cursor = first.NextCursor
			second, err := engine.Search(t.Context(), req)
			r.NoError(err)
			r.Len(second.Hits, 1)
			r.Equal(ids[1], second.Hits[0].MediaID)

			if change == "endpoint" {
				cfg.Endpoint = provider.URL + "/second"
			} else {
				cfg.APIKey = "synthetic-second-key"
			}
			client = embedding.NewClient(cfg)
			fresh, err := engine.Search(t.Context(), hybrid.Request{Owner: owner, Query: "sunset", Limit: 1})
			r.NoError(err)
			r.Len(fresh.Hits, 1)
			r.Equal(ids[1], fresh.Hits[0].MediaID, "new provider settings reverse the ranking without changing the generation")
			stale, err := engine.Search(t.Context(), req)
			r.ErrorIs(err, errs.ErrInvalidArgument, "old cursor must not repeat page one: %+v", stale.Hits)
			req.Cursor = fresh.NextCursor
			last, err := engine.Search(t.Context(), req)
			r.NoError(err)
			r.Len(last.Hits, 1)
			r.Equal(ids[0], last.Hits[0].MediaID)
			r.False(last.HasMore)
		})
	}
}
