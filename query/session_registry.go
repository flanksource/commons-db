package query

import (
	stdcontext "context"
	"errors"
	"fmt"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

var (
	// ErrMaxSessions is returned by Add when the active-capture cap is reached.
	ErrMaxSessions = errors.New("max sessions reached")

	// ErrMaxViews is returned by Add when the active-view cap is reached. It
	// wraps ErrMaxSessions, so a transport answering that answers this too.
	ErrMaxViews = fmt.Errorf("max views reached: %w", ErrMaxSessions)

	// ErrPrepareRead wraps a RegistryOptions.BeforeRead failure, so a
	// transport can answer it by its own cause rather than as a malformed
	// session request.
	ErrPrepareRead = errors.New("prepare read failed")

	// ErrSessionNotLive is returned for a control call on a session this
	// registry does not hold.
	ErrSessionNotLive = errors.New("session is not live in this process")

	// ErrNotRestartable is returned by Restart for a record the registry's
	// Restartable refuses.
	ErrNotRestartable = errors.New("session is not restartable here")
)

// Registry defaults for zero RegistryOptions.
const (
	DefaultHeartbeatEvery = 30 * time.Second
	DefaultStaleAfter     = 2 * time.Minute
	DefaultViewGrace      = 15 * time.Second
	DefaultMaxViews       = 50
	DefaultProgressEvery  = 5 * time.Second
	DefaultStopTimeout    = 30 * time.Second
)

// processBoot distinguishes this process from an earlier one on the same host.
var processBoot = uuid.NewString()

// RestartFunc re-arms an ended session through its host. opts carries the
// previous session's profile, kind, labels and params with the overrides
// applied, and RestartOf naming it; the host must start the new session with
// Track(ctx, opts).
type RestartFunc func(ctx stdcontext.Context, previous SessionRecord, opts TrackOptions) (*Session, error)

// RegistryOptions bounds a SessionRegistry. Zero values take the defaults;
// profile-declared limits are clamped to these server caps, never raised.
type RegistryOptions struct {
	// MaxSessions caps concurrently active capture sessions. Views never count
	// against it, so a client re-opening live views cannot starve captures.
	MaxSessions int // default 5

	// MaxViews caps concurrently active view sessions.
	MaxViews int // default DefaultMaxViews

	// MaxDuration caps any session's run duration.
	MaxDuration time.Duration // default 15m

	// MaxEvents caps any session's ring buffer.
	MaxEvents int // default 10000

	// RetainDone is how many terminal sessions stay in memory before the
	// oldest are pruned.
	RetainDone int // default 50

	// Store persists capture sessions. Views are never persisted. Nil keeps
	// sessions in memory only.
	Store SessionStore

	// Events receives every event a capture stream session emits.
	Events EventSink

	// Owner identifies this process on records it writes. Empty fields take
	// the host name, the process id and a per-process boot id.
	Owner SessionOwner

	// HeartbeatEvery is how often active sessions' heartbeats are written and
	// status writes the store refused are retried.
	HeartbeatEvery time.Duration // default 30s

	// StaleAfter is how old an active record's heartbeat must be before Sweep
	// interrupts it.
	StaleAfter time.Duration // default 2m

	// ViewGrace is how long a view session may have no event subscriber before
	// it is reaped — stopped with StopReasonReapedIdle — by a timer armed when
	// its last subscriber leaves, or at admission before the first arrives. It
	// is the only bound on a view a client abandoned without stopping it, which
	// a page reload, an HMR restart and a closed tab all do.
	ViewGrace time.Duration // default 15s

	// ProgressEvery bounds how often progress alone writes a status.
	ProgressEvery time.Duration // default 5s

	// StopTimeout bounds a profile's stop; a stop that overruns it fails the
	// session. Nil gives every profile DefaultStopTimeout.
	StopTimeout func(profile string) time.Duration

	// Restarters re-arm ended sessions, keyed by profile prefix; the longest
	// matching prefix wins.
	Restarters map[string]RestartFunc

	// Restartable decides whether an ended record may be restarted here. Nil
	// allows any record whose profile a Restarters prefix matches.
	Restartable func(SessionRecord) bool

	// BeforeRead, when set, prepares a profile's data before a session reads
	// it: once as the session starts, and again before every later sample a
	// top session takes, so each sample reads data as current as a one-off
	// execution would. It gets the parameters the caller supplied, unresolved.
	// The returned function releases the prepared read after that read ends. A
	// failure refuses the start, or fails the session at that sample.
	BeforeRead func(ctx stdcontext.Context, p Profile, params map[string]any) (release func(), err error)
}

// prepareRead runs BeforeRead, if one is set, and always returns a safe,
// idempotent cleanup.
func (r *SessionRegistry) prepareRead(ctx stdcontext.Context, p Profile, params map[string]any) (func(), error) {
	if r.opts.BeforeRead == nil {
		return func() {}, nil
	}
	release, err := r.opts.BeforeRead(ctx, p, params)
	if release == nil {
		release = func() {}
	} else {
		release = sync.OnceFunc(release)
	}
	if err != nil {
		release()
		return func() {}, fmt.Errorf("profile %q: %w: %w", p.Name, ErrPrepareRead, err)
	}
	return release, nil
}

// SessionRegistry tracks live and recently finished sessions in memory, and
// owns the status writes of the capture sessions it persists.
type SessionRegistry struct {
	mu           sync.Mutex
	sessions     map[string]*Session
	managed      map[string]*ManagedSession
	order        []string // insertion order, for pruning
	opts         RegistryOptions
	heartbeating bool

	reapedViews     atomic.Int64
	persistFailures atomic.Int64
}

// NewSessionRegistry creates a registry, applying defaults to zero options.
func NewSessionRegistry(opts RegistryOptions) *SessionRegistry {
	opts.MaxSessions = defaultInt(opts.MaxSessions, 5)
	opts.MaxViews = defaultInt(opts.MaxViews, DefaultMaxViews)
	opts.MaxEvents = defaultInt(opts.MaxEvents, DefaultMaxEvents)
	opts.RetainDone = defaultInt(opts.RetainDone, 50)
	opts.MaxDuration = defaultDuration(opts.MaxDuration, DefaultMaxDuration)
	opts.HeartbeatEvery = defaultDuration(opts.HeartbeatEvery, DefaultHeartbeatEvery)
	opts.StaleAfter = defaultDuration(opts.StaleAfter, DefaultStaleAfter)
	opts.ViewGrace = defaultDuration(opts.ViewGrace, DefaultViewGrace)
	opts.ProgressEvery = defaultDuration(opts.ProgressEvery, DefaultProgressEvery)
	opts.Owner = processOwner(opts.Owner)
	return &SessionRegistry{sessions: map[string]*Session{}, managed: map[string]*ManagedSession{}, opts: opts}
}

func defaultInt(v, fallback int) int {
	if v <= 0 {
		return fallback
	}
	return v
}

func defaultDuration(v, fallback time.Duration) time.Duration {
	if v <= 0 {
		return fallback
	}
	return v
}

func processOwner(owner SessionOwner) SessionOwner {
	if owner.Host == "" {
		host, err := os.Hostname()
		if err != nil {
			panic(fmt.Sprintf("query: session owner host: %v", err))
		}
		owner.Host = host
	}
	if owner.PID == 0 {
		owner.PID = os.Getpid()
	}
	if owner.Boot == "" {
		owner.Boot = processBoot
	}
	return owner
}

// Owner is the owner this registry writes on its records.
func (r *SessionRegistry) Owner() SessionOwner { return r.opts.Owner }

// StaleAfter is how old an active record's heartbeat is before its owner
// counts as gone.
func (r *SessionRegistry) StaleAfter() time.Duration { return r.opts.StaleAfter }

// Add registers s, failing fast when its role's cap — MaxViews for a view,
// MaxSessions for a capture — is already reached, and prunes the oldest
// terminal sessions beyond RetainDone.
func (r *SessionRegistry) Add(s *Session) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if info := s.Snapshot(); !info.State.Terminal() {
		limit, refusal := r.opts.MaxSessions, ErrMaxSessions
		if info.Role == SessionRoleView {
			limit, refusal = r.opts.MaxViews, ErrMaxViews
		}
		if active := r.activeLocked(info.Role); active >= limit {
			return fmt.Errorf("%w (%d active); stop one first", refusal, active)
		}
	}
	r.sessions[s.ID()] = s
	r.order = append(r.order, s.ID())
	r.pruneLocked()
	r.startHeartbeatLocked()
	return nil
}

