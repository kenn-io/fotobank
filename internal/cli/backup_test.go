package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/cli"
)

// writeBackupConfig creates a TOML config sufficient for backup commands.
func writeBackupConfig(t *testing.T, tmp string) string {
	t.Helper()
	cfgPath := filepath.Join(tmp, "fotobank.toml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(`
[nas]
root = "`+filepath.Join(tmp, "nas")+`"
[flash]
root = "`+filepath.Join(tmp, "flash")+`"
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
file_lock_path = "`+filepath.Join(tmp, "import.lock")+`"
`), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(tmp, "nas"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(tmp, "flash"), 0o700))
	return cfgPath
}

func TestBackupSnapshotCLIWritesFile(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeBackupConfig(t, tmp)

	// Create the DB by running an import (or any DB-touching subcommand).
	// Use the existing `import` subcommand on an empty source dir; it
	// creates the DB and writes no media rows but does run migrations.
	srcDir := filepath.Join(tmp, "src-empty")
	r.NoError(os.MkdirAll(srcDir, 0o700))

	var so, se bytes.Buffer
	code := cli.RunContext(context.Background(),
		[]string{"import", "--config", cfgPath, srcDir},
		&so, &se)
	r.Equal(0, code, "import bootstrap failed: %s / %s", so.String(), se.String())

	// Now take a snapshot.
	out := filepath.Join(tmp, "snap.sqlite")
	so.Reset()
	se.Reset()
	code = cli.RunContext(context.Background(),
		[]string{"backup", "snapshot", "--config", cfgPath, "--out", out},
		&so, &se)
	r.Equal(0, code, "snapshot failed: %s / %s", so.String(), se.String())
	r.FileExists(out)
}

func TestBackupSnapshotCLIJSON(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeBackupConfig(t, tmp)
	srcDir := filepath.Join(tmp, "src-empty")
	r.NoError(os.MkdirAll(srcDir, 0o700))

	var so, se bytes.Buffer
	code := cli.RunContext(context.Background(),
		[]string{"import", "--config", cfgPath, srcDir},
		&so, &se)
	r.Equal(0, code)

	out := filepath.Join(tmp, "snap.sqlite")
	so.Reset()
	code = cli.RunContext(context.Background(),
		[]string{"backup", "snapshot", "--config", cfgPath, "--out", out, "--json"},
		&so, &se)
	r.Equal(0, code)

	var got struct {
		Path       string `json:"path"`
		SizeBytes  int64  `json:"size_bytes"`
		DurationMs int64  `json:"duration_ms"`
		Timestamp  string `json:"timestamp"`
	}
	r.NoError(json.Unmarshal(so.Bytes(), &got))
	r.Equal(out, got.Path)
	r.Positive(got.SizeBytes)
}

func TestBackupListCLI(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeBackupConfig(t, tmp)
	srcDir := filepath.Join(tmp, "src-empty")
	r.NoError(os.MkdirAll(srcDir, 0o700))

	var so, se bytes.Buffer
	cli.RunContext(context.Background(),
		[]string{"import", "--config", cfgPath, srcDir}, &so, &se)

	// Take two snapshots into the configured backup dir.
	// The default dir layout is {nas}/.fotobank/snapshots; the snapshot
	// subcommand creates it via os.MkdirAll. Filenames carry nanosecond
	// precision so back-to-back invocations get distinct names without
	// any artificial sleep.
	for range 2 {
		so.Reset()
		code := cli.RunContext(context.Background(),
			[]string{"backup", "snapshot", "--config", cfgPath}, &so, &se)
		r.Equal(0, code, se.String())
	}

	so.Reset()
	code := cli.RunContext(context.Background(),
		[]string{"backup", "list", "--config", cfgPath, "--json"}, &so, &se)
	r.Equal(0, code)

	var got []struct {
		Timestamp string `json:"timestamp"`
		SizeBytes int64  `json:"size_bytes"`
		Path      string `json:"path"`
	}
	r.NoError(json.Unmarshal(so.Bytes(), &got))
	r.Len(got, 2)
}

func TestBackupRestoreCLIRefusesWhileServerRuns(t *testing.T) {
	// Spin up the server in stub mode; while it runs, attempt a restore
	// against a different temp DB. The error must wrap ErrServerHoldsLock.
	// (Keeping this minimal — the exhaustive integration is in T11.)

	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeBackupConfig(t, tmp)

	// Bootstrap DB.
	srcDir := filepath.Join(tmp, "src-empty")
	r.NoError(os.MkdirAll(srcDir, 0o700))
	var so, se bytes.Buffer
	cli.RunContext(context.Background(),
		[]string{"import", "--config", cfgPath, srcDir}, &so, &se)

	// Take a snapshot to use as restore source.
	snap := filepath.Join(tmp, "snap.sqlite")
	cli.RunContext(context.Background(),
		[]string{"backup", "snapshot", "--config", cfgPath, "--out", snap},
		&so, &se)

	// Boot the server (will hold the lifetime lock).
	t.Setenv("FOTOBANK_CONFIG", cfgPath)
	sink := filepath.Join(tmp, "addr")
	t.Setenv("FOTOBANK_TEST_LISTEN_ADDR_SINK", sink)
	srvCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan int, 1)
	go func() {
		var so, se bytes.Buffer
		done <- cli.RunContext(srvCtx, []string{"server"}, &so, &se)
	}()

	// Wait for the server to bind (proves the lock is acquired).
	require.Eventually(t, func() bool {
		_, err := os.Stat(sink)
		return err == nil
	}, 3*time.Second, 30*time.Millisecond)

	// Attempt restore; expect non-zero exit + stderr mentioning the lock.
	so.Reset()
	se.Reset()
	code := cli.RunContext(context.Background(),
		[]string{"backup", "restore", "--config", cfgPath, "--yes", snap},
		&so, &se)
	r.NotEqual(0, code)
	r.Contains(strings.ToLower(se.String()+so.String()),
		"another fotobank process",
		"stderr/stdout must explain the lock contention")

	cancel()
	<-done
}
