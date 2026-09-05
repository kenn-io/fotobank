package backup_test

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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
	// A later content append can precede its catalog receipt. Recovery must
	// validate the recorded path/node without requiring this newer head.
	later := []byte("a later original")
	node, err := vault.Stat(t.Context(), item.DocbankVirtualPath)
	r.NoError(err)
	_, err = vault.Replace(t.Context(), content.ReplaceRequest{
		VirtualPath: item.DocbankVirtualPath, NodeID: node.ID,
		BaseVersionID: item.CurrentVersionID,
		Base:          content.Identity{SHA256: item.SHA256, Size: item.Size},
		Expected:      content.Identity{SHA256: fmt.Sprintf("%x", sha256.Sum256(later)), Size: int64(len(later))},
		MediaType:     item.MimeType, Reader: bytes.NewReader(later),
	})
	r.NoError(err)
	snapshot, err := backup.CreateArchive(t.Context(), databasePath, vault, repository, "before-edit")
	r.NoError(err)
	// A later catalog change must not alter the recovery point.
	_, err = database.WriteDB().ExecContext(t.Context(), "UPDATE assets SET make='later edit' WHERE id=?", item.ID)
	r.NoError(err)
	report, err := repository.Verify(t.Context(), content.BackupVerifyOptions{SnapshotID: snapshot.ID})
	r.NoError(err)
	r.Empty(report.Problems)
	r.NoError(vault.Close())
	r.NoError(database.Close())
	restored, err := backup.RestoreArchive(t.Context(), repository, snapshot.ID, filepath.Join(t.TempDir(), "restored"), nil)
	r.NoError(err)
	r.Positive(restored.ReferencesVerified)
	catalogPath := restored.CatalogPath
	r.NoError(backup.ValidateSnapshot(t.Context(), catalogPath))
	restoredDB, err := db.Open(catalogPath)
	r.NoError(err)
	t.Cleanup(func() { r.NoError(restoredDB.Close()) })
	restoredVault, err := content.Open(t.Context(), content.Config{Root: restored.VaultRoot})
	r.NoError(err)
	t.Cleanup(func() { r.NoError(restoredVault.Close()) })
	repo := media.NewRepo(restoredDB.WriteDB(), restoredDB.ReadDB())
	resolver := contentresolver.New(repo, restoredVault)
	ref, err := resolver.ResolveCurrent(t.Context(), item.ID, item.PrimaryFileID)
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

func TestArchiveRestoreRejectsMismatchedCatalogReferences(t *testing.T) {
	r := require.New(t)
	databasePath := filepath.Join(t.TempDir(), "catalog.sqlite")
	database := testutil.OpenTestDBAt(t, databasePath)
	t.Cleanup(func() { r.NoError(database.Close()) })
	owner := testutil.SeedOwner(t, database.WriteDB(), "local", "restore-owner")
	vault, err := content.Open(t.Context(), content.Config{Root: filepath.Join(t.TempDir(), "vault")})
	r.NoError(err)
	t.Cleanup(func() { r.NoError(vault.Close()) })
	item := assetfixture.InsertContent(t, media.NewRepo(database.WriteDB(), database.ReadDB()), vault,
		[]byte("synthetic original"), media.Media{Owner: owner, OriginalFilename: "photo.jpg"})
	_, err = database.WriteDB().ExecContext(t.Context(), "UPDATE media_files SET sha256=? WHERE id=?",
		strings.Repeat("0", 64), item.PrimaryFileID)
	r.NoError(err)
	repository, err := content.InitBackupRepository(filepath.Join(t.TempDir(), "repository"))
	r.NoError(err)
	snapshot, err := backup.CreateArchive(t.Context(), databasePath, vault, repository, "mismatch")
	r.NoError(err)
	result, err := backup.RestoreArchive(t.Context(), repository, snapshot.ID, filepath.Join(t.TempDir(), "restored"), nil)
	r.ErrorContains(err, "does not match its catalog reference")
	r.Empty(result.SnapshotID, "a byte-valid archive is not necessarily a valid product recovery")
}

