package ingest_test

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/ingest"
	"github.com/wesm/fotobank/internal/media"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/storage"
	"github.com/wesm/fotobank/internal/testutil"
)

const testStorageKey = "sk-importer"

// importerFixture wires a DB, a NAS-only store, and a registered owner.
type importerFixture struct {
	owner owners.Principal
	store *storage.NASOnly
	repo  *media.Repo
	nas   string
}

func newImporterFixture(t *testing.T) *importerFixture {
	t.Helper()
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())

	owner := owners.Principal{Hub: "h", UserID: "u"}
	_, err := d.WriteDB().ExecContext(context.Background(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		owner.Hub, owner.UserID, testStorageKey, time.Now().UTC(),
	)
	r.NoError(err)

	nas := t.TempDir()
	store := storage.NewNASOnly(nas, map[owners.Principal]string{owner: testStorageKey})
	return &importerFixture{owner: owner, store: store, repo: repo, nas: nas}
}

// copyFile copies src to dst without preserving permissions, a thin
// io.Copy wrapper that matches the Plan B helper sketch.
func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	r := require.New(t)
	in, err := os.Open(src)
	r.NoError(err)
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	r.NoError(err)
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	r.NoError(copyErr)
	r.NoError(closeErr)
}

func fixtureDir(t *testing.T) string {
	t.Helper()
	// tests run from the package dir, so fixtures live two levels up.
	return filepath.Join("..", "..", "testdata", "exif")
}

func seedSource(t *testing.T, names ...string) string {
	t.Helper()
	src := t.TempDir()
	for _, name := range names {
		copyFile(t, filepath.Join(fixtureDir(t), name), filepath.Join(src, name))
	}
	return src
}

func TestImportHappyPath(t *testing.T) {
	r := require.New(t)
	f := newImporterFixture(t)
	src := seedSource(t, "photo-with-timestamp.jpg", "photo-no-exif.jpg", "video.mp4")

	imp := ingest.NewImporter(f.store, f.repo, nil)
	res, err := imp.ImportDirectory(context.Background(), src, ingest.Options{Owner: f.owner, ConcurrentWorkers: 2})
	r.NoError(err)
	r.Equal(3, res.Imported)
	r.Equal(0, res.Duplicates)
	r.Equal(0, res.PathCollisions)
	r.Empty(res.Failures)

	// Verify media rows.
	all, err := f.repo.List(context.Background(), media.ListFilter{Owner: f.owner})
	r.NoError(err)
	r.Len(all, 3)

	byPath := map[string]media.Media{}
	for _, m := range all {
		byPath[m.Path] = m
	}

	// photo-with-timestamp.jpg -> 2024/20240615_143022_0.jpg.
	tsPhoto, ok := byPath["2024/20240615_143022_0.jpg"]
	r.True(ok, "timestamped photo missing; got %v", byPath)
	r.Equal(media.TypePhoto, tsPhoto.Type)
	r.NotNil(tsPhoto.Timestamp)
	r.Equal(time.Date(2024, 6, 15, 14, 30, 22, 0, time.UTC), tsPhoto.Timestamp.UTC())
	r.Equal("pending", tsPhoto.ThumbStatus)
	r.NotEmpty(tsPhoto.Checksum)
	r.NotEmpty(tsPhoto.Make)
	r.Equal("photo-with-timestamp.jpg", tsPhoto.OriginalFilename)

	// Bytes landed on the NAS.
	r.FileExists(filepath.Join(f.nas, testStorageKey, "2024", "20240615_143022_0.jpg"))

	// photo-no-exif.jpg -> unknown_date/photo-no-exif_0.jpg.
	nePhoto, ok := byPath["unknown_date/photo-no-exif_0.jpg"]
	r.True(ok, "no-exif photo missing; got %v", byPath)
	r.Nil(nePhoto.Timestamp)
	r.Equal("photo-no-exif.jpg", nePhoto.OriginalFilename)
	r.FileExists(filepath.Join(f.nas, testStorageKey, "unknown_date", "photo-no-exif_0.jpg"))

	// video.mp4 -> movies/{md5}.mp4.
	var videoRow media.Media
	for _, m := range all {
		if m.Type == media.TypeVideo {
			videoRow = m
			break
		}
	}
	r.NotEmpty(videoRow.ID)
	r.Equal("movies/"+videoRow.Checksum+".mp4", videoRow.Path)
	r.Equal("video.mp4", videoRow.OriginalFilename)
	r.FileExists(filepath.Join(f.nas, testStorageKey, "movies", videoRow.Checksum+".mp4"))

	// Second import of the same dir: all three checksums are known.
	res2, err := imp.ImportDirectory(context.Background(), src, ingest.Options{Owner: f.owner, ConcurrentWorkers: 2})
	r.NoError(err)
	r.Equal(0, res2.Imported)
	r.Equal(3, res2.Duplicates)
	r.Equal(0, res2.PathCollisions)
	r.Empty(res2.Failures)
}

