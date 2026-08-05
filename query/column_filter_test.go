package query_test

import (
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("OpenSearch column filters", func() {
	It("maps direct and simple CEL columns while requiring an override for complex CEL", func() {
		profile := query.Profile{
			Provider: query.ProviderConfig{
				Type: "opentelemetry",
				Options: map[string]any{
					"serviceField": "process.serviceName",
				},
			},
			Columns: []query.ColumnDef{
				{Name: "service"},
				{Name: "service_name"},
				{Name: "method", CEL: `span["attributes.http.method"]`},
				{
					Name: "status", CEL: `jsonpath("$.status", row.payload)`,
					Filter: &query.ColumnFilterDef{Field: "attributes.http.status_code"},
				},
				{Name: "payload.user", CEL: `jsonpath("$.user", row.payload)`},
			},
		}

		bindings, err := profile.ColumnFilterBindings()
		Expect(err).ToNot(HaveOccurred())
		Expect(bindings).To(Equal([]query.ColumnFilterBinding{
			{Column: "service", Key: "filter.service", Field: "process.serviceName", Label: "service"},
			{Column: "service_name", Key: "filter.service_name", Field: "process.serviceName", Label: "service_name"},
			{Column: "method", Key: "filter.method", Field: "attributes.http.method", Label: "method"},
			{Column: "status", Key: "filter.status", Field: "attributes.http.status_code", Label: "status"},
		}))
	})

	It("keeps renamed column filters bound to the provider field", func() {
		profile := query.Profile{
			Provider: query.ProviderConfig{Type: "opensearch"},
			Columns: []query.ColumnDef{{
				Name: "service", Source: "service.name", Type: query.ColumnTypeString,
			}},
		}

		bindings, err := profile.ColumnFilterBindings()
		Expect(err).ToNot(HaveOccurred())
		Expect(bindings).To(Equal([]query.ColumnFilterBinding{{
			Column: "service", Key: "filter.service", Field: "service.name", Label: "service",
		}}))
	})

	It("does not advertise native column filters for non-OpenSearch providers", func() {
		bindings, err := (query.Profile{
			Provider: query.ProviderConfig{Type: "postgres"},
			Columns:  []query.ColumnDef{{Name: "status"}},
		}).ColumnFilterBindings()
		Expect(err).ToNot(HaveOccurred())
		Expect(bindings).To(BeEmpty())
	})

	It("rejects an explicit filter without a backend field", func() {
		err := (query.Profile{
			Name:     "invalid",
			Provider: query.ProviderConfig{Type: "opensearch"},
			Columns:  []query.ColumnDef{{Name: "status", Filter: &query.ColumnFilterDef{}}},
		}).Validate()
		Expect(err).To(MatchError(ContainSubstring("filter field is required")))
	})

	It("rejects a declared parameter that conflicts with a native column filter", func() {
		err := (query.Profile{
			Name:     "conflict",
			Provider: query.ProviderConfig{Type: "opensearch"},
			Params:   []query.ParamDef{{Name: "filter.status"}},
			Columns:  []query.ColumnDef{{Name: "status"}},
		}).Validate()
		Expect(err).To(MatchError(ContainSubstring("conflicts with native column filter")))
	})
})
