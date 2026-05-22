package ingest_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/ingest"
)

func TestLockIsExclusive(t *testing.T) {
	r := require.New(t)
	nasRoot := t.TempDir()
	lockPath := filepath.Join(nasRoot, ".fotobank", "import.lock")

	u1, err := ingest.Acquire(context.Background(), lockPath, 0)
	r.NoError(err)
	defer u1()

	_, err = ingest.Acquire(context.Background(), lockPath, 100*time.Millisecond)
	r.ErrorIs(err, errs.ErrConcurrentImport)
}

func TestLockIsReleasedAfterUnlock(t *testing.T) {
	r := require.New(t)
	nasRoot := t.TempDir()
	lockPath := filepath.Join(nasRoot, ".fotobank", "import.lock")

	u1, err := ingest.Acquire(context.Background(), lockPath, 0)
	r.NoError(err)
	u1()

	u2, err := ingest.Acquire(context.Background(), lockPath, 0)
	r.NoError(err)
	defer u2()
}
