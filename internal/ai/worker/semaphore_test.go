package worker_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/ai/worker"
)

func TestSemaphoreLimitsConcurrency(t *testing.T) {
	sem := worker.NewVisionSemaphore(2)
	var inflight, peak atomic.Int32

	run := func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		require.NoError(t, sem.Acquire(ctx))
		defer sem.Release()
		current := inflight.Add(1)
		for {
			if cur := peak.Load(); cur >= current || peak.CompareAndSwap(cur, current) {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
		inflight.Add(-1)
	}

	done := make(chan struct{})
	for range 10 {
		go func() { run(); done <- struct{}{} }()
	}
	for range 10 {
		<-done
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
