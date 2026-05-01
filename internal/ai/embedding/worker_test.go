package embedding_test

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/ai"
	"github.com/wesm/fotobank/internal/ai/embedding"
	"github.com/wesm/fotobank/internal/ai/jobs"
	"github.com/wesm/fotobank/internal/ai/skipped"
	"github.com/wesm/fotobank/internal/db"
	"github.com/wesm/fotobank/internal/testutil"
)

// embedFP is the canonical fingerprint used across the worker tests so
// every test asserts the same triple the generation registry hashes on.
func embedFP() ai.Fingerprint {
	return ai.Fingerprint{
		ModelID:      "siglip2",
		InputProfile: "jpeg-384-q85-metadata-stripped-embed-v1",
	}
}

// embedCfg is the minimal EmbedConfig the worker consumes.
func embedCfg() ai.EmbedConfig {
	return ai.EmbedConfig{
		Model:     "siglip2",
		Dimension: 768,
		InputEdge: 384,
		BatchSize: 32,
	}
}

// fakeEmbedClient is the test stub for embedding.ClientIface. It either
// returns a fixed slate of vectors (one per call) or a configured error.
// vectorsToReturn caps the response length so partial-failure cases can
// be exercised by returning fewer vectors than the input batch size.
type fakeEmbedClient struct {
	vectors         [][]float32
	vectorsToReturn int // -1 means "match input length exactly"
	err             error
	calls           atomic.Int32
	lastInputCount  atomic.Int32
}

func (f *fakeEmbedClient) EmbedImages(_ context.Context, jpegs [][]byte) ([][]float32, error) {
	f.calls.Add(1)
	f.lastInputCount.Store(int32(len(jpegs)))
	if f.err != nil {
		return nil, f.err
	}
	n := len(jpegs)
	if f.vectorsToReturn >= 0 {
		n = f.vectorsToReturn
	}
	if n > len(f.vectors) {
		n = len(f.vectors)
	}
	return f.vectors[:n], nil
}

// fakeResolver returns the same (jpeg, status) pair for every media id.
// Variation across tests is achieved by constructing a fresh resolver
// per test rather than per-id overrides, which kept the F1 wiring
// simple — F3 will graduate to a per-id stub once the run loop needs
// to exercise mixed-status batches.
type fakeResolver struct {
	defaultJPEG   []byte
	defaultStatus string
}

func (f *fakeResolver) ResolvePreviewJPEG(_ context.Context, _ string) ([]byte, string, error) {
	return f.defaultJPEG, f.defaultStatus, nil
}

// recordingEmitter captures EmitAIEmbedCompleted calls so happy-path
// tests can confirm the worker fires the post-commit event for every
// successful claim.
type recordingEmitter struct {
	completed atomic.Int32
	failed    atomic.Int32
}

func (r *recordingEmitter) EmitAIEmbedCompleted(_ string, _ string) { r.completed.Add(1) }
func (r *recordingEmitter) EmitAIEmbedFailed(_ string, _ string)    { r.failed.Add(1) }

// dim768N returns n distinct 768-dim vectors. Each vector starts with a
// distinct float so the worker's positional alignment can be verified
// without a deep-equals comparison.
func dim768N(n int) [][]float32 {
	out := make([][]float32, n)
	for i := range out {
		out[i] = make([]float32, 768)
		out[i][0] = float32(i+1) * 0.01
	}
	return out
}

