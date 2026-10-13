// Package jfr is a trace kind that profiles a JVM with a JFR recording, made by
// the JDK's Flight Recorder or by async-profiler, and stores what each dump of
// the recording adds as samples aggregated by stack.
package jfr

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sync"
	"time"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore/recordresults"

	"github.com/flanksource/commons-db/tracing/traces"
)

// maxStackBytes cuts a stored stack longer than this.
const maxStackBytes = 64 << 10

// Recordable is a params type that maps itself onto the recording it starts.
// The kind validates the options as the capture starts; a host's Validate
// should too, so a start it cannot honour is refused before a session exists.
type Recordable interface {
	JFROptions() (Options, error)
}

// Recorder starts recordings in the JVM a host profiles: it is the kind's
// transport, which a host provides.
type Recorder interface {
	// Start begins a recording named name with options.
	Start(ctx context.Context, options Options, name string) (Recording, error)
}

// Recording is a running recording.
type Recording interface {
	// Dump returns the whole recording so far as a JFR file, uncompressed:
	// one or more chunks, beginning with the "FLR\0" magic. The kind decodes
	// only the events that end after the previous dump's latest.
	Dump(ctx context.Context) ([]byte, error)
	// Stop ends the recording and frees what it holds in the JVM.
	Stop(ctx context.Context) error
	// Handle names the recording in the JVM; the session is named after it.
	Handle() string
	// Metadata describes how the recording was started, such as the commands
	// that started it.
	Metadata() []query.SessionMetadata
}

// running is what Prepare set up for Handle and Checkpoint: the recording,
// the watermark after which its next dump's events are new, and the samples
// decoded but not yet emitted.
type running struct {
	mu        sync.Mutex
	recording Recording
	options   Options
	watermark time.Time
	pending   []Sample
	report    Report
}

type runningKey struct{}

type profiler[P Recordable] struct {
	recorder Recorder
}

// Params start a capture from P's zero value; a host prefills its own form
// with Handler.WithDefaults.
func (profiler[P]) Params() P {
	var defaults P
	return defaults
}

// Schema follows a capture's samples while it runs, and searches their frames,
// stacks and threads.
func (profiler[P]) Schema() recordresults.ResultType[Sample] {
	return recordresults.ResultType[Sample]{
		Title: "JFR profile samples", TimeColumn: "timestamp", Follow: true,
		SearchColumns: []string{"frame", "stack", "thread"},
	}
}

// Prepare starts the recording before the session runs, so a JVM that cannot
// record refuses the start, and names the session after it. The release stops
// the recording; a stop that fails is the session's error.
func (p profiler[P]) Prepare(ctx dbcontext.Context, params P, records traces.Emitter[Sample]) (dbcontext.Context, func() error, error) {
	options, err := params.JFROptions()
	if err != nil {
		return ctx, nil, err
	}
	if err := options.Validate(); err != nil {
		return ctx, nil, err
	}
	name := traces.StreamOf(ctx)
	if name == "" {
		return ctx, nil, errors.New("jfr: the capture has no stream to name its recording after")
	}
	recording, err := p.recorder.Start(ctx, options, name)
	if err != nil {
		return ctx, nil, fmt.Errorf("start %s recording: %w", options.Engine, err)
	}
	handle := recording.Handle()
	records.Describe(handle, append([]query.SessionMetadata{
		{Name: "jfr.options", Label: "Profiling", Value: options.String()},
	}, recording.Metadata()...)...)
	release := func() error {
		if err := recording.Stop(context.WithoutCancel(ctx)); err != nil {
			return fmt.Errorf("stop JFR recording %q, which may still be running in the JVM: %w", handle, err)
		}
		return nil
	}
	return ctx.WithValue(runningKey{}, &running{
		recording: recording, options: options,
		report: Report{Engine: options.Engine, Recording: handle, Events: map[string]int64{}},
	}), release, nil
}

// Handle dumps the recording every Options.Every until the capture stops, then
// once more: the final dump's samples are emitted under a context the stop
// does not cancel. A dump that fails ends the periodic dumps, but not the
// final one.
func (profiler[P]) Handle(ctx dbcontext.Context, _ P, records traces.Emitter[Sample], _ traces.Records[Sample]) error {
	run := ctx.Value(runningKey{}).(*running)
	err := run.dumpEvery(ctx, records)
	return errors.Join(err, run.dump(context.WithoutCancel(ctx), records))
}

// Checkpoint dumps the recording, so the samples since the previous dump are
// committed with the checkpoint.
func (profiler[P]) Checkpoint(ctx dbcontext.Context, _ P, records traces.Emitter[Sample]) error {
	return ctx.Value(runningKey{}).(*running).dump(ctx, records)
}

// dumpEvery dumps every Options.Every until ctx is done. Each dump runs to its
// end under a context ctx's cancellation does not reach, so a stop never cuts
// one short.
func (r *running) dumpEvery(ctx context.Context, records traces.Emitter[Sample]) error {
	if r.options.Every <= 0 {
		<-ctx.Done()
		return nil
	}
	ticker := time.NewTicker(r.options.Every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := r.dump(context.WithoutCancel(ctx), records); err != nil {
				return err
			}
		}
	}
}

// dump decodes the events the recording added since the watermark, advances
// the watermark past them, emits their samples and reports the capture.
// Samples an emit whose context ended could not take are emitted first by the
// next dump, so none is stored twice or lost.
func (r *running) dump(ctx context.Context, records traces.Emitter[Sample]) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	data, err := r.recording.Dump(ctx)
	if err != nil {
		return errors.Join(fmt.Errorf("dump JFR recording %q: %w", r.report.Recording, err), r.flush(ctx, records))
	}
	r.report.Dumps++
	r.report.Bytes += int64(len(data))
	defer func() {
		report := r.report
		report.Events = maps.Clone(r.report.Events)
		records.Report(report)
	}()
	profile, err := Decode(data, DecodeOptions{After: r.watermark, Threads: r.options.Threads, MaxStackDepth: r.options.MaxStackDepth})
	if err != nil {
		return errors.Join(fmt.Errorf("JFR recording %q, dump %d: %w", r.report.Recording, r.report.Dumps, err), r.flush(ctx, records))
	}
	r.pending = append(r.pending, profile.Samples...)
	for event, samples := range profile.Counts {
		r.report.Events[event] += samples
	}
	if profile.Latest.After(r.watermark) {
		r.watermark, r.report.Latest = profile.Latest, profile.Latest
	}
	return r.flush(ctx, records)
}

// flush emits the decoded samples not yet emitted.
func (r *running) flush(ctx context.Context, records traces.Emitter[Sample]) error {
	for len(r.pending) > 0 {
		if err := records.Emit(ctx, r.pending[0]); err != nil {
			return err
		}
		r.pending = r.pending[1:]
	}
	r.pending = nil
	return nil
}

// NewKindFor is the jfr kind over recorder for a host's own params type, its
// stacks cut at 64KiB.
func NewKindFor[P Recordable](recorder Recorder) *traces.Handler[P, Sample] {
	return traces.NewHandler[P, Sample](profiler[P]{recorder: recorder}, traces.Capabilities{Live: true}).
		WithTruncation(maxStackBytes)
}
