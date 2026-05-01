package httpapi

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/wesm/fotobank/internal/auth/hidden"
	"github.com/wesm/fotobank/internal/errs"
	"github.com/wesm/fotobank/internal/service"
	"github.com/wesm/fotobank/internal/thumb"
)

// registerMediaThumb wires GET /api/v1/media/{id}/thumb onto mux. The
// handler enforces owner visibility via svc, defaults ?size= to "grid",
// requires a non-negative integer ?v= that matches the row's
// thumb_version, and streams the bytes with a strong ETag and
// `private, no-cache` so each reuse goes through ETag revalidation.
// Versioned ?v= URLs still bust the cache instantly on regenerate.
// Callers that don't need the thumb route (OpenAPI dumper, tests)
// pass a Deps without a ThumbService; this function then returns
// without registering anything.
//
// 404 responses are explicitly marked Cache-Control: no-store. A thumb
// can transition from pending → ready and later have its version
// bumped by a regenerate; caching a 404 would let clients miss that
// transition until their cache entry expired.
func registerMediaThumb(mux *http.ServeMux, svc *service.ThumbService) {
	if svc == nil {
		return
	}
	mux.Handle("GET /api/v1/media/{id}/thumb", WrapMuxHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		ident, ok := IdentityFromContext(r.Context())
		if !ok {
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
			return
		}

		// 404 helper sets no-store before erroring so clients don't cache
		// a pending thumb's 404 and miss the version bump. http.Error
		// writes headers immediately, so the Cache-Control must be set
		// first.
		notFound := func() {
			w.Header().Set("Cache-Control", "no-store")
			http.Error(w, "thumb not found", http.StatusNotFound)
		}

		sizeStr := r.URL.Query().Get("size")
		if sizeStr == "" {
			sizeStr = "grid"
		}
		size, err := thumb.ParseSize(sizeStr)
		if err != nil {
			http.Error(w, "unknown size", http.StatusBadRequest)
			return
		}

		vStr := r.URL.Query().Get("v")
		if vStr == "" {
			notFound()
			return
		}
		version, err := strconv.Atoi(vStr)
		if err != nil || version < 0 {
			notFound()
			return
		}

		caller := ident.Principal.OwnersPrincipal()
		// Honor the unlock claim for direct-by-id reads (anti-enumeration).
		includeHidden := false
		if claim, hasClaim := hidden.UnlockClaimFromContext(r.Context()); hasClaim && claim.Principal == caller {
			includeHidden = true
		}
		rc, m, err := svc.Get(r.Context(), id, size, version, caller, includeHidden)
		if err != nil {
			if errors.Is(err, errs.ErrNotFound) {
				notFound()
				return
			}
			slog.Error("thumb get", "err", err, "id", id)
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}

		h := w.Header()
		h.Set("Content-Type", "image/jpeg")

		// Hidden bytes must never be stored in any cache. Once a
		// session expires the browser must re-validate via the server
		// (which will 401/404) rather than serving stale hidden bytes
		// from its own disk cache.
		if m.HiddenAt != nil {
			h.Set("Cache-Control", "no-store")
			writeThumbResponse(w, r, func() (io.ReadCloser, error) {
				return rc, nil
			})
			return
		}

		etag := `"` + m.ID + "-" + string(size) + "-v" + strconv.Itoa(m.ThumbVersion) + `"`
		h.Set("ETag", etag)
		if m.ThumbUpdatedAt != nil {
			h.Set("Last-Modified", m.ThumbUpdatedAt.UTC().Format(http.TimeFormat))
		}
		// no-cache forces a conditional ETag revalidation on every reuse.
		// The ?v= URL still cache-busts after thumb regeneration; this
		// header is what stops a hidden thumbnail from being served from
		// the private cache after visibility changes.
		h.Set("Cache-Control", "private, no-cache")

		if ifNoneMatch := r.Header.Get("If-None-Match"); ifNoneMatch != "" && etagMatches(ifNoneMatch, etag) {
			_ = rc.Close()
			w.WriteHeader(http.StatusNotModified)
			return
		}

		writeThumbResponse(w, r, func() (io.ReadCloser, error) {
			return rc, nil
		})
	})))
}
