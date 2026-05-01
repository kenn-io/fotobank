package ingest_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/ai"
	"github.com/wesm/fotobank/internal/ai/jobs"
	"github.com/wesm/fotobank/internal/ai/skipped"
	"github.com/wesm/fotobank/internal/ingest"
	"github.com/wesm/fotobank/internal/testutil"
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
