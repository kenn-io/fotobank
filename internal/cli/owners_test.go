package cli_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.kenn.io/fotobank/internal/cli"
	"go.kenn.io/fotobank/internal/db"
	"go.kenn.io/fotobank/internal/httpapi"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/thumb"
)

func TestOwnersAddGeneratedAndExplicitKeys(t *testing.T) {
	r := require.New(t)
	tmp := newCLITempEnv(t)

	var out, eout bytes.Buffer
	code := cli.Run([]string{"owners", "add",
		"--hub", "h", "--user-id", "generated", "--handle", "User",
	}, &out, &eout)
	r.Equal(0, code, eout.String())
	outputFields := strings.Fields(out.String())
	r.NotEmpty(outputFields)
	generatedKey := outputFields[len(outputFields)-1]
	_, err := uuid.Parse(generatedKey)
	r.NoError(err)

	// Repeating an add without an explicit key returns the stored winner.
	out.Reset()
	eout.Reset()
	code = cli.Run([]string{"owners", "add",
		"--hub", "h", "--user-id", "generated",
	}, &out, &eout)
	r.Equal(0, code, eout.String())
	r.Contains(out.String(), generatedKey)
	out.Reset()
	eout.Reset()
	code = cli.Run([]string{"owners", "add", "--hub", "h", "--user-id", "generated", "--storage-key", "770e8400-e29b-41d4-a716-446655440000"}, &out, &eout)
	r.Equal(1, code)
	r.Contains(eout.String(), "409")

	explicitKey := "660e8400-e29b-41d4-a716-446655440000"
	out.Reset()
	eout.Reset()
	code = cli.Run([]string{"owners", "add",
		"--hub", "h", "--user-id", "explicit", "--storage-key", explicitKey,
	}, &out, &eout)
	r.Equal(0, code, eout.String())
	r.Contains(out.String(), explicitKey)
	out.Reset()
	eout.Reset()
	code = cli.Run([]string{"owners", "add", "--hub", "h", "--user-id", "other", "--storage-key", explicitKey}, &out, &eout)
	r.Equal(1, code)
	r.Contains(eout.String(), "409")

	out.Reset()
	eout.Reset()
	code = cli.Run([]string{"owners", "add",
		"--hub", "h", "--user-id", "invalid", "--storage-key", "not-a-uuid",
	}, &out, &eout)
	r.Equal(2, code)
	r.Contains(eout.String(), "UUID")

	d, err := db.Open(filepath.Join(tmp, "fotobank.sqlite"))
	r.NoError(err)
	stored, err := owners.NewRepo(d.WriteDB(), d.ReadDB()).GetByPrincipal(
		t.Context(), owners.Principal{Hub: "h", UserID: "generated"})
	r.NoError(err)
	r.Equal(generatedKey, stored.StorageKey)
	r.NoError(d.Close())
}

func TestOwnersLiveAgainstDaemon(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfg := writeBasicConfig(t, tmp)
	dbPath := filepath.Join(tmp, "catalog.sqlite")
	t.Setenv("FOTOBANK_CONFIG", cfg)
	t.Setenv("FOTOBANK_DB_PATH", dbPath)
	startCheckoutServer(t, cfg, dbPath)
	var out, stderr bytes.Buffer
	code := cli.RunContext(t.Context(), []string{"owners", "add", "--hub", "h", "--user-id", "guest", "--handle", "Guest"}, &out, &stderr)
	r.Zero(code, "%s", stderr.String())
	out.Reset()
	stderr.Reset()
	code = cli.RunContext(t.Context(), []string{"owners", "list", "--json"}, &out, &stderr)
	r.Zero(code, "%s", stderr.String())
	r.Contains(out.String(), "Guest")
	var page struct {
		Items []struct {
			UserID string `json:"user_id"`
		} `json:"items"`
	}
	r.NoError(json.Unmarshal(out.Bytes(), &page))
	r.Len(page.Items, 2)
}

func TestOwnersListShowsAddedRow(t *testing.T) {
	r := require.New(t)
	_ = newCLITempEnv(t)

	var out, eout bytes.Buffer
	r.Equal(0, cli.Run([]string{"owners", "add",
		"--hub", "h", "--user-id", "u", "--storage-key", "550e8400-e29b-41d4-a716-446655440000",
	}, &out, &eout))

	out.Reset()
	eout.Reset()
	r.Equal(0, cli.Run([]string{"owners", "list", "--json"}, &out, &eout))
	var result struct {
		Items []map[string]any `json:"items"`
	}
	r.NoError(json.Unmarshal(out.Bytes(), &result))
	r.Len(result.Items, 1)
	r.Equal("h", result.Items[0]["hub"])
}

