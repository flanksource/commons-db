package query_test

import (
	"strings"
	"time"

	context "github.com/flanksource/commons-db/context"
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

	It("strips the credentials out of a connection string an option carries", func() {
		diagnostics := query.NewProviderDiagnostics("sql", "SELECT 1", map[string]any{
			"url":      "postgres://reader:hunter2@db.example.com:5432/analytics?sslmode=require&api_key=abcd",
			"dsn":      "server=db.example.com;user id=reader;password=hunter2",
			"database": "analytics",
		})

		snapshot := diagnostics.Snapshot()
		Expect(snapshot.Request.Options["url"]).To(Equal(
			"postgres://reader@db.example.com:5432/analytics?api_key=%2A%2A%2A%2A%2A%2A%2A%2A&sslmode=require"))
		Expect(snapshot.Request.Options["dsn"]).To(Equal(
			"server=db.example.com;user id=reader;password=********"))
		Expect(snapshot.Request.Options["database"]).To(Equal("analytics"))
	})

	It("records the rendered query of a buffered execution into a context sink", func() {
		query.RegisterProvider(&mockProvider{typ: "sink-buffered", rows: []query.Row{{"id": 1}}})
		diagnostics := query.NewProviderDiagnostics("sink-buffered", "", nil)
		profile := query.Profile{
			Name:     "regional",
			Provider: query.ProviderConfig{Type: "sink-buffered", Options: map[string]any{"password": "hunter2"}},
			Query:    "select * where region = '{{.params.region}}'",
			Params:   []query.ParamDef{{Name: "region", Type: query.ParamTypeString}},
		}

		ctx := query.WithDiagnosticSink(context.New(), diagnostics)
		result, err := query.Execute(ctx, profile, map[string]any{"region": "EU"})
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Rows).To(HaveLen(1))

		snapshot := diagnostics.Snapshot()
		Expect(snapshot.Request.Query).To(Equal("select * where region = 'EU'"))
		Expect(snapshot.Request.Options).To(Equal(map[string]any{"password": "********"}))
	})

	It("leaves an ordinary execution with no recorder attached", func() {
		provider := &mockProvider{typ: "sink-absent", rows: []query.Row{{"id": 1}}}
		query.RegisterProvider(provider)

		_, err := query.Execute(context.New(), query.Profile{
			Name: "plain", Provider: query.ProviderConfig{Type: "sink-absent"}, Query: "select 1",
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(provider.last.Diagnostics).To(BeNil())
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
