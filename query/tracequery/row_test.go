package tracequery

import (
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("span row projection", func() {
	It("normalizes error tags and preserves typed resource and span attributes", func() {
		span, err := FromRow(query.Row{"trace_id": "trace", "span_id": "span", "timestamp": "2026-11-02T10:00:00Z", "duration_ms": 0.25, "status": "true", "_attributes": map[string]any{"tag.db.statement": "SELECT 1", "resource.attributes.service.name": "checkout", "attributes.gen_ai.usage.input_tokens": float64(12)}})
		Expect(err).NotTo(HaveOccurred())
		Expect(span.Status).To(Equal("error"))
		Expect(span.Attributes).To(HaveKeyWithValue("db.statement", "SELECT 1"))
		Expect(span.Attributes).To(HaveKeyWithValue("gen_ai.usage.input_tokens", float64(12)))
		Expect(span.Resource).To(HaveKeyWithValue("service.name", "checkout"))
	})
})
