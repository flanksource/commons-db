package kv_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/flanksource/clicky/cache"
	"github.com/flanksource/clicky/valkey"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	valkeygo "github.com/valkey-io/valkey-go"

	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/recordstore/kv"
	"github.com/flanksource/commons-db/recordstore/recordstoretest"
	"github.com/flanksource/commons-db/recordstore/sqlite"
)

func TestKV(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Record Store KV Suite")
}

func openKV(store cache.Store, maxChunkBytes int) *kv.Backend {
	return openKVAt(store, maxChunkBytes, time.Now)
}

func openKVAt(store cache.Store, maxChunkBytes int, now func() time.Time) *kv.Backend {
	backend, err := kv.New(kv.Options{
		Store: store, Prefix: "records", Schema: recordstoretest.Schema, TTL: recordstoretest.TTL,
		MaxChunkBytes: maxChunkBytes, Now: now,
	})
	Expect(err).ToNot(HaveOccurred())
	return backend
}

// fakeClock is the backend's clock, started at the wall clock so the expiries
// it stamps agree with the store's own. Concurrent appends read it, so it is
// locked.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// kvHarness opens the conformance backend over store; reap moves the store's
// own expiry clock.
func kvHarness(store cache.Store, reap func(time.Duration)) recordstoretest.Harness {
	clock := &fakeClock{now: time.Now()}
	open := func() recordstore.Backend { return openKVAt(store, 1<<20, clock.Now) }
	return recordstoretest.Harness{
		Backend: open(), Advance: clock.Advance, Now: clock.Now, Reopen: open,
		Elapse: func(d time.Duration) {
			clock.Advance(d)
			reap(d)
		},
	}
}

// failingStore fails the failChunkWrite-th chunk write, or the failRowWrite-th
// keyed row write, after it is armed, the way a store that drops a connection
// mid-append does.
type failingStore struct {
	cache.Store
	failChunkWrite int
	chunkWrites    int
	failRowWrite   int
	rowWrites      int

	// failMeta fails every metadata write, the commit point of an append.
	failMeta bool
}

func (s *failingStore) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if s.failMeta && strings.HasSuffix(key, "/meta") {
		return errors.New("injected meta write failure")
	}
	if s.failChunkWrite > 0 && strings.Contains(key, "/chunk/") {
		s.chunkWrites++
		if s.chunkWrites == s.failChunkWrite {
			return errors.New("injected chunk write failure")
		}
	}
	if s.failRowWrite > 0 && strings.Contains(key, "/row/") {
		s.rowWrites++
		if s.rowWrites == s.failRowWrite {
			return errors.New("injected row write failure")
		}
	}
	return s.Store.Set(ctx, key, value, ttl)
}

func newValkey() (cache.Store, *miniredis.Miniredis) {
	server, err := miniredis.Run()
	Expect(err).ToNot(HaveOccurred())
	client, err := valkeygo.NewClient(valkeygo.ClientOption{InitAddress: []string{server.Addr()}, DisableCache: true})
	Expect(err).ToNot(HaveOccurred())
	DeferCleanup(func() { client.Close(); server.Close() })
	return valkey.NewStore(client), server
}

var _ = Describe("kv backend over the in-process store", func() {
	recordstoretest.Conformance(func() recordstoretest.Harness {
		// The in-process store expires against the wall clock.
		return kvHarness(cache.NewMemory(), time.Sleep)
	})
})

var _ = Describe("kv backend over valkey", func() {
	recordstoretest.Conformance(func() recordstoretest.Harness {
		store, server := newValkey()
		return kvHarness(store, server.FastForward)
	})
})

