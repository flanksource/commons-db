package metrics

import (
	"context"
	"fmt"
	"github.com/flanksource/commons-db/recordstore"
	"maps"
)

// DynamicMetricSource prepares metric samples for atomic observation batches.
// The caller supplies a durable batch identity and may append sibling results.
type DynamicMetricSource struct {
	Table  *DynamicMetricTable
	Stream string
}

type AppendOptions struct {
	Backend recordstore.BatchAppender
	Batch   recordstore.Batch
	Samples []Sample
}

func (s DynamicMetricSource) Entry(sample Sample) (recordstore.BatchEntry, error) {
	if s.Table == nil {
		return recordstore.BatchEntry{}, fmt.Errorf("metric source requires a table")
	}
	if err := recordstore.ValidateStream(s.Stream); err != nil {
		return recordstore.BatchEntry{}, err
	}
	row, err := s.Table.Row(sample)
	if err != nil {
		return recordstore.BatchEntry{}, err
	}
	return recordstore.BatchEntry{Op: recordstore.BatchAppend, Kind: s.Table.kind, Stream: s.Stream, Rows: []recordstore.Row{row}}, nil
}

func (s DynamicMetricSource) Append(ctx context.Context, options AppendOptions) (recordstore.BatchResult, error) {
	if options.Backend == nil {
		return recordstore.BatchResult{}, fmt.Errorf("metric source requires a batch backend")
	}
	if s.Table == nil {
		return recordstore.BatchResult{}, fmt.Errorf("metric source requires a table")
	}
	s.Table.mu.Lock()
	defer s.Table.mu.Unlock()
	staged := &DynamicMetricTable{kind: s.Table.kind, limit: s.Table.limit, definitions: maps.Clone(s.Table.definitions)}
	source := DynamicMetricSource{Table: staged, Stream: s.Stream}
	batch := options.Batch
	rows := []recordstore.Row{}
	for _, sample := range options.Samples {
		entry, err := source.Entry(sample)
		if err != nil {
			return recordstore.BatchResult{}, err
		}
		rows = append(rows, entry.Rows...)
	}
	batch.Entries = append(batch.Entries, recordstore.BatchEntry{Op: recordstore.BatchAppend, Stream: s.Stream, Kind: staged.kind, Rows: rows})
	batch.Atomic = true
	batch.Schemas = append(batch.Schemas, staged.Schema())
	result, err := options.Backend.AppendBatch(ctx, batch)
	if err != nil {
		return result, err
	}
	for _, entry := range result.Entries {
		if entry.Error != nil {
			return result, entry.Error
		}
	}
	s.Table.definitions = staged.definitions
	return result, nil
}
