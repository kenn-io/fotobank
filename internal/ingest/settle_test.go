package ingest

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/errs"
)

func TestSettleCandidateRejectsFileChangedBetweenObservations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "changing.jpg")
	require.NoError(t, os.WriteFile(path, []byte("first"), 0o600))

	synctest.Test(t, func(t *testing.T) {
		done := make(chan error, 1)
		go func() {
			_, err := settleCandidate(context.Background(), path, 100*time.Millisecond)
			done <- err
		}()
		// Wait until the first observation completes and settling blocks on its
		// timer, then change the file before allowing fake time to advance.
		synctest.Wait()
		require.NoError(t, os.WriteFile(path, []byte("second version"), 0o600))

		err := <-done
		require.ErrorIs(t, err, errs.ErrContentConflict)
	})
}
