package cli_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	_ "modernc.org/sqlite"

	"github.com/wesm/fotobank/internal/cli"
	"github.com/wesm/fotobank/internal/testutil/brokerhelper"
)

// TestMain dispatches the test binary into broker-helper mode when the
// brokerexec child-process env var is set. Without this, the e2e test
// below cannot use itself as the broker CLI: child invocations would
// re-run the full test suite. The dispatcher is a no-op for the normal
// `go test` invocation because BROKEREXEC_TEST_HELPER is only ever set
// on env passed to brokerexec.New (see internal/brokerexec/registrar.go),
// which targets child processes only.
func TestMain(m *testing.M) {
	if brokerhelper.IsHelper() {
		brokerhelper.Run()
		return
	}
	os.Exit(m.Run())
}

// TestE2EBrokerExecPublishesAndRevokes boots a real fotobank server
// with mode = "exec" and the test binary itself as the broker CLI.
// It POSTs a share, watches for broker_status='active' (the worker
// has run PublishScope through the helper), then POSTs revoke and
// watches for 'revoked_remote'.
//
// The Layer A and B tests in internal/brokerexec already cover wire
// and exec details; Layer C only proves wiring + worker + DB
// compose under the actual server boot path.
func TestE2EBrokerExecPublishesAndRevokes(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e tests skipped under -short")
	}
	r := require.New(t)

	tmp := t.TempDir()
	nasRoot := filepath.Join(tmp, "nas")
	flashRoot := filepath.Join(tmp, "flash")
	r.NoError(os.MkdirAll(nasRoot, 0o700))
	r.NoError(os.MkdirAll(flashRoot, 0o700))

	self, err := os.Executable()
	r.NoError(err)

	// The plan's TOML omits [identity]; runServer's buildIdentityProvider
	// rejects empty modes, so mirror the stub block from
	// e2e_shared_test.go:70-76. The owner hub stays "local"; the share's
	// grantee uses hub "h" — cross-hub grantees are explicitly the
	// pattern e2e_shared_test.go:150 already exercises.
	cfgPath := filepath.Join(tmp, "fotobank.toml")
	r.NoError(os.WriteFile(cfgPath, fmt.Appendf(nil, `
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
storage_key = "alice-sk"
[http]
listen_address = "127.0.0.1:0"
[imports]
file_lock_path = %q
[broker]
mode = "exec"
[broker.exec]
command = %q
publish_scope_args = []
revoke_scope_args  = []
call_timeout = "5s"
env = [
  "%s=1",
  "BROKEREXEC_TEST_EXIT=0",
]
`, nasRoot, flashRoot, filepath.Join(tmp, "import.lock"),
		self, brokerhelper.EnvVar), 0o600))

	t.Setenv("FOTOBANK_CONFIG", cfgPath)
	t.Setenv("FOTOBANK_TEST_SHARE_WORKER_TICK", "50ms")

	sink := filepath.Join(tmp, "addr")
	t.Setenv("FOTOBANK_TEST_LISTEN_ADDR_SINK", sink)

	// Seed media BEFORE booting the server. The import path drives
	// migrations and writes one media row to flashRoot/fotobank.sqlite
	// (the default DB path resolved by runServer at server.go:96-99),
	// matching the pattern in e2e_shares_test.go:64-70.
	src := seedImportSource(t, "photo-with-timestamp.jpg")
	var impOut, impErr bytes.Buffer
	code := cli.RunContext(context.Background(),
		[]string{"import", "--config", cfgPath, src},
		&impOut, &impErr)
	r.Equal(0, code, "import failed: stdout=%s stderr=%s",
		impOut.String(), impErr.String())

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	done := make(chan int, 1)
	go func() {
		var so, se bytes.Buffer
		done <- cli.RunContext(ctx, []string{"server"}, &so, &se)
	}()

	addr := waitForSink(t, sink)
	r.NotEmpty(addr, "server did not publish bind address")
	base := "http://" + addr
	client := &http.Client{Timeout: 5 * time.Second}

	mediaID := importOneMedia(t, client, base)

	// Create a share targeting media_set with the seeded media.
	body, err := json.Marshal(map[string]any{
		"grantee":     map[string]string{"hub": "h", "user_id": "bob"},
		"target_type": "media_set",
		"media_ids":   []string{mediaID},
	})
	r.NoError(err)

	resp, err := client.Post(base+"/api/v1/shares",
		"application/json", bytes.NewReader(body))
	r.NoError(err)
	r.Equal(http.StatusCreated, resp.StatusCode)
	var scope struct {
		UUID string `json:"uuid"`
	}
	r.NoError(json.NewDecoder(resp.Body).Decode(&scope))
	r.NoError(resp.Body.Close())
	r.NotEmpty(scope.UUID)

	// runServer defaults the DB to flashRoot/fotobank.sqlite (see
	// internal/cli/server.go:96-99) when FOTOBANK_DB_PATH is unset.
	dbPath := filepath.Join(flashRoot, "fotobank.sqlite")

	// Phase 1: worker should reach broker_status='active' within
	// a few ticks (50ms each). The helper exits 0 on every call.
	require.Eventually(t, func() bool {
		return readScopeStatus(t, dbPath, scope.UUID) == "active"
	}, 5*time.Second, 25*time.Millisecond,
		"broker_status did not reach 'active' — worker or registrar broken")

	// Assert the progress timestamps are populated.
	requireProgressSet(t, dbPath, scope.UUID,
		"broker_registered_at", "broker_granted_at")

	// Phase 2: revoke via POST /api/v1/shares/{uuid}/revoke (see
	// internal/httpapi/shares.go:301-305 — there is no DELETE route).
	resp, err = client.Post(base+"/api/v1/shares/"+scope.UUID+"/revoke",
		"application/json", nil)
	r.NoError(err)
	r.NoError(resp.Body.Close())
	r.Equal(http.StatusOK, resp.StatusCode)

	require.Eventually(t, func() bool {
		return readScopeStatus(t, dbPath, scope.UUID) == "revoked_remote"
	}, 5*time.Second, 25*time.Millisecond,
		"broker_status did not reach 'revoked_remote'")

	requireProgressSet(t, dbPath, scope.UUID, "broker_revoked_at")

	// Tidy shutdown.
	cancel()
	select {
	case ec := <-done:
		r.Equal(0, ec)
	case <-time.After(5 * time.Second):
		r.Fail("server did not shut down")
	}
}

