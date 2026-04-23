package album_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/album"
	"github.com/wesm/fotobank/internal/errs"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/testutil"
)

// seedOwner inserts a minimal owners row so album FK constraints resolve.
func seedOwner(t *testing.T, rw *sql.DB, p owners.Principal, sk string) {
	t.Helper()
	_, err := rw.ExecContext(context.Background(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		p.Hub, p.UserID, sk, time.Now().UTC(),
	)
	require.NoError(t, err)
}

// seedAlbum inserts an album via the repo.
func seedAlbum(t *testing.T, r *album.Repo, p owners.Principal, name string) album.Album {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	a := album.Album{
		ID:        uuid.NewString(),
		Owner:     p,
		Name:      name,
		CreatedAt: now,
		UpdatedAt: now,
	}
	require.NoError(t, r.Insert(context.Background(), a))
	return a
}

func TestRepoInsertAndGetByID(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := album.NewRepo(d.WriteDB(), d.ReadDB())
	p := owners.Principal{Hub: "h", UserID: "u"}
	seedOwner(t, d.WriteDB(), p, "sk")

	a := seedAlbum(t, repo, p, "Trip")
	got, err := repo.GetByID(context.Background(), a.ID)
	r.NoError(err)
	r.Equal(a.ID, got.ID)
	r.Equal("Trip", got.Name)
	r.Equal(p, got.Owner)
}

func TestRepoGetByIDMissing(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := album.NewRepo(d.WriteDB(), d.ReadDB())
	_, err := repo.GetByID(context.Background(), "nope")
	r.ErrorIs(err, errs.ErrNotFound)
}

func TestRepoRenameUpdatesName(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := album.NewRepo(d.WriteDB(), d.ReadDB())
	p := owners.Principal{Hub: "h", UserID: "u"}
	seedOwner(t, d.WriteDB(), p, "sk")
	a := seedAlbum(t, repo, p, "Old")

	// Move the clock forward so updated_at can change.
	later := a.UpdatedAt.Add(2 * time.Second)
	r.NoError(repo.Rename(context.Background(), a.ID, "New", later))

	got, err := repo.GetByID(context.Background(), a.ID)
	r.NoError(err)
	r.Equal("New", got.Name)
	r.True(got.UpdatedAt.Equal(later), "updated_at should advance: got %v want %v", got.UpdatedAt, later)
	r.True(got.CreatedAt.Equal(a.CreatedAt), "created_at must not change")
}

func TestRepoRenameMissing(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := album.NewRepo(d.WriteDB(), d.ReadDB())
	err := repo.Rename(context.Background(), "nope", "x", time.Now().UTC())
	r.ErrorIs(err, errs.ErrNotFound)
}

func TestRepoDelete(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := album.NewRepo(d.WriteDB(), d.ReadDB())
	p := owners.Principal{Hub: "h", UserID: "u"}
	seedOwner(t, d.WriteDB(), p, "sk")
	a := seedAlbum(t, repo, p, "Trip")

	r.NoError(repo.Delete(context.Background(), a.ID))

	_, err := repo.GetByID(context.Background(), a.ID)
	r.ErrorIs(err, errs.ErrNotFound)
}

func TestRepoDeleteMissing(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := album.NewRepo(d.WriteDB(), d.ReadDB())
	err := repo.Delete(context.Background(), "nope")
	r.ErrorIs(err, errs.ErrNotFound)
}

// seedMediaRow inserts a minimal media row. We do this with raw SQL
// (not through media.Repo) to keep album tests independent of the media
// package's insert surface, which requires many more fields.
func seedMediaRow(t *testing.T, rw *sql.DB, p owners.Principal, id, checksum, thumbStatus string, thumbVersion int) {
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
          ?, ?, NULL)`,
		id, p.Hub, p.UserID, "p/"+id, time.Now().UTC(), checksum, thumbStatus, thumbVersion,
	)
	require.NoError(t, err)
}

// seedAlbumMedia inserts directly into album_media (bypassing the service)
// using the given added_at.
func seedAlbumMedia(t *testing.T, rw *sql.DB, albumID, mediaID string, addedAt time.Time) {
	t.Helper()
	_, err := rw.ExecContext(context.Background(),
		`INSERT INTO album_media(album_id, media_id, added_at) VALUES(?,?,?)`,
		albumID, mediaID, addedAt,
	)
	require.NoError(t, err)
}

func TestRepoGetDetailByIDEmpty(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := album.NewRepo(d.WriteDB(), d.ReadDB())
	p := owners.Principal{Hub: "h", UserID: "u"}
	seedOwner(t, d.WriteDB(), p, "sk")
	a := seedAlbum(t, repo, p, "Empty")

	got, err := repo.GetDetailByID(context.Background(), a.ID)
	r.NoError(err)
	r.Equal(0, got.ItemCount)
	r.Nil(got.Cover)
	r.Equal("Empty", got.Name)
}

func TestRepoGetDetailByIDWithReadyCover(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := album.NewRepo(d.WriteDB(), d.ReadDB())
	p := owners.Principal{Hub: "h", UserID: "u"}
	seedOwner(t, d.WriteDB(), p, "sk")
	a := seedAlbum(t, repo, p, "Trip")

	// Older pending member, newer ready member — cover should be the newer one.
	older := uuid.NewString()
	newer := uuid.NewString()
	seedMediaRow(t, d.WriteDB(), p, older, "cs-o", "pending", 0)
	seedMediaRow(t, d.WriteDB(), p, newer, "cs-n", "ready", 3)
	base := time.Now().UTC().Truncate(time.Second)
	seedAlbumMedia(t, d.WriteDB(), a.ID, older, base)
	seedAlbumMedia(t, d.WriteDB(), a.ID, newer, base.Add(time.Second))

	got, err := repo.GetDetailByID(context.Background(), a.ID)
	r.NoError(err)
	r.Equal(2, got.ItemCount)
	r.NotNil(got.Cover)
	r.Equal(newer, got.Cover.MediaID)
	r.Equal(3, got.Cover.ThumbVersion)
}

func TestRepoGetDetailByIDPendingOnlyNilCover(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := album.NewRepo(d.WriteDB(), d.ReadDB())
	p := owners.Principal{Hub: "h", UserID: "u"}
	seedOwner(t, d.WriteDB(), p, "sk")
	a := seedAlbum(t, repo, p, "Pending")

	m := uuid.NewString()
	seedMediaRow(t, d.WriteDB(), p, m, "cs", "pending", 0)
	seedAlbumMedia(t, d.WriteDB(), a.ID, m, time.Now().UTC())

	got, err := repo.GetDetailByID(context.Background(), a.ID)
	r.NoError(err)
	r.Equal(1, got.ItemCount)
	r.Nil(got.Cover, "all members pending → cover must be nil")
}

func TestRepoGetDetailByIDMissing(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := album.NewRepo(d.WriteDB(), d.ReadDB())
	_, err := repo.GetDetailByID(context.Background(), "nope")
	r.ErrorIs(err, errs.ErrNotFound)
}

func TestRepoListByOwnerSortsByUpdatedDesc(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := album.NewRepo(d.WriteDB(), d.ReadDB())
	p := owners.Principal{Hub: "h", UserID: "u"}
	seedOwner(t, d.WriteDB(), p, "sk")

	oldA := seedAlbum(t, repo, p, "Old")
	// Advance the updated_at on the second album explicitly so the
	// test doesn't depend on same-second timestamps colliding.
	newA := seedAlbum(t, repo, p, "New")
	later := newA.UpdatedAt.Add(10 * time.Second)
	r.NoError(repo.Rename(context.Background(), newA.ID, "New", later))

	items, err := repo.ListByOwner(context.Background(), p, 10, 0)
	r.NoError(err)
	r.Len(items, 2)
	r.Equal(newA.ID, items[0].ID, "most recently updated album first")
	r.Equal(oldA.ID, items[1].ID)
}

func TestRepoListByOwnerIsolatesOwners(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := album.NewRepo(d.WriteDB(), d.ReadDB())
	pA := owners.Principal{Hub: "h", UserID: "a"}
	pB := owners.Principal{Hub: "h", UserID: "b"}
	seedOwner(t, d.WriteDB(), pA, "sk-a")
	seedOwner(t, d.WriteDB(), pB, "sk-b")

	seedAlbum(t, repo, pA, "A-1")
	seedAlbum(t, repo, pA, "A-2")
	seedAlbum(t, repo, pB, "B-1")

	itemsA, err := repo.ListByOwner(context.Background(), pA, 10, 0)
	r.NoError(err)
	r.Len(itemsA, 2)
	for _, it := range itemsA {
		r.Equal(pA, it.Owner)
	}
}

func TestRepoListByOwnerPagination(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := album.NewRepo(d.WriteDB(), d.ReadDB())
	p := owners.Principal{Hub: "h", UserID: "u"}
	seedOwner(t, d.WriteDB(), p, "sk")

	for i := range 5 {
		a := seedAlbum(t, repo, p, "x")
		r.NoError(repo.Rename(context.Background(), a.ID, "x", a.UpdatedAt.Add(time.Duration(i)*time.Second)))
	}

	page1, err := repo.ListByOwner(context.Background(), p, 2, 0)
	r.NoError(err)
	r.Len(page1, 2)

	page2, err := repo.ListByOwner(context.Background(), p, 2, 2)
	r.NoError(err)
	r.Len(page2, 2)

	page3, err := repo.ListByOwner(context.Background(), p, 2, 4)
	r.NoError(err)
	r.Len(page3, 1)

	// No duplicates across pages.
	seen := map[string]struct{}{}
	for _, it := range page1 {
		seen[it.ID] = struct{}{}
	}
	for _, it := range page2 {
		_, dup := seen[it.ID]
		r.False(dup)
		seen[it.ID] = struct{}{}
	}
}

func TestRepoListByOwnerDerivesCoverAndCount(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := album.NewRepo(d.WriteDB(), d.ReadDB())
	p := owners.Principal{Hub: "h", UserID: "u"}
	seedOwner(t, d.WriteDB(), p, "sk")
	a := seedAlbum(t, repo, p, "Trip")

	ready := uuid.NewString()
	seedMediaRow(t, d.WriteDB(), p, ready, "cs-r", "ready", 1)
	seedAlbumMedia(t, d.WriteDB(), a.ID, ready, time.Now().UTC())

	items, err := repo.ListByOwner(context.Background(), p, 10, 0)
	r.NoError(err)
	r.Len(items, 1)
	r.Equal(1, items[0].ItemCount)
	r.NotNil(items[0].Cover)
	r.Equal(ready, items[0].Cover.MediaID)
}
