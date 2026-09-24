package query

import (
	stdcontext "context"
	"fmt"
	"sync"
	"time"

	"github.com/flanksource/clicky/formatters"

	"github.com/flanksource/commons-db/context"
)

// SessionState is the lifecycle state of a session.
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
//
// A trace event carries its row twice. Row is the row as the profile's
// pipeline produced it — what an ndjson export and a session's materialized
// result read. ClickyRow is the same row as an interactive table page of the
// profile presents it (RenderClickyPage: the RowPresenter's rich cells, the
// application/json+clicky row shape), so a live table can insert it beside the
// rows of a page it fetched and render them alike.
type Event struct {
	SessionID string                `json:"sessionId"`
	Sequence  int64                 `json:"sequence"`
	Time      time.Time             `json:"time"`
	Row       Row                   `json:"row,omitempty"`
	ClickyRow *formatters.ClickyRow `json:"clickyRow,omitempty"`
	Rows      []Row                 `json:"rows,omitempty"`
	Error     string                `json:"error,omitempty"`
}

// SessionOptions configures NewSession.
type SessionOptions struct {
	ID string

	// Profile is the profile a stream session executes. A session a host
	// drives (Track) names only its profile and has no trace or top spec.
	Profile   Profile
	Kind      ProfileKind
	Role      SessionRole
	Params    map[string]any
	Labels    map[string]string
	Principal string
	Owner     SessionOwner
	RestartOf string

	// MaxEvents caps the in-memory ring buffer (already clamped by the caller).
	MaxEvents int
}

// subscriberBuffer is the per-subscriber channel capacity; Emit never blocks —
// events beyond it are dropped for that subscriber (the ring stays complete).
const subscriberBuffer = 256

// Session is one running (or finished) session: a capped ring buffer of
// events, live subscribers, and a record whose status moves
// starting → running → stopping → {completed|failed|stopped}.
type Session struct {
	profile Profile

	mu          sync.Mutex
	rec         SessionRecord
	ring        []Event
	head, count int
	seq         int64
	subscribers map[int]chan Event
	nextSub     int
	latest      *Result

	run    stdcontext.Context
	cancel stdcontext.CancelFunc

	stopRequested bool
	stopOutcome   SessionState
	onStop        func(reason string)
	onStopCalled  bool
	deadline      *time.Timer
	durationStart time.Time
	stopTimer     *time.Timer
	done          chan struct{}

	sessionPersistence
	sessionView
}

// NewSession creates a session in the starting state. The caller is expected
// to have validated the profile and clamped MaxEvents.
func NewSession(opts SessionOptions) (*Session, error) {
	switch {
	case opts.ID == "":
		return nil, fmt.Errorf("session id is required")
	case opts.Profile.Name == "":
		return nil, fmt.Errorf("session %s: profile name is required", opts.ID)
	case opts.Kind == "":
		return nil, fmt.Errorf("session %s: kind is required", opts.ID)
	case opts.Role != SessionRoleCapture && opts.Role != SessionRoleView:
		return nil, fmt.Errorf("session %s: role %q is neither %q nor %q", opts.ID, opts.Role, SessionRoleCapture, SessionRoleView)
	}
	max := opts.MaxEvents
	if max <= 0 {
		max = DefaultMaxEvents
	}
	now := time.Now()
	return &Session{
		profile: opts.Profile,
		rec: SessionRecord{
			SessionStart: SessionStart{
				SchemaVersion: SessionSchemaVersion,
				ID:            opts.ID,
				Profile:       opts.Profile.Name,
				Kind:          opts.Kind,
				Role:          opts.Role,
				Params:        opts.Params,
				Labels:        opts.Labels,
				Principal:     opts.Principal,
				Owner:         opts.Owner,
				RestartOf:     opts.RestartOf,
				StartedAt:     now,
			},
			SessionStatus: SessionStatus{State: SessionStarting, UpdatedAt: now, HeartbeatAt: now},
		},
		ring:        make([]Event, max),
		subscribers: map[int]chan Event{},
		done:        make(chan struct{}),
	}, nil
}

// ID returns the session's identifier.
func (s *Session) ID() string { return s.rec.ID }

// Context is the context the registry runs the session under. Stop cancels it,
// so a host arming a capture under it stops arming. It is nil for a session no
// registry started.
func (s *Session) Context() stdcontext.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.run
}

