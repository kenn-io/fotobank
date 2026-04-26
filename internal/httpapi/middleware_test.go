package httpapi_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/errs"
	"github.com/wesm/fotobank/internal/httpapi"
	"github.com/wesm/fotobank/internal/identity"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/share"
	"github.com/wesm/fotobank/internal/testutil"
)

func newDiscardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestMiddlewareAttachesIdentityToContext(t *testing.T) {
	r := require.New(t)
	idp := identity.NewStub(owners.Principal{Hub: "h", UserID: "u"}, "User")

	var captured identity.Identity
	h := httpapi.WithMiddleware(idp)(http.HandlerFunc(func(_ http.ResponseWriter, req *http.Request) {
		captured, _ = httpapi.IdentityFromContext(req.Context())
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	r.Equal("h", captured.Principal.Hub)
	r.Equal("u", captured.Principal.UserID)
}

func TestMiddlewareGeneratesRequestIDIfAbsent(t *testing.T) {
	idp := identity.NewStub(owners.Principal{Hub: "h", UserID: "u"}, "")
	var got string
	h := httpapi.WithMiddleware(idp)(http.HandlerFunc(func(_ http.ResponseWriter, req *http.Request) {
		got = httpapi.RequestIDFromContext(req.Context())
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	require.NotEmpty(t, got)
}

func TestMiddlewareSurfacesIdentityError(t *testing.T) {
	r := require.New(t)
	idp := &errIdentityProvider{err: errs.ErrIdentityMissing}
	h := httpapi.WithMiddleware(idp)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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
	h := httpapi.WithMiddleware(idp)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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
	h := httpapi.WithMiddleware(idp)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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
