package inspect_test

import (
	"context"
	"sync/atomic"
	"time"

	inspection "github.com/flanksource/commons-db/inspect"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The registry is process-wide and append-only, so these specs assert on the
// caches they created rather than on the whole list — every other spec file in
// this package contributes memos to it too.
func statsFor(policy string) (inspection.CacheStats, bool) {
	for _, stats := range inspection.Stats() {
		if stats.Policy == policy {
			return stats, true
		}
	}
	return inspection.CacheStats{}, false
}

var _ = Describe("inspection cache registry", func() {
	var clock *fakeClock

	BeforeEach(func() {
		clock = &fakeClock{now: time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)}
	})

	policyNamed := func(name string) inspection.CachePolicy {
		return inspection.CachePolicy{
			Name: name, InitialFreshFor: time.Hour, MaximumFreshFor: 24 * time.Hour,
			FillTimeout: time.Second, MaxEntries: 10, MaxWeight: 10,
		}
	}

	It("makes a memo reachable without the package that owns it saying so", func(ctx SpecContext) {
		policy := policyNamed("registry-reachable")
		memo := inspection.NewMemo(memoOptions(clock, policy))

		_, err := memo.Get(ctx, inspection.GetOptions[string]{
			Key: "a", Load: func(context.Context) (string, error) { return "value", nil },
		})
		Expect(err).ToNot(HaveOccurred())

		stats, found := statsFor(policy.Name)
		Expect(found).To(BeTrue())
		Expect(stats.Entries).To(Equal(1))
		Expect(stats.MaxEntries).To(Equal(policy.MaxEntries))
		Expect(inspection.PolicyNames()).To(ContainElement(policy.Name))
	})

	It("drops only the cache it was aimed at, and says how much went", func(ctx SpecContext) {
		kept := inspection.NewMemo(memoOptions(clock, policyNamed("registry-kept")))
		flushed := inspection.NewMemo(memoOptions(clock, policyNamed("registry-flushed")))
		load := func(context.Context) (string, error) { return "value", nil }
		for _, key := range []string{"a", "b"} {
			_, err := kept.Get(ctx, inspection.GetOptions[string]{Key: key, Load: load})
			Expect(err).ToNot(HaveOccurred())
			_, err = flushed.Get(ctx, inspection.GetOptions[string]{Key: key, Load: load})
			Expect(err).ToNot(HaveOccurred())
		}

		result := inspection.Flush(inspection.FlushOptions{Policy: "registry-flushed"})

		Expect(result.Entries).To(Equal(2))
		Expect(result.Caches).To(ConsistOf(inspection.FlushedCache{Policy: "registry-flushed", Entries: 2}))
		Expect(flushed.Stats().Entries).To(BeZero())
		Expect(kept.Stats().Entries).To(Equal(2))
	})

	// "Nothing was dropped" and "the flush did not work" look identical from
	// outside, so the count is the whole point of the response.
	It("reports an empty flush rather than claiming a cache it did not touch", func() {
		inspection.NewMemo(memoOptions(clock, policyNamed("registry-empty")))

		result := inspection.Flush(inspection.FlushOptions{Policy: "registry-empty"})

		Expect(result.Entries).To(BeZero())
		Expect(result.Caches).To(BeEmpty())
	})

	It("drops one key without disturbing its neighbours", func(ctx SpecContext) {
		memo := inspection.NewMemo(memoOptions(clock, policyNamed("registry-by-key")))
		load := func(context.Context) (string, error) { return "value", nil }
		for _, key := range []string{"keep", "drop"} {
			_, err := memo.Get(ctx, inspection.GetOptions[string]{Key: key, Load: load})
			Expect(err).ToNot(HaveOccurred())
		}

		result := inspection.Flush(inspection.FlushOptions{Policy: "registry-by-key", Key: "drop"})

		Expect(result.Entries).To(Equal(1))
		Expect(memo.Stats().Entries).To(Equal(1))
	})

	// A fill already running when the flush lands must not be allowed to store
	// its result afterwards — that would resurrect exactly what was dropped, and
	// make the flush look intermittent rather than broken.
	It("does not let a fill in flight survive the flush that overtook it", func(ctx SpecContext) {
		memo := inspection.NewMemo(memoOptions(clock, policyNamed("registry-inflight")))
		release := make(chan struct{})
		var loads atomic.Int32
		load := func(context.Context) (string, error) {
			loads.Add(1)
			<-release
			return "late", nil
		}

		go func() {
			defer GinkgoRecover()
			_, _ = memo.Get(context.WithoutCancel(ctx), inspection.GetOptions[string]{Key: "slow", Load: load})
		}()
		Eventually(loads.Load).Should(Equal(int32(1)))

		inspection.Flush(inspection.FlushOptions{Policy: "registry-inflight"})
		close(release)

		Consistently(func() int { return memo.Stats().Entries }).Should(BeZero())
	})
})
