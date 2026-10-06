// Reads the span documents of an opentelemetry connection's index oldest first:
// a window it returns from once caught up, or a follow of what is indexed next.

package providers

import (
	"errors"
	"time"

	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/logs/opensearch"
	"github.com/flanksource/commons-db/query"
)

// OpenTelemetryRead is one read of span documents.
type OpenTelemetryRead struct {
	// Connection is the opentelemetry connection, and Options the
	// opentelemetry provider's options for its index and fields.
	Connection string
	Options    map[string]any
	// From and To bound the read on the date field, as date math such as
	// now-1h or RFC3339; empty leaves that side open.
	From, To string
	// Follow keeps reading the spans indexed after the read caught up, until
	// ctx ends; it has no To.
	Follow bool
	// Poll is how long a caught-up follow waits before asking again; zero is
	// two seconds. Lag holds a follow behind now, so spans indexed late still
	// land before the read passes their instant.
	Poll, Lag time.Duration
}

// ReadOpenTelemetrySpans emits each span read, normalized as the opentelemetry
// provider's rows are, with source_index and source_id naming its document.
// It reads in the date field's order with search_after, opening no point in
// time, so a follow sees what lands after it started.
func ReadOpenTelemetrySpans(ctx context.Context, read OpenTelemetryRead, emit func(query.Row)) error {
	if read.Follow && read.To != "" {
		return errors.New("a followed read has no end: drop To, or read the window without follow")
	}
	req := query.ProviderRequest{
		Connection: read.Connection, Options: read.Options,
		Params: map[string]any{}, ParamRoles: map[string]query.ParamRole{},
	}
	for name, bound := range map[string]struct {
		value string
		role  query.ParamRole
	}{"from": {read.From, query.ParamRoleTimeFrom}, "to": {read.To, query.ParamRoleTimeTo}} {
		if bound.value != "" {
			req.Params[name], req.ParamRoles[name] = bound.value, bound.role
		}
	}
	runtime, err := openTelemetrySearchClient(ctx, req)
	if err != nil {
		return err
	}
	search := openTelemetrySearch(runtime.options)
	mapping, err := ResolveOpenSearchTimeFieldMapping(ctx, OpenSearchTimeFieldMappingRequest{
		Searcher: runtime.searcher, Index: runtime.options.Index, Search: search, Params: openSearchParamBindings(req),
	})
	if err != nil {
		return err
	}
	// Checked now so a lag that cannot be bounded fails the start; each poll
	// then bounds again from its own now, so the cursor keeps moving.
	if _, _, err := openSearchTailBound(&search, read.Lag, mapping, time.Now().UTC()); err != nil {
		return err
	}
	req.Order = query.Order{{Column: search.TimeField}, {Column: openSearchTiebreaker, Unique: true}}
	walk := openSearchWalk{
		searcher: runtime.searcher,
		index:    runtime.options.Index,
		paging:   runtime.paging,
		build: func(position openSearchPage) (openSearchRequest, error) {
			built, err := buildOpenTelemetryRequest(req, runtime.options, position, mapping)
			if err != nil {
				return openSearchRequest{}, err
			}
			boundField, boundValue, err := openSearchTailBound(&search, read.Lag, mapping, time.Now().UTC())
			if err != nil {
				return openSearchRequest{}, err
			}
			applyOpenSearchTailBound(built.body, boundField, boundValue)
			return built, nil
		},
		mapRows: func(raw opensearch.Response) []query.Row {
			return openTelemetryRows(raw, runtime.options)
		},
	}
	settings := openSearchTailSettings{poll: read.Poll, lag: read.Lag, untilCaughtUp: !read.Follow}
	if settings.poll <= 0 {
		settings.poll = openSearchDefaultTailPoll
	}
	return walk.tail(ctx, req, settings, emit)
}
