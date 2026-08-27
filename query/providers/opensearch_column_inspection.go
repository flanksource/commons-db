package providers

import (
	"context"
	"fmt"
	"sort"
	"strings"

	dbcontext "github.com/flanksource/commons-db/context"
	inspection "github.com/flanksource/commons-db/inspect"
	opensearchinspect "github.com/flanksource/commons-db/inspect/opensearch"
	"github.com/flanksource/commons-db/logs/opensearch"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/esdsl"
)

var openSearchColumnStatsCache = inspection.NewMemo(inspection.MemoOptions[map[string]int64]{
	Policy: inspection.Policy(inspection.CacheClassCardinality),
	Weight: func(counts map[string]int64) int { return len(counts) },
})

const openSearchColumnStatsBatchSize = 50

func (opensearchProvider) InspectColumnFilters(
	ctx dbcontext.Context,
	req query.ProviderRequest,
	columns []query.ColumnDef,
) (query.ColumnInspectionResult, error) {
	runtime, err := openSearchClient(ctx, req)
	if err != nil {
		return query.ColumnInspectionResult{}, err
	}
	return inspectOpenSearchColumnFilters(ctx, req, columns, runtime.searcher, openSearchInspectionSource{
		Index:  runtime.options.Index,
		Search: runtime.options.Search,
		Build: func(mapping *esdsl.TimeFieldMapping) (openSearchRequest, error) {
			return buildOpenSearchRequest(req, runtime.options, openSearchPage{}, mapping)
		},
	})
}

type openSearchInspectionSource struct {
	Index  string
	Search *esdsl.Search
	Build  func(*esdsl.TimeFieldMapping) (openSearchRequest, error)
}

func inspectOpenSearchColumnFilters(
	ctx dbcontext.Context,
	req query.ProviderRequest,
	columns []query.ColumnDef,
	searcher *opensearch.Searcher,
	source openSearchInspectionSource,
) (query.ColumnInspectionResult, error) {
	requestedNames := openSearchInspectionFields(columns)
	inspector, err := opensearchinspect.New(searcher.GetRawClient(), opensearchinspect.Options{
		CacheKey: searcher.InspectionKey(),
	})
	if err != nil {
		return query.ColumnInspectionResult{}, err
	}
	catalog, err := inspector.Fields(ctx, opensearchinspect.FieldRequest{
		Target: openSearchTimeTarget(source.Index), Names: requestedNames, Refresh: req.Inspection.Refresh,
	})
	if err != nil {
		return query.ColumnInspectionResult{}, fmt.Errorf("inspect OpenSearch column mappings: %w", err)
	}
	statFields := openSearchStatFields(columns, catalog.Fields)
	counts := map[string]int64{}
	cache := make([]inspection.CacheMetadata, 0, 2)
	if catalog.Cache != nil {
		cache = append(cache, *catalog.Cache)
	}
	if len(statFields) > 0 {
		var statsCache inspection.CacheMetadata
		counts, statsCache, err = inspectOpenSearchColumnStats(ctx, searcher, source, req, statFields)
		if err != nil {
			return query.ColumnInspectionResult{}, err
		}
		cache = append(cache, statsCache)
	}
	return query.ColumnInspectionResult{
		Filters: openSearchFilterSuggestions(columns, catalog.Fields, counts),
		Cache:   cache,
		Counts:  counts,
	}, nil
}

func inspectOpenSearchColumnStats(
	ctx dbcontext.Context,
	searcher *opensearch.Searcher,
	source openSearchInspectionSource,
	req query.ProviderRequest,
	fields []string,
) (map[string]int64, inspection.CacheMetadata, error) {
	key, err := inspectionRequestKey("opensearch-column-stats:"+searcher.InspectionKey(), req, fields)
	if err != nil {
		return nil, inspection.CacheMetadata{}, err
	}
	result, err := openSearchColumnStatsCache.Get(ctx, inspection.GetOptions[map[string]int64]{
		Key: key, Refresh: req.Inspection.Refresh,
		Load: func(fillContext context.Context) (map[string]int64, error) {
			loadContext := ctx.Wrap(fillContext)
			var timeFieldMapping *esdsl.TimeFieldMapping
			if source.Search != nil {
				var err error
				timeFieldMapping, err = ResolveOpenSearchTimeFieldMapping(loadContext, OpenSearchTimeFieldMappingRequest{
					Searcher: searcher, Index: source.Index, Search: *source.Search,
					Params: openSearchParamBindings(req), Inspection: req.Inspection,
				})
				if err != nil {
					return nil, err
				}
			}
			counts := make(map[string]int64, len(fields))
			for _, batch := range fieldBatches(fields, openSearchColumnStatsBatchSize) {
				request, err := source.Build(timeFieldMapping)
				if err != nil {
					return nil, err
				}
				delete(request.body, "sort")
				aggregations, err := inspectionAggregations(request.body)
				if err != nil {
					return nil, err
				}
				for index, field := range batch {
					name := inspectionAggregationName(index)
					if _, exists := aggregations[name]; exists {
						return nil, fmt.Errorf("OpenSearch query uses reserved inspection aggregation %q", name)
					}
					aggregations[name] = map[string]any{
						"terms": map[string]any{"field": field, "size": query.DefaultFilterLookupLimit + 1},
					}
				}
				request.body["aggs"] = aggregations
				encoded, err := request.encode()
				if err != nil {
					return nil, err
				}
				response, err := searcher.SearchRaw(loadContext, opensearch.Request{
					Index: source.Index, Query: encoded, Limit: "0",
				})
				if err != nil {
					return nil, err
				}
				batchCounts, err := parseOpenSearchColumnStats(response.Aggregations, batch)
				if err != nil {
					return nil, err
				}
				for field, count := range batchCounts {
					counts[field] = count
				}
			}
			return counts, nil
		},
	})
	if err != nil && !result.Cache.Cached {
		return nil, inspection.CacheMetadata{}, fmt.Errorf("inspect OpenSearch column cardinality: %w", err)
	}
	return result.Value, result.Cache, nil
}

