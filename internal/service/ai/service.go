// Package ai (under internal/service) is the auth boundary for AI
// operations. Every method takes a caller principal and either scopes
// the action to that principal's library or returns
// errs.ErrAcknowledgementRequired / errs.ErrPermissionDenied.
package ai

import (
	"context"
	"fmt"
	"time"

	"go.kenn.io/fotobank/internal/ai"
	"go.kenn.io/fotobank/internal/ai/ack"
	"go.kenn.io/fotobank/internal/ai/failures"
	"go.kenn.io/fotobank/internal/ai/gapscanner"
	"go.kenn.io/fotobank/internal/ai/jobs"
	"go.kenn.io/fotobank/internal/ai/results"
	airuntime "go.kenn.io/fotobank/internal/ai/runtime"
	"go.kenn.io/fotobank/internal/ai/skipped"
	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/owners"
)

// ConfigFingerprints captures the active per-task fingerprints derived
// from the operator's [ai] config + Go-defined prompts at boot. The
// service treats them as immutable for its lifetime; a config change
// requires a process restart.
//
// Embed is the embed task's fingerprint — model + input profile, no
// prompt version (embeddings have no prompt). Surfaced on the health
// payload's EmbedTaskPart so the panel can display the active embed
// model alongside tag/caption.
type ConfigFingerprints struct {
	Tag     ai.Fingerprint
	Caption ai.Fingerprint
	Embed   ai.Fingerprint
}

// RuntimeProvider publishes the live effective AI config. When present,
// service methods use its claim fingerprints for queue writes and its
// result fingerprints for provenance/failure lookups.
type RuntimeProvider interface {
	Effective() airuntime.Snapshot
}

// Lookup returns the active fingerprint for a task.
func (c ConfigFingerprints) Lookup(t ai.Task) (ai.Fingerprint, bool) {
	switch t {
	case ai.TaskTag:
		return c.Tag, true
	case ai.TaskCaption:
		return c.Caption, true
	case ai.TaskEmbed:
		return c.Embed, true
	}
	return ai.Fingerprint{}, false
}

// MediaCheck is the visibility gate the lightbox AI surface uses before
// returning per-photo artifacts. Implementations return errs.ErrNotFound
// (anti-enumeration) when caller does not own mediaID, when the row
// does not exist, or when the row is hidden and includeHidden is false.
// MediaService satisfies this interface (its Get method has matching
// semantics). Decoupling via interface keeps the AI service from
// importing the internal/service package directly.
type MediaCheck interface {
	Check(ctx context.Context, mediaID string, caller owners.Principal, includeHidden bool) error
}

// Deps bundles the collaborators the service needs.
//
// EmbeddingActivator and EmbeddingGenerations are optional — they are
// nil when the embed surface is not wired (e.g. in tests that exercise
// only tag/caption flows, or in deployments where ai.embed.enabled is
// false at boot). The health aggregator degrades gracefully under nil
// (empty generations list, no eligible recount).
type Deps struct {
	Queue                *jobs.Queue
	Results              *results.Repo
	Failures             *failures.Repo
	Skipped              *skipped.Repo
	Ack                  *ack.Store
	Gap                  *gapscanner.Scanner
	Media                MediaCheck
	ConfigFingerprints   ConfigFingerprints
	EmbeddingActivator   EmbeddingActivatorIface
	EmbeddingGenerations EmbeddingGenerationsLister
	// EmbeddingProbe returns a shared observation with its actual check time.
	EmbeddingProbe func(context.Context) VisionPart
	Runtime        RuntimeProvider
}

// Service is the auth-scoped AI service.
type Service struct {
	deps Deps
}

// New constructs a Service from its collaborators.
func New(deps Deps) *Service { return &Service{deps: deps} }

// retryBatchSize bounds how many failure rows the retry-failed loop
// pulls (and re-enqueues) per pass. Keeps a single HTTP/CLI request
// from materializing an unbounded set when the caller has many
// failures.
const retryBatchSize = 500

// requireScopedCaller rejects a zero principal at the auth boundary.
// Owner-scoped repo queries treat zero (hub, userID) as "no row matches",
// but a zero caller can also be interpreted as "unscoped" by codepaths
// that branch on emptiness — disallow it explicitly.
func requireScopedCaller(p owners.Principal) error {
	if p.IsZero() {
		return fmt.Errorf("%w: caller principal required", errs.ErrPermissionDenied)
	}
	return nil
}

type taskFingerprints struct {
	claim  string
	result ai.Fingerprint
}

func (s *Service) taskFingerprints(task ai.Task) (taskFingerprints, bool) {
	if s.deps.Runtime != nil {
		snap := s.deps.Runtime.Effective()
		switch task {
		case ai.TaskTag:
			return taskFingerprints{claim: snap.Claim.Tag, result: snap.Result.Tag}, true
		case ai.TaskCaption:
			return taskFingerprints{claim: snap.Claim.Caption, result: snap.Result.Caption}, true
		case ai.TaskEmbed:
			return taskFingerprints{claim: snap.Claim.Embed, result: snap.Result.Embed}, true
		}
	}
	fp, ok := s.deps.ConfigFingerprints.Lookup(task)
	if !ok {
		return taskFingerprints{}, false
	}
	return taskFingerprints{claim: fp.String(), result: fp}, true
}

