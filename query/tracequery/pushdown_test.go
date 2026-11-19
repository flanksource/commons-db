package tracequery

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/flanksource/commons-db/connection/crd"
	"github.com/flanksource/commons-db/connection/store"
	"github.com/flanksource/commons-db/models"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// jaegerCore is the mapping every Jaeger span index shares; the tag shapes
// below add where span and process tags are kept.
var jaegerCore = map[string]string{"traceID": "keyword", "spanID": "keyword", "startTimeMillis": "long", "duration": "long", "operationName": "keyword", "process": "object", "process.serviceName": "keyword"}

// Jaeger keeps tags as nested key/value lists, or with tags_as_fields as
// tag.<key> keyword fields with "." written as "@".
var (
	nestedTags    = map[string]string{"tags": "nested", "tags.key": "keyword", "tags.value": "keyword", "process.tags": "nested", "process.tags.key": "keyword", "process.tags.value": "keyword"}
	tagsAsFields  = map[string]string{"tag": "object", "process.tag": "object", "tag.oipa@plan@guid": "keyword"}
	bothTagShapes = mappingOf(nestedTags, tagsAsFields)
)

func mappingOf(shapes ...map[string]string) map[string]string {
	mapping := maps.Clone(jaegerCore)
	for _, shape := range shapes {
		maps.Copy(mapping, shape)
	}
	return mapping
}

// serveMapping answers the inspection requests a trace read makes from
// mapping: the _field_caps of every field or the named ones, and a named
// field's mapping. It reports whether r was one of them.
func serveMapping(w http.ResponseWriter, r *http.Request, mapping map[string]string) bool {
	if strings.HasSuffix(r.URL.Path, "/_field_caps") {
		names := strings.Split(r.URL.Query().Get("fields"), ",")
		if names[0] == "*" {
			names = slices.Collect(maps.Keys(mapping))
		}
		fields := map[string]any{}
		for _, name := range names {
			if typ, ok := mapping[name]; ok {
				fields[name] = map[string]any{typ: map[string]any{"searchable": true, "aggregatable": true}}
			}
		}
		Expect(json.NewEncoder(w).Encode(map[string]any{"fields": fields})).To(Succeed())
		return true
	}
	if strings.Contains(r.URL.Path, "/_mapping/field/") {
		field := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		Expect(json.NewEncoder(w).Encode(map[string]any{"traces-example": map[string]any{"mappings": map[string]any{field: map[string]any{"full_name": field, "mapping": map[string]any{field: map[string]any{"type": mapping[field]}}}}}})).To(Succeed())
		return true
	}
	return false
}

// jaegerIndex is a fake OpenSearch over Jaeger span documents mapped as
// mapping. It evaluates the bool, nested, term, terms, prefix and exists
// clauses a trace read sends, treats a range as true, and records each search
// body's query.
type jaegerIndex struct {
	mapping map[string]string
	docs    []map[string]any
	mu      sync.Mutex
	queries []any
}

func (x *jaegerIndex) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer GinkgoRecover()
	if r.Method == http.MethodHead {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if serveMapping(w, r, x.mapping) {
		return
	}
	var body map[string]any
	Expect(json.NewDecoder(r.Body).Decode(&body)).To(Succeed())
	hits := []any{}
	if body["search_after"] == nil {
		x.mu.Lock()
		x.queries = append(x.queries, body["query"])
		x.mu.Unlock()
		for _, doc := range x.docs {
			if matches(body["query"].(map[string]any), doc) {
				hits = append(hits, map[string]any{"_index": "traces-example", "_id": doc["spanID"], "_source": doc, "sort": []any{doc["startTimeMillis"], doc["spanID"]}})
			}
		}
	}
	Expect(json.NewEncoder(w).Encode(map[string]any{"took": 1, "hits": map[string]any{"total": map[string]any{"value": len(hits), "relation": "eq"}, "hits": hits}})).To(Succeed())
}

func matches(clause map[string]any, doc map[string]any) bool {
	for op, raw := range clause {
		body := raw.(map[string]any)
		switch op {
		case "bool":
			return matchesBool(body, doc)
		case "nested":
			path := body["path"].(string)
			entries, _ := lookup(doc, path).([]any)
			return slices.ContainsFunc(entries, func(entry any) bool {
				return matches(body["query"].(map[string]any), nest(path, entry))
			})
		case "exists":
			return lookup(doc, body["field"].(string)) != nil
		case "range", "match_all":
			return true
		}
		for field, want := range body {
			got := lookup(doc, field)
			switch op {
			case "term":
				return got == want
			case "terms":
				return slices.Contains(want.([]any), got)
			case "prefix":
				text, ok := got.(string)
				return ok && strings.HasPrefix(text, want.(string))
			}
		}
	}
	Fail("fake index cannot evaluate " + toJSON(clause))
	return false
}

