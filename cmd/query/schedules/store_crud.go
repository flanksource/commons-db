package schedules

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrNotFound is returned when a schedule or run does not exist.
var ErrNotFound = errors.New("not found")

// RunFilter narrows a run listing. Empty fields match everything.
type RunFilter struct {
	Schedule string
	Kind     string
	Status   string
	Labels   map[string]string
	Limit    int
}

// List returns every stored schedule, by name.
func (s *Store) List(ctx context.Context) ([]Schedule, error) {
	var rows []scheduleRecord
	if err := s.db.WithContext(ctx).Order("name ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]Schedule, 0, len(rows))
	for _, row := range rows {
		schedule, err := row.decode()
		if err != nil {
			return nil, err
		}
		out = append(out, schedule)
	}
	return out, nil
}

// Get returns one schedule by name.
func (s *Store) Get(ctx context.Context, name string) (Schedule, error) {
	var row scheduleRecord
	err := s.db.WithContext(ctx).Where("name = ?", name).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Schedule{}, fmt.Errorf("schedule %q: %w", name, ErrNotFound)
	}
	if err != nil {
		return Schedule{}, err
	}
	return row.decode()
}

// Save upserts a schedule by name, refusing one that could never run.
func (s *Store) Save(ctx context.Context, schedule Schedule) error {
	if err := schedule.Validate(); err != nil {
		return err
	}
	spec, err := json.Marshal(schedule)
	if err != nil {
		return fmt.Errorf("marshal schedule %q: %w", schedule.Name, err)
	}
	row := scheduleRecord{
		Name:      schedule.Name,
		Namespace: schedule.Namespace,
		Spec:      spec,
		Enabled:   schedule.Enabled,
	}
	// last_run and next_run belong to the scheduler, not to the document, so an
	// edit must not reset a schedule's memory of when it last fired.
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "name"}},
		DoUpdates: clause.Assignments(map[string]any{
			"namespace":  row.Namespace,
			"spec":       row.Spec,
			"enabled":    row.Enabled,
			"updated_at": time.Now(),
		}),
	}).Create(&row).Error
}

// Delete removes a schedule. Its runs are left behind: history outlives the
// definition that produced it.
func (s *Store) Delete(ctx context.Context, name string) error {
	result := s.db.WithContext(ctx).Where("name = ?", name).Delete(&scheduleRecord{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("schedule %q: %w", name, ErrNotFound)
	}
	return nil
}

// SetEnabled toggles a schedule without rewriting its spec.
func (s *Store) SetEnabled(ctx context.Context, name string, enabled bool) error {
	result := s.db.WithContext(ctx).Model(&scheduleRecord{}).
		Where("name = ?", name).
		Updates(map[string]any{"enabled": enabled, "updated_at": time.Now()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("schedule %q: %w", name, ErrNotFound)
	}
	return nil
}

// Run is one row of a schedule's history, as the `schedule run` entity lists it.
type Run struct {
	ID           string            `json:"id" pretty:"label=ID"`
	Schedule     string            `json:"schedule,omitempty" pretty:"label=Schedule"`
	Name         string            `json:"name" pretty:"label=Name"`
	Kind         string            `json:"kind,omitempty" pretty:"label=Kind"`
	Status       string            `json:"status" pretty:"label=Status"`
	Total        int               `json:"total" pretty:"label=Tasks"`
	Completed    int               `json:"completed" pretty:"label=Completed"`
	Failed       int               `json:"failed" pretty:"label=Failed"`
	Labels       map[string]string `json:"labels,omitempty"`
	Owner        string            `json:"owner,omitempty"`
	ArtifactPath string            `json:"artifactPath,omitempty" pretty:"label=Artifact"`
	Delivery     []DeliveryResult  `json:"delivery,omitempty"`
	StartedAt    *time.Time        `json:"startedAt,omitempty" pretty:"label=Started"`
	FinishedAt   *time.Time        `json:"finishedAt,omitempty" pretty:"label=Finished"`
}

func (r Run) GetID() string   { return r.ID }
func (r Run) GetName() string { return r.Name }

// DeliveryResult records what happened when one channel was sent to.
type DeliveryResult struct {
	Connection string    `json:"connection"`
	Channel    string    `json:"channel,omitempty"`
	Sent       bool      `json:"sent"`
	Attached   bool      `json:"attached,omitempty"`
	Error      string    `json:"error,omitempty"`
	At         time.Time `json:"at"`
}

// ListRuns returns run history, newest first.
func (s *Store) ListRuns(ctx context.Context, filter RunFilter) ([]Run, error) {
	rows, err := s.listRuns(ctx, filter)
	if err != nil {
		return nil, err
	}
	out := make([]Run, 0, len(rows))
	for _, row := range rows {
		run, err := row.toRun()
		if err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, nil
}

// GetRun returns one run by its id.
func (s *Store) GetRun(ctx context.Context, id string) (Run, error) {
	var row runRecord
	err := s.db.WithContext(ctx).Where("id = ?", id).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Run{}, fmt.Errorf("run %q: %w", id, ErrNotFound)
	}
	if err != nil {
		return Run{}, err
	}
	return row.toRun()
}

// RecordReport attaches the produced artifact and delivery outcomes to a run.
//
// This runs from inside the run's own task, which is necessarily before the run
// itself is persisted: a run is written when it reaches a terminal status, and
// it cannot be terminal while the task recording the report is still going. So
// this upserts rather than updates — a plain UPDATE would match no row and
// silently discard the artifact. The placeholder it may insert is completed by
// the SaveRun that follows, which deliberately leaves these two columns alone.
func (s *Store) RecordReport(ctx context.Context, runID, artifactPath string, delivery []DeliveryResult) error {
	if runID == "" {
		return fmt.Errorf("run id is required to record a report")
	}
	row := runRecord{
		ID:           runID,
		ArtifactPath: artifactPath,
		Status:       string(statusPending),
		Snapshots:    []byte("[]"),
	}
	assignments := map[string]any{"artifact_path": artifactPath, "updated_at": time.Now()}

	if delivery != nil {
		encoded, err := json.Marshal(delivery)
		if err != nil {
			return fmt.Errorf("marshal delivery for run %s: %w", runID, err)
		}
		row.Delivery = encoded
		assignments["delivery"] = encoded
	}

	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		DoUpdates: clause.Assignments(assignments),
	}).Create(&row).Error
}