// mockJPEG returns a small decodable JPEG. EncodeEmbed re-decodes the
// preview before resizing, so the bytes must be a valid JPEG image —
// not arbitrary bytes.
var mockJPEG = func() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 32, 24))
	for y := range 24 {
		for x := range 32 {
			img.Set(x, y, color.RGBA{R: uint8(x * 8), G: uint8(y * 10), B: 0x40, A: 0xff})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err != nil {
		panic(err)
	}
	return buf.Bytes()
}()

// mappingExists reports whether a media_embedding_ids row exists for
// (gen, media). Used to check that the worker actually wrote the
// mapping under the expected generation.
func mappingExists(t *testing.T, d *db.DB, genID int64, mediaID string) bool {
	t.Helper()
	var n int
	require.NoError(t, d.ReadDB().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM media_embedding_ids WHERE generation_id=? AND media_id=?`,
		genID, mediaID,
	).Scan(&n))
	return n > 0
}

// jobStatus reads ai_jobs.status for a row keyed by (media, task). The
// worker's outcome is verified by inspecting status alongside any
// side-effects (mappings, ai_skipped rows).
func jobStatus(t *testing.T, d *db.DB, mediaID string, task ai.Task) string {
	t.Helper()
	var s string
	require.NoError(t, d.ReadDB().QueryRowContext(context.Background(),
		`SELECT status FROM ai_jobs WHERE media_id=? AND task=?`,
		mediaID, string(task),
	).Scan(&s))
	return s
}

// embeddedCount reads embedding_generations.embedded_count for the
// supplied generation row id.
func embeddedCount(t *testing.T, d *db.DB, genID int64) int {
	t.Helper()
	var n int
	require.NoError(t, d.ReadDB().QueryRowContext(context.Background(),
		`SELECT embedded_count FROM embedding_generations WHERE id=?`, genID,
	).Scan(&n))
	return n
}

// newTestWorker assembles a worker against a fresh test DB. Callers
// supply the resolver/client/emitter so per-test variation is local;
// queue, generations, mapping, skipped repo, and config are constant.
func newTestWorker(
	t *testing.T,
	d *db.DB,
	resolver embedding.PreviewResolver,
	client embedding.ClientIface,
	emitter embedding.EventEmitter,
) (*embedding.Worker, *jobs.Queue, *embedding.Generations) {
	t.Helper()
	q := jobs.NewQueue(d.WriteDB(), d.ReadDB())
	gens := embedding.NewGenerations(d.WriteDB(), d.ReadDB())
	mapping := embedding.NewMapping(d.WriteDB())
	skipR := skipped.NewRepo(d.WriteDB(), d.ReadDB())
	w := embedding.NewWorker(embedding.WorkerDeps{
		Q:        q,
		Gens:     gens,
		Mapping:  mapping,
		Client:   client,
		Resolver: resolver,
		Cfg:      embedCfg(),
		Events:   emitter,
		DB:       d.WriteDB(),
		Skipped:  skipR,
	})
	return w, q, gens
}

func TestWorker_ProcessesBatchEndToEnd(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	d := testutil.OpenTestDB(t)
	owner := testutil.SeedOwner(t, d.WriteDB(), "local", "alice")
	mids := []string{
		testutil.SeedPhoto(t, d.WriteDB(), owner, "p1"),
		testutil.SeedPhoto(t, d.WriteDB(), owner, "p2"),
		testutil.SeedPhoto(t, d.WriteDB(), owner, "p3"),
	}
	resolver := &fakeResolver{defaultJPEG: mockJPEG, defaultStatus: "ready"}
	client := &fakeEmbedClient{vectors: dim768N(3), vectorsToReturn: -1}
	emitter := &recordingEmitter{}
	w, q, gens := newTestWorker(t, d, resolver, client, emitter)

	fp := embedFP()
	for _, m := range mids {
		r.NoError(q.Enqueue(ctx, m, ai.TaskEmbed, fp))
	}

	r.NoError(w.RunOnce(ctx))

	// Generation row exists in 'building' state with the expected
	// embedded_count after the worker's per-batch UPDATE.
	gen, err := gens.FindOrCreateBuilding(ctx, fp, embedCfg().Dimension)
	r.NoError(err)
	r.Equal("building", gen.State)
	r.Equal(3, embeddedCount(t, d, gen.ID))

	// Each media has a mapping row under that generation.
	for _, m := range mids {
		r.True(mappingExists(t, d, gen.ID, m), "media %s missing mapping", m)
		r.Equal("done", jobStatus(t, d, m, ai.TaskEmbed))
	}

	// One batched call to the embeddings endpoint, three completion
	// events emitted (one per claim, post-commit).
	r.EqualValues(1, client.calls.Load())
	r.EqualValues(3, client.lastInputCount.Load())
	r.EqualValues(3, emitter.completed.Load())
	r.EqualValues(0, emitter.failed.Load())
}

func TestWorker_ReplacementIsZeroDelta(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	d := testutil.OpenTestDB(t)
	owner := testutil.SeedOwner(t, d.WriteDB(), "local", "alice")
	mid := testutil.SeedPhoto(t, d.WriteDB(), owner, "p1")
	resolver := &fakeResolver{defaultJPEG: mockJPEG, defaultStatus: "ready"}
	client := &fakeEmbedClient{vectors: dim768N(1), vectorsToReturn: -1}
	w, q, gens := newTestWorker(t, d, resolver, client, &recordingEmitter{})

	fp := embedFP()
	r.NoError(q.Enqueue(ctx, mid, ai.TaskEmbed, fp))
	r.NoError(w.RunOnce(ctx))

	gen, err := gens.FindOrCreateBuilding(ctx, fp, embedCfg().Dimension)
	r.NoError(err)
	r.Equal(1, embeddedCount(t, d, gen.ID), "first run is net-new")

	// Re-enqueue the same (media, fp) and run again. The mapping row
	// is overwritten, so the per-generation embedded_count must NOT
	// double — replacement is zero-delta.
	r.NoError(q.Enqueue(ctx, mid, ai.TaskEmbed, fp))
	r.NoError(w.RunOnce(ctx))

	r.Equal(1, embeddedCount(t, d, gen.ID), "replacement does not bump embedded_count")
	// The mapping table still has exactly one row for this (gen, media).
	var n int
	r.NoError(d.ReadDB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM media_embedding_ids WHERE generation_id=? AND media_id=?`,
		gen.ID, mid,
	).Scan(&n))
	r.Equal(1, n)
}

