package tracequery

import (
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestTraceQuery(t *testing.T) { RegisterFailHandler(Fail); RunSpecs(t, "Trace query") }

var _ = Describe("trace search", func() {
	at := time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)
	var spans []Span
	BeforeEach(func() {
		spans = []Span{
			{TraceID: "trace-a", SpanID: "root", Service: "checkout", Operation: "POST /orders", StartedAt: at, DurationMs: 100, Status: "ok"},
			{TraceID: "trace-a", SpanID: "child", ParentID: "root", Service: "payments", Operation: "charge", StartedAt: at.Add(10 * time.Millisecond), DurationMs: 40, Status: "error", Attributes: map[string]any{"http.status_code": 500}},
			{TraceID: "trace-b", SpanID: "other", Service: "checkout", Operation: "GET /orders", StartedAt: at.Add(time.Second), DurationMs: 10, Status: "ok"},
		}
	})
	It("filters whole traces by their services, root operation, status and any span's attributes, returning whole-trace totals", func() {
		result, err := Search(spans, Request{
			Services: Modes{"payments": Include}, Operations: Modes{"POST /orders": Include}, Statuses: Modes{"error": Include},
			Attributes: Modes{"http.status_code=5*": Include}, MinDuration: "90ms",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Traces).To(HaveExactElements(Summary{TraceID: "trace-a", Service: "checkout", Operation: "POST /orders", StartedAt: at, DurationMs: 100, SpanCount: 2, Errors: 1, Services: []string{"checkout", "payments"}}))
		result, err = Search(spans, Request{Operations: Modes{"charge": Include}})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Total).To(BeZero(), "charge runs in a trace but is not its root")
	})
	DescribeTable("includes are any-of, excludes reject, and attribute includes are any-of per key and all-of across keys",
		func(request Request, want []string) {
			result, err := Search(spans, request)
			Expect(err).NotTo(HaveOccurred())
			ids := []string{}
			for _, trace := range result.Traces {
				ids = append(ids, trace.TraceID)
			}
			Expect(ids).To(Equal(want))
		},
		Entry("either root operation", Request{Operations: Modes{"POST /orders": Include, "GET /orders": Include}}, []string{"trace-b", "trace-a"}),
		Entry("an excluded service", Request{Services: Modes{"payments": Exclude}}, []string{"trace-b"}),
		Entry("ok traces", Request{Statuses: Modes{"ok": Include}}, []string{"trace-b"}),
		Entry("a bare key matches its presence", Request{Attributes: Modes{"http.status_code": Include}}, []string{"trace-a"}),
		Entry("values of one key are any-of", Request{Attributes: Modes{"http.status_code=404": Include, "http.status_code=500": Include}}, []string{"trace-a"}),
		Entry("keys are all-of", Request{Attributes: Modes{"http.status_code=500": Include, "absent": Include}}, []string{}),
		Entry("an excluded attribute", Request{Attributes: Modes{"http.status_code": Exclude}}, []string{"trace-b"}),
		Entry("traces of at least two spans", Request{MinSpans: 2}, []string{"trace-a"}),
	)
	It("counts every root operation in the window, with its percentiles, whatever the operation and span filters", func() {
		result, err := Search(spans, Request{Operations: Modes{"GET /orders": Include}, MinSpans: 2})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Total).To(BeZero())
		Expect(result.RootOperations).To(Equal([]OperationStats{
			{Operation: "POST /orders", Count: 1, P50Ms: 100, P95Ms: 100},
			{Operation: "GET /orders", Count: 1, SingleSpan: true, P50Ms: 10, P95Ms: 10},
		}))
		result, err = Search(spans, Request{Services: Modes{"payments": Exclude}})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RootOperations).To(HaveExactElements(HaveField("Operation", "GET /orders")), "the other filters still narrow the counts")
	})
	It("suggests the root spans' attribute values, most frequent first", func() {
		spans[0].Attributes = map[string]any{"http.method": "POST"}
		spans[2].Attributes = map[string]any{"http.method": "GET", "payload": map[string]any{"nested": true}}
		spans[2].Resource = map[string]any{"service.name": "checkout"}
		spans[0].Resource = map[string]any{"service.name": "checkout"}
		result, err := Search(spans, Request{})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Attributes).To(Equal([]AttributeCount{
			{Value: "service.name=checkout", Count: 2},
			{Value: "http.method=GET", Count: 1},
			{Value: "http.method=POST", Count: 1},
		}))
	})
	It("caps traces after grouping and keeps deterministic newest-first order", func() {
		result, err := Search(spans, Request{Limit: 1})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Total).To(Equal(2))
		Expect(result.Traces).To(HaveExactElements(HaveField("TraceID", "trace-b")))
	})
	It("reports missing parents and refuses cycles or conflicting duplicate span IDs", func() {
		detail, err := Trace(spans, "trace-a")
		Expect(err).NotTo(HaveOccurred())
		Expect(detail.Spans).To(HaveLen(2))
		spans[0].ParentID = "absent"
		detail, err = Trace(spans, "trace-a")
		Expect(err).NotTo(HaveOccurred())
		Expect(detail.MissingParents).To(Equal([]string{"absent"}))
		spans[0].ParentID = "child"
		_, err = Trace(spans, "trace-a")
		Expect(err).To(MatchError(ContainSubstring("cycle")))
	})
	It("names a rootless trace after its earliest span regardless of input order", func() {
		spans[0].ParentID = "absent"
		result, err := Search([]Span{spans[1], spans[0]}, Request{})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Traces[0].Operation).To(Equal("POST /orders"))
	})
	It("deduplicates identical spans and refuses conflicting or overflowing durations", func() {
		result, err := Search(append(spans, spans[0]), Request{})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Traces[1].SpanCount).To(Equal(2))
		changed := spans[0]
		changed.DurationMs++
		_, err = Search(append(spans, changed), Request{})
		Expect(err).To(MatchError(ContainSubstring("conflicting span")))
		spans[0].DurationMs = 1e20
		_, err = Search(spans, Request{})
		Expect(err).To(MatchError(ContainSubstring("invalid span")))
	})
	DescribeTable("refuses invalid filters", func(request Request) { _, err := Search(spans, request); Expect(err).To(HaveOccurred()) },
		Entry("negative duration", Request{MinDuration: "-1ms"}), Entry("duration order", Request{MinDuration: "2s", MaxDuration: "1s"}), Entry("limit", Request{Limit: 1001}),
		Entry("status", Request{Statuses: Modes{"maybe": Include}}), Entry("mode", Request{Services: Modes{"checkout": "only"}}),
		Entry("attribute key", Request{Attributes: Modes{"=500": Include}}), Entry("span count", Request{MinSpans: -1}))
})
