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

	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/media"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/service"
	"go.kenn.io/fotobank/internal/storage"
	"go.kenn.io/fotobank/internal/testutil"
)

// mediaServiceFixture bundles the collaborators the MediaService tests
// need: the service under test, the seeded owner, the underlying store
// and repo for arranging state, the on-disk NAS root, and the writable
// DB so tests that need a second owner can seed one.
type mediaServiceFixture struct {
	svc   *service.MediaService
	owner owners.Principal
	store *storage.NASOnly
	repo  *media.Repo
	rw    *sql.DB
	root  string
}

func newMediaServiceTest(t *testing.T) mediaServiceFixture {
	t.Helper()
	d := testutil.OpenTestDB(t)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	p := owners.Principal{Hub: "h", UserID: "u"}
	_, err := d.WriteDB().ExecContext(context.Background(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		p.Hub, p.UserID, "sk", time.Now().UTC(),
	)
	require.NoError(t, err)
	root := t.TempDir()
	store := storage.NewNASOnly(root, map[owners.Principal]string{p: "sk"})
	svc := service.NewMediaService(repo, store)
	return mediaServiceFixture{
		svc: svc, owner: p, store: store, repo: repo, rw: d.WriteDB(), root: root,
	}
}

// insertTestMedia inserts a minimal media row for the given principal
// and returns it.
func insertTestMedia(t *testing.T, repo *media.Repo, p owners.Principal, path, checksum string) media.Media {
	t.Helper()
	m := media.Media{
		ID:               uuid.NewString(),
		Owner:            p,
		Type:             media.TypePhoto,
		MimeType:         "image/jpeg",
		Path:             path,
		OriginalFilename: "x.jpg",
		ImportedAt:       time.Now().UTC().Truncate(time.Second),
		Size:             100,
		Checksum:         checksum,
		ThumbStatus:      "pending",
	}
	require.NoError(t, repo.Insert(context.Background(), m))
	return m
}

// insertTestMediaGPS inserts a primary with the given GPS coordinates.
// Use insertTestMedia for non-GPS rows.
func insertTestMediaGPS(t *testing.T, repo *media.Repo, p owners.Principal, path, checksum string, lat, lon float64) media.Media {
	t.Helper()
	m := media.Media{
		ID:               uuid.NewString(),
		Owner:            p,
		Type:             media.TypePhoto,
		MimeType:         "image/jpeg",
		Path:             path,
		OriginalFilename: "x.jpg",
		ImportedAt:       time.Now().UTC().Truncate(time.Second),
		Size:             100,
		Checksum:         checksum,
		ThumbStatus:      "pending",
		Latitude:         &lat,
		Longitude:        &lon,
	}
	require.NoError(t, repo.Insert(context.Background(), m))
	return m
}

func TestMediaServiceGetReturnsCallerRow(t *testing.T) {
	r := require.New(t)
	fx := newMediaServiceTest(t)
	ctx := context.Background()

	m := insertTestMedia(t, fx.repo, fx.owner, "2024/a.jpg", "cs1")

	got, err := fx.svc.Get(ctx, m.ID, fx.owner)
	r.NoError(err)
	r.Equal(m.ID, got.ID)
	r.Equal(fx.owner, got.Owner)

	// A non-owning caller must see ErrNotFound rather than a permission
	// error, so the handler surface cannot be used to probe for the
	// existence of other owners' media.
	intruder := owners.Principal{Hub: "h", UserID: "intruder"}
	_, err = fx.svc.Get(ctx, m.ID, intruder)
	r.ErrorIs(err, errs.ErrNotFound)
}

func TestMediaServiceListFiltersByCaller(t *testing.T) {
	r := require.New(t)
	fx := newMediaServiceTest(t)
	ctx := context.Background()

	// Seed a second owner B alongside the fixture's owner A.
	ownerB := owners.Principal{Hub: "h", UserID: "b"}
	_, err := fx.rw.ExecContext(ctx,
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		ownerB.Hub, ownerB.UserID, "sk-b", time.Now().UTC(),
	)
	r.NoError(err)

	mA := insertTestMedia(t, fx.repo, fx.owner, "2024/a.jpg", "cs-a")
	_ = insertTestMedia(t, fx.repo, ownerB, "2024/b.jpg", "cs-b")

	// Caller A asks for owner B's rows; the service must clamp the
	// filter's Owner to the caller so only A's rows come back.
	rows, err := fx.svc.List(ctx, media.ListFilter{Owner: ownerB}, fx.owner)
	r.NoError(err)
	r.Len(rows, 1)
	r.Equal(mA.ID, rows[0].ID)
	r.Equal(fx.owner, rows[0].Owner)
}

func TestMediaServiceOpenOriginalFullReader(t *testing.T) {
	r := require.New(t)
	fx := newMediaServiceTest(t)
	ctx := context.Background()

	payload := []byte("photo-bytes")
	_, err := fx.store.Write(ctx, fx.owner, "2024/a.jpg", bytes.NewReader(payload))
	r.NoError(err)
	m := insertTestMedia(t, fx.repo, fx.owner, "2024/a.jpg", "cs1")

	rc, got, err := fx.svc.OpenOriginal(ctx, m.ID, fx.owner, 0, -1)
	r.NoError(err)
	defer rc.Close()
	r.Equal(m.ID, got.ID)
	buf, err := io.ReadAll(rc)
	r.NoError(err)
	r.Equal(payload, buf)
}

func TestMediaServiceOpenOriginalRange(t *testing.T) {
	r := require.New(t)
	fx := newMediaServiceTest(t)
	ctx := context.Background()

	payload := []byte("0123456789")
	_, err := fx.store.Write(ctx, fx.owner, "2024/r.jpg", bytes.NewReader(payload))
	r.NoError(err)
	m := insertTestMedia(t, fx.repo, fx.owner, "2024/r.jpg", "cs-r")

	rc, _, err := fx.svc.OpenOriginal(ctx, m.ID, fx.owner, 2, 3)
	r.NoError(err)
	defer rc.Close()
	buf, err := io.ReadAll(rc)
	r.NoError(err)
	r.Equal("234", string(buf))
}

func TestMediaServiceOpenOriginalRejectsNonOwner(t *testing.T) {
	r := require.New(t)
	fx := newMediaServiceTest(t)
	ctx := context.Background()

	payload := []byte("secret")
	_, err := fx.store.Write(ctx, fx.owner, "2024/s.jpg", bytes.NewReader(payload))
	r.NoError(err)
	m := insertTestMedia(t, fx.repo, fx.owner, "2024/s.jpg", "cs-s")

	intruder := owners.Principal{Hub: "h", UserID: "intruder"}
	rc, _, err := fx.svc.OpenOriginal(ctx, m.ID, intruder, 0, -1)
	r.ErrorIs(err, errs.ErrNotFound)
	r.Nil(rc)
}

func TestMediaServiceUpdateGPSRoundTrips(t *testing.T) {
	r := require.New(t)
	fx := newMediaServiceTest(t)
	ctx := context.Background()

	m := insertTestMedia(t, fx.repo, fx.owner, "2024/g.jpg", "cs-g")

	lat, lon := 1.0, 2.0
	gpsAt := time.Date(2024, 6, 15, 14, 30, 22, 0, time.UTC)
	r.NoError(fx.svc.UpdateGPS(ctx, fx.owner, m.ID, &lat, &lon, &gpsAt, "Foo, Bar"))

	got, err := fx.repo.GetByID(ctx, m.ID)
	r.NoError(err)
	r.NotNil(got.Latitude)
	r.NotNil(got.Longitude)
	r.InDelta(1.0, *got.Latitude, 1e-9)
	r.InDelta(2.0, *got.Longitude, 1e-9)
	r.NotNil(got.GPSAt, "service must not drop the gpsAt arg")
	r.True(got.GPSAt.Equal(gpsAt), "got %v", got.GPSAt)
	r.Equal("Foo, Bar", got.LocationLabel)
}

func TestMediaServiceUpdateGPSCallerMismatchReturnsNotFound(t *testing.T) {
	r := require.New(t)
	fx := newMediaServiceTest(t)
	ctx := context.Background()

	m := insertTestMedia(t, fx.repo, fx.owner, "2024/m.jpg", "cs-m")

	// Intruder need not exist in the owners table — the service rejects
	// on the in-memory Owner equality check inside Get before any
	// foreign-key path runs.
	intruder := owners.Principal{Hub: "h", UserID: "intruder"}
	lat, lon := 1.0, 2.0
	err := fx.svc.UpdateGPS(ctx, intruder, m.ID, &lat, &lon, nil, "Foo")
	r.ErrorIs(err, errs.ErrNotFound)

	// And the row must be untouched.
	got, err := fx.repo.GetByID(ctx, m.ID)
	r.NoError(err)
	r.Nil(got.Latitude)
	r.Nil(got.Longitude)
	r.Empty(got.LocationLabel)
}

func TestMediaServiceUpdateGPSMissingRowReturnsNotFound(t *testing.T) {
	r := require.New(t)
	fx := newMediaServiceTest(t)
	ctx := context.Background()

	err := fx.svc.UpdateGPS(ctx, fx.owner, "no-such-id", nil, nil, nil, "")
	r.ErrorIs(err, errs.ErrNotFound)
}

// TestMediaServiceUpdateGPSRefreshesFTS pins the J2 wiring contract on
// the service-side write path: when UpdateGPS commits, the media_fts
// row's location_label column reflects the just-written value. The
// label is part of the FTS corpus, so without this refresh lexical
// search by city / region would lag the row's columnar value.
func TestMediaServiceUpdateGPSRefreshesFTS(t *testing.T) {
	r := require.New(t)
	fx := newMediaServiceTest(t)
	ctx := context.Background()

	m := insertTestMedia(t, fx.repo, fx.owner, "2024/loc.jpg", "cs-loc")

	lat, lon := 48.8566, 2.3522
	gpsAt := time.Date(2024, 6, 15, 14, 30, 22, 0, time.UTC)
	r.NoError(fx.svc.UpdateGPS(ctx, fx.owner, m.ID, &lat, &lon, &gpsAt, "Paris, France"))

	var loc string
	r.NoError(fx.rw.QueryRowContext(ctx,
		`SELECT location_label FROM media_fts WHERE media_id = ?`, m.ID).Scan(&loc))
	r.Equal("Paris, France", loc)

	// Clearing the row clears the FTS row's location column too, so
	// the corpus stays in lock-step with the columnar value.
	r.NoError(fx.svc.UpdateGPS(ctx, fx.owner, m.ID, nil, nil, nil, ""))
	r.NoError(fx.rw.QueryRowContext(ctx,
		`SELECT location_label FROM media_fts WHERE media_id = ?`, m.ID).Scan(&loc))
	r.Empty(loc)
}

func TestMediaServiceListHidesSidecarsByDefault(t *testing.T) {
	r := require.New(t)
	fx := newMediaServiceTest(t)
	ctx := context.Background()

	primary := insertTestMedia(t, fx.repo, fx.owner, "2024/a.jpg", "cs-pri")
	sidecar := insertTestMedia(t, fx.repo, fx.owner, "2024/a.dng", "cs-sid")
	r.NoError(fx.repo.UpdatePairedWithID(ctx, sidecar.ID, &primary.ID))

	rows, err := fx.svc.List(ctx, media.ListFilter{}, fx.owner)
	r.NoError(err)
	r.Len(rows, 1)
	r.Equal(primary.ID, rows[0].ID)
}

func TestMediaServiceListClampsIncludeSidecars(t *testing.T) {
	r := require.New(t)
	fx := newMediaServiceTest(t)
	ctx := context.Background()

	primary := insertTestMedia(t, fx.repo, fx.owner, "2024/a.jpg", "cs-pri")
	sidecar := insertTestMedia(t, fx.repo, fx.owner, "2024/a.dng", "cs-sid")
	r.NoError(fx.repo.UpdatePairedWithID(ctx, sidecar.ID, &primary.ID))

	// Caller asks for true; service must clamp it back to false.
	rows, err := fx.svc.List(ctx, media.ListFilter{IncludeSidecars: true}, fx.owner)
	r.NoError(err)
	r.Len(rows, 1)
	r.Equal(primary.ID, rows[0].ID)
}

// --- hidden-aware service tests ---

func markHidden(t *testing.T, rw *sql.DB, id string) {
	t.Helper()
	_, err := rw.ExecContext(context.Background(),
		`UPDATE media SET hidden_at = ? WHERE id = ?`,
		time.Now().UTC(), id,
	)
	require.NoError(t, err)
}

func TestMediaServiceGetExcludesHiddenByDefault(t *testing.T) {
	r := require.New(t)
	fx := newMediaServiceTest(t)
	ctx := context.Background()

	m := insertTestMedia(t, fx.repo, fx.owner, "2024/h.jpg", "cs-hid")
	markHidden(t, fx.rw, m.ID)

	_, err := fx.svc.Get(ctx, m.ID, fx.owner)
	r.ErrorIs(err, errs.ErrNotFound)
}

func TestMediaServiceGetIncludesHiddenWhenFlagSet(t *testing.T) {
	r := require.New(t)
	fx := newMediaServiceTest(t)
	ctx := context.Background()

	m := insertTestMedia(t, fx.repo, fx.owner, "2024/h.jpg", "cs-hid")
	markHidden(t, fx.rw, m.ID)

	got, err := fx.svc.Get(ctx, m.ID, fx.owner, true)
	r.NoError(err)
	r.Equal(m.ID, got.ID)
	r.NotNil(got.HiddenAt)
}

func TestMediaServiceListClampsIncludeHidden(t *testing.T) {
	r := require.New(t)
	fx := newMediaServiceTest(t)
	ctx := context.Background()

	visible := insertTestMedia(t, fx.repo, fx.owner, "2024/v.jpg", "cs-v")
	hidden := insertTestMedia(t, fx.repo, fx.owner, "2024/h.jpg", "cs-h")
	markHidden(t, fx.rw, hidden.ID)

	// Even if the caller tries to set IncludeHidden=true, List must clamp to false.
	rows, err := fx.svc.List(ctx, media.ListFilter{IncludeHidden: true}, fx.owner)
	r.NoError(err)
	r.Len(rows, 1)
	r.Equal(visible.ID, rows[0].ID)
}

func TestMediaServiceOpenOriginalHiddenReturnsNotFoundByDefault(t *testing.T) {
	r := require.New(t)
	fx := newMediaServiceTest(t)
	ctx := context.Background()

	payload := []byte("hidden-bytes")
	_, err := fx.store.Write(ctx, fx.owner, "2024/h.jpg", bytes.NewReader(payload))
	r.NoError(err)
	m := insertTestMedia(t, fx.repo, fx.owner, "2024/h.jpg", "cs-hid")
	markHidden(t, fx.rw, m.ID)

	rc, _, err := fx.svc.OpenOriginal(ctx, m.ID, fx.owner, 0, -1)
	r.ErrorIs(err, errs.ErrNotFound)
	r.Nil(rc)
}

func TestMediaServiceOpenOriginalHiddenSucceedsWithFlag(t *testing.T) {
	r := require.New(t)
	fx := newMediaServiceTest(t)
	ctx := context.Background()

	payload := []byte("hidden-bytes")
	_, err := fx.store.Write(ctx, fx.owner, "2024/h.jpg", bytes.NewReader(payload))
	r.NoError(err)
	m := insertTestMedia(t, fx.repo, fx.owner, "2024/h.jpg", "cs-hid")
	markHidden(t, fx.rw, m.ID)

	rc, got, err := fx.svc.OpenOriginal(ctx, m.ID, fx.owner, 0, -1, true)
	r.NoError(err)
	defer rc.Close()
	r.Equal(m.ID, got.ID)
	buf, err := io.ReadAll(rc)
	r.NoError(err)
	r.Equal(payload, buf)
}

func TestMediaServiceHideRejectsInputSidecarID(t *testing.T) {
	r := require.New(t)
	fx := newMediaServiceTest(t)
	ctx := context.Background()

	primary := insertTestMedia(t, fx.repo, fx.owner, "2024/p.jpg", "cs-pri")
	sidecar := insertTestMedia(t, fx.repo, fx.owner, "2024/p.dng", "cs-sid")
	r.NoError(fx.repo.UpdatePairedWithID(ctx, sidecar.ID, &primary.ID))

	result, err := fx.svc.Hide(ctx, fx.owner, []string{sidecar.ID})
	r.NoError(err, "Hide must return partial result, not an error")
	r.Empty(result.Succeeded)
	r.Len(result.Failed, 1)
	r.Equal(sidecar.ID, result.Failed[0].ID)
	r.Equal("invalid_sidecar", result.Failed[0].Code)
}

func TestMediaServiceHideCascadesToSidecars(t *testing.T) {
	r := require.New(t)
	fx := newMediaServiceTest(t)
	ctx := context.Background()

	primary := insertTestMedia(t, fx.repo, fx.owner, "2024/p.jpg", "cs-pri2")
	sidecar := insertTestMedia(t, fx.repo, fx.owner, "2024/p.dng", "cs-sid2")
	r.NoError(fx.repo.UpdatePairedWithID(ctx, sidecar.ID, &primary.ID))

	result, err := fx.svc.Hide(ctx, fx.owner, []string{primary.ID})
	r.NoError(err)
	r.Len(result.Succeeded, 1)
	r.Equal(primary.ID, result.Succeeded[0])
	r.Empty(result.Failed)

	// Both primary and sidecar must now be hidden.
	got, err := fx.repo.GetByID(ctx, primary.ID)
	r.NoError(err)
	r.NotNil(got.HiddenAt, "primary must be hidden")
	gotSidecar, err := fx.repo.GetByID(ctx, sidecar.ID)
	r.NoError(err)
	r.NotNil(gotSidecar.HiddenAt, "sidecar must cascade to hidden")
}

func TestMediaServiceHideReturnsNotFoundForCrossOwnerID(t *testing.T) {
	r := require.New(t)
	fx := newMediaServiceTest(t)
	ctx := context.Background()

	ownerB := owners.Principal{Hub: "h", UserID: "b"}
	_, err := fx.rw.ExecContext(ctx,
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		ownerB.Hub, ownerB.UserID, "sk-b", time.Now().UTC(),
	)
	r.NoError(err)
	mB := insertTestMedia(t, fx.repo, ownerB, "2024/b.jpg", "cs-cross")

	result, err := fx.svc.Hide(ctx, fx.owner, []string{mB.ID})
	r.NoError(err)
	r.Len(result.Failed, 1)
	r.Equal(mB.ID, result.Failed[0].ID)
	r.Equal("not_found", result.Failed[0].Code)
	r.Empty(result.Succeeded)
}

func TestMediaServiceHideEmptyIDsReturnsEmptyResult(t *testing.T) {
	r := require.New(t)
	fx := newMediaServiceTest(t)
	ctx := context.Background()

	result, err := fx.svc.Hide(ctx, fx.owner, []string{})
	r.NoError(err)
	r.Empty(result.Succeeded)
	r.Empty(result.Failed)
}

func TestMediaServiceUnhideCascadesToSidecars(t *testing.T) {
	r := require.New(t)
	fx := newMediaServiceTest(t)
	ctx := context.Background()

	primary := insertTestMedia(t, fx.repo, fx.owner, "2024/u.jpg", "cs-unhide")
	sidecar := insertTestMedia(t, fx.repo, fx.owner, "2024/u.dng", "cs-unhide-sid")
	r.NoError(fx.repo.UpdatePairedWithID(ctx, sidecar.ID, &primary.ID))

	// Mark both hidden first.
	markHidden(t, fx.rw, primary.ID)
	markHidden(t, fx.rw, sidecar.ID)

	result, err := fx.svc.Unhide(ctx, fx.owner, []string{primary.ID})
	r.NoError(err)
	r.Len(result.Succeeded, 1)
	r.Empty(result.Failed)

	// Both must now be visible.
	got, err := fx.repo.GetByID(ctx, primary.ID)
	r.NoError(err)
	r.Nil(got.HiddenAt, "primary must be visible after Unhide")
	gotSidecar, err := fx.repo.GetByID(ctx, sidecar.ID)
	r.NoError(err)
	r.Nil(gotSidecar.HiddenAt, "sidecar must cascade to visible")
}

func TestMediaServiceListHiddenReturnsOnlyHiddenPrimaries(t *testing.T) {
	r := require.New(t)
	fx := newMediaServiceTest(t)
	ctx := context.Background()

	visible := insertTestMedia(t, fx.repo, fx.owner, "2024/v.jpg", "cs-list-vis")
	hidden1 := insertTestMedia(t, fx.repo, fx.owner, "2024/h1.jpg", "cs-list-h1")
	hidden2 := insertTestMedia(t, fx.repo, fx.owner, "2024/h2.jpg", "cs-list-h2")
	markHidden(t, fx.rw, hidden1.ID)
	markHidden(t, fx.rw, hidden2.ID)
	_ = visible

	rows, err := fx.svc.ListHidden(ctx, fx.owner, 10, 0)
	r.NoError(err)
	r.Len(rows, 2)
	ids := []string{rows[0].ID, rows[1].ID}
	r.Contains(ids, hidden1.ID)
	r.Contains(ids, hidden2.ID)
}

func TestMediaServiceClearAllHiddenForOwner(t *testing.T) {
	r := require.New(t)
	fx := newMediaServiceTest(t)
	ctx := context.Background()

	m1 := insertTestMedia(t, fx.repo, fx.owner, "2024/c1.jpg", "cs-c1")
	m2 := insertTestMedia(t, fx.repo, fx.owner, "2024/c2.jpg", "cs-c2")
	markHidden(t, fx.rw, m1.ID)
	markHidden(t, fx.rw, m2.ID)

	r.NoError(fx.svc.ClearAllHiddenForOwner(ctx, fx.owner))

	got1, err := fx.repo.GetByID(ctx, m1.ID)
	r.NoError(err)
	r.Nil(got1.HiddenAt)
	got2, err := fx.repo.GetByID(ctx, m2.ID)
	r.NoError(err)
	r.Nil(got2.HiddenAt)
}

func TestMediaService_ListGeo_OwnerScoped(t *testing.T) {
	r := require.New(t)
	fx := newMediaServiceTest(t)
	ctx := context.Background()

	// Seed a second owner B alongside fx.owner.
	ownerB := owners.Principal{Hub: "h", UserID: "b"}
	_, err := fx.rw.ExecContext(ctx,
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		ownerB.Hub, ownerB.UserID, "sk-b", time.Now().UTC(),
	)
	r.NoError(err)

	insertTestMediaGPS(t, fx.repo, fx.owner, "2024/p1.jpg", "cs-a", 10.0, 20.0)
	insertTestMediaGPS(t, fx.repo, ownerB, "2024/p2.jpg", "cs-b", 30.0, 40.0)

	rows, err := fx.svc.ListGeo(ctx, fx.owner, service.ListGeoOptions{})
	r.NoError(err)
	r.Len(rows, 1)
	r.Equal("2024/p1.jpg", rows[0].Path)
}

func TestMediaService_ListGeo_PropagatesIncludeHidden(t *testing.T) {
	r := require.New(t)
	fx := newMediaServiceTest(t)
	ctx := context.Background()

	visible := insertTestMediaGPS(t, fx.repo, fx.owner, "2024/v.jpg", "cs-vis", 10.0, 20.0)
	hidden := insertTestMediaGPS(t, fx.repo, fx.owner, "2024/h.jpg", "cs-hid", 30.0, 40.0)
	markHidden(t, fx.rw, hidden.ID)

	visOnly, err := fx.svc.ListGeo(ctx, fx.owner, service.ListGeoOptions{})
	r.NoError(err)
	r.Len(visOnly, 1)
	r.Equal(visible.ID, visOnly[0].ID)

	all, err := fx.svc.ListGeo(ctx, fx.owner, service.ListGeoOptions{IncludeHidden: true})
	r.NoError(err)
	r.Len(all, 2)
	ids := []string{all[0].ID, all[1].ID}
	r.ElementsMatch([]string{visible.ID, hidden.ID}, ids)
}
