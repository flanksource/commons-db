package query_test

import (
	"errors"
	"time"

	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/sqlactivity"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("ProviderDiagnostics server session", func() {
	It("keeps the recorded session through the request the provider records after it", func() {
		var seen []query.ServerSession
		diagnostics := query.NewDiagnostics(query.DiagnosticOptions{
			Provider: "sql", OnSession: func(session query.ServerSession) { seen = append(seen, session) },
		})
		session := query.ServerSession{Engine: "postgres", SessionID: 4121, Server: "10.0.0.5", Database: "orders", User: "app", Page: 1}
		diagnostics.RecordSession(session)
		diagnostics.RecordRequest("SELECT 1", nil, map[string]any{"dialect": "postgres"})

		Expect(seen).To(Equal([]query.ServerSession{session}))
		Expect(diagnostics.Snapshot().Request.Details).To(Equal(map[string]any{"dialect": "postgres", "session": session}))
	})

	It("hands every sample to OnSample and keeps only the last 100 through the response", func() {
		const recorded = query.DiagnosticSampleLimit + 7
		var seen int
		diagnostics := query.NewDiagnostics(query.DiagnosticOptions{
			Provider: "sql", OnSample: func(sqlactivity.Sample) { seen++ },
		})
		for i := range recorded {
			diagnostics.RecordSample(sqlactivity.Sample{SessionID: 4121, RequestID: int64(i)})
		}
		diagnostics.RecordResponse(time.Now(), 0, map[string]any{"dialect": "postgres"})

		details := diagnostics.Snapshot().Response.Details
		Expect(seen).To(Equal(recorded))
		Expect(details).To(HaveKeyWithValue("samplesDropped", 7))
		samples := details["samples"].([]sqlactivity.Sample)
		Expect(samples).To(HaveLen(query.DiagnosticSampleLimit))
		Expect([]int64{samples[0].RequestID, samples[len(samples)-1].RequestID}).To(Equal([]int64{7, recorded - 1}))
	})

	It("records a sample error and hands it to OnSampleError", func() {
		var seen []error
		diagnostics := query.NewDiagnostics(query.DiagnosticOptions{
			Provider: "sql", OnSampleError: func(err error) { seen = append(seen, err) },
		})
		cause := errors.New("VIEW SERVER STATE permission was denied")
		diagnostics.RecordSampleError(cause)

		Expect(seen).To(Equal([]error{cause}))
		Expect(diagnostics.Snapshot().Response.Details).To(HaveKeyWithValue("sampleError", cause.Error()))
	})

	DescribeTable("wants a session only when something consumes it",
		func(options query.DiagnosticOptions, wants bool) {
			Expect(query.NewDiagnostics(options).WantsSession()).To(Equal(wants))
		},
		Entry("nothing asked", query.DiagnosticOptions{Provider: "sql"}, false),
		Entry("OnSession", query.DiagnosticOptions{OnSession: func(query.ServerSession) {}}, true),
		Entry("OnSample", query.DiagnosticOptions{OnSample: func(sqlactivity.Sample) {}}, true),
		Entry("AutoDiagnose", query.DiagnosticOptions{AutoDiagnose: time.Second}, true),
		Entry("AutoDiagnose from the start", query.DiagnosticOptions{AutoDiagnose: -1}, true),
	)

	It("is a no-op on nil diagnostics", func() {
		var diagnostics *query.ProviderDiagnostics
		Expect(diagnostics.WantsSession()).To(BeFalse())
		Expect(diagnostics.AutoDiagnoseAfter()).To(BeZero())
		diagnostics.RecordSession(query.ServerSession{Engine: "postgres"})
		diagnostics.RecordSample(sqlactivity.Sample{})
		diagnostics.RecordSampleError(errors.New("unreachable"))
	})
})
