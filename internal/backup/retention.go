package backup

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Policy is the per-tier retention count. A field of 0 retains nothing
// of that tier; config validation rejects that.
type Policy struct {
	Keep15Min  int
	KeepHourly int
	KeepDaily  int
}

// SweepResult describes the per-tier outcome of a single Sweep call.
// Kept counts are total retained per tier; Deleted is non-future
// deletions only. Future-snapshot deletions are emitted via slog
// warnings, not counted here.
type SweepResult struct {
	Kept15Min  int
	KeptHourly int
	KeptDaily  int
	Deleted    int
}

// Sweep applies tiered retention to dir. Snapshots are bucketed by age
// into 15-min / hourly / daily tiers; the newest representative of each
// slot is kept up to the per-tier count cap. Older-than-7d snapshots
// are deleted. Future-dated snapshots (negative age) are deleted with a
// warn-level log. Stale .partial files older than 24h are also deleted.
//
// Tier selection is decoupled from "bucket full": a young snapshot
// whose 15-min slots are saturated is deleted, NOT promoted into the
// hourly tier.
func Sweep(dir string, policy Policy, now time.Time, logger *slog.Logger) (SweepResult, error) {
	if logger == nil {
		logger = slog.Default()
	}
	files, err := List(dir)
	if err != nil {
		return SweepResult{}, fmt.Errorf("list snapshots: %w", err)
	}

	seen15 := map[time.Time]bool{}
	seenH := map[time.Time]bool{}
	seenD := map[time.Time]bool{}
	var res SweepResult

	for _, f := range files {
		age := now.Sub(f.Timestamp)
		if age < 0 {
			if rmErr := os.Remove(f.Path); rmErr != nil {
				logger.Warn("future snapshot delete failed",
					"path", f.Path, "err", rmErr)
				continue
			}
			logger.Warn("future snapshot deleted",
				"path", f.Path, "ts", f.Timestamp,
				"clock_skew_seconds", -int64(age.Seconds()))
			continue
		}

		keep := false
		switch {
		case age < time.Hour:
			slot := f.Timestamp.Truncate(15 * time.Minute)
			if !seen15[slot] && len(seen15) < policy.Keep15Min {
				seen15[slot] = true
				res.Kept15Min++
				keep = true
			}
		case age < 24*time.Hour:
			slot := f.Timestamp.Truncate(time.Hour)
			if !seenH[slot] && len(seenH) < policy.KeepHourly {
				seenH[slot] = true
				res.KeptHourly++
				keep = true
			}
		case age < 7*24*time.Hour:
			slot := f.Timestamp.Truncate(24 * time.Hour)
			if !seenD[slot] && len(seenD) < policy.KeepDaily {
				seenD[slot] = true
				res.KeptDaily++
				keep = true
			}
		}

		if !keep {
			if rmErr := os.Remove(f.Path); rmErr != nil {
				logger.Warn("retention delete failed",
					"path", f.Path, "err", rmErr)
				continue
			}
			res.Deleted++
		}
	}

	if err := cleanStalePartials(dir, now, logger); err != nil {
		return res, err
	}
	return res, nil
}

func cleanStalePartials(dir string, now time.Time, logger *slog.Logger) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("readdir for partials: %w", err)
	}
	const partialAgeCutoff = 24 * time.Hour
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, snapshotExt+".partial") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if now.Sub(info.ModTime()) <= partialAgeCutoff {
			continue
		}
		p := filepath.Join(dir, name)
		if rmErr := os.Remove(p); rmErr != nil {
			logger.Warn("partial cleanup failed", "path", p, "err", rmErr)
			continue
		}
		logger.Info("partial cleanup",
			"path", p, "age_hours", now.Sub(info.ModTime()).Hours())
	}
	return nil
}
