package ingest

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/errs"
)

func TestSettleCandidateRejectsFileChangedBetweenObservations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "changing.jpg")
	require.NoError(t, os.WriteFile(path, []byte("first"), 0o600))

	done := make(chan error, 1)
	go func() {
		_, err := settleCandidate(context.Background(), path, 100*time.Millisecond)
		done <- err
	}()
	time.Sleep(30 * time.Millisecond)
	require.NoError(t, os.WriteFile(path, []byte("second version"), 0o600))

	err := <-done
	require.ErrorIs(t, err, errs.ErrContentConflict)
}
