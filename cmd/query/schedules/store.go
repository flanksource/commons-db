package schedules

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/flanksource/clicky/task"
	"github.com/flanksource/commons-db/types"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// defaultRunLimit bounds an unfiltered run listing so history cannot return an
// unbounded result set.
const defaultRunLimit = 200

type scheduleRecord struct {
	ID        string     `gorm:"column:id;primaryKey;default:generate_ulid()"`
	Name      string     `gorm:"column:name"`
	Namespace string     `gorm:"column:namespace"`
	Spec      types.JSON `gorm:"column:spec;type:jsonb"`
	Enabled   bool       `gorm:"column:enabled"`
	LastRun   *time.Time `gorm:"column:last_run"`
	NextRun   *time.Time `gorm:"column:next_run"`
	CreatedAt time.Time  `gorm:"column:created_at;default:now();<-:create"`
	UpdatedAt time.Time  `gorm:"column:updated_at;default:now()"`
}

func (scheduleRecord) TableName() string { return "schedules" }

type runRecord struct {
	ID           string            `gorm:"column:id;primaryKey"`
	ScheduleName string            `gorm:"column:schedule_name"`
	Name         string            `gorm:"column:name"`
	Kind         string            `gorm:"column:kind"`
	Status       string            `gorm:"column:status"`
	Labels       map[string]string `gorm:"column:labels;type:jsonb;serializer:json"`
	Owner        string            `gorm:"column:owner"`
	Total        int               `gorm:"column:total"`
	Completed    int               `gorm:"column:completed"`
	Failed       int               `gorm:"column:failed"`
	Running      int               `gorm:"column:running"`
	Snapshots    types.JSON        `gorm:"column:snapshots;type:jsonb"`
	ArtifactPath string            `gorm:"column:artifact_path"`
	Delivery     types.JSON        `gorm:"column:delivery;type:jsonb"`
	StartedAt    *time.Time        `gorm:"column:started_at"`
	FinishedAt   *time.Time        `gorm:"column:finished_at"`
	CreatedAt    time.Time         `gorm:"column:created_at;default:now();<-:create"`
	UpdatedAt    time.Time         `gorm:"column:updated_at;default:now()"`
}

func (runRecord) TableName() string { return "schedule_runs" }

type fireRecord struct {
	ID           string    `gorm:"column:id;primaryKey;default:generate_ulid()"`
	ScheduleName string    `gorm:"column:schedule_name"`
	ScheduledFor time.Time `gorm:"column:scheduled_for"`
	FiredAt      time.Time `gorm:"column:fired_at"`
	Outcome      string    `gorm:"column:outcome"`
	RunID        string    `gorm:"column:run_id"`
	Reason       string    `gorm:"column:reason"`
	Error        string    `gorm:"column:error"`
	CreatedAt    time.Time `gorm:"column:created_at;default:now();<-:create"`
}

func (fireRecord) TableName() string { return "schedule_fires" }

// Store is the durable home of schedules and their run history. It implements
// clicky's task.Store, so the task manager persists finished runs through it and
// reads evicted ones back out without knowing anything about this schema.
type Store struct {
	db *gorm.DB
}

var _ task.Store = (*Store)(nil)

func NewStore(db *gorm.DB) (*Store, error) {
	if db == nil {
		return nil, fmt.Errorf("schedule store requires a database")
	}
	return &Store{db: db}, nil
}

// SaveRun upserts one run keyed by its clicky group id. It is idempotent by
// design: the terminal-transition write and the later eviction write are the
// same row, and the newest snapshot wins.
func (s *Store) SaveRun(ctx context.Context, groupID string, snapshots []task.TaskSnapshot) error {
	record, ok := task.RunFromSnapshots(snapshots)
	if !ok {
		return fmt.Errorf("run %s carries no group snapshot", groupID)
	}
	payload, err := json.Marshal(snapshots)
	if err != nil {
		return fmt.Errorf("marshal snapshots for run %s: %w", record.ID, err)
	}

	row := runRecord{
		ID:           record.ID,
		ScheduleName: record.Labels["schedule"],
		Name:         record.Name,
		Kind:         record.Kind,
		Status:       record.Status,
		Labels:       record.Labels,
		Owner:        record.Owner,
		Total:        record.Total,
		Completed:    record.Completed,
		Failed:       record.Failed,
		Running:      record.Running,
		Snapshots:    payload,
		StartedAt:    parseTime(record.StartedAt),
		FinishedAt:   parseTime(record.FinishedAt),
	}

	// artifact_path and delivery are written by the runner after the report is
	// produced; a snapshot write must not blank them back out.
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"schedule_name", "name", "kind", "status", "labels", "owner",
			"total", "completed", "failed", "running", "snapshots",
			"started_at", "finished_at", "updated_at",
		}),
	}).Create(&row).Error
}

