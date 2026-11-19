package tracequery

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("trace HTTP handler", func() {
	It("decodes typed trace filters and rejects malformed parameters", func() {
		values, err := url.ParseQuery("minSpans=2&services=%7B%22checkout%22:%22include%22%7D")
		Expect(err).NotTo(HaveOccurred())
		request, err := ParseRequest(values)
		Expect(err).NotTo(HaveOccurred())
		Expect(request).To(Equal(Request{MinSpans: 2, Services: Modes{"checkout": Include}}))
		for _, query := range []string{"minSpans=oops", "unknown=value", "limit=1&limit=2", "services=invalid"} {
			values, err := url.ParseQuery(query)
			Expect(err).NotTo(HaveOccurred())
			_, err = ParseRequest(values)
			Expect(err).To(HaveOccurred(), query)
		}
	})

	It("searches at its root and opens a trace below it, for its fixed source", func() {
		at := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
		sources := []string{}
		service := Service{Read: func(_ context.Context, r Request, _ []string) ([]Span, error) {
			sources = append(sources, r.Source)
			return []Span{
				{TraceID: "trace-a", SpanID: "root", StartedAt: at, DurationMs: 5, Service: "checkout", Operation: "root", Status: "ok"},
				{TraceID: "trace-a", SpanID: "child", ParentID: "root", StartedAt: at.Add(time.Millisecond), DurationMs: 1, Service: "checkout", Operation: "child", Status: "error"},
			}, nil
		}}
		handler := http.StripPrefix("/traces", Handler(service, "fixed"))
		serve := func(target string, header http.Header) *httptest.ResponseRecorder {
			request := httptest.NewRequest(http.MethodGet, target, nil)
			request.Header = header
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			return response
		}

		response := serve("/traces?minSpans=2&source=ignored", http.Header{})
		Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())
		var result SearchResult
		Expect(json.Unmarshal(response.Body.Bytes(), &result)).To(Succeed())
		Expect(result.Traces).To(HaveExactElements(And(HaveField("TraceID", "trace-a"), HaveField("SpanCount", 2), HaveField("Errors", 1))))

		response = serve("/traces/trace-a", http.Header{})
		Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())
		var detail Detail
		Expect(json.Unmarshal(response.Body.Bytes(), &detail)).To(Succeed())
		Expect(detail.Spans).To(HaveLen(2))
		Expect(sources).To(HaveEach("fixed"))

		response = serve("/traces/", http.Header{"Accept": {"application/schema+json"}})
		Expect(response.Code).To(Equal(http.StatusOK))
		Expect(response.Body.String()).To(ContainSubstring(`"attributes"`))

		Expect(serve("/traces/trace-missing", http.Header{}).Code).To(Equal(http.StatusNotFound))
		Expect(serve("/traces?minSpans=oops", http.Header{}).Code).To(Equal(http.StatusBadRequest))
		Expect(serve("/traces?lookback=-1h", http.Header{}).Code).To(Equal(http.StatusBadRequest))
	})
})
