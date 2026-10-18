package git

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/flanksource/commons-db/observability"
	commonshttp "github.com/flanksource/commons/http"
	"github.com/flanksource/commons/http/middlewares"
)

const (
	restTimeout      = 30 * time.Second
	errorBodyExcerpt = 512
)

// APIError is a non-2xx provider API response.
type APIError struct {
	Provider   string
	Method     string
	URL        string
	StatusCode int
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("%s API %s %s returned HTTP %d: %s", e.Provider, e.Method, e.URL, e.StatusCode, e.Body)
}

// restClient is an HTTPS-only JSON client for one provider API base URL.
type restClient struct {
	provider string
	base     *url.URL
	http     *commonshttp.Client
}

// newRESTClient refuses a non-HTTPS base URL, verifies TLS (unless the
// connection opts out), refuses redirects to non-HTTPS URLs, times out after
// 30s and attaches the "git" observability feature.
func newRESTClient(ctx any, provider, baseURL string, tls TLS, authorization string) (*restClient, error) {
	base, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || base.Scheme != "https" || base.Host == "" {
		return nil, fmt.Errorf("%s API URL must be an HTTPS URL", provider)
	}
	base.RawQuery, base.Fragment = "", ""
	client, err := commonshttp.NewClient().TLSConfig(commonshttp.TLSConfig{
		CA: tls.CA, Cert: tls.Cert, Key: tls.Key, InsecureSkipVerify: tls.InsecureSkipVerify})
	if err != nil {
		return nil, fmt.Errorf("%s API TLS: %w", provider, err)
	}
	client.Timeout(restTimeout).Use(requireHTTPS(provider)).Header("Authorization", authorization).
		Header("Accept", "application/json")
	observability.ApplyHTTPClientObservability(ctx, "git", client, nil)
	return &restClient{provider: provider, base: base, http: client}, nil
}

// requireHTTPS refuses every hop, including redirects, that is not HTTPS.
func requireHTTPS(provider string) middlewares.Middleware {
	return func(next http.RoundTripper) http.RoundTripper {
		return middlewares.RoundTripperFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Scheme != "https" {
				return nil, fmt.Errorf("%s API redirected to a non-HTTPS URL", provider)
			}
			return next.RoundTrip(req)
		})
	}
}

// endpoint is base + an already-escaped path + query.
func (c *restClient) endpoint(path string, query url.Values) string {
	endpoint := c.base.String() + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	return endpoint
}

// do sends body as JSON (when non-nil) to an absolute URL on the base host,
// decodes a 2xx response into out (when non-nil) and returns its headers.
func (c *restClient) do(ctx context.Context, method, endpoint string, body, out any) (http.Header, error) {
	target, err := url.Parse(endpoint)
	if err != nil || !strings.EqualFold(target.Host, c.base.Host) {
		return nil, fmt.Errorf("%s API URL %q is not on %s", c.provider, endpoint, c.base.Host)
	}
	request := c.http.R(ctx)
	if body != nil {
		request.Header("Content-Type", "application/json")
		if err := request.Body(body); err != nil {
			return nil, fmt.Errorf("%s API %s %s: encode body: %w", c.provider, method, endpoint, err)
		}
	}
	response, err := request.Do(method, endpoint)
	if err != nil {
		return nil, fmt.Errorf("%s API %s %s failed: %w", c.provider, method, endpoint, err)
	}
	defer response.Body.Close() //nolint:errcheck // read-only body
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		excerpt, _ := io.ReadAll(io.LimitReader(response.Body, errorBodyExcerpt))
		return nil, &APIError{Provider: c.provider, Method: method, URL: endpoint, StatusCode: response.StatusCode,
			Body: strings.TrimSpace(string(excerpt))}
	}
	if out != nil {
		if err := json.NewDecoder(response.Body).Decode(out); err != nil {
			return nil, fmt.Errorf("%s API %s %s: decode response: %w", c.provider, method, endpoint, err)
		}
	}
	return response.Header, nil
}

func isStatus(err error, status int) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == status
}
