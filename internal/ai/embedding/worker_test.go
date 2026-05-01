package embedding_test

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/ai"
	"github.com/wesm/fotobank/internal/ai/embedding"
	"github.com/wesm/fotobank/internal/ai/failures"
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

// embedCfg is the minimal EmbedConfig the worker consumes. IdlePoll is
// short so the Run-loop test can observe a tick without padding the
// suite with multi-second waits.
func embedCfg() ai.EmbedConfig {
	return ai.EmbedConfig{
		Model:     "siglip2",
		Dimension: 768,
		InputEdge: 384,
		BatchSize: 32,
		IdlePoll:  10 * time.Millisecond,
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

// recordingEmitter captures EmitAIEmbedCompleted / EmitAIEmbedFailed /
// EmitAIEmbedGenerationActivated calls so happy-path tests can confirm
// the worker (or activator) fires the post-commit event for every
// successful claim or activation.
type recordingEmitter struct {
	completed atomic.Int32
	failed    atomic.Int32
	activated atomic.Int32
}

func (r *recordingEmitter) EmitAIEmbedCompleted(_ string, _ string) { r.completed.Add(1) }
func (r *recordingEmitter) EmitAIEmbedFailed(_ string, _ string)    { r.failed.Add(1) }
func (r *recordingEmitter) EmitAIEmbedGenerationActivated(_ int64, _ string) {
	r.activated.Add(1)
}

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
// queue, generations, mapping, skipped repo, failures repo, and config
// are constant.
func newTestWorker(
	t *testing.T,
	d *db.DB,
	resolver embedding.PreviewResolver,
	client embedding.ClientIface,
	emitter embedding.EventEmitter,
) (*embedding.Worker, *jobs.Queue, *embedding.Generations, *failures.Repo) {
	t.Helper()
	q := jobs.NewQueue(d.WriteDB(), d.ReadDB())
	gens := embedding.NewGenerations(d.WriteDB(), d.ReadDB())
	mapping := embedding.NewMapping(d.WriteDB())
	skipR := skipped.NewRepo(d.WriteDB(), d.ReadDB())
	failR := failures.NewRepo(d.WriteDB(), d.ReadDB())
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
		Failures: failR,
	})
	return w, q, gens, failR
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
	w, q, gens, _ := newTestWorker(t, d, resolver, client, emitter)

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
	w, q, gens, _ := newTestWorker(t, d, resolver, client, &recordingEmitter{})

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
	w, q, _, _ := newTestWorker(t, d, resolver, client, &recordingEmitter{})

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
	w, q, _, _ := newTestWorker(t, d, resolver, client, &recordingEmitter{})

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
	w, q, gens, _ := newTestWorker(t, d, resolver, client, &recordingEmitter{})

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

// TestWorker_RunDrainsQueueAndExitsOnCancel covers the F3 long-running
// loop: Run drains a queued job before the first idle tick fires and
// returns nil when the supplied context is cancelled. Verifies that
// the loop's cancellation path is the graceful one (no error returned)
// and that RunOnce-equivalent work proceeds before the ticker has any
// chance to fire.
func TestWorker_RunDrainsQueueAndExitsOnCancel(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	d := testutil.OpenTestDB(t)
	owner := testutil.SeedOwner(t, d.WriteDB(), "local", "alice")
	mid := testutil.SeedPhoto(t, d.WriteDB(), owner, "p1")
	resolver := &fakeResolver{defaultJPEG: mockJPEG, defaultStatus: "ready"}
	client := &fakeEmbedClient{vectors: dim768N(1), vectorsToReturn: -1}
	w, q, gens, _ := newTestWorker(t, d, resolver, client, &recordingEmitter{})

	fp := embedFP()
	r.NoError(q.Enqueue(ctx, mid, ai.TaskEmbed, fp))

	// Resolve the building generation up-front so the assertion can
	// check the mapping under the same gen the worker writes into.
	gen, err := gens.FindOrCreateBuilding(ctx, fp, embedCfg().Dimension)
	r.NoError(err)

	ctx2, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx2) }()

	r.Eventually(func() bool {
		return mappingExists(t, d, gen.ID, mid)
	}, 5*time.Second, 25*time.Millisecond, "Run loop should drain the queued job")

	cancel()
	select {
	case err := <-done:
		r.NoError(err, "Run should return nil on context cancel")
	case <-time.After(2 * time.Second):
		r.Fail("Run did not return after cancel within 2s")
	}
}

