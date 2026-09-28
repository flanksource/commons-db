package xetrace

import (
	"fmt"
	"net/url"
	"strings"
)

// matchPattern is one clicky MultiFilter value, normalized the way
// collections.MatchItems normalizes it: split from its comma list, trimmed and
// URL-unescaped, with a leading `!` marking an exclusion.
type matchPattern struct {
	raw    string
	negate bool
	value  string
}

// parseMatchPatterns normalizes MultiFilter values and refuses any value SQL
// Server could not evaluate the way collections.MatchItems does. Blank entries
// are dropped, so `--host a,,b` and an empty flag are harmless.
func parseMatchPatterns(values []string) ([]matchPattern, error) {
	var out []matchPattern
	for _, v := range values {
		for _, part := range strings.Split(v, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			decoded, err := url.QueryUnescape(part)
			if err != nil {
				return nil, fmt.Errorf("pattern %q: %w", part, err)
			}
			p := matchPattern{raw: decoded, negate: strings.HasPrefix(decoded, "!"), value: strings.TrimPrefix(decoded, "!")}
			if err := p.validate(); err != nil {
				return nil, err
			}
			out = append(out, p)
		}
	}
	return out, nil
}

// ValidateMatchPatterns refuses a MultiFilter value the XE session predicate
// cannot honour exactly as collections.MatchItems would: a `*` anywhere but the
// start or end (MatchItems reads it literally, a LIKE would not), an exclusion
// naming nothing, an exclusion of everything, or a malformed URL escape.
func ValidateMatchPatterns(values []string) error {
	_, err := parseMatchPatterns(values)
	return err
}

func (p matchPattern) validate() error {
	switch {
	case p.negate && p.value == "":
		return fmt.Errorf("pattern %q: excludes no value", p.raw)
	case p.negate && p.value == "*":
		return fmt.Errorf("pattern %q: excludes every value, so nothing would be captured", p.raw)
	case p.value == "*":
		return nil
	}
	if strings.Contains(strings.TrimSuffix(strings.TrimPrefix(p.value, "*"), "*"), "*") {
		return fmt.Errorf("pattern %q: * is a wildcard only at the start or end of a value", p.raw)
	}
	return nil
}

func (p matchPattern) wildcard() bool {
	return strings.HasPrefix(p.value, "*") || strings.HasSuffix(p.value, "*")
}

// matches reports whether item satisfies the pattern's value (ignoring its
// negation), with collections.MatchItems' case-insensitive prefix/suffix rules.
func (p matchPattern) matches(item string) bool {
	item, value := strings.ToLower(item), strings.ToLower(p.value)
	if value == "*" || item == value {
		return true
	}
	lead, trail := strings.HasPrefix(value, "*"), strings.HasSuffix(value, "*")
	core := strings.TrimSuffix(strings.TrimPrefix(value, "*"), "*")
	switch {
	case lead && trail:
		return strings.Contains(item, core)
	case lead:
		return strings.HasSuffix(item, core)
	case trail:
		return strings.HasPrefix(item, core)
	}
	return false
}

// comparator renders the pattern's value (ignoring its negation) as an XE
// predicate on field: case-insensitive equality for an exact value, or a
// case-insensitive LIKE for a prefix/suffix wildcard, whose literal part has
// its LIKE metacharacters bracket-escaped.
func (p matchPattern) comparator(field string) string {
	if !p.wildcard() {
		return fmt.Sprintf("sqlserver.equal_i_sql_unicode_string(%s, N'%s')", field, escapeSQLStringLiteral(p.value))
	}
	like := escapeLikeLiteral(strings.TrimSuffix(strings.TrimPrefix(p.value, "*"), "*"))
	if strings.HasPrefix(p.value, "*") {
		like = "%" + like
	}
	if strings.HasSuffix(p.value, "*") {
		like += "%"
	}
	return fmt.Sprintf("sqlserver.like_i_sql_unicode_string(%s, N'%s')", field, escapeSQLStringLiteral(like))
}

// escapeLikeLiteral makes every T-SQL LIKE metacharacter in s literal. `[`
// goes first so the brackets the other two gain are not escaped again.
func escapeLikeLiteral(s string) string {
	return strings.NewReplacer("[", "[[]", "%", "[%]", "_", "[_]").Replace(s)
}

// buildMatchPredicate translates MultiFilter values for one XE field into a
// CREATE EVENT SESSION WHERE fragment that keeps exactly what
// collections.MatchItems keeps: positives are OR'd, exclusions are AND'd and
// win, and an exclusion-only set matches everything not excluded. Returns ""
// when nothing constrains the field (no values, or a positive `*` with no
// exclusions).
func buildMatchPredicate(field string, values []string) (string, error) {
	patterns, err := parseMatchPatterns(values)
	if err != nil {
		return "", err
	}
	var positives, negatives []string
	matchAll := false
	for _, p := range patterns {
		switch {
		case p.negate:
			negatives = append(negatives, "NOT ("+p.comparator(field)+")")
		case p.value == "*":
			matchAll = true
		default:
			positives = append(positives, p.comparator(field))
		}
	}
	var groups []string
	if len(positives) > 0 && !matchAll {
		groups = append(groups, "("+strings.Join(positives, " OR ")+")")
	}
	if len(negatives) > 0 {
		groups = append(groups, "("+strings.Join(negatives, " AND ")+")")
	}
	return strings.Join(groups, " AND "), nil
}
