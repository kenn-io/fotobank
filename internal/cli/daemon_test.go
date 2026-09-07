package cli_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDaemonLifecycle(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	configPath := writeBasicConfig(t, tmp)
	dbPath := filepath.Join(tmp, "catalog.sqlite")
	// Reserve an available control port, then require the daemon to use it.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	r.NoError(err)
	controlAddress := listener.Addr().String()
	r.NoError(listener.Close())
	file, err := os.OpenFile(configPath, os.O_APPEND|os.O_WRONLY, 0)
	r.NoError(err)
	_, err = file.WriteString("\n[http]\nlisten_address = '127.0.0.1:0'\n[observability]\nadmin_listen = '127.0.0.1:0'\n[daemon]\nlisten_address = '" + controlAddress + "'\n")
	r.NoError(err)
	r.NoError(file.Close())
	binary := filepath.Join(tmp, "fotobank")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.CommandContext(t.Context(), "go", "build", "-tags", "sqlite_fts5", "-o", binary, "../../cmd/fotobank")
	output, err := build.CombinedOutput()
	r.NoError(err, "%s", output)
	run := func(args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, binary, append(args, "--config", configPath)...)
		command.Env = append(os.Environ(), "FOTOBANK_DB_PATH="+dbPath)
		return command.CombinedOutput()
	}
	// Cleanup is independent of the test context, including assertion failures.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, binary, "daemon", "stop", "--config", configPath)
		command.Env = append(os.Environ(), "FOTOBANK_DB_PATH="+dbPath)
		output, err := command.CombinedOutput()
		r.NoError(err, "%s", output)
	})
	output, err = run("daemon", "status", "--json")
	r.NoError(err, "%s", output)
	r.JSONEq(`{"running":false}`, string(output))
	_, err = os.Stat(dbPath)
	r.ErrorIs(err, os.ErrNotExist)
	// A real data command starts the daemon, then gets its result over HTTP.
	output, err = run("checkout", "list", "--json")
	r.NoError(err, "%s", output)
	r.JSONEq("[]", string(output))
	output, err = run("daemon", "status", "--json")
	r.NoError(err, "%s", output)
	var first struct {
		Running bool   `json:"running"`
		PID     int    `json:"pid"`
		Address string `json:"address"`
		WebURL  string `json:"web_url"`
	}
	r.NoError(json.Unmarshal(output, &first))
	r.True(first.Running)
	r.Positive(first.PID)
	r.Equal(controlAddress, first.Address)
	r.NotEmpty(first.WebURL)
	response, err := http.Get(first.WebURL + "/api/v1/healthz")
	r.NoError(err)
	r.NoError(response.Body.Close())
	r.Equal(http.StatusOK, response.StatusCode)
	response, err = http.Post(first.WebURL+"/api/v1/operator/daemon/stop", "application/json", nil)
	r.NoError(err)
	r.NoError(response.Body.Close())
	r.Equal(http.StatusForbidden, response.StatusCode)
	output, err = run("daemon", "start")
	r.NoError(err, "%s", output)
	r.Contains(string(output), first.WebURL)
	output, err = run("daemon", "status", "--json")
	r.NoError(err, "%s", output)
	var again struct {
		PID int `json:"pid"`
	}
	r.NoError(json.Unmarshal(output, &again))
	r.Equal(first.PID, again.PID)
	output, err = run("daemon", "restart")
	r.NoError(err, "%s", output)
	restarted := string(output)
	output, err = run("daemon", "status", "--json")
	r.NoError(err, "%s", output)
	r.NoError(json.Unmarshal(output, &first))
	r.NotEqual(again.PID, first.PID)
	r.Contains(restarted, first.WebURL)
	// A newer CLI replaces the old process through the same start path.
	previousPID := first.PID
	newBinary := filepath.Join(tmp, "fotobank-new")
	if runtime.GOOS == "windows" {
		newBinary += ".exe"
	}
	build = exec.CommandContext(t.Context(), "go", "build", "-tags", "sqlite_fts5", "-ldflags", "-X main.vVersion=lifecycle-next", "-o", newBinary, "../../cmd/fotobank")
	output, err = build.CombinedOutput()
	r.NoError(err, "%s", output)
	binary = newBinary
	output, err = run("daemon", "start")
	r.NoError(err, "%s", output)
	output, err = run("daemon", "status", "--json")
	r.NoError(err, "%s", output)
	r.NoError(json.Unmarshal(output, &first))
	r.NotEqual(previousPID, first.PID)
	output, err = run("daemon", "stop")
	r.NoError(err, "%s", output)
	output, err = run("daemon", "status", "--json")
	r.NoError(err, "%s", output)
	r.JSONEq(`{"running":false}`, string(output))
	// Header deployments still support host lifecycle, and advertise the
	// configured browser URL rather than the internal proxy bind address.
	configBytes, err := os.ReadFile(configPath)
	r.NoError(err)
	configured := strings.Replace(string(configBytes), `mode = "stub"`, `mode = "header"`, 1)
	configured = strings.Replace(configured, "[http]", "[http]\nbase_url = 'https://photos.example.test'", 1)
	r.NoError(os.WriteFile(configPath, []byte(configured), 0o600))
	output, err = run("daemon", "start")
	r.NoError(err, "%s", output)
	r.Contains(string(output), "https://photos.example.test")
	output, err = run("daemon", "stop")
	r.NoError(err, "%s", output)
	// Report the child's configuration error, not just an opaque timeout.
	configured = strings.Replace(configured, `mode = "header"`, `mode = "invalid"`, 1)
	configured = strings.Replace(configured, "[daemon]", "[daemon]\nstart_timeout = '30s'", 1)
	r.NoError(os.WriteFile(configPath, []byte(configured), 0o600))
	failedStart := time.Now()
	output, err = run("daemon", "start")
	r.Error(err)
	r.Contains(string(output), "daemon.log")
	r.Contains(string(output), "identity")
	r.Less(time.Since(failedStart), 20*time.Second, "a child that exits should not consume the startup wait budget")
}
