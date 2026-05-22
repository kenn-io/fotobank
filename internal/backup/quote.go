package backup

import "strings"

// sqlQuoteLiteral wraps s as a SQLite string literal: doubles any
// internal single quotes and surrounds the result with single quotes.
// VACUUM INTO requires its destination as a literal in the SQL text;
// driver parameter binding is not supported for that statement, so we
// quote correctly to protect against malformed paths.
func sqlQuoteLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
