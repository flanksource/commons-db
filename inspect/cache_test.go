package inspect_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	inspection "github.com/flanksource/commons-db/inspect"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(elapsed time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(elapsed)
}

func memoOptions(clock *fakeClock, policy inspection.CachePolicy) inspection.MemoOptions[string] {
	return inspection.MemoOptions[string]{
		Policy: policy,
		Now:    clock.Now,
		Weight: func(string) int { return 1 },
	}
}

func testPolicy() inspection.CachePolicy {
	return inspection.CachePolicy{
		Name: "test", InitialFreshFor: time.Hour, MaximumFreshFor: 24 * time.Hour,
		FillTimeout: time.Second, MaxEntries: 10, MaxWeight: 10,
	}
}

var _ = Describe("inspection cache", func() {
	var clock *fakeClock

	BeforeEach(func() {
		clock = &fakeClock{now: time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)}
	})

	It("returns stale metadata immediately while one background refresh runs", func(ctx SpecContext) {
		memo := inspection.NewMemo(memoOptions(clock, testPolicy()))
		started := make(chan struct{})
		release := make(chan struct{})
		var calls atomic.Int32
		load := func(context.Context) (string, error) {
			if calls.Add(1) == 1 {
				return "catalog-v1", nil
			}
			close(started)
			<-release
			return "catalog-v2", nil
		}

		first, err := memo.Get(ctx, inspection.GetOptions[string]{Key: "catalog", Load: load})
		Expect(err).ToNot(HaveOccurred())
		Expect(first.Value).To(Equal("catalog-v1"))
		Expect(first.Cache.State).To(Equal(inspection.CacheStateFresh))

		clock.Advance(time.Hour + time.Second)
		stale, err := memo.Get(ctx, inspection.GetOptions[string]{Key: "catalog", Load: load})
		Expect(err).ToNot(HaveOccurred())
		Expect(stale.Value).To(Equal("catalog-v1"))
		Expect(stale.Cache.State).To(Equal(inspection.CacheStateStale))
		Expect(stale.Cache.Refreshing).To(BeTrue())
		Eventually(started).Should(BeClosed())
		close(release)

		Eventually(func(g Gomega) {
			current, err := memo.Get(ctx, inspection.GetOptions[string]{Key: "catalog", Load: load})
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(current.Value).To(Equal("catalog-v2"))
			g.Expect(current.Cache.State).To(Equal(inspection.CacheStateFresh))
		}).Should(Succeed())
		Expect(calls.Load()).To(Equal(int32(2)))
	})

	It("retains stale metadata and reports a failed background refresh", func(ctx SpecContext) {
		memo := inspection.NewMemo(memoOptions(clock, testPolicy()))
		var calls atomic.Int32
		load := func(context.Context) (string, error) {
			if calls.Add(1) == 1 {
				return "last-known-good", nil
			}
			return "", errors.New("source unavailable")
		}
		_, err := memo.Get(ctx, inspection.GetOptions[string]{Key: "catalog", Load: load})
		Expect(err).ToNot(HaveOccurred())
		clock.Advance(time.Hour + time.Second)

		stale, err := memo.Get(ctx, inspection.GetOptions[string]{Key: "catalog", Load: load})
		Expect(err).ToNot(HaveOccurred())
		Expect(stale.Value).To(Equal("last-known-good"))
		Eventually(func(g Gomega) {
			current, err := memo.Get(ctx, inspection.GetOptions[string]{Key: "catalog", Load: load})
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(current.Value).To(Equal("last-known-good"))
			g.Expect(current.Cache.LastRefreshError).To(Equal("source unavailable"))
			g.Expect(current.Cache.Refreshing).To(BeFalse())
		}).Should(Succeed())
		Consistently(func() int32 { return calls.Load() }).Should(Equal(int32(2)))
	})

	It("extends freshness after an unchanged refresh and resets it after a change", func(ctx SpecContext) {
		memo := inspection.NewMemo(memoOptions(clock, testPolicy()))
		value := "stable"
		load := func(context.Context) (string, error) { return value, nil }
		first, err := memo.Get(ctx, inspection.GetOptions[string]{Key: "catalog", Load: load})
		Expect(err).ToNot(HaveOccurred())
		Expect(first.Cache.FreshUntil.Sub(first.Cache.LoadedAt)).To(Equal(time.Hour))

		clock.Advance(time.Hour + time.Second)
		unchanged, err := memo.Get(ctx, inspection.GetOptions[string]{Key: "catalog", Load: load, Refresh: true})
		Expect(err).ToNot(HaveOccurred())
		Expect(unchanged.Cache.UnchangedRefreshes).To(Equal(1))
		Expect(unchanged.Cache.FreshUntil.Sub(unchanged.Cache.LoadedAt)).To(Equal(2 * time.Hour))

		value = "changed"
		changed, err := memo.Get(ctx, inspection.GetOptions[string]{Key: "catalog", Load: load, Refresh: true})
		Expect(err).ToNot(HaveOccurred())
		Expect(changed.Value).To(Equal("changed"))
		Expect(changed.Cache.UnchangedRefreshes).To(BeZero())
		Expect(changed.Cache.FreshUntil.Sub(changed.Cache.LoadedAt)).To(Equal(time.Hour))
		Expect(changed.Cache.LastChangedAt).To(Equal(changed.Cache.LoadedAt))
	})

	It("does not let an older background fill overwrite a manual refresh", func(ctx SpecContext) {
		memo := inspection.NewMemo(memoOptions(clock, testPolicy()))
		backgroundStarted := make(chan struct{})
		releaseBackground := make(chan struct{})
		var calls atomic.Int32
		load := func(context.Context) (string, error) {
			switch calls.Add(1) {
			case 1:
				return "v1", nil
			case 2:
				close(backgroundStarted)
				<-releaseBackground
				return "v2-old", nil
			default:
				return "v3-manual", nil
			}
		}
		_, err := memo.Get(ctx, inspection.GetOptions[string]{Key: "catalog", Load: load})
		Expect(err).ToNot(HaveOccurred())
		clock.Advance(time.Hour + time.Second)
		_, err = memo.Get(ctx, inspection.GetOptions[string]{Key: "catalog", Load: load})
		Expect(err).ToNot(HaveOccurred())
		Eventually(backgroundStarted).Should(BeClosed())

		manual, err := memo.Get(ctx, inspection.GetOptions[string]{Key: "catalog", Load: load, Refresh: true})
		Expect(err).ToNot(HaveOccurred())
		Expect(manual.Value).To(Equal("v3-manual"))
		close(releaseBackground)
		Consistently(func() string {
			current, _ := memo.Get(ctx, inspection.GetOptions[string]{Key: "catalog", Load: load})
			return current.Value
		}).Should(Equal("v3-manual"))
	})

	It("returns stale metadata with the manual refresh error", func(ctx SpecContext) {
		memo := inspection.NewMemo(memoOptions(clock, testPolicy()))
		var calls atomic.Int32
		load := func(context.Context) (string, error) {
			if calls.Add(1) == 1 {
				return "cached", nil
			}
			return "", errors.New("refresh failed")
		}
		_, err := memo.Get(ctx, inspection.GetOptions[string]{Key: "catalog", Load: load})
		Expect(err).ToNot(HaveOccurred())
		result, err := memo.Get(ctx, inspection.GetOptions[string]{Key: "catalog", Load: load, Refresh: true})
		Expect(err).To(MatchError("refresh failed"))
		Expect(result.Value).To(Equal("cached"))
		Expect(result.Cache.State).To(Equal(inspection.CacheStateStale))
		Expect(result.Cache.LastRefreshError).To(Equal("refresh failed"))
	})

	It("evicts least-recently-used entries by count", func(ctx SpecContext) {
		policy := testPolicy()
		policy.MaxEntries, policy.MaxWeight = 2, 2
		memo := inspection.NewMemo(memoOptions(clock, policy))
		var loads atomic.Int32
		get := func(key string) string {
			result, err := memo.Get(ctx, inspection.GetOptions[string]{
				Key: key,
				Load: func(context.Context) (string, error) {
					loads.Add(1)
					return key, nil
				},
			})
			Expect(err).ToNot(HaveOccurred())
			return result.Value
		}
		Expect(get("one")).To(Equal("one"))
		Expect(get("two")).To(Equal("two"))
		Expect(get("one")).To(Equal("one"))
		Expect(get("three")).To(Equal("three"))
		Expect(get("two")).To(Equal("two"))
		Expect(loads.Load()).To(Equal(int32(4)))
	})

	It("evicts least-recently-used entries by weight", func(ctx SpecContext) {
		policy := testPolicy()
		policy.MaxWeight = 5
		memo := inspection.NewMemo(inspection.MemoOptions[string]{
			Policy: policy, Now: clock.Now, Weight: func(value string) int { return len(value) },
		})
		var loads atomic.Int32
		get := func(key, value string) {
			_, err := memo.Get(ctx, inspection.GetOptions[string]{
				Key: key,
				Load: func(context.Context) (string, error) {
					loads.Add(1)
					return value, nil
				},
			})
			Expect(err).ToNot(HaveOccurred())
		}
		get("one", "111")
		get("two", "222")
		get("one", "111")
		Expect(loads.Load()).To(Equal(int32(3)))
	})

	It("does not cache an initial fill failure", func(ctx SpecContext) {
		memo := inspection.NewMemo(memoOptions(clock, testPolicy()))
		var calls atomic.Int32
		load := func(context.Context) (string, error) {
			if calls.Add(1) == 1 {
				return "", errors.New("unavailable")
			}
			return "ready", nil
		}
		_, err := memo.Get(ctx, inspection.GetOptions[string]{Key: "catalog", Load: load})
		Expect(err).To(MatchError("unavailable"))
		result, err := memo.Get(ctx, inspection.GetOptions[string]{Key: "catalog", Load: load})
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Value).To(Equal("ready"))
		Expect(calls.Load()).To(Equal(int32(2)))
	})
})

