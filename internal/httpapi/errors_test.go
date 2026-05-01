package httpapi_test

import (
	"errors"
	"fmt"
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
		{errs.ErrLockedOut, 429},
		{errs.ErrHiddenNotConfigured, 409},
		{errs.ErrAcknowledgementRequired, 409},
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

func TestTranslateHidesWrappedSentinelContext(t *testing.T) {
	// Regression: errors.Is matches through fmt.Errorf(... %w ...),
	// so a wrapped sentinel carrying an internal detail could have
	// echoed the detail through the 404/409/403/... body. Translate
	// must return only the sentinel's own message.
	r := require.New(t)
	leaky := "owner_hub=h user_id=u storage_key=k"
	wrapped := fmt.Errorf("%s: %w", leaky, errs.ErrNotFound)
	got := httpapi.Translate(wrapped)
	r.Equal(404, httpapi.StatusFrom(got))
	r.NotContains(got.Error(), leaky)
	r.Contains(got.Error(), errs.ErrNotFound.Error())
}

// TestTranslateErrPairedSidecarsExist locks in the F2.2 §8.7
// future-contract mapping. No service path throws this sentinel today
// — delete is deferred — but the translator must already map it to
// 409 so a future delete handler inherits the right HTTP shape
// without a translator change.
func TestTranslateErrPairedSidecarsExist(t *testing.T) {
	r := require.New(t)
	se := httpapi.Translate(fmt.Errorf("delete: %w", errs.ErrPairedSidecarsExist))
	r.NotNil(se)
	r.Equal(409, se.GetStatus())
}
