package cli

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/config"
)

func TestResolveDBPathHonorsEnvOverride(t *testing.T) {
	t.Setenv("FOTOBANK_DB_PATH", "/custom/path/db.sqlite")
	cfg := &config.Config{}
	require.Equal(t, "/custom/path/db.sqlite", resolveDBPath(cfg))
}

func TestResolveDBPathFallsBackToFlashRoot(t *testing.T) {
	t.Setenv("FOTOBANK_DB_PATH", "")
	cfg := &config.Config{}
	cfg.Flash.Root = "/tmp/flashroot"
	require.Equal(t, filepath.Join("/tmp/flashroot", "fotobank.sqlite"), resolveDBPath(cfg))
}

func TestLockPathForDerivesFromDBPath(t *testing.T) {
	require.Equal(t, "/var/db/fotobank.sqlite.lock", lockPathFor("/var/db/fotobank.sqlite"))
	require.Equal(t, "/custom/x.sqlite.lock", lockPathFor("/custom/x.sqlite"))
}
