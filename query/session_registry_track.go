package query

import (
	stdcontext "context"
	"encoding/json"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/flanksource/commons-db/context"
)

// maxSessionParamsBytes caps a session's params, which a restart replays.
const maxSessionParamsBytes = 256 << 10

// TrackOptions describes a capture a host runs itself.
type TrackOptions struct {
	// Profile names the capture, e.g. "trace-capture/jvm_trace"; it keys
	// authorization, StopTimeout and Restarters.
	Profile   string
	Kind      ProfileKind
	Params    map[string]any
	Labels    map[string]string
	Principal string
	RestartOf string

	// StopAt is the deadline, clamped to MaxDuration; nil runs for MaxDuration.
	StopAt *time.Time
}

// RestartOverrides changes what a restart replays.
type RestartOverrides struct {
	// Params are merged over the previous session's params.
	Params map[string]any
	// Duration replaces the previous session's requested duration; zero replays
	// its params' durationMs.
	Duration time.Duration
	// Principal is who restarts; empty keeps the previous principal.
	Principal string
}

// Track registers a capture session a host drives, in the starting state, and
// returns it with its context installed: the host arms under
// Session.Context(), which a stop cancels. The session's store writes and the
// host's stop keep ctx's values but not its cancellation, so a capture started
// by a request outlives it and still routes to the request's environment.
//
// The host then reports Running, Progress and Finish, and installs OnStop. The
// deadline stops the session as completed.
func (r *SessionRegistry) Track(ctx stdcontext.Context, opts TrackOptions) (*Session, error) {
	if opts.Profile == "" || opts.Kind == "" {
		return nil, fmt.Errorf("track session: profile %q and kind %q are both required", opts.Profile, opts.Kind)
	}
	if params, err := json.Marshal(opts.Params); err != nil {
		return nil, fmt.Errorf("track session %q: params: %w", opts.Profile, err)
	} else if len(params) > maxSessionParamsBytes {
		return nil, fmt.Errorf("track session %q: params are %d bytes, over the %d byte cap", opts.Profile, len(params), maxSessionParamsBytes)
	}
	session, err := NewSession(SessionOptions{
		ID:        uuid.NewString(),
		Profile:   Profile{Name: opts.Profile},
		Kind:      opts.Kind,
		Role:      SessionRoleCapture,
		Params:    opts.Params,
		Labels:    opts.Labels,
		Principal: opts.Principal,
		Owner:     r.opts.Owner,
		RestartOf: opts.RestartOf,
		MaxEvents: r.opts.MaxEvents,
	})
	if err != nil {
		return nil, err
	}
	var duration time.Duration
	if opts.StopAt != nil {
		if duration = time.Until(*opts.StopAt); duration <= 0 {
			return nil, fmt.Errorf("track session %q: stopAt %s is not in the future", opts.Profile, opts.StopAt.Format(time.RFC3339))
		}
	}
	storeCtx := stdcontext.WithoutCancel(ctx)
	run, cancel := stdcontext.WithCancel(storeCtx)
	err = r.admit(session, sessionAttachment{
		run: run, cancel: cancel, storeCtx: storeCtx,
		stopAt: session.rec.StartedAt.Add(r.ClampDuration(duration)),
	})
	if err != nil {
		return nil, err
	}
	return session, nil
}

// streamRun is a stream session admitted with its run context.
type streamRun struct {
	session *Session
	ctx     context.Context
	cancel  stdcontext.CancelFunc
}

// startStream admits a stream session over p. A followed trace is a view and is
// never persisted; anything else is a capture.
func (r *SessionRegistry) startStream(ctx context.Context, p Profile, params map[string]any, maxEvents int, duration time.Duration) (streamRun, error) {
	role := SessionRoleCapture
	if p.Trace != nil && p.Trace.Follow {
		role = SessionRoleView
	}
	session, err := NewSession(SessionOptions{
		ID: uuid.NewString(), Profile: p, Kind: p.Kind(), Role: role,
		Params: params, Owner: r.opts.Owner, MaxEvents: maxEvents,
	})
	if err != nil {
		return streamRun{}, err
	}
	run, cancel := ctx.WithCancel()
	err = r.admit(session, sessionAttachment{
		run: run, cancel: cancel, storeCtx: stdcontext.WithoutCancel(ctx),
		stopAt: session.rec.StartedAt.Add(r.ClampDuration(duration)),
	})
	if err != nil {
		return streamRun{}, err
	}
	return streamRun{session: session, ctx: run, cancel: cancel}, nil
}

// Extend moves a live session's deadline to stopAt, clamped to MaxDuration
// from the session's start, and returns the deadline it settled on.
func (r *SessionRegistry) Extend(id string, stopAt time.Time) (time.Time, error) {
	session, ok := r.Get(id)
	if !ok {
		return time.Time{}, fmt.Errorf("extend session %s: %w", id, ErrSessionNotLive)
	}
	return session.extend(stopAt)
}