func matchesBool(body map[string]any, doc map[string]any) bool {
	all := func(key string) bool {
		clauses, _ := body[key].([]any)
		return !slices.ContainsFunc(clauses, func(c any) bool { return !matches(c.(map[string]any), doc) })
	}
	should, _ := body["should"].([]any)
	return all("filter") && all("must") && (len(should) == 0 || slices.ContainsFunc(should, func(c any) bool { return matches(c.(map[string]any), doc) }))
}

func lookup(doc map[string]any, field string) any {
	var current any = doc
	for _, part := range strings.Split(field, ".") {
		object, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = object[part]
	}
	return current
}

func nest(path string, value any) map[string]any {
	parts := strings.Split(path, ".")
	for i := len(parts) - 1; i > 0; i-- {
		value = map[string]any{parts[i]: value}
	}
	return map[string]any{parts[0]: value}
}

func toJSON(value any) string {
	encoded, err := json.Marshal(value)
	Expect(err).NotTo(HaveOccurred())
	return string(encoded)
}

func jaegerSpan(trace, id, parent, service string, at time.Time, tags ...string) map[string]any {
	entries := []any{}
	for i := 0; i < len(tags); i += 2 {
		entries = append(entries, map[string]any{"key": tags[i], "type": "string", "value": tags[i+1]})
	}
	doc := map[string]any{"startTimeMillis": float64(at.UnixMilli()), "traceID": trace, "spanID": id, "operationName": id, "duration": float64(1000), "tags": entries, "process": map[string]any{"serviceName": service, "tags": []any{}}}
	if parent != "" {
		doc["references"] = []any{map[string]any{"refType": "CHILD_OF", "spanID": parent}}
	}
	return doc
}

func jaegerService(index *jaegerIndex) Service {
	server := httptest.NewServer(index)
	DeferCleanup(server.Close)
	catalog, err := store.New(store.Layer{Name: store.LayerCWD, Dir: GinkgoT().TempDir()})
	Expect(err).NotTo(HaveOccurred())
	search, err := crd.FromModel(models.Connection{Name: "search", Type: models.ConnectionTypeOpenSearch, URL: server.URL})
	Expect(err).NotTo(HaveOccurred())
	Expect(catalog.Save(store.LayerCWD, search)).To(Succeed())
	return Service{Connections: catalog}
}

// pushedQuery is the condition a search-phase read pushed beside its time range.
func pushedQuery(query any) string {
	filters := query.(map[string]any)["bool"].(map[string]any)["filter"].([]any)
	Expect(filters).To(HaveLen(2), toJSON(query))
	Expect(filters[1]).To(HaveKey("range"))
	return toJSON(filters[0])
}

