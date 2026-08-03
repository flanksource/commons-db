package providers

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/flanksource/commons-db/query"
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

func applyOpenSearchFilters(body map[string]any, filters []query.ColumnFilterValue) {
	if len(filters) == 0 {
		return
	}
	existing := body["query"]
	if existing == nil {
		existing = map[string]any{"match_all": map[string]any{}}
	}
	includes := []any{existing}
	excludes := []any{}
	for _, filter := range filters {
		for _, value := range filter.Include {
			includes = append(includes, openSearchTerm(filter.Field, value))
		}
		for _, value := range filter.Exclude {
			excludes = append(excludes, openSearchTerm(filter.Field, value))
		}
	}
	boolQuery := map[string]any{"filter": includes}
	if len(excludes) > 0 {
		boolQuery["must_not"] = excludes
	}
	body["query"] = map[string]any{"bool": boolQuery}
}

func openSearchTerm(field, value string) map[string]any {
	return map[string]any{"term": map[string]any{field: value}}
}
