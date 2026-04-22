package httpapi_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/errs"
	"github.com/wesm/fotobank/internal/httpapi"
	"github.com/wesm/fotobank/internal/identity"
	"github.com/wesm/fotobank/internal/owners"
)

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
