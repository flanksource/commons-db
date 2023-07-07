package models

import (
	"time"

	"github.com/flanksource/duty/types"
	"github.com/google/uuid"
	"github.com/lib/pq"
)

// Notification represents the notifications table
type Notification struct {
	ID        uuid.UUID      `json:"id"`
	Events    pq.StringArray `json:"events" gorm:"type:[]text"`
	Template  string         `json:"template"`
	Filter    string         `json:"filter"`
	PersonID  *uuid.UUID     `json:"person_id,omitempty"`
	TeamID    *uuid.UUID     `json:"team_id,omitempty"`
	Receivers types.JSON     `json:"receivers,omitempty"`
	CreatedBy uuid.UUID      `json:"created_by"`
	UpdatedAt time.Time      `json:"updated_at"`
	CreatedAt time.Time      `json:"created_at"`
	DeletedAt *time.Time     `json:"deleted_at"`
}
