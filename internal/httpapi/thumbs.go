package httpapi

import (
	"io"
	"log/slog"
	"net/http"
)

// writeThumbResponse streams cached thumb bytes. Caller has already
// performed auth and set Cache-Control / Vary / Content-Type / ETag /
// Last-Modified. open returns the full-file reader; thumbs are small
// enough that Range is not honoured.
func writeThumbResponse(
	w http.ResponseWriter,
	_ *http.Request,
	open func() (io.ReadCloser, error),
) {
	rc, err := open()
	if err != nil {
		slog.Error("open thumb bytes", "err", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError),
			http.StatusInternalServerError)
		return
	}
	defer func() { _ = rc.Close() }()
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, rc); err != nil {
		slog.Error("thumb stream", "err", err)
	}
}
