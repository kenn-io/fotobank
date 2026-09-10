package cli_test

import (
	"bytes"
	json "encoding/json/v2"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/cli"
	"go.kenn.io/fotobank/internal/db"
	"go.kenn.io/fotobank/internal/httpapi"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/share"
)

func runSharesCLI(t *testing.T, cfgPath string, args ...string) (string, string, int) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	command := append([]string{"shares"}, args...)
	command = append(command, "--config", cfgPath)
	code := cli.RunContext(t.Context(), command, &stdout, &stderr)
	return stdout.String(), stderr.String(), code
}

func TestSharesRetryConflictThroughDaemon(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeBasicConfig(t, tmp)
	dbPath := filepath.Join(tmp, "catalog.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)
	m := seedReadyRow(t, dbPath)
	startCheckoutServer(t, cfgPath, dbPath)
	out, stderr, code := runSharesCLI(t, cfgPath, "create", "--media", m.ID, "--grantee", "h:guest")
	r.Zero(code, "%s", stderr)
	var created struct {
		UUID string `json:"uuid"`
	}
	r.NoError(json.Unmarshal([]byte(out), &created))
	r.NotEmpty(created.UUID)
	_, stderr, code = runSharesCLI(t, cfgPath, "retry", created.UUID)
	r.Equal(1, code, "%s", stderr)
	r.Contains(stderr, "409")
	_, stderr, code = runSharesCLI(t, cfgPath, "revoke", created.UUID)
	r.Zero(code, "%s", stderr)
	_, stderr, code = runSharesCLI(t, cfgPath, "revoke", created.UUID)
	r.Equal(1, code, "%s", stderr)
	r.Contains(stderr, "409")
}

func TestSharesLiveWorkflow(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeBasicConfig(t, tmp)
	dbPath := filepath.Join(tmp, "catalog.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)
	m := seedReadyRow(t, dbPath)
	database, err := db.Open(dbPath)
	r.NoError(err)
	defer database.Close()
	repo := share.NewRepo(database.WriteDB(), database.ReadDB())
	failedID := "550e8400-e29b-41d4-a716-446655440001"
	r.NoError(repo.Insert(t.Context(), share.Scope{
		UUID: failedID, Owner: m.Owner, Grantee: owners.Principal{Hub: "h", UserID: "guest"},
		TargetType: share.TargetMediaSet, BrokerStatus: share.StatusFailed, CreatedAt: time.Now().UTC(),
	}, []string{m.ID}))
	other := owners.Principal{Hub: "h", UserID: "other-owner"}
	r.NoError(owners.NewRepo(database.WriteDB(), database.ReadDB()).Insert(t.Context(), owners.Owner{
		Principal: other, StorageKey: "550e8400-e29b-41d4-a716-446655440002", CreatedAt: time.Now().UTC(),
	}))
	otherID := "550e8400-e29b-41d4-a716-446655440003"
	r.NoError(repo.Insert(t.Context(), share.Scope{
		UUID: otherID, Owner: other, Grantee: owners.Principal{Hub: "h", UserID: "guest"},
		TargetType: share.TargetMediaSet, BrokerStatus: share.StatusFailed, CreatedAt: time.Now().UTC(),
	}, nil))
	r.NoError(database.Close())
	startCheckoutServer(t, cfgPath, dbPath)
	out, stderr, code := runSharesCLI(t, cfgPath, "create", "--media", m.ID, "--grantee", "h:guest", "--label", "Trip", "--allow-download", "--expires", "2099-01-01T00:00:00Z")
	r.Zero(code, "%s", stderr)
	var created httpapi.ScopeDTO
	r.NoError(json.Unmarshal([]byte(out), &created))
	r.NotEmpty(created.UUID)
	r.True(created.AllowDownload)
	r.Equal("Trip", created.Label)
	r.NotNil(created.ExpiresAt)
	r.Equal(2099, created.ExpiresAt.Year())
	out, stderr, code = runSharesCLI(t, cfgPath, "show", created.UUID)
	r.Zero(code, "%s", stderr)
	var detail httpapi.ScopeDTO
	r.NoError(json.Unmarshal([]byte(out), &detail))
	r.Equal([]string{m.ID}, detail.MediaIDs)
	out, stderr, code = runSharesCLI(t, cfgPath, "list", "--json", "--grantee", "h:guest", "--status", "failed")
	r.Zero(code, "%s", stderr)
	var page httpapi.ShareListResult
	r.NoError(json.Unmarshal([]byte(out), &page))
	r.Len(page.Items, 1)
	r.Equal(failedID, page.Items[0].UUID)
	out, stderr, code = runSharesCLI(t, cfgPath, "list", "--json", "--limit", "1")
	r.Zero(code, "%s", stderr)
	r.NoError(json.Unmarshal([]byte(out), &page))
	r.Len(page.Items, 1)
	r.NotNil(page.NextOffset)
	r.Equal(1, *page.NextOffset)
	firstPageID := page.Items[0].UUID
	out, stderr, code = runSharesCLI(t, cfgPath, "list", "--json", "--limit", "1", "--offset", "1")
	r.Zero(code, "%s", stderr)
	page = httpapi.ShareListResult{}
	r.NoError(json.Unmarshal([]byte(out), &page))
	r.Len(page.Items, 1)
	r.NotEqual(firstPageID, page.Items[0].UUID)
	r.Nil(page.NextOffset)
	out, stderr, code = runSharesCLI(t, cfgPath, "retry", failedID)
	r.Zero(code, "%s", stderr)
	r.NoError(json.Unmarshal([]byte(out), &detail))
	r.NotEqual("failed", detail.BrokerStatus)
	albumOut, stderr, code := runAlbumsCLI("albums", "create", "Shared album", "--config", cfgPath)
	r.Zero(code, "%s", stderr)
	albumID := strings.Fields(albumOut)[0]
	_, stderr, code = runAlbumsCLI("albums", "add", albumID, m.ID, "--config", cfgPath)
	r.Zero(code, "%s", stderr)
	out, stderr, code = runSharesCLI(t, cfgPath, "create", "--album", albumID, "--grantee", "h:guest")
	r.Zero(code, "%s", stderr)
	created = httpapi.ScopeDTO{}
	r.NoError(json.Unmarshal([]byte(out), &created))
	r.Equal("album_live", created.TargetType)
	r.Equal(albumID, created.TargetAlbumID)
	out, stderr, code = runSharesCLI(t, cfgPath, "list", "--json", "--album", albumID)
	r.Zero(code, "%s", stderr)
	page = httpapi.ShareListResult{}
	r.NoError(json.Unmarshal([]byte(out), &page))
	r.Len(page.Items, 1)
	r.Equal(created.UUID, page.Items[0].UUID)
	out, stderr, code = runSharesCLI(t, cfgPath, "list", "--album", albumID)
	r.Zero(code, "%s", stderr)
	r.Contains(out, created.UUID)
	r.Contains(out, "h:guest")
	for _, action := range []string{"show", "revoke", "retry"} {
		_, stderr, code = runSharesCLI(t, cfgPath, action, otherID)
		r.Equal(1, code, "%s", stderr)
		r.Contains(stderr, "404")
	}
}

func TestSharesInvalidArgumentsBeforeStartup(t *testing.T) {
	for _, args := range [][]string{
		{"create", "--media", ",,", "--grantee", "h:guest"},
		{"create", "--album", "invalid", "--grantee", "h:guest"},
		{"create", "--album", "550e8400-e29b-41d4-a716-446655440000", "--grantee", "no-colon"},
		{"create", "--album", "550e8400-e29b-41d4-a716-446655440000", "--grantee", strings.Repeat("h", share.PrincipalFieldMaxLen+1) + ":guest"},
		{"list", "--grantee", "h:" + strings.Repeat("u", share.PrincipalFieldMaxLen+1)},
		{"create", "--media", "550e8400-e29b-41d4-a716-446655440000", "--grantee", "h:guest", "--expires", "invalid"},
		{"list", "--status", "unknown"}, {"list", "--grantee", "no-colon"},
		{"show", "invalid"}, {"revoke", "invalid"}, {"retry", "invalid"},
	} {
		t.Run(args[0], func(t *testing.T) {
			r := require.New(t)
			tmp := t.TempDir()
			cfgPath := writeBasicConfig(t, tmp)
			dbPath := filepath.Join(tmp, "catalog.sqlite")
			t.Setenv("FOTOBANK_DB_PATH", dbPath)
			_, stderr, code := runSharesCLI(t, cfgPath, args...)
			r.Equal(2, code, "%s", stderr)
			r.NoFileExists(dbPath)
			r.NoDirExists(cfgPath + ".operator")
		})
	}
}

func TestSharesRejectHeaderBeforeStartup(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeNonStubConfig(t, tmp)
	dbPath := filepath.Join(tmp, "catalog.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)
	_, stderr, code := runSharesCLI(t, cfgPath, "list")
	r.Equal(1, code)
	r.Contains(stderr, "identity.mode = stub")
	r.NoFileExists(dbPath)
}

func TestSharesCmdUsageErrorWithoutSub(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cli.Run([]string{"shares"}, &stdout, &stderr)
	require.Equal(t, 2, code)
	require.Contains(t, stderr.String(), "usage")
}

func TestSharesCreateMissingGrantee(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cli.Run([]string{"shares", "create"}, &stdout, &stderr)
	// Cobra's MarkFlagRequired("grantee") produces a flag parse error,
	// which root.go maps to usageError → exit 2.
	require.Equal(t, 2, code)
}

func TestParseHubUser(t *testing.T) {
	r := require.New(t)
	cases := []struct {
		in      string
		wantHub string
		wantUID string
		wantErr bool
	}{
		{"h:u", "h", "u", false},
		{"alice.hub:bob-123", "alice.hub", "bob-123", false},
		// Multi-colon: first ':' splits; remaining colons belong to UserID.
		{"h:u:extra", "h", "u:extra", false},
		{"", "", "", true},
		{":u", "", "", true},
		{"h:", "", "", true},
		{"noColon", "", "", true},
	}
	for _, tc := range cases {
		got, err := cli.ParseHubUserForTest(tc.in)
		if tc.wantErr {
			r.Errorf(err, "in=%q", tc.in)
			continue
		}
		r.NoErrorf(err, "in=%q", tc.in)
		r.Equalf(tc.wantHub, got.Hub, "in=%q", tc.in)
		r.Equalf(tc.wantUID, got.UserID, "in=%q", tc.in)
	}
}

func TestSplitCSV(t *testing.T) {
	r := require.New(t)
	r.Nil(cli.SplitCSVForTest(""))
	r.Equal([]string{"a"}, cli.SplitCSVForTest("a"))
	r.Equal([]string{"a", "b"}, cli.SplitCSVForTest("a,b"))
	r.Equal([]string{"a", "b"}, cli.SplitCSVForTest(" a , b "))
	r.Empty(cli.SplitCSVForTest(",,,"))
	r.Empty(cli.SplitCSVForTest("  ,  "))
}

func TestShortUUID(t *testing.T) {
	r := require.New(t)
	r.Equal("abcd", cli.ShortUUIDForTest("abcd"))
	r.Equal("abcdefgh", cli.ShortUUIDForTest("abcdefgh"))
	r.Equal("abcd..7890", cli.ShortUUIDForTest("abcd12345677890"))
}

func TestSharesCreateExtraArgs(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cli.Run([]string{"shares", "create", "--grantee", "h:u", "--album", "x", "extra"},
		&stdout, &stderr)
	require.Equal(t, 2, code)
}

func TestSharesListExtraArgs(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cli.Run([]string{"shares", "list", "extra"}, &stdout, &stderr)
	require.Equal(t, 2, code)
}
