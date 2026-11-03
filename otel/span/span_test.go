package span

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"testing"
	"time"
)

func TestSpans(t *testing.T) { RegisterFailHandler(Fail); RunSpecs(t, "Span normalization") }

var _ = Describe("Normalize", func() {
	It("retains empty, false and zero attribute values and honors explicit fields", func() {
		row := map[string]any{"traceID": "raw-trace", "spanID": "raw-span", "customTrace": "selected-trace", "customSpan": "selected-span", "startTimeMillis": float64(1793613600000), "duration_ms": 0.25, "tags": []any{map[string]any{"key": "empty", "value": ""}, map[string]any{"key": "cached", "value": false}, map[string]any{"key": "count", "value": 0}, map[string]any{"key": "custom.status", "value": "ERROR"}}}
		s, err := Normalize(row, Options{TraceIDField: "customTrace", SpanIDField: "customSpan", StatusFields: []string{"tag.custom@status"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(s.TraceID).To(Equal("selected-trace"))
		Expect(s.SpanID).To(Equal("selected-span"))
		Expect(s.Status).To(Equal("error"))
		Expect(s.Attributes).To(Equal(map[string]any{"empty": "", "cached": false, "count": 0, "custom.status": "ERROR"}))
	})
	It("detects Jaeger units, references and typed tags", func() {
		s, err := Normalize(map[string]any{"traceID": "trace-a", "spanID": "child", "timestamp": "1999-01-01T00:00:00Z", "startTime": "1793613600000100", "duration": "250", "operationName": "SELECT", "process.serviceName": "checkout", "references": `[{"refType":"CHILD_OF","spanID":"root"}]`, "tags": []any{map[string]any{"key": "error", "value": true}}}, Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(s).To(Equal(Span{TraceID: "trace-a", SpanID: "child", ParentID: "root", StartedAt: time.Date(2026, 11, 2, 10, 0, 0, 100000, time.UTC), DurationMs: 0.25, Service: "checkout", Operation: "SELECT", Status: "error", Attributes: map[string]any{"error": true}, Resource: map[string]any{"service.name": "checkout"}}))
	})
	DescribeTable("rejects invalid input", func(row map[string]any) { _, err := Normalize(row, Options{}); Expect(err).To(HaveOccurred()) },
		Entry("missing identity", map[string]any{}),
		Entry("ambiguous duration", map[string]any{"trace_id": "t", "span_id": "s", "timestamp": "2026-11-02T10:00:00Z", "duration": 42}),
		Entry("invalid duration", map[string]any{"trace_id": "t", "span_id": "s", "timestamp": "2026-11-02T10:00:00Z", "duration_ms": "bad"}),
		Entry("unknown status", map[string]any{"trace_id": "t", "span_id": "s", "timestamp": "2026-11-02T10:00:00Z", "duration_ms": 1, "status": "constructor"}),
		Entry("negative duration", map[string]any{"trace_id": "t", "span_id": "s", "timestamp": "2026-11-02T10:00:00Z", "duration_ms": -1}),
	)
	DescribeTable("detects epoch units", func(value int64) {
		at, err := timestamp(value, "timestamp", "")
		Expect(err).NotTo(HaveOccurred())
		Expect(at).To(Equal(time.Date(2026, 7, 27, 8, 42, 43, 0, time.UTC)))
	}, Entry("seconds", int64(1785141763)), Entry("milliseconds", int64(1785141763000)), Entry("microseconds", int64(1785141763000000)), Entry("nanoseconds", int64(1785141763000000000)))
	DescribeTable("uses explicit duration units before format", func(value any, field, format string, want float64) {
		got, err := duration(map[string]any{field: value}, format, time.Time{})
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(want))
	}, Entry("Jaeger microseconds", int64(123000), "duration", "jaeger", 123.0), Entry("flat nanoseconds", int64(500000), "duration", "flat", 0.5), Entry("explicit milliseconds", int64(500), "duration_ms", "jaeger", 500.0))
})
