// Specs for what a capture reports beyond its records: the handle and metadata
// it describes itself with, its own summary, and the warnings it ends with.

package tracing_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore/recordresults"
	"github.com/flanksource/commons-db/tracing"
	"github.com/flanksource/commons-db/tracing/tracingtest"
)

// preparedIn and handledIn receive the stream a reporting capture was told it
// writes, as it prepares and as it runs.
var preparedIn, handledIn chan string

// reporting is a tick kind that describes its server-side session as it
// prepares, then reports what it lost as it runs.
type reporting struct{}

type lossReport struct {
	Lost int `json:"lost"`
}

func (reporting) Params() tickParams { return tickParams{} }

func (reporting) Schema() recordresults.ResultType[tick] {
	return recordresults.ResultType[tick]{Title: "Ticks", TimeColumn: "at"}
}

func (reporting) Prepare(ctx dbcontext.Context, _ tickParams, records tracing.Emitter[tick]) (dbcontext.Context, func() error, error) {
	preparedIn <- tracing.StreamOf(ctx)
	records.Describe("xe_capture_1", query.SessionMetadata{Name: "statements", Label: "Started with", Language: "sql", Value: "CREATE EVENT SESSION xe_capture_1;"})
	return ctx, nil, nil
}

func (reporting) Handle(ctx dbcontext.Context, _ tickParams, records tracing.Emitter[tick], _ tracing.Records[tick]) error {
	handledIn <- tracing.StreamOf(ctx)
	if err := emitTicks(ctx, records, 1); err != nil {
		return err
	}
	records.Report(lossReport{Lost: 2})
	<-ctx.Done()
	records.Report(lossReport{Lost: 5})
	records.Warn("lost 5 events before they could be read")
	return nil
}

var _ = Describe("a capture's report", func() {
	var env tracingtest.Env

	BeforeEach(func() {
		preparedIn, handledIn = make(chan string, 1), make(chan string, 1)
		env = tracingtest.NewEnv(map[string]tracing.TracePlugin{
			"reporting": tracing.NewHandler[tickParams, tick](reporting{}, tracing.Capabilities{Live: true}),
		})
	})

	It("names the session by the handle and metadata the kind described as it prepared", func() {
		session := env.Start("reporting", `{}`)
		tracingtest.Running(session)
		running := session.Snapshot()
		Expect(running.Handle).To(Equal("xe_capture_1"))
		Expect(running.Metadata).To(ConsistOf(query.SessionMetadata{
			Name: "statements", Label: "Started with", Language: "sql", Value: "CREATE EVENT SESSION xe_capture_1;",
		}))
	})

	It("shows the kind's latest report while it runs and keeps its last one when it ends", func() {
		session := env.Start("reporting", `{}`)
		Eventually(func() any {
			return tracingtest.Summary(session.Snapshot()).Report
		}).Should(Equal(map[string]any{"lost": float64(2)}))

		session.Stop("user")
		info := tracingtest.Ended(session)
		summary := tracingtest.Summary(info)
		Expect(summary.Report).To(Equal(map[string]any{"lost": float64(5)}))
		Expect(summary.Emitted).To(Equal(int64(1)))
		Expect(info.Handle).To(Equal("xe_capture_1"))
	})

	It("tells the capture the stream its records are written to, so it can name companion streams after it", func() {
		session := env.Start("reporting", `{}`)
		tracingtest.Running(session)
		stream := session.Snapshot().Events.Stream
		Expect(stream).ToNot(BeEmpty())
		Eventually(preparedIn).Should(Receive(Equal(stream)))
		Eventually(handledIn).Should(Receive(Equal(stream)))
	})

	It("ends the session with the warnings the kind raised", func() {
		session := env.Start("reporting", `{}`)
		tracingtest.Running(session)
		session.Stop("user")
		info := tracingtest.Ended(session)
		Expect(info.Error).To(BeEmpty())
		Expect(info.Warning).To(Equal("lost 5 events before they could be read"))
	})
})
