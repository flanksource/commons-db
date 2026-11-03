package providers

import (
	"fmt"
	"strings"

	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/logs/opensearch"
	"github.com/flanksource/commons-db/otel/span/layout"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/esdsl"
)

func (opensearchProvider) LookupFilterValues(ctx context.Context, req query.ProviderRequest, lookup query.FilterValueSearch) ([]query.FilterOption, *query.Total, error) {
	runtime, err := openSearchClient(ctx, req)
	if err != nil {
		return nil, nil, err
	}
	var timeFieldMapping *esdsl.TimeFieldMapping
	if runtime.options.Search != nil {
		timeFieldMapping, err = ResolveOpenSearchTimeFieldMapping(ctx, OpenSearchTimeFieldMappingRequest{
			Searcher: runtime.searcher, Index: runtime.options.Index, Search: *runtime.options.Search,
			Params: openSearchParamBindings(req), Inspection: req.Inspection,
		})
		if err != nil {
			return nil, nil, err
		}
	}
	// A value lookup reads an aggregation rather than hits, so it wants the
	// query body without a page position on it.
	request, err := buildOpenSearchRequest(req, runtime.options, openSearchPage{}, timeFieldMapping)
	if err != nil {
		return nil, nil, err
	}
	if req.Search != nil {
		return lookupCanonicalFilterValues(ctx, runtime.searcher, openSearchValuesSource{Index: runtime.options.Index, Body: request.body}, lookup, runtime.options.Layout)
	}
	return lookupOpenSearchFilterValues(ctx, runtime.searcher, openSearchValuesSource{
		Index: runtime.options.Index, Body: request.body,
	}, lookup)
}

func (openTelemetryProvider) LookupFilterValues(ctx context.Context, req query.ProviderRequest, lookup query.FilterValueSearch) ([]query.FilterOption, *query.Total, error) {
	runtime, err := openTelemetrySearchClient(ctx, req)
	if err != nil {
		return nil, nil, err
	}
	structured := openTelemetrySearch(runtime.options)
	var timeFieldMapping *esdsl.TimeFieldMapping
	if req.Search == nil {
		timeFieldMapping, err = ResolveOpenSearchTimeFieldMapping(ctx, OpenSearchTimeFieldMappingRequest{
			Searcher: runtime.searcher, Index: runtime.options.Index, Search: structured,
			Params: openSearchParamBindings(req), Inspection: req.Inspection,
		})
		if err != nil {
			return nil, nil, err
		}
	}
	request, err := buildOpenTelemetryRequest(req, runtime.options, openSearchPage{}, timeFieldMapping)
	if err != nil {
		return nil, nil, err
	}
	if req.Search != nil {
		return lookupCanonicalFilterValues(ctx, runtime.searcher, openSearchValuesSource{Index: runtime.options.Index, Body: request.body}, lookup, runtime.options.Layout)
	}
	return lookupOpenSearchFilterValues(ctx, runtime.searcher, openSearchValuesSource{
		Index: runtime.options.Index, Body: request.body,
	}, lookup)
}

// openSearchValuesSource is the index and query body a value lookup aggregates over.
type openSearchValuesSource struct {
	Index string
	Body  map[string]any
}

func lookupCanonicalFilterValues(ctx context.Context, searcher *opensearch.Searcher, source openSearchValuesSource, lookup query.FilterValueSearch, resolved *layout.Definition) ([]query.FilterOption, *query.Total, error) {
	canonical := lookup.Binding.Field
	field, err := resolved.SourceField(canonical)
	if err != nil {
		return nil, nil, err
	}
	if lookup.Binding.Nested != "" {
		return nil, nil, fmt.Errorf("canonical value lookups require a reversible nested layout")
	}
	if canonical == "status" {
		options := []query.FilterOption{}
		for _, value := range []string{"unset", "ok", "error"} {
			if len(resolved.Fields[canonical].Values) > 0 {
				if _, ok := resolved.Fields[canonical].Values[value]; !ok {
					continue
				}
			}
			if lookup.Search == "" || strings.Contains(value, strings.ToLower(lookup.Search)) {
				options = append(options, query.FilterOption{Value: value})
			}
		}
		return options, &query.Total{Value: int64(len(options)), Exact: true}, nil
	}
	lookup.Binding.Field = field
	options, total, err := lookupOpenSearchFilterValues(ctx, searcher, source, lookup)
	if err != nil {
		return nil, nil, err
	}
	for index, option := range options {
		value, err := resolved.CanonicalValue(canonical, option.Value)
		if err != nil {
			return nil, nil, err
		}
		options[index].Value = fmt.Sprint(value)
	}
	return options, total, nil
}

// lookupOpenSearchFilterValues adapts the searcher's distinct-value lookup to
// the filter options a bound column offers.
func lookupOpenSearchFilterValues(ctx context.Context, searcher *opensearch.Searcher, source openSearchValuesSource, lookup query.FilterValueSearch) ([]query.FilterOption, *query.Total, error) {
	binding := lookup.Binding
	result, err := searcher.DistinctValues(ctx, opensearch.ValuesRequest{
		Index:  source.Index,
		Field:  binding.Field,
		Search: lookup.Search,
		Limit:  lookup.Limit,
		Body:   source.Body,
		// A nested field's values live in entries the parent document does not
		// carry, so the lookup has to descend the same way the filter does — and
		// pin the same entry, or it would offer the values of every other tag as
		// choices for this one.
		Nested: binding.Nested,
		Where:  binding.Where,
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
