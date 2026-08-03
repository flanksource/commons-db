package providers

import (
	"fmt"

	dbconnection "github.com/flanksource/commons-db/connection"
	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/logs/opensearch"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/esdsl"
)

func init() {
	query.RegisterProvider(&openTelemetryProvider{})
}

type openTelemetryProvider struct{}

func (openTelemetryProvider) Type() string { return "opentelemetry" }

type openTelemetryOptions struct {
	Format         string   `json:"format,omitempty"`
	Index          string   `json:"index,omitempty"`
	DateField      string   `json:"dateField,omitempty"`
	TraceIDField   string   `json:"traceIdField,omitempty"`
	SpanIDField    string   `json:"spanIdField,omitempty"`
	ParentIDField  string   `json:"parentIdField,omitempty"`
	ParentRefType  string   `json:"parentRefType,omitempty"`
	ServiceField   string   `json:"serviceField,omitempty"`
	OperationField string   `json:"operationField,omitempty"`
	StatusFields   []string `json:"statusFields,omitempty"`
	SelectFields   []string `json:"selectFields,omitempty"`
	SourceExcludes []string `json:"sourceExcludes,omitempty"`
	Limit          int      `json:"limit,omitempty"`

	// Search is the structured search specification. The trace-shaped options
	// above fill whatever it leaves unset.
	Search *esdsl.Search `json:"search,omitempty"`
}

func (openTelemetryProvider) Execute(ctx context.Context, req query.ProviderRequest) ([]query.Row, error) {
	searcher, options, err := openTelemetrySearchClient(ctx, req)
	if err != nil {
		return nil, err
	}
	search, err := buildOpenTelemetryRequest(req, options)
	if err != nil {
		return nil, err
	}
	body, err := search.encode()
	if err != nil {
		return nil, err
	}
	result, err := searcher.SearchRaw(ctx, opensearch.Request{
		Index: options.Index,
		Query: body,
		Limit: search.limitParam(),
	})
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

func openTelemetrySearchClient(ctx context.Context, req query.ProviderRequest) (*opensearch.Searcher, openTelemetryOptions, error) {
	if req.Connection == "" {
		return nil, openTelemetryOptions{}, fmt.Errorf("opentelemetry connection is required")
	}
	options, err := query.DecodeOptions[openTelemetryOptions](req.Options)
	if err != nil {
		return nil, options, err
	}
	// options.params was the ad-hoc predecessor of options.search. Decoding is
	// lenient, so a profile that still carries it would otherwise lose every
	// filter it declares without a word.
	if _, legacy := req.Options["params"]; legacy {
		return nil, options, fmt.Errorf(
			"provider.options.params has been replaced by provider.options.search; re-import the profile to migrate it")
	}
	options.withDefaults()
	if options.Format != "jaeger" && options.Format != "flat" {
		return nil, options, fmt.Errorf("unsupported opentelemetry format %q", options.Format)
	}
	outerModel, err := ctx.HydrateConnectionByURL(req.Connection)
	if err != nil {
		return nil, options, fmt.Errorf("hydrate opentelemetry connection %q: %w", req.Connection, err)
	}
	outer, err := dbconnection.NewOpenTelemetry(outerModel)
	if err != nil {
		return nil, options, err
	}
	nested, err := outer.ResolveOpenSearch(ctx)
	if err != nil {
		return nil, options, err
	}
	searcher, err := openSearchClientForConnection(ctx, nested)
	return searcher, options, err
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
