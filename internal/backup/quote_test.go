package backup

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSqlQuoteLiteral(t *testing.T) {
	cases := []struct{ in, want string }{
		{"foo", "'foo'"},
		{"with space", "'with space'"},
		{"O'Reilly", "'O''Reilly'"},
		{"trailing'", "'trailing'''"},
		{"''", "''''''"}, // two-single-quote input → six (open + 2*2 + close)
		{"unicode/路径", "'unicode/路径'"},
		{`back\slash`, `'back\slash'`}, // backslash is not special to SQLite literals
	}
	for _, c := range cases {
		require.Equal(t, c.want, sqlQuoteLiteral(c.in), "input=%q", c.in)
	}
}
