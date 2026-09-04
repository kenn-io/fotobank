package backup_test

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/backup"
	"go.kenn.io/fotobank/internal/content"
	"go.kenn.io/fotobank/internal/contentresolver"
	"go.kenn.io/fotobank/internal/db"
	"go.kenn.io/fotobank/internal/media"
	"go.kenn.io/fotobank/internal/testutil"
	"go.kenn.io/fotobank/internal/testutil/assetfixture"
)

func TestArchiveRestoresCatalogAndReferencedOriginal(t *testing.T) {
	r := require.New(t)
	databasePath := filepath.Join(t.TempDir(), "catalog.sqlite")
	database := testutil.OpenTestDBAt(t, databasePath)
	t.Cleanup(func() { r.NoError(database.Close()) })
	owner := testutil.SeedOwner(t, database.WriteDB(), "local", "archive-owner")
	vault, err := content.Open(t.Context(), content.Config{Root: filepath.Join(t.TempDir(), "vault")})
	r.NoError(err)
	t.Cleanup(func() { r.NoError(vault.Close()) })
	body := []byte("synthetic original included in the recovery point")
	item := assetfixture.InsertContent(t, media.NewRepo(database.WriteDB(), database.ReadDB()), vault, body,
		media.Media{Owner: owner, OriginalFilename: "photo.jpg"})
	repository, err := content.InitBackupRepository(filepath.Join(t.TempDir(), "repository"))
	r.NoError(err)
	snapshot, err := backup.CreateArchive(t.Context(), databasePath, vault, repository, "before-edit")
	r.NoError(err)
	// A later catalog change must not alter the recovery point.
	_, err = database.WriteDB().ExecContext(t.Context(), "UPDATE assets SET make='later edit' WHERE id=?", item.ID)
	r.NoError(err)
	report, err := repository.Verify(t.Context(), content.BackupVerifyOptions{SnapshotID: snapshot.ID})
	r.NoError(err)
	r.Empty(report.Problems)
	restored, err := vault.RestoreBackup(t.Context(), repository, content.BackupRestoreOptions{
		SnapshotID: snapshot.ID, Target: filepath.Join(t.TempDir(), "restored"),
	})
	r.NoError(err)
	catalogPath := filepath.Join(restored.Target, filepath.FromSlash(backup.ArchiveCatalogPath))
	r.NoError(backup.ValidateSnapshot(t.Context(), catalogPath))
	restoredDB, err := db.Open(catalogPath)
	r.NoError(err)
	t.Cleanup(func() { r.NoError(restoredDB.Close()) })
	restoredVault, err := content.Open(t.Context(), content.Config{Root: restored.Target})
	r.NoError(err)
	t.Cleanup(func() { r.NoError(restoredVault.Close()) })
	repo := media.NewRepo(restoredDB.WriteDB(), restoredDB.ReadDB())
	resolver := contentresolver.New(repo, restoredVault)
	ref, err := resolver.ValidateCurrent(t.Context(), item.ID, item.PrimaryFileID)
	r.NoError(err)
	r.Equal(item.CurrentVersionID, ref.VersionID)
	r.Empty(ref.Asset.Make)
	opened, err := resolver.Open(t.Context(), ref, 0, -1)
	r.NoError(err)
	got, err := io.ReadAll(opened.Reader)
	r.NoError(err)
	r.NoError(opened.Reader.Close())
	r.Equal(body, got)
}

func TestArchiveDoesNotPublishInvalidCatalog(t *testing.T) {
	r := require.New(t)
	vault, err := content.Open(t.Context(), content.Config{Root: filepath.Join(t.TempDir(), "vault")})
	r.NoError(err)
	t.Cleanup(func() { r.NoError(vault.Close()) })
	repository, err := content.InitBackupRepository(filepath.Join(t.TempDir(), "repository"))
	r.NoError(err)
	catalog := filepath.Join(t.TempDir(), "catalog.sqlite")
	r.NoError(os.WriteFile(catalog, []byte("not SQLite"), 0o600))
	_, err = backup.CreateArchive(t.Context(), catalog, vault, repository, "invalid")
	r.Error(err)
	snapshots, err := repository.Snapshots()
	r.NoError(err)
	r.Empty(snapshots)
	_, err = backup.CreateArchive(t.Context(), filepath.Dir(catalog), vault, repository, "directory")
	r.ErrorContains(err, "regular SQLite file")
}
