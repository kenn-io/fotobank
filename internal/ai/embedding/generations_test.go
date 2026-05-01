package embedding_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/ai"
	"github.com/wesm/fotobank/internal/ai/embedding"
	"github.com/wesm/fotobank/internal/errs"
	"github.com/wesm/fotobank/internal/testutil"
)

// fpEmbed1 is the canonical example fingerprint from the design — used
// across multiple tests so a typo in one ModelID/InputProfile doesn't
// silently change what's being asserted.
func fpEmbed1() ai.Fingerprint {
	return ai.Fingerprint{
		ModelID:       "siglip2",
		PromptVersion: "",
		InputProfile:  "jpeg-384-q85-metadata-stripped-embed-v1",
	}
}

func TestGenerations_FindOrCreateBuilding_CreatesOnce(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	d := testutil.OpenTestDB(t)
	g := embedding.NewGenerations(d.WriteDB(), d.ReadDB())

	row1, err := g.FindOrCreateBuilding(ctx, fpEmbed1(), 768)
	r.NoError(err)
	r.Equal("building", row1.State)
	r.Equal(fmt.Sprintf("media_embeddings_g%d", row1.ID), row1.VecTableName)
	r.Equal(768, row1.Dimension)
	r.Equal("siglip2", row1.ModelID)
	r.Equal("jpeg-384-q85-metadata-stripped-embed-v1", row1.InputProfile)

	row2, err := g.FindOrCreateBuilding(ctx, fpEmbed1(), 768)
	r.NoError(err)
	r.Equal(row1.ID, row2.ID, "second call must return the same row")
}

func TestGenerations_FindActive_ReturnsNoneInitially(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	d := testutil.OpenTestDB(t)
	g := embedding.NewGenerations(d.WriteDB(), d.ReadDB())

	got, err := g.FindActive(ctx)
	r.NoError(err)
	r.Nil(got, "no active generation initially")
}

// TestGenerations_FindBuilding covers the activator's lookup path.
// The schema's embedding_generations_one_building partial unique
// index enforces at most one building row at a time, so the test
// rotates through the lifecycle: nil → one building → promote →
// FindBuilding nil again until the next FindOrCreate, → retire,
// nil. The ORDER BY id ASC LIMIT 1 in the implementation is
// defensive — should the schema constraint ever be lifted, the
// activator still picks the oldest candidate.
func TestGenerations_FindBuilding(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	d := testutil.OpenTestDB(t)
	g := embedding.NewGenerations(d.WriteDB(), d.ReadDB())

	// Empty registry: nil, no error.
	got, err := g.FindBuilding(ctx)
	r.NoError(err)
	r.Nil(got)

	// One building row: returns it.
	a, err := g.FindOrCreateBuilding(ctx,
		ai.Fingerprint{ModelID: "v1", InputProfile: "p1"}, 768)
	r.NoError(err)

	got, err = g.FindBuilding(ctx)
	r.NoError(err)
	r.NotNil(got)
	r.Equal(a.ID, got.ID)

	// Promote the building row → state moves to 'active'. The
	// one-building partial unique index now permits a fresh
	// building row for a different fingerprint.
	r.NoError(g.Promote(ctx, a.ID))
	got, err = g.FindBuilding(ctx)
	r.NoError(err)
	r.Nil(got, "no building rows after the only candidate was promoted")

	b, err := g.FindOrCreateBuilding(ctx,
		ai.Fingerprint{ModelID: "v2", InputProfile: "p2"}, 768)
	r.NoError(err)
	r.NotEqual(a.ID, b.ID)

	got, err = g.FindBuilding(ctx)
	r.NoError(err)
	r.NotNil(got)
	r.Equal(b.ID, got.ID)

	// Retire the last building; FindBuilding goes back to nil.
	r.NoError(g.Retire(ctx, b.ID))
	got, err = g.FindBuilding(ctx)
	r.NoError(err)
	r.Nil(got, "no building rows after retire")
}

func TestGenerations_PromoteRetiresPriorActive(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	d := testutil.OpenTestDB(t)
	g := embedding.NewGenerations(d.WriteDB(), d.ReadDB())

	a, err := g.FindOrCreateBuilding(ctx,
		ai.Fingerprint{ModelID: "v1", InputProfile: "p1"}, 768)
	r.NoError(err)
	r.NoError(g.Promote(ctx, a.ID))

	b, err := g.FindOrCreateBuilding(ctx,
		ai.Fingerprint{ModelID: "v2", InputProfile: "p2"}, 768)
	r.NoError(err)
	r.NoError(g.Promote(ctx, b.ID))

	active, err := g.FindActive(ctx)
	r.NoError(err)
	r.NotNil(active)
	r.Equal(b.ID, active.ID)

	rows, err := g.List(ctx, "retired")
	r.NoError(err)
	r.Len(rows, 1)
	r.Equal(a.ID, rows[0].ID)
	r.NotNil(rows[0].RetiredAt, "retired row must carry retired_at timestamp")
}

func TestGenerations_VecTableIsCreated(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	d := testutil.OpenTestDB(t)
	g := embedding.NewGenerations(d.WriteDB(), d.ReadDB())

	row, err := g.FindOrCreateBuilding(ctx, fpEmbed1(), 768)
	r.NoError(err)

	// Both 'table' and 'virtual' types resolve under sqlite_master; the
	// vec0 module surfaces the shadow table as a regular table entry
	// alongside the parent virtual-table row. Querying by name is enough
	// to confirm CREATE VIRTUAL TABLE ran inside the FindOrCreate tx.
	var name string
	err = d.ReadDB().QueryRowContext(ctx,
		`SELECT name FROM sqlite_master WHERE type='table' AND name=?`,
		row.VecTableName,
	).Scan(&name)
	r.NoError(err)
	r.Equal(row.VecTableName, name)
}

