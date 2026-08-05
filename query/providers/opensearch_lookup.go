package providers

import (
	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/logs/opensearch"
	"github.com/flanksource/commons-db/query"
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

// lookupOpenSearchFilterValues adapts the searcher's distinct-value lookup to
// the filter options a bound column offers.
func lookupOpenSearchFilterValues(ctx context.Context, searcher *opensearch.Searcher, index string, body map[string]any, binding query.ColumnFilterBinding, search string, limit int) ([]query.FilterOption, int, error) {
	result, err := searcher.DistinctValues(ctx, opensearch.ValuesRequest{
		Index:  index,
		Field:  binding.Field,
		Search: search,
		Limit:  limit,
		Body:   body,
	})
	if err != nil {
		return nil, 0, err
	}
	options := make([]query.FilterOption, 0, len(result.Values))
	for _, value := range result.Values {
		options = append(options, query.FilterOption{Value: value.Value, Count: value.Count})
	}
	return options, result.Total, nil
}
