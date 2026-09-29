// A bulk Writer over a store: applied directly by the owner, or published
// to the spool by a reader, which may exit before the owner ingests it.
package owner

import (
	"context"
	"errors"
	"sync"

	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/recordstore/spool"
)

// FlushOptions say what Flush waits for.
type FlushOptions struct {
	// Wait waits for the owner to apply every batch this writer published,
	// and reports the entries it refused. Without it Flush returns once the
	// batches are durable in the spool.
	Wait bool
}

// Writer gathers writes into batches for a store. It is not safe for
// concurrent use.
type Writer[T Target] struct {
	store  *Store[T]
	writer *spool.Writer

	mu      sync.Mutex
	pending []string
	refused []error
}

// Writer gathers writes of the kinds schema resolves into batches cut at
// options' caps; options' Producer, Schema and Deliver are the store's.
func (s *Store[T]) Writer(schema recordstore.SchemaResolver, options spool.WriterOptions) (*Writer[T], error) {
	writer := &Writer[T]{store: s}
	options.Producer, options.Schema, options.Deliver = s.producer, schema, writer.deliver
	delivering, err := spool.NewWriter(options)
	if err != nil {
		return nil, err
	}
	writer.writer = delivering
	return writer, nil
}

// deliver applies a cut batch as the owner, or publishes it as a reader.
func (w *Writer[T]) deliver(ctx context.Context, batch recordstore.Batch) error {
	if w.store.Role() == RoleOwner {
		backend, err := w.store.Backend()
		if err != nil {
			return err
		}
		result, err := backend.AppendBatch(ctx, batch)
		if err != nil {
			return err
		}
		w.record(result)
		return nil
	}
	if err := w.store.publish(batch); err != nil {
		return err
	}
	w.mu.Lock()
	w.pending = append(w.pending, batch.ID)
	w.mu.Unlock()
	return nil
}

// record keeps the entries result refused, for Flush to report.
func (w *Writer[T]) record(result recordstore.BatchResult) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, entry := range result.Entries {
		if entry.Error != nil {
			w.refused = append(w.refused, entry.Error)
		}
	}
}

// Append gathers rows for stream.
func (w *Writer[T]) Append(ctx context.Context, stream, kind string, rows []recordstore.Row) error {
	return w.writer.Append(ctx, stream, kind, rows)
}

// Seal gathers a seal of stream.
func (w *Writer[T]) Seal(ctx context.Context, stream string) error {
	return w.writer.Seal(ctx, stream)
}

// Flush delivers everything gathered. With options.Wait it waits for every
// published batch and returns the entries the owner refused, joined; the
// refusals of batches the owner applied directly are returned either way.
func (w *Writer[T]) Flush(ctx context.Context, options FlushOptions) error {
	if err := w.writer.Flush(ctx); err != nil {
		return err
	}
	if options.Wait {
		w.mu.Lock()
		pending := w.pending
		w.pending = nil
		w.mu.Unlock()
		for index, id := range pending {
			result, err := w.store.Await(ctx, id)
			if err != nil {
				w.mu.Lock()
				w.pending = append(pending[index:], w.pending...)
				w.mu.Unlock()
				return err
			}
			w.record(result)
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	refused := errors.Join(w.refused...)
	w.refused = nil
	return refused
}
