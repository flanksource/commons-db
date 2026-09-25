package sqltrace

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/flanksource/commons-db/tracing/xetrace"
)

// blockingAppender models the real failure mode: a store whose writes are slow
// enough that appending inline would stall the caller. releaseAll unblocks it.
type blockingAppender struct {
	gate chan struct{}

	mu     sync.Mutex
	chunks []pendingChunk
	err    error
}

func newBlockingAppender() *blockingAppender {
	return &blockingAppender{gate: make(chan struct{})}
}

func (b *blockingAppender) Append(_ string, seq int, events []xetrace.Event) error {
	<-b.gate
	b.mu.Lock()
	defer b.mu.Unlock()
	b.chunks = append(b.chunks, pendingChunk{seq: seq, events: events})
	return b.err
}

func (b *blockingAppender) releaseAll() { close(b.gate) }

func (b *blockingAppender) seqs() []int {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]int, 0, len(b.chunks))
	for _, c := range b.chunks {
		out = append(out, c.seq)
	}
	return out
}

func chunkEvents(stmt string) []xetrace.Event {
	return []xetrace.Event{ev(1, stmt, time.Unix(0, 0))}
}

// The property the whole type exists for: the drain loop keeps running while
// the store is wedged. Enqueue must return regardless of how far behind the
// writer is, because every millisecond blocked is a millisecond in which SQL
// Server's ring buffer can overwrite unread events.
func TestEventWriterEnqueueDoesNotBlockOnSlowStore(t *testing.T) {
	store := newBlockingAppender()
	w := newEventWriter(store, "trace-1")

	const chunks = 200
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < chunks; i++ {
			w.Enqueue(i, chunkEvents(fmt.Sprintf("select %d", i)))
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Enqueue blocked while the store was wedged")
	}

	store.releaseAll()
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := len(store.seqs()); got != chunks {
		t.Fatalf("wrote %d chunks, want %d", got, chunks)
	}
}

// Chunk order is the contract EventStore.Since relies on: sequence numbers are
// allocated on the drain loop, and the writer must not reorder them.
func TestEventWriterPreservesChunkOrder(t *testing.T) {
	store := newBlockingAppender()
	w := newEventWriter(store, "trace-2")

	const chunks = 50
	for i := 0; i < chunks; i++ {
		w.Enqueue(i, chunkEvents(fmt.Sprintf("select %d", i)))
	}
	store.releaseAll()
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	got := store.seqs()
	if len(got) != chunks {
		t.Fatalf("wrote %d chunks, want %d", len(got), chunks)
	}
	for i, seq := range got {
		if seq != i {
			t.Fatalf("chunk %d written out of order: got seq %d", i, seq)
		}
	}
}

// Close is the durability checkpoint: everything enqueued must reach the store
// before it returns, or a reader unblocked by trace.done sees a missing chunk.
func TestEventWriterCloseDrainsQueue(t *testing.T) {
	store := newBlockingAppender()
	store.releaseAll() // never blocks; we are testing the drain, not the stall
	w := newEventWriter(store, "trace-3")

	for i := 0; i < 25; i++ {
		w.Enqueue(i, chunkEvents("select 1"))
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := len(store.seqs()); got != 25 {
		t.Fatalf("Close returned with %d of 25 chunks written", got)
	}
}

// A write failure must survive to Close rather than being logged and forgotten —
// runDrain folds it into the trace's terminal error.
func TestEventWriterSurfacesStoreErrors(t *testing.T) {
	store := newBlockingAppender()
	store.err = fmt.Errorf("redis unavailable")
	store.releaseAll()
	w := newEventWriter(store, "trace-4")

	w.Enqueue(0, chunkEvents("select 1"))
	err := w.Close()
	if err == nil {
		t.Fatal("Close returned nil, want the store error")
	}
	if got := err.Error(); got != "redis unavailable" {
		t.Fatalf("Close error = %q, want it to carry the store error", got)
	}
}

// Close is idempotent, and a chunk offered afterwards is reported rather than
// silently dropped.
func TestEventWriterCloseIsIdempotentAndRejectsLateChunks(t *testing.T) {
	store := newBlockingAppender()
	store.releaseAll()
	w := newEventWriter(store, "trace-5")

	w.Enqueue(0, chunkEvents("select 1"))
	if err := w.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	w.Enqueue(1, chunkEvents("select 2"))
	err := w.Close()
	if err == nil {
		t.Fatal("Close after a late Enqueue returned nil, want a discard error")
	}
	if got := len(store.seqs()); got != 1 {
		t.Fatalf("wrote %d chunks, want the late one rejected", got)
	}
}

// Empty batches are skipped rather than queued as empty chunks, matching
// EventStore.Append's own contract.
func TestEventWriterSkipsEmptyBatches(t *testing.T) {
	store := newBlockingAppender()
	store.releaseAll()
	w := newEventWriter(store, "trace-6")

	w.Enqueue(0, nil)
	w.Enqueue(1, []xetrace.Event{})
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := len(store.seqs()); got != 0 {
		t.Fatalf("wrote %d chunks, want 0", got)
	}
}
