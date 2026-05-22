package ingest_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/ai"
	"go.kenn.io/fotobank/internal/ai/jobs"
	"go.kenn.io/fotobank/internal/ai/skipped"
	"go.kenn.io/fotobank/internal/ingest"
	"go.kenn.io/fotobank/internal/testutil"
)

func TestEnqueueForPhotoCreatesBothTaskJobs(t *testing.T) {
	r := require.New(t)
	rw, ro := testutil.OpenTestDBPair(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	mid := testutil.SeedPhoto(t, rw, owner, "p1")

	q := jobs.NewQueue(rw, ro)
	skipR := skipped.NewRepo(rw, ro)
	tagFP := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}
	capFP := ai.Fingerprint{ModelID: "m", PromptVersion: "caption-v1", InputProfile: "ip"}

	enq := ingest.NewRealAIEnqueuer(tagFP, capFP,
		func(ctx context.Context, m string, t ai.Task, fp ai.Fingerprint) error {
			return q.Enqueue(ctx, m, t, fp)
		},
		func(ctx context.Context, m string, t ai.Task, reason string) error {
			return skipR.Record(ctx, m, t, reason)
		},
	)
	r.NoError(enq.EnqueueForPhoto(context.Background(), mid))

	c, err := q.Counters(context.Background(), ai.TaskTag)
	r.NoError(err)
	r.Equal(1, c.Pending)
	c, err = q.Counters(context.Background(), ai.TaskCaption)
	r.NoError(err)
	r.Equal(1, c.Pending)
}