// IsAcknowledged returns whether p has acknowledged hidden-photo processing.
func (s *Service) IsAcknowledged(ctx context.Context, p owners.Principal) (bool, error) {
	if err := requireScopedCaller(p); err != nil {
		return false, err
	}
	return s.deps.Ack.IsAcknowledged(ctx, p)
}

// Acknowledge records the ack for p.
func (s *Service) Acknowledge(ctx context.Context, p owners.Principal) error {
	if err := requireScopedCaller(p); err != nil {
		return err
	}
	return s.deps.Ack.Acknowledge(ctx, p)
}

// Backfill enqueues missing-fingerprint jobs for caller-owned media.
// Returns the count enqueued. Requires acknowledgement.
func (s *Service) Backfill(ctx context.Context, caller owners.Principal, task ai.Task, force bool) (int, error) {
	if err := requireScopedCaller(caller); err != nil {
		return 0, err
	}
	if !task.Valid() {
		return 0, fmt.Errorf("%w: invalid task", errs.ErrInvalidArgument)
	}
	ok, err := s.deps.Ack.IsAcknowledged(ctx, caller)
	if err != nil {
		return 0, fmt.Errorf("ack lookup: %w", err)
	}
	if !ok {
		return 0, errs.ErrAcknowledgementRequired
	}
	fp, _ := s.taskFingerprints(task)
	return s.deps.Gap.Scan(ctx, gapscanner.ScanRequest{
		Task:              task,
		ClaimFingerprint:  fp.claim,
		ResultFingerprint: fp.result,
		Owner:             caller,
		Force:             force,
		Limit:             0,
	})
}

// RetryFailed clears the current-fingerprint failures that existed at
// the moment of the call and re-enqueues the corresponding media in
// bounded batches. Returns the total count enqueued. The cutoff is
// captured once at entry so newly recorded failures (e.g. from a
// concurrent worker re-failing a freshly enqueued retry) are NOT
// chased into a subsequent batch — that would let one
// `retry-failed` request loop indefinitely on the same media.
func (s *Service) RetryFailed(ctx context.Context, caller owners.Principal, task ai.Task) (int, error) {
	if err := requireScopedCaller(caller); err != nil {
		return 0, err
	}
	if !task.Valid() {
		return 0, fmt.Errorf("%w: invalid task", errs.ErrInvalidArgument)
	}
	ok, err := s.deps.Ack.IsAcknowledged(ctx, caller)
	if err != nil {
		return 0, fmt.Errorf("ack lookup: %w", err)
	}
	if !ok {
		return 0, errs.ErrAcknowledgementRequired
	}
	fp, _ := s.taskFingerprints(task)
	cutoff := time.Now().UTC()
	total := 0
	for {
		rows, err := s.deps.Failures.ListForFingerprintByOwner(
			ctx, task, fp.result, caller.Hub, caller.UserID, cutoff, retryBatchSize)
		if err != nil {
			return total, fmt.Errorf("list failures: %w", err)
		}
		if len(rows) == 0 {
			return total, nil
		}
		mediaIDs := make([]string, 0, len(rows))
		for _, r := range rows {
			mediaIDs = append(mediaIDs, r.MediaID)
		}
		if _, err := s.deps.Failures.DeleteByMediaIDs(ctx, task, fp.result, mediaIDs); err != nil {
			return total, fmt.Errorf("delete failures: %w", err)
		}
		n, err := s.deps.Gap.Scan(ctx, gapscanner.ScanRequest{
			Task:              task,
			ClaimFingerprint:  fp.claim,
			ResultFingerprint: fp.result,
			Owner:             caller,
			Force:             true,
			MediaIDs:          mediaIDs,
		})
		total += n
		if err != nil {
			return total, fmt.Errorf("scan: %w", err)
		}
		if len(rows) < retryBatchSize {
			return total, nil
		}
	}
}

// RetryPhoto enqueues a single (media, task) job and clears its
// failure row. Lightbox per-photo Retry button. Returns errs.ErrNotFound
// (wrapped) if mediaID does not belong to caller.
func (s *Service) RetryPhoto(ctx context.Context, caller owners.Principal, mediaID string, task ai.Task) error {
	if err := requireScopedCaller(caller); err != nil {
		return err
	}
	if !task.Valid() {
		return fmt.Errorf("%w: invalid task", errs.ErrInvalidArgument)
	}
	ok, err := s.deps.Ack.IsAcknowledged(ctx, caller)
	if err != nil {
		return fmt.Errorf("ack lookup: %w", err)
	}
	if !ok {
		return errs.ErrAcknowledgementRequired
	}
	fp, _ := s.taskFingerprints(task)
	// Owner-scoped scan verifies that mediaID belongs to caller. If not,
	// it returns errs.ErrNotFound and we never touch the failure row.
	if _, err := s.deps.Gap.Scan(ctx, gapscanner.ScanRequest{
		Task:              task,
		ClaimFingerprint:  fp.claim,
		ResultFingerprint: fp.result,
		Owner:             caller,
		Force:             true,
		MediaIDs:          []string{mediaID},
	}); err != nil {
		return fmt.Errorf("scan: %w", err)
	}
	if err := s.deps.Failures.Delete(ctx, mediaID, task, fp.result); err != nil {
		return fmt.Errorf("delete failure: %w", err)
	}
	return nil
}

