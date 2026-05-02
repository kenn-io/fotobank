package embedding

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/wesm/fotobank/internal/ai"
	"github.com/wesm/fotobank/internal/ai/failures"
	"github.com/wesm/fotobank/internal/ai/imginput/encode"
	"github.com/wesm/fotobank/internal/ai/jobs"
	"github.com/wesm/fotobank/internal/ai/skipped"
	"github.com/wesm/fotobank/internal/obs"
)

// PreviewResolver is the minimal surface the embed worker needs from
// imginput: fetch (jpeg, thumbStatus, err) for a media id. Defined as
// an interface so tests can substitute an in-memory stub without a
// real storage.Store. The production implementation is *imginput.Resolver.
type PreviewResolver interface {
	ResolvePreviewJPEG(ctx context.Context, mediaID string) ([]byte, string, error)
}

// ClientIface is the minimal surface the embed worker needs from the
// embeddings HTTP client. Tests substitute a fake that returns canned
// vectors; the production impl is *embedding.Client.
//
// The model and dimension parameters are the per-call values the
// worker passes from the claim's fingerprint.ModelID and the matched
// generation row's Dimension — see worker.processGroup. Routing per
// claim rather than per worker is what keeps mid-rollout batches
// (jobs claimed under both the prior and the new fingerprint) honest:
// a stale-fp claim must validate against its own dimension, not the
// worker's currently-configured one.
type ClientIface interface {
	EmbedImages(ctx context.Context, model string, dimension int, jpegs [][]byte) ([][]float32, error)
}

// Compile-time check: *Client satisfies ClientIface. If the client's
// EmbedImages signature drifts (e.g. an extra arg) this assertion
// catches it at compile time rather than at the worker's first call.
var _ ClientIface = (*Client)(nil)

// QueueIface is the subset of *jobs.Queue the embed worker calls. The
// interface exists so tests can inject a one-shot fault (e.g. a queue
// that fails ClaimBatch the first time and succeeds after) without
// having to fault the underlying SQLite handle. Production wires
// *jobs.Queue, which already satisfies the interface.
type QueueIface interface {
	ClaimBatch(ctx context.Context, task ai.Task, n int) ([]jobs.Claim, error)
	PromoteThumbReadyBlocked(ctx context.Context, task ai.Task) (int, error)
	MarkFailed(ctx context.Context, jobID string, claimedAt time.Time, kind ai.LastErrorKind, errMsg string) error
	MarkDone(ctx context.Context, jobID string, claimedAt time.Time) error
	MarkBlocked(ctx context.Context, jobID string, claimedAt time.Time, reason string) error
}

// Compile-time check: *jobs.Queue satisfies QueueIface.
var _ QueueIface = (*jobs.Queue)(nil)

// EventEmitter is the post-commit notification hook for the embed
// pipeline. The two worker hooks fire from the worker's commit /
// failure paths; the three lifecycle hooks fire from the generations
// registry (created / retired) and the activator (activated). The
// production adapter is httpapi.AIEmbedEvents — a thin wrapper that
// binds the configured stub-mode principal and forwards onto the
// SSE EventBus.
//
// Methods are principal-free because the worker, activator, and
// generations registry don't thread a principal through their hot
// paths; v1 is single-principal so the adapter binds it once.
type EventEmitter interface {
	// EmitAIEmbedCompleted fires from worker.processGroup once per
	// claim that lands a successful mapping. fingerprint is the
	// canonical Fingerprint.String() value the claim was processed
	// under.
	EmitAIEmbedCompleted(mediaID string, fingerprint string)
	// EmitAIEmbedFailed fires from worker.recordTerminalFailure after
	// MarkFailed succeeds. errorKind is the string form of
	// ai.LastErrorKind ("transient", "provider_4xx", "malformed", …).
	EmitAIEmbedFailed(mediaID string, fingerprint string, errorKind string)
	// EmitAIEmbedGenerationCreated fires when FindOrCreateBuilding
	// inserts a new embedding_generations row. The fast-path lookup
	// (an existing row for this fingerprint) does not emit.
	EmitAIEmbedGenerationCreated(generationID int64, fingerprint string)
	// EmitAIEmbedGenerationActivated fires when the activator promotes
	// a building generation to active. generationID is the row id and
	// fingerprint is the canonical Fingerprint.String() value the row
	// was created under, so listeners can route by either key.
	EmitAIEmbedGenerationActivated(generationID int64, fingerprint string)
	// EmitAIEmbedGenerationRetired fires from Promote /
	// PromoteFromBuilding when the retire-prior-active UPDATE actually
	// changed a row (RowsAffected > 0). "No prior active" — the normal
	// first-promotion case — does not emit because there's no retired
	// row to announce.
	EmitAIEmbedGenerationRetired(generationID int64, fingerprint string)
}

