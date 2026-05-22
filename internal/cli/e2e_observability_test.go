package cli_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/cli"
)

// writeObsConfig produces a TOML config with a free-port admin listener
// so concurrent e2e tests don't fight for 127.0.0.1:9090. Backup is
// disabled so the snapshot_dir readyz probe doesn't fail before the
// backup worker has had a chance to create the snapshot directory.
func writeObsConfig(t *testing.T, tmp string) (cfgPath, adminAddrSink string) {
	t.Helper()
	r := require.New(t)
	nasRoot := filepath.Join(tmp, "nas")
	flashRoot := filepath.Join(tmp, "flash")
	r.NoError(os.MkdirAll(nasRoot, 0o700))
	r.NoError(os.MkdirAll(flashRoot, 0o700))
	cfgPath = filepath.Join(tmp, "fotobank.toml")
	r.NoError(os.WriteFile(cfgPath, []byte(`
[nas]
root = "`+nasRoot+`"
[flash]
root = "`+flashRoot+`"
[identity]
mode = "stub"
[identity.stub]
hub = "local"
user_id = "alice"
handle = "Alice"
storage_key = "alice-sk"
[http]
listen_address = "127.0.0.1:0"
[imports]
file_lock_path = "`+filepath.Join(tmp, "import.lock")+`"
[backup]
enabled = false
[observability]
admin_listen = "127.0.0.1:0"
`), 0o600))
	adminAddrSink = filepath.Join(tmp, "admin-addr")
	t.Setenv("FOTOBANK_TEST_ADMIN_ADDR_SINK", adminAddrSink)
	return cfgPath, adminAddrSink
}

func TestE2EObservabilityAdminScrape(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e tests skipped under -short")
	}
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath, adminSink := writeObsConfig(t, tmp)
	mainSink := filepath.Join(tmp, "main-addr")
	t.Setenv("FOTOBANK_TEST_LISTEN_ADDR_SINK", mainSink)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan int, 1)
	go func() {
		var so, se bytes.Buffer
		done <- cli.RunContext(ctx, []string{"server", "--config", cfgPath}, &so, &se)
	}()

	mainAddr := waitForSink(t, mainSink)
	r.NotEmpty(mainAddr)
	adminAddr := waitForSink(t, adminSink)
	r.NotEmpty(adminAddr, "admin listener must publish its bound address")

	// Issue a few requests through the main listener.
	for range 3 {
		resp, err := http.Get("http://" + mainAddr + "/api/v1/healthz")
		r.NoError(err)
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}

	// Scrape /metrics on admin and assert series presence. huma routes
	// register through humago, which doesn't apply WrapMuxHandler, so
	// requests dispatched via huma record route="unmatched". Raw mux
	// routes (media original/thumb, shared bytes) and any future
	// registration that uses WrapMuxHandler record their normalized
	// pattern. Healthz is a huma route, so we assert the request was
	// counted with status_class="2xx" without pinning the route label.
	resp, err := http.Get("http://" + adminAddr + "/metrics")
	r.NoError(err)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	r.Equal(200, resp.StatusCode)
	out := string(body)
	r.Contains(out, "fotobank_build_info")
	r.Contains(out, "go_goroutines")
	r.Contains(out, `fotobank_http_requests_total`)
	r.Contains(out, `status_class="2xx"`)

	// /readyz should return 200.
	resp, err = http.Get("http://" + adminAddr + "/readyz")
	r.NoError(err)
	resp.Body.Close()
	r.Equal(200, resp.StatusCode)

	cancel()
	select {
	case code := <-done:
		r.Equal(0, code, "server must exit cleanly on context cancel")
	case <-time.After(10 * time.Second):
		r.Fail("server did not exit within 10s of cancel")
	}
}

func TestE2EObservabilityShutdownReadyz(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e tests skipped under -short")
	}
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath, adminSink := writeObsConfig(t, tmp)
	mainSink := filepath.Join(tmp, "main-addr")
	t.Setenv("FOTOBANK_TEST_LISTEN_ADDR_SINK", mainSink)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan int, 1)
	go func() {
		var so, se bytes.Buffer
		done <- cli.RunContext(ctx, []string{"server", "--config", cfgPath}, &so, &se)
	}()
	r.NotEmpty(waitForSink(t, mainSink))
	adminAddr := waitForSink(t, adminSink)
	r.NotEmpty(adminAddr)

	// Initial /readyz is 200.
	resp, err := http.Get("http://" + adminAddr + "/readyz")
	r.NoError(err)
	resp.Body.Close()
	r.Equal(200, resp.StatusCode)

	// Cancel and observe: /readyz must either return 503, or the
	// admin listener must close. Both prove the readiness flip
	// happened. We must NOT see a sustained 200.
	cancel()
	deadline := time.Now().Add(2 * time.Second)
	saw503OrClosed := false
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://" + adminAddr + "/readyz")
		if err != nil {
			saw503OrClosed = true
			break
		}
		resp.Body.Close()
		if resp.StatusCode == 503 {
			saw503OrClosed = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	r.True(saw503OrClosed, "expected /readyz to flip to 503 or refuse before listener closed")
	select {
	case code := <-done:
		r.Equal(0, code, "server must exit cleanly after readyz flip")
	case <-time.After(10 * time.Second):
		r.Fail("server did not exit within 10s of cancel")
	}
}

func TestE2EObservabilityRejectsNonLoopbackAdmin(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e tests skipped under -short")
	}
	r := require.New(t)
	tmp := t.TempDir()
	nasRoot := filepath.Join(tmp, "nas")
	flashRoot := filepath.Join(tmp, "flash")
	r.NoError(os.MkdirAll(nasRoot, 0o700))
	r.NoError(os.MkdirAll(flashRoot, 0o700))
	cfgPath := filepath.Join(tmp, "fotobank.toml")
	r.NoError(os.WriteFile(cfgPath, []byte(`
[nas]
root = "`+nasRoot+`"
[flash]
root = "`+flashRoot+`"
[identity]
mode = "stub"
[identity.stub]
hub = "local"
user_id = "alice"
handle = "Alice"
storage_key = "alice-sk"
[http]
listen_address = "127.0.0.1:0"
[imports]
file_lock_path = "`+filepath.Join(tmp, "import.lock")+`"
[observability]
admin_listen = "0.0.0.0:9090"
`), 0o600))
	// Bounded ctx + goroutine: if validation regresses and the server
	// accepts 0.0.0.0, RunContext would block on Serve indefinitely.
	// The 5s timeout forces a clean exit and surfaces the regression
	// as a test failure rather than a hang.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var so, se bytes.Buffer
	codeCh := make(chan int, 1)
	go func() {
		codeCh <- cli.RunContext(ctx, []string{"server", "--config", cfgPath}, &so, &se)
	}()
	var code int
	select {
	case code = <-codeCh:
	case <-time.After(5 * time.Second):
		// Force the goroutine to exit before we read so/se to avoid a
		// data race on the buffers and to stop a runaway Serve. Bound
		// the post-cancel wait too: if the goroutine doesn't observe
		// the cancel within 2s the test fails loudly with a leaked
		// goroutine rather than hanging forever.
		cancel()
		select {
		case <-codeCh:
		case <-time.After(2 * time.Second):
		}
		r.FailNow("server did not exit within 5s — non-loopback admin_listen must be rejected at validation")
	}
	r.NotEqual(0, code, "non-loopback admin_listen must be rejected at validation")
	r.Contains(strings.ToLower(se.String()+so.String()), "loopback")
}
