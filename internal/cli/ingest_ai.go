package cli

import (
	"go.kenn.io/fotobank/internal/ai/jobs"
	airuntime "go.kenn.io/fotobank/internal/ai/runtime"
	"go.kenn.io/fotobank/internal/ai/skipped"
	"go.kenn.io/fotobank/internal/db"
	"go.kenn.io/fotobank/internal/ingest"
)

func newIngestAIEnqueuer(d *db.DB, snapshot airuntime.Snapshot) ingest.AIEnqueuer {
	aiQueue := jobs.NewQueue(d.WriteDB(), d.ReadDB())
	aiSkippedRepo := skipped.NewRepo(d.WriteDB(), d.ReadDB())
	enqueuer := ingest.NewRealAIEnqueuer(
		snapshot.Result.Tag,
		snapshot.Result.Caption,
		aiQueue.Enqueue,
		aiSkippedRepo.Record,
	).WithClaimFingerprints(
		snapshot.Claim.Tag,
		snapshot.Claim.Caption,
		aiQueue.EnqueueClaim,
	)
	if snapshot.Config.Embed.Enabled {
		enqueuer.WithEmbedClaim(snapshot.Result.Embed, snapshot.Claim.Embed)
	}
	return enqueuer
}
