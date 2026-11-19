package layout_test

import (
	"github.com/flanksource/commons-db/otel/span/layout"
	"github.com/flanksource/commons-db/query/esdsl"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// jaegerFlattenedFields is a Jaeger span index written with tags_as_fields:
// span and process tags are tag.<key> / process.tag.<key> keyword fields
// under plain objects, with "." in a key replaced by "@".
func jaegerFlattenedFields(extra map[string]string) map[string]string {
	fields := map[string]string{"traceID": "keyword", "spanID": "keyword", "startTimeMillis": "long", "duration": "long", "process": "object", "process.serviceName": "keyword", "operationName": "keyword", "tag": "object", "process.tag": "object"}
	for name, typ := range extra {
		fields[name] = typ
	}
	return fields
}

var (
	spanTagFields    = layout.FlattenedAttributes{Prefix: "attributes.", FieldPrefix: "tag.", Separator: "@"}
	processTagFields = layout.FlattenedAttributes{Prefix: "resource.", FieldPrefix: "process.tag.", Separator: "@"}
)

func compiledCondition(condition esdsl.Condition) string {
	compiled, err := esdsl.Compile(esdsl.CompileRequest{Search: esdsl.Search{Query: &condition}})
	Expect(err).NotTo(HaveOccurred())
	encoded, err := compiled.JSON()
	Expect(err).NotTo(HaveOccurred())
	return string(encoded)
}

var _ = Describe("Flattened tag-field attributes", func() {
	It("discovers tag and process.tag objects as flattened attribute rules and resolves unmapped keys through them", func() {
		definition, err := layout.Discover(layout.Discovery{Fields: jaegerFlattenedFields(nil)})
		Expect(err).NotTo(HaveOccurred())
		Expect(definition.Flattened).To(ConsistOf(spanTagFields, processTagFields))
		Expect(definition.Nested).To(BeEmpty())
		targets, err := definition.SourceTargets("attributes.oipa.plan.guid")
		Expect(err).NotTo(HaveOccurred())
		Expect(targets).To(Equal([]layout.Target{{Field: "tag.oipa@plan@guid"}}))
		field, err := definition.SourceField("resource.host.name")
		Expect(err).NotTo(HaveOccurred())
		Expect(field).To(Equal("process.tag.host@name"))
	})

	It("keeps a nested-only index free of flattened rules", func() {
		definition, err := layout.Discover(jaegerNestedDiscovery(nil))
		Expect(err).NotTo(HaveOccurred())
		Expect(definition.Flattened).To(BeEmpty())
	})

	It("resolves an index holding both shapes to the flattened field and the nested entry", func() {
		discovery := jaegerNestedDiscovery(nil)
		discovery.Fields = jaegerFlattenedFields(discovery.Fields)
		definition, err := layout.Discover(discovery)
		Expect(err).NotTo(HaveOccurred())
		targets, err := definition.SourceTargets("attributes.db.name")
		Expect(err).NotTo(HaveOccurred())
		Expect(targets).To(Equal([]layout.Target{{Field: "tag.db@name"}, {Field: "tags.value", Nested: "tags", Where: map[string]string{"tags.key": "db.name"}}}))
	})

	It("applies a connection override to the flattened rule of its prefix", func() {
		custom := layout.FlattenedAttributes{Prefix: "attributes.", FieldPrefix: "tag.", Separator: "_"}
		definition, err := layout.Discover(layout.Discovery{Fields: jaegerFlattenedFields(nil), FlattenedOverrides: []layout.FlattenedAttributes{custom}})
		Expect(err).NotTo(HaveOccurred())
		Expect(definition.Flattened).To(ConsistOf(custom, processTagFields))
		field, err := definition.SourceField("attributes.http.method")
		Expect(err).NotTo(HaveOccurred())
		Expect(field).To(Equal("tag.http_method"))
	})

	DescribeTable("rejects a flattened override that does not fit the index",
		func(override layout.FlattenedAttributes, message string) {
			_, err := layout.Discover(layout.Discovery{Fields: jaegerFlattenedFields(nil), FlattenedOverrides: []layout.FlattenedAttributes{override}})
			Expect(err).To(MatchError(ContainSubstring(message)))
		},
		Entry("an unknown prefix", layout.FlattenedAttributes{Prefix: "labels.", FieldPrefix: "tag.", Separator: "@"}, "prefix"),
		Entry("a field prefix the index does not map", layout.FlattenedAttributes{Prefix: "attributes.", FieldPrefix: "span.tag.", Separator: "@"}, "span.tag."),
		Entry("no separator", layout.FlattenedAttributes{Prefix: "attributes.", FieldPrefix: "tag."}, "separator"),
	)

	Describe("AttributeCondition", func() {
		const plan = "oipa.plan.guid"
		nested := func(path, inner string) string {
			return `{"nested":{"path":"` + path + `","query":{"bool":{"filter":[{"term":{"` + path + `.key":"` + plan + `"}}` + inner + `]}}}}`
		}

		It("matches a span or resource attribute through the nested lists", func() {
			definition, err := layout.Discover(jaegerNestedDiscovery(nil))
			Expect(err).NotTo(HaveOccurred())
			condition, err := definition.AttributeCondition(plan, esdsl.OpTerms, "A", "B")
			Expect(err).NotTo(HaveOccurred())
			Expect(compiledCondition(condition)).To(MatchJSON(`{"query":{"bool":{"minimum_should_match":1,"should":[` +
				nested("tags", `,{"terms":{"tags.value":["A","B"]}}`) + `,` + nested("process.tags", `,{"terms":{"process.tags.value":["A","B"]}}`) + `]}}}`))
		})

		It("matches a span or resource attribute through the flattened tag fields", func() {
			definition, err := layout.Discover(layout.Discovery{Fields: jaegerFlattenedFields(nil)})
			Expect(err).NotTo(HaveOccurred())
			condition, err := definition.AttributeCondition(plan, esdsl.OpPrefix, "AB")
			Expect(err).NotTo(HaveOccurred())
			Expect(compiledCondition(condition)).To(MatchJSON(`{"query":{"bool":{"minimum_should_match":1,"should":[{"prefix":{"tag.oipa@plan@guid":"AB"}},{"prefix":{"process.tag.oipa@plan@guid":"AB"}}]}}}`))
		})

		It("refuses an exact condition on an attribute mapped as a non-keyword field", func() {
			definition, err := layout.Discover(layout.Discovery{Fields: jaegerFlattenedFields(map[string]string{"tag.http@status_code": "long"})})
			Expect(err).NotTo(HaveOccurred())
			_, err = definition.AttributeCondition("http.status_code", esdsl.OpPrefix, "5")
			Expect(err).To(MatchError(And(ContainSubstring("attributes.http.status_code"), ContainSubstring("long"))))
		})

		It("refuses an attribute no source of the layout holds", func() {
			definition, err := layout.Discover(layout.Discovery{Fields: map[string]string{"traceID": "keyword", "spanID": "keyword", "startTimeMillis": "long", "duration": "long"}})
			Expect(err).NotTo(HaveOccurred())
			_, err = definition.AttributeCondition(plan, esdsl.OpExists)
			Expect(err).To(MatchError(ContainSubstring(plan)))
		})
	})
})