// startHeartbeatLocked starts the heartbeat loop unless it is running.
// Registry.mu must be held.
func (r *SessionRegistry) startHeartbeatLocked() {
	if !r.heartbeating {
		r.heartbeating = true
		go r.heartbeatLoop()
	}
}

// persistFailed counts a refused status write and makes sure a heartbeat will
// retry it, even when the session it belongs to has already ended.
func (r *SessionRegistry) persistFailed() {
	r.persistFailures.Add(1)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.startHeartbeatLocked()
}

// admit attaches the registry's bounds and store to s, registers it, begins its
// record, and arms its deadline. On failure s is cancelled and not registered.
func (r *SessionRegistry) admit(s *Session, a sessionAttachment) error {
	timeout, err := r.stopTimeout(s.rec.Profile)
	if err != nil {
		a.cancel()
		return err
	}
	a.stopTimeout, a.progressEvery, a.maxDuration = timeout, r.opts.ProgressEvery, r.opts.MaxDuration
	a.persistFailed = r.persistFailed
	if s.rec.Role == SessionRoleCapture {
		a.store, a.events = r.opts.Store, r.opts.Events
	} else {
		a.viewGrace, a.viewReaped = r.opts.ViewGrace, func() { r.reapedViews.Add(1) }
	}
	s.attach(a)

	if err := r.register(s); err != nil {
		if errors.Is(err, ErrMaxSessions) {
			a.cancel()
			return err
		}
		r.remove(s.ID())
		s.detachStore()
		s.Abort(err)
		return err
	}
	s.mu.Lock()
	s.scheduleDeadlineLocked(a.stopAt)
	s.armIdleLocked()
	s.mu.Unlock()
	return nil
}

