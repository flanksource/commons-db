package providers

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/flanksource/commons-db/context"
	opensearchinspect "github.com/flanksource/commons-db/inspect/opensearch"
	"github.com/flanksource/commons-db/logs/opensearch"
	"github.com/flanksource/commons-db/otel/span/layout"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/esdsl"
)

func resolveSpanLayout(ctx context.Context, searcher *opensearch.Searcher, index string, req query.ProviderRequest, configured string) (*layout.Definition, error) {
	if req.Search == nil {
		return nil, nil
	}
	if err := req.Spans.ValidateReversible(); err != nil {
		return nil, err
	}
	overrides, err := decodeSpanLayout(configured)
	if err != nil {
		return nil, err
	}
	for name, mapping := range req.Spans {
		canonical := map[string]string{"traceId": "trace_id", "spanId": "span_id", "parentId": "parent_id", "startedAt": "timestamp", "durationMs": "duration_ms", "service": "service", "operation": "operation", "status": "status", "kind": "kind"}[name]
		if canonical == "" {
			return nil, fmt.Errorf("span mapping %s has no reversible query field", name)
		}
		overrides.Fields[canonical] = layout.Field{Source: mapping.Source, Unit: mapping.Unit}
	}
	resolved, err := discoverSpanLayout(ctx, spanLayoutSource{searcher: searcher, index: index, refresh: req.Inspection.Refresh}, overrides)
	if err != nil {
		return nil, err
	}
	return &resolved, nil
}

// decodeSpanLayout strictly reads a connection's span_layout property: the
// overrides it applies over the layout discovered from the index mapping.
func decodeSpanLayout(configured string) (layout.Definition, error) {
	overrides := layout.Definition{}
	if configured != "" {
		decoder := json.NewDecoder(strings.NewReader(configured))
		decoder.DisallowUnknownFields()
		decoder.UseNumber()
		if err := decoder.Decode(&overrides); err != nil {
			return overrides, fmt.Errorf("connection span_layout: %w", err)
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			return overrides, fmt.Errorf("connection span_layout must contain one JSON object")
		}
	}
	if overrides.Fields == nil {
		overrides.Fields = map[string]layout.Field{}
	}
	return overrides, nil
}

type spanLayoutSource struct {
	searcher *opensearch.Searcher
	index    string
	refresh  bool
}

// discoverSpanLayout resolves the layout of source's index from its mapped
// fields, read through the cached OpenSearch inspection, with overrides applied.
func discoverSpanLayout(ctx context.Context, source spanLayoutSource, overrides layout.Definition) (layout.Definition, error) {
	if source.index == "" {
		return layout.Definition{}, fmt.Errorf("span layout requires an index on the profile or connection")
	}
	inspector, err := opensearchinspect.New(source.searcher.GetRawClient(), opensearchinspect.Options{CacheKey: source.searcher.InspectionKey()})
	if err != nil {
		return layout.Definition{}, err
	}
	catalog, err := inspector.Fields(ctx, opensearchinspect.FieldRequest{Target: openSearchTimeTarget(source.index), Refresh: source.refresh})
	if err != nil {
		return layout.Definition{}, fmt.Errorf("resolve span layout on %s: %w", source.index, err)
	}
	discovery, err := spanLayoutDiscovery(catalog)
	if err != nil {
		return layout.Definition{}, err
	}
	discovery.Overrides, discovery.NestedOverrides, discovery.FlattenedOverrides = overrides.Fields, overrides.Nested, overrides.Flattened
	resolved, err := layout.Discover(discovery)
	if err != nil {
		return layout.Definition{}, err
	}
	clock := resolved.Fields["timestamp"]
	if clock.Type == "date" || clock.Type == "date_nanos" {
		if clock.Format, err = timestampFormat(ctx, inspector, source, clock.Source); err != nil {
			return layout.Definition{}, err
		}
		resolved.Fields["timestamp"] = clock
	}
	return resolved, nil
}

