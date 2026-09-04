package content

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/docbank"
	"go.kenn.io/fotobank/internal/errs"
)

func TestTranslateError(t *testing.T) {
	tests := []struct {
		name     string
		upstream error
		want     error
	}{
		{name: "digest mismatch", upstream: docbank.ErrDigestMismatch, want: errs.ErrContentIdentityMismatch},
		{name: "size mismatch", upstream: docbank.ErrSizeMismatch, want: errs.ErrContentIdentityMismatch},
		{name: "content conflict", upstream: docbank.ErrContentConflict, want: errs.ErrContentConflict},
		{name: "content unavailable", upstream: docbank.ErrContentUnavailable, want: errs.ErrContentUnavailable},
		{name: "not found", upstream: docbank.ErrNotFound, want: errs.ErrNotFound},
		{name: "backup repository locked", upstream: docbank.ErrBackupRepositoryLocked, want: errs.ErrBackupRepositoryLocked},
		{name: "backup restore target active", upstream: docbank.ErrBackupRestoreTargetActive, want: errs.ErrBackupRestoreTargetActive},
		{name: "backup restore target changed", upstream: docbank.ErrBackupRestoreTargetChanged, want: errs.ErrBackupRestoreTargetChanged},
		{name: "backup restore target not empty", upstream: docbank.ErrBackupRestoreTargetNotEmpty, want: errs.ErrBackupRestoreTargetNotEmpty},
		{name: "backup restore overlap", upstream: docbank.ErrBackupRestoreTargetOverlap, want: errs.ErrBadConfiguration},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			upstream := fmt.Errorf("upstream detail: %w", tt.upstream)
			got := translateError(upstream)
			require.ErrorIs(got, tt.want)
			require.ErrorIs(got, tt.upstream)
			if tt.want == errs.ErrContentIdentityMismatch {
				require.NotErrorIs(got, errs.ErrInvalidArgument)
				require.NotErrorIs(got, errs.ErrContentConflict)
			}
		})
	}
}
