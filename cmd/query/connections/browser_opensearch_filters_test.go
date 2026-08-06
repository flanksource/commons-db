package connections

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	opensearchinspect "github.com/flanksource/commons-db/inspect/opensearch"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
)

func mapping(name, kind string, searchable, aggregatable bool) opensearchinspect.Field {
	return opensearchinspect.Field{
		Name: name, Types: []string{kind}, Searchable: searchable, Aggregatable: aggregatable,
	}
}

func filterKinds(columns []query.ColumnDef) map[string]string {
	kinds := map[string]string{}
	for _, column := range columns {
		if column.Filter != nil && !column.Filter.Disabled {
			kinds[column.Name] = string(column.Filter.Kind)
		}
	}
	return kinds
}

func TestOpenSearchColumnsOfferAFilterPerMappingFamily(t *testing.T) {
	rows := []query.Row{{
		"_id": "1", "_score": 1.0,
		"service": "payments", "level": "error", "latency_ms": 12, "@timestamp": "2026-08-05T00:00:00Z",
		"deleted": false, "client_ip": "10.0.0.1", "trace": map[string]any{"id": "abc"},
	}}
	columns := openSearchBrowserColumns(rows, []opensearchinspect.Field{
		mapping("service", "keyword", true, true),
		mapping("level", "keyword", true, true),
		mapping("latency_ms", "long", true, true),
		mapping("@timestamp", "date", true, true),
		mapping("deleted", "boolean", true, true),
		mapping("client_ip", "ip", true, true),
		mapping("trace", "object", true, false),
	})

	want := map[string]string{
		"service": "terms", "level": "terms", "client_ip": "terms",
		"latency_ms": "range", "@timestamp": "time", "deleted": "boolean",
	}
	got := filterKinds(columns)
	if len(got) != len(want) {
		t.Fatalf("filter kinds = %+v, want %+v", got, want)
	}
	for name, kind := range want {
		if got[name] != kind {
			t.Errorf("column %q kind = %q, want %q", name, got[name], kind)
		}
	}

	// The hit's own keys are the display set, so the document metadata a search
	// always returns is still a column — it just cannot be narrowed on, because
	// the mapping says nothing about it.
	names := make([]string, 0, len(columns))
	for _, column := range columns {
		names = append(names, column.Name)
	}
	if !strings.Contains(strings.Join(names, ","), "_id") {
		t.Fatalf("columns = %v, want the hit metadata among them", names)
	}
}

// A mapping lists every field the index could hold; a hit carries the few it
// does. Building the table from the mapping would render a wall of empty
// columns, so a mapped field no document has is not a column at all.
func TestOpenSearchColumnsComeFromTheHitsNotTheMapping(t *testing.T) {
	columns := openSearchBrowserColumns(
		[]query.Row{{"service": "payments"}},
		[]opensearchinspect.Field{
			mapping("service", "keyword", true, true),
			mapping("kubernetes.labels.app", "keyword", true, true),
		},
	)
	if len(columns) != 1 || columns[0].Name != "service" {
		t.Fatalf("columns = %+v, want only the field the hit carried", columns)
	}
}

func TestOpenSearchTextColumnAggregatesThroughItsKeywordSibling(t *testing.T) {
	columns := openSearchBrowserColumns(
		[]query.Row{{"message": "connection refused"}},
		[]opensearchinspect.Field{
			mapping("message", "text", true, false),
			mapping("message.keyword", "keyword", true, true),
		},
	)
	if len(columns) != 1 {
		t.Fatalf("columns = %+v", columns)
	}
	filter := columns[0].Filter
	// The column keeps its own name; only the aggregation and the term clause
	// move to the sibling.
	if columns[0].Name != "message" || filter == nil || filter.Kind != query.ColumnFilterKindTerms {
		t.Fatalf("column = %+v, filter = %+v", columns[0], filter)
	}
	if filter.Field != "message.keyword" {
		t.Fatalf("filter field = %q, want the keyword sibling", filter.Field)
	}
}

// Without a sibling an analyzed field has no doc values, so it is matched
// rather than enumerated — a value list the cluster would refuse to produce is
// worse than no list.
func TestOpenSearchTextColumnWithoutASiblingIsMatchedNotEnumerated(t *testing.T) {
	columns := openSearchBrowserColumns(
		[]query.Row{{"message": "connection refused"}},
		[]opensearchinspect.Field{mapping("message", "text", true, false)},
	)
	if filter := columns[0].Filter; filter == nil || filter.Kind != query.ColumnFilterKindText {
		t.Fatalf("filter = %+v, want a text filter", filter)
	}
}

