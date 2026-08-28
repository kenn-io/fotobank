package service_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/service"
	"go.kenn.io/fotobank/internal/testutil"
)

// raceFakeRepo simulates the lost-race scenario Ensure must handle: the
// first GetByPrincipal returns ErrNotFound (our probe sees no row), Insert
// fails as though another caller inserted the same row first, and the
// second GetByPrincipal returns that other caller's row.
type raceFakeRepo struct {
	getCalls        int
	raceWinnerOwner owners.Owner
	insertCalls     int
	insertErr       error
}

func (f *raceFakeRepo) GetByPrincipal(context.Context, owners.Principal) (owners.Owner, error) {
	f.getCalls++
	if f.getCalls == 1 {
		return owners.Owner{}, errs.ErrNotFound
	}
	return f.raceWinnerOwner, nil
}

func (f *raceFakeRepo) Insert(context.Context, owners.Owner) error {
	f.insertCalls++
	return f.insertErr
}

func (f *raceFakeRepo) List(context.Context) ([]owners.Owner, error) { return nil, nil }
func (f *raceFakeRepo) Delete(context.Context, owners.Principal) error {
	return nil
}
func (f *raceFakeRepo) UpdateDisplayHandle(context.Context, owners.Principal, string) error {
	return nil
}
func (f *raceFakeRepo) DB() *sql.DB { return nil }

func TestOwnerServiceEnsureGeneratesStorageKey(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := owners.NewRepo(d.WriteDB(), d.ReadDB())
	svc := service.NewOwnerService(repo)

	p := owners.Principal{Hub: "h", UserID: "u"}
	got, err := svc.Ensure(t.Context(), p, "")
	r.NoError(err)
	r.Equal(p, got.Principal)
	_, err = uuid.Parse(got.StorageKey)
	r.NoError(err)
	stored, err := repo.GetByPrincipal(t.Context(), p)
	r.NoError(err)
	r.Equal(got, stored)
}

func TestOwnerServiceEnsureExistingOwner(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	svc := service.NewOwnerService(owners.NewRepo(d.WriteDB(), d.ReadDB()))

	p := owners.Principal{Hub: "h", UserID: "u"}
	key := "550e8400-e29b-41d4-a716-446655440000"
	created, err := svc.Ensure(t.Context(), p, key)
	r.NoError(err)

	got, err := svc.Ensure(t.Context(), p, "")
	r.NoError(err)
	r.Equal(created, got)
	got, err = svc.Ensure(t.Context(), p, key)
	r.NoError(err)
	r.Equal(created, got)

	_, err = svc.Ensure(t.Context(), p, "660e8400-e29b-41d4-a716-446655440000")
	r.ErrorIs(err, errs.ErrAlreadyExists)
}

func TestOwnerServiceEnsureRejectsInvalidStorageKey(t *testing.T) {
	d := testutil.OpenTestDB(t)
	repo := owners.NewRepo(d.WriteDB(), d.ReadDB())
	svc := service.NewOwnerService(repo)
	p := owners.Principal{Hub: "h", UserID: "u"}

	_, err := svc.Ensure(t.Context(), p, "not-a-uuid")
	require.ErrorIs(t, err, errs.ErrInvalidArgument)
	_, err = repo.GetByPrincipal(t.Context(), p)
	require.ErrorIs(t, err, errs.ErrNotFound)
}