// spanLayoutDiscovery splits the inspected fields into scalar fields and the
// leaves of nested containers: a key/value attribute list is only queryable
// through a nested clause, never as a scalar field.
func spanLayoutDiscovery(catalog opensearchinspect.FieldCatalog) (layout.Discovery, error) {
	if catalog.Truncated {
		return layout.Discovery{}, fmt.Errorf("span layout inspection is incomplete: %s", catalog.TruncateReason)
	}
	discovery := layout.Discovery{Fields: map[string]string{}, Nested: map[string]layout.NestedLeaf{}}
	for _, field := range catalog.Fields {
		if field.Nested() {
			if !field.Conflicting && len(field.Types) == 1 {
				discovery.Nested[field.Name] = layout.NestedLeaf{Type: field.Types[0], Container: field.Container}
			}
			continue
		}
		if field.Conflicting || len(field.Types) != 1 {
			return layout.Discovery{}, fmt.Errorf("span layout field %s has conflicting mapping types", field.Name)
		}
		discovery.Fields[field.Name] = field.Types[0]
	}
	return discovery, nil
}

func timestampFormat(ctx context.Context, inspector *opensearchinspect.Inspector, source spanLayoutSource, name string) (string, error) {
	catalog, err := inspector.Fields(ctx, opensearchinspect.FieldRequest{Target: openSearchTimeTarget(source.index), Names: []string{name}, IncludeFormats: true, Refresh: source.refresh})
	if err != nil {
		return "", fmt.Errorf("resolve timestamp format: %w", err)
	}
	for _, field := range catalog.Fields {
		if field.Name != name {
			continue
		}
		if field.FormatConflicting {
			return "", fmt.Errorf("timestamp %s has conflicting date formats", name)
		}
		return field.Format, nil
	}
	return "", fmt.Errorf("timestamp %s has no resolved date format", name)
}

func canonicalSearchRequest(req query.ProviderRequest, page openSearchPage, resolved *layout.Definition) (openSearchRequest, error) {
	search := *req.Search
	if search.Size == nil && req.Options["limit"] != nil {
		limit, err := openSearchLimit(fmt.Sprint(req.Options["limit"]))
		if err != nil {
			return openSearchRequest{}, err
		}
		if limit > 0 {
			search.Size = &limit
		}
	}
	if search.TimeField == "" {
		search.TimeField = "timestamp"
	}
	if len(req.Order) > 0 {
		search.Sort = nil
	}
	compiled, err := esdsl.Compile(esdsl.CompileRequest{Search: search, Params: openSearchParamBindings(req), Referenced: openSearchReferencedParams(req), PageSize: page.size, TimeFieldMapping: &esdsl.TimeFieldMapping{Type: "date"}})
	if err != nil {
		return openSearchRequest{}, err
	}
	if err := applyOpenSearchFilters(compiled.Body, req.Filters, compiled.ParamUses); err != nil {
		return openSearchRequest{}, err
	}
	if len(req.Order) > 0 {
		compiled.Body["sort"] = openSearchSort(req.Order)
	}
	encoded, err := json.Marshal(compiled.Body)
	if err != nil {
		return openSearchRequest{}, err
	}
	encoded, err = resolved.Translate(encoded, layout.ToSource)
	if err != nil {
		return openSearchRequest{}, err
	}
	body, err := decodeOpenSearchBody(string(encoded))
	if err != nil {
		return openSearchRequest{}, err
	}
	applyOpenSearchPage(body, page)
	return openSearchRequest{body: body, limit: compiled.Size, capped: compiled.Capped}, nil
}

func attachSpanLayout(rows []query.Row, resolved *layout.Definition) ([]query.Row, error) {
	if resolved == nil {
		return rows, nil
	}
	encoded, err := json.Marshal(resolved)
	if err != nil {
		return nil, err
	}
	var snapshot map[string]any
	if err := json.Unmarshal(encoded, &snapshot); err != nil {
		return nil, err
	}
	for _, row := range rows {
		row["_span_layout"] = snapshot
	}
	return rows, nil
}
