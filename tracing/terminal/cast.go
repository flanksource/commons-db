package terminal

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"math"
	"time"

	"github.com/flanksource/commons-db/tracing"
)

// NoExitCode is the exit code of a window the session had not exited in, or
// whose host never reported one.
const NoExitCode = -1

// Cast is the cast format: asciinema v2, whose first line is a header object
// and every line after it one event array.
var Cast = tracing.ExportFormat{Name: "cast", Ext: "cast", MIME: "application/x-asciicast"}

// Formats are the formats the terminal kind exports in, beyond NDJSON.
func (terminal) Formats() []tracing.ExportFormat { return []tracing.ExportFormat{Cast} }

// Export writes the window's events as an asciinema v2 cast. The `_gavel`
// header object carries what asciinema has no field for; players ignore keys
// they do not know, so the cast stays playable. A resize is written as the
// format's "r" event.
func (terminal) Export(format string, rows iter.Seq[Event], w io.Writer) error {
	if format != Cast.Name {
		return fmt.Errorf("%w %q for trace kind %q", tracing.ErrUnknownFormat, format, Kind)
	}
	window, err := windowOf(rows)
	if err != nil {
		return err
	}
	if window.width <= 0 || window.height <= 0 {
		return fmt.Errorf("export cast: the window holds no terminal rows")
	}
	return writeCast(w, window)
}

// Summarize is the variables an assertion over the window reads: events,
// duration_ms, exit_code, width, height, final, snapshots (a count),
// duplicates ([{text, count}]), has_duplicates and truncated. Every key is
// present on an empty window.
func (terminal) Summarize(rows iter.Seq[Event]) (map[string]any, error) {
	window, err := windowOf(rows)
	if err != nil {
		return nil, err
	}
	duplicates := make([]map[string]any, 0, len(window.duplicates))
	for _, duplicate := range window.duplicates {
		duplicates = append(duplicates, map[string]any{"text": duplicate.Text, "count": duplicate.Count})
	}
	return map[string]any{
		"events":         len(window.events),
		"duration_ms":    window.duration.Milliseconds(),
		"exit_code":      window.exitCode,
		"width":          window.width,
		"height":         window.height,
		"final":          window.final,
		"snapshots":      len(window.snapshots),
		"duplicates":     duplicates,
		"has_duplicates": len(duplicates) > 0,
		"truncated":      window.truncated,
	}, nil
}

// castWindow is a window of rows as a cast reads it.
type castWindow struct {
	width, height int
	command       string
	exitCode      int
	duration      time.Duration
	events        []castEvent
	snapshots     []castSnapshot
	// final is the screen of the window's last snapshot, and duplicates the
	// lines it repeats: the tell-tale of a redraw that left stale content.
	final      string
	duplicates []DupLine
	truncated  bool
}

type castEvent struct {
	offset time.Duration
	kind   string
	data   string
}

type castSnapshot struct {
	offset time.Duration
	screen string
}

// MarshalJSON emits the [t_ms, screen] pair a dashboard scrubs, in the unit of
// the header's duration.
func (s castSnapshot) MarshalJSON() ([]byte, error) {
	return json.Marshal([]any{s.offset.Milliseconds(), s.screen})
}

// MarshalJSON emits asciinema's [time_seconds, kind, data] triple.
func (e castEvent) MarshalJSON() ([]byte, error) {
	return json.Marshal([]any{e.offset.Seconds(), e.kind, e.data})
}

// MarshalJSON emits the duplicate as an object, since an assertion reads it by name.
func (d DupLine) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"text": d.Text, "count": d.Count})
}

// offsetOf is a row's offset as the duration it was recorded from.
func offsetOf(seconds float64) time.Duration {
	return time.Duration(math.Round(seconds * float64(time.Second)))
}

// windowOf folds rows into the cast they record.
func windowOf(rows iter.Seq[Event]) (castWindow, error) {
	window := castWindow{exitCode: NoExitCode}
	var last time.Duration
	for row := range rows {
		offset := offsetOf(row.Offset)
		last = max(last, offset)
		if window.width == 0 {
			window.width, window.height = row.Width, row.Height
		}
		switch row.Kind {
		case KindOutput:
			window.events = append(window.events, castEvent{offset: offset, kind: KindOutput, data: row.Data})
		case KindResize:
			window.events = append(window.events, castEvent{offset: offset, kind: KindResize, data: fmt.Sprintf("%dx%d", row.Width, row.Height)})
		case KindSnapshot:
			window.snapshots = append(window.snapshots, castSnapshot{offset: offset, screen: row.Data})
			window.final = row.Data
		case KindExit:
			window.command, window.truncated, window.duration = row.Command, row.Truncated, offset
			if row.ExitCode != nil {
				window.exitCode = *row.ExitCode
			}
		default:
			return castWindow{}, fmt.Errorf("terminal row of unknown kind %q", row.Kind)
		}
	}
	if window.duration == 0 {
		window.duration = last
	}
	window.duration = window.duration.Truncate(time.Millisecond)
	if len(window.snapshots) > 0 {
		window.duplicates = duplicateSettledLines(window.final)
	}
	return window, nil
}

// castHeader is asciinema v2's first line.
type castHeader struct {
	Version  int        `json:"version"`
	Width    int        `json:"width"`
	Height   int        `json:"height"`
	Duration float64    `json:"duration"`
	Command  string     `json:"command,omitempty"`
	Gavel    castExtras `json:"_gavel"`
}

type castExtras struct {
	ExitCode   int            `json:"exit_code"`
	Snapshots  []castSnapshot `json:"snapshots,omitempty"`
	Final      string         `json:"final,omitempty"`
	Duplicates []DupLine      `json:"duplicates,omitempty"`
	Truncated  bool           `json:"truncated,omitempty"`
}

// writeCast serializes the window line-delimited, so a large cast streams into
// a player instead of being parsed whole.
func writeCast(w io.Writer, window castWindow) error {
	buffered := bufio.NewWriter(w)
	encoder := json.NewEncoder(buffered)
	header := castHeader{
		Version: 2, Width: window.width, Height: window.height,
		Duration: window.duration.Seconds(), Command: window.command,
		Gavel: castExtras{
			ExitCode: window.exitCode, Snapshots: window.snapshots, Final: window.final,
			Duplicates: window.duplicates, Truncated: window.truncated,
		},
	}
	if err := encoder.Encode(header); err != nil {
		return fmt.Errorf("write cast header: %w", err)
	}
	for _, event := range window.events {
		if err := encoder.Encode(event); err != nil {
			return fmt.Errorf("write cast event: %w", err)
		}
	}
	return buffered.Flush()
}
