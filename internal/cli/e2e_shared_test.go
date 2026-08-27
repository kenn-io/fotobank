package cli_test

import (
	"bytes"
	"context"
	"database/sql"
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

// TestSharedE2EHeaderMode drives the grantee-side /api/v1/shared/*
// surface end-to-end against a header-mode server. The test boots the
// server twice against a shared SQLite DB:
//
//  1. First boot runs in stub mode (owner = alice) so the owner-side
//     pipeline can seed content via the real HTTP API: import a fixture,
//     create an album, add the media, and create an album_live scope
//     granting bob@h with allow_download=true.
//  2. Between boots, the scope's broker_status is nudged directly to
//     'active' in SQL (populating broker_granted_at /
//     broker_registered_at). This stands in for the share worker's
//     PublishScope path so the test doesn't have to wait on the worker
//     from the second boot.
//  3. Second boot runs in header mode. Grantee requests carry
//     X-Auth-Hub / X-Auth-User-ID / X-Auth-Handle plus a
//     X-Auth-Scopes header with the scope UUID. The test asserts that:
//     - /api/v1/shared/scopes returns the scope when the header is set;
//     - /api/v1/shared/albums/{id}/media returns the album's media;
//     - /api/v1/shared/media/{id}/original streams a non-empty body
//     (scope carries allow_download=true);
//     - /api/v1/shared/scopes with no X-Auth-Scopes returns an empty
//     items list (header-attested scopes is the auth signal).
//
// Both boots bind 127.0.0.1:0 so the Guard trusts the ingress by
// loopback. Each boot writes to its own FOTOBANK_TEST_LISTEN_ADDR_SINK
// path so stale sink content from boot 1 cannot fool boot 2's
// discovery loop.
func TestSharedE2EHeaderMode(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	nasRoot := filepath.Join(tmp, "nas")
	flashRoot := filepath.Join(tmp, "flash")
	r.NoError(os.MkdirAll(nasRoot, 0o700))
	r.NoError(os.MkdirAll(flashRoot, 0o700))

	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)

	// --- Phase 1: stub-mode server to seed content. ---

	stubCfg := filepath.Join(tmp, "stub.toml")
	r.NoError(os.WriteFile(stubCfg, fmt.Appendf(nil, `
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

	t.Setenv("FOTOBANK_CONFIG", stubCfg)
	stubSink := filepath.Join(tmp, "addr-stub")
	t.Setenv("FOTOBANK_TEST_LISTEN_ADDR_SINK", stubSink)

	// Seed one fixture so the import produces exactly one media row.
	src := seedImportSource(t, "photo-with-timestamp.jpg")

	var impOut, impErr bytes.Buffer
	code := cli.RunContext(context.Background(),
		[]string{"import", "--config", stubCfg, src},
		&impOut, &impErr)
	r.Equal(0, code, "import failed: stdout=%s stderr=%s", impOut.String(), impErr.String())

	// Boot the stub-mode server.
	stubCtx, stubCancel := context.WithCancel(context.Background())
	t.Cleanup(stubCancel)

	stubDone := make(chan int, 1)
	go func() {
		var so, se bytes.Buffer
		stubDone <- cli.RunContext(stubCtx, []string{"server"}, &so, &se)
	}()

	stubAddr := waitForSink(t, stubSink)
	r.NotEmpty(stubAddr, "stub server did not publish bind address")

	client := &http.Client{Timeout: 5 * time.Second}
	stubBase := "http://" + stubAddr

	// Discover the imported media_id via GET /api/v1/media.
	resp, err := client.Get(stubBase + "/api/v1/media")
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
	resp, err = client.Post(stubBase+"/api/v1/albums", "application/json", bytes.NewReader(body))
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
	resp, err = client.Post(stubBase+"/api/v1/albums/"+album.ID+"/media",
		"application/json", bytes.NewReader(body))
	r.NoError(err)
	r.NoError(resp.Body.Close())
	r.Equal(http.StatusOK, resp.StatusCode)

	// Create the share (album_live) with allow_download=true so the
	// grantee's /original GET is authorised.
	body, err = json.Marshal(map[string]any{
		"grantee":        map[string]string{"hub": "h", "user_id": "bob"},
		"target_type":    "album_live",
		"album_id":       album.ID,
		"allow_download": true,
	})
	r.NoError(err)
	resp, err = client.Post(stubBase+"/api/v1/shares", "application/json", bytes.NewReader(body))
	r.NoError(err)
	var scope struct {
		UUID string `json:"uuid"`
	}
	r.NoError(json.NewDecoder(resp.Body).Decode(&scope))
	r.NoError(resp.Body.Close())
	r.Equal(http.StatusCreated, resp.StatusCode)
	r.NotEmpty(scope.UUID)

	// Shut down the stub server cleanly before touching the DB so the
	// WAL is flushed and the second server can reopen the file without
	// observing stale in-flight state.
	client.CloseIdleConnections()
	stubCancel()
	select {
	case ec := <-stubDone:
		r.Equal(0, ec)
	case <-time.After(5 * time.Second):
		r.Fail("stub server did not shut down")
	}

	// Bump broker_status to active. This stands in for the share
	// worker's PublishScope path (which the second boot would run, but
	// we don't want to depend on its timing for the header-mode
	// assertions).
	bumpScopeActive(t, dbPath, scope.UUID, time.Now().UTC())

	// --- Phase 2: header-mode server for grantee reads. ---

	headerCfg := filepath.Join(tmp, "header.toml")
	r.NoError(os.WriteFile(headerCfg, fmt.Appendf(nil, `
[nas]
root = %q
[flash]
root = %q
[identity]
mode = "header"
[identity.header]
user_id_header    = "X-Auth-User-ID"
hub_header        = "X-Auth-Hub"
handle_header     = "X-Auth-Handle"
scopes_header     = "X-Auth-Scopes"
request_id_header = "X-Request-ID"
[http]
listen_address = "127.0.0.1:0"
[imports]
file_lock_path = %q
[observability]
admin_listen = "127.0.0.1:0"
`, nasRoot, flashRoot, filepath.Join(tmp, "import.lock")), 0o600))

	t.Setenv("FOTOBANK_CONFIG", headerCfg)
	headerSink := filepath.Join(tmp, "addr-header")
	t.Setenv("FOTOBANK_TEST_LISTEN_ADDR_SINK", headerSink)

	headerCtx, headerCancel := context.WithCancel(context.Background())
	t.Cleanup(headerCancel)

	headerDone := make(chan int, 1)
	go func() {
		var so, se bytes.Buffer
		headerDone <- cli.RunContext(headerCtx, []string{"server"}, &so, &se)
	}()

	headerAddr := waitForSink(t, headerSink)
	r.NotEmpty(headerAddr, "header server did not publish bind address")
	headerBase := "http://" + headerAddr

	newReq := func(path string, withScopes bool) *http.Request {
		req, err := http.NewRequestWithContext(headerCtx, http.MethodGet, headerBase+path, nil)
		r.NoError(err)
		req.Header.Set("X-Auth-Hub", "h")
		req.Header.Set("X-Auth-User-ID", "bob")
		req.Header.Set("X-Auth-Handle", "Bob")
		if withScopes {
			req.Header.Set("X-Auth-Scopes", scope.UUID)
		}
		return req
	}

	// GET /api/v1/shared/scopes with X-Auth-Scopes should list our scope.
	resp, err = client.Do(newReq("/api/v1/shared/scopes", true))
	r.NoError(err)
	r.Equal(http.StatusOK, resp.StatusCode)
	var scopesResp struct {
		Items []struct {
			UUID          string `json:"uuid"`
			TargetType    string `json:"target_type"`
			AllowDownload bool   `json:"allow_download"`
		} `json:"items"`
	}
	r.NoError(json.NewDecoder(resp.Body).Decode(&scopesResp))
	r.NoError(resp.Body.Close())
	r.Len(scopesResp.Items, 1)
	r.Equal(scope.UUID, scopesResp.Items[0].UUID)
	r.Equal("album_live", scopesResp.Items[0].TargetType)
	r.True(scopesResp.Items[0].AllowDownload)

	// GET /api/v1/shared/albums/{id}/media with X-Auth-Scopes.
	resp, err = client.Do(newReq("/api/v1/shared/albums/"+album.ID+"/media", true))
	r.NoError(err)
	r.Equal(http.StatusOK, resp.StatusCode)
	var albumMediaResp struct {
		Items []struct {
			ID          string `json:"id"`
			CanDownload bool   `json:"can_download"`
		} `json:"items"`
	}
	r.NoError(json.NewDecoder(resp.Body).Decode(&albumMediaResp))
	r.NoError(resp.Body.Close())
	r.Len(albumMediaResp.Items, 1)
	r.Equal(mediaID, albumMediaResp.Items[0].ID)
	r.True(albumMediaResp.Items[0].CanDownload)

	// GET /api/v1/shared/media/{mediaID}/original with X-Auth-Scopes.
	resp, err = client.Do(newReq("/api/v1/shared/media/"+mediaID+"/original", true))
	r.NoError(err)
	r.Equal(http.StatusOK, resp.StatusCode)
	bodyBytes, err := io.ReadAll(resp.Body)
	r.NoError(err)
	r.NoError(resp.Body.Close())
	r.NotEmpty(bodyBytes, "expected non-empty original body")

	// GET /api/v1/shared/scopes without X-Auth-Scopes should return an
	// empty items list — header-attested scopes is the auth signal.
	resp, err = client.Do(newReq("/api/v1/shared/scopes", false))
	r.NoError(err)
	r.Equal(http.StatusOK, resp.StatusCode)
	var emptyResp struct {
		Items []json.RawMessage `json:"items"`
	}
	r.NoError(json.NewDecoder(resp.Body).Decode(&emptyResp))
	r.NoError(resp.Body.Close())
	r.Empty(emptyResp.Items)

	// Shut down the header server.
	client.CloseIdleConnections()
	headerCancel()
	select {
	case ec := <-headerDone:
		r.Equal(0, ec)
	case <-time.After(5 * time.Second):
		r.Fail("header server did not shut down")
	}
}

// waitForSink polls path up to ~3s for the server's bind address to
// appear (written by runServer when FOTOBANK_TEST_LISTEN_ADDR_SINK is
// set). Returns the trimmed address, or "" if the poll timed out.
func waitForSink(t *testing.T, path string) string {
	t.Helper()
	for range 100 {
		if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
			return strings.TrimSpace(string(b))
		}
		time.Sleep(30 * time.Millisecond)
	}
	return ""
}

// bumpScopeActive flips a scope's broker_status to 'active' and stamps
// broker_granted_at / broker_registered_at, simulating what the share
// worker's PublishScope path would do in production. The caller must
// ensure no server is currently holding dbPath open so the WAL isn't
// mid-checkpoint.
func bumpScopeActive(t *testing.T, dbPath, scopeUUID string, now time.Time) {
	t.Helper()
	r := require.New(t)
	dsn := dbPath + "?_busy_timeout=5000&_fk=1"
	d, err := sql.Open("sqlite3", dsn)
	r.NoError(err)
	t.Cleanup(func() { _ = d.Close() })

	res, err := d.Exec(`
		UPDATE scopes
		   SET broker_status = 'active',
		       broker_granted_at = ?,
		       broker_registered_at = ?
		 WHERE uuid = ?`,
		now, now, scopeUUID)
	r.NoError(err)
	n, err := res.RowsAffected()
	r.NoError(err)
	r.Equal(int64(1), n, "expected exactly one scope row to be updated")

	r.NoError(d.Close())
}
