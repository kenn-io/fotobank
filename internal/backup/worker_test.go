package backup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.kenn.io/fotobank/internal/content"
	"go.kenn.io/fotobank/internal/media"
	"go.kenn.io/fotobank/internal/testutil"
	"go.kenn.io/fotobank/internal/testutil/assetfixture"
)

func TestWorkerKeepsCompleteArchivesAndManualPoints(t *testing.T) {
	r := require.New(t)
	databasePath := filepath.Join(t.TempDir(), "catalog.sqlite")
	database := testutil.OpenTestDBAt(t, databasePath)
	t.Cleanup(func() { r.NoError(database.Close()) })
	owner := testutil.SeedOwner(t, database.WriteDB(), "local", "scheduled-owner")
	vault, err := content.Open(t.Context(), content.Config{Root: filepath.Join(t.TempDir(), "vault")})
	r.NoError(err)
	t.Cleanup(func() { r.NoError(vault.Close()) })
	assetfixture.InsertContent(t, media.NewRepo(database.WriteDB(), database.ReadDB()), vault,
		[]byte("synthetic scheduled original"), media.Media{Owner: owner, OriginalFilename: "photo.jpg"})
	repository, err := content.InitBackupRepository(filepath.Join(t.TempDir(), "repository"))
	r.NoError(err)
	manual, err := CreateArchive(t.Context(), databasePath, vault, repository, "manual")
	r.NoError(err)
	worker := NewWorker(Config{DatabasePath: databasePath, Vault: vault, Repository: repository.Root(), Interval: time.Hour, KeepLast: 1})
	r.NoError(worker.capture(t.Context(), repository))
	r.NoError(worker.capture(t.Context(), repository))
	points, err := repository.Snapshots()
	r.NoError(err)
	r.Len(points, 2)
	ids := make([]string, 0, len(points))
	var scheduledID string
	for _, point := range points {
		ids = append(ids, point.ID)
		if point.Tag == ScheduledTag {
			scheduledID = point.ID
		}
	}
	r.Contains(ids, manual.ID)
	r.NotEmpty(scheduledID)
	verified, err := repository.Verify(t.Context(), content.BackupVerifyOptions{All: true})
	r.NoError(err)
	r.Empty(verified.Problems)
	restored, err := RestoreArchive(t.Context(), repository, scheduledID, filepath.Join(t.TempDir(), "restored"), nil)
	r.NoError(err)
	r.Equal(int64(1), restored.ReferencesVerified)

	// A failed capture must not consume earlier recovery points.
	worker.cfg.DatabasePath = filepath.Join(t.TempDir(), "invalid.sqlite")
	r.NoError(os.WriteFile(worker.cfg.DatabasePath, nil, 0o600))
	r.Error(worker.capture(t.Context(), repository))
	after, err := repository.Snapshots()
	r.NoError(err)
	r.Equal(points, after)
}

func TestWorkerRetriesUnavailableRepositoryWithoutCreatingIt(t *testing.T) {
	r := require.New(t)
	path := filepath.Join(t.TempDir(), "missing")
	vault, err := content.Open(t.Context(), content.Config{Root: filepath.Join(t.TempDir(), "vault")})
	r.NoError(err)
	t.Cleanup(func() { r.NoError(vault.Close()) })
	worker := NewWorker(Config{DatabasePath: filepath.Join(t.TempDir(), "catalog.sqlite"), Vault: vault, Repository: path, Interval: time.Hour, KeepLast: 1})
	delay := worker.runDue(t.Context())
	r.Positive(delay)
	r.NoDirExists(path)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	r.NoError(worker.Run(ctx))
}
