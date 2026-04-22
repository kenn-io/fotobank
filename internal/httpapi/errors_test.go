package httpapi_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/errs"
	"github.com/wesm/fotobank/internal/httpapi"
)

func TestTranslateMapsSentinels(t *testing.T) {
	cases := []struct {
		in   error
		want int
	}{
		{errs.ErrNotFound, 404},
		{errs.ErrAlreadyExists, 409},
		{errs.ErrInvalidArgument, 400},
		{errs.ErrPermissionDenied, 403},
		{errs.ErrOwnerMismatch, 403},
		{errs.ErrConcurrentImport, 409},
		{errs.ErrBrokerUnavailable, 503},
		{errs.ErrIdentityMissing, 401},
		{errs.ErrDirectAccessBlocked, 403},
		{errs.ErrBadConfiguration, 500},
		{errors.New("random"), 500},
	}
	for _, c := range cases {
		got := httpapi.Translate(c.in)
		require.NotNil(t, got)
		require.Equal(t, c.want, httpapi.StatusFrom(got), "%v", c.in)
	}
}

func TestTranslateNilReturnsNil(t *testing.T) {
	// Regression: Translate(nil) used to panic on the default branch
	// when it called err.Error(). It should now return nil so callers
	// can treat it symmetrically with StatusFrom.
	require.Nil(t, httpapi.Translate(nil))
}

func TestTranslateHidesInternalErrorMessage(t *testing.T) {
	// Regression: unknown errors (non-sentinel) must not leak their
	// message through the 500 response body. Callers are expected to
	// log the original err; wire messages should be the generic
	// http.StatusText for 500.
	leaky := errors.New("sql: database is locked at /var/fotobank/registry.sqlite")
	got := httpapi.Translate(leaky)
	require.Equal(t, 500, httpapi.StatusFrom(got))
	require.NotContains(t, got.Error(), "registry.sqlite")
}
