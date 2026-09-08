package owners_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.kenn.io/fotobank/internal/album"
	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/share"
	"go.kenn.io/fotobank/internal/testutil"
)

func TestInsertThenGet(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := owners.NewRepo(d.WriteDB(), d.ReadDB())

	o := owners.Owner{
		Principal:     owners.Principal{Hub: "h", UserID: "u"},
		StorageKey:    "550e8400-e29b-41d4-a716-446655440000",
		DisplayHandle: "User",
		CreatedAt:     time.Now().UTC(),
	}
	r.NoError(repo.Insert(context.Background(), o))

	got, err := repo.GetByPrincipal(context.Background(), o.Principal)
	r.NoError(err)
	r.Equal(o.StorageKey, got.StorageKey)
	r.Equal(o.DisplayHandle, got.DisplayHandle)
}

func TestInsertDuplicatePrincipalFails(t *testing.T) {
	d := testutil.OpenTestDB(t)
	repo := owners.NewRepo(d.WriteDB(), d.ReadDB())

	o := owners.Owner{
		Principal:  owners.Principal{Hub: "h", UserID: "u"},
		StorageKey: "550e8400-e29b-41d4-a716-446655440000",
		CreatedAt:  time.Now().UTC(),
	}
	require.NoError(t, repo.Insert(context.Background(), o))
	require.Error(t, repo.Insert(context.Background(), o))
}

func TestInsertDuplicateStorageKeyFails(t *testing.T) {
	d := testutil.OpenTestDB(t)
	repo := owners.NewRepo(d.WriteDB(), d.ReadDB())
	now := time.Now().UTC()
	require.NoError(t, repo.Insert(context.Background(),
		owners.Owner{Principal: owners.Principal{Hub: "h", UserID: "u1"}, StorageKey: "550e8400-e29b-41d4-a716-446655440000", CreatedAt: now}))
	require.ErrorIs(t, repo.Insert(context.Background(),
		owners.Owner{Principal: owners.Principal{Hub: "h", UserID: "u2"}, StorageKey: "550e8400-e29b-41d4-a716-446655440000", CreatedAt: now}), errs.ErrAlreadyExists)
}

func TestDeleteReferencedOwnerIsConflict(t *testing.T) {
	for _, kind := range []string{"album", "share"} {
		t.Run(kind, func(t *testing.T) {
			r := require.New(t)
			d := testutil.OpenTestDB(t)
			repo := owners.NewRepo(d.WriteDB(), d.ReadDB())
			p := owners.Principal{Hub: "h", UserID: "guest"}
			now := time.Now().UTC()
			r.NoError(repo.Insert(t.Context(), owners.Owner{Principal: p, StorageKey: "550e8400-e29b-41d4-a716-446655440000", CreatedAt: now}))
			if kind == "album" {
				r.NoError(album.NewRepo(d.WriteDB(), d.ReadDB()).Insert(t.Context(), album.Album{ID: "550e8400-e29b-41d4-a716-446655440001", Owner: p, Name: "Album", CreatedAt: now, UpdatedAt: now}))
			} else {
				r.NoError(share.NewRepo(d.WriteDB(), d.ReadDB()).Insert(t.Context(), share.Scope{UUID: "550e8400-e29b-41d4-a716-446655440001", Owner: p, Grantee: owners.Principal{Hub: "h", UserID: "other"}, TargetType: share.TargetMediaSet, BrokerStatus: share.StatusFailed, CreatedAt: now}, nil))
			}
			r.ErrorIs(repo.Delete(t.Context(), p), errs.ErrAlreadyExists)
			_, err := repo.GetByPrincipal(t.Context(), p)
			r.NoError(err)
		})
	}
}

func TestListReturnsAll(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := owners.NewRepo(d.WriteDB(), d.ReadDB())
	now := time.Now().UTC()
	for i := range 3 {
		r.NoError(repo.Insert(context.Background(), owners.Owner{
			Principal:  owners.Principal{Hub: "h", UserID: fmt.Sprintf("u%d", i)},
			StorageKey: fmt.Sprintf("550e8400-e29b-41d4-a716-44665544000%d", i),
			CreatedAt:  now,
		}))
	}
	all, err := repo.List(context.Background())
	r.NoError(err)
	r.Len(all, 3)
}

func TestDeleteRemoves(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := owners.NewRepo(d.WriteDB(), d.ReadDB())
	p := owners.Principal{Hub: "h", UserID: "u"}
	r.NoError(repo.Insert(context.Background(),
		owners.Owner{Principal: p, StorageKey: "550e8400-e29b-41d4-a716-446655440000", CreatedAt: time.Now().UTC()}))
	r.NoError(repo.Delete(context.Background(), p))
	_, err := repo.GetByPrincipal(context.Background(), p)
	r.ErrorIs(err, errs.ErrNotFound)
}
