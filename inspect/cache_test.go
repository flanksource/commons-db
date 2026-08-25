package inspect_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
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