func TestArchiveRestoreRequiresCapturedCatalog(t *testing.T) {
	r := require.New(t)
	vault, err := content.Open(t.Context(), content.Config{Root: filepath.Join(t.TempDir(), "vault")})
	r.NoError(err)
	t.Cleanup(func() { r.NoError(vault.Close()) })
	repository, err := content.InitBackupRepository(filepath.Join(t.TempDir(), "repository"))
	r.NoError(err)
	snapshot, err := vault.CreateBackup(t.Context(), repository, content.BackupOptions{})
	r.NoError(err)
	result, err := backup.RestoreArchive(t.Context(), repository, snapshot.ID, filepath.Join(t.TempDir(), "restored"), nil)
	r.ErrorContains(err, "validate restored catalog")
	r.Empty(result.SnapshotID)
}

func TestArchiveRestoreRejectsIncorrectVirtualPath(t *testing.T) {
	for _, wrongPath := range []string{"missing", "another-file"} {
		t.Run(wrongPath, func(t *testing.T) {
			r := require.New(t)
			databasePath := filepath.Join(t.TempDir(), "catalog.sqlite")
			database := testutil.OpenTestDBAt(t, databasePath)
			t.Cleanup(func() { r.NoError(database.Close()) })
			owner := testutil.SeedOwner(t, database.WriteDB(), "local", "restore-owner")
			vault, err := content.Open(t.Context(), content.Config{Root: filepath.Join(t.TempDir(), "vault")})
			r.NoError(err)
			t.Cleanup(func() { r.NoError(vault.Close()) })
			repo := media.NewRepo(database.WriteDB(), database.ReadDB())
			item := assetfixture.InsertContent(t, repo, vault, []byte("original"), media.Media{Owner: owner})
			path := item.DocbankVirtualPath + ".other"
			if wrongPath == "another-file" {
				_, err := vault.Create(t.Context(), content.CreateRequest{
					VirtualPath: path, MediaType: item.MimeType,
					Expected: content.Identity{SHA256: item.SHA256, Size: item.Size},
					Reader:   bytes.NewReader([]byte("original")),
				})
				r.NoError(err)
			}
			_, err = database.WriteDB().ExecContext(t.Context(),
				"UPDATE media_files SET docbank_virtual_path=? WHERE id=?", path, item.PrimaryFileID)
			r.NoError(err)
			_, err = contentresolver.New(repo, vault).ValidateCurrent(t.Context(), item.ID, item.PrimaryFileID)
			r.Error(err, "the bad path makes the catalog unusable by normal validation")
			repository, err := content.InitBackupRepository(filepath.Join(t.TempDir(), "repository"))
			r.NoError(err)
			snapshot, err := backup.CreateArchive(t.Context(), databasePath, vault, repository, "bad-path")
			r.NoError(err)
			result, err := backup.RestoreArchive(t.Context(), repository, snapshot.ID, filepath.Join(t.TempDir(), "restored"), nil)
			r.Error(err)
			r.Empty(result.SnapshotID)
		})
	}
}

func TestArchiveRejectsEmptyAndUnrelatedDatabases(t *testing.T) {
	for _, name := range []string{"empty", "unrelated"} {
		t.Run(name, func(t *testing.T) {
			r := require.New(t)
			vault, err := content.Open(t.Context(), content.Config{Root: filepath.Join(t.TempDir(), "vault")})
			r.NoError(err)
			t.Cleanup(func() { r.NoError(vault.Close()) })
			repository, err := content.InitBackupRepository(filepath.Join(t.TempDir(), "repository"))
			r.NoError(err)
			catalog := filepath.Join(t.TempDir(), "catalog.sqlite")
			r.NoError(os.WriteFile(catalog, nil, 0o600))
			if name == "unrelated" {
				db.RegisterSqliteVec()
				other, err := sql.Open("sqlite3", catalog)
				r.NoError(err)
				_, err = other.ExecContext(t.Context(), "CREATE TABLE unrelated (value TEXT)")
				r.NoError(err)
				r.NoError(other.Close())
			}
			_, err = backup.CreateArchive(t.Context(), catalog, vault, repository, name)
			r.Error(err)
			snapshots, err := repository.Snapshots()
			r.NoError(err)
			r.Empty(snapshots)
			if name == "empty" {
				info, err := os.Stat(catalog)
				r.NoError(err)
				r.Zero(info.Size(), "validation must not initialize the source")
			}
		})
	}
}
