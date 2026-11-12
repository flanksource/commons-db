// Package sampleframe publishes completed samples with their exact row windows.
package sampleframe

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
	"github.com/google/uuid"
)

type Frame struct {
	Error       string         `json:"error,omitempty"`
	Result      string         `json:"result,omitempty"`
	Tick        int64          `json:"tick,omitempty"`
	ID          string         `json:"id" filter:"exact"`
	Source      string         `json:"source" filter:"terms"`
	StartedAt   time.Time      `json:"startedAt"`
	CompletedAt time.Time      `json:"completedAt"`
	AfterSeq    int64          `json:"afterSeq"`
	ToSeq       int64          `json:"toSeq"`
	Observed    int            `json:"observed"`
	Retained    int            `json:"retained"`
	Limit       int            `json:"limit"`
	Summary     map[string]any `json:"summary,omitempty"`
}

type Backend interface {
	recordstore.BatchAppender
	Meta(context.Context, string) (recordstore.Meta, error)
}

type Writer struct {
	mu       sync.Mutex
	instance string
	sequence int64
	Backend  Backend
	Schema   recordstore.KindSchema
	Stream   string
}

func (w *Writer) Append(ctx context.Context, frame Frame, rows []recordstore.Row) (Frame, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.Backend == nil || w.Stream == "" || w.Schema.Options.Key != "" {
		return Frame{}, fmt.Errorf("sample writer requires a backend and exclusive ownership of an unkeyed stream")
	}
	if frame.Source == "" || frame.StartedAt.IsZero() || frame.CompletedAt.Before(frame.StartedAt) || frame.Observed < len(rows) || frame.Limit < 0 || (frame.Limit > 0 && len(rows) > frame.Limit) {
		return Frame{}, fmt.Errorf("sample requires a source, ordered time bounds and observed >= retained within its capture limit")
	}
	meta, err := w.Backend.Meta(ctx, w.Stream)
	if err != nil && !errors.Is(err, recordstore.ErrNotFound) {
		return Frame{}, err
	}
	frame.ID, frame.AfterSeq, frame.ToSeq, frame.Retained = uuid.NewString(), meta.HighSeq, meta.HighSeq+int64(len(rows)), len(rows)
	if w.instance == "" {
		w.instance = uuid.NewString()
	}
	schema, err := Schema(w.Schema.Kind + "_frames")
	if err != nil {
		return Frame{}, err
	}
	row, err := recordstore.EncodeRow(frame)
	if err != nil {
		return Frame{}, err
	}
	result, err := w.Backend.AppendBatch(ctx, recordstore.Batch{
		ID: frame.ID, Atomic: true, Producer: recordstore.Producer{Instance: w.instance, Seq: w.sequence + 1},
		Schemas: []recordstore.KindSchema{w.Schema, schema},
		Entries: []recordstore.BatchEntry{
			{Op: recordstore.BatchAppend, Stream: w.Stream, Kind: w.Schema.Kind, Rows: rows},
			{Op: recordstore.BatchAppend, Stream: w.Stream + "-frames", Kind: schema.Kind, Rows: []recordstore.Row{row}},
		},
	})
	if err != nil {
		return Frame{}, fmt.Errorf("publish sample: %w", err)
	}
	if len(result.Entries) != 2 {
		return Frame{}, fmt.Errorf("sample batch returned %d entries, expected 2", len(result.Entries))
	}
	for _, entry := range result.Entries {
		if entry.Error != nil {
			return Frame{}, fmt.Errorf("publish sample: %w", entry.Error)
		}
	}
	if result.Entries[0].Append == nil || result.Entries[0].Append.Window != (recordstore.Window{From: frame.AfterSeq + 1, To: frame.ToSeq}) {
		return Frame{}, fmt.Errorf("sample stream %q has another writer", w.Stream)
	}
	w.sequence++
	return frame, nil
}

// Close seals completion metadata after the final checkpoint has been flushed.
func (w *Writer) Close(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.instance == "" {
		w.instance = uuid.NewString()
	}
	schema, err := Schema(w.Schema.Kind + "_frames")
	if err != nil {
		return err
	}
	result, err := w.Backend.AppendBatch(ctx, recordstore.Batch{ID: uuid.NewString(), Atomic: true, Producer: recordstore.Producer{Instance: w.instance, Seq: w.sequence + 1},
		Schemas: []recordstore.KindSchema{schema}, Entries: []recordstore.BatchEntry{
			{Op: recordstore.BatchAppend, Stream: w.Stream + "-frames", Kind: schema.Kind},
			{Op: recordstore.BatchSeal, Stream: w.Stream + "-frames"},
		}})
	if err != nil {
		return err
	}
	for _, entry := range result.Entries {
		if entry.Error != nil {
			return entry.Error
		}
	}
	w.sequence++
	return nil
}

func Schema(kind string) (recordstore.KindSchema, error) {
	columns, err := query.ColumnsFor(reflect.TypeFor[Frame]())
	if err != nil {
		return recordstore.KindSchema{}, err
	}
	return recordstore.KindSchema{Kind: kind, Columns: columns, Options: recordstore.KindOptions{TimeColumn: "completedAt"}}, nil
}
