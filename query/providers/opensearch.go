package providers

import (
	"fmt"
	"iter"
	"net/http"
	"strings"
	"time"

	dbconnection "github.com/flanksource/commons-db/connection"
	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/logs/opensearch"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/esdsl"
)

func init() {
	query.RegisterProvider(&opensearchProvider{})
}

// opensearchProvider runs a query against an OpenSearch index and returns one
// row per hit. HAR capture is maintained by the underlying searcher (feature
// "opensearch"). The connection is resolved from req.Connection; an inline
// address may be supplied via options.
type opensearchProvider struct{}

func (opensearchProvider) Type() string { return "opensearch" }

type opensearchOptions struct {
	// Address is an inline OpenSearch URL used when no stored connection is referenced.
	Address string `json:"address,omitempty"`

	// Index is the OpenSearch index to query.
	Index string `json:"index,omitempty"`

	// Limit is the maximum number of hits to return.
	Limit string `json:"limit,omitempty"`

	// Search is the structured search specification. It is mutually exclusive
	// with the profile's raw query.
	Search *esdsl.Search `json:"search,omitempty"`

	// TailPoll is how long a caught-up tail waits before asking the index
	// again. Empty is openSearchDefaultTailPoll. It has no effect on a query.
	TailPoll string `json:"tailPoll,omitempty"`

	// TailLag holds a tail's cursor that far behind now, so a document indexed
	// later than it was written still lands ahead of the cursor rather than
	// behind it. Empty is no lag: lines appear as soon as they are searchable,
	// and one that arrives late is never emitted. See openSearchTailBound.
	TailLag string `json:"tailLag,omitempty"`
}

type openSearchPagingMode string

const (
	openSearchPagingScroll openSearchPagingMode = models.OpenSearchPagingModeScroll
	openSearchPagingPIT    openSearchPagingMode = models.OpenSearchPagingModePIT
)

type openSearchRuntime struct {
	searcher *opensearch.Searcher
	options  opensearchOptions
	paging   openSearchPagingMode
}

// PagingModes reports both caller-facing strategies. Offset is served by
// from/size inside the index result window. Cursor paging uses the connection's
// configured OpenSearch backend cursor and works at any depth.
func (opensearchProvider) PagingModes() query.PagingMode {
	return query.PagingOffset | query.PagingCursor
}

// SupportsRequestSort reports yes: the order is rendered into the search body's
// own sort, and search_after keys are read back from it.
func (opensearchProvider) SupportsRequestSort() bool { return true }

func (p opensearchProvider) Execute(ctx context.Context, req query.ProviderRequest) ([]query.Row, error) {
	return drainOpenSearch(ctx, p, req)
}

// Pages walks the index with the connection's scroll or point-in-time cursor
// when requested, and by from/size otherwise. Both cursor backends keep one
// stable index view across the walk and release it after the last page.
func (p opensearchProvider) Pages(ctx context.Context, req query.ProviderRequest, page query.PageRequest) iter.Seq2[query.Page, error] {
	return func(yield func(query.Page, error) bool) {
		runtime, err := openSearchClient(ctx, req)
		if err != nil {
			yield(query.Page{}, err)
			return
		}
		var timeFieldMapping *esdsl.TimeFieldMapping
		if runtime.options.Search != nil {
			timeFieldMapping, err = ResolveOpenSearchTimeFieldMapping(ctx, OpenSearchTimeFieldMappingRequest{
				Searcher: runtime.searcher, Index: runtime.options.Index, Search: *runtime.options.Search,
				Params: openSearchParamBindings(req), Inspection: req.Inspection,
			})
			if err != nil {
				yield(query.Page{}, err)
				return
			}
		}
		walk := openSearchWalk{
			searcher:    runtime.searcher,
			index:       runtime.options.Index,
			paging:      runtime.paging,
			diagnostics: req.Diagnostics,
			build: func(position openSearchPage) (openSearchRequest, error) {
				return buildOpenSearchRequest(req, runtime.options, position, timeFieldMapping)
			},
			mapRows: func(raw opensearch.Response) []query.Row {
				return logResultToRows(runtime.searcher.ParseResponse(ctx, raw))
			},
		}
		walk.run(ctx, req, page, yield)
	}
}

