package profiles

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/flanksource/commons-db/query"
)

// maxExecuteBodyBytes bounds the POST body. A selection of ~100k ids fits well
// inside it, while an unbounded body would let one request exhaust memory.
const maxExecuteBodyBytes = 8 << 20

// profileExecuteRequest is the POST body for {prefix}/profile/{name}. A value
// may be a string or a list of strings, so a selection too large for a query
// string still travels as one request. Everything else — format, scope, paging,
// download — stays in the query string, so a run is described in one place and
// the export path needs no second reader.
type profileExecuteRequest struct {
	Params map[string]any `json:"params"`
}

// executeParams merges the request's params: the query string carries the
// bookmarkable defaults, the body overrides them with the explicit payload.
func executeParams(r *http.Request, p query.Profile) (map[string]any, error) {
	lists := map[string]bool{}
	for _, param := range p.Params {
		lists[param.Name] = param.Type == query.ParamTypeList
	}

	params := map[string]any{}
	for key, values := range r.URL.Query() {
		if reservedParam(key) || p.HasParamRoleName(query.ParamRoleLimit, key) ||
			p.HasParamRoleName(query.ParamRoleOffset, key) ||
			p.HasParamRoleName(query.ParamRoleSort, key) ||
			p.HasParamRoleName(query.ParamRoleOrder, key) || len(values) == 0 {
			continue
		}
		// A list param may repeat its key; a scalar one keeps taking the first.
		if lists[key] {
			params[key] = values
			continue
		}
		params[key] = values[0]
	}
	if r.Method != http.MethodPost {
		return params, nil
	}

	defer func() { _ = r.Body.Close() }()
	var request profileExecuteRequest
	decoder := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxExecuteBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return nil, fmt.Errorf("invalid execute request: %w", err)
	}
	for key, value := range request.Params {
		normalized, err := normalizeBodyParam(key, value)
		if err != nil {
			return nil, err
		}
		params[key] = normalized
	}
	return params, nil
}

// normalizeBodyParam accepts the JSON shapes a param value may take. A nested
// object or a non-string list element is refused rather than stringified, so a
// malformed request fails loudly instead of filtering on "map[]".
func normalizeBodyParam(key string, value any) (any, error) {
	switch typed := value.(type) {
	case nil, string, bool, float64:
		return typed, nil
	case []any:
		values := make([]string, 0, len(typed))
		for i, item := range typed {
			text, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("param %q item %d must be a string, got %T", key, i, item)
			}
			values = append(values, text)
		}
		return values, nil
	default:
		return nil, fmt.Errorf("param %q must be a string, a number, a boolean or a string list, got %T", key, value)
	}
}
