package connections

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/flanksource/clicky/entity"
)

func (s *Service) lookupHandler(prefix string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		params := r.URL.Query()
		path := strings.TrimSuffix(r.URL.Path, "/")
		if r.Method != http.MethodGet || params.Get("__lookup") != "filters" || (path != prefix+"/connection" && path != prefix+"/connections") {
			next.ServeHTTP(w, r)
			return
		}
		if key := params.Get("__lookup_filter"); key != "" && key != "connection" {
			entity.WriteError(w, entity.NewStatusErrorf(http.StatusBadRequest, "invalid_filter", "unknown connection lookup filter %q", key))
			return
		}
		conns, err := s.List(ListOptions{Type: params.Get("type"), Types: params.Get("types")})
		if err != nil {
			entity.WriteError(w, err)
			return
		}
		values, total := connectionOptions(conns, params.Get("__lookup_q"), 100)
		options := make(map[string]entity.LookupNode, len(values))
		for reference, label := range values {
			options[reference] = entity.LookupNode{Kind: "text", Text: label.String(), Plain: label.String()}
		}
		response := entity.LookupResponse{Filters: map[string]entity.LookupFilter{"connection": {Label: "Connection", Options: options, Total: total, Truncated: total > len(options)}}}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(response); err != nil {
			panic(err)
		}
	})
}
