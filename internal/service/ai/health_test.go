package ai_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/ai"
	"github.com/wesm/fotobank/internal/ai/ack"
	"github.com/wesm/fotobank/internal/ai/embedding"
	"github.com/wesm/fotobank/internal/ai/failures"
	"github.com/wesm/fotobank/internal/ai/gapscanner"
	"github.com/wesm/fotobank/internal/ai/jobs"
	"github.com/wesm/fotobank/internal/ai/results"
	airuntime "github.com/wesm/fotobank/internal/ai/runtime"
	"github.com/wesm/fotobank/internal/ai/skipped"
	"github.com/wesm/fotobank/internal/owners"
	aiservice "github.com/wesm/fotobank/internal/service/ai"
	"github.com/wesm/fotobank/internal/testutil"
)

type stubProbe struct {
	err error
}

func (s stubProbe) Probe(_ context.Context) error { return s.err }

func TestHealthDisabled(t *testing.T) {
	svc, _ := makeServiceWithDB(t)
	h := svc.Health(context.Background(), owners.Principal{Hub: "local", UserID: "alice"},
		aiservice.HealthInput{Enabled: false, Probe: stubProbe{}})
	require.False(t, h.Enabled)
	require.Equal(t, "config_disabled", h.PausedReason)
}

func TestHealthUsesRuntimeEnabledAndFingerprints(t *testing.T) {
	rw, ro := testutil.OpenTestDBPair(t)
	resultFP := ai.Fingerprint{ModelID: "runtime-model", PromptVersion: "tags-v1", InputProfile: "ip"}
	svc := aiservice.New(aiservice.Deps{
		Queue:    jobs.NewQueue(rw, ro),
		Results:  results.NewRepo(rw, ro),
		Failures: failures.NewRepo(rw, ro),
		Skipped:  skipped.NewRepo(rw, ro),
		Ack:      ack.New(rw, ro),
		Runtime: fakeRuntimeProvider{snap: airuntime.Snapshot{
			Config: ai.Config{Enabled: true, Tag: ai.TaskConfig{Enabled: true}},
			Claim:  airuntime.ClaimFingerprints{Tag: "claim-runtime"},
			Result: airuntime.ResultFingerprints{Tag: resultFP},
		}},
	})

	h := svc.Health(context.Background(), owners.Principal{Hub: "local", UserID: "alice"},
		aiservice.HealthInput{Enabled: false, Probe: stubProbe{}})
	require.True(t, h.Enabled)
	require.Equal(t, resultFP.String(), h.Tag.ActiveFingerprint)
}

func TestHealthAcknowledgementRequired(t *testing.T) {
	svc, _ := makeServiceWithDB(t)
	h := svc.Health(context.Background(), owners.Principal{Hub: "local", UserID: "alice"},
		aiservice.HealthInput{Enabled: true, Probe: stubProbe{}})
	require.True(t, h.Enabled)
	require.Equal(t, "acknowledgement_required", h.PausedReason)
}

