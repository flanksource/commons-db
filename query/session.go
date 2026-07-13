package query

import (
	stdcontext "context"
	"fmt"
	"sync"
	"time"

	"github.com/flanksource/commons-db/context"
)

// SessionState is the lifecycle state of a trace or top session.
type SessionState string

const (
	SessionStarting    SessionState = "starting"
	SessionRunning     SessionState = "running"
	SessionCompleted   SessionState = "completed"
	SessionFailed      SessionState = "failed"
	SessionStopped     SessionState = "stopped"
	SessionInterrupted SessionState = "interrupted"
)

// Terminal reports whether no further transitions or events can occur.
func (s SessionState) Terminal() bool {
	return s == SessionCompleted || s == SessionFailed || s == SessionStopped || s == SessionInterrupted
}

// Event is one emission from a session: a single streamed row (trace) or a
// full snapshot for one tick (top).
type Event struct {
	SessionID string    `json:"sessionId"`
	Sequence  int64     `json:"sequence"`
	Time      time.Time `json:"time"`
	Row       Row       `json:"row,omitempty"`
	Rows      []Row     `json:"rows,omitempty"`
	Error     string    `json:"error,omitempty"`
}

// SessionInfo is a JSON-safe snapshot of a session's state.
type SessionInfo struct {
	ID         string         `json:"id"`
	Profile    string         `json:"profile"`
	Kind       ProfileKind    `json:"kind"`
	State      SessionState   `json:"state"`
	Params     map[string]any `json:"params,omitempty"`
	Error      string         `json:"error,omitempty"`
	EventCount int64          `json:"eventCount"`
	StartedAt  time.Time      `json:"startedAt"`
	StoppedAt  *time.Time     `json:"stoppedAt,omitempty"`
}

// SessionOptions configures NewSession.
type SessionOptions struct {
	ID      string
	Profile Profile
	Params  map[string]any

	// MaxEvents caps the in-memory ring buffer (already clamped by the caller).
	MaxEvents int

	// OnEvent is called synchronously for every emitted event (before ring
	// eviction can drop it) — the persistence hook.
	OnEvent func(Event)

	// OnTransition is called synchronously after every state change.
	OnTransition func(SessionInfo)
}

// subscriberBuffer is the per-subscriber channel capacity; Emit never blocks —
// events beyond it are dropped for that subscriber (the ring stays complete).
const subscriberBuffer = 256

// Session is one running (or finished) trace/top execution: a capped ring
// buffer of events, live subscribers, and a state machine
// starting → running → {completed|failed|stopped}.
type Session struct {
	id      string
	profile Profile
	params  map[string]any

	mu          sync.Mutex
	state       SessionState
	err         string
	startedAt   time.Time
	stoppedAt   *time.Time
	cancel      stdcontext.CancelFunc
	ring        []Event
	head, count int
	seq         int64
	subscribers map[int]chan Event
	nextSub     int
	latest      *Result

	onEvent      func(Event)
	onTransition func(SessionInfo)
}

// NewSession creates a session in the starting state. The caller is expected
// to have validated the profile and clamped MaxEvents.
func NewSession(opts SessionOptions) *Session {
	max := opts.MaxEvents
	if max <= 0 {
		max = DefaultMaxEvents
	}
	return &Session{
		id:           opts.ID,
		profile:      opts.Profile,
		params:       opts.Params,
		state:        SessionStarting,
		startedAt:    time.Now(),
		ring:         make([]Event, max),
		subscribers:  map[int]chan Event{},
		onEvent:      opts.OnEvent,
		onTransition: opts.OnTransition,
	}
}

// ID returns the session's identifier.
func (s *Session) ID() string { return s.id }

// Emit stamps the event with the next sequence, appends it to the ring
// (evicting the oldest at capacity), and fans it out to subscribers without
// blocking. Events emitted after the session is terminal are discarded.
func (s *Session) Emit(e Event) {
	s.mu.Lock()
	if s.state.Terminal() {
		s.mu.Unlock()
		return
	}
	s.seq++
	e.SessionID = s.id
	e.Sequence = s.seq
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	s.ring[(s.head+s.count)%len(s.ring)] = e
	if s.count < len(s.ring) {
		s.count++
	} else {
		s.head = (s.head + 1) % len(s.ring)
	}
	for _, ch := range s.subscribers {
		select {
		case ch <- e:
		default: // slow subscriber: drop, never block the stream
		}
	}
	hook := s.onEvent
	s.mu.Unlock()

	if hook != nil {
		hook(e)
	}
}

// Events returns a copy of the buffered events, oldest first.
func (s *Session) Events() []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Event, s.count)
	for i := 0; i < s.count; i++ {
		out[i] = s.ring[(s.head+i)%len(s.ring)]
	}
	return out
}

