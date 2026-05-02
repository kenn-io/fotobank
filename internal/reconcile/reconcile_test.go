package reconcile_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"

	exif "github.com/dsoprea/go-exif/v3"
	exifcommon "github.com/dsoprea/go-exif/v3/common"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/db"
	"github.com/wesm/fotobank/internal/errs"
	"github.com/wesm/fotobank/internal/media"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/reconcile"
	"github.com/wesm/fotobank/internal/storage"
	"github.com/wesm/fotobank/internal/testutil"
)

const (
	testStorageKey = "sk-recon"
)

// reconcileFixture wires a DB, a registered owner, and a NAS root with
// the owner's storage_key subdirectory already created. The db handle
// is exposed so tests that read derived tables (e.g. media_fts) can
// inspect them directly.
type reconcileFixture struct {
	ctx       context.Context
	owner     owners.Principal
	repo      *media.Repo
	db        *db.DB
	nasRoot   string
	ownerRoot string
}

func newReconcileFixture(t *testing.T) *reconcileFixture {
	t.Helper()
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())

	ctx := context.Background()
	owner := owners.Principal{Hub: "h", UserID: "u"}
	_, err := d.WriteDB().ExecContext(ctx,
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		owner.Hub, owner.UserID, testStorageKey, time.Now().UTC(),
	)
	r.NoError(err)

	nasRoot := t.TempDir()
	ownerRoot := filepath.Join(nasRoot, testStorageKey)
	r.NoError(os.MkdirAll(ownerRoot, 0o700))

	return &reconcileFixture{
		ctx:       ctx,
		owner:     owner,
		repo:      repo,
		db:        d,
		nasRoot:   nasRoot,
		ownerRoot: ownerRoot,
	}
}

func (f *reconcileFixture) seedMedia(t *testing.T, path, checksum string, size int64) media.Media {
	t.Helper()
	r := require.New(t)
	m := media.Media{
		ID:          uuid.NewString(),
		Owner:       f.owner,
		Type:        media.TypePhoto,
		MimeType:    "image/jpeg",
		Path:        path,
		ImportedAt:  time.Now().UTC().Truncate(time.Second),
		Size:        size,
		Checksum:    checksum,
		ThumbStatus: "pending",
	}
	r.NoError(f.repo.Insert(f.ctx, m))
	return m
}

func (f *reconcileFixture) writeFile(t *testing.T, rel string, data []byte) string {
	t.Helper()
	r := require.New(t)
	full := filepath.Join(f.ownerRoot, filepath.FromSlash(rel))
	r.NoError(os.MkdirAll(filepath.Dir(full), 0o700))
	r.NoError(os.WriteFile(full, data, 0o600))
	return full
}

func (f *reconcileFixture) defaultOptions() reconcile.Options {
	return reconcile.Options{
		Owner:      f.owner,
		StorageKey: testStorageKey,
		NASRoot:    f.nasRoot,
	}
}

func TestReconcileRejectsInvalidStorageKey(t *testing.T) {
	f := newReconcileFixture(t)
	for _, sk := range []string{"", ".", "..", "../escape", "a/b"} {
		opts := f.defaultOptions()
		opts.StorageKey = sk
		_, err := reconcile.Reconcile(f.ctx, f.repo, opts)
		require.ErrorIs(t, err, storage.ErrInvalidStorageKey, "StorageKey %q must be rejected", sk)
	}
}

func TestReconcileRejectsEmptyNASRoot(t *testing.T) {
	f := newReconcileFixture(t)
	opts := f.defaultOptions()
	opts.NASRoot = ""
	_, err := reconcile.Reconcile(f.ctx, f.repo, opts)
	require.Error(t, err)
}

func TestReconcileReportsOrphansAndMissing(t *testing.T) {
	r := require.New(t)
	f := newReconcileFixture(t)

	// Orphan: bytes on disk, no DB row.
	f.writeFile(t, "2024/orphan.jpg", []byte("orphan-bytes"))
	// Missing: DB row, no bytes.
	missing := f.seedMedia(t, "2024/missing.jpg", "cs-missing", 7)

	rep, err := reconcile.Reconcile(f.ctx, f.repo, f.defaultOptions())
	r.NoError(err)

	r.Len(rep.Orphans, 1)
	r.Equal("2024/orphan.jpg", rep.Orphans[0].Path)
	r.Equal(int64(len("orphan-bytes")), rep.Orphans[0].Size)

	r.Len(rep.Missing, 1)
	r.Equal(missing.ID, rep.Missing[0].ID)
	r.Empty(rep.SizeMismatch)
	r.Empty(rep.StaleTemps)
	r.Zero(rep.DeletedRows)
	r.Zero(rep.DeletedTemps)
}

