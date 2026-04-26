package shareworker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"time"

	"github.com/wesm/fotobank/internal/broker"
	"github.com/wesm/fotobank/internal/obs"
	"github.com/wesm/fotobank/internal/share"
)

// Config carries the worker's collaborators + tunables.
type Config struct {
	Repo   *share.Repo
	Broker broker.BrokerClient
	// Tick is how often Run triggers RunOnce. Defaults to 15s.
	Tick time.Duration
	// Batch is the max number of rows drained per tick. Defaults to 20.
	Batch int
	// Now returns the current time; overridable for tests.
	Now func() time.Time
	// Rand is the jitter source for Backoff. Non-nil in prod; tests
	// pin it to a deterministic seed.
	Rand *rand.Rand
	// Logger is optional; a silent slog.Logger is used if nil.
	Logger *slog.Logger
	// Metrics is optional; when nil, no observability counters/histograms
	// are emitted. Production wires this from obs.NewMetrics; tests use
	// obs.NewTestMetrics or leave nil to skip the assertion surface.
	Metrics *obs.Metrics
}

// Worker drives the outbox. Call New to construct, then Run (long-
// running) or RunOnce (per-tick, for tests).
type Worker struct {
	cfg Config
}

// New constructs a Worker with defaults filled in.
func New(cfg Config) *Worker {
	if cfg.Tick <= 0 {
		cfg.Tick = 15 * time.Second
	}
	if cfg.Batch <= 0 {
		cfg.Batch = 20
	}
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
	}
	if cfg.Rand == nil {
		cfg.Rand = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Worker{cfg: cfg}
}

// Run loops: on each tick, calls RunOnce. Returns when ctx is
// cancelled. A RunOnce error is logged and the loop continues.
// RunOnce runs once immediately on entry so startup doesn't wait a
// full tick before making progress.
func (w *Worker) Run(ctx context.Context) error {
	t := time.NewTicker(w.cfg.Tick)
	defer t.Stop()
	for {
		if _, err := w.RunOnce(ctx); err != nil && !isCtxErr(err) {
			w.cfg.Logger.Error("shareworker tick failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

// RunOnce drains up to Batch ready rows once and returns the number
// processed. Non-terminal errors from a single row are logged and do
// not stop the drain. Context errors from a row's broker call abort
// the drain immediately; the aborted row is NOT counted as processed.
func (w *Worker) RunOnce(ctx context.Context) (int, error) {
	rows, err := w.cfg.Repo.ListReady(ctx, w.cfg.Now(), w.cfg.Batch)
	if err != nil {
		return 0, fmt.Errorf("list ready: %w", err)
	}
	n := 0
	for _, s := range rows {
		if ctx.Err() != nil {
			return n, ctx.Err()
		}
		var perr error
		switch s.BrokerStatus {
		case share.StatusPending:
			perr = w.processPending(ctx, s)
		case share.StatusRevoking:
			perr = w.processRevoking(ctx, s)
		default:
			w.cfg.Logger.Warn("shareworker unexpected status",
				"uuid", s.UUID, "status", s.BrokerStatus)
		}
		if isCtxErr(perr) {
			return n, perr
		}
		n++
	}
	return n, nil
}

func (w *Worker) processPending(ctx context.Context, s share.Scope) error {
	start := time.Now()
	err := w.cfg.Broker.PublishScope(ctx, s)
	if err == nil {
		if _, merr := w.cfg.Repo.MarkPublished(ctx, s.UUID, w.cfg.Now()); merr != nil && !isCtxErr(merr) {
			w.cfg.Logger.Error("mark-published call errored", "uuid", s.UUID, "err", merr)
		}
		w.cfg.Logger.Info("scope publish ok", "uuid", s.UUID)
		if w.cfg.Metrics != nil {
			w.cfg.Metrics.SharePublishes("ok").Inc()
			w.cfg.Metrics.SharePublishDuration("ok").Update(time.Since(start).Seconds())
		}
		return nil
	}
	if isCtxErr(err) {
		return err
	}
	w.recordFailure(ctx, s, share.StatusPending, err)
	if w.cfg.Metrics != nil {
		result := failureResult(err, s.BrokerAttempts)
		w.cfg.Metrics.SharePublishes(result).Inc()
		w.cfg.Metrics.SharePublishDuration(result).Update(time.Since(start).Seconds())
	}
	return nil
}

func (w *Worker) processRevoking(ctx context.Context, s share.Scope) error {
	start := time.Now()
	err := w.cfg.Broker.RevokeScope(ctx, s.UUID)
	if err == nil {
		if _, merr := w.cfg.Repo.MarkRevoked(ctx, s.UUID, w.cfg.Now()); merr != nil && !isCtxErr(merr) {
			w.cfg.Logger.Error("mark-revoked call errored", "uuid", s.UUID, "err", merr)
		}
		w.cfg.Logger.Info("scope revoke ok", "uuid", s.UUID)
		if w.cfg.Metrics != nil {
			w.cfg.Metrics.ShareRevokes("ok").Inc()
			w.cfg.Metrics.ShareRevokeDuration("ok").Update(time.Since(start).Seconds())
		}
		return nil
	}
	if isCtxErr(err) {
		return err
	}
	w.recordFailure(ctx, s, share.StatusRevoking, err)
	if w.cfg.Metrics != nil {
		result := failureResult(err, s.BrokerAttempts)
		w.cfg.Metrics.ShareRevokes(result).Inc()
		w.cfg.Metrics.ShareRevokeDuration(result).Update(time.Since(start).Seconds())
	}
	return nil
}

func (w *Worker) recordFailure(ctx context.Context, s share.Scope, phase share.BrokerStatus, err error) {
	if isTerminal(err, s.BrokerAttempts) {
		if _, merr := w.cfg.Repo.MarkFailed(ctx, s.UUID, phase, err.Error()); merr != nil && !isCtxErr(merr) {
			w.cfg.Logger.Error("mark-failed call errored", "uuid", s.UUID, "err", merr)
		}
		return
	}
	nextAt := w.cfg.Now().Add(Backoff(s.BrokerAttempts+1, w.cfg.Rand))
	if _, merr := w.cfg.Repo.MarkAttemptFailed(ctx, s.UUID, phase, err.Error(), nextAt); merr != nil && !isCtxErr(merr) {
		w.cfg.Logger.Error("mark-attempt-failed call errored", "uuid", s.UUID, "err", merr)
	}
}

// isTerminal reports whether the next attempt would call MarkFailed
// (true) versus MarkAttemptFailed (false). Phase-agnostic: applies
// equally to publish and revoke. Used both by recordFailure to choose
// the mark, and by callers to label the emitted result counter so the
// counter and the actual mark stay coherent.
func isTerminal(err error, attempts int) bool {
	return errors.Is(err, broker.ErrBrokerPermanent) || attempts+1 >= share.MaxBrokerAttempts
}

// failureResult maps a non-nil, non-ctx broker error and the current
// attempt count to the metric result label. Phase-agnostic: returns
// the same label for publish and revoke failures.
func failureResult(err error, attempts int) string {
	if isTerminal(err, attempts) {
		return "terminal_fail"
	}
	return "retry"
}

// isCtxErr reports whether err is a context cancellation / deadline error.
// We treat both as "do not log at Error level" since they signal shutdown,
// not a real broker or DB fault.
func isCtxErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