// TestWorker_RunPromotesThumbReadyBlocked covers the F3 housekeeping
// promotion: a job that was MarkBlocked because thumb_status was
// 'pending' must be re-promoted by the Run loop's pre-RunOnce sweep
// once the source media flips to 'ready'. The worker then drains the
// promoted row in the same loop iteration.
func TestWorker_RunPromotesThumbReadyBlocked(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	d := testutil.OpenTestDB(t)
	owner := testutil.SeedOwner(t, d.WriteDB(), "local", "alice")
	mid := testutil.SeedPhoto(t, d.WriteDB(), owner, "p1")
	// First pass: thumb_status='pending' so the worker parks the job.
	resolver := &fakeResolver{defaultStatus: "pending"}
	client := &fakeEmbedClient{vectors: dim768N(1), vectorsToReturn: -1}
	w, queue, gens, _ := newTestWorker(t, d, resolver, client, &recordingEmitter{})

	fp := embedFP()
	r.NoError(queue.Enqueue(ctx, mid, ai.TaskEmbed, fp))
	r.NoError(w.RunOnce(ctx))
	r.Equal("blocked", jobStatus(t, d, mid, ai.TaskEmbed))

	// Second pass: flip the resolver to 'ready' and the media row's
	// thumb_status to 'ready' so the promotion sweep matches. Then run
	// the long-running loop and assert the previously blocked job
	// drains through to 'done'.
	resolver.defaultStatus = "ready"
	resolver.defaultJPEG = mockJPEG
	_, err := d.WriteDB().ExecContext(ctx,
		`UPDATE media SET thumb_status='ready' WHERE id=?`, mid)
	r.NoError(err)

	gen, err := gens.FindOrCreateBuilding(ctx, fp, embedCfg().Dimension)
	r.NoError(err)

	ctx2, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx2) }()

	r.Eventually(func() bool {
		return mappingExists(t, d, gen.ID, mid)
	}, 5*time.Second, 25*time.Millisecond, "Run should promote thumb-ready blocked and drain")
	r.Equal("done", jobStatus(t, d, mid, ai.TaskEmbed))

	cancel()
	select {
	case err := <-done:
		r.NoError(err)
	case <-time.After(2 * time.Second):
		r.Fail("Run did not return after cancel within 2s")
	}
}

// perCallEmbedClient returns a fresh slice of vectors per call,
// indexed by call number. Used by tests where the worker is expected
// to issue more than one EmbedImages call per RunOnce — e.g. one call
// per fingerprint group.
type perCallEmbedClient struct {
	mu          sync.Mutex
	calls       atomic.Int32
	perCall     [][][]float32 // perCall[i] is the slice for the i-th call
	inputCounts []int         // recorded len(jpegs) per call, in call order
}

func (f *perCallEmbedClient) EmbedImages(_ context.Context, jpegs [][]byte) ([][]float32, error) {
	idx := int(f.calls.Add(1)) - 1
	f.mu.Lock()
	f.inputCounts = append(f.inputCounts, len(jpegs))
	f.mu.Unlock()
	if idx >= len(f.perCall) {
		idx = len(f.perCall) - 1
	}
	return f.perCall[idx], nil
}

