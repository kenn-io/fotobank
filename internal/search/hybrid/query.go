package hybrid

import (
	"strings"
	"unicode"
)

// BuildMatchExpr converts user-typed q into an FTS5 MATCH expression:
//
//  1. Tokenize by whitespace.
//  2. Strip FTS5-meaningful and other punctuation chars from each token
//     (see stripFTS5Specials for the kept-rune set).
//  3. Drop tokens shorter than 2 chars.
//  4. Wrap each token in double quotes (phrase tokens — prevents
//     prefix interpretation by FTS5).
//  5. Apply trailing prefix `*` to the LAST token when q does not end
//     in whitespace (heuristic for "user is still typing this token").
//  6. Join with " AND ".
//
// Returns (expr, true) on success, ("", false) when every token was
// dropped and the query collapses to empty.
//
// User input flows directly into the FTS5 MATCH operand, so the strip
// is intentionally aggressive: anything that is not a Unicode letter
// or digit is removed, except `'` and `-` which are kept so words like
// "don't" and "high-res" tokenize as a single phrase rather than two
// short fragments. This rejects FTS5 column-prefix syntax (`col:term`),
// the near-operator (`^`), parens, double-quotes, the prefix glob
// (`*`), and any other punctuation a user might paste.
func BuildMatchExpr(q string) (string, bool) {
	endsInSpace := q == "" || endsInWhitespace(q)
	tokens := tokenize(q)
	if len(tokens) == 0 {
		return "", false
	}
	pieces := make([]string, len(tokens))
	for i, tok := range tokens {
		quoted := `"` + tok + `"`
		if i == len(tokens)-1 && !endsInSpace {
			quoted += "*"
		}
		pieces[i] = quoted
	}
	return strings.Join(pieces, " AND "), true
}

// endsInWhitespace reports whether q's last rune is whitespace, using
// proper UTF-8 decoding rather than indexing the final byte (which
// breaks for multi-byte runes).
func endsInWhitespace(q string) bool {
	if q == "" {
		return false
	}
	rs := []rune(q)
	return unicode.IsSpace(rs[len(rs)-1])
}

// tokenize splits q by whitespace, strips FTS5-meaningful and other
// punctuation chars from each token, and drops tokens that are
// shorter than 2 characters (measured in runes) or contain no
// letter/digit at all. The post-strip-no-alphanumeric guard catches
// punctuation-only inputs like `'a'` (strips to `'a`) and `--`
// (strips to `--`) that survive the strip but produce nonsensical
// FTS5 phrases — the >=2-rune filter alone would let `'a` and `--`
// through.
func tokenize(q string) []string {
	fields := strings.Fields(q)
	out := make([]string, 0, len(fields))
	for _, raw := range fields {
		clean := stripFTS5Specials(raw)
		if !hasLetterOrDigit(clean) {
			continue
		}
		// Use rune count, not byte count, so multi-byte characters
		// (e.g. accented letters) aren't accidentally treated as
		// "long enough" by virtue of UTF-8 encoding bloat.
		if len([]rune(clean)) < 2 {
			continue
		}
		out = append(out, clean)
	}
	return out
}

// hasLetterOrDigit reports whether s contains at least one Unicode
// letter or digit. Used after stripFTS5Specials to drop tokens whose
// only surviving runes are word-internal punctuation (apostrophes,
// hyphens) — those slip through the >=2-rune filter but yield FTS5
// phrases like `"--"` or `"'a"` that have no useful match semantics.
func hasLetterOrDigit(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

// stripFTS5Specials keeps only Unicode letters, digits, apostrophes,
// and hyphens. The plan's narrow strip (only `"*():^`) leaves
// pure-punctuation tokens like "!!" intact, which then leak into the
// MATCH expression as `"!!"` — surprising to users and brittle against
// future FTS5 syntax extensions. Stripping all non-alphanumeric
// (except `'` and `-` which are word-internal punctuation) collapses
// such tokens to empty so they drop on the >=2-rune filter.
func stripFTS5Specials(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r):
			b.WriteRune(r)
		case r == '\'' || r == '-':
			b.WriteRune(r)
		}
	}
	return b.String()
}