var _ = Describe("inspection cache policies", func() {
	DescribeTable("match limited-volatility freshness windows",
		func(class inspection.CacheClass, initial, maximum time.Duration) {
			policy := inspection.Policy(class)
			Expect(policy.InitialFreshFor).To(Equal(initial))
			Expect(policy.MaximumFreshFor).To(Equal(maximum))
			Expect(policy.MaxEntries).To(BeNumerically(">", 0))
			Expect(policy.MaxWeight).To(BeNumerically(">", 0))
		},
		Entry("OpenSearch target names", inspection.CacheClassOpenSearchTargets, 6*time.Hour, 24*time.Hour),
		Entry("SQL catalogs", inspection.CacheClassSQLCatalog, 24*time.Hour, 7*24*time.Hour),
		Entry("dynamic mappings", inspection.CacheClassOpenSearchDynamicMapping, 24*time.Hour, 7*24*time.Hour),
		Entry("concrete mappings", inspection.CacheClassOpenSearchConcreteMapping, 7*24*time.Hour, 30*24*time.Hour),
		Entry("cardinality", inspection.CacheClassCardinality, 7*24*time.Hour, 30*24*time.Hour),
		Entry("filter values", inspection.CacheClassFilterValues, 24*time.Hour, 7*24*time.Hour),
	)
})

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

func TestInspect(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Inspection Cache Suite")
}
