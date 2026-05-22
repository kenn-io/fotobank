package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/media"
)

func TestWriteOriginalResponseFullBody(t *testing.T) {
	r := require.New(t)
	body := "hello world"
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	m := media.Media{Size: int64(len(body)), MimeType: "text/plain",
		ImportedAt: time.Now().UTC()}
	writeOriginalResponse(w, req, m, func(off, length int64) (io.ReadCloser, error) {
		r.Equal(int64(0), off)
		r.Equal(int64(-1), length)
		return io.NopCloser(strings.NewReader(body)), nil
	})
	res := w.Result()
	defer res.Body.Close()
	r.Equal(http.StatusOK, res.StatusCode)
	got, err := io.ReadAll(res.Body)
	r.NoError(err)
	r.Equal(body, string(got))
}

func TestWriteOriginalResponseRange(t *testing.T) {
	r := require.New(t)
	body := "0123456789"
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Range", "bytes=2-5")
	w := httptest.NewRecorder()
	m := media.Media{Size: int64(len(body)), MimeType: "text/plain",
		ImportedAt: time.Now().UTC()}
	writeOriginalResponse(w, req, m, func(off, length int64) (io.ReadCloser, error) {
		r.Equal(int64(2), off)
		r.Equal(int64(4), length)
		return io.NopCloser(strings.NewReader(body[off : off+length])), nil
	})
	res := w.Result()
	defer res.Body.Close()
	r.Equal(http.StatusPartialContent, res.StatusCode)
	r.Equal("bytes 2-5/10", res.Header.Get("Content-Range"))
	got, err := io.ReadAll(res.Body)
	r.NoError(err)
	r.Equal("2345", string(got))
}

func TestWriteOriginalResponseUnsatisfiableRange(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Range", "bytes=999-")
	w := httptest.NewRecorder()
	m := media.Media{Size: 10, MimeType: "text/plain", ImportedAt: time.Now().UTC()}
	writeOriginalResponse(w, req, m, func(off, length int64) (io.ReadCloser, error) {
		require.Fail(t, "open must not be called on unsatisfiable range")
		return nil, nil
	})
	require.Equal(t, http.StatusRequestedRangeNotSatisfiable, w.Result().StatusCode)
}

// Malformed Range syntax must also map to 416, not 400. The owner
// path's existing handler rejects any invalid single-range request
// uniformly with 416; E2 preserves that contract on the extracted
// helper so owner/shared routes stay byte-identical.
func TestWriteOriginalResponseMalformedRange(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Range", "not-a-range")
	w := httptest.NewRecorder()
	m := media.Media{Size: 10, MimeType: "text/plain", ImportedAt: time.Now().UTC()}
	writeOriginalResponse(w, req, m, func(off, length int64) (io.ReadCloser, error) {
		require.Fail(t, "open must not be called on malformed range")
		return nil, nil
	})
	res := w.Result()
	require.Equal(t, http.StatusRequestedRangeNotSatisfiable, res.StatusCode)
	require.Equal(t, "bytes */10", res.Header.Get("Content-Range"))
}
