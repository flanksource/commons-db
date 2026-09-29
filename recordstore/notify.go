package recordstore

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/flanksource/commons/logger"
)

// ChangeSource is a backend another process may write: WatchChanges calls
// changed whenever that process may have committed, until ctx ends, without
// knowing which streams it changed.
type ChangeSource interface {
	WatchChanges(ctx context.Context, changed func()) error
}

// NotifierOptions configure NewNotifier.
type NotifierOptions struct {
	// RecheckInterval is how often a waiter re-reads its stream's metadata
	// while no append wakes it. Appends made through the Notifier wake waiters
	// at once; the recheck is what notices what no append announces — a stream
	// expired, trimmed away or removed by the backend itself. Required.
	RecheckInterval time.Duration
}

// Notifier is a Backend that wakes the readers waiting on a stream as soon as
// an append to it commits, which is what lets a reader follow a stream rather
// than poll it. It delegates every call to the backend it wraps.
//
// Only appends made through the Notifier wake a waiter at once: a stream has
// one writer process (see the package documentation), so that writer appending
// through the Notifier is the whole of the contract. Anything else is seen at
// the next recheck — but for a backend that is a ChangeSource, whose changes
// wake every waiter, which then re-reads its stream.
type Notifier struct {
	backend Backend
	recheck time.Duration

	// stopWatching ends the watch of a ChangeSource backend, and watched is
	// closed once it has ended.
	stopWatching context.CancelFunc
	watched      chan struct{}

	mu      sync.Mutex
	signals map[string]*appendSignal
}

// appendSignal is closed by the next append to its stream. Waiters share one
// signal per stream, counted so an unused one is dropped.
type appendSignal struct {
	appended chan struct{}
	waiters  int
}

var _ Backend = (*Notifier)(nil)