func TestOwnersListRejectsBadFlags(t *testing.T) {
	// Regression: owners list used to discard fs.Parse errors, so an
	// invalid flag would silently open the DB and list rows. It must
	// now exit 2 with usage before doing any work.
	r := require.New(t)
	tmp := t.TempDir()
	t.Setenv("FOTOBANK_CONFIG", writeBasicConfig(t, tmp))
	dbPath := filepath.Join(tmp, "catalog.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)

	var out, eout bytes.Buffer
	r.Equal(2, cli.Run([]string{"owners", "list", "--bad"}, &out, &eout))
	r.Contains(eout.String(), "usage")
	r.NoFileExists(dbPath)

	out.Reset()
	eout.Reset()
	r.Equal(2, cli.Run([]string{"owners", "list", "extra-positional"}, &out, &eout))
	r.Contains(eout.String(), "usage")
}

func TestOwnersInvalidArgumentsBeforeStartup(t *testing.T) {
	for _, args := range [][]string{
		{"add", "--hub", "h"},
		{"add", "--hub", "h", "--user-id", "guest", "--storage-key", "invalid"},
		{"remove", "--hub", "h"},
		{"remove", "--hub", "h", "--user-id", "guest", "--purge"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			r := require.New(t)
			tmp := t.TempDir()
			cfg := writeBasicConfig(t, tmp)
			dbPath := filepath.Join(tmp, "catalog.sqlite")
			t.Setenv("FOTOBANK_DB_PATH", dbPath)
			var out, stderr bytes.Buffer
			command := append([]string{"owners", "--config", cfg}, args...)
			r.Equal(2, cli.RunContext(t.Context(), command, &out, &stderr), "%s", stderr.String())
			r.NoFileExists(dbPath)
			r.NoDirExists(dbPath + ".operator")
		})
	}
}

