package errs_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/fotobank/internal/errs"
)

func TestSentinelsAreDistinct(t *testing.T) {
	require := require.New(t)
	sentinels := []error{
		errs.ErrNotFound, errs.ErrAlreadyExists, errs.ErrInvalidArgument,
		errs.ErrPermissionDenied, errs.ErrOwnerMismatch, errs.ErrConcurrentImport,
		errs.ErrBrokerUnavailable, errs.ErrIdentityMissing, errs.ErrDirectAccessBlocked,
		errs.ErrMigrationPrecondition, errs.ErrBadConfiguration,
		errs.ErrContentConflict, errs.ErrContentIdentityMismatch,
		errs.ErrContentUnavailable,
	}
	for i, a := range sentinels {
		for j, b := range sentinels {
			if i == j {
				require.ErrorIs(a, b)
				continue
			}
			require.NotErrorIs(a, b, "sentinels at %d and %d collapsed", i, j)
		}
	}
}
