package providers_test

import (
	"net/http"
	"net/http/httptest"

	context "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func jsonServer(status int, body string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

var _ = Describe("http provider", func() {
	It("uses an inline base URL from provider options", func() {
		srv := jsonServer(http.StatusOK, `[{"source":"options"}]`)
		defer srv.Close()

		result, err := query.Execute(context.New(), query.Profile{
			Name: "http-options-url",
			Provider: query.ProviderConfig{
				Type:    "http",
				Options: map[string]any{"url": srv.URL},
			},
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Rows).To(ConsistOf(query.Row{"source": "options"}))
	})

	It("returns one row per element of a JSON array response", func() {
		srv := jsonServer(http.StatusOK, `[{"id":1,"name":"a"},{"id":2,"name":"b"}]`)
		defer srv.Close()

		result, err := query.Execute(context.New(), query.Profile{
			Name:     "http-array",
			Provider: query.ProviderConfig{Type: "http"},
			Query:    srv.URL,
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Rows).To(HaveLen(2))
		Expect(result.Rows[0]).To(HaveKeyWithValue("name", "a"))
		Expect(result.Rows[1]).To(HaveKeyWithValue("name", "b"))
	})

	It("extracts an inner array via jsonpath", func() {
		srv := jsonServer(http.StatusOK, `{"Traces":[{"x":1},{"x":2},{"x":3}],"total":3}`)
		defer srv.Close()

		result, err := query.Execute(context.New(), query.Profile{
			Name: "http-jsonpath",
			Provider: query.ProviderConfig{
				Type:    "http",
				Options: map[string]any{"jsonpath": "$.Traces"},
			},
			Query: srv.URL,
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Rows).To(HaveLen(3))
	})

	It("fails loudly on a non-2xx response", func() {
		srv := jsonServer(http.StatusInternalServerError, `{"error":"boom"}`)
		defer srv.Close()

		_, err := query.Execute(context.New(), query.Profile{
			Name:     "http-error",
			Provider: query.ProviderConfig{Type: "http"},
			Query:    srv.URL,
		})
		Expect(err).To(MatchError(ContainSubstring("status 500")))
	})
})

var _ = Describe("prometheus provider", func() {
	const vectorResponse = `{"status":"success","data":{"resultType":"vector","result":[` +
		`{"metric":{"__name__":"up","instance":"a"},"value":[1700000000,"1"]},` +
		`{"metric":{"__name__":"up","instance":"b"},"value":[1700000000,"0"]}]}}`

	It("returns one row per vector sample with the value", func() {
		srv := jsonServer(http.StatusOK, vectorResponse)
		defer srv.Close()

		result, err := query.Execute(context.New(), query.Profile{
			Name: "prom",
			Provider: query.ProviderConfig{
				Type:    "prometheus",
				Options: map[string]any{"url": srv.URL},
			},
			Query: "up",
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Rows).To(HaveLen(2))
		Expect(result.Rows[0]).To(HaveKeyWithValue("value", float64(1)))
		Expect(result.Rows[0]).To(HaveKeyWithValue("instance", "a"))
	})

	It("restricts labels via selectLabels", func() {
		srv := jsonServer(http.StatusOK, vectorResponse)
		defer srv.Close()

		result, err := query.Execute(context.New(), query.Profile{
			Name: "prom-select",
			Provider: query.ProviderConfig{
				Type:    "prometheus",
				Options: map[string]any{"url": srv.URL, "selectLabels": []string{"instance"}},
			},
			Query: "up",
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Rows[0]).To(HaveKey("instance"))
		Expect(result.Rows[0]).ToNot(HaveKey("__name__"))
	})
})
