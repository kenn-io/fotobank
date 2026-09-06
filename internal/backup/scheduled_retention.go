package backup

import (
	"cmp"
	"fmt"
	"slices"
	"time"

	"go.kenn.io/fotobank/internal/content"
)

// ScheduledTag is reserved for recovery points owned by the scheduler.
const ScheduledTag = "fotobank:scheduled"

type scheduledPoint struct {
	id      string
	created time.Time
}

func scheduledPoints(points []content.BackupSnapshot) ([]scheduledPoint, error) {
	var scheduled []scheduledPoint
	for _, point := range points {
		if point.Tag != ScheduledTag {
			continue
		}
		created, err := time.Parse(time.RFC3339Nano, point.CreatedAt)
		if err != nil {
			return nil, fmt.Errorf("scheduled recovery point %s: %w", point.ID, err)
		}
		scheduled = append(scheduled, scheduledPoint{id: point.ID, created: created})
	}
	slices.SortFunc(scheduled, func(a, b scheduledPoint) int {
		if order := b.created.Compare(a.created); order != 0 {
			return order
		}
		return cmp.Compare(a.id, b.id)
	})
	return scheduled, nil
}

func scheduledRemovals(points []content.BackupSnapshot, current string, keep int) ([]string, error) {
	if keep < 1 {
		return nil, fmt.Errorf("keep_last must be positive")
	}
	scheduled, err := scheduledPoints(points)
	if err != nil {
		return nil, err
	}
	if !slices.ContainsFunc(scheduled, func(p scheduledPoint) bool { return p.id == current }) {
		return nil, fmt.Errorf("new recovery point %s is absent from repository", current)
	}
	// Always retain this run's point, including after a clock correction.
	remaining := keep - 1
	var remove []string
	for _, p := range scheduled {
		if p.id == current {
			continue
		}
		if remaining > 0 {
			remaining--
			continue
		}
		remove = append(remove, p.id)
	}
	return remove, nil
}

func nextArchiveDelay(points []content.BackupSnapshot, now time.Time, interval time.Duration) (time.Duration, error) {
	scheduled, err := scheduledPoints(points)
	if err != nil {
		return 0, err
	}
	if len(scheduled) == 0 {
		return 0, nil
	}
	// A future timestamp must not suspend scheduling indefinitely after a
	// clock correction. Start a new point on the corrected clock.
	if scheduled[0].created.After(now) {
		return 0, nil
	}
	return max(0, scheduled[0].created.Add(interval).Sub(now)), nil
}
