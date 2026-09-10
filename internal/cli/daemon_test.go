package cli_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
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
	"go.kenn.io/kit/daemon"
)

func TestDaemonShutdownClosesOperatorBeforeDraining(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	configPath := writeBasicConfig(t, tmp)
	dbPath := filepath.Join(tmp, "catalog.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)
	record := startCheckoutServer(t, configPath, dbPath)

	// Keep a real photo request in flight while shutdown drains that listener.
	photoAddress := strings.TrimPrefix(record.Metadata["web_url"], "http://")
	connection, err := net.DialTimeout("tcp", photoAddress, 5*time.Second)
	r.NoError(err)
	defer connection.Close()
	r.NoError(connection.SetDeadline(time.Now().Add(10 * time.Second)))
	_, err = fmt.Fprintf(connection, "POST /api/v1/albums HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\nContent-Length: 100\r\nExpect: 100-continue\r\n\r\n", photoAddress)
	r.NoError(err)
	response, err := http.ReadResponse(bufio.NewReader(connection), nil)
	r.NoError(err)
	r.NoError(response.Body.Close())
	r.Equal(http.StatusContinue, response.StatusCode)

	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, record.Endpoint().BaseURL()+"/api/v1/operator/daemon/stop", nil)
	r.NoError(err)
	request.Header.Set("Authorization", "Bearer "+record.Metadata["token"])
	client := &http.Client{Timeout: 5 * time.Second}
	response, err = client.Do(request)
	r.NoError(err)
	r.NoError(response.Body.Close())
	r.Equal(http.StatusNoContent, response.StatusCode)
	r.Eventually(func() bool {
		conn, err := net.DialTimeout("tcp", record.Address, 100*time.Millisecond)
		if err != nil {
			return true
		}
		_ = conn.Close()
		return false
	}, 2*time.Second, 10*time.Millisecond, "operator listener still accepts connections during shutdown")
	// Discovery stays reserved until the photo request and storage have drained.
	recordPath, err := (daemon.RuntimeStore{Dir: configPath + ".operator"}).Path(record.PID)
	r.NoError(err)
	_, err = os.Stat(recordPath)
	r.NoError(err)
}

func TestDaemonLifecycle(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	configPath := writeBasicConfig(t, tmp)
	dbPath := filepath.Join(tmp, "flash", "catalog.sqlite")
	// Reserve an available control port, then require the daemon to use it.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	r.NoError(err)
	controlAddress := listener.Addr().String()
	r.NoError(listener.Close())
	file, err := os.OpenFile(configPath, os.O_APPEND|os.O_WRONLY, 0)
	r.NoError(err)
	_, err = file.WriteString("\n[http]\nlisten_address = '127.0.0.1:0'\n[daemon]\nlisten_address = '" + controlAddress + "'\n")
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
	// Repository inspection must not recreate photo storage when no daemon
	// is running. Exercise the built binary so automatic launch is possible.
	r.NoError(os.Remove(filepath.Join(tmp, "nas")))
	r.NoError(os.Remove(filepath.Join(tmp, "flash")))
	for _, action := range []string{"list", "verify", "init"} {
		output, err = run("backup", action, "--repo", filepath.Join(tmp, "archives"))
		r.Error(err, "%s", output)
		r.NoDirExists(filepath.Join(tmp, "flash"), "%s", output)
		r.NoDirExists(filepath.Join(tmp, "nas"))
		r.NoDirExists(configPath + ".operator")
		r.Contains(string(output), "no daemon running")
		r.Contains(string(output), "daemon start --recovery")
	}
	r.NoError(os.Mkdir(filepath.Join(tmp, "nas"), 0o700))
	r.NoError(os.Mkdir(filepath.Join(tmp, "flash"), 0o700))
	// Recovery uses the same process slot, but does not initialize the catalog.
	output, err = run("daemon", "start", "--recovery")
	r.NoError(err, "%s", output)
	r.Contains(string(output), "recovery mode")
	r.NoFileExists(dbPath)
	output, err = run("daemon", "status", "--json")
	r.NoError(err, "%s", output)
	r.Contains(string(output), `"recovery":true`)
	output, err = run("albums", "list")
	r.Error(err)
	r.Contains(string(output), "recovery")
	output, err = run("daemon", "restart")
	r.NoError(err, "%s", output)
	r.Contains(string(output), "Web UI:")
	output, err = run("daemon", "stop")
	r.NoError(err, "%s", output)
	// Album commands start the daemon instead of opening the catalog themselves.
	output, err = run("albums", "create", "Trip")
	r.NoError(err, "%s", output)
	r.Contains(string(output), "Trip")
	output, err = run("daemon", "status", "--json")
	r.NoError(err, "%s", output)
	var albumDaemon struct {
		Running bool `json:"running"`
	}
	r.NoError(json.Unmarshal(output, &albumDaemon))
	r.True(albumDaemon.Running)
	output, err = run("daemon", "stop")
	r.NoError(err, "%s", output)
	// A real import starts the daemon and uses its vault, not a second owner.
	source := seedImportSource(t, "photo-no-exif.jpg")
	output, err = run("import", source, "--json")
	r.NoError(err, "%s", output)
	r.Contains(string(output), `"imported":1`)
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
	// Owner inspection starts a missing daemon without opening its own catalog.
	output, err = run("owners", "list", "--json")
	r.NoError(err, "%s", output)
	r.Contains(string(output), `"storage_key"`)
	output, err = run("daemon", "status", "--json")
	r.NoError(err, "%s", output)
	r.NoError(json.Unmarshal(output, &albumDaemon))
	r.True(albumDaemon.Running)
	output, err = run("daemon", "stop")
	r.NoError(err, "%s", output)
	// Share inspection also starts a missing daemon.
	output, err = run("shares", "list", "--json")
	r.NoError(err, "%s", output)
	r.JSONEq(`{"items":[]}`, string(output))
	output, err = run("daemon", "status", "--json")
	r.NoError(err, "%s", output)
	r.NoError(json.Unmarshal(output, &albumDaemon))
	r.True(albumDaemon.Running)
	output, err = run("daemon", "stop")
	r.NoError(err, "%s", output)
	// Recovery also starts a missing daemon, without opening a second vault.
	output, err = run("content", "recover", "--json")
	r.NoError(err, "%s", output)
	r.Contains(string(output), `"reports"`)
	output, err = run("daemon", "stop")
	r.NoError(err, "%s", output)
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
	// Explicit GPS scope works with automatic startup in a header deployment.
	output, err = run("gps", "backfill", "--all-owners", "--mode", "relabel", "--json")
	r.NoError(err, "%s", output)
	r.Contains(string(output), `"processed"`)
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
