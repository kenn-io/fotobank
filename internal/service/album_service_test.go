package service_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/album"
	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/media"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/service"
	"go.kenn.io/fotobank/internal/share"
	"go.kenn.io/fotobank/internal/testutil"
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
	sRepo := share.NewRepo(d.WriteDB(), d.ReadDB())
	caller := owners.Principal{Hub: "h", UserID: "u"}
	seedOwnerSvc(t, d.WriteDB(), caller, "550e8400-e29b-41d4-a716-446655440000")
	return albumSvcFixture{
		svc:    service.NewAlbumService(aRepo, mRepo, sRepo, d),
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
	seedOwnerSvc(t, fx.rw, other, "550e8400-e29b-41d4-a716-44665544000b")
	otherItem, err := fx.svc.Create(context.Background(), other, "OtherAlbum")
	r.NoError(err)

	_, err = fx.svc.Get(context.Background(), otherItem.ID, fx.caller)
	r.ErrorIs(err, errs.ErrNotFound)
}

func TestAlbumServiceGetDetailCrossOwnerReturnsNotFound(t *testing.T) {
	r := require.New(t)
	fx := newAlbumSvcFixture(t)

	other := owners.Principal{Hub: "h", UserID: "other"}
	seedOwnerSvc(t, fx.rw, other, "550e8400-e29b-41d4-a716-44665544000b")
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
	seedOwnerSvc(t, fx.rw, other, "550e8400-e29b-41d4-a716-44665544000b")
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
	seedOwnerSvc(t, fx.rw, other, "550e8400-e29b-41d4-a716-44665544000b")
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
	seedOwnerSvc(t, fx.rw, other, "550e8400-e29b-41d4-a716-44665544000b")

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
	seedOwnerSvc(t, fx.rw, other, "550e8400-e29b-41d4-a716-44665544000b")
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
	seedOwnerSvc(t, fx.rw, other, "550e8400-e29b-41d4-a716-44665544000b")
	otherIt, err := fx.svc.Create(context.Background(), other, "Theirs")
	r.NoError(err)
	m := uuid.NewString()
	seedMediaSvc(t, fx.rw, fx.caller, m, "cs")

	_, _, err = fx.svc.AddMedia(context.Background(), otherIt.ID,
		[]string{m}, fx.caller)
	r.ErrorIs(err, errs.ErrNotFound)
}

