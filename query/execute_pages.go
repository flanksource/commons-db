package query

import (
	"fmt"
	"iter"

	"github.com/flanksource/commons-db/context"
)

// walkBatchSize is how many rows one page of a full walk asks for. It is a
// transport detail rather than a cap a caller sees: a walk reads everything
// either way, and the batch only decides how often the backend is asked.
const walkBatchSize = DefaultMaxPageSize

// SupportsPaging reports which paging strategies the registered provider can
// serve natively. A provider with none is still pageable — the engine slices a
// buffered result for it — but only by running the whole query per page, which
// is why the distinction is worth being able to ask about.
func SupportsPaging(providerType string) PagingMode {
	provider, err := GetProvider(providerType)
	if err != nil {
		return 0
	}
	if paging, ok := provider.(PagingProvider); ok {
		return paging.PagingModes()
	}
	return PagingOffset
}

// PagesNatively reports whether this provider serves a page without producing
// the whole result first.
//
// SupportsPaging answers a different question — which strategies a caller may
// ask for — and answers PagingOffset for a provider that has no paging at all,
// because offset paging over a materialized result is still correct. Only this
// separates a walk that streams from one that buffers, which is what decides
// whether an all-row export can be served forward and what X-Export-Mode may
// truthfully call it.
func PagesNatively(providerType string) bool {
	provider, err := GetProvider(providerType)
	if err != nil {
		return false
	}
	_, ok := provider.(PagingProvider)
	return ok
}

// walkRequest is the page a full forward walk asks for.
//
// It prefers a cursor wherever one is available: it is the only strategy that
// reads past a backend's offset ceiling, and the only one whose cost does not
// grow with how far it has already read. A provider that cannot cursor is
// walked by offset, which is correct and merely slower.
func walkRequest(p Profile, batch int) PageRequest {
	page := PageRequest{Limit: batch}
	// A derivation failure is not decided here: ExecutePages resolves the same
	// order and reports it. This only chooses a strategy, and offset is the
	// correct one for a profile that turns out to have no order at all.
	order, err := p.EffectiveOrder()
	if err == nil && len(order) > 0 && SupportsPaging(p.Provider.Type).Supports(PagingCursor) {
		page.Strategy = PagingCursor
	}
	return page
}

// ExecutePages resolves profile parameters and templates exactly like Execute,
// then yields the requested page and every page after it, applying the
// Profile's CEL columns to each batch as it passes.
//
// The sequence ends when the source is exhausted or the consumer stops ranging.
// A caller wanting a single page breaks after the first, which is also what
// releases the backend cursor.
func ExecutePages(ctx context.Context, p Profile, page PageRequest, params ...map[string]any) iter.Seq2[Page, error] {
	if err := page.Validate(); err != nil {
		return ErrorPage(err)
	}
	if err := p.Validate(); err != nil {
		return ErrorPage(err)
	}
	if p.Kind() == KindTrace {
		return ErrorPage(fmt.Errorf("profile %q is a trace; use ExecuteStream", p.Name))
	}

	// Resolved once and used for both the provider request and the cursor scope
	// below: the scope is what a cursor is validated against, so serving a page
	// under one order and scoping it under another would stale every cursor the
	// walk mints.
	order, err := p.EffectiveOrder()
	if err != nil {
		return ErrorPage(fmt.Errorf("profile %q: %w", p.Name, err))
	}

	// A position past the first row only means something under a total order,
	// so the profile is held to declaring one before it can be asked for one.
	if page.Offset > 0 || !page.Cursor.IsZero() {
		if err := order.Pageable(); err != nil {
			return ErrorPage(fmt.Errorf("profile %q cannot be paged: %w", p.Name, err))
		}
	}
	if mode := page.Mode(); !SupportsPaging(p.Provider.Type).Supports(mode) {
		return ErrorPage(fmt.Errorf("profile %q: provider %q cannot page by %s (it serves %s)",
			p.Name, p.Provider.Type, mode, SupportsPaging(p.Provider.Type)))
	}

	if p.Namespace != "" {
		ctx = ctx.WithNamespace(p.Namespace)
	}
	var supplied map[string]any
	if len(params) > 0 {
		supplied = params[0]
	}
	resolved, filters, err := resolveProfileInput(p, supplied)
	if err != nil {
		return ErrorPage(fmt.Errorf("profile %q: %w", p.Name, err))
	}

	req, err := buildProviderRequest(ctx, p.Provider, p.Query, p.Params, resolved)
	if err != nil {
		return ErrorPage(fmt.Errorf("profile %q: %w", p.Name, err))
	}
	req.Filters = filters
	req.Order = order
	req.Diagnostics = page.Diagnostics
	if req.Diagnostics == nil {
		req.Diagnostics = DiagnosticSink(ctx)
	}
	ctx, req, operation, err := prepareConnectionOperation(ctx, req)
	if err != nil {
		return ErrorPage(fmt.Errorf("profile %q: %w", p.Name, err))
	}
	scope := CursorScope{
		Profile:    p.Name,
		Provider:   p.Provider.Type,
		Connection: req.Connection,
		Query:      req.Query,
		Options:    req.Options,
		Order:      order,
		Params:     resolved,
		Roles:      paramRoles(p.Params),
		Filters:    filters,
	}
	position, err := DecodeCursor(page.Cursor, scope)
	if err != nil {
		return ErrorPage(fmt.Errorf("profile %q: %w", p.Name, err))
	}
	req.Position = position

	// The processors run before the cursor is minted, because a processor that
	// folds rows across pages contributes to the position: the token has to
	// carry what it has already emitted, or the page after it emits the same
	// group again. That is why the chain is assembled here rather than wrapped
	// around ExecutePages by the caller, which is where it used to live.
	rows := withRowTransforms(ctx, p, providerPages(ctx, p, req, page))
	if page.ApplyProcessors && len(p.Processors) > 0 {
		rows = ProcessPages(ctx, p.Processors, position.State, rows)
	}
	return withConnectionLogging(operation, withCursors(scope, rows))
}

