// A running capture as a probe.Source: the handler's processed records wait in
// a bounded buffer until the session's probe appends and commits them.

package traces

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/recordstore/probe"

	dbcontext "github.com/flanksource/commons-db/context"
)

// pageRows caps the rows one Read hands the probe.
const pageRows = 1000

// defaultBufferBytes is how many bytes of rows a capture holds unless its
// runtime says otherwise.
const defaultBufferBytes = 64 << 20

// errCaptureEnded refuses a record emitted after the capture's handler returned.
var errCaptureEnded = errors.New("the trace capture has ended")

// Summary counts what a capture's handler emitted: the records buffered for
// the store, those it lost before they got there, and the copies of a key one
// page collapsed, which the store never saw.
type Summary struct {
	Emitted      int64 `json:"emitted"`
	Deduplicated int64 `json:"deduplicated,omitempty"`
	Dropped      int64 `json:"dropped,omitempty"`
	Unencodable  int64 `json:"unencodable,omitempty"`
	Collapsed    int64 `json:"collapsed,omitempty"`
}

// source buffers the rows of one capture. rows[0] is the row at index base of
// the capture; base is how many rows the probe has committed.
type source struct {
	generation string
	capacity   int
	// maxBytes caps the bytes of the rows held, as rowBytes measures them;
	// a row larger than it is still taken while nothing else is held.
	maxBytes int
	// key is the kind's key column: a page never names one key twice,
	// which a store refuses in one append. replace says which copy a page
	// keeps: the last, for a kind whose later rows replace earlier ones,
	// otherwise the first, as the store would.
	key     string
	replace bool
	// drainLimit is the most rows one final drain can commit: the probe's
	// page budget for one sample. capacity never exceeds it.
	drainLimit int

	mu      sync.Mutex
	space   chan struct{}
	rows    []recordstore.Row
	sizes   []int
	bytes   int
	base    int64
	running bool
	frozen  bool
	summary Summary
	err     error

	ctx     dbcontext.Context
	capture func(dbcontext.Context) error
	release func() error
	cancel  context.CancelFunc
	done    chan struct{}
}

func newSource(generation string, capacity, maxBytes int, key string, replace bool) *source {
	drainLimit := probe.DefaultMaxPages * pageRows
	return &source{
		generation: generation, capacity: min(capacity, drainLimit), maxBytes: maxBytes, drainLimit: drainLimit,
		key: key, replace: replace,
		space: make(chan struct{}), running: true, done: make(chan struct{}),
	}
}

// prepare sets the capture start runs, under a context Freeze cancels, and
// the release that runs once the capture has ended.
func (s *source) prepare(ctx dbcontext.Context, capture func(dbcontext.Context) error, release func() error) {
	s.ctx, s.capture, s.release = ctx, capture, release
}

// start runs the capture on its own goroutine. It starts only once the
// session's stream exists, so a handler can read its own records from the
// first moment it runs. A capture that returns because it was cancelled ended
// normally.
func (s *source) start() {
	captureCtx, cancel := context.WithCancel(s.ctx)
	s.mu.Lock()
	s.cancel = cancel
	s.mu.Unlock()
	go func() {
		err := s.capture(s.ctx.Wrap(captureCtx))
		if captureCtx.Err() != nil && errors.Is(err, context.Canceled) {
			err = nil
		}
		s.end(err)
	}()
}

func (s *source) end(err error) {
	if s.release != nil {
		err = errors.Join(err, s.release())
	}
	s.mu.Lock()
	s.running, s.err = false, err
	s.signalSpaceLocked()
	s.mu.Unlock()
	close(s.done)
}

// push buffers row, waiting while the buffer is full. A frozen source does not
// wait, because nothing commits until its handler has returned: it takes rows
// until it holds what one final drain can commit, or its byte cap, and drops
// and counts the rest.
func (s *source) push(ctx context.Context, row recordstore.Row) error {
	size := rowBytes(row)
	s.mu.Lock()
	for s.running && !s.frozen && (len(s.rows) >= s.capacity || s.overBytes(size)) {
		space := s.space
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-space:
		}
		s.mu.Lock()
	}
	defer s.mu.Unlock()
	if !s.running {
		return errCaptureEnded
	}
	if s.frozen && (len(s.rows) >= s.drainLimit || s.overBytes(size)) {
		s.summary.Dropped++
		return nil
	}
	s.add(row, size)
	return nil
}

// tryPush buffers row only if there is room, counting it dropped otherwise.
func (s *source) tryPush(row recordstore.Row) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	limit := s.capacity
	if s.frozen {
		limit = s.drainLimit
	}
	size := rowBytes(row)
	if !s.running || len(s.rows) >= limit || s.overBytes(size) {
		s.summary.Dropped++
		return false
	}
	s.add(row, size)
	return true
}

