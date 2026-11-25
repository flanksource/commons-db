// Package azure provides thin typed REST clients for the Azure services
// commons-db uses (Key Vault keys, Log Analytics queries), built on
// commons/http with SDK-free Azure AD bearer tokens.
package azure

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"time"

	commonshttp "github.com/flanksource/commons/http"
	"github.com/flanksource/commons/http/middlewares/azuread"
)

const (
	retryAttempts = 3
	retryDelay    = 200 * time.Millisecond
)

// newClient returns an HTTP client that authenticates every request with a
// token for scope obtained from cred. TLS verification is always configured
// explicitly (commons/http otherwise flips a nil config to insecure), and the
// transient statuses azcore used to retry are retried.
func newClient(cred azuread.Credential, scope string, skipTLSVerify bool) (*commonshttp.Client, error) {
	if cred == nil {
		return nil, errors.New("azure: credential is required")
	}
	if scope == "" {
		return nil, errors.New("azure: token scope is required")
	}
	client := commonshttp.NewClient().
		RetryStrategy(commonshttp.RetryOnStatus(retryAttempts, retryDelay, 408, 429, 500, 502, 503, 504))
	if _, err := client.TLSConfig(commonshttp.TLSConfig{InsecureSkipVerify: skipTLSVerify}); err != nil {
		return nil, fmt.Errorf("azure: configuring TLS: %w", err)
	}
	return client.BearerToken(azuread.TokenSource(cred, scope)), nil
}

// postJSON POSTs in as JSON to rawURL and decodes a 2xx response into out.
// Any other status becomes an *APIError.
func postJSON(ctx context.Context, client *commonshttp.Client, rawURL string, query url.Values, in, out any) error {
	req := client.R(ctx).Header("Content-Type", "application/json").Header("Accept", "application/json")
	for k, vs := range query {
		for _, v := range vs {
			req = req.QueryParamAdd(k, v)
		}
	}
	resp, err := req.Post(rawURL, in)
	if err != nil {
		return fmt.Errorf("POST %s: %w", rawURL, err)
	}
	defer resp.Body.Close()
	if !resp.IsOK() {
		return newAPIError(resp)
	}
	if err := resp.Into(out); err != nil {
		return fmt.Errorf("decoding response from %s: %w", rawURL, err)
	}
	return nil
}
