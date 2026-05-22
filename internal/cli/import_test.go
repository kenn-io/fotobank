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

	"go.kenn.io/fotobank/internal/cli"
	"go.kenn.io/fotobank/internal/db"
	"go.kenn.io/fotobank/internal/media"
	"go.kenn.io/fotobank/internal/owners"
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

// TestFotobankImportColdStartFromTildePaths is the end-to-end regression
// the existing tests didn't cover. It pins the fresh-install
// expectations the user actually faces:
//
//  1. Config contains tilde-prefixed paths ("~/fotobank", "~/.fotobank").
//     The loader expands them to $HOME/... rather than treating "~" as
//     a literal directory.
//  2. Neither the NAS root, the flash root, nor the DB parent dir
//     exist on the user's machine yet. The import command creates them.
//  3. After import, photos land in $HOME/fotobank and the SQLite DB
//     lands in $HOME/.fotobank — not in a literal "~" subdir of CWD.
//
// Three previously-shipped bugs would each fail at least one assertion
// here: the missing-tilde-expansion bug (#1), the missing-parent-dir
// MkdirAll bug (#2 — db.Open), and any future regression where the
// default flash root quietly drifts (#3 from the same incident).
func TestFotobankImportColdStartFromTildePaths(t *testing.T) {
	r := require.New(t)

	// Seed the source dir BEFORE Chdir; the seedImportSource helper
	// reads fixtures via a CWD-relative path, and we want to chdir
	// into a deliberately unrelated dir to expose any literal-"~"
	// leak. So copy fixtures out first while we still know where
	// they are.
	src := seedImportSource(t,
		"photo-with-timestamp.jpg",
		"photo-no-exif.jpg",
	)

	// Synthetic HOME so the tilde expansion hits a clean tempdir
	// instead of clobbering the developer's actual ~/fotobank.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", "")
	// Run from a deliberately unrelated CWD so any literal-"~" bug
	// would land debris there, where this assertion can catch it.
	cwd := t.TempDir()
	t.Chdir(cwd)

	// Config exclusively uses tilde forms — no absolute paths
	// anywhere except the toml file itself, which the test writes
	// outside HOME so the test never depends on something living
	// under the synthetic HOME for it to be findable.
	cfgRoot := t.TempDir()
	cfgPath := filepath.Join(cfgRoot, "c.toml")
	r.NoError(os.WriteFile(cfgPath, []byte(`
[nas]
root = "~/fotobank"
[flash]
root = "~/.fotobank"
[identity]
mode = "stub"
[identity.stub]
hub = "local"
user_id = "alice"
storage_key = "sk"
`), 0o600))

	var out, eout bytes.Buffer
	code := cli.RunContext(context.Background(),
		[]string{"import", "--config", cfgPath, src},
		&out, &eout)
	r.Equal(0, code, "stderr=%s stdout=%s", eout.String(), out.String())
	r.Contains(out.String(), "imported=2", "stdout=%s", out.String())

	// Resolved paths are visible up-front so a bad config can't sneak
	// past the user. The startup banner is part of the contract.
	r.Contains(out.String(), filepath.Join(home, "fotobank"))
	r.Contains(out.String(), filepath.Join(home, ".fotobank", "fotobank.sqlite"))

	// Photos landed under $HOME/fotobank/<storage_key>/...
	r.FileExists(filepath.Join(home, "fotobank", "sk", "2024", "20240615_143022_0.jpg"))
	r.FileExists(filepath.Join(home, "fotobank", "sk", "unknown_date", "photo-no-exif_0.jpg"))

	// DB landed under $HOME/.fotobank, not in CWD or under a literal "~".
	_, err := os.Stat(filepath.Join(home, ".fotobank", "fotobank.sqlite"))
	r.NoError(err)

	// Crucially: NO literal "~" subdirectory leaked into CWD.
	_, err = os.Stat(filepath.Join(cwd, "~"))
	r.True(os.IsNotExist(err),
		"literal '~' subdirectory leaked into CWD %q — tilde expansion regressed", cwd)
}
