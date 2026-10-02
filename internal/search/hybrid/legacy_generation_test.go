package hybrid_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/kit/embedmodel"

	"go.kenn.io/fotobank/internal/ai"
	"go.kenn.io/fotobank/internal/ai/embedding"
	"go.kenn.io/fotobank/internal/search/hybrid"
	"go.kenn.io/fotobank/internal/search/index"
	"go.kenn.io/fotobank/internal/testutil"
)

func TestSearchReusesLegacyImageGeneration(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	ctx := t.Context()
	owner := testutil.SeedOwner(t, d.WriteDB(), "example", "photographer")
	id := testutil.SeedPhoto(t, d.WriteDB(), owner, "dog.jpg")
	// Persist the pre-Kit format directly, including the L2 vec0 table. Neither
	// the new registry code nor Descriptor constructs this fixture's identity.
	const fingerprint = "image-model||jpeg-384-q85-metadata-stripped-embed-v1"
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(fingerprint+"\x00dim=2")))
	_, err := d.WriteDB().ExecContext(ctx, `INSERT INTO embedding_generations
  (id,fingerprint,fingerprint_hash,model_id,input_profile,vec_table_name,dimension,state,embedded_count,created_at)
  VALUES (42,?,?, 'image-model','jpeg-384-q85-metadata-stripped-embed-v1','media_embeddings_g42',2,'active',1,?)`, fingerprint, hash, time.Now().UTC())
	r.NoError(err)
	_, err = d.WriteDB().ExecContext(ctx, `CREATE VIRTUAL TABLE media_embeddings_g42 USING vec0(vec_id INTEGER PRIMARY KEY, embedding FLOAT[2])`)
	r.NoError(err)
	_, err = d.WriteDB().ExecContext(ctx, `INSERT INTO media_embedding_ids(generation_id,media_id,vec_id) VALUES (42,?,1)`, id)
	r.NoError(err)
	original := embedding.VecToBlob([]float32{3, 4})
	_, err = d.WriteDB().ExecContext(ctx, `INSERT INTO media_embeddings_g42(vec_id,embedding) VALUES(1,vec_f32(?))`, original)
	r.NoError(err)
	gens := embedding.NewGenerations(d.WriteDB(), d.ReadDB())
	fp := embedding.Fingerprint(ai.EmbedConfig{Model: "image-model", InputEdge: 384})
	descriptor := embedding.Descriptor(fp, 2)
	matches, err := descriptor.Matches(fingerprint)
	r.NoError(err)
	r.True(matches)
	queryDescriptor := descriptor
	queryDescriptor.Input.Recipe = "query-text"
	r.NoError(embedmodel.Compatible(descriptor, queryDescriptor))
	gen, err := gens.FindOrCreateBuilding(ctx, fp, 2)
	r.NoError(err)
	r.EqualValues(42, gen.ID)
	tx, err := d.WriteDB().BeginTx(ctx, nil)
	r.NoError(err)
	defer func() { _ = tx.Rollback() }()
	reused, err := gens.FindOrCreateBuildingTx(ctx, tx, fp, 2)
	r.NoError(err)
	r.NoError(tx.Commit())
	r.Equal(gen, reused)
	r.Equal("active", reused.State)

	calls := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, err := io.ReadAll(req.Body)
		assert.NoError(t, err)
		calls <- string(body)
		_, _ = io.WriteString(w, `{"data":[{"index":0,"embedding":[3,4]}]}`)
	}))
	t.Cleanup(srv.Close)
	// Live settings may describe a future generation. Search must still encode
	// with the active image model and width, without normalizing its raw output.
	c := embedding.NewClient(embedding.Config{Parts: (ai.EmbedConfig{Endpoint: srv.URL, Model: "future-model", Dimension: 3}).EmbeddingParts()})
	engine := hybrid.NewEngine(index.NewSQLiteVecBackend(d.ReadDB(), embedding.Row{}), func(context.Context) embedding.ClientIface { return c }, gens, engineCfg())
	response, err := engine.Search(ctx, hybrid.Request{Owner: owner, Query: "a dog", Limit: 1})
	r.NoError(err)
	r.Equal("hybrid", response.EngineMode)
	r.Len(response.Hits, 1)
	r.Equal(id, response.Hits[0].MediaID)
	r.Zero(*response.Hits[0].ScoreComponents.Vector)
	r.False(response.HasMore)
	r.JSONEq(`{"model":"image-model","input":["a dog"]}`, <-calls)
	var stored []byte
	r.NoError(d.ReadDB().QueryRowContext(ctx, `SELECT embedding FROM media_embeddings_g42 WHERE vec_id=1`).Scan(&stored))
	r.Equal(original, stored)
	var count int
	r.NoError(d.ReadDB().QueryRowContext(ctx, `SELECT count(*) FROM embedding_generations`).Scan(&count))
	r.Equal(1, count)
	r.NoError(d.ReadDB().QueryRowContext(ctx, `SELECT count(*) FROM ai_jobs`).Scan(&count))
	r.Zero(count, "reuse must not schedule a rebuild")
}
