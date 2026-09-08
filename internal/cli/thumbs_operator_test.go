package cli_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/fotobank/internal/cli"
	"go.kenn.io/fotobank/internal/db"
	"go.kenn.io/fotobank/internal/httpapi"
	"go.kenn.io/fotobank/internal/owners"
)

func TestThumbsOperatorBoundary(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfg := writeBasicConfig(t, tmp)
	dbPath := filepath.Join(tmp, "catalog.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)
	m := seedReadyRow(t, dbPath)
	record := startCheckoutServer(t, cfg, dbPath)
	for _, tc := range []struct {
		name, base, token, body string
		status                  int
	}{
		{"unauthenticated", record.Endpoint().BaseURL(), "", `{"all":true}`, 401},
		{"photo listener", record.Metadata["web_url"], record.Metadata["token"], `{"all":true}`, 403},
		{"no selector", record.Endpoint().BaseURL(), record.Metadata["token"], `{}`, 400},
		{"conflicting scope", record.Endpoint().BaseURL(), record.Metadata["token"], `{"all":true,"owner":"h:u","all_owners":true}`, 400},
		{"invalid type", record.Endpoint().BaseURL(), record.Metadata["token"], `{"type":"invalid"}`, 422},
		{"invalid ID", record.Endpoint().BaseURL(), record.Metadata["token"], `{"ids":["not-a-uuid"]}`, 422},
		{"operator", record.Endpoint().BaseURL(), record.Metadata["token"], `{"ids":["` + m.ID + `"]}`, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := require.New(t)
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, tc.base+"/api/v1/operator/thumbs/regenerate", strings.NewReader(tc.body))
			r.NoError(err)
			req.Header.Set("Content-Type", "application/json")
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			resp, err := http.DefaultClient.Do(req)
			r.NoError(err)
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			r.NoError(err)
			r.Equal(tc.status, resp.StatusCode, "%s", body)
			if tc.status == 200 {
				r.Contains(string(body), `"enqueued":1`)
			}
		})
	}
	var out, stderr bytes.Buffer
	r.Zero(cli.RunContext(t.Context(), []string{"thumbs", "regenerate", "--config", cfg, "--id", m.ID, "--json"}, &out, &stderr), "%s", stderr.String())
	var result struct {
		Items []struct {
			Hub      string `json:"hub"`
			UserID   string `json:"user_id"`
			Enqueued int    `json:"enqueued"`
		} `json:"items"`
	}
	r.NoError(json.Unmarshal(out.Bytes(), &result))
	r.Len(result.Items, 1)
	r.Equal("h", result.Items[0].Hub)
	r.Equal("u", result.Items[0].UserID)
	r.Equal(1, result.Items[0].Enqueued)
}

func TestThumbsFiltersThroughDaemon(t *testing.T) {
	tmp := t.TempDir()
	cfg := writeBasicConfig(t, tmp)
	dbPath := filepath.Join(tmp, "catalog.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)
	seedReadyRow(t, dbPath)
	startCheckoutServer(t, cfg, dbPath)
	for _, flags := range [][]string{{"--type", "video"}, {"--all", "--type", "video"}, {"--status", "failed"}, {"--since", "2999-01-01T00:00:00Z"}} {
		t.Run(strings.Join(flags, " "), func(t *testing.T) {
			r := require.New(t)
			var out, stderr bytes.Buffer
			args := append([]string{"thumbs", "regenerate", "--config", cfg, "--json"}, flags...)
			r.Zero(cli.RunContext(t.Context(), args, &out, &stderr), "%s", stderr.String())
			var result struct {
				Items []struct {
					Enqueued int `json:"enqueued"`
				} `json:"items"`
			}
			r.NoError(json.Unmarshal(out.Bytes(), &result))
			r.Len(result.Items, 1)
			r.Zero(result.Items[0].Enqueued)
		})
	}
}

func TestThumbsInvalidSelectorsBeforeStartup(t *testing.T) {
	for _, flags := range [][]string{{"--type", "invalid"}, {"--status", "invalid"}, {"--id", "not-a-uuid"}} {
		t.Run(strings.Join(flags, " "), func(t *testing.T) {
			r := require.New(t)
			tmp := t.TempDir()
			cfg := writeBasicConfig(t, tmp)
			dbPath := filepath.Join(tmp, "catalog.sqlite")
			t.Setenv("FOTOBANK_DB_PATH", dbPath)
			var out, stderr bytes.Buffer
			args := append([]string{"thumbs", "regenerate", "--config", cfg}, flags...)
			r.Equal(2, cli.RunContext(t.Context(), args, &out, &stderr))
			r.NoFileExists(dbPath)
			r.NoDirExists(dbPath + ".operator")
		})
	}
}

func TestThumbsPartialFailureReportsCompletedOwners(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfg := writeNonStubConfig(t, tmp)
	dbPath := filepath.Join(tmp, "catalog.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)
	seedRowForOwner(t, dbPath, owners.Principal{Hub: "h", UserID: "alice"})
	seedRowForOwner(t, dbPath, owners.Principal{Hub: "h", UserID: "bob"})
	database, err := db.Open(dbPath)
	r.NoError(err)
	defer database.Close()
	// Force the second owner's enqueue to fail after the first commits.
	_, err = database.WriteDB().ExecContext(t.Context(), `CREATE TRIGGER fail_bob_enqueue
		BEFORE UPDATE OF thumb_version ON assets WHEN OLD.owner_user_id = 'bob'
		BEGIN SELECT RAISE(ABORT, 'enqueue failed'); END`)
	r.NoError(err)
	startCheckoutServer(t, cfg, dbPath)
	var out, stderr bytes.Buffer
	r.Equal(1, cli.RunContext(t.Context(), []string{"thumbs", "regenerate", "--config", cfg, "--all-owners", "--all", "--json"}, &out, &stderr))
	var result httpapi.RegenerateThumbsResult
	r.NoError(json.Unmarshal(out.Bytes(), &result))
	r.NotEmpty(result.Error)
	r.Len(result.Items, 1)
	r.Equal("alice", result.Items[0].UserID)
	r.Equal(1, result.Items[0].Enqueued)
}