// Runs lists persisted runs matching filter, newest first.
func (s *Store) Runs(ctx context.Context, filter task.RunFilter) ([]task.RunMeta, error) {
	rows, err := s.listRuns(ctx, RunFilter{
		Kind:   filter.Kind,
		Status: filter.Status,
		Labels: filter.Labels,
	})
	if err != nil {
		return nil, err
	}
	out := make([]task.RunMeta, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.meta())
	}
	return out, nil
}

// Snapshot returns the stored snapshot slice for one run.
func (s *Store) Snapshot(ctx context.Context, id string) ([]task.TaskSnapshot, error) {
	var row runRecord
	err := s.db.WithContext(ctx).Where("id = ?", id).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var snapshots []task.TaskSnapshot
	if len(row.Snapshots) == 0 {
		return nil, nil
	}
	if err := json.Unmarshal(row.Snapshots, &snapshots); err != nil {
		return nil, fmt.Errorf("decode snapshots for run %s: %w", id, err)
	}
	return snapshots, nil
}

// Control is refused for stored runs. A run this process no longer holds has no
// goroutine to stop, and pretending otherwise would report success for an action
// that did nothing.
func (s *Store) Control(_ context.Context, id string, action task.ControlAction) error {
	return fmt.Errorf("run %s has finished and cannot be %sed", id, action)
}

func (s *Store) ListSchedules(ctx context.Context) ([]task.Schedule, error) {
	stored, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]task.Schedule, 0, len(stored))
	for _, schedule := range stored {
		out = append(out, schedule.Timing())
	}
	return out, nil
}

// SaveSchedule persists the timing half of a schedule. The spec itself is
// written by Save; this records what the scheduler learned by running it, which
// is what lets a restart tell that a scheduled time was missed.
func (s *Store) SaveSchedule(ctx context.Context, schedule task.Schedule) error {
	updates := map[string]any{
		"enabled":    schedule.Enabled,
		"updated_at": time.Now(),
	}
	if schedule.LastRun != nil {
		updates["last_run"] = *schedule.LastRun
	}
	if schedule.NextRun != nil {
		updates["next_run"] = *schedule.NextRun
	}
	return s.db.WithContext(ctx).Model(&scheduleRecord{}).
		Where("name = ?", schedule.Name).Updates(updates).Error
}

func (s *Store) DeleteSchedule(ctx context.Context, name string) error {
	return s.Delete(ctx, name)
}

// RecordFire appends one firing decision, including the ones that produced no
// run. A schedule that keeps skipping is invisible without this.
func (s *Store) RecordFire(ctx context.Context, name string, fire task.Fire) error {
	return s.db.WithContext(ctx).Create(&fireRecord{
		ScheduleName: name,
		ScheduledFor: fire.ScheduledFor,
		FiredAt:      fire.At,
		Outcome:      string(fire.Outcome),
		RunID:        fire.RunID,
		Reason:       fire.Reason,
		Error:        fire.Error,
	}).Error
}

func parseTime(value string) *time.Time {
	if value == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		if parsed, err = time.Parse(time.RFC3339, value); err != nil {
			return nil
		}
	}
	return &parsed
}

func (r runRecord) meta() task.RunMeta {
	meta := task.RunMeta{
		ID:        r.ID,
		Name:      r.Name,
		Kind:      r.Kind,
		Labels:    r.Labels,
		Owner:     r.Owner,
		Status:    r.Status,
		Total:     r.Total,
		Completed: r.Completed,
		Failed:    r.Failed,
		Running:   r.Running,
	}
	if r.StartedAt != nil {
		meta.StartedAt = r.StartedAt.UTC().Format(time.RFC3339)
	}
	if r.FinishedAt != nil {
		meta.FinishedAt = r.FinishedAt.UTC().Format(time.RFC3339)
	}
	return meta
}