func inspectionAggregations(body map[string]any) (map[string]any, error) {
	aggregations := make(map[string]any)
	raw, exists := body["aggs"]
	if !exists {
		raw = body["aggregations"]
		delete(body, "aggregations")
	}
	if raw == nil {
		return aggregations, nil
	}
	existing, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("OpenSearch query aggregations must be an object")
	}
	for name, aggregation := range existing {
		aggregations[name] = aggregation
	}
	return aggregations, nil
}

func parseOpenSearchColumnStats(aggregations map[string]any, fields []string) (map[string]int64, error) {
	counts := make(map[string]int64, len(fields))
	for index, field := range fields {
		raw, ok := aggregations[inspectionAggregationName(index)].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("OpenSearch column inspection response is missing aggregation for %q", field)
		}
		buckets, ok := raw["buckets"].([]any)
		if !ok {
			return nil, fmt.Errorf("OpenSearch column inspection aggregation for %q has invalid buckets", field)
		}
		count := int64(len(buckets))
		if remaining, ok := openSearchCount(raw["sum_other_doc_count"]); ok && remaining > 0 {
			count = query.DefaultFilterLookupLimit + 1
		}
		counts[field] = count
	}
	return counts, nil
}

func openSearchFilterSuggestions(
	columns []query.ColumnDef,
	fields []opensearchinspect.Field,
	counts map[string]int64,
) map[string]*query.ColumnFilterDef {
	mappings := make(map[string]opensearchinspect.Field, len(fields))
	for _, field := range fields {
		mappings[field.Name] = field
	}
	suggestions := make(map[string]*query.ColumnFilterDef)
	for _, column := range columns {
		name := inspectedColumnField(column)
		field, exists := mappings[name]
		if !exists || field.Conflicting || !field.Searchable || len(field.Types) != 1 {
			suggestions[column.Name] = &query.ColumnFilterDef{Disabled: true}
			continue
		}
		nested := ""
		if field.Nested() {
			nested = field.Container
		}
		switch {
		case openSearchAnalyzedType(field.Types[0]):
			sibling, siblingExists := mappings[name+".keyword"]
			count, counted := counts[sibling.Name]
			if siblingExists && sibling.Aggregatable && !sibling.Conflicting && !sibling.Nested() && counted && count <= query.DefaultFilterLookupLimit {
				suggestions[column.Name] = &query.ColumnFilterDef{
					Kind: query.ColumnFilterKindTerms, Field: sibling.Name, Nested: nested,
				}
			} else {
				suggestions[column.Name] = &query.ColumnFilterDef{Kind: query.ColumnFilterKindText, Nested: nested}
				if name != column.Name {
					suggestions[column.Name].Field = name
				}
			}
		case openSearchExactType(field.Types[0]):
			if count, counted := counts[name]; !counted || count > query.DefaultFilterLookupLimit {
				suggestions[column.Name] = &query.ColumnFilterDef{Kind: query.ColumnFilterKindExact, Nested: nested}
				if name != column.Name {
					suggestions[column.Name].Field = name
				}
			} else if name != column.Name {
				suggestions[column.Name] = &query.ColumnFilterDef{
					Kind: query.ColumnFilterKindTerms, Field: name, Nested: nested,
				}
			}
		default:
			suggestions[column.Name] = &query.ColumnFilterDef{Disabled: true}
		}
	}
	return suggestions
}

func openSearchInspectionFields(columns []query.ColumnDef) []string {
	names := map[string]struct{}{}
	for _, column := range columns {
		name := inspectedColumnField(column)
		names[name] = struct{}{}
		names[name+".keyword"] = struct{}{}
		for cut := strings.LastIndex(name, "."); cut > 0; cut = strings.LastIndex(name[:cut], ".") {
			names[name[:cut]] = struct{}{}
		}
	}
	result := make([]string, 0, len(names))
	for name := range names {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}

func openSearchStatFields(columns []query.ColumnDef, fields []opensearchinspect.Field) []string {
	mappings := make(map[string]opensearchinspect.Field, len(fields))
	for _, field := range fields {
		mappings[field.Name] = field
	}
	unique := map[string]struct{}{}
	for _, column := range columns {
		name := inspectedColumnField(column)
		field, exists := mappings[name]
		if !exists || field.Conflicting || field.Nested() || len(field.Types) != 1 {
			continue
		}
		if openSearchAnalyzedType(field.Types[0]) {
			field = mappings[name+".keyword"]
			name = field.Name
		}
		if name != "" && field.Aggregatable && !field.Conflicting && !field.Nested() {
			unique[name] = struct{}{}
		}
	}
	result := make([]string, 0, len(unique))
	for field := range unique {
		result = append(result, field)
	}
	sort.Strings(result)
	return result
}

func openSearchAnalyzedType(fieldType string) bool {
	return fieldType == "text" || fieldType == "match_only_text" || fieldType == "search_as_you_type"
}

func openSearchExactType(fieldType string) bool {
	return fieldType == "keyword" || fieldType == "constant_keyword" || fieldType == "wildcard" || fieldType == "ip"
}

func inspectionAggregationName(index int) string { return fmt.Sprintf("__cdb_column_%d", index) }

func openSearchCount(value any) (int64, bool) {
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