func TestWorker_NoPreviewSkips(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	d := testutil.OpenTestDB(t)
	owner := testutil.SeedOwner(t, d.WriteDB(), "local", "alice")
	mid := testutil.SeedPhoto(t, d.WriteDB(), owner, "p1")
	resolver := &fakeResolver{defaultStatus: "no_preview"}
	client := &fakeEmbedClient{vectors: dim768N(1)}
	w, q, _ := newTestWorker(t, d, resolver, client, &recordingEmitter{})

	fp := embedFP()
	r.NoError(q.Enqueue(ctx, mid, ai.TaskEmbed, fp))
	r.NoError(w.RunOnce(ctx))

	// ai_skipped row recorded with reason='no_preview'.
	skipR := skipped.NewRepo(d.WriteDB(), d.ReadDB())
	reason, found, err := skipR.Get(ctx, mid, ai.TaskEmbed)
	r.NoError(err)
	r.True(found, "no_preview must record an ai_skipped row")
	r.Equal("no_preview", reason)

	// Job marked done — no future re-runs against this media.
	r.Equal("done", jobStatus(t, d, mid, ai.TaskEmbed))
	// Embed client is never called for the no_preview branch.
	r.EqualValues(0, client.calls.Load())
}

func TestWorker_PendingThumbBlocks(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	d := testutil.OpenTestDB(t)
	owner := testutil.SeedOwner(t, d.WriteDB(), "local", "alice")
	mid := testutil.SeedPhoto(t, d.WriteDB(), owner, "p1")
	resolver := &fakeResolver{defaultStatus: "pending"}
	client := &fakeEmbedClient{vectors: dim768N(1)}
	w, q, _ := newTestWorker(t, d, resolver, client, &recordingEmitter{})

	fp := embedFP()
	r.NoError(q.Enqueue(ctx, mid, ai.TaskEmbed, fp))
	r.NoError(w.RunOnce(ctx))

	r.Equal("blocked", jobStatus(t, d, mid, ai.TaskEmbed))
	// Counters reflect one blocked job for the embed task.
	c, err := q.Counters(ctx, ai.TaskEmbed)
	r.NoError(err)
	r.Equal(1, c.Blocked)
	r.EqualValues(0, client.calls.Load())
}

// TestWorker_PartialFailureRerunsSingles asserts the F1 simplification:
// when the embeddings endpoint returns fewer vectors than were sent
// (a "partial failure" the client itself will reject as malformed in
// practice), the worker marks every claim in the affected batch failed
// with a transient kind. F2's per-index re-run is deliberately deferred.
func TestWorker_PartialFailureRerunsSingles(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	d := testutil.OpenTestDB(t)
	owner := testutil.SeedOwner(t, d.WriteDB(), "local", "alice")
	mids := []string{
		testutil.SeedPhoto(t, d.WriteDB(), owner, "p1"),
		testutil.SeedPhoto(t, d.WriteDB(), owner, "p2"),
		testutil.SeedPhoto(t, d.WriteDB(), owner, "p3"),
	}
	resolver := &fakeResolver{defaultJPEG: mockJPEG, defaultStatus: "ready"}
	// Server returns only 2 vectors when 3 were requested. The worker
	// detects the mismatch and falls back to MarkFailed-all (F1 policy).
	client := &fakeEmbedClient{vectors: dim768N(3), vectorsToReturn: 2}
	w, q, gens := newTestWorker(t, d, resolver, client, &recordingEmitter{})

	fp := embedFP()
	for _, m := range mids {
		r.NoError(q.Enqueue(ctx, m, ai.TaskEmbed, fp))
	}

	r.NoError(w.RunOnce(ctx))

	// All three jobs are marked failed; no mappings written.
	gen, err := gens.FindOrCreateBuilding(ctx, fp, embedCfg().Dimension)
	r.NoError(err)
	r.Equal(0, embeddedCount(t, d, gen.ID), "no mappings on partial failure (F1 policy)")
	for _, m := range mids {
		r.Equal("failed", jobStatus(t, d, m, ai.TaskEmbed))
		r.False(mappingExists(t, d, gen.ID, m), "no mapping for %s on partial failure", m)
	}
}

// TestWorker_TransientErrorMarksAllFailed exercises the full-batch
// failure path: the embeddings endpoint returns a transient error, so
// every claim in the batch is marked failed with the transient kind
// and zero mappings are written.
func TestWorker_TransientErrorMarksAllFailed(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	d := testutil.OpenTestDB(t)
	owner := testutil.SeedOwner(t, d.WriteDB(), "local", "alice")
	mid := testutil.SeedPhoto(t, d.WriteDB(), owner, "p1")
	resolver := &fakeResolver{defaultJPEG: mockJPEG, defaultStatus: "ready"}
	client := &fakeEmbedClient{err: errors.New("HTTP 500: boom")}
	w, q, gens := newTestWorker(t, d, resolver, client, &recordingEmitter{})

	fp := embedFP()
	r.NoError(q.Enqueue(ctx, mid, ai.TaskEmbed, fp))
	r.NoError(w.RunOnce(ctx))

	gen, err := gens.FindOrCreateBuilding(ctx, fp, embedCfg().Dimension)
	r.NoError(err)
	r.Equal(0, embeddedCount(t, d, gen.ID))
	r.Equal("failed", jobStatus(t, d, mid, ai.TaskEmbed))
}
