package profiles

import (
	"iter"
	"net/http"
	"strconv"
	"strings"

	"github.com/flanksource/clicky/api"
	"github.com/flanksource/clicky/rpc"
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

	// pageable reports whether the profile can serve a position past its first
	// page. A response that cannot must not report one as available.
	pageable bool
	paging   query.PagingMode

	// diagnostics is set only for an info request, which runs the export it is
	// asked about purely to report what the provider was sent.
	diagnostics *query.ProviderDiagnostics
}

// pageRequest renders the transport request as the engine's page.
func (r exportRequest) pageRequest() query.PageRequest {
	page := query.PageRequest{Limit: r.limit, Offset: r.offset, Cursor: r.cursor, Diagnostics: r.diagnostics}
	if r.offset == 0 && r.cursor.IsZero() &&
		!r.paging.Supports(query.PagingOffset) && r.paging.Supports(query.PagingCursor) {
		page.Strategy = query.PagingCursor
	}
	if r.scope == "all" {
		// An export reads forward to the ceiling and never jumps, so it takes
		// the strategy that does not get more expensive the further it goes.
		//
		// It also waives the exact total and states its ceiling. Both exist for
		// the same reason: a backend asked for the size of the whole result has
		// to produce the whole result first, which is the difference between an
		// export that streams and one that hangs until the last row is found.
		// What is lost is X-Total-Count on a download — the trailer still reports
		// whether the ceiling bit, and X-Total-Relation says "unknown" rather
		// than implying a count nobody stated.
		page = query.PageRequest{
			Limit:       query.DefaultMaxPageSize,
			Strategy:    query.PagingCursor,
			Ceiling:     r.maxRows,
			SkipTotal:   true,
			Diagnostics: r.diagnostics,
		}
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
	// Streamable asks about the profile — a Top or a whole-result processor needs
	// every row before any row is correct. It does not ask whether the provider
	// underneath can hand back a page without producing the whole result, and a
	// walk is only forward if both are true. Getting this wrong is not cosmetic:
	// an all-row export of a provider that cannot page is a cursor walk it has to
	// refuse outright, and a page of one reports itself as a page that was read
	// when the whole query ran and the result was sliced.
	streamable = streamable && query.PagesNatively(p.Provider.Type)

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

	// ExecutePages applies the profile's processors itself, before row mapping
	// and the cursor it mints. A folding processor's carried state has to be
	// inside that token, so the chain cannot be wrapped around the walk here.
	pages := query.ExecutePages(ctx, p, request.pageRequest(), params)
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

	processed := query.ExecutePages(ctx, p, request.pageRequest(), params)
	pages, first, err := peekPages(processed)
	if err != nil {
		return exportResponse{}, err
	}
	// A backend that states its total has already answered whether the ceiling
	// will bite: it is arithmetic, not something to be discovered by reading to
	// the end. Doing it here is what turns the answer into a header the caller
	// can read rather than a trailer no browser exposes.
	truncated := first.Truncated
	if first.Total != nil && request.maxRows > 0 && first.Total.Value > int64(request.maxRows) {
		truncated = true
	}
	return exportResponse{
		rows:      query.Rows(pages),
		ceiling:   request.maxRows,
		mode:      "streaming",
		total:     first.Total,
		truncated: truncated,
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

// exportHeader is one paging fact a response carries, described once.
//
// Three consumers read this list and none of them may disagree: the
// Access-Control-Expose-Headers value (a header a browser cannot read is a
// header that does not exist), the OpenAPI response documentation (a contract
// only clicky-ui knows is a contract no generated client can honour), and the
// setters below.
type exportHeader struct {
	name        string
	description string
	kind        string
}

var exportHeaderSpecs = []exportHeader{
	{"Content-Disposition", "Attachment filename; present when ?_download or ?filename is given", "string"},
	{"X-Export-Mode", "How the rows were produced: page, buffered or streaming. Orthogonal to scope — a scope=page request against a profile that cannot stream reports buffered", "string"},
	{"X-Page-Limit", "Rows this page was limited to. Present when scope=page", "integer"},
	{"X-Page-Offset", "Rows skipped before this page. Present when scope=page and the profile can be paged", "integer"},
	{"X-Total-Count", "Size of the whole result set. Absent when the backend does not report one — read X-Total-Relation to tell that from zero", "integer"},
	{"X-Total-Relation", "How to read X-Total-Count: eq (exact), gte (lower bound), or unknown (the backend states no total)", "string"},
	{"X-Has-More", "Whether a further page can be requested. False for a profile with no total order, which serves its first page and refuses every page after it", "boolean"},
	{"X-Next-Cursor", "Opaque position resuming after this page. Present when scope=page and the provider issued one", "string"},
	{"X-Truncated", "The rows were cut short. Present when scope=all and the cut is known before the body; otherwise sent as the declared trailer", "boolean"},
	{"X-Max-Rows", "Ceiling this export was bounded by. Present when scope=all; a PDF reports its own lower ceiling", "integer"},
}

// exportHeaders are the header names, for Access-Control-Expose-Headers.
var exportHeaders = func() []string {
	names := make([]string, 0, len(exportHeaderSpecs))
	for _, spec := range exportHeaderSpecs {
		names = append(names, spec.name)
	}
	return names
}()

// exportResponseHeaders documents the same headers for the OpenAPI response.
func exportResponseHeaders() map[string]rpc.OpenAPIHeader {
	headers := make(map[string]rpc.OpenAPIHeader, len(exportHeaderSpecs))
	for _, spec := range exportHeaderSpecs {
		headers[spec.name] = rpc.OpenAPIHeader{
			Description: spec.description,
			Schema:      &rpc.OpenAPISchema{Type: spec.kind},
		}
	}
	return headers
}

// setCORSHeaders permits a cross-origin caller to read this response, whatever
// it turns out to be.
//
// It is deliberately independent of the response: none of it depends on the
// rows, so it is set before the first thing that can fail. Setting it only on
// success is what makes an error body unreadable in a browser — and an error
// nobody can read is worse than the one it describes.
func setCORSHeaders(w http.ResponseWriter) {
	header := w.Header()
	header.Set("Access-Control-Allow-Origin", "*")
	header.Set("Access-Control-Expose-Headers", strings.Join(exportHeaders, ", "))
}

func setExportHeaders(w http.ResponseWriter, r *http.Request, profileName string, request exportRequest, response exportResponse) {
	header := w.Header()
	setCORSHeaders(w)
	header.Set("Content-Type", exportContentType(request.format))
	header.Set("X-Export-Mode", response.mode)

	if request.scope == "page" {
		header.Set("X-Page-Limit", strconv.Itoa(request.limit))
		// A profile with no total order serves its first page and refuses every
		// page after it. Reporting rows behind that page would be true and
		// useless: the only request it invites is one this server answers 400.
		// Offset is withheld for the same reason — it names a position that
		// cannot be asked for.
		if request.pageable {
			if request.pageRequest().Mode() == query.PagingOffset {
				header.Set("X-Page-Offset", strconv.Itoa(request.offset))
			}
			header.Set("X-Has-More", strconv.FormatBool(response.hasMore))
			if response.next != "" {
				header.Set("X-Next-Cursor", string(response.next))
			}
		} else {
			header.Set("X-Has-More", "false")
		}
	} else {
		header.Set("X-Max-Rows", strconv.Itoa(request.maxRows))
		// Only a ceiling that might still bite needs a trailer. A buffered export
		// has none to hit, and one already known to have been cut has its answer
		// in the headers below — declaring a trailer for either costs the
		// response its Content-Length and promises an answer that never comes.
		//
		// A trailer is a poor last resort: browsers do not expose them, so this
		// reaches CLI and library callers only. It is kept for the one case that
		// is genuinely unknowable up front — a stream whose backend states no
		// total — rather than as the primary channel.
		if response.ceiling > 0 && !response.truncated {
			header.Set("Trailer", "X-Truncated")
		}
	}

	// A total the backend could not state exactly is a lower bound, and a caller
	// rendering it as a count would be reporting a number nobody promised. No
	// total at all is a third answer: without it, a missing X-Total-Count reads
	// the same as a zero one, so the relation is always stated.
	header.Set("X-Total-Relation", response.total.Relation())
	if response.total != nil {
		header.Set("X-Total-Count", strconv.FormatInt(response.total.Value, 10))
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
