package ai_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/ai"
	"go.kenn.io/fotobank/internal/ai/ack"
	"go.kenn.io/fotobank/internal/ai/failures"
	"go.kenn.io/fotobank/internal/ai/gapscanner"
	"go.kenn.io/fotobank/internal/ai/jobs"
	"go.kenn.io/fotobank/internal/ai/parse"
	"go.kenn.io/fotobank/internal/ai/results"
	airuntime "go.kenn.io/fotobank/internal/ai/runtime"
	"go.kenn.io/fotobank/internal/ai/skipped"
	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/owners"
	aiservice "go.kenn.io/fotobank/internal/service/ai"
	"go.kenn.io/fotobank/internal/testutil"
)

type fakeRuntimeProvider struct {
	snap airuntime.Snapshot
}

func (f fakeRuntimeProvider) Effective() airuntime.Snapshot { return f.snap }

// fakeMediaCheck mirrors MediaService.Get's contract: returns
// errs.ErrNotFound when caller is not the owner of mediaID, or when
// the row is hidden and includeHidden is false. Tests configure rows
// via add(); the lightbox MediaView path goes through this gate before
// reading any AI table.
type fakeMediaCheck struct {
	rows map[string]struct {
		owner  owners.Principal
		hidden bool
	}
}

func newFakeMediaCheck() *fakeMediaCheck {
	return &fakeMediaCheck{rows: map[string]struct {
		owner  owners.Principal
		hidden bool
	}{}}
}

func (f *fakeMediaCheck) add(id string, owner owners.Principal, hidden bool) {
	f.rows[id] = struct {
		owner  owners.Principal
		hidden bool
	}{owner: owner, hidden: hidden}
}

func (f *fakeMediaCheck) Check(_ context.Context, id string, caller owners.Principal, includeHidden bool) error {
	row, ok := f.rows[id]
	if !ok {
		return errs.ErrNotFound
	}
	if row.owner != caller {
		return errs.ErrNotFound
	}
	if row.hidden && !includeHidden {
		return errs.ErrNotFound
	}
	return nil
}

func makeServiceWithDB(t *testing.T) (*aiservice.Service, *sql.DB) {
	svc, _, rw := makeServiceWithMedia(t)
	return svc, rw
}

func makeServiceWithMedia(t *testing.T) (*aiservice.Service, *fakeMediaCheck, *sql.DB) {
	t.Helper()
	rw, ro := testutil.OpenTestDBPair(t)
	q := jobs.NewQueue(rw, ro)
	resR := results.NewRepo(rw, ro)
	failR := failures.NewRepo(rw, ro)
	skipR := skipped.NewRepo(rw, ro)
	ackS := ack.New(rw, ro)
	gs := gapscanner.New(ro, q, resR, skipR)
	mediaCheck := newFakeMediaCheck()

	svc := aiservice.New(aiservice.Deps{
		Queue: q, Results: resR, Failures: failR, Skipped: skipR,
		Ack: ackS, Gap: gs, Media: mediaCheck,
		ConfigFingerprints: aiservice.ConfigFingerprints{
			Tag:     ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"},
			Caption: ai.Fingerprint{ModelID: "m", PromptVersion: "caption-v1", InputProfile: "ip"},
		},
	})
	return svc, mediaCheck, rw
}

