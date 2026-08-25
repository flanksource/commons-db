package query

import (
	"encoding/json"

	"github.com/flanksource/commons-db/context"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("hasTemplate", func() {
	It("recognises both delimiter pairs and nothing else", func() {
		Expect(hasTemplate("{{.params.country}}-api")).To(BeTrue())
		Expect(hasTemplate("$(.params.country)-api")).To(BeTrue())
		Expect(hasTemplate("process.serviceName")).To(BeFalse())
		Expect(hasTemplate("cost > $100")).To(BeFalse())
	})
})

var _ = Describe("paramRefs", func() {
	It("finds the dotted form and the index form, distinct and in order", func() {
		Expect(paramRefs(`{{.params.country}}-{{.params.env}}-{{.params.country}}`)).
			To(Equal([]string{"country", "env"}))
	})

	It("finds a name that is not a bare identifier", func() {
		Expect(paramRefs(`{{index .params "with-dash"}}`)).To(Equal([]string{"with-dash"}))
	})

	It("returns nothing for a template that references no param", func() {
		Expect(paramRefs(`{{now | date "2006-01-02"}}`)).To(BeEmpty())
	})
})

var _ = Describe("paramTemplate", func() {
	newTemplate := func(params map[string]any) *paramTemplate {
		return newParamTemplate(context.New(), params)
	}

	It("interpolates a param into a surrounding literal", func() {
		rendered, err := newTemplate(map[string]any{"country": "kenya"}).
			render("provider.options.search", "{{.params.country}}-api")
		Expect(err).ToNot(HaveOccurred())
		Expect(rendered).To(Equal("kenya-api"))
	})

	It("interpolates with the $( ) delimiters too", func() {
		rendered, err := newTemplate(map[string]any{"country": "kenya"}).
			render("query", "$(.params.country)-api")
		Expect(err).ToNot(HaveOccurred())
		Expect(rendered).To(Equal("kenya-api"))
	})

	It("returns a string with no delimiters identically", func() {
		template := newTemplate(map[string]any{"country": "kenya"})
		rendered, err := template.render("query", "select * from orders")
		Expect(err).ToNot(HaveOccurred())
		Expect(rendered).To(Equal("select * from orders"))
		Expect(template.usedParams()).To(BeNil())
	})

	It("fails naming the field and the param when the param has no value", func() {
		_, err := newTemplate(map[string]any{"env": "prod"}).
			render("provider.options.database", "{{.params.tenant}}_reporting")
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("provider.options.database"))
		Expect(err.Error()).To(ContainSubstring(`param "tenant"`))
	})

	It("reports exactly the params it consumed, sorted", func() {
		template := newTemplate(map[string]any{"country": "kenya", "env": "prod", "unused": "x"})
		_, err := template.render("query", "{{.params.env}}")
		Expect(err).ToNot(HaveOccurred())
		_, err = template.render("provider.connection", "{{.params.country}}")
		Expect(err).ToNot(HaveOccurred())
		Expect(template.usedParams()).To(Equal([]string{"country", "env"}))
	})

	It("renders only the strings of a nested options map, preserving keys and scalar types", func() {
		template := newTemplate(map[string]any{"country": "kenya", "rows": 50})
		rendered, err := template.renderOptions(map[string]any{
			"index":   "{{.params.country}}-traces-*",
			"limit":   500,
			"scroll":  true,
			"ratio":   1.5,
			"fields":  []string{"{{.params.country}}.id", "name"},
			"headers": []any{"x-tenant: {{.params.country}}", 7},
			"search": map[string]any{
				"{{.params.country}}": "left as a key",
				"size":                10,
				"term":                "{{.params.country}}-api",
			},
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(rendered).To(Equal(map[string]any{
			"index":   "kenya-traces-*",
			"limit":   500,
			"scroll":  true,
			"ratio":   1.5,
			"fields":  []string{"kenya.id", "name"},
			"headers": []any{"x-tenant: kenya", 7},
			"search": map[string]any{
				"{{.params.country}}": "left as a key",
				"size":                10,
				"term":                "kenya-api",
			},
		}))
	})

	It("returns an empty options map untouched", func() {
		rendered, err := newTemplate(nil).renderOptions(nil)
		Expect(err).ToNot(HaveOccurred())
		Expect(rendered).To(BeNil())
	})
})

var _ = Describe("RenderParamsJSON", func() {
	It("templates the strings of a document and reports the params consumed", func() {
		doc := []byte(`{"query":{"op":"term","field":"process.serviceName","value":"{{.params.country}}-api","boost":2}}`)

		rendered, referenced, err := RenderParamsJSON(context.New(), doc, map[string]any{"country": "kenya"})
		Expect(err).ToNot(HaveOccurred())
		Expect(referenced).To(Equal([]string{"country"}))

		var decoded map[string]any
		Expect(json.Unmarshal(rendered, &decoded)).To(Succeed())
		Expect(decoded["query"]).To(Equal(map[string]any{
			"op":    "term",
			"field": "process.serviceName",
			"value": "kenya-api",
			"boost": float64(2),
		}))
	})

	It("returns a document with no delimiters byte-for-byte", func() {
		doc := []byte(`{"query":{"op":"term","field":"a","value":"b"}}`)
		rendered, referenced, err := RenderParamsJSON(context.New(), doc, nil)
		Expect(err).ToNot(HaveOccurred())
		Expect(referenced).To(BeNil())
		Expect(rendered).To(Equal(doc))
	})

	It("fails when the document references a param with no value", func() {
		doc := []byte(`{"query":{"op":"term","field":"a","value":"{{.params.missing}}"}}`)
		_, _, err := RenderParamsJSON(context.New(), doc, map[string]any{"other": 1})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring(`param "missing"`))
	})
})
