package query

import (
	"fmt"
	"time"

	"github.com/flanksource/commons-db/types"
)

// ProfileKind classifies how a Profile executes: a single-shot query, a
// long-running trace session, or an interval-sampled top session.
type ProfileKind string

const (
	KindQuery ProfileKind = "query"
	KindTrace ProfileKind = "trace"
	KindTop   ProfileKind = "top"
)

const (
	// DefaultMaxDuration bounds a trace or top session when the spec omits one.
	DefaultMaxDuration = 15 * time.Minute

	// DefaultMaxEvents caps a trace session's event ring buffer.
	DefaultMaxEvents = 10000

	// DefaultTopInterval is the sampling interval when a TopSpec omits one.
	DefaultTopInterval = 5 * time.Second

	// MinTopInterval is the floor for a TopSpec interval.
	MinTopInterval = time.Second
)

// TraceSpec declares a Profile as a trace: a long-running streaming session
// with explicit setup (start) and teardown (stop). The provider must implement
// StreamProvider.
type TraceSpec struct {
	// MaxDuration bounds the session; the server may clamp it lower.
	MaxDuration types.Duration `json:"maxDuration,omitempty" yaml:"maxDuration,omitempty"`

	// MaxEvents caps the in-memory event ring buffer.
	MaxEvents int `json:"maxEvents,omitempty" yaml:"maxEvents,omitempty"`
}

// DurationLimit returns MaxDuration, defaulted when unset.
func (s TraceSpec) DurationLimit() time.Duration {
	if s.MaxDuration.Duration <= 0 {
		return DefaultMaxDuration
	}
	return s.MaxDuration.Duration
}

// EventLimit returns MaxEvents, defaulted when unset.
func (s TraceSpec) EventLimit() int {
	if s.MaxEvents <= 0 {
		return DefaultMaxEvents
	}
	return s.MaxEvents
}

// TopSpec declares a Profile as a top: the engine re-executes the query on an
// interval and each tick replaces the previous snapshot. Any provider works.
type TopSpec struct {
	// Interval is the sampling period (default 5s, floor 1s).
	Interval types.Duration `json:"interval,omitempty" yaml:"interval,omitempty"`

	// MaxDuration bounds the session; the server may clamp it lower.
	MaxDuration types.Duration `json:"maxDuration,omitempty" yaml:"maxDuration,omitempty"`

	// SortBy names the column each snapshot is sorted by (descending).
	SortBy string `json:"sortBy,omitempty" yaml:"sortBy,omitempty"`

	// Limit truncates each snapshot after sorting.
	Limit int `json:"limit,omitempty" yaml:"limit,omitempty"`
}

// TickInterval returns Interval, defaulted and floored.
func (s TopSpec) TickInterval() time.Duration {
	if s.Interval.Duration <= 0 {
		return DefaultTopInterval
	}
	if s.Interval.Duration < MinTopInterval {
		return MinTopInterval
	}
	return s.Interval.Duration
}

// DurationLimit returns MaxDuration, defaulted when unset.
func (s TopSpec) DurationLimit() time.Duration {
	if s.MaxDuration.Duration <= 0 {
		return DefaultMaxDuration
	}
	return s.MaxDuration.Duration
}

// Kind derives the Profile's execution kind from its Trace/Top blocks.
func (p Profile) Kind() ProfileKind {
	switch {
	case p.Trace != nil:
		return KindTrace
	case p.Top != nil:
		return KindTop
	default:
		return KindQuery
	}
}

// ValidateKind rejects a Profile that declares both trace and top.
func (p Profile) ValidateKind() error {
	if p.Trace != nil && p.Top != nil {
		return fmt.Errorf("profile %q declares both trace and top; pick one", p.Name)
	}
	return nil
}
