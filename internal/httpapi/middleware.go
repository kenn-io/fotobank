package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/wesm/fotobank/internal/errs"
	"github.com/wesm/fotobank/internal/identity"
	"github.com/wesm/fotobank/internal/obs"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/share"
)

type ctxKey int

const (
	ctxKeyIdentity ctxKey = iota
	ctxKeyRequestID
	ctxKeyObs
)

// WithMiddlewareDeps groups the collaborators consumed by the
// main-listener middleware stack. Provider resolves Identity; Logger
// is the base logger augmented per request; Metrics is the registry
// counter/histogram emitter (nil disables emission).
type WithMiddlewareDeps struct {
	// Provider resolves the caller Identity from the inbound request.
	Provider identity.Provider
	// Logger is the base slog.Logger. Per-request loggers are derived
	// from this with component=httpapi, req_id, and (post identity
	// resolution) principal_* attrs. nil falls back to slog.Default().
	Logger *slog.Logger
	// Metrics is the obs.Metrics registry used to record per-request
	// counters and histograms. nil disables metric recording but the
	// rest of the middleware still runs.
	Metrics *obs.Metrics
	// RequestIDHeader is the inbound header name to read for an
	// upstream-supplied request ID. Empty means generate a fresh UUID
	// per request.
	RequestIDHeader string
}

// requestObs is per-request mutable state shared across the middleware
// chain via context. The outer metrics+log layer allocates one per
// request; inner layers (identity, the route-capture handler wrapper)
// mutate fields on it; the outer layer reads them after the chain
// returns. The indirection exists because r.WithContext clones
// *Request, so outer-layer reads of inner mutations on r (such as
// r.Pattern set by ServeMux) would otherwise be lost.
type requestObs struct {
	routeTemplate string       // "unmatched" until WrapMuxHandler runs
	logger        *slog.Logger // augmented by identityWrap with principal fields
}

// obsFromContext returns the per-request requestObs allocated by
// metricsWrap, or nil if no middleware ran in front of the caller.
// Callers may mutate the fields on the returned struct; mutations are
// visible to the outer metricsWrap layer after the inner chain returns.
func obsFromContext(ctx context.Context) *requestObs {
	o, _ := ctx.Value(ctxKeyObs).(*requestObs)
	return o
}

// WrapMuxHandler captures r.Pattern from the request that ServeMux
// dispatches into the per-request observability state. ServeMux sets
// Pattern on the request it passes to the handler — this wrapper IS
// the handler from ServeMux's perspective, so it sees the populated
// Pattern. Without this wrapper, the outer metrics layer records every
// request as route="unmatched". Apply at registration time wherever
// handlers are mounted on the main mux.
func WrapMuxHandler(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if o := obsFromContext(r.Context()); o != nil {
			o.routeTemplate = normalizeRouteTemplate(r.Pattern)
		}
		h.ServeHTTP(w, r)
	})
}

