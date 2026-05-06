package appsettings_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/appsettings"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/testutil"
)

func TestRepoUpsertGetListDelete(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	d := testutil.OpenTestDB(t)
	repo := appsettings.NewRepo(d.WriteDB(), d.ReadDB())
	by := owners.Principal{Hub: "local", UserID: "admin"}

	require.NoError(repo.Upsert(ctx, "ai.enabled", "true", &by))

	row, ok, err := repo.Get(ctx, "ai.enabled")
	require.NoError(err)
	require.True(ok)
	require.Equal("ai.enabled", row.Key)
	require.Equal("true", row.Value)
	require.NotZero(row.UpdatedAt)
	require.NotNil(row.UpdatedByHub)
	require.NotNil(row.UpdatedByUserID)
	require.Equal("local", *row.UpdatedByHub)
	require.Equal("admin", *row.UpdatedByUserID)

	rows, err := repo.List(ctx)
	require.NoError(err)
	require.Len(rows, 1)
	require.Equal("ai.enabled", rows[0].Key)

	require.NoError(repo.Upsert(ctx, "ai.enabled", "false", nil))
	row, ok, err = repo.Get(ctx, "ai.enabled")
	require.NoError(err)
	require.True(ok)
	require.Equal("false", row.Value)
	require.Nil(row.UpdatedByHub)
	require.Nil(row.UpdatedByUserID)

	require.NoError(repo.Delete(ctx, "ai.enabled"))
	_, ok, err = repo.Get(ctx, "ai.enabled")
	require.NoError(err)
	require.False(ok)
}

func TestRepoDeleteMany(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	d := testutil.OpenTestDB(t)
	repo := appsettings.NewRepo(d.WriteDB(), d.ReadDB())

	require.NoError(repo.Upsert(ctx, "ai.enabled", "true", nil))
	require.NoError(repo.Upsert(ctx, "ai.tag.model", `"old"`, nil))
	require.NoError(repo.Upsert(ctx, "ai.caption.model", `"old"`, nil))

	require.NoError(repo.DeleteMany(ctx, []string{"ai.enabled", "ai.tag.model"}))

	rows, err := repo.List(ctx)
	require.NoError(err)
	require.Len(rows, 1)
	require.Equal("ai.caption.model", rows[0].Key)
}
