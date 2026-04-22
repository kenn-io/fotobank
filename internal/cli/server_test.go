package cli_test

import (
	"bytes"
	"context"
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
