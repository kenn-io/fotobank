package ai_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.kenn.io/fotobank/internal/ai"
	"go.kenn.io/fotobank/internal/ai/ack"
	"go.kenn.io/fotobank/internal/ai/embedding"
	"go.kenn.io/fotobank/internal/ai/failures"
	"go.kenn.io/fotobank/internal/ai/gapscanner"
	"go.kenn.io/fotobank/internal/ai/jobs"
	"go.kenn.io/fotobank/internal/ai/results"
	airuntime "go.kenn.io/fotobank/internal/ai/runtime"
	"go.kenn.io/fotobank/internal/ai/skipped"
	"go.kenn.io/fotobank/internal/errs"
	aiservice "go.kenn.io/fotobank/internal/service/ai"
	"go.kenn.io/fotobank/internal/testutil"
)

func TestEmbeddingQueueUsesCurrentSettingsAndOwner(t *testing.T) {
	for _, retry := range []bool{false, true} {
		name := "backfill"
		if retry {
			name = "retry"
		}
		t.Run(name, func(t *testing.T) {
			r := require.New(t)
			ctx := t.Context()
			rw, ro := testutil.OpenTestDBPair(t)
			q := jobs.NewQueue(rw, ro)
			res := results.NewRepo(rw, ro)
			fail := failures.NewRepo(rw, ro)
			skipped := skipped.NewRepo(rw, ro)
			gens := embedding.NewGenerations(rw, ro)
			runtime := &fakeRuntimeProvider{}
			svc := aiservice.New(aiservice.Deps{
				Queue: q, Results: res, Failures: fail, Skipped: skipped,
				Ack: ack.New(rw, ro), Gap: gapscanner.New(ro, q, res, skipped),
				Runtime: runtime, Generations: gens,
			})
			alice := testutil.SeedOwner(t, rw, "local", "alice")
			bob := testutil.SeedOwner(t, rw, "local", "bob")
			aID := testutil.SeedPhoto(t, rw, alice, "a")
			bID := testutil.SeedPhoto(t, rw, bob, "b")
			r.NoError(svc.Acknowledge(ctx, alice))

			// Publish settings after constructing the service. Queueing is allowed
			// while the global processing switch is off.
			fp := ai.Fingerprint{ModelID: "new-model", InputProfile: "ip"}
			runtime.snap = airuntime.Snapshot{
				Config: ai.Config{Embed: ai.EmbedConfig{Enabled: true, Dimension: 8, MaxRetries: 1}},
				Claim:  airuntime.ClaimFingerprints{Embed: "new-claim"},
				Result: airuntime.ResultFingerprints{Embed: fp},
			}
			var n int
			var err error
			if retry {
				r.NoError(fail.Record(ctx, aID, ai.TaskEmbed, fp, ai.ErrKindTransient, "retry", 1))
				r.NoError(fail.Record(ctx, bID, ai.TaskEmbed, fp, ai.ErrKindTransient, "retry", 1))
				n, err = svc.RetryFailed(ctx, alice, ai.TaskEmbed)
			} else {
				n, err = svc.Backfill(ctx, alice, ai.TaskEmbed, false)
			}
			r.NoError(err)
			r.Equal(1, n)
			claims, err := q.ClaimBatchForFingerprint(ctx, ai.TaskEmbed, "new-claim", 10)
			r.NoError(err)
			r.Len(claims, 1)
			r.Equal(aID, claims[0].MediaID)
			rows, err := gens.List(ctx, "building")
			r.NoError(err)
			r.Len(rows, 1)
			r.Equal("new-model", rows[0].ModelID)
			r.Equal(8, rows[0].Dimension)
			if retry {
				remaining, err := fail.ListForFingerprintByOwner(ctx, ai.TaskEmbed, fp, bob.Hub, bob.UserID, time.Time{}, 0)
				r.NoError(err)
				r.Len(remaining, 1)
				r.Equal(bID, remaining[0].MediaID)
			}
			runtime.snap.Config.Embed.Enabled = false
			_, err = svc.Backfill(ctx, alice, ai.TaskEmbed, false)
			r.ErrorIs(err, errs.ErrInvalidArgument)
		})
	}
}
