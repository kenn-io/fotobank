package service_test

import (
	"bytes"
	"context"
	"database/sql"
	"io"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/album"
	"go.kenn.io/fotobank/internal/db"
	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/media"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/service"
	"go.kenn.io/fotobank/internal/share"
	"go.kenn.io/fotobank/internal/storage"
	"go.kenn.io/fotobank/internal/testutil"
	"go.kenn.io/fotobank/internal/thumb"
)

type sharedReadFixture struct {
	t        *testing.T
	db       *db.DB
	shares   *share.Repo
	mediaR   *media.Repo
	albumsR  *album.Repo
	store    storage.Store
	resolver *share.ScopeResolver
	svc      *service.SharedReadService
	now      time.Time
}

func newSharedReadFixture(t *testing.T) sharedReadFixture {
	t.Helper()
	d := testutil.OpenTestDB(t)
	shares := share.NewRepo(d.WriteDB(), d.ReadDB())
	mRepo := media.NewRepo(d.WriteDB(), d.ReadDB())
	aRepo := album.NewRepo(d.WriteDB(), d.ReadDB())
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	// Storage keys are seeded per-owner via sharedSeedOwner. NASOnly is
	// pointed at the test's TempDir so the Store rejects unknown owners;
	// the map registers the fixture's canonical hub/user_id → storage_key
	// pairs up front.
	store := storage.NewNASOnly(t.TempDir(), map[owners.Principal]string{
		{Hub: "h", UserID: "alice"}:   "alice-sk",
		{Hub: "h", UserID: "bob"}:     "bob-sk",
		{Hub: "h", UserID: "charlie"}: "charlie-sk",
	})
	resolver := share.NewScopeResolver(shares, func() time.Time { return now }, nil)
	svc := service.NewSharedReadService(shares, mRepo, aRepo, store, resolver)
	return sharedReadFixture{
		t: t, db: d, shares: shares, mediaR: mRepo, albumsR: aRepo,
		store: store, resolver: resolver, svc: svc, now: now,
	}
}

// Helpers replicated from internal/share/repo_test.go (adapted for the
// service test package). Kept minimal: only what T11's tests exercise.

