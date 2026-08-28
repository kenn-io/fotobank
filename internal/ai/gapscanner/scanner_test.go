package gapscanner_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/ai"
	"go.kenn.io/fotobank/internal/ai/embedding"
	"go.kenn.io/fotobank/internal/ai/failures"
	"go.kenn.io/fotobank/internal/ai/gapscanner"
	"go.kenn.io/fotobank/internal/ai/jobs"
	"go.kenn.io/fotobank/internal/ai/parse"
	"go.kenn.io/fotobank/internal/ai/results"
	"go.kenn.io/fotobank/internal/ai/skipped"
	"go.kenn.io/fotobank/internal/testutil"
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

func TestGapScannerUsesClaimFingerprintForQueueAndResultFingerprintForSkips(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	rw, ro := testutil.OpenTestDBPair(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	done := testutil.SeedPhoto(t, rw, owner, "p-done")
	fresh := testutil.SeedPhoto(t, rw, owner, "p-fresh")
	q := jobs.NewQueue(rw, ro)
	resR := results.NewRepo(rw, ro)
	skipR := skipped.NewRepo(rw, ro)
	resultFP := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}

	r.NoError(resR.WriteTagResult(ctx, done, resultFP, "h",
		[]parse.Tag{{Key: "x", Label: "x", Rank: 1}}))

	s := gapscanner.New(ro, q, resR, skipR)
	n, err := s.Scan(ctx, gapscanner.ScanRequest{
		Task:              ai.TaskTag,
		ClaimFingerprint:  "claim-current",
		ResultFingerprint: resultFP,
		Force:             false,
		Limit:             100,
	})
	r.NoError(err)
	r.Equal(1, n)

	claims, err := q.ClaimBatchForFingerprint(ctx, ai.TaskTag, "claim-current", 10)
	r.NoError(err)
	r.Len(claims, 1)
	r.Equal(fresh, claims[0].MediaID)
	claims, err = q.ClaimBatchForFingerprint(ctx, ai.TaskTag, resultFP.String(), 10)
	r.NoError(err)
	r.Empty(claims)
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
		`UPDATE assets SET media_type='video' WHERE id=?`, mid)
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
	skippedID := testutil.SeedPhoto(t, rw, owner, "00000000-0000-4000-8000-1725c9f3951c")
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

// embedFingerprint mirrors how the embed worker builds fingerprints:
// PromptVersion is empty (the embed task has no prompt). The scanner's
// failure-budget predicate hard-codes the empty prompt_version against
// ai_failures, so the test fixtures must match.
func embedFingerprint() ai.Fingerprint {
	return ai.Fingerprint{ModelID: "siglip2", InputProfile: "preview-v1"}
}

// seedEmbedGen creates a fresh "building" generation row plus its vec0
// table for the embed scan tests. The dim is arbitrary (the scanner
// never reads the vec table directly).
func seedEmbedGen(t *testing.T, rw, ro *sql.DB) embedding.Row {
	t.Helper()
	g := embedding.NewGenerations(rw, ro)
	row, err := g.FindOrCreateBuilding(context.Background(), embedFingerprint(), 8)
	require.NoError(t, err)
	return row
}

// insertEmbedMapping inserts a media_embedding_ids row for the given
// (gen, media) without touching the vec0 table. The scanner's
// gap-fill predicate only cares whether the mapping row exists, so
// allocating a real vector via the Mapping helper is unnecessary work.
func insertEmbedMapping(t *testing.T, rw *sql.DB, gen embedding.Row, mediaID string, vecID int64) {
	t.Helper()
	_, err := rw.ExecContext(context.Background(),
		`INSERT INTO media_embedding_ids(generation_id, media_id, vec_id) VALUES (?,?,?)`,
		gen.ID, mediaID, vecID)
	require.NoError(t, err)
}

