package obs

import (
	"context"
	"errors"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestReady_FlipsOnShutdown(t *testing.T) {
	r := require.New(t)
	ready := NewReady()
	checks := []ReadyCheck{{
		Name: "always_ok",
		Fn:   func(context.Context) error { return nil },
	}}
	h := NewReadyzHandler(ready, checks, ReadyzConfig{
		DeadlineTotal: 50 * time.Millisecond,
		CacheTTL:      0, // no cache for this test
	})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
	r.Equal(200, rec.Code)

	ready.Store(false)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
	r.Equal(503, rec.Code)
}

func TestReadyz_BoundedDeadline(t *testing.T) {
	r := require.New(t)
	ready := NewReady()
	checks := []ReadyCheck{{
		Name: "slow",
		Fn: func(ctx context.Context) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(200 * time.Millisecond):
				return nil
			}
		},
	}}
	h := NewReadyzHandler(ready, checks, ReadyzConfig{
		DeadlineTotal: 50 * time.Millisecond,
		CacheTTL:      0,
	})

	start := time.Now()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
	elapsed := time.Since(start)
	r.Less(elapsed, 100*time.Millisecond,
		"deadline 50ms must surface fast; took %s", elapsed)
	r.Equal(503, rec.Code)
	r.Contains(rec.Body.String(), `"slow"`)
	r.Contains(rec.Body.String(), `"fail"`)
}

func TestReadyz_CachesSuccess(t *testing.T) {
	r := require.New(t)
	ready := NewReady()
	var calls atomic.Int64
	checks := []ReadyCheck{{
		Name: "counts",
		Fn: func(context.Context) error {
			calls.Add(1)
			return nil
		},
	}}
	h := NewReadyzHandler(ready, checks, ReadyzConfig{
		DeadlineTotal: time.Second,
		CacheTTL:      time.Hour,
	})
	for range 5 {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
		r.Equal(200, rec.Code)
	}
	r.EqualValues(1, calls.Load(), "cache must collapse 5 probes to 1 check pass")
}

func TestReadyz_ShutdownBypassesCache(t *testing.T) {
	r := require.New(t)
	ready := NewReady()
	checks := []ReadyCheck{{
		Name: "ok", Fn: func(context.Context) error { return nil },
	}}
	h := NewReadyzHandler(ready, checks, ReadyzConfig{
		DeadlineTotal: time.Second,
		CacheTTL:      time.Hour,
	})
	// Prime the cache with a 200.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
	r.Equal(200, rec.Code)

	// Flip ready false; cache is still warm but must be bypassed.
	ready.Store(false)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
	r.Equal(503, rec.Code)
	r.Contains(rec.Body.String(), `"shutting_down"`)
}

func TestReadyz_SingleflightCollapsesConcurrentMisses(t *testing.T) {
	r := require.New(t)
	ready := NewReady()
	var calls atomic.Int64
	gate := make(chan struct{})
	checks := []ReadyCheck{{
		Name: "blocking",
		Fn: func(context.Context) error {
			<-gate
			calls.Add(1)
			return nil
		},
	}}
	h := NewReadyzHandler(ready, checks, ReadyzConfig{
		DeadlineTotal: time.Second,
		CacheTTL:      0, // every probe misses the cache
	})

	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
		})
	}
	// Give the goroutines time to all enter the singleflight wait.
	time.Sleep(20 * time.Millisecond)
	close(gate)
	wg.Wait()
	r.EqualValues(1, calls.Load(),
		"singleflight must collapse 10 concurrent probes into 1 check pass")
}

func TestReadyz_PerCheckErrorsAggregated(t *testing.T) {
	r := require.New(t)
	ready := NewReady()
	checks := []ReadyCheck{
		{Name: "ok", Fn: func(context.Context) error { return nil }},
		{Name: "broken", Fn: func(context.Context) error { return errors.New("dial: refused") }},
	}
	h := NewReadyzHandler(ready, checks, ReadyzConfig{
		DeadlineTotal: time.Second,
		CacheTTL:      0,
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
	r.Equal(503, rec.Code)
	r.Contains(rec.Body.String(), `"ok"`)
	r.Contains(rec.Body.String(), `"broken"`)
	r.Contains(rec.Body.String(), `dial: refused`)
}
