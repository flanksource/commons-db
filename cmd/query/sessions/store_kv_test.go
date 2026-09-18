package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"strings"
	"sync"
	"time"

	"github.com/flanksource/clicky/cache"
	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/query"
)

// ttlRecordingCache is a memory cache that remembers the ttl of every Set, so
// a spec can see the expiry a store pinned without waiting for it.
type ttlRecordingCache struct {
	cache.Store
	mu   sync.Mutex
	ttls map[string]time.Duration
}

func newTTLRecordingCache() *ttlRecordingCache {
	return &ttlRecordingCache{Store: cache.NewMemory(), ttls: map[string]time.Duration{}}
}

func (c *ttlRecordingCache) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	c.mu.Lock()
	c.ttls[key] = ttl
	c.mu.Unlock()
	return c.Store.Set(ctx, key, value, ttl)
}

func (c *ttlRecordingCache) ttl(key string) time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ttls[key]
}

// countingCache counts how often each session's start key is read, so a spec
// can see which records a request read and how many times.
type countingCache struct {
	cache.Store
	mu    sync.Mutex
	reads map[string]int
}

func (c *countingCache) MGet(ctx context.Context, keys iter.Seq[string]) iter.Seq2[cache.Entry, error] {
	c.mu.Lock()
	for key := range keys {
		if strings.HasSuffix(key, ":start") {
			c.reads[key]++
		}
	}
	c.mu.Unlock()
	return c.Store.MGet(ctx, keys)
}

func (c *countingCache) Get(ctx context.Context, key string) ([]byte, error) {
	c.mu.Lock()
	if strings.HasSuffix(key, ":start") {
		c.reads[key]++
	}
	c.mu.Unlock()
	return c.Store.Get(ctx, key)
}

// readsOf is how often session id's start key was read under any prefix.
func (c *countingCache) readsOf(id string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	total := 0
	for key, reads := range c.reads {
		if strings.HasSuffix(key, "sessions:v1:"+id+":start") {
			total += reads
		}
	}
	return total
}

func (c *countingCache) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reads = map[string]int{}
}

