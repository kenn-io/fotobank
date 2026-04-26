package obs

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAdminMux_MetricsRoute(t *testing.T) {
	r := require.New(t)
	m := NewTestMetrics()
	m.HTTPRequests("GET", "/x", "2xx").Inc()

	mux := NewAdminMux(AdminConfig{
		Metrics:      m,
		Ready:        NewReady(),
		Checks:       nil,
		ReadyzCfg:    ReadyzConfig{DeadlineTotal: time.Second, CacheTTL: 0},
		PprofEnabled: false,
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/metrics")
	r.NoError(err)
	defer resp.Body.Close()
	r.Equal(200, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	r.Contains(string(body), "fotobank_http_requests_total")
}

func TestAdminMux_ReadyzRoute(t *testing.T) {
	r := require.New(t)
	mux := NewAdminMux(AdminConfig{
		Metrics:   NewTestMetrics(),
		Ready:     NewReady(),
		Checks:    nil,
		ReadyzCfg: ReadyzConfig{DeadlineTotal: time.Second, CacheTTL: 0},
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/readyz")
	r.NoError(err)
	defer resp.Body.Close()
	r.Equal(200, resp.StatusCode)
}

func TestAdminMux_PprofGated(t *testing.T) {
	r := require.New(t)
	muxOff := NewAdminMux(AdminConfig{
		Metrics:      NewTestMetrics(),
		Ready:        NewReady(),
		ReadyzCfg:    ReadyzConfig{DeadlineTotal: time.Second, CacheTTL: 0},
		PprofEnabled: false,
	})
	srvOff := httptest.NewServer(muxOff)
	defer srvOff.Close()
	resp, err := http.Get(srvOff.URL + "/debug/pprof/")
	r.NoError(err)
	defer resp.Body.Close()
	r.Equal(404, resp.StatusCode)

	muxOn := NewAdminMux(AdminConfig{
		Metrics:      NewTestMetrics(),
		Ready:        NewReady(),
		ReadyzCfg:    ReadyzConfig{DeadlineTotal: time.Second, CacheTTL: 0},
		PprofEnabled: true,
	})
	srvOn := httptest.NewServer(muxOn)
	defer srvOn.Close()
	resp, err = http.Get(srvOn.URL + "/debug/pprof/")
	r.NoError(err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	r.Equal(200, resp.StatusCode)
	r.True(strings.Contains(string(body), "/debug/pprof/") ||
		strings.Contains(string(body), "Profile Descriptions"),
		"pprof index page expected; got %q", string(body))
}