func TestImportPhotoSequenceCollision(t *testing.T) {
	r := require.New(t)
	f := newImporterFixture(t)
	src := seedSource(t, "photo-with-timestamp.jpg")

	// Seed a collider directly on disk at the canonical path. Since we
	// wrote it without a matching media row, the dedup check won't fire,
	// so the importer must bump the seq to land at _1.
	colliderDir := filepath.Join(f.nas, testStorageKey, "2024")
	r.NoError(os.MkdirAll(colliderDir, 0o700))
	r.NoError(os.WriteFile(filepath.Join(colliderDir, "20240615_143022_0.jpg"), []byte("other"), 0o600))

	imp := ingest.NewImporter(f.store, f.repo, nil)
	res, err := imp.ImportDirectory(context.Background(), src, ingest.Options{Owner: f.owner, ConcurrentWorkers: 1})
	r.NoError(err)
	r.Equal(1, res.Imported)
	r.Equal(0, res.Duplicates)
	r.Equal(0, res.PathCollisions)
	r.Empty(res.Failures)

	// The import landed at _1.
	r.FileExists(filepath.Join(colliderDir, "20240615_143022_1.jpg"))

	rows, err := f.repo.List(context.Background(), media.ListFilter{Owner: f.owner})
	r.NoError(err)
	r.Len(rows, 1)
	r.Equal("2024/20240615_143022_1.jpg", rows[0].Path)
}

func TestImportSkipsUnsupportedFiles(t *testing.T) {
	r := require.New(t)
	f := newImporterFixture(t)
	src := seedSource(t, "photo-with-timestamp.jpg")
	r.NoError(os.WriteFile(filepath.Join(src, "note.txt"), []byte("hello"), 0o600))

	imp := ingest.NewImporter(f.store, f.repo, nil)
	res, err := imp.ImportDirectory(context.Background(), src, ingest.Options{Owner: f.owner, ConcurrentWorkers: 1})
	r.NoError(err)
	r.Equal(1, res.Imported)
	r.Equal(0, res.Duplicates)
	r.Equal(0, res.PathCollisions)
	r.Empty(res.Failures)
}

// md5Of returns the hex MD5 of the file at path — used by collision
// tests that need to know the canonical video path before import.
func md5Of(t *testing.T, path string) string {
	t.Helper()
	r := require.New(t)
	f, err := os.Open(path)
	r.NoError(err)
	defer f.Close()
	h := md5.New()
	_, err = io.Copy(h, f)
	r.NoError(err)
	return hex.EncodeToString(h.Sum(nil))
}

func TestImportPhantomPhotoPathBumpsSeq(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	f := newImporterFixture(t)
	src := seedSource(t, "photo-with-timestamp.jpg")

	// Seed a phantom media row: path claimed, checksum unrelated to the
	// incoming file. The NAS is empty so writeBytes succeeds at seq=0,
	// but the subsequent Insert hits a (owner, path) UNIQUE violation.
	ts := time.Date(2024, 6, 15, 14, 30, 22, 0, time.UTC)
	phantom := media.Media{
		ID:               uuid.NewString(),
		Owner:            f.owner,
		Type:             media.TypePhoto,
		MimeType:         "image/jpeg",
		Path:             "2024/20240615_143022_0.jpg",
		OriginalFilename: "phantom.jpg",
		ImportedAt:       time.Now().UTC(),
		Timestamp:        &ts,
		Size:             123,
		Checksum:         "phantom-cs",
		ThumbStatus:      "pending",
	}
	r.NoError(f.repo.Insert(ctx, phantom))

	imp := ingest.NewImporter(f.store, f.repo, nil)
	res, err := imp.ImportDirectory(ctx, src, ingest.Options{Owner: f.owner, ConcurrentWorkers: 1})
	r.NoError(err)
	r.Equal(1, res.Imported)
	r.Equal(0, res.Duplicates)
	r.Equal(0, res.PathCollisions)
	r.Empty(res.Failures)

	// The import landed at _1 and the phantom row is untouched.
	r.FileExists(filepath.Join(f.nas, testStorageKey, "2024", "20240615_143022_1.jpg"))
	// No bytes at _0 because the phantom only claimed the DB row.
	_, statErr := os.Stat(filepath.Join(f.nas, testStorageKey, "2024", "20240615_143022_0.jpg"))
	r.ErrorIs(statErr, os.ErrNotExist)

	rows, err := f.repo.List(ctx, media.ListFilter{Owner: f.owner})
	r.NoError(err)
	r.Len(rows, 2)

	var landed media.Media
	for _, m := range rows {
		if m.Checksum != "phantom-cs" {
			landed = m
		}
	}
	r.Equal("2024/20240615_143022_1.jpg", landed.Path)

	phantomRow, err := f.repo.GetByID(ctx, phantom.ID)
	r.NoError(err)
	r.Equal("phantom-cs", phantomRow.Checksum)
}

