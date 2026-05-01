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
