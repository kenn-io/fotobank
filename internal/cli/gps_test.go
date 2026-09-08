package cli_test

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/cli"
	"go.kenn.io/fotobank/internal/content"
	"go.kenn.io/fotobank/internal/httpapi"
	"go.kenn.io/fotobank/internal/media"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/testutil"
	"go.kenn.io/fotobank/internal/testutil/assetfixture"
)

// runGPS invokes the gps subcommand and returns (exitCode, stdout, stderr).
// Invalid arguments are exercised without starting a server.
func runGPS(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := cli.RunContext(context.Background(),
		append([]string{"gps"}, args...), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func writeGPSConfig(t *testing.T, tmp string) string {
	t.Helper()
	cfgPath := filepath.Join(tmp, "gps.toml")
	require.NoError(t, os.WriteFile(cfgPath, fmt.Appendf(nil, `
[nas]
root = %q
[flash]
root = %q
[docbank]
root = %q
[observability]
admin_listen = "127.0.0.1:0"
[identity]
mode = "stub"
[identity.stub]
hub = "h"
user_id = "u"
`, filepath.Join(tmp, "nas"), filepath.Join(tmp, "flash"), filepath.Join(tmp, "docbank")), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(tmp, "nas"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(tmp, "flash"), 0o700))
	return cfgPath
}

func TestGPSBackfillBadSinceErrorsBeforeOpeningDB(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeGPSConfig(t, tmp)
	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)

	code, _, stderr := runGPS(t, "backfill", "--config", cfgPath, "--since", "garbage")
	r.Equal(2, code, "usage error → exit 2; got %s", stderr)
	r.NoFileExists(dbPath, "DB must not be created on bad --since")
}

func TestGPSBackfillNegativeSinceErrors(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeGPSConfig(t, tmp)
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

	code, _, stderr := runLiveGPS(t, cfgPath, dbPath, "backfill",
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

	seedRowForOwner(t, dbPath, owners.Principal{Hub: "h", UserID: "one"})
	seedRowForOwner(t, dbPath, owners.Principal{Hub: "h", UserID: "two"})
	d := testutil.OpenTestDBAt(t, dbPath)
	_, err := d.WriteDB().ExecContext(t.Context(), `UPDATE assets SET latitude=48.8566, longitude=2.3522, location_label=''`)
	r.NoError(err)
	r.NoError(d.Close())
	t.Run("header scopes", func(t *testing.T) {
		r := require.New(t)
		startCheckoutServer(t, cfgPath, dbPath)
		code, stdout, stderr := runGPS(t, "backfill", "--config", cfgPath, "--owner", "h:one", "--mode", "relabel", "--json")
		r.Zero(code, stderr)
		var result httpapi.GPSBackfillResult
		r.NoError(json.Unmarshal([]byte(stdout), &result))
		r.Equal(1, result.Updated)
		code, stdout, stderr = runGPS(t, "backfill", "--config", cfgPath, "--all-owners", "--mode", "relabel", "--json")
		r.Zero(code, stderr)
		r.NoError(json.Unmarshal([]byte(stdout), &result))
		r.Equal(2, result.Processed)
		r.Equal(1, result.Updated)
		r.Equal(1, result.Unchanged)
	})
}

func TestGPSBackfillOwnerAndAllOwnersMutuallyExclusive(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeGPSConfig(t, tmp)
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
	cfgPath := writeGPSConfig(t, tmp)
	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)

	dbCtx := context.Background()
	d := testutil.OpenTestDBAt(t, dbPath)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	owner := owners.Principal{Hub: "h", UserID: "u"}
	_, err := d.WriteDB().ExecContext(dbCtx,
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		owner.Hub, owner.UserID, "550e8400-e29b-41d4-a716-446655440000", time.Now().UTC(),
	)
	r.NoError(err)
	lat, lon := 48.8566, 2.3522
	rowWithGPS := uuid.NewString()
	rowNoGPS := uuid.NewString()
	assetfixture.Insert(t, repo, media.Media{
		ID: rowWithGPS, Owner: owner, Type: media.TypePhoto, MimeType: "image/jpeg",
		ImportedAt: time.Now().UTC(),
		Latitude:   &lat, Longitude: &lon, ThumbStatus: "pending",
	})
	assetfixture.Insert(t, repo, media.Media{
		ID: rowNoGPS, Owner: owner, Type: media.TypePhoto, MimeType: "image/jpeg",
		ImportedAt:  time.Now().UTC(),
		ThumbStatus: "pending",
	})
	r.NoError(d.Close())

	var code int
	var stdout, stderr string
	t.Run("live backfill", func(t *testing.T) {
		startCheckoutServer(t, cfgPath, dbPath)
		code, stdout, stderr = runGPS(t, "backfill", "--config", cfgPath, "--mode", "relabel")
	})
	r.Equal(0, code, "stderr=%s", stderr)
	r.Contains(stdout, "gps backfill:")
	r.Contains(stdout, "updated=1", "stderr=%s", stderr)

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
	cfgPath := writeGPSConfig(t, tmp)
	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)

	dbCtx := context.Background()
	d := testutil.OpenTestDBAt(t, dbPath)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	owner := owners.Principal{Hub: "h", UserID: "u"}
	_, err := d.WriteDB().ExecContext(dbCtx,
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		owner.Hub, owner.UserID, "550e8400-e29b-41d4-a716-446655440000", time.Now().UTC(),
	)
	r.NoError(err)
	lat, lon := 48.8566, 2.3522
	videoID := uuid.NewString()
	assetfixture.Insert(t, repo, media.Media{
		ID: videoID, Owner: owner, Type: media.TypeVideo, MimeType: "video/mp4",
		ImportedAt: time.Now().UTC(),
		Latitude:   &lat, Longitude: &lon, LocationLabel: "Original Label",
		ThumbStatus: "pending",
	})
	r.NoError(d.Close())

	code, stdout, _ := runLiveGPS(t, cfgPath, dbPath, "backfill", "--config", cfgPath, "--mode", "relabel")
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
	cfgPath := writeGPSConfig(t, tmp)
	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)

	dbCtx := context.Background()
	d := testutil.OpenTestDBAt(t, dbPath)
	_, err := d.WriteDB().ExecContext(dbCtx,
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		"h", "u", "550e8400-e29b-41d4-a716-446655440000", time.Now().UTC(),
	)
	r.NoError(err)
	r.NoError(d.Close())

	code, stdout, _ := runLiveGPS(t, cfgPath, dbPath, "backfill", "--config", cfgPath, "--mode", "relabel")
	r.Equal(0, code)
	r.True(strings.HasPrefix(strings.TrimSpace(stdout), "gps backfill:"),
		"expected summary line; got %q", stdout)
}

