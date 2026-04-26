package httpapi

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRecovery_PanicConvertedTo500(t *testing.T) {
	r := require.New(t)
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, nil))

	h := WithRecovery(logger)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))
	r.Equal(500, rec.Code)
	r.Contains(logBuf.String(), `"level":"ERROR"`)
	r.Contains(logBuf.String(), "boom")
}

func TestRecovery_NoPanicPassesThrough(t *testing.T) {
	r := require.New(t)
	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	h := WithRecovery(logger)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(204)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))
	r.Equal(204, rec.Code)
}
