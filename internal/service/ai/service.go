// Package ai (under internal/service) is the auth boundary for AI
// operations. Every method takes a caller principal and either scopes
// the action to that principal's library or returns
// errs.ErrAcknowledgementRequired / errs.ErrPermissionDenied.
package ai

import (
	"context"
	"fmt"

	"github.com/wesm/fotobank/internal/ai"
	"github.com/wesm/fotobank/internal/ai/ack"
	"github.com/wesm/fotobank/internal/ai/failures"
	"github.com/wesm/fotobank/internal/ai/gapscanner"
	"github.com/wesm/fotobank/internal/ai/jobs"
	"github.com/wesm/fotobank/internal/ai/results"
	"github.com/wesm/fotobank/internal/ai/skipped"
	"github.com/wesm/fotobank/internal/errs"
	"github.com/wesm/fotobank/internal/owners"
)

// ConfigFingerprints captures the active per-task fingerprints derived
// from the operator's [ai] config + Go-defined prompts at boot. The
// service treats them as immutable for its lifetime; a config change
// requires a process restart.
type ConfigFingerprints struct {
	Tag     ai.Fingerprint
	Caption ai.Fingerprint
}

// Lookup returns the active fingerprint for a task.
func (c ConfigFingerprints) Lookup(t ai.Task) (ai.Fingerprint, bool) {
	switch t {
	case ai.TaskTag:
		return c.Tag, true
	case ai.TaskCaption:
		return c.Caption, true
	}
	return ai.Fingerprint{}, false
}

// Deps bundles the collaborators the service needs.
type Deps struct {
	Queue              *jobs.Queue
	Results            *results.Repo
	Failures           *failures.Repo
	Skipped            *skipped.Repo
	Ack                *ack.Store
	Gap                *gapscanner.Scanner
	ConfigFingerprints ConfigFingerprints
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
	fp, _ := s.deps.ConfigFingerprints.Lookup(task)
	return s.deps.Gap.Scan(ctx, gapscanner.ScanRequest{
		Task:        task,
		Fingerprint: fp,
		Owner:       caller,
		Force:       force,
		Limit:       0,
	})
}

// RetryFailed clears all current-fingerprint failures for task and
// re-enqueues the corresponding media in bounded batches. Returns the
// total count enqueued.
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
	fp, _ := s.deps.ConfigFingerprints.Lookup(task)
	total := 0
	for {
		rows, err := s.deps.Failures.ListForFingerprintByOwner(ctx, task, fp, caller.Hub, caller.UserID, retryBatchSize)
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
		if _, err := s.deps.Failures.DeleteByMediaIDs(ctx, task, fp, mediaIDs); err != nil {
			return total, fmt.Errorf("delete failures: %w", err)
		}
		n, err := s.deps.Gap.Scan(ctx, gapscanner.ScanRequest{
			Task: task, Fingerprint: fp, Owner: caller, Force: true, MediaIDs: mediaIDs,
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
	fp, _ := s.deps.ConfigFingerprints.Lookup(task)
	// Owner-scoped scan verifies that mediaID belongs to caller. If not,
	// it returns errs.ErrNotFound and we never touch the failure row.
	if _, err := s.deps.Gap.Scan(ctx, gapscanner.ScanRequest{
		Task: task, Fingerprint: fp, Owner: caller, Force: true, MediaIDs: []string{mediaID},
	}); err != nil {
		return fmt.Errorf("scan: %w", err)
	}
	if err := s.deps.Failures.Delete(ctx, mediaID, task, fp); err != nil {
		return fmt.Errorf("delete failure: %w", err)
	}
	return nil
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
	fp, _ := s.deps.ConfigFingerprints.Lookup(task)
	return s.deps.Failures.ListForFingerprintByOwner(ctx, task, fp, caller.Hub, caller.UserID, limit)
}
