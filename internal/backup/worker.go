package backup

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"go.kenn.io/fotobank/internal/obs"
)

// Config configures a Worker. Production constructs one from
// config.Backup; tests construct one directly.
type Config struct {
	DB  *sql.DB
	Dir string
	// RequiredRoot is an externally managed directory that must already
	// exist before a snapshot may create directories beneath it.
	RequiredRoot string
	Interval     time.Duration
	Policy       Policy
	Logger       *slog.Logger
	Metrics      *obs.Metrics
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
		case <-t.C:
			w.tick(ctx)
			w.maybeWarnStale(time.Now())
		}
	}
}

// tick takes one snapshot and runs one retention sweep. The ticker
// channel is treated as a wakeup signal, NOT a clock — every time
// reading happens via time.Now() so the snapshot filename, the
// lastSuccessAt stamp, and Sweep's age-bucketing all reference the
// same real-time instant. Otherwise, on a slow NAS where Snapshot
// stretches past one tick interval, a tick-time filename combined
// with a time.Now()-based file mtime can land "in the future"
// relative to a tick-time Sweep `now`, and the just-written snapshot
// gets deleted as a future-dated file.
func (w *Worker) tick(ctx context.Context) {
	now := time.Now()
	dst := filepath.Join(w.cfg.Dir, now.UTC().Format(StampLayout)+SnapshotExt)
	start := now
	if err := w.snapshot(ctx, dst); err != nil {
		w.cfg.Logger.Error("backup snapshot failed",
			"err", err, "dst", dst,
			"dur_ms", time.Since(start).Milliseconds())
		if w.cfg.Metrics != nil {
			w.cfg.Metrics.BackupSnapshots("failed").Inc()
			w.cfg.Metrics.BackupSnapshotDuration("failed").
				Update(time.Since(start).Seconds())
		}
		return
	}
	w.lastSuccessAt = time.Now()
	if w.cfg.Metrics != nil {
		w.cfg.Metrics.BackupSnapshots("ok").Inc()
		w.cfg.Metrics.BackupSnapshotDuration("ok").
			Update(time.Since(start).Seconds())
		w.cfg.Metrics.SetBackupLastSuccess(w.lastSuccessAt.Unix())
	}
	var size int64
	if info, err := os.Stat(dst); err == nil {
		size = info.Size()
	}

	res, sweepErr := Sweep(w.cfg.Dir, w.cfg.Policy, time.Now(), w.cfg.Logger)
	if sweepErr != nil {
		w.cfg.Logger.Warn("backup retention sweep failed",
			"err", sweepErr, "dir", w.cfg.Dir)
		if w.cfg.Metrics != nil {
			w.cfg.Metrics.BackupRetentionSweeps("failed").Inc()
		}
	} else if w.cfg.Metrics != nil {
		w.cfg.Metrics.BackupRetentionSweeps("ok").Inc()
		w.cfg.Metrics.BackupRetentionDeleted().Add(res.Deleted)
	}

	attrs := []any{
		"path", dst,
		"size_bytes", size,
		"dur_ms", time.Since(start).Milliseconds(),
	}
	if sweepErr == nil {
		attrs = append(attrs,
			"kept_15min", res.Kept15Min,
			"kept_hourly", res.KeptHourly,
			"kept_daily", res.KeptDaily,
			"deleted", res.Deleted)
	}
	w.cfg.Logger.Debug("backup snapshot ok", attrs...)
}

func (w *Worker) snapshot(ctx context.Context, dst string) error {
	if w.cfg.RequiredRoot == "" {
		return Snapshot(ctx, w.cfg.DB, dst)
	}
	root, err := os.OpenRoot(w.cfg.RequiredRoot)
	if err != nil {
		return fmt.Errorf("open required backup root: %w", err)
	}
	defer root.Close()
	return Snapshot(ctx, w.cfg.DB, dst)
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
