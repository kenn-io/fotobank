package httpapi

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/fotobank/internal/errs"
)

func TestBackupRepositoryLockedIsConflict(t *testing.T) {
	err := backupRepositoryError(fmt.Errorf("verify archive: %w", errs.ErrBackupRepositoryLocked))
	require.Equal(t, http.StatusConflict, err.GetStatus())
	require.Contains(t, err.Error(), "backup repository is locked")
}
