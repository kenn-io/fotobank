package cli_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.kenn.io/fotobank/internal/backup"
	"go.kenn.io/fotobank/internal/cli"
	"go.kenn.io/fotobank/internal/content"
)

func TestE2EBackupWorkerProducesCompleteArchive(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e tests skipped under -short")
	}
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeBackupConfig(t, tmp)
	repository, err := content.InitBackupRepository(filepath.Join(tmp, "repository"))
	r.NoError(err)
	cfg, err := os.ReadFile(cfgPath)
	r.NoError(err)
	cfg = fmt.Appendf(cfg, "\n[backup]\nenabled=true\nrepository=%q\ninterval=\"1h\"\nkeep_last=2\n", repository.Root())
	r.NoError(os.WriteFile(cfgPath, cfg, 0o600))
	t.Setenv("FOTOBANK_DB_PATH", filepath.Join(tmp, "flash", "fotobank.sqlite"))
	sink := filepath.Join(tmp, "addr")
	t.Setenv("FOTOBANK_TEST_LISTEN_ADDR_SINK", sink)
	ctx, cancel := context.WithCancel(t.Context())
	var stdout, stderr bytes.Buffer
	done := make(chan int, 1)
	go func() { done <- cli.RunContext(ctx, []string{"server", "--config", cfgPath}, &stdout, &stderr) }()
	var stopOnce sync.Once
	stopServer := func() {
		stopOnce.Do(func() {
			cancel()
			select {
			case code := <-done:
				r.Equal(0, code, "%s", stderr.String())
			case <-time.After(10 * time.Second):
				r.Fail("server did not shut down")
			}
		})
	}
	t.Cleanup(stopServer)
	r.NotEmpty(waitForSink(t, sink))
	r.Eventually(func() bool {
		points, err := repository.Snapshots()
		return err == nil && len(points) > 0
	}, 10*time.Second, 30*time.Millisecond)
	// A recovery point is visible before the worker finishes retention. Join
	// the server so verification observes the completed repository operation.
	stopServer()
	// The first complete archive is published immediately, not after the
	// hour-long configured interval. Restore checks the captured catalog.
	points, err := repository.Snapshots()
	r.NoError(err)
	r.Equal(backup.ScheduledTag, points[0].Tag)
	report, err := repository.Verify(t.Context(), content.BackupVerifyOptions{SnapshotID: points[0].ID})
	r.NoError(err)
	r.Empty(report.Problems)
	_, err = backup.RestoreArchive(t.Context(), repository, points[0].ID, filepath.Join(tmp, "restored"), nil)
	r.NoError(err)
}
