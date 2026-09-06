package backup

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.kenn.io/fotobank/internal/content"
)

func TestScheduledRetentionPreservesManualAndCurrent(t *testing.T) {
	r := require.New(t)
	points := []content.BackupSnapshot{
		{ID: "manual", Tag: "before-edit", CreatedAt: "2026-01-01T00:00:00Z"},
		{ID: "old", Tag: ScheduledTag, CreatedAt: "2026-01-02T00:00:00Z"},
		{ID: "newer", Tag: ScheduledTag, CreatedAt: "2026-01-03T00:00:00Z"},
		{ID: "current", Tag: ScheduledTag, CreatedAt: "2026-01-01T00:00:00Z"},
	}
	ids, err := scheduledRemovals(points, "current", 2)
	r.NoError(err)
	r.Equal([]string{"old"}, ids)
	_, err = scheduledRemovals(points, "absent", 2)
	r.Error(err)
	points[1].CreatedAt = "invalid"
	_, err = scheduledRemovals(points, "current", 2)
	r.Error(err)
}

func TestScheduleSurvivesRestart(t *testing.T) {
	r := require.New(t)
	now := time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)
	points := []content.BackupSnapshot{
		{ID: "scheduled", Tag: ScheduledTag, CreatedAt: now.Add(-23 * time.Hour).Format(time.RFC3339Nano)},
		{ID: "manual", CreatedAt: now.Format(time.RFC3339Nano)},
	}
	delay, err := nextArchiveDelay(points, now, 24*time.Hour)
	r.NoError(err)
	r.Equal(time.Hour, delay)
	delay, err = nextArchiveDelay(points, now.Add(-48*time.Hour), 24*time.Hour)
	r.NoError(err)
	r.Zero(delay, "clock rollback must not postpone backups")
	delay, err = nextArchiveDelay(points, now.Add(2*time.Hour), 24*time.Hour)
	r.NoError(err)
	r.Zero(delay)
}
