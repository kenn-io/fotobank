package cli_test

import (
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.kenn.io/fotobank/internal/auth/hidden"
	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/media"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/testutil"
)

func TestHiddenInputValidationBeforeStartup(t *testing.T) {
	for _, tc := range []struct {
		args  []string
		input string
	}{
		{[]string{"hidden", "setup"}, "one\ntwo\n"},
		{[]string{"hidden", "setup"}, "\n\n"},
		{[]string{"hidden", "change"}, "old\nnew\ndifferent\n"},
		{[]string{"hidden", "disable"}, "passcode\nno\n"},
		{[]string{"admin", "reset-hidden-passcode", "--confirm", "--owner", "invalid"}, ""},
	} {
		t.Run(strings.Join(tc.args, " ")+tc.input, func(t *testing.T) {
			r := require.New(t)
			tmp := t.TempDir()
			cfg := writeBasicConfig(t, tmp)
			dbPath := filepath.Join(tmp, "catalog.sqlite")
			t.Setenv("FOTOBANK_DB_PATH", dbPath)
			_, _, code := runHiddenCLIIn(tc.input, append(tc.args, "--config", cfg)...)
			r.NotZero(code)
			r.NoFileExists(dbPath)
			r.NoDirExists(dbPath + ".operator")
		})
	}
}

func TestHiddenResetOperatorBoundary(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfg := writeNonStubConfig(t, tmp)
	dbPath := filepath.Join(tmp, "catalog.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)
	seedOwnerDirectly(t, dbPath, "h", "u")
	seedCredentialInDB(t, dbPath, "h", "u")
	id := seedHiddenMedia(t, dbPath, "h", "u")
	record := startCheckoutServer(t, cfg, dbPath)
	for _, tc := range []struct {
		name, base, token, body string
		status                  int
	}{
		{"no credential", record.Endpoint().BaseURL(), "", `{"owner":"h:u","confirm":true}`, 401},
		{"photo listener", record.Metadata["web_url"], record.Metadata["token"], `{"owner":"h:u","confirm":true}`, 403},
		{"no confirmation", record.Endpoint().BaseURL(), record.Metadata["token"], `{"owner":"h:u"}`, 422},
		{"false confirmation", record.Endpoint().BaseURL(), record.Metadata["token"], `{"owner":"h:u","confirm":false}`, 400},
		{"no owner", record.Endpoint().BaseURL(), record.Metadata["token"], `{"confirm":true}`, 400},
		{"operator", record.Endpoint().BaseURL(), record.Metadata["token"], `{"owner":"h:u","confirm":true}`, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := require.New(t)
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, tc.base+"/api/v1/operator/hidden/reset", strings.NewReader(tc.body))
			r.NoError(err)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Auth-Hub", "h")
			req.Header.Set("X-Auth-User-Id", "u")
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			response, err := http.DefaultClient.Do(req)
			r.NoError(err)
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			r.NoError(err)
			r.Equal(tc.status, response.StatusCode, "%s", body)
		})
	}
	r.True(hiddenAtIsSet(t, dbPath, id))
}

func TestHiddenCommandsRevokeOnlyTargetSessions(t *testing.T) {
	for _, tc := range []struct {
		name, input                        string
		args                               []string
		preserveHidden, preserveCredential bool
	}{
		{"change", "mypasscode\nnewpasscode\nnewpasscode\n", []string{"hidden", "change"}, true, true},
		{"disable", "mypasscode\nyes\n", []string{"hidden", "disable"}, false, false},
		{"reset", "", []string{"admin", "reset-hidden-passcode", "--confirm"}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := require.New(t)
			tmp := t.TempDir()
			cfg := writeBasicConfig(t, tmp)
			dbPath := filepath.Join(tmp, "catalog.sqlite")
			t.Setenv("FOTOBANK_DB_PATH", dbPath)
			seedOwnerDirectly(t, dbPath, "h", "u")
			seedCredentialInDB(t, dbPath, "h", "u")
			id := seedHiddenMedia(t, dbPath, "h", "u")
			d := testutil.OpenTestDBAt(t, dbPath)
			_, err := d.WriteDB().Exec(`INSERT INTO owners (hub, user_id, storage_key, created_at) VALUES ('h', 'other', '550e8400-e29b-41d4-a716-446655440001', ?)`, time.Now())
			r.NoError(err)
			repo := hidden.NewRepo(d.WriteDB(), d.ReadDB())
			svc := hidden.NewService(repo, media.NewRepo(d.WriteDB(), d.ReadDB()))
			owner := owners.Principal{Hub: "h", UserID: "u"}
			other := owners.Principal{Hub: "h", UserID: "other"}
			r.NoError(svc.Setup(t.Context(), other, "otherpass"))
			token, _, err := svc.Unlock(t.Context(), owner, "mypasscode")
			r.NoError(err)
			otherToken, _, err := svc.Unlock(t.Context(), other, "otherpass")
			r.NoError(err)
			startCheckoutServer(t, cfg, dbPath)
			_, stderr, code := runHiddenCLIIn(tc.input, append(tc.args, "--config", cfg)...)
			r.Zero(code, stderr)
			hash, err := hidden.TokenSHA256(token)
			r.NoError(err)
			_, err = repo.LookupActiveSession(t.Context(), hash, time.Now())
			r.ErrorIs(err, errs.ErrNotFound)
			hash, err = hidden.TokenSHA256(otherToken)
			r.NoError(err)
			_, err = repo.LookupActiveSession(t.Context(), hash, time.Now())
			r.NoError(err)
			r.Equal(tc.preserveHidden, hiddenAtIsSet(t, dbPath, id))
			_, err = repo.GetCredential(t.Context(), owner)
			if tc.preserveCredential {
				r.NoError(err)
				_, _, err = svc.Unlock(t.Context(), owner, "newpasscode")
				r.NoError(err)
			} else {
				r.ErrorIs(err, errs.ErrNotFound)
			}
			_, err = repo.GetCredential(t.Context(), other)
			r.NoError(err)
		})
	}
}
