package providers

import (
	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/logs/opensearch"
	"github.com/flanksource/commons-db/query"
)

func (opensearchProvider) LookupFilterValues(ctx context.Context, req query.ProviderRequest, binding query.ColumnFilterBinding, search string, limit int) ([]query.FilterOption, *query.Total, error) {
	searcher, options, err := openSearchClient(ctx, req)
	if err != nil {
		return nil, nil, err
	}
	// A value lookup reads an aggregation rather than hits, so it wants the
	// query body without a page position on it.
	request, err := buildOpenSearchRequest(req, options, openSearchPage{})
	if err != nil {
		return nil, nil, err
	}
	return lookupOpenSearchFilterValues(ctx, searcher, options.Index, request.body, binding, search, limit)
}

func (openTelemetryProvider) LookupFilterValues(ctx context.Context, req query.ProviderRequest, binding query.ColumnFilterBinding, search string, limit int) ([]query.FilterOption, *query.Total, error) {
	searcher, options, err := openTelemetrySearchClient(ctx, req)
	if err != nil {
		return nil, nil, err
	}
	request, err := buildOpenTelemetryRequest(req, options, openSearchPage{})
	if err != nil {
		return nil, nil, err
	}
	return lookupOpenSearchFilterValues(ctx, searcher, options.Index, request.body, binding, search, limit)
}

// lookupOpenSearchFilterValues adapts the searcher's distinct-value lookup to
// the filter options a bound column offers.
func lookupOpenSearchFilterValues(ctx context.Context, searcher *opensearch.Searcher, index string, body map[string]any, binding query.ColumnFilterBinding, search string, limit int) ([]query.FilterOption, *query.Total, error) {
	result, err := searcher.DistinctValues(ctx, opensearch.ValuesRequest{
		Index:  index,
		Field:  binding.Field,
		Search: search,
		Limit:  limit,
		Body:   body,
	})
	if err != nil {
		return nil, nil, err
	}
	options := make([]query.FilterOption, 0, len(result.Values))
	for _, value := range result.Values {
		options = append(options, query.FilterOption{Value: value.Value, Count: value.Count})
	}
	// The total comes from a cardinality aggregation, which OpenSearch computes
	// approximately past its precision threshold. Reporting it as inexact is
	// what stops an estimate being rendered as a count.
	return options, &query.Total{Value: int64(result.Total)}, nil
}
