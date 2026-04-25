package backup

import (
	"bytes"
	"context"
	"database/sql"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestWorkerRunsTickAndExitsOnContextCancel(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src.sqlite")
	makeSourceDB(t, src)
	db, err := sql.Open("sqlite", src+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	r.NoError(err)
	t.Cleanup(func() { _ = db.Close() })

	dir := filepath.Join(tmp, "snaps")
	w := NewWorker(Config{
		DB:       db,
		Dir:      dir,
		Interval: 30 * time.Millisecond,
		Policy:   Policy{Keep15Min: 4, KeepHourly: 24, KeepDaily: 7},
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	require.Eventually(t, func() bool {
		entries, _ := os.ReadDir(dir)
		return len(entries) >= 1
	}, 2*time.Second, 10*time.Millisecond, "worker must produce at least one snapshot file")

	cancel()
	r.NoError(<-done, "Run must return nil after context cancel")
}

func TestWorkerLogsSnapshotSuccessFields(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src.sqlite")
	makeSourceDB(t, src)
	db, err := sql.Open("sqlite", src+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	r.NoError(err)
	t.Cleanup(func() { _ = db.Close() })

	var buf bytes.Buffer
	w := NewWorker(Config{
		DB:       db,
		Dir:      filepath.Join(tmp, "snaps"),
		Interval: 30 * time.Millisecond,
		Policy:   Policy{Keep15Min: 4, KeepHourly: 24, KeepDaily: 7},
		Logger:   slog.New(slog.NewTextHandler(&buf, nil)),
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	require.Eventually(t, func() bool {
		return strings.Contains(buf.String(), "backup snapshot ok")
	}, 2*time.Second, 10*time.Millisecond)
	cancel()
	<-done

	logs := buf.String()
	r.Contains(logs, "size_bytes=")
	r.Contains(logs, "duration_ms=")
	r.Contains(logs, "kept_15min=")
}

func TestWorkerStaleWarningSuppression(t *testing.T) {
	// Fake worker time via private field manipulation — start lastSuccessAt
	// at "now" and verify no stale warn fires before 48h elapse. Then jump
	// time forward and verify exactly one warn per 48h window.
	r := require.New(t)
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src.sqlite")
	makeSourceDB(t, src)
	db, err := sql.Open("sqlite", src+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	r.NoError(err)
	t.Cleanup(func() { _ = db.Close() })

	var buf bytes.Buffer
	w := NewWorker(Config{
		DB:       db,
		Dir:      filepath.Join(tmp, "snaps"),
		Interval: time.Hour, // never ticks during test
		Policy:   Policy{Keep15Min: 4, KeepHourly: 24, KeepDaily: 7},
		Logger:   slog.New(slog.NewTextHandler(&buf, nil)),
	})
	w.lastSuccessAt = time.Now().Add(-1 * time.Hour) // fresh; no warn
	w.maybeWarnStale(time.Now())
	r.NotContains(buf.String(), "backup snapshot stale")

	w.lastSuccessAt = time.Now().Add(-49 * time.Hour) // stale
	w.maybeWarnStale(time.Now())
	r.Contains(buf.String(), "backup snapshot stale")

	// Subsequent calls within the 48h suppression window do not re-warn.
	before := strings.Count(buf.String(), "backup snapshot stale")
	w.maybeWarnStale(time.Now())
	after := strings.Count(buf.String(), "backup snapshot stale")
	r.Equal(before, after, "stale warn must rate-limit to one per 48h")
}
