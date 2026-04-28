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

// TestRepoGetDetailByIDSkipsNewerPendingForOlderReady forces the
// thumb_status = 'ready' filter to do work: without it, ROW_NUMBER OVER
// (ORDER BY added_at DESC) would pick the newer pending row as cover.
// With the filter, the older ready row is selected.
func TestRepoGetDetailByIDSkipsNewerPendingForOlderReady(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := album.NewRepo(d.WriteDB(), d.ReadDB())
	p := owners.Principal{Hub: "h", UserID: "u"}
	seedOwner(t, d.WriteDB(), p, "sk")
	a := seedAlbum(t, repo, p, "Trip")

	older := uuid.NewString()
	newer := uuid.NewString()
	seedMediaRow(t, d.WriteDB(), p, older, "cs-o", "ready", 2)
	seedMediaRow(t, d.WriteDB(), p, newer, "cs-n", "pending", 0)
	base := time.Now().UTC().Truncate(time.Second)
	seedAlbumMedia(t, d.WriteDB(), a.ID, older, base)
	seedAlbumMedia(t, d.WriteDB(), a.ID, newer, base.Add(time.Second))

	got, err := repo.GetDetailByID(context.Background(), a.ID)
	r.NoError(err)
	r.Equal(2, got.ItemCount)
	r.NotNil(got.Cover)
	r.Equal(older, got.Cover.MediaID, "ready filter must exclude newer pending")
	r.Equal(2, got.Cover.ThumbVersion)
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

func TestRepoAddMediaHappyPath(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := album.NewRepo(d.WriteDB(), d.ReadDB())
	p := owners.Principal{Hub: "h", UserID: "u"}
	seedOwner(t, d.WriteDB(), p, "sk")
	a := seedAlbum(t, repo, p, "Trip")
	m1 := uuid.NewString()
	m2 := uuid.NewString()
	seedMediaRow(t, d.WriteDB(), p, m1, "cs1", "ready", 1)
	seedMediaRow(t, d.WriteDB(), p, m2, "cs2", "ready", 1)

	added, already, err := repo.AddMedia(context.Background(), a.ID, []string{m1, m2}, time.Now().UTC())
	r.NoError(err)
	r.Equal(2, added)
	r.Equal(0, already)
}

func TestRepoAddMediaIsIdempotent(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := album.NewRepo(d.WriteDB(), d.ReadDB())
	p := owners.Principal{Hub: "h", UserID: "u"}
	seedOwner(t, d.WriteDB(), p, "sk")
	a := seedAlbum(t, repo, p, "Trip")
	m := uuid.NewString()
	seedMediaRow(t, d.WriteDB(), p, m, "cs", "ready", 1)

	added1, already1, err := repo.AddMedia(context.Background(), a.ID, []string{m}, time.Now().UTC())
	r.NoError(err)
	r.Equal(1, added1)
	r.Equal(0, already1)

	added2, already2, err := repo.AddMedia(context.Background(), a.ID, []string{m}, time.Now().UTC())
	r.NoError(err)
	r.Equal(0, added2)
	r.Equal(1, already2)
}

func TestRepoAddMediaEmptyInputNoOp(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := album.NewRepo(d.WriteDB(), d.ReadDB())
	p := owners.Principal{Hub: "h", UserID: "u"}
	seedOwner(t, d.WriteDB(), p, "sk")
	a := seedAlbum(t, repo, p, "Empty")

	added, already, err := repo.AddMedia(context.Background(), a.ID, nil, time.Now().UTC())
	r.NoError(err)
	r.Equal(0, added)
	r.Equal(0, already)
}

func TestRepoAddMediaScalesToBatchCap(t *testing.T) {
	// Proves the INSERT handles the 500-row batch size that the service layer caps at.
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := album.NewRepo(d.WriteDB(), d.ReadDB())
	p := owners.Principal{Hub: "h", UserID: "u"}
	seedOwner(t, d.WriteDB(), p, "sk")
	a := seedAlbum(t, repo, p, "Big")

	ids := make([]string, 500)
	for i := range ids {
		id := uuid.NewString()
		ids[i] = id
		seedMediaRow(t, d.WriteDB(), p, id, "cs"+id, "ready", 1)
	}
	added, already, err := repo.AddMedia(context.Background(), a.ID, ids, time.Now().UTC())
	r.NoError(err)
	r.Equal(500, added)
	r.Equal(0, already)
}

func TestRepoRemoveMediaHappyPath(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := album.NewRepo(d.WriteDB(), d.ReadDB())
	p := owners.Principal{Hub: "h", UserID: "u"}
	seedOwner(t, d.WriteDB(), p, "sk")
	a := seedAlbum(t, repo, p, "Trip")
	m := uuid.NewString()
	seedMediaRow(t, d.WriteDB(), p, m, "cs", "ready", 1)
	seedAlbumMedia(t, d.WriteDB(), a.ID, m, time.Now().UTC())

	r.NoError(repo.RemoveMedia(context.Background(), a.ID, m))
}

func TestRepoRemoveMediaNotInAlbum(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := album.NewRepo(d.WriteDB(), d.ReadDB())
	p := owners.Principal{Hub: "h", UserID: "u"}
	seedOwner(t, d.WriteDB(), p, "sk")
	a := seedAlbum(t, repo, p, "Trip")

	err := repo.RemoveMedia(context.Background(), a.ID, "nonesuch")
	r.ErrorIs(err, errs.ErrNotFound)
}

func TestRepoDeleteAlbumCascadesAlbumMedia(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := album.NewRepo(d.WriteDB(), d.ReadDB())
	p := owners.Principal{Hub: "h", UserID: "u"}
	seedOwner(t, d.WriteDB(), p, "sk")
	a := seedAlbum(t, repo, p, "Trip")
	m1 := uuid.NewString()
	m2 := uuid.NewString()
	seedMediaRow(t, d.WriteDB(), p, m1, "cs1", "ready", 1)
	seedMediaRow(t, d.WriteDB(), p, m2, "cs2", "ready", 1)
	seedAlbumMedia(t, d.WriteDB(), a.ID, m1, time.Now().UTC())
	seedAlbumMedia(t, d.WriteDB(), a.ID, m2, time.Now().UTC())

	r.NoError(repo.Delete(context.Background(), a.ID))

	var n int
	r.NoError(d.ReadDB().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM album_media WHERE album_id = ?`, a.ID).Scan(&n))
	r.Equal(0, n)
}

func TestRepoDeleteMediaCascadesAlbumMedia(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := album.NewRepo(d.WriteDB(), d.ReadDB())
	p := owners.Principal{Hub: "h", UserID: "u"}
	seedOwner(t, d.WriteDB(), p, "sk")
	a := seedAlbum(t, repo, p, "Trip")
	m := uuid.NewString()
	seedMediaRow(t, d.WriteDB(), p, m, "cs", "ready", 1)
	seedAlbumMedia(t, d.WriteDB(), a.ID, m, time.Now().UTC())

	_, err := d.WriteDB().ExecContext(context.Background(), `DELETE FROM media WHERE id = ?`, m)
	r.NoError(err)

	var n int
	r.NoError(d.ReadDB().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM album_media WHERE album_id = ?`, a.ID).Scan(&n))
	r.Equal(0, n, "deleting media should cascade to album_media via FK")
}

