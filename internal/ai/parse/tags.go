// Package parse holds defensive parsers for VLM responses. Workers
// must not assume structured-output mode; every field is parsed
// permissively and validated post-hoc.
package parse

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// ErrMalformed is returned when the VLM response can't be parsed into
// the expected JSON shape, even after permissive cleanup. Workers
// classify this as LastErrorKind=malformed.
var ErrMalformed = errors.New("parse: malformed VLM output")

// Tag is one normalized tag from a VLM response.
type Tag struct {
	Key   string
	Label string
	Rank  int
}

// MaxTags caps how many tags are persisted per result.
const MaxTags = 10

type tagsEnvelope struct {
	// Pointer so we can distinguish "field missing or null" from "empty list".
	Tags *[]string `json:"tags"`
}

var fencedJSON = regexp.MustCompile("(?s)^\\s*```(?:json)?\\s*(.*?)\\s*```\\s*$")

// Tags extracts the tag list from a VLM response and applies the
// normalization pipeline (NFKC → lowercase → strip leading/trailing
// punct → drop empties → dedupe by Key, first-seen wins → cap at MaxTags).
func Tags(raw string) ([]Tag, error) {
	body := strings.TrimSpace(raw)
	if m := fencedJSON.FindStringSubmatch(body); m != nil {
		body = m[1]
	}
	var env tagsEnvelope
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	if env.Tags == nil {
		return nil, fmt.Errorf("%w: missing or null \"tags\" field", ErrMalformed)
	}
	out := make([]Tag, 0, MaxTags)
	seen := map[string]int{} // key -> index in out
	for _, raw := range *env.Tags {
		key := normalizeKey(raw)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		out = append(out, Tag{Key: key, Label: raw, Rank: len(out) + 1})
		seen[key] = len(out) - 1
		if len(out) == MaxTags {
			break
		}
	}
	return out, nil
}

// normalizeKey applies the spec's tag normalization pipeline:
//  1. NFKC.
//  2. Trim leading/trailing whitespace.
//  3. Lowercase.
//  4. Collapse internal whitespace runs to single space.
//  5. Strip leading/trailing punctuation (non-alphanumeric, non-hyphen, non-apostrophe).
//  6. Remove emoji, emoji sequence joiners/variation selectors, and control chars.
//  7. Reject empty post-normalization.
//
// Internal punctuation (hyphens, apostrophes, etc.) is preserved.
func normalizeKey(s string) string {
	s = norm.NFKC.String(s)
	s = strings.TrimSpace(s)
	s = strings.ToLower(s)

	// Build with a single pass: collapse whitespace, drop control chars
	// and non-emoji symbols, allow letters/digits/hyphen/apostrophe/space.
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := false
	for _, r := range s {
		switch {
		case unicode.IsControl(r):
			continue
		case unicode.Is(unicode.Cf, r): // format chars: ZWJ, BOM, etc.
			continue
		case isVariationSelector(r):
			continue
		case unicode.Is(unicode.S, r): // symbols, including emoji and skin-tone modifiers
			continue
		case unicode.IsSpace(r):
			if !prevSpace && b.Len() > 0 {
				b.WriteRune(' ')
			}
			prevSpace = true
		default:
			b.WriteRune(r)
			prevSpace = false
		}
	}
	out := b.String()

	// Strip leading/trailing punctuation but preserve internal hyphens/apostrophes.
	out = strings.TrimFunc(out, func(r rune) bool {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return false
		}
		return true
	})
	return out
}

// isVariationSelector reports whether r is a Unicode variation selector
// (used to qualify emoji presentation, e.g. U+FE0F). These are category
// Mn but functionally part of an emoji sequence, so we strip them
// alongside Cf joiners.
func isVariationSelector(r rune) bool {
	switch {
	case r >= 0xFE00 && r <= 0xFE0F:
		return true
	case r >= 0xE0100 && r <= 0xE01EF:
		return true
	case r >= 0x180B && r <= 0x180D:
		return true
	}
	return false
}
