package ingest

import (
	"context"

	"github.com/wesm/fotobank/internal/ai"
)

// AIEnqueuer is the surface the importer needs from the AI subsystem.
// Production wiring (cmd/fotobank server) provides a concrete impl
// constructed via NewRealAIEnqueuer; existing import tests get the
// NoopAIEnqueuer{} default from NewImporter so they don't need AI
// tables wired.
type AIEnqueuer interface {
	EnqueueForPhoto(ctx context.Context, mediaID string) error
	RecordVideoSkip(ctx context.Context, mediaID string) error
}

// NoopAIEnqueuer satisfies AIEnqueuer with no side effects. It is the
// default AI handle on a fresh Importer; SetAIEnqueuer swaps in a real
// one when production wiring is available.
type NoopAIEnqueuer struct{}

func (NoopAIEnqueuer) EnqueueForPhoto(_ context.Context, _ string) error { return nil }
func (NoopAIEnqueuer) RecordVideoSkip(_ context.Context, _ string) error { return nil }

// realAIEnqueuer adapts plain function values onto the AIEnqueuer
// interface. The constructor takes function values rather than concrete
// repo handles so internal/ingest doesn't pull in internal/ai/jobs or
// internal/ai/skipped (which would create a cycle if those ever needed
// to import ingest types).
type realAIEnqueuer struct {
	tagFP, captionFP ai.Fingerprint
	enqueue          func(ctx context.Context, mid string, t ai.Task, fp ai.Fingerprint) error
	skip             func(ctx context.Context, mid string, t ai.Task, reason string) error
}

// NewRealAIEnqueuer builds a production AIEnqueuer from the per-task
// fingerprints and the queue/skipped repo callbacks. Pass the
// fingerprints resolved at boot from the loaded config + prompts; the
// importer will tag every imported photo with these.
func NewRealAIEnqueuer(
	tagFP, captionFP ai.Fingerprint,
	enqueue func(context.Context, string, ai.Task, ai.Fingerprint) error,
	skip func(context.Context, string, ai.Task, string) error,
) AIEnqueuer {
	return &realAIEnqueuer{tagFP: tagFP, captionFP: captionFP, enqueue: enqueue, skip: skip}
}

func (r *realAIEnqueuer) EnqueueForPhoto(ctx context.Context, mid string) error {
	if err := r.enqueue(ctx, mid, ai.TaskTag, r.tagFP); err != nil {
		return err
	}
	return r.enqueue(ctx, mid, ai.TaskCaption, r.captionFP)
}

func (r *realAIEnqueuer) RecordVideoSkip(ctx context.Context, mid string) error {
	if err := r.skip(ctx, mid, ai.TaskTag, "video"); err != nil {
		return err
	}
	return r.skip(ctx, mid, ai.TaskCaption, "video")
}
