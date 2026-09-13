package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/fotobank/internal/ai"
	"go.kenn.io/fotobank/internal/ai/jobs"
	airuntime "go.kenn.io/fotobank/internal/ai/runtime"
	appsettingsstore "go.kenn.io/fotobank/internal/appsettings"
	"go.kenn.io/fotobank/internal/cli"
	"go.kenn.io/fotobank/internal/content"
	"go.kenn.io/fotobank/internal/db"
	"go.kenn.io/fotobank/internal/media"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/testutil/assetfixture"
)

// jsonUnmarshal is a thin alias used by newTestEmbedEndpoint so we
// don't carry a doc-only encoding/json reference at the package level
// for callers that don't need the dep — the helper is the only user.
func jsonUnmarshal(data []byte, v any) error { return json.Unmarshal(data, v) }

// lockedBuffer wraps a bytes.Buffer with a mutex so Write and String
// can be called from different goroutines without racing. Used as the
// server's stderr sink in tests where the test reads stderr while the
// server is still running (e.g. diagnosing a boot failure).
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

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
[observability]
admin_listen = "127.0.0.1:0"
`, nasRoot, filepath.Join(tmp, "flash")), 0o600))

	addrFile := filepath.Join(tmp, "addr")
	t.Setenv("FOTOBANK_CONFIG", cfgPath)
	t.Setenv("FOTOBANK_DB_PATH", filepath.Join(tmp, "state", "fotobank.sqlite"))
	t.Setenv("FOTOBANK_TEST_LISTEN_ADDR_SINK", addrFile)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan int, 1)
	var stderr lockedBuffer
	go func() {
		errCh <- cli.RunContext(
			ctx, []string{"server", "--config", cfgPath}, io.Discard, &stderr)
	}()

	var resolved string
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(20 * time.Millisecond)
	defer poll.Stop()
	for resolved == "" {
		if b, err := os.ReadFile(addrFile); err == nil && len(b) > 0 {
			resolved = strings.TrimSpace(string(b))
			break
		}
		select {
		case code := <-errCh:
			r.FailNow("server exited before publishing its bind address",
				"exit code %d; stderr: %s", code, stderr.String())
		case <-deadline.C:
			r.FailNow("server never published its bind address",
				"stderr: %s", stderr.String())
		case <-poll.C:
		}
	}

	resp, err := http.Get("http://" + resolved + "/api/v1/healthz")
	r.NoError(err)
	defer resp.Body.Close()
	r.Equal(http.StatusOK, resp.StatusCode)

	cancel()
	select {
	case code := <-errCh:
		r.Equal(0, code, "server stderr: %s", stderr.String())
	case <-time.After(5 * time.Second):
		r.Fail("server did not shut down within 5s")
	}
}

func TestServerStartsNotReadyWhenNASIsMissing(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	nasRoot := filepath.Join(tmp, "missing-nas")
	cfgPath := filepath.Join(tmp, "config.toml")
	r.NoError(os.WriteFile(cfgPath, fmt.Appendf(nil, `
[nas]
root = %q
[flash]
root = %q
[http]
listen_address = "127.0.0.1:0"
[observability]
admin_listen = "127.0.0.1:0"
`, nasRoot, filepath.Join(tmp, "flash")), 0o600))

	mainSink := filepath.Join(tmp, "main-addr")
	adminSink := filepath.Join(tmp, "admin-addr")
	t.Setenv("FOTOBANK_DB_PATH", filepath.Join(tmp, "state", "fotobank.sqlite"))
	t.Setenv("FOTOBANK_TEST_LISTEN_ADDR_SINK", mainSink)
	t.Setenv("FOTOBANK_TEST_ADMIN_ADDR_SINK", adminSink)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	var stdout, stderr lockedBuffer
	go func() {
		done <- cli.RunContext(ctx, []string{"serve", "--config", cfgPath}, &stdout, &stderr)
	}()
	t.Cleanup(func() {
		check := assert.New(t)
		cancel()
		select {
		case code := <-done:
			check.Equal(0, code, "server stderr: %s", stderr.String())
		case <-time.After(10 * time.Second):
			check.Fail("server did not shut down within 10s", "stderr: %s", stderr.String())
		}
	})

	mainAddr := waitForSink(t, mainSink)
	r.NotEmpty(mainAddr, "server did not start: %s", stderr.String())
	adminAddr := waitForSink(t, adminSink)
	r.NotEmpty(adminAddr, "admin listener did not start: %s", stderr.String())

	resp, err := http.Get("http://" + mainAddr + "/api/v1/healthz")
	r.NoError(err)
	r.NoError(resp.Body.Close())
	r.Equal(http.StatusOK, resp.StatusCode)
	resp, err = http.Get("http://" + adminAddr + "/readyz")
	r.NoError(err)
	r.NoError(resp.Body.Close())
	r.Equal(http.StatusServiceUnavailable, resp.StatusCode)
	r.NoDirExists(nasRoot)
}

func TestServerStartsNotReadyWhenNASSymlinkTargetIsMissing(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	nasTarget := filepath.Join(tmp, "missing-nas")
	nasRoot := filepath.Join(tmp, "nas-link")
	if err := os.Symlink(nasTarget, nasRoot); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	cfgPath := filepath.Join(tmp, "config.toml")
	r.NoError(os.WriteFile(cfgPath, fmt.Appendf(nil, `
