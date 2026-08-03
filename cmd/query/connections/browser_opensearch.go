package connections

import (
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
// filters produce before anything is run.
type browserCompileRequest struct {
	Search esdsl.Search      `json:"search"`
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
	compiled, err := esdsl.Compile(esdsl.CompileRequest{
		Search: request.Search,
		Params: compileParamBindings(request.Params, request.Roles),
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
