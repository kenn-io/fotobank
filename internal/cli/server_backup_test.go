package cli

import (
	"github.com/stretchr/testify/require"
	"go.kenn.io/fotobank/internal/config"
	"go.kenn.io/fotobank/internal/content"
	"path/filepath"
	"testing"
)

func TestBackupReadinessRequiresInitializedRepository(t *testing.T) {
	r := require.New(t)
	root := filepath.Join(t.TempDir(), "repository")
	cfg := &config.Config{Backup: config.Backup{Enabled: true, Repository: root}}
	check := obsBackupCheck(cfg)
	r.Error(check.Fn(t.Context()))
	r.NoDirExists(root)
	_, err := content.InitBackupRepository(root)
	r.NoError(err)
	r.NoError(check.Fn(t.Context()))
}
