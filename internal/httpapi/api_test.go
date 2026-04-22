package httpapi_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wesm/fotobank/internal/httpapi"
)

func TestHealthzReturnsOK(t *testing.T) {
	r := require.New(t)
	h, err := httpapi.New(httpapi.Deps{})
	r.NoError(err)

	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/healthz")
	r.NoError(err)
	defer resp.Body.Close()
	r.Equal(http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	r.NoError(err)
	var out map[string]any
	r.NoError(json.Unmarshal(body, &out))
	r.Equal("ok", out["status"])
}
