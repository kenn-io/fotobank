package gapscanner_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/ai"
	"github.com/wesm/fotobank/internal/ai/failures"
	"github.com/wesm/fotobank/internal/ai/gapscanner"
	"github.com/wesm/fotobank/internal/ai/jobs"
	"github.com/wesm/fotobank/internal/ai/parse"
	"github.com/wesm/fotobank/internal/ai/results"
	"github.com/wesm/fotobank/internal/ai/skipped"
	"github.com/wesm/fotobank/internal/testutil"
)

func TestGapScannerEnqueuesMissingMedia(t *testing.T) {
	r := require.New(t)
	rw, ro := testutil.OpenTestDBPair(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	mids := []string{
		testutil.SeedPhoto(t, rw, owner, "p1"),
		testutil.SeedPhoto(t, rw, owner, "p2"),
	}
	q := jobs.NewQueue(rw, ro)
	resR := results.NewRepo(rw, ro)
	skipR := skipped.NewRepo(rw, ro)
	fp := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}

	// p1 already has an active result for fp; p2 doesn't.
	r.NoError(resR.WriteTagResult(context.Background(), mids[0], fp, "h",
		[]parse.Tag{{Key: "x", Label: "x", Rank: 1}}))

	s := gapscanner.New(ro, q, resR, skipR)
	n, err := s.Scan(context.Background(), gapscanner.ScanRequest{
		Task: ai.TaskTag, Fingerprint: fp, Force: false, Limit: 100,
	})
	r.NoError(err)
	r.Equal(1, n)
	c, _ := q.Counters(context.Background(), ai.TaskTag)
	r.Equal(1, c.Pending)
	_ = mids[1]
}

func TestGapScannerForceIncludesActiveMedia(t *testing.T) {
	r := require.New(t)
	rw, ro := testutil.OpenTestDBPair(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	mids := []string{
		testutil.SeedPhoto(t, rw, owner, "p1"),
		testutil.SeedPhoto(t, rw, owner, "p2"),
	}
	q := jobs.NewQueue(rw, ro)
	resR := results.NewRepo(rw, ro)
	skipR := skipped.NewRepo(rw, ro)
	fp := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}

	r.NoError(resR.WriteTagResult(context.Background(), mids[0], fp, "h",
		[]parse.Tag{{Key: "x", Label: "x", Rank: 1}}))

	s := gapscanner.New(ro, q, resR, skipR)
	n, err := s.Scan(context.Background(), gapscanner.ScanRequest{
		Task: ai.TaskTag, Fingerprint: fp, Force: true, Limit: 100,
	})
	r.NoError(err)
	r.Equal(2, n)
	_ = mids[1]
}

func TestGapScannerSkipsVideoAndRecordsSkip(t *testing.T) {
	r := require.New(t)
	rw, ro := testutil.OpenTestDBPair(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	mid := testutil.SeedPhoto(t, rw, owner, "p1")
	_, err := rw.ExecContext(context.Background(),
		`UPDATE media SET media_type='video' WHERE id=?`, mid)
	r.NoError(err)

	q := jobs.NewQueue(rw, ro)
	resR := results.NewRepo(rw, ro)
	skipR := skipped.NewRepo(rw, ro)
	fp := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}

	s := gapscanner.New(ro, q, resR, skipR)
	n, err := s.Scan(context.Background(), gapscanner.ScanRequest{
		Task: ai.TaskTag, Fingerprint: fp, Force: false, Limit: 100,
	})
	r.NoError(err)
	r.Equal(0, n, "video produces no enqueue")

	reason, found, _ := skipR.Get(context.Background(), mid, ai.TaskTag)
	r.True(found)
	r.Equal("video", reason)
}

// TestGapScannerSkipsTerminalFailures verifies that a non-force scan
// excludes media whose current-fingerprint failure is already recorded
// in ai_failures. Without this filter the periodic tick would
// re-enqueue dead jobs every interval, draining the worker on rows
// that would fail again.
func TestGapScannerSkipsTerminalFailures(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	rw, ro := testutil.OpenTestDBPair(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	failed := testutil.SeedPhoto(t, rw, owner, "p-failed")
	fresh := testutil.SeedPhoto(t, rw, owner, "p-fresh")

	q := jobs.NewQueue(rw, ro)
	resR := results.NewRepo(rw, ro)
	skipR := skipped.NewRepo(rw, ro)
	failR := failures.NewRepo(rw, ro)
	fp := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}

	r.NoError(failR.Record(ctx, failed, ai.TaskTag, fp, ai.ErrKindMalformed, "x", 2))

	s := gapscanner.New(ro, q, resR, skipR)
	n, err := s.Scan(ctx, gapscanner.ScanRequest{
		Task: ai.TaskTag, Fingerprint: fp, Force: false, Limit: 100,
	})
	r.NoError(err)
	r.Equal(1, n, "only the fresh row should be enqueued; the terminal failure must stay put")
	c, _ := q.Counters(ctx, ai.TaskTag)
	r.Equal(1, c.Pending)
	_ = fresh
}

// TestGapScannerSkipsAlreadySkipped verifies that a non-force scan
// excludes media that already have an ai_skipped row. Skipped is
// terminal regardless of fingerprint — once skipped, scans must not
// chase them every tick.
func TestGapScannerSkipsAlreadySkipped(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	rw, ro := testutil.OpenTestDBPair(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	skippedID := testutil.SeedPhoto(t, rw, owner, "p-skipped")
	fresh := testutil.SeedPhoto(t, rw, owner, "p-fresh")

	q := jobs.NewQueue(rw, ro)
	resR := results.NewRepo(rw, ro)
	skipR := skipped.NewRepo(rw, ro)
	fp := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}

	r.NoError(skipR.Record(ctx, skippedID, ai.TaskTag, "no_preview"))

	s := gapscanner.New(ro, q, resR, skipR)
	n, err := s.Scan(ctx, gapscanner.ScanRequest{
		Task: ai.TaskTag, Fingerprint: fp, Force: false, Limit: 100,
	})
	r.NoError(err)
	r.Equal(1, n, "only the fresh row should be enqueued; the skipped row must stay put")
	_ = fresh
}

// TestGapScannerLimitMakesProgressPastFinishedRows verifies the cursor
// behavior: with LIMIT applied AFTER excluding finished work, a scan
// over a library where most rows are already done returns the next
// pending batch, not the same un-ordered head every tick.
func TestGapScannerLimitMakesProgressPastFinishedRows(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	rw, ro := testutil.OpenTestDBPair(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")

	// Seed 6 photos. Mark the first 4 as already done (active result),
	// leaving 2 candidates. With LIMIT=2 the scan must enqueue both —
	// not silently return zero because the first 2 rows in the un-ordered
	// scan happen to already be finished.
	mids := make([]string, 6)
	for i := range mids {
		mids[i] = testutil.SeedPhoto(t, rw, owner, "p"+string(rune('0'+i)))
	}
	q := jobs.NewQueue(rw, ro)
	resR := results.NewRepo(rw, ro)
	skipR := skipped.NewRepo(rw, ro)
	fp := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}
	for i := range 4 {
		r.NoError(resR.WriteTagResult(ctx, mids[i], fp, "h",
			[]parse.Tag{{Key: "x", Label: "x", Rank: 1}}))
	}

	s := gapscanner.New(ro, q, resR, skipR)
	n, err := s.Scan(ctx, gapscanner.ScanRequest{
		Task: ai.TaskTag, Fingerprint: fp, Force: false, Limit: 2,
	})
	r.NoError(err)
	r.Equal(2, n, "LIMIT counts unfinished work, not pre-filter rows")
}
