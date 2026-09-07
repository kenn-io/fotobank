package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.kenn.io/kit/daemon"

	"go.kenn.io/fotobank/internal/cli"
	"go.kenn.io/fotobank/internal/client"
	"go.kenn.io/fotobank/internal/content"
	"go.kenn.io/fotobank/internal/db"
	"go.kenn.io/fotobank/internal/httpapi"
	"go.kenn.io/fotobank/internal/media"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/version"
)

func TestLiveImportPartialResultsAndAuthorization(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	configPath := writeBasicConfig(t, tmp)
	dbPath := filepath.Join(tmp, "catalog.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)
	record := startCheckoutServer(t, configPath, dbPath)
	source := seedImportSource(t, "photo-no-exif.jpg")
	r.NoError(os.WriteFile(filepath.Join(source, "orphan.xmp"), []byte("sidecar"), 0o600))
	for run := range 2 {
		var stdout, stderr bytes.Buffer
		code := cli.RunContext(t.Context(), []string{"import", source, "--config", configPath, "--json"}, &stdout, &stderr)
		r.Equal(1, code)
		var result httpapi.ImportResult
		r.NoError(json.Unmarshal(stdout.Bytes(), &result))
		r.Equal(1-run, result.Imported)
		r.Equal(run, result.Duplicates)
		r.Len(result.Failures, 1)
		r.NotEmpty(result.Error)
		r.Contains(stderr.String(), "Discovered")
	}
	for _, tc := range []struct {
		base, token, user string
		status            int
	}{
		{record.Endpoint().BaseURL(), record.Metadata["token"], "u", http.StatusOK},
		{record.Endpoint().BaseURL(), "", "u", http.StatusUnauthorized},
		{record.Endpoint().BaseURL(), record.Metadata["token"], "other", http.StatusForbidden},
		{record.Metadata["web_url"], record.Metadata["token"], "u", http.StatusForbidden},
	} {
		// API callers may omit workers and wait to use the daemon defaults.
		body, err := json.Marshal(map[string]string{"hub": "h", "user_id": tc.user, "source": source})
		r.NoError(err)
		request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, tc.base+"/api/v1/operator/imports", bytes.NewReader(body))
		r.NoError(err)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+tc.token)
		response, err := http.DefaultClient.Do(request)
		r.NoError(err)
		_, err = io.Copy(io.Discard, response.Body)
		r.NoError(err)
		r.NoError(response.Body.Close())
		r.Equal(tc.status, response.StatusCode)
		if tc.status == http.StatusOK {
			r.Equal("application/x-ndjson", response.Header.Get("Content-Type"))
		}
	}
}

