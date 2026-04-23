package ingest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"

	"github.com/wesm/fotobank/internal/errs"
)

// Acquire takes an advisory exclusive lock on lockPath. wait is the
// maximum time to block before returning ErrConcurrentImport; 0 means
// "return immediately if busy".
func Acquire(ctx context.Context, lockPath string, wait time.Duration) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return nil, fmt.Errorf("mkdir lock dir: %w", err)
	}
	l := flock.New(lockPath)

	if wait <= 0 {
		ok, err := l.TryLock()
		if err != nil {
			return nil, fmt.Errorf("flock: %w", err)
		}
		if !ok {
			return nil, fmt.Errorf("%w: another import is in progress", errs.ErrConcurrentImport)
		}
		return func() { _ = l.Unlock() }, nil
	}

	waitCtx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()

	ok, err := l.TryLockContext(waitCtx, 50*time.Millisecond)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("%w: another import is in progress", errs.ErrConcurrentImport)
		}
		return nil, fmt.Errorf("flock: %w", err)
	}
	if !ok {
		return nil, fmt.Errorf("%w: another import is in progress", errs.ErrConcurrentImport)
	}
	return func() { _ = l.Unlock() }, nil
}
