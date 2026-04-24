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

func TestShareGetReturnsOwnedScope(t *testing.T) {
	r := require.New(t)
	fx := newShareFixture(t)
	albumID := fx.seedAlbum(t, 1)
	s, err := fx.svc.Create(context.Background(), service.CreateShareRequest{
		Grantee:    owners.Principal{Hub: "h", UserID: "a"},
		TargetType: share.TargetAlbumLive,
		AlbumID:    albumID,
	}, fx.owner)
	r.NoError(err)

	got, err := fx.svc.Get(context.Background(), s.UUID, fx.owner)
	r.NoError(err)
	r.Equal(s.UUID, got.UUID)
}

func TestShareGetCrossOwnerNotFound(t *testing.T) {
	r := require.New(t)
	fx := newShareFixture(t)
	albumID := fx.seedAlbum(t, 1)
	s, err := fx.svc.Create(context.Background(), service.CreateShareRequest{
		Grantee:    owners.Principal{Hub: "h", UserID: "a"},
		TargetType: share.TargetAlbumLive,
		AlbumID:    albumID,
	}, fx.owner)
	r.NoError(err)

	intruder := owners.Principal{Hub: "h", UserID: "intruder"}
	_, err = fx.svc.Get(context.Background(), s.UUID, intruder)
	r.ErrorIs(err, errs.ErrNotFound)
}

func TestShareListScopedToCaller(t *testing.T) {
	r := require.New(t)
	fx := newShareFixture(t)
	albumID := fx.seedAlbum(t, 1)
	s1, err := fx.svc.Create(context.Background(), service.CreateShareRequest{
		Grantee: owners.Principal{Hub: "h", UserID: "a"}, TargetType: share.TargetAlbumLive, AlbumID: albumID,
	}, fx.owner)
	r.NoError(err)

	got, err := fx.svc.List(context.Background(), share.ScopeFilter{}, fx.owner)
	r.NoError(err)
	found := false
	for _, s := range got {
		if s.UUID == s1.UUID {
			found = true
		}
	}
	r.True(found)

	intruder := owners.Principal{Hub: "h", UserID: "intruder"}
	got, err = fx.svc.List(context.Background(), share.ScopeFilter{}, intruder)
	r.NoError(err)
	r.Empty(got)
}

func TestShareRevokeTransitionsPendingToRevoking(t *testing.T) {
	r := require.New(t)
	fx := newShareFixture(t)
	albumID := fx.seedAlbum(t, 1)
	s, err := fx.svc.Create(context.Background(), service.CreateShareRequest{
		Grantee: owners.Principal{Hub: "h", UserID: "a"}, TargetType: share.TargetAlbumLive, AlbumID: albumID,
	}, fx.owner)
	r.NoError(err)

	got, err := fx.svc.Revoke(context.Background(), s.UUID, fx.owner)
	r.NoError(err)
	r.Equal(share.StatusRevoking, got.BrokerStatus)
	r.NotNil(got.RevokedAt)
}

func TestShareRevokeIdempotentError(t *testing.T) {
	r := require.New(t)
	fx := newShareFixture(t)
	albumID := fx.seedAlbum(t, 1)
	s, err := fx.svc.Create(context.Background(), service.CreateShareRequest{
		Grantee: owners.Principal{Hub: "h", UserID: "a"}, TargetType: share.TargetAlbumLive, AlbumID: albumID,
	}, fx.owner)
	r.NoError(err)

	_, err = fx.svc.Revoke(context.Background(), s.UUID, fx.owner)
	r.NoError(err)
	_, err = fx.svc.Revoke(context.Background(), s.UUID, fx.owner)
	r.ErrorIs(err, share.ErrScopeAlreadyRevoked)
}

func TestShareRevokeCrossOwnerNotFound(t *testing.T) {
	r := require.New(t)
	fx := newShareFixture(t)
	albumID := fx.seedAlbum(t, 1)
	s, err := fx.svc.Create(context.Background(), service.CreateShareRequest{
		Grantee: owners.Principal{Hub: "h", UserID: "a"}, TargetType: share.TargetAlbumLive, AlbumID: albumID,
	}, fx.owner)
	r.NoError(err)

	intruder := owners.Principal{Hub: "h", UserID: "intruder"}
	_, err = fx.svc.Revoke(context.Background(), s.UUID, intruder)
	r.ErrorIs(err, errs.ErrNotFound)
}

func TestShareRetryPublishRoutesFailedToPending(t *testing.T) {
	r := require.New(t)
	fx := newShareFixture(t)
	albumID := fx.seedAlbum(t, 1)
	s, err := fx.svc.Create(context.Background(), service.CreateShareRequest{
		Grantee: owners.Principal{Hub: "h", UserID: "a"}, TargetType: share.TargetAlbumLive, AlbumID: albumID,
	}, fx.owner)
	r.NoError(err)
	_, err = fx.rw.ExecContext(context.Background(),
		`UPDATE scopes SET broker_status='failed', broker_attempts=10, broker_last_error='x' WHERE uuid=?`,
		s.UUID)
	r.NoError(err)

	got, err := fx.svc.Retry(context.Background(), s.UUID, fx.owner)
	r.NoError(err)
	r.Equal(share.StatusPending, got.BrokerStatus)
	r.Equal(0, got.BrokerAttempts)
}