// TestGPSBackfillFullClearsCoordsWhenEXIFLacksGPS exercises the
// authoritative-clear branch of mode=full: a row with non-NULL coords
// whose backing file has no EXIF GPS segment must be wiped of all four
// GPS fields after the backfill. Docbank source metadata contains no GPS fields
// when the file lacks an EXIF segment, so a tiny non-JPEG byte string is enough.
func TestGPSBackfillFullClearsCoordsWhenEXIFLacksGPS(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeGPSConfig(t, tmp)
	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)

	dbCtx := context.Background()
	d := testutil.OpenTestDBAt(t, dbPath)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	owner := owners.Principal{Hub: "h", UserID: "u"}
	_, err := d.WriteDB().ExecContext(dbCtx,
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		owner.Hub, owner.UserID, "550e8400-e29b-41d4-a716-446655440000", time.Now().UTC(),
	)
	r.NoError(err)
	lat, lon := 48.8566, 2.3522
	gpsAt := time.Date(2024, 6, 15, 14, 30, 22, 0, time.UTC)
	rowID := uuid.NewString()
	contentStore, err := content.Open(dbCtx, content.Config{Root: filepath.Join(tmp, "docbank")})
	r.NoError(err)
	assetfixture.InsertContent(t, repo, contentStore, []byte("not-an-image"), media.Media{
		ID: rowID, Owner: owner, Type: media.TypePhoto, MimeType: "image/jpeg",
		OriginalFilename: "x.jpg", ImportedAt: time.Now().UTC(),
		Latitude: &lat, Longitude: &lon, GPSAt: &gpsAt,
		LocationLabel: "Paris, France",
		ThumbStatus:   "pending",
	})
	r.NoError(contentStore.Close())
	r.NoError(d.Close())

	code, stdout, stderr := runLiveGPS(t, cfgPath, dbPath, "backfill", "--config", cfgPath, "--mode", "full")
	r.Equal(0, code, "stderr=%s", stderr)
	r.Contains(stdout, "updated=1", "stderr=%s", stderr)

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

