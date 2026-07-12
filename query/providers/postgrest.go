package providers

import (
	"fmt"
	"io"

	"github.com/flanksource/commons-db/connection"
	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
)

func init() {
	query.RegisterProvider(&postgrestProvider{})
}

// postgrestProvider reads from a PostgREST endpoint. The connection supplies the
// PostgREST base URL; the query is the resource path plus PostgREST filters,
// e.g. "config_items?select=id,name&type=eq.Kubernetes". PostgREST returns a
// JSON array, which becomes the rows.
type postgrestProvider struct{}

func (postgrestProvider) Type() string { return "postgrest" }

type postgrestOptions struct {
	// URL is an inline PostgREST base URL used when no stored connection is referenced.
	URL string `json:"url,omitempty"`

	// JSONPath optionally extracts an inner array from the response.
	JSONPath string `json:"jsonpath,omitempty"`
}

func (postgrestProvider) Execute(ctx context.Context, req query.ProviderRequest) ([]query.Row, error) {
	opts, err := query.DecodeOptions[postgrestOptions](req.Options)
	if err != nil {
		return nil, err
	}

	conn := connection.HTTPConnection{ConnectionName: req.Connection}
	hydrated, err := conn.Hydrate(ctx, ctx.GetNamespace())
	if err != nil {
		return nil, fmt.Errorf("failed to hydrate postgrest connection: %w", err)
	}
	if opts.URL != "" {
		hydrated.URL, err = resolveInlineURL(ctx, opts.URL, "http")
		if err != nil {
			return nil, err
		}
	}

	url := requestURL(hydrated.URL, req.Query)
	if url == "" {
		return nil, fmt.Errorf("postgrest query url is required")
	}

	client, err := connection.CreateHTTPClient(ctx, *hydrated)
	if err != nil {
		return nil, fmt.Errorf("failed to create http client: %w", err)
	}
	connection.ApplyHTTPClientObservability(ctx, "postgrest", client, nil)

	resp, err := client.R(ctx).Header("Accept", "application/json").Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to execute postgrest request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if !resp.IsOK() {
		peek, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		return nil, fmt.Errorf("postgrest request failed with status %d: %s", resp.StatusCode, string(peek))
	}

	maxBodySize := int64(ctx.Properties().Int(bodyMaxSizeProperty, defaultHTTPBodyMaxSizeBytes))
	if maxBodySize <= 0 {
		maxBodySize = defaultHTTPBodyMaxSizeBytes
	}

	body, err := readHTTPBodyWithLimit(resp.Body, resp.ContentLength, maxBodySize)
	if err != nil {
		return nil, err
	}
	if len(body) == 0 {
		return []query.Row{}, nil
	}

	if opts.JSONPath != "" {
		body, err = applyJSONPath(body, opts.JSONPath)
		if err != nil {
			return nil, err
		}
	}

	return transformHTTPResult(body)
}
