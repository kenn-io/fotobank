package httpapi_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/auth/hidden"
	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/httpapi"
	"go.kenn.io/fotobank/internal/identity"
	"go.kenn.io/fotobank/internal/obs"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/share"
	"go.kenn.io/fotobank/internal/testutil"
)

func newDiscardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// withTestMiddleware wraps next with the new metrics+recovery+identity
// middleware using a discard logger and a test metrics registry. Used
// by the existing identity-focused tests that don't need to assert on
// log output or counters.
func withTestMiddleware(idp identity.Provider) func(http.Handler) http.Handler {
	return httpapi.WithMiddleware(httpapi.WithMiddlewareDeps{
		Provider: idp,
		Logger:   newDiscardLogger(),
		Metrics:  obs.NewTestMetrics(),
	})
}

func TestMiddlewareAttachesIdentityToContext(t *testing.T) {
	r := require.New(t)
	idp := identity.NewStub(owners.Principal{Hub: "h", UserID: "u"}, "User")

	var captured identity.Identity
	h := withTestMiddleware(idp)(http.HandlerFunc(func(_ http.ResponseWriter, req *http.Request) {
		captured, _ = httpapi.IdentityFromContext(req.Context())
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	r.Equal("h", captured.Principal.Hub)
	r.Equal("u", captured.Principal.UserID)
}

func TestMiddlewareGeneratesRequestIDIfAbsent(t *testing.T) {
	idp := identity.NewStub(owners.Principal{Hub: "h", UserID: "u"}, "")
	var got string
	h := withTestMiddleware(idp)(http.HandlerFunc(func(_ http.ResponseWriter, req *http.Request) {
		got = httpapi.RequestIDFromContext(req.Context())
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	require.NotEmpty(t, got)
}

func TestMiddlewareSurfacesIdentityError(t *testing.T) {
	r := require.New(t)
	idp := &errIdentityProvider{err: errs.ErrIdentityMissing}
	h := withTestMiddleware(idp)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	r.Equal(http.StatusUnauthorized, rec.Code)
	body, err := io.ReadAll(rec.Body)
	r.NoError(err)
	r.Contains(string(body), http.StatusText(http.StatusUnauthorized))
}

func TestMiddlewareSanitizesInternalErrorBody(t *testing.T) {
	// Regression: internal provider errors must not leak backend details
	// to the client. 500 responses should carry only http.StatusText.
	r := require.New(t)
	leakySecret := "password=hunter2 internal trace: /etc/foo"
	idp := &errIdentityProvider{err: errors.New(leakySecret)}
	h := withTestMiddleware(idp)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	r.Equal(http.StatusInternalServerError, rec.Code)

	body, err := io.ReadAll(rec.Body)
	r.NoError(err)
	r.NotContains(string(body), leakySecret)
	r.Contains(string(body), http.StatusText(http.StatusInternalServerError))
}

func TestMiddlewareSanitizesWrappedSentinel(t *testing.T) {
	// Regression: errors.Is matches wrapped sentinels, so an internal
	// detail around ErrIdentityMissing would have been echoed to the
	// 401 body. Middleware must emit only http.StatusText for the
	// mapped status — the full err goes to slog, not the wire.
	r := require.New(t)
	leaky := "db path /srv/fotobank/nas"
	idp := &errIdentityProvider{err: fmt.Errorf("%s: %w", leaky, errs.ErrIdentityMissing)}
	h := withTestMiddleware(idp)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	r.Equal(http.StatusUnauthorized, rec.Code)

	body, err := io.ReadAll(rec.Body)
	r.NoError(err)
	r.NotContains(string(body), leaky)
	r.Contains(string(body), http.StatusText(http.StatusUnauthorized))
}

type errIdentityProvider struct{ err error }

func (e *errIdentityProvider) FromRequest(context.Context, *http.Request) (identity.Identity, error) {
	return identity.Identity{}, e.err
}

// failingProvider is a local test adapter — there is no
// identity.ProviderFunc in the production package; tests construct a
// tiny struct that implements identity.Provider.
type failingProvider struct{ err error }

func (f failingProvider) FromRequest(context.Context, *http.Request) (identity.Identity, error) {
	return identity.Identity{}, f.err
}

func TestRequestIDHeaderSetBeforeIdentity(t *testing.T) {
	r := require.New(t)
	failing := failingProvider{err: errs.ErrIdentityMissing}
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, nil))
	m := obs.NewTestMetrics()

	mw := httpapi.WithMiddleware(httpapi.WithMiddlewareDeps{
		Provider: failing,
		Logger:   logger,
		Metrics:  m,
	})
	reachedHandler := false
	h := mw(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		reachedHandler = true
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))
	r.False(reachedHandler, "identity-rejected request must not reach handler")
	r.Equal(401, rec.Code)
	r.NotEmpty(rec.Header().Get("X-Request-ID"),
		"X-Request-ID must be set before identity resolution")
	r.Contains(logBuf.String(), `"req_id"`)
	// Identity-rejected request still increments the 4xx counter.
	r.EqualValues(1, m.HTTPRequests("GET", "unmatched", "4xx").Get(),
		"metrics must record 4xx for identity rejection")
}

func TestPanicRecoveryRecordsAs5xx(t *testing.T) {
	r := require.New(t)
	idp := identity.NewStub(owners.Principal{Hub: "h", UserID: "u"}, "")
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	m := obs.NewTestMetrics()

	mux := http.NewServeMux()
	mux.Handle("GET /panic", httpapi.WrapMuxHandler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("expected by test")
	})))
	mw := httpapi.WithMiddleware(httpapi.WithMiddlewareDeps{
		Provider: idp,
		Logger:   logger,
		Metrics:  m,
	})
	srv := httptest.NewServer(mw(mux))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/panic")
	r.NoError(err)
	defer resp.Body.Close()
	r.Equal(500, resp.StatusCode)
	r.EqualValues(1, m.HTTPRequests("GET", "/panic", "5xx").Get(),
		"panic must be recorded as 5xx after recovery")
}