var _ = Describe("OpenSearch trace pushdown", func() {
	const plan = "oipa.plan.guid"
	at := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
	window := Request{Source: "search", Index: "traces-*", Format: "jaeger", From: at.Add(-time.Minute).Format(time.RFC3339Nano), Until: at.Add(time.Minute).Format(time.RFC3339Nano)}
	nestedTag := func(path, inner string) string {
		return `{"nested":{"path":"` + path + `","query":{"bool":{"filter":[{"term":{"` + path + `.key":"` + plan + `"}}` + inner + `]}}}}`
	}
	either := func(span, resource string) string {
		return `{"bool":{"minimum_should_match":1,"should":[` + span + `,` + resource + `]}}`
	}

	DescribeTable("pushes an included attribute into the search-phase read through the index's tag shape", func(expr string, mapping map[string]string, expected string) {
		index := &jaegerIndex{mapping: mapping}
		request := window
		request.Attributes = Modes{expr: Include}
		_, err := jaegerService(index).ReadSpans(context.Background(), request, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(index.queries).To(HaveLen(1))
		Expect(pushedQuery(index.queries[0])).To(MatchJSON(expected))
	},
		Entry("key=value as a nested term", plan+"=X", mappingOf(nestedTags),
			either(nestedTag("tags", `,{"term":{"tags.value":"X"}}`), nestedTag("process.tags", `,{"term":{"process.tags.value":"X"}}`))),
		Entry("key=prefix* as a nested prefix", plan+"=AB*", mappingOf(nestedTags),
			either(nestedTag("tags", `,{"prefix":{"tags.value":"AB"}}`), nestedTag("process.tags", `,{"prefix":{"process.tags.value":"AB"}}`))),
		Entry("a bare key as a nested key term", plan, mappingOf(nestedTags),
			either(nestedTag("tags", ""), nestedTag("process.tags", ""))),
		Entry("key=value as a tag field term", plan+"=X", mappingOf(tagsAsFields),
			either(`{"term":{"tag.oipa@plan@guid":"X"}}`, `{"term":{"process.tag.oipa@plan@guid":"X"}}`)),
		Entry("key=prefix* as a tag field prefix", plan+"=AB*", mappingOf(tagsAsFields),
			either(`{"prefix":{"tag.oipa@plan@guid":"AB"}}`, `{"prefix":{"process.tag.oipa@plan@guid":"AB"}}`)),
		Entry("a bare key as a tag field exists", plan, mappingOf(tagsAsFields),
			either(`{"exists":{"field":"tag.oipa@plan@guid"}}`, `{"exists":{"field":"process.tag.oipa@plan@guid"}}`)),
		Entry("key=value as either shape on an index holding both", plan+"=X", bothTagShapes,
			`{"bool":{"minimum_should_match":1,"should":[{"term":{"tag.oipa@plan@guid":"X"}},`+nestedTag("tags", `,{"term":{"tags.value":"X"}}`)+
				`,{"term":{"process.tag.oipa@plan@guid":"X"}},`+nestedTag("process.tags", `,{"term":{"process.tags.value":"X"}}`)+`]}}`),
	)

	It("ORs the values of one key and leaves exclusions and inexact wildcards in memory", func() {
		index := &jaegerIndex{mapping: mappingOf(nestedTags)}
		request := window
		request.Attributes = Modes{plan + "=A": Include, plan + "=B": Include, "oipa.rule.name": Exclude, "oipa.outcome=*ed": Include}
		_, err := jaegerService(index).ReadSpans(context.Background(), request, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(index.queries).To(HaveLen(1))
		Expect(pushedQuery(index.queries[0])).To(MatchJSON(`{"bool":{"minimum_should_match":1,"should":[` +
			strings.Join([]string{
				nestedTag("tags", `,{"term":{"tags.value":"A"}}`), nestedTag("process.tags", `,{"term":{"process.tags.value":"A"}}`),
				nestedTag("tags", `,{"term":{"tags.value":"B"}}`), nestedTag("process.tags", `,{"term":{"process.tags.value":"B"}}`),
			}, ",") + `]}}`))
	})

	It("leaves the by-trace-ID read unchanged", func() {
		index := &jaegerIndex{mapping: mappingOf(nestedTags)}
		request := window
		request.From, request.Until = "", ""
		request.Attributes = Modes{plan + "=X": Include}
		_, err := jaegerService(index).ReadSpans(context.Background(), request, []string{"trace-a"})
		Expect(err).NotTo(HaveOccurred())
		Expect(toJSON(index.queries)).To(MatchJSON(`[{"terms":{"traceID":["trace-a"]}}]`))
	})

	It("finds traces whose spans meet different keys and services by narrowing one read at a time", func() {
		index := &jaegerIndex{mapping: mappingOf(nestedTags), docs: []map[string]any{
			jaegerSpan("trace-a", "a-root", "", "oipa", at, plan, "X"),
			jaegerSpan("trace-a", "a-rule", "a-root", "rules", at.Add(time.Millisecond), "oipa.rule.name", "Premium"),
			jaegerSpan("trace-a", "a-jdbc", "a-root", "db", at.Add(2*time.Millisecond)),
			jaegerSpan("trace-b", "b-root", "", "oipa", at, plan, "X"),
			jaegerSpan("trace-c", "c-root", "", "oipa", at, plan, "Y"),
			jaegerSpan("trace-c", "c-rule", "c-root", "rules", at.Add(time.Millisecond), "oipa.rule.name", "Premium"),
		}}
		request := window
		request.Attributes = Modes{plan + "=X": Include, "oipa.rule.name=Prem*": Include}
		request.Services = Modes{"rules": Include}
		result, err := jaegerService(index).Search(context.Background(), request)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Traces).To(HaveExactElements(And(HaveField("TraceID", "trace-a"), HaveField("SpanCount", 3))))
		Expect(index.queries).To(HaveLen(4))
		narrowed := index.queries[1].(map[string]any)["bool"].(map[string]any)["filter"].([]any)
		Expect(toJSON(narrowed[1])).To(MatchJSON(`{"terms":{"traceID":["trace-a","trace-b"]}}`))
		services := index.queries[2].(map[string]any)["bool"].(map[string]any)["filter"].([]any)
		Expect(toJSON(services[:2])).To(MatchJSON(`[{"terms":{"process.serviceName":["rules"]}},{"terms":{"traceID":["trace-a"]}}]`))
		Expect(toJSON(index.queries[3])).To(MatchJSON(`{"terms":{"traceID":["trace-a"]}}`))
	})
})
