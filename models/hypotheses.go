package models

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type Hypothesis struct {
	ID         uuid.UUID  `json:"id,omitempty" gorm:"default:generate_ulid()"`
	IncidentID uuid.UUID  `json:"incident_id,omitempty"`
	Type       string     `json:"type,omitempty"`
	Title      string     `json:"title,omitempty"`
	Status     string     `json:"status,omitempty"`
	ParentID   *uuid.UUID `json:"parent_id,omitempty"`
	TeamID     *uuid.UUID `json:"team_id,omitempty"`
	Owner      *uuid.UUID `json:"owner,omitempty"`
	CreatedAt  *time.Time `json:"created_at,omitempty"`
	UpdatedAt  *time.Time `json:"updated_at,omitempty"`
	CreatedBy  uuid.UUID  `json:"created_by,omitempty"`
}

func (Hypothesis) TableName() string {
	return "hypotheses"
}

func (h Hypothesis) AsMap() map[string]any {
	m := make(map[string]any)
	b, _ := json.Marshal(&h)
	_ = json.Unmarshal(b, &m)
	return m
}
