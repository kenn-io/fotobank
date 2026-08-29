package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/config"
)

func TestResolveDBPathHonorsEnvOverride(t *testing.T) {
	want := filepath.Join(t.TempDir(), "db.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", want)
	cfg := &config.Config{}
	got, err := resolveDBPath(cfg)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(canonicalExistingPath(t, filepath.Dir(want)), filepath.Base(want)), got)
}

func TestResolveDBPathFallsBackToFlashRoot(t *testing.T) {
	t.Setenv("FOTOBANK_DB_PATH", "")
	cfg := &config.Config{}
	cfg.Flash.Root = filepath.Join(t.TempDir(), "flashroot")
	got, err := resolveDBPath(cfg)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(
		canonicalExistingPath(t, filepath.Dir(cfg.Flash.Root)),
		filepath.Base(cfg.Flash.Root), "fotobank.sqlite"), got)
}

func TestResolveDBPathCanonicalizesExistingParent(t *testing.T) {
	realParent := t.TempDir()
	aliasParent := filepath.Join(t.TempDir(), "database-parent")
	if err := os.Symlink(realParent, aliasParent); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	t.Setenv("FOTOBANK_DB_PATH", filepath.Join(aliasParent, "fotobank.sqlite"))

	got, err := resolveDBPath(&config.Config{})
	require.NoError(t, err)
	require.Equal(t, filepath.Join(canonicalExistingPath(t, realParent), "fotobank.sqlite"), got)
}

func TestLockPathForDerivesFromDBPath(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "fotobank.sqlite")
	require.Equal(t, dbPath+".lock", lockPathFor(dbPath))
}

func canonicalExistingPath(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	require.NoError(t, err)
	return resolved
}
