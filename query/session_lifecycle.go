package query

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"
)

// ErrSessionEnded is returned for a lifecycle call on a terminal session.
var ErrSessionEnded = errors.New("session ended")

// Stop reasons the registry writes itself.
const (
	StopReasonDeadline = "duration elapsed"
	StopReasonShutdown = "shutdown"

	// StopReasonReapedIdle prefixes the reason of a view the registry ended for
	// having had no event subscriber for its ViewGrace; the grace follows it. A
	// reaped view is stopped, not completed: nothing about its source ended, the
	// server ended it because nobody was reading.
	StopReasonReapedIdle = "reaped idle"
)

// RunningUpdate reports a capture armed: the external resource it holds, where
// its events are recorded, and the deadline the host settled on.
type RunningUpdate struct {
	Handle string
	Events *EventsRef
	StopAt *time.Time
	// Metadata is what the armed capture reports about itself; see
	// SessionMetadata. Empty leaves the session's metadata as it was.
	Metadata []SessionMetadata
}

// ProgressUpdate is a capture's running totals.
type ProgressUpdate struct {
	// EventCount is the total so far, not an increment.
	EventCount int64
	Events     *EventsRef
	Summary    any
}

// FinishUpdate is how a capture ended. Err must already exclude the
// cancellation a stop caused (see Session.Context): what remains is a failure.
type FinishUpdate struct {
	Err     error
	Warning string
	Events  *EventsRef
	Summary any
	Result  any
}

// Running moves a starting session to running and records what the host armed.
// A session stopped while arming stays stopping: it records the handle so the
// status shows what OnStop tears down.
func (s *Session) Running(u RunningUpdate) error {
	if err := validateSessionMetadata(u.Metadata); err != nil {
		return fmt.Errorf("session %s: %w", s.ID(), err)
	}
	var err error
	s.mutate(func() bool {
		if s.rec.State.Terminal() {
			err = fmt.Errorf("session %s is %s: %w", s.rec.ID, s.rec.State, ErrSessionEnded)
			return false
		}
		if u.Handle != "" {
			s.rec.Handle = u.Handle
		}
		if len(u.Metadata) > 0 {
			s.rec.Metadata = slices.Clone(u.Metadata)
		}
		s.recordEventsLocked(u.Events)
		if u.StopAt != nil {
			s.scheduleDeadlineLocked(*u.StopAt)
		}
		if s.rec.State == SessionStarting {
			s.rec.State = SessionRunning
		}
		return true
	})
	return err
}

// Progress records a capture's totals. The status is written at most once per
// the registry's ProgressEvery; a change inside that interval is written when
// it ends.
func (s *Session) Progress(u ProgressUpdate) {
	var summary json.RawMessage
	var warning string
	if u.Summary != nil {
		summary, warning = encodeStatusPayload("summary", u.Summary)
	}
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	s.mu.Lock()
	if s.rec.State.Terminal() {
		s.mu.Unlock()
		return
	}
	count := eventCount(u.EventCount, u.Events)
	changed := count != s.rec.EventCount ||
		(u.Events != nil && !reflect.DeepEqual(u.Events, s.rec.Events)) ||
		(summary != nil && !bytes.Equal(summary, s.rec.Summary)) ||
		(warning != "" && !strings.Contains(s.rec.Warning, warning))
	s.rec.EventCount = count
	if u.Events != nil {
		s.rec.Events = u.Events
	}
	if summary != nil {
		s.rec.Summary = summary
	}
	s.rec.Warning = joinWarning(s.rec.Warning, warning)
	due := changed && time.Since(s.persistedAt) >= s.progressEvery && s.progressFlush == nil
	if changed && !due {
		s.scheduleProgressFlushLocked()
	}
	status := s.statusLocked()
	s.mu.Unlock()
	if due {
		_ = s.writeStatus(status)
	}
}

// eventCount is the count a progress update records: the count the host gave,
// or, when it gave none, the total of the events ref it reported. A count and a
// ref describe the same rows, so a host that only reports the ref must not
// leave the count behind it.
func eventCount(explicit int64, events *EventsRef) int64 {
	if explicit == 0 && events != nil {
		return events.Total
	}
	return explicit
}

// recordEventsLocked records the events ref a host armed or finished with, and
// the count it holds.
func (s *Session) recordEventsLocked(events *EventsRef) {
	if events != nil {
		s.rec.Events, s.rec.EventCount = events, events.Total
	}
}

// Finish ends the session: stopped after Stop, completed after the deadline or
// a natural end, failed on an error. It writes the final status, closes
// subscribers, and then closes Done. A finished session ignores it.
func (s *Session) Finish(u FinishUpdate) {
	s.finishWithReason(u, "", "")
}

