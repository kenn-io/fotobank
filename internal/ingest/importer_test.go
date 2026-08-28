package ingest_test

import (
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

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
	r.Equal(1, result.Imported)
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