func TestAlbumServiceAddMediaRejectsSidecar(t *testing.T) {
	r := require.New(t)
	fx := newAlbumSvcFixture(t)
	ctx := context.Background()

	primaryID := uuid.NewString()
	sidecarID := uuid.NewString()
	seedMediaSvc(t, fx.rw, fx.caller, primaryID, "cs-pri")
	seedMediaSvc(t, fx.rw, fx.caller, sidecarID, "cs-sid")
	r.NoError(fx.media.UpdatePairedWithID(ctx, sidecarID, &primaryID))

	a, err := fx.svc.Create(ctx, fx.caller, "Trip")
	r.NoError(err)

	// Primary alone is fine.
	_, _, err = fx.svc.AddMedia(ctx, a.ID, []string{primaryID}, fx.caller)
	r.NoError(err)

	// Sidecar must be rejected with ErrInvalidArgument (HTTP 400).
	_, _, err = fx.svc.AddMedia(ctx, a.ID, []string{sidecarID}, fx.caller)
	r.ErrorIs(err, errs.ErrInvalidArgument)
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
	seedOwnerSvc(t, fx.rw, other, "550e8400-e29b-41d4-a716-44665544000b")
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

func TestAlbumServiceListMediaAcceptsSortByTaken(t *testing.T) {
	r := require.New(t)
	fx := newAlbumSvcFixture(t)
	a, err := fx.svc.Create(context.Background(), fx.caller, "Trip")
	r.NoError(err)
	_, err = fx.svc.ListMedia(context.Background(), a.ID,
		album.AlbumMediaFilter{SortBy: "taken", Limit: 10}, fx.caller)
	r.NoError(err)
}

func TestAlbumServiceListMediaRejectsUnknownSortBy(t *testing.T) {
	r := require.New(t)
	fx := newAlbumSvcFixture(t)
	a, err := fx.svc.Create(context.Background(), fx.caller, "Trip")
	r.NoError(err)
	_, err = fx.svc.ListMedia(context.Background(), a.ID,
		album.AlbumMediaFilter{SortBy: "garbage", Limit: 10}, fx.caller)
	r.ErrorIs(err, album.ErrInvalidSort)
}

func TestAlbumServiceListMediaCrossOwner(t *testing.T) {
	r := require.New(t)
	fx := newAlbumSvcFixture(t)
	other := owners.Principal{Hub: "h", UserID: "other"}
	seedOwnerSvc(t, fx.rw, other, "550e8400-e29b-41d4-a716-44665544000b")
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

func TestAlbumDeleteBlocksWhenLiveScopes(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	d := testutil.OpenTestDB(t)
	owner := owners.Principal{Hub: "h", UserID: "o"}
	_, err := d.WriteDB().ExecContext(ctx,
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		owner.Hub, owner.UserID, "550e8400-e29b-41d4-a716-446655440000", time.Now().UTC())
	r.NoError(err)
	albums := album.NewRepo(d.WriteDB(), d.ReadDB())
	shares := share.NewRepo(d.WriteDB(), d.ReadDB())
	mediaRepo := media.NewRepo(d.WriteDB(), d.ReadDB())
	svc := service.NewAlbumService(albums, mediaRepo, shares, d)
	shareSvc := service.NewShareService(shares, albums, mediaRepo)

	a, err := svc.Create(ctx, owner, "Trip")
	r.NoError(err)
	m := media.Media{
		ID: uuid.NewString(), Owner: owner, Type: media.TypePhoto,
		MimeType: "image/jpeg", Path: "2024/t.jpg",
		OriginalFilename: "x.jpg", ImportedAt: time.Now().UTC().Truncate(time.Second),
		Size: 100, Checksum: "cs", ThumbStatus: "pending",
	}
	r.NoError(mediaRepo.Insert(ctx, m))
	_, _, err = svc.AddMedia(ctx, a.ID, []string{m.ID}, owner)
	r.NoError(err)

	s, err := shareSvc.Create(ctx, service.CreateShareRequest{
		Grantee: owners.Principal{Hub: "h", UserID: "a"}, TargetType: share.TargetAlbumLive, AlbumID: a.ID,
	}, owner)
	r.NoError(err)

	err = svc.Delete(ctx, a.ID, owner)
	r.ErrorIs(err, share.ErrAlbumHasLiveScopes)
	_, err = albums.GetByID(ctx, a.ID)
	r.NoError(err)
	_, err = shares.GetByUUID(ctx, s.UUID)
	r.NoError(err)
}

func TestAlbumDeletePurgesRevokedRemote(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	d := testutil.OpenTestDB(t)
	owner := owners.Principal{Hub: "h", UserID: "o"}
	_, err := d.WriteDB().ExecContext(ctx,
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		owner.Hub, owner.UserID, "550e8400-e29b-41d4-a716-446655440000", time.Now().UTC())
	r.NoError(err)
	albums := album.NewRepo(d.WriteDB(), d.ReadDB())
	shares := share.NewRepo(d.WriteDB(), d.ReadDB())
	mediaRepo := media.NewRepo(d.WriteDB(), d.ReadDB())
	svc := service.NewAlbumService(albums, mediaRepo, shares, d)
	shareSvc := service.NewShareService(shares, albums, mediaRepo)

	a, err := svc.Create(ctx, owner, "Trip")
	r.NoError(err)
	m := media.Media{
		ID: uuid.NewString(), Owner: owner, Type: media.TypePhoto,
		MimeType: "image/jpeg", Path: "2024/t.jpg",
		OriginalFilename: "x.jpg", ImportedAt: time.Now().UTC().Truncate(time.Second),
		Size: 100, Checksum: "cs", ThumbStatus: "pending",
	}
	r.NoError(mediaRepo.Insert(ctx, m))
	_, _, err = svc.AddMedia(ctx, a.ID, []string{m.ID}, owner)
	r.NoError(err)

	s, err := shareSvc.Create(ctx, service.CreateShareRequest{
		Grantee: owners.Principal{Hub: "h", UserID: "a"}, TargetType: share.TargetAlbumLive, AlbumID: a.ID,
	}, owner)
	r.NoError(err)
	now := time.Now().UTC()
	_, err = d.WriteDB().ExecContext(ctx,
		`UPDATE scopes SET broker_status='revoked_remote', revoked_at=?, broker_revoked_at=? WHERE uuid=?`,
		now, now, s.UUID)
	r.NoError(err)

	r.NoError(svc.Delete(ctx, a.ID, owner))
	_, err = albums.GetByID(ctx, a.ID)
	r.ErrorIs(err, errs.ErrNotFound)
	_, err = shares.GetByUUID(ctx, s.UUID)
	r.ErrorIs(err, errs.ErrNotFound)
}

// seedHiddenMediaSvc inserts a minimal media row with hidden_at set.
func seedHiddenMediaSvc(t *testing.T, rw *sql.DB, p owners.Principal, id, checksum string) {
	t.Helper()
	hiddenAt := time.Now().UTC().Add(-time.Hour)
	_, err := rw.ExecContext(context.Background(), `
INSERT INTO media (
    id, owner_hub, owner_user_id, media_type, mime_type, path, original_filename,
    imported_at, timestamp, size, checksum,
    make, model, focal_length, shutter, width, height, iso, aperture,
    duration_ms,
    thumb_status, thumb_version, thumb_updated_at, hidden_at
) VALUES (?, ?, ?, 'photo', 'image/jpeg', ?, NULL, ?, NULL, 0, ?,
          NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL,
          'ready', 1, NULL, ?)`,
		id, p.Hub, p.UserID, "p/"+id, time.Now().UTC(), checksum, hiddenAt,
	)
	require.NoError(t, err)
}

// TestAlbumServiceAddMediaHiddenIDNotFoundByDefault verifies that a hidden
// media row is treated as not-found when no carve-out option is passed.
func TestAlbumServiceAddMediaHiddenIDNotFoundByDefault(t *testing.T) {
	r := require.New(t)
	fx := newAlbumSvcFixture(t)
	it, err := fx.svc.Create(context.Background(), fx.caller, "Trip")
	r.NoError(err)

	hiddenID := uuid.NewString()
	seedHiddenMediaSvc(t, fx.rw, fx.caller, hiddenID, "cs-hidden")

	_, _, err = fx.svc.AddMedia(context.Background(), it.ID,
		[]string{hiddenID}, fx.caller)
	r.ErrorIs(err, errs.ErrNotFound, "hidden id must be not-found by default")
}

// TestAlbumServiceAddMediaHiddenIDAllowedWithOption verifies that a hidden
// media row passes through when WithHiddenMediaAllowed() is supplied.
func TestAlbumServiceAddMediaHiddenIDAllowedWithOption(t *testing.T) {
	r := require.New(t)
	fx := newAlbumSvcFixture(t)
	it, err := fx.svc.Create(context.Background(), fx.caller, "Trip")
	r.NoError(err)

	hiddenID := uuid.NewString()
	seedHiddenMediaSvc(t, fx.rw, fx.caller, hiddenID, "cs-hidden-opt")

	added, already, err := fx.svc.AddMedia(context.Background(), it.ID,
		[]string{hiddenID}, fx.caller, service.WithHiddenMediaAllowed())
	r.NoError(err, "hidden id must succeed with WithHiddenMediaAllowed")
	r.Equal(1, added)
	r.Equal(0, already)
}