func TestReconcileDetectsSizeMismatch(t *testing.T) {
	r := require.New(t)
	f := newReconcileFixture(t)

	seeded := f.seedMedia(t, "2024/size.jpg", "cs-size", 100)
	// Disk has 50 bytes but DB says 100.
	f.writeFile(t, "2024/size.jpg", []byte("0123456789012345678901234567890123456789012345678x"))

	rep, err := reconcile.Reconcile(f.ctx, f.repo, f.defaultOptions())
	r.NoError(err)

	r.Empty(rep.Orphans)
	r.Empty(rep.Missing)
	r.Len(rep.SizeMismatch, 1)
	r.Equal(seeded.ID, rep.SizeMismatch[0].MediaID)
	r.Equal(int64(100), rep.SizeMismatch[0].DBSize)
	r.Equal(int64(50), rep.SizeMismatch[0].OnDiskSize)
}

func TestReconcileDetectsStaleTemps(t *testing.T) {
	r := require.New(t)
	f := newReconcileFixture(t)

	stale := f.writeFile(t, "2024/a.jpg.tmp-1234-oldstale", []byte("x"))
	// Backdate mtime 2h.
	old := time.Now().Add(-2 * time.Hour)
	r.NoError(os.Chtimes(stale, old, old))

	// Fresh temp — mtime "now", should not be reported under 1h grace.
	f.writeFile(t, "2024/a.jpg.tmp-1234-fresh", []byte("y"))

	rep, err := reconcile.Reconcile(f.ctx, f.repo, f.defaultOptions())
	r.NoError(err)

	r.Len(rep.StaleTemps, 1)
	r.Equal(stale, rep.StaleTemps[0])
	r.Empty(rep.Orphans) // temp files must not show up as orphans
}

func TestReconcileCommitDeletesRemovesPhantomRows(t *testing.T) {
	r := require.New(t)
	f := newReconcileFixture(t)

	phantom := f.seedMedia(t, "2024/phantom.jpg", "cs-phantom", 11)

	opts := f.defaultOptions()
	opts.CommitDeletes = true
	rep, err := reconcile.Reconcile(f.ctx, f.repo, opts)
	r.NoError(err)

	r.Equal(1, rep.DeletedRows)
	r.Len(rep.Missing, 1)

	_, err = f.repo.GetByID(f.ctx, phantom.ID)
	r.ErrorIs(err, errs.ErrNotFound)
}

func TestReconcileCommitTempsRemovesStaleTemps(t *testing.T) {
	r := require.New(t)
	f := newReconcileFixture(t)

	stale := f.writeFile(t, "2024/a.jpg.tmp-stale", []byte("x"))
	old := time.Now().Add(-2 * time.Hour)
	r.NoError(os.Chtimes(stale, old, old))

	opts := f.defaultOptions()
	opts.CommitTemps = true
	rep, err := reconcile.Reconcile(f.ctx, f.repo, opts)
	r.NoError(err)

	r.Equal(1, rep.DeletedTemps)
	r.Len(rep.StaleTemps, 1)

	_, err = os.Stat(stale)
	r.ErrorIs(err, os.ErrNotExist)
}

func TestReconcileSkipsDotFiles(t *testing.T) {
	r := require.New(t)
	f := newReconcileFixture(t)

	f.writeFile(t, ".fotobank/import.lock", []byte("lock"))
	f.writeFile(t, ".thumbs/2024/thumb.jpg", []byte("thumb"))
	f.writeFile(t, ".DS_Store", []byte("ds"))
	f.writeFile(t, "2024/.DS_Store", []byte("ds"))

	rep, err := reconcile.Reconcile(f.ctx, f.repo, f.defaultOptions())
	r.NoError(err)

	r.Empty(rep.Orphans)
	r.Empty(rep.Missing)
	r.Empty(rep.SizeMismatch)
	r.Empty(rep.StaleTemps)
}

// buildJPEGWithLensModel builds a minimal JPEG (SOI + APP1/EXIF + EOI)
// embedding a LensModel tag. Used by the lens_model backfill test
// to produce on-disk bytes that exifread.ExtractPhoto can parse.
func buildJPEGWithLensModel(t *testing.T, lens string) []byte {
	t.Helper()
	r := require.New(t)
	im, err := exifcommon.NewIfdMappingWithStandard()
	r.NoError(err)
	ti := exif.NewTagIndex()
	rootIb := exif.NewIfdBuilder(im, ti, exifcommon.IfdStandardIfdIdentity, binary.LittleEndian)
	r.NoError(rootIb.AddStandardWithName("Make", "TestMake"))
	childIb := exif.NewIfdBuilder(im, ti, exifcommon.IfdExifStandardIfdIdentity, binary.LittleEndian)
	r.NoError(childIb.AddStandardWithName("LensModel", lens))
	r.NoError(rootIb.AddChildIb(childIb))
	ibe := exif.NewIfdByteEncoder()
	exifData, err := ibe.EncodeToExif(rootIb)
	r.NoError(err)
	app1 := append([]byte("Exif\x00\x00"), exifData...)
	r.Less(len(app1)+2, 0x10000, "APP1 must fit in u16 length field")

	var buf bytes.Buffer
	buf.Write([]byte{0xFF, 0xD8}) // SOI
	buf.Write([]byte{0xFF, 0xE1}) // APP1 marker
	r.NoError(binary.Write(&buf, binary.BigEndian, uint16(len(app1)+2)))
	buf.Write(app1)
	buf.Write([]byte{0xFF, 0xD9}) // EOI
	return buf.Bytes()
}

