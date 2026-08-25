package opensearch

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	dutyContext "github.com/flanksource/commons-db/context"
)

// valuesServer answers every search with the given aggregations payload and
// records the body it was asked with.
func valuesServer(t *testing.T, aggregations string) (*httptest.Server, *map[string]any) {
	t.Helper()
	recorded := map[string]any{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&recorded); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"hits":{"total":{"value":0,"relation":"eq"},"hits":[]},"aggregations":%s}`, aggregations)
	}))
	return server, &recorded
}

func newTestSearcher(t *testing.T, address string) (*Searcher, dutyContext.Context) {
	t.Helper()
	ctx := dutyContext.New()
	searcher, err := New(ctx, Backend{Address: address}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return searcher, ctx
}

func TestDistinctValuesAggregatesOverTheWholeIndex(t *testing.T) {
	server, recorded := valuesServer(t, `{
		"__clicky_values":{"buckets":[{"key":"payments","doc_count":12},{"key":"checkout","doc_count":3}]},
		"__clicky_total":{"value":7}
	}`)
	defer server.Close()

	searcher, ctx := newTestSearcher(t, server.URL)
	result, err := searcher.DistinctValues(ctx, ValuesRequest{Index: "logs-*", Field: "service.name", Limit: 25})
	if err != nil {
		t.Fatal(err)
	}

	want := ValuesResult{
		Values: []Value{{Value: "payments", Count: 12}, {Value: "checkout", Count: 3}},
		Total:  7,
	}
	if !reflect.DeepEqual(result, want) {
		t.Fatalf("got %#v, want %#v", result, want)
	}

	body := *recorded
	if body["size"] != float64(0) {
		t.Fatalf("lookup must not return hits, got size %v", body["size"])
	}
	if _, ok := body["query"].(map[string]any)["match_all"]; !ok {
		t.Fatalf("an unscoped lookup asks the whole index, got query %v", body["query"])
	}
	aggregations := body["aggregations"].(map[string]any)
	terms := aggregations["__clicky_values"].(map[string]any)["terms"].(map[string]any)
	if terms["field"] != "service.name" || terms["size"] != float64(25) {
		t.Fatalf("unexpected terms aggregation: %v", terms)
	}
	if _, ok := terms["include"]; ok {
		t.Fatalf("an unsearched lookup filters nothing, got include %v", terms["include"])
	}
	total := aggregations["__clicky_total"].(map[string]any)
	if _, ok := total["cardinality"]; !ok {
		t.Fatalf("total must count distinct values, got %v", total)
	}
}

func TestDistinctValuesScopesToTheRequestBody(t *testing.T) {
	server, recorded := valuesServer(t, `{
		"__clicky_values":{"buckets":[{"key":"payments","doc_count":12}]},
		"__clicky_total":{"doc_count":12,"values":{"value":1}}
	}`)
	defer server.Close()

	searcher, ctx := newTestSearcher(t, server.URL)
	result, err := searcher.DistinctValues(ctx, ValuesRequest{
		Index:  "logs-*",
		Field:  "service.name",
		Search: `pay+@"`,
		Limit:  10,
		Body: map[string]any{
			"query": map[string]any{"term": map[string]any{"environment": "prod"}},
			"aggs":  map[string]any{"stale": map[string]any{"terms": map[string]any{"field": "level"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 1 || len(result.Values) != 1 || result.Values[0].Value != "payments" {
		t.Fatalf("searched lookup reads the nested total, got %#v", result)
	}

	body := *recorded
	if _, ok := body["aggs"]; ok {
		t.Fatalf("the query's own aggregations must be replaced, got %v", body["aggs"])
	}
	encoded, err := json.Marshal(body["query"])
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"term":{"environment":"prod"}}`; string(encoded) != want {
		t.Fatalf("scope lost: got %s, want %s", encoded, want)
	}
	aggregations := body["aggregations"].(map[string]any)
	terms := aggregations["__clicky_values"].(map[string]any)["terms"].(map[string]any)
	if want := `.*pay\+\@\".*`; terms["include"] != want {
		t.Fatalf("include regex: got %v, want %s", terms["include"], want)
	}
	total := aggregations["__clicky_total"].(map[string]any)
	if total["filter"] == nil {
		t.Fatalf("a searched total counts only matching values, got %v", total)
	}
}

// A nested field's values live in entries the parent document does not carry, so
// an aggregation that stays at the parent returns nothing at all — and one that
// descends without pinning the entry offers the values of every other tag as
// choices for this one.
func TestDistinctValuesDescendsIntoOneEntryOfANestedField(t *testing.T) {
	server, recorded := valuesServer(t, `{
		"__clicky_values":{"doc_count":4,"__clicky_scope":{"doc_count":2,"__clicky_scope":{
			"buckets":[{"key":"web","doc_count":9},{"key":"api","doc_count":4}]}}},
		"__clicky_total":{"doc_count":4,"__clicky_scope":{"doc_count":2,"__clicky_scope":{"value":2}}}
	}`)
	defer server.Close()

	searcher, ctx := newTestSearcher(t, server.URL)
	result, err := searcher.DistinctValues(ctx, ValuesRequest{
		Index: "traces-*", Field: "tags.value", Limit: 25,
		Nested: "tags", Where: map[string]string{"tags.key": "app"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := ValuesResult{Values: []Value{{Value: "web", Count: 9}, {Value: "api", Count: 4}}, Total: 2}
	if !reflect.DeepEqual(result, want) {
		t.Fatalf("got %#v, want %#v", result, want)
	}

	values := (*recorded)["aggregations"].(map[string]any)["__clicky_values"].(map[string]any)
	nested, ok := values["nested"].(map[string]any)
	if !ok || nested["path"] != "tags" {
		t.Fatalf("the aggregation must descend into the nested field, got %v", values)
	}
	entry := values["aggregations"].(map[string]any)["__clicky_scope"].(map[string]any)
	pinned := entry["filter"].(map[string]any)["bool"].(map[string]any)["filter"].([]any)
	if len(pinned) != 1 || !reflect.DeepEqual(pinned[0], map[string]any{
		"term": map[string]any{"tags.key": "app"},
	}) {
		t.Fatalf("the entry must be pinned by the constants, got %v", pinned)
	}
	terms := entry["aggregations"].(map[string]any)["__clicky_scope"].(map[string]any)["terms"].(map[string]any)
	if terms["field"] != "tags.value" || terms["size"] != float64(25) {
		t.Fatalf("unexpected terms aggregation: %v", terms)
	}
}

func TestDistinctValuesRejectsPinningWithoutANestedField(t *testing.T) {
	server, _ := valuesServer(t, `{}`)
	defer server.Close()

	searcher, ctx := newTestSearcher(t, server.URL)
	_, err := searcher.DistinctValues(ctx, ValuesRequest{
		Index: "traces-*", Field: "tags.value", Where: map[string]string{"tags.key": "app"},
	})
	if err == nil {
		t.Fatal("expected an error for constants with no entry to scope them to")
	}
}

func TestDistinctValuesRejectsAnEmptyField(t *testing.T) {
	server, _ := valuesServer(t, `{}`)
	defer server.Close()

	searcher, ctx := newTestSearcher(t, server.URL)
	if _, err := searcher.DistinctValues(ctx, ValuesRequest{Index: "logs-*"}); err == nil {
		t.Fatal("expected an error for a lookup without a field")
	}
}

func TestDistinctValuesRejectsAMissingAggregation(t *testing.T) {
	server, _ := valuesServer(t, `{"__clicky_values":{"buckets":[]}}`)
	defer server.Close()

	searcher, ctx := newTestSearcher(t, server.URL)
	_, err := searcher.DistinctValues(ctx, ValuesRequest{Index: "logs-*", Field: "service.name"})
	if err == nil {
		t.Fatal("expected an error when the total aggregation is absent")
	}
}
