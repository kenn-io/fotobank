package embedding

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/wesm/fotobank/internal/ai"
	"github.com/wesm/fotobank/internal/ai/imginput/encode"
	"github.com/wesm/fotobank/internal/ai/jobs"
	"github.com/wesm/fotobank/internal/ai/skipped"
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
type ClientIface interface {
	EmbedImages(ctx context.Context, jpegs [][]byte) ([][]float32, error)
}

// Compile-time check: *Client satisfies ClientIface. If the client's
// EmbedImages signature drifts (e.g. an extra arg) this assertion
// catches it at compile time rather than at the worker's first call.
var _ ClientIface = (*Client)(nil)

// EventEmitter is the post-commit notification hook. F1 ships with a
// no-op default; the real bus is wired up in Task P1 once the event
// names are defined.
type EventEmitter interface {
	EmitAIEmbedCompleted(mediaID string, fingerprint string)
	EmitAIEmbedFailed(mediaID string, fingerprint string)
}

// NoopEmitter satisfies EventEmitter without doing anything. The
// production wiring (Task P1) replaces it with a real bus; the
// no-op keeps the worker testable and avoids nil-checks on the hot
// path.
type NoopEmitter struct{}

// EmitAIEmbedCompleted is a no-op.
func (NoopEmitter) EmitAIEmbedCompleted(_ string, _ string) {}

// EmitAIEmbedFailed is a no-op.
func (NoopEmitter) EmitAIEmbedFailed(_ string, _ string) {}

// WorkerDeps is the fully-wired dependency set the worker requires.
// Construct via NewWorker — there is no zero-value worker.
type WorkerDeps struct {
	Q        *jobs.Queue
	Gens     *Generations
	Mapping  *Mapping
	Client   ClientIface
	Resolver PreviewResolver
	Cfg      ai.EmbedConfig
	Events   EventEmitter
	DB       *sql.DB // writer pool — used to bundle per-batch writes in one tx.
	Skipped  *skipped.Repo
}

// Worker batches pending TaskEmbed jobs into one /v1/embeddings call
// per cycle, persists vectors via Mapping into the active building
// generation, and updates ai_jobs in lockstep.
//
// Concurrency: RunOnce is single-threaded. F3 will spawn N parallel
// RunOnce goroutines once the run loop and housekeeping promotion
// land; until then a single worker drains the queue serially.
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

// prepared captures the result of one resolver+encode pass for one
// claim. Stored positionally so a partial failure can still attribute
// per-claim outcomes when the encode step rejects only some entries.
type prepared struct {
	claim  jobs.Claim
	jpeg   []byte
	status string
	err    error
}

// encoded names a claim that survived the resolve+encode preflight and
// is queued for the batched /v1/embeddings call.
type encoded struct {
	claim jobs.Claim
	body  []byte
}

