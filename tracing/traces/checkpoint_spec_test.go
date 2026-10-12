// Specs for a capture's checkpoints: a host marks a moment in a running
// capture, and a kind that reads at checkpoints records a reading then.

package traces_test

import (
	"context"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/recordstore/recordresults"

	"github.com/flanksource/commons-db/tracing/traces"
	"github.com/flanksource/commons-db/tracing/traces/tracestest"
)

// readings records one tick per checkpoint, numbered in order, and nothing
// on its own.
type readings struct{ taken *atomic.Int64 }

func (readings) Params() tickParams { return tickParams{} }

func (readings) Schema() recordresults.ResultType[tick] {
	return recordresults.ResultType[tick]{Title: "Readings", TimeColumn: "at"}
}

func (readings) Handle(ctx dbcontext.Context, _ tickParams, _ traces.Emitter[tick], _ traces.Records[tick]) error {
	<-ctx.Done()
	return nil
}

func (r readings) Checkpoint(ctx dbcontext.Context, _ tickParams, records traces.Emitter[tick]) error {
	return records.Emit(ctx, tick{At: time.Now().UTC(), N: int(r.taken.Add(1))})
}

var _ = Describe("a capture's checkpoint", func() {
	It("records the kind's reading and commits it before it returns, while the background poll takes none", func() {
		taken := &atomic.Int64{}
		env := tracestest.NewEnv(map[string]traces.TracePlugin{
			"readings": traces.NewHandler[tickParams, tick](readings{taken: taken}, traces.Capabilities{Live: true}),
		}, func(runtime *traces.Runtime) { runtime.PollEvery = 10 * time.Millisecond })
		managed, err := env.Runtime.Start(dbcontext.New(), traces.StartRequest{Kind: "readings"})
		Expect(err).ToNot(HaveOccurred())
		tracestest.Running(managed.Session())

		Consistently(taken.Load).WithTimeout(100*time.Millisecond).Should(BeZero(), "a poll is not a checkpoint")
		Expect(env.Runtime.Checkpoint(context.Background(), managed)).To(Succeed())
		Expect(managed.Session().Snapshot().EventCount).To(Equal(int64(1)), "the reading is committed by the checkpoint itself")
		Expect(env.Runtime.Checkpoint(context.Background(), managed)).To(Succeed())

		managed.Session().Stop("spec")
		Expect(numbers(env.Rows(tracestest.Ended(managed.Session())))).To(Equal([]string{"1", "2"}))
	})

	It("only commits what a kind that takes no readings already emitted", func() {
		env := tracestest.NewEnv(map[string]traces.TracePlugin{"ticks": liveTicks()})
		managed, err := env.Runtime.Start(dbcontext.New(), traces.StartRequest{Kind: "ticks", Params: []byte(`{"count": 2}`)})
		Expect(err).ToNot(HaveOccurred())
		tracestest.Running(managed.Session())

		Eventually(func() int64 {
			Expect(env.Runtime.Checkpoint(context.Background(), managed)).To(Succeed())
			return managed.Session().Snapshot().EventCount
		}).WithTimeout(5 * time.Second).Should(Equal(int64(2)))
		managed.Session().Stop("spec")
		Expect(numbers(env.Rows(tracestest.Ended(managed.Session())))).To(Equal([]string{"1", "2"}))
	})
})
