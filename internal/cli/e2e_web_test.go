package cli_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/cli"
)

// TestE2EServerServesSPAOnRoot boots the real server binary and confirms
// the embedded SPA handler answers GET / with the stub HTML while the
// API surface continues to respond on /api/v1/*. The two assertions
// together prove the outer mux routes correctly: /api/* delegates to
// the huma + raw-mux chain, and / falls through to the embed handler.
func TestE2EServerServesSPAOnRoot(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e tests skipped under -short")
	}
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath, _ := writeObsConfig(t, tmp)
	mainSink := filepath.Join(tmp, "main-addr")
	t.Setenv("FOTOBANK_TEST_LISTEN_ADDR_SINK", mainSink)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan int, 1)
	go func() {
		var so, se bytes.Buffer
		done <- cli.RunContext(ctx, []string{"server", "--config", cfgPath}, &so, &se)
	}()

	addr := waitForSink(t, mainSink)
	r.NotEmpty(addr)

	resp, err := http.Get("http://" + addr + "/")
	r.NoError(err)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	r.Equal(200, resp.StatusCode)
	r.Contains(string(body), "<html")

	// Sanity check: API still responds on /api/v1/healthz (the embed
	// handler must NOT swallow API routes).
	resp2, err := http.Get("http://" + addr + "/api/v1/healthz")
	r.NoError(err)
	resp2.Body.Close()
	r.Equal(200, resp2.StatusCode)

	// Huma's docs/spec routes were moved under /api/ so the SPA handler
	// at / doesn't swallow them. Confirm /api/openapi.json returns
	// JSON, /api/docs returns the docs HTML, and the legacy root paths
	// either 404 or fall through to the SPA shell (both acceptable —
	// what matters is they don't return JSON spec bytes).
	resp3, err := http.Get("http://" + addr + "/api/openapi.json")
	r.NoError(err)
	body3, _ := io.ReadAll(resp3.Body)
	resp3.Body.Close()
	r.Equal(200, resp3.StatusCode)
	r.Contains(string(body3), `"openapi"`, "openapi.json must be valid JSON spec")
	r.Contains(resp3.Header.Get("Content-Type"), "json")

	resp4, err := http.Get("http://" + addr + "/api/docs")
	r.NoError(err)
	resp4.Body.Close()
	r.Equal(200, resp4.StatusCode)

	// Legacy root path no longer serves the OpenAPI spec; the SPA
	// handler answers with HTML or a 404. Either way, the response
	// must not be the JSON spec.
	resp5, err := http.Get("http://" + addr + "/openapi.json")
	r.NoError(err)
	body5, _ := io.ReadAll(resp5.Body)
	resp5.Body.Close()
	r.NotContains(string(body5), `"openapi"`, "root /openapi.json must NOT serve the spec")

	cancel()
	select {
	case code := <-done:
		r.Equal(0, code)
	case <-time.After(10 * time.Second):
		// Force the goroutine to exit before failing so we don't race
		// on output buffers (mirrors pattern used elsewhere in this
		// file). Bound the post-cancel wait too: if the goroutine
		// doesn't observe the cancel within 2s the test fails loudly
		// with a leaked goroutine rather than hanging forever.
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
		r.FailNow("server did not exit")
	}
}
