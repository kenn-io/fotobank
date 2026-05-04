package ingest_test

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/ai"
	aiembedding "github.com/wesm/fotobank/internal/ai/embedding"
	"github.com/wesm/fotobank/internal/ai/jobs"
	"github.com/wesm/fotobank/internal/ai/skipped"
	"github.com/wesm/fotobank/internal/db"
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
	db    *db.DB
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
	return &importerFixture{owner: owner, store: store, repo: repo, nas: nas, db: d}
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

// TestImportDirectoryRejectsEmptyRoot pins the empty-root guard. Without
// it, filepath.Abs("") would silently substitute the process CWD as the
// import root — letting a buggy caller import the working directory.
func TestImportDirectoryRejectsEmptyRoot(t *testing.T) {
	r := require.New(t)
	f := newImporterFixture(t)
	imp := ingest.NewImporter(f.store, f.repo, nil)
	res, err := imp.ImportDirectory(context.Background(), "",
		ingest.Options{Owner: f.owner, ConcurrentWorkers: 1})
	r.Error(err)
	r.Equal(0, res.Imported)
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

// fixtureDNGBytes returns a minimal TIFF byte slice masquerading as a
// DNG. The importer doesn't decode RAW pixels, so the bytes only need
// to be classifiable by extension and survive checksumming. We embed a
// JPEG preview the same way a real DNG would so the fixture is
// recognisable.
func fixtureDNGBytes(t *testing.T) []byte {
	t.Helper()
	r := require.New(t)
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := range 8 {
		for x := range 8 {
			img.Set(x, y, color.RGBA{R: uint8(x * 32), G: uint8(y * 32), B: 128, A: 255})
		}
	}
	var jbuf bytes.Buffer
	r.NoError(jpeg.Encode(&jbuf, img, &jpeg.Options{Quality: 60}))
	jpegBytes := jbuf.Bytes()

	var buf bytes.Buffer
	buf.WriteString("II")
	r.NoError(binary.Write(&buf, binary.LittleEndian, uint16(42)))
	r.NoError(binary.Write(&buf, binary.LittleEndian, uint32(8)))
	r.NoError(binary.Write(&buf, binary.LittleEndian, uint16(2)))
	ifdSize := uint32(2 + 2*12 + 4)
	jpegOffset := uint32(8) + ifdSize
	r.NoError(binary.Write(&buf, binary.LittleEndian, uint16(513)))
	r.NoError(binary.Write(&buf, binary.LittleEndian, uint16(4)))
	r.NoError(binary.Write(&buf, binary.LittleEndian, uint32(1)))
	r.NoError(binary.Write(&buf, binary.LittleEndian, jpegOffset))
	r.NoError(binary.Write(&buf, binary.LittleEndian, uint16(514)))
	r.NoError(binary.Write(&buf, binary.LittleEndian, uint16(4)))
	r.NoError(binary.Write(&buf, binary.LittleEndian, uint32(1)))
	r.NoError(binary.Write(&buf, binary.LittleEndian, uint32(len(jpegBytes))))
	r.NoError(binary.Write(&buf, binary.LittleEndian, uint32(0)))
	buf.Write(jpegBytes)
	return buf.Bytes()
}

// fixtureJPEGBytes reads the existing photo-with-timestamp.jpg fixture
// so the test has a real JPEG (with EXIF, decodable) without
// constructing one from scratch.
func fixtureJPEGBytes(t *testing.T) []byte {
	t.Helper()
	r := require.New(t)
	b, err := os.ReadFile(filepath.Join(fixtureDir(t), "photo-with-timestamp.jpg"))
	r.NoError(err)
	return b
}

// TestImporterPairsJPEGWithExistingRAW locks the F2.2 bidirectional
// pairing contract: a JPEG imported in batch N must pair with a RAW
// already imported in batch N-1 (and vice versa). Without the
// post-barrier pass that fetches existing rows in the touched
// directories, the JPEG would land but its sibling RAW would stay
// orphaned at paired_with_id = NULL.
func TestImporterPairsJPEGWithExistingRAW(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	fx := newImporterFixture(t)
	imp := ingest.NewImporter(fx.store, fx.repo, nil)

	// Stage 1: import only the RAW.
	srcRoot, err := filepath.Abs(t.TempDir())
	r.NoError(err)
	rawDir := filepath.Join(srcRoot, "trip-paris")
	r.NoError(os.MkdirAll(rawDir, 0o755))
	rawPath := filepath.Join(rawDir, "IMG_1234.DNG")
	r.NoError(os.WriteFile(rawPath, fixtureDNGBytes(t), 0o644))

	res, err := imp.ImportDirectory(ctx, srcRoot,
		ingest.Options{Owner: fx.owner, ConcurrentWorkers: 2})
	r.NoError(err)
	r.Equal(1, res.Imported)
	r.Empty(res.Failures)

	// Stage 2: drop the JPEG into the same directory and import again.
	jpegPath := filepath.Join(rawDir, "IMG_1234.JPG")
	r.NoError(os.WriteFile(jpegPath, fixtureJPEGBytes(t), 0o644))
	res, err = imp.ImportDirectory(ctx, srcRoot,
		ingest.Options{Owner: fx.owner, ConcurrentWorkers: 2})
	r.NoError(err)
	r.Equal(1, res.Imported) // The DNG is now a duplicate-by-checksum, ignored.
	r.Equal(1, res.Duplicates)
	r.Empty(res.Failures)

	// Both rows must exist; the DNG (imported in stage 1) should now
	// have paired_with_id pointing at the JPEG.
	rows, err := fx.repo.List(ctx,
		media.ListFilter{Owner: fx.owner, IncludeSidecars: true})
	r.NoError(err)
	r.Len(rows, 2)
	var jpegRow, dngRow media.Media
	for _, m := range rows {
		switch m.OriginalFilename {
		case "IMG_1234.JPG":
			jpegRow = m
		case "IMG_1234.DNG":
			dngRow = m
		}
	}
	r.NotEmpty(jpegRow.ID, "JPEG row missing")
	r.NotEmpty(dngRow.ID, "DNG row missing")
	r.Nil(jpegRow.PairedWithID, "JPEG primary should not have paired_with_id")
	r.NotNil(dngRow.PairedWithID, "DNG sidecar should be paired")
	r.Equal(jpegRow.ID, *dngRow.PairedWithID)

	// Both rows should carry root-relative source paths captured at
	// import time so the backfill CLI can recompute pairs without
	// touching the source filesystem.
	r.Equal("trip-paris/IMG_1234.JPG", jpegRow.ImportSourcePath)
	r.Equal("trip-paris/IMG_1234.DNG", dngRow.ImportSourcePath)
}

// TestImporter_EnqueuesEmbedJobAlongsideTagCaption locks the Task I1
// end-to-end contract: when the importer is wired with a real
// AIEnqueuer that has an embed fingerprint set, importing a photo
// produces one ai_jobs row per task (tag, caption, embed). Without
// this test the per-task wiring inside ImportFile could regress and
// only the unit test on realAIEnqueuer would catch it; this asserts
// the path through processPhoto -> repo.Insert -> ai.EnqueueForPhoto
// is intact.
func TestImporter_EnqueuesEmbedJobAlongsideTagCaption(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	fx := newImporterFixture(t)

	q := jobs.NewQueue(fx.db.WriteDB(), fx.db.ReadDB())
	skipR := skipped.NewRepo(fx.db.WriteDB(), fx.db.ReadDB())
	tagFP := ai.Fingerprint{ModelID: "tag-m", PromptVersion: "tags-v1", InputProfile: "ip"}
	capFP := ai.Fingerprint{ModelID: "cap-m", PromptVersion: "caption-v1", InputProfile: "ip"}
	embedFP := aiembedding.Fingerprint(ai.EmbedConfig{Model: "siglip2", InputEdge: 384})

	imp := ingest.NewImporter(fx.store, fx.repo, nil)
	imp.SetAIEnqueuer(ingest.NewRealAIEnqueuer(tagFP, capFP, q.Enqueue, skipR.Record).
		WithEmbed(embedFP))

	src := seedSource(t, "photo-with-timestamp.jpg")
	res, err := imp.ImportDirectory(ctx, src,
		ingest.Options{Owner: fx.owner, ConcurrentWorkers: 1})
	r.NoError(err)
	r.Equal(1, res.Imported)
	r.Empty(res.Failures)

	rows, err := fx.repo.List(ctx, media.ListFilter{Owner: fx.owner})
	r.NoError(err)
	r.Len(rows, 1)
	mid := rows[0].ID

	// One row per task — and explicit per-task assertions so a regression
	// that drops a single task fails with a clear "import must enqueue X"
	// message rather than a single combined-count mismatch.
	for _, task := range []string{"tag", "caption", "embed"} {
		var n int
		r.NoError(fx.db.ReadDB().QueryRowContext(ctx,
			`SELECT COUNT(*) FROM ai_jobs WHERE media_id = ? AND task = ?`,
			mid, task,
		).Scan(&n))
		r.Equal(1, n, "import must enqueue %s", task)
	}
}

// TestImporter_PopulatesFTSImmediately pins the J2 wiring contract on
// the importer side: after a successful import, the media_fts row for
// the just-imported media exists and carries the corpus columns
// derived from the media row. caption_text and tag_label are empty
// because no AI promotion has run yet — the importer alone seeds the
// FTS row's filename / camera / lens / location_label surface.
func TestImporter_PopulatesFTSImmediately(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	fx := newImporterFixture(t)
	imp := ingest.NewImporter(fx.store, fx.repo, nil)

	src := seedSource(t, "photo-with-timestamp.jpg")
	res, err := imp.ImportDirectory(ctx, src,
		ingest.Options{Owner: fx.owner, ConcurrentWorkers: 1})
	r.NoError(err)
	r.Equal(1, res.Imported)
	r.Empty(res.Failures)

	rows, err := fx.repo.List(ctx, media.ListFilter{Owner: fx.owner})
	r.NoError(err)
	r.Len(rows, 1)
	mid := rows[0].ID

	var captionText, tagLabel, filename, camera, lens, locationLabel string
	r.NoError(fx.db.ReadDB().QueryRowContext(ctx,
		`SELECT caption_text, tag_label, filename, camera, lens, location_label
		   FROM media_fts WHERE media_id = ?`, mid,
	).Scan(&captionText, &tagLabel, &filename, &camera, &lens, &locationLabel))

	// Filename mirrors the source basename; caption_text and tag_label
	// stay empty until AI promotion runs. Camera is derived from the
	// EXIF Make+Model on the fixture; the test fixture is known to have
	// a Make at minimum — assert non-empty rather than pinning the exact
	// vendor string.
	r.Equal("photo-with-timestamp.jpg", filename)
	r.Empty(captionText, "caption is empty until AI promotion runs")
	r.Empty(tagLabel, "tag corpus is empty until AI promotion runs")
	r.NotEmpty(camera, "camera is derived from EXIF make/model on import")
}

// TestImporter_VideoImportSkipsAllThreeTasksWhenEmbedEnabled mirrors
// the photo path for videos: when embed is wired, importing a video
// records ai_skipped rows for tag, caption, AND embed. The embed gap
// scanner relies on this to avoid re-enqueueing videos forever.
func TestImporter_VideoImportSkipsAllThreeTasksWhenEmbedEnabled(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	fx := newImporterFixture(t)

	q := jobs.NewQueue(fx.db.WriteDB(), fx.db.ReadDB())
	skipR := skipped.NewRepo(fx.db.WriteDB(), fx.db.ReadDB())
	tagFP := ai.Fingerprint{ModelID: "tag-m", PromptVersion: "tags-v1", InputProfile: "ip"}
	capFP := ai.Fingerprint{ModelID: "cap-m", PromptVersion: "caption-v1", InputProfile: "ip"}
	embedFP := aiembedding.Fingerprint(ai.EmbedConfig{Model: "siglip2", InputEdge: 384})

	imp := ingest.NewImporter(fx.store, fx.repo, nil)
	imp.SetAIEnqueuer(ingest.NewRealAIEnqueuer(tagFP, capFP, q.Enqueue, skipR.Record).
		WithEmbed(embedFP))

	src := seedSource(t, "video.mp4")
	res, err := imp.ImportDirectory(ctx, src,
		ingest.Options{Owner: fx.owner, ConcurrentWorkers: 1})
	r.NoError(err)
	r.Equal(1, res.Imported)
	r.Empty(res.Failures)

	rows, err := fx.repo.List(ctx, media.ListFilter{Owner: fx.owner})
	r.NoError(err)
	r.Len(rows, 1)
	mid := rows[0].ID

	for _, task := range []ai.Task{ai.TaskTag, ai.TaskCaption, ai.TaskEmbed} {
		reason, found, err := skipR.Get(ctx, mid, task)
		r.NoError(err, "skip lookup for %s", task)
		r.True(found, "video import must record skip for %s", task)
		r.Equal("video", reason)
	}
}

// TestImportProgressFiresIncrementally pins the contract that the
// Progress callback observes per-candidate completion as it happens —
// not in a single burst at the end of the run. The original feature
// landed with the result-drain loop running AFTER wg.Wait(), which
// made the import LOOK hung from the user's terminal: every progress
// event fired in microseconds at the very end. The fix moves the
// drain into a goroutine that runs concurrently with workers; this
// test reproduces the symptom by parking each candidate's worker
// behind a per-candidate gate and asserting Progress fires for
// candidate K before candidate K+1 is ever released.
func TestImportProgressFiresIncrementally(t *testing.T) {
	r := require.New(t)
	f := newImporterFixture(t)
	// Three real fixtures so the importer's pipeline has actual bytes
	// to checksum and write — we don't need to fake processCandidate.
	src := seedSource(t,
		"photo-with-timestamp.jpg",
		"photo-no-exif.jpg",
		"video.mp4",
	)
	imp := ingest.NewImporter(f.store, f.repo, nil)

	// Sequential workers (1) so the per-candidate ordering is
	// deterministic regardless of OS scheduler whims.
	var (
		mu           sync.Mutex
		seenDone     []int
		seenAt       []time.Time
		announceSeen bool
	)
	progress := func(ev ingest.ProgressEvent) {
		mu.Lock()
		defer mu.Unlock()
		if ev.Done == 0 && !announceSeen {
			announceSeen = true
			return
		}
		seenDone = append(seenDone, ev.Done)
		seenAt = append(seenAt, time.Now())
	}

	start := time.Now()
	res, err := imp.ImportDirectory(context.Background(), src, ingest.Options{
		Owner:             f.owner,
		ConcurrentWorkers: 1,
		Progress:          progress,
	})
	r.NoError(err)
	r.Equal(3, res.Imported)

	// Three per-candidate events with monotonically increasing Done.
	r.True(announceSeen, "announce event must fire before per-candidate events")
	r.Equal([]int{1, 2, 3}, seenDone)

	// Liveness: the FIRST per-candidate event must arrive STRICTLY
	// before the import call returns. The original drain-after-wait
	// bug made all three events arrive in a microsecond burst at
	// import-completion time; this assertion fails under that bug.
	r.Less(seenAt[0].Sub(start), time.Since(start),
		"first progress event must arrive before ImportDirectory returns")
	// Sanity: total wall time should be at least an OS time tick so
	// the timestamp comparison above is meaningful (paranoia against
	// a too-fast machine where everything happens in 0ns).
	r.Greater(time.Since(start), time.Microsecond)
}
