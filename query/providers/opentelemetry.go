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
	Format         string   `json:"format,omitempty"`
	Index          string   `json:"index,omitempty"`
	DateField      string   `json:"dateField,omitempty"`
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
	options.withDefaults()
	options.RawRows = req.RawRows || req.Search != nil
	outerModel, err := ctx.HydrateConnectionByURL(req.Connection)
	if err != nil {
		return openTelemetryRuntime{}, fmt.Errorf("hydrate opentelemetry connection %q: %w", req.Connection, err)
	}
	nested := outerModel
	if outerModel == nil {
		return openTelemetryRuntime{}, fmt.Errorf("trace connection %q not found", req.Connection)
	}
	if outerModel.Type != models.ConnectionTypeOpenSearch {
		outer, err := dbconnection.NewOpenTelemetry(outerModel)
		if err != nil {
			return openTelemetryRuntime{}, err
		}
		nested, err = outer.ResolveOpenSearch(ctx)
		if err != nil {
			return openTelemetryRuntime{}, err
		}
	}
	runtime, err := openSearchClientForConnection(ctx, nested)
	if err != nil {
		return openTelemetryRuntime{}, err
	}
	policy, err := resolveOpenSearchPagingPolicy(nested, options.OpenSearchPagingOptions)
	if err != nil {
		return openTelemetryRuntime{}, err
	}
	if req.Options["index"] == nil {
		if index := outerModel.Properties["index"]; index != "" {
			options.Index = index
		} else if index := nested.Properties["index"]; index != "" {
			options.Index = index
		}
	}
	configured := outerModel.Properties["span_layout"]
	if configured == "" {
		configured = nested.Properties["span_layout"]
	}
	options.Layout, err = resolveSpanLayout(ctx, runtime.searcher, options.Index, req, configured)
	if err != nil {
		return openTelemetryRuntime{}, err
	}
	return openTelemetryRuntime{searcher: runtime.searcher, options: options, paging: policy.mode, threshold: policy.threshold}, nil
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
