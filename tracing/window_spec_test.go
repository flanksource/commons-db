// Specs for what a host reads of a running capture: the environment its
// Prepare exports for the processes it observes, and the exact seq windows
// between the marks the host takes.

package tracing_test

import (
	"context"
	"sync/atomic"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/recordstore/recordresults"
	"github.com/flanksource/commons-db/tracing"
	"github.com/flanksource/commons-db/tracing/tracingtest"
)

// exporting exports the pairs its spec names from Prepare, and the late
// pairs from Handle, once the capture runs.
type exporting struct {
	prepare, late [][2]string
}

func (exporting) Params() tickParams { return tickParams{} }

func (exporting) Schema() recordresults.ResultType[tick] {
	return recordresults.ResultType[tick]{Title: "Exporting", TimeColumn: "at"}
}

func (e exporting) Prepare(ctx dbcontext.Context, _ tickParams, records tracing.Emitter[tick]) (dbcontext.Context, func() error, error) {
	for _, pair := range e.prepare {
		records.Export(pair[0], pair[1])
	}
	return ctx, nil, nil
}

func (e exporting) Handle(ctx dbcontext.Context, _ tickParams, records tracing.Emitter[tick], _ tracing.Records[tick]) error {
	for _, pair := range e.late {
		records.Export(pair[0], pair[1])
	}
	<-ctx.Done()
	return nil
}

func exportingEnv(kind exporting) tracingtest.Env {
	return tracingtest.NewEnv(map[string]tracing.TracePlugin{
		"exporting": tracing.NewHandler[tickParams, tick](kind, tracing.Capabilities{Live: true}),
	})
}

var _ = Describe("a capture's exports", func() {
	It("are on the session as soon as Start returns", func() {
		env := exportingEnv(exporting{prepare: [][2]string{{"HTTPS_PROXY", "http://127.0.0.1:41234"}, {"NO_PROXY", "localhost"}}})
		managed, err := env.Runtime.Start(dbcontext.New(), tracing.StartRequest{Kind: "exporting"})
		Expect(err).ToNot(HaveOccurred())
		want := map[string]string{"HTTPS_PROXY": "http://127.0.0.1:41234", "NO_PROXY": "localhost"}
		Expect(managed.Status().Exports).To(Equal(want))
		Expect(managed.Session().Snapshot().Exports).To(Equal(want))
	})

	DescribeTable("refuse the start when Prepare exports what no process could be given",
		func(pairs [][2]string, message string) {
			env := exportingEnv(exporting{prepare: pairs})
			_, err := env.Runtime.Start(dbcontext.New(), tracing.StartRequest{Kind: "exporting"})
			Expect(err).To(MatchError(ContainSubstring(message)))
		},
		Entry("a name that is not an environment variable name", [][2]string{{"HTTP-PROXY", "x"}}, "environment variable name"),
		Entry("one name twice", [][2]string{{"PGHOST", "a"}, {"PGHOST", "b"}}, `"PGHOST" twice`),
	)

	It("are never changed once the capture runs: a late export warns the session", func() {
		env := exportingEnv(exporting{late: [][2]string{{"LATE", "x"}}})
		session := env.Start("exporting", "")
		tracingtest.Running(session)
		session.Stop("spec")
		info := tracingtest.Ended(session)
		Expect(info.Exports).To(BeEmpty())
		Expect(info.Warning).To(ContainSubstring(`export "LATE"`))
	})
})

var _ = Describe("a capture's seq windows", func() {
	var env tracingtest.Env
	var managed *query.ManagedSession

	BeforeEach(func() {
		env = tracingtest.NewEnv(map[string]tracing.TracePlugin{
			"readings": tracing.NewHandler[tickParams, tick](readings{taken: &atomic.Int64{}}, tracing.Capabilities{Live: true}),
			"ticks":    liveTicks(),
		})
		var err error
		managed, err = env.Runtime.Start(dbcontext.New(), tracing.StartRequest{Kind: "readings"})
		Expect(err).ToNot(HaveOccurred())
		tracingtest.Running(managed.Session())
	})

	mark := func() int64 {
		seq, err := env.Runtime.Mark(context.Background(), managed)
		Expect(err).ToNot(HaveOccurred())
		return seq
	}

	readings := func(ref tracing.TraceRef) []int {
		var read []int
		Expect(tracing.ReadRecords(context.Background(), env.Runtime, ref, func(record tick) error {
			read = append(read, record.N)
			return nil
		})).To(Succeed())
		return read
	}

	It("read exactly the records committed between two marks, each mark committing a reading", func() {
		stream := managed.Session().Snapshot().Events.Stream
		before := mark()
		mark()
		after := mark()
		mark()
		Expect(readings(tracing.TraceRef{Kind: "readings", Stream: stream, AfterSeq: before, ToSeq: after})).To(Equal([]int{2, 3}))
		Expect(readings(tracing.TraceRef{Kind: "readings", Stream: stream, AfterSeq: after})).To(Equal([]int{4}), "a window to 0 runs to the end")

		var seqs []int64
		Expect(env.Runtime.Read(context.Background(), tracing.TraceRef{Kind: "readings", Stream: stream, AfterSeq: before, ToSeq: after},
			func(seq int64, _ recordstore.Row) error {
				seqs = append(seqs, seq)
				return nil
			})).To(Succeed())
		Expect(seqs).To(HaveLen(2))
		Expect(seqs[1]).To(Equal(after))

		ref := tracing.TraceRef{Kind: "readings", Stream: stream, AfterSeq: before, ToSeq: after}
		Expect(env.Read("readings", stream, query.PageRequest{Limit: 50}, ref.Params())).To(HaveLen(2), "the kind's profile reads the same window")
	})

	It("mark a stream that has committed nothing at seq 0", func() {
		empty, err := env.Runtime.Start(dbcontext.New(), tracing.StartRequest{Kind: "ticks", Params: []byte(`{"count": 0}`)})
		Expect(err).ToNot(HaveOccurred())
		Expect(env.Runtime.Mark(context.Background(), empty)).To(Equal(int64(0)))
	})

	DescribeTable("refuse a ref they cannot read",
		func(ref func(stream string) tracing.TraceRef, want error, message string) {
			err := env.Runtime.Read(context.Background(), ref(managed.Session().Snapshot().Events.Stream),
				func(int64, recordstore.Row) error { return nil })
			Expect(err).To(MatchError(want))
			Expect(err).To(MatchError(ContainSubstring(message)))
		},
		Entry("an unknown kind", func(stream string) tracing.TraceRef { return tracing.TraceRef{Kind: "nope", Stream: stream} },
			tracing.ErrUnknownKind, "nope"),
		Entry("another kind's stream", func(stream string) tracing.TraceRef { return tracing.TraceRef{Kind: "ticks", Stream: stream} },
			recordstore.ErrNotFound, "holds readings records"),
		Entry("no stream", func(string) tracing.TraceRef { return tracing.TraceRef{Kind: "readings"} },
			tracing.ErrInvalidRef, "stream"),
		Entry("a window ending before it starts", func(stream string) tracing.TraceRef {
			return tracing.TraceRef{Kind: "readings", Stream: stream, AfterSeq: 5, ToSeq: 2}
		}, tracing.ErrInvalidRef, "5"),
	)

	It("refuse to decode a kind's records as another type", func() {
		ref := tracing.TraceRef{Kind: "readings", Stream: managed.Session().Snapshot().Events.Stream}
		err := tracing.ReadRecords(context.Background(), env.Runtime, ref, func(exchange) error { return nil })
		Expect(err).To(MatchError(ContainSubstring("stores tracing_test.tick records")))
	})
})
