package config_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/fotobank/internal/config"
	"go.kenn.io/fotobank/internal/errs"
)

func TestValidateDocbankRootSymlinkOverlap(t *testing.T) {
	tmp := t.TempDir()
	realRoot := filepath.Join(tmp, "real")
	require.NoError(t, os.Mkdir(realRoot, 0o755))
	alias := filepath.Join(tmp, "alias")
	if err := os.Symlink(realRoot, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	p := filepath.Join(tmp, "config.toml")
	require.NoError(t, os.WriteFile(p, []byte(fmt.Sprintf(`
[flash]
root = %q
[nas]
root = %q
[docbank]
root = %q
`, filepath.Join(tmp, "flash"), filepath.Join(realRoot, "missing"), filepath.Join(alias, "missing", "vault"))), 0o600))

	_, err := config.Load(p)
	require.ErrorIs(t, err, errs.ErrBadConfiguration)
}
