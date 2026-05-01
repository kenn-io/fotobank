package jobs_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/ai"
	"github.com/wesm/fotobank/internal/ai/jobs"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/testutil"
)

func newQueue(t *testing.T) (*jobs.Queue, owners.Principal, []string) {
	t.Helper()
	rw, ro := testutil.OpenTestDBPair(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	mids := []string{
		testutil.SeedPhoto(t, rw, owner, "p1"),
		testutil.SeedPhoto(t, rw, owner, "p2"),
	}
	return jobs.NewQueue(rw, ro), owner, mids
}

func TestEnqueueAndClaim(t *testing.T) {
	r := require.New(t)
	q, _, mids := newQueue(t)
	ctx := context.Background()
	fp := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}

	r.NoError(q.Enqueue(ctx, mids[0], ai.TaskTag, fp))
	claims, err := q.ClaimBatch(ctx, ai.TaskTag, 10)
	r.NoError(err)
	r.Len(claims, 1)
	r.Equal(mids[0], claims[0].MediaID)
	r.Equal(fp.String(), claims[0].Fingerprint)
}

func TestEnqueueIdempotent(t *testing.T) {
	r := require.New(t)
	q, _, mids := newQueue(t)
	ctx := context.Background()
	fp := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}

	r.NoError(q.Enqueue(ctx, mids[0], ai.TaskTag, fp))
	r.NoError(q.Enqueue(ctx, mids[0], ai.TaskTag, fp)) // no-op

	claims, err := q.ClaimBatch(ctx, ai.TaskTag, 10)
	r.NoError(err)
	r.Len(claims, 1)
}

func TestSupersedeOnFingerprintChange(t *testing.T) {
	r := require.New(t)
	q, _, mids := newQueue(t)
	ctx := context.Background()
	fp1 := ai.Fingerprint{ModelID: "m1", PromptVersion: "tags-v1", InputProfile: "ip"}
	fp2 := ai.Fingerprint{ModelID: "m2", PromptVersion: "tags-v1", InputProfile: "ip"}

	r.NoError(q.Enqueue(ctx, mids[0], ai.TaskTag, fp1))
	r.NoError(q.Enqueue(ctx, mids[0], ai.TaskTag, fp2))

	claims, err := q.ClaimBatch(ctx, ai.TaskTag, 10)
	r.NoError(err)
	r.Len(claims, 1)
	r.Equal(fp2.String(), claims[0].Fingerprint)
}

func TestMarkDoneSucceeds(t *testing.T) {
	r := require.New(t)
	q, _, mids := newQueue(t)
	ctx := context.Background()
	fp := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}

	r.NoError(q.Enqueue(ctx, mids[0], ai.TaskTag, fp))
	claims, err := q.ClaimBatch(ctx, ai.TaskTag, 10)
	r.NoError(err)
	r.Len(claims, 1)
	r.NoError(q.MarkDone(ctx, claims[0].JobID, claims[0].ClaimedAt))
}

func TestMarkDoneClaimLost(t *testing.T) {
	r := require.New(t)
	q, _, mids := newQueue(t)
	ctx := context.Background()
	fp := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}

	r.NoError(q.Enqueue(ctx, mids[0], ai.TaskTag, fp))
	claims, err := q.ClaimBatch(ctx, ai.TaskTag, 10)
	r.NoError(err)
	r.Len(claims, 1)

	r.NoError(q.SupersedeAll(ctx, mids[0], ai.TaskTag))
	r.ErrorIs(q.MarkDone(ctx, claims[0].JobID, claims[0].ClaimedAt), jobs.ErrClaimLost)
}

func TestMarkBlockedReleasesAttempts(t *testing.T) {
	r := require.New(t)
	q, _, mids := newQueue(t)
	ctx := context.Background()
	fp := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}

	r.NoError(q.Enqueue(ctx, mids[0], ai.TaskTag, fp))
	claims, err := q.ClaimBatch(ctx, ai.TaskTag, 10)
	r.NoError(err)
	r.Len(claims, 1)

	r.NoError(q.MarkBlocked(ctx, claims[0].JobID, claims[0].ClaimedAt, "thumb_blocked"))
	r.NoError(q.PromoteBlocked(ctx, claims[0].JobID))

	again, err := q.ClaimBatch(ctx, ai.TaskTag, 10)
	r.NoError(err)
	r.Len(again, 1)
	r.Equal(0, again[0].Attempts)
}

func TestSweepLeasesResetsStaleWorking(t *testing.T) {
	r := require.New(t)
	q, _, mids := newQueue(t)
	ctx := context.Background()
	fp := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}

	r.NoError(q.Enqueue(ctx, mids[0], ai.TaskTag, fp))
	claims, err := q.ClaimBatch(ctx, ai.TaskTag, 10)
	r.NoError(err)
	r.Len(claims, 1)

	r.NoError(q.BackdateClaim(ctx, claims[0].JobID, time.Now().Add(-30*time.Minute)))
	n, err := q.SweepLeases(ctx, 10*time.Minute)
	r.NoError(err)
	r.Equal(1, n)

	again, err := q.ClaimBatch(ctx, ai.TaskTag, 10)
	r.NoError(err)
	r.Len(again, 1)
	r.Equal(0, again[0].Attempts)
}

func TestCounters(t *testing.T) {
	r := require.New(t)
	q, _, mids := newQueue(t)
	ctx := context.Background()
	fp := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}

	r.NoError(q.Enqueue(ctx, mids[0], ai.TaskTag, fp))
	r.NoError(q.Enqueue(ctx, mids[1], ai.TaskTag, fp))
	c, err := q.Counters(ctx, ai.TaskTag)
	r.NoError(err)
	r.Equal(2, c.Pending)
	r.Equal(0, c.Working)
}