[nas]
root = %q
[flash]
root = %q
[http]
listen_address = "127.0.0.1:0"
[observability]
admin_listen = "127.0.0.1:0"
`, nasRoot, filepath.Join(tmp, "flash")), 0o600))

	mainSink := filepath.Join(tmp, "main-addr")
	adminSink := filepath.Join(tmp, "admin-addr")
	t.Setenv("FOTOBANK_DB_PATH", filepath.Join(tmp, "state", "fotobank.sqlite"))
	t.Setenv("FOTOBANK_TEST_LISTEN_ADDR_SINK", mainSink)
	t.Setenv("FOTOBANK_TEST_ADMIN_ADDR_SINK", adminSink)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	var stdout, stderr lockedBuffer
	go func() {
		done <- cli.RunContext(ctx, []string{"serve", "--config", cfgPath}, &stdout, &stderr)
	}()
	t.Cleanup(func() {
		check := assert.New(t)
		cancel()
		select {
		case code := <-done:
			check.Equal(0, code, "server stderr: %s", stderr.String())
		case <-time.After(10 * time.Second):
			var stacks bytes.Buffer
			_ = pprof.Lookup("goroutine").WriteTo(&stacks, 2)
			check.Fail("server did not shut down within 10s", "stderr: %s\ngoroutines:\n%s", stderr.String(), stacks.String())
		}
	})

	mainAddr := waitForSink(t, mainSink)
	r.NotEmpty(mainAddr, "server did not start: %s", stderr.String())
	adminAddr := waitForSink(t, adminSink)
	r.NotEmpty(adminAddr, "admin listener did not start: %s", stderr.String())

	resp, err := http.Get("http://" + mainAddr + "/api/v1/healthz")
	r.NoError(err)
	r.NoError(resp.Body.Close())
	r.Equal(http.StatusOK, resp.StatusCode)
	resp, err = http.Get("http://" + adminAddr + "/readyz")
	r.NoError(err)
	r.NoError(resp.Body.Close())
	r.Equal(http.StatusServiceUnavailable, resp.StatusCode)
	r.NoDirExists(nasTarget)
}

func TestRunServerOwnsDocbankVaultForLifetime(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	nasRoot := filepath.Join(tmp, "nas")
	flashRoot := filepath.Join(tmp, "flash")
	vaultRoot := filepath.Join(tmp, "vault")
	r.NoError(os.MkdirAll(nasRoot, 0o700))

	cfgPath := filepath.Join(tmp, "config.toml")
	r.NoError(os.WriteFile(cfgPath, fmt.Appendf(nil, `
