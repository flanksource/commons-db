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
	if index == "" {
		return nil, fmt.Errorf("canonical search requires an index on the profile or connection")
	}
	inspector, err := opensearchinspect.New(searcher.GetRawClient(), opensearchinspect.Options{CacheKey: searcher.InspectionKey()})
	if err != nil {
		return nil, err
	}
	catalog, err := inspector.Fields(ctx, opensearchinspect.FieldRequest{Target: openSearchTimeTarget(index), Refresh: req.Inspection.Refresh})
	if err != nil {
		return nil, fmt.Errorf("resolve span layout on %s: %w", index, err)
	}
	if catalog.Truncated {
		return nil, fmt.Errorf("span layout inspection is incomplete: %s", catalog.TruncateReason)
	}
	fields := map[string]string{}
	// Nested leaves are kept apart: a key/value attribute list is only queryable
	// through a nested clause, never as a scalar field.
	nested := map[string]layout.NestedLeaf{}
	for _, field := range catalog.Fields {
		if field.Nested() {
			if !field.Conflicting && len(field.Types) == 1 {
				nested[field.Name] = layout.NestedLeaf{Type: field.Types[0], Container: field.Container}
			}
			continue
		}
		if field.Conflicting || len(field.Types) != 1 {
			return nil, fmt.Errorf("span layout field %s has conflicting mapping types", field.Name)
		}
		fields[field.Name] = field.Types[0]
	}
	overrides := layout.Definition{Fields: map[string]layout.Field{}}
	if configured != "" {
		decoder := json.NewDecoder(strings.NewReader(configured))
		decoder.DisallowUnknownFields()
		decoder.UseNumber()
		if err := decoder.Decode(&overrides); err != nil {
			return nil, fmt.Errorf("connection span_layout: %w", err)
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			return nil, fmt.Errorf("connection span_layout must contain one JSON object")
		}
	}
	for name, mapping := range req.Spans {
		canonical := map[string]string{"traceId": "trace_id", "spanId": "span_id", "parentId": "parent_id", "startedAt": "timestamp", "durationMs": "duration_ms", "service": "service", "operation": "operation", "status": "status", "kind": "kind"}[name]
		if canonical == "" {
			return nil, fmt.Errorf("span mapping %s has no reversible query field", name)
		}
		if overrides.Fields == nil {
			overrides.Fields = map[string]layout.Field{}
		}
		overrides.Fields[canonical] = layout.Field{Source: mapping.Source, Unit: mapping.Unit}
	}
	resolved, err := layout.Discover(layout.Discovery{Fields: fields, Nested: nested, Overrides: overrides.Fields, NestedOverrides: overrides.Nested})
	if err != nil {
		return nil, err
	}
	clock := resolved.Fields["timestamp"]
	if clock.Type == "date" || clock.Type == "date_nanos" {
		catalog, err := inspector.Fields(ctx, opensearchinspect.FieldRequest{Target: openSearchTimeTarget(index), Names: []string{clock.Source}, IncludeFormats: true, Refresh: req.Inspection.Refresh})
		if err != nil {
			return nil, fmt.Errorf("resolve timestamp format: %w", err)
		}
		found := false
		for _, field := range catalog.Fields {
			if field.Name != clock.Source {
				continue
			}
			if field.FormatConflicting {
				return nil, fmt.Errorf("timestamp %s has conflicting date formats", clock.Source)
			}
			clock.Format = field.Format
			found = true
		}
		if !found {
			return nil, fmt.Errorf("timestamp %s has no resolved date format", clock.Source)
		}
		resolved.Fields["timestamp"] = clock
	}
	return &resolved, nil
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
