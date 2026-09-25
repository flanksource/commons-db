package sqltrace

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons/logger"

	"github.com/flanksource/commons-db/tracing/xetrace"
)

// chunkAppender commits one poll's events and reports the seq window they took
// and the stream's ref as of that commit. recordAppender is the production one.
type chunkAppender interface {
	Append(events []xetrace.Event) (recordstore.Window, query.EventsRef, error)
}

// backlogWarnFloor is the queue depth at which a backlog first gets reported,
// doubling thereafter (64, 128, 256 …). The queue is deliberately unbounded, so
// this is the only signal that the store is not keeping up — without it a
// pathological backlog would grow silently until the process ran out of memory.
const backlogWarnFloor = 64

// previewSize is how many of the latest committed events the writer keeps for a
// result's preview.
const previewSize = 10

// previewEvent is a committed event and the seq the store gave its row.
type previewEvent struct {
	seq   int64
	event xetrace.Event
}

// eventWriter decouples the Extended Events drain loop from the record store.
//
// The drain loop must return to SQL Server's ring buffer promptly: the buffer is
// fixed-size and overwrites the oldest events once full, so every millisecond
// spent writing is a millisecond in which captured events can be lost. So a poll
// batch is handed to an UNBOUNDED queue and a single goroutine commits it.
// Enqueue never blocks, which moves the backpressure off the drain loop and onto
// memory; the trade is deliberate, because a dropped event is unrecoverable
// while a queued one is merely late.
//
// The writer is also the capture's source of truth for what has been stored:
// the summary and preview fold only committed events, and Checkpoint is a
// barrier that never describes rows the store does not hold yet.
type eventWriter struct {
	store  chunkAppender
	onFail func(error)

	mu       sync.Mutex
	wake     *sync.Cond
	queue    [][]xetrace.Event
	closed   bool
	enqueued int64 // chunks handed to Enqueue
	settled  int64 // chunks committed, or discarded once the writer failed
	// settledCh is closed, and replaced, whenever settled or err changes.
	settledCh chan struct{}
	ref       query.EventsRef
	err       error
	summary   *xetrace.Accumulator
	preview   []previewEvent
	maxDepth  int
	warnAt    int

	done chan struct{}
}

// newEventWriter opens the stream with an empty append, so a reader finds it
// from the start of the capture, and starts the commit goroutine. onFail runs
// once, on the commit goroutine, with the first store failure; Close must be
// called to drain the writer.
func newEventWriter(store chunkAppender, onFail func(error)) (*eventWriter, error) {
	_, ref, err := store.Append(nil)
	if err != nil {
		return nil, fmt.Errorf("open sql_xevent stream: %w", err)
	}
	w := &eventWriter{
		store: store, onFail: onFail, ref: ref, summary: xetrace.NewAccumulator(),
		settledCh: make(chan struct{}), warnAt: backlogWarnFloor, done: make(chan struct{}),
	}
	w.wake = sync.NewCond(&w.mu)
	go w.run()
	return w, nil
}

// Enqueue hands one poll's events to the writer without blocking. events must
// not be mutated afterwards: the drain loop allocates a fresh slice per poll.
func (w *eventWriter) Enqueue(events []xetrace.Event) {
	if len(events) == 0 {
		return
	}
	w.mu.Lock()
	if w.closed {
		w.fail(fmt.Errorf("%d event(s) discarded: writer already closed", len(events)))
		w.mu.Unlock()
		return
	}
	w.queue = append(w.queue, events)
	w.enqueued++
	depth := len(w.queue)
	w.maxDepth = max(w.maxDepth, depth)
	warn := depth >= w.warnAt
	if warn {
		w.warnAt *= 2
	}
	stream := w.ref.Stream
	w.mu.Unlock()
	w.wake.Signal()
	if warn {
		logger.Warnf("sqltrace: stream %s has %d uncommitted event chunk(s) queued — the record store is not keeping up with capture", stream, depth)
	}
}

// run commits queued chunks in poll order until Close.
func (w *eventWriter) run() {
	defer close(w.done)
	for {
		w.mu.Lock()
		for len(w.queue) == 0 && !w.closed {
			w.wake.Wait()
		}
		if len(w.queue) == 0 {
			w.mu.Unlock()
			return
		}
		batch := w.queue
		w.queue = nil
		w.mu.Unlock()
		for _, events := range batch {
			w.commit(events)
		}
	}
}