[nas]
root = %q
[flash]
root = %q
[docbank]
root = %q
[http]
listen_address = "127.0.0.1:0"
[observability]
admin_listen = "127.0.0.1:0"
`, nasRoot, flashRoot, vaultRoot), 0o600))

	addrFile := filepath.Join(tmp, "addr")
	t.Setenv("FOTOBANK_DB_PATH", filepath.Join(tmp, "fotobank.sqlite"))
	t.Setenv("FOTOBANK_TEST_LISTEN_ADDR_SINK", addrFile)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan int, 1)
	var stdout, stderr lockedBuffer
	go func() {
		errCh <- cli.RunContext(ctx, []string{"serve", "--config", cfgPath}, &stdout, &stderr)
	}()

	for range 500 {
		if b, err := os.ReadFile(addrFile); err == nil && len(b) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	addr, err := os.ReadFile(addrFile)
	r.NoError(err, "server did not publish its address: %s", stderr.String())
	r.NotEmpty(strings.TrimSpace(string(addr)), "server did not publish its address: %s", stderr.String())

	contender, openErr := content.Open(t.Context(), content.Config{Root: vaultRoot})
	if contender != nil {
		r.NoError(contender.Close())
	}
	r.Error(openErr)

	cancel()
	select {
	case code := <-errCh:
		r.Equal(0, code, "server stderr: %s", stderr.String())
	case <-time.After(5 * time.Second):
		r.Fail("server did not shut down within 5s", stderr.String())
	}

	reopened, err := content.Open(t.Context(), content.Config{Root: vaultRoot})
	r.NoError(err)
	r.NoError(reopened.Close())
}

// startWorkerTestServer owns the server through cleanup, including when a worker
// assertion fails. These are functional smoke tests, not startup/shutdown latency
// tests; allow the daemon's 30s drain budget plus time to release storage.
func startWorkerTestServer(t *testing.T, cfgPath, addrFile string) (string, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var diagnostics lockedBuffer
	var code int
	go func() {
		defer close(done)
		code = cli.RunContext(ctx, []string{"server", "--config", cfgPath}, io.Discard, &diagnostics)
	}()
	stop := func() {
		t.Helper()
		cancel()
		select {
		case <-done:
			assert.Zero(t, code, "server stderr: %s", diagnostics.String())
		case <-time.After(40 * time.Second):
			var stacks bytes.Buffer
			_ = pprof.Lookup("goroutine").WriteTo(&stacks, 2)
			assert.Fail(t, "server cleanup timed out", "stderr: %s\ngoroutines:\n%s", diagnostics.String(), stacks.String())
		}
	}
	t.Cleanup(stop)
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(20 * time.Millisecond)
	defer poll.Stop()
	for {
		if b, err := os.ReadFile(addrFile); err == nil && len(b) > 0 {
			return strings.TrimSpace(string(b)), stop
		}
		select {
		case <-done:
			require.FailNow(t, "server exited before publishing its address", "exit code %d; stderr: %s", code, diagnostics.String())
		case <-deadline.C:
			require.FailNow(t, "server startup timed out", "stderr: %s", diagnostics.String())
		case <-poll.C:
		}
	}
}

func TestServerDrainsPendingThumbRow(t *testing.T) {
	// Smoke test: seed a ready JPEG row pre-import, boot the server,
	// poll until thumb_status becomes 'ready' (worker has drained it).
	// The no-EXIF fixture has the sRGB-compatible color policy required
	// by Docbank's canonical preview producer.
	r := require.New(t)
	tmp := t.TempDir()
	nasRoot := filepath.Join(tmp, "nas")
	r.NoError(os.MkdirAll(filepath.Join(nasRoot, "550e8400-e29b-41d4-a716-446655440000", "2024"), 0o700))
	fixture, err := os.ReadFile(filepath.Join("..", "..", "testdata", "exif", "photo-no-exif.jpg"))
	r.NoError(err)

	cfgPath := filepath.Join(tmp, "c.toml")
	r.NoError(os.WriteFile(cfgPath, fmt.Appendf(nil, `
