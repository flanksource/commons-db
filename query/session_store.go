package query

import (
	stdcontext "context"
	"sort"
	"strings"
	"time"

	"github.com/flanksource/commons/collections"
)

// SessionStore persists capture session records: an immutable start and a
// status the owning registry overwrites.
type SessionStore interface {
	// Begin writes a new record's start and status. A record that already
	// exists under the id is an error.
	Begin(ctx stdcontext.Context, rec SessionRecord) error

	// Update overwrites the status of an existing record.
	Update(ctx stdcontext.Context, id string, status SessionStatus) error

	// Get returns the record with the id, reporting whether it exists.
	Get(ctx stdcontext.Context, id string) (SessionRecord, bool, error)

	// List returns one page of records matching filter; see ApplySessionFilter.
	List(ctx stdcontext.Context, filter SessionFilter) (SessionPage, error)

	// Lineage maps each of ids to the ids of sessions restarted from it, within
	// the store's window.
	Lineage(ctx stdcontext.Context, ids []string) (map[string][]string, error)
}

// EventSink receives every event a capture session emits, in sequence.
type EventSink interface {
	// Append records one event. An error fails the session: a sink that
	// dropped an event holds a log the record's count no longer describes.
	Append(ctx stdcontext.Context, e Event) error

	// CloseSession reports that no further events will be appended for the
	// session,
	// so a sink that buffers must make what it holds durable before returning.
	//
	// It is called once per persisted session, when the session becomes
	// terminal and BEFORE its final status is written, so a reader that finds
	// the record finished finds every event it claims. A session whose record
	// was never begun is never closed — there is nothing to be consistent
	// with. An error fails the session, for the same reason Append's does.
	CloseSession(ctx stdcontext.Context, sessionID string) error
}

// ApplySessionFilter selects records with filter, authorizes them, sorts and
// pages them. Field filters run first, then Allow, then the total is taken, so
// the total and the page only ever describe records the caller may see. Stores
// that hold records in memory (or fetch a window of them) call it for List.
func ApplySessionFilter(records []SessionRecord, filter SessionFilter) (SessionPage, error) {
	if err := filter.Validate(); err != nil {
		return SessionPage{}, err
	}
	matched := make([]SessionRecord, 0, len(records))
	for _, rec := range records {
		if !filter.matches(rec) {
			continue
		}
		if filter.Allow != nil && !filter.Allow(rec.Profile) {
			continue
		}
		matched = append(matched, rec)
	}
	sortSessionRecords(matched, filter.Sort, filter.Desc)

	page := SessionPage{Total: len(matched), Items: []SessionRecord{}}
	start := min(filter.Offset, len(matched))
	end := len(matched)
	if filter.Limit > 0 {
		end = min(start+filter.Limit, len(matched))
	}
	page.Items = append(page.Items, matched[start:end]...)
	return page, nil
}

func (f SessionFilter) matches(rec SessionRecord) bool {
	fields := []struct {
		value    string
		patterns []string
	}{
		{rec.ID, f.IDs},
		{rec.Profile, f.Profile},
		{string(rec.Kind), f.Kind},
		{string(rec.Role), f.Role},
		{string(rec.State), f.State},
		{rec.Principal, f.Principal},
		{rec.RestartOf, f.RestartOf},
	}
	for _, field := range fields {
		if len(field.patterns) > 0 && !collections.MatchItems(field.value, field.patterns...) {
			return false
		}
	}
	for key, patterns := range f.Labels {
		if !matchLabel(rec.Labels, key, patterns) {
			return false
		}
	}
	if !f.From.IsZero() && rec.StartedAt.Before(f.From) {
		return false
	}
	return f.To.IsZero() || !rec.StartedAt.After(f.To)
}

// matchLabel matches a label's value; a missing label matches only a filter
// made entirely of exclusions, so `target=*` means "has a target".
func matchLabel(labels map[string]string, key string, patterns []string) bool {
	if len(patterns) == 0 {
		return true
	}
	value, ok := labels[key]
	if ok {
		return collections.MatchItems(value, patterns...)
	}
	for _, pattern := range patterns {
		if !strings.HasPrefix(strings.TrimSpace(pattern), "!") {
			return false
		}
	}
	return true
}

func sortSessionRecords(records []SessionRecord, field string, desc bool) {
	sort.SliceStable(records, func(i, j int) bool {
		order := compareSessionField(records[i], records[j], field)
		if order == 0 {
			order = strings.Compare(records[i].ID, records[j].ID)
		}
		if desc {
			return order > 0
		}
		return order < 0
	})
}

func compareSessionField(a, b SessionRecord, field string) int {
	switch field {
	case "", "startedAt":
		return a.StartedAt.Compare(b.StartedAt)
	case "stoppedAt":
		return optionalTime(a.StoppedAt).Compare(optionalTime(b.StoppedAt))
	case "updatedAt":
		return a.UpdatedAt.Compare(b.UpdatedAt)
	case "state":
		return strings.Compare(string(a.State), string(b.State))
	case "profile":
		return strings.Compare(a.Profile, b.Profile)
	case "principal":
		return strings.Compare(a.Principal, b.Principal)
	case "eventCount":
		return compareInt64(a.EventCount, b.EventCount)
	}
	// Validate rejected every other field before sorting began.
	panic("query: unvalidated session sort " + field)
}

// optionalTime orders a session that has not stopped before any that has.
func optionalTime(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

func compareInt64(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
