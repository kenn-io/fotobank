package share_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/album"
	"github.com/wesm/fotobank/internal/errs"
	"github.com/wesm/fotobank/internal/media"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/share"
	"github.com/wesm/fotobank/internal/testutil"
)

// seedOwner inserts a minimal owners row so scopes FK-checks pass.
func seedOwner(t *testing.T, rw *sql.DB, p owners.Principal, storageKey string) {
	t.Helper()
	_, err := rw.ExecContext(context.Background(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		p.Hub, p.UserID, storageKey, time.Now().UTC(),
	)
	require.NoError(t, err)
}

// seedAlbum inserts a minimal album row owned by p.
func seedAlbum(t *testing.T, rw *sql.DB, p owners.Principal) string {
	t.Helper()
	id := uuid.NewString()
	now := time.Now().UTC()
	_, err := rw.ExecContext(context.Background(),
		`INSERT INTO albums(id, owner_hub, owner_user_id, name, created_at, updated_at)
         VALUES(?,?,?,?,?,?)`,
		id, p.Hub, p.UserID, "t", now, now)
	require.NoError(t, err)
	return id
}

// seedMedia inserts a minimal media row owned by p and returns its ID.
func seedMedia(t *testing.T, rw *sql.DB, p owners.Principal, checksum string) string {
	t.Helper()
	repo := media.NewRepo(rw, rw)
	m := media.Media{
		ID: uuid.NewString(), Owner: p, Type: media.TypePhoto,
		MimeType: "image/jpeg", Path: "2024/" + checksum + ".jpg",
		OriginalFilename: "x.jpg", ImportedAt: time.Now().UTC().Truncate(time.Second),
		Size: 100, Checksum: checksum, ThumbStatus: "pending",
	}
	require.NoError(t, repo.Insert(context.Background(), m))
	return m.ID
}

func TestRepoInsertAlbumLiveAndGet(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	owner := owners.Principal{Hub: "h", UserID: "o"}
	seedOwner(t, d.WriteDB(), owner, "sk")
	albumID := seedAlbum(t, d.WriteDB(), owner)

	repo := share.NewRepo(d.WriteDB(), d.ReadDB())
	s := share.Scope{
		UUID: uuid.NewString(), Owner: owner,
		Grantee:       owners.Principal{Hub: "h", UserID: "g"},
		TargetType:    share.TargetAlbumLive,
		TargetAlbumID: &albumID,
		Label:         "Summer", CreatedAt: time.Now().UTC().Truncate(time.Second),
		BrokerStatus: share.StatusPending,
	}
	r.NoError(repo.Insert(context.Background(), s, nil))

	got, err := repo.GetByUUID(context.Background(), s.UUID)
	r.NoError(err)
	r.Equal(s.UUID, got.UUID)
	r.Equal(owner, got.Owner)
	r.Equal(share.TargetAlbumLive, got.TargetType)
	r.NotNil(got.TargetAlbumID)
	r.Equal(albumID, *got.TargetAlbumID)
	r.Equal(share.StatusPending, got.BrokerStatus)
	r.Empty(got.MediaIDs)
}

func TestRepoInsertMediaSetAndGet(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	owner := owners.Principal{Hub: "h", UserID: "o"}
	seedOwner(t, d.WriteDB(), owner, "sk")
	m1 := seedMedia(t, d.WriteDB(), owner, "c1")
	m2 := seedMedia(t, d.WriteDB(), owner, "c2")

	repo := share.NewRepo(d.WriteDB(), d.ReadDB())
	s := share.Scope{
		UUID: uuid.NewString(), Owner: owner,
		Grantee:      owners.Principal{Hub: "h", UserID: "g"},
		TargetType:   share.TargetMediaSet,
		CreatedAt:    time.Now().UTC().Truncate(time.Second),
		BrokerStatus: share.StatusPending,
	}
	r.NoError(repo.Insert(context.Background(), s, []string{m1, m2}))

	got, err := repo.GetByUUID(context.Background(), s.UUID)
	r.NoError(err)
	r.Equal(share.TargetMediaSet, got.TargetType)
	r.Nil(got.TargetAlbumID)
	r.ElementsMatch([]string{m1, m2}, got.MediaIDs)
}

func TestRepoGetByUUIDNotFound(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := share.NewRepo(d.WriteDB(), d.ReadDB())
	_, err := repo.GetByUUID(context.Background(), uuid.NewString())
	r.ErrorIs(err, errs.ErrNotFound)
}

func TestRepoInsertRoundtripsAllColumns(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	owner := owners.Principal{Hub: "h", UserID: "o"}
	seedOwner(t, d.WriteDB(), owner, "sk")
	albumID := seedAlbum(t, d.WriteDB(), owner)

	expires := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Second)
	repo := share.NewRepo(d.WriteDB(), d.ReadDB())
	s := share.Scope{
		UUID: uuid.NewString(), Owner: owner,
		Grantee:       owners.Principal{Hub: "h", UserID: "g"},
		TargetType:    share.TargetAlbumLive,
		TargetAlbumID: &albumID,
		AllowDownload: true,
		Label:         "Trip",
		ExpiresAt:     &expires,
		CreatedAt:     time.Now().UTC().Truncate(time.Second),
		BrokerStatus:  share.StatusPending,
	}
	r.NoError(repo.Insert(context.Background(), s, nil))

	got, err := repo.GetByUUID(context.Background(), s.UUID)
	r.NoError(err)
	r.True(got.AllowDownload)
	r.Equal("Trip", got.Label)
	r.NotNil(got.ExpiresAt)
	r.True(got.ExpiresAt.Equal(expires))
}

// Ensure the album import is used so goimports keeps it; pinned reference.
var _ = album.NameMaxLen

// Silence ctx/errors unused-import warnings until state-transition tests arrive.
var _ = errors.New