// Abort forces an active session into the failed state (e.g. when its durable
// event log cannot be written), cancelling the run and closing subscribers. It
// tears down what the host armed exactly as Stop does: the OnStop handler runs
// asynchronously, now or as soon as it is installed.
func (s *Session) Abort(err error) {
	s.finishWithReason(FinishUpdate{Err: err}, SessionFailed, "")
}

// Interrupt ends an active session because its owner detached while the
// external capture remains resumable. It is terminal for this owner; recovery
// starts a successor linked through RestartOf.
func (s *Session) Interrupt(reason string, u FinishUpdate) {
	if strings.TrimSpace(reason) == "" {
		panic(fmt.Sprintf("session %s: interrupt reason is empty", s.rec.ID))
	}
	s.finishWithReason(u, SessionInterrupted, reason)
}

func (s *Session) finish(u FinishUpdate, forced SessionState) {
	s.finishWithReason(u, forced, "")
}

func (s *Session) finishWithReason(u FinishUpdate, forced SessionState, reason string) {
	final := encodeFinish(u)

	s.persistMu.Lock()
	s.mu.Lock()
	if s.rec.State.Terminal() {
		s.mu.Unlock()
		s.persistMu.Unlock()
		return
	}
	if reason != "" {
		s.rec.StopReason = reason
	}
	teardown := s.forceTeardownLocked(forced, u.Err)
	s.rec.State = forced
	if forced == "" {
		s.rec.State = s.outcomeLocked(u.Err)
	}
	if u.Err != nil {
		s.rec.Error = u.Err.Error()
	}
	for _, warning := range final.warnings {
		s.rec.Warning = joinWarning(s.rec.Warning, warning)
	}
	s.recordEventsLocked(u.Events)
	if final.summary != nil {
		s.rec.Summary = final.summary
	}
	if final.result != nil {
		s.rec.Result = final.result
	}
	now := time.Now()
	s.rec.StoppedAt = &now
	s.stopTimersLocked()
	for id, ch := range s.subscribers {
		delete(s.subscribers, id)
		close(ch)
	}
	cancel := s.cancel
	sink, storeCtx, id := s.events, s.storeCtx, s.rec.ID
	s.mu.Unlock()

	// Settle the event log before the status write publishes the session as
	// finished, so a reader that finds the terminal record can read every
	// event it counts. The sink is closed outside the session lock: it may go
	// to a database or a cache, and holding the lock across that would stall
	// every Emit sharing it.
	var closeErr error
	if sink != nil {
		closeErr = sink.CloseSession(storeCtx, id)
	}

	s.mu.Lock()
	if closeErr != nil {
		// Events were lost, so the record must not read as a clean end: the
		// count it carries no longer describes a log anyone can replay.
		s.rec.State = SessionFailed
		s.rec.Warning = joinWarning(s.rec.Warning, fmt.Sprintf("close event sink: %v", closeErr))
		if s.rec.Error == "" {
			s.rec.Error = closeErr.Error()
		}
	}
	status := s.statusLocked()
	s.mu.Unlock()
	_ = s.writeStatus(status)
	s.persistMu.Unlock()

	if cancel != nil {
		cancel()
	}
	teardown()
	close(s.done)
}

// forceTeardownLocked marks a forced end (Abort, an overrun stop) as a stop
// request, so the host's OnStop handler releases what it armed, and returns
// the call that starts the handler if it is installed and has not run. A host
// that finished the session itself tore its capture down already.
func (s *Session) forceTeardownLocked(forced SessionState, err error) func() {
	if forced == "" {
		return func() {}
	}
	if !s.stopRequested {
		s.stopRequested, s.stopOutcome = true, forced
	}
	handler, reason := s.onStop, s.teardownReasonLocked(err)
	if handler == nil || s.onStopCalled {
		return func() {}
	}
	s.onStopCalled = true
	return func() { go handler(reason) }
}

// teardownReasonLocked is the reason an OnStop handler is given: the stop's
// reason, or the failure that aborted the session.
func (s *Session) teardownReasonLocked(err error) string {
	switch {
	case s.rec.StopReason != "":
		return s.rec.StopReason
	case err != nil:
		return "aborted: " + err.Error()
	case s.rec.Error != "":
		return "aborted: " + s.rec.Error
	}
	return "aborted"
}

type finishPayload struct {
	summary, result json.RawMessage
	warnings        []string
}

// encodeFinish encodes a FinishUpdate's payloads outside the session locks.
func encodeFinish(u FinishUpdate) finishPayload {
	final := finishPayload{warnings: []string{u.Warning}}
	var warning string
	if u.Summary != nil {
		final.summary, warning = encodeStatusPayload("summary", u.Summary)
		final.warnings = append(final.warnings, warning)
	}
	if u.Result != nil {
		final.result, warning = encodeStatusPayload("result", u.Result)
		final.warnings = append(final.warnings, warning)
	}
	return final
}