// TestReconcileBackfillsLensModelWhenNull proves the lens_model
// backfill: a photo row with NULL lens_model whose on-disk file's
// EXIF carries a LensModel value gets updated by Reconcile, while
// rows that already have lens_model set are not overwritten and rows
// without on-disk bytes are left alone.
func TestReconcileBackfillsLensModelWhenNull(t *testing.T) {
	r := require.New(t)
	f := newReconcileFixture(t)

	const lens = "EF 35mm f/1.4L II USM"
	jpeg := buildJPEGWithLensModel(t, lens)

	// Row with on-disk bytes and NULL lens_model — should be filled in.
	withFile := f.seedMedia(t, "2024/lens.jpg", "cs-lens", int64(len(jpeg)))
	f.writeFile(t, "2024/lens.jpg", jpeg)

	// Row with on-disk bytes whose lens_model is already set — must not
	// be overwritten by the backfill.
	preset := media.Media{
		ID:          uuid.NewString(),
		Owner:       f.owner,
		Type:        media.TypePhoto,
		MimeType:    "image/jpeg",
		Path:        "2024/preset.jpg",
		ImportedAt:  time.Now().UTC().Truncate(time.Second),
		Size:        int64(len(jpeg)),
		Checksum:    "cs-preset",
		LensModel:   "Already Set",
		ThumbStatus: "pending",
	}
	r.NoError(f.repo.Insert(f.ctx, preset))
	f.writeFile(t, "2024/preset.jpg", jpeg)

	// Row with NULL lens_model but no on-disk bytes — must be left alone
	// (no LensModelBackfilled credit) and instead surface as Missing.
	missing := f.seedMedia(t, "2024/missing.jpg", "cs-miss", 7)
	_ = missing

	rep, err := reconcile.Reconcile(f.ctx, f.repo, f.defaultOptions())
	r.NoError(err)
	r.Equal(1, rep.LensModelBackfilled)

	got, err := f.repo.GetByID(f.ctx, withFile.ID)
	r.NoError(err)
	r.Equal(lens, got.LensModel)

	gotPreset, err := f.repo.GetByID(f.ctx, preset.ID)
	r.NoError(err)
	r.Equal("Already Set", gotPreset.LensModel)

	// Re-running reconcile is a no-op for the already-filled row.
	rep2, err := reconcile.Reconcile(f.ctx, f.repo, f.defaultOptions())
	r.NoError(err)
	r.Zero(rep2.LensModelBackfilled)
}

// TestReconcileBackfillLensModelRefreshesFTS pins the J2 contract for
// the reconcile-side wiring: when backfillLensModel writes a new
// lens_model value into a row, the media_fts row's lens column
// reflects the same value in the same write transaction. Without the
// wiring, the row would be filled in but lexical search by lens
// vendor would miss it until the next caption/tag promotion or the
// next service-side write reached the row.
func TestReconcileBackfillLensModelRefreshesFTS(t *testing.T) {
	r := require.New(t)
	f := newReconcileFixture(t)

	const lens = "EF 35mm f/1.4L II USM"
	jpeg := buildJPEGWithLensModel(t, lens)
	withFile := f.seedMedia(t, "2024/lens.jpg", "cs-fts-lens", int64(len(jpeg)))
	f.writeFile(t, "2024/lens.jpg", jpeg)

	// Pre-fact: the row has no FTS row yet (the importer would have
	// written one on import, but the test seeds rows directly via the
	// repo). Reconcile's backfill is the write that populates FTS for
	// this row.
	rep, err := reconcile.Reconcile(f.ctx, f.repo, f.defaultOptions())
	r.NoError(err)
	r.Equal(1, rep.LensModelBackfilled)

	var lensCol string
	r.NoError(f.db.ReadDB().QueryRowContext(f.ctx,
		`SELECT lens FROM media_fts WHERE media_id = ?`, withFile.ID).Scan(&lensCol))
	r.Equal(lens, lensCol, "lens corpus column must reflect the just-backfilled lens_model")
}
