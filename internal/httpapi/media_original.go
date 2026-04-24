package httpapi

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/wesm/fotobank/internal/errs"
	"github.com/wesm/fotobank/internal/service"
)

// registerMediaOriginal wires GET /api/v1/media/{id}/original onto mux.
// The handler enforces owner visibility via svc, honours single-range
// Range requests, ETag / If-None-Match, and sets long-lived cache
// headers since content is content-addressed (checksum is the ETag).
// Callers that don't need media HTTP access (for example the OpenAPI
// spec dumper) pass a Deps without a MediaService; this function then
// returns without registering anything.
func registerMediaOriginal(mux *http.ServeMux, svc *service.MediaService) {
	if svc == nil {
		return
	}
	mux.Handle("GET /api/v1/media/{id}/original", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		ident, ok := IdentityFromContext(r.Context())
		if !ok {
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
			return
		}
		caller := ident.Principal.OwnersPrincipal()
		m, err := svc.Get(r.Context(), id, caller)
		if err != nil {
			if errors.Is(err, errs.ErrNotFound) {
				http.Error(w, "media not found", http.StatusNotFound)
				return
			}
			slog.Error("media original get", "err", err, "id", id)
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}

		etag := `"` + m.Checksum + `"`
		h := w.Header()
		h.Set("ETag", etag)
		h.Set("Last-Modified", m.ImportedAt.UTC().Format(http.TimeFormat))
		h.Set("Cache-Control", "private, max-age=31536000, immutable")
		h.Set("Content-Type", m.MimeType)

		if ifNoneMatch := r.Header.Get("If-None-Match"); ifNoneMatch != "" && etagMatches(ifNoneMatch, etag) {
			w.WriteHeader(http.StatusNotModified)
			return
		}

		writeOriginalResponse(w, r, m, func(off, length int64) (io.ReadCloser, error) {
			rc, _, err := svc.OpenOriginal(r.Context(), id, caller, off, length)
			return rc, err
		})
	}))
}

// etagMatches reports whether an If-None-Match header value matches the
// given strong ETag. Handles "*" wildcard and comma-separated lists.
func etagMatches(header, etag string) bool {
	header = strings.TrimSpace(header)
	if header == "*" {
		return true
	}
	for part := range strings.SplitSeq(header, ",") {
		if strings.TrimSpace(part) == etag {
			return true
		}
	}
	return false
}