func TestOpenSearchRefusesToFilterFieldsThatMeanTwoThings(t *testing.T) {
	conflicting := mapping("status", "keyword", true, true)
	conflicting.Types = []string{"keyword", "long"}
	conflicting.Conflicting = true

	for name, fields := range map[string][]opensearchinspect.Field{
		"two mappings under one name": {conflicting},
		"not indexed at all":          {mapping("blob", "keyword", false, false)},
		"an unmapped family":          {mapping("blob", "geo_shape", true, true)},
	} {
		columns := openSearchBrowserColumns([]query.Row{{"status": "x", "blob": "y"}}, fields)
		if kinds := filterKinds(columns); len(kinds) != 0 {
			t.Errorf("%s: filter kinds = %+v, want none", name, kinds)
		}
	}
}

// openSearchBackend answers a search and a field-caps lookup, recording the
// query body it was searched with.
func openSearchBackend(t *testing.T, fieldCaps string) (*httptest.Server, *map[string]any) {
	t.Helper()
	searched := map[string]any{}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/":
			w.WriteHeader(http.StatusOK)
		case strings.HasSuffix(r.URL.Path, "/_field_caps"):
			_, _ = w.Write([]byte(fieldCaps))
		default:
			if err := json.NewDecoder(r.Body).Decode(&searched); err != nil {
				t.Errorf("decode search: %v", err)
			}
			_, _ = w.Write([]byte(`{"hits":{"total":{"value":1,"relation":"eq"},"hits":[
				{"_index":"logs-1","_id":"a","_score":1,"_source":{"service":"payments","latency_ms":12}}
			]}}`))
		}
	}))
	return server, &searched
}

func TestBrowserQueryMergesFiltersIntoTheAuthorsOpenSearchQuery(t *testing.T) {
	openSearch, searched := openSearchBackend(t, `{"fields":{
		"service":{"keyword":{"searchable":true,"aggregatable":true}},
		"latency_ms":{"long":{"searchable":true,"aggregatable":true}}
	}}`)
	defer openSearch.Close()

	handler, base := browserHandlerFor(t, models.ConnectionTypeOpenSearch, openSearch.URL)
	// No columns are echoed back: the index maps what can be narrowed and how, so
	// the console has nothing to restate.
	recorder := postBrowser(t, handler, base+"/query", `{
		"query": "{\"query\":{\"term\":{\"level\":\"error\"}}}",
		"options": {"index": "logs-*"},
		"filters": {"filter.service": "payments,!billing", "filter.latency_ms": ">=10"}
	}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("query status = %d: %s", recorder.Code, recorder.Body.String())
	}

	// The author's own clause survives as the first filter member; the selection
	// is merged beside it rather than replacing it.
	body, err := json.Marshal(*searched)
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{
		`{"term":{"level":"error"}}`,
		`{"terms":{"service":["payments"]}}`,
		`"must_not":[{"terms":{"service":["billing"]}}]`,
		`{"range":{"latency_ms":{"gte":10}}}`,
	} {
		if !strings.Contains(string(body), fragment) {
			t.Errorf("searched body %s\nis missing %s", body, fragment)
		}
	}

	var result browserQueryResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	keys := map[string]string{}
	for _, column := range result.Columns {
		if column.Filter != nil {
			keys[column.Name] = column.FilterKey + ":" + column.Filter.Kind
		}
	}
	want := map[string]string{"service": "filter.service:terms", "latency_ms": "filter.latency_ms:range"}
	for name, key := range want {
		if keys[name] != key {
			t.Errorf("column %q described as %q, want %q", name, keys[name], key)
		}
	}
}

// A filter names a field, so one the index does not map has nothing to narrow
// and is refused rather than dropped — a query that silently ignored it would
// return more rows than the console says it asked for.
func TestBrowserQueryRefusesAFilterOnAnUnmappedOpenSearchField(t *testing.T) {
	openSearch, _ := openSearchBackend(t, `{"fields":{
		"service":{"keyword":{"searchable":true,"aggregatable":true}}
	}}`)
	defer openSearch.Close()

	handler, base := browserHandlerFor(t, models.ConnectionTypeOpenSearch, openSearch.URL)
	recorder := postBrowser(t, handler, base+"/query", `{
		"query": "{\"query\":{\"match_all\":{}}}",
		"options": {"index": "logs-*"},
		"filters": {"filter.nope": "x"}
	}`)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %s", recorder.Code, recorder.Body.String())
	}
}
