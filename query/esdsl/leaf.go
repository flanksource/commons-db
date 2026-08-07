package esdsl

import (
	"fmt"
	"strings"
)

// compileLeaf renders one non-group condition as an OpenSearch query clause.
func compileLeaf(node bound, path string) (map[string]any, error) {
	c := node.spec
	switch c.Op {
	case OpTerm:
		return compileTerm(node)
	case OpTerms:
		return compileTerms(node)
	case OpMatch:
		return compileMatch(node)
	case OpMatchPhrase, OpMatchPhrasePrefix:
		return compilePhrase(node)
	case OpMultiMatch:
		return compileMultiMatch(node)
	case OpPrefix, OpWildcard:
		return compilePattern(node)
	case OpRegexp:
		return compileRegexp(node, path)
	case OpFuzzy:
		return compileFuzzy(node)
	case OpRange:
		return compileRange(node)
	case OpExists:
		return map[string]any{"exists": map[string]any{"field": c.Field}}, nil
	case OpIDs:
		return map[string]any{"ids": map[string]any{"values": operandList(node.values, false)}}, nil
	case OpQueryString, OpSimpleQueryString:
		return compileQueryString(node)
	case OpMatchAll:
		body := map[string]any{}
		addBoost(body, c.Boost)
		return map[string]any{"match_all": body}, nil
	default:
		return nil, fmt.Errorf("%s: operator %q has no compiler", path, c.Op)
	}
}

// RangeBounds is the four edges of a range clause; a nil edge is unbounded. The
// values are rendered as given — a date field's operand may be an instant or
// date math, and OpenSearch is the only thing that should resolve the latter.
type RangeBounds struct {
	Gt  any
	Gte any
	Lt  any
	Lte any
}

// TermClause, TermsClause and RangeClause render the leaf clauses a runtime
// column filter compiles to.
//
// They are exported because an authored condition compiles to the same JSON,
// and a filter the operator picked from a list must not reach OpenSearch in a
// different shape from the identical condition they could have written by hand.
func TermClause(field string, value any) map[string]any {
	return map[string]any{"term": map[string]any{field: value}}
}

func TermsClause(field string, values []any) map[string]any {
	return map[string]any{"terms": map[string]any{field: values}}
}

func RangeClause(field string, bounds RangeBounds) map[string]any {
	body := map[string]any{}
	for _, bound := range []struct {
		key   string
		value any
	}{{"gt", bounds.Gt}, {"gte", bounds.Gte}, {"lt", bounds.Lt}, {"lte", bounds.Lte}} {
		if bound.value != nil {
			body[bound.key] = bound.value
		}
	}
	return map[string]any{"range": map[string]any{field: body}}
}

func compileTerm(node bound) (map[string]any, error) {
	c := node.spec
	body := map[string]any{"value": node.value.value}
	addBoost(body, c.Boost)
	addBool(body, "case_insensitive", c.CaseInsensitive)
	if len(body) == 1 {
		return TermClause(c.Field, node.value.value), nil
	}
	return map[string]any{"term": map[string]any{c.Field: body}}, nil
}

func compileTerms(node bound) (map[string]any, error) {
	c := node.spec
	compiled := TermsClause(c.Field, operandList(node.values, false))
	addBoost(compiled["terms"].(map[string]any), c.Boost)
	return compiled, nil
}

func compileMatch(node bound) (map[string]any, error) {
	c := node.spec
	body := map[string]any{"query": node.value.value}
	addString(body, "operator", c.MatchOperator)
	addString(body, "analyzer", c.Analyzer)
	addString(body, "fuzziness", c.Fuzziness)
	addBoost(body, c.Boost)
	return clause("match", c.Field, body, node.value.value), nil
}

func compilePhrase(node bound) (map[string]any, error) {
	c := node.spec
	body := map[string]any{"query": node.value.value}
	addString(body, "analyzer", c.Analyzer)
	addInt(body, "slop", c.Slop)
	addBoost(body, c.Boost)
	return clause(string(c.Op), c.Field, body, node.value.value), nil
}

