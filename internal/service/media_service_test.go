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

	"github.com/wesm/fotobank/internal/errs"
	"github.com/wesm/fotobank/internal/media"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/service"
	"github.com/wesm/fotobank/internal/storage"
	"github.com/wesm/fotobank/internal/testutil"
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

func TestMediaServiceListHidesSidecarsByDefault(t *testing.T) {
	r := require.New(t)
	fx := newMediaServiceTest(t)
	ctx := context.Background()

	primary := insertTestMedia(t, fx.repo, fx.owner, "2024/a.jpg", "cs-pri")
	sidecar := insertTestMedia(t, fx.repo, fx.owner, "2024/a.dng", "cs-sid")
	// Manually set paired_with_id (Task 5 lands the helper; in the
	// meantime, write the FK directly via the test DB).
	_, err := fx.rw.ExecContext(ctx, `UPDATE media SET paired_with_id = ? WHERE id = ?`, primary.ID, sidecar.ID)
	r.NoError(err)

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
	_, err := fx.rw.ExecContext(ctx, `UPDATE media SET paired_with_id = ? WHERE id = ?`, primary.ID, sidecar.ID)
	r.NoError(err)

	// Caller asks for true; service must clamp it back to false.
	rows, err := fx.svc.List(ctx, media.ListFilter{IncludeSidecars: true}, fx.owner)
	r.NoError(err)
	r.Len(rows, 1)
	r.Equal(primary.ID, rows[0].ID)
}
