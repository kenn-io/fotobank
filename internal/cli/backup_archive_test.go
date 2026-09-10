package cli_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.kenn.io/kit/daemon"

	"go.kenn.io/fotobank/internal/backup"
	"go.kenn.io/fotobank/internal/cli"
	"go.kenn.io/fotobank/internal/client"
	"go.kenn.io/fotobank/internal/content"
	"go.kenn.io/fotobank/internal/httpapi"
)

func TestBackupArchiveCLI(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeBackupConfig(t, tmp)
	source := seedImportSource(t, "photo-no-exif.jpg")
	var stdout, stderr bytes.Buffer
	code := runLiveImport(t, t.Context(), cfgPath, filepath.Join(tmp, "flash", "fotobank.sqlite"), []string{"import", "--config", cfgPath, source}, &stdout, &stderr)
	r.Equal(0, code, "%s", stderr.String())
	repository := filepath.Join(tmp, "repository")
	t.Run("capture with running server", func(t *testing.T) {
		r := require.New(t)
		t.Chdir(tmp)
		record := startCheckoutServer(t, cfgPath, filepath.Join(tmp, "flash", "fotobank.sqlite"))
		response, err := http.Get(record.Metadata["web_url"] + "/api/v1/operator/backup-repository/snapshots?repository=/unused")
		r.NoError(err)
		r.NoError(response.Body.Close())
		r.Equal(http.StatusForbidden, response.StatusCode)
		for _, args := range [][]string{
			{"backup", "init", "--config", cfgPath, "--repo", repository, "--json"},
			{"backup", "create", "--config", cfgPath, "--repo", "repository", "--tag", "cli-drill", "--json"},
			{"backup", "verify", "--config", cfgPath, "--repo", repository, "--all", "--json"},
		} {
			stdout.Reset()
			stderr.Reset()
			code = cli.RunContext(t.Context(), args, &stdout, &stderr)
			r.Equal(0, code, "%v: %s", args, stderr.String())
			r.True(json.Valid(stdout.Bytes()), "%s", stdout.String())
			if args[1] == "create" {
				var snapshot content.BackupSnapshot
				r.NoError(json.Unmarshal(stdout.Bytes(), &snapshot))
				r.NotEmpty(snapshot.ID)
				r.Equal("cli-drill", snapshot.Tag)
				r.Positive(snapshot.BlobBytes)
			}
		}
		stdout.Reset()
		stderr.Reset()
		code = cli.RunContext(t.Context(), []string{"backup", "restore", "--repo", repository,
			"--target", filepath.Join(tmp, "live-restore"), "--config", cfgPath, "--json"}, &stdout, &stderr)
		r.NotZero(code, "%s", stdout.String())
		r.Contains(stderr.String(), "recovery")
		r.NoDirExists(filepath.Join(tmp, "live-restore"))
		for _, baseURL := range []string{record.Metadata["web_url"], record.Endpoint().BaseURL()} {
			body, err := json.Marshal(map[string]string{"repository": repository, "target": filepath.Join(tmp, "direct-restore")})
			r.NoError(err)
			request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, baseURL+"/api/v1/operator/backup-repository/restore", bytes.NewReader(body))
			r.NoError(err)
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Authorization", "Bearer "+record.Metadata["token"])
			response, err := http.DefaultClient.Do(request)
			r.NoError(err)
			r.NoError(response.Body.Close())
			r.Equal(http.StatusForbidden, response.StatusCode)
		}
		r.NoDirExists(filepath.Join(tmp, "direct-restore"))
		for _, denied := range []struct {
			name, hub, token, tag string
			status                int
		}{
			{name: "missing credential", hub: "local", status: http.StatusUnauthorized},
			{name: "wrong owner", hub: "other", token: record.Metadata["token"], status: http.StatusForbidden},
			{name: "reserved tag", hub: "local", token: record.Metadata["token"], tag: backup.ScheduledTag, status: http.StatusBadRequest},
		} {
			t.Run(denied.name, func(t *testing.T) {
				r := require.New(t)
				body, err := json.Marshal(map[string]string{"hub": denied.hub, "user_id": "alice", "repository": repository, "tag": denied.tag})
				r.NoError(err)
				request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, record.Endpoint().BaseURL()+"/api/v1/operator/backups", bytes.NewReader(body))
				r.NoError(err)
				request.Header.Set("Content-Type", "application/json")
				request.Header.Set("Authorization", "Bearer "+denied.token)
				response, err := record.Endpoint().HTTPClient(daemon.HTTPClientOptions{DisableKeepAlives: true}).Do(request)
				r.NoError(err)
				defer response.Body.Close()
				r.Equal(denied.status, response.StatusCode)
			})
		}
	})
	// Listing and verification need only the repository, even after source loss.
	r.NoError(os.Rename(filepath.Join(tmp, "flash"), filepath.Join(tmp, "offline-flash")))
	recovery := startRecoveryServer(t, cfgPath)
	stdout.Reset()
	stderr.Reset()
	code = cli.RunContext(t.Context(), []string{"backup", "list", "--config", cfgPath, "--repo", repository, "--json"}, &stdout, &stderr)
	r.Equal(0, code, "%s", stderr.String())
	var snapshots []content.BackupSnapshot
	r.NoError(json.Unmarshal(stdout.Bytes(), &snapshots))
	r.Len(snapshots, 1)
	r.Equal("cli-drill", snapshots[0].Tag)
	r.Positive(snapshots[0].BlobBytes)
	stdout.Reset()
	stderr.Reset()
	code = cli.RunContext(t.Context(), []string{"backup", "verify", snapshots[0].ID, "--config", cfgPath, "--repo", repository}, &stdout, &stderr)
	r.Equal(0, code, "%s", stderr.String())
	// Recovery must not bootstrap or open the lost source installation.
	r.NoError(os.RemoveAll(filepath.Join(tmp, "offline-flash")))
	r.NoError(os.RemoveAll(filepath.Join(tmp, "nas")))
	t.Run("changed recovery storage configuration", func(t *testing.T) {
		r := require.New(t)
		original, err := os.ReadFile(cfgPath)
		r.NoError(err)
		t.Cleanup(func() { require.NoError(t, os.WriteFile(cfgPath, original, 0o600)) })
		changed := strings.ReplaceAll(string(original), strconv.Quote(filepath.Join(tmp, "nas")), strconv.Quote(filepath.Join(tmp, "replacement-nas")))
		r.NotEqual(string(original), changed)
		r.NoError(os.WriteFile(cfgPath, []byte(changed), 0o600))
		target := filepath.Join(tmp, "nas", "restored")
		_, err = client.RestoreArchive(t.Context(), cfgPath, recovery.Version,
			httpapi.ArchiveRestoreRequest{Repository: repository, Target: target})
		r.ErrorContains(err, "storage configuration changed")
		r.NoDirExists(target)
	})
	direct, err := client.RestoreArchive(t.Context(), cfgPath, recovery.Version,
		httpapi.ArchiveRestoreRequest{Repository: repository, Target: filepath.Join(tmp, "direct-recovery")})
	r.NoError(err)
	r.Positive(direct.ReferencesVerified)
	r.FileExists(direct.CatalogPath)
	_, err = client.RestoreArchive(t.Context(), cfgPath, recovery.Version,
		httpapi.ArchiveRestoreRequest{Repository: repository, Target: filepath.Join(tmp, "direct-recovery")})
	r.Error(err)
	r.Contains(err.Error(), "409")
	stdout.Reset()
	stderr.Reset()
	code = cli.RunContext(t.Context(), []string{"backup", "restore", "--repo", repository,
		"--target", filepath.Join(tmp, "restored"), "--config", cfgPath, "--json"}, &stdout, &stderr)
	r.Equal(0, code, "%s", stderr.String())
	var restored backup.ArchiveRestoreReport
	r.NoError(json.Unmarshal(stdout.Bytes(), &restored))
	r.Equal(snapshots[0].ID, restored.SnapshotID)
	r.Positive(restored.ReferencesVerified)
	r.FileExists(restored.CatalogPath)
	r.NoDirExists(filepath.Join(tmp, "flash"))
	r.NoDirExists(filepath.Join(tmp, "nas"))
	r.NoError((client.Lifecycle{ConfigPath: cfgPath, StopTimeout: 5 * time.Second}).Stop(t.Context()))
	for _, kind := range []string{"database", "parent", "nested-relative"} {
		t.Run("lost-symlink-"+kind, func(t *testing.T) {
			r := require.New(t)
			aliases := t.TempDir()
			lost := filepath.Join(t.TempDir(), "lost")
			alias := filepath.Join(aliases, "catalog")
			linkTarget := lost
			dbPath := filepath.Join(alias, "fotobank.sqlite")
			if kind == "database" {
				linkTarget = filepath.Join(lost, "fotobank.sqlite")
				dbPath = alias
			}
			if kind == "nested-relative" {
				parent := filepath.Join(filepath.Dir(lost), "state")
				r.NoError(os.Mkdir(parent, 0o700))
				parentAlias := filepath.Join(aliases, "parent")
				if err := os.Symlink(parent, parentAlias); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
				alias = filepath.Join(parentAlias, "catalog")
				dbPath = alias
				linkTarget = filepath.Join("..", "lost", "fotobank.sqlite")
			}
			if err := os.Symlink(linkTarget, alias); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			t.Setenv("FOTOBANK_DB_PATH", dbPath)
			startRecoveryServer(t, cfgPath)
			// A requesting client cannot change the daemon's source selection.
			t.Setenv("FOTOBANK_DB_PATH", filepath.Join(tmp, "client-only", "catalog.sqlite"))
			var stdout, stderr bytes.Buffer
			code := cli.RunContext(t.Context(), []string{"backup", "restore", "--repo", repository,
				"--target", filepath.Join(tmp, "recovered-"+kind), "--config", cfgPath}, &stdout, &stderr)
			r.Equal(0, code, "%s", stderr.String())
			r.NoDirExists(lost)
			for _, target := range []string{filepath.Join(filepath.Dir(dbPath), "restore"), filepath.Join(lost, "restore")} {
				stdout.Reset()
				stderr.Reset()
				code = cli.RunContext(t.Context(), []string{"backup", "restore", "--repo", repository,
					"--target", target, "--config", cfgPath}, &stdout, &stderr)
				r.NotEqual(0, code, "must not restore into either source location")
				r.NoDirExists(target)
			}
		})
	}
	startRecoveryServer(t, cfgPath)
	// The lost deployment's paths remain reserved even though they are absent.
	stdout.Reset()
	stderr.Reset()
	code = cli.RunContext(t.Context(), []string{"backup", "restore", "--repo", repository,
		"--target", filepath.Join(tmp, "flash", "restored"), "--config", cfgPath}, &stdout, &stderr)
	r.NotEqual(0, code)
	r.Contains(stderr.String(), "overlap")
	r.NoDirExists(filepath.Join(tmp, "flash"))
}