func TestAcknowledgePersists(t *testing.T) {
	r := require.New(t)
	svc, rw := makeServiceWithDB(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	r.NoError(svc.Acknowledge(context.Background(), owner))
	got, err := svc.IsAcknowledged(context.Background(), owner)
	r.NoError(err)
	r.True(got)
}

func TestBackfillRequiresAck(t *testing.T) {
	r := require.New(t)
	svc, rw := makeServiceWithDB(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	_, err := svc.Backfill(context.Background(), owner, ai.TaskTag, false)
	r.ErrorIs(err, errs.ErrAcknowledgementRequired)
}

func TestBackfillScopedToCallerOwnedMedia(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	svc, rw := makeServiceWithDB(t)
	alice := testutil.SeedOwner(t, rw, "local", "alice")
	bob := testutil.SeedOwner(t, rw, "local", "bob")
	_ = testutil.SeedPhoto(t, rw, alice, "a1")
	_ = testutil.SeedPhoto(t, rw, alice, "a2")
	_ = testutil.SeedPhoto(t, rw, bob, "b1")
	r.NoError(svc.Acknowledge(ctx, alice))

	n, err := svc.Backfill(ctx, alice, ai.TaskTag, false)
	r.NoError(err)
	r.Equal(2, n, "Alice's backfill must enqueue only her photos, not Bob's")
}

func TestBackfillUsesRuntimeClaimAndResultFingerprint(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	rw, ro := testutil.OpenTestDBPair(t)
	q := jobs.NewQueue(rw, ro)
	resR := results.NewRepo(rw, ro)
	failR := failures.NewRepo(rw, ro)
	skipR := skipped.NewRepo(rw, ro)
	ackS := ack.New(rw, ro)
	gs := gapscanner.New(ro, q, resR, skipR)
	resultFP := ai.Fingerprint{ModelID: "runtime-model", PromptVersion: "tags-v1", InputProfile: "ip"}
	svc := aiservice.New(aiservice.Deps{
		Queue: q, Results: resR, Failures: failR, Skipped: skipR,
		Ack: ackS, Gap: gs,
		ConfigFingerprints: aiservice.ConfigFingerprints{
			Tag: ai.Fingerprint{ModelID: "boot-model", PromptVersion: "tags-v1", InputProfile: "ip"},
		},
		Runtime: fakeRuntimeProvider{snap: airuntime.Snapshot{
			Config: ai.Config{Enabled: true, Tag: ai.TaskConfig{Enabled: true}},
			Claim:  airuntime.ClaimFingerprints{Tag: "claim-runtime"},
			Result: airuntime.ResultFingerprints{Tag: resultFP},
		}},
	})
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	mid := testutil.SeedPhoto(t, rw, owner, "runtime")
	r.NoError(svc.Acknowledge(ctx, owner))

	n, err := svc.Backfill(ctx, owner, ai.TaskTag, false)
	r.NoError(err)
	r.Equal(1, n)

	claims, err := q.ClaimBatchForFingerprint(ctx, ai.TaskTag, "claim-runtime", 10)
	r.NoError(err)
	r.Len(claims, 1)
	r.Equal(mid, claims[0].MediaID)
}

func TestRetryFailedScopedToCallerOwnership(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	svc, rw := makeServiceWithDB(t)
	alice := testutil.SeedOwner(t, rw, "local", "alice")
	bob := testutil.SeedOwner(t, rw, "local", "bob")
	aMid := testutil.SeedPhoto(t, rw, alice, "a1")
	bMid := testutil.SeedPhoto(t, rw, bob, "b1")
	r.NoError(svc.Acknowledge(ctx, alice))

	failR := failures.NewRepo(rw, rw)
	tagFP := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}
	r.NoError(failR.Record(ctx, aMid, ai.TaskTag, tagFP, ai.ErrKindMalformed, "x", 1))
	r.NoError(failR.Record(ctx, bMid, ai.TaskTag, tagFP, ai.ErrKindMalformed, "y", 1))

	n, err := svc.RetryFailed(ctx, alice, ai.TaskTag)
	r.NoError(err)
	r.Equal(1, n, "Alice's retry must touch only her own failure")

	// Bob's failure must remain untouched.
	rows, err := failR.ListForFingerprintByOwner(ctx, ai.TaskTag, tagFP, bob.Hub, bob.UserID, time.Time{}, 0)
	r.NoError(err)
	r.Len(rows, 1)
	r.Equal(bMid, rows[0].MediaID)
}

func TestRetryPhotoRejectsForeignMedia(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	svc, rw := makeServiceWithDB(t)
	alice := testutil.SeedOwner(t, rw, "local", "alice")
	bob := testutil.SeedOwner(t, rw, "local", "bob")
	bMid := testutil.SeedPhoto(t, rw, bob, "b1")
	r.NoError(svc.Acknowledge(ctx, alice))

	failR := failures.NewRepo(rw, rw)
	tagFP := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}
	r.NoError(failR.Record(ctx, bMid, ai.TaskTag, tagFP, ai.ErrKindMalformed, "y", 1))

	err := svc.RetryPhoto(ctx, alice, bMid, ai.TaskTag)
	r.ErrorIs(err, errs.ErrNotFound)

	// Bob's failure row must still be present.
	rows, err := failR.ListForFingerprintByOwner(ctx, ai.TaskTag, tagFP, bob.Hub, bob.UserID, time.Time{}, 0)
	r.NoError(err)
	r.Len(rows, 1)
}

