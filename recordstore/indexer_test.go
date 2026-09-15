package recordstore_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/flanksource/clicky/cache"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/recordstore/kv"
	"github.com/flanksource/commons-db/recordstore/ndjson"
	"github.com/flanksource/commons-db/recordstore/recordstoretest"
	"github.com/flanksource/commons-db/recordstore/sqlite"
)

func TestRecordStore(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Record Store Suite")
}

// recordingIndex records every import window, which is the only way to tell an
// incremental catch-up from a full re-copy that happens to end in the same rows.
type recordingIndex struct {
	*sqlite.Backend
	mu       sync.Mutex
	imports  []recordstore.Window
	prepared []recordstore.Meta
}

func (r *recordingIndex) Prepare(ctx context.Context, source recordstore.Meta) (recordstore.Meta, bool, error) {
	indexed, found, err := r.Backend.Prepare(ctx, source)
	r.mu.Lock()
	r.prepared = append(r.prepared, source)
	r.mu.Unlock()
	return indexed, found, err
}

func (r *recordingIndex) Import(ctx context.Context, request recordstore.ImportRequest) (recordstore.Window, error) {
	window, err := r.Backend.Import(ctx, request)
	if err == nil {
		r.mu.Lock()
		r.imports = append(r.imports, window)
		r.mu.Unlock()
	}
	return window, err
}

func (r *recordingIndex) Imports() []recordstore.Window {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recordstore.Window(nil), r.imports...)
}

func (r *recordingIndex) Prepared() []recordstore.Meta {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recordstore.Meta(nil), r.prepared...)
}

// shortSource stops every scan after rows rows and reports success, the way a
// backend that lost the tail of a stream without noticing would.
type shortSource struct {
	recordstore.Backend
	rows int
}

func (s shortSource) Scan(ctx context.Context, stream string, afterSeq int64, fn func(int64, recordstore.Row) error) error {
	delivered := 0
	stop := errors.New("short")
	err := s.Backend.Scan(ctx, stream, afterSeq, func(seq int64, row recordstore.Row) error {
		if delivered == s.rows {
			return stop
		}
		delivered++
		return fn(seq, row)
	})
	if errors.Is(err, stop) {
		return nil
	}
	return err
}

// fakeClock is the source's clock, started at the wall clock so the expiries
// it stamps agree with the in-process store's.
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

