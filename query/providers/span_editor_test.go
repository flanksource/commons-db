package providers

import (
	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Canonical span authoring input", func() {
	It("rejects removed field options before resolving a connection", func() {
		request := query.ProviderRequest{Options: map[string]any{"spanIdField": "spanID"}}
		for _, err := range (openTelemetryProvider{}).Pages(context.New(), request, query.PageRequest{}) {
			Expect(err).To(MatchError(ContainSubstring("spanIdField was removed")))
		}
	})
	It("defers normalization until custom identity mappings are evaluated", func() {
		row, err := openTelemetryRow(map[string]any{"custom_trace": "t", "custom_span": "s", "startedAt": "2026-11-02T10:00:00Z", "durationMs": 0.25}, OpenTelemetryOptions{RawRows: true})
		Expect(err).NotTo(HaveOccurred())
		profile := query.Profile{Render: query.RenderTraces, Spans: query.SpanMappings{"traceId": {Source: "custom_trace"}, "spanId": {Source: "custom_span"}}}
		result, err := query.PreviewMapping(context.New(), profile, []query.Row{row})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.NormalizedRows[0]["span_id"]).To(Equal("s"))
	})
})
