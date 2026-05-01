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
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wesm/fotobank/internal/obs"
)

// syncBuf wraps bytes.Buffer with a mutex so a slog handler running in
// the worker goroutine can write while a polling goroutine reads.
type syncBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func TestWorkerRunsTickAndExitsOnContextCancel(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src.sqlite")
	makeSourceDB(t, src)
	db, err := sql.Open("sqlite3", src+"?_busy_timeout=5000&_fk=1")
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
	db, err := sql.Open("sqlite3", src+"?_busy_timeout=5000&_fk=1")
	r.NoError(err)
	t.Cleanup(func() { _ = db.Close() })

	var buf syncBuf
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
	r.Contains(logs, "dur_ms=")
	r.Contains(logs, "kept_15min=")
}

// time.NewTicker panics on a non-positive duration; Run must reject
// such configurations cleanly so a misconfigured server gets an error
// log instead of a goroutine panic that crashes the whole process.
func TestWorkerRunRejectsNonPositiveInterval(t *testing.T) {
	r := require.New(t)
	for _, iv := range []time.Duration{0, -1 * time.Second} {
		w := NewWorker(Config{
			Dir:      t.TempDir(),
			Interval: iv,
			Policy:   Policy{Keep15Min: 4, KeepHourly: 24, KeepDaily: 7},
			Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		})
		err := w.Run(context.Background())
		r.Error(err, "interval=%s must reject", iv)
		r.Contains(err.Error(), "interval must be positive")
	}
}

func TestWorkerStaleWarningSuppression(t *testing.T) {
	// Fake worker time via private field manipulation — start lastSuccessAt
	// at "now" and verify no stale warn fires before 48h elapse. Then jump
	// time forward and verify exactly one warn per 48h window.
	r := require.New(t)
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src.sqlite")
	makeSourceDB(t, src)
	db, err := sql.Open("sqlite3", src+"?_busy_timeout=5000&_fk=1")
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

func TestWorkerEmitsMetricsAndPushesLastSuccess(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src.sqlite")
	makeSourceDB(t, src)
	db, err := sql.Open("sqlite3", src+"?_busy_timeout=5000&_fk=1")
	r.NoError(err)
	t.Cleanup(func() { _ = db.Close() })

	m := obs.NewTestMetrics()
	w := NewWorker(Config{
		DB:       db,
		Dir:      filepath.Join(tmp, "snaps"),
		Interval: 30 * time.Millisecond,
		Policy:   Policy{Keep15Min: 4, KeepHourly: 24, KeepDaily: 7},
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Metrics:  m,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	require.Eventually(t, func() bool {
		return m.BackupSnapshots("ok").Get() >= 1
	}, 2*time.Second, 10*time.Millisecond)
	cancel()
	r.NoError(<-done)

	r.Positive(m.BackupSnapshots("ok").Get())
	// last_success_unix gauge must have been pushed.
	var buf bytes.Buffer
	m.WritePrometheus(&buf)
	out := buf.String()
	r.NotContains(out, "fotobank_backup_last_success_unix 0",
		"last_success_unix must have a real timestamp after a successful tick")
	r.NotContains(out, "fotobank_backup_seconds_since_last_success -1",
		"seconds_since_last_success must be derived now that last_success is set")
}

func TestWorkerEmitsFailedSnapshotMetric(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	// Use a missing source DB — Snapshot will fail.
	src := filepath.Join(tmp, "missing.sqlite")
	db, err := sql.Open("sqlite3", "file:"+src+"?mode=rw")
	r.NoError(err)
	t.Cleanup(func() { _ = db.Close() })

	m := obs.NewTestMetrics()
	w := NewWorker(Config{
		DB:       db,
		Dir:      filepath.Join(tmp, "snaps"),
		Interval: 30 * time.Millisecond,
		Policy:   Policy{Keep15Min: 4, KeepHourly: 24, KeepDaily: 7},
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Metrics:  m,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	require.Eventually(t, func() bool {
		return m.BackupSnapshots("failed").Get() >= 1
	}, 2*time.Second, 10*time.Millisecond)
	cancel()
	r.NoError(<-done)
}

// TestWorkerLogsCarryComponent: when the caller wires a logger derived
// via .With("component", "backup") (as cli/server.go does), the
// worker's log lines carry that component attribute. We construct the
// same wrapper here so the assertion is robust against future log
// additions in the worker.
func TestWorkerLogsCarryComponent(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src.sqlite")
	makeSourceDB(t, src)
	db, err := sql.Open("sqlite3", src+"?_busy_timeout=5000&_fk=1")
	r.NoError(err)
	t.Cleanup(func() { _ = db.Close() })

	// syncBuf (defined at the top of this file) wraps bytes.Buffer with
	// a mutex so the worker goroutine's slog writes don't race with the
	// polling goroutine inside require.Eventually that reads via String().
	var logBuf syncBuf
	base := slog.New(slog.NewJSONHandler(&logBuf, nil))
	w := NewWorker(Config{
		DB:       db,
		Dir:      filepath.Join(tmp, "snaps"),
		Interval: 30 * time.Millisecond,
		Policy:   Policy{Keep15Min: 4, KeepHourly: 24, KeepDaily: 7},
		Logger:   base.With("component", "backup"),
		Metrics:  obs.NewTestMetrics(),
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	require.Eventually(t, func() bool {
		return strings.Contains(logBuf.String(), `"msg":"backup snapshot ok"`)
	}, 2*time.Second, 10*time.Millisecond)
	cancel()
	r.NoError(<-done)
	// Per-line scan: enforce that EVERY emitted line carries
	// component=backup, not just at least one. A stray non-component
	// line would slip past a single Contains check.
	for line := range strings.SplitSeq(strings.TrimSpace(logBuf.String()), "\n") {
		r.Contains(line, `"component":"backup"`,
			"every backup-worker log line must carry component=backup")
	}
}
