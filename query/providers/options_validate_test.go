package providers_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/providers"
	"github.com/flanksource/commons-db/query/providers/sqlite"
)

var _ = Describe("exported provider options", func() {
	DescribeTable("Validate refuses what the provider would refuse",
		func(options interface{ Validate() error }, message string) {
			Expect(options.Validate()).To(MatchError(ContainSubstring(message)))
		},
		Entry("a sql type that is no SQL engine", sqlite.SQLOptions{Type: "oracle"}, `sql type "oracle"`),
		Entry("a prometheus range without a step", providers.PrometheusOptions{Range: &providers.PrometheusRange{Start: "now-1h", End: "now"}}, "start, end and step"),
		Entry("a prometheus range ending before it starts", providers.PrometheusOptions{Range: &providers.PrometheusRange{Start: "now", End: "now-1h", Step: "1m"}}, "after start"),
		Entry("a prometheus language it cannot run", providers.PrometheusOptions{Language: "logql"}, `unsupported prometheus language "logql"`),
		Entry("an http method the provider does not send", providers.HTTPOptions{Method: "FETCH"}, "unsupported http method"),
		Entry("an opensearch limit that is no number", providers.OpenSearchOptions{Limit: "many"}, "invalid opensearch limit"),
		Entry("an opensearch tail poll that never waits", providers.OpenSearchOptions{TailPoll: "0s"}, "tailPoll"),
		Entry("an opentelemetry format it cannot read", providers.OpenTelemetryOptions{Format: "zipkin"}, `unsupported opentelemetry format "zipkin"`),
	)

	DescribeTable("Validate accepts what the provider reads",
		func(options interface{ Validate() error }) {
			Expect(options.Validate()).To(Succeed())
		},
		Entry("a sql_server override", sqlite.SQLOptions{Type: "sql_server"}),
		Entry("a prometheus range", providers.PrometheusOptions{Range: &providers.PrometheusRange{Start: "now-1h", End: "now", Step: "15s"}}),
		Entry("prometheus metric SQL", providers.PrometheusOptions{Language: "sql"}),
		Entry("explicit promql", providers.PrometheusOptions{Language: "promql"}),
		Entry("a lowercase http method", providers.HTTPOptions{Method: "post"}),
		Entry("opentelemetry defaults", providers.OpenTelemetryOptions{}),
	)

	It("refuses invalid options before the provider connects", func() {
		_, err := query.Execute(context.New(), query.Profile{
			Name:     "bad-method",
			Provider: query.ProviderConfig{Type: "http", Options: map[string]any{"url": "http://127.0.0.1:1", "method": "FETCH"}},
			Query:    "/",
		})
		Expect(err).To(MatchError(ContainSubstring("unsupported http method: FETCH")))
	})
})
