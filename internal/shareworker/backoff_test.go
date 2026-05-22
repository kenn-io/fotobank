package shareworker_test

import (
	"math"
	"math/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/shareworker"
)

// zeroJitterRng is a *rand.Rand whose Float64 always returns 0.5 so the
// jitter term cancels to zero (jitter = raw * pct * (2*0.5 - 1) == 0).
func zeroJitterRng() *rand.Rand {
	return rand.New(halfSource{})
}

type halfSource struct{}

func (halfSource) Int63() int64 { return 1 << 62 } // maps Float64 to 0.5
func (halfSource) Seed(int64)   {}

func TestBackoffTableWithoutJitter(t *testing.T) {
	r := require.New(t)
	rng := zeroJitterRng()
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{1, 30 * time.Second},
		{2, 60 * time.Second},
		{3, 120 * time.Second},
		{4, 240 * time.Second},
		{5, 480 * time.Second},
		{6, 960 * time.Second},
		{7, 1920 * time.Second},
		{8, 1 * time.Hour},
		{9, 1 * time.Hour},
		{10, 1 * time.Hour},
	}
	for _, tc := range cases {
		got := shareworker.Backoff(tc.attempt, rng)
		r.Equalf(tc.want, got, "attempt=%d", tc.attempt)
	}
}

func TestBackoffClampsAttemptsBelowOne(t *testing.T) {
	r := require.New(t)
	rng := zeroJitterRng()
	r.Equal(30*time.Second, shareworker.Backoff(0, rng))
	r.Equal(30*time.Second, shareworker.Backoff(-5, rng))
}

func TestBackoffJitterBandedAroundRaw(t *testing.T) {
	r := require.New(t)
	rng := rand.New(rand.NewSource(42))
	for range 100 {
		got := shareworker.Backoff(3, rng)     // raw = 120s
		r.GreaterOrEqual(got, 108*time.Second) // 120s - 10% = 108s
		r.LessOrEqual(got, 132*time.Second)    // 120s + 10% = 132s
	}
}

// MaxBrokerAttempts fences attempts to 10 in production, but the pure
// Backoff function is defensive: math.Pow(2, MaxInt) overflows to
// +Inf, which the raw > maxDelay guard catches, so extreme inputs
// still yield maxDelay (pre-jitter) rather than NaN or negative
// durations.
func TestBackoffHandlesExtremeAttempts(t *testing.T) {
	r := require.New(t)
	rng := zeroJitterRng()
	r.Equal(1*time.Hour, shareworker.Backoff(math.MaxInt, rng))
}
