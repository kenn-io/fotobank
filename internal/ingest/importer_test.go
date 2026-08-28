package ingest_test

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/content"
	"go.kenn.io/fotobank/internal/ingest"
	"go.kenn.io/fotobank/internal/media"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/testutil"
)

func newImporterFixture(t *testing.T) (*ingest.Importer, *media.AssetRepo, *media.Repo, *content.Adapter, owners.Principal, *sql.DB) {
	t.Helper()
	d := testutil.OpenTestDB(t)
	owner := testutil.SeedOwner(t, d.WriteDB(), "local", "alice")
	var storageKey string
	require.NoError(t, d.ReadDB().QueryRowContext(t.Context(),
		`SELECT storage_key FROM owners WHERE hub=? AND user_id=?`, owner.Hub, owner.UserID,
	).Scan(&storageKey))
	contentStore, err := content.Open(t.Context(), content.Config{Root: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, contentStore.Close()) })
	assets := media.NewAssetRepo(d.WriteDB(), d.ReadDB())
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	return ingest.NewImporter(contentStore, assets, repo, storageKey, nil), assets, repo, contentStore, owner, d.ReadDB()
}

func writeSource(t *testing.T, root, name string, body []byte) string {
	t.Helper()
	path := filepath.Join(root, name)
	require.NoError(t, os.WriteFile(path, body, 0o600))
	return path
}

func TestImporterGroupsJPEGRAWAndXMPIntoOneReadyAsset(t *testing.T) {
	r := require.New(t)
	imp, assets, repo, contentStore, owner, ro := newImporterFixture(t)
	source := t.TempDir()
	wantBodies := map[string][]byte{
		"trip.jpg":     []byte("jpeg bytes"),
		"trip.arw":     []byte("raw bytes"),
		"trip.arw.xmp": []byte("xmp bytes"),
	}
	for name, body := range wantBodies {
		writeSource(t, source, name, body)
	}

	result, err := imp.ImportDirectory(t.Context(), source, ingest.Options{
		Owner: owner, ConcurrentWorkers: 1, SettleInterval: time.Millisecond,
	})
	r.NoError(err)
	r.Equal(1, result.Imported, "result: %+v", result)
	r.Zero(result.Duplicates)
	r.Empty(result.Failures)

	items, err := repo.List(t.Context(), media.ListFilter{Owner: owner})
	r.NoError(err)
	r.Len(items, 1)
	files, err := assets.ListFiles(t.Context(), items[0].ID)
	r.NoError(err)
	r.Len(files, 3)

	byName := make(map[string]media.File, len(files))
	for _, file := range files {
		byName[file.OriginalFilename] = file
		r.NotNil(file.DocbankNodeID)
		r.NotEmpty(file.CurrentVersionID)
		opened, err := contentStore.OpenVersion(t.Context(), file.CurrentVersionID)
		r.NoError(err)
		body, err := io.ReadAll(opened.Reader)
		r.NoError(err)
		r.NoError(opened.Reader.Verify())
		r.NoError(opened.Reader.Close())
		r.Equal(wantBodies[file.OriginalFilename], body)
	}
	r.Equal(media.RolePrimary, byName["trip.jpg"].Role)
	r.Equal(media.RoleOriginal, byName["trip.arw"].Role)
	r.Equal(media.RoleSidecar, byName["trip.arw.xmp"].Role)

	var paired, sidecar int
	r.NoError(ro.QueryRowContext(t.Context(), `SELECT COUNT(*)
		FROM media_file_relationships WHERE source_file_id=? AND target_file_id=? AND kind='paired_with'`,
		byName["trip.arw"].ID, byName["trip.jpg"].ID).Scan(&paired))
	r.NoError(ro.QueryRowContext(t.Context(), `SELECT COUNT(*)
		FROM media_file_relationships WHERE source_file_id=? AND target_file_id=? AND kind='sidecar_of'`,
		byName["trip.arw.xmp"].ID, byName["trip.arw"].ID).Scan(&sidecar))
	r.Equal(1, paired)
	r.Equal(1, sidecar)

	result, err = imp.ImportDirectory(t.Context(), source, ingest.Options{
		Owner: owner, ConcurrentWorkers: 1, SettleInterval: time.Millisecond,
	})
	r.NoError(err)
	r.Zero(result.Imported)
	r.Equal(1, result.Duplicates)
}

func TestImporterRejectsXMPOnlyGroupBeforeReservation(t *testing.T) {
	r := require.New(t)
	imp, _, _, _, owner, ro := newImporterFixture(t)
	source := t.TempDir()
	writeSource(t, source, "orphan.xmp", []byte("sidecar"))

	result, err := imp.ImportDirectory(t.Context(), source, ingest.Options{
		Owner: owner, ConcurrentWorkers: 1, SettleInterval: time.Millisecond,
	})
	r.NoError(err)
	r.Zero(result.Imported)
	r.Len(result.Failures, 1)

	var assetsCount, operationsCount int
	r.NoError(ro.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM assets`).Scan(&assetsCount))
	r.NoError(ro.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM content_operations`).Scan(&operationsCount))
	r.Zero(assetsCount)
	r.Zero(operationsCount)
}

