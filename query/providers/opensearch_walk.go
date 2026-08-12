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

// drainOpenSearch reads every row of an index-backed provider into one slice.
//
// It prefers a cursor wherever the profile declares an order, for the same
// reason the engine's own full walk does: search_after is the only strategy
// that reads past the index result window, and a read of everything that
// stopped there would be a read of the first 10,000 rows reported as a failure
// — which is what a caller asking for all of an index used to get. An
// unordered profile has no position to resume from and is walked by from/size,
// so it is still held to the window and still says so.
func drainOpenSearch(ctx context.Context, provider query.PagingProvider, req query.ProviderRequest) ([]query.Row, error) {
	page := query.PageRequest{Limit: openSearchWalkBatch}
	if len(req.Order) > 0 {
		page.Strategy = query.PagingCursor
	}
	var result []query.Row
	for current, err := range provider.Pages(ctx, req, page) {
		if err != nil {
			return nil, err
		}
		result = append(result, current.Rows...)
	}
	return result, nil
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
		if current.HasMore && len(req.Order) > 0 && len(rows) > 0 {
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
	releasePIT := false
	if pit == "" {
		opened, err := w.searcher.OpenPIT(ctx, w.index, opensearch.DefaultPITKeepAlive)
		if err != nil {
			yield(query.Page{}, err)
			return
		}
		pit = opened

	}
	defer func() {
		if releasePIT {
			cleanup, cancel := context.NewContext(stdcontext.WithoutCancel(ctx.Context)).WithTimeout(10 * time.Second)
			defer cancel()
			if err := w.searcher.ClosePIT(cleanup, pit); err != nil {
				ctx.Warnf("failed to close opensearch point-in-time: %v", err)
			}
		}
	}()

	after := req.Position.Keys
	for {
		raw, rows, capped, err := w.search(ctx, openSearchPage{size: page.Limit, after: after, pit: pit})
		if err != nil {
			releasePIT = true
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
			current.HasMore = len(rows) == page.Limit
			if current.HasMore {
				current.NextKeys = keys
			}
		}
		releasePIT = !current.HasMore
		if !yield(current, nil) {
			return
		}
		if !current.HasMore {
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
		return opensearch.Response{}, nil, false,
			openSearchFailure(err, index, body, built.limitParam(), position.pit)
	}
	if position.size > 0 && len(raw.Hits.Hits) > position.size {
		raw.Hits.Hits = raw.Hits.Hits[:position.size]
		built.capped = true
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

// openSearchFailure attaches the search that failed to the error. OpenSearch
// answers a rejected query with a parse exception and a Java stack trace that
// names neither the index nor any part of the DSL it refused, so the request is
// the only thing that makes the message actionable.
func openSearchFailure(err error, index, body, limit, pit string) error {
	details := map[string]any{"index": index, "limit": limit}
	if pit != "" {
		details["pit"] = pit
	}
	diagnostics := query.NewProviderDiagnostics("opensearch", body, nil)
	diagnostics.RecordRequest(body, nil, details)
	return query.WithDiagnostics(err, diagnostics)
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
