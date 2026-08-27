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

func TestValidateDocbankRootFlashCacheSymlinkOverlap(t *testing.T) {
	for _, cacheDir := range []string{"originals", "thumbs"} {
		t.Run(cacheDir, func(t *testing.T) {
			require := require.New(t)
			tmp := t.TempDir()
			vaultRoot := filepath.Join(tmp, "vault")
			flashRoot := filepath.Join(tmp, "flash")
			require.NoError(os.Mkdir(vaultRoot, 0o700))
			require.NoError(os.Mkdir(flashRoot, 0o700))
			if err := os.Symlink(vaultRoot, filepath.Join(flashRoot, cacheDir)); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}

			p := filepath.Join(tmp, "config.toml")
			require.NoError(os.WriteFile(p, []byte(fmt.Sprintf(`
[flash]
root = %q
[nas]
root = %q
[docbank]
root = %q
`, flashRoot, filepath.Join(tmp, "nas"), vaultRoot)), 0o600))

			_, err := config.Load(p)
			require.ErrorIs(err, errs.ErrBadConfiguration)
		})
	}
}

func TestValidateRejectsFlashRootInsideDocbankWithExternalCacheSymlinks(t *testing.T) {
	require := require.New(t)
	tmp := t.TempDir()
	docbankRoot := filepath.Join(tmp, "vault")
	flashRoot := filepath.Join(docbankRoot, "flash")
	externalCacheRoot := filepath.Join(tmp, "external-cache")
	require.NoError(os.MkdirAll(flashRoot, 0o700))
	for _, cacheDir := range []string{"originals", "thumbs"} {
		target := filepath.Join(externalCacheRoot, cacheDir)
		require.NoError(os.MkdirAll(target, 0o700))
		if err := os.Symlink(target, filepath.Join(flashRoot, cacheDir)); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
	}

	p := filepath.Join(tmp, "config.toml")
	require.NoError(os.WriteFile(p, []byte(fmt.Sprintf(`
[flash]
root = %q
[nas]
root = %q
[docbank]
root = %q
`, flashRoot, filepath.Join(tmp, "nas"), docbankRoot)), 0o600))

	_, err := config.Load(p)
	require.ErrorIs(err, errs.ErrBadConfiguration)
}

func TestValidateRejectsDanglingDocbankSymlinkAncestor(t *testing.T) {
	tmp := t.TempDir()
	nasRoot := filepath.Join(tmp, "future-storage")
	alias := filepath.Join(tmp, "vault-alias")
	if err := os.Symlink(nasRoot, alias); err != nil {
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
`, nasRoot, nasRoot, filepath.Join(alias, "vault"))), 0o600))

	_, err := config.Load(p)
	require.ErrorIs(t, err, errs.ErrBadConfiguration)
}

func TestValidateDocbankRootSymlinkParentOverlap(t *testing.T) {
	require := require.New(t)
	tmp := t.TempDir()
	flashRoot := filepath.Join(tmp, "flash")
	cacheChild := filepath.Join(flashRoot, "originals", "child")
	require.NoError(os.MkdirAll(cacheChild, 0o700))
	aliasesRoot := filepath.Join(tmp, "aliases")
	require.NoError(os.Mkdir(aliasesRoot, 0o700))
	alias := filepath.Join(aliasesRoot, "vault")
	if err := os.Symlink(cacheChild, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	p := filepath.Join(tmp, "config.toml")
	require.NoError(os.WriteFile(p, []byte(fmt.Sprintf(`
[flash]
root = %q
[nas]
root = %q
[docbank]
root = %q
`, flashRoot, filepath.Join(tmp, "nas"), alias+string(os.PathSeparator)+"..")), 0o600))

	_, err := config.Load(p)
	require.ErrorIs(err, errs.ErrBadConfiguration)
}

func TestLoadReturnsCanonicalDocbankRootAfterSymlinkParent(t *testing.T) {
	require := require.New(t)
	tmp := t.TempDir()
	realRoot := filepath.Join(tmp, "real-vault")
	realChild := filepath.Join(realRoot, "child")
	require.NoError(os.MkdirAll(realChild, 0o700))
	aliasesRoot := filepath.Join(tmp, "aliases")
	require.NoError(os.Mkdir(aliasesRoot, 0o700))
	alias := filepath.Join(aliasesRoot, "vault")
	if err := os.Symlink(realChild, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	p := filepath.Join(tmp, "config.toml")
	require.NoError(os.WriteFile(p, []byte(fmt.Sprintf(`
[flash]
root = %q
[nas]
root = %q
[docbank]
root = %q
`, filepath.Join(tmp, "flash"), filepath.Join(tmp, "nas"),
		alias+string(os.PathSeparator)+"..")), 0o600))

	cfg, err := config.Load(p)
	require.NoError(err)
	want, err := filepath.EvalSymlinks(realRoot)
	require.NoError(err)
	require.Equal(want, cfg.Docbank.Root)
}
