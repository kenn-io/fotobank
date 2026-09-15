package httpapi

import (
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"go.kenn.io/fotobank/internal/auth/hidden"
	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/media"
	"go.kenn.io/fotobank/internal/service"
)

// registerMediaFile serves any file attached to an owned asset. The asset ID
// remains the authorization boundary; a file ID from another asset is hidden
// behind the same not-found response.
func registerMediaFile(mux *http.ServeMux, api huma.API, svc *service.MediaService) {
	const path = "/api/v1/media/{id}/files/{fileID}/content"
	registerDownloadSchema(api, path, "download-media-file", "id", "fileID")
	if svc == nil {
		return
	}
	mux.Handle("GET "+path, WrapMuxHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ident, ok := IdentityFromContext(r.Context())
		if !ok {
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
			return
		}
		assetID, fileID := r.PathValue("id"), r.PathValue("fileID")
		caller := ident.Principal.OwnersPrincipal()
		includeHidden := false
		if claim, hasClaim := hidden.UnlockClaimFromContext(r.Context()); hasClaim && claim.Principal == caller {
			includeHidden = true
		}
		file, item, err := svc.GetFile(r.Context(), assetID, fileID, caller, includeHidden)
		if err != nil {
			if errors.Is(err, errs.ErrNotFound) {
				http.Error(w, "media file not found", http.StatusNotFound)
				return
			}
			slog.Error("media file get", "err", err, "asset_id", assetID, "file_id", fileID)
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}

		h := w.Header()
		h.Set("Content-Type", file.MimeType)
		h.Set("Accept-Ranges", "bytes")
		if item.HiddenAt != nil {
			h.Set("Cache-Control", "no-store")
		} else {
			etag := `"` + file.SHA256 + `"`
			h.Set("ETag", etag)
			h.Set("Last-Modified", item.ImportedAt.UTC().Format(http.TimeFormat))
			h.Set("Cache-Control", "private, no-cache")
			if ifNoneMatch := r.Header.Get("If-None-Match"); ifNoneMatch != "" && etagMatches(ifNoneMatch, etag) {
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}

		writeOriginalResponse(w, r, media.Media{ID: file.ID, Size: file.Size}, func(off, length int64) (io.ReadCloser, error) {
			rc, _, _, err := svc.OpenFile(r.Context(), assetID, fileID, caller, off, length, includeHidden)
			return rc, err
		})
	})))
}