// NewNotifier wraps backend.
func NewNotifier(backend Backend, options NotifierOptions) (*Notifier, error) {
	switch {
	case backend == nil:
		return nil, errors.New("record store notifier: a backend is required")
	case options.RecheckInterval <= 0:
		return nil, fmt.Errorf("record store notifier: RecheckInterval must be positive, got %s", options.RecheckInterval)
	}
	if _, wrapped := backend.(*Notifier); wrapped {
		return nil, errors.New("record store notifier: the backend already is a notifier; wrap the backend it wraps once")
	}
	notifier := &Notifier{backend: backend, recheck: options.RecheckInterval, signals: map[string]*appendSignal{}, watched: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	notifier.stopWatching = cancel
	go notifier.watch(ctx)
	return notifier, nil
}

// watch wakes every waiter whenever a ChangeSource backend reports a change,
// until ctx ends. A watch that fails is logged: waiters still see the change
// at their next recheck.
func (n *Notifier) watch(ctx context.Context) {
	defer close(n.watched)
	source, ok := n.backend.(ChangeSource)
	if !ok {
		return
	}
	if err := source.WatchChanges(ctx, n.wakeAll); err != nil {
		logger.Errorf("record store notifier: watch changes: %v", err)
	}
}

// Unwrap is the backend the Notifier delegates to.
func (n *Notifier) Unwrap() Backend { return n.backend }

// Append appends through the wrapped backend and, once the append committed,
// wakes every waiter on stream.
func (n *Notifier) Append(ctx context.Context, stream, kind string, rows []Row) (AppendResult, error) {
	result, err := n.backend.Append(ctx, stream, kind, rows)
	if err != nil {
		return result, err
	}
	n.wake(stream)
	return result, nil
}

func (n *Notifier) Meta(ctx context.Context, stream string) (Meta, error) {
	return n.backend.Meta(ctx, stream)
}

func (n *Notifier) Scan(ctx context.Context, stream string, afterSeq int64, fn func(seq int64, row Row) error) error {
	return n.backend.Scan(ctx, stream, afterSeq, fn)
}

// Trim trims through the wrapped backend and wakes the stream's waiters, which
// re-read what is left.
func (n *Notifier) Trim(ctx context.Context, stream string, before time.Time) (Meta, error) {
	meta, err := n.backend.Trim(ctx, stream, before)
	n.wake(stream)
	return meta, err
}

// Expire sets the expiry through the wrapped backend and wakes the stream's
// waiters.
func (n *Notifier) Expire(ctx context.Context, stream string, ttl time.Duration) error {
	err := n.backend.Expire(ctx, stream, ttl)
	n.wake(stream)
	return err
}

// Seal seals through the wrapped backend and wakes the stream's waiters, which
// finish once they have read what the stream holds.
func (n *Notifier) Seal(ctx context.Context, stream string) error {
	err := n.backend.Seal(ctx, stream)
	n.wake(stream)
	return err
}

func (n *Notifier) Reopen(ctx context.Context, stream, generation string) error {
	reopener, ok := n.backend.(Reopener)
	if !ok {
		return fmt.Errorf("record store for %q cannot reopen streams", stream)
	}
	if err := reopener.Reopen(ctx, stream, generation); err != nil {
		return err
	}
	n.wake(stream)
	return nil
}

// AppendBatch applies batch through the wrapped backend, which must be a
// BatchAppender, and wakes the waiters on every stream the batch names.
func (n *Notifier) AppendBatch(ctx context.Context, batch Batch) (BatchResult, error) {
	appender, ok := n.backend.(BatchAppender)
	if !ok {
		return BatchResult{}, fmt.Errorf("record store cannot apply batch %q: %w", batch.ID, ErrUnsupported)
	}
	result, err := appender.AppendBatch(ctx, batch)
	if err != nil {
		return result, err
	}
	for _, entry := range batch.Entries {
		n.wake(entry.Stream)
	}
	return result, nil
}

func (n *Notifier) BatchOutcome(ctx context.Context, id string) (BatchResult, bool, error) {
	appender, ok := n.backend.(BatchAppender)
	if !ok {
		return BatchResult{}, false, fmt.Errorf("record store cannot read batch %q: %w", id, ErrUnsupported)
	}
	return appender.BatchOutcome(ctx, id)
}

func (n *Notifier) Delete(ctx context.Context, stream string) error {
	err := n.backend.Delete(ctx, stream)
	n.wake(stream)
	return err
}

// Close stops watching the backend and closes it.
func (n *Notifier) Close() error {
	n.stopWatching()
	<-n.watched
	return n.backend.Close()
}

// wakeAll wakes the waiters on every stream.
func (n *Notifier) wakeAll() {
	n.mu.Lock()
	defer n.mu.Unlock()
	for stream, signal := range n.signals {
		close(signal.appended)
		delete(n.signals, stream)
	}
}

func (n *Notifier) wake(stream string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if signal, ok := n.signals[stream]; ok {
		close(signal.appended)
		delete(n.signals, stream)
	}
}

// subscribe returns the signal the next append to stream closes, and the
// function that gives it up.
func (n *Notifier) subscribe(stream string) (<-chan struct{}, func()) {
	n.mu.Lock()
	defer n.mu.Unlock()
	signal, ok := n.signals[stream]
	if !ok {
		signal = &appendSignal{appended: make(chan struct{})}
		n.signals[stream] = signal
	}
	signal.waiters++
	return signal.appended, func() {
		n.mu.Lock()
		defer n.mu.Unlock()
		signal.waiters--
		if signal.waiters == 0 && n.signals[stream] == signal {
			delete(n.signals, stream)
		}
	}
}

// Wait blocks until stream holds a row after afterSeq, or is sealed, and
// returns its metadata: a sealed stream whose HighSeq is at or below afterSeq
// has nothing more to wait for. generation is the incarnation of the stream the caller has read:
// a stream that is gone, or that was recreated under another generation since,
// is ErrNotFound, because the seqs the caller holds no longer name its rows. A
// cancelled ctx returns ctx.Err().
//
// The metadata is read after subscribing to the next append, so an append that
// commits between the read and the wait still wakes it.
func (n *Notifier) Wait(ctx context.Context, stream string, afterSeq int64, generation string) (Meta, error) {
	if generation == "" {
		return Meta{}, fmt.Errorf("wait on stream %q: the generation read is required", stream)
	}
	for {
		appended, unsubscribe := n.subscribe(stream)
		meta, err := n.backend.Meta(ctx, stream)
		switch {
		case err != nil:
			unsubscribe()
			return Meta{}, n.waitErr(ctx, stream, err)
		case meta.Generation != generation:
			unsubscribe()
			return Meta{}, fmt.Errorf("stream %q was recreated as generation %q after generation %q was read: %w",
				stream, meta.Generation, generation, ErrNotFound)
		case meta.HighSeq > afterSeq || meta.Sealed:
			unsubscribe()
			return meta, nil
		}
		err = n.sleep(ctx, appended)
		unsubscribe()
		if err != nil {
			return Meta{}, err
		}
	}
}

// sleep waits for an append, the next recheck, or ctx.
func (n *Notifier) sleep(ctx context.Context, appended <-chan struct{}) error {
	timer := time.NewTimer(n.recheck)
	defer timer.Stop()
	select {
	case <-appended:
	case <-timer.C:
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}

// waitErr reports a failure caused by ctx as ctx's own error, so a caller can
// tell a cancelled wait from a backend that failed.
func (n *Notifier) waitErr(ctx context.Context, stream string, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return fmt.Errorf("wait on stream %q: %w", stream, err)
}

// Tail calls fn with every row of stream after afterSeq, in seq order, and then
// with every row appended after that as it is appended. It returns nil once
// ctx ends or it has read through a sealed stream's high seq, fn's first error,
// and an ErrNotFound error when the stream does not exist or stops existing —
// expired, removed, or recreated as a new generation.
func (n *Notifier) Tail(ctx context.Context, stream string, afterSeq int64, fn func(seq int64, row Row) error) error {
	meta, err := n.backend.Meta(ctx, stream)
	if err != nil {
		return tailEnd(ctx, fmt.Errorf("tail stream %q: %w", stream, err))
	}
	last := afterSeq
	for {
		err := n.backend.Scan(ctx, stream, last, func(seq int64, row Row) error {
			if err := fn(seq, row); err != nil {
				return err
			}
			last = seq
			return nil
		})
		if err != nil {
			return tailEnd(ctx, fmt.Errorf("tail stream %q: %w", stream, err))
		}
		latest, err := n.Wait(ctx, stream, last, meta.Generation)
		if err != nil {
			return tailEnd(ctx, fmt.Errorf("tail stream %q: %w", stream, err))
		}
		if latest.Sealed && latest.HighSeq <= last {
			return nil
		}
	}
}

// tailEnd is nil for a tail whose ctx ended, which is how a tail is stopped,
// and err otherwise.
func tailEnd(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return nil
	}
	return err
}
