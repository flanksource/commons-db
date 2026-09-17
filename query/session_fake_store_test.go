package query_test

import (
	stdcontext "context"
	"fmt"
	"sync"
	"time"

	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/gomega"
)

// routeKey is the context value a routed fakeSessionStore requires, standing
// in for an environment-routed store resolver.
type routeKey struct{}

// statusWrite is one Update the fake store accepted or refused.
type statusWrite struct {
	ID     string
	Status query.SessionStatus
	Err    error
}

// fakeSessionStore is an in-memory SessionStore that records every write.
type fakeSessionStore struct {
	mu      sync.Mutex
	records map[string]query.SessionRecord
	writes  []statusWrite

	// route, when set, is the value every call's context must carry.
	route any
	// failUpdates refuses that many Updates.
	failUpdates int
	// updateDelay widens the window for write-ordering races.
	updateDelay time.Duration
}

func newFakeSessionStore() *fakeSessionStore {
	return &fakeSessionStore{records: map[string]query.SessionRecord{}}
}

func (f *fakeSessionStore) checkRoute(ctx stdcontext.Context) error {
	if ctx.Err() != nil {
		return fmt.Errorf("store call on a finished context: %w", ctx.Err())
	}
	if f.route != nil && ctx.Value(routeKey{}) != f.route {
		return fmt.Errorf("store call routed to %v, want %v", ctx.Value(routeKey{}), f.route)
	}
	return nil
}

func (f *fakeSessionStore) Begin(ctx stdcontext.Context, rec query.SessionRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.checkRoute(ctx); err != nil {
		return err
	}
	if _, exists := f.records[rec.ID]; exists {
		return fmt.Errorf("session %s already begun", rec.ID)
	}
	f.records[rec.ID] = rec
	return nil
}

func (f *fakeSessionStore) Update(ctx stdcontext.Context, id string, status query.SessionStatus) error {
	time.Sleep(f.updateDelay)
	f.mu.Lock()
	defer f.mu.Unlock()
	err := f.checkRoute(ctx)
	rec, exists := f.records[id]
	switch {
	case err != nil:
	case !exists:
		err = fmt.Errorf("session %s was never begun", id)
	case f.failUpdates > 0:
		f.failUpdates--
		err = fmt.Errorf("store unavailable")
	}
	f.writes = append(f.writes, statusWrite{ID: id, Status: status, Err: err})
	if err != nil {
		return err
	}
	rec.SessionStatus = status
	f.records[id] = rec
	return nil
}

func (f *fakeSessionStore) Get(ctx stdcontext.Context, id string) (query.SessionRecord, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.checkRoute(ctx); err != nil {
		return query.SessionRecord{}, false, err
	}
	rec, ok := f.records[id]
	return rec, ok, nil
}

func (f *fakeSessionStore) List(ctx stdcontext.Context, filter query.SessionFilter) (query.SessionPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.checkRoute(ctx); err != nil {
		return query.SessionPage{}, err
	}
	records := make([]query.SessionRecord, 0, len(f.records))
	for _, rec := range f.records {
		records = append(records, rec)
	}
	return query.ApplySessionFilter(records, filter)
}

func (f *fakeSessionStore) Lineage(stdcontext.Context, []string) (map[string][]string, error) {
	return nil, fmt.Errorf("fake store does not compute lineage")
}

func (f *fakeSessionStore) seed(rec query.SessionRecord) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.records[rec.ID] = rec
}

func (f *fakeSessionStore) record(id string) (query.SessionRecord, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rec, ok := f.records[id]
	return rec, ok
}

// status returns the persisted status of id, failing the spec when the record
// was never begun.
func (f *fakeSessionStore) status(id string) query.SessionStatus {
	rec, ok := f.record(id)
	Expect(ok).To(BeTrue(), "session %s has no record", id)
	return rec.SessionStatus
}

// statesWritten lists the states of every accepted write for id, in order.
func (f *fakeSessionStore) statesWritten(id string) []query.SessionState {
	f.mu.Lock()
	defer f.mu.Unlock()
	var states []query.SessionState
	for _, write := range f.writes {
		if write.ID == id && write.Err == nil {
			states = append(states, write.Status.State)
		}
	}
	return states
}

func (f *fakeSessionStore) writesFor(id string) []statusWrite {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []statusWrite
	for _, write := range f.writes {
		if write.ID == id {
			out = append(out, write)
		}
	}
	return out
}

func (f *fakeSessionStore) recordCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.records)
}

// fakeEventSink collects appended event sequences per session.
type fakeEventSink struct {
	mu        sync.Mutex
	sequences map[string][]int64
}

func (f *fakeEventSink) Append(_ stdcontext.Context, e query.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sequences == nil {
		f.sequences = map[string][]int64{}
	}
	f.sequences[e.SessionID] = append(f.sequences[e.SessionID], e.Sequence)
	return nil
}

func (f *fakeEventSink) appended(id string) []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int64(nil), f.sequences[id]...)
}
