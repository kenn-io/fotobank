package cli

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/config"
	"go.kenn.io/fotobank/internal/content"
	"go.kenn.io/fotobank/internal/errs"
)

func TestLoadedConfigRestoreRejectsRetargetedManagedStorage(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	originalNAS := filepath.Join(tmp, "original-nas")
	currentNAS := filepath.Join(tmp, "current-nas")
	r.NoError(os.Mkdir(currentNAS, 0o700))
	nasAlias := filepath.Join(tmp, "nas")
	if err := os.Symlink(originalNAS, nasAlias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	configPath := filepath.Join(tmp, "fotobank.toml")
	r.NoError(os.WriteFile(configPath, fmt.Appendf(nil, `
[flash]
root = %q
[docbank]
root = %q
[nas]
root = %q
`, filepath.Join(tmp, "flash"), filepath.Join(tmp, "docbank"), nasAlias), 0o600))
	cfg, err := config.LoadUnchecked(configPath)
	r.NoError(err)
	r.NoError(cfg.ValidateWithOptions(config.ValidationOptions{AllowUnavailableNAS: true}))

	adapter, err := content.Open(t.Context(), contentAdapterConfig(cfg, true))
	r.NoError(err)
	t.Cleanup(func() { r.NoError(adapter.Close()) })
	body := []byte("protected photo bytes\n")
	digest := sha256.Sum256(body)
	_, err = adapter.Create(t.Context(), content.CreateRequest{
		VirtualPath: "/owners/owner/media/file/photo.jpg",
		Reader:      bytes.NewReader(body),
		Expected: content.Identity{
			SHA256: fmt.Sprintf("%x", digest),
			Size:   int64(len(body)),
		},
		MediaType: "image/jpeg",
	})
	r.NoError(err)
	repository, err := content.InitBackupRepository(filepath.Join(tmp, "repository"))
	r.NoError(err)
	snapshot, err := adapter.CreateBackup(t.Context(), repository, content.BackupOptions{})
	r.NoError(err)

	r.NoError(os.Remove(nasAlias))
	r.NoError(os.Symlink(currentNAS, nasAlias))
	_, err = adapter.RestoreBackup(t.Context(), repository, content.BackupRestoreOptions{
		SnapshotID: snapshot.ID,
		Target:     filepath.Join(nasAlias, "restore"),
		Overwrite:  true,
	})
	r.ErrorIs(err, errs.ErrBadConfiguration)
	r.NoDirExists(filepath.Join(currentNAS, "restore"))

	_, err = adapter.RestoreBackup(t.Context(), repository, content.BackupRestoreOptions{
		SnapshotID: snapshot.ID,
		Target:     filepath.Join(originalNAS, "restore"),
		Overwrite:  true,
	})
	r.ErrorIs(err, errs.ErrBadConfiguration)
	r.NoDirExists(filepath.Join(originalNAS, "restore"))
}

func TestLoadedRelativeStorageRootsOpenContentAdapter(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	t.Chdir(tmp)
	r.NoError(os.Mkdir("nas", 0o700))
	configPath := filepath.Join(tmp, "fotobank.toml")
	r.NoError(os.WriteFile(configPath, []byte(`
[flash]
root = "flash"
[docbank]
root = "docbank"
[nas]
root = "nas"
`), 0o600))
	cfg, err := config.Load(configPath)
	r.NoError(err)

	adapter, err := content.Open(t.Context(), contentAdapterConfig(cfg, false))
	r.NoError(err)
	r.NoError(adapter.Close())
}