// insertAIJob writes a raw ai_jobs row with the given status. Used by
// the in-flight test which needs a 'pending'/'working'/'blocked' row
// without going through Queue.Enqueue (Enqueue would itself satisfy
// the predicate, but we want to assert ScanEmbed sees the existing row
// and skips, not that it then enqueues a duplicate).
func insertAIJob(t *testing.T, rw *sql.DB, mediaID string, task ai.Task, fp ai.Fingerprint, status ai.JobStatus) {
	t.Helper()
	_, err := rw.ExecContext(context.Background(),
		`INSERT INTO ai_jobs(id, media_id, task, fingerprint, status, attempts, enqueued_at)
		 VALUES (?, ?, ?, ?, ?, 0, ?)`,
		uuid.NewString(), mediaID, string(task), fp.String(), string(status), time.Now().UTC())
	require.NoError(t, err)
}

func TestScanEmbed_EnqueuesNewMedia(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	rw, ro := testutil.OpenTestDBPair(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	mid := testutil.SeedPhoto(t, rw, owner, "p1")
	gen := seedEmbedGen(t, rw, ro)

	q := jobs.NewQueue(rw, ro)
	resR := results.NewRepo(rw, ro)
	skipR := skipped.NewRepo(rw, ro)
	fp := embedFingerprint()

	s := gapscanner.New(ro, q, resR, skipR)
	n, err := s.ScanEmbed(ctx, gapscanner.EmbedScanRequest{
		Owner:             owner,
		Generation:        gen,
		ClaimFingerprint:  "embed-claim",
		ResultFingerprint: fp,
		RetryBudget:       3,
	})
	r.NoError(err)
	r.Equal(1, n, "thumb-ready media with no mapping/skip/failure/job is enqueued")

	c, _ := q.Counters(ctx, ai.TaskEmbed)
	r.Equal(1, c.Pending)
	claims, err := q.ClaimBatchForFingerprint(ctx, ai.TaskEmbed, "embed-claim", 10)
	r.NoError(err)
	r.Len(claims, 1)
	_ = mid
}

func TestScanEmbed_SkipsMediaWithExistingMappingInGen(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	rw, ro := testutil.OpenTestDBPair(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	mid := testutil.SeedPhoto(t, rw, owner, "p1")
	gen := seedEmbedGen(t, rw, ro)

	// Mapping row exists for (gen, mid) — scanner must NOT re-enqueue.
	insertEmbedMapping(t, rw, gen, mid, 1)

	q := jobs.NewQueue(rw, ro)
	resR := results.NewRepo(rw, ro)
	skipR := skipped.NewRepo(rw, ro)

	s := gapscanner.New(ro, q, resR, skipR)
	n, err := s.ScanEmbed(ctx, gapscanner.EmbedScanRequest{
		Owner:       owner,
		Generation:  gen,
		Fingerprint: embedFingerprint(),
		RetryBudget: 3,
	})
	r.NoError(err)
	r.Equal(0, n, "media already mapped in this generation must be skipped")
}

func TestScanEmbed_SkipsMediaWithSkippedRowForEmbed(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	rw, ro := testutil.OpenTestDBPair(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	mid := testutil.SeedPhoto(t, rw, owner, "p1")
	gen := seedEmbedGen(t, rw, ro)

	skipR := skipped.NewRepo(rw, ro)
	r.NoError(skipR.Record(ctx, mid, ai.TaskEmbed, "no_preview"))

	q := jobs.NewQueue(rw, ro)
	resR := results.NewRepo(rw, ro)

	s := gapscanner.New(ro, q, resR, skipR)
	n, err := s.ScanEmbed(ctx, gapscanner.EmbedScanRequest{
		Owner:       owner,
		Generation:  gen,
		Fingerprint: embedFingerprint(),
		RetryBudget: 3,
	})
	r.NoError(err)
	r.Equal(0, n, "media with ai_skipped row for task=embed must be skipped")
}

func TestScanEmbed_SkipsMediaPastFailureBudget(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	rw, ro := testutil.OpenTestDBPair(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	mid := testutil.SeedPhoto(t, rw, owner, "p1")
	gen := seedEmbedGen(t, rw, ro)

	// Record a past-budget failure for the same fingerprint the worker
	// would write: prompt_version=''.
	failR := failures.NewRepo(rw, ro)
	fp := embedFingerprint()
	r.NoError(failR.Record(ctx, mid, ai.TaskEmbed, fp, ai.ErrKindMalformed, "x", 5))

	q := jobs.NewQueue(rw, ro)
	resR := results.NewRepo(rw, ro)
	skipR := skipped.NewRepo(rw, ro)

	s := gapscanner.New(ro, q, resR, skipR)
	n, err := s.ScanEmbed(ctx, gapscanner.EmbedScanRequest{
		Owner:       owner,
		Generation:  gen,
		Fingerprint: fp,
		RetryBudget: 3, // attempt_count(5) >= 3 → past budget
	})
	r.NoError(err)
	r.Equal(0, n, "media past failure budget must be skipped")
}

func TestScanEmbed_SkipsMediaWithInflightJob(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	rw, ro := testutil.OpenTestDBPair(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	mid := testutil.SeedPhoto(t, rw, owner, "p1")
	gen := seedEmbedGen(t, rw, ro)

	q := jobs.NewQueue(rw, ro)
	resR := results.NewRepo(rw, ro)
	skipR := skipped.NewRepo(rw, ro)
	fp := embedFingerprint()

	// In-flight job already exists. Try each of the three live statuses
	// to confirm the predicate's IN ('pending','working','blocked')
	// covers them all.
	for _, status := range []ai.JobStatus{ai.JobPending, ai.JobWorking, ai.JobBlocked} {
		_, err := rw.ExecContext(ctx, `DELETE FROM ai_jobs WHERE media_id=?`, mid)
		r.NoError(err)
		insertAIJob(t, rw, mid, ai.TaskEmbed, fp, status)

		s := gapscanner.New(ro, q, resR, skipR)
		n, err := s.ScanEmbed(ctx, gapscanner.EmbedScanRequest{
			Owner:       owner,
			Generation:  gen,
			Fingerprint: fp,
			RetryBudget: 3,
		})
		r.NoError(err)
		r.Equalf(0, n, "in-flight ai_jobs row with status=%s must be skipped", status)
	}
}

func TestScanEmbed_HiddenMediaExcludedWithoutAck(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	rw, ro := testutil.OpenTestDBPair(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	mid := testutil.SeedPhoto(t, rw, owner, "p1")
	gen := seedEmbedGen(t, rw, ro)

	// Hide the media: scanner without ack must exclude it.
	_, err := rw.ExecContext(ctx, `UPDATE assets SET hidden_at=? WHERE id=?`,
		time.Now().UTC(), mid)
	r.NoError(err)

	q := jobs.NewQueue(rw, ro)
	resR := results.NewRepo(rw, ro)
	skipR := skipped.NewRepo(rw, ro)

	s := gapscanner.New(ro, q, resR, skipR)
	n, err := s.ScanEmbed(ctx, gapscanner.EmbedScanRequest{
		Owner:           owner,
		Generation:      gen,
		Fingerprint:     embedFingerprint(),
		AckAllowsHidden: false,
		RetryBudget:     3,
	})
	r.NoError(err)
	r.Equal(0, n, "hidden media must not be enqueued without operator ack")
}

func TestScanEmbed_HiddenMediaIncludedWithAck(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	rw, ro := testutil.OpenTestDBPair(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	mid := testutil.SeedPhoto(t, rw, owner, "p1")
	gen := seedEmbedGen(t, rw, ro)

	_, err := rw.ExecContext(ctx, `UPDATE assets SET hidden_at=? WHERE id=?`,
		time.Now().UTC(), mid)
	r.NoError(err)

	q := jobs.NewQueue(rw, ro)
	resR := results.NewRepo(rw, ro)
	skipR := skipped.NewRepo(rw, ro)

	s := gapscanner.New(ro, q, resR, skipR)
	n, err := s.ScanEmbed(ctx, gapscanner.EmbedScanRequest{
		Owner:           owner,
		Generation:      gen,
		Fingerprint:     embedFingerprint(),
		AckAllowsHidden: true,
		RetryBudget:     3,
	})
	r.NoError(err)
	r.Equal(1, n, "hidden media must be enqueued when operator ack allows it")
	_ = mid
}
