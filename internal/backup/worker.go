package backup

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"go.kenn.io/fotobank/internal/content"
	"go.kenn.io/fotobank/internal/obs"
)

// Config uses the server's live authority and catalog lifetime.
type Config struct {
	DatabasePath string
	Vault        *content.Adapter
	Repository   string
	Interval     time.Duration
	KeepLast     int
	Logger       *slog.Logger
	Metrics      *obs.Metrics
}

// Worker serially captures complete archives and then applies scheduled-only
// retention. It never initializes a repository or deletes its files directly.
type Worker struct {
	cfg             Config
	lastSuccessAt   time.Time
	lastStaleWarnAt time.Time
}

func NewWorker(cfg Config) *Worker {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Worker{cfg: cfg}
}

func (w *Worker) Run(ctx context.Context) error {
	if w.cfg.Interval <= 0 || w.cfg.KeepLast < 1 || w.cfg.Repository == "" || w.cfg.DatabasePath == "" || w.cfg.Vault == nil {
		return fmt.Errorf("backup worker requires a catalog, vault, repository, positive interval and keep_last")
	}
	w.cfg.Logger.Info("archive scheduling started", "repository", w.cfg.Repository, "interval", w.cfg.Interval, "keep_last", w.cfg.KeepLast)
	w.lastSuccessAt = time.Now()
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
			if ctx.Err() != nil {
				return nil
			}
			delay := w.runDue(ctx)
			w.maybeWarnStale(time.Now())
			timer.Reset(delay)
		}
	}
}

// Consult persisted timestamps on startup/retry so restarts do not postpone
// backups. Missing repositories are retried, never implicitly initialized.
func (w *Worker) runDue(ctx context.Context) time.Duration {
	retry := min(w.cfg.Interval, 5*time.Minute)
	repository, err := content.OpenBackupRepository(w.cfg.Repository)
	if err != nil {
		w.cfg.Logger.Error("backup repository unavailable", "err", err)
		return retry
	}
	points, err := repository.Snapshots()
	if err != nil {
		w.cfg.Logger.Error("list recovery points", "err", err)
		return retry
	}
	delay, err := nextArchiveDelay(points, time.Now(), w.cfg.Interval)
	if err != nil {
		w.cfg.Logger.Error("read backup schedule", "err", err)
		return retry
	}
	if delay > 0 {
		return delay
	}
	point, err := w.capture(ctx, repository)
	if err != nil {
		w.cfg.Logger.Error("archive capture failed", "err", err)
		return retry
	}
	delay, err = nextArchiveDelay([]content.BackupSnapshot{point}, time.Now(), w.cfg.Interval)
	if err != nil {
		w.cfg.Logger.Error("read new recovery point timestamp", "err", err)
		return retry
	}
	return delay
}

func (w *Worker) capture(ctx context.Context, repository *content.BackupRepository) (content.BackupSnapshot, error) {
	start := time.Now()
	point, err := CreateArchive(ctx, w.cfg.DatabasePath, w.cfg.Vault, repository, ScheduledTag)
	status := "ok"
	if err != nil {
		status = "failed"
	}
	if w.cfg.Metrics != nil {
		w.cfg.Metrics.BackupSnapshots(status).Inc()
		w.cfg.Metrics.BackupSnapshotDuration(status).Update(time.Since(start).Seconds())
	}
	if err != nil {
		return content.BackupSnapshot{}, err
	}
	w.lastSuccessAt = time.Now()
	if w.cfg.Metrics != nil {
		w.cfg.Metrics.SetBackupLastSuccess(w.lastSuccessAt.Unix())
	}
	w.cfg.Logger.Info("recovery point created", "snapshot", point.ID, "duration", time.Since(start))
	// Cleanup failure does not invalidate the newly published archive.
	deleted, err := w.retain(ctx, repository, point.ID)
	if err != nil {
		w.cfg.Logger.Warn("recovery point retained; archive cleanup failed", "snapshot", point.ID, "err", err)
		if w.cfg.Metrics != nil {
			w.cfg.Metrics.BackupRetentionSweeps("failed").Inc()
		}
	} else if w.cfg.Metrics != nil {
		w.cfg.Metrics.BackupRetentionSweeps("ok").Inc()
		w.cfg.Metrics.BackupRetentionDeleted().Add(deleted)
	}
	return point, nil
}

func (w *Worker) retain(ctx context.Context, repository *content.BackupRepository, current string) (int, error) {
	points, err := repository.Snapshots()
	if err != nil {
		return 0, err
	}
	remove, err := scheduledRemovals(points, current, w.cfg.KeepLast)
	if err != nil {
		return 0, err
	}
	deleted := 0
	if len(remove) > 0 {
		report, err := repository.Forget(ctx, remove, false)
		deleted = len(report.Forgotten)
		if err != nil {
			return deleted, err
		}
	}
	_, err = repository.Prune(ctx, false)
	return deleted, err
}

func (w *Worker) maybeWarnStale(now time.Time) {
	if now.Sub(w.lastSuccessAt) <= max(48*time.Hour, 2*w.cfg.Interval) {
		return
	}
	if !w.lastStaleWarnAt.IsZero() && now.Sub(w.lastStaleWarnAt) < 48*time.Hour {
		return
	}
	w.cfg.Logger.Warn("scheduled archives are overdue", "last_success_at", w.lastSuccessAt)
	w.lastStaleWarnAt = now
}
