package providers

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/logs/opensearch"
	"github.com/flanksource/commons-db/query"
)

const (
	filterValuesAggregation = "__clicky_values"
	filterTotalAggregation  = "__clicky_total"
	defaultFilterValueLimit = 1000
)

func (opensearchProvider) LookupFilterValues(ctx context.Context, req query.ProviderRequest, binding query.ColumnFilterBinding, search string, limit int) ([]query.FilterOption, int, error) {
	searcher, options, err := openSearchClient(ctx, req)
	if err != nil {
		return nil, 0, err
	}
	request, err := buildOpenSearchRequest(req, options, false)
	if err != nil {
		return nil, 0, err
	}
	return lookupOpenSearchFilterValues(ctx, searcher, options.Index, request.body, binding, search, limit)
}

func (openTelemetryProvider) LookupFilterValues(ctx context.Context, req query.ProviderRequest, binding query.ColumnFilterBinding, search string, limit int) ([]query.FilterOption, int, error) {
	searcher, options, err := openTelemetrySearchClient(ctx, req)
	if err != nil {
		return nil, 0, err
	}
	request, err := buildOpenTelemetryRequest(req, options)
	if err != nil {
		return nil, 0, err
	}
	return lookupOpenSearchFilterValues(ctx, searcher, options.Index, request.body, binding, search, limit)
}

func lookupOpenSearchFilterValues(ctx context.Context, searcher *opensearch.Searcher, index string, body map[string]any, binding query.ColumnFilterBinding, search string, limit int) ([]query.FilterOption, int, error) {
	if limit <= 0 {
		limit = defaultFilterValueLimit
	}
	terms := map[string]any{"field": binding.Field, "size": limit}
	search = strings.TrimSpace(search)
	if search != "" {
		terms["include"] = openSearchContainsRegex(search)
	}
	body["size"] = 0
	// A lookup counts distinct values, so it replaces whatever aggregations the
	// query carries. Both spellings are cleared: OpenSearch rejects a body that
	// declares aggs and aggregations together.
	delete(body, "aggs")
	body["aggregations"] = map[string]any{
		filterValuesAggregation: map[string]any{"terms": terms},
		filterTotalAggregation:  totalFilterAggregation(binding.Field, search),
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, 0, fmt.Errorf("encode OpenSearch filter lookup: %w", err)
	}
	response, err := searcher.SearchRaw(ctx, opensearch.Request{Index: index, Query: string(encoded), Limit: "0"})
	if err != nil {
		return nil, 0, err
	}
	return parseOpenSearchFilterAggregations(response.Aggregations, search != "")
}

func totalFilterAggregation(field, search string) map[string]any {
	cardinality := map[string]any{"cardinality": map[string]any{"field": field}}
	if search == "" {
		return cardinality
	}
	return map[string]any{
		"filter":       map[string]any{"regexp": map[string]any{field: openSearchContainsRegex(search)}},
		"aggregations": map[string]any{"values": cardinality},
	}
}

func openSearchContainsRegex(search string) string {
	replacer := strings.NewReplacer(
		`\`, `\\`, `.`, `\.`, `?`, `\?`, `+`, `\+`, `*`, `\*`, `|`, `\|`,
		`{`, `\{`, `}`, `\}`, `[`, `\[`, `]`, `\]`, `(`, `\(`, `)`, `\)`,
		`"`, `\"`, `@`, `\@`, `#`, `\#`, `&`, `\&`, `<`, `\<`, `>`, `\>`, `~`, `\~`,
	)
	return ".*" + replacer.Replace(search) + ".*"
}

func parseOpenSearchFilterAggregations(aggregations map[string]any, searched bool) ([]query.FilterOption, int, error) {
	values, ok := aggregations[filterValuesAggregation].(map[string]any)
	if !ok {
		return nil, 0, fmt.Errorf("OpenSearch filter lookup response is missing %q aggregation", filterValuesAggregation)
	}
	buckets, ok := values["buckets"].([]any)
	if !ok {
		return nil, 0, fmt.Errorf("OpenSearch filter lookup aggregation %q has invalid buckets", filterValuesAggregation)
	}
	options := make([]query.FilterOption, 0, len(buckets))
	for _, raw := range buckets {
		bucket, ok := raw.(map[string]any)
		if !ok || bucket["key"] == nil {
			return nil, 0, fmt.Errorf("OpenSearch filter lookup returned an invalid bucket")
		}
		count, ok := numberToInt64(bucket["doc_count"])
		if !ok {
			return nil, 0, fmt.Errorf("OpenSearch filter lookup bucket %v has invalid doc_count", bucket["key"])
		}
		options = append(options, query.FilterOption{Value: fmt.Sprint(bucket["key"]), Count: count})
	}
	totalAggregation, ok := aggregations[filterTotalAggregation].(map[string]any)
	if !ok {
		return nil, 0, fmt.Errorf("OpenSearch filter lookup response is missing %q aggregation", filterTotalAggregation)
	}
	if searched {
		nested, ok := totalAggregation["values"].(map[string]any)
		if !ok {
			return nil, 0, fmt.Errorf("OpenSearch filter lookup total has invalid values aggregation")
		}
		totalAggregation = nested
	}
	total, ok := numberToInt64(totalAggregation["value"])
	if !ok {
		return nil, 0, fmt.Errorf("OpenSearch filter lookup total has invalid value")
	}
	return options, int(total), nil
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
