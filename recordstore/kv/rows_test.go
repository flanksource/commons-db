package kv_test

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"path/filepath"
	"sync"
	"time"

	"github.com/flanksource/clicky/cache"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/recordstore/recordstoretest"
	"github.com/flanksource/commons-db/recordstore/sqlite"
)

// countingStore counts what the backend asks of the store — each call, and each
// member a range read returns — so a spec can hold an append's cost to its
// batch rather than to the stream it lands in.
type countingStore struct {
	cache.Store
	mu    sync.Mutex
	calls map[string]int
}

func newCountingStore() *countingStore {
	return &countingStore{Store: cache.NewMemory(), calls: map[string]int{}}
}

func (s *countingStore) add(op string, n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls[op] += n
}

// take returns the counts so far and starts counting afresh.
func (s *countingStore) take() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	calls := s.calls
	s.calls = map[string]int{}
	return calls
}

func (s *countingStore) Get(ctx context.Context, key string) ([]byte, error) {
	s.add("Get", 1)
	return s.Store.Get(ctx, key)
}

func (s *countingStore) MGet(ctx context.Context, keys iter.Seq[string]) iter.Seq2[cache.Entry, error] {
	s.add("MGet", 1)
	pulled := func(yield func(string) bool) {
		for key := range keys {
			s.add("MGet keys", 1)
			if !yield(key) {
				return
			}
		}
	}
	return s.Store.MGet(ctx, pulled)
}

func (s *countingStore) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	s.add("Set", 1)
	return s.Store.Set(ctx, key, value, ttl)
}

func (s *countingStore) Del(ctx context.Context, key string) error {
	s.add("Del", 1)
	return s.Store.Del(ctx, key)
}

func (s *countingStore) Expire(ctx context.Context, key string, ttl time.Duration) error {
	s.add("Expire", 1)
	return s.Store.Expire(ctx, key, ttl)
}

func (s *countingStore) ZAdd(ctx context.Context, key string, score float64, member string) error {
	s.add("ZAdd", 1)
	return s.Store.ZAdd(ctx, key, score, member)
}

func (s *countingStore) ZRem(ctx context.Context, key, member string) error {
	s.add("ZRem", 1)
	return s.Store.ZRem(ctx, key, member)
}

func (s *countingStore) ZRevRange(ctx context.Context, key string, start, stop int64) ([]string, error) {
	members, err := s.Store.ZRevRange(ctx, key, start, stop)
	s.add("ZRevRange", 1)
	s.add("ZRevRange members", len(members))
	return members, err
}

func (s *countingStore) ZRangeByScore(ctx context.Context, key string, lower, upper cache.Bound) ([]string, error) {
	members, err := s.Store.ZRangeByScore(ctx, key, lower, upper)
	s.add("ZRangeByScore", 1)
	s.add("ZRangeByScore members", len(members))
	return members, err
}

func (s *countingStore) ZRemRangeByScore(ctx context.Context, key string, lower, upper cache.Bound) error {
	s.add("ZRemRangeByScore", 1)
	return s.Store.ZRemRangeByScore(ctx, key, lower, upper)
}

func (s *countingStore) ZRemRangeByRank(ctx context.Context, key string, start, stop int64) error {
	s.add("ZRemRangeByRank", 1)
	return s.Store.ZRemRangeByRank(ctx, key, start, stop)
}

// seqNames is each row a stream holds as "<seq>:<name>", in seq order.
func seqNames(backend recordstore.Backend, stream string) []string {
	seqs, rows := recordstoretest.Scanned(backend, stream, 0)
	pairs := make([]string, len(rows))
	for index, row := range rows {
		pairs[index] = fmt.Sprintf("%d:%v", seqs[index], row["name"])
	}
	return pairs
}

