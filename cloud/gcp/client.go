// Package gcp holds thin typed REST clients for the Google Cloud APIs commons-db
// uses. They are built on commons/http with bearer-token authentication, expose
// only the operations and fields commons-db reads, and replace the generated
// cloud.google.com/go and google.golang.org/api client libraries.
package gcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strings"
	"time"

	"golang.org/x/oauth2"

	chttp "github.com/flanksource/commons/http"
)

// Options configures a service client.
type Options struct {
	// Endpoint overrides the service root URL (for example an emulator). The
	// service's version path is appended when the endpoint does not already end
	// with it.
	Endpoint string

	// TokenSource authenticates every request with a bearer token. A nil source
	// sends requests unauthenticated, which is what emulators expect.
	TokenSource oauth2.TokenSource

	// SkipTLSVerify disables server certificate verification.
	SkipTLSVerify bool
}

const (
	retryAttempts = 3
	retryDelay    = 200 * time.Millisecond
	errorBodyMax  = 64 << 10
)

// api is one service client: an authenticated HTTP client plus its base URL.
type api struct {
	http *chttp.Client
	base string
}

// newAPI builds the HTTP client for a service rooted at defaultRoot, whose
// versioned paths start with versionPath.
func newAPI(opts Options, defaultRoot, versionPath string) (*api, error) {
	base, err := serviceBase(opts.Endpoint, defaultRoot, versionPath)
	if err != nil {
		return nil, err
	}
	client, err := newHTTPClient(opts)
	if err != nil {
		return nil, err
	}
	return &api{http: client, base: base}, nil
}

func newHTTPClient(opts Options) (*chttp.Client, error) {
	client := chttp.NewClient().
		RetryStrategy(chttp.RetryOnStatus(retryAttempts, retryDelay, 429, 500, 502, 503, 504))
	if _, err := client.TLSConfig(chttp.TLSConfig{InsecureSkipVerify: opts.SkipTLSVerify}); err != nil {
		return nil, fmt.Errorf("gcp: configuring TLS: %w", err)
	}
	if opts.TokenSource != nil {
		client.BearerToken(opts.TokenSource)
	}
	return client, nil
}

// serviceBase returns the URL requests are made under: the endpoint (or the
// default root) with versionPath appended unless it is already there.
func serviceBase(endpoint, defaultRoot, versionPath string) (string, error) {
	root := defaultRoot
	if endpoint != "" {
		root = endpoint
	}
	root, err := cleanRoot(root)
	if err != nil {
		return "", err
	}
	if strings.HasSuffix(root, versionPath) {
		return root, nil
	}
	return root + versionPath, nil
}

func cleanRoot(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", fmt.Errorf("gcp: invalid endpoint %q: want an absolute http(s) URL", raw)
	}
	parsed.RawQuery, parsed.Fragment = "", ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

// call describes one REST request.
type call struct {
	Method string
	// URL is absolute, query string included.
	URL     string
	Headers map[string]string
	// Body is JSON-encoded unless it is a []byte, string or io.Reader.
	Body any
	// Out receives the decoded JSON response body when non-nil.
	Out any
	// AcceptStatus lists non-2xx statuses that are not errors (e.g. 308 for an
	// acknowledged resumable-upload chunk).
	AcceptStatus []int
}

// withQuery appends encoded query parameters to rawURL.
func withQuery(rawURL string, query url.Values) string {
	if len(query) == 0 {
		return rawURL
	}
	return rawURL + "?" + query.Encode()
}

// send performs the request and returns the response for any 2xx status; the
// caller closes its body. Other statuses become an *APIError.
func (a *api) send(ctx context.Context, c call) (*chttp.Response, error) {
	request := a.http.R(ctx)
	for key, value := range c.Headers {
		request.Header(key, value)
	}
	if c.Body != nil {
		if err := setBody(request, c); err != nil {
			return nil, err
		}
	}
	resp, err := request.Do(c.Method, c.URL)
	if err != nil {
		return nil, fmt.Errorf("gcp: %s %s: %w", c.Method, redactQuery(c.URL), err)
	}
	if !resp.IsOK() && !slices.Contains(c.AcceptStatus, resp.StatusCode) {
		defer resp.Body.Close()
		return nil, parseAPIError(resp.StatusCode, io.LimitReader(resp.Body, errorBodyMax))
	}
	return resp, nil
}

func setBody(request *chttp.Request, c call) error {
	if _, ok := c.Headers["Content-Type"]; !ok {
		switch c.Body.(type) {
		case []byte, string, io.Reader:
		default:
			request.Header("Content-Type", "application/json")
		}
	}
	if err := request.Body(c.Body); err != nil {
		return fmt.Errorf("gcp: %s %s: encoding body: %w", c.Method, redactQuery(c.URL), err)
	}
	return nil
}

// do performs the request, decoding the JSON response into c.Out.
func (a *api) do(ctx context.Context, c call) error {
	resp, err := a.send(ctx, c)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if c.Out == nil {
		_, err = io.Copy(io.Discard, resp.Body)
		return err
	}
	if err := json.NewDecoder(resp.Body).Decode(c.Out); err != nil {
		return fmt.Errorf("gcp: %s %s: decoding response: %w", c.Method, redactQuery(c.URL), err)
	}
	return nil
}

// redactQuery drops the query string so tokens and filters stay out of errors.
func redactQuery(rawURL string) string {
	base, _, _ := strings.Cut(rawURL, "?")
	return base
}