func compileMultiMatch(node bound) (map[string]any, error) {
	c := node.spec
	body := map[string]any{"query": node.value.value, "fields": targetFields(c)}
	addString(body, "type", c.MultiMatchType)
	addString(body, "operator", c.MatchOperator)
	addString(body, "analyzer", c.Analyzer)
	addString(body, "fuzziness", c.Fuzziness)
	addString(body, "minimum_should_match", c.MinimumShouldMatch)
	addInt(body, "slop", c.Slop)
	addBoost(body, c.Boost)
	return map[string]any{"multi_match": body}, nil
}

func compilePattern(node bound) (map[string]any, error) {
	c := node.spec
	body := map[string]any{"value": node.value.value}
	addBool(body, "case_insensitive", c.CaseInsensitive)
	addBoost(body, c.Boost)
	return clause(string(c.Op), c.Field, body, node.value.value), nil
}

func compileRegexp(node bound, path string) (map[string]any, error) {
	c := node.spec
	pattern, ok := node.value.value.(string)
	if !ok {
		return nil, fmt.Errorf("%s: regexp requires a string pattern, got %T", path, node.value.value)
	}
	if err := validateRegexpOperand(pattern); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	body := map[string]any{"value": pattern, "max_determinized_states": maxDeterminizedStates}
	addBool(body, "case_insensitive", c.CaseInsensitive)
	addBoost(body, c.Boost)
	return map[string]any{"regexp": map[string]any{c.Field: body}}, nil
}

func compileFuzzy(node bound) (map[string]any, error) {
	c := node.spec
	body := map[string]any{"value": node.value.value}
	addString(body, "fuzziness", c.Fuzziness)
	addBoost(body, c.Boost)
	return clause("fuzzy", c.Field, body, node.value.value), nil
}

func compileRange(node bound) (map[string]any, error) {
	c := node.spec
	bounds := RangeBounds{}
	for _, bound := range []struct {
		into  *any
		value *boundValue
	}{{&bounds.Gt, node.gt}, {&bounds.Gte, node.gte}, {&bounds.Lt, node.lt}, {&bounds.Lte, node.lte}} {
		if bound.value != nil {
			*bound.into = bound.value.value
		}
	}
	compiled := RangeClause(c.Field, bounds)
	body := compiled["range"].(map[string]any)[c.Field].(map[string]any)
	addString(body, "format", c.Format)
	addString(body, "time_zone", c.TimeZone)
	addBoost(body, c.Boost)
	return compiled, nil
}

func compileQueryString(node bound) (map[string]any, error) {
	c := node.spec
	query, ok := node.value.value.(string)
	if !ok {
		query = fmt.Sprintf("%v", node.value.value)
	}
	if node.value.fromParam && (c.Escape == nil || *c.Escape) {
		query = EscapeLucene(query)
	}
	body := map[string]any{"query": query, "fields": targetFields(c)}
	addString(body, "analyzer", c.Analyzer)
	addString(body, "default_operator", c.MatchOperator)
	addString(body, "minimum_should_match", c.MinimumShouldMatch)
	addBoost(body, c.Boost)
	return map[string]any{string(c.Op): body}, nil
}

// clause emits the compact {op:{field:value}} form when no qualifier is set,
// and the expanded {op:{field:{...}}} form otherwise.
func clause(op, field string, body map[string]any, compact any) map[string]any {
	if len(body) == 1 {
		return map[string]any{op: map[string]any{field: compact}}
	}
	return map[string]any{op: map[string]any{field: body}}
}

// targetFields returns the field list for an operator that searches several
// fields, falling back to the single field when only one is declared.
func targetFields(c Condition) []string {
	if len(c.Fields) > 0 {
		fields := make([]string, len(c.Fields))
		copy(fields, c.Fields)
		return fields
	}
	return []string{c.Field}
}

func operandList(values []boundValue, escape bool) []any {
	out := make([]any, 0, len(values))
	for _, value := range values {
		if escape {
			if text, ok := value.value.(string); ok && value.fromParam {
				out = append(out, EscapeLucene(text))
				continue
			}
		}
		out = append(out, value.value)
	}
	return out
}

func addString(body map[string]any, key, value string) {
	if strings.TrimSpace(value) != "" {
		body[key] = value
	}
}

func addInt(body map[string]any, key string, value *int) {
	if value != nil {
		body[key] = *value
	}
}

func addBool(body map[string]any, key string, value *bool) {
	if value != nil {
		body[key] = *value
	}
}

func addBoost(body map[string]any, boost *float64) {
	if boost != nil {
		body["boost"] = *boost
	}
}
