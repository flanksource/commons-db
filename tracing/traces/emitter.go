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

func (e *emitter[R]) Emit(ctx context.Context, record R) error {
	row, ok, err := e.prepare(record)
	if !ok {
		return err
	}
	return e.source.push(ctx, row)
}

func (e *emitter[R]) TryEmit(record R) bool {
	row, ok, _ := e.prepare(record)
	return ok && e.source.tryPush(row)
}

// prepare turns record into the row to store, reporting false for a record
// its key's window already admitted, and for one that does not encode.
func (e *emitter[R]) prepare(record R) (recordstore.Row, bool, error) {
	if e.dedup != nil {
		e.dedupMu.Lock()
		admitted := e.dedup.admit(record)
		e.dedupMu.Unlock()
		if !admitted {
			e.source.count(&e.source.summary.Deduplicated)
			return nil, false, nil
		}
	}
	row, err := recordstore.EncodeRow(record)
	if err != nil {
		e.source.count(&e.source.summary.Unencodable)
		logger.Warnf("trace capture %s: %v", e.source.generation, err)
		return nil, false, err
	}
	return e.process(row), true, nil
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
