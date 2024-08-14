package models

import (
	"context"
	"time"

	"github.com/flanksource/duty/types"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Event represents the event queue table.

type Event struct {
	ID          uuid.UUID           `gorm:"default:generate_ulid()"`
	Name        string              `json:"name"`
	CreatedAt   time.Time           `json:"created_at"`
	Properties  types.JSONStringMap `json:"properties"`
	Error       *string             `json:"error,omitempty"`
	Attempts    int                 `json:"attempts"`
	LastAttempt *time.Time          `json:"last_attempt"`
	Priority    int                 `json:"priority"`
}

// We are using the term `Event` as it represents an event in the
// event_queue table, but the table is named event_queue
// to signify it's usage as a queue
func (Event) TableName() string {
	return "event_queue"
}

func (t *Event) SetError(err string) {
	t.Error = &err
}

type Events []Event

// Recreate creates the given failed events in batches after updating the
// attempts count.
func (events Events) Recreate(ctx context.Context, tx *gorm.DB) error {
	if len(events) == 0 {
		return nil
	}

	var batch Events
	for _, event := range events {
		batch = append(batch, Event{
			Name:        event.Name,
			Properties:  event.Properties,
			Error:       event.Error,
			Attempts:    event.Attempts + 1,
			LastAttempt: event.LastAttempt,
			Priority:    event.Priority,
		})
	}

	return tx.CreateInBatches(batch, 100).Error
}

func (e Event) PK() string {
	return e.ID.String()
}

type EventQueueSummary struct {
	Name          string     `json:"name"`
	Pending       int64      `json:"pending"`
	Failed        int64      `json:"failed"`
	AvgAttempts   int64      `json:"average_attempts"`
	FirstFailure  *time.Time `json:"first_failure,omitempty"`
	LastFailure   *time.Time `json:"last_failure,omitempty"`
	MostCommonErr string     `json:"most_common_error,omitempty"`
}

func (t *EventQueueSummary) TableName() string {
	return "event_queue_summary"
}
