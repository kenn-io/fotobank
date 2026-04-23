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
