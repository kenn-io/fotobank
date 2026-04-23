package httpapi

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/wesm/fotobank/internal/errs"
	"github.com/wesm/fotobank/internal/service"
	"github.com/wesm/fotobank/internal/thumb"
)

// registerMediaThumb wires GET /api/v1/media/{id}/thumb onto mux. The
// handler enforces owner visibility via svc, defaults ?size= to "grid",
// requires a non-negative integer ?v= that matches the row's
// thumb_version, and streams the bytes with a strong ETag +
// immutable cache directive (URL identity via ?v= is what makes
// "immutable" safe — a regenerate bumps the version and therefore the
// URL). Callers that don't need the thumb route (OpenAPI dumper, tests)
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
	mux.Handle("GET /api/v1/media/{id}/thumb", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		rc, m, err := svc.Get(r.Context(), id, size, version, caller)
		if err != nil {
			if errors.Is(err, errs.ErrNotFound) {
				notFound()
				return
			}
			slog.Error("thumb get", "err", err, "id", id)
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		defer func() { _ = rc.Close() }()

		etag := `"` + m.ID + "-" + string(size) + "-v" + strconv.Itoa(m.ThumbVersion) + `"`
		h := w.Header()
		h.Set("ETag", etag)
		if m.ThumbUpdatedAt != nil {
			h.Set("Last-Modified", m.ThumbUpdatedAt.UTC().Format(http.TimeFormat))
		}
		h.Set("Cache-Control", "private, max-age=31536000, immutable")
		h.Set("Content-Type", "image/jpeg")

		if ifNoneMatch := r.Header.Get("If-None-Match"); ifNoneMatch != "" && etagMatches(ifNoneMatch, etag) {
			w.WriteHeader(http.StatusNotModified)
			return
		}

		if _, err := io.Copy(w, rc); err != nil {
			slog.Error("thumb stream", "err", err, "id", id)
		}
	}))
}
