package httpapi

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"go.kenn.io/fotobank/internal/media"
)

// writeOriginalResponse streams the original bytes, honouring an HTTP
// Range request when present. Caller has already performed auth and
// set Cache-Control / Vary / Content-Type / ETag / Last-Modified. open
// is called exactly once after Range parsing succeeds; it returns the
// (offset, length)-sliced reader. length == -1 means "to EOF".
//
// Writes Content-Length (both 200 and 206), Content-Range (206 only),
// Accept-Ranges (unconditionally), and the response status.
func writeOriginalResponse(
	w http.ResponseWriter,
	r *http.Request,
	m media.Media,
	open func(offset, length int64) (io.ReadCloser, error),
) {
	size := m.Size
	h := w.Header()
	// Restore: every successful response (200 or 206) must advertise
	// range support. The owner handler also sets this before its 304
	// early return — setting it here keeps shared byte routes from
	// losing the header on their 200/206 path.
	h.Set("Accept-Ranges", "bytes")

	offset, length, partial, err := parseRangeHeader(r.Header.Get("Range"), size)
	if err != nil {
		// Preserve the existing owner contract: any malformed or
		// unsatisfiable single-range request returns 416 with a
		// Content-Range: bytes */SIZE hint. Emitting 400 here would
		// diverge the shared byte route from the owner byte route and
		// break the "byte-identical" invariant the spec pins.
		// Success cache headers set by the caller would otherwise
		// stick to this 416; overwrite them with no-store so clients
		// don't cache the "unsatisfiable" answer.
		h.Set("Cache-Control", "no-store")
		h.Set("Content-Range", fmt.Sprintf("bytes */%d", size))
		http.Error(w, http.StatusText(http.StatusRequestedRangeNotSatisfiable),
			http.StatusRequestedRangeNotSatisfiable)
		return
	}

	rc, err := open(offset, length)
	if err != nil {
		slog.Error("open original bytes", "err", err, "id", m.ID)
		// Same reason as the 416 path above: the caller already
		// committed to a long-lived Cache-Control assuming success;
		// a transient storage/read failure must not inherit that.
		h.Set("Cache-Control", "no-store")
		http.Error(w, http.StatusText(http.StatusInternalServerError),
			http.StatusInternalServerError)
		return
	}
	defer func() { _ = rc.Close() }()

	if partial {
		total := length
		if total < 0 {
			total = size - offset
		}
		h.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", offset, offset+total-1, size))
		h.Set("Content-Length", strconv.FormatInt(total, 10))
		w.WriteHeader(http.StatusPartialContent)
	} else {
		h.Set("Content-Length", strconv.FormatInt(size, 10))
		w.WriteHeader(http.StatusOK)
	}
	if _, err := io.Copy(w, rc); err != nil {
		slog.Error("original stream", "err", err, "id", m.ID)
	}
}

var errRangeUnsatisfiable = errors.New("range unsatisfiable")

// parseRangeHeader handles the subset of RFC 7233 we need:
//
//	bytes=START-END
//	bytes=START-
//	bytes=-SUFFIX
//
// length == -1 means "to EOF" for the open closure. Any non-nil error
// (malformed syntax, unsupported range unit, multi-range, or out-of-
// bounds) is treated uniformly by the caller as unsatisfiable and
// produces 416. errRangeUnsatisfiable is kept as a named sentinel for
// future callers that want to distinguish malformed from unsatisfiable,
// but writeOriginalResponse intentionally does not branch on it.
func parseRangeHeader(raw string, size int64) (offset, length int64, partial bool, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, -1, false, nil
	}
	if size == 0 {
		return 0, 0, false, errRangeUnsatisfiable
	}
	if !strings.HasPrefix(raw, "bytes=") {
		return 0, 0, false, fmt.Errorf("unsupported range unit: %s", raw)
	}
	spec := strings.TrimPrefix(raw, "bytes=")
	if strings.Contains(spec, ",") {
		return 0, 0, false, fmt.Errorf("multi-range not supported")
	}
	startStr, endStr, ok := strings.Cut(spec, "-")
	if !ok {
		return 0, 0, false, fmt.Errorf("malformed range: %s", raw)
	}
	switch {
	case startStr == "" && endStr == "":
		return 0, 0, false, fmt.Errorf("malformed range: %s", raw)
	case startStr == "":
		n, err := strconv.ParseInt(endStr, 10, 64)
		if err != nil || n <= 0 {
			return 0, 0, false, fmt.Errorf("bad suffix: %s", raw)
		}
		if n > size {
			n = size
		}
		return size - n, n, true, nil
	case endStr == "":
		start, err := strconv.ParseInt(startStr, 10, 64)
		if err != nil || start < 0 {
			return 0, 0, false, fmt.Errorf("bad start: %s", raw)
		}
		if start >= size {
			return 0, 0, false, errRangeUnsatisfiable
		}
		return start, size - start, true, nil
	default:
		start, err1 := strconv.ParseInt(startStr, 10, 64)
		end, err2 := strconv.ParseInt(endStr, 10, 64)
		if err1 != nil || err2 != nil || start < 0 || end < start {
			return 0, 0, false, fmt.Errorf("bad range: %s", raw)
		}
		if start >= size {
			return 0, 0, false, errRangeUnsatisfiable
		}
		if end >= size {
			end = size - 1
		}
		return start, end - start + 1, true, nil
	}
}