// NoopEmitter satisfies EventEmitter without doing anything. Useful
// for tests that don't want to assert on event emission and for
// embedding-only deployments that haven't wired an EventBus yet.
type NoopEmitter struct{}

// EmitAIEmbedCompleted is a no-op.
func (NoopEmitter) EmitAIEmbedCompleted(_ string, _ string) {}

// EmitAIEmbedFailed is a no-op.
func (NoopEmitter) EmitAIEmbedFailed(_ string, _ string, _ string) {}

// EmitAIEmbedGenerationCreated is a no-op.
func (NoopEmitter) EmitAIEmbedGenerationCreated(_ int64, _ string) {}

// EmitAIEmbedGenerationActivated is a no-op.
func (NoopEmitter) EmitAIEmbedGenerationActivated(_ int64, _ string) {}

// EmitAIEmbedGenerationRetired is a no-op.
func (NoopEmitter) EmitAIEmbedGenerationRetired(_ int64, _ string) {}

// WorkerDeps is the fully-wired dependency set the worker requires.
// Construct via NewWorker — there is no zero-value worker.
type WorkerDeps struct {
	Q        QueueIface
	Gens     *Generations
	Mapping  *Mapping
	Client   ClientIface
	Resolver PreviewResolver
	Cfg      ai.EmbedConfig
	Events   EventEmitter
	DB       *sql.DB // writer pool — used to bundle per-batch writes in one tx.
	Skipped  *skipped.Repo
	// Failures records terminal failure rows alongside MarkFailed so the
	// AI panel and gap-scan repair queries can see persistent embed
	// failures keyed by (media, task, fingerprint). On a successful
	// embed the worker clears any prior row in the same tx that writes
	// the mapping. Mirrors the chat worker's failure surface.
	Failures *failures.Repo
	// Metrics, when non-nil, receives the per-batch AIEmbedBatchSize
	// observation each time processGroup issues a /v1/embeddings call.
	// Nil-safe — every metric emit is guarded so tests and embedding-
	// only deployments without an observability registry stay terse.
	Metrics *obs.Metrics
}

// Worker batches pending TaskEmbed jobs into one /v1/embeddings call
// per cycle, persists vectors via Mapping into the active building
// generation, and updates ai_jobs in lockstep.
//
// Concurrency: RunOnce is single-threaded. Run is the long-running
// loop driver — see Run for the cancellation contract. The wiring in
// internal/cli/server.go (Task S1) instantiates Cfg.WorkerConcurrency
// Run goroutines off the same Queue, all sharing this Worker shape;
// generation targeting + lease leases are designed for that fan-out.
type Worker struct{ d WorkerDeps }

// NewWorker constructs a Worker. The caller is responsible for
// supplying a fully-populated WorkerDeps; missing fields cause a
// nil-deref at the first RunOnce call. Events defaults to NoopEmitter
// when left nil so the simplest test wiring stays terse.
func NewWorker(d WorkerDeps) *Worker {
	if d.Events == nil {
		d.Events = NoopEmitter{}
	}
	return &Worker{d: d}
}

// RunOnce drains pending TaskEmbed jobs in BatchSize-sized claim
// cycles. Returns nil when no more pending jobs are claimable. Errors
// classified per-claim do not abort the loop; only infrastructure
// failures (claim SQL, transaction begin/commit) bubble up.
func (w *Worker) RunOnce(ctx context.Context) error {
	for {
		batch, err := w.d.Q.ClaimBatch(ctx, ai.TaskEmbed, w.d.Cfg.BatchSize)
		if err != nil {
			return fmt.Errorf("claim batch: %w", err)
		}
		if len(batch) == 0 {
			return nil
		}
		if err := w.process(ctx, batch); err != nil {
			return err
		}
	}
}

