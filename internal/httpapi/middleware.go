package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/wesm/fotobank/internal/errs"
	"github.com/wesm/fotobank/internal/identity"
)

type ctxKey int

const (
	ctxKeyIdentity ctxKey = iota
	ctxKeyRequestID
)

// WithMiddleware returns a net/http middleware that resolves the caller
// Identity via the given Provider, attaches Identity and a request ID to
// the request context, logs each request, and maps identity errors to
// HTTP status codes (401 for ErrIdentityMissing, 403 for
// ErrDirectAccessBlocked, 500 otherwise).
func WithMiddleware(idp identity.Provider) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			id, err := idp.FromRequest(r.Context(), r)
			if err != nil {
				status := http.StatusInternalServerError
				switch {
				case errors.Is(err, errs.ErrIdentityMissing):
					status = http.StatusUnauthorized
				case errors.Is(err, errs.ErrDirectAccessBlocked):
					status = http.StatusForbidden
				}
				http.Error(w, err.Error(), status)
				slog.Warn("request rejected",
					"method", r.Method, "path", r.URL.Path,
					"status", status, "err", err, "dur", time.Since(start))
				return
			}
			reqID := id.RequestID
			if reqID == "" {
				reqID = uuid.NewString()
			}
			ctx := context.WithValue(r.Context(), ctxKeyIdentity, id)
			ctx = context.WithValue(ctx, ctxKeyRequestID, reqID)

			rw := &statusCapture{ResponseWriter: w, code: http.StatusOK}
			next.ServeHTTP(rw, r.WithContext(ctx))

			slog.Info("request",
				"method", r.Method, "path", r.URL.Path,
				"status", rw.code, "principal", id.Principal.OwnersPrincipal().String(),
				"req", reqID, "dur", time.Since(start))
		})
	}
}

// IdentityFromContext returns the Identity attached to ctx by
// WithMiddleware. The second return value reports whether an Identity
// was present.
func IdentityFromContext(ctx context.Context) (identity.Identity, bool) {
	id, ok := ctx.Value(ctxKeyIdentity).(identity.Identity)
	return id, ok
}

// RequestIDFromContext returns the request ID attached to ctx by
// WithMiddleware, or the empty string if none was set.
func RequestIDFromContext(ctx context.Context) string {
	s, _ := ctx.Value(ctxKeyRequestID).(string)
	return s
}

type statusCapture struct {
	http.ResponseWriter
	code int
}

func (c *statusCapture) WriteHeader(code int) {
	c.code = code
	c.ResponseWriter.WriteHeader(code)
}
