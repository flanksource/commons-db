package query

import (
	"encoding/json"
	"fmt"
	"slices"
	"time"
)

// KindCapture is the kind of a session a host drives itself (Track): it runs no
// profile, it records a capture the host performs.
const KindCapture ProfileKind = "capture"

// SessionStopping is the only non-terminal state after running: a stop was
// requested and the run is flushing and tearing down.
const SessionStopping SessionState = "stopping"

// SessionRole says whether a session creates data or only watches it.
type SessionRole string

const (
	// SessionRoleCapture creates data (a tracked profiler, a connection trace)
	// and is persisted when the registry has a SessionStore.
	SessionRoleCapture SessionRole = "capture"

	// SessionRoleView follows data that already exists (a browser live view).
	// It is never persisted.
	SessionRoleView SessionRole = "view"
)

// SessionSchemaVersion is the SessionStart.SchemaVersion this package writes.
const SessionSchemaVersion = 1

// SessionOwner identifies the process that owns a session's status writes.
type SessionOwner struct {
	Host string `json:"host"`
	PID  int    `json:"pid"`
	Boot string `json:"boot"`
}

// SessionStart is written once, when the session begins. Nothing modifies it.
type SessionStart struct {
	SchemaVersion int               `json:"schemaVersion"`
	ID            string            `json:"id"`
	Profile       string            `json:"profile"`
	Kind          ProfileKind       `json:"kind"`
	Role          SessionRole       `json:"role"`
	Params        map[string]any    `json:"params,omitempty"`
	Labels        map[string]string `json:"labels,omitempty"`
	Principal     string            `json:"principal,omitempty"`
	Owner         SessionOwner      `json:"owner"`
	RestartOf     string            `json:"restartOf,omitempty"`
	StartedAt     time.Time         `json:"startedAt"`
}

// EventsStoreLocation names where a session's canonical event rows live: the
// backend kind, and for a local file the host and file holding them. A kv
// stream has neither, and its JSON omits both, as the record-results
// StoreLocation's does.
type EventsStoreLocation struct {
	Backend string `json:"backend"`
	Host    string `json:"host,omitempty"`
	File    string `json:"file,omitempty"`
}

// EventsRef points at a session's canonical event rows in a record store. It
// mirrors the JSON of the record-results StreamRef, which converts to it; query
// cannot import that package.
type EventsRef struct {
	Stream     string              `json:"stream"`
	Kind       string              `json:"kind"`
	Generation string              `json:"generation"` // recordstore.Meta.Generation (a uuid)
	Low        int64               `json:"low"`
	High       int64               `json:"high"`
	From       int64               `json:"from"`
	To         int64               `json:"to"`
	Total      int64               `json:"total"`
	ExpiresAt  *time.Time          `json:"expiresAt,omitempty"`
	Store      EventsStoreLocation `json:"store"`
}

// SessionStatus is overwritten on every change by the owner, or by Sweep when
// the owner is gone.
type SessionStatus struct {
	State       SessionState    `json:"state"`
	Error       string          `json:"error,omitempty"`
	Warning     string          `json:"warning,omitempty"`
	EventCount  int64           `json:"eventCount"`
	UpdatedAt   time.Time       `json:"updatedAt"`
	HeartbeatAt time.Time       `json:"heartbeatAt"`
	StopAt      *time.Time      `json:"stopAt,omitempty"`
	StoppedAt   *time.Time      `json:"stoppedAt,omitempty"`
	StopReason  string          `json:"stopReason,omitempty"`
	Handle      string          `json:"handle,omitempty"`
	Events      *EventsRef      `json:"events,omitempty"`
	Summary     json.RawMessage `json:"summary,omitempty"`
	Result      json.RawMessage `json:"result,omitempty"`
}

// SessionRecord is what a SessionStore persists and returns.
type SessionRecord struct {
	SessionStart
	SessionStatus
}

// SessionInfo is the API shape: the record plus what the serving process
// derives. Its JSON is flat.
type SessionInfo struct {
	SessionRecord
	Controllable    bool `json:"controllable"`
	LocalWriter     bool `json:"localWriter"`
	EventsAvailable bool `json:"eventsAvailable"`
	Unresponsive    bool `json:"unresponsive"`
	// Restartable reports that this process can restart the ended session.
	Restartable bool `json:"restartable"`
	// RestartedAs names the sessions whose RestartOf is this one.
	RestartedAs []string `json:"restartedAs,omitempty"`
}

// SessionSortFields is the allowlist SessionFilter.Sort is checked against.
var SessionSortFields = []string{"startedAt", "stoppedAt", "updatedAt", "state", "profile", "principal", "eventCount"}

// SessionFilter selects and pages session records. Every []string field
// matches with `*` wildcards and `!` exclusions (collections.MatchItems); an
// empty slice matches everything.
type SessionFilter struct {
	IDs       []string
	Profile   []string
	Kind      []string
	Role      []string
	State     []string
	Principal []string
	RestartOf []string

	// Labels matches label values per key. A record without the key matches
	// only exclusion patterns.
	Labels map[string][]string

	// From and To bound StartedAt, inclusively; a zero time leaves that side
	// open.
	From, To time.Time

	// Sort is one of SessionSortFields; empty sorts by startedAt. The id is
	// always the secondary key, in the same direction.
	Sort string
	Desc bool

	// Limit caps the page (0 returns every match); Offset skips matches.
	Limit, Offset int

	// Allow authorizes a record's profile. It runs on every candidate before
	// totals and paging, so a caller never learns how many records it cannot
	// see. Nil allows every profile.
	Allow func(profile string) bool
}

// Validate rejects a sort outside the allowlist and negative paging.
func (f SessionFilter) Validate() error {
	if f.Sort != "" && !slices.Contains(SessionSortFields, f.Sort) {
		return fmt.Errorf("session sort %q is not one of %v", f.Sort, SessionSortFields)
	}
	if f.Limit < 0 || f.Offset < 0 {
		return fmt.Errorf("session page limit %d and offset %d must not be negative", f.Limit, f.Offset)
	}
	if !f.From.IsZero() && !f.To.IsZero() && f.To.Before(f.From) {
		return fmt.Errorf("session window to %s is before from %s", f.To.Format(time.RFC3339), f.From.Format(time.RFC3339))
	}
	return nil
}

// SessionPage is one page of filtered session records.
type SessionPage struct {
	Items []SessionRecord `json:"items"`
	// Total counts every authorized match, before Limit and Offset.
	Total int `json:"total"`
	// Shared reports whether the store is visible to other processes.
	Shared bool `json:"shared"`
}
