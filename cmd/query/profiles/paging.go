package profiles

import (
	"iter"
	"net/http"
	"strconv"
	"strings"

	"github.com/flanksource/clicky/api"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
)

// exportRequest is one export: which rows, in what shape, and how far.
type exportRequest struct {
	format string
	scope  string
	limit  int
	offset int
	cursor query.Cursor

	// maxRows is the profile's export ceiling, resolved alongside the page so
	// every response reports and enforces the same number.
	maxRows int
}

// pageRequest renders the transport request as the engine's page.
func (r exportRequest) pageRequest() query.PageRequest {
	page := query.PageRequest{Limit: r.limit, Offset: r.offset, Cursor: r.cursor}
	if r.scope == "all" {
		// An export reads forward to the ceiling and never jumps, so it takes
		// the strategy that does not get more expensive the further it goes.
		page = query.PageRequest{Limit: query.DefaultMaxPageSize, Strategy: query.PagingCursor}
	}
	return page
}

// exportResponse is everything the transport reports about the rows it is about
// to write. Every field is resolved before the first byte of the body, because
// a header cannot be corrected once the body has begun.
type exportResponse struct {
	rows iter.Seq2[query.Row, error]
	mode string

	total   *query.Total
	hasMore bool
	next    query.Cursor

	// ceiling stops an all-row export. Zero means the rows are already bounded.
	ceiling int

	// truncated reports a cut this response already knows about — the profile's
	// own size, or a cap the backend applied. The export ceiling is not known to
	// have bitten until the rows have been read, so it is reported as a trailer.
	truncated bool
}

// exportRows resolves the rows for one export request.
//
// A page is materialized before any header is written: it is bounded by the
// caller's own limit, so holding it costs a page and buys the ability to report
// the total, the next cursor and whether more exists — none of which can be
// corrected after the body has started.
func exportRows(
	ctx dbcontext.Context,
	p query.Profile,
	params map[string]any,
	request exportRequest,
) (exportResponse, error) {
	streamable, err := p.Streamable()
	if err != nil {
		return exportResponse{}, err
	}

	if request.scope == "all" {
		return exportAll(ctx, p, params, request, streamable)
	}
	return exportPage(ctx, p, params, request, streamable)
}

// exportPage materializes exactly the page asked for.
func exportPage(
	ctx dbcontext.Context,
	p query.Profile,
	params map[string]any,
	request exportRequest,
	streamable bool,
) (exportResponse, error) {
	pages := query.ExecutePages(ctx, p, request.pageRequest(), params)
	if !streamable {
		// A pipeline that needs every row before any row is correct cannot be
		// served a page at a time; the whole query runs and the page is cut
		// from the result. The mode says so, because the cost is real.
		result, err := query.Execute(ctx, p, params)
		if err != nil {
			return exportResponse{}, err
		}
		total := query.Total{Value: int64(len(result.Rows)), Exact: !result.Truncated}
		start := min(request.offset, len(result.Rows))
		end := min(start+request.limit, len(result.Rows))
		return exportResponse{
			rows:      query.Rows(query.SlicePages(result.Rows[start:end])),
			mode:      "buffered",
			total:     &total,
			hasMore:   end < len(result.Rows),
			truncated: result.Truncated,
		}, nil
	}

	for page, err := range pages {
		if err != nil {
			return exportResponse{}, err
		}
		return exportResponse{
			rows:      query.Rows(query.SlicePages(page.Rows)),
			mode:      "page",
			total:     page.Total,
			hasMore:   page.HasMore,
			next:      page.Next,
			truncated: page.Truncated,
		}, nil
	}
	return exportResponse{rows: query.Rows(query.SlicePages(nil)), mode: "page"}, nil
}

// exportAll streams to the export ceiling.
//
// The first page is read before the headers are written so the total can be
// reported up front: a caller about to download 100,000 of 5,000,000 rows is
// entitled to know that before the file arrives rather than after.
func exportAll(
	ctx dbcontext.Context,
	p query.Profile,
	params map[string]any,
	request exportRequest,
	streamable bool,
) (exportResponse, error) {
	if !streamable {
		result, err := query.Execute(ctx, p, params)
		if err != nil {
			return exportResponse{}, err
		}
		// The engine already stopped at this profile's export ceiling — the same
		// number this request reports — so there is nothing left to cut here.
		total := query.Total{Value: int64(len(result.Rows)), Exact: !result.Truncated}
		return exportResponse{
			rows:      query.Rows(query.SlicePages(result.Rows)),
			mode:      "buffered",
			total:     &total,
			truncated: result.Truncated,
		}, nil
	}

	pages, first, err := peekPages(query.ExecutePages(ctx, p, request.pageRequest(), params))
	if err != nil {
		return exportResponse{}, err
	}
	return exportResponse{
		rows:      query.Rows(pages),
		ceiling:   request.maxRows,
		mode:      "streaming",
		total:     first.Total,
		truncated: first.Truncated,
	}, nil
}

