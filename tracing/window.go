// Seq windows of a capture's stream: a host marks the seq a running capture
// has committed through, and reads, exports or summarizes exactly the records
// between two marks, whatever their clocks say.

package tracing

import (
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"reflect"

	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/recordstore/recordresults"
)

// ErrInvalidRef refuses a TraceRef that names no window.
var ErrInvalidRef = errors.New("invalid trace ref")

// TraceRef names a window of a capture's records: the rows of kind's stream
// after seq AfterSeq, through seq ToSeq. An AfterSeq of 0 starts at the
// stream's first row, a ToSeq of 0 runs to its last. A host takes both from
// Runtime.Mark. A kind whose later rows replace earlier ones, by key, moves a
// replaced row to the seq of its replacement, so a window holds the rows that
// last changed within it.
type TraceRef struct {
	Kind     string `json:"kind"`
	Stream   string `json:"stream"`
	AfterSeq int64  `json:"afterSeq,omitempty"`
	ToSeq    int64  `json:"toSeq,omitempty"`
}

// Validate refuses a ref without a kind or stream, or whose window holds a
// negative seq or ends before it starts.
func (r TraceRef) Validate() error {
	switch {
	case r.Kind == "" || r.Stream == "":
		return fmt.Errorf("%w: it needs a kind and a stream", ErrInvalidRef)
	case r.AfterSeq < 0 || r.ToSeq < 0:
		return fmt.Errorf("%w: window %d..%d holds a negative seq", ErrInvalidRef, r.AfterSeq, r.ToSeq)
	case r.ToSeq > 0 && r.ToSeq < r.AfterSeq:
		return fmt.Errorf("%w: window after %d through %d ends before it starts", ErrInvalidRef, r.AfterSeq, r.ToSeq)
	}
	return nil
}

// Params are the params of the kind's result profile, traces/<kind>, that
// read the ref's window.
func (r TraceRef) Params() map[string]any {
	return recordresults.WindowParams(r.Stream, r.AfterSeq, r.ToSeq)
}

// Mark checkpoints managed (Checkpoint) and returns the seq its stream has
// committed through: 0 for a stream that has committed nothing yet. Records
// the capture emitted before Mark are at or below it, so two marks bound a
// window exactly.
func (r *Runtime) Mark(ctx context.Context, managed *query.ManagedSession) (int64, error) {
	if err := r.Checkpoint(ctx, managed); err != nil {
		return 0, err
	}
	events := managed.Session().Snapshot().Events
	if events == nil {
		return 0, fmt.Errorf("mark session %s: it records no stream", managed.Session().ID())
	}
	meta, err := r.Results.Backend.Meta(ctx, events.Stream)
	if err != nil {
		return 0, fmt.Errorf("mark session %s: %w", managed.Session().ID(), err)
	}
	return meta.HighSeq, nil
}

// Read reads the rows of ref's window as stored, in seq order.
func (r *Runtime) Read(ctx context.Context, ref TraceRef, fn func(seq int64, row recordstore.Row) error) error {
	if _, err := r.resolve(ctx, ref); err != nil {
		return err
	}
	return scanWindow(ctx, r.Results.Backend, ref, fn)
}

// ReadRecords reads the records of ref's window as the kind's Records reads
// them back, refusing an R that is not the kind's record type.
func ReadRecords[R any](ctx context.Context, r *Runtime, ref TraceRef, fn func(R) error) error {
	plugin, err := r.resolve(ctx, ref)
	if err != nil {
		return err
	}
	if stored, want := plugin.recordType(), reflect.TypeFor[R](); stored != want {
		return fmt.Errorf("trace kind %q stores %s records, not %s", ref.Kind, stored, want)
	}
	records := storedRecords[R]{store: r.Results.Backend, stream: ref.Stream, jsonPaths: plugin.jsonFields()}
	return records.scanWindow(ctx, ref, func(_ int64, record R) error { return fn(record) })
}

// Export writes ref's window to w in format, one of its kind's ExportFormats.
func (r *Runtime) Export(ctx context.Context, ref TraceRef, format string, w io.Writer) error {
	plugin, err := r.resolve(ctx, ref)
	if err != nil {
		return err
	}
	return plugin.export(ctx, r.Results.Backend, ref, format, w)
}

// Summarize is the kind's summary of ref's window: what its Summarizer makes
// of the records, and always their count.
func (r *Runtime) Summarize(ctx context.Context, ref TraceRef) (map[string]any, error) {
	plugin, err := r.resolve(ctx, ref)
	if err != nil {
		return nil, err
	}
	return plugin.summarize(ctx, r.Results.Backend, ref)
}

// resolve is the plugin of ref's kind, once ref names a window of a stream of
// that kind: a stream of another kind is as good as missing.
func (r *Runtime) resolve(ctx context.Context, ref TraceRef) (TracePlugin, error) {
	if err := ref.Validate(); err != nil {
		return nil, err
	}
	plugin, ok := r.Kinds.Get(ref.Kind)
	if !ok {
		return nil, fmt.Errorf("%w %q", ErrUnknownKind, ref.Kind)
	}
	meta, err := r.Results.Backend.Meta(ctx, ref.Stream)
	if err != nil {
		return nil, fmt.Errorf("trace %s stream %q: %w", ref.Kind, ref.Stream, err)
	}
	if meta.Kind != ref.Kind {
		return nil, fmt.Errorf("trace stream %q holds %s records, not %s: %w", ref.Stream, meta.Kind, ref.Kind, recordstore.ErrNotFound)
	}
	return plugin, nil
}

// errWindowEnd ends a scan at the first row past its window.
var errWindowEnd = errors.New("end of the trace window")

// scanWindow scans the rows of ref's window, in seq order.
func scanWindow(ctx context.Context, store scanner, ref TraceRef, fn func(seq int64, row recordstore.Row) error) error {
	err := store.Scan(ctx, ref.Stream, ref.AfterSeq, func(seq int64, row recordstore.Row) error {
		if ref.ToSeq > 0 && seq > ref.ToSeq {
			return errWindowEnd
		}
		return fn(seq, row)
	})
	if errors.Is(err, errWindowEnd) {
		return nil
	}
	return err
}

// windowScan is what the latest pass over a window's records found: how many
// it yielded, whether it reached the window's end, and the error it ended on.
type windowScan struct {
	rows     int64
	complete bool
	err      error
}

// errStopped ends a scan whose consumer stopped iterating.
var errStopped = errors.New("trace window iteration stopped")

// window iterates ref's records, reporting each pass in its windowScan; the
// error of any pass is kept, so a summary never rests on a pass that failed.
func (r storedRecords[R]) window(ctx context.Context, ref TraceRef) (iter.Seq[R], *windowScan) {
	scan := &windowScan{}
	return func(yield func(R) bool) {
		scan.rows, scan.complete = 0, false
		err := r.scanWindow(ctx, ref, func(_ int64, record R) error {
			scan.rows++
			if !yield(record) {
				return errStopped
			}
			return nil
		})
		if errors.Is(err, errStopped) {
			return
		}
		scan.complete, scan.err = err == nil, errors.Join(scan.err, err)
	}, scan
}
