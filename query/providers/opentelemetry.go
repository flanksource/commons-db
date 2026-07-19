package providers

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	dbconnection "github.com/flanksource/commons-db/connection"
	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/logs/opensearch"
	"github.com/flanksource/commons-db/query"
)

func init() {
	query.RegisterProvider(&openTelemetryProvider{})
}

type openTelemetryProvider struct{}

func (openTelemetryProvider) Type() string { return "opentelemetry" }

type openTelemetryOptions struct {
	Format         string                        `json:"format,omitempty"`
	Index          string                        `json:"index,omitempty"`
	DateField      string                        `json:"dateField,omitempty"`
	TraceIDField   string                        `json:"traceIdField,omitempty"`
	SpanIDField    string                        `json:"spanIdField,omitempty"`
	ParentIDField  string                        `json:"parentIdField,omitempty"`
	ParentRefType  string                        `json:"parentRefType,omitempty"`
	ServiceField   string                        `json:"serviceField,omitempty"`
	OperationField string                        `json:"operationField,omitempty"`
	StatusFields   []string                      `json:"statusFields,omitempty"`
	SelectFields   []string                      `json:"selectFields,omitempty"`
	SourceExcludes []string                      `json:"sourceExcludes,omitempty"`
	Params         map[string]openTelemetryParam `json:"params,omitempty"`
	Limit          int                           `json:"limit,omitempty"`
}

type openTelemetryParam struct {
	Field    string `json:"field,omitempty"`
	Operator string `json:"operator,omitempty"`
	Clause   string `json:"clause,omitempty"`
	Format   string `json:"format,omitempty"`
	Internal bool   `json:"internal,omitempty"`
}

func (openTelemetryProvider) Execute(ctx context.Context, req query.ProviderRequest) ([]query.Row, error) {
	if req.Connection == "" {
		return nil, fmt.Errorf("opentelemetry connection is required")
	}
	options, err := query.DecodeOptions[openTelemetryOptions](req.Options)
	if err != nil {
		return nil, err
	}
	options.withDefaults()
	if options.Format != "jaeger" && options.Format != "flat" {
		return nil, fmt.Errorf("unsupported opentelemetry format %q", options.Format)
	}
	outerModel, err := ctx.HydrateConnectionByURL(req.Connection)
	if err != nil {
		return nil, fmt.Errorf("hydrate opentelemetry connection %q: %w", req.Connection, err)
	}
	outer, err := dbconnection.NewOpenTelemetry(outerModel)
	if err != nil {
		return nil, err
	}
	nested, err := outer.ResolveOpenSearch(ctx)
	if err != nil {
		return nil, err
	}
	searcher, err := openSearchClientForConnection(ctx, nested)
	if err != nil {
		return nil, err
	}
	body, err := buildOpenTelemetryQuery(options, req.Params)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode OpenSearch trace query: %w", err)
	}
	limit := options.Limit
	if limit <= 0 {
		limit = 500
	}
	result, err := searcher.SearchRaw(ctx, opensearch.Request{Index: options.Index, Query: string(encoded), Limit: strconv.Itoa(limit)})
	if err != nil {
		return nil, err
	}
	rows := make([]query.Row, 0, len(result.Hits.Hits))
	for _, hit := range result.Hits.Hits {
		document := make(map[string]any, len(hit.Fields)+len(hit.Source))
		for name, value := range hit.Fields {
			document[name] = unwrapTraceValue(value)
		}
		for name, value := range hit.Source {
			document[name] = value
		}
		rows = append(rows, openTelemetryRow(document, options))
	}
	return rows, nil
}

func (options *openTelemetryOptions) withDefaults() {
	if options.Format == "" {
		options.Format = "flat"
	}
	if options.Index == "" {
		options.Index = "otel-traces-*"
	}
	if options.DateField == "" {
		options.DateField = "@timestamp"
	}
	if options.TraceIDField == "" {
		options.TraceIDField = "trace_id"
	}
	if options.SpanIDField == "" {
		options.SpanIDField = "span_id"
	}
	if options.ParentIDField == "" {
		options.ParentIDField = "parent_id"
	}
	if options.ServiceField == "" {
		options.ServiceField = "service_name"
	}
	if options.OperationField == "" {
		options.OperationField = "operation_name"
	}
}

