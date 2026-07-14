package providers

import (
	stdcontext "context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	dbconnection "github.com/flanksource/commons-db/connection"
	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/logs/opensearch"
	"github.com/flanksource/commons-db/query"
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
}

func (opensearchProvider) Execute(ctx context.Context, req query.ProviderRequest) ([]query.Row, error) {
	searcher, opts, err := openSearchClient(ctx, req)
	if err != nil {
		return nil, err
	}
	result, err := searcher.Search(ctx, opensearch.Request{
		Index: opts.Index,
		Query: req.Query,
		Limit: opts.Limit,
	})
	if err != nil {
		return nil, err
	}
	return logResultToRows(result), nil
}

func (opensearchProvider) OpenRows(ctx context.Context, req query.ProviderRequest) (query.RowIterator, error) {
	searcher, opts, err := openSearchClient(ctx, req)
	if err != nil {
		return nil, err
	}
	limit := 0
	if opts.Limit != "" {
		limit, err = strconv.Atoi(opts.Limit)
		if err != nil || limit < 0 {
			return nil, fmt.Errorf("invalid opensearch limit %q", opts.Limit)
		}
	}
	if req.MaxRows > 0 {
		boundedLimit := req.MaxRows
		if limit > 0 && limit < boundedLimit {
			boundedLimit = limit
		}
		result, err := searcher.Search(ctx, opensearch.Request{
			Index: opts.Index,
			Query: req.Query,
			Limit: strconv.Itoa(boundedLimit),
		})
		if err != nil {
			return nil, err
		}
		return query.SliceRows(logResultToRows(result)), nil
	}
	batchSize := 1000
	if limit > 0 && limit < batchSize {
		batchSize = limit
	}
	result, scrollID, err := searcher.SearchWithScroll(ctx, opensearch.ScrollRequest{
		Request: opensearch.Request{Index: opts.Index, Query: req.Query},
		Scroll:  opensearch.ScrollOptions{Enabled: true, Size: batchSize, Timeout: time.Minute},
	})
	if err != nil {
		return nil, err
	}
	return &openSearchRowIterator{
		ctx:        ctx,
		cleanupCtx: context.NewContext(stdcontext.WithoutCancel(ctx.Context)),
		searcher:   searcher,
		scrollID:   scrollID,
		rows:       logResultToRows(result),
		limit:      limit,
	}, nil
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

type openSearchRowIterator struct {
	ctx        context.Context
	cleanupCtx context.Context
	searcher   *opensearch.Searcher
	scrollID   string
	rows       []query.Row
	index      int
	count      int
	limit      int
	row        query.Row
	err        error
	closed     bool
}

func (i *openSearchRowIterator) Next() bool {
	if i.err != nil || i.closed || (i.limit > 0 && i.count >= i.limit) {
		return false
	}
	for i.index >= len(i.rows) {
		if len(i.rows) == 0 || i.scrollID == "" {
			return false
		}
		result, nextID, err := i.searcher.ScrollNext(i.ctx, i.scrollID, time.Minute)
		if err != nil {
			i.err = err
			return false
		}
		i.scrollID = nextID
		i.rows = logResultToRows(result)
		i.index = 0
	}
	i.row = i.rows[i.index]
	i.index++
	i.count++
	return true
}

func (i *openSearchRowIterator) Row() query.Row { return i.row }
func (i *openSearchRowIterator) Err() error     { return i.err }
func (i *openSearchRowIterator) Close() error {
	if i.closed {
		return nil
	}
	i.closed = true
	if i.scrollID == "" {
		return nil
	}
	cleanup, cancel := i.cleanupCtx.WithTimeout(10 * time.Second)
	defer cancel()
	return i.searcher.ClearScroll(cleanup, i.scrollID)
}
