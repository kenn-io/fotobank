package worker

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/wesm/fotobank/internal/ai"
	"github.com/wesm/fotobank/internal/ai/ack"
	"github.com/wesm/fotobank/internal/ai/failures"
	"github.com/wesm/fotobank/internal/ai/gateway"
	"github.com/wesm/fotobank/internal/ai/jobs"
	"github.com/wesm/fotobank/internal/ai/parse"
	"github.com/wesm/fotobank/internal/ai/results"
	"github.com/wesm/fotobank/internal/ai/skipped"
	"github.com/wesm/fotobank/internal/owners"
)

// ImageResolver fetches the AI-input JPEG bytes for a media id, plus
// the underlying thumb_status so the worker can react to upstream
// readiness. Implementation: see Task G3 (real impl) and Task G2 stub.
type ImageResolver interface {
	// ResolveAndEncode returns (jpeg, thumbStatus, err).
	// thumbStatus is the source media's thumb_status — "ready",
	// "pending", "working", "no_preview", or "failed".
	ResolveAndEncode(ctx context.Context, mediaID string) ([]byte, string, error)
}

// AcknowledgedFn returns whether ack has been recorded for a principal.
type AcknowledgedFn func(ctx context.Context, p owners.Principal) (bool, error)

// OwnerOf maps mediaID to its owning principal. Implementation passes
// through internal/media.Repo.GetOwner; tests can use a fake.
type OwnerOf func(ctx context.Context, mediaID string) (owners.Principal, error)

// ProcessOutcome is what the per-task Process callback returns.
type ProcessOutcome struct {
	Tags    []parse.Tag
	Caption string
}

// ProcessFn turns gateway output into the persisted shape.
type ProcessFn func(rawText string) (ProcessOutcome, error)

// TagProcess parses tags from the VLM response.
func TagProcess(rawText string) (ProcessOutcome, error) {
	tags, err := parse.Tags(rawText)
	if err != nil {
		return ProcessOutcome{}, err
	}
	return ProcessOutcome{Tags: tags}, nil
}

// CaptionProcess parses and validates a caption.
func CaptionProcess(rawText string) (ProcessOutcome, error) {
	text, err := parse.Caption(rawText)
	if err != nil {
		return ProcessOutcome{}, err
	}
	return ProcessOutcome{Caption: text}, nil
}

// Config bundles worker dependencies.
type Config struct {
	Task           ai.Task
	Fingerprint    ai.Fingerprint
	PromptHash     string
	PromptText     string
	Gateway        gateway.VisionGateway
	Image          ImageResolver
	Queue          *jobs.Queue
	Results        *results.Repo
	Failures       *failures.Repo
	Skipped        *skipped.Repo
	Acknowledged   AcknowledgedFn
	OwnerOf        OwnerOf
	MaxJobAttempts int
	Process        ProcessFn
	Sem            *VisionSemaphore
	BatchSize      int
	PollInterval   time.Duration
	Logger         *slog.Logger
}

// Worker runs claim/lease iterations against ai_jobs.
type Worker struct {
	cfg Config
}

// New constructs a Worker with sane defaults for unset fields.
func New(cfg Config) *Worker {
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 4
	}
	if cfg.MaxJobAttempts <= 0 {
		cfg.MaxJobAttempts = 2
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 5 * time.Second
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.OwnerOf == nil {
		cfg.OwnerOf = func(_ context.Context, _ string) (owners.Principal, error) {
			return owners.Principal{}, errors.New("OwnerOf not configured")
		}
	}
	return &Worker{cfg: cfg}
}

