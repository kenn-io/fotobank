package media_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/wesm/fotobank/internal/errs"
	"github.com/wesm/fotobank/internal/media"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/testutil"
)

func testOwner() owners.Principal {
	return owners.Principal{Hub: "h", UserID: "u"}
}

func baseMedia(id string, p owners.Principal) media.Media {
	return media.Media{
		ID:               id,
		Owner:            p,
		Type:             media.TypePhoto,
		MimeType:         "image/jpeg",
		Path:             "2024/a.jpg",
		OriginalFilename: "a.jpg",
		ImportedAt:       time.Now().UTC().Truncate(time.Second),
		Size:             100,
		Checksum:         "cs1",
		ThumbStatus:      "pending",
	}
}

func TestMediaInsertAndGet(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	d := testutil.OpenTestDB(t)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())

	p := testOwner()
	_, err := d.WriteDB().ExecContext(ctx,
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		p.Hub, p.UserID, "sk-a", time.Now().UTC(),
	)
	r.NoError(err)

	m := baseMedia(uuid.NewString(), p)
	r.NoError(repo.Insert(ctx, m))

	got, err := repo.GetByID(ctx, m.ID)
	r.NoError(err)
	r.Equal("2024/a.jpg", got.Path)
	r.Equal(int64(100), got.Size)
	r.Equal(media.TypePhoto, got.Type)
	r.Equal(p, got.Owner)
	r.Equal("cs1", got.Checksum)
}

func TestMediaInsertDuplicateChecksumReturnsAlreadyExists(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	d := testutil.OpenTestDB(t)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())

	p := testOwner()
	_, err := d.WriteDB().ExecContext(ctx,
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		p.Hub, p.UserID, "sk-b", time.Now().UTC(),
	)
	r.NoError(err)

	first := baseMedia(uuid.NewString(), p)
	r.NoError(repo.Insert(ctx, first))

	second := baseMedia(uuid.NewString(), p)
	second.Path = "2024/b.jpg"
	err = repo.Insert(ctx, second)
	r.ErrorIs(err, errs.ErrAlreadyExists)
}

func TestMediaInsertDuplicatePathReturnsAlreadyExists(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	d := testutil.OpenTestDB(t)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())

	p := testOwner()
	_, err := d.WriteDB().ExecContext(ctx,
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		p.Hub, p.UserID, "sk-c", time.Now().UTC(),
	)
	r.NoError(err)

	first := baseMedia(uuid.NewString(), p)
	r.NoError(repo.Insert(ctx, first))

	second := baseMedia(uuid.NewString(), p)
	second.Checksum = "cs2"
	err = repo.Insert(ctx, second)
	r.ErrorIs(err, errs.ErrAlreadyExists)
}

func TestMediaListFiltersByOwnerAndType(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	d := testutil.OpenTestDB(t)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())

	p := testOwner()
	_, err := d.WriteDB().ExecContext(ctx,
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		p.Hub, p.UserID, "sk-d", time.Now().UTC(),
	)
	r.NoError(err)

	photo1 := baseMedia(uuid.NewString(), p)
	photo1.Path = "2024/p1.jpg"
	photo1.Checksum = "cs-p1"
	r.NoError(repo.Insert(ctx, photo1))

	photo2 := baseMedia(uuid.NewString(), p)
	photo2.Path = "2024/p2.jpg"
	photo2.Checksum = "cs-p2"
	r.NoError(repo.Insert(ctx, photo2))

	video := baseMedia(uuid.NewString(), p)
	video.Type = media.TypeVideo
	video.MimeType = "video/mp4"
	video.Path = "2024/v.mp4"
	video.Checksum = "cs-v"
	r.NoError(repo.Insert(ctx, video))

	photoType := media.TypePhoto
	photos, err := repo.List(ctx, media.ListFilter{Owner: p, Type: &photoType})
	r.NoError(err)
	r.Len(photos, 2)

	all, err := repo.List(ctx, media.ListFilter{Owner: p})
	r.NoError(err)
	r.Len(all, 3)
}

func TestMediaGetByIDNotFoundReturnsErrNotFound(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	d := testutil.OpenTestDB(t)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())

	_, err := repo.GetByID(ctx, uuid.NewString())
	r.ErrorIs(err, errs.ErrNotFound)
}

func TestMediaListPaginationIsStableOnTies(t *testing.T) {
	// Rows with identical timestamp + imported_at must still paginate
	// deterministically so successive pages don't skip or duplicate.
	r := require.New(t)
	ctx := context.Background()
	d := testutil.OpenTestDB(t)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())

	p := testOwner()
	_, err := d.WriteDB().ExecContext(ctx,
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		p.Hub, p.UserID, "sk-page", time.Now().UTC(),
	)
	r.NoError(err)

	ts := time.Date(2024, 6, 15, 14, 30, 22, 0, time.UTC)
	imp := time.Date(2024, 6, 15, 15, 0, 0, 0, time.UTC)
	for i := range 5 {
		m := baseMedia(uuid.NewString(), p)
		m.Path = "tied/" + string(rune('a'+i)) + ".jpg"
		m.Checksum = "cs-tie-" + string(rune('a'+i))
		m.Timestamp = &ts
		m.ImportedAt = imp
		r.NoError(repo.Insert(ctx, m))
	}

	page1, err := repo.List(ctx, media.ListFilter{Owner: p, Limit: 2, Offset: 0})
	r.NoError(err)
	r.Len(page1, 2)
	page2, err := repo.List(ctx, media.ListFilter{Owner: p, Limit: 2, Offset: 2})
	r.NoError(err)
	r.Len(page2, 2)
	page3, err := repo.List(ctx, media.ListFilter{Owner: p, Limit: 2, Offset: 4})
	r.NoError(err)
	r.Len(page3, 1)

	seen := map[string]bool{}
	for _, pg := range [][]media.Media{page1, page2, page3} {
		for _, m := range pg {
			r.False(seen[m.ID], "row %s appeared in two pages", m.ID)
			seen[m.ID] = true
		}
	}
	r.Len(seen, 5)
}

func TestMediaGetByOwnerChecksum(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	d := testutil.OpenTestDB(t)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())

	p := testOwner()
	_, err := d.WriteDB().ExecContext(ctx,
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		p.Hub, p.UserID, "sk-e", time.Now().UTC(),
	)
	r.NoError(err)

	m := baseMedia(uuid.NewString(), p)
	r.NoError(repo.Insert(ctx, m))

	got, err := repo.GetByOwnerChecksum(ctx, p, "cs1")
	r.NoError(err)
	r.Equal(m.ID, got.ID)

	_, err = repo.GetByOwnerChecksum(ctx, p, "nope")
	r.ErrorIs(err, errs.ErrNotFound)
}