func TestImporterGroupsCompoundJPEGSidecarName(t *testing.T) {
	r := require.New(t)
	imp, _, repo, _, owner, _ := newImporterFixture(t)
	source := t.TempDir()
	writeSource(t, source, "photo.jpg", []byte("jpeg"))
	writeSource(t, source, "photo.jpg.xmp", []byte("sidecar"))

	result, err := imp.ImportDirectory(t.Context(), source, ingest.Options{
		Owner: owner, ConcurrentWorkers: 1, SettleInterval: time.Millisecond,
	})
	r.NoError(err)
	r.Equal(1, result.Imported, "result: %+v", result)
	r.Empty(result.Failures)

	items, err := repo.List(t.Context(), media.ListFilter{Owner: owner})
	r.NoError(err)
	r.Len(items, 1)
	files, err := repo.ListFiles(t.Context(), items[0].ID)
	r.NoError(err)
	r.Len(files, 2)
	var sidecar media.File
	for _, file := range files {
		if file.Role == media.RoleSidecar {
			sidecar = file
		}
	}
	r.Equal("photo.jpg.xmp", sidecar.OriginalFilename)
}

func TestImporterDiscoversFilesThroughSymlinkRoot(t *testing.T) {
	r := require.New(t)
	imp, _, _, _, owner, _ := newImporterFixture(t)
	target := t.TempDir()
	writeSource(t, target, "linked.jpg", []byte("linked content"))
	link := filepath.Join(t.TempDir(), "import-link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}

	result, err := imp.ImportDirectory(t.Context(), link, ingest.Options{
		Owner: owner, ConcurrentWorkers: 1, SettleInterval: time.Millisecond,
	})
	r.NoError(err)
	r.Equal(1, result.Imported, "result: %+v", result)
	r.Empty(result.Failures)
}

func TestImporterResumesReservedDocbankCreate(t *testing.T) {
	r := require.New(t)
	imp, assets, repo, contentStore, owner, ro := newImporterFixture(t)
	source := t.TempDir()
	body := []byte("interrupted import")
	sourcePath := writeSource(t, source, "resume.jpg", body)
	info, err := os.Stat(sourcePath)
	r.NoError(err)

	var storageKey string
	r.NoError(ro.QueryRowContext(t.Context(),
		`SELECT storage_key FROM owners WHERE hub=? AND user_id=?`, owner.Hub, owner.UserID,
	).Scan(&storageKey))
	assetID, fileID, operationID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	virtualPath, err := content.VirtualPath(storageKey, fileID, "resume.jpg")
	r.NoError(err)
	digest := sha256.Sum256(body)
	sha := hex.EncodeToString(digest[:])
	modified := info.ModTime()
	r.NoError(assets.ReserveImport(t.Context(), media.Asset{
		ID: assetID, Owner: owner, Type: media.TypePhoto,
		ImportedAt: time.Now().UTC(), ThumbStatus: "pending",
	}, []media.PendingContent{{
		OperationID: operationID,
		File: media.File{
			ID: fileID, AssetID: assetID, Owner: owner, Role: media.RolePrimary,
			MimeType: "image/jpeg", OriginalFilename: "resume.jpg",
			ImportSourcePath: "resume.jpg", Size: int64(len(body)),
		},
		SHA256: sha, Size: int64(len(body)), VirtualPath: virtualPath,
	}}, nil))
	_, err = contentStore.Create(t.Context(), content.CreateRequest{
		VirtualPath: virtualPath, MediaType: "image/jpeg",
		Expected: content.Identity{SHA256: sha, Size: int64(len(body))},
		Source: content.Source{
			Kind: "filesystem-import", Description: "Fotobank source import",
			Reference: "resume.jpg", ModifiedAt: &modified,
		},
		Reader: bytes.NewReader(body),
	})
	r.NoError(err)

	result, err := imp.ImportDirectory(t.Context(), source, ingest.Options{
		Owner: owner, ConcurrentWorkers: 1, SettleInterval: time.Millisecond,
	})
	r.NoError(err)
	r.Equal(1, result.Imported, "result: %+v", result)
	r.Empty(result.Failures)

	items, err := repo.List(t.Context(), media.ListFilter{Owner: owner})
	r.NoError(err)
	r.Len(items, 1)
	r.Equal(assetID, items[0].ID)
	var status string
	r.NoError(ro.QueryRowContext(t.Context(),
		`SELECT status FROM content_operations WHERE id=?`, operationID,
	).Scan(&status))
	r.Equal("applied", status)
}

func TestConcurrentImportsShareOneContentReservation(t *testing.T) {
	r := require.New(t)
	imp, _, _, _, owner, ro := newImporterFixture(t)
	source := t.TempDir()
	writeSource(t, source, "same.jpg", []byte("same content"))

	results := make([]ingest.Result, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range results {
		wg.Go(func() {
			results[i], errs[i] = imp.ImportDirectory(t.Context(), source, ingest.Options{
				Owner: owner, ConcurrentWorkers: 1, SettleInterval: time.Millisecond,
			})
		})
	}
	wg.Wait()
	for i := range results {
		r.NoError(errs[i])
		r.Empty(results[i].Failures)
	}

	var assetsCount, operationsCount, readyCount int
	r.NoError(ro.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM assets`).Scan(&assetsCount))
	r.NoError(ro.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM content_operations`).Scan(&operationsCount))
	r.NoError(ro.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM assets WHERE state='ready'`).Scan(&readyCount))
	r.Equal(1, assetsCount)
	r.Equal(1, operationsCount)
	r.Equal(1, readyCount)
}