func TestSuccessfulRequestRecordedAs2xxAndCarriesDurMs(t *testing.T) {
	r := require.New(t)
	idp := identity.NewStub(owners.Principal{Hub: "h", UserID: "u"}, "")
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, nil))
	m := obs.NewTestMetrics()

	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/healthz", httpapi.WrapMuxHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	})))
	mw := httpapi.WithMiddleware(httpapi.WithMiddlewareDeps{
		Provider: idp,
		Logger:   logger,
		Metrics:  m,
	})
	srv := httptest.NewServer(mw(mux))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/healthz")
	r.NoError(err)
	defer resp.Body.Close()
	r.Equal(200, resp.StatusCode)
	r.EqualValues(1, m.HTTPRequests("GET", "/api/v1/healthz", "2xx").Get())

	out := logBuf.String()
	r.Contains(out, `"dur_ms"`, "log must use dur_ms field name")
	r.Contains(out, `"req_id"`, "log must use req_id field name")
	r.NotContains(out, `"dur":`, "legacy dur field must be gone")
	r.NotContains(out, `"req":`, "legacy req field must be gone")
	r.Contains(out, `"component":"httpapi"`)
	r.Contains(out, `"principal_hub":"h"`)
	r.Contains(out, `"principal_user_id":"u"`)
}