func TestBackupCreateRequiresInitializedRepository(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeBackupConfig(t, tmp)
	startCheckoutServer(t, cfgPath, filepath.Join(tmp, "flash", "fotobank.sqlite"))
	repository := filepath.Join(tmp, "missing")
	var stdout, stderr bytes.Buffer
	code := cli.RunContext(t.Context(), []string{"backup", "create", "--config", cfgPath, "--repo", repository}, &stdout, &stderr)
	r.NotEqual(0, code)
	r.Contains(strings.ToLower(stderr.String()), "repository")
	r.NoDirExists(repository)
}

func TestBackupCreateDoesNotOpenStorageWhenLaunchFails(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeBackupConfig(t, tmp)
	var stdout, stderr bytes.Buffer
	code := cli.RunContext(t.Context(), []string{"backup", "create", "--config", cfgPath, "--repo", filepath.Join(tmp, "repository"), "--json"}, &stdout, &stderr)
	r.NotZero(code)
	r.Contains(stderr.String(), "build fotobank")
	var failure struct {
		Error string `json:"error"`
	}
	r.NoError(json.Unmarshal(stdout.Bytes(), &failure))
	r.Contains(failure.Error, "build fotobank")
	r.NoFileExists(filepath.Join(tmp, "flash", "fotobank.sqlite"))
}
