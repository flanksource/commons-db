package jfr

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/flanksource/commons-db/tracing"
	"github.com/flanksource/commons-db/tracing/jfr/decode"
)

// Sample is the record the jfr kind stores: a StackSample, truncated as the
// kind's processors cut it.
type Sample struct {
	tracing.Truncation
	decode.StackSample
}

// Engine is what records a JVM's profile into a JFR recording.
type Engine string

const (
	// EngineJFR is the JDK's own Flight Recorder (JDK 8u262 and later),
	// recording every profiling event its Settings enable.
	EngineJFR Engine = "jfr"
	// EngineAsyncProfiler is async-profiler in its jfr output mode, profiling
	// one Event per session.
	EngineAsyncProfiler Engine = "async-profiler"
)

// asyncProfilerEvents are the events async-profiler profiles, one at a time.
var asyncProfilerEvents = []string{decode.EventCPU, decode.EventWall, decode.EventAlloc, decode.EventLock}

// Options are what a capture records and how it reads the recording back.
type Options struct {
	Engine Engine
	// Event is the one event async-profiler profiles; the JDK's recorder
	// takes its events from Settings instead.
	Event string
	// Settings is the JDK recorder's predefined configuration, such as
	// "profile" or "default".
	Settings string
	// Interval is async-profiler's sampling interval; zero is its own. The
	// JDK recorder takes its periods from Settings.
	Interval time.Duration
	// Every dumps the recording at that interval while the capture runs; zero
	// dumps only at checkpoints and as the capture stops.
	Every time.Duration
	// Threads splits each stack's samples by the thread they were taken on.
	Threads bool
	// MaxStackDepth keeps only the leaf-most frames of deeper stacks; zero
	// keeps every frame the recorder recorded.
	MaxStackDepth int
}

// Validate refuses options a recording could not honour.
func (o Options) Validate() error {
	var errs []error
	switch o.Engine {
	case EngineJFR:
		if o.Event != "" {
			errs = append(errs, fmt.Errorf("event %q is async-profiler's: the jfr engine records the events its settings enable", o.Event))
		}
		if strings.TrimSpace(o.Settings) == "" {
			errs = append(errs, errors.New(`settings is required for the jfr engine: a predefined configuration such as "profile"`))
		}
		if o.Interval != 0 {
			errs = append(errs, errors.New("interval is async-profiler's: the jfr engine takes its sampling periods from its settings"))
		}
	case EngineAsyncProfiler:
		if !slices.Contains(asyncProfilerEvents, o.Event) {
			errs = append(errs, fmt.Errorf("event %q: async-profiler profiles one of %s", o.Event, strings.Join(asyncProfilerEvents, ", ")))
		}
		if o.Settings != "" {
			errs = append(errs, fmt.Errorf("settings %q are the jfr engine's: async-profiler profiles its one event", o.Settings))
		}
	default:
		errs = append(errs, fmt.Errorf("engine %q: expected %q or %q", o.Engine, EngineJFR, EngineAsyncProfiler))
	}
	if o.Interval < 0 {
		errs = append(errs, fmt.Errorf("interval %s is negative", o.Interval))
	}
	if o.Every < 0 {
		errs = append(errs, fmt.Errorf("every %s is negative", o.Every))
	}
	if o.MaxStackDepth < 0 {
		errs = append(errs, fmt.Errorf("maxStackDepth %d is negative", o.MaxStackDepth))
	}
	return errors.Join(errs...)
}

// String is what the recording profiles, as its session shows it.
func (o Options) String() string {
	parts := []string{string(o.Engine)}
	if o.Settings != "" {
		parts = append(parts, "settings="+o.Settings)
	}
	if o.Event != "" {
		parts = append(parts, "event="+o.Event)
	}
	if o.Interval > 0 {
		parts = append(parts, "interval="+o.Interval.String())
	}
	return strings.Join(parts, " ")
}

// Report is a capture's summary: the recording it dumped, how often and how
// much, the samples it stored by event, and its watermark.
type Report struct {
	Engine    Engine           `json:"engine"`
	Recording string           `json:"recording"`
	Dumps     int              `json:"dumps"`
	Bytes     int64            `json:"bytes"`
	Events    map[string]int64 `json:"events"`
	// Latest is the watermark: the latest event stored, after which the next
	// dump's events are new.
	Latest time.Time `json:"latest,omitzero"`
}
