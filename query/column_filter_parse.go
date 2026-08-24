package query

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/timberio/go-datemath"
)

// rangeOperators are matched longest-first so ">=" is never read as ">".
var rangeOperators = []struct {
	prefix    string
	lower     bool
	inclusive bool
}{
	{prefix: ">=", lower: true, inclusive: true},
	{prefix: "<=", lower: false, inclusive: true},
	{prefix: ">", lower: true, inclusive: false},
	{prefix: "<", lower: false, inclusive: false},
}

// parseColumnFilterSelection decodes one request value under the grammar its
// binding's kind declares.
//
// The kind is the column's, never the value's: a string column's ">=10" is the
// literal ">=10", and only a range column reads it as a bound. That is what
// lets one wire form — a comma-separated list of prefixed tokens — stay
// unambiguous across every kind.
func parseColumnFilterSelection(kind ColumnFilterKind, value any) (ColumnFilterValue, error) {
	tokens, err := columnFilterTokens(value)
	if err != nil {
		return ColumnFilterValue{}, err
	}
	resolved := ColumnFilterValue{Kind: kind.Normalized()}
	switch resolved.Kind {
	case ColumnFilterKindTerms, ColumnFilterKindText:
		resolved.Include, resolved.Exclude, err = parseTermTokens(tokens)
	case ColumnFilterKindRange:
		resolved.Range, err = parseRangeTokens(tokens, parseNumericBound)
	case ColumnFilterKindTime:
		resolved.Range, err = parseRangeTokens(tokens, parseTimeBound)
	case ColumnFilterKindBoolean:
		resolved.Bool, err = parseBooleanTokens(tokens)
	case ColumnFilterKindNone:
		return ColumnFilterValue{}, fmt.Errorf("column offers no filter")
	default:
		return ColumnFilterValue{}, fmt.Errorf("filter kind %q is not supported", kind)
	}
	if err != nil {
		return ColumnFilterValue{}, err
	}
	return resolved, nil
}

// columnFilterTokens flattens a request value into its comma-separated items.
// A repeated query-string key arrives as a list and a single one as a string;
// both mean the same selection.
func columnFilterTokens(value any) ([]string, error) {
	var raw []string
	switch typed := value.(type) {
	case string:
		raw = []string{typed}
	case []string:
		raw = typed
	case []any:
		for _, item := range typed {
			text, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("value must be a string, got %T", item)
			}
			raw = append(raw, text)
		}
	default:
		return nil, fmt.Errorf("value must be a string or string list, got %T", value)
	}
	tokens := make([]string, 0, len(raw))
	for _, joined := range raw {
		for _, item := range strings.Split(joined, ",") {
			if item = strings.TrimSpace(item); item != "" {
				tokens = append(tokens, item)
			}
		}
	}
	return tokens, nil
}

// parseTermTokens splits the selection into the values kept and the values
// removed, preserving first-seen order so one request builds one body.
func parseTermTokens(tokens []string) ([]string, []string, error) {
	include, exclude := []string{}, []string{}
	seenInclude, seenExclude := map[string]bool{}, map[string]bool{}
	for _, item := range tokens {
		if after, negated := strings.CutPrefix(item, "!"); negated {
			after = strings.TrimSpace(after)
			if after == "" {
				return nil, nil, fmt.Errorf("excluded value must not be empty")
			}
			if !seenExclude[after] {
				exclude = append(exclude, after)
				seenExclude[after] = true
			}
			continue
		}
		if !seenInclude[item] {
			include = append(include, item)
			seenInclude[item] = true
		}
	}
	return include, exclude, nil
}

// parseRangeTokens reads bounded tokens into one range. Both edges may be
// absent, leaving that side open; a second edge on the same side is a mistake
// rather than a last-one-wins, because the request meant two things and only
// one of them could have been applied.
func parseRangeTokens(tokens []string, operand func(string) (any, error)) (*FilterRange, error) {
	bounded := &FilterRange{}
	for _, item := range tokens {
		if strings.HasPrefix(item, "!") {
			return nil, fmt.Errorf("excluding a value is not supported by a range filter")
		}
		matched := false
		for _, op := range rangeOperators {
			rest, ok := strings.CutPrefix(item, op.prefix)
			if !ok {
				continue
			}
			value, err := operand(strings.TrimSpace(rest))
			if err != nil {
				return nil, err
			}
			bound := &FilterBound{Value: value, Inclusive: op.inclusive}
			if op.lower {
				if bounded.Min != nil {
					return nil, fmt.Errorf("two lower bounds (%v and %v); a range has one", bounded.Min.Value, value)
				}
				bounded.Min = bound
			} else {
				if bounded.Max != nil {
					return nil, fmt.Errorf("two upper bounds (%v and %v); a range has one", bounded.Max.Value, value)
				}
				bounded.Max = bound
			}
			matched = true
			break
		}
		if !matched {
			return nil, fmt.Errorf("%q needs a comparison operator (>=, >, <=, <)", item)
		}
	}
	if bounded.Min == nil && bounded.Max == nil {
		return nil, nil
	}
	if err := assertOrderedBounds(bounded); err != nil {
		return nil, err
	}
	return bounded, nil
}

// assertOrderedBounds refuses a range that can never match. An empty range is a
// mistake in the request, not a query for nothing.
func assertOrderedBounds(bounded *FilterRange) error {
	if bounded.Min == nil || bounded.Max == nil {
		return nil
	}
	switch low := bounded.Min.Value.(type) {
	case float64:
		high, ok := bounded.Max.Value.(float64)
		if ok && low > high {
			return fmt.Errorf("lower bound %v is above upper bound %v", low, high)
		}
	}
	return nil
}

// parseNumericBound normalizes every numeric operand to float64, so ">=10" and
// ">=10.0" are one request and fingerprint to one cursor.
func parseNumericBound(operand string) (any, error) {
	number, err := strconv.ParseFloat(operand, 64)
	if err != nil {
		return nil, fmt.Errorf("%q is not a number", operand)
	}
	return number, nil
}

// parseTimeBound keeps a time operand as written. Date math is a string the
// backend resolves against its own clock, and rewriting it here would pin a
// rolling window to the moment the request was parsed.
func parseTimeBound(operand string) (any, error) {
	if _, err := datemath.Parse(operand); err != nil {
		return nil, fmt.Errorf("%q is not an RFC3339 time or date math: %w", operand, err)
	}
	return operand, nil
}

// parseBooleanTokens reads the yes/no arm of a toggle. Its third arm is the
// absence of a selection, so it never reaches here.
func parseBooleanTokens(tokens []string) (*bool, error) {
	if len(tokens) == 0 {
		return nil, nil
	}
	if len(tokens) > 1 {
		return nil, fmt.Errorf("a yes/no filter takes one value, got %d", len(tokens))
	}
	item := tokens[0]
	negated := false
	if after, ok := strings.CutPrefix(item, "!"); ok {
		item, negated = strings.TrimSpace(after), true
	}
	parsed, err := strconv.ParseBool(item)
	if err != nil {
		return nil, fmt.Errorf("%q is not true or false", tokens[0])
	}
	if negated {
		parsed = !parsed
	}
	return &parsed, nil
}
