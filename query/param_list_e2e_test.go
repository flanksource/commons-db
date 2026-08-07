package query_test

import (
	context "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/yaml"
)

var _ = Describe("List params end to end", func() {
	It("unmarshals a bound list param from YAML", func() {
		const spec = `
profile: logs
provider:
  type: opensearch
query: '{"query":{"match_all":{}}}'
params:
  - name: regions
    label: Regions
    type: list
    field: region.keyword
    options: [us-east, us-west, eu]
`
		var p query.Profile
		Expect(yaml.Unmarshal([]byte(spec), &p)).To(Succeed())
		Expect(p.Params).To(HaveLen(1))
		Expect(p.Params[0].Type).To(Equal(query.ParamTypeList))
		Expect(p.Params[0].Field).To(Equal("region.keyword"))
		Expect(p.Params[0].Options).To(Equal([]string{"us-east", "us-west", "eu"}))
	})

	It("routes a tri-state selection to the provider as native filter clauses", func() {
		// Registered as opensearch because tri-state is gated on a provider that
		// actually applies native filters.
		mp := &mockProvider{typ: "opensearch"}
		query.RegisterProvider(mp)

		_, err := query.Execute(context.New(), query.Profile{
			Name:     "logs",
			Provider: query.ProviderConfig{Type: "opensearch"},
			Query:    `{"query":{"match_all":{}}}`,
			Params: []query.ParamDef{
				{Name: "regions", Type: query.ParamTypeList, Field: "region.keyword"},
			},
		}, map[string]any{"regions": "us-east,us-west,!eu"})
		Expect(err).ToNot(HaveOccurred())

		// The template sees only the includes; the exclusion rides the filter.
		Expect(mp.last.Params).To(Equal(map[string]any{"regions": []string{"us-east", "us-west"}}))
		Expect(mp.last.Filters).To(Equal([]query.ColumnFilterValue{{
			Key:     "regions",
			Field:   "region.keyword",
			Kind:    query.ColumnFilterKindTerms,
			Include: []string{"us-east", "us-west"},
			Exclude: []string{"eu"},
		}}))
	})

	It("carries a column filter and a list param on the same request", func() {
		mp := &mockProvider{typ: "opensearch"}
		query.RegisterProvider(mp)

		_, err := query.Execute(context.New(), query.Profile{
			Name:     "logs",
			Provider: query.ProviderConfig{Type: "opensearch"},
			Query:    `{"query":{"match_all":{}}}`,
			Columns:  []query.ColumnDef{{Name: "service"}},
			Params: []query.ParamDef{
				{Name: "regions", Type: query.ParamTypeList, Field: "region"},
			},
		}, map[string]any{"filter.service": "api,!worker", "regions": "eu"})
		Expect(err).ToNot(HaveOccurred())

		Expect(mp.last.Filters).To(Equal([]query.ColumnFilterValue{
			{Column: "service", Key: "filter.service", Field: "service", Kind: query.ColumnFilterKindTerms,
				Include: []string{"api"}, Exclude: []string{"worker"}},
			{Key: "regions", Field: "region", Kind: query.ColumnFilterKindTerms,
				Include: []string{"eu"}, Exclude: []string{}},
		}))
	})

	It("templates a list into a query through a join", func() {
		mp := &mockProvider{typ: "list-param-template"}
		query.RegisterProvider(mp)

		_, err := query.Execute(context.New(), query.Profile{
			Name:     "accounts",
			Provider: query.ProviderConfig{Type: "list-param-template"},
			Query:    `select * from a where id in ('{{ join .params.ids "','" }}')`,
			Params:   []query.ParamDef{{Name: "ids", Type: query.ParamTypeList}},
		}, map[string]any{"ids": "A-1,A-2"})
		Expect(err).ToNot(HaveOccurred())
		Expect(mp.last.Query).To(Equal(`select * from a where id in ('A-1','A-2')`))
	})
})
