package service_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/album"
	"github.com/wesm/fotobank/internal/errs"
	"github.com/wesm/fotobank/internal/media"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/service"
	"github.com/wesm/fotobank/internal/testutil"
)

// albumSvcFixture bundles the service with the helpers tests need:
// the repos (for arranging state that bypasses the service), the
// writable DB (for seeding a second owner), and the default caller.
type albumSvcFixture struct {
	svc    *service.AlbumService
	albums *album.Repo
	media  *media.Repo
	rw     *sql.DB
	caller owners.Principal
}

func newAlbumSvcFixture(t *testing.T) albumSvcFixture {
	t.Helper()
	d := testutil.OpenTestDB(t)
	aRepo := album.NewRepo(d.WriteDB(), d.ReadDB())
	mRepo := media.NewRepo(d.WriteDB(), d.ReadDB())
	caller := owners.Principal{Hub: "h", UserID: "u"}
	seedOwnerSvc(t, d.WriteDB(), caller, "sk")
	return albumSvcFixture{
		svc:    service.NewAlbumService(aRepo, mRepo),
		albums: aRepo,
		media:  mRepo,
		rw:     d.WriteDB(),
		caller: caller,
	}
}

func seedOwnerSvc(t *testing.T, rw *sql.DB, p owners.Principal, sk string) {
	t.Helper()
	_, err := rw.ExecContext(context.Background(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		p.Hub, p.UserID, sk, time.Now().UTC(),
	)
	require.NoError(t, err)
}

func TestAlbumServiceCreateReturnsListItem(t *testing.T) {
	r := require.New(t)
	fx := newAlbumSvcFixture(t)

	it, err := fx.svc.Create(context.Background(), fx.caller, "Trip")
	r.NoError(err)
	r.NotEmpty(it.ID)
	r.Equal("Trip", it.Name)
	r.Equal(0, it.ItemCount)
	r.Nil(it.Cover)
	r.Equal(fx.caller, it.Owner)
}

func TestAlbumServiceCreateTrimsName(t *testing.T) {
	r := require.New(t)
	fx := newAlbumSvcFixture(t)

	it, err := fx.svc.Create(context.Background(), fx.caller, "  Trip  ")
	r.NoError(err)
	r.Equal("Trip", it.Name)
}

func TestAlbumServiceCreateRejectsInvalidName(t *testing.T) {
	fx := newAlbumSvcFixture(t)
	cases := []string{"", "   ", strings.Repeat("x", album.NameMaxLen+1)}
	for _, name := range cases {
		_, err := fx.svc.Create(context.Background(), fx.caller, name)
		require.ErrorIs(t, err, album.ErrInvalidName, "name=%q", name)
	}
}

func TestAlbumServiceGetCrossOwnerReturnsNotFound(t *testing.T) {
	r := require.New(t)
	fx := newAlbumSvcFixture(t)

	other := owners.Principal{Hub: "h", UserID: "other"}
	seedOwnerSvc(t, fx.rw, other, "sk-o")
	otherItem, err := fx.svc.Create(context.Background(), other, "OtherAlbum")
	r.NoError(err)

	_, err = fx.svc.Get(context.Background(), otherItem.ID, fx.caller)
	r.ErrorIs(err, errs.ErrNotFound)
}

func TestAlbumServiceGetDetailCrossOwnerReturnsNotFound(t *testing.T) {
	r := require.New(t)
	fx := newAlbumSvcFixture(t)

	other := owners.Principal{Hub: "h", UserID: "other"}
	seedOwnerSvc(t, fx.rw, other, "sk-o")
	otherItem, err := fx.svc.Create(context.Background(), other, "OtherAlbum")
	r.NoError(err)

	_, err = fx.svc.GetDetail(context.Background(), otherItem.ID, fx.caller)
	r.ErrorIs(err, errs.ErrNotFound)
}

func TestAlbumServiceGetDetailHappyPath(t *testing.T) {
	r := require.New(t)
	fx := newAlbumSvcFixture(t)

	it, err := fx.svc.Create(context.Background(), fx.caller, "Trip")
	r.NoError(err)

	got, err := fx.svc.GetDetail(context.Background(), it.ID, fx.caller)
	r.NoError(err)
	r.Equal(it.ID, got.ID)
	r.Equal(0, got.ItemCount)
	r.Nil(got.Cover)
}

func TestAlbumServiceRenameReturnsUpdatedDetail(t *testing.T) {
	r := require.New(t)
	fx := newAlbumSvcFixture(t)

	it, err := fx.svc.Create(context.Background(), fx.caller, "Old")
	r.NoError(err)

	got, err := fx.svc.Rename(context.Background(), it.ID, "New", fx.caller)
	r.NoError(err)
	r.Equal("New", got.Name)
	r.Equal(it.ID, got.ID)
	r.True(got.UpdatedAt.After(it.UpdatedAt) || got.UpdatedAt.Equal(it.UpdatedAt))
}

func TestAlbumServiceRenameCrossOwner(t *testing.T) {
	r := require.New(t)
	fx := newAlbumSvcFixture(t)

	other := owners.Principal{Hub: "h", UserID: "other"}
	seedOwnerSvc(t, fx.rw, other, "sk-o")
	otherIt, err := fx.svc.Create(context.Background(), other, "Theirs")
	r.NoError(err)

	_, err = fx.svc.Rename(context.Background(), otherIt.ID, "Mine", fx.caller)
	r.ErrorIs(err, errs.ErrNotFound)
}

func TestAlbumServiceRenameInvalidName(t *testing.T) {
	r := require.New(t)
	fx := newAlbumSvcFixture(t)
	it, err := fx.svc.Create(context.Background(), fx.caller, "x")
	r.NoError(err)

	_, err = fx.svc.Rename(context.Background(), it.ID, "", fx.caller)
	r.ErrorIs(err, album.ErrInvalidName)
}

func TestAlbumServiceDeleteCrossOwner(t *testing.T) {
	r := require.New(t)
	fx := newAlbumSvcFixture(t)

	other := owners.Principal{Hub: "h", UserID: "other"}
	seedOwnerSvc(t, fx.rw, other, "sk-o")
	otherIt, err := fx.svc.Create(context.Background(), other, "Theirs")
	r.NoError(err)

	err = fx.svc.Delete(context.Background(), otherIt.ID, fx.caller)
	r.ErrorIs(err, errs.ErrNotFound)
}

func TestAlbumServiceDeleteHappyPath(t *testing.T) {
	r := require.New(t)
	fx := newAlbumSvcFixture(t)

	it, err := fx.svc.Create(context.Background(), fx.caller, "Trip")
	r.NoError(err)
	r.NoError(fx.svc.Delete(context.Background(), it.ID, fx.caller))
	_, err = fx.svc.Get(context.Background(), it.ID, fx.caller)
	r.ErrorIs(err, errs.ErrNotFound)
}

func TestAlbumServiceListIsolatesOwners(t *testing.T) {
	r := require.New(t)
	fx := newAlbumSvcFixture(t)

	other := owners.Principal{Hub: "h", UserID: "other"}
	seedOwnerSvc(t, fx.rw, other, "sk-o")

	_, err := fx.svc.Create(context.Background(), fx.caller, "Mine-1")
	r.NoError(err)
	_, err = fx.svc.Create(context.Background(), fx.caller, "Mine-2")
	r.NoError(err)
	_, err = fx.svc.Create(context.Background(), other, "Theirs")
	r.NoError(err)

	items, err := fx.svc.List(context.Background(), fx.caller, 10, 0)
	r.NoError(err)
	r.Len(items, 2)
	for _, it := range items {
		r.Equal(fx.caller, it.Owner)
	}
}
