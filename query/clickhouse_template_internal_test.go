package query

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/context"
)

var _ = Describe("ClickHouse parameter templating", func() {
	DescribeTable("renders a datetime parameter in the DateTime64 literal format",
		func(provider ProviderConfig) {
			const canonical = "2026-08-23T10:10:34.153552+03:00"
			request, err := buildProviderRequest(
				context.New(),
				provider,
				"SELECT * FROM telemetry.k8s_logs WHERE timestamp > '{{.params.from}}'",
				[]ParamDef{{Name: "from", Type: ParamTypeDateTime}},
				map[string]any{"from": canonical},
			)

			Expect(err).ToNot(HaveOccurred())
			Expect(request.Query).To(Equal(
				"SELECT * FROM telemetry.k8s_logs WHERE timestamp > '2026-08-23 07:10:34.153552'",
			))
			Expect(request.Params).To(HaveKeyWithValue("from", canonical))
		},
		Entry("through the ClickHouse provider", ProviderConfig{Type: "clickhouse"}),
		Entry("through the generic SQL provider", ProviderConfig{
			Type: "sql", Options: map[string]any{"driver": "clickhouse"},
		}),
	)

	It("keeps the canonical datetime spelling for other providers", func() {
		const canonical = "2026-08-23T07:10:34.153552Z"
		request, err := buildProviderRequest(
			context.New(),
			ProviderConfig{Type: "postgres"},
			"SELECT * FROM logs WHERE timestamp > '{{.params.from}}'",
			[]ParamDef{{Name: "from", Type: ParamTypeDateTime}},
			map[string]any{"from": canonical},
		)

		Expect(err).ToNot(HaveOccurred())
		Expect(request.Query).To(ContainSubstring("'" + canonical + "'"))
	})
})
