package query

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("ClickHouse parameter normalization", func() {
	DescribeTable("renders a datetime parameter in the DateTime64 literal format",
		func(provider ProviderConfig) {
			const canonical = "2026-08-23T10:10:34.153552+03:00"
			params, err := providerTemplateParams(
				provider,
				[]ParamDef{{Name: "from", Type: ParamTypeDateTime}},
				map[string]any{"from": canonical},
			)

			Expect(err).ToNot(HaveOccurred())
			Expect(params).To(HaveKeyWithValue("from", "2026-08-23 07:10:34.153552"))
		},
		Entry("through the ClickHouse provider", ProviderConfig{Type: "clickhouse"}),
		Entry("through the generic SQL provider", ProviderConfig{
			Type: "sql", Options: map[string]any{"driver": "clickhouse"},
		}),
	)

	It("keeps the canonical datetime spelling for other providers", func() {
		const canonical = "2026-08-23T07:10:34.153552Z"
		params, err := providerTemplateParams(
			ProviderConfig{Type: "postgres"},
			[]ParamDef{{Name: "from", Type: ParamTypeDateTime}},
			map[string]any{"from": canonical},
		)

		Expect(err).ToNot(HaveOccurred())
		Expect(params).To(HaveKeyWithValue("from", canonical))
	})
})