// commit stores one chunk. After the first failure nothing more is stored: the
// rows after a gap would read as a complete capture.
func (w *eventWriter) commit(events []xetrace.Event) {
	w.mu.Lock()
	failed := w.err != nil
	w.mu.Unlock()
	var window recordstore.Window
	var ref query.EventsRef
	var err error
	if !failed {
		window, ref, err = w.store.Append(events)
	}

	w.mu.Lock()
	first := err != nil && w.err == nil
	switch {
	case err != nil:
		w.err = err
	case !failed:
		w.ref = ref
		w.summary.AddAll(events)
		w.remember(window.From, events)
	}
	w.settled++
	w.signal()
	w.mu.Unlock()

	if first {
		logger.Errorf("sqltrace: %v", err)
		w.onFail(err)
	}
}

// remember keeps the latest previewSize committed events, numbered from seq.
func (w *eventWriter) remember(seq int64, events []xetrace.Event) {
	skip := max(0, len(events)-previewSize)
	for index, event := range events[skip:] {
		w.preview = append(w.preview, previewEvent{seq: seq + int64(skip+index), event: event})
	}
	w.preview = w.preview[max(0, len(w.preview)-previewSize):]
}

// fail records err; mu must be held.
func (w *eventWriter) fail(err error) {
	w.err = errors.Join(w.err, err)
	w.signal()
}

// signal wakes every waiting checkpoint; mu must be held.
func (w *eventWriter) signal() {
	close(w.settledCh)
	w.settledCh = make(chan struct{})
}

// Checkpoint blocks until every chunk enqueued before the call is committed,
// then returns the stream's ref as of the last commit. It never describes a row
// the store does not hold. A store failure, past or while waiting, is returned.
func (w *eventWriter) Checkpoint(ctx context.Context) (query.EventsRef, error) {
	w.mu.Lock()
	target := w.enqueued
	for {
		if w.err != nil {
			err := w.err
			w.mu.Unlock()
			return query.EventsRef{}, err
		}
		if w.settled >= target {
			ref := w.ref
			w.mu.Unlock()
			return ref, nil
		}
		settled := w.settledCh
		w.mu.Unlock()
		select {
		case <-settled:
		case <-ctx.Done():
			return query.EventsRef{}, fmt.Errorf("checkpoint stream %s: %w", w.EventsRef().Stream, ctx.Err())
		}
		w.mu.Lock()
	}
}

// EventsRef is the stream's ref as of the last commit, without waiting.
func (w *eventWriter) EventsRef() query.EventsRef {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.ref
}

// Summary is the IO/CPU/timing aggregate over every committed event, with the
// events the capture lost or could not resolve.
func (w *eventWriter) Summary() xetrace.Summary {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.summary.Result()
}

// AddLost counts events the server evicted before they were read.
func (w *eventWriter) AddLost(n int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.summary.AddLost(n)
}

// AddUnresolved counts an event the filter dropped only for want of its text.
func (w *eventWriter) AddUnresolved() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.summary.AddUnresolved()
}

// Preview returns the kept events whose seq is within from..to, oldest first. A
// from or to of 0 leaves that side open.
func (w *eventWriter) Preview(from, to int64) []xetrace.Event {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := []xetrace.Event{}
	for _, kept := range w.preview {
		if (from == 0 || kept.seq >= from) && (to == 0 || kept.seq <= to) {
			out = append(out, kept.event)
		}
	}
	return out
}

// Close stops accepting chunks, waits for the queue to be committed, and
// returns the store failure, if any. Idempotent.
func (w *eventWriter) Close() error {
	w.mu.Lock()
	alreadyClosed := w.closed
	w.closed = true
	depth, stream := w.maxDepth, w.ref.Stream
	w.mu.Unlock()
	w.wake.Broadcast()
	<-w.done
	if !alreadyClosed && depth >= backlogWarnFloor {
		logger.Warnf("sqltrace: stream %s peaked at %d queued event chunk(s)", stream, depth)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err
}