func TestGPSBackfillRejectsMisboundDocbankVersion(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeGPSConfig(t, tmp)
	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)

	dbCtx := context.Background()
	d := testutil.OpenTestDBAt(t, dbPath)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	owner := owners.Principal{Hub: "h", UserID: "u"}
	_, err := d.WriteDB().ExecContext(dbCtx,
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		owner.Hub, owner.UserID, "550e8400-e29b-41d4-a716-446655440000", time.Now().UTC(),
	)
	r.NoError(err)
	store, err := content.Open(dbCtx, content.Config{Root: filepath.Join(tmp, "docbank")})
	r.NoError(err)
	latitude, longitude := 48.8566, 2.3522
	first := assetfixture.InsertContent(t, repo, store, []byte("first photo"), media.Media{
		Owner: owner, Type: media.TypePhoto, MimeType: "image/jpeg",
		OriginalFilename: "first.jpg", ImportedAt: time.Now().UTC(),
		Latitude: &latitude, Longitude: &longitude, LocationLabel: "Paris, France",
		ThumbStatus: "pending",
	})
	second := assetfixture.InsertContent(t, repo, store, []byte("second photo"), media.Media{
		Owner: owner, Type: media.TypePhoto, MimeType: "image/jpeg",
		OriginalFilename: "second.jpg", ImportedAt: time.Now().UTC(), ThumbStatus: "pending",
	})
	_, err = d.WriteDB().ExecContext(dbCtx, `DELETE FROM assets WHERE id = ?`, second.ID)
	r.NoError(err)
	_, err = d.WriteDB().ExecContext(dbCtx, `UPDATE media_files SET current_version_id = ? WHERE id = ?`,
		second.CurrentVersionID, first.PrimaryFileID)
	r.NoError(err)
	r.NoError(store.Close())
	r.NoError(d.Close())

	code, stdout, stderr := runLiveGPS(t, cfgPath, dbPath, "backfill", "--config", cfgPath, "--mode", "full")
	r.Equal(1, code, "stderr=%s", stderr)
	r.Contains(stdout, "failed=1")
	r.Contains(stderr, "current projection differs from Docbank")

	d = testutil.OpenTestDBAt(t, dbPath)
	defer func() { _ = d.Close() }()
	got, err := media.NewRepo(d.WriteDB(), d.ReadDB()).GetByID(dbCtx, first.ID)
	r.NoError(err)
	r.InDelta(latitude, *got.Latitude, 0)
	r.InDelta(longitude, *got.Longitude, 0)
	r.Equal("Paris, France", got.LocationLabel)
}

// TestGPSBackfillFillMissingTerminatesOnUnchangedBatch is a regression
// test for the cursor-reset infinite loop: in fill-missing mode, rows
// whose backing files have no EXIF GPS stay in the candidate set after
// being processed (they remain both-NULL). With a stale "" cursor, the
// next page would re-fetch the same rows forever. The fixture spans two pages.
func TestGPSBackfillFillMissingTerminatesOnUnchangedBatch(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeGPSConfig(t, tmp)
	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)

	dbCtx := context.Background()
	d := testutil.OpenTestDBAt(t, dbPath)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	owner := owners.Principal{Hub: "h", UserID: "u"}
	_, err := d.WriteDB().ExecContext(dbCtx,
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		owner.Hub, owner.UserID, "550e8400-e29b-41d4-a716-446655440000", time.Now().UTC(),
	)
	r.NoError(err)
	contentStore, err := content.Open(dbCtx, content.Config{Root: filepath.Join(tmp, "docbank")})
	r.NoError(err)
	// Seed 501 photo rows with both coords NULL and exact Docbank versions
	// that have no EXIF segment. fill-missing must visit every row,
	// mark them unchanged, and terminate.
	for i := range 501 {
		id := uuid.NewString()
		assetfixture.InsertContent(t, repo, contentStore,
			[]byte(fmt.Sprintf("no-exif-%d", i)), media.Media{
				ID: id, Owner: owner, Type: media.TypePhoto, MimeType: "image/jpeg",
				OriginalFilename: fmt.Sprintf("file-%d.jpg", i), ImportedAt: time.Now().UTC(),
				ThumbStatus: "pending",
			})
	}
	r.NoError(contentStore.Close())
	r.NoError(d.Close())

	code, stdout, stderr := runLiveGPS(t, cfgPath, dbPath, "backfill", "--config", cfgPath, "--mode", "fill-missing")
	r.Equal(0, code, "stderr=%s", stderr)
	r.Contains(stdout, "processed=501")
	r.Contains(stdout, "unchanged=501")
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

func runLiveGPS(t *testing.T, cfgPath, dbPath string, args ...string) (int, string, string) {
	t.Helper()
	var code int
	var stdout, stderr string
	t.Run("live GPS", func(t *testing.T) {
		startCheckoutServer(t, cfgPath, dbPath)
		code, stdout, stderr = runGPS(t, args...)
	})
	return code, stdout, stderr
}

func TestGPSBackfillOperatorAuthorization(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := writeGPSConfig(t, tmp)
	dbPath := filepath.Join(tmp, "catalog.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)
	record := startCheckoutServer(t, cfgPath, dbPath)
	for _, tc := range []struct {
		name, base, token, body string
		status                  int
	}{
		{"operator", record.Endpoint().BaseURL(), record.Metadata["token"], `{}`, 200},
		{"no token", record.Endpoint().BaseURL(), "", `{}`, 401},
		{"photo listener", record.Metadata["web_url"], record.Metadata["token"], `{}`, 403},
		{"invalid mode", record.Endpoint().BaseURL(), record.Metadata["token"], `{"mode":"invalid"}`, 422},
		{"ambiguous scope", record.Endpoint().BaseURL(), record.Metadata["token"], `{"owner":"h:u","all_owners":true}`, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := require.New(t)
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, tc.base+"/api/v1/operator/gps/backfill", strings.NewReader(tc.body))
			r.NoError(err)
			req.Header.Set("Content-Type", "application/json")
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			response, err := http.DefaultClient.Do(req)
			r.NoError(err)
			defer response.Body.Close()
			_, err = io.Copy(io.Discard, response.Body)
			r.NoError(err)
			r.Equal(tc.status, response.StatusCode)
		})
	}
}
