package query_test

import (
	"encoding/json"

	"github.com/flanksource/clicky/formatters"
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Trace profile presentation", func() {
	It("preserves raw span fields and typed attributes in the interactive document", func() {
		profile := query.Profile{Name: "Service traces", Render: query.RenderTraces, Columns: []query.ColumnDef{{Name: "operation"}}}
		rows := []query.Row{{"operation": "POST /orders", "trace_id": "trace-a", "span_id": "root", "timestamp": "2026-11-02T10:00:00.000100Z", "duration_ms": 1250.25, "_span_attributes": map[string]any{"cached": false, "db.rows": 3}}}
		output, err := query.RenderClickyPage(profile, rows)
		Expect(err).NotTo(HaveOccurred())
		var document formatters.ClickyDocument
		Expect(json.Unmarshal([]byte(output), &document)).To(Succeed())
		cells := document.Node.Rows[0].Cells
		Expect(cells).To(HaveKey("trace_id"))
		Expect(cells).To(HaveKey("duration_ms"))
		Expect(cells["duration_ms"].FilterValue).To(Equal(1250.25))
		Expect(cells["timestamp"].PlainText()).To(Equal("2026-11-02T10:00:00.000100Z"))
		Expect(cells["_span_attributes"].Language).To(Equal("json"))
		Expect(cells["_span_attributes"].Source).To(MatchJSON(`{"cached":false,"db.rows":3}`))
	})
})
