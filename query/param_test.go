package query_test

import (
	context "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/yaml"
)

var _ = Describe("Profile params", func() {
	Describe("YAML", func() {
		It("unmarshals declared params", func() {
			const spec = `
profile: activities
provider:
  type: sql
query: "select * from a where region = '{{.params.region}}'"
params:
  - name: region
    label: Region
    type: enum
    options: [US, EU]
    required: true
  - name: limit
    type: number
    default: 50
`
			var p query.Profile
			Expect(yaml.Unmarshal([]byte(spec), &p)).To(Succeed())
			Expect(p.Params).To(HaveLen(2))
			Expect(p.Params[0].Name).To(Equal("region"))
			Expect(p.Params[0].Type).To(Equal(query.ParamTypeEnum))
			Expect(p.Params[0].Options).To(Equal([]string{"US", "EU"}))
			Expect(p.Params[0].Required).To(BeTrue())
			Expect(p.Params[1].Default).To(BeEquivalentTo(50))
		})
	})

	Describe("Execute injects params into the query", func() {
		It("templates a supplied value into the rendered query", func() {
			mp := &mockProvider{typ: "param-render"}
			query.RegisterProvider(mp)

			_, err := query.Execute(context.New(), query.Profile{
				Name:     "p",
				Provider: query.ProviderConfig{Type: "param-render"},
				Query:    "select * from a where region = '{{.params.region}}'",
				Params:   []query.ParamDef{{Name: "region", Type: query.ParamTypeEnum, Options: []string{"US", "EU"}}},
			}, map[string]any{"region": "EU"})

			Expect(err).ToNot(HaveOccurred())
			Expect(mp.last.Query).To(Equal("select * from a where region = 'EU'"))
		})

		It("falls back to the declared default when no value is supplied", func() {
			mp := &mockProvider{typ: "param-default"}
			query.RegisterProvider(mp)

			_, err := query.Execute(context.New(), query.Profile{
				Name:     "p",
				Provider: query.ProviderConfig{Type: "param-default"},
				Query:    "limit {{.params.limit}}",
				Params:   []query.ParamDef{{Name: "limit", Type: query.ParamTypeNumber, Default: 50}},
			})

			Expect(err).ToNot(HaveOccurred())
			Expect(mp.last.Query).To(Equal("limit 50"))
		})

		It("applies the per-param template rewrite", func() {
			mp := &mockProvider{typ: "param-template"}
			query.RegisterProvider(mp)

			_, err := query.Execute(context.New(), query.Profile{
				Name:     "p",
				Provider: query.ProviderConfig{Type: "param-template"},
				Query:    "ns {{.params.ns}}",
				Params:   []query.ParamDef{{Name: "ns", Template: "{value}-api"}},
			}, map[string]any{"ns": "zimbabwe"})

			Expect(err).ToNot(HaveOccurred())
			Expect(mp.last.Query).To(Equal("ns zimbabwe-api"))
		})

		It("leaves a plain query untouched when no delimiters are present", func() {
			mp := &mockProvider{typ: "param-plain"}
			query.RegisterProvider(mp)

			_, err := query.Execute(context.New(), query.Profile{
				Name:     "p",
				Provider: query.ProviderConfig{Type: "param-plain"},
				Query:    "select 1",
			})

			Expect(err).ToNot(HaveOccurred())
			Expect(mp.last.Query).To(Equal("select 1"))
		})
	})

	Describe("validation", func() {
		It("fails when a required param is missing", func() {
			query.RegisterProvider(&mockProvider{typ: "param-required"})

			_, err := query.Execute(context.New(), query.Profile{
				Name:     "p",
				Provider: query.ProviderConfig{Type: "param-required"},
				Params:   []query.ParamDef{{Name: "region", Required: true}},
			})

			Expect(err).To(MatchError(ContainSubstring(`param "region" is required`)))
		})

		It("rejects a value outside the declared options", func() {
			query.RegisterProvider(&mockProvider{typ: "param-enum"})

			_, err := query.Execute(context.New(), query.Profile{
				Name:     "p",
				Provider: query.ProviderConfig{Type: "param-enum"},
				Params:   []query.ParamDef{{Name: "region", Type: query.ParamTypeEnum, Options: []string{"US", "EU"}}},
			}, map[string]any{"region": "MARS"})

			Expect(err).To(MatchError(ContainSubstring("not one of the allowed options")))
		})

		It("rejects a non-numeric value for a number param", func() {
			query.RegisterProvider(&mockProvider{typ: "param-number"})

			_, err := query.Execute(context.New(), query.Profile{
				Name:     "p",
				Provider: query.ProviderConfig{Type: "param-number"},
				Params:   []query.ParamDef{{Name: "limit", Type: query.ParamTypeNumber}},
			}, map[string]any{"limit": "abc"})

			Expect(err).To(MatchError(ContainSubstring("is not a number")))
		})
	})
})
