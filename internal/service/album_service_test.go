package service_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
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

// seedMediaSvc inserts a minimal media row directly.
func seedMediaSvc(t *testing.T, rw *sql.DB, p owners.Principal, id, checksum string) {
	t.Helper()
	_, err := rw.ExecContext(context.Background(), `
INSERT INTO media (
    id, owner_hub, owner_user_id, media_type, mime_type, path, original_filename,
    imported_at, timestamp, size, checksum,
    make, model, focal_length, shutter, width, height, iso, aperture,
    duration_ms,
    thumb_status, thumb_version, thumb_updated_at
) VALUES (?, ?, ?, 'photo', 'image/jpeg', ?, NULL, ?, NULL, 0, ?,
          NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL,
          'ready', 1, NULL)`,
		id, p.Hub, p.UserID, "p/"+id, time.Now().UTC(), checksum,
	)
	require.NoError(t, err)
}

func TestAlbumServiceAddMediaHappyPath(t *testing.T) {
	r := require.New(t)
	fx := newAlbumSvcFixture(t)
	it, err := fx.svc.Create(context.Background(), fx.caller, "Trip")
	r.NoError(err)
	m1 := uuid.NewString()
	m2 := uuid.NewString()
	seedMediaSvc(t, fx.rw, fx.caller, m1, "cs1")
	seedMediaSvc(t, fx.rw, fx.caller, m2, "cs2")

	added, already, err := fx.svc.AddMedia(context.Background(), it.ID,
		[]string{m1, m2}, fx.caller)
	r.NoError(err)
	r.Equal(2, added)
	r.Equal(0, already)
}

func TestAlbumServiceAddMediaDedupesInput(t *testing.T) {
	r := require.New(t)
	fx := newAlbumSvcFixture(t)
	it, err := fx.svc.Create(context.Background(), fx.caller, "Trip")
	r.NoError(err)
	m1 := uuid.NewString()
	m2 := uuid.NewString()
	seedMediaSvc(t, fx.rw, fx.caller, m1, "cs1")
	seedMediaSvc(t, fx.rw, fx.caller, m2, "cs2")

	added, already, err := fx.svc.AddMedia(context.Background(), it.ID,
		[]string{m1, m1, m2}, fx.caller)
	r.NoError(err)
	r.Equal(2, added)
	r.Equal(0, already)

	added2, already2, err := fx.svc.AddMedia(context.Background(), it.ID,
		[]string{m1, m2}, fx.caller)
	r.NoError(err)
	r.Equal(0, added2)
	r.Equal(2, already2)
}

func TestAlbumServiceAddMediaEmptyBatchInvalid(t *testing.T) {
	r := require.New(t)
	fx := newAlbumSvcFixture(t)
	it, err := fx.svc.Create(context.Background(), fx.caller, "Trip")
	r.NoError(err)

	_, _, err = fx.svc.AddMedia(context.Background(), it.ID, nil, fx.caller)
	r.ErrorIs(err, album.ErrInvalidBatch)
}

func TestAlbumServiceAddMediaOverCapInvalid(t *testing.T) {
	r := require.New(t)
	fx := newAlbumSvcFixture(t)
	it, err := fx.svc.Create(context.Background(), fx.caller, "Trip")
	r.NoError(err)

	ids := make([]string, album.BatchMaxLen+1)
	for i := range ids {
		ids[i] = uuid.NewString()
	}
	_, _, err = fx.svc.AddMedia(context.Background(), it.ID, ids, fx.caller)
	r.ErrorIs(err, album.ErrInvalidBatch)
}

func TestAlbumServiceAddMediaDedupePassesLengthCheck(t *testing.T) {
	// 502 ids with 2 duplicates → dedupes to 500 → valid.
	r := require.New(t)
	fx := newAlbumSvcFixture(t)
	it, err := fx.svc.Create(context.Background(), fx.caller, "Trip")
	r.NoError(err)

	ids := make([]string, 0, 502)
	distinct := make([]string, 500)
	for i := range distinct {
		id := uuid.NewString()
		distinct[i] = id
		seedMediaSvc(t, fx.rw, fx.caller, id, "cs-"+id)
	}
	ids = append(ids, distinct...)
	// Introduce 2 duplicates.
	ids = append(ids, distinct[0], distinct[1])

	added, already, err := fx.svc.AddMedia(context.Background(), it.ID, ids, fx.caller)
	r.NoError(err)
	r.Equal(500, added)
	r.Equal(0, already)
}

func TestAlbumServiceAddMediaUnknownIDIsNotFound(t *testing.T) {
	r := require.New(t)
	fx := newAlbumSvcFixture(t)
	it, err := fx.svc.Create(context.Background(), fx.caller, "Trip")
	r.NoError(err)

	_, _, err = fx.svc.AddMedia(context.Background(), it.ID,
		[]string{uuid.NewString()}, fx.caller)
	r.ErrorIs(err, errs.ErrNotFound)
}