func TestHealthReachable(t *testing.T) {
	r := require.New(t)
	svc, rw := makeServiceWithDB(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	r.NoError(svc.Acknowledge(context.Background(), owner))

	h := svc.Health(context.Background(), owner, aiservice.HealthInput{
		Enabled: true, Probe: stubProbe{},
	})
	r.Empty(h.PausedReason)
	r.True(h.Vision.Reachable)
	r.Equal("m|tags-v1|ip", h.Tag.ActiveFingerprint)
}

func TestHealthUnreachable(t *testing.T) {
	r := require.New(t)
	svc, rw := makeServiceWithDB(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	r.NoError(svc.Acknowledge(context.Background(), owner))

	h := svc.Health(context.Background(), owner, aiservice.HealthInput{
		Enabled: true, Probe: stubProbe{err: errors.New("connection refused")},
	})
	r.False(h.Vision.Reachable)
	r.Equal("connection refused", h.Vision.LastError)
}

func TestHealthCountersScopedToCaller(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	svc, rw := makeServiceWithDB(t)
	alice := testutil.SeedOwner(t, rw, "local", "alice")
	bob := testutil.SeedOwner(t, rw, "local", "bob")
	r.NoError(svc.Acknowledge(ctx, alice))
	r.NoError(svc.Acknowledge(ctx, bob))

	tagFP := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}
	// Two pending tag jobs for bob; none for alice.
	for _, label := range []string{"bob-1", "bob-2"} {
		mid := testutil.SeedPhoto(t, rw, bob, label)
		_, err := rw.ExecContext(ctx,
			`INSERT INTO ai_jobs(id, media_id, task, fingerprint, status, attempts, enqueued_at)
			 VALUES (?,?,?,?, 'pending', 0, datetime('now'))`,
			mid+"-job", mid, "tag", tagFP.String())
		r.NoError(err)
	}

	hAlice := svc.Health(ctx, alice, aiservice.HealthInput{Enabled: true, Probe: stubProbe{}})
	r.Equal(0, hAlice.Tag.Pending, "alice must not see bob's pending jobs")

	hBob := svc.Health(ctx, bob, aiservice.HealthInput{Enabled: true, Probe: stubProbe{}})
	r.Equal(2, hBob.Tag.Pending, "bob sees his own pending jobs")
}

func TestHealthNilProbeDoesNotPanic(t *testing.T) {
	r := require.New(t)
	svc, rw := makeServiceWithDB(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	r.NoError(svc.Acknowledge(context.Background(), owner))

	h := svc.Health(context.Background(), owner, aiservice.HealthInput{
		Enabled: true, Probe: nil,
	})
	r.False(h.Vision.Reachable)
	r.Equal("probe not configured", h.Vision.LastError)
}

// embedFP is the canonical embed fingerprint used across the
// embed-task health tests. Empty PromptVersion mirrors the embed
// pipeline's actual shape (embeddings have no prompt).
func embedFP() ai.Fingerprint {
	return ai.Fingerprint{
		ModelID:      "siglip2",
		InputProfile: "jpeg-384-q85-metadata-stripped-embed-v1",
	}
}

// embedHealthFixture wires a service with the embed deps populated:
// the activator (so EligibleCount/EmbeddedCount are real) and the SQL
// generations lister (so the panel sees the actual embedding_generations
// rows). Returns the wired service plus the owner and rw/ro handles so
// the caller can stage data.
func embedHealthFixture(t *testing.T) (*aiservice.Service, owners.Principal, *sql.DB, *sql.DB) {
	t.Helper()
	d := testutil.OpenTestDB(t)
	rwDB, roDB := d.WriteDB(), d.ReadDB()
	owner := testutil.SeedOwner(t, rwDB, "local", "alice")

	q := jobs.NewQueue(rwDB, roDB)
	resR := results.NewRepo(rwDB, roDB)
	failR := failures.NewRepo(rwDB, roDB)
	skipR := skipped.NewRepo(rwDB, roDB)
	ackS := ack.New(rwDB, roDB)
	gs := gapscanner.New(roDB, q, resR, skipR)

	gens := embedding.NewGenerations(rwDB, roDB)
	activator := embedding.NewActivator(roDB, gens, ackS, nil, nil, embedding.ActivatorCfg{
		Principal: owner, ThresholdPct: 95, Tick: 50 * time.Millisecond,
	})

	svc := aiservice.New(aiservice.Deps{
		Queue: q, Results: resR, Failures: failR, Skipped: skipR,
		Ack: ackS, Gap: gs, Media: newFakeMediaCheck(),
		ConfigFingerprints: aiservice.ConfigFingerprints{
			Tag:     ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"},
			Caption: ai.Fingerprint{ModelID: "m", PromptVersion: "caption-v1", InputProfile: "ip"},
			Embed:   embedFP(),
		},
		EmbeddingActivator:   activator,
		EmbeddingGenerations: &aiservice.SQLEmbeddingGenerationsLister{RO: roDB},
	})
	return svc, owner, rwDB, roDB
}

// TestHealthEmbedPausedReasonReflectsAck verifies that the embed
// sub-block surfaces "acknowledgement_required" when the principal has
// not acked, and the empty string once they have. The activator is
// gated on the same condition (see TestActivator_PausedOnAckRequired) so
// the panel's "embed paused" pill matches the activator's behaviour.
func TestHealthEmbedPausedReasonReflectsAck(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	svc, owner, _, _ := embedHealthFixture(t)

	// Without ack, embed.paused_reason must be acknowledgement_required.
	h := svc.Health(ctx, owner, aiservice.HealthInput{Enabled: true, Probe: stubProbe{}})
	r.Equal("acknowledgement_required", h.Embed.PausedReason)

	// Same fingerprint surfaced for the panel to display.
	r.Equal(embedFP().String(), h.Embed.ActiveFingerprint)

	// After ack, the field clears.
	r.NoError(svc.Acknowledge(ctx, owner))
	h2 := svc.Health(ctx, owner, aiservice.HealthInput{Enabled: true, Probe: stubProbe{}})
	r.Empty(h2.Embed.PausedReason)
}

// TestHealthEmbeddingGenerationsListsBuildingAndActive verifies that
// the embedding_generations summary block returns both states, with
// EligibleCount populated from the activator's exported helper. The
// test stages 20 ready media, 19 mappings on a building generation
// (exactly at the 95% threshold, but the activator is not invoked
// here — we only verify the summary's display values), then asks the
// service for the health payload.
func TestHealthEmbeddingGenerationsListsBuildingAndActive(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	svc, owner, rw, ro := embedHealthFixture(t)
	r.NoError(svc.Acknowledge(ctx, owner))

	// 20 thumb-ready media → eligible_count = 20.
	mids := make([]string, 20)
	for i := range mids {
		mids[i] = testutil.SeedPhoto(t, rw, owner, "p"+pad(i))
	}

	// One previously-active generation under a different fingerprint
	// (simulates a model swap mid-rollout) — promoted FIRST so the
	// active state is committed before the new building row appears.
	gens := embedding.NewGenerations(rw, ro)
	priorFP := ai.Fingerprint{ModelID: "siglip1", InputProfile: "v0"}
	prior, err := gens.FindOrCreateBuilding(ctx, priorFP, 512)
	r.NoError(err)
	r.NoError(gens.Promote(ctx, prior.ID))

	// Then a fresh building row under the embed fingerprint with 19
	// mappings (95%). The order here matters: FindOrCreateBuilding only
	// inserts a building row if no row matching the fingerprint already
	// exists, so we use distinct fingerprints to keep the two rows live.
	building, err := gens.FindOrCreateBuilding(ctx, embedFP(), 768)
	r.NoError(err)
	for i := range 19 {
		_, err := rw.ExecContext(ctx,
			`INSERT INTO media_embedding_ids(generation_id, media_id, vec_id) VALUES (?,?,?)`,
			building.ID, mids[i], i+1)
		r.NoError(err)
	}

	h := svc.Health(ctx, owner, aiservice.HealthInput{Enabled: true, Probe: stubProbe{}})
	r.Len(h.EmbeddingGenerations, 2, "must list both building and active rows")

	// Find each by state.
	got := map[string]aiservice.EmbeddingGenerationSummary{}
	for _, g := range h.EmbeddingGenerations {
		got[g.State] = g
	}

	bs, ok := got["building"]
	r.True(ok, "building row must be present")
	r.Equal(embedFP().String(), bs.Fingerprint)
	r.Equal(20, bs.EligibleCount, "eligible_count must come from the activator's predicate")
	r.Equal(19, bs.EmbeddedCount, "embedded_count must be the recounted JOIN value")
	r.Nil(bs.ActivatedAt, "building row has no activated_at")

	as, ok := got["active"]
	r.True(ok, "active row must be present")
	r.Equal(priorFP.String(), as.Fingerprint)
	r.NotNil(as.ActivatedAt, "active row carries activated_at")
	r.Equal(20, as.EligibleCount, "eligible is global per principal — same number for both rows")
	r.Equal(0, as.EmbeddedCount, "no mappings on the prior active → 0 embedded")
}

// TestHealthEmbeddingGenerationsEmptyWhenNoEmbedDeps verifies the
// degraded path: when EmbeddingGenerations / EmbeddingActivator are
// nil (e.g. tests that exercise only tag/caption flows), the summary
// block is an empty slice, not nil. The panel renders an empty list
// without a JSON null shape change.
func TestHealthEmbeddingGenerationsEmptyWhenNoEmbedDeps(t *testing.T) {
	r := require.New(t)
	svc, rw := makeServiceWithDB(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	r.NoError(svc.Acknowledge(context.Background(), owner))

	h := svc.Health(context.Background(), owner, aiservice.HealthInput{Enabled: true, Probe: stubProbe{}})
	r.NotNil(h.EmbeddingGenerations, "must return a slice, not nil")
	r.Empty(h.EmbeddingGenerations, "no embed deps → empty list")
}

// pad zero-pads i to 2 digits so tests don't depend on insert order
// matching alphabetical sort. (Kept local to avoid pulling strconv
// into health_test for two callsites.)
func pad(i int) string {
	return fmt.Sprintf("%02d", i)
}
