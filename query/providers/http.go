package providers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	commonshttp "github.com/flanksource/commons/http"
	"github.com/flanksource/commons/text"
	"github.com/ohler55/ojg/jp"

	"github.com/flanksource/commons-db/connection"
	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
)

const (
	defaultHTTPBodyMaxSizeBytes = 25 * 1024 * 1024
	bodyMaxSizeProperty         = "query.http.body.max_size_bytes"
)

func init() {
	query.RegisterProvider(&httpProvider{})
}

// httpProvider executes an HTTP request and extracts rows from the JSON response.
// Ported from duty/dataquery/http.go.
type httpProvider struct{}

func (httpProvider) Type() string { return "http" }

// httpOptions are decoded from ProviderRequest.Options.
type httpOptions struct {
	// Method is the HTTP method (default GET).
	Method string `json:"method,omitempty"`

	// Body is the request body for POST/PUT/PATCH.
	Body string `json:"body,omitempty"`

	// JSONPath extracts an inner array/object from a wrapper response,
	// e.g. "$.Traces" for {"Traces": [...]}.
	JSONPath string `json:"jsonpath,omitempty"`
}

func (httpProvider) Execute(ctx context.Context, req query.ProviderRequest) ([]query.Row, error) {
	opts, err := query.DecodeOptions[httpOptions](req.Options)
	if err != nil {
		return nil, err
	}

	conn := connection.HTTPConnection{ConnectionName: req.Connection}
	hydrated, err := conn.Hydrate(ctx, ctx.GetNamespace())
	if err != nil {
		return nil, fmt.Errorf("failed to hydrate http connection: %w", err)
	}

	url := requestURL(hydrated.URL, req.Query)
	if url == "" {
		return nil, fmt.Errorf("http query url is required")
	}

	method := strings.ToUpper(opts.Method)
	if method == "" {
		method = http.MethodGet
	}

	client, err := connection.CreateHTTPClient(ctx, *hydrated)
	if err != nil {
		return nil, fmt.Errorf("failed to create http client: %w", err)
	}
	// Maintain HAR capture / HTTP logging driven by the context's effective level.
	connection.ApplyHTTPClientObservability(ctx, "http", client, nil)

	httpReq := client.R(ctx)

	var resp *commonshttp.Response
	switch method {
	case http.MethodGet:
		resp, err = httpReq.Get(url)
	case http.MethodPost:
		resp, err = httpReq.Post(url, opts.Body)
	case http.MethodPut:
		resp, err = httpReq.Put(url, opts.Body)
	case http.MethodDelete:
		resp, err = httpReq.Delete(url)
	case http.MethodPatch:
		resp, err = httpReq.Patch(url, opts.Body)
	default:
		return nil, fmt.Errorf("unsupported http method: %s", method)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to execute http request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if !resp.IsOK() {
		peek, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		return nil, fmt.Errorf("http request failed with status %d: %s", resp.StatusCode, string(peek))
	}

	maxBodySize := int64(ctx.Properties().Int(bodyMaxSizeProperty, defaultHTTPBodyMaxSizeBytes))
	if maxBodySize <= 0 {
		maxBodySize = defaultHTTPBodyMaxSizeBytes
	}

	if !resp.IsJSON() {
		contentType := resp.Header.Get("Content-Type")
		if contentType == "" {
			contentType = "<empty>"
		}
		return nil, fmt.Errorf("http response content-type is not json: %s", contentType)
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

// requestURL resolves the effective request URL: a full URL in query overrides
// the connection base; a relative query is joined onto the base.
func requestURL(base, q string) string {
	if q == "" {
		return base
	}
	if strings.HasPrefix(q, "http://") || strings.HasPrefix(q, "https://") {
		return q
	}
	if base == "" {
		return q
	}
	return strings.TrimRight(base, "/") + "/" + strings.TrimLeft(q, "/")
}

// readHTTPBodyWithLimit reads the response body with a size guard.
func readHTTPBodyWithLimit(r io.Reader, contentLength int64, maxBytes int64) ([]byte, error) {
	if contentLength > 0 && contentLength > maxBytes {
		return nil, fmt.Errorf("http response body size (%s) exceeds maximum allowed (%s); increase limit via property %q",
			text.HumanizeBytes(contentLength), text.HumanizeBytes(maxBytes), bodyMaxSizeProperty)
	}

	limitedReader := &io.LimitedReader{R: r, N: maxBytes + 1}
	body, err := io.ReadAll(limitedReader)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if int64(len(body)) > maxBytes {
		return nil, fmt.Errorf("http response body exceeds maximum allowed (%s); increase limit via property %q",
			text.HumanizeBytes(maxBytes), bodyMaxSizeProperty)
	}

	return body, nil
}

// applyJSONPath extracts data from a JSON body using a JSONPath expression.
func applyJSONPath(body []byte, jsonPath string) ([]byte, error) {
	expr, err := jp.ParseString(jsonPath)
	if err != nil {
		return nil, fmt.Errorf("invalid jsonPath expression %q: %w", jsonPath, err)
	}

	var data any
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, fmt.Errorf("failed to parse json for jsonPath extraction: %w", err)
	}

	results := expr.Get(data)
	if len(results) == 0 {
		return nil, fmt.Errorf("jsonPath %q matched no data", jsonPath)
	}

	var extracted any
	if len(results) == 1 {
		extracted = results[0]
	} else {
		extracted = results
	}

	return json.Marshal(extracted)
}

// transformHTTPResult converts a JSON response (array or object) into rows.
func transformHTTPResult(body []byte) ([]query.Row, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return []query.Row{}, nil
	}

	if trimmed[0] == '[' {
		var items []query.Row
		if err := json.Unmarshal(trimmed, &items); err != nil {
			return nil, fmt.Errorf("failed to parse json response array: %w", err)
		}
		return items, nil
	}

	var item map[string]any
	if err := json.Unmarshal(trimmed, &item); err != nil {
		return nil, fmt.Errorf("failed to parse json response object: %w", err)
	}

	return []query.Row{query.Row(item)}, nil
}
