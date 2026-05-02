package hybrid_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/search/hybrid"
)

// TestBuildMatchExpr pins the FTS5 MATCH expression construction
// contract: tokenization, FTS5-special stripping, short-token drop,
// phrase-quoting, and the trailing-prefix rule (only the LAST token
// gets `*`, only when q does not end in whitespace).
//
// The cases below mirror the plan's table verbatim. The "all tokens
// drop" case is the load-bearing one: the strip semantics are broader
// than the plan's narrow `"*():^` set — anything that is not a
// Unicode letter/digit (with apostrophe/hyphen kept for words like
// "don't" and "high-res") is removed, so pure-punctuation tokens like
// "!!" and "?" collapse to empty and drop on the >=2-char filter.
func TestBuildMatchExpr(t *testing.T) {
	tests := []struct {
		name   string
		q      string
		want   string
		wantOk bool
	}{
		{"single complete token (trailing space)", "small ", `"small"`, true},
		{"complete + trailing prefix", "small dog", `"small" AND "dog"*`, true},
		{"escape FTS5 chars", `dog "cat"`, `"dog" AND "cat"*`, true},
		{"drop short tokens", "a small dog on", `"small" AND "dog" AND "on"*`, true},
		{"all tokens drop → empty", "!! ?", "", false},
		// `--` and `''` survive stripFTS5Specials (which keeps `-` and
		// `'` as word-internal punctuation) but contain no letter or
		// digit; the post-strip alphanumeric guard drops them so they
		// don't leak into the MATCH expression as `"--"` / `"''"`.
		{"punctuation-only tokens drop", "-- ''", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := require.New(t)
			got, ok := hybrid.BuildMatchExpr(tt.q)
			r.Equal(tt.wantOk, ok)
			r.Equal(tt.want, got)
		})
	}
}