func TestOwnersOperatorAuthorization(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfg := writeBasicConfig(t, tmp)
	dbPath := filepath.Join(tmp, "catalog.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)
	seedReadyRow(t, dbPath)
	record := startCheckoutServer(t, cfg, dbPath)
	for _, operation := range []struct {
		method, suffix, body string
		status               int
	}{
		{http.MethodPost, "", `{"hub":"h","user_id":"guest","handle":"Guest"}`, 200},
		{http.MethodGet, "", "", 200},
		{http.MethodDelete, "?hub=h&user_id=guest", "", 204},
		{http.MethodDelete, "?hub=h&user_id=u", "", 409},
	} {
		for _, access := range []struct {
			name, base, token string
			denied            int
		}{
			{"no token", record.Endpoint().BaseURL(), "", 401},
			{"photo listener", record.Metadata["web_url"], record.Metadata["token"], 403},
			{"operator", record.Endpoint().BaseURL(), record.Metadata["token"], 0},
		} {
			t.Run(operation.method+operation.suffix+access.name, func(t *testing.T) {
				r := require.New(t)
				req, err := http.NewRequestWithContext(t.Context(), operation.method, access.base+"/api/v1/operator/owners"+operation.suffix, strings.NewReader(operation.body))
				r.NoError(err)
				req.Header.Set("Content-Type", "application/json")
				if access.token != "" {
					req.Header.Set("Authorization", "Bearer "+access.token)
				}
				response, err := http.DefaultClient.Do(req)
				r.NoError(err)
				defer response.Body.Close()
				body, err := io.ReadAll(response.Body)
				r.NoError(err)
				want := access.denied
				if want == 0 {
					want = operation.status
				}
				r.Equal(want, response.StatusCode, "%s", body)
			})
		}
	}
	var out, stderr bytes.Buffer
	r.Zero(cli.RunContext(t.Context(), []string{"owners", "list", "--config", cfg, "--json"}, &out, &stderr), "%s", stderr.String())
	var result httpapi.OwnerListResult
	r.NoError(json.Unmarshal(out.Bytes(), &result))
	r.Len(result.Items, 1)
	r.Equal("u", result.Items[0].UserID)
}

func TestOwnersHeaderDeployment(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfg := writeNonStubConfig(t, tmp)
	dbPath := filepath.Join(tmp, "catalog.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)
	startCheckoutServer(t, cfg, dbPath)
	var out, stderr bytes.Buffer
	r.Zero(cli.RunContext(t.Context(), []string{"owners", "add", "--config", cfg, "--hub", "example", "--user-id", "guest"}, &out, &stderr), "%s", stderr.String())
	out.Reset()
	stderr.Reset()
	r.Zero(cli.RunContext(t.Context(), []string{"owners", "list", "--config", cfg, "--json"}, &out, &stderr), "%s", stderr.String())
	var result httpapi.OwnerListResult
	r.NoError(json.Unmarshal(out.Bytes(), &result))
	r.Len(result.Items, 1)
	r.Equal("guest", result.Items[0].UserID)
	out.Reset()
	stderr.Reset()
	r.Zero(cli.RunContext(t.Context(), []string{"owners", "remove", "--config", cfg, "--hub", "example", "--user-id", "guest"}, &out, &stderr), "%s", stderr.String())
	out.Reset()
	stderr.Reset()
	r.Zero(cli.RunContext(t.Context(), []string{"owners", "list", "--config", cfg, "--json"}, &out, &stderr), "%s", stderr.String())
	r.NoError(json.Unmarshal(out.Bytes(), &result))
	r.Empty(result.Items)
}

func TestNewOwnerThumbnailWithoutRestart(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfg := writeNonStubConfig(t, tmp)
	dbPath := filepath.Join(tmp, "catalog.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)
	record := startCheckoutServer(t, cfg, dbPath)
	key := "550e8400-e29b-41d4-a716-446655440009"
	var out, stderr bytes.Buffer
	r.Zero(cli.RunContext(t.Context(), []string{"owners", "add", "--config", cfg, "--hub", "h", "--user-id", "guest", "--storage-key", key}, &out, &stderr), "%s", stderr.String())
	m := seedRowForOwner(t, dbPath, owners.Principal{Hub: "h", UserID: "guest"})
	artifact := filepath.Join(tmp, "nas", key, filepath.FromSlash(thumb.ThumbKey(m.ID, m.ThumbVersion, thumb.SizeGrid)))
	r.NoError(os.MkdirAll(filepath.Dir(artifact), 0o700))
	r.NoError(os.WriteFile(artifact, []byte("thumbnail bytes"), 0o600))
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, record.Metadata["web_url"]+"/api/v1/media/"+m.ID+"/thumb?size=grid&v=2", nil)
	r.NoError(err)
	req.Header.Set("X-Auth-Hub", "h")
	req.Header.Set("X-Auth-User-Id", "guest")
	response, err := http.DefaultClient.Do(req)
	r.NoError(err)
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	r.NoError(err)
	r.Equal(200, response.StatusCode, "%s", body)
	r.Equal("thumbnail bytes", string(body))
}

func TestOwnersRemoveReferencedOwnerConflict(t *testing.T) {
	for _, reference := range []string{"asset", "checkout"} {
		t.Run(reference, func(t *testing.T) {
			r := require.New(t)
			tmp := newCLITempEnv(t)
			var out, stderr bytes.Buffer
			r.Zero(cli.RunContext(t.Context(), []string{"owners", "add", "--hub", "h", "--user-id", "guest"}, &out, &stderr), "%s", stderr.String())
			database, err := db.Open(filepath.Join(tmp, "fotobank.sqlite"))
			r.NoError(err)
			defer database.Close()
			if reference == "asset" {
				_, err = database.WriteDB().ExecContext(t.Context(), `INSERT INTO assets
					(id, owner_hub, owner_user_id, state, media_type, imported_at, thumb_status, thumb_version)
					VALUES ('aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa', 'h', 'guest', 'pending', 'photo', datetime('now'), 'pending', 0)`)
			} else {
				_, err = database.WriteDB().ExecContext(t.Context(), `INSERT INTO checkouts
					(id, owner_hub, owner_user_id, root, layout, state, created_at, updated_at)
					VALUES ('aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa', 'h', 'guest', ?, 'capture_date', 'error', datetime('now'), datetime('now'))`, filepath.Join(tmp, "checkout"))
			}
			r.NoError(err)
			out.Reset()
			stderr.Reset()
			r.Equal(1, cli.RunContext(t.Context(), []string{"owners", "remove", "--hub", "h", "--user-id", "guest"}, &out, &stderr))
			r.Contains(stderr.String(), "409")
			_, err = owners.NewRepo(database.WriteDB(), database.ReadDB()).GetByPrincipal(t.Context(), owners.Principal{Hub: "h", UserID: "guest"})
			r.NoError(err)
		})
	}
}

func TestOwnersRemoveSucceedsWhenEmpty(t *testing.T) {
	r := require.New(t)
	_ = newCLITempEnv(t)
	var out, eout bytes.Buffer
	r.Equal(0, cli.Run([]string{"owners", "add",
		"--hub", "h", "--user-id", "guest", "--storage-key", "660e8400-e29b-41d4-a716-446655440000",
	}, &out, &eout))
	out.Reset()
	eout.Reset()
	r.Equal(0, cli.Run([]string{"owners", "remove",
		"--hub", "h", "--user-id", "guest",
	}, &out, &eout))
	out.Reset()
	eout.Reset()
	r.Equal(1, cli.Run([]string{"owners", "remove", "--hub", "h", "--user-id", "u"}, &out, &eout))
	r.Contains(eout.String(), "409")
}

// newCLITempEnv sets FOTOBANK_CONFIG + FOTOBANK_DB_PATH to t.TempDir()-backed
// values and starts a fixture daemon; returns the tempdir.
func newCLITempEnv(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	cfg := filepath.Join(tmp, "c.toml")
	require.NoError(t, os.WriteFile(cfg, fmt.Appendf(nil, `
[nas]
root = %q
[flash]
root = %q
[identity]
mode = "stub"
[identity.stub]
hub = "h"
user_id = "u"
storage_key = "550e8400-e29b-41d4-a716-446655440000"
`, filepath.Join(tmp, "nas"), filepath.Join(tmp, "flash")), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(tmp, "nas"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(tmp, "flash"), 0o700))
	t.Setenv("FOTOBANK_CONFIG", cfg)
	t.Setenv("FOTOBANK_DB_PATH", filepath.Join(tmp, "fotobank.sqlite"))
	startCheckoutServer(t, cfg, filepath.Join(tmp, "fotobank.sqlite"))
	return tmp
}
