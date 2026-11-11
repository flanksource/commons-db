package tracequery

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("trace reader orchestration", func() {
	It("hydrates parents outside the discovery window before summarizing", func() {
		at := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
		root := Span{TraceID: "trace", SpanID: "root", StartedAt: at, DurationMs: 100, Operation: "root"}
		child := Span{TraceID: "trace", SpanID: "child", ParentID: "root", StartedAt: at.Add(20 * time.Millisecond), DurationMs: 10}
		reads := 0
		service := Service{Read: func(_ context.Context, request Request, ids []string) ([]Span, error) {
			reads++
			if len(ids) == 0 {
				return []Span{child}, nil
			}
			Expect(ids).To(Equal([]string{"trace"}))
			Expect(request.From).To(BeEmpty())
			Expect(request.Until).To(BeEmpty())
			return []Span{root, child}, nil
		}}
		result, err := service.Search(context.Background(), Request{From: at.Add(10 * time.Millisecond).Format(time.RFC3339Nano), Until: at.Add(time.Second).Format(time.RFC3339Nano), MinSpans: 2})
		Expect(err).NotTo(HaveOccurred())
		Expect(reads).To(Equal(2))
		Expect(result.Traces).To(HaveExactElements(And(HaveField("Operation", "root"), HaveField("SpanCount", 2), HaveField("DurationMs", float64(100)))))
	})
	It("passes cancellation to the source and returns its error", func() {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		service := Service{Read: func(readCtx context.Context, _ Request, _ []string) ([]Span, error) {
			Expect(readCtx.Err()).To(Equal(context.Canceled))
			return nil, readCtx.Err()
		}}
		_, err := service.Search(ctx, Request{})
		Expect(err).To(MatchError(context.Canceled))
	})
})
