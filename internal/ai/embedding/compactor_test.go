package embedding_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/ai"
	"github.com/wesm/fotobank/internal/ai/embedding"
	"github.com/wesm/fotobank/internal/db"
	"github.com/wesm/fotobank/internal/errs"
	"github.com/wesm/fotobank/internal/testutil"
)

// mustCreateRetiredGenAt drives the E1 Generations repo to insert a
// fresh generation row, promote it (so it carries activated_at), then
// retires it and back-dates retired_at to the supplied timestamp via
// a direct UPDATE.
//
// The standard Retire stamps retired_at = time.Now(), which makes
// "retired more than the window ago" impossible to express through the
// public API alone. Manually back-dating the column keeps the helper
// terse and avoids leaking a test-only insertion path through the repo.
//
// A unique fingerprint per call keeps every test independent. Returns
// the generation row including the populated VecTableName.
func mustCreateRetiredGenAt(t *testing.T, d *db.DB, dim int, retiredAt time.Time) embedding.Row {
	t.Helper()
	ctx := context.Background()
	g := embedding.NewGenerations(d.WriteDB(), d.ReadDB())
	fp := ai.Fingerprint{
		ModelID:      "siglip2",
		InputProfile: "compactor-" + t.Name(),
	}
	row, err := g.FindOrCreateBuilding(ctx, fp, dim)
	require.NoError(t, err)
	require.NoError(t, g.Promote(ctx, row.ID))
	require.NoError(t, g.Retire(ctx, row.ID))
	// Back-date retired_at directly. Must be UTC to match the canonical
	// shape every other writer uses (Promote/Retire all stamp UTC).
	_, err = d.WriteDB().ExecContext(ctx,
		`UPDATE embedding_generations SET retired_at=? WHERE id=?`,
		retiredAt.UTC(), row.ID,
	)
	require.NoError(t, err)
	// Re-read so the returned Row carries the back-dated retired_at.
	got, err := g.GetByID(ctx, row.ID)
	require.NoError(t, err)
	return *got
}

// requireTableDoesNotExist asserts that no row in sqlite_master matches
// the supplied name (across both regular and virtual tables — vec0
// surfaces the parent virtual-table row plus shadow tables, so a single
// name match is enough to prove the DROP fired).
func requireTableDoesNotExist(t *testing.T, d *db.DB, name string) {
	t.Helper()
	var count int
	require.NoError(t, d.ReadDB().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`,
		name,
	).Scan(&count))
	require.Equal(t, 0, count, "table %q should have been dropped", name)
}

// TestCompactor_DropsRetiredOlderThanWindow exercises the happy path:
// a retired generation older than the retain-retired window is gone
// after one sweep — both the registry row and the per-generation vec0
// table.
func TestCompactor_DropsRetiredOlderThanWindow(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	d := testutil.OpenTestDB(t)

	retainWindow := 30 * 24 * time.Hour
	retiredAt := time.Now().Add(-31 * 24 * time.Hour) // outside the window
	row := mustCreateRetiredGenAt(t, d, 768, retiredAt)

	c := embedding.NewCompactor(d.WriteDB(), retainWindow)
	dropped, err := c.SweepOnce(ctx)
	r.NoError(err)
	r.Equal(1, dropped, "the retired generation should be compacted")

	// Registry row gone.
	g := embedding.NewGenerations(d.WriteDB(), d.ReadDB())
	_, err = g.GetByID(ctx, row.ID)
	r.ErrorIs(err, errs.ErrNotFound)

	// Per-generation vec0 table gone.
	requireTableDoesNotExist(t, d, row.VecTableName)
}

// TestCompactor_LeavesRecentRetiredAlone exercises the negative path:
// a retired generation inside the retain-retired window must be left
// untouched, so an admin can still inspect or re-promote it.
func TestCompactor_LeavesRecentRetiredAlone(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	d := testutil.OpenTestDB(t)

	retainWindow := 30 * 24 * time.Hour
	retiredAt := time.Now().Add(-5 * 24 * time.Hour) // well inside the window
	row := mustCreateRetiredGenAt(t, d, 768, retiredAt)

	c := embedding.NewCompactor(d.WriteDB(), retainWindow)
	dropped, err := c.SweepOnce(ctx)
	r.NoError(err)
	r.Equal(0, dropped, "a recently-retired generation must not be compacted")

	// Registry row stays.
	g := embedding.NewGenerations(d.WriteDB(), d.ReadDB())
	got, err := g.GetByID(ctx, row.ID)
	r.NoError(err)
	r.NotNil(got)
	r.Equal("retired", got.State)
}
