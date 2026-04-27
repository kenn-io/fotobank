package httpapi_test

import (
	"bufio"
	"context"
	"encoding/json"
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

// readSSEFrame reads SSE lines until a blank line (frame terminator)
// or br errors out, returning the collected lines for assertions.
func readSSEFrame(br *bufio.Reader, deadline time.Time) []string {
	var lines []string
	for time.Now().Before(deadline) {
		line, err := br.ReadString('\n')
		if err != nil {
			return lines
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			return lines
		}
		lines = append(lines, line)
	}
	return lines
}

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
	frame := readSSEFrame(br, time.Now().Add(1*time.Second))
	r.NotEmpty(frame, "expected an SSE frame for hello")

	var sawHelloEvent, sawData, sawID bool
	for _, line := range frame {
		switch {
		case line == "event: hello":
			sawHelloEvent = true
		case strings.HasPrefix(line, "data: "):
			sawData = true
		case strings.HasPrefix(line, "id:"):
			sawID = true
		}
	}
	r.True(sawHelloEvent, "expected hello event line in frame=%v", frame)
	r.True(sawData, "expected data line in hello frame=%v", frame)
	r.False(sawID,
		"hello is a control event and MUST NOT carry an id field (would advance Last-Event-ID): frame=%v",
		frame)
}

// TestEventsCatchupRequiredOnGap exercises Finding B: when the
// client's Last-Event-ID predates the retained ring window, the
// handler must emit catchup-required instead of silently skipping the
// evicted range. We use a 4-event ring, publish 6, then reconnect
// with Last-Event-ID=1 (evicted). The third frame after hello must be
// catchup-required, and the catchup-required frame itself must NOT
// carry an id field.
func TestEventsCatchupRequiredOnGap(t *testing.T) {
	r := require.New(t)
	prov := identity.NewStub(owners.Principal{Hub: "local", UserID: "alice"}, "Alice")
	bus := httpapi.NewEventBusWithSize(4)
	p := owners.Principal{Hub: "local", UserID: "alice"}
	for i := int64(1); i <= 6; i++ {
		bus.Publish(p, httpapi.Event{ID: i, Type: "test", Data: json.RawMessage(`{}`)})
	}

	h, err := httpapi.New(httpapi.Deps{IdentityProvider: prov, EventBus: bus})
	r.NoError(err)
	srv := httptest.NewServer(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/events", nil)
	req.Header.Set("Last-Event-ID", "1")
	resp, err := http.DefaultClient.Do(req)
	r.NoError(err)
	defer resp.Body.Close()
	r.Equal(http.StatusOK, resp.StatusCode)

	br := bufio.NewReader(resp.Body)
	deadline := time.Now().Add(1 * time.Second)

	// Frame 1: hello
	hello := readSSEFrame(br, deadline)
	r.NotEmpty(hello, "expected hello frame")
	r.Contains(hello, "event: hello")

	// Frame 2: catchup-required (NOT replay)
	catchup := readSSEFrame(br, deadline)
	r.NotEmpty(catchup, "expected catchup-required frame")
	r.Contains(catchup, "event: catchup-required")
	for _, line := range catchup {
		r.Falsef(strings.HasPrefix(line, "id:"),
			"catchup-required is a control event and MUST NOT carry an id field: frame=%v",
			catchup)
	}
}