func TestShareRetryRevokeRoutesFailedToRevoking(t *testing.T) {
	r := require.New(t)
	fx := newShareFixture(t)
	albumID := fx.seedAlbum(t, 1)
	s, err := fx.svc.Create(context.Background(), service.CreateShareRequest{
		Grantee: owners.Principal{Hub: "h", UserID: "a"}, TargetType: share.TargetAlbumLive, AlbumID: albumID,
	}, fx.owner)
	r.NoError(err)
	now := time.Now().UTC()
	_, err = fx.rw.ExecContext(context.Background(),
		`UPDATE scopes SET broker_status='failed', broker_attempts=10, revoked_at=? WHERE uuid=?`,
		now, s.UUID)
	r.NoError(err)

	got, err := fx.svc.Retry(context.Background(), s.UUID, fx.owner)
	r.NoError(err)
	r.Equal(share.StatusRevoking, got.BrokerStatus)
	r.Equal(0, got.BrokerAttempts)
	r.NotNil(got.RevokedAt)
}

func TestShareRetryRejectsNonFailed(t *testing.T) {
	r := require.New(t)
	fx := newShareFixture(t)
	albumID := fx.seedAlbum(t, 1)
	s, err := fx.svc.Create(context.Background(), service.CreateShareRequest{
		Grantee: owners.Principal{Hub: "h", UserID: "a"}, TargetType: share.TargetAlbumLive, AlbumID: albumID,
	}, fx.owner)
	r.NoError(err)

	_, err = fx.svc.Retry(context.Background(), s.UUID, fx.owner)
	r.ErrorIs(err, share.ErrRetryNotApplicable)
}

func TestPreviewScopeAlbumLivePopulatesMediaAndAlbum(t *testing.T) {
	r := require.New(t)
	fx := newShareFixture(t)
	albumID := fx.seedAlbum(t, 2)

	s, err := fx.svc.Create(context.Background(), service.CreateShareRequest{
		Grantee: owners.Principal{Hub: "h", UserID: "bob"}, TargetType: share.TargetAlbumLive, AlbumID: albumID,
	}, fx.owner)
	r.NoError(err)

	prev, err := fx.svc.PreviewScope(context.Background(), s.UUID, fx.owner)
	r.NoError(err)
	r.Equal(s.UUID, prev.Scope.UUID)
	r.NotNil(prev.Album)
	r.Equal(albumID, prev.Album.ID)
	r.Len(prev.Media, 2)
}

func TestPreviewScopeMediaSetPopulatesFrozenMedia(t *testing.T) {
	r := require.New(t)
	fx := newShareFixture(t)
	m1 := fx.seedMediaRow(t)
	m2 := fx.seedMediaRow(t)
	s, err := fx.svc.Create(context.Background(), service.CreateShareRequest{
		Grantee:    owners.Principal{Hub: "h", UserID: "bob"},
		TargetType: share.TargetMediaSet, MediaIDs: []string{m1, m2},
	}, fx.owner)
	r.NoError(err)

	prev, err := fx.svc.PreviewScope(context.Background(), s.UUID, fx.owner)
	r.NoError(err)
	r.Equal(s.UUID, prev.Scope.UUID)
	r.Nil(prev.Album, "media_set previews must not carry an album")
	r.Len(prev.Media, 2)
	gotIDs := []string{prev.Media[0].ID, prev.Media[1].ID}
	r.ElementsMatch([]string{m1, m2}, gotIDs)
}

func TestPreviewScopeCrossOwnerReturnsNotFound(t *testing.T) {
	fx := newShareFixture(t)
	// Seed a second owner so we can attempt cross-owner preview.
	charlie := owners.Principal{Hub: "h", UserID: "charlie"}
	_, err := fx.rw.ExecContext(context.Background(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		charlie.Hub, charlie.UserID, "sk-c", time.Now().UTC())
	require.NoError(t, err)

	albumID := fx.seedAlbum(t, 1)
	s, err := fx.svc.Create(context.Background(), service.CreateShareRequest{
		Grantee: owners.Principal{Hub: "h", UserID: "bob"}, TargetType: share.TargetAlbumLive, AlbumID: albumID,
	}, fx.owner)
	require.NoError(t, err)

	_, err = fx.svc.PreviewScope(context.Background(), s.UUID, charlie)
	require.ErrorIs(t, err, errs.ErrNotFound)
}

func TestPreviewScopeSurfacesExpiryAndBrokerWarnings(t *testing.T) {
	r := require.New(t)
	fx := newShareFixture(t)
	albumID := fx.seedAlbum(t, 1)
	past := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	s, err := fx.svc.Create(context.Background(), service.CreateShareRequest{
		Grantee: owners.Principal{Hub: "h", UserID: "bob"}, TargetType: share.TargetAlbumLive, AlbumID: albumID,
		ExpiresAt: &past,
	}, fx.owner)
	r.NoError(err)

	prev, err := fx.svc.PreviewScope(context.Background(), s.UUID, fx.owner)
	r.NoError(err)
	r.Contains(prev.Warnings, "scope_expired")
	r.Contains(prev.Warnings, "broker_not_active", "freshly-created scope is pending")
	// Album has 1 media, all with thumb_status=pending → >25% not ready.
	r.Contains(prev.Warnings, "missing_thumbs")
}

func TestPreviewScopeUnknownScopeReturnsNotFound(t *testing.T) {
	fx := newShareFixture(t)
	_, err := fx.svc.PreviewScope(context.Background(), uuid.NewString(), fx.owner)
	require.ErrorIs(t, err, errs.ErrNotFound)
}
