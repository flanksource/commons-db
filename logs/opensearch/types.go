package opensearch

import (
	"time"

	"github.com/flanksource/commons-db/types"
)

// +kubebuilder:object:generate=true
type Backend struct {
	Address     string        `json:"address"`
	Username    *types.EnvVar `json:"username,omitempty"`
	Password    *types.EnvVar `json:"password,omitempty"`
	InsecureTLS bool          `json:"insecureTLS,omitempty"`
	// InspectionKey identifies the resolved connection without placing its URL
	// or credentials in metadata cache keys.
	InspectionKey string `json:"-"`
}

// DefaultPITKeepAlive is how long a point-in-time is held between pages. It
// only has to outlive the gap between one page and the next, not the whole
// walk: each search extends it.
const DefaultPITKeepAlive = time.Minute

const DefaultScrollKeepAlive = time.Minute

// +kubebuilder:object:generate=true
type Request struct {
	Index string `json:"index" template:"true"`
	Query string `json:"query" template:"true"`

	// Limit is how many documents to return. It is required — "0" asks for none,
	// which is what an aggregation-only search wants.
	Limit string `json:"limit,omitempty" template:"true"`

	// PIT pins the search to a point-in-time, so consecutive pages of one walk
	// read the same view of the index.
	PIT string `json:"-"`
}

type ScrollRequest struct {
	Request
	KeepAlive time.Duration
}

type ScrollPageRequest struct {
	ID        string
	KeepAlive time.Duration
}

type TotalHitsInfo struct {
	Value    int64  `json:"value"`
	Relation string `json:"relation"`
}

type HitsInfo struct {
	Total    TotalHitsInfo `json:"total"`
	MaxScore float64       `json:"max_score"`
	Hits     []SearchHit   `json:"hits"`
}

type Response struct {
	Took         float64        `json:"took"`
	TimedOut     bool           `json:"timed_out"`
	Hits         HitsInfo       `json:"hits"`
	ScrollID     string         `json:"_scroll_id,omitempty"`
	Aggregations map[string]any `json:"aggregations,omitempty"`
}

type SearchHit struct {
	Index  string         `json:"_index"`
	Type   string         `json:"_type"`
	ID     string         `json:"_id"`
	Score  float64        `json:"_score"`
	Sort   []any          `json:"sort"`
	Source map[string]any `json:"_source"`
	Fields map[string]any `json:"fields,omitempty"`
}

type Result struct {
	// Id is the unique identifier provided by the underlying system, use to link to a point in time of a log stream
	Id string `json:"id,omitempty"`
	// RFC3339 timestamp
	Time    string            `json:"timestamp,omitempty"`
	Message string            `json:"message,omitempty"`
	Labels  map[string]string `json:"labels,omitempty"`
}
