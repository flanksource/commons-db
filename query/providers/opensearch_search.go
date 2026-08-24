package providers

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/esdsl"
)

// openSearchWalkBatch is how many hits one page of a full walk fetches.
const openSearchWalkBatch = 1000

// openSearchResultWindow is index.max_result_window's default: the most hits a
// from/size search may reach before OpenSearch rejects it. An index may raise
// it, so this is the safe assumption rather than the truth about any one index.
// Past it a walk needs a cursor, which is a thing to tell the caller rather
// than to silently switch mechanism over.
const openSearchResultWindow = 10000

// openSearchPage is where one search reads from: a from/size offset, or a
// search_after position pinned to a point-in-time. The two are alternatives —
// OpenSearch rejects from alongside search_after.
type openSearchPage struct {
	from  int
	size  int
	after []any
	pit   string
}

// openSearchRequest is a search body plus the hit cap that must travel beside
// it. The searcher sends size as a URL parameter, so a body size would be
// silently overridden — the two are kept apart all the way to the wire.
type openSearchRequest struct {
	body  map[string]any
	limit int

	// capped reports that the profile's own size held the page below what was
	// asked for, so a short page is not read as the end of the index.
	capped bool
}

// buildOpenSearchRequest renders the search body for req, from either the
// structured specification or the profile's raw Query DSL, and composes the
// runtime column filters and the page position onto it.
func buildOpenSearchRequest(
	req query.ProviderRequest,
	opts opensearchOptions,
	page openSearchPage,
	timeFieldMapping *esdsl.TimeFieldMapping,
) (openSearchRequest, error) {
	raw := strings.TrimSpace(req.Query)
	var built openSearchRequest
	var err error
	switch {
	case opts.Search != nil && raw != "":
		return openSearchRequest{}, fmt.Errorf(
			"provider.options.search and the profile query are mutually exclusive; keep the structured search or the raw query, not both")
	case opts.Search != nil:
		built, err = compileOpenSearchSearch(req, opts, page, timeFieldMapping)
	case raw != "":
		built, err = buildOpenSearchRawRequest(req, opts, page)
	default:
		return openSearchRequest{}, fmt.Errorf("opensearch requires a query or provider.options.search")
	}
	if err != nil {
		return openSearchRequest{}, err
	}
	applyOpenSearchPage(built.body, page)
	return built, nil
}

func buildOpenSearchRawRequest(req query.ProviderRequest, opts opensearchOptions, page openSearchPage) (openSearchRequest, error) {
	body, err := decodeOpenSearchBody(req.Query)
	if err != nil {
		return openSearchRequest{}, err
	}
	limit, err := openSearchLimit(opts.Limit)
	if err != nil {
		return openSearchRequest{}, err
	}
	if err := applyOpenSearchFilters(body, req.Filters, nil); err != nil {
		return openSearchRequest{}, err
	}
	if len(req.Order) > 0 {
		body["sort"] = openSearchSort(req.Order)
	}
	bounded, capped := boundOpenSearchLimit(limit, page.size)
	return openSearchRequest{body: body, limit: bounded, capped: capped}, nil
}

// compileOpenSearchSearch compiles the structured specification. options.limit
// seeds the hit cap only when the specification sets no size of its own, so the
// precedence is search.size, then options.limit.
func compileOpenSearchSearch(
	req query.ProviderRequest,
	opts opensearchOptions,
	page openSearchPage,
	timeFieldMapping *esdsl.TimeFieldMapping,
) (openSearchRequest, error) {
	search := *opts.Search
	if search.Size == nil {
		limit, err := openSearchLimit(opts.Limit)
		if err != nil {
			return openSearchRequest{}, err
		}
		if limit > 0 {
			search.Size = &limit
		}
	}
	// A declared order is the profile's, and it is what a cursor is cut from,
	// so it wins over a sort the specification carries for its own reasons.
	if len(req.Order) > 0 {
		search.Sort = nil
	}
	compiled, err := esdsl.Compile(esdsl.CompileRequest{
		Search:           search,
		Params:           openSearchParamBindings(req),
		Referenced:       openSearchReferencedParams(req),
		PageSize:         page.size,
		TimeFieldMapping: timeFieldMapping,
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
	return openSearchRequest{body: compiled.Body, limit: compiled.Size, capped: compiled.Capped}, nil
}

// applyOpenSearchPage writes the position into the body. from and search_after
// are mutually exclusive, and a point-in-time replaces the index on the wire.
func applyOpenSearchPage(body map[string]any, page openSearchPage) {
	delete(body, "from")
	delete(body, "search_after")
	delete(body, "pit")

	switch {
	case len(page.after) > 0:
		body["search_after"] = page.after
	case page.from > 0:
		body["from"] = page.from
	}
	if page.pit != "" {
		body["pit"] = map[string]any{"id": page.pit, "keep_alive": "60s"}
	}
}

// openSearchSort renders the profile's declared order, which both orders the
// hits and decides the shape of the sort values a cursor is cut from.
func openSearchSort(order query.Order) []any {
	sort := make([]any, 0, len(order))
	for _, by := range order {
		direction := "asc"
		if by.Desc {
			direction = "desc"
		}
		sort = append(sort, map[string]any{by.Column: map[string]any{"order": direction}})
	}
	return sort
}

func openSearchReferencedParams(req query.ProviderRequest) []string {
	referenced := append([]string{}, req.TemplatedParams...)
	for _, filter := range req.Filters {
		if _, isParam := req.Params[filter.Key]; isParam {
			referenced = append(referenced, filter.Key)
		}
	}
	return referenced
}

// openSearchParamBindings pairs each resolved parameter with its declared role.
func openSearchParamBindings(req query.ProviderRequest) []esdsl.ParamBinding {
	bindings := make([]esdsl.ParamBinding, 0, len(req.Params))
	for name, value := range req.Params {
		bindings = append(bindings, esdsl.ParamBinding{
			Name:  name,
			Role:  string(req.ParamRoles[name]),
			Value: value,
		})
	}
	return bindings
}

// encode renders the request body.
func (r openSearchRequest) encode() (string, error) {
	encoded, err := json.Marshal(r.body)
	if err != nil {
		return "", fmt.Errorf("encode OpenSearch query: %w", err)
	}
	return string(encoded), nil
}

// limitParam renders the hit cap for the searcher's size URL parameter. There
// is no unspecified case: the searcher requires a size, because an unset one
// used to mean a silent 500.
func (r openSearchRequest) limitParam() string {
	return strconv.Itoa(max(r.limit, 0))
}

func openSearchLimit(limit string) (int, error) {
	if strings.TrimSpace(limit) == "" {
		return 0, nil
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(limit))
	if err != nil || parsed < 0 {
		return 0, fmt.Errorf("invalid opensearch limit %q", limit)
	}
	return parsed, nil
}

// boundOpenSearchLimit narrows a configured limit to the page being asked for,
// and reports when the profile's own limit is the narrower of the two.
//
// That report is the point. A profile carrying `size: 50` used to serve 50 rows
// to a request for 100 and let the short page be read as the end of the index —
// so a million-document index reported a total of 50.
func boundOpenSearchLimit(limit, pageSize int) (int, bool) {
	switch {
	case pageSize <= 0:
		return limit, false
	case limit <= 0 || limit > pageSize:
		return pageSize, false
	default:
		return limit, true
	}
}
