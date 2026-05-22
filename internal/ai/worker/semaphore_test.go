package worker_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/ai/worker"
)

func TestSemaphoreLimitsConcurrency(t *testing.T) {
	sem := worker.NewVisionSemaphore(2)
	var inflight, peak atomic.Int32

	run := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := sem.Acquire(ctx); err != nil {
			return err
		}
		defer sem.Release()
		current := inflight.Add(1)
		for {
			if cur := peak.Load(); cur >= current || peak.CompareAndSwap(cur, current) {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
		inflight.Add(-1)
		return nil
	}

	errs := make(chan error, 10)
	for range 10 {
		go func() { errs <- run() }()
	}
	for range 10 {
		require.NoError(t, <-errs)
	}
	require.LessOrEqual(t, peak.Load(), int32(2))
}

func TestSemaphoreAcquireRespectsCancel(t *testing.T) {
	sem := worker.NewVisionSemaphore(1)
	require.NoError(t, sem.Acquire(context.Background()))
	defer sem.Release()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.Error(t, sem.Acquire(ctx))
}