[nas]
root = %q
[flash]
root = %q
[identity]
mode = "stub"
[identity.stub]
hub = "h"
user_id = "u"
[http]
listen_address = "127.0.0.1:0"
[thumbs]
poll_interval = "20ms"
worker_concurrency = 1
[observability]
admin_listen = "127.0.0.1:0"
`, nasRoot, filepath.Join(tmp, "flash")), 0o600))

	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_CONFIG", cfgPath)
	t.Setenv("FOTOBANK_DB_PATH", dbPath)

	// Migrate the DB by opening+closing once, then insert a pending row.
	d, err := db.Open(dbPath)
	r.NoError(err)
	p := owners.Principal{Hub: "h", UserID: "u"}
	_, err = d.WriteDB().ExecContext(context.Background(),
		`INSERT OR IGNORE INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		p.Hub, p.UserID, "550e8400-e29b-41d4-a716-446655440000", time.Now().UTC(),
	)
	r.NoError(err)
	mediaID := uuid.NewString()
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	contentStore, err := content.Open(context.Background(), content.Config{Root: filepath.Join(tmp, "flash", "docbank")})
	r.NoError(err)
	assetfixture.InsertContent(t, repo, contentStore, fixture, media.Media{
		ID: mediaID, Owner: p, Type: media.TypePhoto, MimeType: "image/jpeg",
		OriginalFilename: "a.jpg", ImportedAt: time.Now().UTC(), ThumbStatus: "pending",
	})
	r.NoError(contentStore.Close())
	_ = d.Close()

	addrFile := filepath.Join(tmp, "addr")
	t.Setenv("FOTOBANK_TEST_LISTEN_ADDR_SINK", addrFile)

	startWorkerTestServer(t, cfgPath, addrFile)

	// Poll DB until the worker drains the row.
	deadline := time.Now().Add(10 * time.Second)
	d2, err := db.Open(dbPath)
	r.NoError(err)
	defer func() { r.NoError(d2.Close()) }()
	repo2 := media.NewRepo(d2.WriteDB(), d2.ReadDB())
	for time.Now().Before(deadline) {
		got, err := repo2.GetByID(context.Background(), mediaID)
		r.NoError(err)
		if got.ThumbStatus == "ready" {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	r.Fail("worker did not drain pending row within 10s")
}

func TestServerShutdownWaitsForThumbWorker(t *testing.T) {
	// Regression: runServer must join its background thumb-worker
	// goroutine before returning, otherwise the deferred d.Close races
	// a per-claim processOne that still holds a *sql.DB reference.
	//
	// Strategy: seed many pending rows, boot the server with a tiny
	// poll interval so drain is mid-flight when we cancel, then assert
	// that after RunContext returns the goroutine count is back to the
	// pre-run baseline. Without the WaitGroup join, up to
	// worker_concurrency processOne goroutines survive RunContext and
	// the test observes the leak.
	r := require.New(t)
	tmp := t.TempDir()
	nasRoot := filepath.Join(tmp, "nas")
	r.NoError(os.MkdirAll(filepath.Join(nasRoot, "550e8400-e29b-41d4-a716-446655440000", "2024"), 0o700))

	fixture, err := os.ReadFile(filepath.Join("..", "..", "testdata", "exif", "photo-with-timestamp.jpg"))
	r.NoError(err)

	cfgPath := filepath.Join(tmp, "c.toml")
	r.NoError(os.WriteFile(cfgPath, fmt.Appendf(nil, `
[nas]
root = %q
[flash]
root = %q
[identity]
mode = "stub"
[identity.stub]
hub = "h"
user_id = "u"
[http]
listen_address = "127.0.0.1:0"
[thumbs]
poll_interval = "10ms"
worker_concurrency = 4
[observability]
admin_listen = "127.0.0.1:0"
`, nasRoot, filepath.Join(tmp, "flash")), 0o600))

	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_CONFIG", cfgPath)
	t.Setenv("FOTOBANK_DB_PATH", dbPath)

	// Seed many pending rows so drain is back-pressured on the
	// concurrency semaphore when cancel fires.
	const nRows = 20
	d, err := db.Open(dbPath)
	r.NoError(err)
	p := owners.Principal{Hub: "h", UserID: "u"}
	_, err = d.WriteDB().ExecContext(context.Background(),
		`INSERT OR IGNORE INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		p.Hub, p.UserID, "550e8400-e29b-41d4-a716-446655440000", time.Now().UTC(),
	)
	r.NoError(err)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	contentStore, err := content.Open(context.Background(), content.Config{Root: filepath.Join(tmp, "flash", "docbank")})
	r.NoError(err)
	for i := range nRows {
		id := uuid.NewString()
		body := append(append([]byte(nil), fixture...), byte(i))
		assetfixture.InsertContent(t, repo, contentStore, body, media.Media{
			ID: id, Owner: p, Type: media.TypePhoto, MimeType: "image/jpeg",
			OriginalFilename: fmt.Sprintf("row-%03d.jpg", i),
			ImportedAt:       time.Now().UTC(), ThumbStatus: "pending",
		})
	}
	r.NoError(contentStore.Close())
	_ = d.Close()

	addrFile := filepath.Join(tmp, "addr")
	t.Setenv("FOTOBANK_TEST_LISTEN_ADDR_SINK", addrFile)

	baseline := runtime.NumGoroutine()
	_, stop := startWorkerTestServer(t, cfgPath, addrFile)

	// Let at least one poll tick fire so drain is mid-flight and
	// processOne goroutines are running inside the worker's semaphore.
	time.Sleep(40 * time.Millisecond)
	stop()

	// If the WaitGroup join is wired correctly, every thumb-worker
	// goroutine has exited by the time RunContext returns. Allow a
	// small tolerance for transient runtime goroutines (finalizers,
	// GC assist, sqlite driver threads) that the scheduler may not
	// have reaped yet. A missed join would leave at least
	// worker_concurrency=4 processOne goroutines alive here.
	const tolerance = 5
	after := runtime.NumGoroutine()
	r.LessOrEqualf(after, baseline+tolerance,
		"thumb worker goroutines leaked past RunContext: baseline=%d after=%d (tolerance=%d)",
		baseline, after, tolerance)
}

func TestServerShutdownEvictsSSEConnections(t *testing.T) {
	// Regression: srv.Shutdown waits for in-flight handlers to return.
	// Closing the event bus on shutdown releases SSE subscribers without
	// canceling ordinary API requests during their drain window.
	//
	// Strategy: boot the server, open an SSE connection, read the
	// "hello" frame so we know the handler is parked in its select
	// loop, then cancel and observe EOF before the 30s HTTP drain
	// deadline. Full shutdown also joins workers and closes storage;
	// its elapsed time does not tell us when the SSE handler exited.
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
hub = "h"
user_id = "u"
[http]
listen_address = "127.0.0.1:0"
[observability]
admin_listen = "127.0.0.1:0"
`, nasRoot, filepath.Join(tmp, "flash")), 0o600))

	addrFile := filepath.Join(tmp, "addr")
	t.Setenv("FOTOBANK_CONFIG", cfgPath)
	t.Setenv("FOTOBANK_DB_PATH", filepath.Join(tmp, "fotobank.sqlite"))
	t.Setenv("FOTOBANK_TEST_LISTEN_ADDR_SINK", addrFile)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan int, 1)
	finished := make(chan struct{})
	var out, eout lockedBuffer
	go func() {
		defer close(finished)
		errCh <- cli.RunContext(ctx, []string{"server", "--config", cfgPath}, &out, &eout)
	}()
	t.Cleanup(func() {
		check := assert.New(t)
		cancel()
		select {
		case <-finished:
		case <-time.After(10 * time.Second):
			var stacks bytes.Buffer
			_ = pprof.Lookup("goroutine").WriteTo(&stacks, 2)
			check.Fail("server did not shut down within 10s", "stderr: %s\ngoroutines:\n%s", eout.String(), stacks.String())
		}
	})

	resolved := waitForSink(t, addrFile)
	r.NotEmpty(resolved, "server never published its bind address; stderr: %s", eout.String())

	// Open the SSE subscription. We read until we see the "hello"
	// frame so we know the handler has flushed its bootstrap and is
	// blocked on its select loop — that's the state where Shutdown
	// would otherwise hang.
	req, err := http.NewRequest(http.MethodGet, "http://"+resolved+"/api/v1/events", nil)
	r.NoError(err)
	resp, err := http.DefaultClient.Do(req)
	r.NoError(err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	r.Equal(http.StatusOK, resp.StatusCode)

	helloSeen := make(chan struct{})
	go func() {
		buf := make([]byte, 4096)
		for {
			n, rerr := resp.Body.Read(buf)
			if n > 0 && bytes.Contains(buf[:n], []byte("event: hello")) {
				close(helloSeen)
				return
			}
			if rerr != nil {
				return
			}
		}
	}()
	select {
	case <-helloSeen:
	case <-time.After(2 * time.Second):
		r.FailNow("never received SSE hello frame")
	}

	streamClosed := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, resp.Body)
		streamClosed <- err
	}()
	cancel()
	select {
	case err := <-streamClosed:
		r.NoError(err, "SSE stream should end cleanly during shutdown")
	case <-time.After(10 * time.Second):
		r.FailNow("SSE stream stayed open during shutdown")
	}

	select {
	case code := <-errCh:
		r.Equal(0, code)
	case <-time.After(30 * time.Second):
		r.FailNowf("server did not finish shutdown after closing SSE", "stderr: %s", eout.String())
	}
}

