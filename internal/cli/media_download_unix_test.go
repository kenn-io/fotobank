//go:build !windows

package cli_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"
	"uuid"

	"github.com/stretchr/testify/require"
	"go.kenn.io/fotobank/internal/cli"
	"go.kenn.io/fotobank/internal/config"
	"go.kenn.io/fotobank/internal/version"
	"go.kenn.io/kit/daemon"
)

func TestMediaDownloadInterruptCleanup(t *testing.T) {
	if os.Getenv("FOTOBANK_TEST_DOWNLOAD_INTERRUPT") == "1" {
		// Use the executable's entry path, which supplies a background context.
		os.Exit(cli.Run(os.Args[slices.Index(os.Args, "--")+1:], os.Stdout, os.Stderr))
	}
	r := require.New(t)
	dir := t.TempDir()
	cfgPath := writeBasicConfig(t, dir)
	t.Setenv("FOTOBANK_DB_PATH", filepath.Join(dir, "catalog.sqlite"))
	cfg, err := config.LoadUnchecked(cfgPath)
	r.NoError(err)
	selection, err := config.CatalogSelection(cfg)
	r.NoError(err)
	mux := http.NewServeMux()
	server := httptest.NewUnstartedServer(mux)
	record := daemon.NewRuntimeRecord("fotobank-operator", version.Short,
		daemon.Endpoint{Network: daemon.NetworkTCP, Address: server.Listener.Addr().String()})
	record.Metadata = map[string]string{"token": "synthetic-download-test-token", "catalog_selection": selection}
	proof, err := daemon.NewProof([]byte(record.Metadata["token"]))
	r.NoError(err)
	ping, err := proof.NewPingHandler(record)
	r.NoError(err)
	mux.Handle(daemon.DefaultPingPath, ping)
	mux.HandleFunc("GET /api/v1/operator/daemon", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"running":true}`)
	})
	id := uuid.New().String()
	mux.HandleFunc("GET /api/v1/media/"+id, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"size":5,"sha256":"2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"}`)
	})
	started := make(chan struct{})
	mux.HandleFunc("GET /api/v1/media/"+id+"/original", func(w http.ResponseWriter, request *http.Request) {
		_, _ = io.WriteString(w, "hel")
		_ = http.NewResponseController(w).Flush()
		close(started)
		<-request.Context().Done()
	})
	server.Start()
	defer server.Close()
	_, err = (daemon.RuntimeStore{Dir: cfgPath + ".operator"}).Write(record)
	r.NoError(err)
	executable, err := os.Executable()
	r.NoError(err)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	destination := t.TempDir()
	command := exec.CommandContext(ctx, executable, "-test.run=^TestMediaDownloadInterruptCleanup$", "--",
		"media", "download", id, "--output", filepath.Join(destination, "photo.jpg"), "--config", cfgPath)
	command.Env = append(os.Environ(), "FOTOBANK_TEST_DOWNLOAD_INTERRUPT=1")
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	r.NoError(command.Start())
	done := make(chan struct{})
	var waitErr error
	go func() { waitErr = command.Wait(); close(done) }()
	defer func() { cancel(); <-done }()
	select {
	case <-started:
	case <-done:
		r.FailNow("download exited before streaming", "%v: %s", waitErr, stderr.String())
	case <-ctx.Done():
		cancel()
		<-done
		r.FailNow("download did not start", "%s", stderr.String())
	}
	r.NoError(command.Process.Signal(os.Interrupt))
	<-done
	r.Error(waitErr)
	r.Empty(stdout.String())
	files, err := os.ReadDir(destination)
	r.NoError(err)
	r.Empty(files, "Ctrl-C must remove the temporary file without publishing a partial download")
	r.Contains(stderr.String(), "read download:")
}
