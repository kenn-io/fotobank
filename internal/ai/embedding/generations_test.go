package embedding_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/ai"
	"github.com/wesm/fotobank/internal/ai/embedding"
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
