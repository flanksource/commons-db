package terminal

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/flanksource/commons-db/tracing"
)

// Stats are what a capture counts beyond the rows it stores.
type Stats struct {
	// Bytes is every byte the host wrote, recorded or not.
	Bytes int64 `json:"bytes"`
	// Dropped is the output and resize events a full buffer could not take.
	Dropped int64 `json:"dropped,omitempty"`
	// Truncated says the size caps or dropped events left output unrecorded.
	Truncated bool `json:"truncated,omitempty"`
}

type sinkKey struct{}

// sinks are the live captures, by the stream they write to: the process-wide
// registry a host reaches its capture through, as it does an HTTP observer.
var sinks = struct {
	sync.Mutex
	byStream map[string]*sink
}{byStream: map[string]*sink{}}

func register(stream string, s *sink) error {
	sinks.Lock()
	defer sinks.Unlock()
	if _, taken := sinks.byStream[stream]; taken {
		return fmt.Errorf("terminal capture %q is already registered", stream)
	}
	sinks.byStream[stream] = s
	return nil
}

func unregister(stream string, s *sink) {
	sinks.Lock()
	defer sinks.Unlock()
	if sinks.byStream[stream] == s {
		delete(sinks.byStream, stream)
	}
	s.release()
}

func lookup(stream string) (*sink, error) {
	sinks.Lock()
	defer sinks.Unlock()
	s, ok := sinks.byStream[stream]
	if !ok {
		return nil, fmt.Errorf("no terminal capture for stream %q", stream)
	}
	return s, nil
}

// Writer is where a host copies the bytes its PTY produces into the capture
// of stream, the stream the session's status names. It never blocks the copy:
// an event a full buffer cannot take is dropped and counted. Close it once the
// PTY is done, after SetExitCode, to end the capture.
func Writer(stream string) (io.WriteCloser, error) {
	s, err := lookup(stream)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// Resize tells the capture of stream that the terminal changed size.
func Resize(stream string, width, height int) error {
	if width <= 0 || height <= 0 {
		return fmt.Errorf("resize terminal %q: width and height must be > 0 (got %dx%d)", stream, width, height)
	}
	s, err := lookup(stream)
	if err != nil {
		return err
	}
	return s.resize(width, height)
}

// SetExitCode tells the capture of stream how its session exited; the writer's
// Close records it. A capture closed without one records no exit code.
func SetExitCode(stream string, code int) error {
	s, err := lookup(stream)
	if err != nil {
		return err
	}
	return s.setExitCode(code)
}

var errEnded = errors.New("terminal capture has ended")

// sink is one capture's host-fed side: it settles the screen and emits an event
// for each write on the host's goroutine.
type sink struct {
	records  tracing.Emitter[Event]
	maxBytes int64
	done     chan struct{}
	command  string

	mu       sync.Mutex
	viewport *screen
	width    int
	height   int
	origin   time.Time
	counts   Stats
	events   int
	exitCode *int
	closed   bool
	released bool
	final    Event
	exit     Event
}

func newSink(params Params, maxBytes int64, records tracing.Emitter[Event]) *sink {
	return &sink{
		records: records, maxBytes: maxBytes, done: make(chan struct{}),
		// A label a player prints, never re-executed, so the join needs no quoting.
		command:  strings.Join(params.Command, " "),
		viewport: newScreen(params.Width, params.Height), width: params.Width, height: params.Height,
	}
}

func (s *sink) offset() float64 {
	if s.origin.IsZero() {
		return 0
	}
	return time.Since(s.origin).Seconds()
}

func (s *sink) event(kind, data string) Event {
	return Event{At: time.Now(), Offset: s.offset(), Kind: kind, Data: data, Width: s.width, Height: s.height}
}

func (s *sink) live() error {
	if s.released || s.closed {
		return errEnded
	}
	return nil
}

// emit hands event to the buffer without waiting, counting what it drops.
func (s *sink) emit(event Event) {
	if s.records.TryEmit(event) {
		return
	}
	if s.counts.Dropped == 0 {
		s.records.Warn("terminal events were dropped: the capture's buffer is full")
	}
	s.counts.Dropped++
	s.counts.Truncated = true
}

// Write records p as an output event unless the caps have been reached; the
// screen settles every byte either way.
func (s *sink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.live(); err != nil {
		return 0, err
	}
	if s.origin.IsZero() {
		s.origin = time.Now()
	}
	s.viewport.Write(p)
	s.counts.Bytes += int64(len(p))
	if s.counts.Bytes > s.maxBytes || s.events >= maxEvents {
		s.counts.Truncated = true
		return len(p), nil
	}
	s.events++
	s.emit(s.event(KindOutput, string(p)))
	return len(p), nil
}

func (s *sink) resize(width, height int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.live(); err != nil {
		return err
	}
	s.width, s.height = width, height
	s.viewport.resize(width, height)
	if !s.origin.IsZero() {
		s.emit(s.event(KindResize, ""))
	}
	return nil
}

func (s *sink) setExitCode(code int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.live(); err != nil {
		return err
	}
	s.exitCode = &code
	return nil
}

// Close ends the capture: the session records the final screen and the exit
// row, and finishes. Closing twice is harmless.
func (s *sink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.released {
		return nil
	}
	s.closed = true
	s.final = s.event(KindSnapshot, s.viewport.String())
	s.exit = s.event(KindExit, "")
	s.exit.ExitCode, s.exit.Truncated, s.exit.Command = s.exitCode, s.counts.Truncated, s.command
	close(s.done)
	return nil
}

// release ends the writers' access once the session has stopped.
func (s *sink) release() {
	s.mu.Lock()
	s.released = true
	s.mu.Unlock()
}

// snapshot is the settled screen now, once the first byte has arrived.
func (s *sink) snapshot() (Event, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.origin.IsZero() || s.closed {
		return Event{}, false
	}
	return s.event(KindSnapshot, s.viewport.String()), true
}

// ending is the final screen and exit row Close recorded.
func (s *sink) ending() (final, exit Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.final, s.exit
}

func (s *sink) stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.counts
}
