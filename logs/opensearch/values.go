package opensearch

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/flanksource/commons-db/context"
)

const (
	valuesAggregation = "__clicky_values"
	totalAggregation  = "__clicky_total"
	// DefaultValueLimit caps how many distinct values one lookup returns when the
	// caller does not say.
	DefaultValueLimit = 1000
)

// ValuesRequest asks what a field holds. Body scopes the question to the
// documents a query already narrows to; nil asks the whole index.
type ValuesRequest struct {
	Index string
	Field string
	// Search keeps only the values containing it, matched as a substring.
	Search string
	Limit  int
	Body   map[string]any
}

// Value is one distinct value and how many documents carry it.
type Value struct {
	Value string `json:"value"`
	Count int64  `json:"count"`
}

// ValuesResult carries the values the terms aggregation returned and how many
// distinct ones exist behind them, which is what tells an author the list is a
// window rather than the whole set.
type ValuesResult struct {
	Values []Value `json:"values"`
	Total  int     `json:"total"`
}

// DistinctValues answers what values a field holds, ordered by document count.
// The request body is reused as the aggregation's scope, so the values reflect
// whatever the query already narrows to.
func (t *Searcher) DistinctValues(ctx context.Context, req ValuesRequest) (ValuesResult, error) {
	if req.Field == "" {
		return ValuesResult{}, ctx.Oops().Errorf("field is empty")
	}
	limit := req.Limit
	if limit <= 0 {
		limit = DefaultValueLimit
	}
	search := strings.TrimSpace(req.Search)
	terms := map[string]any{"field": req.Field, "size": limit}
	if search != "" {
		terms["include"] = ContainsRegex(search)
	}

	body := req.Body
	if body == nil {
		body = map[string]any{"query": map[string]any{"match_all": map[string]any{}}}
	}
	body["size"] = 0
	// A lookup counts distinct values, so it replaces whatever aggregations the
	// query carries. Both spellings are cleared: OpenSearch rejects a body that
	// declares aggs and aggregations together.
	delete(body, "aggs")
	body["aggregations"] = map[string]any{
		valuesAggregation: map[string]any{"terms": terms},
		totalAggregation:  totalValuesAggregation(req.Field, search),
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return ValuesResult{}, fmt.Errorf("encode OpenSearch value lookup: %w", err)
	}
	response, err := t.SearchRaw(ctx, Request{Index: req.Index, Query: string(encoded), Limit: "0"})
	if err != nil {
		return ValuesResult{}, err
	}
	return parseValueAggregations(response.Aggregations, search != "")
}

func totalValuesAggregation(field, search string) map[string]any {
	cardinality := map[string]any{"cardinality": map[string]any{"field": field}}
	if search == "" {
		return cardinality
	}
	return map[string]any{
		"filter":       map[string]any{"regexp": map[string]any{field: ContainsRegex(search)}},
		"aggregations": map[string]any{"values": cardinality},
	}
}

// ContainsRegex renders a substring match as the anchored regexp OpenSearch
// aggregations take, escaping every character its regexp grammar reserves.
func ContainsRegex(search string) string {
	replacer := strings.NewReplacer(
		`\`, `\\`, `.`, `\.`, `?`, `\?`, `+`, `\+`, `*`, `\*`, `|`, `\|`,
		`{`, `\{`, `}`, `\}`, `[`, `\[`, `]`, `\]`, `(`, `\(`, `)`, `\)`,
		`"`, `\"`, `@`, `\@`, `#`, `\#`, `&`, `\&`, `<`, `\<`, `>`, `\>`, `~`, `\~`,
	)
	return ".*" + replacer.Replace(search) + ".*"
}

func parseValueAggregations(aggregations map[string]any, searched bool) (ValuesResult, error) {
	values, ok := aggregations[valuesAggregation].(map[string]any)
	if !ok {
		return ValuesResult{}, fmt.Errorf("OpenSearch value lookup response is missing %q aggregation", valuesAggregation)
	}
	buckets, ok := values["buckets"].([]any)
	if !ok {
		return ValuesResult{}, fmt.Errorf("OpenSearch value lookup aggregation %q has invalid buckets", valuesAggregation)
	}
	result := ValuesResult{Values: make([]Value, 0, len(buckets))}
	for _, raw := range buckets {
		bucket, ok := raw.(map[string]any)
		if !ok || bucket["key"] == nil {
			return ValuesResult{}, fmt.Errorf("OpenSearch value lookup returned an invalid bucket")
		}
		count, ok := numberToInt64(bucket["doc_count"])
		if !ok {
			return ValuesResult{}, fmt.Errorf("OpenSearch value lookup bucket %v has invalid doc_count", bucket["key"])
		}
		result.Values = append(result.Values, Value{Value: fmt.Sprint(bucket["key"]), Count: count})
	}
	total, ok := aggregations[totalAggregation].(map[string]any)
	if !ok {
		return ValuesResult{}, fmt.Errorf("OpenSearch value lookup response is missing %q aggregation", totalAggregation)
	}
	if searched {
		nested, ok := total["values"].(map[string]any)
		if !ok {
			return ValuesResult{}, fmt.Errorf("OpenSearch value lookup total has invalid values aggregation")
		}
		total = nested
	}
	count, ok := numberToInt64(total["value"])
	if !ok {
		return ValuesResult{}, fmt.Errorf("OpenSearch value lookup total has invalid value")
	}
	result.Total = int(count)
	return result, nil
}

func numberToInt64(value any) (int64, bool) {
	switch typed := value.(type) {
	case float64:
		return int64(typed), true
	case int64:
		return typed, true
	case int:
		return int64(typed), true
	default:
		return 0, false
	}
}
