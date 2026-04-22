package service_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wesm/fotobank/internal/errs"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/service"
	"github.com/wesm/fotobank/internal/testutil"
)

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
