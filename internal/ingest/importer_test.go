package ingest_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

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

	imp := ingest.NewImporter(f.store, f.repo)
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

	// Bytes landed on the NAS.
	r.FileExists(filepath.Join(f.nas, testStorageKey, "2024", "20240615_143022_0.jpg"))

	// photo-no-exif.jpg -> unknown_date/photo-no-exif_0.jpg.
	nePhoto, ok := byPath["unknown_date/photo-no-exif_0.jpg"]
	r.True(ok, "no-exif photo missing; got %v", byPath)
	r.Nil(nePhoto.Timestamp)
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

	imp := ingest.NewImporter(f.store, f.repo)
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

	imp := ingest.NewImporter(f.store, f.repo)
	res, err := imp.ImportDirectory(context.Background(), src, ingest.Options{Owner: f.owner, ConcurrentWorkers: 1})
	r.NoError(err)
	r.Equal(1, res.Imported)
	r.Equal(0, res.Duplicates)
	r.Equal(0, res.PathCollisions)
	r.Empty(res.Failures)
}

func TestImportReturnsCtxErrOnCancellation(t *testing.T) {
	r := require.New(t)
	f := newImporterFixture(t)
	src := seedSource(t, "photo-with-timestamp.jpg", "photo-no-exif.jpg", "video.mp4")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	imp := ingest.NewImporter(f.store, f.repo)
	res, err := imp.ImportDirectory(ctx, src, ingest.Options{Owner: f.owner, ConcurrentWorkers: 2})
	r.NoError(err)
	r.NotEmpty(res.Failures)
	for _, fe := range res.Failures {
		r.ErrorIs(fe, context.Canceled)
	}
	r.Equal(0, res.Imported)
}
