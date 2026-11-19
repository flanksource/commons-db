package tracequery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/flanksource/clicky/rpc"
)

const handlerTimeout = 30 * time.Second

// Handler serves service's traces of source relative to where it is mounted
// (behind http.StripPrefix): GET / searches with the query parameters
// ParseRequest reads, and GET /{traceId} opens one trace. An Accept of
// application/schema+json returns the Request schema instead.
func Handler(service Service, source string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "traces are read with GET", http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("Accept") == "application/schema+json" {
			writeJSON(w, rpc.SchemaForStruct(Request{}))
			return
		}
		values, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		request, err := ParseRequest(values)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		request.Source = source
		ctx, cancel := context.WithTimeout(r.Context(), handlerTimeout)
		defer cancel()
		var result any
		if id := strings.Trim(r.URL.Path, "/"); id == "" {
			result, err = service.Search(ctx, request)
		} else {
			result, err = service.Trace(ctx, request, id)
		}
		if err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, ErrTraceNotFound) {
				status = http.StatusNotFound
			}
			http.Error(w, err.Error(), status)
			return
		}
		writeJSON(w, result)
	})
}

// ParseRequest reads a Request from query parameters named by its JSON
// fields, each given once: integers as numbers and Modes as a JSON object.
func ParseRequest(values url.Values) (Request, error) {
	schema := rpc.SchemaForStruct(Request{})
	body := map[string]any{}
	for key, items := range values {
		property, ok := schema.Properties[key]
		if !ok || len(items) != 1 {
			return Request{}, fmt.Errorf("unknown or repeated trace parameter %q", key)
		}
		var err error
		switch property.Type {
		case "integer":
			body[key], err = strconv.Atoi(items[0])
		case "object":
			var modes map[string]string
			err = json.Unmarshal([]byte(items[0]), &modes)
			body[key] = modes
		default:
			body[key] = items[0]
		}
		if err != nil {
			return Request{}, fmt.Errorf("%s: %w", key, err)
		}
	}
	data, err := json.Marshal(body)
	if err != nil {
		return Request{}, err
	}
	var request Request
	err = json.Unmarshal(data, &request)
	return request, err
}

func writeJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	_ = json.NewEncoder(w).Encode(payload)
}
