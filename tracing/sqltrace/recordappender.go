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
			a.decodeDeadlock(event, &result)
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

// decodeDeadlock fills result with the decoded, index-resolved and classified
// graph of a deadlock report. A report that cannot be decoded is stored with its
// XML and the failure instead, and one whose indexes cannot be looked up is
// stored unresolved and naming why: either way it is one event's detail, and
// failing the append would stop the whole capture from recording.
func (a recordAppender) decodeDeadlock(event xetrace.Event, result *EventRow) {
	graph, err := deadlocks.DecodeReport(event.Timestamp, event.DeadlockReportXML)
	if err != nil {
		result.RawStatement = event.DeadlockReportXML
		result.ErrorMessage = fmt.Sprintf("decode deadlock report: %v", err)
		return
	}
	graphs := []deadlocks.Graph{graph}
	if a.db == nil {
		result.ErrorMessage = "resolve deadlock indexes: no database lease"
	} else if err := a.resolve(graphs); err != nil {
		result.ErrorMessage = fmt.Sprintf("resolve deadlock indexes: %v", err)
	}
	graph = graphs[0]
	deadlocks.Analyze(&graph.Deadlock)
	result.Deadlock = &graph
	result.Database = graph.Database
	result.SQL = graph.VictimStatement
	result.Tables = graph.Objects
}

// resolve looks up graphs' indexes within one poll timeout: a ctx is detached
// from cancellation, so a catalog query blocked on the server would otherwise
// hold the writer, and the stop that waits on it, indefinitely.
func (a recordAppender) resolve(graphs []deadlocks.Graph) error {
	ctx, cancel := context.WithTimeout(a.ctx, xetrace.PollTimeout())
	defer cancel()
	return deadlocks.Resolve(ctx, a.db, graphs)
}

// Seal declares the stream complete, so a follower that has read its last row
// stops waiting for more.
func (a recordAppender) Seal() error {
	if err := a.store.Seal(a.ctx, a.stream); err != nil {
		return fmt.Errorf("seal stream %s: %w", a.stream, err)
	}
	return nil
}
