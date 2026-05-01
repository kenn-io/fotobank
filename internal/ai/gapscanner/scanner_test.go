package gapscanner_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/ai"
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

	s := gapscanner.New(rw, ro, q, resR, skipR)
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

	s := gapscanner.New(rw, ro, q, resR, skipR)
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

	s := gapscanner.New(rw, ro, q, resR, skipR)
	n, err := s.Scan(context.Background(), gapscanner.ScanRequest{
		Task: ai.TaskTag, Fingerprint: fp, Force: false, Limit: 100,
	})
	r.NoError(err)
	r.Equal(0, n, "video produces no enqueue")

	reason, found, _ := skipR.Get(context.Background(), mid, ai.TaskTag)
	r.True(found)
	r.Equal("video", reason)
}
