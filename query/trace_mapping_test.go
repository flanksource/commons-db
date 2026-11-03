package query

import (
	"encoding/json"
	"github.com/flanksource/clicky/formatters"
	"github.com/flanksource/commons-db/context"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Trace mapping preview", func() {
	It("keeps canonical span metadata hidden in discovered columns", func() {
		Expect(InferSampleColumns([]Row{{"_trace": map[string]any{"spanId": "span"}}})).To(Equal([]ColumnDef{{Name: "_trace", Type: ColumnTypeJSON, Hidden: true}}))
	})
	It("retains nested sample input across repeated mapping edits", func() {
		input := []Row{{"nested": map[string]any{"value": 1}}}
		profile := Profile{Aliases: []AliasDef{{Name: "nested.value", CEL: "2"}}, Columns: []ColumnDef{{Name: "nested"}}}
		_, err := PreviewMapping(context.New(), profile, input)
		Expect(err).NotTo(HaveOccurred())
		Expect(input).To(Equal([]Row{{"nested": map[string]any{"value": 1}}}))
	})
	It("uses retained canonical precision instead of reinterpreting source fields", func() {
		profile := Profile{Render: RenderTraces, Provider: ProviderConfig{Options: map[string]any{"dateField": "raw_time"}}, Columns: []ColumnDef{{Name: "timestamp"}}}
		input := []Row{{"raw_time": float64(1793613600000000000), "_trace": map[string]any{"traceId": "trace", "spanId": "span", "startedAt": "2026-11-02T10:00:00.000000123Z", "durationMs": 0.25, "service": "checkout", "operation": "request", "status": "unset"}}}
		result, err := PreviewMapping(context.New(), profile, input)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Rows[0]["timestamp"]).To(Equal("2026-11-02T10:00:00.000000123Z"))
	})
	It("normalizes Jaeger input before aliases and column projection while retaining details", func() {
		profile := Profile{Name: "traces", Render: RenderTraces, Aliases: []AliasDef{{Name: "canonical_span", CEL: "row.span_id"}}, Columns: []ColumnDef{{Name: "display", Source: "span_id", Label: "Span label"}, {Name: "aliased", Source: "canonical_span"}}}
		input := []Row{{"traceID": "trace", "spanID": "span", "startTime": int64(1793613600000100), "duration": 250, "operationName": "GET /orders", "tags": []any{map[string]any{"key": "cached", "value": false}}}}
		result, err := PreviewMapping(context.New(), profile, input)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Rows[0]["display"]).To(Equal("span"))
		Expect(result.Rows[0]["aliased"]).To(Equal("span"))
		Expect(result.Rows[0]).NotTo(HaveKey("span_id"))
		encoded, err := json.Marshal(result.Rows[0]["_trace"])
		Expect(err).NotTo(HaveOccurred())
		Expect(string(encoded)).To(ContainSubstring(`"durationMs":0.25`))
		Expect(input[0]).NotTo(HaveKey("span_id"))
		var document formatters.ClickyDocument
		Expect(json.Unmarshal(result.Document, &document)).To(Succeed())
		Expect(document.Node.Columns).To(HaveLen(2))
		Expect(document.Node.Columns[0].Name).To(Equal("display"))
		Expect(document.Node.Columns[0].Label).To(Equal("Span label"))
	})
})
