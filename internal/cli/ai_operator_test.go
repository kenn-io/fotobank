package cli_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/fotobank/internal/ai/ack"
	"go.kenn.io/fotobank/internal/owners"
	aiservice "go.kenn.io/fotobank/internal/service/ai"
	"go.kenn.io/fotobank/internal/testutil"
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
	data = fmt.Appendf(data, "\n[ai]\nenabled = true\n[ai.vision]\nendpoint = %q\n[ai.tag]\nenabled = true\nmodel = %q\n", gateway.URL+"/v1", "test-model")
	r.NoError(os.WriteFile(cfg, data, 0o600))
	dbPath := filepath.Join(tmp, "catalog.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)
	startCheckoutServer(t, cfg, dbPath)
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

func TestAIStatusAndConsentWithUnavailableEmbeddings(t *testing.T) {
	r := require.New(t)
	var probes atomic.Int64
	var available atomic.Bool
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		probes.Add(1)
		if !available.Load() {
			http.Error(w, "provider unavailable", http.StatusServiceUnavailable)
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
	startCheckoutServer(t, cfg, dbPath)
	r.Zero(probes.Load(), "startup must not depend on provider availability")
	out, stderr, code := runAICLI("ai", "status", "--config", cfg)
	r.Zero(code, stderr)
	var health aiservice.Health
	r.NoError(json.Unmarshal([]byte(out), &health))
	r.Equal("acknowledgement_required", health.PausedReason)
	r.NotNil(health.Embed.Provider)
	r.False(health.Embed.Provider.Reachable)
	r.Contains(health.Embed.Provider.LastError, "503")
	before := probes.Load()
	_, stderr, code = runAICLI("ai", "acknowledge", "--hidden-processing", "--config", cfg)
	r.Zero(code, stderr)
	r.Equal(before, probes.Load(), "consent must not contact the provider")
	out, stderr, code = runAICLI("ai", "status", "--config", cfg)
	r.Zero(code, stderr)
	r.NoError(json.Unmarshal([]byte(out), &health))
	r.Empty(health.PausedReason)
	r.False(health.Embed.Provider.Reachable)
	available.Store(true)
	out, stderr, code = runAICLI("ai", "status", "--config", cfg)
	r.Zero(code, stderr)
	health = aiservice.Health{}
	r.NoError(json.Unmarshal([]byte(out), &health))
	r.NotNil(health.Embed.Provider)
	r.True(health.Embed.Provider.Reachable)
	r.Empty(health.Embed.Provider.LastError)
}
