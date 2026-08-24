package devtools

// Ring is a bounded, fan-out event buffer: append never blocks, every item gets
// a monotonic sequence, and a subscriber can join from a sequence it already
// holds without a gap or a duplicate.
//
// It is the valuable half of query.Session (the atomic subscribe and the
// never-blocking emit) with the session lifecycle left out, because a devtools
// stream has no profile, no result and no end. It is instantiated twice — once
// for execution records, once for the log tail — which is what makes it worth
// extracting rather than copying.
//
// query.Session should eventually be rebased onto this. Not in this change:
// session.go is load-bearing and well covered, and rebasing it here would put a
// behaviour change inside a feature addition.

import "sync"

// subscriberBuffer is the per-subscriber channel capacity. Beyond it, frames are
// dropped for that subscriber alone; the ring itself stays complete, so a slow
// browser degrades its own view and nothing else.
const subscriberBuffer = 256

// RingOptions configures NewRing.
type RingOptions[T any] struct {
	// Max is how many items the ring retains. Older items are evicted first.
	Max int

	// Stamp writes the assigned sequence into the item. A consumer that reads an
	// item on its own — out of a replay, out of an SSE frame — still has to know
	// where it sits in the stream, and threading that back through every call
	// site is how items end up unstamped in one path and stamped in another.
	Stamp func(item *T, sequence int64)
}

type Ring[T any] struct {
	max   int
	stamp func(*T, int64)

	mu          sync.Mutex
	items       []T
	head        int
	count       int
	seq         int64
	dropped     int64
	subscribers map[int]chan T
	nextSub     int
}

func NewRing[T any](options RingOptions[T]) *Ring[T] {
	if options.Max <= 0 {
		panic("devtools: ring requires a positive Max")
	}
	return &Ring[T]{
		max: options.Max, stamp: options.Stamp,
		items:       make([]T, options.Max),
		subscribers: map[int]chan T{},
	}
}

// Append stores an item and delivers it to every current subscriber, returning
// the sequence it was given.
//
// It never blocks: a subscriber whose channel is full misses this item and the
// ring keeps it, so the stream degrades for one reader rather than stalling the
// query that produced it.
func (r *Ring[T]) Append(item T) int64 {
	r.mu.Lock()
	r.seq++
	sequence := r.seq
	if r.stamp != nil {
		r.stamp(&item, sequence)
	}
	if r.count == r.max {
		r.items[r.head] = item
		r.head = (r.head + 1) % r.max
		r.dropped++
	} else {
		r.items[(r.head+r.count)%r.max] = item
		r.count++
	}
	subscribers := make([]chan T, 0, len(r.subscribers))
	for _, channel := range r.subscribers {
		subscribers = append(subscribers, channel)
	}
	r.mu.Unlock()

	for _, channel := range subscribers {
		select {
		case channel <- item:
		default:
		}
	}
	return sequence
}

// SubscribeFrom returns everything past `after` that the ring still holds, plus
// a channel carrying everything appended from now on.
//
// Both are produced under one lock so there is no window in which an item is in
// neither: a replay taken before registering would miss anything appended in
// between, and registering first would deliver it twice.
func (r *Ring[T]) SubscribeFrom(after int64) (replay []T, live <-chan T, cancel func()) {
	r.mu.Lock()
	defer r.mu.Unlock()

	replay = r.itemsAfterLocked(after)
	channel := make(chan T, subscriberBuffer)
	id := r.nextSub
	r.nextSub++
	r.subscribers[id] = channel

	return replay, channel, func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if existing, ok := r.subscribers[id]; ok {
			delete(r.subscribers, id)
			close(existing)
		}
	}
}

// Items returns everything the ring holds, oldest first.
func (r *Ring[T]) Items() []T {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.itemsAfterLocked(0)
}

// ItemsAfter returns everything past a sequence the caller already holds.
func (r *Ring[T]) ItemsAfter(after int64) []T {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.itemsAfterLocked(after)
}

func (r *Ring[T]) itemsAfterLocked(after int64) []T {
	// The ring's oldest retained sequence is (newest - count + 1); anything the
	// caller asks for below that was evicted and is reported through Dropped
	// rather than silently skipped.
	oldest := r.seq - int64(r.count) + 1
	skip := int64(0)
	if after >= oldest {
		skip = after - oldest + 1
	}
	if skip >= int64(r.count) {
		return nil
	}
	out := make([]T, 0, int64(r.count)-skip)
	for i := skip; i < int64(r.count); i++ {
		out = append(out, r.items[(r.head+int(i))%r.max])
	}
	return out
}

// Dropped is how many items the ring has evicted. A client that finds a gap
// between its Last-Event-ID and the next sequence needs this to tell "nothing
// happened" from "I stopped reading for too long".
func (r *Ring[T]) Dropped() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.dropped
}

// OldestSequence is the lowest sequence still retained, or 0 when empty.
func (r *Ring[T]) OldestSequence() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.count == 0 {
		return 0
	}
	return r.seq - int64(r.count) + 1
}

// Clear discards everything retained. Sequences keep climbing: a client holding
// a Last-Event-ID from before the clear must not be handed sequences it has
// already seen.
func (r *Ring[T]) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items = make([]T, r.max)
	r.head = 0
	r.count = 0
}
