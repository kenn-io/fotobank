// Package shareworker is the outbox worker that drives scopes through
// their broker-side state machine.
package shareworker

import (
	"math"
	"math/rand"
	"time"
)

const (
	baseDelay = 30 * time.Second
	maxDelay  = 1 * time.Hour
	jitterPct = 10
)

// Backoff returns the delay AFTER attempt n has failed, before attempt
// n+1. The worker calls Backoff(scope.BrokerAttempts + 1) on a
// transient failure. Attempt 1 post-fail waits baseDelay (30s),
// attempt 2 waits 60s, and so on; beyond ~attempt 8 the raw delay is
// capped at maxDelay (1h). Jitter is ± jitterPct % of the raw delay
// and is applied after the cap, so the returned duration can exceed
// maxDelay by up to jitterPct (e.g., 1h → up to 66m). rng is
// injected so tests can pin it.
//
// No-jitter delays in seconds (n=1..9):
//
//	30, 60, 120, 240, 480, 960, 1920, 3600, 3600.
//
// Sum of n=1..9 (the nine gaps before the 10th and terminal attempt)
// is ~11010 s ≈ 3h03m.
func Backoff(attempt int, rng *rand.Rand) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	raw := float64(baseDelay) * math.Pow(2, float64(attempt-1))
	if raw > float64(maxDelay) {
		raw = float64(maxDelay)
	}
	jitter := raw * (float64(jitterPct) / 100) * (2*rng.Float64() - 1)
	return time.Duration(raw + jitter)
}