func TestPrincipalDisplayCacheUpsertsSynchronously(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := share.NewPrincipalDisplayRepo(d.WriteDB(), d.ReadDB())

	var called bool
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	mw := httpapi.WithPrincipalDisplayCache(repo, newDiscardLogger())

	ctx := httpapi.ContextWithIdentity(context.Background(), identity.Identity{
		Principal: identity.Principal{Hub: "h", UserID: "alice", Handle: "Alice"},
	})
	req := httptest.NewRequest(http.MethodGet, "/x", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	mw(next).ServeHTTP(rec, req)
	r.True(called)

	handle, ok, err := repo.Get(context.Background(),
		owners.Principal{Hub: "h", UserID: "alice"})
	r.NoError(err)
	r.True(ok)
	r.Equal("Alice", handle)
}

func TestPrincipalDisplayCacheEmptyHandleSkipsUpsert(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := share.NewPrincipalDisplayRepo(d.WriteDB(), d.ReadDB())
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mw := httpapi.WithPrincipalDisplayCache(repo, newDiscardLogger())

	ctx := httpapi.ContextWithIdentity(context.Background(),
		identity.Identity{Principal: identity.Principal{Hub: "h", UserID: "alice"}})
	req := httptest.NewRequest(http.MethodGet, "/x", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	mw(next).ServeHTTP(rec, req)

	_, ok, err := repo.Get(context.Background(),
		owners.Principal{Hub: "h", UserID: "alice"})
	r.NoError(err)
	r.False(ok)
}

func TestPrincipalDisplayCacheNoIdentityIsNoOp(t *testing.T) {
	d := testutil.OpenTestDB(t)
	repo := share.NewPrincipalDisplayRepo(d.WriteDB(), d.ReadDB())
	called := false
	next := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { called = true })
	mw := httpapi.WithPrincipalDisplayCache(repo, newDiscardLogger())
	rec := httptest.NewRecorder()
	mw(next).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	require.True(t, called)
}

// Second request inside TTL must not hit the DB. We can't easily assert
// "no DB call" directly, but we can assert that the Upsert-observable
// state (the updated_at column) does not change between the first and
// second request that cached principal sees.
func TestPrincipalDisplayCacheTTLSuppressesRepeatUpsert(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := share.NewPrincipalDisplayRepo(d.WriteDB(), d.ReadDB())
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mw := httpapi.WithPrincipalDisplayCache(repo, newDiscardLogger())

	ctx := httpapi.ContextWithIdentity(context.Background(),
		identity.Identity{Principal: identity.Principal{Hub: "h", UserID: "alice", Handle: "Alice"}})
	req := httptest.NewRequest(http.MethodGet, "/x", nil).WithContext(ctx)
	rec1 := httptest.NewRecorder()
	mw(next).ServeHTTP(rec1, req)
	h1, ok1, err := repo.Get(context.Background(), owners.Principal{Hub: "h", UserID: "alice"})
	r.NoError(err)
	r.True(ok1)
	r.Equal("Alice", h1)

	// Change the underlying row directly; if the middleware re-upserts,
	// the second call will overwrite our synthetic "Alice2" handle.
	_, err = d.WriteDB().ExecContext(context.Background(),
		`UPDATE principal_display SET handle = ? WHERE hub = ? AND user_id = ?`,
		"Alice2", "h", "alice")
	r.NoError(err)

	rec2 := httptest.NewRecorder()
	mw(next).ServeHTTP(rec2, req)
	h2, ok2, err := repo.Get(context.Background(), owners.Principal{Hub: "h", UserID: "alice"})
	r.NoError(err)
	r.True(ok2)
	r.Equal("Alice2", h2, "TTL suppression: second request within ttl must not re-upsert")
}

func TestLoggerFromContext_FallbackToDefault(t *testing.T) {
	// No logger in context -> returns slog.Default(); never returns nil.
	lg := httpapi.LoggerFromContext(context.Background())
	require.NotNil(t, lg)
}

func TestLoggerFromContext_ReturnsAttached(t *testing.T) {
	r := require.New(t)
	want := slog.New(slog.NewJSONHandler(io.Discard, nil)).With("k", "v")
	ctx := httpapi.WithLogger(context.Background(), want)
	got := httpapi.LoggerFromContext(ctx)
	r.Equal(want, got)
}

// --- WithHiddenUnlock middleware tests ---

// hiddenMiddleFx is a test fixture for WithHiddenUnlock.
type hiddenMiddleFx struct {
	repo      *hidden.Repo
	svc       *hidden.Service
	cookie    hidden.CookieConfig
	principal owners.Principal
	now       time.Time
}

func newHiddenMiddleFx(t *testing.T) hiddenMiddleFx {
	t.Helper()
	d := testutil.OpenTestDB(t)
	rw := d.WriteDB()
	ro := d.ReadDB()
	p := owners.Principal{Hub: "h", UserID: "u"}
	// Seed the owner row so FK constraints on auth_hidden_* tables pass.
	_, err := rw.ExecContext(context.Background(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		p.Hub, p.UserID, "sk", time.Now().UTC())
	require.NoError(t, err)

	repo := hidden.NewRepo(rw, ro)
	media := &fakeHiddenMedia{}
	svc := hidden.NewService(repo, media)
	now := time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC)
	return hiddenMiddleFx{
		repo:      repo,
		svc:       svc,
		cookie:    hidden.CookieConfigFor(false), // prod cookie
		principal: p,
		now:       now,
	}
}

// fakeHiddenMedia satisfies hidden.MediaPrivacy for middleware test fixtures.
type fakeHiddenMedia struct{}

func (f *fakeHiddenMedia) ClearAllHiddenForOwner(_ context.Context, _ owners.Principal) error {
	return nil
}

// issueSession seeds a credential + issues a session for fx.principal.
// Returns the raw token string.
func (fx hiddenMiddleFx) issueSession(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, fx.svc.Setup(ctx, fx.principal, "secret"))
	// Advance clock so session expires in the future.
	fx.svc.SetClockForTest(func() time.Time { return fx.now })
	raw, _, err := fx.svc.Unlock(ctx, fx.principal, "secret")
	require.NoError(t, err)
	return raw
}

