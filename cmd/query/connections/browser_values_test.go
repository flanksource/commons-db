package connections

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/flanksource/commons-db/models"
)

// valuesBackend answers a lookup with fixed aggregations and records the body it
// was searched with.
func valuesBackend(t *testing.T, aggregations string) (*httptest.Server, *map[string]any) {
	t.Helper()
	searched := map[string]any{}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/" {
			w.WriteHeader(http.StatusOK)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&searched); err != nil {
			t.Errorf("decode search: %v", err)
		}
		_, _ = w.Write([]byte(`{"hits":{"total":{"value":0,"relation":"eq"},"hits":[]},"aggregations":` + aggregations + `}`))
	}))
	return server, &searched
}

func TestServeValuesScopesToTheSuppliedSearch(t *testing.T) {
	openSearch, searched := valuesBackend(t, `{
		"__clicky_values":{"buckets":[{"key":"payments","doc_count":12}]},
		"__clicky_total":{"value":4}
	}`)
	defer openSearch.Close()

	handler, base := browserHandlerFor(t, models.ConnectionTypeOpenSearch, openSearch.URL)
	recorder := postBrowser(t, handler, base+"/values", `{
		"index": "logs-*",
		"field": "service.name",
		"limit": 20,
		"search": {"query": {"op": "term", "field": "environment", "value": "prod"}}
	}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("values status = %d: %s", recorder.Code, recorder.Body.String())
	}

	var result browserValuesResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Values) != 1 || result.Values[0].Value != "payments" || result.Values[0].Count != 12 {
		t.Fatalf("values = %#v", result.Values)
	}
	if result.Total != 4 || !result.Scoped {
		t.Fatalf("total = %d, scoped = %v; want 4, true", result.Total, result.Scoped)
	}

	encoded, err := json.Marshal((*searched)["query"])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(`"environment":"prod"`)) {
		t.Fatalf("the supplied search must scope the lookup, got %s", encoded)
	}
}

func TestServeValuesEncodesNumericTimestampFields(t *testing.T) {
	searched := map[string]any{}
	openSearch := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/" {
			w.WriteHeader(http.StatusOK)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/_field_caps") {
			if got := r.URL.Query().Get("fields"); got != "observed_at" {
				t.Errorf("field_caps fields = %q, want observed_at", got)
			}
			_, _ = fmt.Fprint(w, `{"fields":{"observed_at":{"long":{"searchable":true,"aggregatable":true}}}}`)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&searched); err != nil {
			t.Errorf("decode search: %v", err)
		}
		_, _ = fmt.Fprint(w, `{"hits":{"total":{"value":0,"relation":"eq"},"hits":[]},"aggregations":{"__clicky_values":{"buckets":[]},"__clicky_total":{"value":0}}}`)
	}))
	defer openSearch.Close()

	handler, base := browserHandlerFor(t, models.ConnectionTypeOpenSearch, openSearch.URL)
	recorder := postBrowser(t, handler, base+"/values", `{
		"index": "logs-*",
		"field": "service.name",
		"search": {
			"timeField": "observed_at",
			"timeFieldFormat": "epoch_millis",
			"query": {"op": "match_all"}
		},
		"params": {"since": "2026-08-12"},
		"roles": {"since": "time-from"}
	}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("values status = %d: %s", recorder.Code, recorder.Body.String())
	}
	dayStart := time.Date(2026, time.August, 12, 0, 0, 0, 0, time.UTC)
	assertJSONEqual(t, "query", searched["query"], fmt.Sprintf(
		`{"bool":{"filter":[{"range":{"observed_at":{"gte":%d}}}]}}`, dayStart.UnixMilli()))
}

// The builder sends the specification with the condition being edited removed,
// so a param bound only by that condition is missing from the scope. That is the
// normal shape of a lookup, not the authoring mistake assertAllUsed catches — the
// scope must still narrow the answer rather than be rejected.
func TestServeValuesScopesWithParamsTheNarrowedSearchDoesNotUse(t *testing.T) {
	openSearch, searched := valuesBackend(t, `{
		"__clicky_values":{"buckets":[{"key":"payments","doc_count":3}]},
		"__clicky_total":{"value":3}
	}`)
	defer openSearch.Close()

	handler, base := browserHandlerFor(t, models.ConnectionTypeOpenSearch, openSearch.URL)
	recorder := postBrowser(t, handler, base+"/values", `{
		"index": "logs-*",
		"field": "service.name",
		"search": {"query": {"op": "term", "field": "environment", "value": "{{.params.env}}"}},
		"params": {"env": "prod", "service": "payments"}
	}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("values status = %d: %s", recorder.Code, recorder.Body.String())
	}

	var result browserValuesResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Scoped {
		t.Fatalf("the lookup must stay scoped: %#v", result)
	}
	encoded, err := json.Marshal((*searched)["query"])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(`"environment":"prod"`)) {
		t.Fatalf("the interpolated param must scope the lookup, got %s", encoded)
	}
}

func TestServeValuesAsksTheWholeIndexWithoutASearch(t *testing.T) {
	openSearch, searched := valuesBackend(t, `{
		"__clicky_values":{"buckets":[]},
		"__clicky_total":{"value":0}
	}`)
	defer openSearch.Close()

	handler, base := browserHandlerFor(t, models.ConnectionTypeOpenSearch, openSearch.URL)
	recorder := postBrowser(t, handler, base+"/values", `{"index":"logs-*","field":"service.name"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("values status = %d: %s", recorder.Code, recorder.Body.String())
	}

	var result browserValuesResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Scoped {
		t.Fatalf("a lookup without a search is not scoped: %#v", result)
	}
	if _, ok := (*searched)["query"].(map[string]any)["match_all"]; !ok {
		t.Fatalf("unscoped lookup asks the whole index, got %v", (*searched)["query"])
	}
}

func TestServeValuesRejectsIncompleteRequests(t *testing.T) {
	openSearch, _ := valuesBackend(t, `{}`)
	defer openSearch.Close()

	handler, base := browserHandlerFor(t, models.ConnectionTypeOpenSearch, openSearch.URL)
	tests := []struct {
		name   string
		body   string
		status int
	}{
		{"missing index", `{"field":"service.name"}`, http.StatusBadRequest},
		{"missing field", `{"index":"logs-*"}`, http.StatusBadRequest},
		{
			// A required operand with no value cannot compile, and the lookup says so
			// rather than quietly widening back to the whole index.
			name:   "uncompilable search",
			body:   `{"index":"logs-*","field":"service.name","search":{"query":{"op":"term","field":"environment"}}}`,
			status: http.StatusUnprocessableEntity,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := postBrowser(t, handler, base+"/values", test.body)
			if recorder.Code != test.status {
				t.Fatalf("status = %d, want %d: %s", recorder.Code, test.status, recorder.Body.String())
			}
		})
	}
}

func TestServeValuesRejectsNonOpenSearchConnections(t *testing.T) {
	handler, base := browserHandlerFor(t, models.ConnectionTypePostgres, "postgres://localhost/logs")
	recorder := postBrowser(t, handler, base+"/values", `{"index":"logs-*","field":"service.name"}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", recorder.Code, recorder.Body.String())
	}
}
