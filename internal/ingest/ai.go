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
//
// embedFP is optional: a zero-value fingerprint (ModelID == "") signals
// the operator hasn't enabled the embed pipeline, so the photo path
// stops at tag+caption and the video path stops at the tag+caption
// skip rows. Wire embed by calling RealAIEnqueuer.WithEmbed at
// construction time when cfg.AI.Embed.Enabled is true. Gating on
// ModelID rather than a separate bool keeps the zero value of
// realAIEnqueuer correctly representing "no embed configured."
type realAIEnqueuer struct {
	tagFP, captionFP, embedFP ai.Fingerprint
	enqueue                   func(ctx context.Context, mid string, t ai.Task, fp ai.Fingerprint) error
	skip                      func(ctx context.Context, mid string, t ai.Task, reason string) error
}

// RealAIEnqueuer is the concrete production AIEnqueuer. Returned by
// NewRealAIEnqueuer so callers can chain WithEmbed before passing it
// into Importer.SetAIEnqueuer.
type RealAIEnqueuer = realAIEnqueuer

// NewRealAIEnqueuer builds a production AIEnqueuer from the per-task
// fingerprints and the queue/skipped repo callbacks. Pass the
// fingerprints resolved at boot from the loaded config + prompts; the
// importer will tag every imported photo with these. To wire the embed
// task too, chain WithEmbed onto the result.
func NewRealAIEnqueuer(
	tagFP, captionFP ai.Fingerprint,
	enqueue func(context.Context, string, ai.Task, ai.Fingerprint) error,
	skip func(context.Context, string, ai.Task, string) error,
) *RealAIEnqueuer {
	return &realAIEnqueuer{tagFP: tagFP, captionFP: captionFP, enqueue: enqueue, skip: skip}
}

// WithEmbed sets the embed-task fingerprint and returns the receiver so
// callers can chain it inline after NewRealAIEnqueuer. Pass the
// fingerprint produced by embedding.Fingerprint(cfg.AI.Embed) when the
// operator has flipped cfg.AI.Embed.Enabled. A zero-value fingerprint
// is rejected at the call site by the cfg gate, but defensively the
// EnqueueForPhoto / RecordVideoSkip paths also check ModelID before
// firing the embed-side write — so a misconfigured caller cannot
// accidentally enqueue embed jobs against an empty model.
func (r *realAIEnqueuer) WithEmbed(fp ai.Fingerprint) *RealAIEnqueuer {
	r.embedFP = fp
	return r
}

func (r *realAIEnqueuer) EnqueueForPhoto(ctx context.Context, mid string) error {
	if err := r.enqueue(ctx, mid, ai.TaskTag, r.tagFP); err != nil {
		return err
	}
	if err := r.enqueue(ctx, mid, ai.TaskCaption, r.captionFP); err != nil {
		return err
	}
	if r.embedFP.ModelID == "" {
		return nil
	}
	return r.enqueue(ctx, mid, ai.TaskEmbed, r.embedFP)
}

func (r *realAIEnqueuer) RecordVideoSkip(ctx context.Context, mid string) error {
	if err := r.skip(ctx, mid, ai.TaskTag, "video"); err != nil {
		return err
	}
	if err := r.skip(ctx, mid, ai.TaskCaption, "video"); err != nil {
		return err
	}
	if r.embedFP.ModelID == "" {
		return nil
	}
	// The embed gap scanner explicitly relies on the importer recording
	// videos as ai_skipped(task='embed') — see internal/ai/gapscanner/
	// scanner.go::ScanEmbed. Without this branch a periodic embed
	// gap-scan would re-enqueue every video forever, only for the
	// worker's preview resolver to bounce them back.
	return r.skip(ctx, mid, ai.TaskEmbed, "video")
}
