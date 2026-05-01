// Package worker contains the AI claim/lease loop and supporting types.
// VisionSemaphore is the global cap on simultaneous in-flight VLM calls
// across all task workers — without it, per-task concurrency caps would
// still allow tag and caption to oversubscribe a single GPU.
package worker

import "context"

// VisionSemaphore is a counting semaphore around the [ai.vision]
// max_inflight cap. Acquire blocks until a slot is available or ctx
// is canceled; Release returns a slot.
type VisionSemaphore struct {
	ch chan struct{}
}

// NewVisionSemaphore constructs a semaphore with `n` slots. Callers
// must ensure n >= 1; ai.Config.Validate already enforces this.
func NewVisionSemaphore(n int) *VisionSemaphore {
	return &VisionSemaphore{ch: make(chan struct{}, n)}
}

// Acquire grabs one slot. Returns ctx.Err() if canceled before a slot
// becomes available.
func (s *VisionSemaphore) Acquire(ctx context.Context) error {
	select {
	case s.ch <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Release returns a slot. Always paired with a successful Acquire.
func (s *VisionSemaphore) Release() {
	<-s.ch
}