// TestWorker_BatchWithMixedFingerprintsRoutesToCorrectGenerations
// exercises the per-fingerprint partitioning in process. The realistic
// trigger is a mid-rollout window: fpV1 was the prior fingerprint and
// has been promoted to 'active'; fpV2 is the new fingerprint whose
// 'building' row is in flight. While that window is open, ClaimBatch
// can hand back rows for both fingerprints in a single call.
//
// Per the spec, every ai_jobs row carries its own fingerprint and the
// worker must route each to its own generation. The test enqueues two
// jobs under each fingerprint and asserts: one EmbedImages call fires
// per group, each fingerprint resolves to its own generation row, and
// every media's mapping lands in the generation matching its claim's
// fingerprint.
func TestWorker_BatchWithMixedFingerprintsRoutesToCorrectGenerations(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	d := testutil.OpenTestDB(t)
	owner := testutil.SeedOwner(t, d.WriteDB(), "local", "alice")

	fpV1 := ai.Fingerprint{ModelID: "siglip2", InputProfile: "embed-v1"}
	fpV2 := ai.Fingerprint{ModelID: "siglip2", InputProfile: "embed-v2"}

	v1Mids := []string{
		testutil.SeedPhoto(t, d.WriteDB(), owner, "v1-a"),
		testutil.SeedPhoto(t, d.WriteDB(), owner, "v1-b"),
	}
	v2Mids := []string{
		testutil.SeedPhoto(t, d.WriteDB(), owner, "v2-a"),
		testutil.SeedPhoto(t, d.WriteDB(), owner, "v2-b"),
	}

	resolver := &fakeResolver{defaultJPEG: mockJPEG, defaultStatus: "ready"}
	// The worker issues one EmbedImages call per fingerprint group; the
	// per-call client returns 2 vectors per call so each group gets a
	// matching response length.
	client := &perCallEmbedClient{
		perCall: [][][]float32{dim768N(2), dim768N(2)},
	}
	w, q, gens, _ := newTestWorker(t, d, resolver, client, &recordingEmitter{})

	// Pre-create the v1 generation and promote it to 'active' so v2's
	// building row can coexist with it (the schema's
	// embedding_generations_one_building partial index allows at most
	// one building row at a time but does not constrain active ones).
	v1Gen, err := gens.FindOrCreateBuilding(ctx, fpV1, embedCfg().Dimension)
	r.NoError(err)
	r.NoError(gens.Promote(ctx, v1Gen.ID))

	for _, m := range v1Mids {
		r.NoError(q.Enqueue(ctx, m, ai.TaskEmbed, fpV1))
	}
	for _, m := range v2Mids {
		r.NoError(q.Enqueue(ctx, m, ai.TaskEmbed, fpV2))
	}

	r.NoError(w.RunOnce(ctx))

	// One EmbedImages call per fingerprint group, two inputs each.
	r.EqualValues(2, client.calls.Load(), "one call per fingerprint group")
	client.mu.Lock()
	r.Equal([]int{2, 2}, client.inputCounts, "each call carries exactly its group's inputs")
	client.mu.Unlock()

	// v1 stayed active (worker writes mappings to it without changing
	// its state); v2 is the building row created by the worker.
	active, err := gens.FindActive(ctx)
	r.NoError(err)
	r.NotNil(active)
	r.Equal(v1Gen.ID, active.ID)

	v2Gen, err := gens.FindOrCreateBuilding(ctx, fpV2, embedCfg().Dimension)
	r.NoError(err)
	r.NotEqual(v1Gen.ID, v2Gen.ID, "fingerprints must resolve to distinct generations")
	r.Equal(2, embeddedCount(t, d, v1Gen.ID), "v1 generation count")
	r.Equal(2, embeddedCount(t, d, v2Gen.ID), "v2 generation count")

	// Mapping rows route to the correct generation per claim's fingerprint.
	for _, m := range v1Mids {
		r.True(mappingExists(t, d, v1Gen.ID, m), "v1 media %s missing in v1 generation", m)
		r.False(mappingExists(t, d, v2Gen.ID, m), "v1 media %s leaked into v2 generation", m)
		r.Equal("done", jobStatus(t, d, m, ai.TaskEmbed))
	}
	for _, m := range v2Mids {
		r.True(mappingExists(t, d, v2Gen.ID, m), "v2 media %s missing in v2 generation", m)
		r.False(mappingExists(t, d, v1Gen.ID, m), "v2 media %s leaked into v1 generation", m)
		r.Equal("done", jobStatus(t, d, m, ai.TaskEmbed))
	}
}

// TestWorker_TerminalFailureRecordsAIFailureRow asserts the worker
// upserts an ai_failures row alongside MarkFailed when the embeddings
// endpoint reports a terminal failure. The row is keyed on the claim's
// fingerprint so the panel and gap-scan repair queries see it under
// the active fingerprint triple.
func TestWorker_TerminalFailureRecordsAIFailureRow(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	d := testutil.OpenTestDB(t)
	owner := testutil.SeedOwner(t, d.WriteDB(), "local", "alice")
	mid := testutil.SeedPhoto(t, d.WriteDB(), owner, "p1")
	resolver := &fakeResolver{defaultJPEG: mockJPEG, defaultStatus: "ready"}
	client := &fakeEmbedClient{err: errors.New("HTTP 500: boom")}
	w, q, _, failR := newTestWorker(t, d, resolver, client, &recordingEmitter{})

	fp := embedFP()
	r.NoError(q.Enqueue(ctx, mid, ai.TaskEmbed, fp))
	r.NoError(w.RunOnce(ctx))

	// ai_jobs row is failed AND a matching ai_failures row exists.
	r.Equal("failed", jobStatus(t, d, mid, ai.TaskEmbed))
	cnt, err := failR.CountForFingerprint(ctx, ai.TaskEmbed, fp)
	r.NoError(err)
	r.Equal(1, cnt, "ai_failures must carry one row for (mid, embed, fp)")

	row, found, err := failR.GetForFingerprint(ctx, mid, ai.TaskEmbed, fp)
	r.NoError(err)
	r.True(found)
	r.Equal(ai.ErrKindTransient, row.LastErrorKind)
	r.Contains(row.LastError, "boom")
	r.Equal(1, row.AttemptCount, "attempt_count includes the just-failed run")
}

