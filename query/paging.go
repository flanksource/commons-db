package query

import (
	"fmt"
	"iter"
	"strings"

	"github.com/flanksource/commons-db/context"
)

// PagingMode is the set of paging strategies a provider can serve.
type PagingMode uint8

const (
	// PagingOffset skips rows by position. It is sound only under a total
	// order, and costs O(offset) against a backend that cannot push the skip
	// down — which it must be assumed cannot, since the alternative is
	// rewriting a query whose text belongs to its author.
	PagingOffset PagingMode = 1 << iota

	// PagingCursor resumes from the position carried by the previous page. It
	// requires a total order and is O(1) at any depth, which is what makes the
	// hundredth page cost the same as the first.
	PagingCursor
)

// Supports reports whether m can serve mode.
func (m PagingMode) Supports(mode PagingMode) bool { return m&mode != 0 }

// String names the modes for errors and for the API description.
func (m PagingMode) String() string {
	var modes []string
	if m.Supports(PagingOffset) {
		modes = append(modes, "offset")
	}
	if m.Supports(PagingCursor) {
		modes = append(modes, "cursor")
	}
	if len(modes) == 0 {
		return "none"
	}
	return strings.Join(modes, ",")
}

// PageRequest is the caller's position in an ordered result set.
//
// Cursor and Offset are mutually exclusive. A cursor already encodes where it
// resumes, so a request carrying both states its position twice, and the two
// statements can disagree — there is no reading of that request which is
// obviously right, so it is refused instead of resolved.
type PageRequest struct {
	// Limit is the most rows one Page may carry. Required and positive.
	Limit int

	// Offset skips rows from the start of the order. Offset paging only.
	Offset int

	// Cursor resumes immediately after the position it encodes. Cursor paging
	// only.
	Cursor Cursor

	// Strategy chooses how to page when the request does not already say.
	//
	// It is needed because the first page of a cursor walk carries no cursor,
	// and so is indistinguishable from an offset page at position zero. Zero
	// means offset, which is the strategy a caller gets by not thinking about
	// it — and the one that can jump to an arbitrary page.
	Strategy PagingMode

	// Ceiling bounds the whole walk rather than one page: an export reads
	// forward to it and stops. Zero leaves the walk unbounded. A provider that
	// can push it down to the backend should, so the read stops where the export
	// does instead of continuing past it into rows nobody will receive.
	Ceiling int

	// SkipTotal releases the provider from reporting the size of the whole
	// result, and a provider that takes it up must report no total rather than a
	// zero one — Total.Relation tells those apart, and a caller reading "exactly
	// 0" while rows stream past is worse served than one reading "unknown".
	//
	// It exists because stating an exact total can cost the whole result: see
	// buildPagedSQL, where it is the difference between a walk that streams and
	// one that materializes. A page cannot waive it — a table has to say what it
	// is a page of — so this is an export's trade to make.
	SkipTotal bool

	// Diagnostics is non-nil only for an explicit debug execution. It is not a
	// paging input and is excluded from cursor scope.
	Diagnostics *ProviderDiagnostics `json:"-"`
}

// Mode reports which strategy this request asks for.
func (r PageRequest) Mode() PagingMode {
	if !r.Cursor.IsZero() || r.Strategy == PagingCursor {
		return PagingCursor
	}
	return PagingOffset
}

// Validate rejects a request no provider could serve unambiguously.
func (r PageRequest) Validate() error {
	switch r.Strategy {
	case 0, PagingOffset, PagingCursor:
	default:
		return fmt.Errorf("a page is read by one strategy, got %s", r.Strategy)
	}
	if r.Limit <= 0 {
		return fmt.Errorf("page limit must be greater than zero, got %d", r.Limit)
	}
	if r.Offset < 0 {
		return fmt.Errorf("page offset must be zero or greater, got %d", r.Offset)
	}
	if r.Mode() == PagingCursor && r.Offset != 0 {
		return fmt.Errorf("a cursor already says where to resume, so it cannot be combined with offset %d", r.Offset)
	}
	if r.Ceiling < 0 {
		return fmt.Errorf("page ceiling must be zero or greater, got %d", r.Ceiling)
	}
	return nil
}

// Total is a row count and whether the source could state it exactly.
//
// Exactness is carried rather than assumed because one of the backends cannot
// promise it: past its tracking threshold OpenSearch reports a lower bound, and
// a caller shown that number as a total would be reading "10,000" where the
// truth is "at least 10,000". The distinction belongs to whoever renders it.
type Total struct {
	Value int64
	Exact bool
}

// Relation names how a caller may read Value: "eq" when the number is the
// count, "gte" when it is a lower bound, and "unknown" when there is no total
// at all.
//
// The nil case is the reason this is a method rather than a field: a missing
// total and a total of zero are different facts that serialize identically, and
// every surface that reports one has to say which it means.
func (t *Total) Relation() string {
	switch {
	case t == nil:
		return "unknown"
	case t.Exact:
		return "eq"
	default:
		return "gte"
	}
}

