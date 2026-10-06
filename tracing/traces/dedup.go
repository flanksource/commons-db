// The dedup window WithDeduplication gives a session: a record whose key the
// session already emitted within the window is dropped before it is stored.

package traces

import "time"

// sweepEvery is how many admitted keys pass between sweeps of expired ones.
const sweepEvery = 1024

type deduplicator[R any] struct {
	window  time.Duration
	key     func(R) string
	now     func() time.Time
	seen    map[string]time.Time
	inserts int
}

func newDeduplicator[R any](window time.Duration, key func(R) string) *deduplicator[R] {
	return &deduplicator[R]{window: window, key: key, now: time.Now, seen: map[string]time.Time{}}
}

// reserve claims record's key for the window, reporting false when the key
// was already claimed within it: by a record that was accepted, or by one
// still being accepted. A claim for a record that is then not accepted is
// given back with forget. A dropped record does not extend its key's window.
func (d *deduplicator[R]) reserve(record R) (string, bool) {
	key := d.key(record)
	now := d.now()
	if last, ok := d.seen[key]; ok && now.Sub(last) < d.window {
		return key, false
	}
	d.seen[key] = now
	d.inserts++
	if d.inserts%sweepEvery == 0 {
		d.sweep()
	}
	return key, true
}

// forget gives back the claim on key of a record that was not accepted.
func (d *deduplicator[R]) forget(key string) {
	delete(d.seen, key)
}

// sweep forgets the keys whose window has passed.
func (d *deduplicator[R]) sweep() {
	now := d.now()
	for key, last := range d.seen {
		if now.Sub(last) >= d.window {
			delete(d.seen, key)
		}
	}
}