func TestServerListensOnUnixSocket(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix-domain socket listener is not part of the Windows contract")
	}
	// Regression: [http].listen_address may be "unix:/path/to/sock",
	// which Validate accepts. runServer must bind a Unix socket rather
	// than pass the literal string to net.Listen("tcp", ...).
	r := require.New(t)

	tmp := t.TempDir()
	nasRoot := filepath.Join(tmp, "nas")
	r.NoError(os.MkdirAll(nasRoot, 0o700))
	sockPath := filepath.Join(tmp, "s.sock") // short enough for macOS's sun_path cap
	addrFile := filepath.Join(tmp, "listening")
	t.Setenv("FOTOBANK_TEST_LISTEN_ADDR_SINK", addrFile)

	cfgPath := filepath.Join(tmp, "c.toml")
	r.NoError(os.WriteFile(cfgPath, fmt.Appendf(nil, `
[nas]
root = %q
[flash]
root = %q
[http]
listen_address = "unix:%s"
[observability]
admin_listen = "127.0.0.1:0"
`, nasRoot, filepath.Join(tmp, "flash"), sockPath), 0o600))

	t.Setenv("FOTOBANK_CONFIG", cfgPath)
	t.Setenv("FOTOBANK_DB_PATH", filepath.Join(tmp, "fotobank.sqlite"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan int, 1)
	var stderr lockedBuffer
	go func() {
		errCh <- cli.RunContext(
			ctx, []string{"server", "--config", cfgPath}, io.Discard, &stderr)
	}()

	// Wait for net.Listen to finish, not just for its socket file to appear.
	// Also surface a real startup failure
	// immediately. The full suite starts many packages concurrently, so a
	// fixed two-second polling loop is too tight on loaded CI hosts.
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(20 * time.Millisecond)
	defer poll.Stop()
	for {
		if _, err := os.Stat(addrFile); err == nil {
			break
		}
		select {
		case code := <-errCh:
			r.FailNow("server exited before binding the Unix socket",
				"exit code %d; stderr: %s", code, stderr.String())
		case <-deadline.C:
			r.FailNow("server never bound the Unix socket",
				"path %s; stderr: %s", sockPath, stderr.String())
		case <-poll.C:
		}
	}

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
		r.Equal(0, code, "server stderr: %s", stderr.String())
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
[observability]
admin_listen = "127.0.0.1:0"
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

// newTestEmbedEndpoint stands up an httptest.Server that emulates the
// OpenAI /v1/embeddings shape: it returns one dim-sized vector per
// input regardless of modality. Probe + Worker calls all succeed; the
// vector contents are arbitrary because the wiring tests never check
// retrieval quality. Returns (server, base URL with /v1 suffix).
func newTestEmbedEndpoint(t *testing.T, dim int) (*httptest.Server, string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, err := io.ReadAll(req.Body)
		_ = req.Body.Close()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// Decode the request body to count input entries. The OpenAI
		// envelope guarantees `input` is an array of strings; we don't
		// care about the contents, just the length.
		var reqBody struct {
			Input []string `json:"input"`
		}
		// Prefer a real JSON parser over manual scanning so a
		// well-formed body with embedded brackets/commas (e.g. base64
		// data URLs) doesn't trip the response builder.
		if err := jsonUnmarshal(body, &reqBody); err != nil {
			http.Error(w, "decode: "+err.Error(), http.StatusBadRequest)
			return
		}
		n := len(reqBody.Input)
		if n == 0 {
			n = 1
		}
		// Build a single-dim-sized vector of zeroes and emit one entry
		// per input.
		var vec strings.Builder
		vec.WriteByte('[')
		for i := range dim {
			if i > 0 {
				vec.WriteByte(',')
			}
			vec.WriteString("0.1")
		}
		vec.WriteByte(']')

		var out strings.Builder
		out.WriteString(`{"data":[`)
		for i := range n {
			if i > 0 {
				out.WriteByte(',')
			}
			fmt.Fprintf(&out, `{"embedding":%s,"index":%d}`, vec.String(), i)
		}
		out.WriteString(`],"model":"test-embed"}`)

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, out.String())
	}))
	t.Cleanup(srv.Close)
	return srv, srv.URL + "/v1"
}