// TestWorker_SuccessfulRetryClearsPriorFailureRow confirms the
// worker's success path deletes the ai_failures row for (media, fp) in
// the same tx that writes the mapping. Without this, a transient that
// later succeeds would leave a stale failure visible to the panel.
func TestWorker_SuccessfulRetryClearsPriorFailureRow(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	d := testutil.OpenTestDB(t)
	owner := testutil.SeedOwner(t, d.WriteDB(), "local", "alice")
	mid := testutil.SeedPhoto(t, d.WriteDB(), owner, "p1")
	resolver := &fakeResolver{defaultJPEG: mockJPEG, defaultStatus: "ready"}

	// First run: transient error → MarkFailed + ai_failures row written.
	failingClient := &fakeEmbedClient{err: errors.New("HTTP 500: first")}
	w, q, gens, failR := newTestWorker(t, d, resolver, failingClient, &recordingEmitter{})

	fp := embedFP()
	r.NoError(q.Enqueue(ctx, mid, ai.TaskEmbed, fp))
	r.NoError(w.RunOnce(ctx))
	cnt, err := failR.CountForFingerprint(ctx, ai.TaskEmbed, fp)
	r.NoError(err)
	r.Equal(1, cnt, "first run must have recorded a failure row")

	// Second run with a successful client. Re-enqueue (the failed job
	// is terminal) and swap the worker's client to one that returns
	// vectors.
	r.NoError(q.Enqueue(ctx, mid, ai.TaskEmbed, fp))

	successClient := &fakeEmbedClient{vectors: dim768N(1), vectorsToReturn: -1}
	w2 := embedding.NewWorker(embedding.WorkerDeps{
		Q:        jobs.NewQueue(d.WriteDB(), d.ReadDB()),
		Gens:     gens,
		Mapping:  embedding.NewMapping(d.WriteDB()),
		Client:   successClient,
		Resolver: resolver,
		Cfg:      embedCfg(),
		Events:   &recordingEmitter{},
		DB:       d.WriteDB(),
		Skipped:  skipped.NewRepo(d.WriteDB(), d.ReadDB()),
		Failures: failR,
	})
	r.NoError(w2.RunOnce(ctx))

	// Mapping written, retried job marked done, failure row cleared.
	// The original failed row is still in ai_jobs (terminal state, not
	// re-claimed); the retried row is a separate insert. Count-by-status
	// keeps the assertion robust to that split.
	gen, err := gens.FindOrCreateBuilding(ctx, fp, embedCfg().Dimension)
	r.NoError(err)
	r.True(mappingExists(t, d, gen.ID, mid), "successful retry must write mapping")

	var doneCount int
	r.NoError(d.ReadDB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM ai_jobs WHERE media_id=? AND task=? AND status='done'`,
		mid, string(ai.TaskEmbed),
	).Scan(&doneCount))
	r.Equal(1, doneCount, "exactly one done row for the retried claim")

	cnt, err = failR.CountForFingerprint(ctx, ai.TaskEmbed, fp)
	r.NoError(err)
	r.Equal(0, cnt, "successful retry must clear the prior ai_failures row")
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
	w, q, gens, _ := newTestWorker(t, d, resolver, client, &recordingEmitter{})

	fp := embedFP()
	r.NoError(q.Enqueue(ctx, mid, ai.TaskEmbed, fp))
	r.NoError(w.RunOnce(ctx))

	gen, err := gens.FindOrCreateBuilding(ctx, fp, embedCfg().Dimension)
	r.NoError(err)
	r.Equal(0, embeddedCount(t, d, gen.ID))
	r.Equal("failed", jobStatus(t, d, mid, ai.TaskEmbed))
}
