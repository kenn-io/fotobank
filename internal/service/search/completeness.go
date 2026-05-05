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
// supplied includeHidden predicate. The result is in [0, 1].
//
// The hidden predicate flows identically into both queries — the
// numerator (embedded) and denominator (eligible) must observe the
// same hidden gate or the pill leaks per-context progress. v1's
// invariant (search-design.md §6.6): the request's hidden context is
// the only knob the user sees; the activator's separate
// ackAllowsHidden=false measure is internal and never surfaced through
// this method.
//
// Both counts are owner-scoped: the embedding_generations table is
// global (one active generation across the whole hub), but the
// numerator JOINs through media so the count reflects only the
// caller's mapped rows. A plain read of
// embedding_generations.embedded_count would mix every owner's
// progress together — fine for the activator's promote-on-threshold
// decision (single-principal in v1), wrong for a per-caller pill.
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
//
// The numerator additionally requires a media_embedding_ids row in the
// active generation. Both queries route through the read pool (s.ro);
// the JOIN to media on the embedded query is what keeps the count
// honest across thumb-regen invalidations and hidden-flag flips.
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
	const embeddedSQL = `
SELECT COUNT(*) FROM media_embedding_ids x
  JOIN media m ON m.id = x.media_id
 WHERE x.generation_id = ?
   AND m.owner_hub = ? AND m.owner_user_id = ?
   AND m.thumb_status = 'ready'
   AND (m.hidden_at IS NULL OR ?)
   AND NOT EXISTS (
     SELECT 1 FROM ai_skipped sk WHERE sk.media_id = m.id AND sk.task = 'embed'
   )`

	var eligible, embedded int
	if err := s.ro.QueryRowContext(ctx, eligibleSQL,
		caller.Hub, caller.UserID, includeHidden,
	).Scan(&eligible); err != nil {
		return 0, fmt.Errorf("eligible count: %w", err)
	}
	if eligible == 0 {
		return 0, nil
	}
	if err := s.ro.QueryRowContext(ctx, embeddedSQL,
		active.ID, caller.Hub, caller.UserID, includeHidden,
	).Scan(&embedded); err != nil {
		return 0, fmt.Errorf("embedded count: %w", err)
	}
	// Clamp embedded ≤ eligible. The two QueryRowContext calls each
	// take their own SQLite snapshot, so a concurrent import burst
	// that adds new visible rows AND maps them between the eligible
	// read and the embedded read can leave embedded > eligible
	// transiently (eligible's snapshot froze before the new rows
	// existed; embedded's snapshot sees the new mappings). Without
	// the clamp the returned ratio could exceed 1, breaking the
	// IndexingStatusPill component's "completeness < 1" visibility
	// gate. The drift converges on the next render.
	return float64(min(embedded, eligible)) / float64(eligible), nil
}
