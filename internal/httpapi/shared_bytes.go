package httpapi

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/wesm/fotobank/internal/errs"
	"github.com/wesm/fotobank/internal/media"
	"github.com/wesm/fotobank/internal/service"
	"github.com/wesm/fotobank/internal/thumb"
)

// registerSharedBytes wires the grantee-side byte-streaming handlers
// onto mux. svc == nil registers nothing (consistent with the owner
// byte routes' pattern) so the OpenAPI dumper can still build a spec.
//
// The routes use raw http.HandlerFunc rather than huma because huma
// isn't used for streaming bodies.
func registerSharedBytes(mux *http.ServeMux, svc *service.SharedReadService) {
	if svc == nil {
		return
	}
	mux.Handle("GET /api/v1/shared/media/{id}/thumb", sharedThumbHandler(svc))
	mux.Handle("GET /api/v1/shared/media/{id}/original", sharedOriginalHandler(svc))
}

// sharedThumbHandler serves cached thumb bytes for a grantee-accessible
// media row. Query args ?size=grid|preview|lightbox (default grid) and
// ?v=N (thumb_version). Grantee authorisation is gated by
// CheckMediaAccess — download is *not* required, mirroring the owner
// thumb contract (thumbs are previews).
//
// Response caching: shared byte routes emit Cache-Control: no-store so a
// response cannot be served to another grantee from an intermediate
// cache whose key doesn't include the X-Auth-Scopes header or the
// grantee identity. ETag is still emitted for within-session
// revalidation via If-None-Match.
func sharedThumbHandler(svc *service.SharedReadService) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		// Set Cache-Control: no-store up-front so every path (401, 400,
		// 404, 500, 200) shares the same cache directive. See
		// sharedOriginalHandler for the same-rationale write.
		w.Header().Set("Cache-Control", "no-store")

		ident, ok := IdentityFromContext(r.Context())
		if !ok {
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
			return
		}

		notFound := func() {
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
		rc, m, err := svc.OpenThumb(r.Context(), caller, ident.Scopes, id, size, version)
		if err != nil {
			if errors.Is(err, errs.ErrNotFound) {
				notFound()
				return
			}
			slog.Error("shared thumb", "err", err, "id", id)
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}

		etag := `"` + m.ID + "-" + string(size) + "-v" + strconv.Itoa(m.ThumbVersion) + `"`
		h := w.Header()
		h.Set("ETag", etag)
		if m.ThumbUpdatedAt != nil {
			h.Set("Last-Modified", m.ThumbUpdatedAt.UTC().Format(http.TimeFormat))
		}
		h.Set("Content-Type", "image/jpeg")

		if ifNoneMatch := r.Header.Get("If-None-Match"); ifNoneMatch != "" && etagMatches(ifNoneMatch, etag) {
			_ = rc.Close()
			w.WriteHeader(http.StatusNotModified)
			return
		}
		writeThumbResponse(w, r, func() (io.ReadCloser, error) { return rc, nil })
	})
}

// sharedOriginalHandler serves full-resolution bytes to a grantee whose
// scope carries allow_download=true. GetMedia gates access and yields
// CanDownload; the closure passed to writeOriginalResponse calls
// OpenOriginal to obtain the actual reader (OpenOriginal re-runs the
// access+download gate, which is cheap and keeps the service API
// single-entry). A download-denied row yields 403; unauthorised access
// is indistinguishable from not-found (404) to avoid probing.
func sharedOriginalHandler(svc *service.SharedReadService) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		// Set Cache-Control: no-store up-front so every path (401, 404,
		// 403, 500, 200/206) shares the same cache directive. Grantee
		// auth depends on X-Auth-Scopes / identity which intermediate
		// caches don't key on; caching ANY response for a shared URL
		// would risk serving it to another grantee.
		w.Header().Set("Cache-Control", "no-store")

		ident, ok := IdentityFromContext(r.Context())
		if !ok {
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
			return
		}
		caller := ident.Principal.OwnersPrincipal()

		m, err := svc.GetMedia(r.Context(), caller, ident.Scopes, id)
		if err != nil {
			if errors.Is(err, errs.ErrNotFound) {
				http.Error(w, "media not found", http.StatusNotFound)
				return
			}
			slog.Error("shared original access", "err", err, "id", id)
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		if !m.CanDownload {
			http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
			return
		}

		etag := `"` + m.ID + `"`
		h := w.Header()
		h.Set("ETag", etag)
		h.Set("Last-Modified", m.DisplayTime.UTC().Format(http.TimeFormat))
		h.Set("Accept-Ranges", "bytes")
		h.Set("Content-Type", m.MimeType)

		if ifNoneMatch := r.Header.Get("If-None-Match"); ifNoneMatch != "" && etagMatches(ifNoneMatch, etag) {
			w.WriteHeader(http.StatusNotModified)
			return
		}

		writeOriginalResponse(w, r,
			media.Media{
				ID: m.ID, Owner: m.Owner, MimeType: m.MimeType,
				Size: m.Size, ImportedAt: m.DisplayTime,
			},
			func(off, length int64) (io.ReadCloser, error) {
				rc, _, err := svc.OpenOriginal(r.Context(), caller, ident.Scopes, id, off, length)
				return rc, err
			},
		)
	})
}