func TestImportSkipsMatchRowWhenNASBytesAbsent(t *testing.T) {
	// A media row exists for (owner, checksum) with NAS bytes missing.
	// Dedup by checksum must still fire — healing the NAS is a reconcile
	// concern, not an import concern. The import must count this as a
	// duplicate without writing bytes or inserting a new row.
	r := require.New(t)
	ctx := context.Background()
	f := newImporterFixture(t)
	src := seedSource(t, "photo-with-timestamp.jpg")

	sum, err := ingest.Checksum(filepath.Join(fixtureDir(t), "photo-with-timestamp.jpg"))
	r.NoError(err)

	ts := time.Date(2024, 6, 15, 14, 30, 22, 0, time.UTC)
	match := media.Media{
		ID:               uuid.NewString(),
		Owner:            f.owner,
		Type:             media.TypePhoto,
		MimeType:         "image/jpeg",
		Path:             "2024/20240615_143022_0.jpg",
		OriginalFilename: "orig.jpg",
		ImportedAt:       time.Now().UTC(),
		Timestamp:        &ts,
		Size:             42,
		Checksum:         sum,
		ThumbStatus:      "pending",
	}
	r.NoError(f.repo.Insert(ctx, match))

	imp := ingest.NewImporter(f.store, f.repo, nil)
	res, err := imp.ImportDirectory(ctx, src, ingest.Options{Owner: f.owner, ConcurrentWorkers: 1})
	r.NoError(err)
	r.Equal(0, res.Imported)
	r.Equal(1, res.Duplicates)
	r.Equal(0, res.PathCollisions)
	r.Empty(res.Failures)

	// No bytes were written and the row count is unchanged.
	_, statErr := os.Stat(filepath.Join(f.nas, testStorageKey, "2024", "20240615_143022_0.jpg"))
	r.ErrorIs(statErr, os.ErrNotExist)
	rows, err := f.repo.List(ctx, media.ListFilter{Owner: f.owner})
	r.NoError(err)
	r.Len(rows, 1)
}

func TestImportAdoptsVideoOrphan(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	f := newImporterFixture(t)
	src := seedSource(t, "video.mp4")

	// Pre-seed the NAS with identical bytes at movies/{md5}.mp4 but NO
	// media row. The importer must adopt the orphan: insert the row,
	// reuse the bytes, and count it as an import (not a collision).
	sum := md5Of(t, filepath.Join(src, "video.mp4"))
	moviesDir := filepath.Join(f.nas, testStorageKey, "movies")
	r.NoError(os.MkdirAll(moviesDir, 0o700))
	orphanPath := filepath.Join(moviesDir, sum+".mp4")
	in, err := os.ReadFile(filepath.Join(src, "video.mp4"))
	r.NoError(err)
	r.NoError(os.WriteFile(orphanPath, in, 0o600))

	imp := ingest.NewImporter(f.store, f.repo, nil)
	res, err := imp.ImportDirectory(ctx, src, ingest.Options{Owner: f.owner, ConcurrentWorkers: 1})
	r.NoError(err)
	r.Equal(1, res.Imported)
	r.Equal(0, res.Duplicates)
	r.Equal(0, res.PathCollisions)
	r.Empty(res.Failures)

	rows, err := f.repo.List(ctx, media.ListFilter{Owner: f.owner})
	r.NoError(err)
	r.Len(rows, 1)
	r.Equal("movies/"+sum+".mp4", rows[0].Path)
	r.Equal(media.TypeVideo, rows[0].Type)

	// Bytes at the adopted location are still the original orphan bytes.
	r.FileExists(orphanPath)
}

