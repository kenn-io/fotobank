package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

// TestE2ESharesRoundTrip drives the full share outbox round-trip
// against a real server: import one fixture, create an album, add
// the media, create an album_live scope, wait for the NoopBroker-
// backed worker to flip broker_status pending -> active, verify
// album delete is blocked while the scope is live, revoke the
// scope and wait for revoking -> revoked_remote, then confirm
// album delete succeeds. FOTOBANK_TEST_SHARE_WORKER_TICK collapses
// the worker's 15s default tick so the Eventually polls finish in
// the 5s per-step budget.
func TestE2ESharesRoundTrip(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	nasRoot := filepath.Join(tmp, "nas")
	flashRoot := filepath.Join(tmp, "flash")
	r.NoError(os.MkdirAll(nasRoot, 0o700))
	r.NoError(os.MkdirAll(flashRoot, 0o700))

	cfg := filepath.Join(tmp, "c.toml")
	r.NoError(os.WriteFile(cfg, fmt.Appendf(nil, `
[nas]
root = %q
[flash]
root = %q
[identity]
mode = "stub"
[identity.stub]
hub = "local"
user_id = "alice"
handle = "Alice"
storage_key = "550e8400-e29b-41d4-a716-44665544000e"
[http]
listen_address = "127.0.0.1:0"
[imports]
file_lock_path = %q
[observability]
admin_listen = "127.0.0.1:0"
`, nasRoot, flashRoot, filepath.Join(tmp, "import.lock")), 0o600))

	t.Setenv("FOTOBANK_CONFIG", cfg)
	t.Setenv("FOTOBANK_DB_PATH", filepath.Join(tmp, "fotobank.sqlite"))
	t.Setenv("FOTOBANK_TEST_SHARE_WORKER_TICK", "50ms")
	addrSink := filepath.Join(tmp, "addr")
	t.Setenv("FOTOBANK_TEST_LISTEN_ADDR_SINK", addrSink)

	// Seed one fixture so the import produces exactly one media row.
	src := seedImportSource(t, "photo-with-timestamp.jpg")

	var impOut, impErr bytes.Buffer
	code := cli.RunContext(context.Background(),
		[]string{"import", "--config", cfg, src},
		&impOut, &impErr)
	r.Equal(0, code, "import failed: stdout=%s stderr=%s", impOut.String(), impErr.String())

	// Boot the server.
	ctx, cancel := context.WithCancel(context.Background())
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

	client := &http.Client{Timeout: 5 * time.Second}
	base := "http://" + addr

	// Discover the imported media_id via GET /api/v1/media.
	resp, err := client.Get(base + "/api/v1/media")
	r.NoError(err)
	var mediaList struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	r.NoError(json.NewDecoder(resp.Body).Decode(&mediaList))
	r.NoError(resp.Body.Close())
	r.Len(mediaList.Items, 1)
	mediaID := mediaList.Items[0].ID

	// Create album.
	body, err := json.Marshal(map[string]string{"name": "Trip"})
	r.NoError(err)
	resp, err = client.Post(base+"/api/v1/albums", "application/json", bytes.NewReader(body))
	r.NoError(err)
	var album struct {
		ID string `json:"id"`
	}
	r.NoError(json.NewDecoder(resp.Body).Decode(&album))
	r.NoError(resp.Body.Close())
	r.Equal(http.StatusCreated, resp.StatusCode)
	r.NotEmpty(album.ID)

	// Add media to album.
	body, err = json.Marshal(map[string]any{"media_ids": []string{mediaID}})
	r.NoError(err)
	resp, err = client.Post(base+"/api/v1/albums/"+album.ID+"/media",
		"application/json", bytes.NewReader(body))
	r.NoError(err)
	r.NoError(resp.Body.Close())
	r.Equal(http.StatusOK, resp.StatusCode)

	// Create the share (album_live).
	body, err = json.Marshal(map[string]any{
		"grantee":     map[string]string{"hub": "h", "user_id": "alice"},
		"target_type": "album_live",
		"album_id":    album.ID,
	})
	r.NoError(err)
	resp, err = client.Post(base+"/api/v1/shares", "application/json", bytes.NewReader(body))
	r.NoError(err)
	var scope struct {
		UUID         string `json:"uuid"`
		BrokerStatus string `json:"broker_status"`
	}
	r.NoError(json.NewDecoder(resp.Body).Decode(&scope))
	r.NoError(resp.Body.Close())
	r.Equal(http.StatusCreated, resp.StatusCode)
	r.NotEmpty(scope.UUID)
	r.Equal("pending", scope.BrokerStatus)

	// Poll until the NoopBroker-backed worker flips to active.
	pollStatus := func() (string, string) {
		r2, err := client.Get(base + "/api/v1/shares/" + scope.UUID)
		if err != nil || r2.StatusCode != http.StatusOK {
			if r2 != nil {
				_ = r2.Body.Close()
			}
			return "", ""
		}
		defer r2.Body.Close()
		buf, _ := io.ReadAll(r2.Body)
		var det struct {
			BrokerStatus string `json:"broker_status"`
		}
		_ = json.Unmarshal(buf, &det)
		return det.BrokerStatus, string(buf)
	}
	var lastBody, lastStatus string
	gotActive := false
	for range 100 {
		lastStatus, lastBody = pollStatus()
		if lastStatus == "active" {
			gotActive = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	r.Truef(gotActive, "broker_status never became active; last=%q body=%s", lastStatus, lastBody)

	// Album delete blocked while scope is active.
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, base+"/api/v1/albums/"+album.ID, nil)
	r.NoError(err)
	resp, err = client.Do(req)
	r.NoError(err)
	r.NoError(resp.Body.Close())
	r.Equal(http.StatusConflict, resp.StatusCode)

	// Revoke.
	req, err = http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/v1/shares/"+scope.UUID+"/revoke", nil)
	r.NoError(err)
	resp, err = client.Do(req)
	r.NoError(err)
	r.NoError(resp.Body.Close())
	r.Equal(http.StatusOK, resp.StatusCode)

	// Poll until worker revokes remotely.
	gotRevoked := false
	for range 100 {
		lastStatus, lastBody = pollStatus()
		if lastStatus == "revoked_remote" {
			gotRevoked = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	r.Truef(gotRevoked, "broker_status never became revoked_remote; last=%q body=%s", lastStatus, lastBody)

	// Now album delete succeeds.
	req, err = http.NewRequestWithContext(ctx, http.MethodDelete, base+"/api/v1/albums/"+album.ID, nil)
	r.NoError(err)
	resp, err = client.Do(req)
	r.NoError(err)
	r.NoError(resp.Body.Close())
	r.Truef(resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNoContent,
		"expected 200/204, got %d", resp.StatusCode)

	// Shutdown.
	client.CloseIdleConnections()
	cancel()
	select {
	case code := <-done:
		r.Equal(0, code)
	case <-time.After(5 * time.Second):
		r.Fail("server did not shut down")
	}
}
