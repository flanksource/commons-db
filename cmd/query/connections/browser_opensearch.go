package connections

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	dbconnection "github.com/flanksource/commons-db/connection"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/logs/opensearch"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/esdsl"
)

// browserCompileRequest asks for the Query DSL a structured specification
// compiles to. It is how the query builder shows an author the DSL their
// filters produce before anything is run. The specification stays raw until the
// params have been interpolated into it, so the preview goes through the same
// templating a profile does at execution time.
type browserCompileRequest struct {
	Search json.RawMessage   `json:"search"`
	Params map[string]any    `json:"params,omitempty"`
	Roles  map[string]string `json:"roles,omitempty"`
}

type browserCompileResult struct {
	Query string `json:"query"`
	Size  int    `json:"size"`
	From  int    `json:"from"`
}

// serveCompile compiles a specification to DSL. It is a pure function of its
// input — no backend is contacted — so the connection only scopes the route and
// proves this type has a DSL browser at all.
func (h *connectionBrowserHandler) serveCompile(w http.ResponseWriter, r *http.Request, conn *models.Connection) {
	if conn.Type != models.ConnectionTypeOpenSearch {
		http.Error(w, fmt.Sprintf("connection type %q has no query DSL to compile", conn.Type), http.StatusBadRequest)
		return
	}
	var request browserCompileRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		http.Error(w, "decode search specification: "+err.Error(), http.StatusBadRequest)
		return
	}
	rendered, _, err := query.RenderParamsJSON(h.ctx, request.Search, request.Params)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	search, err := decodeSearch(rendered)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	compiled, err := esdsl.Compile(esdsl.CompileRequest{
		Search: search,
		Params: compileParamBindings(request.Params, request.Roles),
		// A profile templates its params into the provider options and the
		// connection as well as the specification, and only the specification is
		// previewed here. Whether every declared param is referenced is therefore
		// a question this route cannot answer — execution, which sees the whole
		// profile, is where an unreferenced param is reported.
		Referenced: suppliedParamNames(request.Params),
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	dsl, err := compiled.PrettyJSON()
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	writeJSON(w, browserCompileResult{Query: dsl, Size: compiled.Size, From: compiled.From})
}

// browserValuesRequest asks what a field holds. Search scopes the answer to the
// documents the rest of the query already narrows to — the builder sends its
// specification with the condition being edited removed, so a half-typed value
// never filters its own suggestions away. Absent, the whole index is asked.
type browserValuesRequest struct {
	Index  string            `json:"index"`
	Field  string            `json:"field"`
	Query  string            `json:"q,omitempty"`
	Limit  int               `json:"limit,omitempty"`
	Search json.RawMessage   `json:"search,omitempty"`
	Params map[string]any    `json:"params,omitempty"`
	Roles  map[string]string `json:"roles,omitempty"`
}

type browserValuesResult struct {
	Values []opensearch.Value `json:"values"`
	Total  int                `json:"total"`
	Scoped bool               `json:"scoped"`
}

// serveValues answers a field's distinct values, so an author picks what the
// index actually holds instead of typing a value from memory.
func (h *connectionBrowserHandler) serveValues(w http.ResponseWriter, r *http.Request, conn *models.Connection) {
	if conn.Type != models.ConnectionTypeOpenSearch {
		http.Error(w, fmt.Sprintf("connection type %q has no field values to look up", conn.Type), http.StatusBadRequest)
		return
	}
	var request browserValuesRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		http.Error(w, "decode value lookup: "+err.Error(), http.StatusBadRequest)
		return
	}
	if request.Index == "" {
		http.Error(w, "OpenSearch index is required", http.StatusBadRequest)
		return
	}
	if request.Field == "" {
		http.Error(w, "field is required", http.StatusBadRequest)
		return
	}

	var body map[string]any
	if len(request.Search) > 0 {
		rendered, _, err := query.RenderParamsJSON(h.ctx, request.Search, request.Params)
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		search, err := decodeSearch(rendered)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		compiled, err := esdsl.Compile(esdsl.CompileRequest{
			Search: search,
			Params: compileParamBindings(request.Params, request.Roles),
			// The scope is the author's specification with the condition being
			// edited removed, so a param bound only by that condition is absent
			// from it by construction. Unused-param detection is a profile-level
			// guardrail and would reject every lookup here — the params consumed
			// while templating are a subset of these.
			Referenced: suppliedParamNames(request.Params),
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		body = compiled.Body
	}

	requestCtx := h.ctx.Wrap(r.Context())
	searcher, err := h.openSearchSearcher(requestCtx, conn)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	result, err := searcher.DistinctValues(requestCtx, opensearch.ValuesRequest{
		Index:  request.Index,
		Field:  request.Field,
		Search: request.Query,
		Limit:  request.Limit,
		Body:   body,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	writeJSON(w, browserValuesResult{Values: result.Values, Total: result.Total, Scoped: body != nil})
}

// decodeSearch reads a specification strictly, so a misspelt key is reported
// rather than silently dropped. It runs after the params have been interpolated
// into the raw document, which is why the builder previews the DSL an execution
// would produce rather than the template text.
func decodeSearch(raw []byte) (esdsl.Search, error) {
	var search esdsl.Search
	if len(raw) == 0 {
		return search, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&search); err != nil {
		return search, fmt.Errorf("decode search specification: %w", err)
	}
	return search, nil
}

func suppliedParamNames(params map[string]any) []string {
	names := make([]string, 0, len(params))
	for name := range params {
		names = append(names, name)
	}
	return names
}

func compileParamBindings(params map[string]any, roles map[string]string) []esdsl.ParamBinding {
	bindings := make([]esdsl.ParamBinding, 0, len(params))
	for name, value := range params {
		bindings = append(bindings, esdsl.ParamBinding{Name: name, Role: roles[name], Value: value})
	}
	return bindings
}

func (h *connectionBrowserHandler) executeOpenSearch(r *http.Request, conn *models.Connection, request browserQueryRequest) (browserQueryResult, error) {
	index, _ := request.Options["index"].(string)
	if index == "" {
		return browserQueryResult{}, fmt.Errorf("OpenSearch index is required")
	}
	body, limit, err := openSearchBrowserBody(request)
	if err != nil {
		return browserQueryResult{}, err
	}
	requestCtx := h.ctx.Wrap(r.Context())
	searcher, err := h.openSearchSearcher(requestCtx, conn)
	if err != nil {
		return browserQueryResult{}, err
	}
	raw, err := searcher.SearchRaw(requestCtx, opensearch.Request{Index: index, Query: body, Limit: limit})
	if err != nil {
		return browserQueryResult{}, err
	}
	rows := make([]query.Row, 0, len(raw.Hits.Hits))
	for _, hit := range raw.Hits.Hits {
		row := query.Row{"_index": hit.Index, "_id": hit.ID, "_score": hit.Score}
		for key, value := range hit.Source {
			row[key] = value
		}
		rows = append(rows, row)
	}
	return browserQueryResult{Rows: rows, Metadata: map[string]any{
		"total": raw.Hits.Total.Value, "relation": raw.Hits.Total.Relation,
		"took": raw.Took, "timedOut": raw.TimedOut, "aggregations": raw.Aggregations,
	}}, nil
}

// openSearchBrowserBody renders the search body and the hit cap. The builder
// sends a structured specification and the raw editor sends DSL; running both
// at once is an authoring mistake the browser must not silently resolve. size
// travels beside the body because the searcher sends it as a URL parameter — a
// body size would be silently overridden.
func openSearchBrowserBody(request browserQueryRequest) (body string, limit string, err error) {
	raw := strings.TrimSpace(request.Query)
	spec, hasSpec := request.Options["search"]
	if !hasSpec {
		return raw, browserOpenSearchLimit(request.Options), nil
	}
	if raw != "" {
		return "", "", fmt.Errorf(
			"options.search and the query are mutually exclusive; run the structured search or the raw query, not both")
	}
	var search esdsl.Search
	encoded, err := json.Marshal(spec)
	if err != nil {
		return "", "", fmt.Errorf("encode search specification: %w", err)
	}
	if err := json.Unmarshal(encoded, &search); err != nil {
		return "", "", fmt.Errorf("decode search specification: %w", err)
	}
	if search.Size == nil {
		if configured := browserOpenSearchLimit(request.Options); configured != "" {
			parsed, convErr := strconv.Atoi(configured)
			if convErr != nil || parsed < 0 {
				return "", "", fmt.Errorf("invalid opensearch limit %q", configured)
			}
			search.Size = &parsed
		}
	}
	compiled, err := esdsl.Compile(esdsl.CompileRequest{Search: search})
	if err != nil {
		return "", "", err
	}
	encodedBody, err := compiled.JSON()
	if err != nil {
		return "", "", err
	}
	if compiled.Size <= 0 {
		return encodedBody, "", nil
	}
	return encodedBody, strconv.Itoa(compiled.Size), nil
}

func browserOpenSearchLimit(options map[string]any) string {
	value := options["limit"]
	if value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func (h *connectionBrowserHandler) openSearchSearcher(ctx dbcontext.Context, conn *models.Connection) (*opensearch.Searcher, error) {
	httpConnection, err := dbconnection.NewHTTPConnection(ctx, *conn)
	if err != nil {
		return nil, err
	}
	return opensearch.NewWithTransport(ctx, opensearch.Backend{Address: conn.URL}, nil, httpConnection.Transport())
}
