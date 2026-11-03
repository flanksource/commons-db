package query

import (
	"fmt"
	"strings"
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

	// Buffer batches raw provider rows before running the full processor chain.
	// Without it every trace processor must implement PageProcessor.
	Buffer *TraceBufferSpec `json:"buffer,omitempty" yaml:"buffer,omitempty"`

	// Follow marks a session promoted from a plain query profile by ?follow=true
	// rather than one an author declared. It is set by Follow and is not part of
	// the document, which is why it does not serialize: a profile cannot ask to
	// be followed, a request can.
	//
	// It exists because the two kinds want opposite things from a closing time
	// bound. A followed profile is being read from now onward, so an end instant
	// is the moment it would stop rather than a bound on what it reads, and
	// openTailWindow drops it. A declared trace is an author's whole statement of
	// what the session is, and silently discarding half of a window they wrote
	// down would be a worse answer than honouring it.
	Follow bool `json:"-" yaml:"-"`
}

// TraceBufferSpec bounds a raw-row processor batch by count, elapsed time, or
// both. When both are set, the first bound reached flushes the batch.
type TraceBufferSpec struct {
	MaxRows int            `json:"maxRows,omitempty" yaml:"maxRows,omitempty"`
	MaxWait types.Duration `json:"maxWait,omitempty" yaml:"maxWait,omitempty"`
}

func (s TraceBufferSpec) Validate() error {
	if s.MaxRows < 0 {
		return fmt.Errorf("maxRows must be positive, got %d", s.MaxRows)
	}
	if s.MaxWait.Duration < 0 {
		return fmt.Errorf("maxWait must be positive, got %s", s.MaxWait.Duration)
	}
	if s.MaxRows == 0 && s.MaxWait.Duration == 0 {
		return fmt.Errorf("at least one of maxRows or maxWait must be positive")
	}
	return nil
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

// profileError is err in the context of p, by its name; an unnamed profile,
// built in place by a caller that names its own source, leaves err as is.
func profileError(p Profile, err error) error {
	if p.Name == "" {
		return err
	}
	return fmt.Errorf("profile %q: %w", p.Name, err)
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

// ValidateQuerySource rejects a Profile that carries both a raw query and a
// structured search specification. The provider refuses the pair too, but that
// happens at execution — by then the profile is already stored, and the author
// who introduced the conflict is long gone.
func (p Profile) ValidateQuerySource() error {
	if p.Search != nil {
		if p.Provider.Type != "opensearch" && p.Provider.Type != "opentelemetry" {
			return fmt.Errorf("canonical search requires an OpenSearch or OpenTelemetry provider")
		}
		if strings.TrimSpace(p.Query) != "" || p.Provider.Options["search"] != nil {
			return fmt.Errorf("canonical search cannot be combined with a native query or provider.options.search")
		}
		if p.Search.TimeFieldFormat != "" {
			return fmt.Errorf("canonical timestamps use RFC3339; source units belong to the connection layout")
		}
		if err := p.Spans.ValidateReversible(); err != nil {
			return err
		}
		return p.Search.Validate()
	}
	if strings.TrimSpace(p.Query) == "" || p.Provider.Options["search"] == nil {
		return nil
	}
	return fmt.Errorf(
		"profile %q sets both query and provider.options.search; they are mutually exclusive, keep the structured search or the raw query, not both", p.Name)
}

// Validate rejects invalid execution and column presentation metadata.
func (p Profile) Validate() error {
	if p.RenderMode() == RenderTraces {
		if err := ValidateSpanOptions(p.Provider.Options); err != nil {
			return err
		}
	}
	if err := p.Spans.Validate(); err != nil {
		return profileError(p, err)
	}
	if err := p.ValidateKind(); err != nil {
		return err
	}
	if p.Trace != nil && p.Trace.Buffer != nil {
		if err := p.Trace.Buffer.Validate(); err != nil {
			return fmt.Errorf("profile %q: trace.buffer: %w", p.Name, err)
		}
	}
	if err := p.ValidateQuerySource(); err != nil {
		return err
	}
	if err := p.Limits.Validate(); err != nil {
		return profileError(p, err)
	}
	if err := p.Order.Validate(); err != nil {
		return profileError(p, err)
	}
	for index, processor := range p.Processors {
		if _, err := processor.Resolve(); err != nil {
			return fmt.Errorf("profile %q processor %d: %w", p.Name, index, err)
		}
	}
	for _, column := range p.Columns {
		if err := column.Validate(); err != nil {
			return fmt.Errorf("profile %q %w", p.Name, err)
		}
	}
	for index, filter := range p.Filters {
		if err := filter.validate(index); err != nil {
			return profileError(p, err)
		}
	}
	bindings, err := p.ColumnFilterBindings()
	if err != nil {
		return profileError(p, err)
	}
	runtimeBindings, err := p.RuntimeFilterBindings()
	if err != nil {
		return profileError(p, err)
	}
	bindings = append(bindings, runtimeBindings...)
	params := make(map[string]bool, len(p.Params))
	for _, parameter := range p.Params {
		params[parameter.Name] = true
	}
	// Checked before the general param rules so a name that shadows a real
	// binding keeps naming the binding it shadows.
	for _, binding := range bindings {
		if params[binding.Key] {
			return fmt.Errorf("profile %q parameter %q conflicts with native column filter", p.Name, binding.Key)
		}
	}
	return p.validateParams()
}
