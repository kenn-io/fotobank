package results_test

import (
	"context"
	"database/sql"
	"errors"
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

// readFTSRowSnapshot returns the caption_text and tag_label columns of
// the media_fts row for mediaID. Tests that exercise the J2 wiring use
// it to assert that the FTS row reflects the row the promotion just
// wrote, not the prior active. Returns ("", "") when no row exists.
func readFTSRowSnapshot(t *testing.T, rw *sql.DB, mediaID string) (caption, tags string) {
	t.Helper()
	r := require.New(t)
	err := rw.QueryRowContext(context.Background(),
		`SELECT caption_text, tag_label FROM media_fts WHERE media_id = ?`, mediaID,
	).Scan(&caption, &tags)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ""
	}
	r.NoError(err)
	return caption, tags
}

// TestResults_PromoteCaptionRefreshesFTS pins the J2 contract for the
// caption-promotion path: after WriteCaptionResult commits, the
// media_fts row's caption_text reflects the active caption text. A
// second promotion replaces the first.
func TestResults_PromoteCaptionRefreshesFTS(t *testing.T) {
	r := require.New(t)
	rw, ro := testutil.OpenTestDBPair(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	mid := testutil.SeedPhoto(t, rw, owner, "p1")
	repo := results.NewRepo(rw, ro)
	ctx := context.Background()
	fp := ai.Fingerprint{ModelID: "m", PromptVersion: "caption-v1", InputProfile: "ip"}

	r.NoError(repo.WriteCaptionResult(ctx, mid, fp, "h1", "small dog on a beach"))

	cap, tags := readFTSRowSnapshot(t, rw, mid)
	r.Equal("small dog on a beach", cap)
	r.Empty(tags, "tag corpus must be empty until a tag promotion runs")

	// Re-promote with a new model — the FTS row must reflect the new
	// caption, not the stale one.
	fp2 := ai.Fingerprint{ModelID: "m2", PromptVersion: "caption-v1", InputProfile: "ip"}
	r.NoError(repo.WriteCaptionResult(ctx, mid, fp2, "h2", "second caption"))
	cap2, _ := readFTSRowSnapshot(t, rw, mid)
	r.Equal("second caption", cap2)
}

// TestResults_PromoteTagRefreshesFTS pins the J2 contract for the
// tag-promotion path: after WriteTagResult commits, the media_fts
// row's tag_label is the space-joined active tag-label set in rank
// order.
func TestResults_PromoteTagRefreshesFTS(t *testing.T) {
	r := require.New(t)
	rw, ro := testutil.OpenTestDBPair(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	mid := testutil.SeedPhoto(t, rw, owner, "p1")
	repo := results.NewRepo(rw, ro)
	ctx := context.Background()
	fp := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}

	r.NoError(repo.WriteTagResult(ctx, mid, fp, "h", []parse.Tag{
		{Key: "dog", Label: "Dog", Rank: 1},
		{Key: "beach", Label: "Beach", Rank: 2},
	}))

	cap, tags := readFTSRowSnapshot(t, rw, mid)
	r.Empty(cap, "caption corpus must be empty until a caption promotion runs")
	r.Equal("Dog Beach", tags)

	// Re-promote with different tags — the FTS row must reflect the
	// new active set, not the stale one.
	r.NoError(repo.WriteTagResult(ctx, mid, fp, "h", []parse.Tag{
		{Key: "cat", Label: "Cat", Rank: 1},
	}))
	_, tags2 := readFTSRowSnapshot(t, rw, mid)
	r.Equal("Cat", tags2)
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
