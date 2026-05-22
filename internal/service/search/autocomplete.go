package search

import (
	"context"
	"fmt"
	"strings"

	"go.kenn.io/fotobank/internal/auth/hidden"
	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/owners"
)

// TagSuggestion is one row of the AutocompleteTags result. Key is the
// canonical tag_key (the bind value the engine's filter CTE matches
// on), Label is the display string, Count is the number of media in
// the caller's library that carry the tag (i.e. the chip's expected
// hit count). Caller wires Count into the chip popover so users can
// see how many photos a chip would narrow to before they commit.
type TagSuggestion struct {
	Key   string
	Label string
	Count int
}

// LocationSuggestion is one row of the AutocompleteLocations result.
// Locations don't have a canonical key — the label is what the engine's
// filter CTE matches on with exact equality — so the wire shape is
// (label, count) only.
type LocationSuggestion struct {
	Label string
	Count int
}

// autocompleteMaxLimit is the upper bound enforced inside the service
// so a forgetful caller can't ask the DB for thousands of rows. The
// HTTP route enforces a smaller cap of its own; this is a defense in
// depth against a future caller that bypasses huma binding.
const autocompleteMaxLimit = 100

// AutocompleteTags returns up to limit tag suggestions whose label
// starts with prefix (LIKE prefix%). Owner-scoped: only tags attached
// to media in the caller's library are surfaced. The hidden gate
// mirrors Search/EmbeddingCompleteness — includeHidden=true requires a
// valid UnlockClaim, and a nil claim or a checker rejection returns
// errs.ErrPermissionDenied.
//
// The query GROUPs by tag_key (not tag_label) because the same
// label-cased input can canonicalise to one key — choosing one
// representative label per key is the right de-duplication for the
// chip surface. ORDER BY count desc puts the most-used tag first so
// the user's likely choice is at the top.
func (s *Service) AutocompleteTags(
	ctx context.Context,
	caller owners.Principal,
	prefix string,
	limit int,
	includeHidden bool,
	claim *hidden.UnlockClaim,
) ([]TagSuggestion, error) {
	if includeHidden {
		if claim == nil || !s.hiddenChecker.Valid(claim, caller) {
			return nil, errs.ErrPermissionDenied
		}
	}
	limit = clampAutocompleteLimit(limit)

	// LIKE pattern: escape user input and append a single trailing %
	// so the match is a prefix match. The ESCAPE clause matches the
	// helper's escape character — both must agree or the LIKE silently
	// reverts to "no escape" semantics.
	pattern := likeEscape(prefix) + "%"

	const q = `
SELECT mt.tag_key, mt.tag_label, COUNT(*) AS cnt
FROM media_tags mt
JOIN ai_results r ON mt.result_id = r.id
JOIN media m ON m.id = r.media_id
WHERE r.task = 'tag' AND r.status = 'active'
  AND m.owner_hub = ? AND m.owner_user_id = ?
  AND (m.hidden_at IS NULL OR ?)
  AND mt.tag_label LIKE ? ESCAPE '\'
GROUP BY mt.tag_key
ORDER BY cnt DESC, mt.tag_label ASC
LIMIT ?`

	rows, err := s.ro.QueryContext(ctx, q,
		caller.Hub, caller.UserID, includeHidden, pattern, limit)
	if err != nil {
		return nil, fmt.Errorf("autocomplete tags: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []TagSuggestion
	for rows.Next() {
		var s TagSuggestion
		if err := rows.Scan(&s.Key, &s.Label, &s.Count); err != nil {
			return nil, fmt.Errorf("scan tag suggestion: %w", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("tag suggestions rows: %w", err)
	}
	return out, nil
}

// AutocompleteLocations returns up to limit location-label suggestions
// whose label contains substring (LIKE %substring%). The substring
// match — versus the prefix match for tags — is intentional: location
// strings are typically structured as "City, Region, Country", and
// users frequently type a country name expecting matches across many
// (city, country) tuples. Owner-scoped + hidden-gated identically to
// AutocompleteTags.
func (s *Service) AutocompleteLocations(
	ctx context.Context,
	caller owners.Principal,
	substring string,
	limit int,
	includeHidden bool,
	claim *hidden.UnlockClaim,
) ([]LocationSuggestion, error) {
	if includeHidden {
		if claim == nil || !s.hiddenChecker.Valid(claim, caller) {
			return nil, errs.ErrPermissionDenied
		}
	}
	limit = clampAutocompleteLimit(limit)

	// Substring pattern: %escaped%.
	pattern := "%" + likeEscape(substring) + "%"

	const q = `
SELECT m.location_label, COUNT(*) AS cnt
FROM media m
WHERE m.owner_hub = ? AND m.owner_user_id = ?
  AND (m.hidden_at IS NULL OR ?)
  AND m.location_label IS NOT NULL
  AND m.location_label LIKE ? ESCAPE '\'
GROUP BY m.location_label
ORDER BY cnt DESC, m.location_label ASC
LIMIT ?`

	rows, err := s.ro.QueryContext(ctx, q,
		caller.Hub, caller.UserID, includeHidden, pattern, limit)
	if err != nil {
		return nil, fmt.Errorf("autocomplete locations: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []LocationSuggestion
	for rows.Next() {
		var s LocationSuggestion
		if err := rows.Scan(&s.Label, &s.Count); err != nil {
			return nil, fmt.Errorf("scan location suggestion: %w", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("location suggestions rows: %w", err)
	}
	return out, nil
}

// clampAutocompleteLimit normalises the caller-supplied limit so the
// query always asks the DB for at least 1 row and at most
// autocompleteMaxLimit. limit <= 0 falls back to a default of 10
// (matches the SPA's chip popover height); limit > max clamps down.
func clampAutocompleteLimit(limit int) int {
	if limit <= 0 {
		return 10
	}
	if limit > autocompleteMaxLimit {
		return autocompleteMaxLimit
	}
	return limit
}

// likeEscape doubles every LIKE-special character in s with a leading
// backslash so the resulting pattern, when used with `LIKE ? ESCAPE
// '\'`, treats user input as a literal string. The three special
// characters are:
//
//   - %  matches zero or more characters
//   - _  matches exactly one character
//   - \  is the escape character (per ESCAPE '\') and so must itself
//     be escaped
//
// The order matters: we use a single switch over each rune so the
// escape backslash injected for % and _ is not itself re-escaped. A
// naive two-pass approach (escape % and _ first, then escape \) would
// double-escape the injected backslashes and yield a pattern that
// matches "%X" literally where the user typed plain "X". Using one
// pass keeps the invariant local.
func likeEscape(s string) string {
	if s == "" {
		return ""
	}
	if !strings.ContainsAny(s, `%_\`) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 4)
	for _, r := range s {
		switch r {
		case '%', '_', '\\':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}
