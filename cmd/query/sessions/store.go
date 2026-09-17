package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/types"
)

// sessionEventBatchSize triggers a synchronous flush; a background ticker
// flushes partial batches every sessionEventFlushInterval.
const (
	sessionEventBatchSize     = 100
	sessionEventFlushInterval = time.Second
)

// sessionRecord is one row of the sessions table: the start and status JSON a
// SessionRecord is read back from, and the scalar columns List filters, sorts
// and pages by in SQL.
type sessionRecord struct {
	ID          string     `gorm:"column:id;primaryKey"`
	ProfileName string     `gorm:"column:profile_name"`
	Kind        string     `gorm:"column:kind"`
	Role        string     `gorm:"column:role;default:capture"`
	Principal   string     `gorm:"column:principal"`
	RestartOf   string     `gorm:"column:restart_of"`
	State       string     `gorm:"column:state"`
	Error       string     `gorm:"column:error"`
	EventCount  int64      `gorm:"column:event_count"`
	StartedAt   time.Time  `gorm:"column:started_at"`
	StoppedAt   *time.Time `gorm:"column:stopped_at"`
	Start       types.JSON `gorm:"column:start;type:jsonb;not null;default:'{}'"`
	Status      types.JSON `gorm:"column:status;type:jsonb;not null;default:'{}'"`
	CreatedAt   time.Time  `gorm:"column:created_at;default:now();<-:create"`
	UpdatedAt   time.Time  `gorm:"column:updated_at;autoUpdateTime:false"`
}

func (sessionRecord) TableName() string { return "sessions" }

type sessionEventRecord struct {
	SessionID string     `gorm:"column:session_id;primaryKey"`
	Sequence  int64      `gorm:"column:sequence;primaryKey"`
	Time      time.Time  `gorm:"column:time"`
	Payload   types.JSON `gorm:"column:payload;type:jsonb"`
}

func (sessionEventRecord) TableName() string { return "session_events" }

// Store is the database-backed query.SessionStore and query.EventSink of the
// query server: session records in the sessions table, and the events of
// capture stream sessions batch-written to session_events, which is what a
// session's events are replayed from after it left the registry.
type Store struct {
	db        *gorm.DB
	retention time.Duration

	mu      sync.Mutex
	pending map[string][]sessionEventRecord
	failed  map[string]error

	// resolve finds the live session so a background flush failure can fail it
	// loudly.
	resolve func(id string) (*query.Session, bool)

	stop chan struct{}
	done chan struct{}
}

var (
	_ query.SessionStore = (*Store)(nil)
	_ query.EventSink    = (*Store)(nil)
)

// NewStore creates the store and starts its background flusher.
func NewStore(db *gorm.DB, retention time.Duration) (*Store, error) {
	if db == nil {
		return nil, fmt.Errorf("session database is required")
	}
	if retention <= 0 {
		return nil, fmt.Errorf("session retention must be positive")
	}
	s := &Store{
		db:        db,
		retention: retention,
		pending:   map[string][]sessionEventRecord{},
		failed:    map[string]error{},
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
	}
	go s.flushLoop()
	return s, nil
}

// BindResolver wires the live-session lookup used to fail sessions whose
// events cannot be persisted by the background flusher.
func (s *Store) BindResolver(resolve func(id string) (*query.Session, bool)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resolve = resolve
}

func newSessionRow(rec query.SessionRecord) (sessionRecord, error) {
	start, err := json.Marshal(rec.SessionStart)
	if err != nil {
		return sessionRecord{}, fmt.Errorf("encode session %s start: %w", rec.ID, err)
	}
	row := sessionRecord{
		ID: rec.ID, ProfileName: rec.Profile, Kind: string(rec.Kind), Role: string(rec.Role),
		Principal: rec.Principal, RestartOf: rec.RestartOf, StartedAt: rec.StartedAt, Start: types.JSON(start),
	}
	return row, row.setStatus(rec.ID, rec.SessionStatus)
}

func (r *sessionRecord) setStatus(id string, status query.SessionStatus) error {
	data, err := json.Marshal(status)
	if err != nil {
		return fmt.Errorf("encode session %s status: %w", id, err)
	}
	r.State, r.Error, r.EventCount = string(status.State), status.Error, status.EventCount
	r.StoppedAt, r.UpdatedAt, r.Status = status.StoppedAt, status.UpdatedAt, types.JSON(data)
	return nil
}

