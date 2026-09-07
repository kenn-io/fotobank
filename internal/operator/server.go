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
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"go.kenn.io/kit/daemon"

	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/service"
)

const serviceName = "fotobank-operator"

// CommitResult preserves completed work even when another entry fails.
type CommitResult struct {
	CheckoutID string `json:"checkout_id"`
	Pending    int    `json:"pending"`
	Committed  int    `json:"committed"`
	Conflicts  int    `json:"conflicts"`
	Error      string `json:"error,omitempty"`
}

type commitInput struct {
	CheckoutID string `path:"id"`
	Body       struct {
		Hub    string `json:"hub"`
		UserID string `json:"user_id"`
	}
}

// Start serves only the configured owner. The caller already holds the server
// lifetime lock. close must finish before its services or vault are closed.
func Start(ctx context.Context, dbPath, version string, owner owners.Principal, checkouts *service.CheckoutService, backups *service.BackupService) (stop func(), fatal <-chan error, err error) {
	store := daemon.RuntimeStore{Dir: dbPath + ".operator"}
	if err := store.CheckWritable(); err != nil {
		return nil, nil, err
	}
	ln, err := (daemon.Endpoint{Network: daemon.NetworkTCP, Address: "127.0.0.1:0"}).Listen()
	if err != nil {
		return nil, nil, err
	}
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		_ = ln.Close()
		return nil, nil, err
	}
	credential := hex.EncodeToString(token)
	rec := daemon.NewRuntimeRecord(serviceName, version, daemon.Endpoint{Network: daemon.NetworkTCP, Address: ln.Addr().String()})
	// Kit atomically publishes the record inside a current-user-only directory.
	// The credential is never sent until the peer proves possession of it.
	rec.Metadata = map[string]string{"token": credential}
	proof, err := daemon.NewProof([]byte(credential))
	if err != nil {
		_ = ln.Close()
		return nil, nil, err
	}
	ping, err := proof.NewPingHandler(rec)
	if err != nil {
		_ = ln.Close()
		return nil, nil, err
	}
	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("Fotobank operator API", version))
	registerCheckouts(api, owner, checkouts)
	registerBackups(api, backups)
	huma.Register(api, huma.Operation{
		OperationID: "commit-checkout", Method: http.MethodPost,
		Path: "/checkouts/{id}/commit", Summary: "Commit settled tracked edits",
		MaxBodyBytes: 4096,
	}, func(ctx context.Context, input *commitInput) (*struct{ Body CommitResult }, error) {
		if input.Body.Hub != owner.Hub || input.Body.UserID != owner.UserID {
			return nil, huma.Error403Forbidden("configured owner does not match the running server")
		}
		result, err := checkouts.Commit(ctx, owner, input.CheckoutID)
		out := CommitResult{CheckoutID: input.CheckoutID, Pending: result.Pending, Committed: result.Committed, Conflicts: result.Conflicts}
		if err != nil {
			out.Error = err.Error()
		}
		return &struct{ Body CommitResult }{out}, nil
	})
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
			mux.ServeHTTP(w, r)
		}),
	}
	runtimePath, err := store.Write(rec)
	if err != nil {
		cancel()
		_ = ln.Close()
		return nil, nil, err
	}
	failures := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			failures <- fmt.Errorf("operator listener: %w", err)
		}
	}()
	return sync.OnceFunc(func() {
		gate.Lock()
		closing = true
		gate.Unlock()
		_ = os.Remove(runtimePath)
		cancel()
		_ = srv.Close()
		<-done
		handlers.Wait()
	}), failures, nil
}
