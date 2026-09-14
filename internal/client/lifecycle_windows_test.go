//go:build windows

package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.kenn.io/kit/daemon"
	"golang.org/x/sys/windows"
)

func TestStopWaitsForDeletePendingRuntimeRecord(t *testing.T) {
	for _, tc := range []struct {
		name         string
		releaseAfter time.Duration
		stopTimeout  time.Duration
		wantErr      error
	}{
		{name: "deletion completes", releaseAfter: 200 * time.Millisecond, stopTimeout: 2 * time.Second},
		{name: "deletion stays pending", stopTimeout: 2 * time.Second, wantErr: context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := require.New(t)
			var deleted atomic.Bool
			var ping http.Handler
			var recordPath string
			var handle windows.Handle
			var releaseRecord func()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.URL.Path == daemon.DefaultPingPath {
					ping.ServeHTTP(w, req)
					return
				}
				// Keep the handle that marks deletion open until the test releases it.
				deleteOnClose := byte(1) // FILE_DISPOSITION_INFO.DeleteFile (BOOLEAN).
				if err := windows.SetFileInformationByHandle(handle, windows.FileDispositionInfo, &deleteOnClose, 1); err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				if _, err := os.Stat(recordPath); !errors.Is(err, os.ErrPermission) {
					http.Error(w, fmt.Sprintf("expected a delete-pending permission error, got %v", err), http.StatusInternalServerError)
					return
				}
				deleted.Store(true)
				if tc.releaseAfter > 0 {
					time.AfterFunc(tc.releaseAfter, releaseRecord)
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			t.Cleanup(server.Close)

			record := daemon.NewRuntimeRecord("fotobank-operator", "test", daemon.Endpoint{
				Network: daemon.NetworkTCP, Address: strings.TrimPrefix(server.URL, "http://"),
			})
			record.Metadata = map[string]string{"token": "synthetic-operator-credential"}
			proof, err := daemon.NewProof([]byte(record.Metadata["token"]))
			r.NoError(err)
			ping, err = proof.NewPingHandler(record)
			r.NoError(err)

			configPath := filepath.Join(t.TempDir(), "fotobank.toml")
			store := daemon.RuntimeStore{Dir: configPath + ".operator"}
			recordPath, err = store.Write(record)
			r.NoError(err)
			name, err := windows.UTF16PtrFromString(recordPath)
			r.NoError(err)
			handle, err = windows.CreateFile(name, windows.DELETE,
				windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
				nil, windows.OPEN_EXISTING, 0, 0)
			r.NoError(err)
			release := make(chan struct{})
			closed := make(chan error, 1)
			go func() {
				<-release
				closed <- windows.CloseHandle(handle)
			}()
			var releaseOnce sync.Once
			releaseRecord = func() { releaseOnce.Do(func() { close(release) }) }
			t.Cleanup(func() {
				releaseRecord()
				r.NoError(<-closed)
			})

			err = (Lifecycle{ConfigPath: configPath, StopTimeout: tc.stopTimeout}).stopRecord(t.Context(), record)
			r.True(deleted.Load())
			if tc.wantErr == nil {
				r.NoError(err)
			} else {
				r.ErrorIs(err, tc.wantErr)
			}
		})
	}
}
