package cli_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/fotobank/internal/cli"
	"go.kenn.io/fotobank/internal/content"
	"go.kenn.io/fotobank/internal/db"
)

func TestConfigPathPrintsResolvedPath(t *testing.T) {
	t.Setenv("FOTOBANK_CONFIG", "/tmp/example.toml")
	var out, eout bytes.Buffer
	require.Equal(t, 0, cli.Run([]string{"config", "path"}, &out, &eout))
	require.Contains(t, out.String(), "/tmp/example.toml")
}

func TestConfigInitCreatesLoadableExample(t *testing.T) {
	r := require.New(t)
	path := filepath.Join(t.TempDir(), "nested", "config.toml")
	var out, eout bytes.Buffer

	r.Equal(0, cli.Run([]string{"config", "init", "--config", path}, &out, &eout))
	r.Equal("created "+path+"\n", out.String())
	_, err := os.Stat(path)
	r.NoError(err)

	out.Reset()
	r.Equal(0, cli.Run([]string{"config", "validate", "--config", path}, &out, &eout))
	r.Equal("ok\n", out.String())
}

func TestConfigInitDoesNotOverwriteExistingFile(t *testing.T) {
	r := require.New(t)
	path := filepath.Join(t.TempDir(), "config.toml")
	contents := []byte("existing configuration\n")
	r.NoError(os.WriteFile(path, contents, 0o600))
	var out, eout bytes.Buffer

	r.Equal(0, cli.Run([]string{"config", "init", "--config", path}, &out, &eout))
	r.Equal("already exists "+path+"\n", out.String())
	actual, err := os.ReadFile(path)
	r.NoError(err)
	r.Equal(contents, actual)
}

func TestConfigValidateWithValidFile(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "c.toml")
	require.NoError(t, os.WriteFile(p, []byte(`[nas]
root = "/tmp/nas"
`), 0o600))
	t.Setenv("FOTOBANK_CONFIG", p)

	var out, eout bytes.Buffer
	require.Equal(t, 0, cli.Run([]string{"config", "validate"}, &out, &eout))
}

func TestConfigReadReturnsScalar(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	p := filepath.Join(tmp, "c.toml")
	nasRoot := filepath.Join(tmp, "nas")
	r.NoError(os.WriteFile(p, fmt.Appendf(nil, `[nas]
root = %q
`, nasRoot), 0o600))
	t.Setenv("FOTOBANK_CONFIG", p)

	var out, eout bytes.Buffer
	r.Equal(0, cli.Run([]string{"config", "read", "nas.root"}, &out, &eout))
	canonicalTmp, err := filepath.EvalSymlinks(tmp)
	r.NoError(err)
	r.Equal(filepath.Join(canonicalTmp, "nas")+"\n", out.String())
}

func TestConfigReadRejectsDescentIntoScalar(t *testing.T) {
	// Regression: nas.root resolves to a string scalar; the previous
	// traverse implementation tried to call NumField on it and panicked
	// when the user supplied a deeper path like nas.root.extra.
	r := require.New(t)
	tmp := t.TempDir()
	p := filepath.Join(tmp, "c.toml")
	r.NoError(os.WriteFile(p, []byte(`[nas]
root = "/my/nas"
`), 0o600))
	t.Setenv("FOTOBANK_CONFIG", p)

	var out, eout bytes.Buffer
	r.Equal(1, cli.Run([]string{"config", "read", "nas.root.extra"}, &out, &eout))
	r.Contains(eout.String(), "unknown config key")
}

func TestConfigDiagnoseReportsAvailableResources(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	flashRoot := filepath.Join(tmp, "flash")
	nasRoot := filepath.Join(tmp, "nas")
	docbankRoot := filepath.Join(tmp, "docbank")
	dbPath := filepath.Join(flashRoot, "fotobank.sqlite")
	r.NoError(os.MkdirAll(flashRoot, 0o700))
	r.NoError(os.MkdirAll(nasRoot, 0o700))

	database, err := db.Open(dbPath)
	r.NoError(err)
	r.NoError(database.Close())
	vault, err := content.Open(t.Context(), content.Config{Root: docbankRoot})
	r.NoError(err)
	r.NoError(vault.Close())

	cfgPath := filepath.Join(tmp, "config.toml")
	r.NoError(os.WriteFile(cfgPath, fmt.Appendf(nil, `
[flash]
root = %q
[docbank]
root = %q
[nas]
root = %q
`, flashRoot, docbankRoot, nasRoot), 0o600))
	t.Setenv("FOTOBANK_DB_PATH", dbPath)

	var out, eout bytes.Buffer
	r.Equal(0, cli.Run([]string{"config", "diagnose", "--config", cfgPath}, &out, &eout), eout.String())
	for _, check := range []string{
		"configuration", "sqlite", "docbank", "nas artifacts",
		"checkout boundaries", "identity", "backups",
	} {
		r.Contains(out.String(), check)
	}
	r.Contains(out.String(), "ready")
}

func TestConfigDiagnoseDoesNotInitializeMissingStorage(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	flashRoot := filepath.Join(tmp, "missing-flash")
	nasRoot := filepath.Join(tmp, "missing-nas")
	docbankRoot := filepath.Join(tmp, "missing-docbank")
	dbPath := filepath.Join(flashRoot, "fotobank.sqlite")
	cfgPath := filepath.Join(tmp, "config.toml")
	r.NoError(os.WriteFile(cfgPath, fmt.Appendf(nil, `
[flash]
root = %q
[docbank]
root = %q
[nas]
root = %q
`, flashRoot, docbankRoot, nasRoot), 0o600))
	t.Setenv("FOTOBANK_DB_PATH", dbPath)

	var out, eout bytes.Buffer
	r.Equal(1, cli.Run([]string{"config", "diagnose", "--config", cfgPath}, &out, &eout))
	r.Contains(out.String(), dbPath+":")
	r.Contains(out.String(), docbankRoot+":")
	r.Contains(out.String(), nasRoot+":")
	r.Contains(out.String(), "action:")
	r.Contains(eout.String(), "configuration diagnostics failed")
	r.NoDirExists(flashRoot)
	r.NoDirExists(nasRoot)
	r.NoDirExists(docbankRoot)
}