func TestListFailuresScopedToCaller(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	svc, rw := makeServiceWithDB(t)
	alice := testutil.SeedOwner(t, rw, "local", "alice")
	bob := testutil.SeedOwner(t, rw, "local", "bob")
	aMid := testutil.SeedPhoto(t, rw, alice, "a1")
	bMid := testutil.SeedPhoto(t, rw, bob, "b1")

	failR := failures.NewRepo(rw, rw)
	tagFP := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}
	r.NoError(failR.Record(ctx, aMid, ai.TaskTag, tagFP, ai.ErrKindMalformed, "x", 1))
	r.NoError(failR.Record(ctx, bMid, ai.TaskTag, tagFP, ai.ErrKindMalformed, "y", 1))

	rows, err := svc.ListFailures(ctx, alice, ai.TaskTag, 100)
	r.NoError(err)
	r.Len(rows, 1)
	r.Equal(aMid, rows[0].MediaID)
}

// RetryFailed must leave failures newer than its cutoff for the next call.
func TestRetryFailedSnapshotIgnoresFailuresNewerThanCutoff(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	svc, rw := makeServiceWithDB(t)
	alice := testutil.SeedOwner(t, rw, "local", "alice")
	mid := testutil.SeedPhoto(t, rw, alice, "p1")
	r.NoError(svc.Acknowledge(ctx, alice))

	failR := failures.NewRepo(rw, rw)
	tagFP := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}
	r.NoError(failR.Record(ctx, mid, ai.TaskTag, tagFP, ai.ErrKindMalformed, "x", 1))

	// Place the recorded timestamps on either side of the service's cutoff.
	// Complete both writes before retrying; sleeping cannot establish ordering
	// between a background insert and the service or the final assertion.
	mid2 := testutil.SeedPhoto(t, rw, alice, "p2")
	r.NoError(failR.Record(ctx, mid2, ai.TaskTag, tagFP, ai.ErrKindMalformed, "y", 1))
	now := time.Now().UTC()
	_, err := rw.ExecContext(ctx, `UPDATE ai_failures SET failed_at = ? WHERE media_id = ?`, now.Add(-time.Hour), mid)
	r.NoError(err)
	_, err = rw.ExecContext(ctx, `UPDATE ai_failures SET failed_at = ? WHERE media_id = ?`, now.Add(time.Hour), mid2)
	r.NoError(err)

	n, err := svc.RetryFailed(ctx, alice, ai.TaskTag)
	r.NoError(err)
	// Only the original failure (mid) should have been retried; mid2's
	// post-cutoff failure must remain in the table for a future call.
	r.Equal(1, n, "snapshot retry must touch only the original failure")

	// The newer failure remains recorded for a later retry.
	rows, err := failR.ListForFingerprintByOwner(ctx, ai.TaskTag, tagFP, alice.Hub, alice.UserID, time.Time{}, 0)
	r.NoError(err)
	r.Len(rows, 1, "the post-cutoff failure must remain pending for the next retry")
	r.Equal(mid2, rows[0].MediaID)
}

func TestServiceRejectsZeroPrincipal(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	svc, _ := makeServiceWithDB(t)
	zero := owners.Principal{}

	_, err := svc.Backfill(ctx, zero, ai.TaskTag, false)
	r.ErrorIs(err, errs.ErrPermissionDenied)
	_, err = svc.RetryFailed(ctx, zero, ai.TaskTag)
	r.ErrorIs(err, errs.ErrPermissionDenied)
	r.ErrorIs(svc.RetryPhoto(ctx, zero, "mid", ai.TaskTag), errs.ErrPermissionDenied)
	_, err = svc.ListFailures(ctx, zero, ai.TaskTag, 10)
	r.ErrorIs(err, errs.ErrPermissionDenied)
	_, err = svc.IsAcknowledged(ctx, zero)
	r.ErrorIs(err, errs.ErrPermissionDenied)
	r.ErrorIs(svc.Acknowledge(ctx, zero), errs.ErrPermissionDenied)
	_, err = svc.MediaView(ctx, zero, "mid", false)
	r.ErrorIs(err, errs.ErrPermissionDenied)
}

