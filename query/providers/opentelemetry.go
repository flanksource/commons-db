package providers

import (
	"fmt"
	"iter"

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

// PagingModes matches the OpenSearch provider it is backed by: from/size inside
// the index result window, search_after over a point-in-time past it.
func (openTelemetryProvider) PagingModes() query.PagingMode {
	return query.PagingOffset | query.PagingCursor
}

func (p openTelemetryProvider) Execute(ctx context.Context, req query.ProviderRequest) ([]query.Row, error) {
	return drainOpenSearch(ctx, p, req)
}

// Pages walks the trace index the same way the OpenSearch provider walks a log
// index; only the row shape differs.
func (p openTelemetryProvider) Pages(ctx context.Context, req query.ProviderRequest, page query.PageRequest) iter.Seq2[query.Page, error] {
	return func(yield func(query.Page, error) bool) {
		searcher, options, err := openTelemetrySearchClient(ctx, req)
		if err != nil {
			yield(query.Page{}, err)
			return
		}
		search := openTelemetrySearch(options)
		timeFieldMapping, err := ResolveOpenSearchTimeFieldMapping(
			ctx, searcher, options.Index, search, openSearchParamBindings(req),
		)
		if err != nil {
			yield(query.Page{}, err)
			return
		}
		walk := openSearchWalk{
			searcher: searcher,
			index:    options.Index,
			build: func(position openSearchPage) (openSearchRequest, error) {
				return buildOpenTelemetryRequest(req, options, position, timeFieldMapping)
			},
			mapRows: func(raw opensearch.Response) []query.Row {
				return openTelemetryRows(raw, options)
			},
		}
		walk.run(ctx, req, page, yield)
	}
}

func openTelemetryRows(result opensearch.Response, options openTelemetryOptions) []query.Row {
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
	return rows
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
