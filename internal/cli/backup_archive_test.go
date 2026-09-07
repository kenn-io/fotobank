package cli_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/kit/daemon"

	"go.kenn.io/fotobank/internal/backup"
	"go.kenn.io/fotobank/internal/cli"
	"go.kenn.io/fotobank/internal/content"
)

func TestBackupArchiveCLI(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeBackupConfig(t, tmp)
	source := seedImportSource(t, "photo-no-exif.jpg")
	var stdout, stderr bytes.Buffer
	code := cli.RunContext(t.Context(), []string{"import", "--config", cfgPath, source}, &stdout, &stderr)
	r.Equal(0, code, "%s", stderr.String())
	repository := filepath.Join(tmp, "repository")
	t.Run("capture with running server", func(t *testing.T) {
		r := require.New(t)
		t.Chdir(tmp)
		record := startCheckoutServer(t, cfgPath, filepath.Join(tmp, "flash", "fotobank.sqlite"))
		for _, args := range [][]string{
			{"backup", "init", "--repo", repository, "--json"},
			{"backup", "create", "--config", cfgPath, "--repo", "repository", "--tag", "cli-drill", "--json"},
			{"backup", "verify", "--repo", repository, "--all", "--json"},
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
		r.Zero(code, "%s", stderr.String())
		var restored backup.ArchiveRestoreReport
		r.NoError(json.Unmarshal(stdout.Bytes(), &restored))
		r.Positive(restored.ReferencesVerified)
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
				request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, record.Endpoint().BaseURL()+"/backups", bytes.NewReader(body))
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
	stdout.Reset()
	stderr.Reset()
	code = cli.RunContext(t.Context(), []string{"backup", "list", "--repo", repository, "--json"}, &stdout, &stderr)
	r.Equal(0, code, "%s", stderr.String())
	var snapshots []content.BackupSnapshot
	r.NoError(json.Unmarshal(stdout.Bytes(), &snapshots))
	r.Len(snapshots, 1)
	r.Equal("cli-drill", snapshots[0].Tag)
	r.Positive(snapshots[0].BlobBytes)
	stdout.Reset()
	stderr.Reset()
	code = cli.RunContext(t.Context(), []string{"backup", "verify", snapshots[0].ID, "--repo", repository}, &stdout, &stderr)
	r.Equal(0, code, "%s", stderr.String())
	// Recovery must not bootstrap or open the lost source installation.
	r.NoError(os.RemoveAll(filepath.Join(tmp, "offline-flash")))
	r.NoError(os.RemoveAll(filepath.Join(tmp, "nas")))
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

func TestBackupCreateRequiresRunningServer(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeBackupConfig(t, tmp)
	var stdout, stderr bytes.Buffer
	code := cli.RunContext(t.Context(), []string{"backup", "create", "--config", cfgPath, "--repo", filepath.Join(tmp, "repository"), "--json"}, &stdout, &stderr)
	r.NotZero(code)
	r.Contains(stderr.String(), "start fotobank serve")
	var failure struct {
		Error string `json:"error"`
	}
	r.NoError(json.Unmarshal(stdout.Bytes(), &failure))
	r.Contains(failure.Error, "start fotobank serve")
	r.NoFileExists(filepath.Join(tmp, "flash", "fotobank.sqlite"))
}