func TestEnqueueForPhotoUsesClaimFingerprintsWhenConfigured(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	rw, ro := testutil.OpenTestDBPair(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	mid := testutil.SeedPhoto(t, rw, owner, "p1")

	q := jobs.NewQueue(rw, ro)
	skipR := skipped.NewRepo(rw, ro)
	tagFP := ai.Fingerprint{ModelID: "tag-result", PromptVersion: "tags-v1", InputProfile: "ip"}
	capFP := ai.Fingerprint{ModelID: "caption-result", PromptVersion: "caption-v1", InputProfile: "ip"}

	enq := ingest.NewRealAIEnqueuer(tagFP, capFP, q.Enqueue, skipR.Record).
		WithClaimFingerprints("claim-tag", "claim-caption", q.EnqueueClaim)
	r.NoError(enq.EnqueueForPhoto(ctx, mid))

	tagClaims, err := q.ClaimBatchForFingerprint(ctx, ai.TaskTag, "claim-tag", 10)
	r.NoError(err)
	r.Len(tagClaims, 1)
	captionClaims, err := q.ClaimBatchForFingerprint(ctx, ai.TaskCaption, "claim-caption", 10)
	r.NoError(err)
	r.Len(captionClaims, 1)
}

func TestRecordVideoSkipSkipsBoth(t *testing.T) {
	r := require.New(t)
	rw, ro := testutil.OpenTestDBPair(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	mid := testutil.SeedPhoto(t, rw, owner, "p1")

	skipR := skipped.NewRepo(rw, ro)
	tagFP := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}
	capFP := ai.Fingerprint{ModelID: "m", PromptVersion: "caption-v1", InputProfile: "ip"}

	enq := ingest.NewRealAIEnqueuer(tagFP, capFP,
		func(ctx context.Context, m string, t ai.Task, fp ai.Fingerprint) error { return nil },
		func(ctx context.Context, m string, t ai.Task, reason string) error {
			return skipR.Record(ctx, m, t, reason)
		},
	)
	r.NoError(enq.RecordVideoSkip(context.Background(), mid))

	tn, err := skipR.Count(context.Background(), ai.TaskTag)
	r.NoError(err)
	r.Equal(1, tn)
	cn, err := skipR.Count(context.Background(), ai.TaskCaption)
	r.NoError(err)
	r.Equal(1, cn)
}

// TestEnqueueForPhoto_WithEmbedAlsoEnqueuesEmbed pins the Task I1
// invariant: when WithEmbed sets a non-zero fingerprint, the photo
// enqueue path lands one row in ai_jobs for each of the three tasks
// (tag, caption, embed). The fingerprints are all distinct so a row
// per task survives Enqueue's idempotent supersede logic.
func TestEnqueueForPhoto_WithEmbedAlsoEnqueuesEmbed(t *testing.T) {
	r := require.New(t)
	rw, ro := testutil.OpenTestDBPair(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	mid := testutil.SeedPhoto(t, rw, owner, "p1")

	q := jobs.NewQueue(rw, ro)
	skipR := skipped.NewRepo(rw, ro)
	tagFP := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}
	capFP := ai.Fingerprint{ModelID: "m", PromptVersion: "caption-v1", InputProfile: "ip"}
	embedFP := ai.Fingerprint{
		ModelID:      "siglip2",
		InputProfile: "jpeg-384-q85-metadata-stripped-embed-v1",
	}

	enq := ingest.NewRealAIEnqueuer(tagFP, capFP, q.Enqueue, skipR.Record).
		WithEmbed(embedFP)
	r.NoError(enq.EnqueueForPhoto(context.Background(), mid))

	for _, task := range []ai.Task{ai.TaskTag, ai.TaskCaption, ai.TaskEmbed} {
		c, err := q.Counters(context.Background(), task)
		r.NoError(err, "counters for %s", task)
		r.Equal(1, c.Pending, "EnqueueForPhoto must enqueue %s", task)
	}

	// Spot-check that the embed row carries the canonical embed
	// fingerprint string the worker's parseFingerprint reverses. A
	// regression here would silently break gap-fill across model swaps.
	var fpStr string
	r.NoError(rw.QueryRowContext(context.Background(),
		`SELECT fingerprint FROM ai_jobs WHERE media_id = ? AND task = 'embed'`,
		mid).Scan(&fpStr))
	r.Equal("siglip2||jpeg-384-q85-metadata-stripped-embed-v1", fpStr)
}

// TestEnqueueForPhoto_WithoutEmbedSkipsEmbedRow pins the gate: a
// realAIEnqueuer constructed without WithEmbed (i.e. the operator
// has not flipped cfg.AI.Embed.Enabled) must not produce ai_jobs rows
// for the embed task. Without this guard a tag/caption-only
// deployment would accumulate stale embed jobs the worker pool can
// never drain.
func TestEnqueueForPhoto_WithoutEmbedSkipsEmbedRow(t *testing.T) {
	r := require.New(t)
	rw, ro := testutil.OpenTestDBPair(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	mid := testutil.SeedPhoto(t, rw, owner, "p1")

	q := jobs.NewQueue(rw, ro)
	skipR := skipped.NewRepo(rw, ro)
	tagFP := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}
	capFP := ai.Fingerprint{ModelID: "m", PromptVersion: "caption-v1", InputProfile: "ip"}

	enq := ingest.NewRealAIEnqueuer(tagFP, capFP, q.Enqueue, skipR.Record)
	r.NoError(enq.EnqueueForPhoto(context.Background(), mid))

	c, err := q.Counters(context.Background(), ai.TaskEmbed)
	r.NoError(err)
	r.Equal(0, c.Pending)
}

// TestRecordVideoSkip_WithEmbedRecordsAllThree mirrors the photo path:
// when WithEmbed is set, video imports record an ai_skipped row for
// the embed task too. The embed gap scanner explicitly relies on this
// (see internal/ai/gapscanner/scanner.go::ScanEmbed) so a missing
// branch would cause every periodic tick to re-enqueue every video.
func TestRecordVideoSkip_WithEmbedRecordsAllThree(t *testing.T) {
	r := require.New(t)
	rw, ro := testutil.OpenTestDBPair(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	mid := testutil.SeedPhoto(t, rw, owner, "p1")

	skipR := skipped.NewRepo(rw, ro)
	tagFP := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}
	capFP := ai.Fingerprint{ModelID: "m", PromptVersion: "caption-v1", InputProfile: "ip"}
	embedFP := ai.Fingerprint{
		ModelID:      "siglip2",
		InputProfile: "jpeg-384-q85-metadata-stripped-embed-v1",
	}

	enq := ingest.NewRealAIEnqueuer(tagFP, capFP,
		func(ctx context.Context, m string, t ai.Task, fp ai.Fingerprint) error { return nil },
		skipR.Record,
	).WithEmbed(embedFP)
	r.NoError(enq.RecordVideoSkip(context.Background(), mid))

	for _, task := range []ai.Task{ai.TaskTag, ai.TaskCaption, ai.TaskEmbed} {
		n, err := skipR.Count(context.Background(), task)
		r.NoError(err, "count for %s", task)
		r.Equal(1, n, "RecordVideoSkip must skip %s", task)
	}
}
