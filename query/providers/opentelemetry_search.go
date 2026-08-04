package providers

import (
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/esdsl"
)

// buildOpenTelemetryRequest compiles the profile's search specification and
// composes the runtime column filters onto it.
func buildOpenTelemetryRequest(req query.ProviderRequest, options openTelemetryOptions) (openSearchRequest, error) {
	compiled, err := esdsl.Compile(esdsl.CompileRequest{
		Search:     openTelemetrySearch(options),
		Params:     openSearchParamBindings(req),
		Referenced: req.TemplatedParams,
		MaxRows:    req.MaxRows,
	})
	if err != nil {
		return openSearchRequest{}, err
	}
	applyOpenSearchFilters(compiled.Body, req.Filters)
	return openSearchRequest{body: compiled.Body, limit: compiled.Size}, nil
}

// openTelemetrySearch layers the trace-shaped options underneath the
// specification: they fill what the author left unset and never override it, so
// a profile can opt out of any default by stating its own.
func openTelemetrySearch(options openTelemetryOptions) esdsl.Search {
	var search esdsl.Search
	if options.Search != nil {
		search = *options.Search
	}
	if len(search.Sort) == 0 {
		search.Sort = []esdsl.SortBy{{Field: options.DateField, Order: "desc"}}
	}
	if search.TimeField == "" {
		search.TimeField = options.DateField
	}
	if len(options.SelectFields) > 0 && len(search.Fields) == 0 {
		search.StoredFields = []string{"*"}
		search.Fields = options.SelectFields
	}
	if len(options.SourceExcludes) > 0 && search.Source == nil {
		search.Source = &esdsl.Source{Excludes: options.SourceExcludes}
	}
	if options.Limit > 0 && search.Size == nil {
		search.Size = &options.Limit
	}
	return search
}