// TestServer_StartsEmbedSubsystemWhenEnabled boots a server with
// [ai.embed].enabled=true and asserts three things:
//
//  1. The server boots and binds successfully.
//  2. /api/v1/search returns 200 — i.e. deps.Search was wired into the
//     httpapi handler.
//  3. The embed worker's Run loop has claimed at least one
//     pending TaskEmbed job — proving the worker goroutine is alive
//     and pulling from ai_jobs.
//
// We seed an owner, a thumb-ready media row, an ack row (so the
// worker doesn't park on the hidden gate at the activator), and one
// pending embed job. Polling the job's status until it leaves
// 'pending' is the strongest available "worker is alive" signal in a
// short test budget.
func TestServer_StartsEmbedSubsystemWhenEnabled(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	nasRoot := filepath.Join(tmp, "nas")
	r.NoError(os.MkdirAll(filepath.Join(nasRoot, "h", "u"), 0o700))

	// Stand up a fake embed endpoint for the worker.
	const dim = 8
	_, base := newTestEmbedEndpoint(t, dim)

	cfgPath := filepath.Join(tmp, "c.toml")
	r.NoError(os.WriteFile(cfgPath, fmt.Appendf(nil, `
[nas]
root = %q
[flash]
root = %q
[identity]
mode = "stub"
[identity.stub]
hub = "h"
user_id = "u"
storage_key = "550e8400-e29b-41d4-a716-44665544000f"
[http]
listen_address = "127.0.0.1:0"
[ai]
enabled = true
[ai.embed]
enabled = true
model = "test-embed"
endpoint = %q
dimension = 8
input_edge = 384
batch_size = 8
worker_concurrency = 1
idle_poll = "50ms"
timeout = "5s"
max_retries = 1
[search]
retain_retired_days = 30
[observability]
admin_listen = "127.0.0.1:0"
`, nasRoot, filepath.Join(tmp, "flash"), base), 0o600))

	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_CONFIG", cfgPath)
	t.Setenv("FOTOBANK_DB_PATH", dbPath)

	// Seed one media + ack + a pending embed job. The worker should
	// claim the job within a few poll ticks and flip its status away
	// from 'pending'. We don't care about the eventual terminal state
	// (ready storage path is a placeholder so the resolver may fail,
	// landing the row in 'failed' or 'blocked') — only that something
	// other than 'pending' is observed.
	d, err := db.Open(dbPath)
	r.NoError(err)
	p := owners.Principal{Hub: "h", UserID: "u"}
	_, err = d.WriteDB().ExecContext(context.Background(),
		`INSERT OR IGNORE INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		p.Hub, p.UserID, "550e8400-e29b-41d4-a716-44665544000f", time.Now().UTC())
	r.NoError(err)
	mid := uuid.NewString()
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	assetfixture.Insert(t, repo, media.Media{
		ID: mid, Owner: p, Type: media.TypePhoto, MimeType: "image/jpeg",
		OriginalFilename: mid + ".jpg", ImportedAt: time.Now().UTC(), ThumbStatus: "ready",
	})
	q := jobs.NewQueue(d.WriteDB(), d.ReadDB())
	settingsRepo := appsettingsstore.NewRepo(d.WriteDB(), d.ReadDB())
	provider, err := airuntime.NewProvider(context.Background(), airuntime.Source{
		FilePath: cfgPath,
		Repo:     settingsRepo,
	})
	r.NoError(err)
	r.NoError(q.EnqueueClaim(context.Background(), mid, ai.TaskEmbed, provider.Effective().Claim.Embed))
	// Capture the job id so we can poll its row directly without
	// guessing the queue's id-generation strategy.
	var jobID string
	r.NoError(d.ReadDB().QueryRowContext(context.Background(),
		`SELECT id FROM ai_jobs WHERE media_id=? AND task='embed'`, mid).Scan(&jobID))
	_ = d.Close()

	addrFile := filepath.Join(tmp, "addr")
	t.Setenv("FOTOBANK_TEST_LISTEN_ADDR_SINK", addrFile)

	resolved, _ := startWorkerTestServer(t, cfgPath, addrFile)
	client := &http.Client{Transport: &http.Transport{}, Timeout: 10 * time.Second}
	t.Cleanup(client.CloseIdleConnections)

	// 1. /api/v1/search must answer (search service was wired into deps.Search).
	resp, err := client.Get("http://" + resolved + "/api/v1/search?q=")
	r.NoError(err)
	_ = resp.Body.Close()
	r.Equal(http.StatusOK, resp.StatusCode,
		"search route must be registered when embed is enabled")

	// 2. Embed worker must have claimed the seeded job (its status must
	//    leave 'pending' within a few poll ticks).
	d2, err := db.Open(dbPath)
	r.NoError(err)
	defer func() { _ = d2.Close() }()

	deadline := time.Now().Add(5 * time.Second)
	var status string
	for time.Now().Before(deadline) {
		err := d2.ReadDB().QueryRowContext(context.Background(),
			`SELECT status FROM ai_jobs WHERE id=?`, jobID).Scan(&status)
		r.NoError(err)
		if status != "pending" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	r.NotEqual("pending", status,
		"embed worker did not claim the pending job; got status=%q", status)
}

// TestServer_LeavesEmbedSubsystemDormantWhenDisabled boots a server
// with [ai.embed].enabled=false and asserts:
//
//  1. The server boots successfully (no probe runs, no embed wiring).
//  2. Search remains in the contract but returns service unavailable.
//  3. A pre-seeded TaskEmbed job stays in 'pending' for the duration
//     of the boot — confirming no embed worker is consuming the queue.
func TestServer_LeavesEmbedSubsystemDormantWhenDisabled(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	nasRoot := filepath.Join(tmp, "nas")
	r.NoError(os.MkdirAll(filepath.Join(nasRoot, "h", "u"), 0o700))

	cfgPath := filepath.Join(tmp, "c.toml")
	r.NoError(os.WriteFile(cfgPath, fmt.Appendf(nil, `
[nas]
root = %q
[flash]
root = %q
[identity]
mode = "stub"
[identity.stub]
hub = "h"
user_id = "u"
storage_key = "550e8400-e29b-41d4-a716-44665544000f"
[http]
listen_address = "127.0.0.1:0"
[ai]
enabled = false
[ai.embed]
enabled = false
[search]
retain_retired_days = 30
[observability]
admin_listen = "127.0.0.1:0"
`, nasRoot, filepath.Join(tmp, "flash")), 0o600))

	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_CONFIG", cfgPath)
	t.Setenv("FOTOBANK_DB_PATH", dbPath)

	// Seed an owner, media, and a pending embed job. With embed
	// disabled, no worker should claim it.
	d, err := db.Open(dbPath)
	r.NoError(err)
	p := owners.Principal{Hub: "h", UserID: "u"}
	_, err = d.WriteDB().ExecContext(context.Background(),
		`INSERT OR IGNORE INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		p.Hub, p.UserID, "550e8400-e29b-41d4-a716-44665544000f", time.Now().UTC())
	r.NoError(err)
	mid := uuid.NewString()
	repoM := media.NewRepo(d.WriteDB(), d.ReadDB())
	assetfixture.Insert(t, repoM, media.Media{
		ID: mid, Owner: p, Type: media.TypePhoto, MimeType: "image/jpeg",
		OriginalFilename: mid + ".jpg", ImportedAt: time.Now().UTC(), ThumbStatus: "ready",
	})
	q := jobs.NewQueue(d.WriteDB(), d.ReadDB())
	settingsRepo := appsettingsstore.NewRepo(d.WriteDB(), d.ReadDB())
	provider, err := airuntime.NewProvider(context.Background(), airuntime.Source{
		FilePath: cfgPath,
		Repo:     settingsRepo,
	})
	r.NoError(err)
	r.NoError(q.EnqueueClaim(context.Background(), mid, ai.TaskEmbed, provider.Effective().Claim.Embed))
	var jobID string
	r.NoError(d.ReadDB().QueryRowContext(context.Background(),
		`SELECT id FROM ai_jobs WHERE media_id=? AND task='embed'`, mid).Scan(&jobID))
	_ = d.Close()

	addrFile := filepath.Join(tmp, "addr")
	t.Setenv("FOTOBANK_TEST_LISTEN_ADDR_SINK", addrFile)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var out, eout lockedBuffer
	var serverCode int
	go func() {
		defer close(done)
		serverCode = cli.RunContext(ctx, []string{"server", "--config", cfgPath}, &out, &eout)
	}()
	t.Cleanup(func() {
		check := assert.New(t)
		cancel()
		select {
		case <-done:
			check.Zero(serverCode, "server stderr: %s", eout.String())
		case <-time.After(10 * time.Second):
			var stacks bytes.Buffer
			_ = pprof.Lookup("goroutine").WriteTo(&stacks, 2)
			check.Fail("server cleanup did not finish within 10s", "stderr: %s\ngoroutines:\n%s", eout.String(), stacks.String())
		}
	})

	// Wait for boot.
	resolved := waitForSink(t, addrFile)
	r.NotEmpty(resolved, "server never published its bind address: %s", eout.String())
	client := &http.Client{Transport: &http.Transport{}, Timeout: 10 * time.Second}
	t.Cleanup(client.CloseIdleConnections)

	// 1. Search remains discoverable even though its service is unavailable.
	resp, err := client.Get("http://" + resolved + "/api/v1/search?q=")
	r.NoError(err)
	_ = resp.Body.Close()
	r.Equal(http.StatusServiceUnavailable, resp.StatusCode,
		"search must report its unavailable service when embed is disabled")
	resp, err = client.Get("http://" + resolved + "/api/openapi.json")
	r.NoError(err)
	var contract struct {
		Paths map[string]json.RawMessage `json:"paths"`
	}
	err = json.NewDecoder(resp.Body).Decode(&contract)
	_ = resp.Body.Close()
	r.NoError(err)
	r.Equal(http.StatusOK, resp.StatusCode)
	r.Contains(contract.Paths, "/api/v1/search")
	r.Contains(contract.Paths, "/api/v1/facets")
	r.Contains(contract.Paths, "/api/v1/ai/health")

	// 2. Pending embed job stays pending — no worker is consuming it.
	// Wait briefly to give a hypothetical leaked worker time to claim;
	// the job must remain pending throughout.
	time.Sleep(300 * time.Millisecond)
	d2, err := db.Open(dbPath)
	r.NoError(err)
	defer func() { _ = d2.Close() }()
	var status string
	r.NoError(d2.ReadDB().QueryRowContext(context.Background(),
		`SELECT status FROM ai_jobs WHERE id=?`, jobID).Scan(&status))
	r.Equal("pending", status,
		"embed job must stay pending when embed is disabled; got status=%q", status)

	// Release this test's HTTP connections before timing server shutdown.
	// An unused pooled connection can otherwise hold Shutdown in its
	// grace period for a client that has not sent a request yet.
	client.CloseIdleConnections()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		var stacks bytes.Buffer
		_ = pprof.Lookup("goroutine").WriteTo(&stacks, 2)
		r.Fail("server did not shut down within 5s", "stderr: %s\ngoroutines:\n%s", eout.String(), stacks.String())
	}
}