func TestGenerations_RetireTransitionsToRetired(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	d := testutil.OpenTestDB(t)
	g := embedding.NewGenerations(d.WriteDB(), d.ReadDB())

	row, err := g.FindOrCreateBuilding(ctx, fpEmbed1(), 768)
	r.NoError(err)

	r.NoError(g.Retire(ctx, row.ID))

	retired, err := g.List(ctx, "retired")
	r.NoError(err)
	r.Len(retired, 1)
	r.Equal(row.ID, retired[0].ID)
	r.NotNil(retired[0].RetiredAt, "retired row must carry retired_at timestamp")

	// FindActive must remain nil — Retire does not promote anything.
	active, err := g.FindActive(ctx)
	r.NoError(err)
	r.Nil(active)
}

// TestGenerations_PromoteUnknownIDFails covers the rows-affected guard
// on the activation UPDATE: passing an id that doesn't exist must
// return errs.ErrNotFound and roll back the transaction so a prior
// active row stays active.
func TestGenerations_PromoteUnknownIDFails(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	d := testutil.OpenTestDB(t)
	g := embedding.NewGenerations(d.WriteDB(), d.ReadDB())

	// Empty registry: Promote on a missing id is ErrNotFound.
	err := g.Promote(ctx, 99999)
	r.ErrorIs(err, errs.ErrNotFound)
	active, err := g.FindActive(ctx)
	r.NoError(err)
	r.Nil(active, "no row should have transitioned to active")

	// With a prior active row: Promote on a missing id must NOT retire
	// the prior active. The two UPDATEs run in one tx; the activation's
	// zero rows-affected rolls back the retire-prior-active UPDATE too.
	prior, err := g.FindOrCreateBuilding(ctx,
		ai.Fingerprint{ModelID: "v1", InputProfile: "p1"}, 768)
	r.NoError(err)
	r.NoError(g.Promote(ctx, prior.ID))

	err = g.Promote(ctx, 99999)
	r.ErrorIs(err, errs.ErrNotFound)

	active, err = g.FindActive(ctx)
	r.NoError(err)
	r.NotNil(active, "prior active must remain active after a failed Promote")
	r.Equal(prior.ID, active.ID)
	r.Nil(active.RetiredAt, "prior active must not carry a retired_at after rollback")
}

// TestGenerations_PromoteRetiredRowClearsRetiredAt covers the second
// half of Promote's contract: when re-promoting a row that was
// previously retired, the activation UPDATE must clear retired_at so
// the row lands back in the canonical active shape.
func TestGenerations_PromoteRetiredRowClearsRetiredAt(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	d := testutil.OpenTestDB(t)
	g := embedding.NewGenerations(d.WriteDB(), d.ReadDB())

	row, err := g.FindOrCreateBuilding(ctx,
		ai.Fingerprint{ModelID: "v1", InputProfile: "p1"}, 768)
	r.NoError(err)
	r.NoError(g.Promote(ctx, row.ID))
	r.NoError(g.Retire(ctx, row.ID))

	// Sanity: the row carries a retired_at after Retire.
	retired, err := g.List(ctx, "retired")
	r.NoError(err)
	r.Len(retired, 1)
	r.NotNil(retired[0].RetiredAt)

	// Re-promote the same row. The activation UPDATE clears retired_at.
	r.NoError(g.Promote(ctx, row.ID))

	active, err := g.FindActive(ctx)
	r.NoError(err)
	r.NotNil(active)
	r.Equal(row.ID, active.ID)
	r.Nil(active.RetiredAt, "re-promoted row must have retired_at cleared")
	r.NotNil(active.ActivatedAt)
}

// TestGenerations_FindOrCreateBuilding_ConcurrentSafety confirms the
// idempotency contract under a fan-out of N goroutines all racing to
// create the same fingerprint. The contention model relies on the rw
// pool's MaxOpenConns=1: BeginTx physically queues on one connection,
// so the loser's tx cannot start until the winner's Commit returns the
// connection — at which point the loser's re-check inside the tx sees
// the just-committed row and short-circuits without retrying the
// INSERT (which would fail the fingerprint_hash UNIQUE constraint).
//
// `start` is closed only after every goroutine is spawned, so the
// FindOrCreateBuilding calls all attempt to begin transactions at
// roughly the same instant. Without the barrier, fast spawn-then-run
// goroutines could naturally serialise (g0 finishes before g1 starts),
// hiding any correctness regression in the rw-contention path.
func TestGenerations_FindOrCreateBuilding_ConcurrentSafety(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	d := testutil.OpenTestDB(t)
	g := embedding.NewGenerations(d.WriteDB(), d.ReadDB())
	fp := ai.Fingerprint{ModelID: "siglip2", InputProfile: "p"}

	const N = 8
	ids := make([]int64, N)
	errs := make([]error, N)
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(N)
	for i := range N {
		go func() {
			defer wg.Done()
			<-start // barrier — all goroutines released together
			row, err := g.FindOrCreateBuilding(ctx, fp, 768)
			ids[i], errs[i] = row.ID, err
		}()
	}
	close(start) // release barrier
	wg.Wait()

	for i, e := range errs {
		r.NoError(e, "goroutine %d", i)
	}
	for i := 1; i < N; i++ {
		r.Equal(ids[0], ids[i], "goroutine %d returned a different id", i)
	}

	// Sanity-check the registry: exactly one row exists for this fingerprint.
	rows, err := g.List(ctx, "building")
	r.NoError(err)
	r.Len(rows, 1)
	r.Equal(ids[0], rows[0].ID)
}
