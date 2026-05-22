package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWriteThumbResponseFullBody(t *testing.T) {
	r := require.New(t)
	body := "PNGBYTES"
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	w := httptest.NewRecorder()
	writeThumbResponse(w, req, func() (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(body)), nil
	})
	res := w.Result()
	defer res.Body.Close()
	r.Equal(http.StatusOK, res.StatusCode)
	got, _ := io.ReadAll(res.Body)
	r.Equal(body, string(got))
}
