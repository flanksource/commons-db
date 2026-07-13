package query

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrMaxSessions is returned by Add when the active-session cap is reached.
var ErrMaxSessions = errors.New("max sessions reached")

// RegistryOptions bounds a SessionRegistry. Zero values take the defaults;
// profile-declared limits are clamped to these server caps, never raised.
type RegistryOptions struct {
	// MaxSessions caps concurrently active (starting/running) sessions.
	MaxSessions int // default 5

	// MaxDuration caps any session's run duration.
	MaxDuration time.Duration // default 15m

	// MaxEvents caps any session's ring buffer.
	MaxEvents int // default 10000

	// RetainDone is how many terminal sessions stay in memory before the
	// oldest are pruned.
	RetainDone int // default 50

	// OnEvent/OnTransition are installed on every session started through
	// ExecuteStream — the persistence hooks.
	OnEvent      func(Event)
	OnTransition func(SessionInfo)
}

// SessionRegistry tracks live and recently finished sessions in memory.
type SessionRegistry struct {
	mu       sync.Mutex
	sessions map[string]*Session
	order    []string // insertion order, for pruning
	opts     RegistryOptions
}

// NewSessionRegistry creates a registry, applying defaults to zero options.
func NewSessionRegistry(opts RegistryOptions) *SessionRegistry {
	if opts.MaxSessions <= 0 {
		opts.MaxSessions = 5
	}
	if opts.MaxDuration <= 0 {
		opts.MaxDuration = DefaultMaxDuration
	}
	if opts.MaxEvents <= 0 {
		opts.MaxEvents = DefaultMaxEvents
	}
	if opts.RetainDone <= 0 {
		opts.RetainDone = 50
	}
	return &SessionRegistry{sessions: map[string]*Session{}, opts: opts}
}

// Add registers s, failing fast when MaxSessions active sessions already
// exist, and prunes the oldest terminal sessions beyond RetainDone.
func (r *SessionRegistry) Add(s *Session) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	active := 0
	for _, existing := range r.sessions {
		if !existing.Snapshot().State.Terminal() {
			active++
		}
	}
	if !s.Snapshot().State.Terminal() && active >= r.opts.MaxSessions {
		return fmt.Errorf("%w (%d active); stop one first", ErrMaxSessions, active)
	}
	r.sessions[s.ID()] = s
	r.order = append(r.order, s.ID())
	r.pruneLocked()
	return nil
}

// Get returns the session with the given id.
func (r *SessionRegistry) Get(id string) (*Session, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[id]
	return s, ok
}

// List returns snapshots of all tracked sessions, oldest first.
func (r *SessionRegistry) List() []SessionInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]SessionInfo, 0, len(r.sessions))
	for _, id := range r.order {
		if s, ok := r.sessions[id]; ok {
			out = append(out, s.Snapshot())
		}
	}
	return out
}

// StopAll stops every active session (serve shutdown hook).
func (r *SessionRegistry) StopAll() {
	for _, s := range r.snapshotSessions() {
		s.Stop()
	}
}

// ClampDuration lowers d to the server cap when it exceeds it.
func (r *SessionRegistry) ClampDuration(d time.Duration) time.Duration {
	if d <= 0 || d > r.opts.MaxDuration {
		return r.opts.MaxDuration
	}
	return d
}

// ClampEvents lowers n to the server cap when it exceeds it.
func (r *SessionRegistry) ClampEvents(n int) int {
	if n <= 0 || n > r.opts.MaxEvents {
		return r.opts.MaxEvents
	}
	return n
}

func (r *SessionRegistry) snapshotSessions() []*Session {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*Session, 0, len(r.sessions))
	for _, s := range r.sessions {
		out = append(out, s)
	}
	return out
}

// pruneLocked drops the oldest terminal sessions beyond RetainDone.
func (r *SessionRegistry) pruneLocked() {
	terminal := 0
	for _, s := range r.sessions {
		if s.Snapshot().State.Terminal() {
			terminal++
		}
	}
	if terminal <= r.opts.RetainDone {
		return
	}
	kept := make([]string, 0, len(r.order))
	for _, id := range r.order {
		s, ok := r.sessions[id]
		if !ok {
			continue
		}
		if terminal > r.opts.RetainDone && s.Snapshot().State.Terminal() {
			delete(r.sessions, id)
			terminal--
			continue
		}
		kept = append(kept, id)
	}
	r.order = kept
}