// Page is one batch of an ordered result set together with the position that
// resumes after it.
//
// A batch rather than a row, because a batch is the unit both backends actually
// fetch and the only place a cursor is well defined: a position between pages
// is something the source can hand back, whereas a position between rows of one
// fetch is something it would have to be asked to invent.
type Page struct {
	Rows []Row

	// Next resumes after this Page's last row. Empty when the source is
	// exhausted, and never set by a provider that cannot cursor.
	//
	// A provider does not set this: it sets NextKeys and the engine mints the
	// token, so the format and its validation stay in one place rather than in
	// every backend.
	Next Cursor

	// NextKeys are the ordered sort values of this Page's last row, in the
	// order's column order. A cursoring provider sets them; ExecutePages turns
	// them into Next.
	NextKeys []any

	// PIT is the backend snapshot this page was read from, when the provider
	// opened one. It travels inside Next so the next request lands on the same
	// snapshot rather than on the index as it has since become.
	PIT string

	// HasMore reports that the source had rows beyond this Page.
	//
	// It is the provider's answer rather than an inference from len(Rows),
	// because a short page and the end of the data are different facts that
	// look identical from the outside — and reading one as the other is how a
	// partial answer starts being presented as a complete one.
	HasMore bool

	// Total is the size of the whole result set when the source knows it.
	Total *Total

	// Truncated reports that the provider applied a cap of its own — a
	// configured limit, a backend default, a result-window ceiling — so a short
	// Page is never mistaken for the end of the data.
	//
	// This is distinct from HasMore: HasMore says the caller may ask for more,
	// Truncated says the source already decided not to give it. A caller that
	// asked for everything needs to know the difference.
	Truncated bool
}

type PageInfo struct {
	Mode          string `json:"mode"`
	Limit         int    `json:"limit"`
	Offset        int    `json:"offset,omitempty"`
	Cursor        Cursor `json:"cursor,omitempty"`
	NextCursor    Cursor `json:"nextCursor,omitempty"`
	HasMore       bool   `json:"hasMore"`
	Total         *int64 `json:"total,omitempty"`
	TotalRelation string `json:"totalRelation"`
	Consistency   string `json:"consistency"`
}

func NewPageInfo(request PageRequest, page Page) PageInfo {
	info := PageInfo{
		Mode: request.Mode().String(), Limit: request.Limit, Offset: request.Offset,
		Cursor: request.Cursor, NextCursor: page.Next, HasMore: page.HasMore,
		TotalRelation: page.Total.Relation(), Consistency: "live",
	}
	if page.Total != nil {
		total := page.Total.Value
		info.Total = &total
	}
	if page.PIT != "" {
		info.Consistency = "snapshot"
	}
	return info
}

// PagingProvider is the result contract implemented by every provider that can
// serve a page.
//
// Pages yields consecutive Pages starting at page and keeps yielding until the
// source is exhausted or the consumer stops ranging. One page and every page
// are therefore the same call — a caller wanting one breaks after the first —
// and ending the range is what releases the backend cursor, so the release
// cannot be forgotten the way an explicit Close can.
//
// Errors are yielded in band as the second value. The sequence yields at most
// one error and then stops, and a setup failure surfaces on the first
// iteration rather than through a second return value, so a consumer has one
// error path instead of two.
type PagingProvider interface {
	Provider

	Pages(ctx context.Context, req ProviderRequest, page PageRequest) iter.Seq2[Page, error]

	// PagingModes reports which strategies this provider can serve, so a
	// request for one it cannot is refused before it runs rather than quietly
	// answered with another.
	PagingModes() PagingMode
}

// Rows flattens pages for consumers that do not care about batch boundaries.
// Stopping the returned sequence stops the underlying one, and with it the
// backend cursor.
func Rows(pages iter.Seq2[Page, error]) iter.Seq2[Row, error] {
	return func(yield func(Row, error) bool) {
		for page, err := range pages {
			if err != nil {
				yield(nil, err)
				return
			}
			for _, row := range page.Rows {
				if !yield(row, nil) {
					return
				}
			}
		}
	}
}

// Limit stops a row sequence after n rows.
//
// It exists because PageRequest.Limit is a request, not a guarantee: a provider
// may round it up to a batch size or ignore it in favour of its own, so the
// ceiling a caller is entitled to is enforced here as well as asked for there.
func Limit(rows iter.Seq2[Row, error], n int) iter.Seq2[Row, error] {
	if n <= 0 {
		panic(fmt.Sprintf("Limit: n must be greater than zero, got %d", n))
	}
	return func(yield func(Row, error) bool) {
		remaining := n
		for row, err := range rows {
			if err != nil {
				yield(nil, err)
				return
			}
			if remaining == 0 {
				return
			}
			if !yield(row, nil) {
				return
			}
			remaining--
		}
	}
}

// SlicePages adapts a complete, already-materialized result to the contract —
// the buffered pipeline, and providers that cannot stream.
//
// The Total it reports is exact because the whole result is in hand. Callers
// holding only one page of a larger set must not use this: it would claim the
// page length as the size of everything.
func SlicePages(rows []Row) iter.Seq2[Page, error] {
	return func(yield func(Page, error) bool) {
		yield(Page{Rows: rows, Total: &Total{Value: int64(len(rows)), Exact: true}}, nil)
	}
}

// ErrorPage returns a sequence that yields nothing but err, so a provider can
// fail out of a setup step without a second return value.
func ErrorPage(err error) iter.Seq2[Page, error] {
	return func(yield func(Page, error) bool) { yield(Page{}, err) }
}

// CollectRows drains a row sequence, returning the first error it yields.
func CollectRows(rows iter.Seq2[Row, error]) ([]Row, error) {
	var collected []Row
	for row, err := range rows {
		if err != nil {
			return nil, err
		}
		collected = append(collected, row)
	}
	return collected, nil
}