// MediaView is the lightbox-facing artifact bundle for one media: the
// active tag list, the active caption, an optional skip reason, and any
// current-fingerprint failure rows for tag/caption.
type MediaView struct {
	Tags           []TagItem     `json:"tags,omitempty"`
	Caption        *CaptionItem  `json:"caption,omitempty"`
	Skipped        *SkippedItem  `json:"skipped,omitempty"`
	TagFailure     *MediaFailure `json:"tag_failure,omitempty"`
	CaptionFailure *MediaFailure `json:"caption_failure,omitempty"`
}

// TagItem is one tag in MediaView.Tags.
type TagItem struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Rank  int    `json:"rank"`
}

// CaptionItem is the active caption with its provenance fields.
type CaptionItem struct {
	Text          string    `json:"text"`
	ModelID       string    `json:"model_id"`
	PromptVersion string    `json:"prompt_version"`
	GeneratedAt   time.Time `json:"generated_at"`
}

// SkippedItem captures why a media is excluded from AI processing.
type SkippedItem struct {
	Reason string `json:"reason"`
}

// MediaFailure is the per-photo failure detail surfaced inline in the
// lightbox so the user can retry without leaving the photo.
type MediaFailure struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
}

// MediaView returns the AI artifacts for one media. Caller must be
// non-zero AND own the row; hidden rows are gated behind includeHidden
// (set by the HTTP handler when a valid hidden-unlock cookie is
// present). Failures of either check map to errs.ErrNotFound so the
// surface cannot be used to probe for cross-owner media or to
// enumerate hidden rows without an unlock.
func (s *Service) MediaView(ctx context.Context, caller owners.Principal, mediaID string, includeHidden bool) (MediaView, error) {
	if err := requireScopedCaller(caller); err != nil {
		return MediaView{}, err
	}
	if s.deps.Media == nil {
		return MediaView{}, fmt.Errorf("media gate not configured")
	}
	if err := s.deps.Media.Check(ctx, mediaID, caller, includeHidden); err != nil {
		return MediaView{}, err
	}
	out := MediaView{}
	tags, err := s.deps.Results.GetActiveTags(ctx, mediaID)
	if err != nil {
		return MediaView{}, fmt.Errorf("tags: %w", err)
	}
	for _, t := range tags {
		out.Tags = append(out.Tags, TagItem{Key: t.Key, Label: t.Label, Rank: t.Rank})
	}
	caption, found, err := s.deps.Results.GetActiveCaption(ctx, mediaID)
	if err != nil {
		return MediaView{}, fmt.Errorf("caption: %w", err)
	}
	if found {
		out.Caption = &CaptionItem{
			Text:          caption.Text,
			ModelID:       caption.ModelID,
			PromptVersion: caption.PromptVersion,
			GeneratedAt:   caption.GeneratedAt,
		}
	}
	if reason, found, err := s.deps.Skipped.Get(ctx, mediaID, ai.TaskTag); err != nil {
		return MediaView{}, fmt.Errorf("skipped: %w", err)
	} else if found {
		out.Skipped = &SkippedItem{Reason: reason}
	}
	tagFP, _ := s.taskFingerprints(ai.TaskTag)
	if r, found, err := s.deps.Failures.GetForFingerprint(
		ctx, mediaID, ai.TaskTag, tagFP.result); err != nil {
		return MediaView{}, fmt.Errorf("tag failure: %w", err)
	} else if found {
		out.TagFailure = &MediaFailure{Kind: string(r.LastErrorKind), Message: r.LastError}
	}
	captionFP, _ := s.taskFingerprints(ai.TaskCaption)
	if r, found, err := s.deps.Failures.GetForFingerprint(
		ctx, mediaID, ai.TaskCaption, captionFP.result); err != nil {
		return MediaView{}, fmt.Errorf("caption failure: %w", err)
	} else if found {
		out.CaptionFailure = &MediaFailure{Kind: string(r.LastErrorKind), Message: r.LastError}
	}
	return out, nil
}

// ListFailures returns recent failures for the active fingerprint,
// scoped to caller-owned media so failure metadata never leaks across
// principals.
func (s *Service) ListFailures(ctx context.Context, caller owners.Principal, task ai.Task, limit int) ([]failures.Row, error) {
	if err := requireScopedCaller(caller); err != nil {
		return nil, err
	}
	if !task.Valid() {
		return nil, fmt.Errorf("%w: invalid task", errs.ErrInvalidArgument)
	}
	fp, _ := s.taskFingerprints(task)
	// Panel reads pass a zero cutoff so the latest failures (including
	// any that landed after the request started) are visible.
	return s.deps.Failures.ListForFingerprintByOwner(
		ctx, task, fp.result, caller.Hub, caller.UserID, time.Time{}, limit)
}
