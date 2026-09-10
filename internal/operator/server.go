// Package operator serves host-operator commands separately from the photo API.
package operator

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"go.kenn.io/kit/daemon"

	"go.kenn.io/fotobank/internal/config"
	"go.kenn.io/fotobank/internal/httpapi"
)

const serviceName = "fotobank-operator"

type Server struct {
	Close        func()
	RemoveRecord func()
	Fatal        <-chan error
}

// Start serves host lifecycle and configured photo-owner operations. The caller
// holds the server lifetime lock. Close must precede storage cleanup, and
// RemoveRecord must follow storage and lifetime-lock cleanup.
func Start(ctx context.Context, configPath, version, address, webURL string, recovery bool, shutdown func(), deps httpapi.Deps) (*Server, error) {
	var databaseOverride string
	if !recovery {
		var err error
		databaseOverride, err = config.DatabaseOverride()
		if err != nil {
			return nil, err
		}
	}
	store := daemon.RuntimeStore{Dir: configPath + ".operator"}
	if err := store.CheckWritable(); err != nil {
		return nil, err
	}
	if err := daemon.RequireLoopback(address); err != nil {
		return nil, err
	}
	ln, err := (daemon.Endpoint{Network: daemon.NetworkTCP, Address: address}).Listen()
	if err != nil {
		return nil, err
	}
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		_ = ln.Close()
		return nil, err
	}
	credential := hex.EncodeToString(token)
	rec := daemon.NewRuntimeRecord(serviceName, version, daemon.Endpoint{Network: daemon.NetworkTCP, Address: ln.Addr().String()})
	// Kit atomically publishes the record inside a current-user-only directory.
	// The credential is never sent until the peer proves possession of it.
	rec.Metadata = map[string]string{"token": credential, "web_url": webURL, "database_override": databaseOverride}
	if recovery {
		rec.Metadata["mode"] = "recovery"
	}
	deps.Daemon = &httpapi.DaemonDeps{
		Status:   httpapi.DaemonStatus{Running: true, Recovery: recovery, PID: rec.PID, Version: version, Address: rec.Address, WebURL: webURL, StartedAt: &rec.StartedAt},
		Shutdown: shutdown,
	}
	proof, err := daemon.NewProof([]byte(credential))
	if err != nil {
		_ = ln.Close()
		return nil, err
	}
	ping, err := proof.NewPingHandler(rec)
	if err != nil {
		_ = ln.Close()
		return nil, err
	}
	handler, err := httpapi.New(deps)
	if err != nil {
		_ = ln.Close()
		return nil, err
	}
	requests, cancel := context.WithCancel(ctx)
	var handlers sync.WaitGroup
	var gate sync.Mutex
	closing := false
	srv := &http.Server{
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
		IdleTimeout: 30 * time.Second,
		BaseContext: func(net.Listener) context.Context { return requests },
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gate.Lock()
			if closing {
				gate.Unlock()
				http.Error(w, "server stopping", http.StatusServiceUnavailable)
				return
			}
			handlers.Add(1)
			gate.Unlock()
			defer handlers.Done()
			if r.URL.Path == daemon.DefaultPingPath {
				ping.ServeHTTP(w, r)
				return
			}
			if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+credential)) != 1 {
				http.Error(w, "operator authentication required", http.StatusUnauthorized)
				return
			}
			if recovery && !recoveryOperation(r.URL.Path) {
				http.Error(w, "photo operations unavailable in recovery mode; restart normally", http.StatusServiceUnavailable)
				return
			}
			handler.ServeHTTP(w, r)
		}),
	}
	runtimePath, err := store.Write(rec)
	if err != nil {
		cancel()
		_ = ln.Close()
		return nil, err
	}
	failures := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			failures <- fmt.Errorf("operator listener: %w", err)
		}
	}()
	return &Server{Close: sync.OnceFunc(func() {
		gate.Lock()
		closing = true
		gate.Unlock()
		cancel()
		_ = srv.Close()
		<-done
		handlers.Wait()
	}), RemoveRecord: sync.OnceFunc(func() { _ = os.Remove(runtimePath) }), Fatal: failures}, nil
}

func recoveryOperation(path string) bool {
	switch path {
	case "/api/v1/operator/daemon", "/api/v1/operator/daemon/stop", "/api/docs":
		return true
	default:
		return strings.HasPrefix(path, "/api/v1/operator/backup-repository/") ||
			strings.HasPrefix(path, "/api/openapi.") || strings.HasPrefix(path, "/api/schemas/")
	}
}
