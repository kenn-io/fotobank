package backup

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// Config configures a Worker. Production constructs one from
// config.Backup; tests construct one directly.
type Config struct {
	DB       *sql.DB
	Dir      string
	Interval time.Duration
	Policy   Policy
	Logger   *slog.Logger
}

// Worker takes periodic snapshots and runs retention sweeps. One
// goroutine, single serial loop. Skipped ticks coalesce naturally.
type Worker struct {
	cfg             Config
	lastSuccessAt   time.Time
	lastStaleWarnAt time.Time
}

// staleAfter is the threshold past which a worker that has not had a
// successful snapshot warns. staleSuppress is the minimum interval
// between consecutive stale warnings to keep the log readable.
const (
	staleAfter    = 48 * time.Hour
	staleSuppress = 48 * time.Hour
)

func NewWorker(cfg Config) *Worker {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Worker{cfg: cfg}
}

func (w *Worker) Run(ctx context.Context) error {
	// time.NewTicker panics on a non-positive duration. Validate up
	// front so an operator misconfiguration surfaces as a clean error
	// rather than a goroutine panic that crashes the server.
	if w.cfg.Interval <= 0 {
		return fmt.Errorf("backup worker: interval must be positive, got %s", w.cfg.Interval)
	}
	w.cfg.Logger.Info("backup worker starting",
		"dir", w.cfg.Dir,
		"interval", w.cfg.Interval,
		"keep_15min", w.cfg.Policy.Keep15Min,
		"keep_hourly", w.cfg.Policy.KeepHourly,
		"keep_daily", w.cfg.Policy.KeepDaily)
	defer w.cfg.Logger.Info("backup worker stopped")

	// Suppress false-stale: treat "never succeeded" as "just succeeded"
	// for the first 48h of uptime.
	w.lastSuccessAt = time.Now()

	t := time.NewTicker(w.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case now := <-t.C:
			w.tick(ctx, now)
			w.maybeWarnStale(now)
		}
	}
}

func (w *Worker) tick(ctx context.Context, now time.Time) {
	dst := filepath.Join(w.cfg.Dir, now.UTC().Format(StampLayout)+SnapshotExt)
	start := time.Now()
	if err := Snapshot(ctx, w.cfg.DB, dst); err != nil {
		w.cfg.Logger.Error("backup snapshot failed",
			"err", err, "dst", dst,
			"duration_ms", time.Since(start).Milliseconds())
		return
	}
	w.lastSuccessAt = now
	var size int64
	if info, err := os.Stat(dst); err == nil {
		size = info.Size()
	}

	res, err := Sweep(w.cfg.Dir, w.cfg.Policy, now, w.cfg.Logger)
	if err != nil {
		w.cfg.Logger.Warn("backup retention sweep failed", "err", err, "dir", w.cfg.Dir)
		// Snapshot still succeeded; don't suppress the success log.
	}

	attrs := []any{
		"path", dst,
		"size_bytes", size,
		"duration_ms", time.Since(start).Milliseconds(),
	}
	if err == nil {
		attrs = append(attrs,
			"kept_15min", res.Kept15Min,
			"kept_hourly", res.KeptHourly,
			"kept_daily", res.KeptDaily,
			"deleted", res.Deleted)
	}
	w.cfg.Logger.Info("backup snapshot ok", attrs...)
}

func (w *Worker) maybeWarnStale(now time.Time) {
	staleness := now.Sub(w.lastSuccessAt)
	if staleness <= staleAfter {
		return
	}
	if !w.lastStaleWarnAt.IsZero() && now.Sub(w.lastStaleWarnAt) < staleSuppress {
		return
	}
	w.cfg.Logger.Warn("backup snapshot stale",
		"last_success_at", w.lastSuccessAt,
		"hours_since", staleness.Hours())
	w.lastStaleWarnAt = now
}
