package cli_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

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
	for _, args := range [][]string{
		{"backup", "init", "--repo", repository, "--json"},
		{"backup", "create", "--config", cfgPath, "--repo", repository, "--tag", "cli-drill", "--json"},
		{"backup", "verify", "--repo", repository, "--all", "--json"},
	} {
		stdout.Reset()
		stderr.Reset()
		code = cli.RunContext(t.Context(), args, &stdout, &stderr)
		r.Equal(0, code, "%v: %s", args, stderr.String())
		r.True(json.Valid(stdout.Bytes()), "%s", stdout.String())
	}
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
}

func TestBackupCreateRequiresInitializedRepository(t *testing.T) {
	r := require.New(t)
	repository := filepath.Join(t.TempDir(), "missing")
	var stdout, stderr bytes.Buffer
	code := cli.RunContext(t.Context(), []string{"backup", "create", "--repo", repository}, &stdout, &stderr)
	r.NotEqual(0, code)
	r.NoDirExists(repository)
}
