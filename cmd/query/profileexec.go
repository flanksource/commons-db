package main

import (
	"encoding/json"
	"net/http"
	"strings"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
)

// execHandler serves profile execution: a plain GET to {prefix}/profile/{name}
// runs the profile with the request's query params as server-side filter values
// and returns the rows as JSON. Schema requests and every other path fall through
// to next.
type execHandler struct {
	prefix string
	ctx    dbcontext.Context
	store  *ProfileStore
	next   http.Handler
}

func newExecHandler(prefix string, ctx dbcontext.Context, store *ProfileStore, next http.Handler) *execHandler {
	return &execHandler{prefix: strings.TrimRight(prefix, "/"), ctx: ctx, store: store, next: next}
}

func (h *execHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && !wantsSchema(r) {
		if name, ok := h.profileName(r.URL.Path); ok {
			h.execute(w, r, name)
			return
		}
	}
	h.next.ServeHTTP(w, r)
}

// profileName returns the {name} segment of {prefix}/profile/{name}, or false.
func (h *execHandler) profileName(path string) (string, bool) {
	rel := strings.Trim(strings.TrimPrefix(strings.TrimSuffix(path, "/"), h.prefix), "/")
	if !strings.HasPrefix(rel, "profile/") {
		return "", false
	}
	name := strings.TrimPrefix(rel, "profile/")
	if name == "" || strings.Contains(name, "/") {
		return "", false
	}
	return name, true
}

func (h *execHandler) execute(w http.ResponseWriter, r *http.Request, name string) {
	p, err := h.store.Get(name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	params := map[string]any{}
	for k, vs := range r.URL.Query() {
		if reservedParam(k) || len(vs) == 0 {
			continue
		}
		params[k] = vs[0]
	}

	result, err := query.Execute(h.ctx, p, params)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	rows := result.Rows
	if rows == nil {
		rows = []query.Row{}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if err := json.NewEncoder(w).Encode(rows); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// reservedParam reports whether a query-string key is a transport concern (paging,
// format, content-negotiation) rather than a profile filter param.
func reservedParam(key string) bool {
	switch key {
	case "format", "page", "limit", "offset", "filename", "args", "__schema", "__lookup", "__lookup_filter", "__lookup_q":
		return true
	default:
		return false
	}
}
