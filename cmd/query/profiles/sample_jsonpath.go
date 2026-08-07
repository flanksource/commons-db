package profiles

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/flanksource/commons-db/query"
)

// sampleJSONPathPath is a child of the reserved "sample" name rather than a
// sibling of it, so evaluating a path costs no second profile name: the exec
// handler only claims single-segment /profile/{name}, and the sample handler
// only claims that segment exactly.
const sampleJSONPathPath = "/profile/" + sampleProfileName + "/jsonpath"

type sampleJSONPathHandler struct {
	prefix string
	next   http.Handler
}

// The row travels in the request because it came out of /profile/sample moments
// earlier. Re-running someone's backend query on every keystroke to fetch a row
// the caller is already holding would make the preview cost real money.
type sampleJSONPathRequest struct {
	JSONPath string    `json:"jsonpath"`
	Source   string    `json:"source,omitempty"`
	Row      query.Row `json:"row"`
}

// Matches carries every match rather than the 0/1/N collapse a column cell gets,
// so an author can tell "selected nothing" from "selected one null".
type sampleJSONPathResponse struct {
	Matches     []any  `json:"matches"`
	Count       int    `json:"count"`
	Error       string `json:"error,omitempty"`
	FilterField string `json:"filterField,omitempty"`
}

func newSampleJSONPathHandler(prefix string, next http.Handler) *sampleJSONPathHandler {
	return &sampleJSONPathHandler{prefix: strings.TrimRight(prefix, "/"), next: next}
}

func (h *sampleJSONPathHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != h.prefix+sampleJSONPathPath {
		h.next.ServeHTTP(w, r)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "jsonpath evaluation requires POST", http.StatusMethodNotAllowed)
		return
	}
	defer func() { _ = r.Body.Close() }()
	var request sampleJSONPathRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		http.Error(w, "invalid jsonpath request: "+err.Error(), http.StatusBadRequest)
		return
	}

	response := sampleJSONPathResponse{Matches: []any{}}
	// A half-written path is the normal state of the input this serves, so an
	// unparseable one is an answer — the parser's own message, which is the
	// useful part — not a failed request.
	if expression := strings.TrimSpace(request.JSONPath); expression == "" {
		response.Error = "enter a JSONPath expression"
	} else if matches, err := query.EvalJSONPath(expression, request.Source, request.Row); err != nil {
		response.Error = err.Error()
	} else {
		response.Matches = matches
		response.Count = len(matches)
		if field, ok := query.FilterFieldForJSONPath(expression, request.Source); ok {
			response.FilterField = field
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