// Run loops until ctx is canceled, calling RunOnce on each tick.
func (w *Worker) Run(ctx context.Context) error {
	t := time.NewTicker(w.cfg.PollInterval)
	defer t.Stop()
	for {
		if _, err := w.RunOnce(ctx); err != nil {
			w.cfg.Logger.Warn("ai worker tick failed", "task", w.cfg.Task, "err", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

// RunOnce claims and processes up to BatchSize jobs. Before claiming it
// promotes any blocked jobs whose blocker has cleared (acknowledgement
// recorded, source thumb now ready) so they re-enter the claim path.
func (w *Worker) RunOnce(ctx context.Context) (int, error) {
	if err := w.PromoteBlocked(ctx); err != nil {
		w.cfg.Logger.Warn("ai promote blocked failed", "task", w.cfg.Task, "err", err)
	}
	claims, err := w.cfg.Queue.ClaimBatch(ctx, w.cfg.Task, w.cfg.BatchSize)
	if err != nil {
		return 0, fmt.Errorf("claim: %w", err)
	}
	processed := 0
	for _, c := range claims {
		if err := w.handleOne(ctx, c); err != nil {
			w.cfg.Logger.Warn("ai job error", "job", c.JobID, "err", err)
		}
		processed++
	}
	return processed, nil
}

// PromoteBlocked walks blocked rows for the worker's task and elevates
// any whose blocker has cleared. Errors are returned aggregated so the
// caller can choose to log and continue.
func (w *Worker) PromoteBlocked(ctx context.Context) error {
	if _, err := w.cfg.Queue.PromoteAckedBlocked(ctx, w.cfg.Task, ack.SettingKey); err != nil {
		return fmt.Errorf("promote acked: %w", err)
	}
	if _, err := w.cfg.Queue.PromoteThumbReadyBlocked(ctx, w.cfg.Task); err != nil {
		return fmt.Errorf("promote thumb-ready: %w", err)
	}
	return nil
}

func (w *Worker) handleOne(ctx context.Context, c jobs.Claim) error {
	owner, err := w.cfg.OwnerOf(ctx, c.MediaID)
	if err != nil {
		return w.releaseTransient(ctx, c, ai.ErrKindTransient, fmt.Sprintf("owner lookup: %v", err))
	}
	acked, err := w.cfg.Acknowledged(ctx, owner)
	if err != nil {
		return w.releaseTransient(ctx, c, ai.ErrKindTransient, fmt.Sprintf("ack lookup: %v", err))
	}
	if !acked {
		return w.releaseTransient(ctx, c, ai.ErrKindTransient, jobs.AckBlockedReason)
	}

	jpegBytes, thumbStatus, err := w.cfg.Image.ResolveAndEncode(ctx, c.MediaID)
	if err != nil {
		return w.maybeRetryOrFail(ctx, c, ai.ErrKindMissingAIInput, err.Error())
	}
	switch thumbStatus {
	case "pending":
		return w.cfg.Queue.MarkBlocked(ctx, c.JobID, c.ClaimedAt, jobs.ThumbBlockedPending)
	case "working":
		return w.cfg.Queue.MarkBlocked(ctx, c.JobID, c.ClaimedAt, jobs.ThumbBlockedWorking)
	case "no_preview":
		if err := w.cfg.Skipped.Record(ctx, c.MediaID, w.cfg.Task, "no_preview"); err != nil {
			return err
		}
		return w.cfg.Queue.MarkDone(ctx, c.JobID, c.ClaimedAt)
	case "failed":
		return w.cfg.Queue.MarkBlocked(ctx, c.JobID, c.ClaimedAt, jobs.ThumbBlockedFailed)
	case "ready":
	default:
		return w.markFailed(ctx, c, ai.ErrKindMissingAIInput, "unknown thumb_status: "+thumbStatus)
	}

	if err := w.cfg.Sem.Acquire(ctx); err != nil {
		return w.releaseTransient(ctx, c, ai.ErrKindTransient, "sem: "+err.Error())
	}
	defer w.cfg.Sem.Release()

	resp, err := w.cfg.Gateway.Generate(ctx, gateway.Request{
		Model:  w.cfg.Fingerprint.ModelID,
		Prompt: w.cfg.PromptText,
		JPEG:   jpegBytes,
	})
	if err != nil {
		if errors.Is(err, gateway.ErrPermanent4xx) {
			return w.markFailed(ctx, c, ai.ErrKindProvider4xx, err.Error())
		}
		return w.maybeRetryOrFail(ctx, c, ai.ErrKindTransient, err.Error())
	}

	out, err := w.cfg.Process(resp.Text)
	if err != nil {
		if errors.Is(err, parse.ErrMalformed) {
			return w.maybeRetryOrFail(ctx, c, ai.ErrKindMalformed, err.Error())
		}
		return w.maybeRetryOrFail(ctx, c, ai.ErrKindTransient, err.Error())
	}

	var writeFn func(context.Context, *sql.Tx) error
	switch w.cfg.Task {
	case ai.TaskTag:
		writeFn = func(ctx context.Context, tx *sql.Tx) error {
			return w.cfg.Results.WriteTagResultTx(ctx, tx, c.MediaID, w.cfg.Fingerprint, w.cfg.PromptHash, out.Tags)
		}
	case ai.TaskCaption:
		writeFn = func(ctx context.Context, tx *sql.Tx) error {
			return w.cfg.Results.WriteCaptionResultTx(ctx, tx, c.MediaID, w.cfg.Fingerprint, w.cfg.PromptHash, out.Caption)
		}
	default:
		return w.markFailed(ctx, c, ai.ErrKindTransient, "unknown task: "+string(w.cfg.Task))
	}
	if err := w.cfg.Queue.WriteAndMarkDone(ctx, c, writeFn); err != nil {
		if errors.Is(err, jobs.ErrClaimLost) {
			// Lease was reclaimed mid-flight; the result tx was rolled back
			// so the active row is unchanged. The job will be reprocessed
			// under a fresh claim.
			w.cfg.Logger.Debug("ai claim lost on finalize", "job", c.JobID)
			return nil
		}
		return w.maybeRetryOrFail(ctx, c, ai.ErrKindTransient, "write result: "+err.Error())
	}
	if err := w.cfg.Failures.Delete(ctx, c.MediaID, w.cfg.Task, w.cfg.Fingerprint); err != nil {
		w.cfg.Logger.Warn("clear failure row", "err", err)
	}
	return nil
}

func (w *Worker) maybeRetryOrFail(ctx context.Context, c jobs.Claim, kind ai.LastErrorKind, msg string) error {
	if c.Attempts+1 >= w.cfg.MaxJobAttempts {
		return w.markFailed(ctx, c, kind, msg)
	}
	return w.cfg.Queue.MarkRetryable(ctx, c.JobID, c.ClaimedAt, kind, msg)
}

func (w *Worker) markFailed(ctx context.Context, c jobs.Claim, kind ai.LastErrorKind, msg string) error {
	if err := w.cfg.Queue.MarkFailed(ctx, c.JobID, c.ClaimedAt, kind, msg); err != nil {
		return err
	}
	if err := w.cfg.Failures.Record(ctx, c.MediaID, w.cfg.Task, w.cfg.Fingerprint, kind, msg, c.Attempts+1); err != nil {
		w.cfg.Logger.Warn("record failure row", "err", err)
	}
	return nil
}

// releaseTransient puts a claim back in 'blocked' (without bumping
// attempts). Used for ack-not-yet, owner-lookup transients, semaphore
// cancellation. The periodic lease sweep + PromoteBlocked elevates
// these back to pending.
func (w *Worker) releaseTransient(ctx context.Context, c jobs.Claim, kind ai.LastErrorKind, msg string) error {
	w.cfg.Logger.Debug("release transient", "kind", kind, "msg", msg, "job", c.JobID)
	return w.cfg.Queue.MarkBlocked(ctx, c.JobID, c.ClaimedAt, msg)
}
