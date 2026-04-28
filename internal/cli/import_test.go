package cli_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/cli"
	"github.com/wesm/fotobank/internal/db"
	"github.com/wesm/fotobank/internal/media"
	"github.com/wesm/fotobank/internal/owners"
)

// fixtureDir mirrors the ingest_test helper: tests run from the package
// dir, so the shared fixtures live two levels up.
func importFixtureDir(t *testing.T) string {
	t.Helper()
	return filepath.Join("..", "..", "testdata", "exif")
}

func copyFixture(t *testing.T, src, dst string) {
	t.Helper()
	r := require.New(t)
	in, err := os.Open(src)
	r.NoError(err)
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	r.NoError(err)
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	r.NoError(copyErr)
	r.NoError(closeErr)
}

// seedImportSource copies the named fixtures into a fresh temp dir and
// returns its path.
func seedImportSource(t *testing.T, names ...string) string {
	t.Helper()
	src := t.TempDir()
	for _, name := range names {
		copyFixture(t, filepath.Join(importFixtureDir(t), name), filepath.Join(src, name))
	}
	return src
}

func TestFotobankImportImportsMedia(t *testing.T) {
	r := require.New(t)

	tmp := t.TempDir()
	nasRoot := filepath.Join(tmp, "nas")
	r.NoError(os.MkdirAll(nasRoot, 0o700))

	cfgPath := filepath.Join(tmp, "c.toml")
	r.NoError(os.WriteFile(cfgPath, fmt.Appendf(nil, `
[nas]
root = %q
[flash]
root = %q
[identity]
mode = "stub"
[identity.stub]
hub = "local"
user_id = "alice"
storage_key = "sk"
[imports]
file_lock_path = %q
`, nasRoot, filepath.Join(tmp, "flash"),
		filepath.Join(tmp, "import.lock")), 0o600))

	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_CONFIG", cfgPath)
	t.Setenv("FOTOBANK_DB_PATH", dbPath)

	src := seedImportSource(t,
		"photo-with-timestamp.jpg",
		"photo-no-exif.jpg",
		"video.mp4",
	)

	var out, eout bytes.Buffer
	code := cli.RunContext(context.Background(),
		[]string{"import", "--config", cfgPath, src},
		&out, &eout)
	r.Equal(0, code, "stderr=%s stdout=%s", eout.String(), out.String())

	r.Contains(out.String(), "imported=3")
	r.Contains(out.String(), "duplicates=0")
	r.Contains(out.String(), "path_collisions=0")
	r.Contains(out.String(), "failures=0")
	r.Empty(eout.String())

	// Bytes landed on the NAS under the configured storage key.
	r.FileExists(filepath.Join(nasRoot, "sk", "2024", "20240615_143022_0.jpg"))
	r.FileExists(filepath.Join(nasRoot, "sk", "unknown_date", "photo-no-exif_0.jpg"))

	// Second run of the same source should see three duplicates.
	out.Reset()
	eout.Reset()
	code = cli.RunContext(context.Background(),
		[]string{"import", "--config", cfgPath, src},
		&out, &eout)
	r.Equal(0, code, "stderr=%s stdout=%s", eout.String(), out.String())
	r.Contains(out.String(), "imported=0")
	r.Contains(out.String(), "duplicates=3")
}

func TestFotobankImportRejectsMissingSource(t *testing.T) {
	r := require.New(t)

	var out, eout bytes.Buffer
	code := cli.Run([]string{"import"}, &out, &eout)
	r.Equal(2, code)
	r.Contains(eout.String(), "usage")
}

func TestFotobankImportRejectsHeaderIdentityMode(t *testing.T) {
	r := require.New(t)

	tmp := t.TempDir()
	nasRoot := filepath.Join(tmp, "nas")
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
listen_address = "127.0.0.1:8090"
`, nasRoot, filepath.Join(tmp, "flash")), 0o600))

	t.Setenv("FOTOBANK_CONFIG", cfgPath)
	t.Setenv("FOTOBANK_DB_PATH", filepath.Join(tmp, "fotobank.sqlite"))

	src := t.TempDir()

	var out, eout bytes.Buffer
	code := cli.Run([]string{"import", "--config", cfgPath, src}, &out, &eout)
	r.Equal(1, code)
	r.Contains(eout.String(), "identity.mode = stub")
}

// TestImportWiresGeoResolverPopulatesLocationLabel covers the geo
// wiring in cli/import.go: importing a GPS-bearing photo must result
// in a non-empty LocationLabel on the persisted media row. Without
// this test the wiring (NewImporter passing the gazetteer through)
// could silently regress.
func TestImportWiresGeoResolverPopulatesLocationLabel(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	nasRoot := filepath.Join(tmp, "nas")
	r.NoError(os.MkdirAll(nasRoot, 0o700))

	cfgPath := filepath.Join(tmp, "c.toml")
	r.NoError(os.WriteFile(cfgPath, fmt.Appendf(nil, `
[nas]
root = %q
[flash]
root = %q
[identity]
mode = "stub"
[identity.stub]
hub = "local"
user_id = "alice"
storage_key = "sk"
[imports]
file_lock_path = %q
`, nasRoot, filepath.Join(tmp, "flash"),
		filepath.Join(tmp, "import.lock")), 0o600))

	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_CONFIG", cfgPath)
	t.Setenv("FOTOBANK_DB_PATH", dbPath)

	src := seedImportSource(t, "photo-with-gps.jpg")

	var out, eout bytes.Buffer
	code := cli.RunContext(context.Background(),
		[]string{"import", "--config", cfgPath, src}, &out, &eout)
	r.Equal(0, code, "stderr=%s stdout=%s", eout.String(), out.String())
	r.Contains(out.String(), "imported=1")

	d, err := db.Open(dbPath)
	r.NoError(err)
	defer func() { _ = d.Close() }()
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	rows, err := repo.List(context.Background(), media.ListFilter{
		Owner: owners.Principal{Hub: "local", UserID: "alice"},
	})
	r.NoError(err)
	r.Len(rows, 1)
	r.NotEmpty(rows[0].LocationLabel,
		"geo resolver wiring missing — LocationLabel was not populated")
}
