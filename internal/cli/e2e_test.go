package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wesm/fotobank/internal/cli"
)

func TestEndToEndServerStubPrincipal(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfg := filepath.Join(tmp, "c.toml")
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
`, filepath.Join(tmp, "nas"), filepath.Join(tmp, "flash")), 0o600))
	t.Setenv("FOTOBANK_CONFIG", cfg)
	t.Setenv("FOTOBANK_DB_PATH", filepath.Join(tmp, "fotobank.sqlite"))
	addrSink := filepath.Join(tmp, "addr")
	t.Setenv("FOTOBANK_TEST_LISTEN_ADDR_SINK", addrSink)

	ctx, cancel := context.WithCancel(context.Background())
	// Register cleanup immediately so any require.* failure before the
	// explicit cancel() still triggers server shutdown. The server
	// goroutine observes ctx.Done() and exits; on happy path we block
	// on `done` below to assert the exit code.
	t.Cleanup(cancel)

	done := make(chan int, 1)
	go func() {
		var so, se bytes.Buffer
		done <- cli.RunContext(ctx, []string{"server"}, &so, &se)
	}()

	var addr string
	for range 100 {
		if b, err := os.ReadFile(addrSink); err == nil && len(b) > 0 {
			addr = strings.TrimSpace(string(b))
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
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
	defer resp.Body.Close()
	r.Equal(http.StatusOK, resp.StatusCode)

	var body struct {
		Principal struct {
			Hub    string `json:"hub"`
			UserID string `json:"user_id"`
			Handle string `json:"handle"`
		} `json:"principal"`
	}
	r.NoError(json.NewDecoder(resp.Body).Decode(&body))
	r.Equal("local", body.Principal.Hub)
	r.Equal("alice", body.Principal.UserID)
	r.Equal("Alice", body.Principal.Handle)

	// Trigger shutdown explicitly and assert the server exits cleanly.
	// t.Cleanup will also call cancel(), but a second cancel on an
	// already-cancelled context is a no-op.
	cancel()
	select {
	case code := <-done:
		r.Equal(0, code)
	case <-time.After(5 * time.Second):
		r.Fail("server did not shut down")
	}
}
