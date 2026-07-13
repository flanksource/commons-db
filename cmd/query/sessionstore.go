package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/types"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// sessionEventBatchSize triggers a synchronous flush; a background ticker
// flushes partial batches every sessionEventFlushInterval.
const (
	sessionEventBatchSize     = 100
	sessionEventFlushInterval = time.Second
)

type sessionRecord struct {
	ID          string     `gorm:"column:id;primaryKey"`
	ProfileName string     `gorm:"column:profile_name"`
	Kind        string     `gorm:"column:kind"`
	Params      types.JSON `gorm:"column:params;type:jsonb"`
	State       string     `gorm:"column:state"`
	Error       string     `gorm:"column:error"`
	EventCount  int64      `gorm:"column:event_count"`
	StartedAt   time.Time  `gorm:"column:started_at"`
	StoppedAt   *time.Time `gorm:"column:stopped_at"`
	CreatedAt   time.Time  `gorm:"column:created_at;default:now();<-:create"`
	UpdatedAt   time.Time  `gorm:"column:updated_at;default:now()"`
}

func (sessionRecord) TableName() string { return "sessions" }

type sessionEventRecord struct {
	SessionID string     `gorm:"column:session_id;primaryKey"`
	Sequence  int64      `gorm:"column:sequence;primaryKey"`
	Time      time.Time  `gorm:"column:time"`
	Payload   types.JSON `gorm:"column:payload;type:jsonb"`
}

func (sessionEventRecord) TableName() string { return "session_events" }

// SessionStore is the durable record of trace/top sessions: its OnTransition
// and OnEvent methods plug into query.RegistryOptions, upserting session state
// and batch-writing events. Live streaming always serves from memory; the DB
// answers after the session left the registry (restart, pruning).
type SessionStore struct {
	db        *gorm.DB
	retention time.Duration

	mu      sync.Mutex
	pending map[string][]sessionEventRecord
	failed  map[string]error

	// resolve finds the live session so a flush failure can fail it loudly.
	resolve func(id string) (*query.Session, bool)

	stop chan struct{}
	done chan struct{}
}

// NewSessionStore creates the store and starts its background flusher.
func NewSessionStore(db *gorm.DB, retention time.Duration) *SessionStore {
	s := &SessionStore{
		db:        db,
		retention: retention,
		pending:   map[string][]sessionEventRecord{},
		failed:    map[string]error{},
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
	}
	go s.flushLoop()
	return s
}

// BindResolver wires the live-session lookup used to fail sessions whose
// events cannot be persisted.
func (s *SessionStore) BindResolver(resolve func(id string) (*query.Session, bool)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resolve = resolve
}

// OnTransition upserts the session row; it matches query.RegistryOptions.OnTransition.
func (s *SessionStore) OnTransition(info query.SessionInfo) {
	record := sessionRecord{
		ID:          info.ID,
		ProfileName: info.Profile,
		Kind:        string(info.Kind),
		State:       string(info.State),
		Error:       info.Error,
		EventCount:  info.EventCount,
		StartedAt:   info.StartedAt,
		StoppedAt:   info.StoppedAt,
	}
	if len(info.Params) > 0 {
		data, err := json.Marshal(info.Params)
		if err != nil {
			s.failSession(info.ID, fmt.Errorf("marshal session params: %w", err))
			return
		}
		record.Params = types.JSON(data)
	}
	err := s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		DoUpdates: clause.AssignmentColumns([]string{"state", "error", "event_count", "stopped_at", "updated_at"}),
	}).Create(&record).Error
	if err != nil {
		s.failSession(info.ID, fmt.Errorf("persist session state: %w", err))
	}
}

// OnEvent buffers the event, flushing synchronously once a full batch has
// accumulated; it matches query.RegistryOptions.OnEvent.
func (s *SessionStore) OnEvent(e query.Event) {
	payload, err := json.Marshal(e)
	if err != nil {
		s.failSession(e.SessionID, fmt.Errorf("marshal session event: %w", err))
		return
	}
	s.mu.Lock()
	s.pending[e.SessionID] = append(s.pending[e.SessionID], sessionEventRecord{
		SessionID: e.SessionID,
		Sequence:  e.Sequence,
		Time:      e.Time,
		Payload:   types.JSON(payload),
	})
	full := len(s.pending[e.SessionID]) >= sessionEventBatchSize
	s.mu.Unlock()

	if full {
		if err := s.Flush(); err != nil {
			s.failSession(e.SessionID, err)
		}
	}
}

