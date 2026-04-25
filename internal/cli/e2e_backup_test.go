package cli_test

import (
	"bytes"
	"context"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	_ "modernc.org/sqlite"

	"github.com/wesm/fotobank/internal/cli"
)

// stableCopySnapshot copies one .sqlite file from snapDir to a stable
// path outside the worker-managed directory, returning that path. Used
// by e2e tests to pin a snapshot for downstream checks even though the
// worker is still running and may sweep the original at any moment.
func stableCopySnapshot(t *testing.T, snapDir, dst string) string {
	t.Helper()
	r := require.New(t)
	entries, err := os.ReadDir(snapDir)
	r.NoError(err)
	var src string
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".sqlite" {
			src = filepath.Join(snapDir, e.Name())
			break
		}
	}
	r.NotEmpty(src, "no snapshot to copy from %s", snapDir)
	in, err := os.Open(src)
	r.NoError(err)
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	r.NoError(err)
	_, err = io.Copy(out, in)
	r.NoError(err)
	r.NoError(out.Close())
	return dst
}

// writeBackupE2EConfig produces a TOML config sufficient for a server
// to boot in stub-identity mode with backup enabled. It mirrors the
// per-test config helper used by the smaller backup CLI tests but
// pins FOTOBANK_DB_PATH so server.go and backup-restore agree on the
// canonical DB location.
func writeBackupE2EConfig(t *testing.T, tmp string) string {
	t.Helper()
	r := require.New(t)
	nasRoot := filepath.Join(tmp, "nas")
	flashRoot := filepath.Join(tmp, "flash")
	r.NoError(os.MkdirAll(nasRoot, 0o700))
	r.NoError(os.MkdirAll(flashRoot, 0o700))

	cfgPath := filepath.Join(tmp, "fotobank.toml")
	r.NoError(os.WriteFile(cfgPath, []byte(`
[nas]
root = "`+nasRoot+`"
[flash]
root = "`+flashRoot+`"
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
	return cfgPath
}

// TestE2EBackupWorkerProducesSnapshot boots a real server with the
// backup worker driven at 50ms ticks, waits for at least one snapshot
// to land in cfg.Backup.Dir, and integrity-checks it. This exercises
// the wiring in runServer plus the worker → snapshot path under
// realistic conditions (live WAL, identity bootstrap, etc.).
func TestE2EBackupWorkerProducesSnapshot(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e tests skipped under -short")
	}
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeBackupE2EConfig(t, tmp)

	flashRoot := filepath.Join(tmp, "flash")
	dbPath := filepath.Join(flashRoot, "fotobank.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)
	t.Setenv("FOTOBANK_TEST_BACKUP_INTERVAL", "50ms")
	sink := filepath.Join(tmp, "addr")
	t.Setenv("FOTOBANK_TEST_LISTEN_ADDR_SINK", sink)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	done := make(chan int, 1)
	go func() {
		var so, se bytes.Buffer
		done <- cli.RunContext(ctx, []string{"server", "--config", cfgPath}, &so, &se)
	}()

	addr := waitForSink(t, sink)
	r.NotEmpty(addr, "server did not bind")

	nasRoot := filepath.Join(tmp, "nas")
	snapDir := filepath.Join(nasRoot, ".fotobank", "snapshots")
	require.Eventually(t, func() bool {
		entries, err := os.ReadDir(snapDir)
		if err != nil {
			return false
		}
		for _, e := range entries {
			if filepath.Ext(e.Name()) == ".sqlite" {
				return true
			}
		}
		return false
	}, 5*time.Second, 30*time.Millisecond,
		"backup worker must produce at least one snapshot within 5s")

	// Stop the server before integrity-checking the snapshot. The
	// retention policy keeps one snapshot per 15-minute slot, so a
	// later worker tick at 50ms cadence could delete the file we
	// pick before PRAGMA integrity_check returns. Cancel + wait for
	// the worker to drain, then the snapshot dir is quiescent.
	cancel()
	select {
	case ec := <-done:
		r.Equal(0, ec)
	case <-time.After(5 * time.Second):
		r.Fail("server did not shut down")
	}

	// Pick the newest surviving snapshot and integrity-check it.
	// Filename timestamps sort lexicographically (RFC3339 UTC), so
	// the largest name corresponds to the newest snapshot.
	entries, err := os.ReadDir(snapDir)
	r.NoError(err)
	var newest string
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".sqlite" && e.Name() > newest {
			newest = e.Name()
		}
	}
	r.NotEmpty(newest)
	snapPath := filepath.Join(snapDir, newest)

	d, err := sql.Open("sqlite", "file:"+snapPath+"?mode=ro")
	r.NoError(err)
	t.Cleanup(func() { _ = d.Close() })
	var s string
	r.NoError(d.QueryRow("PRAGMA integrity_check").Scan(&s))
	r.Equal("ok", s, "snapshot must pass integrity_check")
}

// TestE2ERestoreRefusesWhileServerRuns proves the lifetime-lock fence:
// while a server is running and holding flock(dbPath.lock), an out-of-
// process `backup restore` must refuse to proceed instead of clobbering
// a live DB. The lock-held error path is the one production guarantee
// the restore CLI exists to enforce, so this case earns its own e2e
// even though TestBackupRestoreCLIRefusesWhileServerRuns covers it at
// a smaller scale.
func TestE2ERestoreRefusesWhileServerRuns(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e tests skipped under -short")
	}
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeBackupE2EConfig(t, tmp)

	flashRoot := filepath.Join(tmp, "flash")
	dbPath := filepath.Join(flashRoot, "fotobank.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)
	t.Setenv("FOTOBANK_TEST_BACKUP_INTERVAL", "50ms")
	sink := filepath.Join(tmp, "addr")
	t.Setenv("FOTOBANK_TEST_LISTEN_ADDR_SINK", sink)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	done := make(chan int, 1)
	go func() {
		var so, se bytes.Buffer
		done <- cli.RunContext(ctx, []string{"server", "--config", cfgPath}, &so, &se)
	}()

	addr := waitForSink(t, sink)
	r.NotEmpty(addr)

	// Wait for at least one snapshot to use as restore source.
	nasRoot := filepath.Join(tmp, "nas")
	snapDir := filepath.Join(nasRoot, ".fotobank", "snapshots")
	require.Eventually(t, func() bool {
		entries, err := os.ReadDir(snapDir)
		if err != nil {
			return false
		}
		for _, e := range entries {
			if filepath.Ext(e.Name()) == ".sqlite" {
				return true
			}
		}
		return false
	}, 5*time.Second, 30*time.Millisecond)

	// Copy the snapshot to a stable location outside snapDir so the
	// running worker's retention sweep cannot delete it between now
	// and the restore invocation. Without this, a 50ms-cadence
	// worker can race ahead, delete the file, and the restore would
	// then exit non-zero with a "stat snapshot" error rather than
	// the lock-held error this test is meant to assert.
	stableSnap := filepath.Join(tmp, "snap.sqlite")
	stableCopySnapshot(t, snapDir, stableSnap)

	// Attempt restore; must fail with lock-held. The exit code alone
	// is insufficient because many unrelated failures also exit
	// non-zero — assert the error text contains the lock-held
	// signature so a regression in the lock fence cannot pass this
	// test by failing for a different reason.
	var so, se bytes.Buffer
	code := cli.RunContext(context.Background(),
		[]string{"backup", "restore", "--config", cfgPath, "--yes", stableSnap},
		&so, &se)
	r.NotEqual(0, code, "restore must fail while server holds the lock")
	r.Contains(strings.ToLower(se.String()+so.String()),
		"another fotobank process",
		"stderr/stdout must explain the lock contention, not some unrelated failure")

	cancel()
	<-done
}