func TestAlbumServiceAddMediaCrossOwnerMaskedAsNotFound(t *testing.T) {
	// Cross-owner media must NOT return ErrOwnerMismatch (which would
	// leak existence). The spec requires ErrNotFound here.
	r := require.New(t)
	fx := newAlbumSvcFixture(t)
	it, err := fx.svc.Create(context.Background(), fx.caller, "Trip")
	r.NoError(err)
	other := owners.Principal{Hub: "h", UserID: "other"}
	seedOwnerSvc(t, fx.rw, other, "sk-o")
	otherMedia := uuid.NewString()
	seedMediaSvc(t, fx.rw, other, otherMedia, "cs-o")

	_, _, err = fx.svc.AddMedia(context.Background(), it.ID,
		[]string{otherMedia}, fx.caller)
	r.ErrorIs(err, errs.ErrNotFound)
	r.NotErrorIs(err, errs.ErrOwnerMismatch, "must mask cross-owner as not-found")
}

func TestAlbumServiceAddMediaCrossOwnerAlbumNotFound(t *testing.T) {
	r := require.New(t)
	fx := newAlbumSvcFixture(t)
	other := owners.Principal{Hub: "h", UserID: "other"}
	seedOwnerSvc(t, fx.rw, other, "sk-o")
	otherIt, err := fx.svc.Create(context.Background(), other, "Theirs")
	r.NoError(err)
	m := uuid.NewString()
	seedMediaSvc(t, fx.rw, fx.caller, m, "cs")

	_, _, err = fx.svc.AddMedia(context.Background(), otherIt.ID,
		[]string{m}, fx.caller)
	r.ErrorIs(err, errs.ErrNotFound)
}

func TestAlbumServiceRemoveMediaHappyPath(t *testing.T) {
	r := require.New(t)
	fx := newAlbumSvcFixture(t)
	it, err := fx.svc.Create(context.Background(), fx.caller, "Trip")
	r.NoError(err)
	m := uuid.NewString()
	seedMediaSvc(t, fx.rw, fx.caller, m, "cs")
	_, _, err = fx.svc.AddMedia(context.Background(), it.ID, []string{m}, fx.caller)
	r.NoError(err)

	r.NoError(fx.svc.RemoveMedia(context.Background(), it.ID, m, fx.caller))
}

func TestAlbumServiceRemoveMediaCrossOwnerAlbum(t *testing.T) {
	r := require.New(t)
	fx := newAlbumSvcFixture(t)
	other := owners.Principal{Hub: "h", UserID: "other"}
	seedOwnerSvc(t, fx.rw, other, "sk-o")
	otherIt, err := fx.svc.Create(context.Background(), other, "Theirs")
	r.NoError(err)

	err = fx.svc.RemoveMedia(context.Background(), otherIt.ID, "anything", fx.caller)
	r.ErrorIs(err, errs.ErrNotFound)
}

func TestAlbumServiceRemoveMediaNotInAlbum(t *testing.T) {
	r := require.New(t)
	fx := newAlbumSvcFixture(t)
	it, err := fx.svc.Create(context.Background(), fx.caller, "Trip")
	r.NoError(err)

	err = fx.svc.RemoveMedia(context.Background(), it.ID, "nonesuch", fx.caller)
	r.ErrorIs(err, errs.ErrNotFound)
}

func TestAlbumServiceListMediaInvalidSort(t *testing.T) {
	r := require.New(t)
	fx := newAlbumSvcFixture(t)
	it, err := fx.svc.Create(context.Background(), fx.caller, "Trip")
	r.NoError(err)

	_, err = fx.svc.ListMedia(context.Background(), it.ID,
		album.AlbumMediaFilter{SortBy: "name"}, fx.caller)
	r.ErrorIs(err, album.ErrInvalidSort)
}

func TestAlbumServiceListMediaCrossOwner(t *testing.T) {
	r := require.New(t)
	fx := newAlbumSvcFixture(t)
	other := owners.Principal{Hub: "h", UserID: "other"}
	seedOwnerSvc(t, fx.rw, other, "sk-o")
	otherIt, err := fx.svc.Create(context.Background(), other, "Theirs")
	r.NoError(err)

	_, err = fx.svc.ListMedia(context.Background(), otherIt.ID,
		album.AlbumMediaFilter{SortBy: "added"}, fx.caller)
	r.ErrorIs(err, errs.ErrNotFound)
}

func TestAlbumServiceListMediaHappyPath(t *testing.T) {
	r := require.New(t)
	fx := newAlbumSvcFixture(t)
	it, err := fx.svc.Create(context.Background(), fx.caller, "Trip")
	r.NoError(err)
	m1 := uuid.NewString()
	m2 := uuid.NewString()
	seedMediaSvc(t, fx.rw, fx.caller, m1, "cs1")
	seedMediaSvc(t, fx.rw, fx.caller, m2, "cs2")
	_, _, err = fx.svc.AddMedia(context.Background(), it.ID, []string{m1, m2}, fx.caller)
	r.NoError(err)

	got, err := fx.svc.ListMedia(context.Background(), it.ID,
		album.AlbumMediaFilter{SortBy: "added"}, fx.caller)
	r.NoError(err)
	r.Len(got, 2)
}
