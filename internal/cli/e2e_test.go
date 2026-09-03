package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.kenn.io/fotobank/internal/cli"
)

func TestEndToEndServerStubPrincipal(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfg := filepath.Join(tmp, "c.toml")
	r.NoError(os.Mkdir(filepath.Join(tmp, "nas"), 0o700))
	r.NoError(os.WriteFile(cfg, fmt.Appendf(nil, `
[nas]
root = %q
[flash]
root = %q
[identity.stub]
hub = "local"
user_id = "alice"
handle = "Alice"
[http]
listen_address = "127.0.0.1:0"
[observability]
admin_listen = "127.0.0.1:0"
`, filepath.Join(tmp, "nas"), filepath.Join(tmp, "flash")), 0o600))
	t.Setenv("FOTOBANK_CONFIG", cfg)
	t.Setenv("FOTOBANK_DB_PATH", filepath.Join(tmp, "fotobank.sqlite"))
	addrSink := filepath.Join(tmp, "addr")
	t.Setenv("FOTOBANK_TEST_LISTEN_ADDR_SINK", addrSink)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() {
		var so, se bytes.Buffer
		done <- cli.RunContext(ctx, []string{"server"}, &so, &se)
	}()
	var stopOnce sync.Once
	stopServer := func() {
		stopOnce.Do(func() {
			cancel()
			select {
			case code := <-done:
				r.Equal(0, code)
			case <-time.After(10 * time.Second):
				r.Fail("server did not shut down")
			}
		})
	}
	t.Cleanup(stopServer)

	addr := waitForSink(t, addrSink)
	r.NotEmpty(addr, "server did not publish bind address")

	// Short per-request timeout so a stalled server can't hang the test
	// until Go's global test timeout (minutes).
	client := &http.Client{Timeout: 5 * time.Second}

	healthz, err := client.Get("http://" + addr + "/api/v1/healthz")
	r.NoError(err)
	r.NoError(healthz.Body.Close())
	r.Equal(http.StatusOK, healthz.StatusCode)

	resp, err := client.Get("http://" + addr + "/api/v1/me")
	r.NoError(err)
	r.Equal(http.StatusOK, resp.StatusCode)

	var body struct {
		Principal struct {
			Hub    string `json:"hub"`
			UserID string `json:"user_id"`
			Handle string `json:"handle"`
		} `json:"principal"`
	}
	r.NoError(json.NewDecoder(resp.Body).Decode(&body))
	r.NoError(resp.Body.Close())
	r.Equal("local", body.Principal.Hub)
	r.Equal("alice", body.Principal.UserID)
	r.Equal("Alice", body.Principal.Handle)

	// Trigger shutdown explicitly and assert the server exits cleanly.
	// t.Cleanup will also call cancel(), but a second cancel on an
	// already-cancelled context is a no-op.
	client.CloseIdleConnections()
	stopServer()
}
