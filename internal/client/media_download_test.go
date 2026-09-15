package client

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/fotobank/internal/httpapi"
)

func TestVerifyDownload(t *testing.T) {
	const helloHash = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	for _, tc := range []struct {
		name, body string
		length     int64
		status     int
		wantError  bool
	}{
		{"verified", "hello", 5, 200, false},
		{"unknown length", "hello", -1, 200, false},
		{"changed bytes", "HELLO", 5, 200, true},
		{"short stream", "hell", -1, 200, true},
		{"long stream", "hello!", -1, 200, true},
		{"wrong length header", "hello", 4, 200, true},
		{"partial response", "hello", 5, 206, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := require.New(t)
			response := &http.Response{StatusCode: tc.status, ContentLength: tc.length, Body: io.NopCloser(strings.NewReader(tc.body))}
			var output bytes.Buffer
			err := verifyDownload(&output, response, httpapi.FileDTO{Size: 5, SHA256: helloHash})
			if tc.wantError {
				r.Error(err)
			} else {
				r.NoError(err)
				r.Equal("hello", output.String())
			}
		})
	}
	pr, pw := io.Pipe()
	require.NoError(t, pr.Close())
	defer pw.Close()
	response := &http.Response{StatusCode: 200, ContentLength: 5, Body: io.NopCloser(strings.NewReader("hello"))}
	require.ErrorIs(t, verifyDownload(pw, response, httpapi.FileDTO{Size: 5, SHA256: helloHash}), io.ErrClosedPipe)
}

func TestVerifyDownloadCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "hel")
		_ = http.NewResponseController(w).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	require.NoError(t, err)
	response, err := server.Client().Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	// A disconnect during a streamed download must reach the copy loop.
	cancel()
	require.ErrorIs(t, verifyDownload(io.Discard, response, httpapi.FileDTO{Size: 5,
		SHA256: "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"}), context.Canceled)
}