func TestRepoListMediaSortModes(t *testing.T) {
	d := testutil.OpenTestDB(t)
	repo := album.NewRepo(d.WriteDB(), d.ReadDB())
	p := owners.Principal{Hub: "h", UserID: "u"}
	seedOwner(t, d.WriteDB(), p, "sk")
	a := seedAlbum(t, repo, p, "Trip")

	first := uuid.NewString()
	second := uuid.NewString()
	third := uuid.NewString()
	seedMediaRow(t, d.WriteDB(), p, first, "cs1", "ready", 1)
	seedMediaRow(t, d.WriteDB(), p, second, "cs2", "ready", 1)
	seedMediaRow(t, d.WriteDB(), p, third, "cs3", "ready", 1)

	base := time.Now().UTC().Truncate(time.Second)
	seedAlbumMedia(t, d.WriteDB(), a.ID, first, base) // added earliest
	seedAlbumMedia(t, d.WriteDB(), a.ID, second, base.Add(time.Second))
	seedAlbumMedia(t, d.WriteDB(), a.ID, third, base.Add(2*time.Second)) // added latest

	cases := []struct {
		name    string
		filter  album.AlbumMediaFilter
		wantIDs []string
	}{
		{"added-desc (default)", album.AlbumMediaFilter{SortBy: "added"}, []string{third, second, first}},
		{"added-asc", album.AlbumMediaFilter{SortBy: "added", SortAsc: true}, []string{first, second, third}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := repo.ListMedia(context.Background(), a.ID, tc.filter)
			require.NoError(t, err)
			ids := make([]string, 0, len(got))
			for _, m := range got {
				ids = append(ids, m.ID)
			}
			require.Equal(t, tc.wantIDs, ids)
		})
	}
}

