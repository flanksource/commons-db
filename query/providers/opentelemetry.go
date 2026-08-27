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

var _ query.ColumnInspectionProvider = openTelemetryProvider{}

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

type openTelemetryRuntime struct {
	searcher *opensearch.Searcher
	options  openTelemetryOptions
	paging   openSearchPagingMode
}

// PagingModes matches the nested OpenSearch connection: from/size inside the
// index result window and its configured backend cursor past it.
func (openTelemetryProvider) PagingModes() query.PagingMode {
	return query.PagingOffset | query.PagingCursor
}

// SupportsRequestSort reports yes, for the same reason OpenSearch does: it is
// the same search body underneath.
func (openTelemetryProvider) SupportsRequestSort() bool { return true }

func (p openTelemetryProvider) Execute(ctx context.Context, req query.ProviderRequest) ([]query.Row, error) {
	return drainOpenSearch(ctx, p, req)
}

func (openTelemetryProvider) InspectColumnFilters(
	ctx context.Context,
	req query.ProviderRequest,
	columns []query.ColumnDef,
) (query.ColumnInspectionResult, error) {
	runtime, err := openTelemetrySearchClient(ctx, req)
	if err != nil {
		return query.ColumnInspectionResult{}, err
	}
	search := openTelemetrySearch(runtime.options)
	return inspectOpenSearchColumnFilters(
		ctx,
		req,
		openTelemetryInspectionColumns(columns, runtime.options),
		runtime.searcher,
		openSearchInspectionSource{
			Index:  runtime.options.Index,
			Search: &search,
			Build: func(mapping *esdsl.TimeFieldMapping) (openSearchRequest, error) {
				return buildOpenTelemetryRequest(req, runtime.options, openSearchPage{}, mapping)
			},
		},
	)
}

func openTelemetryInspectionColumns(columns []query.ColumnDef, options openTelemetryOptions) []query.ColumnDef {
	statusField := "status"
	if len(options.StatusFields) > 0 && options.StatusFields[0] != "" {
		statusField = options.StatusFields[0]
	}
	fields := map[string]string{
		"timestamp": options.DateField, "trace_id": options.TraceIDField,
		"span_id": options.SpanIDField, "id": options.SpanIDField,
		"parent_id": options.ParentIDField,
		"service":   options.ServiceField, "service_name": options.ServiceField,
		"operation": options.OperationField, "operation_name": options.OperationField,
		"status": statusField,
	}
	mapped := append([]query.ColumnDef(nil), columns...)
	for index := range mapped {
		if mapped[index].Source == "" && mapped[index].Filter == nil {
			mapped[index].Source = fields[mapped[index].Name]
		}
	}
	return mapped
}

// Pages walks the trace index the same way the OpenSearch provider walks a log
// index; only the row shape differs.
func (p openTelemetryProvider) Pages(ctx context.Context, req query.ProviderRequest, page query.PageRequest) iter.Seq2[query.Page, error] {
	return func(yield func(query.Page, error) bool) {
		runtime, err := openTelemetrySearchClient(ctx, req)
		if err != nil {
			yield(query.Page{}, err)
			return
		}
		search := openTelemetrySearch(runtime.options)
		timeFieldMapping, err := ResolveOpenSearchTimeFieldMapping(ctx, OpenSearchTimeFieldMappingRequest{
			Searcher: runtime.searcher, Index: runtime.options.Index, Search: search,
			Params: openSearchParamBindings(req), Inspection: req.Inspection,
		})
		if err != nil {
			yield(query.Page{}, err)
			return
		}
		walk := openSearchWalk{
			searcher: runtime.searcher,
			index:    runtime.options.Index,
			paging:   runtime.paging,
			build: func(position openSearchPage) (openSearchRequest, error) {
				return buildOpenTelemetryRequest(req, runtime.options, position, timeFieldMapping)
			},
			mapRows: func(raw opensearch.Response) []query.Row {
				return openTelemetryRows(raw, runtime.options)
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

func openTelemetrySearchClient(ctx context.Context, req query.ProviderRequest) (openTelemetryRuntime, error) {
	if req.Connection == "" {
		return openTelemetryRuntime{}, fmt.Errorf("opentelemetry connection is required")
	}
	options, err := query.DecodeOptions[openTelemetryOptions](req.Options)
	if err != nil {
		return openTelemetryRuntime{}, err
	}
	// options.params was the ad-hoc predecessor of options.search. Decoding is
	// lenient, so a profile that still carries it would otherwise lose every
	// filter it declares without a word.
	if _, legacy := req.Options["params"]; legacy {
		return openTelemetryRuntime{}, fmt.Errorf(
			"provider.options.params has been replaced by provider.options.search; re-import the profile to migrate it")
	}
	options.withDefaults()
	if options.Format != "jaeger" && options.Format != "flat" {
		return openTelemetryRuntime{}, fmt.Errorf("unsupported opentelemetry format %q", options.Format)
	}
	outerModel, err := ctx.HydrateConnectionByURL(req.Connection)
	if err != nil {
		return openTelemetryRuntime{}, fmt.Errorf("hydrate opentelemetry connection %q: %w", req.Connection, err)
	}
	outer, err := dbconnection.NewOpenTelemetry(outerModel)
	if err != nil {
		return openTelemetryRuntime{}, err
	}
	nested, err := outer.ResolveOpenSearch(ctx)
	if err != nil {
		return openTelemetryRuntime{}, err
	}
	runtime, err := openSearchClientForConnection(ctx, nested)
	if err != nil {
		return openTelemetryRuntime{}, err
	}
	return openTelemetryRuntime{searcher: runtime.searcher, options: options, paging: runtime.paging}, nil
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
