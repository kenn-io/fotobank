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
