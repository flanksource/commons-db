package profiles

import (
	"fmt"

	"github.com/flanksource/commons-db/query/esdsl"
)

// legacyOperators maps the legacy per-parameter operator vocabulary onto the
// search specification's. The legacy builder split every value on commas and
// emitted one clause per part; the list operators keep that, because the
// specification's terms binding splits the same way. A phrase, pattern or query
// string now receives its value whole, which is what a comma inside one always
// meant.
var legacyOperators = map[string]esdsl.Operator{
	"":             esdsl.OpTerms,
	"term":         esdsl.OpTerms,
	"terms":        esdsl.OpTerms,
	"match_phrase": esdsl.OpMatchPhrase,
	"wildcard":     esdsl.OpWildcard,
	"query_string": esdsl.OpQueryString,
	"exists":       esdsl.OpExists,
}

// legacyOpenTelemetrySearch converts the legacy filter parameters into the
// structured search specification the opentelemetry provider compiles. Every
// condition is optional, matching the legacy builder's rule that an unsupplied
// parameter contributes no clause.
//
// Internal parameters are left out: supplying one was an error, and one left
// unsupplied contributed nothing either way. Their ParamDefs survive, so
// supplying one still fails — now because nothing in the specification
// references it.
func legacyOpenTelemetrySearch(params map[string]legacyTraceParam) (*esdsl.Search, error) {
	conditions := make([]esdsl.Condition, 0, len(params))
	for _, name := range sortedKeys(params) {
		param := params[name]
		if param.Internal {
			continue
		}
		condition, err := legacyCondition(name, param)
		if err != nil {
			return nil, fmt.Errorf("param %q: %w", name, err)
		}
		conditions = append(conditions, condition)
	}
	if len(conditions) == 0 {
		return nil, nil
	}
	search := &esdsl.Search{Query: &esdsl.Condition{Op: esdsl.OpBool, Conditions: conditions}}
	if err := search.Validate(); err != nil {
		return nil, err
	}
	return search, nil
}

func legacyCondition(name string, param legacyTraceParam) (esdsl.Condition, error) {
	operator, supported := legacyOperators[param.Operator]
	if !supported {
		return esdsl.Condition{}, fmt.Errorf("unsupported operator %q", param.Operator)
	}
	if param.Field == "" {
		return esdsl.Condition{}, fmt.Errorf("field is required")
	}
	condition := esdsl.Condition{Op: operator, Field: param.Field, Occur: esdsl.Occur(param.Clause)}
	// exists takes no operand, so the parameter gates the clause instead of
	// supplying it — the legacy builder emitted it only when one was supplied.
	if operator == esdsl.OpExists {
		condition.When = name
		return condition, nil
	}
	if operator == esdsl.OpQueryString {
		condition.Fields = []string{param.Field}
		condition.Field = ""
	}
	condition.Value = esdsl.Param(name)
	condition.Optional = true
	return condition, nil
}
