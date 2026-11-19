package tracequery

import (
	"cmp"
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
	"github.com/flanksource/commons-db/types"
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
// mapping. It evaluates the bool, nested, term, terms, prefix, exists and
// numeric range clauses a trace read sends, and records each search body's
// query.
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
				hits = append(hits, map[string]any{"_index": "traces-example", "_id": doc["spanID"], "_source": doc, "sort": []any{cmp.Or(doc["startTimeMillis"], doc["startTimeUnixNano"]), doc["spanID"]}})
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
		case "range":
			return matchesRange(body, doc)
		case "match_all":
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
	mustNot, _ := body["must_not"].([]any)
	return all("filter") && all("must") && !slices.ContainsFunc(mustNot, func(c any) bool { return matches(c.(map[string]any), doc) }) &&
		(len(should) == 0 || slices.ContainsFunc(should, func(c any) bool { return matches(c.(map[string]any), doc) }))
}

// matchesRange bounds a numeric time field by the epoch values a read encodes.
func matchesRange(body map[string]any, doc map[string]any) bool {
	for field, raw := range body {
		got, ok := lookup(doc, field).(float64)
		if !ok {
			return false
		}
		for bound, value := range raw.(map[string]any) {
			want, numeric := value.(float64)
			if !numeric {
				continue
			}
			if !map[string]bool{"gte": got >= want, "gt": got > want, "lte": got <= want, "lt": got < want}[bound] {
				return false
			}
		}
	}
	return true
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

// jaegerService searches index through an OpenSearch connection holding
// properties.
func jaegerService(index *jaegerIndex, properties types.JSONStringMap) Service {
	server := httptest.NewServer(index)
	DeferCleanup(server.Close)
	catalog, err := store.New(store.Layer{Name: store.LayerCWD, Dir: GinkgoT().TempDir()})
	Expect(err).NotTo(HaveOccurred())
	search, err := crd.FromModel(models.Connection{Name: "search", Type: models.ConnectionTypeOpenSearch, URL: server.URL, Properties: properties})
	Expect(err).NotTo(HaveOccurred())
	Expect(catalog.Save(store.LayerCWD, search)).To(Succeed())
	return Service{Connections: catalog}
}

// narrowedQuery is the condition a narrowing read pushed beside the trace IDs
// it asks of, which it checks are ids.
func narrowedQuery(query any, ids ...string) string {
	filters := query.(map[string]any)["bool"].(map[string]any)["filter"].([]any)
	Expect(filters).To(HaveLen(2), toJSON(query))
	Expect(toJSON(filters[1])).To(MatchJSON(toJSON(map[string]any{"terms": map[string]any{"traceID": ids}})))
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

	DescribeTable("pushes an included attribute into the read narrowing the window's traces through the index's tag shape", func(expr string, mapping map[string]string, expected string) {
		index := &jaegerIndex{mapping: mapping, docs: []map[string]any{jaegerSpan("trace-a", "a-root", "", "oipa", at, plan, "X")}}
		request := window
		request.Attributes = Modes{expr: Include}
		_, err := jaegerService(index, nil).Search(context.Background(), request)
		Expect(err).NotTo(HaveOccurred())
		Expect(len(index.queries)).To(BeNumerically(">=", 2))
		Expect(narrowedQuery(index.queries[1], "trace-a")).To(MatchJSON(expected))
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
		index := &jaegerIndex{mapping: mappingOf(nestedTags), docs: []map[string]any{jaegerSpan("trace-a", "a-root", "", "oipa", at, plan, "A")}}
		request := window
		request.Attributes = Modes{plan + "=A": Include, plan + "=B": Include, "oipa.rule.name": Exclude, "oipa.outcome=*ed": Include}
		_, err := jaegerService(index, nil).Search(context.Background(), request)
		Expect(err).NotTo(HaveOccurred())
		Expect(index.queries).To(HaveLen(3))
		Expect(narrowedQuery(index.queries[1], "trace-a")).To(MatchJSON(`{"bool":{"minimum_should_match":1,"should":[` +
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
		_, err := jaegerService(index, nil).ReadSpans(context.Background(), request, []string{"trace-a"})
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
		result, err := jaegerService(index, nil).Search(context.Background(), request)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Traces).To(HaveExactElements(And(HaveField("TraceID", "trace-a"), HaveField("SpanCount", 3))))
		Expect(result.Services).To(Equal([]string{"db", "oipa", "rules"}))
		// The window read, one narrowing read per key and the services, the
		// full read of the one trace left, then the services the rest add.
		Expect(index.queries).To(HaveLen(6))
		Expect(toJSON(index.queries[0])).To(And(ContainSubstring(`"range"`), Not(ContainSubstring(`"terms"`))))
		narrowedQuery(index.queries[1], "trace-a", "trace-b", "trace-c")
		narrowedQuery(index.queries[2], "trace-a", "trace-b")
		Expect(narrowedQuery(index.queries[3], "trace-a")).To(MatchJSON(`{"terms":{"process.serviceName":["rules"]}}`))
		Expect(toJSON(index.queries[4])).To(MatchJSON(`{"terms":{"traceID":["trace-a"]}}`))
		Expect(toJSON(index.queries[5])).To(MatchJSON(`{"bool":{"filter":[{"terms":{"traceID":["trace-b","trace-c"]}}],"must_not":[{"terms":{"process.serviceName":["db","oipa","rules"]}}]}}`))
	})

	It("keeps a trace in the window whose filter span starts before it, and facets every trace in the window", func() {
		index := &jaegerIndex{mapping: mappingOf(nestedTags), docs: []map[string]any{
			jaegerSpan("trace-x", "x-root", "", "checkout", at.Add(-2*time.Minute)),
			jaegerSpan("trace-x", "x-db", "x-root", "db", at),
			jaegerSpan("trace-y", "y-root", "", "inventory", at),
			jaegerSpan("trace-z", "z-root", "", "payments", at),
			jaegerSpan("trace-z", "z-bill", "z-root", "billing", at.Add(2*time.Minute)),
		}}
		request := window
		request.Services = Modes{"checkout": Include}
		result, err := jaegerService(index, nil).Search(context.Background(), request)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Traces).To(HaveExactElements(And(HaveField("TraceID", "trace-x"), HaveField("SpanCount", 2))))
		Expect(result.Services).To(Equal([]string{"billing", "checkout", "db", "inventory", "payments"}))
	})

	It("keeps a filter on the service.name normalization derives from the span's service in memory", func() {
		index := &jaegerIndex{mapping: mappingOf(nestedTags), docs: []map[string]any{
			jaegerSpan("trace-a", "a-root", "", "checkout", at),
			jaegerSpan("trace-b", "b-root", "", "inventory", at),
		}}
		request := window
		request.Attributes = Modes{"service.name=checkout": Include}
		result, err := jaegerService(index, nil).Search(context.Background(), request)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Traces).To(HaveExactElements(HaveField("TraceID", "trace-a")))
		// The window read and the full read of both traces: nothing narrowed.
		Expect(index.queries).To(HaveLen(2))
	})

	It("reports the service the connection's span layout names, which a service filter matches", func() {
		mapping := mappingOf(nestedTags)
		mapping["custom_service"] = "keyword"
		span := jaegerSpan("trace-s", "s-root", "", "jaeger-service", at)
		span["custom_service"] = "checkout"
		index := &jaegerIndex{mapping: mapping, docs: []map[string]any{span}}
		request := window
		request.Services = Modes{"checkout": Include}
		result, err := jaegerService(index, types.JSONStringMap{"span_layout": `{"fields":{"service":{"source":"custom_service"}}}`}).Search(context.Background(), request)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Traces).To(HaveExactElements(And(HaveField("TraceID", "trace-s"), HaveField("Service", "checkout"))))
		Expect(result.Services).To(Equal([]string{"checkout"}))
	})

	It("bounds a window on a numeric timestamp in its source unit", func() {
		mapping := map[string]string{"traceID": "keyword", "spanID": "keyword", "startTimeUnixNano": "long", "durationNano": "long", "operationName": "keyword", "process": "object", "process.serviceName": "keyword"}
		span := func(trace string, started time.Time) map[string]any {
			return map[string]any{"traceID": trace, "spanID": trace + "-root", "startTimeUnixNano": float64(started.UnixNano()), "durationNano": float64(time.Millisecond), "operationName": "GET /", "process": map[string]any{"serviceName": "web"}}
		}
		index := &jaegerIndex{mapping: mapping, docs: []map[string]any{span("trace-in", at), span("trace-before", at.Add(-2*time.Minute))}}
		request := window
		request.Format = ""
		result, err := jaegerService(index, nil).Search(context.Background(), request)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Traces).To(HaveExactElements(HaveField("TraceID", "trace-in")))
	})

	It("reads spans whose trace ID the connection's span layout names", func() {
		mapping := mappingOf(nestedTags)
		delete(mapping, "traceID")
		mapping["custom_trace_id"] = "keyword"
		span := jaegerSpan("trace-k", "k-root", "", "web", at)
		span["custom_trace_id"] = span["traceID"]
		delete(span, "traceID")
		index := &jaegerIndex{mapping: mapping, docs: []map[string]any{span}}
		service := jaegerService(index, types.JSONStringMap{"span_layout": `{"fields":{"trace_id":{"source":"custom_trace_id"}}}`})
		result, err := service.Search(context.Background(), window)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Traces).To(HaveExactElements(HaveField("TraceID", "trace-k")))
		detail, err := service.Trace(context.Background(), window, "trace-k")
		Expect(err).NotTo(HaveOccurred())
		Expect(detail.Spans).To(HaveExactElements(HaveField("SpanID", "k-root")))
	})
})
