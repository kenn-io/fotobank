package cli_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.kenn.io/fotobank/internal/cli"
	"go.kenn.io/kit/daemon"
)

func startRecoveryServer(t *testing.T, configPath string) daemon.RuntimeRecord {
	t.Helper()
	r := require.New(t)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan int, 1)
	var diagnostics lockedBuffer
	go func() {
		done <- cli.RunContext(ctx, []string{"daemon", "run", "--recovery", "--config", configPath}, io.Discard, &diagnostics)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case code := <-done:
			r.Zero(code, "%s", diagnostics.String())
		case <-time.After(10 * time.Second):
			r.Fail("recovery daemon did not stop")
		}
	})
	r.Eventually(func() bool {
		records, err := (daemon.RuntimeStore{Dir: configPath + ".operator"}).List()
		return err == nil && len(records) == 1
	}, 3*time.Second, 20*time.Millisecond, "%s", diagnostics.String())
	records, err := (daemon.RuntimeStore{Dir: configPath + ".operator"}).List()
	r.NoError(err)
	return records[0]
}

func TestRecoveryDaemonWithoutSourceStorage(t *testing.T) {
	r := require.New(t)
	dir := t.TempDir()
	configPath := writeBackupConfig(t, dir)
	r.NoError(os.Remove(filepath.Join(dir, "nas")))
	r.NoError(os.Remove(filepath.Join(dir, "flash")))
	databasePath := filepath.Join(dir, "lost", "catalog.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", databasePath)
	record := startRecoveryServer(t, configPath)
	repository := filepath.Join(dir, "archives")
	for _, args := range [][]string{
		{"backup", "init", "--repo", repository},
		{"backup", "list", "--repo", repository},
	} {
		var output, errors bytes.Buffer
		code := cli.RunContext(t.Context(), append(args, "--config", configPath, "--json"), &output, &errors)
		r.Zero(code, "%v: %s", args, errors.String())
		r.NotEmpty(output.String())
	}
	var output, errors bytes.Buffer
	code := cli.RunContext(t.Context(), []string{"backup", "verify", "--all", "--repo", repository, "--config", configPath}, &output, &errors)
	r.NotZero(code)
	r.Contains(errors.String(), "no snapshots")
	r.Empty(output.String())
	errors.Reset()
	code = cli.RunContext(t.Context(), []string{"owners", "list", "--config", configPath}, &output, &errors)
	r.NotZero(code)
	r.Contains(errors.String(), "recovery")
	for _, tc := range []struct {
		path, token string
		status      int
	}{
		{"/api/v1/media", record.Metadata["token"], http.StatusServiceUnavailable},
		{"/api/v1/operator/backup-repository/snapshots?repository=" + url.QueryEscape(repository), "", http.StatusUnauthorized},
	} {
		request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, record.Endpoint().BaseURL()+tc.path, nil)
		r.NoError(err)
		request.Header.Set("Authorization", "Bearer "+tc.token)
		response, err := http.DefaultClient.Do(request)
		r.NoError(err)
		r.NoError(response.Body.Close())
		r.Equal(tc.status, response.StatusCode)
	}
	r.NoFileExists(databasePath)
	r.NoDirExists(filepath.Dir(databasePath))
	r.NoDirExists(filepath.Join(dir, "nas"))
	r.NoDirExists(filepath.Join(dir, "flash"))
}