// Done is closed once the session is terminal and its final status written.
func (s *Session) Done() <-chan struct{} { return s.done }

// Emit stamps the event with the next sequence, appends it to the ring
// (evicting the oldest at capacity), and fans it out to subscribers without
// blocking. Events emitted after the session is terminal are discarded. A
// capture's event sink receives every event; a sink failure fails the session.
func (s *Session) Emit(e Event) {
	s.mu.Lock()
	if s.rec.State.Terminal() {
		s.mu.Unlock()
		return
	}
	s.seq++
	s.rec.EventCount = s.seq
	e.SessionID = s.rec.ID
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
	s.scheduleProgressFlushLocked()
	sink, storeCtx := s.events, s.storeCtx
	s.mu.Unlock()

	if sink != nil {
		if err := sink.Append(storeCtx, e); err != nil {
			s.Abort(fmt.Errorf("append event %d: %w", e.Sequence, err))
		}
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
	return s.SubscribeFrom(0)
}

// SubscribeFrom is Subscribe for a consumer that already holds every event up
// to and including after — a reconnecting SSE client naming its Last-Event-ID.
// Sequences start at 1, so 0 replays the whole ring and is what Subscribe asks
// for.
//
// A sequence older than the ring's oldest surviving event replays what is left
// rather than failing: the evicted span is unrecoverable whichever way it is
// answered, and the alternative is a client that reconnects into silence.
func (s *Session) SubscribeFrom(after int64) (replay []Event, live <-chan Event, cancel func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	replay = make([]Event, 0, s.count)
	for i := 0; i < s.count; i++ {
		if event := s.ring[(s.head+i)%len(s.ring)]; event.Sequence > after {
			replay = append(replay, event)
		}
	}
	ch := make(chan Event, subscriberBuffer)
	if s.rec.State.Terminal() {
		close(ch)
		return replay, ch, func() {}
	}
	id := s.nextSub
	s.nextSub++
	s.subscribers[id] = ch
	s.disarmIdleLocked()
	return replay, ch, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if ch, ok := s.subscribers[id]; ok {
			delete(s.subscribers, id)
			close(ch)
			if len(s.subscribers) == 0 {
				s.armIdleLocked()
			}
		}
	}
}

// Snapshot returns a copy of the session's record. A live session is
// controllable and written by this process; whether its events are readable
// here is the serving layer's to derive.
func (s *Session) Snapshot() SessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return SessionInfo{SessionRecord: s.rec, Controllable: !s.rec.State.Terminal(), LocalWriter: true}
}

// Latest returns the most recent top snapshot (nil for traces or before the
// first tick).
func (s *Session) Latest() *Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.latest
}

// Result materializes a stream session: the latest snapshot for top, or the
// rows already emitted by the trace pipeline. A session a host drives has no
// profile to materialize; its result is its status.
func (s *Session) Result(ctx context.Context) (*Result, error) {
	switch s.profile.Kind() {
	case KindTop:
		if latest := s.Latest(); latest != nil {
			return latest, nil
		}
		return nil, fmt.Errorf("session %s: no snapshot yet", s.rec.ID)
	case KindTrace:
		return MaterializeEvents(ctx, s.profile, s.Events())
	}
	return nil, fmt.Errorf("session %s (%s) runs no profile; read its status result", s.rec.ID, s.rec.Profile)
}

// MaterializeEvents turns a session's event log into a Result: the last
// snapshot for a top profile, or the final streamed trace rows. It also serves
// persisted events after the live session is gone.
func MaterializeEvents(_ context.Context, p Profile, events []Event) (*Result, error) {
	if p.Kind() == KindTop {
		for i := len(events) - 1; i >= 0; i-- {
			if events[i].Rows != nil {
				return &Result{Profile: p.Name, Rows: events[i].Rows}, nil
			}
		}
		return nil, fmt.Errorf("profile %q: no snapshot recorded", p.Name)
	}
	var rows []Row
	for _, e := range events {
		if e.Row != nil {
			rows = append(rows, e.Row)
		}
	}
	return &Result{Profile: p.Name, Rows: rows}, nil
}

func (s *Session) setLatest(r *Result) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.latest = r
}
