package cli_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/cli"
	"go.kenn.io/fotobank/internal/media"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/testutil"
)

// runGPS invokes the gps subcommand and returns (exitCode, stdout, stderr).
// Mirrors the existing thumbs_test pattern.
func runGPS(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := cli.RunContext(context.Background(),
		append([]string{"gps"}, args...), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestGPSBackfillBadSinceErrorsBeforeOpeningDB(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeBasicConfig(t, tmp)
	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)

	code, _, stderr := runGPS(t, "backfill", "--config", cfgPath, "--since", "garbage")
	r.Equal(2, code, "usage error → exit 2; got %s", stderr)
	r.NoFileExists(dbPath, "DB must not be created on bad --since")
}

func TestGPSBackfillNegativeSinceErrors(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeBasicConfig(t, tmp)
	t.Setenv("FOTOBANK_DB_PATH", filepath.Join(tmp, "fotobank.sqlite"))

	code, _, stderr := runGPS(t, "backfill", "--config", cfgPath, "--since", "-1h")
	r.Equal(2, code, "got %s", stderr)
}

func TestGPSBackfillStubModeRequiredByDefault(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeNonStubConfig(t, tmp)
	t.Setenv("FOTOBANK_DB_PATH", filepath.Join(tmp, "fotobank.sqlite"))

	code, _, stderr := runGPS(t, "backfill", "--config", cfgPath)
	r.Equal(2, code, "got %s", stderr)
	r.Contains(stderr, "stub")
}

func TestGPSBackfillOwnerScopeBypassesStubModeRequirement(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeNonStubConfig(t, tmp)
	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)

	code, _, stderr := runGPS(t, "backfill",
		"--config", cfgPath,
		"--owner", "h:u",
		"--mode", "relabel",
	)
	// Owner doesn't exist; this should NOT fail with the stub-mode usage
	// error (exit 2). The point is that the stub-mode gate did NOT fire.
	r.NotEqual(2, code, "stderr=%q", stderr)
}

func TestGPSBackfillAllOwnersBypassesStubModeRequirement(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeNonStubConfig(t, tmp)
	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)

	code, _, _ := runGPS(t, "backfill", "--config", cfgPath, "--all-owners", "--mode", "relabel")
	r.NotEqual(2, code)
}

func TestGPSBackfillOwnerAndAllOwnersMutuallyExclusive(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeBasicConfig(t, tmp)
	t.Setenv("FOTOBANK_DB_PATH", filepath.Join(tmp, "fotobank.sqlite"))

	code, _, stderr := runGPS(t, "backfill",
		"--config", cfgPath,
		"--owner", "h:u",
		"--all-owners",
	)
	r.Equal(2, code, "got %s", stderr)
}

func TestGPSBackfillRelabelOnlyTouchesRowsWithCoords(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeBasicConfig(t, tmp)
	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)

	dbCtx := context.Background()
	d := testutil.OpenTestDBAt(t, dbPath)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	owner := owners.Principal{Hub: "h", UserID: "u"}
	_, err := d.WriteDB().ExecContext(dbCtx,
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		owner.Hub, owner.UserID, "u", time.Now().UTC(),
	)
	r.NoError(err)
	lat, lon := 48.8566, 2.3522
	rowWithGPS := uuid.NewString()
	rowNoGPS := uuid.NewString()
	r.NoError(repo.Insert(dbCtx, media.Media{
		ID: rowWithGPS, Owner: owner, Type: media.TypePhoto, MimeType: "image/jpeg",
		Path: "x.jpg", ImportedAt: time.Now().UTC(), Size: 1, Checksum: "c-" + rowWithGPS,
		Latitude: &lat, Longitude: &lon, ThumbStatus: "pending",
	}))
	r.NoError(repo.Insert(dbCtx, media.Media{
		ID: rowNoGPS, Owner: owner, Type: media.TypePhoto, MimeType: "image/jpeg",
		Path: "y.jpg", ImportedAt: time.Now().UTC(), Size: 1, Checksum: "c-" + rowNoGPS,
		ThumbStatus: "pending",
	}))
	r.NoError(d.Close())

	code, stdout, stderr := runGPS(t, "backfill", "--config", cfgPath, "--mode", "relabel")
	r.Equal(0, code, "stderr=%s", stderr)
	r.Contains(stdout, "gps backfill:")
	r.Contains(stdout, "updated=1")

	d = testutil.OpenTestDBAt(t, dbPath)
	defer func() { _ = d.Close() }()
	repo = media.NewRepo(d.WriteDB(), d.ReadDB())
	got, err := repo.GetByID(dbCtx, rowWithGPS)
	r.NoError(err)
	r.Contains(got.LocationLabel, "France",
		"expected France-shaped label after relabel; got %q", got.LocationLabel)
	got, err = repo.GetByID(dbCtx, rowNoGPS)
	r.NoError(err)
	r.Empty(got.LocationLabel)
}