var _ = ginkgo.Describe("KVStore", func() {
	ginkgo.Describe("over the memory cache", func() {
		describeSessionStoreContract(func() query.SessionStore {
			store, err := NewMemoryKVStore(KVStoreOptions{})
			Expect(err).ToNot(HaveOccurred())
			return store
		})
	})

	const prefix = "oipa.lab:"
	var (
		ctx     context.Context
		backing *ttlRecordingCache
		store   *KVStore
		epoch   time.Time
	)

	ginkgo.BeforeEach(func() {
		ctx, backing, epoch = context.Background(), newTTLRecordingCache(), contractEpoch()
		var err error
		store, err = NewKVStore(func(context.Context) (cache.Store, string, error) { return backing, prefix, nil }, KVStoreOptions{TTL: 48 * time.Hour})
		Expect(err).ToNot(HaveOccurred())
	})

	ginkgo.It("writes the start, the status and the index under the environment prefix", func() {
		rec := contractFixture(epoch)[0]
		Expect(store.Begin(ctx, rec)).To(Succeed())

		start, err := backing.Get(ctx, prefix+"sessions:v1:c:start")
		Expect(err).ToNot(HaveOccurred())
		wantStart, _ := json.Marshal(rec.SessionStart)
		Expect(start).To(MatchJSON(wantStart))
		status, err := backing.Get(ctx, prefix+"sessions:v1:c:status")
		Expect(err).ToNot(HaveOccurred())
		wantStatus, _ := json.Marshal(rec.SessionStatus)
		Expect(status).To(MatchJSON(wantStatus))

		indexed, err := backing.ZRangeByScore(ctx, prefix+"sessions:v1:index",
			cache.Inclusive(float64(rec.StartedAt.UnixMilli())), cache.Inclusive(float64(rec.StartedAt.UnixMilli())))
		Expect(err).ToNot(HaveOccurred())
		Expect(indexed).To(Equal([]string{"c"}))
	})

	ginkgo.It("deletes one session record and removes it from the index", func() {
		records := contractFixture(epoch)
		Expect(store.Begin(ctx, records[0])).To(Succeed())
		Expect(store.Begin(ctx, records[1])).To(Succeed())

		Expect(store.Delete(ctx, records[0].ID)).To(Succeed())
		_, found, err := store.Get(ctx, records[0].ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse())
		page, err := store.List(ctx, query.SessionFilter{})
		Expect(err).NotTo(HaveOccurred())
		Expect(page.Items).To(HaveLen(1))
		Expect(page.Items[0].ID).To(Equal(records[1].ID))
	})

	ginkgo.It("pins the status expiry to the start record's", func() {
		rec := contractFixture(epoch)[0]
		Expect(store.Begin(ctx, rec)).To(Succeed())
		startTTL := backing.ttl(prefix + "sessions:v1:c:start")
		Expect(startTTL).To(BeNumerically("~", 47*time.Hour+2*time.Minute, time.Minute), "48h less the 58 minutes it has run")

		rec.State = query.SessionStopped
		Expect(store.Update(ctx, rec.ID, rec.SessionStatus)).To(Succeed())
		Expect(backing.ttl(prefix + "sessions:v1:c:status")).To(BeNumerically("~", startTTL, time.Second))
	})

	ginkgo.It("refuses a record that has already outlived the ttl", func() {
		rec := contractFixture(epoch)[0]
		rec.StartedAt = time.Now().Add(-49 * time.Hour)
		Expect(store.Begin(ctx, rec)).To(MatchError(ContainSubstring("outside the 48h0m0s session ttl")))
	})

	ginkgo.It("drops index entries whose start record is gone, and trims those older than the ttl", func() {
		for _, rec := range contractFixture(epoch) {
			Expect(store.Begin(ctx, rec)).To(Succeed())
		}
		index := prefix + "sessions:v1:index"
		Expect(backing.ZAdd(ctx, index, float64(time.Now().Add(-72*time.Hour).UnixMilli()), "ancient")).To(Succeed())
		Expect(backing.Del(ctx, prefix+"sessions:v1:b:start")).To(Succeed())

		page, err := store.List(ctx, query.SessionFilter{})
		Expect(err).ToNot(HaveOccurred())
		Expect(pageIDs(page)).To(Equal([]string{"a", "c", "d", "f"}))
		Expect(page.Shared).To(BeTrue())
		members, err := backing.ZRangeByScore(ctx, index, cache.NegInf, cache.PosInf)
		Expect(err).ToNot(HaveOccurred())
		Expect(members).ToNot(ContainElement("b"), "a missing start removes its index entry")
		Expect(members).To(ContainElement("ancient"), "only a write trims by age")

		Expect(store.Begin(ctx, contractRecord(epoch, "g", "trace-capture/jvm_trace", query.SessionRunning, 6, 0, nil))).To(Succeed())
		members, err = backing.ZRangeByScore(ctx, index, cache.NegInf, cache.PosInf)
		Expect(err).ToNot(HaveOccurred())
		Expect(members).ToNot(ContainElement("ancient"))
	})

	ginkgo.It("removes a start record whose status was evicted, counts it, and lists the rest", func() {
		for _, rec := range contractFixture(epoch) {
			Expect(store.Begin(ctx, rec)).To(Succeed())
		}
		orphansBefore := KVOrphanedRecords()
		Expect(backing.Del(ctx, prefix+"sessions:v1:c:status")).To(Succeed())

		page, err := store.List(ctx, query.SessionFilter{})
		Expect(err).ToNot(HaveOccurred())
		Expect(pageIDs(page)).To(Equal([]string{"a", "b", "d", "f"}))
		Expect(KVOrphanedRecords()).To(Equal(orphansBefore + 1))
		_, err = backing.Get(ctx, prefix+"sessions:v1:c:start")
		Expect(err).To(MatchError(cache.ErrKeyNotFound))
		members, err := backing.ZRangeByScore(ctx, prefix+"sessions:v1:index", cache.NegInf, cache.PosInf)
		Expect(err).ToNot(HaveOccurred())
		Expect(members).ToNot(ContainElement("c"))
		_, found, err := store.Get(ctx, "c")
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeFalse())
	})

	ginkgo.It("leaves a just-begun start whose status is not written yet, unlisted but in place", func() {
		rec := contractRecord(time.Now(), "fresh", jvmProfile, query.SessionStarting, 0, 0, nil)
		Expect(store.Begin(ctx, rec)).To(Succeed())
		Expect(backing.Del(ctx, prefix+"sessions:v1:fresh:status")).To(Succeed())

		page, err := store.List(ctx, query.SessionFilter{})
		Expect(err).ToNot(HaveOccurred())
		Expect(pageIDs(page)).To(BeEmpty())
		_, err = backing.Get(ctx, prefix+"sessions:v1:fresh:start")
		Expect(err).ToNot(HaveOccurred())
		members, err := backing.ZRangeByScore(ctx, prefix+"sessions:v1:index", cache.NegInf, cache.PosInf)
		Expect(err).ToNot(HaveOccurred())
		Expect(members).To(ContainElement("fresh"))
	})

	ginkgo.It("reads only records started no earlier than the ids it maps, for lineage", func() {
		counting := &countingCache{Store: backing, reads: map[string]int{}}
		store, err := NewKVStore(func(context.Context) (cache.Store, string, error) { return counting, prefix, nil }, KVStoreOptions{TTL: 48 * time.Hour})
		Expect(err).ToNot(HaveOccurred())
		for _, rec := range contractFixture(epoch) {
			Expect(store.Begin(ctx, rec)).To(Succeed())
		}
		counting.reset()

		lineage, err := store.Lineage(ctx, []string{"c"})
		Expect(err).ToNot(HaveOccurred())
		Expect(lineage).To(Equal(map[string][]string{"c": {"f"}}))
		Expect(counting.readsOf("a")).To(BeZero(), "a started before c and cannot be its restart")
	})

	ginkgo.It("surfaces a resolver failure", func() {
		broken, err := NewKVStore(func(context.Context) (cache.Store, string, error) {
			return nil, "", errors.New("no L2 for environment x")
		}, KVStoreOptions{})
		Expect(err).ToNot(HaveOccurred())
		_, err = broken.List(ctx, query.SessionFilter{})
		Expect(err).To(MatchError(ContainSubstring("no L2 for environment x")))
	})

	ginkgo.It("reports a memory store as not shared", func() {
		memory, err := NewMemoryKVStore(KVStoreOptions{})
		Expect(err).ToNot(HaveOccurred())
		page, err := memory.List(ctx, query.SessionFilter{})
		Expect(err).ToNot(HaveOccurred())
		Expect(page.Shared).To(BeFalse())
	})

	ginkgo.It("rejects a negative ttl", func() {
		_, err := NewMemoryKVStore(KVStoreOptions{TTL: -time.Second})
		Expect(err).To(MatchError(ContainSubstring("ttl")))
	})
})
