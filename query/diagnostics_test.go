package query_test

import (
	"strings"
	"time"

	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("ProviderDiagnostics", func() {
	It("redacts secret-shaped options while preserving native query details", func() {
		diagnostics := query.NewProviderDiagnostics("clickhouse", "SELECT 1", map[string]any{
			"database": "analytics",
			"nested": map[string]any{
				"api_key": "sensitive",
			},
		})
		diagnostics.RecordRequest("SELECT * FROM events WHERE tenant = ?", []any{"acme"}, nil)

		snapshot := diagnostics.Snapshot()
		Expect(snapshot.Request.Query).To(Equal("SELECT * FROM events WHERE tenant = ?"))
		Expect(snapshot.Request.Arguments).To(Equal([]any{"acme"}))
		Expect(snapshot.Request.Options).To(Equal(map[string]any{
			"database": "analytics",
			"nested": map[string]any{
				"api_key": "********",
			},
		}))
	})

	It("caps provider response previews and says they were truncated", func() {
		diagnostics := query.NewProviderDiagnostics("http", "/events", nil)
		diagnostics.RecordPreview("application/json", []byte(strings.Repeat("x", query.DiagnosticPreviewLimit+1)))
		diagnostics.RecordResponse(time.Now(), 1, nil)

		snapshot := diagnostics.Snapshot()
		Expect(snapshot.Response.Preview).To(HaveLen(query.DiagnosticPreviewLimit))
		Expect(snapshot.Response.Truncated).To(BeTrue())
		Expect(snapshot.Response.ContentType).To(Equal("application/json"))
	})
})
