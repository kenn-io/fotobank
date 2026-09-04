//go:build linux || darwin

package cli

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestInspectSQLiteFileRejectsFIFO(t *testing.T) {
	r := require.New(t)
	path := filepath.Join(t.TempDir(), "fotobank.sqlite")
	r.NoError(unix.Mkfifo(path, 0o600))

	result := make(chan error, 1)
	go func() {
		result <- inspectSQLiteFile(path)
	}()

	select {
	case err := <-result:
		r.ErrorContains(err, "regular file")
	case <-time.After(100 * time.Millisecond):
		// The prior implementation blocked while opening the FIFO. Supply a
		// valid header so the call can finish and expose the wrong result.
		file, err := os.OpenFile(path, os.O_WRONLY, 0)
		r.NoError(err)
		_, err = file.Write([]byte(sqliteHeader))
		r.NoError(err)
		r.NoError(file.Close())
		r.ErrorContains(<-result, "regular file")
	}
}
