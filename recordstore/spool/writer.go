// The bulk Writer: a producer's writes gathered into batches cut at a row and
// byte cap, each delivered whole and in order.
package spool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/google/uuid"

	"github.com/flanksource/commons-db/recordstore"
)

// Producer numbers one process's batches: every batch it identifies gets the
// next seq, so an owner can ingest a producer's batches in order.
type Producer struct {
	mu       sync.Mutex
	identity recordstore.Producer
}

// NewProducer identifies the batches of this process as instance, made by
// build.
func NewProducer(instance, build string) *Producer {
	host, _ := os.Hostname()
	return &Producer{identity: recordstore.Producer{Instance: instance, PID: os.Getpid(), Host: host, Build: build}}
}

// Next is the identity of the producer's next batch.
func (p *Producer) Next() recordstore.Producer {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.identity.Seq++
	return p.identity
}

// WriterOptions configure a Writer.
type WriterOptions struct {
	// Producer identifies every batch the writer delivers. Required.
	Producer *Producer

	// Schema resolves the kinds appended to; a batch carries the schema of
	// every kind it appends to. Required.
	Schema recordstore.SchemaResolver

	// Deliver hands a batch on: Dir.Publisher, or a BatchAppender's
	// AppendBatch. A batch it fails is delivered again, with the same id and
	// seq, before any later one. Required.
	Deliver func(context.Context, recordstore.Batch) error

	// MaxRows and MaxBytes cap a batch's rows and their JSON size; zero is
	// 50,000 rows and 64MiB. A batch always takes at least one row.
	MaxRows  int
	MaxBytes int
}

// Writer gathers writes into batches. A write it has taken is delivered by a
// later Append that fills a batch or by Flush; a bulk write spanning batches
// is not atomic. It is not safe for concurrent use.
type Writer struct {
	options WriterOptions

	entries []recordstore.BatchEntry
	kinds   []recordstore.KindSchema
	rows    int
	bytes   int

	// ready are the batches cut and not yet delivered, oldest first.
	ready []recordstore.Batch
}

// NewWriter returns a Writer delivering through options.Deliver.
func NewWriter(options WriterOptions) (*Writer, error) {
	switch {
	case options.Producer == nil:
		return nil, errors.New("spool writer: a producer is required")
	case options.Schema == nil:
		return nil, errors.New("spool writer: a schema resolver is required")
	case options.Deliver == nil:
		return nil, errors.New("spool writer: a delivery function is required")
	}
	if options.MaxRows <= 0 {
		options.MaxRows = 50_000
	}
	if options.MaxBytes <= 0 {
		options.MaxBytes = 64 << 20
	}
	return &Writer{options: options}, nil
}

// Append gathers rows for stream, cutting and delivering a batch whenever one
// fills.
func (w *Writer) Append(ctx context.Context, stream, kind string, rows []recordstore.Row) error {
	if err := recordstore.ValidateAppend(stream, kind); err != nil {
		return err
	}
	schema, err := recordstore.ResolveKind(w.options.Schema, kind)
	if err != nil {
		return fmt.Errorf("spool writer: %w", err)
	}
	sizes := make([]int, len(rows))
	for index, row := range rows {
		encoded, err := json.Marshal(row)
		if err != nil {
			return fmt.Errorf("spool writer: stream %q row %d: %w", stream, index, err)
		}
		sizes[index] = len(encoded)
	}
	w.declare(schema)
	for len(rows) > 0 {
		taken := 0
		for taken < len(rows) && w.rows < w.options.MaxRows && (w.rows == 0 || w.bytes+sizes[taken] <= w.options.MaxBytes) {
			w.rows, w.bytes = w.rows+1, w.bytes+sizes[taken]
			taken++
		}
		if taken > 0 {
			w.entries = append(w.entries, recordstore.BatchEntry{Op: recordstore.BatchAppend, Stream: stream, Kind: kind, Rows: rows[:taken]})
			rows, sizes = rows[taken:], sizes[taken:]
		}
		if len(rows) > 0 {
			w.cut()
		}
	}
	if w.rows >= w.options.MaxRows || w.bytes >= w.options.MaxBytes {
		w.cut()
	}
	return w.deliver(ctx)
}

// Seal gathers a seal of stream, after the writes gathered before it.
func (w *Writer) Seal(ctx context.Context, stream string) error {
	if err := recordstore.ValidateStream(stream); err != nil {
		return err
	}
	w.entries = append(w.entries, recordstore.BatchEntry{Op: recordstore.BatchSeal, Stream: stream})
	return w.deliver(ctx)
}

// Flush delivers everything gathered.
func (w *Writer) Flush(ctx context.Context) error {
	w.cut()
	return w.deliver(ctx)
}

func (w *Writer) declare(schema recordstore.KindSchema) {
	for _, declared := range w.kinds {
		if declared.Kind == schema.Kind {
			return
		}
	}
	w.kinds = append(w.kinds, schema)
}

// cut makes the gathered entries the next batch to deliver.
func (w *Writer) cut() {
	if len(w.entries) == 0 {
		return
	}
	w.ready = append(w.ready, recordstore.Batch{
		ID: uuid.NewString(), Producer: w.options.Producer.Next(), Schemas: w.kinds, Entries: w.entries,
	})
	w.entries, w.kinds, w.rows, w.bytes = nil, nil, 0, 0
}

// deliver hands on every batch cut, oldest first, stopping at the first
// failure so the order holds.
func (w *Writer) deliver(ctx context.Context) error {
	for len(w.ready) > 0 {
		if err := w.options.Deliver(ctx, w.ready[0]); err != nil {
			return fmt.Errorf("spool writer: deliver batch %q: %w", w.ready[0].ID, err)
		}
		w.ready = w.ready[1:]
	}
	return nil
}
