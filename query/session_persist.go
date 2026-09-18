package query

import (
	stdcontext "context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/flanksource/commons/logger"
)

// maxStatusPayloadBytes caps a status summary and result each.
const maxStatusPayloadBytes = 16 << 10

// sessionPersistence is what a registry attaches to a session it admits: the
// store its status is written to and the bounds its lifecycle runs under.
type sessionPersistence struct {
	// persistMu serializes every status change with its write, so writes land
	// in the order the changes happened and the last one carries the latest
	// status. Emit never takes it, so a slow store never blocks the stream.
	persistMu sync.Mutex

	store         SessionStore
	events        EventSink
	storeCtx      stdcontext.Context
	stopTimeout   time.Duration
	progressEvery time.Duration
	maxDuration   time.Duration
	persistFailed func()

	persistedAt   time.Time   // guarded by Session.mu
	progressFlush *time.Timer // guarded by Session.mu

	// unpersisted reports that the last status write failed, so the store is
	// behind the session until a later write lands. The registry's heartbeat
	// retries it, even after the session ended. Guarded by Session.mu.
	unpersisted bool
}

// sessionAttachment is what admit hands a session before it becomes visible.
type sessionAttachment struct {
	run           stdcontext.Context
	cancel        stdcontext.CancelFunc
	store         SessionStore
	events        EventSink
	storeCtx      stdcontext.Context
	stopTimeout   time.Duration
	progressEvery time.Duration
	maxDuration   time.Duration
	stopAt        time.Time
	persistFailed func()
	viewGrace     time.Duration
	viewReaped    func()
}

func (s *Session) attach(a sessionAttachment) {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.run, s.cancel = a.run, a.cancel
	s.store, s.events, s.storeCtx = a.store, a.events, a.storeCtx
	s.stopTimeout, s.progressEvery, s.maxDuration = a.stopTimeout, a.progressEvery, a.maxDuration
	s.persistFailed = a.persistFailed
	s.viewGrace, s.viewReaped = a.viewGrace, a.viewReaped
	stopAt := a.stopAt
	s.rec.StopAt = &stopAt
}

// detachStore drops the store from a session whose record was never begun, so
// its final status is not written to a record that does not exist.
func (s *Session) detachStore() {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.store, s.events = nil, nil
}

func (s *Session) record() SessionRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rec
}

// statusLocked stamps UpdatedAt and returns the status to write. Session.mu
// must be held.
func (s *Session) statusLocked() SessionStatus {
	s.rec.UpdatedAt = time.Now()
	return s.rec.SessionStatus
}

// writeStatus is the single writer of a session's status. persistMu must be
// held and Session.mu must not. A failed write is logged at error level,
// counted on the registry, set on the status warning, and marked unpersisted,
// so the next write — a later change, or the heartbeat's repair — carries it.
func (s *Session) writeStatus(status SessionStatus) error {
	if s.store == nil {
		return nil
	}
	s.mu.Lock()
	s.persistedAt = time.Now()
	s.mu.Unlock()

	err := s.store.Update(s.storeCtx, s.rec.ID, status)
	s.mu.Lock()
	s.unpersisted = err != nil
	if err != nil {
		s.rec.Warning = joinWarning(s.rec.Warning, fmt.Sprintf("persist %s status: %v", status.State, err))
	}
	s.mu.Unlock()
	if err == nil {
		return nil
	}
	logger.Errorf("session %s (%s): persist %s status: %v", s.rec.ID, s.rec.Profile, status.State, err)
	if s.persistFailed != nil {
		s.persistFailed()
	}
	return fmt.Errorf("session %s: persist %s status: %w", s.rec.ID, status.State, err)
}

// statusUnpersisted reports whether the store is behind the session's status.
func (s *Session) statusUnpersisted() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.unpersisted
}

// reissueStatus writes the session's current status again: the repair of a
// write the store refused, or of a store copy Sweep found behind a session
// that already ended here.
func (s *Session) reissueStatus() error {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	s.mu.Lock()
	status := s.statusLocked()
	s.mu.Unlock()
	return s.writeStatus(status)
}

// mutate applies change under both locks and writes the resulting status when
// change reports it should be written.
func (s *Session) mutate(change func() bool) {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	s.mu.Lock()
	write := change()
	status := s.statusLocked()
	s.mu.Unlock()
	if write {
		_ = s.writeStatus(status)
	}
}

// scheduleProgressFlushLocked arranges one status write no sooner than
// progressEvery after the last, so progress persists at most that often and
// never later. Session.mu must be held.
func (s *Session) scheduleProgressFlushLocked() {
	if s.store == nil || s.progressFlush != nil || s.rec.State.Terminal() {
		return
	}
	delay := max(s.progressEvery-time.Since(s.persistedAt), 0)
	s.progressFlush = time.AfterFunc(delay, s.flushProgress)
}

func (s *Session) flushProgress() {
	s.mutate(func() bool {
		s.progressFlush = nil
		return !s.rec.State.Terminal()
	})
}

// heartbeat stamps HeartbeatAt and writes it.
func (s *Session) heartbeat(now time.Time) {
	s.mutate(func() bool {
		if s.rec.State.Terminal() {
			return false
		}
		s.rec.HeartbeatAt = now
		return true
	})
}

// encodeStatusPayload marshals a summary or result for the status record. A
// value over the cap is replaced by {"omitted": bytes}; either problem comes
// back as a warning for the status.
func encodeStatusPayload(field string, value any) (json.RawMessage, string) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Sprintf("%s not recorded: %v", field, err)
	}
	if len(data) > maxStatusPayloadBytes {
		return json.RawMessage(fmt.Sprintf(`{"omitted":%d}`, len(data))),
			fmt.Sprintf("%s omitted: %d bytes exceeds the %d byte cap", field, len(data), maxStatusPayloadBytes)
	}
	return data, ""
}

func joinWarning(existing, next string) string {
	switch {
	case next == "" || strings.Contains(existing, next):
		return existing
	case existing == "":
		return next
	}
	return existing + "; " + next
}