// Restart starts a new session replaying an ended one through the RestartFunc
// registered for its profile. The ended record is read, never written: lineage
// is the new record's RestartOf.
func (r *SessionRegistry) Restart(ctx stdcontext.Context, id string, overrides RestartOverrides) (*Session, error) {
	previous, err := r.lookupRecord(ctx, id)
	if err != nil {
		return nil, err
	}
	if !previous.State.Terminal() {
		return nil, fmt.Errorf("restart session %s: it is %s; only an ended session restarts", id, previous.State)
	}
	restart, err := r.restarter(previous.Profile)
	if err != nil {
		return nil, fmt.Errorf("restart session %s: %w", id, err)
	}
	if r.opts.Restartable != nil && !r.opts.Restartable(previous) {
		return nil, fmt.Errorf("restart session %s (%s): %w", id, previous.Profile, ErrNotRestartable)
	}
	opts := TrackOptions{
		Profile:   previous.Profile,
		Kind:      previous.Kind,
		Params:    maps.Clone(previous.Params),
		Labels:    maps.Clone(previous.Labels),
		Principal: previous.Principal,
		RestartOf: previous.ID,
	}
	if opts.Params == nil && len(overrides.Params) > 0 {
		opts.Params = map[string]any{}
	}
	maps.Copy(opts.Params, overrides.Params)
	if overrides.Principal != "" {
		opts.Principal = overrides.Principal
	}
	if err := restartDuration(&opts, overrides.Duration); err != nil {
		return nil, fmt.Errorf("restart session %s (%s): %w", id, previous.Profile, err)
	}

	session, err := restart(ctx, previous, opts)
	if err != nil {
		return nil, fmt.Errorf("restart session %s (%s): %w", id, previous.Profile, err)
	}
	if session == nil {
		return nil, fmt.Errorf("restart session %s (%s): restarter returned no session", id, previous.Profile)
	}
	if got := session.Snapshot().RestartOf; got != id {
		session.Stop(fmt.Sprintf("restart of %s started with restartOf %q", id, got))
		return nil, fmt.Errorf("restart session %s (%s): restarter started session %s with restartOf %q; it was stopped", id, previous.Profile, session.ID(), got)
	}
	return session, nil
}

// restartDurationParam is the param a capture records its requested run
// duration in, in milliseconds.
const restartDurationParam = "durationMs"

// restartDuration sets opts' deadline: the override when one is given, which
// also becomes the new session's durationMs, otherwise the durationMs the
// replayed params ask for — the duration first requested, not the run an
// extension lengthened. Params without durationMs run for MaxDuration.
func restartDuration(opts *TrackOptions, override time.Duration) error {
	duration := override
	if override > 0 {
		if opts.Params == nil {
			opts.Params = map[string]any{}
		}
		opts.Params[restartDurationParam] = float64(override.Milliseconds())
	} else if raw, ok := opts.Params[restartDurationParam]; ok {
		ms, err := durationMillis(raw)
		if err != nil {
			return fmt.Errorf("params.%s: %w", restartDurationParam, err)
		}
		duration = time.Duration(ms * float64(time.Millisecond))
	}
	if duration > 0 {
		stopAt := time.Now().Add(duration)
		opts.StopAt = &stopAt
	}
	return nil
}

// durationMillis reads a positive millisecond count as a record's params hold
// it: a JSON number, or a Go number before a round trip.
func durationMillis(raw any) (float64, error) {
	var ms float64
	switch value := raw.(type) {
	case float64:
		ms = value
	case int:
		ms = float64(value)
	case int64:
		ms = float64(value)
	case json.Number:
		parsed, err := value.Float64()
		if err != nil {
			return 0, fmt.Errorf("%q is not a number: %w", value, err)
		}
		ms = parsed
	default:
		return 0, fmt.Errorf("%v (%T) is not a number of milliseconds", raw, raw)
	}
	if ms <= 0 {
		return 0, fmt.Errorf("%v is not a positive number of milliseconds", raw)
	}
	return ms, nil
}

func (r *SessionRegistry) lookupRecord(ctx stdcontext.Context, id string) (SessionRecord, error) {
	if session, ok := r.Get(id); ok {
		return session.record(), nil
	}
	if r.opts.Store == nil {
		return SessionRecord{}, fmt.Errorf("session %s: %w and no session store is configured", id, ErrSessionNotLive)
	}
	rec, found, err := r.opts.Store.Get(ctx, id)
	if err != nil {
		return SessionRecord{}, fmt.Errorf("get session %s: %w", id, err)
	}
	if !found {
		return SessionRecord{}, fmt.Errorf("session %s not found", id)
	}
	return rec, nil
}

// Restartable reports whether rec could be restarted here once it has ended:
// RegistryOptions.Restartable decides when set, otherwise a restarter matching
// its profile does.
func (r *SessionRegistry) Restartable(rec SessionRecord) bool {
	if _, err := r.restarter(rec.Profile); err != nil {
		return false
	}
	return r.opts.Restartable == nil || r.opts.Restartable(rec)
}

// restarter resolves the RestartFunc with the longest prefix of profile.
func (r *SessionRegistry) restarter(profile string) (RestartFunc, error) {
	var best string
	var found RestartFunc
	for prefix, fn := range r.opts.Restarters {
		if strings.HasPrefix(profile, prefix) && (found == nil || len(prefix) > len(best)) {
			best, found = prefix, fn
		}
	}
	if found == nil {
		return nil, fmt.Errorf("profile %q has no restarter", profile)
	}
	return found, nil
}