func TestImportVideoCollisionWithDifferentBytesReportsPathCollision(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	f := newImporterFixture(t)
	src := seedSource(t, "video.mp4")

	// Pre-seed movies/{md5}.mp4 with DIFFERENT bytes. Adoption must not
	// fire; the importer must report a path collision and leave the
	// pre-existing bytes alone.
	sum := md5Of(t, filepath.Join(src, "video.mp4"))
	moviesDir := filepath.Join(f.nas, testStorageKey, "movies")
	r.NoError(os.MkdirAll(moviesDir, 0o700))
	squatterPath := filepath.Join(moviesDir, sum+".mp4")
	r.NoError(os.WriteFile(squatterPath, []byte("unrelated content"), 0o600))

	imp := ingest.NewImporter(f.store, f.repo, nil)
	res, err := imp.ImportDirectory(ctx, src, ingest.Options{Owner: f.owner, ConcurrentWorkers: 1})
	r.NoError(err)
	r.Equal(0, res.Imported)
	r.Equal(0, res.Duplicates)
	r.Equal(1, res.PathCollisions)
	r.NotEmpty(res.Failures)

	// Squatter bytes untouched.
	got, err := os.ReadFile(squatterPath)
	r.NoError(err)
	r.Equal([]byte("unrelated content"), got)

	rows, err := f.repo.List(ctx, media.ListFilter{Owner: f.owner})
	r.NoError(err)
	r.Empty(rows)
}

func TestImportReturnsCtxErrOnCancellation(t *testing.T) {
	r := require.New(t)
	f := newImporterFixture(t)
	src := seedSource(t, "photo-with-timestamp.jpg", "photo-no-exif.jpg", "video.mp4")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	imp := ingest.NewImporter(f.store, f.repo, nil)
	res, err := imp.ImportDirectory(ctx, src, ingest.Options{Owner: f.owner, ConcurrentWorkers: 2})
	r.NoError(err)
	r.NotEmpty(res.Failures)
	for _, fe := range res.Failures {
		r.ErrorIs(fe, context.Canceled)
	}
	r.Equal(0, res.Imported)
}

type stubResolver struct{ label string }

func (s stubResolver) Resolve(lat, lon float64) (string, bool) {
	return s.label, s.label != ""
}

func TestImporterPopulatesGPSWhenResolverReturnsLabel(t *testing.T) {
	r := require.New(t)
	f := newImporterFixture(t)
	imp := ingest.NewImporter(f.store, f.repo, stubResolver{label: "Test City, Test Region, Test Country"})

	src := seedSource(t, "photo-with-gps.jpg")

	res, err := imp.ImportDirectory(context.Background(), src,
		ingest.Options{Owner: f.owner, ConcurrentWorkers: 1})
	r.NoError(err)
	r.Equal(1, res.Imported)
	r.Empty(res.Failures)

	all, err := f.repo.ListAll(context.Background(), f.owner)
	r.NoError(err)
	r.Len(all, 1)
	row := all[0]
	r.NotNil(row.Latitude)
	r.NotNil(row.Longitude)
	r.InDelta(48.8566, *row.Latitude, 1e-3)
	r.InDelta(2.3522, *row.Longitude, 1e-3)
	r.Equal("Test City, Test Region, Test Country", row.LocationLabel)
}

func TestImporterLeavesLocationLabelEmptyWhenResolverNil(t *testing.T) {
	r := require.New(t)
	f := newImporterFixture(t)
	imp := ingest.NewImporter(f.store, f.repo, nil)

	src := seedSource(t, "photo-with-gps.jpg")
	_, err := imp.ImportDirectory(context.Background(), src,
		ingest.Options{Owner: f.owner, ConcurrentWorkers: 1})
	r.NoError(err)

	all, err := f.repo.ListAll(context.Background(), f.owner)
	r.NoError(err)
	r.Len(all, 1)
	r.NotNil(all[0].Latitude)
	r.Empty(all[0].LocationLabel)
}

// TestImporterLeavesLocationLabelEmptyWhenResolverReturnsNotOk locks
// the in-ocean / antarctic / no-match production case: the resolver
// said "I don't know", so we still store the raw coords but leave the
// label empty. Without this test the `ok=false` branch in
// buildMediaRow is structurally unexercised — the nil-resolver path
// short-circuits before places.Resolve is called.
func TestImporterLeavesLocationLabelEmptyWhenResolverReturnsNotOk(t *testing.T) {
	r := require.New(t)
	f := newImporterFixture(t)
	// stubResolver returns ok=false when label=="" — see its
	// definition above.
	imp := ingest.NewImporter(f.store, f.repo, stubResolver{label: ""})

	src := seedSource(t, "photo-with-gps.jpg")
	_, err := imp.ImportDirectory(context.Background(), src,
		ingest.Options{Owner: f.owner, ConcurrentWorkers: 1})
	r.NoError(err)

	all, err := f.repo.ListAll(context.Background(), f.owner)
	r.NoError(err)
	r.Len(all, 1)
	r.NotNil(all[0].Latitude)
	r.NotNil(all[0].Longitude)
	r.Empty(all[0].LocationLabel)
}