var _ = Describe("Indexer", func() {
	var (
		ctx     context.Context
		clock   *fakeClock
		source  *kv.Backend
		index   *recordingIndex
		indexer *recordstore.Indexer
	)

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		clock = &fakeClock{now: time.Now()}
		source, err = kv.New(kv.Options{
			Store: cache.NewMemory(), Prefix: "records", Schema: recordstoretest.Schema, TTL: time.Hour,
			MaxChunkBytes: 1 << 20, Now: clock.Now,
		})
		Expect(err).ToNot(HaveOccurred())
		backend, err := sqlite.Open(sqlite.Options{
			Path: filepath.Join(GinkgoT().TempDir(), "index.sqlite"), Schema: recordstoretest.Schema, Derived: true,
			SweepInterval: time.Minute,
		})
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(backend.Close)
		index = &recordingIndex{Backend: backend}
		indexer, err = recordstore.NewIndexer(source, index)
		Expect(err).ToNot(HaveOccurred())
	})

	appendSource := func(first, last int) {
		_, err := source.Append(ctx, "run-1", recordstoretest.Kind, recordstoretest.SampleRows(first, last))
		Expect(err).ToNot(HaveOccurred())
	}

	It("copies only the rows past the index's high seq, under the source's seqs", func() {
		appendSource(1, 3)
		Expect(indexer.Ensure(ctx, "run-1")).To(Succeed())
		appendSource(4, 5)
		Expect(indexer.Ensure(ctx, "run-1")).To(Succeed())
		Expect(indexer.Ensure(ctx, "run-1")).To(Succeed())

		Expect(index.Imports()).To(Equal([]recordstore.Window{{From: 1, To: 3}, {From: 4, To: 5}}))
		seqs, rows := recordstoretest.Scanned(index, "run-1", 0)
		Expect(seqs).To(Equal([]int64{1, 2, 3, 4, 5}))
		Expect(recordstoretest.Normalize(rows)).To(Equal(recordstoretest.Normalize(recordstoretest.SampleRows(1, 5))))
	})

	It("prepares the index before returning when its high seq already matches the source", func() {
		appendSource(1, 3)
		Expect(indexer.Ensure(ctx, "run-1")).To(Succeed())
		Expect(indexer.Ensure(ctx, "run-1")).To(Succeed())

		prepared := index.Prepared()
		Expect(prepared).To(HaveLen(2))
		Expect(prepared[1].Generation).To(Equal(prepared[0].Generation))
		Expect(index.Imports()).To(Equal([]recordstore.Window{{From: 1, To: 3}}))
	})

	It("mirrors the source's expiry, so the index never outlives what it indexes", func() {
		appendSource(1, 1)
		Expect(indexer.Ensure(ctx, "run-1")).To(Succeed())

		sourceMeta, err := source.Meta(ctx, "run-1")
		Expect(err).ToNot(HaveOccurred())
		indexMeta, err := index.Meta(ctx, "run-1")
		Expect(err).ToNot(HaveOccurred())
		Expect(indexMeta.ExpiresAt).ToNot(BeNil())
		Expect(*indexMeta.ExpiresAt).To(BeTemporally("~", *sourceMeta.ExpiresAt, time.Second))
	})

	It("does not expire an index whose source has no expiry", func() {
		immortal, err := ndjson.New(ndjson.Options{
			Dir: filepath.Join(GinkgoT().TempDir(), "source"), Schema: recordstoretest.Schema, MaxBytes: 1 << 20, KeepStreams: 10,
		})
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(immortal.Close)
		_, err = immortal.Append(ctx, "run-1", recordstoretest.Kind, recordstoretest.SampleRows(1, 1))
		Expect(err).ToNot(HaveOccurred())

		shortLived, err := sqlite.Open(sqlite.Options{
			Path: filepath.Join(GinkgoT().TempDir(), "short-lived-index.sqlite"), Schema: recordstoretest.Schema,
			Derived: true, TTL: time.Millisecond, SweepInterval: time.Hour,
		})
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(shortLived.Close)
		immortalIndexer, err := recordstore.NewIndexer(immortal, shortLived)
		Expect(err).ToNot(HaveOccurred())
		Expect(immortalIndexer.Ensure(ctx, "run-1")).To(Succeed())

		indexed, err := shortLived.Meta(ctx, "run-1")
		Expect(err).ToNot(HaveOccurred())
		Expect(indexed.ExpiresAt).To(BeNil())
	})

	It("indexes an empty stream as an empty stream rather than a missing one", func() {
		_, err := source.Append(ctx, "run-1", recordstoretest.Kind, nil)
		Expect(err).ToNot(HaveOccurred())
		Expect(indexer.Ensure(ctx, "run-1")).To(Succeed())

		meta, err := index.Meta(ctx, "run-1")
		Expect(err).ToNot(HaveOccurred())
		Expect(meta.Total).To(BeZero())
	})

	It("reports a stream the source does not have as recordstore.ErrNotFound", func() {
		err := indexer.Ensure(ctx, "missing")
		Expect(errors.Is(err, recordstore.ErrNotFound)).To(BeTrue(), "%v", err)
	})

	It("refuses an index that is ahead of its source", func() {
		appendSource(1, 2)
		Expect(indexer.Ensure(ctx, "run-1")).To(Succeed())
		sourceMeta, err := source.Meta(ctx, "run-1")
		Expect(err).ToNot(HaveOccurred())
		sourceMeta.HighSeq = 4
		_, err = index.Import(ctx, recordstore.ImportRequest{
			Source: sourceMeta, First: 3, Rows: recordstoretest.SampleRows(3, 4),
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(indexer.Ensure(ctx, "run-1")).To(MatchError(ContainSubstring("ahead")))
	})

	Context("when the source is the index", func() {
		It("copies nothing and still reports a stream that does not exist", func() {
			durable, err := sqlite.Open(sqlite.Options{
				Path: filepath.Join(GinkgoT().TempDir(), "durable.sqlite"), Schema: recordstoretest.Schema,
				SweepInterval: time.Minute,
			})
			Expect(err).ToNot(HaveOccurred())
			DeferCleanup(durable.Close)
			self, err := recordstore.NewIndexer(durable, durable)
			Expect(err).ToNot(HaveOccurred())
			_, err = durable.Append(ctx, "run-1", recordstoretest.Kind, recordstoretest.SampleRows(1, 2))
			Expect(err).ToNot(HaveOccurred())

			Expect(self.Ensure(ctx, "run-1")).To(Succeed())
			seqs, _ := recordstoretest.Scanned(durable, "run-1", 0)
			Expect(seqs).To(Equal([]int64{1, 2}))
			Expect(errors.Is(self.Ensure(ctx, "missing"), recordstore.ErrNotFound)).To(BeTrue())
		})
	})

	DescribeTable("rebuilds an index when a stream id is reused under a new generation",
		func(oldLast, newFirst, newLast int) {
			appendSource(1, oldLast)
			Expect(indexer.Ensure(ctx, "run-1")).To(Succeed())
			oldMeta, err := index.Meta(ctx, "run-1")
			Expect(err).ToNot(HaveOccurred())

			Expect(source.Expire(ctx, "run-1", time.Millisecond)).To(Succeed())
			time.Sleep(5 * time.Millisecond)
			appendSource(newFirst, newLast)
			sourceMeta, err := source.Meta(ctx, "run-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(sourceMeta.Generation).ToNot(Equal(oldMeta.Generation))
			Expect(indexer.Ensure(ctx, "run-1")).To(Succeed())

			indexedMeta, err := index.Meta(ctx, "run-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(indexedMeta.Generation).To(Equal(sourceMeta.Generation))
			seqs, rows := recordstoretest.Scanned(index, "run-1", 0)
			Expect(seqs).To(Equal(func() []int64 {
				seqs := make([]int64, newLast-newFirst+1)
				for position := range seqs {
					seqs[position] = int64(position + 1)
				}
				return seqs
			}()))
			Expect(recordstoretest.Normalize(rows)).To(Equal(recordstoretest.Normalize(recordstoretest.SampleRows(newFirst, newLast))))
		},
		Entry("with the same high seq", 3, 10, 12),
		Entry("with a lower high seq", 4, 10, 11),
		Entry("with a higher high seq", 2, 10, 13),
	)

	Context("when the source trims its stream", func() {
		// trimSource trims the source's rows appended before the clock now,
		// and advances it so later appends are not trimmed with them.
		trimSource := func() {
			_, err := source.Trim(ctx, "run-1", clock.Now())
			Expect(err).ToNot(HaveOccurred())
			clock.Advance(time.Minute)
		}

		expectIndexed := func(first, last int, low int64) {
			seqs, rows := recordstoretest.Scanned(index, "run-1", 0)
			expected := make([]int64, 0, last-first+1)
			for seq := first; seq <= last; seq++ {
				expected = append(expected, int64(seq))
			}
			Expect(seqs).To(Equal(expected))
			Expect(recordstoretest.Normalize(rows)).To(Equal(recordstoretest.Normalize(recordstoretest.SampleRows(first, last))))
			meta, err := index.Meta(ctx, "run-1")
			Expect(err).ToNot(HaveOccurred())
			Expect([]int64{meta.LowSeq, meta.HighSeq, meta.Total}).To(Equal([]int64{low, int64(last), int64(last) - low + 1}))
		}

		It("drops the indexed rows below the source's new low seq", func() {
			appendSource(1, 2)
			clock.Advance(time.Minute)
			appendSource(3, 4)
			Expect(indexer.Ensure(ctx, "run-1")).To(Succeed())
			clock.Advance(-time.Minute / 2)
			trimSource()

			Expect(indexer.Ensure(ctx, "run-1")).To(Succeed())
			expectIndexed(3, 4, 3)
		})

		It("starts an index of an already trimmed stream at the source's low seq", func() {
			appendSource(1, 2)
			clock.Advance(time.Minute)
			trimSource()
			appendSource(3, 4)

			Expect(indexer.Ensure(ctx, "run-1")).To(Succeed())
			Expect(index.Imports()).To(Equal([]recordstore.Window{{From: 3, To: 4}}))
			expectIndexed(3, 4, 3)
		})

		It("catches up an index the source trimmed past, importing from the new low seq", func() {
			appendSource(1, 2)
			Expect(indexer.Ensure(ctx, "run-1")).To(Succeed())
			clock.Advance(time.Minute)
			appendSource(3, 4)
			clock.Advance(time.Minute)
			trimSource()
			appendSource(5, 6)

			Expect(indexer.Ensure(ctx, "run-1")).To(Succeed())
			Expect(index.Imports()).To(Equal([]recordstore.Window{{From: 1, To: 2}, {From: 5, To: 6}}))
			expectIndexed(5, 6, 5)
		})
	})

	It("refuses a keyed row an import would store under a key the indexed stream holds", func() {
		source := recordstore.NewStreamMeta("run-1", recordstoretest.KeyedKind, clock.Now())
		source.Total, source.HighSeq = 2, 2
		_, err := index.Import(ctx, recordstore.ImportRequest{Source: source, First: 1, Rows: recordstoretest.SampleRows(1, 1)})
		Expect(err).ToNot(HaveOccurred())
		_, err = index.Import(ctx, recordstore.ImportRequest{Source: source, First: 2, Rows: recordstoretest.SampleRows(1, 1)})
		Expect(err).To(MatchError(ContainSubstring("UNIQUE")))
	})

	It("refuses a source whose scan ends below the high seq it reports, naming both", func() {
		appendSource(1, 5)
		short, err := recordstore.NewIndexer(shortSource{Backend: source, rows: 3}, index)
		Expect(err).ToNot(HaveOccurred())

		err = short.Ensure(ctx, "run-1")
		Expect(err).To(MatchError(And(ContainSubstring(`"run-1"`), ContainSubstring("seq 5"), ContainSubstring("reached seq 3"))))
	})

	It("catches up with a stream that is being appended to while it indexes", func() {
		const batches, batch = 40, 7
		done := make(chan struct{})
		go func() {
			defer GinkgoRecover()
			defer close(done)
			for n := range batches {
				appendSource(n*batch+1, (n+1)*batch)
			}
		}()
		appendSource(0, -1)
		for running := true; running; {
			select {
			case <-done:
				running = false
			default:
			}
			sourceMeta, err := source.Meta(ctx, "run-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(indexer.Ensure(ctx, "run-1")).To(Succeed())
			indexMeta, err := index.Meta(ctx, "run-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(indexMeta.HighSeq).To(BeNumerically(">=", sourceMeta.HighSeq))
		}
		Expect(indexer.Ensure(ctx, "run-1")).To(Succeed())

		seqs, rows := recordstoretest.Scanned(index, "run-1", 0)
		Expect(seqs).To(HaveLen(batches * batch))
		for position, seq := range seqs {
			Expect(seq).To(Equal(int64(position + 1)))
		}
		Expect(recordstoretest.Normalize(rows)).To(Equal(recordstoretest.Normalize(recordstoretest.SampleRows(1, batches*batch))))
	})

	It("refuses to be built without both backends or with unsafe derived ownership", func() {
		_, err := recordstore.NewIndexer(nil, index)
		Expect(err).To(HaveOccurred())
		_, err = recordstore.NewIndexer(source, nil)
		Expect(err).To(HaveOccurred())
		_, err = recordstore.NewIndexer(index, index)
		Expect(err).To(MatchError(ContainSubstring("own source")))

		durable, err := sqlite.Open(sqlite.Options{
			Path: filepath.Join(GinkgoT().TempDir(), "not-derived.sqlite"), Schema: recordstoretest.Schema,
			SweepInterval: time.Minute,
		})
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(durable.Close)
		_, err = recordstore.NewIndexer(source, durable)
		Expect(err).To(MatchError(ContainSubstring("must be derived")))
	})
})
