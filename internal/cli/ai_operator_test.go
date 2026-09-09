package cli_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/fotobank/internal/ai/ack"
	"go.kenn.io/fotobank/internal/owners"
	aiservice "go.kenn.io/fotobank/internal/service/ai"
	"go.kenn.io/fotobank/internal/testutil"
	"go.kenn.io/kit/daemon"
)

func TestAIStatusAndConsentThroughDaemon(t *testing.T) {
	r := require.New(t)
	var probes atomic.Int64
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet || req.URL.Path != "/v1/models" {
			http.NotFound(w, req)
			return
		}
		probes.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(gateway.Close)
	tmp := t.TempDir()
	cfg := writeBasicConfig(t, tmp)
	data, err := os.ReadFile(cfg)
	r.NoError(err)
	data = fmt.Appendf(data, "\n[admin]\nprincipals = [{hub = %q, user_id = %q}]\n[ai]\nenabled = true\n[ai.vision]\nendpoint = %q\n[ai.tag]\nenabled = true\nmodel = %q\n", "h", "u", gateway.URL+"/v1", "test-model")
	r.NoError(os.WriteFile(cfg, data, 0o600))
	dbPath := filepath.Join(tmp, "catalog.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)
	record := startCheckoutServer(t, cfg, dbPath)
	before := probes.Load()
	out, stderr, code := runAICLI("ai", "status", "--config", cfg)
	r.Zero(code, stderr)
	var health aiservice.Health
	r.NoError(json.Unmarshal([]byte(out), &health))
	r.True(health.Enabled)
	r.True(health.Vision.Reachable)
	r.Greater(probes.Load(), before)
	r.Equal("acknowledgement_required", health.PausedReason)
	_, stderr, code = runAICLI("ai", "acknowledge", "--hidden-processing", "--config", cfg)
	r.Zero(code, stderr)
	out, stderr, code = runAICLI("ai", "status", "--config", cfg)
	r.Zero(code, stderr)
	r.NoError(json.Unmarshal([]byte(out), &health))
	r.Empty(health.PausedReason)
	d := testutil.OpenTestDBAt(t, dbPath)
	t.Cleanup(func() { r.NoError(d.Close()) })
	store := ack.New(d.WriteDB(), d.ReadDB())
	acked, err := store.IsAcknowledged(t.Context(), owners.Principal{Hub: "h", UserID: "u"})
	r.NoError(err)
	r.True(acked)
	acked, err = store.IsAcknowledged(t.Context(), owners.Principal{Hub: "h", UserID: "other"})
	r.NoError(err)
	r.False(acked)

	// Change endpoint and credentials through the running daemon's settings API.
	var updatedProbes atomic.Int64
	t.Setenv("FOTOBANK_TEST_VISION_KEY", "synthetic-vision-key")
	updated := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/v1/models" || req.Header.Get("Authorization") != "Bearer synthetic-vision-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		updatedProbes.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(updated.Close)
	ep := record.Endpoint()
	body := fmt.Sprintf(`{"values":{"ai.vision.endpoint":%q,"ai.vision.api_key_env":"FOTOBANK_TEST_VISION_KEY"}}`, updated.URL+"/v1")
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPut, ep.BaseURL()+"/api/v1/admin/settings/sections/vision", strings.NewReader(body))
	r.NoError(err)
	req.Header.Set("Authorization", "Bearer "+record.Metadata["token"])
	req.Header.Set("Content-Type", "application/json")
	resp, err := ep.HTTPClient(daemon.HTTPClientOptions{DisableKeepAlives: true}).Do(req)
	r.NoError(err)
	r.NoError(resp.Body.Close())
	r.Equal(http.StatusOK, resp.StatusCode)
	before = probes.Load()
	out, stderr, code = runAICLI("ai", "status", "--config", cfg)
	r.Zero(code, stderr)
	r.NoError(json.Unmarshal([]byte(out), &health))
	r.True(health.Vision.Reachable)
	r.Equal(before, probes.Load(), "status must stop probing the old endpoint")
	r.Positive(updatedProbes.Load(), "status must use the new endpoint and credentials")
}

func TestAICommandValidationBeforeStartup(t *testing.T) {
	for _, tc := range []struct {
		name   string
		header bool
		args   []string
	}{
		{"missing consent", false, []string{"ai", "acknowledge"}},
		{"false consent", false, []string{"ai", "acknowledge", "--hidden-processing=false"}},
		{"header status", true, []string{"ai", "status"}},
		{"header consent", true, []string{"ai", "acknowledge", "--hidden-processing"}},
		{"missing backfill task", false, []string{"ai", "backfill"}},
		{"invalid retry task", false, []string{"ai", "retry-failed", "--task=bogus"}},
		{"header backfill", true, []string{"ai", "backfill", "--task=tag"}},
		{"header retry", true, []string{"ai", "retry-failed", "--task=caption"}},
		{"invalid generation state", false, []string{"ai", "list-generations", "--state=bogus"}},
		{"invalid generation ID", false, []string{"ai", "promote-generation", "no", "--yes"}},
		{"nonpositive generation ID", false, []string{"ai", "promote-generation", "0", "--yes"}},
		{"header generations", true, []string{"ai", "list-generations"}},
		{"header promotion", true, []string{"ai", "promote-generation", "1", "--yes"}},
		{"header compaction", true, []string{"ai", "compact-retired-generations", "--dry-run"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			var cfg string
			if tc.header {
				cfg = writeNonStubConfig(t, tmp)
			} else {
				cfg = writeBasicConfig(t, tmp)
			}
			dbPath := filepath.Join(tmp, "catalog.sqlite")
			t.Setenv("FOTOBANK_DB_PATH", dbPath)
			_, _, code := runAICLI(append(tc.args, "--config", cfg)...)
			require.NotZero(t, code)
			require.NoFileExists(t, dbPath)
			require.NoDirExists(t, dbPath+".operator")
		})
	}
}