func TestLiveImportDisconnectReleasesImportLock(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	configPath := writeBasicConfig(t, tmp)
	dbPath := filepath.Join(tmp, "catalog.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)
	startCheckoutServer(t, configPath, dbPath)
	source := seedImportSource(t, "photo-no-exif.jpg")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	input := httpapi.ImportRequest{Hub: "h", UserID: "u", Source: source, Workers: 1, Wait: "0s"}
	_, err := client.Import(ctx, dbPath, version.Short, input, func(httpapi.ImportProgress) { cancel() })
	r.Error(err)
	// A new request must acquire the released lock, without stopping the daemon.
	input.Wait = "5s"
	retryCtx, retryCancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer retryCancel()
	result, err := client.Import(retryCtx, dbPath, version.Short, input, nil)
	r.NoError(err)
	r.Equal(1, result.Imported+result.Duplicates)
}

func TestLiveImportShutdownReportsInterruptedResult(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	configPath := writeBasicConfig(t, tmp)
	dbPath := filepath.Join(tmp, "catalog.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)
	record := startCheckoutServer(t, configPath, dbPath)
	source := seedImportSource(t, "photo-no-exif.jpg")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	stopped := false
	_, err := client.Import(ctx, dbPath, version.Short,
		httpapi.ImportRequest{Hub: "h", UserID: "u", Source: source, Wait: "0s"},
		func(httpapi.ImportProgress) {
			if stopped {
				return
			}
			stopped = true
			request, err := http.NewRequestWithContext(ctx, http.MethodPost, record.Endpoint().BaseURL()+"/api/v1/operator/daemon/stop", nil)
			r.NoError(err)
			request.Header.Set("Authorization", "Bearer "+record.Metadata["token"])
			response, err := http.DefaultClient.Do(request)
			r.NoError(err)
			r.NoError(response.Body.Close())
			r.Equal(http.StatusNoContent, response.StatusCode)
		})
	r.True(stopped)
	r.Error(err, "an interrupted stream must not report success")
	recordPath, err := (daemon.RuntimeStore{Dir: dbPath + ".operator"}).Path(record.PID)
	r.NoError(err)
	r.Eventually(func() bool { _, err := os.Stat(recordPath); return os.IsNotExist(err) }, 5*time.Second, 10*time.Millisecond)
}

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

// runLiveImport owns a temporary server for tests that inspect or reopen the
// vault after importing. The CLI still exercises the real operator connection.
func runLiveImport(t *testing.T, ctx context.Context, configPath, dbPath string, args []string, out, errOut io.Writer) int {
	t.Helper()
	code := 1
	t.Run("live import", func(t *testing.T) {
		t.Setenv("FOTOBANK_TEST_LISTEN_ADDR_SINK", "")
		startCheckoutServer(t, configPath, dbPath)
		code = cli.RunContext(ctx, args, out, errOut)
	})
	return code
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
	code := runLiveImport(t, t.Context(), cfgPath, dbPath,
		[]string{"import", "--config", cfgPath, src},
		&out, &eout)
	r.Equal(0, code, "stderr=%s stdout=%s", eout.String(), out.String())

	r.Contains(out.String(), "imported=3")
	r.Contains(out.String(), "duplicates=0")
	r.Contains(out.String(), "conflicts=0")
	r.Contains(out.String(), "failures=0")
	r.Empty(eout.String())

	d, err := db.Open(dbPath)
	r.NoError(err)
	storedOwner, err := owners.NewRepo(d.WriteDB(), d.ReadDB()).GetByPrincipal(
		t.Context(), owners.Principal{Hub: "local", UserID: "alice"})
	r.NoError(err)
	_, err = uuid.Parse(storedOwner.StorageKey)
	r.NoError(err)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	rows, err := repo.List(context.Background(), media.ListFilter{Owner: storedOwner.Principal})
	r.NoError(err)
	r.Len(rows, 3)
	contentStore, err := content.Open(context.Background(), content.Config{
		Root: filepath.Join(tmp, "flash", "docbank"),
	})
	r.NoError(err)
	for _, row := range rows {
		opened, err := contentStore.OpenVersion(context.Background(), row.CurrentVersionID)
		r.NoError(err)
		_, err = io.Copy(io.Discard, opened.Reader)
		r.NoError(err)
		r.NoError(opened.Reader.Verify())
		r.NoError(opened.Reader.Close())
	}
	r.NoError(contentStore.Close())
	r.NoError(d.Close())

	// Second run of the same source should see three duplicates.
	out.Reset()
	eout.Reset()
	code = runLiveImport(t, t.Context(), cfgPath, dbPath,
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

func TestImportValidatesArgumentsBeforeStartingDaemon(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	configPath := writeBasicConfig(t, tmp)
	dbPath := filepath.Join(tmp, "catalog.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)
	for _, args := range [][]string{
		{"import", ""},
		{"import", tmp, "--workers", "-1"},
		{"import", tmp, "--wait=-1s"},
	} {
		var stdout, stderr bytes.Buffer
		code := cli.RunContext(t.Context(), append(args, "--config", configPath, "--json"), &stdout, &stderr)
		r.NotZero(code)
		var result httpapi.ImportResult
		r.NoError(json.Unmarshal(stdout.Bytes(), &result))
		r.NotEmpty(result.Error)
		_, err := os.Stat(dbPath + ".operator")
		r.ErrorIs(err, os.ErrNotExist, "invalid arguments must not reach automatic launch")
	}
}

func TestFotobankImportRejectsManagedStorageSource(t *testing.T) {
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
[imports]
file_lock_path = %q
`, nasRoot, filepath.Join(tmp, "flash"), filepath.Join(tmp, "import.lock")), 0o600))
	t.Setenv("FOTOBANK_DB_PATH", filepath.Join(tmp, "fotobank.sqlite"))

	var out, eout bytes.Buffer
	code := runLiveImport(t, t.Context(), cfgPath, filepath.Join(tmp, "fotobank.sqlite"),
		[]string{"import", "--config", cfgPath, nasRoot}, &out, &eout)
	r.Equal(1, code)
	r.Contains(eout.String(), "import root overlaps managed storage")
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
storage_key = "550e8400-e29b-41d4-a716-446655440000"
[imports]
file_lock_path = %q
`, nasRoot, filepath.Join(tmp, "flash"),
		filepath.Join(tmp, "import.lock")), 0o600))

	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_CONFIG", cfgPath)
	t.Setenv("FOTOBANK_DB_PATH", dbPath)

	src := seedImportSource(t, "photo-with-gps.jpg")

	var out, eout bytes.Buffer
	code := runLiveImport(t, t.Context(), cfgPath, dbPath,
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

// Import through the daemon with tilde-configured roots and a different cwd.
// NAS must exist; the daemon creates its local vault and database directories.
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
	t.Setenv("USERPROFILE", home)
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
storage_key = "550e8400-e29b-41d4-a716-446655440000"
`), 0o600))
	r.NoError(os.MkdirAll(filepath.Join(home, "fotobank"), 0o700))

	var out, eout bytes.Buffer
	code := runLiveImport(t, t.Context(), cfgPath, filepath.Join(home, ".fotobank", "fotobank.sqlite"),
		[]string{"import", "--config", cfgPath, src},
		&out, &eout)
	r.Equal(0, code, "stderr=%s stdout=%s", eout.String(), out.String())
	r.Contains(out.String(), "imported=2", "stdout=%s", out.String())

	// The vault and DB landed under $HOME/.fotobank, not in CWD or under a literal "~".
	_, err := os.Stat(filepath.Join(home, ".fotobank", "docbank"))
	r.NoError(err)
	_, err = os.Stat(filepath.Join(home, ".fotobank", "fotobank.sqlite"))
	r.NoError(err)

	// Crucially: NO literal "~" subdirectory leaked into CWD.
	_, err = os.Stat(filepath.Join(cwd, "~"))
	r.True(os.IsNotExist(err),
		"literal '~' subdirectory leaked into CWD %q — tilde expansion regressed", cwd)
}
