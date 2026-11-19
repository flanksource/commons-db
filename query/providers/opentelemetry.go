package providers

import (
	"fmt"
	"iter"

	dbconnection "github.com/flanksource/commons-db/connection"
	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/logs/opensearch"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/otel/span/layout"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/esdsl"
	"github.com/flanksource/commons-db/query/providers/internal/providerkit"
)

func init() {
	query.RegisterProvider(&openTelemetryProvider{})
}

type openTelemetryProvider struct{}

var _ query.ColumnInspectionProvider = openTelemetryProvider{}

func (openTelemetryProvider) Type() string { return "opentelemetry" }

func (openTelemetryProvider) BackendCapabilities(
	_ context.Context,
	req query.ProviderRequest,
) (dbconnection.BackendCapabilities, error) {
	options, err := query.DecodeOptions[OpenTelemetryOptions](req.Options)
	if err != nil {
		return dbconnection.BackendCapabilities{}, err
	}
	options.withDefaults()
	return documentArrayCapabilities("opentelemetry", options.Index), nil
}

type OpenTelemetryOptions struct {
	Layout  *layout.Definition `json:"-"`
	RawRows bool               `json:"-"`
	OpenSearchPagingOptions
	Format    string `json:"format,omitempty"`
	Index     string `json:"index,omitempty"`
	DateField string `json:"dateField,omitempty"`
	// TraceIDField and SpanIDField name the span identity fields a row is
	// normalized from, when the span layout resolves them to a field outside
	// the names normalization tries.
	TraceIDField   string   `json:"traceIdField,omitempty"`
	SpanIDField    string   `json:"spanIdField,omitempty"`
	SelectFields   []string `json:"selectFields,omitempty"`
	SourceExcludes []string `json:"sourceExcludes,omitempty"`
	Limit          int      `json:"limit,omitempty"`

	// Search is the structured search specification. The trace-shaped options
	// above fill whatever it leaves unset.
	Search *esdsl.Search `json:"search,omitempty"`
}

type openTelemetryRuntime struct {
	threshold int
	searcher  *opensearch.Searcher
	options   OpenTelemetryOptions
	paging    openSearchPagingMode
	// spanLayout is the connection's span_layout property, unparsed.
	spanLayout string
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
		openSearchInspectionSource{
			Searcher: runtime.searcher,
			Index:    runtime.options.Index,
			Search:   &search,
			Build: func(mapping *esdsl.TimeFieldMapping) (openSearchRequest, error) {
				return buildOpenTelemetryRequest(req, runtime.options, openSearchPage{}, mapping)
			},
		},
	)
}

func openTelemetryInspectionColumns(columns []query.ColumnDef, options OpenTelemetryOptions) []query.ColumnDef {
	fields := map[string]string{
		"timestamp": options.DateField, "trace_id": "trace_id",
		"span_id": "span_id", "parent_id": "parent_id",
		"service": "service_name", "operation": "operation_name", "status": "status",
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
		if !req.RawRows {
			if err := query.ValidateSpanOptions(req.Options); err != nil {
				yield(query.Page{}, err)
				return
			}
		}
		runtime, err := openTelemetrySearchClient(ctx, req)
		if err != nil {
			yield(query.Page{}, err)
			return
		}
		search := openTelemetrySearch(runtime.options)
		var timeFieldMapping *esdsl.TimeFieldMapping
		if req.Search == nil {
			timeFieldMapping, err = ResolveOpenSearchTimeFieldMapping(ctx, OpenSearchTimeFieldMappingRequest{
				Searcher: runtime.searcher, Index: runtime.options.Index, Search: search,
				Params: openSearchParamBindings(req), Inspection: req.Inspection,
			})
			if err != nil {
				yield(query.Page{}, err)
				return
			}
		}
		walk := openSearchWalk{
			searcher:    runtime.searcher,
			index:       runtime.options.Index,
			paging:      runtime.paging,
			threshold:   runtime.threshold,
			diagnostics: req.Diagnostics,
			build: func(position openSearchPage) (openSearchRequest, error) {
				return buildOpenTelemetryRequest(req, runtime.options, position, timeFieldMapping)
			},
			mapRows: func(raw opensearch.Response) ([]query.Row, error) {
				rows, err := openTelemetryRows(raw, runtime.options)
				if err != nil {
					return nil, err
				}
				return attachSpanLayout(rows, runtime.options.Layout)
			},
		}
		walk.run(ctx, req, page, yield)
	}
}

func openTelemetryRows(result opensearch.Response, options OpenTelemetryOptions) ([]query.Row, error) {
	rows := openSearchRows(result)
	for index, document := range rows {
		row, err := openTelemetryRow(document, options)
		if err != nil {
			return nil, fmt.Errorf("span %s/%s: %w", document["source_index"], document["source_id"], err)
		}
		rows[index] = row
	}
	return rows, nil
}

