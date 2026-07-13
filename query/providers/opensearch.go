package providers

import (
	"fmt"
	"net/http"

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
	opts, err := query.DecodeOptions[opensearchOptions](req.Options)
	if err != nil {
		return nil, err
	}

	address, err := resolveInlineURL(ctx, opts.Address, "opensearch")
	if err != nil {
		return nil, err
	}
	backend := opensearch.Backend{Address: address}
	var transport http.RoundTripper
	if req.Connection != "" {
		conn, err := ctx.HydrateConnectionByURL(req.Connection)
		if err != nil {
			return nil, fmt.Errorf("could not hydrate connection[%s]: %w", req.Connection, err)
		}
		if conn == nil {
			return nil, fmt.Errorf("connection[%s] not found", req.Connection)
		}
		if backend.Address == "" {
			backend.Address = conn.URL
		}
		httpConnection, err := dbconnection.NewHTTPConnection(ctx, *conn)
		if err != nil {
			return nil, err
		}
		transport = httpConnection.Transport()
	}
	if backend.Address == "" {
		return nil, fmt.Errorf("opensearch address is required")
	}

	searcher, err := opensearch.NewWithTransport(ctx, backend, nil, transport)
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