// process runs one claim batch through the six-step pipeline:
//
//  1. Resolve preview JPEGs in parallel.
//  2. Classify per the §6.4 step-2 table (skip/block/encode/error).
//  3. Resolve the target generation by fingerprint.
//  4. Issue one batched /v1/embeddings call.
//  5. On partial failure, fall back to MarkFailed-all (F2 will refine).
//  6. Persist mappings + ai_jobs status updates in one tx.
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

	// Step 3: resolve the target generation. All claims in a batch
	// share a fingerprint because Enqueue supersedes any in-flight job
	// when fp changes (queue.go:Enqueue's tx) and ClaimBatch only
	// returns task='embed' rows that survived that supersession. The
	// fingerprint comes from the claim row itself (queue persisted it
	// at Enqueue time), not from cfg — so a stale cfg can't poison the
	// generation registry.
	fpStr := ready[0].claim.Fingerprint
	fp, err := parseFingerprint(fpStr)
	if err != nil {
		// Defensive: a malformed fp on a working row is an invariant
		// violation, but we still mark every claim failed so the queue
		// drains rather than re-claiming forever.
		for _, e := range ready {
			_ = w.d.Q.MarkFailed(ctx, e.claim.JobID, e.claim.ClaimedAt, ai.ErrKindMalformed, "parse fingerprint: "+err.Error())
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

	// Step 4: issue one batched /v1/embeddings call.
	jpegs := make([][]byte, len(ready))
	for i, e := range ready {
		jpegs[i] = e.body
	}
	vectors, callErr := w.d.Client.EmbedImages(ctx, jpegs)
	if callErr != nil {
		// Full-batch failure: classify once, mark every claim failed
		// with the same kind. Per-claim attribution is not meaningful
		// because the request body is the same for all of them.
		kind := classifyEmbedErr(callErr)
		for _, e := range ready {
			_ = w.d.Q.MarkFailed(ctx, e.claim.JobID, e.claim.ClaimedAt, kind, callErr.Error())
			w.d.Events.EmitAIEmbedFailed(e.claim.MediaID, fpStr)
		}
		return nil
	}

	// Step 5: partial failure shortcut — F1 simplification. The client
	// validates response length before returning, so this is mostly a
	// belt-and-braces guard, but if a future client backend returns
	// fewer vectors than requested we mark all of them failed
	// (transient) and let the next sweep re-claim them as singles.
	// F2 will replace this with per-index attribution.
	if len(vectors) != len(ready) {
		for _, e := range ready {
			_ = w.d.Q.MarkFailed(ctx, e.claim.JobID, e.claim.ClaimedAt, ai.ErrKindTransient,
				fmt.Sprintf("partial response: got %d vectors, want %d", len(vectors), len(ready)))
			w.d.Events.EmitAIEmbedFailed(e.claim.MediaID, fpStr)
		}
		return nil
	}

	// Step 6: persist mappings + status updates in one tx so a crash
	// between the WriteVectorTx and the MarkDone never leaves a
	// mapping without a 'done' job (or vice versa). embedded_count is
	// bumped by the net-new delta inside the same tx — replacements
	// contribute zero, matching Mapping.WriteVectorTx's contract.
	if err := w.commitBatch(ctx, gen, ready, vectors); err != nil {
		// commitBatch's failures all leave the rows in 'working' so
		// the next claim sweep recovers them. Don't double-mark.
		return fmt.Errorf("commit batch: %w", err)
	}

	// Step 7: emit one completion event per successful claim. The
	// event bus is async-best-effort; emitting after Commit means a
	// listener sees a row that already exists in the DB.
	for _, e := range ready {
		w.d.Events.EmitAIEmbedCompleted(e.claim.MediaID, fpStr)
	}
	return nil
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
func (w *Worker) classify(ctx context.Context, out []prepared) ([]encoded, error) {
	var ready []encoded
	for _, p := range out {
		switch {
		case p.err != nil:
			// Resolver failures are transport-level: the media row
			// missing or the storage read failing. MissingAIInput is
			// the established kind for "the worker couldn't get the
			// bytes it needed".
			if err := w.d.Q.MarkFailed(ctx, p.claim.JobID, p.claim.ClaimedAt,
				ai.ErrKindMissingAIInput, p.err.Error()); err != nil {
				return nil, fmt.Errorf("mark failed (resolver): %w", err)
			}
			w.d.Events.EmitAIEmbedFailed(p.claim.MediaID, p.claim.Fingerprint)
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
			// Re-encode the preview into the embed-task input profile.
			// The model selects edge length at boot via the modality
			// probe; cfg.InputEdge is the validated value.
			body, err := encode.EncodeEmbed(p.jpeg, w.d.Cfg.InputEdge)
			if err != nil {
				if mfErr := w.d.Q.MarkFailed(ctx, p.claim.JobID, p.claim.ClaimedAt,
					ai.ErrKindMalformed, "encode embed input: "+err.Error()); mfErr != nil {
					return nil, fmt.Errorf("mark failed (encode): %w", mfErr)
				}
				w.d.Events.EmitAIEmbedFailed(p.claim.MediaID, p.claim.Fingerprint)
				continue
			}
			ready = append(ready, encoded{claim: p.claim, body: body})
		default:
			// Unknown thumb_status — treat as missing input rather than
			// silently dropping the job. The chat worker handles this
			// the same way.
			if err := w.d.Q.MarkFailed(ctx, p.claim.JobID, p.claim.ClaimedAt,
				ai.ErrKindMissingAIInput, "unknown thumb_status: "+p.status); err != nil {
				return nil, fmt.Errorf("mark failed (unknown status): %w", err)
			}
			w.d.Events.EmitAIEmbedFailed(p.claim.MediaID, p.claim.Fingerprint)
		}
	}
	return ready, nil
}

// commitBatch writes every (mediaID, vector) mapping, marks every
// claim done, and bumps embedded_count by the net-new delta — all in
// one transaction. A rollback unwinds the mappings and leaves the
// rows in 'working' for the lease sweep to recover.
func (w *Worker) commitBatch(ctx context.Context, gen Row, ready []encoded, vectors [][]float32) error {
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
