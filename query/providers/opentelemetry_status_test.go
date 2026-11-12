package providers

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("trace document metadata", func() {
	It("reads nested status codes and separates resource and span tags", func() {
		row := openTelemetryRow(map[string]any{"status": map[string]any{"code": 2}, "tags": []any{map[string]any{"key": "shared", "value": "span"}}, "process": map[string]any{"tags": []any{map[string]any{"key": "shared", "value": "resource"}}}}, OpenTelemetryOptions{})
		Expect(row["status"]).To(Equal("2"))
		Expect(row["_resource"]).To(Equal(map[string]any{"shared": "resource"}))
		Expect(row["_span_attributes"]).To(Equal(map[string]any{"shared": "span"}))
	})
})