// withCursors mints each Page's Next from the keys its provider handed back.
// Providers deal in key values and never in the token format, so a cursor can
// only have been issued — and can only be validated — here.
func withCursors(scope CursorScope, pages iter.Seq2[Page, error]) iter.Seq2[Page, error] {
	return func(yield func(Page, error) bool) {
		for page, err := range pages {
			if err != nil {
				yield(Page{}, err)
				return
			}
			if len(page.NextKeys) > 0 {
				cursor, err := EncodeCursor(scope, page.NextKeys, page.PIT, page.State)
				if err != nil {
					yield(Page{}, err)
					return
				}
				page.Next = cursor
			}
			if !yield(page, nil) {
				return
			}
		}
	}
}

// providerPages dispatches to a provider's native paging, or slices a buffered
// result for one that has none.
func providerPages(ctx context.Context, p Profile, req ProviderRequest, page PageRequest) iter.Seq2[Page, error] {
	provider, err := GetProvider(p.Provider.Type)
	if err != nil {
		return ErrorPage(err)
	}
	if paging, ok := provider.(PagingProvider); ok {
		return paging.Pages(ctx, req, page)
	}
	return func(yield func(Page, error) bool) {
		rows, err := provider.Execute(ctx, req)
		if err != nil {
			yield(Page{}, fmt.Errorf("profile %q: provider %q failed: %w", p.Name, p.Provider.Type, err))
			return
		}
		for pg, err := range bufferedPages(rows, page) {
			if !yield(pg, err) {
				return
			}
		}
	}
}

// bufferedPages serves pages out of an already-materialized result.
//
// It is correct and expensive in equal measure: the whole query ran to produce
// one page of it, and it runs again for the next. That cost is the reason
// PagingModes exists — a provider that can page natively says so, and this path
// is what remains for the ones that cannot.
func bufferedPages(rows []Row, page PageRequest) iter.Seq2[Page, error] {
	return func(yield func(Page, error) bool) {
		total := Total{Value: int64(len(rows)), Exact: true}
		for start := min(page.Offset, len(rows)); ; start += page.Limit {
			end := min(start+page.Limit, len(rows))
			if !yield(Page{
				Rows:    rows[start:end],
				HasMore: end < len(rows),
				Total:   &total,
			}, nil) {
				return
			}
			if end >= len(rows) {
				return
			}
		}
	}
}

// withRowTransforms applies the Profile's aliases, column projections and
// ignores to each batch as it passes, so a consumer that stops early never pays
// for rows it did not read.
func withRowTransforms(ctx context.Context, p Profile, pages iter.Seq2[Page, error]) iter.Seq2[Page, error] {
	return func(yield func(Page, error) bool) {
		index := 0
		for page, err := range pages {
			if err != nil {
				yield(Page{}, err)
				return
			}
			read := len(page.Rows)
			kept, styles, err := applyRowTransforms(ctx, p, page.Rows)
			if err != nil {
				yield(Page{}, fmt.Errorf("profile %q: rows %d-%d: %w", p.Name, index, index+read, err))
				return
			}
			page.Rows, page.Styles = kept, styles
			index += read
			if !yield(page, nil) {
				return
			}
		}
	}
}
