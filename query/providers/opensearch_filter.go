package providers

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/esdsl"
)

// decodeOpenSearchBody parses a hand-written Query DSL body. Numbers are
// decoded as json.Number so re-encoding reproduces the author's literals rather
// than rewriting them through float64.
func decodeOpenSearchBody(raw string) (map[string]any, error) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	body := map[string]any{}
	if err := decoder.Decode(&body); err != nil {
		return nil, fmt.Errorf("decode OpenSearch query: %w", err)
	}
	return body, nil
}

// applyOpenSearchFilters folds the resolved include/exclude selections into the
// query as bool clauses. The values one field collects are alternatives, so they
// become a single terms clause — one term clause per value would AND them and
// match nothing. Distinct fields stay ANDed, and two filters bound to the same
// backend field (a column filter and a list param, say) merge into one clause.
func applyOpenSearchFilters(body map[string]any, filters []query.ColumnFilterValue, paramUses []esdsl.ParamUse) error {
	usesByName := make(map[string][]esdsl.ParamUse, len(paramUses))
	for _, use := range paramUses {
		usesByName[use.Name] = append(usesByName[use.Name], use)
	}
	effective := make([]query.ColumnFilterValue, 0, len(filters))
	for _, filter := range filters {
		uses := usesByName[filter.Key]
		if len(uses) > 1 {
			return fmt.Errorf("param %q has %d structural query mappings; list parameters require exactly one", filter.Key, len(uses))
		}
		if len(uses) == 1 {
			if uses[0].Field != filter.Field {
				return fmt.Errorf("param %q maps native field %q but its query condition uses %q", filter.Key, filter.Field, uses[0].Field)
			}
			filter.Include = nil
		}
		if len(filter.Include) > 0 || len(filter.Exclude) > 0 {
			effective = append(effective, filter)
		}
	}
	if len(effective) == 0 {
		return nil
	}
	existing := body["query"]
	if existing == nil {
		existing = map[string]any{"match_all": map[string]any{}}
	}
	includes := append([]any{existing}, openSearchTermsByField(effective, func(f query.ColumnFilterValue) []string {
		return f.Include
	})...)
	excludes := openSearchTermsByField(effective, func(f query.ColumnFilterValue) []string { return f.Exclude })

	boolQuery := map[string]any{"filter": includes}
	if len(excludes) > 0 {
		boolQuery["must_not"] = excludes
	}
	body["query"] = map[string]any{"bool": boolQuery}
	return nil
}

// openSearchTermsByField groups the values select returns by backend field,
// preserving first-seen field order so a body is byte-stable across runs.
func openSearchTermsByField(filters []query.ColumnFilterValue, selectValues func(query.ColumnFilterValue) []string) []any {
	fields := make([]string, 0, len(filters))
	byField := make(map[string][]any, len(filters))
	for _, filter := range filters {
		for _, value := range selectValues(filter) {
			if _, seen := byField[filter.Field]; !seen {
				fields = append(fields, filter.Field)
			}
			byField[filter.Field] = append(byField[filter.Field], value)
		}
	}
	clauses := make([]any, 0, len(fields))
	for _, field := range fields {
		clauses = append(clauses, map[string]any{"terms": map[string]any{field: byField[field]}})
	}
	return clauses
}