func buildOpenTelemetryQuery(options openTelemetryOptions, params map[string]any) (map[string]any, error) {
	clauses := map[string][]map[string]any{"filter": {}, "must": {}, "should": {}, "must_not": {}}
	names := make([]string, 0, len(params))
	for name := range params {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		definition, ok := options.Params[name]
		if !ok {
			return nil, fmt.Errorf("filter %q is not supported by opentelemetry profile", name)
		}
		if definition.Internal {
			return nil, fmt.Errorf("filter %q is internal to the opentelemetry profile", name)
		}
		built, err := buildOpenTelemetryParam(definition, params[name])
		if err != nil {
			return nil, fmt.Errorf("build filter %q: %w", name, err)
		}
		clause := definition.Clause
		if clause == "" {
			clause = "filter"
		}
		if _, ok := clauses[clause]; !ok {
			return nil, fmt.Errorf("unsupported clause %q for filter %q", clause, name)
		}
		clauses[clause] = append(clauses[clause], built...)
	}
	body := map[string]any{"sort": []map[string]any{{options.DateField: map[string]any{"order": "desc"}}}}
	if len(options.SelectFields) > 0 {
		body["stored_fields"] = []string{"*"}
		body["fields"] = options.SelectFields
	}
	if len(options.SourceExcludes) > 0 {
		body["_source"] = map[string]any{"excludes": options.SourceExcludes}
	}
	boolQuery := map[string]any{}
	for _, clause := range []string{"filter", "must", "should", "must_not"} {
		if len(clauses[clause]) > 0 {
			boolQuery[clause] = clauses[clause]
		}
	}
	if len(boolQuery) == 0 {
		body["query"] = map[string]any{"match_all": map[string]any{}}
	} else {
		if len(clauses["should"]) > 0 {
			boolQuery["minimum_should_match"] = 1
		}
		body["query"] = map[string]any{"bool": boolQuery}
	}
	return body, nil
}

func buildOpenTelemetryParam(param openTelemetryParam, value any) ([]map[string]any, error) {
	if param.Field == "" {
		return nil, fmt.Errorf("field is required")
	}
	values := normalizeOpenTelemetryValues(value)
	if len(values) == 0 {
		return nil, nil
	}
	operator := param.Operator
	if operator == "" {
		operator = "term"
	}
	switch operator {
	case "term":
		if len(values) > 1 {
			return []map[string]any{{"terms": map[string]any{param.Field: values}}}, nil
		}
		return []map[string]any{{"term": map[string]any{param.Field: values[0]}}}, nil
	case "terms":
		return []map[string]any{{"terms": map[string]any{param.Field: values}}}, nil
	case "match_phrase", "wildcard":
		result := make([]map[string]any, 0, len(values))
		for _, item := range values {
			result = append(result, map[string]any{operator: map[string]any{param.Field: item}})
		}
		return result, nil
	case "query_string":
		result := make([]map[string]any, 0, len(values))
		for _, item := range values {
			result = append(result, map[string]any{"query_string": map[string]any{"fields": []string{param.Field}, "query": item}})
		}
		return result, nil
	case "exists":
		return []map[string]any{{"exists": map[string]any{"field": param.Field}}}, nil
	default:
		return nil, fmt.Errorf("unsupported operator %q", operator)
	}
}

func normalizeOpenTelemetryValues(value any) []any {
	switch typed := value.(type) {
	case []any:
		return typed
	case []string:
		values := make([]any, len(typed))
		for index := range typed {
			values[index] = typed[index]
		}
		return values
	case string:
		parts := strings.Split(typed, ",")
		values := make([]any, 0, len(parts))
		for _, part := range parts {
			if part = strings.TrimSpace(part); part != "" {
				values = append(values, part)
			}
		}
		return values
	default:
		return []any{typed}
	}
}