func (s *Store) Begin(ctx context.Context, rec query.SessionRecord) error {
	if rec.ID == "" || rec.StartedAt.IsZero() {
		return fmt.Errorf("begin session: id %q and startedAt are both required", rec.ID)
	}
	row, err := newSessionRow(rec)
	if err != nil {
		return err
	}
	result := s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
	switch {
	case result.Error != nil:
		return fmt.Errorf("begin session %s: %w", rec.ID, result.Error)
	case result.RowsAffected == 0:
		return fmt.Errorf("begin session %s: a record already exists", rec.ID)
	}
	return nil
}

// Update writes the status. A terminal status is written only after the
// session's buffered events are, so the record never ends ahead of its events.
func (s *Store) Update(ctx context.Context, id string, status query.SessionStatus) error {
	if status.State.Terminal() {
		if err := s.flushSession(id); err != nil {
			return fmt.Errorf("update session %s: %w", id, err)
		}
	}
	var row sessionRecord
	if err := row.setStatus(id, status); err != nil {
		return err
	}
	result := s.db.WithContext(ctx).Model(&sessionRecord{}).Where("id = ?", id).Updates(map[string]any{
		"state": row.State, "error": row.Error, "event_count": row.EventCount,
		"stopped_at": row.StoppedAt, "updated_at": row.UpdatedAt, "status": row.Status,
	})
	switch {
	case result.Error != nil:
		return fmt.Errorf("update session %s: %w", id, result.Error)
	case result.RowsAffected == 0:
		return fmt.Errorf("update session %s: no record; it was never begun or has been pruned", id)
	}
	return nil
}

func (s *Store) Get(ctx context.Context, id string) (query.SessionRecord, bool, error) {
	var row sessionRecord
	err := s.db.WithContext(ctx).Where("id = ?", id).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return query.SessionRecord{}, false, nil
	}
	if err != nil {
		return query.SessionRecord{}, false, fmt.Errorf("get session %s: %w", id, err)
	}
	rec, err := row.record()
	return rec, err == nil, err
}

func (s *Store) Lineage(ctx context.Context, ids []string) (map[string][]string, error) {
	lineage := map[string][]string{}
	if len(ids) == 0 {
		return lineage, nil
	}
	var rows []sessionRecord
	err := s.db.WithContext(ctx).Select("id", "restart_of").Where("restart_of IN ?", ids).
		Order("started_at, id").Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("session lineage: %w", err)
	}
	for _, row := range rows {
		lineage[row.RestartOf] = append(lineage[row.RestartOf], row.ID)
	}
	return lineage, nil
}

func (r sessionRecord) record() (query.SessionRecord, error) {
	var rec query.SessionRecord
	if err := json.Unmarshal(r.Start, &rec.SessionStart); err != nil {
		return rec, fmt.Errorf("decode session %s start: %w", r.ID, err)
	}
	if err := json.Unmarshal(r.Status, &rec.SessionStatus); err != nil {
		return rec, fmt.Errorf("decode session %s status: %w", r.ID, err)
	}
	if rec.ID != r.ID {
		return rec, fmt.Errorf("session %s: start record names id %q", r.ID, rec.ID)
	}
	return rec, nil
}

// Append buffers the event, flushing synchronously once a full batch has
// accumulated. It returns the failure of an earlier background flush of the
// session, so the session emitting it fails.
func (s *Store) Append(_ context.Context, e query.Event) error {
	payload, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("encode session %s event %d: %w", e.SessionID, e.Sequence, err)
	}
	s.mu.Lock()
	if failure, failed := s.failed[e.SessionID]; failed {
		delete(s.failed, e.SessionID)
		s.mu.Unlock()
		return failure
	}
	s.pending[e.SessionID] = append(s.pending[e.SessionID], sessionEventRecord{
		SessionID: e.SessionID, Sequence: e.Sequence, Time: e.Time, Payload: types.JSON(payload),
	})
	full := len(s.pending[e.SessionID]) >= sessionEventBatchSize
	s.mu.Unlock()

	if full {
		return s.flushSession(e.SessionID)
	}
	return nil
}

