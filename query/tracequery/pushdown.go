package tracequery

import (
	"maps"
	"slices"
	"strings"

	"github.com/flanksource/commons-db/otel/span/layout"
	"github.com/flanksource/commons-db/query/esdsl"
)

// pushdown is one condition per included attribute key, then one for the
// included services, each of which a passing trace meets on some span of its
// own. Reading the spans that meet them in turn narrows a search without
// dropping a trace Search would keep: different keys may hold on different
// spans, so they are never AND-ed within one read. Each compiles through the
// index's span layout, so a nested tag list and tag fields read alike.
// Exclusions, and anything the layout cannot match exactly, stay with Search.
func pushdown(r Request, definition layout.Definition) []esdsl.Condition {
	result := []esdsl.Condition{}
	groups := r.Attributes.includedByKey()
	for _, key := range slices.Sorted(maps.Keys(groups)) {
		if condition, ok := attributeCondition(definition, groups[key]); ok {
			result = append(result, condition)
		}
	}
	if services := r.Services.values(Include); len(services) > 0 {
		if field, ok := keywordField(definition, "service"); ok {
			slices.Sort(services)
			result = append(result, termsCondition(field, services))
		}
	}
	return result
}

// attributeCondition is any of exprs on a span or resource attribute, or false
// when one of them has no exact condition in the layout: that key is then
// matched by Search alone, which reads every span the time window holds.
func attributeCondition(definition layout.Definition, exprs []string) (esdsl.Condition, bool) {
	should := []esdsl.Condition{}
	for _, expr := range slices.Sorted(slices.Values(exprs)) {
		op, key, value, ok := attributeOperator(expr)
		if !ok {
			return esdsl.Condition{}, false
		}
		values := []string{value}
		if op == esdsl.OpExists {
			values = nil
		}
		condition, err := definition.AttributeCondition(key, op, values...)
		if err != nil {
			return esdsl.Condition{}, false
		}
		should = append(should, condition.Conditions...)
	}
	return esdsl.Condition{Op: esdsl.OpBool, Conditions: should}, true
}

// attributeOperator reads expr as attributeMatches does: a bare key is
// presence, key=value an exact value and key=prefix* a prefix. Any other
// wildcard is not pushed down.
func attributeOperator(expr string) (op esdsl.Operator, key, value string, ok bool) {
	key, want, hasValue := strings.Cut(expr, "=")
	key, want = strings.TrimSpace(key), strings.TrimSpace(want)
	star := strings.Index(want, "*")
	switch {
	case !hasValue:
		return esdsl.OpExists, key, "", true
	case star < 0:
		return esdsl.OpTerm, key, want, true
	case star > 0 && star == len(want)-1:
		return esdsl.OpPrefix, key, strings.TrimSuffix(want, "*"), true
	default:
		return "", "", "", false
	}
}

// keywordField is the field an exact terms condition on canonical reads, when
// the layout maps it to a keyword.
func keywordField(definition layout.Definition, canonical string) (string, bool) {
	field, ok := definition.Fields[canonical]
	if !ok || field.Type != "keyword" && field.Query == "" {
		return "", false
	}
	name, err := definition.SourceField(canonical)
	return name, err == nil
}

func termsCondition(field string, values []string) esdsl.Condition {
	literals := make([]esdsl.Value, len(values))
	for i, value := range values {
		literals[i] = *esdsl.Literal(value)
	}
	return esdsl.Condition{Op: esdsl.OpTerms, Field: field, Values: literals}
}

// readNarrowed reads the spans meeting each condition in turn, every read
// after the first limited to the traces the one before found, and returns the
// last read's spans, whose traces meet every condition.
func readNarrowed(read func(*esdsl.Condition) ([]Span, error), conditions []esdsl.Condition, traceIDField string) ([]Span, error) {
	if len(conditions) == 0 {
		return read(nil)
	}
	var spans []Span
	for i, condition := range conditions {
		query := esdsl.Condition{Op: esdsl.OpBool, Conditions: []esdsl.Condition{condition}}
		if i > 0 {
			ids := traceIDs(spans)
			if len(ids) == 0 {
				return spans, nil
			}
			query.Conditions = append(query.Conditions, termsCondition(traceIDField, ids))
		}
		var err error
		if spans, err = read(&query); err != nil {
			return nil, err
		}
	}
	return spans, nil
}

func traceIDs(spans []Span) []string {
	ids := map[string]bool{}
	for _, span := range spans {
		ids[span.TraceID] = true
	}
	return slices.Sorted(maps.Keys(ids))
}
