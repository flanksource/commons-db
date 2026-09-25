package sqltrace

import (
	"errors"
	"fmt"
	"sync"

	"github.com/flanksource/commons/logger"

	"github.com/flanksource/commons-db/tracing/xetrace"
)

// chunkAppender is the slice of EventStore the writer needs. Narrowing it here
// lets a test inject a deliberately slow or failing store without standing up
// Redis.
type chunkAppender interface {
	Append(traceID string, seq int, events []xetrace.Event) error
}

// backlogWarnFloor is the queue depth at which a backlog first gets reported,
// doubling thereafter (64, 128, 256 …). The queue is deliberately unbounded, so
// this is the only signal that Redis is not keeping up — without it a pathological
// backlog would grow silently until the process ran out of memory.
const backlogWarnFloor = 64

// eventWriter decouples the Extended Events drain loop from Redis.
//
// The drain loop must return to SQL Server's ring buffer promptly: the buffer is
// fixed-size and overwrites the oldest events once full, so every millisecond
// spent writing is a millisecond in which captured events can be lost
// ("lost N event(s) before they could be read"). Appending inline did exactly
// that — EventStore.Append goes through cachestore's async handle, whose channel
// is bounded and whose Set BLOCKS once the flusher falls behind
// (internal/cachestore/async.go, "backpressure when the writer falls behind").
// Against a slow L2 that is a half-second stall per poll, and the drain loop
// wears it.
//
// So the poll batch is handed to an UNBOUNDED queue here and a single goroutine
// drains it into the store. Enqueue never blocks and never fails, which moves
// the backpressure off the drain loop and onto memory; the trade is deliberate,
// because a dropped event is unrecoverable while a queued one is merely late.
// Each wake takes everything queued, so a slow store naturally coalesces into
// larger, less frequent write batches instead of one round-trip per poll.
type eventWriter struct {
	store   chunkAppender
	traceID string

	mu     sync.Mutex
	cond   *sync.Cond
	queue  []pendingChunk
	closed bool

	// maxDepth is the high-water queue depth, and warnAt the next depth that
	// earns a log line. Both are only touched under mu.
	maxDepth int
	warnAt   int

	errMu sync.Mutex
	err   error

	done chan struct{}
}

// pendingChunk is one poll's events plus the sequence number already allocated
// for it. The sequence is assigned on the drain loop so chunk order stays poll
// order regardless of how the writer batches.
type pendingChunk struct {
	seq    int
	events []xetrace.Event
}

// newEventWriter starts the writer goroutine. Close must be called to drain it.
func newEventWriter(store chunkAppender, traceID string) *eventWriter {
	w := &eventWriter{
		store:   store,
		traceID: traceID,
		warnAt:  backlogWarnFloor,
		done:    make(chan struct{}),
	}
	w.cond = sync.NewCond(&w.mu)
	go w.run()
	return w
}

// Enqueue hands one poll's events to the writer. It never blocks: that is the
// whole point of the type. events must not be mutated afterwards — the drain
// loop allocates a fresh slice per poll, so it hands off ownership.
func (w *eventWriter) Enqueue(seq int, events []xetrace.Event) {
	if len(events) == 0 {
		return
	}
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		w.recordErr(fmt.Errorf("trace %s chunk %d discarded: writer already closed", w.traceID, seq))
		return
	}
	w.queue = append(w.queue, pendingChunk{seq: seq, events: events})
	depth := len(w.queue)
	if depth > w.maxDepth {
		w.maxDepth = depth
	}
	warn := false
	if depth >= w.warnAt {
		warn = true
		w.warnAt *= 2
	}
	w.mu.Unlock()
	w.cond.Signal()

	if warn {
		logger.Warnf(
			"sqltrace: trace %s has %d unwritten event chunk(s) queued — the cache is not keeping up with capture",
			w.traceID, depth,
		)
	}
}

// run drains the queue until Close. Each wake takes every queued chunk, so a
// slow store coalesces into fewer, larger write batches rather than one
// round-trip per poll.
func (w *eventWriter) run() {
	defer close(w.done)
	for {
		w.mu.Lock()
		for len(w.queue) == 0 && !w.closed {
			w.cond.Wait()
		}
		if len(w.queue) == 0 && w.closed {
			w.mu.Unlock()
			return
		}
		batch := w.queue
		w.queue = nil
		w.mu.Unlock()

		for _, chunk := range batch {
			if err := w.store.Append(w.traceID, chunk.seq, chunk.events); err != nil {
				w.recordErr(err)
				logger.Warnf("sqltrace: %v", err)
			}
		}
	}
}

// Close stops accepting chunks, waits for the queue to drain, and returns every
// write error seen. Idempotent.
func (w *eventWriter) Close() error {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		<-w.done
		return w.loadErr()
	}
	w.closed = true
	depth := w.maxDepth
	w.mu.Unlock()
	w.cond.Broadcast()
	<-w.done

	if depth >= backlogWarnFloor {
		logger.Warnf("sqltrace: trace %s peaked at %d queued event chunk(s)", w.traceID, depth)
	}
	return w.loadErr()
}

func (w *eventWriter) recordErr(err error) {
	if err == nil {
		return
	}
	w.errMu.Lock()
	w.err = errors.Join(w.err, err)
	w.errMu.Unlock()
}

func (w *eventWriter) loadErr() error {
	w.errMu.Lock()
	defer w.errMu.Unlock()
	return w.err
}
