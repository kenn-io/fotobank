package media_test

import (
	"context"
	"database/sql"
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

// seedOwner inserts a minimal owners row so media FK constraints resolve.
func seedOwner(t *testing.T, rw *sql.DB, p owners.Principal, sk string) {
	t.Helper()
	_, err := rw.ExecContext(context.Background(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		p.Hub, p.UserID, sk, time.Now().UTC())
	require.NoError(t, err)
}

// seedOneMedia inserts a minimally valid media row owned by p, returning
// its id. Fresh path + checksum each call so multi-row tests don't
// collide on the (owner, checksum) or (owner, path) unique indexes.
func seedOneMedia(t *testing.T, repo *media.Repo, p owners.Principal) string {
	t.Helper()
	id := uuid.NewString()
	cs := uuid.NewString()
	m := media.Media{
		ID: id, Owner: p, Type: media.TypePhoto, MimeType: "image/jpeg",
		Path: "2024/" + cs + ".jpg", OriginalFilename: "x.jpg",
		ImportedAt: time.Now().UTC().Truncate(time.Second),
		Size:       100, Checksum: cs, ThumbStatus: "pending",
	}
	require.NoError(t, repo.Insert(context.Background(), m))
	return id
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

func TestMediaGetByOwnerPath(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	d := testutil.OpenTestDB(t)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())

	p := testOwner()
	_, err := d.WriteDB().ExecContext(ctx,
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		p.Hub, p.UserID, "sk-path", time.Now().UTC(),
	)
	r.NoError(err)

	m := baseMedia(uuid.NewString(), p)
	r.NoError(repo.Insert(ctx, m))

	got, err := repo.GetByOwnerPath(ctx, p, "2024/a.jpg")
	r.NoError(err)
	r.Equal(m.ID, got.ID)

	_, err = repo.GetByOwnerPath(ctx, p, "2024/missing.jpg")
	r.ErrorIs(err, errs.ErrNotFound)
}

func TestMediaInsertUniqueViolationsDistinguishSentinels(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	d := testutil.OpenTestDB(t)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())

	p := testOwner()
	_, err := d.WriteDB().ExecContext(ctx,
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		p.Hub, p.UserID, "sk-kinds", time.Now().UTC(),
	)
	r.NoError(err)

	first := baseMedia(uuid.NewString(), p)
	r.NoError(repo.Insert(ctx, first))

	// Same checksum, different path → ErrDuplicateChecksum.
	dupChecksum := baseMedia(uuid.NewString(), p)
	dupChecksum.Path = "2024/other.jpg"
	err = repo.Insert(ctx, dupChecksum)
	r.ErrorIs(err, media.ErrDuplicateChecksum)
	r.ErrorIs(err, errs.ErrAlreadyExists)

	// Same path, different checksum → ErrDuplicatePath.
	dupPath := baseMedia(uuid.NewString(), p)
	dupPath.Checksum = "cs-other"
	err = repo.Insert(ctx, dupPath)
	r.ErrorIs(err, media.ErrDuplicatePath)
	r.ErrorIs(err, errs.ErrAlreadyExists)
}

func TestMediaListAllReturnsEveryRow(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	d := testutil.OpenTestDB(t)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())

	p := testOwner()
	_, err := d.WriteDB().ExecContext(ctx,
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		p.Hub, p.UserID, "sk-all", time.Now().UTC(),
	)
	r.NoError(err)

	for i := range 3 {
		m := baseMedia(uuid.NewString(), p)
		m.Path = "2024/all-" + string(rune('a'+i)) + ".jpg"
		m.Checksum = "cs-all-" + string(rune('a'+i))
		r.NoError(repo.Insert(ctx, m))
	}

	rows, err := repo.ListAll(ctx, p)
	r.NoError(err)
	r.Len(rows, 3)
}

func TestMediaDeleteRemovesRow(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	d := testutil.OpenTestDB(t)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())

	p := testOwner()
	_, err := d.WriteDB().ExecContext(ctx,
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		p.Hub, p.UserID, "sk-del", time.Now().UTC(),
	)
	r.NoError(err)

	m := baseMedia(uuid.NewString(), p)
	r.NoError(repo.Insert(ctx, m))

	r.NoError(repo.Delete(ctx, m.ID))

	_, err = repo.GetByID(ctx, m.ID)
	r.ErrorIs(err, errs.ErrNotFound)
}