// Subscribe atomically returns the buffered events and a live channel for
// subsequent ones — no gap, no duplication. The channel is closed when the
// session reaches a terminal state; cancel detaches the subscriber.
func (s *Session) Subscribe() (replay []Event, live <-chan Event, cancel func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	replay = make([]Event, s.count)
	for i := 0; i < s.count; i++ {
		replay[i] = s.ring[(s.head+i)%len(s.ring)]
	}
	ch := make(chan Event, subscriberBuffer)
	if s.state.Terminal() {
		close(ch)
		return replay, ch, func() {}
	}
	id := s.nextSub
	s.nextSub++
	s.subscribers[id] = ch
	return replay, ch, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if ch, ok := s.subscribers[id]; ok {
			delete(s.subscribers, id)
			close(ch)
		}
	}
}

// Snapshot returns a JSON-safe copy of the session's current state.
func (s *Session) Snapshot() SessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotLocked()
}

func (s *Session) snapshotLocked() SessionInfo {
	return SessionInfo{
		ID:         s.id,
		Profile:    s.profile.Name,
		Kind:       s.profile.Kind(),
		State:      s.state,
		Params:     s.params,
		Error:      s.err,
		EventCount: s.seq,
		StartedAt:  s.startedAt,
		StoppedAt:  s.stoppedAt,
	}
}

// Latest returns the most recent top snapshot (nil for traces or before the
// first tick).
func (s *Session) Latest() *Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.latest
}

// Result materializes the session: the latest snapshot for top, or the
// buffered rows run through the profile's processors for trace.
func (s *Session) Result(ctx context.Context) (*Result, error) {
	if s.profile.Kind() == KindTop {
		if latest := s.Latest(); latest != nil {
			return latest, nil
		}
		return nil, fmt.Errorf("session %s: no snapshot yet", s.id)
	}
	var rows []Row
	for _, e := range s.Events() {
		if e.Row != nil {
			rows = append(rows, e.Row)
		}
	}
	return applyProcessors(ctx, s.profile.Processors, &Result{Profile: s.profile.Name, Rows: rows})
}

// Stop transitions an active session to stopped and cancels its run context.
// A later markDone never downgrades the stopped state.
func (s *Session) Stop() {
	s.mu.Lock()
	if s.state.Terminal() {
		s.mu.Unlock()
		return
	}
	s.transitionLocked(SessionStopped, "")
	cancel := s.cancel
	info, hook := s.snapshotLocked(), s.onTransition
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if hook != nil {
		hook(info)
	}
}

// Abort forces an active session into the failed state (e.g. when its durable
// event log cannot be written), cancelling the run and closing subscribers.
func (s *Session) Abort(err error) {
	s.mu.Lock()
	if s.state.Terminal() {
		s.mu.Unlock()
		return
	}
	s.transitionLocked(SessionFailed, err.Error())
	for id, ch := range s.subscribers {
		delete(s.subscribers, id)
		close(ch)
	}
	cancel := s.cancel
	info, hook := s.snapshotLocked(), s.onTransition
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if hook != nil {
		hook(info)
	}
}

func (s *Session) setCancel(cancel stdcontext.CancelFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancel = cancel
}

func (s *Session) setLatest(r *Result) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.latest = r
}

func (s *Session) markRunning() {
	s.mu.Lock()
	if s.state != SessionStarting {
		s.mu.Unlock()
		return
	}
	s.transitionLocked(SessionRunning, "")
	info, hook := s.snapshotLocked(), s.onTransition
	s.mu.Unlock()

	if hook != nil {
		hook(info)
	}
}

// markDone finalizes the session: completed on nil error, failed otherwise.
// It preserves an earlier stopped state and closes all subscriber channels.
func (s *Session) markDone(err error) {
	s.mu.Lock()
	changed := !s.state.Terminal()
	if changed {
		if err != nil {
			s.transitionLocked(SessionFailed, err.Error())
		} else {
			s.transitionLocked(SessionCompleted, "")
		}
	}
	if s.stoppedAt == nil {
		now := time.Now()
		s.stoppedAt = &now
	}
	for id, ch := range s.subscribers {
		delete(s.subscribers, id)
		close(ch)
	}
	info, hook := s.snapshotLocked(), s.onTransition
	s.mu.Unlock()

	if changed && hook != nil {
		hook(info)
	}
}

func (s *Session) transitionLocked(state SessionState, errMsg string) {
	s.state = state
	if errMsg != "" {
		s.err = errMsg
	}
	if state.Terminal() && s.stoppedAt == nil {
		now := time.Now()
		s.stoppedAt = &now
	}
}