// buildUnlockHandler returns a handler that has WithHiddenUnlock applied.
// The inner handler captures the claim and signals done.
func buildUnlockHandler(
	svc *hidden.Service, cookie hidden.CookieConfig, now time.Time,
	inner func(context.Context),
) http.Handler {
	innerH := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		inner(r.Context())
	})
	return httpapi.WithHiddenUnlock(innerH, svc, cookie, func() time.Time { return now })
}

// contextWithPrincipal injects an identity into ctx so IdentityFromContext works
// inside WithHiddenUnlock.
func contextWithPrincipal(ctx context.Context, p owners.Principal) context.Context {
	return httpapi.ContextWithIdentity(ctx, identity.Identity{
		Principal: identity.Principal{Hub: p.Hub, UserID: p.UserID},
	})
}

func TestHiddenMiddlewareNoCookiePassesThrough(t *testing.T) {
	r := require.New(t)
	fx := newHiddenMiddleFx(t)

	var reached bool
	var claimPresent bool
	h := buildUnlockHandler(fx.svc, fx.cookie, fx.now, func(ctx context.Context) {
		reached = true
		_, claimPresent = hidden.UnlockClaimFromContext(ctx)
	})
	ctx := contextWithPrincipal(context.Background(), fx.principal)
	req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
	h.ServeHTTP(httptest.NewRecorder(), req)

	r.True(reached)
	r.False(claimPresent, "no cookie → no claim")
}

func TestHiddenMiddlewareValidCookieAttachesClaim(t *testing.T) {
	r := require.New(t)
	fx := newHiddenMiddleFx(t)
	raw := fx.issueSession(t)

	var claim hidden.UnlockClaim
	var claimPresent bool
	h := buildUnlockHandler(fx.svc, fx.cookie, fx.now, func(ctx context.Context) {
		claim, claimPresent = hidden.UnlockClaimFromContext(ctx)
	})
	ctx := contextWithPrincipal(context.Background(), fx.principal)
	req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
	req.AddCookie(&http.Cookie{Name: fx.cookie.Name, Value: raw})

	h.ServeHTTP(httptest.NewRecorder(), req)

	r.True(claimPresent)
	r.Equal(fx.principal, claim.Principal)
	r.False(claim.ExpiresAt.IsZero())
}

