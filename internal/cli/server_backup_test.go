package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/config"
	"go.kenn.io/fotobank/internal/errs"
)

func TestBackupReadyCheckRecoversWhenDefaultNASAppears(t *testing.T) {
	r := require.New(t)
	nasRoot := filepath.Join(t.TempDir(), "nas")
	cfg := &config.Config{
		NAS:    config.NAS{Root: nasRoot},
		Backup: config.Backup{Enabled: true},
	}
	dir := backupDirFor(cfg)
	check := obsBackupCheck(cfg, dir)

	err := check.Fn(t.Context())
	r.ErrorIs(err, errs.ErrContentUnavailable)
	r.NoDirExists(nasRoot)

	r.NoError(os.Mkdir(nasRoot, 0o700))
	r.NoError(check.Fn(t.Context()))
	r.DirExists(dir)

	legacyProbe := filepath.Join(dir, ".readyz-probe")
	r.NoError(os.WriteFile(legacyProbe, []byte("keep"), 0o600))
	r.NoError(check.Fn(t.Context()))
	body, err := os.ReadFile(legacyProbe)
	r.NoError(err)
	r.Equal([]byte("keep"), body)
}
