package providers_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"

	context "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("opensearch provider", func() {
	It("uses a regular size-limited search for bounded page reads", func() {
		var usedScroll bool
		var requestedSize string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodHead {
				w.WriteHeader(http.StatusOK)
				return
			}
			usedScroll = r.URL.Query().Has("scroll")
			requestedSize = r.URL.Query().Get("size")
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"took":1,"timed_out":false,"hits":{"total":{"value":1,"relation":"eq"},"hits":[{"_index":"logs","_id":"one","_source":{"message":"hello"}}]}}`)
		}))
		defer srv.Close()

		rows, err := query.ExecuteRowsBounded(context.New(), query.Profile{
			Name: "bounded-opensearch",
			Provider: query.ProviderConfig{
				Type:    "opensearch",
				Options: map[string]any{"address": srv.URL, "index": "logs"},
			},
			Query: `{"query":{"match_all":{}}}`,
		}, 101)
		Expect(err).ToNot(HaveOccurred())
		defer rows.Close()
		Expect(rows.Next()).To(BeTrue())
		Expect(rows.Row()).To(HaveKeyWithValue("message", "hello"))
		Expect(usedScroll).To(BeFalse())
		Expect(requestedSize).To(Equal("101"))
	})

	It("clears an unbounded scroll when iteration stops early", func() {
		var clearedScroll bool
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodHead {
				w.WriteHeader(http.StatusOK)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			if strings.Contains(r.URL.Path, "/_search/scroll") {
				clearedScroll = true
				_, _ = fmt.Fprint(w, `{"succeeded":true,"num_freed":1}`)
				return
			}
			_, _ = fmt.Fprint(w, `{"_scroll_id":"scroll-1","took":1,"timed_out":false,"hits":{"total":{"value":1,"relation":"eq"},"hits":[{"_index":"logs","_id":"one","_source":{"message":"hello"}}]}}`)
		}))
		defer srv.Close()

		rows, err := query.ExecuteRows(context.New(), query.Profile{
			Name: "scrolling-opensearch",
			Provider: query.ProviderConfig{
				Type:    "opensearch",
				Options: map[string]any{"address": srv.URL, "index": "logs"},
			},
			Query: `{"query":{"match_all":{}}}`,
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(rows.Next()).To(BeTrue())
		Expect(rows.Close()).To(Succeed())
		Expect(clearedScroll).To(BeTrue())
	})
})

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
