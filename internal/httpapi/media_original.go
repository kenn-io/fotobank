package httpapi

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
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
		h.Set("Accept-Ranges", "bytes")
		h.Set("Content-Type", m.MimeType)

		if ifNoneMatch := r.Header.Get("If-None-Match"); ifNoneMatch != "" && etagMatches(ifNoneMatch, etag) {
			w.WriteHeader(http.StatusNotModified)
			return
		}

		offset := int64(0)
		length := int64(-1)
		if rangeHdr := r.Header.Get("Range"); rangeHdr != "" {
			start, end, rok := parseSingleByteRange(rangeHdr, m.Size)
			if !rok {
				h.Set("Content-Range", fmt.Sprintf("bytes */%d", m.Size))
				http.Error(w, "range not satisfiable", http.StatusRequestedRangeNotSatisfiable)
				return
			}
			offset = start
			length = end - start + 1
			h.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, m.Size))
			h.Set("Content-Length", strconv.FormatInt(length, 10))
			w.WriteHeader(http.StatusPartialContent)
		} else {
			h.Set("Content-Length", strconv.FormatInt(m.Size, 10))
		}

		if _, streamErr := svc.StreamOriginal(r.Context(), id, caller, offset, length, w); streamErr != nil {
			// Headers are already flushed, so we can't change the status.
			// Log server-side; the client sees a truncated body.
			slog.Error("media original stream", "err", streamErr, "id", id)
		}
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

// parseSingleByteRange parses an RFC 7233 byte-range header of the form
// "bytes=START-END", "bytes=START-", or "bytes=-N" (suffix). Returns
// inclusive start and end in [0, size) and ok. ok is false for empty,
// malformed, unsatisfiable, or multi-range values; multi-range is
// explicitly out of scope for Plan B.
func parseSingleByteRange(header string, size int64) (int64, int64, bool) {
	if !strings.HasPrefix(header, "bytes=") {
		return 0, 0, false
	}
	spec := strings.TrimPrefix(header, "bytes=")
	if strings.Contains(spec, ",") {
		return 0, 0, false
	}
	startStr, endStr, found := strings.Cut(spec, "-")
	if !found {
		return 0, 0, false
	}
	startStr = strings.TrimSpace(startStr)
	endStr = strings.TrimSpace(endStr)
	if startStr == "" && endStr == "" {
		return 0, 0, false
	}

	if startStr == "" {
		n, err := strconv.ParseInt(endStr, 10, 64)
		if err != nil || n <= 0 {
			return 0, 0, false
		}
		if n > size {
			n = size
		}
		return size - n, size - 1, true
	}

	start, err := strconv.ParseInt(startStr, 10, 64)
	if err != nil || start < 0 {
		return 0, 0, false
	}
	if start >= size {
		return 0, 0, false
	}
	end := size - 1
	if endStr != "" {
		e, err := strconv.ParseInt(endStr, 10, 64)
		if err != nil || e < start {
			return 0, 0, false
		}
		end = e
	}
	if end >= size {
		end = size - 1
	}
	return start, end, true
}