var _ = Describe("kv backend keyed rows", func() {
	ctx := context.Background()

	// A long-lived keyed stream is re-synced over a window it mostly holds
	// already, so an append that read or touched the whole stream would cost
	// more with every day the stream retains.
	It("costs a keyed append the same however many rows and appends the stream already holds", func() {
		cost := func(held int) map[string]int {
			store := newCountingStore()
			backend := openKV(store, 1<<20)
			for n := 1; n <= held; n++ {
				_, err := backend.Append(ctx, "run-1", recordstoretest.RollingKind, recordstoretest.SampleRows(n, n))
				Expect(err).ToNot(HaveOccurred())
			}
			store.take()
			result, err := backend.Append(ctx, "run-1", recordstoretest.RollingKind, recordstoretest.SampleRows(held, held+1))
			Expect(err).ToNot(HaveOccurred())
			Expect(result).To(Equal(recordstore.AppendResult{
				Window: recordstore.Window{From: int64(held + 1), To: int64(held + 1)}, Skipped: 1,
			}))
			return store.take()
		}
		Expect(cost(200)).To(Equal(cost(2)))
	})

	// Rebuilding an index scans the whole retained stream; a read per row would
	// be a round trip per row.
	It("reads a scan's rows through one MGet of their keys rather than a Get per row", func() {
		store := newCountingStore()
		backend := openKV(store, 1<<20)
		_, err := backend.Append(ctx, "run-1", recordstoretest.KeyedKind, recordstoretest.SampleRows(1, 250))
		Expect(err).ToNot(HaveOccurred())
		store.take()

		seqs, _ := recordstoretest.Scanned(backend, "run-1", 0)
		Expect(seqs).To(HaveLen(250))
		Expect(store.take()).To(Equal(map[string]int{
			"Get": 1, "ZRangeByScore": 1, "ZRangeByScore members": 250, "MGet": 1, "MGet keys": 250,
		}), "one metadata read, the seqs, and the rows")
	})

	Context("over valkey, whose clock expires keys", func() {
		var (
			store   cache.Store
			clock   *fakeClock
			elapse  func(time.Duration)
			backend recordstore.Backend
		)

		BeforeEach(func() {
			var server interface{ FastForward(time.Duration) }
			store, server = newValkey()
			clock = &fakeClock{now: time.Now()}
			elapse = func(d time.Duration) {
				clock.Advance(d)
				server.FastForward(d)
			}
			backend = openKVAt(store, 1<<20, clock.Now)
		})

		appendKind := func(kind string, first, last int) {
			_, err := backend.Append(ctx, "run-1", kind, recordstoretest.SampleRows(first, last))
			Expect(err).ToNot(HaveOccurred())
		}

		// No append moves a stored row's own expiry, so a row has to be written to
		// outlive every moment its stream can still report it.
		It("keeps every row a retaining stream still holds readable until the stream expires", func() {
			appendKind(recordstoretest.RollingKind, 1, 1)
			elapse(50 * time.Minute)
			appendKind(recordstoretest.RollingKind, 2, 2)
			// Row 1 is past the ttl, but no append has trimmed it since.
			elapse(55 * time.Minute)

			Expect(seqNames(backend, "run-1")).To(Equal([]string{"1:row-001", "2:row-002"}))
			elapse(10 * time.Minute)
			_, err := backend.Meta(ctx, "run-1")
			Expect(errors.Is(err, recordstore.ErrNotFound)).To(BeTrue(), "Meta: %v", err)
		})

		It("keeps a keyed stream's rows for as long as Expire extends the stream", func() {
			appendKind(recordstoretest.KeyedKind, 1, 2)
			Expect(backend.Expire(ctx, "run-1", 3*time.Hour)).To(Succeed())
			elapse(2 * time.Hour)

			Expect(seqNames(backend, "run-1")).To(Equal([]string{"1:row-001", "2:row-002"}))
		})

		// A pod restart takes the index file with it; the rows live on in valkey.
		It("rebuilds an empty derived index from every row the stream still holds, restart after restart", func() {
			appendKind(recordstoretest.RollingKind, 1, 3)
			elapse(40 * time.Minute)
			appendKind(recordstoretest.RollingKind, 2, 5)
			elapse(30 * time.Minute)
			// Trims rows 1-3, appended 70 minutes ago.
			appendKind(recordstoretest.RollingKind, 6, 6)

			indexed := func() []string {
				index, err := sqlite.Open(sqlite.Options{
					Path: filepath.Join(GinkgoT().TempDir(), "index.sqlite"), Schema: recordstoretest.Schema, Derived: true,
					SweepInterval: time.Minute,
				})
				Expect(err).ToNot(HaveOccurred())
				DeferCleanup(index.Close)
				indexer, err := recordstore.NewIndexer(openKVAt(store, 1<<20, clock.Now), index)
				Expect(err).ToNot(HaveOccurred())
				Expect(indexer.Ensure(ctx, "run-1")).To(Succeed())
				return seqNames(index, "run-1")
			}
			retained := []string{"4:row-004", "5:row-005", "6:row-006"}
			Expect(indexed()).To(Equal(retained))
			// Rows 4-5 are now past the ttl with no append to trim them, and the
			// rows trimmed earlier have left the store.
			elapse(55 * time.Minute)
			Expect(indexed()).To(Equal(retained))
		})
	})

	It("reports a keyed row that vanished from the store rather than a shorter stream", func() {
		store := cache.NewMemory()
		backend := openKV(store, 1<<20)
		_, err := backend.Append(ctx, "run-1", recordstoretest.KeyedKind, recordstoretest.SampleRows(1, 3))
		Expect(err).ToNot(HaveOccurred())
		Expect(store.Del(ctx, "records/run-1/row/row-002")).To(Succeed())

		err = backend.Scan(ctx, "run-1", 0, func(int64, recordstore.Row) error { return nil })
		Expect(err).To(MatchError(And(ContainSubstring("seq 2"), ContainSubstring(`"row-002"`), ContainSubstring("missing"))))
	})

	It("numbers the next keyed appends on from the committed high seq when an append fails part way through its rows", func() {
		store := &failingStore{Store: cache.NewMemory()}
		backend := openKV(store, 1<<20)
		_, err := backend.Append(ctx, "run-1", recordstoretest.KeyedKind, recordstoretest.SampleRows(1, 2))
		Expect(err).ToNot(HaveOccurred())
		store.failRowWrite, store.rowWrites = 2, 0
		_, err = backend.Append(ctx, "run-1", recordstoretest.KeyedKind, recordstoretest.SampleRows(3, 6))
		Expect(err).To(MatchError(ContainSubstring("injected")))
		store.failRowWrite = 0

		result, err := backend.Append(ctx, "run-1", recordstoretest.KeyedKind, recordstoretest.SampleRows(4, 4))
		Expect(err).ToNot(HaveOccurred())
		Expect(result).To(Equal(recordstore.AppendResult{Window: recordstore.Window{From: 3, To: 3}}))
		result, err = backend.Append(ctx, "run-1", recordstoretest.KeyedKind, recordstoretest.SampleRows(3, 6))
		Expect(err).ToNot(HaveOccurred())
		Expect(result).To(Equal(recordstore.AppendResult{Window: recordstore.Window{From: 4, To: 6}, Skipped: 1}))
		Expect(seqNames(backend, "run-1")).To(Equal([]string{
			"1:row-001", "2:row-002", "3:row-004", "4:row-003", "5:row-005", "6:row-006",
		}))
	})
})