func TestRepoListMediaPagination(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := album.NewRepo(d.WriteDB(), d.ReadDB())
	p := owners.Principal{Hub: "h", UserID: "u"}
	seedOwner(t, d.WriteDB(), p, "sk")
	a := seedAlbum(t, repo, p, "Trip")

	ids := make([]string, 3)
	base := time.Now().UTC().Truncate(time.Second)
	for i := range ids {
		ids[i] = uuid.NewString()
		seedMediaRow(t, d.WriteDB(), p, ids[i], "cs"+ids[i], "ready", 1)
		seedAlbumMedia(t, d.WriteDB(), a.ID, ids[i], base.Add(time.Duration(i)*time.Second))
	}

	got, err := repo.ListMedia(context.Background(), a.ID,
		album.AlbumMediaFilter{SortBy: "added", Limit: 2, Offset: 1})
	r.NoError(err)
	r.Len(got, 2)
	// added-desc → [ids[2], ids[1], ids[0]], offset=1 → [ids[1], ids[0]]
	r.Equal(ids[1], got[0].ID)
	r.Equal(ids[0], got[1].ID)
}

func TestRepoDeleteTxCommitRemovesAlbum(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := album.NewRepo(d.WriteDB(), d.ReadDB())
	owner := owners.Principal{Hub: "h", UserID: "o"}
	seedOwner(t, d.WriteDB(), owner, "sk")
	a := album.Album{
		ID: uuid.NewString(), Owner: owner,
		Name: "t", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	r.NoError(repo.Insert(context.Background(), a))

	tx, err := d.WriteDB().BeginTx(context.Background(), nil)
	r.NoError(err)
	r.NoError(repo.DeleteTx(context.Background(), tx, a.ID))
	r.NoError(tx.Commit())

	_, err = repo.GetByID(context.Background(), a.ID)
	r.ErrorIs(err, errs.ErrNotFound)
}

func TestRepoDeleteTxRollbackLeavesAlbum(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := album.NewRepo(d.WriteDB(), d.ReadDB())
	owner := owners.Principal{Hub: "h", UserID: "o"}
	seedOwner(t, d.WriteDB(), owner, "sk")
	a := album.Album{
		ID: uuid.NewString(), Owner: owner,
		Name: "t", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	r.NoError(repo.Insert(context.Background(), a))

	tx, err := d.WriteDB().BeginTx(context.Background(), nil)
	r.NoError(err)
	r.NoError(repo.DeleteTx(context.Background(), tx, a.ID))
	r.NoError(tx.Rollback())

	got, err := repo.GetByID(context.Background(), a.ID)
	r.NoError(err)
	r.Equal(a.ID, got.ID)
}

func TestRepoDeleteTxNotFound(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := album.NewRepo(d.WriteDB(), d.ReadDB())
	tx, err := d.WriteDB().BeginTx(context.Background(), nil)
	r.NoError(err)
	defer tx.Rollback()
	err = repo.DeleteTx(context.Background(), tx, uuid.NewString())
	r.ErrorIs(err, errs.ErrNotFound)
}

func TestAlbumGetDetailsByIDsPreservesOrder(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	p := owners.Principal{Hub: "h", UserID: "u"}
	seedOwner(t, d.WriteDB(), p, "sk")
	repo := album.NewRepo(d.WriteDB(), d.ReadDB())

	a := seedAlbum(t, repo, p, "A")
	b := seedAlbum(t, repo, p, "B")
	c := seedAlbum(t, repo, p, "C")

	got, err := repo.GetDetailsByIDs(context.Background(), []string{b.ID, c.ID, a.ID})
	r.NoError(err)
	r.Len(got, 3)
	r.Equal(b.ID, got[0].ID)
	r.Equal(c.ID, got[1].ID)
	r.Equal(a.ID, got[2].ID)
}

func TestAlbumGetDetailsByIDsSkipsMissing(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	p := owners.Principal{Hub: "h", UserID: "u"}
	seedOwner(t, d.WriteDB(), p, "sk")
	repo := album.NewRepo(d.WriteDB(), d.ReadDB())
	a := seedAlbum(t, repo, p, "A")
	got, err := repo.GetDetailsByIDs(context.Background(), []string{a.ID, "00000000-0000-0000-0000-000000000000"})
	r.NoError(err)
	r.Len(got, 1)
	r.Equal(a.ID, got[0].ID)
}

func TestAlbumGetDetailsByIDsEmpty(t *testing.T) {
	d := testutil.OpenTestDB(t)
	repo := album.NewRepo(d.WriteDB(), d.ReadDB())
	got, err := repo.GetDetailsByIDs(context.Background(), nil)
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestRepoListMediaImportedSort(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := album.NewRepo(d.WriteDB(), d.ReadDB())
	p := owners.Principal{Hub: "h", UserID: "u"}
	seedOwner(t, d.WriteDB(), p, "sk")
	a := seedAlbum(t, repo, p, "Trip")

	// seedMediaRow hardcodes imported_at = time.Now() at call time, so
	// we order the calls to match our expectation.
	early := uuid.NewString()
	late := uuid.NewString()
	seedMediaRow(t, d.WriteDB(), p, early, "cs-e", "ready", 1)
	time.Sleep(10 * time.Millisecond)
	seedMediaRow(t, d.WriteDB(), p, late, "cs-l", "ready", 1)
	seedAlbumMedia(t, d.WriteDB(), a.ID, early, time.Now().UTC())
	seedAlbumMedia(t, d.WriteDB(), a.ID, late, time.Now().UTC())

	got, err := repo.ListMedia(context.Background(), a.ID,
		album.AlbumMediaFilter{SortBy: "imported"})
	r.NoError(err)
	r.Len(got, 2)
	r.Equal(late, got[0].ID, "imported-desc → latest first")
	r.Equal(early, got[1].ID)
}

// TestRepoListMediaPreservesGPS exercises albumMediaMediaSelect's GPS
// columns. The four-way projection sync (mediaSelect, mediaColumnsQualified,
// mediaInsert, albumMediaMediaSelect) means a column-order drift here
// would corrupt /api/v1/albums/.../media DTOs without breaking the
// other three projections.
func TestRepoListMediaPreservesGPS(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := album.NewRepo(d.WriteDB(), d.ReadDB())
	p := owners.Principal{Hub: "h", UserID: "u"}
	seedOwner(t, d.WriteDB(), p, "sk")
	a := seedAlbum(t, repo, p, "Trip")

	id := uuid.NewString()
	lat, lon := 48.8566, 2.3522
	gps := time.Date(2024, 6, 15, 14, 30, 22, 0, time.UTC)
	_, err := d.WriteDB().ExecContext(context.Background(), `
INSERT INTO media (
    id, owner_hub, owner_user_id, media_type, mime_type, path, original_filename,
    imported_at, timestamp, size, checksum,
    make, model, focal_length, shutter, width, height, iso, aperture,
    duration_ms,
    latitude, longitude, gps_at, location_label,
    thumb_status, thumb_version, thumb_updated_at
) VALUES (?, ?, ?, 'photo', 'image/jpeg', ?, NULL, ?, NULL, 0, ?,
          NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL,
          ?, ?, ?, ?,
          'ready', 1, NULL)`,
		id, p.Hub, p.UserID, "p/"+id, time.Now().UTC(), "cs-"+id,
		lat, lon, gps, "Paris, Île-de-France, France",
	)
	r.NoError(err)
	seedAlbumMedia(t, d.WriteDB(), a.ID, id, time.Now().UTC())

	got, err := repo.ListMedia(context.Background(), a.ID,
		album.AlbumMediaFilter{SortBy: "added"})
	r.NoError(err)
	r.Len(got, 1)
	r.NotNil(got[0].Latitude)
	r.NotNil(got[0].Longitude)
	r.InDelta(48.8566, *got[0].Latitude, 1e-9)
	r.InDelta(2.3522, *got[0].Longitude, 1e-9)
	r.NotNil(got[0].GPSAt)
	r.True(got[0].GPSAt.Equal(gps), "got %v", got[0].GPSAt)
	r.Equal("Paris, Île-de-France, France", got[0].LocationLabel)
}
