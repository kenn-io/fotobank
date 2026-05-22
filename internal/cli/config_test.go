package cli_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/fotobank/internal/cli"
)

func TestConfigPathPrintsResolvedPath(t *testing.T) {
	t.Setenv("FOTOBANK_CONFIG", "/tmp/example.toml")
	var out, eout bytes.Buffer
	require.Equal(t, 0, cli.Run([]string{"config", "path"}, &out, &eout))
	require.Contains(t, out.String(), "/tmp/example.toml")
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
	tmp := t.TempDir()
	p := filepath.Join(tmp, "c.toml")
	require.NoError(t, os.WriteFile(p, []byte(`[nas]
root = "/my/nas"
`), 0o600))
	t.Setenv("FOTOBANK_CONFIG", p)

	var out, eout bytes.Buffer
	require.Equal(t, 0, cli.Run([]string{"config", "read", "nas.root"}, &out, &eout))
	require.Equal(t, "/my/nas\n", out.String())
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
