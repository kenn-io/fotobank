package client_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/fotobank/internal/client"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/kit/daemon"
)

func TestCommitDoesNotSendCredentialToUnprovenEndpoint(t *testing.T) {
	r := require.New(t)
	var credentialSeen, commitSeen atomic.Bool
	ping := daemon.NewPingHandler(daemon.PingHandlerOptions{Service: "fotobank-operator", Version: "test"})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Authorization") != "" {
			credentialSeen.Store(true)
		}
		if req.Method == http.MethodPost {
			commitSeen.Store(true)
		}
		ping.ServeHTTP(w, req)
	}))
	defer server.Close()
	configPath := filepath.Join(t.TempDir(), "catalog.sqlite")
	store := daemon.RuntimeStore{Dir: configPath + ".operator"}
	record := daemon.NewRuntimeRecord("fotobank-operator", "test", daemon.Endpoint{
		Network: daemon.NetworkTCP, Address: strings.TrimPrefix(server.URL, "http://"),
	})
	record.Metadata = map[string]string{"token": "synthetic-operator-credential"}
	_, err := store.Write(record)
	r.NoError(err)
	_, err = client.Commit(context.Background(), configPath, "test", "checkout", owners.Principal{Hub: "h", UserID: "u"})
	r.ErrorContains(err, "unreachable")
	r.False(credentialSeen.Load())
	r.False(commitSeen.Load())
}