func sharedSeedOwner(t *testing.T, rw *sql.DB, p owners.Principal, sk string) {
	t.Helper()
	_, err := rw.ExecContext(context.Background(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		p.Hub, p.UserID, sk, time.Now().UTC())
	require.NoError(t, err)
}

func sharedSeedMedia(t *testing.T, rw *sql.DB, p owners.Principal) string {
	t.Helper()
	cs := uuid.NewString()
	repo := media.NewRepo(rw, rw)
	m := media.Media{
		ID: uuid.NewString(), Owner: p, Type: media.TypePhoto,
		MimeType: "image/jpeg", Path: "2024/" + cs + ".jpg",
		OriginalFilename: "x.jpg",
		ImportedAt:       time.Now().UTC().Truncate(time.Second),
		Size:             100, Checksum: cs, ThumbStatus: "pending",
	}
	require.NoError(t, repo.Insert(context.Background(), m))
	return m.ID
}

func sharedBumpActive(t *testing.T, rw *sql.DB, uuidStr string, at time.Time) {
	t.Helper()
	_, err := rw.ExecContext(context.Background(),
		`UPDATE scopes SET broker_status='active', broker_granted_at=?, broker_registered_at=? WHERE uuid=?`,
		at, at, uuidStr)
	require.NoError(t, err)
}

func sharedMakeMediaSetScopeOver(
	t *testing.T, repo *share.Repo,
	owner, grantee owners.Principal, now time.Time, download bool, mediaIDs ...string,
) share.Scope {
	t.Helper()
	s := share.Scope{
		UUID: uuid.NewString(), Owner: owner, Grantee: grantee,
		TargetType:    share.TargetMediaSet,
		AllowDownload: download,
		CreatedAt:     now,
		BrokerStatus:  share.StatusPending,
	}
	require.NoError(t, repo.Insert(context.Background(), s, mediaIDs))
	return s
}

// sharedSeedMediaWithTimestamp inserts a media row with an explicit
// Timestamp so display_time = COALESCE(timestamp, imported_at) is
// deterministic for ordering-sensitive tests.
func sharedSeedMediaWithTimestamp(t *testing.T, rw *sql.DB, p owners.Principal, ts time.Time) string {
	t.Helper()
	cs := uuid.NewString()
	repo := media.NewRepo(rw, rw)
	m := media.Media{
		ID: uuid.NewString(), Owner: p, Type: media.TypePhoto,
		MimeType: "image/jpeg", Path: "2024/" + cs + ".jpg",
		OriginalFilename: "x.jpg",
		ImportedAt:       time.Now().UTC().Truncate(time.Second),
		Timestamp:        &ts,
		Size:             100, Checksum: cs, ThumbStatus: "pending",
	}
	require.NoError(t, repo.Insert(context.Background(), m))
	return m.ID
}

func sharedSeedAlbumWithMedia(t *testing.T, rw *sql.DB, owner owners.Principal, n int) (string, []string) {
	t.Helper()
	now := time.Now().UTC()
	albumID := uuid.NewString()
	_, err := rw.ExecContext(context.Background(),
		`INSERT INTO albums(id, owner_hub, owner_user_id, name, created_at, updated_at) VALUES(?,?,?,?,?,?)`,
		albumID, owner.Hub, owner.UserID, "t", now, now)
	require.NoError(t, err)
	mediaIDs := make([]string, 0, n)
	for range n {
		mid := sharedSeedMedia(t, rw, owner)
		_, err := rw.ExecContext(context.Background(),
			`INSERT INTO album_media(album_id, media_id, added_at) VALUES(?,?,?)`,
			albumID, mid, now)
		require.NoError(t, err)
		mediaIDs = append(mediaIDs, mid)
	}
	return albumID, mediaIDs
}

func sharedMakeAlbumLiveScope(
	t *testing.T, repo *share.Repo,
	owner, grantee owners.Principal, albumID string, now time.Time, download bool,
) share.Scope {
	t.Helper()
	s := share.Scope{
		UUID: uuid.NewString(), Owner: owner, Grantee: grantee,
		TargetType:    share.TargetAlbumLive,
		TargetAlbumID: &albumID,
		AllowDownload: download,
		CreatedAt:     now,
		BrokerStatus:  share.StatusPending,
	}
	require.NoError(t, repo.Insert(context.Background(), s, nil))
	return s
}

// --- tests ---

func TestSharedReadListScopesReturnsAuthorizedOnly(t *testing.T) {
	r := require.New(t)
	fx := newSharedReadFixture(t)
	alice := owners.Principal{Hub: "h", UserID: "alice"}
	bob := owners.Principal{Hub: "h", UserID: "bob"}
	charlie := owners.Principal{Hub: "h", UserID: "charlie"}
	sharedSeedOwner(t, fx.db.WriteDB(), alice, "alice-sk")
	sharedSeedOwner(t, fx.db.WriteDB(), bob, "bob-sk")
	sharedSeedOwner(t, fx.db.WriteDB(), charlie, "charlie-sk")

	m := sharedSeedMedia(t, fx.db.WriteDB(), alice)
	live := sharedMakeMediaSetScopeOver(t, fx.shares, alice, bob, fx.now, false, m)
	sharedBumpActive(t, fx.db.WriteDB(), live.UUID, fx.now)
	other := sharedMakeMediaSetScopeOver(t, fx.shares, alice, charlie, fx.now, false, m)
	sharedBumpActive(t, fx.db.WriteDB(), other.UUID, fx.now)

	got, err := fx.svc.ListScopes(context.Background(), bob,
		[]string{live.UUID, other.UUID})
	r.NoError(err)
	r.Len(got, 1)
	r.Equal(live.UUID, got[0].UUID)
	r.Equal(1, got[0].ItemCount)
}

func TestSharedReadGetScopeEnforcesHeaderMembership(t *testing.T) {
	r := require.New(t)
	fx := newSharedReadFixture(t)
	alice := owners.Principal{Hub: "h", UserID: "alice"}
	bob := owners.Principal{Hub: "h", UserID: "bob"}
	sharedSeedOwner(t, fx.db.WriteDB(), alice, "alice-sk")
	sharedSeedOwner(t, fx.db.WriteDB(), bob, "bob-sk")

	m := sharedSeedMedia(t, fx.db.WriteDB(), alice)
	live := sharedMakeMediaSetScopeOver(t, fx.shares, alice, bob, fx.now, false, m)
	sharedBumpActive(t, fx.db.WriteDB(), live.UUID, fx.now)
	ghost := sharedMakeMediaSetScopeOver(t, fx.shares, alice, bob, fx.now, false, m)
	sharedBumpActive(t, fx.db.WriteDB(), ghost.UUID, fx.now)

	_, err := fx.svc.GetScope(context.Background(), bob,
		[]string{live.UUID}, ghost.UUID)
	require.ErrorIs(t, err, errs.ErrNotFound)

	got, err := fx.svc.GetScope(context.Background(), bob,
		[]string{live.UUID}, live.UUID)
	r.NoError(err)
	r.Equal(live.UUID, got.UUID)
	r.ElementsMatch([]string{m}, got.MediaIDs)
}

func TestSharedReadGetScopeRevokedReturnsNotFound(t *testing.T) {
	fx := newSharedReadFixture(t)
	alice := owners.Principal{Hub: "h", UserID: "alice"}
	bob := owners.Principal{Hub: "h", UserID: "bob"}
	sharedSeedOwner(t, fx.db.WriteDB(), alice, "alice-sk")
	sharedSeedOwner(t, fx.db.WriteDB(), bob, "bob-sk")
	m := sharedSeedMedia(t, fx.db.WriteDB(), alice)
	live := sharedMakeMediaSetScopeOver(t, fx.shares, alice, bob, fx.now, false, m)
	sharedBumpActive(t, fx.db.WriteDB(), live.UUID, fx.now)
	_, err := fx.shares.SetRevoking(context.Background(), live.UUID, fx.now)
	require.NoError(t, err)

	_, err = fx.svc.GetScope(context.Background(),
		owners.Principal{Hub: "h", UserID: "bob"},
		[]string{live.UUID}, live.UUID)
	require.ErrorIs(t, err, errs.ErrNotFound)
}

func TestSharedReadGetScopeAlbumLiveItemCount(t *testing.T) {
	r := require.New(t)
	fx := newSharedReadFixture(t)
	alice := owners.Principal{Hub: "h", UserID: "alice"}
	bob := owners.Principal{Hub: "h", UserID: "bob"}
	sharedSeedOwner(t, fx.db.WriteDB(), alice, "alice-sk")
	sharedSeedOwner(t, fx.db.WriteDB(), bob, "bob-sk")

	albumID, _ := sharedSeedAlbumWithMedia(t, fx.db.WriteDB(), alice, 3)
	live := sharedMakeAlbumLiveScope(t, fx.shares, alice, bob, albumID, fx.now, false)
	sharedBumpActive(t, fx.db.WriteDB(), live.UUID, fx.now)

	got, err := fx.svc.GetScope(context.Background(), bob,
		[]string{live.UUID}, live.UUID)
	r.NoError(err)
	r.Equal(live.UUID, got.UUID)
	r.Equal(share.TargetAlbumLive, got.TargetType)
	r.Equal(3, got.ItemCount)
	r.Empty(got.MediaIDs, "album_live should not expose frozen media_ids")
}

// sharedSeedAlbumWithMediaTimestamped seeds n media with distinct
// timestamps descending from baseTime (so display_time ordering is
// deterministic for pagination tests). Returns (albumID, mediaIDs in
// baseTime-descending order).
func sharedSeedAlbumWithMediaTimestamped(t *testing.T, rw *sql.DB, owner owners.Principal, baseTime time.Time, n int) (string, []string) {
	t.Helper()
	now := time.Now().UTC()
	albumID := uuid.NewString()
	_, err := rw.ExecContext(context.Background(),
		`INSERT INTO albums(id, owner_hub, owner_user_id, name, created_at, updated_at) VALUES(?,?,?,?,?,?)`,
		albumID, owner.Hub, owner.UserID, "t", now, now)
	require.NoError(t, err)
	mRepo := media.NewRepo(rw, rw)
	mediaIDs := make([]string, 0, n)
	for i := range n {
		ts := baseTime.Add(-time.Duration(i) * time.Minute) // newest first
		cs := uuid.NewString()
		id := uuid.NewString()
		require.NoError(t, mRepo.Insert(context.Background(), media.Media{
			ID: id, Owner: owner, Type: media.TypePhoto,
			MimeType: "image/jpeg", Path: "2024/" + cs + ".jpg",
			OriginalFilename: "x.jpg",
			ImportedAt:       now, Timestamp: &ts,
			Size: 100, Checksum: cs, ThumbStatus: "pending",
		}))
		_, err := rw.ExecContext(context.Background(),
			`INSERT INTO album_media(album_id, media_id, added_at) VALUES(?,?,?)`,
			albumID, id, now)
		require.NoError(t, err)
		mediaIDs = append(mediaIDs, id)
	}
	return albumID, mediaIDs
}

func TestSharedReadListAlbumsReturnsAlbumLiveOnly(t *testing.T) {
	r := require.New(t)
	fx := newSharedReadFixture(t)
	alice := owners.Principal{Hub: "h", UserID: "alice"}
	bob := owners.Principal{Hub: "h", UserID: "bob"}
	sharedSeedOwner(t, fx.db.WriteDB(), alice, "alice-sk")
	sharedSeedOwner(t, fx.db.WriteDB(), bob, "bob-sk")

	albumID, mediaIDs := sharedSeedAlbumWithMedia(t, fx.db.WriteDB(), alice, 2)
	// Two album_live scopes over the same album — download OR'd across.
	live1 := sharedMakeAlbumLiveScope(t, fx.shares, alice, bob, albumID, fx.now, false)
	sharedBumpActive(t, fx.db.WriteDB(), live1.UUID, fx.now)
	live2 := sharedMakeAlbumLiveScope(t, fx.shares, alice, bob, albumID, fx.now, true)
	sharedBumpActive(t, fx.db.WriteDB(), live2.UUID, fx.now)
	// media_set over same media must not surface as an album
	ms := sharedMakeMediaSetScopeOver(t, fx.shares, alice, bob, fx.now, false, mediaIDs...)
	sharedBumpActive(t, fx.db.WriteDB(), ms.UUID, fx.now)

	got, err := fx.svc.ListAlbums(context.Background(), bob,
		[]string{live1.UUID, live2.UUID, ms.UUID})
	r.NoError(err)
	r.Len(got, 1)
	r.Equal(albumID, got[0].ID)
	r.Equal(2, got[0].ItemCount)
	r.True(got[0].CanDownload, "two album_live scopes — OR(false, true) == true")
}

func TestSharedReadListAlbumsCanDownloadFalseWhenNoDownloadScope(t *testing.T) {
	r := require.New(t)
	fx := newSharedReadFixture(t)
	alice := owners.Principal{Hub: "h", UserID: "alice"}
	bob := owners.Principal{Hub: "h", UserID: "bob"}
	sharedSeedOwner(t, fx.db.WriteDB(), alice, "alice-sk")
	sharedSeedOwner(t, fx.db.WriteDB(), bob, "bob-sk")

	albumID, _ := sharedSeedAlbumWithMedia(t, fx.db.WriteDB(), alice, 1)
	live := sharedMakeAlbumLiveScope(t, fx.shares, alice, bob, albumID, fx.now, false)
	sharedBumpActive(t, fx.db.WriteDB(), live.UUID, fx.now)

	got, err := fx.svc.ListAlbums(context.Background(), bob, []string{live.UUID})
	r.NoError(err)
	r.Len(got, 1)
	r.False(got[0].CanDownload, "single album_live scope with download=false")
}

func TestSharedReadGetAlbumUnauthorizedReturns404(t *testing.T) {
	fx := newSharedReadFixture(t)
	alice := owners.Principal{Hub: "h", UserID: "alice"}
	bob := owners.Principal{Hub: "h", UserID: "bob"}
	sharedSeedOwner(t, fx.db.WriteDB(), alice, "alice-sk")
	sharedSeedOwner(t, fx.db.WriteDB(), bob, "bob-sk")
	albumID, _ := sharedSeedAlbumWithMedia(t, fx.db.WriteDB(), alice, 1)
	_, err := fx.svc.GetAlbum(context.Background(), bob, nil, albumID)
	require.ErrorIs(t, err, errs.ErrNotFound)
}

func TestSharedReadListAlbumMediaPaginates(t *testing.T) {
	r := require.New(t)
	fx := newSharedReadFixture(t)
	alice := owners.Principal{Hub: "h", UserID: "alice"}
	bob := owners.Principal{Hub: "h", UserID: "bob"}
	sharedSeedOwner(t, fx.db.WriteDB(), alice, "alice-sk")
	sharedSeedOwner(t, fx.db.WriteDB(), bob, "bob-sk")
	albumID, _ := sharedSeedAlbumWithMediaTimestamped(t, fx.db.WriteDB(), alice, fx.now, 3)
	live := sharedMakeAlbumLiveScope(t, fx.shares, alice, bob, albumID, fx.now, false)
	sharedBumpActive(t, fx.db.WriteDB(), live.UUID, fx.now)

	page, nextCursor, err := fx.svc.ListAlbumMedia(context.Background(),
		bob, []string{live.UUID}, albumID,
		service.SharedMediaCursor{Limit: 2})
	r.NoError(err)
	r.Len(page, 2)
	r.NotEmpty(nextCursor.AfterID)
	r.False(page[0].CanDownload, "single download=false scope → per-media CanDownload stays false")

	page2, nextCursor2, err := fx.svc.ListAlbumMedia(context.Background(),
		bob, []string{live.UUID}, albumID, nextCursor)
	r.NoError(err)
	r.Len(page2, 1)
	r.Empty(nextCursor2.AfterID) // exhausted
}

// ListAlbums must order rows by updated_at DESC, then id ASC. Seeded
// with two albums at distinct updated_at values so the sort closure is
// exercised (the single-album tests above do not).
func TestSharedReadListAlbumsSortsByUpdatedAtDesc(t *testing.T) {
	r := require.New(t)
	fx := newSharedReadFixture(t)
	alice := owners.Principal{Hub: "h", UserID: "alice"}
	bob := owners.Principal{Hub: "h", UserID: "bob"}
	sharedSeedOwner(t, fx.db.WriteDB(), alice, "alice-sk")
	sharedSeedOwner(t, fx.db.WriteDB(), bob, "bob-sk")

	tsOlder := fx.now
	tsNewer := fx.now.Add(time.Hour)
	olderID := uuid.NewString()
	newerID := uuid.NewString()
	_, err := fx.db.WriteDB().ExecContext(context.Background(),
		`INSERT INTO albums(id, owner_hub, owner_user_id, name, created_at, updated_at) VALUES(?,?,?,?,?,?)`,
		olderID, alice.Hub, alice.UserID, "older", tsOlder, tsOlder)
	r.NoError(err)
	_, err = fx.db.WriteDB().ExecContext(context.Background(),
		`INSERT INTO albums(id, owner_hub, owner_user_id, name, created_at, updated_at) VALUES(?,?,?,?,?,?)`,
		newerID, alice.Hub, alice.UserID, "newer", tsNewer, tsNewer)
	r.NoError(err)

	live1 := sharedMakeAlbumLiveScope(t, fx.shares, alice, bob, olderID, fx.now, false)
	sharedBumpActive(t, fx.db.WriteDB(), live1.UUID, fx.now)
	live2 := sharedMakeAlbumLiveScope(t, fx.shares, alice, bob, newerID, fx.now, false)
	sharedBumpActive(t, fx.db.WriteDB(), live2.UUID, fx.now)

	got, err := fx.svc.ListAlbums(context.Background(), bob,
		[]string{live1.UUID, live2.UUID})
	r.NoError(err)
	r.Len(got, 2)
	r.Equal(newerID, got[0].ID, "updated_at DESC puts newer first")
	r.Equal(olderID, got[1].ID)
}

// ListMedia returns every media covered by any authorised scope, with
// per-media CanDownload OR'd across overlapping scopes. Ordering is
// display_time DESC + id ASC from share.Repo.ListSharedMediaIDs.
func TestSharedReadListMediaUnionOfScopes(t *testing.T) {
	r := require.New(t)
	fx := newSharedReadFixture(t)
	alice := owners.Principal{Hub: "h", UserID: "alice"}
	bob := owners.Principal{Hub: "h", UserID: "bob"}
	sharedSeedOwner(t, fx.db.WriteDB(), alice, "alice-sk")
	sharedSeedOwner(t, fx.db.WriteDB(), bob, "bob-sk")

	t0 := fx.now.Add(-2 * time.Hour)
	t1 := fx.now.Add(-1 * time.Hour)
	t2 := fx.now
	m1 := sharedSeedMediaWithTimestamp(t, fx.db.WriteDB(), alice, t0)
	m2 := sharedSeedMediaWithTimestamp(t, fx.db.WriteDB(), alice, t1)
	m3 := sharedSeedMediaWithTimestamp(t, fx.db.WriteDB(), alice, t2)

	s1 := sharedMakeMediaSetScopeOver(t, fx.shares, alice, bob, fx.now, false, m1, m2)
	sharedBumpActive(t, fx.db.WriteDB(), s1.UUID, fx.now)
	s2 := sharedMakeMediaSetScopeOver(t, fx.shares, alice, bob, fx.now, true, m2, m3)
	sharedBumpActive(t, fx.db.WriteDB(), s2.UUID, fx.now)

	page, next, err := fx.svc.ListMedia(context.Background(), bob,
		[]string{s1.UUID, s2.UUID},
		service.SharedMediaCursor{Limit: 10})
	r.NoError(err)
	r.Len(page, 3)
	r.Equal(m3, page[0].ID)
	r.Equal(m2, page[1].ID)
	r.Equal(m1, page[2].ID)
	r.True(page[0].CanDownload, "m3 is covered only by s2 (download=true)")
	r.True(page[1].CanDownload, "m2 is covered by s1 (false) and s2 (true) — OR is true")
	r.False(page[2].CanDownload, "m1 is covered only by s1 (download=false)")
	r.Empty(next.AfterID, "single page — no next cursor")
}

func TestSharedReadGetMediaUnauthorizedReturns404(t *testing.T) {
	fx := newSharedReadFixture(t)
	alice := owners.Principal{Hub: "h", UserID: "alice"}
	bob := owners.Principal{Hub: "h", UserID: "bob"}
	sharedSeedOwner(t, fx.db.WriteDB(), alice, "alice-sk")
	sharedSeedOwner(t, fx.db.WriteDB(), bob, "bob-sk")
	m := sharedSeedMedia(t, fx.db.WriteDB(), alice)
	_, err := fx.svc.GetMedia(context.Background(), bob, nil, m)
	require.ErrorIs(t, err, errs.ErrNotFound)
}

func TestSharedReadGetMediaAuthorizedSetsCanDownload(t *testing.T) {
	r := require.New(t)
	fx := newSharedReadFixture(t)
	alice := owners.Principal{Hub: "h", UserID: "alice"}
	bob := owners.Principal{Hub: "h", UserID: "bob"}
	sharedSeedOwner(t, fx.db.WriteDB(), alice, "alice-sk")
	sharedSeedOwner(t, fx.db.WriteDB(), bob, "bob-sk")
	m := sharedSeedMedia(t, fx.db.WriteDB(), alice)
	s := sharedMakeMediaSetScopeOver(t, fx.shares, alice, bob, fx.now, true, m)
	sharedBumpActive(t, fx.db.WriteDB(), s.UUID, fx.now)

	got, err := fx.svc.GetMedia(context.Background(), bob, []string{s.UUID}, m)
	r.NoError(err)
	r.Equal(m, got.ID)
	r.True(got.CanDownload)
}

// sharedSeedStoredMedia inserts a media row owned by p and writes the
// given body into the fixture's storage at the row's path. Returns the
// media ID and body for convenience.
func sharedSeedStoredMedia(t *testing.T, fx sharedReadFixture, p owners.Principal, body string) (string, string) {
	t.Helper()
	cs := uuid.NewString()
	path := "2024/" + cs + ".jpg"
	m := media.Media{
		ID: uuid.NewString(), Owner: p, Type: media.TypePhoto,
		MimeType: "image/jpeg", Path: path,
		OriginalFilename: "x.jpg",
		ImportedAt:       time.Now().UTC().Truncate(time.Second),
		Size:             int64(len(body)), Checksum: cs, ThumbStatus: "pending",
	}
	require.NoError(t, fx.mediaR.Insert(context.Background(), m))
	_, err := fx.store.Write(context.Background(), p, path, bytes.NewReader([]byte(body)))
	require.NoError(t, err)
	return m.ID, body
}

// sharedSeedMediaWithReadyThumb seeds a media row with thumb_status='ready'
// and writes the thumb bytes into the fixture's storage at the computed
// thumb.ThumbKey. Returns (mediaID, thumbVersion).
func sharedSeedMediaWithReadyThumb(t *testing.T, fx sharedReadFixture, p owners.Principal, body string) (string, int) {
	t.Helper()
	cs := uuid.NewString()
	version := 1
	updatedAt := time.Now().UTC().Truncate(time.Second)
	m := media.Media{
		ID: uuid.NewString(), Owner: p, Type: media.TypePhoto,
		MimeType: "image/jpeg", Path: "2024/" + cs + ".jpg",
		OriginalFilename: "x.jpg",
		ImportedAt:       updatedAt,
		Size:             100, Checksum: cs,
		ThumbStatus:    "pending",
		ThumbVersion:   version,
		ThumbUpdatedAt: nil,
	}
	require.NoError(t, fx.mediaR.Insert(context.Background(), m))
	_, err := fx.db.WriteDB().ExecContext(context.Background(),
		`UPDATE media SET thumb_status='ready', thumb_updated_at=? WHERE id=?`,
		updatedAt, m.ID)
	require.NoError(t, err)
	key := thumb.ThumbKey(m.ID, version, thumb.SizeGrid)
	_, err = fx.store.Write(context.Background(), p, key, bytes.NewReader([]byte(body)))
	require.NoError(t, err)
	return m.ID, version
}

func TestSharedReadOpenOriginalAuthorizedWithDownload(t *testing.T) {
	r := require.New(t)
	fx := newSharedReadFixture(t)
	alice := owners.Principal{Hub: "h", UserID: "alice"}
	bob := owners.Principal{Hub: "h", UserID: "bob"}
	sharedSeedOwner(t, fx.db.WriteDB(), alice, "alice-sk")
	sharedSeedOwner(t, fx.db.WriteDB(), bob, "bob-sk")

	mID, body := sharedSeedStoredMedia(t, fx, alice, "hello")
	s := sharedMakeMediaSetScopeOver(t, fx.shares, alice, bob, fx.now, true, mID)
	sharedBumpActive(t, fx.db.WriteDB(), s.UUID, fx.now)

	rc, row, err := fx.svc.OpenOriginal(context.Background(),
		bob, []string{s.UUID}, mID, 0, -1)
	r.NoError(err)
	defer rc.Close()
	got, err := io.ReadAll(rc)
	r.NoError(err)
	r.Equal(body, string(got))
	r.Equal(mID, row.ID)
}

func TestSharedReadOpenOriginalAuthorizedWithoutDownloadReturnsForbidden(t *testing.T) {
	fx := newSharedReadFixture(t)
	alice := owners.Principal{Hub: "h", UserID: "alice"}
	bob := owners.Principal{Hub: "h", UserID: "bob"}
	sharedSeedOwner(t, fx.db.WriteDB(), alice, "alice-sk")
	sharedSeedOwner(t, fx.db.WriteDB(), bob, "bob-sk")
	mID, _ := sharedSeedStoredMedia(t, fx, alice, "hello")
	s := sharedMakeMediaSetScopeOver(t, fx.shares, alice, bob, fx.now, false, mID)
	sharedBumpActive(t, fx.db.WriteDB(), s.UUID, fx.now)

	_, _, err := fx.svc.OpenOriginal(context.Background(),
		bob, []string{s.UUID}, mID, 0, -1)
	require.ErrorIs(t, err, errs.ErrPermissionDenied)
}

func TestSharedReadOpenOriginalUnauthorizedReturnsNotFound(t *testing.T) {
	fx := newSharedReadFixture(t)
	alice := owners.Principal{Hub: "h", UserID: "alice"}
	bob := owners.Principal{Hub: "h", UserID: "bob"}
	sharedSeedOwner(t, fx.db.WriteDB(), alice, "alice-sk")
	sharedSeedOwner(t, fx.db.WriteDB(), bob, "bob-sk")
	mID, _ := sharedSeedStoredMedia(t, fx, alice, "hello")
	_, _, err := fx.svc.OpenOriginal(context.Background(),
		bob, nil, mID, 0, -1)
	require.ErrorIs(t, err, errs.ErrNotFound)
}

func TestSharedReadOpenOriginalRange(t *testing.T) {
	r := require.New(t)
	fx := newSharedReadFixture(t)
	alice := owners.Principal{Hub: "h", UserID: "alice"}
	bob := owners.Principal{Hub: "h", UserID: "bob"}
	sharedSeedOwner(t, fx.db.WriteDB(), alice, "alice-sk")
	sharedSeedOwner(t, fx.db.WriteDB(), bob, "bob-sk")
	mID, _ := sharedSeedStoredMedia(t, fx, alice, "0123456789")
	s := sharedMakeMediaSetScopeOver(t, fx.shares, alice, bob, fx.now, true, mID)
	sharedBumpActive(t, fx.db.WriteDB(), s.UUID, fx.now)

	rc, _, err := fx.svc.OpenOriginal(context.Background(),
		bob, []string{s.UUID}, mID, 2, 3)
	r.NoError(err)
	defer rc.Close()
	got, err := io.ReadAll(rc)
	r.NoError(err)
	r.Equal("234", string(got))
}

func TestSharedReadOpenThumbAuthorizedIgnoresDownload(t *testing.T) {
	r := require.New(t)
	fx := newSharedReadFixture(t)
	alice := owners.Principal{Hub: "h", UserID: "alice"}
	bob := owners.Principal{Hub: "h", UserID: "bob"}
	sharedSeedOwner(t, fx.db.WriteDB(), alice, "alice-sk")
	sharedSeedOwner(t, fx.db.WriteDB(), bob, "bob-sk")
	mID, version := sharedSeedMediaWithReadyThumb(t, fx, alice, "jpegbytes")
	// download=false on the scope — thumb must still be served.
	s := sharedMakeMediaSetScopeOver(t, fx.shares, alice, bob, fx.now, false, mID)
	sharedBumpActive(t, fx.db.WriteDB(), s.UUID, fx.now)

	rc, row, err := fx.svc.OpenThumb(context.Background(),
		bob, []string{s.UUID}, mID, thumb.SizeGrid, version)
	r.NoError(err)
	defer rc.Close()
	got, err := io.ReadAll(rc)
	r.NoError(err)
	r.Equal("jpegbytes", string(got))
	r.Equal(mID, row.ID)
	r.Equal(version, row.ThumbVersion)
}

func TestSharedReadOpenThumbUnauthorizedReturnsNotFound(t *testing.T) {
	fx := newSharedReadFixture(t)
	alice := owners.Principal{Hub: "h", UserID: "alice"}
	bob := owners.Principal{Hub: "h", UserID: "bob"}
	sharedSeedOwner(t, fx.db.WriteDB(), alice, "alice-sk")
	sharedSeedOwner(t, fx.db.WriteDB(), bob, "bob-sk")
	mID, version := sharedSeedMediaWithReadyThumb(t, fx, alice, "jpegbytes")

	_, _, err := fx.svc.OpenThumb(context.Background(),
		bob, nil, mID, thumb.SizeGrid, version)
	require.ErrorIs(t, err, errs.ErrNotFound)
}

func TestSharedReadOpenThumbVersionMismatchReturnsNotFound(t *testing.T) {
	fx := newSharedReadFixture(t)
	alice := owners.Principal{Hub: "h", UserID: "alice"}
	bob := owners.Principal{Hub: "h", UserID: "bob"}
	sharedSeedOwner(t, fx.db.WriteDB(), alice, "alice-sk")
	sharedSeedOwner(t, fx.db.WriteDB(), bob, "bob-sk")
	mID, version := sharedSeedMediaWithReadyThumb(t, fx, alice, "jpegbytes")
	s := sharedMakeMediaSetScopeOver(t, fx.shares, alice, bob, fx.now, false, mID)
	sharedBumpActive(t, fx.db.WriteDB(), s.UUID, fx.now)

	_, _, err := fx.svc.OpenThumb(context.Background(),
		bob, []string{s.UUID}, mID, thumb.SizeGrid, version+1)
	require.ErrorIs(t, err, errs.ErrNotFound)
}

func TestSharedReadOpenThumbPendingReturnsNotFound(t *testing.T) {
	fx := newSharedReadFixture(t)
	alice := owners.Principal{Hub: "h", UserID: "alice"}
	bob := owners.Principal{Hub: "h", UserID: "bob"}
	sharedSeedOwner(t, fx.db.WriteDB(), alice, "alice-sk")
	sharedSeedOwner(t, fx.db.WriteDB(), bob, "bob-sk")
	mID := sharedSeedMedia(t, fx.db.WriteDB(), alice) // thumb_status=pending
	s := sharedMakeMediaSetScopeOver(t, fx.shares, alice, bob, fx.now, false, mID)
	sharedBumpActive(t, fx.db.WriteDB(), s.UUID, fx.now)

	_, _, err := fx.svc.OpenThumb(context.Background(),
		bob, []string{s.UUID}, mID, thumb.SizeGrid, 0)
	require.ErrorIs(t, err, errs.ErrNotFound)
}

// sharedHideMedia stamps hidden_at on a media row.
func sharedHideMedia(t *testing.T, rw *sql.DB, mediaID string) {
	t.Helper()
	_, err := rw.ExecContext(context.Background(),
		`UPDATE media SET hidden_at = ? WHERE id = ?`, time.Now().UTC(), mediaID)
	require.NoError(t, err)
}

// TestSharedReadListMediaExcludesHidden verifies that a hidden shared
// photo is absent from ListMedia even when an active scope covers it.
func TestSharedReadListMediaExcludesHidden(t *testing.T) {
	r := require.New(t)
	fx := newSharedReadFixture(t)
	alice := owners.Principal{Hub: "h", UserID: "alice"}
	bob := owners.Principal{Hub: "h", UserID: "bob"}
	sharedSeedOwner(t, fx.db.WriteDB(), alice, "alice-sk")
	sharedSeedOwner(t, fx.db.WriteDB(), bob, "bob-sk")

	visible := sharedSeedMedia(t, fx.db.WriteDB(), alice)
	hidden := sharedSeedMedia(t, fx.db.WriteDB(), alice)
	sharedHideMedia(t, fx.db.WriteDB(), hidden)

	s := sharedMakeMediaSetScopeOver(t, fx.shares, alice, bob, fx.now, false, visible, hidden)
	sharedBumpActive(t, fx.db.WriteDB(), s.UUID, fx.now)

	page, _, err := fx.svc.ListMedia(context.Background(), bob,
		[]string{s.UUID}, service.SharedMediaCursor{Limit: 10})
	r.NoError(err)
	r.Len(page, 1)
	r.Equal(visible, page[0].ID)
}

// TestSharedReadListAlbumMediaExcludesHidden verifies that a hidden
// album member is absent from ListAlbumMedia.
func TestSharedReadListAlbumMediaExcludesHidden(t *testing.T) {
	r := require.New(t)
	fx := newSharedReadFixture(t)
	alice := owners.Principal{Hub: "h", UserID: "alice"}
	bob := owners.Principal{Hub: "h", UserID: "bob"}
	sharedSeedOwner(t, fx.db.WriteDB(), alice, "alice-sk")
	sharedSeedOwner(t, fx.db.WriteDB(), bob, "bob-sk")

	albumID, mIDs := sharedSeedAlbumWithMedia(t, fx.db.WriteDB(), alice, 2)
	sharedHideMedia(t, fx.db.WriteDB(), mIDs[1])

	live := sharedMakeAlbumLiveScope(t, fx.shares, alice, bob, albumID, fx.now, false)
	sharedBumpActive(t, fx.db.WriteDB(), live.UUID, fx.now)

	page, _, err := fx.svc.ListAlbumMedia(context.Background(), bob,
		[]string{live.UUID}, albumID, service.SharedMediaCursor{Limit: 10})
	r.NoError(err)
	r.Len(page, 1, "hidden album member must be excluded from ListAlbumMedia")
	r.Equal(mIDs[0], page[0].ID)
}

// TestSharedReadGetMediaHiddenReturnsNotFound verifies that GetMedia
// returns ErrNotFound for a hidden shared photo.
func TestSharedReadGetMediaHiddenReturnsNotFound(t *testing.T) {
	fx := newSharedReadFixture(t)
	alice := owners.Principal{Hub: "h", UserID: "alice"}
	bob := owners.Principal{Hub: "h", UserID: "bob"}
	sharedSeedOwner(t, fx.db.WriteDB(), alice, "alice-sk")
	sharedSeedOwner(t, fx.db.WriteDB(), bob, "bob-sk")
	mID := sharedSeedMedia(t, fx.db.WriteDB(), alice)
	sharedHideMedia(t, fx.db.WriteDB(), mID)
	s := sharedMakeMediaSetScopeOver(t, fx.shares, alice, bob, fx.now, true, mID)
	sharedBumpActive(t, fx.db.WriteDB(), s.UUID, fx.now)

	_, err := fx.svc.GetMedia(context.Background(), bob, []string{s.UUID}, mID)
	require.ErrorIs(t, err, errs.ErrNotFound)
}

// TestSharedReadOpenOriginalHiddenReturnsNotFound verifies that
// OpenOriginal returns ErrNotFound for a hidden shared photo.
func TestSharedReadOpenOriginalHiddenReturnsNotFound(t *testing.T) {
	fx := newSharedReadFixture(t)
	alice := owners.Principal{Hub: "h", UserID: "alice"}
	bob := owners.Principal{Hub: "h", UserID: "bob"}
	sharedSeedOwner(t, fx.db.WriteDB(), alice, "alice-sk")
	sharedSeedOwner(t, fx.db.WriteDB(), bob, "bob-sk")
	mID, _ := sharedSeedStoredMedia(t, fx, alice, "bytes")
	sharedHideMedia(t, fx.db.WriteDB(), mID)
	s := sharedMakeMediaSetScopeOver(t, fx.shares, alice, bob, fx.now, true, mID)
	sharedBumpActive(t, fx.db.WriteDB(), s.UUID, fx.now)

	_, _, err := fx.svc.OpenOriginal(context.Background(),
		bob, []string{s.UUID}, mID, 0, -1)
	require.ErrorIs(t, err, errs.ErrNotFound)
}

// TestSharedReadOpenThumbHiddenReturnsNotFound verifies that OpenThumb
// returns ErrNotFound for a hidden shared photo.
func TestSharedReadOpenThumbHiddenReturnsNotFound(t *testing.T) {
	fx := newSharedReadFixture(t)
	alice := owners.Principal{Hub: "h", UserID: "alice"}
	bob := owners.Principal{Hub: "h", UserID: "bob"}
	sharedSeedOwner(t, fx.db.WriteDB(), alice, "alice-sk")
	sharedSeedOwner(t, fx.db.WriteDB(), bob, "bob-sk")
	mID, version := sharedSeedMediaWithReadyThumb(t, fx, alice, "jpegbytes")
	sharedHideMedia(t, fx.db.WriteDB(), mID)
	s := sharedMakeMediaSetScopeOver(t, fx.shares, alice, bob, fx.now, false, mID)
	sharedBumpActive(t, fx.db.WriteDB(), s.UUID, fx.now)

	_, _, err := fx.svc.OpenThumb(context.Background(),
		bob, []string{s.UUID}, mID, thumb.SizeGrid, version)
	require.ErrorIs(t, err, errs.ErrNotFound)
}
