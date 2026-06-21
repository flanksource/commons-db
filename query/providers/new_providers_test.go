package providers_test

import (
	"net/http"

	context "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("postgrest provider", func() {
	It("returns rows from a PostgREST JSON array response", func() {
		srv := jsonServer(http.StatusOK, `[{"id":1,"name":"alpha"},{"id":2,"name":"beta"}]`)
		defer srv.Close()

		result, err := query.Execute(context.New(), query.Profile{
			Name:     "pgrst",
			Provider: query.ProviderConfig{Type: "postgrest"},
			Query:    srv.URL, // full URL acts as the resource endpoint
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Rows).To(HaveLen(2))
		Expect(result.Rows[0]).To(HaveKeyWithValue("name", "alpha"))
	})
})

var _ = Describe("loki provider", func() {
	const lokiResponse = `{"status":"success","data":{"resultType":"streams","result":[` +
		`{"stream":{"app":"checkout"},"values":[["1700000000000000000","payment failed"]]}]}}`

	It("returns one row per log line with labels", func() {
		srv := jsonServer(http.StatusOK, lokiResponse)
		defer srv.Close()

		result, err := query.Execute(context.New(), query.Profile{
			Name: "loki",
			Provider: query.ProviderConfig{
				Type:    "loki",
				Options: map[string]any{"url": srv.URL},
			},
			Query: `{app="checkout"}`,
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Rows).To(HaveLen(1))
		Expect(result.Rows[0]).To(HaveKeyWithValue("message", "payment failed"))
		Expect(result.Rows[0]).To(HaveKeyWithValue("app", "checkout"))
	})
})

var _ = Describe("clickhouse provider registration", func() {
	It("registers per-engine SQL aliases", func() {
		for _, typ := range []string{"sql", "postgres", "mysql", "sqlserver", "clickhouse"} {
			_, err := query.GetProvider(typ)
			Expect(err).ToNot(HaveOccurred(), "provider %q should be registered", typ)
		}
	})
})