func TestHiddenMiddlewareMalformedCookieNoError(t *testing.T) {
	r := require.New(t)
	fx := newHiddenMiddleFx(t)

	var reached bool
	var claimPresent bool
	h := buildUnlockHandler(fx.svc, fx.cookie, fx.now, func(ctx context.Context) {
		reached = true
		_, claimPresent = hidden.UnlockClaimFromContext(ctx)
	})
	ctx := contextWithPrincipal(context.Background(), fx.principal)
	req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
	req.AddCookie(&http.Cookie{Name: fx.cookie.Name, Value: "not!valid!base64url!!"})

	h.ServeHTTP(httptest.NewRecorder(), req)
	r.True(reached)
	r.False(claimPresent, "malformed cookie → no claim, no error")
}

func TestHiddenMiddlewareNoSessionRowNoError(t *testing.T) {
	// Cookie value is valid base64url but has no matching session in the DB.
	r := require.New(t)
	fx := newHiddenMiddleFx(t)
	// 43-char base64url token that was never inserted.
	fakeTok := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

	var reached bool
	var claimPresent bool
	h := buildUnlockHandler(fx.svc, fx.cookie, fx.now, func(ctx context.Context) {
		reached = true
		_, claimPresent = hidden.UnlockClaimFromContext(ctx)
	})
	ctx := contextWithPrincipal(context.Background(), fx.principal)
	req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
	req.AddCookie(&http.Cookie{Name: fx.cookie.Name, Value: fakeTok})

	h.ServeHTTP(httptest.NewRecorder(), req)
	r.True(reached)
	r.False(claimPresent, "unknown token → ErrNotFound swallowed, no claim")
}

func TestHiddenMiddlewareRevokedSessionNoError(t *testing.T) {
	r := require.New(t)
	fx := newHiddenMiddleFx(t)
	raw := fx.issueSession(t)

	// Revoke the session.
	sha, err := hidden.TokenSHA256(raw)
	require.NoError(t, err)
	require.NoError(t, fx.repo.RevokeSession(context.Background(), sha, fx.now))

	var claimPresent bool
	h := buildUnlockHandler(fx.svc, fx.cookie, fx.now, func(ctx context.Context) {
		_, claimPresent = hidden.UnlockClaimFromContext(ctx)
	})
	ctx := contextWithPrincipal(context.Background(), fx.principal)
	req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
	req.AddCookie(&http.Cookie{Name: fx.cookie.Name, Value: raw})

	h.ServeHTTP(httptest.NewRecorder(), req)
	r.False(claimPresent, "revoked session → LookupActiveSession returns ErrNotFound, swallowed")
}

func TestHiddenMiddlewareExpiredSessionNoError(t *testing.T) {
	r := require.New(t)
	fx := newHiddenMiddleFx(t)
	raw := fx.issueSession(t)

	// Advance the clock past session expiry.
	future := fx.now.Add(10 * time.Minute)

	var claimPresent bool
	h := buildUnlockHandler(fx.svc, fx.cookie, future, func(ctx context.Context) {
		_, claimPresent = hidden.UnlockClaimFromContext(ctx)
	})
	ctx := contextWithPrincipal(context.Background(), fx.principal)
	req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
	req.AddCookie(&http.Cookie{Name: fx.cookie.Name, Value: raw})

	h.ServeHTTP(httptest.NewRecorder(), req)
	r.False(claimPresent, "expired session → LookupActiveSession returns ErrNotFound, swallowed")
}

