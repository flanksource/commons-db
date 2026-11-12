package query

import (
	"encoding/json"

	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/otel/span/layout"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Canonical span editor", func() {
	It("rejects computed and aggregate mappings that cannot preserve search conditions", func() {
		for _, mappings := range []SpanMappings{{"service": {CEL: "row.component"}}, {"attributes": {CEL: "row.tags"}}, {"attributes": {Source: "custom_tags"}}, {"resource": {JSONPath: "$.resource"}}} {
			Expect(mappings.ValidateReversible()).To(HaveOccurred())
		}
		Expect((SpanMappings{"service": {Source: "component"}}).ValidateReversible()).To(Succeed())
	})
	It("restores integer source attributes and explicit timestamp units from a browser sample", func() {
		definition := layout.Definition{Fields: map[string]layout.Field{"trace_id": {Source: "traceID", Type: "keyword"}, "span_id": {Source: "spanID", Type: "keyword"}, "timestamp": {Source: "clock", Type: "long", Unit: "ms"}, "duration_ms": {Source: "elapsed", Type: "double", Unit: "ms"}, "attributes.attempt": {Source: "attributes.attempt", Type: "long"}}}
		profile := Profile{Render: RenderTraces, Columns: []ColumnDef{{Name: "attempts", CEL: "row._span_attributes.attempt * 2"}}}
		result, err := PreviewMapping(context.New(), profile, []Row{{"traceID": "t", "spanID": "s", "clock": float64(1793613600000), "elapsed": 0.25, "attributes": map[string]any{"attempt": float64(3)}, "_span_layout": definition}})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Rows[0]["attempts"]).To(BeNumerically("==", 6))
		Expect(result.NormalizedRows[0]["timestamp"]).To(Equal("2026-11-02T10:00:00Z"))
	})
	It("retains nanosecond source timestamps losslessly and applies explicit units", func() {
		for _, clock := range []any{int64(1793613600000000123), json.Number("1793613600000000123")} {
			input, err := copyMappingRows([]Row{{"custom_time": clock, "traceID": "t", "spanID": "s", "startTime": int64(1793613600000100), "duration": 250}})
			Expect(err).NotTo(HaveOccurred())
			Expect(input[0]["custom_time"]).To(Equal("1793613600000000123"))
			p := Profile{Render: RenderTraces, Spans: SpanMappings{"startedAt": {Source: "custom_time", Unit: "ns"}, "durationMs": {Source: "duration", Unit: "us"}}}
			result, err := PreviewMapping(context.New(), p, input)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.NormalizedRows[0]["timestamp"]).To(Equal("2026-11-02T10:00:00.000000123Z"))
			Expect(result.NormalizedRows[0]["duration_ms"]).To(Equal(0.25))
		}
	})
	It("maps canonical fields before aliases and offers only normalized columns", func() {
		p := Profile{Render: RenderTraces, Spans: SpanMappings{"service": {Source: "custom_service"}}, Columns: DefaultSpanColumns()}
		result, err := PreviewMapping(context.New(), p, []Row{{"traceID": "t", "spanID": "s", "startTime": int64(1793613600000100), "duration": 250, "custom_service": "checkout", "tags": []any{map[string]any{"key": "http.method", "value": "GET"}}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.NormalizedRows[0]["service"]).To(Equal("checkout"))
		Expect(result.AvailableColumns).To(ContainElement(HaveField("Name", "attributes.http.method")))
		Expect(result.AvailableColumns).NotTo(ContainElement(HaveField("Name", "custom_service")))
	})
	It("rejects a missing explicit mapping instead of detecting another field", func() {
		_, err := PreviewMapping(context.New(), Profile{Render: RenderTraces, Spans: SpanMappings{"traceId": {Source: "missing"}}}, []Row{{"traceID": "t", "spanID": "s"}})
		Expect(err).To(MatchError(ContainSubstring("traceId")))
	})
})