func TestMediaDeleteUnknownIDReturnsErrNotFound(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	d := testutil.OpenTestDB(t)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())

	err := repo.Delete(ctx, uuid.NewString())
	r.ErrorIs(err, errs.ErrNotFound)
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

func TestMediaGetByIDsPreservesInputOrder(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	p := testOwner()
	seedOwner(t, d.WriteDB(), p, "sk")
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())

	a := seedOneMedia(t, repo, p)
	b := seedOneMedia(t, repo, p)
	c := seedOneMedia(t, repo, p)

	got, err := repo.GetByIDs(context.Background(), []string{c, a, b})
	r.NoError(err)
	r.Len(got, 3)
	r.Equal(c, got[0].ID)
	r.Equal(a, got[1].ID)
	r.Equal(b, got[2].ID)
}

func TestMediaGetByIDsSkipsMissing(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	p := testOwner()
	seedOwner(t, d.WriteDB(), p, "sk")
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	a := seedOneMedia(t, repo, p)
	got, err := repo.GetByIDs(context.Background(), []string{a, "00000000-0000-0000-0000-000000000000"})
	r.NoError(err)
	r.Len(got, 1)
	r.Equal(a, got[0].ID)
}

func TestMediaGetByIDsEmptyInputReturnsNil(t *testing.T) {
	d := testutil.OpenTestDB(t)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	got, err := repo.GetByIDs(context.Background(), nil)
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestRepoInsertGetByIDPreservesGPS(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	owner := owners.Principal{Hub: "h", UserID: "u"}
	_, err := d.WriteDB().ExecContext(context.Background(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		owner.Hub, owner.UserID, "sk", time.Now().UTC(),
	)
	r.NoError(err)

	lat, lon := 48.8566, 2.3522
	gps := time.Date(2024, 6, 15, 14, 30, 22, 0, time.UTC)
	id := uuid.NewString()
	r.NoError(repo.Insert(context.Background(), media.Media{
		ID: id, Owner: owner, Type: media.TypePhoto, MimeType: "image/jpeg",
		Path: "x.jpg", ImportedAt: time.Now().UTC(), Size: 1, Checksum: "c-" + id,
		Latitude: &lat, Longitude: &lon, GPSAt: &gps,
		LocationLabel: "Paris, Île-de-France, France",
		ThumbStatus:   "pending",
	}))

	got, err := repo.GetByID(context.Background(), id)
	r.NoError(err)
	r.NotNil(got.Latitude)
	r.NotNil(got.Longitude)
	r.NotNil(got.GPSAt)
	r.InDelta(48.8566, *got.Latitude, 1e-9)
	r.InDelta(2.3522, *got.Longitude, 1e-9)
	r.True(got.GPSAt.Equal(gps), "got %v", got.GPSAt)
	r.Equal("Paris, Île-de-France, France", got.LocationLabel)

	// Also exercise the mediaColumnsQualified projection used by
	// GetByIDs — this is a separate column list and a column-order
	// drift here would silently corrupt /api/v1/media DTOs.
	multi, err := repo.GetByIDs(context.Background(), []string{id})
	r.NoError(err)
	r.Len(multi, 1)
	r.NotNil(multi[0].Latitude)
	r.NotNil(multi[0].Longitude)
	r.InDelta(48.8566, *multi[0].Latitude, 1e-9)
	r.InDelta(2.3522, *multi[0].Longitude, 1e-9)
	r.NotNil(multi[0].GPSAt)
	r.True(multi[0].GPSAt.Equal(gps), "got %v", multi[0].GPSAt)
	r.Equal("Paris, Île-de-France, France", multi[0].LocationLabel)
}

func TestRepoInsertGetByIDPreservesAbsentGPS(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	owner := owners.Principal{Hub: "h", UserID: "u"}
	_, err := d.WriteDB().ExecContext(context.Background(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		owner.Hub, owner.UserID, "sk", time.Now().UTC(),
	)
	r.NoError(err)

	id := uuid.NewString()
	r.NoError(repo.Insert(context.Background(), media.Media{
		ID: id, Owner: owner, Type: media.TypePhoto, MimeType: "image/jpeg",
		Path: "x.jpg", ImportedAt: time.Now().UTC(), Size: 1, Checksum: "c-" + id,
		ThumbStatus: "pending",
		// no GPS fields
	}))

	got, err := repo.GetByID(context.Background(), id)
	r.NoError(err)
	r.Nil(got.Latitude)
	r.Nil(got.Longitude)
	r.Nil(got.GPSAt)
	r.Empty(got.LocationLabel)
}

func TestUpdateGPSRoundTrips(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	owner := owners.Principal{Hub: "h", UserID: "u"}
	_, err := d.WriteDB().ExecContext(context.Background(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		owner.Hub, owner.UserID, "sk", time.Now().UTC(),
	)
	r.NoError(err)

	id := uuid.NewString()
	r.NoError(repo.Insert(context.Background(), media.Media{
		ID: id, Owner: owner, Type: media.TypePhoto, MimeType: "image/jpeg",
		Path: "x.jpg", ImportedAt: time.Now().UTC(), Size: 1, Checksum: "c-" + id,
		ThumbStatus: "pending",
	}))

	lat, lon := 48.8566, 2.3522
	gps := time.Date(2024, 6, 15, 14, 30, 22, 0, time.UTC)
	r.NoError(repo.UpdateGPS(context.Background(), id, &lat, &lon, &gps, "Paris, France"))

	got, err := repo.GetByID(context.Background(), id)
	r.NoError(err)
	r.NotNil(got.Latitude)
	r.InDelta(48.8566, *got.Latitude, 1e-9)
	r.InDelta(2.3522, *got.Longitude, 1e-9)
	r.True(got.GPSAt.Equal(gps))
	r.Equal("Paris, France", got.LocationLabel)
}

func TestUpdateGPSClearsAllFieldsWhenNil(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	owner := owners.Principal{Hub: "h", UserID: "u"}
	_, err := d.WriteDB().ExecContext(context.Background(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		owner.Hub, owner.UserID, "sk", time.Now().UTC(),
	)
	r.NoError(err)

	lat, lon := 1.0, 2.0
	id := uuid.NewString()
	r.NoError(repo.Insert(context.Background(), media.Media{
		ID: id, Owner: owner, Type: media.TypePhoto, MimeType: "image/jpeg",
		Path: "x.jpg", ImportedAt: time.Now().UTC(), Size: 1, Checksum: "c-" + id,
		Latitude: &lat, Longitude: &lon, LocationLabel: "Old", ThumbStatus: "pending",
	}))

	r.NoError(repo.UpdateGPS(context.Background(), id, nil, nil, nil, ""))

	got, err := repo.GetByID(context.Background(), id)
	r.NoError(err)
	r.Nil(got.Latitude)
	r.Nil(got.Longitude)
	r.Nil(got.GPSAt)
	r.Empty(got.LocationLabel)
}

func TestUpdateGPSReturnsNotFoundForMissingRow(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())

	err := repo.UpdateGPS(context.Background(), "no-such-id", nil, nil, nil, "")
	r.ErrorIs(err, errs.ErrNotFound)
}

func TestListGPSBackfillCandidatesByMode(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	owner := owners.Principal{Hub: "h", UserID: "u"}
	_, err := d.WriteDB().ExecContext(context.Background(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		owner.Hub, owner.UserID, "sk", time.Now().UTC(),
	)
	r.NoError(err)

	mk := func(id string, t media.Type, lat, lon *float64) {
		r.NoError(repo.Insert(context.Background(), media.Media{
			ID: id, Owner: owner, Type: t, MimeType: "image/jpeg",
			Path: id + ".jpg", ImportedAt: time.Now().UTC(),
			Size: 1, Checksum: "c-" + id,
			Latitude: lat, Longitude: lon, ThumbStatus: "pending",
		}))
	}
	one := 1.0
	mk("photo-no-gps", media.TypePhoto, nil, nil)
	mk("photo-with-gps", media.TypePhoto, &one, &one)
	mk("video-with-gps", media.TypeVideo, &one, &one) // must be excluded
	// Partial-coord row (one coord set, one nil). Repo.Insert now
	// rejects this shape (atomic-pair invariant), so we bypass it with
	// direct SQL to simulate a row that arrived via a different path
	// (legacy migration, manual fix-up, etc). The point is to lock the
	// FillMissing/Relabel predicates against partial state that could
	// exist in the DB regardless of how it got there.
	_, err = d.WriteDB().ExecContext(context.Background(),
		`INSERT INTO media (
			id, owner_hub, owner_user_id, media_type, mime_type, path,
			imported_at, size, checksum,
			latitude, longitude,
			thumb_status, thumb_version
		) VALUES (?, ?, ?, 'photo', 'image/jpeg', ?, ?, 1, ?, ?, NULL, 'pending', 0)`,
		"photo-partial-coord", owner.Hub, owner.UserID,
		"photo-partial-coord.jpg", time.Now().UTC(),
		"c-photo-partial-coord", one,
	)
	r.NoError(err)

	ids := func(ms []media.Media) []string {
		out := make([]string, 0, len(ms))
		for _, m := range ms {
			out = append(out, m.ID)
		}
		return out
	}

	full, err := repo.ListGPSBackfillCandidates(context.Background(), owner, media.GPSBackfillModeFull, nil, "", 100)
	r.NoError(err)
	r.ElementsMatch([]string{"photo-no-gps", "photo-with-gps", "photo-partial-coord"}, ids(full))

	missing, err := repo.ListGPSBackfillCandidates(context.Background(), owner, media.GPSBackfillModeFillMissing, nil, "", 100)
	r.NoError(err)
	r.ElementsMatch([]string{"photo-no-gps"}, ids(missing))

	relabel, err := repo.ListGPSBackfillCandidates(context.Background(), owner, media.GPSBackfillModeRelabel, nil, "", 100)
	r.NoError(err)
	r.ElementsMatch([]string{"photo-with-gps"}, ids(relabel))
}

func TestListGPSBackfillCandidatesSinceFilter(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	owner := owners.Principal{Hub: "h", UserID: "u"}
	_, err := d.WriteDB().ExecContext(context.Background(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		owner.Hub, owner.UserID, "sk", time.Now().UTC(),
	)
	r.NoError(err)

	older := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	r.NoError(repo.Insert(context.Background(), media.Media{
		ID: "old-id", Owner: owner, Type: media.TypePhoto, MimeType: "image/jpeg",
		Path: "old.jpg", ImportedAt: older, Size: 1, Checksum: "c-old",
		ThumbStatus: "pending",
	}))
	r.NoError(repo.Insert(context.Background(), media.Media{
		ID: "new-id", Owner: owner, Type: media.TypePhoto, MimeType: "image/jpeg",
		Path: "new.jpg", ImportedAt: newer, Size: 1, Checksum: "c-new",
		ThumbStatus: "pending",
	}))

	cutoff := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	got, err := repo.ListGPSBackfillCandidates(context.Background(), owner, media.GPSBackfillModeFull, &cutoff, "", 100)
	r.NoError(err)
	r.Len(got, 1)
	r.Equal("new-id", got[0].ID)
}

// TestInsertRejectsPartialGPSPair locks the atomic-pair invariant: a
// row with exactly one of latitude / longitude set leaves the DB in a
// state that BOTH the FillMissing and Relabel candidate predicates
// exclude — un-fixable via the backfill CLI.
func TestInsertRejectsPartialGPSPair(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	owner := owners.Principal{Hub: "h", UserID: "u"}
	_, err := d.WriteDB().ExecContext(context.Background(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		owner.Hub, owner.UserID, "sk", time.Now().UTC(),
	)
	r.NoError(err)

	one := 1.0
	base := func(id string) media.Media {
		return media.Media{
			ID: id, Owner: owner, Type: media.TypePhoto, MimeType: "image/jpeg",
			Path: id + ".jpg", ImportedAt: time.Now().UTC(),
			Size: 1, Checksum: "c-" + id, ThumbStatus: "pending",
		}
	}

	m := base("only-lat")
	m.Latitude = &one
	r.ErrorIs(repo.Insert(context.Background(), m), errs.ErrInvalidArgument)

	m = base("only-lon")
	m.Longitude = &one
	r.ErrorIs(repo.Insert(context.Background(), m), errs.ErrInvalidArgument)
}

// TestUpdateGPSRejectsPartialPair mirrors TestInsertRejectsPartialGPSPair
// for the UpdateGPS path. The CLI backfill loop must never persist a
// half-coord row.
func TestUpdateGPSRejectsPartialPair(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	owner := owners.Principal{Hub: "h", UserID: "u"}
	_, err := d.WriteDB().ExecContext(context.Background(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		owner.Hub, owner.UserID, "sk", time.Now().UTC(),
	)
	r.NoError(err)

	id := uuid.NewString()
	r.NoError(repo.Insert(context.Background(), media.Media{
		ID: id, Owner: owner, Type: media.TypePhoto, MimeType: "image/jpeg",
		Path: "x.jpg", ImportedAt: time.Now().UTC(),
		Size: 1, Checksum: "c-" + id, ThumbStatus: "pending",
	}))

	one := 1.0
	r.ErrorIs(repo.UpdateGPS(context.Background(), id, &one, nil, nil, ""), errs.ErrInvalidArgument)
	r.ErrorIs(repo.UpdateGPS(context.Background(), id, nil, &one, nil, ""), errs.ErrInvalidArgument)
}

func TestRepoInsertGetByIDPreservesPairingFields(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	owner := testOwner()
	seedOwner(t, d.WriteDB(), owner, "sk-pair")

	primary := baseMedia(uuid.NewString(), owner)
	primary.Path = "2024/a.jpg"
	primary.Checksum = "cs-pri"
	primary.ImportSourcePath = "2024-Paris/IMG_1234.JPG"
	r.NoError(repo.Insert(context.Background(), primary))

	sidecarID := uuid.NewString()
	sidecar := baseMedia(sidecarID, owner)
	sidecar.Path = "2024/a.dng"
	sidecar.Checksum = "cs-sid"
	sidecar.ImportSourcePath = "2024-Paris/IMG_1234.DNG"
	sidecar.PairedWithID = &primary.ID
	r.NoError(repo.Insert(context.Background(), sidecar))

	gotPrimary, err := repo.GetByID(context.Background(), primary.ID)
	r.NoError(err)
	r.Equal("2024-Paris/IMG_1234.JPG", gotPrimary.ImportSourcePath)
	r.Nil(gotPrimary.PairedWithID)

	gotSidecar, err := repo.GetByID(context.Background(), sidecarID)
	r.NoError(err)
	r.Equal("2024-Paris/IMG_1234.DNG", gotSidecar.ImportSourcePath)
	r.NotNil(gotSidecar.PairedWithID)
	r.Equal(primary.ID, *gotSidecar.PairedWithID)
}

// TestListGPSBackfillCandidatesKeysetPagination drives the keyset
// cursor across multiple pages and asserts that (a) afterID="" returns
// the first lexicographic page and (b) passing the last seen ID
// returns only rows strictly greater. This is the contract the backfill
// CLI relies on to make progress under FillMissing (rows leave the
// candidate set as they're updated, so offset would skip rows on
// page 2; keyset is monotone in id).
func TestListGPSBackfillCandidatesKeysetPagination(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	owner := owners.Principal{Hub: "h", UserID: "u"}
	_, err := d.WriteDB().ExecContext(context.Background(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		owner.Hub, owner.UserID, "sk", time.Now().UTC(),
	)
	r.NoError(err)

	for _, id := range []string{"a", "b", "c", "d", "e"} {
		r.NoError(repo.Insert(context.Background(), media.Media{
			ID: id, Owner: owner, Type: media.TypePhoto, MimeType: "image/jpeg",
			Path: id + ".jpg", ImportedAt: time.Now().UTC(),
			Size: 1, Checksum: "c-" + id, ThumbStatus: "pending",
		}))
	}

	page1, err := repo.ListGPSBackfillCandidates(context.Background(), owner,
		media.GPSBackfillModeFillMissing, nil, "", 2)
	r.NoError(err)
	r.Len(page1, 2)
	r.Equal("a", page1[0].ID)
	r.Equal("b", page1[1].ID)

	page2, err := repo.ListGPSBackfillCandidates(context.Background(), owner,
		media.GPSBackfillModeFillMissing, nil, page1[len(page1)-1].ID, 2)
	r.NoError(err)
	r.Len(page2, 2)
	r.Equal("c", page2[0].ID)
	r.Equal("d", page2[1].ID)

	page3, err := repo.ListGPSBackfillCandidates(context.Background(), owner,
		media.GPSBackfillModeFillMissing, nil, page2[len(page2)-1].ID, 2)
	r.NoError(err)
	r.Len(page3, 1)
	r.Equal("e", page3[0].ID)

	page4, err := repo.ListGPSBackfillCandidates(context.Background(), owner,
		media.GPSBackfillModeFillMissing, nil, page3[len(page3)-1].ID, 2)
	r.NoError(err)
	r.Empty(page4)
}

func TestRepoListByOwnerDirectories(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	d := testutil.OpenTestDB(t)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	p := testOwner()
	other := owners.Principal{Hub: "h", UserID: "u2"}
	seedOwner(t, d.WriteDB(), p, "sk-a")
	seedOwner(t, d.WriteDB(), other, "sk-u2")

	mk := func(id, ownerHub, ownerUser, path, importPath, checksum string) media.Media {
		m := baseMedia(id, owners.Principal{Hub: ownerHub, UserID: ownerUser})
		m.Path = path
		m.Checksum = checksum
		m.ImportSourcePath = importPath
		return m
	}
	a := mk(uuid.NewString(), p.Hub, p.UserID, "2024/a.jpg", "trip-paris/IMG_1.JPG", "cs-a")
	b := mk(uuid.NewString(), p.Hub, p.UserID, "2024/b.dng", "trip-paris/IMG_1.DNG", "cs-b")
	c := mk(uuid.NewString(), p.Hub, p.UserID, "2024/c.jpg", "trip-rome/IMG_2.JPG", "cs-c")
	d2 := mk(uuid.NewString(), other.Hub, other.UserID, "2024/d.jpg", "trip-paris/IMG_3.JPG", "cs-d")
	for _, m := range []media.Media{a, b, c, d2} {
		r.NoError(repo.Insert(ctx, m))
	}

	// Caller asks for owner=p, dirs={"trip-paris"} — must return
	// a + b only; not c (different dir) and not d2 (different owner).
	rows, err := repo.ListByOwnerDirectories(ctx, p, []string{"trip-paris"})
	r.NoError(err)
	gotIDs := make([]string, 0, len(rows))
	for _, m := range rows {
		gotIDs = append(gotIDs, m.ID)
	}
	r.ElementsMatch([]string{a.ID, b.ID}, gotIDs)

	// Empty dirs returns nil.
	rows, err = repo.ListByOwnerDirectories(ctx, p, nil)
	r.NoError(err)
	r.Empty(rows)
}

func TestRepoUpdatePairedWithIDRoundTrips(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	d := testutil.OpenTestDB(t)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	p := testOwner()
	seedOwner(t, d.WriteDB(), p, "sk-a")

	primary := baseMedia(uuid.NewString(), p)
	primary.Path = "2024/a.jpg"
	primary.Checksum = "cs-pri"
	r.NoError(repo.Insert(ctx, primary))
	sidecar := baseMedia(uuid.NewString(), p)
	sidecar.Path = "2024/a.dng"
	sidecar.Checksum = "cs-sid"
	r.NoError(repo.Insert(ctx, sidecar))

	// Pair.
	r.NoError(repo.UpdatePairedWithID(ctx, sidecar.ID, &primary.ID))
	got, err := repo.GetByID(ctx, sidecar.ID)
	r.NoError(err)
	r.NotNil(got.PairedWithID)
	r.Equal(primary.ID, *got.PairedWithID)

	// Unpair (write nil).
	r.NoError(repo.UpdatePairedWithID(ctx, sidecar.ID, nil))
	got, err = repo.GetByID(ctx, sidecar.ID)
	r.NoError(err)
	r.Nil(got.PairedWithID)

	// Missing row returns ErrNotFound.
	err = repo.UpdatePairedWithID(ctx, "no-such-id", nil)
	r.ErrorIs(err, errs.ErrNotFound)
}

func TestRepoGetSidecarsReturnsSortedByOriginalFilename(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	d := testutil.OpenTestDB(t)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	p := testOwner()
	seedOwner(t, d.WriteDB(), p, "sk-a")

	primary := baseMedia(uuid.NewString(), p)
	primary.Path = "2024/a.jpg"
	primary.Checksum = "cs-pri"
	r.NoError(repo.Insert(ctx, primary))

	// Insert two sidecars with original_filenames in deliberately
	// reversed order to confirm the helper sorts ASC.
	for _, filename := range []string{"Z.dng", "A.dng"} {
		s := baseMedia(uuid.NewString(), p)
		s.Path = "2024/" + filename
		s.OriginalFilename = filename
		s.Checksum = "cs-" + filename
		s.PairedWithID = &primary.ID
		r.NoError(repo.Insert(ctx, s))
	}

	sidecars, err := repo.GetSidecars(ctx, primary.ID)
	r.NoError(err)
	r.Len(sidecars, 2)
	r.Equal("A.dng", sidecars[0].OriginalFilename)
	r.Equal("Z.dng", sidecars[1].OriginalFilename)

	// Empty result for a primary with no sidecars.
	loneID := uuid.NewString()
	lone := baseMedia(loneID, p)
	lone.Path = "2024/lone.jpg"
	lone.Checksum = "cs-lone"
	r.NoError(repo.Insert(ctx, lone))
	sidecars, err = repo.GetSidecars(ctx, loneID)
	r.NoError(err)
	r.Empty(sidecars)
}
