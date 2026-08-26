package cli_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/cli"
	"go.kenn.io/fotobank/internal/media"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/testutil"
)

// runPair invokes the pair subcommand and returns (exitCode, stdout, stderr).
// Mirrors the runGPS helper in gps_test.go.
func runPair(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := cli.RunContext(context.Background(),
		append([]string{"pair"}, args...), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestPairBackfillRecomputesPairs(t *testing.T) {
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
		owner.Hub, owner.UserID, "550e8400-e29b-41d4-a716-446655440000", time.Now().UTC(),
	)
	r.NoError(err)
	now := time.Now().UTC()
	primaryID := uuid.NewString()
	sidecarID := uuid.NewString()
	r.NoError(repo.Insert(dbCtx, media.Media{
		ID: primaryID, Owner: owner, Type: media.TypePhoto, MimeType: "image/jpeg",
		Path: "2024/IMG_1.jpg", ImportedAt: now, Size: 1, Checksum: "c-" + primaryID,
		ImportSourcePath: "trip-paris/IMG_1.JPG",
		ThumbStatus:      "pending",
	}))
	r.NoError(repo.Insert(dbCtx, media.Media{
		ID: sidecarID, Owner: owner, Type: media.TypePhoto, MimeType: "image/x-adobe-dng",
		Path: "2024/IMG_1.dng", ImportedAt: now, Size: 1, Checksum: "c-" + sidecarID,
		ImportSourcePath: "trip-paris/IMG_1.DNG",
		ThumbStatus:      "pending",
	}))
	r.NoError(d.Close())

	code, stdout, stderr := runPair(t, "backfill", "--config", cfgPath)
	r.Equal(0, code, "stderr=%s", stderr)
	r.Contains(stdout, "pair backfill:")
	r.Contains(stdout, "paired=1")

	d = testutil.OpenTestDBAt(t, dbPath)
	defer func() { _ = d.Close() }()
	repo = media.NewRepo(d.WriteDB(), d.ReadDB())
	got, err := repo.GetByID(dbCtx, sidecarID)
	r.NoError(err)
	r.NotNil(got.PairedWithID, "sidecar must be paired after backfill")
	r.Equal(primaryID, *got.PairedWithID)

	gotPrimary, err := repo.GetByID(dbCtx, primaryID)
	r.NoError(err)
	r.Nil(gotPrimary.PairedWithID, "primary JPEG must remain unpaired")
}

func TestPairBackfillRejectsBadMode(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeBasicConfig(t, tmp)
	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)

	code, _, stderr := runPair(t, "backfill", "--config", cfgPath, "--mode", "bogus")
	r.Equal(2, code, "got %s", stderr)
	r.Contains(stderr, "--mode")
	_, statErr := os.Stat(dbPath)
	r.True(os.IsNotExist(statErr),
		"DB must not be created on bad --mode: stat=%v", statErr)
}

func TestPairBackfillRejectsOwnerAndAllOwners(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeBasicConfig(t, tmp)
	t.Setenv("FOTOBANK_DB_PATH", filepath.Join(tmp, "fotobank.sqlite"))

	code, _, stderr := runPair(t, "backfill",
		"--config", cfgPath,
		"--owner", "h:u",
		"--all-owners",
	)
	r.Equal(2, code, "got %s", stderr)
	r.Contains(stderr, "mutually exclusive")
}
