package query

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// ManagedStatus is the current externally-owned capture state mirrored into a
// SessionRecord while the capture runs.
type ManagedStatus struct {
	Handle     string
	Events     *EventsRef
	EventCount int64
	Summary    any
}

// ManagedFinish is the terminal state returned by ManagedRun.Stop or Detach.
type ManagedFinish struct {
	ManagedStatus
	Warning string
	Result  any
}

// ManagedRun is an armed capture whose lifecycle SessionRegistry coordinates.
// Implementations need not be safe for concurrent use; ManagedSession
// serializes every call.
type ManagedRun interface {
	Sample(context.Context) (any, error)
	Status() ManagedStatus
	Stop(context.Context) (ManagedFinish, error)
	Detach(context.Context) (ManagedFinish, error)
	Finished() <-chan struct{}
}

// ArmFunc arms a capture under the tracked session's context. It must return
// only once the source is observable, so a running session never precedes its
// capture.
type ArmFunc func(context.Context) (ManagedRun, error)

// ManageOptions configures a host-driven capture without changing Track's
// existing API or semantics.
type ManageOptions struct {
	Track TrackOptions

	// PollEvery publishes progress at this cadence. A zero duration disables
	// background polling.
	PollEvery time.Duration
	// PullOnPoll samples the run before publishing each background update.
	PullOnPoll bool
	// SampleTimeout bounds one background sample. Zero leaves it bounded only
	// by the managed session's operation context.
	SampleTimeout time.Duration
	// StopTimeout bounds Stop and Detach. Zero uses the registry's timeout for
	// the tracked profile.
	StopTimeout time.Duration
	// Recoverable permits Detach to leave the external source alive for a
	// successor session.
	Recoverable bool
}

// ManagedSession is an externally-owned capture coordinated with one tracked
// session.
type ManagedSession struct {
	registry    *SessionRegistry
	session     *Session
	run         ManagedRun
	options     ManageOptions
	operation   context.Context
	stopTimeout time.Duration

	callMu sync.Mutex
	closed bool

	endOnce sync.Once
	result  any
	err     error
}

// Manage tracks and arms one externally-owned capture. Existing callers that
// drive Track themselves are unaffected.
func (r *SessionRegistry) Manage(ctx context.Context, options ManageOptions, arm ArmFunc) (*ManagedSession, error) {
	switch {
	case arm == nil:
		return nil, errors.New("manage session: arm function is required")
	case options.PollEvery < 0:
		return nil, fmt.Errorf("manage session %q: poll interval %s must not be negative", options.Track.Profile, options.PollEvery)
	case options.StopTimeout < 0:
		return nil, fmt.Errorf("manage session %q: stop timeout %s must not be negative", options.Track.Profile, options.StopTimeout)
	case options.SampleTimeout < 0:
		return nil, fmt.Errorf("manage session %q: sample timeout %s must not be negative", options.Track.Profile, options.SampleTimeout)
	}

	timeout := options.StopTimeout
	if timeout == 0 {
		var err error
		timeout, err = r.stopTimeout(options.Track.Profile)
		if err != nil {
			return nil, err
		}
	}
	session, err := r.Track(ctx, options.Track)
	if err != nil {
		return nil, err
	}
	run, err := arm(session.Context())
	if err != nil {
		session.Finish(FinishUpdate{Err: err})
		return nil, err
	}
	if run == nil {
		err = fmt.Errorf("manage session %q: arm returned no run", options.Track.Profile)
		session.Finish(FinishUpdate{Err: err})
		return nil, err
	}

	managed := &ManagedSession{
		registry: r, session: session, run: run, options: options,
		operation: context.WithoutCancel(ctx), stopTimeout: timeout,
	}
	r.registerManaged(managed)
	session.OnStop(managed.stopForSession)
	status := run.Status()
	if err := session.Running(RunningUpdate{Handle: status.Handle, Events: status.Events}); err != nil {
		_, stopErr := managed.Stop(context.Background())
		r.forgetManaged(managed)
		return nil, errors.Join(err, stopErr)
	}
	managed.publish(status)
	go managed.loop()
	return managed, nil
}

func (r *SessionRegistry) registerManaged(managed *ManagedSession) {
	r.mu.Lock()
	r.managed[managed.session.ID()] = managed
	r.mu.Unlock()
}

func (r *SessionRegistry) forgetManaged(managed *ManagedSession) {
	r.mu.Lock()
	if r.managed[managed.session.ID()] == managed {
		delete(r.managed, managed.session.ID())
	}
	r.mu.Unlock()
}

// Session is the durable session recording this capture.
func (m *ManagedSession) Session() *Session { return m.session }

// Context scopes work observed by the managed capture.
func (m *ManagedSession) Context() context.Context { return m.session.Context() }

// Status returns the external run's current status under the same serialization
// used by Sample, Stop and background polling.
func (m *ManagedSession) Status() ManagedStatus {
	m.callMu.Lock()
	defer m.callMu.Unlock()
	return m.run.Status()
}

// Sample pulls one capture window and publishes the resulting progress.
func (m *ManagedSession) Sample(ctx context.Context) (any, error) {
	m.callMu.Lock()
	defer m.callMu.Unlock()
	if m.closed {
		return nil, fmt.Errorf("managed session %s: %w", m.session.ID(), ErrSessionEnded)
	}
	result, err := m.run.Sample(ctx)
	m.publish(m.run.Status())
	return result, err
}

// ReportProgress publishes the run's current status without sampling it.
func (m *ManagedSession) ReportProgress() {
	m.callMu.Lock()
	defer m.callMu.Unlock()
	if !m.closed {
		m.publish(m.run.Status())
	}
}