func openTelemetrySearchClient(ctx context.Context, req query.ProviderRequest) (openTelemetryRuntime, error) {
	if req.Connection == "" {
		return openTelemetryRuntime{}, fmt.Errorf("opentelemetry connection is required")
	}
	options, err := providerkit.DecodeOptions[OpenTelemetryOptions](req.Options)
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
	options.RawRows = req.RawRows || req.Search != nil
	outer, nested, err := openTelemetryConnections(ctx, req.Connection)
	if err != nil {
		return openTelemetryRuntime{}, err
	}
	runtime, err := openSearchClientForConnection(ctx, nested)
	if err != nil {
		return openTelemetryRuntime{}, err
	}
	policy, err := resolveOpenSearchPagingPolicy(nested, options.OpenSearchPagingOptions)
	if err != nil {
		return openTelemetryRuntime{}, err
	}
	// The request's options win, then the opentelemetry connection's own
	// properties, then those of the OpenSearch connection it wraps.
	if req.Options["index"] == nil {
		options.Index = connectionProperty("index", outer, nested)
	}
	if req.Options["format"] == nil {
		options.Format = connectionProperty("format", outer, nested)
		if err := options.Validate(); err != nil {
			return openTelemetryRuntime{}, fmt.Errorf("connection %q: %w", req.Connection, err)
		}
	}
	options.withDefaults()
	spanLayout := connectionProperty("span_layout", outer, nested)
	options.Layout, err = resolveSpanLayout(ctx, runtime.searcher, options.Index, req, spanLayout)
	if err != nil {
		return openTelemetryRuntime{}, err
	}
	return openTelemetryRuntime{searcher: runtime.searcher, options: options, paging: policy.mode, threshold: policy.threshold, spanLayout: spanLayout}, nil
}

// openTelemetryConnections hydrates reference and the OpenSearch connection
// holding its spans: the nested one an opentelemetry connection names, or the
// connection itself when it is OpenSearch.
func openTelemetryConnections(ctx context.Context, reference string) (outer, nested *models.Connection, err error) {
	outer, err = ctx.HydrateConnectionByURL(reference)
	if err != nil {
		return nil, nil, fmt.Errorf("hydrate opentelemetry connection %q: %w", reference, err)
	}
	if outer == nil {
		return nil, nil, fmt.Errorf("trace connection %q not found", reference)
	}
	if outer.Type == models.ConnectionTypeOpenSearch {
		return outer, outer, nil
	}
	wrapper, err := dbconnection.NewOpenTelemetry(outer)
	if err != nil {
		return nil, nil, err
	}
	nested, err = wrapper.ResolveOpenSearch(ctx)
	return outer, nested, err
}

func connectionProperty(key string, outer, nested *models.Connection) string {
	if value := outer.Properties[key]; value != "" {
		return value
	}
	return nested.Properties[key]
}

// SpanLayoutRequest names the index whose span layout is resolved: the one an
// opentelemetry read of Connection with Options would search.
type SpanLayoutRequest struct {
	// Connection is an opentelemetry or opensearch connection reference.
	Connection string
	// Options are the opentelemetry provider's options; their index wins over
	// the connection's.
	Options map[string]any
	// Refresh re-reads the index mapping instead of the cached inspection.
	Refresh bool
}

// ResolveSpanLayout discovers the span layout of a connection's index: its
// mapped fields, with the connection's span_layout applied as overrides. The
// mapping is read through the OpenSearch inspection memo, so it is cached per
// connection and index, bounded and refreshed by that memo's policy.
func ResolveSpanLayout(ctx context.Context, request SpanLayoutRequest) (layout.Definition, error) {
	runtime, err := openTelemetrySearchClient(ctx, query.ProviderRequest{Connection: request.Connection, Options: request.Options})
	if err != nil {
		return layout.Definition{}, err
	}
	overrides, err := decodeSpanLayout(runtime.spanLayout)
	if err != nil {
		return layout.Definition{}, err
	}
	return discoverSpanLayout(ctx, spanLayoutSource{searcher: runtime.searcher, index: runtime.options.Index, refresh: request.Refresh}, overrides)
}

// Validate refuses a format the provider cannot shape rows from; empty is flat.
func (options OpenTelemetryOptions) Validate() error {
	switch options.Format {
	case "", "flat", "jaeger":
		return nil
	}
	return fmt.Errorf("unsupported opentelemetry format %q", options.Format)
}

func (options *OpenTelemetryOptions) withDefaults() {
	if options.Index == "" {
		options.Index = "otel-traces-*"
	}
	if options.DateField == "" {
		options.DateField = "@timestamp"
	}
}
