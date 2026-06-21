package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"gorm.io/gorm"
)

// writeHandler owns mutating requests (POST/PUT/PATCH/DELETE) for connections and
// profiles, reading the full nested JSON body — which the clicky executor cannot
// (it flattens bodies to string flags). Reads and everything else fall through to
// next.
type writeHandler struct {
	prefix string
	db     *gorm.DB
	store  *ProfileStore
	next   http.Handler
}

func newWriteHandler(prefix string, db *gorm.DB, store *ProfileStore, next http.Handler) *writeHandler {
	return &writeHandler{prefix: strings.TrimRight(prefix, "/"), db: db, store: store, next: next}
}

func (h *writeHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	resource, id, ok := h.route(r.URL.Path)
	if !ok {
		h.next.ServeHTTP(w, r)
		return
	}
	if r.Method == http.MethodOptions {
		writeCORS(w)
		w.WriteHeader(http.StatusOK)
		return
	}
	if !isWrite(r.Method) {
		h.next.ServeHTTP(w, r)
		return
	}

	switch resource {
	case "connection":
		h.connection(w, r, id)
	case "profile":
		h.profile(w, r, id)
	default:
		h.next.ServeHTTP(w, r)
	}
}

// route returns the resource ("connection"/"profile"), the optional id, and
// whether the path is a connection/profile resource.
func (h *writeHandler) route(path string) (resource, id string, ok bool) {
	rel := strings.Trim(strings.TrimPrefix(strings.TrimSuffix(path, "/"), h.prefix), "/")
	seg := strings.Split(rel, "/")
	switch seg[0] {
	case "connection", "connections":
		resource = "connection"
	case "profile", "profiles":
		resource = "profile"
	default:
		return "", "", false
	}
	if len(seg) > 1 {
		id = seg[1]
	}
	if len(seg) > 2 {
		return "", "", false // deeper paths are not write targets
	}
	return resource, id, true
}

func (h *writeHandler) connection(w http.ResponseWriter, r *http.Request, id string) {
	switch r.Method {
	case http.MethodPost:
		body, err := decodeBody(r)
		if err != nil {
			httpError(w, http.StatusBadRequest, err)
			return
		}
		c, err := createConnection(h.db, body)
		if err != nil {
			httpError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusCreated, c)
	case http.MethodPut, http.MethodPatch:
		if id == "" {
			httpError(w, http.StatusBadRequest, fmt.Errorf("connection id is required"))
			return
		}
		body, err := decodeBody(r)
		if err != nil {
			httpError(w, http.StatusBadRequest, err)
			return
		}
		c, err := updateConnection(h.db, id, body)
		if err != nil {
			httpError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, c)
	case http.MethodDelete:
		if id == "" {
			httpError(w, http.StatusBadRequest, fmt.Errorf("connection id is required"))
			return
		}
		if err := deleteConnection(h.db, id); err != nil {
			httpError(w, http.StatusBadRequest, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func (h *writeHandler) profile(w http.ResponseWriter, r *http.Request, id string) {
	switch r.Method {
	case http.MethodPost, http.MethodPut, http.MethodPatch:
		body, err := decodeBody(r)
		if err != nil {
			httpError(w, http.StatusBadRequest, err)
			return
		}
		p, err := profileFromBody(body)
		if err != nil {
			// allow the path id to supply the name on update
			if id != "" {
				body["profile"] = id
				p, err = profileFromBody(body)
			}
			if err != nil {
				httpError(w, http.StatusBadRequest, err)
				return
			}
		}
		if err := h.store.Save(p); err != nil {
			httpError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, profileItem{p})
	case http.MethodDelete:
		if id == "" {
			httpError(w, http.StatusBadRequest, fmt.Errorf("profile id is required"))
			return
		}
		if err := h.store.Delete(id); err != nil {
			httpError(w, http.StatusBadRequest, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func isWrite(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

func decodeBody(r *http.Request) (map[string]any, error) {
	defer func() { _ = r.Body.Close() }()
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("invalid JSON body: %w", err)
	}
	return body, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	writeCORS(w)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept")
}

func httpError(w http.ResponseWriter, status int, err error) {
	writeCORS(w)
	http.Error(w, err.Error(), status)
}
