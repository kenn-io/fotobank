package cli_test

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wesm/fotobank/internal/cli"
)

func TestServerRespondsToHealthz(t *testing.T) {
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
[http]
listen_address = "127.0.0.1:0"
`, nasRoot, filepath.Join(tmp, "flash")), 0o600))

	addrFile := filepath.Join(tmp, "addr")
	t.Setenv("FOTOBANK_CONFIG", cfgPath)
	t.Setenv("FOTOBANK_DB_PATH", filepath.Join(tmp, "fotobank.sqlite"))
	t.Setenv("FOTOBANK_TEST_LISTEN_ADDR_SINK", addrFile)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan int, 1)
	go func() {
		var out, eout bytes.Buffer
		errCh <- cli.RunContext(ctx, []string{"server", "--config", cfgPath}, &out, &eout)
	}()

	var resolved string
	for range 100 {
		if b, err := os.ReadFile(addrFile); err == nil && len(b) > 0 {
			resolved = strings.TrimSpace(string(b))
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	r.NotEmpty(resolved, "server never published its bind address")

	resp, err := http.Get("http://" + resolved + "/api/v1/healthz")
	r.NoError(err)
	defer resp.Body.Close()
	r.Equal(http.StatusOK, resp.StatusCode)

	cancel()
	select {
	case code := <-errCh:
		r.Equal(0, code)
	case <-time.After(5 * time.Second):
		r.Fail("server did not shut down within 5s")
	}
}

func TestServerListensOnUnixSocket(t *testing.T) {
	// Regression: [http].listen_address may be "unix:/path/to/sock",
	// which Validate accepts. runServer must bind a Unix socket rather
	// than pass the literal string to net.Listen("tcp", ...).
	r := require.New(t)

	tmp := t.TempDir()
	nasRoot := filepath.Join(tmp, "nas")
	r.NoError(os.MkdirAll(nasRoot, 0o700))
	sockPath := filepath.Join(tmp, "s.sock") // short enough for macOS's sun_path cap

	cfgPath := filepath.Join(tmp, "c.toml")
	r.NoError(os.WriteFile(cfgPath, fmt.Appendf(nil, `
[nas]
root = %q
[flash]
root = %q
[http]
listen_address = "unix:%s"
`, nasRoot, filepath.Join(tmp, "flash"), sockPath), 0o600))

	t.Setenv("FOTOBANK_CONFIG", cfgPath)
	t.Setenv("FOTOBANK_DB_PATH", filepath.Join(tmp, "fotobank.sqlite"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan int, 1)
	go func() {
		var out, eout bytes.Buffer
		errCh <- cli.RunContext(ctx, []string{"server", "--config", cfgPath}, &out, &eout)
	}()

	// Poll until the socket file appears (server has bound).
	var bound bool
	for range 100 {
		if _, err := os.Stat(sockPath); err == nil {
			bound = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	r.True(bound, "server never bound the Unix socket at %s", sockPath)

	// Issue a request over the socket and confirm /healthz responds.
	tr := &http.Transport{
		DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
			return net.Dial("unix", sockPath)
		},
	}
	client := &http.Client{Transport: tr}
	resp, err := client.Get("http://fotobank/api/v1/healthz")
	r.NoError(err)
	defer resp.Body.Close()
	r.Equal(http.StatusOK, resp.StatusCode)

	cancel()
	select {
	case code := <-errCh:
		r.Equal(0, code)
	case <-time.After(5 * time.Second):
		r.Fail("server did not shut down within 5s")
	}
}

func TestServerListenOverrideIsRevalidated(t *testing.T) {
	// Regression: --listen used to mutate cfg.HTTP.ListenAddress after
	// Validate ran. In header mode with no guard, the file-only config
	// validates because of the loopback bind; overriding to a public
	// address would start successfully and only fail requests. Ensure
	// the CLI re-validates and exits 1 when the override would break
	// the identity guard.
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
listen_address = "127.0.0.1:0"
`, nasRoot, filepath.Join(tmp, "flash")), 0o600))

	t.Setenv("FOTOBANK_CONFIG", cfgPath)
	t.Setenv("FOTOBANK_DB_PATH", filepath.Join(tmp, "fotobank.sqlite"))
	// Neutralise any inherited secret so the guard's re-validation
	// fails deterministically.
	t.Setenv("FOTOBANK_PROXY_SECRET", "")

	var out, eout bytes.Buffer
	code := cli.RunContext(context.Background(),
		[]string{"server", "--config", cfgPath, "--listen", "0.0.0.0:0"},
		&out, &eout)
	r.Equal(1, code)
	r.Contains(eout.String(), "header")
}