func TestOwnerServiceEnsureRecoversFromRaceInsert(t *testing.T) {
	// Regression: when Ensure's GetByPrincipal probe returns ErrNotFound
	// but Insert then fails because a concurrent caller inserted the
	// same principal first, Ensure must re-read and honour the
	// idempotent contract. Uses a fake repo so the race is
	// deterministic, not goroutine-flaky.
	p := owners.Principal{Hub: "h", UserID: "u"}

	t.Run("empty request returns concurrent winner", func(t *testing.T) {
		r := require.New(t)
		repo := &raceFakeRepo{
			raceWinnerOwner: owners.Owner{
				Principal:  p,
				StorageKey: "550e8400-e29b-41d4-a716-446655440000",
				CreatedAt:  time.Now().UTC(),
			},
			insertErr: errors.New("UNIQUE constraint failed: owners.hub, owners.user_id"),
		}
		svc := service.NewOwnerService(repo)
		got, err := svc.Ensure(t.Context(), p, "")
		r.NoError(err)
		r.Equal(repo.raceWinnerOwner, got)
		r.Equal(2, repo.getCalls, "should re-read after failed Insert")
		r.Equal(1, repo.insertCalls)
	})

	t.Run("matching explicit key returns concurrent winner", func(t *testing.T) {
		r := require.New(t)
		key := "550e8400-e29b-41d4-a716-446655440000"
		repo := &raceFakeRepo{
			raceWinnerOwner: owners.Owner{
				Principal: p, StorageKey: key, CreatedAt: time.Now().UTC(),
			},
			insertErr: errors.New("UNIQUE constraint failed: owners.hub, owners.user_id"),
		}
		svc := service.NewOwnerService(repo)
		got, err := svc.Ensure(t.Context(), p, key)
		r.NoError(err)
		r.Equal(repo.raceWinnerOwner, got)
		r.Equal(2, repo.getCalls, "should re-read after failed Insert")
	})

	t.Run("mismatching explicit key returns ErrAlreadyExists", func(t *testing.T) {
		r := require.New(t)
		repo := &raceFakeRepo{
			raceWinnerOwner: owners.Owner{
				Principal:  p,
				StorageKey: "550e8400-e29b-41d4-a716-446655440000",
				CreatedAt:  time.Now().UTC(),
			},
			insertErr: errors.New("UNIQUE constraint failed: owners.hub, owners.user_id"),
		}
		svc := service.NewOwnerService(repo)
		_, err := svc.Ensure(t.Context(), p, "660e8400-e29b-41d4-a716-446655440000")
		r.ErrorIs(err, errs.ErrAlreadyExists)
		r.Equal(2, repo.getCalls, "should re-read after failed Insert")
	})
}

func TestRemoveRefusesWhenMediaExists(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	svc := service.NewOwnerService(owners.NewRepo(d.WriteDB(), d.ReadDB()))
	p := owners.Principal{Hub: "h", UserID: "u"}
	_, err := svc.Ensure(context.Background(), p, "550e8400-e29b-41d4-a716-446655440000")
	r.NoError(err)

	_ = testutil.SeedPhoto(t, d.WriteDB(), p, "x")

	r.ErrorIs(svc.Remove(context.Background(), p, false), errs.ErrInvalidArgument)
}

func TestRemoveRefusesWhenAssetExists(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	svc := service.NewOwnerService(owners.NewRepo(d.WriteDB(), d.ReadDB()))
	p := owners.Principal{Hub: "h", UserID: "u"}
	_, err := svc.Ensure(t.Context(), p, "550e8400-e29b-41d4-a716-446655440000")
	r.NoError(err)

	_, err = d.WriteDB().ExecContext(t.Context(), `
		INSERT INTO assets (
			id, owner_hub, owner_user_id, state, media_type,
			imported_at, thumb_status, thumb_version
		) VALUES (
			'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa', 'h', 'u', 'pending', 'photo',
			datetime('now'), 'pending', 0
		)`)
	r.NoError(err)

	r.ErrorIs(svc.Remove(t.Context(), p, false), errs.ErrInvalidArgument)
}

func TestRemoveSucceedsWhenEmpty(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	svc := service.NewOwnerService(owners.NewRepo(d.WriteDB(), d.ReadDB()))
	p := owners.Principal{Hub: "h", UserID: "u"}
	_, err := svc.Ensure(context.Background(), p, "550e8400-e29b-41d4-a716-446655440000")
	r.NoError(err)
	r.NoError(svc.Remove(context.Background(), p, false))
}
