package cli_test

import (
	"bytes"
	"crypto/sha256"
	json "encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
	"uuid"

	"github.com/stretchr/testify/require"
	"go.kenn.io/fotobank/internal/cli"
	"go.kenn.io/fotobank/internal/content"
	"go.kenn.io/fotobank/internal/db"
	"go.kenn.io/fotobank/internal/media"
	"go.kenn.io/fotobank/internal/testutil"
	"go.kenn.io/fotobank/internal/testutil/assetfixture"
)

func TestMediaDownload(t *testing.T) {
	r := require.New(t)
	dir := t.TempDir()
	cfg := writeBasicConfig(t, dir)
	dbPath := filepath.Join(dir, "catalog.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)
	database, err := db.Open(dbPath)
	r.NoError(err)
	t.Cleanup(func() { _ = database.Close() })
	owner := testutil.SeedOwner(t, database.WriteDB(), "h", "u")
	other := testutil.SeedOwner(t, database.WriteDB(), "h", "other")
	vault, err := content.Open(t.Context(), content.Config{Root: filepath.Join(dir, "flash", "docbank")})
	r.NoError(err)
	t.Cleanup(func() { _ = vault.Close() })
	repo := media.NewRepo(database.WriteDB(), database.ReadDB())
	body := []byte("original photo bytes")
	item := assetfixture.InsertContent(t, repo, vault, body, media.Media{Owner: owner})
	hidden := assetfixture.InsertContent(t, repo, vault, []byte("hidden"), media.Media{Owner: owner, HiddenAt: new(time.Now())})
	foreign := assetfixture.InsertContent(t, repo, vault, []byte("other owner"), media.Media{Owner: other})
	fileID := uuid.New()
	sidecar := []byte("<xmp>sample metadata</xmp>")
	fileHash := fmt.Sprintf("%x", sha256.Sum256(sidecar))
	var storageKey string
	r.NoError(database.ReadDB().QueryRow(`SELECT storage_key FROM owners WHERE hub='h' AND user_id='u'`).Scan(&storageKey))
	path, err := content.VirtualPath(storageKey, fileID.String(), "photo.xmp")
	r.NoError(err)
	receipt, err := vault.Create(t.Context(), content.CreateRequest{
		VirtualPath: path, MediaType: "application/xml", Reader: bytes.NewReader(sidecar),
		Expected: content.Identity{SHA256: fileHash, Size: int64(len(sidecar))},
	})
	r.NoError(err)
	_, err = database.WriteDB().Exec(`INSERT INTO media_files
		(id,asset_id,owner_hub,owner_user_id,role,mime_type,original_filename,size,docbank_node_id,docbank_virtual_path,current_version_id,sha256)
		VALUES (?,?,'h','u','sidecar','application/xml','photo.xmp',?,?,?,?,?)`,
		fileID, item.ID, len(sidecar), receipt.Node.ID, path, receipt.Version.ID, fileHash)
	r.NoError(err)
	r.NoError(vault.Close())
	r.NoError(database.Close())
	startCheckoutServer(t, cfg, dbPath)
	destination := t.TempDir()
	t.Chdir(destination)
	canonicalDestination, err := filepath.EvalSymlinks(destination)
	r.NoError(err)
	for _, tc := range []struct {
		name string
		file []string
		body []byte
		sha  string
	}{
		{"photo.jpg", nil, body, item.SHA256},
		{"photo.xmp", []string{"--file", fileID.String()}, sidecar, fileHash},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := require.New(t)
			var out, stderr bytes.Buffer
			args := append([]string{"media", "download", item.ID, "--output", tc.name, "--json", "--config", cfg}, tc.file...)
			r.Zero(cli.RunContext(t.Context(), args, &out, &stderr), "%s", stderr.String())
			got, err := os.ReadFile(tc.name)
			r.NoError(err)
			r.Equal(tc.body, got)
			var result struct {
				MediaID string     `json:"media_id"`
				FileID  *uuid.UUID `json:"file_id"`
				Output  string     `json:"output"`
				Size    int64      `json:"size"`
				SHA256  string     `json:"sha256"`
			}
			r.NoError(json.Unmarshal(out.Bytes(), &result))
			r.Equal(item.ID, result.MediaID)
			r.Equal(filepath.Join(canonicalDestination, tc.name), result.Output)
			r.Equal(int64(len(tc.body)), result.Size)
			r.Equal(tc.sha, result.SHA256)
			if tc.file != nil {
				r.Equal(&fileID, result.FileID)
			} else {
				r.Nil(result.FileID)
			}
			// An existing output is never truncated, even when the bytes match.
			out.Reset()
			r.NotZero(cli.RunContext(t.Context(), args, &out, &stderr))
			r.Empty(out.String())
			got, err = os.ReadFile(tc.name)
			r.NoError(err)
			r.Equal(tc.body, got)
		})
	}
	for _, args := range [][]string{
		{hidden.ID}, {foreign.ID}, {uuid.New().String()},
		{item.ID, "--file", foreign.PrimaryFileID},
	} {
		var out, stderr bytes.Buffer
		command := append([]string{"media", "download"}, args...)
		command = append(command, "--output", "refused.jpg", "--json", "--config", cfg)
		r.Equal(1, cli.RunContext(t.Context(), command, &out, &stderr))
		r.Empty(out.String())
		_, err := os.Lstat("refused.jpg")
		r.ErrorIs(err, os.ErrNotExist)
	}
	files, err := os.ReadDir(destination)
	r.NoError(err)
	r.Len(files, 2, "failed downloads must leave neither output nor temporary files")
}

func TestMediaDownloadValidatesDestinationBeforeStartup(t *testing.T) {
	r := require.New(t)
	dir := t.TempDir()
	cfg := filepath.Join(dir, "missing.toml")
	existing := filepath.Join(dir, "existing.jpg")
	r.NoError(os.WriteFile(existing, []byte("keep"), 0o600))
	for _, output := range []string{existing, filepath.Join(dir, "missing", "photo.jpg")} {
		var out, stderr bytes.Buffer
		r.NotZero(cli.RunContext(t.Context(), []string{"media", "download", uuid.New().String(), "--output", output, "--config", cfg}, &out, &stderr))
		r.Empty(out.String())
		r.NotContains(stderr.String(), "missing.toml", "destination errors precede configuration/startup")
	}
}