// WithMiddleware composes the main-listener middleware stack:
//
//	metrics → recovery → identity → handler
//
// Recovery is inside metrics so a recovered 5xx is recorded; metrics
// is outside identity so 401/403 still count as 4xx. The X-Request-ID
// response header is set BEFORE identity resolution so identity-
// rejection logs carry req_id.
//
// httpapi.New (api.go) inserts WithPrincipalDisplayCache between the
// identity layer and the registered mux when deps.PrincipalDisplay !=
// nil; the production request chain is therefore metrics → recovery →
// identity → display-cache → mux.
//
// Per-request mutable state (route template + augmented logger) lives
// in a *requestObs allocated by metricsWrap and stored in ctx. Inner
// layers mutate its fields; the outer layer reads them after the
// inner chain returns. This indirection exists because Go's
// r.WithContext clones the Request, so outer-layer reads of inner
// mutations on r (such as r.Pattern set by ServeMux) would otherwise
// be lost.
func WithMiddleware(deps WithMiddlewareDeps) func(http.Handler) http.Handler {
	idp := deps.Provider
	baseLogger := deps.Logger
	if baseLogger == nil {
		baseLogger = slog.Default()
	}
	m := deps.Metrics

	identityWrap := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
				if o := obsFromContext(r.Context()); o != nil && o.logger != nil {
					o.logger.Warn("request rejected",
						"method", r.Method, "path", r.URL.Path,
						"status", status, "err", err)
				}
				return
			}
			ctx := context.WithValue(r.Context(), ctxKeyIdentity, id)
			// Augment the per-request logger via SHARED state so the
			// outer layer reads the augmented logger after this layer
			// returns (forking ctx with a new logger would leave the
			// outer ctx unchanged).
			if o := obsFromContext(ctx); o != nil {
				o.logger = o.logger.With(
					"principal_hub", id.Principal.Hub,
					"principal_user_id", id.Principal.UserID,
					"principal", id.Principal.OwnersPrincipal().String(),
				)
				ctx = WithLogger(ctx, o.logger)
			}
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}

	recoveryWrap := WithRecovery(baseLogger.With("component", "httpapi"))

	metricsWrap := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			reqID := ""
			if deps.RequestIDHeader != "" {
				reqID = r.Header.Get(deps.RequestIDHeader)
			}
			if reqID == "" {
				reqID = uuid.NewString()
			}
			w.Header().Set("X-Request-ID", reqID)

			ro := &requestObs{
				routeTemplate: "unmatched",
				logger:        baseLogger.With("component", "httpapi", "req_id", reqID),
			}
			ctx := context.WithValue(r.Context(), ctxKeyObs, ro)
			ctx = context.WithValue(ctx, ctxKeyRequestID, reqID)
			ctx = WithLogger(ctx, ro.logger)

			rw := &statusCapture{ResponseWriter: w, code: http.StatusOK}
			next.ServeHTTP(rw, r.WithContext(ctx))

			method := normalizeMethod(r.Method)
			route := ro.routeTemplate
			class := statusClass(rw.code)

			if m != nil {
				m.HTTPRequests(method, route, class).Inc()
				m.HTTPRequestDuration(method, route).
					Update(time.Since(start).Seconds())
			}

			ro.logger.Info("request",
				"method", method, "route", route, "status", rw.code,
				"dur_ms", time.Since(start).Milliseconds())
		})
	}
	return func(next http.Handler) http.Handler {
		return metricsWrap(recoveryWrap(identityWrap(next)))
	}
}

func normalizeMethod(m string) string {
	switch m {
	case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS":
		return m
	default:
		return "OTHER"
	}
}

// normalizeRouteTemplate normalizes a registered ServeMux pattern for
// use as a metric label. Returns "unmatched" when pat is empty.
// Strips method and host prefixes; rewrites "{name}" placeholders to
// ":name" so the metric label is stable across renames.
func normalizeRouteTemplate(pat string) string {
	if pat == "" {
		return "unmatched"
	}
	if i := strings.IndexByte(pat, ' '); i >= 0 {
		pat = pat[i+1:]
	}
	if pat == "" {
		return "unmatched"
	}
	if i := strings.Index(pat, "/"); i > 0 {
		pat = pat[i:]
	}
	var b strings.Builder
	b.Grow(len(pat))
	for i := 0; i < len(pat); {
		if pat[i] == '{' {
			j := strings.IndexByte(pat[i:], '}')
			if j > 0 {
				name := pat[i+1 : i+j]
				name = strings.TrimSuffix(name, "...")
				b.WriteByte(':')
				b.WriteString(name)
				i += j + 1
				continue
			}
		}
		b.WriteByte(pat[i])
		i++
	}
	return b.String()
}

func statusClass(status int) string {
	switch {
	case status >= 200 && status < 300:
		return "2xx"
	case status >= 300 && status < 400:
		return "3xx"
	case status >= 400 && status < 500:
		return "4xx"
	default:
		return "5xx"
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

// Flush forwards to the underlying ResponseWriter when it implements
// http.Flusher. Streaming handlers (notably the SSE route at
// /api/v1/events) type-assert their writer to http.Flusher to push
// frames as they're produced; an embedded http.ResponseWriter does not
// promote Flush onto the wrapper, so without this forward the assertion
// fails and the handler returns 500.
func (c *statusCapture) Flush() {
	if f, ok := c.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap exposes the inner ResponseWriter so http.NewResponseController
// can reach the underlying *http.conn for SetWriteDeadline. Without
// this, SSE handlers can't clear the server's WriteTimeout and
// long-poll subscribers get silently disconnected after the deadline.
func (c *statusCapture) Unwrap() http.ResponseWriter {
	return c.ResponseWriter
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
