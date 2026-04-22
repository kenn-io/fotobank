package service_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wesm/fotobank/internal/errs"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/service"
	"github.com/wesm/fotobank/internal/testutil"
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

func TestEnsureIsIdempotent(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	svc := service.NewOwnerService(owners.NewRepo(d.WriteDB(), d.ReadDB()))

	p := owners.Principal{Hub: "h", UserID: "u"}
	r.NoError(svc.Ensure(context.Background(), p, "k"))
	r.NoError(svc.Ensure(context.Background(), p, "k")) // second call no-op
}

func TestEnsureConflictingStorageKeyErrors(t *testing.T) {
	d := testutil.OpenTestDB(t)
	svc := service.NewOwnerService(owners.NewRepo(d.WriteDB(), d.ReadDB()))

	p := owners.Principal{Hub: "h", UserID: "u"}
	require.NoError(t, svc.Ensure(context.Background(), p, "k1"))
	err := svc.Ensure(context.Background(), p, "k2")
	require.ErrorIs(t, err, errs.ErrAlreadyExists)
}

func TestEnsureRecoversFromRaceInsert(t *testing.T) {
	// Regression: when Ensure's GetByPrincipal probe returns ErrNotFound
	// but Insert then fails because a concurrent caller inserted the
	// same principal first, Ensure must re-read and honour the
	// idempotent contract. Uses a fake repo so the race is
	// deterministic, not goroutine-flaky.
	p := owners.Principal{Hub: "h", UserID: "u"}

	t.Run("matching storage key returns nil", func(t *testing.T) {
		r := require.New(t)
		repo := &raceFakeRepo{
			raceWinnerOwner: owners.Owner{
				Principal: p, StorageKey: "k", CreatedAt: time.Now().UTC(),
			},
			insertErr: errors.New("UNIQUE constraint failed: owners.hub, owners.user_id"),
		}
		svc := service.NewOwnerService(repo)
		r.NoError(svc.Ensure(context.Background(), p, "k"))
		r.Equal(2, repo.getCalls, "should re-read after failed Insert")
		r.Equal(1, repo.insertCalls)
	})

	t.Run("mismatching storage key returns ErrAlreadyExists", func(t *testing.T) {
		r := require.New(t)
		repo := &raceFakeRepo{
			raceWinnerOwner: owners.Owner{
				Principal: p, StorageKey: "winner", CreatedAt: time.Now().UTC(),
			},
			insertErr: errors.New("UNIQUE constraint failed: owners.hub, owners.user_id"),
		}
		svc := service.NewOwnerService(repo)
		r.ErrorIs(svc.Ensure(context.Background(), p, "mine"), errs.ErrAlreadyExists)
		r.Equal(2, repo.getCalls, "should re-read after failed Insert")
	})
}

func TestRemoveRefusesWhenMediaExists(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	svc := service.NewOwnerService(owners.NewRepo(d.WriteDB(), d.ReadDB()))
	p := owners.Principal{Hub: "h", UserID: "u"}
	r.NoError(svc.Ensure(context.Background(), p, "k"))

	// Insert a raw media row for this owner.
	_, err := d.WriteDB().Exec(`
		INSERT INTO media (id, owner_hub, owner_user_id, media_type, mime_type, path,
		                   imported_at, size, checksum, thumb_status, thumb_version, thumb_updated_at)
		VALUES ('c0000000-0000-0000-0000-000000000001', 'h', 'u', 'photo', 'image/jpeg',
		        'x.jpg', datetime('now'), 1, 'cs', 'pending', 1, datetime('now'))`)
	r.NoError(err)

	r.ErrorIs(svc.Remove(context.Background(), p, false), errs.ErrInvalidArgument)
}

func TestRemoveSucceedsWhenEmpty(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	svc := service.NewOwnerService(owners.NewRepo(d.WriteDB(), d.ReadDB()))
	p := owners.Principal{Hub: "h", UserID: "u"}
	r.NoError(svc.Ensure(context.Background(), p, "k"))
	r.NoError(svc.Remove(context.Background(), p, false))
}
