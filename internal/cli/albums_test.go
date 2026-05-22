package cli_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/cli"
)

// runAlbumsCLI invokes the CLI with background context and returns
// stdout, stderr, and the exit code.
func runAlbumsCLI(args ...string) (string, string, int) {
	var stdout, stderr bytes.Buffer
	code := cli.RunContext(context.Background(), args, &stdout, &stderr)
	return stdout.String(), stderr.String(), code
}

// bootstrapAlbumsOwner runs a prior CLI command to ensure the DB exists
// with the stub owner row seeded. Uses `owners add` for the side effect
// of db.Open + migrations + owner insert.
func bootstrapAlbumsOwner(t *testing.T) {
	t.Helper()
	_, stderr, code := runAlbumsCLI("owners", "add",
		"--hub", "h", "--user-id", "u", "--storage-key", "sk",
	)
	require.Equal(t, 0, code, "bootstrap owner must succeed: %s", stderr)
}

func TestCLIAlbumsCreateHappyPath(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeBasicConfig(t, tmp)
	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_CONFIG", cfgPath)
	t.Setenv("FOTOBANK_DB_PATH", dbPath)

	bootstrapAlbumsOwner(t)

	stdout, stderr, code := runAlbumsCLI("albums", "create", "Trip", "--config", cfgPath)
	r.Equal(0, code, "stderr=%s", stderr)
	r.NotEmpty(stdout)
	r.Contains(stdout, "Trip")
}

func TestCLIAlbumsListShowsCreatedAlbum(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeBasicConfig(t, tmp)
	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_CONFIG", cfgPath)
	t.Setenv("FOTOBANK_DB_PATH", dbPath)

	bootstrapAlbumsOwner(t)
	_, stderr, code := runAlbumsCLI("albums", "create", "Trip", "--config", cfgPath)
	r.Equal(0, code, "stderr=%s", stderr)

	stdout, stderr, code := runAlbumsCLI("albums", "list", "--config", cfgPath)
	r.Equal(0, code, "stderr=%s", stderr)
	r.Contains(stdout, "Trip")
}

func TestCLIAlbumsRenameUpdatesList(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeBasicConfig(t, tmp)
	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_CONFIG", cfgPath)
	t.Setenv("FOTOBANK_DB_PATH", dbPath)
	bootstrapAlbumsOwner(t)

	stdout, stderr, code := runAlbumsCLI("albums", "create", "Old", "--config", cfgPath)
	r.Equal(0, code, "stderr=%s", stderr)
	fields := strings.Fields(stdout)
	r.NotEmpty(fields)
	id := fields[0]

	_, stderr, code = runAlbumsCLI("albums", "rename", id, "New", "--config", cfgPath)
	r.Equal(0, code, "stderr=%s", stderr)

	listOut, stderr, code := runAlbumsCLI("albums", "list", "--config", cfgPath)
	r.Equal(0, code, "stderr=%s", stderr)
	r.Contains(listOut, "New")
	r.NotContains(listOut, "Old")
}

func TestCLIAlbumsDeleteRemovesFromList(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeBasicConfig(t, tmp)
	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_CONFIG", cfgPath)
	t.Setenv("FOTOBANK_DB_PATH", dbPath)
	bootstrapAlbumsOwner(t)

	stdout, stderr, code := runAlbumsCLI("albums", "create", "Trip", "--config", cfgPath)
	r.Equal(0, code, "stderr=%s", stderr)
	fields := strings.Fields(stdout)
	r.NotEmpty(fields)
	id := fields[0]

	_, stderr, code = runAlbumsCLI("albums", "delete", id, "--config", cfgPath)
	r.Equal(0, code, "stderr=%s", stderr)

	listOut, _, code := runAlbumsCLI("albums", "list", "--config", cfgPath)
	r.Equal(0, code)
	r.NotContains(listOut, id)
}

func TestCLIAlbumsRejectsNonStubIdentity(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	flashRoot := filepath.Join(tmp, "flash")
	nasRoot := filepath.Join(tmp, "nas")
	r.NoError(os.MkdirAll(flashRoot, 0o700))
	r.NoError(os.MkdirAll(nasRoot, 0o700))
	cfgPath := filepath.Join(tmp, "c.toml")
	r.NoError(os.WriteFile(cfgPath, fmt.Appendf(nil, `
[nas]
root = %q
[flash]
root = %q
[identity]
mode = "header"
[http]
listen_address = "127.0.0.1:9999"
`, nasRoot, flashRoot), 0o600))
	t.Setenv("FOTOBANK_CONFIG", cfgPath)
	t.Setenv("FOTOBANK_DB_PATH", filepath.Join(flashRoot, "fotobank.sqlite"))

	_, stderr, code := runAlbumsCLI("albums", "create", "Trip", "--config", cfgPath)
	r.Equal(1, code)
	r.Contains(stderr, "identity.mode = stub")
}
