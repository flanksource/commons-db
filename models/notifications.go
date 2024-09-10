package models

import (
	"fmt"
	"time"

	"github.com/flanksource/duty/types"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/samber/lo"
)

// Notification represents the notifications table
type Notification struct {
	ID             uuid.UUID           `json:"id"`
	Events         pq.StringArray      `json:"events" gorm:"type:[]text"`
	Title          string              `json:"title,omitempty"`
	Template       string              `json:"template,omitempty"`
	Filter         string              `json:"filter,omitempty"`
	PersonID       *uuid.UUID          `json:"person_id,omitempty"`
	TeamID         *uuid.UUID          `json:"team_id,omitempty"`
	Properties     types.JSONStringMap `json:"properties,omitempty"`
	Source         string              `json:"source"`
	RepeatInterval string              `json:"repeat_interval"`
	GroupBy        pq.StringArray      `json:"group_by" gorm:"type:[]text"`
	CustomServices types.JSON          `json:"custom_services,omitempty" gorm:"column:custom_services"`
	CreatedBy      *uuid.UUID          `json:"created_by,omitempty"`
	UpdatedAt      time.Time           `json:"updated_at" time_format:"postgres_timestamp" gorm:"<-:false"`
	CreatedAt      time.Time           `json:"created_at" time_format:"postgres_timestamp" gorm:"<-:false"`
	DeletedAt      *time.Time          `json:"deleted_at,omitempty"`

	// Error stores errors in notification filters (if any)
	Error *string `json:"error,omitempty"`
}

func (n Notification) TableName() string {
	return "notifications"
}

func (n Notification) PK() string {
	return n.ID.String()
}

func (n *Notification) HasRecipients() bool {
	return n.TeamID != nil || n.PersonID != nil || len(n.CustomServices) != 0
}

func (n Notification) AsMap(removeFields ...string) map[string]any {
	return asMap(n, removeFields...)
}

const (
	NotificationStatusError   = "error"
	NotificationStatusSent    = "sent"
	NotificationStatusSending = "sending"
)

type NotificationSendHistory struct {
	ID             uuid.UUID `json:"id,omitempty" gorm:"default:generate_ulid()"`
	NotificationID uuid.UUID `json:"notification_id"`
	Body           string    `json:"body,omitempty"`
	Error          *string   `json:"error,omitempty"`
	DurationMillis int64     `json:"duration_millis,omitempty"`
	CreatedAt      time.Time `json:"created_at" time_format:"postgres_timestamp"`
	Status         string    `json:"status,omitempty"`

	// Name of the original event that caused this notification
	SourceEvent string `json:"source_event"`

	// ID of the resource this notification is for
	ResourceID uuid.UUID `json:"resource_id"`

	// ID of the person this notification is for.
	PersonID *uuid.UUID `json:"person_id"`

	timeStart time.Time
}

func (n NotificationSendHistory) AsMap(removeFields ...string) map[string]any {
	return asMap(n, removeFields...)
}

func (t *NotificationSendHistory) TableName() string {
	return "notification_send_history"
}

func NewNotificationSendHistory(notificationID uuid.UUID) *NotificationSendHistory {
	return &NotificationSendHistory{
		NotificationID: notificationID,
		timeStart:      time.Now(),
	}
}

func (t *NotificationSendHistory) Sending() *NotificationSendHistory {
	t.Status = NotificationStatusSending
	return t
}

func (t *NotificationSendHistory) Sent() *NotificationSendHistory {
	t.Status = NotificationStatusSent
	return t.End()
}

func (t *NotificationSendHistory) Failed(e error) *NotificationSendHistory {
	t.Status = NotificationStatusError
	t.Error = lo.ToPtr(e.Error())
	return t.End()
}

func (t *NotificationSendHistory) End() *NotificationSendHistory {
	t.DurationMillis = time.Since(t.timeStart).Milliseconds()
	return t
}

type NotificationSilenceResource struct {
	ConfigID    *string `json:"config_id,omitempty"`
	CanaryID    *string `json:"canary_id,omitempty"`
	ComponentID *string `json:"component_id,omitempty"`
	CheckID     *string `json:"check_id,omitempty"`
}

func (t NotificationSilenceResource) Key() string {
	return fmt.Sprintf("%s:%s:%s:%s", lo.FromPtr(t.ConfigID), lo.FromPtr(t.CanaryID), lo.FromPtr(t.ComponentID), lo.FromPtr(t.CheckID))
}

type NotificationSilence struct {
	NotificationSilenceResource `json:",inline" yaml:",inline"`

	ID        uuid.UUID  `json:"id"`
	Namespace string     `json:"namespace"`
	From      time.Time  `json:"from"`
	Until     time.Time  `json:"until"`
	Source    string     `json:"source"`
	Recursive bool       `json:"recursive"`
	CreatedBy *uuid.UUID `json:"created_by,omitempty"`
	CreatedAt time.Time  `json:"created_at" time_format:"postgres_timestamp" gorm:"<-:false"`
	UpdatedAt time.Time  `json:"updated_at" time_format:"postgres_timestamp" gorm:"<-:false"`
	DeletedAt *time.Time `json:"deleted_at,omitempty"`
}

func (n NotificationSilence) AsMap(removeFields ...string) map[string]any {
	return asMap(n, removeFields...)
}

func (t *NotificationSilence) TableName() string {
	return "notification_silences"
}
