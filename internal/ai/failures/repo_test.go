package failures_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/ai"
	"go.kenn.io/fotobank/internal/ai/failures"
	"go.kenn.io/fotobank/internal/testutil"
)

func TestRecordAndDelete(t *testing.T) {
	r := require.New(t)
	rw, ro := testutil.OpenTestDBPair(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	mid := testutil.SeedPhoto(t, rw, owner, "p1")
	repo := failures.NewRepo(rw, ro)
	ctx := context.Background()
	fp := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}

	r.NoError(repo.Record(ctx, mid, ai.TaskTag, fp, ai.ErrKindMalformed, "bad json", 2))

	rows, err := repo.ListForFingerprint(ctx, ai.TaskTag, fp, 10)
	r.NoError(err)
	r.Len(rows, 1)
	r.Equal(mid, rows[0].MediaID)

	r.NoError(repo.Delete(ctx, mid, ai.TaskTag, fp))
	rows, _ = repo.ListForFingerprint(ctx, ai.TaskTag, fp, 10)
	r.Empty(rows)
}

// TestRecord_AccumulatesAttemptCountOnConflict pins the upsert
// contract that the gap scanner's failure-budget gate relies on:
// when a (media, task, fingerprint) row already exists, a follow-up
// Record must INCREMENT attempt_count, not overwrite it. The embed
// worker re-enqueues create a fresh ai_jobs row each time, so each
// re-enqueue presents attempts=1 to Record. Without the accumulator,
// the panel and the gap-scanner both lose count of repeat failures.
func TestRecord_AccumulatesAttemptCountOnConflict(t *testing.T) {
	r := require.New(t)
	rw, ro := testutil.OpenTestDBPair(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	mid := testutil.SeedPhoto(t, rw, owner, "p1")
	repo := failures.NewRepo(rw, ro)
	ctx := context.Background()
	fp := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}

	// First record: net-new row. attempt_count is whatever the caller
	// supplied (mirrors the chat worker's c.Attempts+1).
	r.NoError(repo.Record(ctx, mid, ai.TaskTag, fp, ai.ErrKindMalformed, "first", 1))
	row, found, err := repo.GetForFingerprint(ctx, mid, ai.TaskTag, fp)
	r.NoError(err)
	r.True(found)
	r.Equal(1, row.AttemptCount)

	// Second record under the same key with attempts=1: +1.
	r.NoError(repo.Record(ctx, mid, ai.TaskTag, fp, ai.ErrKindTransient, "second", 1))
	row, _, err = repo.GetForFingerprint(ctx, mid, ai.TaskTag, fp)
	r.NoError(err)
	r.Equal(2, row.AttemptCount, "second record must increment by attempts=1")
	r.Equal("second", row.LastError, "last_error must reflect the latest record")
	r.Equal(ai.ErrKindTransient, row.LastErrorKind)

	// Third record with attempts=1 bumps to 3.
	r.NoError(repo.Record(ctx, mid, ai.TaskTag, fp, ai.ErrKindMalformed, "third", 1))
	row, _, err = repo.GetForFingerprint(ctx, mid, ai.TaskTag, fp)
	r.NoError(err)
	r.Equal(3, row.AttemptCount)
}

// TestRecord_AccumulatesProvidedAttempts pins the contract that the
// upsert increments by the SUPPLIED attempts argument, not by a fixed
// constant. The chat worker (MaxJobAttempts > 1) records the per-job
// attempt count after a job exhausts its in-job retries — it must
// accumulate by that count, not by 1. Embed-worker callers always
// pass attempts=1, so their behaviour is unchanged.
func TestRecord_AccumulatesProvidedAttempts(t *testing.T) {
	r := require.New(t)
	rw, ro := testutil.OpenTestDBPair(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	mid := testutil.SeedPhoto(t, rw, owner, "p1")
	repo := failures.NewRepo(rw, ro)
	ctx := context.Background()
	fp := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}

	// First record carries attempts=2 (the just-failed run had 2
	// in-job attempts before exhausting retry budget).
	r.NoError(repo.Record(ctx, mid, ai.TaskTag, fp, ai.ErrKindTransient, "first", 2))
	row, found, err := repo.GetForFingerprint(ctx, mid, ai.TaskTag, fp)
	r.NoError(err)
	r.True(found)
	r.Equal(2, row.AttemptCount, "first insert preserves the supplied attempts")

	// Second record with attempts=3 must accumulate to 5, NOT 3 (overwrite)
	// or 3 (fixed +1).
	r.NoError(repo.Record(ctx, mid, ai.TaskTag, fp, ai.ErrKindTransient, "second", 3))
	row, _, err = repo.GetForFingerprint(ctx, mid, ai.TaskTag, fp)
	r.NoError(err)
	r.Equal(5, row.AttemptCount,
		"second record must accumulate by supplied attempts (2+3=5)")
}

