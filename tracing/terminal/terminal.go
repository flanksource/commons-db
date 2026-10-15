// Package terminal is the terminal trace kind: it records a PTY session its
// host feeds it, and exports it as an asciinema v2 cast. The PTY stays in the
// host, which copies the bytes it reads into Writer; the capture settles the
// screen on an interval, as gavel's ANSI capture does, so the cast carries the
// timeline a viewer scrubs, the final screen and the lines it repeats.
package terminal

import (
	"errors"
	"fmt"
	"time"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/recordstore/recordresults"
	"github.com/flanksource/commons-db/tracing"
)

// Kind is the name the terminal kind is registered under.
const Kind = "terminal"

// Event kinds a capture records.
const (
	KindOutput   = "o"
	KindResize   = "r"
	KindSnapshot = "snapshot"
	KindExit     = "exit"
)

// Event is one row of a terminal session: a chunk of output, a resize, the
// settled screen at an instant, or the end of the session. Offset is the
// seconds since the first byte, the clock a cast plays on. Every row carries
// the terminal's size at that moment.
type Event struct {
	At     time.Time `json:"at" pretty:"label=At" sort:"at"`
	Offset float64   `json:"offset" pretty:"label=Offset" sort:"offset"`
	Kind   string    `json:"kind" filter:"terms"`
	// Data is the output of an "o" event and the screen of a "snapshot".
	Data   string `json:"data" pretty:"hide" filter:"none"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	// ExitCode is the host's report of how the session ended, on the "exit" row.
	ExitCode *int `json:"exitCode,omitempty"`
	// Truncated, on the "exit" row, says that output events were not recorded:
	// the size caps stopped them, or a full buffer dropped them. The screen is
	// still the true final one.
	Truncated bool `json:"truncated,omitempty"`
	// Command, on the "exit" row, is the command line the terminal ran, a
	// label for the cast's header.
	Command string `json:"command,omitempty"`
}

type terminal struct{}

func (terminal) Params() Params {
	return Params{
		Width: DefaultWidth, Height: DefaultHeight,
		SnapshotInterval: DefaultSnapshotInterval.String(), MaxBytes: DefaultMaxBytes,
	}
}

func (terminal) Schema() recordresults.ResultType[Event] {
	return recordresults.ResultType[Event]{
		Title: "Terminal", TimeColumn: "at", Follow: true, Compressed: []string{"data"},
	}
}

// Prepare registers the capture's sink before the session runs, so the first
// bytes the host writes are not missed. Its release unregisters the sink.
func (terminal) Prepare(ctx dbcontext.Context, params Params, records tracing.Emitter[Event]) (dbcontext.Context, func() error, error) {
	stream := tracing.StreamOf(ctx)
	maxBytes, err := params.byteCap()
	if err != nil {
		return ctx, nil, err
	}
	s := newSink(params, maxBytes, records)
	if err := register(stream, s); err != nil {
		return ctx, nil, err
	}
	return ctx.WithValue(sinkKey{}, s), func() error { unregister(stream, s); return nil }, nil
}

// Handle settles the screen every interval, and ends the capture once the
// host closes its writer, with the final screen and how the session exited.
func (terminal) Handle(ctx dbcontext.Context, params Params, records tracing.Emitter[Event], _ tracing.Records[Event]) error {
	s, ok := ctx.Value(sinkKey{}).(*sink)
	if !ok {
		return errors.New("terminal capture has no sink: Prepare did not run")
	}
	interval, err := params.interval()
	if err != nil {
		return err
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-s.done:
			return finish(ctx, s, records)
		case <-ticker.C:
			if snapshot, started := s.snapshot(); started {
				if err := records.Emit(ctx, snapshot); err != nil {
					return err
				}
			}
			records.Report(s.stats())
		}
	}
}

// finish records the final screen and the exit row, and reports the totals.
func finish(ctx dbcontext.Context, s *sink, records tracing.Emitter[Event]) error {
	final, exit := s.ending()
	for _, event := range []Event{final, exit} {
		if err := records.Emit(ctx, event); err != nil {
			return fmt.Errorf("record %s: %w", event.Kind, err)
		}
	}
	records.Report(s.stats())
	return nil
}

// NewKind is the terminal kind. A host registers it, starts a session of it,
// and feeds the session's stream through Writer.
func NewKind() *tracing.Handler[Params, Event] {
	return tracing.NewHandler[Params, Event](terminal{}, tracing.Capabilities{Live: true})
}
