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
		diagnostics := query.NewDiagnostics(query.DiagnosticOptions{
			Provider: "clickhouse", Query: "SELECT 1", Detail: query.DiagnosticFull,
			Options: map[string]any{
				"database": "analytics",
				"nested": map[string]any{
					"api_key": "sensitive",
				},
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
		diagnostics := query.NewDiagnostics(query.DiagnosticOptions{
			Provider: "sql", Query: "SELECT 1", Detail: query.DiagnosticFull,
			Options: map[string]any{
				"url":      "postgres://reader:hunter2@db.example.com:5432/analytics?sslmode=require&api_key=abcd",
				"dsn":      "server=db.example.com;user id=reader;password=hunter2",
				"database": "analytics",
			},
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
		diagnostics := query.NewDiagnostics(query.DiagnosticOptions{Provider: "sink-buffered", Query: "", Detail: query.DiagnosticFull})
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

	It("attaches a rendered-only recorder to an ordinary execution", func() {
		provider := &mockProvider{typ: "sink-absent", rows: []query.Row{{"id": 1}}}
		query.RegisterProvider(provider)

		_, err := query.Execute(context.New(), query.Profile{
			Name: "plain", Provider: query.ProviderConfig{Type: "sink-absent"}, Query: "select 1",
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(provider.last.Diagnostics).NotTo(BeNil())
		Expect(provider.last.Diagnostics.WantsPreview()).To(BeFalse())
	})

	It("caps provider response previews and says they were truncated", func() {
		diagnostics := query.NewDiagnostics(query.DiagnosticOptions{Provider: "http", Query: "/events", Detail: query.DiagnosticFull})
		diagnostics.RecordPreview("application/json", []byte(strings.Repeat("x", query.DiagnosticPreviewLimit+1)))
		diagnostics.RecordResponse(time.Now(), 1, nil)

		snapshot := diagnostics.Snapshot()
		Expect(snapshot.Response.Preview).To(HaveLen(query.DiagnosticPreviewLimit))
		Expect(snapshot.Response.Truncated).To(BeTrue())
		Expect(snapshot.Response.ContentType).To(Equal("application/json"))
	})

	It("keeps the rendered query beside the statement a provider issued", func() {
		diagnostics := query.NewDiagnostics(query.DiagnosticOptions{Provider: "sql", Query: "", Detail: query.DiagnosticFull})
		diagnostics.RecordRendered("select * from events where tenant = 'acme'", nil)
		diagnostics.RecordRequest("SELECT * FROM events WHERE tenant = 'acme' LIMIT 100", nil, nil)

		snapshot := diagnostics.Snapshot()
		Expect(snapshot.Request.Rendered).To(Equal("select * from events where tenant = 'acme'"))
		Expect(snapshot.Request.Query).To(Equal("SELECT * FROM events WHERE tenant = 'acme' LIMIT 100"))
	})

	It("records a connection reference verbatim and strips an inline DSN", func() {
		reference := query.NewDiagnostics(query.DiagnosticOptions{Provider: "sql", Query: "", Detail: query.DiagnosticFull})
		reference.RecordConnection("connection://ops/warehouse")
		Expect(reference.Snapshot().Request.Connection).To(Equal("connection://ops/warehouse"))

		inline := query.NewDiagnostics(query.DiagnosticOptions{Provider: "sql", Query: "", Detail: query.DiagnosticFull})
		inline.RecordConnection("postgres://reader:hunter2@db.example.com:5432/analytics")
		Expect(inline.Snapshot().Request.Connection).To(Equal("postgres://reader@db.example.com:5432/analytics"))
		Expect(inline.Snapshot().Request.Connection).ToNot(ContainSubstring("hunter2"))
	})

	Describe("walk diagnostics", func() {
		It("reports the first statement and sums what every page cost", func() {
			diagnostics := query.NewDiagnostics(query.DiagnosticOptions{Provider: "sql", Walk: true})
			diagnostics.RecordRendered("select * from events", nil)

			diagnostics.RecordRequest("SELECT * FROM events LIMIT 100 OFFSET 0", []any{0}, map[string]any{"page": 1})
			diagnostics.RecordResponse(time.Now().Add(-10*time.Millisecond), 100, map[string]any{"page": 1})
			diagnostics.RecordRequest("SELECT * FROM events LIMIT 100 OFFSET 100", []any{100}, map[string]any{"page": 2})
			diagnostics.RecordResponse(time.Now().Add(-10*time.Millisecond), 40, map[string]any{"page": 2})

			snapshot := diagnostics.Snapshot()
			// The walk's identity is its first page: reporting the last would
			// name OFFSET 100 as the query the reconciliation ran.
			Expect(snapshot.Request.Query).To(Equal("SELECT * FROM events LIMIT 100 OFFSET 0"))
			Expect(snapshot.Request.Arguments).To(Equal([]any{0}))
			Expect(snapshot.Request.Details).To(Equal(map[string]any{"page": 1}))
			Expect(snapshot.Request.Rendered).To(Equal("select * from events"))
			Expect(snapshot.Response.ReturnedRows).To(Equal(140))
			Expect(snapshot.Response.Pages).To(Equal(2))
			Expect(snapshot.Response.DurationMS).To(BeNumerically(">=", 20))
		})

		It("declines the previews and instrumentation only a debug run pays for", func() {
			walk := query.NewDiagnostics(query.DiagnosticOptions{Provider: "sql", Walk: true})
			Expect(walk.WantsPreview()).To(BeFalse())
			Expect(query.NewDiagnostics(query.DiagnosticOptions{Provider: "sql", Query: "", Detail: query.DiagnosticFull}).WantsPreview()).To(BeTrue())

			var absent *query.ProviderDiagnostics
			Expect(absent.WantsPreview()).To(BeFalse())

			walk.RecordPreview("application/json", []byte(`[{"id":1}]`))
			Expect(walk.Snapshot().Response.Preview).To(BeEmpty())
		})
	})
})
