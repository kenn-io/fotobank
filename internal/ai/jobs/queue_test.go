package jobs_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/ai"
	"go.kenn.io/fotobank/internal/ai/jobs"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/testutil"
)

func newQueueWithDB(t *testing.T) (*jobs.Queue, *sql.DB, owners.Principal, []string) {
	t.Helper()
	rw, ro := testutil.OpenTestDBPair(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	mids := []string{
		testutil.SeedPhoto(t, rw, owner, "p1"),
		testutil.SeedPhoto(t, rw, owner, "p2"),
	}
	return jobs.NewQueue(rw, ro), rw, owner, mids
}

func newQueue(t *testing.T) (*jobs.Queue, owners.Principal, []string) {
	t.Helper()
	q, _, owner, mids := newQueueWithDB(t)
	return q, owner, mids
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

func TestClaimBatchForFingerprintOnlyClaimsMatchingRows(t *testing.T) {
	r := require.New(t)
	q, _, mids := newQueue(t)
	ctx := context.Background()

	r.NoError(q.EnqueueClaim(ctx, mids[0], ai.TaskTag, "claim-new"))
	r.NoError(q.EnqueueClaim(ctx, mids[1], ai.TaskTag, "claim-old"))

	claims, err := q.ClaimBatchForFingerprint(ctx, ai.TaskTag, "claim-new", 10)
	r.NoError(err)
	r.Len(claims, 1)
	r.Equal(mids[0], claims[0].MediaID)
	r.Equal("claim-new", claims[0].Fingerprint)

	claims, err = q.ClaimBatchForFingerprint(ctx, ai.TaskTag, "claim-new", 10)
	r.NoError(err)
	r.Empty(claims)

	claims, err = q.ClaimBatchForFingerprint(ctx, ai.TaskTag, "claim-old", 10)
	r.NoError(err)
	r.Len(claims, 1)
	r.Equal(mids[1], claims[0].MediaID)
}

func TestSupersedeForFingerprintChange(t *testing.T) {
	r := require.New(t)
	q, rw, _, mids := newQueueWithDB(t)
	ctx := context.Background()

	r.NoError(q.EnqueueClaim(ctx, mids[0], ai.TaskTag, "claim-old"))
	r.NoError(q.EnqueueClaim(ctx, mids[1], ai.TaskTag, "claim-old"))
	claimed, err := q.ClaimBatchForFingerprint(ctx, ai.TaskTag, "claim-old", 1)
	r.NoError(err)
	r.Len(claimed, 1)

	r.NoError(q.SupersedeForFingerprintChange(ctx, ai.TaskTag, mids, "claim-new"))
	r.NoError(q.EnqueueClaim(ctx, mids[0], ai.TaskTag, "claim-new"))
	r.NoError(q.EnqueueClaim(ctx, mids[1], ai.TaskTag, "claim-new"))

	claims, err := q.ClaimBatchForFingerprint(ctx, ai.TaskTag, "claim-old", 10)
	r.NoError(err)
	r.Empty(claims)

	claims, err = q.ClaimBatchForFingerprint(ctx, ai.TaskTag, "claim-new", 10)
	r.NoError(err)
	r.Len(claims, 2)

	var superseded int
	r.NoError(rw.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM ai_jobs WHERE task='tag' AND fingerprint='claim-old' AND status='superseded'`,
	).Scan(&superseded))
	r.Equal(2, superseded)
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

func TestWriteAndMarkDoneCommitsBoth(t *testing.T) {
	r := require.New(t)
	q, rw, _, mids := newQueueWithDB(t)
	ctx := context.Background()
	fp := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}
	r.NoError(q.Enqueue(ctx, mids[0], ai.TaskTag, fp))
	claims, err := q.ClaimBatch(ctx, ai.TaskTag, 10)
	r.NoError(err)
	r.Len(claims, 1)
	c := claims[0]

	r.NoError(q.WriteAndMarkDone(ctx, c, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO ai_results(id, media_id, task, model_id, prompt_version,
			 prompt_hash, input_profile, status, generated_at)
			 VALUES (?,?,?,?,?,?,?,'active',?)`,
			"r1", c.MediaID, "tag", fp.ModelID, fp.PromptVersion, "h", fp.InputProfile, time.Now().UTC())
		return err
	}))

	var status string
	r.NoError(rw.QueryRowContext(ctx, `SELECT status FROM ai_jobs WHERE id=?`, c.JobID).Scan(&status))
	r.Equal("done", status)

	var n int
	r.NoError(rw.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM ai_results WHERE media_id=? AND status='active'`, c.MediaID).Scan(&n))
	r.Equal(1, n)
}

func TestWriteAndMarkDoneRollsBackOnClaimLost(t *testing.T) {
	r := require.New(t)
	q, rw, _, mids := newQueueWithDB(t)
	ctx := context.Background()
	fp := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}
	r.NoError(q.Enqueue(ctx, mids[0], ai.TaskTag, fp))
	claims, err := q.ClaimBatch(ctx, ai.TaskTag, 10)
	r.NoError(err)
	r.Len(claims, 1)
	c := claims[0]

	// Lease lost mid-flight: another worker / sweep advanced the row.
	r.NoError(q.SupersedeAll(ctx, c.MediaID, ai.TaskTag))

	err = q.WriteAndMarkDone(ctx, c, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO ai_results(id, media_id, task, model_id, prompt_version,
			 prompt_hash, input_profile, status, generated_at)
			 VALUES (?,?,?,?,?,?,?,'active',?)`,
			"r1", c.MediaID, "tag", fp.ModelID, fp.PromptVersion, "h", fp.InputProfile, time.Now().UTC())
		return err
	})
	r.ErrorIs(err, jobs.ErrClaimLost)

	// The result row must NOT have been persisted.
	var n int
	r.NoError(rw.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM ai_results WHERE media_id=?`, c.MediaID).Scan(&n))
	r.Equal(0, n, "claim-lost write must roll back")
}

func TestPromoteAckedBlocked(t *testing.T) {
	r := require.New(t)
	q, rw, owner, mids := newQueueWithDB(t)
	ctx := context.Background()
	fp := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}
	r.NoError(q.Enqueue(ctx, mids[0], ai.TaskTag, fp))
	claims, err := q.ClaimBatch(ctx, ai.TaskTag, 10)
	r.NoError(err)
	r.Len(claims, 1)
	c := claims[0]
	r.NoError(q.MarkBlocked(ctx, c.JobID, c.ClaimedAt, jobs.AckBlockedReason))

	// Without an ack row, no promotion.
	n, err := q.PromoteAckedBlocked(ctx, ai.TaskTag, "ai.hidden_processing_acknowledged_at")
	r.NoError(err)
	r.Equal(0, n)

	_, err = rw.ExecContext(ctx,
		`INSERT INTO user_settings(principal_hub, principal_user_id, key, value, updated_at)
		 VALUES (?,?,?,?,?)`,
		owner.Hub, owner.UserID, "ai.hidden_processing_acknowledged_at", "ts", time.Now().UTC())
	r.NoError(err)

	n, err = q.PromoteAckedBlocked(ctx, ai.TaskTag, "ai.hidden_processing_acknowledged_at")
	r.NoError(err)
	r.Equal(1, n)

	again, err := q.ClaimBatch(ctx, ai.TaskTag, 10)
	r.NoError(err)
	r.Len(again, 1)
}

func TestPromoteThumbReadyBlocked(t *testing.T) {
	r := require.New(t)
	q, rw, _, mids := newQueueWithDB(t)
	ctx := context.Background()
	fp := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}

	// Force the source media into a non-ready thumb_status; SeedPhoto
	// inserts 'ready' so we override it.
	_, err := rw.ExecContext(ctx, `UPDATE assets SET thumb_status='pending' WHERE id=?`, mids[0])
	r.NoError(err)

	r.NoError(q.Enqueue(ctx, mids[0], ai.TaskTag, fp))
	claims, err := q.ClaimBatch(ctx, ai.TaskTag, 10)
	r.NoError(err)
	r.Len(claims, 1)
	c := claims[0]
	r.NoError(q.MarkBlocked(ctx, c.JobID, c.ClaimedAt, jobs.ThumbBlockedPending))

	// Still pending: no promotion.
	n, err := q.PromoteThumbReadyBlocked(ctx, ai.TaskTag)
	r.NoError(err)
	r.Equal(0, n)

	_, err = rw.ExecContext(ctx, `UPDATE assets SET thumb_status='ready' WHERE id=?`, mids[0])
	r.NoError(err)
	n, err = q.PromoteThumbReadyBlocked(ctx, ai.TaskTag)
	r.NoError(err)
	r.Equal(1, n)

	again, err := q.ClaimBatch(ctx, ai.TaskTag, 10)
	r.NoError(err)
	r.Len(again, 1)
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