var _ = Describe("kv backend chunking", func() {
	ctx := context.Background()

	It("splits an append across chunks no larger than the cap and scans across them", func() {
		store := cache.NewMemory()
		backend := openKV(store, 300)
		rows := recordstoretest.SampleRows(1, 12)

		result, err := backend.Append(ctx, "run-1", recordstoretest.Kind, rows)
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Window).To(Equal(recordstore.Window{From: 1, To: 12}))

		chunks, err := store.ZRangeByScore(ctx, "records/run-1/index", cache.NegInf, cache.PosInf)
		Expect(err).ToNot(HaveOccurred())
		Expect(len(chunks)).To(BeNumerically(">", 1))
		for _, chunk := range chunks {
			payload, err := store.Get(ctx, "records/run-1/chunk/"+chunk)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(payload)).To(BeNumerically("<=", 300))
		}
		seqs, scanned := recordstoretest.Scanned(backend, "run-1", 5)
		Expect(seqs).To(HaveLen(7))
		Expect(recordstoretest.Normalize(scanned)).To(Equal(recordstoretest.Normalize(rows[5:])))
	})

	It("refuses a row larger than a chunk, loudly and whole, and marks the stream capped", func() {
		backend := openKV(cache.NewMemory(), 64)
		_, err := backend.Append(ctx, "run-1", recordstoretest.Kind, recordstoretest.SampleRows(1, 1))
		Expect(errors.Is(err, recordstore.ErrCapacity)).To(BeTrue(), "%v", err)

		meta, err := backend.Meta(ctx, "run-1")
		Expect(err).ToNot(HaveOccurred())
		Expect(meta.Capped).To(BeTrue())
		Expect(meta.Total).To(BeZero())
	})

	It("reports a chunk that vanished from under its index rather than a short stream", func() {
		store := cache.NewMemory()
		backend := openKV(store, 1<<20)
		_, err := backend.Append(ctx, "run-1", recordstoretest.Kind, recordstoretest.SampleRows(1, 2))
		Expect(err).ToNot(HaveOccurred())
		chunks, err := store.ZRangeByScore(ctx, "records/run-1/index", cache.NegInf, cache.PosInf)
		Expect(err).ToNot(HaveOccurred())
		Expect(store.Del(ctx, "records/run-1/chunk/"+chunks[0])).To(Succeed())

		err = backend.Scan(ctx, "run-1", 0, func(int64, recordstore.Row) error { return nil })
		Expect(err).To(MatchError(ContainSubstring("missing")))
	})

	// A store that evicts under memory pressure can drop the chunk index whole.
	// Reading nothing then is not an empty stream: the metadata still says
	// what the stream holds.
	It("reports a scan that ends below the stream's high seq, naming what it reached", func() {
		store := cache.NewMemory()
		backend := openKV(store, 1<<20)
		_, err := backend.Append(ctx, "run-1", recordstoretest.Kind, recordstoretest.SampleRows(1, 3))
		Expect(err).ToNot(HaveOccurred())
		Expect(store.Del(ctx, "records/run-1/index")).To(Succeed())

		err = backend.Scan(ctx, "run-1", 0, func(int64, recordstore.Row) error { return nil })
		Expect(err).To(MatchError(And(ContainSubstring(`"run-1"`), ContainSubstring("seq 3"), ContainSubstring("reached seq 0"))))
	})

	Context("when an append fails part way through its chunks", func() {
		var (
			store   *failingStore
			backend *kv.Backend
		)

		BeforeEach(func() {
			store = &failingStore{Store: cache.NewMemory()}
			// Each sample row encodes to about 90 bytes, so a 200-byte cap packs
			// two rows to a chunk.
			backend = openKV(store, 200)
			_, err := backend.Append(ctx, "run-1", recordstoretest.Kind, recordstoretest.SampleRows(1, 2))
			Expect(err).ToNot(HaveOccurred())
			store.failChunkWrite = 3
			_, err = backend.Append(ctx, "run-1", recordstoretest.Kind, recordstoretest.SampleRows(3, 8))
			Expect(err).To(MatchError(ContainSubstring("injected")))
			store.failChunkWrite = 0
		})

		It("numbers the next appends on from the committed high seq, reading each seq once", func() {
			// Different boundaries than the failed append: one row, then three.
			_, err := backend.Append(ctx, "run-1", recordstoretest.Kind, recordstoretest.SampleRows(3, 3))
			Expect(err).ToNot(HaveOccurred())
			_, err = backend.Append(ctx, "run-1", recordstoretest.Kind, recordstoretest.SampleRows(4, 6))
			Expect(err).ToNot(HaveOccurred())

			seqs, rows := recordstoretest.Scanned(backend, "run-1", 0)
			Expect(seqs).To(Equal([]int64{1, 2, 3, 4, 5, 6}))
			Expect(recordstoretest.Normalize(rows)).To(Equal(recordstoretest.Normalize(recordstoretest.SampleRows(1, 6))))
		})

		It("leaves no chunk of the failed append for an index to trip over", func() {
			_, err := backend.Append(ctx, "run-1", recordstoretest.Kind, recordstoretest.SampleRows(3, 3))
			Expect(err).ToNot(HaveOccurred())
			_, err = backend.Append(ctx, "run-1", recordstoretest.Kind, recordstoretest.SampleRows(4, 6))
			Expect(err).ToNot(HaveOccurred())

			index, err := sqlite.Open(sqlite.Options{
				Path: filepath.Join(GinkgoT().TempDir(), "index.sqlite"), Schema: recordstoretest.Schema, Derived: true,
				SweepInterval: time.Minute,
			})
			Expect(err).ToNot(HaveOccurred())
			DeferCleanup(index.Close)
			indexer, err := recordstore.NewIndexer(backend, index)
			Expect(err).ToNot(HaveOccurred())
			Expect(indexer.Ensure(ctx, "run-1")).To(Succeed())
			seqs, _ := recordstoretest.Scanned(index, "run-1", 0)
			Expect(seqs).To(Equal([]int64{1, 2, 3, 4, 5, 6}))
		})
	})

	DescribeTable("refuses options it cannot run with",
		func(options kv.Options, message string) {
			_, err := kv.New(options)
			Expect(err).To(MatchError(ContainSubstring(message)))
		},
		Entry("no store", kv.Options{Prefix: "p", Schema: recordstoretest.Schema, TTL: time.Hour, MaxChunkBytes: 1}, "store"),
		Entry("no prefix", kv.Options{Store: cache.NewMemory(), Schema: recordstoretest.Schema, TTL: time.Hour, MaxChunkBytes: 1}, "prefix"),
		Entry("no schema", kv.Options{Store: cache.NewMemory(), Prefix: "p", TTL: time.Hour, MaxChunkBytes: 1}, "schema"),
		Entry("no ttl", kv.Options{Store: cache.NewMemory(), Prefix: "p", Schema: recordstoretest.Schema, MaxChunkBytes: 1}, "ttl"),
		Entry("no chunk cap", kv.Options{Store: cache.NewMemory(), Prefix: "p", Schema: recordstoretest.Schema, TTL: time.Hour}, "chunk"),
	)

	It("refuses a kind its schema resolver does not know, writing nothing", func() {
		backend := openKV(cache.NewMemory(), 1<<20)
		_, err := backend.Append(ctx, "run-1", "unknown", recordstoretest.SampleRows(1, 1))
		Expect(err).To(MatchError(ContainSubstring(`kind "unknown"`)))
		_, err = backend.Meta(ctx, "run-1")
		Expect(errors.Is(err, recordstore.ErrNotFound)).To(BeTrue(), "%v", err)
	})

	It("ignores key entries an interrupted keyed append left past the high seq", func() {
		store := &failingStore{Store: cache.NewMemory()}
		backend := openKV(store, 200)
		_, err := backend.Append(ctx, "run-1", recordstoretest.KeyedKind, recordstoretest.SampleRows(1, 2))
		Expect(err).ToNot(HaveOccurred())
		store.failMeta = true
		_, err = backend.Append(ctx, "run-1", recordstoretest.KeyedKind, recordstoretest.SampleRows(3, 4))
		Expect(err).To(MatchError(ContainSubstring("injected")))
		store.failMeta = false

		result, err := backend.Append(ctx, "run-1", recordstoretest.KeyedKind, recordstoretest.SampleRows(2, 4))
		Expect(err).ToNot(HaveOccurred())
		Expect(result).To(Equal(recordstore.AppendResult{Window: recordstore.Window{From: 3, To: 4}, Skipped: 1}))
		seqs, rows := recordstoretest.Scanned(backend, "run-1", 0)
		Expect(seqs).To(Equal([]int64{1, 2, 3, 4}))
		Expect(recordstoretest.Normalize(rows)).To(Equal(recordstoretest.Normalize(recordstoretest.SampleRows(1, 4))))
	})
})
