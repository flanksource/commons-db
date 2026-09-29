// Match patterns: the collections.MatchItem reading of a value a match filter
// selects by, for a backend to compile into its own comparison.
package query

import "strings"

// MatchMode is how a match pattern compares a value.
type MatchMode string

const (
	MatchAny      MatchMode = "any"
	MatchExact    MatchMode = "exact"
	MatchPrefix   MatchMode = "prefix"
	MatchSuffix   MatchMode = "suffix"
	MatchContains MatchMode = "contains"
)

// MatchPattern is a pattern read: its mode and the text it compares, which a
// backend compares case-insensitively.
type MatchPattern struct {
	Mode MatchMode
	Text string
}

// ParseMatchPattern reads pattern as collections.MatchItem does: "*" alone
// matches anything, a "*" at the end, the start or both makes a prefix, a
// suffix or a substring of the rest, and any other value — a "*" inside it
// included — matches whole. The pattern is taken as written: MatchItem also
// URL-decodes its patterns, which a filter value, already decoded from its
// request, must not be — "50%" is a literal percent, not a bad escape.
func ParseMatchPattern(pattern string) MatchPattern {
	leading, trailing := strings.HasPrefix(pattern, "*"), strings.HasSuffix(pattern, "*")
	switch {
	case pattern == "*":
		return MatchPattern{Mode: MatchAny}
	case leading && trailing:
		return MatchPattern{Mode: MatchContains, Text: pattern[1 : len(pattern)-1]}
	case leading:
		return MatchPattern{Mode: MatchSuffix, Text: pattern[1:]}
	case trailing:
		return MatchPattern{Mode: MatchPrefix, Text: pattern[:len(pattern)-1]}
	default:
		return MatchPattern{Mode: MatchExact, Text: pattern}
	}
}

// selectsValues reports a kind that selects among a column's values — terms
// exactly, match by pattern — and so may carry options and a lookup limit.
func (k ColumnFilterKind) selectsValues() bool {
	return k == ColumnFilterKindTerms || k == ColumnFilterKindMatch
}