// Stream tails the index by re-asking for the documents written past the last
// one it emitted.
//
// OpenSearch has no tail: its whole read surface is request/response, so unlike
// Loki's websocket or the kubelet's held-open read this is a poll, and the two
// consequences are worth naming because neither is visible in the rows.
//
// It opens no point-in-time. A PIT is what makes a cursor walk read one
// unchanging view of the index, and it is exactly wrong here — a frozen view
// cannot contain a document that did not exist when it was taken, which is the
// only kind of document a tail is waiting for.
//
// And a document is emitted only if it is indexed before the cursor passes its
// instant. Ingest is not instantaneous, so one buffered past that point is
// skipped and nothing here can tell: search_after moves forward and never looks
// back. options.tailLag holds the cursor behind now to leave room for it.
func (p opensearchProvider) Stream(ctx context.Context, req query.ProviderRequest, emit func(query.Row)) error {
	runtime, err := openSearchClient(ctx, req)
	if err != nil {
		return err
	}
	settings, err := openSearchTailOptions(runtime.options)
	if err != nil {
		return err
	}
	tailOrder, err := openSearchTailOrder(req, runtime.options)
	if err != nil {
		return err
	}
	// A cursor's keys are the sort values of the order that cut them, so keys
	// cut under the profile's order would resume a tail sorted differently at
	// whatever position those values happen to name — silently, and in the
	// wrong place.
	if len(req.Position.Keys) > 0 && tailOrder.Fingerprint() != req.Order.Fingerprint() {
		return fmt.Errorf(
			"this cursor was cut under %v but a tail follows %v, so it cannot resume from it",
			req.Order.Columns(), tailOrder.Columns())
	}

	var timeFieldMapping *esdsl.TimeFieldMapping
	if runtime.options.Search != nil {
		// Resolved once, before the first poll: the mapping carries the instant
		// the window's date math resolves against, so re-resolving it per poll
		// would slide the tail's lower bound forward under it.
		timeFieldMapping, err = ResolveOpenSearchTimeFieldMapping(ctx, OpenSearchTimeFieldMappingRequest{
			Searcher: runtime.searcher, Index: runtime.options.Index, Search: *runtime.options.Search,
			Params: openSearchParamBindings(req), Inspection: req.Inspection,
		})
		if err != nil {
			return err
		}
	}
	boundField, boundValue, err := openSearchTailBound(runtime.options, settings.lag, timeFieldMapping, time.Now().UTC())
	if err != nil {
		return err
	}

	// The tail reads in its own ascending order rather than the profile's — see
	// openSearchTailOrder — and the request is what carries that to the sort.
	tailReq := req
	tailReq.Order = tailOrder

	walk := openSearchWalk{
		searcher:    runtime.searcher,
		index:       runtime.options.Index,
		paging:      runtime.paging,
		diagnostics: req.Diagnostics,
		build: func(position openSearchPage) (openSearchRequest, error) {
			built, err := buildOpenSearchRequest(tailReq, runtime.options, position, timeFieldMapping)
			if err != nil {
				return openSearchRequest{}, err
			}
			applyOpenSearchTailBound(built.body, boundField, boundValue)
			return built, nil
		},
		mapRows: func(raw opensearch.Response) []query.Row {
			return logResultToRows(runtime.searcher.ParseResponse(ctx, raw))
		},
	}
	return walk.tail(ctx, tailReq, settings, emit)
}

func openSearchClient(ctx context.Context, req query.ProviderRequest) (openSearchRuntime, error) {
	opts, err := query.DecodeOptions[opensearchOptions](req.Options)
	if err != nil {
		return openSearchRuntime{}, err
	}

	address, err := resolveInlineURL(ctx, opts.Address, "opensearch")
	if err != nil {
		return openSearchRuntime{}, err
	}
	backend := opensearch.Backend{Address: address}
	paging := openSearchPagingScroll
	var transport http.RoundTripper
	if req.Connection != "" {
		conn, err := ctx.HydrateConnectionByURL(req.Connection)
		if err != nil {
			return openSearchRuntime{}, fmt.Errorf("could not hydrate connection[%s]: %w", req.Connection, err)
		}
		if conn == nil {
			return openSearchRuntime{}, fmt.Errorf("connection[%s] not found", req.Connection)
		}
		paging, err = resolveOpenSearchPagingMode(conn)
		if err != nil {
			return openSearchRuntime{}, fmt.Errorf("connection[%s]: %w", req.Connection, err)
		}
		if backend.Address == "" {
			backend.Address = conn.URL
		}
		backend.InspectionKey = fmt.Sprintf("%s:connection:%s:%d", ctx.ConnectionCacheScope(), conn.ID, conn.UpdatedAt.UnixNano())
		httpConnection, err := dbconnection.NewHTTPConnection(ctx, *conn)
		if err != nil {
			return openSearchRuntime{}, err
		}
		transport = httpConnection.Transport()
	}
	if backend.Address == "" {
		return openSearchRuntime{}, fmt.Errorf("opensearch address is required")
	}
	transport = dbconnection.ApplyHTTPObservability(ctx, "opensearch", transport, nil)

	// A debug run watches the wire as well as the body: which URL the search
	// went to and which headers the connection put on it are half of "where did
	// these rows come from", and neither is anywhere in the profile.
	searcher, err := opensearch.NewWithTransport(ctx, backend, nil, req.Diagnostics.HTTPTransport(transport))
	if err != nil {
		return openSearchRuntime{}, err
	}
	return openSearchRuntime{searcher: searcher, options: opts, paging: paging}, nil
}

func openSearchClientForConnection(ctx context.Context, conn *models.Connection) (openSearchRuntime, error) {
	if conn == nil || conn.URL == "" {
		return openSearchRuntime{}, fmt.Errorf("OpenSearch connection URL is required")
	}
	paging, err := resolveOpenSearchPagingMode(conn)
	if err != nil {
		return openSearchRuntime{}, err
	}
	httpConnection, err := dbconnection.NewHTTPConnection(ctx, *conn)
	if err != nil {
		return openSearchRuntime{}, err
	}
	transport := dbconnection.ApplyHTTPObservability(ctx, "opensearch", httpConnection.Transport(), nil)
	searcher, err := opensearch.NewWithTransport(ctx, opensearch.Backend{
		Address: conn.URL, InspectionKey: fmt.Sprintf("%s:connection:%s:%d", ctx.ConnectionCacheScope(), conn.ID, conn.UpdatedAt.UnixNano()),
	}, nil, transport)
	if err != nil {
		return openSearchRuntime{}, err
	}
	return openSearchRuntime{searcher: searcher, paging: paging}, nil
}

func resolveOpenSearchPagingMode(conn *models.Connection) (openSearchPagingMode, error) {
	value := ""
	if conn != nil {
		value = strings.ToLower(strings.TrimSpace(conn.Properties[models.OpenSearchPropertyPagingMode]))
	}
	switch value {
	case "", models.OpenSearchPagingModeScroll:
		return openSearchPagingScroll, nil
	case models.OpenSearchPagingModePIT:
		return openSearchPagingPIT, nil
	default:
		return "", fmt.Errorf("invalid OpenSearch paging mode %q; expected %q or %q",
			value, models.OpenSearchPagingModeScroll, models.OpenSearchPagingModePIT)
	}
}
