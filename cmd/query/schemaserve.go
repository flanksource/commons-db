package main

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/flanksource/commons-db/query/schema"
)

// SchemaContentType is the media type a client sends in Accept to request a
// resource's JSON Schema instead of its data, served on the same endpoint.
const SchemaContentType = "application/schema+json"

// schemaHandler wraps the clicky executor mux and serves a resource's JSON
// Schema via content negotiation: a GET/HEAD with `Accept: application/schema+json`
// (or `?__schema`) to a known resource path returns the generated schema; every
// other request is delegated to next.
//
//   - {prefix}/connection[s]        -> the if/then connection schema
//   - {prefix}/profile              -> the profile-setup schema
//   - {prefix}/profile/{name}       -> the per-profile FilterBar+columns schema
type schemaHandler struct {
	prefix string
	store  *ProfileStore
	next   http.Handler
}

func newSchemaHandler(prefix string, store *ProfileStore, next http.Handler) *schemaHandler {
	return &schemaHandler{prefix: strings.TrimRight(prefix, "/"), store: store, next: next}
}

func (h *schemaHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !wantsSchema(r) {
		h.next.ServeHTTP(w, r)
		return
	}

	doc, ok, err := h.resolve(r.URL.Path)
	if !ok {
		h.next.ServeHTTP(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	body, err := schema.JSON(doc)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", SchemaContentType)
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(body)
}

// resolve maps a request path to its schema. The second return is false when the
// path is not a schema-serving resource (so the caller delegates to next).
func (h *schemaHandler) resolve(path string) (schema.Schema, bool, error) {
	rel := strings.Trim(strings.TrimPrefix(strings.TrimSuffix(path, "/"), h.prefix), "/")
	switch {
	case rel == "connection" || rel == "connections":
		return schema.Connection(), true, nil
	case rel == "profile" || rel == "profiles":
		return schema.Profile(), true, nil
	case strings.HasPrefix(rel, "profile/"):
		name := strings.TrimPrefix(rel, "profile/")
		if name == "" || strings.Contains(name, "/") {
			return nil, false, nil
		}
		p, err := h.store.Get(name)
		if err != nil {
			return nil, true, err
		}
		return schema.ProfileInstance(p), true, nil
	default:
		return nil, false, nil
	}
}

// wantsSchema reports whether the request asks for the JSON Schema representation
// via the Accept header or the ?__schema sentinel. Only safe methods qualify.
func wantsSchema(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	if r.URL.Query().Has("__schema") {
		return true
	}
	for _, part := range strings.Split(r.Header.Get("Accept"), ",") {
		mt := strings.ToLower(strings.TrimSpace(strings.SplitN(part, ";", 2)[0]))
		// accept the canonical spelling and the informal application/json+schema
		if mt == SchemaContentType || mt == "application/json+schema" {
			return true
		}
	}
	return false
}

// schemaAccepted is a tiny helper used in tests to assert a JSON body is a schema.
func isSchemaDoc(body []byte) bool {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return false
	}
	_, ok := m["$schema"]
	return ok
}
