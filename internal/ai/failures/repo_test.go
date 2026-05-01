package failures_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/ai"
	"github.com/wesm/fotobank/internal/ai/failures"
	"github.com/wesm/fotobank/internal/testutil"
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
