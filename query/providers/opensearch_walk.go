package providers

import (
	stdcontext "context"
	"fmt"
	"time"

	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/logs/opensearch"
	"github.com/flanksource/commons-db/query"
)

// openSearchWalk pages an OpenSearch index. Both the log provider and the trace
// provider read the same index the same way and differ only in how they shape a
// hit into a row, so the walk is shared and the shaping is injected.
type openSearchWalk struct {
	searcher *opensearch.Searcher
	index    string

	// build renders the search body for one position.
	build func(openSearchPage) (openSearchRequest, error)

	// mapRows shapes a response's hits into rows, one row per hit and in hit
	// order.
	mapRows func(opensearch.Response) []query.Row
}

// run serves the strategy the request asked for.
//
// It is the request's choice rather than the profile's, because both are useful
// on the same profile: offset can jump to an arbitrary page and cursor cannot,
// while cursor reads past the index result window and offset cannot. A caller
// that wants page numbers near the front and a walk further in gets both by
// asking for both.
func (w openSearchWalk) run(
	ctx context.Context,
	req query.ProviderRequest,
	page query.PageRequest,
	yield func(query.Page, error) bool,
) {
	if page.Mode() == query.PagingCursor {
		if len(req.Order) == 0 {
			yield(query.Page{}, fmt.Errorf(
				"this profile declares no order, so a cursor has nothing to resume after; declare `order:` ending in a unique column"))
			return
		}
		w.byCursor(ctx, req, page, yield)
		return
	}
	w.byOffset(ctx, req, page, yield)
}

// byOffset serves from/size pages, bounded by the index result window.
//
// The window is reported rather than worked around. Reading past it used to
// switch the whole read to a scroll, which quietly changed both the cost and
// the consistency of a walk at a boundary no caller could see. An ordered
// profile still gets a cursor off every page, so a caller can carry on past the
// window by following it rather than by asking for a deeper offset.
func (w openSearchWalk) byOffset(
	ctx context.Context,
	req query.ProviderRequest,
	page query.PageRequest,
	yield func(query.Page, error) bool,
) {
	for from := page.Offset; ; from += page.Limit {
		if from+page.Limit > openSearchResultWindow {
			yield(query.Page{}, fmt.Errorf(
				"reading past row %d needs a cursor: OpenSearch refuses a from/size search beyond the index result window (%d); follow the cursor this profile returns with each page, or declare `order:` ending in a unique column so it can return one",
				openSearchResultWindow, openSearchResultWindow))
			return
		}
		raw, rows, capped, err := w.search(ctx, openSearchPage{from: from, size: page.Limit})
		if err != nil {
			yield(query.Page{}, err)
			return
		}
		current := query.Page{
			Rows:      rows,
			HasMore:   len(rows) == page.Limit && openSearchHasMore(raw, from+len(rows)),
			Total:     openSearchTotal(raw),
			Truncated: capped,
		}
		if len(req.Order) > 0 && len(rows) > 0 {
			keys, err := openSearchSortKeys(raw, req.Order)
			if err != nil {
				yield(query.Page{}, err)
				return
			}
			current.NextKeys = keys
		}
		if !yield(current, nil) || !current.HasMore {
			return
		}
	}
}

// openSearchSortKeys reads the position of a response's last hit, which is what
// the next page resumes after.
func openSearchSortKeys(raw opensearch.Response, order query.Order) ([]any, error) {
	last := raw.Hits.Hits[len(raw.Hits.Hits)-1]
	if len(last.Sort) != len(order) {
		return nil, fmt.Errorf(
			"opensearch returned %d sort values for a %d column order; a cursor cut from them would resume in the wrong place",
			len(last.Sort), len(order))
	}
	return last.Sort, nil
}

// byCursor serves search_after pages over a point-in-time, so every page of one
// walk reads the same view of the index.
func (w openSearchWalk) byCursor(
	ctx context.Context,
	req query.ProviderRequest,
	page query.PageRequest,
	yield func(query.Page, error) bool,
) {
	pit := req.Position.PIT
	if pit == "" {
		opened, err := w.searcher.OpenPIT(ctx, w.index, opensearch.DefaultPITKeepAlive)
		if err != nil {
			yield(query.Page{}, err)
			return
		}
		pit = opened

		// The walk that opened the point-in-time owns it, so ending the range
		// releases it even when the consumer stopped after one page. A resumed
		// walk was handed its PIT and leaves it alone.
		defer func() {
			cleanup, cancel := context.NewContext(stdcontext.WithoutCancel(ctx.Context)).WithTimeout(10 * time.Second)
			defer cancel()
			if err := w.searcher.ClosePIT(cleanup, pit); err != nil {
				ctx.Warnf("failed to close opensearch point-in-time: %v", err)
			}
		}()
	}

	after := req.Position.Keys
	for {
		raw, rows, capped, err := w.search(ctx, openSearchPage{size: page.Limit, after: after, pit: pit})
		if err != nil {
			yield(query.Page{}, err)
			return
		}

		current := query.Page{
			Rows:      rows,
			Total:     openSearchTotal(raw),
			Truncated: capped,
			PIT:       pit,
		}
		if len(rows) > 0 {
			keys, err := openSearchSortKeys(raw, req.Order)
			if err != nil {
				yield(query.Page{}, err)
				return
			}
			after = keys
			current.NextKeys = keys
			current.HasMore = len(rows) == page.Limit
		}
		if !yield(current, nil) || !current.HasMore {
			return
		}
	}
}

// search issues one search and maps its hits, keeping the raw response beside
// the rows so the sort values and the total survive the mapping.
func (w openSearchWalk) search(ctx context.Context, position openSearchPage) (opensearch.Response, []query.Row, bool, error) {
	built, err := w.build(position)
	if err != nil {
		return opensearch.Response{}, nil, false, err
	}
	body, err := built.encode()
	if err != nil {
		return opensearch.Response{}, nil, false, err
	}
	index := w.index
	if position.pit != "" {
		// A point-in-time already names the indices it was opened over.
		index = ""
	}
	raw, err := w.searcher.SearchRaw(ctx, opensearch.Request{
		Index: index,
		Query: body,
		Limit: built.limitParam(),
		PIT:   position.pit,
	})
	if err != nil {
		return opensearch.Response{}, nil, false, err
	}
	rows := w.mapRows(raw)

	// A cursor is cut from the raw hits by position, so a mapping that dropped
	// or reordered one would resume the walk somewhere other than where it
	// stopped — silently, and only for the rows that went missing.
	if len(rows) != len(raw.Hits.Hits) {
		return opensearch.Response{}, nil, false, fmt.Errorf(
			"opensearch returned %d hits but mapped to %d rows; their positions no longer line up",
			len(raw.Hits.Hits), len(rows))
	}
	return raw, rows, built.capped, nil
}

// openSearchTotal reads the hit count, carrying whether the index could state
// it exactly — past track_total_hits it reports a lower bound, and a caller
// shown that as a total would read "10000" where the truth is "at least 10000".
func openSearchTotal(raw opensearch.Response) *query.Total {
	if raw.Hits.Total.Value == 0 && raw.Hits.Total.Relation == "" {
		return nil
	}
	return &query.Total{
		Value: raw.Hits.Total.Value,
		Exact: raw.Hits.Total.Relation == "eq",
	}
}

// openSearchHasMore reports whether the index holds rows past the ones read so
// far. An inexact total cannot rule more out, so it does not.
func openSearchHasMore(raw opensearch.Response, read int) bool {
	total := openSearchTotal(raw)
	if total == nil || !total.Exact {
		return true
	}
	return int64(read) < total.Value
}
