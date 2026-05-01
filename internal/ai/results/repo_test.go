package results_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/ai"
	"github.com/wesm/fotobank/internal/ai/parse"
	"github.com/wesm/fotobank/internal/ai/results"
	"github.com/wesm/fotobank/internal/testutil"
)

func TestWriteTagSuccess(t *testing.T) {
	r := require.New(t)
	rw, ro := testutil.OpenTestDBPair(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	mid := testutil.SeedPhoto(t, rw, owner, "p1")
	repo := results.NewRepo(rw, ro)
	ctx := context.Background()
	fp := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}

	r.NoError(repo.WriteTagResult(ctx, mid, fp, "hash", []parse.Tag{
		{Key: "dog", Label: "Dog", Rank: 1},
		{Key: "beach", Label: "Beach", Rank: 2},
	}))

	tags, err := repo.GetActiveTags(ctx, mid)
	r.NoError(err)
	r.Len(tags, 2)
	r.Equal("dog", tags[0].Key)
}

func TestWriteCaptionSuccess(t *testing.T) {
	r := require.New(t)
	rw, ro := testutil.OpenTestDBPair(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	mid := testutil.SeedPhoto(t, rw, owner, "p1")
	repo := results.NewRepo(rw, ro)
	ctx := context.Background()
	fp := ai.Fingerprint{ModelID: "m", PromptVersion: "caption-v1", InputProfile: "ip"}

	r.NoError(repo.WriteCaptionResult(ctx, mid, fp, "hash", "A dog on a beach."))

	cap, found, err := repo.GetActiveCaption(ctx, mid)
	r.NoError(err)
	r.True(found)
	r.Equal("A dog on a beach.", cap.Text)
	r.Equal("m", cap.ModelID)
	r.WithinDuration(time.Now(), cap.GeneratedAt, 10*time.Second)
}

func TestRerunStaleAndPromoteAtomic(t *testing.T) {
	r := require.New(t)
	rw, ro := testutil.OpenTestDBPair(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	mid := testutil.SeedPhoto(t, rw, owner, "p1")
	repo := results.NewRepo(rw, ro)
	ctx := context.Background()
	fp1 := ai.Fingerprint{ModelID: "m1", PromptVersion: "caption-v1", InputProfile: "ip"}
	fp2 := ai.Fingerprint{ModelID: "m2", PromptVersion: "caption-v1", InputProfile: "ip"}

	r.NoError(repo.WriteCaptionResult(ctx, mid, fp1, "h1", "First."))
	r.NoError(repo.WriteCaptionResult(ctx, mid, fp2, "h2", "Second."))

	cap, found, err := repo.GetActiveCaption(ctx, mid)
	r.NoError(err)
	r.True(found)
	r.Equal("Second.", cap.Text)
	r.Equal("m2", cap.ModelID)
}

func TestDoneCounter(t *testing.T) {
	r := require.New(t)
	rw, ro := testutil.OpenTestDBPair(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	mids := []string{
		testutil.SeedPhoto(t, rw, owner, "p1"),
		testutil.SeedPhoto(t, rw, owner, "p2"),
	}
	repo := results.NewRepo(rw, ro)
	ctx := context.Background()
	fp := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}

	r.NoError(repo.WriteTagResult(ctx, mids[0], fp, "h", []parse.Tag{{Key: "x", Label: "x", Rank: 1}}))
	r.NoError(repo.WriteTagResult(ctx, mids[1], fp, "h", []parse.Tag{{Key: "y", Label: "y", Rank: 1}}))

	n, err := repo.DoneCount(ctx, ai.TaskTag, fp)
	r.NoError(err)
	r.Equal(2, n)
}
