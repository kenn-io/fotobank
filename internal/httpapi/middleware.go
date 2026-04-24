package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/wesm/fotobank/internal/errs"
	"github.com/wesm/fotobank/internal/identity"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/share"
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
				// Never echo err.Error() to the wire. A sentinel may be
				// wrapped by an internal detail (e.g. fmt.Errorf("db
				// path /srv/sensitive: %w", ErrIdentityMissing)) that
				// errors.Is still matches. Log the full err
				// server-side; return only the status text to clients.
				http.Error(w, http.StatusText(status), status)
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

// ContextWithIdentity returns a derived context carrying id. Tests and
// internal callers that construct handlers without running them through
// WithMiddleware can use this to stand in for the context key.
func ContextWithIdentity(ctx context.Context, id identity.Identity) context.Context {
	return context.WithValue(ctx, ctxKeyIdentity, id)
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

// displayCacheLRU tracks recent (hub, user_id) upserts so a burst of
// requests does not hammer principal_display. Process-local; not shared
// across server instances.
type displayCacheLRU struct {
	mu   sync.Mutex
	seen map[owners.Principal]time.Time
	cap  int
	ttl  time.Duration
}

func newDisplayCacheLRU(cap int, ttl time.Duration) *displayCacheLRU {
	return &displayCacheLRU{
		seen: make(map[owners.Principal]time.Time, cap),
		cap:  cap, ttl: ttl,
	}
}

// shouldUpsert reports whether p needs a fresh upsert at now. Entries
// inside ttl are considered fresh and return false; once the map hits
// cap, one arbitrary entry is evicted per admission so memory stays
// bounded. The TTL path is what keeps hits fresh — strict LRU isn't
// worth the cost here.
func (l *displayCacheLRU) shouldUpsert(p owners.Principal, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if t, ok := l.seen[p]; ok && now.Sub(t) < l.ttl {
		return false
	}
	if len(l.seen) >= l.cap {
		for k := range l.seen {
			delete(l.seen, k)
			break
		}
	}
	l.seen[p] = now
	return true
}

// WithPrincipalDisplayCache wraps next with a middleware that upserts
// the observed display handle into principal_display. The upsert is
// synchronous but bounded by a 50ms context timeout so a hung DB
// cannot stall the grantee read path. Errors are logged at warn and
// swallowed — display handles are a UX enhancement, not a correctness
// input.
//
// Identity-less requests (no middleware in front, or a handler mounted
// outside the authenticated tree) are a no-op. An identity whose
// Handle is empty is also a no-op: we only cache handles that were
// actually observed.
func WithPrincipalDisplayCache(repo *share.PrincipalDisplayRepo, logger *slog.Logger) func(http.Handler) http.Handler {
	lru := newDisplayCacheLRU(10_000, time.Minute)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ident, ok := IdentityFromContext(r.Context())
			if !ok || ident.Principal.Handle == "" {
				next.ServeHTTP(w, r)
				return
			}
			now := time.Now().UTC()
			if !lru.shouldUpsert(ident.Principal.OwnersPrincipal(), now) {
				next.ServeHTTP(w, r)
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 50*time.Millisecond)
			defer cancel()
			if err := repo.Upsert(ctx, ident.Principal, now); err != nil {
				logger.Warn("principal_display upsert", "err", err,
					"hub", ident.Principal.Hub, "user_id", ident.Principal.UserID)
			}
			next.ServeHTTP(w, r)
		})
	}
}
