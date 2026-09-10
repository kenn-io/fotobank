package cli_test

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/album"
	"go.kenn.io/fotobank/internal/cli"
	"go.kenn.io/fotobank/internal/db"
	"go.kenn.io/fotobank/internal/httpapi"
	"go.kenn.io/fotobank/internal/owners"
)

// runAlbumsCLI invokes the CLI with background context and returns
// stdout, stderr, and the exit code.
func runAlbumsCLI(args ...string) (string, string, int) {
	var stdout, stderr bytes.Buffer
	code := cli.RunContext(context.Background(), args, &stdout, &stderr)
	return stdout.String(), stderr.String(), code
}

func TestCLIAlbumsCreateHappyPath(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeBasicConfig(t, tmp)
	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_CONFIG", cfgPath)
	t.Setenv("FOTOBANK_DB_PATH", dbPath)

	startCheckoutServer(t, cfgPath, dbPath)

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

	startCheckoutServer(t, cfgPath, dbPath)
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
	startCheckoutServer(t, cfgPath, dbPath)

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
	startCheckoutServer(t, cfgPath, dbPath)

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
	r.NoFileExists(filepath.Join(flashRoot, "fotobank.sqlite"))
}

func TestCLIAlbumsMembershipAndScope(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeBasicConfig(t, tmp)
	dbPath := filepath.Join(tmp, "catalog.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)
	first := seedReadyRow(t, dbPath)
	second := seedReadyRow(t, dbPath)
	hidden := seedReadyRow(t, dbPath)
	database, err := db.Open(dbPath)
	r.NoError(err)
	defer database.Close()
	_, err = database.WriteDB().ExecContext(t.Context(), `UPDATE assets SET hidden_at = ? WHERE id = ?`, time.Now().UTC(), hidden.ID)
	r.NoError(err)
	other := owners.Principal{Hub: "other", UserID: "user-b"}
	r.NoError(owners.NewRepo(database.WriteDB(), database.ReadDB()).Insert(t.Context(), owners.Owner{
		Principal: other, StorageKey: "550e8400-e29b-41d4-a716-446655440002", CreatedAt: time.Now().UTC(),
	}))
	otherAlbum := album.Album{ID: "550e8400-e29b-41d4-a716-446655440003", Owner: other, Name: "Private album", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	r.NoError(album.NewRepo(database.WriteDB(), database.ReadDB()).Insert(t.Context(), otherAlbum))
	r.NoError(database.Close())
	startCheckoutServer(t, cfgPath, dbPath)

	stdout, stderr, code := runAlbumsCLI("albums", "create", "Trip", "--config", cfgPath)
	r.Zero(code, "%s", stderr)
	id := strings.Fields(stdout)[0]
	stdout, stderr, code = runAlbumsCLI("albums", "add", id, first.ID, second.ID, "--config", cfgPath)
	r.Zero(code, "%s", stderr)
	r.Contains(stdout, "added 2, already present 0")
	stdout, stderr, code = runAlbumsCLI("albums", "add", id, first.ID, "--config", cfgPath)
	r.Zero(code, "%s", stderr)
	r.Contains(stdout, "added 0, already present 1")
	stdout, stderr, code = runAlbumsCLI("albums", "show", id, "--sort-by", "imported", "--sort-asc", "--limit", "1", "--offset", "1", "--config", cfgPath)
	r.Zero(code, "%s", stderr)
	r.Contains(stdout, "items:      2")
	r.Contains(stdout, "  "+second.ID)
	r.NotContains(stdout, "  "+first.ID)

	for _, args := range [][]string{
		{"add", id, hidden.ID},
		{"show", otherAlbum.ID},
		{"rename", otherAlbum.ID, "Not yours"},
		{"delete", otherAlbum.ID},
	} {
		_, stderr, code = runAlbumsCLI(append(append([]string{"albums"}, args...), "--config", cfgPath)...)
		r.Equal(1, code, "%v: %s", args, stderr)
		r.Contains(stderr, "404")
	}
	_, stderr, code = runAlbumsCLI("albums", "remove", id, first.ID, "--config", cfgPath)
	r.Zero(code, "%s", stderr)
	stdout, stderr, code = runAlbumsCLI("albums", "show", id, "--config", cfgPath)
	r.Zero(code, "%s", stderr)
	r.Contains(stdout, "items:      1")
	r.NotContains(stdout, first.ID)
	stdout, stderr, code = runAlbumsCLI("albums", "list", "--json", "--config", cfgPath)
	r.Zero(code, "%s", stderr)
	var page httpapi.AlbumListResult
	r.NoError(json.Unmarshal([]byte(stdout), &page))
	r.Len(page.Items, 1)
	r.Equal(id, page.Items[0].ID)
	r.Equal(1, page.Items[0].ItemCount)
	r.Nil(page.NextOffset)
	_, stderr, code = runAlbumsCLI("albums", "create", "Another", "--config", cfgPath)
	r.Zero(code, "%s", stderr)
	stdout, stderr, code = runAlbumsCLI("albums", "list", "--json", "--limit", "1", "--config", cfgPath)
	r.Zero(code, "%s", stderr)
	r.NoError(json.Unmarshal([]byte(stdout), &page))
	r.Len(page.Items, 1)
	r.NotNil(page.NextOffset)
	r.Equal(1, *page.NextOffset)
}

func TestCLIAlbumsInvalidArgumentsBeforeStartup(t *testing.T) {
	for _, args := range [][]string{
		{"create", " "},
		{"rename", "550e8400-e29b-41d4-a716-446655440000", strings.Repeat("a", 201)},
		{"delete", "not-an-id"},
		{"show", "550e8400-e29b-41d4-a716-446655440000", "--sort-by", "unknown"},
		{"add", "550e8400-e29b-41d4-a716-446655440000", "not-an-id"},
	} {
		t.Run(args[0], func(t *testing.T) {
			r := require.New(t)
			tmp := t.TempDir()
			cfgPath := writeBasicConfig(t, tmp)
			dbPath := filepath.Join(tmp, "catalog.sqlite")
			t.Setenv("FOTOBANK_DB_PATH", dbPath)
			_, stderr, code := runAlbumsCLI(append(append([]string{"albums"}, args...), "--config", cfgPath)...)
			r.Equal(2, code, "%s", stderr)
			r.NoFileExists(dbPath)
			r.NoDirExists(cfgPath + ".operator")
		})
	}
}
