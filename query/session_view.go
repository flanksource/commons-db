package query

import (
	"fmt"
	"time"
)

// sessionView is how a view session ends once nobody watches it: a timer armed
// when its last subscriber leaves (or at admission, before any arrives) and
// disarmed when one subscribes, so a view a client abandoned ends within its
// grace instead of holding a view slot until a heartbeat notices.
//
// The grace is what makes a reload survivable rather than fatal: a browser that
// reloads, restarts on HMR or closes a tab never sends the stop its unmount
// handler would have, and a viewer that comes back inside the window — an
// EventSource retry naming its Last-Event-ID — disarms the timer and resumes
// from the event it names.
type sessionView struct {
	viewGrace  time.Duration
	viewReaped func()

	idleTimer *time.Timer // guarded by Session.mu
	idleArm   uint64      // guarded by Session.mu; identifies the armed timer
}

// armIdleLocked starts a view's grace. Session.mu must be held.
func (s *Session) armIdleLocked() {
	if s.rec.Role != SessionRoleView || s.viewGrace <= 0 || s.rec.State.Terminal() || len(s.subscribers) > 0 {
		return
	}
	s.disarmIdleLocked()
	arm, grace := s.idleArm, s.viewGrace
	s.idleTimer = time.AfterFunc(grace, func() { s.reapIdle(arm, grace) })
}

// disarmIdleLocked cancels an armed grace. Session.mu must be held.
func (s *Session) disarmIdleLocked() {
	s.idleArm++
	if s.idleTimer != nil {
		s.idleTimer.Stop()
		s.idleTimer = nil
	}
}

// reapIdle stops the view, unless a subscriber arrived (or the grace was
// re-armed) after the timer that calls it fired. stillIdle is checked under
// Session.mu by requestStopWhen, which is what makes the race with a viewer
// reconnecting inside the grace decidable rather than a guess.
func (s *Session) reapIdle(arm uint64, grace time.Duration) {
	stillIdle := func() bool { return s.idleArm == arm && len(s.subscribers) == 0 }
	reason := fmt.Sprintf("%s: no event subscriber for %s", StopReasonReapedIdle, grace)
	if s.requestStopWhen(reason, SessionStopped, stillIdle) && s.viewReaped != nil {
		s.viewReaped()
	}
}