func TestGPSBackfillSkipsVideos(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeBasicConfig(t, tmp)
	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)

	dbCtx := context.Background()
	d := testutil.OpenTestDBAt(t, dbPath)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	owner := owners.Principal{Hub: "h", UserID: "u"}
	_, err := d.WriteDB().ExecContext(dbCtx,
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		owner.Hub, owner.UserID, "u", time.Now().UTC(),
	)
	r.NoError(err)
	lat, lon := 48.8566, 2.3522
	videoID := uuid.NewString()
	r.NoError(repo.Insert(dbCtx, media.Media{
		ID: videoID, Owner: owner, Type: media.TypeVideo, MimeType: "video/mp4",
		Path: "v.mp4", ImportedAt: time.Now().UTC(), Size: 1, Checksum: "c-" + videoID,
		Latitude: &lat, Longitude: &lon, LocationLabel: "Original Label",
		ThumbStatus: "pending",
	}))
	r.NoError(d.Close())

	code, stdout, _ := runGPS(t, "backfill", "--config", cfgPath, "--mode", "relabel")
	r.Equal(0, code)
	r.Contains(stdout, "updated=0")

	d = testutil.OpenTestDBAt(t, dbPath)
	defer func() { _ = d.Close() }()
	repo = media.NewRepo(d.WriteDB(), d.ReadDB())
	got, err := repo.GetByID(dbCtx, videoID)
	r.NoError(err)
	r.Equal("Original Label", got.LocationLabel,
		"video row must be untouched by gps backfill")
}

func TestGPSBackfillFinalSummaryAlwaysEmitted(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeBasicConfig(t, tmp)
	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)

	dbCtx := context.Background()
	d := testutil.OpenTestDBAt(t, dbPath)
	_, err := d.WriteDB().ExecContext(dbCtx,
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		"h", "u", "u", time.Now().UTC(),
	)
	r.NoError(err)
	r.NoError(d.Close())

	code, stdout, _ := runGPS(t, "backfill", "--config", cfgPath, "--mode", "relabel")
	r.Equal(0, code)
	r.True(strings.HasPrefix(strings.TrimSpace(stdout), "gps backfill:"),
		"expected summary line; got %q", stdout)
}

