package cli

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/config"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/service"
	"go.kenn.io/fotobank/internal/testutil"
)

func TestBuildIdentityProviderPersistsGeneratedStorageKey(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := owners.NewRepo(d.WriteDB(), d.ReadDB())
	svc := service.NewOwnerService(repo)
	cfg := &config.Config{Identity: config.Identity{
		Mode: "stub",
		Stub: config.IdentityStub{Hub: "hub", UserID: "owner", Handle: "Owner"},
	}}

	_, err := buildIdentityProvider(t.Context(), cfg, svc)
	r.NoError(err)
	stored, err := repo.GetByPrincipal(t.Context(), owners.Principal{Hub: "hub", UserID: "owner"})
	r.NoError(err)
	_, err = uuid.Parse(stored.StorageKey)
	r.NoError(err)

	_, err = buildIdentityProvider(t.Context(), cfg, svc)
	r.NoError(err)
	again, err := repo.GetByPrincipal(t.Context(), stored.Principal)
	r.NoError(err)
	r.Equal(stored.StorageKey, again.StorageKey)
}
