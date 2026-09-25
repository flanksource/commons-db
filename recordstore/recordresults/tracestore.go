package recordresults

import (
	"context"
	"errors"

	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
)

// TraceStore is an opened results store as the store a SQL Server trace
// (recordstore/tracing/sqltrace) commits its rows through. Rows go through its
// notifying backend, which wakes followers tailing the stream, and refs come from
// Results.Ref, which resolves an environment router to the store that really
// holds the stream.
type TraceStore struct{ results *Results }

// NewTraceStore wraps results, refusing one that is not open.
func NewTraceStore(results *Results) (TraceStore, error) {
	if results == nil || results.Backend == nil {
		return TraceStore{}, errors.New("sql trace record store: the trace results store is not open")
	}
	return TraceStore{results: results}, nil
}

func (s TraceStore) Append(ctx context.Context, stream, kind string, rows []recordstore.Row) (recordstore.AppendResult, error) {
	return s.results.Backend.Append(ctx, stream, kind, rows)
}

func (s TraceStore) Seal(ctx context.Context, stream string) error {
	return s.results.Backend.Seal(ctx, stream)
}

// EventsRef describes the window from..to of stream as a session status records it.
func (s TraceStore) EventsRef(ctx context.Context, stream string, from, to int64) (query.EventsRef, error) {
	ref, err := s.results.Ref(ctx, stream, from, to)
	if err != nil {
		return query.EventsRef{}, err
	}
	return *ref.EventsRef(), nil
}