// outcomeLocked resolves the terminal state Finish lands in.
func (s *Session) outcomeLocked(err error) SessionState {
	switch {
	case s.stopRequested && s.stopOutcome == SessionStopped:
		return SessionStopped
	case err != nil:
		return SessionFailed
	}
	return SessionCompleted
}

func (s *Session) stopTimersLocked() {
	for _, timer := range []*time.Timer{s.deadline, s.stopTimer, s.progressFlush} {
		if timer != nil {
			timer.Stop()
		}
	}
	s.deadline, s.stopTimer, s.progressFlush = nil, nil, nil
	s.disarmIdleLocked()
}

// OnStop installs the handler that stops what the host armed. It runs
// asynchronously, once. A stop requested before it was installed — during
// arming, or even one that has since finished the session — runs it at once, so
// a resource armed after the stop is torn down.
func (s *Session) OnStop(fn func(reason string)) {
	if fn == nil {
		panic(fmt.Sprintf("session %s: OnStop handler is nil", s.rec.ID))
	}
	s.mu.Lock()
	s.onStop = fn
	runNow := s.stopRequested && !s.onStopCalled
	s.onStopCalled = s.onStopCalled || runNow
	reason := s.teardownReasonLocked(nil)
	s.mu.Unlock()
	if runNow {
		go fn(reason)
	}
}

// Stop requests the session end as stopped. It moves the session to stopping,
// cancels its context, and runs the OnStop handler asynchronously. A stop that
// is not finished within the registry's StopTimeout fails the session. A
// session nothing runs finishes at once.
func (s *Session) Stop(reason string) {
	s.requestStop(reason, SessionStopped)
}

// requestStop starts a stop that finishes as outcome, reporting whether this
// call started it.
func (s *Session) requestStop(reason string, outcome SessionState) bool {
	return s.requestStopWhen(reason, outcome, nil)
}

// requestStopWhen is requestStop that goes ahead only while when, checked under
// Session.mu, holds; a nil when always holds.
func (s *Session) requestStopWhen(reason string, outcome SessionState, when func() bool) bool {
	s.persistMu.Lock()
	s.mu.Lock()
	if s.rec.State.Terminal() || s.stopRequested || (when != nil && !when()) {
		s.mu.Unlock()
		s.persistMu.Unlock()
		return false
	}
	s.stopRequested, s.stopOutcome = true, outcome
	s.rec.StopReason = reason
	cancel, handler := s.cancel, s.onStop
	if cancel == nil && handler == nil {
		s.mu.Unlock()
		s.persistMu.Unlock()
		s.Finish(FinishUpdate{})
		return true
	}
	s.rec.State = SessionStopping
	s.onStopCalled = handler != nil
	if s.stopTimeout > 0 {
		timeout := s.stopTimeout
		s.stopTimer = time.AfterFunc(timeout, func() {
			s.finish(FinishUpdate{Err: fmt.Errorf("stop (%s) did not finish within %s", reason, timeout)}, SessionFailed)
		})
	}
	status := s.statusLocked()
	s.mu.Unlock()
	_ = s.writeStatus(status)
	s.persistMu.Unlock()

	if cancel != nil {
		cancel()
	}
	if handler != nil {
		go handler(reason)
	}
	return true
}

// extend moves an active, not stopping, session's deadline, returning the
// deadline it settled on.
func (s *Session) extend(stopAt time.Time) (time.Time, error) {
	var settled time.Time
	var err error
	s.mutate(func() bool {
		switch {
		case s.rec.State.Terminal():
			err = fmt.Errorf("session %s is %s: %w", s.rec.ID, s.rec.State, ErrSessionEnded)
		case s.stopRequested:
			err = fmt.Errorf("session %s is stopping (%s)", s.rec.ID, s.rec.StopReason)
		case !stopAt.After(time.Now()):
			err = fmt.Errorf("session %s: extend to %s is not in the future", s.rec.ID, stopAt.Format(time.RFC3339))
		default:
			settled = s.scheduleDeadlineLocked(stopAt)
			return true
		}
		return false
	})
	return settled, err
}

// scheduleDeadlineLocked clamps stopAt to maxDuration from the session's start,
// records it, and (re)arms the timer that stops the session as completed.
func (s *Session) scheduleDeadlineLocked(stopAt time.Time) time.Time {
	start := s.rec.StartedAt
	if !s.durationStart.IsZero() {
		start = s.durationStart
	}
	if limit := start.Add(s.maxDuration); s.maxDuration > 0 && stopAt.After(limit) {
		stopAt = limit
	}
	s.rec.StopAt = &stopAt
	if s.deadline != nil {
		s.deadline.Stop()
	}
	s.deadline = time.AfterFunc(time.Until(stopAt), func() {
		s.requestStop(StopReasonDeadline, SessionCompleted)
	})
	return stopAt
}
