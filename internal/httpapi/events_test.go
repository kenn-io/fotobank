package httpapi_test

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/httpapi"
	"github.com/wesm/fotobank/internal/identity"
	"github.com/wesm/fotobank/internal/owners"
)

func TestEventsHelloOnConnect(t *testing.T) {
	r := require.New(t)
	prov := identity.NewStub(owners.Principal{Hub: "local", UserID: "alice"}, "Alice")
	h, err := httpapi.New(httpapi.Deps{IdentityProvider: prov, EventBus: httpapi.NewEventBus()})
	r.NoError(err)
	srv := httptest.NewServer(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/events", nil)
	resp, err := http.DefaultClient.Do(req)
	r.NoError(err)
	defer resp.Body.Close()
	r.Equal(http.StatusOK, resp.StatusCode)
	r.Equal("text/event-stream", resp.Header.Get("Content-Type"))

	br := bufio.NewReader(resp.Body)
	var sawHello, sawData bool
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) && (!sawHello || !sawData) {
		line, err := br.ReadString('\n')
		if err != nil {
			break
		}
		line = strings.TrimRight(line, "\r\n")
		if strings.HasPrefix(line, "event: hello") {
			sawHello = true
		}
		if strings.HasPrefix(line, "data: ") && sawHello {
			sawData = true
		}
	}
	r.True(sawHello, "expected hello event")
	r.True(sawData, "expected data line after hello")
}
