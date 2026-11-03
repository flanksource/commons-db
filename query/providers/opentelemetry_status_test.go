package providers

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("trace document metadata", func() {
	It("reads nested status codes and separates resource and span tags", func() {
		row, err := openTelemetryRow(map[string]any{"trace_id": "trace", "span_id": "span", "timestamp": "2026-11-02T10:00:00Z", "duration_ms": 0.25, "status": map[string]any{"code": 2}, "tags": []any{map[string]any{"key": "shared", "value": "span"}}, "process": map[string]any{"tags": []any{map[string]any{"key": "shared", "value": "resource"}}}}, OpenTelemetryOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(row["status"]).To(Equal("error"))
		Expect(row["_resource"]).To(Equal(map[string]any{"shared": "resource"}))
		Expect(row["_span_attributes"]).To(Equal(map[string]any{"shared": "span"}))
	})
})
