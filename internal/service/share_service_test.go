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
	"github.com/wesm/fotobank/internal/share"
	"github.com/wesm/fotobank/internal/testutil"
)

type shareFixture struct {
	svc    *service.ShareService
	shares *share.Repo
	albums *album.Repo
	media  *media.Repo
	owner  owners.Principal
	rw     *sql.DB
}

func newShareFixture(t *testing.T) *shareFixture {
	t.Helper()
	d := testutil.OpenTestDB(t)
	owner := owners.Principal{Hub: "h", UserID: "o"}
	_, err := d.WriteDB().ExecContext(context.Background(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		owner.Hub, owner.UserID, "sk", time.Now().UTC())
	require.NoError(t, err)

	shares := share.NewRepo(d.WriteDB(), d.ReadDB())
	albums := album.NewRepo(d.WriteDB(), d.ReadDB())
	mediaRepo := media.NewRepo(d.WriteDB(), d.ReadDB())
	svc := service.NewShareService(shares, albums, mediaRepo)
	return &shareFixture{
		svc: svc, shares: shares, albums: albums, media: mediaRepo,
		owner: owner, rw: d.WriteDB(),
	}
}

// seedAlbum inserts an album row + `items` album_media rows directly via
// SQL. T13 predates T15's AlbumService constructor change, so we avoid
// going through service.NewAlbumService here.
func (fx *shareFixture) seedAlbum(t *testing.T, items int) string {
	t.Helper()
	id := uuid.NewString()
	now := time.Now().UTC()
	_, err := fx.rw.ExecContext(context.Background(),
		`INSERT INTO albums(id, owner_hub, owner_user_id, name, created_at, updated_at)
         VALUES(?,?,?,?,?,?)`,
		id, fx.owner.Hub, fx.owner.UserID, "Trip", now, now)
	require.NoError(t, err)
	for range items {
		mid := fx.seedMediaRow(t)
		_, err := fx.rw.ExecContext(context.Background(),
			`INSERT INTO album_media(album_id, media_id, added_at) VALUES(?,?,?)`,
			id, mid, now)
		require.NoError(t, err)
	}
	return id
}

func (fx *shareFixture) seedMediaRow(t *testing.T) string {
	t.Helper()
	m := media.Media{
		ID: uuid.NewString(), Owner: fx.owner, Type: media.TypePhoto,
		MimeType: "image/jpeg", Path: "2024/" + uuid.NewString() + ".jpg",
		OriginalFilename: "x.jpg", ImportedAt: time.Now().UTC().Truncate(time.Second),
		Size: 100, Checksum: "cs" + uuid.NewString(), ThumbStatus: "pending",
	}
	require.NoError(t, fx.media.Insert(context.Background(), m))
	return m.ID
}

func TestShareCreateAlbumLiveHappyPath(t *testing.T) {
	r := require.New(t)
	fx := newShareFixture(t)
	albumID := fx.seedAlbum(t, 2)
	expires := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Second)

	got, err := fx.svc.Create(context.Background(), service.CreateShareRequest{
		Label:         "Summer",
		Grantee:       owners.Principal{Hub: "h", UserID: "alice"},
		TargetType:    share.TargetAlbumLive,
		AlbumID:       albumID,
		AllowDownload: true,
		ExpiresAt:     &expires,
	}, fx.owner)
	r.NoError(err)
	r.NotEmpty(got.UUID)
	r.Equal(share.StatusPending, got.BrokerStatus)
	r.NotNil(got.TargetAlbumID)
	r.Equal(albumID, *got.TargetAlbumID)
	r.True(got.AllowDownload)
	r.NotNil(got.ExpiresAt)
	r.True(got.ExpiresAt.Equal(expires))

	det, err := fx.shares.GetByUUID(context.Background(), got.UUID)
	r.NoError(err)
	r.True(det.AllowDownload)
	r.NotNil(det.ExpiresAt)
	r.True(det.ExpiresAt.Equal(expires))
}

func TestShareCreateMediaSetHappyPath(t *testing.T) {
	r := require.New(t)
	fx := newShareFixture(t)
	m1 := fx.seedMediaRow(t)
	m2 := fx.seedMediaRow(t)

	got, err := fx.svc.Create(context.Background(), service.CreateShareRequest{
		Grantee:    owners.Principal{Hub: "h", UserID: "alice"},
		TargetType: share.TargetMediaSet,
		MediaIDs:   []string{m1, m2, m1}, // duplicate deduped
	}, fx.owner)
	r.NoError(err)

	// ShareService.Get lands in T14 — read membership via the repo here.
	det, err := fx.shares.GetByUUID(context.Background(), got.UUID)
	r.NoError(err)
	r.ElementsMatch([]string{m1, m2}, det.MediaIDs)
}

func TestShareCreateRejectsEmptyAlbum(t *testing.T) {
	r := require.New(t)
	fx := newShareFixture(t)
	albumID := fx.seedAlbum(t, 0)

	_, err := fx.svc.Create(context.Background(), service.CreateShareRequest{
		Grantee:    owners.Principal{Hub: "h", UserID: "a"},
		TargetType: share.TargetAlbumLive,
		AlbumID:    albumID,
	}, fx.owner)
	r.ErrorIs(err, share.ErrAlbumEmpty)
}

func TestShareCreateRejectsOversizedLabel(t *testing.T) {
	r := require.New(t)
	fx := newShareFixture(t)
	albumID := fx.seedAlbum(t, 1)

	_, err := fx.svc.Create(context.Background(), service.CreateShareRequest{
		Label:      strings.Repeat("x", share.LabelMaxLen+1),
		Grantee:    owners.Principal{Hub: "h", UserID: "alice"},
		TargetType: share.TargetAlbumLive,
		AlbumID:    albumID,
	}, fx.owner)
	r.ErrorIs(err, share.ErrInvalidLabel)
}

