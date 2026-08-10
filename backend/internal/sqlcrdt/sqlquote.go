package sqlcrdt

import "strings"

// sqlQuote renders a Go string as a SQL string literal (single-quoted, inner
// quotes doubled). Used only for ATTACH, whose filename cannot be a bind
// parameter.
func sqlQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// sqlIdent renders an identifier as a double-quoted SQL identifier. Used for
// schema, table and column names, which likewise cannot be bind parameters.
//
// Every identifier this package interpolates comes from either a caller-supplied
// allow-list or SQLite's own PRAGMA table_info output, but quoting is applied
// unconditionally so a table named `order` or one containing a quote can never
// change the shape of a generated statement.
func sqlIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}
