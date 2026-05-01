package skipped_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/ai"
	"github.com/wesm/fotobank/internal/ai/skipped"
	"github.com/wesm/fotobank/internal/testutil"
)

func TestRecordAndCount(t *testing.T) {
	r := require.New(t)
	rw, ro := testutil.OpenTestDBPair(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	mid1 := testutil.SeedPhoto(t, rw, owner, "p1")
	mid2 := testutil.SeedPhoto(t, rw, owner, "p2")
	repo := skipped.NewRepo(rw, ro)
	ctx := context.Background()

	r.NoError(repo.Record(ctx, mid1, ai.TaskTag, "video"))
	r.NoError(repo.Record(ctx, mid1, ai.TaskCaption, "video"))
	r.NoError(repo.Record(ctx, mid2, ai.TaskTag, "no_preview"))

	n, err := repo.Count(ctx, ai.TaskTag)
	r.NoError(err)
	r.Equal(2, n)

	n, err = repo.Count(ctx, ai.TaskCaption)
	r.NoError(err)
	r.Equal(1, n)
}

func TestRecordIsIdempotent(t *testing.T) {
	r := require.New(t)
	rw, ro := testutil.OpenTestDBPair(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	mid := testutil.SeedPhoto(t, rw, owner, "p1")
	repo := skipped.NewRepo(rw, ro)
	ctx := context.Background()

	r.NoError(repo.Record(ctx, mid, ai.TaskTag, "video"))
	r.NoError(repo.Record(ctx, mid, ai.TaskTag, "video"))

	reason, found, err := repo.Get(context.Background(), mid, ai.TaskTag)
	r.NoError(err)
	r.True(found)
	r.Equal("video", reason)
}