func TestCountForFingerprint(t *testing.T) {
	r := require.New(t)
	rw, ro := testutil.OpenTestDBPair(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	mids := []string{
		testutil.SeedPhoto(t, rw, owner, "p1"),
		testutil.SeedPhoto(t, rw, owner, "p2"),
	}
	repo := failures.NewRepo(rw, ro)
	ctx := context.Background()
	fp := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}

	r.NoError(repo.Record(ctx, mids[0], ai.TaskTag, fp, ai.ErrKindMalformed, "x", 2))
	r.NoError(repo.Record(ctx, mids[1], ai.TaskTag, fp, ai.ErrKindProvider4xx, "y", 1))

	n, err := repo.CountForFingerprint(ctx, ai.TaskTag, fp)
	r.NoError(err)
	r.Equal(2, n)
}

func TestGetForFingerprint(t *testing.T) {
	r := require.New(t)
	rw, ro := testutil.OpenTestDBPair(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	mid := testutil.SeedPhoto(t, rw, owner, "p1")
	repo := failures.NewRepo(rw, ro)
	ctx := context.Background()
	fp1 := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}
	fp2 := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v2", InputProfile: "ip"}

	r.NoError(repo.Record(ctx, mid, ai.TaskTag, fp1, ai.ErrKindMalformed, "bad json", 2))
	r.NoError(repo.Record(ctx, mid, ai.TaskTag, fp2, ai.ErrKindProvider4xx, "old", 1))

	row, found, err := repo.GetForFingerprint(ctx, mid, ai.TaskTag, fp1)
	r.NoError(err)
	r.True(found)
	r.Equal(mid, row.MediaID)
	r.Equal("bad json", row.LastError)
	r.Equal(ai.ErrKindMalformed, row.LastErrorKind)
	r.Equal(2, row.AttemptCount)

	// Wrong fingerprint returns (zero, false, nil).
	otherFP := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-vX", InputProfile: "ip"}
	_, found, err = repo.GetForFingerprint(ctx, mid, ai.TaskTag, otherFP)
	r.NoError(err)
	r.False(found)

	// Wrong media returns (zero, false, nil).
	_, found, err = repo.GetForFingerprint(ctx, "missing", ai.TaskTag, fp1)
	r.NoError(err)
	r.False(found)
}

func TestDeleteAllForFingerprint(t *testing.T) {
	r := require.New(t)
	rw, ro := testutil.OpenTestDBPair(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	mid := testutil.SeedPhoto(t, rw, owner, "p1")
	repo := failures.NewRepo(rw, ro)
	ctx := context.Background()
	fp := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}

	r.NoError(repo.Record(ctx, mid, ai.TaskTag, fp, ai.ErrKindMalformed, "x", 2))
	n, err := repo.DeleteAllForFingerprint(ctx, ai.TaskTag, fp)
	r.NoError(err)
	r.Equal(1, n)
}

// TestListForFingerprintByOwnerCutoffExcludesNewerRows is a focused
// regression for the cutoff filter relied on by service.RetryFailed:
// rows whose failed_at is strictly newer than the cutoff must NOT
// appear in the result. Without the filter, RetryFailed could chase
// freshly created failures across batches indefinitely.
func TestListForFingerprintByOwnerCutoffExcludesNewerRows(t *testing.T) {
	r := require.New(t)
	rw, ro := testutil.OpenTestDBPair(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	old := testutil.SeedPhoto(t, rw, owner, "p-old")
	fresh := testutil.SeedPhoto(t, rw, owner, "p-fresh")
	repo := failures.NewRepo(rw, ro)
	ctx := context.Background()
	fp := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}

	r.NoError(repo.Record(ctx, old, ai.TaskTag, fp, ai.ErrKindMalformed, "old", 2))
	// Capture the cutoff strictly between the two failures so the
	// filter has a row to exclude. SQLite stores failed_at at ns
	// precision; a 5 ms gap is more than enough.
	time.Sleep(5 * time.Millisecond)
	cutoff := time.Now().UTC()
	time.Sleep(5 * time.Millisecond)
	r.NoError(repo.Record(ctx, fresh, ai.TaskTag, fp, ai.ErrKindMalformed, "fresh", 1))

	// With cutoff applied, only the older row is visible.
	rows, err := repo.ListForFingerprintByOwner(ctx, ai.TaskTag, fp, owner.Hub, owner.UserID, cutoff, 0)
	r.NoError(err)
	r.Len(rows, 1)
	r.Equal(old, rows[0].MediaID)

	// Without cutoff (zero time), both are visible — confirms it's the
	// filter, not some unrelated bug, that excluded the fresh row.
	rows, err = repo.ListForFingerprintByOwner(ctx, ai.TaskTag, fp, owner.Hub, owner.UserID, time.Time{}, 0)
	r.NoError(err)
	r.Len(rows, 2)
}
