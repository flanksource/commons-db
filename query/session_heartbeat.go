package query

import (
	stdcontext "context"
	"errors"
	"fmt"
	"time"
)

// Sweep stop reasons.
const (
	StopReasonHeartbeatLost = "heartbeat lost"
	StopReasonOwnerRestart  = "owner restarted"
)

// heartbeatLoop writes heartbeats and retries refused status writes while any
// session is active or any ended session's status never reached the store, and
// exits when neither holds; Add and a refused write start it again.
func (r *SessionRegistry) heartbeatLoop() {
	ticker := time.NewTicker(r.opts.HeartbeatEvery)
	defer ticker.Stop()
	for now := range ticker.C {
		r.tick(now)
		r.mu.Lock()
		if r.activeLocked() == 0 && !r.repairsPendingLocked() {
			r.heartbeating = false
			r.mu.Unlock()
			return
		}
		r.mu.Unlock()
	}
}

// tick writes the heartbeat of every active capture, which also carries a
// status write the store refused, and re-issues the final status of every
// ended session whose last write was refused. Views have no record, and end on
// their own idle timer.
func (r *SessionRegistry) tick(now time.Time) {
	for _, session := range r.snapshotSessions() {
		switch info := session.Snapshot(); {
		case info.Role == SessionRoleView:
		case !info.State.Terminal():
			session.heartbeat(now)
		case session.statusUnpersisted():
			// A refused write is already logged and counted; the next tick
			// retries it.
			_ = session.reissueStatus()
		}
	}
}

// Sweep interrupts active records whose owner is gone: a heartbeat older than
// StaleAfter, from an owner on another host or from another boot of this one.
// A fresh heartbeat is never interrupted, whoever wrote it: a CLI capture
// beside a server on the same host and store writes its own. A record whose
// session ended in this registry while its store copy is still active has its
// final status written again; a record still live here is left alone.
func (r *SessionRegistry) Sweep(ctx stdcontext.Context) error {
	if r.opts.Store == nil {
		return fmt.Errorf("sweep sessions: no session store is configured")
	}
	page, err := r.opts.Store.List(ctx, SessionFilter{
		State: []string{string(SessionStarting), string(SessionRunning), string(SessionStopping)},
	})
	if err != nil {
		return fmt.Errorf("sweep sessions: list active: %w", err)
	}
	now := time.Now()
	var errs []error
	for _, rec := range page.Items {
		if rec.State.Terminal() {
			continue
		}
		if session, live := r.Get(rec.ID); live {
			if session.Snapshot().State.Terminal() {
				errs = append(errs, session.reissueStatus())
			}
			continue
		}
		reason := r.sweepReason(rec, now)
		if reason == "" {
			continue
		}
		status := rec.SessionStatus
		status.State, status.StopReason = SessionInterrupted, reason
		status.StoppedAt, status.UpdatedAt = &now, now
		if err := r.opts.Store.Update(ctx, rec.ID, status); err != nil {
			errs = append(errs, fmt.Errorf("sweep session %s: %w", rec.ID, err))
		}
	}
	return errors.Join(errs...)
}

// sweepReason is why rec's owner counts as gone, or empty while its heartbeat
// is fresh.
func (r *SessionRegistry) sweepReason(rec SessionRecord, now time.Time) string {
	switch {
	case now.Sub(rec.HeartbeatAt) <= r.opts.StaleAfter:
		return ""
	case rec.Owner.Host == r.opts.Owner.Host && rec.Owner.Boot != r.opts.Owner.Boot:
		return StopReasonOwnerRestart
	}
	return StopReasonHeartbeatLost
}
