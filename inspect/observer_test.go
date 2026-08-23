package inspect_test

import (
	"context"
	"errors"
	"sync"
	"time"

	inspection "github.com/flanksource/commons-db/inspect"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type recordingObserver struct {
	mu   sync.Mutex
	seen []inspection.Observation
}

func (o *recordingObserver) observe(observation inspection.Observation) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.seen = append(o.seen, observation)
}

func (o *recordingObserver) all() []inspection.Observation {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]inspection.Observation(nil), o.seen...)
}

var _ = Describe("inspection cache observer", func() {
	var clock *fakeClock

	BeforeEach(func() {
		clock = &fakeClock{now: time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)}
	})

	It("distinguishes the caller that paid for the fill from the one that did not", func(ctx SpecContext) {
		memo := inspection.NewMemo(memoOptions(clock, testPolicy()))
		observer := &recordingObserver{}
		watched := inspection.WithObserver(ctx, observer.observe)
		fillDuration := 250 * time.Millisecond
		load := func(context.Context) (string, error) {
			clock.Advance(fillDuration)
			return "catalog", nil
		}

		miss, err := memo.Get(watched, inspection.GetOptions[string]{Key: "targets", Load: load})
		Expect(err).ToNot(HaveOccurred())
		Expect(miss.Value).To(Equal("catalog"))

		hit, err := memo.Get(watched, inspection.GetOptions[string]{Key: "targets", Load: load})
		Expect(err).ToNot(HaveOccurred())
		Expect(hit.Value).To(Equal("catalog"))

		seen := observer.all()
		Expect(seen).To(HaveLen(2))
		Expect(seen[0].Policy).To(Equal(testPolicy().Name))
		Expect(seen[0].Key).To(Equal("targets"))
		Expect(seen[0].Cache.Cached).To(BeFalse())
		Expect(seen[0].Elapsed).To(Equal(fillDuration))
		Expect(seen[1].Cache.Cached).To(BeTrue())
		Expect(seen[1].Elapsed).To(BeZero())
	})

	It("reports the failure the caller was given", func(ctx SpecContext) {
		memo := inspection.NewMemo(memoOptions(clock, testPolicy()))
		observer := &recordingObserver{}
		unreachable := errors.New("connection refused")

		_, err := memo.Get(inspection.WithObserver(ctx, observer.observe), inspection.GetOptions[string]{
			Key:  "fields",
			Load: func(context.Context) (string, error) { return "", unreachable },
		})
		Expect(err).To(MatchError(unreachable))

		seen := observer.all()
		Expect(seen).To(HaveLen(1))
		Expect(seen[0].Err).To(MatchError(unreachable))
		Expect(seen[0].Cache.Cached).To(BeFalse())
	})

	It("stays silent for a lookup made outside any observed request", func(ctx SpecContext) {
		memo := inspection.NewMemo(memoOptions(clock, testPolicy()))
		observer := &recordingObserver{}

		_, err := memo.Get(ctx, inspection.GetOptions[string]{
			Key:  "targets",
			Load: func(context.Context) (string, error) { return "catalog", nil },
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(observer.all()).To(BeEmpty())
	})
})