// statusPending is what a placeholder row carries until the run finishes and
// SaveRun writes its real status.
const statusPending = "pending"

func (s *Store) listRuns(ctx context.Context, filter RunFilter) ([]runRecord, error) {
	query := s.db.WithContext(ctx).Model(&runRecord{})
	if filter.Schedule != "" {
		query = query.Where("schedule_name = ?", filter.Schedule)
	}
	if filter.Kind != "" {
		query = query.Where("kind = ?", filter.Kind)
	}
	if filter.Status != "" {
		query = query.Where("status = ?", filter.Status)
	}
	for key, value := range filter.Labels {
		query = query.Where("labels ->> ? = ?", key, value)
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = defaultRunLimit
	}

	var rows []runRecord
	err := query.Order("started_at DESC NULLS LAST").Limit(limit).Find(&rows).Error
	return rows, err
}

func (r scheduleRecord) decode() (Schedule, error) {
	var schedule Schedule
	if err := json.Unmarshal(r.Spec, &schedule); err != nil {
		return Schedule{}, fmt.Errorf("decode schedule %q: %w", r.Name, err)
	}
	// The columns are the source of truth for the fields the scheduler writes
	// back, so a stale copy inside the spec blob cannot resurrect a paused
	// schedule or a fire time that already passed.
	schedule.Name = r.Name
	schedule.Namespace = r.Namespace
	schedule.Enabled = r.Enabled
	return schedule, nil
}

func (r runRecord) toRun() (Run, error) {
	run := Run{
		ID:           r.ID,
		Schedule:     r.ScheduleName,
		Name:         r.Name,
		Kind:         r.Kind,
		Status:       r.Status,
		Total:        r.Total,
		Completed:    r.Completed,
		Failed:       r.Failed,
		Labels:       r.Labels,
		Owner:        r.Owner,
		ArtifactPath: r.ArtifactPath,
		StartedAt:    r.StartedAt,
		FinishedAt:   r.FinishedAt,
	}
	if len(r.Delivery) > 0 {
		if err := json.Unmarshal(r.Delivery, &run.Delivery); err != nil {
			return Run{}, fmt.Errorf("decode delivery for run %s: %w", r.ID, err)
		}
	}
	return run, nil
}
