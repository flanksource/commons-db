// What a handler emits through and reads back from: the emitter dedups,
// encodes and processes each record into its source; the records scan its stream.

package traces

import (
	"context"
	"sync"

	"github.com/flanksource/commons/logger"

	"github.com/flanksource/commons-db/recordstore"
)

type emitter[R any] struct {
	source  *source
	process rowProcessor

	dedupMu sync.Mutex
	dedup   *deduplicator[R]
}

// Emit buffers record unless its key's window already admitted it. A record
// that does not encode, or that is not buffered before ctx ends, gives its
// key back, so a later copy of it is admitted.
func (e *emitter[R]) Emit(ctx context.Context, record R) error {
	key, admitted := e.reserve(record)
	if !admitted {
		return nil
	}
	row, err := e.encode(record)
	if err == nil {
		err = e.source.push(ctx, row)
	}
	if err != nil {
		e.forget(key)
	}
	return err
}

func (e *emitter[R]) TryEmit(record R) bool {
	key, admitted := e.reserve(record)
	if !admitted {
		return false
	}
	row, err := e.encode(record)
	if err != nil {
		// Nobody receives TryEmit's error, so the log is where it is seen.
		logger.Warnf("trace capture %s: %v", e.source.generation, err)
	}
	if err != nil || !e.source.tryPush(row) {
		e.forget(key)
		return false
	}
	return true
}

// reserve claims record's dedup key, counting a record its window already
// admitted. Every record is admitted when the kind deduplicates nothing.
func (e *emitter[R]) reserve(record R) (string, bool) {
	if e.dedup == nil {
		return "", true
	}
	e.dedupMu.Lock()
	key, admitted := e.dedup.reserve(record)
	e.dedupMu.Unlock()
	if !admitted {
		e.source.count(&e.source.summary.Deduplicated)
	}
	return key, admitted
}

func (e *emitter[R]) forget(key string) {
	if e.dedup == nil {
		return
	}
	e.dedupMu.Lock()
	e.dedup.forget(key)
	e.dedupMu.Unlock()
}

// encode turns record into the row to store, counting one that does not encode.
func (e *emitter[R]) encode(record R) (recordstore.Row, error) {
	row, err := recordstore.EncodeRow(record)
	if err != nil {
		e.source.count(&e.source.summary.Unencodable)
		return nil, err
	}
	return e.process(row), nil
}

// scanner reads a stream's committed rows.
type scanner interface {
	Scan(ctx context.Context, stream string, afterSeq int64, fn func(seq int64, row recordstore.Row) error) error
}

type storedRecords[R any] struct {
	store  scanner
	stream string
}

// Scan decodes every row the session has committed after afterSeq, as it was
// stored: masked and truncated values come back masked and truncated.
func (r storedRecords[R]) Scan(ctx context.Context, afterSeq int64, fn func(seq int64, record R) error) error {
	return r.store.Scan(ctx, r.stream, afterSeq, func(seq int64, row recordstore.Row) error {
		var record R
		if err := roundTrip(row, &record); err != nil {
			return err
		}
		return fn(seq, record)
	})
}