// peekPages reads the first page so its metadata can be reported before the
// body begins, and returns a sequence that still yields it.
func peekPages(pages iter.Seq2[query.Page, error]) (iter.Seq2[query.Page, error], query.Page, error) {
	next, stop := iter.Pull2(pages)
	first, err, ok := next()
	if err != nil {
		stop()
		return nil, query.Page{}, err
	}
	if !ok {
		stop()
		return query.SlicePages(nil), query.Page{}, nil
	}
	replayed := func(yield func(query.Page, error) bool) {
		defer stop()
		if !yield(first, nil) {
			return
		}
		for {
			page, err, ok := next()
			if !ok {
				return
			}
			if !yield(page, err) || err != nil {
				return
			}
		}
	}
	return replayed, first, nil
}

// collectBounded materializes rows up to ceiling and reports whether one
// existed past it. A caller that cannot stream — the CLI renders a table, so it
// holds every row — still has to be able to tell a finished read from a stopped
// one, and reading the row after the ceiling is the only way to know.
func collectBounded(rows iter.Seq2[query.Row, error], ceiling int) ([]query.Row, bool, error) {
	var out []query.Row
	for row, err := range rows {
		if err != nil {
			return nil, false, err
		}
		if ceiling > 0 && len(out) >= ceiling {
			return out, true, nil
		}
		out = append(out, row)
	}
	return out, false, nil
}

// profileClickyRows adapts the engine's push-style row sequence to clicky's
// pull-style iterator. Stopping the pull is what ends the underlying walk, so
// Close releases the backend cursor.
type profileClickyRows struct {
	next    func() (query.Row, error, bool)
	stop    func()
	columns []api.ColumnDef
	ceiling int

	row   query.Row
	err   error
	count int

	// overflowed reports that a row existed past the ceiling. It is what makes
	// a stopped export distinguishable from a finished one — the row itself is
	// read and discarded, because asking is the only way to know.
	overflowed bool
}

func newProfileClickyRows(rows iter.Seq2[query.Row, error], columns []api.ColumnDef, ceiling int) *profileClickyRows {
	next, stop := iter.Pull2(rows)
	return &profileClickyRows{next: next, stop: stop, columns: columns, ceiling: ceiling}
}

func (i *profileClickyRows) Columns() []api.ColumnDef { return i.columns }

func (i *profileClickyRows) Next() bool {
	if i.ceiling > 0 && i.count >= i.ceiling {
		_, err, ok := i.next()
		if err != nil {
			i.err = err
		}
		i.overflowed = ok
		return false
	}
	row, err, ok := i.next()
	if err != nil {
		i.err = err
		return false
	}
	if !ok {
		return false
	}
	i.row = row
	i.count++
	return true
}

func (i *profileClickyRows) Row() map[string]any { return i.row }
func (i *profileClickyRows) Err() error          { return i.err }
func (i *profileClickyRows) Close() error        { i.stop(); return nil }

// exportHeaders are the paging facts a response carries. They are listed once so
// the Access-Control-Expose-Headers value cannot drift from the headers that are
// actually set — a header a browser cannot read is a header that does not exist.
var exportHeaders = []string{
	"Content-Disposition",
	"X-Export-Mode",
	"X-Page-Limit",
	"X-Page-Offset",
	"X-Total-Count",
	"X-Total-Relation",
	"X-Has-More",
	"X-Next-Cursor",
	"X-Truncated",
	"X-Max-Rows",
}

func setExportHeaders(w http.ResponseWriter, r *http.Request, profileName string, request exportRequest, response exportResponse) {
	header := w.Header()
	header.Set("Access-Control-Allow-Origin", "*")
	header.Set("Access-Control-Expose-Headers", strings.Join(exportHeaders, ", "))
	header.Set("Content-Type", exportContentType(request.format))
	header.Set("X-Export-Mode", response.mode)

	if request.scope == "page" {
		header.Set("X-Page-Limit", strconv.Itoa(request.limit))
		header.Set("X-Page-Offset", strconv.Itoa(request.offset))
		header.Set("X-Has-More", strconv.FormatBool(response.hasMore))
		if response.next != "" {
			header.Set("X-Next-Cursor", string(response.next))
		}
	} else {
		header.Set("X-Max-Rows", strconv.Itoa(request.maxRows))
		// The ceiling is only known to have bitten once the rows have been read,
		// which is after the headers are gone. Declaring the trailer is what
		// keeps that answer reportable at all.
		header.Set("Trailer", "X-Truncated")
	}

	if response.total != nil {
		header.Set("X-Total-Count", strconv.FormatInt(response.total.Value, 10))
		// A total the backend could not state exactly is a lower bound, and a
		// caller rendering it as a count would be reporting a number nobody
		// promised.
		header.Set("X-Total-Relation", totalRelation(response.total.Exact))
	}
	if response.truncated {
		header.Set("X-Truncated", "true")
	}

	if r.URL.Query().Has("_download") || r.URL.Query().Get("filename") != "" {
		filename := r.URL.Query().Get("filename")
		if filename == "" {
			filename = profileName + exportExtension(request.format)
		}
		header.Set("Content-Disposition", "attachment; filename="+strconv.Quote(sanitizeExportFilename(filename)))
	}
}

func totalRelation(exact bool) string {
	if exact {
		return "eq"
	}
	return "gte"
}
