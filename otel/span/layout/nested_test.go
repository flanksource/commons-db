package layout_test

import (
	"github.com/flanksource/commons-db/otel/span/layout"
	"github.com/flanksource/commons-db/query/esdsl"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// jaegerNestedDiscovery is a Jaeger span index whose tags were never flattened:
// span and process attributes live only in nested key/value lists.
func jaegerNestedDiscovery(extra map[string]string) layout.Discovery {
	fields := map[string]string{"traceID": "keyword", "spanID": "keyword", "startTimeMillis": "long", "duration": "long", "process.serviceName": "keyword", "operationName": "keyword"}
	for name, typ := range extra {
		fields[name] = typ
	}
	return layout.Discovery{
		Fields: fields,
		Nested: map[string]layout.NestedLeaf{
			"tags.key":           {Type: "keyword", Container: "tags"},
			"tags.value":         {Type: "keyword", Container: "tags"},
			"tags.type":          {Type: "keyword", Container: "tags"},
			"process.tags.key":   {Type: "keyword", Container: "process.tags"},
			"process.tags.value": {Type: "keyword", Container: "process.tags"},
		},
	}
}

var _ = Describe("Nested key/value attributes", func() {
	var definition layout.Definition
	BeforeEach(func() {
		var err error
		definition, err = layout.Discover(jaegerNestedDiscovery(nil))
		Expect(err).NotTo(HaveOccurred())
	})

	It("discovers span and resource attribute lists from nested tag mappings", func() {
		Expect(definition.Nested).To(ConsistOf(
			layout.NestedAttributes{Prefix: "attributes.", Path: "tags", Key: "tags.key", Value: "tags.value"},
			layout.NestedAttributes{Prefix: "resource.", Path: "process.tags", Key: "process.tags.key", Value: "process.tags.value"},
		))
	})

	It("resolves an attribute to the value field of the entry pinned by its key", func() {
		target, err := definition.SourceTarget("attributes.db.operation")
		Expect(err).NotTo(HaveOccurred())
		Expect(target).To(Equal(layout.Target{Field: "tags.value", Nested: "tags", Where: map[string]string{"tags.key": "db.operation"}}))
		_, err = definition.SourceField("attributes.db.operation")
		Expect(err).To(MatchError(ContainSubstring("nested")))
	})

	It("prefers a flattened tag field over the nested list when the index maps one", func() {
		flattened, err := layout.Discover(jaegerNestedDiscovery(map[string]string{"tag.http@method": "keyword"}))
		Expect(err).NotTo(HaveOccurred())
		target, err := flattened.SourceTarget("attributes.http.method")
		Expect(err).NotTo(HaveOccurred())
		Expect(target).To(Equal(layout.Target{Field: "tag.http@method"}))
	})

	It("translates attribute conditions into nested queries and back", func() {
		logical := []byte(`{"query":{"bool":{"filter":[
			{"term":{"attributes.db.operation":"SELECT"}},
			{"terms":{"resource.host.name":["a","b"]}},
			{"wildcard":{"attributes.db.name":{"value":"lab_*"}}},
			{"exists":{"field":"attributes.http.route"}}
		]}}}`)
		native, err := definition.Translate(logical, layout.ToSource)
		Expect(err).NotTo(HaveOccurred())
		Expect(native).To(MatchJSON(`{"query":{"bool":{"filter":[
			{"nested":{"path":"tags","query":{"bool":{"filter":[{"term":{"tags.key":"db.operation"}},{"term":{"tags.value":"SELECT"}}]}}}},
			{"nested":{"path":"process.tags","query":{"bool":{"filter":[{"term":{"process.tags.key":"host.name"}},{"terms":{"process.tags.value":["a","b"]}}]}}}},
			{"nested":{"path":"tags","query":{"bool":{"filter":[{"term":{"tags.key":"db.name"}},{"wildcard":{"tags.value":{"value":"lab_*"}}}]}}}},
			{"nested":{"path":"tags","query":{"bool":{"filter":[{"term":{"tags.key":"http.route"}}]}}}}
		]}}}`))
		restored, err := definition.Translate(native, layout.ToCanonical)
		Expect(err).NotTo(HaveOccurred())
		Expect(restored).To(MatchJSON(logical))
	})

	It("sorts on an attribute through the entry pinned by its key", func() {
		native, err := definition.Translate([]byte(`{"sort":[{"attributes.db.name":{"order":"desc"}}]}`), layout.ToSource)
		Expect(err).NotTo(HaveOccurred())
		Expect(native).To(MatchJSON(`{"sort":[{"tags.value":{"order":"desc","nested":{"path":"tags","filter":{"term":{"tags.key":"db.name"}}}}}]}`))
	})

	It("rejects ranges on keyword attribute values instead of comparing them lexically", func() {
		_, err := definition.Translate([]byte(`{"query":{"range":{"attributes.http.status_code":{"gte":500}}}}`), layout.ToSource)
		Expect(err).To(MatchError(And(ContainSubstring("attributes.http.status_code"), ContainSubstring("keyword"))))
	})

	It("translates structured attribute conditions into nested groups and back", func() {
		logical := esdsl.Search{Query: &esdsl.Condition{Op: esdsl.OpTerm, Field: "attributes.db.operation", Value: esdsl.Literal("SELECT")}}
		native, err := definition.TranslateSearch(logical, layout.ToSource)
		Expect(err).NotTo(HaveOccurred())
		compiled, err := esdsl.Compile(esdsl.CompileRequest{Search: native})
		Expect(err).NotTo(HaveOccurred())
		encoded, err := compiled.JSON()
		Expect(err).NotTo(HaveOccurred())
		Expect(encoded).To(MatchJSON(`{"query":{"nested":{"path":"tags","query":{"bool":{"filter":[{"term":{"tags.key":"db.operation"}},{"term":{"tags.value":"SELECT"}}]}}}}}`))
		restored, err := definition.TranslateSearch(native, layout.ToCanonical)
		Expect(err).NotTo(HaveOccurred())
		Expect(restored.Query).To(Equal(logical.Query))
	})
})