// Run is the long-running loop driver: each tick it promotes any
// blocked-by-thumb embed jobs whose source media has reached
// thumb_status='ready', then drains the claim queue until empty
// before sleeping on IdlePoll.
//
// Returns nil on ctx cancellation (graceful shutdown). The error
// posture splits queue-side errors from process-side errors:
//
//   - Queue/SQL errors from PromoteThumbReadyBlocked and ClaimBatch
//     are logged and the inner drain loop breaks back out to the
//     ticker so the next tick re-evaluates from scratch. The queue
//     is the source of truth; a one-off contention spike must not
//     tear the worker down.
//   - Errors from process (the worker logic itself) propagate. Those
//     usually indicate programming bugs, and a continue-and-log loop
//     would mask them indefinitely. The chat worker takes a similar
//     posture (see internal/ai/worker/worker.go::Run).
//
// The drain loop ensures a backlog enqueued between ticks is fully
// processed in the current tick rather than leaked across IdlePoll
// cycles — critical when BatchSize doesn't cover the whole queue
// and a single ticker fire would otherwise process at most one
// batch.
//
// The cancellation check on every error keeps a deliberate shutdown
// from being misclassified as either kind.
func (w *Worker) Run(ctx context.Context) error {
	t := time.NewTicker(w.d.Cfg.IdlePoll)
	defer t.Stop()
	for {
		// Drain inner loop: keep promoting + claiming + processing
		// until either the queue is empty or a process error escapes.
		// Queue errors break out to the outer ticker so the next tick
		// retries from scratch.
		for {
			// Promote any rows that the chat-style "thumb blocked"
			// sweep would otherwise leave parked. The embed worker
			// owns its own task's promotion because the chat worker
			// only iterates over its configured task (tag or caption)
			// — there is no central housekeeping site that knows
			// about ai.TaskEmbed.
			if _, err := w.d.Q.PromoteThumbReadyBlocked(ctx, ai.TaskEmbed); err != nil &&
				!errors.Is(err, context.Canceled) {
				slog.Default().Warn("embedding worker promote thumb-ready blocked", "err", err)
			}

			batch, claimErr := w.d.Q.ClaimBatch(ctx, ai.TaskEmbed, w.d.Cfg.BatchSize)
			if claimErr != nil {
				if errors.Is(claimErr, context.Canceled) {
					return nil
				}
				slog.Default().Warn("embedding worker claim batch", "err", claimErr)
				break
			}
			if len(batch) == 0 {
				break
			}
			if perr := w.process(ctx, batch); perr != nil {
				if errors.Is(perr, context.Canceled) {
					return nil
				}
				return fmt.Errorf("process: %w", perr)
			}
		}

		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// prepared captures the result of one resolver+encode pass for one
// claim. Stored positionally so a partial failure can still attribute
// per-claim outcomes when the encode step rejects only some entries.
type prepared struct {
	claim  jobs.Claim
	jpeg   []byte
	status string
	err    error
}

// encoded names a claim that survived the resolve preflight and is
// queued for the per-fingerprint encode + /v1/embeddings call. The
// raw preview JPEG is carried through here rather than re-encoded
// upfront because the encode parameters (specifically the edge size)
// are derived from the claim's fingerprint, not from cfg — see
// processGroup. Two claims that share a media id but live under
// different fingerprints would otherwise need their own encode pass
// each anyway, so deferring saves one call when they don't.
type encoded struct {
	claim   jobs.Claim
	preview []byte // raw preview JPEG, encoded per-fp inside processGroup
}

// process runs one claim batch through the six-step pipeline:
//
//  1. Resolve preview JPEGs in parallel.
//  2. Classify per the §6.4 step-2 table (skip/block/encode/error).
//  3. Partition the encoded claims by fingerprint and process each
//     fingerprint group separately (resolve generation → issue
//     /v1/embeddings → commit batch).
//
// Errors raised here are limited to infrastructure failures — per-claim
// outcomes (skipped, blocked, failed) are surfaced via ai_jobs / ai_skipped
// rows, not return values.
func (w *Worker) process(ctx context.Context, batch []jobs.Claim) error {
	out := w.resolveAll(ctx, batch)

	// Step 2: classify each prepared entry. Successful ready claims are
	// added to `ready`; everything else is finalised inline (MarkFailed,
	// MarkBlocked, ai_skipped + MarkDone).
	ready, err := w.classify(ctx, out)
	if err != nil {
		return err
	}
	if len(ready) == 0 {
		return nil
	}

	// Step 3: partition by fingerprint. Each ai_jobs row carries its
	// own fingerprint; in steady state all claims in one batch share
	// one (Enqueue's tx supersedes prior in-flight jobs on fp change),
	// but a queue with two active fingerprints — e.g. mid-rollout when
	// an operator just changed cfg.AI.Embed.Model — will hand back a
	// mixed batch. Each group must be routed to its own generation row,
	// and each becomes its own /v1/embeddings call so vectors are
	// validated against the right input set.
	groups := partitionByFingerprint(ready)
	for fpStr, group := range groups {
		if perr := w.processGroup(ctx, fpStr, group); perr != nil {
			return perr
		}
	}
	return nil
}

// partitionByFingerprint groups encoded claims by ai_jobs.fingerprint.
// Iteration order over the returned map is non-deterministic — the
// caller must not depend on group ordering.
func partitionByFingerprint(ready []encoded) map[string][]encoded {
	groups := make(map[string][]encoded)
	for _, e := range ready {
		groups[e.claim.Fingerprint] = append(groups[e.claim.Fingerprint], e)
	}
	return groups
}

// processGroup runs the per-fingerprint pipeline tail: parse the fp,
// derive the encode edge from fp.InputProfile, encode every preview
// for this fp's edge, resolve the building generation, issue one
// batched embeddings call against fp.ModelID, and commit mappings +
// status in a single tx. Returns only on infrastructure failures —
// per-claim outcomes are surfaced via ai_jobs / ai_skipped rows.
//
// Routing the model and the encode edge per fingerprint is what keeps
// mid-rollout batches honest: a claim under fpV1 (model=A, edge=384)
// and a claim under fpV2 (model=B, edge=512) can land in the same
// ClaimBatch, and each must hit the right endpoint with the right
// pixel input — using cfg.Model / cfg.InputEdge for both would land
// the fpV1 vector in fpV2's coordinate system, breaking search.
func (w *Worker) processGroup(ctx context.Context, fpStr string, group []encoded) error {
	fp, err := parseFingerprint(fpStr)
	if err != nil {
		// Defensive: a malformed fp on a working row is an invariant
		// violation, but we still mark every claim in the group failed
		// so the queue drains rather than re-claiming forever.
		// recordTerminalFailure skips the ai_failures row for a
		// zero-fp — see its docstring.
		for _, e := range group {
			w.recordTerminalFailure(ctx, e.claim, fp, ai.ErrKindMalformed, "parse fingerprint: "+err.Error())
		}
		return nil
	}

	// Derive the encode edge from the claim's InputProfile. A failure
	// here means the fp is structurally well-formed (three pipe-
	// separated parts) but the InputProfile string doesn't match the
	// canonical "jpeg-{N}-q85-metadata-stripped-embed-v1" shape. Treat
	// as malformed and drain the group.
	edge, err := EdgeFromInputProfile(fp.InputProfile)
	if err != nil {
		for _, e := range group {
			w.recordTerminalFailure(ctx, e.claim, fp, ai.ErrKindMalformed, "parse input profile: "+err.Error())
		}
		return nil
	}

	gen, err := w.d.Gens.FindOrCreateBuilding(ctx, fp, w.d.Cfg.Dimension)
	if err != nil {
		// Generation lookup is infrastructure: any failure here means
		// the next attempt should retry the same batch. Leave the rows
		// in 'working' and let the lease sweep recover them.
		return fmt.Errorf("resolve generation: %w", err)
	}

	// Encode every preview at this fingerprint's edge. A per-claim
	// encode failure is malformed (the bytes the resolver returned
	// won't decode/resize even at this edge); mark just that claim
	// failed and continue with the rest of the group. Empty group
	// after partitioning is a no-op.
	jpegs := make([][]byte, 0, len(group))
	survivors := make([]encoded, 0, len(group))
	for _, e := range group {
		body, err := encode.EncodeEmbed(e.preview, edge)
		if err != nil {
			w.recordTerminalFailure(ctx, e.claim, fp, ai.ErrKindMalformed,
				"encode embed input: "+err.Error())
			continue
		}
		jpegs = append(jpegs, body)
		survivors = append(survivors, e)
	}
	if len(survivors) == 0 {
		return nil
	}

	// Issue one batched /v1/embeddings call for this fingerprint
	// group, targeting the claim's model AND dimension. An empty
	// model in the fingerprint would fall back to cfg.Model on the
	// client side — but the worker has already validated
	// parseFingerprint's three parts, so ModelID is non-empty here
	// unless the fingerprint itself is "||...". Dimension comes from
	// the matched generation row so a stale-fp claim is validated
	// against its own dimension, not the currently-configured one.
	if w.d.Metrics != nil {
		// Observe pre-call: a downstream failure still tells the
		// operator how big the batch was when it failed.
		w.d.Metrics.AIEmbedBatchSize().Update(float64(len(jpegs)))
	}
	vectors, callErr := w.d.Client.EmbedImages(ctx, fp.ModelID, gen.Dimension, jpegs)
	if callErr != nil {
		// Full-batch failure: classify once, mark every survivor
		// failed with the same kind. Per-claim attribution is not
		// meaningful because the request body is the same for all of
		// them.
		kind := classifyEmbedErr(callErr)
		for _, e := range survivors {
			w.recordTerminalFailure(ctx, e.claim, fp, kind, callErr.Error())
		}
		return nil
	}

	// Partial failure shortcut — F1 simplification. The client
	// validates response length before returning, so this is mostly a
	// belt-and-braces guard, but if a future client backend returns
	// fewer vectors than requested we mark all of them failed
	// (transient) and let the next sweep re-claim them as singles.
	// F2 will replace this with per-index attribution.
	if len(vectors) != len(survivors) {
		msg := fmt.Sprintf("partial response: got %d vectors, want %d", len(vectors), len(survivors))
		for _, e := range survivors {
			w.recordTerminalFailure(ctx, e.claim, fp, ai.ErrKindTransient, msg)
		}
		return nil
	}

	// Persist mappings + status updates in one tx so a crash between
	// the WriteVectorTx and the MarkDone never leaves a mapping without
	// a 'done' job (or vice versa). embedded_count is bumped by the
	// net-new delta inside the same tx — replacements contribute zero,
	// matching Mapping.WriteVectorTx's contract. The same tx also
	// clears any prior ai_failures row for each successful (media, fp)
	// so a transient retry can't leave a stale failure visible.
	if err := w.commitBatch(ctx, gen, fp, survivors, vectors); err != nil {
		// commitBatch's failures all leave the rows in 'working' so
		// the next claim sweep recovers them. Don't double-mark.
		return fmt.Errorf("commit batch: %w", err)
	}

	// Emit one completion event per successful claim. The event bus is
	// async-best-effort; emitting after Commit means a listener sees a
	// row that already exists in the DB.
	for _, e := range survivors {
		w.d.Events.EmitAIEmbedCompleted(e.claim.MediaID, fpStr)
	}
	return nil
}

// recordTerminalFailure marks the claim failed and, on success,
// records an ai_failures row alongside it. The Failures.Record call
// and the failure-event emit are gated on MarkFailed succeeding so a
// reclaimed lease (jobs.ErrClaimLost) doesn't write a stale failure
// row under a claim some other worker now owns — that worker will
// reach its own terminal state and write its own row.
//
// fp may be the zero value when this is called from the malformed
// fingerprint branch in processGroup: the claim's fp string couldn't
// be parsed, so there is no canonical (model, prompt, profile) triple
// to key on. Skip Failures.Record in that case — an ai_failures row
// keyed under empty model/prompt/profile would silently merge with
// every other zero-fp failure across all media.
func (w *Worker) recordTerminalFailure(ctx context.Context, c jobs.Claim, fp ai.Fingerprint, kind ai.LastErrorKind, msg string) {
	if err := w.d.Q.MarkFailed(ctx, c.JobID, c.ClaimedAt, kind, msg); err != nil {
		if errors.Is(err, jobs.ErrClaimLost) {
			// The lease was reclaimed mid-flight — another worker now
			// owns the claim. Don't write a failure row under a claim
			// we no longer hold.
			return
		}
		slog.Default().Warn("embedding worker mark failed", "job", c.JobID, "err", err)
		return
	}
	if w.d.Failures != nil && fp != (ai.Fingerprint{}) {
		// attempt_count includes the just-failed run; mirrors
		// internal/ai/worker/worker.go's markFailed.
		if err := w.d.Failures.Record(ctx, c.MediaID, ai.TaskEmbed, fp, kind, msg, c.Attempts+1); err != nil {
			slog.Default().Warn("embedding worker record failure", "media", c.MediaID, "err", err)
		}
	}
	w.d.Events.EmitAIEmbedFailed(c.MediaID, fp.String(), string(kind))
}

// resolveAll fans out one ResolvePreviewJPEG goroutine per claim and
// collects the results positionally. The resolver is read-only and
// safe to call concurrently across goroutines (it reads through the
// ro pool and the storage.Store contract is concurrent-safe).
func (w *Worker) resolveAll(ctx context.Context, batch []jobs.Claim) []prepared {
	out := make([]prepared, len(batch))
	var wg sync.WaitGroup
	for i, c := range batch {
		wg.Go(func() {
			jpg, status, err := w.d.Resolver.ResolvePreviewJPEG(ctx, c.MediaID)
			out[i] = prepared{claim: c, jpeg: jpg, status: status, err: err}
		})
	}
	wg.Wait()
	return out
}

// classify walks the prepared entries and applies the §6.4 step-2
// table, returning the subset of claims that survived to the encode
// step. Side-effecting branches (skipped, blocked, failed) finalise
// the claim inline so the caller does not need to revisit them.
//
// Failure branches in this function call recordTerminalFailure so the
// ai_failures row is written in lockstep with the MarkFailed UPDATE.
// MarkFailed errors there are swallowed (the chat worker does the same):
// a transient SQL hiccup on the failure side leaves the row in 'working'
// for the lease sweep to recover, which is the correct fallback.
func (w *Worker) classify(ctx context.Context, out []prepared) ([]encoded, error) {
	var ready []encoded
	for _, p := range out {
		switch {
		case p.err != nil:
			// Resolver failures are transport-level: the media row
			// missing or the storage read failing. MissingAIInput is
			// the established kind for "the worker couldn't get the
			// bytes it needed".
			fp, _ := parseFingerprint(p.claim.Fingerprint)
			w.recordTerminalFailure(ctx, p.claim, fp, ai.ErrKindMissingAIInput, p.err.Error())
		case p.status == "no_preview":
			// The thumb pipeline determined this media has no usable
			// preview (typically a video). Record the skip and mark
			// the job done so the gap scanner doesn't re-enqueue it.
			if err := w.d.Skipped.Record(ctx, p.claim.MediaID, ai.TaskEmbed, "no_preview"); err != nil {
				return nil, fmt.Errorf("record skip: %w", err)
			}
			if err := w.d.Q.MarkDone(ctx, p.claim.JobID, p.claim.ClaimedAt); err != nil {
				return nil, fmt.Errorf("mark done (skipped): %w", err)
			}
		case p.status == "pending" || p.status == "working" || p.status == "failed":
			// Upstream thumb pipeline isn't done yet. Park the job;
			// PromoteThumbReadyBlocked re-enqueues it once the source
			// flips to 'ready'.
			reason := thumbBlockReason(p.status)
			if err := w.d.Q.MarkBlocked(ctx, p.claim.JobID, p.claim.ClaimedAt, reason); err != nil {
				return nil, fmt.Errorf("mark blocked: %w", err)
			}
		case p.status == "ready":
			// Defer the embed-input encode to processGroup so the edge
			// size comes from the claim's fingerprint (parsed out of
			// fp.InputProfile) rather than cfg.InputEdge. A claim under
			// a stale fingerprint must encode at that fingerprint's
			// edge or the resulting vector lives under the wrong
			// InputProfile and search-time queries miss it.
			ready = append(ready, encoded{claim: p.claim, preview: p.jpeg})
		default:
			// Unknown thumb_status — treat as missing input rather than
			// silently dropping the job. The chat worker handles this
			// the same way.
			fp, _ := parseFingerprint(p.claim.Fingerprint)
			w.recordTerminalFailure(ctx, p.claim, fp, ai.ErrKindMissingAIInput,
				"unknown thumb_status: "+p.status)
		}
	}
	return ready, nil
}

// commitBatch writes every (mediaID, vector) mapping, marks every
// claim done, clears any prior ai_failures row for (media, fp), and
// bumps embedded_count by the net-new delta — all in one transaction.
// A rollback unwinds every change and leaves the rows in 'working' for
// the lease sweep to recover.
//
// Clearing ai_failures inside the same tx that writes the mapping
// keeps the panel's view of "still failing" honest: a successful retry
// can never leave a stale failure visible, even if the process crashes
// between the mapping write and the cleanup.
func (w *Worker) commitBatch(ctx context.Context, gen Row, fp ai.Fingerprint, ready []encoded, vectors [][]float32) error {
	tx, err := w.d.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	totalDelta := 0
	for i, e := range ready {
		delta, werr := WriteVectorTx(ctx, tx, gen, e.claim.MediaID, vectors[i])
		if werr != nil {
			return fmt.Errorf("write vec %s: %w", e.claim.MediaID, werr)
		}
		totalDelta += delta
		if merr := markDoneTx(ctx, tx, e.claim.JobID, e.claim.ClaimedAt); merr != nil {
			return fmt.Errorf("mark done %s: %w", e.claim.JobID, merr)
		}
		if w.d.Failures != nil {
			if ferr := w.d.Failures.DeleteTx(ctx, tx, e.claim.MediaID, ai.TaskEmbed, fp); ferr != nil {
				return fmt.Errorf("clear failure %s: %w", e.claim.MediaID, ferr)
			}
		}
	}
	if totalDelta > 0 {
		if _, err := tx.ExecContext(ctx,
			`UPDATE embedding_generations SET embedded_count = embedded_count + ? WHERE id = ?`,
			totalDelta, gen.ID,
		); err != nil {
			return fmt.Errorf("inc embedded_count: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// markDoneTx is the in-tx variant of jobs.Queue.MarkDone. The queue
// package exposes WriteAndMarkDone which already wraps a single
// closure in a tx, but the embed worker needs to bundle N mapping
// writes + N MarkDone calls into one tx — so we issue the same UPDATE
// directly. The lease check (status='working' AND claimed_at=?) is
// preserved so a sweep that reclaimed the row mid-flight still rolls
// the whole tx back via the zero-rows-affected path.
func markDoneTx(ctx context.Context, tx *sql.Tx, jobID string, claimedAt time.Time) error {
	res, err := tx.ExecContext(ctx,
		`UPDATE ai_jobs SET status='done', completed_at=?, last_error=NULL, last_error_kind=NULL
		 WHERE id=? AND status='working' AND claimed_at=?`,
		time.Now().UTC(), jobID, claimedAt)
	if err != nil {
		return fmt.Errorf("mark done: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("mark done rows affected: %w", err)
	}
	if n == 0 {
		return jobs.ErrClaimLost
	}
	return nil
}

// thumbBlockReason maps the resolver's thumb_status into the canonical
// last_error string the queue's PromoteThumbReadyBlocked sweep keys on.
func thumbBlockReason(status string) string {
	switch status {
	case "pending":
		return jobs.ThumbBlockedPending
	case "working":
		return jobs.ThumbBlockedWorking
	case "failed":
		return jobs.ThumbBlockedFailed
	default:
		// Caller is expected to gate on the canonical statuses; if a
		// new state is added, the resolver's contract surfaces it
		// here and the operator sees an unfamiliar last_error string
		// rather than a silent miscategorisation.
		return "thumb_" + status
	}
}

// classifyEmbedErr maps client errors to the appropriate ai.ErrKind.
// Provider 4xx is permanent (operator must intervene); transient is
// retry-eligible; malformed sits between (config drift on the server
// side, retrying won't help but the cause is the response, not us).
func classifyEmbedErr(err error) ai.LastErrorKind {
	switch {
	case errors.Is(err, ErrProvider4xx):
		return ai.ErrKindProvider4xx
	case errors.Is(err, ErrMalformed):
		return ai.ErrKindMalformed
	default:
		return ai.ErrKindTransient
	}
}

// parseFingerprint reverses ai.Fingerprint.String — splits on the two
// '|' separators that String inserts. Used to rehydrate the fp from
// the ai_jobs.fingerprint column at process time, so the worker can
// hand a structured Fingerprint to the generations registry. A
// malformed string here means a row was inserted with a non-canonical
// fingerprint, which is an invariant violation worth surfacing.
//
// The middle segment (PromptVersion) is allowed to be empty — embed
// fingerprints always have it blank. A wrong number of separators
// (anything other than 2) is rejected.
func parseFingerprint(s string) (ai.Fingerprint, error) {
	parts := strings.Split(s, "|")
	if len(parts) != 3 {
		return ai.Fingerprint{}, fmt.Errorf("expected model|prompt|profile, got %q", s)
	}
	return ai.Fingerprint{
		ModelID:       parts[0],
		PromptVersion: parts[1],
		InputProfile:  parts[2],
	}, nil
}