// overBytes reports whether a row of size would take the rows held past the
// byte cap. A buffer holding nothing takes any row, so one larger than the cap
// is stored rather than waiting forever.
func (s *source) overBytes(size int) bool {
	return len(s.rows) > 0 && s.bytes+size > s.maxBytes
}

func (s *source) add(row recordstore.Row, size int) {
	s.rows = append(s.rows, row)
	s.sizes = append(s.sizes, size)
	s.bytes += size
	s.summary.Emitted++
}

// rowBytes estimates what row holds in memory: the length of its keys and
// text, and eight bytes for every other value.
func rowBytes(row recordstore.Row) int {
	return valueBytes(map[string]any(row))
}

func valueBytes(value any) int {
	switch value := value.(type) {
	case string:
		return len(value)
	case []byte:
		return len(value)
	case map[string]any:
		total := 0
		for key, item := range value {
			total += len(key) + valueBytes(item)
		}
		return total
	case []any:
		total := 0
		for _, item := range value {
			total += valueBytes(item)
		}
		return total
	default:
		return 8
	}
}

func (s *source) count(field *int64) {
	s.mu.Lock()
	*field++
	s.mu.Unlock()
}

func (s *source) signalSpaceLocked() {
	close(s.space)
	s.space = make(chan struct{})
}

// Read hands the probe the buffered rows from cursor on. It is idempotent
// until Commit: a retried Read returns the same rows.
func (s *source) Read(_ context.Context, cursor probe.Cursor) (probe.Batch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cursor.Next < s.base {
		return probe.Batch{}, fmt.Errorf("trace capture %s: cursor %d is before the committed %d", s.generation, cursor.Next, s.base)
	}
	write := s.base + int64(len(s.rows))
	next := min(write, cursor.Next+pageRows)
	return probe.Batch{
		Generation: s.generation, Next: next, Write: write, More: next < write, Active: s.running,
		Rows: s.distinct(s.rows[cursor.Next-s.base : next-s.base]), Summary: s.summary,
	}, nil
}

// distinct is a copy of page naming each key once, keeping the copy of a
// repeated key the store would end up holding.
func (s *source) distinct(page []recordstore.Row) []recordstore.Row {
	if s.key == "" {
		return append([]recordstore.Row(nil), page...)
	}
	kept := make([]recordstore.Row, 0, len(page))
	position := map[any]int{}
	for _, row := range page {
		key := row[s.key]
		if index, seen := position[key]; seen {
			if s.replace {
				kept[index] = row
			}
			continue
		}
		position[key] = len(kept)
		kept = append(kept, row)
	}
	return kept
}

// Commit releases the rows the probe has stored, making room for more. The
// page committed is the one Read handed out from base; the rows it spans but
// did not carry are the copies it collapsed. They are counted here rather
// than in Read, which a retry repeats.
func (s *source) Commit(_ context.Context, batch probe.Batch) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if batch.Next <= s.base {
		return nil
	}
	committed := int(batch.Next - s.base)
	s.summary.Collapsed += int64(committed - len(batch.Rows))
	for _, size := range s.sizes[:committed] {
		s.bytes -= size
	}
	s.rows = append([]recordstore.Row(nil), s.rows[committed:]...)
	s.sizes = append([]int(nil), s.sizes[committed:]...)
	s.base = batch.Next
	s.signalSpaceLocked()
	return nil
}

// Freeze ends the capture and waits for its handler to return, so the probe's
// drain after it reads every row the handler emitted.
func (s *source) Freeze(ctx context.Context) error {
	s.mu.Lock()
	s.frozen = true
	s.signalSpaceLocked()
	s.mu.Unlock()
	return s.wait(ctx)
}

func (s *source) Finalize(context.Context) (probe.Final, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var warnings []string
	if s.summary.Dropped > 0 {
		warnings = append(warnings, fmt.Sprintf("dropped %d records a full buffer could not take", s.summary.Dropped))
	}
	if s.summary.Unencodable > 0 {
		warnings = append(warnings, fmt.Sprintf("could not encode %d records", s.summary.Unencodable))
	}
	return probe.Final{Summary: s.summary, Warning: strings.Join(warnings, "; ")}, nil
}

// Release ends a capture that is still running, as when its session fails to
// arm after the capture opened.
func (s *source) Release(ctx context.Context) error {
	return s.wait(ctx)
}

// wait cancels the capture and waits for its handler to return. A capture
// that never started, because its session failed to arm, simply ends.
func (s *source) wait(ctx context.Context) error {
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	if cancel == nil {
		select {
		case <-s.done:
		default:
			s.end(nil)
		}
		return nil
	}
	cancel()
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("trace capture %s: the handler did not return: %w", s.generation, ctx.Err())
	}
}

// handlerErr is the error the capture's handler ended with.
func (s *source) handlerErr() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}