func TestHiddenMiddlewarePrincipalMismatchNoError(t *testing.T) {
	// Session was issued for principal "u" but request identity is "other".
	r := require.New(t)
	fx := newHiddenMiddleFx(t)
	raw := fx.issueSession(t)

	other := owners.Principal{Hub: "h", UserID: "other"}

	var claimPresent bool
	h := buildUnlockHandler(fx.svc, fx.cookie, fx.now, func(ctx context.Context) {
		_, claimPresent = hidden.UnlockClaimFromContext(ctx)
	})
	ctx := contextWithPrincipal(context.Background(), other)
	req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
	req.AddCookie(&http.Cookie{Name: fx.cookie.Name, Value: raw})

	h.ServeHTTP(httptest.NewRecorder(), req)
	r.False(claimPresent, "principal mismatch → claim must not be attached")
}

func TestHiddenMiddlewareNilDepsPassThrough(t *testing.T) {
	// When svc/repo are nil, middleware is a no-op pass-through.
	r := require.New(t)
	reached := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	})
	h := httpapi.WithHiddenUnlock(inner, nil, hidden.CookieConfigFor(false), time.Now)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	r.True(reached)
	r.Equal(http.StatusOK, rec.Code)
}

// seedHiddenSessionRaw inserts a session for principal p directly into repo
// using a real random token. Returns the raw token.
func seedHiddenSessionRaw(
	t *testing.T, rw *sql.DB, repo *hidden.Repo, p owners.Principal, now, expiresAt time.Time,
) string {
	t.Helper()
	raw, sha, err := hidden.NewToken(rand.Reader)
	require.NoError(t, err)
	require.NoError(t, repo.InsertSession(context.Background(), hidden.Session{
		TokenSHA256: sha,
		Principal:   p,
		IssuedAt:    now,
		ExpiresAt:   expiresAt,
	}))
	_ = rw // passed for future use; unused right now
	return raw
}

func TestHiddenMiddlewareDevCookieName(t *testing.T) {
	// Dev cookie (no __Host- prefix) must also be accepted.
	r := require.New(t)
	fx := newHiddenMiddleFx(t)
	devCookie := hidden.CookieConfigFor(true)

	// Issue a session directly into the DB (bypass service so we control the clock).
	expiresAt := fx.now.Add(5 * time.Minute)
	raw := seedHiddenSessionRaw(t, nil, fx.repo, fx.principal, fx.now, expiresAt)

	var claimPresent bool
	h := buildUnlockHandler(fx.svc, devCookie, fx.now, func(ctx context.Context) {
		_, claimPresent = hidden.UnlockClaimFromContext(ctx)
	})
	ctx := contextWithPrincipal(context.Background(), fx.principal)
	req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
	req.AddCookie(&http.Cookie{Name: devCookie.Name, Value: raw})

	h.ServeHTTP(httptest.NewRecorder(), req)
	r.True(claimPresent, "dev cookie must work too")
}

// TestHiddenMiddlewareIdentityMustRunFirst documents the expected middleware order.
// WithHiddenUnlock reads IdentityFromContext; if identity is NOT in context,
// the principal check fails and no claim is attached — even for a valid cookie.
func TestHiddenMiddlewareIdentityMustRunFirst(t *testing.T) {
	r := require.New(t)
	fx := newHiddenMiddleFx(t)
	expiresAt := fx.now.Add(5 * time.Minute)
	raw := seedHiddenSessionRaw(t, nil, fx.repo, fx.principal, fx.now, expiresAt)

	var claimPresent bool
	h := buildUnlockHandler(fx.svc, fx.cookie, fx.now, func(ctx context.Context) {
		_, claimPresent = hidden.UnlockClaimFromContext(ctx)
	})
	// No identity in context — middleware must NOT attach a claim.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: fx.cookie.Name, Value: raw})

	h.ServeHTTP(httptest.NewRecorder(), req)
	r.False(claimPresent, "no identity in context → no claim even with valid cookie")
}
