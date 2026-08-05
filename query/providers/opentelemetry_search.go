package providers

import (
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/esdsl"
)

// buildOpenTelemetryRequest compiles the profile's search specification and
// composes the runtime column filters onto it.
func buildOpenTelemetryRequest(req query.ProviderRequest, options openTelemetryOptions, page openSearchPage) (openSearchRequest, error) {
	search := openTelemetrySearch(options)
	// A declared order is the profile's, and it is what a cursor is cut from,
	// so it wins over the trace-shaped default sort.
	if len(req.Order) > 0 {
		search.Sort = nil
	}
	compiled, err := esdsl.Compile(esdsl.CompileRequest{
		Search:     search,
		Params:     openSearchParamBindings(req),
		Referenced: openSearchReferencedParams(req),
		PageSize:   page.size,
	})
	if err != nil {
		return openSearchRequest{}, err
	}
	if err := applyOpenSearchFilters(compiled.Body, req.Filters, compiled.ParamUses); err != nil {
		return openSearchRequest{}, err
	}
	if len(req.Order) > 0 {
		compiled.Body["sort"] = openSearchSort(req.Order)
	}
	applyOpenSearchPage(compiled.Body, page)
	return openSearchRequest{body: compiled.Body, limit: compiled.Size, capped: compiled.Capped}, nil
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
