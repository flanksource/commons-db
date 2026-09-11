package kv_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
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
	backend, err := kv.New(kv.Options{Store: store, Prefix: "records", TTL: time.Hour, MaxChunkBytes: maxChunkBytes})
	Expect(err).ToNot(HaveOccurred())
	return backend
}

// failingStore fails the failChunkWrite-th chunk write after it is armed, the
// way a store that drops a connection mid-append does.
type failingStore struct {
	cache.Store
	failChunkWrite int
	chunkWrites    int
}

func (s *failingStore) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if s.failChunkWrite > 0 && strings.Contains(key, "/chunk/") {
		s.chunkWrites++
		if s.chunkWrites == s.failChunkWrite {
			return errors.New("injected chunk write failure")
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
		return recordstoretest.Harness{
			Backend: openKV(cache.NewMemory(), 1<<20),
			// The in-process store expires against the wall clock.
			Elapse: time.Sleep,
		}
	})
})

var _ = Describe("kv backend over valkey", func() {
	recordstoretest.Conformance(func() recordstoretest.Harness {
		store, server := newValkey()
		return recordstoretest.Harness{Backend: openKV(store, 1<<20), Elapse: server.FastForward}
	})
})

var _ = Describe("kv backend chunking", func() {
	ctx := context.Background()

	It("splits an append across chunks no larger than the cap and scans across them", func() {
		store := cache.NewMemory()
		backend := openKV(store, 300)
		rows := recordstoretest.SampleRows(1, 12)

		window, err := backend.Append(ctx, "run-1", recordstoretest.Kind, rows)
		Expect(err).ToNot(HaveOccurred())
		Expect(window).To(Equal(recordstore.Window{From: 1, To: 12}))

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
		Entry("no store", kv.Options{Prefix: "p", TTL: time.Hour, MaxChunkBytes: 1}, "store"),
		Entry("no prefix", kv.Options{Store: cache.NewMemory(), TTL: time.Hour, MaxChunkBytes: 1}, "prefix"),
		Entry("no ttl", kv.Options{Store: cache.NewMemory(), Prefix: "p", MaxChunkBytes: 1}, "ttl"),
		Entry("no chunk cap", kv.Options{Store: cache.NewMemory(), Prefix: "p", TTL: time.Hour}, "chunk"),
	)
})