// readScopeStatus opens a fresh sql.DB to read scopes.broker_status.
// Opening per call is fine for the 50ms poll cadence; SQLite readers
// don't block writers in WAL mode.
func readScopeStatus(t *testing.T, dbPath, uuid string) string {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer db.Close()

	var status string
	err = db.QueryRow(
		"SELECT broker_status FROM scopes WHERE uuid = ?", uuid,
	).Scan(&status)
	if err == sql.ErrNoRows {
		return ""
	}
	require.NoError(t, err)
	return status
}

// requireProgressSet asserts every named timestamp column on the
// scopes row is non-NULL.
func requireProgressSet(t *testing.T, dbPath, uuid string, cols ...string) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer db.Close()

	for _, c := range cols {
		var ts sql.NullString
		err := db.QueryRow(
			fmt.Sprintf("SELECT %s FROM scopes WHERE uuid = ?", c), uuid,
		).Scan(&ts)
		require.NoError(t, err)
		require.True(t, ts.Valid, "%s must be non-NULL after broker call", c)
	}
}

// importOneMedia returns the imported media ID by listing /api/v1/media.
// Import has already happened pre-boot via cli.RunContext("import", ...).
func importOneMedia(t *testing.T, client *http.Client, base string) string {
	t.Helper()
	resp, err := client.Get(base + "/api/v1/media")
	require.NoError(t, err)
	defer resp.Body.Close()

	var mediaList struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&mediaList))
	require.NotEmpty(t, mediaList.Items, "no media — pre-boot import did not seed")
	return mediaList.Items[0].ID
}
