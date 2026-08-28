package httpapi

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"go.kenn.io/fotobank/internal/auth/hidden"
	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/service"
)

// registerMediaOriginal wires GET /api/v1/media/{id}/original onto mux.
// The handler enforces owner visibility via svc, honours single-range
// Range requests, ETag / If-None-Match, and sets long-lived cache
// headers since content is content-addressed (SHA-256 is the ETag).
// Callers that don't need media HTTP access (for example the OpenAPI
// spec dumper) pass a Deps without a MediaService; this function then
// returns without registering anything.
func registerMediaOriginal(mux *http.ServeMux, svc *service.MediaService) {
	if svc == nil {
		return
	}
	mux.Handle("GET /api/v1/media/{id}/original", WrapMuxHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		ident, ok := IdentityFromContext(r.Context())
		if !ok {
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
			return
		}
		caller := ident.Principal.OwnersPrincipal()
		// Honor the unlock claim for direct-by-id reads. Pass includeHidden
		// through both the initial Get (header phase) and the later
		// OpenOriginal call (streaming phase) so an unlocked hidden original
		// passes both phases consistently.
		includeHidden := false
		if claim, hasClaim := hidden.UnlockClaimFromContext(r.Context()); hasClaim && claim.Principal == caller {
			includeHidden = true
		}
		m, err := svc.Get(r.Context(), id, caller, includeHidden)
		if err != nil {
			if errors.Is(err, errs.ErrNotFound) {
				http.Error(w, "media not found", http.StatusNotFound)
				return
			}
			slog.Error("media original get", "err", err, "id", id)
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}

		h := w.Header()
		h.Set("Content-Type", m.MimeType)

		// Hidden bytes must never be stored in any cache. Once a
		// session expires the browser must re-validate via the server
		// (which will 401/404) rather than serving stale hidden bytes
		// from its own disk cache.
		if m.HiddenAt != nil {
			h.Set("Cache-Control", "no-store")
			writeOriginalResponse(w, r, m, func(off, length int64) (io.ReadCloser, error) {
				rc, _, err := svc.OpenOriginal(r.Context(), id, caller, off, length, includeHidden)
				return rc, err
			})
			return
		}

		etag := `"` + m.SHA256 + `"`
		h.Set("ETag", etag)
		h.Set("Last-Modified", m.ImportedAt.UTC().Format(http.TimeFormat))
		// no-cache forces a conditional ETag revalidation on every reuse.
		// max-age + must-revalidate would still let the browser serve a
		// fresh entry without contacting us, so a photo hidden after view
		// could come back from the private cache.
		h.Set("Cache-Control", "private, no-cache")
		// Set Accept-Ranges before the If-None-Match early return so
		// 304 responses continue to advertise range support, matching
		// the pre-helper owner contract.
		h.Set("Accept-Ranges", "bytes")

		if ifNoneMatch := r.Header.Get("If-None-Match"); ifNoneMatch != "" && etagMatches(ifNoneMatch, etag) {
			w.WriteHeader(http.StatusNotModified)
			return
		}

		writeOriginalResponse(w, r, m, func(off, length int64) (io.ReadCloser, error) {
			rc, _, err := svc.OpenOriginal(r.Context(), id, caller, off, length, includeHidden)
			return rc, err
		})
	})))
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