func TestAIEmbeddingQueueRejectsHeaderMode(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfg := writeAIEmbedConfig(t, tmp)
	data, err := os.ReadFile(cfg)
	r.NoError(err)
	r.NoError(os.WriteFile(cfg, []byte(strings.Replace(string(data), `mode = "stub"`, `mode = "header"`, 1)), 0o600))
	dbPath := filepath.Join(tmp, "catalog.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)
	seedEmbedOwnerAndPhoto(t, dbPath)
	acknowledgeStubOwner(t, dbPath)
	record := startCheckoutServer(t, cfg, dbPath)
	for _, operation := range []string{"backfill", "retry-failed"} {
		for _, task := range []string{"embed", "tag", "caption"} {
			request, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
				record.Metadata["web_url"]+"/api/v1/ai/"+operation, strings.NewReader(fmt.Sprintf(`{"task":%q}`, task)))
			r.NoError(err)
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("X-Auth-Hub", "h")
			request.Header.Set("X-Auth-User-Id", "u")
			response, err := http.DefaultClient.Do(request)
			r.NoError(err)
			body, err := io.ReadAll(response.Body)
			r.NoError(response.Body.Close())
			r.NoError(err)
			if task == "embed" {
				r.Equal(http.StatusBadRequest, response.StatusCode, "%s: %s", operation, body)
			} else {
				r.Equal(http.StatusOK, response.StatusCode, "%s %s: %s", operation, task, body)
			}
		}
	}
}

func TestAIGenerationOperatorBoundary(t *testing.T) {
	for _, mode := range []string{"stub", "header"} {
		t.Run(mode, func(t *testing.T) {
			r := require.New(t)
			tmp := t.TempDir()
			cfg := writeAIEmbedConfig(t, tmp)
			data, err := os.ReadFile(cfg)
			r.NoError(err)
			r.NoError(os.WriteFile(cfg, []byte(strings.Replace(string(data), `mode = "stub"`, `mode = "`+mode+`"`, 1)), 0o600))
			dbPath := filepath.Join(tmp, "catalog.sqlite")
			t.Setenv("FOTOBANK_DB_PATH", dbPath)
			record := startCheckoutServer(t, cfg, dbPath)
			for _, tc := range []struct {
				path, method, body string
			}{
				{"", http.MethodGet, ""},
				{"/1", http.MethodGet, ""},
				{"/1/promote", http.MethodPost, `{"confirm":true}`},
				{"/compact", http.MethodPost, `{"dry_run":true}`},
			} {
				for _, listener := range []string{"operator", "photo", "unauthenticated"} {
					base := record.Endpoint().BaseURL()
					client := record.Endpoint().HTTPClient(daemon.HTTPClientOptions{DisableKeepAlives: true})
					if listener == "photo" {
						base = record.Metadata["web_url"]
						client = http.DefaultClient
					}
					req, err := http.NewRequestWithContext(t.Context(), tc.method, base+"/api/v1/operator/ai/generations"+tc.path, strings.NewReader(tc.body))
					r.NoError(err)
					req.Header.Set("Content-Type", "application/json")
					req.Header.Set("X-Auth-Hub", "h")
					req.Header.Set("X-Auth-User-Id", "u")
					if listener != "unauthenticated" {
						req.Header.Set("Authorization", "Bearer "+record.Metadata["token"])
					}
					resp, err := client.Do(req)
					r.NoError(err)
					body, err := io.ReadAll(resp.Body)
					r.NoError(resp.Body.Close())
					r.NoError(err)
					want := http.StatusOK
					if tc.path == "/1" || tc.path == "/1/promote" {
						want = http.StatusNotFound
					}
					if mode == "header" || listener == "photo" {
						want = http.StatusForbidden
					}
					if listener == "unauthenticated" {
						want = http.StatusUnauthorized
					}
					r.Equal(want, resp.StatusCode, "%s %s: %s", listener, tc.path, body)
				}
			}
		})
	}
}

