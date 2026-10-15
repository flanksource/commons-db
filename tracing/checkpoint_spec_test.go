// Specs for a capture's checkpoints: a host marks a moment in a running
// capture, and a kind that reads at checkpoints records a reading then.

package tracing_test

import (
	"context"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/recordstore/recordresults"
	"github.com/flanksource/commons-db/tracing"

	"github.com/flanksource/commons-db/tracing/tracingtest"
)

// readings records one tick per checkpoint, numbered in order, and nothing
// on its own.
type readings struct{ taken *atomic.Int64 }

func (readings) Params() tickParams { return tickParams{} }

func (readings) Schema() recordresults.ResultType[tick] {
	return recordresults.ResultType[tick]{Title: "Readings", TimeColumn: "at"}
}

func (readings) Handle(ctx dbcontext.Context, _ tickParams, _ tracing.Emitter[tick], _ tracing.Records[tick]) error {
	<-ctx.Done()
	return nil
}

func (r readings) Checkpoint(ctx dbcontext.Context, _ tickParams, records tracing.Emitter[tick]) error {
	return records.Emit(ctx, tick{At: time.Now().UTC(), N: int(r.taken.Add(1))})
}

// preparedReadings takes its readings from what Prepare set up.
type preparedReadings struct{ readings }

type readingBase struct{}

func (preparedReadings) Prepare(ctx dbcontext.Context, _ tickParams, _ tracing.Emitter[tick]) (dbcontext.Context, func() error, error) {
	return ctx.WithValue(readingBase{}, 100), nil, nil
}

func (r preparedReadings) Checkpoint(ctx dbcontext.Context, _ tickParams, records tracing.Emitter[tick]) error {
	base, _ := ctx.Value(readingBase{}).(int)
	return records.Emit(ctx, tick{At: time.Now().UTC(), N: base + int(r.taken.Add(1))})
}

var _ = Describe("a capture's checkpoint", func() {
	It("reads under what Prepare set up, bounded by the host's context", func() {
		env := tracingtest.NewEnv(map[string]tracing.TracePlugin{
			"readings": tracing.NewHandler[tickParams, tick](preparedReadings{readings{taken: &atomic.Int64{}}}, tracing.Capabilities{Live: true}),
		})
		managed, err := env.Runtime.Start(dbcontext.New(), tracing.StartRequest{Kind: "readings"})
		Expect(err).ToNot(HaveOccurred())
		tracingtest.Running(managed.Session())

		Expect(env.Runtime.Checkpoint(context.Background(), managed)).To(Succeed())
		cancelled, cancel := context.WithCancel(context.Background())
		cancel()
		Expect(env.Runtime.Checkpoint(cancelled, managed)).ToNot(Succeed(), "a checkpoint whose host gave up does not wait")

		managed.Session().Stop("spec")
		Expect(numbers(env.Rows(tracingtest.Ended(managed.Session())))).To(ContainElement("101"))
	})

	It("records the kind's reading and commits it before it returns, while the background poll takes none", func() {
		taken := &atomic.Int64{}
		env := tracingtest.NewEnv(map[string]tracing.TracePlugin{
			"readings": tracing.NewHandler[tickParams, tick](readings{taken: taken}, tracing.Capabilities{Live: true}),
		}, func(runtime *tracing.Runtime) { runtime.PollEvery = 10 * time.Millisecond })
		managed, err := env.Runtime.Start(dbcontext.New(), tracing.StartRequest{Kind: "readings"})
		Expect(err).ToNot(HaveOccurred())
		tracingtest.Running(managed.Session())

		Consistently(taken.Load).WithTimeout(100*time.Millisecond).Should(BeZero(), "a poll is not a checkpoint")
		Expect(env.Runtime.Checkpoint(context.Background(), managed)).To(Succeed())
		Expect(managed.Session().Snapshot().EventCount).To(Equal(int64(1)), "the reading is committed by the checkpoint itself")
		Expect(env.Runtime.Checkpoint(context.Background(), managed)).To(Succeed())

		managed.Session().Stop("spec")
		Expect(numbers(env.Rows(tracingtest.Ended(managed.Session())))).To(Equal([]string{"1", "2"}))
	})

	It("checkpoints a running capture named by its session id, and refuses an id that names none", func() {
		taken := &atomic.Int64{}
		env := tracingtest.NewEnv(map[string]tracing.TracePlugin{
			"readings": tracing.NewHandler[tickParams, tick](readings{taken: taken}, tracing.Capabilities{Live: true}),
		})
		managed, err := env.Runtime.Start(dbcontext.New(), tracing.StartRequest{Kind: "readings"})
		Expect(err).ToNot(HaveOccurred())
		tracingtest.Running(managed.Session())

		Expect(env.Runtime.CheckpointSession(context.Background(), managed.Session().ID())).To(Succeed())
		Expect(managed.Session().Snapshot().EventCount).To(Equal(int64(1)))
		Expect(env.Runtime.CheckpointSession(context.Background(), "no-such-session")).To(MatchError(tracing.ErrNoCapture))

		managed.Session().Stop("spec")
		tracingtest.Ended(managed.Session())
		Expect(env.Runtime.CheckpointSession(context.Background(), managed.Session().ID())).To(MatchError(tracing.ErrNoCapture), "an ended capture takes no checkpoint")
	})

	It("only commits what a kind that takes no readings already emitted", func() {
		env := tracingtest.NewEnv(map[string]tracing.TracePlugin{"ticks": liveTicks()})
		managed, err := env.Runtime.Start(dbcontext.New(), tracing.StartRequest{Kind: "ticks", Params: []byte(`{"count": 2}`)})
		Expect(err).ToNot(HaveOccurred())
		tracingtest.Running(managed.Session())

		Eventually(func() int64 {
			Expect(env.Runtime.Checkpoint(context.Background(), managed)).To(Succeed())
			return managed.Session().Snapshot().EventCount
		}).WithTimeout(5 * time.Second).Should(Equal(int64(2)))
		managed.Session().Stop("spec")
		Expect(numbers(env.Rows(tracingtest.Ended(managed.Session())))).To(Equal([]string{"1", "2"}))
	})
})