// register adds s and begins its record under the session's persist lock, so
// no status write — a heartbeat, a stop by id — reaches the store before the
// record exists.
func (r *SessionRegistry) register(s *Session) error {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	if err := r.Add(s); err != nil {
		return err
	}
	if s.store == nil {
		return nil
	}
	if err := s.store.Begin(s.storeCtx, s.record()); err != nil {
		return fmt.Errorf("session %s (%s): begin record: %w", s.ID(), s.rec.Profile, err)
	}
	return nil
}

func (r *SessionRegistry) stopTimeout(profile string) (time.Duration, error) {
	if r.opts.StopTimeout == nil {
		return DefaultStopTimeout, nil
	}
	timeout := r.opts.StopTimeout(profile)
	if timeout <= 0 {
		return 0, fmt.Errorf("stop timeout for profile %q is %s; it must be positive", profile, timeout)
	}
	return timeout, nil
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

// StopAll stops every active session and waits until each has finished: its
// run torn down, its final events flushed and its status written. Hosts close
// followers and record stores only after it returns.
func (r *SessionRegistry) StopAll(ctx stdcontext.Context) error {
	sessions := r.snapshotSessions()
	for _, s := range sessions {
		s.Stop(StopReasonShutdown)
	}
	for _, s := range sessions {
		select {
		case <-s.Done():
		case <-ctx.Done():
			return fmt.Errorf("stop all sessions: session %s is still %s: %w", s.ID(), s.Snapshot().State, ctx.Err())
		}
	}
	return nil
}

// ActiveViews counts view sessions that have not ended.
func (r *SessionRegistry) ActiveViews() int {
	count := 0
	for _, s := range r.snapshotSessions() {
		if info := s.Snapshot(); info.Role == SessionRoleView && !info.State.Terminal() {
			count++
		}
	}
	return count
}

// ReapedViews counts view sessions stopped for having had no event subscriber
// for ViewGrace.
func (r *SessionRegistry) ReapedViews() int64 { return r.reapedViews.Load() }

// PersistFailures counts status writes the store rejected.
func (r *SessionRegistry) PersistFailures() int64 { return r.persistFailures.Load() }

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

func (r *SessionRegistry) remove(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.sessions, id)
}

// activeLocked counts the sessions that have not ended, of roles when any are
// named. Registry.mu must be held.
func (r *SessionRegistry) activeLocked(roles ...SessionRole) int {
	active := 0
	for _, s := range r.sessions {
		info := s.Snapshot()
		if !info.State.Terminal() && (len(roles) == 0 || slices.Contains(roles, info.Role)) {
			active++
		}
	}
	return active
}

// repairsPendingLocked reports whether an ended session's status never reached
// the store. Registry.mu must be held.
func (r *SessionRegistry) repairsPendingLocked() bool {
	for _, s := range r.sessions {
		if s.statusUnpersisted() {
			return true
		}
	}
	return false
}

// pruneLocked drops the oldest terminal sessions beyond RetainDone, keeping
// any whose final status the store has not accepted yet.
func (r *SessionRegistry) pruneLocked() {
	terminal := len(r.sessions) - r.activeLocked()
	if terminal <= r.opts.RetainDone {
		return
	}
	kept := make([]string, 0, len(r.order))
	for _, id := range r.order {
		s, ok := r.sessions[id]
		if !ok {
			continue
		}
		if terminal > r.opts.RetainDone && s.Snapshot().State.Terminal() && !s.statusUnpersisted() {
			delete(r.sessions, id)
			terminal--
			continue
		}
		kept = append(kept, id)
	}
	r.order = kept
}
