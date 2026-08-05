package providers

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/esdsl"
)

// openSearchScrollBatch caps how many hits one scroll page fetches.
const openSearchScrollBatch = 1000

// openSearchResultWindow is index.max_result_window's default: the most hits a
// single search may return before OpenSearch rejects it and points at the
// scroll API. An index may raise it, so this is the safe assumption rather than
// the truth about any one index.
const openSearchResultWindow = 10000

// openSearchScrolls decides how a bounded read is issued. A bound says how far
// to read, not how to read it: a page fits in one search, while an export
// ceiling is far past the result window and can only be reached by scrolling.
// An unbounded read has no idea how far it goes, so it scrolls too.
func openSearchScrolls(maxRows int) bool {
	return maxRows <= 0 || maxRows > openSearchResultWindow
}

// openSearchRequest is a search body plus the hit cap that must travel beside
// it. The searcher sends size as a URL parameter, so a body size would be
// silently overridden — the two are kept apart all the way to the wire.
type openSearchRequest struct {
	body  map[string]any
	limit int
}

// buildOpenSearchRequest renders the search body for req, from either the
// structured specification or the profile's raw Query DSL, and composes the
// runtime column filters onto it.
func buildOpenSearchRequest(req query.ProviderRequest, opts opensearchOptions, scroll bool) (openSearchRequest, error) {
	raw := strings.TrimSpace(req.Query)
	switch {
	case opts.Search != nil && raw != "":
		return openSearchRequest{}, fmt.Errorf(
			"provider.options.search and the profile query are mutually exclusive; keep the structured search or the raw query, not both")
	case opts.Search != nil:
		return compileOpenSearchSearch(req, opts, scroll)
	case raw != "":
		body, err := decodeOpenSearchBody(raw)
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
		return openSearchRequest{body: body, limit: boundOpenSearchLimit(limit, req.MaxRows)}, nil
	default:
		return openSearchRequest{}, fmt.Errorf("opensearch requires a query or provider.options.search")
	}
}

// compileOpenSearchSearch compiles the structured specification. options.limit
// seeds the hit cap only when the specification sets no size of its own, so the
// precedence is limit-role param, then search.size, then options.limit.
func compileOpenSearchSearch(req query.ProviderRequest, opts opensearchOptions, scroll bool) (openSearchRequest, error) {
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
	compiled, err := esdsl.Compile(esdsl.CompileRequest{
		Search:     search,
		Params:     openSearchParamBindings(req),
		Referenced: openSearchReferencedParams(req),
		Scroll:     scroll,
		MaxRows:    req.MaxRows,
	})
	if err != nil {
		return openSearchRequest{}, err
	}
	if err := applyOpenSearchFilters(compiled.Body, req.Filters, compiled.ParamUses); err != nil {
		return openSearchRequest{}, err
	}
	return openSearchRequest{body: compiled.Body, limit: compiled.Size}, nil
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

// scrollBatch is the page size for a scrolling read, bounded by the hit cap so a
// small query does not fetch a full page.
func (r openSearchRequest) scrollBatch() int {
	if r.limit > 0 && r.limit < openSearchScrollBatch {
		return r.limit
	}
	return openSearchScrollBatch
}

// limitParam renders the hit cap for the searcher's size URL parameter. An
// unspecified cap is empty, leaving the searcher's own default in place.
func (r openSearchRequest) limitParam() string {
	if r.limit <= 0 {
		return ""
	}
	return strconv.Itoa(r.limit)
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

// boundOpenSearchLimit narrows a configured limit to the caller's bound.
func boundOpenSearchLimit(limit, maxRows int) int {
	if maxRows > 0 && (limit == 0 || limit > maxRows) {
		return maxRows
	}
	return limit
}
