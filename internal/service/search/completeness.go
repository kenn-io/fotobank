package search

import (
	"context"
	"fmt"

	"github.com/wesm/fotobank/internal/auth/hidden"
	"github.com/wesm/fotobank/internal/errs"
	"github.com/wesm/fotobank/internal/owners"
)

// EmbeddingCompleteness returns the embedded/eligible fraction for the
// caller's library under the active generation, evaluated against the
// supplied includeHidden predicate. The result is clamped to [0, 1].
//
// Read-time vs assertive split. This method is called on every search
// render, so the numerator trusts the cached
// embedding_generations.embedded_count column (maintained incrementally
// by the worker as mappings are inserted). The denominator still scans
// media, since it depends on the request's includeHidden flag — but
// the assertive JOIN through media_embedding_ids that the activator's
// embeddedCount uses for promotion decisions is intentionally not
// repeated here. At 100k rows the JOIN was ~25ms per render; trusting
// the cached counter drops that to a single-column read on the
// generations row.
//
// Tradeoff. The cached counter does not decrement when a previously-
// mapped row is hidden, has its thumb_status flipped to non-ready, or
// gains an ai_skipped(embed) entry. All three drift in the same
// direction (cached >= visible-only assertive), so the pill may show
// "98% indexed" lingering when the assertive number has reached 100%.
// The activator's promote condition (a.embeddedCount, JOIN-derived)
// is the source of truth for the activate-on-threshold decision and
// is unchanged by this split. The pill is rendering progress, not
// gating behavior — a 1-2 percentage-point drift on the way down is
// acceptable. The tradeoff was reviewed in kata#18.
//
// includeHidden=true is gated on a valid UnlockClaim — same posture
// as Search. A nil claim or a checker rejection returns
// errs.ErrPermissionDenied. Defense-in-depth: even if the caller
// already presented a valid claim to Search on the same request,
// EmbeddingCompleteness validates independently so a misuse that
// calls only the completeness path can't leak the hidden tally.
//
// Sentinel returns:
//   - active generation absent → (0, nil). The pill renders 0% in this
//     state; treating it as an error would force every caller to
//     branch on a not-yet-ready library.
//   - eligible == 0 → (0, nil). Empty library or every photo
//     ai_skipped(embed) — divide-by-zero avoided, and the pill correctly
//     shows "0 indexed" rather than NaN.
//
// Eligibility predicate (matches the activator's gap-fill predicate
// minus the per-generation mapping check):
//   - thumb_status='ready' (only encoded media can be embedded)
//   - hidden_at IS NULL OR includeHidden=true (hidden gate)
//   - NOT EXISTS ai_skipped(embed) (videos and unsupported formats)
func (s *Service) EmbeddingCompleteness(ctx context.Context, caller owners.Principal, includeHidden bool, claim *hidden.UnlockClaim) (float64, error) {
	if includeHidden {
		if claim == nil || !s.hiddenChecker.Valid(claim, caller) {
			return 0, errs.ErrPermissionDenied
		}
	}
	active, err := s.gens.FindActive(ctx)
	if err != nil {
		return 0, fmt.Errorf("find active generation: %w", err)
	}
	if active == nil {
		return 0, nil
	}

	const eligibleSQL = `
SELECT COUNT(*) FROM media m
 WHERE m.owner_hub = ? AND m.owner_user_id = ?
   AND m.thumb_status = 'ready'
   AND (m.hidden_at IS NULL OR ?)
   AND NOT EXISTS (
     SELECT 1 FROM ai_skipped sk WHERE sk.media_id = m.id AND sk.task = 'embed'
   )`

	var eligible int
	if err := s.ro.QueryRowContext(ctx, eligibleSQL,
		caller.Hub, caller.UserID, includeHidden,
	).Scan(&eligible); err != nil {
		return 0, fmt.Errorf("eligible count: %w", err)
	}
	if eligible == 0 {
		return 0, nil
	}
	embedded := min(
		// Clamp: cached counter can drift above the visible-only eligible
		// floor (mapping written before a hide). Without the clamp the
		// pill would render >100% and the IndexingStatusPill component's
		// "completeness < 1" visibility gate would fail to hide it on a
		// fully-indexed library.
		active.EmbeddedCount, eligible)
	return float64(embedded) / float64(eligible), nil
}