func TestMediaViewPopulatesAllSurfaces(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	svc, mediaCheck, rw := makeServiceWithMedia(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	mid := testutil.SeedPhoto(t, rw, owner, "p1")
	mediaCheck.add(mid, owner, false)

	tagFP := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}
	captionFP := ai.Fingerprint{ModelID: "m", PromptVersion: "caption-v1", InputProfile: "ip"}

	resR := results.NewRepo(rw, rw)
	r.NoError(resR.WriteTagResult(ctx, mid, tagFP, "thash", []parse.Tag{
		{Key: "dog", Label: "Dog", Rank: 1},
		{Key: "beach", Label: "Beach", Rank: 2},
	}))
	r.NoError(resR.WriteCaptionResult(ctx, mid, captionFP, "chash", "A small dog on a beach."))

	failR := failures.NewRepo(rw, rw)
	r.NoError(failR.Record(ctx, mid, ai.TaskTag, tagFP, ai.ErrKindMalformed, "bad json", 2))

	view, err := svc.MediaView(ctx, owner, mid, false)
	r.NoError(err)
	r.Len(view.Tags, 2)
	r.Equal("dog", view.Tags[0].Key)
	r.Equal("Dog", view.Tags[0].Label)
	r.Equal(1, view.Tags[0].Rank)
	r.NotNil(view.Caption)
	r.Equal("A small dog on a beach.", view.Caption.Text)
	r.Equal("m", view.Caption.ModelID)
	r.Equal("caption-v1", view.Caption.PromptVersion)
	r.False(view.Caption.GeneratedAt.IsZero())
	r.NotNil(view.TagFailure)
	r.Equal("bad json", view.TagFailure.Message)
	r.Equal(string(ai.ErrKindMalformed), view.TagFailure.Kind)
	r.Nil(view.CaptionFailure)
	r.Nil(view.Skipped)
}

func TestMediaViewSurfacesSkipReason(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	svc, mediaCheck, rw := makeServiceWithMedia(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	mid := testutil.SeedPhoto(t, rw, owner, "p1")
	mediaCheck.add(mid, owner, false)

	skipR := skipped.NewRepo(rw, rw)
	r.NoError(skipR.Record(ctx, mid, ai.TaskTag, "video"))

	view, err := svc.MediaView(ctx, owner, mid, false)
	r.NoError(err)
	r.NotNil(view.Skipped)
	r.Equal("video", view.Skipped.Reason)
	r.Empty(view.Tags)
	r.Nil(view.Caption)
}

// TestMediaViewMissingMediaReturnsNotFound verifies the gate fires
// before any AI table is read: an unknown id propagates errs.ErrNotFound
// so the caller cannot probe for cross-owner or non-existent rows.
// (Previously, MediaView returned an empty MediaView for any id.)
func TestMediaViewMissingMediaReturnsNotFound(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	svc, _, rw := makeServiceWithMedia(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")

	_, err := svc.MediaView(ctx, owner, "missing", false)
	r.ErrorIs(err, errs.ErrNotFound)
}

// TestMediaViewRejectsCrossOwnerReads verifies the High-severity fix:
// even authenticated callers cannot fetch AI artifacts for media owned
// by another principal. The gate maps to errs.ErrNotFound (not
// ErrPermissionDenied) to preserve the anti-enumeration convention.
func TestMediaViewRejectsCrossOwnerReads(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	svc, mediaCheck, rw := makeServiceWithMedia(t)
	alice := testutil.SeedOwner(t, rw, "local", "alice")
	bob := testutil.SeedOwner(t, rw, "local", "bob")
	bobMid := testutil.SeedPhoto(t, rw, bob, "b1")
	mediaCheck.add(bobMid, bob, false)

	// Seed a tag result on Bob's photo so the gate is the only thing
	// preventing Alice from reading it.
	resR := results.NewRepo(rw, rw)
	tagFP := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}
	r.NoError(resR.WriteTagResult(ctx, bobMid, tagFP, "h",
		[]parse.Tag{{Key: "x", Label: "x", Rank: 1}}))

	_, err := svc.MediaView(ctx, alice, bobMid, false)
	r.ErrorIs(err, errs.ErrNotFound)
}

// TestMediaViewHiddenRowGatedOnIncludeHidden verifies the second half
// of the High finding: a hidden media's AI artifacts are reachable only
// when includeHidden=true (i.e. when the HTTP layer saw a valid
// hidden-unlock claim). Without the unlock the gate returns
// errs.ErrNotFound.
func TestMediaViewHiddenRowGatedOnIncludeHidden(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	svc, mediaCheck, rw := makeServiceWithMedia(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	mid := testutil.SeedPhoto(t, rw, owner, "p-hidden")
	mediaCheck.add(mid, owner, true) // hidden=true

	resR := results.NewRepo(rw, rw)
	tagFP := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}
	r.NoError(resR.WriteTagResult(ctx, mid, tagFP, "h",
		[]parse.Tag{{Key: "secret", Label: "secret", Rank: 1}}))

	// Without the unlock claim, the gate hides the row.
	_, err := svc.MediaView(ctx, owner, mid, false)
	r.ErrorIs(err, errs.ErrNotFound)

	// With includeHidden=true, the artifacts are visible.
	view, err := svc.MediaView(ctx, owner, mid, true)
	r.NoError(err)
	r.Len(view.Tags, 1)
	r.Equal("secret", view.Tags[0].Key)
}