// flushSession writes the events buffered for one session. A batch that fails
// is buffered again, ahead of anything appended since, so the retried terminal
// status write retries it too rather than landing without its events.
func (s *Store) flushSession(id string) error {
	s.mu.Lock()
	records := s.pending[id]
	delete(s.pending, id)
	s.mu.Unlock()
	err := s.writeEvents(id, records)
	if err != nil {
		s.mu.Lock()
		s.pending[id] = append(records, s.pending[id]...)
		s.mu.Unlock()
	}
	return err
}

func (s *Store) writeEvents(id string, records []sessionEventRecord) error {
	if len(records) == 0 {
		return nil
	}
	err := s.db.Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(records, sessionEventBatchSize).Error
	if err != nil {
		return fmt.Errorf("persist %d events for session %s: %w", len(records), id, err)
	}
	return nil
}

// Flush writes all buffered events, joining any errors (including flush
// failures recorded for sessions with no live handle).
func (s *Store) Flush() error {
	flushErr := s.flushPending()
	s.mu.Lock()
	errs := make([]error, 0, len(s.failed)+1)
	for id, err := range s.failed {
		errs = append(errs, fmt.Errorf("session %s: %w", id, err))
	}
	s.failed = map[string]error{}
	s.mu.Unlock()
	return errors.Join(append(errs, flushErr)...)
}

// flushPending writes every buffered batch, failing each session whose batch
// cannot be written.
func (s *Store) flushPending() error {
	s.mu.Lock()
	batches := s.pending
	s.pending = map[string][]sessionEventRecord{}
	s.mu.Unlock()

	var errs []error
	for id, records := range batches {
		if err := s.writeEvents(id, records); err != nil {
			s.failSession(id, err)
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Close stops the background flusher after a final flush.
func (s *Store) Close() error {
	close(s.stop)
	<-s.done
	return s.Flush()
}

// Events returns the persisted events for a session, in sequence order.
func (s *Store) Events(ctx context.Context, id string) ([]query.Event, error) {
	var records []sessionEventRecord
	if err := s.db.WithContext(ctx).Where("session_id = ?", id).Order("sequence").Find(&records).Error; err != nil {
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

// HasEvents reports whether any event of session id is buffered or persisted.
func (s *Store) HasEvents(ctx context.Context, id string) (bool, error) {
	s.mu.Lock()
	buffered := len(s.pending[id]) > 0
	s.mu.Unlock()
	if buffered {
		return true, nil
	}
	var held bool
	err := s.db.WithContext(ctx).Raw("SELECT EXISTS (SELECT 1 FROM session_events WHERE session_id = ?)", id).Scan(&held).Error
	if err != nil {
		return false, fmt.Errorf("find events for session %s: %w", id, err)
	}
	return held, nil
}

// Prune atomically deletes sessions stopped before the retention window and
// their events.
func (s *Store) Prune(ctx context.Context) error {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var sessionIDs []string
		if err := tx.Model(&sessionRecord{}).
			Where("stopped_at IS NOT NULL AND stopped_at < ?", time.Now().Add(-s.retention)).
			Pluck("id", &sessionIDs).Error; err != nil {
			return fmt.Errorf("find expired sessions: %w", err)
		}
		if len(sessionIDs) == 0 {
			return nil
		}
		if err := tx.Where("session_id IN ?", sessionIDs).Delete(&sessionEventRecord{}).Error; err != nil {
			return fmt.Errorf("delete expired session events: %w", err)
		}
		if err := tx.Where("id IN ?", sessionIDs).Delete(&sessionRecord{}).Error; err != nil {
			return fmt.Errorf("delete expired sessions: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("prune sessions: %w", err)
	}
	return nil
}

func (s *Store) flushLoop() {
	defer close(s.done)
	ticker := time.NewTicker(sessionEventFlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
			// A failed batch has already failed its session, or is held for
			// the session's next Append and the next Flush or Close.
			_ = s.flushPending()
		}
	}
}

// failSession fails the live session loudly; without a live handle the error
// is held for the session's next Append, or the next Flush/Close.
func (s *Store) failSession(id string, err error) {
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