func (m *ManagedSession) publish(status ManagedStatus) {
	m.session.Progress(ProgressUpdate{EventCount: status.EventCount, Events: status.Events, Summary: status.Summary})
}

func (m *ManagedSession) loop() {
	defer m.registry.forgetManaged(m)
	var ticker *time.Ticker
	var tick <-chan time.Time
	if m.options.PollEvery > 0 {
		ticker = time.NewTicker(m.options.PollEvery)
		defer ticker.Stop()
		tick = ticker.C
	}
	for {
		select {
		case <-m.session.Done():
			return
		case <-m.run.Finished():
			_, _ = m.Stop(m.operation)
			return
		case <-tick:
			if m.options.PullOnPoll {
				sampleCtx, cancel := m.sampleContext()
				_, err := m.Sample(sampleCtx)
				cancel()
				if err != nil && !errors.Is(err, ErrSessionEnded) {
					_, _ = m.Abort(fmt.Errorf("managed sample: %w", err))
					return
				}
			} else {
				m.ReportProgress()
			}
		}
	}
}

func (m *ManagedSession) sampleContext() (context.Context, context.CancelFunc) {
	if m.options.SampleTimeout > 0 {
		return context.WithTimeout(m.operation, m.options.SampleTimeout)
	}
	return context.WithCancel(m.operation)
}

// Stop ends the external capture once and finishes its session.
func (m *ManagedSession) Stop(ctx context.Context) (any, error) {
	m.endOnce.Do(func() { m.end(ctx, false, "", nil) })
	return m.result, m.err
}

// Abort stops the capture and fails its session with cause.
func (m *ManagedSession) Abort(cause error) (any, error) {
	m.endOnce.Do(func() { m.end(m.operation, false, "", cause) })
	return m.result, m.err
}

// Detach stops local ownership of a recoverable capture without stopping its
// external source or sealing its record stream.
func (m *ManagedSession) Detach(ctx context.Context, reason string) error {
	if !m.options.Recoverable {
		return fmt.Errorf("managed session %s is not recoverable", m.session.ID())
	}
	if strings.TrimSpace(reason) == "" {
		return fmt.Errorf("managed session %s: detach reason is required", m.session.ID())
	}
	m.endOnce.Do(func() { m.end(ctx, true, reason, nil) })
	return m.err
}

func (m *ManagedSession) end(request context.Context, detach bool, reason string, cause error) {
	defer m.registry.forgetManaged(m)
	ctx, cancel := context.WithTimeout(m.operation, m.stopTimeout)
	stopCancel := context.AfterFunc(request, cancel)
	defer stopCancel()
	defer cancel()

	m.callMu.Lock()
	m.closed = true
	var finish ManagedFinish
	if detach {
		finish, m.err = m.run.Detach(ctx)
	} else {
		finish, m.err = m.run.Stop(ctx)
	}
	status := m.run.Status()
	m.callMu.Unlock()
	finish = mergeManagedFinish(finish, status)
	m.result = finish.Result
	if cause != nil {
		if m.err == nil {
			m.err = cause
		} else {
			m.err = errors.Join(cause, m.err)
		}
	}
	m.publish(finish.ManagedStatus)
	update := FinishUpdate{Err: m.err, Warning: finish.Warning, Events: finish.Events, Summary: finish.Summary, Result: finish.Result}
	if detach && m.err == nil {
		m.session.Interrupt(reason, update)
		return
	}
	m.session.Finish(update)
}

func mergeManagedFinish(finish ManagedFinish, status ManagedStatus) ManagedFinish {
	if finish.Handle == "" {
		finish.Handle = status.Handle
	}
	if finish.Events == nil {
		finish.Events = status.Events
	}
	if finish.EventCount == 0 {
		finish.EventCount = status.EventCount
	}
	if finish.Summary == nil {
		finish.Summary = status.Summary
	}
	return finish
}

func (m *ManagedSession) stopForSession(string) {
	_, _ = m.Stop(m.operation)
}

// ShutdownOptions controls how SessionRegistry.Shutdown treats managed
// captures. StopAll retains its existing stop-everything behavior.
type ShutdownOptions struct {
	DetachRecoverable bool
	Reason            string
}

// Shutdown ends every active session and waits for its final status. When
// requested, recoverable managed captures detach their local owner while
// their external source and durable stream remain available to a successor.
func (r *SessionRegistry) Shutdown(ctx context.Context, options ShutdownOptions) error {
	reason := strings.TrimSpace(options.Reason)
	if reason == "" {
		reason = StopReasonShutdown
	}
	sessions, managed := r.shutdownSnapshot()
	var shutdownErr error
	for _, session := range sessions {
		capture := managed[session.ID()]
		if options.DetachRecoverable && capture != nil && capture.options.Recoverable {
			if err := capture.Detach(ctx, reason); err != nil {
				shutdownErr = errors.Join(shutdownErr, fmt.Errorf("detach session %s: %w", session.ID(), err))
			}
			continue
		}
		session.Stop(reason)
	}
	for _, session := range sessions {
		select {
		case <-session.Done():
		case <-ctx.Done():
			return errors.Join(shutdownErr, fmt.Errorf("shutdown sessions: session %s is still %s: %w", session.ID(), session.Snapshot().State, ctx.Err()))
		}
	}
	return shutdownErr
}

func (r *SessionRegistry) shutdownSnapshot() ([]*Session, map[string]*ManagedSession) {
	r.mu.Lock()
	defer r.mu.Unlock()
	sessions := make([]*Session, 0, len(r.sessions))
	for _, session := range r.sessions {
		if !session.Snapshot().State.Terminal() {
			sessions = append(sessions, session)
		}
	}
	managed := make(map[string]*ManagedSession, len(r.managed))
	for id, session := range r.managed {
		managed[id] = session
	}
	return sessions, managed
}
