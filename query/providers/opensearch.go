package providers

import (
	"fmt"
	"iter"
	"net/http"

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
}

// PagingModes reports both strategies. Offset is served by from/size and is
// only usable inside the index result window; cursor paging is served by
// search_after over a point-in-time and works at any depth, which is why the
// window is a refusal rather than a silent switch.
func (opensearchProvider) PagingModes() query.PagingMode {
	return query.PagingOffset | query.PagingCursor
}

func (p opensearchProvider) Execute(ctx context.Context, req query.ProviderRequest) ([]query.Row, error) {
	var result []query.Row
	for page, err := range p.Pages(ctx, req, query.PageRequest{Limit: openSearchWalkBatch}) {
		if err != nil {
			return nil, err
		}
		result = append(result, page.Rows...)
	}
	return result, nil
}

// Pages walks the index by search_after when the profile declares an order, and
// by from/size when it does not.
//
// The cursoring walk pins a point-in-time for its whole length, so every page
// reads the same view of the index; ending the range closes it. The from/size
// walk cannot go past the index result window and says so rather than quietly
// changing mechanism at the boundary, which is what a scroll used to do.
func (p opensearchProvider) Pages(ctx context.Context, req query.ProviderRequest, page query.PageRequest) iter.Seq2[query.Page, error] {
	return func(yield func(query.Page, error) bool) {
		searcher, opts, err := openSearchClient(ctx, req)
		if err != nil {
			yield(query.Page{}, err)
			return
		}
		walk := openSearchWalk{
			searcher: searcher,
			index:    opts.Index,
			build: func(position openSearchPage) (openSearchRequest, error) {
				return buildOpenSearchRequest(req, opts, position)
			},
			mapRows: func(raw opensearch.Response) []query.Row {
				return logResultToRows(searcher.ParseResponse(ctx, raw))
			},
		}
		walk.run(ctx, req, page, yield)
	}
}

func openSearchClient(ctx context.Context, req query.ProviderRequest) (*opensearch.Searcher, opensearchOptions, error) {
	opts, err := query.DecodeOptions[opensearchOptions](req.Options)
	if err != nil {
		return nil, opts, err
	}

	address, err := resolveInlineURL(ctx, opts.Address, "opensearch")
	if err != nil {
		return nil, opts, err
	}
	backend := opensearch.Backend{Address: address}
	var transport http.RoundTripper
	if req.Connection != "" {
		conn, err := ctx.HydrateConnectionByURL(req.Connection)
		if err != nil {
			return nil, opts, fmt.Errorf("could not hydrate connection[%s]: %w", req.Connection, err)
		}
		if conn == nil {
			return nil, opts, fmt.Errorf("connection[%s] not found", req.Connection)
		}
		if backend.Address == "" {
			backend.Address = conn.URL
		}
		httpConnection, err := dbconnection.NewHTTPConnection(ctx, *conn)
		if err != nil {
			return nil, opts, err
		}
		transport = httpConnection.Transport()
	}
	if backend.Address == "" {
		return nil, opts, fmt.Errorf("opensearch address is required")
	}

	searcher, err := opensearch.NewWithTransport(ctx, backend, nil, transport)
	if err != nil {
		return nil, opts, err
	}
	return searcher, opts, nil
}

func openSearchClientForConnection(ctx context.Context, conn *models.Connection) (*opensearch.Searcher, error) {
	if conn == nil || conn.URL == "" {
		return nil, fmt.Errorf("OpenSearch connection URL is required")
	}
	httpConnection, err := dbconnection.NewHTTPConnection(ctx, *conn)
	if err != nil {
		return nil, err
	}
	return opensearch.NewWithTransport(ctx, opensearch.Backend{Address: conn.URL}, nil, httpConnection.Transport())
}
