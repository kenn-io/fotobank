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
		if _, err := w.RunOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
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
// not stop the drain.
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
		switch s.BrokerStatus {
		case share.StatusPending:
			w.processPending(ctx, s)
		case share.StatusRevoking:
			w.processRevoking(ctx, s)
		default:
			w.cfg.Logger.Warn("shareworker unexpected status",
				"uuid", s.UUID, "status", s.BrokerStatus)
		}
		n++
	}
	return n, nil
}

func (w *Worker) processPending(ctx context.Context, s share.Scope) {
	err := w.cfg.Broker.PublishScope(ctx, s)
	if err == nil {
		if _, merr := w.cfg.Repo.MarkPublished(ctx, s.UUID, w.cfg.Now()); merr != nil {
			w.cfg.Logger.Error("mark published failed", "uuid", s.UUID, "err", merr)
		}
		return
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return
	}
	w.recordFailure(ctx, s, share.StatusPending, err)
}

func (w *Worker) processRevoking(ctx context.Context, s share.Scope) {
	err := w.cfg.Broker.RevokeScope(ctx, s.UUID)
	if err == nil {
		if _, merr := w.cfg.Repo.MarkRevoked(ctx, s.UUID, w.cfg.Now()); merr != nil {
			w.cfg.Logger.Error("mark revoked failed", "uuid", s.UUID, "err", merr)
		}
		return
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return
	}
	w.recordFailure(ctx, s, share.StatusRevoking, err)
}

func (w *Worker) recordFailure(ctx context.Context, s share.Scope, phase share.BrokerStatus, err error) {
	permanent := errors.Is(err, broker.ErrBrokerPermanent)
	exhausted := s.BrokerAttempts+1 >= share.MaxBrokerAttempts
	if permanent || exhausted {
		if _, merr := w.cfg.Repo.MarkFailed(ctx, s.UUID, phase, err.Error()); merr != nil {
			w.cfg.Logger.Error("mark failed failed", "uuid", s.UUID, "err", merr)
		}
		return
	}
	nextAt := w.cfg.Now().Add(Backoff(s.BrokerAttempts+1, w.cfg.Rand))
	if _, merr := w.cfg.Repo.MarkAttemptFailed(ctx, s.UUID, phase, err.Error(), nextAt); merr != nil {
		w.cfg.Logger.Error("mark attempt-failed failed", "uuid", s.UUID, "err", merr)
	}
}
