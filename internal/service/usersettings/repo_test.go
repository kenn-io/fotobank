package usersettings_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/service/usersettings"
	"github.com/wesm/fotobank/internal/testutil"
)

func TestRepoUpsertAndGet(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := usersettings.NewRepo(d.WriteDB(), d.ReadDB())
	p := owners.Principal{Hub: "local", UserID: "alice"}

	r.NoError(repo.Upsert(context.Background(), p, "theme", `"dark"`))

	val, ok, err := repo.Get(context.Background(), p, "theme")
	r.NoError(err)
	r.True(ok)
	r.JSONEq(`"dark"`, val)
}

func TestRepoGetMissing(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := usersettings.NewRepo(d.WriteDB(), d.ReadDB())
	p := owners.Principal{Hub: "local", UserID: "alice"}

	_, ok, err := repo.Get(context.Background(), p, "missing")
	r.NoError(err)
	r.False(ok)
}

func TestRepoUpsertReplaces(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := usersettings.NewRepo(d.WriteDB(), d.ReadDB())
	p := owners.Principal{Hub: "local", UserID: "alice"}

	r.NoError(repo.Upsert(context.Background(), p, "density.library", `"comfortable"`))
	r.NoError(repo.Upsert(context.Background(), p, "density.library", `"compact"`))

	val, ok, err := repo.Get(context.Background(), p, "density.library")
	r.NoError(err)
	r.True(ok)
	r.JSONEq(`"compact"`, val)
}

func TestRepoDelete(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := usersettings.NewRepo(d.WriteDB(), d.ReadDB())
	p := owners.Principal{Hub: "local", UserID: "alice"}

	r.NoError(repo.Upsert(context.Background(), p, "theme", `"dark"`))
	r.NoError(repo.Delete(context.Background(), p, "theme"))

	_, ok, err := repo.Get(context.Background(), p, "theme")
	r.NoError(err)
	r.False(ok)
}
