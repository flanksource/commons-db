package sqltrace

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/tracing/deadlocks"
	"github.com/flanksource/commons-db/tracing/xetrace"
)

// RecordStore is where a capture commits its rows. EventsRef describes a window
// of a stream the way the store holding it reports it, so a reader can replay
// the rows after the capturing process is gone.
type RecordStore interface {
	Append(ctx context.Context, stream, kind string, rows []recordstore.Row) (recordstore.AppendResult, error)
	Seal(ctx context.Context, stream string) error
	EventsRef(ctx context.Context, stream string, from, to int64) (query.EventsRef, error)
}

// recordAppender commits poll batches to one sql_xevent stream. ctx is the
// environment-routed context captured when the trace started, detached from its
// cancellation: the drain outlives the request that started it, and every row
// must reach the environment that request ran against.
type recordAppender struct {
	store  RecordStore
	ctx    context.Context
	stream string
	db     *sql.DB
}

// Append commits events as rows and returns the seq window they were numbered
// into, with the stream's ref as of that commit.
func (a recordAppender) Append(events []xetrace.Event) (recordstore.Window, query.EventsRef, error) {
	rows := make([]recordstore.Row, len(events))
	for index, event := range events {
		result := FromEvent(event)
		if event.Name == xetrace.EventXMLDeadlockReport {
			graph, err := deadlocks.DecodeReport(event.Timestamp, event.DeadlockReportXML)
			if err != nil {
				return recordstore.Window{}, query.EventsRef{}, fmt.Errorf("decode deadlock row %d of stream %s: %w", index, a.stream, err)
			}
			if a.db == nil {
				return recordstore.Window{}, query.EventsRef{}, fmt.Errorf("resolve deadlock row %d of stream %s: no database lease", index, a.stream)
			}
			graphs := []deadlocks.Graph{graph}
			if err := deadlocks.Resolve(a.ctx, a.db, graphs); err != nil {
				return recordstore.Window{}, query.EventsRef{}, fmt.Errorf("resolve deadlock row %d of stream %s: %w", index, a.stream, err)
			}
			graph = graphs[0]
			deadlocks.Analyze(&graph.Deadlock)
			result.Deadlock = &graph
			result.Database = graph.Database
			result.SQL = graph.VictimStatement
			result.Tables = graph.Objects
		}
		row, err := recordstore.EncodeRow(result)
		if err != nil {
			return recordstore.Window{}, query.EventsRef{}, fmt.Errorf("encode %s row %d of stream %s: %w", RowKind, index, a.stream, err)
		}
		rows[index] = row
	}
	appended, err := a.store.Append(a.ctx, a.stream, RowKind, rows)
	if err != nil {
		return recordstore.Window{}, query.EventsRef{}, fmt.Errorf("append %d %s row(s) to stream %s: %w", len(rows), RowKind, a.stream, err)
	}
	// Preview seqs are derived from the window, so a store that numbered fewer
	// rows than it was given would misattribute every one after the gap.
	if appended.Window.Len() != int64(len(rows)) {
		return recordstore.Window{}, query.EventsRef{}, fmt.Errorf("append %d %s row(s) to stream %s: the store numbered %d (window %d..%d)",
			len(rows), RowKind, a.stream, appended.Window.Len(), appended.Window.From, appended.Window.To)
	}
	ref, err := a.store.EventsRef(a.ctx, a.stream, 0, 0)
	if err != nil {
		return recordstore.Window{}, query.EventsRef{}, fmt.Errorf("describe stream %s after appending %d row(s): %w", a.stream, len(rows), err)
	}
	return appended.Window, ref, nil
}

// Seal declares the stream complete, so a follower that has read its last row
// stops waiting for more.
func (a recordAppender) Seal() error {
	if err := a.store.Seal(a.ctx, a.stream); err != nil {
		return fmt.Errorf("seal stream %s: %w", a.stream, err)
	}
	return nil
}