func TestAIEmbeddingQueueConsentThroughDaemon(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfg := writeAIEmbedConfig(t, tmp)
	dbPath := filepath.Join(tmp, "catalog.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)
	seedEmbedOwnerAndPhoto(t, dbPath)
	record := startCheckoutServer(t, cfg, dbPath)
	ep := record.Endpoint()
	for _, operation := range []string{"backfill", "retry-failed"} {
		request, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
			ep.BaseURL()+"/api/v1/ai/"+operation, strings.NewReader(`{"task":"embed"}`))
		r.NoError(err)
		request.Header.Set("Authorization", "Bearer "+record.Metadata["token"])
		request.Header.Set("Content-Type", "application/json")
		response, err := ep.HTTPClient(daemon.HTTPClientOptions{DisableKeepAlives: true}).Do(request)
		r.NoError(err)
		body, err := io.ReadAll(response.Body)
		r.NoError(response.Body.Close())
		r.NoError(err)
		r.Equal(http.StatusConflict, response.StatusCode, "%s: %s", operation, body)
		_, stderr, code := runAICLI("ai", operation, "--task=embed", "--config", cfg)
		r.NotZero(code)
		r.Contains(stderr, "acknowledgement")
	}
	_, stderr, code := runAICLI("ai", "acknowledge", "--hidden-processing", "--config", cfg)
	r.Zero(code, stderr)
	out, stderr, code := runAICLI("ai", "backfill", "--task=tag,caption,embed", "--config", cfg)
	r.Zero(code, stderr)
	r.Contains(out, "tag: enqueued 1")
	r.Contains(out, "caption: enqueued 1")
	r.Contains(out, "embed: enqueued 1")
	r.Contains(out, "total: 3")
}

func TestAIStatusAndConsentWithUnavailableEmbeddings(t *testing.T) {
	r := require.New(t)
	var probes atomic.Int64
	var available atomic.Bool
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		probes.Add(1)
		if !available.Load() {
			http.Error(w, "provider unavailable: synthetic-private-diagnostic", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"index":0,"embedding":[1,0,0,0,0,0,0,0]}]}`))
	}))
	t.Cleanup(gateway.Close)
	tmp := t.TempDir()
	cfg := writeBasicConfig(t, tmp)
	data, err := os.ReadFile(cfg)
	r.NoError(err)
	data = fmt.Appendf(data, "\n[ai]\nenabled = true\n[ai.embed]\nenabled = true\nmodel = %q\nendpoint = %q\ndimension = 8\ninput_edge = 384\ntimeout = %q\n", "test-model", gateway.URL+"/v1", "1s")
	r.NoError(os.WriteFile(cfg, data, 0o600))
	dbPath := filepath.Join(tmp, "catalog.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)
	sink := filepath.Join(tmp, "listen-address")
	t.Setenv("FOTOBANK_TEST_LISTEN_ADDR_SINK", sink)
	startCheckoutServer(t, cfg, dbPath)
	r.Zero(probes.Load(), "startup must not depend on provider availability")
	// The photo listener permits health to the stub photo user without any
	// admin role or operator token. Provider response bodies must not cross it.
	address, err := os.ReadFile(sink)
	r.NoError(err)
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+string(address)+"/api/v1/ai/health", nil)
	r.NoError(err)
	response, err := http.DefaultClient.Do(request)
	r.NoError(err)
	responseBody, err := io.ReadAll(response.Body)
	r.NoError(response.Body.Close())
	r.NoError(err)
	r.Equal(http.StatusOK, response.StatusCode)
	r.NotContains(string(responseBody), "synthetic-private-diagnostic")
	out, stderr, code := runAICLI("ai", "status", "--config", cfg)
	r.Zero(code, stderr)
	var health aiservice.Health
	r.NoError(json.Unmarshal([]byte(out), &health))
	r.Equal("acknowledgement_required", health.PausedReason)
	r.NotNil(health.Embed.Provider)
	r.False(health.Embed.Provider.Reachable)
	r.Equal("embedding provider unavailable", health.Embed.Provider.LastError)
	checkedAt := health.Embed.Provider.LastCheckAt
	before := probes.Load()
	_, stderr, code = runAICLI("ai", "acknowledge", "--hidden-processing", "--config", cfg)
	r.Zero(code, stderr)
	r.Equal(before, probes.Load(), "consent must not contact the provider")
	out, stderr, code = runAICLI("ai", "status", "--config", cfg)
	r.Zero(code, stderr)
	r.NoError(json.Unmarshal([]byte(out), &health))
	r.Empty(health.PausedReason)
	r.False(health.Embed.Provider.Reachable)
	r.Equal(before, probes.Load(), "repeated status must reuse the provider probe")
	available.Store(true)
	out, stderr, code = runAICLI("ai", "status", "--config", cfg)
	r.Zero(code, stderr)
	health = aiservice.Health{}
	r.NoError(json.Unmarshal([]byte(out), &health))
	r.NotNil(health.Embed.Provider)
	r.False(health.Embed.Provider.Reachable, "recovery is observed after the shared cache expires")
	r.Equal(checkedAt, health.Embed.Provider.LastCheckAt)
	r.Equal(before, probes.Load())
}