// TestGPSBackfillFullClearsCoordsWhenEXIFLacksGPS exercises the
// authoritative-clear branch of mode=full: a row with non-NULL coords
// whose backing file has no EXIF GPS segment must be wiped of all four
// GPS fields after the backfill. exifread.ExtractPhotoFromReader
// returns an empty Metadata{} (nil error) when the file lacks an EXIF
// segment entirely, so a tiny non-JPEG byte string is enough.
func TestGPSBackfillFullClearsCoordsWhenEXIFLacksGPS(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeBasicConfig(t, tmp)
	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)

	dbCtx := context.Background()
	d := testutil.OpenTestDBAt(t, dbPath)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	owner := owners.Principal{Hub: "h", UserID: "u"}
	_, err := d.WriteDB().ExecContext(dbCtx,
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		owner.Hub, owner.UserID, "u", time.Now().UTC(),
	)
	r.NoError(err)
	lat, lon := 48.8566, 2.3522
	gpsAt := time.Date(2024, 6, 15, 14, 30, 22, 0, time.UTC)
	rowID := uuid.NewString()
	r.NoError(repo.Insert(dbCtx, media.Media{
		ID: rowID, Owner: owner, Type: media.TypePhoto, MimeType: "image/jpeg",
		Path: "x.jpg", ImportedAt: time.Now().UTC(), Size: 1, Checksum: "c-" + rowID,
		Latitude: &lat, Longitude: &lon, GPSAt: &gpsAt,
		LocationLabel: "Paris, France",
		ThumbStatus:   "pending",
	}))
	r.NoError(d.Close())

	// Stage the NAS file at <nasRoot>/<storage_key>/<path>. Non-EXIF
	// bytes — exifread treats this as Metadata{} with nil error, which
	// drives the Full-mode authoritative clear branch.
	nasFile := filepath.Join(tmp, "nas", "u", "x.jpg")
	r.NoError(os.MkdirAll(filepath.Dir(nasFile), 0o700))
	r.NoError(os.WriteFile(nasFile, []byte("not-an-image"), 0o600))

	code, stdout, stderr := runGPS(t, "backfill", "--config", cfgPath, "--mode", "full")
	r.Equal(0, code, "stderr=%s", stderr)
	r.Contains(stdout, "updated=1")

	d = testutil.OpenTestDBAt(t, dbPath)
	defer func() { _ = d.Close() }()
	repo = media.NewRepo(d.WriteDB(), d.ReadDB())
	got, err := repo.GetByID(dbCtx, rowID)
	r.NoError(err)
	r.Nil(got.Latitude, "Full mode must clear Latitude when EXIF has no GPS")
	r.Nil(got.Longitude, "Full mode must clear Longitude when EXIF has no GPS")
	r.Nil(got.GPSAt, "Full mode must clear GPSAt when EXIF has no GPS")
	r.Empty(got.LocationLabel, "Full mode must clear LocationLabel when EXIF has no GPS")
}

// TestGPSBackfillFillMissingTerminatesOnUnchangedBatch is a regression
// test for the cursor-reset infinite loop: in fill-missing mode, rows
// whose backing files have no EXIF GPS stay in the candidate set after
// being processed (they remain both-NULL). With a stale "" cursor, the
// next page would re-fetch the same rows forever. Shrinking
// backfillBatch lets us prove termination with only a few seeded rows.
func TestGPSBackfillFillMissingTerminatesOnUnchangedBatch(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeBasicConfig(t, tmp)
	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)

	prev := cli.SetBackfillBatchForTest(2)
	t.Cleanup(func() { cli.SetBackfillBatchForTest(prev) })

	dbCtx := context.Background()
	d := testutil.OpenTestDBAt(t, dbPath)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	owner := owners.Principal{Hub: "h", UserID: "u"}
	_, err := d.WriteDB().ExecContext(dbCtx,
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		owner.Hub, owner.UserID, "u", time.Now().UTC(),
	)
	r.NoError(err)
	// Seed 5 photo rows with both coords NULL and matching NAS files
	// that have no EXIF segment. fill-missing must visit every row,
	// mark them unchanged, and terminate.
	for i := range 5 {
		id := uuid.NewString()
		path := fmt.Sprintf("file-%d.jpg", i)
		r.NoError(repo.Insert(dbCtx, media.Media{
			ID: id, Owner: owner, Type: media.TypePhoto, MimeType: "image/jpeg",
			Path: path, ImportedAt: time.Now().UTC(), Size: 1, Checksum: "c-" + id,
			ThumbStatus: "pending",
		}))
		nasFile := filepath.Join(tmp, "nas", "u", path)
		r.NoError(os.MkdirAll(filepath.Dir(nasFile), 0o700))
		r.NoError(os.WriteFile(nasFile, []byte("no-exif"), 0o600))
	}
	r.NoError(d.Close())

	code, stdout, stderr := runGPS(t, "backfill", "--config", cfgPath, "--mode", "fill-missing")
	r.Equal(0, code, "stderr=%s", stderr)
	r.Contains(stdout, "processed=5")
	r.Contains(stdout, "unchanged=5")
}

// TestGPSBackfillMalformedOwnerErrorsBeforeOpeningDB locks in that
// --owner is parse-validated before the DB is opened — a malformed
// value must not create a SQLite file or load the gazetteer.
func TestGPSBackfillMalformedOwnerErrorsBeforeOpeningDB(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeNonStubConfig(t, tmp) // bypass stub-mode requirement
	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)

	code, _, stderr := runGPS(t, "backfill", "--config", cfgPath, "--owner", "no-colon")
	r.Equal(2, code, "got %s", stderr)
	r.NoFileExists(dbPath, "DB must not be created on bad --owner")
}
