package ai_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/ai"
	"github.com/wesm/fotobank/internal/ai/ack"
	"github.com/wesm/fotobank/internal/ai/failures"
	"github.com/wesm/fotobank/internal/ai/gapscanner"
	"github.com/wesm/fotobank/internal/ai/jobs"
	"github.com/wesm/fotobank/internal/ai/results"
	"github.com/wesm/fotobank/internal/ai/skipped"
	"github.com/wesm/fotobank/internal/errs"
	"github.com/wesm/fotobank/internal/owners"
	aiservice "github.com/wesm/fotobank/internal/service/ai"
	"github.com/wesm/fotobank/internal/testutil"
)

func makeServiceWithDB(t *testing.T) (*aiservice.Service, *sql.DB) {
	t.Helper()
	rw, ro := testutil.OpenTestDBPair(t)
	q := jobs.NewQueue(rw, ro)
	resR := results.NewRepo(rw, ro)
	failR := failures.NewRepo(rw, ro)
	skipR := skipped.NewRepo(rw, ro)
	ackS := ack.New(rw, ro)
	gs := gapscanner.New(ro, q, resR, skipR)

	return aiservice.New(aiservice.Deps{
		Queue: q, Results: resR, Failures: failR, Skipped: skipR,
		Ack: ackS, Gap: gs,
		ConfigFingerprints: aiservice.ConfigFingerprints{
			Tag:     ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"},
			Caption: ai.Fingerprint{ModelID: "m", PromptVersion: "caption-v1", InputProfile: "ip"},
		},
	}), rw
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
	rows, err := failR.ListForFingerprintByOwner(ctx, ai.TaskTag, tagFP, bob.Hub, bob.UserID, 0)
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
	rows, err := failR.ListForFingerprintByOwner(ctx, ai.TaskTag, tagFP, bob.Hub, bob.UserID, 0)
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
}