func TestShareCreateRejectsCrossOwnerAlbum(t *testing.T) {
	r := require.New(t)
	fx := newShareFixture(t)
	otherOwner := owners.Principal{Hub: "h", UserID: "other"}
	_, err := fx.rw.ExecContext(context.Background(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		otherOwner.Hub, otherOwner.UserID, "sk2", time.Now().UTC())
	r.NoError(err)
	otherAlbumID := uuid.NewString()
	now := time.Now().UTC()
	_, err = fx.rw.ExecContext(context.Background(),
		`INSERT INTO albums(id, owner_hub, owner_user_id, name, created_at, updated_at)
         VALUES(?,?,?,?,?,?)`,
		otherAlbumID, otherOwner.Hub, otherOwner.UserID, "t", now, now)
	r.NoError(err)

	_, err = fx.svc.Create(context.Background(), service.CreateShareRequest{
		Grantee:    owners.Principal{Hub: "h", UserID: "a"},
		TargetType: share.TargetAlbumLive,
		AlbumID:    otherAlbumID,
	}, fx.owner)
	r.ErrorIs(err, errs.ErrNotFound)
}

func TestShareCreateRejectsCrossOwnerMediaSet(t *testing.T) {
	r := require.New(t)
	fx := newShareFixture(t)
	m := fx.seedMediaRow(t)
	otherOwner := owners.Principal{Hub: "h", UserID: "other"}
	_, err := fx.rw.ExecContext(context.Background(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		otherOwner.Hub, otherOwner.UserID, "sk2", time.Now().UTC())
	r.NoError(err)
	otherM := media.Media{
		ID: uuid.NewString(), Owner: otherOwner, Type: media.TypePhoto,
		MimeType: "image/jpeg", Path: "2024/other.jpg",
		OriginalFilename: "x.jpg", ImportedAt: time.Now().UTC().Truncate(time.Second),
		Size: 100, Checksum: "cso", ThumbStatus: "pending",
	}
	r.NoError(fx.media.Insert(context.Background(), otherM))

	_, err = fx.svc.Create(context.Background(), service.CreateShareRequest{
		Grantee:    owners.Principal{Hub: "h", UserID: "a"},
		TargetType: share.TargetMediaSet,
		MediaIDs:   []string{m, otherM.ID},
	}, fx.owner)
	r.ErrorIs(err, errs.ErrNotFound)
}

func TestShareCreateValidatesGrantee(t *testing.T) {
	fx := newShareFixture(t)
	albumID := fx.seedAlbum(t, 1)
	cases := []struct {
		name    string
		grantee owners.Principal
	}{
		{"zero", owners.Principal{}},
		{"empty hub", owners.Principal{UserID: "a"}},
		{"empty user", owners.Principal{Hub: "h"}},
		{"caller self", fx.owner},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := require.New(t)
			_, err := fx.svc.Create(context.Background(), service.CreateShareRequest{
				Grantee:    tc.grantee,
				TargetType: share.TargetAlbumLive,
				AlbumID:    albumID,
			}, fx.owner)
			r.ErrorIs(err, share.ErrInvalidGrantee)
		})
	}
}

func TestShareCreateRejectsOversizedGrantee(t *testing.T) {
	fx := newShareFixture(t)
	albumID := fx.seedAlbum(t, 1)
	cases := []struct {
		name    string
		grantee owners.Principal
	}{
		{
			"oversized hub",
			owners.Principal{
				Hub:    strings.Repeat("h", share.PrincipalFieldMaxLen+1),
				UserID: "alice",
			},
		},
		{
			"oversized user_id",
			owners.Principal{
				Hub:    "h",
				UserID: strings.Repeat("u", share.PrincipalFieldMaxLen+1),
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := require.New(t)
			_, err := fx.svc.Create(context.Background(), service.CreateShareRequest{
				Grantee:    tc.grantee,
				TargetType: share.TargetAlbumLive,
				AlbumID:    albumID,
			}, fx.owner)
			r.ErrorIs(err, share.ErrInvalidGrantee)
		})
	}
}

func TestShareCreateValidatesTargetCombo(t *testing.T) {
	r := require.New(t)
	fx := newShareFixture(t)
	albumID := fx.seedAlbum(t, 1)

	_, err := fx.svc.Create(context.Background(), service.CreateShareRequest{
		Grantee:    owners.Principal{Hub: "h", UserID: "a"},
		TargetType: share.TargetAlbumLive,
		AlbumID:    albumID,
		MediaIDs:   []string{"m1"}, // illegal combo
	}, fx.owner)
	r.ErrorIs(err, share.ErrInvalidTargetCombo)

	_, err = fx.svc.Create(context.Background(), service.CreateShareRequest{
		Grantee:    owners.Principal{Hub: "h", UserID: "a"},
		TargetType: share.TargetMediaSet,
		AlbumID:    albumID, // illegal combo
		MediaIDs:   []string{"m1"},
	}, fx.owner)
	r.ErrorIs(err, share.ErrInvalidTargetCombo)
}

func TestShareCreateValidatesMediaSetSize(t *testing.T) {
	r := require.New(t)
	fx := newShareFixture(t)

	_, err := fx.svc.Create(context.Background(), service.CreateShareRequest{
		Grantee:    owners.Principal{Hub: "h", UserID: "a"},
		TargetType: share.TargetMediaSet,
		MediaIDs:   nil, // empty after dedupe
	}, fx.owner)
	r.ErrorIs(err, share.ErrInvalidMediaSet)
}
