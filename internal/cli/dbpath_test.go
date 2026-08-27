package cli

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/config"
)

func TestResolveDBPathHonorsEnvOverride(t *testing.T) {
	want := filepath.Join(t.TempDir(), "db.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", want)
	cfg := &config.Config{}
	require.Equal(t, want, resolveDBPath(cfg))
}

func TestResolveDBPathFallsBackToFlashRoot(t *testing.T) {
	t.Setenv("FOTOBANK_DB_PATH", "")
	cfg := &config.Config{}
	cfg.Flash.Root = filepath.Join(t.TempDir(), "flashroot")
	require.Equal(t, filepath.Join(cfg.Flash.Root, "fotobank.sqlite"), resolveDBPath(cfg))
}

func TestLockPathForDerivesFromDBPath(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "fotobank.sqlite")
	require.Equal(t, dbPath+".lock", lockPathFor(dbPath))
}
