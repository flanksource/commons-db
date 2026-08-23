package query_test

import (
	context "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons/logger"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Recorder", func() {
	Describe("an unarmed run", func() {
		// This is the "console closed costs nothing" guarantee. If it ever fails,
		// every ordinary execution on the server started paying for a feature
		// nobody switched on.
		It("attaches no recorder and still records the rendered walk it always did", func() {
			query.RegisterProvider(&mockProvider{typ: "recorder-unarmed", rows: []query.Row{{"id": 1}}})
			ctx := context.New()

			Expect(query.RecorderFrom(ctx)).To(BeNil())

			diagnostics := query.NewDiagnostics(query.DiagnosticOptions{Provider: "recorder-unarmed", Walk: true})
			_, err := query.Execute(query.WithDiagnosticSink(ctx, diagnostics), query.Profile{
				Name:     "plain",
				Provider: query.ProviderConfig{Type: "recorder-unarmed"},
				Query:    "select 1",
			})
			Expect(err).ToNot(HaveOccurred())

			Expect(diagnostics.Snapshot().Request.Rendered).To(Equal("select 1"))
			Expect(diagnostics.WantsPreview()).To(BeFalse())
		})

		It("is safe to call every recorder method on", func() {
			var absent *query.Recorder
			Expect(absent.ID()).To(BeEmpty())
			Expect(absent.Level()).To(Equal(logger.Info))
			Expect(absent.DiagnosticDetail()).To(Equal(query.DiagnosticRendered))
			Expect(absent.Operation("sql")).To(BeNil())
			Expect(absent.Summary()).To(Equal(query.ExecutionSummary{}))

			absent.Log(query.LogLine{Message: "ignored"})
			absent.RecordProbe(query.CardinalityProbe{Column: "ignored"})
			absent.Finish(query.FinishOptions{Status: 200})
		})
	})

	Describe("capture level", func() {
		DescribeTable("chooses how much a run pays to explain itself",
			func(level logger.LogLevel, expected query.DiagnosticDetail) {
				Expect(query.DetailForLevel(level)).To(Equal(expected))
			},
			Entry("info records only what it already knew", logger.Info, query.DiagnosticRendered),
			Entry("debug still asks no backend for anything", logger.Debug, query.DiagnosticRendered),
			Entry("trace is where previews begin", logger.Trace, query.DiagnosticFull),
			Entry("trace2 keeps them", logger.Trace2, query.DiagnosticFull),
		)
	})

	Describe("an armed run", func() {
		var recorder *query.Recorder

		BeforeEach(func() {
			recorder = query.NewRecorder(query.RecorderOptions{
				ID:    "rec-1",
				Level: logger.Trace,
				Source: query.ExecutionSource{
					Surface: "profile", Profile: "regional", Method: "GET", Path: "/api/v1/profile/regional",
				},
			})
		})

		It("records one operation per provider execution, in the order they ran", func() {
			query.RegisterProvider(&mockProvider{typ: "recorder-armed", rows: []query.Row{{"id": 1}, {"id": 2}}})

			ctx := query.WithRecorder(context.New(), recorder)
			_, err := query.Execute(ctx, query.Profile{
				Name:     "regional",
				Provider: query.ProviderConfig{Type: "recorder-armed"},
				Query:    "select * from events",
			})
			Expect(err).ToNot(HaveOccurred())
			recorder.Finish(query.FinishOptions{Status: 200})

			summary := recorder.Summary()
			Expect(summary.ID).To(Equal("rec-1"))
			Expect(summary.Source.Profile).To(Equal("regional"))
			Expect(summary.Rows).To(Equal(2))
			Expect(summary.Counts.Operations).To(Equal(1))
			Expect(summary.Operations).To(HaveLen(1))
			Expect(summary.Operations[0].Index).To(Equal(1))
			Expect(summary.Operations[0].Provider).To(Equal("recorder-armed"))
			Expect(summary.Operations[0].Query).To(Equal("select * from events"))
		})

		It("raises the diagnostic detail so a paged read is still explained in full", func() {
			Expect(recorder.DiagnosticDetail()).To(Equal(query.DiagnosticFull))
		})

		It("bounds retained log lines and counts what it dropped", func() {
			bounded := query.NewRecorder(query.RecorderOptions{ID: "rec-2", Level: logger.Debug, MaxLogLines: 2})
			for range 5 {
				bounded.Log(query.LogLine{Level: "debug", Message: "line"})
			}
			bounded.Finish(query.FinishOptions{Status: 200})

			counts := bounded.Summary().Counts
			Expect(counts.LogLines).To(Equal(2))
			Expect(counts.LogDropped).To(Equal(3))
		})

		It("assigns each retained line a sequence and the record it belongs to", func() {
			recorder.Log(query.LogLine{Level: "debug", Message: "first"})
			recorder.Log(query.LogLine{Level: "debug", Message: "second"})

			logs := recorder.Detail().Logs
			Expect(logs).To(HaveLen(2))
			Expect(logs[0].Sequence).To(Equal(int64(1)))
			Expect(logs[1].Sequence).To(Equal(int64(2)))
			Expect(logs[0].RecordID).To(Equal("rec-1"))
			Expect(logs[0].Source).To(Equal("request"))
		})

		It("reports a failed execution rather than dropping it", func() {
			recorder.Finish(query.FinishOptions{Status: 422, Err: errStub{"provider refused the statement"}})

			summary := recorder.Summary()
			Expect(summary.Status).To(Equal(422))
			Expect(summary.Error).To(Equal("provider refused the statement"))
		})

		It("closes once, so an error that unwinds twice is recorded once", func() {
			recorder.Finish(query.FinishOptions{Status: 200})
			recorder.Finish(query.FinishOptions{Status: 500, Err: errStub{"late"}})

			summary := recorder.Summary()
			Expect(summary.Status).To(Equal(200))
			Expect(summary.Error).To(BeEmpty())
		})
	})
})

type errStub struct{ message string }

func (e errStub) Error() string { return e.message }
