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

func compileTerm(node bound) (map[string]any, error) {
	c := node.spec
	body := map[string]any{"value": node.value.value}
	addBoost(body, c.Boost)
	addBool(body, "case_insensitive", c.CaseInsensitive)
	return clause("term", c.Field, body, node.value.value), nil
}

func compileTerms(node bound) (map[string]any, error) {
	c := node.spec
	body := map[string]any{c.Field: operandList(node.values, false)}
	addBoost(body, c.Boost)
	return map[string]any{"terms": body}, nil
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
	body := map[string]any{}
	for _, bound := range []struct {
		key   string
		value *boundValue
	}{{"gt", node.gt}, {"gte", node.gte}, {"lt", node.lt}, {"lte", node.lte}} {
		if bound.value != nil {
			body[bound.key] = bound.value.value
		}
	}
	addString(body, "format", c.Format)
	addString(body, "time_zone", c.TimeZone)
	addBoost(body, c.Boost)
	return map[string]any{"range": map[string]any{c.Field: body}}, nil
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
