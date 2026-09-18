package recordresults

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
)

// followBatchRows bounds the rows one follow read takes from the index, so a
// follow that starts far behind its stream catches up in reads that each hold
// the index lease briefly.
const followBatchRows = 500

// FollowRecheckInterval is how often a follow confirms, while nothing is
// appended, that its stream still exists. It bounds how long a follow of an
// expired or removed stream runs on; rows appended through Results.Backend
// arrive without waiting for it.
const FollowRecheckInterval = 5 * time.Second

// follow emits every row of the stream req names after its afterSeq — the rows
// it holds, then each row appended after — in seq order, through the query req
// carries: the followed profile's own statement and column filters, so a
// followed row is the row a page of the same seqs serves. It returns nil once
// ctx ends or it has read through an explicit toSeq or a sealed stream's high
// seq, and ErrNotFound when the stream stops existing.
func (r *Registry) follow(ctx dbcontext.Context, req query.ProviderRequest, emit func(query.Row)) error {
	stream, err := requestedStream(ProviderType, req.Params)
	if err != nil {
		return err
	}
	position, err := seqParam(req.Params, afterSeqParam)
	if err != nil {
		return err
	}
	through, err := seqParam(req.Params, toSeqParam)
	if err != nil {
		return err
	}
	source, err := r.followSource(ctx, stream)
	if err != nil {
		return err
	}
	req.Order = query.Order{{Column: seqColumn, Unique: true}}
	for position < through {
		read, err := r.readAfter(ctx, req, source, position)
		if err != nil {
			return err
		}
		for _, row := range read.rows {
			// The stream id is the index's key, a hidden column no page serves,
			// and every row of a follow is the stream the session names.
			delete(row, streamIDKey)
			emit(row)
		}
		position = read.covered
		if read.more || position >= through {
			continue
		}
		latest, err := r.notifier.Wait(ctx, stream, position, source.Generation)
		if err != nil {
			return notFound(err)
		}
		if latest.Sealed && latest.HighSeq <= position {
			return nil
		}
	}
	return nil
}

// followSource is the incarnation of stream a follow reads, which must hold a
// result type that follows.
func (r *Registry) followSource(ctx dbcontext.Context, stream string) (recordstore.Meta, error) {
	if r.notifier == nil {
		return recordstore.Meta{}, fmt.Errorf("follow stream %q: the registry's source does not notify appends", stream)
	}
	source, err := r.notifier.Meta(ctx, stream)
	if err != nil {
		return recordstore.Meta{}, notFound(fmt.Errorf("follow stream %q: %w", stream, err))
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	result, ok := r.results[r.prefix+"/"+source.Kind]
	if !ok {
		return recordstore.Meta{}, fmt.Errorf("follow stream %q: it holds %q results, which no result type serves", stream, source.Kind)
	}
	if result.profile.Provider.Type != ProviderType {
		return recordstore.Meta{}, fmt.Errorf("follow stream %q: %q results do not follow their streams", stream, source.Kind)
	}
	return source, nil
}

// followRead is one read of the index: its rows, the seq every row at or
// below which it has read, and whether rows past that remain.
type followRead struct {
	rows    []query.Row
	covered int64
	more    bool
}

// readAfter catches the index up and reads the rows after position through
// the followed query, under a lease held only for the read.
func (r *Registry) readAfter(ctx dbcontext.Context, req query.ProviderRequest, source recordstore.Meta, position int64) (followRead, error) {
	if err := r.indexer.Ensure(ctx, source.Stream); err != nil {
		return followRead{}, notFound(err)
	}
	release := r.index.Lease()
	defer release()
	indexed, err := r.index.Meta(ctx, source.Stream)
	if err != nil {
		return followRead{}, notFound(fmt.Errorf("follow stream %q: %w", source.Stream, err))
	}
	if indexed.Generation != source.Generation {
		return followRead{}, notFound(fmt.Errorf("follow stream %q: generation %q was replaced by %q: %w",
			source.Stream, source.Generation, indexed.Generation, recordstore.ErrNotFound))
	}
	index, err := sqliteProvider()
	if err != nil {
		return followRead{}, err
	}
	req.Position = query.CursorPosition{Keys: []any{position}}
	pageRequest := query.PageRequest{Limit: followBatchRows, Strategy: query.PagingCursor, SkipTotal: true}
	for page, err := range index.Pages(ctx, req, pageRequest) {
		if err != nil {
			return followRead{}, fmt.Errorf("follow stream %q: %w", source.Stream, err)
		}
		return pageRead(page, max(position, indexed.HighSeq))
	}
	return followRead{covered: max(position, indexed.HighSeq)}, nil
}

// pageRead is what one page read covers: through the index's high seq when it
// was the last page, else through its last row.
func pageRead(page query.Page, high int64) (followRead, error) {
	read := followRead{rows: page.Rows, covered: high, more: page.HasMore}
	if !page.HasMore {
		return read, nil
	}
	if len(page.Rows) == 0 {
		return followRead{}, errors.New("follow: the index reported more rows after an empty page")
	}
	last, err := seqValue(page.Rows[len(page.Rows)-1][seqColumn])
	if err != nil {
		return followRead{}, fmt.Errorf("follow: %w", err)
	}
	read.covered = last
	return read, nil
}

// seqParam reads a resolved seq param as the whole number it must be.
func seqParam(params map[string]any, name string) (int64, error) {
	value, ok := params[name]
	if !ok {
		return 0, fmt.Errorf("follow: the %s param was not resolved", name)
	}
	seq, err := seqValue(value)
	if err != nil {
		return 0, fmt.Errorf("follow: %s: %w", name, err)
	}
	return seq, nil
}

func seqValue(value any) (int64, error) {
	switch typed := value.(type) {
	case int64:
		return typed, nil
	case int:
		return int64(typed), nil
	case float64:
		if typed != math.Trunc(typed) || typed < 0 || typed >= math.MaxInt64 {
			return 0, fmt.Errorf("seq %v is not a whole number from 0 to %d", typed, int64(math.MaxInt64))
		}
		return int64(typed), nil
	case json.Number:
		return typed.Int64()
	default:
		return 0, fmt.Errorf("seq %v is a %T, not a number", value, value)
	}
}