// Flush writes all buffered events, joining any errors (including flush
// failures recorded for sessions with no live handle).
func (s *SessionStore) Flush() error {
	s.mu.Lock()
	batches := s.pending
	s.pending = map[string][]sessionEventRecord{}
	errs := make([]error, 0, len(s.failed))
	for id, err := range s.failed {
		errs = append(errs, fmt.Errorf("session %s: %w", id, err))
	}
	s.failed = map[string]error{}
	s.mu.Unlock()

	for id, records := range batches {
		if err := s.db.Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(records, sessionEventBatchSize).Error; err != nil {
			wrapped := fmt.Errorf("persist %d events for session %s: %w", len(records), id, err)
			s.failSession(id, wrapped)
			errs = append(errs, wrapped)
		}
	}
	return errors.Join(errs...)
}

// Close stops the background flusher after a final flush.
func (s *SessionStore) Close() error {
	close(s.stop)
	<-s.done
	return s.Flush()
}

// List returns all persisted sessions, newest first.
func (s *SessionStore) List() ([]query.SessionInfo, error) {
	var records []sessionRecord
	if err := s.db.Order("started_at DESC").Find(&records).Error; err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	out := make([]query.SessionInfo, len(records))
	for i := range records {
		info, err := records[i].info()
		if err != nil {
			return nil, err
		}
		out[i] = info
	}
	return out, nil
}

// Get returns the persisted session, reporting absence without error.
func (s *SessionStore) Get(id string) (query.SessionInfo, bool, error) {
	var record sessionRecord
	err := s.db.Where("id = ?", id).First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return query.SessionInfo{}, false, nil
	}
	if err != nil {
		return query.SessionInfo{}, false, fmt.Errorf("get session %s: %w", id, err)
	}
	info, err := record.info()
	return info, err == nil, err
}

// Events returns the persisted events for a session, in sequence order.
func (s *SessionStore) Events(id string) ([]query.Event, error) {
	var records []sessionEventRecord
	if err := s.db.Where("session_id = ?", id).Order("sequence").Find(&records).Error; err != nil {
		return nil, fmt.Errorf("list events for session %s: %w", id, err)
	}
	out := make([]query.Event, len(records))
	for i := range records {
		if err := json.Unmarshal(records[i].Payload, &out[i]); err != nil {
			return nil, fmt.Errorf("decode event %s/%d: %w", id, records[i].Sequence, err)
		}
	}
	return out, nil
}

// MarkInterrupted finalizes sessions orphaned by a restart: their streams are
// gone, so rows still starting/running become interrupted.
func (s *SessionStore) MarkInterrupted() error {
	err := s.db.Model(&sessionRecord{}).
		Where("state IN ?", []string{string(query.SessionStarting), string(query.SessionRunning)}).
		Updates(map[string]any{"state": string(query.SessionInterrupted), "stopped_at": time.Now(), "updated_at": time.Now()}).Error
	if err != nil {
		return fmt.Errorf("mark interrupted sessions: %w", err)
	}
	return nil
}

// Prune deletes terminal sessions older than the retention window; their
// events cascade-delete.
func (s *SessionStore) Prune() error {
	err := s.db.Where("stopped_at IS NOT NULL AND stopped_at < ?", time.Now().Add(-s.retention)).
		Delete(&sessionRecord{}).Error
	if err != nil {
		return fmt.Errorf("prune sessions: %w", err)
	}
	return nil
}

func (s *SessionStore) flushLoop() {
	defer close(s.done)
	ticker := time.NewTicker(sessionEventFlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
			// Flush aborts the affected sessions on error and re-reports
			// unresolved failures on the next explicit Flush/Close.
			_ = s.Flush()
		}
	}
}

// failSession fails the live session loudly; without a live handle the error
// is held for the next Flush/Close to return.
func (s *SessionStore) failSession(id string, err error) {
	s.mu.Lock()
	resolve := s.resolve
	s.mu.Unlock()
	if resolve != nil {
		if session, ok := resolve(id); ok {
			session.Abort(err)
			return
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.failed[id]; !exists {
		s.failed[id] = err
	}
}

func (r sessionRecord) info() (query.SessionInfo, error) {
	info := query.SessionInfo{
		ID:         r.ID,
		Profile:    r.ProfileName,
		Kind:       query.ProfileKind(r.Kind),
		State:      query.SessionState(r.State),
		Error:      r.Error,
		EventCount: r.EventCount,
		StartedAt:  r.StartedAt,
		StoppedAt:  r.StoppedAt,
	}
	if len(r.Params) > 0 {
		if err := json.Unmarshal(r.Params, &info.Params); err != nil {
			return query.SessionInfo{}, fmt.Errorf("decode session %s params: %w", r.ID, err)
		}
	}
	return info, nil
}
