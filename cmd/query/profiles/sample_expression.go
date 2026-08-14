package profiles

import (
	"encoding/json"
	"net/http"
	"strings"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
)

// sampleExpressionPath is a child of the reserved "sample" name, like the
// jsonpath evaluator, so previewing an expression costs no second profile name.
const sampleExpressionPath = "/profile/" + sampleProfileName + "/expression"

type sampleExpressionHandler struct {
	prefix string
	ctx    dbcontext.Context
	next   http.Handler
}

// The rows travel in the request because they came out of /profile/sample
// moments earlier. Re-running someone's backend query on every keystroke to
// fetch rows the caller is already holding would make the preview cost money.
//
// Scope is required rather than inferred: the same expression text means
// different things under `columns[].cel` and `processors[].config.set`, and
// guessing would make a preview that agrees with the engine only by luck.
type sampleExpressionRequest struct {
	CEL   string                `json:"cel"`
	Scope query.ExpressionScope `json:"scope"`
	Rows  []query.Row           `json:"rows"`
	Keep  string                `json:"keep,omitempty"`
}

// Results carry one entry per row — or one for a batch — each naming the row it
// came from. A per-row outcome is the whole point: `applyRowTransforms` aborts a
// sample on the first row a column expression cannot evaluate, so an author
// asking "does this work" currently learns only that some row did not.
type sampleExpressionResponse struct {
	Results []query.ExpressionResult `json:"results"`

	// Error is set only when the request as a whole could not be evaluated —
	// an empty expression, say. A failure on one row belongs on that row.
	Error string `json:"error,omitempty"`
}

func newSampleExpressionHandler(prefix string, ctx dbcontext.Context, next http.Handler) *sampleExpressionHandler {
	return &sampleExpressionHandler{prefix: strings.TrimRight(prefix, "/"), ctx: ctx, next: next}
}

func (h *sampleExpressionHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != h.prefix+sampleExpressionPath {
		h.next.ServeHTTP(w, r)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "expression evaluation requires POST", http.StatusMethodNotAllowed)
		return
	}
	defer func() { _ = r.Body.Close() }()

	var request sampleExpressionRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		http.Error(w, "invalid expression request: "+err.Error(), http.StatusBadRequest)
		return
	}

	// An unknown scope is a caller bug rather than a draft, so it fails loudly
	// instead of being answered with an empty result the UI would render as
	// "evaluates to nothing".
	switch request.Scope {
	case query.ScopeRow, query.ScopeBatch, query.ScopeBoundary:
	default:
		http.Error(w, "unknown expression scope "+string(request.Scope), http.StatusBadRequest)
		return
	}

	response := sampleExpressionResponse{Results: []query.ExpressionResult{}}
	// A half-written expression is the normal state of the input this serves, so
	// an unparseable one is an answer — the compiler's own message, which is the
	// useful part — not a failed request.
	results, err := query.EvalExpression(h.ctx, request.CEL, query.ExpressionOptions{
		Scope: request.Scope,
		Rows:  request.Rows,
		Keep:  request.Keep,
	})
	if err != nil {
		response.Error = err.Error()
	} else if results != nil {
		response.Results = results
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
