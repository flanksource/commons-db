package sqltrace

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/tracing/xetrace"
)

// slowStore commits every non-empty append after delay: a store that is up
// but behind, the way a busy disk or remote kv is.
type slowStore struct {
	RecordStore
	delay time.Duration
}

func (s slowStore) Append(ctx context.Context, stream, kind string, rows []recordstore.Row) (recordstore.AppendResult, error) {
	if len(rows) > 0 {
		time.Sleep(s.delay)
	}
	return s.RecordStore.Append(ctx, stream, kind, rows)
}

// countingStore counts the appends that reach the store.
type countingStore struct {
	RecordStore
	appends atomic.Int64
}

func (c *countingStore) Append(ctx context.Context, stream, kind string, rows []recordstore.Row) (recordstore.AppendResult, error) {
	c.appends.Add(1)
	return c.RecordStore.Append(ctx, stream, kind, rows)
}

func one(sql string) []xetrace.Event { return []xetrace.Event{statement(1, sql, 0)} }

var _ = ginkgo.Describe("eventWriter", func() {
	const stream = "writer-stream"
	var env environment

	ginkgo.BeforeEach(func() { env = newEnvironment(nil) })

	open := func(store RecordStore, onFail func(error)) *eventWriter {
		writer, err := newEventWriter(recordAppender{store: store, ctx: env.ctx(), stream: stream}, onFail)
		Expect(err).ToNot(HaveOccurred())
		return writer
	}
	ignoreFailure := func(error) {}

	ginkgo.It("creates the stream empty before anything is captured", func() {
		writer := open(env.store(), ignoreFailure)
		ginkgo.DeferCleanup(writer.Close)

		ref := writer.EventsRef()
		Expect(ref.Stream).To(Equal(stream))
		Expect(ref.Kind).To(Equal("sql_xevent"))
		Expect([]int64{ref.Low, ref.High, ref.Total}).To(Equal([]int64{1, 0, 0}))
	})

	// The property the queue exists for: the drain loop keeps running while the
	// store is wedged, because every millisecond blocked is a millisecond in
	// which SQL Server's ring buffer can overwrite unread events.
	ginkgo.It("never blocks Enqueue on a wedged store, and commits every chunk in poll order", func() {
		gate := &gatedStore{RecordStore: env.store(), release: make(chan struct{})}
		writer := open(gate, ignoreFailure)

		const chunks = 200
		want := make([]string, chunks)
		enqueued := make(chan struct{})
		go func() {
			defer close(enqueued)
			for index := range want {
				want[index] = fmt.Sprintf("select %d", index)
				writer.Enqueue(one(want[index]))
			}
		}()
		Eventually(enqueued).WithTimeout(5 * time.Second).Should(BeClosed())

		close(gate.release)
		Expect(writer.Close()).To(Succeed())
		Expect(env.statements(stream)).To(Equal(want))
	})

	ginkgo.It("holds a checkpoint until the rows queued before it are committed", func() {
		gate := &gatedStore{RecordStore: env.store(), release: make(chan struct{})}
		writer := open(gate, ignoreFailure)
		ginkgo.DeferCleanup(writer.Close)
		writer.Enqueue(one("select 1"))
		writer.Enqueue(one("select 2"))

		refs := make(chan query.EventsRef, 1)
		go func() {
			defer ginkgo.GinkgoRecover()
			ref, err := writer.Checkpoint(context.Background())
			Expect(err).ToNot(HaveOccurred())
			refs <- ref
		}()
		Consistently(refs, 100*time.Millisecond).ShouldNot(Receive())

		close(gate.release)
		var ref query.EventsRef
		Eventually(refs).Should(Receive(&ref))
		Expect([]int64{ref.From, ref.To, ref.High, ref.Total}).To(Equal([]int64{1, 2, 2, 2}))
		Expect(env.statements(stream)).To(Equal([]string{"select 1", "select 2"}))
	})

	ginkgo.It("never returns a checkpoint ahead of the committed rows while capture and checkpoints race", func() {
		writer := open(slowStore{RecordStore: env.store(), delay: 2 * time.Millisecond}, ignoreFailure)
		ginkgo.DeferCleanup(writer.Close)

		const chunks = 60
		var enqueued atomic.Int64
		capture := make(chan struct{})
		go func() {
			defer close(capture)
			for index := range chunks {
				writer.Enqueue(one(fmt.Sprintf("select %d", index)))
				enqueued.Add(1)
			}
		}()

		var checkpoints int
		for done := false; !done; checkpoints++ {
			select {
			case <-capture:
				done = true
			default:
			}
			queuedBefore := enqueued.Load()
			ref, err := writer.Checkpoint(context.Background())
			Expect(err).ToNot(HaveOccurred())
			committed := env.meta(stream).HighSeq
			Expect(ref.High).To(BeNumerically("<=", committed), "a checkpoint named rows the store does not hold")
			Expect(ref.High).To(BeNumerically(">=", queuedBefore), "a checkpoint returned before rows queued ahead of it were committed")
		}
		Expect(checkpoints).To(BeNumerically(">", 1))
		Expect(env.meta(stream).HighSeq).To(Equal(int64(chunks)))
	})

	ginkgo.It("gives up a checkpoint when its caller does", func() {
		gate := &gatedStore{RecordStore: env.store(), release: make(chan struct{})}
		writer := open(gate, ignoreFailure)
		ginkgo.DeferCleanup(func() { close(gate.release); _ = writer.Close() })
		writer.Enqueue(one("select 1"))

		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		_, err := writer.Checkpoint(ctx)

		Expect(err).To(MatchError(context.DeadlineExceeded))
	})

	ginkgo.It("fails the capture on an append error, and writes nothing after it", func() {
		refused := errors.New("disk full")
		gate := &gatedStore{RecordStore: env.store(), release: make(chan struct{}), failed: refused}
		close(gate.release)
		var mu sync.Mutex
		var failures []error
		writer := open(gate, func(err error) {
			mu.Lock()
			defer mu.Unlock()
			failures = append(failures, err)
		})

		writer.Enqueue(one("select 1"))
		_, err := writer.Checkpoint(context.Background())
		Expect(err).To(MatchError(refused))

		gate.failed = nil
		writer.Enqueue(one("select 2"))
		Expect(writer.Close()).To(MatchError(refused))
		Expect(env.meta(stream).Total).To(BeZero(), "rows after a gap would read as a complete capture")
		mu.Lock()
		defer mu.Unlock()
		Expect(failures).To(HaveLen(1))
		Expect(failures[0]).To(MatchError(ContainSubstring("append 1 sql_xevent row(s) to stream writer-stream")))
	})

	ginkgo.It("refuses a chunk offered after Close rather than dropping it silently", func() {
		writer := open(env.store(), ignoreFailure)
		writer.Enqueue(one("select 1"))
		Expect(writer.Close()).To(Succeed())

		writer.Enqueue(one("select 2"))

		Expect(writer.Close()).To(MatchError(ContainSubstring("writer already closed")))
		Expect(env.statements(stream)).To(Equal([]string{"select 1"}))
	})

	ginkgo.It("skips empty batches instead of committing empty appends", func() {
		counting := &countingStore{RecordStore: env.store()}
		writer := open(counting, ignoreFailure)
		writer.Enqueue(nil)
		writer.Enqueue([]xetrace.Event{})
		Expect(writer.Close()).To(Succeed())

		Expect(counting.appends.Load()).To(Equal(int64(1)), "only the append opening the stream reaches the store")
	})

	ginkgo.It("summarises exactly the committed events and previews the latest of them by seq", func() {
		writer := open(env.store(), ignoreFailure)
		batch := make([]xetrace.Event, 12)
		for index := range batch {
			batch[index] = statement(index, fmt.Sprintf("select %d", index), time.Duration(index)*time.Second)
			batch[index].LogicalReads = 10
		}
		writer.Enqueue(batch[:5])
		writer.Enqueue(batch[5:])
		writer.AddLost(3)
		writer.AddUnresolved()
		Expect(writer.Close()).To(Succeed())

		summary := writer.Summary()
		want := xetrace.Summarize(batch)
		want.Lost, want.Unresolved = 3, 1
		Expect(summary).To(Equal(want))

		Expect(writer.Preview(0, 0)).To(Equal(batch[2:]), "the ring keeps the latest ten")
		Expect(writer.Preview(6, 8)).To(Equal(batch[5:8]), "seqs 6..8 are the 6th to 8th events")
	})
})
